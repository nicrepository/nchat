#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
CERT_DIR="$ROOT_DIR/infra/traefik/local/certs"
FORCE=false
DOMAIN="${NCHAT_LOCAL_HOST:-nchat.local}"

if [[ "${1:-}" == --force ]]; then
  FORCE=true
  shift
fi
if [[ $# -gt 1 ]]; then
  echo "usage: dev-tls-generate.sh [--force] [hostname]" >&2
  exit 1
fi
[[ $# -eq 0 ]] || DOMAIN="$1"
if [[ ! "$DOMAIN" =~ ^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$ || "$DOMAIN" == *..* ]]; then
  echo "Invalid local TLS hostname." >&2
  exit 1
fi

CERT_FILE="$CERT_DIR/${DOMAIN}.pem"
KEY_FILE="$CERT_DIR/${DOMAIN}-key.pem"
umask 077
install -d -m 0700 "$CERT_DIR"

if [[ -e "$CERT_FILE" || -e "$KEY_FILE" ]]; then
  if ! $FORCE; then
    echo "Local TLS files already exist; use --force to replace them." >&2
    exit 1
  fi
  rm -f "$CERT_FILE" "$KEY_FILE"
fi

if command -v mkcert >/dev/null 2>&1; then
  echo "Generating trusted local certificate with mkcert for ${DOMAIN}."
  mkcert -install
  mkcert -cert-file "$CERT_FILE" -key-file "$KEY_FILE" \
    "$DOMAIN" nchat.local localhost 127.0.0.1 ::1
elif command -v openssl >/dev/null 2>&1; then
  echo "mkcert not found; generating a self-signed local certificate with openssl." >&2
  echo "Browsers will not trust this certificate automatically." >&2
  temporary="$(mktemp -d "${TMPDIR:-/tmp}/nchat-local-tls.XXXXXX")"
  trap 'rm -rf "$temporary"' EXIT
  config="$temporary/openssl.cnf"
  printf '%s\n' \
    '[req]' \
    'default_bits = 2048' \
    'prompt = no' \
    'default_md = sha256' \
    'distinguished_name = dn' \
    'x509_extensions = v3_req' \
    '' \
    '[dn]' \
    "CN = $DOMAIN" \
    '' \
    '[v3_req]' \
    'subjectAltName = @alt_names' \
    '' \
    '[alt_names]' \
    "DNS.1 = $DOMAIN" \
    'DNS.2 = nchat.local' \
    'DNS.3 = localhost' \
    'IP.1 = 127.0.0.1' \
    'IP.2 = ::1' >"$config"
  openssl req -x509 -nodes -days 397 -newkey rsa:2048 \
    -keyout "$KEY_FILE" -out "$CERT_FILE" -config "$config" >/dev/null 2>&1
else
  echo "Neither mkcert nor openssl was found." >&2
  exit 1
fi

chmod 0600 "$KEY_FILE"
chmod 0644 "$CERT_FILE"
echo "Local TLS certificate generated for ${DOMAIN}; private key contents were not printed."
