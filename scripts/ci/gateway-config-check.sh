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
}
trap cleanup EXIT

if [ ! -f "$NCHAT_DEV_TOPOLOGY_FILE" ]; then
  cp "$NCHAT_DEV_TOPOLOGY_EXAMPLE" "$NCHAT_DEV_TOPOLOGY_FILE"
  created_topology_env=1
fi

# Issue #425: validates that the local gateway, every Kubernetes overlay, the
# Go router and the frontend agree on the /api/auth contract. Runs before the
# Docker-dependent checks below, which exit early when Docker is absent.
bash "$ROOT_DIR/scripts/ci/auth-route-contract-check.sh"

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
