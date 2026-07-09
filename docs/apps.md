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

### 1. The @chat application (`apps/chat/daemon`)

A self-contained Juice user, run **in-process** by the plugin (its own package,
depending only on the kernel client — testable standalone against a fake/live
kernel).

- **Its own kernel identity.** On `Start` it authenticates as `@chat` and registers
  its own **free (price-0)** actions — `create-room`, `message`, `delete-room`,
  `add-member`, … Users interact by *running* those actions; the kernel gives free
  execution and unforgeable identity. `store.go` holds the pure projection;
  `daemon.go` holds the kernel I/O (login, register, poll) and the in-process facade
  (`Rooms`, `Room`, `Poll`).
- **Event-sourced state + attribution.** Every action call is an event on the
  `@chat` transaction ledger (`{caller_handle, action_name, args}`, entity ids =
  tx ids). It **projects** that ledger into rooms + messages, attributing by the
  unforgeable `caller_handle` and authorizing each event (only the creator manages a
  room, only members post, only a sender deletes their message). The projection is
  disposable — the kernel ledger is the durable truth, replayed on boot. No OAuth,
  no per-user consent: it reads its *own* ledger because it *is* a Juice member.

### 2. The UI-integration plugin (`apps/chat`, the `apps.App`)

Thin, and the only chat-specific thing inside juice-ui.

- **Provisions itself.** On `Start` it loads the `@chat` password via
  `LoadAppState("chat")`; on first run it generates one, `CreateUser`s the account,
  and `SaveAppState`s it. No chat environment variables — one launch and chat works.
- Contributes the `chat` nav entry (its **own inline SVG icon**), the `/chat`
  pages/routes, templates composed from existing grammar (no core CSS), and the
  `POST /_kernel/chat/ack` callback the `@chat` actions point at.
- **Reads** rooms/messages from the in-process app via direct Go calls.
- **Writes** via free `run @chat/*` as the signed-in user — the kernel records the
  caller, which the app attributes — then `Poll`s so the write shows immediately.

This is the model for the next app: make it a Juice user with free actions and a
ledger projection, run it in-process, self-provision through the app config store,
and keep the UI plugin a thin front over it.
