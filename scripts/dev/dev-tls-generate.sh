#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
CERT_DIR="$ROOT_DIR/infra/traefik/local/certs"
DOMAIN="${1:-${NCHAT_LOCAL_HOST:-nchat.local}}"
CERT_FILE="$CERT_DIR/${DOMAIN}.pem"
KEY_FILE="$CERT_DIR/${DOMAIN}-key.pem"

mkdir -p "$CERT_DIR"

if command -v mkcert >/dev/null 2>&1; then
  echo "Generating trusted local certificate with mkcert for ${DOMAIN}."
  mkcert -install
  mkcert     -cert-file "$CERT_FILE"     -key-file "$KEY_FILE"     "$DOMAIN" localhost 127.0.0.1 ::1
elif command -v openssl >/dev/null 2>&1; then
  echo "mkcert not found; generating self-signed local certificate with openssl." >&2
  echo "Browsers will not trust this certificate automatically." >&2
  OPENSSL_CONFIG="$(mktemp)"
  cleanup() {
    rm -f "$OPENSSL_CONFIG"
  }
  trap cleanup EXIT
  cat >"$OPENSSL_CONFIG" <<EOF
[req]
default_bits = 2048
prompt = no
default_md = sha256
distinguished_name = dn
x509_extensions = v3_req

[dn]
CN = ${DOMAIN}

[v3_req]
subjectAltName = @alt_names

[alt_names]
DNS.1 = ${DOMAIN}
DNS.2 = localhost
IP.1 = 127.0.0.1
IP.2 = ::1
EOF
  openssl req -x509 -nodes -days 397 -newkey rsa:2048     -keyout "$KEY_FILE"     -out "$CERT_FILE"     -config "$OPENSSL_CONFIG" >/dev/null 2>&1
else
  echo "Neither mkcert nor openssl was found. Install mkcert or openssl and rerun this script." >&2
  exit 1
fi

chmod 0600 "$KEY_FILE"
chmod 0644 "$CERT_FILE"

printf '%s
' "Local TLS files generated:"
printf '%s
' "- ${CERT_FILE#$ROOT_DIR/}"
printf '%s
' "- ${KEY_FILE#$ROOT_DIR/}"
printf '%s
' ""
printf '%s
' "Next steps:"
printf '%s
' "- make dev-gateway-up"
printf '%s
' "- open https://${DOMAIN}:8443"
