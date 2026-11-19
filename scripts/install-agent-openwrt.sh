#!/bin/sh
set -eu

# Network Observer OpenWrt Gateway Lite installer. POSIX sh only: no Bash,
# curl, systemd, Docker or package-manager assumptions.
REPO="${NETWORK_OBSERVER_REPO:-jonysun/Network-Observer}"
VERSION="${NETWORK_OBSERVER_VERSION:-v0.3.33}"
DOWNLOAD_BASE="${NETWORK_OBSERVER_DOWNLOAD_BASE:-}"
SERVER=""
TOKEN=""
NAME=""
SECURITY_MODE=true
CA_URL=""
FINGERPRINT=""
TLS_SERVER_NAME=""
DNS_METADATA_ENABLED=false
CONFIG_DIR="/etc/network-observer"
STATE_DIR="/tmp/network-observer"
INSTALL_BIN="${NETWORK_OBSERVER_INSTALL_BIN:-$CONFIG_DIR/network-observer-agent}"

# Avoid Go 1.24+ startup failure when the router exports obsolete GODEBUG keys.
unset GODEBUG || true

usage() {
  cat <<'EOF'
用法：install-agent-openwrt.sh --server HOST:PORT --token TOKEN --name NAME [选项]
  --version vX.Y.Z             固定版本（默认 v0.3.33）
  --download-base URL          资产下载根地址（明文模式默认推导为 Center:8080）
  --security-mode true|false   TLS 或明文（默认 true）
  --dns-metadata-enabled true|false  采集可见 UDP/53 DNS 元数据（默认 false）
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
    --download-base) DOWNLOAD_BASE=$2; shift 2;;
    --security-mode) SECURITY_MODE=$2; shift 2;;
    --dns-metadata-enabled) DNS_METADATA_ENABLED=$2; shift 2;;
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
[ "$DNS_METADATA_ENABLED" = true ] || [ "$DNS_METADATA_ENABLED" = false ] || { echo "--dns-metadata-enabled 必须为 true 或 false" >&2; exit 2; }
if [ -z "$DOWNLOAD_BASE" ]; then
  if [ "$SECURITY_MODE" = false ]; then
    SERVER_HOST=${SERVER%:*}
    DOWNLOAD_BASE="http://${SERVER_HOST}:8080/bootstrap/openwrt"
  else
    DOWNLOAD_BASE="https://github.com/$REPO/releases/download"
  fi
fi
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
    uclient-fetch -O "$out" "$url" || { echo "下载失败：$url" >&2; return 1; }
  elif command -v wget >/dev/null 2>&1; then
    wget -O "$out" "$url" || { echo "下载失败：$url" >&2; return 1; }
  else
    echo "缺少 uclient-fetch 或 HTTPS wget，无法安全下载" >&2
    return 1
  fi
}

fetch_bootstrap() {
  url=$1; out=$2
  if command -v uclient-fetch >/dev/null 2>&1; then
    uclient-fetch --no-check-certificate -O "$out" "$url" || { echo "下载 Center 证书失败：$url" >&2; return 1; }
  elif command -v wget >/dev/null 2>&1; then
    wget --no-check-certificate -O "$out" "$url" || { echo "下载 Center 证书失败：$url" >&2; return 1; }
  else
    echo "缺少 uclient-fetch 或 HTTPS wget，无法下载 Center 证书" >&2
    return 1
  fi
}

sha256_file() {
  file=$1
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$file" | awk '{print $1}'
    return 0
  fi
  if command -v busybox >/dev/null 2>&1; then
    digest=$(busybox sha256sum "$file" 2>/dev/null) || digest=""
    if [ -n "$digest" ]; then
      printf '%s\n' "$digest" | awk '{print $1}'
      return 0
    fi
  fi
  if command -v openssl >/dev/null 2>&1; then
    openssl dgst -sha256 "$file" 2>/dev/null | awk -F'= ' '{print $2}'
    return 0
  fi
  echo "缺少 SHA-256 校验工具：需要 sha256sum、BusyBox sha256sum applet 或 openssl" >&2
  return 1
}

copy_with_mode() {
  mode=$1
  source=$2
  destination=$3
  if command -v install >/dev/null 2>&1; then
    install -m "$mode" "$source" "$destination"
  elif command -v cp >/dev/null 2>&1; then
    cp "$source" "$destination" && chmod "$mode" "$destination"
  else
    echo "缺少 install 或 cp，无法写入 Agent 文件：$destination" >&2
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

BASE="${DOWNLOAD_BASE%/}/$VERSION"
BINARY="network-observer-agent-openwrt-linux-$ARCH"
fetch "$BASE/$BINARY" "$TMP/$BINARY"
fetch "$BASE/SHA256SUMS" "$TMP/SHA256SUMS"
grep "[[:space:]]$BINARY$" "$TMP/SHA256SUMS" > "$TMP/checksum" || { echo "发布包缺少 $BINARY 的校验和" >&2; exit 1; }
EXPECTED_SHA256=$(awk '{print $1}' "$TMP/checksum")
ACTUAL_SHA256=$(sha256_file "$TMP/$BINARY") || { echo "无法校验 Agent 摘要，已停止安装" >&2; exit 1; }
[ "$EXPECTED_SHA256" = "$ACTUAL_SHA256" ] || { echo "Agent 摘要校验失败" >&2; exit 1; }
echo "Agent 摘要校验通过"
chmod 0755 "$TMP/$BINARY"

if [ "$SECURITY_MODE" = true ]; then
  fetch_bootstrap "$CA_URL" "$TMP/ca.crt"
  "$TMP/$BINARY" verify-cert --file "$TMP/ca.crt" --fingerprint "$FINGERPRINT" >/dev/null || { echo "Center 证书指纹不匹配，已停止安装" >&2; exit 1; }
  copy_with_mode 0644 "$TMP/ca.crt" "$CONFIG_DIR/ca.crt"
fi

copy_with_mode 0755 "$TMP/$BINARY" "$INSTALL_BIN"
chmod 0700 "$CONFIG_DIR" "$STATE_DIR"
CA_ARGS=""
if [ "$SECURITY_MODE" = true ]; then CA_ARGS="--ca $CONFIG_DIR/ca.crt"; fi
"$INSTALL_BIN" register --server "$SERVER" --tls-server-name "$TLS_SERVER_NAME" $CA_ARGS \
  --security-mode="$SECURITY_MODE" --dns-metadata-enabled="$DNS_METADATA_ENABLED" --token "$TOKEN" --name "$NAME" --type gateway \
  --config "$CONFIG_DIR/agent.json" --wal "$STATE_DIR/agent.wal" \
  --wal-max-bytes 16777216 --volatile-wal --snapshot-interval 2
chmod 0600 "$CONFIG_DIR/agent.json"

cat > /etc/init.d/network-observer <<EOF
#!/bin/sh /etc/rc.common
START=99
STOP=10
USE_PROCD=1

start_service() {
  procd_open_instance
  procd_set_param command $INSTALL_BIN run --config $CONFIG_DIR/agent.json
  procd_set_param env GODEBUG=
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
