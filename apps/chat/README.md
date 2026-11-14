# chat

The conversation surface of the UI: the rooms of `juice-chat` (`services/chat`), a
program of its own. This app declares that program to the process supervisor, which
runs it as it runs the kernel and the agents, and otherwise reaches it only by running
its actions through the kernel as the viewer. The rules are `requirements.md` §6.7; the seam it sits on is
`docs/apps.md`.

## Files

| File | Role |
|---|---|
| `chat.go` | The app: declaring the program, joining the installation's users, the workspace seam (the rail, the landing, a room's context), the browser routes, the heartbeat watcher. Every room read and command is a run of an action as the viewer. |
| `templates/` | `_rail.html` (the sidebar's conversations, New conversation), `room.html` (a room: the conversation and the action panel), `landing.html` (no conversation yet), `_entries.html` (an entry, whom the room waits on, the fragments). |

## The viewer's chats

A viewer's chats are read from the requesters whose handle is `chat` and the grants
they hold, from the request's one read of the viewer. This kernel's chat is joined once approved, which joining does; a chat on
another kernel is joined when it is approved and holds a grant of the viewer's, and
asks to be connected when it has addressed the viewer and is neither joined nor
blocked. Joining is approve, `join` and connect the key it returns; joining again
first has the kernel grant the key it kept, which keeps the member, and approves
the chat again. Connect is joining, and one that fails leaves the chat
asking; Disconnect deletes the grant and
returns the requester to pending; Block deletes the grant and blocks it. Nothing
else connects a chat elsewhere. A room is named by its address, `thread@<key>`, in
its URL, its watch and the rail; every read and command runs the action of that
room's chat, at home or elsewhere, by one path. Nothing is run on a chat elsewhere
that is not joined, and a run on one joined that the kernel refuses for want of a
grant joins again only while the viewer's kernel still lists that grant, as after
an upgrade of chat revokes the grants on its contracts; a grant the viewer
removed, as in Settings, is never restored. A chat is identified by its kernel's
key, `chat@<key>`, in room addresses, URLs, the rail and every comparison, and
shown by its kernel's label (`docs/ui-grammar.md` "Naming"): its petname, else
`~name`, else its key shortened, a row naming a kernel no petname names copying
the chat's address from its menu; naming a kernel changes only how its chat is
shown.

Chat names members as its kernel names any party, `{address, principal,
nickname?}`, and a room's creator by principal. That spelling is chat's data:
the page names every party of chat's replies as the viewer's kernel names its
principal (`Chat.name`, by `sys/resolve`, kept ten minutes), shows each with the
one party partial, and addresses a member in a post by chat's spelling, which
chat routes by. The viewer's own rooms are those whose creator is the viewer's
principal.

A member added to a room is invited. The invitations are chat's own list, read
with the rooms; each is a rail row whose menu holds Accept, which runs `room/join`,
clears the invitation's task and opens the room, and Decline, which runs
`room/leave` and declines the task. A failure destroys nothing: an invitation
counts as withdrawn only when its chat says so, at home by its status, elsewhere by
a list that answers without it. The UI seats the installation's agents where it
adds them: joins them and accepts with their own session.

The rail is single rows. A chat asking, an invitation and a chat that could not be
read are each one button opening its menu; a command that may wait on another
kernel dims its row until it answers, and its failure is said on the page it was
sent from.

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
