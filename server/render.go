package server

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"strings"

	"github.com/daios-ai/juice-ui/apps"
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
	Title        string
	Theme        string          // theme slug, or "" to defer to prefers-color-scheme
	Sidebar      string          // "collapsed" | "" (expanded); stamped to avoid flash
	Active       string          // active nav group ("run"|"action"|…) for aria-current
	Handle       string          // signed-in handle for the nav, or "" when anonymous
	KernelHandle string          // this kernel's own handle (from /health), or "" if unknown
	KernelFP     string          // short key fingerprint for pre-auth verification (login only)
	IsSuperuser  bool            // handle == "@sys": show the admin group and admin scope
	Available    int64           // signed-in user's available balance; 0 when unknown
	Themes       []ANSIPalette   // selectable terminal themes for the picker
	CSRFField    template.HTML   // hidden CSRF input for no-JS forms
	CSRFToken    string          // raw token for the <meta> tag htmx reads
	Commands     template.JS     // JSON command registry for the keyboard palette (inert data)
	Flash        string          // transient user message
	FlashKind    string          // "success" | "error" | "info" | ""
	AppNav       []apps.NavEntry // sidebar entries contributed by apps
	BadgeSecs    int             // nav-badge poll interval in seconds (steps + apps)
	Data         any             // page-specific view model
}

// Renderer holds parsed templates. Each page is parsed together with the layout
// and shared partials; partials double as htmx swap fragments (R2.6).
type Renderer struct {
	pages    map[string]*template.Template
	partials *template.Template
}

// NewRenderer parses all embedded templates, plus any app-module template
// filesystems. A app's partials (files under templates/ whose name starts
// with "_") are merged into the shared base set; its pages are cloned from that
// base, so a app page renders through the same layout, partials, and helpers
// as a core page.
func NewRenderer(appTemplates ...fs.FS) (*Renderer, error) {
	funcs := template.FuncMap{
		"money":        money,
		"json":         prettyJSON,
		"jsonEdit":     jsonEdit,
		"statusClass":  statusClass,
		"truncate":     truncate,
		"costOf":       costOf,
		"buildForm":    BuildForm, // lets templates derive a FormModel from a raw schema
		"pct":          pct,
		"ratingPct":    ratingPct,
		"refOwner":     refOwner,
		"refName":      refName,
		"shortTime":    shortTime,
		"txAmount":     txAmount,
		"txrow":        txRowOf,
		"kindBadge":    kindBadge,
		"tab":          tab,
		"paneltab":     paneltab,
		"actionsHref":  actionsHref,
		"runForm":      runForm,
		"completeForm": completeForm,
		"statStrip":    statStrip,
		"badge":        badge,
	}
	base, err := template.New("base").Funcs(funcs).ParseFS(tmplFS, "templates/layout.html", "templates/_*.html")
	if err != nil {
		return nil, fmt.Errorf("parse base templates: %w", err)
	}
	// Merge each app's partials into the base set first, so a app page
	// (cloned below) can reference both core and app partials.
	for _, ffs := range appTemplates {
		if parts, _ := fs.Glob(ffs, "templates/_*.html"); len(parts) > 0 {
			if _, err := base.ParseFS(ffs, "templates/_*.html"); err != nil {
				return nil, fmt.Errorf("parse app partials: %w", err)
			}
		}
	}
	pages := map[string]*template.Template{}
	addPages := func(fsys fs.FS) error {
		files, err := fs.Glob(fsys, "templates/*.html")
		if err != nil {
			return err
		}
		for _, pf := range files {
			name := strings.TrimSuffix(strings.TrimPrefix(pf, "templates/"), ".html")
			if name == "layout" || strings.HasPrefix(name, "_") {
				continue
			}
			clone, err := base.Clone()
			if err != nil {
				return err
			}
			if _, err := clone.ParseFS(fsys, pf); err != nil {
				return fmt.Errorf("parse page %s: %w", pf, err)
			}
			pages[name] = clone
		}
		return nil
	}
	if err := addPages(tmplFS); err != nil {
		return nil, err
	}
	for _, ffs := range appTemplates {
		if err := addPages(ffs); err != nil {
			return nil, err
		}
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

// indentJSON pretty-prints inline JSON (args/result) as indented text (R4.2),
// never double-encoding (structured fields arrive as raw JSON). Empty input yields
// the given placeholder.
func indentJSON(raw json.RawMessage, empty string) string {
	if len(bytes.TrimSpace(raw)) == 0 {
		return empty
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return string(raw)
	}
	return buf.String()
}

// prettyJSON shows a value read-only ("—" when absent); jsonEdit prefills an
// editable <textarea> (blank when absent, not the "—" placeholder).
func prettyJSON(raw json.RawMessage) string { return indentJSON(raw, "—") }
func jsonEdit(raw json.RawMessage) string   { return indentJSON(raw, "") }

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

// refOwner returns the owner segment of "@owner/name" (including the leading @),
// split on the first slash — names may contain slashes (e.g. @sys/llm/chat), but
// handles never do, so the first slash is unambiguous. The owner segment is
// emphasized in the UI and links to the owner's actions.
func refOwner(ref string) string {
	if i := strings.IndexByte(ref, '/'); i >= 0 {
		return ref[:i]
	}
	return ref
}

// refName returns the name segment of "@owner/name" (everything after the first
// slash), or "" if there is no slash.
func refName(ref string) string {
	if i := strings.IndexByte(ref, '/'); i >= 0 {
		return ref[i+1:]
	}
	return ""
}

// Tab is one rendered tab for the _tab partial. Active drives both the class and
// aria-current, so they can't diverge. A non-empty Data marks a JS panel toggle
// (login) — it adds data-tab + role="tab". Built via tab / paneltab.
type Tab struct {
	Label, Href, Data string
	Active            bool
}

// StatStrip feeds the _stat_strip partial. Counts toggles the (N) run/rater
// counts — shown on the action detail page, omitted on the list cards.
type StatStrip struct {
	Price  int64
	S      client.Stats
	Counts bool
}

// statStrip builds a StatStrip. It is nil-safe (a card's *client.Stats is nil
// until the action has usage), so an unused action renders honest empty states.
func statStrip(price int64, s *client.Stats, counts bool) StatStrip {
	var v client.Stats
	if s != nil {
		v = *s
	}
	return StatStrip{Price: price, S: v, Counts: counts}
}

// tab builds a plain navigation tab (list filters, scope switch).
func tab(label, href string, active bool) Tab { return Tab{Label: label, Href: href, Active: active} }

// paneltab builds a login toggle tab: its href is /login?tab=<key> and its Data
// (the panel key) makes _tab emit data-tab + role="tab" for the JS swap.
func paneltab(label, key string, active bool) Tab {
	return Tab{Label: label, Href: "/login?tab=" + key, Data: key, Active: active}
}

// actionsHref builds the /actions URL for a given ownership scope ("" | "all")
// and state filter ("" | "active" | "inactive"), so a tab in one axis preserves
// the other axis's current value.
func actionsHref(scope, filter string) string {
	q := url.Values{}
	if scope == "all" {
		q.Set("scope", "all")
	}
	if filter != "" {
		q.Set("filter", filter)
	}
	if len(q) == 0 {
		return "/actions"
	}
	return "/actions?" + q.Encode()
}

// badgeView is the model for the _badge_poll partial: a nav attention badge that
// htmx re-fetches on a fixed interval. Count is the initial server-rendered value
// (so a no-JS client still sees it); the poll then overwrites it.
type badgeView struct {
	URL   string
	Label string
	Count int
	Every int // poll interval, seconds
}

// badge builds a badgeView for the template, defaulting the interval defensively
// so a mis-plumbed 0 never yields an invalid "every 0s" trigger.
func badge(url, label string, count, secs int) badgeView {
	if secs < 1 {
		secs = 30
	}
	return badgeView{URL: url, Label: label, Count: count, Every: secs}
}

// KindBadge is the single-letter attribute chip for an action's execution kind.
// R (remote) is the notable one and stands out by weight (a filled chip), not
// color — every theme hue is reserved for a money/trust signal.
type KindBadge struct {
	Letter string
	Title  string
	Filled bool
}

// kindBadge maps an action kind to its chip. Unknown kinds fall back to the
// uppercased first rune with the raw kind as the tooltip, so nothing leaks the
// internal token as visible text.
func kindBadge(kind string) KindBadge {
	switch kind {
	case "wasm":
		return KindBadge{"W", "WebAssembly action", false}
	case "native":
		return KindBadge{"N", "Native action", false}
	case "http":
		return KindBadge{"H", "HTTP action", false}
	case "remote_proxy":
		return KindBadge{"R", "Remote — runs on another kernel", true}
	default:
		letter := "?"
		if kind != "" {
			letter = strings.ToUpper(kind[:1])
		}
		return KindBadge{letter, kind, false}
	}
}

// shortTime trims an RFC3339 timestamp to "YYYY-MM-DD HH:MM" for compact display.
func shortTime(ts string) string {
	if len(ts) < 16 {
		return ts
	}
	return ts[:10] + " " + ts[11:16]
}

// txRow bundles a transaction with the viewer's handle and the resolved action
// ref, so one partial (_tx_row) renders every transaction list identically.
// Built in-template via the `txrow` helper.
type txRow struct {
	Tx     client.Transaction
	Viewer string
	Ref    string // full "@owner/name" when the action is known, else ""
}

// txRowOf resolves the action ref (via the actions map, keyed by action id) and
// pairs the transaction with the viewer handle.
func txRowOf(tx client.Transaction, viewer string, actions map[string]client.Action) txRow {
	ref := ""
	if a, ok := actions[tx.ActionID]; ok && a.ID != "" {
		ref = a.Ref()
	}
	return txRow{Tx: tx, Viewer: viewer, Ref: ref}
}

// amountDash is the amount cell for a viewer with no stake in a transaction.
const amountDash = template.HTML(`<span class="muted">—</span>`)

// txAmount renders a transaction's signed, perspective-relative amount as seen
// by `viewer` — "what did this call do to my balance". On a settled call the
// seller sees +net, the buyer −gross, and the platform (@sys) +fee; a viewer
// occupying several roles sees the sum (a self-call nets to −fee; @sys's own fee
// returns to it). A failed call shows the buyer the ↩ refund; an open call shows
// the buyer the ◷ parked gross. A viewer with no stake sees an em dash. Gross,
// fee and the full party list live on the transaction detail page.
func txAmount(tx client.Transaction, viewer string) template.HTML {
	switch tx.Status {
	case "failure":
		if viewer == tx.OwnerHandle {
			return sigAmount("sig-refunded", "↩ ", tx.Gross)
		}
		return amountDash
	case "open":
		if viewer == tx.OwnerHandle {
			return sigAmount("sig-escrow", "◷ ", tx.Gross)
		}
		return amountDash
	default: // success / settled
		var amt int64
		if viewer == tx.OwnerHandle {
			amt -= tx.Gross
		}
		if viewer == tx.TargetHandle {
			amt += tx.Net
		}
		if viewer == superuserHandle {
			amt += tx.Fee
		}
		switch {
		case amt > 0:
			return sigAmount("sig-credited", "+", amt)
		case amt < 0:
			return sigAmount("", "", amt) // money() already prints the leading −
		default:
			return amountDash
		}
	}
}

// sigAmount builds a money cell: an optional signal class + glyph before the
// formatted amount. Inputs are constant-controlled or numeric, so the result is
// safe HTML.
func sigAmount(sig, glyph string, n int64) template.HTML {
	cls := "money"
	if sig != "" {
		cls += " " + sig
	}
	return template.HTML(`<span class="` + cls + `">` + glyph + template.HTMLEscapeString(money(n)) + `</span>`)
}
