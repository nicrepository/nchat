#!/usr/bin/env bash
# CICD-07: the production runner refuses every job that is not one of the
# production release operations (issues #626, #933).
#
# `nchat-prod-deploy` is a label, and a label is routing, not authorisation:
# any workflow in this public repository can ask for it. What decides whether a
# job may run as nchat-prod-runner -- the only identity that can read the
# least-privilege production kubeconfig -- is this hook, which the runner
# executes through ACTIONS_RUNNER_HOOK_JOB_STARTED before the first step of
# every job it accepts.
#
# Three contexts are authorised, and they are an exact list, not a pattern:
#
#   cd-prepare-production.yml   on the default branch, from workflow_run
#   cutover-nchat-prod.yml      on main, dispatched by hand
#   rollback-nchat-prod.yml     on main, dispatched by hand
#
# Every other repository, workflow, ref and event is a refusal, and so is a
# variable the runner did not set -- an absent value authorises nothing.
#
# WHY PREPARATION IS AUTHORISED FROM THE DEFAULT BRANCH, and it is the one
# concession in this file. A `workflow_run` handler always runs the copy of its
# own YAML that is on the repository's default branch; GitHub offers no way to
# run it from main, and the alternative -- an operator pasting a SHA into a
# dispatch -- is precisely the manual step #933 removes. So the orchestration
# of candidate preparation is authorised from `develop`.
#
# What that concession does not include is the mutation. The preparation
# workflow checks out the release commit and runs deploy.sh, lib.sh and the
# capacity gate from *that* commit, which is on main and has passed
# `CI / Required`; the YAML on develop can order those scripts around but
# cannot change what they do. And preparation moves no traffic at all: cutover
# and rollback are the two operations that patch a stable Service selector,
# both are `workflow_dispatch`, and both are authorised only from main.
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
WORKFLOW_PREFIX="nicrepository/nchat/.github/workflows"
# One record per authorised context: "<workflow ref>|<git ref>|<event>".
# Written out rather than composed from parts, so the whole authorised surface
# of the production runner is nine readable fields in one place.
ALLOWED_CONTEXTS=(
  "$WORKFLOW_PREFIX/cd-prepare-production.yml@refs/heads/develop|refs/heads/develop|workflow_run"
  "$WORKFLOW_PREFIX/cutover-nchat-prod.yml@refs/heads/main|refs/heads/main|workflow_dispatch"
  "$WORKFLOW_PREFIX/rollback-nchat-prod.yml@refs/heads/main|refs/heads/main|workflow_dispatch"
)

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

# The authorised record this job's workflow belongs to, or a refusal.
#
# The workflow ref is the key because it is the one value that carries both the
# file and the branch it was read from, so matching it first means the ref and
# event below are checked against the expectations of *that* workflow rather
# than against a union in which any combination would pass.
context_for_workflow() {
  local actual="$1" record
  for record in "${ALLOWED_CONTEXTS[@]}"; do
    [[ "${record%%|*}" == "$actual" ]] && { printf '%s' "$record"; return 0; }
  done
  return 1
}

main() {
  local record rest
  require_exactly GITHUB_REPOSITORY "$ALLOWED_REPOSITORY" "${GITHUB_REPOSITORY-}"
  record="$(context_for_workflow "${GITHUB_WORKFLOW_REF-}")" || deny GITHUB_WORKFLOW_REF
  rest="${record#*|}"
  require_exactly GITHUB_REF "${rest%%|*}" "${GITHUB_REF-}"
  require_exactly GITHUB_EVENT_NAME "${rest#*|}" "${GITHUB_EVENT_NAME-}"
  printf 'runner job guard: ALLOW, an authorised production release operation.\n'
}

main "$@"
