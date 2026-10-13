# Juice UI — Visual Grammar

The single source of truth for the interface. Every screen is built from the
canvas and components below; nothing is styled ad hoc. **Rules are normative
(MUST).** Code conforms to this document, not the other way round.

Three laws underpin everything:
- **No inline styles.** Every visual decision is a named class in `app.css`.
- **Tokens only.** Components reference semantic CSS custom properties (§2),
  never raw colors or sizes.
- **Closed component set.** All UI composes the registry below; there is no
  extra button style, no bespoke widget. Adding a component means adding it here
  *and* to the drift guard (`server/grammar_test.go`). A small, fixed set is what
  makes runtime user customization tractable.
- **Lean but flexible.** The grammar grows by generalization, never accretion.
  A new need first composes existing components; only if that genuinely fails
  does the set admit **one** general component with a stated principle, a
  registry entry, and a drift guard — applied everywhere the principle holds. A
  page-local widget is never admitted; every deviation is generalized or deleted.
- **Content-first, calm.** Title → content. Commands live quietly *with* the
  content they act on (never a floating page-head cluster); color appears only at
  the real money/danger moment. Hierarchy comes from position and whitespace, not
  from filled buttons.

**Component registry** (the whole vocabulary):

| Component | Class(es) | Role |
|---|---|---|
| Button | `.btn` (the one neutral) · `.btn-money` · `.btn-danger` · `.btn-icon` · `.btn-quiet` | actions |
| **Tabs** | `.tabs` + `.tab` | **the** view switch — login sign in/create toggle *and* in-content list filters |
| Menu | `.menu` + `.menu-item` (`[data-menu]` trigger) | dropdowns & popovers, incl. quiet "+ new …" |
| Card / list / table | `.card` · `.card-list` · `.action-list` + `.action-row` · `table` (`.rowable`, `.stats-table`) | containers & data |
| **Media** | `.media` (16:9 slot) · `.media.tile` (square) · `_media` / `_tile` partials · `cardArt` fallback | the action image (§5.13) |
| **Figures** | `.stat-row` + `.stat` (label · value · link) · `_figures` partial | **the** headline money figures, one block each — label, mono amount coloured by its signal, a link to the records behind it. One component on wallet, home, workshop and a session |
| **Pager** | `.pager` + `_pager` partial | quiet offset pagination under any long list (20/page) |
| **Crumb** | `.crumb` (sticky `<nav>`) | a detail page's context row: back-link + record identity + its inline commands. Sticky — *the way up never scrolls away* (self-gating on short pages; the page's own header, not floating chrome) |
| **Search pill** | `.omni` + `.omni-go` submit segment + `.omni-suggest` | **the** search, in the topbar at every width, segmented like a single pill: input left, equal-height icon-submit right. Search hints drop beneath it as a `.menu`-styled panel of links, matched in the browser against the catalog file (`/search/catalog.json`) loaded once per page — each a title that runs the search for it, never an action. The command registry's entries list first among them, marked `›` (`.menu-item.command`; a title row never carries the mark), at most three beside titles, twelve rows in all; `>` lists commands only (§5.9) |
| **you-row / you-tiles** | `.you-row` · `.you-tiles`/`.you-tile` | a setting row, or an icon destination tile, on the `you` hub |
| Field | `.field` + inputs/select/textarea · `fieldset.group` (a nested node) · `[data-rows]` / `[data-row]` / `[data-row-add]` / `[data-row-remove]` (repeatable rows) | forms |
| **Switch** | `.switch` (`role="switch"`, `aria-checked`) · `_json_switch` | a binary view or setting: its label, then a track and knob, ink-filled when on; the JSON switch at the top right of a form and of a frozen form (§5.4) |
| **Frozen form** | `.frozen` · `.field .box` · `.frozen-fold` · `_frozen` / `_value_block` partials (the `_field` partial with `Frozen` nodes) · `.switch` | **the** read-only JSON value — a result, a receipt's args and result, sealed `partial_args`, verification checks — in the run form's own anatomy, each control a box in the read-only register (no fill, a hairline), nothing submittable; raw JSON and Download behind the same JSON toggle the form carries (`requirements.md` §7). `dl.kv` is a label/value list of kernel facts (an account's balances) |
| Dialog | `dialog.modal-form` · `dialog.modal-confirm` (`#modal-backdrop`) · `dialog.palette` (the help overlay) | overlays (bottom-sheet on mobile) |
| Status | `.pill` (+state) · `.sig-*` money signals · `.chip` (visibility/kind) | trust signals |
| Feedback | flash · `.outcome` · `.banner` · `.live-region` · `.progress` · `.badge` | results/notice |
| Ref | `_ref` partial | an action address `owner@kernel/name` (owner linked only when local) |

---

## 1. Canvas

The frame every page renders into. Defined in `layout.html`.

```
┌──────────┬───────────────────────────────────────┐
│ SIDEBAR  │  flash bar (one-shot, when present)     │
│ brand    ├───────────────────────────────────────┤
│ juice ◄logo│ ┌── page-head ───────────────────┐   │
│          │  │ title                           │   │
│ ▸ browse │  └─────────────────────────────────┘   │
│ ▸ inbox• │  content (container, max ~1160px)       │
│ ▸ workshop│                                        │
│ ▸ wallet │                                         │
│ ▸ you    │  (• = badge)                            │
│ ─ chat•  │  (apps, after a divider)                │
│ ▸ network│  (network/admin: sys only)              │
│ ▸ admin  │                                         │
│          │                                         │
│ ⌂ you    │  ← identity summary → the `you` hub     │
└────────┴─────────────────────────────────────────┘
```

| Region | Contents | Rules |
|---|---|---|
| **Sidebar** | brand (`juice-logo.svg`) → `/browse`; collapse toggle; nav groups; identity summary at foot | Sticky, full height. Collapsible to an icon rail; state persisted server-side (`sidebar` cookie), stamped on `<html data-sidebar>` — no flash. |
| **Nav** | `Home · Inbox · Workshop · Wallet · Settings` + `Network · Admin` (only `sys`) | The marketplace groups (§1 of `requirements.md`); one server-built nav model drives the sidebar, the mobile drawer, and the palette. Active group carries `aria-current="page"`. `inbox` carries the task badge; an app entry (chat) MAY carry its own. Icon + sentence-case label; label hidden when collapsed. |
| **you** (hub) | balances, links to wallet/workshop/network/agents, account modal, connected accounts, theme picker, Sign out | A real screen, not a popover — the profile/preferences hub. Theme picker is a `<select>` of the shipped themes + "System default". |
| **Main area** | flash bar, then the content container | Container: `max-width ~1160px`, centered. **The container is the ONLY width decision**: every component fills it — no per-component `max-width` caps (no narrow strips inside broad pages). |
| **page-head** | the title only — no subtitle, no commands (§3, §5.3) | Present on every **index** page. Commands sit inline with their content. |
| **Flash bar** | one transient message | One-shot cookie, top of content; `role=alert` (error) / `status`. Not a feed. |
| **Mobile** (`≤720px`) | the same shell, narrower: the sidebar becomes a left **drawer** opened by a menu button at the topbar's left (`requirements.md` §6.1) | The topbar stays at every width and carries the menu trigger and the one search pill. The drawer holds the **whole** nav (the same model the sidebar renders); its trigger carries the inbox badge. No fixed bottom bar: the nav is unbounded, so any bar would need an arbitrary overflow. |

**Entry screen** (unauthenticated): no sidebar — a single centered, branded auth
card (`◆ juice`) that **names the kernel you're joining** ("Sign in to
`handle`") with a short **key fingerprint** for pre-auth verification, then the
**Sign in / Create account** toggle (the `.tabs` component, §5.2, its one
retained use), one action per tab. The kernel handle + key come from the open
`GET /health`; the sidebar brand shows the same handle once signed in (kernel at
the top, your own `handle` at the profile foot). Tabs are real `/login?tab=`
links (server renders the active tab with JS off); app.js swaps panels
client-side.

**Layer order** (all native `<dialog>` except the menu/flash; `Esc` closes the
topmost): content < flash < menu/popover < modal form & confirm modal < help
overlay < aria-live toast.

---

## 2. Tokens & theming

Semantic tokens are the only colors/radii components may use. Generated per theme
into `static/themes.css` from Ghostty 16-color palettes (`server/themes/`).

| Token | Meaning |
|---|---|
| `--bg` / `--surface` / `--surface-2` | page / raised / raised-more backgrounds |
| `--text` / `--text-muted` | body / secondary text (both ≥4.5:1 on bg) |
| `--border` / `--border-strong` | hairline / control borders |
| `--accent` / `--accent-hover` / `--accent-contrast` / `--focus` | **money-movement + focus** color, its hover, readable text on it, focus ring |
| `--credited` / `--refunded` / `--escrow` | money signals: in `+` / returned `↩` / parked `◷` |
| `--danger` / `--danger-contrast` | critical color, readable text on a filled danger button |
| `--sel-bg` / `--sel-fg` | text selection |
| `--radius` (card) / `--radius-s` (control) / `--shadow` (`none`) | structural (in `tokens.css`; a theme MAY override `--radius`/`--radius-s`, e.g. `terminal` → `0`) |
| `--font-ui` (proportional) / `--font-mono` (provenance) | type faces (`tokens.css`; a theme MAY override `--font-ui`, e.g. `terminal` → `var(--font-mono)`) |

- **Theme selection:** defaults from `prefers-color-scheme` (a default light and
  dark theme); an explicit choice is the `theme` cookie (slug) stamped on
  `<html data-theme>`, no flash, works with JS off.
- **Contrast:** every theme MUST pass WCAG AA — body/muted ≥4.5, signals/accent
  ≥3.0, accent distinct from every money signal. Enforced by test; a failing
  theme does not ship.
- **Money signals** are color **+ glyph on text/borders only, never fills.**
  Accent is reserved for money movement (§5) and focus — never decoration.

---

## 3. Typography & page structure

- **Face:** the **proportional system sans stack** (`--font-ui`) for chrome and
  prose — the storefront register. **Monospace (`--font-mono`) is reserved** for
  the *provenance* set: money, IDs, hashes, addresses, and sealed
  `partial_args`. The typeface itself marks kernel-vouched facts apart from
  chrome and from counterparty prose. (The `terminal` theme overrides `--font-ui`
  back to mono; components never assume one or the other — they use the token.)
- **Scale:** `h1 1.4rem` · `h2 1.1rem` · `h3 1rem` · body `.9rem/14px` ·
  `.small .85rem` · `.tiny .8rem`. Headings carry slight `letter-spacing`.
- **The brand names the kernel and its world** (`.brand` + `.brand .world`), in
  both renderings of the nav — sidebar and mobile drawer. A kernel serves one
  world for life, and the world decides what the money on every screen is:
  whether it can leave, and what it is worth. The symbol on an amount does not
  say that — `credits` is not "not redeemable" — so without this a person cannot
  tell a rehearsal from a live market. Both halves are `/health` fields, stated
  and not judged: the UI never labels a world play or real, it names it.
- **One capitalization: sentence case.** Page titles (`<h1>`), section
  headings (`<h2>`) and nav labels all start with a capital and continue as
  written. A page is named as the nav and the lists name it: `Home · Inbox ·
  Workshop · Wallet · Settings · Network · Admin`, a detail page the singular
  of its list (`Transaction`, `Session`, `Task`), and pages outside the nav
  likewise (`Recovery phrase`, `Connect`, `Error`, `Juice installation` — the gate's
  screen, an `.auth-card` with the state line, the `.progress` bar or the
  first-boot form). A title rendered from data
  (an action's title, an address) keeps its own case. `TestHeadingCase`
  enforces it. A **detail** page
  opens with the sticky **crumb** (`← group` back-link + record identity) above
  the title and shows the resource id/name.
- **Money, IDs, hashes, addresses:** mono + `tabular-nums`. The **owner
  segment** of a ref is weight-emphasized (`.owner`) and MUST NOT use accent.
- **Index vs detail:**
  - *Index* = `page-head` (the title) → section `<h2>` → a `.tabs` filter
    row (with an optional inline `+ new …` command) → list.
  - *Detail* = crumb (sticky back-link) → `<h1>` → `.subtitle` (the id) → cards.

**Copy discipline — words are a component.** Prose is rationed like color. Every
visible string is exactly one register:

- **No orientation prose.** A page is not introduced: the title, the section
  headings and the content say what it is. A `.subtitle` holds only a record's
  **identity** on a detail page (its id, an action's ref, a kernel's key or its
  own self-description) or a superuser **scope note**; never a sentence about
  the page.
- **Disclosure** — only a fact that is data and changes what a person does: who
  is paid and how much (`Paid from your balance to …`, a first payee), a ticket
  or payment status. Shown **once, at the point of consequence**. A sentence
  explaining how the system behaves ("refunded if it fails", "saving
  deactivates", "secrets are write-only", "re-importing reconciles") is a
  remark, and remarks do not appear.
- **Narration** — how the kernel/system works. Belongs **nowhere**: structure,
  labels, columns, chips, and just-in-time affordances already carry it. Cut it, or
  turn it into structure.

**One exception, by principle:** money that can be lost in a way the UI cannot
observe is explained where it moves. The wallet's **How money moves** card is
that one block (requirements §6.5): the registered address and the deposit
address and token, each labelled with one `.help` line saying what it is, and a
`.banner-error` warning led by the `#alert` icon — "Send from your registered
address only, never from an exchange." — since a payment from an exchange is
held for the operator and nothing else on screen would show why. No other
surface carries explanation.

Empty states name the absence in one phrase ("No connected accounts yet."), never a
lecture on how the list will fill. Field `help` states what to type or the single
consequence of that field — it is not where a feature gets explained.

**No reassurance, no mechanism.** The label already says what a field is; do not
re-describe it, and never explain how the system handles the value. A password
field is `label: Kernel password` and nothing else — **banned** is a caption like
"The agent's own kernel-account password. It is piped to the daemon and never stored
— re-enter it after a restart." (restates the label, then narrates internals). A
read-only region needs no "Learned by the agent. Read-only." — the absence of an edit
control *is* the statement. This holds identically in **app** templates
(`apps/*/templates/`); the drift guard (§6) lints them the same as the core, and the
disclosure/narration line is enforced by review because it is a matter of register,
not length.

---

## 4. Spacing scale

One scale; no ad-hoc margins. Steps (rem): **.25 · .35 · .5 · .75 · 1 · 1.4 · 2**.

| Use | Value |
|---|---|
| `.stack` between children | `1` |
| `.row` gap / `.row.compact` · `.cluster` gap | `1` / `.5` |
| `h2` top / `h3` top | `1.4` |
| `.subtitle` bottom | `1.4` |
| `.card` padding | `.85 1` |
| `.field` bottom | `1` |
| `.action-list` gap (one column) | `1.1` |

Compose with `.stack` / `.row` / `.cluster` / `.action-list`; never hand-set margins in
templates.

---

## 5. Components

### 5.1 Buttons — one neutral, plus money & danger

All buttons share **one geometry** (padding, height, radius, font-size).
**Color is reserved for the real money/danger moment**; everything else is the
single neutral `.btn`, so two ordinary buttons never read as "different types."

| Class | Tier | Fill | Use |
|---|---|---|---|
| `.btn` | **the one neutral** | transparent + border, weight 500 | every ordinary action & form submit: create, save, import, unimport, complete (unpaid), update, sign in, search, inspect, navigate, add-row |
| `.btn-money` | **money movement** (solid accent) | `--accent` / `--accent-contrast`, weight 600 | run/pay, synthesize, deposit, add funds, withdraw — **names the amount in parens** as `verb (N)` (e.g. `Run (12)`, `Synthesize (20)`); the accent color conveys it's credits, so **no credit glyph on the button**. An **ordinary task completion is NOT a money button** — it is the neutral `.btn` reading `Complete` (no amount): the completer does the work, but the money was reserved by the task's creator, so `Complete (N)` would falsely read as "you pay N". A **payment task** (its target bears the kernel's `effect="transfer"`, requirements.md §6.3) IS a spend of the completer's own balance and uses `.btn-money` naming the all-in amount |
| `.btn-danger` | **critical** (solid danger) | `--danger` / `--danger-contrast`, weight 600 | delete, suspend, force-end, decline a task |
| `.btn-icon` | icon-only | — | icon buttons; **MUST carry `aria-label`** |
| `.btn-quiet` | inline text button | — | Cancel, remove-row, and the quiet inline commands (`+ new action`, `update account`, `+ deposit credits`) |

**One geometry, one row, one look.**
- No size modifier exists: every button, in a table cell or a card, has the
  one geometry. `.small` is a text utility, never on a button.
- A record's commands stand in **one `.cluster` row** (gap .5rem), primary
  first, destructive last. A command that posts to its own action joins the row
  through the HTML `form="…"` attribute (a task's Decline beside Complete), so
  no command drops below its row. A quiet text command never shares a row with
  buttons; navigation inside such a row (`Inspect`, `Rename`) is a `.btn`.
- One command has one look and one label everywhere: Suspend is `.btn-danger`,
  Unsuspend `.btn`, ending a session `End · refund N` (`.btn-danger`). There is
  no quiet-danger tier.
- `TestButtonsHaveOneGeometryAndOneLook` enforces this on every template.

**Rules.** These are the *only* button styles. The money tier is the **authorizing
control** — the form submit that actually moves credits (`Run (12)`,
`Synthesize (20)`, Deposit/Withdraw). The run form is **inline** (on the action
page), not a modal — its `Run (N)` confirm is the money control directly. **Danger
beats money** (an op that is both destructive and monetary is danger — e.g.
force-end). Money buttons state the amount in parens when it is fixed/known —
`verb (N)`, **never** a credit glyph
(the accent color already says "credits"); where the unit must be spelled in prose
(help text, flashes, errors), write "N credits". When the operator types the amount
(deposit/withdraw) the adjacent field is the amount. Destructive buttons that
can't be undone use `data-confirm` (§5.8).

### 5.2 View switching — one component: `.tabs`

All view switching is the **`.tabs`** underline component. Every tab renders
through the single **`_tab`** partial (built via the `tab` / `paneltab` helpers):
`.active` **and** `aria-current` come from **one bool**, so they can never
diverge, and adding a tab can't reintroduce that class of bug. No template
hand-writes `class="tab{{if …`; a drift guard enforces it.

```
┌ .tabs ─────────────────────────────────────────┐
│  all   active   inactive            + new action │
└──────────────────────────────────────────────────┘
```

| Use | Renderer | Active |
|---|---|---|
| **Entry toggle** | `paneltab` in `.auth-card .tabs` — Sign in / Create account (real `?tab=` links; JS swaps panels **and moves `aria-current`** so only one is lit) | requested tab |
| **List filters** | `tab` in `.tabs.control-tabs` — action `all/active/inactive`, task `all/done/cancelled` (real query-string links, reload) | selected filter |

**Distinct axes get distinct rows, never mixed.** The actions page's *ownership
scope* (`yours | everyone`, `sys`) is its **own** switch on the heading, not a
peer of the state filters — mixing them let two tabs on different variables both
light up. Each axis preserves the other's value (`actionsHref`).

A filter row may carry **one** inline command (`+ new action`) as a
`.btn-quiet.tabs-action`, right-aligned and not styled as a tab. The `.linkbar` is
retired.

### 5.3 Commands are inline & quiet

The `page-head` carries **no commands** — just the title. A command
lives **inline, with the content it acts on**. An opener that **adds** something
is a single quiet affordance: `+ new action` above the actions list,
`+ deposit credits` in the users section, `+ inspect a kernel by key` — all
`.btn-quiet` (or a plain link); none float, none are colored. A **record's
verb** — Name, Rename, Suspend, End — is a `.btn` (§5.1) wherever it appears,
on the record's page or in its row, so one verb has one face. A `.cluster` of
verbs is a child of a `.stack` (a `.card.stack`, a `form.stack`): that is what
separates it from the content above, and nothing else does (§4).

- When one group has several create paths, the quiet opener is a `+ new … `
  `[data-menu]` trigger opening a `.menu` (§5.12) of the methods — not a row of
  equal buttons (see the action page's four creation methods).
- Money moves stay deliberate: the quiet opener launches a modal (§5.8) whose
  confirm is the `.btn-money` that names the amount.

### 5.4 Forms

- `.field` (bottom `1rem`) wraps `label` (weight 600) + optional `.help` (muted
  `.85`) + control. Required marker: `.req` (`*`, accent).
- **Inputs / selects / textarea:** full width, shared height, `--border-strong`,
  `--radius`, `--bg`. Inputs and buttons on the same row align in height.
- **Textarea:** starts at **exactly one line** (`min-height` = 1 line + padding),
  **autosizes** to content up to 10 lines then scrolls; mono; `rows="1"`. Never
  a fixed multi-line box.
- **Progressive widgets:** repeatable rows (`[data-rows]`/`[data-row]`, Add =
  `.btn-icon` `#plus`, Remove = `.btn-icon` `#x-mark`, each with its
  `aria-label`; one mechanism for the http parameter rows and a schema array's
  rows — Add clones the last row, or the kept template once every row is
  removed, clears it and, when the
  container names its array's path, renumbers the clone to one past the highest
  index in every `name`, `id`, `for` and nested `data-rows`), scheme reveal
  (`[data-auth-scheme]`), kind reveal (`[data-kind]`) — all disable hidden
  inputs so they aren't submitted. Secrets are masked, write-only, never
  pre-filled.
- **Call form** (`_call_form`) — **one** component for *every* action execution
  (action run **and** task completion), always inline (never a modal): optional
  **locked block** (a task's pre-bound args as a frozen form, `.frozen.locked`,
  no verbal label — the sealed half of the form), the **editable input**, **one
  button** (a paid run's `Run (N)` money button, §5.1; an ordinary task's neutral `Complete`; a payment task's money button, §5.1)
  with `.progress` + `hx-disabled-elt`, then the **result attached below**
  (`.outcome`, never nested). Tasks are **one-shot** (`data-oneshot`); actions repeat.
- Editable input is schema-driven, one of three: **structured** (typed fields +
  the JSON toggle), **approval** (nothing left to fill → just the button), or a
  lone **raw JSON** editor (free-form schema). Same component throughout.
  Structured means the schema's whole tree: a nested object is a
  `fieldset.group` under its `legend`, an array its `[data-rows]` with Add and
  Remove, a map-shaped node an empty JSON textarea with the shape as its
  placeholder; a field's wire name is its JSON Pointer (`f_/messages/0/content`,
  `t_` the type hint, `n_` the null box; `requirements.md` §6.2).
- **JSON switch** (`.switch`, `_json_switch`: the label `JSON` and a track
  with a knob, `role="switch"`, at the top right of a `[data-json]` block) is
  the one way to see a block as JSON: on a structured form it swaps the fields
  for the raw editor; on a frozen form it swaps the fields for the raw JSON (and
  its Download). A binary view whose off-state has no name is what `.tabs`
  (§5.2) cannot express; the switch is the component for it. No "edit as JSON"
  button, no disclosure for JSON, no underlined word.

### 5.5 Cards, lists, tables

- `.card` (surface, border, radius, pad); `.card-list` for stacked cards;
  `.action-list` for a collection of things, one **ranked column** of
  `.action-row`s. There is no grid: a rank is a sequence, and rows align the
  facts a person compares. Like every component it fills the container (§1).
- **Action row:** a **square tile** (`.media.tile`, §5.13) on the left, then
  the action's **title** as the link to the action (`.action-title`) — never
  the bare name or the raw kind string — with its chips beside it and the price
  right-aligned on that line (`.action-line`), the **ref** on its own line
  beneath (`.action-ref`, `_ref`, owner emphasized), the description (at most two
  lines, `.action-desc`), and **the same `_stat_strip`** the action page uses
  (icon + tooltip: success · rating · latency), rendered **price-less** on the row
  (`statStrip … withPrice=false`) since price is already on the title line. The
  title is the row's one pointer target; the row is `.rowable` for `j`/`k`/`Enter`
  and carries no click handler. The peer roster and the agents page use the same
  row. The strip sits outside the tile (kernel chrome never overlaps counterparty
  pixels). One strip, one grammar, both surfaces. **Attribute chips** (`.chip`)
  mark the *exception* only:
  - **A chip is ONE letter or ONE icon. Never a word, never a phrase** — the
    meaning lives in its `title`, which is also its accessible label. A chip is
    a mark beside a title, not a caption.
  - **Kind** (one chip): `W` wasm · `N` native · `H` http · `R` remote
    (`kind == "remote_proxy"`, via the `kindBadge` helper). **`R` stands out by
    weight** (`.chip.filled`), never by hue.
  - **Visibility** → a chip only for the non-default narrowing: `private` (lock
    icon) or `local` (`L`); `public` shows nothing.
  - **Inactive** → the row is dimmed (`.action-row.inactive`) plus an ✗ icon
    chip.
  - **Needs account** → a filled key-icon chip (`.chip.filled`, `#key`) only
    when the action requires a per-caller grant the viewer hasn't connected.
    On the action page the run form is replaced by the connect gesture
    (banner + `Connect account`) — **a doomed run is never offered**, the
    same rule as peer-blocked runs.
  - **Discovered** → an unfilled `R` on a lookup result this kernel has not
    resolved — the same letter as a resolved remote, without the weight, because
    it is that action before resolution: the ref is kernel-qualified and the
    row carries no reputation strip, because nothing local backs it yet; its
    link resolves it (requirements.md §6.2). It DOES carry a price — the kernel prices every
    lookup row — rendered **indicative** (muted **plus** an accessible label,
    never weight alone, which a screen reader cannot perceive). It is the
    unresolved counterpart of `R`, so the two never appear together.
  - Every chip carries a `title` tooltip. **Chips are colorless** — no theme hue
    is spent on them (every hue is a reserved money/trust signal); emphasis is
    by weight (`.chip.filled`), never by hue.
- **Tables:** full width, hairline row borders, muted `th`. A navigable row is
  `.rowable` + `tabindex="-1"` + `data-href`: `j`/`k` move to it and `Enter`
  follows it. Its **link is the pointer target** — the row shows no pointer
  cursor, because no click handler stands behind it (and a row holds
  selectable text and nested links).
- **Money columns are right-aligned, header included** — a money `<th>` carries
  `class="money"` so the title sits over its right-aligned values.
- **Every table lives in a `.table-scroll`** (`overflow-x: auto`) so a wide
  table scrolls inside its own box and the page body never scrolls sideways.
- **Chat is list → detail, like everything else.** The chat index is a rooms
  table (`.rowable` rows, `tr.unread` weighting, unread `.badge`); a room is a
  detail page: crumb (← chat · room name · members · leave/delete) → the message
  stack (`.msgs`, `[data-autoscroll]`) in the document flow → the composer at the
  document's end. **The page is the scroll container** — no fixed-height frame, no
  inner scroll region. The document pins to its **newest** entry on open and on any
  append while already at the bottom — never yanking a reader scrolled up into
  history; the sticky crumb and the nav keep the way out always in reach.
- **Record rows** (the same record type wherever it appears) render through **one
  shared partial** — `_tx_row`, `_process_row` — so lists never drift apart.
  Row rules:
  - The **truncated id is the link** to the record (`/transactions/{id}`, …).
    No separate "open"/"view" link column.
  - **Every referenced entity is a link** to its page when a route exists — the
    action ref → the action, an admin handle → the user page. Refs with no route
    stay plain text (don't invent routes).
  - **Money is the signed, perspective amount** (§5.6): what the call did to the
    viewer's balance — `+`earned / `−`paid / `↩`refunded / `◷`parked — not raw
    gross/net/fee. `sys` sees `+fee` (its tax). Gross, fee and the full party
    list belong to the **detail** page, not the list.

### 5.6 Status, pills, money

- **Headline money figures are one component, `_figures`**: a `.stat-row` of
  `.stat` blocks — the label muted on top, the amount in mono coloured by its
  signal (`sig-credited` with a `+`, `sig-escrow`), and a quiet link to the
  records behind it when there are any. Wallet, home, workshop earnings and a
  session's funds all render through it; no page draws its own figures.

- `.pill` — bordered, `currentColor`. State variants:
  `.status-credited / -refunded / -escrow / -danger`.
- Money value + signal glyph: `+`/`↩`/`◷` via `.sig-credited / -refunded /
  -escrow`. Signals are text/glyph only, never fills.
- **Money display** renders mono + tabular through the single `money` helper,
  which renders the kernel's own way: the base-unit integer scaled by the world's
  `decimals`, trailing zeros trimmed to a **two-digit floor**, **no separators**,
  the world's `symbol` appended — `133 credits`, `1.50 USDC`, `0.000133 USDC`,
  `-2500 credits`. Separators are absent by design: the text pastes back into an
  amount field. A symbol the UI picks is a claim about a world it does not own,
  so `$`, `¤` and the retired gem/diamond mark are **banned from every template**,
  as is a hard-coded `step` on an amount input (`{{amountStep}}` reads the world's).
  A spend button names the amount the same way — `verb (N)` (§5.1) — and money in
  prose (help/flash/error) is written in the same form. Where the unit is not yet
  known, a money cell renders an em dash and no amount is accepted.

### 5.7 Refs

An action address, `owner@kernel/name`, renders via the **one** `_ref`
partial, built with the `ref` helper (`ref <address> <id>`; pass `""` for no
link): `<span class="ref mono">` with the owner in `.owner` (weight-emphasized,
confusable-distinct, **not** accent), split on the first `/`. Passing an action
id makes the **name** the link to the action. The owner links to its profile
(`/users/{address}`) only when the action is **local** — its kernel segment is
this kernel's own name (requirements.md §4); another kernel's owner renders
**unlinked**, since no local route exists behind it — don't invent one. A name
is rendered as served: never stripped of its kernel, never decorated.

When that kernel segment is a **raw public key** — an action discovered but not yet
resolved is `owner@<43 chars>/name` — it is **truncated** through the same
primitive a kernel card uses (§5.11), leaving the owner legible and the whole
segment in the `title`. A bound petname is not key-shaped and renders whole. A
43-character key MUST never be a card title — the action's title is.

Wherever an action is presented as a thing — a card, the action page's `<h1>`,
a workshop row, a peer's action list — its **title** leads and its ref follows
beneath it. Where a row only names the action a record is about (a receipt, a
task, a ledger entry), the ref alone stands: the kernel serves no title there.

### 5.8 Modals & floating forms

- **Every create/edit opens a floating modal:** native `<dialog class="modal-form">`.
  Trigger is a real link `href="?form=NAME" data-modal="form-NAME"`; JS opens it
  with `showModal()`. **No-JS:** the server renders the named dialog `[open]` as
  an in-flow card. `?form` values are whitelisted server-side.
- Structure: `.modal-head` (title + `.modal-close`) → `.modal-body` (the form).
- **A refused dialog comes back open:** a local parse error or the kernel's
  refusal re-renders the dialog `[open]` with the message as `_error` at the top
  of `.modal-body` and every typed value kept — except a secret, which never
  reaches a page: its input returns empty and `required`.
- **A nullable schema field** renders its typed input and a `label.check`
  checkbox `null` beneath it. Blank omits the field; the box sends `null`.
- **Destructive confirm:** `form[data-confirm="…"]` routes through the shared
  confirm modal — a native `dialog.modal-confirm` (`#modal-backdrop`) shown with
  `showModal()`, so it stacks in the top layer above an open `modal-form`. Its
  message is **one plain question** naming the act and its record ("Decline
  this task?", "Suspend bob@alpha?"), never a sentence explaining what follows;
  an amount that is data rides on the button (`End · refund 0.40 fUSD`).

### 5.9 Commands & keyboard

- **Commands are rows of the search pill.** The registry is **inert JSON**
  (`<script type="application/json" id="command-registry">`, CSP-safe), built
  server-side and **admin-gated** (admin commands only for `sys`). The pill
  lists matching commands before the title hints, each a `.menu-item.command`
  with the `›` mark (`.mark`) and its hint in `.muted.small`: a link, or for a
  post a `<button type="submit" form="command-post" formaction name value>`
  bound to the page's one hidden `#command-post` form, which carries the CSRF
  field. At most three commands beside titles, twelve rows in all; a leading
  `>` lists commands only. There is no palette, no ⌘K chip, no Ctrl/Cmd+K.
- Shortcuts: `g`+letter (section nav, `g h` home) · `/` (focus search) · `?`
  (help overlay, `dialog.palette`) · `j/k/Enter` (table and list rows) · `Esc`
  (close topmost) · `↓/↑` in the search box (move through hints and commands).
  Every command targets a real link/form — the keyboard layer adds no
  capability unreachable without it.

### 5.10 Feedback

| Element | Use |
|---|---|
| Flash bar | one-shot result of the last action, top of content |
| `.outcome` (`success`/`failed`/`rejected`) | a call's result panel; a success states no sentence — its styling says it — and carries the result as a frozen form (`_value_block`), an `hr.card-sep` between the result and its receipt and rating row |
| `.banner` (`-info`/`-error`) | inline notice |
| `.live-region` toast | **the one** polite `aria-live` region — ephemeral, auto-dismiss, **no history, no stack, no feed** |
| `.progress` | indeterminate bar for long calls (never an indefinite spinner) |
| `.badge` | round, accent — an async attention count. In the **core** surface, **only** the `inbox` nav carries it (the sole core async signal). An **app** MAY carry its own **app-scoped** badge on its nav entry (e.g. chat's unread count) — a deliberate exception like the `peers` slot, scoped to the app, leaving the core rule intact. **Every** nav badge is the `_badge_poll` partial: htmx re-fetches its endpoint on the **one** configured interval (`badge_interval_seconds`, default 30s), body = bare count, **empty when zero** (`.badge:empty` hides it). Rendered identically in the sidebar and the mobile tab bar |
| `.unread` | row modifier for a list row with unseen activity (e.g. a chat room): the primary-cell link is weighted (`tr.unread > td:first-child a`) so the row reads as "new", complementing the count badge |

### 5.11 Federation (network)

`admin` (users, money, access) and `network` (federation) are **two separate
top-level nav entries** (`sys` only), not one page with a sub-nav — federation
is a distinct concern. The `network` page leads with **this kernel's money**
through `_figures` (§5.6), then the obligations, then **one list** of every
known kernel in the action row's grammar (§5.5): **this kernel**, then its
**counterparties**, then the **known network**, each class in the kernel's
order, paged and never polled. A class is a neutral `.pill` on the title line —
`this kernel`, `party`; a known kernel carries no mark. There is **no
subscribe/unsubscribe verb**; the one opener is **inspect a kernel by key**,
beside the Show/Hide-suspended toggle.

A peer's row is `.rowable` and opens its page (`/admin/peers/{key}`), where it
is named and suspended (suspension freezes the kernel row, so every kernel's
page offers it); a suspended
counterparty is dimmed (`.action-row.inactive`) and keeps its `suspended`
danger pill. This kernel's row is titled with its own handle and links nowhere,
because it is this page. A named row carries **no verb**, as a result row
carries none; a row titled by its key carries the one verb, `Name` — the same
`.btn` as on the kernel's page — at the end of its title line (the price's
slot, `margin-left: auto`), linking into its page with the rename modal open:
the verb appears exactly where the gap it fills is visible.

**The key has one spelling**: `keyDisplay` (8 characters and `…`, the whole
key in `title`). An unbound row's title is that spelling and the row has no
ref line; a named row's `.action-ref` is that spelling. `truncate` never
touches a key.

**The kernel strip** (`_kernel_strip`, `KernelStrip`) ends every peer row, in
the `_stat_strip` grammar: icon and value per slot, the meaning in `title`,
`·` between slots, a muted `none` where a slot has nothing. This kernel's row
carries none — the roster states nothing about it — and the kernel's page
carries none: a strip is for comparing rows, and a page states its facts with
labels.

| slot | icon | value |
|---|---|---|
| catalog | `#layers` | public actions in its catalog |
| verified | `#clock` | `last_seen`, relative |
| failed | `#x-mark` | `last_contact_failed_at`, relative |

The strip keeps the action strip's flex rule: its guarantee is fixed slot
order and fixed icons, not column alignment, and a grid variant would be a
second layout of one component.

**Naming is a petname system** (kernel §13, after Stiegler), and the three name
classes render differently because they are trusted differently:

| class | what it is | renders |
|---|---|---|
| **key** | global, self-certifying, unreadable | truncated (`esKD_l0c…`), full value in `title`; the only thing URLs and forms key on |
| **nickname** | the kernel's claim about itself; never resolves | counterparty content (§6): inert, never linked, shown as a claim |
| **petname** | bound here, unique here, resolves | whole, in the provenance face |

An unbound kernel therefore shows its truncated key, never its nickname. The
**one** rename modal (§5.8) lives on the kernel's page, keyed on the public key
— an unbound kernel has no name to key on, and advertised names collide. Its
field opens prefilled by the kernel's auto-bind preference: the petname when
bound, else the nickname or `k-<key8>`, suffixed `2`, `3`, … past every name
held here (requirements §6.6).

**Inspect** (`/admin/peers/{key}`) opens with the row's header in a
`.page-head` — name with its marks, key, `about` — then one `.card.stack`
holding the polled `kv` of labelled, present facts (Status; Version without
the agent prefix; Catalog, the roster's count; Self-reported; Known since, a
date from the kernel row; no `none`, no dash, no protocol strings) and a
`.cluster` of buttons, `Name` (`.btn`) and `Suspend` or `Unsuspend`,
as the user page's card does; then the kernel's **public actions** as
**discovered `_action_row`s** (§5.5 — a remote action before resolution, its
link resolving it), and **evidence** in **two blocks**, in the same
grouped-block grammar as the action stats table: the provider's own **report**
("reported by the provider"), then **counterparty experience** per issuer with
its corroborated count. Each row names its subject action and states
uses/failures/contradicted with the two rating counts as text —
`N trade-backed · M unverified`. The **one `_evidence` partial** also renders
what other kernels report on an **action's page**, below its ratings, the
provider's block first. Evidence carries **no colour and no fill**: it is a
trust fact, not a status, and never joins the first-party reputation strip.

**Obligations** sit between this kernel's figures and the list: every cross-kernel
debt settles per call on the rail by a ticket both kernels name, so each row is
one debt — the debtor, the ticket, what was owed, what the draw says must be
paid — and one gesture: a `.btn-money` `Confirm (N)` behind `data-confirm`,
posting the peer's key and the ticket id. Recording a payment moves real money.

**Rename** is one modal per namespace. A user is renamed on the admin user
detail (`/admin/users/{address}?form=admin-rename`): a `.btn` opener beside
Suspend, one field taking the new address, a neutral `.btn` submit. A kernel is
named on its inspect page the same way, keyed on its public key. The two are
separate because the kernel's two routes are.

A counterparty carries **no money at all**: a peer account is identity and
moderation state, never a wallet (kernel §D14), so a row states its names and
its strip. The money on this page is this kernel's
own — exposure against its credit limit, the rates, the operator split, solvency,
and a rail halt as a `.banner-error` — plus the obligations above.

### 5.12 Menus & icons

- `.menu` floating panel via `[data-menu]`; `.menu-item` rows; used by the
  `+ new …` openers, row-lifecycle `⋯` menus, and the `you` hub.
- One SVG sprite (`icons.svg`), recolored via `currentColor`, **never emoji**.
  Every icon-only control carries an `aria-label`. The brand uses
  `juice-logo.svg`.
- **The copy control** is the `_copy` partial, built with `copyOf <what> <value>`:
  a `.btn-quiet.copy` icon button whose `data-copy` carries the **whole**
  value — a key, a hash, an address, the recovery phrase — beside the
  truncated or prose rendering of it, labelled `Copy <what>`. It is the one
  way a page yields a value the commands take whole (requirements.md §7);
  "Copied" is announced through the live region, never on the button. A
  record id gets none, since the kernel resolves a prefix.

### 5.13 Media slot

- `.media` is a **16:9** slot on the action-page hero; `.media.tile` is the
  **square** crop of the same image that leads a list row (48px, 40px on a
  phone). The art slices to any box, so one generator serves both. It renders, in order of precedence: a **kernel image** (Conditional — no field yet,
  `ROADMAP.md`), else the **`cardArt` fallback** — a deterministic inline SVG
  seeded from the action's id/owner/kind, colored from tokens
  (`var(--surface-2)`/`var(--accent)`), **presentation attributes only** (no
  `style=`), no external fetch (CSP-safe).
- The media is **counterparty content**: it is inert (§6), never carries chrome,
  and kernel-captured facts (ref, price, reputation, any money control) render
  **outside** it (the media is in the hero; the run form and its money control are
  a separate section below).

---

## 6. Invariants (enforced)

- No inline `style=""`; tokens only; every decision is a named class.
- **Accent = money movement + focus only.** Concretely, the accent appears ONLY
  on `.btn-money`, the `.badge`, the focus ring, `.req`, and the progress bar —
  never on links, tab underlines, active nav, or the active mobile tab (those key
  on text weight + surface). Money buttons name the amount as `verb (N)` and carry
  **no credit glyph**; money *display* uses the same scaled form in the mono
  provenance face (§5.6). **Danger beats money.** An **ordinary task completion is
  neutral** (`Complete`, no amount, no accent): the completer is not the payer;
  a **payment task** completion is a genuine spend and uses the money button
  naming the amount (§5.1, requirements.md §6.3).
- Counterparty content (names, descriptions, args, results, reasons) renders
  **inert**: escaped, no chrome, no accent; a JSON value only through the frozen
  form (`_frozen` / `_value_block`), which is structure by JSON type and
  interprets nothing. Trust affordances key on kernel-captured fields, never
  payload shape.
- No spend reachable from a list view or a GET; money moves only through a
  confirm that names the amount.
- Record lists share one row partial per record type; the truncated id is the
  link to the record; referenced entities link to their pages; list money is the
  signed perspective amount (gross/fee/parties live on the detail page). Every
  `<table>` sits in a `.table-scroll`.
- Sentence-case titles, headings and nav labels (`Home · Inbox · Workshop ·
  Wallet · Settings · Network · Admin`); `page-head` (the title only) on index, sticky crumb on detail.
  Commands are quiet and inline — never a floating page-head cluster (sticky is
  the page's own header refusing to scroll away, not floating chrome).
- **Copy is rationed (§3 Copy discipline):** no orientation subtitles; no
  sentence narrates kernel/control behavior; disclosures appear once, at the point
  of consequence. Empty states and `help` name the thing, they don't lecture. A
  field caption never restates its label or explains a mechanism. The drift guard
  lints **app** templates (`apps/*/templates/`) for this, identically to the core.
- One `aria-live` region; the task badge (on `inbox`) is the only async attention
  signal in the core surface (an app MAY add its own app-scoped nav badge, e.g.
  chat unread); no notification feed/toast stack.
- Visible focus; full keyboard operability; `prefers-reduced-motion` respected;
  WCAG AA (theme test).
- CSP: no inline script; the command registry is inert JSON; `Referrer-Policy:
  same-origin` (never `no-referrer` — it nulls the Origin and breaks CSRF).
- Floating forms degrade to the `?form=` no-JS path.
- **Closed component set** (§0 registry): new UI composes existing components;
  adding one means updating the registry **and** the drift guard. Color is
  money/danger only — no neutral-emphasis button tier. All view switching is
  `.tabs` (§5.2), never buttons; the retired `.linkbar` must not reappear. The
  `page-head` carries no command cluster (§5.3).
- **Drift guard:** a test scans templates and fails on any button class outside
  §5.1 (incl. the retired `.btn-primary`), any inline `style=`, any `.toolbar`,
  any `.linkbar`, the superseded `auth-tab` class,
  any `<table>` not wrapped in a `.table-scroll`, any `.subtitle` holding prose
  (only an identity or a scope note), any prose note (`.help` / `.card-note`) that
  swells into a paragraph, and any **`¤` character, `$` before a figure, or
  hard-coded `step` on an amount input** (`TestMoneyGrammar`), any `{{json …}}`
  outside `_value_block` (`TestJSONRendersThroughTheFrozenForm`), and the retired
  `data-param-*` row attributes — money is the
  world's unit and symbol through the `money` helper, the step is the world's
  through `{{amountStep}}`, and buttons read `verb (N)` (§5.6). `TestNoRetiredSurfaces` fails on the
  vocabulary the kernel retired: settle, unimport, the transfer queue, admin
  withdraw, a peer's balance.
