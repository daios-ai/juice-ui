# Kernel v0.14 live verification record — 2026-09-09

Base pass on v0.14.0 (`6f347c0`); re-verified on v0.14.1 (`660e969`) after the
engineer's fixes. Every "FIXED" note below was re-run, not assumed.

Harness: two throwaway kernels on world `play`, A=`alpha` :4141 (buyer, seeded with B), B=`bravo` :4142 (provider, bootstrap). Config: fee_bps 2000, remote_bps 500, import_bps 500, lottery 10, credit_limit 100000, retry 5s, discovery 5s.

## 1. Health, world, instance layout
- `GET /health` → `{decimals:0, handle, network:"play", network_digest, public_key, rail_address:"", status:"ok"}`. `rail_address` is present as `""` on play.
- Instance home `$JUICE_HOME/kernels/default/` holds `cache/ config.json juice.db(+wal/shm) serve.lock`. Config written back with all defaults incl. `credentials_key`, `native.*` prices (tinygo 5, others 0), `peer_retention_days 90`.
- `fed_listen_addrs: []` → OS-assigned fed ports; no need to hold 31313.

## 2. Auth / users / client contexts
- PKCE authorize without redirect_uri → 200 `{redirect:"?code=…"}`; token → `{access_token, refresh_token}`; refresh → same pair. Unchanged.
- `POST /v1/users` 201 `{available, description, handle, id, locked}`. Duplicate → 422 `invalid_input` "create account: already exists". Sigil `@carl` → 422 "handle must not contain @ or /". Login as `@alice` → 401 "invalid credentials".
- `GET /v1/me` → `{available, connections:[], connectors:[], description, handle, id, locked}`. **DEVIATION: no `rail_address` field on play** (API.md lists it; absent, not "").
- CLI `juice use NAME --endpoint URL` writes `~/.juice/client/config.json` `{current, kernels:{NAME:{endpoint, public_key, world_digest, network}}, contexts:{NAME:{kernel, handle, principal_id}}}` and `client/credentials/NAME.json` (0600) `{token, refresh_token}`. NOTE: kernel record key is `world_digest` (ecosystem-standard.md says the same; no `decimals` written on play).
- Re-pointing `use alpha --endpoint <other kernel>` succeeds silently and STRANDS the login (context shows handle `—`); `use` list format: `* NAME HANDLE ENDPOINT NETWORK`.
- `juice user deposit` on play: "Money on the play network has no addresses to send to. The operator of this kernel records payments here; there is nothing to send from your side." (`--json` prints the same prose, not JSON.)
- `juice user address` bare → "No address registered."; `user withdraw` bare → `[]`.

## 3. Admin identity (play)
`GET /control/identity` → `{handle, public_key, about, addrs[], network, rail_address:"", finalized:null, sys:{earnings,pending_payouts,held_deposits,refill_locks}, solvency:{liabilities,vault,gap}, custody:{checked:false,difference,ok:false}, stop:null, lottery, credit_limit, exposure, fee_bps, remote_bps, import_bps}`.
**DEVIATION: no `network_digest` in identity** (API.md lists it). CLI text labels: Operator earned/paying-out/unattributed/fee-locks; Solvency user-credits/money-in/difference; Credit owed-to-us/limit; Rates fee/serving/import bps + ticket.

## 4. Money on the manual rail (play)
- `POST /control/deposit {handle, amount, ref}` → 200 ledger entry `{amount, reason, external_key:"attr:rail:ref:<ref>", created_at, operator_handle, from_handle:"sys", to_handle}`. **No `id` on ledger entries.** Replay same ref+amount → same entry 200. Same ref, other amount → 422 "payment rail:ref:<ref> was already recorded on other terms". No ref → 422 "a deposit must name the payment it records (--ref)". Old `external_key` field ignored (422). Deposit naming a peer key/petname with a plain ref → 422 "<key> is a peer: name the obligation its payment closes, not the peer itself". Non-superuser → 403 `unauthorized` "superuser required".
- Each deposit is TWO ledger rows in sys's ledger: `rail:ref:<ref>` (null→sys, crossing in) then `attr:rail:ref:<ref>` (sys→user). The user sees only the attr row.
- `GET /control/deposits` → `{deposits:[], owed:[]}`.
- `PUT /v1/me/address` on play → 409 `invalid_state` "this world has no payment addresses".
- `POST /v1/withdrawals {id, amount}`: id must be a UUID the caller mints (422 "withdrawal id must be a uuid the caller mints"). On play it confirms immediately: `{id, kind:"payout", amount, credit, status:"confirmed", tx_hash:"manual:<id>", created_at, finalized_at, party_handle}`. Over balance → 402 `insufficient_funds`. Two ledger rows on the owner: `res:<id>` (owner→sys) then `rail:manual:<id>` (sys→null). NOTE: docs say 409 when destination not registered; on play (no addresses) the withdrawal simply completes.
- `POST /v1/transfers {recipient, amount, reason, external_key}` → 200 `{amount, reason, external_key, created_at, operator_handle, from_handle, to_handle}`; replay by external_key → same. Recipient = a peer key → 422 "recipient must be a local user account".
- `sys/transfer` run: transaction `gross/net/fee/refund` all 0 (price 0), `target_handle:"sys"`; PLUS a ledger entry `{amount, reason:<tx id>, created_at, operator_handle:<caller>, from_handle, to_handle}` (no external_key) visible to both parties. Beneficiary has no transaction row. Self-transfer via the native is accepted (200); D18's admission rule does not list self, so this is within contract (D4's self-rejection is the direct `/v1/transfers` op).
- Over-balance run → 402 `insufficient_funds` "user has N credits, call costs M" (message carries both numbers).

## 5. Actions
- Natives (12, all `visibility:"local"`, price 0 except `sys/tinygo/compile` 5): tinygo/compile, web, transfer (effect:"transfer"), random, message, sink, time, llm/decide, llm/json, llm/embed, llm/chat, lookup. `sys/user-lookup` is GONE. Anonymous `GET /v1/actions` → `[]`; anonymous `sys/lookup` → 401.
- Action read shape: `{id, owner_handle, name, kind, active, visibility, price, description, input_schema, output_schema, [effect], created_at, updated_at, action, [http:{method,url,[params]}], requires_grant, [auth_scheme], quote_hash}`. `effect` absent unless set. No `source` on http reads. WASM detail read: `wasm_artifact` (base64, ~1.4MB!) + `artifact_hash`, no `source`; WASM list row has neither. Create response echoes `wasm_artifact` back.
- `GET /v1/actions?ref=<ref>` → array of 0 or 1 rows (a miss is `[]` 200, never 404). Index fallback verified: `?ref=vendor/echo` → `vendor/echo/index`. `?ref` + `owner`/`name` → 422 "ref cannot be combined with owner or name". Kernel-qualified `?ref=vendor@bravo/pets` RESOLVES OVER THE WIRE and caches the proxy (cost: one resolve, no spend); anonymous → 401 "a kernel-qualified reference requires authentication"; unknown remote → `[]`. `GET /v1/actions/<ref>` → 404.
- Mutations: `POST /v1/actions/enable|disable {target}`, `PUT /v1/actions {target,…}`, `DELETE /v1/actions?target=` → array of written rows. `target` = id or owner/path (subtree). Description/schema/source on a multi-row path → 422 "description, schema, and source belong to one action: name it by id or by its exact path". No match → 404 "no action matches <target>". Old `POST /v1/actions/{id}/enable` → 404 plain text "404 page not found" (NOT a JSON envelope).
- Price change deactivates (`active:false`) — re-enable needed. Description change keeps active, moves `quote_hash`.
- Remote proxy row: `{kind:"remote_proxy", visibility:"local", owner_handle:<petname>, name:"vendor/echo/index", action:"vendor@bravo/echo/index", source:"vendor/echo/index", artifact_hash, remote_action_id, remote_owner_id, remote_bps, base_price, price (all-in), quote_hash}`. enable/disable/visibility/delete on it → 409 `invalid_state` "remote proxy is kernel-managed; use admin suspend to block a peer" (DELETE now refused too — fixed vs v0.12.3).
- Proxies are listed to any local user under `GET /v1/actions` (they are `local`) and via `?all=1`; `?owner=vendor@bravo` filter returns nothing (owner_handle is the petname).
- OpenAPI import `POST /v1/actions/import {name, spec_url}` → 200 **PascalCase** `{Created:[…], Unchanged, Updated, Deactivated, Rejected:[{Key, Reason}]}` — rows carry `owner_user_id` and a JSON-encoded `source` string (R1/R2 violation). Re-import `{name}` only → Unchanged rows. Spec 404 → 500 `execution_failed` "spec server returned 404". Operation `index` becomes the root (`vendor/pets` runs it). Path/query params bind; upstream sees `/pets/42?verbose=true`. `x-juice-price` applied (7), undeclared → 0.
- Delegated bearer: create with `auth:{scheme:"delegated_bearer",config:{header,template}}` → read carries `auth_scheme`, `requires_grant:true`. Run without grant → 403 `grant_required` meta `{action}`. `GET /v1/grants/plan?selector=` → `{groups:[{provider_key:"bearer:<host>", provider:"<host>", scheme, scopes:[], destinations:[host], connected, covered, actions:[{action_id, action, granted}]}], skipped_loginless, skipped_uncallable}`. `POST /v1/grants {selector, token}` → `{actions:[…], created_at, provider:"" (empty!), status:"granted"}`. Upstream receives the token in the configured header. `/v1/me.connectors[{directory, connections[], actions[{action, provider_key, created_at}]}]` and `connections[{provider, actions:<count>, unused, provider_key, created_at}]`. `DELETE /v1/grants?selector=` → `{revoked:[…]}`; `?account=<provider_key>` → `{connection:"<host>", revoked:null}`.
- OAuth delegated: plan `provider_key:"oauth:<token_url>|<client_id>|<host>"`, scopes from config. `grants/start` `provider` MUST be the `provider_key` (the display `provider` → 404 "no connectable actions for that provider in the selector") → `{state, authorize_url}` (PKCE S256, access_type=offline). `flow:"device"` with no device_auth_url → 409 "OAuth endpoint not configured".
- tinygo compile: `{status:"success", artifact:<base64 1.39MB>, artifact_hash, diagnostics:[]}`, charged 5. Create wasm via `wasm_artifact` needs a file body (too long for argv).
- HTTP dispatch headers: `X-Juice-Callback: http://127.0.0.1:<port>` and `X-Juice-Capability: <trace_id>.<sig>`. Composition verified: upstream `POST <callback>/v1/call {action,args}` with the capability header → `{result, tx_id, trace_id, receipt_id}`; child tx `owner=carol caller=vendor target=sys`, `parent_trace_id` = the http action's trace. Works across kernels too.

## 6. Run outcomes (local, on B)
- success: `{result, tx_id, trace_id, receipt_id, process_id}`; tx `gross 100 net 80 fee 20` (fee_bps 2000).
- input schema: 422 `schema_violation` "field #.text: expected string, got float64". Output schema: 422 `schema_violation` "field #.text: required field missing" — a FAILURE tx is recorded (gross 30, refund 30, reason schema_violation).
- upstream 500: 500 `execution_failed` "upstream returned status 500"; tx refund = gross, reason `execution_failed`.
- quote pin: 409 `terms_changed` "the action's terms changed; it now costs 100" meta `{price:"100", quote_hash:"…"}` (strings). Pinned run against a deactivated action → 409 `invalid_state` "action is inactive".
- missing `args` → 422 "args is required"; sigil ref → 422 "action ref must be owner[@kernel]/name"; unknown → 404 "action <ref> not found".
- `sys/web` private IP → 500 `execution_failed` "upstream request failed".

## 7. Transactions, ratings, receipts
- tx read: `{id, process_id, trace_id, parent_trace_id, action_id, action_name, args, result, status, gross, net, fee, refund, reason, started_at, ended_at, rating, owner_handle, caller_handle, target_handle}` + on remote: `remote_action_id, remote_receipt_hash, remote_receipt_json (string), remote_signer_key, ticket_id`. `ticket_id` is on the BUYER's tx only; the seller's tx has `ticket_id: null` (docs say both). Seller's tx `caller_handle` = the buyer kernel's raw PUBLIC KEY, `target_handle` = owner.
- rate → full signed Rating row `{id, rated_tx_id, rated_receipt_id, rater_user_id, rating, note, rated_receipt_hash, created_at, signature}`. Again → 422 "this transaction is already rated"; non-buyer → 403 "only the direct buyer may rate a transaction"; value 2 → 422 "rating must be 0 or 1". Remote-proxy tx is rateable by the local buyer.
- `GET /v1/actions/{id}/ratings` → `[{value, note, created_at, source:"local"}]`, anonymous OK.
- `GET /v1/stats/{id}` → `{action_id, uses, successes, failures, rating_count, latency_estimate, rating_estimate, last_used_at}`; a price change RESET uses to 0 while rating_count survived; `last_used_at` zero time `0001-01-01T00:00:00Z`.
- receipt-verification local: `{transaction_id, valid, checks:{action_id,args_hash,charge,reply_hash,settlement,signature,status}, receipt:{…}}`; failure receipt omits `reply_hash` check. Remote: adds `remote_kernel_handle, remote_kernel_public_key`, checks `charge_ceiling, draw, premium, receipt_hash, refund_conservation, settlement_arith`; receipt carries `premium, nonce`. Any party may verify.
- List paging: `limit=201` → 200 (clamped silently); no pagination headers at all, only `X-Request-Id`.

## 8. Steps
- Step row: `{id, parent_trace_id, action_id, partial_args, price, status, created_at, action, created_by, owner_handle, required_caller_handle, allowed_input, [waiting_on_peer], [required_caller_remote_id], [tx_id]}`. sys/message → `allowed_input {"type":"object"}`.
- Complete: wrong caller 403 "only the step's required caller may complete it"; no `args` 422; done → `{result, tx_id, trace_id, receipt_id, step_id}` (receipt_id extra); again → 409 `invalid_state` "step is not waiting".
- Force-end: non-owner 403 "only the process owner may end it"; owner → 204; waiting step → `status:"cancelled"` (no cancellation reason field); end again → 409 "process is already closed".
- Steps to a peer: `sys/message to:<key>` → step `required_caller_handle:"bravo"` (petname), `waiting_on_peer:true`. (A `to:"bravo"` in the same batch resolved to a LOCAL USER named bravo created minutes earlier for the ambiguity test — harness artefact, not a kernel inconsistency.) `to:"vendor@bravo"` → `required_caller_remote_id:<uuid>`, **`required_caller_handle:"<uuid>@bravo"` (user id leaks; not the handle)**, `waiting_on_peer:true`.
- `GET /v1/steps?peer=<key>` → `{steps:[{id, [required_caller:<remote uuid>], partial_args, allowed_input, price, created_at}]}` — **no `truncated` key when false**; with a filter → 422 "status cannot be combined with peer…". sys sees all steps the peer holds for this kernel; a user sees only those addressed to them. Same list appears under `admin inspect … steps` + `steps_truncated`.
- Completion of a peer-held step is CLI-only (`step complete <id> --peer <key>`) → `{receipt, result, step_id, trace_id, tx_id}`; replay returns the same stored result; wrong scope → "attested user is not the step's required caller". There is NO HTTP route for it (`?peer=` on complete → 404 step not found). On A the completion tx has `caller_handle:"bravo"` (petname), target sys.

## 9. Federation call (A buys from B)
- Pricing verified: base 120 → sr 126 (+5%) → q 133 (+5% import). Buyer tx gross 133 net 126 fee 7. Seller tx gross 120 net 96 fee 24 (its own fee_bps); receipt premium 6.
- Petname auto-bound on first use (`bravo` from nickname); lookup renders `vendor@bravo/echo/index`.
- Manual rail settlement is AUTOMATIC on the buyer: A's sys ledger shows `res:<ticket>` (alice→sys 126, reason "obligation <ticket>") then `rail:manual:<ticket>` (sys→null). Alice's own ledger shows the `res:` row with reason "obligation <id>". B `/control/deposits.owed` = `[{id:<ticket>, obligation:126, amount:126, tx_hash:"manual:<ticket>", status:"announced", created_at, peer:<Akey>}]`; B identity `exposure:126`.
- Closing: `POST /control/deposit {handle:<Akey>, ref:<ticket>}` (amount optional) → ledger entry sys→vendor 126, `external_key:"attr:rail:ref:manual:<ticket>"`; owed → [], exposure → 0. **KERNEL BUG: replaying that deposit (same or different amount) PANICS the handler — HTTP 500 with EMPTY body, log "panic: runtime error: invalid memory address or nil pointer dereference".** Unknown ref on a peer → 422.
- Roster `GET /control/peers` → `[{public_key, [petname], nickname, has_account, actions, last_seen, [last_contact_failed_at], [suspended_at]}]`; `null` (not `[]`) when empty; NO balances, NO peer_credit. CLI columns: PETNAME NICKNAME TRADED LAST SEEN LAST FAILED ACTIONS PUBLIC KEY.
- `admin show <key|petname>` → `{about, created_at, id, last_seen, nickname, petname, public_key, rail_address, suspended_at}`.
- inspect → `{about, account:{suspended} | null, actions:[{action_id, name, description, input_schema, output_schema, price (all-in), indicative:true}], evidence:[{issuer_public_key, subject_action_id, uses, successes, failures, corroborated_uses, avg_latency_ms, rating_count, rating_mean, unverified_ratings}], nickname, online, petname, public_key, reachability:{path, rtt_millis, protocols, [error]}, source:"live"|"none", steps:[…], steps_truncated}`. Unknown key → usable page, `source:"none"`, `reachability.error`.
- Rename: `POST /control/users/{target}/rename {new_name}` → `{handle}` or `{petname, public_key}`. Petname may equal an existing USER handle (accepted) → then that bare name is ambiguous everywhere (422 "…names both a local user and a kernel; use the account id or the kernel's public key"). Renaming sys → 422; sigil → 422. Renamed handle propagates into ledger `to_handle`.
- Drift: pinned run on stale proxy → 500 `execution_failed` **"the provider updated this action; nothing was charged — re-run to refresh"** meta `{retry:"refresh"}`; a FAILURE tx is recorded (gross 133, refund 133, ticket_id set) and the proxy row is deactivated + still old description. Next pinned run → 409 `terms_changed` meta new price/hash and the proxy is refreshed+active. Unpinned run then succeeds.
- Identity over HTTP carries `network_digest` and omits `stop` when null; the CLI `--json` prints `stop: null` and omits `network_digest` (CLI struct ≠ HTTP body).

## 10. Users / admin
- suspend → `{target:"alice"}`; suspended token → 401 `unauthenticated` "account suspended"; login same. `GET /control/users/alice` gains `suspended_at`. `PUT /v1/me` shapes as documented; wrong current password → 401 "invalid current password".
- logout → revoked; refresh of a revoked token → 401 "invalid or expired refresh token"; logout again → 401 "invalid or already revoked refresh token".
- CLI `user create` prints "Recovery phrase (write this down; it is shown only once…)" + 12 words; `auth recover USER --phrase --password` → status ok. `recover/start` → `{expires_in_seconds:600, nonce}`; no key → 409 `invalid_state`.
- `GET /control/users` rows `{id, handle, description, available, locked, created_at, updated_at, [suspended_at]}` (local users only). CLI `admin users` prints handle + description.
- Contexts: `juice use NAME --kernel K` adds a second login; `--context NAME` addresses it; config lists both under one kernel.

## 11. Parked call (timeout), recovery, unreachable, refusal, unfunded
- SIGSTOP B, cached proxy run → after **32 s**: 504 `timeout` "remote receipt pending; result confirmation is pending — funds remain reserved on process <id> and the call retries automatically", meta `{pending_since, process_id, refund_eligible_at (+24h)}`. Balance: available −143, **locked 143 = 133 (price) + 10 (lottery stake L)**; the process row shows `locked:133, awaiting_receipt:true, awaiting_receipt_since`. The user's `locked` therefore exceeds the sum of process locks by the ticket stakes; a UI computing "reserved" from the price alone under-reports.
- SIGCONT B → the running retry loop (5 s) settled it: process closed, tx `success gross 133`, no refund. Kill -9 B → the same cached call ALSO parks as `timeout` (the transport connection was still open, so dispatch "may have reached"); only an UNCACHED ref (needs a resolve) fails fast: 502 `peer_unreachable` "peer <KEY> is unreachable" meta `{peer:<KEY>}` (the raw key, although a petname was bound — refusal uses the petname). `last_contact_failed_at` then appears on the roster row; a stream reset mid-flight recorded nothing.
- Kernel restart (A, with the parked trace): recovery resumed the retry at startup; B (restarted meanwhile with credit_limit 100) answered a signed 402, so the parked call settled as `failure, reason peer_unfunded, refund 133` and the 10-credit stake was released (alice +143). Waiting steps, proxies, petname, and balances survived both restarts.
- Peer suspension (B suspends A's key): A's run → 403 `unauthorized` "peer bravo refused this call" meta `{peer:"bravo"}` (petname); cached proxy → `active:false`; a FAILURE tx recorded (gross 23, refund 23, reason `unauthorized`). `GET /v1/steps?peer=` from A while suspended → 401 `unauthenticated` "account suspended" (the PEER's refusal surfaces as the caller's own 401!). Unsuspend → next run re-resolves and succeeds, proxy active again. B's roster shows the suspended peer only under `?all=1` with `suspended_at`.
- Credit limit: B restarted with `credit_limit:100` while holding 431 unpaid exposure → every priced call (even D_max 9) → 402 `peer_unfunded` "this kernel's credit with peer bravo is exhausted; the operator must top up" meta `{peer:"bravo"}`; a FAILURE tx recorded (refund = gross, reason `peer_unfunded`); proxy stays active. After B's operator records the four payments (`admin deposit <Akey> --ref <ticket>` ×4), exposure → 0 and calls resume.
- Config guard: `lottery` above the world's `lotteryMax` refuses boot: "lottery 1000 exceeds this world's ceiling of 100".

## 12. Lottery (ticket face L=100 > obligation D=8), three calls on pets/index (q=9)
- All three buyer txs identical: `gross 9, net 8, fee 1, refund 0, ticket_id`. On a REMOTE tx `net` = the obligation D returned to the caller and `fee` = the import fee — NOT "net to provider". A UI captioning `net` as provider revenue is wrong for remote txs.
- Two draws lost: buyer's realised cost = 1 (import fee only); no ledger row; B's owed list closes them silently; B's `exposure` keeps counting them (24 after 3 calls: a losing draw discharges the obligation but not the exposure, D14).
- One draw won: alice's ledger gains `res:<ticket>` alice→sys **amount 100** (reason "obligation <ticket>"), then sys's ledger `rail:manual:<ticket>` sys→null 100 (auto on manual rail); B's `owed` row `{obligation:8, amount:100, status:"announced", tx_hash:"manual:<ticket>"}`. Realised cost 101. Expected 9 ✓. The winning stake is the only money movement a buyer ever sees for a remote call besides the tx.
- receipt-verification `draw:true` on all.

## 13b. Chain world on a local chain (anvil, decimals 6) — 2026-09-09

Rig: `anvil --chain-id 31337 --slots-in-an-epoch 1`, juice-rail's compiled mocks
(`MockUSDT0`, `MockRouter`) deployed by the kernel's own `flows/lib.sh` recipe, a
generated world file naming them, one kernel serving it with `rail_rpc`.

- `/health` → `decimals:6`, `network:"anvil"`, `rail_address:<vault>`; identity
  carries the same. Money renders scaled: 5000000 base units is `5.000000`.
- **Held deposit shape (this is what was unverifiable on play).** A token transfer
  to the vault from an address no account has registered is booked and listed:
  `GET /control/deposits.deposits[] = {id:"rail:<txhash>:<index>", kind:"deposit",
  amount, credit, status:"held", tx_hash, created_at, party_handle:<sender>}`.
  `party_handle` is the sending address — the fact the operator reads to decide
  whose it is.
- ~~KERNEL DEVIATION: the row's `id` is not a reference `POST /control/deposit`
  accepts~~ → **FIXED in v0.14.1** (`660e969`), re-verified on the chain rig: the
  prefixed id, the bare `<txhash>` and `<txhash>:<index>` are all accepted, and a
  replay of any of them moves money once. On v0.14.0 the id answered 404 "no
  finalized payment rail:0x… has been observed". The UI links the id.
- Attributing credits the named user for the full amount, empties the held list,
  and replays harmlessly (200, no second credit). An unknown reference → 404
  "no finalized payment 0x… has been observed".

## 13. Chain world (`test`, decimals 6) with an unreachable RPC
- Boot without `rail_rpc` → refuses: `rail: world "test" needs rail_rpc: an endpoint is the one thing only you can supply`. With a dead RPC the server STARTS (`rail.unready` logged) and serves `/health` `{decimals:6, network:"test", rail_address:"0x…", …}`; `/v1/me` still has NO `rail_address`; identity `lottery 1000000000, credit_limit 100000000000` (world-scaled defaults), `rail_address`, `finalized:null`.
- Every money verb while the rail is unready → 503 `rail_stopped` "rail is not ready: read chain id: Post …connection refused": `PUT /v1/me/address`, `POST /v1/withdrawals`, `POST /control/deposit`.
- CLI `user deposit` (chain): "Send test to this kernel at: 0x…  You have no address registered, so a payment from you cannot be recognized as yours. Register the address you will pay from first: juice user address ADDRESS". `user address ADDR` needs `--signature` headless. CLI identity adds a "Paid at:" line.
- A database made on `play` refuses to boot on `test`: "bootstrap: this database was made for another network; config selects "test" — one kernel serves one network…". Boot without a kernel name: "bootstrap: kernel name is required: run interactively or set JUICE_BOOTSTRAP_KERNEL_HANDLE" (but it DID write config.json/juice.db/serve.lock into the home before failing — contradicts U43 "fails before writing anything").

## 14. Deviations from juicedocs, consolidated (strict reading: the kernel governs; the UI adapts)

Obvious kernel defects — reported, and **re-verified FIXED on the rebuilt binary v0.14.0-dirty (2026-09-09 11:14)**:
1. ~~PANIC on replayed peer-obligation deposit~~ → replay now returns the existing ledger entry (200), with or without an amount; no panic logged.
2. ~~PascalCase import result~~ → `{created:[…], unchanged, updated, deactivated, rejected}` in snake_case; rows are ordinary action reads (`owner_handle`, `action`, decomposed `http`, no `owner_user_id`, no `source` string).
3. ~~`null` roster~~ → `GET /control/peers` returns `[]` when empty.

Questions for the kernel (defensible either way; unchanged on the rebuilt binary):
4. Remote-principal step renders `required_caller_handle` as `<user uuid>@bravo`. D20 forbids raw ids, but the step stores only `required_caller_remote_id` (D4) and PrincipalID is the rename-stable identity (D15). Should the remote handle be captured at creation for display?
5. `ticket_id` is null on the seller's transaction (API.md: either party can follow it). The seller follows it via `/control/deposits`; should the tx row carry it too?

Kernel decisions the UI adapts to (documented or defensible):
6. A drift refusal (`meta.retry:"refresh"`), a peer refusal, and credit exhaustion each RECORD a failure transaction with a full refund ("one transaction per attempted call").
7. A peer that has suspended this kernel makes `GET /v1/steps?peer=` answer 401 `unauthenticated` "account suspended" — the peer's own answer relayed (only code + message cross).
8. `peer_unreachable` names the peer by raw key; `unauthorized`/`peer_unfunded` by petname (D20: petname where the failing path knows one).
9. Nameless first boot leaves `config.json`/`juice.db`/`serve.lock` (conformance line only demands no superuser and a repeated prompt).
10. Retired per-id action routes answer the router's plain-text "404 page not found".
11. `GET /v1/steps?peer=` omits `truncated` when false; `POST /v1/grants` returns `provider:""`; `DELETE /v1/grants?account=` returns `revoked:null`; `grants/start.provider` is the opaque `provider_key`.
12. CLI `admin identity --json` omits `network_digest` and prints `stop:null`; HTTP omits `stop` when null and carries `network_digest`.
13. WASM create/detail echo the whole `wasm_artifact` (~1.4 MB). No pagination headers on any list.
14. A kernel petname may equal a local handle; the bare name then fails on every mixed command (422) until renamed.
15. `/v1/me.rail_address` is the user's own registered address, absent until registered (not observable in this run). `sys/transfer` accepts self (D18 lists no self rule). The `waiting_on_peer` "inconsistency" was a harness artefact (a local user `bravo` existed on A).

## 15. What the UI must change (planning input)
- **Money unit**: `decimals` from `/health` (0 on play, 6 on chain worlds); no fixed `$`/3-decimals; label by `network`.
- **Pin the kernel**: session must record `public_key` + `network_digest` and refuse a changed server; read `$JUICE_HOME/client/config.json` contexts + `credentials/<ctx>.json` (ecosystem-standard) or at least honour the same identity rule.
- **Action mutations**: target-based routes returning arrays; index convention; `?ref=` reads (including remote resolve, auth required); proxies refuse every lifecycle verb incl. delete; description edits don't deactivate; price/schema/source do.
- **Wallet**: withdrawals list/create (UUID minted client-side), address registration only on chain worlds (`rail_address` non-empty in health), `rail_stopped` state, ledger rows for `res:<ticket>` stakes, sys-side `rail:*` rows, the `obligation <id>` reason on remote calls; remote tx `net/fee` are D/import-fee, not provider revenue; `locked` includes ticket stakes.
- **Outcome classes**: `timeout` (parked; message text now carries the process id), `execution_failed`+`meta.retry`, `unauthorized`+`meta.peer`, `peer_unreachable`+`meta.peer` (raw key), `peer_unfunded`+`meta.peer` ("operator must top up"), `terms_changed`, `grant_required`, `rail_stopped` (503), `insufficient_funds` message includes both numbers.
- **Admin**: identity (sys split, solvency, custody, stop, exposure/credit_limit, rates, rail_address, finalized); `/control/deposits` (held deposits + owed obligations with ticket ids) and the three deposit forms (`--ref` mandatory); no settle verbs; no `admin withdraw`; roster without balances (petname/nickname/has_account/actions/last_seen/last_contact_failed_at/suspended_at); inspect with `account:{suspended}`, all-in indicative prices, `steps`; rename with `new_name`; ambiguity 422.
- **Steps**: `created_by`, `allowed_input`, `waiting_on_peer`, `required_caller_remote_id`; peer-held steps (`?peer=`) are read-only over HTTP (completion is CLI/wire only); cancelled steps carry no reason field.
- **Grants**: plan/start/complete/attach shapes above; `provider_key` opaque; `connectors` tree + `connections` inventory on `/v1/me`.
- **Natives**: `sys/user-lookup` gone; all `local`; `sys/lookup` rows carry `price`, `quote_hash`, `observed_at`, `last_seen`, `last_contact_failed_at`.
- **OpenAPI wizard**: `{name, spec_url}` first, `{name}` re-import; result keys PascalCase; `Rejected[{Key,Reason}]`; no unimport (disable/delete by path instead).
- **WASM path**: compile → create with `wasm_artifact` server-side (body ~1.4 MB).
