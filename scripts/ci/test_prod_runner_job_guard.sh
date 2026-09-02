#!/usr/bin/env bash
# Behaviour tests for the production runner pre-job guard (CICD-07).
#
# The guard is the host-side boundary that a label cannot be: it decides, before
# the first step of a job, whether the job may run as the identity that can read
# the production kubeconfig. So what is proved here is refusal, not features.
#
# One context is allowed, and every deviation from it -- a different repository,
# workflow, ref or event, a missing variable, an empty one, a value that merely
# looks like the authorised one -- must exit non-zero. The suite also proves the
# two properties a log-reading operator depends on: the values a workflow chose
# never reach the guard's output, and no shell metacharacter in them is ever
# executed.
set -uo pipefail

# Preparation is checked, not assumed. An unresolved root, or a workspace
# mktemp never created, would leave the marker paths hanging off nothing
# instead of a directory this suite owns -- which would make the injection
# assertions meaningless and point the cleanup at the filesystem root. So each
# step is proved before the next one uses it, and a failure ends the run here
# instead of at the first scenario.
fatal() {
  echo "FAIL: $1" >&2
  exit 1
}

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)" ||
  fatal 'unable to resolve the repository root'
[[ -n "$ROOT" && -d "$ROOT" ]] || fatal 'the resolved repository root is not a directory'
GUARD="$ROOT/scripts/deploy/nchat-prod/runner-job-guard.sh"

WORK="$(mktemp -d "${TMPDIR:-/tmp}/nchat-runner-job-guard-test.XXXXXX")" ||
  fatal 'unable to create the test workspace'
[[ -n "$WORK" && -d "$WORK" ]] || fatal 'the test workspace is not a directory'
trap 'rm -rf -- "$WORK"' EXIT

# Stands in for the runner credentials and kubeconfig path that really are in
# the hook's environment. Nothing the guard prints may contain them.
SECRET_MARKER="AKIA-NOT-A-REAL-RUNNER-TOKEN"
# Any of these firing would create its marker, which is the point of using
# them. Both are children of the workspace proved above. The markers are
# cleared before every scenario, so the scenario whose value was executed is
# the one that fails, and the next one starts clean.
COMMAND_MARKER="$WORK/pwned-semicolon"
SUBSTITUTION_MARKER="$WORK/pwned-substitution"
INJECTION="; touch $COMMAND_MARKER"
SUBSTITUTION="\$(touch $SUBSTITUTION_MARKER)"

failures=0

declare -A AUTHORISED=(
  [GITHUB_REPOSITORY]="nicrepository/nchat"
  [GITHUB_WORKFLOW_REF]="nicrepository/nchat/.github/workflows/deploy-nchat-prod.yml@refs/heads/main"
  [GITHUB_REF]="refs/heads/main"
  [GITHUB_EVENT_NAME]="workflow_dispatch"
)

# Runs the guard in an environment built from the authorised context plus the
# given overrides. `NAME=@absent` removes the variable entirely, which is a
# distinct case from setting it empty.
run_guard() {
  local -A vars=()
  local key spec name value
  for key in "${!AUTHORISED[@]}"; do
    vars["$key"]="${AUTHORISED[$key]}"
  done
  for spec in "$@"; do
    name="${spec%%=*}"
    value="${spec#*=}"
    if [[ "$value" == "@absent" ]]; then
      unset "vars[$name]"
    else
      vars["$name"]="$value"
    fi
  done
  local -a environment=("RUNNER_TOKEN=$SECRET_MARKER" "KUBECONFIG=$SECRET_MARKER")
  for key in "${!vars[@]}"; do
    environment+=("$key=${vars[$key]}")
  done
  env -i "${environment[@]}" bash "$GUARD"
}

# A refusal names the variable that disagreed. Echoing what it said would put
# an untrusted string into the system log an operator reads during an incident,
# so only the values this scenario supplied are looked for -- the guard's own
# static allowlist is text it is entitled to carry.
reflected_value() {
  local output="$1" spec value
  shift
  for spec in "$@"; do
    value="${spec#*=}"
    [[ -z "$value" || "$value" == "@absent" ]] && continue
    [[ "$output" == *"$value"* ]] && echo "the guard echoed the value it rejected"
  done
}

# The contract an operator reads out of the log: a verdict, and for a refusal
# the name of what disagreed. Each returns the first problem it finds, or
# nothing when the contract holds.
allow_contract() {
  local status="$1" output="$2"
  [[ "$status" -eq 0 ]] || {
    echo "exit $status, want 0"
    return
  }
  [[ "$output" == *ALLOW* ]] || echo "the verdict does not say ALLOW"
}

deny_contract() {
  local status="$1" output="$2" field="$3"
  shift 3
  [[ "$status" -ne 0 ]] || {
    echo "the guard allowed the job"
    return
  }
  [[ "$output" == *DENY* ]] || {
    echo "the refusal does not say DENY"
    return
  }
  [[ "$output" == *"$field"* ]] || {
    echo "the refusal does not name $field"
    return
  }
  reflected_value "$output" "$@"
}

# One case. `field` empty means the authorised context, which must be allowed;
# otherwise it is the variable the refusal has to name.
check() {
  local label="$1" field="$2"
  shift 2
  local output status problem
  reset_injection_markers
  output="$(run_guard "$@" 2>&1)"
  status=$?

  if [[ -n "$field" ]]; then
    problem="$(deny_contract "$status" "$output" "$field" "$@")"
  else
    problem="$(allow_contract "$status" "$output")"
  fi
  if [[ -z "$problem" && "$output" == *"$SECRET_MARKER"* ]]; then
    problem="the guard printed environment it must not print"
  fi
  # Per case, not once at the end: a marker proves this scenario's value
  # reached a shell as a command.
  if [[ -z "$problem" ]] && ! injection_markers_absent; then
    problem="the guard executed a value it was given"
  fi
  report "$label" "$problem"
}

report() {
  local label="$1" problem="$2"
  if [[ -n "$problem" ]]; then
    echo "FAIL $label: $problem" >&2
    failures=$((failures + 1))
    return
  fi
  echo "ok   $label"
}

injection_markers_absent() {
  [[ ! -e "$COMMAND_MARKER" && ! -e "$SUBSTITUTION_MARKER" ]]
}

# Clearing them is scenario setup, not a scenario result: if it cannot be done
# the suite has no clean ground to test on, so it stops rather than reporting
# whatever the leftovers make the next assertion say.
reset_injection_markers() {
  rm -f -- "$COMMAND_MARKER" "$SUBSTITUTION_MARKER" || {
    echo "the injection markers could not be cleared; the suite cannot continue" >&2
    exit 1
  }
}

expect_allow() { check "$1" "" "${@:2}"; }
expect_deny() { check "$1" "$2" "${@:3}"; }


# --- the one authorised context ---------------------------------------------

expect_allow "the production deploy dispatched from main is allowed"

# --- repository --------------------------------------------------------------

expect_deny "an absent GITHUB_REPOSITORY is refused" GITHUB_REPOSITORY GITHUB_REPOSITORY=@absent
expect_deny "an empty GITHUB_REPOSITORY is refused" GITHUB_REPOSITORY GITHUB_REPOSITORY=
expect_deny "another repository is refused" GITHUB_REPOSITORY GITHUB_REPOSITORY=nicrepository/other
expect_deny "a fork of this repository is refused" GITHUB_REPOSITORY GITHUB_REPOSITORY=attacker/nchat

# --- workflow ----------------------------------------------------------------

expect_deny "an absent GITHUB_WORKFLOW_REF is refused" GITHUB_WORKFLOW_REF GITHUB_WORKFLOW_REF=@absent
expect_deny "an empty GITHUB_WORKFLOW_REF is refused" GITHUB_WORKFLOW_REF GITHUB_WORKFLOW_REF=
expect_deny "another workflow of this repository is refused" GITHUB_WORKFLOW_REF \
  GITHUB_WORKFLOW_REF=nicrepository/nchat/.github/workflows/ci.yml@refs/heads/main
expect_deny "the deploy workflow on develop is refused" GITHUB_WORKFLOW_REF \
  GITHUB_WORKFLOW_REF=nicrepository/nchat/.github/workflows/deploy-nchat-prod.yml@refs/heads/develop
expect_deny "the deploy workflow on a pull request ref is refused" GITHUB_WORKFLOW_REF \
  GITHUB_WORKFLOW_REF=nicrepository/nchat/.github/workflows/deploy-nchat-prod.yml@refs/pull/123/merge

# --- ref ---------------------------------------------------------------------

expect_deny "an absent GITHUB_REF is refused" GITHUB_REF GITHUB_REF=@absent
expect_deny "an empty GITHUB_REF is refused" GITHUB_REF GITHUB_REF=
expect_deny "develop is refused" GITHUB_REF GITHUB_REF=refs/heads/develop
expect_deny "a pull request ref is refused" GITHUB_REF GITHUB_REF=refs/pull/123/merge
expect_deny "a tag is refused" GITHUB_REF GITHUB_REF=refs/tags/v1.0.0

# --- event -------------------------------------------------------------------

expect_deny "an absent GITHUB_EVENT_NAME is refused" GITHUB_EVENT_NAME GITHUB_EVENT_NAME=@absent
expect_deny "an empty GITHUB_EVENT_NAME is refused" GITHUB_EVENT_NAME GITHUB_EVENT_NAME=
expect_deny "pull_request is refused" GITHUB_EVENT_NAME GITHUB_EVENT_NAME=pull_request
expect_deny "pull_request_target is refused" GITHUB_EVENT_NAME GITHUB_EVENT_NAME=pull_request_target
expect_deny "push is refused" GITHUB_EVENT_NAME GITHUB_EVENT_NAME=push
expect_deny "workflow_call is refused" GITHUB_EVENT_NAME GITHUB_EVENT_NAME=workflow_call

# --- values that only look authorised ----------------------------------------

expect_deny "a repository the authorised one is a prefix of is refused" GITHUB_REPOSITORY \
  GITHUB_REPOSITORY=nicrepository/nchat-staging
expect_deny "a repository that ends with the authorised one is refused" GITHUB_REPOSITORY \
  GITHUB_REPOSITORY=evil/nicrepository/nchat
expect_deny "a ref the authorised one is a prefix of is refused" GITHUB_REF \
  GITHUB_REF=refs/heads/mainline
expect_deny "a ref that merely contains the authorised one is refused" GITHUB_REF \
  GITHUB_REF=refs/heads/feature/refs/heads/main
expect_deny "a differently cased ref is refused" GITHUB_REF GITHUB_REF=refs/heads/MAIN
expect_deny "a workflow path under the authorised one is refused" GITHUB_WORKFLOW_REF \
  GITHUB_WORKFLOW_REF=nicrepository/nchat/.github/workflows/deploy-nchat-prod.yml.bak@refs/heads/main
expect_deny "trailing whitespace is not trimmed away" GITHUB_REF GITHUB_REF="refs/heads/main "

# --- hostile values ----------------------------------------------------------

expect_deny "a repository carrying a command separator is refused" GITHUB_REPOSITORY \
  "GITHUB_REPOSITORY=nicrepository/nchat$INJECTION"
expect_deny "a ref carrying a command substitution is refused" GITHUB_REF \
  "GITHUB_REF=refs/heads/main$SUBSTITUTION"
expect_deny "a workflow ref carrying a substitution is refused" GITHUB_WORKFLOW_REF \
  "GITHUB_WORKFLOW_REF=$SUBSTITUTION"
expect_deny "a glob that would match the authorised repository is refused" GITHUB_REPOSITORY \
  "GITHUB_REPOSITORY=*"
expect_deny "a glob that would match the authorised ref is refused" GITHUB_REF \
  "GITHUB_REF=refs/heads/mai?"
expect_deny "every variable empty is refused" GITHUB_REPOSITORY \
  GITHUB_REPOSITORY= GITHUB_WORKFLOW_REF= GITHUB_REF= GITHUB_EVENT_NAME=
expect_deny "an empty environment is refused" GITHUB_REPOSITORY \
  GITHUB_REPOSITORY=@absent GITHUB_WORKFLOW_REF=@absent GITHUB_REF=@absent \
  GITHUB_EVENT_NAME=@absent

if [[ "$failures" -ne 0 ]]; then
  echo "$failures runner job guard test(s) failed." >&2
  exit 1
fi
echo "Production runner job guard tests passed."
