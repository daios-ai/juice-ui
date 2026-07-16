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
	if err := r.Render(rec, 200, "you", p); err != nil {
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
	out := Outcome{Class: GrantRequired, ActionRef: "@sys/gmail-send", Message: "needs a connected account"}
	if err := r.Fragment(rec, 200, "_call_outcome", out); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	// The affordance links to the consent-plan page for the action's selector; the
	// per-scheme consent forms live on that page (v0.7 §14 delta display).
	if !strings.Contains(body, `href="/grants/connect?selector=`) || !strings.Contains(body, `%40sys%2fgmail-send`) {
		t.Errorf("grant_required outcome must link to the connect plan for the action: %s", body)
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
	p := Page{Handle: "@me", CSRFField: template.HTML("<input>"), Data: grantConnectView{Selector: "@acme", Plan: plan}}
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
	// The oauth group offers the browser consent instead.
	if !strings.Contains(body, `action="/grants/start"`) {
		t.Errorf("oauth group must offer the browser consent: %s", body)
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
	p := Page{Handle: "@me", CSRFField: template.HTML("<input>"), Data: userView{
		User: client.User{Handle: "@me",
			Connectors: []client.Connector{{
				Directory:   "@chat",
				Connections: []client.Connection{{Provider: "127.0.0.1:8080", ProviderKey: "bearer:h"}},
				Actions:     []client.Grant{{Action: "@chat/pay", Scopes: []string{"pay.write"}, ProviderKey: "bearer:h", CreatedAt: "2026-07-12T00:00:00Z"}},
			}},
			Connections: []client.Connection{
				{Provider: "127.0.0.1:8080", Actions: 8, Unused: false, ProviderKey: "bearer:h", CreatedAt: "2026-07-12T00:00:00Z"},
				{Provider: "api.acme.com", Actions: 0, Unused: true, ProviderKey: "oauth:a", CreatedAt: "2026-07-11T00:00:00Z"},
			}},
	}}
	if err := r.Render(rec, 200, "you", p); err != nil {
		t.Fatalf("render: %v", err)
	}
	body := rec.Body.String()
	// The account inventory: one row per upstream account, by provider (the raw
	// per-folder tree fragments a single account across many grants).
	if !strings.Contains(body, "127.0.0.1:8080") {
		t.Errorf("connected account (by provider) not shown: %s", body)
	}
	if !strings.Contains(body, `name="account" value="bearer:h"`) {
		t.Errorf("account-level disconnect (provider_key) missing: %s", body)
	}
	// The unused account is surfaced as its own row.
	if !strings.Contains(body, "api.acme.com") || !strings.Contains(body, "no actions use this account") {
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
	p := Page{Handle: "@sys", IsSuperuser: true, CSRFField: template.HTML("<input>"), Data: adminView{
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
	cf := runForm(template.HTML(`<input>`), "A1", 12, FormModel{UseRawFallback: true}, nil)
	if err := r.Fragment(rec, 200, "_call_form", cf); err != nil {
		t.Fatalf("Fragment: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `btn-money`) || !strings.Contains(body, "Run (12)") {
		t.Errorf("run form must have a money button reading %q: %s", "Run (12)", body)
	}
	if strings.ContainsRune(body, '¤') {
		t.Error("money button must not render the ¤ glyph")
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
// one-shot money button "Complete (5)" and the pre-bound (locked) args shown via
// the result renderer (<pre class="json">), with no verbal "prefilled" label.
func TestCallFormComplete(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	locked := json.RawMessage(`{"to":"@bob"}`)
	cf := completeForm(template.HTML(`<input>`), "S1", 5, FormModel{UseRawFallback: true}, false, locked, nil)
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
	if !strings.Contains(body, `<pre class="json">`) || !strings.Contains(body, "@bob") {
		t.Errorf("locked args must render via the result renderer (pre.json): %s", body)
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
	locked := json.RawMessage(`{"message":"hi"}`)
	cf := completeForm(template.HTML(`<input>`), "S9", 1, FormModel{UseRawFallback: true}, true, locked, nil)
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
	if err := r.Fragment(rec, 200, "_stat_strip", statStrip(12, nil, true)); err != nil {
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
	if err := r.Fragment(rec, 200, "_stat_strip", statStrip(5, &s, true)); err != nil {
		t.Fatal(err)
	}
	detail := rec.Body.String()
	if !strings.Contains(detail, "90%") || !strings.Contains(detail, "(10)") || !strings.Contains(detail, "(4)") {
		t.Errorf("detail strip must show success%% with run/rater counts: %s", detail)
	}

	rec = httptest.NewRecorder()
	if err := r.Fragment(rec, 200, "_stat_strip", statStrip(5, &s, false)); err != nil {
		t.Fatal(err)
	}
	card := rec.Body.String()
	if !strings.Contains(card, "90%") {
		t.Errorf("card strip must still show success%%: %s", card)
	}
	if strings.Contains(card, "(10)") || strings.Contains(card, "(4)") {
		t.Errorf("card strip must NOT show counts: %s", card)
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
		Handle:    "@me",
		BadgeSecs: 15,
		Nav: []NavItem{
			{Slug: "inbox", Label: "inbox", Href: "/inbox", Icon: template.HTML(`<svg class="ico"></svg>`), BadgeURL: "/inbox/badge", Tab: true},
			{Slug: "chat", Label: "chat", Href: "/chat", Icon: template.HTML(`<svg class="ico"></svg>`), Badge: 2, BadgeURL: "/chat/badge", Tab: true},
		},
		Data: struct{ Message string }{"x"},
	}
	if err := r.Render(rec, 200, "error", p); err != nil {
		t.Fatalf("render: %v", err)
	}
	body := rec.Body.String()
	if strings.Count(body, `hx-get="/inbox/badge"`) < 2 {
		t.Errorf("steps badge poller must appear in sidebar AND tab bar: %s", body)
	}
	if !strings.Contains(body, `hx-get="/chat/badge"`) {
		t.Errorf("app badge poller missing: %s", body)
	}
	// steps(sidebar+tab) + chat(sidebar+tab) = 4 pollers, all on the configured 15s.
	if n := strings.Count(body, `every 15s`); n < 4 {
		t.Errorf("configured interval applied to %d pollers, want >= 4: %s", n, body)
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
	if err := r.Render(rec, 200, "error", Page{Handle: "@me", Data: struct{ Message string }{"x"}}); err != nil {
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

// TestCreditsColumnIsSingleTotal guards snag #3: lists show one "Credits" column =
// available + parked (total); the split stays only on prominent wallet/detail views.
func TestCreditsColumnIsSingleTotal(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	// List → a single Credits column showing the total.
	rec := httptest.NewRecorder()
	list := Page{Handle: "@me", Data: processesView{Open: []client.Process{{ID: "p1", Status: "open", Available: 30, Locked: 70}}}}
	if err := r.Render(rec, 200, "processes", list); err != nil {
		t.Fatalf("render processes: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `<th class="money">Credits</th>`) {
		t.Errorf("list must have a single Credits column: %s", body)
	}
	if strings.Contains(body, ">Parked<") || strings.Contains(body, ">Available<") {
		t.Errorf("list must drop the standing Available/Parked columns: %s", body)
	}
	if !strings.Contains(body, ">100<") {
		t.Errorf("Credits cell must show the total 30+70=100: %s", body)
	}

	// Detail → the available + parked split is kept.
	rec = httptest.NewRecorder()
	detail := Page{Handle: "@me", CSRFField: template.HTML("<input>"),
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
			Action: client.Action{ID: "a1", Action: "@peerx/greet", Name: "greet", Kind: "remote_proxy", Active: true, Visibility: "public", Price: 10, PeerState: ps},
			Form:   BuildForm(nil),
		}
	}
	render := func(ps string) string {
		rec := httptest.NewRecorder()
		p := Page{Handle: "@me", CSRFField: template.HTML("<input>"), Data: view(ps)}
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

	unf := render("unfunded")
	if !strings.Contains(unf, "Peer unfunded.") {
		t.Errorf("unfunded notice missing: %s", unf)
	}
	if strings.Contains(unf, "data-run-panel") {
		t.Error("unfunded peer must NOT render the run form")
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
		p := Page{Handle: "@me", CSRFField: template.HTML("<input>"), Data: actionView{
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
		p := Page{Handle: "@me", CSRFField: template.HTML("<input>"), Data: processView{
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

// TestPeersFriendsPollerAndDeniedToggle checks items 1 & 6: the Friends table is an
// htmx-polled fragment, and a Show/Hide-denied toggle drives the ?all=1 view.
func TestPeersFriendsPollerAndDeniedToggle(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	renderPeers := func(showDenied bool) string {
		rec := httptest.NewRecorder()
		p := Page{Handle: "@sys", IsSuperuser: true, BadgeSecs: 15, CSRFField: template.HTML("<input>"), Data: adminPeersView{
			Identity:   client.Identity{Handle: "@me", PublicKey: "PK"},
			Peers:      []client.Peer{{Handle: "@peerb", PublicKey: "KEY", PeerCredit: 250, LastSeen: "2026-07-12T00:00:00Z"}},
			ShowDenied: showDenied,
		}}
		if err := r.Render(rec, 200, "admin_peers", p); err != nil {
			t.Fatalf("render: %v", err)
		}
		return rec.Body.String()
	}
	body := renderPeers(false)
	if !strings.Contains(body, `id="peers-friends"`) || !strings.Contains(body, `hx-get="/admin/peers"`) || !strings.Contains(body, "every 15s") {
		t.Errorf("Friends table must be an htmx-polled fragment: %s", body)
	}
	if !strings.Contains(body, `href="/admin/peers?all=1"`) || !strings.Contains(body, "Show denied") {
		t.Errorf("peers page must offer a Show-denied toggle: %s", body)
	}
	denied := renderPeers(true)
	if !strings.Contains(denied, "Hide denied") || !strings.Contains(denied, `hx-get="/admin/peers?all=1"`) {
		t.Errorf("denied view must show a Hide toggle and poll ?all=1: %s", denied)
	}
}

// TestPeersDeniedRowOffersRefriend checks item 6: a denied peer (denied_at set) is
// marked and offers Re-friend (which clears the denial), while a live friend offers
// Unfriend.
func TestPeersDeniedRowOffersRefriend(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	p := Page{Handle: "@sys", IsSuperuser: true, BadgeSecs: 15, CSRFField: template.HTML("<input>"), Data: adminPeersView{
		Identity:   client.Identity{Handle: "@me", PublicKey: "PK"},
		ShowDenied: true,
		Peers: []client.Peer{
			{Handle: "@friend", PublicKey: "KF"},
			{Handle: "@gone", PublicKey: "KG", DeniedAt: "2026-07-13T17:50:24Z"},
		},
	}}
	if err := r.Render(rec, 200, "admin_peers", p); err != nil {
		t.Fatalf("render: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, ">denied<") {
		t.Errorf("denied peer must be marked: %s", body)
	}
	// The denied row re-friends by key; the live friend row unfriends by handle.
	if !strings.Contains(body, `action="/admin/peers/friend"`) || !strings.Contains(body, `value="KG"`) {
		t.Errorf("denied row must offer Re-friend prefilled with its key: %s", body)
	}
	if !strings.Contains(body, `value="@friend"`) {
		t.Errorf("live friend row must still offer Unfriend: %s", body)
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
	p := Page{Handle: "@sys", IsSuperuser: true, BadgeSecs: 15, CSRFField: template.HTML("<input>"), Data: adminInspectView{
		Key:        "KEYC",
		Inspection: client.Inspection{Handle: "@kernelc", Online: true, Source: "live", Reachability: client.Reachability{Path: "direct"}},
	}}
	if err := r.Render(rec, 200, "admin_inspect", p); err != nil {
		t.Fatalf("render: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="inspect-status"`) || !strings.Contains(body, `hx-get="/admin/peers/KEYC"`) || !strings.Contains(body, "every 15s") {
		t.Errorf("inspect status card must be an htmx-polled fragment: %s", body)
	}
}

// TestInspectOfflineStranger checks two UX fixes: an unreachable non-friend must NOT
// offer Befriend (the handshake needs to reach the peer), and the offline reason is a
// short standardized phrase with the raw fed error tucked into a title tooltip.
func TestInspectOfflineStranger(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	p := Page{Handle: "@sys", IsSuperuser: true, BadgeSecs: 15, CSRFField: template.HTML("<input>"), Data: adminInspectView{
		Key: "KDOWN", IsFriend: false,
		Inspection: client.Inspection{Handle: "", Online: false, Source: "none",
			Reachability: client.Reachability{Path: "unreachable", Error: "fed: cannot resolve peer 12D3KooW…: routing: not found"}},
	}}
	if err := r.Render(rec, 200, "admin_inspect", p); err != nil {
		t.Fatalf("render: %v", err)
	}
	body := rec.Body.String()
	if strings.Contains(body, ">Befriend<") {
		t.Error("an unreachable kernel must not offer Befriend")
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
	p := Page{Handle: "@sys", IsSuperuser: true, CSRFField: template.HTML("<input>"), Data: adminPeersView{
		Identity: client.Identity{Handle: "@me", PublicKey: "PK"},
		Peers:    []client.Peer{{Handle: "@peerb", PublicKey: "KEY", Available: 10, Locked: 0, PeerCredit: 250, LastSeen: "2026-07-12T00:00:00Z"}},
	}}
	if err := r.Render(rec, 200, "admin_peers", p); err != nil {
		t.Fatalf("render: %v", err)
	}
	body := rec.Body.String()
	// Friends now render as kernel cards (with banner art), not a table.
	if !strings.Contains(body, "our credit there") {
		t.Error("friend kernel card missing the our-credit figure")
	}
	if !strings.Contains(body, "250 ¤") {
		t.Errorf("our credit value not rendered: %s", body)
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
