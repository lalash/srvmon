#!/usr/bin/env bash
# Installs the central hub on this machine. Works two ways:
#
#   straight from GitHub, nothing checked out:
#     bash <(curl -fsSL https://raw.githubusercontent.com/lalash/srvmon/main/scripts/install-hub.sh) \
#          --base-url https://monitor.example.com
#
#   from a checkout:
#     sudo bash scripts/install-hub.sh --base-url https://monitor.example.com
#
# Re-run it any time to update: it refetches the source and rebuilds in place.
set -euo pipefail

GO_VERSION="1.24.5"
REPO="lalash/srvmon"
BRANCH="main"
ADDR=":8080"
BASE_URL=""
DATA_DIR="/var/lib/srvmon"
SRC_DIR="/opt/srvmon"
CERT=""
KEY=""
ADMIN_USER="admin"
ADMIN_PASSWORD=""
SOURCE_DIR=""

while [ $# -gt 0 ]; do
  case "$1" in
    --addr) ADDR="$2"; shift 2 ;;
    --base-url) BASE_URL="$2"; shift 2 ;;
    --data-dir) DATA_DIR="$2"; shift 2 ;;
    --cert) CERT="$2"; shift 2 ;;
    --key) KEY="$2"; shift 2 ;;
    --admin-user) ADMIN_USER="$2"; shift 2 ;;
    --admin-password) ADMIN_PASSWORD="$2"; shift 2 ;;
    --source) SOURCE_DIR="$2"; shift 2 ;;
    --repo) REPO="$2"; shift 2 ;;
    --branch) BRANCH="$2"; shift 2 ;;
    --uninstall) UNINSTALL="1"; shift ;;
    *) echo "unknown option: $1" >&2; exit 1 ;;
  esac
done

if [ "$(id -u)" != "0" ]; then
  echo "error: run this as root (sudo)" >&2
  exit 1
fi

if [ "${UNINSTALL:-0}" = "1" ]; then
  systemctl disable --now srvmon-hub 2>/dev/null || true
  rm -f /etc/systemd/system/srvmon-hub.service /usr/local/bin/srvmon-hub
  systemctl daemon-reload 2>/dev/null || true
  echo "hub removed. Its database is still at $DATA_DIR — delete it yourself if you meant to."
  exit 0
fi

echo "==> checking prerequisites"
if command -v apt-get >/dev/null 2>&1; then
  apt-get update -qq
  apt-get install -y -qq curl ca-certificates tar >/dev/null
elif command -v dnf >/dev/null 2>&1; then
  dnf install -y -q curl ca-certificates tar >/dev/null
fi

# Prefer an explicit --source, then the checkout this script lives in, and fall
# back to the release tarball — that last path is what the curl one-liner hits,
# where the script has no source tree around it at all.
if [ -z "$SOURCE_DIR" ]; then
  here="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." 2>/dev/null && pwd || true)"
  if [ -n "$here" ] && [ -f "$here/go.mod" ]; then
    SOURCE_DIR="$here"
  else
    echo "==> fetching $REPO ($BRANCH) into $SRC_DIR"
    rm -rf "$SRC_DIR"
    mkdir -p "$SRC_DIR"
    curl -fsSL "https://codeload.github.com/$REPO/tar.gz/refs/heads/$BRANCH" \
      | tar -xz -C "$SRC_DIR" --strip-components=1
    SOURCE_DIR="$SRC_DIR"
  fi
fi

if [ ! -f "$SOURCE_DIR/go.mod" ]; then
  echo "error: $SOURCE_DIR does not look like the srvmon source tree" >&2
  exit 1
fi

case "$(uname -m)" in
  x86_64|amd64) HOST_ARCH="amd64" ;;
  aarch64|arm64) HOST_ARCH="arm64" ;;
  *) echo "error: unsupported architecture $(uname -m)" >&2; exit 1 ;;
esac

if ! command -v go >/dev/null 2>&1 && [ ! -x /usr/local/go/bin/go ]; then
  echo "==> installing Go $GO_VERSION"
  curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${HOST_ARCH}.tar.gz" -o /tmp/go.tar.gz
  rm -rf /usr/local/go
  tar -C /usr/local -xzf /tmp/go.tar.gz
  rm -f /tmp/go.tar.gz
fi
export PATH="/usr/local/go/bin:$PATH"
go version

echo "==> resolving dependencies"
cd "$SOURCE_DIR"
go mod tidy

echo "==> building the hub"
go build -trimpath -ldflags "-s -w" -o /usr/local/bin/srvmon-hub ./cmd/hub

echo "==> building agents for linux amd64, arm64 and arm"
mkdir -p "$DATA_DIR/bin"
for target in amd64 arm64 arm; do
  GOOS=linux GOARCH="$target" CGO_ENABLED=0 \
    go build -trimpath -ldflags "-s -w" -o "$DATA_DIR/bin/srvmon-agent-linux-$target" ./cmd/agent
done
chmod 0755 "$DATA_DIR"/bin/srvmon-agent-*

mkdir -p /etc/srvmon
if [ ! -f /etc/srvmon/hub.conf ]; then
  umask 077
  cat >/etc/srvmon/hub.conf <<EOF
SRVMON_ADDR=$ADDR
SRVMON_DB=$DATA_DIR/srvmon.db
SRVMON_BIN_DIR=$DATA_DIR/bin
SRVMON_BASE_URL=$BASE_URL
SRVMON_CERT=$CERT
SRVMON_KEY=$KEY
SRVMON_ADMIN_USER=$ADMIN_USER
SRVMON_ADMIN_PASSWORD=$ADMIN_PASSWORD
EOF
  chmod 600 /etc/srvmon/hub.conf
else
  echo "==> keeping the existing /etc/srvmon/hub.conf"
fi

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

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now srvmon-hub
sleep 2

echo
if systemctl is-active --quiet srvmon-hub; then
  echo "==> hub is running on $ADDR"
  if [ -z "$ADMIN_PASSWORD" ]; then
    echo "==> first-run login (also in: journalctl -u srvmon-hub):"
    journalctl -u srvmon-hub --no-pager | grep -A2 "dashboard login created" | tail -n 3 || true
  else
    echo "==> sign in as $ADMIN_USER with the password you passed"
  fi
  echo "    open ${BASE_URL:-http://<this-server>${ADDR}} and add your first server"
else
  echo "==> the hub failed to start; last log lines:" >&2
  journalctl -u srvmon-hub -n 30 --no-pager >&2 || true
  exit 1
fi
