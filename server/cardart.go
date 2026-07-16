package server

import (
	"hash/fnv"
	"html/template"
	"strconv"
	"strings"
)

// cardart.go generates the deterministic fallback image for an action's media
// slot (§5.13). The kernel has no image field yet (ROADMAP.md), so until one
// lands every card shows a generated pattern seeded from the action's stable
// identity — the same action always draws the same art, and a real image simply
// overrides it. The art is inline <svg> using presentation attributes only (no
// style=, no external URL) so it satisfies the CSP, and it is colored from theme
// tokens (var(--surface-2), var(--accent)) so it tracks the active theme rather
// than baking in hues. Counterparty content stays outside it (the ref, price, and
// reputation render as chrome beside the frame, never over it).

// rng is a tiny deterministic PRNG (xorshift32) seeded from the action identity,
// so the art is a pure function of the seed — no Math.random, reproducible in the
// drift test.
type rng struct{ s uint32 }

func (r *rng) next() uint32 {
	r.s ^= r.s << 13
	r.s ^= r.s >> 17
	r.s ^= r.s << 5
	return r.s
}

// intn returns a value in [0, n).
func (r *rng) intn(n int) int {
	if n <= 0 {
		return 0
	}
	return int(r.next() % uint32(n))
}

// CardArt returns the fallback media SVG for an action, keyed on its id (falling
// back to owner/name so a partial record — e.g. a lookup result — still draws).
// The seed is hashed so visually adjacent ids don't produce adjacent art.
func CardArt(id, owner, kind string) template.HTML {
	seed := id
	if seed == "" {
		seed = owner + "/" + kind
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(seed))
	r := &rng{s: h.Sum32() | 1} // non-zero state for xorshift

	// A 16:9 field: a token-colored ground, a scatter of accent shapes at varying
	// low opacity (the texture), and one or two stronger accent marks (the focus).
	var b strings.Builder
	b.WriteString(`<svg viewBox="0 0 320 180" preserveAspectRatio="xMidYMid slice" role="img" aria-hidden="true">`)
	b.WriteString(`<rect width="320" height="180" fill="var(--surface-2)"/>`)

	// A soft diagonal band of the accent, offset by the seed.
	bandY := 40 + r.intn(80)
	b.WriteString(`<rect x="-20" y="` + itoa(bandY) + `" width="360" height="` +
		itoa(18+r.intn(26)) + `" fill="var(--accent)" opacity="0.08" transform="rotate(` +
		itoa(-12+r.intn(24)) + ` 160 90)"/>`)

	// Scatter: 7 shapes, circles or squares, gridded so they never fully overlap.
	shapes := 7
	for i := 0; i < shapes; i++ {
		cx := 30 + (i*40+r.intn(24))%280
		cy := 24 + r.intn(132)
		size := 10 + r.intn(26)
		op := "0." + itoa(10+r.intn(30)) // 0.10–0.39
		if r.intn(2) == 0 {
			b.WriteString(`<circle cx="` + itoa(cx) + `" cy="` + itoa(cy) + `" r="` +
				itoa(size) + `" fill="var(--accent)" opacity="` + op + `"/>`)
		} else {
			b.WriteString(`<rect x="` + itoa(cx) + `" y="` + itoa(cy) + `" width="` +
				itoa(size) + `" height="` + itoa(size) + `" rx="3" fill="var(--accent)" opacity="` + op + `"/>`)
		}
	}

	// One focal mark: a bold accent ring, clearly the strongest element.
	fx := 60 + r.intn(200)
	fy := 40 + r.intn(100)
	fr := 18 + r.intn(20)
	b.WriteString(`<circle cx="` + itoa(fx) + `" cy="` + itoa(fy) + `" r="` + itoa(fr) +
		`" fill="none" stroke="var(--accent)" stroke-width="4" opacity="0.55"/>`)

	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

// itoa is strconv.Itoa, named short for the builder above.
func itoa(n int) string { return strconv.Itoa(n) }
