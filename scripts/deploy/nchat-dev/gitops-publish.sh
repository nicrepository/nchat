#!/usr/bin/env bash
# Delivers a release to nchat-dev through GitOps.
#
#   scripts/deploy/nchat-dev/gitops-publish.sh
#
# nchat-dev no longer runs on srv-apps-01: it runs on a k3s cluster that Flux
# reconciles from an infrastructure Git repository. No CI runner talks
# to that cluster's API, so this script does not apply anything. It writes the
# release into the repository and Flux applies it, in the order deploy.sh used:
#
#   nchat-dev-migracoes/  the migrations Job of this release, named after the
#                         commit so every release runs a Job of its own. Flux
#                         applies this folder first and waits for the Job to
#                         complete (Kustomization "producao-migracoes").
#   nchat-dev/            the ten runtime images, pinned to this release's
#                         digests. Flux applies them only once the migrations
#                         Kustomization is ready at the same revision
#                         (dependsOn), so a failed migration stops the rollout.
#
# The data services (PostgreSQL, SeaweedFS, Valkey) and the secrets stay as the
# infrastructure owns them in that repository; a release only moves images.
#
# Required environment:
#   ARTIFACTS_DIR  the digest-<image>.txt files of this release
#   DEPLOY_SHA     the 40-character commit the images were built from
#   K3S_DEV_REPOSITORY  HTTPS URL of the infrastructure repository (environment variable)
#   K3S_DEV_TOKEN  project access token, write_repository on that repository only
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../../.." && pwd -P)"
# shellcheck source=scripts/deploy/nchat-dev/lib.sh
source "$SCRIPT_DIR/lib.sh"
# shellcheck source=scripts/deploy/nchat-dev/kustomize.env
source "$SCRIPT_DIR/kustomize.env"

IMAGE_PREFIX=ghcr.io/nicrepository/nchat

fail() {
  echo "$*" >&2
  exit 1
}

[[ "${DEPLOY_SHA:-}" =~ ^[a-f0-9]{40}$ ]] || fail "DEPLOY_SHA must be a full lowercase commit SHA"
[[ "${K3S_DEV_REPOSITORY:-}" =~ ^(https|file):// ]] || fail "K3S_DEV_REPOSITORY must be the repository URL"
[[ -n "${K3S_DEV_TOKEN:-}" ]] || fail "K3S_DEV_TOKEN is required"
[[ -d "${ARTIFACTS_DIR:-}" ]] || fail "ARTIFACTS_DIR must name the digest artifacts directory"
validate_digest_artifacts "$ARTIFACTS_DIR" || fail "the digest artifacts in $ARTIFACTS_DIR are not exactly the eleven images"
[[ "$(kustomize version)" == "$KUSTOMIZE_VERSION" ]] || fail "kustomize is not $KUSTOMIZE_VERSION"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# The Job is renamed with the commit: a Job's template is immutable, and a
# fresh name also makes Flux prune the previous release's Job.
render_migrations() {
  local overlay="$WORK/infra-k8s/overlays/nchat-dev-server/migrations"
  cp -a "$ROOT_DIR/infra/k8s" "$WORK/infra-k8s"
  set_digest_image "$overlay" "$IMAGE_PREFIX/migrations" "$ARTIFACTS_DIR/digest-migrations.txt"
  (cd "$overlay" && kustomize edit set namesuffix -- "-${DEPLOY_SHA:0:12}")
  validate_rendered_overlay "$overlay" "$WORK/migrations.yaml"
  [[ "$(grep -c '^kind: Job$' "$WORK/migrations.yaml")" -eq 1 ]] || fail "the migrations overlay must render exactly one Job"
}

# The token reaches git through GIT_ASKPASS, never through the URL or argv.
clone_repository() {
  cat >"$WORK/askpass" <<'ASKPASS'
#!/bin/sh
case "$1" in
  Username*) echo nchat-deploy ;;
  *) printf '%s\n' "$K3S_DEV_TOKEN" ;;
esac
ASKPASS
  chmod 0700 "$WORK/askpass"
  export GIT_ASKPASS="$WORK/askpass" GIT_TERMINAL_PROMPT=0
  git clone --quiet --depth 50 --branch main "$K3S_DEV_REPOSITORY" "$WORK/k3s-dev"
  [[ -d "$WORK/k3s-dev/nchat-dev" && -d "$WORK/k3s-dev/nchat-dev-migracoes" ]] ||
    fail "the infrastructure repository has no nchat-dev or nchat-dev-migracoes folder"
}

# Each runtime image is pinned by digest in exactly one manifest; anything
# else means the repository drifted from what this script knows how to move.
pin_runtime_images() {
  local image digest files
  for image in "${NCHAT_DEV_RUNTIME_IMAGES[@]}"; do
    digest="$(<"$ARTIFACTS_DIR/digest-$image.txt")"
    mapfile -t files < <(grep -lE "image: $IMAGE_PREFIX/$image@sha256:[a-f0-9]{64}" "$WORK"/k3s-dev/nchat-dev/*.yaml || true)
    [[ "${#files[@]}" -eq 1 ]] || fail "expected one manifest pinning $image in nchat-dev, found ${#files[@]}"
    sed -i -E "s#(image: $IMAGE_PREFIX/$image)@sha256:[a-f0-9]{64}#\\1@$digest#" "${files[0]}"
  done
}

write_migrations() {
  local folder="$WORK/k3s-dev/nchat-dev-migracoes"
  cp "$WORK/migrations.yaml" "$folder/job.yaml"
  cat >"$folder/kustomization.yaml" <<KUSTOMIZATION
# Written by nchat's scripts/deploy/nchat-dev/gitops-publish.sh for
# $DEPLOY_SHA. Flux applies this folder before ./nchat-dev.
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - job.yaml
KUSTOMIZATION
}

# A concurrent commit by the infrastructure is the only expected reason for a
# rejected push; the release is rebased on top of it, never forced.
publish() {
  local repository="$WORK/k3s-dev"
  git -C "$repository" add nchat-dev nchat-dev-migracoes
  if git -C "$repository" diff --cached --quiet; then
    echo "the infrastructure repository already carries $DEPLOY_SHA"
    return 0
  fi
  git -C "$repository" -c user.name="nchat CD" -c user.email="nchat-cd@nic-labs.com" \
    commit --quiet -m "nchat-dev: release ${DEPLOY_SHA:0:12}" \
    -m "Source: https://github.com/nicrepository/nchat/commit/$DEPLOY_SHA"
  for _ in 1 2 3; do
    if git -C "$repository" push --quiet origin HEAD:main; then
      echo "published $DEPLOY_SHA to the infrastructure repository as $(git -C "$repository" rev-parse --short HEAD)"
      return 0
    fi
    git -C "$repository" pull --quiet --rebase origin main || return 1
  done
  fail "the infrastructure repository kept rejecting the push"
}

render_migrations
clone_repository
pin_runtime_images
write_migrations
publish
