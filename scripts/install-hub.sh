#!/usr/bin/env bash
# Installs the srvmon hub on this machine. Works two ways:
#
#   straight from GitHub, nothing checked out (asks for port / SSL / domain):
#     bash <(curl -fsSL https://raw.githubusercontent.com/lalash/srvmon/main/scripts/install-hub.sh)
#
#   fully scripted, no questions:
#     bash install-hub.sh --port 443 --ssl domain --domain monitor.example.com --admin-password '...'
#
# Re-run it any time to update: it refetches the source and rebuilds in place.
set -euo pipefail

GO_VERSION="1.24.5"
REPO="lalash/srvmon"
BRANCH="main"

PORT=""
SSL_MODE=""
DOMAIN=""
SERVER_IP=""
CERT=""
KEY=""
ACME_HTTP_PORT="80"
DATA_DIR="/var/lib/srvmon"
SRC_DIR="/opt/srvmon"
CERT_DIR="/etc/srvmon/cert"
ADMIN_USER=""
ADMIN_PASSWORD=""
BASE_URL=""
SOURCE_DIR=""
OPEN_FIREWALL=""
ASSUME_YES="0"
FORCE_CERT="0"

red=$'\033[0;31m'; green=$'\033[0;32m'; yellow=$'\033[0;33m'; blue=$'\033[0;34m'; plain=$'\033[0m'
info() { echo -e "${green}==>${plain} $*"; }
warn() { echo -e "${yellow}warning:${plain} $*" >&2; }
fail() { echo -e "${red}error:${plain} $*" >&2; exit 1; }

usage() {
  cat <<'EOF'
Options (anything you leave out is asked interactively):
  --port N               port the dashboard listens on
  --ssl domain|ip|files|none  certificate mode
  --domain NAME          domain for --ssl domain
  --server-ip ADDR       public IPv4 for --ssl ip (default: auto-detected)
  --cert PATH --key PATH files for --ssl files
  --acme-port N          port acme.sh binds while validating (default 80)
  --admin-user NAME      first operator (default admin)
  --admin-password PASS  first operator password (default: generated)
  --base-url URL         override the URL baked into agent install commands
  --data-dir DIR         database and agent builds (default /var/lib/srvmon)
  --source DIR           build from a local checkout instead of GitHub
  --repo USER/NAME --branch NAME
  --open-firewall yes|no punch the port through ufw/firewalld
  --force-cert           reissue the certificate even if the current one is fine
  -y, --yes              accept every default, never prompt
  --uninstall            remove the service and binary, keep the database
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --port) PORT="$2"; shift 2 ;;
    --ssl) SSL_MODE="$2"; shift 2 ;;
    --domain) DOMAIN="$2"; shift 2 ;;
    --server-ip) SERVER_IP="$2"; shift 2 ;;
    --cert) CERT="$2"; shift 2 ;;
    --key) KEY="$2"; shift 2 ;;
    --acme-port) ACME_HTTP_PORT="$2"; shift 2 ;;
    --admin-user) ADMIN_USER="$2"; shift 2 ;;
    --admin-password) ADMIN_PASSWORD="$2"; shift 2 ;;
    --base-url) BASE_URL="$2"; shift 2 ;;
    --data-dir) DATA_DIR="$2"; shift 2 ;;
    --source) SOURCE_DIR="$2"; shift 2 ;;
    --repo) REPO="$2"; shift 2 ;;
    --branch) BRANCH="$2"; shift 2 ;;
    --open-firewall) OPEN_FIREWALL="$2"; shift 2 ;;
    --force-cert) FORCE_CERT="1"; shift ;;
    -y|--yes) ASSUME_YES="1"; shift ;;
    --uninstall) UNINSTALL="1"; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown option: $1" >&2; usage >&2; exit 1 ;;
  esac
done

[ "$(id -u)" = "0" ] || fail "run this as root (sudo)"

if [ "${UNINSTALL:-0}" = "1" ]; then
  systemctl disable --now srvmon-hub 2>/dev/null || true
  rm -f /etc/systemd/system/srvmon-hub.service /usr/local/bin/srvmon-hub
  systemctl daemon-reload 2>/dev/null || true
  echo "hub removed. Its database is still at $DATA_DIR — delete it yourself if you meant to."
  exit 0
fi

# Prompting needs a terminal. The curl one-liner uses process substitution
# rather than a pipe precisely so stdin stays attached to the console.
INTERACTIVE="1"
if [ "$ASSUME_YES" = "1" ]; then
  INTERACTIVE="0"
elif [ ! -t 0 ]; then
  INTERACTIVE="0"
  warn "stdin is not a terminal, so nothing can be asked — every unset option takes its default."
  warn "For the questions, run it as: bash <(curl -fsSL .../install-hub.sh)   not   curl ... | bash"
fi

ask() {
  local __var="$1" prompt="$2" default="${3:-}" answer=""
  if [ -n "${!__var}" ]; then return; fi
  if [ "$INTERACTIVE" = "0" ]; then
    printf -v "$__var" '%s' "$default"
    return
  fi
  read -rp "$(echo -e "${blue}?${plain} ${prompt}")" answer
  answer="${answer//[[:space:]]/}"
  printf -v "$__var" '%s' "${answer:-$default}"
}

confirm() {
  local prompt="$1" default="${2:-y}" answer=""
  if [ "$INTERACTIVE" = "0" ]; then [ "$default" = "y" ]; return; fi
  read -rp "$(echo -e "${blue}?${plain} ${prompt}")" answer
  answer="${answer:-$default}"
  [[ "$answer" =~ ^[Yy] ]]
}

is_domain() {
  [[ "$1" =~ ^([A-Za-z0-9](-*[A-Za-z0-9])*\.)+(xn--[a-z0-9]{2,}|[A-Za-z]{2,})$ ]]
}

is_ipv4() {
  [[ "$1" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]] || return 1
  local part
  for part in ${1//./ }; do [ "$part" -le 255 ] || return 1; done
}

is_port() {
  [[ "$1" =~ ^[1-9][0-9]*$ ]] && [ "$1" -le 65535 ]
}

# Re-running the installer is the documented way to update, so it must not burn
# a Let's Encrypt issuance every time. Anything with more than two days left is
# left alone — well inside the ~6-day life of an IP certificate.
cert_still_good() {
  [ -s "$1" ] || return 1
  openssl x509 -checkend 172800 -noout -in "$1" >/dev/null 2>&1
}

detect_pkg() {
  if command -v apt-get >/dev/null 2>&1; then echo apt
  elif command -v dnf >/dev/null 2>&1; then echo dnf
  elif command -v yum >/dev/null 2>&1; then echo yum
  elif command -v pacman >/dev/null 2>&1; then echo pacman
  elif command -v zypper >/dev/null 2>&1; then echo zypper
  elif command -v apk >/dev/null 2>&1; then echo apk
  else echo none
  fi
}

install_packages() {
  local pkg; pkg="$(detect_pkg)"
  case "$pkg" in
    apt) apt-get update -qq >/dev/null && apt-get install -y -qq "$@" >/dev/null ;;
    dnf) dnf install -y -q "$@" >/dev/null ;;
    yum) yum install -y -q "$@" >/dev/null ;;
    pacman) pacman -Sy --noconfirm "$@" >/dev/null ;;
    zypper) zypper -q install -y "$@" >/dev/null ;;
    apk) apk add --quiet "$@" >/dev/null ;;
    *) warn "unknown package manager; make sure these are installed: $*" ;;
  esac
}

public_ip() {
  curl -fsS --max-time 5 https://api.ipify.org 2>/dev/null ||
    curl -fsS --max-time 5 https://ipv4.icanhazip.com 2>/dev/null || true
}

# ---------------------------------------------------------------- questions

echo
echo -e "${green}srvmon hub installer${plain}"
echo -e "${blue}Anything you leave blank takes the default in brackets.${plain}"
echo

if [ -z "$SSL_MODE" ] && [ "$INTERACTIVE" = "1" ]; then
  echo "How should the dashboard be served?"
  echo -e "  ${green}1.${plain} HTTPS with a free Let's Encrypt certificate for a domain (auto-renews)"
  echo -e "  ${green}2.${plain} HTTPS for this server's IP address — no domain needed (~6-day cert, auto-renews)"
  echo -e "  ${green}3.${plain} HTTPS with certificate files you already have"
  echo -e "  ${green}4.${plain} Plain HTTP — only safe behind a reverse proxy or on a private network"
  echo -e "${blue}Note:${plain} options 1 and 2 need port ${ACME_HTTP_PORT} reachable from the internet."
  echo -e "${blue}Note:${plain} option 1 also needs the domain's A record already pointing here."
  choice=""
  ask choice "Choose [1]: " "1"
  case "$choice" in
    2) SSL_MODE="ip" ;;
    3) SSL_MODE="files" ;;
    4) SSL_MODE="none" ;;
    *) SSL_MODE="domain" ;;
  esac
fi
SSL_MODE="${SSL_MODE:-none}"

case "$SSL_MODE" in
  domain)
    while ! is_domain "${DOMAIN:-}"; do
      DOMAIN=""
      ask DOMAIN "Domain name (e.g. monitor.example.com): " ""
      [ -n "$DOMAIN" ] || { [ "$INTERACTIVE" = "1" ] || fail "--domain is required with --ssl domain"; }
      is_domain "${DOMAIN:-}" || warn "\"${DOMAIN}\" is not a valid domain name"
    done
    ;;
  ip)
    # Echo services can return a transit address on multi-WAN hosts, so the
    # detected value is confirmed before an issuance attempt is spent on it.
    detected="$(public_ip)"
    while ! is_ipv4 "${SERVER_IP:-}"; do
      SERVER_IP=""
      ask SERVER_IP "This server's public IPv4 [${detected:-none detected}]: " "$detected"
      is_ipv4 "${SERVER_IP:-}" || warn "\"${SERVER_IP}\" is not a valid IPv4 address"
      [ -n "${SERVER_IP:-}" ] || [ "$INTERACTIVE" = "1" ] || fail "could not detect a public IPv4; pass --server-ip"
    done
    ;;
  files)
    while [ -z "$CERT" ]; do ask CERT "Path to the certificate (fullchain.pem): " ""; done
    while [ -z "$KEY" ]; do ask KEY "Path to the private key: " ""; done
    [ -s "$CERT" ] || fail "certificate not found: $CERT"
    [ -s "$KEY" ] || fail "private key not found: $KEY"
    ;;
  none) ;;
  *) fail "--ssl must be one of: domain, ip, files, none" ;;
esac

if [ -z "$PORT" ]; then
  default_port="8080"
  [ "$SSL_MODE" != "none" ] && default_port="443"
  ask PORT "Port for the dashboard [${default_port}]: " "$default_port"
fi
is_port "$PORT" || fail "invalid port: $PORT"

if [ -z "$ADMIN_PASSWORD" ] && [ "$INTERACTIVE" = "1" ]; then
  if confirm "Set the admin password yourself? (otherwise one is generated) [y/N]: " "n"; then
    ask ADMIN_USER "Admin username [admin]: " "admin"
    while [ ${#ADMIN_PASSWORD} -lt 8 ]; do
      read -rsp "$(echo -e "${blue}?${plain} Admin password (min 8 chars): ")" ADMIN_PASSWORD; echo
      [ ${#ADMIN_PASSWORD} -lt 8 ] && warn "too short"
    done
  fi
fi
ADMIN_USER="${ADMIN_USER:-admin}"

if [ -z "$OPEN_FIREWALL" ]; then
  if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -q "Status: active"; then
    OPEN_FIREWALL=$(confirm "ufw is active — open port ${PORT}? [Y/n]: " "y" && echo yes || echo no)
  elif command -v firewall-cmd >/dev/null 2>&1 && firewall-cmd --state >/dev/null 2>&1; then
    OPEN_FIREWALL=$(confirm "firewalld is active — open port ${PORT}? [Y/n]: " "y" && echo yes || echo no)
  else
    OPEN_FIREWALL="no"
  fi
fi

# ------------------------------------------------------------------- build

info "installing prerequisites"
install_packages curl ca-certificates tar openssl

case "$(uname -m)" in
  x86_64|amd64) HOST_ARCH="amd64" ;;
  aarch64|arm64) HOST_ARCH="arm64" ;;
  *) fail "unsupported architecture $(uname -m)" ;;
esac

if [ -z "$SOURCE_DIR" ]; then
  here="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." 2>/dev/null && pwd || true)"
  if [ -n "$here" ] && [ -f "$here/go.mod" ]; then
    SOURCE_DIR="$here"
  else
    info "fetching $REPO ($BRANCH) into $SRC_DIR"
    rm -rf "$SRC_DIR"; mkdir -p "$SRC_DIR"
    curl -fsSL "https://codeload.github.com/$REPO/tar.gz/refs/heads/$BRANCH" \
      | tar -xz -C "$SRC_DIR" --strip-components=1
    SOURCE_DIR="$SRC_DIR"
  fi
fi
[ -f "$SOURCE_DIR/go.mod" ] || fail "$SOURCE_DIR does not look like the srvmon source tree"

if ! command -v go >/dev/null 2>&1 && [ ! -x /usr/local/go/bin/go ]; then
  info "installing Go $GO_VERSION"
  curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${HOST_ARCH}.tar.gz" -o /tmp/go.tar.gz
  rm -rf /usr/local/go
  tar -C /usr/local -xzf /tmp/go.tar.gz
  rm -f /tmp/go.tar.gz
fi
export PATH="/usr/local/go/bin:$PATH"

# A first build on a 512MB box gets OOM-killed; a temporary swapfile is far
# less surprising than a compiler that dies with no explanation.
total_mem_kb=$(awk '/MemTotal/ {print $2}' /proc/meminfo 2>/dev/null || true)
[[ "$total_mem_kb" =~ ^[0-9]+$ ]] || total_mem_kb=0
if [ "$total_mem_kb" -gt 0 ] && [ "$total_mem_kb" -lt 1000000 ] &&
   [ ! -f /swapfile ] && [ "$(swapon --show --noheadings 2>/dev/null | wc -l)" = "0" ]; then
  info "only $((total_mem_kb / 1024))MB of RAM — adding a 1GB swapfile so the build survives"
  fallocate -l 1G /swapfile 2>/dev/null || dd if=/dev/zero of=/swapfile bs=1M count=1024 status=none
  chmod 600 /swapfile && mkswap -q /swapfile && swapon /swapfile
  grep -q '^/swapfile' /etc/fstab 2>/dev/null || echo '/swapfile none swap sw 0 0' >>/etc/fstab
fi

info "resolving dependencies (this can take a minute)"
cd "$SOURCE_DIR"
go mod tidy

info "building the hub"
go build -trimpath -ldflags "-s -w" -o /usr/local/bin/srvmon-hub ./cmd/hub

info "building agents for linux amd64, arm64 and arm"
mkdir -p "$DATA_DIR/bin"
# The database holds agent tokens and password hashes, so the directory is not
# world-readable; UMask in the unit keeps the database file itself at 0600.
chmod 0750 "$DATA_DIR"
chmod 0600 "$DATA_DIR"/srvmon.db* 2>/dev/null || true
for target in amd64 arm64 arm; do
  GOOS=linux GOARCH="$target" CGO_ENABLED=0 \
    go build -trimpath -ldflags "-s -w" -o "$DATA_DIR/bin/srvmon-agent-linux-$target" ./cmd/agent
done
chmod 0755 "$DATA_DIR"/bin/srvmon-agent-*

# ------------------------------------------------------------- certificate

if [ "$SSL_MODE" = "domain" ] || [ "$SSL_MODE" = "ip" ]; then
  CERT_NAME="$DOMAIN"
  [ "$SSL_MODE" = "ip" ] && CERT_NAME="$SERVER_IP"
  CERT="$CERT_DIR/$CERT_NAME.crt"
  KEY="$CERT_DIR/$CERT_NAME.key"

  if [ "$FORCE_CERT" = "0" ] && cert_still_good "$CERT"; then
    expiry="$(openssl x509 -enddate -noout -in "$CERT" 2>/dev/null | cut -d= -f2)"
    info "reusing the existing certificate for $CERT_NAME (valid until $expiry)"
    SKIP_ISSUE="1"
  fi
fi

if [ "${SKIP_ISSUE:-0}" = "0" ] && { [ "$SSL_MODE" = "domain" ] || [ "$SSL_MODE" = "ip" ]; }; then
  if [ "$SSL_MODE" = "domain" ]; then
    info "checking DNS for $DOMAIN"
    server_ip="$(public_ip)"
    resolved="$(getent ahostsv4 "$DOMAIN" 2>/dev/null | awk 'NR==1 {print $1}')"
    if [ -n "$server_ip" ] && [ -n "$resolved" ] && [ "$server_ip" != "$resolved" ]; then
      warn "$DOMAIN resolves to $resolved but this server's public IP is $server_ip."
      warn "Let's Encrypt validation will fail unless the A record points here."
      confirm "Continue anyway? [y/N]: " "n" || fail "aborted — fix the DNS record and re-run"
    elif [ -z "$resolved" ]; then
      warn "$DOMAIN does not resolve yet; validation will fail if DNS has not propagated."
      confirm "Continue anyway? [y/N]: " "n" || fail "aborted — fix the DNS record and re-run"
    fi
  fi

  install_packages socat
  if [ ! -x "$HOME/.acme.sh/acme.sh" ]; then
    info "installing acme.sh"
    curl -fsSL https://get.acme.sh | sh >/dev/null
  fi
  acme="$HOME/.acme.sh/acme.sh"
  [ -x "$acme" ] || fail "acme.sh installation failed"

  # acme.sh's standalone server binds IPv4 unless told otherwise; forcing v6 on
  # a host with a global v4 address breaks HTTP-01 validation.
  listen_flag=""
  ip -4 addr show scope global 2>/dev/null | grep -q "inet " || listen_flag="--listen-v6"

  if ss -ltn 2>/dev/null | grep -q ":${ACME_HTTP_PORT} "; then
    warn "port ${ACME_HTTP_PORT} is already in use; acme.sh needs it free to validate."
    confirm "Continue anyway? [y/N]: " "n" || fail "aborted — free port ${ACME_HTTP_PORT} and re-run"
  fi

  "$acme" --set-default-ca --server letsencrypt >/dev/null 2>&1 || true

  if [ "$SSL_MODE" = "ip" ]; then
    info "requesting an IP certificate for $SERVER_IP"
    # Let's Encrypt only issues for a bare IP under the shortlived profile, so
    # these certs last ~6 days; acme.sh's daily cron is what keeps them valid.
    "$acme" --upgrade --auto-upgrade >/dev/null 2>&1 || true
    # shellcheck disable=SC2086
    "$acme" --issue -d "$SERVER_IP" $listen_flag --standalone --server letsencrypt \
      --certificate-profile shortlived --days 6 --httpport "$ACME_HTTP_PORT" --force \
      || fail "IP certificate issuance failed — check that port ${ACME_HTTP_PORT} is reachable from the internet"
  else
    info "requesting a certificate for $DOMAIN"
    # shellcheck disable=SC2086
    "$acme" --issue -d "$DOMAIN" $listen_flag --standalone --httpport "$ACME_HTTP_PORT" --force \
      || fail "certificate issuance failed — check that port ${ACME_HTTP_PORT} is reachable from the internet"
  fi

  mkdir -p "$CERT_DIR"
  # --installcert also registers the renewal hook, so the acme.sh cron copies
  # the renewed pair here and restarts the hub without anyone watching. Its
  # exit code is unusable: the reloadcmd runs immediately and the unit does not
  # exist yet, so the certificate files are what gets checked.
  "$acme" --installcert --force -d "$CERT_NAME" \
    --fullchain-file "$CERT" \
    --key-file "$KEY" \
    --reloadcmd "systemctl restart srvmon-hub" >/dev/null 2>&1 || true
  [ -s "$CERT" ] && [ -s "$KEY" ] || fail "acme.sh issued a certificate but did not write $CERT / $KEY"
  chmod 600 "$KEY"
  info "certificate installed at $CERT, auto-renewal registered"
fi

SCHEME="http"
[ -n "$CERT" ] && SCHEME="https"

HOST="$DOMAIN"
[ -z "$HOST" ] && HOST="${SERVER_IP:-}"
[ -z "$HOST" ] && HOST="$(public_ip)"
[ -z "$HOST" ] && HOST="localhost"

if [ -z "$BASE_URL" ]; then
  if { [ "$SCHEME" = "https" ] && [ "$PORT" = "443" ]; } || { [ "$SCHEME" = "http" ] && [ "$PORT" = "80" ]; }; then
    BASE_URL="${SCHEME}://${HOST}"
  else
    BASE_URL="${SCHEME}://${HOST}:${PORT}"
  fi
fi

# ----------------------------------------------------------------- service

mkdir -p /etc/srvmon
umask 077
cat >/etc/srvmon/hub.conf <<EOF
SRVMON_ADDR=:$PORT
SRVMON_DB=$DATA_DIR/srvmon.db
SRVMON_BIN_DIR=$DATA_DIR/bin
SRVMON_BASE_URL=$BASE_URL
SRVMON_CERT=$CERT
SRVMON_KEY=$KEY
SRVMON_ADMIN_USER=$ADMIN_USER
SRVMON_ADMIN_PASSWORD=$ADMIN_PASSWORD
EOF
chmod 600 /etc/srvmon/hub.conf
umask 022

cat >/etc/systemd/system/srvmon-hub.service <<EOF
[Unit]
Description=srvmon hub
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=/etc/srvmon/hub.conf
ExecStart=/usr/local/bin/srvmon-hub
Restart=always
RestartSec=5
NoNewPrivileges=true
ProtectHome=true
ProtectSystem=full
ReadWritePaths=$DATA_DIR
UMask=0077

[Install]
WantedBy=multi-user.target
EOF

if [ "$OPEN_FIREWALL" = "yes" ]; then
  if command -v ufw >/dev/null 2>&1; then
    ufw allow "$PORT/tcp" >/dev/null 2>&1 || true
    [ "$SSL_MODE" = "domain" ] && ufw allow "$ACME_HTTP_PORT/tcp" >/dev/null 2>&1 || true
  elif command -v firewall-cmd >/dev/null 2>&1; then
    firewall-cmd --permanent --add-port="$PORT/tcp" >/dev/null 2>&1 || true
    [ "$SSL_MODE" = "domain" ] && firewall-cmd --permanent --add-port="$ACME_HTTP_PORT/tcp" >/dev/null 2>&1 || true
    firewall-cmd --reload >/dev/null 2>&1 || true
  fi
  info "opened port $PORT"
fi

systemctl daemon-reload
systemctl enable srvmon-hub >/dev/null 2>&1
# restart, not `enable --now`: on an update run the unit is already active, and
# --now is a no-op there, so the freshly built binary would never be picked up.
systemctl restart srvmon-hub
sleep 2

echo
if systemctl is-active --quiet srvmon-hub; then
  echo -e "${green}────────────────────────────────────────────────────────${plain}"
  echo -e "${green} srvmon hub is running${plain}"
  echo -e "   dashboard: ${blue}${BASE_URL}${plain}"
  if [ -n "$ADMIN_PASSWORD" ]; then
    echo -e "   sign in as ${ADMIN_USER} with the password you chose"
  else
    echo -e "   first-run login:"
    journalctl -u srvmon-hub --no-pager 2>/dev/null | grep -E "username:|password:" | tail -n 2 || true
  fi
  echo -e "${green}────────────────────────────────────────────────────────${plain}"
  echo -e " logs:      ${blue}journalctl -u srvmon-hub -f${plain}"
  echo -e " update:    re-run this installer"
  echo -e " uninstall: ${blue}bash <(curl -fsSL https://raw.githubusercontent.com/$REPO/$BRANCH/scripts/install-hub.sh) --uninstall${plain}"
  echo
  echo -e " Next: open the dashboard, go to Servers → Add server, and paste the"
  echo -e " one-liner it gives you on each machine you want to monitor."
else
  echo -e "${red}the hub failed to start; last log lines:${plain}" >&2
  journalctl -u srvmon-hub -n 30 --no-pager >&2 || true
  exit 1
fi
