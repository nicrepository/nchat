#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
TOPOLOGY_FIXTURE="$ROOT_DIR/scripts/ci/testdata/nchat-dev-topology.env"
export NCHAT_DEV_TOPOLOGY_FILE="${NCHAT_DEV_TOPOLOGY_FILE:-$TOPOLOGY_FIXTURE}"
# shellcheck source=scripts/deploy/nchat-dev/lib.sh
source "$ROOT_DIR/scripts/deploy/nchat-dev/lib.sh"
# shellcheck source=scripts/deploy/nchat-dev/kustomize.env
source "$ROOT_DIR/scripts/deploy/nchat-dev/kustomize.env"

TEMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/nchat-deploy-check.XXXXXX")"
trap 'cleanup_deploy_tree "$TEMP_DIR"' EXIT
ARTIFACTS="$TEMP_DIR/artifacts"
DIGEST='sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'

fail() {
  echo "nchat-dev deployment check failed: $*" >&2
  exit 1
}

require_pinned_kustomize() {
  local actual
  command -v kustomize >/dev/null 2>&1 || return 1
  actual="$(kustomize version 2>/dev/null)" || return 1
  [[ "$actual" == "$KUSTOMIZE_VERSION" ]]
}

make_valid_artifacts() {
  local image
  cleanup_deploy_tree "$ARTIFACTS"
  mkdir -p "$ARTIFACTS"
  for image in "${NCHAT_DEV_IMAGES[@]}"; do
    printf '%s' "$DIGEST" >"$ARTIFACTS/digest-$image.txt"
  done
}

expect_invalid_artifacts() {
  if validate_digest_artifacts "$ARTIFACTS"; then
    fail "$1"
  fi
}

make_topology_variant() {
  local destination="$1" changed_key="$2" changed_value="$3" key value
  while IFS='=' read -r key value || [[ -n "$key$value" ]]; do
    if [[ "$key" == "$changed_key" ]]; then
      value="$changed_value"
    fi
    printf '%s=%s\n' "$key" "$value"
  done <"$TOPOLOGY_FIXTURE" >"$destination"
}

expect_invalid_topology() {
  if (load_nchat_dev_topology "$1") >/dev/null 2>&1; then
    fail "$2"
  fi
}

validate_topology_contract() {
  local variant="$TEMP_DIR/topology-variant.env" materialized="$TEMP_DIR/materialized.env"
  load_nchat_dev_topology "$TOPOLOGY_FIXTURE" || fail "valid topology fixture rejected"

  make_topology_variant "$variant" NCHAT_DEV_NODE_IP 999.0.2.10
  expect_invalid_topology "$variant" "invalid IPv4 accepted"
  make_topology_variant "$variant" NCHAT_DEV_NODE_CIDR 192.0.2.0/24
  expect_invalid_topology "$variant" "non-/32 node CIDR accepted"
  make_topology_variant "$variant" TURN_LISTEN_PORT 3478
  expect_invalid_topology "$variant" "reserved port 3478 accepted"
  make_topology_variant "$variant" TURN_RELAY_MIN_PORT 50000
  expect_invalid_topology "$variant" "inverted relay range accepted"
  make_topology_variant "$variant" NCHAT_DEV_HOST $'invalid\nhost'
  expect_invalid_topology "$variant" "newline in topology value accepted"
  make_topology_variant "$variant" NCHAT_DEV_HOST 'Nchat-Dev.example.invalid'
  expect_invalid_topology "$variant" "uppercase host accepted"
  make_topology_variant "$variant" NCHAT_DEV_HOST 'nchat_dev.example.invalid'
  expect_invalid_topology "$variant" "underscore in host accepted"
  make_topology_variant "$variant" NCHAT_DEV_HOST 'nchat dev.example.invalid'
  expect_invalid_topology "$variant" "space in host accepted"
  make_topology_variant "$variant" NCHAT_DEV_HOST '-nchat-dev.example.invalid'
  expect_invalid_topology "$variant" "leading hyphen in host label accepted"
  make_topology_variant "$variant" NCHAT_DEV_HOST 'nchat-dev-.example.invalid'
  expect_invalid_topology "$variant" "trailing hyphen in host label accepted"
  make_topology_variant "$variant" NCHAT_DEV_HOST '.nchat-dev.example.invalid'
  expect_invalid_topology "$variant" "leading dot in host accepted"
  make_topology_variant "$variant" NCHAT_DEV_HOST 'nchat-dev.example.invalid.'
  expect_invalid_topology "$variant" "trailing dot in host accepted"
  make_topology_variant "$variant" NCHAT_DEV_HOST 'nchat-dev..example.invalid'
  expect_invalid_topology "$variant" "empty host label accepted"
  make_topology_variant "$variant" NCHAT_DEV_HOST REPLACE_ME_HOST
  expect_invalid_topology "$variant" "unresolved REPLACE_ME_HOST accepted as topology host"
  cp "$TOPOLOGY_FIXTURE" "$variant"
  printf '%s\n' 'UNEXPECTED_KEY=value' >>"$variant"
  expect_invalid_topology "$variant" "unexpected topology key accepted"

  (
    unset NCHAT_DEV_TOPOLOGY_FILE
    export NCHAT_DEV_NODE_IP=192.0.2.20
    export NCHAT_DEV_NODE_CIDR=192.0.2.20/32
    export NCHAT_DEV_HOST=nchat-dev-ci.example.invalid
    materialize_nchat_dev_topology \
      "$ROOT_DIR/infra/k8s/overlays/nchat-dev-server/topology.env.example" "$materialized"
  ) || fail "environment topology materialization failed"
  [[ "$(stat -c '%a' "$materialized")" == 600 ]] || fail "materialized topology permissions are not 0600"
  load_nchat_dev_topology "$materialized" || fail "materialized topology is invalid"

  local empty_host_dest="$TEMP_DIR/empty-host.env"
  if (
    unset NCHAT_DEV_TOPOLOGY_FILE
    export NCHAT_DEV_NODE_IP=192.0.2.21
    export NCHAT_DEV_NODE_CIDR=192.0.2.21/32
    export NCHAT_DEV_HOST=
    materialize_nchat_dev_topology \
      "$ROOT_DIR/infra/k8s/overlays/nchat-dev-server/topology.env.example" "$empty_host_dest"
  ) >/dev/null 2>&1; then
    fail "empty NCHAT_DEV_HOST was accepted"
  fi
  [[ ! -e "$empty_host_dest" ]] || fail "empty NCHAT_DEV_HOST produced a rendered topology file"
  if (
    unset NCHAT_DEV_TOPOLOGY_FILE NCHAT_DEV_NODE_IP NCHAT_DEV_NODE_CIDR NCHAT_DEV_HOST
    materialize_nchat_dev_topology \
      "$ROOT_DIR/infra/k8s/overlays/nchat-dev-server/topology.env.example" "$TEMP_DIR/missing.env"
  ) >/dev/null 2>&1; then
    fail "missing operational topology was accepted"
  fi

  git -C "$ROOT_DIR" check-ignore -q infra/k8s/overlays/nchat-dev-server/topology.env || fail "local topology is not ignored"
  ! git -C "$ROOT_DIR" ls-files --error-unmatch infra/k8s/overlays/nchat-dev-server/topology.env >/dev/null 2>&1 || fail "real topology is tracked"
  [[ -f "$ROOT_DIR/infra/k8s/overlays/nchat-dev-server/topology.env.example" ]] || fail "topology example is missing"
  ! git -C "$ROOT_DIR" check-ignore -q infra/k8s/overlays/nchat-dev-server/topology.env.example || fail "topology example is ignored"

  local source_link="$TEMP_DIR/topology-source-link" local_root="$TEMP_DIR/topology-root" destination="$TEMP_DIR/topology-destination.env"
  ln -s "$TOPOLOGY_FIXTURE" "$source_link"
  if NCHAT_DEV_TOPOLOGY_FILE="$source_link" prepare_nchat_dev_topology "$ROOT_DIR" "$destination"; then fail "symlink topology source accepted"; fi
  [[ ! -e "$destination" ]] || fail "symlink topology source reached install"
  mkdir -p "$local_root/infra/k8s/overlays/nchat-dev-server"
  ln -s "$TOPOLOGY_FIXTURE" "$local_root/infra/k8s/overlays/nchat-dev-server/topology.env"
  if (unset NCHAT_DEV_TOPOLOGY_FILE; prepare_nchat_dev_topology "$local_root" "$destination"); then fail "symlink local topology accepted in conditional"; fi
  [[ ! -e "$destination" ]] || fail "failed topology validation reached install"
  make_topology_variant "$variant" NCHAT_DEV_HOST invalid_host
  if (NCHAT_DEV_TOPOLOGY_FILE="$variant" prepare_nchat_dev_topology "$ROOT_DIR" "$destination"); then fail "invalid topology accepted in subshell conditional"; fi
  [[ ! -e "$destination" ]] || fail "invalid topology created destination"
}

validate_kustomize_pin_contract() {
  local bin_dir="$TEMP_DIR/kustomize-pin" empty_dir="$TEMP_DIR/no-kustomize"
  mkdir -p "$bin_dir" "$empty_dir"
  if (PATH="$empty_dir" require_pinned_kustomize); then fail "missing Kustomize accepted"; fi
  printf '%s\n' '#!/usr/bin/env bash' 'printf "%s\\n" v0.0.0' >"$bin_dir/kustomize"; chmod +x "$bin_dir/kustomize"
  if (PATH="$bin_dir:$PATH" require_pinned_kustomize); then fail "wrong Kustomize version accepted"; fi
  printf '%s\n' '#!/usr/bin/env bash' "printf '%s\\n' '$KUSTOMIZE_VERSION'" >"$bin_dir/kustomize"
  (PATH="$bin_dir:$PATH" require_pinned_kustomize) || fail "pinned Kustomize version rejected"
}

yaml_document() {
  local file="$1" wanted_kind="$2" wanted_name="$3"
  awk -v wanted_kind="$wanted_kind" -v wanted_name="$wanted_name" '
    function emit() { if (kind == wanted_kind && name == wanted_name) printf "%s", document }
    /^---$/ { emit(); document=""; kind=name=""; next }
    { document=document $0 ORS }
    /^kind:/ { kind=$2 }
    /^  name:/ && name=="" { name=$2 }
    END { emit() }
  ' "$file"
}

has_exact_ingress_first_rule_host() {
  local document="$1" host="$2"
  awk -v host="$host" '
    function indent(line) { match(line, /[^ ]/); return RSTART ? RSTART - 1 : length(line) }
    function is_key(line, key) { return line ~ ("^[ ]*" key ":[ ]*$") }
    /^[[:space:]]*#/ || /^[[:space:]]*$/ { next }
    {
      level = indent($0)
      if (is_key($0, "spec") && level == 0) {
        spec_count++; in_spec=1; spec_level=level; child_level=-1; in_rules=0
        next
      }
      if (in_spec && level <= spec_level) in_spec=0
      if (!in_spec) next
      if (level > spec_level && child_level < 0) child_level=level
      if (level == child_level && is_key($0, "rules")) {
        rules_count++; in_rules=1; rules_level=level; awaiting_first_item=1; first_item_level=-1; first_child_level=-1
        next
      }
      if (!in_rules) next
      if (awaiting_first_item) {
        if ($0 !~ /^[ ]*-[ ]+host:[ ]+/) { invalid=1; in_rules=0; next }
        value=$0; sub(/^[ ]*-[ ]+host:[ ]+/, "", value)
        if (value != host) { invalid=1; in_rules=0; next }
        awaiting_first_item=0; first_item_level=level; first_host_seen=1
        next
      }
      if (level == first_item_level && $0 ~ /^[ ]*-[ ]/) { in_rules=0; next }
      if (level <= rules_level) { in_rules=0; next }
      if (level > first_item_level) {
        if (first_child_level < 0) first_child_level=level
        if (level == first_child_level && $0 ~ /^[ ]*host:[ ]+/) invalid=1
      }
    }
    END { exit !(spec_count == 1 && rules_count == 1 && first_host_seen && !invalid) }
  ' <<<"$document"
}

validate_ingress_host_assertion_contract() {
  local fixture="$TEMP_DIR/ingress-host-assertion.yaml" document host='nchat-dev.example.invalid'
  cat >"$fixture" <<EOF
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: nchat-dev
spec:
  rules:
  - host: $host
---
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: another-ingress
spec:
  rules:
    - host: wrong.example.invalid
EOF
  document="$(yaml_document "$fixture" Ingress nchat-dev)"
  has_exact_ingress_first_rule_host "$document" "$host" || fail "two-space Ingress host was rejected"
  document="${document/  - host:/    - host:}"
  has_exact_ingress_first_rule_host "$document" "$host" || fail "four-space Ingress host was rejected"
  has_exact_ingress_first_rule_host $'spec:\n  rules:\n  - host: nchat-dev.example.invalid\n    http:\n      paths: []' "$host" || fail "first rule with fields was rejected"
  for document in \
    $'spec:\n  rules:\n    nested:\n      - host: nchat-dev.example.invalid' \
    $'spec:\n  rules:\n  - http:\n      host: nchat-dev.example.invalid\n      paths: []' \
    $'spec:\n  rules:\n  - hostname: nchat-dev.example.invalid' \
    $'spec:\n  rules:\n  - http:\n      paths: []' \
    $'spec:\n  rules:\n  - host: nchat-dev.example.invalid\n    host: nchat-dev.example.invalid' \
    $'spec:\n  rules:\n  - host: wrong.example.invalid\n  - host: nchat-dev.example.invalid' \
    $'spec:\n  rules:\n  - http:\n      paths: []\n  unrelated:\n  - host: nchat-dev.example.invalid' \
    $'spec:\n  rules:\n  - http:\n      paths: []\n  tls:\n  - hosts:\n    - nchat-dev.example.invalid' \
    $'spec:\n  rules:\n  - hosts: nchat-dev.example.invalid' \
    $'spec:\n  rules:\n  # - host: nchat-dev.example.invalid\n  - http:\n      paths: []' \
    $'spec:\n  rules:\n  - host: nchat-dev.example.invalid:443' \
    $'spec:\n  rules:\n  - host: nchat-dev.example.invalid.attacker.example' \
    $'spec:\n  rules:\n  - host: nchat-dev.example.invalid # comment' \
    $'spec:\n  rules:\n  - host: nchat-dev.example.invalid\n  rules:\n  - host: nchat-dev.example.invalid'; do
    ! has_exact_ingress_first_rule_host "$document" "$host" || fail "invalid spec.rules[0].host was accepted"
  done
  cat >"$fixture" <<EOF
kind: Ingress
metadata:
  name: nchat-dev
spec:
  rules:
  - http: {}
---
kind: Ingress
metadata:
  name: other-ingress
spec:
  rules:
  - host: $host
EOF
  document="$(yaml_document "$fixture" Ingress nchat-dev)"
  ! has_exact_ingress_first_rule_host "$document" "$host" || fail "host in another Ingress was accepted"
}

assert_rendered_replacements() {
  local rendered="$1" host="$2" document
  document="$(yaml_document "$rendered" Certificate nchat-dev-tls)"; grep -Fqx "    - $host" <<<"$document" && ! grep -q REPLACE_ME_HOST <<<"$document" || fail "Certificate/nchat-dev-tls hostname replacement failed"
  document="$(yaml_document "$rendered" Ingress nchat-dev)"; [[ "$(grep -Fc "$host" <<<"$document")" -eq 2 ]] && has_exact_ingress_first_rule_host "$document" "$host" && grep -Fqx "        - $host" <<<"$document" || fail "Ingress/nchat-dev hostname targets failed"
  document="$(yaml_document "$rendered" Ingress nchat-dev-http)"; [[ "$(grep -Fc "$host" <<<"$document")" -eq 1 ]] && grep -Fqx "    - host: $host" <<<"$document" || fail "Ingress/nchat-dev-http hostname target failed"
  document="$(yaml_document "$rendered" Ingress nchat-dev-livekit)"; [[ "$(grep -Fc "$host" <<<"$document")" -eq 2 ]] && grep -Fqx "    - host: $host" <<<"$document" && grep -Fqx "        - $host" <<<"$document" || fail "Ingress/nchat-dev-livekit hostname targets failed"
  document="$(yaml_document "$rendered" IngressRoute nchat-dev-uploads)"; grep -Fqx "      match: Host(\`$host\`) && Method(\`POST\`) && PathRegexp(\`^/api/files/(channels|dm)/[^/]+/attachments$\`)" <<<"$document" && [[ "$(grep -Fc "$host" <<<"$document")" -eq 1 ]] || fail "IngressRoute/nchat-dev-uploads match replacement failed"
  document="$(yaml_document "$rendered" ConfigMap nchat-config)"; grep -Fqx "  AUTH_PUBLIC_WEB_BASE_URL: https://$host" <<<"$document" || fail "ConfigMap/nchat-config public URL replacement failed"
  ! grep -q REPLACE_ME_HOST "$rendered" || fail "rendered manifest still contains REPLACE_ME_HOST"
}

# Picks the first installed UTF-8 locale from a preference list, without
# installing anything. Returns empty (and prints nothing) if none is found.
find_utf8_locale() {
  local candidate available
  available="$(locale -a 2>/dev/null)"
  for candidate in pt_BR.UTF-8 pt_BR.utf8 en_US.UTF-8 en_US.utf8 C.UTF-8 C.utf8; do
    if grep -Fxq "$candidate" <<<"$available"; then
      printf '%s\n' "$candidate"
      return 0
    fi
  done
  return 1
}

# is_rfc1123_hostname must reject every non-ASCII, shell-metacharacter and
# out-of-bounds hostname regardless of the caller's locale (the bug this
# guards against: under some UTF-8 locales, libc's regex engine treated
# [a-z0-9] ranges as locale-collation ranges and let accented/multibyte
# characters through). Run the full case list once under a given locale.
run_hostname_case_list() {
  # NOTE: intentionally does not declare its own "local label" — it relies on
  # the caller's $label (inherited across subshells) purely for diagnostics.
  is_rfc1123_hostname 'nchat-dev.example.invalid' ||
    fail "[$label] valid ASCII host rejected"

  # Unicode: accented, leading-in-label, mid-label, non-Latin.
  ! is_rfc1123_hostname 'café.example.invalid' || fail "[$label] accented Unicode host accepted"
  ! is_rfc1123_hostname 'ãexample.invalid' || fail "[$label] Unicode at start of label accepted"
  ! is_rfc1123_hostname 'example-ç.invalid' || fail "[$label] Unicode in middle of label accepted"
  ! is_rfc1123_hostname 'пример.example.invalid' || fail "[$label] non-Latin Unicode host accepted"

  # Shell metacharacters: must never be executed nor accepted as a hostname.
  ! is_rfc1123_hostname 'example`cmd`.invalid' || fail "[$label] backtick host accepted"
  ! is_rfc1123_hostname 'example$(id).invalid' || fail "[$label] command substitution host accepted"
  ! is_rfc1123_hostname 'example${PATH}.invalid' || fail "[$label] parameter expansion host accepted"

  # Other rejected characters/shapes.
  ! is_rfc1123_hostname 'nchat dev.example.invalid' || fail "[$label] space in host accepted"
  ! is_rfc1123_hostname $'nchat-dev.example\tinvalid' || fail "[$label] tab in host accepted"
  ! is_rfc1123_hostname $'nchat-dev.example\ninvalid' || fail "[$label] newline in host accepted"
  ! is_rfc1123_hostname 'nchat-dev.example.invalid/path' || fail "[$label] slash in host accepted"
  ! is_rfc1123_hostname 'nchat-dev.example.invalid:8443' || fail "[$label] colon/port in host accepted"
  ! is_rfc1123_hostname 'https://nchat-dev.example.invalid' || fail "[$label] URL scheme in host accepted"
  ! is_rfc1123_hostname '*.example.invalid' || fail "[$label] wildcard host accepted"
  ! is_rfc1123_hostname 'nchat_dev.example.invalid' || fail "[$label] underscore in host accepted"
  ! is_rfc1123_hostname 'Nchat-Dev.example.invalid' || fail "[$label] uppercase host accepted"
  ! is_rfc1123_hostname '.nchat-dev.example.invalid' || fail "[$label] leading dot accepted"
  ! is_rfc1123_hostname 'nchat-dev.example.invalid.' || fail "[$label] trailing dot accepted"
  ! is_rfc1123_hostname 'nchat-dev..example.invalid' || fail "[$label] empty label accepted"
  ! is_rfc1123_hostname '-nchat-dev.example.invalid' || fail "[$label] leading hyphen accepted"
  ! is_rfc1123_hostname 'nchat-dev-.example.invalid' || fail "[$label] trailing hyphen accepted"

  # Length boundaries: 63/64-char labels, 253/254-char total hostnames.
  local label63 label64 host253 host254
  label63="$(printf 'a%.0s' $(seq 1 63))"
  label64="$(printf 'a%.0s' $(seq 1 64))"
  is_rfc1123_hostname "$label63" || fail "[$label] exactly-63-char label rejected"
  ! is_rfc1123_hostname "$label64" || fail "[$label] 64-char label accepted"
  ! is_rfc1123_hostname "$label64.invalid" || fail "[$label] host with a 64-char label accepted"

  # 3 labels of 63 'a' + 1 label of 61 'a', joined by 3 dots: 63*3+61+3 = 253.
  host253="$(printf 'a%.0s' $(seq 1 63)).$(printf 'a%.0s' $(seq 1 63)).$(printf 'a%.0s' $(seq 1 63)).$(printf 'a%.0s' $(seq 1 61))"
  [[ "${#host253}" -eq 253 ]] || fail "[$label] 253-char host fixture is miscomputed"
  is_rfc1123_hostname "$host253" || fail "[$label] exactly-253-char host rejected"

  # Same shape with one more character in the last label: 254 total.
  host254="$(printf 'a%.0s' $(seq 1 63)).$(printf 'a%.0s' $(seq 1 63)).$(printf 'a%.0s' $(seq 1 63)).$(printf 'a%.0s' $(seq 1 62))"
  [[ "${#host254}" -eq 254 ]] || fail "[$label] 254-char host fixture is miscomputed"
  ! is_rfc1123_hostname "$host254" || fail "[$label] 254-char host accepted"
}

validate_hostname_unicode_locale_and_boundaries() {
  local label utf8_locale

  label="default ($LANG${LC_ALL:+/$LC_ALL})"
  run_hostname_case_list

  label='LC_ALL=C'
  (LC_ALL=C; run_hostname_case_list) || fail "hostname case list failed under LC_ALL=C"

  if utf8_locale="$(find_utf8_locale)"; then
    label="LC_ALL=$utf8_locale"
    (LC_ALL="$utf8_locale"; run_hostname_case_list) || fail "hostname case list failed under LC_ALL=$utf8_locale"
    echo "info: hostname validation confirmed identical under LC_ALL=C and LC_ALL=$utf8_locale" >&2
  else
    echo "warning: no UTF-8 locale available (checked locale -a); skipped the cross-locale comparison, Unicode cases were still exercised under the current locale" >&2
  fi
}

validate_image_inventory() {
  local service image deployment invalid_inventory="$TEMP_DIR/invalid-images.txt"
  local -a discovered=()
  for service in "$ROOT_DIR"/services/*; do
    [[ -d "$service/cmd/$(basename "$service")" ]] || continue
    discovered+=("$(basename "$service")")
  done
  [[ "$(printf '%s\n' "${discovered[@]}" | LC_ALL=C sort)" == \
      "$(printf '%s\n' "${NCHAT_DEV_GO_SERVICES[@]}" | LC_ALL=C sort)" ]] || fail "Go service inventory drift"
  grep -Fq 'fromJSON(needs.inventory.outputs.images)' "$ROOT_DIR/.github/workflows/images.yml" || fail "image workflow does not derive its matrix"
  ! grep -q 'workflow_dispatch:' "$ROOT_DIR/.github/workflows/images.yml" || fail "unprotected manual image publishing is enabled"
  ! grep -Eq 'for image in (web|auth-service)' "$ROOT_DIR/scripts/deploy/nchat-dev/deploy.sh" || fail "deploy duplicates the image inventory"
  ! grep -Eq 'kubectl[[:space:]]+apply[[:space:]]+-k' "$ROOT_DIR/scripts/deploy/nchat-dev/deploy.sh" || fail "deploy uses kubectl embedded Kustomize"
  grep -Fq 'actual_kustomize="$(kustomize version)"' "$ROOT_DIR/scripts/deploy/nchat-dev/deploy.sh" || fail "deploy does not validate standalone Kustomize"
  grep -Fq 'validate_rendered_overlay "$DATA_OVERLAY" "$TEMPORARY_ROOT/data.yaml"' "$ROOT_DIR/scripts/deploy/nchat-dev/deploy.sh" || fail "data overlay is not rendered with standalone Kustomize"

  for image in "${NCHAT_DEV_RUNTIME_IMAGES[@]}"; do
    [[ "$(grep -Fxc "  - name: ghcr.io/nicrepository/nchat/$image" "$ROOT_DIR/infra/k8s/overlays/nchat-dev-server/kustomization.yaml")" -eq 1 ]] || fail "Kustomize image missing or duplicated: $image"
  done
  [[ "$(grep -Fxc '  - name: ghcr.io/nicrepository/nchat/migrations' "$ROOT_DIR/infra/k8s/overlays/nchat-dev-server/migrations/kustomization.yaml")" -eq 1 ]] || fail "migration Kustomize image drift"

  for deployment in "${NCHAT_DEV_APPLICATION_DEPLOYMENTS[@]}"; do
    if [[ "$deployment" == nchat-web ]]; then
      service="$ROOT_DIR/infra/k8s/base/web/service.yaml"
    else
      service="$ROOT_DIR/infra/k8s/base/services/$deployment/service.yaml"
    fi
    [[ -f "$service" ]] || fail "Service manifest missing for $deployment"
    [[ "$(grep -Ec '^[[:space:]]*- name: http$' "$service" || true)" -eq 1 ]] || fail "Service $deployment must expose exactly one named http port"
  done
  for service in "${NCHAT_DEV_GO_SERVICES[@]}"; do
    grep -Fq "COPY services/$service/go.mod services/$service/go.sum ./services/$service/" "$ROOT_DIR/Dockerfile.service" || fail "Docker metadata layer missing $service"
  done
  grep -Fq 'COPY libs/go ./libs/go' "$ROOT_DIR/Dockerfile.service" || fail "Docker build does not include all shared Go modules"
  ! grep -Eq '^COPY services([[:space:]]|/)[[:space:]]+\.?/services' "$ROOT_DIR/Dockerfile.service" || fail "Docker build copies every service source"
  ! grep -Eq ':808[0-9]' "$ROOT_DIR/scripts/deploy/nchat-dev/deploy.sh" || fail "smoke tests duplicate HTTP port numbers"
  grep -Fq "services/http:\$service:http/proxy/healthz" "$ROOT_DIR/scripts/deploy/nchat-dev/deploy.sh" || fail "smoke tests do not use the named http port"

  cp "$NCHAT_DEV_IMAGE_INVENTORY" "$invalid_inventory"
  printf '%s\n' 'go auth-service auth-service' >>"$invalid_inventory"
  if (load_nchat_dev_image_inventory "$invalid_inventory") >/dev/null 2>&1; then
    fail "duplicate image inventory entry was accepted"
  fi
}

# Fake `kubectl` that only understands `auth can-i` (answering "no" for the
# single verb/resource pair given in $1, "yes" otherwise) and fails loudly if
# any other subcommand — in particular `apply` — is ever invoked, so an RBAC
# preflight test can prove no apply happened.
setup_fake_kubectl() {
  local deny="$1" bin_dir="$TEMP_DIR/fake-kubectl-$RANDOM"
  mkdir -p "$bin_dir"
  cat >"$bin_dir/kubectl" <<EOF
#!/usr/bin/env bash
if [[ "\$1 \$2" == "auth can-i" ]]; then
  if [[ "\$3 \$4" == "$deny" ]]; then
    echo no
  else
    echo yes
  fi
  exit 0
fi
echo "fake kubectl: unexpected invocation: \$*" >&2
exit 1
EOF
  chmod +x "$bin_dir/kubectl"
  printf '%s\n' "$bin_dir"
}

validate_rbac_preflight() {
  local bin_dir err_log="$TEMP_DIR/rbac-preflight.err" verb

  grep -Fq 'require_kubernetes_permission "$verb" ingressroutes.traefik.io nchat-dev' "$ROOT_DIR/scripts/deploy/nchat-dev/deploy.sh" || fail "deploy does not preflight ingressroutes RBAC"
  grep -Fq 'require_kubernetes_permission "$verb" ingresses.networking.k8s.io nchat-dev' "$ROOT_DIR/scripts/deploy/nchat-dev/deploy.sh" || fail "deploy does not preflight ingresses RBAC"
  ! grep -Fq -- '--as' "$ROOT_DIR/scripts/deploy/nchat-dev/deploy.sh" || fail "deploy impersonates a different identity for RBAC checks"

  bin_dir="$(setup_fake_kubectl 'none none')"
  for verb in get create patch update; do
    (PATH="$bin_dir:$PATH" require_kubernetes_permission "$verb" ingressroutes.traefik.io nchat-dev) || fail "RBAC preflight rejected a fully permitted identity ($verb)"
  done

  bin_dir="$(setup_fake_kubectl 'get ingressroutes.traefik.io')"
  if (PATH="$bin_dir:$PATH" require_kubernetes_permission get ingressroutes.traefik.io nchat-dev) >/dev/null 2>"$err_log"; then
    fail "RBAC preflight accepted a missing get permission on ingressroutes"
  fi
  grep -Fq 'get ingressroutes.traefik.io' "$err_log" || fail "RBAC preflight failure message omits verb/resource"
  grep -Fq 'nchat-dev' "$err_log" || fail "RBAC preflight failure message omits namespace"
  ! grep -Eqi 'kubeconfig|bearer|token|password' "$err_log" || fail "RBAC preflight leaked sensitive diagnostic data"

  bin_dir="$(setup_fake_kubectl 'patch ingressroutes.traefik.io')"
  if (PATH="$bin_dir:$PATH" require_kubernetes_permission patch ingressroutes.traefik.io nchat-dev) >/dev/null 2>&1; then
    fail "RBAC preflight accepted a missing patch permission on ingressroutes"
  fi
  if (kubectl() { printf yes; return 2; }; require_kubernetes_permission get ingressroutes.traefik.io nchat-dev) >/dev/null 2>&1; then
    fail "RBAC preflight accepted yes with kubectl exit 2"
  fi
  if (kubectl() { printf no; }; require_kubernetes_permission get ingressroutes.traefik.io nchat-dev) >/dev/null 2>&1; then
    fail "RBAC preflight accepted no"
  fi
  if (kubectl() { :; }; require_kubernetes_permission get ingressroutes.traefik.io nchat-dev) >/dev/null 2>&1; then
    fail "RBAC preflight accepted empty output"
  fi
  if (kubectl() { printf 'yes\nwarning'; }; require_kubernetes_permission get ingressroutes.traefik.io nchat-dev) >/dev/null 2>&1; then
    fail "RBAC preflight accepted multiline output"
  fi
  if (kubectl() { printf 'yes '; }; require_kubernetes_permission get ingressroutes.traefik.io nchat-dev) >/dev/null 2>&1; then
    fail "RBAC preflight accepted padded output"
  fi
  if (kubectl() { return 127; }; require_kubernetes_permission get ingressroutes.traefik.io nchat-dev) >/dev/null 2>&1; then
    fail "RBAC preflight accepted operational error"
  fi
}

validate_rendered_placeholder_gate() {
  local fixture="$TEMP_DIR/placeholders.yaml" err="$TEMP_DIR/placeholders.err"
  printf '%s\n' 'kind: ConfigMap' 'data:' '  VALUE: safe' >"$fixture"
  validate_rendered_placeholders "$fixture" || fail "placeholder gate rejected a clean manifest"
  printf '%s\n' 'kind: ConfigMap' 'data:' '  VALUE: REPLACE_ME_ONE' >"$fixture"
  if validate_rendered_placeholders "$fixture" >"$err" 2>&1; then fail "placeholder gate accepted one placeholder"; fi
  grep -Fq 'REPLACE_ME_ONE' "$err" || fail "placeholder diagnostic omitted token"
  printf '%s\n' 'REPLACE_ME_ONE' 'REPLACE_ME_TWO' >"$fixture"
  if validate_rendered_placeholders "$fixture" >"$err" 2>&1; then fail "placeholder gate accepted multiple placeholders"; fi
  grep -Fq 'REPLACE_ME_ONE' "$err" && grep -Fq 'REPLACE_ME_TWO' "$err" || fail "multiple placeholders were not diagnostic"
  cat >"$fixture" <<'EOF'
apiVersion: v1
kind: Secret
metadata:
  name: placeholder-test
stringData:
  password: super-sensitive-prefix-REPLACE_ME_SECRET-super-sensitive-suffix
EOF
  if validate_rendered_placeholders "$fixture" >"$err" 2>&1; then fail "placeholder gate accepted Secret placeholder"; fi
  grep -Fq 'REPLACE_ME_SECRET' "$err" || fail "Secret diagnostic omitted token"
  ! grep -Eqi 'super-sensitive-prefix|super-sensitive-suffix|password' "$err" || fail "Secret diagnostic leaked adjacent content"
  if validate_rendered_placeholders "$TEMP_DIR" >"$err" 2>&1; then fail "placeholder gate accepted unreadable input"; fi
  printf '%s\n' 'kind: ConfigMap' 'data:' '  VALUE: safe' >"$fixture"
  if (rendered_overlay_placeholder_matches() { return 2; }; validate_rendered_placeholders "$fixture") >"$err" 2>&1; then fail "placeholder gate accepted grep exit 2 without a placeholder"; fi
  printf '%s\n' 'REPLACE_ME_ONE' >"$fixture"
  if (rendered_overlay_placeholder_matches() { return 2; }; validate_rendered_placeholders "$fixture") >"$err" 2>&1; then fail "placeholder gate accepted grep exit 2"; fi
  if (rendered_overlay_placeholder_matches() { return 126; }; validate_rendered_placeholders "$fixture") >"$err" 2>&1; then fail "placeholder gate accepted grep exit 126"; fi
  if (rendered_overlay_placeholder_matches() { return 127; }; validate_rendered_placeholders "$fixture") >"$err" 2>&1; then fail "placeholder gate accepted grep exit 127"; fi
  if (format_rendered_placeholder_matches() { return 1; }; validate_rendered_placeholders "$fixture") >"$err" 2>&1; then fail "placeholder gate accepted formatter failure"; fi
}

setup_deploy_harness() {
  local bin_dir="$1"
  mkdir -p "$bin_dir"
  cat >"$bin_dir/kubectl" <<'EOF'
#!/usr/bin/env bash
printf '%s\t%s\t%s\n' "${1:-}" "${2:-}" "${3:-}" >>"$FAKE_KUBECTL_LOG"
case "$1 $2" in
  'config current-context') printf '%s\n' nchat-dev-deployer ;;
  'auth can-i')
    if [[ "${3:-} ${4:-}" == "$FAKE_DENY" ]]; then printf '%s\n' no; else printf '%s\n' "${FAKE_AUTH_OUTPUT:-yes}"; fi
    exit "${FAKE_AUTH_STATUS:-0}"
    ;;
  *) echo 'unexpected fake kubectl invocation' >&2; exit 1 ;;
esac
EOF
  cat >"$bin_dir/kustomize" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "${1:-}" >>"$FAKE_KUSTOMIZE_LOG"
[[ "$1" == version ]] && { printf '%s\n' v5.7.1; exit 0; }
exit 1
EOF
  cat >"$bin_dir/curl" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' called >>"$FAKE_CURL_LOG"
exit 1
EOF
  chmod +x "$bin_dir/kubectl" "$bin_dir/kustomize" "$bin_dir/curl"
}

assert_no_mutable_kubectl_operations() {
  local log="$1"
  if ! awk -F '\t' '$1 ~ /^(apply|delete|create|patch|replace|rollout|wait|scale|set|annotate|label)$/ { found=1 } END { exit found }' "$log"; then
    fail "RBAC denial allowed a mutable kubectl operation"
  fi
}

assert_preflight_only_kubectl_operations() {
  local log="$1"
  if ! awk -F '\t' '$1 != "config" && $1 != "auth" { found=1 } END { exit found }' "$log"; then
    fail "RBAC denial ran kubectl outside the preflight"
  fi
}

run_deploy_preflight_case() {
  local label="$1" deny="$2" auth_status="${3:-0}" bin_dir log kustomize_log curl_log output
  bin_dir="$TEMP_DIR/deploy-$label/bin"
  log="$TEMP_DIR/deploy-$label/kubectl.log"
  kustomize_log="$TEMP_DIR/deploy-$label/kustomize.log"
  curl_log="$TEMP_DIR/deploy-$label/curl.log"
  output="$TEMP_DIR/deploy-$label/output"
  mkdir -p "$TEMP_DIR/deploy-$label"
  : >"$log"; : >"$kustomize_log"; : >"$curl_log"
  setup_deploy_harness "$bin_dir"
  if PATH="$bin_dir:$PATH" FAKE_KUBECTL_LOG="$log" FAKE_KUSTOMIZE_LOG="$kustomize_log" FAKE_CURL_LOG="$curl_log" FAKE_DENY="$deny" FAKE_AUTH_STATUS="$auth_status" ARTIFACTS_DIR="$ARTIFACTS" DEPLOY_SHA=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa NCHAT_DEV_TOPOLOGY_FILE="$TOPOLOGY_FIXTURE" bash "$ROOT_DIR/scripts/deploy/nchat-dev/deploy.sh" >"$output" 2>&1; then
    fail "deploy accepted denied preflight case $label"
  fi
  assert_no_mutable_kubectl_operations "$log"
  assert_preflight_only_kubectl_operations "$log"
  [[ ! -s "$kustomize_log" ]] || fail "RBAC denial rendered an overlay ($label)"
  [[ ! -s "$curl_log" ]] || fail "RBAC denial called curl ($label)"
}

validate_real_deploy_preflight() {
  make_valid_artifacts
  run_deploy_preflight_case deny-get 'get ingressroutes.traefik.io'
  run_deploy_preflight_case deny-patch 'patch ingressroutes.traefik.io'
  run_deploy_preflight_case deny-create 'create ingresses.networking.k8s.io'
  run_deploy_preflight_case auth-error 'none none' 2
  local bin_dir="$TEMP_DIR/deploy-permitted/bin" log="$TEMP_DIR/deploy-permitted/kubectl.log" klog="$TEMP_DIR/deploy-permitted/kustomize.log" clog="$TEMP_DIR/deploy-permitted/curl.log"
  mkdir -p "$TEMP_DIR/deploy-permitted"; : >"$log"; : >"$klog"; : >"$clog"; setup_deploy_harness "$bin_dir"
  if PATH="$bin_dir:$PATH" FAKE_KUBECTL_LOG="$log" FAKE_KUSTOMIZE_LOG="$klog" FAKE_CURL_LOG="$clog" FAKE_DENY='none none' ARTIFACTS_DIR="$ARTIFACTS" DEPLOY_SHA=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa NCHAT_DEV_TOPOLOGY_FILE="$TOPOLOGY_FIXTURE" bash "$ROOT_DIR/scripts/deploy/nchat-dev/deploy.sh" >/dev/null 2>&1; then fail "fake deploy unexpectedly completed"; fi
  grep -Fqx edit "$klog" || fail "fully permitted deploy did not advance past preflight"
}

validate_commit_sha aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa || fail "valid commit SHA rejected"
validate_commit_sha 'abc' && fail "invalid commit SHA accepted"
validate_topology_contract
validate_kustomize_pin_contract
validate_ingress_host_assertion_contract
validate_hostname_unicode_locale_and_boundaries
validate_image_inventory
validate_rbac_preflight
validate_rendered_placeholder_gate
validate_real_deploy_preflight
if (cleanup_deploy_tree /) >/dev/null 2>&1; then
  fail "cleanup accepted the filesystem root"
fi

make_valid_artifacts
validate_digest_artifacts "$ARTIFACTS" || fail "valid digest artifact set rejected"

printf '%s ' "$DIGEST" >"$ARTIFACTS/digest-web.txt"
expect_invalid_artifacts "digest containing a space was accepted"
make_valid_artifacts
printf '%s\n' "$DIGEST" >"$ARTIFACTS/digest-web.txt"
expect_invalid_artifacts "digest containing a newline was accepted"
make_valid_artifacts
printf '%s' 'sha256:not-a-digest' >"$ARTIFACTS/digest-web.txt"
expect_invalid_artifacts "invalid digest was accepted"
make_valid_artifacts
rm "$ARTIFACTS/digest-web.txt"
expect_invalid_artifacts "missing digest was accepted"
make_valid_artifacts
printf '%s' "$DIGEST" >"$ARTIFACTS/unexpected.txt"
expect_invalid_artifacts "unexpected artifact was accepted"
make_valid_artifacts
rm "$ARTIFACTS/digest-web.txt"
ln -s digest-auth-service.txt "$ARTIFACTS/digest-web.txt"
expect_invalid_artifacts "digest symlink was accepted"

success_cleanup="$TEMP_DIR/success-cleanup"
prepare_deploy_tree "$ROOT_DIR" "$success_cleanup"
cleanup_deploy_tree "$success_cleanup"
[[ ! -e "$success_cleanup" ]] || fail "cleanup after success failed"

error_cleanup="$TEMP_DIR/error-cleanup"
if (trap 'cleanup_deploy_tree "$error_cleanup"' EXIT; prepare_deploy_tree "$ROOT_DIR" "$error_cleanup"; false); then
  fail "error cleanup fixture unexpectedly succeeded"
fi
[[ ! -e "$error_cleanup" ]] || fail "cleanup after error failed"

if require_pinned_kustomize; then
  before="$(git -C "$ROOT_DIR" diff --binary | sha256sum)"
  make_valid_artifacts
  render_root="$TEMP_DIR/render"
  prepare_deploy_tree "$ROOT_DIR" "$render_root"
  application="$render_root/infra-k8s/overlays/nchat-dev-server"
  migrations="$application/migrations"
  set_digest_image "$migrations" ghcr.io/nicrepository/nchat/migrations "$ARTIFACTS/digest-migrations.txt"
  for image in "${NCHAT_DEV_RUNTIME_IMAGES[@]}"; do
    set_digest_image "$application" "ghcr.io/nicrepository/nchat/$image" "$ARTIFACTS/digest-$image.txt"
  done
  validate_rendered_overlay "$migrations" "$TEMP_DIR/migrations.yaml"
  validate_rendered_overlay "$application" "$TEMP_DIR/application.yaml"
  [[ "$(grep -Fc "@$DIGEST" "$TEMP_DIR/application.yaml")" -eq 8 ]] || fail "application images are not digest-pinned"
  [[ "$(grep -Fc "@$DIGEST" "$TEMP_DIR/migrations.yaml")" -eq 1 ]] || fail "migration image is not digest-pinned"

  fixture_host='nchat-dev.example.invalid'
  assert_rendered_replacements "$TEMP_DIR/application.yaml" "$fixture_host"

  # Selector exactness: a replacement targeting Ingress/nchat-dev must never
  # also land on IngressRoute/nchat-dev-uploads, and vice versa. Breaking
  # exactly one target's selector must leave only that target's placeholder
  # unresolved, proving there is no cross-match either way.
  break_target_and_expect_placeholder() {
    local label="$1" sed_expr="$2" break_root output image broken expected document target target_kind target_name broken_kind broken_name
    break_root="$TEMP_DIR/break-$label"
    rm -rf "$break_root"
    prepare_deploy_tree "$ROOT_DIR" "$break_root"
    local overlay="$break_root/infra-k8s/overlays/nchat-dev-server"
    sed -i "$sed_expr" "$overlay/kustomization.yaml"
    for image in "${NCHAT_DEV_RUNTIME_IMAGES[@]}"; do
      set_digest_image "$overlay" "ghcr.io/nicrepository/nchat/$image" "$ARTIFACTS/digest-$image.txt"
    done
    output="$TEMP_DIR/broken-$label.yaml"
    if validate_rendered_overlay "$overlay" "$output" >/dev/null 2>&1; then
      fail "validate_rendered_overlay accepted a broken $label replacement selector"
    fi
    case "$label" in
      ingress-main) broken='Ingress nchat-dev'; expected=2 ;;
      ingress-http) broken='Ingress nchat-dev-http'; expected=1 ;;
      ingress-livekit) broken='Ingress nchat-dev-livekit'; expected=2 ;;
      certificate) broken='Certificate nchat-dev-tls'; expected=1 ;;
      ingressroute) broken='IngressRoute nchat-dev-uploads'; expected=1 ;;
    esac
    broken_kind="${broken% *}"; broken_name="${broken#* }"
    document="$(yaml_document "$output" "$broken_kind" "$broken_name")"
    [[ "$(grep -Fc REPLACE_ME_HOST <<<"$document")" -eq "$expected" ]] || fail "breaking $label did not leave exactly $expected placeholders in $broken"
    for target in 'Certificate nchat-dev-tls' 'Ingress nchat-dev' 'Ingress nchat-dev-http' 'Ingress nchat-dev-livekit' 'IngressRoute nchat-dev-uploads'; do
      [[ "$target" == "$broken" ]] && continue
      target_kind="${target% *}"; target_name="${target#* }"
      document="$(yaml_document "$output" "$target_kind" "$target_name")"
      ! grep -q REPLACE_ME_HOST <<<"$document" || fail "breaking $label also broke $target"
      grep -Fq "$fixture_host" <<<"$document" || fail "breaking $label did not preserve $target replacement"
    done
  }
  break_target_and_expect_placeholder ingress-main \
    's/^          name: nchat-dev$/          name: nchat-dev-broken/'
  break_target_and_expect_placeholder ingress-http \
    's/^          name: nchat-dev-http$/          name: nchat-dev-http-broken/'
  break_target_and_expect_placeholder ingress-livekit \
    's/^          name: nchat-dev-livekit$/          name: nchat-dev-livekit-broken/'
  break_target_and_expect_placeholder certificate \
    's/^          name: nchat-dev-tls$/          name: nchat-dev-tls-broken/'
  break_target_and_expect_placeholder ingressroute \
    's/^          name: nchat-dev-uploads$/          name: nchat-dev-uploads-broken/'

  # A placeholder in a brand-new resource that no replacement target names
  # must still be caught by the generic REPLACE_ME_[A-Z0-9_]+ gate.
  new_resource_root="$TEMP_DIR/new-resource"
  rm -rf "$new_resource_root"
  prepare_deploy_tree "$ROOT_DIR" "$new_resource_root"
  new_resource_overlay="$new_resource_root/infra-k8s/overlays/nchat-dev-server"
  for image in "${NCHAT_DEV_RUNTIME_IMAGES[@]}"; do
    set_digest_image "$new_resource_overlay" "ghcr.io/nicrepository/nchat/$image" "$ARTIFACTS/digest-$image.txt"
  done
  cat >"$new_resource_overlay/unlisted-resource.yaml" <<'EOF'
apiVersion: v1
kind: ConfigMap
metadata:
  name: nchat-dev-unlisted
data:
  VALUE: REPLACE_ME_NEW_RESOURCE
EOF
  (cd "$new_resource_overlay" && kustomize edit add resource unlisted-resource.yaml)
  if validate_rendered_overlay "$new_resource_overlay" "$TEMP_DIR/new-resource.yaml" >/dev/null 2>&1; then
    fail "validate_rendered_overlay accepted an unrelated REPLACE_ME_NEW_RESOURCE placeholder"
  fi
  grep -q 'REPLACE_ME_NEW_RESOURCE' "$TEMP_DIR/new-resource.yaml" || fail "unlisted resource placeholder was unexpectedly resolved"

  sidecar="$TEMP_DIR/sidecar"
  mkdir -p "$sidecar"
  cp "$ROOT_DIR/scripts/ci/testdata/nchat-dev-sidecar/deployment.yaml" "$sidecar/deployment.yaml"
  cp "$ROOT_DIR/scripts/ci/testdata/nchat-dev-sidecar/kustomization.yaml" "$sidecar/kustomization.yaml"
  cp "$ROOT_DIR/infra/k8s/overlays/nchat-dev-server/patches/auth-service.yaml" "$sidecar/auth-service-patch.yaml"
  kustomize build "$sidecar" >"$TEMP_DIR/sidecar.yaml"
  if grep -A 8 'name: sidecar' "$TEMP_DIR/sidecar.yaml" | grep -q DATABASE_URL; then
    fail "sidecar received application secrets"
  fi
  grep -B 8 -A 35 'name: auth-service' "$TEMP_DIR/sidecar.yaml" | grep -q DATABASE_URL || fail "named application container was not patched"
  after="$(git -C "$ROOT_DIR" diff --binary | sha256sum)"
  [[ "$before" == "$after" ]] || fail "deployment rendering changed the working tree"
else
  if [[ "${NCHAT_DEV_RENDER_REQUIRED:-1}" == 1 ]]; then
    fail "standalone Kustomize $KUSTOMIZE_VERSION is required for critical render checks"
  fi
  echo "info: standalone pinned kustomize unavailable; critical render checks skipped by NCHAT_DEV_RENDER_REQUIRED=0" >&2
fi

echo "nchat-dev deployment checks passed."
