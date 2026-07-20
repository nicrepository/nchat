#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
CONTROLLER_DIR="$ROOT_DIR/infra/k8s/security/sealed-secrets/controller"
VERSION_FILE="$ROOT_DIR/infra/k8s/security/sealed-secrets/VERSION"
CHECKSUM_FILE="$ROOT_DIR/infra/k8s/security/sealed-secrets/CONTROLLER_SHA256"
VERSION="$(<"$VERSION_FILE")"
EXPECTED_SHA256="$(<"$CHECKSUM_FILE")"

if ! command -v kubectl >/dev/null 2>&1; then
  echo "kubectl is required to install the Sealed Secrets controller." >&2
  exit 1
fi

read -r ACTUAL_SHA256 _ < <(sha256sum "$CONTROLLER_DIR/controller.yaml")
if [[ ! "$EXPECTED_SHA256" =~ ^[a-f0-9]{64}$ || "$ACTUAL_SHA256" != "$EXPECTED_SHA256" ]]; then
  echo "Vendored Sealed Secrets controller manifest failed integrity validation." >&2
  exit 1
fi

kubectl apply -k "$CONTROLLER_DIR"

if kubectl -n kube-system get deploy/sealed-secrets-controller >/dev/null 2>&1; then
  kubectl -n kube-system rollout status deploy/sealed-secrets-controller --timeout=120s
else
  echo "sealed-secrets-controller deployment was not found in kube-system after apply." >&2
  echo "Check controller namespace/name with: kubectl get deploy -A | grep sealed" >&2
  exit 1
fi

kubectl get crd sealedsecrets.bitnami.com >/dev/null

echo "Sealed Secrets controller ${VERSION} installed from the verified vendored manifest."
