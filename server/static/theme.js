// Theme toggle progressive enhancement. The toggle is a real <form> that POSTs
// to /theme and works fully without JavaScript (R8.6); this only adds two
// niceties: it forwards the kernel CSRF token on htmx requests, and it applies
// the new theme instantly on click to avoid a full reload flash.

// Forward the CSRF token (read from the <meta> tag) on every htmx request so
// state-changing htmx calls pass gorilla/csrf (R5.5).
document.body && document.addEventListener("htmx:configRequest", (e) => {
  const meta = document.querySelector('meta[name="csrf-token"]');
  if (meta && meta.content) {
    e.detail.headers["X-CSRF-Token"] = meta.content;
  }
});

// Optimistic theme application: the server still persists the choice via the
// cookie set by the POST, this just avoids the visual flash.
for (const form of document.querySelectorAll("form.theme-toggle")) {
  form.addEventListener("submit", () => {
    const next = form.querySelector('input[name="theme"]').value;
    document.documentElement.setAttribute("data-theme", next);
  });
}
