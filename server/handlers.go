package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/daios-ai/juice-ui/client"
	"github.com/gorilla/csrf"
)

// ---- view models ----

type loginView struct{ Error, Notice string }

type walletView struct {
	User          client.User
	Transactions  []client.Transaction
	OpenProcesses []client.Process
}

type studioEarnings struct {
	Gross int64
	Fee   int64
	Net   int64
	Count int
}

type actionCardView struct {
	Action client.Action
	Stats  *client.Stats
}

type discoverView struct {
	Query           string
	LookupAvailable bool
	Reason          string // why semantic search fell back (kernel's own message)
	Results         []actionCardView
	Catalog         []actionCardView
}

type actionView struct {
	Action   client.Action
	HasStats bool
	Stats    client.Stats
	Ratings  []client.Rating
	Form     FormModel
	IsOwner  bool
	IsPaid   bool
	Outcome  *Outcome
	Error    string
}

type studioView struct {
	User     client.User
	Cards    []actionCardView
	Earnings studioEarnings
	Filter   string // "active" | "inactive" | "" (all)
	Error    string
	Notice   string
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
	Tx         client.Transaction
	Action     *client.Action // nil if action not found (soft-deleted)
	Me         client.User
	CanRate    bool
	Receipt    *client.ReceiptVerification
	ReceiptMsg string
	Error      string
}

type stepEntry struct {
	Step client.Step
	Form StepFormModel
}

type stepsView struct {
	MyID    string
	Inbox   []stepEntry              // waiting steps that require this user
	History []client.Step            // completed/running/other steps
	Actions map[string]client.Action // action_id → action, for name display
	Error   string
}

type errorView struct{ Message string }

type settingsView struct {
	User   client.User
	Error  string
	Notice string
}

// ---- auth ----

func (s *Server) getLogin(w http.ResponseWriter, r *http.Request) {
	if s.sessions.IsAuthenticated(r.Context()) {
		http.Redirect(w, r, "/run", http.StatusSeeOther)
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
	http.Redirect(w, r, "/run", http.StatusSeeOther)
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
	http.Redirect(w, r, "/run", http.StatusSeeOther)
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

// ---- run room (discover / marketplace) ----

func (s *Server) getRunRoom(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	view := discoverView{Query: q, LookupAvailable: true}
	err := s.sessions.Do(ctx, func(tok string) error {
		if q == "" {
			cat, e := s.client.ListActions(ctx, tok, "", "")
			if e != nil {
				return e
			}
			cards := make([]actionCardView, len(cat))
			var wg sync.WaitGroup
			for i, a := range cat {
				cards[i] = actionCardView{Action: a}
				wg.Add(1)
				go func(i int, id string) {
					defer wg.Done()
					if st, e := s.client.ActionStats(ctx, tok, id); e == nil {
						cards[i].Stats = &st
					}
				}(i, a.ID)
			}
			wg.Wait()
			view.Catalog = cards
			return nil
		}
		// Semantic search via @sys/lookup (price 0; run creates and closes process).
		out, e := s.caller.Run(ctx, tok, "@sys/lookup", mustArgs(map[string]any{"query": q, "limit": 20}))
		if e != nil {
			return e
		}
		if out.IsSuccess() {
			view.Results = parseLookup(out.Result)
		} else {
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
		}
		// Fetch stats (and full action data for partial lookup entries) concurrently.
		var wg sync.WaitGroup
		for i := range view.Results {
			wg.Add(1)
			go func(i int, id string) {
				defer wg.Done()
				if view.Results[i].Action.Kind == "" {
					if a, e := s.client.GetAction(ctx, tok, id); e == nil {
						view.Results[i].Action = a
					}
				}
				if st, e := s.client.ActionStats(ctx, tok, id); e == nil {
					view.Results[i].Stats = &st
				}
			}(i, view.Results[i].Action.ID)
		}
		wg.Wait()
		return nil
	})
	if s.handleErr(w, r, err) {
		return
	}
	s.renderPage(w, r, http.StatusOK, "run", "Run", view)
}

// ---- settings ----

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var view settingsView
	err := s.sessions.Do(ctx, func(tok string) error {
		u, e := s.client.Me(ctx, tok)
		if e != nil {
			return e
		}
		view.User = u
		return nil
	})
	if s.handleErr(w, r, err) {
		return
	}
	s.renderPage(w, r, http.StatusOK, "settings", "Settings", view, view.User.Available)
}

func (s *Server) postSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	email := strings.TrimSpace(r.FormValue("email"))
	currentPw := r.FormValue("current_password")
	newPw := r.FormValue("password")

	inp := client.UpdateMeInput{Email: email, CurrentPassword: currentPw, Password: newPw}
	if inp.Email == "" && inp.Password == "" {
		s.flashRedirect(w, r, "/settings", "info", "Nothing to update.")
		return
	}

	passwordChange := currentPw != ""
	var apiErr error
	var updated client.User
	outerErr := s.sessions.Do(ctx, func(tok string) error {
		u, e := s.client.UpdateMe(ctx, tok, inp)
		if e != nil {
			if passwordChange {
				apiErr = e
				return nil
			}
			return e
		}
		updated = u
		return nil
	})
	if isAuthErr(outerErr) {
		s.handleErr(w, r, outerErr)
		return
	}
	if outerErr != nil {
		apiErr = outerErr
	}
	if apiErr != nil {
		msg := userMessage(apiErr)
		if ke, ok := client.AsKernelError(apiErr); ok && ke.Kind == client.KindUnauthenticated {
			msg = "Current password is incorrect."
		}
		var view settingsView
		view.Error = msg
		_ = s.sessions.Do(ctx, func(tok string) error {
			view.User, _ = s.client.Me(ctx, tok)
			return nil
		})
		s.renderPage(w, r, http.StatusOK, "settings", "Settings", view, view.User.Available)
		return
	}

	notice := "Settings updated."
	if email != "" && newPw == "" {
		notice = "Email updated."
	} else if newPw != "" {
		notice = "Password changed."
	}
	_ = updated
	s.flashRedirect(w, r, "/settings", "success", notice)
}

// ---- wallet ----

func (s *Server) getWallet(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var view walletView
	err := s.sessions.Do(ctx, func(tok string) error {
		u, e := s.client.Me(ctx, tok)
		if e != nil {
			return e
		}
		view.User = u
		txs, _ := s.client.ListTransactions(ctx, tok, "")
		view.Transactions = txs
		procs, _ := s.client.ListProcesses(ctx, tok)
		view.OpenProcesses = filterStatus(procs, "open", true)
		return nil
	})
	if s.handleErr(w, r, err) {
		return
	}
	s.renderPage(w, r, http.StatusOK, "wallet", "Wallet", view, view.User.Available)
}

// ---- inbox ----

func (s *Server) getInbox(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var myID string
	var myAvailable int64
	var inbox []stepEntry
	var history []client.Step
	actions := map[string]client.Action{}
	err := s.sessions.Do(ctx, func(tok string) error {
		me, e := s.client.Me(ctx, tok)
		if e != nil {
			return e
		}
		myID = me.ID
		myAvailable = me.Available
		steps, e := s.client.ListSteps(ctx, tok, "", "")
		if e != nil {
			return e
		}
		seen := map[string]bool{}
		for _, step := range steps {
			if step.ActionID != "" && !seen[step.ActionID] {
				seen[step.ActionID] = true
				if a, ae := s.client.GetAction(ctx, tok, step.ActionID); ae == nil {
					actions[step.ActionID] = a
				}
			}
			if step.Status == "waiting" && step.RequiredCallerUserID == myID {
				inbox = append(inbox, stepEntry{
					Step: step,
					Form: BuildStepForm(actions[step.ActionID].InputSchema, step.PartialArgs),
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
	s.renderPage(w, r, http.StatusOK, "inbox", "Inbox", stepsView{
		MyID:    myID,
		Inbox:   inbox,
		History: history,
		Actions: actions,
	}, myAvailable)
}

const (
	sessionKeyBadgeCount = "inbox.badge.count"
	sessionKeyBadgeAt    = "inbox.badge.at"
	badgeCacheTTL        = 30 * time.Second
)

func (s *Server) getInboxBadge(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sm := s.sessions.SCS()

	// Use int64 (Unix nanos) for the timestamp — gob can encode basic types
	// without registration; time.Time stored as interface{} would need gob.Register.
	cachedAtNano := sm.GetInt64(ctx, sessionKeyBadgeAt)
	count := sm.GetInt(ctx, sessionKeyBadgeCount)
	if time.Since(time.Unix(0, cachedAtNano)) > badgeCacheTTL {
		_ = s.sessions.Do(ctx, func(tok string) error {
			me, e := s.client.Me(ctx, tok)
			if e != nil {
				return e
			}
			steps, e := s.client.ListSteps(ctx, tok, "", "")
			if e != nil {
				return e
			}
			n := 0
			for _, st := range steps {
				if st.Status == "waiting" && st.RequiredCallerUserID == me.ID {
					n++
				}
			}
			count = n
			sm.Put(ctx, sessionKeyBadgeCount, n)
			sm.Put(ctx, sessionKeyBadgeAt, time.Now().UnixNano())
			return nil
		})
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if count > 0 {
		fmt.Fprintf(w, "%d", count)
	}
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
		if ratings, e := s.client.ActionRatings(ctx, tok, id); e == nil {
			view.Ratings = ratings
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
			out, e := s.caller.Run(ctx, tok, a.Ref(), args)
			if e != nil {
				return e
			}
			view.Outcome = &out
			view.Outcome.CSRFField = csrf.TemplateField(r)
		}
		return nil
	})
	// The Run panel submits via htmx and swaps in just the outcome fragment so a
	// long call (e.g. chat) shows a progress bar instead of blocking a full-page
	// reload (R8.2). Without JS the form posts normally and we re-render the page.
	if s.handleErrFragment(w, r, err) {
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
	filter := r.URL.Query().Get("filter") // "active" | "inactive" | "" (all)
	var me client.User
	var acts []client.Action
	var txs []client.Transaction
	actionStats := map[string]client.Stats{}
	err := s.sessions.Do(ctx, func(tok string) error {
		u, e := s.client.Me(ctx, tok)
		if e != nil {
			return e
		}
		me = u
		a, e := s.client.ListActions(ctx, tok, me.Handle, "")
		if e != nil {
			return e
		}
		acts = a
		txs, _ = s.client.ListTransactions(ctx, tok, "")
		for _, action := range acts {
			if st, e := s.client.ActionStats(ctx, tok, action.ID); e == nil {
				actionStats[action.ID] = st
			}
		}
		return nil
	})
	if s.handleErr(w, r, err) {
		return
	}
	var earn studioEarnings
	for _, tx := range txs {
		if tx.TargetUserID == me.ID && tx.Status == "success" {
			earn.Gross += tx.Gross
			earn.Fee += tx.Fee
			earn.Net += tx.Net
			earn.Count++
		}
	}
	var cards []actionCardView
	for _, a := range acts {
		if filter == "active" && !a.Active {
			continue
		}
		if filter == "inactive" && a.Active {
			continue
		}
		var st *client.Stats
		if sv, ok := actionStats[a.ID]; ok && (sv.Uses > 0 || sv.RatingCount > 0) {
			cp := sv
			st = &cp
		}
		cards = append(cards, actionCardView{Action: a, Stats: st})
	}
	s.renderPage(w, r, http.StatusOK, "studio", "Studio", studioView{
		User: me, Cards: cards, Earnings: earn, Filter: filter,
	}, me.Available)
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
	if in.Kind == "http" {
		in.Method = strings.TrimSpace(r.FormValue("method"))
		in.Params = parseHTTPParams(r)
	}
	for _, f := range []struct {
		key string
		dst *json.RawMessage
	}{{"input_schema", &in.InputSchema}, {"output_schema", &in.OutputSchema}} {
		raw, err := schemaFromForm(r, f.key)
		if err != nil {
			s.flashRedirect(w, r, "/studio", "error", err.Error())
			return
		}
		*f.dst = raw
	}

	if scheme := r.FormValue("auth_scheme"); scheme != "" {
		var authJSON []byte
		switch scheme {
		case "bearer":
			token := strings.TrimSpace(r.FormValue("auth_bearer_token"))
			authJSON, _ = json.Marshal(map[string]any{
				"scheme":  "bearer",
				"secrets": map[string]string{"token": token},
			})
		case "basic":
			authJSON, _ = json.Marshal(map[string]any{
				"scheme":  "basic",
				"secrets": map[string]string{"username": r.FormValue("auth_basic_username"), "password": r.FormValue("auth_basic_password")},
			})
		case "header":
			authJSON, _ = json.Marshal(map[string]any{
				"scheme":  "header",
				"config":  map[string]string{"name": strings.TrimSpace(r.FormValue("auth_param_name"))},
				"secrets": map[string]string{"value": r.FormValue("auth_param_value")},
			})
		case "query":
			authJSON, _ = json.Marshal(map[string]any{
				"scheme":  "query",
				"config":  map[string]string{"name": strings.TrimSpace(r.FormValue("auth_param_name"))},
				"secrets": map[string]string{"value": r.FormValue("auth_param_value")},
			})
		}
		if len(authJSON) > 0 {
			in.Auth = json.RawMessage(authJSON)
		}
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
		s.flashRedirect(w, r, "/studio", "error", userMessage(err))
		return
	}
	s.flashRedirect(w, r, "/studio", "success", "Action '"+created.Name+"' created (inactive). Enable it when ready.")
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
	s.flashRedirect(w, r, "/studio", "success", fmt.Sprintf("Import complete: %d created, %d updated.", len(res.Created), len(res.Updated)))
}

func (s *Server) postUnimport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	specURL := strings.TrimSpace(r.FormValue("spec_url"))
	var deactivated []client.Action
	err := s.sessions.Do(ctx, func(tok string) error {
		a, e := s.client.UnimportOpenAPI(ctx, tok, specURL, "")
		deactivated = a
		return e
	})
	if s.handleErr(w, r, err) {
		return
	}
	s.flashRedirect(w, r, "/studio", "success", fmt.Sprintf("Unimport complete: %d action(s) deactivated. History is preserved.", len(deactivated)))
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
		me, e := s.client.Me(ctx, tok)
		if e != nil {
			return e
		}
		if me.Available < 20 {
			view = makeResultView{Error: fmt.Sprintf("You need at least 20 credits to synthesize an action. Your balance is %s ¤.", money(me.Available))}
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
			ActionName  string   `json:"action_name"` // bare name; kernel does NOT return a full ref
			Diagnostics []string `json:"diagnostics"`
		}
		if e := json.Unmarshal(out.Result, &res); e != nil {
			return fmt.Errorf("parse @sys/make result: %w", e)
		}
		if res.Status == "success" {
			// action_name is the bare name ("echo-test"), not a full ref; prefix
			// the signed-in owner's handle to form the callable "@owner/name".
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
	if s.handleErrFragment(w, r, err) {
		return
	}
	if isHTMX(r) {
		_ = s.render.Fragment(w, http.StatusOK, "_make_result", view)
		return
	}
	// No-JS fallback: redirect with flash.
	if view.Success {
		s.flashRedirect(w, r, "/actions/"+view.ActionID, "success", "Action synthesized.")
		return
	}
	msg := view.Error
	if msg == "" {
		msg = "Synthesis failed. Refine your description and try again."
	}
	s.flashRedirect(w, r, "/studio", "error", msg)
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
		case "make-public":
			pub := true
			_, e := s.client.UpdateAction(ctx, tok, id, client.UpdateActionInput{Public: &pub})
			return e
		case "make-private":
			pub := false
			_, e := s.client.UpdateAction(ctx, tok, id, client.UpdateActionInput{Public: &pub})
			return e
		}
		return nil
	})
	if err != nil {
		if isAuthErr(err) {
			_ = s.sessions.Logout(ctx)
			s.flashRedirect(w, r, "/login", "error", "Your session has expired. Please sign in again.")
			return
		}
		s.handleErr(w, r, err)
		return
	}
	http.Redirect(w, r, "/actions/"+id, http.StatusSeeOther)
}

func (s *Server) postUpdateActionPrice(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()
	price := parseInt64(r.FormValue("price"))
	if price < 0 {
		s.flashRedirect(w, r, "/actions/"+id, "error", "Price must be non-negative.")
		return
	}
	err := s.sessions.Do(ctx, func(tok string) error {
		_, e := s.client.UpdateAction(ctx, tok, id, client.UpdateActionInput{Price: &price})
		return e
	})
	if err != nil {
		s.flashRedirect(w, r, "/actions/"+id, "error", userMessage(err))
		return
	}
	s.flashRedirect(w, r, "/actions/"+id, "success", "Price updated. Action is now inactive.")
}

// schemaFromForm reads and validates a JSON-schema form field. An empty field
// is valid and returns nil (meaning "leave unset"); invalid JSON returns an
// error. Shared by action create and update so the rule is defined once.
func schemaFromForm(r *http.Request, key string) (json.RawMessage, error) {
	raw := strings.TrimSpace(r.FormValue(key))
	if raw == "" {
		return nil, nil
	}
	if !json.Valid([]byte(raw)) {
		return nil, fmt.Errorf("The %s is not valid JSON.", key)
	}
	return json.RawMessage(raw), nil
}

// parseHTTPParams reads the repeated param_name/param_in form rows into ordered
// {name, in} bindings. Rows are paired by index (the http-only section enables
// its inputs as a unit, so the two slices stay aligned); blank-name rows are
// skipped and an unset location defaults to body. No rows means implicit routing.
func parseHTTPParams(r *http.Request) []client.HTTPParam {
	names := r.Form["param_name"]
	ins := r.Form["param_in"]
	var params []client.HTTPParam
	for i, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		loc := "body"
		if i < len(ins) && ins[i] != "" {
			loc = ins[i]
		}
		params = append(params, client.HTTPParam{Name: n, In: loc})
	}
	return params
}

func (s *Server) postUpdateAction(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()
	if err := r.ParseForm(); err != nil {
		s.flashRedirect(w, r, "/actions/"+id, "error", "Could not read the form.")
		return
	}
	desc := r.FormValue("description")
	in := client.UpdateActionInput{Description: &desc}
	for _, f := range []struct {
		key string
		dst *json.RawMessage
	}{{"input_schema", &in.InputSchema}, {"output_schema", &in.OutputSchema}} {
		raw, err := schemaFromForm(r, f.key)
		if err != nil {
			s.flashRedirect(w, r, "/actions/"+id, "error", err.Error())
			return
		}
		*f.dst = raw
	}
	// Source/method/params come only from the http subsection, which renders the
	// method select. Absent it (e.g. a wasm action), leave the request untouched.
	if r.PostForm.Has("method") {
		source := strings.TrimSpace(r.FormValue("source"))
		in.Source = &source
		in.Method = strings.TrimSpace(r.FormValue("method"))
		in.Params = parseHTTPParams(r)
	}
	err := s.sessions.Do(ctx, func(tok string) error {
		_, e := s.client.UpdateAction(ctx, tok, id, in)
		return e
	})
	if err != nil {
		if isAuthErr(err) {
			s.handleErr(w, r, err)
			return
		}
		s.flashRedirect(w, r, "/actions/"+id, "error", userMessage(err))
		return
	}
	s.flashRedirect(w, r, "/actions/"+id, "success", "Action updated. It is now inactive.")
}

func (s *Server) postActionDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()
	err := s.sessions.Do(ctx, func(tok string) error { return s.client.DeleteAction(ctx, tok, id) })
	if s.handleErr(w, r, err) {
		return
	}
	s.flashRedirect(w, r, "/studio", "success", "Action deleted. History is preserved.")
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
	s.flashRedirect(w, r, "/processes", "success", "Session ended; remaining balance reclaimed.")
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
		if receipt, re := s.client.ReceiptVerification(ctx, tok, id); re == nil {
			view.Receipt = &receipt
		} else if kerr, ok := re.(*client.KernelError); ok && kerr.Kind == client.KindInvalidState {
			view.ReceiptMsg = "Receipt verification is not available for this local transaction."
		} else {
			view.ReceiptMsg = userMessage(re)
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
		if isHTMX(r) {
			_ = s.render.Fragment(w, http.StatusOK, "_error", userMessage(err))
			return
		}
		s.flashRedirect(w, r, "/transactions/"+id, "error", userMessage(err))
		return
	}
	if isHTMX(r) {
		_ = s.render.Fragment(w, http.StatusOK, "_rated", nil)
		return
	}
	s.flashRedirect(w, r, "/transactions/"+id, "success", "Thanks for rating.")
}

// ---- step mutation handlers ----

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
		outcome.CSRFField = csrf.TemplateField(r)
		return nil
	})
	if s.handleErrFragment(w, r, err) {
		return
	}
	if argErr != nil {
		msg := "Invalid arguments: " + argErr.Error()
		if isHTMX(r) {
			_ = s.render.Fragment(w, http.StatusOK, "_error", msg)
			return
		}
		s.flashRedirect(w, r, "/inbox", "error", msg)
		return
	}
	if isHTMX(r) {
		_ = s.render.Fragment(w, http.StatusOK, "_call_outcome", outcome)
		return
	}
	s.flashRedirect(w, r, "/inbox", "success", "Step completed.")
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

// parseLookup converts a @sys/lookup result into partial actionCardViews.
// Kind is left empty; the goroutine in getRunRoom fills it via GetAction.
func parseLookup(result json.RawMessage) []actionCardView {
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
	out := make([]actionCardView, 0, len(lr.Results))
	for _, r := range lr.Results {
		out = append(out, actionCardView{Action: client.Action{
			ID:          r.ActionID,
			Name:        r.Name,
			OwnerHandle: r.OwnerHandle,
			Description: r.Description,
		}})
	}
	return out
}

// catalogAsResults adapts catalog actions to actionCardViews, filtering by a
// case-insensitive substring match so the fallback "search" is meaningful.
func catalogAsResults(actions []client.Action, q string) []actionCardView {
	ql := strings.ToLower(q)
	var out []actionCardView
	for _, a := range actions {
		if ql != "" && !strings.Contains(strings.ToLower(a.Name+" "+a.Description), ql) {
			continue
		}
		out = append(out, actionCardView{Action: a})
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
