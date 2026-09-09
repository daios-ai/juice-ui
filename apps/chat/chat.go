// Package chat is the juice-ui integration for the chat server (see
// apps/chat/daemon and docs/apps.md) — the reference implementation of the app
// seam.
//
// The daemon (apps/chat/daemon) is a self-contained, Juice-agnostic REST chat
// server (rooms, membership, messages) whose caller authentication is injected. This
// package is the only Juice- and UI-aware piece. Identity works the same for a
// browser user and a headless agent — a registration + verification flow, no juice-ui
// secret required:
//
//   - apps.ServiceAccount — the chat kernel account's own outbound identity
//     (provision, authenticate, backoff, self-heal), used to register the chat
//     actions and to mint steps off the outbox seed.
//   - chat's own API keys — inbound identity. A caller registers a handle via the
//     open chat/register action and receives a pending key; only that handle's owner
//     can complete the verification step (its onward, the private chat/validate-user,
//     activates the key). The key is then connected as the caller's chat/msg grant;
//     the kernel injects it on every chat/msg/* call and the daemon resolves it to
//     the handle. Only the key's hash is stored, so a store leak exposes nothing.
//
// The plugin runs the daemon in-process, mounts its write routes under the
// CSRF-exempt /_kernel/chat/ prefix (the chat action sources), reads via the daemon's
// REST Client, and writes by running chat/msg/* as the signed-in user. The browser's
// "Connect to chat" button runs the identical register→verify→connect path over the
// user's own session — no privileged provisioning path.
//
// The chat password, daemon admin secret, and the register/validate upstream secrets
// are provisioned on first run and persisted in state.json via the app-state store
// — no chat environment variables.
package chat

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/daios-ai/juice-ui/apps"
	"github.com/daios-ai/juice-ui/apps/chat/daemon"
	"github.com/daios-ai/juice-ui/client"
)

//go:embed templates/*.html
var tmplFS embed.FS

// chatHandle is the application's service-account handle — bare, like every
// v0.12 handle; the kernel canonicalizes a pre-0.4 "@chat" user to the same
// account, so existing state carries over.
const chatHandle = "chat"

// outboxName is chat's private, price-0, owner-only action that opens the outbox
// seed. chat runs it (paying itself 0), and its dispatch handler creates the
// sentinel step via the trace capability — no sys/message toll, so chat needs no
// credits on any kernel. Its handler is the plugin (not a daemon Op) because the
// capability callback is a Juice call; the daemon stays Juice-agnostic.
const outboxName = "outbox"

// registerName is chat's open, no-grant action: a caller registers a handle and
// receives a pending API key plus a verification step. validateName is the step's
// private onward that activates the key. Both live outside the msg/ folder so the
// chat/msg grant selector covers only the member actions.
const registerName = "register"
const validateName = "validate-user"

// ackName is chat's private, price-0, no-op action used as the onward of the outbox
// sentinel and every doorbell step. It must be chat-owned and priced 0 so minting a
// doorbell parks 0 from chat's wallet — depending on sys/sink here would tie the
// cost of a notification to an operator-priced action chat does not control.
const ackName = "ack"

// chatDBName is the daemon store filename, kept in the component home ($JUICE_HOME/ui).
const chatDBName = "chat.db"

// chatIcon is the plugin's own nav icon (a speech bubble), inline so no core
// sprite edit is needed.
const chatIcon = template.HTML(`<svg class="ico" aria-hidden="true" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z"/></svg>`)

// Chat is the chat UI plugin. It owns the in-process chat daemon and its Client. Its
// outbound identity is the seam ServiceAccount (the chat account); its inbound
// identity is chat's own API keys — the daemon resolves an injected key to a handle
// via its token store, which the plugin also drives to register and verify keys.
type Chat struct {
	deps   apps.Deps
	daemon *daemon.Daemon
	dc     *daemon.Client
	store  *daemon.Store        // the daemon's store; the plugin mints tokens on it (register)
	sa     *apps.ServiceAccount // chat kernel account (provision + auth + self-heal)
	dbPath string               // overridable in tests; "" -> in-memory store
	st     chatState            // persisted config; written by ensureActions (the SA setup goroutine)

	seedTrace  string // chat's open outbox trace, minted doorbells/verifications hang off it (from st.SeedTrace)
	ackID      string // chat/ack action id — the price-0 onward of the sentinel and doorbell steps
	validateID string // chat/validate-user action id, a verification step's onward (resolved at seed setup)
}

// New constructs the chat plugin. It needs no configuration — it provisions the
// chat account and daemon secret itself on Start and reaches the kernel through
// Deps.
func New() *Chat { return &Chat{} }

func (c *Chat) Slug() string { return "chat" }

// Nav contributes the "chat" sidebar entry. Its badge is the count of the user's
// outstanding chat doorbell steps — the core step badge (all steps) filtered to
// chat, one source of truth. The count is filled by the /chat/badge poll (Nav has
// no session token to read the kernel steps at render time), so the initial value
// is 0 and the poll corrects it within the badge interval.
func (c *Chat) Nav(handle string, superuser bool) []apps.NavEntry {
	if handle == "" {
		return nil
	}
	return []apps.NavEntry{{Slug: "chat", Label: "chat", Href: "/chat", Icon: chatIcon, Match: "/chat", BadgeURL: "/chat/badge"}}
}

func (c *Chat) Templates() fs.FS { return tmplFS }

// Mount registers the browser routes (auth-gated) and mounts the daemon's
// kernel-facing write routes under /_kernel/chat/ (CSRF-exempt — the kernel calls
// them server-to-server, authenticated by the injected per-user key).
func (c *Chat) Mount(mux *http.ServeMux, d apps.Deps) {
	c.deps = d
	mux.HandleFunc("GET /chat", d.RequireAuth(c.getIndex))
	mux.HandleFunc("POST /chat/connect", d.RequireAuth(c.postConnect))
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

// kernelDispatch routes a kernel callback under /_kernel/chat/ (path already stripped
// of that prefix). The private chat/outbox action is handled by the plugin (it makes
// a Juice capability callback); everything else is a daemon write route. Keeping
// /outbox under this same prefix inherits its CSRF exemption.
func (c *Chat) kernelDispatch(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/" + outboxName:
		c.serveOutbox(w, r)
		return
	case "/" + registerName:
		c.serveRegister(w, r)
		return
	case "/" + ackName:
		// The doorbell/sentinel onward: a no-op. Completing a doorbell runs this; there
		// is nothing to do (unread is the step itself, now completed). It exists only so
		// chat owns and prices-0 the onward instead of depending on sys/sink.
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
		return
	}
	if c.daemon == nil {
		http.Error(w, "chat starting", http.StatusServiceUnavailable)
		return
	}
	c.daemon.KernelHandler().ServeHTTP(w, r)
}

// serveRegister handles the kernel's dispatch of the open chat/register action: it
// mints a pending API key for the requested handle and sends that handle a
// verification step (onward chat/validate-user) minted off the outbox seed — so the
// step is owned by chat and its private onward is callable. It returns {api_key,
// step_id}; the key activates only when the handle's owner completes the step. It is a
// plugin handler (not a daemon Op) because minting the step is a Juice call. The
// register action's own bearer secret (injected by the kernel) gates it, so only the
// kernel can drive registration.
func (c *Chat) serveRegister(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Header.Get("Authorization") != "Bearer "+c.st.RegisterSecret {
		writeErrJSON(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var in struct {
		Username string `json:"username"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	if in.Username == "" {
		writeErrJSON(w, http.StatusBadRequest, "username required")
		return
	}
	if c.seedTrace == "" || c.validateID == "" || c.store == nil {
		writeErrJSON(w, http.StatusServiceUnavailable, "chat starting")
		return
	}
	rawKey, regID, err := c.store.RegisterToken(in.Username)
	if err != nil {
		writeErrJSON(w, http.StatusInternalServerError, "register failed")
		return
	}
	var stepID string
	err = c.sa.Do(r.Context(), func(tok string) error {
		st, e := c.deps.Client.CreateStep(r.Context(), tok, client.CreateStepInput{
			TraceID:        c.seedTrace,
			Action:         c.validateID,
			RequiredCaller: in.Username,
			PartialArgs:    mustJSON(map[string]any{"reg_id": regID}),
		})
		if e == nil {
			stepID = st.ID
		}
		return e
	})
	if err != nil || stepID == "" {
		slog.Warn("chat register: verification step mint failed", "username", in.Username, "err", errText(err))
		writeErrJSON(w, http.StatusInternalServerError, "could not send verification step")
		return
	}
	// Exactly the declared output shape {api_key, step_id} — the kernel validates the
	// response against register's output_schema and rejects any undeclared key.
	_, _ = fmt.Fprintf(w, `{"api_key":%q,"step_id":%q}`, rawKey, stepID)
}

// serveOutbox handles the kernel's dispatch of chat/outbox: it reads the injected
// trace-capability headers and calls back to create the never-completed sentinel step
// that holds the outbox seed process open (the mint-able trace notify() hangs
// doorbells off). chat pays itself 0 (the action is price 0). Returns the sentinel
// id as {step_id} so ensureSeed can persist it. A leaf response (no capability) is a
// harmless no-op.
func (c *Chat) serveOutbox(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	cap, cb := r.Header.Get(client.CapabilityHeader), r.Header.Get(client.CallbackHeader)
	stepID := ""
	if cap != "" && cb != "" && c.ackID != "" {
		st, err := c.deps.Client.CallbackCreateStep(r.Context(), cb, cap, client.CreateStepInput{
			Action:         c.ackID,
			RequiredCaller: chatHandle,
		})
		if err != nil {
			slog.Warn("chat outbox: capability callback failed", "callback", cb, "ack", c.ackID, "err", err.Error())
		} else {
			stepID = st.ID
		}
	} else {
		slog.Warn("chat outbox: dispatch missing inputs", "cap_present", cap != "", "callback", cb, "ack_present", c.ackID != "")
	}
	_, _ = fmt.Fprintf(w, `{"step_id":%q}`, stepID)
}

// chatState is the plugin's persisted config: the chat account password, the daemon
// admin secret, the register/validate upstream secrets (the bearer credential the
// kernel injects when it dispatches those actions, proving the caller is the kernel),
// and AuthRev — the revision of the delegated_bearer auth config last pushed to the
// kernel. AuthRev exists because the kernel never returns an action's auth config
// (write-only, R9), so auth-config drift (e.g. a changed header) is invisible to a
// read-based reconciler; persisting the revision lets the plugin force a re-push when
// its desired auth changes.
type chatState struct {
	Password       string `json:"password"`
	AdminSecret    string `json:"admin_secret"`
	RegisterSecret string `json:"register_secret"`
	ValidateSecret string `json:"validate_secret"`
	AuthRev        string `json:"auth_rev"`
	// SeedTrace / SeedStep identify chat's open "outbox" process — opened by running
	// the private chat/outbox action, whose never-completed sentinel step (SeedStep)
	// holds the process open and the trace (SeedTrace) mint-able. Doorbell steps are
	// minted off it. Both survive kernel restarts; re-established only when gone.
	SeedTrace string `json:"seed_trace"`
	SeedStep  string `json:"seed_step"`
}

// Start provisions credentials (once), opens the durable store, brings up the
// in-process daemon, and — in the background — authenticates as chat and
// registers the chat/* actions. Backgrounded so juice-ui starts even when the
// kernel is briefly unavailable.
func (c *Chat) Start(ctx context.Context) error {
	var st chatState
	if raw, err := c.deps.LoadAppState(c.Slug()); err == nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, &st)
	}
	changed := false
	for _, f := range []*string{&st.Password, &st.AdminSecret, &st.RegisterSecret, &st.ValidateSecret} {
		if *f == "" {
			*f, changed = newSecret(), true
		}
	}
	if changed {
		b, _ := json.Marshal(st)
		if err := c.deps.SaveAppState(c.Slug(), b); err != nil {
			return fmt.Errorf("chat: persist credentials: %w", err)
		}
	}
	c.st = st // ensureActions reads AuthRev and rewrites it after an auth re-push

	store, err := daemon.Open(c.dbPathOrDefault())
	if err != nil {
		return fmt.Errorf("chat: open store: %w", err)
	}
	c.store = store
	// Inbound identity: the kernel injects the caller's connected chat/msg key into
	// the caller-key header; the daemon's auth hook resolves it (by hash) to the
	// registered handle. An unknown/inactive key is an unauthenticated caller.
	authHook := func(r *http.Request) (string, bool) {
		u, active, err := store.ResolveToken(r.Header.Get(apps.CallerKeyHeader))
		return u, err == nil && active && u != ""
	}
	c.daemon = daemon.New(store, st.AdminSecret, st.ValidateSecret, authHook, c.notify)
	c.dc = daemon.NewClient(c.daemon)

	// The chat kernel account. The seam ServiceAccount owns its whole lifecycle
	// — provision, authenticate, backoff, state-aware logging, self-heal — so a
	// suspended or briefly-unreachable chat degrades gracefully instead of
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
// override; then "" (in-memory) when there is no persistent app state, so tests
// write no file; then the component home only ($JUICE_HOME/ui/chat.db). No
// legacy-path fallback — see the note in cmd/juice-ui config.go: silent legacy
// resolution is what let chat credentials drift.
func (c *Chat) dbPathOrDefault() string {
	if c.dbPath != "" {
		return c.dbPath
	}
	if c.deps.StatePath == "" {
		return ""
	}
	home := c.deps.Home
	if home == "" {
		home = filepath.Dir(c.deps.StatePath) // fallback: beside state.json
	}
	_ = os.MkdirAll(home, 0o700)
	return filepath.Join(home, chatDBName)
}

// ensureActions makes each chat action exist, active, public, and pointed at the
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
		existing, err := c.deps.Client.ListActions(ctx, tok, chatHandle, "", true, 0, 0)
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
		// Actions reconciled separately (not daemon Ops) — keep them off the
		// orphan-retirement list: the price-0 ack onward, the private outbox seed, the
		// open register, and the private validate-user.
		wanted[ackName] = true
		wanted[outboxName] = true
		wanted[registerName] = true
		wanted[validateName] = true
		base := strings.TrimRight(c.deps.PublicURL, "/") + "/_kernel/chat/"
		auth := authConfig()
		visPublic := "public"
		// Reconcile every op independently and accumulate failures rather than
		// aborting on the first: a transient failure on one action must not starve
		// the rest (a fail-fast loop once left delete-message private and inbox
		// uncreated, cascading into missing grants). AuthRev is recorded only when
		// the whole pass is clean (below), so a partial pass retries next time.
		var errs []error
		for _, op := range daemon.Ops {
			op := op
			errs = append(errs, func() error {
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
					return nil
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
						Visibility:   &visPublic,
					}); err != nil {
						return fmt.Errorf("chat: reconcile %s/%s: %w", chatHandle, op.Name, err)
					}
					if _, err := c.deps.Client.EnableAction(ctx, tok, a.ID); err != nil {
						return fmt.Errorf("chat: enable %s/%s: %w", chatHandle, op.Name, err)
					}
					return nil
				}
				// Contract already correct — only nudge active/public state if needed.
				if !a.Active {
					if _, err := c.deps.Client.EnableAction(ctx, tok, a.ID); err != nil {
						return fmt.Errorf("chat: enable %s/%s: %w", chatHandle, op.Name, err)
					}
				}
				if !a.IsPublic() {
					if _, err := c.deps.Client.UpdateAction(ctx, tok, a.ID, client.UpdateActionInput{Visibility: &visPublic}); err != nil {
						return fmt.Errorf("chat: publish %s/%s: %w", chatHandle, op.Name, err)
					}
				}
				return nil
			}())
		}
		// Retire orphans: an active chat action no longer in Ops (e.g. a removed op
		// like rename-room) is disabled — reversible, preserves history — so the
		// catalog never advertises an action whose daemon route is gone.
		for name, a := range have {
			if wanted[name] || !a.Active {
				continue
			}
			if _, err := c.deps.Client.DisableAction(ctx, tok, a.ID); err != nil {
				errs = append(errs, fmt.Errorf("chat: retire %s/%s: %w", chatHandle, name, err))
			}
		}
		return errors.Join(errs...)
	})
	if err != nil {
		return err
	}
	if force {
		// Record the pushed revision so steady-state restarts don't churn. An auth
		// re-push revokes existing chat/msg grants; users re-establish them by
		// clicking "Connect to chat" again (there is no silent re-provisioning).
		c.st.AuthRev = rev
		if b, e := json.Marshal(c.st); e == nil && c.deps.SaveAppState != nil {
			_ = c.deps.SaveAppState(c.Slug(), b)
		}
	}
	// Ensure chat holds an open outbox trace to mint doorbell steps off. Done after
	// action reconciliation and within the same backoff-retried setup, so a briefly
	// unreachable kernel just retries.
	return c.sa.Do(ctx, func(tok string) error { return c.ensureSeed(ctx, tok) })
}

// outbox action schemas — activation requires described properties; it takes none.
var (
	outboxInSchema  = json.RawMessage(`{"type":"object","properties":{}}`)
	outboxOutSchema = json.RawMessage(`{"type":"object","properties":{"step_id":{"type":"string","description":"Id of the sentinel step that holds the outbox process open."}}}`)
)

const outboxDesc = "Opens the chat outbox seed by creating a sentinel step via the trace capability; called only by chat."

// register / validate-user action contracts. register is public (anyone may request a
// key for a handle); validate-user is private (reached only as the verification
// step's onward). Both carry a bearer upstream secret so the daemon/plugin trusts that
// the kernel is the caller.
var (
	registerInSchema  = json.RawMessage(`{"type":"object","properties":{"username":{"type":"string","description":"The handle to issue a chat key for, for example agent. Handles are bare — no @ prefix. A verification step is sent to this handle."}}}`)
	registerOutSchema = json.RawMessage(`{"type":"object","properties":{"api_key":{"type":"string","description":"The chat API key. Connect it with: juice user connect chat/msg --token <api_key>. It activates once the handle's owner accepts the verification step."},"step_id":{"type":"string","description":"Id of the verification step sent to the handle; complete it (as that handle) to activate the key."}}}`)
	validateInSchema  = json.RawMessage(`{"type":"object","properties":{"reg_id":{"type":"string","description":"Registration id carried by the verification step; names which pending key to activate."}}}`)
	validateOutSchema = json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean","description":"True when the key was activated."}}}`)
)

const (
	registerDesc = "Request a chat API key for a handle. Returns a pending key and sends a verification step to that handle; the key activates when the handle's owner accepts. Then connect the key to chat/msg to use chat."
	validateDesc = "Activate the chat key for a completed registration. Invoked only as the verification step's onward, when the handle's owner accepts."
)

// chat/ack is the price-0 no-op onward of the sentinel and doorbell steps. Its input
// mirrors a doorbell's partial_args ({room, room_name}) so the completed step's merged
// args validate against it.
var (
	ackInSchema  = json.RawMessage(`{"type":"object","properties":{"room":{"type":"string","description":"Id of the room the doorbell is for."},"room_name":{"type":"string","description":"Display name of that room."}}}`)
	ackOutSchema = json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean","description":"Always true; completing a doorbell is a no-op."}}}`)
)

const ackDesc = "No-op onward for chat doorbell and sentinel steps; completing a doorbell dismisses it and does nothing else. Owned by chat and priced 0."

// bearerAuth is the upstream auth config for chat/register and chat/validate-user:
// the kernel injects Authorization: Bearer <secret> when it dispatches the action, so
// the endpoint can trust the caller is the kernel. It carries no per-caller credential
// (requires_grant is false), so a fresh caller can invoke chat/register with no grant.
func bearerAuth(secret string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"scheme":  "bearer",
		"secrets": map[string]any{"token": secret},
	})
	return b
}

// ensureSeed makes sure chat holds an open "outbox" trace to mint doorbell steps off
// (see chatState.SeedTrace). It resolves sys/sink's id (the sentinel's and every
// doorbell's onward), makes the private chat/outbox action exist, then reuses a
// persisted seed whose sentinel is still waiting — or opens a fresh one by running
// chat/outbox (price 0, owner-called, so chat pays itself 0). The outbox handler
// (serveOutbox) creates the never-completed sentinel via the trace capability, holding
// the process open and the trace mint-able. No sys/message toll ⇒ chat needs no
// credits on any kernel. Idempotent and restart-safe (the seed survives kernel restarts).
func (c *Chat) ensureSeed(ctx context.Context, tok string) error {
	// The actions reconciled outside the public msg/ loop: the price-0 no-op ack (the
	// sentinel/doorbell onward — its id is needed to mint those steps), the private
	// outbox (seed), the open register, and the private validate-user.
	if c.ackID == "" {
		id, err := c.ensureSpecialAction(ctx, tok, specialAction{
			// local (not private): a doorbell/message step's required_caller is
			// another member, who must be able to call this onward (kernel requirement).
			name: ackName, vis: "local", in: ackInSchema, out: ackOutSchema, desc: ackDesc,
		})
		if err != nil {
			return err
		}
		c.ackID = id
	}
	if _, err := c.ensureSpecialAction(ctx, tok, specialAction{
		name: outboxName, public: false, in: outboxInSchema, out: outboxOutSchema, desc: outboxDesc,
	}); err != nil {
		return err
	}
	if _, err := c.ensureSpecialAction(ctx, tok, specialAction{
		name: registerName, public: true, auth: bearerAuth(c.st.RegisterSecret),
		in: registerInSchema, out: registerOutSchema, desc: registerDesc,
	}); err != nil {
		return err
	}
	vid, err := c.ensureSpecialAction(ctx, tok, specialAction{
		// local (not private): the verification step's required_caller is the
		// registrant, who must be able to call this onward (kernel requirement).
		name: validateName, vis: "local", auth: bearerAuth(c.st.ValidateSecret),
		in: validateInSchema, out: validateOutSchema, desc: validateDesc,
	})
	if err != nil {
		return err
	}
	c.validateID = vid
	// Reuse a live seed: the sentinel step must still be waiting.
	if c.st.SeedTrace != "" && c.st.SeedStep != "" {
		steps, err := c.deps.Client.ListSteps(ctx, tok, "", "waiting")
		if err != nil {
			return err
		}
		for _, s := range steps {
			if s.ID == c.st.SeedStep {
				c.seedTrace = c.st.SeedTrace // sentinel alive — reuse
				return nil
			}
		}
	}
	reply, err := c.deps.Client.Run(ctx, tok, chatHandle+"/"+outboxName, mustJSON(map[string]any{}), "")
	if err != nil {
		return fmt.Errorf("chat: open outbox seed: %w", err)
	}
	var res struct {
		StepID string `json:"step_id"`
	}
	_ = json.Unmarshal(reply.Result, &res)
	if res.StepID == "" {
		return fmt.Errorf("chat: outbox produced no sentinel step (capability callback failed?)")
	}
	c.seedTrace = reply.TraceID
	c.st.SeedTrace = reply.TraceID
	c.st.SeedStep = res.StepID
	if b, e := json.Marshal(c.st); e == nil && c.deps.SaveAppState != nil {
		_ = c.deps.SaveAppState(c.Slug(), b)
	}
	return nil
}

// specialAction is a chat action reconciled outside the public msg/ op loop —
// outbox (private, no auth), register (public, bearer), validate-user (private,
// bearer). Each has a fixed source (its plugin/daemon route) and a stable auth config,
// so it needs neither contractDrifted (which assumes public delegated_bearer) nor the
// AuthRev machinery (its secret never rotates within a state.json).
type specialAction struct {
	name string
	// vis is the action's caller-scope: "" derives from public (public→"public",
	// else "private"). Set "local" for a step ONWARD whose required_caller is an
	// ordinary user (e.g. validate-user, ack): the kernel requires the step's
	// required_caller to be able to call the onward, and a peer never can, so
	// "local" (any local caller, still bearer-gated) is the minimal scope that lets
	// the verification/doorbell steps be minted. "private" would 403 the mint.
	vis     string
	public  bool
	auth    json.RawMessage // nil for the no-auth owner-called outbox
	in, out json.RawMessage
	desc    string
}

// visibilityOf resolves a specialAction's target visibility.
func (sp specialAction) visibilityOf() string {
	if sp.vis != "" {
		return sp.vis
	}
	if sp.public {
		return "public"
	}
	return "private"
}

// ensureSpecialAction makes one specialAction exist, active, at its route, with the
// right visibility, and returns its id. It reconciles the source URL and public flag
// (the fields the kernel returns); the auth config, being write-only and stable, is
// pushed on create only.
func (c *Chat) ensureSpecialAction(ctx context.Context, tok string, sp specialAction) (string, error) {
	want := strings.TrimRight(c.deps.PublicURL, "/") + "/_kernel/chat/" + sp.name
	existing, err := c.deps.Client.ListActions(ctx, tok, chatHandle, "", true, 0, 0)
	if err != nil {
		return "", err
	}
	var a *client.Action
	for i := range existing {
		if existing[i].Name == sp.name {
			a = &existing[i]
			break
		}
	}
	vis := sp.visibilityOf()
	if a == nil {
		na, err := c.deps.Client.CreateAction(ctx, tok, client.CreateActionInput{
			Name: sp.name, Kind: "http", Source: want, Method: "POST",
			Description: sp.desc, Price: 0, InputSchema: sp.in, OutputSchema: sp.out, Auth: sp.auth,
		})
		if err != nil {
			return "", fmt.Errorf("chat: create %s/%s: %w", chatHandle, sp.name, err)
		}
		if _, err := c.deps.Client.EnableAction(ctx, tok, na.ID); err != nil {
			return "", fmt.Errorf("chat: enable %s/%s: %w", chatHandle, sp.name, err)
		}
		if vis != "private" { // private is the create default; widen when needed
			if _, err := c.deps.Client.UpdateAction(ctx, tok, na.ID, client.UpdateActionInput{Visibility: &vis}); err != nil {
				return "", fmt.Errorf("chat: set visibility %s/%s: %w", chatHandle, sp.name, err)
			}
		}
		return na.ID, nil
	}
	if a.HTTP == nil || a.HTTP.URL != want {
		desc := sp.desc
		if _, err := c.deps.Client.UpdateAction(ctx, tok, a.ID, client.UpdateActionInput{
			Source: &want, Method: "POST", InputSchema: sp.in, OutputSchema: sp.out,
			Description: &desc, Visibility: &vis, Auth: sp.auth,
		}); err != nil {
			return "", fmt.Errorf("chat: reconcile %s/%s: %w", chatHandle, sp.name, err)
		}
		if _, err := c.deps.Client.EnableAction(ctx, tok, a.ID); err != nil {
			return "", fmt.Errorf("chat: enable %s/%s: %w", chatHandle, sp.name, err)
		}
		return a.ID, nil
	}
	if !a.Active {
		if _, err := c.deps.Client.EnableAction(ctx, tok, a.ID); err != nil {
			return "", fmt.Errorf("chat: enable %s/%s: %w", chatHandle, sp.name, err)
		}
	}
	if a.Vis() != vis {
		if _, err := c.deps.Client.UpdateAction(ctx, tok, a.ID, client.UpdateActionInput{Visibility: &vis}); err != nil {
			return "", fmt.Errorf("chat: set visibility %s/%s: %w", chatHandle, sp.name, err)
		}
	}
	return a.ID, nil
}

// authRev is a short, stable revision of the delegated_bearer auth config the
// plugin pushes — it changes exactly when authConfig() changes (e.g. a header
// rename). Persisted as chatState.AuthRev so an invisible auth-config drift can be
// forced back into line (the kernel never returns auth, R9).
func authRev() string {
	sum := sha256.Sum256(authConfig())
	return hex.EncodeToString(sum[:8])
}

// contractDrifted reports whether an existing chat action's contract differs from
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
	if _, err := c.deps.Client.EnableAction(ctx, tok, id); err != nil {
		return fmt.Errorf("chat: enable %s: %w", id, err)
	}
	visPublic := "public"
	if _, err := c.deps.Client.UpdateAction(ctx, tok, id, client.UpdateActionInput{Visibility: &visPublic}); err != nil {
		return fmt.Errorf("chat: publish %s: %w", id, err)
	}
	return nil
}

// authConfig is the delegated_bearer auth every chat/msg member action carries: the
// kernel injects the caller's connected key into the seam's caller-key header, which
// the daemon's injected auth hook (store.ResolveToken) reads to recover the handle.
func authConfig() json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"scheme": "delegated_bearer",
		"config": map[string]any{"header": apps.CallerKeyHeader, "template": "{token}"},
	})
	return b
}

// ---- connection (Connect to chat) ----

// msgSelector is the grant selector covering every member action (the msg/ folder). A
// user connects their chat key here; a user is "connected" iff a grant under it exists.
const msgSelector = chatHandle + "/msg"

// connected reports whether the signed-in user holds a chat/msg grant — read from the
// kernel's own connectors (/v1/me), the same state a headless agent's `user connect`
// creates. It is the gate for the rooms UI.
func (c *Chat) connected(r *http.Request) bool {
	ok := false
	_ = c.deps.Sessions.Do(r.Context(), func(tok string) error {
		me, e := c.deps.Client.Me(r.Context(), tok)
		if e != nil {
			return e
		}
		for _, con := range me.Connectors {
			if con.Directory == msgSelector {
				ok = true
				return nil
			}
			for _, g := range con.Actions {
				if strings.HasPrefix(g.Action, msgSelector+"/") {
					ok = true
					return nil
				}
			}
		}
		return nil
	})
	return ok
}

// postConnect runs the whole onboarding dance as the signed-in user — the identical
// kernel path a headless agent uses, nothing juice-ui-privileged: (1) run the open
// chat/register for the user's own handle to get a pending key + verification step;
// (2) complete the step (the user is its required_caller) to activate the key; (3)
// connect the key as the chat/msg grant. The key never reaches the browser. On
// success the rooms page renders; the kernel connector is now the source of truth.
func (c *Chat) postConnect(w http.ResponseWriter, r *http.Request) {
	// Already connected (e.g. a double-submit, or a reconnect where the grant
	// survived): nothing to do — the kernel connector is the source of truth.
	if c.connected(r) {
		http.Redirect(w, r, "/chat", http.StatusSeeOther)
		return
	}
	handle := c.deps.Handle(r)
	// stage names the step that failed, so a reconnect failure is diagnosable
	// instead of collapsing into one opaque flash.
	stage := "register"
	err := c.deps.Sessions.Do(r.Context(), func(tok string) error {
		reply, e := c.deps.Client.Run(r.Context(), tok, chatHandle+"/"+registerName,
			mustJSON(map[string]any{"username": handle}), "")
		if e != nil {
			return e
		}
		var res struct {
			APIKey string `json:"api_key"`
			StepID string `json:"step_id"`
		}
		if e := json.Unmarshal(reply.Result, &res); e != nil {
			return e
		}
		if res.APIKey == "" || res.StepID == "" {
			return fmt.Errorf("chat: register returned no key/step")
		}
		stage = "verification"
		if _, e := c.deps.Client.CompleteStep(r.Context(), tok, res.StepID, nil); e != nil {
			return e
		}
		stage = "attach"
		_, e = c.deps.Client.ConnectGrant(r.Context(), tok, msgSelector, "", res.APIKey)
		return e
	})
	if err != nil {
		// The attach may have partly succeeded; if the grant now exists, treat the
		// reconnect as done rather than surfacing a spurious error.
		if c.connected(r) {
			http.Redirect(w, r, "/chat", http.StatusSeeOther)
			return
		}
		detail := "please try again"
		if ke, ok := client.AsKernelError(err); ok {
			detail = ke.UserMessage()
		}
		c.deps.FlashRedirect(w, r, "/chat", "error", "Could not connect to chat ("+stage+" failed): "+detail)
		return
	}
	http.Redirect(w, r, "/chat", http.StatusSeeOther)
}

// ---- view models ----

type indexView struct {
	Connected bool // false → render the "Connect to chat" gate instead of rooms
	Rooms     []daemon.Room
	OpenForm  bool
}

type roomView struct {
	Room     daemon.Room
	Rooms    []daemon.Room // the full rail (two-pane layout); the current room is .Room
	Me       string
	IsOwner  bool
	OpenForm string // which ?form= modal to render open (no-JS path); "" = none
	Msgs     msgsView
}

// msgCard is one message plus the chrome its card template needs; each card carries
// a stable DOM id (msg-<id>) so the poll can append/remove without touching others.
type msgCard struct {
	Msg    daemon.Message
	Me     string
	RoomID string
	CSRF   template.HTML
}

func makeCards(msgs []daemon.Message, me, roomID string, csrf template.HTML) []msgCard {
	cards := make([]msgCard, len(msgs))
	for i, m := range msgs {
		cards[i] = msgCard{Msg: m, Me: me, RoomID: roomID, CSRF: csrf}
	}
	return cards
}

// maxSeq is the newest seq in an oldest-first window (0 if empty) — the poll's
// after watermark.
func maxSeq(msgs []daemon.Message) int64 {
	if len(msgs) == 0 {
		return 0
	}
	return msgs[len(msgs)-1].Seq
}

// msgsView backs both the initial conversation render and the poll fragment. Cards
// is the whole window initially, only the new ones on a poll; After is the watermark;
// Deleted/HadAdded are poll-only (ids to OOB-remove, drop-placeholder flag).
type msgsView struct {
	RoomID   string
	Me       string
	CSRF     template.HTML
	Cards    []msgCard
	After    int64
	HadAdded bool
	Deleted  []string
}

// ---- browser handlers ----

func (c *Chat) getIndex(w http.ResponseWriter, r *http.Request) {
	me := c.deps.Handle(r)
	// Gate on the kernel connector (the same state a headless agent's `user connect`
	// creates): with no chat/msg grant the user can't call the member actions, so
	// render the one-click Connect gate instead of rooms.
	if !c.connected(r) {
		c.deps.RenderPage(w, r, http.StatusOK, "chat", "chat", indexView{Connected: false})
		return
	}
	rooms, err := c.dc.RoomsFor(r.Context(), me)
	if c.deps.HandleErr(w, r, err) {
		return
	}
	// Unread is the doorbell steps, not a watermark: bold the rooms the user has an
	// outstanding chat doorbell for, and float them to the top (RoomsFor is
	// recency-ordered; a stable sort keeps that within each group).
	unread, _ := c.chatUnread(r)
	for i := range rooms {
		if unread[rooms[i].ID] {
			rooms[i].Unread = 1
		}
	}
	sort.SliceStable(rooms, func(i, j int) bool { return rooms[i].Unread > rooms[j].Unread })
	c.deps.RenderPage(w, r, http.StatusOK, "chat", "chat", indexView{
		Connected: true,
		Rooms:     rooms,
		OpenForm:  r.URL.Query().Get("form") == "room-create",
	})
}

// getBadge serves the chat nav-badge fragment the core polls: the count of the
// user's outstanding chat doorbells (bare number, empty when zero — the core
// steps-badge contract). It is the core step badge filtered to chat — one source
// of truth (the doorbell steps), never a separate watermark.
func (c *Chat) getBadge(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if _, n := c.chatUnread(r); n > 0 {
		fmt.Fprintf(w, "%d", n)
	}
}

// chatUnread reads the signed-in user's outstanding chat doorbells straight from
// the kernel steps — the single source of truth for chat unread. A doorbell is a
// waiting step owned by chat addressed to the user; its partial_args names the
// room. Returns the set of unread rooms and the count. Both the app-badge and the
// room-list unread are just this: the bell (all waiting steps) filtered to chat.
func (c *Chat) chatUnread(r *http.Request) (map[string]bool, int) {
	rooms := map[string]bool{}
	if c.deps.Sessions == nil {
		return rooms, 0
	}
	err := c.deps.Sessions.Do(r.Context(), func(tok string) error {
		steps, e := c.deps.Client.ListSteps(r.Context(), tok, "", "waiting")
		if e != nil {
			return e
		}
		for _, s := range steps {
			if s.OwnerHandle != chatHandle {
				continue // not a chat doorbell
			}
			var pa struct {
				Room string `json:"room"`
			}
			_ = json.Unmarshal(s.PartialArgs, &pa)
			if pa.Room != "" {
				rooms[pa.Room] = true
			}
		}
		return nil
	})
	if err != nil {
		return map[string]bool{}, 0
	}
	return rooms, len(rooms)
}

func (c *Chat) getRoom(w http.ResponseWriter, r *http.Request) {
	me := c.deps.Handle(r)
	if !c.connected(r) {
		http.Redirect(w, r, "/chat", http.StatusSeeOther) // send them through the Connect gate
		return
	}
	id := r.PathValue("id")
	room, msgs, err := c.dc.Room(r.Context(), me, id)
	if err != nil {
		c.deps.HandleErr(w, r, &client.KernelError{Kind: client.KindNotFound})
		return
	}
	c.dismiss(r, me, id)
	rooms, _ := c.dc.RoomsFor(r.Context(), me) // the rail; best-effort
	csrf := c.deps.CSRFField(r)
	c.deps.RenderPage(w, r, http.StatusOK, "chat_conversation", "chat", roomView{
		Room: room, Rooms: rooms, Me: me, IsOwner: room.Creator == me,
		OpenForm: r.URL.Query().Get("form"),
		Msgs: msgsView{RoomID: room.ID, Me: me, CSRF: csrf,
			Cards: makeCards(msgs, me, room.ID, csrf), After: maxSeq(msgs)},
	})
}

// getMessages is the conversation's poll: an incremental read (writePoll) plus the
// visible-tab doorbell dismiss. X-Visible (from app.js) keeps a backgrounded tab
// from silently clearing unread; an absent header counts as visible.
func (c *Chat) getMessages(w http.ResponseWriter, r *http.Request) {
	me := c.deps.Handle(r)
	id := r.PathValue("id")
	if r.Header.Get("X-Visible") != "0" {
		c.dismiss(r, me, id)
	}
	c.writePoll(w, r, me, id)
}

// writePoll renders the _chat_poll fragment: messages newer than the client's `after`
// watermark (appended, so no existing node is re-rendered — the scroll-anchor fix)
// plus OOB removals for recently-deleted ones. Shared by the poll and the compose
// submit; `after` arrives via FormValue (GET query or POST body).
func (c *Chat) writePoll(w http.ResponseWriter, r *http.Request, me, id string) {
	after, _ := strconv.ParseInt(r.FormValue("after"), 10, 64)
	added, deleted, _ := c.dc.Poll(r.Context(), me, id, after)
	for _, m := range added {
		if m.Seq > after {
			after = m.Seq
		}
	}
	csrf := c.deps.CSRFField(r)
	_ = c.deps.RenderFragment(w, http.StatusOK, "_chat_poll", msgsView{
		RoomID: id, Me: me, CSRF: csrf,
		Cards: makeCards(added, me, id, csrf), After: after,
		HadAdded: len(added) > 0, Deleted: deleted,
	})
}

func htmxReq(r *http.Request) bool { return r.Header.Get("HX-Request") == "true" }

// notify delivers a doorbell — a Juice step addressed to each other member — after a
// message is posted. The doorbell is THE unread signal and the ONLY state: a member's
// chat unread is exactly their outstanding chat doorbells, surfaced on the core step
// badge/inbox and, filtered, on the chat app-badge; an agent bridge is notified by the
// same step. Coalescing reads that truth directly: a member who already has a waiting
// chat doorbell for this room is covered, so we mint only for the rest — no shadow
// table to desync (completing a doorbell removes it from `waiting`, so the next message
// re-rings, browser or headless alike). Owned and funded by chat off its outbox seed
// trace with the price-0 chat/ack onward, so notifying costs nothing on any platform.
// Asynchronous (the post has already committed).
func (c *Chat) notify(ev daemon.NotifyEvent) {
	if c.seedTrace == "" || c.ackID == "" {
		return // seed not established yet; the next post retries
	}
	go func() {
		ctx := context.Background()
		covered, err := c.doorbellHolders(ctx, ev.RoomID)
		if err != nil {
			return // can't read the unread truth → skip; the next post retries
		}
		partial := mustJSON(map[string]any{"room": ev.RoomID, "room_name": ev.RoomName})
		for _, member := range ev.Members {
			if member == ev.Sender || covered[member] {
				continue // sender, or already has an outstanding doorbell for this room
			}
			_ = c.sa.Do(ctx, func(tok string) error {
				_, e := c.deps.Client.CreateStep(ctx, tok, client.CreateStepInput{
					TraceID:        c.seedTrace,
					Action:         c.ackID,
					RequiredCaller: member,
					PartialArgs:    partial,
				})
				return e
			})
		}
	}()
}

// doorbellHolders returns the set of members who currently hold a waiting chat
// doorbell for roomID, read as chat straight from the kernel steps — the coalescing
// gate, with no local mirror. A doorbell is a waiting step owned by chat whose
// partial_args names the room; its required_caller is the notified member.
func (c *Chat) doorbellHolders(ctx context.Context, roomID string) (map[string]bool, error) {
	holders := map[string]bool{}
	err := c.sa.Do(ctx, func(tok string) error {
		steps, e := c.deps.Client.ListSteps(ctx, tok, "", "waiting")
		if e != nil {
			return e
		}
		for _, s := range steps {
			if s.OwnerHandle != chatHandle {
				continue
			}
			var pa struct {
				Room string `json:"room"`
			}
			_ = json.Unmarshal(s.PartialArgs, &pa)
			if pa.Room == roomID {
				holders[s.RequiredCallerHandle] = true
			}
		}
		return nil
	})
	return holders, err
}

// dismiss completes the member's outstanding chat doorbell for the room, if any, when
// they visit it — clearing chat unread everywhere (the completed step drops out of
// `waiting`, which the badge, inbox, and room list all read). It finds the doorbell the
// same way chatUnread does — the member's own waiting chat step for this room — and
// completes it under the member's OWN token (only the required_caller may complete a
// step). No bookkeeping: the step is the state.
func (c *Chat) dismiss(r *http.Request, me, roomID string) {
	if c.deps.Sessions == nil {
		return
	}
	_ = c.deps.Sessions.Do(r.Context(), func(tok string) error {
		steps, e := c.deps.Client.ListSteps(r.Context(), tok, "", "waiting")
		if e != nil {
			return e
		}
		for _, s := range steps {
			if s.OwnerHandle != chatHandle {
				continue
			}
			var pa struct {
				Room string `json:"room"`
			}
			_ = json.Unmarshal(s.PartialArgs, &pa)
			if pa.Room == roomID {
				_, _ = c.deps.Client.CompleteStep(r.Context(), tok, s.ID, nil)
			}
		}
		return nil
	})
}

func (c *Chat) postCreateRoom(w http.ResponseWriter, r *http.Request) {
	name := roomName(r.FormValue("name"))
	if name == "" {
		c.deps.FlashRedirect(w, r, "/chat", "error", "A room needs a name.")
		return
	}
	reply, err := c.run(r, "create-room", map[string]any{"name": name, "members": parseHandles(r.FormValue("members"))})
	if c.failed(w, r, err, "/chat") {
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
	me := c.deps.Handle(r)
	text := strings.TrimSpace(r.FormValue("text"))
	if text != "" {
		if _, err := c.run(r, "message", map[string]any{"room": id, "text": text}); c.deps.HandleErr(w, r, err) {
			return
		}
	}
	// htmx: append in place (no reload → no jump). No-JS: reload the room.
	if htmxReq(r) {
		c.writePoll(w, r, me, id)
		return
	}
	http.Redirect(w, r, "/chat/room/"+url.PathEscape(id), http.StatusSeeOther)
}

func (c *Chat) postDeleteRoom(w http.ResponseWriter, r *http.Request) {
	c.opRedirect(w, r, "delete-room", map[string]any{"room": r.PathValue("id")}, "/chat")
}

func (c *Chat) postLeave(w http.ResponseWriter, r *http.Request) {
	c.opRedirect(w, r, "leave-room", map[string]any{"room": r.PathValue("id")}, "/chat")
}

func (c *Chat) postAddMember(w http.ResponseWriter, r *http.Request) {
	c.opRedirect(w, r, "add-member", map[string]any{"room": r.PathValue("id"), "member": strings.TrimSpace(r.FormValue("member"))}, roomURL(r))
}

func (c *Chat) postRemoveMember(w http.ResponseWriter, r *http.Request) {
	c.opRedirect(w, r, "remove-member", map[string]any{"room": r.PathValue("id"), "member": strings.TrimSpace(r.FormValue("member"))}, roomURL(r))
}

func (c *Chat) postDeleteMessage(w http.ResponseWriter, r *http.Request) {
	id, mid := r.PathValue("id"), r.PathValue("mid")
	if _, err := c.run(r, "delete-message", map[string]any{"room": id, "message": mid}); c.deps.HandleErr(w, r, err) {
		return
	}
	// htmx: a 200 lets the card's hx-swap="delete" remove it in place. No-JS: reload.
	if htmxReq(r) {
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, roomURL(r), http.StatusSeeOther)
}

// ---- helpers ----

// run executes a chat/msg member action as the signed-in user (the member write
// surface lives in the msg/ folder). The kernel dials the daemon synchronously and
// injects the caller's connected key, so the daemon has already applied (or rejected)
// the write by the time this returns — no polling.
func (c *Chat) run(r *http.Request, action string, args map[string]any) (client.CallReply, error) {
	var reply client.CallReply
	err := c.deps.Sessions.Do(r.Context(), func(tok string) error {
		var e error
		reply, e = c.deps.Client.Run(r.Context(), tok, chatHandle+"/msg/"+action, mustJSON(args), "")
		return e
	})
	return reply, err
}

// opRedirect runs an action as the signed-in user and, on success, redirects to
// `to` (the room URL for in-room ops, "/chat" for room removal/leaving).
func (c *Chat) opRedirect(w http.ResponseWriter, r *http.Request, action string, args map[string]any, to string) {
	if _, err := c.run(r, action, args); c.failed(w, r, err, to) {
		return
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// failed reports a run failure and returns true when it handled one. A business
// rejection is the user's own input coming back, so it belongs beside the form
// as a flash; only a genuine fault falls through to the error page, since
// answering a typo with "Something went wrong" says nothing actionable.
func (c *Chat) failed(w http.ResponseWriter, r *http.Request, err error, to string) bool {
	if err == nil {
		return false
	}
	if reason := daemonReason(err); reason != "" {
		c.deps.FlashRedirect(w, r, to, "error", reason)
		return true
	}
	return c.deps.HandleErr(w, r, err)
}

// daemonReason is the daemon's own rejection sentence, or "" for anything else.
// The daemon answers a rejection with a non-2xx {"ok":false,"error":"…"} body,
// which the kernel reports as an execution failure carrying that body.
func daemonReason(err error) string {
	ke, ok := client.AsKernelError(err)
	if !ok || ke.Kind != client.KindExecutionFailed {
		return ""
	}
	var body struct{ Error string }
	if i := strings.Index(ke.KernelMessage, "{"); i < 0 || json.Unmarshal([]byte(ke.KernelMessage[i:]), &body) != nil {
		return ""
	}
	return strings.TrimPrefix(body.Error, "bad input: ")
}

// roomURL is the browser path for the current room.
func roomURL(r *http.Request) string { return "/chat/room/" + url.PathEscape(r.PathValue("id")) }

// roomName sanitizes a display room name (trimmed; empty rejected).
func roomName(s string) string { return strings.TrimSpace(s) }

// parseHandles splits a free-form member list into handles. Handles are bare
// (requirements.md §4) and are never rewritten here: a sigiled one is passed
// through so the daemon rejects it, exactly as it would a headless action call.
func parseHandles(s string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == ',' || r == '\n' || r == '\t' || r == '\r' }) {
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

// newSecret returns a strong random hex secret for the chat account password
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

// writeErrJSON renders a plugin-handler failure (serveRegister) at the given status so
// the kernel surfaces it as a run failure.
func writeErrJSON(w http.ResponseWriter, status int, msg string) {
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `{"ok":false,"error":%q}`, msg)
}

// errText is err.Error() or "" for a nil error — for structured logging.
func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
