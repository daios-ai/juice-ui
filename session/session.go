package session

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/daios-ai/juice-ui/client"
	"golang.org/x/sync/singleflight"
)

// Limits, the UI's own policy: a session ends after IdleLimit without a
// request the person made, and AgeLimit after login whatever happened; the
// page warns Warning before the idle end. Both sit within NIST SP 800-63B-4
// §4.2.3 (1 h, 24 h) and are rev. 3's AAL2 figures. The idle rule is the
// Manager's, not scs's: scs renews on every request it loads and cannot
// except the page's heartbeat.
const (
	IdleLimit = 30 * time.Minute
	AgeLimit  = 12 * time.Hour
	Warning   = time.Minute
)

// Keys of the scs record. A request saves its own copy of the record whole,
// so a request that loaded it before another changed a value, and finishes
// after, writes the old value back. The record therefore holds what is written
// once (the address, the pin and the login time, at login) and what a stale
// save cannot make dangerous: the PKCE verifier lives within one request, the
// grant state is refused by the kernel once completed should a stale save
// restore it, and the badge cache is a cache. What a stale save would make
// wrong — the token pair, the last activity, the recovery phrase — lives in
// the Manager's store instead (live).
const (
	keyVerifier    = "pkce_verifier"
	keyAddress     = "address"      // the signed-in user's address, handle@kernel
	keyGrantState  = "grant_state"  // opaque state for an in-flight OAuth grant consent
	keyGrantAction = "grant_action" // the action the in-flight grant is for (for the return redirect)
	keyKernelKey   = "kernel_public_key"
	keyKernelNet   = "kernel_network_fingerprint"
	keyLoginAt     = "login_at" // unix nanoseconds of the login
)

// live is a session's state that no request's copy of the record may decide,
// kept by the Manager under its lock and keyed by the scs token, which is
// fixed from the moment the entry is made: only Login and PutRecoveryPhrase
// make one, each right after renewing the token, so a later RenewToken must
// move the entry with it. Every other write changes an entry that exists and
// never makes one, so a session ended while a request was in flight stays
// ended. An entry leaves at Logout, or once its record would have (expires,
// the scs Lifetime from when it was made): Sweep removes expired ones, and
// every read treats one as gone.
type live struct {
	toks     client.Tokens     // the kernel's pair; zero before login
	activeAt int64             // unix nanoseconds of the person's last request; only moves forward
	phrase   string            // the recovery phrase until acknowledged
	cache    map[string]string // what an app last read for this user, by app
	expires  time.Time
}

// Pin is the kernel this session signed in to: its public key and the
// fingerprint of the world it serves. A client sends a stored credential only to the server it
// recorded (ecosystem-standard §5), so every authenticated request compares this
// against what the kernel now reports and refuses a server that has changed.
type Pin struct {
	PublicKey          string
	NetworkFingerprint string
}

// ErrNoSession is returned by Do when no authenticated session is present.
var ErrNoSession = errors.New("no authenticated session")

// Issuer is the kernel's token lifecycle: a refresh rotates a pair, a logout
// revokes one. *client.Client satisfies it; an interface keeps the session
// layer testable without a live kernel.
type Issuer interface {
	Refresh(ctx context.Context, refreshToken string) (client.Tokens, error)
	Logout(ctx context.Context, refreshToken string) error
}

// Manager keeps a browser session: the scs record and the store of its live
// state, judged against the limits, with the authenticated request wrapper's
// single-flight refresh-and-retry (R5.2).
type Manager struct {
	sm        *scs.SessionManager
	issuer    Issuer
	refreshes singleflight.Group // one refresh per session at a time, keyed by its token
	mu        sync.Mutex
	live      map[string]*live // by scs token
	Now       func() time.Time // the clock sessions age by; tests set it
}

// NewManager wraps an scs.SessionManager and the kernel that issues its tokens.
func NewManager(sm *scs.SessionManager, k Issuer) *Manager {
	return &Manager{sm: sm, issuer: k, live: map[string]*live{}, Now: time.Now}
}

// entry runs fn on the session's live entry under the lock, and reports
// whether there was one; an expired entry is removed and counts as none.
func (m *Manager) entry(ctx context.Context, fn func(*live)) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.entryLocked(m.sm.Token(ctx), fn)
}

func (m *Manager) entryLocked(key string, fn func(*live)) bool {
	e := m.live[key]
	if e == nil {
		return false
	}
	if !m.Now().Before(e.expires) {
		delete(m.live, key)
		return false
	}
	fn(e)
	return true
}

// create renews the session token and makes its entry, carrying over the
// recovery phrase of the entry it replaces.
func (m *Manager) create(ctx context.Context, fill func(*live)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var phrase string
	m.entryLocked(m.sm.Token(ctx), func(e *live) { phrase = e.phrase })
	delete(m.live, m.sm.Token(ctx))
	if err := m.sm.RenewToken(ctx); err != nil {
		return err
	}
	e := &live{phrase: phrase, expires: m.Now().Add(m.sm.Lifetime)}
	fill(e)
	m.live[m.sm.Token(ctx)] = e
	return nil
}

// Sweep removes the expired entries every interval until ctx ends, so their
// tokens and recovery phrase leave memory even when no request reads them
// again.
func (m *Manager) Sweep(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.sweep()
		}
	}
}

func (m *Manager) sweep() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k := range m.live {
		m.entryLocked(k, func(*live) {})
	}
}

// Expiry is why a session may no longer serve.
type Expiry int

const (
	Live  Expiry = iota
	Idled        // IdleLimit passed since the person's last request
	Aged         // AgeLimit passed since login, or a login the session does not date
)

// Touch records a request the person made. The heartbeat never calls it: a
// page left open is not a person present. The time only moves forward, so a
// request that finishes late cannot set it back.
func (m *Manager) Touch(ctx context.Context) {
	now := m.Now().UnixNano()
	m.entry(ctx, func(e *live) { e.activeAt = max(e.activeAt, now) })
}

// Idle is the time since the person's last request; a session without an
// entry has been idle as long as the limit.
func (m *Manager) Idle(ctx context.Context) time.Duration {
	idle := IdleLimit
	m.entry(ctx, func(e *live) { idle = m.Now().Sub(time.Unix(0, e.activeAt)) })
	return idle
}

// Expiry judges the session against the limits.
func (m *Manager) Expiry(ctx context.Context) Expiry {
	login := m.sm.GetInt64(ctx, keyLoginAt)
	if login == 0 || m.Now().Sub(time.Unix(0, login)) >= AgeLimit {
		return Aged
	}
	if m.Idle(ctx) >= IdleLimit {
		return Idled
	}
	return Live
}

// SCS exposes the underlying scs manager so the server can mount its
// LoadAndSave middleware.
func (m *Manager) SCS() *scs.SessionManager { return m.sm }

// Login renews the session token to prevent fixation, makes the session's
// entry with the fresh token pair, and records the user's address and the
// kernel this login is for.
func (m *Manager) Login(ctx context.Context, t client.Tokens, address string, pin Pin) error {
	now := m.Now()
	if err := m.create(ctx, func(e *live) { e.toks, e.activeAt = t, now.UnixNano() }); err != nil {
		return err
	}
	m.sm.Put(ctx, keyAddress, address)
	m.sm.Put(ctx, keyKernelKey, pin.PublicKey)
	m.sm.Put(ctx, keyKernelNet, pin.NetworkFingerprint)
	m.sm.Put(ctx, keyLoginAt, now.UnixNano())
	m.sm.Remove(ctx, keyVerifier)
	return nil
}

// Pin returns the kernel this session signed in to. A session that predates the
// pin returns a zero Pin, which no live identity matches — it is signed in to a
// kernel we cannot name, and that is exactly what must not be trusted.
func (m *Manager) Pin(ctx context.Context) Pin {
	return Pin{
		PublicKey:          m.sm.GetString(ctx, keyKernelKey),
		NetworkFingerprint: m.sm.GetString(ctx, keyKernelNet),
	}
}

// Logout deletes the session's entry and destroys its record, and returns the
// pair the entry held for the caller to revoke. A request in flight can no
// longer bring either back: it changes only an entry that exists, and a
// record without one holds no session. The pair is taken under the lock that
// a refresh stores under, so every pair the kernel issued is revoked: the one
// stored before is returned here, and one stored after finds no entry and is
// revoked by the refresh.
func (m *Manager) Logout(ctx context.Context) (client.Tokens, error) {
	var t client.Tokens
	m.mu.Lock()
	m.entryLocked(m.sm.Token(ctx), func(e *live) { t = e.toks })
	delete(m.live, m.sm.Token(ctx))
	m.mu.Unlock()
	return t, m.sm.Destroy(ctx)
}

// Tokens returns the session's current token pair and whether it is signed in.
func (m *Manager) Tokens(ctx context.Context) (client.Tokens, bool) {
	var t client.Tokens
	m.entry(ctx, func(e *live) { t = e.toks })
	return t, t.AccessToken != "" && t.RefreshToken != ""
}

// PutAppCache keeps what an app last read for this session's user in the
// server-side entry, never in the session record or the browser, and gone with
// the session. It changes only an entry that exists, so a session ended
// meanwhile stays ended.
func (m *Manager) PutAppCache(ctx context.Context, app, value string) {
	m.entry(ctx, func(e *live) {
		if e.cache == nil {
			e.cache = map[string]string{}
		}
		e.cache[app] = value
	})
}

// AppCache returns what PutAppCache kept for the app.
func (m *Manager) AppCache(ctx context.Context, app string) (value string, ok bool) {
	m.entry(ctx, func(e *live) { value = e.cache[app] })
	return value, value != ""
}

// Address returns the signed-in user's address, handle@kernel, or "". A
// session that predates addresses holds no pin either, so it is logged out
// before this is read.
func (m *Manager) Address(ctx context.Context) string {
	return m.sm.GetString(ctx, keyAddress)
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

// PutRecoveryPhrase stashes the freshly-generated seed phrase server-side,
// in a new entry under a renewed session token, so it can be shown on the
// post-signup page until acknowledged. It never leaves the server except on
// that page.
func (m *Manager) PutRecoveryPhrase(ctx context.Context, phrase string) error {
	return m.create(ctx, func(e *live) { e.phrase = phrase })
}

// PeekRecoveryPhrase returns the stashed phrase without clearing it, so the
// standalone phrase page survives reloads until the user acknowledges it. ok is
// false when none is pending.
func (m *Manager) PeekRecoveryPhrase(ctx context.Context) (phrase string, ok bool) {
	m.entry(ctx, func(e *live) { phrase = e.phrase })
	return phrase, phrase != ""
}

// TakeRecoveryPhrase returns and clears the stashed seed phrase (one-shot). ok is
// false when none is pending, so the phrase page redirects away on a reload. The
// clearing is the store's, so no request's late save can show the phrase again.
func (m *Manager) TakeRecoveryPhrase(ctx context.Context) (phrase string, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := m.sm.Token(ctx)
	m.entryLocked(key, func(e *live) {
		phrase, e.phrase = e.phrase, ""
		if e.toks.RefreshToken == "" {
			delete(m.live, key) // an entry made for the phrase alone ends with it
		}
	})
	return phrase, phrase != ""
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

// refreshSingleFlight refreshes the token pair once per session however many
// requests ask at the same time. Rotation kills the prior refresh token, so a
// request that joins a refresh in flight takes its answer, and one that comes
// after finds the rotated pair in the store, where every request reads it,
// rather than presenting a token that no longer exists. The kernel call is
// not abandoned with the request that made it: a rotation it completes would
// otherwise be lost. A session ended while the kernel rotated is not brought
// back: the pair nobody now holds is revoked. The group keeps no entry past
// the call.
func (m *Manager) refreshSingleFlight(ctx context.Context, old client.Tokens) (client.Tokens, error) {
	key := m.sm.Token(ctx)
	v, err, _ := m.refreshes.Do(key, func() (any, error) {
		cur, ok := m.Tokens(ctx)
		if !ok {
			return nil, ErrNoSession
		}
		if cur.RefreshToken != old.RefreshToken {
			return cur, nil
		}
		kctx := context.WithoutCancel(ctx)
		nt, err := m.issuer.Refresh(kctx, old.RefreshToken)
		if err != nil {
			return nil, err
		}
		if !m.entry(ctx, func(e *live) { e.toks = nt }) {
			_ = m.issuer.Logout(kctx, nt.RefreshToken)
			return nil, ErrNoSession
		}
		return nt, nil
	})
	if err != nil {
		return client.Tokens{}, err
	}
	return v.(client.Tokens), nil
}
