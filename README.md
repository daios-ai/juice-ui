# juice-ui

A terminal-flavoured web interface for the [Juice](#) kernel.

> **Status:** v0.2.0 · design draft. The endpoint surface is gated against a
> running kernel; see [`requirements.md`](requirements.md).

juice-ui is a Go **backend-for-frontend (BFF)**: an HTTP server that renders the
Juice user flows and proxies every request to the kernel **over its documented HTTP
API only** — it never shells out to the CLI, reads the kernel database, or imports
kernel packages. The kernel stays authoritative; the UI holds no durable domain
state beyond the session (tokens, PKCE, CSRF, theme), and re-renders from kernel
responses rather than reproducing kernel logic.

## Highlights

- **CLI-mirroring navigation** — `run · action · process · tx · step · user`, plus
  `admin · peers` for the `@sys` superuser. Each group presents a concept, not a
  command dump.
- **Terminal aesthetic** — a monospace face and selectable **Ghostty 16-colour ANSI
  themes**, every theme derived from one set of design tokens and held to WCAG AA by
  a test.
- **Keyboard-first** — a `Ctrl/Cmd-K` command palette, `g`+letter section jumps,
  `/` to search, `j`/`k`/`Enter` on rows, `?` for help.
- **No SPA** — server-rendered HTML with **htmx** and a few vanilla ES modules. No
  build step, no Node, no framework.
- **Security by construction** — a strict Content-Security-Policy, server-side
  sessions (kernel tokens never reach the browser), CSRF on every state-changing
  route, and counterparty-authored content rendered inert and quarantined from
  chrome.
- **Apps** — a small seam for self-contained modules that ship in the binary. The
  built-in **[chat](apps/chat/README.md)** app (rooms, members, messages, a live
  unread badge) is the reference implementation.

## Prerequisites

- **Go 1.22+**.
- A **running Juice kernel** (default `http://127.0.0.1:4040`).

SQLite is provided by `modernc.org/sqlite` (pure Go, no cgo), so `go build` and
`go test ./...` need no C toolchain and run fully offline.

## Quick start

```sh
# build and run against a local kernel, with dev defaults
make run
# → serving on http://127.0.0.1:8080
```

`make run` uses local defaults (kernel `http://127.0.0.1:4040`, address
`127.0.0.1:8080`, an ephemeral dev session secret). Override them inline:

```sh
make run KERNEL=https://kernel.example.com ADDR=127.0.0.1:9000 SECRET=change-me
```

Or build and run the binary yourself, or install it to your `PATH`:

```sh
make build            # → ./juice-ui
./juice-ui

make install          # → ~/.local/bin/juice-ui  (override PREFIX=/usr/local, etc.)
```

## Configuration

Configuration is layered **defaults < `config.json` < environment variables**
(environment always wins), all optional. The essentials:

| Variable | Purpose | Default |
|---|---|---|
| `JUICE_HOME` | root for all state (`$JUICE_HOME/ui/`) | `~/.juice` |
| `JUICE_KERNEL_URL` | kernel API base URL | `http://127.0.0.1:4040` |
| `JUICE_UI_ADDR` | listen address | `127.0.0.1:8080` |
| `JUICE_UI_SESSION_SECRET` | derives the CSRF key; **set it in production** | — (ephemeral key if unset — sessions reset on restart) |
| `JUICE_UI_SECURE` | `true` sets Secure cookies (behind HTTPS) | `false` |
| `JUICE_UI_PUBLIC_URL` | absolute base URL for OAuth redirects (behind a proxy) | derived from the request |

The optional `config.json` (in `$JUICE_HOME/ui/`) holds the same settings plus
`badge_interval_seconds` (how often nav badges poll, default 30). See
**[docs/config.md](docs/config.md)** for the schema and
**[docs/installation.md](docs/installation.md)** for file locations.

## Architecture

The browser talks only to the BFF; the BFF is the sole client of the kernel API.
Kernel access and refresh tokens live server-side in the session and never reach
the browser, HTML, or logs. A single request wrapper handles single-flight token
refresh; login is authorization-code with PKCE.

**Apps** are self-contained modules behind one interface (`apps/apps.go`), in three
layers: a **daemon** (a Juice-agnostic service — chat's is a plain REST chat
server), a **plugin** (the only Juice- and UI-aware piece, which registers the
kernel actions and mounts the browser UI), and the **core** (which imports the seam,
never a concrete app). The litmus test: delete the plugin and the daemon still runs.

- [`requirements.md`](requirements.md) — the UI specification.
- [`docs/apps.md`](docs/apps.md) — the apps seam, its design rules, and the chat
  reference.
- [`docs/ui-grammar.md`](docs/ui-grammar.md) — the visual grammar (the closed set of
  components, colours, and rules).

## Project layout

| Path | Contents |
|---|---|
| `cmd/juice-ui/` | entrypoint: configuration, dependency wiring, serve |
| `client/` | typed kernel HTTP API client and error mapping |
| `session/` | server-side sessions, token lifecycle, PKCE state, CSRF |
| `server/` | router, middleware, handlers, rendering (+ `templates/`, `static/`) |
| `apps/` | the app seam; `apps/chat/` (the plugin) and `apps/chat/daemon/` (the service) |
| `docs/` | apps, ui-grammar, and configuration docs |
| `juicedocs/` | a vendored copy of the kernel's API + requirements — the authoritative kernel reference |
| `e2e/` | a live end-to-end flow harness |

## Development

| Target | Does |
|---|---|
| `make build` | compile `./juice-ui` (version stamped from `git describe`) |
| `make run` | build and run with local dev defaults |
| `make install` | install the binary to `$(PREFIX)/bin` (`PREFIX ?= ~/.local`) |
| `make test` | `go test ./...` |
| `make check` | the CI gate: `gofmt` check + `go vet` + `go test` |
| `make fmt` / `make vet` | format / vet |
| `make clean` | remove build artifacts |

Testing discipline: **every production `.go` file has a `_test.go`**, and
`go test ./...` runs **without network access** — the kernel is faked as an
`httptest.Server`. `make check` is the gate that must pass.

Dependencies are standard-library-first; the only additions are **htmx** on the
front end and `alexedwards/scs`, `gorilla/csrf`, and `modernc.org/sqlite` on the
back end.

## Contributing

- Follow the **visual grammar** ([docs/ui-grammar.md](docs/ui-grammar.md)) — no
  inline styles; compose the existing components.
- Follow the **apps doctrine** ([docs/apps.md](docs/apps.md)) when adding a module.
- Keep file counts small; **every `.go` file needs a `_test.go`**.
- `make check` must pass, and a feature should be exercised against a live kernel.

## Documentation

- [`requirements.md`](requirements.md) — UI requirements and scope.
- [`docs/apps.md`](docs/apps.md) — the apps seam and design rules.
- [`docs/ui-grammar.md`](docs/ui-grammar.md) — the visual grammar.
- [`docs/config.md`](docs/config.md) — configuration settings.
- [`docs/installation.md`](docs/installation.md) — the filesystem convention & install layout.
- [`apps/chat/README.md`](apps/chat/README.md) — the built-in chat app.
- [`juicedocs/`](juicedocs/) — vendored kernel API and requirements.

## License

TBD (not yet licensed).
