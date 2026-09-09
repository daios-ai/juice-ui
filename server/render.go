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
	"strconv"
	"strings"
	"time"

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

// NavItem is one entry in the single server-built nav model. The sidebar, the
// mobile tab bar, and the palette all render from this list, so the three cannot
// drift (the old layout hand-duplicated them). Icon is the full inline <svg> so a
// core sprite entry and an app's inline icon render uniformly.
type NavItem struct {
	Slug     string
	Label    string
	Href     string
	Icon     template.HTML
	Active   bool
	Badge    int    // static count (apps); 0 hides
	BadgeURL string // polled badge endpoint; "" for none
	Divider  bool   // render a separator before this entry (marks the apps group)
}

// Page is the data passed to the layout for a full-page render.
type Page struct {
	Title        string
	Theme        string          // theme slug, or "" to defer to prefers-color-scheme
	Sidebar      string          // "collapsed" | "" (expanded); stamped to avoid flash
	Active       string          // active nav group ("browse"|"inbox"|…) for aria-current
	Nav          []NavItem       // the single nav model (sidebar + drawer + palette)
	Handle       string          // signed-in handle for the nav, or "" when anonymous
	KernelHandle string          // this kernel's own handle (from /health), or "" if unknown
	KernelFP     string          // short key fingerprint for pre-auth verification (login only)
	KernelNet    string          // the network this kernel serves, named on the login page
	IsSuperuser  bool            // handle == "sys": show the admin group and admin scope
	Available    int64           // signed-in user's available balance; 0 when unknown
	Themes       []ANSIPalette   // selectable terminal themes for the picker
	CSRFField    template.HTML   // hidden CSRF input for no-JS forms
	CSRFToken    string          // raw token for the <meta> tag htmx reads
	Commands     template.JS     // JSON command registry for the keyboard palette (inert data)
	Flash        string          // transient user message
	FlashKind    string          // "success" | "error" | "info" | ""
	AppNav       []apps.NavEntry // sidebar entries contributed by apps
	BadgeSecs    int             // nav-badge poll interval in seconds (steps + apps)
	Path         string          // current path, for links that must return to it
	MenuOpen     bool            // ?menu=open: server-render the nav drawer [open] (no-JS path)
	Data         any             // page-specific view model
}

// Renderer holds parsed templates. Each page is parsed together with the layout
// and shared partials; partials double as htmx swap fragments (R2.6).
type Renderer struct {
	pages    map[string]*template.Template
	partials *template.Template
	unit     func() Unit // the kernel's money unit; nil until wiring sets it
}

// SetUnitSource supplies the kernel's money unit to the money helpers. Amounts
// are integers in the world's base unit and only the kernel knows how that unit
// scales (§7), so the renderer reads it from the live /health identity rather
// than assuming one. Called once during wiring (server.New), before serving.
func (r *Renderer) SetUnitSource(f func() Unit) { r.unit = f }

// Unit is the money unit of the world this kernel serves: how many decimal
// places its base-unit integer carries, and the network's name. One kernel
// serves one world for life (kernel §D23), so this is fixed per installation.
type Unit struct {
	Decimals int
	Network  string
}

// Format renders a base-unit integer for display: thousands-grouped, sign
// first, and always six fraction digits, so amounts from different worlds line
// up in one column. On a world with no fractional unit (decimals 0) 133 reads
// "133.000000"; on a six-decimal world it reads "0.000133". The unit itself is
// named separately (the `unit` helper), never with a currency mark.
func (u Unit) Format(n int64) string {
	neg := n < 0
	if neg {
		n = -n
	}
	whole, frac := u.split(n)
	out := group3(whole) + "." + frac
	if neg {
		return "-" + out
	}
	return out
}

// Input renders a non-negative base-unit integer as a bare decimal for an HTML
// number input's value (no grouping — number inputs reject commas).
func (u Unit) Input(n int64) string {
	if n < 0 {
		n = -n
	}
	whole, frac := u.split(n)
	return strconv.FormatInt(whole, 10) + "." + frac
}

// split divides a non-negative base-unit amount into its whole part and its
// six-digit fraction string.
func (u Unit) split(n int64) (int64, string) {
	d := u.Decimals
	if d < 0 {
		d = 0
	}
	if d > 6 {
		d = 6 // beyond six digits the display truncates; no shipped world needs it
	}
	scale := pow10(d)
	whole := n / scale
	frac := (n % scale) * pow10(6-d)
	return whole, fmt.Sprintf("%06d", frac)
}

// pow10 returns 10^n for the small n a decimals field can hold.
func pow10(n int) int64 {
	p := int64(1)
	for i := 0; i < n; i++ {
		p *= 10
	}
	return p
}

// parseAmount is the inverse of Unit.Format: it reads a typed amount and returns
// the kernel's base-unit integer. It rejects a negative, a non-numeric value, and
// a digit finer than the world's base unit — silently rounding someone's money
// would be worse than refusing it.
func parseAmount(s string, decimals int) (int64, error) {
	s = strings.NewReplacer(",", "", " ", "").Replace(strings.TrimSpace(s))
	if s == "" {
		return 0, nil
	}
	if strings.HasPrefix(s, "-") {
		return 0, fmt.Errorf("amount must not be negative")
	}
	whole, frac, hasFrac := strings.Cut(s, ".")
	w, err := strconv.ParseInt(whole, 10, 64)
	if whole != "" && err != nil {
		return 0, fmt.Errorf("amount must be a number")
	}
	if decimals < 0 {
		decimals = 0
	}
	if !hasFrac {
		return w * pow10(decimals), nil
	}
	if len(frac) > decimals && strings.Trim(frac[decimals:], "0") != "" {
		if decimals == 0 {
			return 0, fmt.Errorf("amount must be a whole number here")
		}
		return 0, fmt.Errorf("amount has at most %d decimals here", decimals)
	}
	frac = (frac + strings.Repeat("0", decimals))[:decimals]
	f := int64(0)
	if frac != "" {
		if f, err = strconv.ParseInt(frac, 10, 64); err != nil {
			return 0, fmt.Errorf("amount must be a number")
		}
	}
	return w*pow10(decimals) + f, nil
}

// NewRenderer parses all embedded templates, plus any app-module template
// filesystems. A app's partials (files under templates/ whose name starts
// with "_") are merged into the shared base set; its pages are cloned from that
// base, so a app page renders through the same layout, partials, and helpers
// as a core page.
func NewRenderer(appTemplates ...fs.FS) (*Renderer, error) {
	r := &Renderer{}
	funcs := template.FuncMap{
		"money":        r.money,
		"unit":         r.unitName,
		"amountInput":  r.amountInput,
		"sum":          func(a, b int64) int64 { return a + b },
		"sub":          func(a, b int64) int64 { return a - b },
		"json":         prettyJSON,
		"jsonEdit":     jsonEdit,
		"statusClass":  statusClass,
		"truncate":     truncate,
		"costOf":       costOf,
		"buildForm":    BuildForm, // lets templates derive a FormModel from a raw schema
		"pct":          pct,
		"ratingPct":    ratingPct,
		"refFolder":    refFolder,
		"nameOf":       nameOf,
		"obligation":   obligation,
		"ref":          refOf,
		"price":        priceOf,
		"shortTime":    shortTime,
		"relTime":      relTime,
		"txAmount":     r.txAmount,
		"txrow":        txRowOf,
		"kindBadge":    kindBadge,
		"tab":          tab,
		"paneltab":     paneltab,
		"actionsHref":  actionsHref,
		"peersHref":    peersHref,
		"runForm":      runForm,
		"completeForm": completeForm,
		"statStrip":    statStrip,
		"badge":        badge,
		"cardArt":      CardArt,
		"initial":      initial,
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
	r.pages, r.partials = pages, base
	return r, nil
}

// unitOrZero is the money unit, or a zero unit before wiring (tests that render
// without a server) — whole base units, unnamed network.
func (r *Renderer) unitOrZero() Unit {
	if r.unit == nil {
		return Unit{}
	}
	return r.unit()
}

// money formats a base-unit amount for display — the single source of truth for
// every amount the UI shows (§7).
func (r *Renderer) money(n int64) string { return r.unitOrZero().Format(n) }

// amountInput formats a base-unit amount as an HTML number input's value.
func (r *Renderer) amountInput(n int64) string { return r.unitOrZero().Input(n) }

// unitName is the network whose money this is, for the quiet label beside a
// balance. Empty before the kernel has answered, and then nothing is labelled.
func (r *Renderer) unitName() string { return r.unitOrZero().Network }

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

// group3 renders a non-negative integer with thousands-separating commas.
func group3(n int64) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var parts []string
	for len(s) > 3 {
		parts = append([]string{s[len(s)-3:]}, parts...)
		s = s[:len(s)-3]
	}
	parts = append([]string{s}, parts...)
	return strings.Join(parts, ",")
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

// PriceView is a price as rendered: the amount, and whether it is indicative
// rather than binding. Free is not a special case in the data — it is what the
// template says when the amount is zero — so every price on every surface goes
// through one partial and cannot drift.
//
// Indicative marks a DISCOVERED action's price: it comes from the gossip
// catalog, refreshed each discovery pass, and only resolution fixes the binding
// figure. It is marked quietly and with an accessible label, never by weight
// alone, which a screen reader cannot perceive.
type PriceView struct {
	Price      int64
	Indicative bool
}

// priceOf builds a PriceView. The one entry point templates use.
func priceOf(price int64, indicative bool) PriceView {
	return PriceView{Price: price, Indicative: indicative}
}

// Name is a kernel name rendered for a human: the text to show, the full value
// behind it, and whether that text is a truncated key.
//
// It exists because the kernel's naming is Stiegler's petname system (§13) and
// the three name classes are NOT interchangeable: a public key is global and
// self-certifying but unreadable, a nickname is self-asserted by the remote and
// never resolves, and a petname is bound here and does resolve. The UI shows a
// petname whole and a key truncated, and it never shows a nickname where a name
// that resolves is expected.
type Name struct {
	Text  string // what to render
	Title string // the full value, for the title attribute
	IsKey bool   // Text is a truncated key, not a bound name
}

// keyLen is how much of a public key survives truncation — enough to
// distinguish kernels by eye, short enough not to dominate a card title.
const keyLen = 8

// looksLikeKey reports whether s has the shape of a base64url public key: the
// kernel's keys are 43 characters and its bare names are validated to be
// neither key- nor id-shaped (§13, §14), so length alone separates them without
// a false positive on a long petname.
func looksLikeKey(s string) bool { return len(s) >= 43 }

// keyDisplay renders a raw value, truncating it when it is key-shaped. THE one
// place truncation is decided: refs (via ref) and kernel cards both route here,
// so a key never renders whole in one place and clipped in another.
func keyDisplay(s string) Name {
	if looksLikeKey(s) {
		return Name{Text: s[:keyLen] + "…", Title: s, IsKey: true}
	}
	return Name{Text: s, Title: s}
}

// nameOf picks the name to show for a kernel: its petname when one is bound,
// else its truncated key. A nickname is never chosen — it does not resolve, so
// showing it where a name is expected would invite typing something the
// resolver rejects.
// obligationRow pairs one debt with the debtor's name as this kernel knows it
// and the CSRF field its confirm needs, so _obligation_row renders from one
// value. The petname map is the roster's; an unbound kernel shows its truncated
// key, which is what the form posts either way.
type obligationRow struct {
	O         client.Obligation
	Peer      Name
	CSRFField template.HTML
}

// obligation builds that pairing (template helper).
func obligation(o client.Obligation, petnames map[string]string, csrfField template.HTML) obligationRow {
	return obligationRow{O: o, Peer: nameOf(petnames[o.Peer], o.Peer), CSRFField: csrfField}
}

func nameOf(petname, key string) Name {
	if petname != "" {
		return Name{Text: petname, Title: key}
	}
	return keyDisplay(key)
}

// refOwner returns the owner segment of "owner/name", split on the first
// slash — names may contain slashes (e.g. sys/llm/chat), but handles never do,
// so the first slash is unambiguous (requirements.md §4). For a remote ref
// (owner@kernel/name) the whole "owner@kernel" segment is returned. The owner
// segment is emphasized in the UI; a LOCAL owner links to its owner page, a
// remote one renders unlinked (the kernel alias is presentation-only).
func refOwner(ref string) string {
	if i := strings.IndexByte(ref, '/'); i >= 0 {
		return ref[:i]
	}
	return ref
}

// refKernel returns the kernel-alias half of a remote owner segment
// ("alice@B/greet" → "B"), or "" for a local ref. The alias is presentation
// only — renamable and collision-suffixed on this kernel — so nothing may key
// or link on it (requirements.md §4).
func refKernel(ref string) string {
	owner := refOwner(ref)
	if i := strings.IndexByte(owner, '@'); i >= 0 {
		return owner[i+1:]
	}
	return ""
}

// refIsRemote reports whether ref names an action on a peer kernel
// (owner@kernel/name). Remote owners have no local owner page: the _ref
// partials render them unlinked.
func refIsRemote(ref string) bool { return refKernel(ref) != "" }

// RefView is a rendered action reference: the owner segment, the name half, and
// the id the name links to when one exists. THE one place a ref is split, so a
// key never renders whole in one template and truncated in another.
//
// Owner is a Name because a remote owner segment carries a kernel that may be a
// raw public key (owner@<43 chars>) — unreadable as a card title, and truncated
// here through the same primitive that renders a kernel card (§13 petname
// model). Local reports that the owner has a profile page here; a remote owner
// never links, the kernel half being presentation-only (requirements.md §4).
type RefView struct {
	Owner Name
	Name  string
	ID    string // "" ⇒ the name is not a link
	Local bool
}

// refOf splits a ref for rendering. Split on the FIRST slash: names may contain
// slashes (sys/llm/chat) but handles never do, so the first is unambiguous.
func refOf(ref, id string) RefView {
	owner, name := refOwner(ref), refName(ref)
	v := RefView{Name: name, ID: id, Local: !refIsRemote(ref)}
	if v.Local {
		v.Owner = Name{Text: owner, Title: owner}
		return v
	}
	// owner@kernel: the kernel half alone is truncated when it is a raw key, so
	// the owner stays readable and the whole segment survives in the title.
	at := strings.IndexByte(owner, '@')
	k := keyDisplay(owner[at+1:])
	v.Owner = Name{Text: owner[:at+1] + k.Text, Title: owner, IsKey: k.IsKey}
	return v
}

// refName returns the name segment of "owner/name" (everything after the first
// slash), or "" if there is no slash.
func refName(ref string) string {
	if i := strings.IndexByte(ref, '/'); i >= 0 {
		return ref[i+1:]
	}
	return ""
}

// refFolder returns the folder of a ref: everything up to the LAST '/'
// (service/a → service, chat/msg/message → chat/msg). This is the kernel's
// `directory` grouping — the grant selector that connects a delegated action's
// whole provider group in one gesture, not the degenerate single-action
// selector that would connect only the one failing action. Distinct from
// refOwner, which splits on the FIRST slash (and would wrongly collapse
// chat/msg/message to chat, sweeping in chat/register). A ref with no slash
// is returned unchanged.
func refFolder(ref string) string {
	if i := strings.LastIndexByte(ref, '/'); i >= 0 {
		return ref[:i]
	}
	return ref
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
	Price     int64
	S         client.Stats
	Counts    bool
	WithPrice bool // detail page leads with price; cards omit it (shown top-right)
}

// statStrip builds a StatStrip. It is nil-safe (a card's *client.Stats is nil
// until the action has usage), so an unused action renders honest empty states.
func statStrip(price int64, s *client.Stats, counts, withPrice bool) StatStrip {
	var v client.Stats
	if s != nil {
		v = *s
	}
	return StatStrip{Price: price, S: v, Counts: counts, WithPrice: withPrice}
}

// tab builds a plain navigation tab (list filters, scope switch).
func tab(label, href string, active bool) Tab { return Tab{Label: label, Href: href, Active: active} }

// paneltab builds a login toggle tab: its href is /login?tab=<key> and its Data
// (the panel key) makes _tab emit data-tab + role="tab" for the JS swap.
func paneltab(label, key string, active bool) Tab {
	return Tab{Label: label, Href: "/login?tab=" + key, Data: key, Active: active}
}

// initial returns the first letter of a handle, uppercased, for an avatar
// monogram. "" → "?".
func initial(handle string) string {
	for _, r := range handle {
		return strings.ToUpper(string(r))
	}
	return "?"
}

// Pager is the model for the _pager partial: prev/next links (empty at a
// boundary) and the 1-based page number. Money accent is reserved, so the pager
// renders as quiet links, never accented.
type Pager struct {
	PrevHref string
	NextHref string
	Page     int
}

// pageSize is the uniform list page size across the UI (browse, transactions,
// the workshop table, the admin roster, ratings).
const pageSize = 20

// pageNav builds a Pager for an offset-paginated list. It preserves the current
// query (search terms, form state) and only rewrites the offset param, so paging
// composes with a search. hasNext is the caller's "there is another page" signal
// (typically an over-fetch by one: len(fetched) > pageSize).
func pageNav(r *http.Request, offset, size int, hasNext bool) Pager {
	if size < 1 {
		size = pageSize
	}
	if offset < 0 {
		offset = 0
	}
	href := func(off int) string {
		q := r.URL.Query()
		if off <= 0 {
			q.Del("offset")
		} else {
			q.Set("offset", strconv.Itoa(off))
		}
		if len(q) == 0 {
			return r.URL.Path
		}
		return r.URL.Path + "?" + q.Encode()
	}
	p := Pager{Page: offset/size + 1}
	if offset > 0 {
		p.PrevHref = href(offset - size)
	}
	if hasNext {
		p.NextHref = href(offset + size)
	}
	return p
}

// pageSlice returns the [start,end) window of n items for the given offset and
// page size, plus whether a further page exists — clamping offset into range.
//
// The UI paginates client-side over a fully-fetched list because the kernel
// applies `limit`/`offset` to RAW rows *before* its visibility/party filter, so a
// server-side page of a filtered list (browse actions, a user's transactions) can
// come back with fewer — or zero — visible rows than exist. Fetching the visible
// set whole and slicing here is the only correct offset pagination for those.
func pageSlice(n, offset, size int) (start, end int, hasNext bool) {
	if size < 1 {
		size = pageSize
	}
	if offset < 0 {
		offset = 0
	}
	if offset > n {
		offset = n
	}
	end = offset + size
	if end > n {
		end = n
	}
	return offset, end, offset+size < n
}

// queryOffset reads a non-negative ?offset from the request (0 on absence/garbage).
func queryOffset(r *http.Request) int {
	n, err := strconv.Atoi(r.URL.Query().Get("offset"))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// actionsHref builds the /workshop URL for a given ownership scope ("" | "all")
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
		return "/workshop"
	}
	return "/workshop?" + q.Encode()
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

// relTime renders an RFC3339 timestamp as a coarse "time ago" for freshness signals
// (a peer's last-seen): "just now", "3 mins ago", "2 hours ago", "5 days ago".
// Empty stays empty; an unparseable value falls back to its raw text.
func relTime(ts string) string {
	if ts == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ts
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return agoUnit(int(d.Minutes()), "min")
	case d < 24*time.Hour:
		return agoUnit(int(d.Hours()), "hour")
	default:
		return agoUnit(int(d.Hours()/24), "day")
	}
}

func agoUnit(n int, unit string) string {
	if n == 1 {
		return "1 " + unit + " ago"
	}
	return fmt.Sprintf("%d %ss ago", n, unit)
}

// txRow bundles a transaction with the viewer's handle and the resolved action
// ref, so one partial (_tx_row) renders every transaction list identically.
// Built in-template via the `txrow` helper.
type txRow struct {
	Tx     client.Transaction
	Viewer string
	Ref    string // full "owner/name" when the action is known, else ""
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

// entryRow bundles a ledger entry with the viewer's perspective, so the
// activity list renders a direct movement (deposit, withdrawal, transfer) beside
// a call without the template deciding sign or wording.
type entryRow struct {
	Entry    client.LedgerEntry
	Label    string // "deposit" | "withdrawal" | "transfer to bob" | "transfer from alice"
	Credited bool   // the amount arrived in the viewer's balance
}

// entryRowOf reads the movement from the kernel's own from/to projection: a
// deposit has no from-side, a withdrawal no to-side, a transfer both.
func entryRowOf(e client.LedgerEntry, viewer string) entryRow {
	r := entryRow{Entry: e, Credited: e.ToHandle == viewer}
	switch {
	case e.FromHandle == "":
		r.Label = "deposit"
	case e.ToHandle == "":
		r.Label = "withdrawal"
	case r.Credited:
		r.Label = "transfer from " + e.FromHandle
	default:
		r.Label = "transfer to " + e.ToHandle
	}
	return r
}

// amountDash is the amount cell for a viewer with no stake in a transaction.
const amountDash = template.HTML(`<span class="muted">—</span>`)

// txAmount renders a transaction's signed, perspective-relative amount as seen
// by `viewer` — "what did this call do to my balance". On a settled call the
// seller sees +net, the buyer −gross, and the platform (sys) +fee; a viewer
// occupying several roles sees the sum (a self-call nets to −fee; sys's own fee
// returns to it). A failed call shows the buyer the ↩ refund; an open call shows
// the buyer the ◷ parked gross. A viewer with no stake sees an em dash. Gross,
// fee and the full party list live on the transaction detail page.
func (r *Renderer) txAmount(tx client.Transaction, viewer string) template.HTML {
	switch tx.Status {
	case "failure":
		if viewer == tx.OwnerHandle {
			return r.sigAmount("sig-refunded", "↩ ", tx.Gross)
		}
		return amountDash
	case "open":
		if viewer == tx.OwnerHandle {
			return r.sigAmount("sig-escrow", "◷ ", tx.Gross)
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
			return r.sigAmount("sig-credited", "+", amt)
		case amt < 0:
			return r.sigAmount("", "", amt) // Format already prints the leading −
		default:
			return amountDash
		}
	}
}

// sigAmount builds a money cell: an optional signal class + glyph before the
// formatted amount. Inputs are constant-controlled or numeric, so the result is
// safe HTML.
func (r *Renderer) sigAmount(sig, glyph string, n int64) template.HTML {
	cls := "money"
	if sig != "" {
		cls += " " + sig
	}
	return template.HTML(`<span class="` + cls + `">` + glyph + template.HTMLEscapeString(r.money(n)) + `</span>`)
}
