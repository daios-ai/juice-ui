package server

import (
	"encoding/json"
	"html/template"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/daios-ai/juice-ui/client"
)

func TestNewRendererParsesAllTemplates(t *testing.T) {
	if _, err := NewRenderer(); err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
}

func TestMoneyFormatting(t *testing.T) {
	cases := map[int64]string{0: "$0.000", 5: "$0.005", 100: "$0.100", 1000: "$1.000", 1234567: "$1,234.567", -2500: "-$2.500"}
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
		Handle:    "alice",
		Available: 1000,
		Data:      userView{User: client.User{Handle: "alice", Available: 1000, Locked: 250}},
	}
	if err := r.Render(rec, 200, "settings", p); err != nil {
		t.Fatalf("Render: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `data-theme="dracula"`) {
		t.Error("theme not stamped onto <html>")
	}
	if !strings.Contains(body, "$1.000") {
		t.Error("available balance not rendered with money formatting")
	}
	if !strings.Contains(body, "alice") {
		t.Error("handle not rendered in nav")
	}
}

func TestDollarsInput(t *testing.T) {
	// dollarsInput renders a bare decimal for number-input values (no $, no commas).
	cases := map[int64]string{0: "0.000", 12: "0.012", 1000: "1.000", 1234567: "1234.567"}
	for in, want := range cases {
		if got := dollarsInput(in); got != want {
			t.Errorf("dollarsInput(%d) = %q want %q", in, got, want)
		}
	}
}

func TestParseDollars(t *testing.T) {
	// parseDollars is the inverse of money: a dollar string → integer credits,
	// tolerating a leading $ and thousands commas, truncating beyond milli-dollars.
	cases := map[string]int64{
		"0.012":      12,
		"1":          1000,
		"1.5":        1500,
		".5":         500,
		"$0.020":     20,
		"$1,234.567": 1234567,
		"-2.5":       -2500,
		"":           0,
		"  12.000  ": 12000,
		"1.2345":     1234, // 4th decimal truncated
	}
	for in, want := range cases {
		if got := parseDollars(in); got != want {
			t.Errorf("parseDollars(%q) = %d want %d", in, got, want)
		}
	}
	// Round-trips: money(parseDollars(dollarsInput(n))) is stable for whole credits.
	for _, n := range []int64{0, 12, 20, 1000, 1234567} {
		if got := parseDollars(dollarsInput(n)); got != n {
			t.Errorf("round-trip parseDollars(dollarsInput(%d)) = %d", n, got)
		}
	}
}

func TestRefFolder(t *testing.T) {
	// refFolder derives the grant selector that connects a delegated action's
	// whole provider group — the ref up to its LAST slash (the kernel directory),
	// not the degenerate single-action selector. Contrast refOwner (first slash).
	cases := []struct{ in, want string }{
		{"@service/a", "@service"},
		{"@service/msg/message", "@service/msg"},
		{"sys/llm/chat", "sys/llm"},
		{"@service", "@service"}, // no slash — returned unchanged
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
	// @buyer pays gross, @seller earns net, sys (platform) earns fee; a viewer
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
		{"buyer pays gross", "@buyer", mk("success", 100, 90, 10), []string{"-$0.100"}},
		{"seller earns net", "@seller", mk("success", 100, 90, 10), []string{"sig-credited", "+$0.090"}},
		{"platform earns fee", superuserHandle, mk("success", 100, 90, 10), []string{"sig-credited", "+$0.010"}},
		{"no stake shows dash", "@bystander", mk("success", 100, 90, 10), []string{"—"}},
		{"failed refunds buyer", "@buyer", mk("failure", 100, 0, 0), []string{"sig-refunded", "↩", "$0.100"}},
		{"failed non-buyer dash", "@seller", mk("failure", 100, 0, 0), []string{"—"}},
		{"open parks buyer", "@buyer", mk("open", 100, 0, 0), []string{"sig-escrow", "◷", "$0.100"}},
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
	if got := string(txAmount(self, "@a")); !strings.Contains(got, "-$0.010") {
		t.Errorf("self-call = %q, want -$0.010 (the fee)", got)
	}
	// sys as the seller collects net + fee = gross.
	sysSell := client.Transaction{OwnerHandle: "@a", TargetHandle: superuserHandle, Status: "success", Gross: 100, Net: 90, Fee: 10}
	if got := string(txAmount(sysSell, superuserHandle)); !strings.Contains(got, "+$0.100") {
		t.Errorf("sys-as-seller = %q, want +$0.100 (net+fee)", got)
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
	// The drawer renders whatever the nav model holds — it no longer hard-codes
	// its own links — so the fixture supplies the nav the sidebar would get.
	data := Page{Handle: "sys", IsSuperuser: true, Themes: ThemeList(), Nav: []NavItem{
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
	r, err := NewRenderer()
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
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	out := Outcome{Class: GrantRequired, ActionRef: "sys/gmail-send", Message: "needs a connected account"}
	if err := r.Fragment(rec, 200, "_call_outcome", out); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	// The affordance links to the consent-plan page for the action's FOLDER
	// selector (sys, not the bare sys/gmail-send) — one gesture connects the
	// whole provider group; the per-scheme consent forms live on that page
	// (v0.7 §14 delta display).
	if !strings.Contains(body, `href="/grants/connect?selector=sys"`) {
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
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	plan := client.GrantPlan{
		Groups: []client.GrantGroup{
			{ProviderKey: "bearer:api.acme.com", Provider: "api.acme.com", Scheme: "delegated_bearer",
				Actions: []client.GrantPlanAction{{Action: "@acme/send"}, {Action: "@acme/read"}}},
			{ProviderKey: "oauth:https://g/token|cid", Provider: "google", Scheme: "oauth_delegated", Scopes: []string{"mail.send"},
				Actions: []client.GrantPlanAction{{Action: "@g/mail"}}},
		},
		SkippedUncallable: 2,
	}
	p := Page{Handle: "me", CSRFField: template.HTML("<input>"), Data: grantConnectView{Selector: "@acme", Plan: plan}}
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
	if !strings.Contains(body, `value="bearer:api.acme.com"`) || !strings.Contains(body, `value="@acme"`) {
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
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	p := Page{Handle: "me", CSRFField: template.HTML("<input>"), Data: userView{
		User: client.User{Handle: "me",
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
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	p := Page{Handle: "sys", IsSuperuser: true, CSRFField: template.HTML("<input>"), Data: adminView{
		Users: []client.AdminUser{{Handle: "@victim", SuspendedAt: "0001-01-01T00:00:00Z"}},
	}}
	if err := r.Render(rec, 200, "admin", p); err != nil {
		t.Fatalf("render: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "suspended") {
		t.Errorf("zero-value suspended_at must render the suspended pill: %s", body)
	}
	if !strings.Contains(body, "/admin/users/@victim/unsuspend") {
		t.Errorf("suspended user must offer the Unsuspend action: %s", body)
	}
}

// TestCallOutcomeFailedStatesRefundOnce guards the de-duplicated failure panel: the
// refund is disclosed once in the headline, the message carries only the kernel's
// reason (no repeated refund, no contradictory "not charged"), and the kernel's
// detail is shown inert below it.
func TestCallOutcomeFailedStatesRefundOnce(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	out := Outcome{Class: Failed, Message: "The action timed out.", Detail: "remote receipt pending"}
	if err := r.Fragment(rec, 200, "_call_outcome", out); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Failed — fully refunded.") {
		t.Errorf("failure headline must state the refund once: %s", body)
	}
	if n := strings.Count(body, "refunded"); n != 1 {
		t.Errorf("refund must be stated exactly once, got %d: %s", n, body)
	}
	if strings.Contains(body, "not charged") {
		t.Errorf("a refunded failure must not also claim 'not charged': %s", body)
	}
	if !strings.Contains(body, "remote receipt pending") {
		t.Errorf("kernel detail must render below the reason: %s", body)
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

// TestCallFormRun renders the shared _call_form for an action run: a repeatable
// money button reading "Run (12)" (no ¤), no locked block, and the result target
// sitting below the form (not nested inside it).
func TestCallFormRun(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	cf := runForm(template.HTML(`<input>`), "A1", 12, FormModel{UseRawFallback: true}, nil, true, "alice")
	if err := r.Fragment(rec, 200, "_call_form", cf); err != nil {
		t.Fatalf("Fragment: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `btn-money`) || !strings.Contains(body, "Run ($0.012)") {
		t.Errorf("run form must have a money button reading %q: %s", "Run ($0.012)", body)
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
	// A paid run is a settling payment: the refund disclosure rides with the confirm.
	if !strings.Contains(body, "Refunded in full if it fails.") {
		t.Errorf("paid run form must disclose the refund at the confirm: %s", body)
	}
	// Result target must sit after the closing </form>, not inside it.
	form := strings.Index(body, "</form>")
	target := strings.Index(body, `id="run-result"`)
	if form < 0 || target < 0 || target < form {
		t.Errorf("result target must render below the form: form@%d target@%d", form, target)
	}
}

// TestCallFormComplete renders the shared _call_form for a step completion: a
// one-shot NEUTRAL "Complete" button and the pre-bound (locked) args shown as a
// read-only definition list above the editable fields, with no verbal label.
func TestCallFormComplete(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	locked := []LockedField{{Name: "to", Value: `"bob"`}}
	cf := completeForm(template.HTML(`<input>`), "S1", 5, FormModel{UseRawFallback: true}, false, locked, nil, nil, false, 0, "")
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
		t.Error("step completion form must be one-shot")
	}
	// Sealed args render as a read-only dl (the completed part), separated from the
	// editable part — same anatomy as the run form.
	if !strings.Contains(body, `class="locked-args`) || !strings.Contains(body, "<dt>to</dt>") || !strings.Contains(body, "bob") {
		t.Errorf("locked args must render as a read-only dl: %s", body)
	}
	if strings.Contains(strings.ToLower(body), "prefilled") || strings.Contains(strings.ToLower(body), "remaining args") {
		t.Error("locked args carry no verbal label — the visual contrast is the signposting")
	}
	if !strings.Contains(body, `id="step-result-S1"`) {
		t.Errorf("completion result target must be step-result-S1: %s", body)
	}
	// A step's price is already parked — the refund disclosure is for the run confirm,
	// not the completion, so it must not appear here.
	if strings.Contains(body, "Refunded in full") {
		t.Errorf("step completion must not carry the run's refund disclosure: %s", body)
	}
}

// TestFormFieldsStructuredSwitch renders _form_fields for a structured schema: it
// carries the segmented fields|JSON field-switch (not the retired "edit as JSON"
// text button) and the has_fields marker.
func TestFormFieldsStructuredSwitch(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	fm := FormModel{Fields: []Field{{Name: "to", Type: "string"}}}
	if err := r.Fragment(rec, 200, "_form_fields", fm); err != nil {
		t.Fatalf("Fragment: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `data-raw-seg="fields"`) || !strings.Contains(body, `data-raw-seg="json"`) {
		t.Errorf("structured form must render the fields|JSON field-switch: %s", body)
	}
	if strings.Contains(body, "edit as JSON") {
		t.Error("retired 'edit as JSON' text button must be gone")
	}
	if !strings.Contains(body, `name="has_fields"`) {
		t.Error("structured form must emit the has_fields marker")
	}
}

// TestCallFormApproval renders an approval _call_form (a step with nothing left to
// fill): just the locked block + the Complete button, no raw-JSON input and no
// wordy "acknowledge" line — the locked JSON + button carry it visually.
func TestCallFormApproval(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	locked := []LockedField{{Name: "message", Value: `"hi"`}}
	cf := completeForm(template.HTML(`<input>`), "S9", 1, FormModel{UseRawFallback: true}, true, locked, nil, nil, false, 0, "")
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
	r, err := NewRenderer()
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
	r, err := NewRenderer()
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

// TestBadgeHelper checks the interval default (a mis-plumbed 0 must not yield an
// invalid "every 0s" htmx trigger).
func TestBadgeHelper(t *testing.T) {
	if got := badge("/x", "l", 0, 0).Every; got != 30 {
		t.Errorf("badge interval defaulted to %d, want 30", got)
	}
	b := badge("/steps/badge", "Waiting", 4, 15)
	if b.Every != 15 || b.Count != 4 || b.URL != "/steps/badge" {
		t.Errorf("badge = %+v", b)
	}
}

// TestBadgePollFragment checks the reusable poller partial: htmx attributes + the
// interval, the count when > 0, and an empty body when 0 (so .badge:empty hides it).
func TestBadgePollFragment(t *testing.T) {
	r, err := NewRenderer()
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
// steps badge and each app badge — on the configured interval, in both the sidebar
// and the mobile tab bar.
func TestLayoutRendersBadgePollers(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	p := Page{
		Handle:    "me",
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
		t.Errorf("steps badge poller must appear in sidebar AND drawer: %s", body)
	}
	if !strings.Contains(body, `hx-get="/chat/badge"`) {
		t.Errorf("app badge poller missing: %s", body)
	}
	// steps(sidebar+drawer+menu button) + chat(sidebar+drawer) = 5 pollers, all on 15s.
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
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	if err := r.Render(rec, 200, "error", Page{Handle: "me", Data: struct{ Message string }{"x"}}); err != nil {
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
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	// List → a single amount column showing the total.
	rec := httptest.NewRecorder()
	list := Page{Handle: "me", Data: processesView{Open: []client.Process{{ID: "p1", Status: "open", Available: 30, Locked: 70}}}}
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
	if !strings.Contains(body, ">$0.100<") {
		t.Errorf("amount cell must show the total 30+70=100 credits = $0.100: %s", body)
	}

	// Detail → the available + parked split is kept.
	rec = httptest.NewRecorder()
	detail := Page{Handle: "me", CSRFField: template.HTML("<input>"),
		Data: processView{Process: client.Process{ID: "p1", Status: "open", Available: 30, Locked: 70}}}
	if err := r.Render(rec, 200, "process", detail); err != nil {
		t.Fatalf("render process: %v", err)
	}
	if d := rec.Body.String(); !strings.Contains(d, "available") || !strings.Contains(d, "parked") {
		t.Errorf("process detail must keep the available+parked split: %s", d)
	}
}

// TestActionDetailGatesRunOnPeerState is the load-bearing federation test: a remote
// action whose peer is offline/unfunded shows a status notice INSTEAD of the run
// form (no doomed spend), while a healthy remote action still shows the form.
func TestActionDetailGatesRunOnPeerState(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	view := func(ps string) actionView {
		return actionView{
			Action: client.Action{ID: "a1", Action: "peerx/greet", Name: "greet", Kind: "remote_proxy", Active: true, Visibility: "public", Price: 10, PeerState: ps},
			Form:   BuildForm(nil),
		}
	}
	render := func(ps string) string {
		rec := httptest.NewRecorder()
		p := Page{Handle: "me", CSRFField: template.HTML("<input>"), Data: view(ps)}
		if err := r.Render(rec, 200, "action_detail", p); err != nil {
			t.Fatalf("render %q: %v", ps, err)
		}
		return rec.Body.String()
	}

	off := render("offline")
	// The banner names the state in one phrase (reusing the chip's label), no prose.
	if !strings.Contains(off, "Peer offline.") {
		t.Errorf("offline notice missing: %s", off)
	}
	if strings.Contains(off, "data-run-panel") {
		t.Error("offline peer must NOT render the run form")
	}
	if !strings.Contains(off, "peer offline") {
		t.Error("offline head chip missing")
	}

	// v0.12: "unfunded" is the stale peer-credit cache — under global exposure
	// the serving kernel alone decides admission, so the form STAYS offered
	// (verified live: an "unfunded" proxy call succeeds); the chip remains a
	// quiet hint and a refusal surfaces as the typed peer_unfunded outcome
	// (requirements.md §6.6).
	unf := render("unfunded")
	if !strings.Contains(unf, "data-run-panel") {
		t.Error("unfunded peer MUST still render the run form (kernel decides admission)")
	}
	if !strings.Contains(unf, "peer unfunded") {
		t.Error("unfunded head chip missing")
	}

	ok := render("")
	if !strings.Contains(ok, "data-run-panel") {
		t.Error("healthy remote action must render the run form")
	}
	if strings.Contains(ok, "Peer offline.") {
		t.Error("no peer notice when healthy")
	}
}

// TestActionRunPollsWhilePeerBlocked checks item 3: a peer-blocked run region polls
// GET /actions/{id} so it re-enables on its own, and the healthy region carries NO
// poller (self-terminating — the poll stops once the form is back).
func TestActionRunPollsWhilePeerBlocked(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	render := func(ps string) string {
		rec := httptest.NewRecorder()
		p := Page{Handle: "me", CSRFField: template.HTML("<input>"), Data: actionView{
			Action:    client.Action{ID: "a1", Action: "@peerx/greet", Name: "greet", Kind: "remote_proxy", Active: true, Visibility: "public", Price: 10, PeerState: ps},
			Form:      BuildForm(nil),
			BadgeSecs: 15,
		}}
		if err := r.Render(rec, 200, "action_detail", p); err != nil {
			t.Fatalf("render %q: %v", ps, err)
		}
		return rec.Body.String()
	}
	off := render("offline")
	if !strings.Contains(off, `hx-get="/actions/a1"`) || !strings.Contains(off, "every 15s") {
		t.Errorf("peer-blocked run region must poll /actions/a1 every 15s: %s", off)
	}
	ok := render("")
	if strings.Contains(ok, `hx-get="/actions/a1"`) {
		t.Error("healthy run region must NOT carry a poller (self-terminating)")
	}
}

// TestProcessAwaitingReceiptPolls checks item 4: a process parked on a remote
// receipt polls GET /processes/{id}; once settled the region carries no poller.
func TestProcessAwaitingReceiptPolls(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	render := func(awaiting bool) string {
		rec := httptest.NewRecorder()
		p := Page{Handle: "me", CSRFField: template.HTML("<input>"), Data: processView{
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
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		class OutcomeClass
		want  string
	}{
		{PeerUnreachable, "Peer unreachable"},
		{PeerUnfunded, "Peer credit exhausted"},
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
func TestPeersPollerAndSuspendedToggle(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	renderPeers := func(showSuspended bool) string {
		rec := httptest.NewRecorder()
		p := Page{Handle: "sys", IsSuperuser: true, BadgeSecs: 15, CSRFField: template.HTML("<input>"), Data: adminPeersView{
			Identity:      client.Identity{Handle: "me", PublicKey: "PK"},
			Peers:         []client.Peer{{Handle: "peerb", PublicKey: "KEY", PeerCredit: 250, LastSeen: "2026-07-12T00:00:00Z"}},
			ShowSuspended: showSuspended,
		}}
		if err := r.Render(rec, 200, "admin_peers", p); err != nil {
			t.Fatalf("render: %v", err)
		}
		return rec.Body.String()
	}
	body := renderPeers(false)
	if !strings.Contains(body, `id="peers"`) || !strings.Contains(body, `hx-get="/admin/peers"`) || !strings.Contains(body, "every 15s") {
		t.Errorf("peers grid must be an htmx-polled fragment: %s", body)
	}
	if !strings.Contains(body, `href="/admin/peers?all=1"`) || !strings.Contains(body, "Show suspended") {
		t.Errorf("peers page must offer a Show-suspended toggle: %s", body)
	}
	suspended := renderPeers(true)
	if !strings.Contains(suspended, "Hide suspended") || !strings.Contains(suspended, `hx-get="/admin/peers?all=1"`) {
		t.Errorf("suspended view must show a Hide toggle and poll ?all=1: %s", suspended)
	}
}

// TestPeerRowOffersSettleAndSuspend: the two levers that remain on a peer row —
// Settle (by handle) and Suspend (by key); a suspended peer is marked and offers
// Unsuspend. There is no unsubscribe: a peer is not something one leaves.
func TestPeerRowOffersSettleAndSuspend(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	p := Page{Handle: "sys", IsSuperuser: true, BadgeSecs: 15, CSRFField: template.HTML("<input>"), Data: adminPeersView{
		Identity:      client.Identity{Handle: "me", PublicKey: "PK"},
		ShowSuspended: true,
		Peers: []client.Peer{
			{Handle: "@active", PublicKey: "KF"},
			{Handle: "@frozen", PublicKey: "KG", SuspendedAt: "2026-07-19T17:50:24Z"},
		},
	}}
	if err := r.Render(rec, 200, "admin_peers", p); err != nil {
		t.Fatalf("render: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, ">suspended<") {
		t.Errorf("suspended peer must be marked: %s", body)
	}
	// Settle posts by handle; suspend/unsuspend post by key.
	if !strings.Contains(body, `action="/admin/peers/settle"`) || !strings.Contains(body, `value="@active"`) {
		t.Errorf("active row must offer Settle by handle: %s", body)
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
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	p := Page{Handle: "sys", IsSuperuser: true, BadgeSecs: 15, CSRFField: template.HTML("<input>"), Data: adminInspectView{
		Key:        "KEYC",
		Inspection: client.Inspection{Handle: "kernelc", Online: true, Source: "live", Reachability: client.Reachability{Path: "direct"}},
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
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	p := Page{Handle: "sys", IsSuperuser: true, BadgeSecs: 15, CSRFField: template.HTML("<input>"), Data: adminInspectView{
		Key: "KDOWN",
		Inspection: client.Inspection{Handle: "", Online: false, Source: "none",
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

// TestPeersPageShowsCreditAndLastSeen checks gap (a): the Friends table surfaces our
// prepaid credit on the peer and when it was last seen.
func TestPeersPageShowsCreditAndLastSeen(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	p := Page{Handle: "sys", IsSuperuser: true, CSRFField: template.HTML("<input>"), Data: adminPeersView{
		Identity: client.Identity{Handle: "me", PublicKey: "PK"},
		Peers:    []client.Peer{{Handle: "peerb", PublicKey: "KEY", Available: 10, Locked: 0, PeerCredit: 250, LastSeen: "2026-07-12T00:00:00Z"}},
	}}
	if err := r.Render(rec, 200, "admin_peers", p); err != nil {
		t.Fatalf("render: %v", err)
	}
	body := rec.Body.String()
	// Friends now render as kernel cards (with banner art), not a table.
	if !strings.Contains(body, "our credit there") {
		t.Error("friend kernel card missing the our-credit figure")
	}
	if !strings.Contains(body, "$0.250") {
		t.Errorf("our credit value not rendered as dollars: %s", body)
	}
	if !strings.Contains(body, `class="kernel-card`) || !strings.Contains(body, `class="media wide"`) {
		t.Errorf("friend must render as a kernel card with a banner: %s", body)
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
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	nav := []NavItem{{Slug: "browse", Label: "browse", Href: "/browse"}}
	render := func(open bool) string {
		rec := httptest.NewRecorder()
		p := Page{Handle: "me", Nav: nav, MenuOpen: open, Path: "/browse",
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
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	p := Page{Handle: "me", Themes: ThemeList(), Nav: []NavItem{{Slug: "browse", Href: "/browse"}},
		Data: struct{ Message string }{"x"}}
	if err := r.Render(rec, 200, "error", p); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(rec.Body.String(), "data-theme-select"); n != 0 {
		t.Errorf("chrome carries %d theme pickers, want 0 (settings owns it)", n)
	}
}

// v0.12 refs: bare owner/name splits on the first slash; a remote ref's owner
// segment carries the kernel alias after "@"; a migration-period leading "@"
// is tolerated (requirements.md §4).
func TestRefHelpersRemote(t *testing.T) {
	cases := []struct {
		ref, owner, kernel, name string
		remote                   bool
	}{
		{"sys/llm/chat", "sys", "", "llm/chat", false},
		{"lena/ocr-translate", "lena", "", "ocr-translate", false},
		{"alice@B/greet", "alice@B", "B", "greet", true},
		{"tom@acme2/brief/daily", "tom@acme2", "acme2", "brief/daily", true},
		{"@sys/llm/chat", "sys", "", "llm/chat", false}, // migration tolerance
	}
	for _, c := range cases {
		if got := refOwner(c.ref); got != c.owner {
			t.Errorf("refOwner(%q)=%q want %q", c.ref, got, c.owner)
		}
		if got := refKernel(c.ref); got != c.kernel {
			t.Errorf("refKernel(%q)=%q want %q", c.ref, got, c.kernel)
		}
		if got := refName(c.ref); got != c.name {
			t.Errorf("refName(%q)=%q want %q", c.ref, got, c.name)
		}
		if got := refIsRemote(c.ref); got != c.remote {
			t.Errorf("refIsRemote(%q)=%v want %v", c.ref, got, c.remote)
		}
	}
}

// A remote owner segment renders emphasized but UNLINKED: the kernel alias is
// presentation-only and there is no local profile behind a remote owner
// (requirements.md §6.2; extends the inert-counterparty rule).
func TestRefRemoteUnlinked(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	if err := r.Fragment(rec, 200, "_ref", "alice@B/greet"); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "alice@B") || !strings.Contains(body, `class="owner"`) {
		t.Errorf("remote owner segment must render emphasized: %s", body)
	}
	if strings.Contains(body, "/users/") || strings.Contains(body, "<a ") {
		t.Errorf("remote owner must not link anywhere: %s", body)
	}
	// A local ref keeps its owner-page link.
	rec2 := httptest.NewRecorder()
	if err := r.Fragment(rec2, 200, "_ref", "lena/greet"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rec2.Body.String(), `href="/users/lena"`) {
		t.Errorf("local owner must link to its owner page: %s", rec2.Body.String())
	}
}

// A payment-step completion is a spend of the completer's own money: money
// button naming the sealed amount, unnumbered money button when the completer
// types it (§6.3 two-branch rule).
func TestCallFormPayment(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	cf := completeForm(template.HTML(`<input>`), "SP", 0, FormModel{}, true,
		[]LockedField{{Name: "target", Value: `"alice"`}, {Name: "amount", Value: "300"}}, nil, nil,
		true, 300, "alice")
	if err := r.Fragment(rec, 200, "_call_form", cf); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "btn-money") || !strings.Contains(body, "Complete ($0.300)") {
		t.Errorf("sealed payment must use the money button naming the amount: %s", body)
	}
	if !strings.Contains(body, "Paid from your balance") || !strings.Contains(body, "alice") {
		t.Errorf("payment form must disclose whose funds move and to whom: %s", body)
	}
	// Typed amount: money button, no parenthesized figure (wallet-send pattern).
	rec2 := httptest.NewRecorder()
	cf2 := completeForm(template.HTML(`<input>`), "SP2", 0, FormModel{UseRawFallback: true}, false, nil, nil, nil,
		true, 0, "")
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
func TestActionCardDiscovered(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	card := func(v actionCardView) string {
		rec := httptest.NewRecorder()
		if err := r.Fragment(rec, 200, "_action_card", v); err != nil {
			t.Fatalf("Fragment: %v", err)
		}
		return rec.Body.String()
	}
	body := card(actionCardView{
		Discovered: true,
		Action: client.Action{
			ID: "REMOTE-ID", Action: "tom@KERNELKEY/greet",
			Description: "Greets a person by name.",
		},
	})
	if !strings.Contains(body, "known network") {
		t.Errorf("a discovered card must carry the known-network chip: %s", body)
	}
	if !strings.Contains(body, "Greets a person by name.") {
		t.Errorf("a discovered card must show the description: %s", body)
	}
	// The remote id names nothing here, so it must not become a local link.
	if strings.Contains(body, "/actions/REMOTE-ID") {
		t.Errorf("a discovered card must not link to a local action page: %s", body)
	}
	// No price at all — not even the "free" fallback, which would be a claim.
	if strings.Contains(body, "free") || strings.Contains(body, `class="price"`) {
		t.Errorf("a discovered card must state no price: %s", body)
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

// A cached remote proxy is kernel-managed state: the kernel refuses enable and
// disable on it, so neither the workshop row nor the action page may offer a
// lifecycle verb for one. The peer-level lever is suspend, on network.
func TestProxyRowsOfferNoLifecycleVerbs(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	proxy := client.Action{ID: "P1", Action: "k-abc/tom/greet", Kind: "remote_proxy", Active: true, Price: 14}
	own := client.Action{ID: "A1", Action: "me/thing", Kind: "wasm", Active: true, Price: 3}

	rec := httptest.NewRecorder()
	p := Page{Handle: "sys", IsSuperuser: true, CSRFField: template.HTML("<input>"), Data: actionsView{
		Mine: []actionCardView{{Action: proxy}, {Action: own}}, Superuser: true, Scope: "all",
	}}
	if err := r.Render(rec, 200, "workshop", p); err != nil {
		t.Fatalf("render workshop: %v", err)
	}
	body := rec.Body.String()
	for _, gone := range []string{"/actions/P1/disable", "/actions/P1/enable", "/actions/P1/visibility", "/actions/P1/delete"} {
		if strings.Contains(body, gone) {
			t.Errorf("workshop must offer no lifecycle verb on a proxy row: %q in %s", gone, body)
		}
	}
	// The operator's own action keeps its full menu — the rule is about proxies.
	if !strings.Contains(body, "/actions/A1/disable") {
		t.Errorf("an ordinary owned action must keep its lifecycle menu: %s", body)
	}

	rec2 := httptest.NewRecorder()
	p2 := Page{Handle: "sys", CSRFField: template.HTML("<input>"), Data: actionView{
		Action: proxy, IsOwner: true, Form: BuildForm(nil),
	}}
	if err := r.Render(rec2, 200, "action_detail", p2); err != nil {
		t.Fatalf("render action_detail: %v", err)
	}
	if d := rec2.Body.String(); strings.Contains(d, "/actions/P1/disable") || strings.Contains(d, ">Manage<") {
		t.Errorf("the action page must withhold Manage for a proxy: %s", d)
	}
}
