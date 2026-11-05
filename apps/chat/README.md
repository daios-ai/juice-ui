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

## When a page runs a read

A run writes a transaction, so the heartbeat never runs an action to find out what
changed. It compares the viewer's open chat tasks, a kernel read that writes nothing,
and runs `room/read` or `room/list` once when they moved. A read that fails
acknowledges nothing and is retried after 5, 10, 20, 40 and then every 60 seconds.
A room command sent from the page's menu or dialog is one run and then one such read,
whatever the watch says is due, and the page answers in place.

## Landing

`/chat` is the viewer's conversation with `assistant@<kernel>`, the agent the
agents app makes and runs; the rooms are the sidebar's rail.
