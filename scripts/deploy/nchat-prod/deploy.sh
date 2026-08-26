#!/usr/bin/env bash
# Bring the candidate slot up on a release, without giving it any traffic
# (issue #626).
#
# Deploy and cutover are separate commands on purpose. This one ends with a
# candidate that is fully Ready and reachable only from the restricted preview
# and from the smoke; promoting it is a decision someone makes afterwards, with
# the smoke result in hand.
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../../.." && pwd -P)"
# shellcheck source=scripts/deploy/nchat-prod/lib.sh
source "$SCRIPT_DIR/lib.sh"
# shellcheck source=scripts/deploy/nchat-dev/lib.sh
source "$ROOT_DIR/scripts/deploy/nchat-dev/lib.sh"

ARTIFACTS_DIR="${ARTIFACTS_DIR:-$ROOT_DIR/artifacts}"
RELEASE_SHA="${NCHAT_PROD_RELEASE_SHA:-}"
TOPOLOGY_FILE="${NCHAT_PROD_TOPOLOGY_FILE:-}"
TEMPORARY_ROOT="$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/nchat-prod-deploy.XXXXXX")"
trap 'rm -rf "$TEMPORARY_ROOT"' EXIT

render_candidate() {
  local slot="$1" overlay
  prepare_prod_deploy_tree "$ROOT_DIR" "$TEMPORARY_ROOT" "$TOPOLOGY_FILE"
  overlay="$TEMPORARY_ROOT/infra-k8s/overlays/k3s-prod/slots/$slot"
  set_slot_release_images "$overlay" "$ARTIFACTS_DIR"
  # Stamps every workload of the slot with one release identity, so a deploy
  # that reached only some of them is visible in status instead of being hidden
  # behind whichever service happened to update.
  (cd "$overlay" && kustomize edit set annotation "nchat.io/release-sha:$RELEASE_SHA")
  # validate_rendered_placeholders is nchat-dev's: it refuses a manifest still
  # carrying sha-placeholder, a mutable tag, or an unresolved REPLACE_ME_*
  # token. Production reuses it rather than growing a second, less-tested rule.
  validate_rendered_overlay "$overlay" "$TEMPORARY_ROOT/candidate.yaml"
}

render_migrations() {
  local overlay="$TEMPORARY_ROOT/infra-k8s/overlays/k3s-prod/migrations" digest
  digest="$(read_digest "$ARTIFACTS_DIR/digest-migrations.txt")"
  (cd "$overlay" && kustomize edit set image \
    "ghcr.io/nicrepository/nchat/migrations=ghcr.io/nicrepository/nchat/migrations@$digest")
  # One Job per release, so a second deploy of the same release finds the first
  # rather than replacing it, and a deploy of a different release is visibly a
  # different object instead of a silent overwrite.
  name_migration_job_for_release "$overlay" "$RELEASE_SHA"
  validate_rendered_overlay "$overlay" "$TEMPORARY_ROOT/migrations.yaml"
}


run_migrations() {
  ensure_migrations_applied "$TEMPORARY_ROOT/migrations.yaml" "$RELEASE_SHA"
}

wait_for_candidate() {
  local slot="$1" service
  for service in "${NCHAT_PROD_STABLE_SERVICES[@]}"; do
    if ! kubectl rollout status "deployment/$service-$slot" \
      -n "$NCHAT_PROD_NAMESPACE" --timeout=300s; then
      kubectl describe "deployment/$service-$slot" -n "$NCHAT_PROD_NAMESPACE" || true
      kubectl get pods -n "$NCHAT_PROD_NAMESPACE" \
        -l "$NCHAT_PROD_SLOT_LABEL=$slot" -o wide || true
      prod_fail "candidate workload $service-$slot did not become Ready"
    fi
  done
}

main() {
  local mapping active candidate
  command -v kustomize >/dev/null || prod_fail "kustomize is required"
  validate_commit_sha "$RELEASE_SHA" ||
    prod_fail "NCHAT_PROD_RELEASE_SHA must be the release's full 40-character commit SHA"
  require_context
  require_namespace
  mapping="$(collect_service_slots)"
  active="$(resolve_active_slot "$mapping")"
  candidate="$(opposite_slot "$active")"
  print_context_banner "$mapping"
  echo "release sha   : $RELEASE_SHA"
  echo "active slot   : $active"
  echo "candidate slot: $candidate"
  confirm "Deploy the release into slot '$candidate' with no traffic"
  render_candidate "$candidate"
  render_migrations
  check_capacity "$TEMPORARY_ROOT/candidate.yaml" "$TEMPORARY_ROOT" "$candidate"
  run_migrations
  kubectl apply -f "$TEMPORARY_ROOT/candidate.yaml"
  wait_for_candidate "$candidate"
  echo
  echo "Slot $candidate is Ready and carries no production traffic."
  echo "Next: smoke.sh $candidate, then cutover.sh"
}

main "$@"
