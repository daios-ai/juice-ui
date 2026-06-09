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

// Keep-session-open checkbox: show/hide the budget field.
for (const cb of document.querySelectorAll('[data-keep-open-toggle]')) {
  const budget = cb.closest('form')?.querySelector('[data-keep-open-budget]');
  if (!budget) continue;
  const sync = () => { budget.hidden = !cb.checked; };
  cb.addEventListener('change', sync);
  sync();
}

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
