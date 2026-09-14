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

	// Identity is the kernel this UI is for: built once at wiring, held to the key
	// its records name (ecosystem-standard §5), and the source of the world's
	// money unit. It also keys the app state, so what an app provisioned on one
	// kernel is never replayed on another (§4).
	Identity *KernelIdentity
	// KernelName is the name this kernel is recorded under, shown so a person can see which
	// kernel the interface is on (ecosystem-standard §6); "" when a bare URL.
	KernelName string
}

// Server holds dependencies and serves the BFF.
type Server struct {
	client     *client.Client
	sessions   *session.Manager
	caller     *Caller
	render     *Renderer
	log        *slog.Logger
	secure     bool
	publicURL  string // configured absolute base URL; "" → derive from the request
	apps       []apps.App
	appStore   *appStore       // per-app persisted state (state.json), keyed by kernel
	home       string          // component home dir handed to apps
	badgeTTL   time.Duration   // nav-badge poll interval + steps-badge cache TTL
	identity   *KernelIdentity // which kernel this UI is for, and its money unit
	kernelName string          // the kernel's name in the client records, for the chrome
}

// KernelIdentity is the kernel this UI is for: the /health banner, cached for a
// TTL, held to the key the records name. It is built once at wiring and owns
// that question for the whole process — the renderer asks it for the world's
// money unit, the server for the kernel's identity — so neither has to be told
// later what it is working with.
type KernelIdentity struct {
	client   *client.Client
	recorded string // the kernel's public key as recorded; never changes
	ttl      time.Duration
	log      *slog.Logger

	mu      sync.Mutex
	id      client.Health
	fetched time.Time
}

// NewKernelIdentity binds this UI to one kernel. recorded is its public key —
// from the named kernel's record, or from AwaitKernel when a bare address was
// given — and is fixed from here: an address that later answers with another key
// is another kernel, not a change of mind.
func NewKernelIdentity(c *client.Client, recorded string, ttl time.Duration, log *slog.Logger) *KernelIdentity {
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	if log == nil {
		log = slog.Default()
	}
	return &KernelIdentity{client: c, recorded: recorded, ttl: ttl, log: log}
}

// AwaitKernel reads /health until the server answers or the timeout passes. It
// is how a UI given a bare address learns which kernel that is, before anything
// is keyed on it; a UI that names a kernel already knows and never calls this.
func AwaitKernel(c *client.Client, timeout time.Duration) (client.Health, error) {
	deadline := time.Now().Add(timeout)
	var last error
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		id, err := c.Health(ctx)
		cancel()
		switch {
		case err != nil:
			last = err
		case id.PublicKey == "":
			last = fmt.Errorf("/health carries no public key")
		default:
			return id, nil
		}
		if time.Now().After(deadline) {
			return client.Health{}, last
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// Get returns the kernel's own /health banner, cached for the TTL, and whether
// it is known. Unknown means the kernel has not answered yet, or the address
// answered as a different kernel: the chrome falls back to the plain brand, the
// session pin treats it as unknown rather than as a mismatch, and money neither
// renders nor is accepted (§7).
func (k *KernelIdentity) Get(ctx context.Context) (client.Health, bool) {
	k.mu.Lock()
	id, at := k.id, k.fetched
	k.mu.Unlock()
	if id.Handle != "" && time.Since(at) < k.ttl {
		return id, true
	}
	hc, err := k.client.Health(ctx)
	if err != nil || hc.Handle == "" {
		return id, id.Handle != "" // keep what we had; retry on a later render
	}
	if k.recorded != "" && hc.PublicKey != k.recorded {
		// The address answers, but not as the kernel our records name. That is
		// not this kernel and nothing is trusted from it: no identity, no unit,
		// no login. Logged, because it is the one thing an operator must know.
		k.log.Warn("kernel identity mismatch: the server at this address is not the recorded kernel",
			"recorded", keyFingerprint(k.recorded), "answered", keyFingerprint(hc.PublicKey))
		return client.Health{}, false
	}
	k.mu.Lock()
	k.id, k.fetched = hc, time.Now()
	k.mu.Unlock()
	return hc, true
}

// Unit is the money unit of the world this kernel serves — its decimals and its
// symbol — and whether it is known at all. A client that cannot learn the unit
// renders no amount and accepts none, rather than scaling by a guess (§7).
func (k *KernelIdentity) Unit() (Unit, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.id.Handle == "" {
		return Unit{}, false
	}
	return Unit{Decimals: k.id.Decimals, Symbol: k.id.Symbol}, true
}

// kernelIdentity is the kernel's banner for this request, or a zero Health when
// it is not known.
func (s *Server) kernelIdentity(ctx context.Context) client.Health {
	id, _ := s.identity.Get(ctx)
	return id
}

// parseAmount reads a typed amount in the kernel's own unit. A unit we cannot
// learn is a refusal, never a guess: scaling by an assumed decimals would send
// the kernel a number the person did not type (§7).
func (s *Server) parseAmount(v string) (int64, error) {
	u, ok := s.identity.Unit()
	if !ok {
		return 0, fmt.Errorf("the kernel has not said what its money is; try again in a moment")
	}
	return parseAmount(v, u.Decimals)
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

// keyFingerprint shortens a base64url public key for eyeball verification.
// One definition serves the whole UI (client.ShortKey), so the same kernel
// abbreviates identically in the header, on a card, and inside a sentence.
func keyFingerprint(key string) string { return client.ShortKey(key) }

// New builds the BFF HTTP handler: routes plus the middleware chain
// (request-id, structured logging, session load/save, CSRF, R5.5/R8.4).
func New(o Options) (http.Handler, error) {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Identity == nil {
		// Which kernel this UI is for is not a thing to default: it keys the app
		// state, pins the session, and fixes the unit every amount is read in.
		return nil, fmt.Errorf("server: Options.Identity is required")
	}
	badgeTTL := o.BadgeInterval
	if badgeTTL <= 0 {
		badgeTTL = 30 * time.Second
	}
	s := &Server{
		client:     o.Client,
		sessions:   o.Sessions,
		caller:     NewCaller(o.Client),
		render:     o.Renderer,
		log:        o.Logger,
		secure:     o.Secure,
		publicURL:  strings.TrimRight(o.PublicURL, "/"),
		apps:       o.Apps,
		home:       o.Home,
		badgeTTL:   badgeTTL,
		identity:   o.Identity,
		kernelName: o.KernelName,
	}
	as, err := newAppStore(o.StatePath, o.Identity.recorded)
	if err != nil {
		return nil, fmt.Errorf("load app state %q: %w", o.StatePath, err)
	}
	if as.Adopted {
		s.log.Warn("app state predates kernel keying; its apps are adopted by the current kernel and the file is rewritten keyed",
			"kernel", keyFingerprint(o.Identity.recorded))
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
	mux.HandleFunc("GET /recover", s.getRecover) // seed-phrase password reset (unauthenticated)
	mux.HandleFunc("POST /recover", s.postRecover)
	// Post-signup recovery phrase: standalone and unauthenticated (outside the app);
	// the user acknowledges it here, then signs in to gain access.
	mux.HandleFunc("GET /recovery-phrase", s.getRecoveryPhrase)
	mux.HandleFunc("POST /recovery-phrase", s.postRecoveryPhrase)
	mux.HandleFunc("POST /logout", s.postLogout)
	mux.HandleFunc("POST /theme", s.postTheme)
	mux.HandleFunc("POST /sidebar", s.postSidebar)

	// Home + legacy redirects (bookmarks / old links). The nav is relabelled as a
	// marketplace (browse/inbox/workshop/wallet/settings), so the old CLI-noun URLs
	// 301 to their new homes.
	mux.HandleFunc("GET /{$}", s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/browse", http.StatusSeeOther)
	}))
	for from, to := range map[string]string{
		"/run": "/browse", "/discover": "/browse", "/actions": "/browse",
		"/steps": "/inbox", "/studio": "/workshop",
		"/user": "/settings", "/you": "/settings",
	} {
		dest := to
		mux.HandleFunc("GET "+from, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, dest, http.StatusMovedPermanently)
		})
	}

	// Browse — discovery, catalog, and execution.
	mux.HandleFunc("GET /browse", s.requireAuth(s.getBrowse))
	mux.HandleFunc("POST /browse/propose", s.requireAuth(s.postPropose))

	// Workshop — the provider surface (own actions, earnings, creation).
	mux.HandleFunc("GET /workshop", s.requireAuth(s.getWorkshop))
	mux.HandleFunc("POST /actions", s.requireAuth(s.postCreateAction))
	mux.HandleFunc("POST /actions/import", s.requireAuth(s.postImport))
	// A DISCOVERED action has no local row yet: opening it resolves the reference
	// with its home kernel, which caches a proxy and lands on an ordinary page.
	mux.HandleFunc("GET /discovered", s.requireAuth(s.getDiscovered))
	// A mutation names a target — an action id, or an owner/path naming a whole
	// application — so these are not per-id routes (kernel §D20).
	mux.HandleFunc("POST /actions/enable", s.requireAuth(s.postActionLifecycle))
	mux.HandleFunc("POST /actions/disable", s.requireAuth(s.postActionLifecycle))
	mux.HandleFunc("POST /actions/visibility", s.requireAuth(s.postActionLifecycle))
	mux.HandleFunc("POST /actions/delete", s.requireAuth(s.postActionDelete))
	mux.HandleFunc("GET /actions/{id}", s.requireAuth(s.getAction))
	mux.HandleFunc("POST /actions/{id}/run", s.requireAuth(s.postRun))
	mux.HandleFunc("POST /actions/{id}/update-price", s.requireAuth(s.postUpdateActionPrice))
	mux.HandleFunc("POST /actions/{id}/update", s.requireAuth(s.postUpdateAction))

	// Wallet — balances, open processes, ledger (evidence).
	mux.HandleFunc("GET /wallet", s.requireAuth(s.getWallet))
	mux.HandleFunc("POST /wallet/withdraw", s.requireAuth(s.postWithdraw))
	mux.HandleFunc("POST /wallet/address", s.requireAuth(s.postRegisterAddress))
	mux.HandleFunc("GET /processes", s.requireAuth(s.getProcesses))
	mux.HandleFunc("GET /processes/{id}", s.requireAuth(s.getProcess))
	mux.HandleFunc("POST /processes/{id}/end", s.requireAuth(s.postEnd))
	mux.HandleFunc("GET /transactions", s.requireAuth(s.getTransactions))
	mux.HandleFunc("GET /transactions/{id}", s.requireAuth(s.getTransaction))
	mux.HandleFunc("GET /transactions/{id}/verify", s.requireAuth(s.getTxVerify))
	mux.HandleFunc("POST /transactions/{id}/rate", s.requireAuth(s.postRate))

	// Inbox — steps addressed to you.
	mux.HandleFunc("GET /inbox", s.requireAuth(s.getSteps))
	mux.HandleFunc("GET /inbox/badge", s.requireAuth(s.getStepsBadge))
	mux.HandleFunc("GET /inbox/{id}", s.requireAuth(s.getStep))
	mux.HandleFunc("POST /inbox/{id}/complete", s.requireAuth(s.postCompleteStep))

	// Settings — account, preferences, connected accounts.
	mux.HandleFunc("GET /settings", s.requireAuth(s.getSettings))
	mux.HandleFunc("POST /settings", s.requireAuth(s.postSettings))

	// Owner page — a handle's public actions (like a channel).
	mux.HandleFunc("GET /users/{handle}", s.requireAuth(s.getOwner))

	// Grants (connect an upstream account to an action): the oauth_delegated
	// browser flow (start/callback) and the delegated_bearer static-token path.
	mux.HandleFunc("GET /grants/connect", s.requireAuth(s.getGrantConnect))
	mux.HandleFunc("GET /grants/start", s.requireAuth(s.getGrantStart))
	mux.HandleFunc("GET /grants/callback", s.requireAuth(s.getGrantCallback))
	mux.HandleFunc("POST /grants/connect", s.requireAuth(s.postGrantConnect))
	mux.HandleFunc("POST /grants/disconnect", s.requireAuth(s.postGrantDisconnect))

	// Admin — superuser-only supervision (money, access, federation trust).
	mux.HandleFunc("GET /admin", s.requireSuperuser(s.getAdmin))
	mux.HandleFunc("GET /admin/users/{handle}", s.requireSuperuser(s.getAdminUser))
	mux.HandleFunc("POST /admin/users/{handle}/suspend", s.requireSuperuser(s.postAdminUserOp))
	mux.HandleFunc("POST /admin/users/{handle}/unsuspend", s.requireSuperuser(s.postAdminUserOp))
	mux.HandleFunc("POST /admin/users/{handle}/rename", s.requireSuperuser(s.postAdminUserOp))
	mux.HandleFunc("POST /admin/deposit", s.requireSuperuser(s.postAdminDeposit))
	mux.HandleFunc("GET /admin/peers", s.requireSuperuser(s.getAdminPeers))
	// The literal /inspect pattern is more specific than /{key}, so it wins; a
	// peer key is 43 base64url chars and can never collide with it.
	mux.HandleFunc("GET /admin/peers/inspect", s.requireSuperuser(s.getAdminInspectByKey))
	mux.HandleFunc("GET /admin/peers/{key}", s.requireSuperuser(s.getAdminInspect))
	mux.HandleFunc("POST /admin/peers/suspend", s.requireSuperuser(s.postAdminPeerSuspend))
	mux.HandleFunc("POST /admin/peers/unsuspend", s.requireSuperuser(s.postAdminPeerSuspend))
	mux.HandleFunc("POST /admin/peers/rename", s.requireSuperuser(s.postAdminPeerRename))
	mux.HandleFunc("POST /admin/peers/{key}/steps/{id}/complete", s.requireSuperuser(s.postAdminPeerStepComplete))

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
		if s.kernelChanged(w, r) {
			return
		}
		h(w, r)
	}
}

// kernelChanged ends the session when the server at this address is no longer
// the kernel it was signed in to. A credential travels only to the server that
// issued it (ecosystem-standard §5), and a port is a default, never an identity:
// another kernel can hold it, and everything after that would be spent on the
// wrong ledger.
//
// An identity we cannot read is UNKNOWN, not a mismatch: the kernel is down or
// restarting, the request fails on its own kernel call, and the heartbeat ends
// the session. Only a positive disagreement logs out.
func (s *Server) kernelChanged(w http.ResponseWriter, r *http.Request) bool {
	id := s.kernelIdentity(r.Context())
	if id.PublicKey == "" {
		return false
	}
	pin := s.sessions.Pin(r.Context())
	if pin.PublicKey == id.PublicKey && pin.NetworkDigest == id.NetworkDigest {
		return false
	}
	_ = s.sessions.Logout(r.Context())
	s.flashRedirect(w, r, "/login", "error", "This address now answers for a different kernel. Sign in again.")
	return true
}

// superuserHandle is the fixed superuser identity (kernel requirements §12); the
// UI shows the admin group and superuser scope to this handle only. The kernel
// remains authoritative — an admin call always re-checks the bearer.
const superuserHandle = "sys"

// isSuperuser reports whether the session belongs to the superuser. Handles are
// bare (requirements.md §4), so this is a plain comparison — the gate fails
// CLOSED (the admin nav silently disappears) if a session ever holds anything
// else, which is the safe direction.
func (s *Server) isSuperuser(r *http.Request) bool {
	return s.sessions.Handle(r.Context()) == superuserHandle
}

// requireSuperuser gates admin routes. A non-superuser (or anonymous) request
// gets a 404 error page rather than a 403 — the admin surface is not advertised
// to those who may not use it. The kernel independently rejects any admin call
// from a non-sys bearer.
func (s *Server) requireSuperuser(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.sessions.IsAuthenticated(r.Context()) {
			// hxRedirect, not a bare 303: an admin view that polls a fragment must
			// navigate on a lost session, never swap the login page into the div.
			hxRedirect(w, r, "/login")
			return
		}
		if s.kernelChanged(w, r) {
			return
		}
		if !s.isSuperuser(r) {
			// Same reasoning for a demoted session: send htmx to the home page
			// rather than swap a full 404 into a polled fragment.
			if isHTMX(r) {
				w.Header().Set("HX-Redirect", "/")
				w.WriteHeader(http.StatusOK)
				return
			}
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
		RequireSuperuser:  s.requireSuperuser,
		LoadAppState:      func(slug string) (json.RawMessage, error) { return s.appStore.load(slug), nil },
		SaveAppState:      func(slug string, v json.RawMessage) error { return s.appStore.save(slug, v) },
		PublicURL:         s.publicURL,
		StatePath:         s.appStore.path,
		Home:              s.home,
		Logger:            s.log,
	}
}

// spriteIcon renders a sprite symbol as the inline <svg> the nav uses, so a core
// entry and an app's inline icon are the same NavItem.Icon shape.
func spriteIcon(id string) template.HTML {
	return template.HTML(`<svg class="ico" aria-hidden="true"><use href="/static/icons.svg#` + id + `"/></svg>`)
}

// The marketplace nav icons, inline (stroked outline, currentColor) so they match
// the approved mockup exactly without a sprite edit. Each is a 24×24 outline.
func navIcon(paths string) template.HTML {
	return template.HTML(`<svg class="ico" aria-hidden="true" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round">` + paths + `</svg>`)
}

var (
	iconBrowse   = navIcon(`<circle cx="12" cy="12" r="9"/><path d="M15.5 8.5l-2 5-5 2 2-5z"/>`)                                      // compass
	iconInbox    = navIcon(`<path d="M4 13l2.5-7h11L20 13v5a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1z"/><path d="M4 13h5a3 3 0 0 0 6 0h5"/>`)    // tray
	iconWorkshop = navIcon(`<path d="M9 3h6M10 3v6l-4.4 8.1A2 2 0 0 0 7.4 20h9.2a2 2 0 0 0 1.8-2.9L14 9V3"/><path d="M7.7 13h8.6"/>`) // lab beaker

	iconWallet  = navIcon(`<rect x="3" y="6" width="18" height="13" rx="2"/><path d="M3 10h18"/><circle cx="16.5" cy="14.5" r="1.3"/>`)
	iconYou     = navIcon(`<circle cx="12" cy="8" r="3.4"/><path d="M5 20c1.5-4 4-5.5 7-5.5s5.5 1.5 7 5.5"/>`)
	iconNetwork = navIcon(`<circle cx="12" cy="12" r="9"/><ellipse cx="12" cy="12" rx="4" ry="9"/><path d="M3.5 9h17M3.5 15h17"/>`)
	iconAdmin   = navIcon(`<path d="M12 3l7 3v6c0 4.5-3 7.5-7 9-4-1.5-7-4.5-7-9V6z"/>`)
)

// buildNav assembles the single nav model: the marketplace groups, then app
// entries, then the superuser-only network/admin. active is the current group.
//
// There is no privileged subset. The list is unbounded — every registered app
// contributes entries at runtime — so no fixed-size bar could ever cover it.
// Where there is room the sidebar renders the whole list; where there isn't, the
// drawer renders the same list. One model, two renderings.
func (s *Server) buildNav(r *http.Request, active string) []NavItem {
	nav := []NavItem{
		{Slug: "browse", Label: "browse", Href: "/browse", Icon: iconBrowse},
		{Slug: "inbox", Label: "inbox", Href: "/inbox", Icon: iconInbox, BadgeURL: "/inbox/badge"},
		{Slug: "workshop", Label: "workshop", Href: "/workshop", Icon: iconWorkshop},
		{Slug: "wallet", Label: "wallet", Href: "/wallet", Icon: iconWallet},
		{Slug: "settings", Label: "settings", Href: "/settings", Icon: iconYou},
	}
	// App entries (chat, agents) fold in after the core groups.
	handle := s.sessions.Handle(r.Context())
	super := s.isSuperuser(r)
	firstApp := true
	for _, f := range s.apps {
		for _, e := range f.Nav(handle, super) {
			nav = append(nav, NavItem{
				Slug:     e.Slug,
				Label:    e.Label,
				Href:     e.Href,
				Icon:     e.Icon,
				Badge:    e.Badge,
				BadgeURL: e.BadgeURL,
				Active:   e.Match != "" && strings.HasPrefix(r.URL.Path, e.Match),

				Divider: firstApp, // separate the apps group from the core groups
			})
			firstApp = false
		}
	}
	if super {
		nav = append(nav,
			NavItem{Slug: "network", Label: "network", Href: "/admin/peers", Icon: iconNetwork},
			NavItem{Slug: "admin", Label: "admin", Href: "/admin", Icon: iconAdmin},
		)
	}
	for i := range nav {
		if nav[i].Slug == active {
			nav[i].Active = true
		}
	}
	return nav
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
	kernelID := s.kernelIdentity(r.Context())
	active := activeGroup(r.URL.Path)
	return Page{
		Title: title,
		Path:  r.URL.Path,
		// ?menu=open is the no-JS route into the mobile nav: the drawer is
		// server-rendered [open] and falls back to an in-flow card, exactly as
		// ?form= does for modals (§6.1). With JS, data-modal intercepts first.
		MenuOpen:     r.URL.Query().Get("menu") == "open",
		Theme:        themeFromRequest(r),
		Sidebar:      sidebarFromRequest(r),
		Active:       active,
		Nav:          s.buildNav(r, active),
		Handle:       s.sessions.Handle(r.Context()),
		KernelHandle: kernelID.Handle,
		KernelFP:     keyFingerprint(kernelID.PublicKey),
		KernelNet:    kernelID.Network,
		KernelName:   s.kernelName,
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

// badgeSecs is the poll interval in whole seconds — the same value the nav badges
// use — for the federation and call-gate live pollers that carry it in view data
// (a standalone fragment has no Page envelope to read .BadgeSecs from).
func (s *Server) badgeSecs() int { return int(s.badgeTTL / time.Second) }

// activeGroup maps a request path to the nav group it belongs to, for
// aria-current highlighting.
func activeGroup(path string) string {
	switch {
	case path == "/browse" || strings.HasPrefix(path, "/actions"):
		// The catalog and every action detail/lifecycle page belong to browse.
		return "browse"
	case strings.HasPrefix(path, "/workshop"):
		return "workshop"
	case strings.HasPrefix(path, "/inbox"):
		return "inbox"
	case strings.HasPrefix(path, "/wallet"),
		strings.HasPrefix(path, "/processes"),
		strings.HasPrefix(path, "/transactions"):
		// The wallet owns money evidence (processes + the tx ledger + receipts).
		return "wallet"
	case strings.HasPrefix(path, "/settings"), strings.HasPrefix(path, "/grants"):
		return "settings"
	case strings.HasPrefix(path, "/admin/peers"):
		// Federation is the network group: the peer roster and settlement.
		return "network"
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

// newUUID returns a random UUID v4. The kernel requires a caller-minted UUID for
// a withdrawal, and that id IS the row: replaying it returns the same row rather
// than paying twice (kernel §D23).
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// newID returns a random hex id for request correlation.
func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "req"
	}
	return hex.EncodeToString(b[:])
}
