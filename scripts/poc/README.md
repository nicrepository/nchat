# PoC Scripts

Local proof-of-concept validation scripts. These scripts require a running Docker
environment and are **not** executed in CI (containers are not available there).

## SeaweedFS PoC (TASK-15)

```bash
make poc-seaweedfs
# or
pnpm poc:seaweedfs
```

Validates upload, download, SHA-256 integrity, basic latency and replication using
a second volume server (profile `seaweed-replication`).

Results written to `poc-results/seaweedfs/` (gitignored).

## Valkey PoC (TASK-16)

```bash
make poc-valkey
# or
pnpm poc:valkey
```

Validates Pub/Sub, Streams (XADD/XREAD/XRANGE), SETNX lock, TTL/EXPIRE, sliding
window rate limiting and basic operation latency.

Results written to `poc-results/valkey/` (gitignored).

## CI config check

The lightweight CI check (`scripts/ci/poc-config-check.sh`) validates that:

- Scripts exist and are executable
- Script syntax is valid (`bash -n`)
- `poc-results/` is gitignored
- Docker Compose has the `seaweed-volume-2` service with profile `seaweed-replication`

No containers are started in CI.
