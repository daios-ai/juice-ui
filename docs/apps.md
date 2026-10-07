# App Modules

Juice-ui has a small core BFF (the CLI-mirroring surface: `run`, `action`,
`process`, `tx`, `task`, `user`, `admin`, `peers`) plus **apps** that don't belong
in that surface — the first is **chat** (`apps/chat`). An app is a self-contained
unit that plugs into the core through one seam, so the core never grows an app's
weight and an app never reaches into the core.

> This document is the **seam mechanics**. For the *mental model and the sharp edges*
> — how to think about an app as a Juice user so it composes with the rest of the
> ecosystem — read `docs/building-juice-apps.md` first.

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

## Design rules & rationale

Five rules govern every app. They are what keep the codebase modular without the
core rotting into a monolith; read them before touching this seam.

1. **Dependency direction is law.** *daemon* (a self-contained service) ← *plugin*
   (the App: the only Juice + UI-aware piece) ← *core* (imports only the `apps`
   seam, never a concrete app). An app imports `client`, `session`, `apps` — never
   `server`. The arrows never reverse.
2. **Litmus test: delete the plugin and the daemon still runs.** A daemon (e.g.
   `apps/chat/daemon`) is a plain REST service that knows nothing about Juice — auth
   is *injected* into it as a hook, not imported. If a daemon needs the plugin to
   compile or run, the boundary has leaked.
3. **Ownership split.** The daemon owns *domain state* (its store, its REST
   contract, its list ordering). The plugin owns *Juice/UI integration* (kernel
   actions, grants, identity, templates, the nav badge). Neither reaches across:
   the plugin never writes the daemon's tables; the daemon never mints a grant.
4. **The caller is who the kernel says.** A `delegated_bearer` action carries the
   key the caller connected on `apps.CallerKeyHeader`; the app resolves that key to
   the caller and trusts nothing else in the request.
5. **Self-provisioning; new capabilities go in the seam.** An app provisions its own
   secrets into app state (no per-app environment variables). A capability more than
   one app could want — the app-state store, `ServiceAccount`, `CallerKeyHeader`,
   badge polling, `UserDataDir` — lands in `apps/apps.go`, never hardwired into one
   app. The badge poll is the worked example: chat asked for a live nav count, and
   the answer was a seam field (`NavEntry.BadgeURL`) every future app inherits.

## The seam (`apps/apps.go`)

```go
type App interface {
    Slug() string
    Nav(me string, superuser bool) []NavEntry     // self-gated sidebar entries; me is the signed-in address
    Mount(mux *http.ServeMux, d Deps)             // register routes
    Templates() fs.FS                             // embedded templates/*.html
    Start(ctx context.Context) error              // provision/open resources
    Stop(ctx context.Context) error               // release resources
}
```

`Deps` is the core capability surface handed to an app at `Mount` time — the kernel
`client`, the `session` manager, and helpers as closures over the core server:
`RenderPage`, `RenderFragment`, `HandleErr`, `HandleErrFragment`, `FlashRedirect`,
`CSRFField`, `IsHTMX`, `Me` (the signed-in address), `Address`, `RequireAuth`. An app renders full pages in the
shared shell and htmx fragments without importing `server`. It also carries:

- **`LoadAppState(slug)` / `SaveAppState(slug, v)`** — a namespaced config store
  over `state.json`. An app provisions and remembers its own state (e.g. a
  service-account password) here, so onboarding needs **no per-app environment
  variables** — a new app adds a JSON key, never a new env var.
- **`PublicURL`** — the BFF's absolute base URL. An app that registers `kind=http`
  actions builds their callback source from it under the CSRF-exempt `/_kernel/`
  prefix (e.g. `PublicURL + "/_kernel/<slug>/ack"`).
- **`Logger`** — the core's structured logger, for an app's background work.

**Every name is an address** (requirements.md §4): a user is `handle@kernel`,
an action `owner@kernel/name`, on this kernel too, and the kernel refuses a bare
name. An app passes the names it is served through verbatim, and composes one
only for its own principal on this kernel, through `Deps.Address(handle)` — the
core's one composition site, `""` while the kernel's name is unknown (the kernel
refuses that, so the app's retry waits for it).

## Being a Juice user — `apps.ServiceAccount`

An app that *is* a Juice user (like chat's `chat@<kernel>`) must not hand-roll the fragile
account lifecycle. The seam owns it in one place: `apps.ServiceAccount`.

```go
sa := apps.NewServiceAccount(deps, "myapp", password) // signs in as myapp@<kernel>; the app persists the password
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
  `AuthRev`, a hash of the auth JSON, in `state.json` via `SaveAppState`).
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

**Live nav badge.** `NavEntry.Badge` is the initial count (server-rendered, so a
no-JS client still sees it). Set `NavEntry.BadgeURL` too — an authenticated `GET`
fragment endpoint (convention `/<slug>/badge`) — and the core polls it on the one
configured interval (`badge_interval_seconds`, default 30s), keeping the badge live
without a page reload. **Endpoint contract:** respond `text/html` whose body is the
bare count, and an **empty** body when zero (`.badge:empty` hides it). This is the
same contract the core tasks badge uses; it is a seam capability, not a chat
special-case (rule 5).

`Deps.DataDir` is the per-user durable-data directory (`apps.UserDataDir`: XDG data
dir on Linux, `~/Library/Application Support` on macOS) — where an app places its
database, kept out of the *config* dir. It is `""` when app config is in-memory
(tests); an app then stays in-memory and writes no file.

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

apps/chat/daemon   ← the chat application: imports only client, session (not the
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
3. **Nav** — the page envelope calls `Nav(me, superuser)` and marks the entry
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
6. Badge (optional): to show a live attention count on the nav entry, add a `GET
   /<slug>/badge` route (auth-gated) honouring the endpoint contract above and set
   `NavEntry.BadgeURL`. Durable data goes under `Deps.DataDir`, not the config dir.
7. Run `go test ./...` (unit + the grammar drift guard) and **drive the real app
   end-to-end** against a throwaway kernel — a single juice-ui launch, with
   realistic pricing (native actions can cost credits).

## Reference implementation: chat

Chat is the worked example — a real **application on top of Juice** — in two parts,
both under `apps/chat/`, with **zero chat-specific code in juice-ui core**.

### 1. The chat daemon (`apps/chat/daemon`)

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
  auth hook returns (in juice-ui, the store resolving, by hash, the key the kernel
  injects for that user's `delegated_bearer` grant), so the daemon
  authorizes *at the door* — creator-only admin ops, member-only posting — and
  returns a real success or a typed failure, never a hollow ack.
- **Durable, indexed, incremental.** The store is the source of truth (the kernel
  ledger is demoted to an audit trail). Reads are bounded range scans over a
  monotonic per-room sequence — never a full-log read — and unread is a count past
  each user's read watermark.

### 2. The UI-integration plugin (`apps/chat`, the `apps.App`)

The only Juice- and UI-aware piece; it crosses both boundaries and touches neither
side's internals.

- **Self-provisions.** On `Start` it loads (or generates + persists) the `chat@<kernel>`
  account password *and* the daemon admin secret via `LoadAppState`/`SaveAppState`,
  and opens the store beside `state.json` (in-memory when there is no persistent
  config). No chat environment variables — one launch and chat works.
- **Owns the `chat@<kernel>` account via `apps.ServiceAccount`.** It hands the seam the
  handle, the persisted password, and its action-registration as the `setup`
  callback; the seam does provisioning, auth, backoff, self-heal, and diagnostics
  (see above). None of that lifecycle code lives in the plugin.
- **Registers the surface** (its `setup`). As `chat@<kernel>`, it registers each `chat@<kernel>/*`
  action with `delegated_bearer` auth, pointing the `source` at the daemon's
  matching write route, and publishes it — reconciling a stale action left by an
  earlier build. It mounts the daemon's write routes under the CSRF-exempt
  `/_kernel/chat/` prefix.
- **Reads** rooms/messages via the daemon's REST `Client`; **writes** by running
  `chat@<kernel>/*` as the signed-in user. The kernel dials the daemon synchronously and
  injects the caller's token, so the write is applied (or rejected) by the time
  `run` returns — no polling.

This is the model for the next app: a durable REST service behind a `Client`, plus
a thin Juice-aware plugin that self-provisions and connects per-user
`delegated_bearer` grants for synchronous identity at the door.
