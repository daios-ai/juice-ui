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
	"regexp"
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
	Me           string          // signed-in address for the nav, or "" when anonymous
	KernelHandle string          // this kernel's own name (from /health), or "" if unknown
	KernelFP     string          // short key fingerprint for pre-auth verification (login only)
	KernelNet    string          // the network this kernel serves, named on the login page
	KernelName   string          // the name this kernel is recorded under, when the UI names one
	IsSuperuser  bool            // Me == sys@<kernel>: show the admin group and admin scope
	Available    int64           // signed-in user's available balance; 0 when unknown
	Themes       []ANSIPalette   // selectable terminal themes for the picker
	CSRFField    template.HTML   // hidden CSRF input for no-JS forms
	CSRFToken    string          // raw token for the <meta> tag htmx reads
	Commands     template.JS     // JSON command registry for the keyboard palette (inert data)
	Flash        string          // transient user message
	FlashKind    string          // "success" | "error" | "info" | ""
	AppNav       []apps.NavEntry // sidebar entries contributed by apps
	BadgeSecs    int             // nav-badge poll interval in seconds (tasks + apps)
	Path         string          // current path, for links that must return to it
	MenuOpen     bool            // ?menu=open: server-render the nav drawer [open] (no-JS path)
	Data         any             // page-specific view model
}

// Renderer holds parsed templates. Each page is parsed together with the layout
// and shared partials; partials double as htmx swap fragments (R2.6).
type Renderer struct {
	pages    map[string]*template.Template
	partials *template.Template
	identity *KernelIdentity // the kernel's money unit and name, given at construction
}

// Unit is the money unit of the world this kernel serves: how many decimal
// places its base-unit integer carries, and the symbol that names it. One kernel
// serves one world for life (kernel §D23), so this is fixed per installation.
type Unit struct {
	Decimals int
	Symbol   string
}

// Format renders a base-unit integer the way the kernel renders it, symbol and
// all: "133 credits" on a world with no fractional unit, "1.50 USDC" on one with
// six. Trailing zeros are trimmed to a two-digit floor — a round amount reads
// 1.50, not 1.500000, while extra digits are kept, because rounding money for
// display is a lie the reconciliation finds — and there are NO separators, so
// the text pastes back into an amount field unchanged.
//
// This is a transcription of the kernel's own Network.Amount. The BFF may not
// import kernel packages (CLAUDE.md), so the algorithm is copied; the test pins
// the kernel's own examples, and that test is what keeps the two from drifting.
func (u Unit) Format(v int64) string {
	unit := ""
	if u.Symbol != "" {
		unit = " " + u.Symbol
	}
	return u.digits(v) + unit
}

// Input renders the same amount without its symbol, for a number input's value:
// what the field shows is what the kernel's parser accepts back.
func (u Unit) Input(v int64) string { return u.digits(v) }

// Step is the granularity of an amount input on this world — 1 where money has
// no fractional unit, else the smallest amount that exists. A hint only: the
// kernel validates, and the server refuses anything finer (§6.2).
func (u Unit) Step() string {
	if u.Decimals <= 0 {
		return "1"
	}
	return "0." + strings.Repeat("0", u.Decimals-1) + "1"
}

// digits renders the amount without a symbol.
func (u Unit) digits(v int64) string {
	if u.Decimals <= 0 {
		return strconv.FormatInt(v, 10)
	}
	s := strconv.FormatInt(v, 10)
	sign := ""
	if strings.HasPrefix(s, "-") {
		sign, s = "-", s[1:]
	}
	for len(s) <= u.Decimals {
		s = "0" + s
	}
	whole, frac := s[:len(s)-u.Decimals], s[len(s)-u.Decimals:]
	for len(frac) > 2 && strings.HasSuffix(frac, "0") {
		frac = frac[:len(frac)-1]
	}
	return sign + whole + "." + frac
}

// parseAmount is the inverse of Unit.Input: it reads a typed amount and returns
// the kernel's base-unit integer. Digits and at most the world's own decimal
// places — a separator is refused rather than stripped, because nothing renders
// one, and a digit finer than the base unit is refused rather than rounded:
// silently changing someone's amount is worse than declining it.
//
// Zero is allowed. An action may cost nothing, and the kernel's own parser is
// stricter only because its callers are money verbs.
func parseAmount(s string, decimals int) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	if decimals < 0 {
		decimals = 0
	}
	whole, frac, hasFrac := strings.Cut(s, ".")
	if !allDigits(whole) || (hasFrac && !allDigits(frac)) {
		return 0, fmt.Errorf("amount must be a number, without separators")
	}
	if len(frac) > decimals {
		if decimals == 0 {
			return 0, fmt.Errorf("amount must be a whole number here")
		}
		return 0, fmt.Errorf("amount has at most %d decimals here", decimals)
	}
	v, err := strconv.ParseInt(whole+frac+strings.Repeat("0", decimals-len(frac)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("amount must be a number, without separators")
	}
	return v, nil
}

// allDigits reports a non-empty run of digits — no sign, no separator, no space.
func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// NewRenderer parses all embedded templates, plus any app-module template
// filesystems. A app's partials (files under templates/ whose name starts
// with "_") are merged into the shared base set; its pages are cloned from that
// base, so a app page renders through the same layout, partials, and helpers
// as a core page.
// identity is the kernel this UI is for: the money unit of its world, and the
// name that decides which action addresses are local. The renderer is built
// before the server, so the source arrives here rather than being set
// afterwards.
func NewRenderer(identity *KernelIdentity, appTemplates ...fs.FS) (*Renderer, error) {
	r := &Renderer{identity: identity}
	funcs := template.FuncMap{
		"money":        r.money,
		"amountInput":  r.amountInput,
		"amountStep":   r.amountStep,
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
		"kernelName":   keyDisplay,
		"obligation":   obligation,
		"declineForm":  declineForm,
		"ref":          r.refOf,
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
		"authPanel":    newAuthPanel,
		"httpMethods":  func() []string { return []string{"POST", "GET", "PUT", "PATCH", "DELETE"} },
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

// noAmount is what a money cell renders when the kernel has not said what its
// money is. An em dash claims nothing; a number would claim a unit (§7).
const noAmount = "—"

// money formats a base-unit amount — the single source of truth for every amount
// the UI shows (§7) — or refuses when the unit is unknown.
func (r *Renderer) money(n int64) string {
	u, ok := r.identity.Unit()
	if !ok {
		return noAmount
	}
	return u.Format(n)
}

// amountInput formats a base-unit amount as an HTML number input's value; empty
// when the unit is unknown, so the field offers no figure to submit.
func (r *Renderer) amountInput(n int64) string {
	u, ok := r.identity.Unit()
	if !ok {
		return ""
	}
	return u.Input(n)
}

// amountStep is the step attribute for an amount input.
func (r *Renderer) amountStep() string {
	u, ok := r.identity.Unit()
	if !ok {
		return "any"
	}
	return u.Step()
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
	case "success", "done":
		return "status-credited"
	case "failure", "cancelled":
		return "status-refunded"
	case "open", "waiting", "running":
		return "status-escrow"
	default:
		return ""
	}
}

// costOf extracts a success transaction's gross/fee/net for the breakdown
// partial, carrying whether the call crossed a kernel boundary — where those
// three numbers mean something else (§6.5).
func costOf(tx client.Transaction) Cost {
	return Cost{Gross: tx.Gross, Fee: tx.Fee, Net: tx.Net, Remote: tx.IsRemote(), Ticket: tx.TicketID}
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

// taskDecline is what a Decline confirm posts: the task and the CSRF field, so
// _task_decline renders from one value wherever a task is shown.
type taskDecline struct {
	ID        string
	CSRFField template.HTML
}

// declineForm builds that pairing (template helper).
func declineForm(csrf template.HTML, id string) taskDecline {
	return taskDecline{ID: id, CSRFField: csrf}
}

func nameOf(petname, key string) Name {
	if petname != "" {
		return Name{Text: petname, Title: key}
	}
	return keyDisplay(key)
}

// refOwner returns the owner of an action address, owner@kernel/name: the
// segment before the first slash — names may contain slashes (sys@k/llm/chat),
// but an owner's address never does, so the first slash is unambiguous
// (requirements.md §4). The owner is emphasized in the UI; a LOCAL owner links
// to its owner page, a remote one renders unlinked.
func refOwner(ref string) string {
	if i := strings.IndexByte(ref, '/'); i >= 0 {
		return ref[:i]
	}
	return ref
}

// RefView is a rendered action reference: the owner segment, the name half, and
// the id the name links to when one exists. THE one place a ref is split, so a
// key never renders whole in one template and truncated in another.
//
// Owner is a Name because a remote owner's kernel segment may be a raw public
// key (owner@<43 chars>) — unreadable as a card title, and truncated here
// through the same primitive that renders a kernel card (§13 petname model).
// Local reports that the owner has a profile page here: its kernel segment is
// this kernel's own name (KernelIdentity.Local).
type RefView struct {
	Owner Name
	Name  string
	ID    string // "" ⇒ the name is not a link
	Local bool
}

// refOf splits an action address for rendering. Split on the FIRST slash:
// names may contain slashes (sys@k/llm/chat) but an owner never does.
func (r *Renderer) refOf(ref, id string) RefView {
	owner := refOwner(ref)
	v := RefView{Name: refName(ref), ID: id, Local: r.identity.Local(ref)}
	at := strings.IndexByte(owner, '@')
	if v.Local || at < 0 {
		v.Owner = Name{Text: owner, Title: owner}
		return v
	}
	// A remote kernel segment is truncated only when it is a raw key, so the
	// owner stays readable and the whole segment survives in the title.
	k := keyDisplay(owner[at+1:])
	v.Owner = Name{Text: owner[:at+1] + k.Text, Title: owner, IsKey: k.IsKey}
	return v
}

// refName returns the name segment of an action address (everything after the
// first slash), or "" if there is no slash.
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

// txRow pairs a transaction with the viewer's address, so one partial
// (_tx_row) renders every transaction list identically. Built in-template via
// the `txrow` helper; the action's address is the transaction's own.
type txRow struct {
	Tx     client.Transaction
	Viewer string
}

func txRowOf(tx client.Transaction, viewer string) txRow { return txRow{Tx: tx, Viewer: viewer} }

// entryRow bundles a ledger entry with the viewer's perspective, so the
// activity list renders a movement without the template deciding sign or party.
type entryRow struct {
	Entry       client.LedgerEntry
	Credited    bool   // the amount arrived in the viewer's balance
	Counterpart string // the other side's address; "" where the money crossed the rail
	// TxID is the reason when it names a transaction of the viewer's, the link
	// to its receipt; Note is any other reason (an obligation's ticket, the
	// other kernel's transaction behind a transfer received), rendered as the
	// text it is.
	TxID string
	Note string
}

// idShape is the kernel's resource-id production (kernel §D15): a hex UUID.
var idShape = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// transferReason is the reason a transfer to or from another kernel is
// journalled under: the SENDER's transaction (4dbfedd). It is the viewer's own
// only on the sending side, where the entry debits them.
var transferReason = regexp.MustCompile(`^transfer ([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$`)

// entryRowOf reads the movement from the kernel's own from/to projection: the
// viewer is on one side, and the counterpart is the other.
func entryRowOf(e client.LedgerEntry, viewer string) entryRow {
	r := entryRow{Entry: e, Credited: e.To == viewer, Counterpart: e.To}
	if r.Credited {
		r.Counterpart = e.From
	}
	switch m := transferReason.FindStringSubmatch(e.Reason); {
	case idShape.MatchString(e.Reason):
		r.TxID = e.Reason
	case m != nil && !r.Credited:
		r.TxID = m[1]
	default:
		r.Note = e.Reason
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
		if viewer == tx.Owner {
			return r.sigAmount("sig-refunded", "↩ ", tx.Gross)
		}
		return amountDash
	case "open":
		if viewer == tx.Owner {
			return r.sigAmount("sig-escrow", "◷ ", tx.Gross)
		}
		return amountDash
	default: // success / settled
		var amt int64
		if viewer == tx.Owner {
			amt -= tx.Gross
		}
		if viewer == tx.Target {
			amt += tx.Net
		}
		if viewer == r.identity.Address(superuserHandle) {
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
