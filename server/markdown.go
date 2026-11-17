package server

import (
	"bytes"
	"encoding/json"
	"html"
	"html/template"
	"net/url"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer"
	gmhtml "github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/util"
)

// markdown renders a chat message's text (requirements §6.7, the one
// exception of §8.1): CommonMark with tables, strikethrough and literal
// addresses as links, the line breaks typed kept (as Slack and Discord do).
// Raw HTML is shown as typed, a link shows the address it goes to, and an
// image is a button naming its host, loaded only when clicked (proseRenderer).
// Table cells carry no alignment, which the extension would write as an
// inline style the content security policy refuses.
var markdown = goldmark.New(
	goldmark.WithExtensions(
		extension.NewTable(extension.WithTableCellAlignMethod(extension.TableCellAlignNone)),
		extension.Strikethrough,
		extension.Linkify,
	),
	goldmark.WithRendererOptions(
		gmhtml.WithHardWraps(),
		renderer.WithNodeRenderers(util.Prioritized(proseRenderer{}, 100)),
	),
)

// prose is a message body rendered, when it is text: a JSON string that is
// neither blank nor a JSON document, which the value view shows as that
// document (textDocument). Any other body is "", and keeps the value view.
func prose(raw json.RawMessage) template.HTML {
	var s string
	if json.Unmarshal(raw, &s) != nil || strings.TrimSpace(s) == "" {
		return ""
	}
	if _, ok := textDocument(s); ok {
		return ""
	}
	var buf bytes.Buffer
	if markdown.Convert([]byte(s), &buf) != nil {
		return ""
	}
	return template.HTML(buf.String())
}

// proseRenderer draws the nodes another member's text could misuse.
type proseRenderer struct{}

func (proseRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(ast.KindHTMLBlock, renderTypedBlock)
	reg.Register(ast.KindRawHTML, renderTypedInline)
	reg.Register(ast.KindLink, renderLink)
	reg.Register(ast.KindAutoLink, renderAutoLink)
	reg.Register(ast.KindImage, renderImage)
}

// renderTypedBlock and renderTypedInline show raw HTML as the text typed.
func renderTypedBlock(w util.BufWriter, src []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	var text strings.Builder
	lines := n.Lines()
	for i := 0; i < lines.Len(); i++ {
		seg := lines.At(i)
		text.Write(seg.Value(src))
	}
	if b, ok := n.(*ast.HTMLBlock); ok && b.HasClosure() {
		text.Write(b.ClosureLine.Value(src))
	}
	_, _ = w.WriteString("<p>" + strings.ReplaceAll(html.EscapeString(strings.TrimRight(text.String(), "\n")), "\n", "<br>\n") + "</p>\n")
	return ast.WalkContinue, nil
}

func renderTypedInline(w util.BufWriter, src []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		segs := n.(*ast.RawHTML).Segments
		for i := 0; i < segs.Len(); i++ {
			seg := segs.At(i)
			_, _ = w.WriteString(html.EscapeString(string(seg.Value(src))))
		}
	}
	return ast.WalkSkipChildren, nil
}

// renderLink writes a link's text, then the address it goes to as the link,
// so a name never disguises a destination; a link whose text is its address
// is the address once, and one not to a web address is its text alone.
func renderLink(w util.BufWriter, src []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	l := n.(*ast.Link)
	u, ok := webAddress(l.Destination)
	dest := ""
	if ok {
		dest = u.String()
	}
	switch {
	case !ok:
	case string(l.Text(src)) == dest:
		if entering {
			writeLink(w, dest, dest)
		}
		return ast.WalkSkipChildren, nil
	case !entering:
		_, _ = w.WriteString(" (")
		writeLink(w, dest, dest)
		_, _ = w.WriteString(")")
	}
	return ast.WalkContinue, nil
}

// renderAutoLink links an address written out, a web one only; an e-mail
// address stays text.
func renderAutoLink(w util.BufWriter, src []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	l := n.(*ast.AutoLink)
	label := string(l.Label(src))
	addr := string(l.URL(src))
	if strings.HasPrefix(strings.ToLower(addr), "www.") {
		addr = "https://" + addr
	}
	if u, ok := webAddress([]byte(addr)); ok && l.AutoLinkType == ast.AutoLinkURL {
		writeLink(w, u.String(), label)
	} else {
		_, _ = w.WriteString(html.EscapeString(label))
	}
	return ast.WalkContinue, nil
}

// renderImage writes an image on https as a button naming its host, which
// the page replaces with the image when clicked (app.js); without script it
// opens the image. Nothing is loaded until then, so no host learns who read
// the message. Any other image is its alt text.
func renderImage(w util.BufWriter, src []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkSkipChildren, nil
	}
	img := n.(*ast.Image)
	alt := string(img.Text(src))
	u, ok := webAddress(img.Destination)
	if !ok || u.Scheme != "https" {
		_, _ = w.WriteString(html.EscapeString(alt))
		return ast.WalkSkipChildren, nil
	}
	_, _ = w.WriteString(`<a class="btn image-link" href="` + html.EscapeString(u.String()) + `" target="_blank" rel="noopener noreferrer" data-image data-alt="` +
		html.EscapeString(alt) + `"><svg class="ico" aria-hidden="true"><use href="/static/icons.svg#image"/></svg>` + html.EscapeString(u.Hostname()) + `</a>`)
	return ast.WalkSkipChildren, nil
}

// webAddress is a destination as Markdown means it — its backslash escapes
// and character references resolved, as goldmark's own renderer resolves
// them (util.URLEscape) — when that is an http or https address with a host.
func webAddress(dest []byte) (*url.URL, bool) {
	u, err := url.Parse(string(util.URLEscape(dest, true)))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, false
	}
	return u, true
}

func writeLink(w util.BufWriter, dest, label string) {
	_, _ = w.WriteString(`<a href="` + html.EscapeString(dest) + `" target="_blank" rel="noopener noreferrer">` + html.EscapeString(label) + `</a>`)
}
