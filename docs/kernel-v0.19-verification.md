# Kernel ba7f5e5 — live verification

What the kernel at `../juice` ba7f5e5 serves, measured on throwaway kernels
rather than read from the contract. Every claim here was produced by a request
whose reply is quoted. The previous record is `docs/kernel-v0.18-verification.md`
(4dbfedd); this one covers the three commits since, of which two change the
surface: `aad085b` (an action is a standard tool definition: a title and one
schema subset) and `c669a59` (Prometheus metrics, off the client API).
`ba7f5e5` revises the manual only.

Rig: both binaries built from source (`go build ./cmd/juice`), world `play`.
`alpha` (API 4191, transport 39941) was created by 4dbfedd, seeded with a
delegated-bearer action, a plain action and a grant, then booted on ba7f5e5.
`bravo` (API 4192, transport 39942) was seeded to alpha. An HTTP echo service
on 8911 (`/echo` returns `{"echo": args}`, `/ok` returns `{"ok":true}`,
`/extra` returns `{"ok":true,"extra":1}`); an OpenAPI fixture on 8912. Users
`vendor`, `ana` on alpha. The UI ran against alpha from an isolated
`JUICE_HOME`.

## 1. Upgrade (migration 058 and the boot rewrite)

Booting a 4dbfedd database on ba7f5e5 titles every row with the last segment
of its name, and rewrites stored schemas into the canonical form. The action
stays active and its grant survives:

```
vendor@alpha/app/msg  title "msg"    active true
vendor@alpha/plain    title "plain"  active true
input_schema  {"additionalProperties":false,"properties":{"room":{…},"text":{…}},"type":"object"}
ana's connectors: [{"directory":"vendor@alpha/app", … "actions":[{"action":"vendor@alpha/app/msg", …}]}]
```

## 2. The title

```
POST /v1/actions (no title)        → 422 {"code":"invalid_input","error":"title is required: a short name a person reads in a list"}
POST /v1/actions (81 characters)   → 422 {"code":"invalid_input","error":"title is longer than 80 characters"}
POST /v1/actions ("a\nb")          → 422 {"code":"invalid_input","error":"title must be one line"}
POST /v1/actions ("  padded  ")    → 201, title "padded"
PUT  /v1/actions {target, title}   → 200, active true; ana's grant kept
```

A title is on every action read, list row, `sys/lookup` row (discovered hits
included), import row, and peer-inspect action.

## 3. The schema subset, stored canonical

A create with properties declared `zeta, alpha, opt, kind, nest, empty, c`
(raw HTTP reply, abridged):

```json
"input_schema":{"additionalProperties":false,"properties":{
 "alpha":{"default":3,"description":"A.","maximum":9,"minimum":1,"type":"integer"},
 "c":{"description":"Const.","enum":["fixed"],"type":"string"},
 "empty":{"description":"E.","type":"object"},
 "kind":{"description":"Nullable enum.","enum":["a","b",null],"type":["string","null"]},
 "nest":{"additionalProperties":false,"description":"Nested.","properties":{"b":{…},"y":{…}},"type":"object"},
 "opt":{"description":"Nullable 3.0.","type":["string","null"]},
 "zeta":{"description":"Z.","maxLength":5,"title":"Zeta field","type":"string"}},
 "required":["zeta"],"type":"object"}
```

- **Keys are sorted.** The author's property order is not kept.
- `nullable: true` → `type: [T, "null"]`; `const` → a one-member `enum`; a
  nullable enum keeps its `null` member; `title`, `default`, bounds are kept.
- `additionalProperties: false` is added beside declared properties, at every
  depth. An empty nested `properties: {}` is dropped (`{"type":"object"}`).
- Top level: `{"type":"object","properties":{}}` → `{"type":"object"}`, which
  admits any object (a run with `{"foo":1}` succeeds).
  `{"type":"object","additionalProperties":false}` →
  `{"additionalProperties":false,"properties":{},"type":"object"}`, which
  admits only `{}`: `{"foo":1}` → `schema_violation "field : undeclared key \"foo\""`.
- A map keeps its shape: `{"additionalProperties":{"type":"string"},"properties":{},"type":"object"}`.

Refusals name the path:

```
oneOf        → 422 schema_violation "input.properties.x: oneOf is not supported: a field has one type, a choice among strings is an enum, and a field that may be empty is \"type\": [T, \"null\"]"
untyped node → 422 schema_violation "input.properties.x: type is required"
top-level    → 422 schema_violation "input: the top level must be an object, not string"
```

## 4. Outputs are closed

An action whose output schema declares `ok` and whose endpoint also returns
`extra`:

```
POST /v1/run → {"code":"schema_violation","error":"field : undeclared key \"extra\"","meta":{"charge":"0","tx_id":"fc331eca-…"}}
```

A reply carrying any undeclared field fails, at zero charge.

## 5. Any schema in an update deactivates

The contract says a schema *change* deactivates (D4). The kernel deactivates
on a schema's *presence*, even an identical one:

```
PUT {target, price: 0}                       (unchanged) → active true
PUT {target, input_schema: <as served>}      (unchanged) → active false
PUT {target, output_schema: <as served>}     (unchanged) → active false
PUT {target, source, method}                 (unchanged) → active true
PUT {target, title, description}                         → active true
```

The UI therefore sends only the fields an edit changed.

## 6. Import

```json
{"created":[{"name":"pets/good","title":"Add a pet"},{"name":"pets/summ","title":"Find pets"}],
 "rejected":[
  {"key":"choice","location":"POST /echo/choice","reason":"input.properties.x: oneOf is not supported: …"},
  {"key":"untitled","location":"POST /echo/untitled","reason":"no summary: a title is required (summary or x-juice-title)"}],
 "notices":[{"key":"good","location":"POST /echo/good","note":"input.properties.tag: nullable folded into type [T, \"null\"]"}]}
```

`x-juice-title` wins over `summary`. Folding `example` and resolving a `$ref`
produce no notice.

## 7. Peer inspection

```
reachability {"path":"relayed","rtt_millis":8,"protocols":[…],"version":"juice-kernel/dev"}
actions[0]   {"action_id":"f0533bdb-…","name":"vendor@8cwLo9eA-…/plain","title":"plain","description":"Plain echo.", …}
```

`protocols` is still served; `version` is new.

## 8. Through the UI

- Create: no title, 81 characters, schema text that is not JSON, an invalid
  amount, and a `oneOf` schema each return 422 with the create dialog open,
  the message inside it, the title, name, schema text and header name kept,
  the header value absent from the page. A valid create succeeds.
- Edit: a title change sends `{"target","title"}` alone and the action stays
  active; a refused edit (an untyped property) returns with the dialog open
  and the typed schema kept.
- Run (`Order test`): fields render in the served order, `zeta` labelled
  `Zeta field`, `maxlength="5"`, `alpha` with `min="1" max="9"`, `kind` and
  `opt` with a `null` box, `kind`'s options `"", a, b`. Submitting `opt` blank
  and `kind` with its box ticked returns
  `{"echo":{"alpha":3,"c":"fixed","kind":null,"nest":{"b":false,"y":""},"zeta":"hi"}}`:
  `opt` omitted, `kind` null.
- A `sys/message` task in ana's inbox renders as an approval.
- Browse, search, the workshop, the action page and peer inspect lead with the
  title.
- Chat: on a fresh home it creates its actions titled. With two of them
  retitled to `message` and `read` and ana holding 8 chat grants, a restart
  sends two title-only updates; every action stays active and ana keeps 8
  grants. A further restart writes nothing (zero `action.updated`), so the
  declared schemas equal the stored ones.
- Chat registration still fails on this kernel with `409 create task: the
  call has already settled` (requirements §10, unchanged by this commit).
