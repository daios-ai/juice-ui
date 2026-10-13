// Package wallet holds what decides money on the wallet page: the registration
// message, the proof of which chain the kernel's world is on, the transfer a
// browser wallet is asked to send, and the only two kernel calls that move or
// point money — registering the address withdrawals go to, and withdrawing.
// Reads and rendering stay in the shell; a drift test confines the two calls here.
package wallet

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/daios-ai/juice-ui/client"
)

// Message is the text the kernel verifies a registration signature against
// (EIP-191; kernel manual, measured on v0.16.0): four lines, no trailing
// newline, the address exactly as sent in the request.
func Message(kernelKey, userID, address string) string {
	return "juice address registration\nkernel: " + kernelKey + "\nuser: " + userID + "\naddress: " + address
}

// chains bounds the search in ChainOf: the worlds the kernel ships, and anvil's
// default for the local rig. A row is accepted only when the fingerprint proves it.
var chains = []int64{137, 42161, 421614, 31337}

// ChainOf proves the chain id of the kernel's world. /health serves no chain id,
// but its network fingerprint is sha256(JCS({chain_id, name, rail, token})), so
// recomputing it for a candidate is a proof. The struct's keys are already in
// JCS order, and no value can hold a character JSON escapes.
func ChainOf(h client.Health) (int64, bool) {
	if h.BlockchainAddress == "" {
		return 0, false
	}
	for _, id := range chains {
		b, _ := json.Marshal(struct {
			ChainID int64  `json:"chain_id"`
			Name    string `json:"name"`
			Rail    string `json:"rail"`
			Token   string `json:"token"`
		}{id, h.Network, "evm", h.Token})
		if sum := sha256.Sum256(b); hex.EncodeToString(sum[:]) == h.NetworkFingerprint {
			return id, true
		}
	}
	return 0, false
}

// TransferData is the calldata of ERC-20 transfer(to, amount).
func TransferData(to string, amount int64) string {
	return fmt.Sprintf("0xa9059cbb%064s%064x", strings.TrimPrefix(strings.ToLower(to), "0x"), amount)
}

// Tx is the transfer a browser wallet is asked to send: the token's transfer of
// amount from the registered address to the kernel's vault.
type Tx struct {
	From    string `json:"from"`
	To      string `json:"to"`
	Data    string `json:"data"`
	ChainID string `json:"chainId"`
}

// ErrNoFunding refuses a transfer the kernel would not credit to the caller.
var ErrNoFunding = errors.New("adding funds needs a registered address on a proven chain")

// FundTx builds the transfer from what the kernel says now: h and me must be
// fresh reads, so a registration changed since the review is refused.
func FundTx(h client.Health, me client.User, amount int64) (Tx, error) {
	id, ok := ChainOf(h)
	if !ok || me.BlockchainAddress == "" || amount <= 0 {
		return Tx{}, ErrNoFunding
	}
	return Tx{me.BlockchainAddress, h.Token, TransferData(h.BlockchainAddress, amount), "0x" + strconv.FormatInt(id, 16)}, nil
}

// Register proves control of the address withdrawals go to (kernel D23).
func Register(ctx context.Context, c *client.Client, tok, address, signature, password string) (client.AddressResult, error) {
	if address == "" || signature == "" || password == "" {
		return client.AddressResult{}, errors.New("An address, its signature and your password are all required.")
	}
	return c.RegisterAddress(ctx, tok, address, signature, password)
}

var uuidShape = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// Withdraw pays amount out to the registered address. id is the row's own,
// minted by NewWithdrawalID at review, so a replay returns that row.
func Withdraw(ctx context.Context, c *client.Client, tok, id string, amount int64, reason string) (client.Withdrawal, error) {
	if amount <= 0 || !uuidShape.MatchString(id) {
		return client.Withdrawal{}, errors.New("Enter an amount to withdraw.")
	}
	return c.Withdraw(ctx, tok, id, amount, reason)
}

// NewWithdrawalID mints the UUID v4 a withdrawal is keyed by.
func NewWithdrawalID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

//go:embed wallet.mjs
var script []byte

// Script serves wallet.mjs, the browser half of this package.
var Script = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	_, _ = w.Write(script)
})
