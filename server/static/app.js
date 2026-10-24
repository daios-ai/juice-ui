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
  // A select keeps its choice: emptied, it would submit nothing it offers. A
  // password's placeholder states what its own row holds, so a copy drops it.
  const clear = (row) => {
    for (const el of row.querySelectorAll('input:not([type=hidden]), textarea')) el.value = '';
    for (const el of row.querySelectorAll('input[type=password]')) el.placeholder = '';
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
  });
}
// The index of a row: the segment after its array's path in its first named control.
function indexOf(row, path) {
  const el = row.querySelector('[name]');
  const m = el && el.name.slice(2).startsWith(path + '/') ? el.name.slice(2 + path.length + 1).split('/')[0] : '0';
  return Number(m) || 0;
}
// Wires the outermost row containers under root: at load, and in fields the
// JSON switch has just rendered.
function wireRowsIn(root) {
  for (const box of root.querySelectorAll('[data-rows]')) {
    if (!box.parentElement.closest('[data-rows]')) wireRows(box);
  }
}
wireRowsIn(document);

// The JSON toggle: a [data-json] block holds its fields ([data-fields]) and its
// JSON ([data-raw]); the toggle swaps them. In a form the hidden editor's
// inputs are disabled so only the visible one submits and has_fields tells the
// server which it was (the kernel validates either, R2.5); on a view
// the same control shows the raw JSON. Without script the block stays as the
// server rendered it. A result arrives by htmx swap, so new content is wired
// as it lands.
//
// A form names its two conversions (data-to-json, data-to-form): a switch
// converts what is on screen into the other view exactly, or stays and shows
// the server's reason (§6.2). A typed password never enters the JSON, so it
// holds the switch until it is saved or cleared.
function wireJSON(root) {
  for (const block of root.querySelectorAll('[data-json]')) {
    const toggle = block.querySelector('[data-json-switch]');
    const fields = block.querySelector('[data-fields]');
    const raw = block.querySelector('[data-raw]');
    if (!toggle || !fields || !raw) continue;
    const editor = raw.querySelector('textarea');
    const error = block.querySelector('[data-json-error]');
    const apply = (json) => {
      toggle.setAttribute('aria-checked', String(json));
      fields.hidden = json;
      raw.hidden = !json;
      for (const el of fields.querySelectorAll('input, select, textarea')) el.disabled = json;
      for (const el of block.querySelectorAll('[name=has_fields]')) el.disabled = json;
    };
    const say = (text) => {
      error.textContent = text;
      error.hidden = !text;
      return !text;
    };
    const convert = async (json) => {
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
        if (json) {
          editor.value = text;
        } else {
          fields.innerHTML = text;
          wireRowsIn(fields);
        }
        return say('');
      } catch {
        return say('The server did not answer.');
      }
    };
    toggle.addEventListener('click', async () => {
      const json = toggle.getAttribute('aria-checked') !== 'true';
      if (!block.dataset.toJson || await convert(json)) apply(json);
    });
    apply(false);
  }
}
wireJSON(document);
document.body.addEventListener('htmx:afterSwap', (e) => { wireJSON(e.target); wireRowsIn(e.target); });

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

// The launcher (requirements §6.2): the composer's `/`. The page carries the
// registry's pages, the room's members and the room (#launcher-data, inert
// JSON); the catalog's actions are loaded once (GET /search/catalog.json) and
// matched in the browser as the person types, so typing sends no request.
// Choosing an action opens its panel beside the conversation, choosing a page
// goes there, and the last row searches Discover; nothing posts or runs. A
// first word that begins a member's address lists the members, and choosing
// one writes its address. Without JavaScript the composer is the plain post
// form and every page stays a link.
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

let launcher = null;
try { launcher = JSON.parse(document.getElementById("launcher-data")?.textContent || "null"); } catch (_) { launcher = null; }
let catalog = null;
const loadCatalog = () => (catalog ??= fetch("/search/catalog.json", { credentials: "same-origin" })
  .then((r) => (r.ok ? r.json() : []))
  .catch(() => []));

// row builds one launcher row: a link, labelled, with a muted detail.
function row(label, detail, href) {
  const a = document.createElement("a");
  a.className = "menu-item";
  a.href = href;
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

// openPanel loads an action's panel beside the conversation.
export function openPanel(id, extra = "") {
  const room = launcher?.room ? "?room=" + encodeURIComponent(launcher.room) : "?";
  window.htmx.ajax("GET", "/actions/" + encodeURIComponent(id) + "/panel" + room + extra, { target: "#panel", swap: "innerHTML" });
}

for (const form of document.querySelectorAll("form[data-composer]")) {
  const box = form.querySelector(".launcher");
  const button = form.querySelector("[data-launcher]");
  if (!box || !button || !launcher) continue;
  const q = box.querySelector("input");
  const list = box.querySelector("nav");
  const pages = (launcher.pages || []).map((p) => ({ t: p.name, k: hintKey(p.name), href: p.href }));
  const field = () => form.querySelector("[data-fields] textarea:not([disabled])");
  let after = ""; // a query string the chosen action's panel is opened with (a result to use)
  let members = false;

  const close = () => { box.hidden = true; list.replaceChildren(); after = ""; members = false; };
  form.launch = (text = "", extra = "") => {
    after = extra; members = false;
    box.hidden = false; q.hidden = false; q.value = text; q.focus();
    render();
    loadCatalog().then(render);
  };

  async function render() {
    if (box.hidden || members) return;
    const typed = q.value;
    const actions = typed.trim() ? matchHints(await loadCatalog(), typed, LAUNCH_ACTIONS) : [];
    const found = matchHints(pages, typed, typed.trim() ? LAUNCH_PAGES : pages.length, true);
    const rows = [];
    if (actions.length) rows.push(caption("Actions"));
    for (const a of actions) {
      const r = row(a.t, a.ref, a.id ? "/actions/" + encodeURIComponent(a.id) : "/discovered?ref=" + encodeURIComponent(a.ref));
      if (a.id) {
        const extra = after;
        r.addEventListener("click", (e) => { e.preventDefault(); close(); openPanel(a.id, extra); field()?.focus(); });
      }
      rows.push(r);
    }
    if (found.length) rows.push(caption("Pages"));
    for (const p of found) rows.push(row(p.t, "", p.href));
    if (typed.trim()) rows.push(row("Search “" + typed.trim() + "” →", "", "/browse?q=" + encodeURIComponent(typed.trim())));
    list.replaceChildren(...rows);
  }

  // The members whose address begins with the first word typed.
  function suggestMembers(text) {
    const word = text.split(/\s/)[0];
    const hits = word && !/\s/.test(text) ? (launcher.members || []).filter((m) => m.toLowerCase().startsWith(word.toLowerCase()) && m !== word) : [];
    if (!hits.length) { if (members) close(); return; }
    members = true; box.hidden = false; q.hidden = true;
    list.replaceChildren(...hits.map((m) => {
      const r = row(m, "", "#");
      r.addEventListener("click", (e) => {
        e.preventDefault();
        const f = field();
        f.value = m + ", " + f.value.slice(word.length).replace(/^\S*/, "");
        close(); f.focus();
      });
      return r;
    }));
  }

  button.addEventListener("click", () => (box.hidden ? form.launch() : close()));
  q.addEventListener("input", render);
  form.addEventListener("input", (e) => {
    const f = field();
    if (e.target !== f) return;
    if (f.value.startsWith("/")) { const rest = f.value.slice(1); f.value = ""; form.launch(rest); return; }
    suggestMembers(f.value);
  });
  form.addEventListener("keydown", (e) => {
    const rows = [...list.querySelectorAll("a")];
    if (e.key === "Escape" && !box.hidden) { e.preventDefault(); close(); field()?.focus(); return; }
    if (!box.hidden && (e.key === "ArrowDown" || e.key === "ArrowUp") && rows.length) {
      e.preventDefault();
      const i = rows.indexOf(document.activeElement);
      rows[Math.min(Math.max(i + (e.key === "ArrowDown" ? 1 : -1), 0), rows.length - 1)].focus();
      return;
    }
    if (e.key === "Enter" && e.target === q && rows.length) { e.preventDefault(); rows[0].click(); return; }
    // Enter sends; Shift+Enter breaks the line.
    if (e.key === "Enter" && !e.shiftKey && !e.isComposing && e.target === field()) { e.preventDefault(); form.requestSubmit(); }
  });
}

// The panel's close empties it, and the conversation has the room again.
document.addEventListener("click", (e) => {
  if (e.target.closest("[data-panel-close]")) document.getElementById("panel")?.replaceChildren();
});

// The sidebar where it is hidden (a narrow screen, or collapsed): the trigger
// slides it in over the page and the scrim or Escape takes it away. Without
// JavaScript the trigger is ?menu=open, which the server stamps the same way.
document.addEventListener("click", (e) => {
  if (e.target.closest("[data-menu-open]")) { e.preventDefault(); document.documentElement.dataset.menu = "open"; }
  else if (e.target.closest("[data-menu-close]")) { e.preventDefault(); delete document.documentElement.dataset.menu; }
});
document.addEventListener("keydown", (e) => {
  if (e.key === "Escape" && document.documentElement.dataset.menu) delete document.documentElement.dataset.menu;
});
