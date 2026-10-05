#!/usr/bin/env bash
# Refuse to prepare a release that cannot honour manual presence once it has
# been enabled in production (issue #798).
#
#   require-release-capability.sh <release-checkout>
#
# This is the deploy CONTROL PLANE's check, not the release's. The preparation
# workflow runs its own YAML from the default branch, but every script it then
# runs comes from the release commit it checked out — so a release from before
# #798 would bring its own deploy scripts, none of which knows this rule. That
# workflow therefore checks out the default branch a second time, beside the
# release, and runs THIS copy against the release's manifests, before the slot
# is reserved and before anything is applied. A guard inside the release's own
# scripts could never protect against the release.
#
# Why before anything starts, and not only before a promotion: an idle slot is
# not inert. Its notification-service claims rows from the outbox both slots
# share, so a build that ignores Do Not Disturb would deliver to people who
# asked for silence without ever receiving traffic.
#
# Fails closed: an unreadable activation record, a release whose manifests do
# not render, or one missing a reader is a refusal.
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
# shellcheck source=scripts/deploy/nchat-prod/lib.sh
source "$SCRIPT_DIR/lib.sh"

main() {
  local release_root="${1:-}" activated
  [[ -n "$release_root" && -d "$release_root/infra/k8s" ]] ||
    prod_fail "usage: require-release-capability.sh <release-checkout>  (no infra/k8s under '${release_root}')"
  require_context
  require_namespace
  activated="$(manual_presence_activated)" ||
    prod_fail "could not read whether manual presence was ever enabled ($NCHAT_PROD_CONFIGMAP); no release is prepared without knowing"
  if [[ -z "$activated" ]]; then
    echo "manual presence was never enabled; any release may be prepared (Phase 1)."
    return 0
  fi
  echo "manual presence enabled since $activated; checking the release's readers."
  require_release_presence_capability "$release_root"
  echo "every manual presence reader of this release carries the capability."
}

main "$@"
