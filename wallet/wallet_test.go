package wallet

import (
	"context"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/daios-ai/juice-ui/client"
)

func TestMessageIsTheKernelsFourLines(t *testing.T) {
	got := Message("K", "U", "0xAbC")
	if got != "juice address registration\nkernel: K\nuser: U\naddress: 0xAbC" {
		t.Errorf("message = %q", got)
	}
}

// Banners measured live on v0.16.0 (anvil rig, Pedro's play kernel), and the
// polygon one predicted by the same rule.
var (
	anvil   = client.Health{Network: "anvil", Token: "0x5fbdb2315678afecb367f032d93f642f64180aa3", BlockchainAddress: "0xd1572fc3bd9ebbab2dfd173504c6e7f82bad21ca", NetworkFingerprint: "7d16b1469f57f12eb983417f64de442bdb7b06909e26a65daa8bcd311bb80d9b"}
	polygon = client.Health{Network: "polygon", Token: "0xc2132d05d31c914a87c6611c10748aeb04b58e8f", BlockchainAddress: "0xvault", NetworkFingerprint: "745bdb1ebcadda5d669bdac5638ba13f1102674f2decf89f137b5ba08b1b8666"}
	play    = client.Health{Network: "play", NetworkFingerprint: "baed18ae3f0c2b63f04f593d113a8af947eea60410d2748771d4ad38df9ecf96"}
)

func TestChainOfProvesTheChainByTheFingerprint(t *testing.T) {
	if id, ok := ChainOf(anvil); !ok || id != 31337 {
		t.Errorf("anvil = %d %v", id, ok)
	}
	if id, ok := ChainOf(polygon); !ok || id != 137 {
		t.Errorf("polygon = %d %v", id, ok)
	}
	if _, ok := ChainOf(play); ok {
		t.Error("a world without addresses has no chain")
	}
	forged := anvil
	forged.NetworkFingerprint = "8" + anvil.NetworkFingerprint[1:]
	if _, ok := ChainOf(forged); ok {
		t.Error("a fingerprint that does not match proves nothing")
	}
}

func TestNativeNamesTheFeeCoin(t *testing.T) {
	if Native(137) != "POL" || Native(31337) != "ETH" || Native(1) != "" {
		t.Errorf("natives = %q %q %q", Native(137), Native(31337), Native(1))
	}
}

func TestTransferDataIsERC20Transfer(t *testing.T) {
	// cast calldata "transfer(address,uint256)" 0xd1572fc3bd9ebbab2dfd173504c6e7f82bad21ca 5000000
	want := "0xa9059cbb000000000000000000000000d1572fc3bd9ebbab2dfd173504c6e7f82bad21ca00000000000000000000000000000000000000000000000000000000004c4b40"
	if got := TransferData("0xD1572FC3Bd9ebBAb2DfD173504C6e7F82BAD21ca", 5000000); got != want {
		t.Errorf("calldata = %s", got)
	}
}

func TestFundTxNeedsAProvenChainAndARegisteredAddress(t *testing.T) {
	me := client.User{BlockchainAddress: "0xme"}
	tx, err := FundTx(anvil, me, 5000000)
	if err != nil || tx != (Tx{"0xme", anvil.Token, TransferData(anvil.BlockchainAddress, 5000000), "0x7a69"}) {
		t.Fatalf("tx = %+v, %v", tx, err)
	}
	for name, c := range map[string]struct {
		h  client.Health
		me client.User
		n  int64
	}{"manual world": {play, me, 1}, "unproven chain": {client.Health{BlockchainAddress: "0xv"}, me, 1}, "unregistered": {anvil, client.User{}, 1}, "zero": {anvil, me, 0}} {
		if _, err := FundTx(c.h, c.me, c.n); err != ErrNoFunding {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

// kernel records the one request it receives and answers reply.
func kernel(t *testing.T, reply string) (*client.Client, *string) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = r.Method + " " + r.URL.Path + " " + string(b)
		io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return client.New(srv.URL, nil), &got
}

func TestRegisterSendsTheProofAndRefusesAnIncompleteOne(t *testing.T) {
	c, got := kernel(t, `{"blockchain_address":"0xa","attributed":[{"amount":1}]}`)
	if _, err := Register(context.Background(), c, "tok", "0xA", "", "pw"); err == nil || *got != "" {
		t.Fatal("an incomplete proof must not reach the kernel")
	}
	res, err := Register(context.Background(), c, "tok", "0xA", "0xsig", "pw")
	if err != nil || len(res.Attributed) != 1 {
		t.Fatalf("res = %+v, %v", res, err)
	}
	if *got != `PUT /v1/me/blockchain-address {"blockchain_address":"0xA","current_password":"pw","signature":"0xsig"}` {
		t.Errorf("request = %s", *got)
	}
}

func TestWithdrawSendsAMintedIDAndRefusesAnythingElse(t *testing.T) {
	c, got := kernel(t, `{"id":"x","status":"submitted"}`)
	for _, bad := range []struct {
		id string
		n  int64
	}{{"not-a-uuid", 1}, {NewWithdrawalID(), 0}} {
		if _, err := Withdraw(context.Background(), c, "tok", bad.id, bad.n, ""); err == nil || *got != "" {
			t.Fatalf("%+v must not reach the kernel", bad)
		}
	}
	id := NewWithdrawalID()
	if wd, err := Withdraw(context.Background(), c, "tok", id, 7, "rent"); err != nil || wd.Status != "submitted" {
		t.Fatalf("wd = %+v, %v", wd, err)
	}
	if *got != `POST /v1/withdrawals {"amount":7,"id":"`+id+`","reason":"rent"}` {
		t.Errorf("request = %s", *got)
	}
}

func TestScriptIsServedAsJavaScript(t *testing.T) {
	rec := httptest.NewRecorder()
	Script(rec, httptest.NewRequest("GET", "/wallet/wallet.mjs", nil))
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/javascript") || !strings.Contains(rec.Body.String(), "eth_sendTransaction") {
		t.Errorf("script = %s %q", rec.Header().Get("Content-Type"), rec.Body.String()[:40])
	}
}

// moneyCall is a call to a kernel mutation that moves or points money.
var moneyCall = regexp.MustCompile(`(?i)client\.(Withdraw|RegisterAddress)\(`)

// TestOnlyTheWalletMovesMoney confines the two mutations to this package:
// a change elsewhere cannot move money without failing here.
func TestOnlyTheWalletMovesMoney(t *testing.T) {
	if !moneyCall.MatchString("s.client.Withdraw(ctx") || moneyCall.MatchString("wallet.Withdraw(ctx") {
		t.Fatal("the guard's pattern is wrong")
	}
	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if n := d.Name(); d.IsDir() && (n == "wallet" || n == "client" || n == ".git") {
			return filepath.SkipDir
		}
		if !d.IsDir() && strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			if b, _ := os.ReadFile(path); moneyCall.Match(b) {
				t.Errorf("%s moves money outside package wallet", path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestWalletScript runs wallet.test.mjs, the browser half's tests, under node.
func TestWalletScript(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed; wallet.test.mjs did not run")
	}
	if out, err := exec.Command("node", "--test", "wallet.test.mjs").CombinedOutput(); err != nil {
		t.Fatalf("wallet.test.mjs:\n%s", out)
	}
}
