#!/usr/bin/env bash
# This file is sourced by deploy.sh.
# The caller must enable: set -Eeuo pipefail.

LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
# shellcheck source=scripts/deploy/nchat-dev/topology.sh
source "$LIB_DIR/topology.sh"
NCHAT_DEV_IMAGE_INVENTORY="${NCHAT_DEV_IMAGE_INVENTORY:-$LIB_DIR/images.txt}"
NCHAT_DEV_GO_SERVICES=()
NCHAT_DEV_RUNTIME_IMAGES=()
NCHAT_DEV_IMAGES=()
NCHAT_DEV_APPLICATION_DEPLOYMENTS=()

load_nchat_dev_image_inventory() {
  local inventory="$1" kind image deployment extra
  local -A seen_images=() seen_deployments=()

  NCHAT_DEV_GO_SERVICES=()
  NCHAT_DEV_RUNTIME_IMAGES=()
  NCHAT_DEV_IMAGES=()
  NCHAT_DEV_APPLICATION_DEPLOYMENTS=()
  [[ -f "$inventory" && ! -L "$inventory" ]] || return 1
  while IFS=' ' read -r kind image deployment extra || [[ -n "$kind$image$deployment$extra" ]]; do
    [[ -n "$kind" && -n "$image" && -n "$deployment" && -z "$extra" ]] || return 1
    [[ "$image" =~ ^[a-z0-9-]+$ ]] || return 1
    [[ -z "${seen_images[$image]:-}" ]] || return 1
    seen_images["$image"]=1
    case "$kind" in
      go)
        [[ "$deployment" == "$image" ]] || return 1
        NCHAT_DEV_GO_SERVICES+=("$image")
        NCHAT_DEV_RUNTIME_IMAGES+=("$image")
        NCHAT_DEV_APPLICATION_DEPLOYMENTS+=("$deployment")
        ;;
      # Static frontend bundles. Each one is its own image and its own
      # Deployment: the chat app and the administrative console (issue #578) do
      # not share a bundle, a CSP or a release cadence, so
      # they must not share an image either. The Dockerfile is derived from the
      # image name in .github/workflows/images.yml.
      web)
        [[ "$image" =~ ^[a-z0-9-]+$ && "$deployment" =~ ^[a-z0-9-]+$ ]] || return 1
        NCHAT_DEV_RUNTIME_IMAGES+=("$image")
        NCHAT_DEV_APPLICATION_DEPLOYMENTS+=("$deployment")
        ;;
      migration)
        [[ "$image" == migrations && "$deployment" == - ]] || return 1
        ;;
      *) return 1 ;;
    esac
    if [[ "$deployment" != - ]]; then
      [[ -z "${seen_deployments[$deployment]:-}" ]] || return 1
      seen_deployments["$deployment"]=1
    fi
    NCHAT_DEV_IMAGES+=("$image")
  done <"$inventory"

  [[ "${#NCHAT_DEV_GO_SERVICES[@]}" -eq 7 ]] || return 1
  [[ "${#NCHAT_DEV_RUNTIME_IMAGES[@]}" -eq 9 ]] || return 1
  [[ "${#NCHAT_DEV_IMAGES[@]}" -eq 10 ]] || return 1
  [[ "${#NCHAT_DEV_APPLICATION_DEPLOYMENTS[@]}" -eq 9 ]] || return 1
}

load_nchat_dev_image_inventory "$NCHAT_DEV_IMAGE_INVENTORY"

validate_commit_sha() {
  [[ "${1:-}" =~ ^[a-f0-9]{40}$ ]]
}

# Fails fast, before any kustomize build or kubectl apply, if the active
# kubectl identity lacks a required verb on a namespaced resource. Never
# passes --as: it must reflect the credential the workflow actually runs as,
# not an impersonated one, so the check can't diverge from the real apply.
require_kubernetes_permission() {
  local verb="$1" resource="$2" namespace="$3" answer status
  if answer="$(kubectl auth can-i "$verb" "$resource" -n "$namespace" 2>/dev/null)"; then
    status=0
  else
    status=$?
  fi
  if [[ "$status" -ne 0 || "$answer" != yes ]]; then
    echo "Kubernetes permission preflight failed: $verb $resource in namespace $namespace." >&2
    return 1
  fi
}

validate_digest_artifacts() {
  local artifacts_dir="$1" image file digest index
  local expected=()
  local actual=()

  for image in "${NCHAT_DEV_IMAGES[@]}"; do
    expected+=("digest-$image.txt")
  done
  mapfile -t expected < <(printf '%s\n' "${expected[@]}" | LC_ALL=C sort)
  mapfile -t actual < <(find "$artifacts_dir" -mindepth 1 -maxdepth 1 -printf '%f\n' | LC_ALL=C sort)
  [[ "${#actual[@]}" -eq "${#expected[@]}" ]] || return 1
  for index in "${!expected[@]}"; do
    [[ "${actual[$index]}" == "${expected[$index]}" ]] || return 1
    file="$artifacts_dir/${expected[$index]}"
    [[ -f "$file" && ! -L "$file" ]] || return 1
    [[ "$(LC_ALL=C wc -c <"$file")" -eq 71 ]] || return 1
    digest="$(<"$file")"
    [[ "$digest" =~ ^sha256:[a-f0-9]{64}$ ]] || return 1
  done
}

prepare_deploy_tree() {
  local root_dir="$1" temporary_root="$2"
  mkdir -p "$temporary_root"
  cp -a "$root_dir/infra/k8s" "$temporary_root/infra-k8s"
  rm -f "$temporary_root/infra-k8s/overlays/nchat-dev-server/topology.env"
  prepare_nchat_dev_topology "$root_dir" \
    "$temporary_root/infra-k8s/overlays/nchat-dev-server/topology.env"
}

cleanup_deploy_tree() {
  local temporary_root="$1"
  [[ -n "$temporary_root" && "$temporary_root" != / ]] || return 1
  rm -rf "$temporary_root"
}

set_digest_image() {
  local overlay="$1" image="$2" digest_file="$3" digest
  digest="$(<"$digest_file")"
  [[ "$digest" =~ ^sha256:[a-f0-9]{64}$ ]] || return 1
  (cd "$overlay" && kustomize edit set image "$image=$image@$digest")
}

validate_rendered_overlay() {
  local overlay="$1" output="$2"
  kustomize build "$overlay" >"$output"
  [[ -s "$output" ]] || return 1
  validate_rendered_placeholders "$output"
}

rendered_overlay_placeholder_matches() {
  grep -Eno 'REPLACE_ME_[A-Z0-9_]+' "$1"
}

format_rendered_placeholder_matches() {
  local output="$1" matches="$2" line token
  while IFS=: read -r line token; do
    [[ "$line" =~ ^[0-9]+$ && "$token" =~ ^REPLACE_ME_[A-Z0-9_]+$ ]] || return 1
    printf '%s:%s:%s\n' "$output" "$line" "$token" || return 1
  done <<<"$matches"
}

validate_rendered_placeholders() {
  local output="$1" mutable_matches placeholder_matches formatted status
  if mutable_matches="$(grep -En 'sha-placeholder|image: .*:(latest|main|master)(@|$)' "$output" 2>/dev/null)"; then
    status=0
  else
    status=$?
  fi
  case "$status" in
    0) echo "Rendered manifest contains a forbidden mutable image reference." >&2; return 1 ;;
    1) ;;
    *) echo "Unable to inspect rendered manifest: $output." >&2; return 1 ;;
  esac
  # Fail fast on any unresolved topology placeholder before it reaches
  # kubectl apply. Report only the file and matched token, never the full
  # line, since a line could belong to a Secret/SealedSecret resource.
  if placeholder_matches="$(rendered_overlay_placeholder_matches "$output" 2>/dev/null)"; then
    status=0
  else
    status=$?
  fi
  case "$status" in
    1) return 0 ;;
    0) ;;
    *) echo "Unable to inspect rendered manifest: $output." >&2; return 1 ;;
  esac
  if ! formatted="$(format_rendered_placeholder_matches "$output" "$placeholder_matches")"; then
    echo "Unable to format unresolved placeholders for rendered manifest: $output." >&2
    return 1
  fi
  if [[ -n "$formatted" ]]; then
    printf '%s\n' "$formatted" >&2
    echo "Rendered manifest $output contains unresolved REPLACE_ME_* placeholders (see tokens above)." >&2
    return 1
  fi
  echo "Unable to format unresolved placeholders for rendered manifest: $output." >&2
  return 1
}
