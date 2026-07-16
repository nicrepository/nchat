#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../../.." && pwd -P)"
OUTPUT_DIR="${1:?usage: render-topology-templates.sh OUTPUT_DIR}"
# shellcheck source=scripts/deploy/nchat-dev/topology.sh
source "$SCRIPT_DIR/topology.sh"

if [[ -n "${NCHAT_DEV_TOPOLOGY_FILE:-}" ]]; then
  load_nchat_dev_topology "$NCHAT_DEV_TOPOLOGY_FILE"
else
  load_nchat_dev_topology "$ROOT_DIR/infra/k8s/overlays/nchat-dev-server/topology.env"
fi
[[ -d "$OUTPUT_DIR" && ! -L "$OUTPUT_DIR" ]]
[[ ! -e "$OUTPUT_DIR/livekit.yaml" && ! -L "$OUTPUT_DIR/livekit.yaml" ]]
[[ ! -e "$OUTPUT_DIR/turnserver.conf" && ! -L "$OUTPUT_DIR/turnserver.conf" ]]
render_nchat_dev_topology_template \
  "$ROOT_DIR/infra/k8s/secrets/templates/nchat-dev-livekit.template.yaml" \
  "$OUTPUT_DIR/livekit.yaml"
render_nchat_dev_topology_template \
  "$ROOT_DIR/infra/k8s/secrets/templates/nchat-dev-turnserver.template.conf" \
  "$OUTPUT_DIR/turnserver.conf"
chmod 0600 "$OUTPUT_DIR/livekit.yaml" "$OUTPUT_DIR/turnserver.conf"
