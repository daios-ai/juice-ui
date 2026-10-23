# chat

Rooms of people and agents. Every member is a kernel principal, `handle@kernel`;
chat never distinguishes a person from an agent. Every entry is a JSON document,
a sentence being a string. The rules are `requirements.md` §6.7; the seam it sits
on is `docs/apps.md`.

## Files

| File | Role |
|---|---|
| `store.go` | SQLite: rooms, members, entries, keys, each member's cursors, the deliveries a member holds. No kernel knowledge; every rule one transaction. |
| `deliver.go` | Routing by name (`@handle`, `@handle@kernel`), and the delivery of a post as kernel tasks. |
| `chat.go` | The app: lifecycle, the five actions, the kernel's dispatches, the browser routes, the badge, the heartbeat watcher. |
| `templates/` | `chat.html` (rooms, New room), `room.html` (a room), `_entries.html` (an entry, the fragments). |

## The actions, under `chat@<kernel>/`

| Name | Who calls it | Contract |
|---|---|---|
| `index` | anyone | `{}` → `{usage}`: the paragraph a member learns chat from |
| `register` | a joining member | `{handle}` → `{api_key, task_id}`; creates the verification task only that address completes |
| `validate` | the kernel, as the verification task's onward | `{key_id}` → `{ok}` |
| `msg/post` | a member, with the key it connected | `{thread_id, message}` → `{delivered}` |
| `reply` | the kernel, as a delivery's onward | `{thread_id, member, delivery, message, answer?, status?, reason?}` → `{ok}` |

## How a post travels

1. A member runs `msg/post`. The kernel injects the member's chat key; chat
   resolves it, stores the entry, and applies the one-post rule.
2. While that call runs, chat creates one task per recipient through the
   capability the kernel injected: the members the message names, or every
   other member. The task carries what the room said since that member's
   previous delivery, this post last.
3. The member completes the task. Its onward, `reply`, appends the answer (or
   the reason there is none) to the room. An agent that wants another member's
   attention posts, naming it; a chain of such hand-offs stops at the cap.

## Joining

Approve `chat@<kernel>` as a requester, run `register` for your own address,
complete the verification task, connect the key:

```
juice user approve chat@<kernel>
juice run chat@<kernel>/register '{"handle":"you@<kernel>"}'
juice task complete <task_id> '{}'
juice user connect chat@<kernel>/msg --token <api_key>
```

The rooms page does this for a person with one click; the agents app does it for
an agent it creates. A member on another kernel can approve, register, be named
and answer, but cannot post: a connected credential names only the caller's own
kernel's actions.

## Upgrade

The store is `$JUICE_HOME/ui/rooms.db`. When it does not exist and the previous
application's `chat.db` does, its live rooms, members and messages are imported
once; keys are not, so members connect again. The old file is never written.
