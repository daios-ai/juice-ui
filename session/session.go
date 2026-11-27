// Package session holds the server-side browser session: the accounts its
// person signed in to, each with its kernel token pair, the account in use,
// and the single-flight token-refresh logic (R5.2). Tokens never leave the
// server (R8.3).
package session

import (
	"context"
	"errors"
	"fmt"
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
// once (the pin and the login time, at the first sign-in) and what a stale
// save cannot make dangerous: the grant state is refused by the kernel once
// completed should a stale save restore it, and names the account that began
// it. What a stale save would make wrong — the accounts, their tokens, the
// account in use, the last activity, the recovery phrase — lives in the
// Manager's store instead (live).
const (
	keyGrantState   = "grant_state"   // opaque state for an in-flight OAuth grant consent
	keyGrantAction  = "grant_action"  // the action the in-flight grant is for (for the return redirect)
	keyGrantAccount = "grant_account" // the account that began it
	keyKernelAddr   = "kernel_address"
	keyKernelNet    = "kernel_network_fingerprint"
	keyLoginAt      = "login_at" // unix nanoseconds of the first sign-in
)

// live is a session's state that no request's copy of the record may decide,
// kept by the Manager under its lock and keyed by the scs token. Only Login
// and PutRecoveryPhrase make an entry; Login and Use renew the token and move
// the entry with it. Every other write changes an entry that exists and never
// makes one, so a session ended while a request was in flight stays ended. An
// entry leaves at Logout, or once its record would have (expires, the scs
// Lifetime from when it was made): Sweep removes expired ones, and every read
// treats one as gone.
//
// A session holds every account its person signed in to, one of them in use
// (as Gmail's and the juice CLI's `auth use` do); the account in use is what
// every request acts as. It lives here and not in the record, since a stale
// save of the record would otherwise switch the account back.
type live struct {
	accounts map[string]*account // by address
	order    []string            // the addresses, as signed in
	using    string              // the account in use
	activeAt int64               // unix nanoseconds of the person's last request; only moves forward
	phrase   string              // the recovery phrase until acknowledged
	expires  time.Time
	ended    bool // the session ended: a refresh in flight stores nothing in it
}

// account is one signed-in account of a session.
type account struct {
	toks  client.Tokens     // the kernel's pair
	cache map[string]string // what an app last read for this account, by app
}

// acting is the account in use, nil when there is none.
func (e *live) acting() *account { return e.accounts[e.using] }

// Pin is the kernel this session signed in to: its address and the
// fingerprint of the world it serves. A client sends a stored credential only to the server it
// recorded (ecosystem-standard §5), so every authenticated request compares this
// against what the kernel now reports and refuses a server that has changed.
type Pin struct {
	Kernel             string
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
		m.endLocked(key)
		return false
	}
	fn(e)
	return true
}

// endLocked removes the entry under key, marking it ended so that a refresh
// holding it stores nothing. A renewal moves an entry and does not end it.
func (m *Manager) endLocked(key string) {
	if e := m.live[key]; e != nil {
		e.ended = true
	}
	delete(m.live, key)
}

// create renews the session token and makes its entry, carrying over the
// recovery phrase of the entry it replaces.
func (m *Manager) create(ctx context.Context, fill func(*live)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var phrase string
	m.entryLocked(m.sm.Token(ctx), func(e *live) { phrase = e.phrase })
	m.endLocked(m.sm.Token(ctx))
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

// ErrOtherKernel refuses an account of a kernel other than the one the
// session signed in to: one process serves one kernel (requirements §2).
var ErrOtherKernel = errors.New("that account is on another kernel")

// Login adds a signed-in account to the session and puts it in use. The first
// makes the session's entry and records the kernel and the time; a further one
// joins the entry, refused when its kernel is another. Either way the session
// token is renewed, against fixation and as at any change of privilege. It
// returns the pair an earlier sign-in to the same account held, for the caller
// to revoke.
func (m *Manager) Login(ctx context.Context, t client.Tokens, address string, pin Pin) (client.Tokens, error) {
	now := m.Now()
	if m.IsAuthenticated(ctx) {
		if m.Pin(ctx) != pin {
			return client.Tokens{}, ErrOtherKernel
		}
		var old client.Tokens
		ok, err := m.renew(ctx, func(e *live) {
			if a := e.accounts[address]; a != nil {
				old, a.toks = a.toks, t
			} else {
				e.accounts[address] = &account{toks: t}
				e.order = append(e.order, address)
			}
			e.using, e.activeAt = address, max(e.activeAt, now.UnixNano())
		})
		if err == nil && !ok {
			err = ErrNoSession
		}
		return old, err
	}
	err := m.create(ctx, func(e *live) {
		e.accounts = map[string]*account{address: {toks: t}}
		e.order, e.using, e.activeAt = []string{address}, address, now.UnixNano()
	})
	if err != nil {
		return client.Tokens{}, err
	}
	m.sm.Put(ctx, keyKernelAddr, pin.Kernel)
	m.sm.Put(ctx, keyKernelNet, pin.NetworkFingerprint)
	m.sm.Put(ctx, keyLoginAt, now.UnixNano())
	return client.Tokens{}, nil
}

// Use puts an account the session holds in use, renewing the session token as
// Login does. It reports whether the session holds that account.
func (m *Manager) Use(ctx context.Context, address string) bool {
	held := false
	_, err := m.renew(ctx, func(e *live) {
		if held = e.accounts[address] != nil; held {
			e.using = address
		}
	})
	return held && err == nil
}

// Accounts are the addresses the session holds, as signed in.
func (m *Manager) Accounts(ctx context.Context) []string {
	var out []string
	m.entry(ctx, func(e *live) { out = append(out, e.order...) })
	return out
}

// renew changes the session's entry with fn and renews the session token,
// moving the entry with it, in one step under the lock. It reports whether
// there was an entry.
func (m *Manager) renew(ctx context.Context, fn func(*live)) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := m.sm.Token(ctx)
	var e *live
	if !m.entryLocked(key, func(x *live) { e = x }) {
		return false, nil
	}
	fn(e)
	if err := m.sm.RenewToken(ctx); err != nil {
		return true, err
	}
	delete(m.live, key)
	m.live[m.sm.Token(ctx)] = e
	return true, nil
}

// Pin returns the kernel this session signed in to. A session that predates the
// pin returns a zero Pin, which no live identity matches — it is signed in to a
// kernel we cannot name, and that is exactly what must not be trusted.
func (m *Manager) Pin(ctx context.Context) Pin {
	return Pin{
		Kernel:             m.sm.GetString(ctx, keyKernelAddr),
		NetworkFingerprint: m.sm.GetString(ctx, keyKernelNet),
	}
}

// Logout deletes the session's entry, every account with it (one exit,
// requirements §5), and destroys its record, and returns the pairs the entry
// held for the caller to revoke. A request in flight can no longer bring
// either back: it changes only an entry that exists, and a record without one
// holds no session. The pairs are taken under the lock that a refresh stores
// under, so every pair the kernel issued is revoked: one stored before is
// returned here, and one stored after finds no entry and is revoked by the
// refresh.
func (m *Manager) Logout(ctx context.Context) ([]client.Tokens, error) {
	var out []client.Tokens
	m.mu.Lock()
	m.entryLocked(m.sm.Token(ctx), func(e *live) {
		for _, a := range e.order {
			out = append(out, e.accounts[a].toks)
		}
	})
	m.endLocked(m.sm.Token(ctx))
	m.mu.Unlock()
	return out, m.sm.Destroy(ctx)
}

// Tokens returns the token pair of the account in use and whether it is signed in.
func (m *Manager) Tokens(ctx context.Context) (client.Tokens, bool) {
	var t client.Tokens
	m.entry(ctx, func(e *live) {
		if a := e.acting(); a != nil {
			t = a.toks
		}
	})
	return t, t.AccessToken != "" && t.RefreshToken != ""
}

// UpdateAppCache replaces what an app last read for the account in use with
// what fn makes of it, in one step under the entry's lock, so concurrent
// updates each see the one before. The value lives in the server-side entry,
// never in the session record or the browser, and goes with the session. It
// changes only an entry that exists, so a session ended meanwhile stays ended
// and fn is not run.
func (m *Manager) UpdateAppCache(ctx context.Context, app string, fn func(old string) string) {
	m.entry(ctx, func(e *live) {
		a := e.acting()
		if a == nil {
			return
		}
		if a.cache == nil {
			a.cache = map[string]string{}
		}
		a.cache[app] = fn(a.cache[app])
	})
}

// AppCache returns what UpdateAppCache kept for the app.
func (m *Manager) AppCache(ctx context.Context, app string) (value string, ok bool) {
	m.entry(ctx, func(e *live) {
		if a := e.acting(); a != nil {
			value = a.cache[app]
		}
	})
	return value, value != ""
}

// Address returns the account in use, handle@kernel, or "" when signed out.
// A switch renews the session token, so a request that began before it reads
// nothing after it: no request mixes two accounts.
func (m *Manager) Address(ctx context.Context) string {
	var a string
	m.entry(ctx, func(e *live) { a = e.using })
	return a
}

// IsAuthenticated reports whether a token pair is present.
func (m *Manager) IsAuthenticated(ctx context.Context) bool {
	_, ok := m.Tokens(ctx)
	return ok
}

// PutGrantState stashes the opaque state (and target action) for an in-flight
// OAuth grant consent, server-side, keyed by the session cookie — so it survives
// from the start redirect to the provider callback and never reaches the browser
// as anything the browser can forge.
func (m *Manager) PutGrantState(ctx context.Context, state, action string) {
	m.sm.Put(ctx, keyGrantState, state)
	m.sm.Put(ctx, keyGrantAction, action)
	m.sm.Put(ctx, keyGrantAccount, m.Address(ctx))
}

// TakeGrantState returns and clears the stored grant state + action. The caller
// must require an exact state match against the callback's state parameter. A
// consent begun by another account of the session is none of this one's.
func (m *Manager) TakeGrantState(ctx context.Context) (state, action string, ok bool) {
	state = m.sm.GetString(ctx, keyGrantState)
	if state == "" {
		return "", "", false
	}
	action = m.sm.GetString(ctx, keyGrantAction)
	account := m.sm.GetString(ctx, keyGrantAccount)
	m.sm.Remove(ctx, keyGrantState)
	m.sm.Remove(ctx, keyGrantAction)
	m.sm.Remove(ctx, keyGrantAccount)
	if account != m.Address(ctx) {
		return "", "", false
	}
	return state, action, true
}

// PutRecoveryPhrase stashes the freshly-generated seed phrase server-side, so
// it can be shown on the post-signup page until acknowledged; it never leaves
// the server except on that page. A session signed in keeps its entry and its
// accounts; otherwise a new entry is made under a renewed session token.
func (m *Manager) PutRecoveryPhrase(ctx context.Context, phrase string) error {
	if m.entry(ctx, func(e *live) { e.phrase = phrase }) {
		return nil
	}
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
		if len(e.accounts) == 0 {
			m.endLocked(key) // an entry made for the phrase alone ends with it
		}
	})
	return phrase, phrase != ""
}

// Do runs fn with the access token of the account in use. If fn returns
// ErrUnauthenticated, Do refreshes that pair once (single-flight per session
// and account) and retries fn exactly once. A failed refresh returns the refresh error so the caller can
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

// refreshSingleFlight refreshes an account's token pair once per session
// however many requests ask at the same time. Rotation kills the prior refresh token, so a
// request that joins a refresh in flight takes its answer, and one that comes
// after finds the rotated pair in the store, where every request reads it,
// rather than presenting a token that no longer exists. The kernel call is
// not abandoned with the request that made it: a rotation it completes would
// otherwise be lost. A session ended while the kernel rotated is not brought
// back: the pair nobody now holds is revoked. The group keeps no entry past
// the call.
func (m *Manager) refreshSingleFlight(ctx context.Context, old client.Tokens) (client.Tokens, error) {
	address := m.Address(ctx)
	// The refresh holds the entry itself, not the session token that named it:
	// a switch renews the token and moves the entry, and the rotated pair must
	// still reach its account, while an ended session takes none.
	var e *live
	m.entry(ctx, func(x *live) { e = x })
	if e == nil {
		return client.Tokens{}, ErrNoSession
	}
	tokens := func() (client.Tokens, bool) {
		m.mu.Lock()
		defer m.mu.Unlock()
		if a := e.accounts[address]; a != nil && !e.ended {
			return a.toks, true
		}
		return client.Tokens{}, false
	}
	v, err, _ := m.refreshes.Do(fmt.Sprintf("%p %s", e, address), func() (any, error) {
		cur, ok := tokens()
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
		m.mu.Lock()
		a := e.accounts[address]
		stored := a != nil && !e.ended
		if stored {
			a.toks = nt
		}
		m.mu.Unlock()
		if !stored {
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
