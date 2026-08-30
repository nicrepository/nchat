#!/usr/bin/env bash
# CICD-04: materialise the identity of a production release as one auditable file.
#
# Before this, a release was a source SHA plus eleven independent `digest-*.txt`
# artifacts, which is an invitation to hand-compose a release out of a code
# identity and digests that never belonged to the same build. This writes the
# whole identity once, from the digests of the current workflow run only, and
# seals it with a SHA-256 that `sha256sum -c` verifies.
#
# The image inventory is not repeated here: it comes from the same
# scripts/deploy/nchat-dev/images.txt the build matrix and the deploys already
# derive from, so an image added there cannot silently escape the manifest.
#
# Usage: release-manifest.sh <digest-artifacts-dir> <output-dir>
#   NCHAT_RELEASE_SOURCE_SHA  commit the images were built from (github.sha)
#   NCHAT_RELEASE_RUN_ID      workflow run that built them (github.run_id)
#
# Every gate below is fail-closed: no fallback digest, no previous artifact, no
# warning that ends in success.
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../../.." && pwd -P)"
# shellcheck source=scripts/deploy/nchat-dev/lib.sh
source "$ROOT_DIR/scripts/deploy/nchat-dev/lib.sh"

RELEASE_MANIFEST_JSON=release-manifest.json
RELEASE_MANIFEST_SHA256=release-manifest.sha256

release_manifest_fail() {
  echo "release manifest: $*" >&2
}

# The digest map of this run, keyed by image name. `$ARGS.named` is what keeps
# names and digests data: nothing is concatenated into the jq program, so a
# hostile digest or image name cannot become jq syntax.
release_manifest_images_json() {
  local artifacts_dir="$1" image
  local args=()
  for image in "${NCHAT_DEV_IMAGES[@]}"; do
    args+=(--arg "$image" "$(<"$artifacts_dir/digest-$image.txt")")
  done
  jq -n "${args[@]}" '$ARGS.named'
}

# -S sorts every key, so the same inputs always serialise to the same bytes.
release_manifest_document() {
  local images="$1" source_sha="$2" run_id="$3" created_at="$4"
  jq -n -S \
    --argjson images "$images" \
    --arg source_sha "$source_sha" \
    --argjson workflow_run_id "$run_id" \
    --arg created_at "$created_at" \
    '{schema_version: 1, source_sha: $source_sha,
      workflow_run_id: $workflow_run_id, created_at: $created_at,
      images: $images}'
}

# Re-reads what was written rather than trusting what was meant to be written:
# this is the gate the hash is taken after.
validate_release_manifest() {
  local file="$1" expected actual created_at
  [[ -f "$file" && ! -L "$file" ]] || return 1
  jq -e '
    (keys == ["created_at", "images", "schema_version", "source_sha", "workflow_run_id"])
    and .schema_version == 1
    and (.source_sha | type == "string" and test("^[a-f0-9]{40}$"))
    and (.workflow_run_id | type == "number" and . > 0 and floor == .)
    and (.created_at | type == "string"
      and test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$"))
    and (.images | type == "object" and length > 0)
    and (.images | to_entries | all(.value | type == "string" and test("^sha256:[a-f0-9]{64}$")))
  ' "$file" >/dev/null || return 1
  created_at="$(jq -r '.created_at' "$file")"
  date -u -d "$created_at" >/dev/null 2>&1 || return 1
  expected="$(printf '%s\n' "${NCHAT_DEV_IMAGES[@]}" | LC_ALL=C sort)"
  # --stream reports every image key as it appears in the file, so a name
  # repeated in the JSON is seen twice here instead of being collapsed by the
  # parser into a set that would look complete.
  actual="$(jq -r --stream '
    select(length == 2 and (.[0] | length) == 2 and .[0][0] == "images") | .[0][1]
  ' "$file" | LC_ALL=C sort)"
  [[ "$expected" == "$actual" ]]
}

release_manifest_inputs_valid() {
  local artifacts_dir="$1" source_sha="$2" run_id="$3"
  if ! validate_digest_artifacts "$artifacts_dir"; then
    release_manifest_fail "the digest artifacts of this run are not exactly the ${#NCHAT_DEV_IMAGES[@]} expected ones"
    return 1
  fi
  if ! validate_commit_sha "$source_sha"; then
    release_manifest_fail "source SHA is not a 40-character lowercase hexadecimal commit"
    return 1
  fi
  if [[ ! "$run_id" =~ ^[1-9][0-9]{0,18}$ ]]; then
    release_manifest_fail "workflow run id is not a positive integer"
    return 1
  fi
}

release_manifest_output_valid() {
  local output_dir="$1"
  [[ -n "$output_dir" && "$output_dir" != / ]]
}

# Removes the two files this script owns, by name. Nothing else in the output
# directory is touched: in the workflow the digest artifacts are its neighbours.
clear_release_manifest_outputs() {
  local output_dir="$1"
  rm -f -- "$output_dir/$RELEASE_MANIFEST_JSON" "$output_dir/$RELEASE_MANIFEST_SHA256"
}

# Writes the manifest into the temporary file and proves it satisfies the
# contract, so nothing under a final name has existed yet when this fails.
build_release_manifest() {
  local manifest_temp="$1" artifacts_dir="$2" source_sha="$3" run_id="$4" images
  images="$(release_manifest_images_json "$artifacts_dir")" || return 1
  release_manifest_document "$images" "$source_sha" "$run_id" \
    "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >"$manifest_temp" || return 1
  validate_release_manifest "$manifest_temp"
}

# The manifest and its hash appear together or not at all. The hash is taken
# from the published name, in the published directory, so the .sha256 keeps the
# exact `sha256sum -c` format and never names a temporary file.
publish_release_manifest() {
  local output_dir="$1" manifest_temp="$2" hash_temp hash_name
  hash_temp="$(mktemp "$output_dir/.release-manifest.sha256.XXXXXX")" || return 1
  hash_name="${hash_temp##*/}"
  mv -- "$manifest_temp" "$output_dir/$RELEASE_MANIFEST_JSON" || {
    rm -f -- "$hash_temp"
    return 1
  }
  # Nothing may touch the manifest from here on: the hash is its seal.
  if ! (
    cd "$output_dir"
    sha256sum "$RELEASE_MANIFEST_JSON" >"$hash_name"
    sha256sum -c "$hash_name" >/dev/null
  ); then
    rm -f -- "$hash_temp"
    return 1
  fi
  mv -- "$hash_temp" "$output_dir/$RELEASE_MANIFEST_SHA256"
}

write_release_manifest() {
  local artifacts_dir="$1" source_sha="$2" run_id="$3" output_dir="$4" manifest_temp
  if ! release_manifest_output_valid "$output_dir"; then
    release_manifest_fail "the output directory must be a non-empty path other than /"
    return 1
  fi
  mkdir -p "$output_dir"
  # Before the first gate that can refuse: a rejected attempt must never leave
  # an earlier attempt's manifest behind, looking like the result of this one.
  clear_release_manifest_outputs "$output_dir"
  release_manifest_inputs_valid "$artifacts_dir" "$source_sha" "$run_id" || return 1
  manifest_temp="$(mktemp "$output_dir/.release-manifest.json.XXXXXX")"
  if ! build_release_manifest "$manifest_temp" "$artifacts_dir" "$source_sha" "$run_id"; then
    release_manifest_fail "the generated manifest does not satisfy the release contract"
    rm -f -- "$manifest_temp"
    return 1
  fi
  if ! publish_release_manifest "$output_dir" "$manifest_temp"; then
    release_manifest_fail "the manifest could not be sealed and published"
    rm -f -- "$manifest_temp"
    clear_release_manifest_outputs "$output_dir"
    return 1
  fi
}

main() {
  if [[ "$#" -ne 2 ]]; then
    release_manifest_fail "usage: release-manifest.sh <digest-artifacts-dir> <output-dir>"
    return 1
  fi
  write_release_manifest "$1" "${NCHAT_RELEASE_SOURCE_SHA:-}" \
    "${NCHAT_RELEASE_RUN_ID:-}" "$2"
  echo "Release manifest written to $2/$RELEASE_MANIFEST_JSON"
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi
