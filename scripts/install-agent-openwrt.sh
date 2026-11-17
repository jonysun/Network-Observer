#!/bin/sh
set -eu

# Network Observer OpenWrt Gateway Lite installer. POSIX sh only: no Bash,
# curl, systemd, Docker or package-manager assumptions.
REPO="${NETWORK_OBSERVER_REPO:-jonysun/Network-Observer}"
VERSION="${NETWORK_OBSERVER_VERSION:-v0.3.20}"
SERVER=""
TOKEN=""
NAME=""
SECURITY_MODE=true
CA_URL=""
FINGERPRINT=""
TLS_SERVER_NAME=""
CONFIG_DIR="/etc/network-observer"
STATE_DIR="/tmp/network-observer"
INSTALL_BIN="/usr/bin/network-observer-agent"

usage() {
  cat <<'EOF'
用法：install-agent-openwrt.sh --server HOST:PORT --token TOKEN --name NAME [选项]
  --version vX.Y.Z             固定版本（默认 v0.3.20）
  --security-mode true|false   TLS 或明文（默认 true）
  --ca-url URL                 Center CA 地址（TLS 必填）
  --fingerprint SHA256         Center CA/证书 DER SHA-256 指纹（TLS 必填）
  --tls-server-name NAME       TLS 证书名称（可选）
  --config-dir DIR             配置目录（默认 /etc/network-observer）
  --state-dir DIR              RAM 状态目录（默认 /tmp/network-observer）
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --server) SERVER=$2; shift 2;;
    --token) TOKEN=$2; shift 2;;
    --name) NAME=$2; shift 2;;
    --version) VERSION=$2; shift 2;;
    --security-mode) SECURITY_MODE=$2; shift 2;;
    --ca-url) CA_URL=$2; shift 2;;
    --fingerprint) FINGERPRINT=$2; shift 2;;
    --tls-server-name) TLS_SERVER_NAME=$2; shift 2;;
    --config-dir) CONFIG_DIR=$2; shift 2;;
    --state-dir) STATE_DIR=$2; shift 2;;
    -h|--help) usage; exit 0;;
    *) echo "未知参数：$1" >&2; usage >&2; exit 2;;
  esac
done

[ "$(id -u)" = 0 ] || { echo "OpenWrt 安装需要 root" >&2; exit 1; }
[ -n "$SERVER" ] && [ -n "$TOKEN" ] && [ -n "$NAME" ] || { echo "--server、--token、--name 均必填" >&2; exit 2; }
[ "$SECURITY_MODE" = true ] || [ "$SECURITY_MODE" = false ] || { echo "--security-mode 必须为 true 或 false" >&2; exit 2; }
if [ "$SECURITY_MODE" = true ]; then
  [ -n "$CA_URL" ] && [ -n "$FINGERPRINT" ] || { echo "TLS 模式必须提供 --ca-url 和 --fingerprint" >&2; exit 2; }
fi
[ -x /sbin/procd ] || [ -x /sbin/ubus ] || { echo "未检测到 OpenWrt procd/ubus，已停止" >&2; exit 1; }

if [ -r /proc/net/nf_conntrack ]; then
  echo "检测到 conntrack：/proc/net/nf_conntrack"
elif [ -r /proc/net/ip_conntrack ]; then
  echo "检测到 conntrack：/proc/net/ip_conntrack"
else
  echo "警告：未检测到可读 conntrack 文件，Agent 仍会安装并在 Center 报告 gateway_conntrack=unavailable" >&2
fi
if [ -r /proc/sys/net/netfilter/nf_conntrack_acct ]; then
  echo "conntrack 字节计数开关：$(cat /proc/sys/net/netfilter/nf_conntrack_acct)"
else
  echo "警告：无法读取 nf_conntrack_acct，字节计数能力将在运行时标记 unknown" >&2
fi

case "$(uname -m)" in
  aarch64|arm64) ARCH=arm64;;
  armv7*|armv8l) ARCH=armv7;;
  *) echo "不支持的 ARM 架构：$(uname -m)（首版仅支持 arm64/ARMv7）" >&2; exit 1;;
esac

fetch() {
  url=$1; out=$2
  if command -v uclient-fetch >/dev/null 2>&1; then
    uclient-fetch -q -O "$out" "$url"
  elif command -v wget >/dev/null 2>&1; then
    wget -q -O "$out" "$url"
  else
    echo "缺少 uclient-fetch 或 HTTPS wget，无法安全下载" >&2
    return 1
  fi
}

fetch_bootstrap() {
  url=$1; out=$2
  if command -v uclient-fetch >/dev/null 2>&1; then
    uclient-fetch -q --no-check-certificate -O "$out" "$url"
  elif command -v wget >/dev/null 2>&1; then
    wget -q --no-check-certificate -O "$out" "$url"
  else
    echo "缺少 uclient-fetch 或 HTTPS wget，无法下载 Center 证书" >&2
    return 1
  fi
}

TMP="/tmp/network-observer-install.$$"
cleanup() { rm -rf "$TMP"; }
trap cleanup EXIT INT TERM
mkdir -p "$TMP" "$CONFIG_DIR" "$STATE_DIR"
[ -w "$STATE_DIR" ] || { echo "状态目录不可写：$STATE_DIR" >&2; exit 1; }
[ "$(df -k "$STATE_DIR" 2>/dev/null | awk 'NR==2 {print $4+0}')" -ge 32768 ] 2>/dev/null || echo "警告：/tmp 可用空间低于 32 MiB，WAL 可能较快产生数据缺口" >&2
[ ! -e "$CONFIG_DIR/agent.json" ] || { echo "已有配置，拒绝覆盖：$CONFIG_DIR/agent.json" >&2; exit 1; }

BASE="https://github.com/$REPO/releases/download/$VERSION"
BINARY="network-observer-agent-openwrt-linux-$ARCH"
fetch "$BASE/$BINARY" "$TMP/$BINARY"
fetch "$BASE/SHA256SUMS" "$TMP/SHA256SUMS"
grep "[[:space:]]$BINARY$" "$TMP/SHA256SUMS" > "$TMP/checksum" || { echo "发布包缺少 $BINARY 的校验和" >&2; exit 1; }
(cd "$TMP" && sha256sum -c checksum) || { echo "Agent 摘要校验失败" >&2; exit 1; }
chmod 0755 "$TMP/$BINARY"

if [ "$SECURITY_MODE" = true ]; then
  fetch_bootstrap "$CA_URL" "$TMP/ca.crt"
  "$TMP/$BINARY" verify-cert --file "$TMP/ca.crt" --fingerprint "$FINGERPRINT" >/dev/null || { echo "Center 证书指纹不匹配，已停止安装" >&2; exit 1; }
  install -m 0644 "$TMP/ca.crt" "$CONFIG_DIR/ca.crt"
fi

install -m 0755 "$TMP/$BINARY" "$INSTALL_BIN"
chmod 0700 "$CONFIG_DIR" "$STATE_DIR"
CA_ARGS=""
if [ "$SECURITY_MODE" = true ]; then CA_ARGS="--ca $CONFIG_DIR/ca.crt"; fi
"$INSTALL_BIN" register --server "$SERVER" --tls-server-name "$TLS_SERVER_NAME" $CA_ARGS \
  --security-mode="$SECURITY_MODE" --token "$TOKEN" --name "$NAME" --type gateway \
  --config "$CONFIG_DIR/agent.json" --wal "$STATE_DIR/agent.wal" \
  --wal-max-bytes 16777216 --volatile-wal --snapshot-interval 2
chmod 0600 "$CONFIG_DIR/agent.json"

cat > /etc/init.d/network-observer <<'EOF'
#!/bin/sh /etc/rc.common
START=99
STOP=10
USE_PROCD=1

start_service() {
  procd_open_instance
  procd_set_param command /usr/bin/network-observer-agent run --config /etc/network-observer/agent.json
  procd_set_param respawn 3600 5 5
  procd_set_param stdout 1
  procd_set_param stderr 1
  procd_close_instance
}
EOF
chmod 0755 /etc/init.d/network-observer
/etc/init.d/network-observer enable
/etc/init.d/network-observer start
echo "OpenWrt Gateway Lite 已安装并启动（$ARCH，版本 $VERSION）"
