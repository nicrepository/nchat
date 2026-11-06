#!/usr/bin/env bash
# Local packaging only; never pushes. Registry digest approval is a rollout prerequisite.
set -Eeuo pipefail
root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
build_dir="$(mktemp -d /tmp/nchat-signatures-build.XXXXXXXX)"
trap 'rm -rf -- "$build_dir"' EXIT
cd "$root_dir"
CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o "$build_dir/clamav-signatures" ./services/file-service/cmd/clamav-signatures
cp infra/docker/clamav-signatures/Dockerfile "$build_dir/Dockerfile"
docker build --network=none --pull=false -t nchat-clamav-signatures:local-test "$build_dir"
