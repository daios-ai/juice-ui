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

// A one-shot call form (a step completion) is spent once it succeeds: when its
// result swaps in a success outcome, hide the form so only the result remains
// (a reload moves the step to History). Failures/rejections leave the form up.
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
  for (const m of document.querySelectorAll('.menu:not([hidden]):not(.mobile-drawer)')) {
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

// Close mobile drawer when clicking outside it.
document.addEventListener('click', (e) => {
  for (const d of document.querySelectorAll('details.mobile-menu[open]')) {
    if (!d.contains(e.target)) d.removeAttribute('open');
  }
});

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

// Kind: reveal the http-only fields (method, params) only for kind=http.
// Inputs in the hidden section are disabled so they are not submitted and the
// param_name/param_in rows stay index-aligned for server-side pairing.
for (const sel of document.querySelectorAll('select[name="kind"]')) {
  const form = sel.closest('form');
  function update() {
    const show = sel.value === 'http';
    for (const sec of form.querySelectorAll('[data-http-only]')) {
      sec.hidden = !show;
      for (const el of sec.querySelectorAll('input, select')) el.disabled = !show;
    }
  }
  sel.addEventListener('change', update);
  update();
}

// Repeatable parameter rows (create and edit forms): clone the first row on add,
// drop a row on remove. Cloned rows inherit the template's disabled state, so
// they stay consistent with the kind toggle above.
for (const box of document.querySelectorAll('[data-param-rows]')) {
  const tmpl = box.querySelector('[data-param-row]');
  const wire = (row) => {
    const rm = row.querySelector('[data-param-remove]');
    if (rm) rm.addEventListener('click', () => row.remove());
  };
  box.querySelectorAll('[data-param-row]').forEach(wire);
  const add = box.parentElement.querySelector('[data-param-add]');
  if (add && tmpl) {
    add.addEventListener('click', () => {
      const row = tmpl.cloneNode(true);
      for (const i of row.querySelectorAll('input')) i.value = '';
      wire(row);
      box.appendChild(row);
    });
  }
}

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
      if (window.history && location.search.includes('form=')) {
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
    if (window.history && location.search.includes('form=')) {
      history.replaceState(null, '', location.pathname);
    }
  }
}
