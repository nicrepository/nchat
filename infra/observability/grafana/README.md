# Grafana

Local Grafana OSS instance for NChat observability.

## Access

- URL: http://localhost:3000
- Default credentials (dev only): `admin` / `admin`

## Provisioning

Datasources are provisioned automatically from `provisioning/datasources/datasources.yml`:

- **Prometheus** (uid: `prometheus`, default) — http://prometheus:9090
- **Jaeger** (uid: `jaeger`) — http://jaeger:16686

Dashboards are provisioned automatically from `provisioning/dashboards/dashboards.yml`,
loading JSON files from `dashboards/`:

| Dashboard      | UID              | Folder |
| -------------- | ---------------- | ------ |
| NChat Overview | `nchat-overview` | NChat  |

## Starting

```bash
make dev-observability-up
```

## Validating dashboard provisioning

```bash
make grafana-dashboard-check
```

## File antimalware (#1129 D1)

`NChat File Antimalware` (`nchat-file-antimalware`) uses the same Prometheus
source and file provider as Overview. It distinguishes local execution slots
from global PostgreSQL snapshots and displays absent telemetry as unknown.
Signature panels normally have no data until a separately approved B rollout
and authorized scrape. No engine effectiveness or client receipt is inferred
from HTTP availability or a successful event publication.

[Detailed contracts and runbook](../../../docs/observability/clamav-1129-delivery-d.md).
