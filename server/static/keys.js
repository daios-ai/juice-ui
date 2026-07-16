// Keyboard layer: a command palette that mirrors the CLI, plus vim-style
// shortcuts. All progressive enhancement — every command targets a real link or
// form, so nothing here is the only way to do anything. The command registry is
// server-rendered inert JSON (CSP-safe); admin commands appear only for @sys.

import { openDialog } from "/static/app.js";

const registryEl = document.getElementById("command-registry");
let COMMANDS = [];
try {
  COMMANDS = registryEl ? JSON.parse(registryEl.textContent) : [];
} catch (_) {
  COMMANDS = [];
}

const palette = document.getElementById("palette");
const paletteInput = document.getElementById("palette-input");
const paletteList = document.getElementById("palette-list");
const help = document.getElementById("help");

// --- fuzzy subsequence match with a light word-prefix bonus ---
function score(query, text) {
  query = query.toLowerCase();
  text = text.toLowerCase();
  if (query === "") return 1;
  let qi = 0, s = 0, prevMatch = -2;
  for (let ti = 0; ti < text.length && qi < query.length; ti++) {
    if (text[ti] === query[qi]) {
      s += ti === prevMatch + 1 ? 2 : 1; // contiguity bonus
      if (ti === 0 || text[ti - 1] === " ") s += 2; // word-start bonus
      prevMatch = ti;
      qi++;
    }
  }
  return qi === query.length ? s : 0;
}

function csrfToken() {
  return document.querySelector('meta[name="csrf-token"]')?.content || "";
}

// Execute a command by its kind.
function runCommand(cmd) {
  if (!cmd) return;
  if (cmd.kind === "post") {
    const form = document.createElement("form");
    form.method = "post";
    form.action = cmd.href;
    const add = (n, v) => { const i = document.createElement("input"); i.type = "hidden"; i.name = n; i.value = v; form.appendChild(i); };
    add("gorilla.csrf.Token", csrfToken());
    for (const [k, v] of Object.entries(cmd.fields || {})) add(k, v);
    document.body.appendChild(form);
    form.submit();
    return;
  }
  if (cmd.kind === "form") {
    // If the target dialog is already on this page, open it directly; else
    // navigate to the ?form= URL (the server renders it open, no-JS friendly).
    const id = new URL(cmd.href, location.origin).searchParams.get("form");
    const dlg = id ? document.getElementById("form-" + id) : null;
    if (dlg) { closePalette(); openDialog(dlg); return; }
  }
  location.href = cmd.href;
}

let filtered = [];
let selected = 0;

function renderList() {
  const q = paletteInput.value.trim();
  filtered = COMMANDS
    .map((c) => ({ c, s: score(q, c.name + " " + (c.hint || "")) }))
    .filter((x) => x.s > 0)
    .sort((a, b) => b.s - a.s)
    .slice(0, 40)
    .map((x) => x.c);
  selected = 0;
  paletteList.innerHTML = "";
  if (filtered.length === 0) {
    const li = document.createElement("li");
    li.className = "palette-empty";
    li.textContent = "No matching commands.";
    paletteList.appendChild(li);
    return;
  }
  filtered.forEach((c, i) => {
    const li = document.createElement("li");
    li.className = "palette-item";
    li.setAttribute("role", "option");
    if (i === selected) li.setAttribute("aria-selected", "true");
    const name = document.createElement("span");
    name.className = "cmd";
    name.textContent = c.name;
    const hint = document.createElement("span");
    hint.className = "hint";
    hint.textContent = c.hint || "";
    li.append(name, hint);
    li.addEventListener("click", () => runCommand(c));
    paletteList.appendChild(li);
  });
}

function highlight() {
  [...paletteList.children].forEach((li, i) => {
    if (i === selected) li.setAttribute("aria-selected", "true");
    else li.removeAttribute("aria-selected");
  });
  paletteList.children[selected]?.scrollIntoView({ block: "nearest" });
}

function openPalette() {
  if (!palette) return;
  paletteInput.value = "";
  renderList();
  openDialog(palette);
  paletteInput.focus();
}
function closePalette() { palette?.close(); }

if (palette) {
  paletteInput.addEventListener("input", renderList);
  paletteInput.addEventListener("keydown", (e) => {
    if (e.key === "ArrowDown") { e.preventDefault(); selected = Math.min(selected + 1, filtered.length - 1); highlight(); }
    else if (e.key === "ArrowUp") { e.preventDefault(); selected = Math.max(selected - 1, 0); highlight(); }
    else if (e.key === "Enter") { e.preventDefault(); runCommand(filtered[selected]); }
  });
}

// --- global shortcuts ---
const GO = { b: "/browse", i: "/inbox", k: "/workshop", w: "/wallet", y: "/you", n: "/admin/peers", x: "/admin" };
let goArmed = false;

function typingContext(el) {
  return el && (el.tagName === "INPUT" || el.tagName === "TEXTAREA" || el.tagName === "SELECT" || el.isContentEditable);
}
function topDialogOpen() {
  return [...document.querySelectorAll("dialog")].some((d) => d.open);
}

document.addEventListener("keydown", (e) => {
  // Ctrl/Cmd+K always opens the palette.
  if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "k") {
    e.preventDefault();
    openPalette();
    return;
  }
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

// j/k row navigation over .rowable table rows.
function moveRow(dir) {
  const rows = [...document.querySelectorAll("tr.rowable")];
  if (rows.length === 0) return;
  let idx = rows.indexOf(document.activeElement);
  idx = idx < 0 ? (dir > 0 ? 0 : rows.length - 1) : Math.min(Math.max(idx + dir, 0), rows.length - 1);
  rows[idx].focus();
}
