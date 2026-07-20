package icons

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// sample refs, grouped so the harness output reads as an argument: real refs
// from this repo, plausible marketplace refs, and refs carrying no lexical
// signal at all (which must fall below MinScore rather than invent a meaning).
var (
	repoRefs = []string{
		"@chat/inbox", "@chat/output", "@chat/register", "@chat/validate-user",
		"@mail/list", "@mail/send", "@sys/llm/chat", "@sys/make", "@sys/lookup",
		"@sys/message", "@sys/random",
	}
	marketRefs = []string{
		"@lena/ocr-translate", "@kai/pdf-extract", "@tomo/tts-narrate",
		"@vera/code-review", "@nordic/ana/weather", "@miro/logo-forge",
		"@ana/image-upscale", "@raj/invoice-parse", "@sol/calendar-sync",
		"@ivy/video-transcribe", "@finn/map-geocode", "@zoe/spreadsheet-clean",
		"@omar/podcast-summarize", "@lin/backup-archive", "@dev/deploy-rollback",
	}
	// No term the corpus knows: the picker must decline rather than invent.
	meaninglessRefs = []string{
		"@kai/v2", "@x/proc", "@z/xyzzy", "@a/qq", "@w/blorp", "@q/plugh",
	}
	// Terms the corpus knows, but not in the sense the author meant. These are
	// the honest weakness of a lexical method: "run3" reads as running, "foo-bar"
	// finds battery bars. They are shown in the harness and deliberately not
	// asserted, because no purely lexical scheme can distinguish them.
	falseFriendRefs = []string{
		"@y/run3", "@b/foo-bar", "@c/spam-filter", "@d/tar-gz", "@e/git-blame",
	}
)

// TestPickTable is the harness: it prints what the picker chooses, with
// runners-up, so match quality can be judged by reading it.
// Run: go test ./icons/ -run TestPickTable -v
func TestPickTable(t *testing.T) {
	show := func(title string, refs []string) {
		t.Logf("\n%s\n%s", title, strings.Repeat("-", 78))
		for _, ref := range refs {
			top := Top(ref, 3)
			if len(top) == 0 {
				t.Logf("%-26s  %s", ref, "(no candidate)")
				continue
			}
			mark := "  "
			if top[0].Score < MinScore {
				mark = "x " // below threshold: Pick would decline
			}
			alts := make([]string, 0, 2)
			for _, m := range top[1:] {
				alts = append(alts, fmt.Sprintf("%s %.1f", m.Name, m.Score))
			}
			t.Logf("%s%-26s  %-22s %5.1f    %s",
				mark, ref, top[0].Name, top[0].Score, strings.Join(alts, " · "))
		}
	}

	c := index()
	t.Logf("corpus: %d icons, %d distinct terms, MinScore %.1f",
		len(c.docs), len(c.idf), MinScore)
	show("repo actions", repoRefs)
	show("marketplace actions", marketRefs)
	show("no lexical signal — must decline", meaninglessRefs)
	show("false friends — known words, wrong sense (the method's limit)", falseFriendRefs)
}

func TestIndexLoads(t *testing.T) {
	c := index()
	if len(c.docs) < 1000 {
		t.Fatalf("corpus too small: %d docs", len(c.docs))
	}
	if len(c.idf) < 1000 {
		t.Fatalf("vocabulary too small: %d terms", len(c.idf))
	}
	for i := range c.docs {
		if c.docs[i].name == "" {
			t.Fatal("doc with empty name")
		}
		if c.docs[i].norm <= 0 {
			t.Fatalf("doc %q has non-positive norm %v", c.docs[i].name, c.docs[i].norm)
		}
	}
}

func TestDeterministic(t *testing.T) {
	for _, ref := range append(append([]string{}, repoRefs...), marketRefs...) {
		first := Top(ref, 3)
		for i := 0; i < 5; i++ {
			if got := Top(ref, 3); !reflect.DeepEqual(got, first) {
				t.Fatalf("%s: run %d differs\n first: %v\n got:   %v", ref, i, first, got)
			}
		}
	}
}

// The whole point of the exercise: a ref whose name carries an obvious
// operation word must resolve to the icon of that name.
func TestObviousMatches(t *testing.T) {
	cases := map[string]string{
		"@chat/inbox":         "inbox",
		"@lena/ocr-translate": "translate",
		"@sys/llm/chat":       "chat",
		"@mail/send":          "send",
		"@sol/calendar-sync":  "sync",
		"@x/print":            "print",
		"@x/download":         "download",
		"@x/search":           "search",
	}
	for ref, want := range cases {
		got, ok := Pick(ref)
		if !ok {
			t.Errorf("%s: declined, want %q", ref, want)
			continue
		}
		if got.Name != want {
			t.Errorf("%s: got %q (%.1f), want %q", ref, got.Name, got.Score, want)
		}
	}
}

func TestOwnerIsIgnored(t *testing.T) {
	// The owner names the provider, not the operation. Two providers offering
	// the same operation must pick the same icon.
	a, okA := Pick("@lena/translate")
	b, okB := Pick("@miro/translate")
	if !okA || !okB || a.Name != b.Name {
		t.Fatalf("owner leaked into the match: %v/%v vs %v/%v", a, okA, b, okB)
	}
	// A bare handle names no operation.
	if _, ok := Pick("@lena"); ok {
		t.Error("bare handle should not match")
	}
}

// The corpus is deliberately whole: trust and money glyphs are reachable,
// because the media box is decoration and every kernel-vouched fact renders as
// chrome outside it. This test pins that decision so nobody silently re-prunes,
// and pins the one exclusion that is technical rather than editorial.
func TestCorpusIsUnpruned(t *testing.T) {
	present := map[string]bool{}
	for i := range index().docs {
		present[index().docs[i].name] = true
	}
	for _, name := range []string{
		"verified", "shield", "lock", "star", "check", "done", "security",
		"payments", "paid", "credit_card", "account_balance_wallet", "diamond",
	} {
		if !present[name] {
			t.Errorf("%q missing: the corpus is meant to be unpruned", name)
		}
	}
	// Excluded for CSP, not for meaning: its artwork carries an inline style=.
	if present["screen_search_desktop"] {
		t.Error("screen_search_desktop carries inline style= and must stay out")
	}
}

// Apache-2.0 §6 grants no trademark rights, so glyphs depicting a mark cannot
// ride the artwork's licence onto a commercial listing. Unlike the trust
// glyphs, excluding these costs no accuracy: nothing else needs them. See
// icons/NOTICE.
func TestTrademarkedGlyphsExcluded(t *testing.T) {
	c := index()
	for _, name := range []string{"android", "flutter_dash"} {
		if _, ok := c.byName[name]; ok {
			t.Errorf("trademarked glyph %q must not be in the corpus", name)
		}
		if _, ok := Glyph(name); ok {
			t.Errorf("trademarked glyph %q is still reachable", name)
		}
	}
	// And no query may reach one.
	for _, ref := range []string{"@x/android-build", "@y/flutter-app", "@z/logo-forge"} {
		if m, ok := Pick(ref); ok && (m.Name == "android" || m.Name == "flutter_dash") {
			t.Errorf("%s reached trademarked glyph %q", ref, m.Name)
		}
	}
}

// Whatever a query reaches, the artwork itself must stay CSP-clean, since it is
// inlined verbatim into the page.
func TestGlyphMarkupIsInert(t *testing.T) {
	c := index()
	for i := range c.docs {
		art := c.docs[i].art
		for _, bad := range []string{"style=", "url(", "<script", "http://", "https://", "#", "id="} {
			if strings.Contains(art, bad) {
				t.Fatalf("glyph %q contains %q", c.docs[i].name, bad)
			}
		}
	}
}

func TestTokenize(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"ocr-translate", []string{"ocr", "translate"}},
		{"pdf_extract", []string{"pdf", "extract"}},
		{"llm/chat", []string{"llm", "chat"}},
		{"videoTranscribe", []string{"video", "transcribe"}},
		{"picture_as_pdf", []string{"picture", "as", "pdf"}},
		{"run3", []string{"run"}}, // trailing digits split off, "3" dropped
		{"v2", nil},               // single letter then digit: nothing survives
		{"10k", nil},              // all-digit and single-char fragments dropped
		{"", nil},
	}
	for _, c := range cases {
		got := tokenize(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("tokenize(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestSingular(t *testing.T) {
	if s, cut := singular("actions"); !cut || s != "action" {
		t.Errorf("singular(actions) = %q,%v", s, cut)
	}
	if _, cut := singular("class"); cut {
		t.Error("singular should not strip from -ss")
	}
	if _, cut := singular("gas"); cut {
		t.Error("singular should not strip from short words")
	}
}

func TestTopBounds(t *testing.T) {
	if got := Top("@x/translate", 0); got != nil {
		t.Errorf("n=0 should yield nil, got %v", got)
	}
	if got := Top("@x/xyzzy-qqq", 5); len(got) != 0 {
		t.Errorf("unknown terms should yield no candidates, got %v", got)
	}
	if got := Top("@x/translate", 3); len(got) > 3 {
		t.Errorf("Top(3) returned %d", len(got))
	}
}

func TestPickDeclinesBelowThreshold(t *testing.T) {
	for _, ref := range meaninglessRefs {
		if m, ok := Pick(ref); ok {
			t.Errorf("%s should be declined, got %q (%.1f)", ref, m.Name, m.Score)
		}
	}
}
