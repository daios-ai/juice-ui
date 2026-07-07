package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/daios-ai/juice-ui/client"
	"github.com/daios-ai/juice-ui/session"
	"github.com/gorilla/csrf"
)

// Options wires the server's dependencies.
type Options struct {
	Client         *client.Client
	Sessions       *session.Manager
	Renderer       *Renderer
	Logger         *slog.Logger
	CSRFKey        []byte   // 32 bytes
	Secure         bool     // set Secure flag on CSRF/session cookies (false for local http)
	TrustedOrigins []string // extra hosts accepted as the request Origin (proxied hostnames)
}

// Server holds dependencies and serves the BFF.
type Server struct {
	client   *client.Client
	sessions *session.Manager
	caller   *Caller
	render   *Renderer
	log      *slog.Logger
	secure   bool

	// Cached kernel identity (handle + key fingerprint) from the open /health,
	// so the login page and sidebar can name/verify the kernel. Fetched once on
	// the first successful render; retried until then.
	idMu         sync.Mutex
	kernelHandle string
	kernelFP     string
	idFetched    bool
}

// kernelIdentity returns the kernel's handle and a short key fingerprint from
// the open /health endpoint, cached after the first success (identity is
// stable). Empty strings when the kernel hasn't answered yet — templates then
// fall back to the plain brand.
func (s *Server) kernelIdentity(ctx context.Context) (handle, fp string) {
	s.idMu.Lock()
	fetched, h, f := s.idFetched, s.kernelHandle, s.kernelFP
	s.idMu.Unlock()
	if fetched {
		return h, f
	}
	hc, err := s.client.Health(ctx)
	if err != nil || hc.Handle == "" {
		return "", "" // retry on a later render
	}
	handle, fp = hc.Handle, keyFingerprint(hc.PublicKey)
	s.idMu.Lock()
	s.kernelHandle, s.kernelFP, s.idFetched = handle, fp, true
	s.idMu.Unlock()
	return handle, fp
}

// keyFingerprint shortens a base64url public key to a head…tail form for
// eyeball verification. The key itself is the canonical global name, so a
// truncation of it is the value to compare against (no hashing).
func keyFingerprint(key string) string {
	if len(key) <= 14 {
		return key
	}
	return key[:10] + "…" + key[len(key)-4:]
}

// New builds the BFF HTTP handler: routes plus the middleware chain
// (request-id, structured logging, session load/save, CSRF, R5.5/R8.4).
func New(o Options) (http.Handler, error) {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	s := &Server{
		client:   o.Client,
		sessions: o.Sessions,
		caller:   NewCaller(o.Client),
		render:   o.Renderer,
		log:      o.Logger,
		secure:   o.Secure,
	}

	mux := http.NewServeMux()
	// Static assets (open).
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(StaticFS()))))
	// Bare /favicon.ico for browsers that probe the root (and older ones that
	// ignore the SVG <link rel=icon>); the raster fallback lives in static.
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFileFS(w, r, StaticFS(), "favicon.ico")
	})

	// Public auth routes.
	mux.HandleFunc("GET /login", s.getLogin)
	mux.HandleFunc("POST /login", s.postLogin)
	mux.HandleFunc("POST /signup", s.postSignup)
	mux.HandleFunc("POST /logout", s.postLogout)
	mux.HandleFunc("POST /theme", s.postTheme)
	mux.HandleFunc("POST /sidebar", s.postSidebar)

	// Home + legacy redirects (bookmarks / old links). The nav now mirrors the
	// CLI command groups, so the old room URLs point at their new homes.
	mux.HandleFunc("GET /{$}", s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/run", http.StatusSeeOther)
	}))
	for from, to := range map[string]string{
		"/discover": "/run", "/inbox": "/steps", "/studio": "/actions",
		"/settings": "/user", "/wallet": "/user",
	} {
		dest := to
		mux.HandleFunc("GET "+from, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, dest, http.StatusMovedPermanently)
		})
	}

	// Run — discovery and execution.
	mux.HandleFunc("GET /run", s.requireAuth(s.getRun))
	mux.HandleFunc("POST /run", s.requireAuth(s.postRunByRef))

	// Action — catalog + provider workspace.
	mux.HandleFunc("GET /actions", s.requireAuth(s.getActions))
	mux.HandleFunc("POST /actions", s.requireAuth(s.postCreateAction))
	mux.HandleFunc("POST /actions/import", s.requireAuth(s.postImport))
	mux.HandleFunc("POST /actions/unimport", s.requireAuth(s.postUnimport))
	mux.HandleFunc("POST /actions/make", s.requireAuth(s.postMakeAction))
	mux.HandleFunc("GET /actions/{id}", s.requireAuth(s.getAction))
	mux.HandleFunc("POST /actions/{id}/run", s.requireAuth(s.postRun))
	mux.HandleFunc("POST /actions/{id}/enable", s.requireAuth(s.postActionLifecycle))
	mux.HandleFunc("POST /actions/{id}/disable", s.requireAuth(s.postActionLifecycle))
	mux.HandleFunc("POST /actions/{id}/delete", s.requireAuth(s.postActionDelete))
	mux.HandleFunc("POST /actions/{id}/make-public", s.requireAuth(s.postActionLifecycle))
	mux.HandleFunc("POST /actions/{id}/make-private", s.requireAuth(s.postActionLifecycle))
	mux.HandleFunc("POST /actions/{id}/update-price", s.requireAuth(s.postUpdateActionPrice))
	mux.HandleFunc("POST /actions/{id}/update", s.requireAuth(s.postUpdateAction))

	// Process — computation lifetimes (evidence).
	mux.HandleFunc("GET /processes", s.requireAuth(s.getProcesses))
	mux.HandleFunc("GET /processes/{id}", s.requireAuth(s.getProcess))
	mux.HandleFunc("POST /processes/{id}/end", s.requireAuth(s.postEnd))

	// Tx — the immutable ledger.
	mux.HandleFunc("GET /transactions", s.requireAuth(s.getTransactions))
	mux.HandleFunc("GET /transactions/{id}", s.requireAuth(s.getTransaction))
	mux.HandleFunc("GET /transactions/{id}/verify", s.requireAuth(s.getTxVerify))
	mux.HandleFunc("POST /transactions/{id}/rate", s.requireAuth(s.postRate))

	// Step — work addressed to you.
	mux.HandleFunc("GET /steps", s.requireAuth(s.getSteps))
	mux.HandleFunc("GET /steps/badge", s.requireAuth(s.getStepsBadge))
	mux.HandleFunc("GET /steps/{id}", s.requireAuth(s.getStep))
	mux.HandleFunc("POST /steps", s.requireAuth(s.postCreateStep))
	mux.HandleFunc("POST /steps/{id}/complete", s.requireAuth(s.postCompleteStep))

	// User — identity + wallet.
	mux.HandleFunc("GET /user", s.requireAuth(s.getUser))
	mux.HandleFunc("POST /user", s.requireAuth(s.postUser))

	// Admin — superuser-only supervision (money, access, federation trust).
	mux.HandleFunc("GET /admin", s.requireSuperuser(s.getAdmin))
	mux.HandleFunc("GET /admin/users/{handle}", s.requireSuperuser(s.getAdminUser))
	mux.HandleFunc("POST /admin/users/{handle}/suspend", s.requireSuperuser(s.postAdminUserOp))
	mux.HandleFunc("POST /admin/users/{handle}/unsuspend", s.requireSuperuser(s.postAdminUserOp))
	mux.HandleFunc("POST /admin/deposit", s.requireSuperuser(s.postAdminAdjust))
	mux.HandleFunc("POST /admin/withdraw", s.requireSuperuser(s.postAdminAdjust))
	mux.HandleFunc("GET /admin/peers", s.requireSuperuser(s.getAdminPeers))
	mux.HandleFunc("GET /admin/peers/{key}", s.requireSuperuser(s.getAdminInspect))
	mux.HandleFunc("POST /admin/peers/friend", s.requireSuperuser(s.postAdminFriend))
	mux.HandleFunc("POST /admin/peers/unfriend", s.requireSuperuser(s.postAdminUnfriend))

	// Session-scoped preferences.
	mux.HandleFunc("GET /inbox/badge", s.requireAuth(s.getStepsBadge)) // legacy poll URL

	// Middleware chain (outer to inner): request-id -> logging -> session -> CSRF -> CSP.
	csrfMW := csrf.Protect(o.CSRFKey,
		csrf.Secure(o.Secure),
		csrf.Path("/"),
		csrf.SameSite(csrf.SameSiteLaxMode),
		// The browser sends an Origin header on POSTs; gorilla/csrf rejects any host
		// it doesn't recognise (beyond the request's own host). Trust the operator's
		// declared hosts so access via a proxy, LAN IP, or hostname still works.
		csrf.TrustedOrigins(o.TrustedOrigins),
		// Turn the opaque "Forbidden - origin invalid" into an actionable message
		// and log the rejected origin so the operator knows what to trust.
		csrf.ErrorHandler(http.HandlerFunc(s.csrfFailed)),
	)
	var h http.Handler = csrfMW(mux)
	if !o.Secure {
		// Over plaintext HTTP (local dev), tell gorilla/csrf to skip the
		// HTTPS-only strict Referer rule; the CSRF token and Origin checks still
		// apply. Behind TLS (Secure=true) the strict check is retained.
		h = plaintextMiddleware(h)
	}
	h = o.Sessions.SCS().LoadAndSave(h)
	h = securityHeaders(h) // CSP + hardening on every response, including errors
	h = s.logging(h)
	h = requestIDMiddleware(h)
	return h, nil
}

// securityHeaders serves a strict Content-Security-Policy and related hardening
// on every response (R8): no inline scripts, self-hosted assets only, no framing,
// no referrer leakage. The command-palette registry and no-JS forms are designed
// to satisfy this policy (inert JSON data, external ES modules, no inline JS).
func securityHeaders(next http.Handler) http.Handler {
	const csp = "default-src 'self'; script-src 'self'; style-src 'self'; " +
		"img-src 'self' data:; font-src 'self'; connect-src 'self'; " +
		"form-action 'self'; base-uri 'none'; frame-ancestors 'none'; object-src 'none'"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		// "same-origin": no referrer is ever sent to another origin (the leakage
		// we care about), but same-origin requests keep their referrer AND their
		// Origin header. It must NOT be "no-referrer": that makes browsers send
		// `Origin: null` on same-origin form POSTs, which gorilla/csrf rejects as an
		// invalid origin — breaking every login and state-changing form.
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

// csrfFailed renders a CSRF rejection as an actionable message and logs the
// details. The most common real cause is an untrusted Origin (the UI reached
// through a proxy or a hostname it doesn't bind), so that case names the fix.
func (s *Server) csrfFailed(w http.ResponseWriter, r *http.Request) {
	reason := csrf.FailureReason(r)
	origin := r.Header.Get("Origin")
	s.log.Warn("csrf rejected",
		"reason", reason.Error(), "origin", origin, "host", r.Host, "path", r.URL.Path)
	msg := "Your request could not be verified. Reload the page and try again."
	if errors.Is(reason, csrf.ErrBadOrigin) && origin != "" {
		msg = "This request came from an origin the server does not trust (" + origin +
			"). If you reach juice-ui through a proxy or a hostname other than the one it" +
			" binds, set JUICE_UI_TRUSTED_ORIGINS to include that host and restart."
	}
	s.renderPage(w, r, http.StatusForbidden, "error", "Blocked", errorView{Message: msg})
}

// plaintextMiddleware marks requests as plaintext HTTP for gorilla/csrf.
func plaintextMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, csrf.PlaintextHTTPRequest(r))
	})
}

// requestIDMiddleware ensures every request has an X-Request-Id, stored in the
// context (so the kernel client forwards it) and echoed on the response (R8.4).
func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rid := r.Header.Get("X-Request-Id")
		if rid == "" {
			rid = newID()
		}
		w.Header().Set("X-Request-Id", rid)
		ctx := client.WithRequestID(r.Context(), rid)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// statusRecorder captures the response status for logging.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (sr *statusRecorder) WriteHeader(code int) {
	sr.status = code
	sr.ResponseWriter.WriteHeader(code)
}

// logging emits one structured log line per request. It never logs tokens or
// secrets (R7.2, R8.3).
func (s *Server) logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sr := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(sr, r)
		s.log.Info("request",
			"request_id", client.RequestIDFromContext(r.Context()),
			"method", r.Method,
			"path", r.URL.Path,
			"status", sr.status,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}

// requireAuth wraps a handler so unauthenticated requests are redirected to the
// login page.
func (s *Server) requireAuth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.sessions.IsAuthenticated(r.Context()) {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		h(w, r)
	}
}

// superuserHandle is the fixed superuser identity (kernel requirements §12); the
// UI shows the admin group and superuser scope to this handle only. The kernel
// remains authoritative — an admin call always re-checks the bearer.
const superuserHandle = "@sys"

// isSuperuser reports whether the session belongs to the superuser.
func (s *Server) isSuperuser(r *http.Request) bool {
	return s.sessions.Handle(r.Context()) == superuserHandle
}

// requireSuperuser gates admin routes. A non-superuser (or anonymous) request
// gets a 404 error page rather than a 403 — the admin surface is not advertised
// to those who may not use it. The kernel independently rejects any admin call
// from a non-@sys bearer.
func (s *Server) requireSuperuser(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.sessions.IsAuthenticated(r.Context()) {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if !s.isSuperuser(r) {
			s.renderPage(w, r, http.StatusNotFound, "error", "Not found", errorView{Message: "Page not found."})
			return
		}
		h(w, r)
	}
}

// page builds the common page envelope (theme, handle, CSRF, flash) for a
// request.
func (s *Server) page(r *http.Request, title string, data any) Page {
	var flash, flashKind string
	if c, err := r.Cookie("flash"); err == nil {
		// The value is URL-encoded (flash messages contain non-ASCII like "¤",
		// which is invalid in a raw cookie value).
		if v, err := url.QueryUnescape(c.Value); err == nil {
			if i := strings.IndexByte(v, '|'); i >= 0 {
				flashKind, flash = v[:i], v[i+1:]
			} else {
				flash = v
			}
		}
	}
	super := s.isSuperuser(r)
	kernelHandle, kernelFP := s.kernelIdentity(r.Context())
	return Page{
		Title:        title,
		Theme:        themeFromRequest(r),
		Sidebar:      sidebarFromRequest(r),
		Active:       activeGroup(r.URL.Path),
		Handle:       s.sessions.Handle(r.Context()),
		KernelHandle: kernelHandle,
		KernelFP:     kernelFP,
		IsSuperuser:  super,
		Themes:       ThemeList(),
		CSRFField:    csrf.TemplateField(r),
		CSRFToken:    csrf.Token(r),
		Commands:     template.JS(commandsJSON(BuildCommands(super, ThemeList()))),
		Flash:        flash,
		FlashKind:    flashKind,
		Data:         data,
	}
}

// activeGroup maps a request path to the nav group it belongs to, for
// aria-current highlighting.
func activeGroup(path string) string {
	switch {
	case path == "/run":
		return "run"
	case strings.HasPrefix(path, "/actions"):
		return "action"
	case strings.HasPrefix(path, "/processes"):
		return "process"
	case strings.HasPrefix(path, "/transactions"):
		return "tx"
	case strings.HasPrefix(path, "/steps"):
		return "step"
	case strings.HasPrefix(path, "/user"):
		return "user"
	case strings.HasPrefix(path, "/admin"):
		return "admin"
	}
	return ""
}

// renderPage writes a full page, clearing any one-shot flash cookie and logging
// (not surfacing) template errors. Pass the signed-in user's available balance
// as the optional last argument to skip a redundant GET /v1/me call when the
// handler already fetched the user.
func (s *Server) renderPage(w http.ResponseWriter, r *http.Request, status int, name, title string, data any, available ...int64) {
	p := s.page(r, title, data)
	if p.Handle != "" {
		if len(available) > 0 {
			p.Available = available[0]
		} else {
			_ = s.sessions.Do(r.Context(), func(tok string) error {
				if me, e := s.client.Me(r.Context(), tok); e == nil {
					p.Available = me.Available
				}
				return nil
			})
		}
	}
	if p.Flash != "" {
		http.SetCookie(w, &http.Cookie{Name: "flash", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteLaxMode})
	}
	if err := s.render.Render(w, status, name, p); err != nil {
		s.log.Error("render", "page", name, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// handleErrFragment handles errors from htmx-capable handlers. Auth errors
// clear the session and redirect (HX-Redirect for htmx requests, flash-redirect
// otherwise). Other errors render the _error fragment for htmx or the full
// error page otherwise. Returns true if it handled the error.
func (s *Server) handleErrFragment(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return false
	}
	if isAuthErr(err) {
		_ = s.sessions.Logout(r.Context())
		if isHTMX(r) {
			w.Header().Set("HX-Redirect", "/login")
			return true
		}
		s.flashRedirect(w, r, "/login", "error", "Your session has expired. Please sign in again.")
		return true
	}
	s.log.Warn("operation failed", "request_id", client.RequestIDFromContext(r.Context()), "path", r.URL.Path, "err", err)
	if isHTMX(r) {
		_ = s.render.Fragment(w, http.StatusOK, "_error", userMessage(err))
		return true
	}
	s.renderPage(w, r, statusFor(err), "error", "Error", errorView{Message: userMessage(err)})
	return true
}

// isAuthErr reports whether an error means the session is no longer usable.
func isAuthErr(err error) bool {
	return errors.Is(err, client.ErrUnauthenticated) || errors.Is(err, session.ErrNoSession)
}

// handleErr deals with an error from an authenticated operation: auth failures
// clear the session and redirect to login (R5.3); everything else renders an
// error page with a mapped, status-free message (R7.1). It returns true if it
// handled the error.
func (s *Server) handleErr(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return false
	}
	if isAuthErr(err) {
		_ = s.sessions.Logout(r.Context())
		s.flashRedirect(w, r, "/login", "error", "Your session has expired. Please sign in again.")
		return true
	}
	s.log.Warn("operation failed", "request_id", client.RequestIDFromContext(r.Context()), "path", r.URL.Path, "err", err)
	s.renderPage(w, r, statusFor(err), "error", "Error", errorView{Message: userMessage(err)})
	return true
}

// statusFor maps an error to an HTTP status for the error page.
func statusFor(err error) int {
	if ke, ok := client.AsKernelError(err); ok {
		switch ke.Kind {
		case client.KindNotFound:
			return http.StatusNotFound
		case client.KindUnauthorized:
			return http.StatusForbidden
		}
	}
	return http.StatusOK // soft error page; we don't echo raw kernel statuses
}

// userMessage extracts an actionable message from an error without leaking
// status codes or secrets.
func userMessage(err error) string {
	if ke, ok := client.AsKernelError(err); ok {
		return ke.UserMessage()
	}
	return "Something went wrong. Please try again."
}

// flashRedirect sets a one-shot flash cookie and redirects. kind must be one
// of "success", "error", or "info".
func (s *Server) flashRedirect(w http.ResponseWriter, r *http.Request, to, kind, msg string) {
	http.SetCookie(w, &http.Cookie{
		// URL-encode: messages contain non-ASCII (e.g. "¤") that a raw cookie
		// value rejects (net/http drops the invalid bytes).
		Name: "flash", Value: url.QueryEscape(kind + "|" + msg), Path: "/", MaxAge: 30,
		HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// themeFromRequest returns the explicit theme slug from the cookie, or "" to
// defer to prefers-color-scheme. Legacy "light"/"dark" cookies map to the default
// light/dark themes so existing sessions keep working after the theme change.
func themeFromRequest(r *http.Request) string {
	c, err := r.Cookie("theme")
	if err != nil {
		return ""
	}
	switch c.Value {
	case "light":
		return defaultLightTheme
	case "dark":
		return defaultDarkTheme
	}
	if ValidTheme(c.Value) {
		return c.Value
	}
	return ""
}

// sidebarFromRequest returns "collapsed" when the user has collapsed the
// sidebar, else "" (expanded). Stamped at render time so the layout is correct
// with no flash and the toggle works without JavaScript.
func sidebarFromRequest(r *http.Request) string {
	if c, err := r.Cookie("sidebar"); err == nil && c.Value == "collapsed" {
		return "collapsed"
	}
	return ""
}

// newID returns a random hex id for request correlation.
func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "req"
	}
	return hex.EncodeToString(b[:])
}
