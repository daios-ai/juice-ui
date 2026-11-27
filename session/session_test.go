package session

import (
	"context"
	"errors"
	"strings"
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

// Logout records nothing the tests read; the server tests count revocations.
func (f *fakeRefresher) Logout(context.Context, string) error { return nil }

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
func newCtx(t *testing.T, r Issuer) (*Manager, context.Context) {
	t.Helper()
	sm := scs.New()
	m := NewManager(sm, r)
	ctx, err := sm.Load(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	return m, ctx
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
	if _, err := m.Login(ctx, client.Tokens{AccessToken: "at", RefreshToken: "rt"}, "alice@testnet", Pin{}); err != nil {
		t.Fatal(err)
	}
	toks, ok := m.Tokens(ctx)
	if !ok || toks.AccessToken != "at" || toks.RefreshToken != "rt" {
		t.Fatalf("tokens = %+v ok=%v", toks, ok)
	}
	if m.Address(ctx) != "alice@testnet" {
		t.Errorf("address = %q", m.Address(ctx))
	}
	if t2, err := m.Logout(ctx); err != nil || len(t2) != 1 || t2[0] != toks {
		t.Fatalf("logout returned %+v, %v; want the pair it held", t2, err)
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

	m.Logout(ctx)
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

// request loads a separate copy of the session record, as scs does for each
// HTTP request.
func request(t *testing.T, m *Manager, ctx context.Context) context.Context {
	t.Helper()
	tok, _, err := m.sm.Commit(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c, err := m.sm.Load(context.Background(), tok)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// The acknowledged phrase is gone for every request, whatever copy of the
// record a request loaded before the acknowledgement.
func TestAnAcknowledgedPhraseStaysGone(t *testing.T) {
	m, ctx := newCtx(t, &fakeRefresher{})
	if err := m.PutRecoveryPhrase(ctx, "w1 w2"); err != nil {
		t.Fatal(err)
	}
	stale := request(t, m, ctx)
	if p, ok := m.PeekRecoveryPhrase(stale); !ok || p != "w1 w2" {
		t.Fatalf("peek = %q %v", p, ok)
	}
	if p, ok := m.TakeRecoveryPhrase(request(t, m, ctx)); !ok || p != "w1 w2" {
		t.Fatalf("take = %q %v", p, ok)
	}
	if _, _, err := m.sm.Commit(stale); err != nil { // the late save of a copy loaded before
		t.Fatal(err)
	}
	if _, ok := m.PeekRecoveryPhrase(request(t, m, ctx)); ok {
		t.Error("a late save brought the phrase back")
	}
	if len(m.live) != 0 {
		t.Errorf("an entry made for the phrase alone must end with it: %d left", len(m.live))
	}
}

// A signed-out session's entry is never made again by a request in flight.
func TestASignedOutEntryIsNotRecreated(t *testing.T) {
	m, ctx := newCtx(t, &fakeRefresher{})
	m.Login(ctx, client.Tokens{AccessToken: "at", RefreshToken: "rt"}, "a", Pin{})
	inflight := request(t, m, ctx)
	if _, err := m.Logout(request(t, m, ctx)); err != nil {
		t.Fatal(err)
	}
	m.Touch(inflight)
	if m.IsAuthenticated(inflight) || len(m.live) != 0 {
		t.Error("a request in flight brought the session back")
	}
}

// An entry ends with its record: past the scs Lifetime it reads as gone, and
// a sweep removes it.
func TestAnEntryExpiresWithItsRecord(t *testing.T) {
	m, ctx := newCtx(t, &fakeRefresher{})
	now := time.Unix(1_000_000, 0)
	m.Now = func() time.Time { return now }
	m.Login(ctx, client.Tokens{AccessToken: "at", RefreshToken: "rt"}, "a", Pin{})
	now = now.Add(m.sm.Lifetime)
	if m.IsAuthenticated(ctx) {
		t.Fatal("an entry past the record's lifetime must read as gone")
	}
	m.Login(ctx, client.Tokens{AccessToken: "at", RefreshToken: "rt"}, "a", Pin{})
	now = now.Add(time.Minute)
	other, _ := m.sm.Load(context.Background(), "")
	m.Login(other, client.Tokens{AccessToken: "at", RefreshToken: "rt"}, "b", Pin{})
	now = now.Add(m.sm.Lifetime - time.Minute)
	m.sweep()
	if len(m.live) != 1 || !m.IsAuthenticated(other) {
		t.Errorf("a sweep must remove the expired entry alone: %d left", len(m.live))
	}
}

// A signup's pending phrase survives the sign-in that follows, as it did when
// it lived in the record.
func TestLoginCarriesThePendingPhrase(t *testing.T) {
	m, ctx := newCtx(t, &fakeRefresher{})
	m.PutRecoveryPhrase(ctx, "w1 w2")
	m.Login(ctx, client.Tokens{AccessToken: "at", RefreshToken: "rt"}, "a", Pin{})
	if p, ok := m.PeekRecoveryPhrase(ctx); !ok || p != "w1 w2" {
		t.Errorf("phrase after login = %q %v", p, ok)
	}
	if len(m.live) != 1 {
		t.Errorf("the pre-login entry must be replaced, not kept: %d", len(m.live))
	}
}

// What an app caches lives in the session's entry beside its tokens: it survives
// a stale save, leaves with the session, is not carried into a new login, and
// never brings an ended session back.
func TestAppCacheLivesInTheEntry(t *testing.T) {
	m, ctx := newCtx(t, &fakeRefresher{})
	put(m, ctx, "chat", "before-login")
	if _, ok := m.AppCache(ctx, "chat"); ok {
		t.Fatal("no entry holds nothing, and none is made")
	}
	if _, err := m.Login(ctx, client.Tokens{AccessToken: "a", RefreshToken: "r"}, "ann@k", Pin{}); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.AppCache(ctx, "chat"); ok {
		t.Fatal("a new login holds nothing yet")
	}
	put(m, ctx, "chat", "K1")
	put(m, ctx, "other", "K2")
	if k, ok := m.AppCache(ctx, "chat"); !ok || k != "K1" {
		t.Fatalf("AppCache = %q %v", k, ok)
	}
	if k, _ := m.AppCache(ctx, "other"); k != "K2" {
		t.Fatal("each app's own")
	}
	put(m, ctx, "chat", "K3")
	if _, err := m.Login(ctx, client.Tokens{AccessToken: "b", RefreshToken: "s"}, "bob@k", Pin{}); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.AppCache(ctx, "chat"); ok {
		t.Fatal("one login's cache is never another's")
	}
	put(m, ctx, "chat", "K4")
	if _, err := m.Logout(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.AppCache(ctx, "chat"); ok {
		t.Fatal("it leaves with the session")
	}
	put(m, ctx, "chat", "late")
	if _, ok := m.AppCache(ctx, "chat"); ok {
		t.Fatal("a late write brings nothing back")
	}
}

// put replaces an app's cache whatever it held.
func put(m *Manager, ctx context.Context, app, value string) {
	m.UpdateAppCache(ctx, app, func(string) string { return value })
}

// An update reads and writes the cache under the entry's lock, so concurrent
// updates each see the one before and none is lost; it reaches the entry from
// a context detached from its request (context.WithoutCancel), after that
// request has ended; and an ended session runs no update.
func TestUpdateAppCacheIsOneStep(t *testing.T) {
	m, ctx := newCtx(t, &fakeRefresher{})
	if _, err := m.Login(ctx, client.Tokens{AccessToken: "a", RefreshToken: "r"}, "ann@k", Pin{}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.UpdateAppCache(ctx, "chat", func(old string) string { return old + "x" })
		}()
	}
	wg.Wait()
	if k, _ := m.AppCache(ctx, "chat"); k != strings.Repeat("x", 50) {
		t.Fatalf("an update was lost: %d of 50", len(k))
	}
	req, cancel := context.WithCancel(ctx)
	detached := context.WithoutCancel(req)
	cancel()
	m.UpdateAppCache(detached, "chat", func(string) string { return "late" })
	if k, _ := m.AppCache(ctx, "chat"); k != "late" {
		t.Fatalf("a detached context reaches the entry: %q", k)
	}
	if _, err := m.Logout(ctx); err != nil {
		t.Fatal(err)
	}
	ran := false
	m.UpdateAppCache(ctx, "chat", func(string) string { ran = true; return "back" })
	if _, ok := m.AppCache(ctx, "chat"); ok || ran {
		t.Fatal("an ended session runs no update")
	}
}

// A session holds every account its person signed in to, each with its own
// tokens and caches, one in use; a sign-in to another adds it and puts it in
// use, Use switches among them, and Logout ends them all.
func TestASessionHoldsAccounts(t *testing.T) {
	m, ctx := newCtx(t, &fakeRefresher{})
	pin := Pin{Kernel: "K", NetworkFingerprint: "F"}
	if _, err := m.Login(ctx, client.Tokens{AccessToken: "a1", RefreshToken: "r1"}, "sys@k", pin); err != nil {
		t.Fatal(err)
	}
	put(m, ctx, "chat", "sys's")
	if _, err := m.Login(ctx, client.Tokens{AccessToken: "b1", RefreshToken: "s1"}, "helper@k", pin); err != nil {
		t.Fatal(err)
	}
	if m.Address(ctx) != "helper@k" || strings.Join(m.Accounts(ctx), ",") != "sys@k,helper@k" {
		t.Fatalf("in use %q of %v", m.Address(ctx), m.Accounts(ctx))
	}
	if toks, _ := m.Tokens(ctx); toks.AccessToken != "b1" {
		t.Errorf("the account in use's tokens: %+v", toks)
	}
	if _, ok := m.AppCache(ctx, "chat"); ok {
		t.Error("each account its own cache")
	}
	if m.Use(ctx, "nobody@k") || m.Address(ctx) != "helper@k" {
		t.Fatal("only an account the session holds is put in use")
	}
	if !m.Use(ctx, "sys@k") || m.Address(ctx) != "sys@k" {
		t.Fatal("Use switches")
	}
	if c, _ := m.AppCache(ctx, "chat"); c != "sys's" {
		t.Errorf("the cache comes back with its account: %q", c)
	}
	if old, err := m.Login(ctx, client.Tokens{AccessToken: "b2", RefreshToken: "s2"}, "helper@k", pin); err != nil || old.RefreshToken != "s1" {
		t.Errorf("a second sign-in to an account replaces its pair and returns the old one: %+v %v", old, err)
	}
	if len(m.Accounts(ctx)) != 2 {
		t.Errorf("no account twice: %v", m.Accounts(ctx))
	}
	if _, err := m.Login(ctx, client.Tokens{AccessToken: "x", RefreshToken: "y"}, "ann@other", Pin{Kernel: "K2"}); !errors.Is(err, ErrOtherKernel) {
		t.Errorf("an account of another kernel is refused: %v", err)
	}
	pairs, err := m.Logout(ctx)
	if err != nil || len(pairs) != 2 || pairs[0].RefreshToken != "r1" || pairs[1].RefreshToken != "s2" {
		t.Errorf("Logout returns every pair: %+v %v", pairs, err)
	}
	if m.IsAuthenticated(ctx) || len(m.Accounts(ctx)) != 0 {
		t.Error("one exit ends every account")
	}
}

// Login and Use renew the session token (a change of privilege, OWASP's
// session management rule), the entry moving with it.
func TestSwitchingRenewsTheSessionToken(t *testing.T) {
	m, ctx := newCtx(t, &fakeRefresher{})
	m.Login(ctx, client.Tokens{AccessToken: "a", RefreshToken: "r"}, "sys@k", Pin{})
	m.Login(ctx, client.Tokens{AccessToken: "b", RefreshToken: "s"}, "helper@k", Pin{})
	before := m.sm.Token(ctx)
	if !m.Use(ctx, "sys@k") {
		t.Fatal("switch")
	}
	if m.sm.Token(ctx) == before || len(m.live) != 1 || !m.IsAuthenticated(ctx) {
		t.Errorf("token renewed and the entry moved with it: %d entries", len(m.live))
	}
}

// A request that began before a switch reads nothing after it, so no request
// mixes two accounts; a stale save of the record cannot switch back, since the
// account in use is not in the record; a refresh is stored on its own account.
func TestNoRequestMixesTwoAccounts(t *testing.T) {
	fr := &fakeRefresher{next: client.Tokens{AccessToken: "b2", RefreshToken: "s2"}}
	m, ctx := newCtx(t, fr)
	m.Login(ctx, client.Tokens{AccessToken: "a", RefreshToken: "r"}, "sys@k", Pin{})
	m.Login(ctx, client.Tokens{AccessToken: "b", RefreshToken: "s"}, "helper@k", Pin{})
	stale := request(t, m, ctx)
	inflight := request(t, m, ctx)
	if m.Address(inflight) != "helper@k" {
		t.Fatal("before the switch")
	}
	if !m.Use(ctx, "sys@k") {
		t.Fatal("switch")
	}
	if m.Address(inflight) != "" || m.IsAuthenticated(inflight) {
		t.Errorf("a request in flight reads nothing after a switch: %q", m.Address(inflight))
	}
	if _, _, err := m.sm.Commit(stale); err != nil {
		t.Fatal(err)
	}
	if m.Address(ctx) != "sys@k" {
		t.Errorf("a stale save switched back: %q", m.Address(ctx))
	}
	m.Use(ctx, "helper@k")
	var seen []string
	_ = m.Do(ctx, func(tok string) error {
		seen = append(seen, tok)
		if tok == "b" {
			return client.ErrUnauthenticated
		}
		return nil
	})
	m.Use(ctx, "sys@k")
	if toks, _ := m.Tokens(ctx); strings.Join(seen, ",") != "b,b2" || toks.AccessToken != "a" {
		t.Errorf("a refresh rotates its own account's pair alone: %v, sys's %+v", seen, toks)
	}
}

// A consent begun by one account is refused to another account of the session.
func TestAGrantStateIsItsAccounts(t *testing.T) {
	m, ctx := newCtx(t, &fakeRefresher{})
	m.Login(ctx, client.Tokens{AccessToken: "a", RefreshToken: "r"}, "sys@k", Pin{})
	m.Login(ctx, client.Tokens{AccessToken: "b", RefreshToken: "s"}, "helper@k", Pin{})
	m.PutGrantState(ctx, "S", "x@k/a")
	m.Use(ctx, "sys@k")
	if _, _, ok := m.TakeGrantState(ctx); ok {
		t.Error("another account's consent")
	}
}

// Creating an account while signed in keeps the session and its accounts.
func TestAPhraseWhileSignedInKeepsTheAccounts(t *testing.T) {
	m, ctx := newCtx(t, &fakeRefresher{})
	m.Login(ctx, client.Tokens{AccessToken: "a", RefreshToken: "r"}, "sys@k", Pin{})
	if err := m.PutRecoveryPhrase(ctx, "w1 w2"); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.TakeRecoveryPhrase(ctx); !ok || !m.IsAuthenticated(ctx) || m.Address(ctx) != "sys@k" {
		t.Error("the phrase came and went; the session stayed")
	}
}

// blockingRefresher holds a refresh at the kernel until released, and counts
// revocations.
type blockingRefresher struct {
	entered, release chan struct{}
	next             client.Tokens
	mu               sync.Mutex
	logouts          int
}

func (b *blockingRefresher) Refresh(context.Context, string) (client.Tokens, error) {
	close(b.entered)
	<-b.release
	return b.next, nil
}

func (b *blockingRefresher) Logout(context.Context, string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.logouts++
	return nil
}

// A switch while one of the session's accounts is being refreshed moves the
// entry to a new session token; the refresh still stores the rotated pair on
// its account and revokes nothing, so the account stays usable.
func TestASwitchDuringARefreshKeepsTheRotation(t *testing.T) {
	b := &blockingRefresher{entered: make(chan struct{}), release: make(chan struct{}), next: client.Tokens{AccessToken: "b2", RefreshToken: "s2"}}
	m, ctx := newCtx(t, b)
	m.Login(ctx, client.Tokens{AccessToken: "a", RefreshToken: "r"}, "sys@k", Pin{})
	m.Login(ctx, client.Tokens{AccessToken: "b", RefreshToken: "s"}, "helper@k", Pin{})
	inflight := request(t, m, ctx)
	done := make(chan error, 1)
	go func() {
		done <- m.Do(inflight, func(tok string) error {
			if tok == "b" {
				return client.ErrUnauthenticated
			}
			return nil
		})
	}()
	<-b.entered
	if !m.Use(ctx, "sys@k") {
		t.Fatal("switch")
	}
	close(b.release)
	if err := <-done; err != nil {
		t.Fatalf("the refresh in flight: %v", err)
	}
	m.Use(ctx, "helper@k")
	if toks, _ := m.Tokens(ctx); toks.RefreshToken != "s2" || b.logouts != 0 {
		t.Errorf("the rotated pair reached its account and nothing was revoked: %+v, %d revocations", toks, b.logouts)
	}
}
