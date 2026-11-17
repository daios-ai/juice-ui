package server

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

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
	Theme        string        // theme slug, or "" to defer to prefers-color-scheme
	Sidebar      string        // "collapsed" | "" (expanded); stamped to avoid flash
	Commands     []Command     // the registry: the avatar menu's destinations and the launcher's entries
	Rail         template.HTML // the conversations rail, the sidebar's content
	Me           string        // signed-in address for the nav, or "" when anonymous
	KernelHandle string        // this kernel's own name (from /health), or "" if unknown
	KernelKey    string        // this kernel's public key, shown shortened with its copy for pre-auth verification (login only)
	KernelNet    string        // the network this kernel serves, named on the login page
	KernelName   string        // the name this kernel is recorded under, when the UI names one
	IsSuperuser  bool          // Me == sys@<kernel>: show the admin group and admin scope
	Available    int64         // signed-in user's available balance; 0 when unknown
	Themes       []ANSIPalette // selectable terminal themes for the picker
	CSRFField    template.HTML // hidden CSRF input for no-JS forms
	CSRFToken    string        // raw token for the <meta> tag htmx reads
	Launcher     template.JS   // the registry as the launcher reads it (inert JSON)
	Flash        string        // transient user message
	FlashKind    string        // "success" | "error" | "info" | ""
	BadgeSecs    int           // the heartbeat's slow lane, in seconds (badges, receipts, peers)
	Path         string        // current path, for links that must return to it
	DrawerOpen   bool          // ?drawer=open: the narrow screen's drawer, open (no-JS path)
	Update       *updateView   // the superuser's update notice; nil for anyone else
	Data         any           // page-specific view model
}

// Renderer holds parsed templates. Each page is parsed together with the layout
// and shared partials; partials double as htmx swap fragments (R2.6).
type Renderer struct {
	pages    map[string]*template.Template
	partials *template.Template
	identity *KernelIdentity // the kernel's money unit and name, given at construction
	claims   claims          // the nicknames seen, and those two keys claim
}

// Unit is the money unit of the world this kernel serves: how many decimal
// places its base-unit integer carries, and the symbol that names it. One kernel
// serves one world for life (kernel §D23), so this is fixed per installation.
type Unit struct {
	Decimals int
	Symbol   string
}

// Format renders a base-unit integer exactly, the way the kernel renders it,
// symbol and all: "133 credits" on a world with no fractional unit, "1.50 USDC"
// on one with six. Trailing zeros are trimmed to a two-digit floor, every other
// digit is kept, and there are NO separators, so the text pastes back into an
// amount field unchanged.
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

// Unasked reports whether a run priced price may be made without asking: a
// price under one hundredth of the world's token, a cent where the token is a
// dollar, is minuscule (§8.2). The line is the world's decimals, never an
// amount written down: a price is 100 times under 10^decimals.
func (u Unit) Unasked(price int64) bool {
	if price < 0 || price > math.MaxInt64/100 {
		return false
	}
	line := int64(1)
	for i := 0; i < u.Decimals; i++ {
		if line > math.MaxInt64/10 {
			return true
		}
		line *= 10
	}
	return price*100 < line
}

// Round renders v for reading: two decimals at one whole unit or more, two
// significant digits below it, rounded half away from zero. A world with two
// decimals or fewer has nothing to round. Only the text is rounded: no rounded
// value is ever parsed or sent.
func (u Unit) Round(v int64) string {
	if u.Decimals <= 2 {
		return u.Format(v)
	}
	abs := v
	if abs < 0 {
		abs = -abs
	}
	drop := u.Decimals - 2
	if n := len(strconv.FormatInt(abs, 10)); n <= u.Decimals {
		drop = max(n-2, 0)
	}
	p := int64(1)
	for range drop {
		p *= 10
	}
	if abs < 0 || abs > math.MaxInt64-p/2 {
		return u.Format(v) // too large to round without overflowing
	}
	rounded := (abs + p/2) / p * p
	if v < 0 {
		rounded = -rounded
	}
	return u.Format(rounded)
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
		"money":          r.money,
		"moneyExact":     r.moneyExact,
		"amountInput":    r.amountInput,
		"amountStep":     r.amountStep,
		"sum":            func(a, b int64) int64 { return a + b },
		"sub":            func(a, b int64) int64 { return a - b },
		"json":           prettyJSON,
		"jsonEdit":       jsonEdit,
		"view":           View,
		"valueBlock":     valueBlock,
		"prose":          prose,
		"download":       resultDownload,
		"statusClass":    statusClass,
		"truncate":       truncate,
		"shortAddr":      shortAddr,
		"costOf":         costOf,
		"buildForm":      BuildForm,     // lets templates derive a FormModel from a raw schema
		"taskForm":       BuildTaskForm, // a form with sealed arguments: the schema and its sealed part
		"pct":            pct,
		"ratingPct":      ratingPct,
		"refFolder":      refFolder,
		"name":           r.name,
		"brief":          r.brief,
		"record":         r.record,
		"subject":        subject,
		"plain":          plain,
		"actionHref":     actionHref,
		"kernelParty":    kernelParty,
		"copyOf":         copyOf,
		"progress":       func(id, label string) progressView { return progressView{ID: id, Label: label} },
		"idle":           func(csrf template.HTML) idleView { return idleView{CSRFField: csrf} },
		"actionEvidence": actionEvidence,
		"peerEvidence":   peerEvidence,
		"endOf":          endOf,
		"obligation":     obligation,
		"declineForm":    declineForm,
		"price":          priceOf,
		"shortTime":      shortTime,
		"shortDate":      shortDate,
		"relTime":        relTime,
		"txAmount":       r.txAmount,
		"txrow":          txRowOf,
		"kindBadge":      kindBadge,
		"tab":            tab,
		"icontab":        icontab,
		"paneltab":       paneltab,
		"runForm":        r.runForm,
		"resultText":     resultText,
		"completeForm":   completeForm,
		"statStrip":      statStrip,
		"figure":         figure,
		"lockedFigure":   lockedFigure,
		"figures":        figures,
		"cardArt":        CardArt,
		"initial":        initial,
		"launcher":       launcherOf,
		"balance":        func(amount int64) balanceView { return balanceView{Amount: amount} },
		"authPanel":      newAuthPanel,
		"httpMethods":    func() []string { return []string{"POST", "GET", "PUT", "PATCH", "DELETE"} },
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

// resultText is a result as a message carries it: a string as itself, any
// other value as its JSON: the draft a success from a room becomes (§6.7).
func resultText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return indentJSON(raw, "")
}

// balanceView feeds the _balance partial: the amount, and whether it is the
// heartbeat's out-of-band answer.
type balanceView struct {
	Amount int64
	OOB    bool
}

// launcherView feeds the _launcher partial: the registry, the room's members
// and the room, as one inert JSON block the launcher reads.
type launcherView struct{ JSON template.JS }

// launcherOf builds the launcher of a composer: the pages it lists beside the
// catalog's actions, the members a leading address completes, and the room an
// action's panel opens for.
func launcherOf(pages template.JS, members []string, room string) launcherView {
	if pages == "" {
		pages = "[]"
	}
	b, _ := json.Marshal(struct {
		Pages   json.RawMessage `json:"pages"`
		Members []string        `json:"members"`
		Room    string          `json:"room"`
	}{json.RawMessage(pages), members, room})
	return launcherView{JSON: template.JS(b)}
}

// noAmount is what a money cell renders when the kernel has not said what its
// money is. An em dash claims nothing; a number would claim a unit (§7).
const noAmount = "—"

// money renders an amount for reading (§7): rounded, with the exact amount as its
// title wherever rounding changed it, or a refusal when the unit is unknown.
func (r *Renderer) money(n int64) template.HTML {
	u, ok := r.identity.Unit()
	if !ok {
		return noAmount
	}
	exact, shown := u.Format(n), u.Round(n)
	if shown == exact {
		return template.HTML(template.HTMLEscapeString(shown))
	}
	return template.HTML(`<span title="` + template.HTMLEscapeString(exact) + `">` + template.HTMLEscapeString(shown) + `</span>`)
}

// moneyExact renders the exact amount (§7): what a control commits and what a
// record states, or a refusal when the unit is unknown.
func (r *Renderer) moneyExact(n int64) string {
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
	h, err := r.HTML(name, data)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, err = io.WriteString(w, string(h))
	return err
}

// HTML renders a single named partial to markup, for a heartbeat answer.
func (r *Renderer) HTML(name string, data any) (template.HTML, error) {
	var buf bytes.Buffer
	if err := r.partials.ExecuteTemplate(&buf, name, data); err != nil {
		return "", fmt.Errorf("render fragment %s: %w", name, err)
	}
	return template.HTML(buf.String()), nil
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

// ValueBlock drives _value_block: a value as its view, its raw JSON
// behind the disclosure, and the download route when it is a transaction's
// result.
type ValueBlock struct {
	Root     Field
	Raw      json.RawMessage
	Download string
}

// valueBlock views a value, and a string holding a JSON object or array (a
// chat message, an agent's reply) as that document. The JSON behind the switch
// stays the value as stored.
func valueBlock(raw json.RawMessage, download string) ValueBlock {
	doc := raw
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if d, ok := textDocument(s); ok {
			doc = d
		}
	}
	return ValueBlock{Root: View(doc), Raw: raw, Download: download}
}

// textDocument is the JSON object or array a text holds, if it holds one.
func textDocument(s string) (json.RawMessage, bool) {
	t := strings.TrimSpace(s)
	if t != "" && (t[0] == '{' || t[0] == '[') && json.Valid([]byte(t)) {
		return json.RawMessage(t), true
	}
	return nil, false
}

// resultDownload is the download route of a transaction's result, or "" when
// the outcome names no transaction here (a task completed on another kernel).
func resultDownload(txID string) string {
	if txID == "" {
		return ""
	}
	return "/transactions/" + txID + "/result.json"
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

// shortAddr abbreviates a chain address the way wallets do, 0x7099…79c8: enough
// to match by eye, the whole value in its title and its copy control (_addr). A
// Juice address naming its kernel by key shows the key as client.ShortKey does;
// anything else passes unchanged.
func shortAddr(a string) string {
	if len(a) != 42 || !strings.HasPrefix(a, "0x") {
		return client.ShortAddress(a)
	}
	return a[:6] + "…" + a[38:]
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

// Name is an address as a page draws it (_name, ui-grammar "Names"): a user,
// a kernel or an action, by one component wherever it stands as a value. It
// holds the label's parts (client.Party.Parts), where each part links, the
// whole address when the label differs, the key a nickname stands in for, and
// the address to copy.
//
// It exists because the kernel's naming is Stiegler's petname system (§13) and
// the three name classes are NOT interchangeable: a petname is bound here and
// resolves, so it is shown alone; a nickname is the kernel's claim about
// itself and never resolves, so it is shown marked ~ with its key beside it
// where there is room; a key is self-certifying but unreadable, so it is shown
// shortened, with the whole value in the title and on the copy control.
type Name struct {
	Handle, Kernel, Path string // the label's parts; Handle and Path "" for a kernel
	Owner                string // the owner's actions (/browse?owner=), "" for a kernel or where the place links elsewhere
	Action               string // the action's page (actionHref), "" without a path or where the place is that page
	Title                string // the whole address, when the label differs from it
	Key                  string // the shortened key the kernel's nickname stands in for, where it is shown
	Copy                 string // the whole address to copy, when no petname names its kernel here
}

// claims remembers, per nickname, the first key seen claiming it and whether a
// second key has claimed it too. A compact label names the key only for a
// nickname so claimed (Renderer.brief): a template sees one party at a time,
// so the clash is remembered rather than found among a page's names.
type claims struct {
	mu    sync.Mutex
	first map[string]string
	clash map[string]bool
}

// claim records that key calls itself nickname and reports whether another key
// has claimed it.
func (c *claims) claim(nickname, key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.first == nil {
		c.first, c.clash = map[string]string{}, map[string]bool{}
	}
	if k, ok := c.first[nickname]; !ok {
		c.first[nickname] = key
	} else if k != key {
		c.clash[nickname] = true
	}
	return c.clash[nickname]
}

// copyView is what the _copy partial renders: the whole value a copy control
// puts on the clipboard, and the word its label names it by ("key", "hash",
// "address"). The control exists because a truncated key or hash serves only
// matching by eye, while every command that takes one — `admin peer inspect
// <key>`, `admin user deposit --ref <hash>`, a run of owner@<key>/name — takes
// it whole, so the page must yield the whole value somewhere.
type copyView struct {
	What  string
	Value string
}

// copyOf builds that pairing (template helper).
func copyOf(what, value string) copyView { return copyView{What: what, Value: value} }

// endView is what the one End control renders from: the session, and the CSRF
// field its form posts with.
type endView struct {
	P    client.Process
	CSRF template.HTML
}

// endOf builds that pairing (template helper).
func endOf(p client.Process, csrf template.HTML) endView { return endView{P: p, CSRF: csrf} }

// obligationRow pairs one debt with the CSRF field its confirm needs, so
// _obligation_row renders from one value.
type obligationRow struct {
	O         client.Obligation
	CSRFField template.HTML
}

// obligation builds that pairing (template helper).
func obligation(o client.Obligation, csrfField template.HTML) obligationRow {
	return obligationRow{O: o, CSRFField: csrfField}
}

// taskDecline is what a Decline confirm posts: the task and the CSRF field, so
// _task_decline renders from one value wherever a task is shown.
type taskDecline struct {
	ID        string
	Verb      string // Decline for the one asked, Cancel for the one who asked
	CSRFField template.HTML
}

// declineForm builds that pairing (template helper).
func declineForm(csrf template.HTML, id, verb string) taskDecline {
	return taskDecline{ID: id, Verb: verb, CSRFField: csrf}
}

// nameOf is THE builder of a Name: roomy (tables, detail pages, dialogs,
// cards) shows the key a nickname stands in for; compact (an author line, a
// reference, a rail row) shows it only once two keys have claimed that
// nickname. The owner links to its actions and the path to its page, both by
// the party's key-spelled address — only when it carries a principal, which
// names it on every kernel: a party without one is spelled as another kernel
// wrote it, whose petnames may name another kernel here, so it links nowhere.
// id, set only on a record of a past call, lets that page be read by id when
// its address does not resolve (actionHref).
func (r *Renderer) nameOf(p client.Party, roomy bool, id string) Name {
	n := Name{}
	n.Handle, n.Kernel, n.Path = p.Parts()
	if p.Label() != p.Address {
		n.Title = p.Address
	}
	if client.ShortAddress(p.Address) != p.Address {
		n.Copy = p.Address
	}
	if key := p.Claim(); key != "" {
		if clash := r.claims.claim(p.Nickname, key); roomy || clash {
			n.Key = client.ShortKey(key)
		}
	}
	if p.Principal == "" {
		return n
	}
	if n.Handle != "" {
		n.Owner = "/browse?owner=" + url.QueryEscape(p.Owner().Keyed())
	}
	if n.Path != "" {
		n.Action = actionHref(p, id)
	}
	return n
}

// name is a party as a roomy place draws it (template helper).
func (r *Renderer) name(p client.Party) Name { return r.nameOf(p, true, "") }

// brief is a party as a compact place draws it (template helper).
func (r *Renderer) brief(p client.Party) Name { return r.nameOf(p, false, "") }

// record is the action a record of a past call names — a receipt, a ledger
// row — whose page that record can still open by id (template helper).
func (r *Renderer) record(p client.Party, id string) Name { return r.nameOf(p, false, id) }

// subject is a name where the action itself is presented, under its title on
// its row, its page or its panel: the title is the link, so the path is not
// (template helper).
func subject(n Name) Name {
	n.Action = ""
	return n
}

// plain is a name inside a link or a heading naming that entity: no link and no
// copy control, neither of which may stand inside a link (template helper).
func plain(n Name) Name {
	n.Owner, n.Action, n.Copy = "", "", ""
	return n
}

// Label is a compact name as one line of text, for a sentence an app builds in
// Go (apps.Deps.Label): the label, and the key once two keys claim its nickname.
func (r *Renderer) Label(p client.Party) string {
	if n := r.brief(p); n.Key != "" {
		return p.Label() + " " + n.Key
	}
	return p.Label()
}

// actionHref is THE address of an action's page: one route, keyed by the
// action's address with its kernel spelled by key, which no petname changes
// (ui-grammar "Names"), whether the action is this kernel's or another's. id is
// given only by a record of a past call: an inactive copy of another kernel's
// action re-resolves when named, so with that kernel away only its id reads the
// copy held here (kernel D13).
func actionHref(p client.Party, id string) string {
	v := url.Values{"ref": {p.Keyed()}}
	if id != "" {
		v.Set("id", id)
	}
	return "/actions?" + v.Encode()
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
	Icon              string // a sprite id drawn before the label, on a tab that is a place
	Active            bool
}

// StatStrip is an action's record as its figures show it (_stats): success and
// rating as ranges, latency, and the price. Its cells (_stat_success,
// _stat_rating, _stat_latency) are the evidence table's too, so a report reads
// as the record does. One rendering on every list, the action's page and its
// panel.
type StatStrip struct {
	Price PriceView
	S     client.Stats
	// Set on a row of evidence (evidenceOf): the uses the provider's receipts
	// confirm, and the ratings no trade backs, stated in the cells' titles.
	Corroborated, Unverified int64
}

// Success is the share of runs that succeeded, as a range.
func (s StatStrip) Success() Range { return rangeOf(s.S.Successes, s.S.Uses) }

// Rating is the share of ratings that were good, as a range. A rating is 0 or
// 1 and the kernel serves their mean, so the good ones are mean × count.
func (s StatStrip) Rating() Range {
	return rangeOf(int64(math.Round(s.S.RatingEstimate*float64(s.S.RatingCount))), s.S.RatingCount)
}

// Range is a share drawn as a bar (_range): K good outcomes of N, and the
// share that is confirmed either way, in percent. Lo and Hi are the ends of the
// Wilson score interval at 95% confidence, the standard interval for a share
// from few trials: closed in form, inside 0–100 for every record, and the
// whole of 0–100 for none. Below Lo the share is confirmed good, above Hi
// confirmed bad, and between them it is still open — so the bar shows what
// the record has settled, and an action never run is all open.
type Range struct {
	K, N   int64
	Lo, Hi float64
}

// z is the standard normal's 97.5th percentile: a two-sided 95% interval.
const z = 1.959963984540054

func rangeOf(k, n int64) Range {
	if n == 0 {
		return Range{Hi: 100}
	}
	nf, p, z2 := float64(n), float64(k)/float64(n), z*z
	d := 1 + z2/nf
	c := (p + z2/(2*nf)) / d
	h := z / d * math.Sqrt(p*(1-p)/nf+z2/(4*nf*nf))
	lo, hi := c-h, c+h
	// The interval reaches 0 exactly when nothing was good and 1 when all was,
	// which floating point misses by a hair; those ends are set as they are.
	if k == 0 {
		lo = 0
	}
	if k == n {
		hi = 1
	}
	return Range{K: k, N: n, Lo: 100 * lo, Hi: 100 * hi}
}

// Open is the width still undecided; Bad the width confirmed bad.
func (r Range) Open() float64 { return r.Hi - r.Lo }
func (r Range) Bad() float64  { return 100 - r.Hi }

// Count states N in words: "no runs", "1 run", "89 runs".
func (r Range) Count(noun string) string {
	switch r.N {
	case 0:
		return "no " + noun + "s"
	case 1:
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", r.N, noun)
}

// Says is the record in words, the cell's title and what a screen reader
// hears: the share, then the count — "96% success · 89 runs", "90% positive ·
// 10 ratings" — as stores and status pages state a rating; "No runs yet" for
// none. The bar shows what is confirmed; the words say what happened.
func (r Range) Says(noun, label string) string {
	if r.N == 0 {
		return "No " + noun + "s yet"
	}
	return fmt.Sprintf("%.0f%% %s · %s", 100*float64(r.K)/float64(r.N), label, r.Count(noun))
}

// evidenceTable is what _evidence renders: one row per report — one issuer's
// signed account of one action (kernel §D16) — in the stats line's cells, the
// source last. Subjects is set where the rows span a kernel's actions (a
// peer's inspection); on an action every row is about it.
type evidenceTable struct {
	Rows     []evidenceRow
	Subjects bool
}

type evidenceRow struct {
	Subject        *client.Party // the action, where its address is known
	SubjectID      string        // its id, shown where its address is not
	Strip          StatStrip
	Source         client.Party // the issuing kernel, by key
	Provider       bool         // the report is the provider's own
	Contradictions int64
}

// evidenceOf builds the table, nil when there is nothing to show. provider is
// the key whose reports are the provider's own; names maps a subject's id to
// its action, nil on an action. The provider's rows come first, by subject;
// nothing is summed (requirements §6.2, §6.6).
func evidenceOf(rows []client.Evidence, provider string, names map[string]client.Party) *evidenceTable {
	if len(rows) == 0 {
		return nil
	}
	t := &evidenceTable{Subjects: names != nil}
	for _, e := range rows {
		var subject *client.Party
		if a, ok := names[e.SubjectActionID]; ok {
			subject = &a
		}
		t.Rows = append(t.Rows, evidenceRow{
			Subject: subject, SubjectID: e.SubjectActionID,
			Strip: StatStrip{
				S: client.Stats{
					Uses: e.Uses, Successes: e.Successes, Failures: e.Failures,
					RatingCount: e.RatingCount, RatingEstimate: e.RatingMean,
					LatencyEstimate: e.AvgLatencyMs / 1000,
				},
				Corroborated: e.CorroboratedUses, Unverified: e.UnverifiedRating,
			},
			Source:         kernelParty("", "", e.IssuerPublicKey),
			Provider:       e.IssuerPublicKey == provider,
			Contradictions: e.Contradictions,
		})
	}
	slices.SortStableFunc(t.Rows, func(a, b evidenceRow) int {
		if t.Subjects {
			if c := strings.Compare(a.subject(), b.subject()); c != 0 {
				return c
			}
		}
		return boolOrder(!a.Provider) - boolOrder(!b.Provider)
	})
	return t
}

func boolOrder(b bool) int {
	if b {
		return 1
	}
	return 0
}

// subject is what a row's subject reads as, for ordering: its label, else its id.
func (e evidenceRow) subject() string {
	if e.Subject != nil {
		return e.Subject.Label()
	}
	return e.SubjectID
}

// actionEvidence is an action's evidence table: the provider's report, then
// what other kernels observed.
func actionEvidence(a client.Action) *evidenceTable {
	if a.Evidence == nil {
		return nil
	}
	var rows []client.Evidence
	provider := ""
	if p := a.Evidence.ProviderReported; p != nil {
		rows, provider = append(rows, *p), p.IssuerPublicKey
	}
	return evidenceOf(append(rows, a.Evidence.ObservedByOthers...), provider, nil)
}

// peerEvidence is a peer's evidence table: every report on its actions, each
// subject named by its address from the inspection's own list.
func peerEvidence(i client.Inspection) *evidenceTable {
	names := map[string]client.Party{}
	for _, a := range i.Actions {
		names[a.ActionID] = a.Name
	}
	return evidenceOf(i.Evidence, i.PublicKey, names)
}

// progressView is one in-flight bar (_progress): its id, which an
// hx-indicator names, and what it says.
type progressView struct{ ID, Label string }

// idleView is the idle warning (_idle): the dialog, open when the sign-out is
// session.Warning away, and the watch input naming what it shows.
type idleView struct {
	Soon      bool
	CSRFField template.HTML
}

// State is the watch's value: "soon" while the dialog is open.
func (v idleView) State() string {
	if v.Soon {
		return "soon"
	}
	return ""
}

// Figure is one headline money figure, drawn as a block: what it is, the
// amount with its signal (§7) — "" plain, "credited" earned (signed +),
// "escrow" parked. Every page that leads with money renders its figures
// through the one _figures partial.
type Figure struct {
	Amount int64
	Label  string
	Signal string
}

// figure builds a Figure (template helper).
func figure(amount int64, label, signal string) Figure {
	return Figure{Amount: amount, Label: label, Signal: signal}
}

// lockedFigure is the account's served `locked`, whole (§6.5): for everyone it
// is parked — what open sessions, a ticket's stake and waiting transfer tasks
// reserve. The operator's also holds payouts in transit on the rail, sys being
// the clearing account, so it takes the kernel's own word and no escrow signal.
func lockedFigure(amount int64, operator bool) Figure {
	if operator {
		return figure(amount, "locked ◷", "")
	}
	return figure(amount, "parked ◷", "escrow")
}

// figures collects Figures for the _figures partial (template helper).
func figures(f ...Figure) []Figure { return f }

// statStrip builds a StatStrip. It is nil-safe: an action this kernel holds no
// record of (a list row before its detail read, a discovered one) is an action
// never run, which its ranges state as the widest bars.
func statStrip(price PriceView, s *client.Stats) StatStrip {
	var v client.Stats
	if s != nil {
		v = *s
	}
	return StatStrip{Price: price, S: v}
}

// tab builds a plain navigation tab (list filters, scope switch).
func tab(label, href string, active bool) Tab { return Tab{Label: label, Href: href, Active: active} }

// icontab builds a tab that is a place rather than a filter (the inbox's To do,
// Sent, Done and Requesters): the same tab, its icon before the word.
func icontab(label, href, icon string, active bool) Tab {
	return Tab{Label: label, Href: href, Icon: icon, Active: active}
}

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

// fetchRows reads the request's page through read, asking for one row more than a
// page holds to learn whether another follows, since the kernel answers no total.
func fetchRows[T any](r *http.Request, read func(limit, offset int) ([]T, error)) ([]T, bool, error) {
	rows, err := read(pageSize+1, queryOffset(r))
	if err != nil {
		return nil, false, err
	}
	if len(rows) > pageSize {
		return rows[:pageSize], true, nil
	}
	return rows, false, nil
}

// fetchPage is fetchRows for a page's only list, with its pager.
func fetchPage[T any](r *http.Request, read func(limit, offset int) ([]T, error)) ([]T, Pager, error) {
	rows, more, err := fetchRows(r, read)
	return rows, pageNav(r, queryOffset(r), pageSize, more), err
}

// pageRows is one page of a list already in hand, and whether more follow. It
// serves a list the kernel cannot order or filter as the page needs, or does not
// page at all; any other list is read with fetchRows.
func pageRows[T any](r *http.Request, rows []T) ([]T, bool) {
	start, end, more := pageSlice(len(rows), queryOffset(r), pageSize)
	return rows[start:end], more
}

// pageOf is pageRows for a page's only list, with its pager.
func pageOf[T any](r *http.Request, rows []T) ([]T, Pager) {
	page, more := pageRows(r, rows)
	return page, pageNav(r, queryOffset(r), pageSize, more)
}

// pageSlice returns the [start,end) window of n items for the given offset and
// page size, plus whether a further page exists — clamping offset into range.
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

// badgeView is the model for the _badge_poll partial: a nav attention badge that
// htmx re-fetches on a fixed interval. Count is the initial server-rendered value
// (so a no-JS client still sees it); the poll then overwrites it.
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

// shortDate renders an RFC3339 timestamp as its date alone, for a fact whose
// clock time says nothing (when a relationship began).
func shortDate(ts string) string {
	if len(ts) < 10 {
		return ts
	}
	return ts[:10]
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
	Entry    client.LedgerEntry
	Credited bool // the amount arrived in the viewer's balance
	// Self marks an entry from the viewer to the viewer, which moves nothing.
	// Only the operator has them: sys is both an account and the clearing
	// account, so a deposit credited to sys, and the first leg of its own
	// withdrawal (into transit), are sys → sys (§6.5).
	Self        bool
	Counterpart client.Party // the other side; no address where the money crossed the rail
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
	r := entryRow{Entry: e, Self: e.From.Address == viewer && e.To.Address == viewer, Counterpart: e.To}
	r.Credited = e.To.Address == viewer && !r.Self
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
		if viewer == tx.Owner.Address {
			return r.sigAmount("sig-refunded", "↩ ", tx.Gross)
		}
		return amountDash
	case "open":
		if viewer == tx.Owner.Address {
			return r.sigAmount("sig-escrow", "◷ ", tx.Gross)
		}
		return amountDash
	default: // success / settled
		var amt int64
		if viewer == tx.Owner.Address {
			amt -= tx.Gross
		}
		if viewer == tx.Target.Address {
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
	return template.HTML(`<span class="` + cls + `">` + glyph + string(r.money(n)) + `</span>`)
}
