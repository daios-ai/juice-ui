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
  "kernel": "alpha",
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

- **`kernel`** — the name this UI's kernel is recorded under in
  `$JUICE_HOME/client/config.json`, the record every juice program shares: its
  endpoint, the key it must present, and its network. Register one with
  `juice kernel add URL NAME`. When set, `kernel_url` is ignored and reported as
  such. The UI names its kernel explicitly and never follows the CLI's selected
  login, so `juice auth use` in a terminal cannot move a running interface onto
  another kernel.

  One UI process serves one kernel, as one `juice kernel serve` process serves
  one kernel home. To offer two, run two.

  The UI reads only the `kernels` half of that file. It never reads or writes
  `client/credentials/`: it authenticates browser people and holds each one's
  tokens in its own server-side session, so the operator's logins stay the
  operator's. A browser sign-out ends that browser session and nothing else.

  (`context` was the key before the client records held logins. It is refused by
  name, with the remedy, rather than silently ignored.)
- **`kernel_url`** — a bare address, for a machine with no client records. The
  kernel is then identified from its `/health` before the application serves:
  until it answers, the UI serves the kernel screen and, for a loopback
  address, installs, creates or starts this installation's kernel (the kernel
  gate, `requirements.md` §1); a kernel elsewhere is waited for. The UI keys
  everything durable on which kernel it is for and never guesses.

- **`badge_interval_seconds`** — how often every nav badge (tasks + each app) is
  polled, and the task-badge cache TTL. File-only; default 30, clamped to ≥ 5.
- **`trusted_origins`** — additive: the listen-addr localhost defaults, plus this
  list, plus `$JUICE_UI_TRUSTED_ORIGINS`. Entries may be URLs or bare hosts.
- The file may hold `session_secret`, so keep it `0600`.

`config.json` (operator, hand-written) is distinct from `state.json` (machine, the
app-state file holding provisioned secrets and `AuthRev`) — see installation.md.
`state.json` is keyed by the kernel the state was provisioned on
(`{"kernels":{"<public_key>":{"apps":{…}}}}`), so a secret minted on one kernel is
never replayed on another; a file from before that keying is adopted by the
current kernel once, and rewritten keyed.

## Environment variables

| Variable | Overrides | Notes |
|---|---|---|
| `JUICE_HOME` | the whole home | relocates `$JUICE_HOME/ui/` (config, state, data); default `~/.juice`. The only path knob. |
| `JUICE_KERNEL` | `kernel` | a kernel name from `client/config.json` |
| `JUICE_KERNEL_URL` | `kernel_url` | http(s) URL; default `http://127.0.0.1:4040`; ignored when `kernel` names one |
| `JUICE_UI_ADDR` | `addr` | default `127.0.0.1:8080` |
| `JUICE_UI_SESSION_SECRET` | `session_secret` | absent ⇒ ephemeral key (sessions reset on restart) |
| `JUICE_UI_SECURE` | `secure` | `true` sets Secure cookies (behind HTTPS) |
| `JUICE_UI_PUBLIC_URL` | `public_url` | OAuth redirect base; else derived from the request |
| `JUICE_UI_TRUSTED_ORIGINS` | (adds to) `trusted_origins` | comma-separated |

`badge_interval_seconds` has **no** env override — it is the kind of setting the
file exists for. It also paces the kernel-identity re-read behind the session
pin (§5), so a swapped server is noticed within one heartbeat.

**The money unit is not configured.** A kernel serves one world for life and
`GET /health` says how its amounts scale (`decimals`) and what that money is
called (`network`). The UI reads it from the live identity; there is no key for
it, and there must not be one — a local override could only ever disagree with
the ledger.
