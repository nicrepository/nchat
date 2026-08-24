package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
	"github.com/nicrepository/nchat/services/admin-service/internal/service"
)

type stubHealthReader struct {
	snapshot domain.HealthSnapshot
	err      error
	calls    int
}

func (s *stubHealthReader) Snapshot(_ context.Context, _ bool) (domain.HealthSnapshot, error) {
	s.calls++
	return s.snapshot, s.err
}

type stubMetricsReader struct {
	counters domain.PlatformCounters
	err      error
}

func (s *stubMetricsReader) PlatformCounters(_ context.Context) (domain.PlatformCounters, error) {
	return s.counters, s.err
}

func healthSnapshotWith(states ...domain.HealthState) domain.HealthSnapshot {
	services := make([]domain.ServiceHealth, 0, len(states))
	for index, state := range states {
		services = append(services, domain.ServiceHealth{
			Descriptor: domain.HealthServiceDescriptor{
				ID:          domain.HealthServiceID("service_" + string(rune('a'+index))),
				DisplayName: "Service",
				Description: "Impacto.",
				RunbookPath: "docs/runbooks/example.md",
			},
			State:     state,
			CheckedAt: time.Unix(1700000000, 0).UTC(),
		})
	}
	return domain.HealthSnapshot{CollectedAt: time.Unix(1700000000, 0).UTC(), Services: services}
}

func TestSummaryComposesHealthAndCountersInOneRead(t *testing.T) {
	health := &stubHealthReader{snapshot: healthSnapshotWith(domain.HealthHealthy, domain.HealthDisabled)}
	metrics := &stubMetricsReader{counters: sampleCounters()}
	dashboard := service.NewDashboardService(health, metrics, service.NewHealthMetrics())

	summary, err := dashboard.Summary(context.Background())
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if summary.Overall != domain.HealthHealthy {
		t.Fatalf("expected healthy, got %s", summary.Overall)
	}
	if !summary.MetricsAvailable {
		t.Fatal("the counters were available and must be reported as such")
	}
	if len(summary.Metrics) != len(domain.MetricDefinitions()) {
		t.Fatalf("expected every declared metric, got %d", len(summary.Metrics))
	}
	if !summary.CollectedAt.Equal(health.snapshot.CollectedAt) {
		t.Fatal("the summary must age from the collection, not from the request")
	}
	// One request, one health collection: the dashboard must not trigger a
	// second one on top of the Health Center's.
	if health.calls != 1 {
		t.Fatalf("expected one health read, got %d", health.calls)
	}
}

func sampleCounters() domain.PlatformCounters {
	return domain.PlatformCounters{
		UsersTotal: 12, UsersActiveNow: 3, UsersActive24h: 7,
		ChannelsActive: 5, GroupsActive: 2, DirectActive: 9,
		Messages24h: 431, CallsActive: 1, Uploads24h: 8,
		FilesBlocked24h: 2, LinksBlocked24h: 4, StorageBytes: 1 << 30,
	}
}

// The partial failure the dashboard is designed around: the database cannot
// answer the counters, so the page must still render the health section that
// would tell the operator the database is the problem.
func TestUnavailableCountersDoNotBlankTheDashboard(t *testing.T) {
	health := &stubHealthReader{snapshot: healthSnapshotWith(domain.HealthUnavailable)}
	metrics := &stubMetricsReader{err: domain.ErrUnavailable}
	dashboard := service.NewDashboardService(health, metrics, service.NewHealthMetrics())

	summary, err := dashboard.Summary(context.Background())
	if err != nil {
		t.Fatalf("a failed aggregate must not fail the request: %v", err)
	}
	if summary.MetricsAvailable {
		t.Fatal("the counters failed and must not be reported as available")
	}
	for _, metric := range summary.Metrics {
		if metric.Available || metric.Value != 0 {
			t.Fatalf("%s was reported despite the failed aggregate", metric.Definition.Key)
		}
	}
	if len(summary.Alerts) == 0 {
		t.Fatal("the health section must still be there; it is what explains the failure")
	}
}

// The health snapshot is the structural half. Without it there is no overall
// state, no counters by state and no alerts, so there is no summary to serve.
func TestAFailedHealthCollectionFailsTheSummary(t *testing.T) {
	health := &stubHealthReader{err: domain.ErrUnavailable}
	dashboard := service.NewDashboardService(health, &stubMetricsReader{}, service.NewHealthMetrics())

	if _, err := dashboard.Summary(context.Background()); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
}

func TestSummaryWithoutAMetricsReaderReportsTheCountersAsUnavailable(t *testing.T) {
	health := &stubHealthReader{snapshot: healthSnapshotWith(domain.HealthHealthy)}
	dashboard := service.NewDashboardService(health, nil, service.NewHealthMetrics())

	summary, err := dashboard.Summary(context.Background())
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if summary.MetricsAvailable {
		t.Fatal("a dashboard with no counter source must say so rather than report zeros")
	}
}

func TestSummaryOnANilServiceIsUnavailable(t *testing.T) {
	var dashboard *service.DashboardService
	if _, err := dashboard.Summary(context.Background()); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
}
