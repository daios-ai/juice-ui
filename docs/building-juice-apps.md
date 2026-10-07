# Building Juice Apps

`docs/apps.md` describes the **seam** — the `apps.App` interface, `ServiceAccount`,
`CallerKeyHeader`, the dependency rules, the checklist. Read it first for the
mechanics. This document is the **mental model and the sharp edges**: how to *think*
about an app so it composes with the rest of Juice, and the non-obvious things that
cost real debugging time on the first app (chat). It is prescriptive for every future
app.

---

## 1. An app is an HTTP service; a dedicated user is how it owns its action space

At bottom an app is a plain HTTP server — its capabilities are `kind=http` actions
pointing at its endpoints. Be precise about what actually requires a Juice identity,
because it is *less* than "the app is a user":

- **Composition needs no identity.** When the kernel dispatches to one of the app's
  actions it hands the endpoint a **trace-capability**; with it the endpoint subcalls
  and fires/completes tasks *as the executing action's owner, within that trace* — no
  login, no token, no account. Any HTTP server the kernel dispatches to can "speak
  Juice" this far. Composition and firing tasks are **not** why you need a user.

- **Owning the action space is.** Actions are owned by a user. For the app's endpoints
  to *become* callable actions at all — and for the app to register, update, enable,
  disable, and set the prices of *its own* actions without an operator doing it by
  hand — the app must be a Juice user. Running its own actions (e.g. to open a trace
  it controls, §4) is the same thing: you can only run and own what you own.

So the accurate slogan is **not** "an app is a Juice user" — it is **"an app owns its
actions."** The identity is the *mechanism* for that ownership. A wallet, durable
credentials, and the ability to hold processes come *with* being a user and you will
use them, but they are consequences of owning an action space, not the reason for it.
Keep the app's domain state (its store) as its own business; only what is async,
cross-user, or attention-bearing flows through the kernel — and when it does, reuse
the kernel's primitives rather than shadowing them (§2).

## 2. Reuse the kernel's primitives; never shadow them

The load-bearing principle. The first version of chat had a bespoke unread system: a
per-user read **watermark** in its own store, an app-scoped badge polling that
watermark, and a hand-rolled `inbox` receive action. All of it duplicated something
the kernel already provides.

> **The Task is the universal async primitive.** A notification, an inbox item, a
> delivery, a request/reply continuation, and an agent's intake are the *same object*
> — a task addressed to a user (`required_caller`). Do not build a second one.

Chat's redesign made "unread" mean *a task addressed to you*, the badge mean *the
core task badge filtered to your app*, and "reading" mean *completing the task*. The
payoff was not elegance — it was **composition**: an autonomous agent could join a
chat room with *zero* chat-specific code, because the agent already speaks tasks. A
parallel mechanism cannot do that; it is a second source of truth that diverges and
that nothing else in the ecosystem understands.

**Rule of thumb:** before adding state or a poll or a bespoke endpoint, ask *"which
kernel primitive is this?"* — a task (attention/delivery/continuation), an action (a
capability), a transaction (an economic event), a process (a lifetime). If it maps to
one, use that one, even if a local shortcut looks cheaper today.

## 3. Compose through the kernel — and your own actions are free

An app extends the kernel by **owning actions**. Two facts make this idiomatic and
cheap, and both surprised us on the first app:

- **You don't know the prices, and you don't need to.** The prices of the kernel's
  standard actions (`sys@<kernel>/*`) are **policy each operator sets** — not constants, not
  knowable at design time; `native.message.price` may be 0 on your laptop and 10 in
  production. Since a price is what *others* pay you, **calling your own action costs
  you nothing.** So never build on a shared *priced* action and assume it's cheap (a
  0-balance service account 402s the moment an operator prices it); instead **create
  your own private, price-0, owner-only action** that does the same thing. You control
  your actions' prices; you do not control `sys@<kernel>/*`'s. (`chat@<kernel>/outbox` replaced a
  per-deployment `sys@<kernel>/message` toll exactly this way.)

- **Actions can be private.** An action you own but never publish is callable only by
  you — perfect for an app's internal machinery. Create it, `enable` it, and simply
  never publish it; reconcile it *outside* the public-action loop (private, no
  delegated-auth, kept off orphan-retirement).

- **A `kind=http` action is not a leaf — it can compose.** On dispatch the kernel
  hands an HTTP action a **trace-scoped capability** (`X-Juice-Capability` header) and
  a callback base (`X-Juice-Callback`). While the call is in flight the endpoint calls
  back — `POST {callback}/v1/tasks`, `/v1/call`, `/v1/tasks/{id}/complete` — carrying
  the capability *as a header, not a bearer*, with **no `trace_id`** (the capability
  *is* the trace). This is how an out-of-kernel service participates in the execution
  graph. See `juicedocs/API.md` §"Composition from a `kind=http` action".

## 4. The economics *are* the design

There is no free async in Juice, and pretending otherwise produces bugs. Every task:

- **parks funds** (its target action's price) from a trace,
- **holds a process open** until it is completed or cancelled, and
- **writes a transaction + receipt** when it settles.

So "just send a notification" has consequences: ledger rows, accumulating open
processes, and a funding source. Design with them in view:

- Decide **who owns the process** the tasks hang off (the poster? a service account?).
  Owner choice determines whose wallet fills with open processes and whose ledger
  gets the rows. Prefer the service account, so real users' wallets/ledgers stay
  clean.
- **Coalesce.** One doorbell per (recipient, room) beats one per message — it bounds
  the open-process and ledger cost.
- **To emit tasks at all you need a live trace.** The kernel refuses a task on a
  trace that has settled — through a capability (`403 capability expired: call
  already settled`) and through a token (`409 process is closed`), measured on
  4dbfedd — so a task is minted *during* the call that funds it, through that
  call's capability. Holding a process open with a never-completed "sentinel"
  task does not keep its root trace mint-able: the trace settles when the call
  returns. Chat's seed/outbox depended on that and is blocked on it
  (`requirements.md` §10).

## 5. Identity durability is a correctness concern, not configuration

A service account's password is **locally generated and stored in app-state, and the
kernel has no password reset.** Therefore:

> If your app ever fails to find its prior app-state at the exact expected path, it
> will silently mint a **new** password and **orphan** the kernel account it already
> created. The only recovery is to retire the handle and start over.

This bit us hard: an asymmetric home-directory migration (the message DB migrated
from one legacy dir; the app-state looked in a *different* dir under a *different*
name) caused a fresh `state.json` — and a fresh, mismatched password — on every such
transition. Rules that follow:

- **Resolve app-state from exactly one canonical path.** No cwd-relative fallback, no
  "try these legacy locations." Ambiguous resolution *is* the bug.
- **Treat a credential mismatch as terminal and loud**, not a silent retry-storm.

## 6. Test against the deployment's reality, not a throwaway's defaults

Nearly every "works on my kernel, fails on yours" this session was a **fidelity gap**:

- **Native action prices are operator policy (§3), not defaults.** They were **0** on
  fresh test kernels and non-zero in prod, which hid a price dependency until it hit a
  real deployment. The fix is architectural — don't depend on a price you don't own;
  realistic test prices (and a **0-balance** service account) are how you *catch* such
  a dependency early, not something to design around.
- `http_callback_url` was *set* in test configs and *empty* (derived) in prod.
- Prod kernels carry accumulated state (existing accounts, actions, grants) a fresh
  kernel doesn't.

Discipline: when you spike a flow on a throwaway kernel, **mirror the deployment's
economics and config** — set the native prices it uses, leave `http_callback_url`
empty if it does, exercise the reconcile path against *pre-existing* actions, and test
a **0-balance** service account. Otherwise you are validating a different system. And
per this repo's `CLAUDE.md`: drive every user-story on a test kernel before writing
production code, then clean up.

## 7. The kernel diverges from its own docs — verify live

The running kernel does not always match `juicedocs/`. Treat the docs as the
*intended* contract and the running kernel as *ground truth*; when they differ, encode
the live behavior in the client and record it (see the project's
`kernel-contract-deviations` notes). Point-in-time example that cost real time:
`GET /v1/actions/{ref}` resolves only a **UUID**, not an `owner@kernel/name` address (404) —
so resolve refs via the **list** endpoint, or read the id off a task/tx that already
carries it. Assume every "this should just work" kernel call is worth one live check.

---

## Building a new app — the questions to ask first

Before the checklist in `docs/apps.md`, answer these, because they shape the design:

1. **What actions does this app own** (which public, which private), and does it need
   a dedicated user to *self-manage* them — or would an operator register them for it?
   The user exists for action ownership, nothing more.
2. **Which of its behaviors are kernel primitives?** Map every async/attention/
   delivery feature to a **task**; every capability to an **action**; every economic
   event to a **transaction**. Reuse; do not shadow.
3. **Who funds and owns the processes** its tasks hang off, and does it need a
   **seed/outbox** trace? If it emits tasks, it does.
4. **Is its identity stored durably and unambiguously?** One canonical path; derive
   what you can.
5. **Does it compose with agents/other apps for free?** If a feature only works
   because *this* app understands it, you have probably built a parallel mechanism —
   go back to (2).

Keep the domain logic Juice-agnostic (the daemon), inject the Juice-aware parts (the
plugin), and let the kernel — not your app — hold the money, the attention, and the
truth.
