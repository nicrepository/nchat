#!/usr/bin/env bash
# Smoke production through the stable Services and record that it passed
# (issue #933).
#
#   record-traffic-smoke.sh --target green --after cutover
#   record-traffic-smoke.sh --target blue  --after rollback
#
# Two things, in one command, because they must not be able to disagree: the
# smoke is `stable-smoke.sh`, unchanged and still the only thing that decides,
# and the record is written only when it exits zero. Nothing else in this
# repository writes `post_cutover_smoke`, and no workflow writes it
# speculatively.
#
# WHY THE RECORD EXISTS. After a cutover the demoted slot is the rollback, and
# the next release retires it once the retention window has passed. The
# question that gate has to answer is "was the release now serving ever proved
# to work?", and readiness cannot answer it: a slot can be entirely Ready, on
# one consistent release, and failing every request. Without this record a
# cutover whose post-cutover smoke failed would still have its rollback retired
# thirty minutes later -- removing the fast way back from precisely the release
# that did not pass.
#
# THIS IS ALSO THE WAY BACK. When a post-cutover smoke fails the workflow fails
# and nothing is recorded, which leaves the lifecycle blocked: the next release
# refuses to retire the rollback. That is deliberate. Once the cause has been
# found and fixed -- or the release rolled back, which clears the record
# another way -- an operator runs this command to re-prove the slot and unblock
# the lifecycle. It is a recovery command, not a third button in the release
# path: it moves no traffic, retires nothing, and cannot make a failing smoke
# pass.
#
# The release recorded is read from the cluster, not passed in. A slot
# redeployed after being smoked carries a different identity, so the evidence
# stops matching and the gate stops honouring it.
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
# shellcheck source=scripts/deploy/nchat-prod/lib.sh
source "$SCRIPT_DIR/lib.sh"
# shellcheck source=scripts/deploy/nchat-prod/release-state.sh
source "$SCRIPT_DIR/release-state.sh"

main() {
  local target release
  target="$(require_target_slot "$@")"
  require_context
  require_namespace
  # The smoke first, with its own arguments passed through exactly as given:
  # `--after` is validated by stable-smoke.sh and refused there if it is
  # neither cutover nor rollback.
  "$SCRIPT_DIR/stable-smoke.sh" "$@"
  # Read after the smoke, so the identity recorded is the one that was just
  # proved rather than one read before it.
  release="$(require_consistent_release "$target")" || return 1
  record_traffic_smoke_passed "$release"
  echo
  echo "Recorded: slot $target passed its post-traffic smoke on release $release."
  echo "The rollback slot may be retired by the next release once the retention"
  echo "window has passed; nothing is retired by this command."
}

main "$@"
