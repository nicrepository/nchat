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

# port_pairs prints one "PROTOCOL/PORT" line per ports: list entry found in an
# isolated NetworkPolicy document, regardless of whether the renderer emits
# "port:" or "protocol:" first within each entry.
port_pairs() {
  awk '
    function emit() { if (proto != "" || port != "") { print proto "/" port; proto = ""; port = "" } }
    /^[[:space:]]*- (port:|protocol:)/ { emit() }
    /port:/ { port = $NF }
    /protocol:/ { proto = $NF }
    END { emit() }
  ' <<<"$1"
}

# network_policy_flows prints one sorted "DIRECTION COMPONENT PROTOCOL/PORT"
# line per (peer, port) pair of every ingress/egress rule in the NetworkPolicy
# documents it is given. Unlike port_pairs it keeps the peer attached to the
# port, which is the whole question for the SeaweedFS rules: 8888 and 8080 are
# both reachable on the same pod, and only the peer distinguishes the flow that
# is intended from the one that must never exist.
#
# A peer selected by matchExpressions rather than matchLabels prints as
# "<none>". That is deliberate: the pairing is lost, but the port is not, so a
# rule that granted 8080 through the other selector form still shows up and
# still breaks an exact-set assertion instead of passing unseen.
network_policy_flows() {
  awk '
    function emit_port() {
      if (proto != "" || port != "") { pairs[++np] = proto "/" port; proto = ""; port = "" }
    }
    function flush(  i, j) {
      if (np > 0) {
        if (nc == 0) { comps[1] = "<none>"; nc = 1 }
        for (i = 1; i <= nc; i++) for (j = 1; j <= np; j++) print direction, comps[i], pairs[j]
      }
      nc = 0; np = 0; proto = ""; port = ""
    }
    /^  (ingress|egress):[[:space:]]*$/ {
      emit_port(); flush(); direction = $1; sub(/:$/, "", direction); active = 1; next
    }
    /^---$/ || /^[^[:space:]]/ || /^  [a-zA-Z]/ { emit_port(); flush(); active = 0; next }
    active != 1 { next }
    /^  - / { emit_port(); flush() }
    /^[[:space:]]*- (port:|protocol:)/ { emit_port() }
    /^[[:space:]]+app\.kubernetes\.io\/component: / { comps[++nc] = $NF }
    /port:/ { port = $NF }
    /protocol:/ { proto = $NF }
    END { emit_port(); flush() }
  ' <<<"$1" | LC_ALL=C sort
}

# yaml_documents_of_kind concatenates every document whose kind matches, unlike
# yaml_document which stops at the first. Needed for negative assertions, where
# "no routing object mentions the scanner" has to look at all of them.
yaml_documents_of_kind() {
  local file="$1" wanted_kinds="$2"
  awk -v wanted="$wanted_kinds" '
    function emit() { if (kind != "" && index(wanted, "|" kind "|")) printf "%s", document }
    /^---$/ { emit(); document=""; kind=""; next }
    { document=document $0 ORS }
    /^kind:/ { kind=$2 }
    END { emit() }
  ' "$file"
}

# clamd_directive prints the value of a clamd.conf directive, ignoring the
# comments that explain it. Empty output means the directive is not set, which
# for this project is a failure rather than "use the default": an implicit
# default is a security-relevant number nobody reviewed.
clamd_directive() {
  awk -v name="$2" '$1 == name { $1 = ""; sub(/^[[:space:]]+/, ""); print; exit }' "$1"
}

# clamd_directive_count counts the ACTIVE occurrences of a directive, by name
# and regardless of value.
#
# It exists because clamd applies the *last* setting it reads, so a duplicate
# does not conflict — it silently wins. Checking that one line with the expected
# value is present therefore proves nothing on its own: "AlertExceedsMax yes"
# followed by "AlertExceedsMax no" satisfies that check and disables the
# heuristic. Comments do not count: on "# MaxScanTime 500000" the first field is
# "#", so only a real directive is ever matched.
clamd_directive_count() {
  awk -v name="$2" '$1 == name { total++ } END { print total + 0 }' "$1"
}

# require_single_clamd_directive fails unless the directive is set exactly once.
require_single_clamd_directive() {
  local conf="$1" name="$2" count
  count="$(clamd_directive_count "$conf" "$name")"
  if [[ "$count" -eq 0 ]]; then
    echo "error: $name must be explicitly configured exactly once in clamd.conf" >&2
    return 1
  fi
  if [[ "$count" -gt 1 ]]; then
    echo "error: $name must be configured exactly once; found $count active directives." >&2
    echo "clamd applies the last one, so a duplicate silently overrides the reviewed value." >&2
    return 1
  fi
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

# validate_scan_time_ordering checks the one clamd setting that decides whether
# an unfinished scan can look finished.
#
# The order that must hold is file-service's deadline < the worker's claim lease
# < clamd's MaxScanTime. When file-service's deadline expires first it closes
# the connection, the scan fails, nothing is written and the attachment stays in
# pending_scan. If the engine's own limit fired first, the engine would decide,
# which is exactly what the threat model refused.
#
# Uniqueness is checked before the value is read, because clamd keeps the last
# occurrence: a duplicate silently overrides the reviewed number.
validate_scan_time_ordering() {
  local conf="$1" scan_timeout="$2" max_scan_time required_ms
  require_single_clamd_directive "$conf" MaxScanTime || return 1
  max_scan_time="$(clamd_directive "$conf" MaxScanTime)"
  # Milliseconds, as a bare integer: clamd takes no unit suffix here, so "420s"
  # is a misconfiguration and not a shorter way of saying the same thing.
  if [[ ! "$max_scan_time" =~ ^[0-9]+$ ]]; then
    echo "error: MaxScanTime must be a plain integer in milliseconds, got '$max_scan_time'." >&2
    return 1
  fi
  # A margin, so the ordering does not hold by a millisecond of luck across
  # scheduling, throttling and clock skew between two processes.
  required_ms=$(((scan_timeout + 60) * 1000))
  if ((max_scan_time < required_ms)); then
    echo "error: MaxScanTime ($max_scan_time ms) must be at least" >&2
    echo "(FILE_MALWARE_SCAN_TIMEOUT_SECONDS + 60) * 1000 = $required_ms ms, so the" >&2
    echo "file-service deadline stays the fail-closed authority over a scan." >&2
    return 1
  fi
}

# selftest_scan_time_ordering proves the check above reacts to each way the
# setting can go wrong.
#
# It runs against throwaway fixtures, never the versioned clamd.conf, and it
# runs in CI: a future edit that makes the gate blind fails here instead of
# shipping. The reference timeout is 300 s, so the contract floor is 360000 ms.
selftest_scan_time_ordering() {
  local fixture failures=0 label expected observed body
  fixture="$(mktemp "${TMPDIR:-/tmp}/nchat-clamd-selftest.XXXXXX")"

  run_case() {
    label="$1"; expected="$2"; body="$3"
    printf '%b' "$body" >"$fixture"
    observed=0
    validate_scan_time_ordering "$fixture" 300 >/dev/null 2>&1 || observed=1
    if [[ "$observed" -ne "$expected" ]]; then
      echo "error: MaxScanTime self-test '$label' expected $([[ "$expected" -eq 0 ]] && echo pass || echo fail)," >&2
      echo "observed $([[ "$observed" -eq 0 ]] && echo pass || echo fail)." >&2
      failures=$((failures + 1))
    fi
  }

  run_case 'single valid directive'      0 'MaxScanTime 420000\n'
  run_case 'directive absent'            1 'AlertExceedsMax yes\n'
  run_case 'two active directives'       1 'MaxScanTime 420000\nMaxScanTime 500000\n'
  run_case 'active plus commented'       0 'MaxScanTime 420000\n# MaxScanTime 500000\n'
  run_case 'non-numeric value'           1 'MaxScanTime banana\n'
  run_case 'unit suffix instead of ms'   1 'MaxScanTime 420s\n'
  run_case 'below the required floor'    1 'MaxScanTime 120000\n'
  run_case 'exactly at the floor'        0 'MaxScanTime 360000\n'
  run_case 'one millisecond short'       1 'MaxScanTime 359999\n'

  unset -f run_case
  rm -f "$fixture"
  [[ "$failures" -eq 0 ]]
}

# validate_clamav asserts the antimalware workload and, more importantly, the
# two properties a verdict depends on (RF-22, issue #483).
#
# The threat model rejected an earlier design because "clean" did not prove a
# finished scan: with AlertExceedsMax off, clamd answers OK after abandoning a
# file at a limit, and with MaxScanTime below file-service's own deadline the
# engine — not the service — decides what an unfinished scan looks like. Both
# are asserted here rather than left to review.
validate_clamav() {
  local application="$1" conf="$ROOT_DIR/infra/k8s/base/services/clamav/clamd.conf"
  local block routing scan_timeout directive value expected
  local -a images=()

  [[ -f "$conf" ]] || { echo "error: missing $conf" >&2; return 1; }

  selftest_scan_time_ordering || return 1

  # One clamd.conf, shared by Compose and Kubernetes. A second copy is how the
  # two quietly stop agreeing about what the scanner enforces.
  if [[ "$(find "$ROOT_DIR/infra" -name clamd.conf -type f | wc -l)" -ne 1 ]]; then
    echo "error: there must be exactly one versioned clamd.conf under infra/" >&2
    return 1
  fi
  if ! grep -Fq '../k8s/base/services/clamav/clamd.conf:/etc/clamav/clamd.conf:ro' \
    "$ROOT_DIR/infra/compose/compose.dev.yml"; then
    echo "error: Compose must mount the same clamd.conf the ConfigMap is generated from" >&2
    return 1
  fi

  # The scanner is rendered by the nchat-dev overlay alone; base must not carry
  # it, or k3s-dev and k3s-staging would inherit a 1 GiB daemon they never call.
  if grep -Eq '^[[:space:]]*-[[:space:]]*services/clamav[[:space:]]*$' \
    "$ROOT_DIR/infra/k8s/base/kustomization.yaml"; then
    echo "error: base/kustomization.yaml must not reference services/clamav" >&2
    return 1
  fi

  # --- verdict semantics -----------------------------------------------------
  #
  # Uniqueness comes first, for every directive whose value decides a verdict.
  # Asserting that the wanted line is present is not enough on its own: clamd
  # keeps the last setting it reads, so an appended duplicate overrides the
  # reviewed one while the "is it there?" check still passes.
  require_single_clamd_directive "$conf" AlertExceedsMax || return 1
  if [[ "$(grep -Exc 'AlertExceedsMax yes' "$conf")" -ne 1 ]]; then
    echo "error: clamd.conf must set AlertExceedsMax yes exactly once: without it a" >&2
    echo "file abandoned at a limit is answered as OK and recorded as clean." >&2
    return 1
  fi

  scan_timeout="$(awk -F': ' '/^  FILE_MALWARE_SCAN_TIMEOUT_SECONDS:/ {gsub(/"/, "", $2); print $2; exit}' \
    "$application")"
  if [[ ! "$scan_timeout" =~ ^[0-9]+$ ]]; then
    echo "error: FILE_MALWARE_SCAN_TIMEOUT_SECONDS must be set in nchat-config." >&2
    return 1
  fi
  validate_scan_time_ordering "$conf" "$scan_timeout" || return 1

  # --- limits, stated rather than inherited ----------------------------------
  for directive in \
    'StreamMaxLength 512M' 'MaxFileSize 512M' 'MaxScanSize 1024M' \
    'MaxRecursion 12' 'MaxFiles 10000' 'MaxThreads 4' \
    'MaxEmbeddedPE 40M' 'MaxHTMLNormalize 40M' 'MaxHTMLNoTags 8M' \
    'MaxScriptNormalize 20M' 'MaxZipTypeRcg 1M' 'MaxPartitions 50' \
    'MaxIconsPE 100' 'MaxRecHWP3 16' 'PCREMatchLimit 100000' \
    'PCRERecMatchLimit 2000' 'PCREMaxFileSize 100M'; do
    if [[ "$(grep -Exc -- "$directive" "$conf")" -ne 1 ]]; then
      echo "error: clamd.conf must declare exactly one: $directive" >&2
      return 1
    fi
    # And no second setting of the same limit under a different value, which the
    # exact-line count above cannot see.
    require_single_clamd_directive "$conf" "${directive%% *}" || return 1
  done
  # Absent on purpose, each for a reason recorded in the file itself. Matched at
  # the start of a line so the comments explaining the absence do not trip it.
  for directive in User LocalSocket PidFile ForceToDisk; do
    if grep -Eq "^${directive}[[:space:]]" "$conf"; then
      echo "error: clamd.conf must not set $directive" >&2
      return 1
    fi
  done
  # The generator wired this exact file, not some other one.
  grep -Fq 'AlertExceedsMax yes' "$application" || {
    echo "error: the rendered clamav-config ConfigMap does not carry clamd.conf" >&2
    return 1
  }

  # --- workload --------------------------------------------------------------
  block="$(yaml_document "$application" Deployment clamav)"
  [[ -n "$block" ]] || { echo "error: nchat-dev must render Deployment/clamav" >&2; return 1; }

  for value in 'runAsUser: 100' 'runAsGroup: 101' 'fsGroup: 101' 'runAsNonRoot: true' \
    'automountServiceAccountToken: false' 'enableServiceLinks: false' 'type: Recreate'; do
    grep -Fq "$value" <<<"$block" || {
      echo "error: Deployment/clamav must declare: $value" >&2
      return 1
    }
  done
  # Container and init container both hardened: two occurrences of each.
  for value in 'allowPrivilegeEscalation: false' 'readOnlyRootFilesystem: true'; do
    if [[ "$(grep -Fc "$value" <<<"$block")" -ne 2 ]]; then
      echo "error: clamav container and init container must both declare: $value" >&2
      return 1
    fi
  done

  # The database that gets copied must come from the build that scans with it.
  mapfile -t images < <(grep -Eo 'image: clamav/clamav:[^[:space:]]+' <<<"$block" | sort -u)
  if [[ "${#images[@]}" -ne 1 ]]; then
    echo "error: clamav init container and container must use one identical image" >&2
    printf '%s\n' "${images[@]}" >&2
    return 1
  fi
  [[ "${images[0]}" =~ @sha256:[a-f0-9]{64}$ ]] || {
    echo "error: the clamav image must be pinned by digest: ${images[0]}" >&2
    return 1
  }

  # Probes have to speak the protocol file-service speaks, on its port. The
  # image's own clamdcheck.sh expects a unix socket this config does not create
  # and reports failure against a daemon that is answering PONG on 3310.
  # Two semantic probes — startup and readiness. Liveness stays a plain TCP
  # check so it cannot restart the pod in the middle of a long scan.
  if [[ "$(grep -Fc -- '- clamdscan' <<<"$block")" -ne 2 ]] ||
    [[ "$(grep -Fc -- '- --ping' <<<"$block")" -ne 2 ]]; then
    echo "error: clamav startup and readiness must validate PING/PONG with clamdscan --ping" >&2
    return 1
  fi
  grep -Fq 'tcpSocket:' <<<"$block" || {
    echo "error: clamav liveness must be a plain tcpSocket check on the clamd port" >&2
    return 1
  }
  if grep -Eq 'clamdcheck\.sh|LocalSocket' <<<"$block"; then
    echo "error: clamav probes must not depend on clamdcheck.sh or a LocalSocket" >&2
    return 1
  fi

  # Every emptyDir bounded, and the container's ephemeral-storage ceiling at
  # least as large as the volumes it has to hold.
  if [[ "$(grep -Fc 'emptyDir:' <<<"$block")" -ne "$(grep -Fc 'sizeLimit:' <<<"$block")" ]]; then
    echo "error: every emptyDir in Deployment/clamav needs a sizeLimit" >&2
    return 1
  fi
  for value in 'sizeLimit: 2Gi' 'sizeLimit: 1Gi'; do
    grep -Fq "$value" <<<"$block" || {
      echo "error: Deployment/clamav must declare: $value" >&2
      return 1
    }
  done
  grep -Fq 'ephemeral-storage: 512Mi' <<<"$block" || {
    echo "error: Deployment/clamav must request ephemeral-storage" >&2
    return 1
  }
  # 4Gi >= 2Gi + 1Gi, so a full /tmp plus a full signature volume cannot exceed
  # what the container is allowed to consume before the kubelet evicts it.
  grep -Fq 'ephemeral-storage: 4Gi' <<<"$block" || {
    echo "error: limits.ephemeral-storage must cover the sum of the emptyDir sizeLimits" >&2
    return 1
  }
  for value in 'cpu: 500m' 'memory: 1536Mi' 'cpu: 1250m' 'memory: 3Gi'; do
    grep -Fq "$value" <<<"$block" || {
      echo "error: Deployment/clamav must declare the ratified resource: $value" >&2
      return 1
    }
  done

  # --- exposure --------------------------------------------------------------
  block="$(yaml_document "$application" Service clamav)"
  grep -Fq 'type: ClusterIP' <<<"$block" || {
    echo "error: Service/clamav must be ClusterIP" >&2
    return 1
  }
  if [[ "$(port_pairs "$block")" != "TCP/3310" ]]; then
    echo "error: Service/clamav must expose exactly TCP/3310" >&2
    return 1
  fi
  if grep -Eq 'NodePort|LoadBalancer|hostPort' "$application"; then
    echo "error: no workload in nchat-dev may use NodePort, LoadBalancer or hostPort" >&2
    return 1
  fi
  routing="$(yaml_documents_of_kind "$application" '|Ingress|IngressRoute|')"
  if grep -Eq 'clamav|3310' <<<"$routing"; then
    echo "error: no Ingress or IngressRoute may reference the scanner" >&2
    return 1
  fi

  block="$(yaml_document "$application" NetworkPolicy nchat-allow-clamav)"
  grep -Fq 'app.kubernetes.io/component: clamav' <<<"$block"
  grep -Fq 'app.kubernetes.io/component: file' <<<"$block"
  if [[ "$(grep -Fxc '    - podSelector:' <<<"$block")" -ne 1 ]]; then
    echo "error: nchat-allow-clamav must authorize exactly one origin pod selector" >&2
    return 1
  fi
  if [[ "$(port_pairs "$block")" != "TCP/3310" ]]; then
    echo "error: nchat-allow-clamav must allow only TCP/3310" >&2
    return 1
  fi
  # No egress policy for the scanner at all: freshclam is off, so it never
  # opens a connection, not even to DNS.
  if grep -Fq 'nchat-allow-clamav-egress' "$application"; then
    echo "error: clamav must have no egress policy while freshclam is disabled" >&2
    return 1
  fi
  # file resolves postgres, valkey, seaweedfs and clamav; upload-guard resolves
  # its upstream when nginx loads its config, so without DNS it never starts.
  block="$(yaml_document "$application" NetworkPolicy nchat-allow-dns-egress)"
  for expected in file upload-guard; do
    grep -Exq "[[:space:]]*- $expected" <<<"$block" || {
      echo "error: $expected must be granted DNS egress" >&2
      return 1
    }
  done
  # The scanner is not on that list, and that is the point.
  if grep -Exq '[[:space:]]*- clamav' <<<"$block"; then
    echo "error: clamav must not be granted DNS egress while freshclam is disabled" >&2
    return 1
  fi
}

# validate_clamav_absent proves the scanner reaches only the environment that
# asked for it.
validate_clamav_absent() {
  local overlay rendered
  for overlay in "$@"; do
    rendered="${overlay#*=}"
    if grep -Eq 'component: clamav|clamav/clamav' "$rendered"; then
      echo "error: ${overlay%%=*} must not render the ClamAV workload" >&2
      return 1
    fi
  done
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
  local application="$1" data="$2" migrations="$3" policy_block egress_block component image_ref
  local livekit_block coturn_block
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
  grep -Fq 'app.kubernetes.io/component: media' <<<"$policy_block"
  if [[ "$(grep -Fxc '  - ports:' <<<"$policy_block")" -ne 1 ]]; then
    echo "error: nchat-allow-livekit-api-egress must have exactly one egress rule" >&2
    return 1
  fi
  if [[ "$(grep -Fxc '    - ipBlock:' <<<"$policy_block")" -ne 1 ]]; then
    echo "error: nchat-allow-livekit-api-egress must have exactly one ipBlock destination" >&2
    return 1
  fi
  if [[ "$(grep -Fxc '    - podSelector:' <<<"$policy_block")" -ne 0 ]]; then
    echo "error: nchat-allow-livekit-api-egress must not select a destination pod" >&2
    return 1
  fi
  if [[ "$(grep -Fxc '    - namespaceSelector:' <<<"$policy_block")" -ne 0 ]]; then
    echo "error: nchat-allow-livekit-api-egress must not target a namespace" >&2
    return 1
  fi
  grep -Fq "cidr: $NCHAT_DEV_NODE_CIDR" <<<"$policy_block"
  if [[ "$(port_pairs "$policy_block")" != "TCP/$LIVEKIT_API_PORT" ]]; then
    echo "error: nchat-allow-livekit-api-egress must have exactly one port TCP/$LIVEKIT_API_PORT" >&2
    return 1
  fi
  if grep -Fq 'port: 5432' <<<"$policy_block"; then
    echo "error: LiveKit NetworkPolicy must not include PostgreSQL port" >&2
    return 1
  fi
  if grep -Fq 'app.kubernetes.io/component: postgres' <<<"$policy_block"; then
    echo "error: LiveKit NetworkPolicy must not select PostgreSQL" >&2
    return 1
  fi

  policy_block="$(yaml_document "$application" NetworkPolicy nchat-allow-dns-egress)"
  grep -Fq -- '- media' <<<"$policy_block"
  if [[ "$(grep -Fxc '  - ports:' <<<"$policy_block")" -ne 1 ]]; then
    echo "error: nchat-allow-dns-egress must have exactly one egress rule" >&2
    return 1
  fi
  if [[ "$(port_pairs "$policy_block" | LC_ALL=C sort)" != "$(printf '%s\n' 'TCP/53' 'UDP/53' | LC_ALL=C sort)" ]]; then
    echo "error: nchat-allow-dns-egress must expose exactly UDP/53 and TCP/53" >&2
    return 1
  fi
  if [[ "$(grep -Fxc '    - namespaceSelector:' <<<"$policy_block")" -ne 1 ]]; then
    echo "error: nchat-allow-dns-egress must target exactly one namespace" >&2
    return 1
  fi
  if [[ "$(grep -Fxc '    - podSelector:' <<<"$policy_block")" -ne 0 ]]; then
    echo "error: nchat-allow-dns-egress must not select a destination pod" >&2
    return 1
  fi
  if grep -Fq 'ipBlock:' <<<"$policy_block"; then
    echo "error: nchat-allow-dns-egress must not use an ipBlock destination" >&2
    return 1
  fi
  if grep -Fq 'namespaceSelector: {}' <<<"$policy_block"; then
    echo "error: nchat-allow-dns-egress must not use an empty namespaceSelector" >&2
    return 1
  fi
  grep -Fq 'kubernetes.io/metadata.name: kube-system' <<<"$policy_block"

  policy_block="$(yaml_document "$application" NetworkPolicy nchat-allow-postgres)"
  grep -Fq 'app.kubernetes.io/component: postgres' <<<"$policy_block"
  grep -Fq -- '- media' <<<"$policy_block"
  if [[ "$(grep -Fxc '  - from:' <<<"$policy_block")" -ne 1 ]]; then
    echo "error: nchat-allow-postgres must have exactly one ingress rule" >&2
    return 1
  fi
  if [[ "$(port_pairs "$policy_block")" != "TCP/5432" ]]; then
    echo "error: nchat-allow-postgres must allow only TCP/5432" >&2
    return 1
  fi
  if [[ "$(grep -Fxc '    - podSelector:' <<<"$policy_block")" -ne 1 ]]; then
    echo "error: nchat-allow-postgres must authorize exactly one origin pod selector" >&2
    return 1
  fi
  if grep -Fq 'ipBlock:' <<<"$policy_block"; then
    echo "error: nchat-allow-postgres must not use an ipBlock origin" >&2
    return 1
  fi
  if grep -Fq 'namespaceSelector: {}' <<<"$policy_block"; then
    echo "error: nchat-allow-postgres must not use an empty namespaceSelector" >&2
    return 1
  fi

  policy_block="$(yaml_document "$application" NetworkPolicy nchat-allow-media-postgres-egress)"
  grep -Fq 'app.kubernetes.io/component: media' <<<"$policy_block"
  grep -Fq 'app.kubernetes.io/component: postgres' <<<"$policy_block"
  if [[ "$(grep -Fxc '  - ports:' <<<"$policy_block")" -ne 1 ]]; then
    echo "error: nchat-allow-media-postgres-egress must have exactly one egress rule" >&2
    return 1
  fi
  if [[ "$(grep -Fxc '    - podSelector:' <<<"$policy_block")" -ne 1 ]]; then
    echo "error: nchat-allow-media-postgres-egress must have exactly one destination" >&2
    return 1
  fi
  if [[ "$(port_pairs "$policy_block")" != "TCP/5432" ]]; then
    echo "error: nchat-allow-media-postgres-egress must allow only TCP/5432" >&2
    return 1
  fi
  if grep -Fq 'ipBlock:' <<<"$policy_block"; then
    echo "error: nchat-allow-media-postgres-egress must not use an ipBlock destination" >&2
    return 1
  fi
  if grep -Fq 'namespaceSelector' <<<"$policy_block"; then
    echo "error: nchat-allow-media-postgres-egress must not target a namespace" >&2
    return 1
  fi
  [[ "$(grep -R -l 'name: LIVEKIT_API_URL' "$ROOT_DIR/infra/k8s/overlays/nchat-dev-server/patches" | wc -l)" -eq 1 ]]
  grep -q 'name: LIVEKIT_API_URL' "$ROOT_DIR/infra/k8s/overlays/nchat-dev-server/patches/media-service.yaml"

  livekit_block="$(yaml_document "$application" Deployment livekit)"
  coturn_block="$(yaml_document "$application" Deployment coturn)"

  [[ "$(grep -Fc 'enableServiceLinks: false' <<<"$livekit_block")" -eq 1 ]]
  [[ "$(grep -Fc 'enableServiceLinks: false' <<<"$coturn_block")" -eq 1 ]]
  [[ "$(grep -Fc -- '- NET_BIND_SERVICE' <<<"$coturn_block")" -eq 1 ]]

  if grep -Fq -- '- NET_BIND_SERVICE' <<<"$livekit_block"; then
    echo "error: LiveKit must not receive Coturn capabilities" >&2
    return 1
  fi

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
    nchat-allow-file-clamav-egress \
    nchat-allow-file-data-egress \
    nchat-allow-livekit-api-egress \
    nchat-allow-media-postgres-egress \
    nchat-allow-migrations-postgres-egress \
    nchat-allow-notification-postgres-egress \
    nchat-allow-seaweedfs-volume-egress \
    nchat-allow-upload-guard-file-egress \
    nchat-default-deny-egress | LC_ALL=C sort)" ]]
  for policy_block in \
    nchat-allow-dns-egress nchat-allow-traefik-http nchat-allow-postgres \
    nchat-allow-valkey nchat-allow-auth-postgres-egress nchat-allow-chat-data-egress \
    nchat-allow-notification-postgres-egress nchat-allow-migrations-postgres-egress \
    nchat-allow-livekit-api-egress nchat-allow-media-postgres-egress; do
    grep -q 'ports:' <<<"$(yaml_document "$application" NetworkPolicy "$policy_block")"
  done
  policy_block="$(yaml_document "$application" NetworkPolicy nchat-allow-traefik-http)"
  grep -Fq 'port: http' <<<"$policy_block"
  grep -Fq 'kubernetes.io/metadata.name: ingress-system' <<<"$policy_block"
  if grep -Fq 'kubernetes.io/metadata.name: kube-system' <<<"$policy_block"; then
    echo "error: Traefik NetworkPolicy must target ingress-system" >&2
    return 1
  fi
  if grep -q 'namespaceSelector: {}' "$application"; then return 1; fi
  if grep -Eq 'port: (8333|9333)' "$application"; then return 1; fi
  if grep -Eq 'name: s3|port: 8333|containerPort: 8333|[[:space:]]- -s3$' "$data"; then return 1; fi

  # The storage endpoint used to be banned outright, back when nothing here
  # spoke to the filer. Attachments do, so a blanket ban would only be evaded;
  # the replacement is stricter, not looser — one exact value, and the S3
  # gateway still absent because this deployment does not run it.
  if [[ "$(grep -Fxc '  SEAWEEDFS_FILER_URL: http://seaweedfs:8888' "$application")" -ne 1 ]]; then
    echo "error: nchat-config must set SEAWEEDFS_FILER_URL to http://seaweedfs:8888 exactly once" >&2
    return 1
  fi
  if grep -q 'SEAWEEDFS_S3_ENDPOINT' "$application"; then
    echo "error: nchat-dev must not configure an S3 endpoint" >&2
    return 1
  fi
  # `weed server` leaves the filer off unless told otherwise, which is how port
  # 8888 came to be declared by the Service while nothing listened on it. The
  # readiness probe has to be the filer as well: probing only the master is what
  # kept the gap invisible.
  policy_block="$(yaml_document "$data" StatefulSet seaweedfs)"
  if [[ "$(grep -Fc -- '- -filer=true' <<<"$policy_block")" -ne 1 ]]; then
    echo "error: the seaweedfs StatefulSet must start the filer (-filer=true)" >&2
    return 1
  fi
  if ! grep -A4 'readinessProbe:' <<<"$policy_block" | grep -Fq 'port: filer'; then
    echo "error: seaweedfs readiness must probe the filer port file-service consumes" >&2
    return 1
  fi

  # In all-in-one mode `weed server` also runs the volume server, and -ip
  # makes it announce itself to the master as seaweedfs:8080. The filer is
  # handed that address and dials it to persist each chunk, so 8080 has to
  # exist on the Service and be a named container port for targetPort to
  # resolve. Neither did, and every upload ended failure_code=storage_write
  # with `connection refused` (#483).
  if [[ "$(awk '
      function emit() { if (name != "") print name "/" port "/" target; name = port = target = "" }
      /^  - name: / { emit(); name = $NF; next }
      /^    port: / { port = $NF; next }
      /^    targetPort: / { target = $NF; next }
      END { emit() }
    ' <<<"$(yaml_document "$data" Service seaweedfs)" | LC_ALL=C sort)" != "$(printf '%s\n' \
    filer/8888/filer master/9333/master volume/8080/volume | LC_ALL=C sort)" ]]; then
    echo "error: the seaweedfs Service must expose master/9333, volume/8080 and filer/8888" >&2
    return 1
  fi
  if ! grep -A1 -Fx '        - containerPort: 8080' <<<"$policy_block" | grep -Fxq '          name: volume'; then
    echo "error: the seaweedfs container must declare containerPort 8080 named volume" >&2
    return 1
  fi

  # Who may reach which SeaweedFS port, stated as flows rather than as ports.
  # file-service talks to the filer and nothing else; the filer->volume hop is
  # the pod reaching itself through the Service, so it needs both halves —
  # ingress, and egress because nchat-default-deny-egress covers every pod.
  policy_block="$(yaml_document "$application" NetworkPolicy nchat-allow-seaweedfs)"
  if [[ "$(network_policy_flows "$policy_block")" != "$(printf '%s\n' \
    'ingress file TCP/8888' 'ingress seaweedfs TCP/8080' | LC_ALL=C sort)" ]]; then
    echo "error: nchat-allow-seaweedfs must admit exactly file->8888 and seaweedfs->8080" >&2
    return 1
  fi
  egress_block="$(yaml_document "$application" NetworkPolicy nchat-allow-seaweedfs-volume-egress)"
  if [[ "$(network_policy_flows "$egress_block")" != 'egress seaweedfs TCP/8080' ]]; then
    echo "error: nchat-allow-seaweedfs-volume-egress must allow exactly seaweedfs->seaweedfs:8080/TCP" >&2
    return 1
  fi
  # Pod-to-pod by label on both sides. A CIDR or a namespace would make the
  # volume server reachable from outside the component it belongs to.
  if grep -Eq 'ipBlock:|namespaceSelector:' <<<"$policy_block$egress_block"; then
    echo "error: the SeaweedFS policies must select peers by podSelector only" >&2
    return 1
  fi
  # And the negative that matters most: across every NetworkPolicy in the
  # overlay, 8080 is reachable by SeaweedFS itself and by nobody else.
  if [[ "$(network_policy_flows "$(yaml_documents_of_kind "$application" '|NetworkPolicy|')" | grep -F '/8080')" \
    != "$(printf '%s\n' 'egress seaweedfs TCP/8080' 'ingress seaweedfs TCP/8080')" ]]; then
    echo "error: only seaweedfs may reach the volume server on 8080" >&2
    return 1
  fi

  # The attachment stack's own configuration. FILE_MALWARE_SCAN_REQUIRED is the
  # one that must never drift: APP_ENV=nchat-dev sits inside the allowlist that
  # would let the service accept false, so the manifest is what keeps the gate
  # on.
  if [[ "$(grep -Fxc '  FILE_MALWARE_SCAN_REQUIRED: "true"' "$application")" -ne 1 ]]; then
    echo "error: FILE_MALWARE_SCAN_REQUIRED must be exactly \"true\" in nchat-config" >&2
    return 1
  fi
  if [[ "$(grep -Fxc '  FILE_UPLOADS_ENABLED: "true"' "$application")" -ne 1 ]]; then
    echo "error: FILE_UPLOADS_ENABLED must be stated explicitly in nchat-config" >&2
    return 1
  fi
  if [[ "$(grep -Fxc '  FILE_MALWARE_SCANNER_ADDRESS: clamav:3310' "$application")" -ne 1 ]]; then
    echo "error: FILE_MALWARE_SCANNER_ADDRESS must be the host:port dial target clamav:3310" >&2
    return 1
  fi

  # WS_INBOUND_BURST=60 must be declared exactly once in nchat-config so the
  # web client's bootstrap burst (1 call.sync + 12 subscribe messages) is not
  # closed with 1008 (issue #455). The sustained rate must stay untouched.
  # Kustomize's YAML map merge silently collapses a duplicate key in the
  # source, so the duplication itself must be caught in the source overlay
  # file, not only in the rendered/merged output.
  if [[ "$(grep -c '^  WS_INBOUND_BURST:' "$ROOT_DIR/infra/k8s/overlays/nchat-dev-server/configmap-patch.yaml")" -ne 1 ]]; then
    echo "error: nchat-dev-server configmap-patch.yaml must declare WS_INBOUND_BURST exactly once" >&2
    return 1
  fi
  if [[ "$(grep -Fxc '  WS_INBOUND_BURST: "60"' "$application")" -ne 1 ]]; then
    echo "error: nchat-config must declare WS_INBOUND_BURST: \"60\" exactly once" >&2
    return 1
  fi
  if grep -q 'WS_INBOUND_MESSAGES_PER_MINUTE' "$application"; then
    echo "error: nchat-dev-server must not override WS_INBOUND_MESSAGES_PER_MINUTE" >&2
    return 1
  fi

  # Two references for clamav — the daemon and the init container that seeds its
  # signatures — so the expected count is 6 + 2.
  mapfile -t external_image_refs < <(
    grep -hE '^ +image: (postgres|valkey/valkey|chrislusf/seaweedfs|livekit/livekit-server|coturn/coturn|clamav/clamav):' \
      "$application" "$data"
  )
  for image_ref in "${external_image_refs[@]}"; do
    image_ref="${image_ref#*image: }"
    [[ "$image_ref" =~ @sha256:[a-f0-9]{64}$ ]]
  done
  [[ "${#external_image_refs[@]}" -eq 8 ]]

  for component in auth chat file notification admin search media web clamav upload-guard; do
    grep -q "app.kubernetes.io/component: $component" "$application"
  done

  validate_clamav "$application"
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
  validate_clamav_absent \
    "infra/k8s/base=${rendered_by_overlay[infra/k8s/base]}" \
    "infra/k8s/overlays/k3s-dev=${rendered_by_overlay[infra/k8s/overlays/k3s-dev]}" \
    "infra/k8s/overlays/k3s-staging=${rendered_by_overlay[infra/k8s/overlays/k3s-staging]}"
fi

echo "K8s manifests CI check passed."
