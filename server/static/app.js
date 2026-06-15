// Progressive enhancement. The theme and sidebar toggles are real <form> POSTs
// that work fully without JavaScript (server-read cookies, R8.6); this only adds
// two niceties — forwarding the CSRF token on htmx requests, and applying the
// new theme/sidebar state instantly to avoid a reload flash.

document.addEventListener("htmx:configRequest", (e) => {
  const meta = document.querySelector('meta[name="csrf-token"]');
  if (meta && meta.content) {
    e.detail.headers["X-CSRF-Token"] = meta.content;
  }
});

// Optimistic theme toggle.
for (const form of document.querySelectorAll("form.theme-toggle")) {
  form.addEventListener("submit", () => {
    document.documentElement.setAttribute("data-theme", form.querySelector('input[name="theme"]').value);
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
    backdrop.hidden = false;
    backdrop.removeAttribute('aria-hidden');
    btnConfirm.focus();
  }
  function closeModal() {
    backdrop.hidden = true;
    backdrop.setAttribute('aria-hidden', 'true');
    pending = null;
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

  backdrop.addEventListener('click', (e) => {
    if (!e.target.closest('.modal-box')) closeModal();
  });

  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape' && !backdrop.hidden) closeModal();
  });
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
