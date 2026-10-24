// Progressive enhancement. The theme and sidebar toggles are real <form> POSTs
// that work fully without JavaScript (server-read cookies, R8.6); this only adds
// two niceties — forwarding the CSRF token on htmx requests, and applying the
// new theme/sidebar state instantly to avoid a reload flash.

document.addEventListener("htmx:configRequest", (e) => {
  const meta = document.querySelector('meta[name="csrf-token"]');
  if (meta && meta.content) {
    e.detail.headers["X-CSRF-Token"] = meta.content;
  }
  // Tell the server whether this tab is in the foreground. A backgrounded tab
  // keeps polling (fresh content/badges) but the chat message poll must not mark
  // rooms read while unattended. CSP forbids htmx's eval-based hx-trigger filters,
  // so we signal via a header every request already carries. (CSP-safe.)
  e.detail.headers["X-Visible"] = document.visibilityState === "visible" ? "1" : "0";
});

// A one-shot call form (a task completion) is spent once it succeeds: when its
// result swaps in a success outcome, hide the form so only the result remains
// (a reload moves the task to History). Failures/rejections leave the form up.
document.body.addEventListener("htmx:afterSwap", (e) => {
  const form = e.target.previousElementSibling;
  if (form && form.dataset?.oneshot === "true" && e.target.querySelector(".outcome.success")) {
    form.hidden = true;
  }
});

// A [data-reset-on-success] form clears after a successful htmx submit (a compose
// box empties without a reload). Generic — any append form opts in.
document.body.addEventListener("htmx:afterRequest", (e) => {
  const form = e.detail?.elt;
  if (e.detail?.successful && form?.matches?.("[data-reset-on-success]")) {
    form.reset();
  }
});

// A [data-autoscroll] log that grows via htmx swaps keeps its newest entry in view,
// but only when the viewer was already at the bottom (never yanks a history reader).
// The page is the scroll container — the log and the compose form below it sit in
// the document flow like everything else — so we pin the document's bottom, which
// reveals the newest entry AND the form. Idempotent: a no-op poll never moves the
// viewport (already-at-bottom → scrollTop unchanged).
let stickBottom = false;
function atBottom(box) {
  const last = box.lastElementChild;
  return !last || last.getBoundingClientRect().bottom <= window.innerHeight + 120;
}
function pinBottom() {
  const sc = document.scrollingElement || document.documentElement;
  sc.scrollTop = sc.scrollHeight;
}
document.body.addEventListener("htmx:beforeSwap", (e) => {
  // A refused form (422) still swaps: its reason is the response. htmx counts
  // the request unsuccessful, so a [data-reset-on-success] form keeps its draft.
  if (e.detail.xhr?.status === 422) e.detail.shouldSwap = true;
  const box = e.target?.closest?.("[data-autoscroll]");
  stickBottom = box ? atBottom(box) : false;
});
document.body.addEventListener("htmx:afterSwap", (e) => {
  const box = e.target?.closest?.("[data-autoscroll]");
  if (box && stickBottom) pinBottom();
});
// On load, a page carrying a [data-autoscroll] log opens pinned to its end — a
// room opens at the newest message with the composer in view.
if (document.querySelector("[data-autoscroll]")) pinBottom();

// Theme picker: apply the chosen terminal theme instantly, then persist it via
// the real form POST (which also works with JS off). An empty value clears the
// data-theme attribute so the OS preference takes over.
for (const sel of document.querySelectorAll("[data-theme-select]")) {
  sel.addEventListener("change", () => {
    if (sel.value) document.documentElement.setAttribute("data-theme", sel.value);
    else document.documentElement.removeAttribute("data-theme");
    sel.closest("form")?.requestSubmit();
  });
}

// Floating menu: buttons with [data-menu="panel-id"] toggle fixed-position panels.
function positionMenu(btn, menu) {
  const r = btn.getBoundingClientRect();
  const mh = menu.offsetHeight;
  const top = r.top - mh - 6;
  menu.style.top = (top < 8 ? r.bottom + 6 : top) + 'px';
  menu.style.left = Math.min(r.left, window.innerWidth - menu.offsetWidth - 8) + 'px';
}
function closeAllMenus() {
  for (const m of document.querySelectorAll('.menu:not([hidden])')) {
    m.hidden = true;
    document.querySelector(`[data-menu="${m.id}"]`)?.setAttribute('aria-expanded', 'false');
  }
}
document.addEventListener('click', (e) => {
  const btn = e.target.closest('[data-menu]');
  if (!btn) return;
  const menu = document.getElementById(btn.dataset.menu);
  if (!menu) return;
  const opening = menu.hidden;
  closeAllMenus();
  if (opening) {
    menu.hidden = false;
    btn.setAttribute('aria-expanded', 'true');
    requestAnimationFrame(() => positionMenu(btn, menu));
  }
});
document.addEventListener('mousedown', (e) => {
  if (!e.target.closest('.menu') && !e.target.closest('[data-menu]')) closeAllMenus();
});
document.addEventListener('keydown', (e) => { if (e.key === 'Escape') closeAllMenus(); });

// Confirmation modal — forms with data-confirm="message" show a styled dialog.
(function() {
  const backdrop = document.getElementById('modal-backdrop');
  if (!backdrop) return;
  const msg = document.getElementById('modal-msg');
  const btnConfirm = document.getElementById('modal-confirm');
  const btnCancel = document.getElementById('modal-cancel');
  let pending = null;
  let confirming = false;

  function openModal(text) {
    msg.textContent = text;
    backdrop.showModal(); // native <dialog>: top layer, above any modal-form
    btnConfirm.focus();
  }
  function closeModal() {
    if (backdrop.open) backdrop.close();
  }

  document.addEventListener('submit', (e) => {
    if (confirming) return;
    const form = e.target.closest('form[data-confirm]');
    if (!form) return;
    e.preventDefault();
    pending = form;
    openModal(form.dataset.confirm);
  }, true);

  btnConfirm.addEventListener('click', () => {
    if (!pending) return;
    const form = pending;
    closeModal();
    confirming = true;
    form.requestSubmit();
    confirming = false;
  });

  btnCancel.addEventListener('click', closeModal);

  // Light-dismiss: a click on the dialog's ::backdrop lands on the dialog element
  // itself (outside .modal-box). Native Esc fires 'close'; clear the pending form there.
  backdrop.addEventListener('click', (e) => {
    if (!e.target.closest('.modal-box')) closeModal();
  });
  backdrop.addEventListener('close', () => { pending = null; });
})();

// Optimistic sidebar collapse/expand.
for (const form of document.querySelectorAll("form.sidebar-collapse")) {
  form.addEventListener("submit", () => {
    const next = form.querySelector('input[name="sidebar"]').value;
    if (next === "collapsed") {
      document.documentElement.setAttribute("data-sidebar", "collapsed");
    } else {
      document.documentElement.removeAttribute("data-sidebar");
    }
  });
}

// Auth scheme: show only the credential fields for the selected scheme.
// Inputs in hidden sections are disabled so they are not submitted. Scoped to the
// enclosing form so it works inside a <details> panel or a <dialog> modal alike.
for (const sel of document.querySelectorAll('select[name="auth_scheme"]')) {
  const panel = sel.closest('form') || sel.closest('details');
  function update() {
    for (const sec of panel.querySelectorAll('[data-auth-scheme]')) {
      const match = sec.dataset.authScheme === sel.value;
      sec.hidden = !match;
      for (const inp of sec.querySelectorAll('input')) inp.disabled = !match;
    }
  }
  sel.addEventListener('change', update);
  update();
}

// Kind: reveal the fields that belong to the selected kind — method and params
// for http, the compiled artifact for wasm. One attribute names the kind a
// section belongs to, so a third kind needs markup and no script. Inputs in a
// hidden section are disabled so they are not submitted and the param_name/
// param_in rows stay index-aligned for server-side pairing.
for (const sel of document.querySelectorAll('select[name="kind"]')) {
  const form = sel.closest('form');
  function update() {
    for (const sec of form.querySelectorAll('[data-kind]')) {
      const show = sec.dataset.kind === sel.value;
      sec.hidden = !show;
      for (const el of sec.querySelectorAll('input, select, textarea')) el.disabled = !show;
    }
  }
  sel.addEventListener('change', update);
  update();
}

// Content reaches the page in several ways — the server's render, an htmx
// swap, an out-of-band swap from the heartbeat, a fill or a JSON switch
// replacing a form's fields — so its controls are not wired element by
// element: one listener on the document reads what it needs from the DOM at the
// moment of the click, and works for every element however it arrived, once.

// own is the first element under block matching sel whose nearest block of
// the same kind is block itself, so a view nested inside a form is never taken
// for the form's own part.
function own(block, kind, sel) {
  return [...block.querySelectorAll(sel)].find((el) => el.parentElement.closest(kind) === block) || null;
}

// Repeatable rows ([data-rows]): the http parameter rows and a schema array's
// rows (§6.2). Add clones the last row (or, once every row is gone, the copy
// kept when the first was removed) and clears it; when the container names
// its array's path, the clone's index segment is rewritten to one past the
// highest in use — in every name, id, for and nested data-rows — so a removed
// index is never reused and a nested array keeps its own rows. Remove drops
// the row. Cloned rows inherit the template's disabled state, so they stay
// consistent with the kind toggle.
const emptied = new WeakMap(); // a container's cleared copy of a row, for Add once every row is gone

// clearRow empties a row's values. A select keeps its choice: emptied, it
// would submit nothing it offers. A password's placeholder states what its own
// row holds, so a copy drops it.
function clearRow(row) {
  for (const el of row.querySelectorAll('input:not([type=hidden]), textarea')) el.value = '';
  for (const el of row.querySelectorAll('input[type=password]')) el.placeholder = '';
  for (const el of row.querySelectorAll('input[type=checkbox]')) el.checked = false;
  return row;
}

// The index of a row: the segment after its array's path in its first named control.
function indexOf(row, path) {
  const el = row.querySelector('[name]');
  const m = el && el.name.slice(2).startsWith(path + '/') ? el.name.slice(2 + path.length + 1).split('/')[0] : '0';
  return Number(m) || 0;
}

document.addEventListener('click', (e) => {
  const remove = e.target.closest('[data-row-remove]');
  if (remove) {
    const row = remove.closest('[data-row]');
    const box = row.parentElement;
    if (!emptied.has(box)) emptied.set(box, clearRow(row.cloneNode(true)));
    row.remove();
    return;
  }
  const add = e.target.closest('[data-row-add]');
  if (!add) return;
  const box = add.parentElement.querySelector(':scope > [data-rows]');
  if (!box) return;
  const rows = [...box.children].filter((el) => el.matches('[data-row]'));
  const last = rows.at(-1);
  const source = last || emptied.get(box);
  if (!source) return;
  const row = clearRow(source.cloneNode(true));
  const path = box.dataset.rows;
  if (path) {
    const from = path + '/' + indexOf(source, path);
    const to = path + '/' + (last ? Math.max(...rows.map((r) => indexOf(r, path))) + 1 : 0);
    for (const el of row.querySelectorAll('[name], [id], [for], [data-rows]')) {
      for (const attr of ['name', 'id', 'for', 'data-rows']) {
        const v = el.getAttribute(attr);
        if (v && (v.endsWith(from) || v.includes(from + '/'))) el.setAttribute(attr, v.replace(from, to));
      }
    }
  }
  box.appendChild(row);
});

// The JSON switch: a [data-json] block holds its fields ([data-fields]) and its
// JSON ([data-raw]); the switch swaps them. In a form the hidden editor's
// inputs are disabled so only the visible one submits and has_fields tells the
// server which it was (the kernel validates either); on a view the same
// control shows the raw JSON. The server renders every block with its fields
// shown, which is the switch's off state.
//
// A form names its two conversions (data-to-json, data-to-form): a switch
// converts what is on screen into the other view exactly, or stays and shows
// the server's reason (§6.2). A typed password never enters the JSON, so it
// holds the switch until it is saved or cleared.
document.addEventListener('click', async (e) => {
  const toggle = e.target.closest('[data-json-switch]');
  if (!toggle) return;
  const block = toggle.closest('[data-json]');
  const fields = block && own(block, '[data-json]', '[data-fields]');
  const raw = block && own(block, '[data-json]', '[data-raw]');
  if (!fields || !raw) return;
  const editor = raw.querySelector('textarea');
  const error = own(block, '[data-json]', '[data-json-error]');
  const json = toggle.getAttribute('aria-checked') !== 'true';
  const say = (text) => {
    if (error) { error.textContent = text; error.hidden = !text; }
    return !text;
  };
  const convert = async () => {
    if (json && [...fields.querySelectorAll('input[type=password]')].some((el) => el.value)) {
      return say('A typed key is never shown as JSON; save or clear it first.');
    }
    const body = new URLSearchParams();
    if (json) {
      for (const el of fields.querySelectorAll('[name]')) {
        if (!el.disabled && !(el.type === 'checkbox' && !el.checked)) body.append(el.name, el.value);
      }
      body.append('has_fields', '1');
    } else {
      body.append('raw_args', editor.value);
    }
    const csrf = document.querySelector('meta[name="csrf-token"]')?.content ?? '';
    try {
      const res = await fetch(json ? block.dataset.toJson : block.dataset.toForm, {
        method: 'POST', body, credentials: 'same-origin', headers: { 'X-CSRF-Token': csrf },
      });
      const text = await res.text();
      if (!res.ok) return say(text.trim());
      if (json) editor.value = text;
      else fields.innerHTML = text;
      return say('');
    } catch {
      return say('The server did not answer.');
    }
  };
  if (block.dataset.toJson && !(await convert())) return;
  toggle.setAttribute('aria-checked', String(json));
  fields.hidden = json;
  raw.hidden = !json;
  for (const el of fields.querySelectorAll('input, select, textarea')) el.disabled = json;
  for (const el of block.querySelectorAll('[name=has_fields]')) el.disabled = json;
});

// Password confirmation: in a form marked [data-confirm-password], the
// confirm field must equal the password field. Uses native constraint validation
// (setCustomValidity) so submit is blocked with the browser's own message — no
// eval, CSP-safe. The server re-validates regardless (JS may be off).
for (const form of document.querySelectorAll('form[data-confirm-password]')) {
  const pw = form.querySelector('[name="password"]');
  const confirm = form.querySelector('[name="confirm_password"]');
  if (!pw || !confirm) continue;
  const check = () => confirm.setCustomValidity(
    confirm.value && confirm.value !== pw.value ? "The passwords don't match." : ''
  );
  pw.addEventListener('input', check);
  confirm.addEventListener('input', check);
}

// light marks one tab of its switch current. .active and aria-current move
// together — the CSS highlights on either, so a stale aria-current would keep
// the old tab lit.
function light(tab) {
  for (const t of tab.closest('.tabs').querySelectorAll('.tab')) {
    const on = t === tab;
    t.classList.toggle('active', on);
    if (on) t.setAttribute('aria-current', 'page'); else t.removeAttribute('aria-current');
  }
}

// Entry-screen tabs: swap the sign-in / create-account panels client-side.
// The tabs are real /login?tab= links, so with JS off the server just renders
// the requested tab (no-JS fallback); here we avoid the reload. Scoped to
// [data-tab] tabs (the entry screen); other .tabs are plain navigation links.
document.addEventListener('click', (e) => {
  const tab = e.target.closest('.tabs .tab[data-tab]');
  if (!tab) return;
  e.preventDefault();
  const want = tab.dataset.tab;
  light(tab);
  for (const p of document.querySelectorAll('.auth-panel')) p.hidden = p.dataset.panel !== want;
  if (window.history) history.replaceState(null, '', '/login?tab=' + want);
});

// Floating modal forms. A trigger <a data-modal="dialog-id" href="?form=…">
// opens the native <dialog> as a modal (focus trap, Esc, top-layer stacking) and
// strips the ?form= param from the URL so a reload doesn't reopen it. Without JS
// the anchor navigates and the server renders the dialog already [open].
export function openDialog(dlg) {
  if (!dlg || dlg.open) return;
  if (typeof dlg.showModal === 'function') dlg.showModal();
  else dlg.setAttribute('open', '');
}
document.addEventListener('click', (e) => {
  const trigger = e.target.closest('[data-modal]');
  if (trigger) {
    const dlg = document.getElementById(trigger.dataset.modal);
    if (dlg) {
      e.preventDefault();
      closeAllMenus(); // a trigger may live inside a dropdown (e.g. New action ▾)
      openDialog(dlg);
      if (window.history && /[?&](form|menu)=/.test(location.search)) {
        history.replaceState(null, '', location.pathname);
      }
    }
    return;
  }
  const closer = e.target.closest('[data-modal-close]');
  if (closer) {
    const dlg = closer.closest('dialog');
    if (dlg) { e.preventDefault(); dlg.close(); }
  }
});
// A dialog server-rendered [open] (no-JS fallback path reached with JS on): if it
// is not already modal, close and reopen it modally so the backdrop and focus
// trap apply, and clean the URL.
for (const dlg of document.querySelectorAll('dialog.modal-form[open]')) {
  if (!dlg.matches(':modal') && typeof dlg.showModal === 'function') {
    dlg.close();
    dlg.showModal();
    if (window.history && /[?&](form|menu)=/.test(location.search)) {
      history.replaceState(null, '', location.pathname);
    }
  }
}

// announce puts one ephemeral acknowledgement in the single polite aria-live
// region (§7): it replaces what the region held, so there is no stack and no
// history, and it clears itself.
export function announce(text) {
  const region = document.getElementById('live-region');
  if (!region) return;
  const toast = document.createElement('div');
  toast.className = 'toast';
  toast.textContent = text;
  region.replaceChildren(toast);
  clearTimeout(announce.timer);
  announce.timer = setTimeout(() => { region.replaceChildren(); }, 2000);
}

// copyText puts text on the clipboard: the async API where the page is a
// secure context (https, or localhost), else the selection command, which
// still works on a plain-http origin. Resolves to whether it succeeded.
function copyText(text) {
  if (navigator.clipboard && window.isSecureContext) {
    return navigator.clipboard.writeText(text).then(() => true, () => false);
  }
  const ta = document.createElement('textarea');
  ta.value = text;
  ta.setAttribute('readonly', '');
  ta.className = 'sr-only';
  document.body.appendChild(ta);
  ta.select();
  let ok = false;
  try { ok = document.execCommand('copy'); } catch (_) { ok = false; }
  ta.remove();
  return Promise.resolve(ok);
}

// Copy-to-clipboard: a [data-copy] control carries the whole value it copies —
// a key, a hash, an address, the recovery phrase — so the clipboard holds what
// a command takes, never the truncation on screen (the _copy partial).
document.body.addEventListener('click', (e) => {
  const btn = e.target.closest('[data-copy]');
  if (!btn) return;
  copyText(btn.getAttribute('data-copy')).then((ok) => announce(ok ? 'Copied' : 'Could not copy'));
});

// The launcher (requirements §6.2): the composer's `/`. Its query is the
// composer's own text after a leading `/` — one box, under its results — and
// the list opens above the composer with the best match nearest the box, as a
// prompt at the foot of a terminal lists them. The page carries the registry's
// pages, the room's members and the room (#launcher-data, inert JSON); the
// catalog's actions and what a search costs are loaded once
// (GET /search/catalog.json) and matched in the browser by title or by
// address, so typing sends no request and spends nothing. Choosing an action
// opens its panel beside the conversation and choosing a page goes there;
// Enter, or the top row naming the search's price, submits the query to the
// economy, whose results open in the panel. The search's row is led by its
// glass and a page's by an arrow.
// A row is chosen only by clicking it, or by ↑ to it and Enter: nothing is
// chosen for the person. Closing the list gives the draft back. A first word
// that begins a member's address lists the members, and choosing one writes
// its address. Without JavaScript the composer is the plain post form and
// every page stays a link.
//
// hintKey and the ranking mirror server/handlers.go (hintKey, wordsBegin, and
// catalogAsResults's title tiers); server/testdata/hints.json holds the cases
// both are checked against.
const LAUNCH_ACTIONS = 5;
const LAUNCH_PAGES = 3;

// hintKey: NFKD, combining marks removed, lowercase, every run of characters
// that are neither letters nor decimal digits one space, trimmed.
function hintKey(s) {
  return s.normalize("NFKD").replace(/\p{Mn}/gu, "").toLowerCase()
    .replace(/[^\p{L}\p{Nd}]+/gu, " ").trim();
}

// matchHints ranks entries {t, k, r?} for the typed text: every typed word
// must begin a word of the key — the title's, or the address's where an entry
// has one; keys beginning with the whole text first, then the other matches;
// shorter first, then alphabetical. An empty text matches nothing unless
// `all` asks for everything.
function matchHints(entries, q, limit, all = false) {
  const typed = hintKey(q).split(" ").filter(Boolean);
  if (typed.length === 0 && !all) return [];
  const whole = typed.join(" ");
  const hits = [];
  for (const e of entries) {
    const words = (e.k + (e.r ? " " + e.r : "")).split(" ");
    if (!typed.every((t) => words.some((w) => w.startsWith(t)))) continue;
    hits.push({ ...e, rank: e.k.startsWith(whole) || (e.r || "").startsWith(whole) ? 0 : 1 });
  }
  hits.sort((a, b) => a.rank - b.rank || a.k.length - b.k.length || (a.k < b.k ? -1 : a.k > b.k ? 1 : 0));
  return hits.slice(0, limit);
}

let launcher = null;
try { launcher = JSON.parse(document.getElementById("launcher-data")?.textContent || "null"); } catch (_) { launcher = null; }
// The catalog file: the actions, and the search's terms — its price as the
// row names it and the quote that pins it — or null where none can be quoted.
let catalog = null;
const loadCatalog = () => (catalog ??= fetch("/search/catalog.json", { credentials: "same-origin" })
  .then((r) => (r.ok ? r.json() : null))
  .then((file) => ({ actions: (file?.actions || []).map((e) => ({ ...e, r: hintKey(e.ref || "") })), search: file?.search || null }))
  .catch(() => ({ actions: [], search: null })));

// row builds one launcher row: a link, labelled, with a muted detail, and
// led by an icon where one marks its kind.
function row(label, detail, href, icon = "") {
  const a = document.createElement("a");
  a.className = "menu-item";
  a.href = href;
  if (icon) {
    const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
    svg.setAttribute("class", "ico");
    svg.setAttribute("aria-hidden", "true");
    const use = document.createElementNS("http://www.w3.org/2000/svg", "use");
    use.setAttribute("href", "/static/icons.svg#" + icon);
    svg.append(use);
    a.append(svg);
  }
  a.append(label);
  if (detail) {
    const d = document.createElement("span");
    d.className = "muted small mono";
    d.textContent = detail;
    a.append(" ", d);
  }
  return a;
}
function caption(text) {
  const c = document.createElement("div");
  c.className = "menu-caption";
  c.textContent = text;
  return c;
}

// The panel beside the conversation (requirements §6.7) holds an action or a
// search's results. An action opened from the results is shown over them, and
// the results are held, with their order and scroll position, for ← Results.
let held = null;

// openPanel opens the action behind a row's link in the panel: a row's panel
// is its page's address with /panel (/actions/{id}, /discovered?ref=), opened
// for the room, and over the results when `back` says so.
function openPanel(href, back = false) {
  const url = new URL(href, location.origin);
  url.pathname += "/panel";
  if (launcher?.room) url.searchParams.set("room", launcher.room);
  if (back) url.searchParams.set("back", "1");
  else held = null;
  window.htmx.ajax("GET", url.pathname + url.search, { target: "#panel", swap: "innerHTML scroll:top" });
}

// searchPanel submits a query to the economy: the lookup, pinned to the quote
// its row named, answering the panel with Discover's results (§6.2).
function searchPanel(q, quoteHash) {
  held = null;
  window.htmx.ajax("POST", "/browse/search", { target: "#panel", swap: "innerHTML scroll:top", values: { q, lookup_quote_hash: quoteHash } });
}

for (const form of document.querySelectorAll("form[data-composer]")) {
  const box = form.querySelector(".launcher");
  const button = form.querySelector("[data-launcher]");
  if (!box || !button || !launcher) continue;
  const list = box.querySelector("nav");
  const pages = (launcher.pages || []).map((p) => ({ t: p.name, k: hintKey(p.name), href: p.href }));
  const field = () => form.querySelector("[data-fields] textarea:not([disabled])");
  let saved = null;  // the draft the launcher gives back when it closes

  const open = () => saved !== null;
  const close = (restore = true) => {
    if (restore && saved !== null && field()) field().value = saved;
    saved = null; box.hidden = true; list.replaceChildren();
    sync();
  };
  // launch opens the launcher over the draft: the field holds `/` and the
  // query, and the draft comes back when it closes. The `/` button and the `/`
  // key (keys.js) ask for it with a `launch` event on the form.
  const launch = () => {
    const f = field();
    if (!f) return;
    saved = f.value.startsWith("/") ? "" : f.value;
    f.value = "/";
    f.focus();
    render();
  };
  form.addEventListener("launch", launch);
  // search closes the launcher, giving the draft back — the query is never
  // part of it — and submits the query at the search's quoted terms.
  const search = async (q) => {
    const { search: terms } = await loadCatalog();
    close();
    searchPanel(q, terms?.quote_hash || "");
  };

  // place puts the rows above the bar, the last row nearest it.
  const place = (rows) => { box.hidden = rows.length === 0; list.replaceChildren(...rows); list.scrollTop = list.scrollHeight; };

  async function render() {
    const f = field();
    if (!open() || !f) return;
    const q = f.value.slice(1).trim();
    const cat = q ? await loadCatalog() : null;
    if (!open()) return;
    const actions = q ? matchHints(cat.actions, q, LAUNCH_ACTIONS) : [];
    const found = matchHints(pages, q, q ? LAUNCH_PAGES : pages.length, true);
    const rows = [];
    if (q) {
      const r = row("Search “" + q + "”", cat.search?.price || "", "/browse?q=" + encodeURIComponent(q), "search");
      r.addEventListener("click", (e) => { e.preventDefault(); search(q); });
      rows.push(r);
    }
    // A page is a place to go: led by an arrow, under no caption.
    rows.push(...found.reverse().map((p) => row(p.t, "", p.href, "arrow-right")));
    if (actions.length) {
      rows.push(caption("Actions"));
      for (const a of actions.reverse()) {
        const r = row(a.t, a.ref, a.id ? "/actions/" + encodeURIComponent(a.id) : "/discovered?ref=" + encodeURIComponent(a.ref));
        r.addEventListener("click", (e) => { e.preventDefault(); close(); openPanel(r.href); field()?.focus(); });
        rows.push(r);
      }
    }
    place(rows);
  }

  // members lists the room's other members to address: choosing one puts its
  // address first in the draft, in place of an address already there.
  const members = launcher.members || [];
  function listMembers(hits) {
    place(hits.map((m) => {
      const r = row(m, "", "#");
      r.addEventListener("click", (e) => {
        e.preventDefault();
        const f = field();
        const first = f.value.split(/\s/)[0];
        const rest = members.some((x) => x.toLowerCase() === first.replace(/[,:;.!?]+$/, "").toLowerCase()) || !/\s/.test(f.value)
          ? f.value.slice(first.length).replace(/^[,:;.!?]*\s*/, "") : f.value;
        f.value = m + ", " + rest;
        place([]); sync(); f.focus();
      });
      return r;
    }));
  }
  // The members whose address begins with the first word typed.
  function suggestMembers(text) {
    const word = text.split(/\s/)[0];
    listMembers(word && !/\s/.test(text) ? members.filter((m) => m.toLowerCase().startsWith(word.toLowerCase()) && m !== word) : []);
  }

  // The arrow sends only something; a refusal shown above the bar is cleared by
  // the next keystroke.
  const send = form.querySelector("[data-send]");
  const error = form.querySelector("#chat-error");
  const sync = () => { if (send) send.disabled = !field()?.value.trim(); };
  sync();
  form.addEventListener("reset", () => setTimeout(sync)); // a sent post clears the box
  form.querySelector("[data-address]")?.addEventListener("click", () => {
    if (open()) close();
    if (!box.hidden) { place([]); return; }
    listMembers(members);
  });

  button.addEventListener("click", () => (open() ? close() : launch()));
  form.addEventListener("input", (e) => {
    const f = field();
    if (e.target !== f) return;
    sync();
    if (error) error.hidden = true;
    if (f.value.startsWith("/")) {
      if (!open()) saved = "";
      render();
      return;
    }
    if (open()) saved = null;
    suggestMembers(f.value);
  });
  form.addEventListener("keydown", (e) => {
    const f = field();
    const rows = [...list.querySelectorAll("a")];
    if (e.key === "Escape" && !box.hidden) { e.preventDefault(); if (open()) close(); else place([]); f?.focus(); return; }
    // The rows stand above the field: ↑ leaves it for the nearest, ↓ comes back.
    if (!box.hidden && rows.length && (e.key === "ArrowUp" || e.key === "ArrowDown")) {
      const items = [...rows, f];
      const i = items.indexOf(document.activeElement);
      if (i < 0) return;
      e.preventDefault();
      items[Math.min(Math.max(i + (e.key === "ArrowUp" ? -1 : 1), 0), items.length - 1)].focus();
      return;
    }
    if (e.key !== "Enter" || e.shiftKey || e.isComposing || e.target !== f) return;
    e.preventDefault();
    // Enter in the box: with the launcher open it submits the query to the
    // economy; with the members listed it takes the nearest; otherwise it
    // sends. Shift+Enter breaks the line.
    const q = f.value.slice(1).trim();
    if (open()) { if (q) search(q); }
    else if (!box.hidden && rows.length) rows[rows.length - 1].click();
    else if (f.value.trim()) form.requestSubmit();
  });
}

// The panel's rows, its sort switch and its way back. A row opens its action
// in the panel over the results, which are held with their scroll position; a
// sort tab arranges the rows by the rank the server numbered them with, so a
// search is never run twice; ← Results puts the held results back. Rows and
// tabs are Discover's links, the no-JS path.
document.addEventListener("click", (e) => {
  const panel = document.getElementById("panel");
  if (!panel?.contains(e.target)) return;
  const link = e.target.closest(".action-list a[href]");
  const tab = e.target.closest(".tabs .tab");
  if (link && /^\/(actions\/[^/]+|discovered)$/.test(new URL(link.href).pathname)) {
    e.preventDefault();
    held = { nodes: [...panel.childNodes], top: panel.scrollTop };
    openPanel(link.href, true);
  } else if (tab) {
    e.preventDefault();
    const by = "data-rank-" + (new URL(tab.href).searchParams.get("sort") || "relevance");
    const rows = panel.querySelector(".action-list");
    rows?.replaceChildren(...[...rows.children].sort((a, b) => a.getAttribute(by) - b.getAttribute(by)));
    light(tab);
  } else if (e.target.closest("[data-panel-back]") && held) {
    panel.replaceChildren(...held.nodes);
    panel.scrollTop = held.top;
    held = null;
  } else if (e.target.closest("[data-panel-close]")) {
    panel.replaceChildren();
    held = null;
  }
});

// A search refused because its price moved names the new price in the panel.
// The launcher's terms are then stale, so the catalog file is read afresh at
// the next `/`: its row names what a search costs now, and the next search is
// pinned to it rather than refused again.
document.body.addEventListener("htmx:afterSwap", (e) => {
  if (e.target.id === "panel" && e.target.querySelector('input[name="lookup_quote_hash"]')) catalog = null;
});

// A success from a room is the composer's draft (requirements §6.7): the
// result, after a blank line when something was typed, and the panel is gone.
// Sending is the person's; the receipt stays in Transactions. A failure,
// refusal or pending result stays in the panel.
document.body.addEventListener("htmx:afterSwap", (e) => {
  const text = e.target.querySelector?.(":scope > .outcome.success[data-draft]")?.dataset.draft;
  const field = document.querySelector("form[data-composer] [data-fields] textarea:not([disabled])");
  if (text === undefined || !field) return;
  field.value = field.value.trim() ? field.value.replace(/\s+$/, "") + "\n\n" + text : text;
  field.dispatchEvent(new Event("input", { bubbles: true })); // the arrow follows the box
  field.focus();
  document.getElementById("panel")?.replaceChildren();
  held = null;
});

// A narrow screen's drawer: the trigger slides the sidebar in over the page
// and the scrim or Escape takes it away. It is never stored, so any page
// loads with it closed. Without JavaScript the trigger is ?drawer=open, which
// the server stamps the same way.
document.addEventListener("click", (e) => {
  if (e.target.closest("[data-drawer-open]")) { e.preventDefault(); document.documentElement.dataset.drawer = "open"; }
  else if (e.target.closest("[data-drawer-close]")) { e.preventDefault(); delete document.documentElement.dataset.drawer; }
});
document.addEventListener("keydown", (e) => {
  if (e.key === "Escape" && document.documentElement.dataset.drawer) delete document.documentElement.dataset.drawer;
});

// Fill from chat (requirements §6.2): the planner prepares the form's fields
// from the conversation the panel was opened from, and from a result carried
// into it. The fields come back as the form's own — or, for a form with none,
// the arguments into the editor — and nothing runs: the person still presses
// the action's own button. A refusal is shown above the buttons and the fields
// stay as they were.
document.addEventListener('click', async (e) => {
  const btn = e.target.closest('[data-fill]');
  if (!btn) return;
  const form = btn.closest('form');
  const fields = form.querySelector('[data-fields]');
  const editor = form.querySelector('[data-raw] textarea');
  const error = form.querySelector('[data-fill-error]');
  const say = (text) => { error.textContent = text; error.hidden = !text; };
  const csrf = document.querySelector('meta[name="csrf-token"]')?.content ?? '';
  btn.disabled = true;
  try {
    const res = await fetch(btn.dataset.fill, {
      method: 'POST', body: new URLSearchParams(new FormData(form)), credentials: 'same-origin', headers: { 'X-CSRF-Token': csrf },
    });
    const text = await res.text();
    if (!res.ok) { say(text.trim()); return; }
    if (fields && !(res.headers.get('Content-Type') || '').startsWith('application/json')) {
      fields.innerHTML = text;
    } else if (editor) {
      editor.value = text;
    }
    say('');
  } catch {
    say('The server did not answer.');
  } finally {
    btn.disabled = false;
  }
});
