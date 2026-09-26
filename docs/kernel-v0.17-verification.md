# Kernel 9705b3b — live verification

What the kernel at `../juice` 9705b3b serves, measured on throwaway kernels
rather than read from the contract. Every claim here was produced by a request
whose reply is quoted. This is the evidence §10 of `requirements.md` cites for
0.10.0. The previous record is `docs/kernel-v0.16-verification.md`.

Rig: the kernel built from source (`go build ./cmd/juice`), world `play`, two
kernels under separate `JUICE_HOME`s — `alpha` (API 4171, peer transport pinned
to `/ip4/127.0.0.1/tcp/39921`) and `bravo` (API 4172, its `worlds/play.json`
naming alpha's transport address under `seeds`). The UI ran against alpha from
its own temporary `JUICE_HOME`.

## 1. Every name is an address

The kernel's own name is `/health.handle`:

```json
{"blockchain_address":"","decimals":6,"handle":"alpha","network":"play",
 "network_fingerprint":"baed18ae…","public_key":"9VJjjwsu…","status":"ok",
 "symbol":"fUSD","token":"","fed_addrs":[…]}
```

`network_digest` and `rail_address` are now `network_fingerprint` and
`blockchain_address`; the client record's `world_digest` is
`world_fingerprint`. `juice kernel add URL` takes no name and records the kernel
under the one it calls itself.

A bare name is refused at every input, with one message:

```
POST /v1/run {"action":"sys/time"}            → 422 "\"sys/time\" is not an address: name the user
                                                 as handle@kernel, an action as handle@kernel/name"
GET  /v1/actions?ref=sys%2Ftime               → the same
GET  /v1/actions?owner=ana                    → 422 "owner must be handle@kernel"
POST /v1/actions/enable {"target":"ana/echo"} → the same refusal
POST /v1/transfers {"recipient":"sys"}        → the same refusal
GET  /v1/grants/plan?selector=sys             → the same refusal
POST /v1/auth/authorize {"handle":"ana"}      → 401, the same message
GET  /v1/admin/users/ana                      → 422, the same message
POST /v1/run sys@alpha/message {"to":"ana"}   → execution_failed "to \"ana\" not found"
```

The addressed forms work: `sys@alpha/time` runs, `?ref=sys%40alpha%2Ftime` and
`?owner=ana%40alpha` resolve (a percent-encoded `@` is accepted in a query),
`POST /v1/users {"handle":"carl@alpha"}` creates `carl@alpha`, and a raw action
id still runs. The admin path takes the literal address and refuses the encoded
one:

```
GET /v1/admin/users/ana@alpha   → {"address":"ana@alpha","available":10000000,…}
GET /v1/admin/users/ana%40alpha → 422 "\"ana%40alpha\" is not an address"
POST /v1/admin/users/ana@alpha/rename {"new_name":"anna@alpha"} → {"address":"anna@alpha",…}
POST /v1/admin/users/anna@alpha/rename {"new_name":"ana"}       → 422, not an address
```

## 2. Party fields hold addresses and are named for their role

```
GET /v1/me           → {"address":"ana@alpha","available":…,"connectors":[],…}
GET /v1/admin/users  → [{"address":"ana@alpha",…},{"address":"sys@alpha",…}]
GET /v1/transactions → [{…,"owner":"ana@alpha","caller":"ana@alpha","target":"sys@alpha",
                         "action":"sys@alpha/time"}]          (no action_name)
GET /v1/steps        → [{…,"action":"sys@alpha/sink","created_by":"sys@alpha/message",
                         "owner":"ana@alpha","required_caller":"ana@alpha"}]
GET /v1/processes    → [{…,"owner":"ana@alpha",…}]
ledger entry         → {"amount":10000000,"operator":"sys@alpha","from":"sys@alpha","to":"ana@alpha"}
withdrawal           → {…,"status":"confirmed","tx_hash":"manual:…","party":"ana@alpha"}
```

An action row serves `action` (its address) and `name`, and **no owner field**:

```
{"id":"99612043…","name":"echo","kind":"http",…,"action":"ana@alpha/echo",…}
```

A peer kernel as a party is its petname alone; a remote action is addressed as
the kernel knows the peer (`seller@k-4lnqDq15/greet` after first use,
`seller@<key>/greet` from discovery).

## 3. Through the UI

Against alpha, as `ana`, `sys` and `vendor`:

- sign-in with the handle `ana` authorizes `ana@alpha`; the sidebar shows
  `ana@alpha`; `sys` (signed in as `sys`) gets the admin and network groups,
  `ana` does not;
- browse and lookup cards link local owners to `/users/vendor@alpha`,
  `/users/sys@alpha`; a discovered action renders
  `seller@4zVovUrj…/greet` with the truncated key and resolves on open to
  `seller@bravo/greet`, whose owner is unlinked; running it succeeds;
- a local run of `vendor@alpha/echo` succeeds; the transactions page names each
  row's action from the transaction itself (`vendor@alpha / echo`,
  `seller@bravo / greet`) with no per-row read;
- the wallet activity reads `to vendor@alpha`, `to sys@alpha`,
  `from sys@alpha`;
- `sys@alpha/message` run by `sys` to `ana@alpha` lands in ana's inbox with
  `Required caller ana@alpha` and completes;
- the admin roster links `/admin/users/ana@alpha`; suspend, unsuspend, rename
  to `anna@alpha` and back, and a deposit by address all succeed;
- the agents page for the home `research` reads the account
  `research@alpha` and links `/users/research@alpha`;
- the chat service account provisions itself as `chat@alpha` and registers
  `chat@alpha/*`; a chat store holding bare rows (`creator ana`, members `ana`,
  `vendor`, `vendor@alpha`, sender `ana`) is qualified once on the next start to
  `ana@alpha`, `vendor@alpha` (the duplicate member collapsed).

## 4. Chat registration is blocked by the kernel

Connecting to chat runs `chat@alpha/register`, whose handler mints the
verification step off chat's outbox seed trace. The kernel refuses it:

```
chat register: verification step mint failed … status=409
  msg="create step: the call has already settled"
```

The seed trace settles when the seed call returns, and the kernel now refuses
to park a step on a settled trace (the conformance line "a settled trace, or
one in a closed process, funds no subcall and parks no step at any price").
This is independent of the naming change and blocks registration, the rooms
gate, and doorbells on this kernel until chat mints from a trace that is still
live.
