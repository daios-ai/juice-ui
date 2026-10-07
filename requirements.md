# Juice Web UI Requirements

Version: 0.17.1
Status: design draft — endpoint surface gated against the running kernel

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

> **What changed in 0.15.0.** One **frozen form** renders every JSON value a
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
> wallet, home, workshop earnings, a session's funds — renders through one
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

> **Gating condition.** A UI feature is mandatory only if it maps to an HTTP
> endpoint **confirmed on the running kernel**. Anything unconfirmed is marked
> *Conditional* and indexed in §10 (Open items), and is treated as conditional
> until verified. Where the running kernel supports something `docs/API.md`
> omits, the fact is recorded here and `docs/API.md` is updated. The BFF may
> compose HTTP calls, but must not infer, emulate, or bypass kernel state
> transitions.

## 1. Purpose and architecture

A modern web interface for the Juice kernel, implemented in Go. It exposes the
Juice user flows and talks to the kernel **only over the documented HTTP API**
(`docs/API.md`); it never shells out to the CLI, reads the kernel database, or
imports kernel packages.

The UI is a **backend-for-frontend (BFF)**: a Go HTTP server that renders the
interface and proxies every request to the kernel API, holding the user session
server-side.

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

**The kernel gate.** The UI is the desktop to the kernel's server: a person who
installs the UI has a kernel. At start the listener answers at once with the
**kernel screen** and a loop acts on observable facts, in this order, until the
kernel answers `/health`, when the application takes the listener:

* the configured kernel URL is not loopback → **wait** (a kernel elsewhere is an
  operator's; it is never installed or started);
* `$JUICE_HOME/bin/juice` is missing or older than the release this UI was
  verified against → **install** it: the release this UI was
  verified against, the platform's asset and `checksums.txt` from the kernel's
  GitHub releases, refused without a matching SHA-256, tried with `--version`,
  renamed atomically into `bin/` (the contract of the kernel's own
  `install.sh`);
* no kernel home `$JUICE_HOME/kernels/<world>/` listens on the URL's port →
  **create**: a first-boot form (world, kernel name, superuser password; the
  world one of `$JUICE_HOME/worlds/*.json`, `play` on an installation never
  served, checked on the server) writes that home's `config.json` and serves
  the kernel once with the password and the superuser's recovery public key in
  the child's environment, the kernel's documented non-interactive first boot.
  The UI generates the recovery phrase as at signup, one for every attempt of
  the boot (a kernel that enrolled the key and then failed to serve keeps it),
  and shows it once the kernel answers, held in memory until acknowledged and
  stored nowhere. The screen is headed Juice installation and each field states
  its one consequence;
* otherwise → **start** `juice kernel serve <world>`, detached so the kernel
  outlives the UI; the kernel's own lock makes this safe beside an operator's
  start.

The first boot is a local act: the form, its post and the phrase page are
served only to a request from this machine (a loopback listener, a loopback
peer, no proxy header); any other request is told the terminal command. The
UI never removes a kernel. A UI configured with a named kernel or an
operator's running kernel sees no screen: the first fact is true.

**Navigation model.** The UI's navigation MUST present the juice CLI concepts as
a marketplace, not a raw command dump. The primary groups are **Home**,
**Inbox**, **Workshop**, **Wallet**, and **Settings**, plus **Network** (federation)
and **Admin**, both shown **only to the `sys` superuser**. Each maps to the same
CLI concepts as before: `home` is the dashboard over the others; discovery +
execution (`run` + the catalog) is reached from the search box on every page,
whose results page is `browse`;
`inbox` is `task` — work addressed to the user, carrying the sole attention badge;
`workshop` is the provider surface (`action` management + earnings + creation);
`wallet` is money evidence (`user` balances + `process` lifetimes + the `tx`
ledger + receipts); `settings` is account, preferences, and connected accounts;
`network` is federation
(peers), and `admin` is operator supervision. The relabelling grants no new
authority — the underlying routes and superuser gating are unchanged. The kernel
still creates and closes processes automatically; the UI MUST NOT ask the user to
manage that bookkeeping, but it
MAY surface a process's parked funds and awaiting-receipt state as evidence.

**Reference discipline.** Each cross-cutting rule is defined once and applies
wherever relevant without per-use citation: the API contract in §4, session and
token rules in §5, the visual language (the accent, button vocabulary, status signals,
feedback) in §7, the security rules (including inert rendering of counterparty
content) in §8. An obligation marked *Conditional* is indexed in §10; the feature
is mandatory only once its prerequisite is confirmed. Where a reference is
unavoidable it names the target as well as its number, so a stale number remains
detectable.

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
    and the nav-badge polling (on a configurable interval). Vanilla JS is limited
    to the schema→form renderer, the theme toggle, and a tab-visibility signal.
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
  OpenAPI import by name; the evidence an action read carries; ratings list;
  earnings folded from the owner's transactions.
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
  identity, and the obligations owed to it (§6.6). The kernel serves these over
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
* A **discovered** remote action is runnable from the UI as of 0.6.0: its
  catalog price is indicative, but the run pins the catalog's `quote_hash`, so
  terms that moved since the last discovery pass are refused before any funds
  are locked rather than charged silently (§6.2). Resolution still happens as a
  side effect of that first call; afterwards the cached proxy is an ordinary
  priced card.

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

So the UI reads the `kernels` half of the client records and **never reads or
writes `client/credentials/`**. Two consequences make this a rule rather than a
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
* When refresh fails, or the user is suspended, the session is cleared and the
  user returns to login with a clear explanation.
* Logout calls `POST /v1/auth/logout` to revoke the refresh token and drops the
  session.
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

* The UI MUST present a left sidebar whose entries are the primary groups
  (`Home`, `Inbox`, `Workshop`, `Wallet`, `Settings`, then `Network` and `Admin` for
  `sys`), default-expanded with labels, collapsible, with collapse state
  persisted server-side. The active group MUST carry `aria-current`. The `inbox`
  entry MUST carry its waiting-count task badge in every state — a constraint on
  the entry wherever it renders, not on any one surface. A single nav model, built
  once server-side, MUST drive the sidebar and the mobile drawer (and the search
  pill's commands), so they cannot drift.
* **Home is the dashboard** (`/`, where sign-in lands). It MUST show the
  viewer's available and parked balance, the waiting tasks addressed to them
  (read as `status=waiting`, never the whole history), their open sessions,
  and their most recent ledger entries — at most five rows each, every section
  linking to its full page. Recent activity MUST come from the ledger, which
  holds only the viewer's entries, not from the transaction list, which the
  kernel pages before its party filter. Home MUST NOT complete a task (the
  inbox is the one surface that spends on a task) and MUST NOT draw a balance
  chart (the kernel serves no series). Results and action pages belong to no
  nav group: they are reached from search.
* The account/identity summary (handle, balance) lives at the sidebar foot;
  nothing that is itself a primary destination sits in a popover. Preferences are
  not chrome: the theme picker MUST appear exactly once, on `settings`.
* Mobile MUST reflow to **a single menu affordance at the top left** opening the
  sidebar as a **left panel** carrying the **whole** nav — the same object the
  sidebar is, at a different width, not a different component. No fixed-size bar is permissible: the nav is
  unbounded, since every registered app contributes entries at runtime, so any bar
  would need an overflow, and the split between bar and overflow would be
  arbitrary and would shift as apps install — unlearnable. One model, two
  renderings: sidebar where there is room, drawer where there is not. The drawer
  MUST be reachable without JavaScript by the same `?form=`-style fallback used
  elsewhere. The closed affordance MUST carry the task badge, so the sole core
  attention signal survives the drawer being shut. (The core rule that money moves
  only through a named confirm is unaffected by which surface hosts a screen.)
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
  files — MUST gate every route on the superuser and MUST contribute no nav
  entry to anyone else. That authority is the operator's machine, which no
  kernel account confers: on a shared root, an ordinary session could otherwise
  edit and stop another person's agent, and anyone signed in could start a
  process on the host.
* The task badge MUST be the only attention-demanding affordance in the **core**
  UI; an app (a bundled module such as chat) MAY carry its own app-scoped badge on
  its nav entry. It MUST reflect waiting tasks whose `required_caller` is the
  signed-in user (a superuser lists everyone's), sourced from
  a single server-side poll of `GET /v1/tasks?status=waiting` per session (a
  per-session cache whose TTL matches the configured badge poll interval), fanned
  out to open tabs — not one polling loop per tab.
* All create and edit operations MUST open **floating modal forms** — native
  `<dialog>` elements opened client-side with `showModal()`. Each modal trigger
  MUST also be a real link (`?form=NAME`); without JavaScript the server renders
  the named dialog already `[open]` as an in-flow card, so every flow stays
  operable with JS off. The `?form=` value MUST be validated server-side against a
  per-page whitelist before rendering.
* The UI MUST be keyboard-navigable as a first-class affordance: `g`+letter
  section navigation, `/` to focus search, `?` for a shortcuts overlay,
  `j`/`k`/`Enter` to move through and open rows (table rows and list rows
  alike), and `Esc` to close the top layer. A rowable row's pointer target is
  its link: the row MUST NOT show a pointer cursor, since no click handler
  stands behind it.
* **Commands are rows of the search pill.** The command registry mirrors the
  CLI (gated: admin commands appear only for `sys`) and is delivered as inert
  JSON data (a `type="application/json"` block), never executable inline
  script, to satisfy the CSP. The pill lists the commands matching the typed
  text before the title hints, each marked `›` (chrome; a title row never
  carries the mark, §8.1) — at most three when titles also match, twelve rows
  in all; a leading `>` lists commands only, `>` alone all of them. A command
  is a link (a page, or a `?form=` URL the server renders open) or a submit
  button bound to the page's one hidden command form, so every command targets
  a real link or form and the keyboard layer adds no capability unreachable
  without it. There is no palette, chip or Ctrl/Cmd+K binding.

### 6.2 run — discovery, action page, run form

**Choosing by what is known.** The kernel serves a success rate, a rating share
and a price on every local row, and the UI renders them on every card. Those
signals MUST also be usable when choosing, or the feedback a buyer gives comes
back only as a number to look at:

* Browse MUST offer a sort switch — **relevance · rating · price** — through the
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
decisions. The UI MUST expose it, and MUST NOT let it become a second way to
spend:

* The command sits with the results it acts on, offered only when the planner is
  present and **free**: a priced native run on a person's behalf would spend
  from a page nobody confirmed (§8.2), so where it is priced the command is
  absent rather than charged for.
* The candidates posted are **exactly the refs on screen**. A set assembled
  server-side would be a claim about what was offered that nobody could check,
  and the kernel refuses the whole call on a bare ref it does not know, so only
  refs it served are ever sent back.
* The reply renders as **the ordinary action page**, with the proposed arguments
  filled into its ordinary fields and the ordinary confirm unpressed. Nothing
  about a proposal is privileged: the fields are editable, the kernel validates
  them as it validates any input, and the money still moves only through the
  button that names the amount.
* The page MUST say it is a proposal **whether or not the model said anything**,
  since a person must know they are reading a choice made for them. Anything the
  model did say is counterparty prose by construction: inert, and never beside
  the confirm (§8.1).
* It MUST NOT be reachable by GET with arguments in the URL: that would be a
  link that fills a money form for someone.
* Where the planner is absent or chooses nothing, the UI states the kernel's own
  reason and the page is unchanged. Nothing is inferred about why.

**Discovery.**

* The omnibox MUST be present on every page — it lives in a **persistent
  topbar** at the top of the content area (a compact search input reflows onto the
  browse page itself on mobile, where the topbar is hidden) — and MUST issue
  `sys/lookup` (required `query`, optional `limit`) via `POST /v1/run`. Results
  MUST render as **rows in one ranked column** (the rank is a sequence, so it
  is not spread over a grid), filling the container like every component. A
  row leads with a
  **square tile** (§6.2 media), then the action's **title** as the link with
  its chips beside it and the **price right-aligned** on that line, the ref
  (owner-emphasized) on its own line beneath, the description (at most two
  lines), and the **same reputation strip the action page uses** (icon +
  tooltip: success · rating · latency), price-less since the price is on the
  title line. The title is the row's one pointer target. The browse page is
  **headless** (no page-head)
  and shows a **flat catalog capped at 20 per page** with a pager (§7 pagination);
  there are no filter tabs.
* **Search hints.** While a person types, the search box MUST offer up to
  twelve **search hints**, matched in the browser as a static site searches its
  index: the page loads the whole catalog's titles once
  (`GET /search/catalog.json`: every page of `GET /v1/actions` — the caller's
  own actions, the live ones it may call, and those discovered on peers — each
  distinct title with its normalized key) and matches them on every keystroke,
  so typing sends no request. Matching folds accents, case and punctuation
  (decimal digits are kept), requires every typed word to begin a word of the
  title, and ranks titles beginning with the text first, then shorter ones. A
  hint is a query, not an action: choosing one MUST run the search for it
  (`/browse?q=`), exactly as if typed, and MUST NOT open an action page. Hints
  MUST NOT come from `sys/lookup`, a run at an operator-set price (§8.2). The
  catalog file holds the account's private titles, so it MUST NOT be stored by
  the browser (`Cache-Control: no-store`); the page fetches it once per load.
  Without JavaScript the box submits as before.
* **Search fallback.** When `sys/lookup` is priced or fails, the search MUST
  match the whole catalog by the hints' rules over title, name and description
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
* Catalog browse (`GET /v1/actions`, owner/name filters) MUST be available as the
  secondary path and the per-owner listing.
* On `ErrInvalidState` from `sys/lookup`, the omnibox MUST fall back to catalog
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
  MUST render each under its issuer, the provider first, through the one
  evidence partial the peer inspection uses (§6.6): uses, failures,
  corroborated, contradicted, and ratings as trade-backed and unverified counts.
  Nothing is summed, scored or coloured, and a page with no reports has no
  section.
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
  untyped); `format` `date`, `email` and `uri` set the input type (`date-time`
  is not mapped: `datetime-local` carries no zone and fails RFC 3339); an
  integer's exclusive bound becomes the next inclusive one. Nothing else is
  mapped (`multipleOf` would misread HTML's `step`, whose base is `min`;
  `pattern` is anchored differently in HTML).
* **The form is the schema's tree.** A nested object renders as a group of its
  properties under its label; an array as rows of its item, `max(minItems, 1)`
  to start, with Add and Remove; a map-shaped node (a schema under
  `additionalProperties`, or an array without `items`) as an empty JSON editor
  whose placeholder is the shape. A field is named by its JSON Pointer
  (`f_/messages/0/content`), its type by `t_<path>` — a container's hint says
  how its children nest — and its null choice by `n_<path>`, which nulls the
  whole subtree. The args are rebuilt from the form alone, with no schema read
  at submit: an empty container is omitted, a row left blank is dropped and the
  indices compact; an explicit `[]` or `{}` is typed in JSON mode. The map-shaped
  input as a whole uses the raw editor; a closed object with no properties is a
  confirm alone.
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
  or whose kernel refuses or cannot be reached, returns to browse with that
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
  lena/ocr-translate                     $0.012
  ─────────────────────────────────────────────
  document_url   [https://…            ]
  target_lang    [German             ▾ ]

  [ Run ($0.012) ]
  ┌─ result ──────────────────────────────────┐
  │  [frozen form of the result]  Receipt · ▲ ▼ │
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
* **parked** (`timeout`) → "Result pending — $N reserved". A call that was
  dispatched but has no signed outcome yet is **not** a failure and **not** a
  refund: the kernel keeps the funds locked and retries. Verified live — the
  balance moves from available to locked, and `meta` carries `process_id` and
  `pending_since`. Only the peer's signed answer settles it; no age expires it,
  so no date is stated. The card MUST carry the escrow-pending signal (§7), name
  the reserved amount through the money helper, and link to the process it
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
  as a frozen form (§7), its JSON behind the JSON toggle and a Download of the
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
* **Who asks** is what the row names. A row carries `created_by` (the creating
  action) and `action` (the completion target) when this kernel holds the task;
  a task held on another kernel is listed here without them, its `owner` the
  holding kernel's name, rendered as every kernel name is (§6.6: a petname
  whole, a key truncated). The card names what the row names and nothing else.
* The completion form MUST be built from the task's own `allowed_input` (the
  kernel-derived completion schema on a waiting task), never by reading the
  target action: a task may be parked on a `private`/`local` action the
  completer can never read.
* `partial_args` MUST render read-only; only `allowed_input` fields MUST be
  editable. When none are editable, the card MUST collapse to a single confirm.
* **Completion is two-branch — this is the single statement of the rule; §7 and
  §8 defer here.**
  * An **ordinary task** stays **calm and free of funds-mechanics narration** —
    no "parked," no "you pay N but it's reserved." The completer is doing
    requested work, not authorizing a spend from their own wallet (the money was
    reserved by whoever created the task), so the completion control is a
    **neutral `Complete`** that names **no** amount — a `verb (N)` money button
    here actively misleads. The card names only *who asks* and *what* and shows
    the sealed `partial_args` as an inert read-only detail.
  * A **payment task** IS a spend of the completer's own balance: a task on an
    `effect="transfer"` action draws the `amount` from the completer at
    completion (verified on 4dbfedd). The signal is the target action's
    **signed contract field `effect`**, read by the task's `action` address
    (`GET /v1/actions?ref=`), never the action's name. The read is made only
    for an address on this kernel — the transfer action is `local`, so a
    payment task always names one — and never dials a peer. Its confirm MUST be
    the money button naming the amount — the sealed `partial_args.amount` when
    bound, else the completer's own typed amount — and the card MUST name the
    recipient (`partial_args.target`) in the provenance face; the §8 rule
    (whose funds move, how much) applies in full, exactly as on a paid run.
    The two branches MUST NOT blend: a card is one or the other, decided solely
    by the kernel's `effect` field, never by name or heuristics. A row that
    names no action here is an ordinary task.
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

* Workshop MUST present, in order: earnings, action list, creation zone. Its
  action list is this kernel's actions (`?owner=` the viewer, or the whole
  catalog for the superuser); a discovered row is not one to manage and MUST be
  left out.
* Earnings MUST be computed in the BFF by folding the user's own transactions;
  each figure MUST link to its underlying transactions. Earnings, like every
  headline money figure (wallet, home, a session), MUST render through the one
  figures component: one block per figure (§7). The BFF MUST NOT
  reproduce kernel settlement.
* The action list MUST use `GET /v1/actions?owner=<self>`, include inactive
  actions, filter by state, and show kind, price, success rate, rating trend, a
  last-failure peek, an inline active/inactive toggle, and a **visibility**
  control (below).
* **Visibility** MUST be set via `PUT /v1/actions/{id}` with
  `{visibility: "private" | "local" | "public"}` — the kernel's three-way
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
  `POST /v1/processes/{id}/end`). "locked" is not
  a user-facing label. Parked is the served `locked` whole: it also holds what a
  call to another kernel stakes on its ticket (kernel P10), which no session
  itemizes, and the UI states the total rather than computing a remainder.
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
  side is the rail and renders as an em dash. The wallet shows the most recent
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
  execution. The two differ exactly when a payment task settles inside someone
  else's process (§6.3).
* **Registering a payout address takes the account's password**
  (`PUT /v1/me/blockchain-address {blockchain_address, signature,
  current_password}`), as a password change does. A wrong or missing one is the
  kernel's 401, checked before anything else; it MUST read as "Current password
  is incorrect." and MUST NOT end the session, which a 401 elsewhere does.
* **Every world pays out.** A withdrawal is offered wherever it has somewhere
  to go: on a world without addresses the operator records it (it lands
  `confirmed`, `tx_hash` `manual:<id>`), and on a world with addresses it waits
  for the user's registered address. Only the address form depends on the
  world. The withdrawals table is read on every world, to exhaustion.
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
  action ref, args and result as frozen forms (§7, the result with its
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

* **The roster is the whole known network.** `/v1/admin/peers` merges by public
  key, excluding this kernel, **counterparties** (`has_account`, a bilateral
  position) and **discovered peers** (a catalog: action count).
  Both render as the same list row (§6.2) under a `.tabs` filter
  (`counterparties` | `known network`), paged and **not polled**. A discovered
  card is the unresolved counterpart of a counterparty card, as §6.2's
  discovered action card is of a resolved one: a `known network` chip and **no
  account-dependent verbs** — verified live, `suspend` on an account-less key is
  refused. The kernel's **contact cache** is served for every roster row, both
  classes: `last_seen` (when a verified exchange last succeeded — catalog
  freshness) and `last_contact_failed_at` (when one last failed). Both render as
  muted dated text and never as a status pill, because the kernel states neither
  as a verdict: it holds two timestamps that only ever move forward, and it
  neither hides nor reorders a peer for having failed. The UI therefore reports
  them and draws no conclusion about whether a peer is up — the call-and-see
  rule below is unchanged. Every absent field — either timestamp, `about`,
  `petname` — is omitted, never rendered as zero, an empty string, or a
  placeholder.

* **A peer row carries no money.** A peer account is identity, attribution and
  moderation state, never a wallet: a foreign call is funded by the serving
  kernel and every obligation settles per call on the rail (kernel §D14, §P10).
  So a counterparty card states its names, its catalog count and the contact
  cache, and nothing else. A balance, a position or a credit figure here would
  be a fabrication.
* **This kernel's own credit position is the money fact on this page.** The
  identity card states what this kernel has delivered to foreign buyers and not
  been paid for, against the limit it set (`exposure` of `credit_limit`); the
  rates it serves under (`fee_bps`, `remote_bps`, `import_bps`), its ticket face
  and the largest ticket it will take from a buyer (`lottery_max`, above which a
  call is refused with a signed rejection); the operator split of `sys` into earnings and money in transit; and the
  solvency identity, whose `gap` MUST carry a danger pill when it is non-zero.
  A rail halt (`stop`) MUST render as a banner naming its reason, on this page
  because the money verbs it refuses are here.
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
    whole value recoverable (a `title`), never as a card title in full. This
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
* **Name is the gesture a discovered card carries**, as a link to the kernel's
  own page with the form open. It binds a petname: the kernel binds one
  automatically only on this kernel's own first outbound use, so an explicit
  bind is the only way to name a kernel merely discovered, and without it the
  operator is left typing a key. It is not account-dependent, while `suspend` on
  an account-less key is refused, so that verb stays off the card. **Inspect a
  kernel by key** remains the way in for a key learned elsewhere (verified live:
  an unknown key still returns a usable page).
* **A kernel is named on its own page.** The petname form lives on the inspect
  page, which is where a kernel is shown, and posts the **public key** — an
  unbound kernel has no name to key on, and advertised names collide. A user is
  renamed on the user page. The two forms are separate because the kernel's two
  routes are, and a single form would have to guess which namespace was meant.
* **Work addressed to this kernel reaches its operator's inbox.** A peer
  addresses a task to the kernel as `sys@<kernel>`, and it is listed in `sys`'s
  inbox and completed there like any other (§6.3); this page lists none. An
  inspected action leads with its title, then its address, which renders as
  every address does (§4), unlinked. Reachability states the path, one ping's
  round trip, and the peer's version.
* **Evidence, not hearsay.** Inspect MUST render the kernel's retained evidence
  in **two views, never folded**: an **execution summary** from the rows the
  subject issued about itself, and **counterparty experience** from the rest,
  grouped by issuer and carrying the **corroborated** count. Each row names its
  subject by its action id (§10) and states uses, successes, failures, and the
  two rating counts distinctly: **trade-backed** (the two-kernel receipt link
  holds) and **unverified**. Counts are text; no new colour, no status fill —
  evidence is a trust fact, not a state. Attribution is provable and honesty is
  not, so the UI MUST NOT present evidence as a verdict or fold it into the
  first-party reputation strip.
* **A cached proxy is kernel-managed state.** Activation is set by resolve and
  cleared by the kernel (verified live: `enable`/`disable` on a `kind=remote_proxy`
  action returns `ErrInvalidState`, "use admin suspend to block a peer"), so the
  UI MUST NOT offer lifecycle verbs — enable/disable, visibility, delete — on a
  proxy row anywhere it lists actions, including the superuser-scoped workshop.
  The peer-level lever is suspend, on this page.
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

**No value-transfer queue.** A transfer to another kernel settles in the
sender's own call and is paid on the rail like a won ticket (kernel §D18, P11);
the kernel serves no queue and the UI carries none. Its one visible state is
the transaction's `payment` (§6.5).

## 7. Visual language and non-functional requirements

* **The accent is exclusive.** The accent MUST appear only on the control that authorizes a
  settling `Call` paying a provider — the terminal, priced, parties-visible
  confirm. A control that opens a confirm or otherwise moves no money MUST NOT use
  the accent.
* **Button vocabulary (closed set).** Spend: the accent, solid, names the amount
  as `verb (N)` (e.g. "Run (133.000000)") — the same figure the money helper
  renders everywhere. Primary
  non-spend: solid ink. Secondary: outline/ghost. Destructive: maroon, distinct from the accent —
  Delete, Disable, Decline, and force-end; force-end MUST name the refund, not a
  price. No other button styles exist. (A task completion is two-branch per
  §6.3: an ordinary completion is the neutral `Complete` naming no amount; a
  **payment task** completion is a genuine spend and uses the money button.)
* **Money and identity typography.** Money, IDs, hashes, and `owner/name` MUST
  render in **monospace with tabular figures** (the provenance face), regardless
  of the surrounding proportional chrome.
* **The frozen form is the one rendering of a JSON value a person reads** — a
  result, a receipt's args and result, a task's sealed `partial_args`, the
  receipt-verification checks. It is the run form's anatomy (§6.2) read-only,
  so a result looks like the form that produced it: a key is a label over a
  box with the input's geometry, in the read-only register (no fill, a hairline
  border) so it cannot be taken for an input, holding the value's text with
  its line breaks;
  a nested object is a group under its legend; an array is its rows; a number
  is its literal, a boolean, `null`, `{}` and `[]` their words, muted. Keys
  keep the order served. A string past 800 characters folds (closed) behind its
  first line. Nothing is submittable. The raw JSON sits behind the same JSON
  switch the form carries — a labelled switch (`role="switch"`) at the block's
  top right, swapping the fields for the JSON — with a Download where the value is a
  transaction's result.
  Nothing in it is interpreted (§8.1).

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
  and `symbol` (`credits`, `USDC`). The kernel also publishes ONE rendering, and
  the UI MUST use it rather than invent a second: the amount, its trailing zeros
  trimmed to a **two-digit floor**, **no thousands separators**, the world's
  symbol appended — `133 credits`, `1.50 USDC`, `0.000133 USDC`, `-2500 credits`.
  Separators are absent by design: the text is meant to paste back into an amount
  field unchanged. It renders through **one** helper (`money`) reading the live
  identity — never a currency mark the UI chose, never a `¤` or gem.

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
* **Pagination.** No list renders unbounded. Every long list — browse, the
  transaction ledger, the workshop management table, the admin roster, an action's
  ratings, and open/closed sessions — MUST page with `limit`/`offset` at **20 per
  page** through **one shared, quiet pager** (`← previous · page N · next →`; a
  boundary renders a dimmed non-link). The pager carries no accent (money-reserved).
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
* **Icons.** One set (Heroicons, outline/solid for inactive/active), recolorable
  to tokens, never emoji. Every icon-only control MUST carry an `aria-label`;
  hover tooltips are enhancement only.
* **Feedback.** There MUST be no notification feed, bell list, or toast stack;
  outcomes render in place. The UI MUST provide exactly one polite `aria-live`
  region for ephemeral acknowledgement of the user's own just-completed action
  (e.g. "Action disabled," "Copied," the 429 "slow down"): no history, no
  stacking, no counterparty text, auto-dismissing. The task badge is the sole
  async attention signal in the core surface; an app MAY add its own app-scoped
  nav badge.
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
refresh, logout-revocation, and cookie/CSRF rules of §5, and additionally MUST:
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
   a button; a result, args or sealed `partial_args` only through the frozen form
   (§7), whose structure is the JSON type and never the value: no value is
   linked, formatted or turned into an icon, a boolean is its word. The chrome
   vocabulary defined in §7 MUST NOT be applied to counterparty content. Trust affordances
   MUST key on kernel-captured fields (transaction parties, process owner, a
   task's `required_caller` and `action`), never on payload shape.
2. Route every act that moves the user's own money — run (including a run of a
   value-bearing action, which is how money is sent, §6.5), payment-task
   completion (§6.3), force-end — through a confirm whose
   button names the amount or refund. An ordinary task completion moves none of
   the completer's money and stays neutral (§6.3, the single statement). No
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
4. Show, on a payment-task card before the confirm, whose funds move, how much,
   and the sealed `partial_args`; an ordinary task card shows the sealed
   `partial_args` and who asks (§6.3).
5. Render receipt-verification failures with the failed check named, and MUST NOT
   display `valid` except from the kernel's own per-check response.
6. Hold no durable state but sessions, compose only documented HTTP calls, never
   infer kernel state transitions, and surface 429 as a "slow down" message.

## 9. Testing

Every production `.go` file has a `_test.go` file with independent tests.
`go test ./...` passes without network access; the kernel API is faked as an
`httptest.Server`. Required coverage:

* the typed API client and error mapping (§10 error mapping);
* session lifecycle: PKCE handshake, single-flight refresh rotation, logout
  revocation, suspension → clean logout;
* CSRF protection on every state-changing route;
* JSON-Schema-to-form rendering, including `partial_args` lock-out on task cards
  and the `allowed_input`-sourced completion form (target action unreadable →
  form still renders);
* the two completion branches: ordinary tasks neutral (no amount anywhere);
  payment tasks render the money button naming the amount and the beneficiary
  (§6.3), decided solely by the target action's signed `effect` field, read by
  address and only for an address on this kernel;
* decline: offered to the required caller and the owner of a waiting task and
  to nobody else; a task held on another kernel names its holder and links no
  receipt after completion (§6.3);
* list reads ask for what they show: inbox history and the sessions page send
  `?all=1` (or the tab's status) and page to exhaustion (§6.3, §6.5);
* the payout address form sends the password, and a wrong one keeps the session
  (§6.5); a `transfer <tx>` reason links only for the account debited, and a
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
* the money unit: amounts scale by the world's `decimals` and always show six
  fraction digits; an amount finer than the base unit is refused, not rounded;
  and no template renders a currency symbol (§7, drift guard);
* the session pin: a login records the kernel's key and network fingerprint, an
  address that answers for another kernel ends the session, and a kernel that
  cannot identify itself cannot be signed in to (§5);
* target-based mutations: enable/disable/update/delete name an id or a path and
  the UI reports the rows returned, not a predicted state (§6.4);
* a discovered action resolves on open and lands on the ordinary action page;
  there is no by-ref run surface (§6.2);
* the wallet's world: the pay-in copy and the address form follow whether the
  world has addresses, a withdrawal is offered on every world that has somewhere
  to pay it, and the token is named where the world has one (§6.5);
* the activity is the ledger: each entry states its counterpart, sign and
  reason, a transaction id linking to its receipt and any other reason inert
  text (§6.5);
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
  route gated, and no nav entry offered to anyone else (§5, apps);
* the single `aria-live` feedback region and the absence of any notification feed.

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
* **Native additions (`sys/random`, `sys/llm/embed`).** Not in the final kernel
  spec; conditional, not assumed. No current UI feature depends on them.
* **The agents app's home path — deviates, and not ours to move.** The UI
  scaffolds `$JUICE_HOME/agent/agents/<handle>/`, which is what the juice-agent
  daemon's own specification (§11 there) says it reads, while
  `ecosystem-standard.md` §2 gives components `$JUICE_HOME/agents/<name>/`. The
  UI writes the files a daemon reads, so it must follow that daemon and cannot
  lead it: the move belongs to the agent's repository first, and this line
  stands until it happens.
* **The planner (§6.2).** Confirmed on 25c07cd against a local model:
  `sys/llm/decide` takes `{messages, actions}` and answers
  `{action, args, message?}` — `message` optional and usually absent. It selects
  and never executes. A dead kernel-qualified candidate among live ones is
  discarded; a **bare unknown** ref refuses the whole call (404), so the UI
  sends only refs the kernel served. With no model reachable it fails as an
  ordinary outcome (`execution_failed`, "model did not select an action"), which
  the existing degrade-honestly path renders as the kernel's own reason.
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
  (4dbfedd), and the completion form derives from it alone. Nothing on a task
  row marks a payment; payment tasks are detected by the target action's
  `effect` field, read by the row's `action` address (§6.3).
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
* **Chat's doorbells — blocked (apps).** The kernel refuses to park a task on a
  trace that has settled — re-confirmed on 4dbfedd on both paths: the
  capability answers `403 capability expired: call already settled`, a token
  `409 process is closed` — and chat mints its verification task and every
  doorbell off the outbox seed trace, which settles when the seed call returns.
  Registration, and so the rooms gate, fail on this kernel until chat holds a
  trace that is still live when it mints.
* **Chat's contract follows the canonical form (apps).** The kernel stores
  every schema canonical — an object naming its properties closed with
  `additionalProperties: false` — and rewrites stored rows into it at boot
  (verified on ba7f5e5: the rewrite keeps the action live and its grants
  intact). Chat declares exactly that form, so a reconcile compares equal and
  rewrites nothing: a contract update deactivates every chat action and revokes
  every member's grant, and members connect again through the rooms gate. A
  closed output also refuses a reply carrying an undeclared field, at zero
  charge, so the daemon's replies are its declared outputs. A title differing
  alone — the kernel titled chat's rows with the last segment of their names —
  is sent alone, which keeps the action live and its grants intact.
* **Kernel contract defects (informative).** `juicedocs/API.md` still describes
  a parked call's `refund_eligible_at` and an automatic refund, which 4dbfedd
  serves neither of; its R6 breaks off mid-sentence; and it says a task
  addressed to a kernel names it alone, where the kernel refuses a bare name and
  the operator is `sys@<kernel>`. The UI follows the measured kernel.

**Error mapping (informative).** `ErrUnauthenticated` (incl. suspension) → clean
logout with explanation; `ErrUnauthorized` → not-permitted message; `ErrNotFound`;
`ErrInvalidInput`/`ErrSchemaViolation` → field-level form errors; `ErrInvalidState`
(also the kernel's refusal of a lifecycle verb on a remote proxy, §6.6 — which the
UI does not offer, so this is a defence, not a flow);
`ErrInsufficientFunds` → a plain not-enough-balance message (how money enters is
the wallet's disclosure, §6.5); `rail_stopped` → payments out are paused while a
rail payment is blocked, and deposits, calls and reads continue;
`peer_unreachable` → the peer was offline, the call refunded, retry later (the
kernel sends no `meta` here, so the UI names the peer from the ref it submitted
or names none);
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

The interface has four rooms and a ledger rather than one page per resource
because a person arrives to do one of three things — get something done, answer a
decision owed to them, or sell a capability — and everything else is evidence
about those activities. Money borrows the most over-tested patterns on the web:
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