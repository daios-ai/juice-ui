package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestUnreachableKernelIsUnavailable checks a transport failure (no HTTP response)
// is typed KindUnavailable, so callers can say "kernel unreachable" instead of a
// generic or credential error.
func TestUnreachableKernelIsUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // the port is now closed → connection refused
	_, err := New(url, nil).Health(context.Background())
	ke, ok := AsKernelError(err)
	if !ok || ke.Kind != KindUnavailable {
		t.Fatalf("unreachable kernel: got %v, want KindUnavailable", err)
	}
	if !errors.Is(err, ErrUnavailable) {
		t.Error("errors.Is(err, ErrUnavailable) must hold")
	}
}

func TestClassifyByStatus(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   Kind
	}{
		{401, `{"error":"missing bearer token"}`, KindUnauthenticated},
		{402, `{"error":"insufficient user balance"}`, KindInsufficientFunds},
		{403, `{"error":"forbidden"}`, KindUnauthorized},
		{404, `{"error":"action not found"}`, KindNotFound},
		{409, `{"error":"lookup requires an embedding service"}`, KindInvalidState},
		{422, `{"error":"name is required"}`, KindInvalidInput},
		{400, `{"error":"bad"}`, KindInvalidInput},
		{429, `{"error":"rate limit exceeded"}`, KindRateLimited},
		{500, `{"error":"boom"}`, KindExecutionFailed},
		{504, `{"error":"timeout"}`, KindTimeout},
		{418, `{"error":"teapot"}`, KindInternal},
	}
	for _, c := range cases {
		err := classify(c.status, []byte(c.body))
		ke, ok := AsKernelError(err)
		if !ok {
			t.Fatalf("status %d: not a KernelError: %v", c.status, err)
		}
		if ke.Kind != c.want {
			t.Errorf("status %d: kind=%v want %v", c.status, ke.Kind, c.want)
		}
	}
}

func TestClassifyGrantRequired(t *testing.T) {
	// A 403 with code=grant_required is a distinct kind carrying the action ref;
	// a plain 403 stays Unauthorized.
	err := classify(403, []byte(`{"error":"grant required","code":"grant_required","meta":{"action":"sys/gmail-send"}}`))
	ke, _ := AsKernelError(err)
	if ke.Kind != KindGrantRequired {
		t.Errorf("kind = %v want KindGrantRequired", ke.Kind)
	}
	if ke.GrantAction != "sys/gmail-send" {
		t.Errorf("GrantAction = %q want sys/gmail-send", ke.GrantAction)
	}
	if plain, _ := AsKernelError(classify(403, []byte(`{"error":"nope"}`))); plain.Kind != KindUnauthorized {
		t.Errorf("plain 403 kind = %v want KindUnauthorized", plain.Kind)
	}
}

// TestClassifyPeerErrors checks the remote-proxy run failures get their own kinds
// (so 402 peer_unfunded doesn't read as the caller's "insufficient funds").
func TestClassifyPeerErrors(t *testing.T) {
	unf, _ := AsKernelError(classify(402, []byte(`{"error":"peer unfunded","code":"peer_unfunded","meta":{"peer":"kernelb"}}`)))
	if unf.Kind != KindPeerUnfunded {
		t.Errorf("kind = %v, want KindPeerUnfunded (not InsufficientFunds)", unf.Kind)
	}
	if msg := unf.UserMessage(); !strings.Contains(msg, "operator") {
		t.Errorf("unfunded message should point at the operator top-up: %q", msg)
	}
	unr, _ := AsKernelError(classify(502, []byte(`{"error":"peer unreachable","code":"peer_unreachable"}`)))
	if unr.Kind != KindPeerUnreachable {
		t.Errorf("kind = %v, want KindPeerUnreachable", unr.Kind)
	}
	if msg := unr.UserMessage(); !strings.Contains(msg, "not spent") {
		t.Errorf("unreachable message should reassure funds not spent: %q", msg)
	}
	// A plain 402 (the caller's own funds) stays InsufficientFunds.
	if plain, _ := AsKernelError(classify(402, []byte(`{"error":"broke"}`))); plain.Kind != KindInsufficientFunds {
		t.Errorf("plain 402 kind = %v, want KindInsufficientFunds", plain.Kind)
	}
}

func TestClassify2xxIsNil(t *testing.T) {
	for _, s := range []int{200, 201, 204} {
		if err := classify(s, nil); err != nil {
			t.Errorf("classify(%d) = %v, want nil", s, err)
		}
	}
}

func TestClassifyExtractsField(t *testing.T) {
	err := classify(422, []byte(`{"error":"field #.query: required field missing"}`))
	ke, _ := AsKernelError(err)
	if ke.Field != "query" {
		t.Errorf("Field = %q want query", ke.Field)
	}
}

func TestSentinelMatching(t *testing.T) {
	err := classify(404, []byte(`{"error":"action not found"}`))
	if !errors.Is(err, ErrNotFound) {
		t.Error("expected errors.Is(err, ErrNotFound)")
	}
	if errors.Is(err, ErrUnauthenticated) {
		t.Error("did not expect match against ErrUnauthenticated")
	}
}

func TestUserMessageNeverLeaksStatus(t *testing.T) {
	for _, s := range []int{401, 402, 403, 404, 409, 422, 429, 500, 504} {
		err := classify(s, []byte(`{"error":"raw detail"}`))
		ke, _ := AsKernelError(err)
		msg := ke.UserMessage()
		if msg == "" {
			t.Errorf("status %d: empty user message", s)
		}
		if strings.Contains(msg, "500") || strings.Contains(msg, "404") || strings.Contains(msg, "status") {
			t.Errorf("status %d: user message leaks status: %q", s, msg)
		}
	}
}

func TestUserMessageSurfacesKernelDetailForInput(t *testing.T) {
	err := classify(422, []byte(`{"error":"name is required"}`))
	ke, _ := AsKernelError(err)
	if !strings.Contains(ke.UserMessage(), "name is required") {
		t.Errorf("input message should surface kernel detail, got %q", ke.UserMessage())
	}
}

// A parked call is NOT a refund. The kernel answers 504 with the process still
// holding the funds until the peer's signed answer settles it, and no age
// expires it (verified live on d59742b: the meta names the process and when it
// started, and no refund date), so the message must not claim money came back.
func TestTimeoutMessageNeverClaimsARefund(t *testing.T) {
	body := []byte(`{"code":"timeout","error":"remote receipt pending; result confirmation is pending — funds remain reserved on process P1 and the call retries automatically","meta":{"pending_since":"2026-08-18T17:01:54Z","process_id":"P1"}}`)
	err := classify(504, body)
	ke, _ := AsKernelError(err)
	if ke.Kind != KindTimeout {
		t.Fatalf("Kind = %v want KindTimeout", ke.Kind)
	}
	msg := ke.UserMessage()
	for _, banned := range []string{"refund", "refunded", "returned"} {
		if strings.Contains(strings.ToLower(msg), banned) {
			t.Errorf("a parked call must not claim a refund, got %q", msg)
		}
	}
	if ke.MetaString("process_id") != "P1" {
		t.Errorf("process_id = %q", ke.MetaString("process_id"))
	}
}

// Contract drift answers the same class as a genuine failure but marks itself.
// The marker is the ONLY thing that makes a retry safe to offer, so it decides
// the sentence (verified live: nothing charged, immediate retry succeeded).
func TestRefreshableIsNotAFailure(t *testing.T) {
	body := []byte(`{"code":"execution_failed","error":"the provider updated this action; nothing was charged — re-run to refresh","meta":{"retry":"refresh"}}`)
	ke, _ := AsKernelError(classify(500, body))
	if !ke.IsRefreshable() {
		t.Fatal("meta.retry=refresh must mark the error refreshable")
	}
	if msg := ke.UserMessage(); strings.Contains(strings.ToLower(msg), "refunded") {
		t.Errorf("nothing was charged, so nothing was refunded: %q", msg)
	}
	// An unmarked execution_failed keeps the failure sentence and offers no retry.
	plain, _ := AsKernelError(classify(500, []byte(`{"code":"execution_failed","error":"remote call failed"}`)))
	if plain.IsRefreshable() {
		t.Error("an unmarked execution_failed must never be treated as retryable")
	}
	if !strings.Contains(plain.UserMessage(), "refunded") {
		t.Errorf("a genuine failure states the refund: %q", plain.UserMessage())
	}
}

// A peer's refusal arrives as 403 but is an operator relationship fact, not the
// user's own permissions (verified live: meta.peer names the kernel).
func TestPeerRefusalIsNotTheUsersPermission(t *testing.T) {
	ke, _ := AsKernelError(classify(403, []byte(`{"code":"unauthorized","error":"peer bravo refused this call","meta":{"peer":"bravo"}}`)))
	if ke.Peer() != "bravo" {
		t.Fatalf("Peer() = %q want bravo", ke.Peer())
	}
	msg := ke.UserMessage()
	if !strings.Contains(msg, "bravo") {
		t.Errorf("the refusing kernel must be named: %q", msg)
	}
	if strings.Contains(msg, "You don't have permission") {
		t.Errorf("a peer refusal must not read as the user's own permission: %q", msg)
	}
	// A plain 403 keeps the ordinary message.
	plain, _ := AsKernelError(classify(403, []byte(`{"error":"not yours"}`)))
	if !strings.Contains(plain.UserMessage(), "permission") {
		t.Errorf("plain 403 keeps the not-permitted message: %q", plain.UserMessage())
	}
}

// A peer name arrives as whichever name the failing path had: a petname where
// it knew one, the public key otherwise (both observed live on v0.12.24). A key
// is 43 unreadable characters, so it is abbreviated before it reaches prose —
// while the raw value stays available for logic.
func TestPeerDisplayAbbreviatesAKeyButNotAPetname(t *testing.T) {
	key := "1MutJi6KXZIWlxJMiIKsu4gR6-R4ACoEJwP7sNT_mq4" // 43 chars, as the kernel sends
	if len(key) != KeyLen {
		t.Fatalf("fixture is not a key-shaped name: %d chars", len(key))
	}
	ke, _ := AsKernelError(classify(502, []byte(`{"code":"peer_unreachable","error":"peer `+key+` is unreachable","meta":{"peer":"`+key+`"}}`)))
	if ke.Peer() != key {
		t.Errorf("Peer() must stay raw for logic, got %q", ke.Peer())
	}
	if got := ke.PeerDisplay(); got != "1MutJi6KXZ…_mq4" {
		t.Errorf("PeerDisplay() = %q, want the abbreviated key", got)
	}
	if msg := ke.UserMessage(); strings.Contains(msg, key) {
		t.Errorf("a 43-char key must never reach a sentence whole: %q", msg)
	}
	// A petname is already what a person recognises the kernel by.
	named, _ := AsKernelError(classify(403, []byte(`{"code":"unauthorized","error":"peer bravo refused this call","meta":{"peer":"bravo"}}`)))
	if named.PeerDisplay() != "bravo" {
		t.Errorf("a petname passes through whole, got %q", named.PeerDisplay())
	}
	if !strings.Contains(named.UserMessage(), "bravo") {
		t.Errorf("the sentence names the peer: %q", named.UserMessage())
	}
	// No peer, no abbreviation, no empty name in a sentence.
	if (&KernelError{}).PeerDisplay() != "" {
		t.Error("an absent peer stays absent")
	}
}

// The kernel relays one fact of an HTTP action's refusal, its status, in a
// fixed message; anything else is no status.
func TestUpstreamStatus(t *testing.T) {
	for _, c := range []struct {
		kind Kind
		msg  string
		want int
	}{
		{KindExecutionFailed, "upstream returned status 404", 404},
		{KindExecutionFailed, "upstream returned status 409", 409},
		{KindExecutionFailed, "upstream returned status 40", 0},
		{KindExecutionFailed, "upstream returned status 404, and more", 0},
		{KindExecutionFailed, "model did not select an action", 0},
		{KindUnauthorized, "upstream returned status 404", 0},
	} {
		if got := (&KernelError{Kind: c.kind, KernelMessage: c.msg}).UpstreamStatus(); got != c.want {
			t.Errorf("%v %q = %d, want %d", c.kind, c.msg, got, c.want)
		}
	}
}

// A failure on another kernel is known by the kernel's own words, as an
// endpoint that cannot be reached is; it carries no upstream status.
func TestRemoteFailed(t *testing.T) {
	remote := &KernelError{Kind: KindExecutionFailed, KernelMessage: "remote call failed"}
	if !remote.RemoteFailed() || remote.UpstreamStatus() != 0 || remote.UpstreamDown() {
		t.Error("a failure on another kernel")
	}
	if (&KernelError{Kind: KindExecutionFailed, KernelMessage: "upstream returned status 404"}).RemoteFailed() ||
		(&KernelError{Kind: KindPeerUnreachable, KernelMessage: "remote call failed"}).RemoteFailed() {
		t.Error("anything else")
	}
}

func TestUpstreamDown(t *testing.T) {
	if !(&KernelError{Kind: KindExecutionFailed, KernelMessage: "upstream request failed"}).UpstreamDown() {
		t.Error("an endpoint that cannot be reached")
	}
	if (&KernelError{Kind: KindExecutionFailed, KernelMessage: "upstream returned status 500"}).UpstreamDown() ||
		(&KernelError{Kind: KindUnavailable, KernelMessage: "upstream request failed"}).UpstreamDown() {
		t.Error("anything else")
	}
}
