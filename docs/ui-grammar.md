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
- **Content-first, calm.** Title → content. Commands live quietly *with* the
  content they act on (never a floating page-head cluster); color appears only at
  the real money/danger moment. Hierarchy comes from position and whitespace, not
  from filled buttons.

**Component registry** (the whole vocabulary):

| Component | Class(es) | Role |
|---|---|---|
| Button | `.btn` (the one neutral) · `.btn-money` · `.btn-danger` · `.btn-icon` · `.btn-quiet` | actions |
| **Tabs** | `.tabs` + `.tab` | **the** view switch — login sign in/create toggle *and* in-content list filters |
| Tab bar | `.tab-bar` + `.tab` | the fixed mobile bottom nav |
| Menu | `.menu` + `.menu-item` (`[data-menu]` trigger) | dropdowns & popovers, incl. quiet "+ new …" |
| Card / grid / table | `.card` · `.grid` · `.card-list` · `table` (`.rowable`, `.stats-table`) | containers & data |
| Field | `.field` + inputs/select/textarea | forms |
| Dialog | `dialog.modal-form` · `#modal-backdrop` confirm · `dialog.palette` | overlays |
| Status | `.pill` (+state) · `.sig-*` money signals | trust signals |
| Feedback | flash · `.outcome` · `.banner` · `.live-region` · `.progress` · `.badge` | results/notice |
| Ref | `_ref` partial | `@owner/name` |

---

## 1. Canvas

The frame every page renders into. Defined in `layout.html`.

```
┌────────┬─────────────────────────────────────────┐
│ SIDEBAR│  flash bar (one-shot, when present)       │
│ brand  ├─────────────────────────────────────────┤
│ ◆ juice│  ┌── page-head ───────────────────────┐  │
│        │  │ title + subtitle                   │  │
│ ▸ run  │  └────────────────────────────────────┘  │
│ ▸ action  │  content (container, max 1000px)      │
│ ▸ process │                                        │
│ ▸ tx   │                                           │
│ ▸ step•│  (• = badge)                              │
│ ▸ user │                                           │
│ ▸ admin│  (admin: @sys only)                       │
│        │                                           │
│ ⌂ @you │  ← profile popover                        │
└────────┴─────────────────────────────────────────┘
```

| Region | Contents | Rules |
|---|---|---|
| **Sidebar** | brand → `/run`; collapse toggle; nav groups; profile popover at foot | Sticky, full height. Collapsible to an icon rail; state persisted server-side (`sidebar` cookie), stamped on `<html data-sidebar>` — no flash. |
| **Nav** | `run · action · process · tx · step · user` + `admin · peers` (only `@sys`) | Mirrors the CLI command groups — with **`peers` elevated to its own slot** (a deliberate exception: federation is distinct enough to warrant it). Active group carries `aria-current="page"`. `step` carries the badge. Icon + lowercase label; label hidden when collapsed. |
| **Profile popover** | handle, balance, theme picker, Sign out — **no navigation** | Opens from the sidebar foot (`[data-menu]`). Nav destinations live in the sidebar, not here (the mobile drawer, which replaces the sidebar, keeps them). Theme picker is a `<select>` of the shipped themes + "System default". |
| **Main area** | flash bar, then the content container | Container: `max-width 1000px`, centered, padding `1.25rem 1.5rem 4rem`. |
| **page-head** | title + subtitle only — no commands (§3, §5.3) | Present on every **index** page. Commands sit inline with their content. |
| **Flash bar** | one transient message | One-shot cookie, top of content; `role=alert` (error) / `status`. Not a feed. |
| **Mobile** (`≤720px`) | sidebar → fixed bottom **tab bar** (`run · action · step• · tx · user`); top hamburger → **drawer** (Account, theme, Sign out, and **Admin + Peers when `@sys`**) | Same layout reflowed. The badge stays on `step`. Admin/Peers MUST remain reachable in the drawer for `@sys` (no sidebar there). |

**Entry screen** (unauthenticated): no sidebar — a single centered, branded auth
card (`◆ juice`) that **names the kernel you're joining** ("Sign in to
`@handle`") with a short **key fingerprint** for pre-auth verification, then the
**Sign in / Create account** toggle (the `.tabs` component, §5.2, its one
retained use), one action per tab. The kernel handle + key come from the open
`GET /health`; the sidebar brand shows the same handle once signed in (kernel at
the top, your own `@handle` at the profile foot). Tabs are real `/login?tab=`
links (server renders the active tab with JS off); app.js swaps panels
client-side.

**Layer order** (all native `<dialog>` except the menu/flash; `Esc` closes the
topmost): content < flash < menu/popover < modal form & confirm modal < command
palette / help overlay < aria-live toast.

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
| `--radius` (`2px`) / `--shadow` (`none`) / `--font-mono` (= `--font-ui`) | structural (theme-independent, `tokens.css`) |

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

- **Face:** the system monospace stack, everywhere (terminal identity).
- **Scale:** `h1 1.4rem` · `h2 1.1rem` · `h3 1rem` · body `.9rem/14px` ·
  `.small .85rem` · `.tiny .8rem`. Headings carry slight `letter-spacing`.
- **Titles are lowercase CLI nouns** on **both** index and detail pages:
  `run · action · process · tx · step · user · admin · peers`. A **detail** page
  adds a `← group` back-link above the title and shows the resource id/name.
- **Money, IDs, hashes, `@owner/name`:** mono + `tabular-nums`. The **owner
  segment** of a ref is weight-emphasized (`.owner`) and MUST NOT use accent.
- **Index vs detail:**
  - *Index* = `page-head` (title + subtitle) → section `<h2>` → a `.tabs` filter
    row (with an optional inline `+ new …` command) → list.
  - *Detail* = back-link → `<h1>` → `.subtitle` (the id) → cards.

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
| `.grid` gap (auto-fill `minmax(260px,1fr)`) | `.85` |

Compose with `.stack` / `.row` / `.cluster` / `.grid`; never hand-set margins in
templates.

---

## 5. Components

### 5.1 Buttons — one neutral, plus money & danger

All buttons share **one geometry** (padding, height, radius, font-size).
**Color is reserved for the real money/danger moment**; everything else is the
single neutral `.btn`, so two ordinary buttons never read as "different types."

| Class | Tier | Fill | Use |
|---|---|---|---|
| `.btn` | **the one neutral** | transparent + border, weight 500 | every ordinary action & form submit: create, save, import, unimport, complete (unpaid), update, sign in, search, befriend, inspect, navigate, add-row |
| `.btn-money` | **money movement** (solid accent) | `--accent` / `--accent-contrast`, weight 600 | run/pay, synthesize, deposit, withdraw — **MUST name the amount** (e.g. `Pay 12 ¤ & run`, `Synthesize · 20 ¤`) |
| `.btn-danger` | **critical** (solid danger) | `--danger` / `--danger-contrast`, weight 600 | delete, suspend, unfriend, force-end |
| `.btn-icon` | icon-only | — | icon buttons; **MUST carry `aria-label`** |
| `.btn-quiet` | inline text button | — | Cancel, remove-row, and the quiet inline commands (`+ new action`, `update account`, `+ deposit credits`) |

**Rules.** These are the *only* button styles. The money tier is the **authorizing
control** — the form submit that actually moves credits (`Pay 12 ¤ & run`,
`Synthesize · 20 ¤`, Deposit/Withdraw, `Complete · 5 ¤`). An *opener* that merely
opens such a form is a quiet `.btn-quiet`, not a colored button — the money color
appears on the confirm inside the modal. **Danger beats money** (an op that is
both destructive and monetary is danger — e.g. force-end). Money buttons state
the amount when it is fixed/known; when the operator types the amount
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
| **List filters** | `tab` in `.tabs.control-tabs` — action `all/active/inactive`, step `all/done/cancelled` (real query-string links, reload) | selected filter |

**Distinct axes get distinct rows, never mixed.** The actions page's *ownership
scope* (`yours | everyone`, `@sys`) is its **own** switch on the heading, not a
peer of the state filters — mixing them let two tabs on different variables both
light up. Each axis preserves the other's value (`actionsHref`).

A filter row may carry **one** inline command (`+ new action`) as a
`.btn-quiet.tabs-action`, right-aligned and not styled as a tab. The fixed mobile
bottom nav is a separate component (`.tab-bar`). The `.linkbar` is retired.

### 5.3 Commands are inline & quiet

The `page-head` carries **no commands** — just the title and subtitle. A command
lives **inline, with the content it acts on**, as a single quiet affordance:
`+ new action` above the actions list, `update account` by the identity card,
`+ deposit credits` in the users section. All are `.btn-quiet` (or a plain link);
none float, none are colored.

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
- **Progressive widgets:** repeatable rows (`[data-param-rows]`/`[data-param-row]`),
  scheme reveal (`[data-auth-scheme]`), kind reveal (`[data-http-only]`) — all
  disable hidden inputs so they aren't submitted. Secrets are masked, write-only,
  never pre-filled.

### 5.5 Cards, grids, tables

- `.card` (surface, border, radius, pad); `.action-card` for catalog items;
  `.grid` for card collections; `.card-list` for stacked cards.
- **Action card:** the **ref is the title** (`_ref`, owner emphasized) and the
  link to the action — never the bare name or the raw kind string. **Attribute
  chips** (`.chip`) sit at the head, right-aligned, marking the *exception*:
  - **Kind** (always, one chip): `W` wasm · `N` native · `H` http · `R` remote
    (`kind == "remote_proxy"`, via the `kindBadge` helper). **`R` stands out by
    weight** (`.chip.filled`), never by hue.
  - **Private** → a lock chip (`#lock`); public shows nothing.
  - **Inactive** → the card is dimmed (`.action-card.inactive`) plus a muted
    `inactive` chip (`.chip.tag`).
  - Every chip carries a `title` tooltip. **Chips are colorless** — no theme hue
    is spent on them (every hue is a reserved money/trust signal).
- **Tables:** full width, hairline row borders, muted `th`. A clickable row is
  `.rowable` + `tabindex="-1"` + `data-href`; navigable with `j`/`k`/`Enter`.
- **Money columns are right-aligned, header included** — a money `<th>` carries
  `class="money"` so the title sits over its right-aligned values.
- **Every table lives in a `.table-scroll`** (`overflow-x: auto`) so a wide
  table scrolls inside its own box and the page body never scrolls sideways.
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
    gross/net/fee. `@sys` sees `+fee` (its tax). Gross, fee and the full party
    list belong to the **detail** page, not the list.

### 5.6 Status, pills, money

- `.pill` — square, bordered, `currentColor`. State variants:
  `.status-credited / -refunded / -escrow / -danger`.
- Money value + signal glyph: `+`/`↩`/`◷` via `.sig-credited / -refunded /
  -escrow`. Signals are text/glyph only, never fills.

### 5.7 Refs

`@owner/name` renders via the `_ref` partial: `<span class="ref mono">` with the
owner in `.owner` (weight-emphasized, confusable-distinct, **not** accent),
split on the first `/`.

### 5.8 Modals & floating forms

- **Every create/edit opens a floating modal:** native `<dialog class="modal-form">`.
  Trigger is a real link `href="?form=NAME" data-modal="form-NAME"`; JS opens it
  with `showModal()`. **No-JS:** the server renders the named dialog `[open]` as
  an in-flow card. `?form` values are whitelisted server-side.
- Structure: `.modal-head` (title + `.modal-close`) → `.modal-body` (the form).
- **Destructive confirm:** `form[data-confirm="…"]` routes through the shared
  confirm modal (`#modal-backdrop`), whose message names the consequence.

### 5.9 Command palette & keyboard

- **Ctrl/Cmd+K** opens the palette (`<dialog class="palette">`). Its registry is
  **inert JSON** (`<script type="application/json">`, CSP-safe), built
  server-side and **admin-gated** (admin commands only for `@sys`).
- Shortcuts: `g`+letter (section nav) · `/` (focus search) · `?` (help overlay) ·
  `j/k/Enter` (rows) · `Esc` (close topmost). Every command targets a real
  link/form — the keyboard layer adds no capability unreachable without it.

### 5.10 Feedback

| Element | Use |
|---|---|
| Flash bar | one-shot result of the last action, top of content |
| `.outcome` (`success`/`failed`/`rejected`) | a call's result panel |
| `.banner` (`-info`/`-error`) | inline notice |
| `.live-region` toast | **the one** polite `aria-live` region — ephemeral, auto-dismiss, **no history, no stack, no feed** |
| `.progress` | indeterminate bar for long calls (never an indefinite spinner) |
| `.badge` | round, accent, **only** on the `step` nav — the sole async attention signal |

### 5.11 Federation (peers)

`admin` (users, money, access) and `peers` (federation) are **two separate
top-level nav entries** (`@sys` only), not one page with a sub-nav — federation
is a distinct concern. `peers` owns the **this kernel** identity card. The
`peers` page is a discovery → inspect → trust journey, all in the table grammar:
**this kernel** (identity to share) ·
**friends** (direct peers, each
with Inspect + Unfriend) · **known network** (the gossip roster: one block per
discovered kernel showing *your* earned stats — labeled `you` — distinct from the
kernel's `self-reported` manifest and hearsay grouped `via <introducer>`; each has
Inspect + Befriend). **Inspect** (`/admin/peers/{key}`) shows reachability
(direct/relayed + rtt), public actions, and transacted friends. Roster requires
`?gossip=1` from the kernel (not `true`).

### 5.12 Menus & icons

- `.menu` floating panel via `[data-menu]`; `.menu-item` rows; used by the
  profile popover and mobile drawer.
- One SVG sprite (`icons.svg`), recolored via `currentColor`, **never emoji**.
  Every icon-only control carries an `aria-label`.

---

## 6. Invariants (enforced)

- No inline `style=""`; tokens only; every decision is a named class.
- **Accent = money movement + focus only.** Money buttons name the amount.
  **Danger beats money.**
- Counterparty content (names, descriptions, args, results, reasons) renders
  **inert**: escaped, no chrome, no accent, JSON in a code block. Trust
  affordances key on kernel-captured fields, never payload shape.
- No spend reachable from a list view or a GET; money moves only through a
  confirm that names the amount.
- Record lists share one row partial per record type; the truncated id is the
  link to the record; referenced entities link to their pages; list money is the
  signed perspective amount (gross/fee/parties live on the detail page). Every
  `<table>` sits in a `.table-scroll`.
- Lowercase CLI titles; `page-head` (title + subtitle only) on index, back-link
  on detail. Commands are quiet and inline — never a floating page-head cluster.
- One `aria-live` region; the `step` badge is the only async attention signal;
  no notification feed/toast stack.
- Visible focus; full keyboard operability; `prefers-reduced-motion` respected;
  WCAG AA (theme test).
- CSP: no inline script; palette registry is inert JSON; `Referrer-Policy:
  same-origin` (never `no-referrer` — it nulls the Origin and breaks CSRF).
- Floating forms degrade to the `?form=` no-JS path.
- **Closed component set** (§0 registry): new UI composes existing components;
  adding one means updating the registry **and** the drift guard. Color is
  money/danger only — no neutral-emphasis button tier. All view switching is
  `.tabs` (§5.2), never buttons; the retired `.linkbar` must not reappear. The
  `page-head` carries no command cluster (§5.3).
- **Drift guard:** a test scans templates and fails on any button class outside
  §5.1 (incl. the retired `.btn-primary`), any inline `style=`, any `.toolbar`,
  any `.linkbar`, the superseded `auth-tab` class, and
  any `<table>` not wrapped in a `.table-scroll`.
