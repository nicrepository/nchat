#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
CERT_DIR="$ROOT_DIR/infra/traefik/local/certs"
DOMAIN="${1:-${NCHAT_LOCAL_HOST:-nchat.local}}"
CERT_FILE="$CERT_DIR/${DOMAIN}.pem"
KEY_FILE="$CERT_DIR/${DOMAIN}-key.pem"

if [ ! -f "$CERT_FILE" ] || [ ! -f "$KEY_FILE" ]; then
  echo "Local TLS certificate or key is missing."
  echo "Run: make dev-tls-generate"
  exit 1
fi

printf '%s
' "Local TLS files present:"
printf '%s
' "- ${CERT_FILE#$ROOT_DIR/}"
printf '%s
' "- ${KEY_FILE#$ROOT_DIR/}"

if command -v openssl >/dev/null 2>&1; then
  echo ""
  openssl x509 -in "$CERT_FILE" -noout -subject -issuer -dates
  openssl x509 -in "$CERT_FILE" -noout -ext subjectAltName 2>/dev/null || true
else
  echo "openssl not found; skipped certificate metadata inspection."
fi
