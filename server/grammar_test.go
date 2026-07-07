package server

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
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
