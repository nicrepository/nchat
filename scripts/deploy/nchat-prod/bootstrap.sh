#!/usr/bin/env bash
# Establish production for the first time, with Blue as the baseline
# (issue #626).
#
#   NCHAT_PROD_RELEASE_SHA=<40-hex> \
#     NCHAT_PROD_RELEASE_MANIFEST_DIR=<dir with the sealed release manifest> \
#     bootstrap.sh
#
# The manifest is the only input that describes the release: the images and the
# identity are both taken from it, so there is no second source to diverge.
#
# deploy.sh cannot do this: it reads the active slot back from Services that do
# not exist yet, and it deliberately refuses to create the shared half of the
# namespace, because on every later release that half must not move. Bootstrap
# is the one time those are created, so it is a separate command run once.
#
# It provisions nothing it has no business owning. Secrets, DNS, TLS issuance,
# the Keycloak client, the stateful layer and the database backup are external
# prerequisites: this script checks for them and stops with an instruction when
# one is missing, rather than inventing a placeholder that would look like a
# working production until someone tried to sign in.
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../../.." && pwd -P)"
# shellcheck source=scripts/deploy/nchat-prod/lib.sh
source "$SCRIPT_DIR/lib.sh"
# shellcheck source=scripts/deploy/nchat-dev/lib.sh
source "$ROOT_DIR/scripts/deploy/nchat-dev/lib.sh"

TOPOLOGY_FILE="${NCHAT_PROD_TOPOLOGY_FILE:-}"
RELEASE_SHA="${NCHAT_PROD_RELEASE_SHA:-}"
# Derived from the sealed manifest before anything is rendered. A baseline that
# carried only a commit would be the one slot in the namespace that no later
# gate could identify: the release state, the smoke and the promotion all read
# the commit and the sealed build together.
RELEASE_ID=""
BASELINE_SLOT="$NCHAT_PROD_BASELINE_SLOT"
TEMPORARY_ROOT="$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/nchat-prod-bootstrap.XXXXXX")"
trap 'rm -rf "$TEMPORARY_ROOT"' EXIT
# The digests this bootstrap pins, materialised from the sealed manifest into
# the tree that is already removed on exit. It is deliberately not a directory
# the operator supplies: a second, independently-provided set of digests is a
# second source of truth, and the two can disagree.
RELEASE_DIGESTS_DIR="$TEMPORARY_ROOT/release-digests"

# The Secrets production cannot start without. Named, not guessed: each is
# provisioned by the sealed-secrets procedure in
# docs/runbooks/sealed-secrets-rotation.md.
# nchat-postgres-migrator is here because base/migrations/job.yaml reads
# MIGRATIONS_DATABASE_URL from it. Without it the bootstrap got as far as
# applying the shared half of production and then failed inside the migration
# Job, leaving a half-established namespace to unpick by hand — the whole point
# of checking prerequisites is that the first failure costs nothing.
REQUIRED_SECRETS=(nchat-secrets nchat-postgres-runtime nchat-postgres-migrator
  nchat-file-encryption ghcr-pull)
# The stateful layer is shared by both slots and is a prerequisite of this
# namespace, not a resource of it.
REQUIRED_SERVICES=(postgres valkey seaweedfs-filer)

require_secrets() {
  local secret missing=0
  for secret in "${REQUIRED_SECRETS[@]}"; do
    if ! kubectl get secret "$secret" -n "$NCHAT_PROD_NAMESPACE" >/dev/null 2>&1; then
      echo "missing Secret: $secret" >&2
      missing=$((missing + 1))
    fi
  done
  [[ "$missing" -eq 0 ]] ||
    prod_fail "provision the Secrets above with scripts/secrets/ before bootstrapping; this script will not create them"
}

require_stateful_layer() {
  local service missing=0
  for service in "${REQUIRED_SERVICES[@]}"; do
    if ! kubectl get service "$service" -n "$NCHAT_PROD_NAMESPACE" >/dev/null 2>&1; then
      echo "missing Service: $service" >&2
      missing=$((missing + 1))
    fi
  done
  [[ "$missing" -eq 0 ]] ||
    prod_fail "the shared stateful layer must exist before bootstrapping; see the prerequisites in docs/runbooks/production-blue-green-deployment.md"
}

# Everything the baseline is made of, taken from one sealed manifest.
#
# The identity and the images have to come from the same place or they are not
# an identity at all: a release id read from manifest A while the digests came
# from somewhere else describes a slot that was never built, and every later
# gate would confirm it happily because the annotation and the images are never
# compared to each other again.
#
# release-digests.sh is that single source. It verifies the seal, checks the
# manifest against the release contract, refuses a manifest that seals a
# different commit, and writes both the eleven digests and the release id --
# so there is one gate, one parser of `.images`, and nothing left to disagree.
#
# Run before render_all, so a failure here is a failure before any apply: the
# baseline is never established half-identified or on unverified images.
materialize_release_digests() {
  local manifest_dir="${NCHAT_PROD_RELEASE_MANIFEST_DIR:-}"
  [[ -n "$manifest_dir" ]] ||
    prod_fail "NCHAT_PROD_RELEASE_MANIFEST_DIR must name the directory holding the sealed release-manifest.json and release-manifest.sha256 of the release being established"
  NCHAT_PROD_RELEASE_SHA="$RELEASE_SHA" \
    "$SCRIPT_DIR/release-digests.sh" "$manifest_dir" "$RELEASE_DIGESTS_DIR" ||
    prod_fail "the release manifest in '$manifest_dir' cannot identify this release"
  RELEASE_ID="$(read_release_id "$RELEASE_DIGESTS_DIR")" ||
    prod_fail "no release identity was materialised from '$manifest_dir'"
}

render_all() {
  local prod_root="$TEMPORARY_ROOT/infra-k8s/overlays/k3s-prod" digest
  prepare_prod_deploy_tree "$ROOT_DIR" "$TEMPORARY_ROOT" "$TOPOLOGY_FILE"
  validate_rendered_overlay "$prod_root/shared" "$TEMPORARY_ROOT/shared.yaml"
  digest="$(read_digest "$RELEASE_DIGESTS_DIR/digest-migrations.txt")"
  (cd "$prod_root/migrations" && kustomize edit set image \
    "ghcr.io/nicrepository/nchat/migrations=ghcr.io/nicrepository/nchat/migrations@$digest")
  name_migration_job_for_release "$prod_root/migrations" "$RELEASE_SHA"
  validate_rendered_overlay "$prod_root/migrations" "$TEMPORARY_ROOT/migrations.yaml"
  set_slot_release_images "$prod_root/slots/$BASELINE_SLOT" "$RELEASE_DIGESTS_DIR"
  (cd "$prod_root/slots/$BASELINE_SLOT" && kustomize edit set annotation \
    "$NCHAT_PROD_RELEASE_SHA_ANNOTATION:$RELEASE_SHA" \
    "$NCHAT_PROD_RELEASE_ID_ANNOTATION:$RELEASE_ID")
  validate_rendered_overlay "$prod_root/slots/$BASELINE_SLOT" "$TEMPORARY_ROOT/baseline.yaml"
}

apply_shared() {
  kubectl apply -f "$TEMPORARY_ROOT/shared.yaml"
}

run_migrations() {
  ensure_migrations_applied "$TEMPORARY_ROOT/migrations.yaml" "$RELEASE_SHA"
}

deploy_baseline() {
  local service
  kubectl apply -f "$TEMPORARY_ROOT/baseline.yaml"
  for service in "${NCHAT_PROD_STABLE_SERVICES[@]}"; do
    if ! kubectl rollout status "deployment/$service-$BASELINE_SLOT" \
      -n "$NCHAT_PROD_NAMESPACE" --timeout=300s; then
      kubectl describe "deployment/$service-$BASELINE_SLOT" -n "$NCHAT_PROD_NAMESPACE" || true
      prod_fail "baseline workload $service-$BASELINE_SLOT did not become Ready"
    fi
  done
}

# The stable Services are rendered selecting blue, so applying shared already
# points them at the baseline. Asserting it here means bootstrap fails loudly if
# that ever stops being true, instead of leaving production selecting a slot
# that was never deployed.
verify_baseline_selected() {
  local mapping active
  mapping="$(collect_service_slots)"
  active="$(resolve_active_slot "$mapping")"
  [[ "$active" == "$BASELINE_SLOT" ]] ||
    prod_fail "stable Services select '$active', expected the baseline '$BASELINE_SLOT'"
}

main() {
  command -v kustomize >/dev/null || prod_fail "kustomize is required"
  validate_commit_sha "$RELEASE_SHA" ||
    prod_fail "NCHAT_PROD_RELEASE_SHA must be the release's full 40-character commit SHA"
  materialize_release_digests
  [[ -n "$TOPOLOGY_FILE" ]] ||
    prod_fail "NCHAT_PROD_TOPOLOGY_FILE must name the production topology (hosts, preview allowlist, LiveKit connect-src)"
  require_context
  require_namespace
  require_secrets
  require_stateful_layer
  echo "kube context : $(kubectl config current-context)"
  echo "namespace    : $NCHAT_PROD_NAMESPACE"
  echo "environment  : production (first establishment)"
  echo "baseline slot: $BASELINE_SLOT"
  echo "release sha  : $RELEASE_SHA"
  echo "release id   : $RELEASE_ID"
  confirm "Establish production with slot $BASELINE_SLOT as the baseline release"
  render_all
  # apply_shared first, and only then the capacity gate: the gate reads the
  # namespace ResourceQuota, which this apply is what creates. Nothing
  # irreversible happens in between — apply_shared writes namespace-scoped
  # configuration, no workload and no schema — so a cluster that cannot hold the
  # baseline is still refused before the migration runs and before any
  # Deployment exists.
  apply_shared
  check_capacity "$TEMPORARY_ROOT/baseline.yaml" "$TEMPORARY_ROOT" "$BASELINE_SLOT"
  run_migrations
  deploy_baseline
  verify_baseline_selected
  echo
  echo "Production is established. Slot $BASELINE_SLOT is Ready and selected by the"
  echo "stable Services; slot green is not deployed and is the next candidate."
  echo
  echo "Users must NOT be given the address yet. Run the release smoke first:"
  echo "  scripts/deploy/nchat-prod/smoke.sh --target $BASELINE_SLOT --baseline"
  echo "and complete its authenticated checklist before announcing availability."
  echo
  "$SCRIPT_DIR/status.sh" || true
}

main "$@"
