#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
CERT_DIR="$ROOT_DIR/infra/traefik/local/certs"
DOMAIN="${1:-${NCHAT_LOCAL_HOST:-nchat.local}}"

read -r -p "Type DELETE_TLS to remove local TLS certs: " confirmation
if [ "$confirmation" != "DELETE_TLS" ]; then
  echo "Aborted."
  exit 1
fi

rm -f   "$CERT_DIR/${DOMAIN}.pem"   "$CERT_DIR/${DOMAIN}-key.pem"   "$CERT_DIR/${DOMAIN}.crt"   "$CERT_DIR/${DOMAIN}.key"

echo "Removed generated local TLS files for ${DOMAIN}."
