package server

import (
	"hash/fnv"
	"html/template"
	"strconv"
	"strings"

	"github.com/daios-ai/juice-ui/icons"
)

// cardart.go generates the deterministic fallback image for an action's media
// slot (§5.13). The kernel has no image field yet (ROADMAP.md), so until one
// lands every card draws itself from the action's own identity, and a real image
// simply overrides it.
//
// Two orthogonal signals, per docs/action-thumbnails.md:
//
//   - the ground color says *who provides it* — the hue is hashed from the owner
//     handle alone, so chat/inbox and chat/output are the same color and read
//     as one provider's shelf;
//   - the glyph says *what it does* — icons.Pick matches the action name against
//     an icon corpus, falling back to a per-kind glyph when the name carries too
//     little signal to name one honestly.
//
// The art is inline <svg> with presentation attributes only (no style=, no
// external URL) so it satisfies the CSP, and every color comes from theme tokens
// so it tracks the active theme rather than baking in hues.
//
// The media box is decoration, and that is load-bearing: the ref, price and
// reputation render as chrome beside the frame, never over it. Because nothing
// keys off the glyph, the icon corpus is unpruned — a matched shield or coin
// asserts no more than a provider-supplied photograph of one would.

// artHues are the ground hue angles, in oklch degrees. Eight, not more: with
// twelve, adjacent angles (60° and 75°) were indistinguishable at this chroma,
// so providers that ought to look separate did not. Since collisions are
// inevitable at any ring size once a marketplace has more providers than hues,
// the useful property is that two *different* hues read as different — which
// argues for a small, well-separated set over a large, crowded one.
//
// The ring avoids the money-signal hues so a ground never reads as a status
// colour (§7): danger red (~25°), credited green (~145°), escrow plum (~330°).
var artHues = [...]int{55, 95, 175, 205, 240, 275, 305, 350}

// kindGlyph is the fallback when the name yields no confident match: it says
// only what sort of thing this is. Names are icons in the vendored corpus.
var kindGlyph = map[string]string{
	"http":         "language",
	"wasm":         "memory",
	"native":       "bolt",
	"remote_proxy": "cloud",
	"kernel":       "hub",
	"agent":        "smart_toy",
}

// defaultGlyph is the last resort: a neutral mark that claims nothing.
const defaultGlyph = "widgets"

// CardArt returns the fallback media SVG for an action. The hue is keyed on
// owner (the provider's identity) and the glyph on name (the operation); id is
// accepted so a partial record still draws, and kind selects the fallback glyph.
func CardArt(id, owner, name, kind string) template.HTML {
	hue := artHues[hashOf(hueSeed(owner, id, kind))%uint32(len(artHues))]

	glyph := ""
	if name != "" {
		if m, ok := icons.Pick(owner + "/" + name); ok {
			glyph = m.Name
		}
	}
	if glyph == "" {
		if g, ok := kindGlyph[kind]; ok {
			glyph = g
		} else {
			glyph = defaultGlyph
		}
	}

	// The ground is a diagonal two-stop gradient rather than a flat fill: the far
	// corner is darker, more saturated and nudged along the hue circle, which
	// reads as shading on one colour rather than as two colours. The gradient id
	// is derived from the card's own identity so several cards on a page cannot
	// collide on it.
	gid := "ag" + strconv.FormatUint(uint64(hashOf(id+"|"+owner+"|"+name+"|"+kind)), 36)

	var b strings.Builder
	b.WriteString(`<svg viewBox="0 0 320 180" preserveAspectRatio="xMidYMid slice" role="img" aria-hidden="true">`)
	b.WriteString(`<defs><linearGradient id="` + gid + `" x1="0" y1="0" x2="1" y2="1">` +
		`<stop offset="0" stop-color="oklch(var(--art-ground) ` + itoa(hue) + `)"/>` +
		`<stop offset="1" stop-color="oklch(var(--art-ground-2) ` + itoa(shadeHue(hue)) + `)"/>` +
		`</linearGradient></defs>`)
	b.WriteString(`<rect width="320" height="180" fill="url(#` + gid + `)"/>`)
	if art, ok := icons.Glyph(glyph); ok {
		// The corpus glyph is a 24x24 viewBox; center it at 72px on the 320x180
		// field. It carries no fill of its own, so the enclosing <g> colors it.
		b.WriteString(`<g transform="translate(124 54) scale(3)" fill="oklch(var(--art-glyph) ` +
			itoa(hue) + `)">` + art + `</g>`)
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

// shadeHue nudges the gradient's far stop along the hue circle. The shift is
// small enough to stay one colour but enough to keep the shading from looking
// like a flat darkening.
func shadeHue(h int) int { return (h + 22) % 360 }

// hueSeed picks the most stable identifier available. Owner is preferred so a
// provider's cards share a color; id and kind only stand in for records that
// carry no owner at all.
func hueSeed(owner, id, kind string) string {
	if owner != "" {
		return owner
	}
	if id != "" {
		return id
	}
	return kind
}

func hashOf(s string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return h.Sum32()
}

// itoa is strconv.Itoa, named short for the builder above.
func itoa(n int) string { return strconv.Itoa(n) }
