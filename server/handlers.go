package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/daios-ai/juice-ui/client"
	"github.com/daios-ai/juice-ui/session"
	"github.com/daios-ai/juice-ui/wallet"
	"github.com/gorilla/csrf"
	"golang.org/x/text/unicode/norm"
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

// userView is the account room: identity and connected accounts.
type userView struct {
	User     client.User
	OpenForm string
	Error    string
}

// studioEarnings is money that came to the user, folded from their transactions:
// their net as a seller, plus the platform fee on every settled call when they are
// the superuser (sys's tax). Mirrors the ledger's per-viewer amounts (txAmount).
type studioEarnings struct {
	Earned int64
	Count  int
}

type actionCardView struct {
	Action       client.Action // a detail read, so it carries the evidence block
	NeedsConnect bool          // demands a per-caller grant the viewer hasn't connected
	Earned       int64         // net earned by this action (folded from transactions), workshop table
	LastFailure  string        // reason of the latest failed call, a quiet peek
	// Discovered marks a lookup hit this kernel has only learned of through
	// gossip: no local row backs it, so it carries no experience of this kernel's
	// and its page is built from the lookup row alone (requirements.md §6.2). It
	// is decided by whether the action_id resolves locally, NOT by the ref's
	// shape — once resolved, a proxy keeps its kernel-qualified ref and gains a
	// local row with a record of its own.
	Discovered bool
	// ObservedAt is the kernel's own freshness stamp on a remote-backed row (a
	// gossip observation or a proxy's reconcile); empty on a local action.
	ObservedAt string
}

// Observed reports whether this row's terms were last verified against a remote
// home kernel, which is the only case where freshness is worth stating
// (template helper).
func (c actionCardView) Observed() bool { return c.ObservedAt != "" }

// cardOf wraps a catalog row. The catalog marks a row the kernel knows only
// through a peer's gossip (discovered: no local id, an indicative price), and
// the row renders in that form: resolve-on-open, no reputation.
func cardOf(a client.Action) actionCardView {
	return actionCardView{Action: a, Discovered: a.Discovered}
}

// ObservedAgo renders the freshness stamp as an age in words, through the same
// helper the peer roster's last-seen uses.
func (c actionCardView) ObservedAgo() string { return relTime(c.ObservedAt) }

type discoverView struct {
	Query           string
	LookupAvailable bool
	Reason          string // why semantic search fell back (kernel's own message)
	Results         []actionCardView
	Catalog         []actionCardView
	Pager           Pager // catalog paging (search results are single-page, ranked)
	// CanPropose reports that the kernel's planner is there and free, so the
	// command to ask it is offered. A kernel without a model, or one that prices
	// the planner, simply does not carry the command — an affordance that
	// cannot work, or that would spend, is not offered (§8.2).
	CanPropose bool
	// Sort is the order the rows are arranged in, and Sorts the choices offered.
	// Relevance is the kernel's own; the others arrange what it served (§6.2).
	Sort  string
	Sorts []SortTab
}

// SortTab is one choice in the sort switch: its label, its URL, and whether it
// is the current one. The tabs component renders it, as every other in-content
// list filter is rendered (grammar §5.2).
type SortTab struct {
	Label   string
	Href    string
	Current bool
}

type actionView struct {
	Action       client.Action
	Ratings      []client.Rating
	RatingsPager Pager
	Form         FormModel
	IsOwner      bool
	IsPaid       bool
	NeedsConnect bool // demands a per-caller grant the viewer hasn't connected
	FirstPayee   bool // viewer has never paid this action's owner (first-payee hint)
	Outcome      *Outcome
	OpenForm     string
	Edit         actionDraft // the edit dialog's values
	Error        string
	// Proposed marks the page as the planner's choice rather than an ordinary
	// open, and ProposedMessage carries anything the model said — which is often
	// nothing. The two are separate because a person must know they are looking
	// at a proposal whether or not the model was talkative. Model-authored prose
	// is counterparty content by construction, so it renders inert and never
	// beside the confirm (§6.2, §8.1).
	Proposed        bool
	ProposedMessage string
	CSRFField       template.HTML // carried so the polled _action_run fragment's form keeps its token
}

// actionsView is the action room: manage your own actions, with earnings folded
// in. Discovery/browse lives in /run; Scope="all" for a superuser widens Mine to
// every owner's actions.
type actionsView struct {
	User        client.User
	Mine        []actionCardView // the signed-in user's own actions (management)
	Earnings    studioEarnings
	MonthEarned int64  // net earned this calendar month (the "this month" tile)
	Filter      string // "active" | "inactive" | "" (all) — applies to Mine
	Scope       string // "" (own+public) | "all" (superuser sees every action)
	Superuser   bool
	Pager       Pager // paging over the (filtered) management table
	OpenForm    string
	Create      actionDraft // the create dialog's values
	Error       string
	Notice      string
}

type processesView struct {
	Open, Closed []client.Process
	Superuser    bool
	CSRFField    template.HTML // carried so the inline End·refund forms keep their token
	Pager        Pager
}

type processView struct {
	Process      client.Process
	Transactions []client.Transaction
	Me           string // the viewer's address
}

type transactionsView struct {
	Transactions []client.Transaction
	Me           string // the viewer's address
	Superuser    bool
	Pager        Pager
}

type txView struct {
	Tx         client.Transaction
	Me         client.User
	CanRate    bool
	Receipt    *client.ReceiptVerification
	ReceiptMsg string
	Error      string
}

// taskEntry is one task as the inbox shows it: the row, its completion form,
// and what the viewer may do with it.
type taskEntry struct {
	Task client.Task
	Form TaskFormModel
	// CanComplete: the task waits on the viewer. CanDecline: the viewer is one
	// of the two parties the kernel lets decline it — the one asked, or the one
	// who asked (requirements.md §6.3).
	CanComplete bool
	CanDecline  bool
}

// taskGroup buckets waiting inbox tasks by their creating action (created_by).
// CreatedBy "" holds the tasks whose row names no creator — those held on
// another kernel, which name their holder instead.
type taskGroup struct {
	CreatedBy string
	Tasks     []taskEntry
}

type tasksView struct {
	Me          string             // the viewer's address
	InboxGroups []taskGroup        // waiting tasks that require this user, grouped by created_by
	History     []client.Task      // every other task the viewer may see
	Requesters  []client.Requester // who has addressed tasks to the viewer, pending first
	Filter      string             // status filter
	Superuser   bool
	Error       string
}

type errorView struct{ Message string }

// ---- auth ----

func (s *Server) getLogin(w http.ResponseWriter, r *http.Request) {
	if s.sessions.IsAuthenticated(r.Context()) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	view := loginView{Tab: loginTab(r)}
	// /health is open (no token), so we can tell the user the kernel is down before
	// they even try — instead of a misleading "invalid handle or password".
	if _, err := s.client.Health(r.Context()); err != nil {
		view.Notice = "Kernel unreachable."
	}
	s.renderPage(w, r, http.StatusOK, "login", "Sign in", view)
}

func (s *Server) postLogin(w http.ResponseWriter, r *http.Request) {
	if err := s.doLogin(r.Context(), s.entryAddress(r), r.FormValue("password")); err != nil {
		s.log.Warn("login failed", "err", err)
		s.renderPage(w, r, http.StatusOK, "login", "Sign in", loginView{Tab: "signin", Error: loginErrorMessage(err)})
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) postSignup(w http.ResponseWriter, r *http.Request) {
	password := r.FormValue("password")
	ctx := r.Context()
	// Validate the password locally: confirmation match and minimum length (the
	// kernel enforces neither). Stay on the create-account tab on error.
	if msg := validateNewPassword(password, r.FormValue("confirm_password")); msg != "" {
		s.renderPage(w, r, http.StatusOK, "login", "Sign in", loginView{Tab: "signup", Error: msg})
		return
	}
	// Generate the recovery phrase locally and enrol only its derived public key
	// (§12): the phrase is the sole reset path, so a generation failure aborts
	// signup rather than creating an unrecoverable account.
	mnemonic, err := generateMnemonic()
	if err != nil {
		s.renderPage(w, r, http.StatusOK, "login", "Sign in", loginView{Tab: "signup", Error: recoverySetupFailed})
		return
	}
	recPub, err := recoveryPubKey(mnemonic)
	if err != nil {
		s.renderPage(w, r, http.StatusOK, "login", "Sign in", loginView{Tab: "signup", Error: recoverySetupFailed})
		return
	}
	if _, err := s.client.CreateUser(ctx, s.entryAddress(r), password, recPub); err != nil {
		// Stay on the create-account tab so the user sees the error in context.
		s.renderPage(w, r, http.StatusOK, "login", "Sign in", loginView{Tab: "signup", Error: signupErrorMessage(err)})
		return
	}
	// Do NOT sign in yet. The recovery phrase is shown on a standalone page
	// (unauthenticated → no app chrome); the user gains access only by signing in
	// after acknowledging it. Stash the phrase against the session cookie.
	s.sessions.PutRecoveryPhrase(ctx, mnemonic)
	http.Redirect(w, r, "/recovery-phrase", http.StatusSeeOther)
}

// recoveryPhraseView carries the freshly-minted seed phrase to its standalone page.
type recoveryPhraseView struct {
	Words  []string
	Phrase string // the words as one line, what the copy control carries
	Action string // where the acknowledgement posts; "" is the sign-up flow's own route
}

// phraseView builds the phrase page's data from the phrase as generated.
func phraseView(phrase, action string) recoveryPhraseView {
	words := strings.Fields(phrase)
	return recoveryPhraseView{Words: words, Phrase: strings.Join(words, " "), Action: action}
}

// getRecoveryPhrase shows the seed phrase on a standalone, unauthenticated page
// after signup. It peeks (the phrase survives reloads) so the user can copy it;
// a visit with none pending falls through to login.
func (s *Server) getRecoveryPhrase(w http.ResponseWriter, r *http.Request) {
	phrase, ok := s.sessions.PeekRecoveryPhrase(r.Context())
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	s.renderPage(w, r, http.StatusOK, "recovery_phrase", "Your recovery phrase", phraseView(phrase, ""))
}

// postRecoveryPhrase is the acknowledgement: the "I understand" box (natively
// required) must be present. It clears the phrase and sends the user to sign in —
// the point at which they gain access to the app.
func (s *Server) postRecoveryPhrase(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if _, ok := s.sessions.PeekRecoveryPhrase(ctx); !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if r.FormValue("understand") == "" {
		http.Redirect(w, r, "/recovery-phrase", http.StatusSeeOther) // peek re-shows it
		return
	}
	s.sessions.TakeRecoveryPhrase(ctx) // clear
	s.flashRedirect(w, r, "/login", "success", "Account created — sign in with your password.")
}

// recoverView drives the password-reset page.
type recoverView struct {
	Error, Notice string
}

func (s *Server) getRecover(w http.ResponseWriter, r *http.Request) {
	s.renderPage(w, r, http.StatusOK, "recover", "Reset password", recoverView{})
}

// postRecover resets a password from the recovery phrase: it fetches a nonce,
// signs it locally with the phrase-derived key, and completes the reset. The
// phrase never reaches the kernel — only the signature does.
func (s *Server) postRecover(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	address := s.entryAddress(r)
	phrase := strings.Join(strings.Fields(r.FormValue("phrase")), " ") // normalize spacing
	newPw := r.FormValue("password")
	fail := func(msg string) {
		s.renderPage(w, r, http.StatusOK, "recover", "Reset password", recoverView{Error: msg})
	}
	if strings.TrimSpace(r.FormValue("handle")) == "" || phrase == "" {
		fail("Enter your handle and recovery phrase.")
		return
	}
	if msg := validateNewPassword(newPw, r.FormValue("confirm_password")); msg != "" {
		fail(msg)
		return
	}
	nonce, err := s.client.RecoverStart(ctx, address)
	if err != nil {
		fail(recoverErrorMessage(err))
		return
	}
	sig, err := signRecoveryChallenge(phrase, s.kernelIdentity(ctx).NetworkFingerprint, nonce)
	if err != nil {
		fail("That recovery phrase isn't valid — check the words and spacing.")
		return
	}
	if err := s.client.RecoverComplete(ctx, address, nonce, sig, newPw); err != nil {
		fail(recoverErrorMessage(err))
		return
	}
	s.flashRedirect(w, r, "/login", "success", "Password reset — sign in with your new password.")
}

// ownerView is an address's action catalogue, shown as the same cards the
// marketplace uses. Each card self-labels its visibility (private/local/public)
// and inactive state via its chips, so the page asserts no single level. The
// kernel has no public user-profile endpoint, so there is no bio — the address
// and its catalogue are the page.
type ownerView struct {
	Address string
	Cards   []actionCardView
}

func (s *Server) getOwner(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	address := r.PathValue("address")
	// The catalog narrowed to one owner is exactly what the viewer may see of
	// theirs: on your own page everything you own, private and inactive
	// included; on anyone else's, their live actions you may call.
	view := ownerView{Address: address}
	err := s.sessions.Do(ctx, func(tok string) error {
		acts, e := s.client.ListActions(ctx, tok, address)
		if e != nil {
			return e
		}
		cards := make([]actionCardView, len(acts))
		for i, a := range acts {
			cards[i] = cardOf(a)
		}
		s.fetchEvidence(ctx, tok, cards)
		view.Cards = cards
		return nil
	})
	if s.handleErr(w, r, err) {
		return
	}
	s.renderPage(w, r, http.StatusOK, "owner", address, view)
}

// recoverErrorMessage maps a recovery failure to actionable copy without leaking
// which of handle/phrase/nonce was wrong beyond what helps the user.
func recoverErrorMessage(err error) string {
	if ke, ok := client.AsKernelError(err); ok {
		switch ke.Kind {
		case client.KindInvalidState:
			return "This account has no recovery phrase enrolled, so it can't be reset this way."
		case client.KindNotFound:
			return "No account with that handle."
		case client.KindUnauthorized, client.KindUnauthenticated:
			return "That recovery phrase didn't match this account. Check the words and try again."
		}
	}
	return userMessage(err)
}

// entryAddress is the account the entry screen names: the handle typed, on
// this kernel. The screen names the kernel it signs in to, so a person types
// only the handle; the address is composed here, once, from /health (read now
// if it has not been, since an entry form may be the first request served).
func (s *Server) entryAddress(r *http.Request) string {
	if id := s.kernelIdentity(r.Context()); id.Handle == "" {
		return ""
	}
	return s.identity.Address(strings.TrimSpace(r.FormValue("handle")))
}

// doLogin runs the PKCE authorization-code flow: the verifier is generated and
// kept server-side, the challenge is sent to authorize, the returned code is
// exchanged with the verifier, and the resulting tokens are stored in the
// session (R5.1) together with the kernel they are for (§5).
//
// The kernel is identified BEFORE the credential is sent: a login is only
// meaningful against a server we can name, and pinning it after the fact would
// record whatever answered rather than what was trusted.
func (s *Server) doLogin(ctx context.Context, address, password string) error {
	id := s.kernelIdentity(ctx)
	if id.PublicKey == "" {
		return client.ErrUnavailable
	}
	challenge, err := s.sessions.BeginPKCE(ctx)
	if err != nil {
		return err
	}
	code, err := s.client.Authorize(ctx, address, password, challenge)
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
	return s.sessions.Login(ctx, tokens, address, session.Pin{
		PublicKey:          id.PublicKey,
		NetworkFingerprint: id.NetworkFingerprint,
	})
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

// getGrantStart begins the consent for an oauth_delegated action: it asks the
// kernel for the provider authorize URL, stashes the opaque state server-side,
// and redirects the browser to the provider. The provider returns to
// getGrantCallback. A navigational GET, per the OAuth authorization request
// (RFC 6749): a form POST cannot start it, because form-action 'self' blocks a
// submission's cross-origin redirect; the flow's CSRF defense is the state
// match at the callback plus the kernel's PKCE challenge (RFC 9700), not a
// form token.
func (s *Server) getGrantStart(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	selector, provider := grantTarget(r)
	if selector == "" {
		s.flashRedirect(w, r, "/settings", "error", "No account to connect.")
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
		s.flashRedirect(w, r, "/settings", "error", "Couldn't verify that connection request. Please try again.")
		return
	}
	if e := r.URL.Query().Get("error"); e != "" {
		s.flashRedirect(w, r, "/settings", "error", "Connection was cancelled or denied.")
		return
	}
	var pending bool
	err := s.sessions.Do(ctx, func(tok string) error {
		_, p, e := s.client.CompleteGrant(ctx, tok, state, code)
		pending = p
		return e
	})
	if s.handleErr(w, r, err) {
		return
	}
	// The kernel may answer that the exchange has not completed. Saying
	// "connected" then would be a claim the next run contradicts with
	// grant_required, so the person is told where it stands instead.
	if pending {
		s.flashRedirect(w, r, "/settings", "info", "Connection to "+action+" is still pending.")
		return
	}
	s.flashRedirect(w, r, "/settings", "success", "Connected "+action+".")
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
		s.flashRedirect(w, r, "/settings", "success", "Account disconnected.")
		return
	}
	selector, _ := grantTarget(r)
	err := s.sessions.Do(ctx, func(tok string) error {
		return s.client.DeleteGrant(ctx, tok, selector)
	})
	if s.handleErr(w, r, err) {
		return
	}
	s.flashRedirect(w, r, "/settings", "success", "Disconnected "+selector+".")
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
		s.flashRedirect(w, r, "/settings", "error", "No account to connect.")
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

// grantedRefs flattens the caller's granted action refs from the /v1/me
// connectors tree into a lookup set.
func grantedRefs(me client.User) map[string]bool {
	g := map[string]bool{}
	for _, c := range me.Connectors {
		for _, a := range c.Actions {
			g[a.Action] = true
		}
	}
	return g
}

// needsConnect reports whether a run is doomed for the viewer: the action
// demands a per-caller grant they don't hold. Safe on a nil set (unknown
// grants → no flag; the run outcome still catches the 403).
func needsConnect(a client.Action, granted map[string]bool) bool {
	return a.RequiresGrant && !granted[a.Ref()]
}

// grantTarget reads the selector (and optional provider) a grant form posts. It
// accepts the modern `selector`+`provider` fields and falls back to the legacy
// `action` field — a full owner/name is the degenerate single-action selector.
func grantTarget(r *http.Request) (selector, provider string) {
	selector = strings.TrimSpace(r.FormValue("selector"))
	if selector == "" {
		selector = strings.TrimSpace(r.FormValue("action"))
	}
	return selector, strings.TrimSpace(r.FormValue("provider"))
}

// backTo returns a same-origin path (query included — the consent page is
// addressed by ?selector=…) from the request's Referer, or /settings as a fallback.
// Only a same-host path is honored, never an absolute/off-site URL.
func backTo(r *http.Request) string {
	if ref, err := url.Parse(r.Header.Get("Referer")); err == nil && ref.Path != "" && (ref.Host == "" || ref.Host == r.Host) {
		if ref.RawQuery != "" {
			return ref.Path + "?" + ref.RawQuery
		}
		return ref.Path
	}
	return "/settings"
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

func (s *Server) getBrowse(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	offset := queryOffset(r)
	order := sortOrder(r)
	view := discoverView{Query: q, LookupAvailable: true, Sort: order}
	for _, t := range []struct{ label, order string }{
		{"relevance", sortRelevance}, {"rating", sortRating}, {"price", sortPrice},
	} {
		view.Sorts = append(view.Sorts, SortTab{
			Label: t.label, Href: sortHref(r, t.order), Current: order == t.order,
		})
	}
	var granted map[string]bool
	var available int64
	haveMe := false
	err := s.sessions.Do(ctx, func(tok string) error {
		// One /v1/me read serves both the needs-account flag and the balance
		// renderPage would otherwise re-fetch. Best-effort: the page must not
		// fail for a chip.
		if me, e := s.client.Me(ctx, tok); e == nil {
			granted = grantedRefs(me)
			available, haveMe = me.Available, true
		}
		if q == "" {
			// Read the whole catalog — local actions and those discovered on
			// peers — and slice one page here, because a sort orders the whole
			// list (below).
			cat, e := s.client.ListActions(ctx, tok, "")
			if e != nil {
				return e
			}
			cards := make([]actionCardView, len(cat))
			for i, a := range cat {
				cards[i] = cardOf(a)
				cards[i].NeedsConnect = !a.Discovered && needsConnect(a, granted)
			}
			// A sort orders the WHOLE catalog, then the page is cut from it.
			// Ordering only the page would answer a different question than the
			// one asked — the best-rated action could sit on page two while page
			// one claimed to be sorted by rating — and nothing on screen would
			// say so. Rating therefore costs a detail read per row, which is why
			// it is paid only when that sort is asked for.
			if order == sortRating {
				s.fetchEvidence(ctx, tok, cards)
			}
			sortCards(cards, order)
			start, end, hasNext := pageSlice(len(cards), offset, pageSize)
			cards = cards[start:end]
			view.Pager = pageNav(r, offset, pageSize, hasNext)
			s.fetchEvidence(ctx, tok, cards)
			view.Catalog = cards
			return nil
		}
		view.CanPropose = s.nativeOffered(ctx, tok, s.native(decideNative))
		// Semantic search runs sys/lookup. A native's price is the OPERATOR's
		// configuration, not a constant (measured: a kernel booted with
		// native.lookup.price = 7 serves 7 on the row and charges it), and a
		// search is a page load — so running it unchecked would make a GET spend,
		// which §8.2 forbids outright. Read the price first and degrade instead.
		// Only the price is read: whether it works at all is discovered by
		// running it and reporting what happened, never guessed.
		priced := s.nativeIsPriced(ctx, tok, s.native(lookupNative))
		var out Outcome
		if !priced {
			o, e := s.caller.Run(ctx, tok, s.native(lookupNative), mustArgs(map[string]any{"query": q, "limit": 20}), "")
			if e != nil {
				return e
			}
			out = o
		}
		if out.IsSuccess() {
			view.Results = parseLookup(out.Result)
		} else {
			// Degrade honestly to catalog search (R6.2.4). The reason is the
			// kernel's own where it ran and failed — it may be a missing embedder,
			// or something else — and ours where we declined to run it, since the
			// kernel said nothing.
			view.LookupAvailable = false
			switch {
			case priced:
				view.Reason = "Semantic search is priced on this kernel."
			default:
				view.Reason = out.Reason()
			}
			cat, e := s.client.ListActions(ctx, tok, "")
			if e != nil {
				return e
			}
			view.Results = catalogAsResults(cat, q)
		}
		// Fetch each hit's full action row concurrently. The read is also what
		// DECIDES the card's class: a lookup row whose action_id names
		// no local action is a discovered one, rendered from the row alone. The
		// ref's shape cannot decide it — a resolved proxy keeps its kernel-
		// qualified ref while having a perfectly readable local row, and reading
		// discovery off the ref would strip a card the buyer has already paid for
		// down to a stranger's (requirements.md §6.2).
		var wg sync.WaitGroup
		for i := range view.Results {
			if view.Results[i].Discovered {
				continue // the catalog already says: no local row to read
			}
			wg.Add(1)
			go func(i int, id string) {
				defer wg.Done()
				a, e := s.client.GetAction(ctx, tok, id)
				if e != nil {
					// No local row. Another kernel's address means it is
					// discovered; a local one that fails to read is a row that has
					// since gone, and keeps the lookup row's own fields.
					view.Results[i].Discovered = !s.identity.Local(view.Results[i].Action.Action)
					return
				}
				view.Results[i].Discovered = false
				view.Results[i].Action = a
			}(i, view.Results[i].Action.ID)
		}
		wg.Wait()
		// Flag after the fetches: a partial lookup entry only carries
		// requires_grant once its full record is in. A discovered hit has no
		// local record and no local grant to want.
		for i := range view.Results {
			if view.Results[i].Discovered {
				continue
			}
			view.Results[i].NeedsConnect = needsConnect(view.Results[i].Action, granted)
		}
		// Search results are one ranked page, so they sort whole.
		sortCards(view.Results, order)
		return nil
	})
	if s.handleErr(w, r, err) {
		return
	}
	if haveMe {
		s.renderPage(w, r, http.StatusOK, "browse", "Browse", view, available)
		return
	}
	s.renderPage(w, r, http.StatusOK, "browse", "Browse", view)
}

// ---- user (account + wallet) ----

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	view := userView{OpenForm: openForm(r, "user")}
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
	desc := strings.TrimSpace(r.FormValue("description"))
	currentPw := r.FormValue("current_password")
	newPw := r.FormValue("password")

	// Description is always sent (prefilled with the current value, so an empty
	// field is a deliberate clear) — this also satisfies the kernel's "at least
	// one of description or password" rule on every save.
	inp := client.UpdateMeInput{Description: &desc, CurrentPassword: currentPw, Password: newPw}
	// Validate a new password (confirmation + length) before touching the kernel.
	if newPw != "" {
		if msg := validateNewPassword(newPw, r.FormValue("confirm_password")); msg != "" {
			s.flashRedirect(w, r, "/settings", "error", msg)
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
		s.flashRedirect(w, r, "/settings", "error", msg)
		return
	}

	notice := "Profile updated."
	if newPw != "" {
		notice = "Password changed."
	}
	s.flashRedirect(w, r, "/settings", "success", notice)
}

// ---- tasks ----

func (s *Server) getTasks(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	filter := r.URL.Query().Get("status")
	view := tasksView{Filter: filter, Superuser: s.isSuperuser(r)}
	var myAvailable int64
	err := s.sessions.Do(ctx, func(tok string) error {
		me, e := s.client.Me(ctx, tok)
		if e != nil {
			return e
		}
		view.Me = me.Address
		myAvailable = me.Available
		tasks, e := s.client.ListTasks(ctx, tok, filter)
		if e != nil {
			return e
		}
		if view.Requesters, e = s.client.ListRequesters(ctx, tok); e != nil {
			return e
		}
		rq := view.Requesters
		sort.SliceStable(rq, func(i, j int) bool {
			if ri, rj := requesterRank[rq[i].Status], requesterRank[rq[j].Status]; ri != rj {
				return ri < rj
			}
			return rq[i].Since < rq[j].Since
		})
		groupIdx := map[string]int{} // created_by → index in view.InboxGroups (first-seen order)
		for _, t := range tasks {
			if !t.Waiting() || t.RequiredCaller != me.Address {
				view.History = append(view.History, t)
				continue
			}
			entry := s.taskEntryOf(t, me.Address)
			i, ok := groupIdx[t.CreatedBy]
			if !ok {
				i = len(view.InboxGroups)
				groupIdx[t.CreatedBy] = i
				view.InboxGroups = append(view.InboxGroups, taskGroup{CreatedBy: t.CreatedBy})
			}
			view.InboxGroups[i].Tasks = append(view.InboxGroups[i].Tasks, entry)
		}
		return nil
	})
	if s.handleErr(w, r, err) {
		return
	}
	s.renderPage(w, r, http.StatusOK, "inbox", "Inbox", view, myAvailable)
}

func (s *Server) getTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()
	var view taskEntry
	err := s.sessions.Do(ctx, func(tok string) error {
		t, e := s.client.GetTask(ctx, tok, id)
		if e != nil {
			return e
		}
		me, e := s.client.Me(ctx, tok)
		if e != nil {
			return e
		}
		view = s.taskEntryOf(t, me.Address)
		return nil
	})
	if s.handleErr(w, r, err) {
		return
	}
	s.renderPage(w, r, http.StatusOK, "inbox_detail", "Task", view)
}

// taskEntryOf builds what the inbox shows for one task: the completion form
// from the task's own allowed_input (never the target action, which may be
// private), and the two gestures the viewer may make.
func (s *Server) taskEntryOf(t client.Task, me string) taskEntry {
	return taskEntry{
		Task:        t,
		Form:        BuildTaskForm(t.AllowedInput, t.PartialArgs),
		CanComplete: t.Waiting() && t.RequiredCaller == me,
		CanDecline:  t.Waiting() && (t.RequiredCaller == me || t.Owner == me),
	}
}

const (
	sessionKeyBadgeCount = "tasks.badge.count"
	sessionKeyBadgeAt    = "tasks.badge.at"
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
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte("kernel unreachable"))
}

// ---- action detail & run ----

func (s *Server) getAction(w http.ResponseWriter, r *http.Request) {
	s.renderAction(w, r, r.PathValue("id"), nil, nil)
}

// renderAction renders one action's page. proposed pre-fills the run form's
// fields and message states who proposed them; both are empty on an ordinary
// open. The two callers are the action page itself and the proposal (§6.2), and
// they differ in nothing else: a proposal is the same page, already filled in,
// and the same confirm still has to be pressed.
//
// edit is a refused edit to re-render in the open dialog; nil fills the dialog
// from the action itself.
func (s *Server) renderAction(w http.ResponseWriter, r *http.Request, id string, proposal *proposal, edit *actionDraft) {
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
			Action:       a,
			Form:         BuildForm(a.InputSchema),
			IsOwner:      refOwner(a.Action) == me.Address,
			IsPaid:       a.Price > 0,
			NeedsConnect: needsConnect(a, grantedRefs(me)),
			OpenForm:     openForm(r, "action-detail"),
			CSRFField:    csrf.TemplateField(r),
		}
		// Ratings: fetch all, slice one page here (uniform with the other lists).
		ratingsOffset := queryOffset(r)
		if all, e := s.client.ActionRatings(ctx, tok, id, 0, 0); e == nil {
			start, end, hasNext := pageSlice(len(all), ratingsOffset, pageSize)
			view.Ratings = all[start:end]
			view.RatingsPager = pageNav(r, ratingsOffset, pageSize, hasNext)
		}
		// First-payee hint on the run form (§8.3): show it unless the viewer has
		// already transacted with this action's owner. Bounded, best-effort.
		if owner := refOwner(a.Action); a.Price > 0 && owner != me.Address {
			view.FirstPayee = true
			if txs, e := s.client.ListTransactions(ctx, tok, "", 200); e == nil {
				for _, t := range txs {
					if t.Target == owner {
						view.FirstPayee = false
						break
					}
				}
			}
		}
		return nil
	})
	if s.handleErrFragment(w, r, err) {
		return
	}
	if proposal != nil {
		view.Proposed, view.ProposedMessage = true, proposal.Message
		args, _ := json.Marshal(proposal.Raws)
		view.Form.Fill(args)
		// The raw-JSON box carries the proposal too. It is the only input where
		// the schema is unusable, and it is one toggle away everywhere else — so
		// filling only the fields would drop the proposal silently for one action
		// and lose it on a switch for every other.
		if raw := proposal.Raw(); raw != "" {
			view.Form.Example = raw
		}
	}
	status := http.StatusOK
	if edit != nil {
		view.Edit, view.OpenForm, status = *edit, "action-edit", http.StatusUnprocessableEntity
	} else {
		view.Edit = s.draftOf(view.Action)
	}
	s.renderPage(w, r, status, "action_detail", view.Action.Title, view)
}

// getDiscovered opens an action this kernel has only heard of through gossip.
// It has no local row — its action_id belongs to the remote kernel — so the
// reference is RESOLVED with its home kernel, which verifies the signed contract
// and caches a proxy. What the buyer reads is then an ordinary priced action
// page quoting live terms, rather than a catalog row that may be a discovery
// interval stale.
//
// The resolve is a pure read: one round trip, and it spends nothing.
func (s *Server) getDiscovered(w http.ResponseWriter, r *http.Request) {
	ref := strings.TrimSpace(r.URL.Query().Get("ref"))
	if ref == "" {
		s.flashRedirect(w, r, "/browse", "error", "No action specified.")
		return
	}
	ctx := r.Context()
	var (
		action client.Action
		found  bool
	)
	err := s.sessions.Do(ctx, func(tok string) error {
		a, ok, e := s.client.ResolveRef(ctx, tok, ref)
		action, found = a, ok
		return e
	})
	if err != nil {
		// An unreachable or refusing home kernel is a fact about that kernel, and
		// its own sentence says so (§6.2).
		s.flashRedirect(w, r, "/browse", "error", userMessage(err))
		return
	}
	if !found {
		s.flashRedirect(w, r, "/browse", "error", "That action could not be resolved from its kernel.")
		return
	}
	http.Redirect(w, r, "/actions/"+url.PathEscape(action.ID), http.StatusSeeOther)
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
		view = actionView{Action: a, Form: BuildForm(a.InputSchema), IsOwner: refOwner(a.Action) == me.Address, IsPaid: a.Price > 0, NeedsConnect: needsConnect(a, grantedRefs(me))}
		if argErr == nil {
			// The pin comes from the REQUEST — the terms the buyer was actually
			// shown — never from the re-read above, which would always match and
			// silently disable the protection while looking correct.
			out, e := s.caller.Run(ctx, tok, a.Ref(), args, r.FormValue("quote_hash"))
			if e != nil {
				return e
			}
			// A refused quote re-renders the form pinned to the terms the kernel
			// just quoted, so accepting them is one more click, not a re-read.
			if out.Class == TermsChanged && out.QuoteHash != "" {
				view.Action.QuoteHash, view.Action.Price = out.QuoteHash, out.Price
			}
			// The provider changed the contract: the kernel says so, charged
			// nothing, and discarded the cached copy. Re-read so the form re-pins
			// to the terms that now hold and one more click accepts them. This is
			// keyed on the kernel's marker, never on the action's kind — an
			// unmarked execution_failed may have executed, and is never retried.
			if out.Class == Refreshed {
				if fresh, e := s.client.GetAction(ctx, tok, id); e == nil {
					view.Action = fresh
					view.Form = BuildForm(fresh.InputSchema)
					out.Price, out.QuoteHash = fresh.Price, fresh.QuoteHash
				}
			}
			// `run` parks exactly the action's price, so the quote the buyer was
			// shown is the amount now reserved.
			if out.Class == Parked {
				out.Reserved = a.Price
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
	view.Edit = s.draftOf(view.Action)
	s.renderPage(w, r, http.StatusOK, "action_detail", view.Action.Title, view)
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

// getWorkshop is the workshop: manage your own actions (with earnings folded
// from your transactions) and browse the public catalog. A superuser can widen
// the management list to every action with ?scope=all.
func (s *Server) getWorkshop(w http.ResponseWriter, r *http.Request) {
	s.renderWorkshop(w, r, nil)
}

// renderWorkshop renders the workshop. create is a refused create to re-render
// in the open dialog; nil renders the page as opened.
func (s *Server) renderWorkshop(w http.ResponseWriter, r *http.Request, create *actionDraft) {
	ctx := r.Context()
	filter := r.URL.Query().Get("filter") // "active" | "inactive" | "" (all)
	scope := r.URL.Query().Get("scope")   // "" | "all" (superuser)
	super := s.isSuperuser(r)
	if scope == "all" && !super {
		scope = ""
	}
	view := actionsView{Filter: filter, Scope: scope, Superuser: super, OpenForm: openForm(r, "action")}
	var mine []actionCardView
	var txs []client.Transaction
	err := s.sessions.Do(ctx, func(tok string) error {
		u, e := s.client.Me(ctx, tok)
		if e != nil {
			return e
		}
		view.User = u
		ownerFilter := u.Address
		if scope == "all" {
			ownerFilter = "" // whole catalog; superuser sees all rows
		}
		a, e := s.client.ListActions(ctx, tok, ownerFilter)
		if e != nil {
			return e
		}
		// The workshop manages this kernel's actions: a row known only from a
		// peer's gossip is not one to manage here.
		for i := range a {
			if !a[i].Discovered {
				mine = append(mine, cardOf(a[i]))
			}
		}
		s.fetchEvidence(ctx, tok, mine)
		txs, _ = s.client.ListTransactions(ctx, tok, "", 0)
		return nil
	})
	if s.handleErr(w, r, err) {
		return
	}
	// Fold transactions once: aggregate earnings (all-time + this month) and bucket
	// per-action earned + latest failure for the management table.
	monthPrefix := time.Now().UTC().Format("2006-01")
	perActionEarned := map[string]int64{}
	perActionFailure := map[string]string{} // latest failure reason (txs are newest-first)
	for _, tx := range txs {
		if tx.Status == "failure" {
			if _, seen := perActionFailure[tx.ActionID]; !seen && tx.Reason != "" {
				perActionFailure[tx.ActionID] = tx.Reason
			}
			continue
		}
		if tx.Status != "success" {
			continue
		}
		var inflow int64
		if tx.Target == view.User.Address {
			inflow += tx.Net // net as the seller
			perActionEarned[tx.ActionID] += tx.Net
		}
		if super {
			inflow += tx.Fee // sys earns the platform fee on every settled call
		}
		if inflow > 0 {
			view.Earnings.Earned += inflow
			view.Earnings.Count++
			if strings.HasPrefix(tx.StartedAt, monthPrefix) {
				view.MonthEarned += inflow
			}
		}
	}
	for _, c := range mine {
		if filter == "active" && !c.Action.Active {
			continue
		}
		if filter == "inactive" && c.Action.Active {
			continue
		}
		c.Earned = perActionEarned[c.Action.ID]
		c.LastFailure = perActionFailure[c.Action.ID]
		view.Mine = append(view.Mine, c)
	}
	// Page the (filtered) management table; earnings tiles above still reflect the
	// full fold, not the visible page.
	offset := queryOffset(r)
	start, end, hasNext := pageSlice(len(view.Mine), offset, pageSize)
	view.Mine = view.Mine[start:end]
	view.Pager = pageNav(r, offset, pageSize, hasNext)
	status := http.StatusOK
	if create != nil {
		view.Create, view.OpenForm, status = *create, "action-create", http.StatusUnprocessableEntity
	}
	s.renderPage(w, r, status, "workshop", "Workshop", view, view.User.Available)
}

func (s *Server) postCreateAction(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	d := readDraft(r)
	in, err := s.createInput(d, r)
	var created client.Action
	if err == nil {
		err = s.sessions.Do(ctx, func(tok string) error {
			a, e := s.client.CreateAction(ctx, tok, in)
			created = a
			return e
		})
	}
	switch {
	case isAuthErr(err):
		s.handleErr(w, r, err)
	case err != nil:
		d.Error = draftError(err)
		s.renderWorkshop(w, r, &d)
	default:
		s.flashRedirect(w, r, "/workshop", "success", "Action '"+created.Title+"' created (inactive). Enable it when ready.")
	}
}

func (s *Server) postImport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	name := strings.TrimSpace(r.FormValue("name"))
	specURL := strings.TrimSpace(r.FormValue("spec_url"))
	if name == "" {
		s.flashRedirect(w, r, "/workshop", "error", "Name the application to install.")
		return
	}
	// One credential configuration for the whole application, set at import. An
	// auth is write-only, so an application that needs one and does not get it
	// here cannot be given one without naming its subtree by hand afterwards.
	auth := upstreamAuth(r)
	var res client.ImportResult
	err := s.sessions.Do(ctx, func(tok string) error {
		v, e := s.client.ImportOpenAPI(ctx, tok, name, specURL, auth)
		res = v
		return e
	})
	if s.handleErr(w, r, err) {
		return
	}
	msg := fmt.Sprintf("%s: %d created, %d updated, %d unchanged, %d deactivated.",
		name, len(res.Created), len(res.Updated), len(res.Unchanged), len(res.Deactivated))
	// A rejected operation is named with the kernel's reason: silently absent, it
	// would read as a kernel fault rather than a document that cannot be installed.
	kind := "success"
	if len(res.Rejected) > 0 {
		kind = "info"
		var parts []string
		for _, rj := range res.Rejected {
			parts = append(parts, rj.Key+" ("+rj.Location+") — "+rj.Reason)
		}
		msg += " Not installed: " + strings.Join(parts, "; ") + "."
	}
	// A notice names an installed operation whose stored contract differs from
	// the document, because the kernel folded a form into its schema subset.
	if len(res.Notices) > 0 {
		var parts []string
		for _, n := range res.Notices {
			parts = append(parts, n.Key+" — "+n.Note)
		}
		msg += " Adjusted: " + strings.Join(parts, "; ") + "."
	}
	s.flashRedirect(w, r, "/workshop", kind, msg)
}

// postActionLifecycle enables, disables or re-scopes a TARGET: an action id, or
// an owner/path naming a whole application. The kernel answers with the rows it
// wrote, so the flash counts what actually changed rather than predicting it.
func (s *Server) postActionLifecycle(w http.ResponseWriter, r *http.Request) {
	target := strings.TrimSpace(r.FormValue("target"))
	op := lastSegment(r.URL.Path)
	ctx := r.Context()
	if target == "" {
		s.flashRedirect(w, r, "/workshop", "error", "Name the action or path to change.")
		return
	}
	var rows []client.Action
	err := s.sessions.Do(ctx, func(tok string) error {
		switch op {
		case "enable":
			a, e := s.client.EnableAction(ctx, tok, target)
			rows = a
			return e
		case "disable":
			a, e := s.client.DisableAction(ctx, tok, target)
			rows = a
			return e
		case "visibility":
			// Three-way caller-scope (private/local/public); the form names the
			// target value. Guard the whitelist so an arbitrary string never reaches
			// the kernel.
			v := r.FormValue("visibility")
			switch v {
			case "private", "local", "public":
			default:
				return nil
			}
			a, e := s.client.UpdateAction(ctx, tok, target, client.UpdateActionInput{Visibility: &v})
			rows = a
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
	// A single row returns to its own page; a path names many, so the workshop is
	// where the result is legible.
	if len(rows) == 1 && rows[0].ID == target {
		http.Redirect(w, r, "/actions/"+url.PathEscape(target), http.StatusSeeOther)
		return
	}
	s.flashRedirect(w, r, "/workshop", "success", fmt.Sprintf("%d action(s) %sd.", len(rows), op))
}

func (s *Server) postUpdateActionPrice(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()
	price, err := s.parseAmount(r.FormValue("price"))
	if err != nil {
		s.flashRedirect(w, r, "/actions/"+id, "error", capitalize(err.Error())+".")
		return
	}
	err = s.sessions.Do(ctx, func(tok string) error {
		_, e := s.client.UpdateAction(ctx, tok, id, client.UpdateActionInput{Price: &price})
		return e
	})
	if err != nil {
		s.flashRedirect(w, r, "/actions/"+id, "error", userMessage(err))
		return
	}
	s.flashRedirect(w, r, "/actions/"+id, "success", "Price updated. Action is now inactive.")
}

// actionDraft is what a person typed into the create or edit dialog, as
// typed. A refused submission — a local parse error or the kernel's refusal —
// re-renders the dialog from it, so nothing typed is lost. Secrets are not
// kept: they never reach a page, so their inputs come back empty and required.
type actionDraft struct {
	Title, Name, Kind, Description, Price string
	Source, Method                        string
	Params                                []client.HTTPParam
	HasHTTP                               bool // the form carried the http section
	InputSchema, OutputSchema             string
	WasmArtifact                          string
	AuthScheme, AuthName, AuthUser        string // the credential's non-secret part
	Error                                 string
}

// readDraft reads the create/edit dialog. One reader serves both, so the two
// dialogs cannot read a field differently.
func readDraft(r *http.Request) actionDraft {
	_ = r.ParseForm()
	return actionDraft{
		Title:        strings.TrimSpace(r.FormValue("title")),
		Name:         strings.TrimSpace(r.FormValue("name")),
		Kind:         r.FormValue("kind"),
		Description:  r.FormValue("description"),
		Price:        strings.TrimSpace(r.FormValue("price")),
		Source:       strings.TrimSpace(r.FormValue("source")),
		Method:       strings.TrimSpace(r.FormValue("method")),
		Params:       parseHTTPParams(r),
		HasHTTP:      r.PostForm.Has("method"),
		InputSchema:  strings.TrimSpace(r.FormValue("input_schema")),
		OutputSchema: strings.TrimSpace(r.FormValue("output_schema")),
		WasmArtifact: strings.TrimSpace(r.FormValue("wasm_artifact")),
		AuthScheme:   r.FormValue("auth_scheme"),
		AuthName:     strings.TrimSpace(r.FormValue("auth_param_name")),
		AuthUser:     r.FormValue("auth_basic_username"),
	}
}

// draftOf fills the edit dialog from the action as the kernel serves it.
func (s *Server) draftOf(a client.Action) actionDraft {
	d := actionDraft{
		Title:        a.Title,
		Description:  a.Description,
		InputSchema:  jsonEdit(a.InputSchema),
		OutputSchema: jsonEdit(a.OutputSchema),
	}
	if u, ok := s.identity.Unit(); ok {
		d.Price = u.Input(a.Price)
	}
	if a.HTTP != nil {
		d.HasHTTP, d.Source, d.Method, d.Params = true, a.HTTP.URL, a.HTTP.Method, a.HTTP.Params
	}
	return d
}

// createInput turns a draft into the create body, refusing what is not
// well-formed before anything is sent.
func (s *Server) createInput(d actionDraft, r *http.Request) (client.CreateActionInput, error) {
	in := client.CreateActionInput{Name: d.Name, Title: d.Title, Kind: d.Kind, Source: d.Source, Description: d.Description}
	var err error
	if in.Price, err = s.parseAmount(d.Price); err != nil {
		return in, err
	}
	if in.InputSchema, in.OutputSchema, err = draftSchemas(d); err != nil {
		return in, err
	}
	switch d.Kind {
	case "http":
		in.Method, in.Params = d.Method, d.Params
	case "wasm":
		// The compiled module, base64, as sys/tinygo/compile returns it. The
		// kernel validates it; the UI carries it.
		in.WasmArtifact = d.WasmArtifact
	}
	in.Auth = upstreamAuth(r)
	return in, nil
}

// updateInput turns a draft into the update body, sending only what differs
// from the action as served. The kernel deactivates an action on any schema in
// an update, even an identical one (verified live, ba7f5e5), so resending what
// the dialog merely showed would make a title edit cost the action its live
// state. ok is false when nothing differs.
func (s *Server) updateInput(d actionDraft, cur client.Action) (in client.UpdateActionInput, ok bool, err error) {
	was := s.draftOf(cur)
	if d.Title != was.Title {
		in.Title, ok = &d.Title, true
	}
	if d.Description != was.Description {
		in.Description, ok = &d.Description, true
	}
	if d.Price != "" {
		p, err := s.parseAmount(d.Price)
		if err != nil {
			return in, false, err
		}
		if p != cur.Price {
			in.Price, ok = &p, true
		}
	}
	inSchema, outSchema, err := draftSchemas(d)
	if err != nil {
		return in, false, err
	}
	if inSchema != nil && !sameJSON(inSchema, cur.InputSchema) {
		in.InputSchema, ok = inSchema, true
	}
	if outSchema != nil && !sameJSON(outSchema, cur.OutputSchema) {
		in.OutputSchema, ok = outSchema, true
	}
	if d.HasHTTP && (d.Source != was.Source || d.Method != was.Method || !reflect.DeepEqual(d.Params, was.Params)) {
		in.Source, in.Method, in.Params, ok = &d.Source, d.Method, d.Params, true
	}
	return in, ok, nil
}

// sameJSON reports whether two JSON texts hold the same value, whatever their
// spacing or key order.
func sameJSON(a, b json.RawMessage) bool {
	var av, bv any
	if json.Unmarshal(a, &av) != nil || json.Unmarshal(b, &bv) != nil {
		return false
	}
	return reflect.DeepEqual(av, bv)
}

// draftSchemas reads the two schema texts. An empty text sends nothing; text
// that is not JSON is refused here, since the kernel could only say less.
func draftSchemas(d actionDraft) (in, out json.RawMessage, err error) {
	for _, f := range []struct {
		name, text string
		dst        *json.RawMessage
	}{{"input schema", d.InputSchema, &in}, {"output schema", d.OutputSchema, &out}} {
		if f.text == "" {
			continue
		}
		if !json.Valid([]byte(f.text)) {
			return nil, nil, fmt.Errorf("the %s is not valid JSON", f.name)
		}
		*f.dst = json.RawMessage(f.text)
	}
	return in, out, nil
}

// draftError is the sentence a refused dialog shows: the kernel's own, mapped,
// or the local parse error as a sentence.
func draftError(err error) string {
	if _, ok := client.AsKernelError(err); ok {
		return userMessage(err)
	}
	return capitalize(err.Error()) + "."
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
	d := readDraft(r)
	var rows []client.Action
	changed := false
	err := s.sessions.Do(ctx, func(tok string) error {
		cur, e := s.client.GetAction(ctx, tok, id)
		if e != nil {
			return e
		}
		in, ok, e := s.updateInput(d, cur)
		if e != nil || !ok {
			return e
		}
		changed = true
		rows, e = s.client.UpdateAction(ctx, tok, id, in)
		return e
	})
	switch {
	case isAuthErr(err):
		s.handleErr(w, r, err)
	case err != nil:
		d.Error = draftError(err)
		s.renderAction(w, r, id, nil, &d)
	case !changed:
		s.flashRedirect(w, r, "/actions/"+id, "info", "No changes.")
	default:
		// The kernel decides what an edit costs: a price, schema or source change
		// deactivates, a title or description change does not (kernel §D4).
		// Report the state it returned rather than predicting one.
		msg := "Action updated."
		if len(rows) == 1 && !rows[0].Active {
			msg += " It is now inactive — enable it when ready."
		}
		s.flashRedirect(w, r, "/actions/"+id, "success", msg)
	}
}

func (s *Server) postActionDelete(w http.ResponseWriter, r *http.Request) {
	target := strings.TrimSpace(r.FormValue("target"))
	ctx := r.Context()
	if target == "" {
		s.flashRedirect(w, r, "/workshop", "error", "Name the action or path to delete.")
		return
	}
	err := s.sessions.Do(ctx, func(tok string) error {
		_, e := s.client.DeleteAction(ctx, tok, target)
		return e
	})
	if s.handleErr(w, r, err) {
		return
	}
	s.flashRedirect(w, r, "/workshop", "success", "Deleted. History is preserved.")
}

// ---- processes ----

// walletView backs the wallet page: balances, open processes (each force-endable
// unless it awaits a peer's receipt), the activity list, and the money acts of
// package wallet. A review names the amount a confirm will move.
type walletView struct {
	Available int64
	Locked    int64
	Open      []client.Process
	Activity  []entryRow
	Superuser bool
	OpenForm  string
	// The rail surface. KernelAddress is where this kernel is paid and Token the
	// contract that money is; both are empty on a world whose money has no
	// addresses, where the operator records money in and out (kernel §D23).
	// MyAddress is the caller's own registered payout address, empty until they
	// prove one. Symbol names the token. Chain says the world's chain is proven
	// (wallet.ChainOf), the one state in which a crypto wallet is asked to deposit.
	KernelAddress string
	Token         string
	Symbol        string
	MyAddress     string
	Withdrawals   []client.Withdrawal
	Chain         bool
	// MessagePrefix is the registration message up to its address, completed by
	// wallet.mjs; Message is the whole of it for an address typed without JS.
	MessagePrefix string
	SignAddress   string
	Message       string
	Review        walletReview
}

// walletReview is what a review dialog confirms: the parsed amount it names,
// the typed one it resubmits, and for a withdrawal the id minted for it.
type walletReview struct {
	Amount int64
	Typed  string
	ID     string
	Reason string
}

// HasAddresses reports a world whose money has addresses: only there does a
// user register one to be paid at (template helper).
func (v walletView) HasAddresses() bool { return v.KernelAddress != "" }

// CanWithdraw reports that a withdrawal has somewhere to go: every world pays
// out, but one with addresses pays only to a registered one (template helper).
func (v walletView) CanWithdraw() bool { return !v.HasAddresses() || v.MyAddress != "" }

func (s *Server) getWallet(w http.ResponseWriter, r *http.Request) {
	s.renderWallet(w, r, openForm(r, "wallet"), walletReview{})
}

// renderWallet renders the wallet page with the named dialog open.
func (s *Server) renderWallet(w http.ResponseWriter, r *http.Request, form string, review walletReview) {
	ctx := r.Context()
	id := s.kernelIdentity(ctx)
	_, chain := wallet.ChainOf(id)
	view := walletView{OpenForm: form, KernelAddress: id.BlockchainAddress, Token: id.Token, Symbol: id.Symbol, Chain: chain, Review: review}
	err := s.sessions.Do(ctx, func(tok string) error {
		me, e := s.client.Me(ctx, tok)
		if e != nil {
			return e
		}
		view.Available, view.Locked, view.MyAddress = me.Available, me.Locked, me.BlockchainAddress
		view.MessagePrefix = wallet.Message(id.PublicKey, me.ID, "")
		if a := strings.TrimSpace(r.URL.Query().Get("address")); a != "" && form == "address" {
			view.SignAddress, view.Message = a, wallet.Message(id.PublicKey, me.ID, a)
		}
		// An unread list is not an empty one: a table claiming no withdrawals
		// while money is in flight would misstate it.
		if view.Withdrawals, e = s.client.ListWithdrawals(ctx, tok); e != nil {
			return e
		}
		if procs, e := s.client.ListProcesses(ctx, tok, false); e == nil {
			view.Open = ownSessions(procs, me.Address)
		}
		// The ledger journals every movement between accounts — deposits,
		// withdrawals, transfers, delivered value, and what each settlement paid
		// (kernel §D4) — so it alone is the activity. A preview only, the most
		// recent 10; each row names the transaction that caused it.
		entries, _ := s.client.Ledger(ctx, tok, 10, 0)
		view.Activity = make([]entryRow, len(entries))
		for i, e := range entries {
			view.Activity[i] = entryRowOf(e, me.Address)
		}
		return nil
	})
	if s.handleErr(w, r, err) {
		return
	}
	view.Superuser = s.isSuperuser(r)
	s.renderPage(w, r, http.StatusOK, "wallet", "Wallet", view, view.Available)
}

// homeView is the dashboard: what the viewer has, what waits on them, and what
// just moved. Each list is a preview of the page its "all" link opens.
type homeView struct {
	Available int64
	Locked    int64
	Superuser bool
	Waiting   []client.Task
	Open      []client.Process
	Activity  []entryRow
}

// ownSessions keeps the viewer's own open sessions. A superuser lists every
// account's, but the wallet and the dashboard are the viewer's money — the
// inbox's rule for tasks; the sessions page is where every account's are read.
func ownSessions(procs []client.Process, me string) []client.Process {
	var own []client.Process
	for _, p := range procs {
		if p.Owner == me {
			own = append(own, p)
		}
	}
	return own
}

// homePreview is how many rows each dashboard section shows.
const homePreview = 5

func (s *Server) getHome(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	view := homeView{Superuser: s.isSuperuser(r)}
	err := s.sessions.Do(ctx, func(tok string) error {
		me, e := s.client.Me(ctx, tok)
		if e != nil {
			return e
		}
		view.Available, view.Locked = me.Available, me.Locked
		// Waiting tasks only: an empty status would read the whole history.
		// A superuser lists everyone's, so keep the ones addressed to the viewer,
		// the inbox's own rule.
		if tasks, e := s.client.ListTasks(ctx, tok, "waiting"); e == nil {
			for _, t := range tasks {
				if t.Waiting() && t.RequiredCaller == me.Address && len(view.Waiting) < homePreview {
					view.Waiting = append(view.Waiting, t)
				}
			}
		}
		if procs, e := s.client.ListProcesses(ctx, tok, false); e == nil {
			own := ownSessions(procs, me.Address)
			view.Open = own[:min(len(own), homePreview)]
		}
		// The ledger, not the transaction list: the kernel pages transactions
		// before its party filter, so a short page could hide the viewer's rows;
		// the ledger holds only the viewer's entries.
		entries, _ := s.client.Ledger(ctx, tok, homePreview, 0)
		for _, e := range entries {
			view.Activity = append(view.Activity, entryRowOf(e, me.Address))
		}
		return nil
	})
	if s.handleErr(w, r, err) {
		return
	}
	s.renderPage(w, r, http.StatusOK, "home", "Home", view, view.Available)
}

// walletAmount reads a review's typed amount, flashing a refusal back to the
// wallet; ok is false when it did.
func (s *Server) walletAmount(w http.ResponseWriter, r *http.Request) (typed string, amount int64, ok bool) {
	typed = strings.TrimSpace(r.FormValue("amount"))
	amount, err := s.parseAmount(typed)
	if err == nil && amount <= 0 {
		err = errors.New("enter an amount")
	}
	if err != nil {
		s.flashRedirect(w, r, "/wallet", "error", capitalize(err.Error())+".")
	}
	return typed, amount, err == nil
}

// postWithdrawReview renders the confirm that names the amount, minting the
// withdrawal's id so a double submit replays to one row (kernel §D23).
func (s *Server) postWithdrawReview(w http.ResponseWriter, r *http.Request) {
	if typed, amount, ok := s.walletAmount(w, r); ok {
		s.renderWallet(w, r, "withdraw-review", walletReview{Amount: amount, Typed: typed, ID: wallet.NewWithdrawalID(), Reason: strings.TrimSpace(r.FormValue("reason"))})
	}
}

// postFundReview renders the confirm that names the amount to deposit; the
// transfer itself is fetched at send time (postFund).
func (s *Server) postFundReview(w http.ResponseWriter, r *http.Request) {
	if typed, amount, ok := s.walletAmount(w, r); ok {
		s.renderWallet(w, r, "fund-review", walletReview{Amount: amount, Typed: typed})
	}
}

// postFund answers wallet.mjs with the transfer to send, built from a fresh read
// of the registered address, so its from is the address on file now, which the
// browser holds the wallet to; nothing fundable on file is 409.
func (s *Server) postFund(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	amount, err := s.parseAmount(r.FormValue("amount"))
	var tx wallet.Tx
	if err == nil {
		err = s.sessions.Do(ctx, func(tok string) error {
			me, e := s.client.Me(ctx, tok)
			if e == nil {
				tx, e = wallet.FundTx(s.kernelIdentity(ctx), me, amount)
			}
			return e
		})
	}
	switch {
	case errors.Is(err, wallet.ErrNoFunding):
		w.WriteHeader(http.StatusConflict)
	case err != nil:
		w.WriteHeader(http.StatusBadGateway)
	default:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(tx)
	}
}

// walletMessage is the sentence for a refused money act: the kernel's mapped
// message, except a 409, whose text names a CLI command; wallet's own refusals
// are written as sentences.
func walletMessage(err error) string {
	ke, ok := client.AsKernelError(err)
	switch {
	case !ok:
		return err.Error()
	case ke.Kind == client.KindInvalidState:
		return "Register your address first."
	case ke.Kind == client.KindUnauthorized:
		return "The signature was not made by that address."
	}
	return userMessage(err)
}

func (s *Server) postWithdraw(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	amount, err := s.parseAmount(r.FormValue("amount"))
	var wd client.Withdrawal
	if err == nil {
		err = s.sessions.Do(ctx, func(tok string) error {
			var e error
			wd, e = wallet.Withdraw(ctx, s.client, tok, r.FormValue("id"), amount, strings.TrimSpace(r.FormValue("reason")))
			return e
		})
	}
	switch {
	case isAuthErr(err):
		s.handleErr(w, r, err)
	case err != nil:
		s.flashRedirect(w, r, "/wallet", "error", walletMessage(err))
	case wd.Status == "blocked":
		s.flashRedirect(w, r, "/wallet", "error", "Withdrawal blocked.")
	default:
		s.flashRedirect(w, r, "/wallet", "success", "Withdrawal "+wd.Status+".")
	}
}

// postRegisterAddress registers the address the caller is paid at, proving
// control of it with a signature over the kernel's registration message. The
// kernel credits the finalized sender, so the proof is what makes a payment
// theirs; registering also attributes payments already received from it.
func (s *Server) postRegisterAddress(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	// The kernel answers a wrong password with 401, which sessions.Do would read
	// as an expired session and sign the person out. The kernel's answer is kept
	// aside instead, exactly as a password change does (postSettings).
	var res client.AddressResult
	var apiErr error
	outerErr := s.sessions.Do(ctx, func(tok string) error {
		res, apiErr = wallet.Register(ctx, s.client, tok, strings.TrimSpace(r.FormValue("address")),
			strings.TrimSpace(r.FormValue("signature")), r.FormValue("current_password"))
		return nil
	})
	if outerErr != nil {
		s.flashResult(w, r, outerErr, "/wallet", "")
		return
	}
	if apiErr != nil {
		msg := walletMessage(apiErr)
		if ke, ok := client.AsKernelError(apiErr); ok && ke.Kind == client.KindUnauthenticated {
			msg = "Current password is incorrect."
		}
		s.flashRedirect(w, r, "/wallet", "error", msg)
		return
	}
	msg := "Address registered."
	if n := len(res.Attributed); n > 0 {
		msg = fmt.Sprintf("Address registered; %d earlier payment(s) credited.", n)
	}
	s.flashRedirect(w, r, "/wallet", "success", msg)
}

func (s *Server) getProcesses(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var procs []client.Process
	err := s.sessions.Do(ctx, func(tok string) error {
		p, e := s.client.ListProcesses(ctx, tok, true)
		procs = p
		return e
	})
	if s.handleErr(w, r, err) {
		return
	}
	open := filterStatus(procs, "open", true)
	closed := filterStatus(procs, "open", false)
	offset := queryOffset(r)
	page := func(rows []client.Process) ([]client.Process, bool) {
		if offset >= len(rows) {
			return nil, false
		}
		end := offset + pageSize
		if end >= len(rows) {
			return rows[offset:], false
		}
		return rows[offset:end], true
	}
	openPage, openMore := page(open)
	closedPage, closedMore := page(closed)
	s.renderPage(w, r, http.StatusOK, "processes", "Processes", processesView{
		Open:      openPage,
		Closed:    closedPage,
		Superuser: s.isSuperuser(r),
		CSRFField: csrf.TemplateField(r),
		Pager:     pageNav(r, offset, pageSize, openMore || closedMore),
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
		view.Me = s.sessions.Address(ctx)
		view.Transactions, _ = s.client.ListTransactions(ctx, tok, id, 0)
		return nil
	})
	if s.handleErrFragment(w, r, err) {
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
	var me client.User
	offset := queryOffset(r)
	hasNext := false
	err := s.sessions.Do(ctx, func(tok string) error {
		var e error
		if me, e = s.client.Me(ctx, tok); e != nil {
			return e
		}
		// Fetch the full ledger and slice one page here: the kernel paginates raw
		// rows before its party filter, so a server-side page can't page the
		// viewer's filtered set (see pageSlice).
		all, e := s.client.ListTransactions(ctx, tok, "", 0)
		if e != nil {
			return e
		}
		start, end, more := pageSlice(len(all), offset, pageSize)
		hasNext = more
		txs = all[start:end]
		return nil
	})
	if s.handleErr(w, r, err) {
		return
	}
	s.renderPage(w, r, http.StatusOK, "transactions", "Transactions", transactionsView{
		Transactions: txs, Me: me.Address, Superuser: s.isSuperuser(r),
		Pager: pageNav(r, offset, pageSize, hasNext),
	})
}

// loadTxView fetches a transaction and the viewer into a txView — the shared
// basis of the transaction page and its verify fragment. The action is the
// transaction's own address, captured when the call was made. CanRate: the
// buyer may rate a successful, unrated transaction (R6.6.4).
func (s *Server) loadTxView(ctx context.Context, tok, id string) (txView, error) {
	tx, e := s.client.GetTransaction(ctx, tok, id)
	if e != nil {
		return txView{}, e
	}
	me, e := s.client.Me(ctx, tok)
	if e != nil {
		return txView{}, e
	}
	return txView{Tx: tx, Me: me, CanRate: tx.Status == "success" && tx.Owner == me.Address && tx.Rating == nil}, nil
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

// getResultJSON serves a transaction's result as a file: the download the
// frozen form offers (§6.5). Who may read it is the kernel's decision, as for
// the receipt page.
func (s *Server) getResultJSON(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()
	var tx client.Transaction
	err := s.sessions.Do(ctx, func(tok string) error {
		t, e := s.client.GetTransaction(ctx, tok, id)
		tx = t
		return e
	})
	if s.handleErr(w, r, err) {
		return
	}
	body := bytes.TrimSpace(tx.Result)
	if len(body) == 0 {
		body = []byte("null")
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="result-`+id[:min(8, len(id))]+`.json"`)
	_, _ = w.Write(body)
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

// ---- task mutation handlers ----

func (s *Server) postCompleteTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()
	args, argErr := buildArgs(r)

	var outcome Outcome
	err := s.sessions.Do(ctx, func(tok string) error {
		if argErr != nil {
			return nil // handled after sessions.Do
		}
		out, e := s.caller.CompleteTask(ctx, tok, id, args)
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
	s.flashRedirect(w, r, "/inbox", "success", "Task completed.")
}

// requesterRank orders the requesters list: who awaits a decision first.
var requesterRank = map[string]int{"pending": 0, "approved": 1, "blocked": 2}

// postRequester approves, revokes or blocks a requester (kernel U52); the
// kernel refuses a requester it does not list, and the outcome comes back as
// the flash on the inbox (post/redirect/get).
func (s *Server) postRequester(w http.ResponseWriter, r *http.Request) {
	verb := r.PathValue("verb")
	if verb != "approve" && verb != "revoke" && verb != "block" {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	err := s.sessions.Do(ctx, func(tok string) error {
		return s.client.DecideRequester(ctx, tok, r.PathValue("address"), verb)
	})
	s.flashResult(w, r, err, "/inbox", "Done.")
}

// postCancelTask declines a waiting task. The kernel decides who may (the one
// asked, or the one who asked) and refuses a task no longer waiting; the
// outcome comes back as the flash on the inbox (post/redirect/get).
func (s *Server) postCancelTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()
	err := s.sessions.Do(ctx, func(tok string) error {
		_, e := s.client.CancelTask(ctx, tok, id)
		return e
	})
	s.flashResult(w, r, err, "/inbox", "Task declined.")
}

// ---- helpers ----

// formWhitelist maps a page group to the modal forms it may open via ?form=.
// The no-JS fallback renders the named dialog with the [open] attribute, so the
// value is validated here before it ever reaches a template.
var formWhitelist = map[string]map[string]bool{
	"action":        {"action-create": true, "action-import": true},
	"action-detail": {"action-edit": true},
	"user":          {"user-update": true},
	"wallet":        {"fund": true, "withdraw": true, "address": true},
	"admin":         {"admin-deposit": true, "admin-inspect": true},
	"admin-user":    {"admin-rename": true},
	"admin-peer":    {"admin-rename": true},
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

// capitalize upper-cases the first letter of a validation message, so a helper's
// lowercase error reads as a sentence in a flash.
func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
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
//
// A field is named by its JSON Pointer (`f_/messages/0/content`), its type by
// `t_<path>` — a container's hint, `object` or `array`, says how its children
// nest — its null box by `n_<path>`, and a required container by `r_<path>`.
// The tree is rebuilt from the form alone, so no schema is read at submit
// (§6.2).
func buildArgs(r *http.Request) (json.RawMessage, error) {
	if err := r.ParseForm(); err != nil {
		return nil, err
	}
	useRaw := r.FormValue("use_raw") != "" || r.FormValue("has_fields") == ""
	if useRaw {
		return rawJSONOrEmpty(r.FormValue("raw_args"))
	}
	// Three states per field, kept distinct: blank is omitted, a value is sent,
	// and null is sent only when the person ticks the field's null box — never
	// derived from a blank input, which would make omission impossible. A null
	// box nulls its whole subtree.
	hints, required := map[string]string{}, map[string]bool{}
	var nulls []string
	for key := range r.Form {
		switch {
		case strings.HasPrefix(key, "t_"):
			hints[key[2:]] = r.Form.Get(key)
		case strings.HasPrefix(key, "r_"):
			required[key[2:]] = true
		case strings.HasPrefix(key, "n_"):
			nulls = append(nulls, key[2:])
		}
	}
	root := map[string]any{}
	for key := range r.Form {
		if !strings.HasPrefix(key, "f_") {
			continue
		}
		path := key[2:]
		if underAny(path, nulls) {
			continue
		}
		val := strings.TrimSpace(r.Form.Get(key))
		if val == "" {
			continue
		}
		rv, err := coerce(hints[path], val)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", path, err)
		}
		if err := setAt(root, path, rv); err != nil {
			return nil, err
		}
	}
	for _, p := range nulls {
		if err := setAt(root, p, json.RawMessage("null")); err != nil {
			return nil, err
		}
	}
	return json.Marshal(finalize("", root, hints, required, true))
}

// The JSON switch converts what is on screen into the other view exactly, or
// refuses with the reason (§6.2). Form → JSON is the args a submit would send,
// built by buildArgs alone; JSON → form is the form's own fields filled from
// the editor, so each form's resource answers it with that form's schema.

// postFormJSON is the form → JSON direction of an argument form.
func (s *Server) postFormJSON(w http.ResponseWriter, r *http.Request) {
	args, err := buildArgs(r)
	if err != nil {
		http.Error(w, capitalize(err.Error())+".", http.StatusUnprocessableEntity)
		return
	}
	writeJSONText(w, args)
}

// postActionForm is the JSON → form direction of an action's run form.
func (s *Server) postActionForm(w http.ResponseWriter, r *http.Request) {
	var form FormModel
	err := s.sessions.Do(r.Context(), func(tok string) error {
		a, e := s.client.GetAction(r.Context(), tok, r.PathValue("id"))
		form = BuildForm(a.InputSchema)
		return e
	})
	s.fillFields(w, r, form, err)
}

// postTaskForm is the JSON → form direction of a task's completion form.
func (s *Server) postTaskForm(w http.ResponseWriter, r *http.Request) {
	var form FormModel
	err := s.sessions.Do(r.Context(), func(tok string) error {
		t, e := s.client.GetTask(r.Context(), tok, r.PathValue("id"))
		form = BuildTaskForm(t.AllowedInput, t.PartialArgs).Editable
		return e
	})
	s.fillFields(w, r, form, err)
}

// fillFields fills form from the editor's JSON and answers its fields.
func (s *Server) fillFields(w http.ResponseWriter, r *http.Request, form FormModel, err error) {
	if err != nil {
		http.Error(w, userMessage(err), http.StatusUnprocessableEntity)
		return
	}
	s.renderFields(w, form, form.Fill(json.RawMessage(r.PostFormValue("raw_args"))))
}

// renderFields answers a form's fields, or — when the form cannot hold the
// JSON exactly — each value it cannot hold, and the switch is refused.
func (s *Server) renderFields(w http.ResponseWriter, form FormModel, inexact []string) {
	if len(inexact) > 0 {
		http.Error(w, strings.Join(inexact, "; ")+".", http.StatusUnprocessableEntity)
		return
	}
	_ = s.render.Fragment(w, http.StatusOK, "_fields", form)
}

// writeJSONText answers a JSON value as the editor shows it.
func writeJSONText(w http.ResponseWriter, v []byte) {
	var out bytes.Buffer
	_ = json.Indent(&out, v, "", "  ")
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(out.Bytes())
}

// underAny reports whether path is one of the prefixes or beneath one.
func underAny(path string, prefixes []string) bool {
	for _, p := range prefixes {
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}

// setAt places a leaf at a JSON Pointer, creating the containers above it as
// maps keyed by reference token; finalize turns the arrays into slices.
func setAt(root map[string]any, path string, leaf json.RawMessage) error {
	if !strings.HasPrefix(path, "/") {
		return fmt.Errorf("field %q: not a pointer", path)
	}
	segs := strings.Split(path[1:], "/")
	node := root
	for _, seg := range segs[:len(segs)-1] {
		key := pointerUnescape(seg)
		child, ok := node[key].(map[string]any)
		if !ok {
			if _, isLeaf := node[key]; isLeaf {
				return fmt.Errorf("field %q: conflicts with a value above it", path)
			}
			child = map[string]any{}
			node[key] = child
		}
		node = child
	}
	node[pointerUnescape(segs[len(segs)-1])] = leaf
	return nil
}

// finalize prunes empty containers bottom-up and orders an array's elements by
// index, so a row left blank is dropped and the indices compact. A container
// is an array iff its hint says so. Nil means "nothing here", except where keep
// holds: the root, and a required container (`r_`) of an object being sent,
// which is sent even when nothing in it was filled, absence being invalid for
// it (§6.2).
func finalize(path string, node any, hints map[string]string, required map[string]bool, keep bool) any {
	m, ok := node.(map[string]any)
	if !ok {
		return node
	}
	if hints[path] == "array" {
		idx := make([]int, 0, len(m))
		for k := range m {
			if i, err := strconv.Atoi(k); err == nil {
				idx = append(idx, i)
			}
		}
		sort.Ints(idx)
		arr := make([]any, 0, len(idx))
		for _, i := range idx {
			k := strconv.Itoa(i)
			if v := finalize(path+"/"+k, m[k], hints, required, false); v != nil {
				arr = append(arr, v)
			}
		}
		if len(arr) == 0 && !keep {
			return nil
		}
		return arr
	}
	obj := make(map[string]any, len(m))
	for k, v := range m {
		if v := finalize(path+"/"+pointerEscape(k), v, hints, required, false); v != nil {
			obj[k] = v
		}
	}
	if len(obj) == 0 && !keep {
		return nil
	}
	for p := range required {
		i := strings.LastIndex(p, "/")
		if i < 0 || p[:i] != path {
			continue
		}
		if k := pointerUnescape(p[i+1:]); obj[k] == nil {
			obj[k] = finalize(p, map[string]any{}, hints, required, true)
		}
	}
	return obj
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
	case "json":
		if !json.Valid([]byte(val)) {
			return nil, fmt.Errorf("must be valid JSON")
		}
		return json.RawMessage(val), nil
	default: // string and unconstrained
		b, _ := json.Marshal(val)
		return b, nil
	}
}

// parseLookup converts a sys/lookup result into partial actionCardViews. A
// lookup row is an action read under two other names — action_id for id, and
// observed_at, the last time its terms were verified against their home kernel
// (a gossip observation or a proxy's reconcile; absent on a local action, so it
// is the served fact that marks a row as remote-backed, requirements.md §6.2).
// Whether a row is DISCOVERED — nothing local backs it — is settled by the
// local read in getBrowse, never by the row's shape.
func parseLookup(result json.RawMessage) []actionCardView {
	var lr struct {
		Results []struct {
			client.Action
			ActionID   string `json:"action_id"`
			ObservedAt string `json:"observed_at"`
		} `json:"results"`
	}
	_ = json.Unmarshal(result, &lr)
	out := make([]actionCardView, 0, len(lr.Results))
	for _, r := range lr.Results {
		a := r.Action
		a.ID = r.ActionID
		out = append(out, actionCardView{Action: a, ObservedAt: r.ObservedAt})
	}
	return out
}

// hintKey is the form titles and typed text are compared in: compatibility
// decomposed (NFKD, so fullwidth and ligature forms read as plain letters) with
// combining marks removed (so "Résumé" reads "resume"), lowercased, every run of non-letters and non-digits one space, trimmed.
func hintKey(s string) string {
	var b strings.Builder
	space := false
	for _, r := range norm.NFKD.String(s) {
		switch {
		case unicode.Is(unicode.Mn, r):
			continue
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteRune(unicode.ToLower(r))
		default:
			space = true
		}
	}
	return b.String()
}

// wordsBegin reports whether every typed word is the beginning of some word.
func wordsBegin(words, typed []string) bool {
	for _, t := range typed {
		found := false
		for _, w := range words {
			if strings.HasPrefix(w, t) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// catalogTitle is one entry of the search-hint catalog: a title and its
// hintKey. The browser matches typed text against these keys, so a title is
// normalized once, here, by the one rule the search fallback also uses.
type catalogTitle struct {
	T string `json:"t"`
	K string `json:"k"`
}

// catalogTitles returns the catalog's distinct titles (equal under hintKey is
// one), ordered by key.
func catalogTitles(actions []client.Action) []catalogTitle {
	seen := map[string]bool{}
	out := []catalogTitle{}
	for _, a := range actions {
		k := hintKey(a.Title)
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, catalogTitle{T: a.Title, K: k})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].K < out[j].K })
	return out
}

// getSearchCatalog serves the search box's hints the way a static site serves
// its search index: the whole catalog's titles at once (local actions,
// resolved proxies and the actions discovered on peers), which the page
// matches in the browser as the person types, so typing sends no request. It
// reads the free catalog and never runs sys/lookup. The list is the caller's
// own (it holds their private titles), so it is never stored by the browser:
// a cached copy at this one URL could be shown to the next account. A failure
// answers with no titles; the search itself still works.
func (s *Server) getSearchCatalog(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	out := []catalogTitle{}
	err := s.sessions.Do(ctx, func(tok string) error {
		actions, e := s.client.ListActions(ctx, tok, "")
		if e == nil {
			out = catalogTitles(actions)
		}
		return e
	})
	if err != nil {
		s.log.Warn("search catalog failed", "err", err)
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// fallbackLimit caps the search fallback, like the one ranked page a lookup
// answers.
const fallbackLimit = 20

// catalogAsResults is the search when sys/lookup is priced or fails: the
// catalog matched by the hints' rules (hintKey, every typed word beginning a
// word). A title beginning with the query ranks first, then a title holding
// the words, then a name or description holding them; shorter titles first
// within each, then alphabetical. Discovered rows are kept and marked.
func catalogAsResults(actions []client.Action, q string) []actionCardView {
	typed := strings.Fields(hintKey(q))
	if len(typed) == 0 {
		return nil
	}
	whole := strings.Join(typed, " ")
	type hit struct {
		a    client.Action
		key  string
		rank int
	}
	var hits []hit
	for _, a := range actions {
		key := hintKey(a.Title)
		rank := 0
		switch {
		case strings.HasPrefix(key, whole):
		case wordsBegin(strings.Fields(key), typed):
			rank = 1
		case wordsBegin(strings.Fields(hintKey(a.Name+" "+a.Description)), typed):
			rank = 2
		default:
			continue
		}
		hits = append(hits, hit{a, key, rank})
	}
	sort.SliceStable(hits, func(i, j int) bool {
		x, y := hits[i], hits[j]
		if x.rank != y.rank {
			return x.rank < y.rank
		}
		if len(x.key) != len(y.key) {
			return len(x.key) < len(y.key)
		}
		return x.key < y.key
	})
	out := make([]actionCardView, 0, min(len(hits), fallbackLimit))
	for _, h := range hits[:min(len(hits), fallbackLimit)] {
		out = append(out, cardOf(h.a))
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
		// A taken handle is typed now (v0.12.14): 422 invalid_input "create
		// account: already exists". Key on the code and the kernel's own phrase,
		// not the substring scan the old raw-500 leak forced — that scan matched
		// on "handle", so any 500 mentioning one became "handle taken".
		if ke.Kind == client.KindInvalidInput && strings.Contains(strings.ToLower(ke.KernelMessage), "already exists") {
			return "That handle is already taken — try signing in, or pick a different one."
		}
		if ke.Kind == client.KindInvalidInput {
			return ke.UserMessage()
		}
	}
	return "Could not create the account. Please check your details and try again."
}

// The natives the UI runs on a person's behalf to POWER A SURFACE: search ranks
// with one, the proposal chooses with another. Both are read before they are
// run, because every native's price is operator configuration — the shipped
// defaults are policy, not contract — and one run unasked would spend from a
// page nobody confirmed (§8.2). An action a person chooses to run is unaffected:
// it is priced and confirmed like any other.
//
// A native is named on this kernel, so its address is composed from the
// kernel's own name: sys@<kernel>/<name>.
const (
	lookupNative = "lookup"
	decideNative = "llm/decide"
)

// native is the address of one of this kernel's natives.
func (s *Server) native(name string) string { return s.identity.Address(superuserHandle) + "/" + name }

// nativeIsPriced reports that running this native would cost the caller money.
// A native that cannot be read at all is not reported as priced: whether it
// works is discovered by running it and reporting what happened.
func (s *Server) nativeIsPriced(ctx context.Context, token, ref string) bool {
	a, ok, err := s.client.ResolveRef(ctx, token, ref)
	return err == nil && ok && a.Price > 0
}

// nativeOffered reports that a command driven by this native may be shown at
// all: it is here, it is callable, and it is free. The stricter test is for an
// affordance rather than a fallback — a doomed run is never offered (§5.5), and
// a priced one would spend without a confirm.
func (s *Server) nativeOffered(ctx context.Context, token, ref string) bool {
	a, ok, err := s.client.ResolveRef(ctx, token, ref)
	return err == nil && ok && a.Active && a.Price == 0
}

// upstreamAuth builds the write-only credential configuration from the shared
// auth panel's fields, or nil when no scheme was chosen. One definition serves
// action creation and OpenAPI import, which post the same fields: the kernel
// takes the same {scheme, config, secrets} object in both bodies, and two
// copies of this switch would drift the moment a scheme is added.
//
// The delegated schemes are absent by construction: they hold no secret here,
// each caller supplying their own through the Grants surface (§6.4).
// authPanel feeds the _form_auth partial: the input id prefix, the owner-held
// schemes it offers, and the non-secret part of a refused submission.
type authPanel struct {
	ID, Scheme, Name, User string
}

// Schemes are the owner-held schemes the panel offers. The delegated schemes
// are absent by construction: each caller supplies their own through Grants.
func (authPanel) Schemes() []string { return []string{"bearer", "basic", "header", "query"} }

// AuthPanel is the create dialog's credential panel, keeping what was typed.
func (d actionDraft) AuthPanel(id string) authPanel {
	return authPanel{ID: id, Scheme: d.AuthScheme, Name: d.AuthName, User: d.AuthUser}
}

// newAuthPanel is an empty credential panel (the import dialog).
func newAuthPanel(id string) authPanel { return authPanel{ID: id} }

func upstreamAuth(r *http.Request) json.RawMessage {
	scheme := r.FormValue("auth_scheme")
	if scheme == "" {
		return nil
	}
	var out []byte
	switch scheme {
	case "bearer":
		out, _ = json.Marshal(map[string]any{
			"scheme":  "bearer",
			"secrets": map[string]string{"token": strings.TrimSpace(r.FormValue("auth_bearer_token"))},
		})
	case "basic":
		out, _ = json.Marshal(map[string]any{
			"scheme":  "basic",
			"secrets": map[string]string{"username": r.FormValue("auth_basic_username"), "password": r.FormValue("auth_basic_password")},
		})
	case "header", "query":
		out, _ = json.Marshal(map[string]any{
			"scheme":  scheme,
			"config":  map[string]string{"name": strings.TrimSpace(r.FormValue("auth_param_name"))},
			"secrets": map[string]string{"value": r.FormValue("auth_param_value")},
		})
	}
	if len(out) == 0 {
		return nil
	}
	return json.RawMessage(out)
}

// postPropose asks the kernel's own planner which of the actions on screen fits
// what the person asked for, and renders its choice as the ordinary action page
// with the arguments it proposed already filled in.
//
// It is the kernel requirement U46 made visible: planning and spending are
// separate decisions. sys/llm/decide selects and never executes, so nothing has
// moved when this returns — the person reads the choice, edits any argument,
// and presses the same confirm that names the amount. There is no second money
// path, and this adds no authority: it fills a form.
//
// The candidate set is exactly the refs the person can see, carried on the
// results form. That is deliberate and not incidental: a set assembled here in
// private would be a claim about what was offered that nobody could check. The
// kernel refuses the whole call on a bare ref it does not know, so only refs it
// served are ever sent back to it.
func (s *Server) postPropose(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := r.ParseForm(); err != nil {
		s.flashRedirect(w, r, "/browse", "error", "Could not read that request.")
		return
	}
	q := strings.TrimSpace(r.FormValue("q"))
	refs := r.Form["ref"]
	if q == "" || len(refs) == 0 {
		s.flashRedirect(w, r, "/browse", "error", "Search first, then ask for a proposal.")
		return
	}
	var chosen, message string
	var args map[string]string
	var raws map[string]json.RawMessage
	err := s.sessions.Do(ctx, func(tok string) error {
		// The same guard as search: a native the UI runs on a person's behalf is
		// read for its price first, since a priced one would spend from a page
		// they did not confirm (§8.2).
		if s.nativeIsPriced(ctx, tok, s.native(decideNative)) {
			return errPricedPlanner
		}
		out, e := s.caller.Run(ctx, tok, s.native(decideNative), mustArgs(map[string]any{
			"messages": []map[string]string{{"role": "user", "content": q}},
			"actions":  refs,
		}), "")
		if e != nil {
			return e
		}
		if !out.IsSuccess() {
			// The kernel's own reason, not a guess: it may have no model
			// configured, or the model may have chosen nothing.
			return fmt.Errorf("%s", out.Reason())
		}
		chosen, args, raws, message = parseDecision(out.Result)
		return nil
	})
	if err != nil {
		s.flashRedirect(w, r, "/browse?q="+url.QueryEscape(q), "error", capitalize(userMessage(err))+".")
		return
	}
	if chosen == "" {
		s.flashRedirect(w, r, "/browse?q="+url.QueryEscape(q), "info", "No action was chosen for that.")
		return
	}
	// The chosen ref is resolved the way any ref is, so a proposal lands on the
	// ordinary action page and inherits every rule that page already holds.
	var id string
	if e := s.sessions.Do(ctx, func(tok string) error {
		a, ok, re := s.client.ResolveRef(ctx, tok, chosen)
		if re != nil {
			return re
		}
		if !ok {
			return fmt.Errorf("the chosen action could not be opened")
		}
		id = a.ID
		return nil
	}); e != nil {
		s.flashRedirect(w, r, "/browse?q="+url.QueryEscape(q), "error", capitalize(userMessage(e))+".")
		return
	}
	s.renderAction(w, r, id, &proposal{Args: args, Raws: raws, Message: message}, nil)
}

// proposal is the planner's choice as the action page renders it: the arguments
// it proposes, keyed by field name, and anything it said.
type proposal struct {
	Args    map[string]string
	Raws    map[string]json.RawMessage // the same arguments as the kernel sent them
	Message string
}

// Raw is the proposed arguments as one pretty JSON object, for the raw input
// box. Built from what the kernel sent rather than from the flattened form
// values, so a number stays a number and an object stays an object.
func (p proposal) Raw() string {
	if len(p.Raws) == 0 {
		return ""
	}
	b, err := json.MarshalIndent(p.Raws, "", "  ")
	if err != nil {
		return ""
	}
	return string(b)
}

// errPricedPlanner is the refusal when an operator has priced the planner: the
// proposal is withheld rather than charged for without a confirm (§8.2).
var errPricedPlanner = errors.New("choosing an action is priced on this kernel")

// parseDecision reads the planner's reply: the action it chose, the arguments it
// proposes as form values, and anything it said. Every argument is rendered as
// text into an ordinary field, so a scalar prints as itself and an object or
// array as its JSON — the person edits and submits it like any other input, and
// the kernel validates it like any other input.
func parseDecision(result json.RawMessage) (action string, args map[string]string, raws map[string]json.RawMessage, message string) {
	var d struct {
		Action  string                     `json:"action"`
		Args    map[string]json.RawMessage `json:"args"`
		Message string                     `json:"message"`
	}
	if err := json.Unmarshal(result, &d); err != nil {
		return "", nil, nil, ""
	}
	args = make(map[string]string, len(d.Args))
	for k, v := range d.Args {
		var str string
		if json.Unmarshal(v, &str) == nil {
			args[k] = str
			continue
		}
		args[k] = strings.TrimSpace(string(v))
	}
	return d.Action, args, d.Args, d.Message
}

// Sort orders for the catalog and for search results. Relevance is what the
// kernel served — the lookup ranking, or the catalog's own order — and is the
// default because it is the only order the kernel itself asserts.
const (
	sortRelevance = ""
	sortRating    = "rating"
	sortPrice     = "price"
)

// sortCards arranges served rows by a signal the kernel already publishes. It
// is an arrangement, never a computation: every number compared here came off
// the wire, and nothing is inferred about a row that carries none.
//
// Under `rating` an UNRATED row — a new action, or a discovered hit, whose
// stats the discovery cache does not store — sorts into a block after the rated
// ones, keeping its own relative order. It is not scored as zero: "no ratings"
// is not a low rating, and interleaving silence with measured opinion would
// invent a judgement the kernel has not made. Grouping says the true thing —
// these have not been rated — while still answering the question asked, which
// was to see the best-rated first. Under relevance they are wherever the kernel
// put them, which is how a new action is still found.
func sortCards(cards []actionCardView, order string) {
	switch order {
	case sortRating:
		rated := func(c actionCardView) bool {
			st := c.Action.Experience()
			return st != nil && st.RatingCount > 0
		}
		sort.SliceStable(cards, func(i, j int) bool {
			a, b := cards[i], cards[j]
			if rated(a) != rated(b) {
				return rated(a)
			}
			if !rated(a) {
				return false // both unrated: leave them as served
			}
			sa, sb := a.Action.Experience(), b.Action.Experience()
			if ra, rb := ratingShare(*sa), ratingShare(*sb); ra != rb {
				return ra > rb
			}
			return sa.RatingCount > sb.RatingCount
		})
	case sortPrice:
		sort.SliceStable(cards, func(i, j int) bool {
			return cards[i].Action.Price < cards[j].Action.Price
		})
	}
}

// ratingShare is the share of ratings that were positive, as the stat strip
// already renders it. One definition, so the number a person sorts by is the
// number they read.
func ratingShare(s client.Stats) float64 {
	if s.RatingCount == 0 {
		return 0
	}
	return s.RatingEstimate
}

// sortOrder reads the requested order, defaulting to relevance. An unknown
// value is relevance rather than an error: a sort is a view, and a mistyped one
// should show the list, not a page of prose.
func sortOrder(r *http.Request) string {
	switch v := r.URL.Query().Get("sort"); v {
	case sortRating, sortPrice:
		return v
	default:
		return sortRelevance
	}
}

// sortHref builds the URL for one sort order, dropping the offset: a different
// order starts at its own first page.
func sortHref(r *http.Request, order string) string {
	q := r.URL.Query()
	q.Del("offset")
	if order == sortRelevance {
		q.Del("sort")
	} else {
		q.Set("sort", order)
	}
	if len(q) == 0 {
		return r.URL.Path
	}
	return r.URL.Path + "?" + q.Encode()
}

// fetchEvidence replaces each card's list row with the action's detail read,
// which is the same row plus its evidence block — a list row carries none. It
// runs concurrently but BOUNDED: a catalog sort reads one row per action, and an
// unbounded fan-out over a large catalog would open a connection per action and
// answer slower than a sequential read. A card that already carries evidence is
// left alone, so paying for the whole catalog and then rendering a page of it
// costs one pass, not two.
func (s *Server) fetchEvidence(ctx context.Context, tok string, cards []actionCardView) {
	const workers = 8
	var wg sync.WaitGroup
	idx := make(chan int)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range idx {
				if a, e := s.client.GetAction(ctx, tok, cards[i].Action.ID); e == nil {
					cards[i].Action = a
				}
			}
		}()
	}
	for i := range cards {
		if cards[i].Action.Evidence == nil && cards[i].Action.ID != "" && !cards[i].Discovered {
			idx <- i
		}
	}
	close(idx)
	wg.Wait()
}
