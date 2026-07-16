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
      web)
        [[ "$image" == web && "$deployment" =~ ^[a-z0-9-]+$ ]] || return 1
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
  [[ "${#NCHAT_DEV_RUNTIME_IMAGES[@]}" -eq 8 ]] || return 1
  [[ "${#NCHAT_DEV_IMAGES[@]}" -eq 9 ]] || return 1
  [[ "${#NCHAT_DEV_APPLICATION_DEPLOYMENTS[@]}" -eq 8 ]] || return 1
}

load_nchat_dev_image_inventory "$NCHAT_DEV_IMAGE_INVENTORY"

validate_commit_sha() {
  [[ "${1:-}" =~ ^[a-f0-9]{40}$ ]]
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
  if grep -En 'sha-placeholder|image: .*:(latest|main|master)(@|$)' "$output"; then
    echo "Rendered manifest contains a forbidden mutable image reference." >&2
    return 1
  fi
}
