package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"html/template"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/daios-ai/juice-ui/client"
	"github.com/daios-ai/juice-ui/models"
	"github.com/daios-ai/juice-ui/wallet"
	"github.com/gorilla/csrf"
	"golang.org/x/mod/semver"
)

// Admin (superuser) handlers. Every route reaches here only through
// requireSuperuser (sys), and the kernel independently rejects a non-superuser
// bearer with 403 — so a stale UI check can never authorize a real admin action.
// The admin surface covers the operator verbs no ordinary user performs: money
// (deposit/withdraw), access (suspend), and federation trust (peers, settlement).

// depositForm carries the shared deposit dialog's per-page state. Every view
// that renders _form_admin_deposit embeds it: FundRef prefills the payment
// being named, so a transaction hash is never retyped, and Back names the page
// the operator came from.
//
// There is no recipient prefill, because there is no gesture that starts from
// one: a deposit credits a user the operator names, and the only prefilled
// form is attributing a held payment, which starts from the payment.
type depositForm struct {
	FundRef string
	Back    string
}

type adminView struct {
	depositForm
	Users        []client.AdminUser
	Pager        Pager
	OpenForm     string
	Rename       string // the address whose rename dialog is open; "" when none
	Configurable bool   // this installation's kernel: its configuration page exists
	Models       bool   // and its Models page
}

// adminPeersView drives the network page: this kernel's money, the money
// awaiting the operator, and one ranked list of every kernel this one knows —
// itself, then its counterparties (a transaction here names them), then the
// known network (discovered). Deposits is shown above the list because closing
// an obligation is the one money verb left here.
type adminPeersView struct {
	depositForm
	Identity      client.Identity
	Native        string // the fee coin of the proven chain the vault is on; "" if none is proven
	Deposits      client.Deposits
	Petnames      map[string]string // public key → bound petname, for naming a debtor
	Kernels       []kernelRow
	Pager         Pager
	ShowSuspended bool // ?all=1 — the list includes suspended counterparties
	CSRFField     template.HTML
	OpenForm      string
}

// The classes of a kernel row, in list order.
const (
	classCurrent = "current"
	classParty   = "party"
	classKnown   = "known"
)

// kernelRow is one kernel on the network list. Name is what its title shows:
// a peer's petname or truncated key (nameOf), and for this kernel its own
// handle — never stored as a petname, which is a name bound here for another
// kernel, nor as a nickname, which is a peer's unverified claim. Strip is the
// peer's facts in the stat strip's grammar; this kernel's row has none, since
// the roster states nothing about it.
type kernelRow struct {
	client.Peer
	Class string
	Name  Name
	Strip *KernelStrip
}

// KernelStrip is a peer row's facts as a stat strip (§5.5): fixed slots in a
// fixed order so rows compare down the column — the catalog and the contact
// cache, which is all the roster states. A detail page states its facts with
// labels instead (adminInspectView), since it has no column to compare.
type KernelStrip struct {
	Actions int
	Seen    string // last_seen, "" until an exchange has succeeded
	Failed  string // last_contact_failed_at, "" until one has failed
}

func kernelStrip(p client.Peer) *KernelStrip {
	return &KernelStrip{Actions: p.Actions, Seen: p.LastSeen, Failed: p.LastContactFailedAt}
}

// Report is the provider's own evidence pooled over its actions, for the
// kernel page: stated only where it has runs.
type Report struct {
	Uses, Successes, RatingCount int64
	RatingMean                   float64
}

// poolEvidence sums one issuer's rows: successes over uses, and the rating as
// the count-weighted mean of the per-action means, since an unweighted mean of
// means over-weights thinly rated actions. A zero count leaves the mean zero.
func poolEvidence(rows []client.Evidence) Report {
	var r Report
	var weighted float64
	for _, e := range rows {
		r.Uses += e.Uses
		r.Successes += e.Successes
		r.RatingCount += e.RatingCount
		weighted += e.RatingMean * float64(e.RatingCount)
	}
	if r.RatingCount > 0 {
		r.RatingMean = weighted / float64(r.RatingCount)
	}
	return r
}

// petnameSuggestion is the name the rename form opens with: the petname when
// one is bound (a rename), else the kernel's own auto-bind preference (kernel
// §D15) — the cached nickname when it is not key-shaped and free, else
// k-<key8>, either suffixed 2, 3, … to the lowest free number. taken reports a
// name bound to another kernel or held by this one. The kernel remains the
// validator.
func petnameSuggestion(petname, nickname, key string, taken func(string) bool) string {
	if petname != "" {
		return petname
	}
	base := nickname
	if base == "" || looksLikeKey(base) {
		base = "k-" + key[:min(keyLen, len(key))]
	}
	if !taken(base) {
		return base
	}
	for n := 2; ; n++ {
		if s := fmt.Sprintf("%s%d", base, n); !taken(s) {
			return s
		}
	}
}

// adminInspectView carries the inspected kernel. A peer holds no account here:
// its moderation and its start are facts of the kernel row (Record), read for
// every key, and its class (Traded) is the roster's. Suggestion is the rename
// form's prefill, computed only when that form opens, because it needs the
// roster.
type adminInspectView struct {
	Key        string
	Inspection client.Inspection
	Report     Report            // the provider's own evidence, pooled
	Record     client.PeerRecord // this kernel's row for the peer: first seen, suspension
	Traded     bool              // a transaction here names it (the roster's class)
	Actions    []actionCardView  // the peer's public actions, as discovered rows
	Catalog    int               // the roster's count of its public actions; 0 when unknown here
	Version    string            // the peer's kernel version, without the agent prefix
	Suggestion string
	OpenForm   string
	CSRFField  template.HTML
}

// peerActionRows renders a peer's public actions as discovered rows: each is a
// remote action before resolution, which is exactly what the lookup results
// show (§6.2) — its link resolves it from its home kernel.
func peerActionRows(i client.Inspection) []actionCardView {
	rows := make([]actionCardView, 0, len(i.Actions))
	for _, a := range i.Actions {
		rows = append(rows, actionCardView{Discovered: true, Action: client.Action{
			ID: a.ActionID, Action: a.Name, Title: a.Title, Description: a.Description, Price: a.Price, Active: true, Discovered: true,
		}})
	}
	return rows
}

func (s *Server) getAdmin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	// Back drives the shared deposit dialog's close/cancel links; empty would make
	// them reload this URL with ?form= still set, reopening the dialog on the
	// no-JS path.
	view := adminView{OpenForm: openForm(r, "admin"), depositForm: depositForm{Back: "/admin"}, Configurable: s.config != nil, Models: s.modelsSupported()}
	if view.OpenForm == "admin-rename" {
		view.Rename = strings.TrimSpace(r.URL.Query().Get("address"))
	}
	err := s.sessions.Do(ctx, func(tok string) error {
		u, e := s.client.AdminListUsers(ctx, tok)
		if e != nil {
			return e
		}
		view.Users = u
		return nil
	})
	if s.handleErr(w, r, err) {
		return
	}
	// Active users first, then alphabetical by address within each block, so the
	// operator sees the live roster before the suspended tail.
	sort.SliceStable(view.Users, func(i, j int) bool {
		a, b := view.Users[i], view.Users[j]
		if a.Suspended() != b.Suspended() {
			return !a.Suspended() // active (not suspended) sorts first
		}
		return strings.ToLower(a.Address) < strings.ToLower(b.Address)
	})
	view.Users, view.Pager = pageOf(r, view.Users)
	s.renderPage(w, r, http.StatusOK, "admin", "Admin", view)
}

// postAdminUserOp addresss the per-user operator verbs, dispatched on the path's
// last segment. The switch is exhaustive on purpose: an unrecognised segment is
// a 404, never a fallthrough into a destructive verb.
func (s *Server) postAdminUserOp(w http.ResponseWriter, r *http.Request) {
	address := r.PathValue("address")
	ctx := r.Context()
	switch lastSegment(r.URL.Path) {
	case "suspend":
		err := s.sessions.Do(ctx, func(tok string) error { return s.client.AdminSuspendUser(ctx, tok, address) })
		s.flashResult(w, r, err, "/admin", "User "+address+" suspended.")
	case "unsuspend":
		err := s.sessions.Do(ctx, func(tok string) error { return s.client.AdminUnsuspendUser(ctx, tok, address) })
		s.flashResult(w, r, err, "/admin", "User "+address+" unsuspended.")
	case "rename":
		s.renameUser(w, r, address)
	default:
		s.renderPage(w, r, http.StatusNotFound, "error", "Not found", errorView{Message: "Page not found."})
	}
}

// renameUser rebinds a local account's address, freeing the old one so every
// reference follows the account. The new address is taken verbatim — one the
// kernel will not accept is the kernel's to refuse, not ours to rewrite
// (requirements.md §4). Both outcomes land on Admin, whose users table is
// where an account stands.
//
// A kernel is renamed on its own page by renamePeer: the two namespaces are
// separate routes on the kernel now, and a route that guessed which one was
// meant would be guessing what the kernel refuses to guess (§6.6).
func (s *Server) renameUser(w http.ResponseWriter, r *http.Request, address string) {
	ctx := r.Context()
	newName := strings.TrimSpace(r.FormValue("new_name"))
	if newName == "" {
		s.flashRedirect(w, r, "/admin", "error", "Enter a new address.")
		return
	}
	if err := s.sessions.Do(ctx, func(tok string) error { return s.client.AdminRenameUser(ctx, tok, address, newName) }); err != nil {
		s.flashRedirect(w, r, "/admin", "error", userMessage(err))
		return
	}
	s.flashRedirect(w, r, "/admin", "success", address+" is now "+newName+".")
}

// postAdminPeerRename binds a kernel's petname, keyed on the public key — an
// unbound kernel has no name to key on, and advertised names collide across
// kernels (§6.6). The redirect returns to the key, which is the address the
// page keeps whatever it is called.
func (s *Server) postAdminPeerRename(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	key := strings.TrimSpace(r.FormValue("key"))
	back := "/admin/peers/" + url.PathEscape(key)
	newName := strings.TrimSpace(r.FormValue("new_name"))
	if key == "" || newName == "" {
		s.flashRedirect(w, r, back, "error", "Enter a new name.")
		return
	}
	if err := s.sessions.Do(ctx, func(tok string) error { return s.client.AdminRenamePeer(ctx, tok, key, newName) }); err != nil {
		s.flashRedirect(w, r, back, "error", userMessage(err))
		return
	}
	s.flashRedirect(w, r, back, "success", "Named "+newName+".")
}

// adjustBack resolves the page a deposit/withdraw returns to. The set is closed:
// a form field must never be able to name an arbitrary redirect target, and the
// only two pages that host the form are the admin roster and the network page —
// funding from a kernel's inspect page lands on the roster, where the promoted
// row now appears.
func adjustBack(v string) string {
	if v == "/admin/peers" {
		return v
	}
	return "/admin"
}

// postAdminDeposit records a payment made outside the system. Ref is mandatory:
// the kernel credits nothing it cannot name, and the same fact never moves money
// twice (§6.6). Amount is optional — omitted, it attributes a payment already
// received, or closes a peer's obligation for the amount its draw decided.
func (s *Server) postAdminDeposit(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	address := strings.TrimSpace(r.FormValue("address"))
	ref := strings.TrimSpace(r.FormValue("ref"))
	reason := strings.TrimSpace(r.FormValue("reason"))
	back := adjustBack(strings.TrimSpace(r.FormValue("back")))
	amount, err := s.parseAmount(r.FormValue("amount"))
	if err != nil {
		s.flashRedirect(w, r, back, "error", capitalize(err.Error())+".")
		return
	}
	if address == "" || ref == "" {
		s.flashRedirect(w, r, back, "error", "An account and the payment it records are both required.")
		return
	}
	err = s.sessions.Do(ctx, func(tok string) error {
		_, e := s.client.AdminDeposit(ctx, tok, address, amount, ref, reason)
		return e
	})
	msg := "Recorded " + ref + " · " + address + "."
	if amount > 0 {
		msg = "Recorded " + s.render.moneyExact(amount) + " · " + address + "."
	}
	s.flashResult(w, r, err, back, msg)
}

// getAdminPeers renders the merged roster. The kernel returns counterparties and
// discovered kernels in one list keyed by public key, so the tab partitions it
// here and the page is sliced locally — the kernel pages before this split, so
// asking it for one page of one class would drop rows.
//
// It is deliberately not polled: its facts advance once per the kernel's
// discovery interval, while the cost of a refresh now scales with the size of
// the known network.
func (s *Server) getAdminPeers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	showSuspended := r.URL.Query().Get("all") == "1"
	view := adminPeersView{
		OpenForm: openForm(r, "admin"), ShowSuspended: showSuspended,
		CSRFField: csrf.TemplateField(r),
		depositForm: depositForm{
			// A held payment is attributed from its own row, so the reference it
			// is named by rides into the form rather than being retyped.
			FundRef: r.URL.Query().Get("ref"),
			Back:    "/admin/peers",
		},
	}
	view.Petnames = map[string]string{}
	if chain, ok := wallet.ChainOf(s.kernelIdentity(ctx)); ok {
		view.Native = wallet.Native(chain)
	}
	err := s.sessions.Do(ctx, func(tok string) error {
		id, e := s.client.AdminIdentity(ctx, tok)
		if e != nil {
			return e
		}
		view.Identity = id
		// The money awaiting the operator: an obligation is closed here, and a
		// held payment is attributed here (§6.6).
		if d, e := s.client.AdminListDeposits(ctx, tok); e == nil {
			view.Deposits = d
		}
		// all=showSuspended also lists suspended counterparties.
		p, e := s.client.AdminListPeers(ctx, tok, showSuspended)
		if e != nil {
			return e
		}
		view.Kernels = kernelRows(id, p)
		for _, peer := range p {
			if peer.Petname != "" {
				view.Petnames[peer.PublicKey] = peer.Petname
			}
		}
		return nil
	})
	if s.handleErr(w, r, err) {
		return
	}
	var owedMore, heldMore, kernelsMore bool
	view.Deposits.Owed, owedMore = pageRows(r, view.Deposits.Owed)
	view.Deposits.Held, heldMore = pageRows(r, view.Deposits.Held)
	view.Kernels, kernelsMore = pageRows(r, view.Kernels)
	view.Pager = pageNav(r, queryOffset(r), pageSize, owedMore || heldMore || kernelsMore)
	s.renderPage(w, r, http.StatusOK, "admin_peers", "Peers", view)
}

// kernelRows lists this kernel, then its counterparties, then the known
// network, each class in the kernel's own order (§6.6).
func kernelRows(id client.Identity, peers []client.Peer) []kernelRow {
	rows := []kernelRow{{
		Peer:  client.Peer{PublicKey: id.PublicKey, About: id.About},
		Class: classCurrent,
		Name:  Name{Text: id.Handle, Title: id.PublicKey},
	}}
	for _, class := range []string{classParty, classKnown} {
		for _, p := range peers {
			if p.Traded == (class == classParty) {
				rows = append(rows, kernelRow{Peer: p, Class: class, Name: nameOf(p.Petname, p.PublicKey), Strip: kernelStrip(p)})
			}
		}
	}
	return rows
}

// getAdminInspect renders a remote kernel's identity, reachability, the public
// actions it serves, and the evidence retained about it (admin inspect <key>).
// It is the only window onto a kernel this one has not transacted with, since
// the kernel serves no known-network roster.
func (s *Server) getAdminInspect(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	ctx := r.Context()
	view, err := s.inspectView(ctx, key, openForm(r, "admin-peer"))
	view.CSRFField = csrf.TemplateField(r)
	if s.handleErrFragment(w, r, err) {
		return
	}
	s.renderPage(w, r, http.StatusOK, "admin_inspect", "Inspect", view)
}

// getAdminInspectByKey turns a typed public key into the canonical inspect URL.
// Inspecting is a pure read, so this stays a GET: the form names the key, and
// the redirect gives the page one address to bookmark and poll. It is the
// successor entry to the retired subscribe opener — a key learned anywhere (a
// discovered lookup result, an operator) still reaches the inspect view.
func (s *Server) getAdminInspectByKey(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.URL.Query().Get("key"))
	if key == "" {
		s.flashRedirect(w, r, "/admin/peers", "error", "A kernel public key is required.")
		return
	}
	http.Redirect(w, r, "/admin/peers/"+url.PathEscape(key), http.StatusSeeOther)
}

// postAdminPeerSuspend freezes or unfreezes a peer by key. The path's last
// segment selects the verb (suspend/unsuspend), mirroring postAdminUserOp.
func (s *Server) postAdminPeerSuspend(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	key := strings.TrimSpace(r.FormValue("key"))
	if key == "" {
		s.flashRedirect(w, r, "/admin/peers", "error", "A peer public key is required.")
		return
	}
	unsuspend := strings.HasSuffix(r.URL.Path, "/unsuspend")
	err := s.sessions.Do(ctx, func(tok string) error {
		if unsuspend {
			return s.client.AdminUnsuspendPeer(ctx, tok, key)
		}
		return s.client.AdminSuspendPeer(ctx, tok, key)
	})
	verb := "suspended — inbound calls refused"
	if unsuspend {
		verb = "unsuspended"
	}
	s.flashResult(w, r, err, "/admin/peers/"+url.PathEscape(key), "Peer "+verb+".")
}

// Configuration is this installation's kernel configuration file, which the UI
// edits for the operator and applies by restarting the kernel (kernel manual:
// "edit the file and restart the kernel"). Version is the file's fingerprint;
// Reconfigure refuses a file changed since that version.
type Configuration interface {
	Config() (config, schema []byte, version string, err error)
	Reconfigure(version string, config []byte) error
}

// configView is the configuration page (§6.6): the file as a form generated
// from the kernel's own schema of it, its secrets as write-only inputs.
type configView struct {
	Form    FormModel
	Version string
}

// configEditor edits a JSON configuration file through the form of its JSON
// Schema (§6.6): the kernel's config.json and an agent's. A hidden key is not
// a field and is kept as saved, though the JSON view shows it; a fixed key is
// shown read-only and kept as saved; a withheld key is in no view at all and
// kept as saved — the kernel's secret, and the language models, which the
// Models page owns (§6.6). Keys are dotted paths. An app edits its file
// through apps.ConfigEditor, which this satisfies.
type configEditor struct {
	s        *Server
	schema   json.RawMessage
	hidden   []string
	fixed    []string
	withheld []string
}

// editor is the editor of a file without secrets.
func (s *Server) editor(schema json.RawMessage, hidden ...string) configEditor {
	return configEditor{s: s, schema: schema, hidden: hidden}
}

// kernelEditor edits the kernel's file: the schema pointer hidden;
// kernel_handle, the first boot's name, and listen_addr, the port this UI finds
// its kernel by (kernel.World), read-only; credentials_key and the language
// models withheld.
func (s *Server) kernelEditor(schema json.RawMessage) configEditor {
	e := s.editor(schema, "$schema")
	e.fixed = []string{"kernel_handle", "listen_addr"}
	e.withheld = []string{"credentials_key", "native.llm"}
	return e
}

func (s *Server) getAdminConfig(w http.ResponseWriter, r *http.Request) {
	raw, schema, version, err := s.config.Config()
	var form FormModel
	if err == nil {
		form, err = s.kernelEditor(schema).page(raw)
	}
	if s.handleErr(w, r, err) {
		return
	}
	s.renderPage(w, r, http.StatusOK, "admin_config", "Configuration", configView{Form: form, Version: version})
}

// withhold removes the withheld keys from cfg, so no view shows them, and an
// object left empty by that, which the file did not hold empty.
func (c configEditor) withhold(cfg map[string]any) {
	for _, p := range c.withheld {
		if getDotted(cfg, p) == nil {
			continue
		}
		deleteDotted(cfg, p)
		for parent := p; strings.Contains(parent, "."); {
			parent = parent[:strings.LastIndex(parent, ".")]
			if m, ok := getDotted(cfg, parent).(map[string]any); !ok || len(m) > 0 {
				break
			}
			deleteDotted(cfg, parent)
		}
	}
}

// fields drops the fields of hidden and withheld keys, at any depth.
func (c configEditor) fields(fs []Field) []Field {
	out := fs[:0]
	for _, f := range fs {
		key := strings.ReplaceAll(strings.TrimPrefix(f.Path, "/"), "/", ".")
		if slices.Contains(c.hidden, key) || slices.Contains(c.withheld, key) {
			continue
		}
		f.Fields = c.fields(f.Fields)
		out = append(out, f)
	}
	return out
}

// decode reads the file as a JSON object, an empty file being {}.
func (c configEditor) decode(file []byte) (map[string]any, error) {
	if len(bytes.TrimSpace(file)) == 0 {
		return map[string]any{}, nil
	}
	var m map[string]any
	err := decodeNumbers(file, &m)
	return m, err
}

// Form is the page's form over the file; Version is the file's SHA-256, which
// the form carries and a save is refused without.
func (c configEditor) Form(file []byte) (any, error) { return c.page(file) }

func (c configEditor) Version(file []byte) string {
	sum := sha256.Sum256(file)
	return hex.EncodeToString(sum[:])
}

func (c configEditor) page(file []byte) (FormModel, error) {
	saved, err := c.decode(file)
	if err != nil {
		return FormModel{}, err
	}
	form, _ := c.form(saved)
	return form, nil
}

// form is the schema filled from what the page shows of saved: hidden and
// withheld keys left out, fixed keys read-only. The JSON editor opens on the
// shown file itself, never a skeleton: what a save from it omits is removed.
// It returns what Fill could not place exactly.
func (c configEditor) form(saved map[string]any) (FormModel, []string) {
	c.withhold(saved)
	raw, _ := json.MarshalIndent(saved, "", "  ")
	form := BuildForm(c.schema)
	inexact := form.Fill(raw)
	form.Example = "" // never BuildForm's skeleton: an absent file opens on nothing
	if len(saved) > 0 {
		form.Example = string(raw)
	}
	form.Fields = c.fields(form.Fields)
	for i := range form.Fields {
		form.Fields[i].ReadOnly = slices.Contains(c.fixed, form.Fields[i].Name)
	}
	return form, inexact
}

// normal reads to as a save writes it over saved, keeping as saved what the
// form cannot carry: the hidden, fixed and withheld keys, and an empty value a
// blank input cannot tell from absent. A save and both directions of the JSON
// switch share it.
func (c configEditor) normal(saved, to map[string]any) {
	for _, p := range slices.Concat(c.hidden, c.fixed, c.withheld) {
		deleteDotted(to, p)
		if v := getDotted(saved, p); v != nil {
			setDotted(to, p, v)
		}
	}
	keepEmpty(saved, to)
}

// read decodes the file and the editor's args, the latter normalized as a save
// would read it; either empty is {}.
func (c configEditor) read(args, file []byte) (saved, to map[string]any, err error) {
	if saved, err = c.decode(file); err == nil {
		to, err = c.decode(args)
	}
	if err == nil {
		c.normal(saved, to)
	}
	return saved, to, err
}

// Read is what the post would write over the file — the form or the JSON,
// read through normal — and whether it differs from the file.
func (c configEditor) Read(r *http.Request, file []byte) ([]byte, bool, error) {
	args, err := buildArgs(r)
	var saved, to map[string]any
	if err == nil {
		saved, to, err = c.read(args, file)
	}
	if err != nil {
		return nil, false, err
	}
	out, _ := json.MarshalIndent(to, "", "  ")
	return out, !reflect.DeepEqual(saved, to), nil
}

// ToJSON is the JSON switch's form → JSON direction: what a save would write,
// its secrets withheld — the view the page opens on.
func (c configEditor) ToJSON(w http.ResponseWriter, r *http.Request, file []byte) {
	args, err := buildArgs(r)
	var to map[string]any
	if err == nil {
		_, to, err = c.read(args, file)
	}
	if err != nil {
		http.Error(w, capitalize(err.Error())+".", http.StatusUnprocessableEntity)
		return
	}
	c.withhold(to)
	out, _ := json.Marshal(to)
	writeJSONText(w, out)
}

// ToForm is the JSON switch's JSON → form direction. The values a save keeps
// by itself — the file's own empty values, shown blank — are left out of the
// fill, so the form holds the JSON exactly or the switch is refused.
func (c configEditor) ToForm(w http.ResponseWriter, r *http.Request, file []byte) {
	saved, to, err := c.read([]byte(r.PostFormValue("raw_args")), file)
	if err != nil {
		http.Error(w, capitalize(err.Error())+".", http.StatusUnprocessableEntity)
		return
	}
	dropKept(saved, to)
	form, inexact := c.form(to)
	c.s.renderFields(w, form, inexact)
}

// kernelFile is the kernel's file and its editor for the switch's handlers,
// or a 422 naming why there is none.
func (s *Server) kernelFile(w http.ResponseWriter) ([]byte, configEditor, bool) {
	raw, schema, _, err := s.config.Config()
	if err != nil {
		http.Error(w, capitalize(err.Error())+".", http.StatusUnprocessableEntity)
		return nil, configEditor{}, false
	}
	return raw, s.kernelEditor(schema), true
}

func (s *Server) postAdminConfigJSON(w http.ResponseWriter, r *http.Request) {
	if raw, e, ok := s.kernelFile(w); ok {
		e.ToJSON(w, r, raw)
	}
}

func (s *Server) postAdminConfigForm(w http.ResponseWriter, r *http.Request) {
	if raw, e, ok := s.kernelFile(w); ok {
		e.ToForm(w, r, raw)
	}
}

// postAdminConfig writes the form over the file the page was rendered from and
// restarts the kernel on it; the kernel refuses a stale version.
func (s *Server) postAdminConfig(w http.ResponseWriter, r *http.Request) {
	const back = "/admin/config"
	raw, schema, _, err := s.config.Config()
	var out []byte
	var changed bool
	if err == nil {
		out, changed, err = s.kernelEditor(schema).Read(r, raw)
	}
	if err != nil {
		s.flashRedirect(w, r, back, "error", capitalize(err.Error())+".")
		return
	}
	if !changed {
		s.flashRedirect(w, r, back, "info", "Nothing changed.")
		return
	}
	if err := s.config.Reconfigure(r.PostFormValue("version"), append(out, '\n')); err != nil {
		s.flashRedirect(w, r, back, "error", capitalize(err.Error())+".")
		return
	}
	s.flashRedirect(w, r, back, "success", "Saved and restarted.")
}

// modelsView is the Models page (§6.6): the language models over the
// kernel's file, the entries no binding names, and the file's version.
type modelsView struct {
	Form    models.Form
	Others  []models.Entry
	Version string
	Error   string
}

// modelsSupported reports this installation's kernel, when its schema
// describes the model entries the Models page writes; an older kernel's file
// is never written there. Read once: the kernel's schema changes only with
// its binary.
func (s *Server) modelsSupported() bool {
	if s.config == nil {
		return false
	}
	return s.modelsOK()
}

// kernelConfig is the kernel's file, decoded, and its version.
func (s *Server) kernelConfig() (map[string]any, string, error) {
	raw, _, version, err := s.config.Config()
	if err != nil {
		return nil, "", err
	}
	var cfg map[string]any
	if len(bytes.TrimSpace(raw)) == 0 {
		return map[string]any{}, version, nil
	}
	return cfg, version, decodeNumbers(raw, &cfg)
}

func (s *Server) getAdminModels(w http.ResponseWriter, r *http.Request) {
	if !s.modelsSupported() {
		http.NotFound(w, r)
		return
	}
	cfg, version, err := s.kernelConfig()
	if s.handleErr(w, r, err) {
		return
	}
	held := models.From(cfg)
	held.Priced = true
	s.renderModels(w, r, http.StatusOK, cfg, version, held, held, "")
}

// renderModels renders the page for setup, the keys of held said to be held,
// each block naming the native it backs.
func (s *Server) renderModels(w http.ResponseWriter, r *http.Request, status int, cfg map[string]any, version string, setup, held models.Setup, msg string) {
	ctx := r.Context()
	form := models.View(ctx, setup, held)
	form.Refresh(modelsRefresh)
	for _, b := range []*models.Block{&form.Chat, &form.Embed} {
		ref := s.native("llm/" + b.Field)
		_ = s.sessions.Do(ctx, func(tok string) error {
			a, ok, err := s.client.ResolveRef(ctx, tok, ref)
			if err == nil && ok {
				b.Action, b.Href = ref, "/actions/"+url.PathEscape(a.ID)
			}
			return err
		})
	}
	s.renderPage(w, r, status, "admin_models", "Models", modelsView{Form: form, Others: models.Others(cfg), Version: version, Error: msg})
}

// modelsRefresh is where the page asks for a block's model field again.
const modelsRefresh = "/admin/models/model"

// modelsField is a refresh's answer: the form as posted, and the block asked.
type modelsField struct {
	Form  models.Form
	Field string
}

// postAdminModelsModel answers a block's model field for the form as posted —
// the provider, address and key typed — with the models that provider lists.
// It writes nothing.
func (s *Server) postAdminModelsModel(w http.ResponseWriter, r *http.Request) {
	if !s.modelsSupported() {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "The form could not be read.", http.StatusBadRequest)
		return
	}
	cfg, _, err := s.kernelConfig()
	if err != nil {
		http.Error(w, capitalize(err.Error())+".", http.StatusUnprocessableEntity)
		return
	}
	held := models.From(cfg)
	setup := models.Read(r.PostForm)
	setup.Keep(held)
	setup.Priced = true
	form := models.View(r.Context(), setup, held)
	form.Refresh(modelsRefresh)
	_ = s.render.Fragment(w, http.StatusOK, "_models_refresh", modelsField{Form: form, Field: r.PostFormValue("block")})
}

// postAdminModels writes the models chosen into the kernel's file and restarts
// it, the configuration page's save; List models shows the form again with
// what the providers serve, and a setup the kernel could not use is refused
// with what to do. The version is the page's, so a file changed meanwhile is
// refused.
func (s *Server) postAdminModels(w http.ResponseWriter, r *http.Request) {
	if !s.modelsSupported() {
		http.NotFound(w, r)
		return
	}
	const back = "/admin/models"
	cfg, _, err := s.kernelConfig()
	if err == nil {
		err = r.ParseForm()
	}
	if err != nil {
		s.flashRedirect(w, r, back, "error", capitalize(err.Error())+".")
		return
	}
	version := r.PostFormValue("version")
	held := models.From(cfg)
	setup := models.Read(r.PostForm)
	setup.Keep(held)
	setup.Priced = true
	msg := ""
	for _, b := range []struct {
		c     *models.Choice
		field string
	}{{&setup.Chat, "chat_price"}, {&setup.Embed, "embed_price"}} {
		p, err := s.parseAmount(r.PostFormValue(b.field))
		if err != nil && msg == "" {
			msg = "Price: " + err.Error() + "."
		}
		b.c.Price = p
	}
	if r.PostFormValue("list") != "" {
		s.renderModels(w, r, http.StatusOK, cfg, version, setup, held, "")
		return
	}
	if msg == "" {
		if err := models.Check(r.Context(), setup); err != nil {
			msg = err.Error()
		}
	}
	if msg != "" {
		s.renderModels(w, r, http.StatusUnprocessableEntity, cfg, version, setup, held, msg)
		return
	}
	before, _ := json.Marshal(cfg)
	models.Apply(cfg, setup)
	after, _ := json.MarshalIndent(cfg, "", "  ")
	if c, _ := json.Marshal(cfg); bytes.Equal(before, c) {
		s.flashRedirect(w, r, back, "info", "Nothing changed.")
		return
	}
	if err := s.config.Reconfigure(version, append(after, '\n')); err != nil {
		s.flashRedirect(w, r, back, "error", capitalize(err.Error())+".")
		return
	}
	s.flashRedirect(w, r, back, "success", "Saved and restarted.")
}

// decodeNumbers decodes a JSON object keeping numbers as written, so a saved
// value and the same value typed back compare equal.
func decodeNumbers(raw []byte, v *map[string]any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(v); err != nil || *v == nil {
		return errors.New("the configuration must be a JSON object")
	}
	return nil
}

// keepEmpty puts back into to what from holds empty and to lacks, at any
// depth: a blank input cannot tell "", [], {} or null from absent.
func keepEmpty(from, to map[string]any) {
	for k, f := range from {
		t, ok := to[k]
		if !ok && isEmpty(f) {
			to[k] = f
		}
		fm, ok1 := f.(map[string]any)
		tm, ok2 := t.(map[string]any)
		if ok1 && ok2 {
			keepEmpty(fm, tm)
		}
	}
}

// dropKept is keepEmpty's inverse: it removes from to each empty value from
// holds equal at the same path, which keepEmpty puts back on every save.
func dropKept(from, to map[string]any) {
	for k, t := range to {
		f, ok := from[k]
		switch {
		case !ok:
		case isEmpty(t) && reflect.DeepEqual(f, t):
			delete(to, k)
		default:
			fm, ok1 := f.(map[string]any)
			tm, ok2 := t.(map[string]any)
			if ok1 && ok2 {
				dropKept(fm, tm)
			}
		}
	}
}

func isEmpty(v any) bool {
	switch v := v.(type) {
	case nil:
		return true
	case string:
		return v == ""
	case []any:
		return len(v) == 0
	case map[string]any:
		return len(v) == 0
	}
	return false
}

// getDotted, setDotted and deleteDotted address a value in a JSON object by a
// dotted path, the kernel's own spelling of a nested key (`native.time.price`).
func getDotted(m map[string]any, path string) any {
	segs := strings.Split(path, ".")
	for _, k := range segs[:len(segs)-1] {
		m, _ = m[k].(map[string]any)
	}
	return m[segs[len(segs)-1]]
}

func setDotted(m map[string]any, path string, v any) {
	segs := strings.Split(path, ".")
	for _, k := range segs[:len(segs)-1] {
		child, ok := m[k].(map[string]any)
		if !ok {
			child = map[string]any{}
			m[k] = child
		}
		m = child
	}
	m[segs[len(segs)-1]] = v
}

func deleteDotted(m map[string]any, path string) {
	segs := strings.Split(path, ".")
	for _, k := range segs[:len(segs)-1] {
		m, _ = m[k].(map[string]any)
	}
	delete(m, segs[len(segs)-1])
}

// ---- Juice updating itself (requirements.md §1 "Updating", §6.6) ----

// Updates is Juice updating itself: what the last check found, a check now,
// and the update, which installs the newest release and restarts Juice.
type Updates interface {
	State() UpdateState
	Check(ctx context.Context) error
	Start() error
}

// UpdateState is what every surface of the update is drawn from.
type UpdateState struct {
	Latest string // the newest release; "" until a check has answered
	Notes  string // the newest release's page
	Phase  string // "", "downloading" or "restarting"
	Failed string // why the last update stopped; "" when none did
}

// updateView is the sidebar's notice and Admin's version row: one state, this
// UI's release, and whether this build can update itself.
type updateView struct {
	UpdateState
	Version   string
	Can       bool
	CSRFField template.HTML
	OOB       bool // an answer of the heartbeat, swapped in place
}

// Available reports a newer release that this build can install.
func (v updateView) Available() bool { return v.Can && semver.Compare(v.Latest, v.Version) > 0 }

// Current and Next are the two releases as the page names them, 0.69.0.
func (v updateView) Current() string { return strings.TrimPrefix(v.Version, "v") }
func (v updateView) Next() string    { return strings.TrimPrefix(v.Latest, "v") }

// Doing is the update's step in words, under its progress bar.
func (v updateView) Doing() string {
	if v.Phase == "restarting" {
		return "Restarting…"
	}
	return "Downloading…"
}

// Shown is the heartbeat's token for what a widget drawn from this view shows.
func (v updateView) Shown() string {
	h := fnv.New32a()
	fmt.Fprint(h, v.Available(), "|", v.Latest, "|", v.Phase, "|", v.Failed)
	return strconv.FormatUint(uint64(h.Sum32()), 36)
}

// updateView is the view for the request: the superuser's, nil for anyone else.
func (s *Server) updateView(r *http.Request) *updateView {
	if !s.isSuperuser(r) {
		return nil
	}
	v := &updateView{Version: s.version, Can: s.updates != nil, CSRFField: csrf.TemplateField(r)}
	if s.updates != nil {
		v.UpdateState = s.updates.State()
	}
	return v
}

// watchUpdate keeps the notice (key notice, on every page) and the version row
// (key row, on Admin) current, so a release found by the check, an update under
// way and a failed one show with no reload.
func (s *Server) watchUpdate(r *http.Request, key, shown string) (template.HTML, error) {
	v := s.updateView(r)
	if v == nil || v.Shown() == shown {
		return "", nil
	}
	v.OOB = true
	switch key {
	case "notice":
		return s.render.HTML("_update_notice", *v)
	case "row":
		return s.render.HTML("_update_row", *v)
	}
	return "", nil
}

// postUpdate starts the update; the row shows its progress.
func (s *Server) postUpdate(w http.ResponseWriter, r *http.Request) {
	if s.updates == nil {
		http.NotFound(w, r)
		return
	}
	if err := s.updates.Start(); err != nil {
		s.flashRedirect(w, r, "/admin", "error", err.Error())
		return
	}
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

// postUpdateCheck asks for the newest release now and says what it found.
func (s *Server) postUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if s.updates == nil {
		http.NotFound(w, r)
		return
	}
	if err := s.updates.Check(r.Context()); err != nil {
		s.flashRedirect(w, r, "/admin", "error", err.Error())
		return
	}
	if v := s.updateView(r); v.Available() {
		s.flashRedirect(w, r, "/admin", "info", "Juice "+v.Next()+" is available.")
		return
	}
	s.flashRedirect(w, r, "/admin", "success", "Juice is up to date.")
}
