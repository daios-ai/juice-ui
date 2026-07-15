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
// provisioned on first run and persisted in state.json via the app-state store
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

// outboxName is @chat's private, price-0, owner-only action that opens the outbox
// seed. @chat runs it (paying itself 0), and its dispatch handler creates the
// sentinel step via the trace capability — no @sys/message toll, so @chat needs no
// credits on any kernel. Its handler is the plugin (not a daemon Op) because the
// capability callback is a Juice call; the daemon stays Juice-agnostic.
const outboxName = "outbox"

// chatDBName is the daemon store filename, kept in the component home ($JUICE_HOME/ui).
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

	seedTrace string // @chat's open outbox trace, minted doorbells hang off it (from st.SeedTrace)
	sinkID    string // @sys/sink action id, the doorbell steps' onward (resolved at seed setup)

	provisioned sync.Map // handle -> struct{}: grants synced this run
}

// New constructs the chat plugin. It needs no configuration — it provisions the
// @chat account and daemon secret itself on Start and reaches the kernel through
// Deps.
func New() *Chat { return &Chat{} }

func (c *Chat) Slug() string { return "chat" }

// Nav contributes the "chat" sidebar entry. Its badge is the count of the user's
// outstanding chat doorbell steps — the core step badge (all steps) filtered to
// @chat, one source of truth. The count is filled by the /chat/badge poll (Nav has
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
// of that prefix). The private @chat/outbox action is handled by the plugin (it makes
// a Juice capability callback); everything else is a daemon write route. Keeping
// /outbox under this same prefix inherits its CSRF exemption.
func (c *Chat) kernelDispatch(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/"+outboxName {
		c.serveOutbox(w, r)
		return
	}
	if c.daemon == nil {
		http.Error(w, "chat starting", http.StatusServiceUnavailable)
		return
	}
	c.daemon.KernelHandler().ServeHTTP(w, r)
}

// serveOutbox handles the kernel's dispatch of @chat/outbox: it reads the injected
// trace-capability headers and calls back to create the never-completed sentinel step
// that holds the outbox seed process open (the mint-able trace notify() hangs
// doorbells off). @chat pays itself 0 (the action is price 0). Returns the sentinel
// id as {step_id} so ensureSeed can persist it. A leaf response (no capability) is a
// harmless no-op.
func (c *Chat) serveOutbox(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	cap, cb := r.Header.Get(client.CapabilityHeader), r.Header.Get(client.CallbackHeader)
	stepID := ""
	if cap != "" && cb != "" && c.sinkID != "" {
		st, err := c.deps.Client.CallbackCreateStep(r.Context(), cb, cap, client.CreateStepInput{
			ActionID:       c.sinkID,
			RequiredCaller: chatHandle,
		})
		if err != nil {
			slog.Warn("chat outbox: capability callback failed", "callback", cb, "sink", c.sinkID, "err", err.Error())
		} else {
			stepID = st.ID
		}
	} else {
		slog.Warn("chat outbox: dispatch missing inputs", "cap_present", cap != "", "callback", cb, "sink_present", c.sinkID != "")
	}
	_, _ = fmt.Fprintf(w, `{"step_id":%q}`, stepID)
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
	// SeedTrace / SeedStep identify @chat's open "outbox" process — opened by running
	// the private @chat/outbox action, whose never-completed sentinel step (SeedStep)
	// holds the process open and the trace (SeedTrace) mint-able. Doorbell steps are
	// minted off it. Both survive kernel restarts; re-established only when gone.
	SeedTrace string `json:"seed_trace"`
	SeedStep  string `json:"seed_step"`
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
	c.daemon = daemon.New(store, st.AdminSecret, c.id.Authenticate, c.notify)
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
// override; then "" (in-memory) when there is no persistent app state, so tests
// write no file; then the component home only ($JUICE_HOME/ui/chat.db). No
// legacy-path fallback — see the note in cmd/juice-ui config.go: silent legacy
// resolution is what let @chat credentials drift.
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
		wanted[outboxName] = true // private seed action (reconciled separately, not a daemon Op) — keep it off the orphan-retirement list
		base := strings.TrimRight(c.deps.PublicURL, "/") + "/_kernel/chat/"
		auth := authConfig()
		pubTrue := true
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
						Public:       &pubTrue,
					}); err != nil {
						return fmt.Errorf("chat: reconcile %s/%s: %w", chatHandle, op.Name, err)
					}
					if err := c.deps.Client.EnableAction(ctx, tok, a.ID); err != nil {
						return fmt.Errorf("chat: enable %s/%s: %w", chatHandle, op.Name, err)
					}
					return nil
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
				return nil
			}())
		}
		// Retire orphans: an active @chat action no longer in Ops (e.g. a removed op
		// like rename-room) is disabled — reversible, preserves history — so the
		// catalog never advertises an action whose daemon route is gone.
		for name, a := range have {
			if wanted[name] || !a.Active {
				continue
			}
			if err := c.deps.Client.DisableAction(ctx, tok, a.ID); err != nil {
				errs = append(errs, fmt.Errorf("chat: retire %s/%s: %w", chatHandle, name, err))
			}
		}
		return errors.Join(errs...)
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
	// Ensure @chat holds an open outbox trace to mint doorbell steps off. Done after
	// action reconciliation and within the same backoff-retried setup, so a briefly
	// unreachable kernel just retries.
	return c.sa.Do(ctx, func(tok string) error { return c.ensureSeed(ctx, tok) })
}

// outbox action schemas — activation requires described properties; it takes none.
var (
	outboxInSchema  = json.RawMessage(`{"type":"object","properties":{}}`)
	outboxOutSchema = json.RawMessage(`{"type":"object","properties":{"step_id":{"type":"string","description":"Id of the sentinel step that holds the outbox process open."}}}`)
)

const outboxDesc = "Opens the chat outbox seed by creating a sentinel step via the trace capability; called only by @chat."

// ensureSeed makes sure @chat holds an open "outbox" trace to mint doorbell steps off
// (see chatState.SeedTrace). It resolves @sys/sink's id (the sentinel's and every
// doorbell's onward), makes the private @chat/outbox action exist, then reuses a
// persisted seed whose sentinel is still waiting — or opens a fresh one by running
// @chat/outbox (price 0, owner-called, so @chat pays itself 0). The outbox handler
// (serveOutbox) creates the never-completed sentinel via the trace capability, holding
// the process open and the trace mint-able. No @sys/message toll ⇒ @chat needs no
// credits on any kernel. Idempotent and restart-safe (the seed survives kernel restarts).
func (c *Chat) ensureSeed(ctx context.Context, tok string) error {
	if c.sinkID == "" {
		id, err := c.resolveSink(ctx, tok)
		if err != nil {
			return fmt.Errorf("chat: resolve @sys/sink: %w", err)
		}
		c.sinkID = id
	}
	if err := c.ensureOutboxAction(ctx, tok); err != nil {
		return err
	}
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
	reply, err := c.deps.Client.Run(ctx, tok, chatHandle+"/"+outboxName, mustJSON(map[string]any{}))
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

// resolveSink returns @sys/sink's action id. GET /v1/actions/{ref} does not resolve an
// @owner/name ref (404 — see kernel-contract-deviations), so it is found by name in
// @sys's public catalog.
func (c *Chat) resolveSink(ctx context.Context, tok string) (string, error) {
	acts, err := c.deps.Client.ListActions(ctx, tok, "@sys", "", false)
	if err != nil {
		return "", err
	}
	for _, a := range acts {
		if a.Action == "@sys/sink" || a.Name == "sink" {
			return a.ID, nil
		}
	}
	return "", fmt.Errorf("@sys/sink not in catalog")
}

// ensureOutboxAction makes @chat's private, price-0, no-auth outbox action exist,
// active, and pointed at the plugin's /_kernel/chat/outbox handler. It is reconciled
// here — not through the public-op loop or contractDrifted (which assume public
// delegated_bearer) — because it must stay PRIVATE (only @chat calls it) with NO auth
// (owner calls own action, so no grant). It is never published.
func (c *Chat) ensureOutboxAction(ctx context.Context, tok string) error {
	want := strings.TrimRight(c.deps.PublicURL, "/") + "/_kernel/chat/" + outboxName
	existing, err := c.deps.Client.ListActions(ctx, tok, chatHandle, "", true)
	if err != nil {
		return err
	}
	var a *client.Action
	for i := range existing {
		if existing[i].Name == outboxName {
			a = &existing[i]
			break
		}
	}
	if a == nil {
		na, err := c.deps.Client.CreateAction(ctx, tok, client.CreateActionInput{
			Name: outboxName, Kind: "http", Source: want, Method: "POST",
			Description: outboxDesc, Price: 0,
			InputSchema: outboxInSchema, OutputSchema: outboxOutSchema, // no Auth: owner-called
		})
		if err != nil {
			return fmt.Errorf("chat: create %s/%s: %w", chatHandle, outboxName, err)
		}
		return c.deps.Client.EnableAction(ctx, tok, na.ID) // enable, never publish → stays private
	}
	priv := false
	if a.HTTP == nil || a.HTTP.URL != want {
		desc := outboxDesc
		if _, err := c.deps.Client.UpdateAction(ctx, tok, a.ID, client.UpdateActionInput{
			Source: &want, Method: "POST", InputSchema: outboxInSchema, OutputSchema: outboxOutSchema,
			Description: &desc, Public: &priv,
		}); err != nil {
			return fmt.Errorf("chat: reconcile %s/%s: %w", chatHandle, outboxName, err)
		}
		return c.deps.Client.EnableAction(ctx, tok, a.ID)
	}
	if !a.Active {
		return c.deps.Client.EnableAction(ctx, tok, a.ID)
	}
	if a.Public {
		if _, err := c.deps.Client.UpdateAction(ctx, tok, a.ID, client.UpdateActionInput{Public: &priv}); err != nil {
			return fmt.Errorf("chat: unpublish %s/%s: %w", chatHandle, outboxName, err)
		}
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

// ensureProvisioned connects the signed-in user's deterministic caller token to
// the @chat actions — once per handle per juice-ui run (an in-memory guard). A
// single selector connect (@chat) mints one grant per callable @chat action against
// one Connection (v0.7 §8); the @chat actions share one source host, so they form a
// single bearer group and provider is left empty. Because the token is a stateless
// function of (signing key, handle), this re-points any stale or revoked grant at
// the current token and can never desync. It runs on the chat entry pages, before
// any write is possible. The selector expands only to actions the user may call, so
// an op still being (re)published is simply picked up on a later run.
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
		_, e := c.deps.Client.ConnectGrant(r.Context(), tok, chatHandle, "", token)
		return e
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
	// Unread is the doorbell steps, not a watermark: bold the rooms the user has an
	// outstanding @chat doorbell for, and float them to the top (RoomsFor is
	// recency-ordered; a stable sort keeps that within each group).
	unread, _ := c.chatUnread(r)
	for i := range rooms {
		if unread[rooms[i].ID] {
			rooms[i].Unread = 1
		}
	}
	sort.SliceStable(rooms, func(i, j int) bool { return rooms[i].Unread > rooms[j].Unread })
	c.deps.RenderPage(w, r, http.StatusOK, "chat", "chat", indexView{
		Rooms:    rooms,
		OpenForm: r.URL.Query().Get("form") == "room-create",
	})
}

// getBadge serves the chat nav-badge fragment the core polls: the count of the
// user's outstanding chat doorbells (bare number, empty when zero — the core
// steps-badge contract). It is the core step badge filtered to @chat — one source
// of truth (the doorbell steps), never a separate watermark.
func (c *Chat) getBadge(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if _, n := c.chatUnread(r); n > 0 {
		fmt.Fprintf(w, "%d", n)
	}
}

// chatUnread reads the signed-in user's outstanding chat doorbells straight from
// the kernel steps — the single source of truth for chat unread. A doorbell is a
// waiting step owned by @chat addressed to the user; its partial_args names the
// room. Returns the set of unread rooms and the count. Both the app-badge and the
// room-list unread are just this: the bell (all waiting steps) filtered to @chat.
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
	c.ensureProvisioned(r)
	id := r.PathValue("id")
	room, msgs, err := c.dc.Room(r.Context(), me, id)
	if err != nil {
		c.deps.HandleErr(w, r, &client.KernelError{Kind: client.KindNotFound})
		return
	}
	c.dismiss(r, me, id)
	c.deps.RenderPage(w, r, http.StatusOK, "chat_conversation", "chat", roomView{
		Room: room, Me: me, IsOwner: room.Creator == me,
		OpenForm: r.URL.Query().Get("form"),
		Msgs:     msgsView{RoomID: room.ID, Me: me, Messages: msgs, CSRF: c.deps.CSRFField(r)},
	})
}

// getMessages renders just the message list for the conversation page's htmx poll.
// It completes the room's doorbell (clearing chat unread) ONLY when the tab is
// visible: the poll keeps running in a backgrounded tab (so content stays fresh),
// but must not silently clear unread while the user isn't looking. app.js sends
// X-Visible from document.visibilityState; an absent header (stale asset, other
// client) is treated as visible, degrading to the prior always-mark behaviour rather
// than to "unread stuck forever". A non-member gets an empty list (no leak).
func (c *Chat) getMessages(w http.ResponseWriter, r *http.Request) {
	me := c.deps.Handle(r)
	id := r.PathValue("id")
	_, msgs, _ := c.dc.Room(r.Context(), me, id)
	if r.Header.Get("X-Visible") != "0" {
		c.dismiss(r, me, id)
	}
	_ = c.deps.RenderFragment(w, http.StatusOK, "_chat_messages", msgsView{
		RoomID: id, Me: me, Messages: msgs, CSRF: c.deps.CSRFField(r),
	})
}

// notify delivers a doorbell — a Juice step addressed to each other member — after
// a message is posted. The doorbell is THE unread signal (single source of truth):
// a member's chat unread is exactly their outstanding @chat doorbells, surfaced on
// the core step badge/inbox and, filtered, on the chat app-badge; an agent bridge is
// notified by the same step. Coalesced: at most one waiting doorbell per
// (member, room); a message arriving while one is outstanding mints nothing. Owned
// and funded by @chat off its outbox seed trace, so posters' wallets stay clean.
// Asynchronous (the post has already committed); a mint failure clears the slot so
// the next message retries.
func (c *Chat) notify(ev daemon.NotifyEvent) {
	if c.seedTrace == "" || c.sinkID == "" {
		return // seed not established yet; the next post retries
	}
	go func() {
		ctx := context.Background()
		partial := mustJSON(map[string]any{"room": ev.RoomID, "room_name": ev.RoomName})
		for _, member := range ev.Members {
			if member == ev.Sender {
				continue
			}
			reserved, err := c.dc.ReserveNotification(ctx, member, ev.RoomID)
			if err != nil || !reserved {
				continue // already ringing, or reserve failed
			}
			var stepID string
			err = c.sa.Do(ctx, func(tok string) error {
				st, e := c.deps.Client.CreateStep(ctx, tok, client.CreateStepInput{
					TraceID:        c.seedTrace,
					ActionID:       c.sinkID,
					RequiredCaller: member,
					PartialArgs:    partial,
				})
				if e == nil {
					stepID = st.ID
				}
				return e
			})
			if err != nil || stepID == "" {
				_ = c.dc.ClearNotification(ctx, member, ev.RoomID) // let the next message retry
				continue
			}
			_ = c.dc.SetNotificationRef(ctx, member, ev.RoomID, stepID)
		}
	}()
}

// dismiss completes the outstanding doorbell step for (me, room), if any, when the
// member visits the room — clearing chat unread everywhere (it IS the unread state:
// the core step badge/inbox and the chat app-badge both read these steps).
// Completion runs under the member's OWN session token (only the required_caller may
// complete a step). The local ref is cleared unless completion failed only
// transiently, so the next unread re-rings.
func (c *Chat) dismiss(r *http.Request, me, roomID string) {
	ref, err := c.dc.GetNotification(r.Context(), me, roomID)
	if err != nil || ref == "" {
		return
	}
	cerr := c.deps.Sessions.Do(r.Context(), func(tok string) error {
		_, e := c.deps.Client.CompleteStep(r.Context(), tok, ref, nil)
		return e
	})
	if completionSettled(cerr) {
		_ = c.dc.ClearNotification(r.Context(), me, roomID)
	}
}

// completionSettled reports whether a doorbell completion left the step resolved —
// success, or any non-transient kernel error (already done, gone, closed) that a
// retry would not fix. Only a transport-transient failure (kernel down/timeout)
// keeps the local ref so a later visit retries.
func completionSettled(err error) bool {
	if err == nil {
		return true
	}
	ke, ok := client.AsKernelError(err)
	if !ok {
		return false
	}
	return ke.Kind != client.KindUnavailable && ke.Kind != client.KindTimeout
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
	c.opRedirect(w, r, "delete-message", map[string]any{"room": r.PathValue("id"), "message": r.PathValue("mid")}, roomURL(r))
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

// opRedirect runs an action as the signed-in user and, on success, redirects to
// `to` (the room URL for in-room ops, "/chat" for room removal/leaving).
func (c *Chat) opRedirect(w http.ResponseWriter, r *http.Request, action string, args map[string]any, to string) {
	if _, err := c.run(r, action, args); c.deps.HandleErr(w, r, err) {
		return
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// roomURL is the browser path for the current room.
func roomURL(r *http.Request) string { return "/chat/room/" + url.PathEscape(r.PathValue("id")) }

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
