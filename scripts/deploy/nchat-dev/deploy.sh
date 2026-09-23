#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../../.." && pwd -P)"
# shellcheck source=scripts/deploy/nchat-dev/lib.sh
source "$SCRIPT_DIR/lib.sh"
# shellcheck source=scripts/deploy/nchat-dev/kustomize.env
source "$SCRIPT_DIR/kustomize.env"

ARTIFACTS_DIR="${ARTIFACTS_DIR:-$ROOT_DIR/artifacts}"
TEMPORARY_ROOT="$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/nchat-deploy.XXXXXX")"
DEPLOY_STAGE=""

cleanup() {
  cleanup_deploy_tree "$TEMPORARY_ROOT"
}

collect_diagnostics() {
  [[ -n "$DEPLOY_STAGE" ]] || return 0
  echo "Collecting non-sensitive diagnostics after failure in: $DEPLOY_STAGE" >&2
  kubectl get deployments,statefulsets,jobs,pods,services -n nchat-dev -o wide || true
  kubectl get events -n nchat-dev --sort-by=.lastTimestamp || true
  case "$DEPLOY_STAGE" in
    bootstrap)
      kubectl describe job/postgres-bootstrap -n nchat-dev || true
      kubectl logs job/postgres-bootstrap -n nchat-dev --all-containers --tail=100 || true
      ;;
    migrations)
      kubectl describe job/nchat-migrations -n nchat-dev || true
      kubectl logs job/nchat-migrations -n nchat-dev --all-containers --tail=100 || true
      ;;
  esac
}

on_error() {
  local status=$?
  trap - ERR
  set +e
  collect_diagnostics
  exit "$status"
}

trap cleanup EXIT
trap on_error ERR

require_prerequisites() {
  local command actual_kustomize verb resource
  for command in curl grep kustomize kubectl; do
    command -v "$command" >/dev/null
  done
  [[ "$(kubectl config current-context)" == nchat-dev-deployer ]]
  require_kubernetes_permission patch deployments nchat-dev
  # RF-32/#455 added IngressRoute/nchat-dev-uploads to the rendered overlay;
  # verify the deployer can manage it (and the plain Ingresses it ships
  # alongside) before any kustomize build, so a missing grant fails here
  # instead of mid-apply.
  for verb in get create patch update; do
    require_kubernetes_permission "$verb" ingressroutes.traefik.io nchat-dev
    require_kubernetes_permission "$verb" ingresses.networking.k8s.io nchat-dev
  done
  actual_kustomize="$(kustomize version)"
  [[ "$actual_kustomize" == "$KUSTOMIZE_VERSION" ]]
}

prepare_application_overlays() {
  local image
  prepare_deploy_tree "$ROOT_DIR" "$TEMPORARY_ROOT"
  DATA_OVERLAY="$TEMPORARY_ROOT/infra-k8s/overlays/nchat-dev-server/data"
  MIGRATIONS_OVERLAY="$TEMPORARY_ROOT/infra-k8s/overlays/nchat-dev-server/migrations"
  APPLICATION_OVERLAY="$TEMPORARY_ROOT/infra-k8s/overlays/nchat-dev-server"
  set_digest_image "$MIGRATIONS_OVERLAY" \
    ghcr.io/nicrepository/nchat/migrations "$ARTIFACTS_DIR/digest-migrations.txt"
  for image in "${NCHAT_DEV_RUNTIME_IMAGES[@]}"; do
    set_digest_image "$APPLICATION_OVERLAY" "ghcr.io/nicrepository/nchat/$image" \
      "$ARTIFACTS_DIR/digest-$image.txt"
  done
  validate_rendered_overlay "$DATA_OVERLAY" "$TEMPORARY_ROOT/data.yaml"
  validate_rendered_overlay "$MIGRATIONS_OVERLAY" "$TEMPORARY_ROOT/migrations.yaml"
  validate_rendered_overlay "$APPLICATION_OVERLAY" "$TEMPORARY_ROOT/application.yaml"
}

start_data_services() {
  DEPLOY_STAGE=bootstrap
  kubectl delete job/postgres-bootstrap -n nchat-dev --ignore-not-found --wait=true
  kubectl apply -f "$TEMPORARY_ROOT/data.yaml"
  kubectl rollout status statefulset/postgres -n nchat-dev --timeout=180s
  kubectl wait job/postgres-bootstrap -n nchat-dev --for=condition=complete --timeout=210s
}

run_migrations() {
  DEPLOY_STAGE=migrations
  kubectl delete job/nchat-migrations -n nchat-dev --ignore-not-found --wait=true
  kubectl apply -f "$TEMPORARY_ROOT/migrations.yaml"
  kubectl wait job/nchat-migrations -n nchat-dev --for=condition=complete --timeout=330s
}

apply_application() {
  DEPLOY_STAGE=application
  kubectl apply -f "$TEMPORARY_ROOT/application.yaml"
}

wait_for_rollouts() {
  local workload
  DEPLOY_STAGE=rollout
  for workload in "${NCHAT_DEV_APPLICATION_DEPLOYMENTS[@]}" livekit coturn; do
    if ! kubectl rollout status "deployment/$workload" -n nchat-dev --timeout=180s; then
      echo "Rollout failed for deployment/$workload" >&2
      kubectl describe "deployment/$workload" -n nchat-dev || true
      kubectl get pods -n nchat-dev -l "app.kubernetes.io/component=$(kubectl get deployment "$workload" -n nchat-dev -o jsonpath='{.metadata.labels.app\.kubernetes\.io/component}')" -o wide || true
      return 1
    fi
  done
  for workload in postgres valkey seaweedfs; do
    if ! kubectl rollout status "statefulset/$workload" -n nchat-dev --timeout=180s; then
      echo "Rollout failed for statefulset/$workload" >&2
      kubectl describe "statefulset/$workload" -n nchat-dev || true
      return 1
    fi
  done
}

# The operational proof moved to scripts/deploy/nchat-dev/smoke.sh with issue
# #933, and with it the health probes and the public request that used to end
# this script.
#
# Two reasons, and neither is tidiness. Applying manifests and proving the
# release works are different jobs with different failure meanings -- one says
# Kubernetes rejected the change, the other says the change is bad -- and the
# CD workflow reports them as two steps because an operator reading a summary
# needs to know which happened. And a smoke that only ever runs as the tail of
# a deploy cannot be run on its own against an environment somebody is
# debugging, which is when it is most useful.
validate_commit_sha "${DEPLOY_SHA:-}"
validate_digest_artifacts "$ARTIFACTS_DIR"
require_prerequisites
prepare_application_overlays
start_data_services
run_migrations
apply_application
wait_for_rollouts
DEPLOY_STAGE=""
