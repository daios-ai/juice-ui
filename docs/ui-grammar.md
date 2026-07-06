# Juice UI — Visual Grammar

The single source of truth for the interface. Every screen is built from the
canvas and components below; nothing is styled ad hoc. **Rules are normative
(MUST).** Code conforms to this document, not the other way round.

Two laws underpin everything:
- **No inline styles.** Every visual decision is a named class in `app.css`.
- **Tokens only.** Components reference semantic CSS custom properties (§2),
  never raw colors or sizes.

---

## 1. Canvas

The frame every page renders into. Defined in `layout.html`.

```
┌────────┬─────────────────────────────────────────┐
│ SIDEBAR│  flash bar (one-shot, when present)       │
│ brand  ├─────────────────────────────────────────┤
│ ◆ juice│  ┌── page-head ───────────────────────┐  │
│        │  │ title + subtitle      [ toolbar ]  │  │
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
| **Nav** | `run · action · process · tx · step · user` + `admin` (only `@sys`) | Mirrors the CLI command groups. Active group carries `aria-current="page"`. `step` carries the badge. Icon + lowercase label; label hidden when collapsed. |
| **Profile popover** | handle, balance, theme picker, Account, Sign out | Opens from the sidebar foot (`[data-menu]`). The theme picker is a `<select>` of the shipped themes + "System default". |
| **Main area** | flash bar, then the content container | Container: `max-width 1000px`, centered, padding `1.25rem 1.5rem 4rem`. |
| **page-head** | title block (left) + toolbar `.cluster` (right) | Present on every **index** page (§3). |
| **Flash bar** | one transient message | One-shot cookie, top of content; `role=alert` (error) / `status`. Not a feed. |
| **Mobile** (`≤720px`) | sidebar → fixed bottom **tab bar** (`run · action · step• · tx · user`); top hamburger → **drawer** (theme, Account, Sign out, and **Admin when `@sys`**) | Same layout reflowed. The badge stays on `step`. Admin MUST remain reachable in the drawer for `@sys`. |

**Entry screen** (unauthenticated): no sidebar — a single centered, branded auth
card (`◆ juice`) with a **Sign in / Create account** tab toggle and one primary
action per tab. Tabs are real `/login?tab=` links (server renders the active tab
with JS off); app.js swaps panels client-side.

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
  - *Index* = `page-head` (title + subtitle left, toolbar right) → filters → list.
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

### 5.1 Buttons — one geometry, three tiers + helpers

All buttons share **one geometry** (padding, height, radius, font-size); only
fill/weight-of-hue changes, so siblings never read as "different types."

| Class | Tier | Fill | Use |
|---|---|---|---|
| `.btn` | **secondary** (neutral, ghost) | transparent + border | cancel, filters, lifecycle toggles (enable/disable, public/private), inspect (read), navigate, add-row |
| `.btn-primary` | **normal op** (solid neutral) | `--surface-2` + strong border, weight 600 | the main non-money action: create, save, import, unimport, complete, update account, create step, sign in |
| `.btn-money` | **money movement** (solid accent) | `--accent` / `--accent-contrast`, weight 600 | run/pay, synthesize, deposit, withdraw — **MUST name the amount** (e.g. `Pay 12 ¤ & run`, `Synthesize · 20 ¤`) |
| `.btn-danger` | **critical** (solid danger) | `--danger` / `--danger-contrast`, weight 600 | delete, suspend, unfriend, force-end |
| `.btn-icon` | icon-only | — | icon buttons; **MUST carry `aria-label`** |
| `.btn-quiet` | inline text button | — | tertiary inline (e.g. remove param row) |

**Rules.** These are the *only* button styles. The money tier is the **authorizing
control** — the form submit that actually moves credits (`Pay 12 ¤ & run`,
`Synthesize · 20 ¤`, Deposit/Withdraw, `Complete · 5 ¤`). A toolbar *opener* that
merely opens such a form is a normal op (`.btn-primary`) — so Create / Synthesize
/ Import / Unimport read uniformly; the money color appears on the confirm inside.
**Danger beats money** (an op that is both destructive and monetary is danger —
e.g. force-end). Money buttons state the amount when it is fixed/known; when the
operator types the amount (deposit/withdraw) the adjacent field is the amount.
Destructive buttons that can't be undone use `data-confirm` (§5.6).

### 5.2 Forms

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

### 5.3 Cards, grids, tables

- `.card` (surface, border, radius, pad); `.action-card` for catalog items;
  `.grid` for card collections; `.card-list` for stacked cards.
- **Tables:** full width, hairline row borders, muted `th`. A clickable row is
  `.rowable` + `tabindex="-1"` + `data-href`; navigable with `j`/`k`/`Enter`.

### 5.4 Status, pills, money

- `.pill` — square, bordered, `currentColor`. State variants:
  `.status-credited / -refunded / -escrow / -danger`.
- Money value + signal glyph: `+`/`↩`/`◷` via `.sig-credited / -refunded /
  -escrow`. Signals are text/glyph only, never fills.

### 5.5 Refs

`@owner/name` renders via the `_ref` partial: `<span class="ref mono">` with the
owner in `.owner` (weight-emphasized, confusable-distinct, **not** accent),
split on the first `/`.

### 5.6 Modals & floating forms

- **Every create/edit opens a floating modal:** native `<dialog class="modal-form">`.
  Trigger is a real link `href="?form=NAME" data-modal="form-NAME"`; JS opens it
  with `showModal()`. **No-JS:** the server renders the named dialog `[open]` as
  an in-flow card. `?form` values are whitelisted server-side.
- Structure: `.modal-head` (title + `.modal-close`) → `.modal-body` (the form).
- **Destructive confirm:** `form[data-confirm="…"]` routes through the shared
  confirm modal (`#modal-backdrop`), whose message names the consequence.

### 5.7 Command palette & keyboard

- **Ctrl/Cmd+K** opens the palette (`<dialog class="palette">`). Its registry is
  **inert JSON** (`<script type="application/json">`, CSP-safe), built
  server-side and **admin-gated** (admin commands only for `@sys`).
- Shortcuts: `g`+letter (section nav) · `/` (focus search) · `?` (help overlay) ·
  `j/k/Enter` (rows) · `Esc` (close topmost). Every command targets a real
  link/form — the keyboard layer adds no capability unreachable without it.

### 5.8 Feedback

| Element | Use |
|---|---|
| Flash bar | one-shot result of the last action, top of content |
| `.outcome` (`success`/`failed`/`rejected`) | a call's result panel |
| `.banner` (`-info`/`-error`) | inline notice |
| `.live-region` toast | **the one** polite `aria-live` region — ephemeral, auto-dismiss, **no history, no stack, no feed** |
| `.progress` | indeterminate bar for long calls (never an indefinite spinner) |
| `.badge` | round, accent, **only** on the `step` nav — the sole async attention signal |

### 5.9 Federation (admin)

The `peers` page is a discovery → inspect → trust journey, all in the table
grammar: **this kernel** (identity to share) · **friends** (direct peers, each
with Inspect + Unfriend) · **known network** (the gossip roster: one block per
discovered kernel showing *your* earned stats — labeled `you` — distinct from the
kernel's `self-reported` manifest and hearsay grouped `via <introducer>`; each has
Inspect + Befriend). **Inspect** (`/admin/peers/{key}`) shows reachability
(direct/relayed + rtt), public actions, and transacted friends. Roster requires
`?gossip=1` from the kernel (not `true`).

### 5.10 Menus & icons

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
- Lowercase CLI titles; `page-head` on index, back-link on detail.
- One `aria-live` region; the `step` badge is the only async attention signal;
  no notification feed/toast stack.
- Visible focus; full keyboard operability; `prefers-reduced-motion` respected;
  WCAG AA (theme test).
- CSP: no inline script; palette registry is inert JSON; `Referrer-Policy:
  same-origin` (never `no-referrer` — it nulls the Origin and breaks CSRF).
- Floating forms degrade to the `?form=` no-JS path.
- **Drift guard:** a test scans templates and fails on any button class outside
  §5.1 or any inline `style=`.
