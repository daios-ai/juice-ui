package server

import (
	"strings"
	"testing"
)

// TestCardArtDeterministic: the same seed always draws the same art (pure
// function of identity), and different seeds differ — so a card's image is stable
// across renders and reproducible in this test.
func TestCardArtDeterministic(t *testing.T) {
	a := CardArt("act-1", "@lena", "http")
	b := CardArt("act-1", "@lena", "http")
	if a != b {
		t.Error("CardArt must be deterministic for the same seed")
	}
	c := CardArt("act-2", "@lena", "http")
	if a == c {
		t.Error("CardArt must differ for different ids")
	}
	// Falls back to owner/kind when id is empty (partial lookup record).
	if CardArt("", "@x", "wasm") == CardArt("", "@y", "wasm") {
		t.Error("CardArt fallback must vary by owner")
	}
}

// TestCardArtCSPSafe: the art must carry no inline style= and no external URL
// (the CSP forbids both), and must be colored from theme tokens, not baked hues.
func TestCardArtCSPSafe(t *testing.T) {
	art := string(CardArt("act-1", "@lena", "http"))
	if strings.Contains(art, "style=") {
		t.Error("CardArt must not use inline style= (CSP / grammar)")
	}
	for _, bad := range []string{"http://", "https://", "url(", "<script"} {
		if strings.Contains(art, bad) {
			t.Errorf("CardArt must not contain %q (CSP: no external refs / script)", bad)
		}
	}
	if !strings.Contains(art, "var(--accent)") || !strings.Contains(art, "var(--surface-2)") {
		t.Error("CardArt must color from theme tokens (var(--accent)/var(--surface-2))")
	}
	if !strings.HasPrefix(art, "<svg") || !strings.HasSuffix(art, "</svg>") {
		t.Error("CardArt must be a single <svg> element")
	}
	// No raw hex colors (every color is a token).
	if strings.Contains(art, "#") {
		t.Error("CardArt must not contain raw hex colors")
	}
}
