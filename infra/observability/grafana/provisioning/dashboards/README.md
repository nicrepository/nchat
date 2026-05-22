# Grafana Dashboards Provisioning

This directory holds Grafana dashboard provisioning configuration.

## dashboards.yml

Instructs Grafana to load all JSON files from `/var/lib/grafana/dashboards` (mounted
from `infra/observability/grafana/dashboards/`) into the **NChat** folder.

## Dashboard files

Dashboard JSON files live in `infra/observability/grafana/dashboards/`:

| File                  | Title          | UID              |
| --------------------- | -------------- | ---------------- |
| `nchat-overview.json` | NChat Overview | `nchat-overview` |

## Adding dashboards

1. Export the dashboard JSON from Grafana (Share → Export → Save to file).
2. Place the JSON file in `infra/observability/grafana/dashboards/`.
3. Run `make grafana-dashboard-check` to validate.
4. Commit and open a PR.
