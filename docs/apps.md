# App Modules

Juice-ui has a small core BFF (the CLI-mirroring surface: `run`, `action`,
`process`, `tx`, `task`, `user`, `admin`, `peers`) plus **apps** that don't belong
in that surface — **chat** (`apps/chat`), the surface a session lands on, and
**agents** (`apps/agents`). An app is a self-contained
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
  when the UI is torn down. A capability the network should reach is not the app's
  to serve: it is a service, a program of its own that the kernel wraps as actions,
  which the app declares to the process supervisor (`Deps.Programs`, package
  `supervisor`) and otherwise reaches only by running those actions through the kernel
  (chat, `services/chat`). An app starts and signals no process itself.

> The **visual grammar stays central**, deliberately. An app owns its Go,
> templates, and routes — but its components register in the one place
> (`docs/ui-grammar.md` + the drift guard in `server/grammar_test.go`, which scans
> app templates too). Code is modular; the grammar is unified. That centralization
> is what keeps every app visually coherent.

## Design rules & rationale

Five rules govern every app. They are what keep the codebase modular without the
core rotting into a monolith; read them before touching this seam.

1. **Dependency direction is law.** *store* (the app's own state) ← *plugin*
   (the App: the only Juice + UI-aware piece) ← *core* (imports only the `apps`
   seam, never a concrete app). An app imports `client`, `session`, `apps` — never
   `server`. The arrows never reverse.
2. **Litmus test: the store knows nothing of Juice, and the plugin knows nothing
   of the store.** Chat's `store.go` is SQL over rooms, members, entries, keys and
   cursors, and decides who may do what by membership alone; it compiles and is
   tested without a kernel. If the store needs the plugin, or the plugin calls the
   store rather than the service, the boundary has leaked.
3. **Ownership split.** The store owns *domain state* and its invariants, each
   as one transaction. The plugin owns *Juice/UI integration* (kernel actions,
   grants, identity, templates, its menu entry and badge, the kernel's dispatches).
4. **The caller is who the kernel says.** A `delegated_bearer` action carries the
   key the caller connected on a header the service names; the service resolves that
   key to the caller and trusts nothing else in the request.
5. **Self-provisioning; new capabilities go in the seam.** An app provisions its own
   secrets into app state (no per-app environment variables). A capability more than
   one app could want — the app-state store, `ServiceAccount`, `Bin`,
   badge polling, `UserDataDir` — lands in `apps/apps.go`, never hardwired into one
   app. The badge poll is the worked example: chat asked for a live nav count, and
   the answer was a seam capability (a heartbeat watcher under the app's slug)
   every future app inherits.

## The seam (`apps/apps.go`)

```go
type App interface {
    Slug() string
    Nav(me string, superuser bool) []NavEntry     // self-gated destinations of the avatar menu; me is the signed-in address
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
- **`ConfigEditor(schema, hidden...)`** — the editing of a JSON configuration
  file through the form of its JSON Schema, the kernel's configuration page's
  own editor (requirements §6.6): `Form` for `{{template "_form_fields" .}}`
  in a `[data-json]` form carrying `Version` in its `version` field, `Read` for
  the save, `ToJSON` and `ToForm` for the switch. The agents app edits an
  agent's `config.json` with it.
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
`X-Chat-Key` → `X-Juice-Caller-Key`) and every readable field still matches, so a
read-based diff sees no drift while the kernel keeps injecting the caller's token
into the *old* header — the service reads nothing and returns 401.

The fix is to **version the auth config locally** and force a re-push on change:

- Persist a short revision of the pushed auth config (chat stores a hash of the auth
  JSON per provider in its home's `state.json`).
- On reconcile, compute `force := storedRev != currentRev`. When `force`, `Update`
  every action with the **full** contract *including `auth`* (not only the readable
  fields), then persist the new revision. Steady-state restarts (matching revision)
  do nothing.
- **An auth change revokes the action's grants** (kernel §8 — verified: renaming the
  header alone empties every caller's grant list). So after a forced re-push the
  app must join its users again — a run the kernel refuses for want of a grant joins
  the person, and the UI joins each local agent at every start (requirements §6.7,
  rule 8). Without this the actions are correct but every user is
  unauthenticated.

Also fold the readable signals into the ordinary drift check: treat
`auth_scheme != "delegated_bearer"` or `requires_grant == false` as drift, so an
action that lost its auth entirely is repaired without waiting for a revision bump.

(Re-pushing the *unchanged* action `source` alongside the new auth is safe even when
that source is a loopback URL: the kernel's SSRF guard validates a source only when
its value changes, so an identical re-push is accepted.)

`NavEntry` is one destination an app adds to the registry — the avatar menu and
the launcher (`Slug`, `Label`, sentence case, `Href`, `Match` — the path prefix
that marks it current, `Icon` — the app's own inline `<svg class="ico">` for its menu row). `Live` says its badge
is kept by the heartbeat: the app registers a watcher under its slug that
answers the key `badge` with `apps.Badge`.

**The conversations.** One app may be the workspace itself by implementing
`apps.Conversations` (requirements §6.1): `Rail(r)` is the sidebar's list of
the viewer's conversations, `Landing()` is where a session begins, and
`Context(r, room)` is a room's recent conversation as the kernel's planner
reads it (`[]client.Message`, newest last, refused to a non-member). The core
finds it among the apps; chat is it, and contributes no `NavEntry`.

`Deps.DataDir` is the per-user durable-data directory (`apps.UserDataDir`: XDG data
dir on Linux, `~/Library/Application Support` on macOS) — where an app places its
database, kept out of the *config* dir. It is `""` when app config is in-memory
(tests); an app then stays in-memory and writes no file.

### Dependency direction (no cycles)

```
apps               ← the seam (leaf; imports client, session)
  ↑        ↑
server   apps/chat, apps/agents   (the apps; consume the seam)
  ↑        ↑
cmd/juice-ui               (wires a []App into server.New)
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
3. **Registry** — the page envelope folds each app's `Nav(me, superuser)` into
   the one registry (`server/commands.go`), marking the entry whose `Match`
   prefixes the request path as current; the conversations app's `Rail` is the
   sidebar's content.

## Adding a new app — checklist

1. Create `apps/<name>/` with `<name>.go` implementing `apps.App`, an embedded
   `templates/` dir, and `*_test.go` for every `.go` file.
2. Templates: page files define `{{define "content"}}`; shared bits are `_*.html`
   partials. **Compose the existing grammar utilities** (`.stack`, `.row`, `.card`,
   `.mono`, `.owner`, `_ref`, …) — never invent chrome, and do **not** edit core
   `app.css`/`icons.svg`. The drift guard scans your templates.
3. **Never touch a core file for app-specific content.** The only acceptable core
   edits are to the seam itself (`apps/`, the mount/render wiring in `server`, the
   wiring line in `cmd`). Everything else lives in `apps/<name>/`.
4. Wire it in `cmd/juice-ui/main.go`: construct it, pass `Templates()` to
   `server.NewRenderer`, add it to the `Apps` slice.
5. State: keep the core BFF stateless. An app that needs its own actions and durable
   state fronts a **service**: a program of its own, under `services/<name>/` of the
   installation, that registers its actions and keeps its store, as `juice-chat` does.
   The app **self-provisions** the provider's account on first run and persists the
   password via `SaveAppState`, so there are no per-app env vars. It reads and writes
   by running the service's actions as the signed-in user.
6. Badge (optional): to keep a count on the app's destination, set
   `NavEntry.Live` and register a watcher under the slug answering `badge` with
   `apps.Badge`. Durable data goes under `Deps.Home`, not the config dir.
7. Run `go test ./...` (unit + the grammar drift guard) and **drive the real app
   end-to-end** against a throwaway kernel — a single juice-ui launch, with
   realistic pricing (native actions can cost credits).

## Reference implementation: chat

Chat is the worked example — a real **application on top of Juice** — with
**zero chat-specific code in juice-ui core**. Its rules are `requirements.md`
§6.7; this is how it sits on the seam.

- **A program and a client of it.** `services/chat` is the program's code:
  `store.go` is the state (SQLite: rooms, members, entries, keys, cursors, the
  deliveries a member holds), every rule that must hold together one transaction, and
  no kernel knowledge; `deliver.go` is routing by address and the delivery of a post
  as kernel tasks; `service.go` is the HTTP service and the fourteen action contracts;
  `register.go` reconciles them on a kernel. `cmd/juice-chat` is its main. They import
  the kernel client and nothing else of this repository, which a test holds. `chat.go`
  is the App: starting and stopping the program, joining the installation's
  principals, the conversations seam (the rail, the landing, a room's context), the
  browser routes and the heartbeat watcher, all of it a client of the actions.
- **Self-provisions.** On `Start` it loads, or generates and persists, the
  `chat@<kernel>` password. Once signed in through `apps.ServiceAccount` it records
  that session in the client records, declares the program `chat` to the supervisor,
  runs `juice-chat register`, and joins the local agents. The program keeps its own secret in its own home.
- **Identity at the door.** `msg/post`, the room commands and the room reads are
  `delegated_bearer` on `X-Juice-Caller-Key`; the store resolves the injected key to
  the member. `validate` and `reply` are the onwards of tasks and carry chat's bearer,
  so only the kernel completing a task reaches them. `register` and `index` are open.
- **Tasks are the delivery.** A post creates, through the capability the kernel
  injected into that dispatch (`client.CallbackCreateTask`), one task per
  recipient, and completing it is the answer. No seed trace, no sentinel, no
  app-side notification: the kernel's one signal to a member is a task.
- **The UI joins its principals, through the kernel alone.** It holds the person's
  session and every local agent's credential file, so as each of them it approves
  chat, runs `register`, completes the verification task and connects the key at
  `chat@<kernel>`; nobody is asked to connect. It keeps no chat key.
- **The browser is a member.** Every room read and command on a page is a run of
  chat's action as the viewer, so a person, an agent and a CLI user take one path. A
  page runs a read only when the viewer's open chat tasks moved or the person acted
  (requirements §6.7), so an idle page writes nothing. A refused run reaches the UI as
  its status alone (`KernelError.UpstreamStatus`), so the sentence is the UI's.
