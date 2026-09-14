# Kernel 25c07cd — live verification

What the launch-strategy kernel actually serves, measured on throwaway kernels
rather than read from the contract. Every claim here was produced by a request
whose reply is quoted. This is the evidence §10 of `requirements.md` cites.

Rig: `juice kernel serve NAME --addr 127.0.0.1:PORT`, world `play`, three
kernels — `alpha` (4171), `bravo` (4172, bootstrapped off alpha), and `charlie`
(4173, isolated, used only for the priced-native and unconfigured-model cases).
A first boot still refuses off a terminal, so each `config.json` pre-writes
`world`.

## 1. The world

`GET /health` on a play kernel:

```json
{"decimals":6,"handle":"alpha","network":"play","network_digest":"ef1fac03…",
 "public_key":"SvS8d96H_oOskeGRX2VJDEFz_A5zuA4dY_BbxROPvSc","rail_address":"",
 "status":"ok","symbol":"credits"}
```

**`play` is now decimals 6, symbol `credits`** — it was decimals 0 in v0.14.
Nothing in the UI needed changing for this: the wallet rendered `4.999867
credits` unaided, because 0.7.1 made the unit the world's. `rail_address` is
empty on a world whose money has no addresses, which is the served fact that
distinguishes a world money can leave from one it cannot.

## 2. The admin surface moved, and split in two

Every `/control/*` route answers 404. The replacements, each confirmed 200:

| Verb | Route |
|---|---|
| List users | `GET /v1/admin/users?limit=&offset=` |
| Show user | `GET /v1/admin/users/{handle}` |
| Suspend / unsuspend | `POST /v1/admin/users/{handle}/suspend`, `…/unsuspend` |
| Rename user | `POST /v1/admin/users/{handle}/rename` `{"new_name":…}` |
| Deposit | `POST /v1/admin/users/{handle}/deposit` `{ref,[amount],[reason]}` |
| Kernel identity | `GET /v1/admin/kernel` |
| Held + owed | `GET /v1/admin/kernel/deposits` |
| List peers | `GET /v1/admin/peers?all=&limit=&offset=` |
| Show peer | `GET /v1/admin/peers/{key\|petname}` |
| Inspect peer | `GET /v1/admin/peers/{key\|petname}/inspect` |
| Suspend / unsuspend / rename peer | `POST /v1/admin/peers/{key\|petname}/{suspend,unsuspend,rename}` |

The rename body field is still `new_name`; `new_handle`, `handle`, `name` and
`petname` were all refused. Suspend and unsuspend now answer with the row
(v0.14 answered 204).

**The namespaces refuse each other**, so the UI must know which it is addressing
before it dials:

```
POST /v1/admin/users/<peer key>/deposit  → 404 "… is not a user here"
POST /v1/admin/users/<peer key>/suspend  → 404 "… is not a user here"
GET  /v1/admin/peers/vendor              → 404 "vendor is not a peer here;
                                                 peers are named by petname or public key"
POST /v1/admin/peers/<key>/deposit       → 404 (no such route)
```

There is no route by which a peer is credited. `GET /v1/admin/kernel` gained
`lottery_max` (5000000 by default) beside `lottery`.

## 3. An obligation closes itself

`ana@alpha` ran `vendor@bravo/echo`, priced 120. On bravo:

```
t+0   owed: [{"id":"f9e5…","obligation":126,"amount":0,"tx_hash":"","status":"",
              "peer":"<alpha key>","created_at":…}]      exposure 126
t+5s  owed: []                                            exposure 0
                                                          vendor credited, sys earnings 24,
                                                          solvency gap 0
```

No operator act. On a world with no addresses the buyer's own reveal **is** the
payment, so the seller's kernel closes the debt itself. A row whose buyer has
not yet spoken carries **`status: ""` and `amount: 0`** — absent fields, not
zero values, and the UI must not render them as an empty badge and a zero
amount.

## 4. A native's price is operator configuration

`charlie` booted with `{"native":{"lookup":{"price":7}}}`. The boot was accepted,
and the price is served on the ordinary action row:

```
GET /v1/actions?ref=sys%2Flookup   → price 7
POST /v1/run {"action":"sys/lookup",…}  → 402   (the caller could not pay)
```

Default prices on an unconfigured kernel: `sys/lookup` 0, `sys/llm/decide` 0,
`sys/transfer` 0, `sys/tinygo/compile` 5.

This is decisive for the UI. `getBrowse` runs `sys/lookup` on a plain page load
and its comment asserts "price 0". On a kernel that prices it, every search
either charges the person without a confirm or fails the page with a 402. The
price is readable before the run, so the UI reads it.

## 5. `sys/llm/decide` — the kernel proposes, it never spends

`POST /v1/run {"action":"sys/llm/decide","args":{messages,actions}}`, against a
local ollama (`qwen3.5`), ~3.7 s:

```
"what time is it right now?"          actions [sys/time, sys/random, sys/web]
  → {"action":"sys/time","args":{}}

"fetch the front page of example.com" actions [sys/time, sys/web, sys/random]
  → {"action":"sys/web","args":{"url":"example.com"}}
```

`message` is optional and was absent on both. Nothing executed: the reply is a
proposal, and the transaction it wrote is the free `decide` call itself.

Candidate handling, both confirmed:

- a **dead kernel-qualified** candidate among live ones is discarded, and the
  live one is still selected;
- a **bare unknown** ref refuses the whole call — `404 "action nope/missing not
  found"`. So the UI may pass only refs the kernel itself served.

With no model reachable the call fails as an ordinary outcome — `500
execution_failed`, `"model did not select an action"` — which the existing
degrade-honestly path renders as the kernel's own reason.

## 6. A peer-held step is now completable over HTTP

This was the ROADMAP's blocker, recorded as a 404 on v0.14.0. It is gone.

`vendor@bravo` parked a step for alpha's kernel
(`sys/message {to: <alpha key>, message: …}` — `to` takes a *local* identifier,
so the peer account must exist, which alpha's first call provisions).

Reading it, two ways:

```
GET /v1/admin/peers/<bravo key>/inspect
  → "steps":[{"id":…,"partial_args":{"message":"second ping"},
              "allowed_input":{"type":"object"},"price":0,"created_at":…}],
    "steps_truncated":false

GET /v1/steps?peer=<bravo key>
  as sys  → {"steps":[ …the same row… ]}
  as ana  → {"steps":[]}          (the step's required caller is the KERNEL, not ana)
```

The step row served here is thinner than an ordinary step: `id`, `partial_args`,
`allowed_input`, `price`, `created_at`. No `action`, no `created_by`.

Completing it:

```
POST /v1/steps/{id}/complete?peer=<key>          → 404
POST /v1/steps/{id}/complete {"args":{},"peer":"<key>"}  → 200, and the reply is a receipt
GET  /v1/steps?peer=<key>                        → {"steps":[]}   (gone)
```

The peer is named **in the body**, not the query. It is a superuser act: the
request is signed with this kernel's own federation identity, so it acts as the
whole kernel.

## 7. The client records hold logins, not contexts

`juice kernel add http://127.0.0.1:4171 alpha` then `juice auth login ana@alpha`
writes:

```json
{"current":"ana@alpha",
 "kernels":{"alpha":{"endpoint":"http://127.0.0.1:4171","public_key":"SvS8…",
   "world_digest":"ef1f…","network":"play","decimals":6,"symbol":"credits"}}}
```

and `client/credentials/ana@alpha.json` = `{token, refresh_token, principal_id}`.
There is no `contexts` map. A UI configured with `JUICE_CONTEXT` refuses to
start: `context "alpha" is not in …/client/config.json`.

## 8. A chain world, and what only one can show

Rig: `anvil --chain-id 31337 --slots-in-an-epoch 1`, juice-rail's compiled
`MockUSDT0` and `MockRouter`, a generated world file naming them, one kernel
(`delta` :4174) serving it with `rail_rpc`.

```json
{"decimals":6,"handle":"delta","network":"anvil","network_digest":"1f6a1102…",
 "rail_address":"0x1dbfeae017bcda47333c56563d1572f79a369e0a","status":"ok","symbol":"USDC"}
```

`symbol` comes from the **world file**, not the token contract: the deployed
mock answers `symbol() = "USDT0"` while the world names `USDC`, and `/health`
serves the world's. A world file without a `symbol` serves `""`, and adding one
does **not** change `network_digest` — it is an operational field, not part of
the world's defining `{name, chain_id, token}`.

**Withdrawals page.** Four rows, paid to a registered address:

```
GET /v1/withdrawals                 → 4 rows
GET /v1/withdrawals?limit=2         → 2
GET /v1/withdrawals?limit=2&offset=2 → 2   (the third and fourth)
GET /v1/withdrawals?limit=3&offset=3 → 1   (the fourth)
```

So an unpaged read truncates at the kernel's default of 50, and the client must
page to exhaustion.

**The rail halts on its own.** A payout with no native currency in the vault
books `status:"blocked"` with a reason naming the shortfall, and every later
money verb answers `503 rail_stopped` until the vault is topped up:

```
"native currency too low, top up: holding 0.00, a refill costs 0.001500018504
 — send native currency to 0x1DBFEae017bcdA47333C56563d1572F79A369e0a"
```

`GET /v1/admin/kernel` carries the same text under `stop`, which is what the
network page's banner states.

## 9. `wasm_artifact`, end to end

`sys/tinygo/compile` wraps the submitted source: it supplies `main` and expects
a `Handle` function, so source declaring its own `main` fails with
`undefined: Handle` and `main redeclared`. Source that declares only

```go
func Handle(in map[string]any) (map[string]any, error) { … }
```

compiles: `{status:"success", artifact:<1 389 036 chars base64>, artifact_hash,
diagnostics:[]}`, charged 5.

`POST /v1/actions {kind:"wasm", wasm_artifact:<that>}` creates the action, the
detail read carries `artifact_hash` and the artifact back, `enable` then
succeeds, and the action runs. Without the field, `enable` refuses with
`409 "cannot activate action with no source"` — which is what the UI produced
before, since it had nowhere to put a compiled module.

`POST /v1/actions/import` accepts `auth` for the whole application: an import
with `{"scheme":"bearer","secrets":{"token":…}}` lands
`ana/pets/index` carrying `auth_scheme: bearer`.
