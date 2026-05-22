# Grafana Dashboard Definitions

Grafana dashboard JSON files for NChat. These files are provisioned automatically into
the **NChat** folder when Grafana starts.

## Files

| File                  | Title          | UID              | Description                                           |
| --------------------- | -------------- | ---------------- | ----------------------------------------------------- |
| `nchat-overview.json` | NChat Overview | `nchat-overview` | Service health, traffic, latency, errors, concurrency |

## Provisioning

Grafana loads these files via `provisioning/dashboards/dashboards.yml`, which mounts
this directory at `/var/lib/grafana/dashboards` inside the container.

## Adding or updating dashboards

1. Modify the JSON directly, or export from Grafana (Share → Export → Save to file).
2. Run `make grafana-dashboard-check` to validate.
3. Commit and open a PR to `develop`.

## PromQL queries used

| Panel              | Query                                                                                                              |
| ------------------ | ------------------------------------------------------------------------------------------------------------------ |
| Services up        | `up{job="nchat-go-services"}`                                                                                      |
| Service inventory  | `nchat_service_info`                                                                                               |
| Request rate       | `sum by (service) (rate(nchat_http_requests_total[5m]))`                                                           |
| Latency p95        | `histogram_quantile(0.95, sum by (le, service) (rate(nchat_http_request_duration_seconds_bucket[5m])))`            |
| 5xx error rate     | `sum by (service) (rate(nchat_http_requests_total{status=~"5.."}[5m]))`                                            |
| 5xx error ratio    | `sum(rate(nchat_http_requests_total{status=~"5.."}[5m])) / clamp_min(sum(rate(nchat_http_requests_total[5m])), 1)` |
| In-flight requests | `sum by (service) (nchat_http_in_flight_requests)`                                                                 |
