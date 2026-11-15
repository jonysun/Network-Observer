#!/usr/bin/env bash
set -Eeuo pipefail

mode="${1:-light}"
env_file="${ENV_FILE:-.env}"
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_dir="$(cd "$script_dir/.." && pwd)"
if [[ "$env_file" != /* ]]; then
  env_file="$repo_dir/$env_file"
fi
cd "$repo_dir"

random_secret() {
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -hex 24
    return
  fi
  od -An -N24 -tx1 /dev/urandom | tr -d ' \n'
}

detect_lan_host() {
  local candidate
  candidate="${TLS_CERT_HOST:-}"
  if [[ -n "$candidate" && "$candidate" != "observer.example.lan" ]]; then
    printf '%s\n' "$candidate"
    return
  fi
  if command -v ip >/dev/null 2>&1; then
    candidate="$(ip route get 1.1.1.1 2>/dev/null | awk '{for (i=1; i<=NF; i++) if ($i == "src") {print $(i+1); exit}}')"
    if [[ "$candidate" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ && "$candidate" != 127.* ]]; then
      printf '%s\n' "$candidate"
      return
    fi
  fi
  if command -v hostname >/dev/null 2>&1; then
    candidate="$(hostname -I 2>/dev/null | awk '{for (i=1; i<=NF; i++) if ($i !~ /^127\./ && $i !~ /:/) {print $i; exit}}')"
    if [[ "$candidate" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]]; then
      printf '%s\n' "$candidate"
      return
    fi
  fi
  printf '%s\n' "observer.example.lan"
}

ensure_env_file() {
  if [[ -f "$env_file" ]]; then
    return
  fi
  mkdir -p "$(dirname "$env_file")"
  local tls_host
  tls_host="$(detect_lan_host)"
  cat > "$env_file" <<EOF
POSTGRES_PASSWORD=$(random_secret)
CLICKHOUSE_PASSWORD=$(random_secret)
ADMIN_USERNAME=admin
ADMIN_PASSWORD=$(random_secret)
TLS_CERT_HOST=${tls_host}
WEB_PORT=8443
AGENT_GRPC_PORT=8444
GRAFANA_PORT=3000
SUMMARY_RETENTION_DAYS=90
GRAFANA_ADMIN_USER=admin
GRAFANA_ADMIN_PASSWORD=$(random_secret)
EOF
  chmod 600 "$env_file"
  echo "已自动创建 $env_file，管理员账号为 admin，密码已写入该文件。"
  echo "TLS_CERT_HOST=$tls_host；Center 首次启动会自动生成证书和 GeoIP 数据库。"
}

ensure_env_file

case "$mode" in
  light)
    compose_files=(-f examples/center/light/docker-compose.yml)
    ;;
  pro)
    compose_files=(-f examples/center/light/docker-compose.yml -f examples/center/pro/docker-compose.yml)
    ;;
  *)
    echo "用法：$0 [light|pro]" >&2
    exit 2
    ;;
esac

pull_retries="${NETWORK_OBSERVER_PULL_RETRIES:-5}"
pull_retry_delay="${NETWORK_OBSERVER_PULL_RETRY_DELAY:-8}"
if ! [[ "$pull_retries" =~ ^[1-9][0-9]*$ && "$pull_retry_delay" =~ ^[0-9]+$ ]]; then
  echo "NETWORK_OBSERVER_PULL_RETRIES 必须是正整数，NETWORK_OBSERVER_PULL_RETRY_DELAY 必须是非负整数。" >&2
  exit 2
fi

pull_images() {
  local attempt
  for ((attempt = 1; attempt <= pull_retries; attempt++)); do
    echo "正在拉取 Center 镜像（第 ${attempt}/${pull_retries} 次）……"
    if docker compose --env-file "$env_file" "${compose_files[@]}" pull; then
      return 0
    fi
    if ((attempt < pull_retries)); then
      echo "镜像拉取失败，${pull_retry_delay} 秒后自动重试；常见原因是 NAS 到镜像仓库的 TLS/DNS 临时超时。" >&2
      sleep "$pull_retry_delay"
    fi
  done
  echo "镜像连续拉取失败。请检查 NAS 是否能访问 ghcr.io/token（DNS、IPv6、代理和防火墙），然后再次运行本脚本。" >&2
  return 1
}

pull_images
recreate=0
existing_observer="$(docker compose --env-file "$env_file" "${compose_files[@]}" ps -q observer 2>/dev/null || true)"
if [[ -n "$existing_observer" ]]; then
  tls_mount_rw="$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/run/tls"}}{{.RW}}{{end}}{{end}}' "$existing_observer" 2>/dev/null || true)"
  if [[ "$tls_mount_rw" != "true" ]]; then
    echo "检测到旧 Center 容器的 /run/tls 不是可写挂载，正在自动重建容器……"
    recreate=1
  fi
fi
if ((recreate)); then
  docker compose --env-file "$env_file" "${compose_files[@]}" up -d --no-build --force-recreate
else
  docker compose --env-file "$env_file" "${compose_files[@]}" up -d --no-build
fi
docker compose --env-file "$env_file" "${compose_files[@]}" ps
