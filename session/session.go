package session

import (
	"context"
	"errors"
	"sync"

	"github.com/alexedwards/scs/v2"
	"github.com/daios-ai/juice-ui/client"
)

// session value keys.
const (
	keyAccess      = "access_token"
	keyRefresh     = "refresh_token"
	keyVerifier    = "pkce_verifier"
	keyHandle      = "handle"          // the signed-in user's handle, for display only
	keyGrantState  = "grant_state"     // opaque state for an in-flight OAuth grant consent
	keyGrantAction = "grant_action"    // the action the in-flight grant is for (for the return redirect)
	keyRecovery    = "recovery_phrase" // one-shot seed phrase to show once after signup
	keyKernelKey   = "kernel_public_key"
	keyKernelNet   = "kernel_network_digest"
)

// Pin is the kernel this session signed in to: its public key and the digest of
// the world it serves. A client sends a stored credential only to the server it
// recorded (ecosystem-standard §5), so every authenticated request compares this
// against what the kernel now reports and refuses a server that has changed.
type Pin struct {
	PublicKey     string
	NetworkDigest string
}

// ErrNoSession is returned by Do when no authenticated session is present.
var ErrNoSession = errors.New("no authenticated session")

// Refresher performs a kernel token refresh. *client.Client satisfies this; an
// interface keeps the session layer testable without a live kernel.
type Refresher interface {
	Refresh(ctx context.Context, refreshToken string) (client.Tokens, error)
}

// Manager stores the kernel token pair and PKCE state in a server-side scs
// session and provides the authenticated request wrapper with single-flight
// refresh-and-retry (R5.2).
type Manager struct {
	sm        *scs.SessionManager
	refresher Refresher
	locks     sync.Map // session token -> *sync.Mutex, serializes refresh per session
}

// NewManager wraps an scs.SessionManager and a Refresher.
func NewManager(sm *scs.SessionManager, r Refresher) *Manager {
	return &Manager{sm: sm, refresher: r}
}

// SCS exposes the underlying scs manager so the server can mount its
// LoadAndSave middleware.
func (m *Manager) SCS() *scs.SessionManager { return m.sm }

// Login stores a fresh token pair, the user handle and the kernel this login is
// for, and renews the session token to prevent fixation.
func (m *Manager) Login(ctx context.Context, t client.Tokens, handle string, pin Pin) error {
	if err := m.sm.RenewToken(ctx); err != nil {
		return err
	}
	m.sm.Put(ctx, keyAccess, t.AccessToken)
	m.sm.Put(ctx, keyRefresh, t.RefreshToken)
	m.sm.Put(ctx, keyHandle, handle)
	m.sm.Put(ctx, keyKernelKey, pin.PublicKey)
	m.sm.Put(ctx, keyKernelNet, pin.NetworkDigest)
	m.sm.Remove(ctx, keyVerifier)
	return nil
}

// Pin returns the kernel this session signed in to. A session that predates the
// pin returns a zero Pin, which no live identity matches — it is signed in to a
// kernel we cannot name, and that is exactly what must not be trusted.
func (m *Manager) Pin(ctx context.Context) Pin {
	return Pin{
		PublicKey:     m.sm.GetString(ctx, keyKernelKey),
		NetworkDigest: m.sm.GetString(ctx, keyKernelNet),
	}
}

// Logout destroys the session.
func (m *Manager) Logout(ctx context.Context) error {
	return m.sm.Destroy(ctx)
}

// Tokens returns the stored token pair and whether a session is authenticated.
func (m *Manager) Tokens(ctx context.Context) (client.Tokens, bool) {
	at := m.sm.GetString(ctx, keyAccess)
	rt := m.sm.GetString(ctx, keyRefresh)
	if at == "" || rt == "" {
		return client.Tokens{}, false
	}
	return client.Tokens{AccessToken: at, RefreshToken: rt}, true
}

// Handle returns the signed-in user's handle for display, or "".
func (m *Manager) Handle(ctx context.Context) string {
	return m.sm.GetString(ctx, keyHandle)
}

// IsAuthenticated reports whether a token pair is present.
func (m *Manager) IsAuthenticated(ctx context.Context) bool {
	_, ok := m.Tokens(ctx)
	return ok
}

// BeginPKCE generates a verifier, stores it server-side, and returns the S256
// challenge to send to the kernel's authorize endpoint. The verifier is never
// exposed to the browser (R5.1).
func (m *Manager) BeginPKCE(ctx context.Context) (challenge string, err error) {
	v, err := NewVerifier()
	if err != nil {
		return "", err
	}
	m.sm.Put(ctx, keyVerifier, v)
	return Challenge(v), nil
}

// TakeVerifier returns and clears the stored PKCE verifier.
func (m *Manager) TakeVerifier(ctx context.Context) (string, bool) {
	v := m.sm.GetString(ctx, keyVerifier)
	if v == "" {
		return "", false
	}
	m.sm.Remove(ctx, keyVerifier)
	return v, true
}

// PutGrantState stashes the opaque state (and target action) for an in-flight
// OAuth grant consent, server-side, keyed by the session cookie — so it survives
// from the start redirect to the provider callback and never reaches the browser
// as anything the browser can forge.
func (m *Manager) PutGrantState(ctx context.Context, state, action string) {
	m.sm.Put(ctx, keyGrantState, state)
	m.sm.Put(ctx, keyGrantAction, action)
}

// TakeGrantState returns and clears the stored grant state + action. The caller
// must require an exact state match against the callback's state parameter.
func (m *Manager) TakeGrantState(ctx context.Context) (state, action string, ok bool) {
	state = m.sm.GetString(ctx, keyGrantState)
	if state == "" {
		return "", "", false
	}
	action = m.sm.GetString(ctx, keyGrantAction)
	m.sm.Remove(ctx, keyGrantState)
	m.sm.Remove(ctx, keyGrantAction)
	return state, action, true
}

// PutRecoveryPhrase stashes the freshly-generated seed phrase server-side, keyed
// by the session cookie, so it can be shown exactly once on the post-signup page
// and never re-rendered. It never leaves the server except on that one render.
func (m *Manager) PutRecoveryPhrase(ctx context.Context, phrase string) {
	m.sm.Put(ctx, keyRecovery, phrase)
}

// PeekRecoveryPhrase returns the stashed phrase without clearing it, so the
// standalone phrase page survives reloads until the user acknowledges it. ok is
// false when none is pending.
func (m *Manager) PeekRecoveryPhrase(ctx context.Context) (phrase string, ok bool) {
	phrase = m.sm.GetString(ctx, keyRecovery)
	return phrase, phrase != ""
}

// TakeRecoveryPhrase returns and clears the stashed seed phrase (one-shot). ok is
// false when none is pending, so the phrase page redirects away on a reload.
func (m *Manager) TakeRecoveryPhrase(ctx context.Context) (phrase string, ok bool) {
	phrase = m.sm.GetString(ctx, keyRecovery)
	if phrase == "" {
		return "", false
	}
	m.sm.Remove(ctx, keyRecovery)
	return phrase, true
}

// Do runs fn with the current access token. If fn returns ErrUnauthenticated,
// Do refreshes the token pair once (single-flight per session) and retries fn
// exactly once. A failed refresh returns the refresh error so the caller can
// clear the session and send the user to login (R5.3).
func (m *Manager) Do(ctx context.Context, fn func(token string) error) error {
	toks, ok := m.Tokens(ctx)
	if !ok {
		return ErrNoSession
	}
	err := fn(toks.AccessToken)
	if !errors.Is(err, client.ErrUnauthenticated) {
		return err
	}
	rotated, rerr := m.refreshSingleFlight(ctx, toks)
	if rerr != nil {
		return rerr
	}
	return fn(rotated.AccessToken)
}

// refreshSingleFlight refreshes the token pair, ensuring only one refresh per
// session occurs even under concurrent requests. Because rotation invalidates
// the prior refresh token, a second concurrent refresh would fail; the loser of
// the lock instead reuses the pair the winner stored.
func (m *Manager) refreshSingleFlight(ctx context.Context, old client.Tokens) (client.Tokens, error) {
	mu := m.lockFor(m.sm.Token(ctx))
	mu.Lock()
	defer mu.Unlock()

	// Another request may have rotated while we waited for the lock.
	if cur, ok := m.Tokens(ctx); ok && cur.RefreshToken != old.RefreshToken {
		return cur, nil
	}
	nt, err := m.refresher.Refresh(ctx, old.RefreshToken)
	if err != nil {
		return client.Tokens{}, err
	}
	m.sm.Put(ctx, keyAccess, nt.AccessToken)
	m.sm.Put(ctx, keyRefresh, nt.RefreshToken)
	return nt, nil
}

func (m *Manager) lockFor(token string) *sync.Mutex {
	v, _ := m.locks.LoadOrStore(token, &sync.Mutex{})
	return v.(*sync.Mutex)
}
