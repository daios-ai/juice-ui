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
const MAX_LINES = 10;
function resizeTA(ta) {
  const cs = getComputedStyle(ta);
  const lineH = parseFloat(cs.lineHeight);
  const padV = parseFloat(cs.paddingTop) + parseFloat(cs.paddingBottom);
  const minH = lineH * (parseInt(ta.getAttribute('rows')) || 3) + padV;
  const maxH = lineH * MAX_LINES + padV;
  ta.style.height = 'auto';
  const sh = ta.scrollHeight;
  ta.style.height = Math.max(minH, Math.min(sh, maxH)) + 'px';
  ta.style.overflowY = sh > maxH ? 'auto' : 'hidden';
}
for (const ta of document.querySelectorAll('textarea')) {
  ta.style.resize = 'none';
  ta.addEventListener('input', () => resizeTA(ta));
  resizeTA(ta);
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
