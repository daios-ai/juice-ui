package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/daios-ai/juice-ui/client"
)

func TestNewRendererParsesAllTemplates(t *testing.T) {
	if _, err := NewRenderer(); err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
}

func TestMoneyFormatting(t *testing.T) {
	cases := map[int64]string{0: "0", 5: "5", 100: "100", 1000: "1,000", 1234567: "1,234,567", -2500: "-2,500"}
	for in, want := range cases {
		if got := money(in); got != want {
			t.Errorf("money(%d) = %q want %q", in, got, want)
		}
	}
}

func TestPrettyJSONNoDoubleEncode(t *testing.T) {
	out := prettyJSON(json.RawMessage(`{"a":1}`))
	if !strings.Contains(out, "\"a\": 1") {
		t.Errorf("prettyJSON = %q", out)
	}
	if prettyJSON(nil) != "—" {
		t.Errorf("empty json should render placeholder")
	}
}

func TestJSONEdit(t *testing.T) {
	out := jsonEdit(json.RawMessage(`{"a":1}`))
	if !strings.Contains(out, "\"a\": 1") {
		t.Errorf("jsonEdit = %q, want indented", out)
	}
	// Empty input must yield "" (blank textarea), not prettyJSON's "—".
	if jsonEdit(nil) != "" || jsonEdit(json.RawMessage("  ")) != "" {
		t.Error("empty json must render as empty string for textarea prefill")
	}
}

func TestStatusClassesDistinct(t *testing.T) {
	// The three financial states must map to three distinct classes (R8.7).
	c := map[string]string{
		"success": statusClass("success"),
		"failure": statusClass("failure"),
		"open":    statusClass("open"),
	}
	seen := map[string]bool{}
	for _, v := range c {
		if v == "" || seen[v] {
			t.Errorf("status classes not distinct/non-empty: %v", c)
		}
		seen[v] = true
	}
}

func TestRenderPageStampsThemeNoLeak(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	p := Page{
		Title:     "Account",
		Theme:     "dracula",
		Handle:    "@alice",
		Available: 1000,
		Data:      userView{User: client.User{Handle: "@alice", Available: 1000, Locked: 250}},
	}
	if err := r.Render(rec, 200, "user", p); err != nil {
		t.Fatalf("Render: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `data-theme="dracula"`) {
		t.Error("theme not stamped onto <html>")
	}
	if !strings.Contains(body, "1,000") {
		t.Error("available balance not rendered with money formatting")
	}
	if !strings.Contains(body, "@alice") {
		t.Error("handle not rendered in nav")
	}
}

func TestPct(t *testing.T) {
	cases := []struct {
		n, d int64
		want string
	}{{138, 142, "97"}, {0, 0, "—"}, {0, 10, "0"}, {10, 10, "100"}}
	for _, c := range cases {
		if got := pct(c.n, c.d); got != c.want {
			t.Errorf("pct(%d,%d) = %q want %q", c.n, c.d, got, c.want)
		}
	}
}

func TestRatingPct(t *testing.T) {
	cases := []struct {
		est   float64
		count int64
		want  string
	}{{0.96, 23, "96"}, {0, 0, ""}, {1.0, 1, "100"}, {0.5, 10, "50"}}
	for _, c := range cases {
		if got := ratingPct(c.est, c.count); got != c.want {
			t.Errorf("ratingPct(%v,%d) = %q want %q", c.est, c.count, got, c.want)
		}
	}
}

func TestTxAmount(t *testing.T) {
	// @buyer pays gross, @seller earns net, @sys (platform) earns fee; a viewer
	// occupying several roles sees the sum. gross = net + fee, so the three
	// viewpoints of a settled call sum to zero.
	mk := func(status string, gross, net, fee int64) client.Transaction {
		return client.Transaction{OwnerHandle: "@buyer", TargetHandle: "@seller", Status: status, Gross: gross, Net: net, Fee: fee}
	}
	cases := []struct {
		name, viewer string
		tx           client.Transaction
		want         []string // substrings the rendered cell must contain
	}{
		{"buyer pays gross", "@buyer", mk("success", 100, 90, 10), []string{"-100"}},
		{"seller earns net", "@seller", mk("success", 100, 90, 10), []string{"sig-credited", "+90"}},
		{"platform earns fee", superuserHandle, mk("success", 100, 90, 10), []string{"sig-credited", "+10"}},
		{"no stake shows dash", "@bystander", mk("success", 100, 90, 10), []string{"—"}},
		{"failed refunds buyer", "@buyer", mk("failure", 100, 0, 0), []string{"sig-refunded", "↩", "100"}},
		{"failed non-buyer dash", "@seller", mk("failure", 100, 0, 0), []string{"—"}},
		{"open parks buyer", "@buyer", mk("open", 100, 0, 0), []string{"sig-escrow", "◷", "100"}},
	}
	for _, c := range cases {
		got := string(txAmount(c.tx, c.viewer))
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: txAmount = %q, want substring %q", c.name, got, w)
			}
		}
	}

	// Self-call by a normal user (buyer == seller): nets to −fee.
	self := client.Transaction{OwnerHandle: "@a", TargetHandle: "@a", Status: "success", Gross: 100, Net: 90, Fee: 10}
	if got := string(txAmount(self, "@a")); !strings.Contains(got, "-10") {
		t.Errorf("self-call = %q, want -10 (the fee)", got)
	}
	// @sys as the seller collects net + fee = gross.
	sysSell := client.Transaction{OwnerHandle: "@a", TargetHandle: superuserHandle, Status: "success", Gross: 100, Net: 90, Fee: 10}
	if got := string(txAmount(sysSell, superuserHandle)); !strings.Contains(got, "+100") {
		t.Errorf("@sys-as-seller = %q, want +100 (net+fee)", got)
	}
}

func TestKindBadge(t *testing.T) {
	cases := []struct {
		kind, letter string
		filled       bool
	}{
		{"wasm", "W", false},
		{"native", "N", false},
		{"http", "H", false},
		{"remote_proxy", "R", true},
		{"weird", "W", false}, // unknown → uppercased first rune, not filled
	}
	for _, c := range cases {
		b := kindBadge(c.kind)
		if b.Letter != c.letter || b.Filled != c.filled {
			t.Errorf("kindBadge(%q) = %+v, want letter %q filled %v", c.kind, b, c.letter, c.filled)
		}
		if b.Title == "" {
			t.Errorf("kindBadge(%q) has empty tooltip", c.kind)
		}
	}
	// Only remote is filled (colorless emphasis for the notable kind).
	if kindBadge("wasm").Filled {
		t.Error("only remote_proxy should be a filled chip")
	}
}

// The desktop popover (_menu_account) is identity + prefs + sign-out with NO
// navigation; the mobile drawer (_menu_content) adds the nav links.
func TestAccountMenuHasNoNav(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	data := Page{Handle: "@sys", IsSuperuser: true, Themes: ThemeList()}
	acctRec, fullRec := httptest.NewRecorder(), httptest.NewRecorder()
	if err := r.Fragment(acctRec, 200, "_menu_account", data); err != nil {
		t.Fatal(err)
	}
	if err := r.Fragment(fullRec, 200, "_menu_content", data); err != nil {
		t.Fatal(err)
	}
	acct, full := acctRec.Body.String(), fullRec.Body.String()
	if strings.Contains(acct, `href="/admin"`) || strings.Contains(acct, `href="/user"`) {
		t.Error("desktop popover (_menu_account) must not contain nav links")
	}
	if !strings.Contains(acct, "Sign out") {
		t.Error("_menu_account must have Sign out")
	}
	// The mobile drawer keeps the nav (it replaces the sidebar).
	if !strings.Contains(full, `href="/admin"`) || !strings.Contains(full, `href="/admin/peers"`) {
		t.Error("mobile drawer (_menu_content) must carry Admin + Peers nav")
	}
}

func TestKeyFingerprint(t *testing.T) {
	long := "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789abcd"
	if got, want := keyFingerprint(long), "ABCDEFGHIJ…abcd"; got != want {
		t.Errorf("keyFingerprint(long) = %q want %q", got, want)
	}
	// Short keys are returned as-is (nothing to shorten).
	if got := keyFingerprint("short"); got != "short" {
		t.Errorf("keyFingerprint(short) = %q want %q", got, "short")
	}
}

func TestRenderFragment(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	out := Outcome{Class: Success, Result: json.RawMessage(`{"ok":true}`), TxID: "tx1"}
	if err := r.Fragment(rec, 200, "_call_outcome", out); err != nil {
		t.Fatalf("Fragment: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Success") || !strings.Contains(body, "/transactions/tx1") {
		t.Errorf("fragment render missing content: %s", body)
	}
}
