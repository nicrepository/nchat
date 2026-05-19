#!/usr/bin/env bash
set -euo pipefail

if ! command -v kubectl >/dev/null 2>&1; then
  echo "error: kubectl is required to inspect k3s-dev status" >&2
  exit 1
fi

kubectl get all -n nchat
kubectl get ingress -n nchat
kubectl get networkpolicy -n nchat
