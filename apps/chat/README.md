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
                    Owns the store (rooms/members/messages), its HTTP
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
| `daemon/store.go` | SQLite store: rooms, members, messages, and a doorbell-tracking `notifications` table. History-only — no identity is stored here. |
| `daemon/daemon.go` | The REST server: two surfaces (below) and `Ops`, the single source of truth for the write API. |
| `daemon/client.go` | In-process client the plugin uses to read/drive the daemon. |
| `chat.go` | The `apps.App`: lifecycle, action reconciliation, per-user provisioning, and the browser handlers. |
| `templates/` | `chat.html` (room list), `chat_conversation.html` (a room), `_chat_messages.html` (the live-updating message list partial). |

## How identity flows

Chat is itself a **Juice user** (`@chat`). It self-provisions on first run and
persists its credentials via the seam's app-state store (`state.json`) — no
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
  message · delete-message · read`

  Writes, plus one read — **`read`** (`{room, after?}` → recent messages, oldest
  first; member-gated; stateless). The receive loop for any member, human or agent,
  is fully kernel-native: **a Juice doorbell step** notifies "room changed" (see
  "Unread & delivery"), **`@chat/read`** fetches what was said, and a reply is a plain
  **`@chat/message`**. `read` is *stateless* — no watermark, no consume-on-read —
  because unread lives entirely in the steps; it is the read twin of `message`.
  (Contrast the old `inbox`: it conflated unread-tracking with content-read; steps
  took the former, `read` is the latter.) `Ops` is the single source of truth: the
  daemon's routes, the plugin's Juice action registration (`@chat/<name>`), and each
  action's public contract.

- **Admin reads** (`admin/*`, authenticated by a shared secret): `rooms`,
  `rooms/{id}`, `rooms/{id}/history`, and the `rooms/{id}/notify/*` doorbell
  bookkeeping. These are the plugin's in-process transport for rendering the web UI
  (room list, scrollback) and tracking the outstanding doorbell per (member, room).

## Browser routes (mounted by the plugin)

`GET /chat` (room list) · `GET /chat/badge` (unread count fragment) ·
`GET /chat/room/{id}` (a room) · `GET /chat/room/{id}/messages` (live poll) ·
plus `POST` routes for create/rename/delete room, add/remove member, leave,
message, and delete message. Every route is auth-gated.

### Unread & delivery

Unread is Juice **steps** — one source of truth, no watermark. When a message is
posted, the plugin mints a coalesced "doorbell" step (owned/funded by `@chat` off
its outbox seed trace) to each other member: `required_caller = the member`, onward
`@sys/sink`, `partial_args` naming the room. At most one waiting doorbell per
(member, room).

A member's chat unread is therefore exactly their outstanding `@chat` doorbells:

- **The bell** (core step badge/inbox) shows **all** the user's waiting steps.
- **The chat app-badge** (`NavEntry.BadgeURL = /chat/badge`) shows the **subset**
  owned by `@chat` — the bell *filtered to chat*, read from the kernel steps, never a
  separate counter.
- **The room list** bolds and floats-first the rooms with an outstanding doorbell.

Visiting a room **completes** the doorbell (as the member's own token — only the
`required_caller` may complete a step), clearing it from every surface. The
conversation poll does this only when the tab is **visible** (`X-Visible` header), so
a backgrounded tab does not silently clear unread. The `notifications(handle,
room_id, ref)` store table backs only the doorbell *lifecycle* (coalescing +
completion lookup); a standalone (plugin-less) daemon has no unread tracking and runs
as a pure message log + membership server.

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

- **Domain data:** `chat.db` (SQLite, WAL) in the component home (`$JUICE_HOME/ui/`,
  default `~/.juice/ui/`; see `docs/installation.md`). An existing legacy store (a
  config-dir or XDG-data location) is kept in place and migrated manually. It is
  **live user state — never delete it.**
- **Credentials:** the `@chat` password, the daemon admin secret, the
  `CallerIdentity` signing key, and `AuthRev` live in `state.json` (app state).

## Tests

Every `.go` file has a `_test.go`. The daemon tests exercise the store and REST
surface in isolation (no Juice); the plugin tests cover Nav/badge, mark-read
visibility, action reconciliation against a fake kernel, and store-path migration.
Run `go test ./apps/chat/...`.
