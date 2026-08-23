package httpapi

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
	"github.com/nicrepository/nchat/services/admin-service/internal/service"
)

// The observability surface of the Admin API (issue #581).
//
// Three endpoints and one shared snapshot. What is *not* here is the part
// worth reading: no endpoint accepts a URL, a hostname, an IP address, a port,
// a DSN, a namespace or a path. The refresh has no request body at all, and
// the only input the listing takes is an optional service filter resolved
// against the closed registry in domain — an identifier the platform declared,
// never a destination the caller supplied.
//
// That is what keeps the Health Center from becoming an SSRF primitive: the
// server already knows every address it is willing to contact, because it read
// them from its own environment at collection time.

// DashboardAdmin is the summary the landing page reads.
type DashboardAdmin interface {
	Summary(ctx context.Context) (service.DashboardSummary, error)
}

// HealthAdmin is the collection the Health Center lists.
//
// `force` is the only argument, and it is a boolean: there is deliberately no
// parameter naming what to check, because the set of things this service is
// willing to check is a compile-time registry.
type HealthAdmin interface {
	Snapshot(ctx context.Context, force bool) (domain.HealthSnapshot, error)
}

// ObservabilityPorts groups the two halves of the surface, wired together
// because they are enabled together: the dashboard reads the same collection
// the Health Center lists, and a deployment that had one without the other
// would serve a summary nobody can drill into.
type ObservabilityPorts struct {
	Dashboard DashboardAdmin
	Health    HealthAdmin
}

// NewObservabilityPorts wires the dashboard and health services into the
// router.
func NewObservabilityPorts(dashboard DashboardAdmin, health HealthAdmin) *ObservabilityPorts {
	return &ObservabilityPorts{Dashboard: dashboard, Health: health}
}

// serviceHealthPayload is one dependency as the console renders it.
//
// Every field is either a constant from the registry or a value the service
// layer already sanitized. In particular there is no host, no port, no
// endpoint, no credential status derived from a secret's value, and no text
// produced by the dependency itself except `version`, which passed through a
// character allowlist and a length cap first.
type serviceHealthPayload struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Category    string `json:"category"`
	// Impact is what breaks for users while this dependency is not healthy.
	Impact string `json:"impact"`
	State  string `json:"state"`
	// Enabled is the deployment's switch. It is reported next to the state and
	// never instead of it: an integration can be enabled and unavailable, and
	// the console must be able to say so.
	Enabled bool `json:"enabled"`
	// Observable reports whether this pod receives the configuration naming
	// the endpoint. False is why a state is unknown, and it is deliberately a
	// different fact from "not configured".
	Observable bool `json:"observable"`
	// Critical marks a dependency whose loss takes the platform down rather
	// than degrading one feature.
	Critical bool `json:"critical"`
	// LatencyMS is absent when no round trip was measured. It is never zero as
	// a stand-in: a disabled integration has no latency, and reporting 0 ms
	// would be reporting a check that did not happen.
	LatencyMS     *int64    `json:"latency_ms,omitempty"`
	CheckedAt     time.Time `json:"checked_at"`
	ErrorCategory string    `json:"error_category,omitempty"`
	Detail        string    `json:"detail,omitempty"`
	Version       string    `json:"version,omitempty"`
	// ConfigKey and RunbookPath are static destinations from the registry, not
	// URLs and never derived from a response.
	ConfigKey   string `json:"config_key,omitempty"`
	RunbookPath string `json:"runbook_path,omitempty"`
}

type healthAlertPayload struct {
	ServiceID   string    `json:"service_id"`
	Severity    string    `json:"severity"`
	Title       string    `json:"title"`
	Impact      string    `json:"impact"`
	Action      string    `json:"action"`
	Since       time.Time `json:"since"`
	RunbookPath string    `json:"runbook_path,omitempty"`
	ConfigKey   string    `json:"config_key,omitempty"`
}

type platformMetricPayload struct {
	Key        string `json:"key"`
	Label      string `json:"label"`
	Definition string `json:"definition"`
	Window     string `json:"window"`
	Unit       string `json:"unit"`
	// Value is absent when the aggregate did not run. Absent and zero are
	// different answers and the console renders them differently.
	Value     *int64 `json:"value,omitempty"`
	Available bool   `json:"available"`
}

type healthSummaryPayload struct {
	CollectedAt      time.Time               `json:"collected_at"`
	Overall          string                  `json:"overall"`
	StateCounts      map[string]int          `json:"state_counts"`
	Metrics          []platformMetricPayload `json:"metrics"`
	MetricsAvailable bool                    `json:"metrics_available"`
	Alerts           []healthAlertPayload    `json:"alerts"`
}

// GetOverview serves the dashboard in one request.
//
// Requires admin.infrastructure.read: it is the operational view of the
// platform, which is the capability the console's navigation map has declared
// for the Health Center and the system section since the foundation issue.
// Aggregate counters only — no identifier, no message, no filename and no URL
// is in the payload, so the endpoint reveals volume and never content.
func GetOverview(ports *ObservabilityPorts) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ports == nil || ports.Dashboard == nil {
			writeUnavailable(w)
			return
		}
		summary, err := ports.Dashboard.Summary(r.Context())
		if err != nil {
			writeDomainError(w, err)
			return
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"summary": newHealthSummaryPayload(summary)})
	})
}

// ListHealthChecks serves the Health Center's table.
//
// The one accepted parameter is `service`, and it is resolved against the
// registry before it is used for anything: an identifier the platform does not
// declare is a 400, not a filter that matches nothing and not — under any
// circumstance — an address. Filtering by state is deliberately left to the
// client: the payload is a dozen rows, so a server-side state filter would add
// a parameter and a round trip to save nothing.
func ListHealthChecks(ports *ObservabilityPorts) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveHealthSnapshot(w, r, ports, false)
	})
}

// RefreshHealth forces a new collection and returns it.
//
// A POST, so it passes the origin and CSRF guards like every other non-safe
// method, and it takes no body at all — there is nothing in a refresh for a
// caller to parameterise. Abuse is bounded in the service rather than here:
// forced collections share one in-flight run and obey a minimum interval, so a
// held-down button costs one collection per interval regardless of how many
// requests arrive.
func RefreshHealth(ports *ObservabilityPorts) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveHealthSnapshot(w, r, ports, true)
	})
}

func serveHealthSnapshot(w http.ResponseWriter, r *http.Request, ports *ObservabilityPorts, force bool) {
	if ports == nil || ports.Health == nil {
		writeUnavailable(w)
		return
	}
	filter, ok := parseHealthServiceFilter(w, r)
	if !ok {
		return
	}
	snapshot, err := ports.Health.Snapshot(r.Context(), force)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"collected_at": snapshot.CollectedAt,
		"overall":      string(snapshot.Overall()),
		"services":     newServiceHealthPayloads(snapshot, filter),
	})
}

// parseHealthServiceFilter resolves the optional service filter.
//
// This is the whole of the client's influence over the health surface, and it
// is a lookup into a compile-time registry. An empty value means "every
// service"; anything the registry does not hold is refused. There is no branch
// that treats the parameter as anything other than a key.
func parseHealthServiceFilter(w http.ResponseWriter, r *http.Request) (domain.HealthServiceID, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get("service"))
	if raw == "" {
		return "", true
	}
	if _, known := domain.LookupHealthService(domain.HealthServiceID(raw)); !known {
		writeInvalidQuery(w)
		return "", false
	}
	return domain.HealthServiceID(raw), true
}

// newServiceHealthPayloads maps the snapshot, ordered by how much attention
// each row demands and then by registry order.
//
// Sorting server-side gives every client the same default view; the console
// still re-sorts locally when an operator picks a column, which needs no round
// trip for a dozen rows.
func newServiceHealthPayloads(snapshot domain.HealthSnapshot, filter domain.HealthServiceID) []serviceHealthPayload {
	services := make([]domain.ServiceHealth, 0, len(snapshot.Services))
	for _, service := range snapshot.Services {
		if filter == "" || service.Descriptor.ID == filter {
			services = append(services, service)
		}
	}
	sort.SliceStable(services, func(i, j int) bool {
		return domain.HealthStateRank(services[i].State) > domain.HealthStateRank(services[j].State)
	})
	payloads := make([]serviceHealthPayload, 0, len(services))
	for _, service := range services {
		payloads = append(payloads, newServiceHealthPayload(service))
	}
	return payloads
}

func newServiceHealthPayload(health domain.ServiceHealth) serviceHealthPayload {
	return serviceHealthPayload{
		ID:            string(health.Descriptor.ID),
		DisplayName:   health.Descriptor.DisplayName,
		Category:      string(health.Descriptor.Category),
		Impact:        health.Descriptor.Description,
		State:         string(health.State),
		Enabled:       health.Enabled,
		Observable:    health.Observable,
		Critical:      health.Descriptor.Critical,
		LatencyMS:     health.LatencyMS,
		CheckedAt:     health.CheckedAt,
		ErrorCategory: string(health.ErrorCategory),
		Detail:        health.Detail,
		Version:       health.Version,
		ConfigKey:     string(health.Descriptor.ConfigKey),
		RunbookPath:   health.Descriptor.RunbookPath,
	}
}

func newHealthSummaryPayload(summary service.DashboardSummary) healthSummaryPayload {
	counts := make(map[string]int, len(summary.StateCounts))
	for state, count := range summary.StateCounts {
		counts[string(state)] = count
	}
	return healthSummaryPayload{
		CollectedAt:      summary.CollectedAt,
		Overall:          string(summary.Overall),
		StateCounts:      counts,
		Metrics:          newMetricPayloads(summary.Metrics),
		MetricsAvailable: summary.MetricsAvailable,
		Alerts:           newAlertPayloads(summary.Alerts),
	}
}

func newMetricPayloads(metrics []domain.PlatformMetric) []platformMetricPayload {
	payloads := make([]platformMetricPayload, 0, len(metrics))
	for _, metric := range metrics {
		payload := platformMetricPayload{
			Key:        string(metric.Definition.Key),
			Label:      metric.Definition.Label,
			Definition: metric.Definition.Definition,
			Window:     string(metric.Definition.Window),
			Unit:       string(metric.Definition.Unit),
			Available:  metric.Available,
		}
		if metric.Available {
			value := metric.Value
			payload.Value = &value
		}
		payloads = append(payloads, payload)
	}
	return payloads
}

func newAlertPayloads(alerts []domain.HealthAlert) []healthAlertPayload {
	payloads := make([]healthAlertPayload, 0, len(alerts))
	for _, alert := range alerts {
		payloads = append(payloads, healthAlertPayload{
			ServiceID:   string(alert.ServiceID),
			Severity:    string(alert.Severity),
			Title:       alert.Title,
			Impact:      alert.Impact,
			Action:      alert.Action,
			Since:       alert.Since,
			RunbookPath: alert.RunbookPath,
			ConfigKey:   string(alert.ConfigKey),
		})
	}
	return payloads
}
