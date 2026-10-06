package server

import (
	"encoding/json"
	"html/template"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/daios-ai/juice-ui/client"
)

// testKernel is a kernel whose /health has been read: the identity a render test
// renders against, without a server to ask.
func testKernel(h client.Health) *KernelIdentity {
	return &KernelIdentity{id: h, fetched: time.Now(), ttl: time.Hour}
}

// playKernel is the kernel a render test renders in: named testnet, serving
// whole credits, as the shipped `play` world does. A test asserting formatted
// money or a local address should say which kernel it is in rather than lean on
// a default.
var playKernel = testKernel(client.Health{Handle: "testnet", Decimals: 0, Symbol: "credits"})

// unknownKernel is a kernel that has not said who it is or what its money is.
var unknownKernel = testKernel(client.Health{})

func TestNewRendererParsesAllTemplates(t *testing.T) {
	if _, err := NewRenderer(playKernel); err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
}

// The kernel publishes one rendering for money and this is a transcription of
// it, so these cases are ITS cases: a two-digit floor with the trailing zeros
// trimmed, no separators, the world's own symbol appended. If the kernel's
// Network.Amount ever moves, this table is what fails.
func TestUnitRendersMoneyTheKernelsWay(t *testing.T) {
	play := Unit{Decimals: 0, Symbol: "credits"}
	usdc := Unit{Decimals: 6, Symbol: "USDC"}
	cases := []struct {
		u    Unit
		in   int64
		want string
	}{
		{play, 0, "0 credits"},
		{play, 133, "133 credits"},
		{play, -2500, "-2500 credits"},
		// No thousands separators: the text pastes straight back into an amount
		// field, and a separator is exactly what the parser refuses.
		{play, 1234567, "1234567 credits"},
		{usdc, 1500000, "1.50 USDC"},
		{usdc, 1000000, "1.00 USDC"},
		{usdc, 133, "0.000133 USDC"},
		{usdc, 1234567890, "1234.56789 USDC"},
		{usdc, -1500000, "-1.50 USDC"},
		// A world that names no symbol renders the bare figure.
		{Unit{Decimals: 0}, 133, "133"},
	}
	for _, c := range cases {
		if got := c.u.Format(c.in); got != c.want {
			t.Errorf("Unit{%d,%q}.Format(%d) = %q want %q", c.u.Decimals, c.u.Symbol, c.in, got, c.want)
		}
	}
}

func TestUnitInputAndStepMatchTheWorld(t *testing.T) {
	// An input's value is the amount without its symbol, so what the field shows
	// is what the parser takes back; the step is the smallest amount that exists.
	if got := (Unit{Decimals: 6, Symbol: "USDC"}).Input(1500000); got != "1.50" {
		t.Errorf("Input = %q want 1.50 (no symbol in a form field)", got)
	}
	if got := (Unit{Decimals: 0, Symbol: "credits"}).Input(133); got != "133" {
		t.Errorf("Input = %q want 133", got)
	}
	if got := (Unit{Decimals: 0}).Step(); got != "1" {
		t.Errorf("Step on a whole-unit world = %q want 1", got)
	}
	if got := (Unit{Decimals: 6}).Step(); got != "0.000001" {
		t.Errorf("Step at six decimals = %q want 0.000001", got)
	}
	// Round trip: what Input renders, parseAmount reads back exactly.
	for _, n := range []int64{0, 133, 1500000, 1234567890} {
		if got, err := parseAmount((Unit{Decimals: 6}).Input(n), 6); err != nil || got != n {
			t.Errorf("round trip %d = %d, %v", n, got, err)
		}
	}
}

// A kernel that has not said what its money is gets no figure and no form value:
// scaling by an assumed unit would put a number in front of someone that they
// did not type and the kernel did not quote (§7).
func TestMoneyRefusesAnUnknownUnit(t *testing.T) {
	r, err := NewRenderer(unknownKernel)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.money(133); got != noAmount {
		t.Errorf("money with no unit = %q, want the em dash", got)
	}
	if got := r.amountInput(133); got != "" {
		t.Errorf("amountInput with no unit = %q, want empty", got)
	}
	if got := r.amountStep(); got != "any" {
		t.Errorf("amountStep with no unit = %q", got)
	}
	// And it renders that way through a real page, not only in isolation.
	rec := httptest.NewRecorder()
	p := Page{Me: "me@testnet", Data: walletView{Available: 500}}
	if err := r.Render(rec, 200, "wallet", p); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rec.Body.String(), noAmount) {
		t.Error("a wallet with no known unit must show no figure")
	}
}

func TestParseAmountRefusesWhatTheWorldCannotExpress(t *testing.T) {
	ok := []struct {
		in       string
		decimals int
		want     int64
	}{
		{"133", 0, 133},
		{"", 0, 0},
		{" 12 ", 0, 12},
		{"0", 0, 0}, // an action may cost nothing
		{"0.000133", 6, 133},
		{"1.5", 6, 1500000},
		{"1.50", 6, 1500000},
		{"1", 6, 1000000},
	}
	for _, c := range ok {
		got, err := parseAmount(c.in, c.decimals)
		if err != nil || got != c.want {
			t.Errorf("parseAmount(%q, %d) = %d, %v want %d", c.in, c.decimals, got, err, c.want)
		}
	}
	bad := []struct {
		in       string
		decimals int
		why      string
	}{
		{"1.5", 0, "no fractional unit on this world"},
		{"0 credits1", 6, "finer than the base unit"},
		{"-1", 0, "negative"},
		{"abc", 0, "not a number"},
		{"1,234", 0, "a separator: nothing renders one, so nothing may type one"},
		{"133 credits", 0, "trailing zeros are still decimals this world lacks"},
		{".5", 6, "no leading digit"},
	}
	for _, c := range bad {
		if v, err := parseAmount(c.in, c.decimals); err == nil {
			t.Errorf("parseAmount(%q, %d) = %d, should have failed: %s", c.in, c.decimals, v, c.why)
		}
	}
}

func TestRefFolder(t *testing.T) {
	// refFolder derives the grant selector that connects a delegated action's
	// whole provider group — the ref up to its LAST slash (the kernel directory),
	// not the degenerate single-action selector. Contrast refOwner (first slash).
	cases := []struct{ in, want string }{
		{"service/a", "service"},
		{"service/msg/message", "service/msg"},
		{"sys/llm/chat", "sys/llm"},
		{"service", "service"}, // no slash — returned unchanged
		{"", ""},
	}
	for _, c := range cases {
		if got := refFolder(c.in); got != c.want {
			t.Errorf("refFolder(%q) = %q, want %q", c.in, got, c.want)
		}
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
	// buyer pays gross, seller earns net, sys (platform) earns fee; a viewer
	// occupying several roles sees the sum. gross = net + fee, so the three
	// viewpoints of a settled call sum to zero.
	mk := func(status string, gross, net, fee int64) client.Transaction {
		return client.Transaction{Owner: "buyer", Target: "seller", Status: status, Gross: gross, Net: net, Fee: fee}
	}
	cases := []struct {
		name, viewer string
		tx           client.Transaction
		want         []string // substrings the rendered cell must contain
	}{
		{"buyer pays gross", "buyer", mk("success", 100, 90, 10), []string{"-100 credits"}},
		{"seller earns net", "seller", mk("success", 100, 90, 10), []string{"sig-credited", "+90 credits"}},
		{"platform earns fee", "sys@testnet", mk("success", 100, 90, 10), []string{"sig-credited", "+10 credits"}},
		{"no stake shows dash", "bystander", mk("success", 100, 90, 10), []string{"—"}},
		{"failed refunds buyer", "buyer", mk("failure", 100, 0, 0), []string{"sig-refunded", "↩", "100 credits"}},
		{"failed non-buyer dash", "seller", mk("failure", 100, 0, 0), []string{"—"}},
		{"open parks buyer", "buyer", mk("open", 100, 0, 0), []string{"sig-escrow", "◷", "100 credits"}},
	}
	r := &Renderer{identity: playKernel}
	for _, c := range cases {
		got := string(r.txAmount(c.tx, c.viewer))
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: txAmount = %q, want substring %q", c.name, got, w)
			}
		}
	}

	// Self-call by a normal user (buyer == seller): nets to −fee.
	self := client.Transaction{Owner: "a", Target: "a", Status: "success", Gross: 100, Net: 90, Fee: 10}
	if got := string(r.txAmount(self, "a")); !strings.Contains(got, "-10 credits") {
		t.Errorf("self-call = %q, want -10 credits (the fee)", got)
	}
	// sys as the seller collects net + fee = gross.
	sysSell := client.Transaction{Owner: "a", Target: "sys@testnet", Status: "success", Gross: 100, Net: 90, Fee: 10}
	if got := string(r.txAmount(sysSell, "sys@testnet")); !strings.Contains(got, "+100 credits") {
		t.Errorf("sys-as-seller = %q, want +100 credits (net+fee)", got)
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
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	// The drawer renders whatever the nav model holds — it no longer hard-codes
	// its own links — so the fixture supplies the nav the sidebar would get.
	data := Page{Me: "sys@testnet", IsSuperuser: true, Themes: ThemeList(), Nav: []NavItem{
		{Slug: "browse", Label: "browse", Href: "/browse"},
		{Slug: "settings", Label: "settings", Href: "/settings"},
		{Slug: "network", Label: "network", Href: "/admin/peers"},
		{Slug: "admin", Label: "admin", Href: "/admin"},
	}}
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
		t.Error("mobile drawer (_menu_content) must carry Admin + network nav")
	}
}

func TestTabRenderer(t *testing.T) {
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	render := func(tb Tab) string {
		rec := httptest.NewRecorder()
		if err := r.Fragment(rec, 200, "_tab", tb); err != nil {
			t.Fatal(err)
		}
		return rec.Body.String()
	}
	// Active: class active AND aria-current, from one bool (can't diverge).
	on := render(tab("all", "/actions", true))
	if !strings.Contains(on, `class="tab active"`) || !strings.Contains(on, `aria-current="page"`) {
		t.Errorf("active tab = %q, want active class + aria-current", on)
	}
	// Inactive: neither active class nor aria-current (label "active" aside).
	off := render(tab("active", "/actions?filter=active", false))
	if strings.Contains(off, `class="tab active"`) || strings.Contains(off, "aria-current") {
		t.Errorf("inactive tab = %q, want neither active class nor aria-current", off)
	}
	// paneltab (login): adds data-tab + role=tab and the /login?tab= href.
	p := render(paneltab("Sign in", "signin", true))
	if !strings.Contains(p, `data-tab="signin"`) || !strings.Contains(p, `role="tab"`) || !strings.Contains(p, `href="/login?tab=signin"`) {
		t.Errorf("paneltab = %q, want data-tab + role=tab + login href", p)
	}
}

func TestActionsHref(t *testing.T) {
	cases := []struct{ scope, filter, want string }{
		{"", "", "/workshop"},
		{"all", "", "/workshop?scope=all"},
		{"", "active", "/workshop?filter=active"},
		{"all", "inactive", "/workshop?filter=inactive&scope=all"},
	}
	for _, c := range cases {
		if got := actionsHref(c.scope, c.filter); got != c.want {
			t.Errorf("actionsHref(%q,%q) = %q want %q", c.scope, c.filter, got, c.want)
		}
	}
}

func TestCallOutcomeGrantRequired(t *testing.T) {
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	out := Outcome{Class: GrantRequired, ActionRef: "sys@testnet/gmail-send", Message: "needs a connected account"}
	if err := r.Fragment(rec, 200, "_call_outcome", out); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	// The affordance links to the consent-plan page for the action's FOLDER
	// selector (sys@testnet, not the action address) — one gesture connects the
	// whole provider group; the per-scheme consent forms live on that page
	// (v0.7 §14 delta display). The query escapes the address's "@".
	if !strings.Contains(body, `href="/grants/connect?selector=sys%40testnet"`) {
		t.Errorf("grant_required outcome must link to the folder connect plan: %s", body)
	}
	if strings.Contains(body, "gmail-send") {
		t.Error("connect link must use the folder selector, not the degenerate single-action ref")
	}
	if !strings.Contains(body, "Connect") || strings.Contains(body, "failed") {
		t.Error("grant_required must read as a Connect affordance, not a failure")
	}
}

func TestGrantConnectPageShowsPlanDelta(t *testing.T) {
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	plan := client.GrantPlan{
		Groups: []client.GrantGroup{
			{ProviderKey: "bearer:api.acme.com", Provider: "api.acme.com", Scheme: "delegated_bearer",
				Actions: []client.GrantPlanAction{{Action: "acme/send"}, {Action: "acme/read"}}},
			{ProviderKey: "oauth:https://g/token|cid", Provider: "google", Scheme: "oauth_delegated", Scopes: []string{"mail.send"},
				Actions: []client.GrantPlanAction{{Action: "g/mail"}}},
		},
		SkippedUncallable: 2,
	}
	p := Page{Me: "me@testnet", CSRFField: template.HTML("<input>"), Data: grantConnectView{Selector: "acme", Plan: plan}}
	if err := r.Render(rec, 200, "grant_connect", p); err != nil {
		t.Fatalf("render: %v", err)
	}
	body := rec.Body.String()
	// The delta: every action to be connected is listed (names, per _ref rendering).
	for _, name := range []string{"send", "read", "mail"} {
		if !strings.Contains(body, name) {
			t.Errorf("plan must list action %q: %s", name, body)
		}
	}
	// The bearer group offers a masked token form carrying selector + provider_key.
	if !strings.Contains(body, `type="password"`) || !strings.Contains(body, `action="/grants/connect"`) {
		t.Errorf("bearer group must offer a masked token form: %s", body)
	}
	if !strings.Contains(body, `value="bearer:api.acme.com"`) || !strings.Contains(body, `value="acme"`) {
		t.Errorf("token form must carry provider_key + selector: %s", body)
	}
	// The oauth group offers the browser consent instead — a navigational link,
	// never a form: form-action 'self' blocks a submission's cross-origin
	// redirect to the provider.
	if !strings.Contains(body, `href="/grants/start?selector=`) {
		t.Errorf("oauth group must offer the browser-consent link: %s", body)
	}
	if strings.Contains(body, `action="/grants/start"`) {
		t.Errorf("consent start must not be a form (CSP form-action blocks its redirect): %s", body)
	}
	if !strings.Contains(body, "2 action") {
		t.Errorf("skipped-uncallable count not surfaced: %s", body)
	}
}

func TestUserPageShowsConnections(t *testing.T) {
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	p := Page{Me: "me@testnet", CSRFField: template.HTML("<input>"), Data: userView{
		User: client.User{Address: "me@testnet",
			Connectors: []client.Connector{{
				Directory:   "chat",
				Connections: []client.Connection{{Provider: "127.0.0.1:8080", ProviderKey: "bearer:h"}},
				Actions:     []client.Grant{{Action: "chat/pay", Scopes: []string{"pay.write"}, ProviderKey: "bearer:h", CreatedAt: "2026-07-12T00:00:00Z"}},
			}},
			Connections: []client.Connection{
				{Provider: "127.0.0.1:8080", Actions: 8, Unused: false, ProviderKey: "bearer:h", CreatedAt: "2026-07-12T00:00:00Z"},
				{Provider: "api.acme.com", Actions: 0, Unused: true, ProviderKey: "oauth:a", CreatedAt: "2026-07-11T00:00:00Z"},
			}},
	}}
	if err := r.Render(rec, 200, "settings", p); err != nil {
		t.Fatalf("render: %v", err)
	}
	body := rec.Body.String()
	// The grant tree: a folder per directory (chat), expandable to its actions.
	if !strings.Contains(body, "<details") || !strings.Contains(body, "chat") {
		t.Errorf("grant folder (details tree) not rendered: %s", body)
	}
	// The folder summary disconnects the whole account (provider_key)…
	if !strings.Contains(body, `name="account" value="bearer:h"`) {
		t.Errorf("account-level disconnect-all (provider_key) missing: %s", body)
	}
	// …and each contained action has its own per-grant disconnect (by selector).
	if !strings.Contains(body, `name="action" value="chat/pay"`) {
		t.Errorf("per-action disconnect missing: %s", body)
	}
	// The unused account folds in as its own folder with account-level disconnect.
	if !strings.Contains(body, "api.acme.com") || !strings.Contains(body, "No actions use this account") {
		t.Errorf("unused account not surfaced: %s", body)
	}
	if !strings.Contains(body, `name="account" value="oauth:a"`) {
		t.Errorf("unused account disconnect missing: %s", body)
	}
}

// TestAdminPageOffersUnsuspend guards the suspended-state detection: a present
// suspended_at (here the RFC3339 zero value an older kernel emitted) must render the
// user as suspended AND offer the Unsuspend action.
func TestAdminPageOffersUnsuspend(t *testing.T) {
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	p := Page{Me: "sys@testnet", IsSuperuser: true, CSRFField: template.HTML("<input>"), Data: adminView{
		Users: []client.AdminUser{{Address: "victim@testnet", SuspendedAt: "0001-01-01T00:00:00Z"}},
	}}
	if err := r.Render(rec, 200, "admin", p); err != nil {
		t.Fatalf("render: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "suspended") {
		t.Errorf("zero-value suspended_at must render the suspended pill: %s", body)
	}
	if !strings.Contains(body, "/admin/users/victim@testnet/unsuspend") {
		t.Errorf("suspended user must offer the Unsuspend action: %s", body)
	}
}

// TestCallOutcomeFailedStatesRefundOnce guards the de-duplicated failure panel: the
// refund is disclosed once in the headline, the message carries only the kernel's
// reason (no repeated refund, no contradictory "not charged"), and the kernel's
// detail is shown inert below it.
func TestCallOutcomeFailedStatesRefundOnce(t *testing.T) {
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	out := Outcome{Class: Failed, Message: "The action timed out. You were refunded in full."}
	if err := r.Fragment(rec, 200, "_call_outcome", out); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Failed — fully refunded.") {
		t.Errorf("failure headline must state the refund once: %s", body)
	}
	if strings.Contains(body, "not charged") {
		t.Errorf("a refunded failure must not also claim 'not charged': %s", body)
	}
	// The kernel answers a failure CLASS now, never prose: a raw code must not
	// reach the buyer (§6.2).
	for _, code := range []string{"execution_failed", "invalid_state", "remote call failed"} {
		if strings.Contains(body, code) {
			t.Errorf("a kernel error class must never be shown to a buyer: %q in %s", code, body)
		}
	}
}

// A refused quote is NOT a failure: the kernel checks the pin before locking, so
// nothing ran and nothing was charged. It states the new price and asks again.
func TestCallOutcomeTermsChanged(t *testing.T) {
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	out := Outcome{Class: TermsChanged, Price: 500, QuoteHash: "abc",
		Message: "The price changed to 500 credits while this page was open. Nothing was charged."}
	if err := r.Fragment(rec, 200, "_call_outcome", out); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "not charged") || !strings.Contains(body, "500 credits") {
		t.Errorf("a refused quote must state the new price and that nothing moved: %s", body)
	}
	if strings.Contains(body, "refunded") || strings.Contains(body, "Failed") {
		t.Errorf("a refused quote is not a failure and nothing was refunded: %s", body)
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
	// One kernel, one abbreviation, wherever it is named: the header's
	// fingerprint and a peer named inside an error sentence must be the same
	// string, or a reader cannot tell they are the same kernel.
	key := "1MutJi6KXZIWlxJMiIKsu4gR6-R4ACoEJwP7sNT_mq4"
	ke := &client.KernelError{Meta: map[string]json.RawMessage{"peer": json.RawMessage(`"` + key + `"`)}}
	if keyFingerprint(key) != ke.PeerDisplay() {
		t.Errorf("the same key abbreviates two ways: header %q vs sentence %q",
			keyFingerprint(key), ke.PeerDisplay())
	}
}

// TestCallFormRun renders the shared _call_form for an action run: a repeatable
// money button reading "Run (12)" (no ¤), no locked block, and the result target
// sitting below the form (not nested inside it).
func TestCallFormRun(t *testing.T) {
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	cf := runForm(template.HTML(`<input>`), "A1", 12, FormModel{UseRawFallback: true}, nil, true, "alice", "QH")
	if err := r.Fragment(rec, 200, "_call_form", cf); err != nil {
		t.Fatalf("Fragment: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `btn-money`) || !strings.Contains(body, "Run (12 credits)") {
		t.Errorf("run form must have a money button reading %q: %s", "Run (12 credits)", body)
	}
	if strings.ContainsRune(body, '¤') {
		t.Error("money button must not render the ¤ glyph")
	}
	// The first-payment hint rides inline (no payment sheet).
	if !strings.Contains(body, "First payment to") || !strings.Contains(body, "alice") {
		t.Errorf("paid run with FirstPayee must show the first-payment hint inline: %s", body)
	}
	if strings.Contains(body, `data-oneshot="true"`) {
		t.Error("action run form must repeat, not be one-shot")
	}
	// The confirm carries no remark beside it (grammar §3).
	if strings.Contains(body, "Refunded") || strings.Contains(body, "all-in") {
		t.Errorf("the run confirm must carry no remark: %s", body)
	}
	// Result target must sit after the closing </form>, not inside it.
	form := strings.Index(body, "</form>")
	target := strings.Index(body, `id="run-result"`)
	if form < 0 || target < 0 || target < form {
		t.Errorf("result target must render below the form: form@%d target@%d", form, target)
	}
}

// TestCallFormComplete renders the shared _call_form for a task completion: a
// one-shot NEUTRAL "Complete" button and the pre-bound (locked) args shown as a
// read-only definition list above the editable fields, with no verbal label.
func TestCallFormComplete(t *testing.T) {
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	cf := completeForm(template.HTML(`<input>`), "S1", 5, FormModel{UseRawFallback: true}, false, json.RawMessage(`{"to":"bob"}`), nil, false, 0, "", false)
	if err := r.Fragment(rec, 200, "_call_form", cf); err != nil {
		t.Fatalf("Fragment: %v", err)
	}
	body := rec.Body.String()
	// Completion is a NEUTRAL button naming no amount (the completer isn't paying).
	if !strings.Contains(body, `type="submit">Complete</button>`) {
		t.Errorf("completion button must be a neutral Complete (no amount): %s", body)
	}
	if strings.Contains(body, "Complete (5)") || strings.Contains(body, "btn-money") {
		t.Errorf("completion button must not name an amount or use the money tier: %s", body)
	}
	if !strings.Contains(body, `data-oneshot="true"`) {
		t.Error("task completion form must be one-shot")
	}
	// Sealed args render as a read-only dl (the completed part), separated from the
	// editable part — same anatomy as the run form.
	if !strings.Contains(body, `class="frozen locked"`) || !strings.Contains(body, "<label>to</label>") || !strings.Contains(body, `<div class="box">bob</div>`) {
		t.Errorf("locked args must render as a frozen form: %s", body)
	}
	if strings.Contains(strings.ToLower(body), "prefilled") || strings.Contains(strings.ToLower(body), "remaining args") {
		t.Error("locked args carry no verbal label — the visual contrast is the signposting")
	}
	if !strings.Contains(body, `id="task-result-S1"`) {
		t.Errorf("completion result target must be task-result-S1: %s", body)
	}
	// A task's price is already parked — the refund disclosure is for the run confirm,
	// not the completion, so it must not appear here.
	if strings.Contains(body, "Refunded in full") {
		t.Errorf("task completion must not carry the run's refund disclosure: %s", body)
	}
}

// A structured form carries the one JSON toggle at its top, before any field,
// with the has_fields marker it disables; an approval or a raw-only form, with
// nothing to swap, carries none.
func TestCallFormCarriesTheJSONToggle(t *testing.T) {
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	render := func(fm FormModel, approval bool) string {
		rec := httptest.NewRecorder()
		if err := r.Fragment(rec, 200, "_call_form", completeForm(template.HTML(`<input>`), "S1", 0, fm, approval, json.RawMessage(`{"to":"bob"}`), nil, false, 0, "", false)); err != nil {
			t.Fatal(err)
		}
		return rec.Body.String()
	}
	body := render(FormModel{Fields: []Field{{Name: "to", Type: "string", Path: "/to"}}}, false)
	toggle := `<button type="button" class="json-toggle" data-json-toggle aria-pressed="false">JSON</button>`
	if !strings.Contains(body, toggle) || !strings.Contains(body, `name="has_fields"`) || !strings.Contains(body, `data-json `) {
		t.Errorf("structured form must carry the JSON toggle and the has_fields marker: %s", body)
	}
	if strings.Index(body, toggle) > strings.Index(body, `class="frozen locked"`) {
		t.Error("the toggle sits at the top of the block, before the sealed part")
	}
	if strings.Contains(body, "data-raw-seg") || strings.Contains(body, "field-switch") {
		t.Error("the retired two-segment switch must be gone")
	}
	for _, b := range []string{render(FormModel{UseRawFallback: true}, false), render(FormModel{}, true)} {
		if strings.Contains(b, "data-json-toggle") {
			t.Errorf("a raw-only or approval form has nothing to swap: %s", b)
		}
	}
}

// TestCallFormApproval renders an approval _call_form (a task with nothing left to
// fill): just the locked block + the Complete button, no raw-JSON input and no
// wordy "acknowledge" line — the locked JSON + button carry it visually.
func TestCallFormApproval(t *testing.T) {
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	cf := completeForm(template.HTML(`<input>`), "S9", 1, FormModel{UseRawFallback: true}, true, json.RawMessage(`{"message":"hi"}`), nil, false, 0, "", false)
	if err := r.Fragment(rec, 200, "_call_form", cf); err != nil {
		t.Fatalf("Fragment: %v", err)
	}
	body := rec.Body.String()
	if strings.Contains(body, `name="raw_args"`) {
		t.Errorf("an approval form must not render a raw-args box: %s", body)
	}
	if strings.Contains(strings.ToLower(body), "acknowledge") || strings.Contains(body, "No further input") {
		t.Errorf("approval form must not carry a wordy acknowledge line: %s", body)
	}
	if !strings.Contains(body, `type="submit">Complete</button>`) {
		t.Errorf("approval form must show a neutral Complete button (no amount): %s", body)
	}
}

// TestStatStripHonestEmpty renders the shared stat strip for an unused action: it
// must read honest empty states (none, with a (0) count when Counts) — never fake
// "—%" or "0.0s" data.
func TestStatStripHonestEmpty(t *testing.T) {
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	if err := r.Fragment(rec, 200, "_stat_strip", statStrip(12, nil, true, true)); err != nil {
		t.Fatalf("Fragment: %v", err)
	}
	body := rec.Body.String()
	if strings.Contains(body, "—%") || strings.Contains(body, "0.0s") {
		t.Errorf("unused action must not render fake stat data: %s", body)
	}
	if !strings.Contains(body, "none (0)") {
		t.Errorf("unused action (with counts) must read honest 'none (0)': %s", body)
	}
	if !strings.Contains(body, "12") {
		t.Errorf("stat strip must show the price: %s", body)
	}
}

// TestStatStripCounts contrasts the detail (with counts) and card (no counts)
// renderings of the same stats — the only difference is the (N) run/rater counts.
func TestStatStripCounts(t *testing.T) {
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	s := client.Stats{Uses: 10, Successes: 9, RatingCount: 4, RatingEstimate: 0.75, LatencyEstimate: 0.4}

	rec := httptest.NewRecorder()
	if err := r.Fragment(rec, 200, "_stat_strip", statStrip(5, &s, true, true)); err != nil {
		t.Fatal(err)
	}
	detail := rec.Body.String()
	if !strings.Contains(detail, "90%") || !strings.Contains(detail, "(10)") || !strings.Contains(detail, "(4)") {
		t.Errorf("detail strip must show success%% with run/rater counts: %s", detail)
	}

	// The card strip drops both the (N) counts and the leading price (the card
	// shows price top-right), but keeps the iconned success/rating/latency line.
	rec = httptest.NewRecorder()
	if err := r.Fragment(rec, 200, "_stat_strip", statStrip(5, &s, false, false)); err != nil {
		t.Fatal(err)
	}
	card := rec.Body.String()
	if !strings.Contains(card, "90%") {
		t.Errorf("card strip must still show success%%: %s", card)
	}
	if strings.Contains(card, "(10)") || strings.Contains(card, "(4)") {
		t.Errorf("card strip must NOT show counts: %s", card)
	}
	if strings.Contains(card, "#gem") {
		t.Errorf("card strip must NOT lead with the price gem: %s", card)
	}
}

func TestRenderFragment(t *testing.T) {
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	out := Outcome{Class: Success, Result: json.RawMessage(`{"ok":true}`), TxID: "tx1"}
	if err := r.Fragment(rec, 200, "_call_outcome", out); err != nil {
		t.Fatalf("Fragment: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `class="outcome success"`) || strings.Contains(body, "Success.") || !strings.Contains(body, "/transactions/tx1") {
		t.Errorf("a success is its styling and its result, with no sentence: %s", body)
	}
	if sep := strings.Index(body, `<hr class="card-sep">`); sep < 0 || sep < strings.Index(body, `data-json-toggle`) || sep > strings.Index(body, "/transactions/tx1/rate") {
		t.Errorf("a rule separates the result from its receipt and rating: %s", body)
	}
}

// TestBadgeHelper checks the interval default (a mis-plumbed 0 must not yield an
// invalid "every 0s" htmx trigger).
func TestBadgeHelper(t *testing.T) {
	if got := badge("/x", "l", 0, 0).Every; got != 30 {
		t.Errorf("badge interval defaulted to %d, want 30", got)
	}
	b := badge("/inbox/badge", "Waiting", 4, 15)
	if b.Every != 15 || b.Count != 4 || b.URL != "/inbox/badge" {
		t.Errorf("badge = %+v", b)
	}
}

// TestBadgePollFragment checks the reusable poller partial: htmx attributes + the
// interval, the count when > 0, and an empty body when 0 (so .badge:empty hides it).
func TestBadgePollFragment(t *testing.T) {
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	if err := r.Fragment(rec, 200, "_badge_poll", badge("/chat/badge", "chat", 3, 15)); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	for _, want := range []string{`hx-get="/chat/badge"`, `every 15s`, `hx-swap="innerHTML"`, `>3<`} {
		if !strings.Contains(body, want) {
			t.Errorf("badge fragment missing %q: %s", want, body)
		}
	}
	rec = httptest.NewRecorder()
	_ = r.Fragment(rec, 200, "_badge_poll", badge("/chat/badge", "chat", 0, 15))
	if strings.Contains(rec.Body.String(), ">0<") {
		t.Errorf("zero badge should render empty, got %q", rec.Body.String())
	}
}

// TestLayoutRendersBadgePollers proves the layout polls every badge — the core
// tasks badge and each app badge — on the configured interval, in both the sidebar
// and the mobile tab bar.
func TestLayoutRendersBadgePollers(t *testing.T) {
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	p := Page{
		Me:        "me@testnet",
		BadgeSecs: 15,
		Nav: []NavItem{
			{Slug: "inbox", Label: "inbox", Href: "/inbox", Icon: template.HTML(`<svg class="ico"></svg>`), BadgeURL: "/inbox/badge"},
			{Slug: "chat", Label: "chat", Href: "/chat", Icon: template.HTML(`<svg class="ico"></svg>`), Badge: 2, BadgeURL: "/chat/badge"},
		},
		Data: struct{ Message string }{"x"},
	}
	if err := r.Render(rec, 200, "error", p); err != nil {
		t.Fatalf("render: %v", err)
	}
	body := rec.Body.String()
	if strings.Count(body, `hx-get="/inbox/badge"`) < 2 {
		t.Errorf("tasks badge poller must appear in sidebar AND drawer: %s", body)
	}
	if !strings.Contains(body, `hx-get="/chat/badge"`) {
		t.Errorf("app badge poller missing: %s", body)
	}
	// tasks(sidebar+drawer+menu button) + chat(sidebar+drawer) = 5 pollers, all on 15s.
	if n := strings.Count(body, `every 15s`); n < 5 {
		t.Errorf("configured interval applied to %d pollers, want >= 5: %s", n, body)
	}
	if strings.Contains(body, `id="inbox-badge"`) {
		t.Error("legacy inbox-badge id should be gone")
	}
}

// TestConfirmModalIsNativeDialog guards snag #1: the shared confirm must be a
// native <dialog> (top layer) so it stacks above an open modal-form dialog —
// not a z-indexed div that a dialog would paint over.
func TestConfirmModalIsNativeDialog(t *testing.T) {
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	if err := r.Render(rec, 200, "error", Page{Me: "me@testnet", Data: struct{ Message string }{"x"}}); err != nil {
		t.Fatalf("render: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `<dialog id="modal-backdrop" class="modal-confirm"`) {
		t.Errorf("confirm modal must be a native <dialog class=modal-confirm>: %s", body)
	}
	if strings.Contains(body, `class="modal-backdrop"`) {
		t.Error("legacy z-indexed .modal-backdrop div must be gone (a dialog would stack over it)")
	}
}

// TestCreditsColumnIsSingleTotal guards snag #3: lists show one amount column =
// available + parked (total); the split stays only on prominent wallet/detail views.
func TestCreditsColumnIsSingleTotal(t *testing.T) {
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	// List → a single amount column showing the total.
	rec := httptest.NewRecorder()
	list := Page{Me: "me@testnet", Data: processesView{Open: []client.Process{{ID: "p1", Status: "open", Available: 30, Locked: 70}}}}
	if err := r.Render(rec, 200, "processes", list); err != nil {
		t.Fatalf("render processes: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `<th class="money">Amount</th>`) {
		t.Errorf("list must have a single amount column: %s", body)
	}
	if strings.Contains(body, ">Parked<") || strings.Contains(body, ">Available<") {
		t.Errorf("list must drop the standing Available/Parked columns: %s", body)
	}
	if !strings.Contains(body, ">100 credits<") {
		t.Errorf("amount cell must show the total 30+70=100 credits = 100 credits: %s", body)
	}

	// Detail → the available + parked split is kept.
	rec = httptest.NewRecorder()
	detail := Page{Me: "me@testnet", CSRFField: template.HTML("<input>"),
		Data: processView{Process: client.Process{ID: "p1", Status: "open", Available: 30, Locked: 70}}}
	if err := r.Render(rec, 200, "process", detail); err != nil {
		t.Fatalf("render process: %v", err)
	}
	if d := rec.Body.String(); !strings.Contains(d, "available") || !strings.Contains(d, "parked") {
		t.Errorf("process detail must keep the available+parked split: %s", d)
	}
}

// TestActionDetailNeverGuessesAvailability replaces the old peer_state gating:
// the kernel withdrew that hint (v0.12.14), so a remote action is offered like
// any other and the kernel reports what happened. No health branch, no notice,
// and no poller waiting for a peer to "recover".
func TestActionDetailNeverGuessesAvailability(t *testing.T) {
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	p := Page{Me: "me@testnet", CSRFField: template.HTML("<input>"), Data: actionView{
		Action:    client.Action{ID: "a1", Action: "peerx/greet", Name: "greet", Kind: "remote_proxy", Active: true, Visibility: "public", Price: 10},
		Form:      BuildForm(nil),
		BadgeSecs: 15,
	}}
	if err := r.Render(rec, 200, "action_detail", p); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "data-json") {
		t.Error("a remote action must render the run form: availability is the kernel's to report")
	}
	if strings.Contains(body, `hx-get="/actions/a1"`) {
		t.Error("the run region must carry no poller")
	}
	for _, gone := range []string{"peer offline", "peer unfunded", "Peer offline.", "peer_state"} {
		if strings.Contains(body, gone) {
			t.Errorf("peer_state residue in the action page: %q", gone)
		}
	}
}

// TestProcessAwaitingReceiptPolls checks item 4: a process parked on a remote
// receipt polls GET /processes/{id}; once settled the region carries no poller.
func TestProcessAwaitingReceiptPolls(t *testing.T) {
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	render := func(awaiting bool) string {
		rec := httptest.NewRecorder()
		p := Page{Me: "me@testnet", CSRFField: template.HTML("<input>"), Data: processView{
			Process:   client.Process{ID: "p1", Status: "open", AwaitingReceipt: awaiting, AwaitingReceiptSince: "2026-07-12T00:00:00Z"},
			BadgeSecs: 15,
		}}
		if err := r.Render(rec, 200, "process", p); err != nil {
			t.Fatalf("render awaiting=%v: %v", awaiting, err)
		}
		return rec.Body.String()
	}
	parked := render(true)
	if !strings.Contains(parked, `hx-get="/processes/p1"`) || !strings.Contains(parked, "every 15s") {
		t.Errorf("awaiting-receipt banner must poll /processes/p1 every 15s: %s", parked)
	}
	if !strings.Contains(parked, "Awaiting remote receipt") {
		t.Errorf("awaiting-receipt banner text missing: %s", parked)
	}
	settled := render(false)
	if strings.Contains(settled, `hx-get="/processes/p1"`) {
		t.Error("settled process must NOT carry the awaiting-receipt poller")
	}
}

// TestCallOutcomePeerBranches checks item 5: peer-unreachable/unfunded render as
// first-class outcomes in the shared _call_outcome panel, not generic errors.
func TestCallOutcomePeerBranches(t *testing.T) {
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		class OutcomeClass
		want  string
	}{
		{PeerUnreachable, "Peer unreachable"},
		{PeerUnfunded, "Not served on credit"},
	} {
		rec := httptest.NewRecorder()
		out := Outcome{Class: tc.class, Message: "explanatory copy"}
		if err := r.Fragment(rec, 200, "_call_outcome", out); err != nil {
			t.Fatalf("fragment %v: %v", tc.class, err)
		}
		body := rec.Body.String()
		if !strings.Contains(body, tc.want) || !strings.Contains(body, "explanatory copy") {
			t.Errorf("outcome %v must render %q with its message: %s", tc.class, tc.want, body)
		}
	}
}

// TestPeersPollerAndSuspendedToggle checks the peers table is an htmx-polled
// fragment, and a Show/Hide-suspended toggle drives the ?all=1 view.
// The roster is one list under a tabs filter, never polled. Suspension is a
// counterparty-only axis, so its toggle appears on that tab alone and the
// known-network link drops it.
func TestPeersTabsAndSuspendedToggle(t *testing.T) {
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	renderPeers := func(tab string, showSuspended bool) string {
		rec := httptest.NewRecorder()
		p := Page{Me: "sys@testnet", IsSuperuser: true, BadgeSecs: 15, CSRFField: template.HTML("<input>"), Data: adminPeersView{
			Identity:      client.Identity{Handle: "me", PublicKey: "PK"},
			Peers:         []client.Peer{{Petname: "peerb", PublicKey: "KEY", HasAccount: true, LastSeen: "2026-07-12T00:00:00Z"}},
			Tab:           tab,
			ShowSuspended: showSuspended,
		}}
		if err := r.Render(rec, 200, "admin_peers", p); err != nil {
			t.Fatalf("render: %v", err)
		}
		return rec.Body.String()
	}
	body := renderPeers(tabCounterparties, false)
	if strings.Contains(body, `hx-get="/admin/peers`) {
		t.Errorf("the roster must not poll itself: %s", body)
	}
	for _, want := range []string{`class="tabs control-tabs"`, ">counterparties<", ">known network<"} {
		if !strings.Contains(body, want) {
			t.Errorf("roster must switch classes with the tabs component, missing %q", want)
		}
	}
	if !strings.Contains(body, `href="/admin/peers?all=1"`) || !strings.Contains(body, "Show suspended") {
		t.Errorf("counterparties tab must offer a Show-suspended toggle: %s", body)
	}
	suspended := renderPeers(tabCounterparties, true)
	if !strings.Contains(suspended, "Hide suspended") {
		t.Errorf("suspended view must show a Hide toggle: %s", suspended)
	}
	// The known-network tab link never carries ?all=1 — a discovered kernel has no
	// account to suspend — and the tab itself offers no suspension control.
	if !strings.Contains(suspended, `href="/admin/peers?tab=known"`) {
		t.Errorf("known-network tab must drop the suspended filter: %s", suspended)
	}
	known := renderPeers(tabKnown, false)
	if strings.Contains(known, "Show suspended") || strings.Contains(known, "Hide suspended") {
		t.Errorf("suspension is a counterparty axis and must not appear on known network: %s", known)
	}
}

// A discovered kernel is the unresolved counterpart of a counterparty: a chip,
// its catalog, and the two gestures that apply to a kernel holding no account —
// Fund (promote it) and Name (bind a petname, the only way to name a kernel we
// have merely discovered). No position and no account-dependent verb.
func TestDiscoveredKernelCardHasNoAccountVerbs(t *testing.T) {
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	p := Page{Me: "sys@testnet", IsSuperuser: true, CSRFField: template.HTML("<input>"), Data: adminPeersView{
		Identity: client.Identity{Handle: "me", PublicKey: "PK"},
		Tab:      tabKnown,
		Peers: []client.Peer{
			{Nickname: "twin", PublicKey: "KT1", Actions: 13, LastSeen: "2026-08-06T00:04:34Z"},
			{Nickname: "twin", PublicKey: "KT2", Actions: 7, LastSeen: "2026-08-06T00:04:35Z"},
		},
	}}
	if err := r.Render(rec, 200, "admin_peers", p); err != nil {
		t.Fatalf("render: %v", err)
	}
	body := rec.Body.String()
	for _, want := range []string{`class="chip" title="Discovered`, "13 actions", "catalog verified"} {
		if !strings.Contains(body, want) {
			t.Errorf("discovered card missing %q: %s", want, body)
		}
	}
	for _, gone := range []string{"position", "/admin/peers/settle", "/admin/peers/suspend", "our credit there"} {
		if strings.Contains(body, gone) {
			t.Errorf("discovered card must not carry %q (it holds no account here)", gone)
		}
	}
	// Naming it IS available — on the peer's own page, and keyed on the public
	// key, since an unbound kernel has no name to key on (§6.6).
	if !strings.Contains(body, `href="/admin/peers/KT1?form=admin-rename"`) {
		t.Errorf("discovered card must offer Name, keyed on the public key: %s", body)
	}
	// last_seen is catalog freshness, not liveness: muted text, never a status pill.
	if strings.Contains(body, "status-credited") {
		t.Error("catalog freshness must not render as a liveness pill")
	}
	// Two kernels advertising one nickname stay two key-distinct cards.
	if strings.Count(body, `class="chip" title="Discovered`) != 2 || !strings.Contains(body, "KT1") || !strings.Contains(body, "KT2") {
		t.Errorf("colliding advertised handles must stay two rows: %s", body)
	}
	// There is no Fund. A peer holds no balance and the kernel serves no route
	// that credits one: a peer account is identity, and a foreign call is funded
	// by the serving kernel (kernel §D14). A discovered kernel is promoted by a
	// first call, not by a payment.
	if strings.Contains(body, "fund_key") || strings.Contains(body, ">Fund<") {
		t.Errorf("a peer is never credited, so no card offers to fund one: %s", body)
	}
}

// TestPeerRowOffersSettleAndSuspend: the two levers that remain on a peer row —
// Settle (by handle) and Suspend (by key); a suspended peer is marked and offers
// Unsuspend. There is no unsubscribe: a peer is not something one leaves.
func TestPeerRowOffersSuspendByKey(t *testing.T) {
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	p := Page{Me: "sys@testnet", IsSuperuser: true, BadgeSecs: 15, CSRFField: template.HTML("<input>"), Data: adminPeersView{
		Identity:      client.Identity{Handle: "me", PublicKey: "PK"},
		ShowSuspended: true,
		Peers: []client.Peer{
			{Petname: "active", PublicKey: "KF", HasAccount: true},
			{Petname: "frozen", PublicKey: "KG", HasAccount: true, SuspendedAt: "2026-07-19T17:50:24Z"},
		},
	}}
	if err := r.Render(rec, 200, "admin_peers", p); err != nil {
		t.Fatalf("render: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, ">suspended<") {
		t.Errorf("suspended peer must be marked: %s", body)
	}
	// Every form posts the KEY: an alias is display only and advertised names
	// collide across kernels, so a handle is not an address.
	if strings.Contains(body, "/admin/peers/settle") {
		t.Errorf("settlement is per call on the rail, not a peer verb: %s", body)
	}
	if strings.Contains(body, "unsubscribe") || strings.Contains(body, "Unsubscribe") {
		t.Errorf("no peer row may offer an unsubscribe verb: %s", body)
	}
	if !strings.Contains(body, `action="/admin/peers/suspend"`) || !strings.Contains(body, `value="KF"`) {
		t.Errorf("active row must offer Suspend by key: %s", body)
	}
	if !strings.Contains(body, `action="/admin/peers/unsuspend"`) || !strings.Contains(body, `value="KG"`) {
		t.Errorf("suspended row must offer Unsuspend by key: %s", body)
	}
}

// TestInspectStatusPolls checks item 2: the inspect status card is an htmx-polled
// fragment so an open inspect view is a live monitor.
func TestInspectStatusPolls(t *testing.T) {
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	p := Page{Me: "sys@testnet", IsSuperuser: true, BadgeSecs: 15, CSRFField: template.HTML("<input>"), Data: adminInspectView{
		Key:        "KEYC",
		Inspection: client.Inspection{Petname: "kernelc", Online: true, Source: "live", Reachability: client.Reachability{Path: "direct"}},
	}}
	if err := r.Render(rec, 200, "admin_inspect", p); err != nil {
		t.Fatalf("render: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="inspect-status"`) || !strings.Contains(body, `hx-get="/admin/peers/KEYC"`) || !strings.Contains(body, "every 15s") {
		t.Errorf("inspect status card must be an htmx-polled fragment: %s", body)
	}
}

// TestInspectOfflineStranger: the offline reason is a short standardized phrase
// with the raw fed error tucked into a title tooltip, and a kernel we hold no
// billing row for states no position.
func TestInspectOfflineStranger(t *testing.T) {
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	p := Page{Me: "sys@testnet", IsSuperuser: true, BadgeSecs: 15, CSRFField: template.HTML("<input>"), Data: adminInspectView{
		Key: "KDOWN",
		Inspection: client.Inspection{Petname: "", Online: false, Source: "none",
			Reachability: client.Reachability{Path: "unreachable", Error: "fed: cannot resolve peer 12D3KooW…: routing: not found"}},
	}}
	if err := r.Render(rec, 200, "admin_inspect", p); err != nil {
		t.Fatalf("render: %v", err)
	}
	body := rec.Body.String()
	if strings.Contains(body, "we owe them") || strings.Contains(body, "owes us") {
		t.Error("a kernel with no billing row must state no position")
	}
	// Standardized short reason; the raw fed error is only in a title tooltip.
	if !strings.Contains(body, ">unreachable</span>") {
		t.Errorf("offline reason must be the short 'unreachable': %s", body)
	}
	if strings.Contains(body, ">fed: cannot") {
		t.Error("the raw libp2p error must not be shown as inline text (title only)")
	}
	if !strings.Contains(body, `title="fed: cannot resolve peer`) {
		t.Error("the raw error should be available on hover (title)")
	}
}

// A counterparty card carries contact facts and no balance: a peer holds no
// money on any path (kernel §D14), so a position or a credit figure here would
// be a fabrication.
func TestPeerCardCarriesContactNotMoney(t *testing.T) {
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	p := Page{Me: "sys@testnet", IsSuperuser: true, CSRFField: template.HTML("<input>"), Data: adminPeersView{
		Identity: client.Identity{Handle: "me", PublicKey: "PK"},
		Peers:    []client.Peer{{Petname: "peerb", PublicKey: "KEY", HasAccount: true, LastSeen: "2026-07-12T00:00:00Z"}},
	}}
	if err := r.Render(rec, 200, "admin_peers", p); err != nil {
		t.Fatalf("render: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "catalog verified") {
		t.Errorf("counterparty card missing the contact cache: %s", body)
	}
	for _, gone := range []string{"our credit there", "owes us", "we owe them", "position"} {
		if strings.Contains(body, gone) {
			t.Errorf("a peer card must state no money: found %q", gone)
		}
	}
	if !strings.Contains(body, `class="kernel-card`) || !strings.Contains(body, `class="media wide"`) {
		t.Errorf("a counterparty must render as a kernel card with a banner: %s", body)
	}
}

// TestRelTime checks the coarse "time ago" freshness rendering used for a peer's
// last-seen (empty stays empty; unparseable falls back to raw).
func TestRelTime(t *testing.T) {
	now := time.Now()
	ago := func(d time.Duration) string { return now.Add(-d).Format(time.RFC3339) }
	cases := []struct{ in, want string }{
		{"", ""},
		{"not-a-time", "not-a-time"},
		{ago(10 * time.Second), "just now"},
		{ago(90 * time.Second), "1 min ago"},
		{ago(3 * time.Minute), "3 mins ago"},
		{ago(1 * time.Hour), "1 hour ago"},
		{ago(2 * time.Hour), "2 hours ago"},
		{ago(24 * time.Hour), "1 day ago"},
		{ago(3 * 24 * time.Hour), "3 days ago"},
	}
	for _, c := range cases {
		if got := relTime(c.in); got != c.want {
			t.Errorf("relTime(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// The mobile nav must survive JavaScript being off. ?menu=open server-renders
// the drawer [open] so it falls back to an in-flow card, and the trigger is a
// real link rather than a bare button — the same guarantee §6.1 makes for
// ?form=. Without this the drawer is unreachable with JS off, which would be
// worse than the tab bar it replaces.
func TestNavDrawerWorksWithoutJS(t *testing.T) {
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	nav := []NavItem{{Slug: "browse", Label: "browse", Href: "/browse"}}
	render := func(open bool) string {
		rec := httptest.NewRecorder()
		p := Page{Me: "me@testnet", Nav: nav, MenuOpen: open, Path: "/browse",
			Data: struct{ Message string }{"x"}}
		if err := r.Render(rec, 200, "error", p); err != nil {
			t.Fatalf("render: %v", err)
		}
		return rec.Body.String()
	}
	closed, open := render(false), render(true)

	// The trigger is a real link to the fallback URL, not a bare button, and it
	// anchors to the drawer so the no-JS landing is unambiguous.
	if !strings.Contains(closed, `href="?menu=open#nav-drawer"`) {
		t.Error("menu trigger must be a real anchored link for the no-JS path")
	}
	if strings.Contains(closed, `<dialog id="nav-drawer" class="drawer" aria-label="Navigation" open`) {
		t.Error("drawer must not render open without ?menu=open")
	}
	if !strings.Contains(open, ` open>`) {
		t.Error("?menu=open must server-render the drawer [open]")
	}
	// Closing must also be a link, so it works with JS off.
	if !strings.Contains(open, `class="modal-close" href="/browse"`) {
		t.Error("drawer close must be a real link back to the current path")
	}
}

// The theme picker is a set-once preference, so it must not sit in persistent
// chrome. Exactly one exists, and it is on the settings page.
func TestThemePickerOnlyInSettings(t *testing.T) {
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	p := Page{Me: "me@testnet", Themes: ThemeList(), Nav: []NavItem{{Slug: "browse", Href: "/browse"}},
		Data: struct{ Message string }{"x"}}
	if err := r.Render(rec, 200, "error", p); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(rec.Body.String(), "data-theme-select"); n != 0 {
		t.Errorf("chrome carries %d theme pickers, want 0 (settings owns it)", n)
	}
}

// An action address splits on the first slash into its owner and its name, and
// it is LOCAL iff its kernel segment is this kernel's own name — never because
// it lacks an "@", which every address carries (requirements.md §4).
func TestRefSplitsAndDecidesLocality(t *testing.T) {
	r := &Renderer{identity: playKernel}
	cases := []struct {
		ref, owner, name string
		local            bool
	}{
		{"sys@testnet/llm/chat", "sys@testnet", "llm/chat", true},
		{"lena@testnet/ocr-translate", "lena@testnet", "ocr-translate", true},
		{"alice@B/greet", "alice@B", "greet", false},
		{"tom@acme2/brief/daily", "tom@acme2", "brief/daily", false},
		{"eve@testnet2/x", "eve@testnet2", "x", false}, // a suffix of another name is not ours
	}
	for _, c := range cases {
		v := r.refOf(c.ref, "")
		if v.Owner.Text != c.owner || v.Name != c.name || v.Local != c.local {
			t.Errorf("ref(%q) = %+v, want owner %q name %q local %v", c.ref, v, c.owner, c.name, c.local)
		}
	}
	// An unknown kernel owns nothing: every address renders as someone else's.
	if (&Renderer{identity: unknownKernel}).refOf("sys@testnet/time", "").Local {
		t.Error("with the kernel's name unknown, no address is local")
	}
}

// A remote owner renders emphasized but UNLINKED: there is no local profile
// behind another kernel's user (requirements.md §6.2; extends the
// inert-counterparty rule). A local owner links to its owner page by address.
func TestRefRemoteUnlinked(t *testing.T) {
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	render := func(ref, id string) string {
		rec := httptest.NewRecorder()
		if err := r.Fragment(rec, 200, "_ref", r.refOf(ref, id)); err != nil {
			t.Fatal(err)
		}
		return rec.Body.String()
	}
	body := render("alice@B/greet", "")
	if !strings.Contains(body, "alice@B") || !strings.Contains(body, `class="owner"`) {
		t.Errorf("remote owner segment must render emphasized: %s", body)
	}
	if strings.Contains(body, "/users/") || strings.Contains(body, "<a ") {
		t.Errorf("remote owner must not link anywhere: %s", body)
	}
	if body := render("lena@testnet/greet", ""); !strings.Contains(body, `href="/users/lena@testnet"`) {
		t.Errorf("local owner must link to its owner page: %s", body)
	}
	// Passing an id makes the NAME the action link.
	if body := render("lena@testnet/greet", "A1"); !strings.Contains(body, `href="/actions/A1"`) {
		t.Errorf("an id must link the name to the action: %s", body)
	}
}

// A raw kernel key never renders whole in a ref: an unresolved remote action is
// addressed owner@<43-char key>/name, which as a card title is unreadable. The
// owner stays legible, the key is truncated through the SAME primitive a kernel
// card uses, and the whole segment survives in the title.
func TestRefTruncatesRawKernelKeys(t *testing.T) {
	const key = "esKD_l0c_1xaJ9rP-C8woBZamNPvIYFE_FOMSCi4C3I"
	r := &Renderer{identity: playKernel}
	v := r.refOf("sys@"+key+"/llm/embed", "")
	if v.Owner.Text != "sys@"+key[:keyLen]+"…" {
		t.Errorf("owner segment = %q, want the key truncated", v.Owner.Text)
	}
	if v.Owner.Title != "sys@"+key {
		t.Errorf("the full segment must survive in the title, got %q", v.Owner.Title)
	}
	if v.Name != "llm/embed" || v.Local {
		t.Errorf("split wrong: %+v", v)
	}
	// A bound petname is not key-shaped and renders whole.
	if b := r.refOf("sys@daios/llm/embed", ""); b.Owner.Text != "sys@daios" || b.Owner.IsKey {
		t.Errorf("a petname-qualified ref must render whole: %+v", b)
	}
}

// A payment-task completion is a spend of the completer's own money: money
// button naming the sealed amount, unnumbered money button when the completer
// types it (§6.3 two-branch rule).
func TestCallFormPayment(t *testing.T) {
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	cf := completeForm(template.HTML(`<input>`), "SP", 0, FormModel{}, true,
		json.RawMessage(`{"target":"alice","amount":300}`), nil,
		true, 300, "alice", false)
	if err := r.Fragment(rec, 200, "_call_form", cf); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "btn-money") || !strings.Contains(body, "Complete (300 credits)") {
		t.Errorf("sealed payment must use the money button naming the amount: %s", body)
	}
	if !strings.Contains(body, "Paid from your balance") || !strings.Contains(body, "alice") {
		t.Errorf("payment form must disclose whose funds move and to whom: %s", body)
	}
	// Typed amount: money button, no parenthesized figure (wallet-send pattern).
	rec2 := httptest.NewRecorder()
	cf2 := completeForm(template.HTML(`<input>`), "SP2", 0, FormModel{UseRawFallback: true}, false, nil, nil,
		true, 0, "", false)
	if err := r.Fragment(rec2, 200, "_call_form", cf2); err != nil {
		t.Fatal(err)
	}
	b2 := rec2.Body.String()
	if !strings.Contains(b2, "btn-money") || strings.Contains(b2, "Complete ($") {
		t.Errorf("typed-amount payment must be an unnumbered money button: %s", b2)
	}
}

// A DISCOVERED lookup hit is rendered from the lookup row alone: nothing local
// backs it, so the card states the kernel-qualified ref and the description and
// stops there — no price, no reputation strip, no link to a local action page,
// and no run affordance (the local price does not exist until resolution).
func TestActionRowDiscovered(t *testing.T) {
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	card := func(v actionCardView) string {
		rec := httptest.NewRecorder()
		if err := r.Fragment(rec, 200, "_action_row", v); err != nil {
			t.Fatalf("Fragment: %v", err)
		}
		return rec.Body.String()
	}
	body := card(actionCardView{
		Discovered: true,
		Action: client.Action{
			ID: "REMOTE-ID", Action: "tom@KERNELKEY/greet",
			Description: "Greets a person by name.", Price: 111,
		},
	})
	if !strings.Contains(body, `class="chip" title="Discovered`) {
		t.Errorf("a discovered card must carry the discovered chip: %s", body)
	}
	if !strings.Contains(body, "Greets a person by name.") {
		t.Errorf("a discovered card must show the description: %s", body)
	}
	// The remote id names nothing here, so it must not become a local link.
	if strings.Contains(body, "/actions/REMOTE-ID") {
		t.Errorf("a discovered card must not link to a local action page: %s", body)
	}
	// The kernel prices every lookup row (v0.12.15), so the card states it — but
	// as INDICATIVE, and marked by an accessible label, not by weight alone: the
	// binding figure is fixed when the action resolves.
	if !strings.Contains(body, "111 credits") {
		t.Errorf("a discovered card must state its catalog price: %s", body)
	}
	if !strings.Contains(body, `class="price muted"`) || !strings.Contains(body, "title=\"Indicative") {
		t.Errorf("an indicative price must be muted AND labelled: %s", body)
	}
	if strings.Contains(body, "stat-strip") {
		t.Errorf("a discovered card must carry no reputation strip: %s", body)
	}
	// The remote owner segment renders unlinked, like every remote ref.
	if strings.Contains(body, `href="/users/tom@KERNELKEY"`) {
		t.Errorf("a remote owner must not link to a local owner page: %s", body)
	}
	// A resolved proxy is the ordinary case and keeps price, strip, and link —
	// the two states never coincide, because the kernel shadows the discovery row.
	proxy := card(actionCardView{Action: client.Action{
		ID: "LOCAL-ID", Action: "k-abc/tom/greet", Kind: "remote_proxy", Price: 14, Active: true,
	}})
	if !strings.Contains(proxy, "/actions/LOCAL-ID") || !strings.Contains(proxy, "stat-strip") {
		t.Errorf("a resolved proxy card must stay an ordinary card: %s", proxy)
	}
	if strings.Contains(proxy, "known network") {
		t.Errorf("a resolved proxy must not read as discovered: %s", proxy)
	}
}

// An action row leads with the tile, then the action's title as its link with
// the chips beside it, and the ref on its own line beneath (§5.5) — a
// discovered row too, whose title comes from the lookup row. The title is the
// one pointer target; the row itself is keyboard-navigable only.
func TestActionRowLeadsWithTitle(t *testing.T) {
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []actionCardView{
		{Action: client.Action{ID: "A1", Action: "tom@play/greet", Title: "Greet a person", Active: true, Visibility: "private"}},
		{Discovered: true, Action: client.Action{ID: "R1", Action: "tom@KERNELKEY/greet", Title: "Greet a person"}},
	} {
		rec := httptest.NewRecorder()
		if err := r.Fragment(rec, 200, "_action_row", v); err != nil {
			t.Fatal(err)
		}
		body := rec.Body.String()
		if !regexp.MustCompile(`<a class="action-title" href="[^"]+" title="Greet a person">Greet a person</a>`).MatchString(body) {
			t.Errorf("the row title must be the action's title, linked: %s", body)
		}
		if !strings.Contains(body, `<p class="action-ref"><span class="ref mono">`) {
			t.Errorf("the ref must sit on its own line beneath the title: %s", body)
		}
		if !regexp.MustCompile(`(?s)<span class="media tile">.*class="action-title"`).MatchString(body) {
			t.Errorf("the row must lead with the square tile: %s", body)
		}
		if !regexp.MustCompile(`(?s)class="action-title"[^>]*>[^<]*</a>\s*<span class="chip".*class="action-ref"`).MatchString(body) {
			t.Errorf("the chips must sit beside the title, above the ref: %s", body)
		}
		if !strings.Contains(body, `class="action-row rowable`) || strings.Contains(body, "onclick") {
			t.Errorf("the row must be keyboard-navigable with no click handler: %s", body)
		}
	}
}

// A peer's action list leads each row with the action's title, the address
// beneath it.
func TestPeerActionsLeadWithTitle(t *testing.T) {
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	if err := r.Fragment(rec, 200, "_peer_actions", []client.PeerAction{{Name: "seller@bee/greet", Title: "Greet a person", Price: 12}}); err != nil {
		t.Fatal(err)
	}
	if body := rec.Body.String(); !strings.Contains(body, `<td>Greet a person<div class="small"><span class="ref mono">`) {
		t.Errorf("a peer action row must lead with its title: %s", body)
	}
}

// A nullable field offers null as its own choice beside its input, the bounds
// the schema states become the browser's, and an optional select can be left
// blank (omitted).
func TestFormFieldsNullableAndBounds(t *testing.T) {
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	fm := BuildForm(json.RawMessage(`{"properties":{
		"n":{"description":"N.","maximum":9,"minimum":1,"type":"integer"},
		"opt":{"description":"O.","enum":["a",null],"type":["string","null"]},
		"s":{"description":"S.","maxLength":5,"title":"Short","type":"string"}},"required":["s"],"type":"object"}`))
	rec := httptest.NewRecorder()
	if err := r.Fragment(rec, 200, "_form_fields", fm); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`<input type="checkbox" name="n_/opt" value="1"> null`,
		`min="1" max="9"`,
		`maxlength="5"`,
		`Short <span class="mono muted small">s</span>`,
		`<option value=""></option>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("form fields missing %q: %s", want, body)
		}
	}
	if strings.Contains(body, `name="n_/s"`) || strings.Contains(body, `name="n_/n"`) {
		t.Error("only a nullable field offers null")
	}
	if strings.Contains(body, `<option value="null"`) {
		t.Error("null is not an enum option; the checkbox carries it")
	}
}

// A cached remote proxy is kernel-managed state: the kernel refuses enable and
// disable on it, so neither the workshop row nor the action page may offer a
// lifecycle verb for one. The peer-level lever is suspend, on network.
func TestProxyRowsOfferNoLifecycleVerbs(t *testing.T) {
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	proxy := client.Action{ID: "P1", Action: "k-abc/tom/greet", Kind: "remote_proxy", Active: true, Price: 14}
	own := client.Action{ID: "A1", Action: "me/thing", Kind: "wasm", Active: true, Price: 3}

	rec := httptest.NewRecorder()
	p := Page{Me: "sys@testnet", IsSuperuser: true, CSRFField: template.HTML("<input>"), Data: actionsView{
		Mine: []actionCardView{{Action: proxy}, {Action: own}}, Superuser: true, Scope: "all",
	}}
	if err := r.Render(rec, 200, "workshop", p); err != nil {
		t.Fatalf("render workshop: %v", err)
	}
	body := rec.Body.String()
	// A mutation names its target in the body, so a proxy offering no verb means
	// no form carries its id.
	if strings.Contains(body, `name="target" value="P1"`) {
		t.Errorf("workshop must offer no lifecycle verb on a proxy row: %s", body)
	}
	// The operator's own action keeps its full menu — the rule is about proxies.
	if !strings.Contains(body, `name="target" value="A1"`) {
		t.Errorf("an ordinary owned action must keep its lifecycle menu: %s", body)
	}

	rec2 := httptest.NewRecorder()
	p2 := Page{Me: "sys@testnet", CSRFField: template.HTML("<input>"), Data: actionView{
		Action: proxy, IsOwner: true, Form: BuildForm(nil),
	}}
	if err := r.Render(rec2, 200, "action_detail", p2); err != nil {
		t.Fatalf("render action_detail: %v", err)
	}
	if d := rec2.Body.String(); strings.Contains(d, "/actions/P1/disable") || strings.Contains(d, ">Manage<") {
		t.Errorf("the action page must withhold Manage for a proxy: %s", d)
	}
}

// The naming primitive (§13 petname model). Three name classes, one rule each:
// a key is global but unreadable, so it renders truncated with the whole value
// recoverable; a petname is bound here and renders whole; a nickname is never
// chosen, because it does not resolve and offering it would invite typing
// something the resolver rejects.
func TestNameOfPrefersPetnameAndTruncatesKeys(t *testing.T) {
	const key = "esKD_l0c_1xaJ9rP-C8woBZamNPvIYFE_FOMSCi4C3I" // 43 chars, as the kernel mints them
	if len(key) != 43 {
		t.Fatalf("fixture key is %d chars, kernel keys are 43", len(key))
	}

	bound := nameOf("daios", key)
	if bound.Text != "daios" || bound.IsKey {
		t.Errorf("a bound petname must render whole: %+v", bound)
	}
	if bound.Title != key {
		t.Errorf("the key must stay recoverable behind the name: %+v", bound)
	}

	unbound := nameOf("", key)
	if !unbound.IsKey || unbound.Text != key[:keyLen]+"…" {
		t.Errorf("an unbound kernel must render a truncated key: %+v", unbound)
	}
	if unbound.Title != key {
		t.Errorf("the full key must survive in the title: %+v", unbound)
	}
	if len(unbound.Text) >= len(key) {
		t.Error("a truncated key must be shorter than the key")
	}

	// A long petname is not key-shaped and must survive whole: a false positive
	// here would silently clip a name the operator chose.
	long := keyDisplay("a-rather-long-but-legitimate-petname")
	if long.IsKey || long.Text != "a-rather-long-but-legitimate-petname" {
		t.Errorf("a long bare name must not be mistaken for a key: %+v", long)
	}
}

func TestRunFormPinsItsQuote(t *testing.T) {
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	cf := runForm(template.HTML(`<input>`), "A1", 12, FormModel{UseRawFallback: true}, nil, false, "lena", "QH1")
	if err := r.Fragment(rec, 200, "_call_form", cf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rec.Body.String(), `name="quote_hash" value="QH1"`) {
		t.Errorf("the run form must carry the quote it was rendered with: %s", rec.Body.String())
	}
	// No hash (an older kernel) carries no field, leaving behaviour unchanged.
	rec2 := httptest.NewRecorder()
	cf2 := runForm(template.HTML(`<input>`), "A1", 12, FormModel{UseRawFallback: true}, nil, false, "lena", "")
	if err := r.Fragment(rec2, 200, "_call_form", cf2); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rec2.Body.String(), "quote_hash") {
		t.Error("no quote means no pin field, not an empty one")
	}
}

// A session awaiting a peer's receipt is not offered a force-end: the kernel
// refuses it (409, verified live on d59742b), and a doomed act is never offered.
// An ordinary open session keeps the control.
func TestWalletWithholdsForceEndWhileAwaitingAReceipt(t *testing.T) {
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	render := func(p client.Process) string {
		rec := httptest.NewRecorder()
		if err := r.Render(rec, 200, "wallet", Page{Me: "me@testnet", Data: walletView{Open: []client.Process{p}}}); err != nil {
			t.Fatal(err)
		}
		return rec.Body.String()
	}
	if !strings.Contains(render(client.Process{ID: "P1", Status: "open", Locked: 5}), "/processes/P1/end") {
		t.Error("an open session is force-endable")
	}
	awaiting := render(client.Process{ID: "P2", Status: "open", Locked: 5, AwaitingReceipt: true})
	if strings.Contains(awaiting, "/processes/P2/end") {
		t.Error("a session awaiting a receipt must not offer force-end")
	}
	if !strings.Contains(awaiting, "awaiting a remote receipt") {
		t.Error("the awaiting state is still stated")
	}
}

// On a world whose money has addresses the pay-in disclosure names the token
// contract money is paid in, and a withdrawal waits for a registered address.
func TestWalletNamesTheTokenOnAnAddressedWorld(t *testing.T) {
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	v := walletView{KernelAddress: "0xvault", Token: "0xtoken"}
	if err := r.Render(rec, 200, "wallet", Page{Me: "me@testnet", Data: v}); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	for _, want := range []string{"0xvault", "0xtoken", "form=address"} {
		if !strings.Contains(body, want) {
			t.Errorf("wallet missing %q", want)
		}
	}
	if strings.Contains(body, "form=withdraw") {
		t.Error("with addresses, a withdrawal needs a registered one first")
	}
}

// What other kernels report about an action renders on its page, each under
// its issuer and the provider's own report first — beside, never inside, this
// kernel's own reputation strip (kernel §D16, U39).
func TestActionPageRendersOthersReports(t *testing.T) {
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	a := client.Action{ID: "P", Action: "seller@k-4lnq/greet", Kind: "remote_proxy", Active: true, Evidence: &client.ActionEvidence{
		LocalExperience:  &client.Stats{Uses: 2, Successes: 1},
		ProviderReported: &client.Evidence{IssuerPublicKey: "PROVIDERKEY0000", SubjectActionID: "R1", Uses: 1},
		ObservedByOthers: []client.Evidence{{IssuerPublicKey: "OTHERKEY0000000", SubjectActionID: "R1", Uses: 3, CorroboratedUses: 2, Contradictions: 1}},
	}}
	rec := httptest.NewRecorder()
	if err := r.Render(rec, 200, "action_detail", Page{Me: "me@testnet", Data: actionView{Action: a}}); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	ev := body[strings.Index(body, "<h2>Evidence</h2>"):]
	if strings.Index(ev, "reported by the provider") > strings.Index(ev, "issued by") {
		t.Error("the provider's own report comes first")
	}
	for _, want := range []string{"reported by the provider", "issued by", "OTHERKEY0000", "contradicted", "corroborated"} {
		if !strings.Contains(ev, want) {
			t.Errorf("evidence missing %q", want)
		}
	}
	// An action no one else has reported on has no section at all.
	rec = httptest.NewRecorder()
	a.Evidence = &client.ActionEvidence{LocalExperience: &client.Stats{}}
	if err := r.Render(rec, 200, "action_detail", Page{Me: "me@testnet", Data: actionView{Action: a}}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rec.Body.String(), "<h2>Evidence</h2>") {
		t.Error("no reports, no evidence section")
	}
}

// The headline money figures are one component, one block per figure: the
// label, the mono amount coloured and signed by its signal, and a link to the
// records behind it only when there are any.
func TestFiguresAreBlocks(t *testing.T) {
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	if err := r.Fragment(rec, 200, "_figures", figures(
		figure(5, "available", ""),
		figure(1, "parked ◷", "escrow"),
		linkedFigure(2, "this month", "credited", "full ledger", "/transactions"),
	)); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `<div class="stat-row">`) || strings.Count(body, `<div class="stat">`) != 3 {
		t.Errorf("each figure must be a block in one row: %s", body)
	}
	for _, want := range []string{
		`<div class="label">available</div>`,
		`<div class="value">5 credits</div>`,
		`<div class="value sig-escrow">1 credits</div>`,
		`<div class="value sig-credited">+2 credits</div>`,
		`<a href="/transactions">full ledger →</a>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("figures must render %q: %s", want, body)
		}
	}
	if strings.Count(body, "<a ") != 1 {
		t.Errorf("only a figure with records behind it links: %s", body)
	}
}

// A task the viewer may also decline renders Decline in the completion form's
// own command row, after Complete, submitting the separate decline form by its
// id; the decline form carries the plain confirm and sits outside the call form.
func TestCompleteFormCarriesDeclineInItsRow(t *testing.T) {
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	cf := completeForm(template.HTML(`<input>`), "T7", 0, FormModel{}, true, nil, nil, false, 0, "", true)
	if err := r.Fragment(rec, 200, "_call_form", cf); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	if !regexp.MustCompile(`(?s)<div class="cluster">\s*<button class="btn" type="submit">Complete</button>\s*<button class="btn-danger" type="submit" form="decline-T7">Decline</button>`).MatchString(body) {
		t.Errorf("Decline must follow Complete in one row: %s", body)
	}
	if !regexp.MustCompile(`(?s)</form>\s*<form id="decline-T7" method="post" action="/inbox/T7/cancel" data-confirm="Decline this task\?">`).MatchString(body) {
		t.Errorf("the decline form must follow the call form, with a plain confirm: %s", body)
	}
	rec = httptest.NewRecorder()
	if err := r.Fragment(rec, 200, "_call_form", completeForm(template.HTML(`<input>`), "T8", 0, FormModel{}, true, nil, nil, false, 0, "", false)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rec.Body.String(), "decline") {
		t.Errorf("no Decline without the right to decline: %s", rec.Body.String())
	}
}

// The run form renders the schema's tree: a nested object as a group under its
// legend, an array as rows that name their path (a nested array its own), a map
// node as an empty JSON editor with the shape as placeholder, a default as a
// placeholder and never a value, and only grammar buttons.
func TestFormFieldsRenderTheTree(t *testing.T) {
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	fm := BuildForm(json.RawMessage(`{"type":"object","properties":{
		"msgs":{"type":"array","title":"Messages","description":"M.","items":{"type":"object","properties":{
			"role":{"type":"string","default":"user"},
			"tags":{"type":"array","items":{"type":"string"}}}}},
		"meta":{"type":"object","additionalProperties":{"type":"string"}}}}`))
	rec := httptest.NewRecorder()
	if err := r.Fragment(rec, 200, "_form_fields", fm); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`<fieldset class="field group">`, `<legend>Messages <span class="mono muted small">msgs</span></legend>`,
		`<input type="hidden" name="t_/msgs" value="array">`, `<div class="rows" data-rows="/msgs">`, `data-row>`,
		`<input type="hidden" name="t_/msgs/0" value="object">`,
		`name="f_/msgs/0/role" rows="1" placeholder="user"></textarea>`,
		`<div class="rows" data-rows="/msgs/0/tags">`, `name="f_/msgs/0/tags/0"`,
		`class="btn-icon" data-row-remove aria-label="Remove"><svg class="ico" aria-hidden="true"><use href="/static/icons.svg#x-mark"/></svg></button>`,
		`class="btn-icon" data-row-add aria-label="Add"><svg class="ico" aria-hidden="true"><use href="/static/icons.svg#plus"/></svg></button>`,
		`name="f_/meta" placeholder="{}" rows="1"></textarea>`, `<input type="hidden" name="t_/meta" value="object">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("form missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, `>user</textarea>`) {
		t.Error("a default is a placeholder, never a value")
	}
}

// The frozen form: a result in the form's own anatomy, read-only — labels over
// boxes, a nested object a group under its legend, an array its rows, the words
// for null and booleans, a long string folded, nothing submittable, the JSON
// behind a disclosure, and a Download only where a transaction exists.
func TestValueBlockRendersAFrozenForm(t *testing.T) {
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	render := func(raw, download string) string {
		rec := httptest.NewRecorder()
		if err := r.Fragment(rec, 200, "_value_block", valueBlock(json.RawMessage(raw), download)); err != nil {
			t.Fatal(err)
		}
		return rec.Body.String()
	}
	long := strings.Repeat("x", longText+1)
	body := render(`{"z":"a\nb","list":[1,true,null],"empty":{},"long":"`+long+`","n":{"k":"<i>"}}`, "/transactions/T1/result.json")
	for _, want := range []string{
		`<label>z</label>`, `<div class="box">a` + "\n" + `b</div>`,
		`<legend>list</legend>`, `<div class="rows">`, `<div class="box">1</div>`, `<div class="box">true</div>`, `<div class="box muted">null</div>`,
		`<label>empty</label>`, `<div class="box muted">{}</div>`,
		`<details class="frozen-fold"><summary>` + strings.Repeat("x", 80) + `…</summary><div class="box">` + long + `</div></details>`,
		`<legend>n</legend>`, `<label>k</label>`, `<div class="box">&lt;i&gt;</div>`,
		`<div class="frozen" data-json><button type="button" class="json-toggle" data-json-toggle aria-pressed="false">JSON</button><div data-fields>`,
		`<div data-raw hidden><pre class="json">`,
		`<a href="/transactions/T1/result.json" download>Download</a>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("frozen form missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "<input") || strings.Contains(body, "data-row") {
		t.Errorf("a frozen form has no control and no row mechanism: %s", body)
	}
	if strings.Index(body, `<label>z</label>`) > strings.Index(body, `<legend>list</legend>`) {
		t.Error("keys keep the order served")
	}
	if b := render(`"plain"`, ""); strings.Contains(b, "Download") || strings.Contains(b, "<label") || !strings.Contains(b, `<div class="box">plain</div>`) {
		t.Errorf("a scalar result is one label-less box with no download: %s", b)
	}
	if b := render(``, ""); !strings.Contains(b, `<pre class="json"></pre>`) {
		t.Errorf("an empty value renders raw: %s", b)
	}
}

func TestResultDownload(t *testing.T) {
	if got := resultDownload("T1"); got != "/transactions/T1/result.json" {
		t.Errorf("download = %q", got)
	}
	if resultDownload("") != "" {
		t.Error("no transaction, no download")
	}
}
