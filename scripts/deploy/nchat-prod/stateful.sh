#!/usr/bin/env bash
# Apply the stateful layer nchat-prod is built on.
#
#   scripts/deploy/nchat-prod/stateful.sh
#
# Its own command, deliberately. Everything it touches outlives every release:
# the database, the object store, the realtime bus and the four
# PersistentVolumes behind them. Not the media plane: LiveKit is one shared AWS
# deployment this cluster neither runs nor provisions. Folding that into deploy.sh would put a write
# over production's data in the blast radius of an ordinary release, which is
# the one operation that must never happen by accident.
#
# What it will not do, by construction:
#   - create or delete a PersistentVolume's directory on the host
#   - delete a PersistentVolume, a PersistentVolumeClaim or a StatefulSet
#   - create a Secret, or read one
#   - touch nchat-dev
#
# `kubectl apply` cannot remove a volume, and nothing below calls delete except
# on the postgres-bootstrap Job, whose pod template is immutable once it has
# completed. Reclaiming a Released volume stays a deliberate manual act; see
# docs/runbooks/production-blue-green-deployment.md.
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../../.." && pwd -P)"
# shellcheck source=scripts/deploy/nchat-prod/lib.sh
source "$SCRIPT_DIR/lib.sh"
# shellcheck source=scripts/deploy/nchat-dev/lib.sh
source "$ROOT_DIR/scripts/deploy/nchat-dev/lib.sh"

OVERLAY="$ROOT_DIR/infra/k8s/overlays/k3s-prod/stateful"
MANIFEST_CHECK="$ROOT_DIR/scripts/ci/prod-stateful-check.sh"
TEMPORARY_ROOT="$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/nchat-prod-stateful.XXXXXX")"
trap 'rm -rf "$TEMPORARY_ROOT"' EXIT

# Read by PostgreSQL, the bootstrap Job and Valkey. Each is provisioned by
# docs/runbooks/sealed-secrets-rotation.md; none is created here.
# nchat-postgres-admin is on the list and is absent from bootstrap.sh's, because
# it is this layer — not the release — that needs the superuser.
#
# Stated as secret/key pairs, not just secret names. A Secret that exists with
# the key missing or empty fails later and further away: PostgreSQL starts with
# no password and rejects every connection, or the bootstrap Job creates a role
# with an empty password. Both are discovered as a workload that will not become
# Ready, with nothing pointing at the cause.
#
# Only what the STATEFUL layer itself consumes. Keys that belong to the
# Blue/Green release -- LIVEKIT_API_KEY, AUTH_JWT_HMAC_SECRET, the rest of
# nchat-secrets -- are checked by bootstrap.sh, which is the script that
# deploys the workloads reading them. Checking them here would fail an operator
# who is correctly applying storage before provisioning the release.
#
# VALKEY_PASSWORD is the one nchat-secrets key on the list: valkey is in this
# layer, and data.yaml reads exactly that key.
REQUIRED_SECRET_KEYS=(
  "nchat-secrets:VALKEY_PASSWORD"
  "nchat-postgres-admin:POSTGRES_ADMIN_USER"
  "nchat-postgres-admin:POSTGRES_ADMIN_PASSWORD"
  "nchat-postgres-runtime:POSTGRES_APP_PASSWORD"
  "nchat-postgres-migrator:POSTGRES_MIGRATOR_PASSWORD"
)
# The complete contract for each PersistentVolume, as declared by
# stateful/storage.yaml, checked field by field against the cluster before
# anything is applied.
#
# The path alone is not enough to call an existing volume safe to reuse. A
# volume with the right path and half the capacity, or bound to a claim in
# another namespace, is a volume that will either fail to bind or hand
# production somebody else's data -- and both are discovered after the apply,
# which is too late for a database.
#
# Fields: name:path:capacity:accessMode:node:claimNamespace/claimName
STORAGE_CLASS=local-hdd-geral
STORAGE_NODE=srv-apps-01
VOLUMES=(
  "nchat-prod-postgres:/mnt/hdd-geral/k3s/nchat-prod/postgres:30Gi:ReadWriteOnce:$STORAGE_NODE:nchat-prod/data-postgres-0"
  "nchat-prod-valkey:/mnt/hdd-geral/k3s/nchat-prod/valkey:10Gi:ReadWriteOnce:$STORAGE_NODE:nchat-prod/data-valkey-0"
  "nchat-prod-seaweedfs:/mnt/hdd-geral/k3s/nchat-prod/seaweedfs:60Gi:ReadWriteOnce:$STORAGE_NODE:nchat-prod/data-seaweedfs-0"
  "nchat-prod-auth-avatars:/mnt/hdd-geral/k3s/nchat-prod/auth-avatars:1Gi:ReadWriteOnce:$STORAGE_NODE:nchat-prod/auth-service-avatars"
)

# Whether one key of one Secret exists and decodes to something non-empty.
#
# The value never leaves this function: it is decoded into a local, measured,
# and discarded. It is never echoed, never interpolated into a command line
# where `ps` could see it, and never written to a file. The caller learns one
# bit -- present and non-empty, or not.
#
# The locals below are named for what they hold: the Kubernetes object's name
# and the name of one entry under its .data. None of them ever holds a
# credential -- the only variable that touches a decoded value is `decoded`,
# which is local and discarded. The naming is also what keeps
# scripts/ci/governance-secret-markers-check.py quiet: it flags any assignment
# whose target ends in a sensitive word, and an identifier called `secret` is
# indistinguishable to it from one holding the thing itself.
secret_key_is_populated() {
  local object_name="$1" data_key="$2" encoded decoded
  encoded="$(kubectl get secret "$object_name" -n "$NCHAT_PROD_NAMESPACE" \
    -o "jsonpath={.data.$data_key}" 2>/dev/null)" || return 1
  [[ -n "$encoded" ]] || return 1
  # A key can hold whitespace-only base64 and still be useless as a password.
  decoded="$(printf '%s' "$encoded" | base64 -d 2>/dev/null | tr -d '[:space:]')" || return 1
  [[ -n "$decoded" ]]
}

require_secrets() {
  local entry object_name data_key missing=0 reported_absent=""
  for entry in "${REQUIRED_SECRET_KEYS[@]}"; do
    object_name="${entry%%:*}"
    data_key="${entry#*:}"
    if ! kubectl get secret "$object_name" -n "$NCHAT_PROD_NAMESPACE" >/dev/null 2>&1; then
      # One line per absent Secret, not one per key it would have carried.
      if [[ "$reported_absent" != *"|$object_name|"* ]]; then
        echo "missing Secret: $object_name" >&2
        reported_absent="$reported_absent|$object_name|"
        missing=$((missing + 1))
      fi
      continue
    fi
    secret_key_is_populated "$object_name" "$data_key" && continue
    # Names only. Never the value, never the base64, never a length.
    echo "missing/empty Secret key: $object_name/$data_key" >&2
    missing=$((missing + 1))
  done
  [[ "$missing" -eq 0 ]] ||
    prod_fail "provision the Secrets above before applying the stateful layer; this script will not create them"
}

volume_field() {
  kubectl get pv "$1" -o "jsonpath={$2}" 2>/dev/null
}

# One existing PersistentVolume, compared field by field against what this
# overlay declares. Prints one line per mismatch and nothing at all when the
# volume does not exist yet -- the first apply is the normal case.
#
# Nothing here mutates anything. A volume that disagrees is reported and the
# apply is refused: this script never clears a claimRef, never deletes a volume
# and never recreates one to reconcile drift, because each of those silently
# detaches production data from the object that describes it.
report_volume_drift() {
  local name="$1" path="$2" capacity="$3" access_mode="$4" node="$5" claim="$6" actual phase
  kubectl get pv "$name" >/dev/null 2>&1 || return 0

  actual="$(volume_field "$name" .spec.local.path)"
  [[ "$actual" == "$path" ]] ||
    echo "pv/$name points at '${actual:-nothing}', expected '$path'" >&2
  actual="$(volume_field "$name" .spec.persistentVolumeReclaimPolicy)"
  [[ "$actual" == Retain ]] ||
    echo "pv/$name has reclaimPolicy '${actual:-unset}', expected Retain" >&2
  actual="$(volume_field "$name" .spec.storageClassName)"
  [[ "$actual" == "$STORAGE_CLASS" ]] ||
    echo "pv/$name has storageClass '${actual:-unset}', expected $STORAGE_CLASS" >&2

  # Capacity: a volume smaller than the claim asks for never binds, and one
  # larger silently hides that the contract moved.
  actual="$(volume_field "$name" .spec.capacity.storage)"
  [[ "$actual" == "$capacity" ]] ||
    echo "pv/$name has capacity '${actual:-unset}', expected $capacity" >&2

  # Access modes: the claim asks for exactly one, and a mismatch is another
  # binding that never happens.
  actual="$(volume_field "$name" ".spec.accessModes[*]")"
  [[ "$actual" == "$access_mode" ]] ||
    echo "pv/$name has accessModes '${actual:-none}', expected $access_mode" >&2

  # volumeMode: these are filesystem volumes. Block would mount as a raw device
  # and the workload would find no data at all. Unset means Filesystem.
  actual="$(volume_field "$name" .spec.volumeMode)"
  [[ -z "$actual" || "$actual" == Filesystem ]] ||
    echo "pv/$name has volumeMode '$actual', expected Filesystem" >&2

  # Node affinity: a local volume that admits another node can bind where its
  # directory does not exist, which mounts an empty filesystem over production
  # data that is still sitting on the right node.
  actual="$(volume_field "$name" ".spec.nodeAffinity.required.nodeSelectorTerms[*].matchExpressions[?(@.key=='kubernetes.io/hostname')].values[*]")"
  [[ "$actual" == "$node" ]] ||
    echo "pv/$name admits nodes '${actual:-none}', expected $node" >&2

  report_volume_binding "$name" "$claim"
}

# Where a volume already carries a claim, that claim must be the one this
# deployment expects. This is the check that separates "our volume, already in
# use, rerun is fine" from "somebody else's data".
report_volume_binding() {
  local name="$1" expected_claim="$2" phase claim_namespace claim_name actual_claim
  phase="$(volume_field "$name" .status.phase)"
  claim_namespace="$(volume_field "$name" .spec.claimRef.namespace)"
  claim_name="$(volume_field "$name" .spec.claimRef.name)"
  actual_claim="$claim_namespace/$claim_name"

  case "$phase" in
    Available)
      # Unbound and unclaimed is the clean rerun: the apply will bind it.
      [[ -z "$claim_namespace$claim_name" ]] ||
        echo "pv/$name is Available but reserved for '$actual_claim'; expected no claimRef" >&2
      ;;
    Bound)
      [[ "$actual_claim" == "$expected_claim" ]] ||
        echo "pv/$name is Bound to '$actual_claim', expected '$expected_claim'" >&2
      ;;
    Released)
      # A Released volume still holds the previous claim's data and will never
      # bind again while its claimRef stands. Clearing that claimRef is exactly
      # the destructive shortcut this script refuses to take: it is how a
      # volume gets rebound to a new, empty claim while the old data is still
      # on disk and unreferenced.
      echo "pv/$name is Released (previous claim '$actual_claim'); reclaim it deliberately by hand -- see the runbook -- this script will not clear a claimRef" >&2
      ;;
    Failed)
      echo "pv/$name is Failed; investigate before applying" >&2
      ;;
    Pending | "")
      echo "pv/$name has phase '${phase:-unknown}'; wait for it to settle before applying" >&2
      ;;
    *)
      echo "pv/$name has unexpected phase '$phase'" >&2
      ;;
  esac
}

require_volumes_undrifted() {
  local entry drift name path capacity access_mode node claim
  drift="$(
    for entry in "${VOLUMES[@]}"; do
      IFS=: read -r name path capacity access_mode node claim <<<"$entry"
      report_volume_drift "$name" "$path" "$capacity" "$access_mode" "$node" "$claim"
    done 2>&1
  )"
  [[ -z "$drift" ]] || {
    echo "$drift" >&2
    prod_fail "an existing PersistentVolume does not match this overlay; reconcile it by hand, this script will not delete a volume or clear a claimRef"
  }
}

render() {
  cp -a "$OVERLAY" "$TEMPORARY_ROOT/stateful"
  validate_rendered_overlay "$TEMPORARY_ROOT/stateful" "$TEMPORARY_ROOT/stateful.yaml"
}

# The state of the bootstrap Job, as one word: Complete, Failed, Running or
# Absent. Read from the Job's own conditions rather than from its pods, because
# the condition is what the API server itself considers settled.
bootstrap_job_state() {
  local complete failed active
  kubectl get job/postgres-bootstrap -n "$NCHAT_PROD_NAMESPACE" >/dev/null 2>&1 || {
    printf 'Absent'
    return
  }
  complete="$(kubectl get job/postgres-bootstrap -n "$NCHAT_PROD_NAMESPACE" \
    -o 'jsonpath={.status.conditions[?(@.type=="Complete")].status}' 2>/dev/null)"
  failed="$(kubectl get job/postgres-bootstrap -n "$NCHAT_PROD_NAMESPACE" \
    -o 'jsonpath={.status.conditions[?(@.type=="Failed")].status}' 2>/dev/null)"
  active="$(kubectl get job/postgres-bootstrap -n "$NCHAT_PROD_NAMESPACE" \
    -o 'jsonpath={.status.active}' 2>/dev/null)"
  if [[ "$complete" == True ]]; then
    printf 'Complete'
  elif [[ "$failed" == True ]]; then
    printf 'Failed'
  elif [[ -n "$active" && "$active" != 0 ]]; then
    printf 'Running'
  else
    # No condition and no active pod: it exists but has not started, which is
    # still not something to delete out from under.
    printf 'Running'
  fi
}

# Whether the existing Job may be replaced, decided before anything is deleted.
#
# The Job's pod template is immutable once it has completed, so a second apply
# is rejected unless the old one goes first. That is the only delete this
# script performs -- but it must never land on a Job that is still running.
# Deleting a running bootstrap kills a pod that is midway through CREATE ROLE
# and GRANT statements, and while each statement is idempotent, being
# interrupted between them is not the same as not having run.
require_bootstrap_job_replaceable() {
  local state
  state="$(bootstrap_job_state)"
  case "$state" in
    Absent)
      ;;
    Complete)
      ;;
    Failed)
      echo "job/postgres-bootstrap previously FAILED; it will be deleted and re-run." >&2
      echo "Inspect the previous attempt first if you have not already:" >&2
      echo "  kubectl -n $NCHAT_PROD_NAMESPACE logs job/postgres-bootstrap" >&2
      ;;
    Running)
      prod_fail "job/postgres-bootstrap is still running; wait for it to finish rather than interrupting it mid-statement. Watch it with: kubectl -n $NCHAT_PROD_NAMESPACE wait job/postgres-bootstrap --for=condition=complete --timeout=300s"
      ;;
    *)
      prod_fail "job/postgres-bootstrap is in an unexpected state '$state'; resolve it by hand"
      ;;
  esac
}

apply_stateful() {
  # Safe to delete: require_bootstrap_job_replaceable has already refused a Job
  # that is still running. This destroys no data -- every statement the Job runs
  # is idempotent.
  kubectl delete job/postgres-bootstrap -n "$NCHAT_PROD_NAMESPACE" \
    --ignore-not-found --wait=true
  kubectl apply -f "$TEMPORARY_ROOT/stateful.yaml"
}

wait_ready() {
  local workload
  for workload in statefulset/postgres statefulset/valkey statefulset/seaweedfs; do
    kubectl rollout status "$workload" -n "$NCHAT_PROD_NAMESPACE" --timeout=300s ||
      prod_fail "$workload did not become Ready"
  done
  kubectl wait job/postgres-bootstrap -n "$NCHAT_PROD_NAMESPACE" \
    --for=condition=complete --timeout=300s ||
    prod_fail "postgres-bootstrap did not complete; the nchat_app and nchat_migrator roles may not exist"
}

main() {
  command -v kustomize >/dev/null || prod_fail "kustomize is required"
  "$MANIFEST_CHECK" >/dev/null ||
    prod_fail "the stateful overlay failed its own manifest check; run $MANIFEST_CHECK"
  require_context
  require_namespace
  require_secrets
  require_volumes_undrifted
  # Before the confirmation prompt, not after: an operator should be refused a
  # running bootstrap Job without first being asked to approve an apply.
  require_bootstrap_job_replaceable
  render
  echo "kube context : $(kubectl config current-context)"
  echo "namespace    : $NCHAT_PROD_NAMESPACE"
  echo "layer        : stateful (postgres, valkey, seaweedfs)"
  echo "media plane  : external, shared AWS LiveKit -- not applied here"
  echo
  echo "The four host directories must already exist and be owned as documented"
  echo "in docs/runbooks/production-blue-green-deployment.md; this script does"
  echo "not create them and a missing one fails as a mount error."
  confirm "Apply the production stateful layer"
  apply_stateful
  wait_ready
  echo
  echo "The stateful layer is Ready. Next: scripts/deploy/nchat-prod/bootstrap.sh,"
  echo "which will now find the Services it requires."
}

# Sourcing defines the preflight checks without running them, so
# scripts/ci/test_prod_stateful_preflight.sh can drive each one against a fake
# kubectl and prove it refuses what it claims to. Same pattern as
# scripts/ci/prod-blue-green-check.sh.
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  main "$@"
fi
