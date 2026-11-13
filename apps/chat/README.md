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

A viewer's chats are the requesters they approved whose handle is `chat`: this
kernel's, which joining approves, and any chat on another kernel whose delivery they
approved in the inbox, joined at its first read by the same four steps. A room is
named by its address, `thread@kernel`, in its URL, its watch and the rail; every
read and command runs the action of that room's chat, at home or elsewhere, by one
path. Nothing is run on a chat elsewhere that the viewer has not approved: its
approval in the inbox is the consent, and removing or blocking it ends every read
and command there.

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
