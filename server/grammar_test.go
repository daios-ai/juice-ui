package server

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
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

// TestNoCurrencyGlyph enforces the money grammar (§7): every amount renders in
// dollars through the single `money` helper (`$N.NNN`), and a spend button reads
// `verb ($N.NNN)`. Neither the ¤ character nor the retired gem/diamond mark (the
// `gem` helper or `#gem` sprite) may appear in any template — both are regressions
// of superseded marks.
func TestNoCurrencyGlyph(t *testing.T) {
	files, _ := fs.Glob(tmplFS, "templates/*.html")
	for _, f := range files {
		src := string(mustRead(t, f))
		if strings.ContainsRune(src, '¤') {
			t.Errorf("%s: the ¤ glyph is banned — render amounts with the {{money}} helper (§7)", f)
		}
		if strings.Contains(src, "{{gem}}") || strings.Contains(src, "#gem") {
			t.Errorf("%s: the gem/diamond mark is retired — money renders in dollars via {{money}} (§7)", f)
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
// themselves (no narrow strips inside broad pages). Pixel max-widths are allowed
// only on the container itself, the topbar omnibox pill, top-layer overlays
// (dialogs, the live region), and the bare entry card.
func TestContainerIsTheOnlyWidthDecision(t *testing.T) {
	b, err := staticFS.ReadFile("static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	src := regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(string(b), "")
	ruleRe := regexp.MustCompile(`(?s)([^{}]+)\{[^{}]*max-width:\s*\d+px`)
	allowed := regexp.MustCompile(`^(\.container|\.omni|\.auth-card|\.live-region|dialog\.)`)
	for _, m := range ruleRe.FindAllStringSubmatch(src, -1) {
		sel := strings.TrimSpace(m[1])
		if !allowed.MatchString(sel) {
			t.Errorf("app.css: %q carries a px max-width — the container is the only width decision; in-flow components fill it (grammar §Main area)", sel)
		}
	}
}

// TestNoRetiredRawToggle ensures the old "edit as JSON" text-button toggle and its
// .raw-switch wrapper are gone — the input-mode toggle is the .field-switch bare
// tab-group switch (fields | JSON), identical on action-run and step-complete forms.
func TestNoRetiredRawToggle(t *testing.T) {
	files, _ := fs.Glob(tmplFS, "templates/*.html")
	for _, f := range files {
		src := string(mustRead(t, f))
		if strings.Contains(src, "edit as JSON") || strings.Contains(src, "raw-switch") || strings.Contains(src, "data-raw-toggle") {
			t.Errorf("%s: retired raw toggle — use the .field-switch (fields | JSON) switch", f)
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

// TestSubtitlesAreOrienting enforces §3: a page-head/section subtitle orients in one
// short phrase and a single sentence — it never narrates kernel or control behavior.
// Detail-page subtitles carry only an id (template-only), so they strip to empty.
func TestSubtitlesAreOrienting(t *testing.T) {
	const maxRunes = 64
	files, _ := fs.Glob(tmplFS, "templates/*.html")
	for _, f := range files {
		src := string(mustRead(t, f))
		for _, m := range subtitleRe.FindAllStringSubmatch(src, -1) {
			txt := visibleText(m[1])
			if txt == "" {
				continue // an id or otherwise template-only subtitle
			}
			if n := utf8.RuneCountInString(txt); n > maxRunes {
				t.Errorf("%s: subtitle runs to %d runes (max %d) — orient in one phrase, don't narrate (grammar §3): %q", f, n, maxRunes, txt)
			}
			if secondSentenceRe.MatchString(txt) {
				t.Errorf("%s: subtitle spans multiple sentences — orient in one phrase, don't narrate (grammar §3): %q", f, txt)
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
		// Copy discipline (§3) applies wherever a template lives: an app's subtitle
		// orients in one phrase and its help note discloses one thing — neither
		// narrates how the kernel/daemon works. Same guards as the core (§3 tests).
		for _, m := range subtitleRe.FindAllStringSubmatch(src, -1) {
			txt := visibleText(m[1])
			if txt == "" {
				continue
			}
			if n := utf8.RuneCountInString(txt); n > 64 {
				t.Errorf("%s: subtitle runs to %d runes (max 64) — orient in one phrase, don't narrate (grammar §3): %q", f, n, txt)
			}
			if secondSentenceRe.MatchString(txt) {
				t.Errorf("%s: subtitle spans multiple sentences — orient in one phrase, don't narrate (grammar §3): %q", f, txt)
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
