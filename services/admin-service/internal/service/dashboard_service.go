package service

import (
	"context"
	"time"

	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
)

// The dashboard summary (issue #581).
//
// This service exists so the console makes one request and not one per card.
// It composes two independent sources — the health snapshot and the aggregate
// counters — and it is deliberately tolerant of one of them failing: a
// database that cannot answer the counters must not blank the page that would
// have told the operator the database is the problem.

// MetricsReader is the aggregate read the dashboard needs. One method, one
// round trip, no predicate: there is nothing here a caller can aim.
type MetricsReader interface {
	PlatformCounters(ctx context.Context) (domain.PlatformCounters, error)
}

// HealthReader is the health collection the dashboard shares with the Health
// Center.
type HealthReader interface {
	Snapshot(ctx context.Context, force bool) (domain.HealthSnapshot, error)
}

// DashboardSummary is everything the landing page renders.
type DashboardSummary struct {
	// CollectedAt is when the health snapshot was taken. It is the timestamp
	// the console shows as "última atualização", so it describes the data and
	// not the moment the request happened to arrive.
	CollectedAt time.Time
	Overall     domain.HealthState
	StateCounts map[domain.HealthState]int
	Metrics     []domain.PlatformMetric
	// MetricsAvailable reports whether the aggregate ran. False means every
	// metric is unavailable — never zero.
	MetricsAvailable bool
	Alerts           []domain.HealthAlert
}

// DashboardService assembles the summary.
type DashboardService struct {
	health  HealthReader
	metrics MetricsReader
	observe *HealthMetrics
}

func NewDashboardService(health HealthReader, metrics MetricsReader, observe *HealthMetrics) *DashboardService {
	return &DashboardService{health: health, metrics: metrics, observe: observe}
}

// Summary builds the dashboard.
//
// The health snapshot is the structural half: without it there is no overall
// state, no counters and no alerts, so a failure there fails the request. The
// counters are the tolerant half: their failure produces a summary whose cards
// say "indisponível", which is the honest rendering and the one that keeps the
// health section visible.
func (s *DashboardService) Summary(ctx context.Context) (DashboardSummary, error) {
	if s == nil || s.health == nil {
		return DashboardSummary{}, domain.ErrUnavailable
	}
	started := time.Now()
	snapshot, err := s.health.Snapshot(ctx, false)
	if err != nil {
		return DashboardSummary{}, err
	}
	counters, available := s.counters(ctx)
	s.observe.recordDashboard(available, time.Since(started))

	return DashboardSummary{
		CollectedAt:      snapshot.CollectedAt,
		Overall:          snapshot.Overall(),
		StateCounts:      snapshot.CountByState(),
		Metrics:          domain.PlatformMetrics(counters, available),
		MetricsAvailable: available,
		Alerts:           domain.DeriveAlerts(snapshot),
	}, nil
}

// counters reads the aggregate, reporting availability rather than an error.
//
// The error is deliberately dropped here and not logged with its text: the
// store already reduced it to domain.ErrUnavailable precisely so a driver
// message carrying the DSN cannot travel any further. What the caller needs is
// the boolean.
func (s *DashboardService) counters(ctx context.Context) (domain.PlatformCounters, bool) {
	if s.metrics == nil {
		return domain.PlatformCounters{}, false
	}
	counters, err := s.metrics.PlatformCounters(ctx)
	if err != nil {
		return domain.PlatformCounters{}, false
	}
	return counters, true
}
