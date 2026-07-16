#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
# shellcheck source=scripts/deploy/nchat-dev/kustomize.env
source "$SCRIPT_DIR/kustomize.env"

[[ "$KUSTOMIZE_VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]
[[ "$KUSTOMIZE_LINUX_AMD64_SHA256" =~ ^[a-f0-9]{64}$ ]]
[[ "$(uname -s)" == Linux && "$(uname -m)" == x86_64 ]]

destination="${RUNNER_TEMP:-${TMPDIR:-/tmp}}/nchat-dev-bin"
temporary="$(mktemp -d "${TMPDIR:-/tmp}/nchat-kustomize.XXXXXX")"
trap 'rm -rf "$temporary"' EXIT
archive="$temporary/kustomize.tar.gz"
url="https://github.com/kubernetes-sigs/kustomize/releases/download/kustomize/${KUSTOMIZE_VERSION}/kustomize_${KUSTOMIZE_VERSION}_linux_amd64.tar.gz"

curl --proto '=https' --tlsv1.2 --fail --silent --show-error --location --output "$archive" "$url"
read -r actual_sha256 _ < <(sha256sum "$archive")
[[ "$actual_sha256" == "$KUSTOMIZE_LINUX_AMD64_SHA256" ]]
tar -xzf "$archive" -C "$temporary" kustomize
mkdir -p "$destination"
install -m 0555 "$temporary/kustomize" "$destination/kustomize.new"
mv -f "$destination/kustomize.new" "$destination/kustomize"
[[ "$("$destination/kustomize" version)" == "$KUSTOMIZE_VERSION" ]]

if [[ -n "${GITHUB_PATH:-}" ]]; then
  printf '%s\n' "$destination" >>"$GITHUB_PATH"
else
  printf '%s\n' "$destination"
fi
