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
    for (const ta of form.querySelectorAll("textarea")) resizeTA(ta);
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

// Auto-resize textareas: grow with content up to 10 visible lines, then scroll.
function resizeTA(ta) {
  const cs = getComputedStyle(ta);
  let lineH = parseFloat(cs.lineHeight);
  if (isNaN(lineH)) lineH = parseFloat(cs.fontSize) * 1.4;
  const padV = parseFloat(cs.paddingTop) + parseFloat(cs.paddingBottom);
  const maxH = lineH * 10 + padV;
  ta.style.height = 'auto';
  const sh = ta.scrollHeight;
  ta.style.height = Math.min(sh, maxH) + 'px';
  ta.style.overflowY = sh > maxH ? 'auto' : 'hidden';
}
for (const ta of document.querySelectorAll('textarea')) {
  ta.style.resize = 'none';
  ta.addEventListener('input', () => resizeTA(ta));
  resizeTA(ta);
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

// The mobile nav drawer is a <dialog> opened via [data-modal], so showModal()
// supplies its focus trap, Escape handling and light dismiss. Nothing here.

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

// Repeatable rows ([data-rows]): the http parameter rows and a schema array's
// rows (§6.2). Add clones the last row (or the kept template of an emptied
// container) and clears it; when the container names
// its array's path, the clone's index segment is rewritten to one past the
// highest in use — in every name, id, for and nested data-rows — so a removed
// index is never reused and a nested array keeps its own rows. Remove drops
// the row. Cloned rows inherit the template's disabled state, so they stay
// consistent with the kind toggle above.
function wireRows(box) {
  const path = box.dataset.rows;
  const rows = () => [...box.children].filter((el) => el.matches('[data-row]'));
  // A row's own Remove is its direct child, and its own containers are the
  // nearest ones: a nested array's buttons belong to the nested rows.
  const wire = (row) => {
    [...row.children].find((el) => el.matches('[data-row-remove]'))?.addEventListener('click', () => row.remove());
    for (const inner of row.querySelectorAll('[data-rows]')) {
      if (inner.closest('[data-row]') === row) wireRows(inner);
    }
  };
  rows().forEach(wire);
  // A select keeps its choice: emptied, it would submit nothing it offers.
  const clear = (row) => {
    for (const el of row.querySelectorAll('input:not([type=hidden]), textarea')) el.value = '';
    for (const el of row.querySelectorAll('input[type=checkbox]')) el.checked = false;
    return row;
  };
  // A cleared copy of the first row, kept so Add still has a row to clone once
  // every row has been removed.
  const template = rows()[0] && clear(rows()[0].cloneNode(true));
  const add = box.parentElement.querySelector(':scope > [data-row-add]');
  if (!add || !template) return;
  add.addEventListener('click', () => {
    const last = rows().at(-1);
    const source = last || template;
    const row = clear(source.cloneNode(true));
    if (path) {
      const from = path + '/' + indexOf(source, path);
      const to = path + '/' + (last ? Math.max(...rows().map((r) => indexOf(r, path))) + 1 : 0);
      for (const el of row.querySelectorAll('[name], [id], [for], [data-rows]')) {
        for (const attr of ['name', 'id', 'for', 'data-rows']) {
          const v = el.getAttribute(attr);
          if (v && (v.endsWith(from) || v.includes(from + '/'))) el.setAttribute(attr, v.replace(from, to));
        }
      }
    }
    wire(row);
    box.appendChild(row);
    for (const ta of row.querySelectorAll('textarea')) resizeTA(ta);
  });
}
// The index of a row: the segment after its array's path in its first named control.
function indexOf(row, path) {
  const el = row.querySelector('[name]');
  const m = el && el.name.slice(2).startsWith(path + '/') ? el.name.slice(2 + path.length + 1).split('/')[0] : '0';
  return Number(m) || 0;
}
for (const box of document.querySelectorAll('[data-rows]')) {
  if (!box.parentElement.closest('[data-rows]')) wireRows(box);
}

// The JSON toggle: a [data-json] block holds its fields ([data-fields]) and its
// JSON ([data-raw]); the toggle swaps them. In a form the hidden editor's
// inputs are disabled so only the visible one submits and has_fields tells the
// server which it was (the kernel validates either, R2.5); on a frozen form
// the same control shows the raw JSON. Without script the block stays as the
// server rendered it. A result arrives by htmx swap, so new content is wired
// as it lands.
function wireJSON(root) {
  for (const block of root.querySelectorAll('[data-json]')) {
    const toggle = block.querySelector('[data-json-switch]');
    const fields = block.querySelector('[data-fields]');
    const raw = block.querySelector('[data-raw]');
    if (!toggle || !fields || !raw) continue;
    const apply = (json) => {
      toggle.setAttribute('aria-checked', String(json));
      fields.hidden = json;
      raw.hidden = !json;
      for (const el of fields.querySelectorAll('input, select, textarea')) el.disabled = json;
      for (const el of block.querySelectorAll('[name=has_fields]')) el.disabled = json;
      if (json) raw.querySelector('textarea')?.dispatchEvent(new Event('input'));
    };
    toggle.addEventListener('click', () => apply(toggle.getAttribute('aria-checked') !== 'true'));
    apply(false);
  }
}
wireJSON(document);
document.body.addEventListener('htmx:afterSwap', (e) => wireJSON(e.target));

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

// Entry-screen tabs: swap the sign-in / create-account panels client-side.
// The tabs are real /login?tab= links, so with JS off the server just renders
// the requested tab (no-JS fallback); here we avoid the reload. Scoped to
// [data-tab] tabs (the entry screen); other .tabs are plain navigation links.
document.addEventListener('click', (e) => {
  const tab = e.target.closest('.tabs .tab[data-tab]');
  if (!tab) return;
  e.preventDefault();
  const want = tab.dataset.tab;
  for (const t of document.querySelectorAll('.tabs .tab[data-tab]')) {
    const on = t.dataset.tab === want;
    t.classList.toggle('active', on);
    // Move aria-current with .active — the CSS highlights on either, so a stale
    // aria-current would keep the old tab lit.
    if (on) t.setAttribute('aria-current', 'page'); else t.removeAttribute('aria-current');
  }
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
  // A textarea measured while its dialog was display:none had scrollHeight 0 and
  // was pinned to one line; now that the dialog is visible, size it to its content.
  for (const ta of dlg.querySelectorAll('textarea')) resizeTA(ta);
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
for (const dlg of document.querySelectorAll('dialog.modal-form[open], dialog.drawer[open]')) {
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
function announce(text) {
  const region = document.getElementById('live-region');
  if (!region) return;
  const toast = document.createElement('div');
  toast.className = 'toast';
  toast.textContent = text;
  region.replaceChildren(toast);
  clearTimeout(announce.timer);
  announce.timer = setTimeout(() => { region.replaceChildren(); }, 2000);
}

// Copy-to-clipboard: a [data-copy] control carries the whole value it copies —
// a key, a hash, an address, the recovery phrase — so the clipboard holds what
// a command takes, never the truncation on screen (the _copy partial).
document.body.addEventListener('click', (e) => {
  const btn = e.target.closest('[data-copy]');
  if (!btn || !navigator.clipboard) return;
  navigator.clipboard.writeText(btn.getAttribute('data-copy')).then(() => announce('Copied'));
});

// Search hints, the way a static site searches its index: the page loads the
// whole catalog's titles once (GET /search/catalog.json, each title with its
// key already normalized by the server's hintKey) and matches them in the
// browser as the person types, so typing sends no request. A hint is a query:
// each is an ordinary link that runs the search for its title. Without
// JavaScript the box submits as before.
//
// The command registry (#command-registry, inert JSON built per session) is
// listed the same way, first and marked (§6.1): a command is chrome, a title is
// counterparty content, and the two differ by what they are built from. At most
// COMMAND_LIMIT commands show when titles also match; a leading ">" lists
// commands only (">" alone, all of them).
//
// hintKey and the ranking mirror server/handlers.go (hintKey, wordsBegin, and
// catalogAsResults's title tiers); server/testdata/hints.json holds the cases
// both are checked against.
const HINT_LIMIT = 12;
const COMMAND_LIMIT = 3;

// hintKey: NFKD, combining marks removed, lowercase, every run of characters
// that are neither letters nor decimal digits one space, trimmed.
function hintKey(s) {
  return s.normalize("NFKD").replace(/\p{Mn}/gu, "").toLowerCase()
    .replace(/[^\p{L}\p{Nd}]+/gu, " ").trim();
}

// matchHints ranks entries {t, k} for the typed text: every typed word must
// begin a word of the key; keys beginning with the whole text first, then the
// other matches; shorter first, then alphabetical. An empty text matches
// nothing unless `all` asks for everything.
function matchHints(entries, q, limit, all = false) {
  const typed = hintKey(q).split(" ").filter(Boolean);
  if (typed.length === 0 && !all) return [];
  const whole = typed.join(" ");
  const hits = [];
  for (const e of entries) {
    const words = e.k.split(" ");
    if (!typed.every((t) => words.some((w) => w.startsWith(t)))) continue;
    hits.push({ ...e, rank: e.k.startsWith(whole) ? 0 : 1 });
  }
  hits.sort((a, b) => a.rank - b.rank || a.k.length - b.k.length || (a.k < b.k ? -1 : a.k > b.k ? 1 : 0));
  return hits.slice(0, limit);
}

// commandRow renders one command: a link, or a submit button bound to the
// page's command form (form="command-post"), which carries the CSRF field.
function commandRow(c) {
  const row = document.createElement(c.kind === "post" ? "button" : "a");
  row.className = "menu-item command";
  if (c.kind === "post") {
    row.type = "submit"; row.setAttribute("form", "command-post");
    row.formAction = c.href; row.name = c.field; row.value = c.value;
  } else {
    row.href = c.href;
  }
  const mark = document.createElement("span");
  mark.className = "mark"; mark.setAttribute("aria-hidden", "true"); mark.textContent = "›";
  const hint = document.createElement("span");
  hint.className = "muted small"; hint.textContent = c.hint;
  row.append(mark, c.name, " ", hint);
  return row;
}

let commands = [];
try {
  commands = JSON.parse(document.getElementById("command-registry")?.textContent || "[]")
    .map((c) => ({ t: c.name, k: hintKey(c.name), cmd: c }));
} catch (_) { commands = []; }

for (const input of document.querySelectorAll("input[data-search-hints]")) {
  const panel = input.form?.querySelector(".omni-suggest");
  if (!panel) continue;
  let catalog = null;
  const load = () => {
    catalog ??= fetch("/search/catalog.json", { credentials: "same-origin" })
      .then((r) => (r.ok ? r.json() : []))
      .catch(() => []);
    return catalog;
  };
  const render = async () => {
    const only = input.value.startsWith(">");
    const q = only ? input.value.slice(1) : input.value;
    const cmds = matchHints(commands, q, only ? HINT_LIMIT : COMMAND_LIMIT, only);
    const titles = only ? [] : matchHints(await load(), q, HINT_LIMIT - cmds.length);
    const nav = document.createElement("nav");
    nav.className = "suggest-list";
    nav.setAttribute("aria-label", "Search suggestions");
    for (const c of cmds) nav.append(commandRow(c.cmd));
    for (const e of titles) {
      const a = document.createElement("a");
      a.className = "menu-item";
      a.href = "/browse?q=" + encodeURIComponent(e.t);
      a.textContent = e.t;
      nav.append(a);
    }
    panel.replaceChildren(...(nav.children.length ? [nav] : []));
  };
  input.addEventListener("focus", load, { once: true });
  input.addEventListener("input", render);
}
