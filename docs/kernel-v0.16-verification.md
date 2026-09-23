# Kernel d59742b — live verification

What the kernel at `../juice` d59742b serves, measured on throwaway kernels
rather than read from the contract. Every claim here was produced by a request
whose reply is quoted. This is the evidence §10 of `requirements.md` cites for
0.9.0. The previous record is `docs/kernel-v0.15-verification.md`.

Rig: the kernel built from source (`go build ./cmd/juice`; the committed
`../juice/juice` binary predates the world grammar), world `play`, two kernels
under separate `JUICE_HOME`s — `alpha` (API 4171, peer transport pinned to
`/ip4/127.0.0.1/tcp/39921`) and `bravo` (API 4172), whose
`worlds/play.json` names alpha's transport address under `seeds`. A config file
written in advance answers first boot:

```json
{"kernel_handle":"alpha","listen_addr":"127.0.0.1:4171","allow_local_sources":true}
```

The UI ran against alpha from its own temporary `JUICE_HOME`.

## 1. The world and the home

`juice kernel serve play` serves `$JUICE_HOME/kernels/play/`. The first serve
writes the shipped worlds into `$JUICE_HOME/worlds/` (`play.json`,
`arbitrum-one.json`, `arbitrum-sepolia.json`) and registers the kernel in
`client/config.json`. `play.json`:

```json
{"rail":"manual","decimals":6,"symbol":"fUSDT",
 "description":"no real money: you credit accounts yourself and keep the records","seeds":[]}
```

`GET /health` gains `token` and `fed_addrs`:

```json
{"decimals":6,"fed_addrs":["/ip4/127.0.0.1/tcp/39921/p2p/12D3Koo…", …],"handle":"alpha",
 "network":"play","network_digest":"baed18ae…","public_key":"5o-od9l8…",
 "rail_address":"","status":"ok","symbol":"fUSDT","token":""}
```

`play`'s symbol is now `fUSDT`. `GET /v1/admin/kernel` gains `addrs`.

## 2. Stats are gone; evidence is read with the action

```
GET /v1/stats/<id>   → 404 page not found
```

A detail read carries the evidence block:

```json
"evidence":{
  "local_experience":{"action_id":"3a77…","uses":0,"successes":0,"failures":0,
    "rating_count":0,"latency_estimate":0,"rating_estimate":0,"last_used_at":"…"},
  "provider_reported":null,"observed_by_others":[],"retained_cap":200}
```

`local_experience` has the old stats fields. Every `sys/lookup` row carries the
same block; a discovered, unresolved hit carries `"local_experience":null`.
**A `GET /v1/actions` list row carries no evidence**, the owner-filtered list
included:

```
GET /v1/actions?owner=vendor → [("fail", evidence: no), ("echo", evidence: no)]
```

After one trade each way, the other kernels' reports appear. On bravo's proxy
of `vendor/echo`, the provider's own report:

```json
"provider_reported":{"issuer_public_key":"5o-od9l8…","subject_action_id":"3a77…",
  "uses":1,"successes":1,"failures":0,"corroborated_uses":0,"avg_latency_ms":0,
  "rating_count":0,"rating_mean":0,"unverified_ratings":0,"contradictions":0,
  "equivocations":0,"since":"…","until":"…"}
```

and on bravo's own `seller/greet`, alpha's account of the trade under
`observed_by_others`, with a rating alpha's buyer gave: `unverified_ratings: 1`.
`GET /v1/actions/{id}/ratings` on bravo lists it with `"source":"peer"`.

## 3. The ledger journals settlement

A local paid call (`ana` runs `vendor/echo` at 1.50, `fee_bps` 2000):

```
ana:    {"amount":1200000,"reason":"<tx>","from_handle":"ana","to_handle":"vendor"}
        {"amount":300000, "reason":"<tx>","from_handle":"ana","to_handle":"sys"}
vendor: {"amount":1200000,"reason":"<tx>","from_handle":"ana","to_handle":"vendor"}
```

A cross-kernel buy (`ana@alpha` runs `seller@bravo/greet`) writes the import
fee against the transaction and the obligation against its ticket:

```
ana:    {"amount":105000, "reason":"<tx>","from_handle":"ana","to_handle":"sys"}
        {"amount":2100000,"reason":"obligation e2c28a67-…","operator_handle":"sys",
         "from_handle":"ana","to_handle":"sys"}
seller: {"amount":2100000,"reason":"obligation e2c28a67-…","from_handle":"sys","to_handle":"seller"}
```

A deposit now names `sys` as its sender:

```
{"amount":100000000,"reason":"","operator_handle":"sys","from_handle":"sys","to_handle":"ana"}
```

A withdrawal is `from_handle: ana, to_handle: sys`. Only `sys`'s own ledger
holds entries with an absent side (the rail crossings).

## 4. What a run drew

```
POST /v1/run vendor/echo → {…,"process_id":"…","charge":1500000}
POST /v1/run sys/time    → {…,"charge":0}
POST /v1/run vendor/fail → 500 {"code":"execution_failed","error":"upstream returned status 500",
                                "meta":{"charge":"0","tx_id":"f2a4…"}}
```

An HTTP action that composes `vendor/echo` through its capability and then
answers 500 draws the sub-work:

```
POST /v1/call (capability) → {"result":{"echo":"sub"},…,"charge":1500000}
POST /v1/run vendor/composite → execution_failed, meta.charge "1500000"
```

## 5. A parked call has no refund date and cannot be ended

With bravo stopped (`SIGSTOP`), `ana` runs `seller@bravo/greet`; after ~39 s:

```json
{"code":"timeout","error":"remote receipt pending; … funds remain reserved on process 7ed8… and the call retries automatically",
 "meta":{"pending_since":"2026-09-23T00:47:45Z","process_id":"7ed8…"}}
```

There is no `refund_eligible_at`. The process reads
`"locked":2205000,"awaiting_receipt":true`, and ending it is refused:

```
POST /v1/processes/7ed8…/end → 409 {"code":"invalid_state","error":"a call on this process has been
  awaiting the peer's receipt since 2026-09-23T00:47:45Z; it settles when the peer answers",
  "meta":{"pending_since":"2026-09-23T00:47:45Z"}}
```

On `SIGCONT` the retry loop settled it within one interval: the process closed
and the transaction reads `success`.

## 6. A manual world pays out

```
POST /v1/withdrawals {"id":"1111…","amount":1000000}
  → {"id":"1111…","kind":"payout","amount":1000000,"credit":1000000,"status":"confirmed",
     "tx_hash":"manual:1111…","finalized_at":"…","party_handle":"ana"}
PUT /v1/me/address → 409 {"code":"invalid_state","error":"this world has no payment addresses"}
```

## 7. Peer credit, and the names a cross-kernel row carries

A foreign call is funded by the seller's own provider. With `vendor` at zero
balance, bravo's call to `vendor@alpha/echo` is refused as `peer_unfunded`, with
a failure transaction:

```json
{"code":"peer_unfunded","error":"this kernel's credit with peer alpha is exhausted; the operator must top up",
 "meta":{"charge":"0","peer":"alpha","tx_id":"5f59…"}}
```

After `vendor` was funded the same call succeeded. A cross-kernel transaction
names the peer by the petname bound on first use (`target_handle":"k-4lnqDq15"`),
and the seller's row names the buying kernel by its petname in `caller_handle`.
`GET /v1/admin/peers/{key}` returns
`{about, created_at, last_seen, nickname, petname, public_key, rail_address, suspended_at}`.

## 8. Through the UI

Against alpha, as `ana`, `vendor` and `sys`:

- wallet activity lists ledger rows only: `to vendor 451333e4… −1.20 fUSDT`,
  `to sys obligation 61303c9a-… −2.10 fUSDT`, `from sys — +100.00 fUSDT`, each
  transaction id linking to its receipt;
- `vendor/echo`'s page shows bravo's report under "issued by"; the proxy's page
  shows "reported by the provider"; neither is folded into the strip;
- lookup and catalog cards render the strip from the evidence block; the
  workshop table reads uses, success and rating from it;
- `vendor/fail` renders "Failed — fully refunded"; `vendor/composite` renders
  "Failed — 1.50 fUSDT charged" with the receipt linked;
- the parked run renders "Result pending — 2.205 fUSDT reserved" with no date,
  and neither the wallet, the sessions list nor the process page offers
  force-end while the receipt is awaited;
- a withdrawal on `play` is offered, lands `confirmed`, and appears in both the
  withdrawals table and the activity.
