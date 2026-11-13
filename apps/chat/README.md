# chat

The conversation surface of the UI: the rooms of `juice-chat` (`services/chat`), a
program of its own. This app declares that program to the process supervisor, which
runs it as it runs the kernel and the agents, and otherwise reaches it only by running
its actions through the kernel as the viewer. The rules are `requirements.md` §6.7; the seam it sits on is
`docs/apps.md`.

## Files

| File | Role |
|---|---|
| `chat.go` | The app: declaring the program, joining the installation's principals, the workspace seam (the rail, the landing, a room's context), the browser routes, the heartbeat watcher. Every room read and command is a run of an action as the viewer. |
| `templates/` | `_rail.html` (the sidebar's conversations, New conversation), `room.html` (a room: the conversation and the action panel), `landing.html` (no conversation yet), `_entries.html` (an entry, whom the room waits on, the fragments). |

## The viewer's chats

A viewer's chats are read from the requesters whose handle is `chat` and the grants
they hold. This kernel's chat is joined once approved, which joining does; a chat on
another kernel is joined when it is approved and holds a grant of the viewer's, and
asks to be connected when it has addressed the viewer and is neither joined nor
blocked. Connect is the four steps of joining; Disconnect deletes the grant and
returns the requester to pending; Block deletes the grant and blocks it. Nothing
else connects a chat elsewhere. A room is named by its address, `thread@kernel`, in
its URL, its watch and the rail; every read and command runs the action of that
room's chat, at home or elsewhere, by one path. Nothing is run on a chat elsewhere
that is not joined, and a run on one joined that the kernel refuses for want of a
grant joins again, since an upgrade of chat revokes the grants on its contracts.

A member added to a room is invited: the invitation is a task whose partial args
carry `from`, shown as a card on the rail with Accept, which completes it and opens
the room, and Decline, which leaves the room through chat and declines the task.
The installation's own agents accept theirs when the UI joins them.

## When a page runs a read

A run writes a transaction, so the heartbeat never runs an action to find out what
changed. It compares the viewer's chats, their open deliveries and their open
processes, kernel reads that write nothing, and runs `room/read` or `room/list`
once when they moved. A read that fails
acknowledges nothing and is retried after 5, 10, 20, 40 and then every 60 seconds.
A room gone, or held by a chat no longer among the viewer's, sends the page to the
landing.
A room command sent from the page's menu or dialog is one run and then one such read,
whatever the watch says is due, and the page answers in place.

## Landing

`/chat` is the viewer's conversation with `assistant@<kernel>`, the agent the
agents app makes and runs; the rooms are the sidebar's rail.
