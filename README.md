# srvmon — central server monitoring

**[🇮🇷 راهنمای فارسی](README.fa.md)**

A small monitoring system in the visual style of the 3x-ui panel overview: the
same vital tiles, live sparklines, throughput card and system strip — but for a
fleet of servers instead of one box, and with none of the VPN panel.

One **hub** runs on a central server and serves the dashboard. Every other
server runs a lightweight **agent** that pushes its metrics to the hub over
HTTPS. Agents only ever make outbound connections, so they work behind NAT and
a closed firewall.

```
  server A ──┐
  server B ──┼── HTTPS push (bearer token, every 2s) ──▶  hub  ──▶  dashboard
  server C ──┘                                             │
                                                           └──▶  Telegram alerts
```

## Quick start

On the central server:

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/lalash/srvmon/main/scripts/install-hub.sh) --base-url https://monitor.example.com
```

Open the printed URL, sign in with the generated password, then **Servers → Add
server**. You get a one-liner to paste on each machine you want to watch:

```bash
sudo bash <(curl -fsSL https://monitor.example.com/install-agent.sh) --hub https://monitor.example.com --token <token> --name berlin-1
```

That machine appears on the dashboard within a couple of seconds.

## What it collects

Per server, every two seconds: CPU utilisation (EMA-smoothed), memory, swap,
disk usage, upload/download rate and cumulative traffic, TCP and UDP connection
counts, load averages, process count, uptime, public IPv4/IPv6, OS, kernel,
architecture and CPU model.

## Features

- **Fleet overview** — average CPU/memory/storage tiles, total throughput, and
  a card per server with live meters and a CPU sparkline.
- **Per-server detail** — the full overview layout: CPU / memory / swap /
  storage tiles, a throughput chart with tooltips, a connections chart, and a
  system strip. Range selector: live, 1h, 6h, 24h, 7d, 30d.
- **History** — one point persisted per server every 30s, kept 8 days by
  default, averaged into buckets when charted.
- **Alerts** — CPU / memory / disk thresholds with a sustain count to filter
  spikes, plus offline detection. Delivered to Telegram and recorded in an
  events log.
- **Accounts** — cookie session login, bcrypt passwords, login rate limiting.
- **Bilingual UI** — English and Persian (RTL), light and dark themes.

## Installing the hub

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/lalash/srvmon/main/scripts/install-hub.sh) --base-url https://monitor.example.com
```

The script installs Go if it is missing, fetches the source into `/opt/srvmon`,
builds the hub and the Linux agents (amd64/arm64/arm), installs a systemd unit
and starts it. On first run it prints a generated admin password — that line is
the only place it appears. Pass `--admin-password` to choose one yourself.

| Flag | Default | Purpose |
| --- | --- | --- |
| `--addr` | `:8080` | listen address |
| `--base-url` | — | public URL baked into generated install commands |
| `--data-dir` | `/var/lib/srvmon` | database and agent builds |
| `--cert` / `--key` | — | terminate TLS directly instead of behind a proxy |
| `--admin-user` | `admin` | first operator name |
| `--admin-password` | generated | first operator password |
| `--uninstall` | — | remove the service and binary, keep the database |

Configuration lives in `/etc/srvmon/hub.conf` and is read by the unit; edit it
and `systemctl restart srvmon-hub`. Re-running the installer updates the hub in
place. Logs: `journalctl -u srvmon-hub -f`.

### TLS

Either point the hub at a certificate (`--cert` / `--key`), or put it behind a
reverse proxy. With nginx, the only non-obvious requirement is that the SSE
stream must not be buffered:

```nginx
location / {
    proxy_pass http://127.0.0.1:8080;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_buffering off;
    proxy_read_timeout 1h;
}
```

The dashboard falls back to polling if the stream is buffered anyway, so a
misconfigured proxy degrades rather than breaks.

## Connecting a server

In the dashboard, open **Servers → Add server**. You get a token and a
ready-to-paste command. On the server you want to monitor:

```bash
sudo bash <(curl -fsSL https://monitor.example.com/install-agent.sh) --hub https://monitor.example.com --token <token> --name berlin-1
```

That downloads the matching agent build from the hub, writes
`/etc/srvmon/agent.conf` (mode 600), installs a systemd unit and starts it.

Remove an agent with `--uninstall`. Follow it with
`journalctl -u srvmon-agent -f`. If the hub uses a self-signed certificate, add
`--insecure`.

## Local development

```bash
make tidy     # resolve dependencies (needs network, run once)
make run      # hub on :8080 with ./dev.db
```

Then run an agent against it:

```bash
go run ./cmd/agent -hub http://localhost:8080 -token <token> -name local -once
```

`make build` produces `bin/srvmon-hub` plus `bin/srvmon-agent-linux-{amd64,arm64,arm}`.
The dashboard is plain HTML/CSS/ES modules embedded with `embed.FS` — there is
no frontend build step, so editing `internal/hub/web/` and restarting the hub
is the whole loop.

## Layout

```
cmd/hub          central server binary
cmd/agent        per-server agent binary
internal/metrics gopsutil collection; the agent/hub JSON contract
internal/hub     store (SQLite), HTTP API, SSE, alerts, embedded dashboard
internal/hub/web the dashboard itself (html/css/js, no build step)
scripts          hub installer
```

Dependencies: `gopsutil` (metrics), `modernc.org/sqlite` (pure-Go SQLite, no
CGo) and `golang.org/x/crypto` (bcrypt). Nothing else.

## API

Agent ingest (bearer token):

| Method | Path | Purpose |
| --- | --- | --- |
| POST | `/api/agent/push` | submit one snapshot, receive the desired interval |

Operator API (session cookie):

| Method | Path | Purpose |
| --- | --- | --- |
| POST | `/api/auth/login` · `/api/auth/logout` | session |
| GET | `/api/auth/me` | current operator |
| GET | `/api/dashboard` | full payload |
| GET | `/api/stream` | SSE live feed |
| GET/POST | `/api/servers` | list (with tokens) / create |
| PATCH/DELETE | `/api/servers/{id}` | rename / delete |
| POST | `/api/servers/{id}/token` | rotate the agent token |
| GET | `/api/servers/{id}/history?range=1h` | bucketed history |
| GET/POST | `/api/settings` | alert and Telegram configuration |
| POST | `/api/settings/telegram/test` | send a test message |
| GET | `/api/alerts` | alert event log |
| POST | `/api/account` | change username / password |

## Security notes

- Agent tokens are stored in plaintext in the hub database so the install
  command can be re-displayed; the database directory is created 0750 and the
  agent config file 0600. Rotate a token from the servers table if it leaks.
- The Telegram bot token never round-trips to the browser — saving an empty
  field keeps the stored value, and `-` clears it.
- Mutating requests require a same-origin `Origin` header, and the session
  cookie is `HttpOnly` + `SameSite=Lax`, `Secure` behind TLS.
- `/download/agent/*` is deliberately unauthenticated: the installing machine
  has no credential yet, and the agent binary is not a secret.

## Licence

MIT
