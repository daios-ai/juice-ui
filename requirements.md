# Juice Web UI Requirements

Version: 0.61.0
Status: design draft — endpoint surface gated against the running kernel

> **What changed in 0.61.0.** The kernel's language models are chosen in one
> form: a provider, its key and a model, for the chat model behind chat, JSON
> and decide, and for the search model. A new machine chooses them in the
> installer's Models step, before the first boot; afterwards the operator's
> Models page changes them, and the configuration page no longer shows them
> (§1, §6.6). Installing a component runs its setup, so its schemas are
> written (§1).

> **What changed in 0.60.0.** A form's default action, the button Enter presses,
> is solid ink, as in the established design systems: the submit of every form
> that has something to fill in or offers a second button (Next and Install in
> the installer, Save, Create, Sign in, Search, a task's Complete). Every other
> button stays outlined, and money and danger keep their colours (§7, grammar
> §5.1).

> **What changed in 0.59.0.** One rule decides a launch: an installation that is
> complete opens the UI, and one that is not runs the installer, which shows only
> the steps the machine needs. Starting the kernel belongs to every launch and
> has no screen: the page is held while it comes up, and only a kernel that is
> slow or fails is said. The installer's Components step is Download, shown only
> when something is missing; Install only creates the kernel; the Done page and
> the start screen of 0.58.4 are gone (§1).

> **What changed in 0.58.4.** Every start showed the installer, although only a
> machine with no kernel installs anything. The installer's frame now appears
> only while a kernel is being created; starting a kernel that exists, or
> awaiting one elsewhere, shows a plain start screen with the job in hand (§1).

> **What changed in 0.58.3.** In the installer, Back stands at the left of the
> commands and the step's command at the right.

> **What changed in 0.58.2.** The installer's proportions follow one system
> (grammar §5.14): one frame for every step, with a head, numbered steps at a
> fixed width and three slots per step (heading, content, commands at the foot),
> every measure taken from the spacing scale. The card keeps its size from step
> to step, and a phone shows "Step 3 of 7" in place of the list. A banner's
> sentence wraps beside its icon.

> **What changed in 0.58.1.** An installation whose `kernels/` could not be read
> counted as fresh, so an older binary could be replaced under a kernel nobody
> could see; what cannot be read is now an error, and nothing is replaced. A
> failure to make the recovery phrase ended the installer and handed the
> listener to the application with no kernel; the installer now stays on its
> page with the reason (§1).

> **What changed in 0.58.0.** The first start is an installer: a sequence of
> pages and jobs in the shape OS installers use, in its own package, `installer`
> (§1). It asks for the world, then the kernel's name and password, then shows
> the recovery phrase, then a summary, and only then does the work, as jobs with
> progress; the page no longer freezes while the kernel is created. Nothing
> present is replaced under a kernel that exists: 0.57.0 replaced an older kernel
> binary under an existing polygon kernel, which the newer kernel then refused to
> serve. The kernel gate, its screen and its form are gone.

> **What changed in 0.57.1.** A booted kernel the supervisor had not yet been
> given, one made before the supervisor or at the command line, kept an older
> agent: the round that declared it read the kernel as declared and replaced
> nothing. Whether the kernel was declared is now read before that round
> declares it (§1).

> **What changed in 0.57.0.** Juice-UI installs with one command, `install.sh`,
> which puts its three programs in `$JUICE_HOME/bin` from one verified release
> tarball. The rest of Juice is installed by the UI on its first start, from one
> list of components: the kernel, as before, and now the agent, so a fresh
> installation's assistant starts without anything installed by hand. Both are
> installed by one routine, under one rule, and the screen names each (§1).

> **What changed in 0.56.1.** The configuration page withheld the keys of
> `native.llm.endpoints`, which the kernel replaced with `native.llm.models` in
> v0.17.0, so a model's key was shown and written back as typed. The page now
> withholds each model's key and keeps it by the model's name (§6.6). The kernel
> release the gate installs is v0.18.1, where it was v0.16.0, whose file has the
> old list.

> **What changed in 0.56.0.** The standard size is `1rem`, 16px at the browser's
> default, where it was 14px, so body text and every control read larger, and a text
> field no longer makes an iPhone zoom. The other sizes rise with it. One line in
> `tokens.css` defines `1rem`. A schema's `url` and `date` fields now take the shared
> field style, where they had the browser's default.

> **What changed in 0.55.0.** Amounts showed every decimal the world declares. An amount
> is now rounded where it is read: two decimals at one unit or more, two significant
> digits below it, with the exact amount in its title. It stays exact where a control
> commits it, where a result or a transaction states what was charged, and in every
> input. The exact form is still the kernel's own.

> **What changed in 0.54.0.** 0.53.0's pagers cut the page after reading the whole
> list, so `sys`'s transactions took 92 kernel calls and 74 seconds to show 20 rows. A
> page now reads one page from the kernel. A comment had claimed the kernel pages before
> it filters by visibility, which it does not, and a contract test now holds that.
> Workshop's earnings figures, its Earned column and its last-failure peek are removed:
> the kernel has no figure for them, and adding them up meant reading every transaction
> on every load. They can return when the kernel offers a total.
>
> **What changed in 0.53.0.** `room/direct` is gone: the landing finds the room of
> exactly the viewer and the assistant among the viewer's rooms, and starts it with
> `room/create` on the first visit. A change of members refused comes back in the open
> dialog with the reason and the address typed, and one that succeeds returns to the
> dialog. Starting a conversation refused keeps its dialog open with the reason and
> what was chosen and typed, on the page it was opened from. The inbox, a user's actions,
> a wallet's withdrawals, a session's transactions and Network's money lists now page
> like every other long list. Long text no longer overflows at phone width on Network, an
> action's page and the configuration page.
>
> **What changed in 0.52.0.** 0.51.0 gave a room two menus that differed, one in its
> header and one in the sidebar. A room now has one menu, its row in the sidebar:
> Members, Rename and Delete for its creator, Members and Leave for any other member.
> The header is the room's name.
>
> **What changed in 0.51.0.** A room's header held its name and four controls in one
> row, so on a phone the name collapsed and overprinted them. The header is now the
> name, on one line, and one menu: Members, Rename and Delete for its creator,
> Members and Leave for any other member. Members opens one dialog, holding the list
> and, for the creator, adding and removing.
>
> **What changed in 0.50.0.** Many actions read as text: a grey word with no border
> that underlined on hover, which a phone cannot do. An action is now always a
> button, and a link only goes somewhere to read. The text button is gone from the
> grammar. A row's menu and the copy control are icon buttons with icons from the
> sprite, and an item that leads to a question or a dialog ends in `…`.
>
> **What changed in 0.49.0.** A room could be renamed, deleted or left only from
> the header of the open room, and each command reloaded the page. Every room in
> the sidebar now has a menu with Rename and Delete for its creator and Leave for
> any other member. A command is one run and one read, and the page answers in
> place: the sidebar shows the change, and nothing reloads unless the room deleted
> or left was the one open. Renaming opens one dialog, on any page.
>
> **What changed in 0.48.0.** The UI started the kernel, the agents and chat in
> three different ways, left them running when it stopped, and never restarted one
> that died. One process supervisor now runs them all. A program is one file in
> `$JUICE_HOME/supervisor/programs/`, and an operator adds a program by writing one.
> The supervisor starts each program as its child, restarts it when it dies, and
> stops it when asked. Stopping the UI stops the kernel, the agents and chat, and
> starting it starts them. An agent a person stopped stays stopped; every other
> agent starts with the UI. A copy of a program started elsewhere is stopped and
> replaced. On a machine with no UI, `juice-supervisor` runs the same programs from
> the command line. One installation has one supervisor, so two UIs need two
> installations. Logs are in `supervisor/log/`, at most 20 MiB per program. If the UI
> crashes its programs go down with it, at their next line of output or at the next
> start. Stopping the UI while an agent is answering takes up to 30 seconds, and the
> agent answers after the next start, calling its model again.

> **What changed in 0.47.0.** In 0.46.0 chat ran inside the UI's process, the UI
> registered chat's actions and held its account, and the UI read rooms by calling
> the chat server directly with the member's key. Chat could therefore not be moved
> out of this repository, and an agent or a member on another kernel could not list
> rooms or read one. Chat is now a program of its own, `juice-chat`, with its home
> in `$JUICE_HOME/services/chat/`. It registers its own actions with `juice-chat
> register` and serves them with `juice-chat serve`. The UI starts it, as it starts
> the kernel and the agents, and reaches it only by running its actions through the
> kernel. Listing rooms and reading a room are two more actions, `room/list` and
> `room/read`, which any member can run. A page no longer reads chat on every
> heartbeat. It asks the kernel for the viewer's open chat tasks, and runs a read
> only when they changed or the person acted, so an idle page writes no
> transaction. A change that reaches the viewer through no task (a post addressed
> to another member and its answer, a rename or a membership change by someone else)
> now appears at the viewer's next read, no longer within five seconds. A post and
> an answer are limited to 65,536 characters and a read to what fits the kernel's
> reply cap. A person already joined signs in without a new chat key. The store
> moves from `ui/rooms.db` to `services/chat/rooms.db`. The UI no longer has a
> kernel-facing `/_kernel/` route.

> **What changed in 0.46.0.** Chat's room operations were browser routes that
> wrote chat's store as the signed-in person, and its room reads were direct
> store reads. An agent could therefore post and answer, but could not create a
> room, add or remove a member, leave, delete or rename one, and the chat service
> could not be moved out of this repository. Every room command is now an action
> chat owns (`room/create`, `room/direct`, `room/rename`, `room/add`,
> `room/remove`, `room/leave`, `room/delete`), and the pages run them as the
> viewer, so a person and an agent take one path. A room is read from two
> endpoints of the chat service with the member's chat key, because a read is not
> an execution and must not fill the member's transaction history. The UI joins a
> member the way a member elsewhere joins: it approves chat, runs `register`,
> completes the verification task and connects the key, all through the kernel.
> The UI no longer touches chat's store, and a test holds that boundary. A room's
> creator can rename it. One grant at `chat@<kernel>` covers every action a member
> runs, and a person who holds only the earlier `msg/post` grant is joined again
> on the next chat page. The kernel relays only the status of an action's refusal,
> never its body, so the UI chooses its sentence by that status; a held post
> previously fell through to an error page and now reads as itself (§6.7).

> **What changed in 0.45.0.** The launcher's actions sat under an `Actions`
> caption although their address already marks them; it is gone. Discover's
> box lost its suggestions when the launcher took them over; the launcher's
> list is now one component beside a text input, composed into the composer
> and into Discover's box, which suggests the catalog's actions (§6.2).

> **What changed in 0.44.1.** A string past 800 characters folded behind an
> excerpt, a widget no form has, and so did a long chat reply; a string's box
> now holds it whole, as the form's textarea does (§7).

> **What changed in 0.44.0.** A chat message is text by the post schema, so a
> JSON document posted by a person or an agent showed as one string box and
> its JSON as an escaped string. A string holding a JSON object or array is
> now viewed as that document; the JSON behind the switch stays the value as
> stored (§7, §8.1). A result and args are objects at the top level by the
> kernel's schema rule, so in practice the rule meets chat messages.

> **What changed in 0.43.1.** The avatar menu's entry for `/browse` reads
> **Discover**, the name the rest of this document gives the page (§6).

> **What changed in 0.43.0.** The panel floated as a card in the page's
> column and took its full width first, leaving the conversation a sliver on
> a laptop; it is now a pane of the window, full height at its right edge,
> beside the conversation while the conversation keeps a readable column and
> over it otherwise, whatever the sidebar's state. The waiting line's
> ellipsis was too faint to see; the sentence now stands at body size with
> three dots pulsing beside it. The one-shot notice was a band of its own
> above the column; it is now a banner of its kind heading the column, which
> × dismisses (§6.7).

> **What changed in 0.42.2.** A sign-out revoked the pair it had read before
> its call to the kernel, so a refresh that stored a new pair during that call
> left the new one live; the sign-out now takes the pair as it ends the
> session, and the refresh, finding no session, revokes its own. Expired
> entries of the session store are removed every minute rather than at the
> next sign-in, so their tokens and recovery phrase leave memory with the
> session (§5).

> **What changed in 0.42.1.** A request saves its own copy of the session, so
> one that finished after another had rotated the kernel tokens wrote the old
> pair back and the next refresh signed the person out; a refresh that
> finished after a sign-out brought the session back and left its new token
> live. The token pair, the last activity and the unacknowledged recovery
> phrase now live in one server-side store that no request's copy can
> overwrite, which only sign-in and sign-up create, and a pair issued to a
> session already ended is revoked (§5).

> **What changed in 0.42.0.** A session ends on the person's inactivity, not
> the page's. The heartbeat counted as activity, so an open tab kept a session
> alive for its whole 24-hour lifetime. A session now ends 30 minutes after
> the last request the person made and 12 hours after login; a minute before
> the idle end the page asks "Stay signed in?"; the login page says why the
> session ended; and every sign-out leaves through one exit that revokes the
> refresh token at the kernel that issued it, and at no other (§5, §8). A
> heartbeat the page abandoned by navigating no longer reads as the kernel
> gone, which had signed people out at random.

> **What changed in 0.41.0.** Fill from chat is a tool of the form: an icon in
> its top row beside the JSON switch, the money button naming the amount when
> the planner is priced, and while the planner works its bar runs there and
> the form waits. What other kernels report reads as the stats line does: one
> table, one report per row in the line's own cells, the source last and the
> provider's first, the kernel's further counts in the cells' titles. In the
> panel the run form begins a section of its own. A room says whom it is
> waiting on, which is what it knows, and the line breathes while the page
> watches; reduced motion now stills pseudo-elements too, the progress bar's
> included (§6.2, §6.6, §6.7).

> **What changed in 0.40.0.** The launcher searches the economy without
> leaving the conversation (docs/conversational-search.md). A known action is
> still chosen from the free catalog as one types; Enter, or the top row naming
> the lookup's price, submits the query to `sys/lookup` pinned to that quote,
> and the results open in the panel, built from Discover's own. An action
> opened from them is shown over them, and ← Results gives them back, in their
> order and at their scroll position, without searching again. A successful
> run from a room becomes the composer's draft and the panel closes; Use
> result, and the result it carried into the next action, are gone (§6.1,
> §6.2, §6.7).

> **What changed in 0.39.0.** Sizes are one system: the type, spacing, icon
> and avatar scales and one control height are tokens, and the stylesheet
> writes no size of its own, so a button, an input and an icon button side by
> side are one height. The sidebar has two states per width, as in the
> established chat clients: on a wide screen docked or a strip that keeps the
> toggle, the new-conversation opener and the avatar; on a narrow screen a
> drawer, never stored, sized to the visible screen so its avatar is never
> under the browser's toolbar (§6.1, §7).

> **What changed in 0.38.0.** The composer is a chat bar: one box with `/`,
> `@` and a send arrow around the post form's own message control, the
> available balance beneath (§6.7). A JSON switch and a list's Add and Remove
> work on every value however it reached the page — a heartbeat's entry
> included — being handled for the whole document rather than wired element
> by element.

> **What changed in 0.37.0.** The UI is one collaborative workspace. The
> sidebar is the conversations and the avatar, whose menu holds every
> destination of one registry; the search pill, the nav groups in the sidebar
> and the rooms page are gone. The composer is the common entry: text, an
> address first, and `/` for an action or a page. An action opens in a panel
> beside the conversation, built from the action page's own parts; its
> arguments can be prepared from the conversation (Fill from chat), and its
> result carried into the next message or action (Use result). A room says who
> is answering, and a conversation starts from its participants. The kernel's
> planner is offered at its price, on the money button when priced, rather
> than hidden (§6.1, §6.2, §6.7).

> **What changed in 0.36.0.** Chat's input and output go through the UI's one
> form and one view. A message is a string, declared so in the post action's
> schema; the composer is that action's form, the room sealed above it as a
> task's sealed arguments are, and its JSON switch; a refusal keeps the draft.
> Every entry is the view of its value. A task form's JSON editor names only
> the fields still to fill (§6.3).

> **What changed in 0.35.0.** Chat provisions what it needs. The UI joins the
> person signed in and every agent with a home on this installation itself,
> with that principal's own session, so nobody connects and no agent is joined
> by hand; the Connect gesture is gone. The agents app makes and runs the
> assistant, and a session lands in its conversation with it (`/chat`); the
> rooms are at `/chat/rooms`. A member is addressed by its address as the first
> word of a post, never by `@handle`. A completion settles what its member said:
> it replaces the member's unaddressed posts since the delivery, adds nothing
> after a hand-off, and a failure shows only when the member said nothing; an
> entry's kind is stored, and a failure reads as one. Room ids are words. The
> measurements behind each rule are in §6.7.

> **What changed in 0.34.0.** Chat is the front door: a session lands on it
> and its entry leads the nav (§6.1). The chat app is rewritten (§6.7). A room
> holds people and agents, every member a kernel principal; an entry is a JSON
> document, text being a string; a post is chat's own action run as the
> member, and while it runs chat creates one kernel task per recipient — the
> members the message names with `@`, or every other member — carrying what
> the room said since that member's previous delivery; completing the task is
> the answer, appended to the room by its onward. One post per held delivery,
> and a cap on hand-offs, keep agents from talking forever. Rooms are the
> UI's; joining is the documented path, which the agents app walks for an
> agent it creates. The seed-trace doorbells, the daemon split and the text
> body are gone, with the two chat items of §10; the previous rooms are
> imported once.

> **What changed in 0.33.0.** An agent's `config.json` is edited as the form
> of the daemon's own JSON Schema of it, which juice-agent 0.7.0 writes to
> `$JUICE_HOME/schemas/juice-agent.schema.json` at every start, with the JSON
> switch and a save, as the kernel's configuration page edits its file (§6.1,
> §6.6). One editor serves both files: the kernel's with its secrets withheld
> and its restart, the agent's with `$schema` kept as saved and a write the
> daemon reads at its next start, refused when the file changed since the page
> was loaded. The raw `config.json` textarea is gone.

> **What changed in 0.32.1.** A demanded row holding nothing is empty, so a
> required array whose item demands no field opens under `minItems` until its
> row holds a value; submit drops a blank row, which `minItems` then misses
> (§6.2).

> **What changed in 0.32.0.** The **view** is the name of the read-only
> rendering of a JSON value, until now the frozen form (§7). A named nested
> node of the form and of the view folds behind its name, as a `details`:
> closed in the view, where its summary names its size; closed in the form
> unless a field it demands is still empty, a required key counting once its
> object is sent and a row once it is under the array's `minItems` (§6.2);
> the root and an array's elements never fold. The fold is presentation: the
> args are rebuilt from the form alone, folded or not, and the JSON switch
> reads every field. A field inside a list's element keeps its spacing.

> **What changed in 0.31.1.** The agent's heartbeat token names the exit
> line it shows as well as its state, so a start that fails between two
> ticks is reported; Delete holds the home's `serve.lock` through the
> removal, so a daemon started from a terminal meanwhile is refused by its
> own rule and none is left holding a deleted lock (§6.1).

> **What changed in 0.31.0.** One heartbeat per page. A signed-in page makes
> one request per lane — every 5 s, and every badge interval — carrying a
> hidden watch for each widget that waits for something (the task and app
> badges, an agent's status, a conversation's tail, a parked process, a
> peer's card), naming what it shows; the reply swaps only the widgets whose
> truth differs, so an unchanged control is never replaced under a click.
> The six pollers each widget kept, their fragment routes and the
> kernel-gone sign-out of the browser heartbeat fold into it (§6.1). Start
> and Stop carry no banner: the status block is the statement. The kernel
> gate's own poll is the one exception, since it runs before a session
> exists.

> **What changed in 0.30.1.** The kernel record the agents app keeps is keyed
> by the kernel's key alone, as `juice kernel add` keys it: a record with the
> same key is the same kernel and is refreshed from `/health`, so a record an
> older client wrote with fewer fields no longer reads as another kernel.

> **What changed in 0.30.0.** An agent is running when its daemon says so
> (§6.1): juice-agent 0.6.0 holds `serve.lock` in the agent's home for as long
> as it serves, as a kernel holds its own, so an agent served from a terminal
> is seen, stopped and protected like one the UI started, and Stop signals
> the process in the lock. Start is one button: one click while the login has
> a session, a password dialog when it has none; Sign in is gone. A daemon
> outlives the UI, as the kernel does, and the next UI finds it through its
> lock. The agent page shows the account and no longer scans the kernel's
> whole transaction history for ten rows.

> **What changed in 0.29.0.** The agents app makes an agent's session (§5,
> §6.1). New agent takes the handle and a kernel password: it creates the
> account when the kernel has none, signs the login in as `juice auth login`
> would — the kernel record when absent or moved, the credential file under
> its lock — makes the home and starts the daemon; the password is kept
> nowhere. Sign in, on a stopped agent, replaces the session alone, so an
> agent made at the command line, or one whose session died, is served from
> the page. The session, the home and the credential change only while no
> daemon of that login is alive: a daemon draining after Stop is still
> running, and Sign in and Delete are refused until it has exited.

> **What changed in 0.28.0.** The agents app follows juice-agent 0.5.0 (§6.1):
> an agent's home is `$JUICE_HOME/agents/<login>/`, named by its login
> `handle@kernel`, and the app edits its `agent.md` and optional `config.json`.
> The daemon runs as `juice-agent serve <login>` on the session the operator
> made with `juice auth login`, so Start asks for no password, and a stopped
> agent shows its daemon's exit error from the current run. The UI stops
> gracefully on SIGTERM, stopping every app, so the daemons it started end
> with it rather than running unsupervised. The open item on the agent's home
> path is closed (§10).

> **What changed in 0.27.0.** The first real payout on Polygon, made by the
> operator, showed what the money pages left unsaid. An entry from an account
> to itself — the operator's, `sys` being the clearing account through which
> rail money is booked — moves nothing and now takes no sign; money crossing in
> or out of Juice names that crossing instead of an em dash (§6.5). A blocked
> payout states the kernel's reason under its status; the operator's locked
> balance, which also holds payouts in transit, is labelled `locked` without the
> escrow signal on every page that shows it (§6.5). The wallet and the dashboard
> list the viewer's own sessions, and the one End control states a refund only
> when there is one. The network page states what the vault held at the last
> finalized block — the token and the native coin its payouts spend in fees —
> and its halt banner is a warning led by `#alert`, spaced from the figures
> (§6.6).

> **What changed in 0.26.1.** The client decodes only what the UI reads, and
> a contract test holds it against the kernel this installation runs (§9):
> a field the kernel sends with another type, or no longer sends, fails
> `make check` rather than a page. It was prompted by a chain kernel serving
> `finalized.token` as a number, which failed the whole admin identity over a
> field nothing displayed.

> **What changed in 0.26.0.** The wallet connects the person's own crypto
> wallet (§6.5): it registers the address withdrawals go to by signing the
> kernel's registration message, deposits by asking that wallet to send the
> token to the kernel's deposit address, and withdraws to it. One box,
> *Deposits and withdrawals*, says in plain words whose each address is and
> warns against paying from an exchange. Deposit and Withdraw are symmetric:
> each opens a dialog stating From and To, and its review's
> confirm is the money button naming the amount (§7). The UI proves the
> world's chain by recomputing its network fingerprint, builds the transfer
> from a fresh read of the registered address, and states only what the wallet
> or the kernel established. The code that moves money lives in one package,
> `wallet/`, and a drift test confines the two kernel mutations to it (§2).

> **What changed in 0.25.0.** The UI follows kernel v0.16.0. A person decides
> who may address tasks to them: the inbox lists every requester, pending ones
> first, to approve, revoke or block, and a pending requester counts in the
> attention badge, its tasks being withheld until approved (§6.1, §6.3). A card
> names the task's requester, the party it acts for. Answering a task spends
> nothing of the person who answers it, the requester's funds being reserved
> when the task is made, so every completion is the neutral `Complete` and the
> payment-task branch is gone (§6.3, §8). The language-model endpoints and
> their models are edited on the configuration page, and an installation
> upgraded from an older kernel edits them as a list replacing the kernel's
> defaults whole (§6.6). A required select left untouched is blank, so a row
> nobody filled is dropped rather than sent with its first option, and a
> required list or object is sent even empty, so the configuration's
> `"models": []` survives a save (§6.2). The JSON switch converts what is on
> screen into the other view, exactly or not at all (§6.2, §6.6).

> **What changed in 0.24.0.** The UI follows kernel v0.15.13, whose schema
> subset has one string per value: a map (`additionalProperties` as a schema)
> is refused, `{}` and a bare object are leaves, and an optional property's
> empty value is not its absence. The run form loses its map-shaped node; a
> node the schema leaves open is a JSON editor sent as the JSON it holds
> (§6.2). The configuration's endpoints are a list, each endpoint's key a
> write-only input in its own row, kept by the endpoint's name (§6.6).

> **What changed in 0.23.0.** One **copy control** (§7): wherever a key, a
> hash, an address or the recovery phrase is named, an icon-only button
> carries the whole value to the clipboard, because every command that takes
> one takes it whole and the screen shows what a person can match by eye. A
> record id gets none, since the kernel resolves a prefix. The phrase page's
> text button migrates to it, and "Copied" is announced through the live
> region.

> **What changed in 0.22.0.** Seven restatements that had stopped tracking
> their owner are removed or corrected, each fact left in one place: amounts
> render in the kernel's form (§7, not six digits); visibility is set by
> `PUT /v1/actions {target, …}` (§6.4); a discovered action resolves on open
> (§6.2 alone); `peer_unreachable` names its peer from `meta.peer` (§10);
> the catalog filter is `?owner=` (§6.2); `sys/random` and `sys/llm/embed`
> are kernel natives, no longer open items (§10); and no normative sentence
> or sketch carries `$` (§6.2).

> **What changed in 0.21.0.** The UI follows kernel v0.15.11, where a peer
> holds no account: a counterparty is a kernel a transaction here names
> (`traded`), suspension freezes the kernel row and is offered on every
> kernel's page, and the page states when the kernel became known here
> (`first_seen`). The account vocabulary leaves §6.6.

> **What changed in 0.20.0.** A kernel row's facts are a strip in the action
> strip's grammar (§6.6): catalog, catalog verified, last contact failed, with
> a tooltip per slot; the inspect page's strip carries the live probe and the
> provider's pooled report instead. The key renders in one spelling, the pill
> reads `this kernel`, a row titled by its key carries the one `name` opener,
> and the naming form opens prefilled by the kernel's own auto-bind rule. The
> kernel's page states its facts with labels, has buttons for its verbs, and
> lists its public actions as discovered rows.

> **What changed in 0.19.0.** The network page reads like the search results
> (§6.6): one list in the action row's grammar, this kernel first, then its
> counterparties, then the known network, marked `current` and `party`. The
> tabs, the identity card and the row verbs go: this kernel's money renders
> through the figures, and a peer is named and suspended on its own page.

> **What changed in 0.18.1.** The first boot can never touch an existing
> kernel (§1): a home holding `juice.db` is a kernel, started and never created
> again; the first boot writes only where none lives, deletes nothing, runs one
> at a time, and answers with a redirect, so a re-sent form boots nothing.

> **What changed in 0.18.0.** The superuser changes this installation's kernel
> configuration from Admin (§6.6): a form generated from the kernel's schema of
> `config.json`; a save rewrites the file and restarts the kernel by the process
> id in its `serve.lock` (kernel v0.15.10), and the previous file returns if the
> kernel refuses the new one.

> **What changed in 0.17.2.** The first boot offers every world the
> installation can serve, as `juice kernel worlds` lists them (kernel v0.15.8),
> the shipped ones included before any kernel has served, each by its name;
> a read-only Superuser name, `sys`, stands above its password; the phrase's
> acknowledgement counts only while one is pending, as at signup.

> **What changed in 0.17.1.** The first boot follows the standard for a
> recovery phrase: the UI generates it, as at signup, shows it once and passes
> the kernel only its public key (`JUICE_BOOTSTRAP_RECOVERY_KEY`, kernel
> v0.15.7); the kernel prints none. The world is chosen from the installation's
> world files, a binary older than the verified release is replaced, and the
> screen, headed Juice installation, states each field's consequence.

> **What changed in 0.17.0.** The UI is the way a person connects to Juice:
> before the application serves, the **kernel gate** (§1) installs the kernel
> binary if it is missing, creates this installation's kernel if none exists,
> starts it if it is not running, and waits for a kernel elsewhere — each step
> decided by a fact the UI observes, with no mode setting. The listener answers
> from the first moment with the kernel screen and the application takes it
> over when the kernel answers.

> **What changed in 0.16.0.** The command palette, its ⌘K chip and the
> Ctrl/Cmd+K binding are gone. The commands are rows of the search pill's hint
> list (§6.1): listed first, marked `›`, at most three when titles also match,
> twelve rows in all; a leading `>` lists commands only. A command is a link or
> a submit button bound to the page's one command form. The registry stays
> inert, server-built JSON, admin verbs for `sys` only.

> **What changed in 0.15.0.** One **view** (then the frozen form) renders every JSON value a
> person reads — a run or task result, a receipt's args and result, a task's
> sealed `partial_args`, the receipt-verification checks (§7): the run form's
> own anatomy, read-only, so a result looks like the form that produced it;
> nothing interpreted, the raw JSON behind the same JSON toggle the form
> carries, a Download on a result. The outcome panel states the money fact alone; a success is its
> styling and its result. The run form renders the input schema at every depth: a nested
> object is a group, an array its rows with Add and Remove, a map-shaped node a
> JSON editor; fields are named by JSON Pointer and the args are rebuilt from
> the form alone (§6.2).

> **What changed in 0.14.0.** The kernel (034426e) serves one catalog:
> `GET /v1/actions` holds the caller's own actions in every state, the live
> ones it may call, and the actions discovered on peers (`discovered: true`,
> no local id), title-ordered and paged after scoping; `?all=` and `?name=` are
> gone (`docs/kernel-v0.20-verification.md`). The browse catalog renders a
> discovered row in its discovered form (§6.2) and the workshop leaves it out
> (§6.4). Search hints load the whole catalog once and match it in the browser,
> discovered actions included, and the search fallback matches the catalog by
> the same rules (§6.2).

> **What changed in 0.13.0.** Home is a **dashboard**: balances, the tasks
> waiting for you, open sessions and recent ledger activity, each linking to
> its full page (§6.1). Browse leaves the navigation and stays as the results
> page a search lands on, its empty query the catalog. Actions are listed as
> **rows in one ranked column**, not a grid; a row leads with a square tile
> cropped from the same art, and the 16:9 slot remains on the action page
> only (§6.2). The peer roster and the agents page use the same row. The
> search box offers **search hints** while typing — catalog titles that run the
> search when chosen, as a search engine completes a query — from the free
> action list and never from `sys/lookup`, which is a priced run (§6.2). A rowable row is
> keyboard-navigable and its link is the pointer target, so no row shows a
> pointer cursor it cannot honour (§6.1). Every headline money figure —
> wallet, home, workshop's parked funds, a session's funds — renders through one
> component, one block per figure (§6.4). **No remarks**: a page carries no orientation
> subtitle, and no control or field carries a sentence explaining behaviour
> (the paid-run caption, the deactivation note, the import, secret and
> rename notes are gone); a subtitle is a record's identity or a scope note
> (grammar §3). Page titles, section headings and nav labels share one
> capitalization, sentence case (§6.1). Buttons have one geometry (no size
> modifier); a record's commands share one row, so a task's Decline stands
> beside Complete; one command has one look and label everywhere (Suspend,
> `End · refund N`); and every confirm is one plain question (§6.3, §6.5).

> **What changed in 0.12.0.** The kernel (ba7f5e5) makes an action a tool
> definition: it carries a required one-line **title**, and both schemas are
> one subset of JSON Schema, stored in canonical form. Each fact below was
> measured live (`docs/kernel-v0.19-verification.md`). A card, the action page,
> the workshop table and a peer's action list lead with the title, the address
> beneath it (§6.2, §6.4, §6.6). Create and edit take a title, and a refused
> dialog comes back open with what was typed, never a secret (§6.4). A form
> reads the canonical schema: a nullable field is a typed field with an explicit
> null choice, distinct from leaving it blank (§6.2). An import names each
> refused operation's place in the document and each fold the kernel applied
> (§6.4). Chat declares its schemas in canonical form and retitles its actions
> without a contract update, so no member loses their grant (§10).

> **What changed in 0.11.0.** The kernel (4dbfedd) calls a step a **task**, and
> the UI follows it in routes, types and words: the inbox lists tasks, and
> `/v1/steps` is gone. Four facts follow, each measured live
> (`docs/kernel-v0.18-verification.md`). A task row names its action by
> address and no longer by id, so the payment branch reads the action by its
> address (§6.3). A waiting task can be **declined** by either party the kernel
> accepts — the one asked, or the one who asked — and its reserved price
> returns (§6.3). A task held for someone on another kernel is listed in that
> person's own inbox, so the network page's peer-held list is gone: work
> addressed to this kernel reaches `sys` like any other (§6.6). Lists answer
> what is open unless asked for all, so history and the sessions page ask
> (§6.3, §6.5).
>
> Money gains two facts. A transfer reaches a user of any kernel: its receipt
> states the payment's state as the kernel serves it, and the sender's ledger
> entry `transfer <tx>` links that receipt (§6.5). Registering a payout address
> takes the account's password, and a wrong one is a message, not a sign-out
> (§6.5).

> **What changed in 0.10.0.** The kernel (9705b3b) names every principal by
> address: a user is `handle@kernel` and an action `owner@kernel/name`, on the
> user's own kernel too, as input and output alike, and a bare name is refused
> everywhere (§4). The UI therefore **stops interpreting names**. Every served
> name is passed through and compared verbatim; the party fields are renamed for
> their role (`owner`, `caller`, `target`, `from`, `to`, `required_caller`,
> `party`) and hold addresses; the session holds the signed-in address. Two
> readings remain, each in one place: an action address splits at its first `/`
> into owner and name, and it is **local iff its kernel segment is this
> kernel's own name** — never because it lacks an `@`, which every address now
> carries. One composition remains, `handle@<this kernel>` from `/health`: the
> entry screen (which already names the kernel, so a person types only the
> handle), the natives the UI runs to power a surface, the superuser, and an
> app's service account (§5).
>
> Less code follows. A transaction carries its action's address, so the per-row
> action read (`ActionsByID`) and the "(deleted)" fallbacks are gone; an action
> row carries no owner, so the ref's `owner/name` join is gone; the settings
> page stops fetching transactions and processes it never rendered. The chat
> app's store qualifies the bare names it held once, and its boundary now
> refuses a bare name as it once refused a sigil. The agents app keeps its
> bare-handle homes, which the daemon names, and reads the account by address.
> `network_digest` and `rail_address` are `network_fingerprint` and
> `blockchain_address`. Measured in `docs/kernel-v0.17-verification.md`.

> **What changed in 0.9.0.** The kernel (d59742b) retired its stats route and
> started journalling settlement, and the UI follows both, which removes more
> than it adds. **What is known about an action is read where the action is
> read** (§6.2): the reputation strip is the detail read's own
> `local_experience`, and what other kernels report — the provider's own account
> and every other kernel's — renders on the action's page under its issuer,
> through the evidence partial the peer inspection already used. **The ledger is
> the activity** (§6.5): every movement between two accounts is now an entry,
> a provider's payout, the operator's fee and an obligation included, so the
> wallet lists entries alone and the fold of transactions into it is gone.
>
> Three outcomes now state what the kernel states (§6.2). A failed call names
> what it drew, because sub-work already delivered stays paid and "fully
> refunded" was false whenever it had. A parked call names no refund date,
> because no age expires it. And a session awaiting a peer's receipt is not
> offered a force-end, which the kernel refuses. A world without addresses pays
> out through its operator, so a withdrawal is offered on every world and only
> the address form depends on the world (§6.5). The pay-in disclosure names the
> token contract on a chain world.
>
> One defect the measurement exposed is fixed across the UI: every truncated id
> rendered two ellipses, because the helper adds one and every template added a
> second. A drift guard keeps the helper the only author. The agents app finds
> its daemon in `$JUICE_HOME/bin` before `PATH`, where the ecosystem standard
> now puts the family's programs.
>
> Measured on two throwaway kernels and recorded in
> `docs/kernel-v0.16-verification.md`.

> **What changed in 0.8.0.** The kernel moved its whole operator surface from
> `/control/*` to `/v1/admin/*` and split the one target namespace in two: a
> user is reached under `users`, a kernel under `peers`, and each route refuses
> a target of the other kind. The UI follows (§3.1, §6.6), which removes rather
> than adds — one record shape where there were two, one rename form per
> namespace where one served both, and no code left that guesses which kind of
> account a name denotes. Two gestures go with it, because the kernel retired
> what backed them: **a peer is never credited**, so the Fund link is gone and
> the deposit dialog is user-only; and **an obligation closes when its buyer
> pays**, so the money confirm on each owed row is gone and "Owed to us" is
> exposure itemised. A row whose buyer has not answered says it is awaiting
> them rather than printing a figure nobody decided.
>
> The client records dropped contexts for `handle@kernel` logins, so a UI that
> named a context could not start. It names a **kernel** now (`kernel` /
> `JUICE_KERNEL`), resolved from the shared records, and the retired key is
> refused by name (§2). One process serves one kernel. §5 states, once, why this
> UI reads no credential file: it is a service that authenticates browser
> people, not an operator's personal client.
>
> **Two invariants the UI already stated were not held**, and both are enforced.
> An app that acts on the UI host is an operator surface (§5, apps):
> `apps/agents` writes the installation's files and spawns a daemon, yet gated
> only on a session, so any account could edit or stop another's agent and start
> a process on the machine. And no spend may be reached by a GET (§8.2): search
> ran `sys/lookup` assuming it is free, but a native's price is the operator's
> configuration — measured on a kernel that prices it at 7 and charges — so the
> UI reads the price first and degrades.
>
> **The world is named wherever the interface names itself** (§7). The brand
> states the kernel and its world, and the wallet's pay-in sentence names the
> world whose rule it is stating. A kernel serves one world for life and the
> world decides whether the money on screen can leave; the symbol does not say
> that, so without it a rehearsal and a live market look alike. Both facts come
> from `/health`, and the UI judges neither.
>
> **Six things the kernel offers that the UI could not reach** now work. A
> compiled `kind=wasm` artifact can be given to an action, so
> `sys/tinygo/compile` has somewhere to go; an OpenAPI import carries one
> upstream credential for the whole application; a pending consent exchange is
> reported as pending rather than granted; the withdrawals list is read to
> exhaustion, since it pages like every other list (§6.4, §6.5); work a peer is
> holding for this kernel is listed and completable, the peer named in the body
> (§6.6); and the kernel's planner reaches the interface (§6.2) —
> `sys/llm/decide` chooses one action from the candidates on screen and proposes
> arguments without executing, so browse can ask it and land on the chosen
> action's own page with the form filled in. That last is kernel U46 made
> visible, planning and spending as separate decisions, and it adds no way to
> spend: the same confirm, naming the same amount, still has to be pressed.
>
> **Two surfaces close loops the UI had left open.** The signals a buyer already
> has became usable when choosing (§6.2): browse sorts by rating or price, not
> only by the kernel's relevance, since a rating that cannot order a list is a
> number to look at. An unrated row sorts after the rated ones rather than as a
> zero, because silence is not a low opinion. And an agent's account reaches its
> page (§5, apps): an agent is a user that holds its own principal and spends
> it, and the app managing one showed a persona and a run switch and no money at
> all, so a person could raise something that spends and never see what it
> spent.
>
> All of it measured on throwaway kernels first, including an anvil chain world
> and a local model, and recorded in `docs/kernel-v0.15-verification.md`.
>
> **What changed in 0.7.1.** The kernel published a money rendering and gave each
> world a `symbol`, so the UI stops rendering its own (§7): amounts read the way
> every juice client reads them — trailing zeros trimmed to a two-digit floor, no
> separators, the world's symbol appended — and a unit the UI cannot learn is a
> refusal rather than a guess. The six-decimal grouped form of 0.7.0 was mine to
> invent only while the kernel had published nothing.
>
> Two things the kernel resolved in the UI's favour land with it (§10): a
> cross-kernel transaction now names its ticket on **both** books, so the seller's
> page names it too, and a step parked for a remote principal renders a handle
> rather than a raw id.

> **What changed in 0.7.0.** The kernel's v0.14 contract lands, verified live
> against a federated pair (`docs/kernel-v0.14-verification.md`). Six axes; each
> is written into the section it governs, and this note is the index.
>
> 1. **Money is the world's unit, not ours** (§7). A kernel serves one world for
>    life and `/health` names its money: `decimals` scales every base-unit
>    integer, `network` names it. The dollar format was a UI invention — 1000
>    credits to a dollar is true on no world the kernel ships — so amounts render
>    scaled by the world's own decimals, and an amount finer than the base unit
>    is refused rather than rounded. (The display form itself is superseded by
>    0.7.1, which adopts the kernel's.)
> 2. **A session is pinned to the kernel it signed in to** (§5). A port is a
>    default, never an identity; the session records the kernel's key and network
>    digest and refuses a server that no longer reports them.
> 3. **A mutation names a target** (§6.4). Enable, disable, update and delete
>    name an action id or an `owner/path` subtree and return the rows written, so
>    the UI reports what changed rather than predicting it. An application is
>    imported by name and switched off by path; unimport is gone.
> 4. **A discovered action resolves on open** (§6.2). Opening its card resolves
>    the reference with its home kernel — one round trip, no spend — and lands on
>    the ordinary action page quoting live terms. The by-ref run surface is gone:
>    one run path, one pin.
> 5. **The wallet follows the rail** (§6.5). A user withdraws and registers the
>    address they are paid at where the world has addresses, and reads
>    "recorded by the operator" where it does not. A cross-kernel call's
>    breakdown names what its numbers mean: paid, import fee, obligation.
> 6. **A peer holds no money** (§6.6). Every cross-kernel debt settles per call
>    on the rail by a ticket both kernels name, so the bilateral position, Settle,
>    Record cash and admin withdraw are gone. The network page states this
>    kernel's own credit position and lists what it is owed, each row closed by
>    confirming the payment that pays it.
>
> Invariants that predate 0.7.0 are unchanged: the BFF architecture, session and
> token rules, the accent's spend-only discipline and the closed button
> vocabulary, inert counterparty rendering, the single aria-live region,
> monospace tabular money, floating modal forms with the `?form=` fallback,
> keyboard navigation, admin/supervision gating, the CSP, and the test
> discipline.

> **What changed in 0.6.5.** The UI stops speaking a naming dialect of its own.
> A handle is bare wherever it is shown, and a name is taken as typed wherever it
> is read (§4): no surface decorates a handle with a sigil on display, and none
> strips one on input — a sigiled handle is refused as the ill-formed input it
> is, exactly as the kernel refuses it. Printing `@h` while silently reading it
> back as `h` let the two halves hold each other up, so neither looked wrong, and
> the same dead grammar reached the code that spawns the agent daemon. A drift
> guard over every template, core and app, now fails the build if a name grows a
> sigil again (§9).

> **What changed in 0.6.4.** One definition of a shortened public key, so a
> kernel abbreviates to the same string wherever it is named — the header, a
> card, or inside a failure sentence (§6.6). Two abbreviations of one key defeat
> the purpose of truncating it, which is to let a person match one against
> another.

> **What changed in 0.6.3.** The kernel's v0.12.24 contract, verified live.
> Money that moves through an action is now **journalled**: settling a
> `transfer`-effect call writes a ledger entry from the caller to the
> beneficiary, so both parties read the movement even though the beneficiary is
> no party to the transaction (§6.5). The wallet's activity list already folds
> the two records the kernel keeps, and needs no new shape: the transaction is
> the call, the entry is the movement, and an ordinary action whose effect is a
> money movement produces both.
>
> Two names stop being printed raw: a ledger entry's `reason`, which the kernel
> sets to the settling transaction's id, and a `meta.peer` that arrives as a
> public key rather than a petname (§6.6). A kernel's contact cache
> (`last_seen`, `last_contact_failed_at`) is served for every roster row and
> renders as dated fact, never as a verdict on whether the peer is up.

> **What changed in 0.6.2.** The kernel's v0.12.23 contract lands, verified live
> against a federated pair. Every axis is written into the section it governs;
> this note is the index, not a second specification.
>
> 1. **A call that parks is not a call that failed** (§6.2). A dispatched remote
>    call with no signed outcome answers `timeout` and keeps the funds
>    *reserved* — the UI gains a fourth outcome class saying so, naming the
>    reserved amount and the date the automatic refund becomes eligible. Calling
>    that a refund was a false statement about money.
> 2. **A provider's contract change is a refusal, not a failure** (§6.2). The
>    kernel marks it (`meta.retry`) and charges nothing, so the buyer is
>    re-quoted and one click re-runs — the blanket no-retry rule keyed on an
>    indistinguishability the kernel has since removed.
> 3. **A peer's refusal names the peer** (§6.2, §10). It is an operator
>    relationship fact, never the buyer's own permissions.
> 4. **The wallet shows one activity list** (§6.5), folding the ledger and the
>    transactions, because value moved by running `sys/transfer` writes no ledger
>    entry: two separate tables let a spend appear in neither.
> 5. **Every surface is signed-in** (§5). The kernel serves its natives `local`,
>    so an anonymous catalog is empty by construction; a signed-out visitor gets
>    the login screen, not an empty storefront.
>
> Removed, because the kernel does not serve it: the value-transfer queue
> (`/control/transfers`, 404 as of v0.12.23 — with value local-only there is no
> unresolved buyer-side transfer to queue) and the receipt's `value_premium` row.
> Invariants that predate 0.6.2 are unchanged: the BFF architecture, session and
> token rules, the accent's spend-only discipline and the closed button
> vocabulary, inert counterparty rendering, the single aria-live region,
> monospace tabular money in dollars, floating modal forms with the `?form=`
> fallback, keyboard navigation, admin/supervision gating, the CSP, and the test
> discipline.

> **What changed in 0.6.1.** One rule, applied in three places: the UI states
> things in the platform's vocabulary, not in prose of its own. No action is
> special-cased — `sys/transfer` loses its bespoke send form, route, command and
> price helper, because sending money is running an action (§6.5). A chip is one
> letter or one icon with its meaning in the title, never a word (`docs/ui-grammar.md`
> §5.5). A kernel's nickname trails its name in parentheses rather than a
> sentence (§6.6).

> **What changed in 0.6.0.** The kernel's v0.12.16 contract lands, and the
> surface loses what the kernel no longer serves. Each axis is written into the
> section it governs; this note is the index, not a second specification.
>
> 1. **A displayed price is binding.** Every result carries one, including a
>    discovered action (§6.2), and a run may pin the `quote_hash` it was shown:
>    terms that moved are refused before funds are locked, at zero charge, and
>    the buyer re-confirms the new terms (§6.2, §8). A discovered action is
>    therefore runnable — the §3.3 limitation lifts.
> 2. **Naming follows the petname model** (§6.6). A kernel has a key, a
>    self-asserted nickname that never resolves, and a petname bound here. Only
>    the first two are facts; the nickname is counterparty prose. Naming a
>    discovered kernel is an explicit act with its own affordance.
> 3. **The UI owns its failure sentences** (§6.2). The kernel now returns a
>    failure class, not prose, so a class is never shown to a buyer.
>
> 4. **No action is special-cased** (§6.5). `sys/transfer` had a hand-written
>    send form, route, command and price helper; sending is running an action,
>    so it goes through the ordinary surfaces like everything else. Where
>    behaviour differs it keys on a kernel-served field, never on a ref.
>
> Removed, because the kernel does not serve them: action synthesis (`sys/make`,
> §6.4) and the `peer_state` display hint (§6.6). Invariants that predate 0.6.0
> are unchanged: the BFF architecture, session/token rules, the accent's
> spend-only discipline and the closed button vocabulary, inert counterparty
> rendering, the single aria-live region, monospace tabular money in dollars,
> floating modal forms with the `?form=` fallback, keyboard navigation,
> admin/supervision gating, the CSP, and the test discipline.

> **What changed in 0.5.1.** One axis: `GET /control/peers` now merges **every**
> kernel this one knows — counterparties and discovered peers alike — so
> `network` becomes one paged roster under a `.tabs` filter, in which a
> discovered kernel is the unresolved counterpart of a counterparty (§6.6).
> Advertised names collide and the kernel re-aliases on promotion, so nothing
> keys, links, or posts on a displayed name. Verified live against v0.12.8:
> `admin inspect` serves an inline `account`, and its action projection lost
> `action_id`/`owner_handle`/`stats` (§10).

> **What changed in 0.5.0.** The naming migration ends and one workflow leaves.
> Three axes change; the visual language and every other invariant of 0.4.0 are
> preserved verbatim.
>
> 1. **Sigiled input is rejected, not rewritten (§4).** The kernel's migration
>    period is over — `@` separates a kernel, so `@alice` is ill-formed input it
>    refuses. The UI stops canonicalizing anything, at every boundary including
>    the chat daemon's, and shows the kernel's own reason. Silently reading
>    `@alice/greet` as `alice/greet` would make a UI-only dialect of a grammar
>    the CLI and kernel share.
> 2. **Step creation leaves the browser (§3.2, §6.3).** `POST /v1/steps` binds a
>    live funding trace, which is exactly the bookkeeping this UI hides, so the
>    form asked the inbox's own reader for a trace UUID. Steps are composed by
>    actions and app modules through the kernel API; the inbox is work addressed
>    to you: list, read, complete.
> 3. **Rename gains a home (§3.1, §6.6).** One modal on the admin user detail
>    frees a handle and carries every reference to the new one. A peer's local
>    alias is an ordinary handle, so the alias itself links into that same form
>    rather than adding a fourth command to every peer card.
>
> Three kernel-contract repairs ride along, each verified live against v0.12.5:
> the ratings projection is `{value, note, created_at}`, `POST /v1/steps` takes
> `action` (not `action_id`), and `GET /control/peers` is a bare array.

> **What changed in 0.4.0.** The kernel's v0.12 contract lands: the network
> becomes first-class — bare names, bilateral money, spendable steps. Three axes
> change; the visual language of 0.3.0 is preserved verbatim.
>
> 1. **Names lose the sigil; `@` now names a kernel.** The canonical action
>    identity is bare `owner/name` and a handle is bare `sys`, `alice` (§4). A
>    remote action is `owner@kernel/name`, where `kernel` is a presentation-only
>    local alias for a peer kernel — never an identity, never a link target, and
>    never a key (§4, §6.2 owner pages). The UI *emits* bare names everywhere and
>    *tolerates* `@`-prefixed input for the kernel's one migration period.
> 2. **Step completion becomes two-branch.** An ordinary completion stays the
>    neutral `Complete` naming no amount; a **payment step** — one whose listing
>    carries the kernel's `payment` descriptor — is a spend of the completer's
>    own balance and MUST use the money button and spend confirm (§6.3, §7, §8).
>    The completion form derives from the step's `allowed_input`, never from
>    reading the target action, whose read may now legitimately fail (§6.3).
> 3. **Peer money reads as a signed bilateral position.** A peer's balance may
>    be negative (they owe this kernel); prepaid-credit copy is retired. Peers
>    surface `settlement_due`, and settlement verbs plus the superuser
>    value-transfer queue get a home in the new §6.6 (network & admin).
>
> The wallet's send affordance now runs the kernel's `sys/transfer` action (a
> receipt-backed spend) instead of the bare `/v1/transfers` endpoint (§6.5).
> Because the card-art hue is hashed from the owner segment, de-sigiling
> reshuffles provider colors once; this is accepted, not worked around (§6.2).
>
> Invariants that predate 0.4.0 are unchanged: the BFF architecture, session/token
> rules, **the accent's spend-only discipline and the closed button vocabulary**
> (a payment step is a genuine spend, so the discipline gains a site, not an
> exception), inert counterparty rendering, the single aria-live region,
> **monospace tabular money**, the provenance typography, floating modal forms
> with the `?form=` fallback, keyboard navigation, admin/supervision gating, the
> CSP, and the test discipline.
Codename: `juice-ui`

> **Only what the kernel serves.** A UI feature is mandatory only if it maps to an HTTP
> endpoint **confirmed on the running kernel**. Anything unconfirmed is marked
> *Conditional* and indexed in §10 (Open items), and is treated as conditional
> until verified. The BFF may compose HTTP calls, but must not infer, emulate,
> or bypass kernel state transitions.

## 1. Purpose and architecture

A modern web interface for the Juice kernel, implemented in Go. It exposes the
Juice user flows and talks to the kernel **only over the documented HTTP API**
(`docs/API.md`); it never shells out to the CLI, reads the kernel database, or
imports kernel packages.

The UI is a **backend-for-frontend (BFF)**: a Go HTTP server that renders the
interface and proxies every request to the kernel API, holding the user session
server-side.

The UI is one collaborative workspace (§6.1, §6.7): conversations among people
and agents are its frame, and actions are operations within them — chosen from
the composer, prepared, run and their results reused without leaving the
conversation. Every other page — the dashboard, the inbox, the catalog, the
workshop, the wallet — stays a destination, reached from the avatar menu and
the composer's launcher.

* The browser communicates only with the BFF. The BFF is the sole client of the
  kernel HTTP API.
* Kernel access and refresh tokens are stored server-side in the session. They
  are never sent to the browser, embedded in HTML, or written to logs.
* The interface is server-rendered HTML with small client-side scripts for
  schema-driven forms and event polling. A heavy single-page framework MUST be
  avoided.
* The kernel is authoritative. The BFF holds no durable domain state beyond the
  session (tokens, PKCE state, CSRF token, theme). It re-renders from kernel
  responses and never reproduces kernel business logic (fees, settlement, schema
  validation): the UI presents kernel results; the kernel decides.

**The installer.** The UI is the desktop to the kernel's server: a person who
installs the UI has a kernel. One rule decides what a launch shows: **an
installation that is complete opens the UI; one that is not runs the
installer**, package `installer`, which shows only the steps this machine needs
and then opens the UI. The installer has the shape OS installers use
(Calamares, Ubuntu's installer): **pages**, which ask and change nothing, a
summary, and one step whose work runs as **jobs** with progress. What is missing
is read once, at start:

| What is missing | The installer's steps |
|---|---|
| Nothing | none: the installer does not appear |
| A component only | Download |
| A kernel only | World → Kernel → Models → Recovery phrase → Summary → Install |
| Both | Download → World → Kernel → Models → Recovery phrase → Summary → Install |

A kernel is missing when the configured kernel is this machine's (a loopback
URL), no kernel home `$JUICE_HOME/kernels/<world>/` for the URL's port holds one
(`juice.db`, a home that cannot be read counting as one), and nothing answers at
the URL: a kernel that answers without a home here is followed, never created
over, since a creation would collide with its port. A kernel elsewhere is an
operator's; it is never installed, created or started.

Every launch then does the same, whether or not the installer ran: it **starts
this installation's kernel**, or waits for one that is not this machine's to
start, and the application takes the listener. While it does, a page request is
held and then answered by the application, so the page loads for those seconds
and then shows sign-in, with no screen in between. Only a kernel that has not
answered within 15 seconds, or that fails to start, is said: on the entry card,
the job in hand ("Start the kernel", "Wait for `<url>`") with the kernel's own
words, polled until it answers. Nothing on that card names an installation.

* **Download** installs what is due, one job each, from one list: the kernel
  (`juice`) then the agent (`juice-agent`), each at the release this UI was
  verified against. A component **missing** from `$JUICE_HOME/bin` is due; one
  **older than its release is due only in a fresh installation**, one whose
  `kernels/` holds no `juice.db` at all, read to the end: a directory that cannot
  be read is an error, never an absence. Otherwise nothing present is replaced: a
  kernel that exists was last served by the binaries beside it, and a newer one
  may refuse its home (kernel v0.18.1 refuses v0.16.0's configuration, which
  stopped a polygon kernel on 2026-11-10). Upgrading under an existing kernel is
  the operator's act, with that component's own installer. A component whose
  override variable is set (`JUICE_AGENT_BIN`) is the operator's, and the kernel
  binary is not installed for a kernel that is followed. Each is installed by one
  routine, the contract of the family's own `install.sh`: the platform's asset and
  `checksums.txt` from that component's GitHub releases, refused without a
  matching SHA-256, tried with `--version`, its setup run (`juice kernel setup`,
  `juice-agent setup`), which writes its schemas (ecosystem-standard §3), and
  renamed atomically into `bin/`. A
  failed download is shown and tried again every 10 s. It comes first because the
  World page lists the worlds by running the kernel program. Before a first boot the
  step also checks the kernel binary: one older than its release, kept because
  another kernel of this installation runs on it, would ignore the recovery key,
  so the step stops there, naming both versions, until Try again.
* **World** is one of those `juice kernel worlds` lists (the world files and the
  shipped worlds not yet written), each with its symbol and the kernel's own
  description, `play` preselected, one the kernel would refuse shown disabled with
  its error; only a listed, servable world is accepted. **Kernel** takes the
  kernel's name and the superuser's password, twice, with `sys` read-only above
  it. The world and the kernel name each state their one consequence.
* **Models** chooses the kernel's language models with the models form (§6.6):
  the chat model, behind the kernel's chat, JSON and decide, and the search
  model. It opens on the first provider on this machine that runs, with the
  models the kernel's own schema states as its defaults where that provider
  serves them, and otherwise on None. None leaves the roles unbound and writes
  no model, so the kernel registers no model action that cannot answer. Next
  goes on only with models their providers serve, or with None. The choice and
  its keys are held in memory like the password, and the first boot is given
  them in its `config.json`.
* **Recovery phrase** comes before the work, so the person holds it before the
  key is enrolled: the UI generates it as at signup and the first boot passes
  only its public key. It is one phrase for the whole sequence, across Back and
  across a failed boot, shown on its page alone, acknowledged with its "I
  understand" box, held in memory and stored nowhere, and dropped when the
  sequence ends.
* **Summary** names the world, the kernel name, the superuser, the chat and
  search models and the kernel's home, and is the point of no return: nothing before it writes anything. On a
  world whose rail is a chain (`evm`) it states, behind the alert icon, that the
  vault's keys live in that home and that losing it loses the money. Its button
  is **Install**.
* **Install** runs the first boot and nothing else. The first boot writes that
  home's `config.json` and serves the kernel once with the password and the
  recovery public key in the child's environment, the kernel's documented
  non-interactive first boot. It never touches a kernel: it writes only into a
  home holding no `juice.db` that no server holds (`serve.lock`), refuses any
  directory holding `juice.db` whatever its configuration says, and deletes
  nothing, even when it fails. One first boot runs at a time per installation,
  under an exclusive lock in the UI's own state directory. A boot that made no
  kernel returns to the Kernel page with the kernel's words, where the password
  is typed again; one that made a kernel goes on to the launch, which starts it,
  and the UI opens with no further page. The password is
  held in memory from its page to the boot and dropped when the boot returns.
* **Starting the kernel**, at every launch, declares it to the process supervisor once, as the program
  `kernel-<world>`, which runs `juice kernel serve <world>`, whatever the version
  of the binary beside it. The first boot's kernel carries two secrets, so it is
  not a program: it ends before the first boot returns, within the same 30
  seconds and SIGKILL as any program, and the supervisor then starts the kernel
  without them; the installation is examined only while no first boot runs. A
  kernel answers for the start only once it is the supervisor's child; a program
  the operator disabled is theirs, and is waited for; a kernel with no usable
  binary here that answers is followed and not declared. A kernel that does not
  start is shown with its own words and tried again, and a directory holding
  `juice.db` whose `config.json` is missing or unreadable is reported as such.

Every step of the installer renders in one frame headed Juice installation (grammar §5.14): the
installer's numbered steps on the side, done ones ticked and the current one
marked, and beside them the step, in three slots: its heading, its content and
its commands at the foot. On a narrow screen one line, "Step 3 of 6", stands in
for the list. A step supplies content only; no step sets a size of its own. A job step lists its jobs, each pending,
running (the in-flight bar), done or failed with its words, and is polled every
2 s until the step changes. The server holds the position and the answers, so
every path renders the current step: a reload, a second tab or a back-navigation
shows where the installation is. A step's post is accepted only from the step
that is current and is answered by a redirect, so a reload or a second submit
changes nothing; a page that refuses its answers is shown again with what to say
and what was typed, never a password. Back is offered on the pages after the
first and never once Install has begun. Nothing follows Install: no finish page. A job step starts its jobs when it is
entered, in the background, so no post waits for the work. A UI restarted
mid-sequence begins again from the facts.

The installer is a local act: a page's form, its posts and the phrase are served
only to a request from this machine (a loopback listener, a loopback peer, no
proxy header, the same origin); any other request is told the terminal command.
The UI never removes a kernel. The package imports nothing of this repository: it
reaches the kernel package and the application's renderer through two small
interfaces the entrypoint satisfies, and a test holds that.

**Navigation model.** The UI's permanent elements are three: the
conversations, the composer, and the avatar menu (§6.1). The destinations —
**Home**, **Inbox**, **Discover**, **Workshop**, **Wallet**,
**Settings**, plus **Network**, **Admin** and an app's operator surfaces for
`sys` alone — are one registry, which the avatar menu lists and the launcher
searches. Each still maps to the CLI concepts it always did: `home` is the
dashboard over the others; discovery + execution (`run` + the catalog) is
Discover and the composer's launcher; `inbox` is `task` — work addressed to the
user, carrying the sole attention badge, which counts the waiting tasks
addressed to the user and the requesters awaiting the user's decision (§6.3);
`workshop` is the provider surface (`action` management + creation); `wallet` is money evidence (`user` balances + `process` lifetimes +
the `tx` ledger + receipts); `settings` is account, preferences, and connected
accounts; `network` is federation (peers), and `admin` is operator
supervision. Moving a page out of the sidebar grants and removes no authority —
the underlying routes and superuser gating are unchanged. The kernel still
creates and closes processes automatically; the UI MUST NOT ask the user to
manage that bookkeeping, but it MAY surface a process's parked funds and
awaiting-receipt state as evidence.

**Reference discipline.** Each cross-cutting rule is defined once and applies
wherever relevant without per-use citation: the API contract in §4, session and
token rules in §5, the visual language (the accent, button vocabulary, status signals,
feedback) in §7, the security rules (including inert rendering of counterparty
content) in §8. An obligation marked *Conditional* is indexed in §10; the feature
is mandatory only once its prerequisite is confirmed. Where a reference is
unavoidable it names the target as well as its number, so a stale number remains
detectable.

**The bootstrap.** Juice-UI is installed in two stages, as a desktop product is.
`install.sh`, at the repository root, is the bootstrap, adapted from the kernel's:
it installs this repository's three programs (`juice-ui`, `juice-chat`,
`juice-supervisor`) from one release tarball, `juice-ui_<os>_<arch>.tar.gz`,
refused unless it matches the release's `checksums.txt`; each program is staged
and tried with `--version` before any replaces an installed one, so a bad
download changes nothing; it runs `juice-supervisor setup` by its path, adds
`$JUICE_HOME/bin` to the shell profile once (`--no-modify-path` prints the line
instead), starts nothing and asks for nothing. The UI then installs its
components (the installer, above). The release workflow packs the three
programs into each platform's tarball.

**The process supervisor.** Package `supervisor` runs the installation's programs:
the kernel, the agents, chat and anything an operator adds. The UI embeds it and
owns one, opened at start and closed at exit; `juice-supervisor` is the same package
for a machine with no UI. It imports only the standard library, and of Juice it knows
only the installation's layout. The UI proper starts and signals no process, and the owners (`kernel`,
`apps/agents`, `apps/chat`) declare their programs and keep what is specific to
them. A test holds this.

* A program runs in the foreground and ends on SIGTERM. Nothing else is asked of it.
* A program is one file, `$JUICE_HOME/supervisor/programs/<name>.json`, with `command`
  required and `env`, `dir`, `restart` (`always`, the default, or `no`), `disabled`,
  `stop_seconds` (30) and `lock` optional. Its schema is
  `schemas/juice-supervisor.schema.json`. A key the schema does not know refuses that
  program, which is reported; the others run. An owner writes a file once, when it is
  absent; after that it is the operator's, and Start and Stop change only `disabled`.
  A bare command is looked up in `bin/`, then `PATH`.
* Each program is a child in its own process group, and its output is written to
  `supervisor/log/<name>.log` through a pipe, rotated at 10 MiB with one old file kept.
* A program that ends by itself is started again after 1 s, doubling to 60 s, back to
  1 s after a run of 60 s, indefinitely. An unchanged declaration does not disturb the
  delay; Start and Restart try at once.
* Stop is SIGTERM to the group, then SIGKILL to whatever remains of it after
  `stop_seconds`; a program counts as ended only when its group has. It writes `disabled`,
  so the program stays stopped across starts of the UI, until Start clears it. Closing
  stops every program at once, waits, and writes nothing.
* One supervisor per installation, by an exclusive lock on `supervisor/supervisor.lock`;
  a second is refused, naming the first.
* A supervisor that was killed leaves its programs running. The next one stops them
  before it starts anything, whether their files say enabled, disabled or nothing,
  from `supervisor/run/<name>`, which holds the child's id and start time. This is
  best effort: a start time has whole seconds, and the check and the signal are two
  steps. With no way to read a start time, nothing is signalled. A program may name
  its own `lock`: a process holding it is stopped by the id in it (ecosystem-standard
  §4) at open, before each start and when the program is disabled. That is how the
  detached programs of 0.47.0 are taken over.

## 2. Implementation constraints

* Use Go. `go build ./...` and `go test ./...` MUST pass without external network
  access.
* Keep package and source-file counts small. Do not split files for size alone.
  Every production `.go` file MUST have a corresponding `_test.go` file with
  independent tests for its logic.
* Suggested package layout (function-named; may change for cohesion or cycle
  reasons):

  ```text
  cmd/juice-ui/   entrypoint: configuration, dependency wiring, serve
  client/         typed kernel HTTP API client and error mapping
  session/        server-side sessions, token lifecycle, PKCE state, CSRF
  server/         router, middleware, request handlers, rendering
  apps/           self-contained app modules behind one seam (e.g. apps/chat)
  services/chat/  the chat service, imported only by cmd/juice-chat, its program
  supervisor/     the process supervisor; standard library only, Juice unknown to it
  installer/      the first start's sequence, imported only by cmd/juice-ui
  models/         the kernel's language-model setup; standard library only
  templates/      HTML templates (not Go; no test file required)
  static/         CSS and client scripts (not Go; no test file required)
  ```

* Always test a new or updated feature against a live kernel (start a throwaway
  one if needed; clean up after).
* Configuration loads from an optional JSON config file overlaid by environment
  variables (env wins), over safe local defaults. At minimum: the kernel — named
  by the **name it is recorded under** (`kernel` / `JUICE_KERNEL`), resolved
  from the `kernels` half of `$JUICE_HOME/client/config.json` to the recorded
  endpoint, public key and network, or else a bare URL (default
  `http://127.0.0.1:4040`) — the UI listen address, and a session secret. Commit
  no secrets. Reject invalid startup configuration clearly, including a kernel
  name the records do not hold. A configuration key the records retired MUST be
  refused **by name**, naming its replacement: the remedy is not guessable from
  an unknown-field parse error.
* **The UI knows which kernel it is for before it starts.** Under a named kernel
  that is the recorded key; under a bare URL the kernel is identified once at
  startup and a kernel that cannot be identified is a startup error. Every
  `/health` answer is held to that key: an address presenting another kernel is
  not this kernel, and nothing is trusted from it. Everything durable the UI
  keeps for its apps is keyed by that kernel's public key, never by a name or an
  address (ecosystem-standard §4), so state provisioned on one kernel is never
  replayed on another. **One process serves one kernel**, as one
  `juice kernel serve` process serves one kernel home; two kernels are two
  processes. The chrome names the kernel and the world it serves.
* Technology stack (standard-library-first; dependencies only where hand-rolling
  would be a correctness or security risk):
  * **Routing:** stdlib `net/http` ServeMux (Go 1.22+ method/pattern routing,
    `{id}` wildcards). No router dependency.
  * **Templating:** stdlib `html/template` (auto-escaping); partials double as
    htmx swap fragments.
  * **Interactivity:** **htmx** plus small vanilla ES modules — no build step, no
    Node, no SPA framework. Handlers return HTML fragments; htmx performs swaps
    and the page's heartbeat (one request per lane, every 5 s and every badge
    interval, carrying each widget's watch). Vanilla JS is limited
    to the schema→form renderer, the theme toggle, a tab-visibility signal, and
    the wallet module (`wallet/wallet.mjs`, §6.5), which speaks EIP-6963 and
    EIP-1193 to the person's browser wallet and is tested with `node:test`
    against a fake provider.
  * **Money isolation:** the package `wallet/` holds the registration message,
    the chain proof, the transfer a wallet is asked to send, and the only calls
    to the two kernel mutations that move or point money (`Withdraw`,
    `RegisterAddress`); a drift test fails on any other caller. Reads and
    rendering stay in the shell.
  * **Styling:** hand-written CSS driven by the design tokens. No CSS framework,
    no build pipeline.
  * **Sessions:** `alexedwards/scs` for server-side token storage.
  * **CSRF:** `gorilla/csrf` — security-sensitive primitives are not hand-rolled.
  * **PKCE / crypto:** stdlib `crypto/rand`, `crypto/sha256`, `encoding/base64`.
  * **Logging:** stdlib `log/slog` (structured).
  * **Tests:** stdlib `testing` and `net/http/httptest`; the kernel is faked as
    an `httptest.Server` (no network, no live kernel).

  The dependency footprint is small (htmx on the front end; `scs` and
  `gorilla/csrf` on the back end); everything else is the standard library.

## 3. Scope

### 3.1 In scope (HTTP-reachable), organized by CLI group

* **run** — server status (`GET /health`), read-only; natural-language discovery
  via `sys/lookup`; catalog browse (`GET /v1/actions`); action detail; call
  execution (`POST /v1/run`); a discovered action is opened by resolving its
  reference (`GET /v1/actions?ref=`), which lands on the ordinary action page.
* **browse / workshop** — catalog browse plus owner action management: create,
  list/show (including inactive, via `GET /v1/actions?owner=<self>`), update,
  enable, disable, delete; three-way `visibility` (private/local/public);
  OpenAPI import by name; the evidence an action read carries; ratings list.
* **process** — computation lifetimes (`GET /v1/processes`, show, force-end),
  surfacing parked funds and awaiting-receipt state.
* **tx** — the immutable ledger: transactions list/show/rate; receipt
  verification (`GET /v1/transactions/{id}/receipt-verification`), on demand.
* **task** — tasks: list/show/complete/decline. Task creation is API and
  app-module surface only (§6.3).
* **user** — self profile, balances (`GET /v1/me`, `available`/`locked`), and
  update (`PUT /v1/me`); the user's own ledger (`GET /v1/ledger`); withdrawals
  (`POST`/`GET /v1/withdrawals`) on every world; and, on a world whose money has
  addresses, the payout address (`PUT /v1/me/blockchain-address`).

*Conditional:* the trace tree on the receipt page.

* **Admin / supervision (superuser only):** the `admin` group, visible only to
  `sys`, covers user list/show/suspend/unsuspend/rename and crediting a user
  (§6.6); federation trust in a namespace of its own — peers, provisioned by
  use alone, with show/inspect/suspend/unsuspend/rename — this kernel's
  identity, the obligations owed to it, and its configuration (§6.6). The kernel serves these over
  HTTP on `/v1/admin/*`, gated by an `IsSuperuser` check on the `sys` bearer
  (API.md C13); the UI is an ordinary client of that surface. **The route says
  which kind of target it takes** and a target of the other kind is refused, so
  the UI addresses users and peers on separate routes and never sends a public
  key to a user route. Superuser **scope** on the normal list pages (a `sys`
  session sees all actions/processes/tx/tasks) is indicated quietly on those
  pages, not duplicated as separate admin listings.

### 3.2 Out of scope (no user-facing flow)

* **Trace inspection:** no trace-read HTTP route is confirmed, so the Engine-room
  trace tree stays conditional (§10).

### 3.3 Known limitations

* Because deposits are admin-only, a UI user cannot top up their own balance.
  Balance views MUST state where money comes in on THIS world: an operator
  records it where the world has no payment addresses, and where it has them the
  wallet names the kernel's address and offers the user their own (§6.5). A peer
  account holds no money on any path (kernel §D14), so no peer surface shows a
  balance or a top-up.

## 4. API contract the UI must honor

* Snake_case JSON on the wire; never expose Go field names; never echo secret
  fields.
* Structured fields (`args`, `result`) are inline JSON objects, not
  double-encoded strings.
* **Every name is an address** (kernel §D15): a user is `handle@kernel`, an
  action `owner@kernel/name`, and the kernel segment is this kernel's own name
  (`/health.handle`) for its own principals, a petname or public key for a
  peer's. A bare `handle` or `owner/name` is refused by the kernel at every
  input. The UI **renders and sends names exactly as served** — it never
  strips, decorates, or rewrites one — and its only readings of an address are
  two, each defined once: the owner is the segment before the first `/`
  (names may hold `/`, an owner never does), and an action is **local iff its
  kernel segment is this kernel's name**. A local owner links to its owner page
  (`/users/{address}`); another kernel's does not, since no local profile backs
  it. The UI composes exactly one address shape, `handle@<this kernel>`, and
  only where it names this kernel's own principal from a handle: the entry
  screen (§5), the natives that power a surface (§6.2), the superuser `sys`,
  and an app's service account. In a URL path an address is written with its
  `@` literal (the kernel refuses `%40` there); in a query it is
  percent-encoded like any value. A peer kernel as a party is its name alone.
* Action read/list responses carry both `id` (UUID, for management) and a
  computed `action` (the address, for calling); an action row carries no owner
  field — the owner is the address's first segment. The UI uses `action` directly
  in call requests and never asks the user to type a UUID.
* State-mutating responses return the updated resource; the UI re-renders from
  that response. Destructive operations and `end` return 204.
* List responses are bare arrays; pagination metadata is in headers.
* `POST /v1/run` and `POST /v1/tasks/{id}/complete` always include `args`; empty
  input is `{}`.

## 5. Authentication and session

**Whose logins these are.** `ecosystem-standard.md` §6 says an interface "is a
client like any other… reads the same logins, shows which one is in use". That
describes a **personal** interface: one operator, one terminal, one installation.
This UI runs in **service** mode — it authenticates browser people over the
network and holds each one's kernel tokens in its own server-side session — and
the two are not the same client.

So the UI reads the `kernels` half of the client records and **never reads a
credential**. It writes one only for an agent's login, at the operator's act
(New agent, or Start when the login has no session, §6.1), from a password typed in that act and kept
nowhere, and the kernel record it writes when that kernel is absent or has
moved is the one `juice kernel add` would. A browser visitor's session never
reaches the store. Two consequences make this a rule rather than a
preference. An installation is one operating-system user's state (§1 of that
document), so tokens placed there are the operator's; a service that wrote its
visitors' sessions into that store would let every visitor act as the operator's
logins. And two programs logged in as one account share one session (§4), so a
browser sign-out would revoke the operator's own command-line session.

What the standard asks for observably, the UI does: it names its kernel from the
shared records, refuses a `/health` that is not the recorded key, shows which
kernel and world is in use, and keys everything durable by
`(network fingerprint, kernel public key, principal id)`.

* The entry screen (sign in, create account, recover) takes a **handle**: the
  screen names the kernel it signs in to, so the account is that handle's
  address here, `handle@<kernel>`, composed from `/health`. The session holds
  that address, and it is what every "is this me" comparison uses; the
  superuser is `sys@<kernel>`. A session from before addresses carries no
  fingerprint pin and is signed out by the ordinary kernel check.
* Browser login uses authorization-code with PKCE. The BFF renders the login
  form, generates the verifier/challenge (verifier kept server-side), calls
  `POST /v1/auth/authorize`, extracts the `code` from the returned redirect, and
  exchanges it at `POST /v1/auth/token` with the verifier. Tokens are stored in
  the server-side session.
* A single request wrapper handles refresh: on `ErrUnauthenticated` it performs
  `POST /v1/auth/refresh`, stores the rotated token pair, and retries the original
  request once. Refresh MUST be single-flight per session, because rotation
  invalidates the prior refresh token and concurrent refreshes would fail.
* **What a request may not save.** Each request works on its own copy of the
  session and saves it whole, so a value another request changed meanwhile
  would be written back stale. The token pair, the last activity, the
  unacknowledged recovery phrase and the member's chat key (§6.7) are therefore kept in a store beside the
  session, keyed by its token, which every request reads and none saves
  whole: a rotation is seen by every request, the last activity only moves
  forward, and an acknowledged phrase stays gone. Only sign-in and sign-up
  create an entry; every other write changes an existing one, so a session
  ended while a request was in flight stays ended, and a token pair the kernel
  issued to it meanwhile is revoked. A sign-out takes the pair as it removes
  the entry and revokes that one, so every pair the kernel issued is revoked
  by the sign-out or by the refresh that finds no entry. An entry leaves at
  sign-out, or with the session's record, removed within a minute. What stays in the copy is written once at sign-in, or is
  harmless if a stale save restores it: the PKCE verifier lives within one
  request, and a grant state restored after its callback is refused by the
  kernel, which completes a state once.
* When refresh fails, or the user is suspended, the session is cleared and the
  user returns to login with a clear explanation.
* Logout calls `POST /v1/auth/logout` to revoke the refresh token and drops the
  session.
* **Session limits, the UI's own policy.** A session ends **30 minutes** after
  the last request the person made and **12 hours** after login, whatever
  happened. Both sit within NIST SP 800-63B-4 §4.2.3 (1 hour, 24 hours) and are
  rev. 3's AAL2 figures; a password login is not AAL2, so the figures are
  stated as policy, not as a level. Activity is a request the person made —
  a page, a fragment, a form — and the heartbeat never counts: a page left
  open is nobody present, so it observes the limit and never extends it. A
  minute before the idle end the page asks **Stay signed in?**, with *Sign
  out* and *Stay signed in*; `Esc` dismisses it without extending. The
  login page's notice names the limit that ended the session. A record whose
  page was closed outlives its session by the idle limit and then leaves the
  store, so a return later than that finds the login page without a notice.
* **One exit.** Every sign-out — Sign out, either limit, a failed refresh, a
  kernel gone or changed — destroys the session and revokes its refresh token
  only at the kernel that issued it: a kernel that cannot be identified, or is
  no longer the pinned one, is sent nothing.
* The session cookie is `HttpOnly`, `Secure`, `SameSite=Lax`. All state-changing
  routes are CSRF-protected.
* Account creation (`POST /v1/users`) is available from the UI; the UI surfaces
  the kernel's per-IP rate limiting (HTTP 429) gracefully.
* **Every surface is signed-in.** Only login, signup, and password recovery are
  reachable without a session; every other route MUST redirect a signed-out
  visitor to the login screen. This is not a policy choice the UI makes on its
  own — the kernel registers its natives `local` (v0.12.23), so an anonymous
  caller is served an empty catalog and `sys/lookup` refuses the token-less call
  outright. An anonymous storefront would therefore be a blank page with a search
  box that cannot answer, which is a worse account of the system than the login
  screen.

## 6. The interface

### 6.1 Information architecture

The workspace (§1), on a wide screen:

```text
┌─ sidebar ───────────┬─ conversation ─────────────────────┬─ panel ──────────┐
│ ◆ kernel · world  ≡ │ research-room · 3 members · Leave  │ Translate text × │
│ + New conversation  │ ana@k        Find suppliers …      │ linguist@k/text  │
│ assistant           │ researcher@k I found three.        │ 2 credits · ▸ ev │
│ research-room     • │ Waiting on researcher@k…           │       JSON ○  ✦  │
│                     │ ┌ message ─────────────── [/] ┐    │ text   [……]      │
│ (A) ana@k        ③  │ └───────────────────── [Send] ┘    │ [Run (2)]        │
└─────────────────────┴────────────────────────────────────┴──────────────────┘
```

The sidebar is the conversations and the avatar (③ the task badge), whose
menu holds every other destination; the composer is where text, an address and
`/` begin; the panel holds the action chosen or a search's results, empty
and absent until then.

* **The sidebar is the workspace's conversations.** It MUST hold, in order:
  the brand (the kernel and its world, §7) with the collapse toggle; the
  conversations rail (§6.7), headed by the opener of a new conversation; and at
  its foot the signed-in user's avatar, which
  carries the task badge and opens the **avatar menu**: the address and the
  available balance, then every destination of the registry with the inbox's
  count on its row, then Sign out. No destination occupies the sidebar itself.
  The current destination MUST carry `aria-current` in the menu. One registry,
  built once per request server-side, MUST drive the menu and the launcher, so
  they cannot drift; its entries are plain words, every one a link.
* **Two states per width, never mixed.** On a wide screen (>720px) the
  sidebar is docked or **collapsed to a strip**: the same element keeping the
  collapse toggle, the opener's icon and the avatar with its task badge. A
  list of conversation names has no icon form, so the rooms are not in the
  strip. The choice is persisted server-side (`sidebar` cookie, stamped on
  `<html>`) and applies to wide screens alone. There is no topbar and no
  overlay on a wide screen.
* **Sign-in lands in a conversation** (§6.7): the conversations app's landing.
  **Home is the dashboard** (`/`). It MUST show the
  viewer's available and parked balance, the waiting tasks addressed to them
  (read as `status=waiting`, never the whole history), their open sessions,
  and their most recent ledger entries — at most five rows each, every section
  linking to its full page. Recent activity MUST come from the ledger, which
  holds only the viewer's entries, not from the transaction list, which the
  kernel pages before its party filter. Home MUST NOT complete a task (the
  inbox is the one surface that spends on a task) and MUST NOT draw a balance
  chart (the kernel serves no series). An action page belongs to no
  destination: it is reached from the launcher, Discover or a row.
* The account summary (address, available balance) heads the avatar menu.
  Preferences are not chrome: the theme picker MUST appear exactly once, on
  `settings`; Sign out is the menu's.
* A narrow screen (≤720px) MUST reflow to **a single menu affordance at the
  top left**, in a slim topbar with the brand, which slides **the same
  sidebar** in over the page as a drawer — the same element, not a copy,
  without the collapse toggle — closed by a scrim, by Escape and by any
  navigation. The drawer's state is never stored, and the collapse cookie does
  not apply. It MUST be reachable without JavaScript (`?drawer=open`, stamped
  on `<html>`). The closed affordance MUST carry the task badge, so the sole
  core attention signal survives the sidebar being out of view. The frame MUST
  be sized to the visible screen (`dvh`), so the avatar at the drawer's foot is
  never under a phone browser's toolbar. The nav is unbounded, since every registered app
  contributes destinations, so no fixed bar is permissible.
* **An app that presents a kernel principal presents its account.** An agent is
  a user: it holds its own principal, is funded like anybody's, and spends it
  (ecosystem-standard §6). An app that manages one MUST therefore show that
  account — its balance, what is parked, and what it has lately been party to —
  or a person can raise something that spends and never see what it spent. Every
  figure MUST be read with the **viewer's own token** and stated as served; a
  home with no principal says so rather than rendering a zero, and there is no
  funding gesture, because sending money is running an action on its own page
  (§6.5).
* **An app that acts on the UI host is an operator surface.** Most apps are
  ordinary kernel clients and gate on a session. One whose handlers act on the
  machine the interface runs on — spawning a process, writing the installation's
  files — MUST gate every route on the superuser and MUST contribute no
  destination to anyone else. That authority is the operator's machine, which no
  kernel account confers: on a shared root, an ordinary session could otherwise
  edit and stop another person's agent, and anyone signed in could start a
  process on the host. The agents app is one: it keeps an agent's home at
  `$JUICE_HOME/agents/<login>/` (ecosystem-standard §2), edits its `agent.md`,
  and its `config.json` through the form of the daemon's own schema of it,
  `$JUICE_HOME/schemas/juice-agent.schema.json`, as the kernel's configuration
  is edited (§6.6), `$schema` kept as saved and the JSON editor alone until the
  schema exists: a save replaces the file whole, by rename, which the daemon
  reads at its next start, and a running agent's flash says so; a save from a
  page older than the file is refused, since the daemon's start writes the
  keys a file lacks (juice-agent 0.7.0 §11). Each agent is the program
  `agent-<login>` of the process supervisor (§1), running `juice-agent serve
  <login>`. The assistant is declared when it has a session and no program; any
  other home with no program is declared once at start, running when it has a
  session and otherwise stopped. An agent is running when a daemon holds
  `serve.lock` in its home (juice-agent 0.6.0 §11, as a kernel holds its own,
  ecosystem-standard §4), whoever started it, or when the supervisor's child has
  not yet taken the lock; a lock whose state cannot be read is an error, never
  "stopped". Stop has the supervisor stop whoever serves it, and keeps it stopped
  across restarts; Start undoes that and retries at once. The daemon's session
  is the login's own, made as `juice auth login <login>` would (§5): New agent
  creates the account when the kernel has none and the session, from the
  handle and a kernel password; Start is one click while the login has a
  session and asks for the password when it has none — no credential file, or
  a last run that ended with the daemon's `run juice auth login` advice —
  which is how an agent made at the command line or one whose session died
  is served from the page. Start and Delete are refused while the agent runs,
  and no credential is written then, and neither carries a banner: the status
  block, refreshed by the page's heartbeat and never while unchanged in state
  or exit line, is the statement. Delete holds `serve.lock` through the
  removal, so no daemon takes it meanwhile. A stopped agent shows the daemon's
  `error:` line from its current run. The agent's page shows its account;
  its history is the transactions page, since the kernel filters
  transactions by process and not by party. On its start it makes and runs
  the assistant, `assistant@<kernel>`, that a session lands in conversation
  with (§6.7, rule 11); the assistant is otherwise an ordinary agent. It knows
  nothing of chat: chat joins the agents it finds.
* The task badge MUST be the only attention-demanding affordance in the **core**
  UI. It MUST reflect waiting tasks whose `required_caller` is the signed-in
  user (a superuser lists everyone's) and the requesters awaiting a decision,
  sourced from a single server-side read per session (a per-session cache
  whose TTL is the badge interval), carried to every open page by its heartbeat
  and swapped only when the count changed — every copy at once: the avatar,
  the Inbox row of the menu, and the topbar's trigger. An app MAY keep its own
  attention in its own surface (the rail's unread weighting, §6.7).
* All create and edit operations MUST open **floating modal forms** — native
  `<dialog>` elements opened client-side with `showModal()`. Each modal trigger
  MUST also be a real link (`?form=NAME`); without JavaScript the server renders
  the named dialog already `[open]` as an in-flow card, so every flow stays
  operable with JS off. The `?form=` value MUST be validated server-side against a
  per-page whitelist before rendering.
* The UI MUST be keyboard-navigable as a first-class affordance: `g`+letter
  section navigation, `/` to open the composer's launcher (Discover's box where
  there is no composer), `?` for a shortcuts overlay,
  `j`/`k`/`Enter` to move through and open rows (table rows and list rows
  alike), and `Esc` to close the top layer. A rowable row's pointer target is
  its link: the row MUST NOT show a pointer cursor, since no click handler
  stands behind it.
* **The launcher is the composer's `/`.** Its query is the composer's own
  text after a leading `/` — one box, at the foot of the screen, under its
  results. Typing `/` at the start of the composer, or pressing its `/` button
  (which sets the draft aside), opens the list above the composer, the best
  match nearest the box: the catalog's actions matching what is typed by title
  or by address (at most five, title and address), then above them the
  registry's destinations and openers, each led by an arrow (at most three;
  all of them while nothing is typed), and at the top one row, `Search "…"`, naming the price
  of submitting it (nothing when free). Typing sends no request and spends
  nothing: the actions are read from the free catalog file and matched in the
  browser, never from `sys/lookup` (§8.2). **Enter submits the query** to the
  economy (§6.2, the search from a conversation), as the search row does; a
  row is chosen only by clicking it, or by ↑ to it and Enter — nothing is
  chosen for the person — and ↓ comes back. Choosing an action, whether this
  kernel holds it or only knows it from a peer, opens its panel beside the
  conversation; choosing a page goes there; neither posts a message nor runs
  anything, and Escape closes the list and gives the draft back, as a search
  does: the query is never part of the draft. The registry is delivered as
  inert JSON (a `type="application/json"` block), never executable inline
  script, to satisfy the CSP, and every entry is a link, so the launcher adds
  no capability unreachable without it. There is no palette, no search pill
  and no command prefix.

### 6.2 run — discovery, action page, run form

**Choosing by what is known.** The kernel serves a success rate, a rating share
and a price on every local row, and the UI renders them on every card. Those
signals MUST also be usable when choosing, or the feedback a buyer gives comes
back only as a number to look at:

* Discover MUST offer a sort switch — **relevance · rating · price** — through the
  one tabs component (§7), preserving the query and starting a new order at its
  own first page.
* **Relevance is the default and is the kernel's own order**: the lookup ranking
  where a query was made, the catalog's order otherwise. It is the only order
  the kernel itself asserts, and it is how a new action is still found.
* Rating and price **arrange served rows**; nothing is computed. The rating
  compared is the one the card shows, so the number a person sorts by is the
  number they read.
* An **unrated** row — new, or discovered, since the discovery cache stores no
  stats — sorts into a block **after** the rated ones under `rating`, keeping
  its own order. It MUST NOT be scored as zero: "no ratings" is not a low
  rating, and interleaving silence with measured opinion would state a judgement
  the kernel has not made.
* An unknown sort value renders the list under relevance. A mistyped view is a
  view, not an error.

**Proposal.** The kernel ships a planner (`sys/llm/decide`) that selects one
action from typed candidates and proposes valid arguments **without executing
anything** — kernel U46, where planning and spending are named as separate
decisions. The UI uses it twice — to choose among Discover's results, and to
prepare one action's arguments from a conversation (Fill from chat, §6.7) — and
both MUST keep planning apart from spending:

* **One rule for its price.** The command is offered wherever the planner is
  present and switched on, at the price the kernel quotes for it: a neutral
  button when free, the money button naming the amount when priced
  (`Choose for me (N)`; Fill from chat, a form tool, its icon and `N`), its run
  pinned to the quote shown
  (§6.2 quote pinning). Asking is then a spend the person authorizes, never a
  silent one (§8.2). It never runs on a page's load, and an absent planner
  offers no command.
* The candidates posted to choose among are **exactly the refs on screen**. A
  set assembled server-side would be a claim about what was offered that
  nobody could check, and the kernel refuses the whole call on a bare ref it
  does not know, so only refs it served are ever sent back. A fill offers one
  candidate: the action whose panel is open.
* The reply fills **the ordinary form**: the action page's own fields, or the
  panel's. Nothing about a proposal is privileged: the fields are editable, the
  kernel validates them as it validates any input, and the action's money
  still moves only through its own button that names the amount. Preparing
  arguments never runs the action.
* **Nothing is invented to complete a form.** The planner is told to use only
  what the context states and to give an empty string for what it does not;
  such an argument is left blank. (The kernel requires a proposal valid
  against the schema, so an unstated required number or boolean may still be
  proposed or refused: §10.)
* The action page MUST say it is a proposal **whether or not the model said
  anything**, since a person must know they are reading a choice made for
  them. Anything the model did say is counterparty prose by construction:
  inert, and never beside the confirm (§8.1).
* It MUST NOT be reachable by GET with arguments in the URL: that would be a
  link that fills a money form for someone.
* Where the planner refuses or chooses nothing, the UI states the kernel's own
  reason — the planner's words, never a generic failure — and the form is
  unchanged.

**Discovery.**

* **Discover** (`/browse`) is the search's own page: its query box is the
  page's content, a GET to itself, with the catalog's actions matching what
  is typed listed under it — the launcher's rows and rules (§6.1), each the
  action's page; Enter submits the search — and MUST issue `sys/lookup` (required
  `query`, optional `limit`) via `POST /v1/run`. Results
  MUST render as **rows in one ranked column** (the rank is a sequence, so it
  is not spread over a grid), filling the container like every component. A
  row leads with a
  **square tile** (§6.2 media), then the action's **title** as the link with
  its chips beside it and the **price right-aligned** on that line, the ref
  (owner-emphasized) on its own line beneath, the description (at most two
  lines), and the **same reputation strip the action page uses** (icon +
  tooltip: success · rating · latency), price-less since the price is on the
  title line. The title is the row's one pointer target. With no query the page
  is a **flat catalog capped at 20 per page** with a pager (§7 pagination).
* **The catalog file.** The launcher's actions come from one file
  (`GET /search/catalog.json`: `actions`, every page of `GET /v1/actions` —
  the caller's own actions, the live ones it may call, and those discovered on
  peers — each action with its title, the title's normalized key, its id,
  none for a discovered one, and its address; and `search`, what submitting a
  query costs: the lookup's price as the row prints it, empty when free, and
  the quote that pins it), loaded once per page and matched in the browser on
  every keystroke, so typing sends no request. `search` is null when the
  kernel has no active lookup, or prices one without a quote to pin: a priced
  search is never offered unpinned. Matching folds accents, case and
  punctuation (decimal digits are kept), requires every typed word to begin a
  word of the title, and ranks titles beginning with the text first, then
  shorter ones. The file holds the account's private titles, so it MUST NOT be
  stored by the browser (`Cache-Control: no-store`).
* **The search from a conversation** (`POST /browse/search`, `q` and
  `lookup_quote_hash`) is Discover's search answering the panel (§6.7): one
  function serves both, and the panel renders Discover's results, its banner
  and its sort switch through the same partial. It runs `sys/lookup` pinned to
  the quote the launcher's row named, which is the person's authorization of
  that price; without a pin it runs only when a read of it confirms it free —
  a lookup priced, absent or whose price cannot be read MUST NOT run unpinned,
  here or on Discover's GET, since the kernel charges an unpinned run its
  current price — and the search falls back to the catalog match with the
  reason stated.
  A pinned run the kernel refuses because the price moved is said as such —
  not charged, the new price named — with one money button,
  `Search "…" (N)`, pinned to the new quote; it is never shown as the
  fallback, and the launcher reads the catalog file afresh at its next use,
  so its row names the new price and the next search is not refused again. A search is **one ranked page** (at most 20) and has no pager.
  Each row is numbered, by the one sort rule, under every order, so the
  panel's sort switch arranges the rows in the browser and never searches
  again; the switch's links are Discover's, the path without script. The
  planner's `Choose for me` is Discover's alone: it lands on an action page.
* **Search fallback.** When `sys/lookup` is priced or fails, the search MUST
  match the whole catalog by the launcher's rules over title, name and description
  — a title beginning with the query first, then a title holding its words, then
  a name or description holding them — at most 20 results, discovered rows
  kept and marked.
* **Media slot.** The action page MUST carry a 16:9 media slot, and every list
  row a square tile cropped from the same image. Until the kernel serves images (*Conditional*, §10; tracked in
  `ROADMAP.md`), the slot MUST render a **deterministic generated fallback**
  (seeded from the action's id/owner/kind, palette-constrained, produced
  server-side as inline SVG — no external fetch, no `style=`, CSP-safe). When a
  kernel image lands it MUST override the fallback. The media is decorative
  counterparty content: kernel-captured chrome (ref, price, reputation) MUST
  render **outside** the media box (§8).
* Price MUST appear on each row. Sort and filter by price MUST be available.
* **Discovered results.** `sys/lookup` fuses the gossip discovery cache for an
  authenticated local caller, so results MAY name a remote action this kernel has
  not resolved: its address names another kernel (`owner@<kernel-key>/name`) and
  the `action_id` is the **remote** id, which resolves to nothing locally. Such a
  card MUST be rendered from the lookup row alone, and MUST show the title, the
  address (owner segment unlinked, §6.2 refs), the description, and a quiet chip
  marking it as discovered, distinct from the remote-proxy chip. It MUST NOT show
  a reputation strip (the discovery cache stores none). Its link is the resolve
  entry, not a page built from the row.
  **In the catalog, a row is discovered iff the kernel marks it so**
  (`discovered: true`, kernel 034426e); the browse catalog renders it in this
  form. **For a lookup hit, a row is discovered iff its `action_id` names no
  local action** — the UI
  attempts the ordinary read and falls back when it 404s; a row that fails the
  read is discovered iff its address is another kernel's. The address alone
  MUST NOT be the test: once resolved, a proxy keeps its remote address *and*
  gains a local row with a record of its own, so reading discovery off the
  address would strip a card the buyer has already paid for down to a
  stranger's.
* **`observed_at` marks a remote-backed row.** The kernel serves it on any row
  whose terms were last verified against their home kernel — a gossip
  observation, or a proxy's reconcile — and omits it on a local action. Where a
  row carries it, the card MUST state that freshness quietly (the age, in words),
  since it is the only evidence of how current a remote quote is. It is the
  served fact that replaces reading provenance out of the ref.
  A lookup row also carries the **hosting kernel's** contact timestamps
  (`last_seen`, `last_contact_failed_at`). The card MUST NOT render them: they
  describe the kernel, not the quote, and a per-action availability signal is
  exactly what §6.6 retired — a card that says "last reached an hour ago" is
  read as "this will fail", which the kernel declines to claim and the UI must
  not imply. They belong on the operator's roster (§6.6), where a kernel is the
  subject. A remote action is offered, called, and its outcome reported.
* **Every result states a price, including a discovered one.** The kernel serves
  `price` on every `sys/lookup` row — an action's own price locally, and for a
  discovered hit its `serving_price` marked up by the current import fee. A
  discovered price is **indicative**: it comes from the gossip catalog, refreshed
  each discovery pass, and the binding figure is fixed when the action resolves.
  It MUST therefore be marked as indicative by a quiet visual treatment **and an
  accessible label** — never by weight alone, which a screen reader cannot
  perceive — and MUST NOT be presented as the amount a spend button authorizes
  unless that spend pins it (§6.2 quote pinning). A blank where a price belongs
  is not permitted: the kernel serves one for every row.
* Catalog browse (`GET /v1/actions`, the `?owner=` filter) MUST be available as
  the secondary path and the per-owner listing.
* On `ErrInvalidState` from `sys/lookup`, Discover MUST fall back to catalog
  search and MUST show a notice that semantic search is unavailable; it MUST NOT
  present a dead control. Discovery is mandatory regardless of embedder status.

**Action page.**

* MUST show a hero pairing the media slot (§6.2 media) with the title as the
  heading, the ref (owner block) as its subtitle, the reputation strip, and price in large type, then description and two schema
  panels rendered from `input_schema`/`output_schema` as field lists (name, type,
  required, description), with raw JSON behind a toggle.
* The reputation strip MUST show four metrics — runs, success rate, rating, mean
  latency — each with an icon, value, and a tooltip that is also the icon's
  `aria-label`. The rating MUST show mean and count. The strip is this kernel's
  own record, the detail read's `evidence.local_experience`; the kernel serves
  no separate stats route, and a list row carries no evidence, so a card reads
  its action's detail.
* **What other kernels report renders beside it, never inside it.** The same
  evidence block carries `provider_reported` (the provider's own account,
  absent when the provider is this kernel) and `observed_by_others`. The page
  MUST render them as one table through the one evidence partial the peer
  inspection uses (§6.6): one report per row, in the strip's own cells —
  success with its run count, rating with its rater count, latency from
  `avg_latency_ms` — and the source last, `provider` for the provider's own
  report (first) and an issuer's key otherwise. The kernel's further counts
  ride in the cells' titles: on success, the uses the provider's receipts
  corroborate; on rating, the ratings no trade backs (unverified). Trades the
  source reported two ways (contradictions) count for nothing and mark the
  source with their number. Nothing is summed, scored or coloured, and a page
  with no reports has no section. In the panel the table is folded under
  **Evidence**.
* Ratings MUST render as reviews (0/1 value, `note`, newest first, paginated),
  labeled as ratings from paid calls.
* The owner of an action address MUST be visually emphasized and, when the
  action is local (§4), link to the owner's profile at `/users/{address}`
  (public actions, aggregate stats, account age). Another kernel's owner renders
  in the same emphasized monospace but MUST NOT link anywhere: there is no local
  profile behind it (§4; extends §8's inert counterparty rule). Card-art hue
  keys off the owner's address; 0.10.0 reshuffles it once, and no compatibility
  shim is permitted.

**Run form.**

* The action page has ONE anatomy: hero → description → an **inline run form**
  (on the page, never a modal) → the result as a card **attached directly below
  the form** → ratings. There is no floating/opener control and no inert
  Method/URL block (an HTTP action's source is editable by its owner inside the
  Edit modal only).
* Inputs MUST be generated from `input_schema` (typed fields, required markers,
  descriptions as help text, client-side hints only; the kernel validates). A
  raw-JSON input MUST be available. The schema arrives in the kernel's
  canonical form, keys sorted, so fields render in that order. A nullable field
  (`type: [T, "null"]`) MUST render as its typed input plus an explicit `null`
  choice: a blank field is **omitted**, a value is sent, and `null` is sent only
  when chosen — never derived from a blank. A property's `title` labels it
  beside its key; `minimum`/`maximum`/`minLength`/`maxLength` become the
  input's bounds; `default` is the input's placeholder, never its value (it is
  an annotation no validator inserts, and a pre-filled value would be submitted
  untyped); a select (an `enum` or a boolean) leads with a blank choice unless
  it is required and already holds a value, so a row left untouched stays blank
  rather than sending the first option nobody chose; `format` `date`, `email`
  and `uri` set the input type (`date-time`
  is not mapped: `datetime-local` carries no zone and fails RFC 3339); an
  integer's exclusive bound becomes the next inclusive one. Nothing else is
  mapped (`multipleOf` would misread HTML's `step`, whose base is `min`;
  `pattern` is anchored differently in HTML).
* **The form is the schema's tree.** A nested object renders as a group of its
  properties that folds behind its label, open while a field it demands is
  still empty: a required key counts once its object is sent — the root, a
  required child of a sent object, or any object holding a value — and a row
  once it is under the array's `minItems`, a demanded row holding nothing
  being empty whatever its item demands; an array as rows of its item,
  `max(minItems, 1)` to start, with Add and Remove, an element never folding; a node the schema leaves open (`{}`, an
  object without `properties`, an array without `items`) as an empty JSON
  editor whose placeholder is the shape, its text sent as the JSON it holds. A field is named by its JSON Pointer
  (`f_/messages/0/content`), its type by `t_<path>` — a container's hint says
  how its children nest — its null choice by `n_<path>`, which nulls the
  whole subtree, and a required container by `r_<path>`. The args are rebuilt
  from the form alone, with no schema read at submit: an empty container is
  omitted, a row left blank is dropped and the indices compact, except that a
  required list or object of an object being sent is sent even when nothing in
  it was filled — `[]`, or `{}` holding its own required containers — since
  absence is invalid for it. An optional property's `""`, `[]` or `{}` is a
  value distinct from its absence (kernel D4), which a blank input cannot
  carry, so it is typed in JSON mode. A closed object with no properties is a
  confirm alone.
* **The JSON switch converts, exactly or not at all.** Switching turns what is
  on screen into the other view, through the server's own transforms: form →
  JSON is the args a submit would send (`POST /form/json`), JSON → form the
  form's own fields filled from the editor (`POST /actions/{id}/form`,
  `/inbox/{id}/form`). A switch never changes the value: when the form cannot
  hold the JSON exactly — a key it has no field for, a value of another kind
  than its field's (`"5"` for an integer), a value outside its choices, a null
  where none is admitted, or an empty value a blank control would drop — the
  view stays and each such value is named under the switch by its pointer. A
  typed password never enters the JSON, so it holds the switch until it is
  saved or cleared.
* **A run MUST pin the quote it was shown.** Every action read and every
  `sys/lookup` row carries `quote_hash`, a fingerprint of the quoted terms
  (price, title, description, schemas, effect). The form carries it as a hidden field
  and the handler MUST read it back **from the request** — never from a fresh
  read of the action, which would always match and silently disable the
  protection. If the terms moved, the kernel refuses with `terms_changed` before
  locking anything, at zero charge; the UI re-renders the form pinned to the
  terms just quoted, so accepting them is one more click (§6.2 outcomes). There
  is ONE run surface, by id, and it pins.
* **A discovered action resolves on open.** Its card links to a resolve
  (`GET /v1/actions?ref=`), which asks its home kernel for the signed contract
  and caches a proxy — a pure read: one round trip, no spend. The buyer then
  lands on the ordinary action page, quoting terms verified moments ago rather
  than a catalog row that may be a discovery interval stale, and the pin protects
  the gap between that page and the run. A reference that resolves to nothing,
  or whose kernel refuses or cannot be reached, returns to Discover with that
  kernel's own sentence (§6.2 outcomes) — never a page built from gossip.
* **A retry is offered only where the kernel marks the call as unrun.** When a
  cached proxy is stale against its peer, the kernel answers `execution_failed`
  carrying `meta.retry = "refresh"`, having charged nothing and re-resolved the
  proxy; that marker is the kernel's own statement that nothing executed, so the
  UI re-reads the action, re-pins the form to the terms that now hold, and lets
  one click re-run (§6.2 outcomes, *refreshed*). Without the marker an
  `execution_failed` is indistinguishable from a call that genuinely executed
  and failed, and the UI MUST NOT re-run it, automatically or by offering a
  one-click retry: repeating it could repeat a side effect or a payment. The
  discriminator is the kernel's field, never the action's kind.
* A **paid** run's confirm is the single money button reading `Run (N)` (the
  accent conveys credits — no glyph on the button), with no remark beside it. A
  **first payment** to this
  owner MUST show a quiet first-payee hint (§8.3). A **free** run is the same
  form with a neutral `Run` button.
* The receipt MUST offer one-click re-run with the same args.

```text
  lena@alpha/ocr-translate          0.012 USDC
  ─────────────────────────────────────────────
  document_url   [https://…            ]
  target_lang    [German             ▾ ]

  [ Run (0.012 USDC) ]
  ┌─ result ──────────────────────────────────┐
  │  [view of the result]         Receipt · ▲ ▼ │
  └────────────────────────────────────────────┘
```

**Outcomes.** Each outcome class MUST render distinctly. The classes divide on
one question — what happened to the money — so no two may share a sentence:

* input-schema rejection → "Not sent. You were not charged"; field-level errors
  inline on the sheet; no receipt.
* failure → "Failed — fully refunded" when the call drew nothing, and
  "Failed — N charged" when it drew something: sub-work already delivered stays
  paid, and the kernel states the draw in `meta.charge` beside the failure
  transaction in `meta.tx_id`. The card links that receipt. **The sentence is the UI's, never the kernel's
  string:** since v0.12.14 the kernel classifies rather than narrates (a remote
  failure answers `execution_failed`), and a class explains nothing to a buyer.
  Each error class maps to exactly one sentence, in one place; a raw class MUST
  NOT reach a template (enforced by drift guard).
* **parked** (`timeout`) → "Result pending — N reserved", N through the money
  helper (§7). A call that was
  dispatched but has no signed outcome yet is **not** a failure and **not** a
  refund: the kernel keeps the funds locked and retries. Verified live — the
  balance moves from available to locked, and `meta` carries `process_id` and
  `pending_since`. Only the peer's signed answer settles it; no age expires it,
  so no date is stated. The card MUST carry the escrow-pending signal (§7), name
  the reserved amount exactly (§7), and link to the process it
  names. It MUST NOT claim a refund has happened or will, and MUST NOT poll the
  peer (§6.6).
* **refreshed** (`execution_failed` with `meta.retry = "refresh"`) → "The
  provider changed this action — you were not charged". Nothing ran and nothing
  moved, so it is a refusal, not a failure. The UI re-reads the action and
  re-pins the form so one more click accepts the new terms — the same recovery
  as a refused quote.
* **peer refused** (`unauthorized` with `meta.peer`) → "Kernel *peer* declined
  this call". An operator relationship fact — the peer suspended this kernel or
  will not serve that action — which no retry fixes and which MUST NOT render as
  the buyer's own lack of permission. The kernel deactivates the cached proxy on
  such a refusal, so the action leaves the catalog; that absence is ordinary
  kernel-managed cache state (§6.6) and MUST NOT be reported as a deleted action.
* refused-before-execution → "Not sent — you were not charged". A `409` is
  checked before funds are locked, so nothing ran and nothing moved; it MUST NOT
  be dressed as a failure with a refund, which would claim a movement that never
  happened.
* refused quote (`terms_changed`) → "Terms changed — you were not charged",
  naming the price the kernel now quotes, with the form re-pinned so one more
  click accepts the new terms (§6.2 quote pinning). Not a failure: nothing was
  locked, so nothing was refunded.
* success → no sentence: the panel's styling says it, and it holds the result
  as the view (§7), its JSON behind the JSON toggle and a Download of the
  transaction's result; then, below a rule, a receipt
  summary (gross paid / net to provider / fee captioned "on value added"); an
  inline rate control (0/1 + optional note).

Long-running calls MUST show elapsed time against the action's historical latency
and MUST NOT show an indefinite spinner. If a call returns with the process still
open (parked tasks), the result MUST say so and link to the Wallet.

### 6.3 task — the inbox of work addressed to you

* A waiting task whose `required_caller` is the signed-in user MUST render as a
  completable card; all other tasks MUST go to a history table, read with
  `?status=` from its tabs or `?all=1` (the kernel lists only open tasks
  otherwise), to exhaustion. A single task is `GET /v1/tasks/{id}`.
* **Who asks** is what the row names. A row carries `requester`, the party the
  task acts for and as whom its completion runs (kernel D6), named first; then
  `created_by` (the creating action) and `action` (the completion target) when
  this kernel holds the task; a task held on another kernel is listed here
  without them, its `owner` the holding kernel's name, rendered as every kernel
  name is (§6.6: a petname whole, a key truncated). The card names what the row
  names and nothing else.
* **Requesters (kernel U52).** A task from a requester the user has not
  approved is withheld: it is not listed, and read by id it is not found. The
  inbox MUST list `GET /v1/me/requesters` to exhaustion under "Requesters",
  pending first, then approved, then blocked, each row naming the address, the
  status and the date since, with the decisions that change its status
  (`POST /v1/me/requesters/{address}/{approve|revoke|block}`): `Approve`
  (primary), `Revoke` (secondary; back to pending, its tasks withheld again)
  and `Block` (destructive, behind the plain confirm "Block {address}?", since
  it declines the requester's waiting tasks and refuses new ones). A requester
  has no content to show and no page of its own, so its row is where its
  decisions stand. The status pill is neutral: it is no money state.
* The completion form MUST be built from the task's own `allowed_input` (the
  kernel-derived completion schema on a waiting task), never by reading the
  target action: a task may be parked on a `private`/`local` action the
  completer can never read.
* `partial_args` MUST render read-only; only `allowed_input` fields MUST be
  editable, in the fields and in the JSON editor alike: the form is built from
  the schema without the sealed properties. When none are editable, the card
  MUST collapse to a single confirm.
* **Completion is neutral — this is the single statement of the rule; §7 and
  §8 defer here.** Answering a task spends nothing of the person who answers
  it (kernel U52): its price was parked by whoever created it, and a transfer
  task's value was reserved from its requester at creation and is paid on
  completion. The completion control is therefore **`Complete`, never a money
  button**, naming **no** amount (the form's primary, in solid ink, §7), with no funds-mechanics narration — a `verb (N)` money
  button here would state a spend that does not happen. The card names only
  *who asks* and *what* and shows the sealed `partial_args` as an inert
  read-only detail. No action is read to decide how a task renders.
* Completion MUST POST to `/v1/tasks/{id}/complete` with `args` (default `{}`)
  and MUST render `{result, tx_id, trace_id, receipt_id, task_id}` through the
  run outcome display (§6.2). **A receipt is linked only when the reply names
  one**: a task held on another kernel completes there, and its reply carries an
  empty `receipt_id` and that kernel's `tx_id`, which this kernel does not
  serve. Completion MUST be offered only to the `required_caller`; duplicate and
  non-caller attempts MUST map to typed-error messages.
* **A waiting task can be declined** (`POST /v1/tasks/{id}/cancel`) by either
  party the kernel accepts: its `required_caller`, or its `owner` (the process
  owner who asked). Declining cancels it and returns its reserved price to the
  payer; the control is the destructive `Decline`, standing in the task's command
  row beside `Complete`, behind the plain confirm "Decline this task?" (§7),
  offered on the inbox card and on the task's own page, never in a list row. The kernel refuses anyone else and any task no longer waiting.
* Completed and cancelled tasks MUST appear in history.
* `sys/message` tasks MUST render as a message (sender, text, acknowledge
  button).
* Task creation MUST NOT be a browser form. `POST /v1/tasks` binds a live
  funding trace, and a trace id is exactly the process bookkeeping this UI keeps
  hidden (§6.5) — a form demanding one is unusable by the person the inbox is
  for. Tasks are composed by actions and app modules through the kernel API; the
  inbox is work addressed to you: list, read, complete, decline.

### 6.4 action — catalog and provider workspace

* Workshop MUST present, in order: what is parked, action list, creation zone. Its
  action list is this kernel's actions (`?owner=` the viewer, or the whole
  catalog for the superuser); a discovered row is not one to manage and MUST be
  left out.
* Workshop MUST NOT read the user's transactions: no kernel figure totals a person's
  earnings, and a fold would read every transaction on every load. What is parked,
  like every headline money figure (wallet, home, a session), MUST render through
  the one figures component: one block per figure (§7). The BFF MUST NOT reproduce
  kernel settlement.
* The action list MUST use `GET /v1/actions?owner=<self>`, include inactive
  actions, filter by state, and show kind, price, success rate, rating trend, an
  inline active/inactive toggle, and a **visibility**
  control (below).
* **Visibility** MUST be set via `PUT /v1/actions` with
  `{target, visibility: "private" | "local" | "public"}` — the kernel's three-way
  caller-scope (`private` owner-only, `local` any local caller of this kernel but
  never peers, `public` anyone and the only value served in manifests/gossip). It
  is a **three-way** selector, not a boolean toggle. No ACL, grant, or revoke
  operations exist; a card MUST show a quiet chip only for the non-default
  narrowing (`private`/`local`), nothing for `public`. Widening an OpenAPI-imported
  action beyond `private` may require verified ownership; the UI surfaces the
  kernel's rejection (§10) rather than driving a proof.
* Saving a source, schema, kind or price change deactivates the action until
  re-enabled (the kernel's contract-drift rule); a title or description change
  keeps it live. The action's state shows the result; the dialog carries no
  remark about it.
* Create and edit MUST take the action's **title** (required, one line, at most
  80 characters, the kernel's rule). A refused create or edit — a local parse
  error or the kernel's refusal — MUST re-render the dialog open with the
  message inside it and everything typed kept, except a secret: a secret never
  reaches a page, so its input comes back empty and required.
* For `kind=wasm`, the UI MUST surface compile failure on enable as a typed build
  error.
* Source-URL input MAY be preflighted for obvious non-HTTP(S)/loopback cases; the
  UI MUST surface the kernel's URL rejection verbatim and MUST NOT reimplement the
  policy.
* The creation zone MUST offer manual creation: `POST /v1/actions` (inactive by
  default), with the schema-driven editors used elsewhere, schema linting as
  hints only, and WASM build errors on enable. Upstream auth (`auth`) MUST follow
  the same write-only treatment as the import wizard: secret fields are masked
  inputs, never pre-populated. There is no synthesis path: `sys/make` is not a
  kernel action (§10), and no control may be offered that the kernel cannot
  serve.
* OpenAPI import MUST be a wizard: spec URL → preview table of importable
  operations with per-operation validation messages → `POST /v1/actions/import`
  (idempotent) → created/updated/deactivated results. A refused operation is
  named with its place in the document (`METHOD /path`) and the kernel's reason;
  an installed operation the kernel adjusted to its schema subset is named with
  the kernel's note. Reimport MUST be presented
  as "changed → deactivated; re-verify and re-enable." There is no unimport: an
  application is switched off or removed with the ordinary verbs over its path
  (`target: owner/name`), which is what the kernel serves. Upstream credentials (`auth_json`) MUST be entered in write-only
  fields, never re-displayed, shown afterward only as "configured ✓ (replace)".
  *Conditional:* a UI-driven ownership-proof flow for public activation; until
  then the UI MUST surface the kernel's rejection rather than drive a proof.

### 6.5 user & tx — money, ledger, and receipts

* The Wallet MUST show two balances (credited / escrow-pending signals):
  **available** and **parked** (all locked funds; the open-sessions list itemizes
  them, each with the `End · refund N` control behind the plain confirm "End this
  session?" — ending cancels waiting tasks and refunds parked prices — via
  `POST /v1/processes/{id}/end`). "locked" is
  a user-facing label only for the operator (below). Parked is the served `locked` whole: it also holds what a
  call to another kernel stakes on its ticket (kernel P10) and the values the
  user's waiting transfer tasks reserve (kernel D6), which no session itemizes,
  and the UI states the total rather than computing a remainder. The control
  states a refund only when there is one (`End` alone on a session holding
  nothing, which still releases its waiting tasks), and is one component
  wherever a session is listed or shown.
* **The operator's locked is not parked.** `sys` is the clearing account: each
  payout's first leg moves the amount to sys, where it is in transit on the
  rail until the payment is final (`sys.pending_payouts`). So the operator's
  `locked` mixes escrow and transit, and the UI cannot split it without
  arithmetic. For a superuser the figure is labelled **locked**, the kernel's
  word, without the escrow signal — on the wallet, the dashboard and the
  workshop alike; the network page's `in transit` figure itemises the transit
  part. Every other account sees **parked**.
* **The wallet and the dashboard are the viewer's own money.** They list the
  viewer's own open sessions — a superuser is served every account's — the
  dashboard taking its five after that filter; the sessions page is where every
  account's are read.
* **A session awaiting a peer's receipt is not force-endable.** The kernel
  refuses to end it (409), because the peer may already have earned the funds,
  so the control MUST be withheld wherever it appears — the wallet, the sessions
  page and the process page — keyed on the kernel's `awaiting_receipt`. A
  doomed act is never offered.
* The wallet MUST link to the full **sessions** page (`GET /v1/processes?all=1`,
  open + closed — the kernel lists only open ones otherwise), which itself MUST offer the same `End · refund N` control on each endable
  row and paginate (§7). A session awaiting a remote receipt MUST show that
  state and its age from the kernel's `awaiting_receipt`/`awaiting_receipt_since`
  — the standing evidence behind a parked run (§6.2).
* **The ledger is the activity.** The kernel journals every movement between
  two accounts (`GET /v1/ledger`): deposits, withdrawals, transfers, value
  delivered by a `transfer`-effect call, and what each settlement moved — the
  provider's payout, the operator's fee, and an obligation (verified live on
  d59742b). An account's balance is the sum of its entries, so the entries alone
  are the account's activity. Each row states the counterpart from the viewer's
  side (`to vendor`, `from sys`), the signed amount, and the time; an absent
  side is money crossing in or out of Juice and renders as `outside Juice`
  (`to outside Juice · fuel`). An entry from the viewer to the viewer moves
  nothing — only the operator has them: a deposit credited to `sys`, and the
  first leg of its own withdrawal — so it names the account alone and its
  amount carries no sign. The wallet shows the most recent
  10; the call history is the transactions page.
* **A reason is an id or it is text, and never prose the UI writes.** An
  entry's `reason` names the transaction that caused it — linked to its
  receipt, truncated — or, for an obligation, the ticket it settles
  (`obligation <ticket>`), or, for a transfer to or from another kernel, the
  sender's transaction (`transfer <tx>`), linked only for the account it was
  debited from: on the receiving kernel the id is the other kernel's. Anything
  else renders as the inert text it is. A movement caused by a call to
  which the viewer is no party (a value transfer's beneficiary) links to a
  receipt the kernel may refuse them; the kernel decides.
* **A value-bearing call is two records.** The transaction states what the
  *call* cost — zero for a price-0 action — and the entry states what *moved*.
  The UI MUST NOT reconcile one against the other, and MUST NOT manufacture a
  money figure out of a transaction's `args` — a displayed amount is a
  kernel-vouched fact or it is not displayed (§7); the entry is that fact.
  Where the kernel additionally serves `value`/`value_to` on a transaction,
  they render as breakdown rows, zero-suppressed, and the payer named beside
  them is the transaction's **`caller`** — the immediate caller, whose
  balance the value actually leaves — never the `owner` that pays for
  execution. The two differ exactly when a transfer task settles inside
  someone else's process (§6.3).
* **The person's own crypto wallet does the chain work.** The UI holds no key,
  runs no relayer and keeps no payment state; every figure it shows is one the
  kernel serves, so the crypto wallet's on-chain balance is not shown and no QR
  code is drawn (the wallet has both). The words are a person's: the **crypto
  wallet** is theirs (MetaMask or similar), the **Juice balance** is this one,
  and the two acts are **Deposit** and **Withdraw** everywhere — title,
  buttons, dialogs, confirms.
  * **Deposits and withdrawals** is the wallet's one explained block, and the
    only place the copy discipline admits explanation (grammar §3). First a
    warning banner with the alert icon, in plain words: "Only send money from
    your crypto wallet, never from an exchange or any other wallet. Money from
    anywhere else won't show up in your balance. Withdrawals are always paid
    back to that same wallet." — a payment from an exchange is held for the
    operator, a loss nothing on screen would otherwise show. Then three rows:
    **Your crypto address** (or "Not connected yet."), **Juice deposit
    address** — the server's, shared by every account, so never labelled
    "yours" — and **Token**, by symbol and contract. Then one row of commands:
    **Change address** (or **Connect crypto wallet**), **Deposit** and
    **Withdraw**, neutral since they open a dialog.
  * **Register:** the wallet signs, EIP-191, the kernel's registration message —
    `juice address registration`, `kernel: <public key>`, `user: <account id>`,
    `address: <address>`, four lines, no trailing newline — with the address
    exactly as the request sends it. Without JavaScript the page shows that
    message for a typed address, to be signed elsewhere and pasted.
  * **Deposit** is offered only on a world whose
    chain the UI has proved: `/health` serves no chain id, so the UI recomputes
    `sha256(JCS({chain_id, name, rail, token}))` for the chains of the shipped
    worlds and accepts one only on an exact match with `network_fingerprint`.
    Its dialog states From (your crypto wallet, the registered address) and
    To (your Juice balance); its review names the amount, and the confirm fetches the
    ERC-20 transfer built from a fresh read of the registered address, picks
    the installed wallet already connected to that address (listing the
    wallets only when none is), and asks it to send on that chain from that
    address. A hash is a submission, announced "Transaction submitted", never
    a credit; a reload never reopens a spent review.
  * **Withdraw** states From (your Juice balance, what is available) and To
    (your crypto wallet, where the world has addresses);
    its review names the amount, and its confirm carries the id minted for it.
  * An outcome names a cause only where the wallet's error establishes one
    (EIP-1193 4001 declined, 4902 unknown chain); any other refusal is "Not
    sent."
* **Registering a payout address takes the account's password**
  (`PUT /v1/me/blockchain-address {blockchain_address, signature,
  current_password}`), as a password change does. A wrong or missing one is the
  kernel's 401, checked before anything else; it MUST read as "Current password
  is incorrect." and MUST NOT end the session, which a 401 elsewhere does.
* **Every world pays out.** A withdrawal is offered wherever it has somewhere
  to go: on a world without addresses the operator records it (it lands
  `confirmed`, `tx_hash` `manual:<id>`), and on a world with addresses it waits
  for the user's registered address. Only the address form depends on the
  world. The withdrawals table is read on every world, to exhaustion. A row's
  `reason` is the kernel's — why a payout is blocked, e.g. its vault short of
  the native coin — and renders under its status as the text it is.
* **Sending money is running an action; it gets no surface of its own.** The
  kernel moves value through `sys/transfer`, an ordinary priced action with no
  special privileges (kernel §9: any provider could have supplied an equivalent),
  so the UI MUST NOT carry a bespoke send form, route, command, or price helper
  for it. It is found, priced, quoted, run, and receipted exactly like every
  other action, through the surfaces of §6.2. It reaches a user of any kernel;
  a transfer to another kernel's user carries `payment` on its transaction —
  the state of this kernel's payment to that kernel (`pending`, `announced`),
  which says the money left and never that the recipient was credited — and
  the receipt states it as served, in a neutral pill.

  **No action may be special-cased anywhere in the UI.** A clause keyed to one
  action's name is a defect, not a feature: it makes the interface disagree with
  the platform it presents, and it multiplies — one exception invites the next.
  Where behaviour genuinely differs, it MUST key on a kernel-served field the
  action carries (`effect`, `kind`, `requires_grant`), never on a ref.

  The rule bites on how an action **renders and spends**. It does not forbid the
  UI naming a native to **power a surface** — search runs `sys/lookup`, the
  proposal runs `sys/llm/decide` — because there the native is the mechanism
  behind a feature, not a marketplace row given privileges. The distinction is
  the one the 0.6.1 retraction drew: `sys/transfer` lost its bespoke form
  because sending money is running an action; search did not lose its native
  because ranking is not.
* The transaction list (`GET /v1/transactions`) MUST return rows where the user
  is buyer, caller, or seller. The kernel renders each party as an address —
  `owner`, `caller`, `target` — and returns no raw user id, so the UI compares
  addresses. Each row MUST show the user's role, the action (the transaction's
  own `action` address, captured with the call, so no action is re-read and a
  deleted one still names itself), status, and cost breakdown.
* The receipt page MUST show parties (buyer/seller, viewer's role highlighted),
  action ref, args and result as views (§7, the result with its
  Download: `GET /transactions/{id}/result.json`, served as an attachment),
  gross/fee/net (fee captioned "on value
  added"), failure `reason` when present, and the buyer's rating block.
  *Conditional:* where the kernel serves them, the value and federation economics
  fields (`premium`, `value`, `value_to`) render as additional breakdown rows,
  zero-suppressed — absent fields render nothing. `value_premium` is not among
  them: with value local to a kernel it was deleted from the receipt entirely
  (kernel §10, migration 044).
* A Verify control MUST call `GET /v1/transactions/{id}/receipt-verification` and
  render per-check results as a checklist (signature, hash, settlement
  arithmetic) plus the top-level `valid` flag, for both success and failure.
  Verification failure MUST name the failed check. The UI MUST NOT display `valid`
  except from the kernel's own per-check response.
* The buyer MAY rate a transaction once (`POST .../rate`, 0/1, optional `note`);
  the control MUST appear only for the buyer and only when unrated.
* Balance views MUST state how money enters on this world (§6.5 pay-in), and
  MUST NOT claim a mechanism the world does not have.
* *Conditional:* an Engine-room disclosure below the receipt showing the trace
  tree with per-call cost and `latency_ms`.

### 6.6 network & admin — federation money

Superuser-only (`sys`), an ordinary client of the kernel's `/v1/admin/*` surface
(§3.1). Two concerns live here: the roster, and the money owed to this kernel.

**Two namespaces, never guessed between.** The kernel serves users and peers on
separate routes and refuses a target of the other kind, so a petname that
happens to equal a handle never decides which account is acted on. The UI MUST
therefore know which namespace it is addressing before it dials: a user is
reached under `/admin/users/{handle}`, a kernel under `/admin/peers/{key}`, and
no page sends a public key to the user routes. This is why the two pages, the
two rename forms and the two suspend verbs are separate here — not for
symmetry, but because the kernel refuses anything else.

**Peers.**

* **The roster is the whole known network, as one list.** `/v1/admin/peers`
  merges by public key, excluding this kernel, **counterparties** (`traded`: a
  transaction here names the kernel) and **discovered peers** (a catalog:
  action count). The
  page lists **this kernel first, then its counterparties, then the known
  network**, each class in the kernel's own order, as one paged list in the
  action row's grammar (§6.2) — tile, title line, key, `about`, strip — and
  **not polled**. A class is marked by a neutral pill on the title line:
  `this kernel` on this kernel, `party` on a counterparty; a known kernel
  carries no mark. This kernel's title is its own handle, and its row links
  nowhere, because it is this page; a peer's row opens its page. The identity
  this kernel's row needs is read before the page renders: a failed read is an
  error, never a blank row.
  **The key renders in one spelling.** The abbreviation exists for matching,
  so a row never shows two of them: an unbound kernel's title is the
  abbreviated key and the row has no ref line; a named kernel's ref line is
  that same abbreviation. The whole key is yielded by the one **copy control**
  beside either (§7), because every command that takes a kernel takes the
  whole key.
  **A peer row's facts are a strip**, in the action strip's grammar (§6.2):
  icon and value per slot, the meaning in the tooltip, slots in one fixed
  order so rows compare down the column — the catalog count (both classes),
  then the kernel's **contact cache**: `last_seen` (when a verified exchange
  last succeeded — catalog freshness) and `last_contact_failed_at` (when one
  last failed). A slot with nothing to state renders a muted `none`, which is
  the strip's positional convention and not the placeholder forbidden below.
  The two timestamps are dated facts and never a status pill, because the
  kernel states neither as a verdict: it holds two timestamps that only ever
  move forward, and it neither hides nor reorders a peer for having failed.
  The UI therefore reports them and draws no conclusion about whether a peer
  is up — the call-and-see rule below is unchanged. This kernel's row carries
  no strip: the roster states nothing about it. Every absent prose field —
  `about`, `petname` — is omitted, never rendered as an empty string or a
  placeholder.
  **A row carries a verb only where its gap is visible.** A row titled by its
  key carries one verb, `Name` — the same button as on the kernel's page —
  into its page with the rename form open; a named row and this kernel's row
  carry none, as a result row carries none.

* **A peer row carries no money.** A peer holds no account here at all
  (kernel v0.15.11): a foreign call is funded by the serving kernel and every
  obligation settles per call on the rail (kernel §D14, §P10).
  So a peer row states its names and its strip, and nothing else. A balance, a
  position or a credit figure here would be a fabrication.
* **This kernel's own credit position is the money fact on this page.** It
  renders through the figures (§5.6 of the grammar), above the list: what this
  kernel has delivered to foreign buyers and not been paid for, against the limit it set (`exposure` of `credit_limit`); the
  rates it serves under (`fee_bps`, `remote_bps`, `import_bps`), its ticket face
  and the largest ticket it will take from a buyer (`lottery_max`, above which a
  call is refused with a signed rejection); the operator split of `sys` into earnings and money in transit; and the
  solvency identity, whose `gap` MUST carry a danger pill when it is non-zero.
  A rail halt (`stop`) MUST render as a warning banner, led by `#alert`, naming
  its reason, on this page because the money verbs it refuses are here. Where
  the world has addresses, the page states what the vault held at the last
  finalized block (`finalized`): the token in its unit and the native coin its
  payouts spend in fees, as the kernel writes it, named by the chain's coin
  (POL on Polygon) — a snapshot at that block, never a live balance.
* **There is no subscription.** A peer row comes into being by use — a first
  inbound call, or an outbound call that resolves and caches a proxy (peering is
  handshake-free). The page therefore offers **no subscribe or unsubscribe
  affordance**, and **no funding gesture at all**: a deposit credits a user, the
  kernel serves no route that credits a peer, and a peer key sent to the user
  route is refused as not a user. The durable lever is suspend, and a peer no
  longer called lapses through the kernel's retention.
* **Naming is a petname system** (kernel §13, after Stiegler). A kernel has
  three names and they are not interchangeable, so the UI MUST keep them apart:
  * its **public key** — global, self-certifying, unreadable. The only thing
    every URL, form and mutation keys on. It renders **truncated**, with the
    whole value yielded by its copy control (§7), never as a card title in
    full. This
    holds wherever a kernel is named, including inside a sentence: a failure
    naming a peer (§6.2) carries whichever name the kernel had to hand —
    a petname where the failing path knew one, the key otherwise — so the UI
    abbreviates a key-shaped name before printing it. The three name classes
    are disjoint by shape, so nothing configures this. The abbreviation MUST
    have exactly one definition: a truncated key exists so a person can match
    one against another, which two spellings of the same key defeat.
  * its **nickname** — what the kernel calls *itself*. Self-asserted, collides
    freely, and **never resolves**, so it is counterparty content under §8:
    inert, never a link, and never offered where a name that resolves is
    expected. It MAY be shown as the claim it is.
  * its **petname** — the name bound *here*. Unique here, and the only friendly
    name that resolves. It renders whole, in the provenance face.
  An unbound kernel therefore shows its truncated key and stays callable by it;
  it MUST NOT borrow the nickname to fill the gap.
* **Name and suspend live on the kernel's own page.** Naming binds a petname: the kernel binds one
  automatically only on this kernel's own first outbound use, so an explicit
  bind is the only way to name a kernel merely discovered, and without it the
  operator is left typing a key. Suspend freezes the **kernel row** and makes
  an unknown key known, so it is offered on every kernel's page, and returns to
  that page; the page reads the row (`GET /v1/admin/peers/{key}`) to say
  whether the kernel is suspended, since the inspection never does, and a key
  this kernel has never learned has no row, which is a usable page and not an
  error. **Inspect a
  kernel by key** remains the way in for a key learned elsewhere (verified live:
  an unknown key still returns a usable page).
* **A kernel is named on its own page.** The petname form lives on the inspect
  page, which is where a kernel is shown, and posts the **public key** — an
  unbound kernel has no name to key on, and advertised names collide. A user is
  renamed on the user page. The two forms are separate because the kernel's two
  routes are, and a single form would have to guess which namespace was meant.
  **The form opens prefilled** by the kernel's own auto-bind preference (kernel
  §D15), so an explicit bind and an automatic one look alike: the petname when
  one is bound; else the nickname when it is not key-shaped and free; else
  `k-<key8>`; either suffixed `2`, `3`, … to the lowest free number. Free
  means bound to no other kernel here, suspended parties included, and not
  this kernel's own name, which it binds to its own key. The roster is read
  for that form alone. The kernel remains the validator: a rejection flashes
  its message.
* **The inspect page states present facts, with labels.** A strip compares
  rows down a column; a page has one subject, so it carries no strip and
  states each fact in a labelled `kv`, present facts only: status (online or
  offline, with the path and one ping's round trip, stated once); version,
  without the `juice-kernel/` agent prefix; catalog, the **roster's**
  `actions` count — the inspection's own list is one page (100, verified
  live: arfante inspects as 100 of 193), so it is never counted as the
  catalog, and when it holds fewer than the roster states, the public actions
  list says `N of M shown`; the provider's own report pooled over its actions,
  labelled **Self-reported** because evidence is never a verdict (successes
  over uses, and the count-weighted mean rating, only where it has runs);
  and the **date** the kernel became known here (`first_seen` of its row,
  `GET /v1/admin/peers/{key}`, for every known kernel). Transport protocol strings
  are not shown, and no absent fact renders as a row, a dash or `none`. The
  verbs stand in a `.cluster` inside the card's `.stack`, separated from the
  facts by the stack's step and by nothing else. The header is the row's anatomy without the strip — name with its
  marks, key, `about` — and the card holds the facts and the verbs as buttons,
  Name and Suspend (or Unsuspend), as the user page does. The `party` pill on
  the page is the roster's class.
* **Work addressed to this kernel reaches its operator's inbox.** A peer
  addresses a task to the kernel as `sys@<kernel>`, and it is listed in `sys`'s
  inbox and completed there like any other (§6.3); this page lists none. An
  inspected action is a remote action before resolution, so it is listed as
  the discovered row of the search results (§6.2) — title, `R` chip,
  indicative price, address, description — and its link resolves it. Reachability states the path, one ping's
  round trip, and the peer's version.
* **Evidence, not hearsay.** Inspect MUST render the kernel's retained evidence
  in **two views, never folded**: the **execution summary** (Self-reported,
  above) pooled from the rows the subject issued about itself, and the
  **evidence table** of every report — §6.2's one table, one report per row
  with its source, the subject's own first. Each row names its subject by the
  address the inspection serves, else by its action id cut short with the
  whole behind a copy control (§10). The rating counts stay distinct:
  trade-backed (the two-kernel receipt link holds) in the cell,
  **unverified** in its title, as is the **corroborated** count on success.
  No new colour, no status fill — evidence is a trust fact, not a state. Attribution is provable and honesty is
  not, so the UI MUST NOT present evidence as a verdict or fold it into the
  first-party reputation strip.
* **A cached proxy is kernel-managed state.** Activation is set by resolve and
  cleared by the kernel (verified live: `enable`/`disable` on a `kind=remote_proxy`
  action returns `ErrInvalidState`, "use admin suspend to block a peer"), so the
  UI MUST NOT offer lifecycle verbs — enable/disable, visibility, delete — on a
  proxy row anywhere it lists actions, including the superuser-scoped workshop.
  The peer-level lever is suspend, on the peer's page.
* `peer_unfunded` on a run means **the serving kernel will not serve this one on
  credit** — its limit is reached, or its own provider cannot fund the work, a
  foreign call running on the seller's money — and MUST NOT be rendered as the
  caller's own insufficient funds (§10 error mapping).
* **Availability is never guessed.** The kernel withdrew the `peer_state` hint
  (v0.12.14), and the UI MUST NOT reconstruct one: a remote action is offered
  like any other, the call is attempted, and the kernel reports the outcome.
  A remote action therefore carries no health chip, no status notice in place of
  its run form, and no poller waiting for a peer to recover. The hint was
  unsound in both directions — it read `offline` for an action that had just
  been called successfully, since only a gossip pull refreshed it, and
  `unfunded` is the steady state of every peer relationship funded by the
  serving kernel's exposure headroom rather than prepayment.

**Rename.**

* One modal on the target's own detail page, in the namespace that target
  belongs to: a user at `POST /v1/admin/users/{handle}/rename`, reached from
  `admin`; a kernel at `POST /v1/admin/peers/{key}/rename`, reached from the
  inspect page, from a counterparty's name and from **Name** on a discovered
  kernel. The body field is `new_name` in both. There is no shared form, because
  the kernel refuses a target of the other kind and a shared form would have to
  guess what the kernel declines to guess. Each detail page therefore renders
  one record shape, never one padded with the other's empty fields.
* Its disclosure MUST state the consequence beside the field, and the two
  consequences differ: renaming a **user** frees the old address and carries
  every reference to the new one; naming a **kernel** binds a petname here,
  exactly, and an occupied name is refused. Success MUST land on the page that
  then exists — the new address for a user, the unchanged key for a kernel — and
  a rejection MUST stay put. The new address is sent as typed (§4): the kernel
  refuses `sys`, a bare handle, and another kernel's name, and the UI shows
  that reason.

**What this kernel is owed.** Every cross-kernel debt settles on its own, per
call, by a ticket both kernels name (kernel §P10), and it closes **without an
operator act**: the chain payment where the world has addresses, and the buyer's
own signed reveal where it has none, recorded by this kernel itself. There is no
bilateral settlement to initiate, no settle route, and nothing here to press.

* The page MUST list the obligations owed to this kernel from
  `GET /v1/admin/kernel/deposits`, each row naming the debtor (by petname where
  one is bound, else its truncated key), the ticket, and what the call owed.
  This list is **`exposure` itemised** — a reading of what the kernel is
  carrying, not a worklist.
* A row whose buyer has answered also states the **status** and what the **draw
  decided** must be paid; the two money figures differ, and neither is computed
  here. A row whose buyer has said nothing carries an empty status and a zero
  amount, which are **absent fields**: the UI MUST state that it is awaiting the
  buyer and show no figure. Rendering `0` would name an amount nobody has
  decided, and an empty pill would show a status nobody has stated.
* Each row MUST carry **no gesture**. An obligation is not closed from here, and
  a control that appeared to close one would misdescribe who settles it.
* Payments the rail has witnessed whose sender no account has registered MUST be
  listed too, each naming the **address it came from** — the fact that decides
  whose it is — with an attribute link that prefills the transaction naming it.
  This is the one money act left on the page, and it credits a user.
* The deposit dialog serves the kernel's **two** forms: a user with an amount and
  a reference, and a user with a reference alone, to attribute a payment already
  received. **The reference is mandatory** — the kernel credits nothing it cannot
  name — and the account is named in the route, not the body. It offers no peer
  field: a peer is not a user, and the kernel refuses one here.

**Configuration.** The kernel's settings are its `config.json`, applied by
editing the file and restarting the kernel (kernel manual). The UI, which
supervises this installation's kernel (§1), does both for the operator, on a
page reached from `admin` and from the `admin config` command, both of which
exist only when the kernel is this installation's. The editor is one
component, parameterised by the file's schema, its hidden keys and the keys
it withholds; an agent's `config.json` is its second instance (§6.1).

* The form is the kernel's own JSON Schema of the file,
  `$JUICE_HOME/schemas/config.schema.json`, rendered by the run form's
  components (§6.2) and filled from the file; its JSON switch opens on the file
  itself. The switch converts through the save's own reading of the file
  (`POST /admin/config/json`, `/admin/config/form`): form → JSON is what Save
  would write, its secrets withheld; JSON → form holds the file's own empty
  values, which a save keeps, while a new optional empty value stays in JSON.
* `kernel_handle`, decided at first boot, and `listen_addr`, the port this UI
  finds its kernel by, are read-only. `credentials_key`, the file's secret, and
  `native.llm`, the language models, are in neither view and are kept as
  saved: the models are the Models page's.
* A field cleared is removed from the file, restoring the kernel's default; a
  blank field where the file holds an empty value is no change, since a blank
  input cannot tell the two apart. A form unchanged writes nothing.
* A save is refused if the file changed since the page was read (its SHA-256).
  Otherwise the kernel is stopped by SIGTERM to the process id in its
  `serve.lock`, once the lock shows a server holds it and only for a positive
  id; the file is replaced atomically; and the kernel is served again. If it
  does not answer, the previous file is written back and served, and the
  kernel's last words are shown.

**Models.** The kernel's language models are entries of `native.llm.models`
and four bindings (kernel D17). The **models form** chooses them, one form
with one block per kind the kernel uses: **Chat, JSON and decide**, the chat
model, which the three chat roles share, and **Search**, the embedding model.
The installer's Models step (§1) and the operator's **Models** page render it
alike; the page is reached from `admin` and the registry, and exists only for
this installation's kernel, when its schema describes the entries it writes,
so an older kernel's file is never written there.

* A block is a provider, its address (Other alone), its key and the model. The
  providers are data in the UI, each only an address and a wire protocol: Ollama
  and LM Studio on this machine, OpenAI, Anthropic, OpenRouter, Other (any
  OpenAI-compatible address), and None; Search adds Same as chat. Search offers
  no provider whose protocol serves no embedding model (Anthropic), and the
  server refuses one whatever the form sent. A provider is not a kernel concept,
  and the kernel is not changed.
* The model is typed, with the models the provider lists (`GET <address>/models`,
  asked as the kernel asks it) offered as suggestions. The providers on this
  machine are asked whenever the form is shown, and say how many models they
  serve or that they are not running. **List models** asks the chosen providers
  and shows the form again; it writes nothing. A provider that lists no models
  takes the model as typed.
* A key is write-only: a key held for the provider's address says so in its
  placeholder, `configured ✓ (replace)`, and a blank key keeps it. A key typed
  and not yet saved is typed again.
* The form's command (Next, or **Save and restart**) is refused, with what to do,
  for a block left incomplete, a provider that does not answer, refuses its key
  or does not serve the model, and search on a protocol without embedding models.
* What it writes is the kernel's own shape. The roles a block names are bound to
  its entry, and None leaves them unbound. An entry is reused only when it is the
  same model on the same protocol and address, of the same kind: it keeps its
  name, price and settings and is enabled. A new entry is named after its model,
  in the kernel's alphabet (lowercase letters, digits, `.`, `_`, `-`, at most 32),
  distinct from every entry the file will hold. An entry that was bound and is no
  longer is removed; an entry no binding named is kept as it is, and the page
  lists it under **Other models**.
* On the page each block names the native it backs, linked to its action page,
  where it is run like any action; that is the test. A price is set per block, in
  the world's money. Save goes through the configuration's save: the file at the
  page's version, the kernel restarted, the previous file restored if it does
  not answer.

**No value-transfer queue.** A transfer to another kernel settles in the
sender's own call and is paid on the rail like a won ticket (kernel §D18, P11);
the kernel serves no queue and the UI carries none. Its one visible state is
the transaction's `payment` (§6.5).

### 6.7 chat — rooms of people and agents

Chat is the surface a session lands on: an app of this UI (§1, `apps/chat`)
presenting a program of its own, `juice-chat`. Its terms: a **member** is a kernel principal, `handle@kernel`, person or agent,
and chat never distinguishes the two (ecosystem-standard §7); an **entry** is
one thing said in a room, author, time, a body that is a JSON value (a post
being a string) and a stored kind (post, answer or failure); a
**delivery** is a kernel task chat creates for one recipient of a post; a
**hand-off** is a post that addresses a member; a post **addresses** a member
when it is text whose first word, trailing `,:;.!?` removed, is that member's
address, compared without case. There is no `@handle`: a member is named as
everywhere in Juice.

Chat is an HTTP server, the **chat service**, whose endpoints the kernel wraps
as actions owned by the kernel user `chat@<kernel>`. Its thirteen actions, every
schema canonical (§6.4) and every text naming this kernel, are:

| Action | Visibility, auth | Input → output | Who may |
|---|---|---|---|
| `index` | public, free | `{}` → `{usage}`, the paragraph a member learns chat from | anyone |
| `register` | public | `{handle}` → `{api_key, task_id}` | anyone; the key activates only when that address completes the task |
| `validate` | private, chat's bearer | the verification task's onward | the kernel |
| `reply` | private, chat's bearer | a delivery's onward: `{thread_id, member, delivery, message, answer?, status?, reason?}` → `{ok}` | the kernel |
| `msg/post` | public, delegated | `{thread_id, message: string}` → `{delivered}` | a member |
| `room/create` | public, delegated | `{members, name?}` → `{room}` | anyone joined, who becomes the creator; a blank name is the members' handles |
| `room/rename` | public, delegated | `{thread_id, name}` → `{room}` | the creator; a name is never blank |
| `room/add` | public, delegated | `{thread_id, member}` → `{room}` | the creator |
| `room/remove` | public, delegated | `{thread_id, member}` → `{room}` | the creator, never itself |
| `room/leave` | public, delegated | `{thread_id}` → `{ok}` | a member other than the creator |
| `room/delete` | public, delegated | `{thread_id}` → `{ok}` | the creator; everything in the room goes |
| `room/list` | public, delegated | `{}` → `{rooms}`, newest activity first | anyone joined |
| `room/read` | public, delegated | `{thread_id, after?, seen?}` → `{room, entries, removed, waiting, more, rooms}` | a member |

Delegated means `delegated_bearer` on the service's caller-key header,
`X-Juice-Caller-Key`: the kernel injects the key the member connected, and the
service resolves it to the member.
The onwards are private so that a member completes a task without being able to
run the action itself and speak as somebody else: the kernel checks an onward's
visibility against the task's creator, chat. They are reconciled at every
registration, and any other action the provider owns is deleted.

**`room/read`.** Without `after` it answers the room's latest entries; with it, the
oldest entries newer than it and the posts an answer replaced. `waiting` names the
other members holding an unanswered delivery. `seen` moves the caller's read cursor
over what it now shows. `rooms` is the caller's list as `room/list` gives it, so one
run serves a whole page.

**Every reply fits the kernel's cap.** The kernel refuses a reply over 4 MiB
(measured on v0.17.1). A post is at most 65,536 characters by its schema. An answer
over that is kept as a failure saying so, and its completion succeeds, because an
agent treats a refused completion as final and its task would wait forever.
`entries` holds at most 200 entries and 1 MiB; with `after`, `more` says others
remain and the caller reads on from the last. `rooms` holds the newest activity that
fits 256 KiB.

**The program.** `juice-chat` is a service in the sense of ecosystem-standard §7,
built from `cmd/juice-chat` and `services/chat`, which import the kernel client and
nothing else of this repository (a test holds both directions). Its home is
`$JUICE_HOME/services/chat/`: `config.json` (`listen_addr`, default
`127.0.0.1:8090`, and `public_url`, what a kernel dials), `state.json` (the bearer
of the private actions and, per provider, the auth configuration last pushed),
`rooms.db` and `serve.lock`. `juice-chat serve` holds no kernel credential: it
creates tasks through the capability of each dispatch. An action's source is
`<public_url>/<provider>/<name>`, and the server reads the provider from the path, so
one server serves every provider that registered it. `juice-chat register LOGIN`
reads the kernel and LOGIN's session from the client records and makes LOGIN's
actions exactly chat's; LOGIN's whole namespace is chat's.

**What the UI does.** At each start, in the background with the service account's
backoff, it makes `chat@<kernel>` with the password in its app state, writes that
session to the client records as `juice auth login` would, declares the program `chat`
to the process supervisor (§1), which runs `juice-chat serve`, waits until it holds
`services/chat/serve.lock`, runs `juice-chat register chat@<kernel>`, and joins the
local agents. The binary is `JUICE_CHAT_BIN`, else `$JUICE_HOME/bin/juice-chat`, else
`PATH`. Everything else is a
run of an action as the viewer: the UI dials no chat address and knows no store.

**When a page runs a read.** A run writes a transaction, so a read is run on a
gesture, or when the kernel shows that something changed for the viewer. The signal
is the viewer's open chat tasks (`GET /v1/tasks`, those on `reply` the viewer owns or
is addressed by, a read that writes nothing) together with a count of the changes
this UI made for that address, since a delivery settled by another tab between two
ticks leaves the open set as it was. A page carries one chat watch holding the signal
of its last successful read. On a room page, when the signal differs or a visible tab
holds a delivery for the room, the tick runs `room/read` once, then settles the
viewer's deliveries when the tab is visible, and answers the new entries, the rail
when it changed, and the watch. A delivery is settled only after a read that
succeeded, because a completion chat cannot receive spends the task. On any other
page the slow tick runs `room/list` when the signal differs. A read that fails
acknowledges nothing: the watch keeps its signal and cursor, the page reads again
after 5 s, then 10, 20, 40, and every 60 s, and the first success shows the change. A
room gone answers the new signal once. The rail on a page load is what the session
last read, with no run; a session holding nothing runs `room/list` once, and a
session not yet joined shows no rooms.

**What reaches a page late.** A change that reaches the viewer through no task
appears at the viewer's next read: a post addressed to another member and that
member's answer, a room command by another member, and a delivery settled within one
heartbeat by something other than this UI. Chat does not signal them with a task,
because an agent answers every task it receives and a completion's onward cannot
create tasks.

**A refused action.** The service answers a refusal with a status and
`{ok: false, code, error}`, but the kernel relays only `upstream returned status
N`, never the body (kernel D4, verified on v0.17.1 and pinned by the contract
test). The UI therefore chooses its sentence by the status, a room gone (404)
being the not-found page, and records nothing as a success: a refusal is a failed
transaction. An endpoint that cannot be reached is `upstream request failed`, also
pinned, and the UI says chat is not answering.

The rules:

1. **A post is `{thread_id, message}`**, run as the member; the kernel injects
   the member's chat key and chat resolves it. `message` is a string.
2. **Addressing routes.** A post that addresses a member reaches that member
   only; any other post reaches every other member. The
   author never receives its own post.
3. **A delivery carries the room.** During the post's own dispatch, through the
   capability the kernel injected, chat creates one task per recipient:
   `required_caller` the recipient, action `chat@<kernel>/reply`, partial args
   `{thread_id, member, delivery, message}` where `message` is the standing
   entries since that recipient's previous delivery, by others, this post
   last, at most fifty. The task is created before the delivery is recorded,
   so a record that fails repeats a range and never leaves a phantom. A
   recipient the kernel refuses is skipped. The kernel shows the task to the
   member once the member has approved chat (kernel U52).
4. **Completing a delivery settles what the member said.** Of the member's
   posts since the delivery: when some address nobody, an answer is appended
   and those posts are marked replaced by it; when there are none, the answer
   is appended; when all are hand-offs, nothing is appended. A failure
   (`status` and no answer) is appended, as a failure, only when the member
   posted nothing since. A completion carrying neither appends nothing. An
   answer is stored as the string it is. Nothing is ever deleted: a replaced post stops standing, and a page showing
   it is told to remove it, so seqs only grow.
5. **One post per held delivery, counted.** A member holding n unanswered
   deliveries in a room may have n standing posts since the oldest of them;
   the next is refused.
6. **Hand-off cap.** A post's depth is zero when the poster holds no delivery
   in the room, else one more than the deepest held; beyond the cap (three)
   the entry is kept and nothing is delivered.
7. **Unread is chat's own read cursor** per member and room, advanced when the
   room is in view; a room with unread entries is weighted in the rail.
   Opening a room also completes the viewer's waiting deliveries for it
   (price 0), so a person's next post has depth zero.
8. **The UI joins its installation's principals, through the kernel alone.**
   Joining is approving `chat@<kernel>` as a requester, running `register`,
   completing the verification task and connecting the key at `chat@<kernel>`,
   each as the principal: the path a member elsewhere walks by hand. A member is
   joined when the kernel's grant plan shows every action under that selector
   granted; the UI holds no chat key. The person signed in is joined, with the
   session, by the landing when the plan does not show them joined, and by a run
   the kernel refuses for want of a grant or chat refuses for an unknown key. The
   rail never joins: a session not yet joined shows no rooms. An agent with a home under
   `$JUICE_HOME/agents/` named by a login on this
   kernel is joined with its own session, read from
   `client/credentials/<login>.json` under that file's lock and refreshed there
   when the kernel refuses it (ecosystem-standard §4): every time chat signs
   in, when it is added to a room, and, for the assistant, at every landing. A
   join that fails is said on the page that asked for it, and no key is ever
   retired: `register` is open and names any address, so retiring one would let
   anyone revoke a member's credential, and a failed connect leaves the earlier key
   working. A contract change revokes grants; the next join restores them.
9. **`/chat` is the viewer's conversation with the assistant**: the room whose
   members are exactly the viewer and `assistant@<kernel>`, found among the viewer's
   rooms, or started with `room/create` and named `assistant`; without an assistant home the
   landing is the newest room, else an empty page beside the rail. The rail
   lists the viewer's rooms. Every room read and command on a page is a run of its
   action as the viewer, so the UI touches no store of chat's, and New conversation
   offers this kernel's agents first. A room
   id is three words, `adjective-animal-NN`, which a language model copies
   exactly (48-hex ids came back altered in 5 of 14 replies, words in none of
   26).
10. **A member on another kernel answers when addressed and cannot post**: a
    connected credential names only the caller's own kernel's actions. It
    joins through `register`.
11. **The assistant exists.** The agents app makes `assistant@<kernel>` when it
    starts and has no home for it: the password it keeps in its app state, the
    account (one the kernel already holds is adopted only when that password
    signs in), the session, the home, and the daemon. It starts a stopped
    assistant that holds a session.

The post action's description is read by an agent on every turn, so it says
what the rules need of one: a member of the room is reached by posting with its
address first, and a task from chat is answered by completing it. Measured on
the production model (gemma4:26b): eight ordinary questions, two posts, one
answer each; three hand-offs, three addressed posts and one answer from the
member addressed. Without the second sentence the model answered by posting
and then narrated the post in its completion; without the first it reached the
member by a direct message outside the room.

The UI (the workspace, §6.1): `/chat` lands in the viewer's conversation with
the assistant, else the newest room, else an empty page beside the rail.

* **The rail** is the sidebar's list of the viewer's rooms, newest activity
  first, the room in view `aria-current`, a room with unread entries weighted;
  a read that finds it changed replaces it. Its first row,
  `+ New conversation`, opens a dialog (from any page, `?form=new`) that asks
  for the **participants first** — this installation's agents as boxes, then
  any addresses — and a name, optional: a blank one is the participants'
  handles. Refused, it stays open with the reason and what was chosen and typed. There is no rooms page. Each room carries its one menu: **Members…**,
  **Rename…** and **Delete…** for its creator, **Members…** and **Leave…** for any
  other member, the question
  naming the room.
* **A room** is crumb (name, truncating on one line) → entries → whom it
  waits on → composer, the page the scroll container, the composer held at
  the foot of the viewport; beside it the **panel**, empty until an action is
  chosen. **Members…** is a dialog (`?form=members`) listing the members, with adding and
  removing for the creator; for the room in view it opens in place, for another it
  opens that room. Its creator alone is offered **Rename**: a dialog holding the name, answered open by the server, with no run
  (`?form=rename` without script), and held outside the sidebar on a room page,
  since a narrow screen does not render the sidebar. A room command sent with
  script is one run and one read, whatever the watch says is due, answered as a
  tick is: the dialog closes, the rail and the crumb are replaced in place, and
  no page loads. A rename refused comes back open with the reason and the name
  typed. Deleting or leaving the room in view goes to `/chat`. A delete or a leave
  of a room already gone is done. The rail follows at each other member's next read. An entry renders as the view of its body (§7), a failure being the
  view of its `{status, reason}`. **Waiting on** is one line naming the
  members other than the viewer that hold an unanswered delivery in the room
  — a fact chat holds, never a claim of activity, which chat cannot observe —
  kept by the heartbeat, empty when nobody; three dots pulse beside it while
  the page watches, still under reduced motion.
* **The composer** is the post action's form drawn as a bar — the one
  sanctioned exception to the form's card, because it is the workspace's
  cornerstone. The exception is in the drawing alone: the text box is the
  schema's own `message` control (§6.2), under its own name and limits, posted
  through the form's own decoding; `thread_id` is sealed by the page's address
  and not shown; there is no label, help or JSON switch. One rounded box holds
  `/` (the launcher, §6.1), `@` where the room has more than one other member
  (a list of them; choosing one puts its address first, in place of one
  already there), the message box growing to ten lines, and an up arrow that
  sends — the one ink-filled control, never the accent, disabled while the
  box is empty; `/` and `@` are quiet icons of the same set. Enter sends and
  Shift+Enter breaks the line; a first word that begins a member's address
  lists the members too. Beneath the bar stands the viewer's available
  balance in small type, kept current by the heartbeat (a run in the panel, a
  priced fill, money arriving). The submission runs `msg/post` as the viewer
  with the room taken from the page's address, so the browser and an agent
  take one path. A refusal, the kernel's or chat's, stands just above the bar,
  keeps what was typed, and clears at the next keystroke. A post is a string
  for now; a document post is a later decision.
* **The panel** holds an action or a search's results, its head held at the
  top as the panel scrolls. An action (`GET /actions/{id}/panel`, or
  `GET /discovered/panel?ref=` for one known only from a peer, resolved first
  as its page is: a row's panel is its page's address with `/panel`) is the
  action as its page states it — title, chips, address, description, this
  kernel's record with the price, what other kernels report folded — and the
  one run form (§6.2), with its outcome below. A search's results
  (`POST /browse/search`, §6.2) are headed `Search "…"`. A row opens its
  action over the results, which are held; its head is then `← Results`,
  which gives them back in the order and at the scroll position they had,
  without searching again. × closes the panel and drops what it held; nothing
  of the conversation or the draft changes. A panel that cannot be filled — an
  action that cannot be opened, a search that fails — keeps its head, the way
  back to the results it was opened over and ×, over the reason, so results
  paid for are never lost to a failure. The panel is a pane of the window —
  full height at its right edge, a hairline from the conversation — while the
  conversation keeps a readable column (the main area 960px and over,
  whatever the sidebar's state); narrower, it covers the conversation. The action page remains, built from the same partials and the
  same loader.
* **Fill from chat** (`POST /actions/{id}/fill`) prepares the panel's fields
  with the planner, under §6.2's Proposal rule: its context is the room's
  recent conversation, as the conversations app hands it (the viewer's
  entries as the user's, every other member's named). It is a tool of the
  form, in its top row beside the JSON switch — an icon named in its title,
  with the amount on the money button when priced — and Run is the form's one
  button at its foot. While the planner works, Run's progress bar runs at the
  row's left, where nothing else stands, and the form is busy, its other
  controls inert. The fields come back as the form's own; a refusal stands in
  the bar's place and the fields stay.
* **The panel's sections.** The record — title, address, description, the
  stats line and its folded evidence — keeps its paragraph rhythm; the run
  form begins a section, one spacing step (`--space-5`) below whatever
  precedes it, above the form's own field rhythm (`--space-4`).
* **A success from a room is the draft.** A successful run from the panel
  puts its result into the composer — a string as itself, any other value as
  its JSON — after a blank line when something was typed, so a draft is never
  overwritten, and the panel closes; the receipt stays in Transactions. A
  failed, refused or pending run keeps the panel with its outcome. Nothing
  reaches the room until the person sends it, through `msg/post` like any
  post. A result reaches a next action through the conversation: an agent
  reads it, or Fill from chat does.

The store is `$JUICE_HOME/services/chat/rooms.db`, upgraded in place (a delivery
held across the upgrade settles nothing said before it). `juice-chat serve` moves one
left at `ui/rooms.db` once: it folds the store's log into the database file, so
nothing committed is lost, and renames that one file. An interruption before the
rename leaves the old store whole, and with a store at both paths or the old one in
use it refuses to start. A first start beside the previous application's `ui/chat.db`
imports its live rooms, members and messages once and never writes that file.

## 7. Visual language and non-functional requirements

* **The accent is exclusive.** The accent MUST appear only on the control that authorizes a
  settling `Call` paying a provider — the terminal, priced, parties-visible
  confirm — or an irreversible move of the person's own money in or out of
  Juice: the confirm of a deposit or a withdrawal (§6.5). A control that opens a confirm or otherwise moves no money MUST NOT use
  the accent.
* **Button vocabulary (closed set).** Spend: the accent, solid, names the amount
  as `verb (N)` (e.g. "Run (1.234567 USDC)") — the exact amount (§7), since the
  button commits it (`Deposit (N)`, `Withdraw (N)`). Primary
  non-spend: solid ink, the default action of a form that has something to fill
  in or offers a second button, one per form, its first submit (grammar §5.1);
  a lone button on a form with nothing to fill is a command and stays
  secondary. Secondary: outline/ghost. Destructive: maroon, distinct from the accent —
  Delete, Disable, Decline, and force-end; force-end MUST name the refund, not a
  price. No other button styles exist. (A task completion is the neutral
  `Complete` naming no amount, §6.3.)
* **Money and identity typography.** Money, IDs, hashes, and `owner/name` MUST
  render in **monospace with tabular figures** (the provenance face), regardless
  of the surrounding proportional chrome.
* **The view is the one rendering of a JSON value a person reads** — a
  result, a receipt's args and result, a task's sealed `partial_args`, the
  receipt-verification checks. It is the run form's anatomy (§6.2) read-only,
  so a result looks like the form that produced it: a key is a label over a
  box with the input's geometry, in the read-only register (no fill, a hairline
  border) so it cannot be taken for an input, holding the value's text with
  its line breaks;
  a nested object or array is a group that folds behind its name and its
  size, closed; an array's element and the root never fold; a number
  is its literal, a boolean, `null`, `{}` and `[]` their words, muted. Keys
  keep the order served. A string's box holds it whole, however long, as the
  form's textarea does. Nothing is submittable. The raw JSON sits behind the same JSON
  switch the form carries — a labelled switch (`role="switch"`) at the block's
  top right, swapping the fields for the JSON — with a Download where the value is a
  transaction's result.
  In a chat message, a run's result and a receipt's args and result, a string
  whose trimmed text is a JSON object or array is viewed as that document;
  the JSON behind the switch stays the value as stored. Nothing else in it is
  interpreted (§8.1).

  **The world is named wherever the interface names itself.** The chrome states
  the kernel's handle and the world it serves, on every page, because a kernel
  serves one world for life and the world decides what every figure on screen
  is: whether the money can leave, and what it is worth. A symbol does not carry
  that — `credits` is not "not redeemable" — so a person who never sees the
  world cannot tell a rehearsal from a live market, which is the one thing the
  money surfaces exist to make legible. The balance view names it again in the
  pay-in sentence, where the consequence is (§6.5). Both the world's name and
  whether its money has addresses are `/health` facts: the UI states them and
  judges neither, never labelling a world play or real.

  **The unit is the world's, and the kernel renders it.** Every amount on the
  wire is an integer in the base unit of the world this kernel serves, and only
  the kernel knows how that unit scales and what it is called: `GET /health`
  carries `decimals` (0 where money has no fractional unit, 6 on a token world)
  and `symbol` (`credits`, `USDC`). The kernel also publishes ONE rendering, which
  is the UI's **exact** form: the amount, its trailing zeros trimmed to a
  **two-digit floor**, **no thousands separators**, the world's symbol appended —
  `133 credits`, `1.50 USDC`, `0.000133 USDC`, `-2500 credits`. Separators are
  absent by design: the text is meant to paste back into an amount field unchanged.

  An amount is **rounded where it is read**: two decimals at one whole unit or more,
  two significant digits below it, rounded half away from zero — `1234.57 USDC`,
  `0.54 USDC`, `0.00013 USDC` — in the same form otherwise, with the exact amount as
  its title. An amount MUST be **exact** where a control commits it, where a
  confirmation or notice names it, where a result or a transaction states what was
  charged, and where it bounds an input. No rounded value is ever parsed or sent. A
  world with two decimals or fewer has nothing to round. These render through **two**
  helpers on one type (`money`, `moneyExact`) reading the live identity — never a
  currency mark the UI chose, never a `¤` or gem.

  An editable amount is typed in that same form without the symbol, its `step`
  the world's own smallest unit, and parsed server-side back to base units; a
  separator or a digit finer than the base unit is **refused**, never stripped or
  rounded. Ledger rows carry explicit `−`/`+`; spend buttons state the unsigned
  gross as `verb (N)`.

  **A unit the UI cannot learn is a refusal, not a guess.** Until the kernel has
  said what its money is, a money cell renders an em dash and every typed amount
  is refused with that reason. Assuming a scale would put a figure in front of a
  person that neither they nor the kernel ever said.

* **Status signals.** The credited, refunded, and escrow-pending states MUST be
  mutually distinct and distinct from the accent in both themes, each pairing a color
  with a glyph: credited green `+`, refunded slate-blue `↩`, escrow-pending
  (parked / in-flight) plum `◷`. These signals MUST be confined to text and
  glyphs, not fills.
* **Pagination.** No list renders unbounded, and no page reads more than it shows when
  the kernel can page the list. The ledger of transactions, a session's transactions,
  task history, sessions, withdrawals and ratings MUST each be read one page at a time,
  through the kernel's `limit` and `offset`, one row beyond the page saying whether
  another follows, since the kernel answers no total. A page is full until the list
  ends, since the kernel pages after visibility, and a contract test holds this. Read
  whole and cut here, each for its reason: Discover (a sort orders the whole catalog),
  the workshop table (its state tabs filter what the kernel does not), a user's actions
  (a page of actions is not promised full), the admin roster (sorted here), Network's
  kernels (merged from classes here), and obligations, held payments and requesters
  (the kernel does not page them). Open tasks and open sessions are read whole: they are
  work still to do, and small. All of them page at **20 per page** through **one shared,
  quiet pager** (`← previous · page N · next →`; a boundary renders a dimmed non-link).
  Several lists on one page share one pager. The pager carries no accent (money-reserved).
* **Themes.** The UI ships **multiple selectable themes**, each defined
  by a **Ghostty 16-colour ANSI palette** (`palette 0–15` plus
  background/foreground/cursor/selection). Every theme MUST derive from one set of
  semantic design tokens (CSS custom properties) mapped from the palette
  (`--bg`←background, `--text`←foreground, money signals from the ANSI
  green/blue/magenta/red slots, `--accent` from a non-status hue slot, surfaces
  and borders mixed between bg and fg); components MUST reference tokens only,
  never raw colours. The token stylesheet MUST be generated from the palettes by a
  committed tool and guarded by a drift test. A theme applies by default from
  `prefers-color-scheme` (a designated default light and dark theme); an explicit
  choice MUST be persisted server-side (a `theme` cookie carrying the slug) and
  stamped on `<html data-theme>` at render time, with no flash and a picker that
  works without JavaScript. Every shipped theme MUST meet WCAG AA (≥4.5:1 body and
  muted text, ≥3:1 status signals and UI affordances) — enforced by test; a theme
  that cannot is not shipped. The accent MUST stay visually distinct from every
  money-status colour. The default light and dark themes are **paper** and
  **ink** (proportional, soft corners). A theme MAY additionally override the
  **structural** tokens (`--font-ui`, `--radius`) — the shipped `terminal` theme
  does so to restore the monospace UI face and square corners — but the
  **provenance** face (`--font-mono` on money, refs, ids, hashes) and every
  money/accent discipline above hold in every theme. `prefers-reduced-motion` MUST
  be respected.
* **Typography.** The UI face is the **proportional system sans stack** (the
  storefront register). **Monospace with tabular figures is reserved** for the
  provenance set — money, IDs, hashes, `owner/name`, and sealed `partial_args` —
  so the typeface itself marks kernel-vouched facts apart from proportional chrome
  and from counterparty prose. Corners are softly rounded (a card radius and a
  tighter control radius, both tokens); no drop shadows. The `terminal` theme
  reverts the UI face to monospace and the radius to square.
* **Sizes are tokens.** Font sizes, spacing, icon and avatar sizes, radii and
  one control height MUST be design tokens, the spacing on a 4px base, and the
  stylesheet MUST write no size literal in their place (hairlines excepted),
  enforced by test. Body text and every control MUST read at the standard size,
  `1rem`, which one line of the stylesheet defines, since iOS Safari zooms the page
  on a text field under 16px. Every button, input, select, one-line textarea and icon button is the
  control height, so controls side by side align.
* **Icons.** One set (Heroicons, outline/solid for inactive/active), recolorable
  to tokens, never emoji. Every icon-only control MUST carry an `aria-label`;
  hover tooltips are enhancement only.
* **The copy control.** A truncated key, hash or address serves matching by
  eye; the commands that take one — `admin peer inspect <key>`,
  `admin user deposit --ref <hash>`, a run of `owner@<key>/name` — and the
  wallets a pay-in address is pasted into take it whole. Wherever such a value
  is named, ONE icon-only control (`_copy`, carrying the whole value, with an
  `aria-label` naming what it copies) puts it on the clipboard, acknowledged
  through the `aria-live` region. A record id needs none: the kernel resolves
  any prefix of four or more hex digits, so the shown prefix is already a
  valid argument. A chain address renders as wallets show it,
  `0x7099…79c8`, beside its copy control; it is written whole only where the
  whole text is the point, the registration message a person signs (§6.5).
* **Feedback.** There MUST be no notification feed, bell list, or toast stack;
  outcomes render in place. The UI MUST provide exactly one polite `aria-live`
  region for ephemeral acknowledgement of the user's own just-completed action
  (e.g. "Action disabled," "Copied," the 429 "slow down"): no history, no
  stacking, no counterparty text, auto-dismissing. The task badge is the sole
  async attention signal in the core surface; an app keeps its own attention
  in its own surface (the rail's unread weighting).
* **CSS grammar.** All visual decisions MUST live in named CSS classes in
  `app.css`; inline `style="..."` attributes are forbidden in templates. Utilities
  (`.small`, `.tiny`, `.flush`, `.stretch`, `.input-sm`, `.row.compact`, etc.)
  MUST be defined once and composed. Icon-plus-badge layouts MUST use
  `.ico-badge-wrap` + `.badge`. Components MUST reference design tokens only,
  never raw colors or sizes.
* **Other.** Long-running calls MUST show progress against historical latency and
  MUST NOT hang on an indefinite spinner. No secrets or tokens in HTML, URLs,
  client storage, or logs. Structured logs (`log/slog`) to stderr MUST carry a
  request ID and, where available, subject and resource identifiers, and MUST NOT
  carry tokens, args, or results.

## 8. Security

**Architecture.** The UI MUST apply the session, token, PKCE, single-flight
refresh, session-limit, logout-revocation, and cookie/CSRF rules of §5, and additionally MUST:
route all browser traffic through the BFF as the sole kernel client; serve a
strict `Content-Security-Policy` (no inline script, self-hosted htmx, no
third-party origins) with `frame-ancestors 'none'` and `Referrer-Policy:
same-origin` (leaks no referrer to any other origin, while preserving the
same-origin `Origin` header that `gorilla/csrf` validates — `no-referrer` MUST
NOT be used, as it makes browsers send `Origin: null` and breaks every
state-changing form); render only through auto-escaping templates; and ship no
client framework.

**Marketplace-threat layer.** Counterparty-authored content (action names,
descriptions, schemas, lookup results, args, results, `partial_args`, failure
`reason`) is untrusted input, wherever it is shown. The UI MUST:

1. Render every counterparty-authored string escaped and quarantined from chrome:
   no markdown, no auto-links, no bold, no the accent, never adjacent to or styled like
   a button; a result, args or sealed `partial_args` only through the view
   (§7), whose structure is the JSON type, a string holding a JSON object or
   array being viewed as that document (§7), and never otherwise the value: no value is
   linked, formatted or turned into an icon, a boolean is its word. The chrome
   vocabulary defined in §7 MUST NOT be applied to counterparty content. Trust affordances
   MUST key on kernel-captured fields (transaction parties, process owner, a
   task's `required_caller` and `action`), never on payload shape.
2. Route every act that moves the user's own money — run (including a run of a
   value-bearing action, which is how money is sent, §6.5), force-end —
   through a confirm whose button names the amount or refund. A task
   completion moves none of the completer's money and stays neutral (§6.3, the
   single statement). No
   spend MUST be reachable from a list view or by a GET.

   This binds the natives the UI runs **on a person's behalf, unasked**, to
   power a surface — `sys/lookup` behind search, and any successor. Every
   native's price is the operator's own configuration and the shipped figures
   are policy, not contract, so a kernel may price one; the UI MUST read that
   price from the action row before running it, and where it is non-zero MUST
   degrade the surface rather than spend. An action a person chooses to run is
   unaffected: it is priced and confirmed like any other.
3. Split an action address by the first-slash rule everywhere (§4); render the
   owner distinguished, in a confusable-distinct monospace face, with the owner
   profile one click away for a local owner and no link for another kernel's;
   show a first-time-payee hint on the run form.
4. Show, on a task card, the sealed `partial_args` and who asks (§6.3).
5. Render receipt-verification failures with the failed check named, and MUST NOT
   display `valid` except from the kernel's own per-check response.
6. Hold no durable state but sessions, compose only documented HTTP calls, never
   infer kernel state transitions, and surface 429 as a "slow down" message.

## 9. Testing

Every production `.go` file has a `_test.go` file with independent tests.
`go test ./...` passes without network access; the kernel API is faked as an
`httptest.Server`, except in one test. Required coverage:

* the process supervisor (`supervisor`), with real short-lived processes: output and
  its bound while a program runs, restart and its growing delay, an unchanged
  declaration leaving the delay alone, SIGTERM then SIGKILL to the group, Start and
  Stop keeping the file, one supervisor per installation, the leftovers of a supervisor
  killed with SIGKILL including a disabled and a deleted program, a record of another
  process signalling nothing, a program's own lock, a refused file, reload; process
  control appearing nowhere else;
* the installer (`install.sh`), run for real with a fake `curl` serving a release
  built by the test: a clean install places the three programs and the schema and
  adds `bin/` to the profile once over two runs; a wrong checksum, a missing
  checksum entry, a program that does not run and a tarball lacking one each leave
  the installation as it was; a relative `JUICE_HOME` is refused;
  `--no-modify-path` writes no profile; a VERSION names its release;
* the components (§1): each installs by the one routine, refused on a wrong
  checksum, a binary that does not run, a setup that fails or an asset without
  it, its setup having written its schema; a fresh
  installation installs what is missing and replaces what is older; one holding a
  kernel, declared or not, keeps an older kernel and an older agent and installs
  only what is missing; an override is respected; a followed kernel has no binary
  installed; a kernel elsewhere installs nothing; a first boot is refused on an
  older kernel binary, naming both versions; no test reaches the network;
* the installer (§1): each sequence is chosen by its facts; a fresh one walks
  every step in order and changes nothing before Install, which answers at once
  while its job runs, once however often it is posted; a page's refusal shows what
  to say and what was typed and never a password; Back works on the pages and not
  once Install has begun; a first boot that made no kernel returns to the Kernel
  page with no password held, and one that made a kernel goes on; a failed
  download is retried and a failure that waits is run again by Try again; a
  followed kernel is offered no page and nothing is created; the phrase is one
  for the sequence, on its page alone, and gone when it ends; a request from
  elsewhere sees the terminal command and may not post; each state of the
  machine gives exactly the steps of §1's table, a complete one none; a page
  request during the launch is held and sent on to the application, and only a
  slow or failing start is said, with the kernel's words; nothing follows
  Install; the Models step opens on this machine's first choice, keeps the page
  for List models, holds a key typed and never shows it, refuses with what to
  do, and gives the first boot the models chosen, gone when the sequence ends;
  the package imports
  nothing of this repository and only the entrypoint imports it;
* the models form (`models`): a provider's list asked as the kernel asks it, a
  refusal in the provider's words, no redirect followed; the setup written in
  the kernel's shape over an empty file, the kernel's defaults and an operator's
  extra entry; an entry reused only when it is the same model and kind, enabled;
  names distinct and within the kernel's rule; search refused on a protocol
  without embedding models; no provider asked by a test; the Models page gated to
  the superuser, this installation's kernel and a schema that describes the
  entries, a key never rendered, List models writing nothing, a save at the
  page's version; the configuration page showing no model;
* the first boot: its child has ended when it returns and when the UI shuts down, one
  ignoring SIGTERM is killed, and a start during it neither declares the kernel
  nor returns;
* the kernel contract (`client/contract_test.go`): the installed kernel binary,
  on throwaway loopback kernels — two federated play kernels and one on a local
  anvil chain with juice-rail's mocks — is driven into the states that fill
  optional fields, and every reply is decoded through the client. A call that
  fails to decode fails the test, as does a client response type no reply
  decoded into or a field no reply carried, save the types the rig cannot
  reach, each named with its reason. It skips, naming what is missing, without
  the binary, anvil, cast or the mocks. The client declares only fields the UI
  reads: a field nothing reads is a decode hazard, not documentation;

* the typed API client and error mapping (§10 error mapping);
* session lifecycle: PKCE handshake, single-flight refresh rotation (a request
  after a rotation reuses it), logout revocation, suspension → clean logout;
  over separate HTTP requests, a request that loaded the session before
  another rotated its tokens and finishes after leaves the rotation in place,
  and a sign-out during a refresh stays a sign-out and revokes the pair the
  refresh received, as does a refresh that stores its pair while the
  sign-out's revocation is at the kernel; an acknowledged recovery phrase
  stays gone after a late save, a signed-out entry is never recreated, and a
  sweep removes the entries expired with their record and no other;
* session limits: a session is idled at the idle limit and aged at the age
  limit however recently used; ticks alone never keep it alive while a page
  request does; the heartbeat signs an idle page out with the notice and one
  revocation; the warning opens a minute before, Stay signed in closes it and
  moves the limit, and an unchanged state is answered with nothing; the scs
  store's own idle rule is off and its lifetime is the age limit plus the
  idle limit; a tick whose request was abandoned keeps the session;
* CSRF protection on every state-changing route;
* JSON-Schema-to-form rendering, including `partial_args` lock-out on task cards
  and the `allowed_input`-sourced completion form (target action unreadable →
  form still renders); the fold of a named nested node: open in the form while
  a field it demands is empty, with an optional object closed until in use and
  a required array content with its blank row under `minItems` 0, closed in the
  view with its size in its summary, an element and the root never folded;
* the form wire and the JSON switch: a required container is sent even
  empty and a blank row dropped; form → JSON is the submit's args; JSON →
  form fills exactly or names each value it cannot hold; the configuration's
  switch reads through the save's normalization, secrets withheld (§6.2, §6.6);
* completion is neutral, a transfer task included, and no action is read to
  render a task (§6.3); a card names its requester; requesters list pending
  first with the decisions that change each status, Block alone behind a
  confirm, and a pending requester counts in the badge (§6.1, §6.3);
* decline: offered to the required caller and the owner of a waiting task and
  to nobody else; a task held on another kernel names its holder and links no
  receipt after completion (§6.3);
* list reads ask for what they show: inbox history and the sessions page send
  `?all=1` (or the tab's status) and page to exhaustion (§6.3, §6.5);
* the payout address form sends the password, and a wrong one keeps the session
  (§6.5); the registration message is the kernel's four lines; the chain is
  accepted only on an exact fingerprint match; the transfer is ERC-20
  `transfer(vault, amount)` built from a fresh read and refused when the
  registration changed; reviews name the amount on the money button, the
  openers stay neutral; and only
  `wallet/` calls the two money mutations (drift guard); a `transfer <tx>` reason links only for the account debited, and a
  transaction's `payment` state renders as served (§6.5);
* the run primitive: cost preview, the priced confirm, process auto-closure, and
  force-end;
* no action is special-cased: no bespoke form, route, command, or price helper
  keyed to a single ref anywhere in the UI (§6.5);
* the entry screen (login, register, recover) takes a handle and signs in by
  its address on this kernel; every other name is sent and rendered as served,
  never rewritten (§4, §5);
* locality: an action is local iff its kernel segment is this kernel's name,
  and only a local owner links (§4);
* no surface decorates a name: no template puts an `@` before a name, and the
  only `@` a template may carry joins an address (§4), enforced by a drift
  guard over the core and app templates;
* the money unit: an amount renders rounded through `money` where it is read, and
  in the kernel's form through `moneyExact` where it is committed or recorded (§7),
  which a drift guard holds for buttons and a render test for the other sites; an
  amount finer than the base unit is refused, not
  rounded; and no template renders a currency symbol (drift guard);
* the session pin: a login records the kernel's key and network fingerprint, an
  address that answers for another kernel ends the session without sending
  it the refresh token, and a kernel that cannot identify itself cannot be
  signed in to (§5);
* target-based mutations: enable/disable/update/delete name an id or a path and
  the UI reports the rows returned, not a predicted state (§6.4);
* a discovered action resolves on open and lands on the ordinary action page;
  there is no by-ref run surface (§6.2);
* the wallet's world: the pay-in copy and the address form follow whether the
  world has addresses, a withdrawal is offered on every world that has somewhere
  to pay it, and the token is named where the world has one (§6.5);
* the activity is the ledger: each entry states its counterpart, sign and
  reason, a transaction id linking to its receipt and any other reason inert
  text; an absent side reads `outside Juice`, and a self-entry carries no sign
  (§6.5);
* the End control is one component that states a refund only when there is
  one; the wallet and the dashboard list the viewer's own sessions, the
  dashboard filtering before it takes five; the operator's locked figure reads
  `locked` without the escrow signal on every page that shows it; a blocked
  payout's reason renders (§6.5);
* the network page's halt banner is led by `#alert`, a banner in page flow owns
  its spacing, and the vault's finalized holdings render as served (§6.6);
* what is known about an action comes from its detail read: the strip is
  `local_experience`, other kernels' reports render under their issuers with
  the provider first, and nothing reads a stats route (§6.2);
* no force-end is offered on a session awaiting a peer's receipt (§6.5);
* a cross-kernel transaction's breakdown names paid, import fee and obligation,
  and its ticket — never "fee on value added" (§6.5);
* a peer row carries no money, and the network page states this kernel's own
  credit position and the obligations owed to it — each a fact with no gesture,
  one awaiting its buyer rendering no figure and no empty badge (§6.6);
* the two admin namespaces: a user verb reaches `/admin/users/…` and a peer verb
  `/admin/peers/…`, and no page sends a public key to a user route (§3.1, §6.6);
* the retired surfaces stay gone: settle, admin withdraw, unimport, the transfer
  queue, a peer's balance, funding a peer, confirming an obligation, the
  whole `/control/` prefix, the stats route, a parked call's refund date, the
  fold of transactions into the activity, `/v1/steps`, `/v1/me/address`,
  `/v1/transfers`, and the network page's peer-held list (drift guard);
* the truncate helper is the only author of the ellipsis on a shortened id
  (drift guard);
* a kernel row and a kernel's page carry the whole key in one copy control,
  which holds the value itself and never a selector (§7);
* the call-outcome classes, rendered distinctly — including that a parked
  (`timeout`) outcome names the reserved amount and claims no refund or refund
  date, that a failure names a non-zero charge rather than a full refund, that a
  `meta.retry="refresh"` refusal offers the re-pinned retry while a bare
  `execution_failed` offers none, and that a peer's refusal names the peer;
* discovery: lookup path and catalog fallback on `ErrInvalidState`;
* the workshop offers no synthesis affordance, `sys/make` being absent from the
  kernel;
* access control of owner-only UI affordances (Studio surfaces shown only to the
  owner);
* security invariants: counterparty content renders inert; trust affordances key
  on kernel fields, not payload shape; no spend reachable by GET — including
  that a search on a kernel which prices `sys/lookup` degrades instead of
  charging (§8.2);
* an app that acts on the UI host is reachable by the operator alone: every
  route gated, and no destination offered to anyone else (§5, apps);
* the single `aria-live` feedback region and the absence of any notification feed;
* the workspace frame (§6.1, §6.7): one sidebar — the rail and the avatar
  menu, the menu listing the registry's destinations with the current one
  marked and the operator's for the operator alone — the task badge on the
  avatar, the menu row and the topbar's trigger, the drawer reachable without
  script, two sidebar states per width and none mixed, the frame in `dvh`, and
  no search pill, palette or command prefix; the launcher carries the whole
  registry as inert JSON, the configuration only where its page exists; a page
  reads the viewer once;
* sizes as tokens: no size literal in the stylesheet beyond a hairline, and
  icons and avatars at token sizes;
* a page's reads: with a thousand rows of each list, a page asks the kernel for one page
  at its own offset and never walks the list, its pager following; Workshop reads no
  transaction; against the real kernel, a walk in small pages equals the whole read for
  each user and the superuser;
* chat as the workspace: the rail newest first with the room in view and an
  unread one marked, its heartbeat answering only a changed rail; a room's menu
  by who made it; the rename dialog opened with no run, refused with the reason
  and the name typed; a room command one run and one read answered in place,
  past a watch that failed and for a room already gone, the room in view going to
  `/chat`; no id rendered twice on a room page; a room's crumb its name, its one menu
  in the rail, its members one dialog, a change refused coming back in it; the landing's
  fallbacks; a conversation started from its participants, its blank name
  theirs; whom the room waits on, never the viewer; the room page carries the
  composer bar — the schema's own message control, no label, switch or sealed
  view, `/`, `@` only with more than one other member, the send arrow, the
  balance and its watch — and the panel; the balance watch answering only a
  changed amount; the composer standing in the room alone (drift guard); the
  form and the bar drawing a control through one partial; a room's context as
  the planner reads it, refused to a non-member;
* chat as a program: the service and `juice-chat` import nothing of the UI, nothing
  else imports the service, and the app dials no address (drift guards); the fake
  kernel dispatches every run to a real service, each command's rule holding for any
  caller and each read's cursor, window, `more` and byte bounds; one server serving
  two providers; registration creating, reconciling and deleting; the configuration,
  the lock, and the store's move with entries only in its log, with a store at both
  paths, and with the old one absent; the UI's start stopping, serving and
  registering in order, a failure retried; a join going through the kernel alone,
  connecting at the root and following the kernel's plan per action, a person
  holding only an earlier grant joined again; a failed connect retiring no key; a run
  refused for want of a grant or for an unknown key joining and running again; a
  refusal said by its status, a room gone the not-found page, chat down said beside
  the composer with the draft kept, and a person never reading a raw status;
* a page's cost: ten quiet ticks run nothing and write nothing; a delivery arriving,
  a delivery answered, and a delivery settled by another tab each run one read; a
  page that is not a room renders its rail with no run; a failed read keeps its
  signal and cursor, settles nothing, retries at the stated delays, and shows the
  change at the first success, even when the signal has returned to the one shown; a
  tab read while hidden settles once visible; the contract test pins that a task is
  listed to its owner until it settles;
* the stylesheet styles nothing that no template, script or Go renders
  (drift guard);
* the planner under one rule: offered at its price, on the money button when
  priced, pinned to the quote shown, never on load; a fill prepares the
  panel's fields from the room's conversation, runs nothing but the planner,
  leaves an unstated argument blank, and states a refusal in its own words;
  the fill is a tool of the form's top row, beside the switch, its bar there,
  and Run alone at the foot;
* evidence as the stats line reads: one table through one partial on the
  action page, the panel and the inspection; the stats line's cells; the
  provider's report first, the source last; corroborated and unverified counts
  in the cells' titles; a contradiction marking only its source; a peer's
  subjects named by address, else by id cut short; the old vocabulary gone
  (drift guard);
* the room's line waits on its members: one element for the page and every
  answer, and the old "is answering" gone (drift guard);
* the search from a conversation: Discover's search and the panel's are one
  function; only a lookup a read confirms free runs unpinned — priced or
  unreadable, never — a GET never spends, and the pinned one runs once at the
  quote shown; a panel that fails keeps the way back and the close; changed terms ask again at the new
  price and are never the fallback; a transport failure is an error, not an
  empty result; the catalog file's search terms, null for a lookup absent,
  switched off or priced without a pin; every row ranked under every order by
  the one sort rule; a discovered row opening in the panel; a success from a
  room carrying its draft, and no other way to use a result (drift guard).

## 10. Open items / kernel prerequisites

Features marked *Conditional* elsewhere are indexed here; each is mandatory only
once its prerequisite is confirmed on the running kernel.

* **Duplicate signup — resolved.** `POST /v1/users` with a taken handle returns
  `422 {"code":"invalid_input","error":"create account: already exists"}`
  (verified on v0.12.16), and the kernel enforces the ≥8-character minimum
  itself. The BFF keys on the code, not the message.
* **Action synthesis — dropped; the compiled path is open (§6.4).** `sys/make`
  is not a kernel action, and the workshop offers no synthesis affordance. The
  kernel's own path — `sys/tinygo/compile` → `POST /v1/actions {kind:"wasm",
  wasm_artifact}` — is now reachable: the create form takes the compiled
  artifact for `kind=wasm`, verified end to end (compile, create, enable, run).
  Before it, a person could compile in the UI and had nowhere to put the result,
  and `enable` refused with "cannot activate action with no source". The
  artifact is **pasted**, never gestured from a result: a control keyed to the
  shape of a result payload is what §8.1 forbids.
* **Upstream credentials on import (§6.4).** `POST /v1/actions/import` takes
  `auth` for the whole application, and the import form now sends it through the
  same panel action creation uses. Verified: an import carrying a bearer
  configuration lands its actions with `auth_scheme: bearer`. An auth is
  write-only, so an application not given one at import cannot be given one
  afterwards without naming its subtree by hand.
* **A pending consent exchange (§6.4).** `POST /v1/grants/complete` may answer
  `{status:"pending"}` rather than granted. The UI reports it as pending;
  reading it as granted would claim an account is connected while the next run
  still answers `grant_required`.
* **Trace tree (Wallet receipt page).** No trace-read HTTP route is confirmed.
  Until one is, the Engine-room trace tree stays conditional; receipt
  verification is confirmed and mandatory.
* **OpenAPI ownership proof (Studio).** Confirm whether the kernel exposes an
  HTTP protocol for challenge creation, credential submission, and proof status.
  Until then the UI surfaces the kernel's rejection on public activation.
* **Lookup operational status (Run).** Confirm whether `sys/lookup` has a
  configured embedder or returns `ErrInvalidState`. Does not gate discovery
  (mandatory); selects semantic ranking versus catalog fallback.
* **The planner (§6.2).** Confirmed on 25c07cd against a local model, and on
  v0.17.1 with one candidate and a conversation:
  `sys/llm/decide` takes `{messages, actions}` and answers
  `{action, args, message?}` — `message` optional and usually absent. It selects
  and never executes. A dead kernel-qualified candidate among live ones is
  discarded; a **bare unknown** ref refuses the whole call (404), so the UI
  sends only refs the kernel served. With no model reachable it fails as an
  ordinary outcome (`execution_failed`, "model did not select an action"), which
  the UI states as the planner's own words. Its proposal must satisfy the
  candidate's schema: a required argument the context does not state makes the
  model omit it, which the kernel refuses ("required field missing"), unless it
  is told to give an empty string — possible for a string, not for a number or
  a boolean. A partial proposal is a contract the kernel does not offer.
* **An agent reads only a refusal's status (§6.7).** The kernel relays
  `upstream returned status N` and no body, so an agent refused a room command or
  a held post learns the number, not the reason (kernel D4). The contract test
  pins that wording. Chat's `index` and action descriptions state the rules
  beforehand, which is what an agent has until a kernel relays a reason.
* **Structured agent proposals.** An agent answers a delivery with a string
  (juice-agent's reply tool, shaped onto chat's `reply.answer`), so a member
  cannot hand the person a validated `{action, args}` to open in the panel.
  Assisted execution goes through the conversation and the fill until an agent
  emits such an object and chat admits it; prose is never read as one.
* **Value-bearing runs.** A run of an action whose `effect` is `transfer`
  moves value beside its price, and §8.2 asks its confirm to name the amount;
  the run form names only the price, and the transfer's `amount` field is the
  kernel's base-unit integer. Both predate the workspace and are open.
* **Settlement — retired (§6.6).** The kernel serves no settle route: every debt
  settles per call on the rail by its own ticket. As of 25c07cd the operator has
  no act in it at all — an obligation closes when its buyer pays, which the
  seller's kernel records itself (measured: an obligation appeared and cleared
  in five seconds with nothing pressed, `docs/kernel-v0.15-verification.md` §3).
  `admin withdraw` is gone with it; withdrawing is a user's own verb (§6.5).
* **The admin surface — moved and split (§3.1, §6.6).** Confirmed on 25c07cd:
  every `/control/*` route answers 404, and `/v1/admin/{users,peers,kernel}`
  serves in its place with the two namespaces refusing each other's targets.
  The rename body field is still `new_name`; suspend and unsuspend now answer
  with the row rather than 204. `GET /v1/admin/kernel` gained `lottery_max`.
* **Crediting a peer — gone (§6.6).** There is no `/v1/admin/peers/{key}/deposit`
  route, and a peer key on the user deposit route answers `404 "… is not a user
  here"`. A peer account is provisioned by its first signed call alone.
* **Steps are tasks — resolved (§6.3, §6.6).** Confirmed on 4dbfedd: every
  `/v1/steps` route answers a plain-text 404 and `/v1/tasks` replaces it. A row
  carries no `action_id` or `parent_trace_id`; the list answers open tasks
  unless `?status=` or `?all=1`; `POST /v1/tasks/{id}/cancel` declines, for the
  required caller or the process owner only (a superuser is refused). A task
  for a user of another kernel is listed on both kernels under one id — on the
  addressee's without `action` or `created_by`, `owner` the holding kernel's
  name — and its completion reply carries `receipt_id: ""` and the holder's
  `tx_id`. The inspection carries no tasks, and a task for a kernel is
  addressed to its `sys` (a bare kernel name is refused)
  (`docs/kernel-v0.18-verification.md` §1–§3).
* **Inspect action identity (§6.6).** `admin inspect` serves each action with an
  `action_id` again, plus an `indicative` all-in price, so an evidence subject
  can be named. As of 4dbfedd the name is the action's address
  (`seller@bee/slow`), rendered as one. Evidence `notes` remains unverified and
  unrendered.
* **Credit position (§6.6).** Re-confirmed on 25c07cd, now at
  `GET /v1/admin/kernel`: `exposure` against `credit_limit`, the three bps
  rates, the ticket face and `lottery_max`, the `sys` split, `solvency`,
  `custody`, `blockchain_address` (was `rail_address`), `finalized` and an
  optional `stop`. The kernel card
  states them.
* **Withdrawals and payout addresses (§6.5).** Re-confirmed on a chain world at
  25c07cd. `GET /v1/withdrawals` **pages** like every other list — with four
  rows, `?limit=2&offset=2` answers the third and fourth — so the UI reads it to
  exhaustion rather than losing a fifty-first withdrawal. A payout with no
  native currency in the vault books `blocked` and halts the rail, and `stop`
  carries the shortfall as text, which the network page states. Confirmed on
  v0.14.0 and still true:
  `POST /v1/withdrawals {id, amount}` with a caller-minted UUID (a replay returns
  the same row). The address route is
  `PUT /v1/me/blockchain-address {blockchain_address, signature,
  current_password}` as of 4dbfedd: a missing or wrong password is 401 before
  the world's 409. While a rail payment is blocked every money verb answers
  `503 rail_stopped`. As of d59742b a world without payment addresses refuses
  only the address (409, "this world has no payment addresses"); a withdrawal
  there lands `confirmed` at once with `tx_hash` `manual:<id>`, the operator
  recording it (`docs/kernel-v0.16-verification.md` §6).
* **Held deposits — resolved (§6.6).** Verified on a local chain (anvil,
  decimals 6): a payment to the vault from an address no account has registered
  is listed with the sender under `party` (was `party_handle`), and the operator attributes it
  to a user by the row's own `id`, as the contract says. (v0.14.0 refused that
  id, accepting only the bare transaction hash; fixed in v0.14.1, and both forms
  now work.)
* **The money unit above six decimals (§7) — closed.** `Unit.Format` now carries
  every digit the world declares, so nothing is truncated at any scale.
* **Settlement is journalled — resolved (§6.5).** As of d59742b every movement
  between two accounts is a ledger entry: a local call's payout (buyer→provider)
  and fee (buyer→`sys`), each `reason` the transaction; a cross-kernel buy's
  import fee (`reason` the transaction) and obligation (`reason`
  `obligation <ticket>`), the seller's provider credited under the same
  ticket; and a deposit now names `sys` as its sender
  (`docs/kernel-v0.16-verification.md` §3). The wallet's fold of transactions
  into its activity is therefore retired.
* **A moved amount — resolved (§6.5).** v0.12.24 journals it (re-confirmed on
  v0.14.0): settling a
  `transfer`-effect call writes a ledger entry from the immediate caller to the
  beneficiary, `reason` = the settling transaction's id, and **both** parties
  read it (verified: the beneficiary gets 404 on the transaction itself, so the
  journal is their only record). The transaction still carries
  `gross`/`fee`/`net`/`refund` — zero at price 0 — and no `value`/`value_to`;
  the breakdown rows stay zero-suppressed and would render those if a kernel
  ever serves them. Remote-proxy transactions may carry them in the remote
  receipt JSON.
* **Parked-run metadata (§6.2).** Re-confirmed on d59742b: a dispatched call with
  no signed outcome answers `504 timeout` with `meta.process_id` and
  `meta.pending_since`, and the funds move from `available` into `locked`.
  `refund_eligible_at` is gone: only the peer's signed answer settles the call,
  and no age expires it. Ending that process answers
  `409 invalid_state`, naming how long the receipt has been awaited
  (`docs/kernel-v0.16-verification.md` §5).
* **What a failed call drew (§6.2).** Confirmed on d59742b: an executed failure
  carries `meta.tx_id` and `meta.charge` (a base-unit string). A composing call
  whose sub-work was delivered before it failed drew that sub-work
  (`charge` 1500000 in the measurement), so the failure card names it.
* **Stats — retired; evidence on the read (§6.2).** `GET /v1/stats/{id}` answers
  404 on d59742b. The detail read and every `sys/lookup` row carry `evidence`
  `{local_experience, provider_reported, observed_by_others, retained_cap}`,
  `local_experience` holding the old stats fields; a list row carries none
  (`docs/kernel-v0.16-verification.md` §2).
* **Contract-drift and peer-refusal markers (§6.2).** Re-confirmed on v0.12.24:
  drift answers `500 execution_failed` with `meta.retry = "refresh"` and charges
  nothing (the immediate retry succeeds); a peer that has suspended this kernel
  answers `403 unauthorized` with `meta.peer`, and the cached proxy is
  deactivated so it leaves the buyer's catalog. **`peer_unreachable` (502) now
  carries `meta.peer` too** — every failure to reach a peer names it. The name
  is a petname where the failing path knows one and the public key otherwise
  (observed: refusal gave `bravo`, unreachable gave the key), so the UI
  abbreviates a key-shaped name before printing it (§6.6).
* **Kernel contact cache (§6.6).** Confirmed on v0.12.24: every roster row
  carries `last_seen`, and `last_contact_failed_at` appears once a contact has
  failed; `sys/lookup` rows carry the same two for the hosting kernel. Raw
  timestamps that only move forward — the kernel neither hides nor reorders a
  hit for being unreachable, and states no verdict.
* **Action read shape (§6.4).** v0.12.24 drops `source` from a `kind=http`
  read: the decomposed `http` object is the read shape and `source` is a
  write-only input (the encoded-string form is gone). A `kind=wasm` read still
  carries its `source`, which is authored text. `effect` is now documented and
  is **absent**, not null, on an ordinary action.
* **`/control/transfers` — gone.** 404 on v0.12.23, and the 404 is plain text,
  not a typed error envelope. The queue is removed from this specification
  (§6.6); nothing replaces it.
* **Action mutations and `?ref=` (§6.4, §6.2).** Confirmed on v0.14.0: the
  per-id mutation routes answer a plain-text 404, `enable`/`disable`/`update`/
  `delete` take a `target` and return the rows written, and `GET /v1/actions?ref=`
  resolves a reference the way a call does — a kernel-qualified one over
  federation, caching a proxy, and a miss is an empty list rather than an error.
* **The remote transaction — resolved (§6.5).** A cross-kernel transaction
  carries `ticket_id`, `remote_receipt_hash` and `remote_signer_key`, and on the
  buyer's row `net` is the obligation returned to the caller while `fee` is the
  import fee retained here. As of v0.14.2 the **seller's row carries the same
  ticket**, so either party can follow the obligation and both pages name it; the
  seller's `net`/`fee` remain its own economics, which is why the captions key on
  `remote_receipt_hash` and not on the ticket. As of d59742b the peer is named by
  the petname bound on first use (`k-4lnqDq15`) on both books.
* **The remote task caller — resolved (§6.3).** A task parked for a principal
  on a peer renders its `required_caller` as `bob@bravo` — the handle it went
  by when the task was made, beneath the peer's local name. Every party is
  served this way, local ones included (`ana@alpha`).
* **The world's symbol (§7).** Confirmed on v0.14.2: `/health` carries `symbol`,
  and the kernel publishes the rendering the UI now follows.
* **`allowed_input` on `/v1/tasks` (§6.3).** Every waiting task carries it
  (4dbfedd), and the completion form derives from it alone.
  An approval-only task serves an object with no fields, in either of two shapes
  observed live — `{"type":"object","properties":{},"required":null}` and
  `{"type":"object"}` — and both MUST collapse to the single confirm of §6.3,
  never to a raw-JSON box, which would ask a human to author `{}`. The two
  shapes reach that outcome by different routes, and the distinction is load
  bearing: an **explicitly empty** `properties` declares that the input has no
  fields, so nothing can be collected; **no `properties` key at all** is an
  unconstrained object, where the raw editor is correct and the collapse instead
  follows from every key being sealed in `partial_args`.

* **Names are addresses — resolved (§4, §5).** Confirmed on 9705b3b: every
  input refuses a bare name (`"sys/time" is not an address: name the user as
  handle@kernel, an action as handle@kernel/name`) — run, `?ref=`, `?owner=`, a
  mutation target, a grant selector, a transfer recipient, `sys/message.to`,
  sign-in, sign-up, recovery, and `/v1/admin/users/{x}`, which takes the
  literal `ana@alpha` and refuses `%40`. Party fields are `owner`, `caller`,
  `target`, `from`, `to`, `operator`, `required_caller`, `party`; `/v1/me` and
  the admin user list serve `address`; a transaction serves `action` and no
  `action_name`; an action row serves no owner field; `network_digest` and
  `rail_address` are `network_fingerprint` and `blockchain_address`
  (`docs/kernel-v0.17-verification.md`).

**Error mapping (informative).** `ErrUnauthenticated` (incl. suspension) → clean
logout with explanation; `ErrUnauthorized` → not-permitted message; `ErrNotFound`;
`ErrInvalidInput`/`ErrSchemaViolation` → field-level form errors; `ErrInvalidState`
(also the kernel's refusal of a lifecycle verb on a remote proxy, §6.6 — which the
UI does not offer, so this is a defence, not a flow);
`ErrInsufficientFunds` → a plain not-enough-balance message (how money enters is
the wallet's disclosure, §6.5); `rail_stopped` → payments out are paused while a
rail payment is blocked, and deposits, calls and reads continue;
`peer_unreachable` → the peer named in `meta.peer` was offline, the call
refunded, retry later;
`peer_unfunded` → the serving kernel will not serve this one on credit — its limit
is reached, or its provider cannot fund the work — nothing was charged, and this
is never the caller's own balance (§6.6);
`ErrUnauthorized` **with `meta.peer`** → the peer refused this call (§6.2), which
must never read as the user's own lack of permission — plain `ErrUnauthorized`
keeps the not-permitted message;
`ErrExecutionFailed` → "the action failed and your funds were fully refunded,"
or, when `meta.charge` is non-zero, that the work already delivered was charged
and the rest refunded, with the failure transaction linked — **except** with
`meta.retry = "refresh"`, which is the provider's contract change: nothing ran,
nothing was charged, and the buyer is re-quoted (§6.2);
`ErrTimeout` → **not** a refund: the call is parked with its funds reserved, and
the message names the reserved amount and the process (§6.2);
`ErrInternal`. Raw status codes are never shown; diagnostic detail goes to logs
only and never includes tokens or secrets.

## 11. Design rationale (non-normative)

Juice is an economy of callable capabilities inhabited by people and agents,
each a principal with its own account. The interface is organized around
collaboration among those principals rather than around the kernel's resource
categories, because the work a person brings rarely fits one action: it moves
between saying what is needed, choosing a capability, preparing its arguments,
reading its result and handing the next step to someone — a person or an agent.
A resource-per-page interface leaves that composition to the person: search,
open, fill in, run, copy, search again. A pure assistant automates it but routes
every relationship through one privileged agent and takes the decisions away.
Collaboration contains both: a conversation with the assistant is its
one-to-one case, a room of specialists its general one, and in either the
person can run an action themselves, have its arguments prepared, or delegate
the whole job to an agent spending its own account. Hence three permanent
elements — the conversations, the composer, the avatar menu — and one
contextual surface, the action panel, so that context flows from conversation
to action to result and back without transcription. Search stays a capability
(the launcher, Discover); the other pages stay destinations, since a person
still arrives to answer a decision owed to them or to sell a capability, and
everything else is evidence about those activities. Money borrows the most over-tested patterns on the web:
the checkout and the order receipt. The kernel's "a price is a price" — one number
up front, one receipt at the end, refund on failure — is the mental model of
buying something online, which is why the run form and receipt page are the
two load-bearing artifacts, and why the the accent spend-verb captures consent
atomically with the amount, mirroring the kernel's park-before-execute.

The marketplace is treated as untrusted: counterparty content is hostile input,
so it is confined to an inert, quarantined style and every trust decision keys on
kernel-captured fields, which are unforgeable, rather than on payload shape, which
is not. Processes and traces stay buried because the kernel buried them;
resurfacing that bookkeeping would re-impose the cost the kernel removed. The
scarcity of alerting — one badge, no feed — keeps the single attention signal
meaningful and keeps hostile strings out of any auto-appearing, chrome-adjacent
slot. The interaction texture, htmx fragments over server HTML, is deliberately
plain: in a system that moves money, fewer moving parts is a security property.