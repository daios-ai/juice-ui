package server

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/daios-ai/juice-ui/client"
	"github.com/gorilla/csrf"
)

// ---- view models ----

type loginView struct {
	Error, Notice string
	Tab           string // "signin" | "signup" — the active entry tab (also the no-JS ?tab= state)
}

// minPasswordLen is the minimum length the BFF enforces for a new password (the
// kernel enforces none).
const minPasswordLen = 8

// validateNewPassword returns a user-facing error message when a newly-defined
// password fails confirmation or length, or "" when it is acceptable. Used by
// both signup and the change-password flow.
func validateNewPassword(password, confirm string) string {
	if len(password) < minPasswordLen {
		return fmt.Sprintf("Password must be at least %d characters.", minPasswordLen)
	}
	if password != confirm {
		return "The passwords don't match."
	}
	return ""
}

// loginTab returns the whitelisted entry tab from the request, defaulting to signin.
func loginTab(r *http.Request) string {
	if r.URL.Query().Get("tab") == "signup" {
		return "signup"
	}
	return "signin"
}

// userView is the account room: identity, balances, and provider earnings folded
// from the user's own transactions.
type userView struct {
	User          client.User
	Transactions  []client.Transaction
	Actions       map[string]client.Action // action_id → Action for ledger refs
	MyHandle      string
	OpenProcesses []client.Process
	OpenForm      string
	Error         string
	Notice        string
}

// studioEarnings is money that came to the user, folded from their transactions:
// their net as a seller, plus the platform fee on every settled call when they are
// the superuser (@sys's tax). Mirrors the ledger's per-viewer amounts (txAmount).
type studioEarnings struct {
	Earned int64
	Count  int
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
	OpenForm        string
}

type actionView struct {
	Action    client.Action
	Stats     *client.Stats // nil until the action has usage
	Ratings   []client.Rating
	Form      FormModel
	IsOwner   bool
	IsPaid    bool
	Outcome   *Outcome
	OpenForm  string
	Error     string
	CSRFField template.HTML // carried so the polled _action_run fragment's form keeps its token
	BadgeSecs int           // poll interval for the self-terminating peer-state poll
}

// actionsView is the action room: manage your own actions, with earnings folded
// in. Discovery/browse lives in /run; Scope="all" for a superuser widens Mine to
// every owner's actions.
type actionsView struct {
	User      client.User
	Mine      []actionCardView // the signed-in user's own actions (management)
	Earnings  studioEarnings
	Filter    string // "active" | "inactive" | "" (all) — applies to Mine
	Scope     string // "" (own+public) | "all" (superuser sees every action)
	Superuser bool
	OpenForm  string
	Error     string
	Notice    string
}

type makeResultView struct {
	Success     bool
	ActionRef   string   // "@owner/name" on success
	ActionID    string   // UUID on success, for link
	Diagnostics []string // synthesis-failure diagnostics
	Error       string   // kernel or validation error
}

type processesView struct {
	Open, Closed []client.Process
	Superuser    bool
}

type processView struct {
	Process      client.Process
	Transactions []client.Transaction
	Actions      map[string]client.Action // action_id → Action; missing if deleted
	MyHandle     string
	BadgeSecs    int // poll interval for the self-terminating awaiting-receipt poll
}

type transactionsView struct {
	Transactions []client.Transaction
	Actions      map[string]client.Action // action_id → Action; missing if deleted
	MyHandle     string
	Superuser    bool
}

// actionsByID fetches each transaction's action once (deduped) so lists can
// render the full @owner/name ref and link it. Deleted actions (404) are simply
// absent from the map; the row falls back to the bare id.
func (s *Server) actionsByID(ctx context.Context, tok string, txs []client.Transaction) map[string]client.Action {
	actions := map[string]client.Action{}
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
	return actions
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

// stepGroup buckets waiting inbox steps by their creating action (created_by).
// CreatedBy "" is the unattributed bucket (manually-created steps).
type stepGroup struct {
	CreatedBy string
	Steps     []stepEntry
}

type stepsView struct {
	MyHandle    string
	InboxGroups []stepGroup              // waiting steps that require this user, grouped by created_by
	History     []client.Step            // completed/running/other steps
	Actions     map[string]client.Action // action_id → action, for name display
	Filter      string                   // status filter
	Superuser   bool
	OpenForm    string
	Error       string
}

type stepView struct {
	Step        client.Step
	Action      *client.Action
	Form        StepFormModel
	CanComplete bool
}

type errorView struct{ Message string }

// ---- auth ----

func (s *Server) getLogin(w http.ResponseWriter, r *http.Request) {
	if s.sessions.IsAuthenticated(r.Context()) {
		http.Redirect(w, r, "/run", http.StatusSeeOther)
		return
	}
	view := loginView{Tab: loginTab(r)}
	// /health is open (no token), so we can tell the user the kernel is down before
	// they even try — instead of a misleading "invalid handle or password".
	if _, err := s.client.Health(r.Context()); err != nil {
		view.Notice = "The kernel is unreachable — you can't sign in until it's back."
	}
	s.renderPage(w, r, http.StatusOK, "login", "Sign in", view)
}

func (s *Server) postLogin(w http.ResponseWriter, r *http.Request) {
	handle, password := r.FormValue("handle"), r.FormValue("password")
	if err := s.doLogin(r.Context(), handle, password); err != nil {
		s.log.Warn("login failed", "err", err)
		s.renderPage(w, r, http.StatusOK, "login", "Sign in", loginView{Tab: "signin", Error: loginErrorMessage(err)})
		return
	}
	http.Redirect(w, r, "/run", http.StatusSeeOther)
}

func (s *Server) postSignup(w http.ResponseWriter, r *http.Request) {
	handle, email, password := r.FormValue("handle"), r.FormValue("email"), r.FormValue("password")
	ctx := r.Context()
	// Validate the password locally: confirmation match and minimum length (the
	// kernel enforces neither). Stay on the create-account tab on error.
	if msg := validateNewPassword(password, r.FormValue("confirm_password")); msg != "" {
		s.renderPage(w, r, http.StatusOK, "login", "Sign in", loginView{Tab: "signup", Error: msg})
		return
	}
	if _, err := s.client.CreateUser(ctx, handle, email, password); err != nil {
		// Stay on the create-account tab so the user sees the error in context.
		s.renderPage(w, r, http.StatusOK, "login", "Sign in", loginView{Tab: "signup", Error: signupErrorMessage(err)})
		return
	}
	if err := s.doLogin(ctx, handle, password); err != nil {
		s.renderPage(w, r, http.StatusOK, "login", "Sign in", loginView{
			Tab:    "signin",
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

// ---- delegated OAuth grants ----

// postGrantStart begins the consent for an oauth_delegated action: it asks the
// kernel for the provider authorize URL, stashes the opaque state server-side,
// and redirects the browser to the provider. The provider returns to
// getGrantCallback. (Mirrors doLogin's two-step split across requests.)
func (s *Server) postGrantStart(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	selector, provider := grantTarget(r)
	if selector == "" {
		s.flashRedirect(w, r, "/user", "error", "No account to connect.")
		return
	}
	redirectURI := s.publicBaseURL(r) + "/grants/callback"
	var authorizeURL, state string
	var granted bool
	err := s.sessions.Do(ctx, func(tok string) error {
		u, st, g, e := s.client.StartGrant(ctx, tok, selector, provider, redirectURI)
		authorizeURL, state, granted = u, st, g
		return e
	})
	if s.handleErr(w, r, err) {
		return
	}
	if granted { // account already covers the group — no browser round-trip needed
		s.flashRedirect(w, r, backTo(r), "success", "Account already connected. You can run it now.")
		return
	}
	s.sessions.PutGrantState(ctx, state, selector)
	http.Redirect(w, r, authorizeURL, http.StatusSeeOther)
}

// getGrantCallback receives the provider's redirect (?code&state), verifies the
// state against the one we stored, exchanges the code for the stored grant, and
// returns the user to /user. GET is correct: an external provider can't supply a
// CSRF token, so the opaque state is the anti-forgery guard instead.
func (s *Server) getGrantCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	want, action, ok := s.sessions.TakeGrantState(ctx)
	if !ok || state == "" || state != want {
		s.flashRedirect(w, r, "/user", "error", "Couldn't verify that connection request. Please try again.")
		return
	}
	if e := r.URL.Query().Get("error"); e != "" {
		s.flashRedirect(w, r, "/user", "error", "Connection was cancelled or denied.")
		return
	}
	err := s.sessions.Do(ctx, func(tok string) error {
		_, e := s.client.CompleteGrant(ctx, tok, state, code)
		return e
	})
	if s.handleErr(w, r, err) {
		return
	}
	s.flashRedirect(w, r, "/user", "success", "Connected "+action+".")
}

// postGrantConnect attaches a static per-caller token to a delegated_bearer action
// (the non-OAuth grant path). The token is write-only: it is read from a masked
// field and never re-displayed. On success the user returns to where they were (the
// action page) so they can retry the run.
func (s *Server) postGrantConnect(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	selector, provider := grantTarget(r)
	token := strings.TrimSpace(r.FormValue("token"))
	back := backTo(r)
	if selector == "" || token == "" {
		s.flashRedirect(w, r, back, "error", "Paste an access token to connect.")
		return
	}
	err := s.sessions.Do(ctx, func(tok string) error {
		_, e := s.client.ConnectGrant(ctx, tok, selector, provider, token)
		return e
	})
	if s.handleErr(w, r, err) {
		return
	}
	s.flashRedirect(w, r, back, "success", "Account connected. You can run it now.")
}

// postGrantDisconnect revokes the caller's grants for a selector, or — when an
// `account` provider_key is posted instead — deletes that upstream account and
// cascades every grant pointing at it.
func (s *Server) postGrantDisconnect(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if account := strings.TrimSpace(r.FormValue("account")); account != "" {
		err := s.sessions.Do(ctx, func(tok string) error {
			return s.client.DeleteConnection(ctx, tok, account)
		})
		if s.handleErr(w, r, err) {
			return
		}
		s.flashRedirect(w, r, "/user", "success", "Account disconnected.")
		return
	}
	selector, _ := grantTarget(r)
	err := s.sessions.Do(ctx, func(tok string) error {
		return s.client.DeleteGrant(ctx, tok, selector)
	})
	if s.handleErr(w, r, err) {
		return
	}
	s.flashRedirect(w, r, "/user", "success", "Disconnected "+selector+".")
}

// grantConnectView drives the consent-delta page: the plan for a selector, shown as
// the upstream-account groups and the actions each will connect, so the user
// reviews the delta before consenting (kernel §14).
type grantConnectView struct {
	Selector string
	Plan     client.GrantPlan
}

// getGrantConnect renders the consent plan for a selector — the callable delegated
// actions grouped by upstream account — so the user reviews what will be connected,
// then covers each group with one gesture (a token paste or a browser consent).
func (s *Server) getGrantConnect(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	selector := strings.TrimSpace(r.URL.Query().Get("selector"))
	if selector == "" {
		s.flashRedirect(w, r, "/user", "error", "No account to connect.")
		return
	}
	var plan client.GrantPlan
	err := s.sessions.Do(ctx, func(tok string) error {
		p, e := s.client.PlanGrants(ctx, tok, selector)
		plan = p
		return e
	})
	if s.handleErr(w, r, err) {
		return
	}
	s.renderPage(w, r, http.StatusOK, "grant_connect", "Connect an account", grantConnectView{Selector: selector, Plan: plan})
}

// grantTarget reads the selector (and optional provider) a grant form posts. It
// accepts the modern `selector`+`provider` fields and falls back to the legacy
// `action` field — a full @owner/name is the degenerate single-action selector.
func grantTarget(r *http.Request) (selector, provider string) {
	selector = strings.TrimSpace(r.FormValue("selector"))
	if selector == "" {
		selector = strings.TrimSpace(r.FormValue("action"))
	}
	return selector, strings.TrimSpace(r.FormValue("provider"))
}

// backTo returns a same-origin path from the request's Referer to return to (so a
// connect made from an action page lands back there), or /user as a fallback. Only
// a same-host path is honored, never an absolute/off-site URL.
func backTo(r *http.Request) string {
	if ref, err := url.Parse(r.Header.Get("Referer")); err == nil && ref.Path != "" && (ref.Host == "" || ref.Host == r.Host) {
		return ref.Path
	}
	return "/user"
}

func (s *Server) postTheme(w http.ResponseWriter, r *http.Request) {
	theme := r.FormValue("theme")
	// An empty or unknown slug clears the cookie (defer to prefers-color-scheme);
	// a valid slug is stamped on <html data-theme> at the next render.
	maxAge := 31536000
	if !ValidTheme(theme) {
		theme = ""
		maxAge = -1
	}
	http.SetCookie(w, &http.Cookie{
		Name: "theme", Value: theme, Path: "/", MaxAge: maxAge,
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

func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	view := discoverView{Query: q, LookupAvailable: true, OpenForm: openForm(r, "run")}
	err := s.sessions.Do(ctx, func(tok string) error {
		if q == "" {
			cat, e := s.client.ListActions(ctx, tok, "", "", false)
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
			cat, e := s.client.ListActions(ctx, tok, "", "", false)
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

// postRunByRef runs an action named by "@owner/name" (the palette / run-page
// path). It resolves the action to confirm the price, then executes; the caller
// is expected to have passed through the priced confirm form.
func (s *Server) postRunByRef(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ref := strings.TrimSpace(r.FormValue("action"))
	args, argErr := buildArgs(r)
	if ref == "" {
		s.flashRedirect(w, r, "/run", "error", "No action specified.")
		return
	}
	if argErr != nil {
		s.flashRedirect(w, r, "/run", "error", "Invalid JSON in arguments: "+argErr.Error())
		return
	}
	var outcome Outcome
	err := s.sessions.Do(ctx, func(tok string) error {
		out, e := s.caller.Run(ctx, tok, ref, args)
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
	if isHTMX(r) {
		_ = s.render.Fragment(w, http.StatusOK, "_call_outcome", outcome)
		return
	}
	if outcome.TxID != "" {
		http.Redirect(w, r, "/transactions/"+outcome.TxID, http.StatusSeeOther)
		return
	}
	s.flashRedirect(w, r, "/run", "info", outcome.Message)
}

// ---- user (account + wallet) ----

func (s *Server) getUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	view := userView{OpenForm: openForm(r, "user")}
	err := s.sessions.Do(ctx, func(tok string) error {
		u, e := s.client.Me(ctx, tok)
		if e != nil {
			return e
		}
		view.User = u
		view.MyHandle = u.Handle
		// A preview only — the most recent 10; "Full transaction ledger →" links
		// to the rest. Capped kernel-side via ?limit rather than fetching all.
		view.Transactions, _ = s.client.ListTransactions(ctx, tok, "", 10)
		view.Actions = s.actionsByID(ctx, tok, view.Transactions)
		procs, _ := s.client.ListProcesses(ctx, tok)
		view.OpenProcesses = filterStatus(procs, "open", true)
		return nil
	})
	if s.handleErr(w, r, err) {
		return
	}
	s.renderPage(w, r, http.StatusOK, "user", "Account", view, view.User.Available)
}

func (s *Server) postUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	email := strings.TrimSpace(r.FormValue("email"))
	currentPw := r.FormValue("current_password")
	newPw := r.FormValue("password")

	inp := client.UpdateMeInput{Email: email, CurrentPassword: currentPw, Password: newPw}
	if inp.Email == "" && inp.Password == "" {
		s.flashRedirect(w, r, "/user", "info", "Nothing to update.")
		return
	}
	// Validate a new password (confirmation + length) before touching the kernel.
	if newPw != "" {
		if msg := validateNewPassword(newPw, r.FormValue("confirm_password")); msg != "" {
			s.flashRedirect(w, r, "/user", "error", msg)
			return
		}
	}

	passwordChange := currentPw != ""
	var apiErr error
	outerErr := s.sessions.Do(ctx, func(tok string) error {
		_, e := s.client.UpdateMe(ctx, tok, inp)
		if e != nil {
			if passwordChange {
				apiErr = e
				return nil
			}
			return e
		}
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
		s.flashRedirect(w, r, "/user", "error", msg)
		return
	}

	notice := "Account updated."
	if email != "" && newPw == "" {
		notice = "Email updated."
	} else if newPw != "" {
		notice = "Password changed."
	}
	s.flashRedirect(w, r, "/user", "success", notice)
}

// ---- steps ----

func (s *Server) getSteps(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	filter := r.URL.Query().Get("status")
	view := stepsView{Actions: map[string]client.Action{}, Filter: filter, Superuser: s.isSuperuser(r), OpenForm: openForm(r, "step")}
	var myAvailable int64
	err := s.sessions.Do(ctx, func(tok string) error {
		me, e := s.client.Me(ctx, tok)
		if e != nil {
			return e
		}
		view.MyHandle = me.Handle
		myAvailable = me.Available
		steps, e := s.client.ListSteps(ctx, tok, "", filter)
		if e != nil {
			return e
		}
		groupIdx := map[string]int{} // created_by → index in view.InboxGroups (first-seen order)
		for _, step := range steps {
			if step.ActionID != "" {
				if _, ok := view.Actions[step.ActionID]; !ok {
					if a, ae := s.client.GetAction(ctx, tok, step.ActionID); ae == nil {
						view.Actions[step.ActionID] = a
					}
				}
			}
			if step.Status == "waiting" && step.RequiredCallerHandle == me.Handle {
				entry := stepEntry{
					Step: step,
					Form: BuildStepForm(view.Actions[step.ActionID].InputSchema, step.PartialArgs),
				}
				i, ok := groupIdx[step.CreatedBy]
				if !ok {
					i = len(view.InboxGroups)
					groupIdx[step.CreatedBy] = i
					view.InboxGroups = append(view.InboxGroups, stepGroup{CreatedBy: step.CreatedBy})
				}
				view.InboxGroups[i].Steps = append(view.InboxGroups[i].Steps, entry)
			} else {
				view.History = append(view.History, step)
			}
		}
		return nil
	})
	if s.handleErr(w, r, err) {
		return
	}
	s.renderPage(w, r, http.StatusOK, "steps", "Steps", view, myAvailable)
}

func (s *Server) getStep(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()
	var view stepView
	err := s.sessions.Do(ctx, func(tok string) error {
		st, e := s.client.GetStep(ctx, tok, id)
		if e != nil {
			return e
		}
		me, e := s.client.Me(ctx, tok)
		if e != nil {
			return e
		}
		view.Step = st
		view.CanComplete = st.Status == "waiting" && st.RequiredCallerHandle == me.Handle
		if a, ae := s.client.GetAction(ctx, tok, st.ActionID); ae == nil {
			view.Action = &a
			view.Form = BuildStepForm(a.InputSchema, st.PartialArgs)
		}
		return nil
	})
	if s.handleErr(w, r, err) {
		return
	}
	s.renderPage(w, r, http.StatusOK, "step_detail", "Step", view)
}

// postCreateStep creates a waiting step (a developer affordance): it needs the
// funding trace, next action, required caller, and any partial args.
func (s *Server) postCreateStep(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	in := client.CreateStepInput{
		TraceID:        strings.TrimSpace(r.FormValue("trace_id")),
		ActionID:       strings.TrimSpace(r.FormValue("action_id")),
		RequiredCaller: strings.TrimSpace(r.FormValue("required_caller")),
	}
	partial, err := rawJSONOrEmpty(r.FormValue("partial_args"))
	if err != nil {
		s.flashRedirect(w, r, "/steps", "error", "Invalid JSON in partial args.")
		return
	}
	in.PartialArgs = partial
	oErr := s.sessions.Do(ctx, func(tok string) error {
		_, e := s.client.CreateStep(ctx, tok, in)
		return e
	})
	s.flashResult(w, r, oErr, "/steps", "Step created.")
}

const (
	sessionKeyBadgeCount = "steps.badge.count"
	sessionKeyBadgeAt    = "steps.badge.at"
)

// getHealth is juice-ui's own liveness probe and the browser heartbeat target: it
// asks the kernel's open /health (no token). A plain probe gets 200 / 503; a
// heartbeat (htmx) from an authenticated session whose kernel has gone away is
// logged out with a clean full-page redirect to /login, which then reports the
// kernel is down. This is the general kernel-down handling — the badge pollers no
// longer carry it.
func (s *Server) getHealth(w http.ResponseWriter, r *http.Request) {
	if _, err := s.client.Health(r.Context()); err == nil {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
		return
	}
	if r.Header.Get("HX-Request") == "true" && s.sessions.IsAuthenticated(r.Context()) {
		_ = s.sessions.Logout(r.Context())
		w.Header().Set("HX-Redirect", "/login")
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte("kernel unreachable"))
}

func (s *Server) getStepsBadge(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sm := s.sessions.SCS()

	// Use int64 (Unix nanos) for the timestamp — gob can encode basic types
	// without registration; time.Time stored as interface{} would need gob.Register.
	cachedAtNano := sm.GetInt64(ctx, sessionKeyBadgeAt)
	count := sm.GetInt(ctx, sessionKeyBadgeCount)
	if time.Since(time.Unix(0, cachedAtNano)) > s.badgeTTL {
		_ = s.sessions.Do(ctx, func(tok string) error {
			me, e := s.client.Me(ctx, tok)
			if e != nil {
				return e
			}
			steps, e := s.client.ListSteps(ctx, tok, "", "waiting")
			if e != nil {
				return e
			}
			n := 0
			for _, st := range steps {
				if st.Status == "waiting" && st.RequiredCallerHandle == me.Handle {
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
			Action:    a,
			Form:      BuildForm(a.InputSchema),
			IsOwner:   a.OwnerHandle == me.Handle,
			IsPaid:    a.Price > 0,
			OpenForm:  openForm(r, "action-detail"),
			CSRFField: csrf.TemplateField(r),
			BadgeSecs: s.badgeSecs(),
		}
		if st, e := s.client.ActionStats(ctx, tok, id); e == nil {
			view.Stats = &st
		}
		if ratings, e := s.client.ActionRatings(ctx, tok, id); e == nil {
			view.Ratings = ratings
		}
		return nil
	})
	if s.handleErrFragment(w, r, err) {
		return
	}
	// A peer-blocked run form polls this handler; return just the run region so it
	// self-terminates (re-enables the form) once the peer recovers.
	if isHTMX(r) {
		_ = s.render.Fragment(w, http.StatusOK, "_action_run", view)
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
		view = actionView{Action: a, Form: BuildForm(a.InputSchema), IsOwner: a.OwnerHandle == me.Handle, IsPaid: a.Price > 0}
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

// getActions is the action room: manage your own actions (with earnings folded
// from your transactions) and browse the public catalog. A superuser can widen
// the management list to every action with ?scope=all.
func (s *Server) getActions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	filter := r.URL.Query().Get("filter") // "active" | "inactive" | "" (all)
	scope := r.URL.Query().Get("scope")   // "" | "all" (superuser)
	super := s.isSuperuser(r)
	if scope == "all" && !super {
		scope = ""
	}
	view := actionsView{Filter: filter, Scope: scope, Superuser: super, OpenForm: openForm(r, "action")}
	var mine []client.Action
	var txs []client.Transaction
	actionStats := map[string]client.Stats{}
	err := s.sessions.Do(ctx, func(tok string) error {
		u, e := s.client.Me(ctx, tok)
		if e != nil {
			return e
		}
		view.User = u
		ownerFilter := u.Handle
		if scope == "all" {
			ownerFilter = "" // whole catalog; superuser sees all rows
		}
		a, e := s.client.ListActions(ctx, tok, ownerFilter, "", true)
		if e != nil {
			return e
		}
		mine = a
		txs, _ = s.client.ListTransactions(ctx, tok, "", 0)
		for _, action := range mine {
			if st, e := s.client.ActionStats(ctx, tok, action.ID); e == nil {
				actionStats[action.ID] = st
			}
		}
		return nil
	})
	if s.handleErr(w, r, err) {
		return
	}
	for _, tx := range txs {
		if tx.Status != "success" {
			continue
		}
		var inflow int64
		if tx.TargetHandle == view.User.Handle {
			inflow += tx.Net // net as the seller
		}
		if super {
			inflow += tx.Fee // @sys earns the platform fee on every settled call
		}
		if inflow > 0 {
			view.Earnings.Earned += inflow
			view.Earnings.Count++
		}
	}
	for _, a := range mine {
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
		view.Mine = append(view.Mine, actionCardView{Action: a, Stats: st})
	}
	s.renderPage(w, r, http.StatusOK, "actions", "Actions", view, view.User.Available)
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
	if !s.applySchemas(w, r, "/actions", &in.InputSchema, &in.OutputSchema) {
		return
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
	s.flashResult(w, r, err, "/actions", "Action '"+created.Name+"' created (inactive). Enable it when ready.")
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
	s.flashRedirect(w, r, "/actions", "success", fmt.Sprintf("Import complete: %d created, %d updated.", len(res.Created), len(res.Updated)))
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
	s.flashRedirect(w, r, "/actions", "success", fmt.Sprintf("Unimport complete: %d action(s) deactivated. History is preserved.", len(deactivated)))
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
			view = makeResultView{Error: fmt.Sprintf("You need at least 20 credits to synthesize an action. Your balance is %s credits.", money(me.Available))}
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
	s.flashRedirect(w, r, "/actions", "error", msg)
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

// applySchemas reads the input_schema and output_schema form fields into the given
// destinations (shared by action create and update). On invalid JSON it flashes a
// form error to dest and returns false, so the caller returns.
func (s *Server) applySchemas(w http.ResponseWriter, r *http.Request, dest string, input, output *json.RawMessage) bool {
	for _, f := range []struct {
		key string
		dst *json.RawMessage
	}{{"input_schema", input}, {"output_schema", output}} {
		raw, err := schemaFromForm(r, f.key)
		if err != nil {
			s.flashRedirect(w, r, dest, "error", err.Error())
			return false
		}
		*f.dst = raw
	}
	return true
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
	if r.PostForm.Has("price") {
		p := parseInt64(r.FormValue("price"))
		if p >= 0 {
			in.Price = &p
		}
	}
	if !s.applySchemas(w, r, "/actions/"+id, &in.InputSchema, &in.OutputSchema) {
		return
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
	s.flashResult(w, r, err, "/actions/"+id, "Action updated. It is now inactive.")
}

func (s *Server) postActionDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()
	err := s.sessions.Do(ctx, func(tok string) error { return s.client.DeleteAction(ctx, tok, id) })
	if s.handleErr(w, r, err) {
		return
	}
	s.flashRedirect(w, r, "/actions", "success", "Action deleted. History is preserved.")
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
	s.renderPage(w, r, http.StatusOK, "processes", "Processes", processesView{
		Open:      filterStatus(procs, "open", true),
		Closed:    filterStatus(procs, "open", false),
		Superuser: s.isSuperuser(r),
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
		view.MyHandle = s.sessions.Handle(ctx)
		view.BadgeSecs = s.badgeSecs()
		if txs, e := s.client.ListTransactions(ctx, tok, id, 0); e == nil {
			view.Transactions = txs
			view.Actions = s.actionsByID(ctx, tok, txs)
		}
		return nil
	})
	if s.handleErrFragment(w, r, err) {
		return
	}
	// A process parked on a remote receipt polls this handler; return just the
	// awaiting-receipt region so it self-terminates when the receipt settles.
	if isHTMX(r) {
		_ = s.render.Fragment(w, http.StatusOK, "_process_await", view)
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
	var myHandle string
	err := s.sessions.Do(ctx, func(tok string) error {
		me, e := s.client.Me(ctx, tok)
		if e != nil {
			return e
		}
		myHandle = me.Handle
		t, e := s.client.ListTransactions(ctx, tok, "", 0)
		if e != nil {
			return e
		}
		txs = t
		actions = s.actionsByID(ctx, tok, txs)
		return nil
	})
	if s.handleErr(w, r, err) {
		return
	}
	s.renderPage(w, r, http.StatusOK, "transactions", "Transactions", transactionsView{
		Transactions: txs, Actions: actions, MyHandle: myHandle, Superuser: s.isSuperuser(r),
	})
}

// loadTxView fetches a transaction, the viewer, and (best-effort) the action into
// a txView — the shared basis of the transaction page and its verify fragment. The
// action is nil on 404 (a soft-deleted action). CanRate: the buyer may rate a
// successful, unrated transaction (R6.6.4).
func (s *Server) loadTxView(ctx context.Context, tok, id string) (txView, error) {
	tx, e := s.client.GetTransaction(ctx, tok, id)
	if e != nil {
		return txView{}, e
	}
	me, e := s.client.Me(ctx, tok)
	if e != nil {
		return txView{}, e
	}
	v := txView{Tx: tx, Me: me, CanRate: tx.Status == "success" && tx.OwnerHandle == me.Handle && tx.Rating == nil}
	if a, ae := s.client.GetAction(ctx, tok, tx.ActionID); ae == nil {
		v.Action = &a
	}
	return v, nil
}

func (s *Server) getTransaction(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()
	var view txView
	err := s.sessions.Do(ctx, func(tok string) error {
		v, e := s.loadTxView(ctx, tok, id)
		view = v
		return e
	})
	if s.handleErr(w, r, err) {
		return
	}
	s.renderPage(w, r, http.StatusOK, "transaction", "Transaction", view)
}

// getTxVerify runs receipt verification on demand and returns the _receipt
// fragment (htmx) or the full transaction page (no-JS) with the result attached.
func (s *Server) getTxVerify(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()
	var view txView
	err := s.sessions.Do(ctx, func(tok string) error {
		v, e := s.loadTxView(ctx, tok, id)
		if e != nil {
			return e
		}
		if receipt, re := s.client.ReceiptVerification(ctx, tok, id); re == nil {
			v.Receipt = &receipt
		} else if kerr, ok := re.(*client.KernelError); ok && kerr.Kind == client.KindInvalidState {
			v.ReceiptMsg = "Receipt verification is not available for this local transaction."
		} else {
			v.ReceiptMsg = userMessage(re)
		}
		view = v
		return nil
	})
	if s.handleErrFragment(w, r, err) {
		return
	}
	if isHTMX(r) {
		_ = s.render.Fragment(w, http.StatusOK, "_receipt", view)
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
		s.flashRedirect(w, r, "/steps", "error", msg)
		return
	}
	if isHTMX(r) {
		_ = s.render.Fragment(w, http.StatusOK, "_call_outcome", outcome)
		return
	}
	s.flashRedirect(w, r, "/steps", "success", "Step completed.")
}

// ---- helpers ----

// formWhitelist maps a page group to the modal forms it may open via ?form=.
// The no-JS fallback renders the named dialog with the [open] attribute, so the
// value is validated here before it ever reaches a template.
var formWhitelist = map[string]map[string]bool{
	"run":           {},
	"action":        {"action-create": true, "action-make": true, "action-import": true, "action-unimport": true},
	"action-detail": {"action-edit": true},
	"user":          {"user-update": true},
	"step":          {"step-create": true},
	"admin":         {"admin-deposit": true, "admin-withdraw": true, "admin-friend": true},
}

// openForm returns the requested ?form value if it is whitelisted for the page
// group, else "". With JS the anchor opens the dialog client-side and this is
// unused; without JS the handler renders the named dialog already open.
func openForm(r *http.Request, group string) string {
	name := r.URL.Query().Get("form")
	if name != "" && formWhitelist[group][name] {
		return name
	}
	return ""
}

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
		case client.KindUnavailable:
			return ke.UserMessage() // "the kernel is unreachable…" — not a credential error
		}
	}
	return "Could not sign in. Please try again."
}

func signupErrorMessage(err error) string {
	if ke, ok := client.AsKernelError(err); ok {
		if ke.Kind == client.KindRateLimited {
			return "Too many sign-up attempts from your network. Please wait a moment and try again."
		}
		// A duplicate handle/email surfaces as a raw 500 (the kernel leaks its
		// UNIQUE-constraint failure). Detect it by the message and translate to a
		// friendly line — never the run-outcome UserMessage() ("action failed /
		// refunded"), which makes no sense for account creation, and never the raw
		// constraint text.
		m := strings.ToLower(ke.KernelMessage)
		for _, hint := range []string{"constraint", "unique", "already", "exists", "duplicate", "email", "handle"} {
			if strings.Contains(m, hint) {
				return "That handle or email is already registered — try signing in, or use different details."
			}
		}
	}
	return "Could not create the account. Please check your details and try again."
}
