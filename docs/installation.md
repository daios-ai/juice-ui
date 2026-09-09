# Installation & filesystem convention — the juice family

Every juice-family binary (`juice`, `juice-ui`, `juice-agent`, …) keeps its state
under **one root, one subdirectory per component**, so they coexist and the whole
suite backs up or relocates as a unit.

```
$JUICE_HOME                       (default ~/.juice; env-overridable, same on macOS)
├── kernels/<instance>/   one kernel: juice.db, config.json, the rail key, serve.lock
├── client/               config.json (the kernels and contexts this client knows),
│                         credentials/<context>.json (one session each, 0600)
├── ui/                   config.json, state.json (+ chat.db)
├── agents/<name>/        one agent's configuration, persona, memory
└── services/<name>/      one installed service
```

This is the layout `juicedocs/ecosystem-standard.md` binds every juice program
to. The UI keeps only `ui/`; it *reads* `client/config.json` to name its kernel
by context, and touches nothing else.

```
$PREFIX/bin/   the binaries (PREFIX ?= ~/.local)
```

Rules each repo follows:

1. Read `JUICE_HOME` (default `~/.juice`); keep all state under
   `$JUICE_HOME/<component>/`.
2. **Fixed default, never cwd-relative** — attach to the same state wherever
   launched.
3. The binary `MkdirAll`s its own subdir (`0700`) before first write.
4. The installer touches only `$PREFIX/bin` (`mkdir -p` + `install`, plus
   `uninstall`); never `$JUICE_HOME`.
5. **Same role → same filename across every component** (the subdir is the
   namespace): `config.json` (operator, hand-written), `state.json` (machine
   state), `tokens/` — never a component-prefixed name like `juice.json` or
   `juice-ui.json`. Databases are named for their content (`chat.db`).
6. Migrate keep-old-if-present: never auto-move a key or live DB; log a one-line
   manual move.

Single home (Geth/IPFS/Cargo lineage) over the XDG split: this state is precious,
not purgeable, and one relocatable root beats three scattered trees.

**juice-ui** is conformant (`$JUICE_HOME/ui/`, creates its own dir, fixed default,
`mkdir -p` + `uninstall`). An existing deployment keeps working untouched — a legacy
cwd `juice-ui.json` or `~/.config|.local/share/juice-ui/chat.db` is read in place and
logged; migrate deliberately:

```sh
# stop juice-ui, then:
mkdir -p ~/.juice/ui
mv juice-ui.json ~/.juice/ui/state.json
mv ~/.config/juice-ui/chat.db{,-wal,-shm} ~/.juice/ui/   # -wal/-shm may not exist
# restart
```

**Open (kernel side):** confirm `JUICE_HOME`/`~/.juice`, and whether the kernel
migrates its files from `~/.juice/` into `~/.juice/kernel/` (recommended, symmetric)
or stays at the root.
