#!/usr/bin/env bash
set -euo pipefail

VERSION="v0.36.6"
CONTROLLER_URL="https://github.com/bitnami-labs/sealed-secrets/releases/download/${VERSION}/controller.yaml"

if ! command -v kubectl >/dev/null 2>&1; then
  echo "kubectl is required to install the Sealed Secrets controller." >&2
  exit 1
fi

kubectl apply -f "$CONTROLLER_URL"

if kubectl -n kube-system get deploy/sealed-secrets-controller >/dev/null 2>&1; then
  kubectl -n kube-system rollout status deploy/sealed-secrets-controller --timeout=120s
else
  echo "sealed-secrets-controller deployment was not found in kube-system after apply." >&2
  echo "Check controller namespace/name with: kubectl get deploy -A | grep sealed" >&2
  exit 1
fi

kubectl get crd sealedsecrets.bitnami.com >/dev/null

echo "Sealed Secrets controller ${VERSION} installed and ready."
