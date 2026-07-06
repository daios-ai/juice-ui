package server

import (
	"os"
	"testing"
)

// TestThemesCSSInSync guards the committed static/themes.css against drift: it
// must byte-match the generator output, so a change to a palette or the mapping
// that isn't regenerated (make themes) fails here rather than shipping stale CSS.
func TestThemesCSSInSync(t *testing.T) {
	want := GenerateThemesCSS()
	got, err := os.ReadFile("static/themes.css")
	if err != nil {
		t.Fatalf("read static/themes.css: %v", err)
	}
	if string(got) != want {
		t.Fatalf("static/themes.css is stale — run `make themes` to regenerate")
	}
}

// TestThemeParsing asserts every registered theme parses with a full palette and
// base colors.
func TestThemeParsing(t *testing.T) {
	if len(ThemeList()) != len(themeMeta) {
		t.Fatalf("loaded %d themes, want %d", len(ThemeList()), len(themeMeta))
	}
	for _, p := range ThemeList() {
		if p.Slug == "" || p.Name == "" {
			t.Errorf("theme missing slug/name: %+v", p.Slug)
		}
		if (p.Background == RGB{}) && (p.Foreground == RGB{}) {
			t.Errorf("%s: no base colors parsed", p.Slug)
		}
	}
}

// TestDefaultThemesExist ensures the two default slugs resolve.
func TestDefaultThemesExist(t *testing.T) {
	if !ValidTheme(defaultLightTheme) {
		t.Errorf("default light theme %q not found", defaultLightTheme)
	}
	if !ValidTheme(defaultDarkTheme) {
		t.Errorf("default dark theme %q not found", defaultDarkTheme)
	}
}

// TestThemeContrast is the accessibility gate: no theme ships unless its derived
// tokens clear the required WCAG ratios against the background, and the accent is
// visually distinct from every money-status color (so accent can never be
// mistaken for a financial signal).
func TestThemeContrast(t *testing.T) {
	for _, p := range ThemeList() {
		tk := p.tokens()
		bg := tk["--bg"]
		// body text and muted text: 4.5:1
		for _, k := range []string{"--text", "--text-muted"} {
			if r := contrast(bg, tk[k]); r < 4.5 {
				t.Errorf("%s: %s contrast %.2f < 4.5", p.Slug, k, r)
			}
		}
		// status + accent: large/affordance text, 3.0:1
		for _, k := range []string{"--credited", "--refunded", "--escrow", "--danger", "--accent"} {
			if r := contrast(bg, tk[k]); r < 3.0 {
				t.Errorf("%s: %s contrast %.2f < 3.0", p.Slug, k, r)
			}
		}
		// accent must differ from each status color
		for _, k := range []string{"--credited", "--refunded", "--escrow", "--danger"} {
			if tk["--accent"] == tk[k] {
				t.Errorf("%s: --accent equals %s (%s)", p.Slug, k, tk[k].hex())
			}
		}
		// accent-contrast must be readable on the accent
		if r := contrast(tk["--accent"], tk["--accent-contrast"]); r < 3.0 {
			t.Errorf("%s: accent-contrast %.2f < 3.0 on accent", p.Slug, r)
		}
		// danger-contrast must be readable on a filled danger button (solid
		// destructive buttons, grammar §5.1)
		if r := contrast(tk["--danger"], tk["--danger-contrast"]); r < 3.0 {
			t.Errorf("%s: danger-contrast %.2f < 3.0 on danger", p.Slug, r)
		}
	}
}
