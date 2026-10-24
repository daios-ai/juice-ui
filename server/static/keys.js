// Keyboard layer: vim-style shortcuts, all progressive enhancement — every key
// targets a real link or form, so nothing here is the only way to do anything.
// Actions and pages live in the composer's launcher (app.js).

import { openDialog } from "/static/app.js";

const help = document.getElementById("help");

// --- global shortcuts ---
const GO = { h: "/", d: "/browse", i: "/inbox", k: "/workshop", w: "/wallet", y: "/settings", n: "/admin/peers", x: "/admin" };
let goArmed = false;

function typingContext(el) {
  return el && (el.tagName === "INPUT" || el.tagName === "TEXTAREA" || el.tagName === "SELECT" || el.isContentEditable);
}

document.addEventListener("keydown", (e) => {
  if (typingContext(e.target)) return; // don't hijack typing
  if (e.ctrlKey || e.metaKey || e.altKey) return;

  if (goArmed) {
    goArmed = false;
    const dest = GO[e.key.toLowerCase()];
    if (dest) { e.preventDefault(); location.href = dest; }
    return;
  }
  switch (e.key) {
    case "g": goArmed = true; setTimeout(() => (goArmed = false), 1200); break;
    case "/": {
      // The composer's launcher where there is a composer, else the page's
      // own search box (Discover).
      const form = document.querySelector("form[data-composer]");
      if (form?.launch) { e.preventDefault(); form.launch(); break; }
      const q = document.querySelector('main input[type="search"], main input[name="q"]');
      if (q) { e.preventDefault(); q.focus(); }
      break;
    }
    case "?": if (help) { e.preventDefault(); openDialog(help); } break;
    case "j": moveRow(1); break;
    case "k": moveRow(-1); break;
    case "Enter": {
      const row = document.activeElement;
      if (row && row.classList.contains("rowable") && row.dataset.href) { e.preventDefault(); location.href = row.dataset.href; }
      break;
    }
  }
});

// j/k row navigation over .rowable rows (table rows and list rows alike).
function moveRow(dir) {
  const rows = [...document.querySelectorAll(".rowable")];
  if (rows.length === 0) return;
  let idx = rows.indexOf(document.activeElement);
  idx = idx < 0 ? (dir > 0 ? 0 : rows.length - 1) : Math.min(Math.max(idx + dir, 0), rows.length - 1);
  rows[idx].focus();
}
