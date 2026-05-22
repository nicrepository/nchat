# Grafana

Local Grafana OSS instance for NChat observability.

## Access

- URL: http://localhost:3000
- Default credentials (dev only): `admin` / `admin`

## Provisioning

Datasources are provisioned automatically from `provisioning/datasources/datasources.yml`:

- **Prometheus** (default) — http://prometheus:9090
- **Jaeger** — http://jaeger:16686

Dashboards will be added in **TASK-19**.

## Starting

```bash
make dev-observability-up
```
