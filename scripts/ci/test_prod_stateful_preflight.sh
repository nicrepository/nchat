#!/usr/bin/env bash
# Negative tests for the production stateful layer's preflight checks.
#
# These are the checks that run before `kubectl apply` touches production
# storage, so the only way to know they refuse what they claim to refuse is to
# drive them with input that must be refused. Each case stubs kubectl with a
# table of object fields and asserts the verdict.
#
# No cluster, no network. Same pattern as scripts/ci/test_prod_blue_green_check.sh:
# the script under test is sourced, which defines its functions without running
# main.
set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
STATEFUL="$ROOT_DIR/scripts/deploy/nchat-prod/stateful.sh"

FAILURES=0
# The fake cluster: "<kind>/<name>|<jsonpath>" -> value, one per line. A kind
# absent from OBJECTS does not exist, which is the first-apply case.
OBJECTS=""
EXISTING=""

# stateful.sh sources lib.sh, which needs these; sourcing must not run main.
NCHAT_PROD_NAMESPACE="${NCHAT_PROD_NAMESPACE:-nchat-prod}"
# shellcheck source=scripts/deploy/nchat-prod/stateful.sh
source "$STATEFUL"

# prod_fail exits; inside a test we only need to know that it fired.
prod_fail() {
  echo "refused: $*" >&2
  return 1
}

# A kubectl that answers from OBJECTS and EXISTING and touches nothing.
#
# It has to accept the forms the scripts really use: `get pv <name>` and
# `get job/<name>` alike, with the kind spelled the short way. Normalising here
# rather than in the fixtures is what keeps the fixtures readable.
normalise_kind() {
  case "$1" in
    pv | persistentvolume | persistentvolumes) printf 'PersistentVolume' ;;
    secret | secrets) printf 'Secret' ;;
    job | jobs) printf 'Job' ;;
    *) printf '%s' "$1" ;;
  esac
}

kubectl() {
  local args="$*" kind name key
  [[ "$1" == get ]] || return 0
  shift
  if [[ "$1" == */* ]]; then
    kind="$(normalise_kind "${1%%/*}")"
    name="${1#*/}"
  else
    kind="$(normalise_kind "$1")"
    name="$2"
  fi
  grep -Fxq "$kind/$name" <<<"$EXISTING" || return 1
  # No -o means an existence probe, which the line above already answered.
  [[ "$args" == *jsonpath=* ]] || { echo "$kind/$name"; return 0; }
  key="${args#*jsonpath=}"
  key="${key%\}*}"
  key="${key#\{}"
  key="${key%\'}"
  awk -F'|' -v want="$kind/$name" -v field="$key" \
    '$1 == want && $2 == field { print $3 }' <<<"$OBJECTS"
  return 0
}

expect() {
  local name="$1" wanted="$2" check="$3" status=0
  "$check" >/dev/null 2>&1 || status=$?
  if [[ "$wanted" == reject && "$status" -eq 0 ]]; then
    echo "  [FAIL] $name: the preflight accepted it" >&2
    FAILURES=$((FAILURES + 1))
    return
  fi
  if [[ "$wanted" == accept && "$status" -ne 0 ]]; then
    echo "  [FAIL] $name: the preflight refused valid input" >&2
    FAILURES=$((FAILURES + 1))
    return
  fi
  echo "  [OK]   $name"
}

# One PersistentVolume matching the committed contract exactly, as the fake
# cluster would report it. Cases below change one field at a time.
good_volume() {
  local phase="${1-Bound}" claim_ns="${2-nchat-prod}" claim="${3-data-postgres-0}"
  EXISTING="PersistentVolume/nchat-prod-postgres"
  OBJECTS="$(cat <<TABLE
PersistentVolume/nchat-prod-postgres|.spec.local.path|/mnt/hdd-geral/k3s/nchat-prod/postgres
PersistentVolume/nchat-prod-postgres|.spec.persistentVolumeReclaimPolicy|Retain
PersistentVolume/nchat-prod-postgres|.spec.storageClassName|local-hdd-geral
PersistentVolume/nchat-prod-postgres|.spec.capacity.storage|30Gi
PersistentVolume/nchat-prod-postgres|.spec.accessModes[*]|ReadWriteOnce
PersistentVolume/nchat-prod-postgres|.spec.volumeMode|Filesystem
PersistentVolume/nchat-prod-postgres|.spec.nodeAffinity.required.nodeSelectorTerms[*].matchExpressions[?(@.key=='kubernetes.io/hostname')].values[*]|srv-apps-01
PersistentVolume/nchat-prod-postgres|.status.phase|$phase
PersistentVolume/nchat-prod-postgres|.spec.claimRef.namespace|$claim_ns
PersistentVolume/nchat-prod-postgres|.spec.claimRef.name|$claim
TABLE
)"
}

# Only the postgres volume is under test; the check iterates VOLUMES, so the
# other three must report as absent, which they do by not being in EXISTING.
one_volume() {
  report_volume_drift nchat-prod-postgres \
    /mnt/hdd-geral/k3s/nchat-prod/postgres 30Gi ReadWriteOnce srv-apps-01 \
    nchat-prod/data-postgres-0
}

# report_volume_drift prints findings rather than returning non-zero, so a case
# is a rejection when it printed anything at all.
volume_verdict() {
  local output
  output="$(one_volume 2>&1)"
  [[ -z "$output" ]]
}

drift_case() {
  local name="$1" field="$2" value="$3"
  good_volume
  OBJECTS="$(awk -F'|' -v f="$field" -v v="$value" 'BEGIN{OFS="|"}
    $2 == f { $3 = v } { print }' <<<"$OBJECTS")"
  expect "$name" reject volume_verdict
}

echo "=== production stateful preflight: negative cases ==="
echo
echo "--- persistent volumes ---"

EXISTING=""
OBJECTS=""
expect "a volume that does not exist yet is accepted (first apply)" accept volume_verdict

good_volume Bound nchat-prod data-postgres-0
expect "a volume bound to its own expected claim is accepted" accept volume_verdict

good_volume Available "" ""
expect "an Available volume with no claimRef is accepted" accept volume_verdict

drift_case "a volume pointed at another path is rejected" .spec.local.path /mnt/hdd-geral/k3s/nchat-dev/postgres
drift_case "a volume that would be garbage collected is rejected" .spec.persistentVolumeReclaimPolicy Delete
drift_case "a volume on the default storage class is rejected" .spec.storageClassName local-path
drift_case "a volume of the wrong capacity is rejected" .spec.capacity.storage 10Gi
drift_case "a volume with the wrong access mode is rejected" '.spec.accessModes[*]' ReadWriteMany
drift_case "a raw block volume is rejected" .spec.volumeMode Block
drift_case "a volume that could bind on another node is rejected" \
  ".spec.nodeAffinity.required.nodeSelectorTerms[*].matchExpressions[?(@.key=='kubernetes.io/hostname')].values[*]" srv-other-99

good_volume Bound nchat-dev data-postgres-0
expect "a volume bound to a claim in another namespace is rejected" reject volume_verdict

good_volume Bound nchat-prod some-other-claim
expect "a volume bound to a different claim is rejected" reject volume_verdict

good_volume Released nchat-prod data-postgres-0
expect "a Released volume is refused rather than silently reused" reject volume_verdict

good_volume Available nchat-prod data-postgres-0
expect "an Available volume still reserved by a claimRef is rejected" reject volume_verdict

good_volume Failed nchat-prod data-postgres-0
expect "a Failed volume is rejected" reject volume_verdict

good_volume Pending nchat-prod data-postgres-0
expect "a volume that has not settled is rejected" reject volume_verdict

echo
echo "--- secret keys ---"

# base64 of "s3cret" and of "   " -- a key that decodes to whitespace is as
# useless as an absent one and must be refused the same way.
POPULATED="czNjcmV0"
BLANK="ICAg"

secret_table() {
  EXISTING="$(printf 'Secret/nchat-secrets\nSecret/nchat-postgres-admin\nSecret/nchat-postgres-runtime\nSecret/nchat-postgres-migrator')"
  OBJECTS="$(cat <<TABLE
Secret/nchat-secrets|.data.VALKEY_PASSWORD|${1-$POPULATED}
Secret/nchat-postgres-admin|.data.POSTGRES_ADMIN_USER|${2-$POPULATED}
Secret/nchat-postgres-admin|.data.POSTGRES_ADMIN_PASSWORD|${3-$POPULATED}
Secret/nchat-postgres-runtime|.data.POSTGRES_APP_PASSWORD|${4-$POPULATED}
Secret/nchat-postgres-migrator|.data.POSTGRES_MIGRATOR_PASSWORD|${5-$POPULATED}
TABLE
)"
}

secret_table
expect "every required key present and populated is accepted" accept require_secrets

secret_table "$POPULATED" "$POPULATED" "" "$POPULATED" "$POPULATED"
expect "an absent admin password is rejected" reject require_secrets

secret_table "$POPULATED" "$POPULATED" "$BLANK" "$POPULATED" "$POPULATED"
expect "an admin password that decodes to whitespace is rejected" reject require_secrets

secret_table "" "$POPULATED" "$POPULATED" "$POPULATED" "$POPULATED"
expect "an absent VALKEY_PASSWORD is rejected" reject require_secrets

secret_table "$POPULATED" "$POPULATED" "$POPULATED" "$POPULATED" ""
expect "an absent migrator password is rejected" reject require_secrets

secret_table
EXISTING="$(printf 'Secret/nchat-secrets\nSecret/nchat-postgres-runtime\nSecret/nchat-postgres-migrator')"
expect "an entirely absent Secret is rejected" reject require_secrets

# The values must never reach the operator's terminal. This asserts on the
# check's own output, which is the thing an operator and a CI log actually see.
secret_table "$POPULATED" "$POPULATED" "" "$POPULATED" "$POPULATED"
output="$(require_secrets 2>&1 || true)"
if grep -q "s3cret\|$POPULATED" <<<"$output"; then
  echo "  [FAIL] the preflight printed a Secret value or its base64" >&2
  FAILURES=$((FAILURES + 1))
else
  echo "  [OK]   a failure names the key and never the value"
fi

echo
echo "--- bootstrap job ---"

job_state() {
  EXISTING="Job/postgres-bootstrap"
  OBJECTS="$(cat <<TABLE
Job/postgres-bootstrap|.status.conditions[?(@.type=="Complete")].status|$1
Job/postgres-bootstrap|.status.conditions[?(@.type=="Failed")].status|$2
Job/postgres-bootstrap|.status.active|$3
TABLE
)"
}

EXISTING=""
OBJECTS=""
expect "no bootstrap Job yet is accepted" accept require_bootstrap_job_replaceable

job_state True "" ""
expect "a completed bootstrap Job may be replaced" accept require_bootstrap_job_replaceable

job_state "" True ""
expect "a failed bootstrap Job may be replaced" accept require_bootstrap_job_replaceable

# The blocker: deleting this Job kills a pod midway through CREATE ROLE and
# GRANT statements.
job_state "" "" 1
expect "a running bootstrap Job is refused, not killed" reject require_bootstrap_job_replaceable

job_state "" "" ""
expect "a bootstrap Job that has not started is refused" reject require_bootstrap_job_replaceable

echo
if [[ "$FAILURES" -ne 0 ]]; then
  echo "stateful preflight tests failed with $FAILURES failure(s)." >&2
  exit 1
fi
echo "production stateful preflight tests passed."
