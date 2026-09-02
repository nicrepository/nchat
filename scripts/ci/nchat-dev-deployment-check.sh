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
    elif [[ "$changed_key" == NCHAT_DEV_HOST && "$key" == NCHAT_DEV_PUBLIC_URL ]]; then
      value="https://$changed_value"
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
  local legacy="$TEMP_DIR/topology-legacy.env" legacy_root="$TEMP_DIR/topology-legacy-root"
  local normalized="$TEMP_DIR/topology-normalized.env" host248 host249
  load_nchat_dev_topology "$TOPOLOGY_FIXTURE" || fail "valid topology fixture rejected"

  cp "$TOPOLOGY_FIXTURE" "$legacy"
  printf '%s\n' 'LIVEKIT_API_URL=http://legacy-livekit.invalid:7880' >>"$legacy"
  [[ "$(wc -l <"$legacy")" -eq 12 ]] || fail "legacy topology fixture must contain 12 keys"
  mkdir -p "$legacy_root/infra/k8s/overlays/nchat-dev-server"
  cp "$legacy" "$legacy_root/infra/k8s/overlays/nchat-dev-server/topology.env"
  (
    unset NCHAT_DEV_TOPOLOGY_FILE NCHAT_DEV_NODE_IP NCHAT_DEV_NODE_CIDR NCHAT_DEV_HOST NCHAT_DEV_TURN_EXTERNAL_IP
    prepare_nchat_dev_topology "$legacy_root" "$normalized"
  ) ||
    fail "legacy topology with LIVEKIT_API_URL rejected"
  [[ "$(wc -l <"$normalized")" -eq 11 ]] || fail "normalized legacy topology must contain 11 keys"
  ! grep -q '^LIVEKIT_API_URL=' "$normalized" || fail "legacy LIVEKIT_API_URL reached normalized topology"
  cmp -s "$TOPOLOGY_FIXTURE" "$normalized" || fail "legacy LIVEKIT_API_URL changed normalized topology"

  make_topology_variant "$variant" NCHAT_DEV_NODE_IP 999.0.2.10
  expect_invalid_topology "$variant" "invalid IPv4 accepted"
  make_topology_variant "$variant" NCHAT_DEV_TURN_EXTERNAL_IP 999.51.100.20
  expect_invalid_topology "$variant" "invalid TURN external IPv4 accepted"
  make_topology_variant "$variant" NCHAT_DEV_TURN_EXTERNAL_IP 198.051.100.20
  expect_invalid_topology "$variant" "leading-zero TURN external IPv4 accepted"
  make_topology_variant "$variant" NCHAT_DEV_TURN_EXTERNAL_IP '198.51.100.20 '
  expect_invalid_topology "$variant" "whitespace in TURN external IPv4 accepted"
  make_topology_variant "$variant" NCHAT_DEV_TURN_EXTERNAL_IP ''
  expect_invalid_topology "$variant" "empty TURN external IPv4 accepted"
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
  # The deployment host must leave room for every label derived from it. The
  # longest is "admin." (issue #578) at six characters, which is now what caps
  # the host at 247: "turn." only needed five. A host one character longer
  # would produce an administrative hostname of 254 characters, which is not a
  # valid DNS name and would render a console nobody can reach.
  host247="$(printf 'a%.0s' {1..63}).$(printf 'b%.0s' {1..63}).$(printf 'c%.0s' {1..63}).$(printf 'd%.0s' {1..55})"
  host248="$(printf 'a%.0s' {1..63}).$(printf 'b%.0s' {1..63}).$(printf 'c%.0s' {1..63}).$(printf 'd%.0s' {1..56})"
  [[ "${#host247}" -eq 247 && "${#host248}" -eq 248 ]] || fail "hostname boundary fixture lengths are incorrect"
  make_topology_variant "$variant" NCHAT_DEV_HOST "$host247"
  load_nchat_dev_topology "$variant" || fail "247-character topology host rejected"
  make_topology_variant "$variant" NCHAT_DEV_HOST "$host248"
  expect_invalid_topology "$variant" "248-character topology host accepted despite derived admin hostname"
  cp "$TOPOLOGY_FIXTURE" "$variant"
  printf '%s\n' 'UNKNOWN_KEY=value' >>"$variant"
  expect_invalid_topology "$variant" "unexpected topology key accepted"
  grep -v '^NCHAT_DEV_TURN_EXTERNAL_IP=' "$TOPOLOGY_FIXTURE" >"$variant"
  expect_invalid_topology "$variant" "missing TURN external IPv4 accepted"
  cp "$TOPOLOGY_FIXTURE" "$variant"
  printf '%s\n' 'NCHAT_DEV_TURN_EXTERNAL_IP=198.51.100.21' >>"$variant"
  expect_invalid_topology "$variant" "duplicate TURN external IPv4 accepted"

  (
    unset NCHAT_DEV_TOPOLOGY_FILE
    export NCHAT_DEV_NODE_IP=192.0.2.20
    export NCHAT_DEV_NODE_CIDR=192.0.2.20/32
    export NCHAT_DEV_HOST=nchat-dev-ci.example.invalid
    export NCHAT_DEV_TURN_EXTERNAL_IP=198.51.100.20
    materialize_nchat_dev_topology \
      "$ROOT_DIR/infra/k8s/overlays/nchat-dev-server/topology.env.example" "$materialized"
  ) || fail "environment topology materialization failed"
  [[ "$(stat -c '%a' "$materialized")" == 600 ]] || fail "materialized topology permissions are not 0600"
  [[ "$(grep -Fxc 'NCHAT_DEV_TURN_EXTERNAL_IP=198.51.100.20' "$materialized")" -eq 1 ]] || \
    fail "materialized topology does not contain exactly one TURN external IPv4"
  load_nchat_dev_topology "$materialized" || fail "materialized topology is invalid"

  local empty_host_dest="$TEMP_DIR/empty-host.env"
  if (
    unset NCHAT_DEV_TOPOLOGY_FILE
    export NCHAT_DEV_NODE_IP=192.0.2.21
    export NCHAT_DEV_NODE_CIDR=192.0.2.21/32
    export NCHAT_DEV_HOST=
    export NCHAT_DEV_TURN_EXTERNAL_IP=198.51.100.21
    materialize_nchat_dev_topology \
      "$ROOT_DIR/infra/k8s/overlays/nchat-dev-server/topology.env.example" "$empty_host_dest"
  ) >/dev/null 2>&1; then
    fail "empty NCHAT_DEV_HOST was accepted"
  fi
  [[ ! -e "$empty_host_dest" ]] || fail "empty NCHAT_DEV_HOST produced a rendered topology file"

  local missing_turn_dest="$TEMP_DIR/missing-turn-external-ip.env"
  if (
    unset NCHAT_DEV_TOPOLOGY_FILE NCHAT_DEV_TURN_EXTERNAL_IP
    export NCHAT_DEV_NODE_IP=192.0.2.21
    export NCHAT_DEV_NODE_CIDR=192.0.2.21/32
    export NCHAT_DEV_HOST=nchat-dev-ci.example.invalid
    materialize_nchat_dev_topology \
      "$ROOT_DIR/infra/k8s/overlays/nchat-dev-server/topology.env.example" "$missing_turn_dest"
  ) >/dev/null 2>&1; then
    fail "missing NCHAT_DEV_TURN_EXTERNAL_IP was accepted"
  fi
  [[ ! -e "$missing_turn_dest" ]] || fail "missing NCHAT_DEV_TURN_EXTERNAL_IP produced a rendered topology file"

  if (
    unset NCHAT_DEV_TOPOLOGY_FILE NCHAT_DEV_NODE_IP NCHAT_DEV_NODE_CIDR NCHAT_DEV_HOST NCHAT_DEV_TURN_EXTERNAL_IP
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

# has_exact_certificate_first_dns_name DOCUMENT HOST
# Passes iff spec.dnsNames[0] == HOST exactly.
# Accepts both indentless and indented sequence items.
# Rejects: wrong value, port/suffix, second item correct with first wrong,
# value under another field, value in wrong Certificate, duplicate dnsNames.
has_exact_certificate_first_dns_name() {
  awk -v host="$2" '
    function indent(line,    i) { i=1; while (substr(line,i,1)==" ") i++; return i-1 }
    /^[[:space:]]*#/ || /^[[:space:]]*$/ { next }
    {
      lv = indent($0)
      if ($0 ~ /^spec:[[:space:]]*$/ && lv == 0) {
        spec_count++; in_spec=1; spec_lv=lv; child_lv=-1; in_dns=0
        next
      }
      if (in_spec && lv <= spec_lv) in_spec=0
      if (!in_spec) next
      if (child_lv < 0 && lv > spec_lv) child_lv=lv
      if (lv == child_lv && $0 ~ /^[[:space:]]*dnsNames:[[:space:]]*$/) {
        dns_count++; in_dns=1; dns_lv=lv; awaiting_first=1; first_lv=-1
        next
      }
      if (!in_dns) next
      if (awaiting_first) {
        if ($0 !~ /^[[:space:]]*-[[:space:]]+[^[:space:]]/) { invalid=1; in_dns=0; next }
        val=$0; sub(/^[[:space:]]*-[[:space:]]+/, "", val)
        if (val != host) { invalid=1; in_dns=0; next }
        awaiting_first=0; first_lv=lv; first_seen=1
        next
      }
      # A second sequence item at the same or parent level ends the list
      if (lv <= dns_lv) { in_dns=0; next }
      if (lv == first_lv && $0 ~ /^[[:space:]]*-[[:space:]]+/) { duplicate=1; in_dns=0; next }
    }
    END { exit !(spec_count==1 && dns_count==1 && first_seen && !invalid && !duplicate) }
  ' <<< "$1"
}

# has_exact_ingress_first_tls_host DOCUMENT HOST
# Passes iff spec.tls[0].hosts[0] == HOST exactly.
# Accepts both indentless and indented sequences.
# Rejects: value only in rules, first-item wrong with second correct,
# hosts under an unrelated field.
has_exact_ingress_first_tls_host() {
  awk -v host="$2" '
    function indent(line,    i) { i=1; while (substr(line,i,1)==" ") i++; return i-1 }
    /^[[:space:]]*#/ || /^[[:space:]]*$/ { next }
    {
      lv = indent($0)
      if ($0 ~ /^spec:[[:space:]]*$/ && lv == 0) {
        spec_count++; in_spec=1; spec_lv=lv; child_lv=-1; in_tls=0
        next
      }
      if (in_spec && lv <= spec_lv) in_spec=0
      if (!in_spec) next
      if (child_lv < 0 && lv > spec_lv) child_lv=lv
      # tls: must be a direct child of spec
      if (lv == child_lv && $0 ~ /^[[:space:]]*tls:[[:space:]]*$/) {
        tls_count++; in_tls=1; tls_lv=lv; await_tls_item=1; tls_item_lv=-1
        in_hosts=0; await_host=0; host_item_lv=-1
        next
      }
      if (in_tls && lv <= tls_lv && !($0 ~ /^[[:space:]]*-[[:space:]]/)) { in_tls=0; next }
      if (!in_tls) next
      # first tls list item
      if (await_tls_item) {
        if ($0 !~ /^[[:space:]]*-[[:space:]]/) { invalid=1; in_tls=0; next }
        tls_item_lv=lv; await_tls_item=0
        # strip leading "- " to get the rest of this line
        rest=$0; sub(/^[[:space:]]*-[[:space:]]+/, "", rest)
        if (rest == "hosts:" || rest ~ /^hosts:[[:space:]]*$/) {
          in_hosts=1; hosts_lv=tls_item_lv; await_host=1; host_item_lv=-1
        }
        next
      }
      # still inside tls[0]
      if (lv <= tls_lv) { in_tls=0; next }
      if (tls_item_lv >= 0 && lv == tls_item_lv && $0 ~ /^[[:space:]]*-[[:space:]]*/) {
        # second tls item — stop
        in_tls=0; next
      }
      # look for hosts: key inside tls[0]
      if (!in_hosts && $0 ~ /^[[:space:]]*hosts:[[:space:]]*$/) {
        in_hosts=1; hosts_lv=lv; await_host=1; host_item_lv=-1
        next
      }
      if (!in_hosts) next
      if (await_host) {
        if ($0 !~ /^[[:space:]]*-[[:space:]]+[^[:space:]]/) { invalid=1; in_hosts=0; next }
        val=$0; sub(/^[[:space:]]*-[[:space:]]+/, "", val)
        if (val != host) { invalid=1; in_hosts=0; next }
        await_host=0; host_item_lv=lv; first_seen=1
        next
      }
      if (host_item_lv >= 0 && lv == host_item_lv && $0 ~ /^[[:space:]]*-[[:space:]]/) {
        duplicate=1; in_hosts=0; next
      }
    }
    END { exit !(spec_count==1 && tls_count==1 && first_seen && !invalid && !duplicate) }
  ' <<< "$1"
}

# has_exact_ingressroute_first_match DOCUMENT HOST
# Passes iff spec.routes[0].match == exact expected string (with HOST).
# Accepts indentation variation before the match: key.
# Rejects: different host/method/path, match on another route, match in
# another IngressRoute.
has_exact_ingressroute_first_match() {
  local expected="Host(\`$2\`) && Method(\`POST\`) && PathRegexp(\`^/api/files/(channels|dm)/[^/]+/attachments$\`)"
  awk -v expected="$expected" '
    function indent(line,    i) { i=1; while (substr(line,i,1)==" ") i++; return i-1 }
    /^[[:space:]]*#/ || /^[[:space:]]*$/ { next }
    {
      lv = indent($0)
      if ($0 ~ /^spec:[[:space:]]*$/ && lv == 0) {
        spec_count++; in_spec=1; spec_lv=lv; child_lv=-1; in_routes=0
        next
      }
      if (in_spec && lv <= spec_lv) in_spec=0
      if (!in_spec) next
      if (child_lv < 0 && lv > spec_lv) child_lv=lv
      if (lv == child_lv && $0 ~ /^[[:space:]]*routes:[[:space:]]*$/) {
        routes_count++; in_routes=1; routes_lv=lv; await_item=1; item_lv=-1
        child2_lv=-1; match_seen=0
        next
      }
      if (!in_routes) next
      if (lv <= routes_lv && !($0 ~ /^[[:space:]]*-[[:space:]]/)) { in_routes=0; next }
      if (await_item) {
        if ($0 !~ /^[[:space:]]*-[[:space:]]/) { invalid=1; in_routes=0; next }
        item_lv=lv; await_item=0
        # inline match on first item line?
        rest=$0; sub(/^[[:space:]]*-[[:space:]]+/, "", rest)
        if (rest ~ /^match:[[:space:]]+/) {
          val=rest; sub(/^match:[[:space:]]+/, "", val)
          if (val == expected) match_seen=1; else invalid=1
        }
        next
      }
      # inside first route item
      if (item_lv >= 0 && lv == item_lv && $0 ~ /^[[:space:]]*-[[:space:]]/) {
        # second route item — stop tracking
        in_routes=0; next
      }
      if (lv <= routes_lv) { in_routes=0; next }
      # a match: key at child level inside first route item
      if (child2_lv < 0 && lv > item_lv) child2_lv=lv
      if (lv == child2_lv && $0 ~ /^[[:space:]]*match:[[:space:]]+/) {
        val=$0; sub(/^[[:space:]]*match:[[:space:]]+/, "", val)
        if (match_seen) { duplicate=1 }
        else if (val == expected) match_seen=1
        else invalid=1
        next
      }
    }
    END { exit !(spec_count==1 && routes_count==1 && match_seen && !invalid && !duplicate) }
  ' <<< "$1"
}

# has_exact_key_value DOCUMENT KEY VALUE
# Passes iff data.KEY == VALUE exactly, key is a direct child of data:,
# there is exactly one data: block, and no duplicate keys.
# Rejects: key under metadata/annotations, URL with port or extra path,
# duplicate keys.
has_exact_key_value() {
  awk -v key="$2" -v val="$3" '
    function indent(line,    i) { i=1; while (substr(line,i,1)==" ") i++; return i-1 }
    /^[[:space:]]*#/ || /^[[:space:]]*$/ { next }
    {
      lv = indent($0)
      if ($0 ~ /^data:[[:space:]]*$/ && lv == 0) {
        data_count++; in_data=1; data_lv=lv; child_lv=-1
        next
      }
      if (in_data && lv <= data_lv) in_data=0
      if (!in_data) next
      if (child_lv < 0 && lv > data_lv) child_lv=lv
      if (lv == child_lv) {
        pat="^[[:space:]]*" key ":[[:space:]]+"
        if ($0 ~ pat) {
          found_count++
          v=$0; sub(/^[[:space:]]*[^:]+:[[:space:]]+/, "", v)
          if (v == val) match_count++
        }
      }
    }
    END { exit !(data_count==1 && found_count==1 && match_count==1) }
  ' <<< "$1"
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

validate_certificate_dns_assertion_contract() {
  local host='nchat-dev.example.invalid'
  # indentless sequence (Kustomize v5.7.1 style)
  has_exact_certificate_first_dns_name $'spec:\n  dnsNames:\n  - nchat-dev.example.invalid' "$host" \
    || fail "Certificate indentless dnsNames rejected"
  # indented sequence (original style)
  has_exact_certificate_first_dns_name $'spec:\n  dnsNames:\n    - nchat-dev.example.invalid' "$host" \
    || fail "Certificate indented dnsNames rejected"
  # first item wrong, second correct -> must fail
  ! has_exact_certificate_first_dns_name \
    $'spec:\n  dnsNames:\n  - wrong.example.invalid\n  - nchat-dev.example.invalid' "$host" \
    || fail "Certificate first-wrong second-correct dnsNames accepted"
  # value correct but in wrong field, not dnsNames -> must fail
  ! has_exact_certificate_first_dns_name \
    $'spec:\n  secretName: nchat-dev.example.invalid' "$host" \
    || fail "Certificate value in non-dnsNames field accepted"
  # value in a different Certificate document (yaml_document isolates, but guard the helper itself)
  ! has_exact_certificate_first_dns_name \
    $'spec:\n  issuerRef:\n    name: nchat-dev.example.invalid' "$host" \
    || fail "Certificate value in nested non-dnsNames field accepted"
  # duplicate dnsNames keys -> must fail
  ! has_exact_certificate_first_dns_name \
    $'spec:\n  dnsNames:\n  - nchat-dev.example.invalid\n  dnsNames:\n  - nchat-dev.example.invalid' "$host" \
    || fail "Certificate duplicate dnsNames keys accepted"
  # value correct but with port suffix -> must fail
  ! has_exact_certificate_first_dns_name \
    $'spec:\n  dnsNames:\n  - nchat-dev.example.invalid:443' "$host" \
    || fail "Certificate host with port accepted"
  # value correct but with extra suffix -> must fail
  ! has_exact_certificate_first_dns_name \
    $'spec:\n  dnsNames:\n  - nchat-dev.example.invalid.attacker.example' "$host" \
    || fail "Certificate host with suffix accepted"
}

validate_ingress_tls_assertion_contract() {
  local host='nchat-dev.example.invalid'
  # indentless hosts list
  has_exact_ingress_first_tls_host \
    $'spec:\n  tls:\n  - hosts:\n    - nchat-dev.example.invalid' "$host" \
    || fail "Ingress TLS indentless hosts rejected"
  # indented hosts list
  has_exact_ingress_first_tls_host \
    $'spec:\n  tls:\n  - hosts:\n      - nchat-dev.example.invalid' "$host" \
    || fail "Ingress TLS indented hosts rejected"
  # host only in rules, not in tls -> must fail
  ! has_exact_ingress_first_tls_host \
    $'spec:\n  rules:\n  - host: nchat-dev.example.invalid' "$host" \
    || fail "Ingress TLS: host only in rules was accepted"
  # first tls.hosts wrong, second correct -> must fail
  ! has_exact_ingress_first_tls_host \
    $'spec:\n  tls:\n  - hosts:\n    - wrong.example.invalid\n    - nchat-dev.example.invalid' "$host" \
    || fail "Ingress TLS: first hosts wrong second correct accepted"
  # hosts under unrelated field -> must fail
  ! has_exact_ingress_first_tls_host \
    $'spec:\n  unrelated:\n  - hosts:\n    - nchat-dev.example.invalid' "$host" \
    || fail "Ingress TLS: hosts under unrelated field accepted"
}

validate_ingress_http_assertion_contract() {
  local host='nchat-dev.example.invalid'
  # rules[0].host correct -> passes
  has_exact_ingress_first_rule_host \
    $'spec:\n  rules:\n  - host: nchat-dev.example.invalid' "$host" \
    || fail "Ingress HTTP: correct rules[0].host rejected"
  # host nested under http sub-object -> must fail
  ! has_exact_ingress_first_rule_host \
    $'spec:\n  rules:\n  - http:\n      host: nchat-dev.example.invalid' "$host" \
    || fail "Ingress HTTP: host nested under http accepted"
  # host only in rules[1] -> must fail
  ! has_exact_ingress_first_rule_host \
    $'spec:\n  rules:\n  - http:\n      paths: []\n  - host: nchat-dev.example.invalid' "$host" \
    || fail "Ingress HTTP: host only in rules[1] accepted"
}

validate_ingressroute_match_assertion_contract() {
  local host='nchat-dev.example.invalid'
  local expected="Host(\`$host\`) && Method(\`POST\`) && PathRegexp(\`^/api/files/(channels|dm)/[^/]+/attachments$\`)"
  # correct match with two-space indentation
  has_exact_ingressroute_first_match \
    $'spec:\n  routes:\n  - match: Host(`nchat-dev.example.invalid`) && Method(`POST`) && PathRegexp(`^/api/files/(channels|dm)/[^/]+/attachments$`)' \
    "$host" || fail "IngressRoute: two-space match rejected"
  # correct match with four-space indentation
  has_exact_ingressroute_first_match \
    $'spec:\n  routes:\n    - match: Host(`nchat-dev.example.invalid`) && Method(`POST`) && PathRegexp(`^/api/files/(channels|dm)/[^/]+/attachments$`)' \
    "$host" || fail "IngressRoute: four-space match rejected"
  # wrong Method -> must fail
  ! has_exact_ingressroute_first_match \
    $'spec:\n  routes:\n  - match: Host(`nchat-dev.example.invalid`) && Method(`GET`) && PathRegexp(`^/api/files/(channels|dm)/[^/]+/attachments$`)' \
    "$host" || fail "IngressRoute: wrong Method accepted"
  # wrong PathRegexp -> must fail
  ! has_exact_ingressroute_first_match \
    $'spec:\n  routes:\n  - match: Host(`nchat-dev.example.invalid`) && Method(`POST`) && PathRegexp(`^/other$`)' \
    "$host" || fail "IngressRoute: wrong PathRegexp accepted"
  # wrong hostname -> must fail
  ! has_exact_ingressroute_first_match \
    $'spec:\n  routes:\n  - match: Host(`wrong.example.invalid`) && Method(`POST`) && PathRegexp(`^/api/files/(channels|dm)/[^/]+/attachments$`)' \
    "$host" || fail "IngressRoute: wrong hostname accepted"
  # match on second route only -> must fail
  ! has_exact_ingressroute_first_match \
    $'spec:\n  routes:\n  - kind: Rule\n  - match: Host(`nchat-dev.example.invalid`) && Method(`POST`) && PathRegexp(`^/api/files/(channels|dm)/[^/]+/attachments$`)' \
    "$host" || fail "IngressRoute: match on route[1] accepted"
}

validate_configmap_key_value_assertion_contract() {
  local host='nchat-dev.example.invalid'
  # correct key in data -> passes
  has_exact_key_value \
    $'data:\n  AUTH_PUBLIC_WEB_BASE_URL: https://nchat-dev.example.invalid' \
    AUTH_PUBLIC_WEB_BASE_URL "https://$host" \
    || fail "ConfigMap: correct key in data rejected"
  # correct key with two-space indent -> passes
  has_exact_key_value \
    $'apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: nchat-config\ndata:\n  AUTH_PUBLIC_WEB_BASE_URL: https://nchat-dev.example.invalid\n  OTHER_KEY: other-value' \
    AUTH_PUBLIC_WEB_BASE_URL "https://$host" \
    || fail "ConfigMap: correct key among siblings rejected"
  # key under metadata annotations -> must fail
  ! has_exact_key_value \
    $'metadata:\n  annotations:\n    AUTH_PUBLIC_WEB_BASE_URL: https://nchat-dev.example.invalid\ndata:\n  OTHER: value' \
    AUTH_PUBLIC_WEB_BASE_URL "https://$host" \
    || fail "ConfigMap: key under metadata annotations accepted"
  # URL with port -> must fail
  ! has_exact_key_value \
    $'data:\n  AUTH_PUBLIC_WEB_BASE_URL: https://nchat-dev.example.invalid:443' \
    AUTH_PUBLIC_WEB_BASE_URL "https://$host" \
    || fail "ConfigMap: URL with port accepted"
  # URL with path -> must fail
  ! has_exact_key_value \
    $'data:\n  AUTH_PUBLIC_WEB_BASE_URL: https://nchat-dev.example.invalid/extra' \
    AUTH_PUBLIC_WEB_BASE_URL "https://$host" \
    || fail "ConfigMap: URL with path accepted"
  # duplicate key -> must fail
  ! has_exact_key_value \
    $'data:\n  AUTH_PUBLIC_WEB_BASE_URL: https://nchat-dev.example.invalid\n  AUTH_PUBLIC_WEB_BASE_URL: https://nchat-dev.example.invalid' \
    AUTH_PUBLIC_WEB_BASE_URL "https://$host" \
    || fail "ConfigMap: duplicate key accepted"
}

assert_rendered_replacements() {
  local rendered="$1" host="$2" document admin_host="admin.$2"
  document="$(yaml_document "$rendered" Certificate nchat-dev-tls)"
  has_exact_certificate_first_dns_name "$document" "$host" || fail "Certificate/nchat-dev-tls hostname replacement failed"
  document="$(yaml_document "$rendered" Ingress nchat-dev)"
  has_exact_ingress_first_rule_host "$document" "$host" && has_exact_ingress_first_tls_host "$document" "$host" || fail "Ingress/nchat-dev hostname targets failed"
  document="$(yaml_document "$rendered" Ingress nchat-dev-http)"
  has_exact_ingress_first_rule_host "$document" "$host" || fail "Ingress/nchat-dev-http hostname target failed"
  document="$(yaml_document "$rendered" Ingress nchat-dev-livekit)"
  has_exact_ingress_first_rule_host "$document" "$host" && has_exact_ingress_first_tls_host "$document" "$host" || fail "Ingress/nchat-dev-livekit hostname targets failed"
  document="$(yaml_document "$rendered" IngressRoute nchat-dev-uploads)"
  has_exact_ingressroute_first_match "$document" "$host" || fail "IngressRoute/nchat-dev-uploads match replacement failed"
  document="$(yaml_document "$rendered" ConfigMap nchat-config)"
  has_exact_key_value "$document" AUTH_PUBLIC_WEB_BASE_URL "https://$host" || fail "ConfigMap/nchat-config public URL replacement failed"
  # The administrative console host (issue #578). Derived as admin.<host>, so
  # asserting the derivation here is what catches a topology pipeline that stops
  # deriving it — at which point the console would render with an unresolved
  # placeholder and be unreachable.
  document="$(yaml_document "$rendered" Ingress nchat-dev-admin)"
  has_exact_ingress_first_rule_host "$document" "$admin_host" && has_exact_ingress_first_tls_host "$document" "$admin_host" || fail "Ingress/nchat-dev-admin hostname targets failed"
  ! grep -q REPLACE_ME_HOST "$rendered" || fail "rendered manifest still contains REPLACE_ME_HOST"
  ! grep -q REPLACE_ME_ADMIN_HOST "$rendered" || fail "rendered manifest still contains REPLACE_ME_ADMIN_HOST"
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

# The caller of the single builder (CICD-05) has to stay a caller: it delegates
# every build to build-nchat-images.yml and grows no matrix of its own, so the
# eleven images cannot drift between development and production. The rest of the
# two-workflow contract -- the protected dispatch, the immutable SHA, the
# reachable-from-main gate and the registry permission boundary -- is owned by
# scripts/ci/check_build_images_workflow.py, which pnpm ci runs against both
# files; restating it here would only give it a second, weaker definition.
check_single_builder_caller() {
  local caller="$1"
  # An active `uses:` key, not the path wherever it happens to appear: a comment
  # or a quoted value mentioning the builder proves nothing about the wiring.
  grep -Eq '^[[:space:]]*uses:[[:space:]]+\./\.github/workflows/build-nchat-images\.yml[[:space:]]*$' "$caller" || return 1
  ! grep -Eq '^[[:space:]]*matrix:' "$caller" || return 1
}

# A caller whose delegation moved to another workflow with the builder path left
# behind as a comment. Every string a substring matcher looks for is still in
# the file; nothing delegates to the single builder any more.
comment_out_single_builder() {
  local line
  while IFS= read -r line; do
    case "$line" in
      *'uses: ./.github/workflows/build-nchat-images.yml')
        printf '%s\n' "${line%%uses:*}uses: ./.github/workflows/other.yml" "#$line" ;;
      *) printf '%s\n' "$line" ;;
    esac
  done
}

validate_single_builder_contract() {
  local caller="$ROOT_DIR/.github/workflows/images.yml" mutant="$TEMP_DIR/images-caller.yml"
  check_single_builder_caller "$caller" || fail "image workflow no longer delegates to the single builder"
  grep -Fv 'uses: ./.github/workflows/build-nchat-images.yml' "$caller" >"$mutant"
  ! check_single_builder_caller "$mutant" || fail "a caller that dropped the single builder was accepted"
  { cat "$caller"; printf '      matrix:\n'; } >"$mutant"
  ! check_single_builder_caller "$mutant" || fail "a caller with a build matrix of its own was accepted"
  comment_out_single_builder <"$caller" >"$mutant"
  [[ "$(grep -Ec '^[[:space:]]*#[[:space:]]*uses:[[:space:]]+\./\.github/workflows/build-nchat-images\.yml$' "$mutant")" -eq 1 ]] || fail "the commented-builder fixture did not apply"
  ! check_single_builder_caller "$mutant" || fail "a caller that only mentions the single builder in a comment was accepted"
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
  validate_single_builder_contract
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
    elif [[ "$deployment" == nchat-admin-web ]]; then
      service="$ROOT_DIR/infra/k8s/base/admin-web/service.yaml"
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
validate_certificate_dns_assertion_contract
validate_ingress_tls_assertion_contract
validate_ingress_http_assertion_contract
validate_ingressroute_match_assertion_contract
validate_configmap_key_value_assertion_contract
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
  [[ "$(grep -Fc "@$DIGEST" "$TEMP_DIR/application.yaml")" -eq 10 ]] || fail "application images are not digest-pinned"
  [[ "$(grep -Fc "@$DIGEST" "$TEMP_DIR/migrations.yaml")" -eq 1 ]] || fail "migration image is not digest-pinned"

  fixture_host='nchat-dev.example.invalid'
  assert_rendered_replacements "$TEMP_DIR/application.yaml" "$fixture_host"

  # Selector exactness: a replacement targeting Ingress/nchat-dev must never
  # also land on IngressRoute/nchat-dev-uploads, and vice versa. Breaking
  # exactly one target's selector must leave only that target's placeholder
  # unresolved, proving there is no cross-match either way.
  break_target_and_expect_placeholder() {
    local label="$1" sed_expr="$2" break_root output image broken expected placeholder document target target_kind target_name broken_kind broken_name
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
    # placeholder names which token the broken target should be left holding:
    # the administrative host is substituted from its own topology value, so a
    # broken admin selector leaves REPLACE_ME_ADMIN_HOST, not REPLACE_ME_HOST.
    placeholder=REPLACE_ME_HOST
    case "$label" in
      ingress-main) broken='Ingress nchat-dev'; expected=2 ;;
      ingress-http) broken='Ingress nchat-dev-http'; expected=1 ;;
      ingress-livekit) broken='Ingress nchat-dev-livekit'; expected=2 ;;
      certificate) broken='Certificate nchat-dev-tls'; expected=1 ;;
      ingressroute) broken='IngressRoute nchat-dev-uploads'; expected=1 ;;
      ingress-admin) broken='Ingress nchat-dev-admin'; expected=2; placeholder=REPLACE_ME_ADMIN_HOST ;;
    esac
    broken_kind="${broken% *}"; broken_name="${broken#* }"
    document="$(yaml_document "$output" "$broken_kind" "$broken_name")"
    [[ "$(grep -Fc "$placeholder" <<<"$document")" -eq "$expected" ]] || fail "breaking $label did not leave exactly $expected $placeholder placeholders in $broken"
    for target in 'Certificate nchat-dev-tls' 'Ingress nchat-dev' 'Ingress nchat-dev-http' 'Ingress nchat-dev-livekit' 'IngressRoute nchat-dev-uploads' 'Ingress nchat-dev-admin'; do
      [[ "$target" == "$broken" ]] && continue
      target_kind="${target% *}"; target_name="${target#* }"
      document="$(yaml_document "$output" "$target_kind" "$target_name")"
      ! grep -q REPLACE_ME <<<"$document" || fail "breaking $label also broke $target"
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
  break_target_and_expect_placeholder ingress-admin \
    's/^          name: nchat-dev-admin$/          name: nchat-dev-admin-broken/'

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
