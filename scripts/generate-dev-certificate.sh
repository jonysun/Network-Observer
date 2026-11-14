#!/usr/bin/env bash
set -euo pipefail

host="${1:-observer.example.lan}"
mkdir -p deploy/tls
openssl req -x509 -nodes -newkey rsa:3072 -sha256 -days 825 \
  -keyout deploy/tls/server.key \
  -out deploy/tls/server.crt \
  -subj "/CN=${host}" \
  -addext "subjectAltName=DNS:${host},DNS:localhost,IP:127.0.0.1"
chmod 600 deploy/tls/server.key
printf 'Created deploy/tls/server.crt for %s and localhost.\n' "$host"
