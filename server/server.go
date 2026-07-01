package server

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/daios-ai/juice-ui/client"
	"github.com/daios-ai/juice-ui/session"
	"github.com/gorilla/csrf"
)

// Options wires the server's dependencies.
type Options struct {
	Client   *client.Client
	Sessions *session.Manager
	Renderer *Renderer
	Logger   *slog.Logger
	CSRFKey  []byte // 32 bytes
	Secure   bool   // set Secure flag on CSRF/session cookies (false for local http)
}

// Server holds dependencies and serves the BFF.
type Server struct {
	client   *client.Client
	sessions *session.Manager
	caller   *Caller
	render   *Renderer
	log      *slog.Logger
	secure   bool
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

	// Public auth routes.
	mux.HandleFunc("GET /login", s.getLogin)
	mux.HandleFunc("POST /login", s.postLogin)
	mux.HandleFunc("POST /signup", s.postSignup)
	mux.HandleFunc("POST /logout", s.postLogout)
	mux.HandleFunc("POST /theme", s.postTheme)
	mux.HandleFunc("POST /sidebar", s.postSidebar)

	// Legacy redirects (bookmarks / old links).
	mux.HandleFunc("GET /{$}", s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/run", http.StatusSeeOther)
	}))
	mux.HandleFunc("GET /discover", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/run", http.StatusMovedPermanently)
	})
	mux.HandleFunc("GET /steps", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/inbox", http.StatusMovedPermanently)
	})

	// Authenticated routes.
	mux.HandleFunc("GET /run", s.requireAuth(s.getRunRoom))
	mux.HandleFunc("GET /inbox", s.requireAuth(s.getInbox))
	mux.HandleFunc("GET /inbox/badge", s.requireAuth(s.getInboxBadge))
	mux.HandleFunc("GET /wallet", s.requireAuth(s.getWallet))
	mux.HandleFunc("GET /settings", s.requireAuth(s.getSettings))
	mux.HandleFunc("POST /settings", s.requireAuth(s.postSettings))
	mux.HandleFunc("GET /actions/{id}", s.requireAuth(s.getAction))
	mux.HandleFunc("POST /actions/{id}/run", s.requireAuth(s.postRun))
	mux.HandleFunc("POST /actions/{id}/enable", s.requireAuth(s.postActionLifecycle))
	mux.HandleFunc("POST /actions/{id}/disable", s.requireAuth(s.postActionLifecycle))
	mux.HandleFunc("POST /actions/{id}/delete", s.requireAuth(s.postActionDelete))
	mux.HandleFunc("POST /actions/{id}/make-public", s.requireAuth(s.postActionLifecycle))
	mux.HandleFunc("POST /actions/{id}/make-private", s.requireAuth(s.postActionLifecycle))
	mux.HandleFunc("POST /actions/{id}/update-price", s.requireAuth(s.postUpdateActionPrice))
	mux.HandleFunc("POST /actions/{id}/update", s.requireAuth(s.postUpdateAction))
	mux.HandleFunc("GET /studio", s.requireAuth(s.getStudio))
	mux.HandleFunc("POST /studio/create", s.requireAuth(s.postCreateAction))
	mux.HandleFunc("POST /studio/import", s.requireAuth(s.postImport))
	mux.HandleFunc("POST /studio/unimport", s.requireAuth(s.postUnimport))
	mux.HandleFunc("POST /studio/make", s.requireAuth(s.postMakeAction))
	mux.HandleFunc("GET /processes", s.requireAuth(s.getProcesses))
	mux.HandleFunc("GET /processes/{id}", s.requireAuth(s.getProcess))
	mux.HandleFunc("POST /processes/{id}/end", s.requireAuth(s.postEnd))
	mux.HandleFunc("GET /transactions", s.requireAuth(s.getTransactions))
	mux.HandleFunc("GET /transactions/{id}", s.requireAuth(s.getTransaction))
	mux.HandleFunc("POST /transactions/{id}/rate", s.requireAuth(s.postRate))
	mux.HandleFunc("POST /steps/{id}/complete", s.requireAuth(s.postCompleteStep))

	// Middleware chain (outer to inner): request-id -> logging -> session -> CSRF.
	csrfMW := csrf.Protect(o.CSRFKey,
		csrf.Secure(o.Secure),
		csrf.Path("/"),
		csrf.SameSite(csrf.SameSiteLaxMode),
	)
	var h http.Handler = csrfMW(mux)
	if !o.Secure {
		// Over plaintext HTTP (local dev), tell gorilla/csrf to skip the
		// HTTPS-only strict Referer rule; the CSRF token and Origin checks still
		// apply. Behind TLS (Secure=true) the strict check is retained.
		h = plaintextMiddleware(h)
	}
	h = o.Sessions.SCS().LoadAndSave(h)
	h = s.logging(h)
	h = requestIDMiddleware(h)
	return h, nil
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

// page builds the common page envelope (theme, handle, CSRF, flash) for a
// request.
func (s *Server) page(r *http.Request, title string, data any) Page {
	var flash, flashKind string
	if c, err := r.Cookie("flash"); err == nil {
		if i := strings.IndexByte(c.Value, '|'); i >= 0 {
			flashKind = c.Value[:i]
			flash = c.Value[i+1:]
		} else {
			flash = c.Value
		}
	}
	return Page{
		Title:     title,
		Theme:     themeFromRequest(r),
		Sidebar:   sidebarFromRequest(r),
		Handle:    s.sessions.Handle(r.Context()),
		CSRFField: csrf.TemplateField(r),
		CSRFToken: csrf.Token(r),
		Flash:     flash,
		FlashKind: flashKind,
		Data:      data,
	}
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
		Name: "flash", Value: kind + "|" + msg, Path: "/", MaxAge: 30,
		HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// themeFromRequest returns the explicit theme from the cookie, or "" to defer to
// the OS preference (R8.6).
func themeFromRequest(r *http.Request) string {
	c, err := r.Cookie("theme")
	if err != nil {
		return ""
	}
	if c.Value == "light" || c.Value == "dark" {
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
