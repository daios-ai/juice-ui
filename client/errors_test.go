package client

import (
	"errors"
	"strings"
	"testing"
)

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
	err := classify(403, []byte(`{"error":"grant required","code":"grant_required","meta":{"action":"@sys/gmail-send"}}`))
	ke, _ := AsKernelError(err)
	if ke.Kind != KindGrantRequired {
		t.Errorf("kind = %v want KindGrantRequired", ke.Kind)
	}
	if ke.GrantAction != "@sys/gmail-send" {
		t.Errorf("GrantAction = %q want @sys/gmail-send", ke.GrantAction)
	}
	if plain, _ := AsKernelError(classify(403, []byte(`{"error":"nope"}`))); plain.Kind != KindUnauthorized {
		t.Errorf("plain 403 kind = %v want KindUnauthorized", plain.Kind)
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
