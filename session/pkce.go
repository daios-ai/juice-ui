// Package session holds the server-side user session: the kernel token pair,
// the in-flight PKCE verifier, and the single-flight token-refresh logic
// (R1.2, R5.2). Tokens never leave the server (R8.3).
package session

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

// NewVerifier returns a PKCE code verifier: 32 random bytes, base64url-encoded
// without padding (43 chars), drawn from crypto/rand (R2.6).
func NewVerifier() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Challenge derives the S256 PKCE code challenge from a verifier:
// base64url(sha256(verifier)) without padding. Verified against the running
// kernel's authorize/exchange handshake.
func Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
