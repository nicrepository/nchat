#!/usr/bin/env bash
# The release lifecycle record, and nothing else (issue #933).
#
# Blue/Green has always been derivable from the cluster: the stable Service
# selectors say which slot is live, and the release annotations say what each
# slot is running. That remains the source of truth and this file does not
# compete with it.
#
# What the cluster cannot say is *when* and *why*. Once candidate preparation
# and cutover became two separate workflows separated by a human, three facts
# stopped having anywhere to live:
#
#   - which slot the last preparation validated, and which release it validated
#     on it, so a cutover can refuse a candidate that was replaced since;
#   - when the last cutover happened, so the slot it demoted can be protected
#     for a retention window instead of being overwritten by the next release;
#   - whether the release now serving actually passed its post-traffic smoke,
#     because a release that did not must not have its rollback retired;
#   - which build run sealed the manifest, so a cutover can fetch the same bytes
#     without an operator copying a run id.
#
# They live here, in one ConfigMap in nchat-prod, written whole and read whole.
# One object rather than ten Service annotations: this is a single logical
# record, and ten copies of it is nine opportunities to disagree.
#
# TRUST BOUNDARY, and it is the important part. This record is a *claim*. It is
# never the reason an operation is allowed. Every consumer re-reads the cluster
# and proves the claim against it -- require_slot_release_identity for the
# release, collect_service_slots for the selectors, slot_ready for readiness --
# and a record that contradicts the cluster fails the operation instead of
# deciding it. The record can make a permitted operation *refused* (a stale
# candidate, an unexpired retention window); it can never make a refused
# operation permitted. Deleting it therefore blocks the pipeline; it does not
# open it.
#
# Sourcing this file only defines functions.

NCHAT_PROD_RELEASE_STATE_CONFIGMAP='nchat-release-state'
NCHAT_PROD_RELEASE_STATE_SCHEMA='nchat-prod-release-state/v1'
# Every key a caller may write. Writes replace the whole record, so the set is
# closed: a key that is not here cannot be written, and a key that is here is
# always present even when its value is empty. `schema` is deliberately absent
# -- release_state_write stamps it and no caller may choose it.
NCHAT_PROD_RELEASE_STATE_KEYS=(
  candidate_slot
  candidate_release
  candidate_ready_at
  prepare_run_id
  active_slot
  cutover_at
  rollback_reserved_slot
  post_cutover_smoke
)
# Thirty minutes. Long enough for a regression to surface in the metrics an
# operator actually watches after a release; short enough that a daily release
# cadence is not blocked by the previous day's reservation. It is a starting
# point for operational tuning, not a constant of the system -- set
# NCHAT_PROD_ROLLBACK_RETENTION_SECONDS on the production environment to change
# it.
#
# ":-1800", so an empty setting falls back too. A GitHub Environment variable
# that has not been created expands to the empty string, not to nothing, and a
# repository that has never set this one must get the documented default rather
# than a refusal from bounded_seconds. That is the trade #933 section 17
# permits: absence is a documented default, and every other malformed value is
# still an error.
NCHAT_PROD_ROLLBACK_RETENTION_SECONDS="${NCHAT_PROD_ROLLBACK_RETENTION_SECONDS:-1800}"

rollback_retention_seconds() {
  bounded_seconds NCHAT_PROD_ROLLBACK_RETENTION_SECONDS \
    "$NCHAT_PROD_ROLLBACK_RETENTION_SECONDS"
}

release_state_now() {
  date -u +%Y-%m-%dT%H:%M:%SZ
}

# --- reading, and the three answers it can give ---------------------------
#
# A reader that can only say "here is the record, possibly empty" is the bug
# this replaced. Three situations have to stay apart, because only one of them
# is a namespace that has not had its first release yet:
#
#   the ConfigMap does not exist        -> bootstrap; a caller may proceed
#   the ConfigMap exists                -> parse it, validate it, use it
#   the read failed                     -> refuse; nothing is known
#
# Collapsing the third into the first is how a transient API error, an expired
# credential or a wrong context becomes "no rollback is reserved", and from
# there a drain of the slot that was the way back.

# The exit status of release_state_read when the ConfigMap genuinely is not
# there. Deliberately not 1: 1 is what a failed read returns, and the whole
# point is that the caller can tell them apart. Named rather than written as a
# number at the call sites.
NCHAT_PROD_RELEASE_STATE_ABSENT=3

# The whole record as "key=value" lines.
#
#   exit 0                                 the record, on stdout
#   exit $NCHAT_PROD_RELEASE_STATE_ABSENT  no ConfigMap; nothing on stdout
#   exit 1                                 the read failed; nothing is known
#
# `--ignore-not-found` is what makes the distinction deterministic without
# parsing an error message: kubectl exits 0 and prints nothing when the object
# is not there, and keeps its non-zero exit for every other failure -- no
# context, no credential, no API, no permission. Matching "NotFound" in stderr
# would be a string comparison against a message nobody promised us.
#
# An existing ConfigMap whose `data` is empty also prints nothing, so emptiness
# alone is not absence. That is why the status carries the answer and the text
# does not: `release_state_shape_problem` refuses an empty record that was
# reported as present.
release_state_read() {
  local record status=0
  record="$(kubectl get configmap "$NCHAT_PROD_RELEASE_STATE_CONFIGMAP" \
    -n "$NCHAT_PROD_NAMESPACE" --ignore-not-found -o go-template='
{{- range $k, $v := .data }}{{ $k }}={{ $v }}
{{ end -}}')" || status=$?
  if ((status != 0)); then
    echo "the release lifecycle record could not be read from the cluster (kubectl exited $status)" >&2
    return 1
  fi
  [[ -z "$record" ]] || { printf '%s' "$record"; return 0; }
  # Empty output: either the object is not there, or it is there with empty
  # `data`. The second read answers that, and its own failure is a third
  # answer -- not a vote for absence.
  release_state_configmap_exists
  case "$?" in
    0) printf '' ;;
    1) return "$NCHAT_PROD_RELEASE_STATE_ABSENT" ;;
    *)
      echo "the release lifecycle record could not be confirmed present or absent" >&2
      return 1
      ;;
  esac
}

# Asked only to tell an empty `data` from a missing object, which the
# go-template render cannot: both come back as no output.
#
#   0   the ConfigMap exists
#   1   it does not
#   2   the question could not be answered
#
# Three answers rather than two, because this is the second read and a failure
# in it is exactly as uninformative as a failure in the first. Written as a
# capture-then-test rather than `| grep -q .`: a pipeline collapses "kubectl
# failed" and "kubectl printed nothing" into the same non-zero status, which
# would make an unreadable cluster look like a namespace awaiting its first
# release.
#
# It is a second call, and that is acceptable here precisely because it decides
# nothing on its own -- it disambiguates a reading that is already in hand, and
# the worst a race between the two can produce is a refusal.
release_state_configmap_exists() {
  local names
  names="$(kubectl get configmap "$NCHAT_PROD_RELEASE_STATE_CONFIGMAP" \
    -n "$NCHAT_PROD_NAMESPACE" --ignore-not-found -o name)" || return 2
  [[ -n "$names" ]]
}

# The record a transition is about to carry forward.
#
# A failed read must not reach the writer: carrying forward what could not be
# read would write empty strings over `active_slot`, `cutover_at` and the
# rollback reservation, destroying exactly the state the next release depends
# on. An absent record is refused too, and named: the writer can only replace
# a record the privileged bootstrap created (issue #1000), so there is nothing
# to carry forward into.
release_state_read_for_update() {
  local record status=0
  record="$(release_state_read)" || status=$?
  case "$status" in
    0) printf '%s' "$record" ;;
    "$NCHAT_PROD_RELEASE_STATE_ABSENT")
      release_state_explain_absent
      return 1
      ;;
    *) return 1 ;;
  esac
}

release_state_explain_absent() {
  echo "the release lifecycle record $NCHAT_PROD_RELEASE_STATE_CONFIGMAP does not exist. An administrator" >&2
  echo "creates it once, with scripts/deploy/nchat-prod/bootstrap-release-state.sh, before anything" >&2
  echo "runs as the deploy identity, which may only replace it" >&2
}

# Whether a key is present at all, which is not the same question as whether
# it has a value. `candidate_slot=` is "there is no candidate"; no
# `candidate_slot` line at all is a record somebody else wrote, or one this
# pipeline failed half-way through writing.
release_state_has_key() {
  local record="$1" key="$2"
  grep -q "^$key=" <<<"$record"
}

# One field of a record already read, so a consumer that needs four fields reads
# the cluster once rather than four times and cannot see two different records.
release_state_field() {
  local record="$1" key="$2"
  sed -n "s/^$key=//p" <<<"$record" | head -1
}

# Replaces the record wholesale, as one request.
#
# A JSON Patch whose single operation replaces `/data`. It was
# `create --dry-run | apply`, and apply is the wrong verb for an object the
# writer must never create: on a missing ConfigMap it turns into a CREATE,
# which is exactly the request production refused (issue #1000), and on an
# object created without its last-applied annotation it merges rather than
# replaces, keeping any key the new document leaves out. The patch needs only
# `patch` on this one named object, fails NotFound instead of creating one, and
# replaces every key at once: the API applies a patch as a single update, so no
# failure can leave half a transition written. A caller that wants to keep a
# field passes it back in, visibly.
release_state_write() {
  local document patch
  document="$(release_state_document "$@")" || return 1
  patch="$(jq -c '[{op: "replace", path: "/data", value: .data}]' <<<"$document")" || return 1
  kubectl patch configmap "$NCHAT_PROD_RELEASE_STATE_CONFIGMAP" \
    -n "$NCHAT_PROD_NAMESPACE" --type=json -p "$patch"
}

# The whole record as a ConfigMap document, shared by the bootstrap's one CREATE
# and every later write, so the two cannot disagree about the contract.
#
# Every value is passed through --from-literal, never interpolated into a
# manifest, so nothing a value contains can become YAML; `--dry-run=client`
# renders locally and asks the API for nothing.
release_state_document() {
  release_state_pairs_complete "$@" || return 1
  kubectl create configmap "$NCHAT_PROD_RELEASE_STATE_CONFIGMAP" \
    -n "$NCHAT_PROD_NAMESPACE" \
    --from-literal="schema=$NCHAT_PROD_RELEASE_STATE_SCHEMA" \
    "${@/#/--from-literal=}" \
    --dry-run=client -o json
}

# Every contract key, each exactly once. The record is replaced whole, so a
# missing key would be deleted from it and a repeated one is ambiguous.
release_state_pairs_complete() {
  local pair key seen=" "
  for pair in "$@"; do
    [[ "$pair" == *=* ]] || { echo "release state pair is not key=value: '$pair'" >&2; return 1; }
    key="${pair%%=*}"
    release_state_key_is_known "$key" || return 1
    [[ "$seen" != *" $key "* ]] || { echo "release state key given twice: '$key'" >&2; return 1; }
    seen+="$key "
  done
  [[ "$#" -eq "${#NCHAT_PROD_RELEASE_STATE_KEYS[@]}" ]] ||
    { echo "release state must carry every contract key; got $# of ${#NCHAT_PROD_RELEASE_STATE_KEYS[@]}" >&2; return 1; }
}

release_state_key_is_known() {
  local key="$1" known
  for known in "${NCHAT_PROD_RELEASE_STATE_KEYS[@]}"; do
    [[ "$key" == "$known" ]] && return 0
  done
  echo "unknown release state key: '$key'" >&2
  return 1
}

# --- provisioning (issue #1000) --------------------------------------------
#
# The deploy identity may replace this record and may not create it: `create`
# cannot be narrowed to one object name, so granting it would mean any
# ConfigMap. The record is therefore created once, by an administrator running
# bootstrap-release-state.sh, and from then on only ever replaced. Everything
# that runs as the deploy identity -- bootstrap.sh included -- only checks that
# this was done, with require_provisioned_release_state.

# The record exists, can be read, and is structurally valid: the precondition
# for anything the deploy identity is about to do in production.
#
# Read-only, and meant to run before the first mutation. Without it an
# unprovisioned namespace was discovered at the end, by the first write, after
# the release had already been deployed.
require_provisioned_release_state() {
  local record status=0
  record="$(release_state_read)" || status=$?
  case "$status" in
    0) require_valid_release_state "$record" ;;
    "$NCHAT_PROD_RELEASE_STATE_ABSENT")
      release_state_explain_absent
      return 1
      ;;
    *) return 1 ;;
  esac
}

# What a present record must satisfy on its own; shared by the preflight above
# and by the administrative bootstrap, so the two cannot disagree about "valid".
require_valid_release_state() {
  local record="$1"
  require_release_state_contract "$record" || return 1
  require_record_candidate_complete "$record"
}

# The identity running the administrative bootstrap can create ConfigMaps here.
#
# Proved by asking the API server, not inferred from a context name: a context
# is a string anyone can name anything. `kubectl auth can-i` answers "yes" and
# exits 0 only when the request would be authorised; "no", an error and an
# unreachable API all fail this, before anything is confirmed or written.
#
# The deploy context is refused by name as well, only because running this with
# it is an obvious slip worth a clearer message; the check that decides is the
# second one.
require_release_state_creator() {
  local answer
  [[ "$NCHAT_PROD_CONTEXT" != nchat-prod-deployer ]] ||
    prod_fail "the lifecycle bootstrap cannot run as nchat-prod-deployer, which may replace $NCHAT_PROD_RELEASE_STATE_CONFIGMAP but never create it; use an administrative context"
  answer="$(kubectl auth can-i create configmaps -n "$NCHAT_PROD_NAMESPACE")" || answer=""
  [[ "$answer" == yes ]] ||
    prod_fail "context $NCHAT_PROD_CONTEXT cannot create ConfigMaps in $NCHAT_PROD_NAMESPACE; the lifecycle bootstrap requires an administrative identity that can create the initial $NCHAT_PROD_RELEASE_STATE_CONFIGMAP"
}

# The administrative bootstrap. Safe to re-run against a live namespace, and
# that is the point of the four answers below. Applying an empty record every
# time would silently wipe a prepared candidate and the rollback reservation;
# this creates only what is absent and never writes over what is present.
#
#   absent             create the empty record the contract defines
#   present, valid     leave it exactly as it is
#   present, invalid   refuse: a record this pipeline did not write, or wrote
#                      and died, is a question for a person, not for a reset
#   unreadable         refuse: a failed read is not absence
release_state_bootstrap() {
  local record status=0
  record="$(release_state_read)" || status=$?
  case "$status" in
    0) release_state_bootstrap_keep "$record" ;;
    "$NCHAT_PROD_RELEASE_STATE_ABSENT") release_state_bootstrap_create ;;
    *) return 1 ;;
  esac
}

release_state_bootstrap_keep() {
  require_valid_release_state "$1" || return 1
  echo "release lifecycle record $NCHAT_PROD_RELEASE_STATE_CONFIGMAP is valid; left unchanged"
}

# `kubectl create`, never apply: if something else created the record since it
# was read, this fails AlreadyExists instead of overwriting it.
release_state_bootstrap_create() {
  local key document initial=()
  for key in "${NCHAT_PROD_RELEASE_STATE_KEYS[@]}"; do
    initial+=("$key=")
  done
  document="$(release_state_document "${initial[@]}")" || return 1
  kubectl create -n "$NCHAT_PROD_NAMESPACE" -f - <<<"$document" || return 1
  echo "release lifecycle record $NCHAT_PROD_RELEASE_STATE_CONFIGMAP created, empty"
}

# --- shape ----------------------------------------------------------------

# Every key the contract requires a present record to carry, `schema` included.
# The set is closed in both directions: a key missing is a record nobody
# finished writing, and a key nobody expected is a record something else wrote.
NCHAT_PROD_RELEASE_STATE_REQUIRED_KEYS=(schema "${NCHAT_PROD_RELEASE_STATE_KEYS[@]}")

# What is wrong with the shape of a non-empty record, or nothing when it is
# well formed. Returns the reason on stdout so the caller decides how loudly to
# say it. An empty record is the caller's own first check: it is a distinct
# condition with a clearer name than "every key is missing".
#
# Shape only. Whether the values agree with the cluster is a different
# question, asked by lifecycle.sh against a reading of that cluster; a parser
# that also knew about slots and selectors would be two things.
release_state_shape_problem() {
  local record="$1" key missing_keys=() unexpected_keys=()
  for key in "${NCHAT_PROD_RELEASE_STATE_REQUIRED_KEYS[@]}"; do
    release_state_has_key "$record" "$key" || missing_keys+=("$key")
  done
  while IFS= read -r key; do
    [[ -n "$key" ]] || continue
    release_state_key_is_required "$key" || unexpected_keys+=("$key")
  done < <(sed -n 's/=.*//p' <<<"$record")
  release_state_describe_shape "${#missing_keys[@]}" "${missing_keys[*]-}" \
    "${#unexpected_keys[@]}" "${unexpected_keys[*]-}"
}

release_state_key_is_required() {
  local key="$1" known
  for known in "${NCHAT_PROD_RELEASE_STATE_REQUIRED_KEYS[@]}"; do
    [[ "$key" == "$known" ]] && return 0
  done
  return 1
}

# Split out so the check above stays one loop per question rather than one
# function with four branches of prose in it.
release_state_describe_shape() {
  local missing_count="$1" missing="$2" unexpected_count="$3" unexpected="$4"
  if ((missing_count > 0)); then
    printf 'the record is missing required key(s): %s' "$missing"
    return 0
  fi
  if ((unexpected_count > 0)); then
    printf 'the record carries key(s) the contract does not define: %s' "$unexpected"
  fi
}

# The schema of a present record, which decides whether the key set above is
# the right one to be checking at all.
release_state_schema_problem() {
  local record="$1" schema
  release_state_has_key "$record" schema ||
    { printf 'the record declares no schema'; return 0; }
  schema="$(release_state_field "$record" schema)"
  [[ "$schema" == "$NCHAT_PROD_RELEASE_STATE_SCHEMA" ]] ||
    printf "the record declares schema '%s', expected %s" "$schema" "$NCHAT_PROD_RELEASE_STATE_SCHEMA"
}

# --- the record's own invariants -------------------------------------------
#
# What a present record must satisfy on its own, before anyone compares it with
# the cluster: the contract's schema and keys, and a candidate written whole.
# Here rather than in lifecycle.sh because they read nothing but the record, so
# the privileged bootstrap can validate an existing record without sourcing the
# code that decides releases. Whether the record agrees with the cluster stays
# lifecycle.sh's question.

# Emptiest condition first, then the schema that decides which keys apply, then
# the keys themselves. An existing ConfigMap with empty `data` renders exactly
# like an absent one, so it has to be named as its own fault rather than
# reported as eight missing keys.
require_release_state_contract() {
  local record="$1" problem
  [[ -n "$record" ]] ||
    { echo "the lifecycle record exists but carries no data at all; refusing to act on it" >&2; return 1; }
  problem="$(release_state_schema_problem "$record")"
  [[ -z "$problem" ]] || { echo "$problem; refusing to act on it" >&2; return 1; }
  problem="$(release_state_shape_problem "$record")"
  [[ -z "$problem" ]] || { echo "$problem; refusing to act on it" >&2; return 1; }
}

# The four candidate fields are written together and cleared together, so any
# mixture of set and empty is a preparation or a cutover that did not finish.
require_record_candidate_complete() {
  local record="$1" key value set_count=0 empty_count=0
  for key in candidate_slot candidate_release candidate_ready_at prepare_run_id; do
    value="$(release_state_field "$record" "$key")"
    if [[ -n "$value" ]]; then
      set_count=$((set_count + 1))
    else
      empty_count=$((empty_count + 1))
    fi
  done
  ((set_count == 0 || empty_count == 0)) ||
    { echo "the lifecycle record holds a half-written candidate ($set_count of 4 fields set); a preparation or a cutover did not finish" >&2; return 1; }
}

# --- transitions ---------------------------------------------------------
#
# Three, one per workflow, and each writes the record that the *next* operation
# will be judged against. They record; they decide nothing.

# Preparation finished: this slot, this release, validated at this moment, from
# this build run.
#
# The active slot and the cutover timestamp are carried forward unchanged --
# preparation moves no traffic, so it may not restate either.
#
# The reservation is carried forward too, unless it named the slot the
# candidate now occupies. Reaching this point means the lifecycle gate allowed
# the reuse and the slot was retired and redeployed, so the release that was
# reserved on it no longer exists to roll back to; leaving the claim standing
# would offer an operator a rollback to a slot running the new release.
record_candidate_ready() {
  local slot="$1" release="$2" run_id="$3" record reserved
  record="$(release_state_read_for_update)" || return 1
  reserved="$(release_state_field "$record" rollback_reserved_slot)"
  if [[ "$reserved" == "$slot" ]]; then
    reserved=""
  fi
  release_state_write \
    "candidate_slot=$slot" \
    "candidate_release=$release" \
    "candidate_ready_at=$(release_state_now)" \
    "prepare_run_id=$run_id" \
    "active_slot=$(release_state_field "$record" active_slot)" \
    "cutover_at=$(release_state_field "$record" cutover_at)" \
    "rollback_reserved_slot=$reserved" \
    "post_cutover_smoke=$(release_state_field "$record" post_cutover_smoke)"
}

# Traffic moved to `slot`. The slot it demoted becomes the reservation, dated
# now, and the candidate fields are cleared: the candidate has been consumed,
# and leaving them behind would let a second cutover replay a promotion that
# already happened.
#
# `post_cutover_smoke` is cleared, and that is the important half. This is
# written *before* the post-cutover smoke runs -- the demoted slot is the
# rollback from the moment traffic moves, and a failing smoke is when that
# matters most -- so at this point the release now serving has not been proved
# through the stable Services at all. Until record_traffic_smoke_passed says
# otherwise, the lifecycle treats it as unvalidated and refuses to retire its
# rollback.
record_cutover() {
  local slot="$1" previous="$2"
  release_state_write \
    "candidate_slot=" \
    "candidate_release=" \
    "candidate_ready_at=" \
    "prepare_run_id=" \
    "active_slot=$slot" \
    "cutover_at=$(release_state_now)" \
    "rollback_reserved_slot=$previous" \
    "post_cutover_smoke="
}

# Traffic returned to `slot`. No reservation is created: the slot that was just
# rolled back off is under investigation, not a rollback target, and recording
# it as one would offer the operator a one-click path back into the incident.
record_rollback() {
  local slot="$1"
  release_state_write \
    "candidate_slot=" \
    "candidate_release=" \
    "candidate_ready_at=" \
    "prepare_run_id=" \
    "active_slot=$slot" \
    "cutover_at=$(release_state_now)" \
    "rollback_reserved_slot=" \
    "post_cutover_smoke="
}

# The release now serving has been proved through the stable Services.
#
# Written after the smoke and only when it passed, by
# record-traffic-smoke.sh -- which is also the operator's way back when a
# post-cutover smoke failed, was investigated and now passes. Nothing else may
# write this field, and no workflow writes it speculatively.
#
# The value is the release identity, not a boolean. A slot that is redeployed
# after being smoked carries a different identity, so the evidence stops
# matching and the lifecycle stops honouring it -- the same reason the
# candidate evidence names a release rather than a slot.
record_traffic_smoke_passed() {
  local release="$1" record
  record="$(release_state_read_for_update)" || return 1
  release_state_write \
    "candidate_slot=$(release_state_field "$record" candidate_slot)" \
    "candidate_release=$(release_state_field "$record" candidate_release)" \
    "candidate_ready_at=$(release_state_field "$record" candidate_ready_at)" \
    "prepare_run_id=$(release_state_field "$record" prepare_run_id)" \
    "active_slot=$(release_state_field "$record" active_slot)" \
    "cutover_at=$(release_state_field "$record" cutover_at)" \
    "rollback_reserved_slot=$(release_state_field "$record" rollback_reserved_slot)" \
    "post_cutover_smoke=$release"
}
