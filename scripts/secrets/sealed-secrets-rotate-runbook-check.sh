#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"

required_files=(
  "$ROOT_DIR/docs/runbooks/sealed-secrets-rotation.md"
  "$ROOT_DIR/docs/security/secrets-owners.md"
  "$ROOT_DIR/infra/k8s/security/sealed-secrets/policy/sealed-secrets-policy.md"
  "$ROOT_DIR/infra/k8s/secrets/templates/nchat-secrets.template.yaml"
  "$ROOT_DIR/infra/k8s/secrets/templates/nchat-staging-tls.template.yaml"
)

for file in "${required_files[@]}"; do
  if [ ! -f "$file" ]; then
    echo "Required Sealed Secrets policy file missing: ${file#$ROOT_DIR/}" >&2
    exit 1
  fi
done

while IFS= read -r tracked; do
  case "$tracked" in
    infra/k8s/secrets/unsealed/.gitkeep|infra/k8s/secrets/unsealed/README.md) ;;
    '') ;;
    *)
      echo "Unsealed secret file is versioned: $tracked" >&2
      exit 1
      ;;
  esac
done < <(git -C "$ROOT_DIR" ls-files infra/k8s/secrets/unsealed)

if grep -R -E 'POSTGRES_[A-Z0-9_]*PASSWORD:[[:space:]]*"?[^"[:space:]]+' "$ROOT_DIR/infra/k8s/secrets/templates" | grep -v 'REPLACE_ME' >/dev/null; then
  echo "Secret templates must not contain real PostgreSQL password values." >&2
  exit 1
fi

if grep -R 'tls.key:' "$ROOT_DIR/infra/k8s/secrets/templates" | grep -v 'REPLACE_WITH_PRIVATE_KEY_PEM' >/dev/null; then
  echo "TLS Secret templates must not contain real private keys." >&2
  exit 1
fi

if grep -R 'tls.crt:' "$ROOT_DIR/infra/k8s/secrets/templates" | grep -v 'REPLACE_WITH_CERTIFICATE_PEM' >/dev/null; then
  echo "TLS Secret templates must not contain real certificates." >&2
  exit 1
fi

echo "Sealed Secrets policy check passed."
