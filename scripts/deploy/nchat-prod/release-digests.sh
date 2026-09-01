#!/usr/bin/env bash
# Materialise a deploy's digest artifacts from a sealed release manifest.
#
# The pipeline names the run that built a release, and a named run is not yet a
# proven one: an artifact downloaded from it is only evidence if something binds
# it to the commit being promoted. The manifest is that binding -- it is sealed
# with a SHA-256 and it names its own source_sha -- so the digests the deploy
# pins are taken from it and from nothing else.
#
# It is also the artifact that outlives the release cycle: release-manifest is
# kept for 90 days, the per-image digest-*.txt for 7. Deriving the digest files
# here rather than downloading them means a release stays deployable for as long
# as its identity is retained.
#
# Usage: release-digests.sh <manifest-dir> <artifacts-dir>
#   NCHAT_PROD_RELEASE_SHA  the commit being promoted; the manifest must name it
#
# Fail-closed throughout: a broken seal, a manifest that fails the release
# contract, a mismatched source SHA and a malformed digest are all refusals, and
# none of them leaves a usable artifacts directory behind.
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
# Sourcing brings validate_release_manifest and the canonical image inventory,
# so the set of digests written here is the same set the manifest is checked
# against and the same one the build matrix derives from.
# shellcheck source=scripts/deploy/nchat-prod/release-manifest.sh
source "$SCRIPT_DIR/release-manifest.sh"
# For NCHAT_PROD_RELEASE_ID_FILE: the name the deploy reads the identity from is
# defined once, where the deploy's other contracts live.
# shellcheck source=scripts/deploy/nchat-prod/lib.sh
source "$SCRIPT_DIR/lib.sh"

release_digests_fail() {
  echo "release digests: $*" >&2
}

# `sha256sum -c` from inside the directory, because the .sha256 names the
# manifest by its published bare name.
verify_manifest_seal() {
  local manifest_dir="$1"
  (cd "$manifest_dir" && sha256sum -c "$RELEASE_MANIFEST_SHA256" >/dev/null 2>&1)
}

# Writes the release identity the deploy will stamp on every candidate workload.
# It is the seal of the manifest these digests came from, so a rebuild of the
# same commit -- different digests, same source SHA -- produces a different one.
write_release_id() {
  local manifest_dir="$1" artifacts_dir="$2" release_id
  release_id="$(release_manifest_id "$manifest_dir")" || {
    release_digests_fail "the manifest seal does not carry a usable release identity"
    return 1
  }
  printf '%s' "$release_id" >"$artifacts_dir/$NCHAT_PROD_RELEASE_ID_FILE"
}

# The manifest names the commit its images were built from. Without this the
# workflow would deploy whichever release the named run happened to seal.
manifest_matches_sha() {
  local manifest="$1" release_sha="$2"
  [[ "$(jq -r '.source_sha' "$manifest")" == "$release_sha" ]]
}

write_release_digests() {
  local manifest="$1" artifacts_dir="$2" image digest
  for image in "${NCHAT_DEV_IMAGES[@]}"; do
    digest="$(jq -r --arg image "$image" '.images[$image] // empty' "$manifest")"
    [[ "$digest" =~ ^sha256:[a-f0-9]{64}$ ]] || {
      release_digests_fail "the manifest carries no usable digest for image '$image'"
      return 1
    }
    printf '%s' "$digest" >"$artifacts_dir/digest-$image.txt"
  done
}

release_digests_valid() {
  local manifest_dir="$1" release_sha="$2" manifest="$1/$RELEASE_MANIFEST_JSON"
  if ! validate_commit_sha "$release_sha"; then
    release_digests_fail "NCHAT_PROD_RELEASE_SHA must be a full 40-character lowercase commit SHA"
    return 1
  fi
  if ! verify_manifest_seal "$manifest_dir"; then
    release_digests_fail "the manifest in '$manifest_dir' does not match its SHA-256 seal"
    return 1
  fi
  if ! validate_release_manifest "$manifest"; then
    release_digests_fail "the manifest does not satisfy the release contract"
    return 1
  fi
  if ! manifest_matches_sha "$manifest" "$release_sha"; then
    release_digests_fail "the manifest seals release $(jq -r '.source_sha' "$manifest"), not $release_sha"
    return 1
  fi
}

# Removes the digest files this script owns, by name, one per image in the
# canonical inventory. Nothing else in the directory is touched: the path came
# from an argument, and `rm -rf` on it would make a mistyped second argument
# destructive rather than merely wrong.
clear_release_digests() {
  local artifacts_dir="$1" image
  for image in "${NCHAT_DEV_IMAGES[@]}"; do
    rm -f -- "$artifacts_dir/digest-$image.txt"
  done
  rm -f -- "$artifacts_dir/$NCHAT_PROD_RELEASE_ID_FILE"
}

# A refused release must leave nothing a later step could mistake for digests.
#
# The order here is the whole point. The previous attempt's digests are cleared
# before the first gate that can refuse, so a manifest that fails its seal, its
# contract or its source SHA cannot leave an earlier release's digests sitting
# in the output directory looking like the result of this run. Clearing them
# afterwards would be too late: the refusal returns before reaching it.
emit_release_digests() {
  local manifest_dir="$1" artifacts_dir="$2" release_sha="$3"
  mkdir -p "$artifacts_dir"
  clear_release_digests "$artifacts_dir"
  release_digests_valid "$manifest_dir" "$release_sha" || return 1
  # A half-written set is as misleading as a stale one, so a failure part-way
  # through takes the files it did manage to write with it.
  if ! write_release_digests "$manifest_dir/$RELEASE_MANIFEST_JSON" "$artifacts_dir"; then
    clear_release_digests "$artifacts_dir"
    return 1
  fi
  if ! write_release_id "$manifest_dir" "$artifacts_dir"; then
    clear_release_digests "$artifacts_dir"
    return 1
  fi
}

main() {
  if [[ "$#" -ne 2 ]]; then
    release_digests_fail "usage: release-digests.sh <manifest-dir> <artifacts-dir>"
    return 1
  fi
  [[ -n "$2" && "$2" != / ]] || {
    release_digests_fail "the artifacts directory must be a non-empty path other than /"
    return 1
  }
  emit_release_digests "$1" "$2" "${NCHAT_PROD_RELEASE_SHA:-}"
  printf 'Pinned %s image digests from the sealed manifest of %s (release %s).\n' \
    "${#NCHAT_DEV_IMAGES[@]}" "$NCHAT_PROD_RELEASE_SHA" "$(<"$2/$NCHAT_PROD_RELEASE_ID_FILE")"
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi
