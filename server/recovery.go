package server

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"

	bip39 "github.com/tyler-smith/go-bip39"
)

// Seed-phrase account recovery (kernel §12). The BFF is the *client* here: it
// generates the 12-word BIP-39 mnemonic locally at signup, sends the kernel only
// the derived public key, and shows the phrase once. A lost password is reset by
// signing a server nonce with the phrase-derived key. The mnemonic never reaches
// the kernel.
//
// Derivation is pinned by API.md and must match the CLI byte-for-byte, else a
// phrase issued here cannot recover via the CLI and vice versa:
//
//	seed  = BIP-39(phrase)          // empty passphrase
//	priv  = Ed25519 from seed[:32]
//	pub   = base64url(priv.public)  // == recovery_public_key
//	sig   = base64url(Ed25519-sign(priv, "juice/v1/" + network + "/recovery\n" ‖ JCS({"recovery_challenge": nonce})))
//
// network is the kernel's network fingerprint (/health network_fingerprint):
// every Juice payload is signed under that prefix and one domain per kind
// (kernel requirements.md §5, "Every Juice payload signs …"), so a signature
// verifies on one network and in one domain only. API.md's bare-JCS form is
// refused by the kernel ("recovery signature is invalid", v0.15.7).
//
// base64url is unpadded (RawURLEncoding), matching the kernel's key format.

// recoveryChallenge is the exact object signed during recovery. A single-key
// object with a string value is already JCS-canonical when marshalled by
// encoding/json (no key ordering to resolve, compact by default), so json.Marshal
// of this struct is the canonical payload.
type recoveryChallenge struct {
	RecoveryChallenge string `json:"recovery_challenge"`
}

// generateMnemonic returns a fresh 12-word BIP-39 mnemonic (128-bit entropy).
func generateMnemonic() (string, error) {
	entropy, err := bip39.NewEntropy(128)
	if err != nil {
		return "", fmt.Errorf("recovery entropy: %w", err)
	}
	m, err := bip39.NewMnemonic(entropy)
	if err != nil {
		return "", fmt.Errorf("recovery mnemonic: %w", err)
	}
	return m, nil
}

// recoveryKey derives the Ed25519 recovery key from a mnemonic. It rejects an
// invalid phrase (wrong checksum / not in the word list) so a mistyped recovery
// phrase fails loudly rather than deriving a silently-wrong key.
func recoveryKey(mnemonic string) (ed25519.PrivateKey, error) {
	if !bip39.IsMnemonicValid(mnemonic) {
		return nil, fmt.Errorf("invalid recovery phrase")
	}
	seed := bip39.NewSeed(mnemonic, "") // empty passphrase, per API.md
	return ed25519.NewKeyFromSeed(seed[:32]), nil
}

// recoveryPubKey returns the base64url recovery_public_key to enroll at signup.
func recoveryPubKey(mnemonic string) (string, error) {
	priv, err := recoveryKey(mnemonic)
	if err != nil {
		return "", err
	}
	pub := priv.Public().(ed25519.PublicKey)
	return base64.RawURLEncoding.EncodeToString(pub), nil
}

// signRecoveryChallenge signs the server nonce with the phrase-derived key, in
// the recovery domain of the kernel's network.
func signRecoveryChallenge(mnemonic, network, nonce string) (string, error) {
	priv, err := recoveryKey(mnemonic)
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(recoveryChallenge{RecoveryChallenge: nonce})
	if err != nil {
		return "", fmt.Errorf("recovery challenge: %w", err)
	}
	sig := ed25519.Sign(priv, append([]byte("juice/v1/"+network+"/recovery\n"), payload...))
	return base64.RawURLEncoding.EncodeToString(sig), nil
}
