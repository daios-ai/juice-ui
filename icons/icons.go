// Package icons deterministically picks an icon whose meaning relates to an
// action's name.
//
// It is a leaf package: it imports nothing from the rest of juice-ui, holds no
// state beyond an immutable index compiled into the binary, and is a pure
// function of its input. Deleting this directory and its single call site
// removes it entirely.
//
// The method is text retrieval, not classification. Each icon is a short
// document (its name, categories and synonym tags, as published with the
// Material Symbols metadata); an action name is a query; documents are ranked
// by summed inverse document frequency of the matched terms. Rare terms such as
// "translate" therefore dominate common ones such as "arrow" without any
// hand-tuned stoplist.
//
// The corpus is the whole published icon set, deliberately unpruned. An earlier
// cut withheld the trust glyphs (shield, verified, lock, badge, star, check) and
// the money glyphs on the reasoning that counterparty-authored text could reach
// one. That reasoning does not survive contact with where the glyph actually
// renders: inside the media box, which is decoration, while every kernel-vouched
// fact — ref, price, reputation — renders as chrome outside it. A drawn shield
// there asserts no more than a provider-supplied photograph of a shield would,
// and that is an accepted, socially policed risk (docs/action-thumbnails.md §5).
// It is not a trust affordance in the sense requirements.md §8.1 governs, since
// nothing keys off it.
//
// The single exclusion is technical: one icon carries an inline style=, which
// the CSP forbids.
package icons

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"io"
	"math"
	"sort"
	"strings"
	"sync"
	"unicode"
)

// The index carries each icon's terms and its artwork (the inner markup of a
// 24x24 viewBox, carrying no fill of its own so a caller colors it). Stored
// gzipped: 1.4MB of JSON compresses to under a third of that, and the cost is
// one decompression at first use.
//
//go:embed index.json.gz
var indexGz []byte

// Match is one ranked icon.
type Match struct {
	Name  string
	Score float64
}

// MinScore is the confidence floor. Below it a query carries too little lexical
// signal to name an icon honestly, and Pick reports no match rather than
// asserting a meaning the action does not have.
const MinScore = 1.6

// Scoring constants.
const (
	lengthB   = 0.75 // document-length normalization strength, as in BM25
	nameBoost = 2.5  // weight of the name-coverage bonus, below
)

// Name coverage is what separates the canonical icon from its compounds. A
// query of "send" matches the names of both "send" and "send_and_archive", but
// only the former is *wholly* explained by the query; the latter promises two
// further concepts the action never mentioned. Coverage is the fraction of the
// icon's own name terms the query accounts for, and it is needed because
// document-length normalization alone points the wrong way here: canonical
// icons carry the richest synonym lists and would otherwise be penalized for it.

type entry struct {
	Name string   `json:"n"`
	Cats []string `json:"c"`
	Tags []string `json:"t"`
	Pop  int      `json:"p"`
	Art  string   `json:"d"`
}

type doc struct {
	name  string
	art   string
	terms map[string]bool // name + categories + tags
	inThe map[string]bool // terms occurring in the name alone
	norm  float64         // precomputed length normalization
	pop   int
}

type corpus struct {
	docs   []doc
	idf    map[string]float64
	byName map[string]int
}

var (
	once   sync.Once
	loaded *corpus
)

func index() *corpus {
	once.Do(func() { loaded = build(indexGz) })
	return loaded
}

// empty is the degraded corpus. The index is a vendored build artifact, so a
// read or parse failure is a programming error rather than a runtime condition;
// callers simply get no match instead of a panic in a request path.
func empty() *corpus {
	return &corpus{idf: map[string]float64{}, byName: map[string]int{}}
}

func build(gz []byte) *corpus {
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return empty()
	}
	defer zr.Close()
	raw, err := io.ReadAll(zr)
	if err != nil {
		return empty()
	}

	var entries []entry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return empty()
	}

	c := &corpus{
		docs:   make([]doc, 0, len(entries)),
		idf:    make(map[string]float64),
		byName: make(map[string]int, len(entries)),
	}
	df := make(map[string]int)
	total := 0.0

	for _, e := range entries {
		d := doc{name: e.Name, art: e.Art, terms: map[string]bool{}, inThe: map[string]bool{}, pop: e.Pop}
		for _, t := range tokenize(e.Name) {
			d.terms[t] = true
			d.inThe[t] = true
		}
		for _, s := range e.Cats {
			for _, t := range tokenize(s) {
				d.terms[t] = true
			}
		}
		for _, s := range e.Tags {
			for _, t := range tokenize(s) {
				d.terms[t] = true
			}
		}
		if len(d.terms) == 0 {
			continue
		}
		for t := range d.terms {
			df[t]++
		}
		total += float64(len(d.terms))
		c.byName[d.name] = len(c.docs)
		c.docs = append(c.docs, d)
	}

	n := float64(len(c.docs))
	if n == 0 {
		return c
	}
	avg := total / n
	for i := range c.docs {
		l := float64(len(c.docs[i].terms))
		c.docs[i].norm = 1 / (1 - lengthB + lengthB*l/avg)
	}
	for t, k := range df {
		c.idf[t] = math.Log(1 + (n-float64(k)+0.5)/(float64(k)+0.5))
	}
	return c
}

// Pick returns the best-matching icon for an action reference, which may be a
// bare name ("ocr-translate") or a full ref ("lena/ocr-translate"); the owner
// segment names the provider, not the operation, and is discarded. The second
// result is false when no candidate clears MinScore.
func Pick(ref string) (Match, bool) {
	top := Top(ref, 1)
	if len(top) == 0 || top[0].Score < MinScore {
		return Match{}, false
	}
	return top[0], true
}

// Top returns up to n ranked matches, unfiltered by MinScore. It exists so the
// quality of the ranking can be inspected, not just its winner.
func Top(ref string, n int) []Match {
	c := index()
	terms := queryTerms(c, ref)
	if len(terms) == 0 || n <= 0 {
		return nil
	}

	scored := make([]Match, 0, 64)
	for i := range c.docs {
		d := &c.docs[i]
		base, covered := 0.0, 0
		for _, t := range terms {
			if !d.terms[t] {
				continue
			}
			base += c.idf[t] * d.norm
			if d.inThe[t] {
				covered++
			}
		}
		if base <= 0 {
			continue
		}
		coverage := 0.0
		if len(d.inThe) > 0 { // some names ("10k") tokenize to nothing
			coverage = float64(covered) / float64(len(d.inThe))
		}
		scored = append(scored, Match{
			Name:  d.name,
			Score: base * (1 + nameBoost*coverage),
		})
	}

	pop := make(map[string]int, len(c.docs))
	for i := range c.docs {
		pop[c.docs[i].name] = c.docs[i].pop
	}
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].Score != scored[j].Score {
			return scored[i].Score > scored[j].Score
		}
		if pop[scored[i].Name] != pop[scored[j].Name] {
			return pop[scored[i].Name] > pop[scored[j].Name]
		}
		return scored[i].Name < scored[j].Name // total order, so results are stable
	})
	if len(scored) > n {
		scored = scored[:n]
	}
	return scored
}

// Glyph returns an icon's artwork: the inner markup of a 24x24 viewBox, using
// presentation attributes only and carrying no fill of its own, so the caller
// colors it by setting fill on an enclosing element. Trust and money glyphs are
// absent from the index, so no name can return one.
func Glyph(name string) (string, bool) {
	c := index()
	i, ok := c.byName[name]
	if !ok || c.docs[i].art == "" {
		return "", false
	}
	return c.docs[i].art, true
}

// queryTerms tokenizes a reference and keeps only terms the corpus knows,
// trying a naive singular form for unknown plurals.
func queryTerms(c *corpus, ref string) []string {
	ref = strings.TrimSpace(ref)
	// A full ref is owner/name: strip the owner segment (through the first
	// slash) so provider handles never leak into the icon query — refs are bare,
	// so any ref that contains a slash is owner-qualified. Callers passing a bare
	// action NAME containing slashes do not exist (names reach here as full
	// refs); a bare handle names no operation.
	if i := strings.Index(ref, "/"); i >= 0 {
		ref = ref[i+1:]
	}

	seen := map[string]bool{}
	out := make([]string, 0, 8)
	for _, t := range tokenize(ref) {
		if _, ok := c.idf[t]; !ok {
			if s, cut := singular(t); cut {
				if _, ok := c.idf[s]; ok {
					t = s
				}
			}
		}
		if _, ok := c.idf[t]; !ok || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

func singular(t string) (string, bool) {
	if len(t) > 3 && strings.HasSuffix(t, "s") && !strings.HasSuffix(t, "ss") {
		return strings.TrimSuffix(t, "s"), true
	}
	return t, false
}

// tokenize lowercases and splits on separators, case transitions and
// letter/digit boundaries, dropping all-digit and single-character tokens.
func tokenize(s string) []string {
	var out []string
	var cur []rune

	flush := func() {
		if len(cur) < 2 {
			cur = cur[:0]
			return
		}
		t := strings.ToLower(string(cur))
		cur = cur[:0]
		if allDigits(t) {
			return
		}
		out = append(out, t)
	}

	prev := rune(0)
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			// camelCase and letter/digit transitions start a new token.
			if prev != 0 {
				lowerToUpper := unicode.IsLower(prev) && unicode.IsUpper(r)
				digitEdge := unicode.IsDigit(prev) != unicode.IsDigit(r)
				if lowerToUpper || digitEdge {
					flush()
				}
			}
			cur = append(cur, r)
		default:
			flush()
		}
		prev = r
	}
	flush()
	return out
}

func allDigits(s string) bool {
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return len(s) > 0
}
