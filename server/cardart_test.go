package server

import (
	"strings"
	"testing"
)

// TestCardArtDeterministic: the art is a pure function of identity, so a card is
// stable across renders and reproducible here.
func TestCardArtDeterministic(t *testing.T) {
	a := CardArt("act-1", "@lena", "ocr-translate", "http")
	if a != CardArt("act-1", "@lena", "ocr-translate", "http") {
		t.Error("CardArt must be deterministic for the same inputs")
	}
	if a == CardArt("act-1", "@lena", "pdf-extract", "http") {
		t.Error("a different operation must draw a different glyph")
	}
	// Falls back to id, then kind, when owner is absent (partial records).
	if CardArt("id-x", "", "", "wasm") == CardArt("id-y", "", "", "wasm") {
		t.Error("hue must vary by id when owner is absent")
	}
}

// The ground says who provides it: one provider, one color, regardless of what
// the individual actions do. This is the signal §6.2 asks the media slot to carry.
func TestCardArtHueIsPerOwner(t *testing.T) {
	hue := func(art string) string {
		_, rest, ok := strings.Cut(art, `stop-color="oklch(var(--art-ground) `)
		if !ok {
			t.Fatalf("no ground stop in %.160s", art)
		}
		h, _, _ := strings.Cut(rest, ")")
		return h
	}
	a := hue(string(CardArt("1", "@chat", "inbox", "http")))
	b := hue(string(CardArt("2", "@chat", "output", "http")))
	if a != b {
		t.Errorf("same owner must share a hue: %s vs %s", a, b)
	}
	// And different owners generally do not. Not a guarantee for any given pair
	// (twelve hues collide), so assert over a spread of handles instead.
	seen := map[string]bool{}
	for _, o := range []string{"@a", "@b", "@c", "@lena", "@kai", "@sys", "@mail", "@vera"} {
		seen[hue(string(CardArt("x", o, "run", "http")))] = true
	}
	if len(seen) < 3 {
		t.Errorf("owners collapsed onto %d hues, expected a spread", len(seen))
	}
}

// The glyph says what it does: a name carrying an obvious operation word must
// reach the matching icon, and one carrying none must still draw something.
func TestCardArtGlyph(t *testing.T) {
	if art := string(CardArt("1", "@chat", "inbox", "http")); !strings.Contains(art, `<g transform=`) {
		t.Error("a matched name must draw a glyph")
	}
	// No name at all: falls back to the kind glyph, never to nothing.
	for _, kind := range []string{"http", "wasm", "native", "remote_proxy", "kernel", "agent", "unknown"} {
		if art := string(CardArt("1", "@x", "", kind)); !strings.Contains(art, `<g transform=`) {
			t.Errorf("kind %q drew no glyph", kind)
		}
	}
}

// TestCardArtCSPSafe: no inline style=, no external URL, no raw hex — every
// color resolves through a theme token.
func TestCardArtCSPSafe(t *testing.T) {
	// Cover the glyph path too: corpus artwork is inlined, so it is under test.
	for _, art := range []string{
		string(CardArt("act-1", "@lena", "ocr-translate", "http")),
		string(CardArt("act-2", "@kai", "", "wasm")),
	} {
		if strings.Contains(art, "style=") {
			t.Error("CardArt must not use inline style= (CSP / grammar)")
		}
		for _, bad := range []string{"http://", "https://", "<script"} {
			if strings.Contains(art, bad) {
				t.Errorf("CardArt must not contain %q", bad)
			}
		}
		// url() is allowed only as a local fragment (the ground gradient); any
		// other form would be a fetch, which the CSP forbids.
		for rest := art; ; {
			i := strings.Index(rest, "url(")
			if i < 0 {
				break
			}
			rest = rest[i+len("url("):]
			if !strings.HasPrefix(rest, "#") {
				t.Errorf("CardArt must only use url(#fragment), got url(%.20s", rest)
			}
		}
		// No raw hex: every color resolves through a theme token. "#" is legal
		// only as the gradient's own fragment reference.
		if strings.Contains(strings.ReplaceAll(art, "url(#", ""), "#") {
			t.Error("CardArt must not contain raw hex colors")
		}
		for _, tok := range []string{"var(--art-ground)", "var(--art-ground-2)", "var(--art-glyph)"} {
			if !strings.Contains(art, tok) {
				t.Errorf("CardArt must color from theme tokens, missing %s", tok)
			}
		}
		if !strings.HasPrefix(art, "<svg") || !strings.HasSuffix(art, "</svg>") {
			t.Error("CardArt must be a single <svg> element")
		}
		if n := strings.Count(art, "<svg"); n != 1 {
			t.Errorf("nested svg: found %d roots", n)
		}
	}
}

// Gradient ids share one document with every other card on the page, so two
// different cards must never claim the same id.
func TestCardArtGradientIDsUnique(t *testing.T) {
	gid := func(art string) string {
		_, rest, ok := strings.Cut(art, `<linearGradient id="`)
		if !ok {
			t.Fatalf("no gradient in %.160s", art)
		}
		g, _, _ := strings.Cut(rest, `"`)
		return g
	}
	seen := map[string]string{}
	for _, c := range []struct{ id, owner, name, kind string }{
		{"1", "@chat", "inbox", "http"}, {"2", "@chat", "output", "http"},
		{"3", "@mail", "list", "http"}, {"4", "@mail", "send", "http"},
		{"5", "@sys", "make", "native"}, {"6", "@sys", "lookup", "native"},
		{"7", "@kai", "", "wasm"}, {"8", "@lena", "ocr-translate", "http"},
	} {
		ref := c.owner + "/" + c.name
		g := gid(string(CardArt(c.id, c.owner, c.name, c.kind)))
		if prev, dup := seen[g]; dup {
			t.Errorf("gradient id %q shared by %s and %s", g, prev, ref)
		}
		seen[g] = ref
	}
	// And it stays stable for the same card, or the art would not be deterministic.
	if gid(string(CardArt("1", "@chat", "inbox", "http"))) !=
		gid(string(CardArt("1", "@chat", "inbox", "http"))) {
		t.Error("gradient id must be deterministic")
	}
}

// Every hue must be a legal oklch angle, and the set must avoid the money-signal
// hues so a ground never reads as a status colour (§7).
func TestArtHuesAvoidStatusBands(t *testing.T) {
	for _, h := range artHues {
		if h < 0 || h > 360 {
			t.Errorf("hue %d out of range", h)
		}
		for _, band := range []struct {
			name   string
			lo, hi int
		}{
			{"credited green", 130, 160},
			{"escrow plum", 315, 345},
			{"danger red", 10, 40},
		} {
			if h >= band.lo && h <= band.hi {
				t.Errorf("hue %d falls in the %s band", h, band.name)
			}
		}
	}
}
