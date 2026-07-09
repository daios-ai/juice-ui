package apps

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strings"
)

// CallerKeyHeader is the request header a plugin's delegated_bearer actions carry
// the per-user token in, and the header CallerIdentity.Authenticate reads. One
// convention across plugins is fine: a token only verifies under the signing key
// that minted it.
const CallerKeyHeader = "X-Juice-Caller-Key"

// CallerIdentity mints and verifies stateless, deterministic per-user tokens for a
// plugin's service — the *inbound* mirror of ServiceAccount (a plugin's *outbound*
// identity). The plugin mints a token for a signed-in user and connects it as that
// user's delegated_bearer grant; the kernel injects it on the user's calls; the
// service recovers the handle by verifying the token — with no stored state, so
// there is nothing to desync with the grant.
//
// token(handle) = base64url(handle) "." base64url(HMAC-SHA256(signingKey, handle))
//
// It is deterministic (same key+handle → same token, so it survives any store
// reset) and unforgeable (only a holder of the signing key can mint a valid token;
// handles are public, so encoding one leaks nothing).
type CallerIdentity struct {
	key []byte
}

// NewCallerIdentity returns an identity that signs tokens with signingKey.
func NewCallerIdentity(signingKey string) *CallerIdentity {
	return &CallerIdentity{key: []byte(signingKey)}
}

var enc = base64.RawURLEncoding

// Token mints the deterministic token for handle.
func (c *CallerIdentity) Token(handle string) string {
	return enc.EncodeToString([]byte(handle)) + "." + enc.EncodeToString(c.mac(handle))
}

// Verify recovers the handle from a token, or reports ok=false if the token is
// malformed or its signature does not match.
func (c *CallerIdentity) Verify(token string) (string, bool) {
	h, sig, found := strings.Cut(token, ".")
	if !found {
		return "", false
	}
	hb, err := enc.DecodeString(h)
	if err != nil {
		return "", false
	}
	sb, err := enc.DecodeString(sig)
	if err != nil {
		return "", false
	}
	if !hmac.Equal(sb, c.mac(string(hb))) {
		return "", false
	}
	return string(hb), true
}

// Authenticate identifies the caller of an incoming request from the injected
// token header — the auth hook a service is handed (e.g. daemon.New(..., auth)).
func (c *CallerIdentity) Authenticate(r *http.Request) (string, bool) {
	return c.Verify(r.Header.Get(CallerKeyHeader))
}

// mac is HMAC-SHA256(key, handle).
func (c *CallerIdentity) mac(handle string) []byte {
	m := hmac.New(sha256.New, c.key)
	m.Write([]byte(handle))
	return m.Sum(nil)
}
