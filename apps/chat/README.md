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
apps/chat/daemon/   the chat service — a self-contained REST chat server.
                    Owns the store (rooms/members/messages), its HTTP
                    contract, and list ordering. Imports no juice-ui packages at
                    all (stdlib + the SQLite driver). Knows nothing about Juice:
                    auth is injected as a hook.

apps/chat/          the plugin (the apps.App). The only Juice + UI-aware piece.
                    Runs the daemon in-process, registers the chat kernel
                    actions and per-user grants, and mounts the browser UI.
```

**Litmus test:** delete the plugin and the daemon still compiles and runs as a
plain chat server. That is why auth is a `func(*http.Request) (handle string, ok
bool)` the plugin supplies, not something the daemon imports.

| File | Role |
|---|---|
| `daemon/store.go` | SQLite store: rooms, members, messages, a doorbell-tracking `notifications` table, and a `tokens` table (hashed API keys → handle). |
| `daemon/daemon.go` | The REST server: two surfaces (below), `Ops` (the single source of truth for the member write API), and the private `validate-user` activation route. |
| `daemon/client.go` | In-process client the plugin uses to read/drive the daemon. |
| `chat.go` | The `apps.App`: lifecycle, action reconciliation, registration/verification, and the browser handlers. |
| `templates/` | `chat.html` (room list), `chat_conversation.html` (a room), `_chat_messages.html` (the live-updating message list partial). |

## How identity flows

Chat is itself a **Juice user** (`chat`). It self-provisions on first run and
persists its credentials via the seam's app-state store (`state.json`) — no
per-app environment variables.

- **Outbound** (chat acting as a Juice user): `apps.ServiceAccount` — provision
  once, PKCE login, token refresh, self-heal with backoff.
- **Inbound** (identifying the user who calls a chat action): `chat`'s own **API
  keys**. A caller registers a `handle` and, once its owner accepts a verification
  step, receives an activated key; the daemon stores only the key's **hash → handle**.
  There is no juice-ui secret involved, so the same path works for a headless agent
  and for a browser user. See "Registration & verification" below.

A member write travels: browser → plugin issues the `chat/msg/<op>` action **as the
signed-in user** → the kernel injects that user's connected key into the
`X-Juice-Caller-Key` header → the kernel calls the daemon's write route → the injected
auth hook (`store.ResolveToken`) hashes the key and recovers the caller's handle
(rejecting an unknown or not-yet-activated key).

### Registration & verification

Three actions, split into folders so one grant selector covers the members:

- **`chat/register`** — *open* (a `bearer` upstream secret, `requires_grant:false`),
  so any kernel-authenticated caller invokes it with **no grant**. It takes a
  `handle`, mints a **pending** key, and sends that handle a verification **step**
  (minted off the outbox seed, so `chat` owns it; its onward is the private
  `chat/validate-user`). Returns `{api_key, step_id}`. Handled by the *plugin*
  (`serveRegister`), not a daemon Op, because minting the step is a Juice call.
- **`chat/validate-user`** — *private*. Reached only as the verification step's
  onward: only the claimed handle's owner can complete that step (the kernel enforces
  `required_caller` — this is the ownership proof, no app logic), and completion runs
  this action, which activates the key.
- **`chat/msg/*`** — `delegated_bearer` member actions. The caller runs `juice user
  connect chat/msg --token <api_key>` once; the key is then injected on every call.

The **"Connect to chat"** button runs this exact path over the signed-in user's own
session — `chat/register` → complete the step → `ConnectGrant(chat/msg, key)` — so
the browser relies on no privileged provisioning path a headless agent lacks. The
rooms page is gated on the `chat/msg` connector being present in `/v1/me`.

## The daemon's two REST surfaces

- **Kernel-facing actions** (`KernelHandler`, mounted by the plugin under the
  CSRF-exempt `/_kernel/chat/` prefix). Fixed URLs, args in the JSON body. The member
  actions are the `Ops`, and all live in the **`msg/`** folder so one grant selector
  (`chat/msg`) covers them:

  `msg/create-room · msg/delete-room · msg/add-member · msg/remove-member ·
  msg/leave-room · msg/message · msg/delete-message · msg/read`

  Writes, plus one read — **`msg/read`** (`{room, after?}` → recent messages, oldest
  first; member-gated; stateless). The receive loop for any member, human or agent,
  is fully kernel-native: **a Juice doorbell step** notifies "room changed" (see
  "Unread & delivery"), **`chat/msg/read`** fetches what was said, and a reply is a
  plain **`chat/msg/message`**. `read` is *stateless* — no watermark, no
  consume-on-read — because unread lives entirely in the steps; it is the read twin of
  `message`. `Ops` is the single source of truth: the daemon's routes, the plugin's
  Juice action registration (`chat/<name>`), and each action's public contract.
  Alongside the `Ops` sit the three actions reconciled separately (`register`,
  `validate-user`, `outbox` — see "How identity flows" and "Unread & delivery").

- **Admin reads** (`admin/*`, authenticated by a shared secret): `rooms`,
  `rooms/{id}`, `rooms/{id}/history`, and the `rooms/{id}/notify/*` doorbell
  bookkeeping. These are the plugin's in-process transport for rendering the web UI
  (room list, scrollback) and tracking the outstanding doorbell per (member, room).

## Browser routes (mounted by the plugin)

`GET /chat` (room list, or the Connect gate) · `POST /chat/connect` (the one-click
onboarding) · `GET /chat/badge` (unread count fragment) · `GET /chat/room/{id}` (a
room) · `GET /chat/room/{id}/messages` (live poll) · plus `POST` routes for
create/delete room, add/remove member, leave, message, and delete message. Every route
is auth-gated. `GET /chat` and `GET /chat/room/{id}` are additionally gated on the
`chat/msg` connector (`/v1/me`): without it they show / redirect to the Connect gate.

### Unread & delivery

Unread is Juice **steps** — one source of truth, no watermark. When a message is
posted, the plugin mints a coalesced "doorbell" step (owned/funded by `chat` off
its outbox seed trace) to each other member: `required_caller = the member`, onward
`sys/sink`, `partial_args` naming the room. At most one waiting doorbell per
(member, room).

A member's chat unread is therefore exactly their outstanding `chat` doorbells:

- **The bell** (core step badge/inbox) shows **all** the user's waiting steps.
- **The chat app-badge** (`NavEntry.BadgeURL = /chat/badge`) shows the **subset**
  owned by `chat` — the bell *filtered to chat*, read from the kernel steps, never a
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

On start, the plugin makes each `chat/msg/*` action exist, active, public, and
pointed at the daemon's matching write route with `delegated_bearer` auth. A correctly
configured action is left untouched, so per-user grants survive restarts. The three
non-`Ops` actions — the open `register` (public, `bearer`), the private
`validate-user` (`bearer`), and the private `outbox` (no auth) — are reconciled
separately by `ensureSpecialAction` (they have fixed sources and stable auth, so they
need neither `contractDrifted` nor the `AuthRev` machinery).

The kernel never returns an action's auth config (write-only), so a read-based
reconciler cannot see a change to the `delegated_bearer` auth header. The plugin
therefore versions that config locally (`chatState.AuthRev`); on a change it
force-re-pushes auth on every member action. An auth change revokes existing
`chat/msg` grants, so users re-establish them by clicking **Connect to chat** again
(there is no silent re-provisioning). See `docs/apps.md` → "Reconciling write-only
auth config".

## Storage

- **Domain data:** `chat.db` (SQLite, WAL) in the component home (`$JUICE_HOME/ui/`,
  default `~/.juice/ui/`; see `docs/installation.md`). It holds rooms, membership,
  messages, and the `tokens` table (hashed API keys → handle — only the hash, so a
  store leak yields no usable key). It is **live user state — never delete it.**
- **Credentials:** the `chat` password, the daemon admin secret, the register and
  validate upstream secrets, and `AuthRev` live in `state.json` (app state).

## Tests

Every `.go` file has a `_test.go`. The daemon tests exercise the store and REST
surface in isolation (no Juice), including the `tokens` register/activate/lookup path
and the `validate-user` route; the plugin tests cover Nav/badge, the connector gate,
`serveRegister`, the Connect route (register → complete → connect), action
reconciliation against a fake kernel, and store-path resolution. Run
`go test ./apps/chat/...`.
