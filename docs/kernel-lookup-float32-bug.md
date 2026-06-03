# Kernel bug: `@sys/lookup` fails its own output-schema validation (`score` is `float32`, schema says `number`)

**Reporter:** juice-ui (BFF) team · **Found:** 2026-06-03 · **Component:** kernel — native `@sys/lookup` action / output-schema validator

## Summary

On a kernel **with an embedder configured**, every `@sys/lookup` call that returns
at least one result fails with HTTP **422**:

```
{"error":"field #.results[0].score: expected number, got float32"}
```

The lookup actually runs; it is rejected only when the kernel validates the
**result** against `@sys/lookup`'s `output_schema`. The `score` field is emitted
as a Go `float32`, and the output-schema validator does not accept `float32` for
a property typed `"number"`. A failure transaction is recorded and the call is
fully refunded.

This is a **kernel-side bug** (a `float32` is a number). It is not a UI issue and
not an embedder/config issue — it reproduces against a healthy embedder.

## Impact

Semantic search is effectively unavailable wherever an embedder *is* configured:
any query that matches ≥1 action errors out. (A query that matches nothing
"succeeds" with an empty `results[]`, because there is no `score` to validate —
which is why it can look intermittent.)

In the juice-ui UI this surfaced in two places, both from this single root cause:
1. **Run panel** for `@sys/lookup`: rendered as "the action failed — fully refunded."
2. **Discover** page: falls back to catalog search with the reason shown.

The UI now handles it gracefully (honest failure + catalog fallback), but
semantic ranking stays dark until the kernel is fixed.

## Environment

- Kernel: running instance on `127.0.0.1:4040`, embedder present (`nomic-embed-text`).
- `@sys/lookup` `output_schema` (from `GET /v1/actions?owner=@sys&name=lookup`)
  declares `results[].score` as:
  ```json
  "score": { "description": "Relevance score between 0 and 1", "type": "number" }
  ```

## Minimal reproduction (HTTP)

```sh
B=http://127.0.0.1:4040
H="@bugrepro_$RANDOM"; PW="repropass123"

# 1. throwaway user
curl -s -X POST $B/v1/users -H 'Content-Type: application/json' \
  -d "{\"handle\":\"$H\",\"email\":\"${H#@}@example.com\",\"password\":\"$PW\"}" >/dev/null

# 2. login
AT=$(curl -s -X POST $B/v1/auth/token -H 'Content-Type: application/json' \
  -d "{\"handle\":\"$H\",\"password\":\"$PW\"}" | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')

# 3. zero-funded process (lookup is price 0)
PID=$(curl -s -X POST $B/v1/processes -H "Authorization: Bearer $AT" \
  -H 'Content-Type: application/json' -d '{"funds":0}' \
  | sed -n 's/.*"process_id":"\([^"]*\)".*/\1/p')

# 4. call lookup with a query that matches something
curl -s -w '\nHTTP %{http_code}\n' -X POST $B/v1/call -H "Authorization: Bearer $AT" \
  -H 'Content-Type: application/json' \
  -d "{\"process_id\":\"$PID\",\"action\":\"@sys/lookup\",\"args\":{\"query\":\"send a message\",\"limit\":3}}"
```

### Observed output

```
{"error":"field #.results[0].score: expected number, got float32"}
HTTP 422
```

The recorded transaction (`GET /v1/transactions?process_id=<PID>`) is:

```
status=failure  reason=output schema violation: field #.results[0].score: expected number, got float32
```

### Expected

HTTP 200 with `{"result":{"results":[{...,"score":0.87}, ...]}, "tx_id":..., "trace_id":...}`
and a `success` transaction. A relevance `score` is a number; it should validate
against `"type":"number"`.

## Suspected cause & suggested fix

The native `@sys/lookup` handler appears to populate `score` with a `float32`
(e.g. straight from the embedding-similarity computation), while the
output-schema validator's `number` check only admits `float64` / JSON numbers and
treats `float32` as a distinct, non-matching kind.

Either fix resolves it (the first is the smaller, more local change):

1. **Emit `score` as `float64`** in the `@sys/lookup` result type (convert the
   `float32` similarity to `float64` before returning). Likely a one-line cast.
2. **Make the output-schema validator accept `float32`** wherever it accepts
   numbers (treat all Go float/int kinds as `"number"`). This also prevents the
   same class of bug in any other action whose handler returns a `float32`.

A regression test: register/keep `@sys/lookup`, ensure an embedder, call it with a
query that returns ≥1 result, and assert HTTP 200 + a `number` `score`.

## How juice-ui copes meanwhile (no kernel change needed for the UI)

- The Run panel classifies this as an **execution/output failure** (refunded), not
  an input rejection, and links the failure transaction.
- Discover **falls back to catalog search** and shows the kernel's own reason,
  e.g. *"(reason: field #.results[0].score: expected number, got float32)"*, rather
  than asserting a missing embedder.

Once the kernel returns a schema-valid `score`, semantic search lights up with **no
UI change**.
