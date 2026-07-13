package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/daios-ai/juice-ui/apps"
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
	CSRFKey        []byte        // 32 bytes
	Secure         bool          // set Secure flag on CSRF/session cookies (false for local http)
	TrustedOrigins []string      // extra hosts accepted as the request Origin (proxied hostnames)
	PublicURL      string        // absolute base URL of this BFF, for OAuth redirect_uri; "" derives from the request
	Apps           []apps.App    // app modules to mount (their templates must already be in Renderer)
	StatePath      string        // path to state.json for per-app persisted state; "" = in-memory
	Home           string        // component home dir ($JUICE_HOME/ui) handed to apps; "" = none
	BadgeInterval  time.Duration // nav-badge poll interval + steps-badge cache TTL; 0 → default 30s
}

// Server holds dependencies and serves the BFF.
type Server struct {
	client    *client.Client
	sessions  *session.Manager
	caller    *Caller
	render    *Renderer
	log       *slog.Logger
	secure    bool
	publicURL string // configured absolute base URL; "" → derive from the request
	apps      []apps.App
	appStore  *appStore     // per-app persisted state (state.json)
	home      string        // component home dir handed to apps
	badgeTTL  time.Duration // nav-badge poll interval + steps-badge cache TTL

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

// publicBaseURL is the absolute base URL of this BFF, used to build the OAuth
// grant redirect_uri. It uses the configured PublicURL when set (for reverse
// proxies), else derives scheme+host from the request — which "just works" for
// direct access over localhost or Tailscale.
func (s *Server) publicBaseURL(r *http.Request) string {
	if s.publicURL != "" {
		return s.publicURL
	}
	scheme := "http"
	if s.secure {
		scheme = "https"
	}
	return scheme + "://" + r.Host
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
	badgeTTL := o.BadgeInterval
	if badgeTTL <= 0 {
		badgeTTL = 30 * time.Second
	}
	s := &Server{
		client:    o.Client,
		sessions:  o.Sessions,
		caller:    NewCaller(o.Client),
		render:    o.Renderer,
		log:       o.Logger,
		secure:    o.Secure,
		publicURL: strings.TrimRight(o.PublicURL, "/"),
		apps:      o.Apps,
		home:      o.Home,
		badgeTTL:  badgeTTL,
	}
	as, err := newAppStore(o.StatePath)
	if err != nil {
		return nil, fmt.Errorf("load app state %q: %w", o.StatePath, err)
	}
	s.appStore = as

	mux := http.NewServeMux()
	// Static assets (open).
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(StaticFS()))))
	// Bare /favicon.ico for browsers that probe the root (and older ones that
	// ignore the SVG <link rel=icon>); the raster fallback lives in static.
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFileFS(w, r, StaticFS(), "favicon.ico")
	})

	// Public auth routes.
	mux.HandleFunc("GET /health", s.getHealth) // open liveness probe + browser heartbeat
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

	// Grants (connect an upstream account to an action): the oauth_delegated
	// browser flow (start/callback) and the delegated_bearer static-token path.
	mux.HandleFunc("GET /grants/connect", s.requireAuth(s.getGrantConnect))
	mux.HandleFunc("POST /grants/start", s.requireAuth(s.postGrantStart))
	mux.HandleFunc("GET /grants/callback", s.requireAuth(s.getGrantCallback))
	mux.HandleFunc("POST /grants/connect", s.requireAuth(s.postGrantConnect))
	mux.HandleFunc("POST /grants/disconnect", s.requireAuth(s.postGrantDisconnect))

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

	// App modules (chat, and future special apps): each registers its own
	// routes and opens any module-owned store. The core stays agnostic — it only
	// knows the apps.App seam, never a concrete module. App routes are
	// gated behind requireAuth here so a module handler always has a session.
	deps := s.appDeps()
	for _, f := range s.apps {
		f.Mount(mux, deps) // Mount first: a module stashes Deps here, then uses it in Start.
		if err := f.Start(context.Background()); err != nil {
			return nil, err
		}
	}

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
	protected := csrfMW(mux)
	if !o.Secure {
		// Over plaintext HTTP (local dev), tell gorilla/csrf to skip the
		// HTTPS-only strict Referer rule; the CSRF token and Origin checks still
		// apply. Behind TLS (Secure=true) the strict check is retained.
		protected = plaintextMiddleware(protected)
	}
	// An app's kernel-facing endpoints live under /_kernel/ and are called
	// server-to-server by the kernel (no browser, no CSRF token), so they bypass
	// CSRF; every browser route stays CSRF-protected. Both paths still pass through
	// session/security/logging below.
	var h http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/_kernel/") {
			mux.ServeHTTP(w, r)
			return
		}
		protected.ServeHTTP(w, r)
	})
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
			"request_id", shortID(client.RequestIDFromContext(r.Context())),
			"method", r.Method,
			"path", r.URL.Path,
			"status", sr.status,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}

// shortID trims an opaque id (e.g. a request id) to its first 8 chars for compact
// logs; short or empty ids pass through unchanged.
func shortID(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

// requireAuth wraps a handler so unauthenticated requests are redirected to the
// login page.
func (s *Server) requireAuth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.sessions.IsAuthenticated(r.Context()) {
			hxRedirect(w, r, "/login")
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

// appDeps builds the capability surface handed to every app at
// Mount time: the kernel client and session manager, plus rendering and flow
// helpers as closures over this server, so a module renders in the shared shell
// without importing package server.
func (s *Server) appDeps() apps.Deps {
	return apps.Deps{
		Client:   s.client,
		Sessions: s.sessions,
		RenderPage: func(w http.ResponseWriter, r *http.Request, status int, page, title string, data any) {
			s.renderPage(w, r, status, page, title, data)
		},
		RenderFragment: func(w http.ResponseWriter, status int, name string, data any) error {
			return s.render.Fragment(w, status, name, data)
		},
		HandleErr:         s.handleErr,
		HandleErrFragment: s.handleErrFragment,
		FlashRedirect:     s.flashRedirect,
		CSRFField:         func(r *http.Request) template.HTML { return csrf.TemplateField(r) },
		IsHTMX:            isHTMX,
		Handle:            func(r *http.Request) string { return s.sessions.Handle(r.Context()) },
		RequireAuth:       s.requireAuth,
		LoadAppState:      func(slug string) (json.RawMessage, error) { return s.appStore.load(slug), nil },
		SaveAppState:      func(slug string, v json.RawMessage) error { return s.appStore.save(slug, v) },
		PublicURL:         s.publicURL,
		StatePath:         s.appStore.path,
		Home:              s.home,
		Logger:            s.log,
	}
}

// appNav collects the sidebar entries contributed by apps for the
// signed-in user, marking the entry whose Match prefixes the request path as the
// current page.
func (s *Server) appNav(r *http.Request) []apps.NavEntry {
	if len(s.apps) == 0 {
		return nil
	}
	handle := s.sessions.Handle(r.Context())
	super := s.isSuperuser(r)
	var out []apps.NavEntry
	for _, f := range s.apps {
		for _, e := range f.Nav(handle, super) {
			e.Active = e.Match != "" && strings.HasPrefix(r.URL.Path, e.Match)
			out = append(out, e)
		}
	}
	return out
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
		AppNav:       s.appNav(r),
		BadgeSecs:    int(s.badgeTTL / time.Second),
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
	case strings.HasPrefix(path, "/admin/peers"):
		return "peers"
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
	hxRedirect(w, r, to)
}

// hxRedirect navigates to `to`. For an htmx request it sets the HX-Redirect header
// (htmx performs a real full-page navigation) instead of a 303 whose body htmx would
// swap into the triggering element — which is how a lost session used to swap the
// whole login page into a badge <span>. Normal requests get the usual 303.
func hxRedirect(w http.ResponseWriter, r *http.Request, to string) {
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", to)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// flashResult renders the standard outcome of a write action: an auth error is a
// clean logout, any other error flashes its user message, and success flashes the
// given message — all redirecting to dest. It is the shared tail of the POST
// handlers (admin ops, action/step create-update).
func (s *Server) flashResult(w http.ResponseWriter, r *http.Request, err error, dest, success string) {
	switch {
	case isAuthErr(err):
		s.handleErr(w, r, err)
	case err != nil:
		s.flashRedirect(w, r, dest, "error", userMessage(err))
	default:
		s.flashRedirect(w, r, dest, "success", success)
	}
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
