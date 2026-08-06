#!/usr/bin/env bash
# Installs the srvmon agent on this machine and points it at a central hub.
#
#   bash <(curl -fsSL https://hub.example.com/install-agent.sh) \
#        --hub https://hub.example.com --token <token> --name berlin-1
set -euo pipefail

HUB=""
TOKEN=""
NAME="$(hostname)"
INTERVAL="2"
INSECURE="0"
DISK=""

while [ $# -gt 0 ]; do
  case "$1" in
    --hub) HUB="$2"; shift 2 ;;
    --token) TOKEN="$2"; shift 2 ;;
    --name) NAME="$2"; shift 2 ;;
    --interval) INTERVAL="$2"; shift 2 ;;
    --disk) DISK="$2"; shift 2 ;;
    --insecure) INSECURE="1"; shift ;;
    --uninstall) UNINSTALL="1"; shift ;;
    *) echo "unknown option: $1" >&2; exit 1 ;;
  esac
done

if [ "$(id -u)" != "0" ]; then
  echo "error: run this as root (sudo)" >&2
  exit 1
fi

if [ "${UNINSTALL:-0}" = "1" ]; then
  systemctl disable --now srvmon-agent 2>/dev/null || true
  rm -f /etc/systemd/system/srvmon-agent.service /usr/local/bin/srvmon-agent /etc/srvmon/agent.conf
  # The management menu and /etc/srvmon belong to the hub too when both are on
  # one machine, so they only go when nothing else is left.
  if [ ! -x /usr/local/bin/srvmon-hub ]; then
    rm -f /usr/local/bin/srvmon
    rmdir /etc/srvmon 2>/dev/null || true
  fi
  systemctl daemon-reload 2>/dev/null || true
  echo "srvmon agent removed"
  exit 0
fi

if [ -z "$HUB" ] || [ -z "$TOKEN" ]; then
  echo "error: --hub and --token are required" >&2
  exit 1
fi

HUB="${HUB%/}"
CURL_OPTS="-fsSL"
if [ "$INSECURE" = "1" ]; then
  CURL_OPTS="$CURL_OPTS -k"
fi

case "$(uname -m)" in
  x86_64|amd64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  armv7l|armv6l|arm) ARCH="arm" ;;
  *) echo "error: unsupported architecture $(uname -m)" >&2; exit 1 ;;
esac

echo "==> downloading agent (linux-$ARCH) from $HUB"
TMP="$(mktemp)"
# shellcheck disable=SC2086
if ! curl $CURL_OPTS -o "$TMP" "$HUB/download/agent/linux-$ARCH"; then
  echo "error: the hub has no linux-$ARCH agent build available" >&2
  rm -f "$TMP"
  exit 1
fi

install -m 0755 "$TMP" /usr/local/bin/srvmon-agent
rm -f "$TMP"

# shellcheck disable=SC2086
if curl $CURL_OPTS -o "$TMP" "$HUB/srvmon.sh" 2>/dev/null; then
  install -m 0755 "$TMP" /usr/local/bin/srvmon
  rm -f "$TMP"
fi

mkdir -p /etc/srvmon
umask 077
cat >/etc/srvmon/agent.conf <<EOF
HUB=$HUB
TOKEN=$TOKEN
NAME=$NAME
INTERVAL=$INTERVAL
DISK=$DISK
INSECURE=$INSECURE
EOF
chmod 600 /etc/srvmon/agent.conf

if ! command -v systemctl >/dev/null 2>&1; then
  echo "warning: systemd not found — start the agent yourself:" >&2
  echo "  /usr/local/bin/srvmon-agent -config /etc/srvmon/agent.conf" >&2
  exit 0
fi

cat >/etc/systemd/system/srvmon-agent.service <<'EOF'
[Unit]
Description=srvmon agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/srvmon-agent -config /etc/srvmon/agent.conf
Restart=always
RestartSec=5
NoNewPrivileges=true
ProtectHome=true
ProtectSystem=full

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now srvmon-agent
sleep 2

echo
if systemctl is-active --quiet srvmon-agent; then
  echo "==> srvmon agent is running and reporting to $HUB as \"$NAME\""
  echo "    manage: srvmon          (menu: status, logs, update, uninstall)"
  echo "    logs:   journalctl -u srvmon-agent -f"
  echo "    remove: bash <(curl -fsSL $HUB/install-agent.sh) --uninstall"
else
  echo "==> the agent failed to start; last log lines:" >&2
  journalctl -u srvmon-agent -n 20 --no-pager >&2 || true
  exit 1
fi
