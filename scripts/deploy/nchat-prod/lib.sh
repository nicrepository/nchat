#!/usr/bin/env bash
# Shared helpers for the production Blue/Green operations (issue #626).
# Sourced by the scripts beside it; the caller enables set -Eeuo pipefail.
#
# Two rules shape everything here:
#
#   1. The cluster is the source of truth. The active slot is read back from the
#      stable Services' selectors on every call. Nothing is cached in a file, a
#      variable or an operator's memory, so two people running these scripts
#      minutes apart cannot disagree about what is live.
#
#   2. A slot name is never a free string. It is checked against an allowlist
#      before it reaches a selector, a resource name or a kubectl argument.

NCHAT_PROD_NAMESPACE="${NCHAT_PROD_NAMESPACE:-nchat-prod}"
# Refusing an unexpected context by default is the difference between a typo and
# an outage. Overridable for a rehearsal cluster, never silently.
NCHAT_PROD_CONTEXT="${NCHAT_PROD_CONTEXT:-nchat-prod-deployer}"
NCHAT_PROD_SLOT_LABEL='nchat.io/release-slot'
# The two annotations that together identify what a slot is running: the commit
# it was built from, and the sealed release whose digests it actually pins.
NCHAT_PROD_RELEASE_SHA_ANNOTATION='nchat.io/release-sha'
NCHAT_PROD_RELEASE_ID_ANNOTATION='nchat.io/release-id'
# Written beside the digests by release-digests.sh, read by the deploy.
NCHAT_PROD_RELEASE_ID_FILE=release-id.txt
NCHAT_PROD_LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
NCHAT_PROD_SLOTS=(blue green)
# The slot the first production release is established in. Blue is the baseline
# by definition; bootstrap.sh and the baseline smoke both read it from here so
# the two cannot disagree about which slot that is.
NCHAT_PROD_BASELINE_SLOT=blue
# The stable Services, in the order status reports them. These are the names
# Traefik routes to; the per-slot Services (<name>-blue / <name>-green) are
# derived from them and never appear in an Ingress backend.
NCHAT_PROD_STABLE_SERVICES=(
  nchat-web
  nchat-admin-web
  auth-service
  chat-service
  file-service
  document-converter
  notification-service
  admin-service
  search-service
  media-service
)

# Ends the operation. `exit`, not `return`: every caller writes
#
#     something || prod_fail "..."
#
# and a `return 1` there sets the status of that one compound statement without
# leaving the function, so execution carried on to the next line. That is not a
# style detail — it is how resolve_active_slot came to report the two-element
# list "blue\ngreen" as a valid active slot and let a mixed cluster look
# uniform. Inside a command substitution this exits only that subshell, which is
# exactly the failure the caller is testing for.
prod_fail() {
  echo "error: $*" >&2
  exit 1
}

is_valid_slot() {
  local candidate="$1" slot
  for slot in "${NCHAT_PROD_SLOTS[@]}"; do
    [[ "$candidate" == "$slot" ]] && return 0
  done
  return 1
}

opposite_slot() {
  case "$1" in
    blue) printf 'green' ;;
    green) printf 'blue' ;;
    *) return 1 ;;
  esac
}

require_context() {
  local actual
  actual="$(kubectl config current-context)" || return 1
  [[ "$actual" == "$NCHAT_PROD_CONTEXT" ]] ||
    prod_fail "kube context is '$actual', expected '$NCHAT_PROD_CONTEXT'"
}

# Confirms the configured namespace is usable by the current identity, using a
# namespaced read only.
#
# `kubectl get namespace` asked for the cluster-scoped `namespaces` resource,
# which the production deployer is deliberately not allowed to read: a namespace
# gate is not a reason to hand a deploy identity a cluster-wide permission. So
# the gate asks the question in the namespace instead -- listing Services, a
# permission the Role already grants and the one every later read depends on.
#
# Under that identity the authorization is the existence check. The RoleBinding
# lives in this namespace and nowhere else, so a namespace that does not exist,
# or any other namespace, answers Forbidden rather than an empty list. The two
# are not distinguishable here and are not guessed apart: only the exit status
# decides, kubectl's stderr is never parsed, and anything short of a successful
# read -- Forbidden, an absent namespace, an API error -- stops the operation.
require_namespace() {
  kubectl get services -n "$NCHAT_PROD_NAMESPACE" >/dev/null 2>&1 ||
    prod_fail "namespace '$NCHAT_PROD_NAMESPACE' does not exist or is not readable with the current context and credentials"
}

# The state of one stable Service, as a single token:
#
#   blue | green   the slot it selects
#   MISSING        the Service does not exist
#   UNSET          it exists but carries no release-slot key
#   <literal>      any other selector value, reported verbatim
#
# These are four different operational situations and the caller must be able to
# tell them apart. A Service deleted mid-cutover is not the same problem as one
# whose selector was never patched, and neither is "mixed"; collapsing them into
# an empty string is what made a partially cut-over namespace unreadable.
service_slot() {
  local service="$1" selector
  if ! selector="$(kubectl get service "$service" -n "$NCHAT_PROD_NAMESPACE" \
    -o "jsonpath={.spec.selector['nchat\\.io/release-slot']}" 2>/dev/null)"; then
    printf 'MISSING'
    return 0
  fi
  printf '%s' "${selector:-UNSET}"
}

# One "<service> <slot>" line per stable Service. Every other function reads the
# cluster through this one, so there is a single place where the mapping is
# obtained and a single shape for the tests to exercise.
collect_service_slots() {
  local service
  for service in "${NCHAT_PROD_STABLE_SERVICES[@]}"; do
    printf '%s %s\n' "$service" "$(service_slot "$service")"
  done
}

distinct_slots() {
  awk '{ print $2 }' <<<"$1" | LC_ALL=C sort -u
}

# The one slot every stable Service agrees on, or failure.
#
# A mixed state is a real operational condition — a cutover that stopped halfway
# — and read-only commands report it while mutating commands refuse to guess
# their way out of it. Mutating commands take their target from the operator
# instead; see require_target_slot.
resolve_active_slot() {
  local mapping="$1" slots count
  slots="$(distinct_slots "$mapping")"
  count="$(printf '%s\n' "$slots" | grep -c .)"
  [[ "$count" -eq 1 ]] || prod_fail "services do not agree on a slot (mixed state)"
  is_valid_slot "$slots" || prod_fail "services select an unknown slot: $slots"
  printf '%s' "$slots"
}

# Parses "--target <slot>" and nothing else.
#
# Every production mutation names its destination explicitly. Deriving it as
# opposite_slot(active) makes the operation depend on the state it is about to
# change, which turns a retry into a reversal: a rollback from Green to Blue,
# run twice, would read Blue as active and send production straight back to the
# release it had just abandoned. A named target is idempotent — running it again
# converges to the same place — and it is the only form that can express intent
# at all when the cluster is mixed and there is no "opposite" to speak of.
parse_target_slot() {
  local flag="${1:-}" value="${2:-}"
  [[ "$flag" == "--target" ]] || return 1
  is_valid_slot "$value" || return 1
  printf '%s' "$value"
}

# Fails before any mutation when the target is missing or not an allowlisted
# slot, so no operator-supplied string ever reaches a selector or a patch.
require_target_slot() {
  local target
  target="$(parse_target_slot "$@")" ||
    prod_fail "a target slot is required: --target <blue|green>"
  printf '%s' "$target"
}

# True when every stable Service already selects the target: the operation has
# nothing to move and must report a no-op rather than invert anything.
all_services_on_slot() {
  local mapping="$1" target="$2" slots
  slots="$(distinct_slots "$mapping")"
  [[ "$slots" == "$target" ]]
}

# Everything Kubernetes reports about a rollout, as one pipe-separated record:
#   generation|observedGeneration|replicas|updated|ready|available|unavailable
deployment_rollout_state() {
  local deployment="$1" template
  template='{.metadata.generation}|{.status.observedGeneration}|{.spec.replicas}'
  template+='|{.status.updatedReplicas}|{.status.readyReplicas}'
  template+='|{.status.availableReplicas}|{.status.unavailableReplicas}'
  kubectl get deployment "$deployment" -n "$NCHAT_PROD_NAMESPACE" \
    -o "jsonpath=$template" 2>/dev/null
}

# The controller has caught up with the object, and the object asks for pods.
#
# An absent field is never healthy: status is populated by the controller, so
# "not reported yet" means the rollout has not been observed, not that it
# succeeded.
rollout_reported_fully() {
  local generation="$1" observed="$2" desired="$3"
  [[ -n "$generation" && -n "$observed" && -n "$desired" ]] || return 1
  [[ "$desired" -gt 0 ]] || return 1
  [[ "$observed" -ge "$generation" ]] || return 1
}

# Every replica is the NEW one, and every one of them is serving.
#
# updatedReplicas is the field that distinguishes the two: readyReplicas counts
# pods of any generation, so a stuck rollout whose previous pods are still up
# satisfies "ready == desired" while none of the new pods exist.
rollout_replicas_settled() {
  local desired="$1" updated="$2" ready="$3" available="$4" unavailable="$5"
  [[ "${updated:-0}" -eq "$desired" ]] || return 1
  [[ "${ready:-0}" -eq "$desired" ]] || return 1
  [[ "${available:-0}" -eq "$desired" ]] || return 1
  [[ "${unavailable:-0}" -eq 0 ]] || return 1
}

# Pure, so the tests can drive every rollout shape without a cluster.
rollout_is_complete() {
  local generation observed desired updated ready available unavailable
  IFS='|' read -r generation observed desired updated ready available unavailable <<<"$1"
  rollout_reported_fully "$generation" "$observed" "$desired" || return 1
  rollout_replicas_settled "$desired" "$updated" "$ready" "$available" "$unavailable"
}

# A workload is ready only when its CURRENT generation has fully rolled out.
#
# The previous test was readyReplicas == spec.replicas, which is satisfied by
# the pods of the release being replaced: apply B, let its pods fail to
# schedule, and A's two Ready pods still answered the question "is this
# Deployment ready?" with yes — so a candidate that never started could be
# smoked and promoted.
deployment_ready() {
  local record
  record="$(deployment_rollout_state "$1")" || return 1
  rollout_is_complete "$record"
}

# Every workload of a slot, Ready. Reports each failure rather than the first,
# so one run tells an operator everything that is wrong.
slot_ready() {
  local slot="$1" service failures=0
  is_valid_slot "$slot" || return 1
  for service in "${NCHAT_PROD_STABLE_SERVICES[@]}"; do
    if ! deployment_ready "$service-$slot"; then
      echo "not ready: deployment/$service-$slot" >&2
      failures=$((failures + 1))
    fi
  done
  [[ "$failures" -eq 0 ]]
}

# "<workload> <release-sha> <image>" for every workload of a slot.
#
# Every workload, not one of them: reading the slot's version from
# chat-service alone would report a clean release while file-service and
# media-service were still on the previous one, which is exactly the state a
# half-applied deploy leaves behind and exactly the state an operator must not
# promote.
# The component label a Deployment selects on, read from the object rather than
# mapped from its name: it is the relation Kubernetes itself uses to find the
# workload's pods, so it cannot drift from what is actually deployed.
deployment_component() {
  kubectl get deployment "$1" -n "$NCHAT_PROD_NAMESPACE" \
    -o "jsonpath={.spec.selector.matchLabels['app\\.kubernetes\\.io/component']}" 2>/dev/null
}

# The release each READY pod of a workload is running, one per line.
#
# The pods, not the Deployment. The Deployment's annotation is the release we
# asked for; these are the releases actually serving. The two differ for the
# whole of a stuck rollout, which is precisely when it matters.
#
# The annotation reaches pods because the slot Kustomization sets it as a common
# annotation, which kustomize applies to spec.template.metadata as well.
deployment_observed_releases() {
  local deployment="$1" slot="$2" component template
  component="$(deployment_component "$deployment")" || return 1
  [[ -n "$component" ]] || return 1
  template='{range .items[?(@.status.conditions[?(@.type=="Ready")].status=="True")]}'
  # Both halves of the identity, joined, so every gate downstream compares the
  # code AND the bytes. A pod missing either annotation yields a value that
  # matches no valid release and is refused rather than defaulted.
  template+="{.metadata.annotations['nchat\\.io/release-sha']}{':'}"
  template+="{.metadata.annotations['nchat\\.io/release-id']}{'\n'}{end}"
  kubectl get pods -n "$NCHAT_PROD_NAMESPACE" \
    -l "app.kubernetes.io/component=$component,$NCHAT_PROD_SLOT_LABEL=$slot" \
    -o "jsonpath=$template" 2>/dev/null
}

# The single release every Ready pod of a workload carries, or a state token.
#
#   <sha>:<id>     every Ready pod runs this release
#   none           the workload has no Ready pod
#   mixed          Ready pods disagree — a rollout caught in the middle
#   unset          the pods carry no complete release identity
observed_release_of() {
  local deployment="$1" slot="$2" releases distinct count
  releases="$(deployment_observed_releases "$deployment" "$slot")" || { printf 'none'; return 0; }
  distinct="$(grep -v '^[[:space:]]*$' <<<"$releases" | LC_ALL=C sort -u)"
  count="$(printf '%s\n' "$distinct" | grep -c .)"
  [[ "$count" -ne 0 ]] || { printf 'none'; return 0; }
  [[ "$count" -eq 1 ]] || { printf 'mixed'; return 0; }
  # An identity is the commit and the sealed release together. A pod carrying
  # only one of the two annotations produces a half-formed value here, and
  # naming it explicitly is what stops it being compared as though it were a
  # release: `unset` is refused by every caller, a truncated pair might not be.
  [[ "$distinct" =~ ^[a-f0-9]{40}:[a-f0-9]{64}$ ]] || { printf 'unset'; return 0; }
  printf '%s' "$distinct"
}

# "<workload> <observed-release> <image>" for every workload of a slot.
#
# Observed, not desired. A workload whose rollout has not completed reports the
# release its Ready pods are on, so a slot mid-rollout can never be mistaken for
# one that finished.
slot_workload_releases() {
  local slot="$1" service observed image
  is_valid_slot "$slot" || return 1
  for service in "${NCHAT_PROD_STABLE_SERVICES[@]}"; do
    observed="$(observed_release_of "$service-$slot" "$slot")"
    image="$(kubectl get deployment "$service-$slot" -n "$NCHAT_PROD_NAMESPACE" \
      -o jsonpath='{.spec.template.spec.containers[0].image}' 2>/dev/null)"
    printf '%s %s %s\n' "$service" "${observed:-none}" "${image:-absent}"
  done
}

# What state a slot's release is in, as one token:
#
#   CONSISTENT <sha>   every workload carries the same release
#   NOT_DEPLOYED       no workload of this slot exists
#   MIXED              the workloads disagree, or only some of them exist, or
#                      one carries no release annotation at all
#
# NOT_DEPLOYED is deliberately separate from MIXED. After bootstrap the second
# slot legitimately does not exist, and reporting that as a fault would train
# operators to ignore the one message that means a deploy landed half-way.
# Every workload of the slot has finished rolling out its current generation.
slot_rollout_complete() {
  local slot="$1" service
  for service in "${NCHAT_PROD_STABLE_SERVICES[@]}"; do
    deployment_ready "$service-$slot" || return 1
  done
}

slot_release_state() {
  local slot="$1" releases shas images distinct absent total
  releases="$(slot_workload_releases "$slot")" || return 1
  images="$(awk '{ print $3 }' <<<"$releases")"
  total="$(printf '%s\n' "$images" | grep -c .)"
  absent="$(printf '%s\n' "$images" | grep -c '^absent$' || true)"
  if [[ "$absent" -eq "$total" ]]; then
    printf 'NOT_DEPLOYED'
    return 0
  fi
  # Before asking which release is running, establish that one has finished
  # rolling out. A slot whose new pods are still Pending is serving the previous
  # release from pods that remain Ready, and calling that CONSISTENT is what let
  # a stuck deploy be smoked and promoted.
  if ! slot_rollout_complete "$slot"; then
    printf 'ROLLING_OUT'
    return 0
  fi
  shas="$(awk '{ print $2 }' <<<"$releases" | LC_ALL=C sort -u)"
  distinct="$(printf '%s\n' "$shas" | grep -c .)"
  if [[ "$absent" -ne 0 || "$distinct" -ne 1 ]]; then
    printf 'MIXED'
    return 0
  fi
  # "none" (no Ready pod carries a release) and "mixed" (they disagree) are
  # answers about the observed state, never a release to promote.
  case "$shas" in
    none | mixed | unset) printf 'MIXED'; return 0 ;;
  esac
  printf 'CONSISTENT %s' "$shas"
}

# The single release SHA a slot carries, or failure when it is not consistent.
slot_release() {
  local slot="$1" state
  state="$(slot_release_state "$slot")" || return 1
  [[ "$state" == CONSISTENT\ * ]] || return 1
  printf '%s' "${state#CONSISTENT }"
}

# The gate every promotion passes through.
#
# Readiness is not enough. A slot whose nine Deployments are all Ready can still
# be carrying two different releases -- chat-service on the new one, file-service
# on the old -- because a deploy can fail after updating some workloads and the
# survivors stay Ready throughout. Promoting that serves users a combination
# nobody built and nobody tested, so the release identity is checked as well and
# anything short of CONSISTENT blocks.
require_consistent_release() {
  local slot="$1" state
  state="$(slot_release_state "$slot")" ||
    prod_fail "cannot read the release identity of slot $slot"
  case "$state" in
    CONSISTENT\ *)
      printf '%s' "${state#CONSISTENT }"
      return 0
      ;;
    NOT_DEPLOYED)
      prod_fail "slot $slot is not deployed; there is no release to promote"
      ;;
    ROLLING_OUT)
      echo "slot $slot has not finished rolling out:" >&2
      slot_workload_releases "$slot" | awk '{ printf "  %-22s observed=%s  %s\n", $1, $2, $3 }' >&2
      prod_fail "slot $slot is still rolling out; its Ready pods are not all on the release it declares"
      ;;
  esac
  echo "slot $slot does not carry one release:" >&2
  slot_workload_releases "$slot" | awk '{ printf "  %-22s %s  %s\n", $1, $2, $3 }' >&2
  prod_fail "slot $slot is $state; deploy the release again before promoting it"
}

# The evidence a smoke run produces and a cutover checks: the slot AND the exact
# release that was validated on it.
#
# The slot name alone is not evidence. Smoke green on release A, redeploy green
# as release B, and a confirmation naming only "green" still satisfies the gate
# -- promoting a release nobody smoked. Binding the two means a candidate that
# changed after validation no longer matches.
smoke_evidence_for() {
  local slot="$1" release
  release="$(slot_release "$slot")" || return 1
  printf '%s:%s' "$slot" "$release"
}

# The release identity recorded beside the digests, refused unless it is one.
read_release_id() {
  local artifacts_dir="$1" release_id
  local file="$artifacts_dir/$NCHAT_PROD_RELEASE_ID_FILE"
  # Checked before it is read: `$(<missing)` under `set -e` ends the caller with
  # a shell diagnostic instead of the refusal the caller wants to report.
  [[ -f "$file" && ! -L "$file" ]] || return 1
  release_id="$(<"$file")"
  [[ "$release_id" =~ ^[a-f0-9]{64}$ ]] || return 1
  printf '%s' "$release_id"
}

confirm() {
  local prompt="$1" answer
  [[ "${NCHAT_PROD_ASSUME_YES:-}" != "1" ]] || return 0
  read -r -p "$prompt [type 'yes' to continue]: " answer
  [[ "$answer" == "yes" ]] || prod_fail "aborted by operator"
}

print_context_banner() {
  local mapping="$1"
  echo "kube context : $(kubectl config current-context)"
  echo "namespace    : $NCHAT_PROD_NAMESPACE"
  echo "environment  : production"
  printf '%s\n' "$mapping" | sed 's/^/service      : /'
}

# The image a stable Service's workload is built from. The two frontends are the
# only ones whose Deployment name and image name differ.
image_for_service() {
  case "$1" in
    nchat-web) printf 'web' ;;
    nchat-admin-web) printf 'admin-web' ;;
    auth-service|chat-service|file-service|document-converter|notification-service|admin-service|search-service|media-service)
      printf '%s' "$1" ;;
    *) return 1 ;;
  esac
}

read_digest() {
  local file="$1" digest
  [[ -f "$file" && ! -L "$file" ]] || prod_fail "missing digest artifact: $file"
  digest="$(<"$file")"
  [[ "$digest" =~ ^sha256:[a-f0-9]{64}$ ]] || prod_fail "not an OCI digest: $file"
  printf '%s' "$digest"
}

# Rewrites a slot's Kustomization to the release digests. Production never
# accepts a tag: a tag can be moved after it has been reviewed, a digest cannot,
# so the identity of a release and the bytes that run are the same fact.
set_slot_release_images() {
  local overlay="$1" artifacts="$2" service image digest
  for service in "${NCHAT_PROD_STABLE_SERVICES[@]}"; do
    image="$(image_for_service "$service")" || return 1
    digest="$(read_digest "$artifacts/digest-$image.txt")" || return 1
    (cd "$overlay" && kustomize edit set image \
      "ghcr.io/nicrepository/nchat/$image=ghcr.io/nicrepository/nchat/$image@$digest") || return 1
  done
}

# Copies the manifests into a throwaway tree and installs the operator's
# topology, so the committed placeholders are never the thing that reaches a
# cluster and an edit made during a release cannot outlive it.
prepare_prod_deploy_tree() {
  local root_dir="$1" temporary_root="$2" topology="${3:-}"
  mkdir -p "$temporary_root"
  cp -a "$root_dir/infra/k8s" "$temporary_root/infra-k8s"
  [[ -n "$topology" ]] || return 0
  [[ -f "$topology" && ! -L "$topology" ]] || prod_fail "topology file not found: $topology"
  cp "$topology" "$temporary_root/infra-k8s/overlays/k3s-prod/shared/topology.env"
}

# One Service's selector, rewritten to a slot. The slot has already passed
# is_valid_slot before it gets here, so nothing operator-supplied is ever
# interpolated into a patch document.
patch_service_slot() {
  local service="$1" slot="$2"
  kubectl patch service "$service" -n "$NCHAT_PROD_NAMESPACE" --type=merge \
    -p "{\"spec\":{\"selector\":{\"$NCHAT_PROD_SLOT_LABEL\":\"$slot\"}}}" >/dev/null
}

# Moves every stable Service to one slot and reads each one back.
#
# Kubernetes has no transaction across nine Services, so this is nine sequential
# writes and this function does not pretend otherwise: it verifies after each
# one and stops at the first that does not take, leaving a described partial
# state instead of an assumed complete one. The window is a few hundred
# milliseconds of two slots serving at once, which the release contract already
# requires to be safe — Blue and Green run against one database, one Valkey and
# one object store, and both must speak the same event and API shapes.
switch_services_to_slot() {
  local slot="$1" service moved=0
  is_valid_slot "$slot" || return 1
  for service in "${NCHAT_PROD_STABLE_SERVICES[@]}"; do
    if ! patch_service_slot "$service" "$slot"; then
      echo "failed to patch service/$service after $moved of ${#NCHAT_PROD_STABLE_SERVICES[@]}" >&2
      return 1
    fi
    if [[ "$(service_slot "$service")" != "$slot" ]]; then
      echo "service/$service did not take slot $slot" >&2
      return 1
    fi
    moved=$((moved + 1))
    echo "  service/$service -> $slot"
  done
}

# --- capacity preflight -------------------------------------------------
#
# The shell gathers what the cluster reports; candidate-capacity.py does every
# unit conversion and every comparison, so all the arithmetic lives in one place
# that has fixtures behind it.

# One field of the namespace quota's status, or nothing.
#
# The dot in the key is escaped, and that is the whole of it. kubectl's jsonpath
# resolves a bracketed key by re-parsing it as a field path, so
# `status.hard['requests.cpu']` asked for `hard.requests.cpu` -- a nested object
# that does not exist -- and came back empty. Every dimension whose key carries a
# dot (cpu, memory, ephemeral-storage) was therefore reported as unread, and only
# `pods` ever arrived. `\.` is the escape the parser understands, and it is the
# same one kubectl's own documentation uses for keys such as
# `kubernetes\.io/hostname`.
#
# Empty output stays empty output: a quota that does not declare a dimension, a
# namespace with no quota at all and a read that failed are alike here, and
# candidate-capacity.py reports each of them INCONCLUSIVE rather than as zero.
# There is nothing to substitute a default for.
#
# .items[0] is unchanged and deliberate: this namespace is provisioned with one
# ResourceQuota (infra/k8s/nchat-prod), and summing or intersecting several would
# be a policy decision, not a bug fix. A second quota would be read past, which
# is why the manifest check keeps the single-quota shape honest.
quota_status_field() {
  local section="$1" field="${2//./\\.}"
  kubectl get resourcequota -n "$NCHAT_PROD_NAMESPACE" \
    -o "jsonpath={.items[0].status.$section['$field']}" 2>/dev/null
}

# One "<cpu> <memory> <ephemeral-storage> <pods>" line per node. Positional: a
# resource the node does not report leaves its position empty rather than
# shifting the ones after it.
node_allocatable_lines() {
  local template='{range .items[*]}{.status.allocatable.cpu}{" "}'
  template+='{.status.allocatable.memory}{" "}'
  template+="{.status.allocatable.ephemeral-storage}{' '}"
  template+="{.status.allocatable.pods}{'\n'}{end}"
  kubectl get nodes -o "jsonpath=$template" 2>/dev/null
}

# One "<phase>|<nodeName>" line per Pod in the cluster.
#
# Separate from the per-container request lines because pod slots are counted
# per Pod: charging a two-container Pod for two slots would understate the
# cluster's remaining capacity.
cluster_pod_lines() {
  kubectl get pods --all-namespaces \
    -o "jsonpath={range .items[*]}{.status.phase}|{.spec.nodeName}{'\n'}{end}" 2>/dev/null
}

# Expands one Pod record into the per-container lines the evaluator reads.
#
#   in:  "<phase>|<nodeName>|<requests>;<requests>;"
#   out: "<phase>|<nodeName>|<requests>", once per container.
#
# A record that is not three fields, and a record that lists no container at
# all, is passed through untouched: the first is for candidate-capacity.py to
# refuse, the second is a Pod that reserves nothing. Dropping either here would
# take commitment out of the sum, which is the direction that turns a full
# cluster into a pass.
expand_pod_container_requests() {
  awk -F'|' '{
    if (NF != 3 || $3 == "") { print; next }
    count = split($3, requests, ";")
    for (i = 1; i <= count; i++)
      if (requests[i] != "") print $1 "|" $2 "|" requests[i]
  }'
}

# One "<phase>|<nodeName>|<cpu> <memory> <ephemeral-storage>" line per container
# in the cluster.
#
# The Pod's phase and node are read once, at Pod level, and its containers are
# emitted after them. They used to be read from inside the container loop as
# `{$.status.phase}`, which is not a parent reference: kubectl's jsonpath treats
# `$` as the *current* object, so both resolved against the container, came back
# empty, and every line arrived as "||<requests>" -- refused by the parser as a
# line whose first field is not a Pod phase. jsonpath cannot reach the Pod from
# inside the loop at all, so the loop no longer needs it to.
#
# The containers of a Pod are ';'-terminated on that Pod's line and split back
# out by expand_pod_container_requests. The separator is one this collector
# emits, into a field that holds Kubernetes quantities and nothing else; no
# human-readable output is parsed to recover it.
#
# The shell only collects; candidate-capacity.py decides which of these hold
# capacity, so that policy is covered by fixtures instead of living inside a
# jsonpath nobody can test. Init containers are emitted too: the kubelet
# reserves the larger of (max init request, sum of app requests), so counting
# both can overstate a Pod but never understates it -- the conservative direction
# for a preflight.
#
# Empty output is a failed read, not an empty cluster: there is always at least
# one Pod. It is reported as a failure rather than passed on as a file that
# would read as a cluster with nothing committed on it.
cluster_container_request_lines() {
  local template pods
  template='{range .items[*]}{.status.phase}|{.spec.nodeName}|'
  template+='{range .spec.containers[*]}'
  template+="{.resources.requests.cpu}{' '}{.resources.requests.memory}{' '}"
  template+="{.resources.requests.ephemeral-storage}{';'}{end}"
  template+='{range .spec.initContainers[*]}'
  template+="{.resources.requests.cpu}{' '}{.resources.requests.memory}{' '}"
  template+="{.resources.requests.ephemeral-storage}{';'}{end}{'\n'}{end}"
  pods="$(kubectl get pods --all-namespaces -o "jsonpath=$template" 2>/dev/null)" || return 1
  [[ -n "$pods" ]] || return 1
  printf '%s\n' "$pods" | expand_pod_container_requests
}

# "<deployment>|<replicas>|<cpu>|<memory>|<ephemeral-storage>", one line per
# container of every workload the target slot already runs.
#
# Replicas alone are not enough. The preflight has to compare what a pod costs
# now against what it will cost, or a release that raises its requests reads as
# though only the surge were new: going from 2x100m to 2x1000m needs 1800m more
# in its final state alone, and counting one surge pod at the new price reported
# 1000m. Both container lists are emitted because the candidate side counts both.
#
# Empty output means the slot does not exist, which is the first deploy of it and
# genuinely costs a whole slot.
current_slot_workloads() {
  local slot="$1" template
  is_valid_slot "$slot" || return 1
  template='{range .items[*]}'
  template+="{range .spec.template.spec.containers[*]}{\$.metadata.name}|{\$.spec.replicas}|"
  template+="{.resources.requests.cpu}|{.resources.requests.memory}|"
  template+="{.resources.requests.ephemeral-storage}{'\n'}{end}"
  template+="{range .spec.template.spec.initContainers[*]}{\$.metadata.name}|{\$.spec.replicas}|"
  template+="{.resources.requests.cpu}|{.resources.requests.memory}|"
  template+="{.resources.requests.ephemeral-storage}{'\n'}{end}{end}"
  kubectl get deployments -n "$NCHAT_PROD_NAMESPACE" \
    -l "$NCHAT_PROD_SLOT_LABEL=$slot" -o "jsonpath=$template" 2>/dev/null
}

# --- cluster-wide capacity evidence -------------------------------------
#
# The three collectors above read Nodes and Pods across every namespace. The
# production deploy identity holds a namespaced Role and nothing cluster-scoped,
# so it cannot run them at all -- and a namespace-only capacity picture would be
# worse than none: the node these workloads share sits at 94% of its CPU
# requests, most of it belonging to other namespaces, so a view of nchat-prod
# alone reports room that does not exist.
#
# So collection and evaluation are separated. A trusted read-only context runs
# capacity-evidence.sh, which writes the same three files plus a metadata
# record; the deployer reads that evidence and consults the API only for what it
# is already allowed to see. Evaluation stays where it was, in
# candidate-capacity.py, against byte-identical input in both modes.
#
# Trust boundary. The checksums here detect a truncated or accidentally edited
# file. They are NOT authenticity: anything that can write the evidence
# directory can write a matching sha256sums.txt. Evidence is trusted because of
# where it came from -- an operator-run collection, or later a controlled CI
# artifact -- not because of anything in it. Freshness, schema and the namespace
# binding narrow the window in which stale or foreign evidence is accepted; they
# do not make an untrusted directory safe to point this at.
NCHAT_PROD_CAPACITY_EVIDENCE_SCHEMA='nchat-prod-capacity-evidence/v1'
NCHAT_PROD_CAPACITY_EVIDENCE_FILES=(node-allocatable.txt cluster-requests.txt cluster-pods.txt)
# Fifteen minutes. Long enough to collect, review and deploy by hand; short
# enough that a snapshot cannot outlive the cluster state it describes by a
# working day. The rollout remains the second barrier for anything that moved
# inside the window.
# "-900", not ":-900": the default applies when the variable is unset, and an
# explicitly empty setting stays empty so that capacity_evidence_max_age refuses
# it rather than quietly substituting the default for a misconfiguration.
NCHAT_PROD_CAPACITY_EVIDENCE_MAX_AGE_SECONDS="${NCHAT_PROD_CAPACITY_EVIDENCE_MAX_AGE_SECONDS-900}"

# Writes the three cluster-wide inputs into a directory, reporting whether every
# one of them was actually produced.
#
# The status matters to the collector and not to the live path: live mode hands
# whatever it got to candidate-capacity.py, which reports a dimension it could
# not read as INCONCLUSIVE and blocks. The collector must never turn a failed
# read into a file, so it checks this return value and refuses to publish.
collect_cluster_capacity_files() {
  local dir="$1" status=0
  node_allocatable_lines >"$dir/node-allocatable.txt" || status=1
  cluster_container_request_lines >"$dir/cluster-requests.txt" || status=1
  cluster_pod_lines >"$dir/cluster-pods.txt" || status=1
  return "$status"
}

# The value of one `key=value` line of an evidence metadata record.
capacity_evidence_field() {
  sed -n "s/^$2=//p" "$1" | head -1
}

# Every collected file must exist and carry something.
#
# An empty file is a failed collection, not an empty cluster: there is always at
# least one node and at least one Pod. Accepting one would hand the evaluator
# "no allocatable capacity" or "nothing committed" as though the cluster had
# said so.
capacity_evidence_files_present() {
  local dir="$1" name
  for name in "${NCHAT_PROD_CAPACITY_EVIDENCE_FILES[@]}"; do
    [[ -s "$dir/$name" ]] ||
      { echo "capacity evidence file is missing or empty: $name" >&2; return 1; }
  done
}

# The freshness limit, refused unless it is a plain non-negative decimal.
#
# It reaches an arithmetic context, and bash resolves a bare word there as a
# variable name: NCHAT_PROD_CAPACITY_EVIDENCE_MAX_AGE_SECONDS=oops ended the run
# with "oops: unbound variable", and "1+1" would have been evaluated as an
# expression. A misconfigured limit is an ordinary operator mistake and has to
# be reported as one.
#
# 10# forces base ten, so 000900 is nine hundred seconds rather than an octal
# reading of the same digits.
#
# The digit cap is the second half of the same problem. "Decimal" was not enough:
# bash arithmetic is signed 64-bit and wraps silently, so 9999999999999999999
# became -8446744073709551617 and 18446744073709551616 became 0 -- a limit of
# zero, which quietly refuses every snapshot, and a negative one, which quietly
# accepts none of them for the right reason. Eighteen digits keeps every accepted
# value below 10^18, comfortably inside the range, and is checked as text
# precisely because measuring it with arithmetic would be the bug itself. Nobody
# needs a freshness window longer than the age of the universe.
capacity_evidence_max_age() {
  local value="$NCHAT_PROD_CAPACITY_EVIDENCE_MAX_AGE_SECONDS"
  [[ "$value" =~ ^[0-9]{1,18}$ ]] ||
    { echo "invalid NCHAT_PROD_CAPACITY_EVIDENCE_MAX_AGE_SECONDS: expected a non-negative decimal integer of at most 18 digits, got '$value'" >&2; return 1; }
  printf '%s' "$((10#$value))"
}

capacity_evidence_is_fresh() {
  local stamp="$1" collected now age limit
  limit="$(capacity_evidence_max_age)" || return 1
  [[ "$stamp" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$ ]] ||
    { echo "capacity evidence has no usable collected_at timestamp: '$stamp'" >&2; return 1; }
  collected="$(date -u -d "$stamp" +%s 2>/dev/null)" ||
    { echo "capacity evidence collected_at is not a real instant: '$stamp'" >&2; return 1; }
  now="$(date -u +%s)"
  age=$((now - collected))
  # A stamp in the future is refused too, with a minute of clock skew allowed:
  # without that, dating evidence forward would make it permanently fresh.
  ((age >= -60 && age <= limit)) ||
    { echo "capacity evidence is ${age}s old; the limit is ${limit}s" >&2; return 1; }
}

capacity_evidence_metadata_ok() {
  local file="$1" schema namespace
  schema="$(capacity_evidence_field "$file" schema)"
  [[ "$schema" == "$NCHAT_PROD_CAPACITY_EVIDENCE_SCHEMA" ]] ||
    { echo "capacity evidence schema is '$schema', expected $NCHAT_PROD_CAPACITY_EVIDENCE_SCHEMA" >&2; return 1; }
  namespace="$(capacity_evidence_field "$file" namespace)"
  [[ "$namespace" == "$NCHAT_PROD_NAMESPACE" ]] ||
    { echo "capacity evidence was collected for namespace '$namespace', not '$NCHAT_PROD_NAMESPACE'" >&2; return 1; }
  capacity_evidence_is_fresh "$(capacity_evidence_field "$file" collected_at)"
}

# Refuses a symlink rather than following one: the evidence path is an operator
# or CI setting, and a link inside it would let a directory that looks like a
# snapshot read something else entirely.
copy_capacity_evidence_file() {
  local from="$1" to="$2"
  [[ ! -L "$from" ]] ||
    { echo "capacity evidence file is a symlink: $from" >&2; return 1; }
  [[ -f "$from" ]] ||
    { echo "capacity evidence file is missing: $from" >&2; return 1; }
  cp -- "$from" "$to"
}

# Copies the evidence into the deploy's own temporary directory and validates
# the copies. Copy first, on purpose: validating in place and reading afterwards
# leaves a window in which the files can change between the two.
load_capacity_evidence() {
  local source_dir="$1" dest="$2" name
  mkdir -p "$dest"
  for name in "${NCHAT_PROD_CAPACITY_EVIDENCE_FILES[@]}" sha256sums.txt metadata; do
    copy_capacity_evidence_file "$source_dir/$name" "$dest/$name" || return 1
  done
  capacity_evidence_metadata_ok "$dest/metadata" || return 1
  (cd "$dest" && sha256sum --check --status sha256sums.txt) ||
    { echo "capacity evidence does not match its recorded checksums" >&2; return 1; }
  capacity_evidence_files_present "$dest"
}

# Runs the preflight for a rendered candidate manifest.
#
# Exit codes come straight from candidate-capacity.py: 0 sufficient,
# 1 provably insufficient, 2 inconclusive, 3 unusable input. The caller decides
# what to do with 2 — this function does not turn "unknown" into "fine".
#
# Cluster-wide inputs come from the evidence directory when one is named and
# from a live collection otherwise. Unusable evidence stops the operation here,
# rather than becoming an absent file the evaluator would report INCONCLUSIVE --
# which the operator override could then wave through.
#
# The slot's own workloads are read live in both modes. They are namespaced, the
# deployer can read them, and they are the one input that has to describe the
# cluster at this instant rather than as a snapshot found it.
run_capacity_preflight() {
  local manifest="$1" workdir="$2" slot="$3" inputs
  inputs="$workdir"
  if [[ -n "${NCHAT_PROD_CAPACITY_EVIDENCE_DIR:-}" ]]; then
    inputs="$workdir/evidence"
    load_capacity_evidence "$NCHAT_PROD_CAPACITY_EVIDENCE_DIR" "$inputs" ||
      prod_fail "capacity evidence in '$NCHAT_PROD_CAPACITY_EVIDENCE_DIR' is unusable; collect it again with capacity-evidence.sh"
  else
    collect_cluster_capacity_files "$workdir" || true
  fi
  current_slot_workloads "$slot" >"$workdir/current-slot.txt" || true
  python3 "$NCHAT_PROD_LIB_DIR/candidate-capacity.py" \
    --manifest "$manifest" \
    --quota-hard-cpu "$(quota_status_field hard requests.cpu)" \
    --quota-used-cpu "$(quota_status_field used requests.cpu)" \
    --quota-hard-memory "$(quota_status_field hard requests.memory)" \
    --quota-used-memory "$(quota_status_field used requests.memory)" \
    --quota-hard-pods "$(quota_status_field hard pods)" \
    --quota-used-pods "$(quota_status_field used pods)" \
    --quota-hard-storage "$(quota_status_field hard requests.ephemeral-storage)" \
    --quota-used-storage "$(quota_status_field used requests.ephemeral-storage)" \
    --node-allocatable-file "$inputs/node-allocatable.txt" \
    --cluster-requests-file "$inputs/cluster-requests.txt" \
    --cluster-pods-file "$inputs/cluster-pods.txt" \
    --current-slot-file "$workdir/current-slot.txt"
}

# --- migration coordination ---------------------------------------------
#
# Deploys used to run `kubectl delete job/nchat-migrations` before applying the
# next one. With two operators that deletes a migration that is still running:
# the pod dies, its PostgreSQL connection drops, the advisory lock it held is
# released by the server — but the schema_migrations row it had marked in
# progress stays behind, and the next run refuses to touch a dirty schema. The
# advisory lock protects against concurrent writers; it cannot protect against
# someone killing the writer.
#
# So a Job is never deleted here. It is named after its release, and its status
# decides what happens next.

# A DNS-safe, deterministic name. The full release stays in an annotation on the
# Job, so the truncation is only ever an identifier, never the record.
migration_job_name() {
  local release="$1"
  [[ "$release" =~ ^[a-f0-9]{40}$ ]] || return 1
  printf 'nchat-migrations-%s' "${release:0:12}"
}

# Complete | Failed | Active | Absent
migration_job_status() {
  local name="$1" record succeeded failed
  record="$(kubectl get job "$name" -n "$NCHAT_PROD_NAMESPACE" \
    -o 'jsonpath={.status.succeeded}|{.status.failed}' 2>/dev/null)" || {
    printf 'Absent'
    return 0
  }
  IFS='|' read -r succeeded failed <<<"$record"
  [[ "${succeeded:-0}" -eq 0 ]] || { printf 'Complete'; return 0; }
  [[ "${failed:-0}" -eq 0 ]] || { printf 'Failed'; return 0; }
  printf 'Active'
}

# The name of any migration Job still running for a different release, or "".
#
# One migration at a time across the whole namespace: Blue and Green share the
# schema, so two releases migrating at once is the situation the expand/contract
# rule assumes never happens.
other_active_migration_job() {
  local ours="$1" name
  while IFS= read -r name; do
    [[ -n "$name" && "$name" != "$ours" ]] || continue
    [[ "$(migration_job_status "$name")" == Active ]] || continue
    printf '%s' "$name"
    return 0
  done < <(kubectl get jobs -n "$NCHAT_PROD_NAMESPACE" \
    -l app.kubernetes.io/component=migrations \
    -o 'jsonpath={range .items[*]}{.metadata.name}{"\n"}{end}' 2>/dev/null)
  printf ''
}

wait_for_migration_job() {
  local name="$1"
  if kubectl wait "job/$name" -n "$NCHAT_PROD_NAMESPACE" \
    --for=condition=complete --timeout=600s; then
    return 0
  fi
  kubectl logs "job/$name" -n "$NCHAT_PROD_NAMESPACE" --all-containers --tail=200 || true
  prod_fail "migration $name did not complete; the release is blocked"
}

# Whether an existing Job already answers the question. Returns 0 when nothing
# further is needed, 1 when the caller must create the Job.
migration_already_settled() {
  local name="$1" release="$2" status
  status="$(migration_job_status "$name")"
  [[ "$status" != Complete ]] || {
    echo "migrations for release $release already completed ($name); not re-running."
    return 0
  }
  [[ "$status" != Failed ]] || report_failed_migration "$name" "$release"
  [[ "$status" != Active ]] || {
    echo "migration $name is already running for this release; waiting for it."
    wait_for_migration_job "$name"
    return 0
  }
  return 1
}

report_failed_migration() {
  local name="$1" release="$2"
  echo "The migration for release $release failed and has been kept for diagnosis." >&2
  echo "  kubectl logs job/$name -n $NCHAT_PROD_NAMESPACE --all-containers" >&2
  prod_fail "migration $name is in Failed state; investigate before deploying again"
}

# Applies the migration for a release exactly once, and never disturbs one that
# is already running.
ensure_migrations_applied() {
  local manifest="$1" release="$2" name other
  name="$(migration_job_name "$release")" ||
    prod_fail "release must be a 40-character commit SHA to name its migration Job"
  ! migration_already_settled "$name" "$release" || return 0
  other="$(other_active_migration_job "$name")"
  [[ -z "$other" ]] ||
    prod_fail "migration $other is in progress for another release; wait for it to finish"
  kubectl apply -f "$manifest"
  wait_for_migration_job "$name"
}

# Gives the rendered migration Job its per-release identity: a deterministic
# name, and the full release recorded as an annotation so the truncation in the
# name is never the only record of what ran.
name_migration_job_for_release() {
  local overlay="$1" release="$2" name
  name="$(migration_job_name "$release")" || return 1
  (
    cd "$overlay" || exit 1
    # "--" because the suffix begins with a hyphen and would otherwise be read
    # as a flag.
    kustomize edit set namesuffix -- "${name#nchat-migrations}"
    kustomize edit set annotation "nchat.io/release-sha:$release"
  )
}

# The capacity gate, shared by deploy and bootstrap.
#
# NCHAT_PROD_ALLOW_INCONCLUSIVE_CAPACITY exists because a preflight that cannot
# read the cluster must not silently pass, and must not become an unskippable
# wall either. The default is fail-safe: unknown blocks, and an operator who has
# checked capacity by hand says so explicitly.
check_capacity() {
  local manifest="$1" workdir="$2" slot="$3" status=0
  echo "capacity preflight:"
  run_capacity_preflight "$manifest" "$workdir" "$slot" || status=$?
  case "$status" in
    0) return 0 ;;
    2) allow_inconclusive_capacity ;;
    1) prod_fail "the cluster cannot hold slot $slot at production capacity" ;;
    # Unusable input, from the candidate manifest or from a cluster-wide file
    # that does not follow the collector's contract. It is a separate case from
    # inconclusive on purpose: NCHAT_PROD_ALLOW_INCONCLUSIVE_CAPACITY reaches 2
    # and never this, because "the input is broken" is not something anyone can
    # verify by hand and acknowledge.
    *) prod_fail "capacity preflight refused its input; see the error above" ;;
  esac
}

allow_inconclusive_capacity() {
  echo "preflight capacity inconclusive: the cluster did not report every dimension." >&2
  [[ "${NCHAT_PROD_ALLOW_INCONCLUSIVE_CAPACITY:-}" == "1" ]] ||
    prod_fail "refusing to proceed on an unverified capacity picture; verify with 'kubectl describe nodes' and set NCHAT_PROD_ALLOW_INCONCLUSIVE_CAPACITY=1 to proceed"
  echo "Proceeding on the operator's explicit acknowledgement." >&2
}
