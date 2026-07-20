#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
RENDERED_DIR="$(mktemp -d "${TMPDIR:-/tmp}/nchat-k8s-ci.XXXXXX")"
trap 'rm -rf "$RENDERED_DIR"' EXIT
export NCHAT_DEV_TOPOLOGY_FILE="${NCHAT_DEV_TOPOLOGY_FILE:-$ROOT_DIR/scripts/ci/testdata/nchat-dev-topology.env}"
# shellcheck source=scripts/deploy/nchat-dev/lib.sh
source "$ROOT_DIR/scripts/deploy/nchat-dev/lib.sh"
prepare_deploy_tree "$ROOT_DIR" "$RENDERED_DIR/tree"
NCHAT_DEV_APPLICATION_OVERLAY="$RENDERED_DIR/tree/infra-k8s/overlays/nchat-dev-server"

if [[ -n "${K8S_OVERLAY:-}" ]]; then
  overlays=("$K8S_OVERLAY")
else
  overlays=(
    infra/k8s/base
    infra/k8s/overlays/k3s-dev
    infra/k8s/overlays/k3s-staging
    infra/k8s/overlays/nchat-dev-server/data
    infra/k8s/overlays/nchat-dev-server/migrations
    infra/k8s/overlays/nchat-dev-server
    infra/k8s/security/sealed-secrets/controller
  )
fi

render_overlay() {
  local overlay="$1" overlay_path="$1" rendered warnings
  [[ "$overlay_path" == /* ]] || overlay_path="$ROOT_DIR/$overlay_path"
  if [[ "$overlay" == infra/k8s/overlays/nchat-dev-server ]]; then
    overlay_path="$NCHAT_DEV_APPLICATION_OVERLAY"
  fi
  [[ -f "$overlay_path/kustomization.yaml" ]]
  rendered="$RENDERED_DIR/$(printf '%s' "$overlay" | tr '/' '_').yaml"
  warnings="$rendered.warnings"
  if command -v kustomize >/dev/null 2>&1; then
    kustomize build "$overlay_path" >"$rendered" 2>"$warnings"
  elif command -v kubectl >/dev/null 2>&1; then
    KUBECONFIG=/dev/null kubectl kustomize "$overlay_path" >"$rendered" 2>"$warnings"
  else
    echo "error: kubectl or kustomize is required to render manifests" >&2
    return 1
  fi
  [[ -s "$rendered" ]]
  if [[ -s "$warnings" ]]; then
    echo "error: Kustomize emitted warnings for $overlay" >&2
    cat "$warnings" >&2
    return 1
  fi
  if command -v kubeconform >/dev/null 2>&1; then
    kubeconform -strict -ignore-missing-schemas -summary "$rendered" >&2
  fi
  printf '%s\n' "$rendered"
}

manifest_identities() {
  awk '
    function emit() { if (api && kind && name) print api "|" kind "|" namespace "|" name }
    /^---$/ { emit(); api=kind=name=namespace=""; next }
    /^apiVersion:/ { api=$2 }
    /^kind:/ { kind=$2 }
    /^  name:/ && name=="" { name=$2 }
    /^  namespace:/ && namespace=="" { namespace=$2 }
    END { emit() }
  ' "$1"
}

yaml_document() {
  local file="$1" wanted_kind="$2" wanted_name="$3"
  awk -v wanted_kind="$wanted_kind" -v wanted_name="$wanted_name" '
    function emit() {
      if (kind == wanted_kind && name == wanted_name) printf "%s", document
    }
    /^---$/ { emit(); document=""; kind=name=""; next }
    { document=document $0 ORS }
    /^kind:/ { kind=$2 }
    /^  name:/ && name=="" { name=$2 }
    END { emit() }
  ' "$file"
}

network_policy_names_by_type() {
  local file="$1" wanted_type="$2"
  awk -v wanted_type="$wanted_type" '
    function emit() {
      if (kind == "NetworkPolicy" && has_type) print name
    }
    /^---$/ { emit(); kind=name=""; has_type=0; next }
    /^kind:/ { kind=$2 }
    /^  name:/ && name=="" { name=$2 }
    $1 == "-" && $2 == wanted_type { has_type=1 }
    END { emit() }
  ' "$file" | LC_ALL=C sort
}

validate_no_duplicate_resources() {
  local duplicates file
  duplicates="$(
    for file in "$@"; do
      manifest_identities "$file"
    done | LC_ALL=C sort | uniq -d
  )"
  if [[ -n "$duplicates" ]]; then
    echo "error: duplicate rendered resources:" >&2
    printf '%s\n' "$duplicates" >&2
    return 1
  fi
}

validate_workload_hardening() {
  awk '
    function reset() { kind=name=""; auto=nonroot=seccomp=noescalation=readonly=dropall=requests=limits=0 }
    function check() {
      if (kind ~ /^(Deployment|StatefulSet|Job)$/) {
        if (!(auto && nonroot && seccomp && noescalation && readonly && dropall && requests && limits)) {
          print "error: incomplete workload hardening: " kind "/" name > "/dev/stderr"; failed=1
        }
      }
      reset()
    }
    BEGIN { reset() }
    /^---$/ { check(); next }
    /^kind:/ { kind=$2 }
    /^  name:/ && name=="" { name=$2 }
    /automountServiceAccountToken: false/ { auto=1 }
    /runAsNonRoot: true/ { nonroot=1 }
    /type: RuntimeDefault/ { seccomp=1 }
    /allowPrivilegeEscalation: false/ { noescalation=1 }
    /readOnlyRootFilesystem: true/ { readonly=1 }
    /- ALL/ { dropall=1 }
    /requests:/ { requests=1 }
    /limits:/ { limits=1 }
    END { check(); exit failed }
  ' "$1"
}

validate_coturn_template() {
  local rendered="$RENDERED_DIR/turnserver.conf" livekit="$RENDERED_DIR/livekit.yaml" directive
  local denied_peer_ips=(
    'denied-peer-ip=0.0.0.0-0.255.255.255'
    'denied-peer-ip=10.0.0.0-10.255.255.255'
    'denied-peer-ip=100.64.0.0-100.127.255.255'
    'denied-peer-ip=127.0.0.0-127.255.255.255'
    'denied-peer-ip=169.254.0.0-169.254.255.255'
    'denied-peer-ip=172.16.0.0-172.31.255.255'
    'denied-peer-ip=192.0.0.0-192.0.0.255'
    'denied-peer-ip=192.168.0.0-192.168.255.255'
    'denied-peer-ip=198.18.0.0-198.19.255.255'
    'denied-peer-ip=224.0.0.0-255.255.255.255'
  )
  "$ROOT_DIR/scripts/deploy/nchat-dev/render-topology-templates.sh" "$RENDERED_DIR"
  for directive in "${denied_peer_ips[@]}" \
    "allowed-peer-ip=$NCHAT_DEV_NODE_IP" \
    "listening-port=$TURN_LISTEN_PORT" \
    "min-port=$TURN_RELAY_MIN_PORT" \
    "max-port=$TURN_RELAY_MAX_PORT" \
    'use-auth-secret' 'no-multicast-peers' 'no-cli' 'fingerprint'; do
    [[ "$(grep -Fxc -- "$directive" "$rendered" || true)" -eq 1 ]] || {
      echo "error: expected exactly one coturn directive: $directive" >&2
      return 1
    }
  done
  [[ "$(grep -c '^allowed-peer-ip=' "$rendered" || true)" -eq 1 ]]
  if grep -Eq '^[[:space:]]*allow-loopback-peers([[:space:]]|$)' "$rendered"; then return 1; fi
  if grep -Eq '^allowed-peer-ip=.*-' "$rendered"; then return 1; fi
  if grep -Eq 'REPLACE_ME_(NODE_IP|HOST|LIVEKIT_|TURN_)' "$rendered" "$livekit"; then return 1; fi
  grep -Fxq "port: $LIVEKIT_API_PORT" "$livekit"
  grep -Fxq "  tcp_port: $LIVEKIT_RTC_TCP_PORT" "$livekit"
  grep -Fxq "  udp_port: $LIVEKIT_RTC_UDP_PORT" "$livekit"
}

validate_nchat_dev() {
  local application="$1" data="$2" migrations="$3" policy_block component image_ref
  local -a external_image_refs=()
  validate_no_duplicate_resources "$application" "$data" "$migrations"
  validate_workload_hardening "$application"
  validate_workload_hardening "$data"
  validate_workload_hardening "$migrations"
  if grep -q '^kind: Secret$' "$application" "$data" "$migrations"; then return 1; fi
  if grep -q 'secretRef:' "$application" "$data" "$migrations"; then return 1; fi
  if grep -q 'REPLACE_ME_' "$application" "$data" "$migrations"; then return 1; fi
  if grep -Eq '0\.0\.0\.0/0|port: 3478|containerPort: 3478' "$application" "$data" "$migrations"; then return 1; fi
  if grep -R -Eq '/containers/0|/env/-' "$ROOT_DIR/infra/k8s/overlays/nchat-dev-server"; then return 1; fi

  policy_block="$(yaml_document "$application" NetworkPolicy nchat-allow-livekit-api-egress)"
  grep -Fq "cidr: $NCHAT_DEV_NODE_CIDR" <<<"$policy_block"
  grep -Fq "port: $LIVEKIT_API_PORT" <<<"$policy_block"
  grep -Fq 'protocol: TCP' <<<"$policy_block"
  grep -Fq 'app.kubernetes.io/component: media' <<<"$policy_block"
  [[ "$(grep -R -l 'name: LIVEKIT_URL' "$ROOT_DIR/infra/k8s/overlays/nchat-dev-server/patches" | wc -l)" -eq 1 ]]
  grep -q 'name: LIVEKIT_URL' "$ROOT_DIR/infra/k8s/overlays/nchat-dev-server/patches/media-service.yaml"

  [[ "$(grep -Fxc '  activeDeadlineSeconds: 300' "$migrations")" -eq 1 ]]
  [[ "$(grep -Fxc '  backoffLimit: 0' "$migrations")" -eq 1 ]]
  grep -q 'name: MIGRATIONS_DATABASE_URL' "$migrations"
  if grep -q 'name: DATABASE_URL' "$migrations"; then return 1; fi
  grep -q 'name: nchat-default-deny-egress' "$application"
  grep -q 'name: nchat-default-deny-ingress' "$application"

  [[ "$(network_policy_names_by_type "$application" Egress)" == "$(printf '%s\n' \
    nchat-allow-auth-postgres-egress \
    nchat-allow-chat-data-egress \
    nchat-allow-dns-egress \
    nchat-allow-livekit-api-egress \
    nchat-allow-migrations-postgres-egress \
    nchat-allow-notification-postgres-egress \
    nchat-default-deny-egress | LC_ALL=C sort)" ]]
  for policy_block in \
    nchat-allow-dns-egress nchat-allow-traefik-http nchat-allow-postgres \
    nchat-allow-valkey nchat-allow-auth-postgres-egress nchat-allow-chat-data-egress \
    nchat-allow-notification-postgres-egress nchat-allow-migrations-postgres-egress \
    nchat-allow-livekit-api-egress; do
    grep -q 'ports:' <<<"$(yaml_document "$application" NetworkPolicy "$policy_block")"
  done
  grep -Fq 'port: http' <<<"$(yaml_document "$application" NetworkPolicy nchat-allow-traefik-http)"
  if grep -q 'namespaceSelector: {}' "$application"; then return 1; fi
  if grep -Eq 'port: (8333|9333)' "$application"; then return 1; fi
  if grep -Eq 'name: s3|port: 8333|containerPort: 8333|[[:space:]]- -s3$' "$data"; then return 1; fi
  if grep -Eq 'SEAWEEDFS_(FILER_URL|S3_ENDPOINT)' "$application"; then return 1; fi

  mapfile -t external_image_refs < <(
    grep -hE '^        image: (postgres|valkey/valkey|chrislusf/seaweedfs|livekit/livekit-server|coturn/coturn):' \
      "$application" "$data"
  )
  for image_ref in "${external_image_refs[@]}"; do
    image_ref="${image_ref#*image: }"
    [[ "$image_ref" =~ @sha256:[a-f0-9]{64}$ ]]
  done
  [[ "${#external_image_refs[@]}" -eq 6 ]]

  for component in auth chat file notification admin search media web; do
    grep -q "app.kubernetes.io/component: $component" "$application"
  done
}

load_nchat_dev_topology "$NCHAT_DEV_TOPOLOGY_FILE"
sh -n "$ROOT_DIR/infra/k8s/overlays/nchat-dev-server/data/postgres-bootstrap.sh"
if grep -q '|' "$ROOT_DIR/infra/k8s/overlays/nchat-dev-server/data/postgres-bootstrap.sh"; then
  echo "error: POSIX PostgreSQL bootstrap must not contain pipelines" >&2
  exit 1
fi
validate_coturn_template

declare -A rendered_by_overlay=()
for overlay in "${overlays[@]}"; do
  rendered_by_overlay["$overlay"]="$(render_overlay "$overlay")"
done

if [[ -z "${K8S_OVERLAY:-}" ]]; then
  validate_nchat_dev \
    "${rendered_by_overlay[infra/k8s/overlays/nchat-dev-server]}" \
    "${rendered_by_overlay[infra/k8s/overlays/nchat-dev-server/data]}" \
    "${rendered_by_overlay[infra/k8s/overlays/nchat-dev-server/migrations]}"
fi

echo "K8s manifests CI check passed."
