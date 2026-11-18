package server

import (
	"os"
	"strings"
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
		for _, k := range []string{"--credited", "--refunded", "--escrow", "--danger", "--accent", "--range-good", "--range-bad"} {
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

// TestShippedThemes holds the shipped set (§7): the ten terminal families, each
// in one dark and one light version, dark first, and nothing else. No theme
// overrides a structural token.
func TestShippedThemes(t *testing.T) {
	want := []struct {
		slug string
		dark bool
	}{
		{"catppuccin-mocha", true}, {"catppuccin-latte", false},
		{"tokyo-night", true}, {"tokyo-night-day", false},
		{"gruvbox-dark", true}, {"gruvbox-light", false},
		{"nord", true}, {"nord-light", false},
		{"dracula", true}, {"dracula-alucard", false},
		{"solarized-dark", true}, {"solarized-light", false},
		{"one-dark", true}, {"one-light", false},
		{"rose-pine-moon", true}, {"rose-pine-dawn", false},
		{"kanagawa-wave", true}, {"kanagawa-lotus", false},
		{"everforest-dark", true}, {"everforest-light", false},
	}
	got := ThemeList()
	if len(got) != len(want) {
		t.Fatalf("%d themes shipped, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].Slug != w.slug || got[i].Dark != w.dark {
			t.Errorf("theme %d is %s (dark %v), want %s (dark %v)", i, got[i].Slug, got[i].Dark, w.slug, w.dark)
		}
		// The file agrees with the row: a dark theme's background is the darker
		// of its two base colors.
		if dark := relLum(got[i].Background) < relLum(got[i].Foreground); dark != w.dark {
			t.Errorf("%s: its palette is dark %v, registered dark %v", w.slug, dark, w.dark)
		}
	}
	for _, gone := range []string{"paper", "ink", "terminal", "github-light"} {
		if ValidTheme(gone) {
			t.Errorf("%s is still shipped", gone)
		}
	}
	if defaultLightTheme != "catppuccin-latte" || defaultDarkTheme != "catppuccin-mocha" {
		t.Errorf("defaults are %s and %s", defaultLightTheme, defaultDarkTheme)
	}
	css := GenerateThemesCSS()
	for _, structural := range []string{"--font-ui", "--radius"} {
		if strings.Contains(css, structural) {
			t.Errorf("the theme stylesheet overrides %s", structural)
		}
	}
}
