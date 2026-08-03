package server

import (
	"html/template"
	"net/http"
	"sort"
	"strings"

	"github.com/daios-ai/juice-ui/client"
	"github.com/gorilla/csrf"
)

// Admin (superuser) handlers. Every route reaches here only through
// requireSuperuser (@sys), and the kernel independently rejects a non-superuser
// bearer with 403 — so a stale UI check can never authorize a real admin action.
// The admin surface covers the operator verbs no ordinary user performs: money
// (deposit/withdraw), access (suspend), and federation trust (friend/peers).

type adminView struct {
	Users    []client.AdminUser
	Pager    Pager
	OpenForm string
}

type adminUserView struct {
	User client.AdminUser
}

type adminPeersView struct {
	Identity       client.Identity
	Peers          []client.Peer
	Roster         []client.DiscoveredKernel
	SubscribedKeys map[string]bool // public keys we already subscribe to (hide Subscribe for these)
	ShowSuspended  bool            // ?all=1 — peers list includes suspended peers
	CSRFField      template.HTML   // carried on the view so the polled fragment's forms keep their token
	OpenForm       string
}

type adminInspectView struct {
	Key          string
	Inspection   client.Inspection
	IsSubscribed bool
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
	s.renderPage(w, r, http.StatusOK, "admin_user", "User "+handle, view)
}

// postAdminUserOp handles suspend/unsuspend, dispatched on the path's last segment.
func (s *Server) postAdminUserOp(w http.ResponseWriter, r *http.Request) {
	handle := r.PathValue("handle")
	op := lastSegment(r.URL.Path)
	ctx := r.Context()
	err := s.sessions.Do(ctx, func(tok string) error {
		if op == "unsuspend" {
			return s.client.AdminUnsuspendUser(ctx, tok, handle)
		}
		return s.client.AdminSuspendUser(ctx, tok, handle)
	})
	verb := "suspended"
	if op == "unsuspend" {
		verb = "unsuspended"
	}
	s.flashResult(w, r, err, "/admin", "User "+handle+" "+verb+".")
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
		// gossip roster included; all=showSuspended also lists suspended peers.
		p, e := s.client.AdminListPeers(ctx, tok, true, showSuspended)
		if e != nil {
			return e
		}
		view.Peers = p.Peers
		view.Roster = p.Roster
		view.SubscribedKeys = make(map[string]bool, len(p.Peers))
		for _, peer := range p.Peers {
			view.SubscribedKeys[peer.PublicKey] = true
		}
		return nil
	})
	if s.handleErrFragment(w, r, err) {
		return
	}
	// An htmx poll refreshes just the peers table (last_seen/peer_credit move);
	// the roster is left static so its <details> state survives.
	if isHTMX(r) {
		_ = s.render.Fragment(w, http.StatusOK, "_peers_subscribed", view)
		return
	}
	s.renderPage(w, r, http.StatusOK, "admin_peers", "Peers", view)
}

// getAdminInspect renders a remote kernel's identity, reachability, public
// actions (with descriptions), and transacted peers (admin inspect <key>).
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
		// A peer we already subscribe to shows differently (no Subscribe prompt).
		if peers, e := s.client.AdminListPeers(ctx, tok, false, false); e == nil {
			for _, p := range peers.Peers {
				if p.PublicKey == key {
					view.IsSubscribed = true
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

func (s *Server) postAdminSubscribe(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	key := strings.TrimSpace(r.FormValue("key"))
	if key == "" {
		s.flashRedirect(w, r, "/admin/peers", "error", "A peer public key is required.")
		return
	}
	err := s.sessions.Do(ctx, func(tok string) error { return s.client.AdminSubscribe(ctx, tok, key) })
	s.flashResult(w, r, err, "/admin/peers", "Subscribed — actions imported.")
}

func (s *Server) postAdminUnsubscribe(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	peer := strings.TrimSpace(r.FormValue("peer"))
	if peer == "" {
		s.flashRedirect(w, r, "/admin/peers", "error", "A peer handle or key is required.")
		return
	}
	err := s.sessions.Do(ctx, func(tok string) error { return s.client.AdminUnsubscribe(ctx, tok, peer) })
	s.flashResult(w, r, err, "/admin/peers", "Unsubscribed "+peer+" — proxies deactivated; history preserved.")
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
