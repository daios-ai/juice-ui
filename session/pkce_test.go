package session

import (
	"crypto/sha256"
	"encoding/base64"
	"testing"
)

func TestNewVerifierUniqueAndCharset(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		v, err := NewVerifier()
		if err != nil {
			t.Fatal(err)
		}
		if len(v) != 43 { // base64url(32 bytes) without padding
			t.Errorf("verifier length = %d want 43 (%q)", len(v), v)
		}
		if _, err := base64.RawURLEncoding.DecodeString(v); err != nil {
			t.Errorf("verifier not base64url: %v", err)
		}
		if seen[v] {
			t.Errorf("duplicate verifier %q", v)
		}
		seen[v] = true
	}
}

func TestChallengeMatchesS256(t *testing.T) {
	v := "uEMiA94hZEgxzFUuWZ9ld6zsdjvMBhCrCVkON3DTbns"
	sum := sha256.Sum256([]byte(v))
	want := base64.RawURLEncoding.EncodeToString(sum[:])
	if got := Challenge(v); got != want {
		t.Errorf("Challenge = %q want %q", got, want)
	}
	// No padding in the output.
	if got := Challenge(v); got[len(got)-1] == '=' {
		t.Errorf("challenge should not be padded: %q", got)
	}
}
