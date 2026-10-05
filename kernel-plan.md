# Kernel requests from juice-ui

An addition to the kernel's user-facing surface that the UI needs and cannot
build on its own, stated as a contract, read against `juicedocs` at kernel
`034426e`; it prescribes no implementation. (A first request, a free read of
the catalog for search hints, was met by `034426e`: `GET /v1/actions` now
serves the whole catalog, discovered actions included.)

## An entry point for creating a task from outside a call

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
