#!/usr/bin/env bash
# Structural validation of the production Blue/Green overlay (issue #626).
#
# Every invariant here is a relationship between objects, not a string in a
# file: which slot a Service selects, whether both slots hold the same set of
# workloads, whether an Ingress reaches a stable Service or a per-slot one,
# whether the two slots differ in anything but their release images. The joins
# are done by scripts/ci/prod_blue_green_query.py; `grep -q blue` cannot express
# any of them.
#
# It renders production the way a release applies it -- shared, blue, green and
# migrations separately, then the composed picture -- because those are the
# units that get applied, and a manifest that only renders as part of a whole is
# a manifest no operator can deploy.
set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
PROD_OVERLAY="$ROOT_DIR/infra/k8s/overlays/k3s-prod"
QUERY="$ROOT_DIR/scripts/ci/prod_blue_green_query.py"
RENDER_DIR="$(mktemp -d "${TMPDIR:-/tmp}/nchat-prod-check.XXXXXX")"
trap 'rm -rf "$RENDER_DIR"' EXIT

ERRORS=0
SLOTS=(blue green)
STABLE_SERVICES=(nchat-web nchat-admin-web auth-service chat-service file-service
  notification-service admin-service search-service media-service)
# Shared by both slots on purpose: a pinned nginx and a virus scanner that no
# NChat release changes.
SHARED_WORKLOADS=(clamav upload-guard)
# Shared dependencies that must never be reachable from outside the cluster.
# upload-guard is absent on purpose: it is a public entrance by design, and it
# is already constrained by ingress_backend_is_allowed.
#
# livekit and coturn are on this list even though production runs neither. That
# is the point: the media plane is one shared AWS LiveKit deployment that
# nchat-dev also uses, the browser dials it directly, and an Ingress here naming
# `livekit` would mean someone had put WebRTC signalling behind this
# deployment's Traefik and, upstream of that, provisioned a local media server
# to point it at. coturn is listed on the same principle, independently of what
# the AWS deployment uses for TURN.
PRIVATE_BACKENDS=(postgres valkey seaweedfs seaweedfs-filer livekit coturn clamav)
# The Services the stateful layer publishes. This gate renders shared, the two
# slots and migrations -- not ../stateful -- so a ConfigMap endpoint naming one
# of these is correct even though nothing here renders it.
#
# The list is not a second source of truth: scripts/ci/prod-stateful-check.sh
# asserts that the stateful overlay publishes exactly these names, so a Service
# renamed there fails that gate immediately rather than silently widening this
# one. livekit is deliberately not among them -- it is not a Service in this
# cluster at all.
STATEFUL_SERVICES=(postgres valkey seaweedfs seaweedfs-filer)

fail() { echo "  [FAIL] $*" >&2; ERRORS=$((ERRORS + 1)); }
ok() { echo "  [OK]   $*"; }

render() {
  local overlay="$1" output="$2" warnings="$2.warnings"
  if command -v kustomize >/dev/null 2>&1; then
    kustomize build "$overlay" >"$output" 2>"$warnings"
  else
    KUBECONFIG=/dev/null kubectl kustomize "$overlay" >"$output" 2>"$warnings"
  fi
  [[ -s "$output" ]] || return 1
  [[ ! -s "$warnings" ]] || { cat "$warnings" >&2; return 1; }
}

query() { python3 "$QUERY" "$ALL" "$1"; }

check_rendering() {
  local unit name
  echo "--- rendering ---"
  for unit in shared slots/blue slots/green migrations .; do
    name="${unit//\//-}"
    [[ "$unit" != "." ]] || name=all
    if render "$PROD_OVERLAY/$unit" "$RENDER_DIR/$name.yaml"; then
      ok "renders: $name"
    else
      fail "does not render: $name"
    fi
  done
}

check_slot_membership() {
  local slot service deployment
  echo "--- slot membership ---"
  for slot in "${SLOTS[@]}"; do
    for service in "${STABLE_SERVICES[@]}"; do
      query 'Deployment|name' | grep -Fxq "$service-$slot" ||
        fail "$service is missing from slot $slot"
    done
  done
  ok "both slots hold every release workload"
  while IFS= read -r deployment; do
    case "$deployment" in
      *-blue | *-green) ;;
      clamav | upload-guard) ;;
      *) fail "Deployment '$deployment' runs in production without a release slot" ;;
    esac
  done < <(query 'Deployment|name')
  ok "no release workload runs unslotted"
}

check_service_selectors() {
  local service slot selector pinned
  echo "--- service selectors ---"
  for service in "${STABLE_SERVICES[@]}"; do
    selector="$(query "Service|$service|selector-slot")"
    case "$selector" in
      blue | green) ok "service/$service selects exactly one slot ($selector)" ;;
      "") fail "service/$service has no release-slot selector; it would serve both slots at once" ;;
      *) fail "service/$service selects an unknown slot '$selector'" ;;
    esac
    for slot in "${SLOTS[@]}"; do
      pinned="$(query "Service|$service-$slot|selector-slot")"
      [[ "$pinned" == "$slot" ]] ||
        fail "service/$service-$slot selects '$pinned'; it must always select $slot"
    done
  done
  for service in "${SHARED_WORKLOADS[@]}"; do
    [[ -z "$(query "Service|$service|selector-slot")" ]] ||
      fail "service/$service is shared and must not carry a release-slot selector"
  done
  ok "shared services carry no slot selector"
}

# The set of Services an Ingress may legitimately reach. Anything outside it is
# a backend nobody reviewed, which is how a public host quietly acquires a route
# to something it should not serve.
ingress_is_slot_backend() {
  [[ "$1" == *-blue || "$1" == *-green ]]
}

# Public hosts carry production traffic and must reach only the stable names,
# whose selectors are what a cutover moves.
check_stable_ingress_routing() {
  local ingress backend
  while IFS='|' read -r ingress backend; do
    case "$ingress" in
      nchat-prod-preview-*) continue ;;
    esac
    if ingress_is_slot_backend "$backend"; then
      fail "public ingress $ingress points at per-slot service '$backend'; it must use a stable name"
      continue
    fi
    ingress_backend_is_allowed "$backend" ||
      fail "public ingress $ingress points at unexpected backend '$backend'"
  done < <(query 'Ingress|backends')
  ok "public ingresses reach only stable, expected Services"
}

# Preview hosts exist to exercise a candidate, so they must reach that slot's own
# Services and never the stable ones -- a preview that resolved to a stable name
# would be testing whatever is already live.
check_preview_ingress_routing() {
  local ingress backend slot
  while IFS='|' read -r ingress backend; do
    case "$ingress" in
      nchat-prod-preview-*) ;;
      *) continue ;;
    esac
    slot="${ingress##*-}"
    if [[ "$backend" != *"-$slot" ]]; then
      fail "preview $ingress points at '$backend', which is not a $slot workload"
      continue
    fi
    ingress_backend_is_allowed "${backend%-"$slot"}" ||
      fail "preview $ingress points at unexpected backend '$backend'"
  done < <(query 'Ingress|backends')
  ok "previews reach only their own slot's Services"
}

ingress_backend_is_allowed() {
  local backend="$1" allowed
  for allowed in "${STABLE_SERVICES[@]}" "${SHARED_WORKLOADS[@]}"; do
    [[ "$backend" == "$allowed" ]] && return 0
  done
  return 1
}

# Every preview host, chat and administrative alike, sits behind the allowlist.
check_preview_access_control() {
  local ingress middlewares allowlist
  for ingress in $(query 'Ingress|name'); do
    case "$ingress" in
      nchat-prod-preview-*) ;;
      *) continue ;;
    esac
    middlewares="$(query "Ingress|$ingress|middlewares")"
    [[ "$middlewares" == *preview-allowlist* ]] ||
      fail "preview ingress $ingress is not behind the preview-allowlist middleware"
  done
  allowlist="$(query 'Middleware|preview-allowlist|source-range')"
  [[ -n "$allowlist" ]] || fail "preview-allowlist declares no sourceRange"
  [[ "$allowlist" != *0.0.0.0/0* ]] || fail "preview-allowlist is open to the world"
  ok "every preview is restricted in the configuration, not only in a comment"
}

# Both slots must be previewable, chat and console, or a candidate cannot be
# validated before it takes traffic.
require_preview_host() {
  local expected="$1"
  query 'Ingress|name' | grep -Fxq "$expected" ||
    fail "no preview host for $expected; that slot cannot be validated before cutover"
}

check_preview_coverage() {
  local slot
  for slot in "${SLOTS[@]}"; do
    require_preview_host "nchat-prod-preview-$slot"
    require_preview_host "nchat-prod-preview-admin-$slot"
  done
  ok "chat and administrative previews exist for both slots"
}

# Nothing shared is a public entrance. The nine release workloads reach the
# outside through their stable Services; everything else in this list -- the
# database, the cache, the object store, the media plane -- is either internal
# or, in LiveKit's case, not in this cluster to begin with.
check_private_backends_are_not_published() {
  local ingress backend private
  echo "--- private backends ---"
  while IFS='|' read -r ingress backend; do
    for private in "${PRIVATE_BACKENDS[@]}"; do
      if [[ "$backend" == "$private" ]]; then
        fail "ingress $ingress publishes '$backend', which must never leave the cluster"
      fi
    done
  done < <(query 'Ingress|backends')
  ok "no shared dependency is published through an Ingress"
}

check_ingress_routing() {
  echo "--- ingress routing ---"
  check_stable_ingress_routing
  check_preview_ingress_routing
  check_preview_coverage
  check_preview_access_control
  check_private_backends_are_not_published
}

# An NChat release image IS the release: it must be a digest, or the committed
# placeholder deploy.sh rewrites into one (validate_rendered_placeholders refuses
# a manifest where that did not happen, so the placeholder never reaches a
# cluster). A third-party image is not in the release rotation and only has to
# be immutable.
release_image_is_pinned() {
  case "$1" in
    *@sha256:* | *:sha-placeholder) return 0 ;;
  esac
  return 1
}

image_tag_is_mutable() {
  case "$1" in
    *:latest | *:main | *:master | *:develop | *:stable | *:prod) return 0 ;;
  esac
  return 1
}

check_workload_images() {
  local deployment image
  while IFS='|' read -r deployment image; do
    if image_tag_is_mutable "$image"; then
      fail "$deployment uses a mutable tag: $image"
      continue
    fi
    check_single_image "$deployment" "$image"
  done < <(query 'Deployment|images')
  ok "release images are immutable and no workload uses a mutable tag"
}

check_single_image() {
  local deployment="$1" image="$2"
  case "$image" in
    ghcr.io/nicrepository/nchat/*)
      release_image_is_pinned "$image" ||
        fail "release image '$image' on $deployment is neither a digest nor the committed placeholder"
      ;;
    *:*) ;;
    *) fail "$deployment image '$image' carries no tag or digest at all" ;;
  esac
}

# Every workload of a slot must be stamped with the release it belongs to, so a
# deploy that reached only some of them is visible instead of hidden behind
# whichever service happened to update.
check_release_metadata() {
  local slot service missing
  for slot in "${SLOTS[@]}"; do
    missing=0
    for service in "${STABLE_SERVICES[@]}"; do
      [[ -n "$(query "Deployment|$service-$slot|release-sha")" ]] || missing=$((missing + 1))
    done
    [[ "$missing" -eq 0 ]] ||
      fail "$missing workload(s) in slot $slot carry no nchat.io/release-sha annotation"
  done
  ok "every slot workload carries a release identity annotation"
}

check_release_identity() {
  echo "--- release identity ---"
  check_workload_images
  check_release_metadata
}

check_slot_equivalence() {
  echo "--- slot equivalence ---"
  if python3 "$QUERY" "$ALL" 'slots|equivalent'; then
    ok "blue and green are structurally identical apart from their images"
  else
    fail "blue and green differ in more than their release images"
  fi
}

check_required_true() {
  local key value
  for key in "$@"; do
    value="$(query "ConfigMap|nchat-config|data.$key")"
    if [[ "$value" == "true" ]]; then
      ok "$key=true"
    else
      fail "$key is '${value:-unset}'; production requires 'true'"
    fi
  done
}

check_scanner() {
  local scanner host
  scanner="$(query 'ConfigMap|nchat-config|data.FILE_MALWARE_SCANNER_ADDRESS')"
  [[ -n "$scanner" ]] || { fail "FILE_MALWARE_SCANNER_ADDRESS is unset while uploads are enabled"; return; }
  host="${scanner%%:*}"
  if query 'Service|name' | grep -Fxq "$host"; then
    ok "malware scanner points at the rendered '$host' Service"
  else
    fail "FILE_MALWARE_SCANNER_ADDRESS names '$host', which this overlay does not render"
  fi
}

check_livekit() {
  local connect_src slot
  connect_src="$(query 'ConfigMap|nchat-config|data.NCHAT_WEB_LIVEKIT_CONNECT_SRC')"
  [[ -n "$connect_src" ]] ||
    fail "LIVEKIT_ENABLED=true with an empty NCHAT_WEB_LIVEKIT_CONNECT_SRC leaves the CSP blocking every call"
  [[ "$connect_src" != *"*"* ]] || fail "NCHAT_WEB_LIVEKIT_CONNECT_SRC must not widen the CSP with a wildcard"
  # base pins the variable to an empty literal and an explicit env entry beats
  # envFrom, so a slot that failed to override it would render LIVEKIT_ENABLED=true
  # beside a CSP that blocks the very connection the feature needs.
  for slot in "${SLOTS[@]}"; do
    [[ "$(query "Deployment|nchat-web-$slot|livekit-env")" == "configMapKeyRef" ]] ||
      fail "nchat-web-$slot does not read NCHAT_WEB_LIVEKIT_CONNECT_SRC from nchat-config"
  done
  ok "both slots read one LiveKit allowlist from the shared ConfigMap"
}

# The capacity preflight refuses to judge a dimension the quota does not declare,
# and an inconclusive dimension blocks a deploy. Leaving this out of the overlay
# would therefore make every production release wait on someone patching the
# quota by hand, which is exactly the kind of undocumented manual step this
# overlay exists to remove.
check_quota_dimensions() {
  local value
  for value in requests.cpu requests.memory pods requests.ephemeral-storage; do
    check_quota_field "$value"
  done
}

check_quota_field() {
  local field="$1" value
  value="$(query "ResourceQuota|nchat-prod-quota|hard.$field")"
  case "$value" in
    "") fail "the production ResourceQuota does not declare $field" ;;
    *REPLACE_ME*) fail "$field is still a placeholder in the production ResourceQuota" ;;
    0 | 0Gi | 0Mi | 0Ki | "0") fail "$field is zero in the production ResourceQuota" ;;
    *) ok "quota declares $field=$value" ;;
  esac
}

# The address media-service hands the browser, and dials itself.
#
# There is one variable for both because the service has one: LIVEKIT_API_URL is
# returned to the client as the token response's serverUrl AND probed by the
# critical `livekit-api` readiness check. An in-cluster address here would make
# the pod ready and every browser fail, which is why the value is required to be
# a public wss:// address rather than merely non-empty.
#
# It must also NOT be this deployment's own host. The media plane is external --
# one shared LiveKit on AWS, used by nchat-dev and by both slots -- and
# the browser dials it directly. A URL on the application host would mean WebRTC
# signalling had been routed through this deployment's Traefik, which is the
# architecture this gate exists to keep out.
livekit_url_host() {
  local host="${1#*://}"
  printf '%s' "${host%%/*}"
}

check_livekit_api_url() {
  local url="$1" app_host="$2" host
  case "$url" in
    "") fail "LIVEKIT_API_URL is unset while LIVEKIT_ENABLED=true; media-service will not start"; return ;;
    *REPLACE_ME*) return ;;
    wss://*) ;;
    *) fail "LIVEKIT_API_URL is '$url'; the browser is handed this value, so it must be wss://"; return ;;
  esac
  host="$(livekit_url_host "$url")"
  [[ "$host" != "$app_host" ]] ||
    fail "LIVEKIT_API_URL is '$url', on the application's own host; the media plane is external and the browser must dial it directly, not through this deployment's gateway"
  # config.validLiveKitAPIURL accepts no path, and the LiveKit SDK appends its
  # own -- so a path here is a value media-service would refuse to start with.
  [[ "$url" == "wss://$host" ]] ||
    fail "LIVEKIT_API_URL is '$url'; it must be scheme and host only, because the LiveKit SDK appends its own paths"
}

# The CSP allowlist and the SDK's URL are different things -- one carries both
# schemes, the other one address -- but they describe one connection, to one
# external host. A connect-src naming anything else blocks the very call the
# token enables.
check_livekit_connect_src_agrees() {
  local connect_src="$1" url="$2" host entry
  case "$connect_src$url" in
    *REPLACE_ME*) return ;;
  esac
  host="$(livekit_url_host "$url")"
  for entry in $connect_src; do
    [[ "$entry" == *"://$host" || "$entry" == *"://$host/"* ]] ||
      fail "NCHAT_WEB_LIVEKIT_CONNECT_SRC allows '$entry', which is not the LiveKit host '$host' that LIVEKIT_API_URL names"
  done
  # Both schemes, because the SDK uses both against the same host: wss:// for
  # the signalling socket and https:// for the requests around it. A CSP with
  # only one of them fails at a point that looks like a call bug -- the token
  # is issued, the browser simply never completes the connection.
  [[ " $connect_src " == *" wss://$host "* ]] ||
    fail "NCHAT_WEB_LIVEKIT_CONNECT_SRC does not allow wss://$host; the LiveKit signalling socket would be blocked by the CSP"
  [[ " $connect_src " == *" https://$host "* ]] ||
    fail "NCHAT_WEB_LIVEKIT_CONNECT_SRC does not allow https://$host; the SDK's HTTPS requests to LiveKit would be blocked by the CSP"
}

# Both slots inherit the whole ConfigMap, which is how one value reaches two
# release copies of media-service without being restated per slot. It is also
# what makes a cutover safe for a call in progress: Blue and Green hand the
# browser the same external LiveKit, with the same credentials, so promoting a
# release does not move anyone between media servers.
check_livekit_reaches_both_slots() {
  local slot
  for slot in "${SLOTS[@]}"; do
    [[ "$(query "Deployment|media-service-$slot|configmap-envfrom")" == *nchat-config* ]] ||
      fail "media-service-$slot does not take its environment from nchat-config; it would have no LiveKit URL"
  done
}

check_livekit_client_contract() {
  local app_host url connect_src
  # The application's own public host, read from the value the overlay already
  # derives from topology rather than from a second source that could disagree.
  app_host="$(livekit_url_host "$(query 'ConfigMap|nchat-config|data.AUTH_PUBLIC_WEB_BASE_URL')")"
  url="$(query 'ConfigMap|nchat-config|data.LIVEKIT_API_URL')"
  connect_src="$(query 'ConfigMap|nchat-config|data.NCHAT_WEB_LIVEKIT_CONNECT_SRC')"
  check_livekit_api_url "$url" "$app_host"
  check_livekit_connect_src_agrees "$connect_src" "$url"
  check_livekit_reaches_both_slots
  ok "both slots hand the browser one external LiveKit, and the CSP allows exactly that host"
}

# A ConfigMap key naming a Service the overlay does not render is an address
# that resolves to nothing. It costs nothing at runtime and everything later,
# when someone reads it as a statement that the dependency exists.
service_exists_in_production() {
  local host="$1" known
  query 'Service|name' | grep -Fxq "$host" && return 0
  for known in "${STATEFUL_SERVICES[@]}"; do
    [[ "$host" == "$known" ]] && return 0
  done
  return 1
}

endpoint_host() {
  local host="${1#*://}"
  host="${host%%/*}"
  printf '%s' "${host%%:*}"
}

check_no_dangling_service_endpoints() {
  local key value host
  for key in SEAWEEDFS_FILER_URL SEAWEEDFS_S3_ENDPOINT; do
    value="$(query "ConfigMap|nchat-config|data.$key")"
    [[ -n "$value" ]] || continue
    host="$(endpoint_host "$value")"
    service_exists_in_production "$host" ||
      fail "$key names '$host', which neither this overlay nor the stateful layer renders"
  done
  ok "every service endpoint in nchat-config names a Service production actually has"
}

check_configuration() {
  echo "--- production configuration ---"
  check_quota_dimensions
  check_required_true VALKEY_WS_BROADCAST_ENABLED FILE_UPLOADS_ENABLED \
    FILE_MALWARE_SCAN_REQUIRED LIVEKIT_ENABLED OIDC_ENABLED
  check_scanner
  check_livekit
  check_livekit_client_contract
  check_no_dangling_service_endpoints
}

check_claims_are_shared() {
  local claim
  while IFS= read -r claim; do
    [[ "$claim" != *-blue && "$claim" != *-green ]] ||
      fail "PersistentVolumeClaim '$claim' is duplicated per slot; storage must be shared"
  done < <(query 'PersistentVolumeClaim|name')
  ok "no persistent claim is duplicated per slot"
}

check_no_dev_secrets() {
  local reference
  while IFS= read -r reference; do
    [[ "$reference" != *nchat-dev* ]] ||
      fail "production references the development secret '$reference'"
  done < <(query 'secret-refs')
  ok "no production workload references a development secret"
}

check_namespaces() {
  local name
  while IFS= read -r name; do
    [[ "$name" == nchat-prod ]] || fail "resource rendered into namespace '$name', expected nchat-prod"
  done < <(query 'namespaces')
  ok "every resource renders into nchat-prod"
}

check_shared_state() {
  echo "--- shared dependencies and secrets ---"
  check_claims_are_shared
  check_no_dev_secrets
  check_namespaces
}

check_workload_probes() {
  local deployment="$1" probes
  probes="$(query "Deployment|$deployment|probes")"
  [[ "$probes" == *readiness* ]] || fail "$deployment declares no readinessProbe"
  [[ "$probes" == *liveness* ]] || fail "$deployment declares no livenessProbe"
}

check_slot_probes() {
  local slot="$1" service
  for service in "${STABLE_SERVICES[@]}"; do
    check_workload_probes "$service-$slot"
  done
}

check_probes_and_budgets() {
  local slot budget components
  echo "--- probes and disruption budgets ---"
  for slot in "${SLOTS[@]}"; do
    check_slot_probes "$slot"
  done
  ok "every slot workload declares readiness and liveness probes"
  # A budget must describe one unit of availability. One spanning several
  # workloads is satisfied by the others while a single-replica service is
  # evicted -- which is exactly what a slot-wide budget did.
  while IFS='|' read -r budget components; do
    [[ "$(wc -w <<<"$components")" -eq 1 ]] ||
      fail "PodDisruptionBudget '$budget' selects several components ($components); it guarantees none of them"
  done < <(query 'PodDisruptionBudget|components')
  ok "every disruption budget covers exactly one workload"
}

main() {
  echo "=== production blue/green check ==="
  echo
  check_rendering
  [[ "$ERRORS" -eq 0 ]] || { echo "production overlay does not render; aborting." >&2; exit 1; }
  ALL="$RENDER_DIR/all.yaml"
  echo
  check_slot_membership; echo
  check_service_selectors; echo
  check_ingress_routing; echo
  check_release_identity; echo
  check_slot_equivalence; echo
  check_configuration; echo
  check_shared_state; echo
  check_probes_and_budgets; echo
  [[ "$ERRORS" -eq 0 ]] || { echo "production blue/green check failed with $ERRORS error(s)." >&2; exit 1; }
  echo "production blue/green check passed."
}

# Sourcing this file defines the checks without running them, so
# scripts/ci/test_prod_blue_green_check.sh can drive individual assertions
# against doctored inputs and prove they actually fail. Same pattern as
# scripts/ci/blue-green-migration-gate.sh.
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  main "$@"
fi
