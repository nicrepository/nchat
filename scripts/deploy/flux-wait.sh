#!/usr/bin/env bash
# Wait for Flux to report a commit of an infrastructure repository healthy.
#
#   flux-wait.sh <repository-url> <commit> <status-name>...
#
# Flux writes one commit status per Kustomization on the commit it reconciled,
# named "<namespace>/<kustomization>" (for example nchat-dev/producao), once
# the reconciliation ends: success when the objects were applied and passed
# their health checks, failed when they did not. This asks the Git server for
# those statuses -- the only view of the cluster a runner outside it has --
# and returns once every named one is success. A failed one fails at once;
# none arriving within the timeout fails too.
#
# Required environment:
#   FLUX_STATUS_TOKEN  read_api on that repository only
# Optional:
#   FLUX_WAIT_TIMEOUT_SECONDS  default 1200
#   FLUX_WAIT_POLL_SECONDS     default 15
set -Eeuo pipefail

fail() {
  echo "$*" >&2
  exit 1
}

[[ $# -ge 3 ]] || fail "usage: flux-wait.sh <repository-url> <commit> <status-name>..."
repository="$1" commit="$2"
shift 2
[[ "$repository" =~ ^https://[^/]+/.+ ]] || fail "the repository must be an https URL"
[[ "$commit" =~ ^[a-f0-9]{40}$ ]] || fail "the commit must be a full lowercase SHA"
[[ -n "${FLUX_STATUS_TOKEN:-}" ]] || fail "FLUX_STATUS_TOKEN is required"
timeout="${FLUX_WAIT_TIMEOUT_SECONDS:-1200}"
poll="${FLUX_WAIT_POLL_SECONDS:-15}"

base="${repository%.git}"
path="${base#https://*/}"
host="${base%/"$path"}"
url="$host/api/v4/projects/${path//\//%2F}/repository/commits/$commit/statuses"

statuses="$(mktemp)"
trap 'rm -f "$statuses"' EXIT
deadline=$((SECONDS + timeout))
echo "waiting for Flux to report $* on ${commit:0:12}"
while :; do
  # The token reaches curl through a header file, never through argv.
  curl --silent --show-error --fail --max-time 20 \
    --header @<(printf 'PRIVATE-TOKEN: %s\n' "$FLUX_STATUS_TOKEN") "$url" >"$statuses" ||
    fail "could not read the commit status of ${commit:0:12}"
  done_all=1
  for name in "$@"; do
    state="$(jq -r --arg name "$name" '[.[] | select(.name == $name)][0].status // "pending"' "$statuses")"
    case "$state" in
      success) ;;
      failed | canceled)
        fail "Flux reported $name $state on ${commit:0:12}: $(jq -r --arg name "$name" '[.[] | select(.name == $name)][0].description' "$statuses")"
        ;;
      *) done_all=0 ;;
    esac
  done
  if [[ "$done_all" -eq 1 ]]; then
    echo "Flux applied ${commit:0:12}: $* healthy"
    exit 0
  fi
  ((SECONDS < deadline)) || fail "Flux did not report $* on ${commit:0:12} within ${timeout}s"
  sleep "$poll"
done
