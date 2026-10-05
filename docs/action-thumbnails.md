# Action thumbnails

Status: design, not implemented. Resolves the *Conditional* media-slot item in
`requirements.md` §6.2 / §10.

The action page carries a 16:9 media slot, and every list row a square tile
cropped from the same image (`requirements.md` §6.2). The fallback SVG uses
`preserveAspectRatio="xMidYMid slice"` with the glyph centred, so one image
fills both boxes: the square crop trims the ground's sides and keeps the
glyph whole. This document specifies what fills them. Two layers, composed per action:

1. a **deterministic fallback**, always available, computed from kernel data;
2. an optional **provider-supplied image**, hash-pinned by the kernel and served
   same-origin by the BFF, which overrides the fallback when present.

The layers are independent. Layer 1 ships alone and stays the permanent floor;
a card is never blank.

## 1. Deterministic fallback

Server-side inline SVG, seeded from kernel-vouched fields only. No network, no
`style=`, CSP-safe.

**Ground — the provider.** Split `@owner/name` by the first-slash rule
(`requirements.md` §4) and seed the hue from the **owner segment alone**, so
`@chat/inbox` and `@chat/output` share a hue by construction. The hue is drawn
from a committed ring of ~12 quantized hues, excluding the accent band and the
credited/refunded/escrow bands (§7 requires the accent stay distinct from every
money signal). Within-provider variation — rotation, lightness delta, geometry —
is seeded from a second hash of the name, so siblings read as a family without
being duplicates.

**Glyph — the operation.** A monochrome icon over the ground, tinted at a fixed
lightness delta from the hue. Selected by a **lookup, not a hash**: hashing an
icon index yields a glyph uncorrelated with meaning, which asserts a falsehood
about the action and is worse than an abstract mark.

Resolution order:

1. tokenize the name on `/`, `-`, `_`; match tokens against a **committed
   keyword table** (~30–60 operation words onto ~40 icons), longest token first;
2. else by `kind` — one glyph each for `http`, `wasm`, `native`;
3. else a generic mark.

The table and icon path data are vendored and compiled into the binary
(`go:embed`); the generator inlines the chosen path directly into the media SVG,
so the browser receives only the icons actually on screen — no font, no sprite
fetch, no CSP addition. Repeated glyphs on a page collapse into one `<defs>`
block referenced by `<use>`.

Heroicons (MIT, already the §7 set) is the first choice; a second set is
introduced only if a needed glyph is missing.

**Vocabulary: unpruned, deliberately.** An earlier cut withheld the trust
glyphs (shield, verified, lock, badge, star, check) and the money glyphs,
reasoning that a provider could tag-stuff `verified secure trusted` and earn a
forged affordance. That reasoning does not survive asking where the glyph
renders: *inside* the media box, which is decoration, while every kernel-vouched
fact — ref, price, reputation — renders as chrome outside it. Nothing keys off
the glyph, so it is not a trust affordance in the sense `requirements.md` §8.1
governs; a drawn shield asserts no more than the provider-supplied photograph of
a shield that §5 already accepts. Withholding them also cost real accuracy:
`validate-user` fell to `supervised_user_circle` instead of `verified_user`, and
`invoice-parse` matched nothing rather than `receipt_long`.

The one exclusion is technical, not editorial: a single icon carries an inline
`style=`, which the CSP forbids. The residual risk is the one in §5 — a picture
can depict a lie — and its recourse is the same: ratings and `admin` suspension.

**Rejected: tag-index / embedding selection.** Deriving the keyword map from
Material Symbols' ~3,600 icons and their published tags scales better, and an
embedding nearest-neighbour would handle vocabulary the table misses. Both are
rejected for now: the embedder (`@sys/llm/embed`) is not in the final kernel
spec (§10), any non-pure selection needs a cache the BFF is denied (§1), the
accuracy gain lives in a long tail this catalog does not yet have, and the cost
is a vendored corpus, a metadata licence review, a corpus-wide exclusion audit,
a generator, a drift test, and a §7 amendment. The curated table upgrades to the
derived index later without changing any seam.

## 2. Provider-supplied image

### What the kernel holds

Two strings on the action record. **No bytes.**

| field | meaning |
|---|---|
| `image_url` | where the provider hosts the image |
| `image_hash` | SHA-256 of the exact committed image bytes |

Federation gossip grows ~100 bytes per action. Images never cross the network
between kernels. The kernel gains no blob storage, no serving route, and no
hosting liability — it stays a ledger of money and JSON.

### What the BFF does

On rendering a media slot for an action carrying both fields:

1. fetch `image_url` (SSRF guard, byte cap, timeout);
2. hash the received bytes and compare against `image_hash` — **mismatch
   discards the image** and the fallback renders;
3. decode and re-encode to a fixed format and bounded dimensions, stripping
   metadata and defeating polyglot files;
4. serve same-origin from the BFF;
5. write to the cache (below).

Any failure at any step falls back to layer 1.

### What hash-pinning buys

* **Immutability.** The provider cannot swap the image after accruing
  reputation; altered bytes fail the hash.
* **No cloaking.** Serving different bytes to different fetchers fails the same
  check, so a moderator and a buyer necessarily see the same image.
* **No viewer tracking.** The browser never contacts the provider's server, so a
  listing cannot observe who is browsing it.
* **CSP intact.** `img-src` stays same-origin; no third-party origins (§8).

Residual cost: availability coupling. A provider whose server is down renders
the fallback. Acceptable — the fallback is always present.

### Cache

A bounded, **content-addressed** directory: the filename is the hash
(`<cache>/3fa9c2….jpg`).

* Stale entries are impossible — the hash identifies the bytes, so a new image
  is a new file and the old one ages out.
* Only verified, re-encoded bytes are ever written.
* Hex filenames admit no path traversal.
* Evict least-recently-used against a **total-byte** cap (images vary in size,
  so a count cap is the wrong bound).
* Survives restarts; large images stay off the heap.

## 3. Rejected: browser fetches the provider URL

Rendering `<img src="https://provider.example/…">` directly is hotlinking. Note
that YouTube — the obvious precedent — does not do this either: the uploader
submits an image and the platform re-hosts it. Rejected on four independent
grounds: it requires `img-src *`, dismantling §8's policy on every page; the
fetch is a beacon giving every provider the IP, timing, and browse pattern of
prospective buyers; a URL is a mutable reference, permitting post-hoc swaps and
per-viewer cloaking; and card rendering becomes coupled to N third-party servers.

## 4. Rejected: kernel stores the image bytes

Safe, and modest code (stdlib jpeg/png decode plus a blob store and one route),
but it changes what the kernel *is* — from settling money over small JSON
records to hosting unbounded user media, with the storage, moderation liability,
and federation bloat that entails. The hash-pinning design above obtains the
same security properties for two metadata fields.

## 5. Accepted risk

An ingested or hash-pinned image can still *depict* a lie — painted text reading
"free", a drawn ✓ badge, an imitated official mark. Keeping kernel-captured
chrome outside the media box (§6.2, §8) preserves the authoritative reading
adjacent to it, but a decorative slot filled with provider-authored pixels is a
phishing surface that every image-bearing marketplace polices socially rather
than technically. Recourse is ratings and `admin` suspension.

## 6. Spec amendments required

* **§6.2** — replace "deterministic generated fallback" with the layer-1
  construction above (owner-seeded hue ring, lookup-selected glyph, exclusion
  vocabulary), and state the layer-2 override.
* **§1 / §8** — carve out the image cache explicitly: *a bounded,
  content-addressed image cache, deletable at any time without loss, is not
  durable domain state.* A strict reading of "no durable state beyond the
  session" would otherwise forbid it.
* **§10** — record the two-field kernel contract (`image_url`, `image_hash`) as
  the recommended prerequisite, replacing "until the kernel serves images."
* **§7** — only if a second icon set proves necessary; scope it strictly to the
  media slot, which is decoration and must stay confusable-distinct from chrome
  icons.

## 7. Build order

1. Layer 1 (hue ring + kind glyph + curated table) — self-contained, no kernel
   dependency, permanent floor.
2. Layer 2 — only once the kernel carries the two fields.
