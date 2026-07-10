// Package chat is the juice-ui integration for the @chat chat server (see
// apps/chat/daemon and docs/apps.md) — the reference implementation of the app
// seam.
//
// The daemon (apps/chat/daemon) is a self-contained, Juice-agnostic REST chat
// server (rooms, membership, messages, per-user unread) whose caller
// authentication is injected. This package is the only Juice- and UI-aware piece;
// it wires the daemon to Juice with two seam identity primitives:
//
//   - apps.ServiceAccount — the @chat kernel account's own outbound identity
//     (provision, authenticate, backoff, self-heal), used to register the @chat/*
//     actions.
//   - apps.CallerIdentity — inbound identity: it mints a stateless, deterministic
//     per-user token that the plugin connects as the user's delegated_bearer grant,
//     and it verifies the token the kernel injects (handed to the daemon as its auth
//     hook). Because the token derives only from (signing key, handle), it is never
//     stored and can never desync with the grant.
//
// The plugin runs the daemon in-process, mounts its write routes under the
// CSRF-exempt /_kernel/chat/ prefix (the @chat/* action sources), reads via the
// daemon's REST Client, and writes by running @chat/* as the signed-in user.
//
// The @chat password, daemon admin secret, and caller-token signing key are
// provisioned on first run and persisted in juice-ui.json via the app config store
// — no chat environment variables.
package chat

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"

	"github.com/daios-ai/juice-ui/apps"
	"github.com/daios-ai/juice-ui/apps/chat/daemon"
	"github.com/daios-ai/juice-ui/client"
)

//go:embed templates/*.html
var tmplFS embed.FS

// chatHandle is the application's service-account handle.
const chatHandle = "@chat"

// chatDBName is the daemon store filename, kept under the user's app data dir.
const chatDBName = "chat.db"

// chatIcon is the plugin's own nav icon (a speech bubble), inline so no core
// sprite edit is needed.
const chatIcon = template.HTML(`<svg class="ico" aria-hidden="true" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z"/></svg>`)

// Chat is the chat UI plugin. It owns the in-process @chat daemon and its Client,
// and uses two seam identity primitives: ServiceAccount (the @chat account's own
// outbound identity) and CallerIdentity (inbound — the per-user tokens the daemon
// verifies).
type Chat struct {
	deps   apps.Deps
	daemon *daemon.Daemon
	dc     *daemon.Client
	sa     *apps.ServiceAccount // @chat kernel account (provision + auth + self-heal)
	id     *apps.CallerIdentity // mints/verifies per-user caller tokens
	dbPath string               // overridable in tests; "" -> in-memory store
	st     chatState            // persisted config; written by ensureActions (the SA setup goroutine)

	provisioned sync.Map // handle -> struct{}: grants synced this run
}

// New constructs the chat plugin. It needs no configuration — it provisions the
// @chat account and daemon secret itself on Start and reaches the kernel through
// Deps.
func New() *Chat { return &Chat{} }

func (c *Chat) Slug() string { return "chat" }

// Nav contributes the "chat" sidebar entry, carrying the signed-in user's unread
// count as an app-scoped badge (distinct from the core's sole step badge).
func (c *Chat) Nav(handle string, superuser bool) []apps.NavEntry {
	if handle == "" {
		return nil
	}
	badge := 0
	if c.dc != nil {
		if n, err := c.dc.UnreadTotal(context.Background(), handle); err == nil {
			badge = n
		}
	}
	return []apps.NavEntry{{Slug: "chat", Label: "chat", Href: "/chat", Icon: chatIcon, Match: "/chat", Badge: badge, BadgeURL: "/chat/badge"}}
}

func (c *Chat) Templates() fs.FS { return tmplFS }

// Mount registers the browser routes (auth-gated) and mounts the daemon's
// kernel-facing write routes under /_kernel/chat/ (CSRF-exempt — the kernel calls
// them server-to-server, authenticated by the injected per-user key).
func (c *Chat) Mount(mux *http.ServeMux, d apps.Deps) {
	c.deps = d
	mux.HandleFunc("GET /chat", d.RequireAuth(c.getIndex))
	mux.HandleFunc("GET /chat/badge", d.RequireAuth(c.getBadge))
	mux.HandleFunc("GET /chat/room/{id}", d.RequireAuth(c.getRoom))
	mux.HandleFunc("GET /chat/room/{id}/messages", d.RequireAuth(c.getMessages))
	mux.HandleFunc("POST /chat/rooms", d.RequireAuth(c.postCreateRoom))
	mux.HandleFunc("POST /chat/room/{id}/message", d.RequireAuth(c.postMessage))
	mux.HandleFunc("POST /chat/room/{id}/delete", d.RequireAuth(c.postDeleteRoom))
	mux.HandleFunc("POST /chat/room/{id}/leave", d.RequireAuth(c.postLeave))
	mux.HandleFunc("POST /chat/room/{id}/add-member", d.RequireAuth(c.postAddMember))
	mux.HandleFunc("POST /chat/room/{id}/remove-member", d.RequireAuth(c.postRemoveMember))
	mux.HandleFunc("POST /chat/room/{id}/message/{mid}/delete", d.RequireAuth(c.postDeleteMessage))
	// The daemon's write routes, reached by the kernel at PublicURL/_kernel/chat/<op>.
	mux.Handle("/_kernel/chat/", http.StripPrefix("/_kernel/chat", http.HandlerFunc(c.kernelDispatch)))
}

// kernelDispatch forwards a kernel callback to the daemon once it is up.
func (c *Chat) kernelDispatch(w http.ResponseWriter, r *http.Request) {
	if c.daemon == nil {
		http.Error(w, "chat starting", http.StatusServiceUnavailable)
		return
	}
	c.daemon.KernelHandler().ServeHTTP(w, r)
}

// chatState is the plugin's persisted config: the @chat account password, the
// daemon admin secret, the caller-token signing key, and AuthRev — the revision of
// the delegated_bearer auth config last pushed to the kernel. AuthRev exists
// because the kernel never returns an action's auth config (write-only, R9), so
// auth-config drift (e.g. a changed header) is invisible to a read-based
// reconciler; persisting the revision lets the plugin force a re-push when its
// desired auth changes.
type chatState struct {
	Password    string `json:"password"`
	AdminSecret string `json:"admin_secret"`
	SigningKey  string `json:"signing_key"`
	AuthRev     string `json:"auth_rev"`
}

// Start provisions credentials (once), opens the durable store, brings up the
// in-process daemon, and — in the background — authenticates as @chat and
// registers the @chat/* actions. Backgrounded so juice-ui starts even when the
// kernel is briefly unavailable.
func (c *Chat) Start(ctx context.Context) error {
	var st chatState
	if raw, err := c.deps.LoadAppState(c.Slug()); err == nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, &st)
	}
	changed := false
	if st.Password == "" {
		st.Password, changed = newSecret(), true
	}
	if st.AdminSecret == "" {
		st.AdminSecret, changed = newSecret(), true
	}
	if st.SigningKey == "" {
		st.SigningKey, changed = newSecret(), true
	}
	if changed {
		b, _ := json.Marshal(st)
		if err := c.deps.SaveAppState(c.Slug(), b); err != nil {
			return fmt.Errorf("chat: persist credentials: %w", err)
		}
	}
	c.st = st // ensureActions reads AuthRev and rewrites it after an auth re-push

	// Inbound caller identity: mints the per-user tokens the plugin connects as
	// grants, and verifies the ones the kernel injects. The daemon receives its
	// Authenticate as its (Juice-agnostic) auth hook.
	c.id = apps.NewCallerIdentity(st.SigningKey)
	store, err := daemon.Open(c.dbPathOrDefault())
	if err != nil {
		return fmt.Errorf("chat: open store: %w", err)
	}
	c.daemon = daemon.New(store, st.AdminSecret, c.id.Authenticate)
	c.dc = daemon.NewClient(c.daemon)

	// The @chat kernel account. The seam ServiceAccount owns its whole lifecycle
	// — provision, authenticate, backoff, state-aware logging, self-heal — so a
	// suspended or briefly-unreachable @chat degrades gracefully instead of
	// 429-storming the kernel. We hand it our idempotent action registration.
	c.sa = apps.NewServiceAccount(c.deps, chatHandle, st.Password)
	return c.sa.Start(ctx, c.ensureActions)
}

// Stop halts the account lifecycle and closes the store.
func (c *Chat) Stop(ctx context.Context) error {
	if c.sa != nil {
		c.sa.Stop()
	}
	if c.daemon != nil {
		return c.daemon.Close()
	}
	return nil
}

// dbPathOrDefault resolves the durable store path. Order: an explicit test
// override; then "" (in-memory) when there is no persistent app config, so tests
// write no file; then — for the migration — the legacy location under the *config*
// dir if a store already exists there, kept in place because it is live user data
// and the WAL sidecars make an automatic move unsafe (operators migrate explicitly,
// see docs/config.md); else the proper data dir (apps.UserDataDir via Deps.DataDir),
// falling back beside the app-state file if none was provided.
func (c *Chat) dbPathOrDefault() string {
	if c.dbPath != "" {
		return c.dbPath
	}
	if c.deps.ConfigPath == "" {
		return ""
	}
	if cfgDir, err := os.UserConfigDir(); err == nil {
		legacy := filepath.Join(cfgDir, "juice-ui", chatDBName)
		if _, err := os.Stat(legacy); err == nil {
			return legacy
		}
	}
	dir := c.deps.DataDir
	if dir == "" {
		dir = filepath.Dir(c.deps.ConfigPath) // fallback: beside the app-state file
	}
	_ = os.MkdirAll(dir, 0o700)
	return filepath.Join(dir, chatDBName)
}

// ensureActions makes each @chat action exist, active, public, and pointed at the
// daemon's matching write route with the current delegated_bearer auth. It creates
// a missing action and reconciles a drifted one (bringing the whole contract into
// line, then re-enabling — an update deactivates the action and revokes its grants,
// which per-user provisioning re-heals). A correctly-configured action is left
// untouched, so grants survive restarts.
//
// `force` covers the one drift a read-based reconciler cannot see: the auth config
// is write-only (R9), so a changed header/template is invisible. When the plugin's
// desired auth revision differs from the one it last pushed, it re-pushes auth on
// every action, then records the new revision.
func (c *Chat) ensureActions(ctx context.Context) error {
	rev := authRev()
	force := c.st.AuthRev != rev
	err := c.sa.Do(ctx, func(tok string) error {
		existing, err := c.deps.Client.ListActions(ctx, tok, chatHandle, "", true)
		if err != nil {
			return err
		}
		have := map[string]client.Action{}
		for _, a := range existing {
			have[a.Name] = a
		}
		wanted := map[string]bool{}
		for _, op := range daemon.Ops {
			wanted[op.Name] = true
		}
		base := strings.TrimRight(c.deps.PublicURL, "/") + "/_kernel/chat/"
		auth := authConfig()
		pubTrue := true
		for _, op := range daemon.Ops {
			want := base + op.Name
			a, ok := have[op.Name]
			if !ok {
				na, err := c.deps.Client.CreateAction(ctx, tok, client.CreateActionInput{
					Name:         op.Name,
					Kind:         "http",
					Source:       want,
					Method:       "POST",
					Description:  op.Description,
					Price:        0,
					InputSchema:  op.InputSchema,
					OutputSchema: op.OutputSchema,
					Auth:         auth,
				})
				if err != nil {
					return fmt.Errorf("chat: create %s/%s: %w", chatHandle, op.Name, err)
				}
				if err := c.enablePublic(ctx, tok, na.ID); err != nil {
					return err
				}
				continue
			}
			if force || contractDrifted(a, want, op) {
				// Bring the WHOLE contract into line (source, schemas, description,
				// AND auth), then re-enable — the update deactivates the action and
				// revokes its grants; per-user provisioning re-heals them.
				desc := op.Description
				if _, err := c.deps.Client.UpdateAction(ctx, tok, a.ID, client.UpdateActionInput{
					Source:       &want,
					Method:       "POST",
					Auth:         auth,
					InputSchema:  op.InputSchema,
					OutputSchema: op.OutputSchema,
					Description:  &desc,
					Public:       &pubTrue,
				}); err != nil {
					return fmt.Errorf("chat: reconcile %s/%s: %w", chatHandle, op.Name, err)
				}
				if err := c.deps.Client.EnableAction(ctx, tok, a.ID); err != nil {
					return fmt.Errorf("chat: enable %s/%s: %w", chatHandle, op.Name, err)
				}
				continue
			}
			// Contract already correct — only nudge active/public state if needed.
			if !a.Active {
				if err := c.deps.Client.EnableAction(ctx, tok, a.ID); err != nil {
					return fmt.Errorf("chat: enable %s/%s: %w", chatHandle, op.Name, err)
				}
			}
			if !a.Public {
				if _, err := c.deps.Client.UpdateAction(ctx, tok, a.ID, client.UpdateActionInput{Public: &pubTrue}); err != nil {
					return fmt.Errorf("chat: publish %s/%s: %w", chatHandle, op.Name, err)
				}
			}
		}
		// Retire orphans: an active @chat action no longer in Ops (e.g. a removed op
		// like rename-room) is disabled — reversible, preserves history — so the
		// catalog never advertises an action whose daemon route is gone.
		for name, a := range have {
			if wanted[name] || !a.Active {
				continue
			}
			if err := c.deps.Client.DisableAction(ctx, tok, a.ID); err != nil {
				return fmt.Errorf("chat: retire %s/%s: %w", chatHandle, name, err)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if force {
		// Record the pushed revision so steady-state restarts don't churn, and let
		// every user re-sync the grants the auth re-push just revoked.
		c.st.AuthRev = rev
		if b, e := json.Marshal(c.st); e == nil && c.deps.SaveAppState != nil {
			_ = c.deps.SaveAppState(c.Slug(), b)
		}
		c.provisioned.Range(func(k, _ any) bool { c.provisioned.Delete(k); return true })
	}
	return nil
}

// authRev is a short, stable revision of the delegated_bearer auth config the
// plugin pushes — it changes exactly when authConfig() changes (e.g. a header
// rename). Persisted as chatState.AuthRev so an invisible auth-config drift can be
// forced back into line (the kernel never returns auth, R9).
func authRev() string {
	sum := sha256.Sum256(authConfig())
	return hex.EncodeToString(sum[:8])
}

// contractDrifted reports whether an existing @chat action's contract differs from
// the desired op on the fields the kernel *does* return — endpoint URL, description,
// either schema (compared order-independently), or the auth scheme/grant
// requirement (a dropped delegated_bearer grant). Auth *config* drift (write-only)
// is handled separately via authRev.
func contractDrifted(a client.Action, wantURL string, op daemon.Op) bool {
	if a.HTTP == nil || a.HTTP.URL != wantURL {
		return true
	}
	if a.AuthScheme != "delegated_bearer" || !a.RequiresGrant {
		return true
	}
	if a.Description != op.Description {
		return true
	}
	return !schemaEqual(a.InputSchema, op.InputSchema) || !schemaEqual(a.OutputSchema, op.OutputSchema)
}

// schemaEqual compares two JSON schemas structurally (order-independent). An
// unparseable schema is treated as unequal (forcing a reconcile).
func schemaEqual(x, y json.RawMessage) bool {
	var xv, yv any
	if json.Unmarshal(x, &xv) != nil || json.Unmarshal(y, &yv) != nil {
		return false
	}
	return reflect.DeepEqual(xv, yv)
}

// enablePublic activates a freshly created action and makes it public.
func (c *Chat) enablePublic(ctx context.Context, tok, id string) error {
	if err := c.deps.Client.EnableAction(ctx, tok, id); err != nil {
		return fmt.Errorf("chat: enable %s: %w", id, err)
	}
	pub := true
	if _, err := c.deps.Client.UpdateAction(ctx, tok, id, client.UpdateActionInput{Public: &pub}); err != nil {
		return fmt.Errorf("chat: publish %s: %w", id, err)
	}
	return nil
}

// authConfig is the delegated_bearer auth every @chat action carries: the kernel
// injects the caller's token into the seam's caller-key header, which the daemon's
// injected CallerIdentity.Authenticate reads.
func authConfig() json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"scheme": "delegated_bearer",
		"config": map[string]any{"header": apps.CallerKeyHeader, "template": "{token}"},
	})
	return b
}

// ---- per-user provisioning ----

// ensureProvisioned connects the signed-in user's deterministic caller token as a
// grant on every @chat action — once per handle per juice-ui run (an in-memory
// guard). Because the token is a stateless function of (signing key, handle), this
// re-points any stale or revoked grant at the current token and can never desync;
// it also migrates grants left carrying an older random key. It runs on the chat
// entry pages, before any write is possible.
func (c *Chat) ensureProvisioned(r *http.Request) {
	handle := c.deps.Handle(r)
	if handle == "" || c.id == nil {
		return
	}
	if _, done := c.provisioned.LoadOrStore(handle, struct{}{}); done {
		return
	}
	token := c.id.Token(handle)
	err := c.deps.Sessions.Do(r.Context(), func(tok string) error {
		for _, op := range daemon.Ops {
			if err := c.deps.Client.ConnectGrant(r.Context(), tok, chatHandle+"/"+op.Name, token); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		c.provisioned.Delete(handle) // let a later visit retry
	}
}

// ---- view models ----

type indexView struct {
	Rooms    []daemon.Room
	OpenForm bool
}

type roomView struct {
	Room     daemon.Room
	Me       string
	IsOwner  bool
	OpenForm string // which ?form= modal to render open (no-JS path); "" = none
	Msgs     msgsView
}

// msgsView is the data for the _chat_messages partial, shared by the full page and
// the live-update fragment. CSRF is carried here because a fragment has no Page
// envelope.
type msgsView struct {
	RoomID   string
	Me       string
	Messages []daemon.Message
	CSRF     template.HTML
}

// ---- browser handlers ----

func (c *Chat) getIndex(w http.ResponseWriter, r *http.Request) {
	me := c.deps.Handle(r)
	c.ensureProvisioned(r)
	rooms, err := c.dc.RoomsFor(r.Context(), me)
	if c.deps.HandleErr(w, r, err) {
		return
	}
	c.deps.RenderPage(w, r, http.StatusOK, "chat", "chat", indexView{
		Rooms:    rooms,
		OpenForm: r.URL.Query().Get("form") == "room-create",
	})
}

// getBadge serves the nav unread badge fragment the core polls: bare count as
// text/html, empty body when zero (matching the core steps-badge contract).
func (c *Chat) getBadge(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if c.dc == nil {
		return
	}
	if n, err := c.dc.UnreadTotal(r.Context(), c.deps.Handle(r)); err == nil && n > 0 {
		fmt.Fprintf(w, "%d", n)
	}
}

func (c *Chat) getRoom(w http.ResponseWriter, r *http.Request) {
	me := c.deps.Handle(r)
	c.ensureProvisioned(r)
	id := r.PathValue("id")
	room, msgs, err := c.dc.Room(r.Context(), me, id)
	if err != nil {
		c.deps.HandleErr(w, r, &client.KernelError{Kind: client.KindNotFound})
		return
	}
	c.markRead(r, me, id, msgs)
	c.deps.RenderPage(w, r, http.StatusOK, "chat_conversation", "chat", roomView{
		Room: room, Me: me, IsOwner: room.Creator == me,
		OpenForm: r.URL.Query().Get("form"),
		Msgs:     msgsView{RoomID: room.ID, Me: me, Messages: msgs, CSRF: c.deps.CSRFField(r)},
	})
}

// getMessages renders just the message list for the conversation page's htmx poll.
// It marks the room read up to the latest message shown ONLY when the tab is
// visible: the poll keeps running in a backgrounded tab (so content and badges stay
// fresh), but must not silently clear unread while the user isn't looking. app.js
// sends X-Visible from document.visibilityState; an absent header (stale asset,
// other client) is treated as visible, degrading to the prior always-mark behaviour
// rather than to "unread stuck forever". A non-member gets an empty list (no leak).
func (c *Chat) getMessages(w http.ResponseWriter, r *http.Request) {
	me := c.deps.Handle(r)
	id := r.PathValue("id")
	_, msgs, _ := c.dc.Room(r.Context(), me, id)
	if r.Header.Get("X-Visible") != "0" {
		c.markRead(r, me, id, msgs)
	}
	_ = c.deps.RenderFragment(w, http.StatusOK, "_chat_messages", msgsView{
		RoomID: id, Me: me, Messages: msgs, CSRF: c.deps.CSRFField(r),
	})
}

// markRead advances the viewer's watermark to the latest message shown.
func (c *Chat) markRead(r *http.Request, me, roomID string, msgs []daemon.Message) {
	if n := len(msgs); n > 0 {
		_ = c.dc.MarkRead(r.Context(), me, roomID, msgs[n-1].Seq)
	}
}

func (c *Chat) postCreateRoom(w http.ResponseWriter, r *http.Request) {
	name := roomName(r.FormValue("name"))
	if name == "" {
		c.deps.FlashRedirect(w, r, "/chat", "error", "A room needs a name.")
		return
	}
	reply, err := c.run(r, "create-room", map[string]any{"name": name, "members": parseHandles(r.FormValue("members"))})
	if c.deps.HandleErr(w, r, err) {
		return
	}
	var res struct {
		RoomID string `json:"room_id"`
	}
	_ = json.Unmarshal(reply.Result, &res)
	if res.RoomID == "" {
		http.Redirect(w, r, "/chat", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/chat/room/"+url.PathEscape(res.RoomID), http.StatusSeeOther)
}

func (c *Chat) postMessage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	text := strings.TrimSpace(r.FormValue("text"))
	if text != "" {
		if _, err := c.run(r, "message", map[string]any{"room": id, "text": text}); c.deps.HandleErr(w, r, err) {
			return
		}
	}
	http.Redirect(w, r, "/chat/room/"+url.PathEscape(id), http.StatusSeeOther)
}

func (c *Chat) postDeleteRoom(w http.ResponseWriter, r *http.Request) {
	if _, err := c.run(r, "delete-room", map[string]any{"room": r.PathValue("id")}); c.deps.HandleErr(w, r, err) {
		return
	}
	http.Redirect(w, r, "/chat", http.StatusSeeOther)
}

func (c *Chat) postLeave(w http.ResponseWriter, r *http.Request) {
	if _, err := c.run(r, "leave-room", map[string]any{"room": r.PathValue("id")}); c.deps.HandleErr(w, r, err) {
		return
	}
	http.Redirect(w, r, "/chat", http.StatusSeeOther)
}

func (c *Chat) postAddMember(w http.ResponseWriter, r *http.Request) {
	c.opRedirect(w, r, "add-member", map[string]any{"room": r.PathValue("id"), "member": strings.TrimSpace(r.FormValue("member"))})
}

func (c *Chat) postRemoveMember(w http.ResponseWriter, r *http.Request) {
	c.opRedirect(w, r, "remove-member", map[string]any{"room": r.PathValue("id"), "member": strings.TrimSpace(r.FormValue("member"))})
}

func (c *Chat) postDeleteMessage(w http.ResponseWriter, r *http.Request) {
	c.opRedirect(w, r, "delete-message", map[string]any{"room": r.PathValue("id"), "message": r.PathValue("mid")})
}

// ---- helpers ----

// run executes a @chat action as the signed-in user. The kernel dials the
// daemon synchronously and injects the caller's key, so the daemon has already
// applied (or rejected) the write by the time this returns — no polling.
func (c *Chat) run(r *http.Request, action string, args map[string]any) (client.CallReply, error) {
	var reply client.CallReply
	err := c.deps.Sessions.Do(r.Context(), func(tok string) error {
		var e error
		reply, e = c.deps.Client.Run(r.Context(), tok, chatHandle+"/"+action, mustJSON(args))
		return e
	})
	return reply, err
}

// opRedirect runs an action against the current room and redirects back to it.
func (c *Chat) opRedirect(w http.ResponseWriter, r *http.Request, action string, args map[string]any) {
	if _, err := c.run(r, action, args); c.deps.HandleErr(w, r, err) {
		return
	}
	http.Redirect(w, r, "/chat/room/"+url.PathEscape(r.PathValue("id")), http.StatusSeeOther)
}

// roomName sanitizes a display room name (trimmed; empty rejected).
func roomName(s string) string { return strings.TrimSpace(s) }

// parseHandles splits a free-form member list into @handles.
func parseHandles(s string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == ',' || r == '\n' || r == '\t' || r == '\r' }) {
		if strings.HasPrefix(f, "@") {
			out = append(out, f)
		}
	}
	return out
}

// newSecret returns a strong random hex secret for the @chat account password
// and the daemon admin secret.
func newSecret() string {
	var b [24]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
