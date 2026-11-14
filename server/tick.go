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
	"github.com/daios-ai/juice-ui/session"
	"github.com/gorilla/csrf"
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
// error is logged and skipped, so one failing widget never stops the rest. A
// tick the page abandoned — it navigated while the request was in flight —
// says nothing about the kernel, so its failed health read is not the
// kernel-gone sign-out.
func (s *Server) getTick(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if _, err := s.client.Health(ctx); err != nil {
		if ctx.Err() != nil {
			return
		}
		s.endSession(ctx)
		w.Header().Set("HX-Redirect", "/login")
		w.WriteHeader(http.StatusOK)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	q := r.URL.Query()
	var out strings.Builder // written last, since a watcher may send the page on
	for _, v := range append(q["watch"], q["slow"]...) {
		kind, rest, _ := strings.Cut(v, ":")
		key, shown, _ := strings.Cut(rest, ":")
		fn := s.watchers[kind]
		if fn == nil {
			continue
		}
		h, err := fn(r, key, shown)
		if moved(w, r, err) {
			return
		}
		if err != nil {
			s.log.Warn("tick", "watch", v, "err", err)
			continue
		}
		out.WriteString(string(h))
	}
	_, _ = io.WriteString(w, out.String())
}

// watchSession is the idle warning: the dialog opens when the person's last
// request is session.Warning from the limit, and closes when a request — from
// another tab, or Stay signed in — moved it.
func (s *Server) watchSession(r *http.Request, key, shown string) (template.HTML, error) {
	v := idleView{Soon: s.sessions.Idle(r.Context()) >= session.IdleLimit-session.Warning, CSRFField: csrf.TemplateField(r)}
	if v.State() == shown {
		return "", nil
	}
	return s.render.HTML("_idle", v)
}

// postSession is Stay signed in: passing the gate recorded the activity, so
// the answer is the dialog closed.
func (s *Server) postSession(w http.ResponseWriter, r *http.Request) {
	_ = s.render.Fragment(w, http.StatusOK, "_idle", idleView{CSRFField: csrf.TemplateField(r)})
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

// asking is how many requesters await the viewer's decision, whose tasks the
// kernel withholds, save those the viewer ignored: a count says what needs
// attention (Discord, LinkedIn and Signal leave an ignored request out of it).
func (s *Server) asking(viewer string, reqs []client.Requester) int {
	n := 0
	for _, q := range reqs {
		if q.Status == "pending" && !s.viewers.ignored(viewer, q.Requester.Principal) {
			n++
		}
	}
	return n
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
		tasks, e := s.client.WaitingTasks(ctx, tok)
		if e != nil {
			return e
		}
		requesters, e := s.client.ListRequesters(ctx, tok)
		if e != nil {
			return e
		}
		// A superuser lists everyone's tasks; the badge counts the viewer's,
		// and each requester awaiting a decision, whose tasks are withheld,
		// save one the viewer ignored. The refresh also starts sending again
		// the viewer's disconnects a kernel did not answer, off this request:
		// the tick never waits on another kernel.
		viewer := me.Address.Principal
		if s.viewers.due(viewer, resendEvery, time.Now()) {
			go s.resend(viewer, tok)
		}
		n := 0
		for _, t := range tasks {
			if t.Waiting() && t.RequiredCaller == me.Address {
				n++
			}
		}
		n += s.asking(viewer, requesters)
		count = n
		sm.Put(ctx, sessionKeyBadgeCount, n)
		sm.Put(ctx, sessionKeyBadgeAt, time.Now().UnixNano())
		return nil
	})
	return count
}

// watchBalance keeps the balance beneath the composer current: the viewer's
// available amount, answered only when it differs from the one shown — after a
// run in the panel, a priced fill, or money arriving.
func (s *Server) watchBalance(r *http.Request, _, shown string) (template.HTML, error) {
	me, err := s.me(r)
	if err != nil {
		return "", err
	}
	if strconv.FormatInt(me.Available, 10) == shown {
		return "", nil
	}
	return s.render.HTML("_balance", balanceView{Amount: me.Available, OOB: true})
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
		view.Report = poolEvidence(insp.SelfEvidence())
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
