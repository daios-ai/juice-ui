# juice-ui — implementation plan

Kernel baseline: **v0.12.14** (`22ade2d`), verified on a throwaway pair and on the
live public network. Everything below is UI work unless marked *kernel-gated*.

**Settled.** Money renders in dollars (the grammar follows `requirements.md`, not
the reverse). No new action-creation workflow. Send names the total debit. A
discovered action becomes runnable once the kernel's contract fingerprint lands.

**Out of scope:** the duplicate `sys` stdlib in search results; the restored
inspect projection; peer-held steps; media images; the trace tree.

**Every step lands whole.** Sequencing orders the work; it never splits a change
across releases. When a step is done, no site still uses the thing it replaced:
all 14 ref sites render through the new partial and the old helpers are gone in
that same change; every price renders through `_price`; `peer_state` is absent
from client, templates, tests and requirements together. No scaffolding, no
temporary branch written to be deleted later, no half-migrated surface.

---

## 1 — Naming repair *(broken today)*

`client.Peer.Handle` / `Inspection.Handle` decode `json:"handle"`, which the
kernel no longer sends; the roster renders nameless cards and a dead rename link.
`AdminRename` sends `new_handle`; the kernel now takes `new_name`.

* `client`: `Peer` and `Inspection` gain `Petname` / `Nickname` / `About`;
  `AdminRename` sends `new_name`; `renameUser` reads the same field name.
* **One display helper** — petname if bound, else the truncated key — replacing
  `.Handle` at its 8 sites. Step 2 consumes this same primitive; written twice it
  reintroduces the drift this plan removes.
* Petname and key are kernel facts: provenance face, linkable. Nickname is a
  stranger's claim: inert text, never linked, never keyed on. Every URL and
  mutation keys on the full public key.
* `admin_user.html` handles two disjoint records (a kernel with no account has no
  balances at all), rather than one template fed a half-empty one.
* A discovered card gains **Name** beside Fund — a `?form=` opener onto the
  existing rename modal, key prefilled, in the one `formWhitelist` table.

## 2 — One ref renderer

`_ref` (9 sites) and `_reflink` (5 sites) split the ref twice; four helpers are
exposed to templates so pages re-split ad hoc. Key truncation added to that
structure would be added twice.

* One `_ref` partial fed by one `ref` helper → `{Owner, Kernel, Name, ID, Local,
  Display}`; the optional `ID` supplies the link, collapsing `_reflink` and
  `reflink`. `refOwner` / `refName` / `refKernel` / `refIsRemote` leave the
  template map.
* A raw key renders truncated (full value in `title`); a petname renders whole;
  a remote owner segment stays emphasized and unlinked.
* On a proxy, `owner_handle` is the kernel petname — never derive an owner from
  it; the computed ref is the only source.

## 3 — Price on every card

`sys/lookup` returns `price` on every result; `parseLookup` never decodes it, so
11 of 20 cards render blank.

* Decode `price`.
* **One `_price` partial** replacing three drifted variants (`_partials.html:34`
  uses `.free`, `workshop.html:66` uses `.muted`, `_stat_strip` has no free
  branch).
* Discovered price is marked by weight, not words.

## 4 — Delete `peer_state`

The kernel removed the field. Dead code today, and it once suppressed the run
form on a working action.

* Drop `client.Action.PeerState`, both chip branches, the `offline` run-form
  suppression, and the self-terminating awaiting poll.
* Availability is never guessed: we let the call run and report what happened.

## 5 — The UI owns failure sentences

A remote failure now returns the bare class `execution_failed`.

* Map kernel error classes to one sentence each, centrally, beside the existing
  `userMessage` mapping — never echo the class.
* "Failed — you were refunded $0.012", reason below, receipt linked.
* Signup: `422 invalid_input` "already exists" → "That name is taken," on the
  field. The string-sniffing in `signupErrorMessage` collapses to a code check.

## 6 — Remove Synthesize

`sys/make` does not exist; the button 404s.

* Delete the menu item, palette entry, form, route, handler, result view and
  partial, whitelist entry, and its tests. Its hardcoded `$0.020` and
  `Available < 20` gate go with it — the last price literals in the codebase.
* No replacement in this pass.

## 7 — Send names the total debit

`sys/transfer` has an execution price (operator-set, `native.transfer`) *and*
delivers an amount. The sender pays both; the recipient receives exactly the
amount.

* Read the price live; the button reads `Send ($2.505)` for a $2.500 transfer at
  a $0.005 price, through `_price` and `money`.
* When the price is non-zero the form states the split at the point of
  consequence; when it is zero there is no split to state.
* No second confirm, no JS-updated label.

## 8 — Fingerprint echo *(kernel-gated)*

Blocked on the agreed kernel change: a contract fingerprint on every action,
echoed on `run`, refused before locking on mismatch, present in `sys/lookup`
results as well as action reads, with the refusal carrying current terms.

* Carry the fingerprint from card/page into the run request.
* On refusal: re-render the new terms in place and ask for confirmation again.
* **Discovered actions get a priced Run button** in this same step.

A discovered card shows its price from step 3 and carries no Run affordance —
not as an interim state, but because a first call binds nothing today (measured:
111 advertised, 552 charged). Step 8 adds the affordance; it removes nothing.

---

## Document edits

* `docs/ui-grammar.md` §5.5 — a discovered card carries a price; drop `peer_state`.
* `docs/ui-grammar.md` §5.6/§6 — the gem mark gives way to the dollar format.
* `docs/ui-grammar.md` §5.11 — roster naming columns; the discovered card's
  second opener.
* `requirements.md` §6.2 — failure copy is ours, not the kernel's string;
  discovered cards.
* `requirements.md` §6.6 — remove the `peer_state` clauses.
* `requirements.md` §3.3 — the "cannot be run" limitation lifts with step 8.
* `requirements.md` §6.4/§10 — remove `sys/make`; re-gate duplicate signup and
  the password minimum; close the inspect-identity item (the kernel restored the
  full action row and `users[]`).
* `ROADMAP.md` — "the `users` list is gone" is stale.

## Verification

Each step: `make check` green (`go test ./...`), plus the user story driven by
hand against a throwaway kernel joined to the live network, then torn down. No
commit before both pass. Steps 1–7 are independent of the kernel change; step 8
waits for it.

## Order

**1 → 2 → 3 → 4 → 5 → 6 → 7**, then **8** when the fingerprint lands. Each
centralization precedes the feature that needs it: the naming helper before the
ref renderer, the ref renderer before truncation, `_price` before the card and
the Send button.
