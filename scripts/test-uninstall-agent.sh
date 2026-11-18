#!/bin/sh
set -eu

[ "$(id -u)" = 0 ] || { echo "test-uninstall-agent.sh must run as root" >&2; exit 1; }

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
UNINSTALLER="$ROOT/scripts/uninstall-agent.sh"
TMP_ROOT=$(mktemp -d)
trap 'rm -rf "$TMP_ROOT"' EXIT HUP INT TERM

SYSTEMD_DIR="$TMP_ROOT/systemd"
INIT_DIR="$TMP_ROOT/init"
FAKE_BIN="$TMP_ROOT/bin"
mkdir -p "$SYSTEMD_DIR" "$INIT_DIR" "$FAKE_BIN"

cat > "$FAKE_BIN/systemctl" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >> "$SYSTEMCTL_LOG"
case "$1" in
  cat) [ -e "$SYSTEMD_TEST_UNIT" ] ;;
  is-active) exit 1;;
  disable) [ "${SYSTEMCTL_STOP_FAIL:-0}" = 0 ];;
  daemon-reload) exit 0;;
  *) exit 2;;
esac
EOF
chmod 0755 "$FAKE_BIN/systemctl"

assert_exists() {
  [ -e "$1" ] || { echo "expected path to exist: $1" >&2; exit 1; }
}

assert_missing() {
  [ ! -e "$1" ] || { echo "expected path to be removed: $1" >&2; exit 1; }
}

write_file() {
  mkdir -p "$(dirname -- "$1")"
  printf 'fixture\n' > "$1"
}

SYSTEMD_CONFIG="$TMP_ROOT/systemd-config"
SYSTEMD_STATE="$TMP_ROOT/systemd-state"
SYSTEMD_BPF="$TMP_ROOT/systemd-bpf"
SYSTEMD_BINARY="$TMP_ROOT/systemd-bin/network-observer-agent"
SYSTEMD_UNIT="$SYSTEMD_DIR/network-observer-agent.service"
SYSTEMD_DROPIN_DIR="$SYSTEMD_DIR/network-observer-agent.service.d"
mkdir -p "$SYSTEMD_DROPIN_DIR"
write_file "$SYSTEMD_CONFIG/agent.json"
write_file "$SYSTEMD_CONFIG/agent.json.tmp"
write_file "$SYSTEMD_CONFIG/ca.crt"
write_file "$SYSTEMD_CONFIG/keep.txt"
write_file "$SYSTEMD_STATE/agent.wal"
write_file "$SYSTEMD_STATE/agent.wal.lock"
write_file "$SYSTEMD_STATE/agent.wal.gaps"
write_file "$SYSTEMD_STATE/agent.wal.gaps.tmp"
write_file "$SYSTEMD_STATE/agent.wal.tmp"
write_file "$SYSTEMD_STATE/keep.txt"
write_file "$SYSTEMD_BPF/flow_counter.o"
write_file "$SYSTEMD_BPF/keep.txt"
write_file "$SYSTEMD_BINARY"
write_file "$SYSTEMD_UNIT"
write_file "$SYSTEMD_DROPIN_DIR/10-runtime-compat.conf"
write_file "$SYSTEMD_DROPIN_DIR/custom.conf"

PATH="$FAKE_BIN:$PATH" \
SYSTEMCTL_LOG="$TMP_ROOT/systemctl.log" \
SYSTEMD_TEST_UNIT="$SYSTEMD_UNIT" \
NETWORK_OBSERVER_SYSTEMD_DIR="$SYSTEMD_DIR" \
NETWORK_OBSERVER_INIT_DIR="$INIT_DIR" \
sh "$UNINSTALLER" --yes \
  --config-dir "$SYSTEMD_CONFIG" \
  --state-dir "$SYSTEMD_STATE" \
  --binary "$SYSTEMD_BINARY" \
  --bpf-dir "$SYSTEMD_BPF"

assert_missing "$SYSTEMD_UNIT"
assert_missing "$SYSTEMD_DROPIN_DIR/10-runtime-compat.conf"
assert_exists "$SYSTEMD_DROPIN_DIR/custom.conf"
assert_missing "$SYSTEMD_BINARY"
assert_missing "$SYSTEMD_BPF/flow_counter.o"
assert_exists "$SYSTEMD_BPF/keep.txt"
assert_missing "$SYSTEMD_CONFIG/agent.json"
assert_missing "$SYSTEMD_CONFIG/agent.json.tmp"
assert_missing "$SYSTEMD_CONFIG/ca.crt"
assert_exists "$SYSTEMD_CONFIG/keep.txt"
assert_missing "$SYSTEMD_STATE/agent.wal"
assert_missing "$SYSTEMD_STATE/agent.wal.lock"
assert_missing "$SYSTEMD_STATE/agent.wal.gaps"
assert_missing "$SYSTEMD_STATE/agent.wal.gaps.tmp"
assert_missing "$SYSTEMD_STATE/agent.wal.tmp"
assert_exists "$SYSTEMD_STATE/keep.txt"
grep -q '^disable --now network-observer-agent$' "$TMP_ROOT/systemctl.log"
grep -q '^daemon-reload$' "$TMP_ROOT/systemctl.log"

PROCD_CONFIG="$TMP_ROOT/procd-config"
PROCD_STATE="$TMP_ROOT/procd-state"
PROCD_BPF="$TMP_ROOT/procd-bpf"
PROCD_LOG="$TMP_ROOT/procd.log"
mkdir -p "$PROCD_CONFIG" "$PROCD_STATE" "$PROCD_BPF"
write_file "$PROCD_CONFIG/agent.json"
write_file "$PROCD_CONFIG/ca.crt"
write_file "$PROCD_CONFIG/network-observer-agent"
write_file "$PROCD_STATE/agent.wal"
write_file "$PROCD_BPF/flow_counter.o"
cat > "$INIT_DIR/network-observer" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >> "$PROCD_LOG"
if [ "$1" = stop ] && [ "${PROCD_STOP_FAIL:-0}" = 1 ]; then exit 1; fi
EOF
chmod 0755 "$INIT_DIR/network-observer"

PATH="$FAKE_BIN:$PATH" \
SYSTEMCTL_LOG="$TMP_ROOT/procd-systemctl.log" \
SYSTEMD_TEST_UNIT="$TMP_ROOT/nonexistent.service" \
NETWORK_OBSERVER_SYSTEMD_DIR="$SYSTEMD_DIR" \
NETWORK_OBSERVER_INIT_DIR="$INIT_DIR" \
PROCD_LOG="$PROCD_LOG" \
sh "$UNINSTALLER" --yes \
  --config-dir "$PROCD_CONFIG" \
  --state-dir "$PROCD_STATE" \
  --binary "$PROCD_CONFIG/network-observer-agent" \
  --bpf-dir "$PROCD_BPF"

assert_missing "$INIT_DIR/network-observer"
assert_missing "$PROCD_CONFIG/agent.json"
assert_missing "$PROCD_CONFIG/ca.crt"
assert_missing "$PROCD_CONFIG/network-observer-agent"
assert_missing "$PROCD_STATE/agent.wal"
assert_missing "$PROCD_BPF/flow_counter.o"
grep -q '^stop$' "$PROCD_LOG"
grep -q '^disable$' "$PROCD_LOG"

KEEP_CONFIG_DIR="$TMP_ROOT/keep-config"
KEEP_STATE_DIR="$TMP_ROOT/keep-state"
KEEP_BINARY="$TMP_ROOT/keep-bin/network-observer-agent"
KEEP_BPF="$TMP_ROOT/keep-bpf"
mkdir -p "$KEEP_CONFIG_DIR" "$KEEP_STATE_DIR" "$KEEP_BPF"
write_file "$KEEP_CONFIG_DIR/agent.json"
write_file "$KEEP_CONFIG_DIR/ca.crt"
write_file "$KEEP_STATE_DIR/agent.wal"
write_file "$KEEP_BINARY"

PATH="$FAKE_BIN:$PATH" \
SYSTEMCTL_LOG="$TMP_ROOT/keep-systemctl.log" \
SYSTEMD_TEST_UNIT="$TMP_ROOT/no-unit.service" \
NETWORK_OBSERVER_SYSTEMD_DIR="$SYSTEMD_DIR" \
NETWORK_OBSERVER_INIT_DIR="$INIT_DIR" \
sh "$UNINSTALLER" --yes --keep-config --keep-wal \
  --config-dir "$KEEP_CONFIG_DIR" \
  --state-dir "$KEEP_STATE_DIR" \
  --binary "$KEEP_BINARY" \
  --bpf-dir "$KEEP_BPF"

assert_exists "$KEEP_CONFIG_DIR/agent.json"
assert_exists "$KEEP_CONFIG_DIR/ca.crt"
assert_exists "$KEEP_STATE_DIR/agent.wal"
assert_missing "$KEEP_BINARY"

STOP_CONFIG_DIR="$TMP_ROOT/stop-failure-config"
STOP_STATE_DIR="$TMP_ROOT/stop-failure-state"
STOP_BINARY="$TMP_ROOT/stop-failure-bin/network-observer-agent"
STOP_BPF="$TMP_ROOT/stop-failure-bpf"
STOP_UNIT="$SYSTEMD_DIR/network-observer-agent.service"
mkdir -p "$STOP_CONFIG_DIR" "$STOP_STATE_DIR" "$STOP_BPF"
write_file "$STOP_CONFIG_DIR/agent.json"
write_file "$STOP_STATE_DIR/agent.wal"
write_file "$STOP_BINARY"
write_file "$STOP_BPF/flow_counter.o"
write_file "$STOP_UNIT"
if PATH="$FAKE_BIN:$PATH" \
  SYSTEMCTL_LOG="$TMP_ROOT/stop-failure-systemctl.log" \
  SYSTEMCTL_STOP_FAIL=1 \
  SYSTEMD_TEST_UNIT="$STOP_UNIT" \
  NETWORK_OBSERVER_SYSTEMD_DIR="$SYSTEMD_DIR" \
  NETWORK_OBSERVER_INIT_DIR="$INIT_DIR" \
  sh "$UNINSTALLER" --yes \
    --config-dir "$STOP_CONFIG_DIR" \
    --state-dir "$STOP_STATE_DIR" \
    --binary "$STOP_BINARY" \
    --bpf-dir "$STOP_BPF" >/dev/null 2>&1; then
  echo "expected systemd stop failure" >&2
  exit 1
fi
assert_exists "$STOP_UNIT"
assert_exists "$STOP_BINARY"
assert_exists "$STOP_CONFIG_DIR/agent.json"
assert_exists "$STOP_STATE_DIR/agent.wal"
assert_exists "$STOP_BPF/flow_counter.o"

PROCD_STOP_DIR="$TMP_ROOT/procd-stop-failure"
PROCD_STOP_CONFIG="$TMP_ROOT/procd-stop-failure-config"
PROCD_STOP_STATE="$TMP_ROOT/procd-stop-failure-state"
PROCD_STOP_BPF="$TMP_ROOT/procd-stop-failure-bpf"
mkdir -p "$PROCD_STOP_DIR" "$PROCD_STOP_CONFIG" "$PROCD_STOP_STATE" "$PROCD_STOP_BPF"
write_file "$PROCD_STOP_CONFIG/agent.json"
write_file "$PROCD_STOP_CONFIG/network-observer-agent"
write_file "$PROCD_STOP_STATE/agent.wal"
write_file "$PROCD_STOP_BPF/flow_counter.o"
cat > "$PROCD_STOP_DIR/network-observer" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >> "$PROCD_LOG"
if [ "$1" = stop ] && [ "${PROCD_STOP_FAIL:-0}" = 1 ]; then exit 1; fi
EOF
chmod 0755 "$PROCD_STOP_DIR/network-observer"
if PATH="$FAKE_BIN:$PATH" \
  SYSTEMCTL_LOG="$TMP_ROOT/procd-stop-systemctl.log" \
  PROCD_LOG="$TMP_ROOT/procd-stop.log" \
  PROCD_STOP_FAIL=1 \
  SYSTEMD_TEST_UNIT="$TMP_ROOT/nonexistent-procd-stop.service" \
  NETWORK_OBSERVER_SYSTEMD_DIR="$SYSTEMD_DIR" \
  NETWORK_OBSERVER_INIT_DIR="$PROCD_STOP_DIR" \
  sh "$UNINSTALLER" --yes \
    --config-dir "$PROCD_STOP_CONFIG" \
    --state-dir "$PROCD_STOP_STATE" \
    --binary "$PROCD_STOP_CONFIG/network-observer-agent" \
    --bpf-dir "$PROCD_STOP_BPF" >/dev/null 2>&1; then
  echo "expected procd stop failure" >&2
  exit 1
fi
assert_exists "$PROCD_STOP_DIR/network-observer"
assert_exists "$PROCD_STOP_CONFIG/agent.json"
assert_exists "$PROCD_STOP_CONFIG/network-observer-agent"
assert_exists "$PROCD_STOP_STATE/agent.wal"
assert_exists "$PROCD_STOP_BPF/flow_counter.o"

DRY_CONFIG="$TMP_ROOT/dry-config"
DRY_STATE="$TMP_ROOT/dry-state"
DRY_BINARY="$TMP_ROOT/dry-bin/network-observer-agent"
DRY_BPF="$TMP_ROOT/dry-bpf"
mkdir -p "$DRY_CONFIG" "$DRY_STATE" "$DRY_BPF"
write_file "$DRY_CONFIG/agent.json"
write_file "$DRY_STATE/agent.wal"
write_file "$DRY_BINARY"
write_file "$DRY_BPF/flow_counter.o"

PATH="$FAKE_BIN:$PATH" \
SYSTEMCTL_LOG="$TMP_ROOT/dry-systemctl.log" \
SYSTEMD_TEST_UNIT="$TMP_ROOT/no-dry-unit.service" \
NETWORK_OBSERVER_SYSTEMD_DIR="$SYSTEMD_DIR" \
NETWORK_OBSERVER_INIT_DIR="$INIT_DIR" \
sh "$UNINSTALLER" --dry-run \
  --config-dir "$DRY_CONFIG" \
  --state-dir "$DRY_STATE" \
  --binary "$DRY_BINARY" \
  --bpf-dir "$DRY_BPF" >/dev/null

assert_exists "$DRY_CONFIG/agent.json"
assert_exists "$DRY_STATE/agent.wal"
assert_exists "$DRY_BINARY"
assert_exists "$DRY_BPF/flow_counter.o"

echo "uninstall-agent tests passed"
