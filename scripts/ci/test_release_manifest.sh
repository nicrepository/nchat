#!/usr/bin/env bash
# Behaviour tests for the immutable release manifest (CICD-04).
#
# The manifest is the identity of a production release, so the only way to know
# its gates refuse what they claim to refuse is to drive them with input that
# must be refused: a missing image, an extra one, a truncated digest, a source
# SHA that is one character short. Every negative case below must leave no
# release-manifest.json behind -- a refused release must not publish one.
#
# No network, no Docker, no cluster: the script under test is run against a
# temporary directory of fake digest artifacts, and sourced when a single
# function has to be driven directly.
set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
RELEASE_MANIFEST="$ROOT_DIR/scripts/deploy/nchat-prod/release-manifest.sh"
WORKFLOW="$ROOT_DIR/.github/workflows/images.yml"
CHECKER="$ROOT_DIR/scripts/ci/check_release_manifest_workflow.py"

SOURCE_SHA=0123456789abcdef0123456789abcdef01234567
RUN_ID=123456789
FAILURES=0

# Sourcing defines the functions without running main, and brings in the
# canonical image inventory the script itself derives the contract from.
# shellcheck source=scripts/deploy/nchat-prod/release-manifest.sh
source "$RELEASE_MANIFEST"

STEP_MUTATOR=""

WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/nchat-release-manifest-test.XXXXXX")"
trap 'rm -rf "$WORK_DIR"' EXIT

fail() {
  echo "  [FAIL] $*" >&2
  FAILURES=$((FAILURES + 1))
}

pass() {
  echo "  [ok] $*"
}

# A distinct, well-formed digest per image, derived from its name so the
# fixtures stay readable and every image gets a different value.
fixture_digest() {
  printf 'sha256:%s' "$(printf '%s' "$1" | sha256sum | cut -d' ' -f1)"
}

# A fresh, complete set of digest artifacts for the current run.
make_artifacts() {
  local dir="$WORK_DIR/artifacts.$1" image
  rm -rf "$dir"
  mkdir -p "$dir"
  for image in "${NCHAT_DEV_IMAGES[@]}"; do
    printf '%s' "$(fixture_digest "$image")" >"$dir/digest-$image.txt"
  done
  printf '%s' "$dir"
}

# Runs the script as the workflow does, in its own output directory. The
# directory is deliberately left as the previous case left it: a refused attempt
# has to clean up after itself, and wiping it here would hide that it does not.
run_manifest() {
  local artifacts="$1" output="$2" sha="${3-$SOURCE_SHA}" run_id="${4-$RUN_ID}"
  NCHAT_RELEASE_SOURCE_SHA="$sha" NCHAT_RELEASE_RUN_ID="$run_id" \
    bash "$RELEASE_MANIFEST" "$artifacts" "$output" >/dev/null 2>&1
}

# A refusal is only fail-closed if it also published nothing.
expect_refused() {
  local name="$1" artifacts="$2" sha="${3-$SOURCE_SHA}" run_id="${4-$RUN_ID}"
  local output="$WORK_DIR/out.refused"
  if run_manifest "$artifacts" "$output" "$sha" "$run_id"; then
    fail "$name: the generator accepted it"
    return
  fi
  if [[ -e "$output/release-manifest.json" || -e "$output/release-manifest.sha256" ]]; then
    fail "$name: refused but left a published manifest behind"
    return
  fi
  pass "$name"
}

expect_manifest_refused() {
  local name="$1" file="$2"
  if validate_release_manifest "$file" 2>/dev/null; then
    fail "$name: the contract check accepted it"
    return
  fi
  pass "$name"
}

assert_equals() {
  local name="$1" expected="$2" actual="$3"
  if [[ "$expected" != "$actual" ]]; then
    fail "$name: expected [$expected], got [$actual]"
    return
  fi
  pass "$name"
}

assert_true() {
  local name="$1"
  shift
  if "$@" >/dev/null 2>&1; then
    pass "$name"
    return
  fi
  fail "$name"
}

# --- Positive cases ---------------------------------------------------------

test_valid_release() {
  local artifacts output manifest images
  echo "valid release"
  artifacts="$(make_artifacts valid)"
  output="$WORK_DIR/out.valid"
  if ! run_manifest "$artifacts" "$output"; then
    fail "11/11 valid digests: the generator refused a valid release"
    return
  fi
  manifest="$output/release-manifest.json"
  pass "11/11 valid digests produce a manifest"
  assert_true "the generated manifest satisfies the contract" \
    validate_release_manifest "$manifest"
  assert_equals "schema_version is 1" 1 "$(jq -r '.schema_version' "$manifest")"
  assert_equals "source_sha is the built commit" "$SOURCE_SHA" \
    "$(jq -r '.source_sha' "$manifest")"
  assert_equals "workflow_run_id is a JSON integer" "number|$RUN_ID" \
    "$(jq -r '(.workflow_run_id | type) + "|" + (.workflow_run_id | tostring)' "$manifest")"
  assert_equals "created_at is an RFC3339 UTC timestamp" true \
    "$(jq -r '.created_at | test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$")' "$manifest")"
  assert_equals "migrations is present" "$(fixture_digest migrations)" \
    "$(jq -r '.images.migrations' "$manifest")"
  images="$(jq -r '.images | keys_unsorted[]' "$manifest" | LC_ALL=C sort | tr '\n' ' ')"
  assert_equals "the image set is exactly the canonical inventory" \
    "$(printf '%s\n' "${NCHAT_DEV_IMAGES[@]}" | LC_ALL=C sort | tr '\n' ' ')" "$images"
  assert_true "the hash verifies with sha256sum -c" \
    bash -c "cd '$output' && sha256sum -c release-manifest.sha256"
  assert_equals "the hash references release-manifest.json" release-manifest.json \
    "$(awk '{print $2}' "$output/release-manifest.sha256")"
}

test_serialisation_is_deterministic() {
  local images first second
  echo "deterministic serialisation"
  images="$(release_manifest_images_json "$(make_artifacts determinism)")"
  first="$(release_manifest_document "$images" "$SOURCE_SHA" "$RUN_ID" 2026-01-02T03:04:05Z)"
  second="$(release_manifest_document "$images" "$SOURCE_SHA" "$RUN_ID" 2026-01-02T03:04:05Z)"
  assert_equals "equal inputs serialise to equal bytes" "$first" "$second"
  assert_equals "keys are emitted in a stable order" \
    "created_at images schema_version source_sha workflow_run_id" \
    "$(jq -r 'keys_unsorted | join(" ")' <<<"$first")"
}

# --- Negative cases: the digest artifacts of the run ------------------------

test_wrong_image_count() {
  local artifacts
  echo "image set"
  artifacts="$(make_artifacts missing)"
  rm "$artifacts/digest-chat-service.txt"
  expect_refused "10 of 11 digests" "$artifacts"

  artifacts="$(make_artifacts extra)"
  printf '%s' "$(fixture_digest extra)" >"$artifacts/digest-extra-service.txt"
  expect_refused "an unknown extra image" "$artifacts"

  artifacts="$(make_artifacts no-migrations)"
  rm "$artifacts/digest-migrations.txt"
  expect_refused "migrations missing" "$artifacts"

  expect_refused "the artifacts directory does not exist" "$WORK_DIR/absent"
}

test_malformed_digests() {
  local artifacts hex
  echo "digest contract"
  hex="$(printf 'a%.0s' {1..64})"
  artifacts="$(make_artifacts empty-digest)"
  : >"$artifacts/digest-web.txt"
  expect_refused "an empty digest" "$artifacts"

  artifacts="$(make_artifacts short-digest)"
  printf 'sha256:%s' "${hex:0:63}" >"$artifacts/digest-web.txt"
  expect_refused "a digest with 63 hex characters" "$artifacts"

  artifacts="$(make_artifacts long-digest)"
  printf 'sha256:%sa' "$hex" >"$artifacts/digest-web.txt"
  expect_refused "a digest with 65 hex characters" "$artifacts"

  artifacts="$(make_artifacts wrong-algorithm)"
  printf 'sha512:%s' "$hex" >"$artifacts/digest-web.txt"
  expect_refused "an algorithm other than sha256" "$artifacts"

  artifacts="$(make_artifacts non-hex-digest)"
  printf 'sha256:%sz' "${hex:0:63}" >"$artifacts/digest-web.txt"
  expect_refused "a non-hexadecimal character in a digest" "$artifacts"

  artifacts="$(make_artifacts uppercase-digest)"
  printf 'sha256:%s' "${hex^^}" >"$artifacts/digest-web.txt"
  expect_refused "an uppercase digest" "$artifacts"
}

test_unexpected_artifact_content() {
  local artifacts
  echo "unexpected artifact content"
  artifacts="$(make_artifacts trailing-newline)"
  printf '%s\n' "$(fixture_digest web)" >"$artifacts/digest-web.txt"
  expect_refused "a digest file with trailing content" "$artifacts"

  artifacts="$(make_artifacts symlink)"
  rm "$artifacts/digest-web.txt"
  ln -s "$artifacts/digest-admin-web.txt" "$artifacts/digest-web.txt"
  expect_refused "a digest artifact that is a symlink" "$artifacts"

  artifacts="$(make_artifacts subdirectory)"
  mkdir "$artifacts/nested"
  expect_refused "an unexpected entry in the artifacts directory" "$artifacts"
}

# --- Negative cases: the release identity -----------------------------------

test_invalid_identity() {
  local artifacts hex
  echo "release identity"
  artifacts="$(make_artifacts identity)"
  hex="$(printf 'a%.0s' {1..40})"
  expect_refused "a source SHA with 39 characters" "$artifacts" "${hex:0:39}"
  expect_refused "a source SHA with 41 characters" "$artifacts" "${hex}a"
  expect_refused "a non-hexadecimal source SHA" "$artifacts" "${hex:0:39}z"
  expect_refused "an uppercase source SHA" "$artifacts" "${hex^^}"
  expect_refused "an empty source SHA" "$artifacts" ""
  expect_refused "a run id of zero" "$artifacts" "$SOURCE_SHA" 0
  expect_refused "a negative run id" "$artifacts" "$SOURCE_SHA" -1
  expect_refused "a non-numeric run id" "$artifacts" "$SOURCE_SHA" notanumber
  expect_refused "a fractional run id" "$artifacts" "$SOURCE_SHA" 1.5
  expect_refused "an empty run id" "$artifacts" "$SOURCE_SHA" ""
}

# --- Negative cases: the manifest contract ----------------------------------

# Drives validate_release_manifest with documents the generator would never
# write, which is exactly why the gate has to refuse them on the way out.
write_variant() {
  local name="$1" filter="$2" file
  file="$WORK_DIR/variant-$name.json"
  jq -S "$filter" "$WORK_DIR/out.valid/release-manifest.json" >"$file"
  printf '%s' "$file"
}

test_manifest_contract() {
  echo "manifest contract"
  # jq cannot emit a repeated key, so this one is assembled from the raw bytes
  # of a valid manifest: the chat-service entry appears twice.
  sed 's/^\( *"chat-service": .*\)$/\1\n\1/' \
    "$WORK_DIR/out.valid/release-manifest.json" >"$WORK_DIR/variant-duplicate.json"
  expect_manifest_refused "a duplicated image name" "$WORK_DIR/variant-duplicate.json"
  expect_manifest_refused "an image absent from the manifest" \
    "$(write_variant missing 'del(.images.web)')"
  expect_manifest_refused "an image unknown to the inventory" \
    "$(write_variant unknown '.images["ghost-service"] = .images.web')"
  expect_manifest_refused "a malformed timestamp" \
    "$(write_variant timestamp '.created_at = "2026-01-02 03:04:05"')"
  expect_manifest_refused "a timestamp that is not a real instant" \
    "$(write_variant impossible '.created_at = "2026-02-30T03:04:05Z"')"
  expect_manifest_refused "a non-UTC timestamp" \
    "$(write_variant offset '.created_at = "2026-01-02T03:04:05-03:00"')"
  expect_manifest_refused "an unexpected schema version" \
    "$(write_variant schema '.schema_version = 2')"
  expect_manifest_refused "arbitrary extra metadata" \
    "$(write_variant extra_key '.operator = "someone"')"
  expect_manifest_refused "a missing required field" \
    "$(write_variant no_run_id 'del(.workflow_run_id)')"
  expect_manifest_refused "a run id serialised as a string" \
    "$(write_variant string_run_id '.workflow_run_id = "123456789"')"
  printf 'not json' >"$WORK_DIR/variant-broken.json"
  expect_manifest_refused "a file that is not JSON" "$WORK_DIR/variant-broken.json"
}

test_manifest_is_sealed() {
  local output="$WORK_DIR/out.sealed" artifacts
  echo "the hash seals the manifest"
  artifacts="$(make_artifacts sealed)"
  if ! run_manifest "$artifacts" "$output"; then
    fail "sealed manifest: the generator refused a valid release"
    return
  fi
  jq -S '.source_sha = "ffffffffffffffffffffffffffffffffffffffff"' \
    "$output/release-manifest.json" >"$output/tampered" &&
    mv "$output/tampered" "$output/release-manifest.json"
  if (cd "$output" && sha256sum -c release-manifest.sha256 >/dev/null 2>&1); then
    fail "a manifest edited after hashing still verified"
    return
  fi
  pass "a manifest edited after hashing fails sha256sum -c"
}

# The bug a code review found: a refused attempt returned non-zero but left the
# previous attempt's manifest and hash in place, where nothing distinguishes
# them from the result of the attempt that was just refused.
test_refused_attempt_leaves_no_output() {
  local artifacts output="$WORK_DIR/out.stale" leftovers
  echo "a refused attempt publishes nothing"
  artifacts="$(make_artifacts stale)"
  mkdir -p "$output"
  printf 'stale-manifest' >"$output/release-manifest.json"
  printf 'stale-hash' >"$output/release-manifest.sha256"
  if run_manifest "$artifacts" "$output" not-a-source-sha; then
    fail "stale output: an invalid source SHA was accepted"
    return
  fi
  if [[ -e "$output/release-manifest.json" || -e "$output/release-manifest.sha256" ]]; then
    fail "stale output: a refused attempt left the previous manifest behind"
    return
  fi
  pass "a refused attempt removes the previous manifest and hash"
  leftovers="$(find "$output" -mindepth 1 | LC_ALL=C sort | tr '\n' ' ')"
  assert_equals "a refused attempt leaves no temporary file" "" "$leftovers"
  if ! run_manifest "$artifacts" "$output"; then
    fail "stale output: a valid release was refused after a failed attempt"
    return
  fi
  pass "a valid attempt in the same directory publishes a fresh manifest"
  assert_true "the fresh manifest verifies with sha256sum -c" \
    bash -c "cd '$output' && sha256sum -c release-manifest.sha256"
  assert_equals "the fresh manifest replaced the stale one" "$SOURCE_SHA" \
    "$(jq -r '.source_sha' "$output/release-manifest.json")"
}

# --- The workflow that produces it ------------------------------------------

# Value mutations are a sed away; moving, dropping or repeating a step is not,
# so those are made on the parsed YAML. The mutator only builds fixtures --
# the contract itself lives in check_release_manifest_workflow.py and is never
# consulted here.
write_step_mutator() {
  cat >"$STEP_MUTATOR" <<'PYTHON'
"""Break one structural invariant of the release-manifest job, on a copy."""

import sys

import yaml

CHECKOUT = "actions/checkout"
DOWNLOAD = "actions/download-artifact"
UPLOAD = "actions/upload-artifact"
GENERATOR = "release-manifest.sh"


def index_using(steps, action):
    return next(
        i for i, s in enumerate(steps) if str(s.get("uses", "")).split("@")[0] == action
    )


def index_generating(steps):
    return next(i for i, s in enumerate(steps) if GENERATOR in str(s.get("run", "")))


def move(steps, source, destination):
    steps.insert(destination, steps.pop(source))


def mutate(steps, operation):
    if operation == "drop-checkout":
        steps.pop(index_using(steps, CHECKOUT))
    elif operation == "duplicate-checkout":
        steps.insert(0, dict(steps[index_using(steps, CHECKOUT)]))
    elif operation == "checkout-after-download":
        move(steps, index_using(steps, CHECKOUT), index_using(steps, DOWNLOAD))
    elif operation == "download-after-generation":
        move(steps, index_using(steps, DOWNLOAD), index_generating(steps))
    elif operation == "upload-before-generation":
        move(steps, index_using(steps, UPLOAD), index_generating(steps))
    else:
        raise SystemExit(f"unknown mutation: {operation}")


def main(source, destination, operation):
    workflow = yaml.safe_load(open(source, encoding="utf-8"))
    mutate(workflow["jobs"]["release-manifest"]["steps"], operation)
    with open(destination, "w", encoding="utf-8") as handle:
        yaml.safe_dump(workflow, handle, sort_keys=False)
    return 0


sys.exit(main(sys.argv[1], sys.argv[2], sys.argv[3]))
PYTHON
}

run_workflow_checker() {
  python3 "$CHECKER" "$1"
}

# A copy of the real workflow with one invariant broken. A sed that matched
# nothing yields the unmodified workflow, which the checker accepts, so a
# mutation that fails to apply is reported as a failure rather than passing.
mutate_workflow() {
  local name="$1" expression="$2" copy
  copy="$WORK_DIR/workflow-$name.yml"
  sed "$expression" "$WORKFLOW" >"$copy"
  printf '%s' "$copy"
}

mutate_steps() {
  local name="$1" operation="$2" copy
  copy="$WORK_DIR/workflow-$name.yml"
  python3 "$STEP_MUTATOR" "$WORKFLOW" "$copy" "$operation"
  printf '%s' "$copy"
}

expect_workflow_refused() {
  local name="$1" copy="$2"
  if run_workflow_checker "$copy" >/dev/null 2>&1; then
    fail "$name: the workflow checker accepted it"
    return
  fi
  pass "$name"
}

test_workflow_wiring() {
  echo "workflow wiring"
  if run_workflow_checker "$WORKFLOW" >/dev/null; then
    pass "images.yml wires the manifest job as specified"
  else
    fail "images.yml does not wire the manifest job as specified"
  fi
}

# The regression a code review found: each of these mutations left the checker
# reporting success, so each one is now a case of its own.
test_workflow_wiring_rejects_regressions() {
  local wrong_needs="s/needs: build/needs: prebuild/"
  local wrong_pattern="s/pattern: digest-\*/pattern: unrelated-*/"
  local no_merge="s/merge-multiple: true/merge-multiple: false/"
  local echoed_generator="s|run: scripts/deploy/nchat-prod/release-manifest.sh.*|run: echo release-manifest.sh|"
  echo "workflow wiring regressions"
  expect_workflow_refused "a job that waits for prebuild instead of build" \
    "$(mutate_workflow needs "$wrong_needs")"
  expect_workflow_refused "a download of an unrelated artifact pattern" \
    "$(mutate_workflow pattern "$wrong_pattern")"
  expect_workflow_refused "digests left unmerged" \
    "$(mutate_workflow merge "$no_merge")"
  expect_workflow_refused "a generation step that only echoes the script name" \
    "$(mutate_workflow generator "$echoed_generator")"
  expect_workflow_refused "the four broken invariants together" \
    "$(mutate_workflow combined "$wrong_needs; $wrong_pattern; $no_merge; $echoed_generator")"
}

# The generator is bash calling GNU coreutils over a checked-out tree, in an
# order the manifest's meaning depends on: digests before the manifest that
# names them, the hash before the artifact that preserves it.
test_workflow_environment_and_order() {
  echo "workflow runner, checkout and step order"
  expect_workflow_refused "a runner that is not ubuntu-latest" \
    "$(mutate_workflow runner 's/runs-on: ubuntu-latest/runs-on: windows-latest/')"
  expect_workflow_refused "a job with no checkout" \
    "$(mutate_steps no-checkout drop-checkout)"
  expect_workflow_refused "a job that checks out twice" \
    "$(mutate_steps two-checkouts duplicate-checkout)"
  expect_workflow_refused "a checkout after the digests are downloaded" \
    "$(mutate_steps late-checkout checkout-after-download)"
  expect_workflow_refused "digests downloaded after the manifest is generated" \
    "$(mutate_steps late-download download-after-generation)"
  expect_workflow_refused "the manifest uploaded before it is generated" \
    "$(mutate_steps early-upload upload-before-generation)"
}

main() {
  STEP_MUTATOR="$WORK_DIR/step-mutator.py"
  write_step_mutator
  test_valid_release
  test_serialisation_is_deterministic
  test_wrong_image_count
  test_malformed_digests
  test_unexpected_artifact_content
  test_invalid_identity
  test_manifest_contract
  test_manifest_is_sealed
  test_refused_attempt_leaves_no_output
  test_workflow_wiring
  test_workflow_wiring_rejects_regressions
  test_workflow_environment_and_order
  if [[ "$FAILURES" -ne 0 ]]; then
    echo "Release manifest tests failed: $FAILURES" >&2
    return 1
  fi
  echo "Release manifest tests passed."
}

main "$@"
