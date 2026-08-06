#!/usr/bin/env bash
# Installed as /usr/local/bin/srvmon on every machine that runs a hub or an
# agent. Run it bare for a menu, or with a subcommand:
#
#   srvmon status | start | stop | restart | logs | update | uninstall
#   srvmon password        (hub only — set a new dashboard password)
#   srvmon url             (hub only — print the dashboard URL)
set -uo pipefail

REPO="lalash/srvmon"
BRANCH="main"
HUB_CONF="/etc/srvmon/hub.conf"
AGENT_CONF="/etc/srvmon/agent.conf"

red=$'\033[0;31m'; green=$'\033[0;32m'; yellow=$'\033[0;33m'; blue=$'\033[0;34m'; plain=$'\033[0m'
info() { echo -e "${green}==>${plain} $*"; }
warn() { echo -e "${yellow}warning:${plain} $*" >&2; }
fail() { echo -e "${red}error:${plain} $*" >&2; exit 1; }

has_hub()   { [ -x /usr/local/bin/srvmon-hub ]; }
has_agent() { [ -x /usr/local/bin/srvmon-agent ]; }

need_root() { [ "$(id -u)" = "0" ] || fail "run this as root (sudo srvmon)"; }

# The role is whichever component is installed; a machine can be both, in which
# case every action applies to both unless one is named explicitly.
units() {
  local list=()
  has_hub && list+=("srvmon-hub")
  has_agent && list+=("srvmon-agent")
  echo "${list[@]:-}"
}

conf_value() {
  local file="$1" key="$2"
  [ -f "$file" ] || return 1
  sed -n "s/^${key}=//p" "$file" | head -n 1 | tr -d '"'"'"
}

cmd_status() {
  local unit
  for unit in $(units); do
    echo
    echo -e "${blue}── ${unit} ${plain}"
    systemctl status "$unit" --no-pager --lines=0 2>/dev/null | head -n 5
  done
  if has_hub; then
    echo
    echo -e "${blue}── dashboard ${plain}"
    echo "   $(cmd_url)"
    local cert; cert="$(conf_value "$HUB_CONF" SRVMON_CERT || true)"
    if [ -n "${cert:-}" ] && [ -s "$cert" ]; then
      echo "   certificate valid until $(openssl x509 -enddate -noout -in "$cert" 2>/dev/null | cut -d= -f2)"
    fi
  fi
  if has_agent; then
    echo
    echo -e "${blue}── agent ${plain}"
    echo "   reporting to $(conf_value "$AGENT_CONF" HUB || echo '?') as \"$(conf_value "$AGENT_CONF" NAME || echo '?')\""
  fi
  echo
}

cmd_start()   { need_root; local u; for u in $(units); do systemctl start "$u" && info "$u started"; done; }
cmd_stop()    { need_root; local u; for u in $(units); do systemctl stop "$u" && info "$u stopped"; done; }
cmd_restart() { need_root; local u; for u in $(units); do systemctl restart "$u" && info "$u restarted"; done; }

cmd_logs() {
  local unit
  unit="$(units)"; unit="${unit%% *}"
  [ -n "$unit" ] || fail "nothing installed"
  echo -e "${blue}following ${unit} — Ctrl+C to stop${plain}"
  journalctl -u "$unit" -f --no-pager
}

cmd_url() {
  local base; base="$(conf_value "$HUB_CONF" SRVMON_BASE_URL || true)"
  echo "${base:-unknown}"
}

cmd_update() {
  need_root
  if has_hub; then
    info "updating the hub from $REPO"
    curl -fsSL "https://raw.githubusercontent.com/$REPO/$BRANCH/scripts/install-hub.sh" -o /tmp/install-hub.sh \
      || fail "could not download the installer"
    # Re-running the installer rebuilds from source and restarts; -y keeps the
    # answers already stored in hub.conf instead of asking again. The address
    # may be ":443" or "0.0.0.0:443", so the port is whatever follows the last
    # colon — deleting every colon turns the second form into 0.0.0.0443.
    local port db
    port="$(conf_value "$HUB_CONF" SRVMON_ADDR | sed 's/.*://')"
    db="$(conf_value "$HUB_CONF" SRVMON_DB || echo /var/lib/srvmon/srvmon.db)"
    # shellcheck disable=SC2046
    bash /tmp/install-hub.sh -y \
      --port "${port:-8080}" \
      --data-dir "$(dirname "$db")" \
      --ssl "$(hub_ssl_mode)" \
      $(hub_ssl_args)
  fi
  if has_agent; then
    local hub token name
    hub="$(conf_value "$AGENT_CONF" HUB)"; token="$(conf_value "$AGENT_CONF" TOKEN)"
    name="$(conf_value "$AGENT_CONF" NAME)"
    [ -n "$hub" ] && [ -n "$token" ] || fail "$AGENT_CONF is missing HUB or TOKEN"
    info "updating the agent from $hub"
    curl -fsSL "$hub/install-agent.sh" -o /tmp/install-agent.sh || fail "could not reach $hub"
    bash /tmp/install-agent.sh --hub "$hub" --token "$token" --name "$name"
  fi
}

hub_ssl_mode() {
  local cert; cert="$(conf_value "$HUB_CONF" SRVMON_CERT || true)"
  if [ -z "${cert:-}" ]; then echo "none"; return; fi
  local name; name="$(basename "$cert" .crt)"
  if [[ "$name" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]]; then echo "ip"; else echo "domain"; fi
}

hub_ssl_args() {
  local cert; cert="$(conf_value "$HUB_CONF" SRVMON_CERT || true)"
  [ -n "${cert:-}" ] || return 0
  local name; name="$(basename "$cert" .crt)"
  if [[ "$name" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]]; then
    echo "--server-ip $name"
  else
    echo "--domain $name"
  fi
}

cmd_uninstall() {
  need_root
  echo -e "${yellow}This removes:${plain}"
  has_hub && echo "  - the hub service and binary (the database at /var/lib/srvmon is kept)"
  has_agent && echo "  - the agent service, binary and /etc/srvmon/agent.conf"
  local answer=""
  read -rp "$(echo -e "${blue}?${plain} Continue? [y/N]: ")" answer
  [[ "$answer" =~ ^[Yy] ]] || { echo "cancelled"; return; }

  if has_agent; then
    systemctl disable --now srvmon-agent 2>/dev/null
    rm -f /etc/systemd/system/srvmon-agent.service /usr/local/bin/srvmon-agent /etc/srvmon/agent.conf
    info "agent removed"
  fi
  if has_hub; then
    systemctl disable --now srvmon-hub 2>/dev/null
    rm -f /etc/systemd/system/srvmon-hub.service /usr/local/bin/srvmon-hub
    info "hub removed — its database is still at /var/lib/srvmon"
  fi
  systemctl daemon-reload 2>/dev/null
  rm -f /usr/local/bin/srvmon
  echo "done."
}

cmd_password() {
  need_root
  has_hub || fail "no hub is installed on this machine"
  local db user pass confirm
  db="$(conf_value "$HUB_CONF" SRVMON_DB || echo /var/lib/srvmon/srvmon.db)"
  read -rp "$(echo -e "${blue}?${plain} Username [admin]: ")" user
  user="${user:-admin}"
  while true; do
    read -rsp "$(echo -e "${blue}?${plain} New password (min 8 chars): ")" pass; echo
    [ ${#pass} -ge 8 ] || { warn "too short"; continue; }
    read -rsp "$(echo -e "${blue}?${plain} Repeat: ")" confirm; echo
    [ "$pass" = "$confirm" ] || { warn "they do not match"; continue; }
    break
  done
  /usr/local/bin/srvmon-hub -db "$db" -admin "${user}:${pass}" || fail "could not update the operator"
  info "password updated — every existing session was signed out"
}

menu() {
  while true; do
    echo
    echo -e "${green}srvmon${plain} — $(has_hub && echo -n 'hub '; has_agent && echo -n 'agent'; true) management"
    echo -e "  ${green}1.${plain} Status"
    echo -e "  ${green}2.${plain} Restart"
    echo -e "  ${green}3.${plain} Stop"
    echo -e "  ${green}4.${plain} Start"
    echo -e "  ${green}5.${plain} Follow logs"
    echo -e "  ${green}6.${plain} Update to the latest version"
    if has_hub; then
      echo -e "  ${green}7.${plain} Change the dashboard password"
      echo -e "  ${green}8.${plain} Show the dashboard URL"
    fi
    echo -e "  ${green}9.${plain} Uninstall"
    echo -e "  ${green}0.${plain} Exit"
    local choice=""
    read -rp "$(echo -e "${blue}?${plain} Choose: ")" choice
    case "$choice" in
      1) cmd_status ;;
      2) cmd_restart ;;
      3) cmd_stop ;;
      4) cmd_start ;;
      5) cmd_logs ;;
      6) cmd_update ;;
      7) has_hub && cmd_password || warn "hub only" ;;
      8) has_hub && cmd_url || warn "hub only" ;;
      9) cmd_uninstall; return ;;
      0|q|"") return ;;
      *) warn "unknown option" ;;
    esac
  done
}

if [[ "${1:-}" =~ ^(-h|--help|help)$ ]]; then
  sed -n '2,9p' "$0" | sed 's/^# \{0,1\}//'
  exit 0
fi

has_hub || has_agent || fail "neither the hub nor the agent is installed on this machine"

case "${1:-}" in
  "")          menu ;;
  status)      cmd_status ;;
  start)       cmd_start ;;
  stop)        cmd_stop ;;
  restart)     cmd_restart ;;
  logs)        cmd_logs ;;
  update)      cmd_update ;;
  uninstall)   cmd_uninstall ;;
  password)    cmd_password ;;
  url)         cmd_url ;;
  *) fail "unknown command: $1 (try: srvmon --help)" ;;
esac
