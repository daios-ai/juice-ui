# Juice Web UI Requirements

Version: 0.4
Status: design draft — endpoint surface gated against the running kernel
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

**Navigation model.** The UI MUST present exactly four destinations — Run,
Inbox, Studio, Wallet. Processes, traces, transactions, and receipts MUST NOT
appear as navigation destinations; they are reachable only as evidence within
those four rooms, and traces only behind the Engine-room control on the receipt
page. The kernel creates and closes processes automatically, and the UI MUST NOT
expose that bookkeeping.

**Reference discipline.** Each cross-cutting rule is defined once and applies
wherever relevant without per-use citation: the API contract in §4, session and
token rules in §5, the visual language (clay, button vocabulary, status signals,
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
  templates/      HTML templates (not Go; no test file required)
  static/         CSS and client scripts (not Go; no test file required)
  ```

* Always test a new or updated feature against a live kernel (start a throwaway
  one if needed; clean up after).
* Configuration loads from environment variables with safe local defaults. At
  minimum: kernel API base URL (default `http://127.0.0.1:4040`), the UI listen
  address, and a session secret. Commit no secrets. Reject invalid startup
  configuration clearly.
* Technology stack (standard-library-first; dependencies only where hand-rolling
  would be a correctness or security risk):
  * **Routing:** stdlib `net/http` ServeMux (Go 1.22+ method/pattern routing,
    `{id}` wildcards). No router dependency.
  * **Templating:** stdlib `html/template` (auto-escaping); partials double as
    htmx swap fragments.
  * **Interactivity:** **htmx** plus small vanilla ES modules — no build step, no
    Node, no SPA framework. Handlers return HTML fragments; htmx performs swaps
    and the inbox-badge polling. Vanilla JS is limited to the schema→form
    renderer and the theme toggle.
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

### 3.1 In scope (HTTP-reachable), organized by room

* **Run** — server status (`GET /health`) and federation metadata
  (`GET /.well-known/juice-kernel.json`), read-only; natural-language discovery
  via `@sys/lookup`; catalog browse (`GET /v1/actions`); action detail; call
  execution (`POST /v1/run`).
* **Inbox** — steps: list/show/complete; step creation as a developer
  affordance.
* **Studio** — owner-private action management: create, list/show (including
  inactive, via `GET /v1/actions?owner=@self`), update, enable, disable, delete;
  public/private visibility toggle; AI synthesis via `@sys/make`; OpenAPI
  import/unimport; stats; ratings list.
* **Wallet** — self profile and balances (`GET /v1/me`, `available`/`locked`);
  transactions list/show/rate; receipt verification
  (`GET /v1/transactions/{id}/receipt-verification`).

*Conditional:* the trace tree on the receipt page.

### 3.2 Out of scope (no user-facing flow)

* **Admin / supervision:** user list/show, suspend/unsuspend, credit deposits and
  withdrawals, admin listings, admin action disable. CLI-only.
* **Federation management:** friending/unfriending, gossip, and the remote-import
  lifecycle. These are operator-level concerns with no user-facing flow, and stay
  out even where an HTTP route exists.

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

* The UI MUST present a left sidebar with the four destinations (Run, Inbox,
  Studio, Wallet), default-expanded with labels, collapsible, with collapse state
  persisted server-side. The Inbox entry MUST carry its count badge in every
  state.
* A top bar MUST be present on every page, holding the omnibox and the inbox bell
  plus account menu, and nothing that is itself a destination.
* Mobile MUST reflow the same layout: the sidebar becomes a fixed bottom tab bar
  (four destinations, Inbox badge intact); the top bar slims to
  hamburger · omnibox · bell, with secondary items (settings, theme, logout) in a
  drawer.
* The Inbox badge MUST be the only attention-demanding affordance in the UI. It
  MUST reflect waiting steps for the signed-in user, sourced from a single
  server-side poll of `GET /v1/steps?status=waiting` per session, fanned out to
  open tabs — not one polling loop per tab.

### 6.2 Run — discovery, action page, payment sheet

**Discovery.**

* The omnibox MUST be present on every page and MUST issue `@sys/lookup`
  (required `query`, optional `limit`) via `POST /v1/run`. Results MUST render as
  ranked cards showing name, owner, one-line description, price, and the
  reputation strip.
* Price MUST appear on each card. Sort and filter by price MUST be available.
* Catalog browse (`GET /v1/actions`, owner/name filters) MUST be available as the
  secondary path and the per-owner listing.
* On `ErrInvalidState` from `@sys/lookup`, the omnibox MUST fall back to catalog
  search and MUST show a notice that semantic search is unavailable; it MUST NOT
  present a dead control. Discovery is mandatory regardless of embedder status.

**Action page.**

* MUST show description, owner block, reputation strip, price in large type, and
  two schema panels rendered from `input_schema`/`output_schema` as field lists
  (name, type, required, description), with raw JSON behind a toggle.
* The reputation strip MUST show four metrics — runs, success rate, rating, mean
  latency — each with an icon, value, and a tooltip that is also the icon's
  `aria-label`. The rating MUST show mean and count.
* Ratings MUST render as reviews (0/1 value, `note`, newest first, paginated),
  labeled as ratings from paid calls.
* The owner segment of `@owner/name` MUST be visually emphasized and link to an
  owner profile (public actions, aggregate stats, account age).

**Payment sheet.**

* Run on the action page MUST open a modal payment sheet. The sheet's confirm
  MUST be the only money-moving control in the flow; the Run control that opens
  it MUST NOT move money and MUST NOT use clay.
* The confirm button MUST state the amount (e.g. "Pay 12 ¤ & Run").
* Inputs MUST be generated from `input_schema` (typed fields, required markers,
  descriptions as help text, client-side hints only; the kernel validates). A
  raw-JSON input MUST be available.
* The sheet MUST show price as "maximum, all-in," current balance, projected
  balance after the call, and "Refunded in full if it fails."
* The receipt MUST offer one-click re-run with the same args.

```text
┌─────────────────────────────────────────────┐
│  Run @lena/ocr-translate                     │
│                                              │
│  document_url   [https://…           ]       │
│  target_lang    [German            ▾ ]       │
│                                              │
│  Price            12 ¤   (maximum, all-in)   │
│  Your balance     87 ¤ → 75 ¤                │
│                                              │
│  Refunded in full if it fails.               │
│                                              │
│            [ Cancel ]   [ Pay 12 ¤ & Run ]   │
└─────────────────────────────────────────────┘
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

### 6.3 Inbox — steps

* A waiting step whose `required_caller` is the signed-in user MUST render as a
  completable card; all other steps MUST go to a history table. Filters by
  `process_id` and `status` MUST be available (`GET /v1/steps`); a single step is
  `GET /v1/steps/{id}`, including the computed `action`.
* `partial_args` MUST render read-only; only schema fields absent from
  `partial_args` MUST be editable. When none are editable, the card MUST collapse
  to a single confirm.
* The card MUST display and attribute the parked price before the confirm.
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

### 6.4 Studio — provider workspace

* Studio MUST present, in order: earnings, action list, creation zone.
* Earnings MUST be computed in the BFF by folding the user's own transactions;
  each figure MUST link to its underlying transactions. The BFF MUST NOT
  reproduce kernel settlement.
* The action list MUST use `GET /v1/actions?owner=@self`, include inactive
  actions, filter by state, and show kind, price, success rate, rating trend, a
  last-failure peek, and inline htmx toggles for active/inactive and
  public/private.
* Visibility MUST be set via `PUT /v1/actions/{id}` with `{public: true|false}`.
  No ACL, grant, or revoke operations exist; kernel access is public ∨ owner.
* Editing source, schema, kind, or price MUST warn that saving deactivates the
  action until re-enabled, matching the kernel's contract-drift rule.
* For `kind=wasm`, the UI MUST surface compile failure on enable as a typed build
  error.
* Source-URL input MAY be preflighted for obvious non-HTTP(S)/loopback cases; the
  UI MUST surface the kernel's URL rejection verbatim and MUST NOT reimplement the
  policy.
* The creation zone MUST offer two paths, each with a clay spend button stating
  the price:
  * `@sys/make`: `POST /v1/run {action: "@sys/make", args: {description}}`,
    button `[ Synthesize · 20 ¤ ]` (price from `native.make.price`). The call is
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

### 6.5 Wallet — money and receipts

* The Wallet MUST show three balances using the credited / refunded /
  escrow-pending signals: **Available**; **Parked** (locked: open processes and
  waiting steps, expandable to the owning items, with a force-end control that
  names its consequence — cancels waiting steps, refunds parked prices — via
  `POST /v1/processes/{id}/end`); **In flight** (open calls awaiting settlement).
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

* **Clay is exclusive.** Clay MUST appear only on the control that authorizes a
  settling `Call` paying a provider — the terminal, priced, parties-visible
  confirm. A control that opens a confirm or otherwise moves no money MUST NOT use
  clay.
* **Button vocabulary (closed set).** Spend: clay, solid, names the amount (e.g.
  "Pay 12 ¤ & Run", "Synthesize · 20 ¤", "Do it · 40 ¤"). Primary non-spend:
  solid ink. Secondary: outline/ghost. Destructive: maroon, distinct from clay —
  Delete, Disable, Decline, and force-end; force-end MUST name the refund, not a
  price. No other button styles exist.
* **Money and identity typography.** Money, IDs, hashes, and `@owner/name` MUST
  render in monospace with tabular figures; the unit is `¤`. Ledger rows MUST
  carry explicit `−`/`+`; spend buttons MUST state the unsigned gross. The owner
  segment of `@owner/name` MUST be weight-emphasized in a confusable-distinct
  monospace face and MUST NOT use clay.
* **Status signals.** The credited, refunded, and escrow-pending states MUST be
  mutually distinct and distinct from clay in both themes, each pairing a color
  with a glyph: credited green `+`, refunded slate-blue `↩`, escrow-pending
  (parked / in-flight) plum `◷`. These signals MUST be confined to text and
  glyphs, not fills.
* **Themes.** Light and dark themes MUST derive from one set of semantic design
  tokens (CSS custom properties); components MUST reference tokens only, never raw
  colors. Warm charcoal dark and warm ivory light; clay reserved for spend-verbs.
  Theme defaults to `prefers-color-scheme`; an explicit choice MUST be persisted
  server-side (a `theme` cookie) and stamped at render time, with no flash and a
  toggle that works without JavaScript. Both themes MUST meet WCAG AA (≥4.5:1
  body, ≥3:1 large text and UI affordances). `prefers-reduced-motion` MUST be
  respected.
* **Icons.** One set (Heroicons, outline/solid for inactive/active), recolorable
  to tokens, never emoji. Every icon-only control MUST carry an `aria-label`;
  hover tooltips are enhancement only.
* **Feedback.** There MUST be no notification feed, bell list, or toast stack;
  outcomes render in place. The UI MUST provide exactly one polite `aria-live`
  region for ephemeral acknowledgement of the user's own just-completed action
  (e.g. "Action disabled," "Copied," the 429 "slow down"): no history, no
  stacking, no counterparty text, auto-dismissing. The Inbox badge is the sole
  async attention signal.
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
third-party origins) with `frame-ancestors 'none'` and
`Referrer-Policy: no-referrer`; render only through auto-escaping templates; and
ship no client framework.

**Marketplace-threat layer.** Counterparty-authored content (action names,
descriptions, schemas, lookup results, args, results, `partial_args`, failure
`reason`) is untrusted input, wherever it is shown. The UI MUST:

1. Render every counterparty-authored string escaped and quarantined from chrome:
   no markdown, no auto-links, no bold, no clay, never adjacent to or styled like
   a button; result JSON in a non-interpreted code block. The chrome vocabulary
   defined in §7 MUST NOT be applied to counterparty content. Trust affordances
   MUST key on kernel-captured fields (transaction parties, process owner, a
   step's `required_caller` and `next_action`), never on payload shape.
2. Route every credit-moving act — run, step completion, force-end — through a
   confirm whose button names the amount or refund. No spend MUST be reachable
   from a list view or by a GET.
3. Parse `@owner/name` by the first-slash rule everywhere; render the owner
   segment distinguished, in a confusable-distinct monospace face, with the owner
   profile one click away; show a first-time-payee hint on the payment sheet.
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
buying something online, which is why the payment sheet and receipt page are the
two load-bearing artifacts, and why the clay spend-verb captures consent
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