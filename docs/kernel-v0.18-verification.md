# Kernel 4dbfedd — live verification

What the kernel at `../juice` 4dbfedd serves, measured on throwaway kernels
rather than read from the contract. Every claim here was produced by a request
whose reply is quoted. The previous record is `docs/kernel-v0.17-verification.md`
(9705b3b); this one covers the fourteen commits since, of which five change the
surface: `c826de8` (a step is a task), `1253eea` (a transfer reaches any
kernel), `4b323c9` (a run's charge is what left the caller), `f445a71`
(registering an address takes the password), `9a9a39a` (id prefixes; lists
show what is open), `66af590` (version), `4dbfedd` (a task reaches its party on
any kernel).

Rig: the kernel built from source (`go build ./cmd/juice`), world `play`, two
kernels under separate `JUICE_HOME`s — `alpha` (API 4181, transport
`/ip4/127.0.0.1/tcp/39931`, its world's public seed removed) and `bravo` (API
4182, transport 39932, seeded to alpha). An HTTP test service on 8901 composed
through the callback capability. Users `ana`, `vendor` on alpha; `bob`,
`seller` on bravo.

## 1. A step is a task

Every `/v1/steps` route is gone; `/v1/tasks` replaces it.

```
GET  /v1/steps                       → 404 "404 page not found" (plain text)
POST /v1/steps/{id}/complete         → 404 (plain text)
GET  /v1/tasks                       → 200 […]
```

A task row:

```json
{"id":"459512cd-…","revision":1,"status":"waiting","partial_args":{"message":"hello 2"},
 "allowed_input":{"type":"object"},"price":0,"created_at":"…",
 "action":"sys@alpha/sink","created_by":"sys@alpha/message",
 "owner":"vendor@alpha","required_caller":"ana@alpha"}
```

- **`action_id` and `parent_trace_id` are gone.** `revision` is new. A done
  task carries `tx_id`, `outcome` and `result`; a cancelled one carries neither
  and no reason.
- The completion reply names the task as `task_id`, and carries `receipt_id`
  and `charge`:
  `{"result":{},"tx_id":"e497…","trace_id":"9be3…","receipt_id":"5b65…","charge":0,"task_id":"56aa…"}`.
- `allowed_input` comes in three shapes: `{"type":"object"}` (a `sink`
  target), `{"properties":{},"required":null,"type":"object"}` (every field
  sealed), and a real schema (`{"properties":{"note":…},"required":["note"],…}`).
  A missing required field is `422 schema_violation "field note: required field missing"`;
  `{}` without `args` is `422 "args is required"`.

**The list shows open tasks by default.** After one completion and two
declines, `GET /v1/tasks` answers `[]`; `?all=1` answers all three, `?status=done`
the completed one, `?status=cancelled` the two declined. `?status=waiting`
answers waiting tasks.

**A superuser lists every account's tasks.** `GET /v1/tasks?status=waiting` as
`sys@alpha` answers a task whose `required_caller` is `ana@alpha`.

**Ids may be shortened** to four or more leading hex digits:
`GET /v1/tasks/56aa5c` answers the task; `GET /v1/tasks?process_id=nope` →
`404 "\"nope\" names no process: give its id, or its first 4 or more characters"`;
`GET /v1/transactions/ee2cb709` → `404 "no transaction of yours starts with ee2cb709"`.

## 2. A task can be declined

```
POST /v1/tasks/{id}/cancel  as the required caller   → 200 {…,"revision":2,"status":"cancelled",…}
POST /v1/tasks/{id}/cancel  as the process owner     → 200 {…,"status":"cancelled",…}
POST /v1/tasks/{id}/cancel  again                    → 409 invalid_state "task is not waiting"
POST /v1/tasks/{id}/cancel  on a done task           → 409 invalid_state "task is not waiting"
POST /v1/tasks/{id}/cancel  as an unrelated user     → 403 unauthorized "read permission denied"
POST /v1/tasks/{id}/cancel  as sys (superuser)       → 403 "only the task's required caller or its process owner may decline it"
POST /v1/tasks/{id}/complete as the process owner    → 403 "only the task's required caller may complete it"
GET  /v1/tasks/{id}          as an unrelated user    → 403 "read permission denied"
```

## 3. A task reaches its party on any kernel

A task addressed to a user of another kernel is listed on **both** kernels, in
each one's ordinary task list. Bravo's `bob` messaged `ana` on alpha
(`sys@bravo/message {"to":"ana@<alpha key>"}`; `ana@alpha` is refused there
until bravo binds a petname for alpha — `422 "to \"ana@alpha\" not found"`).

The holder (bravo) sees:

```json
{"id":"5b9795db-…","status":"waiting",…,"action":"sys@bravo/sink","created_by":"sys@bravo/message",
 "owner":"bob@bravo","required_caller":"ana@alpha","waiting_on_peer":true}
```

The addressee (alpha) sees the **same id**, without the action and its creator,
the holder named by its kernel name — the raw key while alpha has no petname
for it, `alpha` on bravo once bound:

```json
{"id":"5b9795db-…","status":"waiting","partial_args":{"message":"from bob by key"},
 "allowed_input":{"type":"object"},"price":0,"created_at":"…",
 "owner":"K98AJeQ6631CO6LJ73nK2PiQW4qY3Nb3eqquXPHYNJ8","required_caller":"ana@alpha"}
```

It is completed and declined with the ordinary routes, and the change reaches
the holder:

```
alpha  POST /v1/tasks/5b9795db/complete {"args":{}} → 200 {"result":{},"tx_id":"ee2cb709-…",
                                                       "trace_id":"…","receipt_id":"","task_id":"5b97…"}
alpha  GET  /v1/tasks/5b9795db                      → {…,"revision":3,"status":"done","tx_id":"ee2cb709-…","outcome":"success",…}
bravo  GET  /v1/tasks/5b9795db                      → the same, with action and created_by
alpha  GET  /v1/transactions/ee2cb709               → 404 "no transaction of yours starts with ee2cb709"
bravo  GET  /v1/transactions/ee2cb709 (bob)         → 200 {…,"owner":"bob@bravo","caller":"ana@alpha","action":"sys@bravo/sink"}
```

A remote completion's reply carries `receipt_id: ""` and no `charge`, and its
`tx_id` is the **holder's** transaction, which the completer cannot read on
their own kernel. A decline by the addressee and a withdrawal by the holder's
process owner both reach the other side as `cancelled`.

**A task for a whole kernel is a task for its `sys`.** A required caller of
`bravo` (bound petname) or a bare key is refused — `422 "\"bravo\" is not an address"`.
Addressed to `sys@bravo`, the task appears in `sys@bravo`'s ordinary
`GET /v1/tasks` (and in no other bravo user's), with `owner: "alpha"`, and
`sys@bravo` completes it with `POST /v1/tasks/{id}/complete`.

**The peer inspection carries no tasks.** `GET /v1/admin/peers/{key}/inspect`
answers `about, account, actions, evidence, nickname, online, petname,
public_key, reachability, source` — no `steps`, no `steps_truncated`. Its action
rows now name the action by its full address: `"name":"seller@bee/slow"`.

## 4. Tasks created through the capability

An `http` action composing through `X-Juice-Callback` / `X-Juice-Capability`:

```
POST {cb}/v1/tasks {"action":"vendor@alpha/approve","required_caller":"ana@alpha","partial_args":{"count":1}}
  → 201 {…,"allowed_input":{"properties":{"note":…},"required":["note"],…},"price":1000000,
         "created_by":"vendor@alpha/maketask","owner":"ana@alpha","required_caller":"ana@alpha"}
POST {cb}/v1/tasks {… "required_caller":"bob@<bravo key>" …}
  → 201 {…,"required_caller":"bob@bravo","waiting_on_peer":true}
```

**A payment task** — a task on `sys@alpha/transfer` — is listed like any other;
nothing on the row marks it. The effect is read from the action
(`GET /v1/actions?ref=sys%40alpha%2Ftransfer` → `"effect":"transfer"`).
Completed by `ana` with `amount` sealed, it answered
`{"result":{"amount":3000000},…,"charge":0,"task_id":…}` and journalled
`{"amount":3000000,"reason":"c81f…","operator":"ana@alpha","from":"ana@alpha","to":"vendor@alpha"}`;
with `amount` left open, the completer's typed `amount` moved.

**Chat's blocker stands.** A task created on a trace that has settled is
refused on both paths:

```
capability, 2 s after the call returned → 403 unauthorized "capability expired: call already settled"
JWT, trace of a finished run            → 409 invalid_state "process is closed"
```

## 5. Transfers run the transfer action, to any kernel

```
POST /v1/transfers                                              → 404 (plain text)
POST /v1/run sys@alpha/transfer {"target":"ana@alpha",…}        → 422 "cannot transfer to yourself"
POST /v1/run sys@alpha/transfer {"target":"nobody@bravo",…}     → 404 "remote user not found"
POST /v1/run sys@alpha/transfer {"target":"vendor@alpha","amount":500000} → 200 {…,"charge":0}
POST /v1/run sys@alpha/transfer {"target":"bob@bravo","amount":2500000}   → 200 {…,"charge":0}
```

A transfer to another kernel's user carries `payment` on its transaction,
`"pending"` at once and `"announced"` within ten seconds; a local one carries
none. The sender's ledger books it to `sys`, and the recipient's kernel books
it from its own `sys`, both under a **new reason form**, `transfer <tx id>` —
the id being the sender's transaction:

```
alpha ledger (ana) → {"amount":2500000,"reason":"transfer 92a0462f-…","operator":"sys@alpha","from":"ana@alpha","to":"sys@alpha"}
bravo ledger (bob) → {"amount":2500000,"reason":"transfer 92a0462f-…","operator":"sys@bravo","from":"sys@bravo","to":"bob@bravo"}
```

A local transfer journals as before (`reason` = the transaction id, ana→vendor).

## 6. The payout address takes the password

```
PUT /v1/me/address                                                   → 404 (plain text)
PUT /v1/me/blockchain-address {blockchain_address, signature}        → 401 unauthenticated "invalid current password"
PUT /v1/me/blockchain-address {…, "current_password":"wrong"}        → 401 the same
PUT /v1/me/blockchain-address {…, "current_password":"password1"}    → 409 invalid_state "this world has no payment addresses"
```

The password is checked before the world: on `play` a missing password is 401,
not 409. Withdrawals are unchanged (`confirmed`, `tx_hash "manual:<id>"`,
replay 200, other terms 422 "was already made on other terms").

## 7. Run

- **`charge`** on success: `0` for a free call, the price for a local paid call
  (`2000000`), and across kernels the all-in quote (`1102500` for a 1000000
  action: obligation 1050000 + import fee 52500, the ticket not paying at
  that draw).
- **`external_key`**: a repeat with the same key and `{action, args}` answers
  the first run's reply verbatim (same `tx_id`, nothing moved); another action
  under the key → `422 "this external key was used for a different run"`.
- **A parked call** (bravo killed mid-call) → `504 timeout` with
  `meta {"pending_since":"…","process_id":"…"}` — **no `refund_eligible_at`**,
  contrary to `juicedocs/API.md` (Run). `POST /v1/processes/{id}/end` →
  `409 "a call on this process has been awaiting the peer's receipt since …; it settles when the peer answers"`.
  Bravo restarted: the call settled within seconds (failure `interrupted`,
  `refund 1102500`), the process closed.
- **Locked is not the sum of open sessions.** While parked, the process held
  `locked 1102500` and ana's `/v1/me` `locked 2102500`: the further 1000000 is
  the ticket face (`lottery`) staked from her own balance (P10).

## 8. Processes list what is open

`GET /v1/processes` answers open processes only (`[]` with every process
closed); `?all=1` adds the closed ones. `GET /v1/processes/e293` resolves a
prefix. Rows unchanged (`owner, available, locked, status, awaiting_receipt,
awaiting_receipt_since?`).

## 9. Unchanged, re-confirmed

`/health` gains `version` (`"dev"` on a source build); otherwise as on 9705b3b.
`/v1/admin/kernel`, `/kernel/deposits`, `/users[/{address}[/suspend|unsuspend|rename|deposit]]`
(every verb answers the row), `/peers[/{key|petname}[/suspend|unsuspend|rename]]`,
`POST /v1/transactions/{id}/rate`, `receipt-verification`, ratings, the grant
plan, `sys@alpha/lookup` rows, and the transaction shape (`ticket_id` on a
cross-kernel row). A deposit without `ref` → `422 "a deposit must name the payment it records (--ref)"`.
`?owner=` still filters after paging, so `?owner=vendor%40alpha&limit=1` answers
`[]` while the unpaged list answers two rows.

## 10. Contract defects observed

- `juicedocs/API.md` Run still describes `refund_eligible_at` and an automatic
  refund; the kernel serves neither (§7), as P7 and D19 state.
- `juicedocs/API.md` R6 ends a sentence mid-clause where the `?peer=` view was
  removed.
- `juicedocs/API.md` Tasks says a task addressed to a kernel names it "by its
  name alone"; the kernel refuses a bare name as `required_caller` and the
  operator is reached as `sys@<kernel>` (§3).
