#!/usr/bin/env bash
# Behaviour tests for the nchat-dev GitOps publish (scripts/deploy/nchat-dev/gitops-publish.sh).
#
# The publish is the step that moves a release into the infrastructure repository, so the
# properties that matter are that it moves exactly the release's digests, that
# it writes the migrations Job Flux runs first, that publishing the same release
# twice changes nothing, and that it refuses rather than guesses when the
# repository is not what it expects.
#
# The repository is a local bare clone; the overlay rendered is the real
# migrations overlay. No cluster and no network beyond the verified kustomize.
set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
PUBLISH="$ROOT_DIR/scripts/deploy/nchat-dev/gitops-publish.sh"
# shellcheck source=scripts/deploy/nchat-dev/kustomize.env
source "$ROOT_DIR/scripts/deploy/nchat-dev/kustomize.env"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/nchat-dev-gitops.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT

if [[ "$(kustomize version 2>/dev/null)" != "$KUSTOMIZE_VERSION" ]]; then
  PATH="$(GITHUB_PATH='' "$ROOT_DIR/scripts/deploy/nchat-dev/install-kustomize.sh"):$PATH"
  export PATH
fi

IMAGES=(admin-service auth-service chat-service document-converter file-service
  media-service notification-service search-service admin-web web)
SHA=0123456789abcdef0123456789abcdef01234567
FAILURES=0

fail() {
  echo "FAIL: $*" >&2
  FAILURES=$((FAILURES + 1))
}

digest() {
  printf 'sha256:%064x' "$1"
}

# A k3s-dev with one Deployment per runtime image, each pinned by digest, and
# the empty migrations folder the infrastructure creates.
make_repository() {
  local seed="$WORK/seed" image
  rm -rf "$seed" "$WORK/k3s-dev.git"
  mkdir -p "$seed/nchat-dev" "$seed/nchat-dev-migracoes"
  for image in "${IMAGES[@]}"; do
    printf 'kind: Deployment\nmetadata:\n  name: %s\nspec:\n  template:\n    spec:\n      containers:\n      - image: ghcr.io/nicrepository/nchat/%s@%s\n' \
      "$image" "$image" "$(digest 1)" >"$seed/nchat-dev/deployment-$image.yaml"
  done
  printf 'apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources: []\n' \
    >"$seed/nchat-dev-migracoes/kustomization.yaml"
  git -C "$seed" init --quiet --initial-branch main
  git -C "$seed" add .
  git -C "$seed" -c user.name=test -c user.email=test@example.com commit --quiet -m seed
  git clone --quiet --bare "$seed" "$WORK/k3s-dev.git"
}

# Eleven digests, every one different from the seeded ones.
make_artifacts() {
  local image n=100
  rm -rf "$WORK/artifacts"
  mkdir -p "$WORK/artifacts"
  for image in "${IMAGES[@]}" migrations; do
    n=$((n + 1))
    digest "$n" >"$WORK/artifacts/digest-$image.txt"
  done
}

publish() {
  K3S_DEV_REPOSITORY="file://$WORK/k3s-dev.git" ARTIFACTS_DIR="$WORK/artifacts" \
    DEPLOY_SHA="$1" K3S_DEV_TOKEN=test "$PUBLISH" >"$WORK/out" 2>&1
}

commits() {
  git -C "$WORK/k3s-dev.git" rev-list --count main
}

show() {
  git -C "$WORK/k3s-dev.git" show "main:$1"
}

# A release is published: every image moves, the Job is named after it.
make_repository
make_artifacts
if publish "$SHA"; then
  [[ "$(commits)" -eq 2 ]] || fail "publishing a release must add exactly one commit"
  for image in "${IMAGES[@]}"; do
    show "nchat-dev/deployment-$image.yaml" | grep -q "nchat/$image@$(<"$WORK/artifacts/digest-$image.txt")" ||
      fail "$image was not pinned to the release digest"
  done
  show nchat-dev-migracoes/job.yaml | grep -q "name: nchat-migrations-${SHA:0:12}" ||
    fail "the migrations Job is not named after the release"
  show nchat-dev-migracoes/job.yaml | grep -q "nchat/migrations@$(<"$WORK/artifacts/digest-migrations.txt")" ||
    fail "the migrations Job does not run the release's migrations image"
  show nchat-dev-migracoes/kustomization.yaml | grep -q 'job.yaml' ||
    fail "the migrations folder does not include the Job"
else
  fail "a well-formed release was refused: $(cat "$WORK/out")"
fi

# The same release again is a no-op, not a second commit.
if publish "$SHA"; then
  [[ "$(commits)" -eq 2 ]] || fail "republishing the same release added a commit"
else
  fail "republishing the same release failed: $(cat "$WORK/out")"
fi

# A manifest the script does not know how to move is refused, not skipped.
make_repository
make_artifacts
git clone --quiet "$WORK/k3s-dev.git" "$WORK/drift"
git -C "$WORK/drift" rm --quiet nchat-dev/deployment-web.yaml
git -C "$WORK/drift" -c user.name=test -c user.email=test@example.com commit --quiet -m drift
git -C "$WORK/drift" push --quiet origin main
if publish "$SHA"; then
  fail "a repository without the web Deployment was published anyway"
fi
[[ "$(commits)" -eq 2 ]] || fail "a refused release still reached the repository"

# A SHA that is not a full commit is refused before anything is touched.
if publish "${SHA:0:12}"; then
  fail "a short SHA was accepted"
fi

if [[ "$FAILURES" -gt 0 ]]; then
  echo "nchat-dev gitops publish tests: $FAILURES failure(s)"
  exit 1
fi
echo "nchat-dev gitops publish tests passed."
