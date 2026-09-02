#!/usr/bin/env bash
# CICD-05: production may only build a commit that main can reach.
#
# A branch name is a label an input can claim; reachability is a property of the
# object graph, so that is what this proves. The tip of main is resolved once
# and printed with the verdict: the decision names the exact reference it was
# taken against, instead of being re-derived later against a main that has moved.
#
# Usage: require-main-sha.sh <sha>
#   NCHAT_RELEASE_MAIN_REF  reference to consider (default refs/remotes/origin/main)
#
# Every gate is fail-closed: an unresolvable ref, an unknown commit and an
# unreachable one are all refusals, never a warning that ends in success.
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../../.." && pwd -P)"
# shellcheck source=scripts/deploy/nchat-dev/lib.sh
source "$ROOT_DIR/scripts/deploy/nchat-dev/lib.sh"

require_main_sha_fail() {
  echo "release SHA gate: $*" >&2
}

resolve_commit() {
  git rev-parse --verify --quiet "$1^{commit}"
}

require_main_sha() {
  local sha="$1" main_ref="$2" main_tip
  if ! validate_commit_sha "$sha"; then
    require_main_sha_fail "the requested SHA is not a 40-character lowercase hexadecimal commit"
    return 1
  fi
  if ! main_tip="$(resolve_commit "$main_ref")"; then
    require_main_sha_fail "$main_ref does not resolve to a commit in this repository"
    return 1
  fi
  if ! resolve_commit "$sha" >/dev/null; then
    require_main_sha_fail "$sha is not a commit in this repository"
    return 1
  fi
  if ! git merge-base --is-ancestor "$sha" "$main_tip"; then
    require_main_sha_fail "$sha is not reachable from $main_ref (tip $main_tip)"
    return 1
  fi
  printf 'Release SHA %s is reachable from %s (tip %s).\n' "$sha" "$main_ref" "$main_tip"
}

main() {
  if [[ "$#" -ne 1 ]]; then
    require_main_sha_fail "usage: require-main-sha.sh <sha>"
    return 1
  fi
  require_main_sha "$1" "${NCHAT_RELEASE_MAIN_REF:-refs/remotes/origin/main}"
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi
