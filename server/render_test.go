package server

import (
	"encoding/json"
	"html/template"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
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

// An app's template builds a form with sealed arguments through taskForm: the
// sealed property is not a field, the rest is.
func TestTaskFormHelper(t *testing.T) {
	app := fstest.MapFS{"templates/_t.html": {Data: []byte(`{{define "_t"}}{{template "_form_fields" (taskForm .S .L).Editable}}{{end}}`)}}
	r, err := NewRenderer(playKernel, app)
	if err != nil {
		t.Fatal(err)
	}
	out, err := r.HTML("_t", map[string]json.RawMessage{
		"S": json.RawMessage(`{"type":"object","properties":{"message":{"type":"string"},"thread_id":{"type":"string"}}}`),
		"L": json.RawMessage(`{"thread_id":"r"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `name="f_/message"`) || strings.Contains(string(out), "thread_id") {
		t.Errorf("form = %s", out)
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

// The sidebar is the conversations rail and the avatar, whose menu holds the
// account, the registry's destinations with the inbox count, and Sign out —
// and no opener, which is the launcher's.
func TestSidebarHoldsRailAndMenu(t *testing.T) {
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	data := Page{Me: "sys@testnet", Rail: template.HTML(`<nav class="rail">ROOMS</nav>`), Commands: []Command{
		{Slug: "inbox", Name: "Inbox", Href: "/inbox", Nav: true, Live: true},
		{Slug: "admin", Name: "Admin", Href: "/admin", Icon: iconAdmin, Nav: true, Active: true},
		{Name: "Record a payment", Href: "/admin?form=admin-deposit"},
	}}
	rec := httptest.NewRecorder()
	if err := r.Fragment(rec, 200, "_sidebar", data); err != nil {
		t.Fatal(err)
	}
	got := rec.Body.String()
	for _, want := range []string{`<nav class="rail">ROOMS</nav>`, `>sys@testnet</div>`, `class="menu-caption money"`,
		`<a href="/inbox" class="menu-item" role="menuitem">Inbox<span class="badge badge-inbox"`,
		`<a href="/admin" class="menu-item" role="menuitem" aria-current="page">` + string(iconAdmin) + `Admin</a>`, `Sign out`} {
		if !strings.Contains(got, want) {
			t.Errorf("sidebar lacks %q: %s", want, got)
		}
	}
	if strings.Contains(got, "Record a payment") {
		t.Error("an opener is the launcher's, never a menu row")
	}
	if strings.Count(got, `class="badge badge-inbox"`) != 2 {
		t.Error("the task badge rides the avatar and the Inbox row")
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
	cf := r.runForm(actionView{CSRFField: template.HTML(`<input>`), Action: client.Action{ID: "A1", Action: "alice@testnet/x", Price: 12, QuoteHash: "QH"},
		Form: FormModel{UseRawFallback: true}, FirstPayee: true})
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
	cf := completeForm(template.HTML(`<input>`), "S1", 5, FormModel{UseRawFallback: true}, false, json.RawMessage(`{"to":"bob"}`), nil, false)
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
	if !strings.Contains(body, `class="view locked"`) || !strings.Contains(body, "<label>to</label>") || !strings.Contains(body, `<div class="box">bob</div>`) {
		t.Errorf("locked args must render as the view: %s", body)
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
		if err := r.Fragment(rec, 200, "_call_form", completeForm(template.HTML(`<input>`), "S1", 0, fm, approval, json.RawMessage(`{"to":"bob"}`), nil, false)); err != nil {
			t.Fatal(err)
		}
		return rec.Body.String()
	}
	body := render(FormModel{Fields: []Field{{Name: "to", Type: "string", Path: "/to"}}}, false)
	toggle := `<button type="button" class="switch" role="switch" aria-checked="false" data-json-switch>JSON</button>`
	if !strings.Contains(body, toggle) || !strings.Contains(body, `name="has_fields"`) || !strings.Contains(body, `data-json `) {
		t.Errorf("structured form must carry the JSON toggle and the has_fields marker: %s", body)
	}
	if strings.Index(body, toggle) > strings.Index(body, `class="view locked"`) {
		t.Error("the toggle sits at the top of the block, before the sealed part")
	}
	if strings.Contains(body, "data-raw-seg") || strings.Contains(body, "field-switch") || strings.Contains(body, "aria-pressed") {
		t.Error("the retired two-segment switch and the tab-like toggle must be gone")
	}
	for _, b := range []string{render(FormModel{UseRawFallback: true}, false), render(FormModel{}, true)} {
		if strings.Contains(b, "data-json-switch") {
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
	cf := completeForm(template.HTML(`<input>`), "S9", 1, FormModel{UseRawFallback: true}, true, json.RawMessage(`{"message":"hi"}`), nil, false)
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
	if sep := strings.Index(body, `<hr class="card-sep">`); sep < 0 || sep < strings.Index(body, `data-json-switch`) || sep > strings.Index(body, "/transactions/tx1/rate") {
		t.Errorf("a rule separates the result from its receipt and rating: %s", body)
	}
}

// TestLayoutRendersTheHeartbeat proves a signed-in page carries the two tick
// elements — the fast lane and the slow lane on the configured interval — one
// slow watch per live registry entry, once, and the inbox badge on the
// avatar, the menu row and the topbar's trigger, with no poller of its own.
func TestLayoutRendersTheHeartbeat(t *testing.T) {
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	p := Page{
		Me:        "me@testnet",
		BadgeSecs: 15,
		Commands:  []Command{{Slug: "inbox", Name: "Inbox", Href: "/inbox", Nav: true, Live: true}},
		Data:      struct{ Message string }{"x"},
	}
	if err := r.Render(rec, 200, "error", p); err != nil {
		t.Fatalf("render: %v", err)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`hx-get="/tick" hx-trigger="every 5s" hx-include="input[name=watch]" hx-swap="none"`,
		`hx-get="/tick" hx-trigger="every 15s" hx-include="input[name=slow]" hx-swap="none"`,
	} {
		if strings.Count(body, want) != 1 {
			t.Errorf("want one %q: %s", want, body)
		}
	}
	if strings.Count(body, `id="watch-inbox" name="slow" value="inbox:badge:"`) != 1 {
		t.Errorf("one slow watch per live entry: %s", body)
	}
	if strings.Count(body, `class="badge badge-inbox"`) != 3 {
		t.Errorf("the inbox badge stands on the avatar, the menu row and the trigger: %s", body)
	}
	for _, gone := range []string{`hx-get="/inbox/badge"`, `hx-get="/health"`, `hx-trigger="load`} {
		if strings.Contains(body, gone) {
			t.Errorf("no poller of its own: %q in %s", gone, body)
		}
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
		Action: client.Action{ID: "a1", Action: "peerx/greet", Name: "greet", Kind: "remote_proxy", Active: true, Visibility: "public", Price: 10},
		Form:   BuildForm(nil),
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
			Process: client.Process{ID: "p1", Status: "open", AwaitingReceipt: awaiting, AwaitingReceiptSince: "2026-07-12T00:00:00Z"},
		}}
		if err := r.Render(rec, 200, "process", p); err != nil {
			t.Fatalf("render awaiting=%v: %v", awaiting, err)
		}
		return rec.Body.String()
	}
	parked := render(true)
	if !strings.Contains(parked, `name="slow" value="process:p1:awaiting"`) || strings.Contains(parked, `hx-get="/processes/p1"`) {
		t.Errorf("awaiting-receipt banner must carry its slow watch and no poll of its own: %s", parked)
	}
	if !strings.Contains(parked, "Awaiting remote receipt") {
		t.Errorf("awaiting-receipt banner text missing: %s", parked)
	}
	settled := render(false)
	if strings.Contains(settled, `value="process:p1:awaiting"`) {
		t.Error("settled process must NOT carry the awaiting-receipt watch")
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

// renderPeersPage renders the network page for this kernel ("me", key PK) and
// the given roster, through the handler's own row builder.
func renderPeersPage(t *testing.T, peers []client.Peer, showSuspended bool) string {
	t.Helper()
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	id := client.Identity{Handle: "me", PublicKey: "PK"}
	rec := httptest.NewRecorder()
	p := Page{Me: "sys@testnet", IsSuperuser: true, BadgeSecs: 15, CSRFField: template.HTML("<input>"), Data: adminPeersView{
		Identity: id, Kernels: kernelRows(id, peers), ShowSuspended: showSuspended,
	}}
	if err := r.Render(rec, 200, "admin_peers", p); err != nil {
		t.Fatalf("render: %v", err)
	}
	return rec.Body.String()
}

// The network page is one list in the action row's grammar: this kernel, then
// its counterparties, then the known network, each class in the kernel's own
// order and marked by a pill; a known kernel carries no mark (§6.6). It is
// never polled, and the Show/Hide-suspended toggle drives ?all=1.
func TestKernelListOrderAndPills(t *testing.T) {
	body := renderPeersPage(t, []client.Peer{
		{Petname: "ka", PublicKey: "KA"},
		{Petname: "pb", PublicKey: "KB", Traded: true},
		{Petname: "kc", PublicKey: "KC"},
		{Petname: "pd", PublicKey: "KD", Traded: true},
	}, false)
	last := -1
	for _, name := range []string{">me<", ">pb<", ">pd<", ">ka<", ">kc<"} {
		i := strings.Index(body, name)
		if i <= last {
			t.Fatalf("rows must run current, counterparties, known in kernel order; %q out of place: %s", name, body)
		}
		last = i
	}
	if n := strings.Count(body, `<span class="pill">this kernel</span>`); n != 1 {
		t.Errorf("exactly one this-kernel pill, got %d", n)
	}
	if n := strings.Count(body, `<span class="pill">party</span>`); n != 2 {
		t.Errorf("one party pill per counterparty, got %d", n)
	}
	if strings.Contains(body, `class="tabs`) || strings.Contains(body, `title="Discovered`) {
		t.Errorf("one list, no tabs and no discovered chip: %s", body)
	}
	// This kernel's title is its own handle, its key on hover; its row is this
	// page, so it links nowhere.
	if !strings.Contains(body, `<span class="action-title mono" title="PK">me</span>`) || strings.Contains(body, `data-href="/admin/peers/PK"`) {
		t.Errorf("the current row must title its handle and link nowhere: %s", body)
	}
	if strings.Contains(body, `hx-get="/admin/peers`) {
		t.Errorf("the list must not poll itself: %s", body)
	}
	if !strings.Contains(body, `href="/admin/peers?all=1"`) || !strings.Contains(body, "Show suspended") {
		t.Errorf("the list must offer a Show-suspended toggle: %s", body)
	}
	if !strings.Contains(renderPeersPage(t, nil, true), "Hide suspended") {
		t.Error("the suspended view must show a Hide toggle")
	}
}

// This kernel's money is the page's headline money, so it renders through the
// one figures component; a solvency gap carries a danger pill only when it is
// non-zero (§6.6).
func TestNetworkFiguresAndGap(t *testing.T) {
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	render := func(gap int64) string {
		id := client.Identity{Handle: "me", PublicKey: "PK", Exposure: 7, CreditLimit: 900, Solvency: client.Solvency{Gap: gap}}
		rec := httptest.NewRecorder()
		p := Page{Me: "sys@testnet", IsSuperuser: true, CSRFField: template.HTML("<input>"), Data: adminPeersView{Identity: id, Kernels: kernelRows(id, nil)}}
		if err := r.Render(rec, 200, "admin_peers", p); err != nil {
			t.Fatalf("render: %v", err)
		}
		return rec.Body.String()
	}
	body := render(0)
	for _, want := range []string{`class="stat-row"`, ">owed to us<", ">credit limit<"} {
		if !strings.Contains(body, want) {
			t.Errorf("figures missing %q: %s", want, body)
		}
	}
	if strings.Contains(body, `status-danger">gap`) {
		t.Error("a zero gap must not render")
	}
	if !strings.Contains(render(3), `<span class="pill status-danger">gap `) {
		t.Error("a non-zero gap must carry a danger pill")
	}
}

// A named peer row is a results row: the whole row opens the peer's page, where
// it is named and suspended, and the row itself carries no verb (§5.5).
func TestNamedRowsCarryNoVerbs(t *testing.T) {
	body := renderPeersPage(t, []client.Peer{
		{Petname: "active", PublicKey: "KF", Traded: true},
		{Petname: "frozen", PublicKey: "KG", Traded: true, SuspendedAt: "2026-07-19T17:50:24Z"},
		{Nickname: "twin", PublicKey: "KT1", Actions: 13, LastSeen: "2026-08-06T00:04:34Z"},
	}, true)
	for _, key := range []string{"KF", "KG", "KT1"} {
		if !strings.Contains(body, `class="action-row rowable`) || !strings.Contains(body, `data-href="/admin/peers/`+key+`"`) {
			t.Errorf("row %s must open the peer's page: %s", key, body)
		}
	}
	for _, gone := range []string{"/admin/peers/suspend", "/admin/peers/unsuspend", "?form=admin-rename", "unsubscribe", "fund_key"} {
		if strings.Contains(body, gone) {
			t.Errorf("a row carries no verb; found %q", gone)
		}
	}
	if !strings.Contains(body, `class="action-row rowable inactive"`) || !strings.Contains(body, ">suspended<") {
		t.Errorf("a suspended peer is dimmed and marked: %s", body)
	}
	// A known kernel states its catalog; last_seen is catalog freshness, not
	// liveness: a strip slot with a tooltip, never a status pill.
	if !strings.Contains(body, `#layers"/></svg> 13<`) || !strings.Contains(body, `title="Catalog verified`) || strings.Contains(body, "status-credited") {
		t.Errorf("a known row states its catalog and contact cache in the strip: %s", body)
	}
}

// The key renders in one spelling (§6.6): keyDisplay as the title when unbound,
// else on the ref line; never both, and never a second truncation.
func TestKeyRendersInOneSpelling(t *testing.T) {
	const key = "lHynnkVtXrzrfAHCPgrjzctyjQqkehrUz0BZp7Pxnmw"
	short := keyDisplay(key).Text
	unbound := renderPeersPage(t, []client.Peer{{Nickname: "TitanPlus", PublicKey: key}}, false)
	if n := strings.Count(unbound, short); n != 1 {
		t.Errorf("an unbound row spells its key once, got %d: %s", n, unbound)
	}
	if strings.Contains(unbound, key[:16]+"<") || strings.Contains(unbound, `class="action-ref fingerprint" title="`+key+`"`) {
		t.Errorf("an unbound row has no ref line: %s", unbound)
	}
	bound := renderPeersPage(t, []client.Peer{{Petname: "titan", PublicKey: key}}, false)
	if !strings.Contains(bound, `<p class="action-ref fingerprint" title="`+key+`">`+short+` <button`) {
		t.Errorf("a bound row's ref line is the one key spelling: %s", bound)
	}
	// The whole key is yielded by the copy control, in both forms of the row.
	for _, body := range []string{unbound, bound} {
		if strings.Count(body, `data-copy="`+key+`"`) != 1 {
			t.Errorf("a row carries its whole key in one copy control: %s", body)
		}
	}
}

// Every peer row ends with the kernel strip: three slots in one order, a muted
// none where a slot has nothing, the separator between slots and never before
// the first; this kernel's row, built from identity, carries none.
func TestKernelStripSlots(t *testing.T) {
	body := renderPeersPage(t, []client.Peer{
		{Petname: "pb", PublicKey: "KB", Traded: true, Actions: 4, LastSeen: "2026-08-04T09:54:01Z", LastContactFailedAt: "2026-08-04T10:00:00Z"},
		{Petname: "kc", PublicKey: "KC", Actions: 0},
	}, false)
	if n := strings.Count(body, `class="stat-strip"`); n != 2 {
		t.Fatalf("one strip per peer row and none on this kernel's, got %d: %s", n, body)
	}
	last := -1
	for _, slot := range []string{`title="Public actions in its catalog"`, `title="Catalog verified`, `title="Last contact failed"`, `title="Public actions in its catalog"`, `title="Catalog never verified"`, `title="No contact has failed"`} {
		i := strings.Index(body[last+1:], slot)
		if i < 0 {
			t.Fatalf("slot %s missing or out of order: %s", slot, body)
		}
		last += 1 + i
	}
	if !strings.Contains(body, `<span class="muted" title="Catalog never verified">`) {
		t.Error("an empty slot renders a muted none")
	}
	if strings.Contains(body, `"stat-strip">
  ·`) || strings.Contains(body, "catalog verified") {
		t.Errorf("no leading separator and no prose facts: %s", body)
	}
}

// Naming is offered where the gap is visible: a row titled by its key carries
// the one verb, the same .btn as on the kernel's page, into its page with the
// rename modal open; a named row and this kernel's row carry nothing (§6.6).
func TestNameOpenerOnlyOnUnboundRows(t *testing.T) {
	const key = "X7lFapV-gKReFfvyDdaHO0pZrfSyhb9aA-CS_DIWvp8"
	body := renderPeersPage(t, []client.Peer{
		{Nickname: "tk", PublicKey: key},
		{Petname: "pb", PublicKey: "KB", Traded: true},
	}, false)
	want := `<a class="btn" href="/admin/peers/` + key + `?form=admin-rename">Name</a>`
	if n, m := strings.Count(body, want), strings.Count(body, "?form=admin-rename"); n != 1 || m != 1 {
		t.Errorf("exactly one name opener, on the unbound row: %d/%d: %s", n, m, body)
	}
}

// The kernel page states its facts with labels, present ones only: status
// once, version without the agent prefix, the roster's catalog count (the
// inspection lists one page, so the list says N of M shown), the provider's
// pooled report as Self-reported, and the date the kernel became known here;
// no row reads none, no dash, no protocol strings, no strip; the verbs stand
// in the card's stack (§6.6).
func TestInspectStatesLabelledFacts(t *testing.T) {
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	render := func(i client.Inspection, rec client.PeerRecord, catalog int) string {
		rec2 := httptest.NewRecorder()
		p := Page{Me: "sys@testnet", IsSuperuser: true, BadgeSecs: 15, CSRFField: template.HTML("<input>"), Data: adminInspectView{
			Key: "KEYC", Inspection: i, Report: poolEvidence(i.SelfEvidence()), Record: rec, Catalog: catalog,
			Actions: peerActionRows(i), Version: strings.TrimPrefix(i.Reachability.Version, "juice-kernel/"),
		}}
		if err := r.Render(rec2, 200, "admin_inspect", p); err != nil {
			t.Fatalf("render: %v", err)
		}
		return rec2.Body.String()
	}
	live := render(client.Inspection{PublicKey: "KEYC", Online: true, Source: "live",
		Actions:      []client.PeerAction{{ActionID: "a1"}, {ActionID: "a2"}},
		Reachability: client.Reachability{Path: "direct", RTTMillis: 12, Version: "juice-kernel/v0.15.10"},
		Evidence: []client.Evidence{
			{IssuerPublicKey: "KEYC", Uses: 6, Successes: 6, RatingCount: 1, RatingMean: 1},
			{IssuerPublicKey: "KEYC", Uses: 4, Successes: 2, RatingCount: 9, RatingMean: 0.5},
			{IssuerPublicKey: "OTHER", Uses: 100, Successes: 0},
		}}, client.PeerRecord{FirstSeen: "2026-09-23T13:38:52Z"}, 193)
	for _, want := range []string{`<dt>Status</dt>`, `>direct · 12 ms<`, `<dt>Version</dt><dd class="mono">v0.15.10</dd>`, `<dt>Catalog</dt><dd>193 public actions</dd>`, `<dt>Self-reported</dt><dd>80% success over 10 runs · rating 55% (10 raters)</dd>`, `<dt>Known since</dt><dd>2026-09-23</dd>`, `<p class="muted small">2 of 193 shown</p>`, `<div class="card stack">`} {
		if !strings.Contains(live, want) {
			t.Errorf("kernel page missing %q: %s", want, live)
		}
	}
	if n := strings.Count(live, "direct"); n != 1 {
		t.Errorf("reachability is stated once, got %d", n)
	}
	for _, gone := range []string{`class="stat-strip"`, "Petname</dt>", "Protocols", "/juice/fed/gossip/1", ">none<", ">—<", "juice-kernel/", "13:38", "<dt>Reported</dt>"} {
		if strings.Contains(live, gone) {
			t.Errorf("kernel page must not render %q", gone)
		}
	}
	quiet := render(client.Inspection{PublicKey: "KEYC", Reachability: client.Reachability{Path: "unreachable"}}, client.PeerRecord{}, 0)
	for _, gone := range []string{"<dt>Version</dt>", "<dt>Catalog</dt>", "<dt>Self-reported</dt>", "<dt>Known since</dt>", " ms<", "shown</p>"} {
		if strings.Contains(quiet, gone) {
			t.Errorf("an absent fact has no row: %q", gone)
		}
	}
}

func TestPoolEvidence(t *testing.T) {
	r := poolEvidence([]client.Evidence{
		{Uses: 6, Successes: 6, RatingCount: 1, RatingMean: 1},
		{Uses: 4, Successes: 2, RatingCount: 9, RatingMean: 0.5},
	})
	if r.Uses != 10 || r.Successes != 8 || r.RatingCount != 10 || r.RatingMean != 0.55 {
		t.Errorf("pooled = %+v; want 10/8, 10 raters at 0.55 (count-weighted, not 0.75)", r)
	}
	if r := poolEvidence(nil); r.RatingCount != 0 || r.RatingMean != 0 {
		t.Errorf("no rows pool to nothing, got %+v", r)
	}
}

// The prefill is the kernel's own auto-bind preference: petname > free
// nickname > k-<key8>, suffixed 2, 3, … to the lowest free number.
func TestPetnameSuggestion(t *testing.T) {
	const key = "X7lFapV-gKReFfvyDdaHO0pZrfSyhb9aA-CS_DIWvp8"
	taken := func(names ...string) func(string) bool {
		set := map[string]bool{}
		for _, n := range names {
			set[n] = true
		}
		return func(n string) bool { return set[n] }
	}
	cases := []struct {
		petname, nickname string
		taken             func(string) bool
		want              string
	}{
		{"arfante", "arfante", taken("arfante"), "arfante"},
		{"", "TitanPlus", taken(), "TitanPlus"},
		{"", "TitanPlus", taken("TitanPlus"), "TitanPlus2"},
		{"", "TitanPlus", taken("TitanPlus", "TitanPlus2"), "TitanPlus3"},
		{"", "TitanPlus", taken("TitanPlus", "TitanPlus3"), "TitanPlus2"},
		{"", "behemoth", taken("behemoth"), "behemoth2"},
		{"", key, taken(), "k-X7lFapV-"},
		{"", "", taken("k-X7lFapV-"), "k-X7lFapV-2"},
	}
	for _, c := range cases {
		if got := petnameSuggestion(c.petname, c.nickname, key, c.taken); got != c.want {
			t.Errorf("suggestion(%q, %q) = %q, want %q", c.petname, c.nickname, got, c.want)
		}
	}
}

// A peer holds no account here: suspension freezes the kernel row, so every
// kernel's page offers Suspend, or Unsuspend when the row is frozen, posting
// the KEY; the party pill is the roster's class (§6.6).
func TestInspectOffersSuspendOnEveryKernel(t *testing.T) {
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	render := func(traded bool, rec client.PeerRecord) string {
		rec2 := httptest.NewRecorder()
		p := Page{Me: "sys@testnet", IsSuperuser: true, BadgeSecs: 15, CSRFField: template.HTML("<input>"), Data: adminInspectView{
			Key: "KEYC", Inspection: client.Inspection{Petname: "kernelc", Source: "live"}, Traded: traded, Record: rec,
		}}
		if err := r.Render(rec2, 200, "admin_inspect", p); err != nil {
			t.Fatalf("render: %v", err)
		}
		return rec2.Body.String()
	}
	party := render(true, client.PeerRecord{FirstSeen: "2026-09-14T01:13:39Z"})
	if !strings.Contains(party, `action="/admin/peers/suspend"`) || !strings.Contains(party, `value="KEYC"`) || !strings.Contains(party, `<span class="pill">party</span>`) {
		t.Errorf("a counterparty's page must mark it and offer Suspend by key: %s", party)
	}
	frozen := render(true, client.PeerRecord{SuspendedAt: "2026-08-01T00:00:00Z"})
	if !strings.Contains(frozen, `action="/admin/peers/unsuspend"`) || !strings.Contains(frozen, `<span class="pill status-danger">suspended</span>`) {
		t.Errorf("a suspended kernel's page must say so and offer Unsuspend: %s", frozen)
	}
	known := render(false, client.PeerRecord{FirstSeen: "2026-09-14T01:13:39Z"})
	if !strings.Contains(known, `action="/admin/peers/suspend"`) || strings.Contains(known, ">party<") {
		t.Errorf("a discovered kernel is not a party but can be suspended: %s", known)
	}
	if strings.Contains(party+frozen+known, "account") {
		t.Error("a peer holds no account here; the page must not say one")
	}
}

// TestInspectStatusIsWatched: the inspect status card carries its slow watch and
// no poll of its own, so an open inspect view is a live monitor by the heartbeat.
func TestInspectStatusIsWatched(t *testing.T) {
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
	if !strings.Contains(body, `id="inspect-status"`) || !strings.Contains(body, `name="slow" value="peer:KEYC"`) || strings.Contains(body, `hx-get="/admin/peers/KEYC"`) {
		t.Errorf("inspect status card must carry its slow watch and no poll of its own: %s", body)
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
	body := renderPeersPage(t, []client.Peer{{Petname: "peerb", PublicKey: "KEY", Traded: true, LastSeen: "2026-07-12T00:00:00Z"}}, false)
	if !strings.Contains(body, `title="Catalog verified`) {
		t.Errorf("counterparty card missing the contact cache: %s", body)
	}
	for _, gone := range []string{"our credit there", "owes us", "we owe them", "position"} {
		if strings.Contains(body, gone) {
			t.Errorf("a peer card must state no money: found %q", gone)
		}
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

// A narrow screen's drawer must survive JavaScript being off. The trigger is
// a real link to ?drawer=open, which the server stamps on <html> so the
// stylesheet slides the one sidebar in, and the scrim is a link back to the
// page. There is one sidebar: no second copy of its ids in a drawer.
func TestHiddenSidebarWorksWithoutJS(t *testing.T) {
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	render := func(open bool) string {
		rec := httptest.NewRecorder()
		p := Page{Me: "me@testnet", DrawerOpen: open, Path: "/browse",
			Data: struct{ Message string }{"x"}}
		if err := r.Render(rec, 200, "error", p); err != nil {
			t.Fatalf("render: %v", err)
		}
		return rec.Body.String()
	}
	closed, open := render(false), render(true)
	if !strings.Contains(closed, `href="?drawer=open" class="menu-trigger"`) {
		t.Error("the trigger must be a real link for the no-JS path")
	}
	if strings.Contains(closed, `data-drawer="open"`) {
		t.Error("the drawer must not be open without ?drawer=open")
	}
	if !strings.Contains(open, `<html lang="en" data-drawer="open">`) {
		t.Error("?drawer=open must stamp the open drawer on <html>")
	}
	if !strings.Contains(open, `<a class="scrim" href="/browse" data-drawer-close`) {
		t.Error("the scrim must be a real link back to the current path")
	}
	if n := strings.Count(open, `id="profile-popup"`); n != 1 {
		t.Errorf("one sidebar, %d copies of its menu", n)
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
	p := Page{Me: "me@testnet", Themes: ThemeList(), Commands: []Command{{Slug: "browse", Name: "Discover", Href: "/browse", Nav: true}},
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

// A peer's public action is a remote action before resolution, so the inspect
// page lists it as the discovered row of the search results: title, R chip,
// indicative price, address, and a link that resolves it (§6.2).
func TestInspectListsActionsAsDiscoveredRows(t *testing.T) {
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	i := client.Inspection{PublicKey: "KEYC", Actions: []client.PeerAction{{ActionID: "a2", Name: "seller@bee/greet", Title: "Greet a person", Description: "says hi", Price: 12, Indicative: true}}}
	rec := httptest.NewRecorder()
	p := Page{Me: "sys@testnet", IsSuperuser: true, BadgeSecs: 15, CSRFField: template.HTML("<input>"), Data: adminInspectView{
		Key: "KEYC", Inspection: i, Actions: peerActionRows(i),
	}}
	if err := r.Render(rec, 200, "admin_inspect", p); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	for _, want := range []string{`<div class="action-list">`, `<a class="action-title" href="/discovered?ref=seller%40bee%2fgreet" title="Greet a person">Greet a person</a>`, `title="Discovered — known through the network, not resolved here">R</span>`, `<span class="owner">seller@bee</span>/greet`, `<p class="action-desc">says hi</p>`} {
		if !strings.Contains(body, want) {
			t.Errorf("inspect actions missing %q: %s", want, body)
		}
	}
	if strings.Contains(body, "stats-table") || strings.Contains(body, "inactive") {
		t.Errorf("no table and no dimming: %s", body)
	}
	// The verbs are buttons in the status card, after the facts, as on the
	// user page.
	if !strings.Contains(body, `<a class="btn" href="/admin/peers/KEYC?form=admin-rename" data-modal="form-admin-rename">Name</a>`) {
		t.Errorf("Name is a .btn in the card: %s", body)
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

// A required select leads with a blank choice until it holds a value, so a row
// left untouched submits nothing: a select preselecting its first option would
// send a value nobody chose.
func TestRequiredSelectIsBlankUntilFilled(t *testing.T) {
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	fm := BuildForm(json.RawMessage(`{"properties":{
		"kind":{"description":"K.","enum":["chat","embed"],"type":"string"},
		"on":{"description":"O.","type":"boolean"}},"required":["kind","on"],"type":"object"}`))
	render := func() string {
		rec := httptest.NewRecorder()
		if err := r.Fragment(rec, 200, "_form_fields", fm); err != nil {
			t.Fatal(err)
		}
		return rec.Body.String()
	}
	if body := render(); strings.Count(body, `<option value=""></option>`) != 2 {
		t.Errorf("an empty required select must lead with a blank choice: %s", body)
	}
	fm.Fill(json.RawMessage(`{"kind":"embed","on":true}`))
	body := render()
	if strings.Contains(body, `<option value=""></option>`) {
		t.Errorf("a filled required select offers no blank: %s", body)
	}
	if !strings.Contains(body, `<option value="embed" selected>`) || !strings.Contains(body, `<option value="true" selected>`) {
		t.Errorf("a filled select keeps its value: %s", body)
	}
}

// A required container carries r_, so a submit sends it even empty; a filled
// null ticks its box; and a call form names its two JSON switch conversions
// (§6.2).
func TestFormWireForTheSwitch(t *testing.T) {
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	fm := BuildForm(json.RawMessage(`{"type":"object","required":["req"],"properties":{
		"req":{"type":"array","items":{"type":"string"}},"opt":{"type":"array","items":{"type":"string"}},
		"nul":{"type":["string","null"]}}}`))
	fm.Fill(json.RawMessage(`{"nul":null}`))
	cf := r.runForm(actionView{CSRFField: template.HTML(`<input>`), Action: client.Action{ID: "A1", Action: "alice@testnet/x"}, Form: fm})
	rec := httptest.NewRecorder()
	if err := r.Fragment(rec, 200, "_call_form", cf); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`<input type="hidden" name="r_/req" value="1">`,
		`<input type="checkbox" name="n_/nul" value="1" checked> null`,
		`data-to-json="/form/json" data-to-form="/actions/A1/form"`,
		`data-json-error hidden`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("form lacks %q", want)
		}
	}
	if strings.Contains(body, `name="r_/opt"`) {
		t.Error("an optional container carries no r_")
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
	cf := r.runForm(actionView{CSRFField: template.HTML(`<input>`), Action: client.Action{ID: "A1", Action: "lena@testnet/x", Price: 12, QuoteHash: "QH1"}, Form: FormModel{UseRawFallback: true}})
	if err := r.Fragment(rec, 200, "_call_form", cf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rec.Body.String(), `name="quote_hash" value="QH1"`) {
		t.Errorf("the run form must carry the quote it was rendered with: %s", rec.Body.String())
	}
	// No hash (an older kernel) carries no field, leaving behaviour unchanged.
	rec2 := httptest.NewRecorder()
	cf2 := r.runForm(actionView{CSRFField: template.HTML(`<input>`), Action: client.Action{ID: "A1", Action: "lena@testnet/x", Price: 12}, Form: FormModel{UseRawFallback: true}})
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

// One End control serves every page that lists or shows a session: it states a
// refund only when there is one, since ending a session holding nothing still
// releases its waiting tasks.
func TestEndStatesARefundOnlyWhenThereIsOne(t *testing.T) {
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	render := func(p client.Process) string {
		rec := httptest.NewRecorder()
		if err := r.Fragment(rec, 200, "_end", endOf(p, "<input>")); err != nil {
			t.Fatal(err)
		}
		return rec.Body.String()
	}
	if got := render(client.Process{ID: "P1", Status: "open", Available: 3, Locked: 2}); !strings.Contains(got, ">End · refund 5 credits</button>") {
		t.Errorf("a session holding money states its refund: %s", got)
	}
	if got := render(client.Process{ID: "P2", Status: "open"}); !strings.Contains(got, ">End</button>") || strings.Contains(got, "refund") {
		t.Errorf("a session holding nothing offers End alone: %s", got)
	}
	if got := render(client.Process{ID: "P3", Status: "closed", Locked: 2}); strings.Contains(got, "<form") {
		t.Errorf("a closed session offers no End: %s", got)
	}
}

// A blocked payout says why, in the kernel's words, under its status.
func TestWalletStatesWhyAPayoutIsBlocked(t *testing.T) {
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	v := walletView{Withdrawals: []client.Withdrawal{{ID: "W1", Amount: 1, Status: "blocked", Reason: "native currency too low, top up"}}}
	if err := r.Render(rec, 200, "wallet", Page{Me: "me@testnet", Data: v}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rec.Body.String(), `<div class="small muted">native currency too low, top up</div>`) {
		t.Errorf("the block's reason must render under the status: %s", rec.Body.String())
	}
}

// An account's locked balance is parked — escrow — for everyone but the
// operator, whose locked also holds payouts in transit on the rail: the
// kernel's word, and no escrow signal, on every page that shows it (§6.5).
func TestLockedFigureFollowsTheAccount(t *testing.T) {
	if f := lockedFigure(5, false); f.Label != "parked ◷" || f.Signal != "escrow" {
		t.Errorf("a person's locked is parked escrow: %+v", f)
	}
	if f := lockedFigure(5, true); f.Label != "locked ◷" || f.Signal != "" {
		t.Errorf("the operator's locked is locked, unsignalled: %+v", f)
	}
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]any{"wallet": walletView{Superuser: true}, "home": homeView{Superuser: true}, "workshop": actionsView{Superuser: true}} {
		rec := httptest.NewRecorder()
		if err := r.Render(rec, 200, name, Page{Me: "sys@testnet", IsSuperuser: true, Data: data}); err != nil {
			t.Fatal(err)
		}
		if body := rec.Body.String(); !strings.Contains(body, `<div class="label">locked ◷</div>`) || strings.Contains(body, "parked ◷") {
			t.Errorf("%s: the operator's figure reads locked: %s", name, body)
		}
	}
}

// The ledger names money crossing in or out of Juice, and an entry from the
// viewer to the viewer — the operator's, sys being the clearing account —
// moves nothing, so it takes no sign and no direction (§6.5).
func TestActivityNamesTheCrossingAndSignsNoSelfEntry(t *testing.T) {
	if row := entryRowOf(client.LedgerEntry{From: "sys@t", To: "sys@t", Amount: 1, Reason: "I want it back."}, "sys@t"); !row.Self || row.Credited || row.Note != "I want it back." {
		t.Errorf("a self-entry is neither credit nor debit: %+v", row)
	}
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	rows := []entryRow{
		entryRowOf(client.LedgerEntry{From: "sys@t", To: "sys@t", Amount: 1, Reason: "I want it back."}, "sys@t"),
		entryRowOf(client.LedgerEntry{From: "sys@t", Amount: 2, Reason: "fuel"}, "sys@t"),
		entryRowOf(client.LedgerEntry{To: "sys@t", Amount: 3}, "sys@t"),
	}
	if err := r.Fragment(rec, 200, "_activity", rows); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`<td>sys@t</td>`, `<span class="muted">1 credits</span>`,
		`<td>to outside Juice</td>`, `−2 credits`,
		`<td>from outside Juice</td>`, `+3 credits`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("activity missing %q: %s", want, body)
		}
	}
	if strings.Contains(body, "+1 credits") || strings.Contains(body, "−1 credits") {
		t.Errorf("a self-entry carries no sign: %s", body)
	}
}

// The network page states a halt as a warning, led by #alert and spaced from
// the figures, and what the vault held at the last finalized block: the token
// and the native coin, as served (§6.6).
func TestNetworkPageStatesTheHaltAndTheVault(t *testing.T) {
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	id := client.Identity{Handle: "me", PublicKey: "PK", BlockchainAddress: "0xvault",
		Stop:      &client.RailStop{Reason: "native currency too low, top up"},
		Finalized: &client.RailHoldings{Token: 4, Gas: "9.999953481381553915", Block: 9}}
	rec := httptest.NewRecorder()
	p := Page{Me: "sys@testnet", IsSuperuser: true, CSRFField: template.HTML("<input>"), Data: adminPeersView{
		Identity: id, Native: "POL", Kernels: kernelRows(id, nil)}}
	if err := r.Render(rec, 200, "admin_peers", p); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`<div class="banner banner-error" role="status"><svg class="ico ico-inline" aria-hidden="true"><use href="/static/icons.svg#alert"/></svg> Payments out are paused — native currency too low, top up.</div>`,
		`at finalized block 9: <span class="mono">4 credits</span> and <span class="mono">9.999953481381553915 POL</span>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("network page missing %q: %s", want, body)
		}
	}
	css, err := os.ReadFile("static/app.css")
	if err != nil || !strings.Contains(string(css), ".container > .banner { margin-bottom: var(--space-4); }") {
		t.Error("a banner in page flow owns its spacing")
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

// What other kernels report about an action reads as the stats line does:
// one table, one report per row in the line's own cells, the source last and
// the provider's report first — beside, never inside, this kernel's own strip
// (kernel §D16, U39). The kernel's further counts ride in the cells' titles,
// and a contradiction marks its source. The page and the panel render it.
func TestEvidenceReadsAsTheStatsLine(t *testing.T) {
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	other := strings.Repeat("O", 43) // key-shaped
	a := client.Action{ID: "P", Action: "seller@k-4lnq/greet", Kind: "remote_proxy", Active: true, Evidence: &client.ActionEvidence{
		LocalExperience:  &client.Stats{Uses: 2, Successes: 1},
		ProviderReported: &client.Evidence{IssuerPublicKey: "PROVIDERKEY0000", SubjectActionID: "R1", Uses: 4, Successes: 1, AvgLatencyMs: 690.5},
		ObservedByOthers: []client.Evidence{{IssuerPublicKey: other, SubjectActionID: "R1", Uses: 3, Successes: 3, CorroboratedUses: 2, Contradictions: 1, RatingCount: 1, RatingMean: 1, UnverifiedRating: 2}},
	}}
	rec := httptest.NewRecorder()
	if err := r.Render(rec, 200, "action_detail", Page{Me: "me@testnet", Data: actionView{Action: a}}); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	ev := body[strings.Index(body, "<h2>Evidence</h2>"):]
	ev = ev[:strings.Index(ev, "</table>")]
	for _, want := range []string{
		"#milestone", "25% (4)", "0.7s", `title="PROVIDERKEY0000">provider</span>`,
		`title="Success rate — 3 runs, 2 confirmed by the provider"`, `title="Rating — 1 raters, 2 more unverified"`,
		`class="mono" title="` + other + `">OOOOOOOO…</span>`, `data-copy="` + other + `"`,
		`title="1 trades reported two ways — they count for nothing"`,
	} {
		if !strings.Contains(ev, want) {
			t.Errorf("evidence missing %q:\n%s", want, ev)
		}
	}
	if strings.Index(ev, ">provider<") > strings.Index(ev, ">OOOOOOOO…<") {
		t.Error("the provider's own report comes first")
	}
	if strings.Count(ev, "#alert") != 1 || strings.Count(ev, "confirmed by the provider") != 1 {
		t.Error("a contradiction and a corroboration mark only the row that has them")
	}
	for _, gone := range []string{"R1", "trade-backed", "contradicted", "<h3", "<th"} {
		if strings.Contains(ev, gone) {
			t.Errorf("an action's evidence carries no %q", gone)
		}
	}
	// The panel folds the same table.
	rec = httptest.NewRecorder()
	if err := r.Fragment(rec, 200, "_action_panel", actionView{Action: a}); err != nil {
		t.Fatal(err)
	}
	if p := rec.Body.String(); !strings.Contains(p, `<details class="fold"><summary>Evidence</summary>`) || !strings.Contains(p, "25% (4)") {
		t.Errorf("the panel folds the evidence: %s", p)
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

// A peer's evidence spans its actions: each row names its subject by the
// address the inspection serves, else by its id cut short with the whole
// behind a copy control; rows sort by subject, the provider's first.
func TestPeerEvidenceNamesItsSubjects(t *testing.T) {
	id := "9f2c4e1a-0000-4000-8000-000000000001"
	i := client.Inspection{PublicKey: "PEER", Actions: []client.PeerAction{{ActionID: "A", Name: "z@peer/zed"}}, Evidence: []client.Evidence{
		{IssuerPublicKey: "OTHER", SubjectActionID: "A"},
		{IssuerPublicKey: "PEER", SubjectActionID: "A"},
		{IssuerPublicKey: "PEER", SubjectActionID: id},
	}}
	tb := peerEvidence(i)
	if tb == nil || !tb.Subjects || len(tb.Rows) != 3 {
		t.Fatalf("table = %+v", tb)
	}
	if r := tb.Rows[0]; r.Subject.Text != "9f2c4e1a…" || r.Subject.Title != id || !r.Subject.IsKey || r.Source.Text != "provider" {
		t.Errorf("an unknown subject is its id cut short: %+v", r)
	}
	if r := tb.Rows[1]; r.Subject.Text != "z@peer/zed" || r.Subject.IsKey || r.Source.Text != "provider" {
		t.Errorf("a known subject is its address, the provider's report first: %+v", r)
	}
	if r := tb.Rows[2]; r.Source.Text != "OTHER" {
		t.Errorf("then the other sources: %+v", r)
	}
	if peerEvidence(client.Inspection{}) != nil || actionEvidence(client.Action{}) != nil {
		t.Error("no reports, no table")
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
		linked(figure(2, "this month", "credited"), "full ledger", "/transactions"),
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
	cf := completeForm(template.HTML(`<input>`), "T7", 0, FormModel{}, true, nil, nil, true)
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
	if err := r.Fragment(rec, 200, "_call_form", completeForm(template.HTML(`<input>`), "T8", 0, FormModel{}, true, nil, nil, false)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rec.Body.String(), "decline") {
		t.Errorf("no Decline without the right to decline: %s", rec.Body.String())
	}
}

// The run form renders the schema's tree: a named container as a group that
// folds behind its summary, open while a field it demands is empty and closed
// otherwise, an element as a plain group, an array as rows that name their
// path (a nested array its own), a map node as an empty JSON editor with the
// shape as placeholder, a default as a placeholder and never a value, and only
// grammar buttons.
func TestFormFieldsRenderTheTree(t *testing.T) {
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	fm := BuildForm(json.RawMessage(`{"type":"object","properties":{
		"msgs":{"type":"array","title":"Messages","description":"M.","items":{"type":"object","properties":{
			"role":{"type":"string","default":"user"},
			"tags":{"type":"array","items":{"type":"string"}}}}},
		"meta":{"type":"object"},
		"to":{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}},"required":["to"]}`))
	rec := httptest.NewRecorder()
	if err := r.Fragment(rec, 200, "_form_fields", fm); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`<details class="field group">` + "\n" + `  <summary>Messages <span class="mono muted small">msgs</span></summary>`,
		`<div class="field group">` + "\n  \n  " + `<input type="hidden" name="t_/msgs/0" value="object">`,
		`<details class="field group" open>` + "\n" + `  <summary>to <span class="req" title="required">*</span></summary>`,
		`<input type="hidden" name="t_/msgs" value="array">`, `<div class="rows" data-rows="/msgs">`, `data-row>`,
		`<input type="hidden" name="t_/msgs/0" value="object">`,
		`name="f_/msgs/0/role" rows="1" placeholder="user"></textarea>`,
		`<div class="rows" data-rows="/msgs/0/tags">`, `name="f_/msgs/0/tags/0"`,
		`class="btn-icon" data-row-remove aria-label="Remove"><svg class="ico" aria-hidden="true"><use href="/static/icons.svg#x-mark"/></svg></button>`,
		`class="btn-icon" data-row-add aria-label="Add"><svg class="ico" aria-hidden="true"><use href="/static/icons.svg#plus"/></svg></button>`,
		`name="f_/meta" placeholder="{}" rows="1"></textarea>`, `<input type="hidden" name="t_/meta" value="json">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("form missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, `>user</textarea>`) {
		t.Error("a default is a placeholder, never a value")
	}
	if strings.Contains(body, "<fieldset") || strings.Contains(body, "<legend") {
		t.Error("a group is a details, not a fieldset")
	}
}

// The view: a result in the form's own anatomy, read-only — labels over
// boxes, a named container a closed group whose summary names it and its
// size, an element an open plain group, an array its rows, the words for null
// and booleans, a long string whole, nothing submittable, the JSON behind
// the switch, and a Download only where a transaction exists.
func TestValueBlockRendersAView(t *testing.T) {
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
	long := strings.Repeat("x", 2000)
	body := render(`{"z":"a\nb","list":[1,true,null],"empty":{},"long":"`+long+`","n":{"k":"<i>"}}`, "/transactions/T1/result.json")
	for _, want := range []string{
		`<label>z</label>`, `<div class="box">a` + "\n" + `b</div>`,
		`<details class="field group">` + "\n" + `  <summary>list <span class="muted small">3 items</span></summary>`,
		`<div class="rows">`, `<div class="box">1</div>`, `<div class="box">true</div>`, `<div class="box muted">null</div>`,
		`<label>empty</label>`, `<div class="box muted">{}</div>`,
		`<label>long</label>`, `<div class="box">` + long + `</div>`,
		`<summary>n <span class="muted small">1 field</span></summary>`, `<label>k</label>`, `<div class="box">&lt;i&gt;</div>`,
		`<div class="view" data-json><button type="button" class="switch" role="switch" aria-checked="false" data-json-switch>JSON</button><div data-fields>`,
		`<div data-raw hidden><pre class="json">`,
		`<a href="/transactions/T1/result.json" download>Download</a>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("view missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "<input") || strings.Contains(body, "data-row") || strings.Contains(body, `class="fold"`) {
		t.Errorf("a view has no control and no row mechanism: %s", body)
	}
	if strings.Contains(body, " open>") || strings.Contains(body, "<fieldset") {
		t.Errorf("a named container of a view is closed: %s", body)
	}
	if l := render(`[{"k":1}]`, ""); strings.Count(l, `<div class="field group">`) != 2 || strings.Index(l, `<div class="rows">`) > strings.LastIndex(l, `<div class="field group">`) || strings.Contains(l, "<details") {
		t.Errorf("a root list and its element are open plain groups: %s", l)
	}
	if strings.Index(body, `<label>z</label>`) > strings.Index(body, `<summary>list `) {
		t.Error("keys keep the order served")
	}
	if b := render(`"plain"`, ""); strings.Contains(b, "Download") || strings.Contains(b, "<label") || !strings.Contains(b, `<div class="box">plain</div>`) {
		t.Errorf("a scalar result is one label-less box with no download: %s", b)
	}
	if b := render(``, ""); !strings.Contains(b, `<pre class="json"></pre>`) {
		t.Errorf("an empty value renders raw: %s", b)
	}
	// A string holding a JSON object or array is viewed as that document; the
	// JSON behind the switch stays the stored string. Any other string is text.
	if b := render(`"{\"a\":1}"`, ""); !strings.Contains(b, `<label>a</label>`) || !strings.Contains(b, `<div class="box">1</div>`) || !strings.Contains(b, `<pre class="json">&#34;{\&#34;a\&#34;:1}&#34;</pre>`) {
		t.Errorf("a string holding an object is viewed as the object, stored as the string: %s", b)
	}
	if b := render(`" [1,2] "`, ""); !strings.Contains(b, `<div class="rows">`) {
		t.Errorf("a string holding an array is viewed as its rows: %s", b)
	}
	for raw, box := range map[string]string{`"42"`: `42`, `"{not json"`: `{not json`, `"\"hi\""`: `&#34;hi&#34;`} {
		if b := render(raw, ""); strings.Contains(b, "<label") || strings.Contains(b, `class="rows"`) || !strings.Contains(b, `<div class="box">`+box+`</div>`) {
			t.Errorf("%s is text in one box: %s", raw, b)
		}
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

func TestShortAddrAbbreviatesAChainAddressOnly(t *testing.T) {
	if got := shortAddr("0x70997970C51812dc3A010C7d01b50e0d17dc79C8"); got != "0x7099…79C8" {
		t.Errorf("shortAddr = %q", got)
	}
	for _, s := range []string{"0xvault", "alice@k", ""} {
		if got := shortAddr(s); got != s {
			t.Errorf("shortAddr(%q) = %q, want it unchanged", s, got)
		}
	}
}

// A success run from a conversation is the composer's draft: the outcome
// carries the result as a message carries it (a string as itself, anything
// else as its JSON), and a run elsewhere carries nothing. There is no other
// way to use a result.
func TestASuccessFromARoomIsTheDraft(t *testing.T) {
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	render := func(o Outcome) string {
		rec := httptest.NewRecorder()
		if err := r.Fragment(rec, 200, "_call_outcome", o); err != nil {
			t.Fatal(err)
		}
		return rec.Body.String()
	}
	in := render(Outcome{Class: Success, TxID: "T1", Room: "r1", Result: json.RawMessage(`{"iso":"x"}`)})
	if !strings.Contains(in, `<div class="outcome success" role="status" data-draft="{`) {
		t.Errorf("a run from a room carries its draft: %s", in)
	}
	if !strings.Contains(render(Outcome{Class: Success, TxID: "T1", Room: "r1", Result: json.RawMessage(`"plain"`)}), `data-draft="plain"`) {
		t.Error("a string result is the draft as itself")
	}
	if strings.Contains(in, "data-use-") || strings.Contains(in, `data-menu="use-`) {
		t.Error("Use result is gone")
	}
	if out := render(Outcome{Class: Success, TxID: "T1", Result: json.RawMessage(`{}`)}); strings.Contains(out, "data-draft") {
		t.Error("a run outside a conversation has no draft")
	}
	if fail := render(Outcome{Class: Failed, Room: "r1", Message: "boom"}); strings.Contains(fail, "data-draft") {
		t.Error("a failure is never a draft")
	}
	if resultText(json.RawMessage(`"plain"`)) != "plain" || !strings.Contains(resultText(json.RawMessage(`{"a":1}`)), `"a": 1`) {
		t.Error("a string is itself, anything else its JSON")
	}
}

// The form and the composer draw a field's input with one partial: _field
// wraps _control in its label and help, the bar calls _control alone. The
// balance renders through the money helper, out of band when the heartbeat
// answers.
func TestControlIsOneRenderer(t *testing.T) {
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	f := BuildForm(json.RawMessage(`{"type":"object","properties":{"message":{"type":"string","maxLength":4000,"description":"The text."}},"required":["message"]}`)).Fields[0]
	field, control := httptest.NewRecorder(), httptest.NewRecorder()
	if err := r.Fragment(field, 200, "_field", f); err != nil {
		t.Fatal(err)
	}
	if err := r.Fragment(control, 200, "_control", f); err != nil {
		t.Fatal(err)
	}
	c := strings.TrimSpace(control.Body.String())
	if !strings.Contains(c, `name="t_/message" value="string"`) || !strings.Contains(c, `name="f_/message"`) || !strings.Contains(c, `maxlength="4000"`) {
		t.Errorf("control = %s", c)
	}
	if !strings.Contains(field.Body.String(), c) || strings.Contains(c, "<label") {
		t.Error("_field carries _control whole; _control carries no label")
	}
	b := httptest.NewRecorder()
	if err := r.Fragment(b, 200, "_balance", balanceView{Amount: 1250000, OOB: true}); err != nil {
		t.Fatal(err)
	}
	if got := b.Body.String(); !strings.Contains(got, `id="balance" hx-swap-oob="true">`) || !strings.Contains(got, `value="balance::1250000" hx-swap-oob="true"`) {
		t.Errorf("balance = %s", got)
	}
	plain := httptest.NewRecorder()
	_ = r.Fragment(plain, 200, "_balance", balanceView{Amount: 3})
	if strings.Contains(plain.Body.String(), "hx-swap-oob") {
		t.Error("the page's own balance is not out of band")
	}
}
