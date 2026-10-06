// Keyboard layer: vim-style shortcuts, all progressive enhancement — every key
// targets a real link or form, so nothing here is the only way to do anything.
// Commands live in the search pill (app.js lists the registry among the hints).

import { openDialog } from "/static/app.js";

const help = document.getElementById("help");

// --- search hints ---
// The hints are links, and a post command a button, in the search form (app.js
// renders them). ↓/↑ move focus between the input and the rows; Esc leaves the
// form, which hides the panel (CSS :focus-within). Tab and Enter work natively.
document.addEventListener("keydown", (e) => {
  const form = e.target.closest && e.target.closest(".omni");
  if (!form) return;
  if (e.key === "Escape") { e.target.blur(); return; }
  if (e.key !== "ArrowDown" && e.key !== "ArrowUp") return;
  const rows = [...form.querySelectorAll(".omni-suggest a, .omni-suggest button")];
  if (rows.length === 0) return;
  e.preventDefault();
  const items = [form.querySelector('input[name="q"]'), ...rows];
  const i = items.indexOf(e.target) + (e.key === "ArrowDown" ? 1 : -1);
  items[Math.min(Math.max(i, 0), items.length - 1)].focus();
});

// --- global shortcuts ---
const GO = { h: "/", b: "/browse", i: "/inbox", k: "/workshop", w: "/wallet", y: "/settings", n: "/admin/peers", x: "/admin" };
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
      const q = document.getElementById("q") || document.querySelector('input[type="search"], input[name="q"]');
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
