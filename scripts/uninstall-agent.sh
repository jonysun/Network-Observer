#!/bin/sh
set -eu

# Remove a native Linux systemd Agent or an OpenWrt procd Agent. The script is
# POSIX sh so it also works on OpenWrt; Bash can be used for the usual curl
# pipe on systemd hosts.

CONFIG_DIR=${NETWORK_OBSERVER_CONFIG_DIR:-/etc/network-observer}
STATE_DIR=${NETWORK_OBSERVER_STATE_DIR:-}
BINARY_PATH=${NETWORK_OBSERVER_BINARY:-}
BPF_DIR=${NETWORK_OBSERVER_BPF_DIR:-/usr/local/lib/network-observer}
SERVICE_NAME=${NETWORK_OBSERVER_SERVICE:-network-observer-agent}
SYSTEMD_DIR=${NETWORK_OBSERVER_SYSTEMD_DIR:-/etc/systemd/system}
INIT_DIR=${NETWORK_OBSERVER_INIT_DIR:-/etc/init.d}
YES=0
DRY_RUN=0
KEEP_CONFIG=0
KEEP_WAL=0
CONFIG_DIR_EXPLICIT=0
STATE_DIR_EXPLICIT=0
[ -n "${NETWORK_OBSERVER_CONFIG_DIR+x}" ] && CONFIG_DIR_EXPLICIT=1
[ -n "${NETWORK_OBSERVER_STATE_DIR+x}" ] && STATE_DIR_EXPLICIT=1

usage() {
  cat <<'EOF'
用法：uninstall-agent.sh [选项]

默认删除本机原生 Agent 的服务、二进制、配置、Center CA、设备凭证、WAL 和 eBPF 对象。
Center 中的设备记录不会自动删除，请在 Web 的“设备”页面吊销凭证。

  --yes                  不询问确认，适合 curl | bash / sh
  --dry-run              只显示将执行的操作，不修改文件或服务
  --keep-config          保留 agent.json、ca.crt 和配置目录
  --keep-wal             保留 WAL 和状态目录
  --config-dir DIR       配置目录，默认 /etc/network-observer
  --state-dir DIR        状态目录；Linux 默认 /var/lib/network-observer，OpenWrt 默认 /tmp/network-observer
  --binary PATH          要删除的 Agent 二进制；默认按安装方式自动检测
  --bpf-dir DIR          eBPF 对象目录，默认 /usr/local/lib/network-observer
  --service NAME         systemd 服务名，默认 network-observer-agent
EOF
}

die() {
  echo "卸载失败：$*" >&2
  exit 1
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --yes) YES=1; shift;;
    --dry-run) DRY_RUN=1; shift;;
    --keep-config|--keep-identity) KEEP_CONFIG=1; shift;;
    --keep-wal) KEEP_WAL=1; shift;;
    --config-dir) [ "$#" -ge 2 ] || die "--config-dir 缺少参数"; CONFIG_DIR=$2; CONFIG_DIR_EXPLICIT=1; shift 2;;
    --state-dir) [ "$#" -ge 2 ] || die "--state-dir 缺少参数"; STATE_DIR=$2; STATE_DIR_EXPLICIT=1; shift 2;;
    --binary) [ "$#" -ge 2 ] || die "--binary 缺少参数"; BINARY_PATH=$2; shift 2;;
    --bpf-dir) [ "$#" -ge 2 ] || die "--bpf-dir 缺少参数"; BPF_DIR=$2; shift 2;;
    --service) [ "$#" -ge 2 ] || die "--service 缺少参数"; SERVICE_NAME=$2; shift 2;;
    -h|--help) usage; exit 0;;
    *) echo "未知参数：$1" >&2; usage >&2; exit 2;;
  esac
done

# Some OpenWrt images keep /etc read-only. The installer can be pointed at a
# writable /tmp directory; discover that layout when the caller did not pass
# an explicit path.
if [ "$CONFIG_DIR_EXPLICIT" = 0 ] && [ "$CONFIG_DIR" = /etc/network-observer ] &&
  [ ! -e "$CONFIG_DIR/agent.json" ] && [ -e /tmp/network-observer/agent.json ]; then
  CONFIG_DIR=/tmp/network-observer
fi

# OpenWrt's installer uses /tmp for volatile state and stores its executable
# inside the config directory. Keep explicit arguments authoritative.
PROCD_SERVICE=""
if [ -x "$INIT_DIR/network-observer" ]; then
  PROCD_SERVICE=network-observer
  [ "$STATE_DIR_EXPLICIT" = 1 ] || STATE_DIR=/tmp/network-observer
elif [ -x "$INIT_DIR/network-observer-agent" ]; then
  PROCD_SERVICE=network-observer-agent
  [ "$STATE_DIR_EXPLICIT" = 1 ] || STATE_DIR=/tmp/network-observer
else
  [ "$STATE_DIR_EXPLICIT" = 1 ] || STATE_DIR=/var/lib/network-observer
fi

if [ -z "$BINARY_PATH" ]; then
  for candidate in \
    /usr/local/bin/network-observer-agent \
    /usr/bin/network-observer-agent \
    "$CONFIG_DIR/network-observer-agent"; do
    if [ -f "$candidate" ]; then
      BINARY_PATH=$candidate
      break
    fi
  done
fi

case "$SERVICE_NAME" in
  ""|*[!A-Za-z0-9_.@-]*) die "无效的 systemd 服务名称：$SERVICE_NAME";;
esac

SYSTEMD_UNIT="${SYSTEMD_DIR}/${SERVICE_NAME}.service"
SYSTEMD_DROPIN="${SYSTEMD_DIR}/${SERVICE_NAME}.service.d/10-runtime-compat.conf"
SYSTEMD_CHANGED=0
if [ -e "$SYSTEMD_UNIT" ] || [ -e "$SYSTEMD_DROPIN" ]; then
  SYSTEMD_CHANGED=1
fi

case "$CONFIG_DIR" in
  ""|/|/etc|/usr|/var|/tmp) die "拒绝删除过于宽泛的配置目录：$CONFIG_DIR";;
esac
case "$STATE_DIR" in
  ""|/|/var|/var/lib|/tmp) die "拒绝删除过于宽泛的状态目录：$STATE_DIR";;
esac
case "$BPF_DIR" in
  ""|/|/usr|/usr/local) die "拒绝删除过于宽泛的 eBPF 目录：$BPF_DIR";;
esac
for path in "$CONFIG_DIR" "$STATE_DIR" "$BPF_DIR"; do
  case "$path" in
    /*) ;;
    *) die "目录必须是绝对路径：$path";;
  esac
  case "/$path/" in
    */../*|*/./*) die "目录不能包含 . 或 .. 路径段：$path";;
  esac
  [ ! -L "$path" ] || die "卸载目录不能是符号链接：$path"
done
if [ -n "$BINARY_PATH" ]; then
  case "$BINARY_PATH" in
    /*) ;;
    *) die "二进制路径必须是绝对路径：$BINARY_PATH";;
  esac
  case "${BINARY_PATH##*/}" in
    network-observer-agent) ;;
    *) die "二进制文件名必须是 network-observer-agent：$BINARY_PATH";;
  esac
fi

if [ "$DRY_RUN" = 0 ]; then
  [ "$(id -u)" = 0 ] || die "原生 Agent 卸载需要 root，请使用 sudo。"
fi

echo "将卸载本机 Network Observer 原生 Agent："
echo "  systemd 服务：${SERVICE_NAME}.service（若存在）"
echo "  procd 服务：${PROCD_SERVICE:-无}"
echo "  二进制：${BINARY_PATH:-未找到}"
[ "$KEEP_CONFIG" = 1 ] || echo "  配置和设备凭证：$CONFIG_DIR"
[ "$KEEP_WAL" = 1 ] || echo "  WAL 和状态：$STATE_DIR"
echo "  eBPF 对象：$BPF_DIR/flow_counter.o"

if [ "$DRY_RUN" = 0 ] && [ "$YES" = 0 ]; then
  if [ ! -r /dev/tty ]; then
    die "当前不是交互终端；若确认卸载，请追加 --yes。"
  fi
  printf '继续并删除这些本地文件？输入 yes：' > /dev/tty
  read -r answer < /dev/tty || answer=""
  [ "$answer" = yes ] || { echo "已取消。"; exit 0; }
fi

run_systemd() {
  if [ "$DRY_RUN" = 1 ]; then
    echo "+ systemctl disable --now ${SERVICE_NAME}.service"
    echo "+ systemctl daemon-reload"
    return
  fi
  if [ "$SYSTEMD_CHANGED" = 1 ] && ! command -v systemctl >/dev/null 2>&1; then
    die "发现 systemd 服务文件但缺少 systemctl，无法确认服务已停止；未删除文件。"
  fi
  if command -v systemctl >/dev/null 2>&1 && { [ -e "$SYSTEMD_UNIT" ] || systemctl cat "$SERVICE_NAME" >/dev/null 2>&1; }; then
    systemctl disable --now "$SERVICE_NAME" >/dev/null 2>&1 || die "systemd 未能停止或禁用 ${SERVICE_NAME}.service；未删除文件。"
    if systemctl is-active --quiet "$SERVICE_NAME"; then
      die "${SERVICE_NAME}.service 仍在运行；未删除文件。"
    fi
  fi
}

run_procd() {
  [ -n "$PROCD_SERVICE" ] || return 0
  if [ "$DRY_RUN" = 1 ]; then
    echo "+ ${INIT_DIR}/${PROCD_SERVICE} stop"
    echo "+ ${INIT_DIR}/${PROCD_SERVICE} disable"
    return
  fi
  "$INIT_DIR/$PROCD_SERVICE" stop >/dev/null 2>&1 || die "procd 服务未能停止；未删除文件。"
  "$INIT_DIR/$PROCD_SERVICE" disable >/dev/null 2>&1 || die "procd 服务未能禁用；未删除文件。"
}

remove_file() {
  path=$1
  [ -e "$path" ] || [ -L "$path" ] || return 0
  if [ "$DRY_RUN" = 1 ]; then
    echo "+ rm -f $path"
  else
    rm -f "$path"
  fi
}

run_systemd
run_procd

# Remove only the generated service artifacts; other drop-ins remain untouched.
remove_file "$SYSTEMD_UNIT"
remove_file "$SYSTEMD_DROPIN"
if [ "$DRY_RUN" = 1 ]; then
  echo "+ rmdir --ignore-fail-on-non-empty ${SYSTEMD_DROPIN%/*}"
else
  rmdir "${SYSTEMD_DROPIN%/*}" 2>/dev/null || true
fi

if [ -n "$PROCD_SERVICE" ]; then
  remove_file "$INIT_DIR/$PROCD_SERVICE"
fi

if [ -n "$BINARY_PATH" ]; then
  remove_file "$BINARY_PATH"
fi
remove_file "$BPF_DIR/flow_counter.o"
if [ "$DRY_RUN" = 1 ]; then
  echo "+ rmdir --ignore-fail-on-non-empty $BPF_DIR"
else
  rmdir "$BPF_DIR" 2>/dev/null || true
fi

if [ "$DRY_RUN" = 0 ] && [ "$SYSTEMD_CHANGED" = 1 ] && command -v systemctl >/dev/null 2>&1; then
  systemctl daemon-reload >/dev/null 2>&1 || die "systemd daemon-reload 失败"
fi

if [ "$KEEP_CONFIG" = 0 ]; then
  # Remove only files created by the native installers; preserve unrelated
  # files if a custom directory is shared with another local service.
  remove_file "$CONFIG_DIR/agent.json"
  remove_file "$CONFIG_DIR/agent.json.tmp"
  remove_file "$CONFIG_DIR/ca.crt"
  remove_file "$CONFIG_DIR/network-observer-agent"
fi

if [ "$KEEP_WAL" = 0 ]; then
  remove_file "$STATE_DIR/agent.wal"
  remove_file "$STATE_DIR/agent.wal.lock"
  remove_file "$STATE_DIR/agent.wal.gaps"
  remove_file "$STATE_DIR/agent.wal.gaps.tmp"
  remove_file "$STATE_DIR/agent.wal.tmp"
fi

if [ "$DRY_RUN" = 1 ]; then
  [ "$KEEP_CONFIG" = 1 ] || echo "+ rmdir $CONFIG_DIR（仅空目录）"
  [ "$KEEP_WAL" = 1 ] || echo "+ rmdir $STATE_DIR（仅空目录）"
else
  [ "$KEEP_CONFIG" = 1 ] || rmdir "$CONFIG_DIR" 2>/dev/null || true
  [ "$KEEP_WAL" = 1 ] || rmdir "$STATE_DIR" 2>/dev/null || true
fi

if [ "$DRY_RUN" = 1 ]; then
  echo "试运行完成，未修改本机。"
else
  echo "本机 Agent 已卸载。Center 中的设备凭证仍存在，请在 Web 设备页吊销它。"
fi
