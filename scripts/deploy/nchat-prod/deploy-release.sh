#!/usr/bin/env bash
# The supported way to deploy a release by hand (issue #798).
#
#   deploy-release.sh <release-checkout>
#
# Run it from a checkout of the default branch — the deploy CONTROL PLANE —
# naming a separate checkout of the release to deploy. It is the manual twin of
# what CD / Prepare Production does: the control plane's own copy of
# require-release-capability.sh decides whether the release may start at all,
# and only then is the RELEASE's deploy.sh run, from the release checkout.
#
# A release never vouches for itself. Its deploy.sh is whatever that commit
# shipped: a build from before #798 has no idea manual presence exists, and
# would run its migrations and start a notification-service that ignores Do Not
# Disturb — in the idle slot too, which claims from the shared outbox. So the
# question is asked here, by code that knows the rule, before anything of the
# release runs.
#
# This entrypoint did not exist before #798, on purpose under a new name: a
# checkout from before it, mistaken for the control plane, has no such target
# and fails, instead of deploying itself unchecked.
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
# shellcheck source=scripts/deploy/nchat-prod/lib.sh
source "$SCRIPT_DIR/lib.sh"

main() {
  local release_root="${1:-}"
  [[ -n "$release_root" && -f "$release_root/scripts/deploy/nchat-prod/deploy.sh" ]] ||
    prod_fail "usage: deploy-release.sh <release-checkout>  (no scripts/deploy/nchat-prod/deploy.sh under '${release_root}')"
  release_root="$(cd "$release_root" && pwd -P)"
  # Paths the operator gave relative to where they stand keep meaning that
  # once the release's own script runs from the release checkout.
  if [[ -n "${ARTIFACTS_DIR:-}" ]]; then
    ARTIFACTS_DIR="$(cd "$ARTIFACTS_DIR" && pwd -P)" || prod_fail "ARTIFACTS_DIR '$ARTIFACTS_DIR' does not exist"
    export ARTIFACTS_DIR
  fi
  "$SCRIPT_DIR/require-release-capability.sh" "$release_root"
  echo "the control plane accepts $release_root; handing over to the release's deploy.sh."
  cd "$release_root"
  exec bash scripts/deploy/nchat-prod/deploy.sh
}

main "$@"
