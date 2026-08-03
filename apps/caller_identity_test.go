package apps

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCallerIdentityRoundTrips(t *testing.T) {
	id := NewCallerIdentity("s3cret-signing-key")
	for _, h := range []string{"alice", "sys", "@a/weird.handle", "bob"} {
		tok := id.Token(h)
		if tok == "" {
			t.Fatalf("empty token for %q", h)
		}
		got, ok := id.Token(h), tok
		if got != ok {
			t.Errorf("Token not deterministic for %q", h) // same input -> same token
		}
		back, valid := id.Verify(tok)
		if !valid || back != h {
			t.Errorf("Verify(%q) = %q, %v; want %q, true", tok, back, valid, h)
		}
	}
}

func TestCallerIdentityRejectsForgeryAndTamper(t *testing.T) {
	id := NewCallerIdentity("key-A")
	other := NewCallerIdentity("key-B")
	tok := id.Token("alice")

	// A token minted under a different key must not verify.
	if _, ok := other.Verify(tok); ok {
		t.Error("token from a different signing key verified")
	}
	// A token whose handle is swapped but signature kept must not verify.
	forged := NewCallerIdentity("key-A")
	tampered := enc.EncodeToString([]byte("@mallory")) + "." + tok[len(enc.EncodeToString([]byte("alice")))+1:]
	if _, ok := forged.Verify(tampered); ok {
		t.Error("tampered handle verified")
	}
	// Malformed tokens.
	for _, bad := range []string{"", "nodot", "not-base64.also-not", "alice.sig"} {
		if _, ok := id.Verify(bad); ok {
			t.Errorf("malformed token %q verified", bad)
		}
	}
}

func TestCallerIdentityAuthenticateReadsHeader(t *testing.T) {
	id := NewCallerIdentity("k")
	r := httptest.NewRequest(http.MethodPost, "/x", nil)
	r.Header.Set(CallerKeyHeader, id.Token("carol"))
	if h, ok := id.Authenticate(r); !ok || h != "carol" {
		t.Errorf("Authenticate = %q, %v; want carol, true", h, ok)
	}
	// No header -> not authenticated.
	if _, ok := id.Authenticate(httptest.NewRequest(http.MethodPost, "/x", nil)); ok {
		t.Error("authenticated with no header")
	}
}
