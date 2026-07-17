# Juice Web UI Requirements

Version: 0.3.0
Status: design draft — endpoint surface gated against the running kernel

> **What changed in 0.3.0.** The visual language turns from a *terminal* into a
> **minimal marketplace storefront**. Two axes change; everything else that moves
> money is preserved verbatim.
>
> 1. **Typography becomes provenance.** The UI face is no longer monospace
>    everywhere. The default UI face is now the **proportional system stack** (the
>    storefront register); **monospace is *reserved*** for kernel-vouched facts —
>    money, `@owner/name`, ids, hashes, and sealed `partial_args`. The rendered
>    typeface thus marks the trust boundary: proportional chrome, monospace truth.
>    Corners soften (a card radius plus a tighter control radius, both tokens).
> 2. **Navigation reads as a marketplace, not a command dump.** The primary
>    groups are relabelled **browse** (discovery + run), **inbox** (steps),
>    **workshop** (the provider surface: earnings + own actions + creation),
>    **wallet** (balances, open processes, ledger), and **you** (identity hub),
>    with **network** (federation) and **admin** kept `@sys`-only. This is a
>    relabelling of the same CLI-mirrored concepts, not new authority.
>
> A **media slot** is added to the action card and page (§6.2): a 16:9 image with
> a deterministic generated fallback until the kernel serves images (Conditional,
> §10, tracked in `ROADMAP.md`).
>
> The old terminal identity is **not deleted** — it survives losslessly as a
> selectable `terminal` theme (monospace UI face, square corners), because the
> whole surface already renders through semantic tokens. The set of shipped
> palettes is curated down, with new default **paper** (light) and **ink** (dark).
>
> Invariants that predate 0.3.0 are unchanged: the BFF architecture, session/token
> rules, **the accent's spend-only discipline and the closed button vocabulary**,
> inert counterparty rendering, the single aria-live region, **monospace tabular
> money**, floating modal forms with the `?form=` fallback, keyboard navigation,
> admin/supervision gating, the CSP, and the test discipline.
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
**inbox**, **workshop**, **wallet**, and **you**, plus **network** (federation)
and **admin**, both shown **only to the `@sys` superuser**. Each maps to the same
CLI concepts as before: `browse` is discovery + execution (`run` + the catalog);
`inbox` is `step` — work addressed to the user, carrying the sole attention badge;
`workshop` is the provider surface (`action` management + earnings + creation);
`wallet` is money evidence (`user` balances + `process` lifetimes + the `tx`
ledger + receipts); `you` is identity + preferences; `network` is federation
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
  via `@sys/lookup`; catalog browse (`GET /v1/actions`); action detail; call
  execution (`POST /v1/run`), including run-by-ref from the palette.
* **browse / workshop** — catalog browse plus owner action management: create,
  list/show (including inactive, via `GET /v1/actions?owner=@self`), update,
  enable, disable, delete; three-way `visibility` (private/local/public); AI
  synthesis via `@sys/make`; OpenAPI import/unimport; stats; ratings list;
  earnings folded from the owner's transactions.
* **process** — computation lifetimes (`GET /v1/processes`, show, force-end),
  surfacing parked funds and awaiting-receipt state.
* **tx** — the immutable ledger: transactions list/show/rate; receipt
  verification (`GET /v1/transactions/{id}/receipt-verification`), on demand.
* **step** — steps: list/show/complete; step creation as a developer affordance.
* **user** — self profile, balances (`GET /v1/me`, `available`/`locked`), and
  update (`PUT /v1/me`); the ledger of the user's own transactions.

*Conditional:* the trace tree on the receipt page.

* **Admin / supervision (superuser only):** the `admin` group, visible only to
  `@sys`, covers user list/show, suspend/unsuspend, credit deposits and
  withdrawals, and federation trust (friend/unfriend, peers, this kernel's
  identity). The kernel serves these over HTTP on `/control/*`, gated by an
  `IsSuperuser` check on the `@sys` bearer (API.md C13); the UI is an ordinary
  client of that surface. Superuser **scope** on the normal list pages (a `@sys`
  session sees all actions/processes/tx/steps) is indicated quietly on those
  pages, not duplicated as separate admin listings.

### 3.2 Out of scope (no user-facing flow)

* **Trace inspection:** no trace-read HTTP route is confirmed, so the Engine-room
  trace tree stays conditional (§10).

### 3.3 Known limitations

* Because deposits are admin-only, a UI user cannot top up their own balance.
  Balance views MUST state plainly that credit top-ups happen out-of-band
  (operator/CLI). The UI still shows balances and funds processes from existing
  credits.

## 4. API contract the UI must honor

* Snake_case JSON on the wire; never expose Go field names; never echo secret
  fields.
* Structured fields (`args`, `result`) are inline JSON objects, not
  double-encoded strings.
* Action identity is `@owner/name`, parsed by a **single split on the first `/`
  after `@`** — owner before, name (the remainder) after. Never split on every
  `/`: names may contain `/` (e.g. `@sys/llm/chat`), and the rule is unambiguous
  because handles cannot contain `/`. In a URL, `@owner/name` is percent-encoded.
* Action read/list responses carry both `id` (UUID, for management) and a
  computed `action` (`@owner/name`, for calling). The UI uses `action` directly
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

## 6. The interface

### 6.1 Information architecture

* The UI MUST present a left sidebar whose entries are the primary groups
  (`browse`, `inbox`, `workshop`, `wallet`, `you`, then `network` and `admin` for
  `@sys`), default-expanded with labels, collapsible, with collapse state
  persisted server-side. The active group MUST carry `aria-current`. The `inbox`
  entry MUST carry its waiting-count step badge in every state. A single nav model,
  built once server-side, MUST drive the sidebar and the mobile tab bar (and the
  command palette), so the three cannot drift.
* The account/identity summary (handle, balance) and the theme picker live at the
  sidebar foot / on the `you` hub; nothing that is itself a primary destination
  sits in a popover.
* Mobile MUST reflow to a fixed bottom **tab bar of four** — `browse`, `inbox`,
  `chat`, `you` — with the step and chat badges intact; the remaining
  destinations (`wallet`, `workshop`, `network`, `admin`) and preferences (theme,
  sign-out) are reached inside the `you` hub. (The core rule that money moves only
  through a named confirm is unaffected by which tab hosts a screen.)
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
  appear only for `@sys`), `g`+letter section navigation, `/` to focus search,
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
  `@sys/lookup` (required `query`, optional `limit`) via `POST /v1/run`. Results
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
* Catalog browse (`GET /v1/actions`, owner/name filters) MUST be available as the
  secondary path and the per-owner listing.
* On `ErrInvalidState` from `@sys/lookup`, the omnibox MUST fall back to catalog
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
* The owner segment of `@owner/name` MUST be visually emphasized and link to an
  owner profile (public actions, aggregate stats, account age).

**Run form.**

* The action page has ONE anatomy: hero → description → an **inline run form**
  (on the page, never a modal) → the result as a card **attached directly below
  the form** → ratings. There is no floating/opener control and no inert
  Method/URL block (an HTTP action's source is editable by its owner inside the
  Edit modal only).
* Inputs MUST be generated from `input_schema` (typed fields, required markers,
  descriptions as help text, client-side hints only; the kernel validates). A
  raw-JSON input MUST be available.
* A **paid** run's confirm is the single money button reading `Run (N)` (the
  accent conveys credits — no glyph on the button), and the form MUST disclose
  "Maximum, all-in. Refunded in full if it fails." A **first payment** to this
  owner MUST show a quiet first-payee hint (§8.3). A **free** run is the same
  form with a neutral `Run` button.
* The receipt MUST offer one-click re-run with the same args.

```text
  @lena/ocr-translate                    ◆ 12
  ─────────────────────────────────────────────
  document_url   [https://…            ]
  target_lang    [German             ▾ ]

  [ Run (12) ]   Maximum, all-in. Refunded if it fails.
  ┌─ result ──────────────────────────────────┐
  │  Success.   { … }        Receipt · ▲ ▼      │
  └────────────────────────────────────────────┘
```

**Outcomes.** The three outcome classes MUST render distinctly:

* input-schema rejection → "Not sent. You were not charged"; field-level errors
  inline on the sheet; no receipt.
* failure → "Failed — fully refunded"; the refund reflected in the balance; the
  kernel's `reason` shown; a link to the failure receipt.
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
* `partial_args` MUST render read-only; only schema fields absent from
  `partial_args` MUST be editable. When none are editable, the card MUST collapse
  to a single confirm.
* The step card MUST stay **calm and free of funds-mechanics narration** — no
  "parked," no "you pay N but it's reserved," no "the funds don't come from your
  wallet." The completer is doing requested work, not authorizing a spend from
  their own wallet (the money was reserved by whoever created the step), so the
  completion control is a **neutral `Complete`** that names **no** amount — a
  `verb (N)` money button here actively misleads ("you pay N"). The card names
  only *who asks* and *what* (the creating action, quietly) and shows the sealed
  `partial_args` as an inert read-only detail. This reinterprets the older
  "attribute the parked price"/§8.4 disclosure: for a step the anti-phishing
  signal that matters is *who requested it* and *what inputs are sealed*, not a
  funds-movement paragraph.
* Completion MUST POST to `/v1/steps/{id}/complete` with `args` (default `{}`)
  and MUST render `{result, tx_id, trace_id, step_id}` through the run outcome
  display (§6.2). Completion MUST be offered only to the `required_caller`;
  duplicate and non-caller attempts MUST map to typed-error messages.
* Completed and cancelled steps MUST appear in history; cancelled cards MUST
  state the reason.
* `@sys/message` steps MUST render as a message (sender, text, acknowledge
  button).
* Step creation (`POST /v1/steps`: `action`, `process_id`, `parent_trace_id`,
  `partial_args`, `input_schema`, `required_caller`) MUST be present but collapsed
  by default.

### 6.4 action — catalog and provider workspace

* Workshop MUST present, in order: earnings, action list, creation zone.
* Earnings MUST be computed in the BFF by folding the user's own transactions;
  each figure MUST link to its underlying transactions. The BFF MUST NOT
  reproduce kernel settlement.
* The action list MUST use `GET /v1/actions?owner=@self`, include inactive
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
* The creation zone MUST offer two paths, each with a the accent spend button stating
  the price:
  * `@sys/make`: `POST /v1/run {action: "@sys/make", args: {description}}`,
    button `[ Synthesize (20) ]` (price from `native.make.price`). The call is
    synchronous: the UI MUST show progress against elapsed time and render
    `{status, action_id, action_name, diagnostics, tests}`. On `status="success"`
    the action is already registered and active, owned by the caller; the UI MUST
    link to its Studio card. On `status="failure"` the UI MUST render
    `diagnostics[]` as feedback, not a kernel error. Fewer than 20 available
    credits MUST block submission.
  * Manual: `POST /v1/actions` (inactive by default), with the schema-driven
    editors used elsewhere, schema linting as hints only, and WASM build errors on
    enable. Upstream auth (`auth`) MUST follow the same write-only treatment as the
    import wizard: secret fields are masked inputs, never pre-populated.
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
  paginate (§7). The wallet's own activity table is a preview (most recent 10) that
  links to the paginated ledger.
* The ledger MUST list every credit and debit, newest first, each row linking to
  its receipt. Closed processes (including `run`-created ephemerals) MUST appear
  here.
* The transaction list (`GET /v1/transactions`) MUST return rows where the user
  is buyer (`owner_user_id`), caller (`caller_user_id`), or seller (the called
  action's owner). Each row MUST show the user's role, the action
  (`@owner/name`, linked via `GET /v1/actions/{action_id}`; a truncated ID if the
  action was since deleted), status, and cost breakdown.
* The receipt page MUST show parties (buyer/seller, viewer's role highlighted),
  action ref, inline args and result, gross/fee/net (fee captioned "on value
  added"), failure `reason` when present, and the buyer's rating block.
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

## 7. Visual language and non-functional requirements

* **The accent is exclusive.** The accent MUST appear only on the control that authorizes a
  settling `Call` paying a provider — the terminal, priced, parties-visible
  confirm. A control that opens a confirm or otherwise moves no money MUST NOT use
  the accent.
* **Button vocabulary (closed set).** Spend: the accent, solid, names the amount
  as `verb (N)` (e.g. "Run (12)", "Synthesize (20)") — the accent colour conveys
  credits, so a spend button carries **no credit glyph**. Primary non-spend: solid
  ink. Secondary: outline/ghost. Destructive: maroon, distinct from the accent —
  Delete, Disable, Decline, and force-end; force-end MUST name the refund, not a
  price. No other button styles exist. (A **step completion** is NOT a spend
  button — it is the neutral `Complete` naming no amount; the completer does the
  work but the money was reserved by the step's creator.)
* **Money and identity typography.** Money, IDs, hashes, and `@owner/name` MUST
  render in **monospace with tabular figures** (the provenance face), regardless
  of the surrounding proportional chrome; a money amount outside a button carries
  the **credit mark** — the diamond/gem icon (rendered via the `gem` helper /
  `#gem` sprite), e.g. `12 ◆` — never the `¤` character. Ledger rows MUST carry
  explicit `−`/`+`; spend buttons state the unsigned gross as `verb (N)` (no glyph,
  above). The owner segment of `@owner/name` MUST be weight-emphasized in a
  confusable-distinct monospace face and MUST NOT use the accent.
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
  provenance set — money, IDs, hashes, `@owner/name`, and sealed `partial_args` —
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
2. Route every credit-moving act — run, step completion, force-end — through a
   confirm whose button names the amount or refund. No spend MUST be reachable
   from a list view or by a GET.
3. Parse `@owner/name` by the first-slash rule everywhere; render the owner
   segment distinguished, in a confusable-distinct monospace face, with the owner
   profile one click away; show a first-time-payee hint on the run form.
4. Show, on a step card before the confirm, whose funds move, how much, and the
   sealed `partial_args`.
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
* JSON-Schema-to-form rendering, including `partial_args` lock-out on step cards;
* the run primitive: cost preview, the priced confirm, process auto-closure, and
  force-end;
* the three call-outcome classes, rendered distinctly;
* discovery: lookup path and catalog fallback on `ErrInvalidState`;
* synchronous `@sys/make`: success links to an active owned action; failure
  renders `diagnostics`; sub-20-credit block;
* access control of owner-only UI affordances (Studio surfaces shown only to the
  owner);
* security invariants: counterparty content renders inert; trust affordances key
  on kernel fields, not payload shape; no spend reachable by GET;
* the single `aria-live` feedback region and the absence of any notification feed.

## 10. Open items / kernel prerequisites

Features marked *Conditional* elsewhere are indexed here; each is mandatory only
once its prerequisite is confirmed on the running kernel.

* **Duplicate-user signup leaks a raw 500.** `POST /v1/users` with an existing
  handle/email returns `500 {"code":"internal","error":"…UNIQUE constraint
  failed…"}` instead of a clean `409`/`422`. The BFF detects this by message and
  shows a friendly "already registered" line; if the kernel later returns a typed
  conflict, simplify the detection. The kernel also does not enforce a minimum
  password length, so the BFF requires ≥8 characters on new passwords.
* **`@sys/register-action` — dropped.** Not in the final kernel spec. The
  registration-approval Inbox card and the asynchronous "draft reviewed before
  going live" synthesis flow are removed; `@sys/make` is synchronous and
  self-activating.
* **Trace tree (Wallet receipt page).** No trace-read HTTP route is confirmed.
  Until one is, the Engine-room trace tree stays conditional; receipt
  verification is confirmed and mandatory.
* **OpenAPI ownership proof (Studio).** Confirm whether the kernel exposes an
  HTTP protocol for challenge creation, credential submission, and proof status.
  Until then the UI surfaces the kernel's rejection on public activation.
* **Lookup operational status (Run).** Confirm whether `@sys/lookup` has a
  configured embedder or returns `ErrInvalidState`. Does not gate discovery
  (mandatory); selects semantic ranking versus catalog fallback.
* **Native additions (`@sys/random`, `@sys/llm/embed`).** Not in the final kernel
  spec; conditional, not assumed. No current UI feature depends on them.

**Error mapping (informative).** `ErrUnauthenticated` (incl. suspension) → clean
logout with explanation; `ErrUnauthorized` → not-permitted message; `ErrNotFound`;
`ErrInvalidInput`/`ErrSchemaViolation` → field-level form errors; `ErrInvalidState`;
`ErrInsufficientFunds` → clear message noting top-ups are out-of-band;
`ErrExecutionFailed`/`ErrTimeout` → "the action failed and your funds were fully
refunded," with the failure transaction linked when the kernel records one;
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