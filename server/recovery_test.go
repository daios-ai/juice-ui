package server

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	bip39 "github.com/tyler-smith/go-bip39"
)

func TestGenerateMnemonicIsTwelveValidWords(t *testing.T) {
	m, err := generateMnemonic()
	if err != nil {
		t.Fatal(err)
	}
	if n := len(strings.Fields(m)); n != 12 {
		t.Errorf("mnemonic has %d words, want 12: %q", n, m)
	}
	if !bip39.IsMnemonicValid(m) {
		t.Errorf("generated mnemonic is not BIP-39 valid: %q", m)
	}
}

func TestRecoveryPubKeyDeterministicAndValid(t *testing.T) {
	const m = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"
	a, err := recoveryPubKey(m)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := recoveryPubKey(m)
	if a != b {
		t.Errorf("derivation not deterministic: %q vs %q", a, b)
	}
	raw, err := base64.RawURLEncoding.DecodeString(a)
	if err != nil {
		t.Errorf("pub key is not unpadded base64url: %v", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		t.Errorf("pub key is %d bytes, want %d", len(raw), ed25519.PublicKeySize)
	}
}

// TestRecoveryDerivationVector pins the derivation for a fixed phrase so an
// accidental change to the scheme (passphrase, seed slice, encoding) is caught.
// The value matches the kernel's scheme (seed[:32] → Ed25519, unpadded base64url,
// per API.md); the live recovery round-trip verifies interop with the kernel.
func TestRecoveryDerivationVector(t *testing.T) {
	const m = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"
	const want = "xXheGGW3CJOK_4Fh1XMAZJZmOxqhCDTjltxWaGmixmo"
	got, err := recoveryPubKey(m)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("derivation vector drift: got %q, want %q", got, want)
	}
}

// TestSignChallengeVerifiesUnderKey proves the signature the BFF produces verifies
// against the enrolled recovery key over the canonical challenge — the property the
// kernel checks at recover/complete.
func TestSignChallengeVerifiesUnderKey(t *testing.T) {
	const m = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"
	const nonce = "nonce-12345"
	sigB64, err := signRecoveryChallenge(m, nonce)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil {
		t.Fatalf("signature not unpadded base64url: %v", err)
	}
	pubB64, _ := recoveryPubKey(m)
	pub, _ := base64.RawURLEncoding.DecodeString(pubB64)
	payload, _ := json.Marshal(recoveryChallenge{RecoveryChallenge: nonce})
	if string(payload) != `{"recovery_challenge":"nonce-12345"}` {
		t.Errorf("challenge payload not canonical: %s", payload)
	}
	if !ed25519.Verify(pub, payload, sig) {
		t.Error("signature does not verify against the derived recovery key")
	}
}

func TestRecoveryRejectsInvalidPhrase(t *testing.T) {
	if _, err := recoveryPubKey("not a real bip39 mnemonic at all"); err == nil {
		t.Error("an invalid phrase must be rejected, not silently derived")
	}
	if _, err := signRecoveryChallenge("still not valid", "n"); err == nil {
		t.Error("signing with an invalid phrase must fail")
	}
}
