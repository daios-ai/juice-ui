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
| Button | `.btn` (the one neutral) · `.btn-money` · `.btn-danger` · `.btn-icon` · `.btn-send` (the composer's alone) | actions |
| **Tabs** | `.tabs` + `.tab` | **the** view switch — login sign in/create toggle *and* in-content list filters |
| Menu | `.menu` + `.menu-item` (`[data-menu]` trigger) | dropdowns & popovers, incl. "+ new …" |
| Card / list / table | `.card` · `.card-list` · `.action-list` + `.action-row` · `table` (`.rowable`, `.stats-table`) | containers & data |
| **Media** | `.media` (16:9 slot) · `.media.tile` (square) · `_media` / `_tile` partials · `cardArt` fallback | the action image (§5.13) |
| **Figures** | `.stat-row` + `.stat` (label · value) · `_figures` partial | **the** headline money figures, one block each — label, mono amount coloured by its signal. One component on the wallet and a session |
| **Pager** | `.pager` + `_pager` partial | quiet offset pagination under any long list (20/page) |
| **Crumb** | `.crumb` (sticky `<nav>`) | a detail page's context row: back-link + record identity + its inline commands. Sticky — *the way up never scrolls away* (self-gating on short pages; the page's own header, not floating chrome) |
| **Rail** | `.rail` (`_rail`, the conversations app's) | the sidebar's conversations: `+ New conversation`, then the rows, every one a `.rail-row` of its name and its menu (§5.12): first each chat asking to be connected and each invitation, weighted (`.unread`); then the rooms newest activity first, each its link and its menu trigger, the room in view `aria-current`, an unread one weighted, one held on another kernel followed by that kernel, muted (`far · alpha`); then each chat that could not be read, muted, `#alert` before its address, its reason the row's `title` and a `.sr-only` clause. A row that is not a room is one `.rail-item` button opening its menu, its `#ellipsis` where a room's trigger stands, so no part of it is a dead click. A command that may wait on another kernel (`hx-indicator` on its row, `hx-disabled-elt` on its button) dims the row (`.rail-row.htmx-request`) and takes no second click until it answers; its failure is a notice on the page it was sent from |
| **Composer** | `.composer` + `.composer-bar` (`room.html` alone) | **the one sanctioned exception** to the form's card: the post form drawn as a bar — `/`, `@`, the message control (`_control`, the schema's own), the send arrow (`.btn-icon`, `#arrow-up`) — a refusal above, `_balance` beneath. A drift guard keeps it in the room |
| **Launcher** | `.launcher` + `[data-launcher]` (`_launcher`, `launcher` helper; `suggestions` in app.js) | **the** list of suggestions beside a text input, the best match nearest it; ↑/↓ walk the rows, Escape empties, Enter on a row follows it. Over the composer, its `/` — the query is the composer's own text after it: the catalog's actions by title or address, the registry's pages, a top `Search "…"` row naming the search's price; Enter submits the search (§5.9); and its addresses. Under Discover's `.search` box: the catalog's actions alone. Rows `.menu-item`, matched in the browser |
| **Panel** | `.panel` (`_action_panel`, `_search_panel`, `_panel_error`) within `.room` beside `.conversation` | an action or a search's results beside a conversation, one scroller under a sticky `.modal-head`. An action: the head (title, marks, ×), `_action_head` with its evidence folded, then the run form one section step below (`--space-5`, above the form's own `--space-4` field rhythm); opened from results, the head is `← Results` (`.btn`, `[data-panel-back]`) and ×, and the title opens the body. Results: the head `Search "…"` and ×, then `_search_results` — Discover's banner, sort `.tabs` and `.action-list`, each row carrying its rank under every order (`data-rank-*`). A panel that cannot be filled keeps its head — `← Results` where it was opened over results, and × — over the `.banner-error`. A pane of the window: full height at its right edge, `--panel-w` wide, a hairline from the conversation, its width taken from the column, while the main area is 960px and over (a container query, so the sidebar's state counts); narrower, it covers the conversation. Empty, it takes nothing |
| **you-row** | `.you-row` | a setting row: its name, its value (`.detail`, right), its one command (on its own line, right, when the row is too narrow; a figure and its name never break apart); on Settings, and Admin's `version` row (`_update_row`), whose value names the kernel and agent after Juice's release and whose command gives way to the in-flight `.progress` while an update runs |
| **Update notice** | `.update-notice` (`_update_notice`) | the superuser's notice that a newer Juice is out: a `.btn` (`#arrow-up`, `Update to 0.69.0`) leading to Admin, between the rail and the avatar; its icon alone in the strip. The wrapper holds the heartbeat's input and draws no box (`display: contents`) |
| Field | `.field` + inputs/select/textarea · `details.group` (a named nested node, folded behind its `summary`) · `div.group` (an array's element) · a configuration file's form (`/admin/config`, an agent's Config: `form.card.stack[data-json]` carrying `version`, the switch, `_form_fields`, Save) · `_control` (one control's markup, which `_field` wraps in its label and help and the composer bar uses alone) · `[data-rows]` / `[data-row]` / `[data-row-add]` / `[data-row-remove]` (repeatable rows) | forms |
| **Switch** | `.switch` (`role="switch"`, `aria-checked`) · `_json_switch` | a binary view or setting: its label, then a track and knob, ink-filled when on; the JSON switch at the top right of a form and of a view (§5.4) |
| **View** | `.view` · `.field .box` · `_view` / `_value_block` partials (the `_field` partial with `ReadOnly` nodes) · `.switch` | **the** read-only JSON value — a result, a receipt's args and result, sealed `partial_args`, verification checks — in the run form's own anatomy, each control a box in the read-only register (no fill, a hairline), nothing submittable; a named nested node closed behind its name and size; raw JSON and Download behind the same JSON toggle the form carries; in `_value_block`, a string holding a JSON object or array is viewed as that document, its raw JSON the stored string (`requirements.md` §7). `dl.kv` is a label/value list of kernel facts (an account's balances) |
| Dialog | `dialog.modal-form` · `dialog.modal-confirm` (`#modal-backdrop`, and `#idle`, the idle warning) · `dialog.palette` (the help overlay) | overlays (bottom-sheet on mobile) |
| Status | `.pill` (+state) · `.sig-*` money signals · `.chip` (visibility/kind) | trust signals |
| Feedback | `.outcome` · `.banner` (the one-shot notice included) · `.live-region` · `.progress` · `.badge` | results/notice |
| Ref | `_ref` partial | an action address `owner@kernel/name` (owner linked only when local) |

---

## 1. Canvas

The frame every page renders into: the collaborative workspace
(`requirements.md` §6.1). Defined in `layout.html` and `_sidebar`.

```
┌─ sidebar ─────────┬───────────────────────────────┬─ panel ──────┐
│ ◆ k play        ≡ │ notice (one-shot)              │ (a room only: │
│ + New conversation│ crumb · entries · waiting on   │  an action    │
│ assistant         │ composer [message …] [/] Send  │  beside the   │
│ research-room  •  │ — or any page's content —      │  conversation)│
│                   │                                │               │
│ (A) ana@k   ③     │                                │               │
└───────────────────┴───────────────────────────────┴───────────────┘
```

| Region | Contents | Rules |
|---|---|---|
| **Sidebar** | brand (kernel and world) and the collapse toggle; the **rail** (its opener, `#plus` and "New conversation", then the rooms); the **avatar** at the foot | One element, sticky, the visible screen's height (`100dvh`), 240px. The width picks one of two models, never mixed. **Wide (>720px): docked or a strip.** Collapsed, the same element keeps only the toggle, the opener's icon and the avatar with its badge; the brand, the rooms and the handle are hidden, since a list of names has no icon form. The state is persisted server-side (`sidebar` cookie on `<html data-sidebar>`, no flash) and applies to wide screens only. **Narrow (≤720px): a drawer** (Topbar). |
| **Avatar menu** | the address and the available balance; every destination of the registry, the current one `aria-current`, Inbox with the task badge; Sign out | A `.menu` from the avatar (`[data-menu]`), each destination an icon and its name. The avatar carries the task badge too. |
| **Main area** | the content container, a one-shot notice heading it | Container: `max-width ~1160px`, centered. **The container is the ONLY width decision**: every component fills it — no per-component `max-width` caps. |
| **page-head** | the title only — no subtitle, no commands (§3, §5.3) | Present on every **index** page. Commands sit inline with their content. |
| **Notice** | one transient message | A `.banner` of its kind first in the container, from a one-shot cookie, led by its kind's icon (`#alert`, `#check`); × dismisses it (`[data-dismiss]`, a link to the page itself without script); `role=alert` (error) / `status`. Not a feed.. A page that scrolls to its end on load (a room) does not when a notice heads it: the result is read first, where it stands |
| **Topbar** | the menu trigger (with the task badge) and the brand | Narrow screens (`≤720px`) alone, `--topbar-h` tall. The trigger slides **the same sidebar** in over the page as a drawer (`html[data-drawer=open]`, `?drawer=open` without script), without the collapse toggle; the `.scrim` link, Escape and any navigation close it, and it is never stored. No fixed bottom bar: the destinations are unbounded. |

**Entry screen** (unauthenticated): no sidebar — a single centered, branded auth
card (`◆ juice`) that **names the kernel you're joining** ("Sign in to
`handle`") with a short **key fingerprint** for pre-auth verification, then the
**Sign in / Create account** toggle (the `.tabs` component, §5.2, its one
retained use), one action per tab. The kernel handle + key come from the open
`GET /health`; the sidebar brand shows the same handle once signed in (kernel at
the top, your own `handle` at the profile foot). Tabs are real `/login?tab=`
links (server renders the active tab with JS off); app.js swaps panels
client-side.

**Layer order** (all native `<dialog>` except the menu, the panel and the
toast; `Esc` closes the topmost): content < menu/popover < panel < drawer <
modal form & confirm modal < help overlay < aria-live toast.

---

## 2. Tokens & theming

Semantic tokens are the only colors, radii and sizes components may use. Generated per theme
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
| `--font-size-xs … -2xl` · `--space-1 … -6` | the type (§3) and spacing (§4) scales |
| `--control-h` (`2.25rem`) · `--topbar-h` | the one height of every button, input, select, one-line textarea and icon button; the narrow topbar's |
| `--icon-sm` / `--icon-md` / `--icon-lg` · `--avatar` / `--avatar-lg` · `--radius-full` | icon sizes (inside text and chips / everywhere else / the topbar trigger), avatar sizes, round |

- **Theme selection:** defaults from `prefers-color-scheme` (a default light and
  dark theme); an explicit choice is the `theme` cookie (slug) stamped on
  `<html data-theme>`, no flash, works with JS off.
- **Contrast:** every theme MUST pass WCAG AA — body/muted ≥4.5, signals/accent
  ≥3.0, accent distinct from every money signal. Enforced by test; a failing
  theme does not ship.
- **Money signals** are color **+ glyph on text/borders only, never fills.**
  Accent is reserved for money movement (§5) and focus — never decoration.
- **Scrollbars** are thin, a `--border-strong` thumb on no track, in every
  scroller (`scrollbar-width`, `scrollbar-color`, the standard properties).

---

## 3. Typography & page structure

- **Face:** the **proportional system sans stack** (`--font-ui`) for chrome and
  prose — the storefront register. **Monospace (`--font-mono`) is reserved** for
  the *provenance* set: money, IDs, hashes, addresses, and sealed
  `partial_args`. The typeface itself marks kernel-vouched facts apart from
  chrome and from counterparty prose. (The `terminal` theme overrides `--font-ui`
  back to mono; components never assume one or the other — they use the token.)
- **Scale:** six steps anchored at the standard size — `--font-size-2xl`
  (`1.5625rem`, h1 and a headline figure) · `-xl` (`1.25rem`, h2) · `-lg`
  (`1.125rem`, h3, the brand) · `-md` (`1rem`, body and every control) · `-sm`
  (`.9375rem`, `.small`, captions) · `-xs` (`.875rem`, `.tiny`, badges, chips,
  fingerprints). Headings carry slight `letter-spacing`.
- **The standard size is `1rem`,** 16px at the browser's default, since iOS Safari
  zooms the page on a text field under 16px (`TestStandardSizeIsOneRem`). The `html`
  rule of `tokens.css` is the one definition of `1rem`. A size that holds text
  (`.badge`, `.chip`, `.input-sm`, `.stat-row`'s column) is written in `em`, so it
  follows its text.
- **The brand names the kernel and its world** (`.brand` + `.brand .world`,
  the one `_brand` partial), in the sidebar and in the topbar of a narrow
  screen. A kernel serves one
  world for life, and the world decides what the money on every screen is:
  whether it can leave, and what it is worth. The symbol on an amount does not
  say that — `credits` is not "not redeemable" — so without this a person cannot
  tell a rehearsal from a live market. Both halves are `/health` fields, stated
  and not judged: the UI never labels a world play or real, it names it.
- **One capitalization: sentence case.** Page titles (`<h1>`), section
  headings (`<h2>`) and nav labels all start with a capital and continue as
  written. A page is named as the nav and the lists name it: `Inbox ·
  Workshop · Wallet · Settings · Network · Admin`, a detail page the singular
  of its list (`Transaction`, `Session`), and pages outside the nav
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
  - *Conversation* = crumb (the room's name, and its kernel muted when another holds it; no back-link, the rail is the way
    across and holds its menu) → entries → whom it waits on →
    composer, beside the panel (§5.5). It is neither an index nor a detail.

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
observe is explained where it moves, in a person's words. The wallet's
**Deposits and withdrawals** card is that one block (requirements §6.5): a
`.banner-error` warning led by the `#alert` icon — "Only send money from your
crypto wallet, never from an exchange or any other wallet. …" — then your
crypto address, the Juice deposit address and the token, since a
payment from an exchange is held for the operator and nothing else on screen
would show why. No other surface carries explanation.

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

One scale, on a 4px base: `--space-1` … `--space-6` = **.25 · .5 · .75 · 1 ·
1.5 · 2rem**. `app.css` writes every font size, padding, margin, gap and radius
as a token, and every icon and avatar size too; only a hairline (`1px`, `2px`)
is literal (`TestStylesUseTokens`).

| Use | Value |
|---|---|
| `.stack` between children | `--space-4` |
| `.row` gap / `.row.compact` · `.cluster` gap | `--space-4` / `--space-2` |
| `h2` top / `h3` top | `--space-6` / `--space-5` |
| `.subtitle` bottom | `--space-5` |
| `.card` padding | `--space-3 --space-4` |
| `.field` bottom | `--space-4` |
| `.action-list` gap (one column) | `--space-4` |

Compose with `.stack` / `.row` / `.cluster` / `.action-list`; never hand-set margins in
templates.

---

## 5. Components

### 5.1 Buttons — neutral, primary, money & danger

All buttons share **one geometry** (padding, height, radius, font-size); the
height is `--control-h`, the same as every input's.
**Fill marks the one action a form is for.** As in the established systems
(Material's filled button, Apple's default button, GOV.UK's primary), a form's
default action is solid and every other button is outlined: solid ink when it
moves no money and destroys nothing, the accent when it moves money, danger when
it destroys. So one form never shows two solid buttons for its own action, and
two ordinary buttons side by side never read as "different types" except where
one is the action Enter presses.

| Class | Tier | Fill | Use |
|---|---|---|---|
| `.btn-primary` | **a form's default action** (solid ink) | `--text` / `--bg`, weight 600 | the submit of a form that has something to fill in or offers a second button: the action Enter presses (`Save changes`, `Create action`, `Sign in`, `Search`, `Next`, `Install`). One per form, the form's first submit. A form with one button and nothing to fill is a command and stays `.btn` |
| `.btn` | **the neutral** | transparent + border, weight 500 | every other button: a command standing alone (approve, start, stop, inspect, rename…), navigation, add-row, `Back`, and **Cancel** — always `<button type="button" class="btn" data-modal-close>`, never a link (the dialog's × link is the no-JS way out; `TestCancelIsOneButton`) |
| `.btn-money` | **money movement** (solid accent) | `--accent` / `--accent-contrast`, weight 600 | run/pay, deposit, withdraw, and a priced planner's `Choose for me (N)` — **names the amount in parens** as `verb (N)` (e.g. `Run (12)`, `Synthesize (20)`); the accent color conveys it's credits, so **no credit glyph on the button**. An **ordinary task completion is NOT a money button** — it is the neutral `.btn` reading `Complete` (no amount): the completer does the work, but the money was reserved by the task's creator, so `Complete (N)` would falsely read as "you pay N". A **payment task** (its target bears the kernel's `effect="transfer"`, requirements.md §6.3) IS a spend of the completer's own balance and uses `.btn-money` naming the all-in amount |
| `.btn-danger` | **critical** (solid danger) | `--danger` / `--danger-contrast`, weight 600 | delete, suspend, force-end, decline a task |
| `.btn-icon` | icon-only | — | icon buttons, a `--control-h` square; **MUST carry `aria-label`**. `.btn-icon.inline`, borderless and muted at `--space-5`, inside a row or a line of text (a row's menu trigger, the copy control) |
| `.btn-send` | the composer's send (room alone) | `--text` / `--bg`, round, icon-only | the bar's one primary act, `.btn-primary`'s fill in the composer's round icon form, part of the composer exception; disabled while the box is empty. Never the accent: sending moves no money |

**One geometry, one row, one look.**
- No size modifier exists: every button, in a table cell or a card, has the
  one geometry. `.small` is a text utility, never on a button.
- A record's commands stand in **one `.cluster` row** (gap `--space-2`), primary
  first, destructive last. A command that posts to its own action joins the row
  through the HTML `form="…"` attribute (a task's Decline beside Complete), so
  no command drops below its row. Navigation inside such a row (`Inspect`,
  `Rename`) is a `.btn`.
- One command has one look and one label everywhere: Suspend is `.btn-danger`,
  Unsuspend `.btn`, ending a session `End · refund N` (`.btn-danger`, the one
  `_end` partial; `End` alone when nothing is refunded). There is no
  quiet-danger tier.
- `TestButtonsHaveOneGeometryAndOneLook` enforces this on every template.
- `TestAFormsActionIsItsPrimary` holds the primary rule on every form: one with
  something to fill in or a second button has its first submit solid
  (`.btn-primary`, `.btn-money` or `.btn-danger`), and one with a lone button
  and nothing to fill has no `.btn-primary`.

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
│  all   active   inactive                         │
└──────────────────────────────────────────────────┘
```

| Use | Renderer | Active |
|---|---|---|
| **Entry toggle** | `paneltab` in `.auth-card .tabs` — Sign in / Create account (real `?tab=` links; JS swaps panels **and moves `aria-current`** so only one is lit) | requested tab |
| **List filters** | `tab` in `.tabs.control-tabs` — action `all/active/inactive` (real query-string links, reload) | selected filter |
| **Places** | `icontab` in `.tabs` — the inbox's To do · Sent · Done: a sprite icon (`ico-inline`, `aria-hidden`) before the word, never instead of it | the place shown |

**Icons mark places, never filters.** A tab that takes you somewhere (the
inbox's views) carries its icon; a tab that narrows a list (`all/active`,
`relevance/rating/price`) or toggles an entry is a word.

**Distinct axes get distinct rows, never mixed**: two tabs on different
variables in one row both light up. **A filter row holds filters only**; the
section's command stands beside its heading (`.subhead`: the heading left, the
`.btn` right), as the Workshop's **New action** does. The `.linkbar` is
retired.

### 5.3 Commands are inline

The `page-head` carries **no commands** — just the title. A command
lives **inline, with the content it acts on**. An opener that **adds** something
is a single `.btn`: **New action** (the plus icon and the words) beside "Your
actions", `+ record a payment`
in the users section, `+ inspect a kernel by key`; none float, none are colored.
**An action is a button.** A link goes somewhere to read: a record's id or title,
an address, a crumb, the pager, a sentence's next step (`View the receipt →`).
Anything else a person acts on is a `.btn`, or a `.btn-icon` when it has no words,
or a `.menu-item` inside an open menu. A **record's verb** — Name, Rename, Suspend,
End — is a `.btn` (§5.1) on the record's page or in its row, so one verb has one
face, unless the row gathers its verbs in a menu (§5.12), where each is a `.menu-item`. A `.cluster` of
verbs is a child of a `.stack` (a `.card.stack`, a `form.stack`): that is what
separates it from the content above, and nothing else does (§4).

- When one group has several create paths, the opener is a `+ new … `
  `[data-menu]` trigger opening a `.menu` (§5.12) of the methods — not a row of
  equal buttons (see the action page's four creation methods).
- Money moves stay deliberate: the opener launches a modal (§5.8) whose
  confirm is the `.btn-money` that names the amount.

### 5.4 Forms

- `.field` (bottom `--space-4`) wraps `label` (weight 600) + optional `.help`
  (muted `--font-size-sm`) + control. Required marker: `.req` (`*`, accent).
- **Inputs / selects / textarea:** full width, `--control-h` tall,
  `--border-strong`, `--radius-s`, `--bg`. Inputs and buttons on the same row
  align in height because they share one height token.
- **Checkbox:** the browser's box replaced by one square at `--icon-md` in an
  input's border and ground; ticked, it fills with ink (`--text`) and shows
  the set's `#check` tick in the ground — the switch's language, never the
  accent. Beside its words in `label.check`.
- **Textarea:** starts at **exactly one line** (`min-height: var(--control-h)`),
  **autosizes** to content up to 10 lines then scrolls; mono; `rows="1"`. Never
  a fixed multi-line box.
- **Progressive widgets:** repeatable rows (`[data-rows]`/`[data-row]`, Add =
  `.btn-icon` `#plus`, Remove = `.btn-icon` `#x-mark`, each with its
  `aria-label`; one mechanism for the http parameter rows and a schema array's
  rows — Add clones the last row, or the kept template once every row is
  removed, clears it and, when the
  container names its array's path, renumbers the clone to one past the highest
  index in every `name`, `id`, `for` and nested `data-rows`), and **the
  reveal**: a `select[data-reveal="X"]` shows only the sections whose `data-X`
  lists the value chosen, space-separated, within its nearest `[data-group]`,
  else its form (`data-kind`, `data-auth-scheme`, a models block's
  `data-provider`); the server renders the same state, so a page without
  script shows the sections of what is selected. Both disable hidden inputs so
  they aren't submitted. Secrets are masked, write-only, never pre-filled; a
  held one says so in its placeholder, `configured ✓ (replace)`.
- **The models form** (`_models_form`, requirements.md §6.6) is one partial for
  the installer's Models step and the Models page: two `[data-group]` blocks,
  Chat model and Embedding model, each an `h3.flush` and its `.field`s
  (provider, address, key, model, and on the page the price). The model is a
  `select` of the provider's models, a text input only where it lists none.
  The htmx pattern of dependent selects refreshes it: the block's provider,
  address and key each carry `hx-post` to the host's refresh address, and the
  block holds the rest (`hx-target` its model field, `_models_model`,
  `hx-include` the form, one `hx-sync` replacing a request still in flight).
  The field is only swapped and carries no listener, so no change is lost
  while it is, and nothing typed elsewhere moves. The host holds the commands:
  its primary first, then `List models` (`.btn`, `formnovalidate`), the
  no-script path, which writes nothing.
- **Call form** (`_call_form`) — **one** component for *every* action execution
  (action run **and** task completion), always inline (never a modal): optional
  **locked block** (a task's pre-bound args as the view, `.view.locked`,
  no verbal label — the sealed half of the form), the **editable input**, **one
  button** (a paid run's `Run (N)` money button, §5.1; an ordinary task's `Complete`, the form's primary and never a money button; a payment task's money button, §5.1)
  with `_progress` + `hx-disabled-elt`, then the **result attached below**
  (`.outcome`, never nested). Tasks are **one-shot** (`data-oneshot`); actions repeat.
  The form's **tools** stand in its top row above the fields they act on
  (`.form-tools`): the JSON switch and, in a panel opened from a room (the
  room a hidden field), **Fill from chat** — the `sparkles` icon named in its
  title, a `.btn-icon` when the planner is free, the `.btn-money` holding the
  icon and the amount when priced (§5.1: a spend names its amount). While the
  planner works, the same `_progress` bar runs at the row's left, where
  nothing else stands, the form is `aria-busy` and its other controls
  `inert`; a refusal is an `.error-text` in the bar's place. Run is the foot's
  one button. A success's `.outcome` carries its result as the composer's
  draft (`data-draft`), and the panel closes into it.
- Editable input is schema-driven, one of three: **structured** (typed fields +
  the JSON toggle), **approval** (nothing left to fill → just the button), or a
  lone **raw JSON** editor (free-form schema). Same component throughout.
  Structured means the schema's whole tree: a nested object or array is a
  `details.group` under its `summary`, closed unless a field it demands is
  still empty (a required key of an object being sent, a row under
  `minItems`), an element a `div.group`, an array's rows `[data-rows]` with
  Add and Remove, a map-shaped node an empty JSON textarea with the shape as its
  placeholder; a field's wire name is its JSON Pointer (`f_/messages/0/content`,
  `t_` the type hint, `n_` the null box; `requirements.md` §6.2).
- **JSON switch** (`.switch`, `_json_switch`: the label `JSON` and a track
  with a knob, `role="switch"`, at the top right of a `[data-json]` block) is
  the one way to see a block as JSON: on a structured form it swaps the fields
  for the raw editor; on a view it swaps the fields for the raw JSON (and
  its Download). A binary view whose off-state has no name is what `.tabs`
  (§5.2) cannot express; the switch is the component for it. No "edit as JSON"
  button, no disclosure for JSON, no underlined word.

### 5.5 Cards, lists, tables

- `.card` (surface, border, radius, pad); `.card-list` for stacked cards;
  `.action-list` for a collection of things, one **ranked column** of
  `.action-row`s. There is no grid: a rank is a sequence, and rows align the
  facts a person compares. Like every component it fills the container (§1).
- **Action row:** **one row, `_action_row`, on every list** — Discover, a search
  on the page and in the panel, the Workshop, a peer's actions — a discovered
  action included (its link, `_action_href`, resolves it; its price is
  indicative). A **square tile** (`.media.tile`, §5.13); then the text
  (`.action-body`): the action's **title** as the link (`.action-title`) —
  never the bare name or the raw kind string, wrapping and **never cut** — with
  its marks following it in the flow (`.action-name`, `.chips`), the **ref** on
  its own line beneath (`.action-ref`, `_ref`, owner emphasized), and **one
  line** of description (`.action-desc`); then **the figures** (`_stats`). The
  title is the row's one pointer target; the row is `.rowable` for
  `j`/`k`/`Enter` and carries no click handler. The figures sit outside the
  tile (kernel chrome never overlaps counterparty pixels).
  - **The row arranges itself by its list's width**: `.action-list` is a named
    container (`action-list`). Where it is wide (52rem and over) the figures
    are a fixed third column at the right of the text, each figure a fixed
    cell, so they align down the list, and the price stands at the right edge;
    where it is narrow the figures follow the text in its column and wrap when
    they must. A page, the panel and a phone take the same rules: none has a
    rule of its own.
  - **The figures** (`_stats`, `.action-stats`) are the same on the row, the
    action's page and its panel: success, rating, mean time, price, on one
    line. A figure is a `.stat-cell`: an icon and a bar, or a number, and
    nothing under it — the bar's open share says how much is known — its
    record in words in `title` and in `.sr-only` text, the share then the
    count, as stores and status pages state it: `96% success · 89 runs`,
    `90% positive · 10 ratings`, `0.4 s average`, `No runs yet`. Success is `#check`, rating `#thumb-up`, time `#clock`.
    The time cell (`.stat-time`) keeps a minimum width so its column aligns;
    an action never run shows a muted `—`, never `0.0s`.
  - **The range bar** (`_range`, `.range`) shows what a record has settled, in
    three segments across a fixed 0–100% track, the same width down a list:
    **green** (`.range-good`) the share confirmed good, up to the low end of the
    Wilson score interval at 95% confidence; **red** (`.range-bad`) the share
    confirmed bad, from its high end; the open track between (`.range-open`,
    `--border`) what is not yet known. An action never run is all open and
    needs no "none" state; green grows only as runs confirm it. Its geometry is
    the data's, in the SVG's own attributes; its look is the stylesheet's. Its
    hues are **its own tokens**, `--range-good` and `--range-bad` (generated
    per theme from the palette's green and red, at 3:1 against the page): the
    one place a hue states a confirmed share, never money credited nor a
    destructive act.
  - **Marks** (`_action_marks`, the same beside the title on the row, the page
    and the panel) state the *exception* only:
    - **Remote** → `R` (`_kind_chip`, `kindBadge`), filled: a proxy of another
      kernel's action. **Discovered** → an unfilled `R`: that action before
      resolution, the same letter without the weight. The two never appear
      together.
    - **Needs account** → a filled key-icon chip (`.chip.filled`, `#key`) only
      when the action requires a per-caller grant the viewer hasn't connected.
      On the action page the run form is replaced by the connect gesture
      (banner + `Connect account`) — **a doomed run is never offered**, the
      same rule as peer-blocked runs.
    - **State** → one `.pill`: `inactive` (`.muted`) for every viewer, since an
      action that is off cannot be run; otherwise, **only for a viewer who may
      manage the action** (its owner, the superuser), who may call it —
      `private`, `local`, `public` (`.status-credited`). A discovered row has
      no state. A row is never faded: the word says it.
    - The action's page adds the kind's letter (`W` wasm · `N` native · `H`
      http, `_kind_chip`), which a list does not state.
  - **A chip is ONE letter or ONE icon. Never a word, never a phrase** — the
    meaning lives in its `title`, which is also its accessible label. **Chips
    are colorless** — no theme hue is spent on them (every hue is a reserved
    money/trust signal); emphasis is by weight (`.chip.filled`), never by hue.
  - The peer roster and the agents page use the same row, text only, their
    title line a flex `.action-line`.
- **Tables:** full width, hairline row borders, muted `th`. A navigable row is
  `.rowable` + `tabindex="-1"` + `data-href`: `j`/`k` move to it and `Enter`
  follows it. Its **link is the pointer target** — the row shows no pointer
  cursor, because no click handler stands behind it (and a row holds
  selectable text and nested links).
- **Money columns are right-aligned, header included** — a money `<th>` carries
  `class="money"` so the title sits over its right-aligned values.
- **Every table lives in a `.table-scroll`** (`overflow-x: auto`) so a wide
  table scrolls inside its own box and the page body never scrolls sideways.
- **Chat is the workspace.** `/chat` opens the viewer's room with the
  assistant; the rooms are the sidebar's **rail**. A room is `.room`: the
  `.conversation` — crumb (room name · members · leave/delete) → the entry
  stack (`.msgs`, `[data-autoscroll]`) → whom it waits on (`_working`:
  `.muted`, `#chat-working`, one element for the page and every out-of-band
  answer — `Waiting on a and b` at body size, three dots (`.dots`) pulsing in
  turn beside it while the page watches, still under reduced motion) → the composer bar, sticky at the foot — beside the
  `.panel`. **The page is the scroll container** — no fixed-height frame; the
  panel alone scrolls on its own. The document pins to its
  **newest** entry on open and on any append while already at the bottom —
  never yanking a reader scrolled up into history; the sticky crumb and the nav
  keep the way out always in reach. An **entry** (`_entry`: `.msg` with
  `.avatar`, the author in the mono face, the time) renders its body as the
  view (`_value_block`). An entry an answer replaced leaves the page when the
  answer arrives (an out-of-band delete by its id, `e-<seq>`). The
  **composer** is the post action's form drawn as a bar, `form.composer`:
  `.error-text` for a refusal, the launcher's list, then `.composer-bar` —
  `/` and `@` (quiet `.btn-icon`s, `#slash` and `#at-sign`), the `message`
  control through `_control` (the form built with `taskForm`, the room sealed
  by the path and not shown, a `sr-only` label), and the send arrow
  (`.btn-send`, `#arrow-up`) — then `_balance`. A refusal answers
  422, so the form keeps its draft (requirements.md §6.7). Enter sends; a
  leading address is completed from the room's members. Nothing asks the person to connect: the UI joins
  them.
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
  signal (`sig-credited` with a `+`, `sig-escrow`). The wallet's and a
  session's funds render through it; no page draws its own figures. An
  account's locked balance is one helper, `lockedFigure`: `parked ◷` (escrow)
  for everyone, `locked ◷` unsignalled for the operator, whose locked also holds
  payouts in transit.

- `.pill` — bordered, `currentColor`. State variants:
  `.status-credited / -refunded / -escrow / -danger`.
- Money value + signal glyph: `+`/`↩`/`◷` via `.sig-credited / -refunded /
  -escrow`. Signals are text/glyph only, never fills.
- **Money display** renders mono + tabular through two helpers. `moneyExact` is
  the kernel's own form: the base-unit integer scaled by the world's `decimals`,
  trailing zeros trimmed to a **two-digit floor**, **no separators**, the world's
  `symbol` appended — `133 credits`, `1.50 USDC`, `0.000133 USDC`, `-2500 credits`.
  Separators are absent by design: the text pastes back into an amount field.
  `money` rounds that for reading: two decimals at one whole unit or more, two
  significant digits below it — `1234.57 USDC`, `0.00013 USDC` — with the exact
  amount as its title. **An amount is rounded where it is read. It is exact where a
  control commits it, where a confirmation or notice names it, where a result or a
  transaction states what was charged, and where it bounds an input.** A symbol the UI picks is a claim about a world it does not own,
  so `$`, `¤` and the retired gem/diamond mark are **banned from every template**,
  as is a hard-coded `step` on an amount input (`{{amountStep}}` reads the world's).
  A spend button names the exact amount — `verb (N)` (§5.1) — and money in prose
  (help/flash/error) is written in the exact form. Where the unit is not yet
  known, a money cell renders an em dash and no amount is accepted.

### 5.7 Refs

An action address, `owner@kernel/name`, renders via the **one** `_ref`
partial, built with the `ref` helper (`ref <address> <id>`; pass `""` for no
link): `<span class="ref mono">` with the owner in `.owner` (weight-emphasized,
confusable-distinct, **not** accent), split on the first `/`. Passing an action
id makes the **name** the link to the action. The owner links to its actions
on Discover (`/browse?owner={address}`) only when the action is **local** — its
kernel segment is this kernel's own name (requirements.md §4); another kernel's
owner renders **unlinked**, since this kernel cannot list its actions — don't
invent a route. A name
is rendered as served: never stripped of its kernel, never decorated.

When that kernel segment is a **raw public key** — an action discovered but not yet
resolved is `owner@<43 chars>/name` — it is **truncated** through the same
primitive a kernel card uses (§5.11), leaving the owner legible and the whole
segment in the `title`. A bound petname is not key-shaped and renders whole. A
43-character key MUST never be a card title — the action's title is.

Wherever an action is presented as a thing — a card, the action page's `<h1>`,
a workshop row, a peer's action list — its **title** leads and its ref follows
beneath it. A Workshop row is the same `_action_row` as Discover's. Where a row only names the action a record is about (a receipt, a
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
- **Destructive confirm:** `form[data-confirm="…"]`, or a submit button carrying
  it where only that button needs one (Save and restart beside List models),
  routes through the shared
  confirm modal — a native `dialog.modal-confirm` (`#modal-backdrop`) shown with
  `showModal()`, so it stacks in the top layer above an open `modal-form`. Its
  message is **one plain question** naming the act and its record ("Decline
  this task?", "Suspend bob@alpha?"), never a sentence explaining what follows;
  an amount that is data rides on the button (`End · refund 0.40 fUSD`).
- **The idle warning** is the same component, `dialog.modal-confirm#idle`
  (`_idle`), which the heartbeat answers open a minute before the session's
  idle end and closed once a request moved it. One question, **Stay signed
  in?**, and two neutral `.btn`s: *Sign out* (the logout form) and *Stay signed
  in* (`POST /session`). A dialog that arrives `[open]` — server-rendered or
  swapped in — is made modal by one script (`modalize`), so it traps focus
  like any other. The reason a session ended is the login page's notice.
- **A record's dialog opened from a list** is the same mechanism: one dialog on
  the page, closed and empty, which the server answers `[open]` on the record and
  swaps in by id (`_rename`), with no run when the list already holds the record.
  It closes when answered closed. A confirmed form may be an htmx form: the
  confirm gate stops the submit from reaching it until Confirm.

### 5.9 The registry, the launcher & keyboard

- **One registry** (`commands.go`) holds every destination and opener, in
  plain words, every one a link, the operator's for `sys` alone. The avatar
  menu lists its destinations; the **launcher** lists all of it, carried as
  **inert JSON** (`<script type="application/json" id="launcher-data">`,
  CSP-safe) with the room's members and the room. Its query is the
  composer's text after `/`; its rows are `.menu-item`s, under no caption,
  read bottom-up from the box — the actions (title and
  address, from the catalog file, matched by either), the pages, each led
  by `#arrow-right`, and at the top
  `Search "…"`, led by `#search`, with the search's price as its muted detail. Enter in the box
  submits the search into the panel; a row is chosen by a click, or by `↑`
  to it and Enter, never for the person. An action row opens the panel (its
  href is the action page, the no-JS path), as does a row of the panel's
  results; the panel's sort tabs arrange the rows it holds (their hrefs are
  Discover's). There is no palette, no search pill, no command prefix.
  Discover's box carries the same list under it, the catalog's actions alone.
- Shortcuts: `g`+letter (destinations, `g d` discover, `g i` inbox) · `/` (the
  launcher, or Discover's box) · `?` (help overlay, `dialog.palette`) ·
  `j/k/Enter` (table and list rows; Enter opens a row as its own link does,
  so a row in the panel opens in the panel) · `Esc` (close topmost) · `↑` from the
  composer into the launcher's rows, `↓` back. Every row targets a real link — the keyboard layer adds no
  capability unreachable without it.

### 5.10 Feedback

| Element | Use |
|---|---|
| `.outcome` (`success`/`failed`/`rejected`) | a call's result panel; a success states no sentence — its styling says it — and carries the result as the view (`_value_block`), an `hr.card-sep` between the result and its receipt and rating row; from a room it carries its draft (`data-draft`) |
| `.banner` (`-info`/`-error`/`-success`) | inline notice, and the one-shot result of the last action heading the column (dismissable, §1). A `.banner-error` is a warning, led by the `#alert` icon. In page flow it owns its spacing (`.container > .banner`), as `h2` and `.subhead` do; inside a `.stack` the stack spaces it |
| `.live-region` toast | **the one** polite `aria-live` region — ephemeral, auto-dismiss, **no history, no stack, no feed** |
| `.progress` | indeterminate bar for long calls (never an indefinite spinner), one partial, `_progress` (id, label): Run's below its form, a form tool's in the form's top row; shown in flight, `.htmx-request`, set by htmx on an `hx-indicator` or by the script owning a fetch |
| `.badge` | round, accent — the task count, the **sole** count of the core surface: on the avatar, the menu's Inbox row (inline) and the topbar's trigger, every copy swapped at once by the heartbeat (`.badge-inbox`), **empty when zero** (`.badge:empty` hides it) |
| `.update-notice` | the one standing notice: the superuser's `Update to 0.69.0` above the avatar while a newer release is out, kept by the heartbeat as the badge is, gone while an update runs. Neutral, never the accent: updating moves no money |
| `.unread` | a rail room with unseen activity: weighted, so it reads as "new" |

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

**The key has one spelling**: `client.ShortKey` (the first 10 characters, `…`,
the last 4; the whole key in `title`), wherever a key is named, alone or in an
address. A row whose label is its key has that spelling as its title and no
ref line; any other row's `.action-ref` is that spelling. `truncate` never
touches a key.

**A party has one rendering**, in two sizes, wherever a user, a kernel or an
action's owner is named: its label (`client.Party.Label`, below), the whole
address in `title` when the text differs, and a copy of the whole address
whenever no petname names its kernel here, so a key shown shortened, alone or
as the key a nickname stands in for, always has its copy. `_party`, for roomy
places (tables, detail pages, dialogs, cards), adds the key a nickname stands
in for, muted. `_party_brief` and `_ref`, for compact ones (an author line, an
action reference, a rail row), add it only once two keys have claimed that
nickname; a status line of a room ("Waiting on …", "Not delivered to …") is
compact too. A question deciding something about a party names it by
`client.Party.Shown`, the label and that key. A row that is itself a button or a link copies from
its menu (`Copy address`).

**The kernel strip** (`_kernel_strip`, `KernelStrip`) ends every peer row, in
the grammar of an icon and a value per slot, the meaning in `title`,
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
| **nickname** | the kernel's claim about itself; never resolves | counterparty content (§6): inert, never linked, shown as the claim it is, marked `~` |
| **petname** | bound here, unique here, resolves | whole, in the provenance face |

**The label** is one rule: a kernel with a petname is shown by it alone
(`bob@beta`); one without, by its nickname marked `~` (`bob@~alpha`), its key
beside it where there is room; one with neither, by its key (`bob@g0b6iv3OqL…WhD0`).
`~` is the one mark of a claim. A named kernel's nickname is shown nowhere. A
decision on an unnamed kernel (Name, Suspend, Block, Disconnect) names its
key. A nickname is never accepted as input; only a petname or a key resolves,
and the nickname's one use is to prefill Name. The
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
link resolving it), and **evidence** as **one table** (`_evidence`,
`.stats-table`, no header): one report per row, its subject first — the
address the inspection serves, else the id cut short with `_copy` — then the
figures' own cells (`_stat_success`, `_stat_rating`, `_stat_latency`: the
icon, the bar or number, the count, the sentence in `title`) and the **source** last, `provider` for
the subject's own report (first) and the issuer's key otherwise. The
corroborated and unverified counts ride in the success and rating titles; a
contradiction marks the source with `alert` and its count. The **one
`_evidence` partial** also renders what other kernels report on an **action's
page**, below its ratings, and folded under **Evidence** in the panel, without
the subject column. Evidence carries **no colour and no fill**: it is a trust
fact, not a status, and never joins the action's own figures, which
stays its own view, as does the inspection's pooled Self-reported line.

**Obligations** sit between this kernel's figures and the list: every cross-kernel
debt settles per call on the rail by a ticket both kernels name, so each row is
one debt — the debtor, the ticket, what was owed, what the draw says must be
paid — and one gesture: a `.btn-money` `Confirm (N)` behind `data-confirm`,
posting the peer's key and the ticket id. Recording a payment moves real money.

**Rename** is one modal per namespace. A user has no page of its own and is
renamed from its row on Admin (`/admin?form=admin-rename&address={address}`): a
`.btn` opener before Suspend, one field taking the new address. A kernel is
named on its inspect page the same way, keyed on its public key. The two are
separate because the kernel's two routes are.

A counterparty carries **no money at all**: a peer account is identity and
moderation state, never a wallet (kernel §D14), so a row states its names and
its strip. The money on this page is this kernel's
own — exposure against its credit limit, the rates, the operator split, solvency,
the vault's holdings at the last finalized block (token and native coin, a
snapshot, as served), and a rail halt as a `.banner-error` warning — plus the
obligations above.

### 5.12 Menus & icons

- `.menu` floating panel via `[data-menu]`; `.menu-item` rows; used by the
  `+ new …` openers (the Workshop's New action), row-lifecycle menus (the rail's rooms),
  each opened from a `.btn-icon.inline` `#ellipsis`, and the avatar menu.
  Activating an item closes the menu. An item that leads to a question or a
  dialog ends in `…`.
- One SVG sprite (`icons.svg`), recolored via `currentColor`, **never emoji**.
  Every icon-only control carries an `aria-label`. The brand uses
  `juice-logo.svg`.
- **The copy control** is the `_copy` partial, built with `copyOf <what> <value>`:
  a `.btn-icon.inline` whose `data-copy` carries the **whole**
  value — a key, a hash, an address, the recovery phrase — beside the
  truncated or prose rendering of it, labelled `Copy <what>`. It is the one
  way a page yields a value the commands take whole (requirements.md §7);
  "Copied" is announced through the live region, never on the button. A
  record id gets none, since the kernel resolves a prefix.
- **A chain address** — a wallet's, a vault's, a token contract — renders as
  wallets show it, `0x7099…79c8`, through the one `_addr` partial (the
  `shortAddr` helper, the whole value in its `title` and its copy control);
  `TestChainAddressesRenderShort` fails on one rendered whole. The whole text
  appears only where it is the point: the registration message to sign.

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

### 5.14 The installer's frame

Every step of the installer renders in **one frame**, and a step supplies
content only: no step sets a size or a spacing of its own.

- **The frame** is an entry card, `.auth-card.auth-card-install`, padded
  `--space-6` (`--space-4` on a narrow screen). `.install-head` holds the brand
  and the title over a hairline. `.install-body` is two columns, the steps
  (five `--space-6` wide) and the step, `--space-6` apart, with a minimum height
  of thirteen `--control-h`, so the card keeps its size from step to step.
- **The steps**, `.install-steps`: each is a marker slot (`--space-5`) and its
  title on a `--space-5` line. The slot holds the step's number
  (`.install-mark`), or the `#check` tick once `.done`; the current step
  (`aria-current="step"`) is weighted, the ones to come muted. On a narrow
  screen the list gives way to one line, `.install-at`, "Step 3 of 6".
- **A step**, `.install-step`, fills three slots `--space-5` apart: its heading
  (an `h2` on the steps' line height, so the two columns start level), its
  content (`.install-content`, a `.stack` of ordinary `.field`s, a `.kv` or
  text), and its commands (`.install-actions`, a `.cluster` over a hairline,
  held at the foot). The row follows the installers' convention, the one
  exception to §5.1's order: `Back` at the left edge and the step's command
  (`Next`, `Install`, `Try again`) at the right, a second command between
  them (the Models step's `List models`). The command stays
  first in the markup, so Enter in a field submits it and never goes back.
- **A job step** uses the same three slots. Its jobs, `.install-jobs`, share the
  steps' marker slot: `.pending` muted, `.running` followed by the one
  in-flight `.progress` bar, `.done` ticked, `.failed` with its words in a
  `.banner-error` beneath. A failure that waits for the person puts
  `Try again` in the commands slot.
- No accent: nothing here moves money.
- **The launch has no screen.** The frame is the installer's alone, and shows
  only the steps the machine needs. Starting the kernel is held behind the page
  request; only when it is slow or fails is it said, on the ordinary entry card
  (`.auth-card`): the brand, then the same `.install-jobs` list with the job in
  hand and its words. No title, no steps, no commands unless a job waits for
  `Try again`.

---

## 6. Invariants (enforced)

- No inline `style=""`; tokens only, for sizes as for colours
  (`TestStylesUseTokens`); every decision is a named class.
- The sidebar has two states per width, never mixed: docked or strip when wide,
  drawer closed or open when narrow (`TestSidebarHasTwoStatesPerWidth`).
- **Accent = money movement + focus only.** Concretely, the accent appears ONLY
  on `.btn-money`, the `.badge`, the focus ring, `.req`, and the progress bar —
  never on links, tab underlines, the current destination or the current room
  (those key on text weight + surface). Money buttons name the amount as `verb (N)` and carry
  **no credit glyph**; money *display* uses the same scaled form in the mono
  provenance face (§5.6). **Danger beats money.** An **ordinary task completion is
  neutral** (`Complete`, no amount, no accent): the completer is not the payer;
  a **payment task** completion is a genuine spend and uses the money button
  naming the amount (§5.1, requirements.md §6.3).
- Counterparty content (names, descriptions, args, results, reasons) renders
  **inert**: escaped, no chrome, no accent; a JSON value only through the view
  (`_view` / `_value_block`), which is structure by JSON type and
  interprets nothing but a string holding a JSON object or array in
  `_value_block`. Trust affordances key on kernel-captured fields, never
  payload shape.
- No spend reachable from a list view or a GET; money moves only through a
  confirm that names the amount.
- Record lists share one row partial per record type; the truncated id is the
  link to the record; referenced entities link to their pages; list money is the
  signed perspective amount (gross/fee/parties live on the detail page). Every
  `<table>` sits in a `.table-scroll`.
- Sentence-case titles, headings and nav labels (`Inbox · Workshop ·
  Wallet · Settings · Network · Admin`); `page-head` (the title only) on index, sticky crumb on detail.
  Commands are inline — never a floating page-head cluster (sticky is
  the page's own header refusing to scroll away, not floating chrome).
- **Copy is rationed (§3 Copy discipline):** no orientation subtitles; no
  sentence narrates kernel/control behavior; disclosures appear once, at the point
  of consequence. Empty states and `help` name the thing, they don't lecture. A
  field caption never restates its label or explains a mechanism. The drift guard
  lints **app** templates (`apps/*/templates/`) for this, identically to the core.
- One `aria-live` region; the task badge is the only async attention signal in
  the core surface (the rail's unread weighting is the conversations' own); no
  notification feed/toast stack.
- Visible focus; full keyboard operability; `prefers-reduced-motion` respected, pseudo-elements included (the progress bar moves there);
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
  outside `_value_block` (`TestJSONRendersThroughTheView`), any class
  `app.css` styles that nothing renders (`TestNoDeadCSS`), and the retired
  `data-param-*` row attributes — money is the
  world's unit and symbol through the `money` and `moneyExact` helpers, the step is the world's
  through `{{amountStep}}`, and buttons read `verb (N)`, exact (§5.6). `TestRecordedAmountsAreExact` renders the other sites that must be exact. `TestNoRetiredSurfaces` fails on the
  vocabulary the kernel retired: settle, unimport, the transfer queue, admin
  withdraw, a peer's balance.
