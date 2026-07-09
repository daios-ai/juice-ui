# App Modules

Juice-ui has a small core BFF (the CLI-mirroring surface: `run`, `action`,
`process`, `tx`, `step`, `user`, `admin`, `peers`) plus **apps** that don't belong
in that surface — the first is **chat** (`apps/chat`). An app is a self-contained
unit that plugs into the core through one seam, so the core never grows an app's
weight and an app never reaches into the core.

Two properties, on two different axes:

- **Independent in code.** An app is its own package with its own templates,
  routes, tests, and any small store. It depends only on the kernel `client`, the
  `session` manager, and the `apps` seam — **never on package `server`**. Removing
  an app is deleting its package and its one wiring line.
- **Bundled in lifecycle.** An app is *not* a separate deployable. It ships in the
  juice-ui binary, is started by the seam's `Start()`/stopped by `Stop()`, and dies
  when the UI is torn down. An app that is itself a Juice user (like chat) runs its
  service **in-process** — no second process, no separate binary.

> The **visual grammar stays central**, deliberately. An app owns its Go,
> templates, and routes — but its components register in the one place
> (`docs/ui-grammar.md` + the drift guard in `server/grammar_test.go`, which scans
> app templates too). Code is modular; the grammar is unified. That centralization
> is what keeps every app visually coherent.

## The seam (`apps/apps.go`)

```go
type App interface {
    Slug() string
    Nav(handle string, superuser bool) []NavEntry // self-gated sidebar entries
    Mount(mux *http.ServeMux, d Deps)             // register routes
    Templates() fs.FS                             // embedded templates/*.html
    Start(ctx context.Context) error              // provision/open resources
    Stop(ctx context.Context) error               // release resources
}
```

`Deps` is the core capability surface handed to an app at `Mount` time — the kernel
`client`, the `session` manager, and helpers as closures over the core server:
`RenderPage`, `RenderFragment`, `HandleErr`, `HandleErrFragment`, `FlashRedirect`,
`CSRFField`, `IsHTMX`, `Handle`, `RequireAuth`. An app renders full pages in the
shared shell and htmx fragments without importing `server`. It also carries:

- **`LoadAppState(slug)` / `SaveAppState(slug, v)`** — a namespaced config store
  over `juice-ui.json`. An app provisions and remembers its own state (e.g. a
  service-account password) here, so onboarding needs **no per-app environment
  variables** — a new app adds a JSON key, never a new env var.
- **`PublicURL`** — the BFF's absolute base URL. An app that registers `kind=http`
  actions builds their callback source from it under the CSRF-exempt `/_kernel/`
  prefix (e.g. `PublicURL + "/_kernel/<slug>/ack"`).
- **`Logger`** — the core's structured logger, for an app's background work.

## Being a Juice user — `apps.ServiceAccount`

An app that *is* a Juice user (like chat's `@chat`) must not hand-roll the fragile
account lifecycle. The seam owns it in one place: `apps.ServiceAccount`.

```go
sa := apps.NewServiceAccount(deps, "@myapp", password) // app persists the password
sa.Start(ctx, app.registerActions)                     // registerActions uses sa.Do
// ... sa.Do(ctx, func(tok string) error { ... })      // any call as the account
// app.Stop(): sa.Stop()
```

It provisions the account (create-if-absent, **once**), authenticates (PKCE +
single refresh on 401), and runs a resilient background loop with **exponential
backoff** so a persistent failure can never saturate the kernel's per-IP auth rate
limit (a tight retry 429-storms it and wedges *every* login, operator included). It
logs the cause with an operator fix (e.g. a suspended account → "run `admin
unsuspend`"), and because it keeps retrying at the cap it **self-heals** the moment
the account is restored — no restart. The app supplies only its password (persisted
via `SaveAppState`) and its idempotent domain bootstrap (the `setup` callback).

## Identifying callers — `apps.CallerIdentity`

The **inbound** mirror of `ServiceAccount`. A plugin whose service uses
`delegated_bearer` actions must identify the caller the kernel forwards. The seam
does this statelessly:

```go
id := apps.NewCallerIdentity(signingKey)  // app persists signingKey
// mint the token to connect as a user's grant:
client.ConnectGrant(ctx, userTok, "@myapp/do", id.Token(handle))
// register the action with delegated_bearer auth on apps.CallerKeyHeader;
// hand id.Authenticate to the service as its auth hook (it verifies the injected token).
```

`token(handle) = base64url(handle).base64url(HMAC-SHA256(signingKey, handle))` —
**deterministic** (same key+handle → same token, so it survives any store reset and
can never desync with the grant) and **unforgeable** (only the signing-key holder
mints a valid one). The service stores **no** per-user keys; `Verify`/`Authenticate`
recover the handle by recomputing the MAC. The signing key persists via
`SaveAppState`, so identity outlives the service's own store.

Together `ServiceAccount` (outbound: authenticate *as* a Juice user) and
`CallerIdentity` (inbound: identify calling users) are the seam's full "a plugin is
a Juice service" identity toolkit — and both keep the plugin's *service* (e.g.
chat's `daemon`) Juice-agnostic: auth is injected, so the service could run
standalone.

### Reconciling write-only auth config

The kernel never returns an action's `auth` (write-only, R9): `ListActions` reports
the URL, schemas, description, and — usefully — `auth_scheme` + `requires_grant`,
but **not** the auth *config* (the header name and token template). A plugin that
reconciles its actions by reading the kernel therefore cannot detect a change to
that config: rename the header a `delegated_bearer` action carries (e.g.
`X-Chat-Key` → `apps.CallerKeyHeader`) and every readable field still matches, so a
read-based diff sees no drift while the kernel keeps injecting the caller's token
into the *old* header — the service reads nothing and returns 401.

The fix is to **version the auth config locally** and force a re-push on change:

- Persist a short revision of the pushed auth config in app state (chat stores
  `AuthRev`, a hash of the auth JSON, in `juice-ui.json` via `SaveAppState`).
- On reconcile, compute `force := storedRev != currentRev`. When `force`, `Update`
  every action with the **full** contract *including `auth`* (not only the readable
  fields), then persist the new revision. Steady-state restarts (matching revision)
  do nothing.
- **An auth change revokes the action's grants** (kernel §8 — verified: renaming the
  header alone empties every caller's grant list). So after a forced re-push the
  plugin must re-run its per-user grant sync — chat clears its once-per-run
  `provisioned` guard so each user re-connects `id.Token(handle)` on their next
  visit. Without this the actions are correct but every user is unauthenticated.

Also fold the readable signals into the ordinary drift check: treat
`auth_scheme != "delegated_bearer"` or `requires_grant == false` as drift, so an
action that lost its auth entirely is repaired without waiting for a revision bump.

(Re-pushing the *unchanged* action `source` alongside the new auth is safe even when
that source is a loopback URL: the kernel's SSRF guard validates a source only when
its value changes, so an identical re-push is accepted.)

`NavEntry` is one sidebar link (`Slug`, `Label`, `Href`, `Icon` — the app's own
inline `<svg>` markup, so it needs no edit to the core sprite, `Match` — the path
prefix that lights it). An app nav entry is a deliberate exception to the
CLI-mirroring nav, exactly as `peers` is.

The seam also reserves a **CSRF-exempt `/_kernel/` path prefix** for an app's
kernel-facing endpoints — a `kind=http` action `source` the kernel calls
server-to-server, without a browser CSRF token. Every browser route stays
CSRF-protected.

### Dependency direction (no cycles)

```
apps               ← the seam (leaf; imports client, session)
  ↑        ↑
server   apps/chat        (the UI plugin; consumes the seam)
  ↑        ↑
cmd/juice-ui               (wires a []App into server.New)

apps/chat/daemon   ← the @chat application: imports only client, session (not the
                     seam or server); run in-process by the plugin, testable alone.
```

`server` imports `apps` (for the interface, `Deps`, and the app config store). The
UI plugin imports `apps`, `client`, `session`, and its own `daemon` subpackage.
`cmd/juice-ui` wires everything. `server` never imports a concrete app.

## How the core mounts an app

1. **Templates** — `server.NewRenderer(app.Templates()...)` merges each app's
   partials (`templates/_*.html`) into the shared base set and clones its pages from
   that base, so an app page uses the same layout, partials, and helpers as a core
   page.
2. **Routes + lifecycle** — for each app `server.New(Options{Apps: …})` calls
   `Mount(mux, deps)` (the app stashes `Deps`), then `Start(ctx)`, before the
   middleware chain; `Stop` runs on shutdown.
3. **Nav** — the page envelope calls `Nav(handle, superuser)` and marks the entry
   whose `Match` prefixes the request path as current. `layout.html` ranges over
   `.AppNav` in both the sidebar and the mobile tab bar.

## Adding a new app — checklist

1. Create `apps/<name>/` with `<name>.go` implementing `apps.App`, an embedded
   `templates/` dir, and `*_test.go` for every `.go` file.
2. Templates: page files define `{{define "content"}}`; shared bits are `_*.html`
   partials. **Compose the existing grammar utilities** (`.stack`, `.row`, `.card`,
   `.mono`, `.owner`, `_ref`, …) — never invent chrome, and do **not** edit core
   `app.css`/`icons.svg`. Provide the nav icon inline on `NavEntry`. The drift guard
   scans your templates.
3. **Never touch a core file for app-specific content.** The only acceptable core
   edits are to the seam itself (`apps/`, the mount/render wiring in `server`, the
   wiring line in `cmd`). Everything else lives in `apps/<name>/`.
4. Wire it in `cmd/juice-ui/main.go`: construct it, pass `Templates()` to
   `server.NewRenderer`, add it to the `Apps` slice.
5. State: keep the core BFF stateless. An app that needs a kernel identity, its own
   actions, and durable state should be a **Juice user run in-process** (its own
   `daemon` subpackage) that authenticates as a service account, registers free
   (price-0) actions, and projects its own ledger — as `apps/chat/daemon` does. It
   **self-provisions** its account on first run and persists the password via
   `SaveAppState`, so there are no per-app env vars. The UI plugin stays thin: it
   reads from the in-process app and issues writes as the signed-in user.
6. Run `go test ./...` (unit + the grammar drift guard) and **drive the real app
   end-to-end** against a throwaway kernel — a single juice-ui launch, with
   realistic pricing (native actions can cost credits).

## Reference implementation: chat

Chat is the worked example — a real **application on top of Juice** — in two parts,
both under `apps/chat/`, with **zero chat-specific code in juice-ui core**.

### 1. The @chat daemon (`apps/chat/daemon`)

A self-contained, **Juice-agnostic REST chat server** — rooms, membership, an
append-only message log, per-user unread — over a durable SQLite store. It knows
nothing of a kernel, tokens, or grants: **caller authentication is injected** (an
auth hook handed to `New`), and the admin read routes are gated by an **admin
secret**, so the same service could run standalone behind any auth layer.
`store.go` is the (history-only) store; `daemon.go` the REST handlers; `client.go`
the typed REST `Client` the plugin depends on (wired in-process by an in-memory
transport, so the REST contract is exercised without a socket).

- **Synchronous identity + authorization.** Each state-changing op is a fixed
  write route (`/create-room`, `/message`, …). The caller is whatever the injected
  auth hook returns (in juice-ui, `apps.CallerIdentity.Authenticate` verifying the
  token the kernel injects for that user's `delegated_bearer` grant), so the daemon
  authorizes *at the door* — creator-only admin ops, member-only posting — and
  returns a real success or a typed failure, never a hollow ack.
- **Durable, indexed, incremental.** The store is the source of truth (the kernel
  ledger is demoted to an audit trail). Reads are bounded range scans over a
  monotonic per-room sequence — never a full-log read — and unread is a count past
  each user's read watermark.

### 2. The UI-integration plugin (`apps/chat`, the `apps.App`)

The only Juice- and UI-aware piece; it crosses both boundaries and touches neither
side's internals.

- **Self-provisions.** On `Start` it loads (or generates + persists) the `@chat`
  account password *and* the daemon admin secret via `LoadAppState`/`SaveAppState`,
  and opens the store beside `juice-ui.json` (in-memory when there is no persistent
  config). No chat environment variables — one launch and chat works.
- **Owns the `@chat` account via `apps.ServiceAccount`.** It hands the seam the
  handle, the persisted password, and its action-registration as the `setup`
  callback; the seam does provisioning, auth, backoff, self-heal, and diagnostics
  (see above). None of that lifecycle code lives in the plugin.
- **Registers the surface** (its `setup`). As `@chat`, it registers each `@chat/*`
  action with `delegated_bearer` auth, pointing the `source` at the daemon's
  matching write route, and publishes it — reconciling a stale action left by an
  earlier build. It mounts the daemon's write routes under the CSRF-exempt
  `/_kernel/chat/` prefix.
- **Provisions each user via `apps.CallerIdentity`.** On first chat touch (once per
  run) it connects the user's **deterministic** caller token — `id.Token(handle)` —
  as a grant on every `@chat` action, *as the user*, silently. Because the token is
  a stateless function of the signing key + handle, this can never desync with the
  grant and survives a store loss; it also re-points any stale grant.
- **Reads** rooms/messages via the daemon's REST `Client`; **writes** by running
  `@chat/*` as the signed-in user. The kernel dials the daemon synchronously and
  injects the caller's token, so the write is applied (or rejected) by the time
  `run` returns — no polling.

This is the model for the next app: a durable REST service behind a `Client`, plus
a thin Juice-aware plugin that self-provisions and connects per-user
`delegated_bearer` grants for synchronous identity at the door.
