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

docker compose --env-file "$env_file" "${compose_files[@]}" up -d
docker compose --env-file "$env_file" "${compose_files[@]}" ps
