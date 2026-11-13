# juice-chat

Rooms of people and agents, as an ordinary HTTP server of the Juice family
(ecosystem-standard §7) that a kernel wraps as actions. Every member is a kernel
principal, `handle@kernel`; chat never distinguishes a person from an agent. Every
entry is a JSON value, a post being a string. The rules are `requirements.md` §6.7.

It imports the kernel client (`client/`) and nothing else of this repository, so
moving it to a repository of its own is a copy of this directory and `cmd/juice-chat`.

## Running it

```
juice-chat serve             serve $JUICE_HOME/services/chat
juice-chat register LOGIN    make LOGIN's actions on its kernel exactly chat's
```

`serve` holds no kernel credential and serves every provider that registered it: an
action's source is `<public_url>/<provider>/<name>`. `register` reads the kernel and
LOGIN's session from the installation's client records, and deletes any other action
LOGIN owns, so the login is chat's alone. The home holds `config.json` (`listen_addr`,
`public_url`), `state.json`, `rooms.db` and `serve.lock`.

## Files

| File | Role |
|---|---|
| `store.go` | SQLite: rooms, members invited or joined, entries with their kind, keys, each member's cursors, the deliveries a member holds, the settlement of a completed one. No kernel knowledge; every rule one transaction. |
| `deliver.go` | Routing by address (the first word of a post), the delivery of a post and the invitation to a room as kernel tasks. |
| `service.go` | The fifteen action contracts and their endpoints. |
| `register.go` | The service's state, and reconciling the actions on a kernel. |

## The actions, under `chat@<kernel>/`

| Name | Who calls it | Contract |
|---|---|---|
| `index` | anyone | `{}` → `{usage}`: the paragraph a member learns chat from |
| `register` | a member on another kernel | `{handle}` → `{api_key, task_id}`; creates the verification task only that address completes |
| `validate` | the kernel, as the verification task's onward | `{key_id}` → `{ok}` |
| `msg/post` | a member, with the key it connected | `{thread_id, message: string}` → `{delivered, undelivered}`: how many it reached, and whom it could not, by address |
| `reply` | the kernel, as a delivery's onward | `{thread_id, member, delivery, message, answer?, status?, reason?}` → `{ok}` |
| `accept` | the kernel, as an invitation's onward | `{thread_id, member, name?, from?}` → `{ok}`: the member joins |
| `room/create` | anyone joined, who becomes the creator | `{members, name?}` → `{room, uninvited}`: the others are invited; `uninvited` names those not reached yet |
| `room/rename` | the creator | `{thread_id, name}` → `{room}` |
| `room/add` | the creator | `{thread_id, member}` → `{room, uninvited}`: invites, once; a member not reached yet is tried again |
| `room/remove` | the creator, never itself | `{thread_id, member}` → `{room}`: removes a member or withdraws an invitation |
| `room/leave` | a member other than the creator | `{thread_id}` → `{ok}`: an invited member declines |
| `room/join` | a member invited | `{thread_id}` → `{room}`: the caller accepts |
| `room/delete` | the creator | `{thread_id}` → `{ok}` |
| `room/list` | anyone joined | `{}` → `{rooms, invitations}`: the caller's rooms, newest activity first, and the rooms it is invited to |
| `room/read` | a member | `{thread_id, after?, seen?}` → `{room, entries, removed, waiting, more, rooms, invitations}` |

## A refusal

The service answers a refusal with a status and `{ok: false, code, error}`: 400
`bad_input`, 401 `unknown_key`, 403 `forbidden` or `not_member`, 404 `not_found`,
409 `held`. Through the kernel only the status survives, as `upstream returned status
N`, recorded as a failed transaction.

## What fits a reply

The kernel refuses a reply over 4 MiB. A post is at most 65,536 characters by its
schema, and a longer answer is kept as a failure. `room/read` holds at most 200 entries
and 1 MiB, says `more` when others remain past `after`, and holds the rooms of newest
activity that fit 256 KiB.

## Membership

A member is added, then invited once the kernel takes its invitation, then joined
once it accepts. The creator is joined; everyone `room/create` or `room/add` names
is invited by one task created during that command's dispatch, whose onward is
`accept` and whose partial args carry the room, the member, the room's name and its
creator. A member whose kernel cannot be reached stays added, is named in
`uninvited`, and is tried again at each post in the room, with the post's
capability, named in `undelivered` while it fails. Completing the task or running
`room/join` joins the member; leaving declines, and an invitation declined or
withdrawn has no member to join. `room/list` and `room/read` answer the caller's
invitations. Only joined members read, post, list the room, are delivered to and
are waited on. A store made before invitations has every member joined.

## How a post travels

1. A member runs `msg/post`. The kernel injects the member's chat key; chat
   resolves it, stores the entry, and applies the one-post-per-delivery rule.
2. While that call runs, chat creates one task per recipient through the
   capability the kernel injected: the member whose address is the post's
   first word (`critic@k, which is best?`), or every other member. The task
   carries what the room said since that member's previous delivery, this post
   last. The tasks of one dispatch are created at once, each within 3 s, so a
   member whose kernel does not answer never holds the post past the kernel's
   deadline; that member is reached by a later post.
3. The member completes the task. Its onward, `reply`, settles it: the answer
   replaces the member's own unaddressed posts since the delivery, adds nothing
   after a hand-off, and a failure shows only when the member said nothing. An
   agent that wants another member's attention posts with its address first; a
   chain of such hand-offs stops at the cap.

## Joining

The UI joins the principals of its installation itself, each with its own
session: the person signed in, on any chat page, and every agent with a home
under `$JUICE_HOME/agents/`, with the session in its credential file, when chat
signs in and when the agent is added to a room, which also accepts the agent's
invitations here. It takes the same path as a member elsewhere, through the
kernel alone, and no key is ever retired:

```
juice user approve chat@<kernel>
juice run chat@<kernel>/register '{"handle":"you@<kernel>"}'
juice task complete <task_id> '{}'
juice user connect chat@<kernel> --token <api_key>
```

A member on another kernel runs the same four commands on their own kernel,
which forwards the connect to this one (kernel v0.19.0 and later, on both
sides); then they read, post and run room commands as a member here does. Two
names must resolve first: their kernel calls this one `<kernel>` only once it
has a petname for it, bound by the first call made with this kernel's key
(`chat@<key>/index`), and this kernel must have a petname for theirs, bound by
`juice admin peer rename <key> <name>`, or the verification task for
`you@<their kernel>` cannot be created. A call coming in binds no petname.

## Upgrade

The store is `$JUICE_HOME/services/chat/rooms.db`, upgraded in place: the columns a
version adds are added on open, and a delivery held across the upgrade settles nothing
said before it. `serve` moves a store left at `ui/rooms.db` once, folding its log into
the database file first. When there is none and the previous application's `ui/chat.db`
exists, its live rooms, members and messages are imported once; keys are not, and the
UI joins members again. The old file is never written.
