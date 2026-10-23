package server

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// Drift guard for docs/ui-grammar.md. The templates must use only the button
// classes the grammar defines (§5.1) and must carry no inline styles (§6). This
// fails the build the moment a template introduces an off-grammar button or an
// inline style, so the visual grammar cannot silently rot again.

var allowedButtonClasses = map[string]bool{
	"btn": true, "btn-money": true, "btn-danger": true,
	"btn-icon": true, "btn-quiet": true,
}

var (
	classAttrRe   = regexp.MustCompile(`class="([^"]*)"`)
	tmplActionRe  = regexp.MustCompile(`{{[^}]*}}`)
	inlineStyleRe = regexp.MustCompile(`style\s*=\s*["']`)
)

func TestTemplatesUseOnlyGrammarButtons(t *testing.T) {
	files, err := fs.Glob(tmplFS, "templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		b, err := tmplFS.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)

		// No inline styles anywhere.
		if loc := inlineStyleRe.FindStringIndex(src); loc != nil {
			t.Errorf("%s: inline style is forbidden (grammar §6): …%s…", f, snippet(src, loc[0]))
		}

		// Every btn* class token must be a grammar button. Template actions inside a
		// class attribute (conditionals) are blanked to a space first, so the literal
		// class names around them are still checked.
		for _, m := range classAttrRe.FindAllStringSubmatch(src, -1) {
			cleaned := tmplActionRe.ReplaceAllString(m[1], " ")
			for _, tok := range strings.Fields(cleaned) {
				if strings.HasPrefix(tok, "btn") && !allowedButtonClasses[tok] {
					t.Errorf("%s: off-grammar button class %q in class=%q (allowed: btn, btn-money, btn-danger, btn-icon, btn-quiet)", f, tok, m[1])
				}
			}
		}
	}
}

// TestNoFloatingCommandChrome enforces the content-first pull-back: the
// page-head carries no command cluster, so the retired .toolbar component and
// the retired .btn-primary tier must never reappear in a template. Commands are
// quiet and inline; color is reserved for money (.btn-money) and danger.
func TestNoFloatingCommandChrome(t *testing.T) {
	files, _ := fs.Glob(tmplFS, "templates/*.html")
	for _, f := range files {
		src := string(mustRead(t, f))
		if strings.Contains(src, `class="toolbar"`) {
			t.Errorf("%s: retired .toolbar — put commands inline with their content", f)
		}
		if strings.Contains(src, "btn-primary") {
			t.Errorf("%s: retired .btn-primary — use the single neutral .btn (color is money/danger only)", f)
		}
	}
}

// TestNoLinkbar ensures the retired .linkbar component never reappears — .tabs
// is the single view-switch component (login panels and list filters).
func TestNoLinkbar(t *testing.T) {
	files, _ := fs.Glob(tmplFS, "templates/*.html")
	for _, f := range files {
		if strings.Contains(string(mustRead(t, f)), "linkbar") {
			t.Errorf("%s: uses retired '.linkbar' — use the .tabs view-switch component", f)
		}
	}
}

// TestTabsGoThroughRenderer ensures no template hand-writes a tab's active state
// (`class="tab{{if …`) — every .tabs tab must go through the single _tab partial,
// so the class and aria-current can never diverge. The _tab definition itself
// (in _partials.html) is the one allowed occurrence.
func TestTabsGoThroughRenderer(t *testing.T) {
	files, _ := fs.Glob(tmplFS, "templates/*.html")
	for _, f := range files {
		if f == "templates/_partials.html" {
			continue // the _tab renderer lives here
		}
		if strings.Contains(string(mustRead(t, f)), `class="tab{{if`) {
			t.Errorf("%s: hand-written tab active state — render tabs via {{template \"_tab\" (tab …)}}", f)
		}
	}
}

// TestNoLegacyTabClass ensures the superseded auth-tab class is gone.
func TestNoLegacyTabClass(t *testing.T) {
	files, _ := fs.Glob(tmplFS, "templates/*.html")
	for _, f := range files {
		if strings.Contains(string(mustRead(t, f)), "auth-tab") {
			t.Errorf("%s: uses legacy 'auth-tab' — use the .tabs view-switch component", f)
		}
	}
}

// TestTablesAreScrollable enforces that every <table> sits in a .table-scroll
// container, so a wide table scrolls inside its own box and the page body never
// scrolls horizontally.
func TestTablesAreScrollable(t *testing.T) {
	files, _ := fs.Glob(tmplFS, "templates/*.html")
	tableRe := regexp.MustCompile(`<table`)
	wrapRe := regexp.MustCompile(`(?s)<div class="table-scroll">\s*<table`)
	for _, f := range files {
		src := string(mustRead(t, f))
		tables := len(tableRe.FindAllString(src, -1))
		wrapped := len(wrapRe.FindAllString(src, -1))
		if tables != wrapped {
			t.Errorf("%s: %d <table> but %d wrapped in .table-scroll — every table must sit in a .table-scroll container", f, tables, wrapped)
		}
	}
}

// TestMoneyGrammar enforces the money rule (§7): every amount renders through the
// single `money` helper, which scales the kernel's base-unit integer by the
// world's own decimals and names it with the world's own symbol. A symbol the UI
// picks is a claim about a world it does not get to make, so `$` before a figure
// is banned along with the ¤ character and the retired gem/diamond mark. A
// hard-coded `step` is the same mistake in an input: how finely money divides is
// the world's fact, and `amountStep` is where it is read.
func TestMoneyGrammar(t *testing.T) {
	dollarAmount := regexp.MustCompile(`\$\s*(?:[0-9]|\{\{\s*money)`)
	fixedStep := regexp.MustCompile(`step="[0-9.]+"`)
	files, _ := fs.Glob(tmplFS, "templates/*.html")
	for _, f := range files {
		src := string(mustRead(t, f))
		if strings.ContainsRune(src, '¤') {
			t.Errorf("%s: the ¤ glyph is banned — render amounts with the {{money}} helper (§7)", f)
		}
		if strings.Contains(src, "{{gem}}") || strings.Contains(src, "#gem") {
			t.Errorf("%s: the gem/diamond mark is retired — money renders via {{money}} (§7)", f)
		}
		if m := dollarAmount.FindString(src); m != "" {
			t.Errorf("%s: %q — the symbol is the world's, rendered by {{money}}, never one the UI picks (§7)", f, m)
		}
		// A schema-derived number field may fix its own step; an amount may not.
		if m := fixedStep.FindString(src); m != "" && strings.Contains(src, "amountInput") || (m != "" && strings.Contains(src, `name="amount"`)) {
			t.Errorf("%s: %q — an amount's step is the world's own, from {{amountStep}} (§7)", f, m)
		}
	}
}

// The kernel retired a whole vocabulary in v0.14, and more of it at 25c07cd: a
// peer holds no money, so there is no position to settle, no queue of
// unresolved transfers, and no way to fund one; an obligation closes when its
// buyer pays, so no operator confirms it; the admin surface left /control/*
// entirely. An application is switched off by path rather than unimported, and
// withdrawing is a user's own verb. A guard, because a template or handler
// outliving its endpoint fails only when someone clicks it.
func TestNoRetiredSurfaces(t *testing.T) {
	retired := map[string]string{
		"/admin/peers/settle":    "settlement is per call on the rail, by ticket (§6.6)",
		"/admin/withdraw":        "withdrawing is a user's verb (§6.5)",
		"admin-withdraw":         "withdrawing is a user's verb (§6.5)",
		"/control/":              "the admin surface is /v1/admin/* (§3.1)",
		"fund_key":               "a peer is never credited (§6.6)",
		"FundKey":                "a peer is never credited (§6.6)",
		"/actions/unimport":      "an application is disabled or deleted by path (§6.4)",
		"positionText":           "a peer holds no money (§6.6)",
		"peer_credit":            "a peer holds no money (§6.6)",
		"dollarsInput":           "amounts scale by the world's decimals (§7)",
		"parseDollars":           "amounts scale by the world's decimals (§7)",
		"settlement_id":          "there is no bilateral settlement to record (§6.6)",
		"/v1/stats":              "what is known about an action is read where it is read (§6.2)",
		"ActionStats":            "what is known about an action is read where it is read (§6.2)",
		"refund_eligible":        "a parked call settles on the peer's answer; no age expires it (§6.2)",
		"RefundEligible":         "a parked call settles on the peer's answer; no age expires it (§6.2)",
		"mergeActivity":          "the ledger journals every movement; it alone is the activity (§6.5)",
		"owner_handle":           "every party is an address, named for its role (§4)",
		"caller_handle":          "every party is an address, named for its role (§4)",
		"target_handle":          "every party is an address, named for its role (§4)",
		"required_caller_handle": "every party is an address, named for its role (§4)",
		"operator_handle":        "every party is an address, named for its role (§4)",
		"party_handle":           "every party is an address, named for its role (§4)",
		"network_digest":         "the kernel pins a network fingerprint (§5)",
		"rail_address":           "the kernel names a blockchain address (§6.5)",
		"ActionsByID":            "a transaction carries its action's address (§6.5)",
		"refIsRemote":            "an action is local iff its kernel segment is this kernel's name (§4)",
		"/v1/steps":              "a step is a task (§6.3)",
		"/v1/me/address\"":       "the address route is /v1/me/blockchain-address (§6.5)",
		"/v1/transfers":          "sending money is running the transfer action (§6.5)",
		"CompletePeerStep":       "work addressed to this kernel reaches its operator's inbox (§6.6)",
		"steps_truncated":        "the inspection carries no tasks (§6.6)",
		"step-card":              "a step is a task (§6.3)",
		"step-result-":           "a step is a task (§6.3)",
	}
	var files []string
	for _, pat := range []string{"templates/*.html", "*.go"} {
		f, _ := fs.Glob(tmplFS, pat)
		files = append(files, f...)
	}
	// The routes live in the client and the apps as much as here.
	var goFiles []string
	for _, pat := range []string{"*.go", "../client/*.go", "../apps/*/*.go", "../apps/*/*/*.go"} {
		f, _ := filepath.Glob(pat)
		goFiles = append(goFiles, f...)
	}
	for _, f := range files {
		src := string(mustRead(t, f))
		for token, why := range retired {
			if strings.Contains(src, token) {
				t.Errorf("%s: %q is retired — %s", f, token, why)
			}
		}
	}
	for _, f := range goFiles {
		if strings.HasSuffix(f, "_test.go") {
			continue // a test may name a retired surface to assert its absence
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for token, why := range retired {
			if strings.Contains(string(b), token) {
				t.Errorf("%s: %q is retired — %s", f, token, why)
			}
		}
	}
}

// TestBackLinksLiveInCrumbs enforces the crumb principle ("the way up never
// scrolls away", §5.3): every ←-back-link on a detail page sits inside the sticky
// <nav class="crumb"> component — never a bare paragraph. Pager links (.pager-link,
// "← previous") are the one sanctioned ← outside a crumb.
func TestBackLinksLiveInCrumbs(t *testing.T) {
	core, _ := fs.Glob(tmplFS, "templates/*.html")
	app, _ := filepath.Glob("../apps/*/templates/*.html")
	crumbRe := regexp.MustCompile(`(?s)<nav class="crumb">.*?</nav>`)
	backRe := regexp.MustCompile(`<a[^>]*>←`)
	read := func(f string, isApp bool) string {
		if isApp {
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			return string(b)
		}
		return string(mustRead(t, f))
	}
	check := func(f string, isApp bool) {
		src := read(f, isApp)
		crumbs := crumbRe.FindAllStringIndex(src, -1)
		for _, m := range backRe.FindAllStringIndex(src, -1) {
			if strings.Contains(src[m[0]:m[1]], "pager-link") {
				continue
			}
			inCrumb := false
			for _, c := range crumbs {
				if m[0] >= c[0] && m[1] <= c[1] {
					inCrumb = true
					break
				}
			}
			if !inCrumb {
				t.Errorf("%s: ← back-link outside <nav class=\"crumb\"> — the way up must never scroll away (grammar §5.3): …%s…", f, snippet(src, m[0]))
			}
		}
	}
	for _, f := range core {
		check(f, false)
	}
	for _, f := range app {
		check(f, true)
	}
}

// TestNoRowThumbnails enforces §5.13: cardArt has exactly two homes — the action
// card and the page hero (.media). The retired 3.4rem row thumbnail (.thumb-s) is
// unreadable at row scale and must not reappear; a row identifies itself by its
// ref and chips. The retired chat frame components are banned with it.
func TestNoRowThumbnails(t *testing.T) {
	core, _ := fs.Glob(tmplFS, "templates/*.html")
	app, _ := filepath.Glob("../apps/*/templates/*.html")
	banned := []string{"thumb-s", "chat-frame", "chat-back", `class="rooms"`, "thread-head"}
	check := func(f, src string) {
		for _, bad := range banned {
			if strings.Contains(src, bad) {
				t.Errorf("%s: retired component %q must not reappear (grammar §5.13 / chat regrammar)", f, bad)
			}
		}
	}
	for _, f := range core {
		check(f, string(mustRead(t, f)))
	}
	for _, f := range app {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		check(f, string(b))
	}
}

// TestContainerIsTheOnlyWidthDecision enforces the width rule: the content
// container is the ONE width decision — in-flow components fill it, never capping
// themselves (no narrow strips inside broad pages). A max-width in any length
// unit (px, ch, em, rem, vw) is allowed
// only on the container itself, the topbar omnibox pill, top-layer overlays
// (dialogs, the live region), and the bare entry card.
func TestContainerIsTheOnlyWidthDecision(t *testing.T) {
	b, err := staticFS.ReadFile("static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	src := regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(string(b), "")
	ruleRe := regexp.MustCompile(`(?s)([^{}]+)\{[^{}]*max-width:\s*[\d.]+(px|ch|r?em|vw)`)
	allowed := regexp.MustCompile(`^(\.container|\.omni|\.auth-card|\.live-region|dialog\.)`)
	for _, m := range ruleRe.FindAllStringSubmatch(src, -1) {
		sel := strings.TrimSpace(m[1])
		if !allowed.MatchString(sel) {
			t.Errorf("app.css: %q carries a length max-width — the container is the only width decision; in-flow components fill it (grammar §Main area)", sel)
		}
	}
}

// TestNoRetiredRawToggle ensures the old "edit as JSON" text-button toggle and its
// .raw-switch wrapper are gone — the input-mode toggle is the one .switch
// tab-group switch (fields | JSON), identical on action-run and step-complete forms.
func TestNoRetiredRawToggle(t *testing.T) {
	files, _ := fs.Glob(tmplFS, "templates/*.html")
	for _, f := range files {
		src := string(mustRead(t, f))
		if strings.Contains(src, "edit as JSON") || strings.Contains(src, "raw-switch") || strings.Contains(src, "data-raw-toggle") || strings.Contains(src, "data-raw-seg") || strings.Contains(src, "field-switch") || strings.Contains(src, "json-toggle") || strings.Contains(src, "aria-pressed") {
			t.Errorf("%s: retired raw toggle — use the one .switch (_json_switch)", f)
		}
	}
}

// Copy discipline (grammar §3). Prose is rationed like color: a subtitle orients
// in one phrase; a prose note discloses one thing; neither narrates how the kernel
// works. These guards fail the build the moment a string swells back into a
// paragraph, so the narration cannot silently creep back.

var (
	scopeNoteRe = regexp.MustCompile(`(?s)<span class="scope-note">.*?</span>`)
	subtitleRe  = regexp.MustCompile(`(?s)<p class="subtitle[^"]*"[^>]*>(.*?)</p>`)
	proseNoteRe = regexp.MustCompile(`(?s)<p class="[^"]*\b(?:help|card-note)\b[^"]*"[^>]*>(.*?)</p>`)
	tagRe       = regexp.MustCompile(`<[^>]*>`)
	// A sentence-ender followed by more words → a second sentence.
	secondSentenceRe = regexp.MustCompile(`[.;!?]\s+\S`)
)

// visibleText strips the scope-note span, template actions, and tags, then collapses
// whitespace — leaving only the literal words a reader would see at render time.
func visibleText(html string) string {
	s := scopeNoteRe.ReplaceAllString(html, "")
	s = tmplActionRe.ReplaceAllString(s, "")
	s = tagRe.ReplaceAllString(s, "")
	return strings.Join(strings.Fields(s), " ")
}

// TestSubtitlesHoldNoProse enforces §3: a page is not introduced. A subtitle
// holds only a record's identity (template output: an id, a ref, a key) or a
// superuser scope note, so once both are stripped no literal word remains.
func TestSubtitlesHoldNoProse(t *testing.T) {
	files, _ := fs.Glob(tmplFS, "templates/*.html")
	for _, f := range files {
		src := string(mustRead(t, f))
		for _, m := range subtitleRe.FindAllStringSubmatch(src, -1) {
			if txt := visibleText(m[1]); txt != "" {
				t.Errorf("%s: subtitle holds prose — a page is not introduced; a subtitle is an identity or a scope note (grammar §3): %q", f, txt)
			}
		}
	}
}

// TestProseNotesAreShort enforces §3: a help/card-note prose note discloses one thing
// at the point of consequence; it does not swell into a paragraph explaining a
// app. Counterparty content (a field .Description, a rating .Note) is template-
// only and strips to empty, so this lints the chrome, not untrusted payload.
func TestProseNotesAreShort(t *testing.T) {
	const maxRunes = 120
	files, _ := fs.Glob(tmplFS, "templates/*.html")
	for _, f := range files {
		src := string(mustRead(t, f))
		for _, m := range proseNoteRe.FindAllStringSubmatch(src, -1) {
			txt := visibleText(m[1])
			if n := utf8.RuneCountInString(txt); n > maxRunes {
				t.Errorf("%s: prose note runs to %d runes (max %d) — cut narration, disclose one thing (grammar §3): %q", f, n, maxRunes, txt)
			}
		}
	}
}

// TestAppTemplatesFollowGrammar extends the drift guard to app-module
// templates (apps/*/templates/*.html): the visual grammar is central, so a
// module's templates obey the same button, inline-style, table, and
// retired-component rules as the core, wherever the file lives.
func TestAppTemplatesFollowGrammar(t *testing.T) {
	files, err := filepath.Glob("../apps/*/templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Skip("no app templates")
	}
	tableRe := regexp.MustCompile(`<table`)
	wrapRe := regexp.MustCompile(`(?s)<div class="table-scroll">\s*<table`)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		if loc := inlineStyleRe.FindStringIndex(src); loc != nil {
			t.Errorf("%s: inline style is forbidden (grammar §6): …%s…", f, snippet(src, loc[0]))
		}
		for _, m := range classAttrRe.FindAllStringSubmatch(src, -1) {
			cleaned := tmplActionRe.ReplaceAllString(m[1], " ")
			for _, tok := range strings.Fields(cleaned) {
				if strings.HasPrefix(tok, "btn") && !allowedButtonClasses[tok] {
					t.Errorf("%s: off-grammar button class %q (allowed: btn, btn-money, btn-danger, btn-icon, btn-quiet)", f, tok)
				}
			}
		}
		if tables, wrapped := len(tableRe.FindAllString(src, -1)), len(wrapRe.FindAllString(src, -1)); tables != wrapped {
			t.Errorf("%s: %d <table> but %d wrapped in .table-scroll", f, tables, wrapped)
		}
		for _, bad := range []string{`class="toolbar"`, "btn-primary", "linkbar", "auth-tab"} {
			if strings.Contains(src, bad) {
				t.Errorf("%s: retired component %q must not reappear", f, bad)
			}
		}
		// Copy discipline (§3) applies wherever a template lives: an app's
		// subtitle holds no prose either.
		for _, m := range subtitleRe.FindAllStringSubmatch(src, -1) {
			if txt := visibleText(m[1]); txt != "" {
				t.Errorf("%s: subtitle holds prose (grammar §3): %q", f, txt)
			}
		}
		// Prose notes cap on length only (like the core §3 test): a legitimate
		// disclosure may be two short sentences ("Costs 20 credits. Refunded if it
		// fails."), so sentence count is not the axis — narration is cut by review
		// and the written guide, length stops a paragraph.
		for _, m := range proseNoteRe.FindAllStringSubmatch(src, -1) {
			txt := visibleText(m[1])
			if n := utf8.RuneCountInString(txt); n > 120 {
				t.Errorf("%s: prose note runs to %d runes (max 120) — cut narration, disclose one thing (grammar §3): %q", f, n, txt)
			}
		}
	}
}

// sigilRe finds an "@" used as a sigil on a name: one that does not join to a
// preceding name character. An address — handle@kernel, owner@kernel/name (§4) —
// joins its "@" to the handle, a literal one or a template action's "}}", and
// is the only legitimate "@" a template may carry.
var sigilRe = regexp.MustCompile(`(^|[^A-Za-z0-9_}])@[A-Za-z{]`)

// TestTemplatesNeverDecorateNames is the drift guard for requirements.md §4:
// every name is an address the kernel served, and the UI renders it as it is,
// never decorated. The agents app once rendered "@{{.Handle}}" on every card,
// heading, and confirm, which made the UI disagree with the platform it
// presents and forced a matching strip on input, so the two defects held each
// other up. A sigil in a template is a defect, not a style.
func TestTemplatesNeverDecorateNames(t *testing.T) {
	core, err := fs.Glob(tmplFS, "templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	app, err := filepath.Glob("../apps/*/templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	if len(core) == 0 {
		t.Fatal("no core templates found")
	}
	for _, f := range append(core, app...) {
		var b []byte
		if strings.HasPrefix(f, "..") {
			b, err = os.ReadFile(f)
		} else {
			b, err = tmplFS.ReadFile(f)
		}
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		for _, loc := range sigilRe.FindAllStringIndex(src, -1) {
			t.Errorf("%s: a name carries an @ sigil — names are addresses rendered as served (§4); an @ only joins handle@kernel: …%s…", f, snippet(src, loc[0]))
		}
	}
}

func mustRead(t *testing.T, f string) []byte {
	t.Helper()
	b, err := tmplFS.ReadFile(f)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func snippet(s string, at int) string {
	lo := at - 30
	if lo < 0 {
		lo = 0
	}
	hi := at + 30
	if hi > len(s) {
		hi = len(s)
	}
	return s[lo:hi]
}

var tmplComment = regexp.MustCompile(`(?s)\{\{/\*.*?\*/\}\}`)

// TestOutcomeCopyIsOurs guards the §6.2 rule that the UI owns its failure
// sentences. The kernel classifies rather than narrates (v0.12.14: a remote
// failure answers "execution_failed"), so a raw class must never reach a
// template — and the outcome panel must not print the kernel's message
// alongside our sentence, which is how a class leaked to buyers before.
func TestOutcomeCopyIsOurs(t *testing.T) {
	raw, err := tmplFS.ReadFile("templates/_partials.html")
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	start := strings.Index(body, `{{define "_call_outcome"}}`)
	if start < 0 {
		t.Fatal("_call_outcome not found")
	}
	rest := body[start+len(`{{define "_call_outcome"}}`):]
	if next := strings.Index(rest, "{{define "); next >= 0 {
		rest = rest[:next]
	}
	// Comments explain the rule; only rendered markup is linted.
	outcome := tmplComment.ReplaceAllString(rest, "")

	// A kernel error class is machine vocabulary; it explains nothing to a buyer.
	for _, code := range []string{"execution_failed", "invalid_state", "terms_changed", "peer_unreachable", "peer_unfunded"} {
		if strings.Contains(outcome, code) {
			t.Errorf("the outcome panel must not render the kernel class %q", code)
		}
	}
	// .Detail is the raw kernel message. It may inform on a rejection (a schema
	// complaint names the field), but never on a failure, where it is the class.
	if strings.Contains(outcome, "{{.Detail}}") {
		t.Error("the outcome panel must not print the raw kernel message: the sentence is ours (§6.2)")
	}
}

// TestChipsAreOneLetterOrOneIcon guards §5.5: a chip is a mark, not a caption.
// Its body is a single character or an <svg>; the meaning lives in its title.
// Word chips crept back in repeatedly ("known network", "local", "inactive",
// "last failed", "unused"), each one a phrase where a glyph would do.
func TestChipsAreOneLetterOrOneIcon(t *testing.T) {
	chip := regexp.MustCompile(`(?s)<span class="chip[^"]*"[^>]*>(.*?)</span>`)
	files, _ := fs.Glob(tmplFS, "templates/*.html")
	for _, f := range files {
		b, err := tmplFS.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range chip.FindAllStringSubmatch(string(b), -1) {
			body := strings.TrimSpace(m[1])
			switch {
			case strings.HasPrefix(body, "<svg"), strings.HasPrefix(body, "{{"):
				continue // an icon, or a helper that yields one letter (kindBadge)
			case len([]rune(body)) <= 1:
				continue // a single letter
			}
			t.Errorf("%s: chip body %q must be one letter or one icon, not a word", f, body)
		}
	}
}

// The world is named wherever the interface names itself. A kernel serves one
// world for life, and the world decides what the money on every screen is:
// whether it can leave, and what it is worth. The symbol alone does not say it —
// "credits" is not "not redeemable" — so a person who never sees the world
// cannot tell a rehearsal from a live market (§7).
func TestChromeNamesTheWorld(t *testing.T) {
	b, err := fs.ReadFile(tmplFS, "templates/layout.html")
	if err != nil {
		t.Fatal(err)
	}
	body := string(b)
	// Both renderings of the one nav model: the sidebar and the mobile drawer.
	if n := strings.Count(body, "{{.KernelNet}}"); n != 2 {
		t.Errorf("the brand names the world in both renderings; found %d", n)
	}
	if !strings.Contains(body, `<span class="world">`) {
		t.Error("the world renders through the named class, never ad hoc (§2)")
	}
}

// The truncate helper owns the ellipsis that marks a shortened id, so a
// template never writes a second one after it (it rendered "451333e4……").
func TestTruncateOwnsItsEllipsis(t *testing.T) {
	core, _ := fs.Glob(tmplFS, "templates/*.html")
	app, _ := filepath.Glob("../apps/*/templates/*.html")
	twice := regexp.MustCompile(`\{\{truncate [^}]*\}\}…`)
	for _, f := range core {
		if m := twice.FindString(string(mustRead(t, f))); m != "" {
			t.Errorf("%s: %q adds a second ellipsis", f, m)
		}
	}
	for _, f := range app {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if m := twice.FindString(string(b)); m != "" {
			t.Errorf("%s: %q adds a second ellipsis", f, m)
		}
	}
}

// TestHeadingCase enforces §3: one capitalization for every heading. A page
// title (<h1>) and a section heading (<h2>) are both in sentence case — the
// first letter capital, the rest as written. Only literal words are checked; a
// title rendered from data keeps its own case.
func TestHeadingCase(t *testing.T) {
	core, _ := fs.Glob(tmplFS, "templates/*.html")
	apps, _ := filepath.Glob("../apps/*/templates/*.html")
	headRe := regexp.MustCompile(`(?s)<h[12][^>]*>(.*?)</h[12]>`)
	// A chip is a one-letter mark beside a name (§5.5), not a word of it.
	chipRe := regexp.MustCompile(`(?s)<span class="chip[^"]*"[^>]*>.*?</span>`)
	check := func(f, src string) {
		for _, m := range headRe.FindAllStringSubmatch(src, -1) {
			txt := visibleText(chipRe.ReplaceAllString(m[1], ""))
			if r, _ := utf8.DecodeRuneInString(txt); unicode.IsLetter(r) && !unicode.IsUpper(r) {
				t.Errorf("%s: heading %q must be in sentence case (grammar §3)", f, txt)
			}
		}
	}
	for _, f := range core {
		check(f, string(mustRead(t, f)))
	}
	for _, f := range apps {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		check(f, string(b))
	}
}

// TestButtonsHaveOneGeometryAndOneLook enforces §5.1 on every core and app
// template: no size modifier on a button, no quiet-danger tier, no second
// command-row container, and every confirm a single plain question.
func TestButtonsHaveOneGeometryAndOneLook(t *testing.T) {
	core, _ := fs.Glob(tmplFS, "templates/*.html")
	apps, _ := filepath.Glob("../apps/*/templates/*.html")
	sizedRe := regexp.MustCompile(`class="btn(?:-money|-danger|-quiet|-icon)?\b[^"]*\bsmall\b`)
	confirmRe := regexp.MustCompile(`data-confirm="([^"]*)"`)
	check := func(f, src string) {
		if loc := sizedRe.FindStringIndex(src); loc != nil {
			t.Errorf("%s: a button carries a size modifier — all buttons share one geometry (grammar §5.1): …%s…", f, snippet(src, loc[0]))
		}
		for _, bad := range []string{"btn-quiet danger", "row-foot"} {
			if strings.Contains(src, bad) {
				t.Errorf("%s: %q is retired — a destructive command is .btn-danger, a command row is .cluster (grammar §5.1)", f, bad)
			}
		}
		for _, m := range confirmRe.FindAllStringSubmatch(src, -1) {
			q := visibleText(m[1])
			if !strings.HasSuffix(q, "?") || secondSentenceRe.MatchString(q) {
				t.Errorf("%s: confirm %q must be one plain question (grammar §5.8)", f, m[1])
			}
		}
	}
	for _, f := range core {
		check(f, string(mustRead(t, f)))
	}
	for _, f := range apps {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		check(f, string(b))
	}
}

// Cancel is one control everywhere: a real neutral button that closes its
// dialog (grammar §5.1), never a link styled as one — the dialog's × link is
// the no-JS way out.
func TestCancelIsOneButton(t *testing.T) {
	core, _ := fs.Glob(tmplFS, "templates/*.html")
	apps, _ := filepath.Glob("../apps/*/templates/*.html")
	cancelRe := regexp.MustCompile(`<(\w+)([^>]*)>\s*Cancel\s*</\w+>`)
	for _, f := range append(core, apps...) {
		b, err := fs.ReadFile(tmplFS, f)
		if err != nil {
			b, err = os.ReadFile(f)
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range cancelRe.FindAllStringSubmatch(string(b), -1) {
			if m[1] != "button" || !strings.Contains(m[2], `type="button"`) || !strings.Contains(m[2], `class="btn"`) {
				t.Errorf("%s: %s — Cancel is <button type=\"button\" class=\"btn\"> (grammar §5.1)", f, m[0])
			}
		}
	}
}

// A chain address renders as wallets show it, through _addr (0x7099…79c8 with
// the whole value copyable), never raw as element text (requirements §7).
func TestChainAddressesRenderShort(t *testing.T) {
	core, _ := fs.Glob(tmplFS, "templates/*.html")
	raw := regexp.MustCompile(`>\{\{[$\w.]*\.(BlockchainAddress|MyAddress|KernelAddress|Token|Party)\}\}<`)
	for _, f := range core {
		if m := raw.FindString(string(mustRead(t, f))); m != "" {
			t.Errorf("%s: %q renders a chain address whole — use the _addr partial", f, m)
		}
	}
}

// A JSON value a person reads renders only through the view (§7): the
// `json` helper appears in no template outside `_value_block`, and the row
// attributes the shared mechanism replaced do not return.
func TestJSONRendersThroughTheView(t *testing.T) {
	files, _ := fs.Glob(tmplFS, "templates/*.html")
	for _, f := range files {
		src := string(mustRead(t, f))
		if i := strings.Index(src, `{{define "_value_block"}}`); i >= 0 {
			end := strings.Index(src[i:], "\n")
			src = src[:i] + src[i+end:]
		}
		if strings.Contains(src, "{{json ") {
			t.Errorf("%s: JSON renders through _value_block, not the json helper", f)
		}
		if strings.Contains(src, "data-param-") {
			t.Errorf("%s: retired data-param-* rows — use [data-rows]", f)
		}
	}
}

// The command palette is retired: commands are rows of the search pill (§6.1),
// so no template opens a palette, renders its rows, or binds its shortcut.
func TestNoPaletteSurface(t *testing.T) {
	files, _ := fs.Glob(tmplFS, "templates/*.html")
	for _, f := range files {
		src := string(mustRead(t, f))
		for _, gone := range []string{"data-open-palette", "palette-item", `id="palette"`, "Ctrl</kbd> <kbd>K"} {
			if strings.Contains(src, gone) {
				t.Errorf("%s: the palette is retired (%q); commands live in the search pill", f, gone)
			}
		}
	}
}
