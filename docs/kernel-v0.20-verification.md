# Kernel 034426e — live verification

What the kernel at `../juice` 034426e serves, measured on throwaway kernels
rather than read from the contract. The previous record is
`docs/kernel-v0.19-verification.md` (ba7f5e5). Of the commits since, one
changes the surface: `034426e` ("action list shows your own actions, and
--all every action this kernel knows that you may see"). The others revise the
manual.

Rig: both binaries built from source, world `play`. `alpha` (API 4291) with
users `ana` and `sys`; `bravo` (API 4292) seeded to alpha, with `tom`'s public
"Weather forecast". Both kernels also discovered other kernels on the same
network, which is why the catalogs below hold more than the seeded rows.

## 1. `GET /v1/actions` is the catalog

Signed in as `ana`, who owns one inactive private draft:

```
total 208 rows over 2 pages (limit 200): 13 local, 195 discovered from 3 peers
titles sorted: true      duplicate titles: 0
ana@alpha/my/draft   "My draft"        active false   (own row, every state)
sys@alpha/message    "Send a message"  active true    (natives are local, listed)
tom@3F_91…/tools/weather "Weather forecast" discovered
```

- The list is ordered by title and paged after scoping: pages of `limit=5`
  came back 5, 5, 5, 5, 5 with no row skipped or repeated.
- The caller's own actions appear in every state without any flag.
- A discovered row carries `discovered: true`, an empty `id`, a
  `remote_action_id`, and the indicative all-in price. Keys:
  `action, active, created_at, description, discovered, id, input_schema,
  kind, name, output_schema, price, quote_hash, remote_action_id,
  requires_grant, title, updated_at, visibility`. It has no `observed_at`.
- `?owner=ana@alpha` → `["My draft"]` alone.
- `?all=1` and `?name=` are ignored: the reply is an ordinary page.
- Anonymous: only public rows; alpha has none, so `[]`, and no discovered rows.

## 2. Through the UI (UI 0.14.0)

- **Browse catalog (empty query):** discovered rows render in the discovered
  form (unfilled `R`, no stats line) and link to `/discovered?ref=…`. No link
  to `/actions/` with an empty id. Opening one resolved it to a local proxy.
- **Search hints:** the page fetched `/search/catalog.json` once per load
  (`Cache-Control: no-store`) and sent no request while typing. "weather"
  offered 8 titles, bravo's "Weather forecast" first; "message" offered
  "Send a message", "Post a chat message", "Delete a chat message"; "my dr"
  offered the private draft. The transaction count stayed at 1 while typing.
- **Shared fixture:** `server/testdata/hints.json` (11 cases) passed in the
  browser's matcher, with the catalog file served from the fixture, as it
  passes in Go.
- **Search fallback:** with `native.lookup.price = 7` in alpha's config
  (served price 7), the search showed the "priced" banner and matched the
  catalog: titles beginning with "weather" first, then titles holding it,
  then descriptions. Discovered rows were kept and linked to resolve; the
  resolved proxy kept its `/actions/<id>` link.
- **Workshop:** ana's own actions only, the inactive draft included.
- **Owner page:** `/users/ana@alpha` lists the draft.
- **Chat:** started against alpha; a restart wrote nothing (0 `action.created`
  or `action.updated`).
