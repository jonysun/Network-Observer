#!/usr/bin/env bash
set -Eeuo pipefail

# Network Observer Agent installer. It is intentionally usable both from the
# Center's copied one-line command and interactively on a Linux terminal.
REPO="${NETWORK_OBSERVER_REPO:-jonysun/Network-Observer}"
VERSION="${NETWORK_OBSERVER_VERSION:-v0.3.9}"
MODE=""
SERVER=""
TOKEN=""
NAME=""
TYPE="endpoint"
SECURITY_MODE="true"
CA_URL=""
CA_FILE=""
FINGERPRINT=""
CONFIG_DIR="/etc/network-observer"
STATE_DIR="/var/lib/network-observer"
TLS_SERVER_NAME=""
tmp_dir=""
IMAGE="ghcr.io/jonysun/network-observer-agent:${VERSION}"

cleanup_tmp() {
  if [[ -n "${tmp_dir:-}" ]]; then
    rm -rf -- "$tmp_dir" || true
  fi
}
trap cleanup_tmp EXIT

usage() {
  cat <<'EOF'
用法：install-agent.sh [选项]
  --mode native|docker-compose|docker-run
  --version vX.Y.Z --server HOST:PORT --token TOKEN --name NAME
  --type endpoint|gateway --security-mode true|false
  --ca-url URL      TLS 模式下载 Center CA
  --ca FILE         TLS 模式使用本地 CA 文件
  --tls-server-name NAME  TLS 证书名称（当 Center 地址是 IP 时使用）
EOF
}

while (($#)); do
  case "$1" in
    --mode) MODE="$2"; shift 2;;
    --version) VERSION="$2"; IMAGE="ghcr.io/jonysun/network-observer-agent:${VERSION}"; shift 2;;
    --server) SERVER="$2"; shift 2;;
    --token) TOKEN="$2"; shift 2;;
    --name) NAME="$2"; shift 2;;
    --type) TYPE="$2"; shift 2;;
    --security-mode) SECURITY_MODE="$2"; shift 2;;
    --tls-server-name) TLS_SERVER_NAME="$2"; shift 2;;
    --ca-url) CA_URL="$2"; shift 2;;
    --ca) CA_FILE="$2"; shift 2;;
    --fingerprint) FINGERPRINT="${2//:/}"; shift 2;;
    --config-dir) CONFIG_DIR="$2"; shift 2;;
    --state-dir) STATE_DIR="$2"; shift 2;;
    -h|--help) usage; exit 0;;
    *) echo "未知参数：$1" >&2; usage >&2; exit 2;;
  esac
done

if [[ ! -t 0 && -z "$MODE" ]]; then
  echo "非交互调用必须提供 --mode" >&2; exit 2
fi
if [[ -z "$MODE" ]]; then
  echo "选择 Agent 运行方式："
  select choice in native docker-compose docker-run; do MODE="$choice"; break; done
fi
if [[ -z "$SERVER" ]]; then read -r -p "Center gRPC 地址（HOST:PORT）：" SERVER; fi
if [[ -z "$TOKEN" ]]; then read -r -s -p "一次性 Token：" TOKEN; echo; fi
if [[ -z "$NAME" ]]; then read -r -p "设备名称：" NAME; fi
if [[ "$TYPE" != endpoint && "$TYPE" != gateway ]]; then echo "--type 必须为 endpoint 或 gateway" >&2; exit 2; fi
if [[ "$SECURITY_MODE" != true && "$SECURITY_MODE" != false ]]; then echo "--security-mode 必须为 true 或 false" >&2; exit 2; fi
if [[ "$SECURITY_MODE" == true && -z "$CA_URL$CA_FILE" ]]; then echo "TLS 模式必须提供 --ca-url 或 --ca" >&2; exit 2; fi
if [[ "$SECURITY_MODE" == true && -n "$CA_URL" && -z "$FINGERPRINT" ]]; then echo "从 Center 下载 TLS 证书时必须提供 --fingerprint" >&2; exit 2; fi

if [[ $EUID -ne 0 && "$MODE" != docker-run && "$MODE" != docker-compose ]]; then
  echo "原生安装需要 root，请使用 sudo。" >&2; exit 1
fi

download_ca() {
  [[ "$SECURITY_MODE" == true ]] || return 0
  mkdir -p "$CONFIG_DIR"
  if [[ -n "$CA_FILE" ]]; then
    install -m 0644 "$CA_FILE" "$CONFIG_DIR/ca.crt"
  else
    # Certificate pinning bootstraps trust for self-signed LAN certificates.
    # The expected digest is displayed by the authenticated Center console.
    curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 --insecure "$CA_URL" -o "$CONFIG_DIR/ca.crt"
  fi
  openssl x509 -in "$CONFIG_DIR/ca.crt" -noout >/dev/null
  if [[ -n "$FINGERPRINT" ]]; then
    local actual
    actual="$(openssl x509 -in "$CONFIG_DIR/ca.crt" -noout -fingerprint -sha256 | cut -d= -f2 | tr -d ':\r\n' | tr '[:lower:]' '[:upper:]')"
    if [[ "$actual" != "${FINGERPRINT^^}" ]]; then echo "Center 证书指纹不匹配，已停止安装。" >&2; exit 1; fi
  fi
}

write_config() {
  local path="$1"
  umask 077
  mkdir -p "$(dirname "$path")" "$STATE_DIR"
  : # The Agent's register command writes the final credential-only config.
}

install_native() {
  local base="https://github.com/${REPO}/releases/download/${VERSION}"
  tmp_dir="$(mktemp -d)"
  if [[ -e "$CONFIG_DIR/agent.json" ]]; then echo "已有配置，拒绝覆盖：$CONFIG_DIR/agent.json" >&2; exit 1; fi
  mkdir -p "$CONFIG_DIR" "$STATE_DIR"
  download_ca
  local arch; case "$(uname -m)" in x86_64|amd64) arch=amd64;; aarch64|arm64) arch=arm64;; *) echo "不支持的架构：$(uname -m)" >&2; exit 1;; esac
  local binary_name="network-observer-agent-linux-${arch}"
  curl --fail --silent --show-error --location "$base/${binary_name}" -o "$tmp_dir/${binary_name}"
  curl --fail --silent --show-error --location "$base/SHA256SUMS" -o "$tmp_dir/SHA256SUMS"
  (cd "$tmp_dir" && grep "${binary_name}" SHA256SUMS | sha256sum -c -)
  install -m 0755 "$tmp_dir/${binary_name}" /usr/local/bin/network-observer-agent
  umask 077; mkdir -p "$STATE_DIR"; chmod 0700 "$CONFIG_DIR" "$STATE_DIR"
  /usr/local/bin/network-observer-agent register --server "$SERVER" --tls-server-name "$TLS_SERVER_NAME" --ca "$([[ "$SECURITY_MODE" == true ]] && echo "$CONFIG_DIR/ca.crt" || true)" --security-mode="$SECURITY_MODE" --token "$TOKEN" --name "$NAME" --type "$TYPE" --config "$CONFIG_DIR/agent.json" --wal "$STATE_DIR/agent.wal"
  cat > /etc/systemd/system/network-observer-agent.service <<EOF
[Unit]
Description=Network Observer Agent
After=network-online.target
Wants=network-online.target
[Service]
ExecStart=/usr/local/bin/network-observer-agent run --config ${CONFIG_DIR}/agent.json
Restart=on-failure
RestartSec=5s
User=root
NoNewPrivileges=true
ReadWritePaths=${STATE_DIR}
ReadOnlyPaths=/proc /sys
[Install]
WantedBy=multi-user.target
EOF
  systemctl daemon-reload
  systemctl enable --now network-observer-agent
  echo "原生 Agent 已安装，配置：$CONFIG_DIR/agent.json"
}

install_docker() {
  mkdir -p "$CONFIG_DIR" "$STATE_DIR"; chmod 0700 "$CONFIG_DIR" "$STATE_DIR"
  download_ca
  if [[ "$MODE" == docker-compose ]]; then
    if [[ -e "$CONFIG_DIR/docker-compose.yml" ]]; then echo "已有配置，拒绝覆盖：$CONFIG_DIR/docker-compose.yml" >&2; exit 1; fi
    cat > "$CONFIG_DIR/docker-compose.yml" <<EOF
services:
  agent:
    image: ${IMAGE}
    restart: unless-stopped
    command: ["run", "--config", "/etc/network-observer/agent.json"]
    network_mode: host
    pid: host
    privileged: true
    volumes:
      - ${CONFIG_DIR}:/etc/network-observer:ro
      - ${STATE_DIR}:/var/lib/network-observer
      - /sys:/sys:ro
EOF
    docker run --rm --network host -v "$CONFIG_DIR:/etc/network-observer" -v "$STATE_DIR:/var/lib/network-observer" -v /sys:/sys:ro "$IMAGE" register \
      --server "$SERVER" --tls-server-name "$TLS_SERVER_NAME" --ca "$([[ "$SECURITY_MODE" == true ]] && echo /etc/network-observer/ca.crt || true)" --security-mode="$SECURITY_MODE" \
      --token "$TOKEN" --name "$NAME" --type "$TYPE" --config /etc/network-observer/agent.json --wal /var/lib/network-observer/agent.wal
    docker compose -f "$CONFIG_DIR/docker-compose.yml" up -d
  else
    docker run --rm --network host -v "$CONFIG_DIR:/etc/network-observer" -v "$STATE_DIR:/var/lib/network-observer" -v /sys:/sys:ro "$IMAGE" register \
      --server "$SERVER" --tls-server-name "$TLS_SERVER_NAME" --ca "$([[ "$SECURITY_MODE" == true ]] && echo /etc/network-observer/ca.crt || true)" --security-mode="$SECURITY_MODE" \
      --token "$TOKEN" --name "$NAME" --type "$TYPE" --config /etc/network-observer/agent.json --wal /var/lib/network-observer/agent.wal
    docker run -d --name network-observer-agent --restart unless-stopped --network host --pid host --privileged \
      -v "$CONFIG_DIR:/etc/network-observer:ro" -v "$STATE_DIR:/var/lib/network-observer" -v /sys:/sys:ro \
      "$IMAGE" run --config /etc/network-observer/agent.json
  fi
  echo "Docker Agent 已启动，配置：$CONFIG_DIR/agent.json"
}

case "$MODE" in
  native) install_native;;
  docker-compose|docker-run) install_docker;;
  *) echo "--mode 必须为 native、docker-compose 或 docker-run" >&2; exit 2;;
esac
