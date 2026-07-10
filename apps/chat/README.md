# chat

The chat app — juice-ui's built-in communication channel. Users create rooms,
room admins add and remove members, and members post messages. It ships in the
juice-ui binary and appears as the **chat** entry in the sidebar, with a live
unread badge.

Chat is the reference **app** for the `apps` seam. Read `docs/apps.md` for the
seam contract and the five design rules; this README describes chat specifically.

## Two layers

Chat is split so that the domain logic never depends on Juice or on juice-ui:

```
apps/chat/daemon/   the @chat service — a self-contained REST chat server.
                    Owns the store (rooms/members/messages/reads), its HTTP
                    contract, and list ordering. Imports no juice-ui packages at
                    all (stdlib + the SQLite driver). Knows nothing about Juice:
                    auth is injected as a hook.

apps/chat/          the plugin (the apps.App). The only Juice + UI-aware piece.
                    Runs the daemon in-process, registers the @chat kernel
                    actions and per-user grants, and mounts the browser UI.
```

**Litmus test:** delete the plugin and the daemon still compiles and runs as a
plain chat server. That is why auth is a `func(*http.Request) (handle string, ok
bool)` the plugin supplies, not something the daemon imports.

| File | Role |
|---|---|
| `daemon/store.go` | SQLite store: rooms, members, messages, per-user read watermarks. History-only — no identity is stored here. |
| `daemon/daemon.go` | The REST server: two surfaces (below) and `Ops`, the single source of truth for the write API. |
| `daemon/client.go` | In-process client the plugin uses to read/drive the daemon. |
| `chat.go` | The `apps.App`: lifecycle, action reconciliation, per-user provisioning, and the browser handlers. |
| `templates/` | `chat.html` (room list), `chat_conversation.html` (a room), `_chat_messages.html` (the live-updating message list partial). |

## How identity flows

Chat is itself a **Juice user** (`@chat`). It self-provisions on first run and
persists its credentials via the seam's app-state store (`juice-ui.json`) — no
per-app environment variables.

- **Outbound** (chat acting as a Juice user): `apps.ServiceAccount` — provision
  once, PKCE login, token refresh, self-heal with backoff.
- **Inbound** (identifying the user who calls a chat action): `apps.CallerIdentity`
  — a stateless, deterministic HMAC token `token(handle) =
  base64url(handle).HMAC(key, handle)`. The daemon stores no per-user keys, so
  identity can never desync from a kernel grant and survives any store reset.

A write travels: browser → plugin issues the `@chat/<op>` action **as the signed-in
user** → the kernel injects that user's `delegated_bearer` token into the
`X-Juice-Caller-Key` header → the kernel calls the daemon's write route → the
injected auth hook (`CallerIdentity.Authenticate`) recovers the caller's handle.

## The daemon's two REST surfaces

- **Kernel-facing actions** (`KernelHandler`, mounted by the plugin under the
  CSRF-exempt `/_kernel/chat/` prefix). Fixed URLs, args in the JSON body,
  per-caller auth injected by the kernel. These are the `Ops`:

  `create-room · delete-room · add-member · remove-member · leave-room ·
  message · delete-message · inbox`

  Seven are writes; **`inbox`** is the read half of the channel — it returns the
  caller's rooms (with unread counts) and their unseen messages, marking them seen
  (consume-on-read), so a headless Juice client can *receive*, not only send. `Ops`
  is the single source of truth: the daemon's routes, the plugin's Juice action
  registration (`@chat/<name>`), and each action's public contract (description +
  JSON-Schema args and result, written to satisfy `@sys/lookup` and LLM function
  calling).

- **Admin reads** (`admin/*`, authenticated by a shared secret): `rooms`,
  `rooms/{id}`, `rooms/{id}/history`, `rooms/{id}/read`, `unread`. These are the
  plugin's in-process transport for rendering the web UI (room list, scrollback,
  the unread badge) — plumbing, not a capability. The rule the boundary enforces: a
  *capability* (send, receive) must be a Juice action; an endpoint that only renders
  already-delivered content (e.g. `history` scrollback) may stay admin-only. So the
  comms loop is fully action-reachable, and the UI's reads ride a private surface.

## Browser routes (mounted by the plugin)

`GET /chat` (room list) · `GET /chat/badge` (unread count fragment) ·
`GET /chat/room/{id}` (a room) · `GET /chat/room/{id}/messages` (live poll) ·
plus `POST` routes for create/rename/delete room, add/remove member, leave,
message, and delete message. Every route is auth-gated.

### Unread & the badge

Per-room and total unread come from the read watermark in the store. The sidebar
badge is a seam capability (`NavEntry.BadgeURL = /chat/badge`): the core polls it
on the configured interval (`badge_interval_seconds`). The room list bolds unread
rooms, shows a count, and sorts unread-first by recent activity.

The conversation page polls for new messages and advances the read watermark — but
only when the tab is **visible** (via the `X-Visible` header), so a backgrounded
tab keeps its content fresh without silently clearing unread.

## Action reconciliation (the subtle part)

On start, the plugin makes each `@chat` action exist, active, public, and pointed
at the daemon's matching write route with `delegated_bearer` auth. A correctly
configured action is left untouched, so per-user grants survive restarts.

The kernel never returns an action's auth config (write-only), so a read-based
reconciler cannot see a change to the auth header. The plugin therefore versions
the auth config locally (`chatState.AuthRev`); on a change it force-re-pushes auth
on every action and resets the per-user provisioning guard, because an auth change
revokes existing grants. See `docs/apps.md` → "Reconciling write-only auth config".

## Storage

- **Domain data:** `chat.db` (SQLite, WAL) under the per-user data dir
  (`apps.UserDataDir` → `~/.local/share/juice-ui` on Linux). An existing legacy
  store under the config dir is kept in place; migration is manual and documented
  in `docs/config.md`. It is **live user state — never delete it.**
- **Credentials:** the `@chat` password, the daemon admin secret, the
  `CallerIdentity` signing key, and `AuthRev` live in `juice-ui.json` (app state).

## Tests

Every `.go` file has a `_test.go`. The daemon tests exercise the store and REST
surface in isolation (no Juice); the plugin tests cover Nav/badge, mark-read
visibility, action reconciliation against a fake kernel, and store-path migration.
Run `go test ./apps/chat/...`.
