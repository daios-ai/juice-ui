package apps

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/daios-ai/juice-ui/client"
)

// fakeAuth is a minimal kernel for the auth lifecycle: create-user, PKCE
// authorize/token, and refresh. Its authorize behaviour is switchable at runtime
// so a test can suspend then restore the account. All calls are counted.
type fakeAuth struct {
	srv *httptest.Server
	mu  sync.Mutex

	creates, authorizes, refreshes int
	authStatus                     int    // authorize response status (200 = ok)
	authBody                       string // body when authStatus != 200
	nextTok                        int
}

func newFakeAuth() *fakeAuth {
	f := &fakeAuth{authStatus: 200}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/users", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.creates++
		f.mu.Unlock()
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{}`))
	})
	mux.HandleFunc("POST /v1/auth/authorize", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.authorizes++
		st, body := f.authStatus, f.authBody
		f.mu.Unlock()
		if st == 200 {
			_, _ = w.Write([]byte(`{"redirect":"?code=c"}`))
			return
		}
		w.WriteHeader(st)
		_, _ = w.Write([]byte(body))
	})
	mux.HandleFunc("POST /v1/auth/token", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.nextTok++
		n := f.nextTok
		f.mu.Unlock()
		_, _ = w.Write([]byte(`{"access_token":"at` + itoa(n) + `","refresh_token":"rt` + itoa(n) + `"}`))
	})
	mux.HandleFunc("POST /v1/auth/refresh", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.refreshes++
		f.nextTok++
		n := f.nextTok
		f.mu.Unlock()
		_, _ = w.Write([]byte(`{"access_token":"at` + itoa(n) + `","refresh_token":"rt` + itoa(n) + `"}`))
	})
	f.srv = httptest.NewServer(mux)
	return f
}

func (f *fakeAuth) setAuth(status int, body string) {
	f.mu.Lock()
	f.authStatus, f.authBody = status, body
	f.mu.Unlock()
}
func (f *fakeAuth) counts() (int, int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.creates, f.authorizes, f.refreshes
}
func (f *fakeAuth) close() { f.srv.Close() }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// syncBuf is a concurrency-safe buffer for capturing slog output.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func newSA(t *testing.T, f *fakeAuth, buf *syncBuf) *ServiceAccount {
	t.Helper()
	deps := Deps{Client: client.New(f.srv.URL, nil)}
	if buf != nil {
		deps.Logger = slog.New(slog.NewTextHandler(buf, nil))
	}
	return NewServiceAccount(deps, "@svc", "svcpass123")
}

func waitFor(t *testing.T, cond func() bool, within time.Duration, msg string) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s: %s", within, msg)
}

func TestServiceAccountProvisionsAndBecomesReady(t *testing.T) {
	f := newFakeAuth()
	defer f.close()
	sa := newSA(t, f, nil)

	var setupCalls int32
	setup := func(ctx context.Context) error {
		// setup runs authenticated; a Do here must reuse the loop's token.
		return sa.Do(ctx, func(tok string) error {
			if tok == "" {
				t.Error("setup got empty token")
			}
			setupCalls++
			return nil
		})
	}
	sa.Start(context.Background(), setup)
	defer sa.Stop()

	waitFor(t, sa.Ready, time.Second, "Ready")
	if setupCalls == 0 {
		t.Error("setup not called")
	}
	creates, authorizes, _ := f.counts()
	if creates != 1 {
		t.Errorf("CreateUser called %d times, want 1", creates)
	}
	if authorizes != 1 {
		t.Errorf("authorize called %d times, want 1 (loop authenticated once; Do reused the token)", authorizes)
	}
}

func TestDoRefreshesOnUnauthenticated(t *testing.T) {
	f := newFakeAuth()
	defer f.close()
	sa := newSA(t, f, nil)
	sa.Start(context.Background(), nil)
	defer sa.Stop()
	waitFor(t, sa.Ready, time.Second, "ready")

	calls := 0
	err := sa.Do(context.Background(), func(tok string) error {
		calls++
		if calls == 1 {
			return client.ErrUnauthenticated // force a refresh
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if calls != 2 {
		t.Errorf("fn called %d times, want 2 (retry after refresh)", calls)
	}
	if _, _, refreshes := f.counts(); refreshes != 1 {
		t.Errorf("refreshes = %d, want 1", refreshes)
	}
}

func TestSuspendedLogsUnsuspendHintBacksOffAndSelfHeals(t *testing.T) {
	// Shrink backoff so the test is fast.
	defer func(b, m time.Duration) { saBaseDelay, saMaxDelay = b, m }(saBaseDelay, saMaxDelay)
	saBaseDelay, saMaxDelay = 15*time.Millisecond, 40*time.Millisecond

	f := newFakeAuth()
	defer f.close()
	f.setAuth(401, `{"error":"account suspended"}`) // start suspended
	buf := &syncBuf{}
	sa := newSA(t, f, buf)
	sa.Start(context.Background(), nil)
	defer sa.Stop()

	// It logs the actionable unsuspend hint, and does NOT tight-spin.
	waitFor(t, func() bool { return strings.Contains(buf.String(), "admin unsuspend @svc") }, time.Second,
		"suspended log with unsuspend fix")
	time.Sleep(150 * time.Millisecond)
	if sa.Ready() {
		t.Fatal("should not be Ready while suspended")
	}
	_, authorizes, _ := f.counts()
	if authorizes > 15 {
		t.Errorf("authorize attempts = %d in ~150ms — backoff not limiting the storm", authorizes)
	}
	// CreateUser must not repeat across the backoff retries.
	if creates, _, _ := f.counts(); creates != 1 {
		t.Errorf("CreateUser called %d times while suspended, want 1", creates)
	}

	// Operator unsuspends: the loop self-heals with no restart.
	f.setAuth(200, "")
	waitFor(t, sa.Ready, time.Second, "self-heal to Ready after unsuspend")
}

func TestServiceAccountEmail(t *testing.T) {
	sa := NewServiceAccount(Deps{}, "@chat", "p")
	if sa.email() != "chat@juice-ui.local" {
		t.Errorf("email = %q", sa.email())
	}
}
