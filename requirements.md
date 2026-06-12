# Juice Web UI Requirements

Version: 0.3.1
Status: design draft — endpoint surface pending verification against the running kernel
Codename: `juice-ui`

> **Gating condition.** A UI feature is mandatory only if it maps to an HTTP
> endpoint **confirmed on the running kernel**. Anything unconfirmed is named as
> an explicit kernel/API prerequisite in §10 and treated as conditional until
> verified. Where the running kernel supports something `docs/API.md` omits, the
> fact is recorded here and `docs/API.md` is updated. This follows from the
> BFF-only architecture, kernel authority, and the prohibition on reproducing
> kernel business logic (§1, §2): the BFF may compose HTTP calls, but must not
> infer, emulate, or bypass kernel state transitions.

## 1. Purpose and architecture

A modern web user interface for the Juice kernel, implemented in Go. It exposes
the Juice user flows (see `docs/flows.sh`) and talks to the kernel **only over
the documented HTTP API** (see `docs/API.md`); it never shells out to the CLI,
reads the kernel database, or imports kernel packages.

The UI is a **backend-for-frontend (BFF)**: a Go HTTP server that renders the
interface and proxies every request to the kernel API, holding the user session
server-side.

* The browser communicates only with the BFF. The BFF is the sole client of the
  kernel HTTP API.
* Kernel access tokens and refresh tokens are stored server-side in the session.
  They are never sent to the browser, embedded in HTML, or written to logs.
* The interface is server-rendered HTML with small client-side scripts for
  schema-driven forms and event polling. A heavy single-page framework is not
  required and SHOULD be avoided.
* The kernel is treated as authoritative. The BFF holds no durable domain state
  of its own beyond the session (tokens, PKCE state, CSRF token).

## 2. Implementation constraints

* Use Go. `go build ./...` and `go test ./...` MUST pass without external network
  access.
* Keep the package and source-file counts small. Do not split files for size
  alone. Every production `.go` file MUST have a corresponding `_test.go` file
  with independent tests for its logic.
* Suggested package layout (function-named, may change for cohesion or cycle
  reasons):

  ```text
  cmd/juice-ui/   entrypoint: configuration, dependency wiring, serve
  client/         typed kernel HTTP API client and error mapping
  session/        server-side sessions, token lifecycle, PKCE state, CSRF
  server/         router, middleware, request handlers, rendering
  templates/      HTML templates (not Go; no test file required)
  static/         CSS and client scripts (not Go; no test file required)
  ```
* Always test a new/updated feature on a live kernel (start a new one if needed, 
  just clean after).
* Configuration is loaded from environment variables with safe local defaults.
  At minimum: kernel API base URL (default `http://127.0.0.1:4040`, the kernel's
  listen port), the UI listen address, and a session secret. Commit no secrets.
  Reject invalid startup configuration clearly.
* Do not reproduce kernel business logic (fees, settlement, schema validation) in
  the UI. The UI presents kernel results; the kernel decides.
* Technology stack (standard-library-first; dependencies only where hand-rolling
  would be a correctness or security risk):
  * **Routing:** stdlib `net/http` ServeMux (Go 1.22+ method/pattern routing,
    `{id}` wildcards) — no router dependency.
  * **Templating:** stdlib `html/template` (auto-escaping), with partials that
    double as htmx swap fragments.
  * **Interactivity:** **htmx** plus small vanilla ES modules — no build step,
    no Node, no SPA framework. Handlers return HTML fragments; htmx performs
    swaps and the event-inbox polling. Vanilla JS is limited to the
    schema→form renderer and the theme toggle.
  * **Styling:** hand-written CSS driven by the §8 design tokens; no CSS
    framework, no build pipeline.
  * **Sessions:** `alexedwards/scs` (server-side token storage per §1).
  * **CSRF:** `gorilla/csrf` — security-sensitive primitives are not hand-rolled.
  * **PKCE / crypto:** stdlib `crypto/rand`, `crypto/sha256`, `encoding/base64`.
  * **Logging:** stdlib `log/slog` (structured; see §8).
  * **Configuration:** stdlib `os` with a small typed loader.
  * **Tests:** stdlib `testing` and `net/http/httptest`; the kernel is faked as
    an `httptest.Server` (see §9 — no network, no live kernel).

  The total dependency footprint is small (htmx on the front end; `scs` and
  `gorilla/csrf` on the back end); everything else is the standard library. No
  build pipeline or `node_modules`.

## 3. Scope

### 3.1 In scope (HTTP-reachable)

The UI MUST expose every user flow reachable over the kernel HTTP API:

* Server status (`GET /health`) and federation metadata
  (`GET /.well-known/juice-kernel.json`), read-only.
* Authentication: browser login via authorization-code with PKCE, token
  refresh with rotation, and logout.
* User: account creation and self profile (`GET /v1/me`), including
  `available` and `locked` balances.
* Actions: create, list/show (public discovery; owner-private listing of
  inactive actions is **pending verification**, §10), update, enable, disable,
  delete; ACL grant/revoke (`read`/`call`/`admin`); grant-all/revoke-all;
  OpenAPI import/unimport; stats; ratings list.
* Processes: start, list, show, fund, end.
* Call execution: `POST /v1/run`.
* Transactions: list, show, rate.
* Steps: step create/list/show/complete.

The following are in scope **conditionally**, only once their HTTP routes are
confirmed on the running kernel (§10); until then they are not mandatory:

* Traces and receipts: read-only views attached to transactions and processes.

### 3.2 Out of scope (no HTTP endpoint)

These kernel operations are CLI-only and MUST NOT be attempted from the UI:

* **Admin / supervision:** user list/show, suspend/unsuspend, **credit
  deposits and withdrawals**, admin action/process/transaction listings, admin
  action disable.
* **Federation management:** friending/unfriending peers (`POST /v1/peers`,
  `GET /v1/gossip`) and the remote import lifecycle are HTTP endpoints in v0.3
  but remain out of scope — they are operator-level concerns with no user-facing
  flow.

The corresponding `flows.sh` flows are non-goals for this UI: `bootstrap`,
`suspension`, `deposits`, `admin_supervision`, and all `federation_*` flows.

### 3.3 Known limitations

* Because deposits are admin-only, a UI user cannot top up their own credit
  balance. Balance views MUST state plainly that credit top-ups happen
  out-of-band (superuser/CLI). The UI still shows balances and funds processes
  from existing credits.

## 4. API contract the UI must honor

The UI MUST conform to the kernel API design rules in `docs/API.md`:

* Snake_case JSON on the wire; never expose Go field names; never echo secret
  fields.
* Structured fields (`args`, `result`) are inline JSON objects, not
  double-encoded strings.
* Action identity is `@owner/name`. The UI MUST parse references by a
  **single split on the first `/` after `@`** — owner before, name (the
  remainder) after — and MUST NOT split on every `/`. Action names may contain
  `/` (e.g. `@sys/llm/chat`); the parse rule is unambiguous because handles
  cannot contain `/`. When `@owner/name` appears in a URL it is percent-encoded.
* Action read/list responses carry both `id` (UUID, for management) and a
  computed `action` (`@owner/name`, for calling). The UI uses `action` directly
  in call requests and never asks the user to type a UUID.
* State-mutating responses return the updated resource; the UI re-renders from
  that response rather than guessing. Destructive operations and `end` return
  204.
* List responses are bare arrays; pagination metadata is in headers.
* `POST /v1/run` and `POST /v1/steps/{id}/complete` always include `args`;
  empty input is `{}`.

## 5. Authentication and session

* Browser login uses authorization-code with PKCE. The BFF renders the login
  form, generates the verifier/challenge (verifier kept server-side), calls
  `POST /v1/auth/authorize`, extracts the `code` from the returned redirect, and
  exchanges it at `POST /v1/auth/token` with the verifier. The resulting tokens
  are stored in the server-side session.
* A single request wrapper handles token refresh: on `ErrUnauthenticated`, it
  performs `POST /v1/auth/refresh`, stores the rotated token pair, and retries
  the original request once. Refresh MUST be single-flight per session, because
  rotation invalidates the prior refresh token and concurrent refreshes would
  fail.
* When refresh fails, or the user is suspended, the session is cleared and the
  user is returned to login with a clear explanation.
* Logout calls `POST /v1/auth/logout` to revoke the refresh token and drops the
  session.
* The session cookie is `HttpOnly`, `Secure`, and `SameSite`. All state-changing
  routes are CSRF-protected.
* Account creation (`POST /v1/users`) is available from the UI; the UI surfaces
  the kernel's per-IP rate limiting (HTTP `429`) gracefully.

## 6. Functional requirements by surface

### 6.1 Dashboard

* Show the authenticated user (`GET /v1/me`) with `available` and `locked`
  credits distinguished, and the user's funded sessions. The dashboard filters
  to **open** processes; closed processes (including BFF-created ephemeral ones,
  see §6.5) are not pretended away but live in a full, honest process history
  rather than cluttering the dashboard.

### 6.2 Marketplace and discovery

* *(Mandatory, first-class.)* Action discovery is a core feature of the UI. The
  primary way to find actions is **natural-language search backed by
  `@sys/lookup`**: the user describes what they want, and the UI calls
  `@sys/lookup` (required `query`, optional `limit`) via `POST /v1/run` (lookup
  is a price-0 native action; the process closes immediately). Results are ranked `results[]` of `{action_id, name,
  owner_handle, description, score}` and are presented prominently, each linking
  to the action detail / Run panel (§6.2). Because `action_id`, `owner_handle`,
  and `name` are returned together, lookup results also yield a callable
  `@owner/name` and a manageable `id` directly.
* An action detail view shows full metadata, input/output schemas, stats, and
  ratings, with a Run panel (§6.5).
* Catalog browse is the secondary path: `GET /v1/actions` (with owner/name
  filters) lists public, active actions with price, description, and stats
  (uses, success/failure, rating mean and count, latency mean). It is also the
  fallback when lookup is unavailable.
* Discovery degrades honestly. If `@sys/lookup` returns `ErrInvalidState`
  (e.g. no embedder configured on the kernel, see §10), the UI falls back to
  catalog search and tells the user semantic search is unavailable, rather than
  presenting a dead search box. The discovery feature itself remains mandatory;
  only its semantic-ranking quality is environment-dependent.

### 6.3 Studio (author and manage own actions)

* The Studio page has two zones: a **creation zone** (see AI synthesis bullet
  below) at the top and an **action management list** below. The list shows the
  owner's actions including inactive ones (owner-private view), filterable by
  active/inactive state; each card shows kind, status, price, and public state.
  Create actions (`POST /v1/actions`) — created inactive by default.
* Editing source, schema, kind, or price warns that the action will be
  deactivated, matching kernel update rules.
* Enable/disable, delete (preserving history). Public/private visibility is
  toggled via `PUT /v1/actions/{id}` with `{public: true|false}`; there are no
  separate ACL, grant-all, or revoke-all operations. Owner-only operations are
  presented only to the owner; the kernel remains the authority. Active/inactive
  and public/private state changes are inline toggles on the action list card,
  updated via htmx without a full page reload.
* For `kind=wasm`, the UI notes that the artifact compiles on enable and
  surfaces compilation failure as a typed error.
* Action-source URL input MAY be preflighted (e.g. obvious non-HTTP(S) or
  loopback) to give fast feedback, but the kernel remains authoritative on URL
  acceptance (non-HTTP(S), loopback, RFC-1918 private, link-local). The UI MUST
  surface the kernel's rejection clearly rather than reimplement its policy.
* `@sys/make` is exposed in the creation zone as an AI-synthesis path alongside
  manual creation. The user provides a natural-language description; the BFF
  calls `POST /v1/run {action: "@sys/make", args: {description: "..."}}` (the
  kernel atomically creates and closes the process), shows synthesis progress,
  and renders the result inline from the response
  `{status, action_id, action_name, diagnostics, tests}`:
  `status="success"` → `action_name` (`@owner/name`) linked to its edit view;
  `status="failure"` → the `diagnostics[]` field as synthesis feedback (not
  surfaced as a kernel error). The closed process appears in history (see §6.1).
  Fewer than 20 available credits blocks submission with a clear message.

### 6.4 OpenAPI import

* Import operations from an OpenAPI spec (`POST /v1/actions/import`), showing
  created/updated/deactivated results; import is idempotent.
* Draft import is allowed without proof, but the kernel requires API ownership
  proof before public activation or grant-all of an imported action. A
  UI-guided proof step (well-known challenge, challenge embedded in the spec,
  or a verified credential) is **conditional** on the kernel exposing an HTTP
  protocol for challenge creation, credential submission, and proof status (§10);
  until those endpoints are confirmed, the UI surfaces the kernel's rejection on
  attempted public activation rather than driving a proof flow.
* Reimport of a changed spec is presented as "changed → deactivated; re-verify
  and re-enable," never as a silent contract change. Unimport
  (`POST /v1/actions/unimport`) deactivates matching actions without deleting
  history.

### 6.5 Calling actions

* Every call goes through `POST /v1/run {action, args}`. The kernel atomically
  creates a process funded with exactly `action.price`, executes the root call,
  and closes the process when the call returns (unless outstanding steps keep it
  open). The resulting process is **not hidden**: it appears immediately in closed
  process history (see §6.1), or as an open process while steps remain
  outstanding. The UI MUST NOT imply no process occurred.
* A process kept open by outstanding steps appears in the dashboard open-process
  list. The user can force-abort it (`POST /v1/processes/{id}/end`), which
  cancels waiting steps and returns their parked funds to the user.
* A cost preview (action.price) is shown before the call. The user needs
  sufficient available credits; `ErrInsufficientFunds` is surfaced with a clear
  message if not.
* Call input is collected via a form generated from `input_schema` (typed fields,
  required markers, field descriptions as help text), with a raw-JSON escape
  hatch for advanced input. `output_schema` is shown as the expected result
  shape; results render as formatted inline JSON.
* The three outcome classes render distinctly:
  * input-schema rejection → "Not sent; you were not charged" (no transaction);
  * execution, output-schema, or timeout failure → "Failed; fully refunded,"
    with the failure transaction linked;
  * success → result plus a receipt link and a cost breakdown of
    **gross / fee / net**, where the fee is described as applying to value added.

### 6.6 Transactions, receipts, traces, ratings

* List and view transactions (`GET /v1/transactions`), which returns all
  transactions where the user is buyer (`owner_user_id`), caller
  (`caller_user_id`), or seller (the called action's owner). Each row shows the
  user's role (`buyer` / `seller`, both when calling one's own action), the
  action called (`@owner/name`, linked to the action detail page, resolved via
  `GET /v1/actions/{action_id}`; show a truncated ID if the action has since been
  deleted), status, and cost breakdown. The transaction detail page renders as a
  receipt: action ref, buyer and seller parties, gross/fee/net breakdown, inline
  args and result, `reason` on failure, and the rating control for the buyer.
* A transaction detail view shows its immutable receipt via
  `GET /v1/transactions/{id}/receipt-verification`, which returns per-check
  results (signature validity, hash integrity, settlement amounts,
  args/reply match) and a top-level `valid` flag, for both success and failure.
* *(Conditional — §10.)* Once a trace-read HTTP route is confirmed, a transaction
  detail view shows its trace tree with cumulative `cost` and `latency_ms`,
  visualizing contractor sub-calls and step-completed calls as cross-process
  `FOLLOWS_FROM` links, and making visible that the caller's process is debited
  only by `action.price`.
* The buyer may rate a transaction once (`POST .../rate`, rating 0 or 1, optional
  `note` string). The rate control appears only for the buyer and only when
  unrated; duplicate and non-buyer attempts map to friendly errors.

### 6.7 Steps

* Create a step (`POST /v1/steps`) with `action` (`@owner/name`), `process_id`,
  `parent_trace_id`, `partial_args`, `input_schema`, and `required_caller`
  (`@handle`). The kernel stores `required_caller_user_id`; the UI presents the
  handle and passes it in `required_caller`.
* List steps (`GET /v1/steps`), filterable by `process_id` and `status`
  (`waiting` / `running` / `done`); show a single step (`GET /v1/steps/{id}`),
  including the computed `action` (`@owner/name`).
* Complete a step (`POST /v1/steps/{id}/complete`) — available only to the
  `required_caller` user. The body carries `args` (defaulting to `{}`);
  completion merges caller `args` with the step's `partial_args` and executes
  the call. The response shape is `{result, tx_id, trace_id, step_id}`.
* The steps UI is an **inbox**: waiting steps that require the signed-in user
  appear as completable cards; all others go to a history table. The
  create-step form is a developer tool, shown collapsed.
* The step completion form shares the same schema-driven field rendering and
  outcome display as the run UI (§6.5); `partial_args` fields are shown
  read-only as locked context; only fields absent from `partial_args` are
  editable. When `partial_args` covers all schema fields, the form collapses
  to a single confirmation button.

## 7. Error handling

* The client maps the kernel's typed errors to specific, actionable user messages
  and UI affordances: `ErrUnauthenticated` (incl. suspension) → clean logout
  with explanation; `ErrUnauthorized` → not-permitted message; `ErrNotFound`;
  `ErrInvalidInput` / `ErrSchemaViolation` → field-level form errors;
  `ErrInvalidState`; `ErrInsufficientFunds` → prompt to top up the session;
  `ErrExecutionFailed` / `ErrTimeout` → "the action failed and your funds were
  fully refunded," with the failure transaction linked when the kernel records one
  (these occur after execution begins, so — unlike an input-schema rejection — a
  failure transaction normally exists); `ErrInternal`. Raw status codes are never
  shown to the user.
* Diagnostic detail goes to logs, never to the user-facing message, and never
  includes tokens or secrets.

## 8. Non-functional requirements

* Modern, polished, responsive, and accessible interface (keyboard navigation,
  sufficient contrast, labeled controls).
* Long-running calls show progress and never hang on an indefinite spinner; the
  UI reflects timeouts honestly.
* No secrets or tokens in HTML, URLs, client storage, or logs.
* Structured logs to stderr include a request ID and, where available, the
  subject and resource identifiers.
* The UI ships **light and dark themes from a single set of semantic design
  tokens** (CSS custom properties); components reference tokens only, never raw
  colors, so the themes stay in lockstep. The aesthetic is a warm,
  Anthropic/Claude-inspired look: a warm charcoal dark mode and a warm ivory
  light mode (not cool blue-black / pure white), a muted terracotta "clay"
  accent reserved **strictly for primary/brand actions**, a sans-serif grotesque
  for the UI, and monospace for money, IDs, hashes, and `@owner/name`. The exact
  values are tuned by eye; this is inspired-by, not a replica of any brand's
  proprietary tokens.
* Theme selection defaults to the OS `prefers-color-scheme`; an explicit user
  choice is persisted **server-side** (a `theme` cookie the BFF reads) and
  stamped onto the document at render time, so server-rendered pages show the
  correct theme with **no flash of the wrong theme** and the toggle works without
  JavaScript. Motion respects `prefers-reduced-motion`.
* Both themes meet WCAG AA contrast (≥4.5:1 body text, ≥3:1 large text and UI
  affordances). The credited / refunded / escrow-pending status colors MUST
  remain mutually distinct **and** distinct from the clay accent in both themes,
  because that three-way distinction is a financial trust signal.

## 9. Testing

* Every production `.go` file has a `_test.go` file with independent tests.
  `go test ./...` passes without network access; the kernel API is faked in tests
  (no live kernel required).
* Required coverage includes: the typed API client and error mapping; session
  lifecycle including PKCE handshake and single-flight refresh rotation; CSRF
  protection on state-changing routes; JSON-Schema-to-form rendering; the
  atomic run primitive (`POST /v1/run`, cost preview, process auto-closure,
  force-end); the three call-outcome classes; and access control of owner-only
  UI affordances.

## 10. Open items to confirm against the running kernel

Each item below is a prerequisite for a feature marked conditional above. The
feature is mandatory only once the corresponding endpoint is confirmed on the
running kernel; otherwise it stays conditional or is dropped from scope.

* *(traces — §6.6)* No direct trace-read HTTP route is listed in `docs/API.md`;
  confirm whether one exists on the running kernel, then make the conditional
  §6.6 trace bullet mandatory and update `docs/API.md`. (Receipts are confirmed
  and no longer conditional — see `GET /v1/transactions/{id}/receipt-verification`.)
* *(action naming — §4)* Confirmed: action names may contain `/`. The native
  chat action is `@sys/llm/chat`. The parse rule (split on first `/` after `@`)
  is unambiguous and mandatory.
* *(owner-private listing — §3.1, §6.3)* Confirmed: when the authenticated user
  queries `GET /v1/actions?owner=@self`, the kernel returns all owned actions
  regardless of active/public state. Unauthenticated or cross-user queries still
  return only active+public actions.
* *(OpenAPI ownership proof — §6.4)* Confirm whether the kernel exposes any HTTP
  protocol for ownership-proof challenge creation, credential submission, and
  proof status. If not, the UI cannot drive a proof flow and only surfaces the
  kernel's rejection on public activation.
* *(lookup operational status — §6.2)* Confirm whether `@sys/lookup` is
  operational on the running kernel (an embedder is configured) or returns
  `ErrInvalidState`. This does not gate the discovery feature, which is mandatory
  regardless; it determines whether semantic ranking is available or the UI runs
  in catalog-fallback mode.
