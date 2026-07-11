# Configuration

juice-ui is configured from three layers, **lowest precedence first**: built-in
defaults, the operator **config file** (`config.json`), then **environment
variables** (which always win, so a container or systemd unit can override any file
value without editing the file).

For **where files live**, see [installation.md](installation.md); this document
covers the settings. In short: `config.json` and the machine-written `state.json`
sit in `$JUICE_HOME/ui/` (default `~/.juice/ui/`).

## Config file

`config.json` is optional and hand-written (see installation.md for its path). A
missing file is fine; a **malformed** one — or an **unknown key** — is a hard
startup error, so a typo surfaces loudly.

```json
{
  "kernel_url": "http://127.0.0.1:4040",
  "addr": "127.0.0.1:8080",
  "session_secret": "…",
  "secure": false,
  "public_url": "",
  "trusted_origins": ["ui.example.com"],
  "badge_interval_seconds": 30
}
```

Every key is optional:

- **`badge_interval_seconds`** — how often every nav badge (steps + each app) is
  polled, and the steps-badge cache TTL. File-only; default 30, clamped to ≥ 5.
- **`trusted_origins`** — additive: the listen-addr localhost defaults, plus this
  list, plus `$JUICE_UI_TRUSTED_ORIGINS`. Entries may be URLs or bare hosts.
- The file may hold `session_secret`, so keep it `0600`.

`config.json` (operator, hand-written) is distinct from `state.json` (machine, the
app-state file holding provisioned secrets and `AuthRev`) — see installation.md.

## Environment variables

| Variable | Overrides | Notes |
|---|---|---|
| `JUICE_HOME` | the whole home | relocates `$JUICE_HOME/ui/` (config, state, data); default `~/.juice`. The only path knob. |
| `JUICE_KERNEL_URL` | `kernel_url` | http(s) URL; default `http://127.0.0.1:4040` |
| `JUICE_UI_ADDR` | `addr` | default `127.0.0.1:8080` |
| `JUICE_UI_SESSION_SECRET` | `session_secret` | absent ⇒ ephemeral key (sessions reset on restart) |
| `JUICE_UI_SECURE` | `secure` | `true` sets Secure cookies (behind HTTPS) |
| `JUICE_UI_PUBLIC_URL` | `public_url` | OAuth redirect base; else derived from the request |
| `JUICE_UI_TRUSTED_ORIGINS` | (adds to) `trusted_origins` | comma-separated |

`badge_interval_seconds` has **no** env override — it is the kind of setting the
file exists for.
