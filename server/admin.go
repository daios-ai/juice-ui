package server

import (
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/daios-ai/juice-ui/client"
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
	Users    []client.AdminUser
	Pager    Pager
	OpenForm string
}

type adminUserView struct {
	User     client.AdminUser
	OpenForm string
}

// adminPeersView drives the merged roster: every kernel this one knows, split by
// Tab into counterparties (an account here) and the known network (discovered,
// no account). Both classes are the same card; the tab decides which facts and
// verbs are true of it. Deposits is the money awaiting the operator, shown above
// the roster because closing an obligation is the one money verb left here.
type adminPeersView struct {
	depositForm
	Identity      client.Identity
	Deposits      client.Deposits
	Petnames      map[string]string // public key → bound petname, for naming a debtor
	Peers         []client.Peer
	Tab           string // "counterparties" | "known"
	Pager         Pager
	ShowSuspended bool // ?all=1 — counterparties list includes suspended rows
	CSRFField     template.HTML
	OpenForm      string
}

// Known reports whether the discovered tab is selected — the templates switch
// the card's facts and verbs on it.
func (v adminPeersView) Known() bool { return v.Tab == tabKnown }

const (
	tabCounterparties = "counterparties"
	tabKnown          = "known"
)

// peersHref builds a roster URL for one tab. Switching tabs drops the offset
// (page 1 of a different list) and drops ?all=1, which is a counterparty-only
// axis: suspension applies to accounts, and a discovered kernel has none.
func peersHref(tab string, all bool) string {
	q := url.Values{}
	if tab == tabKnown {
		q.Set("tab", tabKnown)
	} else if all {
		q.Set("all", "1")
	}
	if len(q) == 0 {
		return "/admin/peers"
	}
	return "/admin/peers?" + q.Encode()
}

// adminInspectView carries the inspected kernel. The billing account arrives
// inline on the inspection itself (client.Inspection.Account), so the page needs
// no roster scan: a nil account is a kernel that holds none here, which is a
// fact about the peer and no longer an opening for a gesture — a peer is never
// credited (§6.6). The one verb here is naming it.
type adminInspectView struct {
	Key        string
	Inspection client.Inspection
	OpenForm   string
	CSRFField  template.HTML
	// Forms are the completion forms for the steps this peer holds for us, one
	// per step, in the same order. A peer step carries no action, so the form is
	// derived from what the peer still admits (allowed_input) over what it
	// already sealed (partial_args) — exactly as the inbox does (§6.3).
	Forms []StepFormModel
}

func (s *Server) getAdmin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	// Back drives the shared deposit dialog's close/cancel links; empty would make
	// them reload this URL with ?form= still set, reopening the dialog on the
	// no-JS path.
	view := adminView{OpenForm: openForm(r, "admin"), depositForm: depositForm{Back: "/admin"}}
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
	offset := queryOffset(r)
	start, end, hasNext := pageSlice(len(view.Users), offset, pageSize)
	view.Users = view.Users[start:end]
	view.Pager = pageNav(r, offset, pageSize, hasNext)
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
		msg = "Recorded " + s.render.money(amount) + " · " + address + "."
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
	tab := tabCounterparties
	if r.URL.Query().Get("tab") == tabKnown {
		tab = tabKnown
	}
	showSuspended := tab == tabCounterparties && r.URL.Query().Get("all") == "1"
	view := adminPeersView{
		OpenForm: openForm(r, "admin"), Tab: tab, ShowSuspended: showSuspended,
		CSRFField: csrf.TemplateField(r),
		depositForm: depositForm{
			// A held payment is attributed from its own row, so the reference it
			// is named by rides into the form rather than being retyped.
			FundRef: r.URL.Query().Get("ref"),
			Back:    "/admin/peers",
		},
	}
	view.Petnames = map[string]string{}
	err := s.sessions.Do(ctx, func(tok string) error {
		if id, e := s.client.AdminIdentity(ctx, tok); e == nil {
			view.Identity = id
		}
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
		for _, peer := range p {
			// Every known kernel names the debtor rows, whichever tab is showing.
			if peer.Petname != "" {
				view.Petnames[peer.PublicKey] = peer.Petname
			}
			if peer.HasAccount == (tab == tabCounterparties) {
				view.Peers = append(view.Peers, peer)
			}
		}
		return nil
	})
	if s.handleErr(w, r, err) {
		return
	}
	offset := queryOffset(r)
	start, end, hasNext := pageSlice(len(view.Peers), offset, pageSize)
	view.Peers = view.Peers[start:end]
	view.Pager = pageNav(r, offset, pageSize, hasNext)
	s.renderPage(w, r, http.StatusOK, "admin_peers", "Peers", view)
}

// getAdminInspect renders a remote kernel's identity, reachability, the public
// actions it serves, and the evidence retained about it (admin inspect <key>).
// It is the only window onto a kernel this one has not transacted with, since
// the kernel serves no known-network roster.
func (s *Server) getAdminInspect(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	ctx := r.Context()
	view := adminInspectView{
		Key: key, OpenForm: openForm(r, "admin-peer"), CSRFField: csrf.TemplateField(r),
	}
	err := s.sessions.Do(ctx, func(tok string) error {
		insp, e := s.client.AdminInspect(ctx, tok, key)
		if e != nil {
			return e
		}
		view.Inspection = insp
		view.Forms = make([]StepFormModel, len(insp.Steps))
		for i, st := range insp.Steps {
			view.Forms[i] = BuildStepForm(st.AllowedInput, st.PartialArgs)
		}
		return nil
	})
	if s.handleErrFragment(w, r, err) {
		return
	}
	// An htmx poll refreshes just the reachability/status card.
	if isHTMX(r) {
		_ = s.render.Fragment(w, http.StatusOK, "_inspect_status", view)
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
	s.flashResult(w, r, err, "/admin/peers", "Peer "+verb+".")
}

// postAdminPeerStepComplete completes a step a peer holds whose required caller
// is this kernel. The request leaves signed with this kernel's own federation
// identity, so it acts as the whole kernel rather than as the operator who
// pressed it — which is why it lives here, behind requireSuperuser, and not in
// the inbox beside a person's own work (§6.6).
func (s *Server) postAdminPeerStepComplete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	key, id := r.PathValue("key"), r.PathValue("id")
	back := "/admin/peers/" + url.PathEscape(key)
	args, argErr := buildArgs(r)
	if argErr != nil {
		s.flashRedirect(w, r, back, "error", "Invalid arguments: "+argErr.Error())
		return
	}
	err := s.sessions.Do(ctx, func(tok string) error {
		_, e := s.client.CompletePeerStep(ctx, tok, id, key, args)
		return e
	})
	s.flashResult(w, r, err, back, "Completed on their side.")
}
