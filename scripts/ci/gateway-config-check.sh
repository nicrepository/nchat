#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
COMPOSE_FILE="$ROOT_DIR/infra/compose/compose.dev.yml"
ENV_EXAMPLE="$ROOT_DIR/infra/compose/.env.dev.example"
ENV_FILE="$ROOT_DIR/infra/compose/.env.dev"
TRAEFIK_STATIC_CONFIG="$ROOT_DIR/infra/traefik/local/traefik.yml"
TRAEFIK_DYNAMIC_CONFIG="$ROOT_DIR/infra/traefik/local/dynamic.yml"
LOCAL_CERT_DIR="$ROOT_DIR/infra/traefik/local/certs"
TEMP_CERT_DIR=""
# The nchat-dev-server overlay reads this env file through a configMapGenerator,
# so kustomize build fails without it. It is deliberately not versioned — it
# names one specific machine — which means a clean checkout does not have it and
# the contract check below cannot render that overlay. The example is versioned
# and carries the same keys, so it stands in for the real thing here: this gate
# validates route shape, not deployment values.
NCHAT_DEV_TOPOLOGY_EXAMPLE="$ROOT_DIR/infra/k8s/overlays/nchat-dev-server/topology.env.example"
NCHAT_DEV_TOPOLOGY_FILE="$ROOT_DIR/infra/k8s/overlays/nchat-dev-server/topology.env"

require_file() {
  if [ ! -f "$1" ]; then
    echo "Required gateway file missing: ${1#$ROOT_DIR/}" >&2
    exit 1
  fi
}

require_file "$COMPOSE_FILE"
require_file "$ENV_EXAMPLE"
require_file "$TRAEFIK_STATIC_CONFIG"
require_file "$TRAEFIK_DYNAMIC_CONFIG"
require_file "$NCHAT_DEV_TOPOLOGY_EXAMPLE"

if git -C "$ROOT_DIR" ls-files --error-unmatch infra/compose/.env.dev >/dev/null 2>&1; then
  echo "infra/compose/.env.dev must not be versioned." >&2
  exit 1
fi

if ! grep -q 'rewrite-auth-api-prefix:' "$TRAEFIK_DYNAMIC_CONFIG"; then
  echo "Local gateway must rewrite /api/auth/* to auth-service /auth/* paths." >&2
  exit 1
fi

if ! grep -q 'nchat-auth-health:' "$TRAEFIK_DYNAMIC_CONFIG"; then
  echo "Local gateway must keep /api/auth/healthz probe routing explicit." >&2
  exit 1
fi

if ! grep -q 'nchat-chat-health:' "$TRAEFIK_DYNAMIC_CONFIG"; then
  echo "Local gateway must keep /api/chat/healthz probe routing explicit." >&2
  exit 1
fi

if sed -n '/nchat-chat:/,/nchat-chat-https:/p' "$TRAEFIK_DYNAMIC_CONFIG" | grep -q 'strip-chat-prefix'; then
  echo "Local gateway must not strip /api/chat for normal chat API routes." >&2
  exit 1
fi

if sed -n '/nchat-chat-https:/,/nchat-files:/p' "$TRAEFIK_DYNAMIC_CONFIG" | grep -q 'strip-chat-prefix'; then
  echo "Local gateway must not strip /api/chat for normal HTTPS chat API routes." >&2
  exit 1
fi

# RF-32 (issue #458): the gateway must never buffer an upload body.
#
# Traefik's buffering middleware is the only native way to cap a request body,
# and it does so by reading the whole body first -- into memory up to
# memRequestBodyBytes and onto the container's disk beyond it. On the attachment
# routes that happens *before* file-service authenticates, authorises or rate
# limits anything, so an unauthenticated client could fill the gateway's
# temporary storage at will. Size is enforced by file-service, which streams the
# body under http.MaxBytesReader after authenticating; the gateway only routes.
#
# This guard is structural rather than a snapshot: it fails on the settings
# themselves, wherever they appear, so the middleware cannot be reintroduced
# under a different name.
BUFFERING_PATTERN='buffering:|maxRequestBodyBytes|memRequestBodyBytes'
# Comment lines are excluded: the surrounding files explain *why* the middleware
# is banned, and a guard that trips on its own rationale would push maintainers
# to delete the explanation.
buffering_hits="$(
  grep -REn "$BUFFERING_PATTERN" \
    "$TRAEFIK_STATIC_CONFIG" "$TRAEFIK_DYNAMIC_CONFIG" \
    "$ROOT_DIR/infra/k8s" |
    grep -vE '^[^:]+:[0-9]+:[[:space:]]*#' || true
)"
if [ -n "$buffering_hits" ]; then
  echo "Request-body buffering must not be configured on any gateway route:" >&2
  echo "$buffering_hits" >&2
  echo "file-service is the authoritative size limit; buffering before" >&2
  echo "authentication lets unauthenticated clients exhaust gateway disk." >&2
  exit 1
fi

# ── RF-32 upload guard ───────────────────────────────────────────────────────
#
# Banning Traefik's buffering is only half the requirement: something still has
# to refuse an absurd body at the edge, and it has to do so while streaming.
# The checks below assert that the non-buffering cap is present, that it is the
# one number the Go package defines, and that upload traffic actually reaches it
# — a guard nothing is routed through would pass a naive "config exists" check
# while protecting nothing.
UPLOAD_GUARD_CONF="$ROOT_DIR/infra/k8s/base/services/upload-guard/nginx.conf.template"
UPLOAD_GUARD_DEPLOYMENT="$ROOT_DIR/infra/k8s/base/services/upload-guard/deployment.yaml"
UPLOAD_GUARD_SERVICE="$ROOT_DIR/infra/k8s/base/services/upload-guard/service.yaml"
UPLOAD_POLICY_GO="$ROOT_DIR/libs/go/platform/uploadpolicy/uploadpolicy.go"

require_file "$UPLOAD_GUARD_CONF"
require_file "$UPLOAD_GUARD_DEPLOYMENT"
require_file "$UPLOAD_GUARD_SERVICE"

# The hard cap is 512 MiB + 8 KiB of multipart overhead. It is duplicated into
# nginx configuration by necessity, so it is checked against the Go constants
# that define it rather than hardcoded twice.
HARD_CAP=536879104
if ! grep -q 'MaxMaxUploadBytes int64 = 512 << 20' "$UPLOAD_POLICY_GO" ||
  ! grep -q 'MultipartOverheadBytes int64 = 8 << 10' "$UPLOAD_POLICY_GO"; then
  echo "uploadpolicy must define a 512 MiB ceiling and 8 KiB multipart overhead." >&2
  exit 1
fi
if ! grep -q "client_max_body_size ${HARD_CAP};" "$UPLOAD_GUARD_CONF"; then
  echo "Upload guard must cap the request body at ${HARD_CAP} bytes" >&2
  echo "(uploadpolicy.GatewayHardCapBytes = 512 MiB + 8 KiB)." >&2
  exit 1
fi

# The two properties that make this a streaming cap rather than a second
# buffering middleware.
if ! grep -q 'proxy_request_buffering off;' "$UPLOAD_GUARD_CONF"; then
  echo "Upload guard must stream the request body (proxy_request_buffering off)." >&2
  exit 1
fi
if ! grep -q 'proxy_http_version 1.1;' "$UPLOAD_GUARD_CONF"; then
  echo "Upload guard must use HTTP/1.1 upstream so chunked bodies stay chunked." >&2
  exit 1
fi
# A retried POST would need the body replayed, which streaming makes impossible
# and which could create the same attachment twice.
if ! grep -q 'proxy_next_upstream off;' "$UPLOAD_GUARD_CONF"; then
  echo "Upload guard must not retry POSTs (proxy_next_upstream off)." >&2
  exit 1
fi
if grep -qE '\$request_body|\$http_authorization|\$http_cookie' "$UPLOAD_GUARD_CONF"; then
  echo "Upload guard must never log request bodies, tokens or cookies." >&2
  exit 1
fi

# Upload traffic must actually be routed through the guard, in the local gateway
# and in every overlay, and must be narrowed to the upload routes so nothing
# else pays the hop.
#
# The assertions are made **inside each upload router's own block**, never over
# the whole file. A file-wide grep would be satisfied by the guard being defined
# somewhere while the upload route quietly pointed straight at file-service —
# precisely the regression worth catching.

# traefik_router_block prints one router's YAML block from the local dynamic
# configuration: from its key to the next key at the same indentation.
traefik_router_block() {
  awk -v router="    $1:" '
    $0 == router { inside = 1; next }
    inside && /^    [a-zA-Z]/ { exit }
    inside { print }
  ' "$TRAEFIK_DYNAMIC_CONFIG"
}

# yaml_document prints the one --- separated document containing needle.
yaml_document() {
  awk -v needle="$2" '
    /^---$/ { if (found) exit; doc = ""; next }
    { doc = doc $0 "\n"; if (index($0, needle)) found = 1 }
    END { if (found) printf "%s", doc }
  ' "$1"
}

assert_upload_route() {
  local label="$1" block="$2"
  if [ -z "$block" ]; then
    echo "${label}: no upload route found at all." >&2
    exit 1
  fi
  if ! grep -qF 'PathRegexp(`^/api/files/(channels|dm)/[^/]+/attachments$`)' <<<"$block"; then
    echo "${label}: the guard route must be narrowed to the two upload paths." >&2
    exit 1
  fi
  if ! grep -qF 'Method(`POST`)' <<<"$block"; then
    echo "${label}: the guard route must be narrowed to POST." >&2
    exit 1
  fi
  # Match the actual reference, not the word: a component label reading
  # "upload-guard" must not be able to satisfy a routing assertion.
  if ! grep -qE '(service: upload-guard$|- name: upload-guard$)' <<<"$block"; then
    echo "${label}: uploads must be routed through the upload guard, not straight" >&2
    echo "to file-service — the guard is the only streaming body cap." >&2
    exit 1
  fi
  if ! grep -qE '(^ *- upload-inflight$|- name: upload-inflight$)' <<<"$block"; then
    echo "${label}: this upload route must carry the inFlightReq middleware." >&2
    exit 1
  fi
}

# Both entry points locally: an http-only guard would leave the real one open.
for router in nchat-files-upload nchat-files-upload-https; do
  assert_upload_route "local gateway ${router}" "$(traefik_router_block "$router")"
done

# And the non-upload /api/files routers must keep reaching file-service directly,
# so health, download and listing do not pay the hop.
for router in nchat-files nchat-files-https; do
  block="$(traefik_router_block "$router")"
  if grep -qE '(service: upload-guard$|- name: upload-guard$)' <<<"$block"; then
    echo "local gateway ${router}: only upload routes belong behind the guard." >&2
    exit 1
  fi
done

for overlay in k3s-dev k3s-staging nchat-dev-server; do
  file="$ROOT_DIR/infra/k8s/overlays/$overlay/ingress.yaml"
  assert_upload_route "$overlay" "$(yaml_document "$file" 'kind: IngressRoute')"
done

# The concurrency ceiling has to be a real number, not a commented intention,
# in the local gateway and in every overlay.
if ! grep -A 1 'inFlightReq:' "$TRAEFIK_DYNAMIC_CONFIG" | grep -q 'amount:'; then
  echo "Local gateway inFlightReq must set an amount." >&2
  exit 1
fi
for overlay in k3s-dev k3s-staging nchat-dev-server; do
  if ! grep -A 1 'inFlightReq:' "$ROOT_DIR/infra/k8s/overlays/$overlay/ingress.yaml" |
    grep -q 'amount:'; then
    echo "${overlay}: inFlightReq must set an amount." >&2
    exit 1
  fi
done

# The guard runs unprivileged with a read-only root filesystem and a pinned
# image: it terminates untrusted, unauthenticated request bodies.
if grep -qE 'image:.*:latest' "$UPLOAD_GUARD_DEPLOYMENT"; then
  echo "Upload guard image must be pinned, never :latest." >&2
  exit 1
fi
for setting in 'runAsNonRoot: true' 'readOnlyRootFilesystem: true' 'automountServiceAccountToken: false'; do
  if ! grep -q "$setting" "$UPLOAD_GUARD_DEPLOYMENT"; then
    echo "Upload guard deployment must set '${setting}'." >&2
    exit 1
  fi
done

# Both temporary files are tracked from here on, so a single cleanup covers
# every exit path — including the early returns below when Docker is absent.
created_env=0
created_topology_env=0
rendered_overlay_file=""

# One cleanup, one trap. Each branch removes only what this script created, so a
# developer's own .env.dev or topology.env survives the run untouched.
cleanup() {
  if [ "$created_env" -eq 1 ]; then
    rm -f "$ENV_FILE"
  fi
  if [ "$created_topology_env" -eq 1 ]; then
    rm -f "$NCHAT_DEV_TOPOLOGY_FILE"
  fi
  if [ -n "$TEMP_CERT_DIR" ]; then
    rm -rf "$TEMP_CERT_DIR"
  fi
  if [ -n "$rendered_overlay_file" ]; then
    rm -f "$rendered_overlay_file"
  fi
}
trap cleanup EXIT

if [ ! -f "$NCHAT_DEV_TOPOLOGY_FILE" ]; then
  cp "$NCHAT_DEV_TOPOLOGY_EXAMPLE" "$NCHAT_DEV_TOPOLOGY_FILE"
  created_topology_env=1
fi

# ---------------------------------------------------------------------------
# The upload guard is an L7 boundary, and only L7 can enforce it (issue #483).
#
# A NetworkPolicy cannot express "this POST must go through the guard": it
# matches pods and ports, never methods or paths, and Traefik legitimately needs
# file-service on the same /api/files prefix for download, Range, preview and
# listing. So the property "every attachment upload passes the guard" lives
# entirely in the Traefik routing table, and it is asserted here — on the
# manifest that is actually applied, not only on the source file that produces
# it. The assertions above cover route shape in the sources; these cover what
# survives kustomize.
# ---------------------------------------------------------------------------
NCHAT_DEV_OVERLAY="$ROOT_DIR/infra/k8s/overlays/nchat-dev-server"
RENDERED_OVERLAY="$(mktemp "${TMPDIR:-/tmp}/nchat-gateway-render.XXXXXX")"
rendered_overlay_file="$RENDERED_OVERLAY"

if command -v kustomize >/dev/null 2>&1; then
  kustomize build "$NCHAT_DEV_OVERLAY" >"$RENDERED_OVERLAY"
elif command -v kubectl >/dev/null 2>&1; then
  KUBECONFIG=/dev/null kubectl kustomize "$NCHAT_DEV_OVERLAY" >"$RENDERED_OVERLAY"
else
  echo "error: kustomize or kubectl is required to validate the rendered upload route." >&2
  exit 1
fi

UPLOAD_PATH_REGEXP='PathRegexp(`^/api/files/(channels|dm)/[^/]+/attachments$`)'

# Exactly one route in the applied manifest may claim the upload paths. Two
# would mean the winner is decided by priority arithmetic nobody reviewed.
upload_route_count="$(grep -Fc "$UPLOAD_PATH_REGEXP" "$RENDERED_OVERLAY" || true)"
if [ "$upload_route_count" -ne 1 ]; then
  echo "Rendered nchat-dev must contain exactly one upload route, found $upload_route_count." >&2
  exit 1
fi

# upload_route_block prints the IngressRoute document that owns the upload rule.
upload_route_block="$(
  awk -v needle="$UPLOAD_PATH_REGEXP" '
    /^---$/ { if (found) exit; doc = ""; next }
    { doc = doc $0 "\n"; if (index($0, needle)) found = 1 }
    END { if (found) printf "%s", doc }
  ' "$RENDERED_OVERLAY"
)"

if ! printf '%s' "$upload_route_block" | grep -Fq 'Method(`POST`)'; then
  echo "Rendered upload route must be narrowed to POST." >&2
  exit 1
fi
# The backend, matched as a real service reference: a component label reading
# "upload-guard" must not be able to satisfy this.
if ! printf '%s' "$upload_route_block" | grep -Eq '^ *- name: upload-guard$'; then
  echo "Rendered upload route must send uploads to the guard, not to file-service." >&2
  exit 1
fi
if ! printf '%s' "$upload_route_block" | grep -Eq '^ *port: 8080$'; then
  echo "Rendered upload route must target the guard on port 8080." >&2
  exit 1
fi
# file-service must not be reachable through this route by any spelling.
if printf '%s' "$upload_route_block" | grep -Eq '^ *- name: file-service$'; then
  echo "Rendered upload route must not name file-service as a backend." >&2
  exit 1
fi
for middleware in strip-files-prefix upload-inflight; do
  if ! printf '%s' "$upload_route_block" | grep -Eq "^ *- name: ${middleware}$"; then
    echo "Rendered upload route must carry the ${middleware} middleware." >&2
    exit 1
  fi
done

nchat_dev_host="$(awk -F= '/^NCHAT_DEV_HOST=/ { print $2; exit }' "$NCHAT_DEV_TOPOLOGY_FILE")"

# route_inventory turns a rendered manifest into one record per public route:
#
#   kind|namespace|name|explicit_priority|backend|rule
#
# Both kinds this repository actually uses are covered, and nothing else is
# invented. For an Ingress the rule is reconstructed the way Traefik's
# Kubernetes provider builds it, from the host, path and pathType that are in
# the manifest — not from a string this script decides in advance.
route_inventory() {
  awk '
    function emit_ingress_path() {
      # Only a real path entry is a route. A rule that has been opened but has
      # no path yet is not one, and emitting it would invent a match-everything
      # route that does not exist.
      if (kind != "Ingress" || path == "") return
      rule = ""
      if (host != "") rule = "Host(`" host "`)"
      # Exact is the only pathType Traefik maps to Path(); Prefix,
      # ImplementationSpecific and an absent value all become PathPrefix, which
      # is the wider match and therefore the safe assumption.
      matcher = (pathtype == "Exact") ? "Path(`" path "`)" : "PathPrefix(`" path "`)"
      rule = (rule == "") ? matcher : rule " && " matcher
      # A path whose backend could not be read must not look like a route to
      # some other service: it is reported so the caller can refuse it.
      printf "Ingress|%s|%s|%s|%s|%s\n", ns, name, annotation_priority,
        (backend == "" ? "UNRESOLVED" : backend), rule
    }
    function emit_route() {
      if (kind != "IngressRoute" || match_rule == "") return
      printf "IngressRoute|%s|%s|%s|%s|%s\n", ns, name, route_priority, backend, match_rule
    }
    function reset_document() {
      kind=""; ns=""; name=""; annotation_priority=""
      host=""; path=""; pathtype=""; backend=""
      match_rule=""; route_priority=""; section=""
    }
    function reset_path() { path=""; pathtype=""; backend="" }
    function reset_route() { match_rule=""; route_priority=""; backend=""; section="" }
    function value(line) { sub(/^[^:]*:[[:space:]]*/, "", line); gsub(/^"|"$/, "", line); return line }

    BEGIN { reset_document() }
    /^---$/ { emit_ingress_path(); emit_route(); reset_document(); next }
    /^kind: (Ingress|IngressRoute)$/ { kind = $2; next }
    kind == "" { next }

    /^  name: / && name == "" { name = value($0); next }
    /^  namespace: / && ns == "" { ns = value($0); next }
    /^    traefik\.ingress\.kubernetes\.io\/router\.priority: / { annotation_priority = value($0); next }

    # --- Ingress -----------------------------------------------------------
    kind == "Ingress" && /^  - host: / { emit_ingress_path(); reset_path(); host = value($0); next }
    kind == "Ingress" && /^  - http:/  { emit_ingress_path(); reset_path(); host = ""; next }
    kind == "Ingress" && /^      - backend:/ { emit_ingress_path(); reset_path(); next }
    kind == "Ingress" && /^            name: / && backend == "" { backend = value($0); next }
    kind == "Ingress" && /^        path: / { path = value($0); next }
    kind == "Ingress" && /^        pathType: / { pathtype = value($0); next }

    # --- IngressRoute ------------------------------------------------------
    kind == "IngressRoute" && /^  - (kind|match): / { emit_route(); reset_route() }
    kind == "IngressRoute" && /^  - match: / { match_rule = value($0); next }
    kind == "IngressRoute" && /^    match: / { match_rule = value($0); next }
    kind == "IngressRoute" && /^    priority: / { route_priority = value($0); next }
    kind == "IngressRoute" && /^    middlewares:/ { section = "middlewares"; next }
    kind == "IngressRoute" && /^    services:/ { section = "services"; next }
    kind == "IngressRoute" && /^    - name: / { if (section == "services" && backend == "") backend = value($0); next }

    END { emit_ingress_path(); emit_route() }
  ' "$1"
}

# rule_can_match answers whether an &&-composed Traefik rule can receive a POST
# for one of the attachment paths. It returns:
#
#   0  the rule can match          2  the rule is provably disjoint
#   1  the rule cannot be classified — treated as a failure by the caller
#
# It is deliberately not a general Traefik parser. It covers the matchers this
# repository actually uses and refuses to guess about anything else, because a
# wrong "disjoint" here is a silent bypass of the upload guard.
rule_can_match() {
  local rule="$1" sample_path="$2"
  local host_matchers method_matchers path_matchers matcher literal

  # Alternation would break the "every matcher must hold" reasoning below.
  # Rather than model it, refuse to classify.
  case "$rule" in
    *'||'*) return 1 ;;
  esac
  # A regular expression on the path cannot be evaluated here at all.
  case "$rule" in
    *'PathRegexp('*) return 1 ;;
  esac

  host_matchers="$(printf '%s' "$rule" | grep -Eo 'Host\(`[^)]*`\)' || true)"
  if [ -n "$host_matchers" ]; then
    # Host() takes a comma-separated list; the route reaches us if any entry is
    # the public host.
    if ! printf '%s' "$host_matchers" | tr ',`' '\n\n' | grep -Fxq "$nchat_dev_host"; then
      return 2
    fi
  fi

  method_matchers="$(printf '%s' "$rule" | grep -Eo 'Method\(`[^)]*`\)' || true)"
  if [ -n "$method_matchers" ]; then
    if ! printf '%s' "$method_matchers" | tr ',`' '\n\n' | grep -Fxq POST; then
      return 2
    fi
  fi

  # Every path matcher has to admit the sample, because they are ANDed.
  path_matchers="$(printf '%s' "$rule" | grep -Eo 'Path(Prefix)?\(`[^`]*`\)' || true)"
  [ -n "$path_matchers" ] || return 0
  while IFS= read -r matcher; do
    [ -n "$matcher" ] || continue
    literal="${matcher#*(\`}"
    literal="${literal%\`)}"
    case "$matcher" in
      PathPrefix*)
        case "$sample_path" in
          "$literal"*) ;;
          *) return 2 ;;
        esac
        ;;
      Path*)
        [ "$sample_path" = "$literal" ] || return 2
        ;;
    esac
  done <<EOF
$path_matchers
EOF
  return 0
}

# PRIORITY_MODEL_MARGIN pads a priority this script *derived* rather than read.
#
# Traefik's default router priority is the length of the rule, and for an
# Ingress that rule is reconstructed above rather than taken from the manifest.
# The reconstruction is faithful to the provider's shape, but a modelling error
# of a few characters must not be what decides whether the guard is bypassable,
# so a derived competitor is always treated as somewhat stronger than computed.
# A priority read straight from the manifest needs no such padding.
PRIORITY_MODEL_MARGIN=64

UPLOAD_PATH_SAMPLES="/api/files/channels/00000000-0000-0000-0000-000000000000/attachments
/api/files/dm/00000000-0000-0000-0000-000000000000/attachments"

# Priority is what makes the guard win against the generic /api/files rule that
# sends everything else straight to file-service. The competitors are taken from
# the rendered manifest — every Ingress and IngressRoute that can carry an
# attachment POST to file-service — so a route added later cannot escape the
# comparison by being unknown to this script.
assert_upload_route_wins() {
  local rendered="$1"
  local route_kind route_ns route_name route_priority route_backend route_rule
  local upload_priority="" failures=0 matched unclassified sample rule_status
  local effective_priority priority_source competitors

  competitors="$(mktemp "${TMPDIR:-/tmp}/nchat-competing-routes.XXXXXX")"

  while IFS='|' read -r route_kind route_ns route_name route_priority route_backend route_rule; do
    [ -n "$route_kind" ] || continue

    # The dedicated route identifies itself by the rule it carries, not by its
    # name: renaming it must not make it invisible to this comparison.
    case "$route_rule" in
      *"$UPLOAD_PATH_REGEXP"*)
        case "$route_priority" in
          '' | *[!0-9]*)
            echo "Rendered upload route must declare a numeric priority." >&2
            rm -f "$competitors"
            return 1
            ;;
        esac
        upload_priority="$route_priority"
        continue
        ;;
    esac

    if [ "$route_backend" = UNRESOLVED ]; then
      echo "ERROR: ${route_kind}/${route_ns}/${route_name} has a path whose backend could" >&2
      echo "  not be read from the rendered manifest: ${route_rule}" >&2
      echo "A route whose destination is unknown cannot be ruled out as a bypass." >&2
      failures=$((failures + 1))
      continue
    fi
    [ "$route_backend" = file-service ] || continue

    matched=0
    unclassified=0
    for sample in $UPLOAD_PATH_SAMPLES; do
      rule_status=0
      rule_can_match "$route_rule" "$sample" || rule_status=$?
      case "$rule_status" in
        0) matched=1; break ;;
        1) unclassified=1 ;;
      esac
    done

    if [ "$unclassified" -eq 1 ]; then
      echo "ERROR: cannot prove ${route_kind}/${route_ns}/${route_name} is disjoint from" >&2
      echo "  the attachment upload paths." >&2
      echo "  rule:    ${route_rule}" >&2
      echo "  backend: ${route_backend}" >&2
      echo "A file-service route this gate cannot classify is treated as a bypass." >&2
      failures=$((failures + 1))
      continue
    fi
    [ "$matched" -eq 1 ] || continue

    if [ -n "$route_priority" ]; then
      effective_priority="$route_priority"
      priority_source=explicit
    else
      effective_priority=$((${#route_rule} + PRIORITY_MODEL_MARGIN))
      priority_source="derived from rule length +${PRIORITY_MODEL_MARGIN} margin"
    fi
    printf '%s\t%s\t%s\t%s\t%s\n' \
      "${route_kind}/${route_ns}/${route_name}" "$route_rule" "$route_backend" \
      "$effective_priority" "$priority_source" >>"$competitors"
  done <<EOF
$(route_inventory "$rendered")
EOF

  if [ -z "$upload_priority" ]; then
    echo "Rendered upload route not found in the route inventory." >&2
    rm -f "$competitors"
    return 1
  fi

  local competitor rule backend
  while IFS="$(printf '\t')" read -r competitor rule backend effective_priority priority_source; do
    [ -n "$competitor" ] || continue
    if [ "$effective_priority" -ge "$upload_priority" ]; then
      echo "ERROR: competing route ${competitor} can match an attachment POST:" >&2
      echo "  rule:               ${rule}" >&2
      echo "  backend:            ${backend}" >&2
      echo "  effective priority: ${effective_priority} (${priority_source})" >&2
      echo "  upload route:       ${upload_priority}" >&2
      echo "An attachment POST could reach file-service without the streaming cap." >&2
      failures=$((failures + 1))
    fi
  done <"$competitors"

  rm -f "$competitors"
  [ "$failures" -eq 0 ]
}

assert_upload_route_wins "$RENDERED_OVERLAY" || exit 1

# ---------------------------------------------------------------------------
# Self-tests for the comparison above.
#
# The gate decides whether an attachment POST can reach file-service, so "it
# passes today" is not evidence that it would notice tomorrow. Each case mutates
# a *copy* of the rendered manifest — the working tree is never touched — and
# asserts the expected verdict. They run in CI, so a future change that blinds
# the gate fails here rather than in production.
# ---------------------------------------------------------------------------
selftest_failures=0

expect_route_verdict() {
  local label="$1" expected="$2" mutated="$3" observed=0
  assert_upload_route_wins "$mutated" >/dev/null 2>&1 || observed=1
  if [ "$observed" -ne "$expected" ]; then
    echo "Route gate self-test '${label}' expected $([ "$expected" -eq 0 ] && echo PASS || echo FAIL)," >&2
    echo "observed $([ "$observed" -eq 0 ] && echo PASS || echo FAIL)." >&2
    selftest_failures=$((selftest_failures + 1))
  fi
  rm -f "$mutated"
}

mutated_render() {
  local copy
  copy="$(mktemp "${TMPDIR:-/tmp}/nchat-route-selftest.XXXXXX")"
  cp "$RENDERED_OVERLAY" "$copy"
  printf '%s' "$copy"
}

# A — the manifest as it stands.
case_a="$(mutated_render)"
expect_route_verdict 'valid manifest' 0 "$case_a"

# B — the dedicated route loses its priority advantage.
case_b="$(mutated_render)"
sed -i 's/^    priority: 200$/    priority: 10/' "$case_b"
expect_route_verdict 'upload route priority lowered' 1 "$case_b"

# C — the generic /api/files Ingress claims an explicit priority. This is the
# case the review flagged: nothing about the dedicated route changes, and the
# old string-comparison gate saw nothing wrong.
case_c="$(mutated_render)"
sed -i 's|^    traefik.ingress.kubernetes.io/router.entrypoints: websecure$|&\n    traefik.ingress.kubernetes.io/router.priority: "500"|' "$case_c"
expect_route_verdict 'competing Ingress annotates a higher priority' 1 "$case_c"

# D — a brand-new Ingress for /api/files, with a priority that ties.
case_d="$(mutated_render)"
cat >>"$case_d" <<EOF
---
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  annotations:
    traefik.ingress.kubernetes.io/router.priority: "200"
  name: nchat-dev-files-extra
  namespace: nchat-dev
spec:
  ingressClassName: traefik
  rules:
  - host: ${nchat_dev_host}
    http:
      paths:
      - backend:
          service:
            name: file-service
            port:
              name: http
        path: /api/files
        pathType: Prefix
EOF
expect_route_verdict 'new competing Ingress ties on priority' 1 "$case_d"

# E — a brand-new IngressRoute straight to file-service, outranking the guard.
case_e="$(mutated_render)"
cat >>"$case_e" <<EOF
---
apiVersion: traefik.io/v1alpha1
kind: IngressRoute
metadata:
  name: nchat-dev-files-direct
  namespace: nchat-dev
spec:
  entryPoints:
  - websecure
  routes:
  - kind: Rule
    match: Host(\`${nchat_dev_host}\`) && PathPrefix(\`/api/files\`)
    priority: 300
    services:
    - name: file-service
      port: 8083
EOF
expect_route_verdict 'new competing IngressRoute outranks the guard' 1 "$case_e"

# F — a file-service route that is provably disjoint from the upload paths must
# not be flagged. A gate that fails on everything protects nothing, because it
# gets switched off.
case_f="$(mutated_render)"
cat >>"$case_f" <<EOF
---
apiVersion: traefik.io/v1alpha1
kind: IngressRoute
metadata:
  name: nchat-dev-files-metrics
  namespace: nchat-dev
spec:
  entryPoints:
  - websecure
  routes:
  - kind: Rule
    match: Host(\`${nchat_dev_host}\`) && PathPrefix(\`/api/files-metrics\`)
    priority: 900
    services:
    - name: file-service
      port: 8083
EOF
expect_route_verdict 'disjoint file-service route is accepted' 0 "$case_f"

# G — a rule this gate cannot evaluate must fail closed rather than be assumed
# harmless.
case_g="$(mutated_render)"
cat >>"$case_g" <<EOF
---
apiVersion: traefik.io/v1alpha1
kind: IngressRoute
metadata:
  name: nchat-dev-files-regexp
  namespace: nchat-dev
spec:
  entryPoints:
  - websecure
  routes:
  - kind: Rule
    match: Host(\`${nchat_dev_host}\`) && PathRegexp(\`^/api/files/.*\$\`)
    priority: 900
    services:
    - name: file-service
      port: 8083
EOF
expect_route_verdict 'unclassifiable file-service route fails closed' 1 "$case_g"

if [ "$selftest_failures" -ne 0 ]; then
  echo "Route priority gate self-tests failed: $selftest_failures case(s)." >&2
  exit 1
fi

# No other routing object may claim these POSTs. The priority comparison above
# is the substantive guarantee; this stays as a second net, so a new IngressRoute
# has to be looked at deliberately rather than merely out-prioritised.
if [ "$(grep -c 'kind: IngressRoute' "$RENDERED_OVERLAY" || true)" -ne 1 ]; then
  echo "Rendered nchat-dev must declare exactly one IngressRoute (the upload route)." >&2
  exit 1
fi

rm -f "$rendered_overlay_file"

# Issue #425: validates that the local gateway, every Kubernetes overlay, the
# Go router and the frontend agree on the /api/auth contract. Runs before the
# Docker-dependent checks below, which exit early when Docker is absent.
bash "$ROOT_DIR/scripts/ci/auth-route-contract-check.sh"

# The same contract for /api/files: file-service registers /attachments/*, so
# the shared Ingress must strip the public prefix the way the dedicated upload
# route already did, or every GET under /api/files answers 404.
bash "$ROOT_DIR/scripts/ci/files-route-contract-check.sh"

bash "$ROOT_DIR/scripts/ci/admin-route-contract-check.sh"

if ! command -v docker >/dev/null 2>&1; then
  echo "Docker not found; skipping Docker-based gateway config validation."
  echo "Gateway config check passed."
  exit 0
fi

if ! docker compose version >/dev/null 2>&1; then
  echo "Docker Compose v2 not found; skipping Docker Compose gateway config validation."
  echo "Gateway config check passed."
  exit 0
fi

if [ ! -f "$ENV_FILE" ]; then
  cp "$ENV_EXAMPLE" "$ENV_FILE"
  created_env=1
fi

set -a
# shellcheck source=/dev/null
. "$ENV_EXAMPLE"
set +a

TRAEFIK_IMAGE="${TRAEFIK_IMAGE:-traefik:v3.6}"

docker compose --env-file "$ENV_EXAMPLE" -f "$COMPOSE_FILE" --profile gateway config >/dev/null

CERT_MOUNT_DIR="$LOCAL_CERT_DIR"
if [ ! -f "$LOCAL_CERT_DIR/nchat.local.pem" ] || [ ! -f "$LOCAL_CERT_DIR/nchat.local-key.pem" ]; then
  if command -v openssl >/dev/null 2>&1; then
    TEMP_CERT_DIR="$(mktemp -d)"
    OPENSSL_CONFIG="$TEMP_CERT_DIR/openssl.cnf"
    cat >"$OPENSSL_CONFIG" <<'EOF'
[req]
default_bits = 2048
prompt = no
default_md = sha256
distinguished_name = dn
x509_extensions = v3_req

[dn]
CN = nchat.local

[v3_req]
subjectAltName = @alt_names

[alt_names]
DNS.1 = nchat.local
DNS.2 = localhost
IP.1 = 127.0.0.1
EOF
    openssl req -x509 -nodes -days 1 -newkey rsa:2048 \
      -keyout "$TEMP_CERT_DIR/nchat.local-key.pem" \
      -out "$TEMP_CERT_DIR/nchat.local.pem" \
      -config "$OPENSSL_CONFIG" >/dev/null 2>&1
    CERT_MOUNT_DIR="$TEMP_CERT_DIR"
  else
    echo "warning: openssl not found and local TLS certs are absent; skipping Traefik check-config." >&2
    docker run --rm "$TRAEFIK_IMAGE" version >/dev/null
    echo "Gateway config check passed."
    exit 0
  fi
fi

if docker run --rm "$TRAEFIK_IMAGE" check-config --help >/dev/null 2>&1; then
  docker run --rm \
    -v "$TRAEFIK_STATIC_CONFIG:/etc/traefik/traefik.yml:ro" \
    -v "$TRAEFIK_DYNAMIC_CONFIG:/etc/traefik/dynamic.yml:ro" \
    -v "$CERT_MOUNT_DIR:/certs:ro" \
    "$TRAEFIK_IMAGE" \
    check-config --configFile=/etc/traefik/traefik.yml
else
  echo "warning: traefik check-config is unavailable in $TRAEFIK_IMAGE; validating image startup command only." >&2
  docker run --rm "$TRAEFIK_IMAGE" version >/dev/null
fi

echo "Gateway config check passed."
