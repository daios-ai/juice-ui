package server

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/daios/juice-ui/client"
	"github.com/daios/juice-ui/session"
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

	// Authenticated routes.
	mux.HandleFunc("GET /{$}", s.requireAuth(s.getDashboard))
	mux.HandleFunc("GET /discover", s.requireAuth(s.getDiscover))
	mux.HandleFunc("GET /actions/{id}", s.requireAuth(s.getAction))
	mux.HandleFunc("POST /actions/{id}/run", s.requireAuth(s.postRun))
	mux.HandleFunc("POST /actions/{id}/enable", s.requireAuth(s.postActionLifecycle))
	mux.HandleFunc("POST /actions/{id}/disable", s.requireAuth(s.postActionLifecycle))
	mux.HandleFunc("POST /actions/{id}/delete", s.requireAuth(s.postActionDelete))
	mux.HandleFunc("POST /actions/{id}/grant-all", s.requireAuth(s.postActionLifecycle))
	mux.HandleFunc("POST /actions/{id}/revoke-all", s.requireAuth(s.postActionLifecycle))
	mux.HandleFunc("POST /actions/{id}/acl", s.requireAuth(s.postACLGrant))
	mux.HandleFunc("GET /studio", s.requireAuth(s.getStudio))
	mux.HandleFunc("POST /studio/create", s.requireAuth(s.postCreateAction))
	mux.HandleFunc("POST /studio/import", s.requireAuth(s.postImport))
	mux.HandleFunc("GET /processes", s.requireAuth(s.getProcesses))
	mux.HandleFunc("GET /processes/{id}", s.requireAuth(s.getProcess))
	mux.HandleFunc("POST /processes/{id}/fund", s.requireAuth(s.postFund))
	mux.HandleFunc("POST /processes/{id}/end", s.requireAuth(s.postEnd))
	mux.HandleFunc("GET /transactions", s.requireAuth(s.getTransactions))
	mux.HandleFunc("GET /transactions/{id}", s.requireAuth(s.getTransaction))
	mux.HandleFunc("POST /transactions/{id}/rate", s.requireAuth(s.postRate))
	mux.HandleFunc("GET /listeners", s.requireAuth(s.getListeners))
	mux.HandleFunc("POST /listeners/create", s.requireAuth(s.postCreateListener))
	mux.HandleFunc("POST /listeners/emit", s.requireAuth(s.postEmit))
	mux.HandleFunc("GET /listeners/{id}/inbox", s.requireAuth(s.getInbox))
	mux.HandleFunc("POST /listeners/{id}/delete", s.requireAuth(s.postDeleteListener))
	mux.HandleFunc("POST /events/{id}/consume", s.requireAuth(s.postConsume))

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
	flash := ""
	if c, err := r.Cookie("flash"); err == nil {
		flash = c.Value
	}
	return Page{
		Title:     title,
		Theme:     themeFromRequest(r),
		Handle:    s.sessions.Handle(r.Context()),
		CSRFField: csrf.TemplateField(r),
		CSRFToken: csrf.Token(r),
		Flash:     flash,
		Data:      data,
	}
}

// renderPage writes a full page, clearing any one-shot flash cookie and logging
// (not surfacing) template errors.
func (s *Server) renderPage(w http.ResponseWriter, r *http.Request, status int, name, title string, data any) {
	p := s.page(r, title, data)
	if p.Flash != "" {
		http.SetCookie(w, &http.Cookie{Name: "flash", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteLaxMode})
	}
	if err := s.render.Render(w, status, name, p); err != nil {
		s.log.Error("render", "page", name, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
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
		s.flashRedirect(w, r, "/login", "Your session has expired. Please sign in again.")
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

// flashRedirect sets a one-shot flash cookie and redirects.
func (s *Server) flashRedirect(w http.ResponseWriter, r *http.Request, to, msg string) {
	http.SetCookie(w, &http.Cookie{
		Name: "flash", Value: msg, Path: "/", MaxAge: 30,
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

// newID returns a random hex id for request correlation.
func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "req"
	}
	return hex.EncodeToString(b[:])
}
