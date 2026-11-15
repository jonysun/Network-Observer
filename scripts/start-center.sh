#!/usr/bin/env bash
set -Eeuo pipefail

mode="${1:-light}"
env_file="${ENV_FILE:-.env}"

if [[ ! -f "$env_file" ]]; then
  echo "缺少 $env_file。先复制 .env.example 为 .env 并填写密码。" >&2
  exit 1
fi

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

cert_host="$(sed -n 's/^TLS_CERT_HOST=//p' "$env_file" | tail -n 1)"
cert_host="${cert_host:-observer.example.lan}"
cert_file="deploy/tls/server.crt"
key_file="deploy/tls/server.key"

if [[ -s "$cert_file" && -s "$key_file" ]]; then
  echo "使用已有 TLS 证书：$cert_file"
elif [[ -e "$cert_file" || -e "$key_file" ]]; then
  echo "TLS 证书文件不完整，请同时提供 $cert_file 和 $key_file，或删除残留文件。" >&2
  exit 1
else
  command -v openssl >/dev/null || { echo "未找到 openssl，请先安装 openssl。" >&2; exit 1; }
  bash "$(dirname "$0")/generate-dev-certificate.sh" "$cert_host"
fi

docker compose --env-file "$env_file" "${compose_files[@]}" up -d
docker compose --env-file "$env_file" "${compose_files[@]}" ps
