# Juice Web UI Requirements

Version: 0.6
Status: design draft — endpoint surface pending verification against the running kernel
Codename: `juice-ui`

> **Gating condition.** A UI feature is mandatory only if it maps to an HTTP
> endpoint **confirmed on the running kernel**. Anything unconfirmed is named as
> an explicit kernel/API prerequisite in §10 and treated as conditional until
> verified. Where the running kernel supports something `docs/API.md` omits, the
> fact is recorded here and `docs/API.md` is updated. This follows from R1.1,
> R1.4, and R2.5: the BFF may compose HTTP calls, but must not infer, emulate, or
> bypass kernel state transitions.

## 1. Purpose and architecture

A modern web user interface for the Juice kernel, implemented in Go. It exposes
the Juice user flows (see `docs/flows.sh`) and talks to the kernel **only over
the documented HTTP API** (see `docs/API.md`); it never shells out to the CLI,
reads the kernel database, or imports kernel packages.

The UI is a **backend-for-frontend (BFF)**: a Go HTTP server that renders the
interface and proxies every request to the kernel API, holding the user session
server-side.

* **R1.1** The browser communicates only with the BFF. The BFF is the sole
  client of the kernel HTTP API.
* **R1.2** Kernel access tokens and refresh tokens are stored server-side in the
  session. They are never sent to the browser, embedded in HTML, or written to
  logs.
* **R1.3** The interface is server-rendered HTML with small client-side scripts
  for schema-driven forms and event polling. A heavy single-page framework is
  not required and SHOULD be avoided.
* **R1.4** The kernel is treated as authoritative. The BFF holds no durable
  domain state of its own beyond the session (tokens, PKCE state, CSRF token).

## 2. Implementation constraints

* **R2.1** Use Go. `go build ./...` and `go test ./...` MUST pass without
  external network access.
* **R2.2** Keep the package and source-file counts small. Do not split files for
  size alone. Every production `.go` file MUST have a corresponding `_test.go`
  file with independent tests for its logic.
* **R2.3** Suggested package layout (function-named, may change for cohesion or
  cycle reasons):

  ```text
  cmd/juice-ui/   entrypoint: configuration, dependency wiring, serve
  client/         typed kernel HTTP API client and error mapping
  session/        server-side sessions, token lifecycle, PKCE state, CSRF
  server/         router, middleware, request handlers, rendering
  templates/      HTML templates (not Go; no test file required)
  static/         CSS and client scripts (not Go; no test file required)
  ```

* **R2.4** Configuration is loaded from environment variables with safe local
  defaults. At minimum: kernel API base URL (default `http://127.0.0.1:4040`,
  the kernel's listen port), the UI listen address, and a session secret. Commit
  no secrets. Reject invalid startup configuration clearly.
* **R2.5** Do not reproduce kernel business logic (fees, settlement, schema
  validation) in the UI. The UI presents kernel results; the kernel decides.
* **R2.6** Technology stack (standard-library-first; dependencies only where
  hand-rolling would be a correctness or security risk):
  * **Routing:** stdlib `net/http` ServeMux (Go 1.22+ method/pattern routing,
    `{id}` wildcards) — no router dependency.
  * **Templating:** stdlib `html/template` (auto-escaping), with partials that
    double as htmx swap fragments.
  * **Interactivity:** **htmx** plus small vanilla ES modules — no build step,
    no Node, no SPA framework. Handlers return HTML fragments; htmx performs
    swaps and the event-inbox polling. Vanilla JS is limited to the
    schema→form renderer and the theme toggle.
  * **Styling:** hand-written CSS driven by the R8.5 design tokens; no CSS
    framework, no build pipeline.
  * **Sessions:** `alexedwards/scs` (server-side token storage per R1.2).
  * **CSRF:** `gorilla/csrf` (per R5.5). Security-sensitive primitives are not
    hand-rolled.
  * **PKCE / crypto:** stdlib `crypto/rand`, `crypto/sha256`, `encoding/base64`.
  * **Logging:** stdlib `log/slog` (structured, per R8.4).
  * **Configuration:** stdlib `os` with a small typed loader.
  * **Tests:** stdlib `testing` and `net/http/httptest`; the kernel is faked as
    an `httptest.Server` (per R9 — no network, no live kernel).

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
* Call execution: `POST /v1/call`.
* Transactions: list, show, rate.
* Listeners and events: listener create/list/show/delete; event
  emit/list-pending/consume.

The following are in scope **conditionally**, only once their HTTP routes are
confirmed on the running kernel (§10); until then they are not mandatory:

* Traces and receipts: read-only views attached to transactions and processes.

### 3.2 Out of scope (no HTTP endpoint)

These kernel operations are CLI-only and MUST NOT be attempted from the UI:

* **Admin / supervision:** user list/show, suspend/unsuspend, **credit
  deposits**, admin action/process/transaction listings, admin action disable.
* **Remote kernels / federation management:** `remote add/list/import/unimport`
  and the federation import lifecycle.

The corresponding `flows.sh` flows are non-goals for this UI: `bootstrap`,
`suspension`, `deposits`, `admin_supervision`, and all `federation_*` flows.
Server-side restart behavior (in-flight event reset) is likewise out of scope.

### 3.3 Known limitations

* **R3.3.1** Because deposits are admin-only, a UI user cannot top up their own
  credit balance. Balance views MUST state plainly that credit top-ups happen
  out-of-band (superuser/CLI). The UI still shows balances and funds processes
  from existing credits.

## 4. API contract the UI must honor

The UI MUST conform to the kernel API design rules in `docs/API.md`:

* **R4.1** Snake_case JSON on the wire; never expose Go field names; never echo
  secret fields.
* **R4.2** Structured fields (`args`, `result`) are inline JSON objects, not
  double-encoded strings.
* **R4.3** Action identity is `@owner/name`. The UI MUST parse references by a
  **single split on the first `/` after `@`** — owner before, name (the
  remainder) after — and MUST NOT split on every `/`. This rule is correct
  whether or not names may contain `/`, so it is mandatory now. When
  `@owner/name` appears in a URL it is percent-encoded. **Pending R10.2:**
  whether a name may legally contain `/`, and the canonical spelling of the
  native chat action, are unverified; the UI MUST NOT hard-code either
  interpretation — in particular it MUST NOT reject `/` in a name on input nor
  build call URLs without percent-encoding — until R10.2 is resolved.
* **R4.4** Action read/list responses carry both `id` (UUID, for management) and
  a computed `action` (`@owner/name`, for calling). The UI uses `action`
  directly in call requests and never asks the user to type a UUID.
* **R4.5** State-mutating responses return the updated resource; the UI
  re-renders from that response rather than guessing. Destructive operations and
  `end` return 204.
* **R4.6** List responses are bare arrays; pagination metadata is in headers.
* **R4.7** `POST /v1/call` and `POST /v1/events/emit` always include `args`;
  empty input is `{}`. Emit never sends `source_user_id`.

## 5. Authentication and session

* **R5.1** Browser login uses authorization-code with PKCE. The BFF renders the
  login form, generates the verifier/challenge (verifier kept server-side),
  calls `POST /v1/auth/authorize`, extracts the `code` from the returned
  redirect, and exchanges it at `POST /v1/auth/token` with the verifier. The
  resulting tokens are stored in the server-side session.
* **R5.2** A single request wrapper handles token refresh: on
  `ErrUnauthenticated`, it performs `POST /v1/auth/refresh`, stores the rotated
  token pair, and retries the original request once. Refresh MUST be
  single-flight per session, because rotation invalidates the prior refresh
  token and concurrent refreshes would fail.
* **R5.3** When refresh fails, or the user is suspended, the session is cleared
  and the user is returned to login with a clear explanation.
* **R5.4** Logout calls `POST /v1/auth/logout` to revoke the refresh token and
  drops the session.
* **R5.5** The session cookie is `HttpOnly`, `Secure`, and `SameSite`. All
  state-changing routes are CSRF-protected.
* **R5.6** Account creation (`POST /v1/users`) is available from the UI; the UI
  surfaces the kernel's per-IP rate limiting (HTTP `429`) gracefully.

## 6. Functional requirements by surface

### 6.1 Dashboard

* **R6.1.1** Show the authenticated user (`GET /v1/me`) with `available` and
  `locked` credits distinguished, and the user's funded sessions. The dashboard
  filters to **open** processes; closed processes (including BFF-created
  ephemeral ones, see R6.5.1) are not pretended away but live in a full,
  honest process history rather than cluttering the dashboard.

### 6.2 Marketplace and discovery

* **R6.2.1** *(Mandatory, first-class.)* Action discovery is a core feature of
  the UI. The primary way to find actions is **natural-language search backed by
  `@sys/lookup`**: the user describes what they want, and the UI calls
  `@sys/lookup` (required `query`, optional `limit`) via the normal call path
  (`POST /v1/call`, through the funded-session abstraction of R6.5.1 — lookup is
  a price-0 native action). Results are ranked `results[]` of `{action_id, name,
  owner_handle, description, score}` and are presented prominently, each linking
  to the action detail / Run panel (§6.2.2). Because `action_id`, `owner_handle`,
  and `name` are returned together, lookup results also yield a callable
  `@owner/name` and a manageable `id` directly.
* **R6.2.2** An action detail view shows full metadata, input/output schemas,
  stats, and ratings, with a Run panel (§6.5).
* **R6.2.3** Catalog browse is the secondary path: `GET /v1/actions` (with
  owner/name filters) lists public, active actions with price, description, and
  stats (uses, success/failure, rating mean and count, latency mean). It is also
  the fallback when lookup is unavailable.
* **R6.2.4** Discovery degrades honestly. If `@sys/lookup` returns
  `ErrInvalidState` (e.g. no embedder configured on the kernel, see R10.7), the
  UI falls back to catalog search (R6.2.3) and tells the user semantic search is
  unavailable, rather than presenting a dead search box. The discovery feature
  itself remains mandatory; only its semantic-ranking quality is
  environment-dependent.

### 6.3 Studio (author and manage own actions)

* **R6.3.1** List the owner's actions including inactive ones (owner-private
  view). Create actions (`POST /v1/actions`) — created inactive by default.
* **R6.3.2** Editing source, schema, kind, or price warns that the action will
  be deactivated, matching kernel update rules.
* **R6.3.3** Enable/disable, delete (preserving history), and manage ACLs and
  grant-all/revoke-all. ACL **grant** uses `POST /v1/actions/{id}/acl` with body
  `{subject_user_id, permission}`; ACL **revoke** uses
  `DELETE /v1/actions/{id}/acl/{subject_id}/{permission}` with no body
  (permission ∈ `read`/`call`/`admin`). Because grant takes a `subject_user_id`
  (UUID), the UI requires a documented handle→user-ID resolution path (§10).
  Owner-only operations are presented only to the owner; the kernel remains the
  authority.
* **R6.3.4** For `kind=wasm`, the UI notes that the artifact compiles on enable
  and surfaces compilation failure as a typed error.
* **R6.3.5** Action-source URL input MAY be preflighted (e.g. obvious non-HTTP(S)
  or loopback) to give fast feedback, but the kernel remains authoritative on URL
  acceptance (non-HTTP(S), loopback, RFC-1918 private, link-local). The UI MUST
  surface the kernel's rejection clearly rather than reimplement its policy
  (R2.5).

### 6.4 OpenAPI import

* **R6.4.1** Import operations from an OpenAPI spec
  (`POST /v1/actions/import`), showing created/updated/deactivated results;
  import is idempotent.
* **R6.4.2** Draft import is allowed without proof, but the kernel requires API
  ownership proof before public activation or grant-all of an imported action.
  A UI-guided proof step (well-known challenge, challenge embedded in the spec,
  or a verified credential) is **conditional** on the kernel exposing an HTTP
  protocol for challenge creation, credential submission, and proof status
  (§10); until those endpoints are confirmed, the UI surfaces the kernel's
  rejection on attempted public activation rather than driving a proof flow.
* **R6.4.3** Reimport of a changed spec is presented as "changed → deactivated;
  re-verify and re-enable," never as a silent contract change. Unimport
  (`POST /v1/actions/unimport`) deactivates matching actions without deleting
  history.

### 6.5 Funded sessions and calling actions

* **R6.5.1** The process/escrow model is abstracted as a "funded session." For
  **price-0 actions** (including `@sys/lookup` and the native chat action,
  naming per R10.2), the BFF auto-provisions a zero-funded ephemeral process,
  executes the call, and ends it. This process is **hidden from the run UI**, not
  nonexistent: the kernel still creates it, and once ended it appears as a closed
  process in history (R6.1.1). The UI MUST NOT imply no process occurred.
* **R6.5.2** For **paid actions**, the user opens or reuses a funded session
  (`POST /v1/processes`), and calls draw from it. The UI shows escrow draining,
  supports top-up (`POST /v1/processes/{id}/fund`), and "end & reclaim"
  (`POST /v1/processes/{id}/end`) which refunds the remainder.
* **R6.5.3** Calls use `POST /v1/call` with `action` as `@owner/name` and `args`
  defaulting to `{}`. A cost preview is shown before the call.
* **R6.5.4** Call input is collected via a form generated from `input_schema`
  (typed fields, required markers, field descriptions as help text), with a
  raw-JSON escape hatch for advanced input. `output_schema` is shown as the
  expected result shape; results render as formatted inline JSON.
* **R6.5.5** The three outcome classes render distinctly:
  * input-schema rejection → "Not sent; you were not charged" (no transaction);
  * execution, output-schema, or timeout failure → "Failed; fully refunded,"
    with the failure transaction linked;
  * success → result plus a receipt link and a cost breakdown of
    **gross / fee / net**, where the fee is described as applying to value added.

### 6.6 Transactions, receipts, traces, ratings

* **R6.6.1** List and view transactions (`GET /v1/transactions`), showing
  status, cost breakdown, inline args and result, and `reason` on failure.
* **R6.6.2** *(Conditional — §10.)* Once a receipt-read HTTP route is confirmed,
  a transaction detail view shows its immutable receipt (hashes, economic fields,
  signature) for both success and failure.
* **R6.6.3** *(Conditional — §10.)* Once a trace-read HTTP route is confirmed, a
  transaction detail view shows its trace tree with cumulative `cost` and
  `latency_ms`, visualizing contractor sub-calls and event-triggered calls as
  cross-process `FOLLOWS_FROM` links, and making visible that the caller's
  process is debited only by `action.price`.
* **R6.6.4** The buyer may rate a transaction once (`POST .../rate`, rating 0 or
  1). The rate control appears only for the buyer and only when unrated;
  duplicate and non-buyer attempts map to friendly errors.

### 6.7 Listeners and events

* **R6.7.1** Create listeners, list, show, and delete (delete purges pending
  events). The HTTP create body is `{source_user_id, event_name,
  target_action_id}` — both IDs are UUIDs. The UI presents a source `@handle`
  and a target `@owner/name`, so it requires documented resolution paths
  handle→user-ID and action-reference→action-ID (§10). Action-reference→ID is
  likely satisfiable via action listing filters; handle→user-ID is not yet
  confirmed.
* **R6.7.2** Emit events (`POST /v1/events/emit`); the source is always the
  authenticated user.
* **R6.7.3** An inbox polls pending events for a listener
  (`GET /v1/listeners/{id}/events`) and consumes them
  (`POST /v1/events/{id}/consume`) through the funded-session model. Already-
  consumed and non-owner states are shown explicitly.

## 7. Error handling

* **R7.1** The client maps the kernel's typed errors to specific, actionable
  user messages and UI affordances: `ErrUnauthenticated` (incl. suspension) →
  clean logout with explanation; `ErrUnauthorized` → not-permitted message;
  `ErrNotFound`; `ErrInvalidInput` / `ErrSchemaViolation` → field-level form
  errors; `ErrInvalidState`; `ErrInsufficientFunds` → prompt to top up the
  session; `ErrExecutionFailed` / `ErrTimeout` → "the action failed and your
  funds were fully refunded," with the failure transaction linked when the
  kernel records one (these occur after execution begins, so — unlike an
  input-schema rejection — a failure transaction normally exists);
  `ErrInternal`. Raw status codes are never shown to the user.
* **R7.2** Diagnostic detail goes to logs, never to the user-facing message, and
  never includes tokens or secrets.

## 8. Non-functional requirements

* **R8.1** Modern, polished, responsive, and accessible interface (keyboard
  navigation, sufficient contrast, labeled controls).
* **R8.2** Long-running calls show progress and never hang on an indefinite
  spinner; the UI reflects timeouts honestly.
* **R8.3** No secrets or tokens in HTML, URLs, client storage, or logs.
* **R8.4** Structured logs to stderr include a request ID and, where available,
  the subject and resource identifiers.
* **R8.5** The UI ships **light and dark themes from a single set of semantic
  design tokens** (CSS custom properties); components reference tokens only,
  never raw colors, so the themes stay in lockstep. The aesthetic is a warm,
  Anthropic/Claude-inspired look: a warm charcoal dark mode and a warm ivory
  light mode (not cool blue-black / pure white), a muted terracotta "clay"
  accent reserved **strictly for primary/brand actions**, a sans-serif grotesque
  for the UI, and monospace for money, IDs, hashes, and `@owner/name`. The exact
  values are tuned by eye; this is inspired-by, not a replica of any brand's
  proprietary tokens.
* **R8.6** Theme selection defaults to the OS `prefers-color-scheme`; an explicit
  user choice is persisted **server-side** (a `theme` cookie the BFF reads) and
  stamped onto the document at render time, so server-rendered pages show the
  correct theme with **no flash of the wrong theme** and the toggle works without
  JavaScript. Motion respects `prefers-reduced-motion`.
* **R8.7** Both themes meet WCAG AA contrast (≥4.5:1 body text, ≥3:1 large text
  and UI affordances). The credited / refunded / escrow-pending status colors
  MUST remain mutually distinct **and** distinct from the clay accent in both
  themes, because that three-way distinction is a financial trust signal.

## 9. Testing

* **R9.1** Every production `.go` file has a `_test.go` file with independent
  tests. `go test ./...` passes without network access; the kernel API is
  faked in tests (no live kernel required).
* **R9.2** Required coverage includes: the typed API client and error mapping;
  session lifecycle including PKCE handshake and single-flight refresh rotation;
  CSRF protection on state-changing routes; JSON-Schema-to-form rendering;
  the funded-session abstraction (ephemeral price-0 process and paid-session
  open/fund/end); the three call-outcome classes; and access control of
  owner-only UI affordances.

## 10. Open items to confirm against the running kernel

Each item below is a prerequisite for a feature marked conditional above. The
feature is mandatory only once the corresponding endpoint is confirmed on the
running kernel; otherwise it stays conditional or is dropped from scope.

* **R10.1** *(traces & receipts — §6.6.2–6.6.3)* The exact HTTP routes and
  response shapes for trace and receipt reads are not listed in `docs/API.md`;
  confirm them against the running kernel, then make §6.6.2–6.6.3 mandatory and
  update `docs/API.md`.
* **R10.2** *(action naming — R4.3)* Confirm the canonical name of the native
  chat action (`llm/chat` per the kernel spec vs. `llm-chat` in `docs/API.md`)
  and whether action names may legally contain `/`, against a live action
  listing. This settles form routing, URL encoding, and reference parsing.
* **R10.3** *(owner-private listing — §3.1, R6.3.1)* Confirm whether
  `GET /v1/actions?owner=` with the owner authenticated returns that owner's
  inactive/private actions. If not, Studio's "manage my actions including
  inactive" has no HTTP surface and must be renegotiated.
* **R10.4** *(handle→user-ID — R6.3.3, R6.7.1)* ACL grant and listener create
  both take a `subject_user_id`/`source_user_id` UUID on the wire, but the UI
  works in `@handle`. Confirm a documented HTTP path from handle to user ID; if
  none exists, these create flows cannot be satisfied as designed.
* **R10.5** *(action-reference→action-ID — R6.7.1)* Confirm that
  `@owner/name` can be resolved to an `action_id` over HTTP (expected via
  `GET /v1/actions?owner=&name=`), as required for listener target binding.
* **R10.6** *(OpenAPI ownership proof — R6.4.2)* Confirm whether the kernel
  exposes any HTTP protocol for ownership-proof challenge creation, credential
  submission, and proof status. If not, the UI cannot drive a proof flow and
  only surfaces the kernel's rejection on public activation.
* **R10.7** *(lookup operational status — R6.2.1, R6.2.4)* Confirm whether
  `@sys/lookup` is operational on the running kernel (an embedder is configured)
  or returns `ErrInvalidState`. This does not gate the discovery feature, which
  is mandatory regardless; it determines whether semantic ranking is available
  or the UI runs in catalog-fallback mode.
