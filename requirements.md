# Juice Web UI Requirements

Version: 0.6.4
Status: design draft — endpoint surface gated against the running kernel

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

**Navigation model.** The UI's navigation MUST present the juice CLI concepts as
a marketplace, not a raw command dump. The primary groups are **browse**,
**inbox**, **workshop**, **wallet**, and **settings**, plus **network** (federation)
and **admin**, both shown **only to the `sys` superuser**. Each maps to the same
CLI concepts as before: `browse` is discovery + execution (`run` + the catalog);
`inbox` is `step` — work addressed to the user, carrying the sole attention badge;
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
  variables (env wins), over safe local defaults. At minimum: kernel API base URL
  (default `http://127.0.0.1:4040`), the UI listen address, and a session secret.
  Commit no secrets. Reject invalid startup configuration clearly.
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
  execution (`POST /v1/run`), including run-by-ref from the palette.
* **browse / workshop** — catalog browse plus owner action management: create,
  list/show (including inactive, via `GET /v1/actions?owner=<self>`), update,
  enable, disable, delete; three-way `visibility` (private/local/public);
  OpenAPI import/unimport; stats; ratings list; earnings folded from the owner's
  transactions.
* **process** — computation lifetimes (`GET /v1/processes`, show, force-end),
  surfacing parked funds and awaiting-receipt state.
* **tx** — the immutable ledger: transactions list/show/rate; receipt
  verification (`GET /v1/transactions/{id}/receipt-verification`), on demand.
* **step** — steps: list/show/complete. Step creation is API and app-module
  surface only (§6.3).
* **user** — self profile, balances (`GET /v1/me`, `available`/`locked`), and
  update (`PUT /v1/me`); the ledger of the user's own transactions.

*Conditional:* the trace tree on the receipt page.

* **Admin / supervision (superuser only):** the `admin` group, visible only to
  `sys`, covers user list/show, suspend/unsuspend, rename (§6.6), credit
  deposits and withdrawals, federation trust (peers — provisioned by use or by deposit-by-key,
  inspect, suspend/unsuspend, this kernel's identity), and operator settlement
  (§6.6). The kernel serves these over HTTP on
  `/control/*`, gated by an `IsSuperuser` check on the `sys` bearer (API.md
  C13); the UI is an ordinary client of that surface. Superuser **scope** on
  the normal list pages (a `sys` session sees all actions/processes/tx/steps)
  is indicated quietly on those pages, not duplicated as separate admin
  listings.

### 3.2 Out of scope (no user-facing flow)

* **Trace inspection:** no trace-read HTTP route is confirmed, so the Engine-room
  trace tree stays conditional (§10).

### 3.3 Known limitations

* Because deposits are admin-only, a UI user cannot top up their own balance.
  Balance views MUST state plainly that credit top-ups happen out-of-band
  (operator/CLI). The UI still shows balances and funds processes from existing
  credits. Peer accounts differ: a peer's balance is a bilateral position that
  may go negative under the kernel's global exposure cap, and settling it is an
  operator act (§6.6) — never a user-facing top-up.
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
* Action identity is bare `owner/name`, parsed by a **single split on the first
  `/`** — owner segment before, name (the remainder) after. Never split on every
  `/`: names may contain `/` (e.g. `sys/llm/chat`), and the rule is unambiguous
  because handles cannot contain `/`. If the owner segment contains `@`, the
  part after it names a **kernel**: `owner@kernel/name` is a remote action,
  `kernel` a presentation-only local alias for a peer key — never an identity
  and never a key for anything in the UI. Handles are bare (`sys`, `alice`); the
  UI **emits** bare names everywhere and **MUST NOT rewrite** a name on input.
  The migration period is over: `@` separates a kernel, so a sigiled handle,
  ref, or selector is ill-formed input the kernel rejects — the UI passes it
  through and surfaces that typed error, because silently reading `@alice/greet`
  as `alice/greet` would make a UI-only dialect of a grammar the CLI and kernel
  share. In a URL, `owner/name` is percent-encoded.
* Action read/list responses carry both `id` (UUID, for management) and a
  computed `action` (`owner/name`, for calling). The UI uses `action` directly
  in call requests and never asks the user to type a UUID.
* State-mutating responses return the updated resource; the UI re-renders from
  that response. Destructive operations and `end` return 204.
* List responses are bare arrays; pagination metadata is in headers.
* `POST /v1/run` and `POST /v1/steps/{id}/complete` always include `args`; empty
  input is `{}`.

## 5. Authentication and session

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
  (`browse`, `inbox`, `workshop`, `wallet`, `settings`, then `network` and `admin` for
  `sys`), default-expanded with labels, collapsible, with collapse state
  persisted server-side. The active group MUST carry `aria-current`. The `inbox`
  entry MUST carry its waiting-count step badge in every state — a constraint on
  the entry wherever it renders, not on any one surface. A single nav model, built
  once server-side, MUST drive the sidebar and the mobile drawer (and the command
  palette), so the three cannot drift.
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
  elsewhere. The closed affordance MUST carry the step badge, so the sole core
  attention signal survives the drawer being shut. (The core rule that money moves
  only through a named confirm is unaffected by which surface hosts a screen.)
* The step badge MUST be the only attention-demanding affordance in the **core**
  UI; an app (a bundled module such as chat) MAY carry its own app-scoped badge on
  its nav entry. It MUST reflect waiting steps for the signed-in user, sourced from
  a single server-side poll of `GET /v1/steps?status=waiting` per session (a
  per-session cache whose TTL matches the configured badge poll interval), fanned
  out to open tabs — not one polling loop per tab.
* All create and edit operations MUST open **floating modal forms** — native
  `<dialog>` elements opened client-side with `showModal()`. Each modal trigger
  MUST also be a real link (`?form=NAME`); without JavaScript the server renders
  the named dialog already `[open]` as an in-flow card, so every flow stays
  operable with JS off. The `?form=` value MUST be validated server-side against a
  per-page whitelist before rendering.
* The UI MUST be keyboard-navigable as a first-class affordance: a Ctrl/Cmd+K
  command palette whose entries mirror the CLI (and are gated — admin commands
  appear only for `sys`), `g`+letter section navigation, `/` to focus search,
  `?` for a shortcuts overlay, `j`/`k`/`Enter` to move through and open table
  rows, and `Esc` to close the top layer. The command registry MUST be delivered
  as inert JSON data (a `type="application/json"` block), never executable inline
  script, to satisfy the CSP. Every palette command MUST target a real link or
  form, so the keyboard layer adds no capability unreachable without it.

### 6.2 run — discovery, action page, run form

**Discovery.**

* The omnibox MUST be present on every page — it lives in a **persistent
  topbar** at the top of the content area (a compact search input reflows onto the
  browse page itself on mobile, where the topbar is hidden) — and MUST issue
  `sys/lookup` (required `query`, optional `limit`) via `POST /v1/run`. Results
  MUST render as ranked cards, each with a **media slot** (§6.2 media), the ref
  (owner-emphasized) with **price right-aligned** in the title row, a one-line
  description, and the **same reputation strip the action page uses** (icon +
  tooltip: success · rating · latency), price-less on the card since price is
  right-aligned in the title row. The browse page is **headless** (no page-head)
  and shows a **flat catalog capped at 20 per page** with a pager (§7 pagination);
  there are no filter tabs.
* **Media slot.** Every action card and the action page MUST carry a 16:9 media
  slot. Until the kernel serves images (*Conditional*, §10; tracked in
  `ROADMAP.md`), the slot MUST render a **deterministic generated fallback**
  (seeded from the action's id/owner/kind, palette-constrained, produced
  server-side as inline SVG — no external fetch, no `style=`, CSP-safe). When a
  kernel image lands it MUST override the fallback. The media is decorative
  counterparty content: kernel-captured chrome (ref, price, reputation) MUST
  render **outside** the media box (§8).
* Price MUST appear on each card. Sort and filter by price MUST be available.
* **Discovered results.** `sys/lookup` fuses the gossip discovery cache for an
  authenticated local caller, so results MAY name a remote action this kernel has
  not resolved: the ref is kernel-qualified (`owner@<kernel-key>/name`) and the
  `action_id` is the **remote** id, which resolves to nothing locally. Such a card
  MUST be rendered from the lookup row alone, and MUST show the kernel-qualified
  ref (owner segment unlinked, §6.2 refs), the description, and a quiet chip
  marking it as discovered, distinct from the remote-proxy chip. It MUST NOT show
  a reputation strip (the discovery cache stores none).
  **A row is discovered iff its `action_id` names no local action** — the UI
  attempts the ordinary read and falls back when it 404s. A kernel-qualified ref
  MUST NOT be the test: once resolved, a proxy keeps its remote ref *and* gains a
  local row with real stats, so reading discovery off the ref would strip a card
  the buyer has already paid for down to a stranger's.
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

* MUST show a hero pairing the media slot (§6.2 media) with the ref, owner block,
  reputation strip, and price in large type, then description and two schema
  panels rendered from `input_schema`/`output_schema` as field lists (name, type,
  required, description), with raw JSON behind a toggle.
* The reputation strip MUST show four metrics — runs, success rate, rating, mean
  latency — each with an icon, value, and a tooltip that is also the icon's
  `aria-label`. The rating MUST show mean and count.
* Ratings MUST render as reviews (0/1 value, `note`, newest first, paginated),
  labeled as ratings from paid calls.
* The owner segment of `owner/name` MUST be visually emphasized and link to an
  owner profile (public actions, aggregate stats, account age). A **remote** ref
  (`owner@kernel/name`) renders the whole owner segment in the same emphasized
  monospace but MUST NOT link it anywhere: the kernel alias is presentation-only
  and there is no local profile behind a remote owner (§4; extends §8's inert
  counterparty rule). Card-art hue keys off the owner segment string; the 0.4.0
  de-sigiling reshuffles it once, and no compatibility shim is permitted.

**Run form.**

* The action page has ONE anatomy: hero → description → an **inline run form**
  (on the page, never a modal) → the result as a card **attached directly below
  the form** → ratings. There is no floating/opener control and no inert
  Method/URL block (an HTTP action's source is editable by its owner inside the
  Edit modal only).
* Inputs MUST be generated from `input_schema` (typed fields, required markers,
  descriptions as help text, client-side hints only; the kernel validates). A
  raw-JSON input MUST be available.
* **A run MUST pin the quote it was shown.** Every action read and every
  `sys/lookup` row carries `quote_hash`, a fingerprint of the quoted terms
  (price, description, schemas, effect). The form carries it as a hidden field
  and the handler MUST read it back **from the request** — never from a fresh
  read of the action, which would always match and silently disable the
  protection. If the terms moved, the kernel refuses with `terms_changed` before
  locking anything, at zero charge; the UI re-renders the form pinned to the
  terms just quoted, so accepting them is one more click (§6.2 outcomes). Both
  run surfaces (by id and by ref) MUST pin, or one would accept moved terms
  while the other refused them.
* **A discovered action is runnable.** It has no local row, so its page is built
  from the lookup row (ref, description, schemas, indicative price, quote hash)
  and its run posts **by ref**. The pin is what makes this honest: the catalog
  price may be a discovery interval stale, and pinning means the buyer is
  refused rather than charged a figure they were not shown. *(This lifts the
  §3.3 limitation: first contact no longer requires the CLI.)*
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
  accent conveys credits — no glyph on the button), and the form MUST disclose
  "Maximum, all-in. Refunded in full if it fails." A **first payment** to this
  owner MUST show a quiet first-payee hint (§8.3). A **free** run is the same
  form with a neutral `Run` button.
* The receipt MUST offer one-click re-run with the same args.

```text
  lena/ocr-translate                     $0.012
  ─────────────────────────────────────────────
  document_url   [https://…            ]
  target_lang    [German             ▾ ]

  [ Run ($0.012) ]   Maximum, all-in. Refunded if it fails.
  ┌─ result ──────────────────────────────────┐
  │  Success.   { … }        Receipt · ▲ ▼      │
  └────────────────────────────────────────────┘
```

**Outcomes.** Each outcome class MUST render distinctly. The classes divide on
one question — what happened to the money — so no two may share a sentence:

* input-schema rejection → "Not sent. You were not charged"; field-level errors
  inline on the sheet; no receipt.
* failure → "Failed — fully refunded"; the refund reflected in the balance; a
  link to the failure receipt. **The sentence is the UI's, never the kernel's
  string:** since v0.12.14 the kernel classifies rather than narrates (a remote
  failure answers `execution_failed`), and a class explains nothing to a buyer.
  Each error class maps to exactly one sentence, in one place; a raw class MUST
  NOT reach a template (enforced by drift guard).
* **parked** (`timeout`) → "Result pending — $N reserved". A call that was
  dispatched but has no signed outcome yet is **not** a failure and **not** a
  refund: the kernel keeps the funds locked and retries. Verified live — the
  balance moves from available to locked, and `meta` carries `process_id`,
  `pending_since`, and `refund_eligible_at`. The card MUST therefore carry the
  escrow-pending signal (§7), name the reserved amount through the money helper,
  link to the process it names, and state the date from which the automatic full
  refund applies — as eligibility, never as a promise of settlement. It MUST NOT
  claim a refund has happened, and MUST NOT poll the peer (§6.6).
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
* success → the result (formatted, foldable, downloadable JSON); a receipt
  summary (gross paid / net to provider / fee captioned "on value added"); an
  inline rate control (0/1 + optional note).

Long-running calls MUST show elapsed time against the action's historical latency
and MUST NOT show an indefinite spinner. If a call returns with the process still
open (parked steps), the result MUST say so and link to the Wallet.

### 6.3 step — the inbox of work addressed to you

* A waiting step whose `required_caller` is the signed-in user MUST render as a
  completable card; all other steps MUST go to a history table. Filters by
  `process_id` and `status` MUST be available (`GET /v1/steps`); a single step is
  `GET /v1/steps/{id}`, including the computed `action`.
* The completion form MUST be built from the step's own `allowed_input` (the
  kernel-derived completion schema on a waiting step), never by reading the
  target action: a step may be parked on a `private`/`local` action the
  completer can never read, so a 403 on the target action is a normal state and
  MUST degrade silently (the action read is decoration, not a dependency).
* `partial_args` MUST render read-only; only `allowed_input` fields MUST be
  editable. When none are editable, the card MUST collapse to a single confirm.
* **Completion is two-branch — this is the single statement of the rule; §7 and
  §8 defer here.**
  * An **ordinary step** stays **calm and free of funds-mechanics narration** —
    no "parked," no "you pay N but it's reserved." The completer is doing
    requested work, not authorizing a spend from their own wallet (the money was
    reserved by whoever created the step), so the completion control is a
    **neutral `Complete`** that names **no** amount — a `verb (N)` money button
    here actively misleads. The card names only *who asks* and *what* (the
    creating action, quietly) and shows the sealed `partial_args` as an inert
    read-only detail.
  * A **payment step** IS a spend of the completer's own balance. Verified on
    the running kernel: a step on an `effect="transfer"` action draws the sealed
    `amount` from the completer at completion; the federation `payment`
    descriptor exists only on the peer wire, so on `/v1` the signal is the
    target action's **signed contract field `effect`** (from
    `GET /v1/actions/{action_id}`), never the action's name. Its confirm MUST
    be the money button naming the amount — the sealed `partial_args.amount`
    when bound, else the completer's own typed amount — and the card MUST name
    the recipient (`partial_args.target`) in the provenance face; the §8 rule
    (whose funds move, how much) applies in full, exactly as on a paid run.
    The two branches MUST NOT blend: a card is one or the other, decided solely
    by the kernel's `effect` field, never by name or heuristics. (Effect-bearing
    actions are public natives or `local` proxies by construction, so the
    completer can always read the field; if a future kernel makes one
    unreadable, the card falls back to ordinary rendering and the kernel remains
    the sole funds enforcer.)
* Completion MUST POST to `/v1/steps/{id}/complete` with `args` (default `{}`)
  and MUST render `{result, tx_id, trace_id, step_id}` through the run outcome
  display (§6.2). Completion MUST be offered only to the `required_caller`;
  duplicate and non-caller attempts MUST map to typed-error messages.
* Completed and cancelled steps MUST appear in history; cancelled cards MUST
  state the reason.
* `sys/message` steps MUST render as a message (sender, text, acknowledge
  button).
* Step creation MUST NOT be a browser form. `POST /v1/steps` binds a live
  funding trace, and a trace id is exactly the process bookkeeping this UI keeps
  hidden (§6.5) — a form demanding one is unusable by the person the inbox is
  for. Steps are composed by actions and app modules through the kernel API; the
  inbox is work addressed to you: list, read, complete.

### 6.4 action — catalog and provider workspace

* Workshop MUST present, in order: earnings, action list, creation zone.
* Earnings MUST be computed in the BFF by folding the user's own transactions;
  each figure MUST link to its underlying transactions. The BFF MUST NOT
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
* Editing source, schema, kind, or price MUST warn that saving deactivates the
  action until re-enabled, matching the kernel's contract-drift rule.
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
  (idempotent) → created/updated/deactivated results. Reimport MUST be presented
  as "changed → deactivated; re-verify and re-enable." Unimport
  (`POST /v1/actions/unimport`) MUST deactivate matching actions without deleting
  history. Upstream credentials (`auth_json`) MUST be entered in write-only
  fields, never re-displayed, shown afterward only as "configured ✓ (replace)".
  *Conditional:* a UI-driven ownership-proof flow for public activation; until
  then the UI MUST surface the kernel's rejection rather than drive a proof.

### 6.5 user & tx — money, ledger, and receipts

* The Wallet MUST show two balances (credited / escrow-pending signals):
  **available** and **parked** (all locked funds; the open-sessions list itemizes
  them, each with a force-end control naming its consequence — cancels waiting
  steps, refunds parked prices — via `POST /v1/processes/{id}/end`). "locked" is not
  a user-facing label.
* The wallet MUST link to the full **sessions** page (`GET /v1/processes`, open +
  closed), which itself MUST offer the same force-end control on each open row and
  paginate (§7). A session awaiting a remote receipt MUST show that state and its
  age from the kernel's `awaiting_receipt`/`awaiting_receipt_since` — the standing
  evidence behind a parked run (§6.2).
* **Activity is one list, folded from both records the kernel keeps.** Money
  leaves a user's balance through two surfaces that do not share a log: the
  ledger (`GET /v1/ledger` — deposits, withdrawals, direct transfers, and value
  delivered by a `transfer`-effect call) and transactions
  (`GET /v1/transactions` — every call). Neither is a superset: a plain run
  appears only in the second, a deposit and an incoming transfer only in the
  first. The BFF MUST therefore merge both into a single time-ordered activity
  list — an arrangement of served rows, never a recomputation — each row
  linking to its receipt or ledger entry. The wallet shows the most recent 10
  and links to the paginated ledger.
* **A value-bearing call is two records, and renders as two rows.** Settling a
  `transfer`-effect call writes a ledger entry from the immediate caller to the
  beneficiary alongside the ordinary transaction (verified live on v0.12.24;
  the entry's `reason` is the settling transaction's id). The transaction states
  what the *call* cost — zero for a price-0 action — and the entry states what
  *moved*. The UI MUST NOT fold them into one row or reconcile one against the
  other: an action whose effect is a money movement is an ordinary action that
  produces both records, and the activity list shows each as what it is. The
  journal is also the only surface the recipient has, since the beneficiary is
  no party to the transaction and cannot read it.
  The `reason` MUST NOT render as prose: it is an id, not a sentence a person
  wrote. The UI MUST still NOT manufacture a money figure out of a
  transaction's `args` — a displayed amount is a kernel-vouched fact or it is
  not displayed (§7); the entry is now that fact. Where the kernel additionally
  serves `value`/`value_to` on a transaction, they render as breakdown rows,
  zero-suppressed, and the payer named beside them is the transaction's
  **`caller_handle`** — the immediate caller, whose balance the value actually
  leaves — never the `owner_handle` that pays for execution. The two differ
  exactly when a payment step settles inside someone else's process (§6.3).
* **Sending money is running an action; it gets no surface of its own.** The
  kernel moves value through `sys/transfer`, an ordinary priced action with no
  special privileges (kernel §9: any provider could have supplied an equivalent),
  so the UI MUST NOT carry a bespoke send form, route, command, or price helper
  for it. It is found, priced, quoted, run, and receipted exactly like every
  other action, through the surfaces of §6.2. The bare `POST /v1/transfers`
  endpoint remains outside the UI (fee-less plumbing the kernel keeps for other
  clients).

  **No action may be special-cased anywhere in the UI.** A clause keyed to one
  action's name is a defect, not a feature: it makes the interface disagree with
  the platform it presents, and it multiplies — one exception invites the next.
  Where behaviour genuinely differs, it MUST key on a kernel-served field the
  action carries (`effect`, `kind`, `requires_grant`), never on a ref.
* The transaction list (`GET /v1/transactions`) MUST return rows where the user
  is buyer, caller, or seller. The kernel renders each party as a handle —
  `owner_handle`, `caller_handle`, `target_handle` — and returns no raw user id,
  so the UI compares handles. Each row MUST show the user's role, the action
  (`owner/name`, linked via `GET /v1/actions/{action_id}`; a truncated ID if the
  action was since deleted), status, and cost breakdown.
* The receipt page MUST show parties (buyer/seller, viewer's role highlighted),
  action ref, inline args and result, gross/fee/net (fee captioned "on value
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
* Balance views MUST state that top-ups are out-of-band.
* *Conditional:* an Engine-room disclosure below the receipt showing the trace
  tree with per-call cost and `latency_ms`.

### 6.6 network & admin — federation money

Superuser-only (`sys`), an ordinary client of the kernel's `/control/*` surface
(§3.1). Two concerns live here: the roster and settlement.

**Peers.**

* **The roster is the whole known network.** `/control/peers` merges by public
  key, excluding this kernel, **counterparties** (`has_account`, a bilateral
  position) and **discovered peers** (a catalog: action count).
  Both render as the same `.kernel-card` under a `.tabs` filter
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
  rule below is unchanged. Every absent field — either timestamp,
  `peer_credit`, `about`, `petname` — is omitted, never rendered as zero, an
  empty string, or a placeholder.

* A peer row's balance is the **bilateral position** and MUST render signed and
  labeled by direction: a positive position reads **"we owe them"** (the peer
  prepaid or earned here — verified live: a peer's earnings accrue on the same
  row, so "prepaid" would misread it), a negative one **"owes us"** — the raw
  sign convention is never shown as a bare negative number without its reading.
  The old "their credit here / calls rejected until the peer holds credit"
  framing is retired: calling is permissionless and admission is governed by
  the kernel's global exposure cap, which the UI reports, never enforces.
* A peer flagged `settlement_due` MUST carry a quiet status pill (information,
  never authority — crossing the trigger stops nothing). *Conditional:* where
  the kernel serves its global exposure figures (cap and current gross
  receivables), the kernel card states them as evidence.
* **There is no subscription.** A peer row comes into being by use — a first
  inbound call, or an outbound call that resolves and caches a proxy — or by
  `deposit` addressed to a peer's public key, which provisions and funds the
  account in one gesture (peering is handshake-free). The page therefore offers
  **no subscribe or unsubscribe affordance**: the durable lever is suspend, and a
  peer no longer called lapses through the kernel's retention. The deposit form
  MUST accept a public key wherever it accepts a handle (the three account forms
  are disjoint, §5).
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
* **Fund and Name are the two gestures a discovered card carries.** Both open a
  real `?form=` link with the key prefilled — never a `data-modal` trigger,
  which would open the empty in-page dialog. **Fund** promotes the kernel
  (deposit by key provisions and funds in one gesture); its authorizing control
  stays the form's money button. **Name** binds a petname: the kernel binds one
  automatically only on this kernel's own first outbound use, so an explicit
  bind is the only way to name a kernel merely discovered, and without it the
  operator is left typing a key. Neither is account-dependent — verified live,
  `suspend` on an account-less key is refused, and those verbs stay off the
  card. **Inspect a kernel by key** remains the way in for a key learned
  elsewhere (verified live: an unknown key still returns a usable page).
* **One rename form serves both namespaces.** A user target rebinds its handle;
  a kernel target binds its petname, exactly, refusing an occupied name. The
  form is reached from the displayed name on a counterparty card and from
  **Name** on a discovered one, and both open the same modal on the same
  handler — the card's own verbs stay inspect, settle, and suspend. It MUST key
  on the public key for a kernel: an unbound kernel has no name to key on, and
  advertised names collide.
* **Evidence, not hearsay.** Inspect MUST render the kernel's retained evidence
  in **two views, never folded**: an **execution summary** from the rows the
  subject issued about itself, and **counterparty experience** from the rest,
  grouped by issuer and carrying the **corroborated** count. Each row names its
  subject by full action id (§10) and states uses, successes, failures, and the
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
* `peer_unfunded` on a run means **the serving kernel's global exposure cap is
  exhausted — an operator settlement matter** — and MUST NOT be rendered as the
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

* One modal on the target's own detail page
  (`POST /control/users/{target}/rename`, body field `new_name` for both
  namespaces), reached from `admin`, from a counterparty's name, and from
  **Name** on a discovered kernel. The detail page MUST render whichever of the
  two disjoint records the kernel returns — a user carries a handle and
  balances, a kernel carries the petname model and its sync cache, and holds
  balances only once it has an account — never one shape padded with the
  other's empty fields.
* Its disclosure MUST state the consequence beside the field, and the two
  consequences differ: renaming a **user** frees the old handle and carries
  every reference to the new one; naming a **kernel** binds a petname here,
  exactly, and an occupied name is refused. Success MUST land on the page that
  then exists — the new handle for a user, the unchanged key for a kernel — and
  a rejection MUST stay put. The new name is sent as typed (§4): the kernel
  refuses `sys` and any sigiled name, and the UI shows that reason.

**Settlement.** Confirmed on the running kernel: `POST /control/peers/settle`
`{handle}`, and `{handle, settlement_id}` for the record-cash leg (§10).

* Settling moves real external money, so both verbs are modal confirms naming
  the peer and the amounts, on the peer row: **Settle** (initiates the kernel's
  settlement of the bilateral position — exact when the debt covers the
  quantum, else the kernel's probabilistic residual protocol) and **Record
  cash** (records that the external rail payment happened, keyed by the
  settlement id the settle outcome names). Neither is ever reachable by GET.
  The UI presents the kernel's settlement outcome verbatim and never computes
  or predicts one.

**No value-transfer queue.** The kernel served one while value could cross a
kernel boundary and a buyer-side transfer could sit unresolved. Value is now
local to a kernel (kernel §D18): what is locked from the caller is exactly what
the beneficiary receives, in one settlement, so no such record can exist and
`/control/transfers` is gone (404, verified live). The UI carries no queue, no
retry affordance, and no successor page — there is no state left to represent.

## 7. Visual language and non-functional requirements

* **The accent is exclusive.** The accent MUST appear only on the control that authorizes a
  settling `Call` paying a provider — the terminal, priced, parties-visible
  confirm. A control that opens a confirm or otherwise moves no money MUST NOT use
  the accent.
* **Button vocabulary (closed set).** Spend: the accent, solid, names the amount
  as `verb ($N.NNN)` (e.g. "Run ($0.012)") — the `$`-prefixed
  dollar amount is the same one the money helper renders everywhere. Primary
  non-spend: solid ink. Secondary: outline/ghost. Destructive: maroon, distinct from the accent —
  Delete, Disable, Decline, and force-end; force-end MUST name the refund, not a
  price. No other button styles exist. (A step completion is two-branch per
  §6.3: an ordinary completion is the neutral `Complete` naming no amount; a
  **payment step** completion is a genuine spend and uses the money button.)
* **Money and identity typography.** Money, IDs, hashes, and `owner/name` MUST
  render in **monospace with tabular figures** (the provenance face), regardless
  of the surrounding proportional chrome. Money MUST render in **dollars**,
  `$`-prefixed, **always three decimals**, thousands-grouped (`$1,234.567`), with
  the sign before the `$` (`-$2.500`). The wire unit is the integer **credit**
  (`1000 credits = $1`, so 1 credit = `$0.001`, exact); dollars are a
  presentation-only transform produced by **one** helper (`money`), so a future
  change to the money format is a single edit. No diamond/gem/`◆` mark and never
  the `¤` character. Ledger rows MUST carry explicit `−`/`+`; spend buttons state
  the unsigned gross as `verb ($N.NNN)` (above). Editable amount inputs are typed
  in dollars and parsed server-side back to credits. The owner segment of
  `owner/name` MUST be weight-emphasized in a confusable-distinct monospace face
  and MUST NOT use the accent; a remote owner segment (`owner@kernel`) renders
  the same way, unlinked (§6.2).
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
  stacking, no counterparty text, auto-dismissing. The step badge is the sole
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
   a button; result JSON in a non-interpreted code block. The chrome vocabulary
   defined in §7 MUST NOT be applied to counterparty content. Trust affordances
   MUST key on kernel-captured fields (transaction parties, process owner, a
   step's `required_caller` and `next_action`), never on payload shape.
2. Route every act that moves the user's own money — run (including a run of a
   value-bearing action, which is how money is sent, §6.5), payment-step
   completion (§6.3), force-end — through a confirm whose
   button names the amount or refund. An ordinary step completion moves none of
   the completer's money and stays neutral (§6.3, the single statement). No
   spend MUST be reachable from a list view or by a GET.
3. Parse `owner/name` by the first-slash rule everywhere (§4, including the
   remote `owner@kernel/name` form); render the owner segment distinguished, in
   a confusable-distinct monospace face, with the owner profile one click away
   for a local owner and no link for a remote one; show a first-time-payee hint
   on the run form.
4. Show, on a payment-step card before the confirm, whose funds move, how much,
   and the sealed `partial_args`; an ordinary step card shows the sealed
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
* JSON-Schema-to-form rendering, including `partial_args` lock-out on step cards
  and the `allowed_input`-sourced completion form (target action unreadable →
  form still renders);
* the two completion branches: ordinary steps neutral (no amount anywhere);
  payment steps render the money button naming the amount and the beneficiary
  (§6.3), decided solely by the target action's signed `effect` field;
* the run primitive: cost preview, the priced confirm, process auto-closure, and
  force-end;
* no action is special-cased: no bespoke form, route, command, or price helper
  keyed to a single ref anywhere in the UI (§6.5);
* bare-handle auth (login, register, recover) with sigiled input tolerated;
* signed bilateral-position rendering on the peers page (negative positions read
  "owes us", never a bare negative);
* no value-transfer queue: no route, handler, client method, or link to
  `/control/transfers` anywhere (§6.6);
* the call-outcome classes, rendered distinctly — including that a parked
  (`timeout`) outcome names the reserved amount and claims no refund, that a
  `meta.retry="refresh"` refusal offers the re-pinned retry while a bare
  `execution_failed` offers none, and that a peer's refusal names the peer;
* discovery: lookup path and catalog fallback on `ErrInvalidState`;
* the workshop offers no synthesis affordance, `sys/make` being absent from the
  kernel;
* access control of owner-only UI affordances (Studio surfaces shown only to the
  owner);
* security invariants: counterparty content renders inert; trust affordances key
  on kernel fields, not payload shape; no spend reachable by GET;
* the single `aria-live` feedback region and the absence of any notification feed.

## 10. Open items / kernel prerequisites

Features marked *Conditional* elsewhere are indexed here; each is mandatory only
once its prerequisite is confirmed on the running kernel.

* **Duplicate signup — resolved.** `POST /v1/users` with a taken handle returns
  `422 {"code":"invalid_input","error":"create account: already exists"}`
  (verified on v0.12.16), and the kernel enforces the ≥8-character minimum
  itself. The BFF keys on the code, not the message.
* **Action synthesis — dropped.** `sys/make` is not a kernel action; the
  workshop offers manual creation and OpenAPI import only (§6.4). The kernel's
  own path is `sys/tinygo/compile` → `POST /v1/actions {kind:"wasm",
  wasm_artifact}`; designing a UI for it is future work, not a gap in this
  version.
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
* **Settlement over HTTP (§6.6).** Confirmed on the running kernel:
  `POST /control/peers/settle` `{handle}` initiates settlement and
  `{handle, settlement_id}` records the rail payment (the CLI `--cash` leg).
  The settle confirms are therefore mandatory (§6.6).
* **Inspect action identity (§6.6).** `admin inspect` serves each action as
  `{name, description, price}` only — no `action_id`, `owner_handle`, or
  `stats`, and the name is bare. Until the kernel restores them, remote action
  names render unlinked with no reputation columns, and an evidence subject is
  named by its full action id (there is nothing to join it against). Evidence
  `notes` is served but `null` on every observed row; its shape is unverified,
  so it is not rendered.
* **Global exposure figures (§6.6).** Confirmed: `/control/identity` serves
  `exposure_max`, `gross_receivables`, `settlement_due`, `settlement_trigger`,
  and `settlement_quantum`; the kernel card states them as evidence.
* **A moved amount — resolved (§6.5).** v0.12.24 journals it: settling a
  `transfer`-effect call writes a ledger entry from the immediate caller to the
  beneficiary, `reason` = the settling transaction's id, and **both** parties
  read it (verified: the beneficiary gets 404 on the transaction itself, so the
  journal is their only record). The transaction still carries
  `gross`/`fee`/`net`/`refund` — zero at price 0 — and no `value`/`value_to`;
  the breakdown rows stay zero-suppressed and would render those if a kernel
  ever serves them. Remote-proxy transactions may carry them in the remote
  receipt JSON.
* **Parked-run metadata (§6.2).** Confirmed on v0.12.23: a dispatched call with
  no signed outcome answers `504 timeout` with `meta.process_id`,
  `meta.pending_since`, and `meta.refund_eligible_at` (24 h out), and the funds
  move from `available` into `locked` — measured, not inferred.
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
* **`allowed_input` on `/v1/steps` (§6.3).** Confirmed on the running kernel
  (waiting steps carry it). The completion form falls back to the target action's
  `input_schema` where `allowed_input` is absent (older kernels). The federation
  `payment` descriptor does NOT appear on `/v1` step listings (confirmed);
  payment steps are detected by the target action's `effect` field, re-confirmed
  served on v0.12.23 action reads and readable by the completer (§6.3).
  An approval-only step serves an object with no fields, in either of two shapes
  observed live — `{"type":"object","properties":{},"required":null}` and
  `{"type":"object"}` — and both MUST collapse to the single confirm of §6.3,
  never to a raw-JSON box, which would ask a human to author `{}`. The two
  shapes reach that outcome by different routes, and the distinction is load
  bearing: an **explicitly empty** `properties` declares that the input has no
  fields, so nothing can be collected; **no `properties` key at all** is an
  unconstrained object, where the raw editor is correct and the collapse instead
  follows from every key being sealed in `partial_args`.

**Error mapping (informative).** `ErrUnauthenticated` (incl. suspension) → clean
logout with explanation; `ErrUnauthorized` → not-permitted message; `ErrNotFound`;
`ErrInvalidInput`/`ErrSchemaViolation` → field-level form errors; `ErrInvalidState`
(also the kernel's refusal of a lifecycle verb on a remote proxy, §6.6 — which the
UI does not offer, so this is a defence, not a flow);
`ErrInsufficientFunds` → clear message noting top-ups are out-of-band;
`peer_unreachable` → the peer was offline, the call refunded, retry later (the
kernel sends no `meta` here, so the UI names the peer from the ref it submitted
or names none);
`peer_unfunded` → the serving kernel's exposure cap is exhausted — an operator
settlement matter, never rendered as the caller's own insufficient funds (§6.6);
`ErrUnauthorized` **with `meta.peer`** → the peer refused this call (§6.2), which
must never read as the user's own lack of permission — plain `ErrUnauthorized`
keeps the not-permitted message;
`ErrExecutionFailed` → "the action failed and your funds were fully refunded,"
with the failure transaction linked when the kernel records one — **except** with
`meta.retry = "refresh"`, which is the provider's contract change: nothing ran,
nothing was charged, and the buyer is re-quoted (§6.2);
`ErrTimeout` → **not** a refund: the call is parked with its funds reserved, and
the message names the reserved amount, the process, and `refund_eligible_at`
(§6.2);
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