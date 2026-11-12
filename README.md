# juice-ui

The desktop edition of [Juice](https://github.com/daios-ai/juice): a web interface
that installs and runs a whole Juice installation — the kernel, chat, and an
assistant agent — and gives people and agents one workspace to work in.

juice-ui is a Go **backend-for-frontend (BFF)**: an HTTP server that renders the
Juice user flows and proxies every request to the kernel **over its documented HTTP
API only** — it never shells out to the CLI, reads the kernel database, or imports
kernel packages. The kernel stays authoritative; the UI holds no durable domain
state beyond the session (tokens, PKCE, CSRF, theme), and re-renders from kernel
responses rather than reproducing kernel logic.

## Highlights

- **One collaborative workspace** — conversations among people and agents are
  the frame; actions are chosen from the composer's `/`, run in a panel beside
  the conversation, and their results carried into the next message or action.
  Inbox, Discover, Workshop, Wallet and Settings (and Network, Admin and
  Agents for `sys`) are destinations of the avatar menu.
- **Terminal aesthetic** — a monospace face and selectable **Ghostty 16-colour ANSI
  themes**, every theme derived from one set of design tokens and held to WCAG AA by
  a test.
- **Keyboard-first** — `/` for actions and pages, Enter to send, `g`+letter
  jumps, `j`/`k`/`Enter` on rows, `?` for help.
- **No SPA** — server-rendered HTML with **htmx** and a few vanilla ES modules. No
  build step, no Node, no framework.
- **Security by construction** — a strict Content-Security-Policy, server-side
  sessions (kernel tokens never reach the browser), CSRF on every state-changing
  route, and counterparty-authored content rendered inert and quarantined from
  chrome.
- **Apps** — a small seam for self-contained modules that ship in the binary. The
  built-in **[chat](apps/chat/README.md)** app is the workspace — rooms of
  people and agents, input and output through the one form and view, delivered as kernel tasks —
  is the reference implementation; **agents** manages the installation's
  juice-agents.

## Install

On macOS or Linux, Intel or ARM:

```sh
curl -fsSL https://raw.githubusercontent.com/daios-ai/juice-ui/main/install.sh | sh
```

That puts `juice-ui`, `juice-chat` and `juice-supervisor` in `~/.juice/bin` and adds
it to your PATH. Options go after `sh -s --`:

```sh
curl -fsSL https://raw.githubusercontent.com/daios-ai/juice-ui/main/install.sh | sh -s -- VERSION
curl -fsSL https://raw.githubusercontent.com/daios-ai/juice-ui/main/install.sh | sh -s -- --no-modify-path
```

Then start it and open `http://127.0.0.1:8080`:

```sh
juice-ui
```

The first start installs the rest of Juice into `~/.juice/bin`, the
[kernel](https://github.com/daios-ai/juice) and the
[agent](https://github.com/daios-ai/juice-agent), each at the release this UI was
verified against, and asks you to create your kernel in the browser
(`requirements.md` §1). `JUICE_HOME` moves the whole installation.

When a newer release is out, Juice says so above your avatar, and Admin's
**Update** installs it and restarts Juice; the kernel and the agent follow
([docs/installation.md](docs/installation.md#updating)).

## Build from source

Building needs **Go 1.22+**. SQLite is provided by `modernc.org/sqlite` (pure Go,
no cgo), so `go build` and `go test ./...` need no C toolchain and run fully
offline.

```sh
make build            # → ./juice-ui, ./juice-chat, ./juice-supervisor
make install          # → $JUICE_HOME/bin/  (default ~/.juice; override PREFIX=…)
```

`make run` builds and runs against a local kernel with dev defaults (kernel
`http://127.0.0.1:4040`, address `127.0.0.1:8080`, an ephemeral session secret).
Override them inline:

```sh
make run KERNEL=https://kernel.example.com ADDR=127.0.0.1:9000 SECRET=change-me
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
`badge_interval_seconds` (how often the task badge is refreshed, default 30). See
**[docs/config.md](docs/config.md)** for the schema and
**[docs/installation.md](docs/installation.md)** for file locations.

## Architecture

The browser talks only to the BFF; the BFF is the sole client of the kernel API.
Kernel access and refresh tokens live server-side in the session and never reach
the browser, HTML, or logs. A single request wrapper handles single-flight token
refresh; login is authorization-code with PKCE. A session ends 30 minutes
after the person's last request and 12 hours after login; the page's heartbeat
never counts as activity, and every sign-out revokes the refresh token at the
kernel that issued it.

**Apps** are self-contained modules behind one interface (`apps/apps.go`), in
three layers: a **store** (the app's own state, knowing nothing of Juice), a
**plugin** (the only Juice- and UI-aware piece, which registers the kernel
actions and mounts the browser UI), and the **core** (which imports the seam,
never a concrete app).

- [`requirements.md`](requirements.md) — the UI specification.
- [`docs/apps.md`](docs/apps.md) — the apps seam, its design rules, and the chat
  reference.
- [`docs/ui-grammar.md`](docs/ui-grammar.md) — the visual grammar (the closed set of
  components, colours, and rules).

## Project layout

| Path | Contents |
|---|---|
| `cmd/juice-ui/` | entrypoint: configuration, dependency wiring, serve |
| `cmd/juice-chat/`, `services/chat/` | the chat service, a program of its own that the supervisor runs and the UI reaches through the kernel |
| `cmd/juice-supervisor/`, `supervisor/` | the process supervisor: runs the kernel, the agents, chat and an operator's programs, inside the UI or from the command line |
| `client/` | typed kernel HTTP API client and error mapping |
| `session/` | server-side sessions, token lifecycle, PKCE state, CSRF |
| `server/` | router, middleware, handlers, rendering (+ `templates/`, `static/`) |
| `apps/` | the app seam; `apps/chat/` (the conversation) and `apps/agents/` (the installation's agents) |
| `docs/` | apps, ui-grammar, and configuration docs |
| `juicedocs/` | a vendored copy of the kernel's API + requirements — the authoritative kernel reference |
| `e2e/` | a live end-to-end flow harness |

## Development

| Target | Does |
|---|---|
| `make build` | compile `./juice-ui`, `./juice-chat` and `./juice-supervisor` (version stamped from `git describe`) |
| `make run` | build and run with local dev defaults |
| `make install` | install the binary to `$(PREFIX)/bin` (`PREFIX ?= $JUICE_HOME`, default `~/.juice`) |
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

Copyright (C) 2026 Pedro A. Ortega <pedro.ortega@gmail.com>.

Juice-UI is licensed under the [Apache License 2.0](LICENSE). What was copied
from the [juice](https://github.com/daios-ai/juice) repository keeps its licence,
the [GNU AGPL 3.0 only](LICENSES/AGPL-3.0-only.txt): `juicedocs/` and the Juice
logo, with the icons rendered from it. Third-party assets keep theirs.
[`NOTICE`](NOTICE) lists every exception, and [`REUSE.toml`](REUSE.toml) states
each file's licence.
