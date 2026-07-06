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
	"btn": true, "btn-primary": true, "btn-money": true, "btn-danger": true,
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
					t.Errorf("%s: off-grammar button class %q in class=%q (allowed: btn, btn-primary, btn-money, btn-danger, btn-icon, btn-quiet)", f, tok, m[1])
				}
			}
		}
	}
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
