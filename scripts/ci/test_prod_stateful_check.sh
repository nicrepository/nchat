#!/usr/bin/env bash
# Negative tests for the production stateful gate.
#
# A gate is only worth its runtime if it fails on the thing it claims to catch.
# Each case below copies the real overlay, makes one specific mistake in it, and
# requires the check to reject the result -- so the assertions are exercised
# against manifests kustomize actually renders, not against a stubbed reader
# that could agree with a broken rule.
set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
CHECK="$ROOT_DIR/scripts/ci/prod-stateful-check.sh"
SOURCE_OVERLAY="$ROOT_DIR/infra/k8s/overlays/k3s-prod/stateful"
WORK_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/nchat-prod-stateful-test.XXXXXX")"
trap 'rm -rf "$WORK_ROOT"' EXIT

FAILURES=0
CASE=0

# One doctored copy of the overlay, and the gate's verdict on it.
#
#   doctor <name> <accept|reject> <file> <sed script>
#
# The sed script is the whole mistake, so a reader can see what each case is
# actually testing without leaving this file.
doctor() {
  local name="$1" wanted="$2" file="$3" script="$4" overlay status
  CASE=$((CASE + 1))
  overlay="$WORK_ROOT/case-$CASE"
  cp -a "$SOURCE_OVERLAY" "$overlay"
  [[ -z "$script" ]] || sed -i "$script" "$overlay/$file"
  status=0
  NCHAT_PROD_STATEFUL_OVERLAY="$overlay" bash "$CHECK" >/dev/null 2>&1 || status=$?
  report "$name" "$wanted" "$status"
}

report() {
  local name="$1" wanted="$2" status="$3"
  if [[ "$wanted" == reject && "$status" -eq 0 ]]; then
    echo "  [FAIL] $name: the gate accepted it" >&2
    FAILURES=$((FAILURES + 1))
    return
  fi
  if [[ "$wanted" == accept && "$status" -ne 0 ]]; then
    echo "  [FAIL] $name: the gate rejected valid input (exit $status)" >&2
    FAILURES=$((FAILURES + 1))
    return
  fi
  echo "  [OK]   $name"
}

# Wires a file written into a doctored overlay into its Kustomization, so a
# mistake that is an added resource -- rather than an edited line -- can be
# tested the same way as the rest.
add_resource() {
  sed -i "s/^  - data.yaml$/  - data.yaml\n  - $2/" "$1/kustomization.yaml"
}

# The mistakes that cannot be expressed as an edit to an existing line: a real
# Secret with a real value, and a media plane provisioned back into the layer.
secret_case() {
  local name="$1" overlay status
  CASE=$((CASE + 1))
  overlay="$WORK_ROOT/case-$CASE"
  cp -a "$SOURCE_OVERLAY" "$overlay"
  cat >"$overlay/committed-secret.yaml" <<'YAML'
apiVersion: v1
kind: Secret
metadata:
  name: nchat-secrets
type: Opaque
stringData:
  VALKEY_PASSWORD: not-a-real-password
YAML
  add_resource "$overlay" committed-secret.yaml
  status=0
  NCHAT_PROD_STATEFUL_OVERLAY="$overlay" bash "$CHECK" >/dev/null 2>&1 || status=$?
  report "$name" reject "$status"
}

echo "=== production stateful gate: negative cases ==="
echo
echo "--- baseline ---"
doctor "the committed overlay is accepted" accept storage.yaml ''

echo
echo "--- storage lifecycle ---"
doctor "a volume that would be garbage collected is rejected" reject storage.yaml \
  's/persistentVolumeReclaimPolicy: Retain/persistentVolumeReclaimPolicy: Delete/'
doctor "a volume pointed at a development path is rejected" reject storage.yaml \
  's#/mnt/hdd-geral/k3s/nchat-prod/postgres#/mnt/hdd-geral/k3s/nchat-dev/postgres#'
doctor "a volume on the wrong storage class is rejected" reject storage.yaml \
  's/storageClassName: local-hdd-geral/storageClassName: local-path/'
doctor "a volume that could bind on another node is rejected" reject storage.yaml \
  's/- srv-apps-01/- srv-other-99/'
doctor "a volume whose path drifted from the documented one is rejected" reject storage.yaml \
  's#k3s/nchat-prod/valkey#k3s/nchat-prod/valkey-2#'

echo
echo "--- one instance of each ---"
doctor "a second PostgreSQL is rejected" reject data.yaml \
  '/^  name: postgres$/a --- \napiVersion: apps/v1\nkind: StatefulSet\nmetadata:\n  name: postgres'
doctor "removing the filer Service production addresses by name is rejected" reject data.yaml \
  's/^  name: seaweedfs-filer$/  name: seaweedfs-extra/'

echo
echo "--- slots and namespace ---"
doctor "a dependency labelled for a release slot is rejected" reject kustomization.yaml \
  's#^      app.kubernetes.io/instance: nchat-prod$#      app.kubernetes.io/instance: nchat-prod\n      nchat.io/release-slot: blue#'
doctor "rendering into the development namespace is rejected" reject kustomization.yaml \
  's/^namespace: nchat-prod$/namespace: nchat-dev/'

echo
echo "--- images ---"
doctor "an unpinned stateful image is rejected" reject kustomization.yaml \
  '/digest: sha256:029660641a0cfc575b14f336ba448fb8a75fd595d42e1fa316b9fb4378742297/d'

echo
echo "--- the media plane is external ---"
# LiveKit runs as one shared deployment on AWS that nchat-dev already uses and
# both production slots use too. Provisioning a local media server here would
# take srv-apps-01's host ports, and the first symptom would be nchat-dev's
# calls failing on the next restart. These cases are what stops it coming back;
# coturn is covered too, independently of what the AWS deployment uses for
# TURN, because a local TURN server is wrong here for the same reason.
media_case() {
  local name="$1" kind="$2" workload="$3" host_network="$4" overlay status
  CASE=$((CASE + 1))
  overlay="$WORK_ROOT/case-$CASE"
  cp -a "$SOURCE_OVERLAY" "$overlay"
  {
    printf 'apiVersion: apps/v1\nkind: %s\nmetadata:\n  name: %s\nspec:\n' "$kind" "$workload"
    printf '  selector:\n    matchLabels:\n      app.kubernetes.io/component: %s\n' "$workload"
    printf '  template:\n    metadata:\n      labels:\n        app.kubernetes.io/component: %s\n' "$workload"
    printf '    spec:\n'
    [[ "$host_network" != yes ]] || printf '      hostNetwork: true\n'
    printf '      containers:\n        - name: %s\n          image: livekit/livekit-server:v1.13.1@sha256:2c6869d2d5ff6c9c0166f47be1c92dad6928bfecfa5e4060a6ece48db8accfa3\n' "$workload"
  } >"$overlay/media.yaml"
  add_resource "$overlay" media.yaml
  status=0
  NCHAT_PROD_STATEFUL_OVERLAY="$overlay" bash "$CHECK" >/dev/null 2>&1 || status=$?
  report "$name" reject "$status"
}

media_case "a LiveKit Deployment added back to the stateful layer is rejected" Deployment livekit no
media_case "a coturn Deployment added back to the stateful layer is rejected" Deployment coturn no
media_case "a host-networked workload is rejected" Deployment media-relay yes

# A Service is the same mistake at one remove: production addresses LiveKit by
# its external URL, never by an in-cluster name.
service_case() {
  local name="$1" workload="$2" overlay status
  CASE=$((CASE + 1))
  overlay="$WORK_ROOT/case-$CASE"
  cp -a "$SOURCE_OVERLAY" "$overlay"
  printf 'apiVersion: v1\nkind: Service\nmetadata:\n  name: %s\nspec:\n  ports:\n    - name: http\n      port: 7880\n' "$workload" >"$overlay/media-service.yaml"
  add_resource "$overlay" media-service.yaml
  status=0
  NCHAT_PROD_STATEFUL_OVERLAY="$overlay" bash "$CHECK" >/dev/null 2>&1 || status=$?
  report "$name" reject "$status"
}

service_case "a livekit Service in the stateful layer is rejected" livekit
service_case "a coturn Service in the stateful layer is rejected" coturn

echo
echo "--- secrets ---"
secret_case "a Secret committed into the overlay is rejected"
echo

if [[ "$FAILURES" -ne 0 ]]; then
  echo "production stateful gate tests failed with $FAILURES failure(s)." >&2
  exit 1
fi
echo "production stateful gate tests passed ($CASE cases)."
