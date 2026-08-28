#!/usr/bin/env bash
set -Eeuo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
OUTPUT="${1:?usage: prepare-trivy-config.sh OUTPUT_DIRECTORY}"
KUSTOMIZE_BIN="${KUSTOMIZE_BIN:-kustomize}"
export NCHAT_DEV_TOPOLOGY_FILE="${NCHAT_DEV_TOPOLOGY_FILE:-$ROOT/scripts/ci/testdata/nchat-dev-topology.env}"
# shellcheck source=scripts/deploy/nchat-dev/lib.sh
source "$ROOT/scripts/deploy/nchat-dev/lib.sh"

[[ ! -e "$OUTPUT" && ! -L "$OUTPUT" ]]
command -v "$KUSTOMIZE_BIN" >/dev/null 2>&1
mkdir -m 0700 "$OUTPUT"
prepare_deploy_tree "$ROOT" "$OUTPUT/tree"
NCHAT_DEV_ROOT="$OUTPUT/tree/infra-k8s/overlays/nchat-dev-server"

"$KUSTOMIZE_BIN" build "$NCHAT_DEV_ROOT" >"$OUTPUT/application.yaml"
"$KUSTOMIZE_BIN" build "$NCHAT_DEV_ROOT/data" >"$OUTPUT/data.yaml"
"$KUSTOMIZE_BIN" build "$NCHAT_DEV_ROOT/migrations" >"$OUTPUT/migrations.yaml"
cleanup_deploy_tree "$OUTPUT/tree"

expected_controller_sha="$(<"$ROOT/infra/k8s/security/sealed-secrets/CONTROLLER_SHA256")"
read -r actual_controller_sha _ < <(sha256sum "$ROOT/infra/k8s/security/sealed-secrets/controller/controller.yaml")
[[ "$expected_controller_sha" =~ ^[a-f0-9]{64}$ && "$actual_controller_sha" == "$expected_controller_sha" ]]

cp "$ROOT/Dockerfile.migrations" "$ROOT/Dockerfile.service" "$ROOT/Dockerfile.web" "$OUTPUT/"
cp "$ROOT/infra/k8s/overlays/nchat-dev-server/server/runner-rbac.yaml" "$OUTPUT/runner-rbac.yaml"
cp "$ROOT/infra/k8s/security/sealed-secrets/controller/controller.yaml" "$OUTPUT/controller.yaml"
