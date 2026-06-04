package server

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"

	"github.com/daios-ai/juice-ui/client"
	"github.com/gorilla/csrf"
)

// ---- view models ----

type loginView struct{ Error, Notice string }

type dashboardView struct {
	User client.User
	Open []client.Process
}

type discoverResult struct {
	ActionID    string
	Name        string
	OwnerHandle string
	Description string
	Score       float64
}

type discoverView struct {
	Query           string
	LookupAvailable bool
	Reason          string // why semantic search fell back (kernel's own message)
	Results         []discoverResult
	Catalog         []client.Action
}

type actionView struct {
	Action   client.Action
	HasStats bool
	Stats    client.Stats
	Form     FormModel
	IsOwner  bool
	IsPaid   bool
	Open     []client.Process
	Outcome  *Outcome
	Error    string
}

type studioView struct {
	Actions []client.Action
	Error   string
	Notice  string
}

type processesView struct{ Open, Closed []client.Process }

type processView struct {
	Process      client.Process
	Transactions []client.Transaction
}

type transactionsView struct {
	Transactions []client.Transaction
	MyID         string
}

type txView struct {
	Tx      client.Transaction
	CanRate bool
	Error   string
}

type listenersView struct {
	Listeners []client.Listener
	Error     string
	Notice    string
}

type inboxView struct {
	Events    []client.Event
	CSRFField template.HTML
	Error     string
}

type errorView struct{ Message string }

// ---- auth ----

func (s *Server) getLogin(w http.ResponseWriter, r *http.Request) {
	if s.sessions.IsAuthenticated(r.Context()) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.renderPage(w, r, http.StatusOK, "login", "Sign in", loginView{})
}

func (s *Server) postLogin(w http.ResponseWriter, r *http.Request) {
	handle, password := r.FormValue("handle"), r.FormValue("password")
	if err := s.doLogin(r.Context(), handle, password); err != nil {
		s.log.Warn("login failed", "err", err)
		s.renderPage(w, r, http.StatusOK, "login", "Sign in", loginView{Error: loginErrorMessage(err)})
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) postSignup(w http.ResponseWriter, r *http.Request) {
	handle, email, password := r.FormValue("handle"), r.FormValue("email"), r.FormValue("password")
	ctx := r.Context()
	if _, err := s.client.CreateUser(ctx, handle, email, password); err != nil {
		s.renderPage(w, r, http.StatusOK, "login", "Sign in", loginView{Error: signupErrorMessage(err)})
		return
	}
	if err := s.doLogin(ctx, handle, password); err != nil {
		s.renderPage(w, r, http.StatusOK, "login", "Sign in", loginView{
			Notice: "Account created. Please sign in.",
			Error:  loginErrorMessage(err),
		})
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// doLogin runs the PKCE authorization-code flow: the verifier is generated and
// kept server-side, the challenge is sent to authorize, the returned code is
// exchanged with the verifier, and the resulting tokens are stored in the
// session (R5.1).
func (s *Server) doLogin(ctx context.Context, handle, password string) error {
	challenge, err := s.sessions.BeginPKCE(ctx)
	if err != nil {
		return err
	}
	code, err := s.client.Authorize(ctx, handle, password, challenge)
	if err != nil {
		return err
	}
	verifier, ok := s.sessions.TakeVerifier(ctx)
	if !ok {
		return fmt.Errorf("pkce verifier missing from session")
	}
	tokens, err := s.client.ExchangeCode(ctx, code, verifier)
	if err != nil {
		return err
	}
	return s.sessions.Login(ctx, tokens, "@"+strings.TrimPrefix(handle, "@"))
}

func (s *Server) postLogout(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if toks, ok := s.sessions.Tokens(ctx); ok {
		_ = s.client.Logout(ctx, toks.RefreshToken) // revoke refresh token (R5.4)
	}
	_ = s.sessions.Logout(ctx)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) postTheme(w http.ResponseWriter, r *http.Request) {
	theme := r.FormValue("theme")
	if theme != "light" && theme != "dark" {
		theme = ""
	}
	http.SetCookie(w, &http.Cookie{
		Name: "theme", Value: theme, Path: "/", MaxAge: 31536000,
		HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteLaxMode,
	})
	to := r.Header.Get("Referer")
	if to == "" {
		to = "/"
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

func (s *Server) postSidebar(w http.ResponseWriter, r *http.Request) {
	state := r.FormValue("sidebar")
	if state != "collapsed" {
		state = "expanded" // any non-collapsed value clears the collapse
	}
	http.SetCookie(w, &http.Cookie{
		Name: "sidebar", Value: state, Path: "/", MaxAge: 31536000,
		HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteLaxMode,
	})
	to := r.Header.Get("Referer")
	if to == "" {
		to = "/"
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// ---- dashboard ----

func (s *Server) getDashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var user client.User
	var procs []client.Process
	err := s.sessions.Do(ctx, func(tok string) error {
		u, e := s.client.Me(ctx, tok)
		if e != nil {
			return e
		}
		user = u
		p, e := s.client.ListProcesses(ctx, tok)
		if e != nil {
			return e
		}
		procs = p
		return nil
	})
	if s.handleErr(w, r, err) {
		return
	}
	s.renderPage(w, r, http.StatusOK, "dashboard", "Dashboard",
		dashboardView{User: user, Open: filterStatus(procs, "open", true)})
}

// ---- discover / marketplace ----

func (s *Server) getDiscover(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	view := discoverView{Query: q, LookupAvailable: true}
	err := s.sessions.Do(ctx, func(tok string) error {
		if q == "" {
			cat, e := s.client.ListActions(ctx, tok, "", "")
			if e != nil {
				return e
			}
			view.Catalog = cat
			return nil
		}
		// Semantic search via @sys/lookup through the price-0 ephemeral session.
		out, e := s.caller.CallEphemeral(ctx, tok, "@sys/lookup", mustArgs(map[string]any{"query": q, "limit": 20}))
		if e != nil {
			return e
		}
		if out.IsSuccess() {
			view.Results = parseLookup(out.Result)
			return nil
		}
		// Degrade honestly to catalog search (R6.2.4). Surface the kernel's own
		// reason rather than guessing the cause (it may be a missing embedder,
		// or @sys/lookup failing for another reason).
		view.LookupAvailable = false
		view.Reason = out.Detail
		if view.Reason == "" {
			view.Reason = out.Message
		}
		cat, e := s.client.ListActions(ctx, tok, "", "")
		if e != nil {
			return e
		}
		view.Results = catalogAsResults(cat, q)
		return nil
	})
	if s.handleErr(w, r, err) {
		return
	}
	s.renderPage(w, r, http.StatusOK, "discover", "Discover", view)
}

// ---- action detail & run ----

func (s *Server) getAction(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()
	var view actionView
	err := s.sessions.Do(ctx, func(tok string) error {
		a, e := s.client.GetAction(ctx, tok, id)
		if e != nil {
			return e
		}
		me, e := s.client.Me(ctx, tok)
		if e != nil {
			return e
		}
		view = actionView{
			Action:  a,
			Form:    BuildForm(a.InputSchema),
			IsOwner: a.OwnerUserID == me.ID,
			IsPaid:  a.Price > 0,
		}
		if st, e := s.client.ActionStats(ctx, tok, id); e == nil {
			view.Stats, view.HasStats = st, true
		}
		if p, e := s.client.ListProcesses(ctx, tok); e == nil {
			view.Open = filterStatus(p, "open", true)
		}
		return nil
	})
	if s.handleErr(w, r, err) {
		return
	}
	s.renderPage(w, r, http.StatusOK, "action_detail", view.Action.Name, view)
}

func (s *Server) postRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()
	args, argErr := buildArgs(r)

	var view actionView
	err := s.sessions.Do(ctx, func(tok string) error {
		a, e := s.client.GetAction(ctx, tok, id)
		if e != nil {
			return e
		}
		me, e := s.client.Me(ctx, tok)
		if e != nil {
			return e
		}
		view = actionView{Action: a, Form: BuildForm(a.InputSchema), IsOwner: a.OwnerUserID == me.ID, IsPaid: a.Price > 0}
		if argErr == nil {
			out, e := s.runCall(ctx, tok, a, r, args)
			if e != nil {
				return e
			}
			view.Outcome = &out
		}
		if p, e := s.client.ListProcesses(ctx, tok); e == nil {
			view.Open = filterStatus(p, "open", true)
		}
		return nil
	})
	// The Run panel submits via htmx and swaps in just the outcome fragment so a
	// long call (e.g. chat) shows a progress bar instead of blocking a full-page
	// reload (R8.2). Without JS the form posts normally and we re-render the page.
	if err != nil {
		if isAuthErr(err) {
			_ = s.sessions.Logout(ctx)
			if isHTMX(r) {
				w.Header().Set("HX-Redirect", "/login")
				return
			}
			s.flashRedirect(w, r, "/login", "Your session has expired. Please sign in again.")
			return
		}
		s.log.Warn("run failed", "request_id", client.RequestIDFromContext(ctx), "err", err)
		if isHTMX(r) {
			_ = s.render.Fragment(w, http.StatusOK, "_error", userMessage(err))
			return
		}
		s.renderPage(w, r, statusFor(err), "error", "Error", errorView{Message: userMessage(err)})
		return
	}
	if argErr != nil {
		view.Error = "Invalid JSON in arguments: " + argErr.Error()
	}
	if isHTMX(r) {
		if view.Error != "" || view.Outcome == nil {
			_ = s.render.Fragment(w, http.StatusOK, "_error", orDefaultStr(view.Error, "The call could not be completed."))
			return
		}
		_ = s.render.Fragment(w, http.StatusOK, "_call_outcome", *view.Outcome)
		return
	}
	s.renderPage(w, r, http.StatusOK, "action_detail", view.Action.Name, view)
}

// isHTMX reports whether the request came from htmx (so we can return a fragment
// instead of a full page).
func isHTMX(r *http.Request) bool { return r.Header.Get("HX-Request") == "true" }

func orDefaultStr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// runCall executes the action through the funded-session abstraction, opening a
// new session for paid actions when the user did not pick an existing one.
func (s *Server) runCall(ctx context.Context, tok string, a client.Action, r *http.Request, args json.RawMessage) (Outcome, error) {
	if a.Price <= 0 {
		return s.caller.CallEphemeral(ctx, tok, a.Action, args)
	}
	pid := strings.TrimSpace(r.FormValue("process_id"))
	if pid == "" {
		p, err := s.client.StartProcess(ctx, tok, parseInt64(r.FormValue("new_funds")))
		if err != nil {
			return Outcome{}, err
		}
		pid = p.Ident()
	}
	return s.caller.CallFunded(ctx, tok, pid, a.Action, args)
}

// ---- studio ----

func (s *Server) getStudio(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var acts []client.Action
	err := s.sessions.Do(ctx, func(tok string) error {
		me, e := s.client.Me(ctx, tok)
		if e != nil {
			return e
		}
		// Owner filter returns only active+public actions (R10.3 limitation).
		a, e := s.client.ListActions(ctx, tok, me.Handle, "")
		if e != nil {
			return e
		}
		acts = a
		return nil
	})
	if s.handleErr(w, r, err) {
		return
	}
	s.renderPage(w, r, http.StatusOK, "studio", "Studio", studioView{Actions: acts})
}

func (s *Server) postCreateAction(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	in := client.CreateActionInput{
		Name:        strings.TrimSpace(r.FormValue("name")),
		Kind:        r.FormValue("kind"),
		Source:      strings.TrimSpace(r.FormValue("source")),
		Description: r.FormValue("description"),
		Price:       parseInt64(r.FormValue("price")),
	}
	for _, f := range []struct {
		key string
		dst *json.RawMessage
	}{{"input_schema", &in.InputSchema}, {"output_schema", &in.OutputSchema}} {
		raw := strings.TrimSpace(r.FormValue(f.key))
		if raw == "" {
			continue
		}
		if !json.Valid([]byte(raw)) {
			s.renderPage(w, r, http.StatusOK, "studio", "Studio", studioView{Error: "The " + f.key + " is not valid JSON."})
			return
		}
		*f.dst = json.RawMessage(raw)
	}

	var created client.Action
	err := s.sessions.Do(ctx, func(tok string) error {
		a, e := s.client.CreateAction(ctx, tok, in)
		created = a
		return e
	})
	if isAuthErr(err) {
		s.handleErr(w, r, err)
		return
	}
	if err != nil {
		s.renderPage(w, r, http.StatusOK, "studio", "Studio", studioView{Error: userMessage(err)})
		return
	}
	// Created inactive; jump to its detail page (it won't appear in the list).
	s.flashRedirect(w, r, "/actions/"+created.ID, "Action created (inactive). Enable it when ready.")
}

func (s *Server) postImport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	specURL := strings.TrimSpace(r.FormValue("spec_url"))
	var res client.ImportResult
	err := s.sessions.Do(ctx, func(tok string) error {
		v, e := s.client.ImportOpenAPI(ctx, tok, specURL)
		res = v
		return e
	})
	if s.handleErr(w, r, err) {
		return
	}
	s.flashRedirect(w, r, "/studio", fmt.Sprintf("Import complete: %d created, %d updated.", len(res.Created), len(res.Updated)))
}

func (s *Server) postActionLifecycle(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	op := lastSegment(r.URL.Path)
	ctx := r.Context()
	err := s.sessions.Do(ctx, func(tok string) error {
		switch op {
		case "enable":
			return s.client.EnableAction(ctx, tok, id)
		case "disable":
			return s.client.DisableAction(ctx, tok, id)
		case "grant-all":
			return s.client.GrantAll(ctx, tok, id)
		case "revoke-all":
			return s.client.RevokeAll(ctx, tok, id)
		}
		return nil
	})
	if s.handleErr(w, r, err) {
		return
	}
	http.Redirect(w, r, "/actions/"+id, http.StatusSeeOther)
}

func (s *Server) postActionDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()
	err := s.sessions.Do(ctx, func(tok string) error { return s.client.DeleteAction(ctx, tok, id) })
	if s.handleErr(w, r, err) {
		return
	}
	s.flashRedirect(w, r, "/studio", "Action deleted. History is preserved.")
}

func (s *Server) postACLGrant(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	subject := strings.TrimSpace(r.FormValue("subject_user_id"))
	perm := r.FormValue("permission")
	ctx := r.Context()
	err := s.sessions.Do(ctx, func(tok string) error { return s.client.GrantACL(ctx, tok, id, subject, perm) })
	if s.handleErr(w, r, err) {
		return
	}
	s.flashRedirect(w, r, "/actions/"+id, "Access granted.")
}

// ---- processes ----

func (s *Server) getProcesses(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var procs []client.Process
	err := s.sessions.Do(ctx, func(tok string) error {
		p, e := s.client.ListProcesses(ctx, tok)
		procs = p
		return e
	})
	if s.handleErr(w, r, err) {
		return
	}
	s.renderPage(w, r, http.StatusOK, "processes", "Sessions", processesView{
		Open:   filterStatus(procs, "open", true),
		Closed: filterStatus(procs, "open", false),
	})
}

func (s *Server) getProcess(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()
	var view processView
	err := s.sessions.Do(ctx, func(tok string) error {
		p, e := s.client.GetProcess(ctx, tok, id)
		if e != nil {
			return e
		}
		view.Process = p
		if txs, e := s.client.ListTransactions(ctx, tok, id); e == nil {
			view.Transactions = txs
		}
		return nil
	})
	if s.handleErr(w, r, err) {
		return
	}
	s.renderPage(w, r, http.StatusOK, "process", "Session", view)
}

func (s *Server) postFund(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()
	err := s.sessions.Do(ctx, func(tok string) error {
		_, e := s.client.FundProcess(ctx, tok, id, parseInt64(r.FormValue("funds")))
		return e
	})
	if s.handleErr(w, r, err) {
		return
	}
	http.Redirect(w, r, "/processes/"+id, http.StatusSeeOther)
}

func (s *Server) postEnd(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()
	err := s.sessions.Do(ctx, func(tok string) error { return s.client.EndProcess(ctx, tok, id) })
	if s.handleErr(w, r, err) {
		return
	}
	s.flashRedirect(w, r, "/processes", "Session ended; remaining balance reclaimed.")
}

// ---- transactions ----

func (s *Server) getTransactions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var txs []client.Transaction
	var myID string
	err := s.sessions.Do(ctx, func(tok string) error {
		me, e := s.client.Me(ctx, tok)
		if e != nil {
			return e
		}
		myID = me.ID
		t, e := s.client.ListTransactions(ctx, tok, "")
		txs = t
		return e
	})
	if s.handleErr(w, r, err) {
		return
	}
	s.renderPage(w, r, http.StatusOK, "transactions", "Transactions", transactionsView{Transactions: txs, MyID: myID})
}

func (s *Server) getTransaction(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()
	var view txView
	err := s.sessions.Do(ctx, func(tok string) error {
		tx, e := s.client.GetTransaction(ctx, tok, id)
		if e != nil {
			return e
		}
		me, e := s.client.Me(ctx, tok)
		if e != nil {
			return e
		}
		// The buyer (subject) may rate a successful transaction once; duplicate
		// and non-buyer attempts map to friendly errors at submit time (R6.6.4).
		view = txView{Tx: tx, CanRate: tx.Status == "success" && tx.SubjectUserID == me.ID}
		return nil
	})
	if s.handleErr(w, r, err) {
		return
	}
	s.renderPage(w, r, http.StatusOK, "transaction", "Transaction", view)
}

func (s *Server) postRate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()
	rating := int(parseInt64(r.FormValue("rating")))
	err := s.sessions.Do(ctx, func(tok string) error { return s.client.RateTransaction(ctx, tok, id, rating) })
	if isAuthErr(err) {
		s.handleErr(w, r, err)
		return
	}
	if err != nil {
		s.flashRedirect(w, r, "/transactions/"+id, userMessage(err))
		return
	}
	s.flashRedirect(w, r, "/transactions/"+id, "Thanks for rating.")
}

// ---- listeners & events ----

func (s *Server) getListeners(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var ls []client.Listener
	err := s.sessions.Do(ctx, func(tok string) error {
		l, e := s.client.ListListeners(ctx, tok)
		ls = l
		return e
	})
	if s.handleErr(w, r, err) {
		return
	}
	s.renderPage(w, r, http.StatusOK, "listeners", "Listeners", listenersView{Listeners: ls})
}

func (s *Server) postCreateListener(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	source := strings.TrimSpace(r.FormValue("source_user_id"))
	event := strings.TrimSpace(r.FormValue("event_name"))
	ref, ok := client.ParseActionRef(strings.TrimSpace(r.FormValue("target_action")))
	if !ok {
		s.flashRedirect(w, r, "/listeners", "Target action must be in @owner/name form.")
		return
	}
	resolved := false
	err := s.sessions.Do(ctx, func(tok string) error {
		// Resolve @owner/name -> action_id (R10.5). Works for active+public actions.
		acts, e := s.client.ListActions(ctx, tok, ref.Owner, ref.Name)
		if e != nil {
			return e
		}
		if len(acts) == 0 {
			return nil
		}
		resolved = true
		_, e = s.client.CreateListener(ctx, tok, source, event, acts[0].ID)
		return e
	})
	if s.handleErr(w, r, err) {
		return
	}
	if !resolved {
		s.flashRedirect(w, r, "/listeners", "Could not resolve the target action; it must be active and public to bind a listener.")
		return
	}
	s.flashRedirect(w, r, "/listeners", "Listener created.")
}

func (s *Server) postEmit(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	event := strings.TrimSpace(r.FormValue("event_name"))
	args, err := rawJSONOrEmpty(r.FormValue("args"))
	if err != nil {
		s.flashRedirect(w, r, "/listeners", "Arguments must be valid JSON.")
		return
	}
	var res client.EmitResult
	derr := s.sessions.Do(ctx, func(tok string) error {
		v, e := s.client.EmitEvent(ctx, tok, event, args)
		res = v
		return e
	})
	if s.handleErr(w, r, derr) {
		return
	}
	s.flashRedirect(w, r, "/listeners", fmt.Sprintf("Emitted to %d listener(s).", len(res.EventIDs)))
}

func (s *Server) getInbox(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()
	var events []client.Event
	err := s.sessions.Do(ctx, func(tok string) error {
		e, er := s.client.ListEvents(ctx, tok, id)
		events = e
		return er
	})
	view := inboxView{Events: events, CSRFField: csrf.TemplateField(r)}
	if err != nil {
		view.Error = userMessage(err)
	}
	if e := s.render.Fragment(w, http.StatusOK, "_inbox", view); e != nil {
		s.log.Error("render inbox", "err", e)
	}
}

func (s *Server) postDeleteListener(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()
	err := s.sessions.Do(ctx, func(tok string) error { return s.client.DeleteListener(ctx, tok, id) })
	if s.handleErr(w, r, err) {
		return
	}
	s.flashRedirect(w, r, "/listeners", "Listener deleted; pending events purged.")
}

func (s *Server) postConsume(w http.ResponseWriter, r *http.Request) {
	eventID := r.PathValue("id")
	ctx := r.Context()
	// Consume through the funded-session model: a zero-funded ephemeral process
	// (R6.7.3). Works for price-0 target actions; paid targets need funds.
	err := s.sessions.Do(ctx, func(tok string) error {
		p, e := s.client.StartProcess(ctx, tok, 0)
		if e != nil {
			return e
		}
		defer func() { _ = s.client.EndProcess(ctx, tok, p.Ident()) }()
		_, e = s.client.ConsumeEvent(ctx, tok, eventID, p.Ident(), "")
		return e
	})
	if isAuthErr(err) {
		s.handleErr(w, r, err)
		return
	}
	if err != nil {
		s.flashRedirect(w, r, "/listeners", userMessage(err))
		return
	}
	s.flashRedirect(w, r, "/listeners", "Event consumed.")
}

// ---- helpers ----

func filterStatus(procs []client.Process, status string, match bool) []client.Process {
	var out []client.Process
	for _, p := range procs {
		if (p.Status == status) == match {
			out = append(out, p)
		}
	}
	return out
}

func parseInt64(s string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n
}

func lastSegment(path string) string {
	path = strings.TrimRight(path, "/")
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		return path[i+1:]
	}
	return path
}

func mustArgs(m map[string]any) json.RawMessage {
	b, err := json.Marshal(m)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

func rawJSONOrEmpty(s string) (json.RawMessage, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return json.RawMessage(`{}`), nil
	}
	if !json.Valid([]byte(s)) {
		return nil, fmt.Errorf("invalid JSON")
	}
	return json.RawMessage(s), nil
}

// buildArgs assembles the call args from the run form: either the raw-JSON
// editor, or the typed fields coerced by their schema type. The kernel remains
// the validation authority (R2.5); this only shapes JSON.
func buildArgs(r *http.Request) (json.RawMessage, error) {
	if err := r.ParseForm(); err != nil {
		return nil, err
	}
	useRaw := r.FormValue("use_raw") != "" || r.FormValue("has_fields") == ""
	if useRaw {
		return rawJSONOrEmpty(r.FormValue("raw_args"))
	}
	obj := map[string]json.RawMessage{}
	for key := range r.Form {
		if !strings.HasPrefix(key, "f_") {
			continue
		}
		name := key[2:]
		val := strings.TrimSpace(r.Form.Get(key))
		if val == "" {
			continue
		}
		rv, err := coerce(r.Form.Get("t_"+name), val)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", name, err)
		}
		obj[name] = rv
	}
	return json.Marshal(obj)
}

// coerce converts a string form value to a JSON value of the field's type.
func coerce(typ, val string) (json.RawMessage, error) {
	switch typ {
	case "integer":
		if _, err := strconv.ParseInt(val, 10, 64); err != nil {
			return nil, fmt.Errorf("must be an integer")
		}
		return json.RawMessage(val), nil
	case "number":
		if _, err := strconv.ParseFloat(val, 64); err != nil {
			return nil, fmt.Errorf("must be a number")
		}
		return json.RawMessage(val), nil
	case "boolean":
		if val == "true" || val == "false" {
			return json.RawMessage(val), nil
		}
		return nil, fmt.Errorf("must be true or false")
	case "object", "array":
		if !json.Valid([]byte(val)) {
			return nil, fmt.Errorf("must be valid JSON")
		}
		return json.RawMessage(val), nil
	default: // string and unconstrained
		b, _ := json.Marshal(val)
		return b, nil
	}
}

func parseLookup(result json.RawMessage) []discoverResult {
	var lr struct {
		Results []struct {
			ActionID    string  `json:"action_id"`
			Name        string  `json:"name"`
			OwnerHandle string  `json:"owner_handle"`
			Description string  `json:"description"`
			Score       float64 `json:"score"`
		} `json:"results"`
	}
	_ = json.Unmarshal(result, &lr)
	out := make([]discoverResult, 0, len(lr.Results))
	for _, r := range lr.Results {
		out = append(out, discoverResult(r))
	}
	return out
}

// catalogAsResults adapts catalog actions to the result shape, filtering by a
// case-insensitive substring match so the fallback "search" is meaningful.
func catalogAsResults(actions []client.Action, q string) []discoverResult {
	ql := strings.ToLower(q)
	var out []discoverResult
	for _, a := range actions {
		if ql != "" && !strings.Contains(strings.ToLower(a.Name+" "+a.Description), ql) {
			continue
		}
		out = append(out, discoverResult{
			ActionID:    a.ID,
			Name:        a.Name,
			OwnerHandle: a.OwnerHandle,
			Description: a.Description,
		})
	}
	return out
}

func loginErrorMessage(err error) string {
	if ke, ok := client.AsKernelError(err); ok {
		switch ke.Kind {
		case client.KindRateLimited:
			return ke.UserMessage()
		case client.KindUnauthenticated, client.KindNotFound, client.KindInvalidInput:
			return "Invalid handle or password."
		}
	}
	return "Could not sign in. Please try again."
}

func signupErrorMessage(err error) string {
	if ke, ok := client.AsKernelError(err); ok {
		if ke.Kind == client.KindRateLimited {
			return "Too many sign-up attempts from your network. Please wait a moment and try again."
		}
		return ke.UserMessage()
	}
	return "Could not create the account. Please try again."
}
