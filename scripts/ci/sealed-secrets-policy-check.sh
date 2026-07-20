#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"

"$ROOT_DIR/scripts/secrets/sealed-secrets-rotate-runbook-check.sh"

while IFS= read -r file; do
  case "$file" in
    infra/k8s/secrets/unsealed/*.yaml|infra/k8s/secrets/unsealed/*.yml)
      echo "Plaintext unsealed secret manifest is versioned: $file" >&2
      exit 1
      ;;
  esac

  base="$(basename "$file")"
  case "$base" in
    *secret*.yaml|*secrets*.yaml|*secret*.yml|*secrets*.yml)
      case "$file" in
        infra/k8s/secrets/templates/*.template.yaml|infra/k8s/secrets/sealed/*.yaml|infra/k8s/base/secrets.example.yaml)
          ;;
        *)
          echo "Potential plaintext secret YAML outside approved locations: $file" >&2
          exit 1
          ;;
      esac
      ;;
  esac
done < <(git -C "$ROOT_DIR" ls-files '*.yaml' '*.yml')

if grep -q 'secrets.example.yaml' "$ROOT_DIR/infra/k8s/base/kustomization.yaml"; then
  echo "infra/k8s/base/secrets.example.yaml must not be included in kustomization." >&2
  exit 1
fi

if grep -R -En 'kubectl[[:space:]]+apply[[:space:]]+-f[[:space:]]+https?://' "$ROOT_DIR/scripts"; then
  echo "Bootstrap scripts must not apply Kubernetes manifests directly from remote URLs." >&2
  exit 1
fi

expected_controller_sha="$(<"$ROOT_DIR/infra/k8s/security/sealed-secrets/CONTROLLER_SHA256")"
read -r actual_controller_sha _ < <(sha256sum "$ROOT_DIR/infra/k8s/security/sealed-secrets/controller/controller.yaml")
if [[ ! "$expected_controller_sha" =~ ^[a-f0-9]{64}$ || "$actual_controller_sha" != "$expected_controller_sha" ]]; then
  echo "Vendored Sealed Secrets controller manifest checksum mismatch." >&2
  exit 1
fi

sensitive_marker="$(printf 'BEGIN PRIVATE %s' 'KEY')"
tls_block_marker="$(printf 'tls.key: %s' '|')"

while IFS= read -r file; do
  case "$file" in
    docs/*|infra/k8s/secrets/templates/*|infra/k8s/security/sealed-secrets/policy/sealed-secrets-policy.md)
      continue
      ;;
  esac
  if grep -q "$sensitive_marker" "$ROOT_DIR/$file"; then
    echo "Private key material marker found in versioned file: $file" >&2
    exit 1
  fi
  if grep -q "$tls_block_marker" "$ROOT_DIR/$file"; then
    echo "Inline TLS private key block found in versioned file: $file" >&2
    exit 1
  fi
done < <(git -C "$ROOT_DIR" ls-files)

while IFS= read -r file; do
  case "$file" in
    infra/k8s/secrets/templates/*.template.yaml|infra/k8s/base/secrets.example.yaml)
      if grep -E 'POSTGRES_[A-Z0-9_]*PASSWORD:[[:space:]]*"?[^" ]+' "$ROOT_DIR/$file" | grep -Ev 'REPLACE_ME|replace-me' >/dev/null; then
        echo "PostgreSQL passwords in $file must remain placeholders." >&2
        exit 1
      fi
      ;;
    infra/k8s/secrets/sealed/*.yaml|infra/k8s/secrets/sealed/*.yml)
      # SealedSecret encryptedData preserves the original Secret key names,
      # while the corresponding values are ciphertext. These manifests are
      # validated separately as SealedSecret resources.
      ;;
    *.yaml|*.yml)
      if grep -E 'POSTGRES_[A-Z0-9_]*PASSWORD:[[:space:]]*"?[^" ]+' "$ROOT_DIR/$file" | grep -Ev 'REPLACE_ME|replace-me|\$\{[A-Z0-9_]+\}' >/dev/null; then
        echo "A PostgreSQL password appears in non-template YAML with a non-placeholder value: $file" >&2
        exit 1
      fi
      ;;
  esac
done < <(git -C "$ROOT_DIR" ls-files '*.yaml' '*.yml')

echo "Sealed Secrets CI policy check passed."
