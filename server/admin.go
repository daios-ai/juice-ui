package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/daios-ai/juice-ui/client"
	"github.com/daios-ai/juice-ui/wallet"
	"github.com/gorilla/csrf"
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
	Configurable bool // this installation's kernel: its configuration page exists
}

type adminUserView struct {
	User     client.AdminUser
	OpenForm string
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
	view := adminView{OpenForm: openForm(r, "admin"), depositForm: depositForm{Back: "/admin"}, Configurable: s.config != nil}
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

func (s *Server) getAdminUser(w http.ResponseWriter, r *http.Request) {
	address := r.PathValue("address")
	ctx := r.Context()
	var view adminUserView
	err := s.sessions.Do(ctx, func(tok string) error {
		u, e := s.client.AdminGetUser(ctx, tok, address)
		if e != nil {
			return e
		}
		view.User = u
		return nil
	})
	if s.handleErr(w, r, err) {
		return
	}
	view.OpenForm = openForm(r, "admin-user")
	s.renderPage(w, r, http.StatusOK, "admin_user", "User "+address, view)
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
// (requirements.md §4). The redirect lands on the new address, which is the
// page that then exists.
//
// A kernel is renamed on its own page by renamePeer: the two namespaces are
// separate routes on the kernel now, and a route that guessed which one was
// meant would be guessing what the kernel refuses to guess (§6.6).
func (s *Server) renameUser(w http.ResponseWriter, r *http.Request, address string) {
	ctx, back := r.Context(), "/admin/users/"+url.PathEscape(address)
	newName := strings.TrimSpace(r.FormValue("new_name"))
	if newName == "" {
		s.flashRedirect(w, r, back, "error", "Enter a new address.")
		return
	}
	if err := s.sessions.Do(ctx, func(tok string) error { return s.client.AdminRenameUser(ctx, tok, address, newName) }); err != nil {
		s.flashRedirect(w, r, back, "error", userMessage(err))
		return
	}
	s.flashRedirect(w, r, "/admin/users/"+url.PathEscape(newName), "success", address+" is now "+newName+".")
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
// Schema (§6.6): the kernel's config.json and an agent's. A hidden key is
// never shown and kept as saved; a fixed key is shown read-only and kept as
// saved. The hooks are the kernel's secrets: shown withholds them from what
// the page shows and names what to mask, mask makes those inputs write-only,
// keep puts back what the form could not carry. An app edits its file through
// apps.ConfigEditor, which this satisfies.
type configEditor struct {
	s      *Server
	schema json.RawMessage
	hidden []string
	fixed  []string
	shown  func(cfg map[string]any) map[string]bool
	mask   func(f *Field, keyed map[string]bool)
	keep   func(saved, to map[string]any)
}

// editor is the editor of a file without secrets.
func (s *Server) editor(schema json.RawMessage, hidden ...string) configEditor {
	return configEditor{s: s, schema: schema, hidden: hidden}
}

// kernelEditor edits the kernel's file: the schema pointer and credentials_key
// hidden; kernel_handle, the first boot's name, and listen_addr, the port this
// UI finds its kernel by (kernel.World), read-only; each endpoint key, which
// the kernel gives no command-line flag, write-only and kept by its name.
func (s *Server) kernelEditor(schema json.RawMessage) configEditor {
	e := s.editor(schema, "$schema", "credentials_key")
	e.fixed = []string{"kernel_handle", "listen_addr"}
	e.shown, e.mask, e.keep = configShown, maskKeys, keepKeys
	return e
}

// configured is the placeholder of an endpoint key the file holds.
const configured = "configured ✓ (replace)"

// endpoints are the file's language-model endpoints, `native.llm.endpoints`,
// each holding its `name` and the key the kernel gives no command-line flag
// (kernel requirements, Config).
func endpoints(cfg map[string]any) []map[string]any {
	list, _ := getDotted(cfg, "native.llm.endpoints").([]any)
	out := make([]map[string]any, 0, len(list))
	for _, e := range list {
		if m, ok := e.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
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

// configShown withholds the file's secrets from what the page shows:
// credentials_key, and each endpoint's key, its row saying only that it holds
// one. It returns the endpoint names that held a key.
func configShown(cfg map[string]any) map[string]bool {
	delete(cfg, "credentials_key")
	keyed := map[string]bool{}
	for _, e := range endpoints(cfg) {
		if name, ok := e["name"].(string); ok {
			keyed[name] = e["key"] != nil
		}
		delete(e, "key")
	}
	return keyed
}

// maskKeys makes each endpoint row's key write-only, its placeholder saying so
// where the file holds one for that endpoint's name.
func maskKeys(f *Field, keyed map[string]bool) {
	if f.Path == "/native/llm/endpoints" {
		for _, row := range f.Rows {
			i := slices.IndexFunc(row.Fields, func(c Field) bool { return c.Name == "name" })
			for j := range row.Fields {
				if k := &row.Fields[j]; k.Name == "key" {
					k.Secret, k.Default = true, ""
					if i >= 0 && keyed[row.Fields[i].Value] {
						k.Default = configured
					}
				}
			}
		}
	}
	for i := range f.Fields {
		maskKeys(&f.Fields[i], keyed)
	}
}

// keepKeys keeps an endpoint key not replaced — matched by the endpoint's
// name, never its row.
func keepKeys(saved, to map[string]any) {
	keys := map[string]any{}
	for _, e := range endpoints(saved) {
		if name, ok := e["name"].(string); ok {
			keys[name] = e["key"]
		}
	}
	for _, e := range endpoints(to) {
		name, ok := e["name"].(string)
		if _, typed := e["key"]; ok && !typed && keys[name] != nil {
			e["key"] = keys[name]
		}
	}
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

// form is the schema filled from what the page shows of saved: hidden keys and
// secrets left out, fixed keys read-only, secret inputs write-only. The JSON
// editor opens on the shown file itself, never a skeleton: what a save from it
// omits is removed. It returns what Fill could not place exactly.
func (c configEditor) form(saved map[string]any) (FormModel, []string) {
	var keyed map[string]bool
	if c.shown != nil {
		keyed = c.shown(saved)
	}
	raw, _ := json.MarshalIndent(saved, "", "  ")
	form := BuildForm(c.schema)
	inexact := form.Fill(raw)
	form.Example = "" // never BuildForm's skeleton: an absent file opens on nothing
	if len(saved) > 0 {
		form.Example = string(raw)
	}
	fields := form.Fields[:0]
	for _, f := range form.Fields {
		if slices.Contains(c.hidden, f.Name) {
			continue
		}
		f.ReadOnly = slices.Contains(c.fixed, f.Name)
		if c.mask != nil {
			c.mask(&f, keyed)
		}
		fields = append(fields, f)
	}
	form.Fields = fields
	return form, inexact
}

// normal reads to as a save writes it over saved, keeping as saved what the
// form cannot carry: the hidden and fixed keys, the keep hook's secrets, and
// an empty value a blank input cannot tell from absent. A save and both
// directions of the JSON switch share it.
func (c configEditor) normal(saved, to map[string]any) {
	for _, p := range slices.Concat(c.hidden, c.fixed) {
		deleteDotted(to, p)
		if v := getDotted(saved, p); v != nil {
			setDotted(to, p, v)
		}
	}
	if c.keep != nil {
		c.keep(saved, to)
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
	if c.shown != nil {
		c.shown(to)
	}
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
