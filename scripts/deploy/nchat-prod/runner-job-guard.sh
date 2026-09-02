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
# One context is authorised: the production deploy workflow, as it exists on
# main, dispatched by hand. Every other repository, workflow, ref and event is
# a refusal, and so is a variable the runner did not set -- an absent value
# authorises nothing.
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
ALLOWED_WORKFLOW_REF="nicrepository/nchat/.github/workflows/deploy-nchat-prod.yml@refs/heads/main"
ALLOWED_REF="refs/heads/main"
ALLOWED_EVENT_NAME="workflow_dispatch"

# What disagreed, never what it said. The values are strings an untrusted
# workflow chooses, and this line is read out of a system log.
deny() {
  printf 'runner job guard: DENY, %s is not the authorised production deploy context.\n' "$1" >&2
  exit 1
}

# One exact comparison: no glob, no prefix, no case folding. A value merely
# shaped like the authorised one is a different value.
require_exactly() {
  local name="$1" allowed="$2" actual="$3"
  [[ "$actual" == "$allowed" ]] || deny "$name"
}

main() {
  require_exactly GITHUB_REPOSITORY "$ALLOWED_REPOSITORY" "${GITHUB_REPOSITORY-}"
  require_exactly GITHUB_WORKFLOW_REF "$ALLOWED_WORKFLOW_REF" "${GITHUB_WORKFLOW_REF-}"
  require_exactly GITHUB_REF "$ALLOWED_REF" "${GITHUB_REF-}"
  require_exactly GITHUB_EVENT_NAME "$ALLOWED_EVENT_NAME" "${GITHUB_EVENT_NAME-}"
  printf 'runner job guard: ALLOW, the production deploy from main.\n'
}

main "$@"
