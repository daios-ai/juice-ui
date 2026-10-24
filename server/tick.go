package server

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/daios-ai/juice-ui/apps"
	"github.com/daios-ai/juice-ui/client"
)

// The heartbeat. A signed-in page makes one request per lane — `watch` every
// 5 s, `slow` every BadgeSecs — carrying one hidden input per widget that
// waits for something: kind:key:shown. The tick answers out-of-band fragments
// for the widgets whose truth differs from what they show and nothing for the
// rest, so an unchanged widget is never swapped under a click. The kernel-gone
// sign-out rides the tick, once, for every page. A watcher is registered per
// kind: the core's below, an app's through Deps.Watch under its slug.

// watch registers the watcher for a kind.
func (s *Server) watch(kind string, fn apps.Watcher) { s.watchers[kind] = fn }

// getTick answers a page's watches. An unknown kind is ignored; a watcher's
// error is logged and skipped, so one failing widget never stops the rest.
func (s *Server) getTick(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if _, err := s.client.Health(ctx); err != nil {
		_ = s.sessions.Logout(ctx)
		w.Header().Set("HX-Redirect", "/login")
		w.WriteHeader(http.StatusOK)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	q := r.URL.Query()
	for _, v := range append(q["watch"], q["slow"]...) {
		kind, rest, _ := strings.Cut(v, ":")
		key, shown, _ := strings.Cut(rest, ":")
		fn := s.watchers[kind]
		if fn == nil {
			continue
		}
		h, err := fn(r, key, shown)
		if err != nil {
			s.log.Warn("tick", "watch", v, "err", err)
			continue
		}
		_, _ = io.WriteString(w, string(h))
	}
}

// watchInbox keeps the task badge current: the viewer's waiting tasks and the
// requesters awaiting a decision, read at most once per badgeTTL.
func (s *Server) watchInbox(r *http.Request, key, shown string) (template.HTML, error) {
	if key != "badge" {
		return "", nil
	}
	n := s.taskBadgeCount(r)
	if strconv.Itoa(n) == shown {
		return "", nil
	}
	return apps.Badge("inbox", "Waiting tasks", n), nil
}

// taskBadgeCount is the badge's count, cached in the session for badgeTTL so
// the kernel is read no more often than the slow lane ticks.
func (s *Server) taskBadgeCount(r *http.Request) int {
	ctx := r.Context()
	sm := s.sessions.SCS()
	// Unix nanos, since gob encodes basic types without registration.
	cachedAtNano := sm.GetInt64(ctx, sessionKeyBadgeAt)
	count := sm.GetInt(ctx, sessionKeyBadgeCount)
	if time.Since(time.Unix(0, cachedAtNano)) <= s.badgeTTL {
		return count
	}
	_ = s.sessions.Do(ctx, func(tok string) error {
		me, e := s.me(r)
		if e != nil {
			return e
		}
		tasks, e := s.client.ListTasks(ctx, tok, "waiting")
		if e != nil {
			return e
		}
		requesters, e := s.client.ListRequesters(ctx, tok)
		if e != nil {
			return e
		}
		// A superuser lists everyone's tasks; the badge counts the viewer's,
		// and each requester awaiting a decision, whose tasks are withheld.
		n := 0
		for _, t := range tasks {
			if t.Waiting() && t.RequiredCaller == me.Address {
				n++
			}
		}
		for _, q := range requesters {
			if q.Status == "pending" {
				n++
			}
		}
		count = n
		sm.Put(ctx, sessionKeyBadgeCount, n)
		sm.Put(ctx, sessionKeyBadgeAt, time.Now().UnixNano())
		return nil
	})
	return count
}

// watchProcess ends the awaiting-receipt banner once the remote receipt has
// settled; while it is awaited the banner stands and nothing is sent.
func (s *Server) watchProcess(r *http.Request, id, _ string) (template.HTML, error) {
	ctx := r.Context()
	var p client.Process
	err := s.sessions.Do(ctx, func(tok string) (e error) {
		p, e = s.client.GetProcess(ctx, tok, id)
		return e
	})
	if err != nil || p.AwaitingReceipt {
		return "", err
	}
	return `<div id="process-await" hx-swap-oob="delete"></div>`, nil
}

// watchPeer refreshes a peer's reachability card on every slow tick, as the
// inspect page polled it: its details and latency change without the online
// word changing, and the card holds no control a swap could take from a click.
func (s *Server) watchPeer(r *http.Request, key, _ string) (template.HTML, error) {
	if !s.isSuperuser(r) {
		return "", nil
	}
	view, err := s.inspectView(r.Context(), key, "")
	if err != nil {
		return "", err
	}
	card, err := s.render.HTML("_inspect_status", view)
	if err != nil {
		return "", err
	}
	return template.HTML(fmt.Sprintf(`<div id="inspect-status" hx-swap-oob="true">%s</div>`, card)), nil
}

// inspectView loads the inspect page's view for a peer: the live inspection,
// its evidence, this kernel's row for it, and the roster's catalog size; with
// openForm "admin-rename", the rename suggestion too.
func (s *Server) inspectView(ctx context.Context, key, openForm string) (adminInspectView, error) {
	view := adminInspectView{Key: key, OpenForm: openForm}
	err := s.sessions.Do(ctx, func(tok string) error {
		insp, e := s.client.AdminInspect(ctx, tok, key)
		if e != nil {
			return e
		}
		view.Inspection = insp
		view.Report = poolEvidence(insp.SelfEvidence().Rows)
		view.Actions = peerActionRows(insp)
		view.Version = strings.TrimPrefix(insp.Reachability.Version, "juice-kernel/")
		// The kernel row says when the peer became known and whether it is
		// suspended; a key this kernel has never learned has no row, which is a
		// fact of the page, not an error.
		rec, e := s.client.AdminPeer(ctx, tok, key)
		if e != nil && !errors.Is(e, client.ErrNotFound) {
			return e
		}
		view.Record = rec
		// The roster states the catalog's size: the inspection's own list is
		// one page (verified live: 100 of 193). The rename form's prefill must
		// also be free of every bound petname, suspended parties included, and
		// of this kernel's own name, which it binds to its own key. Without the
		// form the roster is best-effort: a page is never refused for it.
		peers, e := s.client.AdminListPeers(ctx, tok, true)
		if e != nil && view.OpenForm != "admin-rename" {
			return nil
		}
		if e != nil {
			return e
		}
		taken := map[string]bool{}
		for _, p := range peers {
			if p.PublicKey == key {
				view.Catalog, view.Traded = p.Actions, p.Traded
			} else if p.Petname != "" {
				taken[p.Petname] = true
			}
		}
		if view.OpenForm != "admin-rename" {
			return nil
		}
		id, e := s.client.AdminIdentity(ctx, tok)
		if e != nil {
			return e
		}
		taken[id.Handle] = true
		view.Suggestion = petnameSuggestion(insp.Petname, insp.Nickname, key, func(n string) bool { return taken[n] })
		return nil
	})
	return view, err
}
