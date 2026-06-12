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
	Outcome  *Outcome
	Error    string
}

type studioView struct {
	User   client.User
	Cards  []myActionCardView
	Error  string
	Notice string
}

type myActionCardView struct {
	Action    client.Action
	CSRFField template.HTML
}

type makeResultView struct {
	Success     bool
	ActionRef   string   // "@owner/name" on success
	ActionID    string   // UUID on success, for link
	Diagnostics []string // synthesis-failure diagnostics
	Error       string   // kernel or validation error
}

type processesView struct{ Open, Closed []client.Process }

type processView struct {
	Process      client.Process
	Transactions []client.Transaction
}

type transactionsView struct {
	Transactions []client.Transaction
	Actions      map[string]client.Action // action_id → Action; missing if deleted
	MyID         string
}

type txView struct {
	Tx      client.Transaction
	Action  *client.Action // nil if action not found (soft-deleted)
	Me      client.User
	CanRate bool
	Error   string
}

type stepEntry struct {
	Step client.Step
	Form StepFormModel
}

type stepsView struct {
	MyID    string
	Inbox   []stepEntry          // waiting steps that require this user
	History []client.Step        // completed/running/other steps
	Actions map[string]client.Action // next_action_id → action, for name display
	Error   string
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
	err := s.sessions.Do(ctx, func(tok string) error {
		u, e := s.client.Me(ctx, tok)
		user = u
		return e
	})
	if s.handleErr(w, r, err) {
		return
	}
	s.renderPage(w, r, http.StatusOK, "dashboard", "Dashboard", dashboardView{User: user})
}

// ---- discover / marketplace ----

func (s *Server) getDiscover(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	view := discoverView{Query: q, LookupAvailable: true}
	err := s.sessions.Do(ctx, func(tok string) error {
		if q == "" {
			cat, e := s.client.ListActions(ctx, "", "", "")
			if e != nil {
				return e
			}
			view.Catalog = cat
			return nil
		}
		// Semantic search via @sys/lookup (price 0; run creates and closes process).
		out, e := s.caller.Run(ctx, tok, "@sys/lookup", mustArgs(map[string]any{"query": q, "limit": 20}))
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
		cat, e := s.client.ListActions(ctx, "", "", "")
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
			out, e := s.caller.Run(ctx, tok, a.Action, args)
			if e != nil {
				return e
			}
			view.Outcome = &out
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

// ---- studio ----

func (s *Server) getStudio(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var me client.User
	var acts []client.Action
	err := s.sessions.Do(ctx, func(tok string) error {
		u, e := s.client.Me(ctx, tok)
		if e != nil {
			return e
		}
		me = u
		// Owner filter with the authenticated user's token returns all owned actions.
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
	csrfField := csrf.TemplateField(r)
	cards := make([]myActionCardView, len(acts))
	for i, a := range acts {
		cards[i] = myActionCardView{Action: a, CSRFField: csrfField}
	}
	s.renderPage(w, r, http.StatusOK, "studio", "Studio", studioView{User: me, Cards: cards})
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
			s.flashRedirect(w, r, "/studio", "The "+f.key+" is not valid JSON.")
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
		s.flashRedirect(w, r, "/studio", userMessage(err))
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

// postMakeAction calls @sys/make to synthesize a WASM action from a
// natural-language description (R6.3.6). It provisions an ephemeral 20-credit
// process, calls @sys/make, ends the process, then returns an htmx fragment
// (_make_result) or, for no-JS clients, a flash-redirect.
func (s *Server) postMakeAction(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	desc := strings.TrimSpace(r.FormValue("description"))

	var view makeResultView
	err := s.sessions.Do(ctx, func(tok string) error {
		if desc == "" {
			view = makeResultView{Error: "A description is required."}
			return nil
		}
		out, e := s.caller.Run(ctx, tok, "@sys/make", mustArgs(map[string]any{"description": desc}))
		if e != nil {
			return e
		}
		if !out.IsSuccess() {
			view = makeResultView{Error: out.Message}
			return nil
		}
		var res struct {
			Status      string   `json:"status"`
			ActionID    string   `json:"action_id"`
			ActionName  string   `json:"action_name"`
			Diagnostics []string `json:"diagnostics"`
		}
		if e := json.Unmarshal(out.Result, &res); e != nil {
			return fmt.Errorf("parse @sys/make result: %w", e)
		}
		if res.Status == "success" {
			view = makeResultView{
				Success:   true,
				ActionRef: s.sessions.Handle(ctx) + "/" + res.ActionName,
				ActionID:  res.ActionID,
			}
		} else {
			view = makeResultView{Diagnostics: res.Diagnostics}
		}
		return nil
	})
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
		s.log.Warn("make failed", "request_id", client.RequestIDFromContext(ctx), "err", err)
		view = makeResultView{Error: userMessage(err)}
	}
	if isHTMX(r) {
		_ = s.render.Fragment(w, http.StatusOK, "_make_result", view)
		return
	}
	// No-JS fallback: redirect with flash.
	if view.Success {
		s.flashRedirect(w, r, "/actions/"+view.ActionID, "Action synthesized.")
		return
	}
	msg := view.Error
	if msg == "" {
		msg = "Synthesis failed. Refine your description and try again."
	}
	s.flashRedirect(w, r, "/studio", msg)
}

func (s *Server) postActionLifecycle(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	op := lastSegment(r.URL.Path)
	ctx := r.Context()
	htmx := isHTMX(r)
	var updated client.Action
	err := s.sessions.Do(ctx, func(tok string) error {
		switch op {
		case "enable":
			if e := s.client.EnableAction(ctx, tok, id); e != nil {
				return e
			}
		case "disable":
			if e := s.client.DisableAction(ctx, tok, id); e != nil {
				return e
			}
		case "make-public":
			pub := true
			a, e := s.client.UpdateAction(ctx, tok, id, client.UpdateActionInput{Public: &pub})
			updated = a
			return e
		case "make-private":
			pub := false
			a, e := s.client.UpdateAction(ctx, tok, id, client.UpdateActionInput{Public: &pub})
			updated = a
			return e
		}
		if htmx {
			a, e := s.client.GetAction(ctx, tok, id)
			updated = a
			return e
		}
		return nil
	})
	if err != nil {
		if isAuthErr(err) {
			_ = s.sessions.Logout(ctx)
			if htmx {
				w.Header().Set("HX-Redirect", "/login")
				return
			}
			s.flashRedirect(w, r, "/login", "Your session has expired. Please sign in again.")
			return
		}
		if htmx {
			_ = s.render.Fragment(w, http.StatusOK, "_error", userMessage(err))
			return
		}
		s.handleErr(w, r, err)
		return
	}
	if htmx {
		_ = s.render.Fragment(w, http.StatusOK, "_my_action_card", myActionCardView{
			Action:    updated,
			CSRFField: csrf.TemplateField(r),
		})
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
	actions := map[string]client.Action{}
	var myID string
	err := s.sessions.Do(ctx, func(tok string) error {
		me, e := s.client.Me(ctx, tok)
		if e != nil {
			return e
		}
		myID = me.ID
		t, e := s.client.ListTransactions(ctx, tok, "")
		if e != nil {
			return e
		}
		txs = t
		// Fetch each unique action once; skip silently on 404 (deleted action).
		seen := map[string]bool{}
		for _, tx := range txs {
			if tx.ActionID == "" || seen[tx.ActionID] {
				continue
			}
			seen[tx.ActionID] = true
			if a, ae := s.client.GetAction(ctx, tok, tx.ActionID); ae == nil {
				actions[tx.ActionID] = a
			}
		}
		return nil
	})
	if s.handleErr(w, r, err) {
		return
	}
	s.renderPage(w, r, http.StatusOK, "transactions", "Transactions", transactionsView{Transactions: txs, Actions: actions, MyID: myID})
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
		// The buyer (process owner) may rate a successful, unrated transaction (R6.6.4).
		view = txView{Tx: tx, Me: me, CanRate: tx.Status == "success" && tx.OwnerUserID == me.ID && tx.Rating == nil}
		// Fetch the action; nil on 404 (soft-deleted action).
		if a, ae := s.client.GetAction(ctx, tok, tx.ActionID); ae == nil {
			view.Action = &a
		}
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
	note := strings.TrimSpace(r.FormValue("note"))
	err := s.sessions.Do(ctx, func(tok string) error { return s.client.RateTransaction(ctx, tok, id, rating, note) })
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

// ---- steps ----

func (s *Server) getSteps(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var myID string
	var inbox []stepEntry
	var history []client.Step
	actions := map[string]client.Action{}
	err := s.sessions.Do(ctx, func(tok string) error {
		me, e := s.client.Me(ctx, tok)
		if e != nil {
			return e
		}
		myID = me.ID
		steps, e := s.client.ListSteps(ctx, tok, "", "")
		if e != nil {
			return e
		}
		seen := map[string]bool{}
		for _, step := range steps {
			if step.NextActionID != "" && !seen[step.NextActionID] {
				seen[step.NextActionID] = true
				if a, ae := s.client.GetAction(ctx, tok, step.NextActionID); ae == nil {
					actions[step.NextActionID] = a
				}
			}
			if step.Status == "waiting" && step.RequiredCallerUserID == myID {
				inbox = append(inbox, stepEntry{
					Step: step,
					Form: BuildStepForm(step.InputSchema, step.PartialArgs),
				})
			} else {
				history = append(history, step)
			}
		}
		return nil
	})
	if s.handleErr(w, r, err) {
		return
	}
	s.renderPage(w, r, http.StatusOK, "steps", "Steps", stepsView{
		MyID:    myID,
		Inbox:   inbox,
		History: history,
		Actions: actions,
	})
}

func (s *Server) postCreateStep(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	partialArgs, err := rawJSONOrEmpty(r.FormValue("partial_args"))
	if err != nil {
		s.flashRedirect(w, r, "/steps", "Partial args must be valid JSON.")
		return
	}
	inputSchema, err := rawJSONOrEmpty(r.FormValue("input_schema"))
	if err != nil {
		s.flashRedirect(w, r, "/steps", "Input schema must be valid JSON.")
		return
	}
	in := client.CreateStepInput{
		ProcessID:      strings.TrimSpace(r.FormValue("process_id")),
		Action:         strings.TrimSpace(r.FormValue("action")),
		PartialArgs:    partialArgs,
		InputSchema:    inputSchema,
		RequiredCaller: strings.TrimSpace(r.FormValue("required_caller")),
	}
	if pt := strings.TrimSpace(r.FormValue("parent_trace_id")); pt != "" {
		in.ParentTraceID = pt
	}
	err = s.sessions.Do(ctx, func(tok string) error {
		_, e := s.client.CreateStep(ctx, tok, in)
		return e
	})
	if s.handleErr(w, r, err) {
		return
	}
	s.flashRedirect(w, r, "/steps", "Step created.")
}

func (s *Server) postCompleteStep(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()
	args, argErr := buildArgs(r)

	var outcome Outcome
	err := s.sessions.Do(ctx, func(tok string) error {
		if argErr != nil {
			return nil // handled after sessions.Do
		}
		out, e := s.caller.CompleteStep(ctx, tok, id, args)
		if e != nil {
			return e
		}
		outcome = out
		return nil
	})
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
		if isHTMX(r) {
			_ = s.render.Fragment(w, http.StatusOK, "_error", userMessage(err))
			return
		}
		s.flashRedirect(w, r, "/steps", userMessage(err))
		return
	}
	if argErr != nil {
		msg := "Invalid arguments: " + argErr.Error()
		if isHTMX(r) {
			_ = s.render.Fragment(w, http.StatusOK, "_error", msg)
			return
		}
		s.flashRedirect(w, r, "/steps", msg)
		return
	}
	if isHTMX(r) {
		_ = s.render.Fragment(w, http.StatusOK, "_call_outcome", outcome)
		return
	}
	s.flashRedirect(w, r, "/steps", "Step completed.")
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
