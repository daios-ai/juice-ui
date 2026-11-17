package server

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

func body(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}

// A text message reads as Markdown: emphasis, lists, code, quotes, tables,
// the line breaks typed, literal addresses as links; a named link shows the
// address it goes to; an image is a button naming its host, nothing loaded.
func TestProseRendersMarkdown(t *testing.T) {
	for in, want := range map[string][]string{
		"**bold** _it_ ~~gone~~":                 {"<strong>bold</strong>", "<em>it</em>", "<del>gone</del>"},
		"one\ntwo":                               {"one<br>", "two"},
		"- a\n- b":                               {"<ul>", "<li>a</li>"},
		"1. a\n2. b":                             {"<ol>", "<li>b</li>"},
		"`x := 1`":                               {"<code>x := 1</code>"},
		"```\nfunc f() {}\n```":                  {"<pre><code>func f() {}\n</code></pre>"},
		"> said":                                 {"<blockquote>", "said"},
		"| a | b |\n|---|---|\n| 1 | 2 |":        {"<table>", "<th>a</th>", "<td>2</td>"},
		"see https://example.com/x now":          {`<a href="https://example.com/x" target="_blank" rel="noopener noreferrer">https://example.com/x</a>`},
		"[bank](https://evil.example/login)":     {`bank (<a href="https://evil.example/login" target="_blank" rel="noopener noreferrer">https://evil.example/login</a>)`},
		"[https://a.example](https://a.example)": {`<a href="https://a.example" target="_blank" rel="noopener noreferrer">https://a.example</a>`},
		"![a cat](https://i.example/cat.png)":    {`<a class="btn image-link" href="https://i.example/cat.png" target="_blank" rel="noopener noreferrer" data-image data-alt="a cat">`, "i.example</a>"},
		"# Title":                                {"<h1>Title</h1>"},
		`![cat](https://example.com/a\(b\).png)`: {`href="https://example.com/a(b).png"`},
		"[q](https://example.com/?a=1&amp;b=2)":  {`href="https://example.com/?a=1&amp;b=2"`, `>https://example.com/?a=1&amp;b=2</a>`},
	} {
		got := string(prose(body(in)))
		for _, w := range want {
			if !strings.Contains(got, w) {
				t.Errorf("%q renders %q, lacking %q", in, got, w)
			}
		}
	}
	if got := string(prose(body("[https://a.example](https://a.example)"))); strings.Count(got, "https://a.example") != 2 {
		t.Errorf("a link whose text is its address says it once: %q", got)
	}
}

// Whatever a member types, the output holds only the tags the renderer
// writes, no handler, style or alignment attribute, no image element, and no
// link but to a web address: raw HTML is shown as typed, a script address is
// text, an image not on https is its alt text, an aligned table aligns
// nothing (the content security policy refuses inline style).
func TestProseIsSafeWhateverIsTyped(t *testing.T) {
	allowed := map[string]bool{"p": true, "br": true, "strong": true, "em": true, "del": true, "code": true, "pre": true,
		"ul": true, "ol": true, "li": true, "blockquote": true, "hr": true, "a": true, "svg": true, "use": true,
		"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
		"table": true, "thead": true, "tbody": true, "tr": true, "th": true, "td": true}
	tag := regexp.MustCompile(`<(/?)([a-zA-Z0-9]+)([^>]*)>`)
	href := regexp.MustCompile(`href="([^"]*)"`)
	for _, in := range []string{
		`<script>alert(1)</script>`,
		"<div onclick=\"x()\">hi</div>\n\nafter",
		`inline <img src=x onerror=alert(1)> tag`,
		`[click](javascript:alert(1))`,
		`[data](data:text/html,<script>alert(1)</script>)`,
		`![x](javascript:alert(1))`,
		`![x](http://plain.example/a.png)`,
		`![x](data:image/png;base64,AAAA)`,
		`![" onload="alert(1)](https://i.example/a.png)`,
		`![x](https://i.example/a.png" onerror="alert(1))`,
		`[t](https://a.example/" onmouseover="x)`,
		"mail me@example.com or www.example.com",
		"| left | mid | right |\n|:---|:---:|---:|\n| 1 | 2 | 3 |",
		"<style>body{display:none}</style>",
		`<a href="javascript:x">raw</a>`,
	} {
		got := string(prose(body(in)))
		for _, m := range tag.FindAllStringSubmatch(got, -1) {
			if !allowed[strings.ToLower(m[2])] {
				t.Errorf("%q writes <%s>: %s", in, m[2], got)
			}
			names := regexp.MustCompile(`"[^"]*"`).ReplaceAllString(m[3], `""`) // attribute names, values set aside
			if regexp.MustCompile(`(?i)\s(on\w+|style|align|src)\s*=`).MatchString(names) {
				t.Errorf("%q writes attribute %q", in, m[3])
			}
		}
		for _, m := range href.FindAllStringSubmatch(got, -1) {
			if m[1] != "/static/icons.svg#image" && !strings.HasPrefix(m[1], "https://") && !strings.HasPrefix(m[1], "http://") {
				t.Errorf("%q links to %q", in, m[1])
			}
		}
		if strings.Contains(got, "<img") {
			t.Errorf("%q writes an image element: %s", in, got)
		}
	}
	if got := string(prose(body(`<b>raw</b>`))); !strings.Contains(got, "&lt;b&gt;raw&lt;/b&gt;") {
		t.Errorf("raw HTML is shown as typed: %q", got)
	}
}

// Only a text message is prose: a structured body, a number, an empty text
// and a text holding a JSON document keep the value view.
func TestProseIsTextAlone(t *testing.T) {
	for _, raw := range []string{`{"a":1}`, `[1,2]`, `42`, `""`, `"   "`, `"{\"a\":1}"`, `null`} {
		if got := prose(json.RawMessage(raw)); got != "" {
			t.Errorf("%s is not prose: %q", raw, got)
		}
	}
	if prose(body("hello")) == "" {
		t.Error("a text is prose")
	}
}
