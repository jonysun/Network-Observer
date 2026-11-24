#!/usr/bin/env bash
set -Eeuo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CLI="$ROOT/scripts/network-observer"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }

mkdir -p "$TMP/config" "$TMP/state" "$TMP/bin"
if printf '' | NETWORK_OBSERVER_CONFIG_DIR="$TMP/config" bash "$CLI" >/dev/null 2>&1; then
  fail 'non-interactive menu should refuse to block'
fi

cat > "$TMP/bin/systemctl" <<'SYSTEMCTL'
#!/bin/sh
echo "$*" >> "$SYSTEMCTL_LOG"
case "$1" in
  is-active|restart|status|show|cat|daemon-reload|start|stop|enable) [ "${SYSTEMCTL_FAIL:-}" != "$1" ] ;;
  *) exit 0;;
esac
SYSTEMCTL
cat > "$TMP/bin/curl" <<'CURL'
#!/bin/sh
printf '{"tag_name":"v0.3.44"}\n'
CURL
chmod +x "$TMP/bin/systemctl" "$TMP/bin/curl"
export PATH="$TMP/bin:$PATH" SYSTEMCTL_LOG="$TMP/systemctl.log"
export NETWORK_OBSERVER_CONFIG_DIR="$TMP/config" NETWORK_OBSERVER_STATE_DIR="$TMP/state"
export NETWORK_OBSERVER_BINARY="$TMP/bin/network-observer-agent"
export NETWORK_OBSERVER_BPF_DIR="$TMP/bpf" NETWORK_OBSERVER_SYSTEMD_DIR="$TMP/systemd"
export NETWORK_OBSERVER_SERVICE=network-observer-agent
export NETWORK_OBSERVER_UPGRADE_SCRIPT="$TMP/upgrade-agent.sh"
export NETWORK_OBSERVER_UNINSTALL_SCRIPT="$TMP/uninstall-agent.sh"
export NETWORK_OBSERVER_INSTALL_SCRIPT="$TMP/install-agent.sh"

# Native Windows cannot reproduce Linux atomic rename/systemd semantics; exercise these integration cases in Linux CI.
case "$(uname -s)" in
  MINGW*|MSYS*|CYGWIN*) echo 'SKIP: DNS JSON/systemd integration tests require Linux.';;
  *)
    if ! python3 -c 'import json' >/dev/null 2>&1; then fail 'Python 3 is required for DNS tests'; fi
    printf '{"server_address":"center:8444","dns_metadata_enabled":false,"device_name":"test"}\n' > "$TMP/config/agent.json"
    # DNS change mutates only its key and restarts the service.
    bash "$CLI" dns enable >/dev/null || fail 'dns enable should work'
    python3 - "$TMP/config/agent.json" <<'PY'
import json,sys
with open(sys.argv[1],encoding='utf-8') as f: obj=json.load(f)
assert obj['dns_metadata_enabled'] is True
assert obj['server_address']=='center:8444' and obj['device_name']=='test'
PY
    grep -q '^restart network-observer-agent$' "$TMP/systemctl.log" || fail 'DNS change must restart Agent'
    # Idempotent setting does not cause needless restart.
    : > "$TMP/systemctl.log"
    bash "$CLI" dns enable >/dev/null || fail 'idempotent dns enable should work'
    [ ! -s "$TMP/systemctl.log" ] || fail 'unchanged DNS setting should not restart Agent'
    # Failed restart restores the original config.
    export SYSTEMCTL_FAIL=restart
    if bash "$CLI" dns disable >/dev/null 2>&1; then fail 'DNS change should report failed restart'; fi
    unset SYSTEMCTL_FAIL
    python3 - "$TMP/config/agent.json" <<'PY'
import json,sys
with open(sys.argv[1],encoding='utf-8') as f: assert json.load(f)['dns_metadata_enabled'] is True
PY
    ;;
esac

# Explicit versions are validated before invoking the updater.
cat > "$TMP/upgrade-agent.sh" <<'UPGRADE'
#!/bin/sh
printf '%s\n' "$*" >> "$UPGRADE_LOG"
UPGRADE
cat > "$TMP/uninstall-agent.sh" <<'UNINSTALL'
#!/bin/sh
printf '%s\n' "$*" >> "$UNINSTALL_LOG"
UNINSTALL
cat > "$TMP/install-agent.sh" <<'INSTALL'
#!/bin/sh
printf '%s\n' "$*" >> "$INSTALL_LOG"
INSTALL
chmod +x "$TMP/upgrade-agent.sh" "$TMP/uninstall-agent.sh" "$TMP/install-agent.sh"
export UPGRADE_LOG="$TMP/upgrade.log" UNINSTALL_LOG="$TMP/uninstall.log" INSTALL_LOG="$TMP/install.log"
if bash "$CLI" upgrade-agent --version invalid >/dev/null 2>&1; then fail 'invalid version should be rejected'; fi
[ ! -e "$TMP/upgrade.log" ] || fail 'invalid version reached updater'
# Piped y is not a terminal confirmation and must never perform destructive work.
if printf 'y\n' | bash "$CLI" uninstall >/dev/null 2>&1; then :; fi
[ ! -e "$TMP/uninstall.log" ] || fail 'non-TTY uninstall executed despite piped confirmation'

# Exercise interactive confirmation and choices through a PTY where available.
if command -v script >/dev/null 2>&1; then
  run_tty() {
    local input="$1"; shift
    printf '%s' "$input" | script -qec "bash \"$CLI\" $*" /dev/null >/dev/null 2>&1
  }
  if run_tty $'y\n' upgrade-agent --version v0.3.41; then :; else fail 'confirmed pinned upgrade should succeed'; fi
  grep -q -- '--version v0.3.41' "$TMP/upgrade.log" || fail 'pinned version was not passed to updater'
  : > "$TMP/upgrade.log"
  if run_tty $'n\n' upgrade-agent --version v0.3.41; then :; else fail 'declined upgrade should exit cleanly'; fi
  [ ! -s "$TMP/upgrade.log" ] || fail 'declined upgrade reached updater'
  if run_tty $'1\n1\ny\n0\n' ; then :; else fail 'latest-version menu flow should succeed'; fi
  cat "$TMP/upgrade.log" >&2
  grep -q -- '--version v0.3.44' "$TMP/upgrade.log" || fail 'latest menu selection did not resolve the release tag'
  : > "$TMP/upgrade.log"
  if run_tty $'y\n2\nv0.3.40\ny\n' reinstall --mode preserve; then :; else fail 'preserve reinstall should succeed'; fi
  grep -q -- '--version v0.3.40' "$TMP/upgrade.log" || fail 'preserve reinstall did not pass selected version'
  [ ! -s "$TMP/uninstall.log" ] || fail 'preserve reinstall must not uninstall retained identity/WAL'
  : > "$TMP/uninstall.log"
  if run_tty $'2\nv0.3.39\ncenter:8444\nfake-token\nrouter\ngateway\nn\ny\n' reinstall --mode full; then :; else fail 'full reinstall flow should succeed'; fi
  grep -q -- '--yes' "$TMP/uninstall.log" || fail 'full reinstall did not invoke uninstall after confirmation'
  grep -q -- '--version v0.3.39' "$TMP/install.log" || fail 'full reinstall did not pass selected version'
  grep -q -- '--type gateway' "$TMP/install.log" || fail 'full reinstall did not retain selected device type'
else
  echo 'SKIP: PTY confirmation tests require util-linux script(1).'
fi

echo 'network-observer tests passed'
