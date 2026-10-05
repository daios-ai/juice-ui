# Kernel requests from juice-ui

Two additions to the kernel's user-facing surface that the UI needs and cannot
build on its own. Both are stated as contracts, read against `juicedocs` at
kernel `ba7f5e5`; neither prescribes an implementation.

## 1. A search-hint read over the catalog

**Need.** The search bar offers search hints while a person types, as a
search engine completes a query: catalog titles, each of which runs the search
when chosen. A hint is a query, not an action, so it needs no address, terms
or proof of existence. The source is the catalog the kernel already holds: its
local actions and the gossip discovery cache.

**Why the kernel.** The discovery cache has two reads today. `sys/lookup` is a
run, so one call per keystroke would write one transaction per keystroke at an
operator-set price. `admin peer inspect` is superuser-only and per peer.
`GET /v1/actions` is free but serves only local rows and resolved proxies. No
free read of discovered titles exists for an ordinary user.

**Request.**

```
GET /v1/suggestions?q=<text>&limit=<n>   →   ["<title>", …]
```

- **Source:** active local actions the caller may see, and the discovery
  cache, in one reply.
- **Matching:** lexical only, a case-insensitive substring match on the
  title. No semantic leg, so no embedding call.
- **Payload:** distinct titles alone. No address, schema, evidence or price:
  a hint feeds the search, and the search returns the actions.
- **Bounds:** `limit` default 8, ceiling 20. An empty `q` answers `[]`, so the
  reply is never the catalog.
- **Cost:** a plain read. No run, transaction or ledger entry, and no price.
- **Access:** authenticated. Local rows pass the ordinary per-row visibility
  gate. Discovered rows are served as held.

**To confirm on the kernel side.** That every row in the discovery cache is
public by construction (only public actions are gossiped), so serving it to
any authenticated user discloses nothing new.

**UI until then.** Distinct titles from one page of `GET /v1/actions` (local
rows only).

## 2. An entry point for creating a task from outside a call

**Need.** On an action's run form a person fills the fields they know and
sends the rest to someone else: a task on that action with `partial_args` set
and a `required_caller`. The recipient side exists already (`allowed_input`
renders as the completion form in the inbox).

**Why the kernel.** `POST /v1/tasks` requires a funding `trace_id` and
precondition-4 authority over it. Only `run` creates a process, and the
process closes once its root call has returned with no task outstanding. A
user on an action page therefore holds no open trace to fund a task from; the
kernel answers `409 create task: the call has already settled`. `sys/message`
does park a task from a run, but only for `sink` with `{message}`.

**Request.** A native, runnable like `sys/message`, that parks a task for an
arbitrary action:

```
run sys@kernel/task {action, partial_args, required_caller}   →   {task_id}
```

- The run's process stays open while the task waits, as with `sys/message`.
- The target's price is snapshotted and parked at creation, funded by the
  caller on top of the native's own price. This is the part `run` cannot
  express today, since a run funds exactly the action's price.
- The caller must pass `CanCall` on the target, and `partial_args` must be a
  valid partial of its input schema (no undeclared key, each given value
  valid for its property).
- Decline and completion follow the existing task rules unchanged.

**UI once served.** A second submit on the run form with a recipient field.
Filled fields become `partial_args`; a nullable field's `null` box and a blank
field keep their meanings (null is sent, blank is left to the recipient).
