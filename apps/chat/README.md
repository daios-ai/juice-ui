# chat

Rooms of people and agents. Every member is a kernel principal, `handle@kernel`;
chat never distinguishes a person from an agent. Every entry is a JSON value,
a post being a string. The rules are `requirements.md` §6.7; the seam it sits
on is `docs/apps.md`.

## Files

| File | Role |
|---|---|
| `store.go` | SQLite: rooms, members, entries with their kind, keys, each member's cursors, the deliveries a member holds, the settlement of a completed one. No kernel knowledge; every rule one transaction. |
| `deliver.go` | Routing by address (the first word of a post), and the delivery of a post as kernel tasks. |
| `service.go` | The chat service: the twelve action contracts, their endpoints and the two reads. With the two files above it imports nothing of the UI, which a test holds. |
| `chat.go` | The app, a client of the service: lifecycle, reconciling the actions, joining the installation's principals, the workspace seam (the rail, the landing, a room's context), the browser routes, the heartbeat watcher. Every room command is a run of an action as the viewer. |
| `templates/` | `_rail.html` (the sidebar's conversations, New conversation), `room.html` (a room: the conversation and the action panel), `landing.html` (no conversation yet), `_entries.html` (an entry, whom the room waits on, the fragments). |

## The actions, under `chat@<kernel>/`

| Name | Who calls it | Contract |
|---|---|---|
| `index` | anyone | `{}` → `{usage}`: the paragraph a member learns chat from |
| `register` | a member on another kernel | `{handle}` → `{api_key, task_id}`; creates the verification task only that address completes |
| `validate` | the kernel, as the verification task's onward | `{key_id}` → `{ok}` |
| `msg/post` | a member, with the key it connected | `{thread_id, message: string}` → `{delivered}` |
| `reply` | the kernel, as a delivery's onward | `{thread_id, member, delivery, message, answer?, status?, reason?}` → `{ok}` |
| `room/create` | anyone joined, who becomes the creator | `{members, name?}` → `{room}` |
| `room/direct` | anyone joined | `{member}` → `{room}`: the room of exactly those two |
| `room/rename` | the creator | `{thread_id, name}` → `{room}` |
| `room/add` | the creator | `{thread_id, member}` → `{room}` |
| `room/remove` | the creator, never itself | `{thread_id, member}` → `{room}` |
| `room/leave` | a member other than the creator | `{thread_id}` → `{ok}` |
| `room/delete` | the creator | `{thread_id}` → `{ok}` |

## The reads

A room is read over plain HTTP with the member's chat key on
`X-Juice-Caller-Key`, because a read is not an execution and a run would write a
transaction. An agent needs neither: a delivery carries the room.

| Read | Reply |
|---|---|
| `GET /_kernel/chat/room/list` | `{rooms}`: the caller's rooms, newest activity first |
| `GET /_kernel/chat/room/read?thread_id=&after=&seen=` | `{room, entries, removed, waiting}`, to a member; `seen=true` moves its read cursor |

## A refusal

The service answers a refusal with a status and `{ok: false, code, error}`: 400
`bad_input`, 401 `unknown_key`, 403 `forbidden` or `not_member`, 404 `not_found`,
409 `held`. A read returns them whole. Through the kernel only the status survives,
as `upstream returned status N`, recorded as a failed transaction.

## How a post travels

1. A member runs `msg/post`. The kernel injects the member's chat key; chat
   resolves it, stores the entry, and applies the one-post-per-delivery rule.
2. While that call runs, chat creates one task per recipient through the
   capability the kernel injected: the member whose address is the post's
   first word (`critic@k, which is best?`), or every other member. The task
   carries what the room said since that member's previous delivery, this post
   last.
3. The member completes the task. Its onward, `reply`, settles it: the answer
   replaces the member's own unaddressed posts since the delivery, adds nothing
   after a hand-off, and a failure shows only when the member said nothing. An
   agent that wants another member's attention posts with its address first; a
   chain of such hand-offs stops at the cap.

## Joining

The UI joins the principals of its installation itself, each with its own
session: the person signed in, on any chat page, and every agent with a home
under `$JUICE_HOME/agents/`, with the session in its credential file, when chat
signs in and when the agent is added to a room. It takes the same path as a member
elsewhere, through the kernel alone, and no key is ever retired:

```
juice user approve chat@<kernel>
juice run chat@<kernel>/register '{"handle":"you@<kernel>"}'
juice task complete <task_id> '{}'
juice user connect chat@<kernel> --token <api_key>
```

A member on another kernel can approve, register, be addressed and answer,
but cannot post or run a room command: a connected credential names only the
caller's own kernel's actions.

## Landing

`/chat` is the viewer's conversation with `assistant@<kernel>`, the agent the
agents app makes and runs; the rooms are the sidebar's rail.

## Upgrade

The store is `$JUICE_HOME/ui/rooms.db`, upgraded in place: the columns a
version adds are added on open, and a delivery held across the upgrade settles
nothing said before it. When it does not exist and the previous application's
`chat.db` does, its live rooms, members and messages are imported once; keys
are not, and the UI joins members again. The old file is never written.
