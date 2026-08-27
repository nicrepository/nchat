#!/usr/bin/env bash
# Structural validation of the production stateful overlay.
#
# The Blue/Green check next door asserts that the two release slots differ only
# by their images. This one asserts the complementary property: that the layer
# underneath them exists exactly once, is bound to production's own retained
# storage, and shares nothing with nchat-dev. Those are the failures that cannot
# be undone by rolling a release back.
#
# It renders the overlay the way scripts/deploy/nchat-prod/stateful.sh applies
# it -- on its own, because that is the unit that gets applied -- and reads the
# result through scripts/ci/prod_blue_green_query.py rather than grepping it, so
# the assertions are about objects and not about strings in a file.
set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
STATEFUL_OVERLAY="${NCHAT_PROD_STATEFUL_OVERLAY:-$ROOT_DIR/infra/k8s/overlays/k3s-prod/stateful}"
QUERY="$ROOT_DIR/scripts/ci/prod_blue_green_query.py"
RENDER_DIR="$(mktemp -d "${TMPDIR:-/tmp}/nchat-prod-stateful-check.XXXXXX")"
trap 'rm -rf "$RENDER_DIR"' EXIT

ERRORS=0
ALL="$RENDER_DIR/stateful.yaml"

# Exactly one of each, named as the rest of the deployment already addresses it.
# All three own data, so all three are StatefulSets.
SINGLETON_STATEFULSETS=(postgres valkey seaweedfs)
# The media plane is NOT here. LiveKit runs as one shared deployment on AWS that
# nchat-dev already uses and nchat-prod uses too; this cluster runs none.
#
# These names exist in this file only so the gate can prove their absence.
# coturn and turnserver are on the list even though the AWS deployment has not
# been shown to run either: whatever it turns out to use for TURN, the rule that
# production must not grow a local one holds independently -- a TURN server here
# would take host ports on srv-apps-01, and the first symptom would be
# nchat-dev's calls breaking.
FORBIDDEN_WORKLOADS=(livekit coturn turnserver)
# name:path. The paths are production's own, under a directory nchat-dev does
# not use, and are the ones the runbook tells an operator to create.
VOLUMES=(
  "nchat-prod-postgres:/mnt/hdd-geral/k3s/nchat-prod/postgres"
  "nchat-prod-valkey:/mnt/hdd-geral/k3s/nchat-prod/valkey"
  "nchat-prod-seaweedfs:/mnt/hdd-geral/k3s/nchat-prod/seaweedfs"
  "nchat-prod-auth-avatars:/mnt/hdd-geral/k3s/nchat-prod/auth-avatars"
)
STORAGE_CLASS=local-hdd-geral
STORAGE_NODE=srv-apps-01
# The Services this layer publishes, as the rest of production addresses them.
# scripts/ci/prod-blue-green-check.sh holds the same list so that a ConfigMap
# endpoint naming one of them is not reported as dangling; asserting it against
# the real render here is what keeps that list honest.
PUBLISHED_SERVICES=(postgres valkey seaweedfs seaweedfs-filer)

fail() { echo "  [FAIL] $*" >&2; ERRORS=$((ERRORS + 1)); }
ok() { echo "  [OK]   $*"; }
query() { python3 "$QUERY" "$ALL" "$1"; }

render() {
  if command -v kustomize >/dev/null 2>&1; then
    kustomize build "$STATEFUL_OVERLAY" >"$ALL" 2>"$ALL.warnings"
  else
    KUBECONFIG=/dev/null kubectl kustomize "$STATEFUL_OVERLAY" >"$ALL" 2>"$ALL.warnings"
  fi
  [[ -s "$ALL" ]] || return 1
  [[ ! -s "$ALL.warnings" ]] || { cat "$ALL.warnings" >&2; return 1; }
}

check_rendering() {
  echo "--- rendering ---"
  render && ok "renders: stateful" || fail "does not render: stateful"
}

count_of() { query "$1|name" | grep -Fxc "$2" || true; }

check_one_of_each() {
  local name
  echo "--- one instance of each shared dependency ---"
  for name in "${SINGLETON_STATEFULSETS[@]}"; do
    [[ "$(count_of StatefulSet "$name")" -eq 1 ]] ||
      fail "expected exactly one StatefulSet named '$name'"
  done
  ok "postgres, valkey and seaweedfs each exist exactly once"
  # base/configmap.yaml sets SEAWEEDFS_FILER_URL to http://seaweedfs-filer:8888
  # and bootstrap.sh checks for that Service by name. Without it production
  # renders a file-service that cannot reach storage, and the failure appears
  # only at runtime as a readiness probe that never passes.
  [[ "$(count_of Service seaweedfs-filer)" -eq 1 ]] ||
    fail "no 'seaweedfs-filer' Service; production's nchat-config addresses the filer by that name"
  ok "the filer is published under the name production's configuration uses"
}

# Blue and Green share this layer. A slot label anywhere in it -- even one added
# by an overlay above -- would mean two of something, and two databases is a
# data loss rather than a deployment mistake.
check_no_release_slot() {
  local name
  echo "--- no dependency is duplicated per slot ---"
  if grep -q 'nchat.io/release-slot' "$ALL"; then
    fail "the stateful layer carries a release-slot label; Blue and Green must share one of each"
  fi
  while IFS= read -r name; do
    [[ "$name" != *-blue && "$name" != *-green ]] ||
      fail "'$name' is named for a release slot; this layer is shared"
  done < <(cat <(query 'StatefulSet|name') <(query 'Deployment|name') <(query 'Service|name'))
  ok "nothing here belongs to a slot"
}

check_published_services() {
  local name
  echo "--- published services ---"
  for name in "${PUBLISHED_SERVICES[@]}"; do
    [[ "$(count_of Service "$name")" -eq 1 ]] ||
      fail "no Service '$name'; production's configuration addresses the stateful layer by that name"
  done
  ok "the layer publishes exactly the names production addresses it by"
}

# The architecture rule this layer is most likely to lose, stated as a test.
#
# The media plane is one shared LiveKit deployment on AWS, used by nchat-dev and
# by both production slots. Provisioning a second one here would not give
# production its own: a media server needs host ports, srv-apps-01 has one set
# of them, and nchat-dev is already using them -- so the first symptom of
# "adding LiveKit to production" is development's calls failing on the next
# restart. It would also put WebRTC behind this deployment's Traefik, which has
# no reason to carry it.
#
# hostNetwork is checked in the same breath because it is the mechanism that
# does the damage, and because nothing that legitimately belongs in this layer
# -- a database, a cache, an object store -- has any use for it.
check_no_media_plane() {
  local name workload published
  echo "--- the media plane is external ---"
  for name in "${FORBIDDEN_WORKLOADS[@]}"; do
    for workload in Deployment StatefulSet DaemonSet Service; do
      [[ "$(count_of "$workload" "$name")" -eq 0 ]] ||
        fail "the stateful layer renders $workload/$name; the media plane is external AWS infrastructure and must not be provisioned here"
    done
  done
  if grep -q 'hostNetwork: true' "$ALL"; then
    fail "the stateful layer claims the node's network namespace; no data workload needs hostNetwork"
  fi
  # A Service for something this layer does not run is the same mistake at one
  # remove: it would publish an in-cluster name for a host that is not here.
  while IFS= read -r published; do
    for name in "${FORBIDDEN_WORKLOADS[@]}"; do
      [[ "$published" != *"$name"* ]] ||
        fail "Service '$published' names the media plane; nchat-prod addresses LiveKit by its external URL, not by a Service"
    done
  done < <(query 'Service|name')
  ok "no media-plane or host-networked workload is provisioned here"
}

check_namespace() {
  local name
  echo "--- namespace ---"
  while IFS= read -r name; do
    [[ "$name" == nchat-prod ]] ||
      fail "resource rendered into namespace '$name', expected nchat-prod"
  done < <(query 'namespaces')
  ok "every namespaced resource renders into nchat-prod"
}

# The single most dangerous mistake available here: a production volume pointed
# at a directory nchat-dev is already writing to. Both would run, and the
# corruption would surface days later.
check_no_dev_paths() {
  echo "--- development isolation ---"
  if grep -n 'nchat-dev' "$ALL" >&2; then
    fail "the production stateful layer references nchat-dev"
  else
    ok "no path, name or Secret reference mentions nchat-dev"
  fi
}

check_volume() {
  local name="$1" expected_path="$2" value
  value="$(query "PersistentVolume|$name|spec.local.path")"
  [[ "$value" == "$expected_path" ]] ||
    fail "pv/$name points at '${value:-nothing}', expected '$expected_path'"
  value="$(query "PersistentVolume|$name|spec.persistentVolumeReclaimPolicy")"
  [[ "$value" == Retain ]] ||
    fail "pv/$name has reclaimPolicy '${value:-unset}'; production data must be retained, not collected"
  value="$(query "PersistentVolume|$name|spec.storageClassName")"
  [[ "$value" == "$STORAGE_CLASS" ]] ||
    fail "pv/$name uses storageClass '${value:-unset}', expected $STORAGE_CLASS"
  value="$(query "PersistentVolume|$name|node-hosts")"
  [[ "$value" == "$STORAGE_NODE" ]] ||
    fail "pv/$name admits nodes '${value:-none}'; a local volume must be pinned to $STORAGE_NODE"
}

check_storage() {
  local entry
  echo "--- storage ---"
  for entry in "${VOLUMES[@]}"; do
    check_volume "${entry%%:*}" "${entry#*:}"
  done
  ok "every volume is retained, on $STORAGE_CLASS, pinned to $STORAGE_NODE, under its own path"
}

rendered_images() {
  grep -E '^ +image: ' "$ALL" | awk '{print $2}'
}

# A tag can be moved; a digest cannot. These three processes own the data, so
# "the same bytes as last time" matters more here than anywhere else in the
# deployment.
check_images() {
  local image count=0
  echo "--- images ---"
  while IFS= read -r image; do
    count=$((count + 1))
    [[ "$image" == *@sha256:* ]] ||
      fail "image '$image' is not pinned by digest"
  done < <(rendered_images)
  [[ "$count" -ge 3 ]] || fail "expected at least three stateful images, found $count"
  ok "every stateful image is pinned by digest"
}

# This overlay names Secrets and never contains one. A literal here would be a
# production credential in Git, which no rotation procedure can take back.
check_no_plaintext_secrets() {
  echo "--- secrets ---"
  if [[ -n "$(query 'Secret|name')" ]]; then
    fail "the stateful overlay renders a Secret; production credentials must be sealed, not committed"
  fi
  if grep -Eq '^ *(stringData|data):' <<<"$(grep -A2 '^kind: Secret' "$ALL")"; then
    fail "a Secret body was rendered into the stateful overlay"
  fi
  ok "the overlay references Secrets by name and contains none"
}

main() {
  echo "=== production stateful check ==="
  echo
  check_rendering
  [[ "$ERRORS" -eq 0 ]] || { echo "stateful overlay does not render; aborting." >&2; exit 1; }
  echo
  check_one_of_each; echo
  check_published_services; echo
  check_no_media_plane; echo
  check_no_release_slot; echo
  check_namespace; echo
  check_no_dev_paths; echo
  check_storage; echo
  check_images; echo
  check_no_plaintext_secrets; echo
  [[ "$ERRORS" -eq 0 ]] || { echo "production stateful check failed with $ERRORS error(s)." >&2; exit 1; }
  echo "production stateful check passed."
}

# Sourcing defines the checks without running them, so
# scripts/ci/test_prod_stateful_check.sh can drive each assertion against a
# doctored overlay and prove it actually fails. Same pattern as
# scripts/ci/prod-blue-green-check.sh.
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  main "$@"
fi
