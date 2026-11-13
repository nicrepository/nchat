# Prometheus alert rules

`file-antimalware.yml` is loaded by the existing local Compose Prometheus.
It provides fresh global queue recording rules and bounded diagnostic alerts.
Global queue gauges use **max of recent successful snapshots**, never sum;
local slots and counters may be summed. Terminal counters start at known zero.
No Alertmanager or notification receiver is provisioned by this change.

`../file-antimalware-candidates.yml` is deliberately **not loaded**. Its signature
rules require approved B activation, authorized scraping and an approved age
policy. Its latency rule is diagnostic for all sizes, not evidence of the
proposed benign <=10 MiB SLO. No Kubernetes scrape, ingress or policy is changed.

Validate through `make grafana-dashboard-check` (also in `make ci-infra-config`).
It runs real promtool rule/config checks and fixtures for idle, absent, stale,
replicated queue state, counter resets and stalled/growing backlog, plus every
provisioned dashboard query with no data. Missing promtool/Docker fails the
check; it is never a rule-validation PASS.

[Semantics, thresholds and operational response](../../../../docs/observability/clamav-1129-delivery-d.md).
