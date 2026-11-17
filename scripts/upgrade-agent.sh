#!/usr/bin/env bash
set -Eeuo pipefail

# Upgrade an existing native Linux Agent without re-registering the device.
REPO="${NETWORK_OBSERVER_REPO:-jonysun/Network-Observer}"
VERSION="${NETWORK_OBSERVER_VERSION:-v0.3.28}"
BINARY_PATH="${NETWORK_OBSERVER_BINARY:-/usr/local/bin/network-observer-agent}"
CONFIG_DIR="${NETWORK_OBSERVER_CONFIG_DIR:-/etc/network-observer}"
BPF_OBJECT_DIR="${NETWORK_OBSERVER_BPF_DIR:-/usr/local/lib/network-observer}"
SERVICE_NAME="${NETWORK_OBSERVER_SERVICE:-network-observer-agent}"
tmp_dir=""
was_active=0
new_binary=""
new_bpf=""

cleanup() {
  if [[ -n "${new_binary:-}" && -e "$new_binary" ]]; then
    rm -f -- "$new_binary" || true
  fi
  if [[ -n "${new_bpf:-}" && -e "$new_bpf" ]]; then
    rm -f -- "$new_bpf" || true
  fi
  if [[ -n "${tmp_dir:-}" ]]; then
    rm -rf -- "$tmp_dir" || true
  fi
}
trap cleanup EXIT

usage() {
  cat <<'EOF'
用法：upgrade-agent.sh [选项]
  --version vX.Y.Z       目标发布版本，默认 v0.3.28
  --repo OWNER/REPO      GitHub 仓库，默认 jonysun/Network-Observer
  --binary PATH          Agent 二进制路径，默认 /usr/local/bin/network-observer-agent
  --config-dir DIR       Agent 配置目录，默认 /etc/network-observer
  --service NAME         systemd 服务名，默认 network-observer-agent

升级会保留 agent.json、ca.crt 和 agent.wal，不会重新注册或要求 Token。
EOF
}

while (($#)); do
  case "$1" in
    --version) VERSION="$2"; shift 2;;
    --repo) REPO="$2"; shift 2;;
    --binary) BINARY_PATH="$2"; shift 2;;
    --config-dir) CONFIG_DIR="$2"; shift 2;;
    --service) SERVICE_NAME="$2"; shift 2;;
    -h|--help) usage; exit 0;;
    *) echo "未知参数：$1" >&2; usage >&2; exit 2;;
  esac
done

if [[ $EUID -ne 0 ]]; then
  echo "升级原生 Agent 需要 root，请使用 sudo bash。" >&2
  exit 1
fi
if [[ ! "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "版本必须是 vX.Y.Z：$VERSION" >&2
  exit 2
fi
if [[ ! -f "$CONFIG_DIR/agent.json" ]]; then
  echo "未找到现有 Agent 配置：$CONFIG_DIR/agent.json" >&2
  echo "请先完成原生安装，或检查 NETWORK_OBSERVER_CONFIG_DIR。" >&2
  exit 1
fi
if [[ ! -x "$BINARY_PATH" ]]; then
  echo "未找到现有 Agent 二进制：$BINARY_PATH" >&2
  exit 1
fi
for command in curl sha256sum install mv cp awk systemctl; do
  command -v "$command" >/dev/null || { echo "缺少命令：$command" >&2; exit 1; }
done
if ! systemctl cat "$SERVICE_NAME" >/dev/null 2>&1; then
  echo "未找到 systemd 服务：$SERVICE_NAME.service" >&2
  exit 1
fi

# Existing units may have been created before the obsolete GODEBUG guard was
# added. A drop-in makes the fix effective before the upgraded binary starts.
mkdir -p "/etc/systemd/system/${SERVICE_NAME}.service.d"
cat > "/etc/systemd/system/${SERVICE_NAME}.service.d/10-runtime-compat.conf" <<'EOF'
[Service]
UnsetEnvironment=GODEBUG
EOF
systemctl daemon-reload

case "$(uname -m)" in
  x86_64|amd64) arch=amd64;;
  aarch64|arm64) arch=arm64;;
  *) echo "不支持的架构：$(uname -m)，仅支持 amd64 和 arm64。" >&2; exit 1;;
esac

binary_name="network-observer-agent-linux-${arch}"
bpf_name="network-observer-flow-counter-linux-${arch}.o"
base="https://github.com/${REPO}/releases/download/${VERSION}"
tmp_dir="$(mktemp -d)"

echo "下载 Agent ${VERSION}（${arch}）..."
curl --fail --silent --show-error --location --retry 5 --retry-all-errors --connect-timeout 10 --max-time 180 \
  "$base/${binary_name}" -o "$tmp_dir/${binary_name}"
if ! curl --fail --silent --show-error --location --retry 5 --retry-all-errors --connect-timeout 10 --max-time 180 \
  "$base/${bpf_name}" -o "$tmp_dir/${bpf_name}"; then
  echo "警告：${VERSION} 没有可用的原生 eBPF 对象，保留现有对象（如有）。" >&2
  bpf_name=""
fi
curl --fail --silent --show-error --location --retry 5 --retry-all-errors --connect-timeout 10 --max-time 180 \
  "$base/SHA256SUMS" -o "$tmp_dir/SHA256SUMS"

checksum_line="$(awk -v file="$binary_name" '$2 == file || $2 == "*" file { print; exit }' "$tmp_dir/SHA256SUMS")"
if [[ -z "$checksum_line" ]]; then
  echo "SHA256SUMS 中没有 ${binary_name}，已停止升级。" >&2
  exit 1
fi
printf '%s\n' "$checksum_line" | (cd "$tmp_dir" && sha256sum -c -)
if [[ -n "$bpf_name" ]]; then
  bpf_checksum_line="$(awk -v file="$bpf_name" '$2 == file || $2 == "*" file { print; exit }' "$tmp_dir/SHA256SUMS")"
  if [[ -z "$bpf_checksum_line" ]]; then
    echo "SHA256SUMS 中没有 ${bpf_name}，已停止升级。" >&2
    exit 1
  fi
  printf '%s\n' "$bpf_checksum_line" | (cd "$tmp_dir" && sha256sum -c -)
fi

if systemctl is-active --quiet "$SERVICE_NAME"; then
  was_active=1
  echo "停止 ${SERVICE_NAME}..."
  systemctl stop "$SERVICE_NAME"
fi

old_binary="$tmp_dir/old-agent"
if [[ -x "$BINARY_PATH" ]]; then
  cp -a -- "$BINARY_PATH" "$old_binary"
fi
old_bpf="$tmp_dir/old-flow-counter.o"
had_old_bpf=0
if [[ -f "$BPF_OBJECT_DIR/flow_counter.o" ]]; then
  cp -a -- "$BPF_OBJECT_DIR/flow_counter.o" "$old_bpf"
  had_old_bpf=1
fi
rollback() {
  if [[ -f "$old_binary" ]]; then
    install -m 0755 "$old_binary" "$BINARY_PATH"
  fi
  if [[ -f "$old_bpf" ]]; then
    install -d -m 0755 "$BPF_OBJECT_DIR"
    install -m 0644 "$old_bpf" "$BPF_OBJECT_DIR/flow_counter.o"
  elif (( ! had_old_bpf )); then
    rm -f -- "$BPF_OBJECT_DIR/flow_counter.o"
  fi
  if ((was_active)); then
    systemctl start "$SERVICE_NAME" || true
  fi
}

new_binary="${BINARY_PATH}.new.$$"
if ! install -m 0755 "$tmp_dir/${binary_name}" "$new_binary"; then
  echo "写入新二进制失败，正在恢复服务。" >&2
  rollback
  exit 1
fi
if ! mv -f -- "$new_binary" "$BINARY_PATH"; then
  echo "替换二进制失败，正在恢复旧版本。" >&2
  rollback
  exit 1
fi
if [[ -n "$bpf_name" ]]; then
  new_bpf="${BPF_OBJECT_DIR}/flow_counter.o.new.$$"
  if ! install -D -m 0644 "$tmp_dir/${bpf_name}" "$new_bpf"; then
    echo "写入 eBPF 对象失败，正在恢复服务。" >&2
    rollback
    exit 1
  fi
  if ! mv -f -- "$new_bpf" "$BPF_OBJECT_DIR/flow_counter.o"; then
    echo "替换 eBPF 对象失败，正在恢复服务。" >&2
    rollback
    exit 1
  fi
  new_bpf=""
fi

if ((was_active)); then
  echo "启动 ${SERVICE_NAME}..."
  if ! systemctl start "$SERVICE_NAME" || ! systemctl is-active --quiet "$SERVICE_NAME"; then
    echo "新版本服务未能启动，正在恢复旧二进制。" >&2
    rollback
    exit 1
  fi
else
  echo "服务当前未运行，已完成二进制替换；保持停止状态。"
fi

echo "原生 Agent 已升级到 ${VERSION}。"
echo "已保留：${CONFIG_DIR}/agent.json、${CONFIG_DIR}/ca.crt、WAL。"
