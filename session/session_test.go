package session

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/daios-ai/juice-ui/client"
)

type fakeRefresher struct {
	mu    sync.Mutex
	calls int
	next  client.Tokens
	err   error
}

func (f *fakeRefresher) Refresh(_ context.Context, _ string) (client.Tokens, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return client.Tokens{}, f.err
	}
	return f.next, nil
}

// newCtx returns a fresh scs session context plus the manager.
func newCtx(t *testing.T, r Refresher) (*Manager, context.Context) {
	t.Helper()
	sm := scs.New()
	m := NewManager(sm, r)
	ctx, err := sm.Load(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	return m, ctx
}

func TestPKCEHandshake(t *testing.T) {
	m, ctx := newCtx(t, &fakeRefresher{})
	challenge, err := m.BeginPKCE(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if challenge == "" {
		t.Fatal("empty challenge")
	}
	v, ok := m.TakeVerifier(ctx)
	if !ok || v == "" {
		t.Fatal("verifier not stored")
	}
	// challenge must be the S256 of the stored verifier.
	if Challenge(v) != challenge {
		t.Errorf("challenge does not match stored verifier")
	}
	// verifier is single-use.
	if _, ok := m.TakeVerifier(ctx); ok {
		t.Error("verifier should be cleared after TakeVerifier")
	}
}

func TestGrantState(t *testing.T) {
	m, ctx := newCtx(t, &fakeRefresher{})
	if _, _, ok := m.TakeGrantState(ctx); ok {
		t.Fatal("no state stored yet")
	}
	m.PutGrantState(ctx, "S1", "sys/gmail-send")
	state, action, ok := m.TakeGrantState(ctx)
	if !ok || state != "S1" || action != "sys/gmail-send" {
		t.Fatalf("got %q %q ok=%v", state, action, ok)
	}
	// single-use: a second take (and thus a replayed callback) finds nothing.
	if _, _, ok := m.TakeGrantState(ctx); ok {
		t.Error("grant state should be cleared after TakeGrantState")
	}
}

func TestLoginLogout(t *testing.T) {
	m, ctx := newCtx(t, &fakeRefresher{})
	if m.IsAuthenticated(ctx) {
		t.Fatal("should start unauthenticated")
	}
	if err := m.Login(ctx, client.Tokens{AccessToken: "at", RefreshToken: "rt"}, "alice@testnet", Pin{}); err != nil {
		t.Fatal(err)
	}
	toks, ok := m.Tokens(ctx)
	if !ok || toks.AccessToken != "at" || toks.RefreshToken != "rt" {
		t.Fatalf("tokens = %+v ok=%v", toks, ok)
	}
	if m.Address(ctx) != "alice@testnet" {
		t.Errorf("address = %q", m.Address(ctx))
	}
	if err := m.Logout(ctx); err != nil {
		t.Fatal(err)
	}
	if m.IsAuthenticated(ctx) {
		t.Error("should be unauthenticated after logout")
	}
}

func TestDoNoSession(t *testing.T) {
	m, ctx := newCtx(t, &fakeRefresher{})
	err := m.Do(ctx, func(string) error { return nil })
	if !errors.Is(err, ErrNoSession) {
		t.Errorf("expected ErrNoSession, got %v", err)
	}
}

func TestDoPassThroughNonAuthError(t *testing.T) {
	m, ctx := newCtx(t, &fakeRefresher{})
	m.Login(ctx, client.Tokens{AccessToken: "at", RefreshToken: "rt"}, "a", Pin{})
	sentinel := errors.New("boom")
	err := m.Do(ctx, func(string) error { return sentinel })
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel passthrough, got %v", err)
	}
}

func TestDoRefreshAndRetry(t *testing.T) {
	fr := &fakeRefresher{next: client.Tokens{AccessToken: "at1", RefreshToken: "rt1"}}
	m, ctx := newCtx(t, fr)
	m.Login(ctx, client.Tokens{AccessToken: "at0", RefreshToken: "rt0"}, "a", Pin{})

	var tokensSeen []string
	err := m.Do(ctx, func(tok string) error {
		tokensSeen = append(tokensSeen, tok)
		if tok == "at0" {
			return client.ErrUnauthenticated
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Do = %v", err)
	}
	if fr.calls != 1 {
		t.Errorf("refresh calls = %d want 1", fr.calls)
	}
	if len(tokensSeen) != 2 || tokensSeen[0] != "at0" || tokensSeen[1] != "at1" {
		t.Errorf("tokensSeen = %v", tokensSeen)
	}
	// rotated pair persisted.
	if toks, _ := m.Tokens(ctx); toks.AccessToken != "at1" || toks.RefreshToken != "rt1" {
		t.Errorf("rotated tokens not stored: %+v", toks)
	}
}

func TestDoRefreshFailureReturnsError(t *testing.T) {
	fr := &fakeRefresher{err: client.ErrUnauthenticated}
	m, ctx := newCtx(t, fr)
	m.Login(ctx, client.Tokens{AccessToken: "at0", RefreshToken: "rt0"}, "a", Pin{})
	err := m.Do(ctx, func(string) error { return client.ErrUnauthenticated })
	if !errors.Is(err, client.ErrUnauthenticated) {
		t.Errorf("expected refresh failure surfaced, got %v", err)
	}
}

// TestSingleFlightRefresh verifies that many concurrent requests that all hit
// ErrUnauthenticated trigger exactly one refresh (R5.2): rotation invalidates
// the prior refresh token, so concurrent refreshes would otherwise fail.
func TestSingleFlightRefresh(t *testing.T) {
	fr := &fakeRefresher{next: client.Tokens{AccessToken: "at1", RefreshToken: "rt1"}}
	m, ctx := newCtx(t, fr)
	m.Login(ctx, client.Tokens{AccessToken: "at0", RefreshToken: "rt0"}, "a", Pin{})

	const n = 25
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = m.Do(ctx, func(tok string) error {
				if tok == "at0" {
					return client.ErrUnauthenticated
				}
				return nil
			})
		}(i)
	}
	wg.Wait()
	for i, e := range errs {
		if e != nil {
			t.Errorf("goroutine %d: %v", i, e)
		}
	}
	if fr.calls != 1 {
		t.Errorf("refresh calls = %d want exactly 1 (single-flight)", fr.calls)
	}
}

// A session ends IdleLimit after the person's last request and AgeLimit after
// login whatever happened; a session that does not date its login is aged.
func TestExpiryJudgesBothLimits(t *testing.T) {
	m, ctx := newCtx(t, &fakeRefresher{})
	now := time.Unix(1_000_000, 0)
	m.Now = func() time.Time { return now }
	if m.Expiry(ctx) != Aged {
		t.Fatal("a session that does not date its login must be aged")
	}
	m.Login(ctx, client.Tokens{AccessToken: "at", RefreshToken: "rt"}, "a", Pin{})
	if m.Expiry(ctx) != Live || m.Idle(ctx) != 0 {
		t.Fatalf("after login: %v idle %v", m.Expiry(ctx), m.Idle(ctx))
	}
	now = now.Add(IdleLimit - time.Second)
	if m.Expiry(ctx) != Live {
		t.Fatal("a second before the idle limit the session serves")
	}
	now = now.Add(time.Second)
	if m.Expiry(ctx) != Idled {
		t.Fatal("at the idle limit the session is idled")
	}

	m.Login(ctx, client.Tokens{AccessToken: "at", RefreshToken: "rt"}, "a", Pin{})
	login := now
	for now.Sub(login) < AgeLimit-10*time.Minute {
		now = now.Add(10 * time.Minute)
		m.Touch(ctx)
	}
	if m.Expiry(ctx) != Live {
		t.Fatal("a session in use is live until the age limit")
	}
	now = login.Add(AgeLimit)
	m.Touch(ctx)
	if m.Expiry(ctx) != Aged {
		t.Fatal("at the age limit the session is aged, however recently used")
	}
}

// A request that meets a 401 after another has already rotated the pair takes
// the rotated pair rather than presenting the dead refresh token.
func TestRefreshAfterARotationReusesIt(t *testing.T) {
	fr := &fakeRefresher{next: client.Tokens{AccessToken: "at1", RefreshToken: "rt1"}}
	m, ctx := newCtx(t, fr)
	m.Login(ctx, client.Tokens{AccessToken: "at0", RefreshToken: "rt0"}, "a", Pin{})
	old, _ := m.Tokens(ctx)
	if _, err := m.refreshSingleFlight(ctx, old); err != nil {
		t.Fatal(err)
	}
	got, err := m.refreshSingleFlight(ctx, old)
	if err != nil || got.AccessToken != "at1" || fr.calls != 1 {
		t.Fatalf("second refresh with the old pair: %+v %v, calls %d", got, err, fr.calls)
	}
}
