#!/usr/bin/env bash
# Create the release lifecycle record in a namespace that is already
# established (issue #1000).
#
#   NCHAT_PROD_CONTEXT=<privileged context> bootstrap-release-state.sh
#
# The administrative half of provisioning, run once per namespace after
# infra/k8s/bootstrap/nchat-prod has been applied and before anything runs as
# nchat-prod-deployer -- bootstrap.sh included, which refuses to start without
# the record. The deploy identity may replace the record but never create it.
#
# Safe to re-run: an existing valid record is left exactly as it is, an invalid
# one or a failed read stops it, and it touches no RBAC, workload or selector.
# The context is required rather than defaulted, because the default is the
# deploy identity; and it must be able to create ConfigMaps, which is asked of
# the API server before anything is confirmed.
set -Eeuo pipefail

: "${NCHAT_PROD_CONTEXT:?name the privileged context that may create the record}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
# shellcheck source=scripts/deploy/nchat-prod/lib.sh
source "$SCRIPT_DIR/lib.sh"
# shellcheck source=scripts/deploy/nchat-prod/release-state.sh
source "$SCRIPT_DIR/release-state.sh"

require_context
require_namespace
require_release_state_creator
confirm "Create the release lifecycle record in $NCHAT_PROD_NAMESPACE if it is absent (an existing record is kept)"
release_state_bootstrap
