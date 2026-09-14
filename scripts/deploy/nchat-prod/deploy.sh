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
# Read from the artifacts directory rather than the environment: it is written
# there by release-digests.sh from the manifest seal, so the identity stamped on
# the workloads is the one the digests were actually taken from.
RELEASE_ID=""
TOPOLOGY_FILE="${NCHAT_PROD_TOPOLOGY_FILE:-}"
# The slot the caller has already decided on, if there is one. Empty for the
# manual flow, which derives it here as it always has.
CANDIDATE_SLOT="${NCHAT_PROD_CANDIDATE_SLOT:-}"
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
  # Both halves of the identity. The SHA says which commit; the release id says
  # which build of it, because two builds of one commit do not produce the same
  # image digests and only the second question distinguishes them.
  (cd "$overlay" && kustomize edit set annotation \
    "$NCHAT_PROD_RELEASE_SHA_ANNOTATION:$RELEASE_SHA" \
    "$NCHAT_PROD_RELEASE_ID_ANNOTATION:$RELEASE_ID")
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

# The slot this deploy will build, and the proof that it is still the right one.
#
# A caller that has already resolved the candidate passes it in. The pipeline
# does: it resolves the slot from its own reading of the stable Services -- the
# same reading its before/after selector comparison is proved against -- and
# smokes and reports that slot afterwards. Re-deriving it here would turn one
# decision into two, and a cutover landing between the two readings would leave
# the pipeline validating one slot while this script built the other.
#
# So a requested slot is revalidated, never replaced. If the cluster no longer
# agrees it is the idle one, the deploy stops -- before the migration, before
# the apply, with every stable Service untouched. Choosing the other slot
# instead is precisely the silent switch this exists to prevent.
#
# With nothing requested the canonical derivation stands, so the manual
# deploy.sh in the runbook behaves exactly as it did.
resolve_candidate_slot() {
  local active="$1" requested="$CANDIDATE_SLOT" expected
  expected="$(opposite_slot "$active")"
  [[ -n "$requested" ]] || { printf '%s' "$expected"; return 0; }
  is_valid_slot "$requested" ||
    prod_fail "NCHAT_PROD_CANDIDATE_SLOT must be blue or green, got '$requested'"
  [[ "$requested" == "$expected" ]] ||
    prod_fail "the stable Services moved after the candidate was chosen: '$requested' was requested, but the idle slot is now '$expected'. Nothing was deployed and no migration was run."
  printf '%s' "$requested"
}

main() {
  local mapping active candidate
  command -v kustomize >/dev/null || prod_fail "kustomize is required"
  validate_commit_sha "$RELEASE_SHA" ||
    prod_fail "NCHAT_PROD_RELEASE_SHA must be the release's full 40-character commit SHA"
  RELEASE_ID="$(read_release_id "$ARTIFACTS_DIR")" ||
    prod_fail "no release identity in $ARTIFACTS_DIR/$NCHAT_PROD_RELEASE_ID_FILE; pin the digests with release-digests.sh first"
  require_context
  require_namespace
  mapping="$(collect_service_slots)"
  active="$(resolve_active_slot "$mapping")"
  # Before the banner, before the confirmation, and before every mutation.
  candidate="$(resolve_candidate_slot "$active")"
  print_context_banner "$mapping"
  echo "release sha   : $RELEASE_SHA"
  echo "release id    : $RELEASE_ID"
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
