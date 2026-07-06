package server

import (
	"embed"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Themes are terminal color schemes in Ghostty's palette format (16 ANSI colors
// plus background/foreground/cursor/selection). The UI's semantic design tokens
// (--bg, --text, --credited, --accent, …) are derived from each palette by the
// mapping in GenerateThemesCSS, so any Ghostty theme drops in by adding its file
// to server/themes/ and one row to themeMeta. The generated stylesheet is
// static/themes.css; themes_test.go guards it against drift and asserts contrast.

//go:embed themes/*.ghostty
var themeFS embed.FS

// ANSIPalette is a parsed Ghostty theme: 16 ANSI slots plus the terminal's
// base colors. Slots 0-7 are the normal colors, 8-15 their bright siblings.
type ANSIPalette struct {
	Slug       string
	Name       string
	Dark       bool
	Palette    [16]RGB
	Background RGB
	Foreground RGB
	Cursor     RGB
	SelBG      RGB
	SelFG      RGB
	AccentSlot int            // ANSI slot used for --accent (distinct from 1/2/4/5)
	Overrides  map[string]RGB // hand-tuned token values for palettes the mapping can't salvage
}

// themeMeta is the curated registry: display name, dark/light, per-theme accent
// slot, and contrast overrides. Order here is the order shown in the picker
// (darks first, then lights, roughly by familiarity).
var themeMeta = []struct {
	Slug      string
	Name      string
	Dark      bool
	Accent    int
	Overrides map[string]RGB
}{
	// --- dark ---
	{Slug: "one-dark", Name: "One Dark", Dark: true, Accent: 6},
	{Slug: "dracula", Name: "Dracula", Dark: true, Accent: 6},
	{Slug: "nord", Name: "Nord", Dark: true, Accent: 6},
	{Slug: "gruvbox-dark", Name: "Gruvbox Dark", Dark: true, Accent: 3},
	{Slug: "solarized-dark", Name: "Solarized Dark", Dark: true, Accent: 6},
	{Slug: "tokyo-night", Name: "Tokyo Night", Dark: true, Accent: 6},
	{Slug: "tokyo-night-storm", Name: "Tokyo Night Storm", Dark: true, Accent: 6},
	{Slug: "catppuccin-mocha", Name: "Catppuccin Mocha", Dark: true, Accent: 6},
	{Slug: "catppuccin-macchiato", Name: "Catppuccin Macchiato", Dark: true, Accent: 6},
	{Slug: "rose-pine", Name: "Rosé Pine", Dark: true, Accent: 6},
	{Slug: "rose-pine-moon", Name: "Rosé Pine Moon", Dark: true, Accent: 6},
	{Slug: "github-dark", Name: "GitHub Dark", Dark: true, Accent: 6},
	{Slug: "adwaita-dark", Name: "Adwaita Dark", Dark: true, Accent: 6},
	{Slug: "ayu-mirage", Name: "Ayu Mirage", Dark: true, Accent: 6},
	{Slug: "everforest-dark", Name: "Everforest Dark", Dark: true, Accent: 3},
	{Slug: "kanagawa-wave", Name: "Kanagawa Wave", Dark: true, Accent: 6},
	{Slug: "monokai-pro", Name: "Monokai Pro", Dark: true, Accent: 3},
	{Slug: "night-owl", Name: "Night Owl", Dark: true, Accent: 6},
	{Slug: "zenburn", Name: "Zenburn", Dark: true, Accent: 3},
	{Slug: "flexoki-dark", Name: "Flexoki Dark", Dark: true, Accent: 6},
	// --- light --- (accent uses a non-status hue slot: 6=cyan→teal or 3=yellow→gold)
	{Slug: "github-light", Name: "GitHub Light", Dark: false, Accent: 6},
	{Slug: "catppuccin-latte", Name: "Catppuccin Latte", Dark: false, Accent: 6},
	{Slug: "gruvbox-light", Name: "Gruvbox Light", Dark: false, Accent: 3},
	{Slug: "solarized-light", Name: "Solarized Light", Dark: false, Accent: 6},
	{Slug: "rose-pine-dawn", Name: "Rosé Pine Dawn", Dark: false, Accent: 6},
	{Slug: "tokyo-night-day", Name: "Tokyo Night Day", Dark: false, Accent: 6},
	{Slug: "one-light", Name: "One Light", Dark: false, Accent: 6},
	{Slug: "everforest-light", Name: "Everforest Light", Dark: false, Accent: 3},
	{Slug: "alabaster", Name: "Alabaster", Dark: false, Accent: 6},
	{Slug: "flexoki-light", Name: "Flexoki Light", Dark: false, Accent: 6},
}

// Default themes chosen when the user has no explicit preference: the light one
// applies by default, the dark one under prefers-color-scheme: dark.
const (
	defaultLightTheme = "github-light"
	defaultDarkTheme  = "one-dark"
)

// loadThemes parses every registered theme once. It panics on a malformed or
// missing file because the theme set is embedded at build time — a failure here
// is a build error, caught by tests, never a runtime condition.
func loadThemes() []ANSIPalette {
	out := make([]ANSIPalette, 0, len(themeMeta))
	for _, m := range themeMeta {
		data, err := themeFS.ReadFile("themes/" + m.Slug + ".ghostty")
		if err != nil {
			panic(fmt.Sprintf("theme %s: %v", m.Slug, err))
		}
		p, err := parseGhostty(string(data))
		if err != nil {
			panic(fmt.Sprintf("theme %s: %v", m.Slug, err))
		}
		p.Slug, p.Name, p.Dark, p.AccentSlot, p.Overrides = m.Slug, m.Name, m.Dark, m.Accent, m.Overrides
		out = append(out, p)
	}
	return out
}

var themesCache = loadThemes()

// ThemeList returns the curated palettes in picker order.
func ThemeList() []ANSIPalette { return themesCache }

// ValidTheme reports whether slug names a shipped theme.
func ValidTheme(slug string) bool {
	for _, t := range themesCache {
		if t.Slug == slug {
			return true
		}
	}
	return false
}

// parseGhostty reads the `key = value` Ghostty theme format.
func parseGhostty(s string) (ANSIPalette, error) {
	var p ANSIPalette
	seen := [16]bool{}
	haveBG, haveFG := false, false
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key, val = strings.TrimSpace(key), strings.TrimSpace(val)
		switch {
		case key == "palette":
			idxStr, hex, ok := strings.Cut(val, "=")
			if !ok {
				continue
			}
			idx, err := strconv.Atoi(strings.TrimSpace(idxStr))
			if err != nil || idx < 0 || idx > 15 {
				continue
			}
			c, err := parseHex(strings.TrimSpace(hex))
			if err != nil {
				return p, err
			}
			p.Palette[idx], seen[idx] = c, true
		case key == "background":
			if c, err := parseHex(val); err == nil {
				p.Background, haveBG = c, true
			}
		case key == "foreground":
			if c, err := parseHex(val); err == nil {
				p.Foreground, haveFG = c, true
			}
		case key == "cursor-color":
			if c, err := parseHex(val); err == nil {
				p.Cursor = c
			}
		case key == "selection-background":
			if c, err := parseHex(val); err == nil {
				p.SelBG = c
			}
		case key == "selection-foreground":
			if c, err := parseHex(val); err == nil {
				p.SelFG = c
			}
		}
	}
	if !haveBG || !haveFG {
		return p, fmt.Errorf("missing background/foreground")
	}
	for i, ok := range seen {
		if !ok {
			return p, fmt.Errorf("missing palette slot %d", i)
		}
	}
	return p, nil
}

// RGB is a 24-bit color.
type RGB struct{ R, G, B uint8 }

func parseHex(s string) (RGB, error) {
	s = strings.TrimPrefix(s, "#")
	if len(s) != 6 {
		return RGB{}, fmt.Errorf("bad hex %q", s)
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return RGB{}, err
	}
	return RGB{uint8(v >> 16), uint8(v >> 8), uint8(v)}, nil
}

func (c RGB) hex() string { return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B) }

// mix blends a toward b by t in [0,1] in sRGB space (a visual nudge for surfaces,
// not a perceptual model).
func mix(a, b RGB, t float64) RGB {
	lerp := func(x, y uint8) uint8 {
		return uint8(math.Round(float64(x)*(1-t) + float64(y)*t))
	}
	return RGB{lerp(a.R, b.R), lerp(a.G, b.G), lerp(a.B, b.B)}
}

// relLum is the WCAG relative luminance of a color.
func relLum(c RGB) float64 {
	lin := func(v uint8) float64 {
		s := float64(v) / 255
		if s <= 0.03928 {
			return s / 12.92
		}
		return math.Pow((s+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(c.R) + 0.7152*lin(c.G) + 0.0722*lin(c.B)
}

// contrast is the WCAG contrast ratio between two colors (1..21).
func contrast(a, b RGB) float64 {
	l1, l2 := relLum(a), relLum(b)
	if l1 < l2 {
		l1, l2 = l2, l1
	}
	return (l1 + 0.05) / (l2 + 0.05)
}

// pickContrast returns whichever of the candidates has the highest contrast on bg.
func pickContrast(bg RGB, cands ...RGB) RGB {
	best, bestR := cands[0], contrast(bg, cands[0])
	for _, c := range cands[1:] {
		if r := contrast(bg, c); r > bestR {
			best, bestR = c, r
		}
	}
	return best
}

// ensureContrast nudges c until it clears min:1 against bg by blending toward the
// opposite extreme (black on a light bg, white on a dark bg). This darkens colors
// on light themes and lightens them on dark themes — the correct direction for
// legibility either way — so any theme can be made to meet WCAG.
func ensureContrast(c, bg RGB, min float64) RGB {
	if contrast(bg, c) >= min {
		return c
	}
	target := RGB{0, 0, 0}
	if relLum(bg) < 0.5 {
		target = RGB{255, 255, 255}
	}
	for t := 0.08; t <= 1.0001; t += 0.08 {
		if m := mix(c, target, t); contrast(bg, m) >= min {
			return m
		}
	}
	return target
}

// statusColor returns the palette slot for a money-status signal, nudged toward
// legibility until it clears min:1 against the background. It uses the normal
// (true-hue) slot rather than the bright sibling because some palettes (Solarized)
// put desaturated base grays in slots 8-15 — swapping there would turn a green
// "credited" signal gray. Status glyphs are large/bold text and affordances (≥3.0).
func statusColor(p ANSIPalette, slot int, min float64) RGB {
	return ensureContrast(p.Palette[slot], p.Background, min)
}

// mutedText derives --text-muted: fg dimmed toward bg as far as it can go while
// staying at 4.5:1 (body threshold), so secondary text reads as quieter but never
// drops below AA.
func mutedText(p ANSIPalette) RGB {
	for _, t := range []float64{0.40, 0.34, 0.28, 0.22, 0.16, 0.10, 0} {
		c := mix(p.Foreground, p.Background, t)
		if contrast(p.Background, c) >= 4.5 {
			return c
		}
	}
	return ensureContrast(p.Foreground, p.Background, 4.5)
}

// tokens computes the semantic token set for a palette.
func (p ANSIPalette) tokens() map[string]RGB {
	fg, bg := p.Foreground, p.Background
	text := ensureContrast(fg, bg, 4.5) // enforce AA body text even on low-contrast palettes
	// accent uses a non-status hue slot (never 1/2/4/5) — its true hue nudged to a
	// comfortable button contrast (~4.0) so it reads as a solid affordance and can
	// never be confused with a money-status signal. The normal slot (not the bright
	// sibling) preserves the hue on palettes whose slots 8-15 are base grays.
	accent := ensureContrast(p.Palette[p.AccentSlot], bg, 4.0)
	danger := statusColor(p, 1, 3.0)
	tk := map[string]RGB{
		"--bg":              bg,
		"--surface":         mix(bg, fg, 0.04),
		"--surface-2":       mix(bg, fg, 0.09),
		"--text":            text,
		"--text-muted":      mutedText(p),
		"--border":          mix(bg, fg, 0.16),
		"--border-strong":   mix(bg, fg, 0.28),
		"--accent":          accent,
		"--accent-hover":    mix(accent, fg, 0.22),
		"--accent-contrast": pickContrast(accent, RGB{0, 0, 0}, RGB{255, 255, 255}, bg),
		"--focus":           accent,
		"--credited":        statusColor(p, 2, 3.0),
		"--refunded":        statusColor(p, 4, 3.0),
		"--escrow":          statusColor(p, 5, 3.0),
		"--danger":          danger,
		// Readable text/icon color on a filled danger button (solid destructive
		// buttons need a contrast pair just like the accent does).
		"--danger-contrast": pickContrast(danger, RGB{0, 0, 0}, RGB{255, 255, 255}, bg),
		"--sel-bg":          p.SelBG,
		"--sel-fg":          p.SelFG,
	}
	for k, v := range p.Overrides {
		tk[k] = v
	}
	return tk
}

// tokenOrder fixes the emit order so generated CSS is deterministic.
var tokenOrder = []string{
	"--bg", "--surface", "--surface-2", "--text", "--text-muted",
	"--border", "--border-strong", "--accent", "--accent-hover",
	"--accent-contrast", "--focus", "--credited", "--refunded",
	"--escrow", "--danger", "--danger-contrast", "--sel-bg", "--sel-fg",
}

func (p ANSIPalette) block() string {
	tk := p.tokens()
	var b strings.Builder
	for _, k := range tokenOrder {
		fmt.Fprintf(&b, "  %s: %s;\n", k, tk[k].hex())
	}
	return b.String()
}

func themeBySlug(slug string) ANSIPalette {
	for _, t := range themesCache {
		if t.Slug == slug {
			return t
		}
	}
	panic("unknown theme " + slug)
}

// GenerateThemesCSS renders the full theme stylesheet: the default light theme on
// :root, the default dark theme under prefers-color-scheme (only when the user
// has made no explicit choice), then one [data-theme] block per shipped theme so
// an explicit selection always wins.
func GenerateThemesCSS() string {
	var b strings.Builder
	b.WriteString("/* GENERATED by cmd/themegen from server/themes/*.ghostty — do not edit by hand. */\n")
	b.WriteString("/* Regenerate: make themes. Guarded by server/themes_test.go. */\n\n")

	b.WriteString(":root {\n")
	b.WriteString(themeBySlug(defaultLightTheme).block())
	b.WriteString("}\n\n")

	b.WriteString("@media (prefers-color-scheme: dark) {\n  :root:not([data-theme]) {\n")
	// indent the dark default one extra level
	for _, line := range strings.Split(strings.TrimRight(themeBySlug(defaultDarkTheme).block(), "\n"), "\n") {
		b.WriteString("  " + line + "\n")
	}
	b.WriteString("  }\n}\n\n")

	slugs := make([]string, 0, len(themesCache))
	for _, t := range themesCache {
		slugs = append(slugs, t.Slug)
	}
	sort.Strings(slugs)
	for _, slug := range slugs {
		fmt.Fprintf(&b, "[data-theme=\"%s\"] {\n%s}\n\n", slug, themeBySlug(slug).block())
	}
	return b.String()
}
