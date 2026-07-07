package server

import (
	"net/http"
	"strings"

	"github.com/daios-ai/juice-ui/client"
)

// Admin (superuser) handlers. Every route reaches here only through
// requireSuperuser (@sys), and the kernel independently rejects a non-superuser
// bearer with 403 — so a stale UI check can never authorize a real admin action.
// The admin surface covers the operator verbs no ordinary user performs: money
// (deposit/withdraw), access (suspend), and federation trust (friend/peers).

type adminView struct {
	Users    []client.AdminUser
	OpenForm string
}

type adminUserView struct {
	User client.AdminUser
}

type adminPeersView struct {
	Identity   client.Identity
	Peers      []client.Peer
	Roster     []client.DiscoveredKernel
	FriendKeys map[string]bool // public keys we already friend (hide Befriend for these)
	OpenForm   string
}

type adminInspectView struct {
	Key        string
	Inspection client.Inspection
	IsFriend   bool
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
	if isAuthErr(err) {
		s.handleErr(w, r, err)
		return
	}
	if err != nil {
		s.flashRedirect(w, r, "/admin", "error", userMessage(err))
		return
	}
	verb := "suspended"
	if op == "unsuspend" {
		verb = "unsuspended"
	}
	s.flashRedirect(w, r, "/admin", "success", "User "+handle+" "+verb+".")
}

// postAdminAdjust handles deposit/withdraw, dispatched on the path's last segment.
func (s *Server) postAdminAdjust(w http.ResponseWriter, r *http.Request) {
	op := lastSegment(r.URL.Path)
	ctx := r.Context()
	handle := strings.TrimSpace(r.FormValue("handle"))
	amount := parseInt64(r.FormValue("amount"))
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
	if isAuthErr(err) {
		s.handleErr(w, r, err)
		return
	}
	if err != nil {
		s.flashRedirect(w, r, "/admin", "error", userMessage(err))
		return
	}
	verb := "Deposited"
	if op == "withdraw" {
		verb = "Withdrew"
	}
	s.flashRedirect(w, r, "/admin", "success", verb+" "+money(amount)+" ¤ · "+handle+".")
}

func (s *Server) getAdminPeers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	view := adminPeersView{OpenForm: openForm(r, "admin")}
	err := s.sessions.Do(ctx, func(tok string) error {
		if id, e := s.client.AdminIdentity(ctx, tok); e == nil {
			view.Identity = id
		}
		p, e := s.client.AdminListPeers(ctx, tok, true) // gossip roster included
		if e != nil {
			return e
		}
		view.Peers = p.Peers
		view.Roster = p.Roster
		view.FriendKeys = make(map[string]bool, len(p.Peers))
		for _, peer := range p.Peers {
			view.FriendKeys[peer.PublicKey] = true
		}
		return nil
	})
	if s.handleErr(w, r, err) {
		return
	}
	s.renderPage(w, r, http.StatusOK, "admin_peers", "Peers", view)
}

// getAdminInspect renders a remote kernel's identity, reachability, public
// actions, and transacted friends (admin inspect <key>).
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
		// A peer we already friend shows differently (no Befriend prompt).
		if peers, e := s.client.AdminListPeers(ctx, tok, false); e == nil {
			for _, p := range peers.Peers {
				if p.PublicKey == key {
					view.IsFriend = true
				}
			}
		}
		return nil
	})
	if s.handleErr(w, r, err) {
		return
	}
	s.renderPage(w, r, http.StatusOK, "admin_inspect", "Inspect", view)
}

func (s *Server) postAdminFriend(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	key := strings.TrimSpace(r.FormValue("key"))
	if key == "" {
		s.flashRedirect(w, r, "/admin/peers", "error", "A peer public key is required.")
		return
	}
	err := s.sessions.Do(ctx, func(tok string) error { return s.client.AdminFriend(ctx, tok, key) })
	if isAuthErr(err) {
		s.handleErr(w, r, err)
		return
	}
	if err != nil {
		s.flashRedirect(w, r, "/admin/peers", "error", userMessage(err))
		return
	}
	s.flashRedirect(w, r, "/admin/peers", "success", "Friend request sent; actions imported.")
}

func (s *Server) postAdminUnfriend(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	peer := strings.TrimSpace(r.FormValue("peer"))
	if peer == "" {
		s.flashRedirect(w, r, "/admin/peers", "error", "A peer handle or key is required.")
		return
	}
	err := s.sessions.Do(ctx, func(tok string) error { return s.client.AdminUnfriend(ctx, tok, peer) })
	if isAuthErr(err) {
		s.handleErr(w, r, err)
		return
	}
	if err != nil {
		s.flashRedirect(w, r, "/admin/peers", "error", userMessage(err))
		return
	}
	s.flashRedirect(w, r, "/admin/peers", "success", "Unfriended "+peer+". Proxies deactivated; history preserved.")
}
