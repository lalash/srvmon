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
bash <(curl -fsSL https://raw.githubusercontent.com/lalash/srvmon/main/scripts/install-hub.sh)
```

It asks what it needs — certificate mode, domain, port, admin password — then
installs Go, builds, obtains the certificate and starts the service:

```
? How should the dashboard be served?
  1. HTTPS with a free Let's Encrypt certificate for a domain (auto-renews)
  2. HTTPS for this server's IP address — no domain needed (~6-day cert, auto-renews)
  3. HTTPS with certificate files you already have
  4. Plain HTTP — only safe behind a reverse proxy or on a private network
? Choose [1]: 1
? Domain name (e.g. monitor.example.com): monitor.example.com
? Port for the dashboard [443]:
? Set the admin password yourself? (otherwise one is generated) [y/N]:
? ufw is active — open port 443? [Y/n]:
```

No domain? Pick **2**. Let's Encrypt issues certificates for bare IP addresses
under its short-lived profile, so you get real HTTPS with no browser warning
and nothing to buy.

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

The installer handles Go, the source, the build, the certificate, the systemd
unit and the firewall. It fetches the source into `/opt/srvmon`, builds the hub
plus the Linux agents (amd64/arm64/arm), and prints a generated admin password
on first run — that line is the only place it appears.

Every question can be answered up front with a flag; supply them all (or pass
`-y`) and it never prompts.

| Flag | Default | Purpose |
| --- | --- | --- |
| `--port N` | 443 with SSL, else 8080 | port the dashboard listens on |
| `--ssl domain\|ip\|files\|none` | asked | certificate mode |
| `--domain NAME` | asked | domain for `--ssl domain` |
| `--server-ip ADDR` | auto-detected | public IPv4 for `--ssl ip` |
| `--cert` / `--key` | asked | files for `--ssl files` |
| `--acme-port N` | `80` | port acme.sh binds while validating |
| `--admin-user` | `admin` | first operator name |
| `--admin-password` | generated | first operator password |
| `--base-url URL` | derived | override the URL baked into agent install commands |
| `--data-dir DIR` | `/var/lib/srvmon` | database and agent builds |
| `--open-firewall yes\|no` | asked | punch the port through ufw/firewalld |
| `--source DIR` | GitHub | build from a local checkout |
| `-y`, `--yes` | — | accept every default, never prompt |
| `--uninstall` | — | remove the service and binary, keep the database |

Fully scripted, for example:

```bash
bash install-hub.sh --ssl domain --domain monitor.example.com --port 443 --admin-password 'choose-something' --open-firewall yes
```

### Which version gets installed

The installer takes the **newest release**, not the tip of `main` — `main` is
where unreleased work lands. To pin or roll back:

```bash
bash install-hub.sh --version v1.1.0
```

`--branch main` installs the latest commit instead, which is what you want only
if you are following development.

### Installing without internet access on the server

The server needs GitHub only to fetch the source. If it cannot reach GitHub,
download a release on a machine that can, copy it over, and point the installer
at it:

```bash
# on a machine with internet
curl -fsSLO https://github.com/lalash/srvmon/archive/refs/tags/v1.1.0.tar.gz
scp v1.1.0.tar.gz root@your-server:/root/

# on the server
tar -xzf /root/v1.1.0.tar.gz -C /root
sudo bash /root/srvmon-1.1.0/scripts/install-hub.sh --source /root/srvmon-1.1.0
```

`--source` skips the download entirely, so the only remaining network call is
the Go toolchain — already present if you installed before, and Let's Encrypt
if you choose a certificate. With `--ssl none` and Go already installed, the
whole install is offline.

Releases also carry prebuilt `srvmon-hub` and `srvmon-agent` binaries for
linux amd64/arm64/arm. Drop them in and skip the build:

```bash
sudo install -m 0755 srvmon-hub /usr/local/bin/srvmon-hub
sudo mkdir -p /var/lib/srvmon/bin
sudo install -m 0755 srvmon-agent-linux-* /var/lib/srvmon/bin/
```

> Use `bash <(curl …)`, not `curl … | bash`. With a pipe, stdin is the script
> itself, so nothing can be asked and every unset option silently takes its
> default. The installer warns when it detects this.

Configuration lives in `/etc/srvmon/hub.conf` and is read by the unit; edit it
and `systemctl restart srvmon-hub`. Re-running the installer updates the hub in
place. Logs: `journalctl -u srvmon-hub -f`.

On a host with under 1 GB of RAM the installer adds a 1 GB swapfile first —
the Go compiler gets OOM-killed otherwise, with no useful error.

### Certificates

Choosing **Let's Encrypt for a domain** installs `acme.sh`, checks that the
domain's A record actually points at this server, issues the certificate over
HTTP-01 on port 80 (standalone, so no web server is required) and registers a
renewal hook that copies the renewed pair into `/etc/srvmon/cert/` and restarts
the hub. Renewal is unattended from then on.

**Let's Encrypt for an IP address** works the same way but needs no domain at
all. These certificates use the short-lived profile and expire in about six
days, so the renewal hook is doing real work — it just runs on the same
unattended acme.sh cron.

Port 80 has to be reachable from the internet for both issuance and renewal. The
hub itself does not use it.

### Behind a reverse proxy

If you would rather terminate TLS in nginx, install with `--ssl none` and proxy
to it. The only non-obvious requirement is that the SSE stream must not be
buffered:

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

If the hub uses a self-signed certificate, add `--insecure`.

## Managing a machine from its own shell

Both installers drop a `srvmon` command on the machine. Run it bare for a menu,
or give it a subcommand:

```bash
srvmon
```

```
srvmon — hub agent management
  1. Status                      6. Update to the latest version
  2. Restart                     7. Change the dashboard password
  3. Stop                        8. Show the dashboard URL
  4. Start                       9. Uninstall
  5. Follow logs                 0. Exit
```

`srvmon status | start | stop | restart | logs | update | uninstall | password | url`

It works out what is installed on that machine: on an agent host the hub-only
entries are hidden, and `update` re-fetches from the hub it reports to. On the
hub, `update` rebuilds from GitHub with the settings already in
`/etc/srvmon/hub.conf`, so it never re-asks the install questions.

The uninstall command is also shown in the dashboard, next to each server's
install command.

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
