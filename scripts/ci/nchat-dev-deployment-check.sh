#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
TOPOLOGY_FIXTURE="$ROOT_DIR/scripts/ci/testdata/nchat-dev-topology.env"
export NCHAT_DEV_TOPOLOGY_FILE="${NCHAT_DEV_TOPOLOGY_FILE:-$TOPOLOGY_FIXTURE}"
# shellcheck source=scripts/deploy/nchat-dev/lib.sh
source "$ROOT_DIR/scripts/deploy/nchat-dev/lib.sh"

TEMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/nchat-deploy-check.XXXXXX")"
trap 'cleanup_deploy_tree "$TEMP_DIR"' EXIT
ARTIFACTS="$TEMP_DIR/artifacts"
DIGEST='sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'

fail() {
  echo "nchat-dev deployment check failed: $*" >&2
  exit 1
}

make_valid_artifacts() {
  local image
  cleanup_deploy_tree "$ARTIFACTS"
  mkdir -p "$ARTIFACTS"
  for image in "${NCHAT_DEV_IMAGES[@]}"; do
    printf '%s' "$DIGEST" >"$ARTIFACTS/digest-$image.txt"
  done
}

expect_invalid_artifacts() {
  if validate_digest_artifacts "$ARTIFACTS"; then
    fail "$1"
  fi
}

make_topology_variant() {
  local destination="$1" changed_key="$2" changed_value="$3" key value
  while IFS='=' read -r key value || [[ -n "$key$value" ]]; do
    if [[ "$key" == "$changed_key" ]]; then
      value="$changed_value"
    fi
    printf '%s=%s\n' "$key" "$value"
  done <"$TOPOLOGY_FIXTURE" >"$destination"
}

expect_invalid_topology() {
  if (load_nchat_dev_topology "$1") >/dev/null 2>&1; then
    fail "$2"
  fi
}

validate_topology_contract() {
  local variant="$TEMP_DIR/topology-variant.env" materialized="$TEMP_DIR/materialized.env"
  load_nchat_dev_topology "$TOPOLOGY_FIXTURE" || fail "valid topology fixture rejected"

  make_topology_variant "$variant" NCHAT_DEV_NODE_IP 999.0.2.10
  expect_invalid_topology "$variant" "invalid IPv4 accepted"
  make_topology_variant "$variant" NCHAT_DEV_NODE_CIDR 192.0.2.0/24
  expect_invalid_topology "$variant" "non-/32 node CIDR accepted"
  make_topology_variant "$variant" TURN_LISTEN_PORT 3478
  expect_invalid_topology "$variant" "reserved port 3478 accepted"
  make_topology_variant "$variant" TURN_RELAY_MIN_PORT 50000
  expect_invalid_topology "$variant" "inverted relay range accepted"
  make_topology_variant "$variant" NCHAT_DEV_HOST $'invalid\nhost'
  expect_invalid_topology "$variant" "newline in topology value accepted"
  cp "$TOPOLOGY_FIXTURE" "$variant"
  printf '%s\n' 'UNEXPECTED_KEY=value' >>"$variant"
  expect_invalid_topology "$variant" "unexpected topology key accepted"

  (
    unset NCHAT_DEV_TOPOLOGY_FILE
    export NCHAT_DEV_NODE_IP=192.0.2.20
    export NCHAT_DEV_NODE_CIDR=192.0.2.20/32
    export NCHAT_DEV_HOST=nchat-dev-ci.example.invalid
    materialize_nchat_dev_topology \
      "$ROOT_DIR/infra/k8s/overlays/nchat-dev-server/topology.env.example" "$materialized"
  ) || fail "environment topology materialization failed"
  [[ "$(stat -c '%a' "$materialized")" == 600 ]] || fail "materialized topology permissions are not 0600"
  load_nchat_dev_topology "$materialized" || fail "materialized topology is invalid"
  if (
    unset NCHAT_DEV_TOPOLOGY_FILE NCHAT_DEV_NODE_IP NCHAT_DEV_NODE_CIDR NCHAT_DEV_HOST
    materialize_nchat_dev_topology \
      "$ROOT_DIR/infra/k8s/overlays/nchat-dev-server/topology.env.example" "$TEMP_DIR/missing.env"
  ) >/dev/null 2>&1; then
    fail "missing operational topology was accepted"
  fi

  git -C "$ROOT_DIR" check-ignore -q infra/k8s/overlays/nchat-dev-server/topology.env || fail "local topology is not ignored"
  ! git -C "$ROOT_DIR" ls-files --error-unmatch infra/k8s/overlays/nchat-dev-server/topology.env >/dev/null 2>&1 || fail "real topology is tracked"
  [[ -f "$ROOT_DIR/infra/k8s/overlays/nchat-dev-server/topology.env.example" ]] || fail "topology example is missing"
  ! git -C "$ROOT_DIR" check-ignore -q infra/k8s/overlays/nchat-dev-server/topology.env.example || fail "topology example is ignored"
}

validate_image_inventory() {
  local service image deployment invalid_inventory="$TEMP_DIR/invalid-images.txt"
  local -a discovered=()
  for service in "$ROOT_DIR"/services/*; do
    [[ -d "$service/cmd/$(basename "$service")" ]] || continue
    discovered+=("$(basename "$service")")
  done
  [[ "$(printf '%s\n' "${discovered[@]}" | LC_ALL=C sort)" == \
      "$(printf '%s\n' "${NCHAT_DEV_GO_SERVICES[@]}" | LC_ALL=C sort)" ]] || fail "Go service inventory drift"
  grep -Fq 'fromJSON(needs.inventory.outputs.images)' "$ROOT_DIR/.github/workflows/images.yml" || fail "image workflow does not derive its matrix"
  ! grep -q 'workflow_dispatch:' "$ROOT_DIR/.github/workflows/images.yml" || fail "unprotected manual image publishing is enabled"
  ! grep -Eq 'for image in (web|auth-service)' "$ROOT_DIR/scripts/deploy/nchat-dev/deploy.sh" || fail "deploy duplicates the image inventory"

  for image in "${NCHAT_DEV_RUNTIME_IMAGES[@]}"; do
    [[ "$(grep -Fxc "  - name: ghcr.io/nicrepository/nchat/$image" "$ROOT_DIR/infra/k8s/overlays/nchat-dev-server/kustomization.yaml")" -eq 1 ]] || fail "Kustomize image missing or duplicated: $image"
  done
  [[ "$(grep -Fxc '  - name: ghcr.io/nicrepository/nchat/migrations' "$ROOT_DIR/infra/k8s/overlays/nchat-dev-server/migrations/kustomization.yaml")" -eq 1 ]] || fail "migration Kustomize image drift"

  for deployment in "${NCHAT_DEV_APPLICATION_DEPLOYMENTS[@]}"; do
    if [[ "$deployment" == nchat-web ]]; then
      service="$ROOT_DIR/infra/k8s/base/web/service.yaml"
    else
      service="$ROOT_DIR/infra/k8s/base/services/$deployment/service.yaml"
    fi
    [[ -f "$service" ]] || fail "Service manifest missing for $deployment"
    [[ "$(grep -Ec '^[[:space:]]*- name: http$' "$service" || true)" -eq 1 ]] || fail "Service $deployment must expose exactly one named http port"
  done
  for service in "${NCHAT_DEV_GO_SERVICES[@]}"; do
    grep -Fq "COPY services/$service/go.mod services/$service/go.sum ./services/$service/" "$ROOT_DIR/Dockerfile.service" || fail "Docker metadata layer missing $service"
  done
  grep -Fq 'COPY libs/go ./libs/go' "$ROOT_DIR/Dockerfile.service" || fail "Docker build does not include all shared Go modules"
  ! grep -Eq '^COPY services([[:space:]]|/)[[:space:]]+\.?/services' "$ROOT_DIR/Dockerfile.service" || fail "Docker build copies every service source"
  ! grep -Eq ':808[0-9]' "$ROOT_DIR/scripts/deploy/nchat-dev/deploy.sh" || fail "smoke tests duplicate HTTP port numbers"
  grep -Fq "services/http:\$service:http/proxy/healthz" "$ROOT_DIR/scripts/deploy/nchat-dev/deploy.sh" || fail "smoke tests do not use the named http port"

  cp "$NCHAT_DEV_IMAGE_INVENTORY" "$invalid_inventory"
  printf '%s\n' 'go auth-service auth-service' >>"$invalid_inventory"
  if (load_nchat_dev_image_inventory "$invalid_inventory") >/dev/null 2>&1; then
    fail "duplicate image inventory entry was accepted"
  fi
}

validate_commit_sha aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa || fail "valid commit SHA rejected"
validate_commit_sha 'abc' && fail "invalid commit SHA accepted"
validate_topology_contract
validate_image_inventory
if (cleanup_deploy_tree /) >/dev/null 2>&1; then
  fail "cleanup accepted the filesystem root"
fi

make_valid_artifacts
validate_digest_artifacts "$ARTIFACTS" || fail "valid digest artifact set rejected"

printf '%s ' "$DIGEST" >"$ARTIFACTS/digest-web.txt"
expect_invalid_artifacts "digest containing a space was accepted"
make_valid_artifacts
printf '%s\n' "$DIGEST" >"$ARTIFACTS/digest-web.txt"
expect_invalid_artifacts "digest containing a newline was accepted"
make_valid_artifacts
printf '%s' 'sha256:not-a-digest' >"$ARTIFACTS/digest-web.txt"
expect_invalid_artifacts "invalid digest was accepted"
make_valid_artifacts
rm "$ARTIFACTS/digest-web.txt"
expect_invalid_artifacts "missing digest was accepted"
make_valid_artifacts
printf '%s' "$DIGEST" >"$ARTIFACTS/unexpected.txt"
expect_invalid_artifacts "unexpected artifact was accepted"
make_valid_artifacts
rm "$ARTIFACTS/digest-web.txt"
ln -s digest-auth-service.txt "$ARTIFACTS/digest-web.txt"
expect_invalid_artifacts "digest symlink was accepted"

success_cleanup="$TEMP_DIR/success-cleanup"
prepare_deploy_tree "$ROOT_DIR" "$success_cleanup"
cleanup_deploy_tree "$success_cleanup"
[[ ! -e "$success_cleanup" ]] || fail "cleanup after success failed"

error_cleanup="$TEMP_DIR/error-cleanup"
if (trap 'cleanup_deploy_tree "$error_cleanup"' EXIT; prepare_deploy_tree "$ROOT_DIR" "$error_cleanup"; false); then
  fail "error cleanup fixture unexpectedly succeeded"
fi
[[ ! -e "$error_cleanup" ]] || fail "cleanup after error failed"

if command -v kustomize >/dev/null 2>&1; then
  before="$(git -C "$ROOT_DIR" diff --binary | sha256sum)"
  make_valid_artifacts
  render_root="$TEMP_DIR/render"
  prepare_deploy_tree "$ROOT_DIR" "$render_root"
  application="$render_root/infra-k8s/overlays/nchat-dev-server"
  migrations="$application/migrations"
  set_digest_image "$migrations" ghcr.io/nicrepository/nchat/migrations "$ARTIFACTS/digest-migrations.txt"
  for image in "${NCHAT_DEV_RUNTIME_IMAGES[@]}"; do
    set_digest_image "$application" "ghcr.io/nicrepository/nchat/$image" "$ARTIFACTS/digest-$image.txt"
  done
  validate_rendered_overlay "$migrations" "$TEMP_DIR/migrations.yaml"
  validate_rendered_overlay "$application" "$TEMP_DIR/application.yaml"
  [[ "$(grep -Fc "@$DIGEST" "$TEMP_DIR/application.yaml")" -eq 8 ]] || fail "application images are not digest-pinned"
  [[ "$(grep -Fc "@$DIGEST" "$TEMP_DIR/migrations.yaml")" -eq 1 ]] || fail "migration image is not digest-pinned"

  sidecar="$TEMP_DIR/sidecar"
  mkdir -p "$sidecar"
  cp "$ROOT_DIR/scripts/ci/testdata/nchat-dev-sidecar/deployment.yaml" "$sidecar/deployment.yaml"
  cp "$ROOT_DIR/scripts/ci/testdata/nchat-dev-sidecar/kustomization.yaml" "$sidecar/kustomization.yaml"
  cp "$ROOT_DIR/infra/k8s/overlays/nchat-dev-server/patches/auth-service.yaml" "$sidecar/auth-service-patch.yaml"
  kustomize build "$sidecar" >"$TEMP_DIR/sidecar.yaml"
  if grep -A 8 'name: sidecar' "$TEMP_DIR/sidecar.yaml" | grep -q DATABASE_URL; then
    fail "sidecar received application secrets"
  fi
  grep -B 8 -A 35 'name: auth-service' "$TEMP_DIR/sidecar.yaml" | grep -q DATABASE_URL || fail "named application container was not patched"
  after="$(git -C "$ROOT_DIR" diff --binary | sha256sum)"
  [[ "$before" == "$after" ]] || fail "deployment rendering changed the working tree"
else
  echo "info: standalone kustomize unavailable; digest render and sidecar checks skipped" >&2
fi

echo "nchat-dev deployment checks passed."
