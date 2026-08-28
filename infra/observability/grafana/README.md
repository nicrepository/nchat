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
