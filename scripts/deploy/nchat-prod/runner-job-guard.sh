#!/usr/bin/env bash
# CICD-07: the production runner refuses every job that is not the production
# deploy.
#
# `nchat-prod-deploy` is a label, and a label is routing, not authorisation:
# any workflow in this public repository can ask for it. What decides whether a
# job may run as nchat-prod-runner -- the only identity that can read the
# least-privilege production kubeconfig -- is this hook, which the runner
# executes through ACTIONS_RUNNER_HOOK_JOB_STARTED before the first step of
# every job it accepts.
#
# Two contexts are authorised, and only two: the production deploy workflow and
# the production rollback workflow, each as it exists on main and dispatched by
# hand. Every other repository, workflow, ref and event is a refusal, and so is
# a variable the runner did not set -- an absent value authorises nothing.
#
# The rollback workflow is here because the guard is the boundary, not a
# convenience: without an entry of its own, the one procedure that returns
# production to a working slot could not run at all on the only identity that
# can reach the cluster (CICD-08). It is a second entry in a closed list, not a
# relaxation of the comparison -- the match is still exact, and every other
# workflow file, branch and event is refused exactly as before.
#
# It reads four variables and compares them. It resolves no path, runs no
# command built from them, and needs no network, git, kubectl or jq: the
# decision cannot be influenced by anything the job controls.
#
# Installation is host-side and outside the runner's workspace: a root-owned
# copy wired in by its own systemd drop-in, per the production runbook.
# Pointing the hook at the checkout would let the job it judges rewrite it
# first.
set -Eeuo pipefail

ALLOWED_REPOSITORY="nicrepository/nchat"
ALLOWED_WORKFLOW_REFS=(
  "nicrepository/nchat/.github/workflows/deploy-nchat-prod.yml@refs/heads/main"
  "nicrepository/nchat/.github/workflows/rollback-nchat-prod.yml@refs/heads/main"
)
ALLOWED_REF="refs/heads/main"
ALLOWED_EVENT_NAME="workflow_dispatch"

# What disagreed, never what it said. The values are strings an untrusted
# workflow chooses, and this line is read out of a system log.
deny() {
  printf 'runner job guard: DENY, %s is not an authorised production release context.\n' "$1" >&2
  exit 1
}

# One exact comparison: no glob, no prefix, no case folding. A value merely
# shaped like the authorised one is a different value.
require_exactly() {
  local name="$1" allowed="$2" actual="$3"
  [[ "$actual" == "$allowed" ]] || deny "$name"
}

# Membership of the closed list, by the same exact comparison. A value that is
# merely shaped like one of them -- a prefix, another branch, another file in
# the same directory -- is a different value and matches nothing.
require_one_of() {
  local name="$1" actual="$2" allowed
  shift 2
  for allowed in "$@"; do
    [[ "$actual" == "$allowed" ]] && return 0
  done
  deny "$name"
}

main() {
  require_exactly GITHUB_REPOSITORY "$ALLOWED_REPOSITORY" "${GITHUB_REPOSITORY-}"
  require_one_of GITHUB_WORKFLOW_REF "${GITHUB_WORKFLOW_REF-}" "${ALLOWED_WORKFLOW_REFS[@]}"
  require_exactly GITHUB_REF "$ALLOWED_REF" "${GITHUB_REF-}"
  require_exactly GITHUB_EVENT_NAME "$ALLOWED_EVENT_NAME" "${GITHUB_EVENT_NAME-}"
  printf 'runner job guard: ALLOW, an authorised production release workflow from main.\n'
}

main "$@"
