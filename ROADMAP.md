# juice-ui Roadmap

Working notes for planned work. Nothing here is normative: an item becomes
binding only when written into `requirements.md`, and an item with a kernel
prerequisite stays *conditional* until that prerequisite is confirmed on the
running kernel (the §10 gating discipline).

## Images for users, actions, and kernels

Marketplace surfaces need real visual identity: users, actions, and kernels
each carry an image, as first-class metadata analogous to `description`
(the human visual channel beside the semantic one).

**Kernel prerequisite (outside juice-ui scope).**

* `image_hash` on `User`, `Action`, and the kernel identity (`GET /health`,
  manifests). Content-addressed: the hash is the identity of the bytes.
* The one invariant: **image bytes never appear inline in any existing
  message** — manifests, gossip, list responses, and receipts carry only the
  hash; bytes move on a dedicated, lazily driven, immutably cached path.
* `GET /v1/images/{hash}` serves bytes with immutable caching. Upload happens
  at create/update; the kernel re-encodes server-side to a capped raster
  (~100 KB), strips metadata, and rejects SVG (script-capable).
* Federation: an image protocol twin of manifest serving
  (`/juice/fed/image/1`), fetched by hash on demand or at background low
  priority, deduplicated and cached forever. The manifest signature covers
  `image_hash`, so integrity is verified transitively without the bytes
  entering canonical JSON.
* Traffic consequence: one cold, incremental catalog fetch per viewing peer,
  once; steady-state federation traffic (manifests, calls, receipts) is
  unchanged, because immutable content is never re-transferred.

**UI work (conditional on the above).**

* A card media slot in the visual grammar, with a deterministic generated
  fallback (hash-seeded, palette-constrained) so a missing image is a default
  avatar, never a broken grid.
* The BFF proxies `/v1/images/{hash}` with `Cache-Control: immutable`; an
  in-memory hash-keyed cache is permissible (a cache, not domain state).
* Rendering discipline: an image renders only inside the fixed media slot;
  kernel-captured chrome (ref, price, reputation) always sits adjacent to and
  outside it; the payment sheet keys on kernel fields and never places the
  image near the confirm.
* Operational note: images raise federation moderation stakes (an operator
  imports pictures, not just strings); suspension remains the enforcement tool.

## Unsurfaced inspection field (peer-held steps)

`GET /control/peers/inspect?key=` carries one list the UI decodes past but does
not yet render (the `users` list it once carried is gone as of v0.12.8):

* `steps` — the steps that peer holds whose required caller is *this* kernel.
  Completing one is `step complete <id> --peer <key>`: a superuser act, because
  the request is signed with this kernel's own federation identity and so acts as
  the whole kernel. That makes it a distinct feature from the ordinary inbox (the
  completer is the operator, not a user), so it wants its own surface and its own
  §6.6 clause before anything is built.
