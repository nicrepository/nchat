#!/usr/bin/env bash
# Negative tests for the production manifest gate (issue #626).
#
# A gate is only worth its runtime if it fails on the thing it claims to catch.
# These stub the manifest reader and drive each routing and release rule with
# input it must reject, so a refactor that quietly stops asserting something is
# caught here rather than in production.
set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
# shellcheck source=scripts/ci/prod-blue-green-check.sh
source "$ROOT_DIR/scripts/ci/prod-blue-green-check.sh"

FAILURES=0
QUERY_FIXTURE=""

# Replaces the manifest reader with a fixture table of "<query>\t<output>".
query() {
  awk -F'\t' -v want="$1" '$1 == want { print substr($0, index($0, "\t") + 1) }' <<<"$QUERY_FIXTURE"
}

# Every check writes findings through fail(); counting them is how these tests
# observe a rule firing.
expect() {
  local name="$1" wanted="$2" check="$3"
  ERRORS=0
  "$check" >/dev/null 2>&1 || true
  if [[ "$wanted" == reject && "$ERRORS" -eq 0 ]]; then
    echo "  [FAIL] $name: the gate accepted it" >&2
    FAILURES=$((FAILURES + 1))
    return
  fi
  if [[ "$wanted" == accept && "$ERRORS" -ne 0 ]]; then
    echo "  [FAIL] $name: the gate rejected valid input ($ERRORS finding(s))" >&2
    FAILURES=$((FAILURES + 1))
    return
  fi
  echo "  [OK]   $name"
}

good_ingress_backends=$(printf 'Ingress|backends\tnchat-prod|chat-service
Ingress|backends\tnchat-prod-admin|admin-service
Ingress|backends\tnchat-prod-preview-blue|chat-service-blue
Ingress|backends\tnchat-prod-preview-green|chat-service-green
Ingress|backends\tnchat-prod-preview-admin-blue|admin-service-blue
Ingress|backends\tnchat-prod-preview-admin-green|admin-service-green')

echo "=== production manifest gate: negative cases ==="
echo
echo "--- stable routing ---"

QUERY_FIXTURE="$good_ingress_backends"
expect "a correct routing table is accepted" accept check_stable_ingress_routing

QUERY_FIXTURE=$(printf 'Ingress|backends\tnchat-prod|chat-service-green')
expect "a public host pointing at a per-slot Service is rejected" reject check_stable_ingress_routing

QUERY_FIXTURE=$(printf 'Ingress|backends\tnchat-prod|postgres')
expect "a public host pointing at an unapproved backend is rejected" reject check_stable_ingress_routing

QUERY_FIXTURE=$(printf 'Ingress|backends\tnchat-prod-admin|admin-service')
expect "the stable admin host keeps its stable backends" accept check_stable_ingress_routing

echo
echo "--- preview routing ---"

QUERY_FIXTURE="$good_ingress_backends"
expect "previews pointing at their own slot are accepted" accept check_preview_ingress_routing

QUERY_FIXTURE=$(printf 'Ingress|backends\tnchat-prod-preview-green|chat-service-blue')
expect "a preview pointing at the other slot is rejected" reject check_preview_ingress_routing

QUERY_FIXTURE=$(printf 'Ingress|backends\tnchat-prod-preview-green|chat-service')
expect "a preview pointing at a stable Service is rejected" reject check_preview_ingress_routing

QUERY_FIXTURE=$(printf 'Ingress|backends\tnchat-prod-preview-admin-blue|admin-service-green')
expect "an admin preview pointing at the other slot is rejected" reject check_preview_ingress_routing

QUERY_FIXTURE=$(printf 'Ingress|backends\tnchat-prod-preview-admin-blue|admin-service')
expect "an admin preview pointing at the stable admin Service is rejected" reject check_preview_ingress_routing

QUERY_FIXTURE=$(printf 'Ingress|backends\tnchat-prod-preview-blue|postgres-blue')
expect "a preview pointing at an unapproved backend is rejected" reject check_preview_ingress_routing

echo
echo "--- preview coverage ---"

QUERY_FIXTURE=$(printf 'Ingress|name\tnchat-prod-preview-blue
Ingress|name\tnchat-prod-preview-green
Ingress|name\tnchat-prod-preview-admin-blue
Ingress|name\tnchat-prod-preview-admin-green')
expect "all four preview hosts present is accepted" accept check_preview_coverage

QUERY_FIXTURE=$(printf 'Ingress|name\tnchat-prod-preview-blue
Ingress|name\tnchat-prod-preview-green')
expect "a missing admin preview is rejected" reject check_preview_coverage

QUERY_FIXTURE=$(printf 'Ingress|name\tnchat-prod-preview-blue
Ingress|name\tnchat-prod-preview-admin-blue')
expect "a missing green preview is rejected" reject check_preview_coverage

echo
echo "--- preview access control ---"

QUERY_FIXTURE=$(printf 'Ingress|name\tnchat-prod-preview-admin-blue
Ingress|nchat-prod-preview-admin-blue|middlewares\tnchat-prod-preview-allowlist@kubernetescrd
Middleware|preview-allowlist|source-range\t198.51.100.0/24')
expect "an allowlisted preview is accepted" accept check_preview_access_control

QUERY_FIXTURE=$(printf 'Ingress|name\tnchat-prod-preview-admin-green
Ingress|nchat-prod-preview-admin-green|middlewares\tnchat-prod-auth-api-prefix@kubernetescrd
Middleware|preview-allowlist|source-range\t198.51.100.0/24')
expect "an admin preview without the allowlist is rejected" reject check_preview_access_control

QUERY_FIXTURE=$(printf 'Ingress|name\tnchat-prod-preview-blue
Ingress|nchat-prod-preview-blue|middlewares\tnchat-prod-preview-allowlist@kubernetescrd
Middleware|preview-allowlist|source-range\t0.0.0.0/0')
expect "an allowlist open to the world is rejected" reject check_preview_access_control

echo
echo "--- release identity ---"

QUERY_FIXTURE=$(printf 'Deployment|images\tchat-service-blue|ghcr.io/nicrepository/nchat/chat-service@sha256:abc
Deployment|images\tclamav|clamav/clamav:1.4.1')
expect "digest-pinned release images are accepted" accept check_workload_images

QUERY_FIXTURE=$(printf 'Deployment|images\tchat-service-blue|ghcr.io/nicrepository/nchat/chat-service:latest')
expect "a mutable tag is rejected" reject check_workload_images

QUERY_FIXTURE=$(printf 'Deployment|images\tchat-service-blue|ghcr.io/nicrepository/nchat/chat-service:v1.2.3')
expect "a release image with a plain tag is rejected" reject check_workload_images

QUERY_FIXTURE=$(printf 'Deployment|images\tclamav|clamav/clamav:develop')
expect "a mutable tag on a shared image is rejected" reject check_workload_images

echo
echo "--- resource quota dimensions ---"

QUOTA_ALL=$(printf 'ResourceQuota|nchat-prod-quota|hard.requests.cpu	8
ResourceQuota|nchat-prod-quota|hard.requests.memory	16Gi
ResourceQuota|nchat-prod-quota|hard.pods	70
ResourceQuota|nchat-prod-quota|hard.requests.ephemeral-storage	8Gi')

QUERY_FIXTURE="$QUOTA_ALL"
expect "a fully declared quota is accepted" accept check_quota_dimensions

# Omitting the dimension makes the capacity preflight inconclusive, which blocks
# every deploy until someone patches the cluster by hand.
QUERY_FIXTURE=$(grep -v 'ephemeral-storage' <<<"$QUOTA_ALL")
expect "a quota without requests.ephemeral-storage is rejected" reject check_quota_dimensions

QUERY_FIXTURE=$(printf 'ResourceQuota|nchat-prod-quota|hard.requests.cpu	8
ResourceQuota|nchat-prod-quota|hard.requests.memory	16Gi
ResourceQuota|nchat-prod-quota|hard.pods	70
ResourceQuota|nchat-prod-quota|hard.requests.ephemeral-storage	0Gi')
expect "a zero ephemeral-storage quota is rejected" reject check_quota_dimensions

QUERY_FIXTURE=$(printf 'ResourceQuota|nchat-prod-quota|hard.requests.cpu	8
ResourceQuota|nchat-prod-quota|hard.requests.memory	16Gi
ResourceQuota|nchat-prod-quota|hard.pods	70
ResourceQuota|nchat-prod-quota|hard.requests.ephemeral-storage	REPLACE_ME_QUOTA')
expect "a placeholder ephemeral-storage quota is rejected" reject check_quota_dimensions

QUERY_FIXTURE=$(grep -v 'hard.pods' <<<"$QUOTA_ALL")
expect "a quota without a pod limit is rejected" reject check_quota_dimensions

echo
if [ "$FAILURES" -gt 0 ]; then
  echo "manifest gate negative tests failed with $FAILURES failure(s)." >&2
  exit 1
fi
echo "manifest gate negative tests passed."
