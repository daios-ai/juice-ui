package server

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"strings"

	"github.com/daios-ai/juice-ui/client"
)

//go:embed templates/*.html
var tmplFS embed.FS

//go:embed static
var staticFS embed.FS

// StaticFS returns the embedded static asset filesystem rooted so that
// "/static/app.css" maps correctly.
func StaticFS() fs.FS {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err) // embedded; cannot fail in practice
	}
	return sub
}

// Page is the data passed to the layout for a full-page render.
type Page struct {
	Title     string
	Theme     string        // "light" | "dark" | "" (defer to prefers-color-scheme)
	Sidebar   string        // "collapsed" | "" (expanded); stamped to avoid flash
	Handle    string        // signed-in handle for the nav, or "" when anonymous
	Available int64         // signed-in user's available balance; 0 when unauthenticated or unknown
	CSRFField template.HTML // hidden CSRF input for no-JS forms
	CSRFToken string        // raw token for the <meta> tag htmx reads
	Flash     string        // transient user message
	FlashKind string        // "success" | "error" | "info" | ""
	Data      any           // page-specific view model
}

// Renderer holds parsed templates. Each page is parsed together with the layout
// and shared partials; partials double as htmx swap fragments (R2.6).
type Renderer struct {
	pages    map[string]*template.Template
	partials *template.Template
}

// NewRenderer parses all embedded templates.
func NewRenderer() (*Renderer, error) {
	funcs := template.FuncMap{
		"money":       money,
		"json":        prettyJSON,
		"jsonCompact": jsonCompact,
		"statusClass": statusClass,
		"truncate":    truncate,
		"costOf":      costOf,
		"buildForm":   BuildForm, // lets templates derive a FormModel from a raw schema
		"pct":         pct,
		"ratingPct":   ratingPct,
	}
	base, err := template.New("base").Funcs(funcs).ParseFS(tmplFS, "templates/layout.html", "templates/_*.html")
	if err != nil {
		return nil, fmt.Errorf("parse base templates: %w", err)
	}
	pageFiles, err := fs.Glob(tmplFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	pages := map[string]*template.Template{}
	for _, pf := range pageFiles {
		name := strings.TrimSuffix(strings.TrimPrefix(pf, "templates/"), ".html")
		if name == "layout" || strings.HasPrefix(name, "_") {
			continue
		}
		clone, err := base.Clone()
		if err != nil {
			return nil, err
		}
		if _, err := clone.ParseFS(tmplFS, pf); err != nil {
			return nil, fmt.Errorf("parse page %s: %w", pf, err)
		}
		pages[name] = clone
	}
	return &Renderer{pages: pages, partials: base}, nil
}

// Render writes a full page (page template wrapped in the layout). It buffers
// first so a template error never produces a half-written response.
func (r *Renderer) Render(w http.ResponseWriter, status int, page string, p Page) error {
	t, ok := r.pages[page]
	if !ok {
		return fmt.Errorf("unknown page %q", page)
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout", p); err != nil {
		return fmt.Errorf("render %s: %w", page, err)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, err := buf.WriteTo(w)
	return err
}

// Fragment writes a single named partial, for htmx swaps.
func (r *Renderer) Fragment(w http.ResponseWriter, status int, name string, data any) error {
	var buf bytes.Buffer
	if err := r.partials.ExecuteTemplate(&buf, name, data); err != nil {
		return fmt.Errorf("render fragment %s: %w", name, err)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, err := buf.WriteTo(w)
	return err
}

// ---- template helpers ----

// money formats integer credits with thousands separators. Money is rendered in
// a monospace face by the stylesheet (R8.5).
func money(n int64) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		if neg {
			return "-" + s
		}
		return s
	}
	var parts []string
	for len(s) > 3 {
		parts = append([]string{s[len(s)-3:]}, parts...)
		s = s[:len(s)-3]
	}
	parts = append([]string{s}, parts...)
	out := strings.Join(parts, ",")
	if neg {
		return "-" + out
	}
	return out
}

// prettyJSON renders inline JSON (args/result) as indented text (R4.2). It never
// double-encodes: structured fields arrive as raw JSON and are pretty-printed.
func jsonCompact(raw json.RawMessage) string {
	if len(bytes.TrimSpace(raw)) == 0 {
		return ""
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return string(raw)
	}
	return buf.String()
}

func prettyJSON(raw json.RawMessage) string {
	if len(bytes.TrimSpace(raw)) == 0 {
		return "—"
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return string(raw)
	}
	return buf.String()
}

// statusClass maps a transaction/credit status to a CSS class. The three
// financial states use mutually distinct, trust-signal colors (R8.7).
func statusClass(status string) string {
	switch status {
	case "success":
		return "status-credited"
	case "failure":
		return "status-refunded"
	case "open":
		return "status-escrow"
	default:
		return ""
	}
}

// costOf extracts a success transaction's gross/fee/net for the breakdown
// partial.
func costOf(tx client.Transaction) Cost {
	return Cost{Gross: tx.Gross, Fee: tx.Fee, Net: tx.Net}
}

func truncate(n int, s string) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// pct formats the ratio n/d as a whole-number percentage string (e.g. "97").
// Returns "—" when d is zero to avoid division by zero.
func pct(n, d int64) string {
	if d == 0 {
		return "—"
	}
	return fmt.Sprintf("%.0f", float64(n)/float64(d)*100)
}

// ratingPct converts a 0–1 mean rating estimate to a 0–100 percentage string.
// Returns "" when count is zero (no ratings yet).
func ratingPct(estimate float64, count int64) string {
	if count == 0 {
		return ""
	}
	return fmt.Sprintf("%.0f", estimate*100)
}
