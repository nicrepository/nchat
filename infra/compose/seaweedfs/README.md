# SeaweedFS local development

The local Compose stack runs SeaweedFS as a provisional Sprint 0 storage service.

## Endpoints

- Master: `http://localhost:9333`
- Volume: `http://localhost:8088`
- Filer: `http://localhost:8888`
- S3 gateway: `http://localhost:8333`

Ports can be changed in `infra/compose/.env.dev`.

## Configured

- Single SeaweedFS master.
- Single volume server.
- Single filer.
- Single S3 gateway.
- Named Docker volumes for local persistence.
- Smoke validation through the filer HTTP API.

## Not Configured

- Production S3 authentication and policies.
- TLS.
- Multi-node replication or HA.
- Scheduled backup and restore.
- Large upload validation.
- Node failure and recovery validation.

## Smoke Test

Run:

```bash
make dev-env-validate
```

The validation checks master and filer HTTP endpoints, uploads a small file through the
filer, downloads it back, compares the content and then removes it.

## Decision Status

SeaweedFS is provisional for Sprint 0. The final storage decision depends on the full
validation planned by the end of Sprint 3, including large upload, preview, replication,
backup/restore, node failure and recovery behavior.
