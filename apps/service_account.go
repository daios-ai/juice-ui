package apps

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/daios-ai/juice-ui/client"
	"github.com/daios-ai/juice-ui/session"
)

// ServiceAccount manages an app's own Juice user — the "an app is a Juice user"
// lifecycle that would otherwise be hand-rolled in every app: provisioning the
// kernel account, authenticating (PKCE, plus a single refresh on a 401), and a
// resilient background loop that survives account states (suspended, a stale
// password, the kernel briefly down) WITHOUT saturating the kernel's per-IP auth
// rate limit.
//
// The app supplies the handle and the password it persists (in the app config
// store); the ServiceAccount owns everything else. Typical use in an app's Start:
//
//	a.svc = apps.NewServiceAccount(deps, "chat", password)
//	a.svc.Start(ctx, a.registerActions) // registerActions uses a.svc.Do(...)
//
// The loop backs off exponentially, so a persistent failure never hammers auth;
// it logs the cause with an operator fix (e.g. "suspended — run admin unsuspend");
// and because it keeps retrying at the cap, it self-heals the moment the operator
// resolves the account state, with no restart.
type ServiceAccount struct {
	deps     Deps
	handle   string
	password string

	mu      sync.Mutex
	toks    client.Tokens
	ensured bool // account confirmed to exist (so CreateUser is not repeated)
	ready   bool
	cancel  context.CancelFunc
}

// Backoff bounds for the maintenance loop (vars so tests can shrink them).
var (
	saBaseDelay = 3 * time.Second
	saMaxDelay  = 60 * time.Second
)

// NewServiceAccount builds a manager for kernel user `handle` with the given
// password (which the app persists).
func NewServiceAccount(deps Deps, handle, password string) *ServiceAccount {
	return &ServiceAccount{deps: deps, handle: handle, password: password}
}

// Start launches the background provision+maintain loop and returns immediately.
// setup is the app's idempotent domain bootstrap (e.g. register actions), run once
// authentication succeeds; it should use Do for authenticated calls. A nil setup
// is fine (authentication only).
func (s *ServiceAccount) Start(ctx context.Context, setup func(context.Context) error) error {
	bg, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	go s.loop(bg, setup)
	return nil
}

// Stop halts the background loop.
func (s *ServiceAccount) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
}

// Ready reports whether the account is provisioned and set up.
func (s *ServiceAccount) Ready() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ready
}

// loop provisions and maintains the account until ctx is cancelled: ensure account
// → authenticate → setup, retried as a whole with exponential backoff so it can
// never saturate the shared per-IP auth limit. On full success it marks Ready and
// stops; Do keeps tokens fresh thereafter.
func (s *ServiceAccount) loop(ctx context.Context, setup func(context.Context) error) {
	delay := saBaseDelay
	for {
		if err := s.provision(ctx, setup); err != nil {
			s.logFailure(err, delay)
		} else {
			s.mu.Lock()
			s.ready = true
			s.mu.Unlock()
			s.log().Info("app service account ready", "handle", s.handle)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		if delay *= 2; delay > saMaxDelay {
			delay = saMaxDelay
		}
	}
}

// provision ensures we are authenticated (reusing a token if we already hold one,
// so a setup retry does not re-authorize) and runs the app's setup.
func (s *ServiceAccount) provision(ctx context.Context, setup func(context.Context) error) error {
	s.mu.Lock()
	tok := s.toks.AccessToken
	s.mu.Unlock()
	if tok == "" {
		if err := s.login(ctx); err != nil {
			return err
		}
	}
	if setup != nil {
		return setup(ctx)
	}
	return nil
}

// login ensures the account exists (once) and obtains a token pair via PKCE.
func (s *ServiceAccount) login(ctx context.Context) error {
	if err := s.ensureAccount(ctx); err != nil {
		return err
	}
	verifier, err := session.NewVerifier()
	if err != nil {
		return err
	}
	code, err := s.deps.Client.Authorize(ctx, s.handle, s.password, session.Challenge(verifier))
	if err != nil {
		// Any definitive auth response (even a rejection) proves the account
		// exists, so stop trying to create it; a 429 is not definitive.
		if ke, ok := client.AsKernelError(err); ok && ke.Kind != client.KindRateLimited {
			s.mu.Lock()
			s.ensured = true
			s.mu.Unlock()
		}
		return fmt.Errorf("authorize %s: %w", s.handle, err)
	}
	toks, err := s.deps.Client.ExchangeCode(ctx, code, verifier)
	if err != nil {
		return fmt.Errorf("token exchange for %s: %w", s.handle, err)
	}
	s.mu.Lock()
	s.toks = toks
	s.ensured = true
	s.mu.Unlock()
	return nil
}

// ensureAccount creates the kernel account unless it is already confirmed to
// exist. A non-rate-limited CreateUser result (created, or "already exists")
// confirms existence; a rate-limited one is left for the next pass.
func (s *ServiceAccount) ensureAccount(ctx context.Context) error {
	s.mu.Lock()
	done := s.ensured
	s.mu.Unlock()
	if done {
		return nil
	}
	// Service accounts are headless: no seed phrase, no recovery key (the password
	// lives in the app config). Pass an empty recovery key.
	_, err := s.deps.Client.CreateUser(ctx, s.handle, s.password, "")
	if err == nil || !errors.Is(err, client.ErrRateLimited) {
		s.mu.Lock()
		s.ensured = true
		s.mu.Unlock()
		return nil
	}
	return fmt.Errorf("create %s: %w", s.handle, err)
}

// Do runs fn authenticated as the service account, refreshing once on an
// authentication error (falling back to a full re-login).
func (s *ServiceAccount) Do(ctx context.Context, fn func(token string) error) error {
	s.mu.Lock()
	tok := s.toks.AccessToken
	s.mu.Unlock()
	if tok == "" {
		if err := s.login(ctx); err != nil {
			return err
		}
		s.mu.Lock()
		tok = s.toks.AccessToken
		s.mu.Unlock()
	}
	err := fn(tok)
	if !errors.Is(err, client.ErrUnauthenticated) {
		return err
	}
	s.mu.Lock()
	rt := s.toks.RefreshToken
	s.mu.Unlock()
	if nt, rerr := s.deps.Client.Refresh(ctx, rt); rerr != nil {
		if lerr := s.login(ctx); lerr != nil {
			return lerr
		}
	} else {
		s.mu.Lock()
		s.toks = nt
		s.mu.Unlock()
	}
	s.mu.Lock()
	tok = s.toks.AccessToken
	s.mu.Unlock()
	return fn(tok)
}

// logFailure logs a provisioning failure with an actionable, state-aware message,
// so the operator can see *why* an app is down (not just that it retried).
func (s *ServiceAccount) logFailure(err error, retryIn time.Duration) {
	l := s.log()
	var ke *client.KernelError
	suspended := errors.As(err, &ke) && ke.Kind == client.KindUnauthenticated &&
		strings.Contains(strings.ToLower(ke.KernelMessage), "suspend")
	switch {
	case suspended:
		l.Warn("app service account is suspended — the app will not work until it is restored",
			"handle", s.handle, "fix", "juice admin unsuspend "+s.handle, "retry_in", retryIn.String())
	case errors.As(err, &ke) && ke.Kind == client.KindRateLimited:
		l.Warn("app service account auth is rate-limited — backing off",
			"handle", s.handle, "retry_in", retryIn.String())
	case errors.As(err, &ke) && ke.Kind == client.KindUnauthenticated:
		l.Warn("app service account login rejected — the stored password may no longer match the account",
			"handle", s.handle, "err", err, "retry_in", retryIn.String())
	default:
		l.Warn("app service account provisioning failed — retrying",
			"handle", s.handle, "err", err, "retry_in", retryIn.String())
	}
}

func (s *ServiceAccount) log() *slog.Logger {
	if s.deps.Logger != nil {
		return s.deps.Logger
	}
	return slog.Default()
}
