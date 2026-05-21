#!/usr/bin/env bash
set -euo pipefail

require_command() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "$1 is required." >&2
    exit 1
  fi
}

require_command kubectl
require_command kubeseal

kubeseal --version
kubectl get crd sealedsecrets.bitnami.com >/dev/null

if kubectl -n kube-system get deploy/sealed-secrets-controller >/dev/null 2>&1; then
  kubectl -n kube-system rollout status deploy/sealed-secrets-controller --timeout=30s
else
  echo "sealed-secrets-controller deployment was not found in kube-system." >&2
  echo "Check controller location with: kubectl get deploy -A | grep sealed" >&2
  exit 1
fi

echo "Sealed Secrets validation passed."
