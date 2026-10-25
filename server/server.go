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
	"github.com/daios-ai/juice-ui/wallet"
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
	BadgeInterval  time.Duration // nav-badge poll interval + task-badge cache TTL; 0 → default 30s

	// Identity is the kernel this UI is for: built once at wiring, held to the key
	// its records name (ecosystem-standard §5), and the source of the world's
	// money unit. It also keys the app state, so what an app provisioned on one
	// kernel is never replayed on another (§4).
	Identity *KernelIdentity
	// Config is this installation's kernel configuration file; nil when the
	// kernel is not this installation's, and then there is no page for it.
	Config Configuration
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
	appStore   *appStore               // per-app persisted state (state.json), keyed by kernel
	home       string                  // component home dir handed to apps
	badgeTTL   time.Duration           // the heartbeat's slow lane, and the task badge's cache TTL
	watchers   map[string]apps.Watcher // the heartbeat's watchers by kind (tick.go)
	conv       apps.Conversations      // the app whose rooms are the workspace; nil when none
	identity   *KernelIdentity         // which kernel this UI is for, and its money unit
	kernelName string                  // the kernel's name in the client records, for the chrome
	config     Configuration           // this installation's kernel configuration; nil = none
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

// Pin records the kernel's public key when the UI was given a bare address and
// has just learned it from /health. It takes effect once: a recorded key is
// never replaced, so a server that later answers with another key is another
// kernel.
func (k *KernelIdentity) Pin(key string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.recorded == "" {
		k.recorded = key
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
	if k == nil {
		return Unit{}, false
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.id.Handle == "" {
		return Unit{}, false
	}
	return Unit{Decimals: k.id.Decimals, Symbol: k.id.Symbol}, true
}

// name is this kernel's own name, the kernel segment of every local address,
// or "" while it is unknown.
func (k *KernelIdentity) name() string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.id.Handle
}

// Address composes this kernel's address for a handle, handle@kernel. It is
// the UI's one composition site — the entry screen, the natives, the
// superuser, an app's service account — and "" while the kernel's name is
// unknown, so every comparison fails closed and the kernel refuses the call.
func (k *KernelIdentity) Address(handle string) string {
	if n := k.name(); n != "" {
		return handle + "@" + n
	}
	return ""
}

// Local reports whether an action address (owner@kernel/name) names an action
// of this kernel: its kernel segment is this kernel's own name. It is the UI's
// one reading of an address; everything else passes names through verbatim.
func (k *KernelIdentity) Local(ref string) bool {
	n := k.name()
	return n != "" && strings.HasSuffix(refOwner(ref), "@"+n)
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
		config:     o.Config,
		watchers:   map[string]apps.Watcher{},
	}
	s.watch("inbox", s.watchInbox)
	s.watch("balance", s.watchBalance)
	s.watch("process", s.watchProcess)
	s.watch("peer", s.watchPeer)
	s.watch("session", s.watchSession)
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
	mux.HandleFunc("GET /health", s.getHealth)                    // open liveness probe
	mux.HandleFunc("GET /tick", s.gate(false, s.getTick))         // the page's heartbeat (tick.go): observes the idle rule, never extends it
	mux.HandleFunc("POST /session", s.requireAuth(s.postSession)) // Stay signed in (tick.go)
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
	mux.HandleFunc("GET /{$}", s.requireAuth(s.getHome))
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
	mux.HandleFunc("GET /search/catalog.json", s.requireAuth(s.getSearchCatalog))
	mux.HandleFunc("POST /browse/propose", s.requireAuth(s.postPropose))
	mux.HandleFunc("POST /browse/search", s.requireAuth(s.postSearch))

	// Workshop — the provider surface (own actions, earnings, creation).
	mux.HandleFunc("GET /workshop", s.requireAuth(s.getWorkshop))
	mux.HandleFunc("POST /actions", s.requireAuth(s.postCreateAction))
	mux.HandleFunc("POST /actions/import", s.requireAuth(s.postImport))
	// A DISCOVERED action has no local row yet: opening it resolves the reference
	// with its home kernel, which caches a proxy and lands on an ordinary page.
	mux.HandleFunc("GET /discovered", s.requireAuth(s.getDiscovered))
	mux.HandleFunc("GET /discovered/panel", s.requireAuth(s.getDiscoveredPanel))
	// A mutation names a target — an action id, or an owner/path naming a whole
	// application — so these are not per-id routes (kernel §D20).
	mux.HandleFunc("POST /actions/enable", s.requireAuth(s.postActionLifecycle))
	mux.HandleFunc("POST /actions/disable", s.requireAuth(s.postActionLifecycle))
	mux.HandleFunc("POST /actions/visibility", s.requireAuth(s.postActionLifecycle))
	mux.HandleFunc("POST /actions/delete", s.requireAuth(s.postActionDelete))
	mux.HandleFunc("GET /actions/{id}", s.requireAuth(s.getAction))
	mux.HandleFunc("GET /actions/{id}/panel", s.requireAuth(s.getActionPanel))
	mux.HandleFunc("POST /actions/{id}/run", s.requireAuth(s.postRun))
	mux.HandleFunc("POST /actions/{id}/fill", s.requireAuth(s.postFill))
	mux.HandleFunc("POST /actions/{id}/form", s.requireAuth(s.postActionForm))
	mux.HandleFunc("POST /actions/{id}/update-price", s.requireAuth(s.postUpdateActionPrice))
	mux.HandleFunc("POST /actions/{id}/update", s.requireAuth(s.postUpdateAction))

	// Wallet — balances, open processes, ledger (evidence).
	mux.HandleFunc("GET /wallet", s.requireAuth(s.getWallet))
	mux.HandleFunc("POST /wallet/withdraw/review", s.requireAuth(s.postWithdrawReview))
	mux.HandleFunc("POST /wallet/withdraw", s.requireAuth(s.postWithdraw))
	mux.HandleFunc("POST /wallet/fund/review", s.requireAuth(s.postFundReview))
	mux.HandleFunc("POST /wallet/fund", s.requireAuth(s.postFund))
	mux.HandleFunc("POST /wallet/address", s.requireAuth(s.postRegisterAddress))
	mux.Handle("GET /wallet/wallet.mjs", wallet.Script)
	mux.HandleFunc("GET /processes", s.requireAuth(s.getProcesses))
	mux.HandleFunc("GET /processes/{id}", s.requireAuth(s.getProcess))
	mux.HandleFunc("POST /processes/{id}/end", s.requireAuth(s.postEnd))
	mux.HandleFunc("GET /transactions", s.requireAuth(s.getTransactions))
	mux.HandleFunc("GET /transactions/{id}", s.requireAuth(s.getTransaction))
	mux.HandleFunc("GET /transactions/{id}/verify", s.requireAuth(s.getTxVerify))
	mux.HandleFunc("GET /transactions/{id}/result.json", s.requireAuth(s.getResultJSON))
	mux.HandleFunc("POST /transactions/{id}/rate", s.requireAuth(s.postRate))

	// Inbox — tasks addressed to you.
	mux.HandleFunc("GET /inbox", s.requireAuth(s.getTasks))
	mux.HandleFunc("GET /inbox/{id}", s.requireAuth(s.getTask))
	mux.HandleFunc("POST /inbox/{id}/complete", s.requireAuth(s.postCompleteTask))
	mux.HandleFunc("POST /inbox/{id}/cancel", s.requireAuth(s.postCancelTask))
	mux.HandleFunc("POST /inbox/{id}/form", s.requireAuth(s.postTaskForm))
	mux.HandleFunc("POST /form/json", s.requireAuth(s.postFormJSON))
	mux.HandleFunc("POST /inbox/requesters/{address}/{verb}", s.requireAuth(s.postRequester))

	// Settings — account, preferences, connected accounts.
	mux.HandleFunc("GET /settings", s.requireAuth(s.getSettings))
	mux.HandleFunc("POST /settings", s.requireAuth(s.postSettings))

	// Owner page — an address's public actions (like a channel).
	mux.HandleFunc("GET /users/{address}", s.requireAuth(s.getOwner))

	// Grants (connect an upstream account to an action): the oauth_delegated
	// browser flow (start/callback) and the delegated_bearer static-token path.
	mux.HandleFunc("GET /grants/connect", s.requireAuth(s.getGrantConnect))
	mux.HandleFunc("GET /grants/start", s.requireAuth(s.getGrantStart))
	mux.HandleFunc("GET /grants/callback", s.requireAuth(s.getGrantCallback))
	mux.HandleFunc("POST /grants/connect", s.requireAuth(s.postGrantConnect))
	mux.HandleFunc("POST /grants/disconnect", s.requireAuth(s.postGrantDisconnect))

	// Admin — superuser-only supervision (money, access, federation trust).
	mux.HandleFunc("GET /admin", s.requireSuperuser(s.getAdmin))
	mux.HandleFunc("GET /admin/users/{address}", s.requireSuperuser(s.getAdminUser))
	mux.HandleFunc("POST /admin/users/{address}/suspend", s.requireSuperuser(s.postAdminUserOp))
	mux.HandleFunc("POST /admin/users/{address}/unsuspend", s.requireSuperuser(s.postAdminUserOp))
	mux.HandleFunc("POST /admin/users/{address}/rename", s.requireSuperuser(s.postAdminUserOp))
	mux.HandleFunc("POST /admin/deposit", s.requireSuperuser(s.postAdminDeposit))
	if s.config != nil {
		mux.HandleFunc("GET /admin/config", s.requireSuperuser(s.getAdminConfig))
		mux.HandleFunc("POST /admin/config", s.requireSuperuser(s.postAdminConfig))
		mux.HandleFunc("POST /admin/config/json", s.requireSuperuser(s.postAdminConfigJSON))
		mux.HandleFunc("POST /admin/config/form", s.requireSuperuser(s.postAdminConfigForm))
	}
	mux.HandleFunc("GET /admin/peers", s.requireSuperuser(s.getAdminPeers))
	// The literal /inspect pattern is more specific than /{key}, so it wins; a
	// peer key is 43 base64url chars and can never collide with it.
	mux.HandleFunc("GET /admin/peers/inspect", s.requireSuperuser(s.getAdminInspectByKey))
	mux.HandleFunc("GET /admin/peers/{key}", s.requireSuperuser(s.getAdminInspect))
	mux.HandleFunc("POST /admin/peers/suspend", s.requireSuperuser(s.postAdminPeerSuspend))
	mux.HandleFunc("POST /admin/peers/unsuspend", s.requireSuperuser(s.postAdminPeerSuspend))
	mux.HandleFunc("POST /admin/peers/rename", s.requireSuperuser(s.postAdminPeerRename))

	// App modules (chat, and future special apps): each registers its own
	// routes and opens any module-owned store. The core stays agnostic — it only
	// knows the apps.App seam, never a concrete module. App routes are
	// gated behind requireAuth here so a module handler always has a session.
	deps := s.appDeps()
	for _, f := range s.apps {
		if c, ok := f.(apps.Conversations); ok {
			s.conv = c
		}
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
// no referrer leakage. The command registry and no-JS forms are designed
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

// gate admits a session that is present, unexpired and signed in to the
// kernel at this address. act says the request is the person's own act and
// counts as activity; the heartbeat's is not — a page left open is nobody
// present — so it can only observe the idle rule, never extend it.
func (s *Server) gate(act bool, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if !s.sessions.IsAuthenticated(ctx) {
			// hxRedirect, not a bare 303: a polled fragment must navigate on a
			// lost session, never swap the login page into its element.
			hxRedirect(w, r, "/login")
			return
		}
		if x := s.sessions.Expiry(ctx); x != session.Live {
			s.endSession(ctx)
			s.flashRedirect(w, r, "/login", "info", expiryMessage(x))
			return
		}
		if s.kernelChanged(w, r) {
			return
		}
		if act {
			s.sessions.Touch(ctx)
		}
		h(w, withViewer(r))
	}
}

// requireAuth gates a request the person made.
func (s *Server) requireAuth(h http.HandlerFunc) http.HandlerFunc { return s.gate(true, h) }

// endSession is the one exit: the session is destroyed, and its refresh token
// revoked (R5.4) only at the kernel that issued it. A credential travels to its
// issuer alone (ecosystem-standard §5), so a kernel that is unknown or no
// longer the pinned one is told nothing. The session ends before the call, so
// the pair revoked is the last one stored (session.Manager.Logout). A
// revocation the kernel refuses, the token being already dead after a failed
// refresh, changes nothing here; one is not abandoned with the request that
// asked for it, since the session it ends is gone either way.
func (s *Server) endSession(ctx context.Context) {
	id := s.kernelIdentity(ctx)
	issuer := id.PublicKey != "" && s.sessions.Pin(ctx) == session.Pin{PublicKey: id.PublicKey, NetworkFingerprint: id.NetworkFingerprint}
	if toks, _ := s.sessions.Logout(ctx); toks.RefreshToken != "" && issuer {
		_ = s.client.Logout(context.WithoutCancel(ctx), toks.RefreshToken)
	}
}

// expiryMessage words a sign-out by the limits, so the figures never drift.
func expiryMessage(x session.Expiry) string {
	if x == session.Idled {
		return fmt.Sprintf("You were signed out after %d minutes of inactivity.", int(session.IdleLimit.Minutes()))
	}
	return fmt.Sprintf("You were signed out after %d hours. Sign in again.", int(session.AgeLimit.Hours()))
}

// viewer is the signed-in user as this request has read it: the page
// envelope needs the balance, the handlers the address and the grants, and
// one read of /v1/me serves them all.
type viewer struct {
	user client.User
	err  error
	read bool
}

type viewerKey struct{}

// withViewer gives the request a place to keep the viewer once read.
func withViewer(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), viewerKey{}, &viewer{}))
}

// me is the signed-in user, read from the kernel at most once per request.
func (s *Server) me(r *http.Request) (client.User, error) {
	v, _ := r.Context().Value(viewerKey{}).(*viewer)
	if v != nil && v.read {
		return v.user, v.err
	}
	var u client.User
	err := s.sessions.Do(r.Context(), func(tok string) (e error) {
		u, e = s.client.Me(r.Context(), tok)
		return e
	})
	if v != nil {
		v.user, v.err, v.read = u, err, true
	}
	return u, err
}

// forgetMe drops the request's read of the viewer, after a call that moved
// its money, so the page renders the balance that call left.
func forgetMe(r *http.Request) {
	if v, _ := r.Context().Value(viewerKey{}).(*viewer); v != nil {
		v.read = false
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
	if pin.PublicKey == id.PublicKey && pin.NetworkFingerprint == id.NetworkFingerprint {
		return false
	}
	s.endSession(r.Context())
	s.flashRedirect(w, r, "/login", "error", "This address now answers for a different kernel. Sign in again.")
	return true
}

// superuserHandle is the fixed superuser identity (kernel requirements §12); the
// UI shows the admin group and superuser scope to its address only. The kernel
// remains authoritative — an admin call always re-checks the bearer.
const superuserHandle = "sys"

// isSuperuser reports whether the session belongs to the superuser: its address
// is sys@<this kernel>. The gate fails CLOSED (the admin nav silently
// disappears) while the kernel's name is unknown, which is the safe direction.
func (s *Server) isSuperuser(r *http.Request) bool {
	sys := s.identity.Address(superuserHandle)
	return sys != "" && s.sessions.Address(r.Context()) == sys
}

// requireSuperuser gates admin routes. A non-superuser (or anonymous) request
// gets a 404 error page rather than a 403 — the admin surface is not advertised
// to those who may not use it. The kernel independently rejects any admin call
// from a non-sys bearer.
func (s *Server) requireSuperuser(h http.HandlerFunc) http.HandlerFunc {
	return s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		if !s.isSuperuser(r) {
			// hxRedirect's reasoning for a demoted session: send htmx to the home
			// page rather than swap a full 404 into a polled fragment.
			if isHTMX(r) {
				w.Header().Set("HX-Redirect", "/")
				w.WriteHeader(http.StatusOK)
				return
			}
			s.renderPage(w, r, http.StatusNotFound, "error", "Not found", errorView{Message: "Page not found."})
			return
		}
		h(w, r)
	})
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
		Fragment:          s.render.HTML,
		Watch:             s.watch,
		Superuser:         s.isSuperuser,
		HandleErr:         s.handleErr,
		HandleErrFragment: s.handleErrFragment,
		FlashRedirect:     s.flashRedirect,
		CSRFField:         func(r *http.Request) template.HTML { return csrf.TemplateField(r) },
		IsHTMX:            isHTMX,
		Me:                func(r *http.Request) string { return s.sessions.Address(r.Context()) },
		Address:           s.identity.Address,
		RequireAuth:       s.requireAuth,
		RequireSuperuser:  s.requireSuperuser,
		LoadAppState:      func(slug string) (json.RawMessage, error) { return s.appStore.load(slug), nil },
		SaveAppState:      func(slug string, v json.RawMessage) error { return s.appStore.save(slug, v) },
		PublicURL:         s.publicURL,
		StatePath:         s.appStore.path,
		Home:              s.home,
		FormArgs:          buildArgs,
		ConfigEditor: func(schema json.RawMessage, hidden ...string) apps.ConfigEditor {
			return s.editor(schema, hidden...)
		},
		Logger: s.log,
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
	kernelID := s.kernelIdentity(r.Context())
	var cmds []Command
	var rail template.HTML
	if s.sessions.IsAuthenticated(r.Context()) {
		cmds = s.commands(r)
		if s.conv != nil {
			var err error
			if rail, err = s.conv.Rail(r); err != nil {
				s.log.Warn("rail", "err", err)
			}
		}
	}
	return Page{
		Title: title,
		Path:  r.URL.Path,
		// ?drawer=open is the no-JS route into a narrow screen's drawer,
		// stamped on <html> as the script would set it (§6.1).
		DrawerOpen:   r.URL.Query().Get("drawer") == "open",
		Theme:        themeFromRequest(r),
		Sidebar:      sidebarFromRequest(r),
		Commands:     cmds,
		Rail:         rail,
		Me:           s.sessions.Address(r.Context()),
		KernelHandle: kernelID.Handle,
		KernelFP:     keyFingerprint(kernelID.PublicKey),
		KernelNet:    kernelID.Network,
		KernelName:   s.kernelName,
		IsSuperuser:  super,
		Themes:       ThemeList(),
		CSRFField:    csrf.TemplateField(r),
		CSRFToken:    csrf.Token(r),
		Launcher:     template.JS(commandsJSON(cmds)),
		Flash:        flash,
		FlashKind:    flashKind,
		BadgeSecs:    int(s.badgeTTL / time.Second),
		Data:         data,
	}
}

// activeGroup maps a request path to the nav group it belongs to, for
// aria-current highlighting.
func activeGroup(path string) string {
	switch {
	case path == "/":
		return "home"
	case strings.HasPrefix(path, "/browse"):
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
// (not surfacing) template errors. The balance in the envelope is the
// request's one read of the viewer.
func (s *Server) renderPage(w http.ResponseWriter, r *http.Request, status int, name, title string, data any) {
	p := s.page(r, title, data)
	if p.Me != "" {
		if me, err := s.me(r); err == nil {
			p.Available = me.Available
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
		s.endSession(r.Context())
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
		s.endSession(r.Context())
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
// handlers (admin ops, action create-update).
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
