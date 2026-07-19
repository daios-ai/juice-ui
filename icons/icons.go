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
// Trust glyphs (shield, verified, lock, badge, star, check) and money glyphs
// are pruned from the index, so counterparty-authored text can never obtain
// one. See docs/action-thumbnails.md.
package icons

import (
	_ "embed"
	"encoding/json"
	"math"
	"sort"
	"strings"
	"sync"
	"unicode"
)

//go:embed index.json
var indexJSON []byte

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
}

type doc struct {
	name  string
	terms map[string]bool // name + categories + tags
	inThe map[string]bool // terms occurring in the name alone
	norm  float64         // precomputed length normalization
	pop   int
}

type corpus struct {
	docs []doc
	idf  map[string]float64
}

var (
	once   sync.Once
	loaded *corpus
)

func index() *corpus {
	once.Do(func() { loaded = build(indexJSON) })
	return loaded
}

func build(raw []byte) *corpus {
	var entries []entry
	if err := json.Unmarshal(raw, &entries); err != nil {
		// The index is a vendored build artifact; a parse failure is a
		// programming error, not a runtime condition. Degrade to an empty
		// corpus so callers simply get no match.
		return &corpus{idf: map[string]float64{}}
	}

	c := &corpus{docs: make([]doc, 0, len(entries)), idf: make(map[string]float64)}
	df := make(map[string]int)
	total := 0.0

	for _, e := range entries {
		d := doc{name: e.Name, terms: map[string]bool{}, inThe: map[string]bool{}, pop: e.Pop}
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
// bare name ("ocr-translate") or a full ref ("@lena/ocr-translate"); the owner
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

// queryTerms tokenizes a reference and keeps only terms the corpus knows,
// trying a naive singular form for unknown plurals.
func queryTerms(c *corpus, ref string) []string {
	ref = strings.TrimSpace(ref)
	if strings.HasPrefix(ref, "@") {
		// Split once on the first slash after the handle: the owner is the
		// provider, the remainder is the operation.
		if i := strings.Index(ref, "/"); i >= 0 {
			ref = ref[i+1:]
		} else {
			return nil // a bare handle names no operation
		}
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
