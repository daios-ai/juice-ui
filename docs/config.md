# Configuration & file locations

juice-ui is configured from three layers, **lowest precedence first**:

1. built-in defaults,
2. the operator **config file** (`config.json`),
3. **environment variables** (always win).

Environment always wins, so a container or systemd unit can override any file value
without editing the file.

## Config file

`config.json` is optional and hand-written. Its location is
`$JUICE_UI_CONFIG_FILE`, or `os.UserConfigDir()/juice-ui/config.json` by default
(`~/.config/juice-ui/config.json` on Linux, `~/Library/Application
Support/juice-ui/config.json` on macOS). A missing file is fine; a **malformed** one
— or an **unknown key** — is a hard startup error, so a typo surfaces loudly.

```json
{
  "kernel_url": "http://127.0.0.1:4040",
  "addr": "127.0.0.1:8080",
  "session_secret": "…",
  "secure": false,
  "public_url": "",
  "trusted_origins": ["ui.example.com"],
  "badge_interval_seconds": 30,
  "app_state_path": "/absolute/path/to/juice-ui.json"
}
```

Every key is optional. Notes:

- **`badge_interval_seconds`** — how often every nav badge (steps + each app) is
  polled, and the steps-badge cache TTL. File-only; default 30, clamped to ≥ 5.
- **`trusted_origins`** — additive: the listen-addr localhost defaults, plus this
  list, plus `$JUICE_UI_TRUSTED_ORIGINS`. Entries may be URLs or bare hosts.
- **Use absolute paths.** Relative paths resolve against the server's working
  directory.
- The file may hold `session_secret`, so keep it `0600`; the loader warns if it is
  group/world-readable.

`config.json` is **not** `juice-ui.json` — see below.

## Environment variables

| Variable | Overrides | Notes |
|---|---|---|
| `JUICE_UI_CONFIG_FILE` | config-file location | else `UserConfigDir/juice-ui/config.json` |
| `JUICE_KERNEL_URL` | `kernel_url` | http(s) URL; default `http://127.0.0.1:4040` |
| `JUICE_UI_ADDR` | `addr` | default `127.0.0.1:8080` |
| `JUICE_UI_SESSION_SECRET` | `session_secret` | absent ⇒ ephemeral key (sessions reset on restart) |
| `JUICE_UI_SECURE` | `secure` | `true` sets Secure cookies (behind HTTPS) |
| `JUICE_UI_PUBLIC_URL` | `public_url` | OAuth redirect base; else derived from the request |
| `JUICE_UI_TRUSTED_ORIGINS` | (adds to) `trusted_origins` | comma-separated |
| `JUICE_UI_CONFIG` | `app_state_path` | path to `juice-ui.json` (app state, not this config) |

`badge_interval_seconds` has **no** env override — it is the kind of setting the
file exists for.

## Two files, two jobs

| File | Who writes it | What it holds |
|---|---|---|
| `config.json` | the operator (you) | server settings above; read-only to the process |
| `juice-ui.json` | juice-ui (machine) | per-app provisioned state (service-account secrets, `AuthRev`, …), namespaced by app slug |

They are deliberately separate: one is hand-authored, one is machine-written. The
`JUICE_UI_CONFIG` env var names the **app-state** file (`juice-ui.json`) for
historical reasons — mind the asymmetry.

`app_state_path` (i.e. `juice-ui.json`) resolves in order: `$JUICE_UI_CONFIG` →
`app_state_path` in `config.json` → an existing `./juice-ui.json` in the working
directory (protects a deployment relying on the historical cwd default) →
`UserConfigDir/juice-ui/juice-ui.json` for a clean install.

## File locations

| Artifact | Linux | macOS |
|---|---|---|
| binary | `$PREFIX/bin/juice-ui` (`make install`, `PREFIX=~/.local`) | same |
| operator config (`config.json`) | `~/.config/juice-ui/` | `~/Library/Application Support/juice-ui/` |
| app state (`juice-ui.json`) | `~/.config/juice-ui/` (or cwd, historical) | `~/Library/Application Support/juice-ui/` |
| app data (`chat.db`) | `$XDG_DATA_HOME/juice-ui/` or `~/.local/share/juice-ui/` | `~/Library/Application Support/juice-ui/` |

`make install` installs the **binary only** (user-scoped); it never writes into your
config or data directories. juice-ui creates its data directory on first run.

### Migrating `chat.db` (existing installs)

Older builds kept `chat.db` under the **config** dir
(`~/.config/juice-ui/chat.db`). juice-ui now prefers the **data** dir
(`~/.local/share/juice-ui/chat.db`), but **keeps using an existing legacy store in
place** — it is live data, and its WAL sidecars make an automatic move unsafe. To
migrate deliberately (Linux):

```sh
# stop juice-ui first
mkdir -p ~/.local/share/juice-ui
mv ~/.config/juice-ui/chat.db ~/.config/juice-ui/chat.db-wal ~/.config/juice-ui/chat.db-shm \
   ~/.local/share/juice-ui/    # the -wal/-shm files may not exist; that's fine
# restart juice-ui
```

On macOS the config and data directories coincide, so nothing moves.
