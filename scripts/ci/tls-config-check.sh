#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"

required_files=(
  "$ROOT_DIR/infra/compose/compose.dev.yml"
  "$ROOT_DIR/infra/compose/.env.dev.example"
  "$ROOT_DIR/infra/traefik/local/traefik.yml"
  "$ROOT_DIR/infra/traefik/local/dynamic.yml"
  "$ROOT_DIR/infra/traefik/local/certs/README.md"
  "$ROOT_DIR/scripts/ci/gateway-config-check.sh"
)

for file in "${required_files[@]}"; do
  if [ ! -f "$file" ]; then
    echo "Required TLS config file missing: ${file#$ROOT_DIR/}" >&2
    exit 1
  fi
done

if ! grep -q 'websecure:' "$ROOT_DIR/infra/traefik/local/traefik.yml"; then
  echo "Traefik local config must define the websecure entrypoint." >&2
  exit 1
fi

if ! grep -q 'VersionTLS13' "$ROOT_DIR/infra/traefik/local/dynamic.yml"; then
  echo "Traefik local dynamic config must require VersionTLS13." >&2
  exit 1
fi

if git -C "$ROOT_DIR" ls-files 'infra/traefik/local/certs/*' | grep -E '\.(pem|key|crt|p12|pfx)$' >/dev/null; then
  echo "Local TLS certificate material must not be versioned." >&2
  git -C "$ROOT_DIR" ls-files 'infra/traefik/local/certs/*' | grep -E '\.(pem|key|crt|p12|pfx)$' >&2
  exit 1
fi

if ! grep -q 'TRAEFIK_HTTPS_HOST_PORT=8443' "$ROOT_DIR/infra/compose/.env.dev.example"; then
  echo "infra/compose/.env.dev.example must define TRAEFIK_HTTPS_HOST_PORT=8443." >&2
  exit 1
fi

"$ROOT_DIR/scripts/ci/gateway-config-check.sh"

echo "TLS config check passed."
