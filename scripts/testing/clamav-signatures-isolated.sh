#!/usr/bin/env bash
# Local only. No pull, external network, published ports, credentials or cluster.
set -Eeuo pipefail
root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
image="${CLAMAV_SIGNATURE_TEST_IMAGE:-clamav/clamav:1.4@sha256:d97f5ef70fbe9f1f8e5490278082067f16f5824eca2eb4176f4df3748a45a4c2}"
if [[ ! "$image" =~ ^clamav/clamav(:1\.4(\.6)?)?@sha256:[a-f0-9]{64}$ ]]; then exit 2; fi
work_dir="$(mktemp -d /tmp/nchat-signatures-test.XXXXXXXX)"
chain_container=""
chain_volume=""
fixture_container=""
cleanup() {
  if [[ -n "$fixture_container" ]]; then docker rm -f "$fixture_container" >/dev/null; fi
  if [[ -n "$chain_container" ]]; then docker rm -f "$chain_container" >/dev/null; fi
  if [[ -n "$chain_volume" ]]; then docker volume rm "$chain_volume" >/dev/null; fi
  rm -rf -- "$work_dir"
}
trap cleanup EXIT
cd "$root_dir"
docker image inspect "$image" >/dev/null
chmod 0755 "$work_dir"
cp "$root_dir/infra/k8s/base/services/clamav/clamd.conf" "$work_dir/clamd.conf"
cp "$root_dir/migrations/files/000020_signature_freshness.up.sql" "$work_dir/signature-policy.up.sql"
mkdir "$work_dir/output"
chmod 0777 "$work_dir/output"
# Optional second official signed snapshot proves an actual recurring upgrade.
if [[ -n "${CLAMAV_SIGNATURE_NEXT_IMAGE:-}" ]]; then
  if [[ ! "$CLAMAV_SIGNATURE_NEXT_IMAGE" =~ ^clamav/clamav(:1\.4(\.6)?)?@sha256:[a-f0-9]{64}$ ]]; then exit 2; fi
  docker image inspect "$CLAMAV_SIGNATURE_NEXT_IMAGE" >/dev/null
  fixture_container="nchat-signature-fixture-${work_dir##*.}"
  docker create --name "$fixture_container" --network none --entrypoint /bin/true "$CLAMAV_SIGNATURE_NEXT_IMAGE" >/dev/null
  mkdir "$work_dir/next-databases"
  for name in main daily bytecode; do
    docker cp "$fixture_container:/var/lib/clamav/$name.cvd" "$work_dir/next-databases/$name.cvd"
    chmod 0444 "$work_dir/next-databases/$name.cvd"
  done
  docker rm "$fixture_container" >/dev/null
  fixture_container=""
fi
for package in scanner signatures signatureguard; do
  CGO_ENABLED=0 go test -c -covermode=atomic -coverpkg=./services/file-service/internal/scanner,./services/file-service/internal/signatures,./services/file-service/internal/signatureguard -o "$work_dir/$package.test" "./services/file-service/internal/$package"
  docker run --rm --pull=never --network none --user 100:101 \
    --memory 3g --cpus 2 --pids-limit 64 --cap-drop ALL \
    --security-opt no-new-privileges --read-only \
    --tmpfs /tmp:rw,noexec,nosuid,size=2g,uid=100,gid=101,mode=700 \
    --mount "type=bind,source=$work_dir,target=/test,readonly" \
    --mount "type=bind,source=$work_dir/output,target=/output" \
    -e NCHAT_CLAMAV_ISOLATED_TEST=true --entrypoint "/test/$package.test" "$image" \
    -test.v -test.coverprofile="/output/$package.out"
  go tool cover -func="$work_dir/output/$package.out" | tail -1
done

awk -f scripts/ci/merge-go-coverprofiles.awk "$work_dir"/output/*.out > "$work_dir/merged.out"
go tool cover -func="$work_dir/merged.out"

# Actual guardian -> encrypted read -> service -> PGX UPDATE -> policy trigger.
postgres_image='postgres@sha256:31ba9c94a03ada9988f2bb493a22a75eef3a185884533e5cdbcd498ea835b08f'
docker image inspect "$postgres_image" >/dev/null
chain_container="nchat-signature-chain-${work_dir##*.}"
chain_volume="$chain_container-socket"
docker volume create "$chain_volume" >/dev/null
docker run -d --pull=never --name "$chain_container" --network none   --memory 512m --cpus 1 --pids-limit 128   --tmpfs /var/lib/postgresql/data:rw,nosuid,size=256m   --mount "type=volume,source=$chain_volume,target=/var/run/postgresql"   -e POSTGRES_HOST_AUTH_METHOD=trust -e POSTGRES_DB=signature_chain_test "$postgres_image" >/dev/null
for attempt in {1..100}; do
  if docker exec "$chain_container" pg_isready -h 127.0.0.1 -U postgres >/dev/null 2>&1; then break; fi
  sleep 0.1
done
docker exec "$chain_container" pg_isready -h 127.0.0.1 -U postgres >/dev/null
CGO_ENABLED=0 go test -c -o "$work_dir/service.test" ./services/file-service/internal/service
docker run --rm --pull=never --network none --user 100:101   --memory 3g --cpus 2 --pids-limit 64 --cap-drop ALL   --security-opt no-new-privileges --read-only   --tmpfs /tmp:rw,noexec,nosuid,size=2g,uid=100,gid=101,mode=700   --mount "type=bind,source=$work_dir,target=/test,readonly"   --mount "type=volume,source=$chain_volume,target=/pgsocket,readonly"   -e NCHAT_SIGNATURE_CHAIN_TEST=true --entrypoint /test/service.test "$image"   -test.run '^TestIsolatedSignatureChainPersistsOnlyFreshEvidence$' -test.v

if [[ -n "${CLAMAV_SIGNATURE_COVERAGE_PROFILE:-}" ]]; then
  mkdir -p "$(dirname "$CLAMAV_SIGNATURE_COVERAGE_PROFILE")"
  cp "$work_dir/merged.out" "$CLAMAV_SIGNATURE_COVERAGE_PROFILE"
fi
