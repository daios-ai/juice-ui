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
// requireSuperuser (@sys), and the kernel independently rejects a non-superuser
// bearer with 403 — so a stale UI check can never authorize a real admin action.
// The admin surface covers the operator verbs no ordinary user performs: money
// (deposit/withdraw), access (suspend), and federation trust (peers, settlement).

type adminView struct {
	Users    []client.AdminUser
	Pager    Pager
	OpenForm string
}

type adminUserView struct {
	User     client.AdminUser
	OpenForm string
}

type adminPeersView struct {
	Identity      client.Identity
	Peers         []client.Peer
	ShowSuspended bool          // ?all=1 — peers list includes suspended peers
	CSRFField     template.HTML // carried on the view so the polled fragment's forms keep their token
	OpenForm      string
}

// adminInspectView carries the inspected kernel plus PeerRow — this kernel's own
// billing row for it, when one exists. A peer we have transacted with is not a
// membership state (there is no subscription); the honest thing to show is the
// bilateral position, so the row itself is carried rather than a boolean.
type adminInspectView struct {
	Key        string
	Inspection client.Inspection
	PeerRow    *client.Peer
}

func (s *Server) getAdmin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	view := adminView{OpenForm: openForm(r, "admin")}
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
	// Active users first, then alphabetical by handle within each block, so the
	// operator sees the live roster before the suspended tail.
	sort.SliceStable(view.Users, func(i, j int) bool {
		a, b := view.Users[i], view.Users[j]
		if a.Suspended() != b.Suspended() {
			return !a.Suspended() // active (not suspended) sorts first
		}
		return strings.ToLower(a.Handle) < strings.ToLower(b.Handle)
	})
	offset := queryOffset(r)
	start, end, hasNext := pageSlice(len(view.Users), offset, pageSize)
	view.Users = view.Users[start:end]
	view.Pager = pageNav(r, offset, pageSize, hasNext)
	s.renderPage(w, r, http.StatusOK, "admin", "Admin", view)
}

func (s *Server) getAdminUser(w http.ResponseWriter, r *http.Request) {
	handle := r.PathValue("handle")
	ctx := r.Context()
	var view adminUserView
	err := s.sessions.Do(ctx, func(tok string) error {
		u, e := s.client.AdminGetUser(ctx, tok, handle)
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
	s.renderPage(w, r, http.StatusOK, "admin_user", "User "+handle, view)
}

// postAdminUserOp handles the per-user operator verbs, dispatched on the path's
// last segment. The switch is exhaustive on purpose: an unrecognised segment is
// a 404, never a fallthrough into a destructive verb.
func (s *Server) postAdminUserOp(w http.ResponseWriter, r *http.Request) {
	handle := r.PathValue("handle")
	ctx := r.Context()
	switch lastSegment(r.URL.Path) {
	case "suspend":
		err := s.sessions.Do(ctx, func(tok string) error { return s.client.AdminSuspendUser(ctx, tok, handle) })
		s.flashResult(w, r, err, "/admin", "User "+handle+" suspended.")
	case "unsuspend":
		err := s.sessions.Do(ctx, func(tok string) error { return s.client.AdminUnsuspendUser(ctx, tok, handle) })
		s.flashResult(w, r, err, "/admin", "User "+handle+" unsuspended.")
	case "rename":
		s.renameUser(w, r, handle)
	default:
		s.renderPage(w, r, http.StatusNotFound, "error", "Not found", errorView{Message: "Page not found."})
	}
}

// renameUser rebinds a handle: the old one is freed and every reference follows
// the account. A peer's local alias is an ordinary handle here, so the same verb
// rebinds it. The new handle is taken verbatim — a sigiled one is the kernel's
// to reject, not ours to rewrite (requirements.md §4).
func (s *Server) renameUser(w http.ResponseWriter, r *http.Request, handle string) {
	ctx, back := r.Context(), "/admin/users/"+url.PathEscape(handle)
	newHandle := strings.TrimSpace(r.FormValue("new_handle"))
	if newHandle == "" {
		s.flashRedirect(w, r, back, "error", "Enter a new handle.")
		return
	}
	if err := s.sessions.Do(ctx, func(tok string) error { return s.client.AdminRename(ctx, tok, handle, newHandle) }); err != nil {
		s.flashRedirect(w, r, back, "error", userMessage(err))
		return
	}
	s.flashRedirect(w, r, "/admin/users/"+url.PathEscape(newHandle), "success", handle+" is now "+newHandle+".")
}

// postAdminAdjust handles deposit/withdraw, dispatched on the path's last segment.
func (s *Server) postAdminAdjust(w http.ResponseWriter, r *http.Request) {
	op := lastSegment(r.URL.Path)
	ctx := r.Context()
	handle := strings.TrimSpace(r.FormValue("handle"))
	amount := parseDollars(r.FormValue("amount"))
	reason := strings.TrimSpace(r.FormValue("reason"))
	extKey := strings.TrimSpace(r.FormValue("external_key"))
	if handle == "" || amount <= 0 {
		s.flashRedirect(w, r, "/admin", "error", "A user handle and a positive amount are required.")
		return
	}
	err := s.sessions.Do(ctx, func(tok string) error {
		if op == "withdraw" {
			_, e := s.client.AdminWithdraw(ctx, tok, handle, amount, reason, extKey)
			return e
		}
		_, e := s.client.AdminDeposit(ctx, tok, handle, amount, reason, extKey)
		return e
	})
	verb := "Deposited"
	if op == "withdraw" {
		verb = "Withdrew"
	}
	s.flashResult(w, r, err, "/admin", verb+" "+money(amount)+" · "+handle+".")
}

func (s *Server) getAdminPeers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	showSuspended := r.URL.Query().Get("all") == "1"
	view := adminPeersView{OpenForm: openForm(r, "admin"), ShowSuspended: showSuspended, CSRFField: csrf.TemplateField(r)}
	err := s.sessions.Do(ctx, func(tok string) error {
		if id, e := s.client.AdminIdentity(ctx, tok); e == nil {
			view.Identity = id
		}
		// all=showSuspended also lists suspended peers.
		p, e := s.client.AdminListPeers(ctx, tok, showSuspended)
		if e != nil {
			return e
		}
		view.Peers = p
		return nil
	})
	if s.handleErrFragment(w, r, err) {
		return
	}
	// An htmx poll refreshes just the peers grid (last_seen/peer_credit move).
	if isHTMX(r) {
		_ = s.render.Fragment(w, http.StatusOK, "_peers", view)
		return
	}
	s.renderPage(w, r, http.StatusOK, "admin_peers", "Peers", view)
}

// getAdminInspect renders a remote kernel's identity, reachability, the public
// actions it serves, and the evidence retained about it (admin inspect <key>).
// It is the only window onto a kernel this one has not transacted with, since
// the kernel serves no known-network roster.
func (s *Server) getAdminInspect(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	ctx := r.Context()
	view := adminInspectView{Key: key}
	err := s.sessions.Do(ctx, func(tok string) error {
		insp, e := s.client.AdminInspect(ctx, tok, key)
		if e != nil {
			return e
		}
		view.Inspection = insp
		// Carry our billing row for this kernel, when there is one, so the page
		// can state the bilateral position. Suspended peers are included: a
		// frozen peer still holds a position, and this is the page an operator
		// reads before unsuspending.
		if peers, e := s.client.AdminListPeers(ctx, tok, true); e == nil {
			for i, p := range peers {
				if p.PublicKey == key {
					view.PeerRow = &peers[i]
				}
			}
		}
		return nil
	})
	if s.handleErrFragment(w, r, err) {
		return
	}
	// An htmx poll refreshes just the reachability/status card. Because inspect
	// persists the peer-sync cache, each refresh also freshens this peer's
	// peer_state kernel-wide (feeds the action/process polls).
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

// positionText renders a peer's bilateral position with its reading — never a
// bare negative number (requirements.md §6.6): positive = we owe the peer (it
// prepaid or earned here — verified live: earnings accrue on the same row, so
// "prepaid" would misread it), negative = the peer owes this kernel.
func positionText(available int64) string {
	switch {
	case available > 0:
		return "we owe them " + money(available)
	case available < 0:
		return "owes us " + money(-available)
	default:
		return "settled"
	}
}

// postAdminSettle initiates settlement of the bilateral position with a peer,
// or — with a settlement_id — records the external rail payment for a prior
// outcome (the CLI --cash leg). Both concern REAL external money, so the forms
// route through confirms naming the peer (§6.6); the kernel's own outcome
// message is flashed verbatim (the UI never computes or predicts one).
func (s *Server) postAdminSettle(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	peer := strings.TrimSpace(r.FormValue("peer"))
	settlementID := strings.TrimSpace(r.FormValue("settlement_id"))
	if peer == "" {
		s.flashRedirect(w, r, "/admin/peers", "error", "A peer handle or key is required.")
		return
	}
	var res client.SettleResult
	err := s.sessions.Do(ctx, func(tok string) error {
		out, e := s.client.AdminSettle(ctx, tok, peer, settlementID)
		res = out
		return e
	})
	msg := res.Message
	if msg == "" {
		msg = "Settlement " + res.Status + "."
	}
	s.flashResult(w, r, err, "/admin/peers", msg)
}

// adminTransfersView drives the value-transfer queue page (§6.6): the kernel's
// buyer-side pending/quarantined records. Retry is the ONLY mutation — the
// template offers no refund/force-settle/edit affordance, because quarantine
// means "evidence insufficient", never an operator-chosen outcome.
type adminTransfersView struct {
	Transfers []client.PendingTransfer
	Status    string // "" = the kernel's default unresolved set (pending + quarantined)
	Detail    *client.PendingTransfer
	Pager     Pager
	CSRFField template.HTML
}

func (s *Server) getAdminTransfers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	status := r.URL.Query().Get("status")
	offset := queryOffset(r)
	view := adminTransfersView{Status: status, CSRFField: csrf.TemplateField(r)}
	err := s.sessions.Do(ctx, func(tok string) error {
		// One page plus a lookahead row so the pager knows there is a next.
		ts, e := s.client.AdminListTransfers(ctx, tok, status, pageSize+1, offset)
		if e != nil {
			return e
		}
		if len(ts) > pageSize {
			ts = ts[:pageSize]
			view.Pager = pageNav(r, offset, pageSize, true)
		} else {
			view.Pager = pageNav(r, offset, pageSize, false)
		}
		view.Transfers = ts
		return nil
	})
	if s.handleErr(w, r, err) {
		return
	}
	s.renderPage(w, r, http.StatusOK, "admin_transfers", "Transfers", view)
}

func (s *Server) getAdminTransfer(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	view := adminTransfersView{CSRFField: csrf.TemplateField(r)}
	err := s.sessions.Do(ctx, func(tok string) error {
		t, e := s.client.AdminGetTransfer(ctx, tok, id)
		if e != nil {
			return e
		}
		view.Detail = &t
		return nil
	})
	if s.handleErr(w, r, err) {
		return
	}
	s.renderPage(w, r, http.StatusOK, "admin_transfers", "Transfer", view)
}

// postAdminTransferRetry re-presents the transfer's stored signed completion;
// the kernel settles strictly on receipt evidence. The sole mutation (§6.6).
func (s *Server) postAdminTransferRetry(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	var res client.PendingTransfer
	err := s.sessions.Do(ctx, func(tok string) error {
		t, e := s.client.AdminRetryTransfer(ctx, tok, id)
		res = t
		return e
	})
	msg := "Retried — status: " + res.Status + "."
	s.flashResult(w, r, err, "/admin/transfers", msg)
}
