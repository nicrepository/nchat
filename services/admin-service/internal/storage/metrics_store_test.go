package storage_test

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	pgxmock "github.com/pashagolub/pgxmock/v2"

	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
	"github.com/nicrepository/nchat/services/admin-service/internal/storage"
)

func metricsColumns() []string {
	return []string{
		"users_total", "users_active_now", "users_active_24h",
		"channels_active", "groups_active", "direct_active",
		"messages_24h", "calls_active", "uploads_24h",
		"files_blocked_24h", "links_blocked_24h", "storage_bytes",
	}
}

func TestPlatformCountersReadsEveryDashboardCounterInOneRoundTrip(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`WITH window_start AS`).
		WillReturnRows(pgxmock.NewRows(metricsColumns()).
			AddRow(int64(12), int64(3), int64(7), int64(5), int64(2), int64(9),
				int64(431), int64(1), int64(8), int64(2), int64(4), int64(1<<30)))

	counters, err := storage.NewPGXMetricsStore(mock).PlatformCounters(context.Background())
	if err != nil {
		t.Fatalf("PlatformCounters: %v", err)
	}
	if counters.UsersTotal != 12 || counters.Messages24h != 431 || counters.StorageBytes != 1<<30 {
		t.Fatalf("unexpected counters: %+v", counters)
	}
	// One statement, not twelve. The dashboard is one request and it must stay
	// one request against the database too, or the N+1 just moves a layer down.
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestPlatformCountersHidesTheDriversMessage(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`WITH window_start AS`).
		WillReturnError(errors.New(`failed to connect to host=10.0.0.5 user=nchat database=nchat: password authentication failed`))

	_, err = storage.NewPGXMetricsStore(mock).PlatformCounters(context.Background())
	if !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
	if strings.Contains(err.Error(), "10.0.0.5") || strings.Contains(err.Error(), "password") {
		t.Fatalf("the driver's message survived: %v", err)
	}
}

func TestPlatformCountersWithoutAPoolIsUnavailable(t *testing.T) {
	if _, err := storage.NewPGXMetricsStore(nil).PlatformCounters(context.Background()); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
	var store *storage.PGXMetricsStore
	if _, err := store.PlatformCounters(context.Background()); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
}

// The dashboard reports volume, never content. This asserts the property where
// it is actually decided — in the SQL — rather than trusting the mapping layer
// above it not to expose what the query selected.
func TestPlatformCountersSelectsNoContentBearingColumn(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer mock.Close()

	var executed string
	mock.ExpectQuery(`WITH window_start AS`).
		WillReturnRows(pgxmock.NewRows(metricsColumns()).
			AddRow(int64(0), int64(0), int64(0), int64(0), int64(0), int64(0),
				int64(0), int64(0), int64(0), int64(0), int64(0), int64(0)))
	_, _ = storage.NewPGXMetricsStore(recordingPool{mock, &executed}).PlatformCounters(context.Background())

	forbidden := []string{
		"body_text", "original_filename", "email", "display_name",
		"storage_object_key", "wrapped_dek", "title", "url",
	}
	for _, column := range forbidden {
		if strings.Contains(executed, column) {
			t.Errorf("the aggregate selects %q, which is content and not volume", column)
		}
	}
	// Every projected expression must be an aggregate. A bare column would be
	// a row of somebody's data on an operational dashboard.
	aggregate := regexp.MustCompile(`(?i)\((?:\s|\n)*SELECT\s+(count|COALESCE\(sum)`)
	if matches := aggregate.FindAllString(executed, -1); len(matches) != len(metricsColumns()) {
		t.Errorf("expected %d aggregate subqueries, found %d", len(metricsColumns()), len(matches))
	}
}

// recordingPool captures the statement the store sends before delegating it,
// so a spec can assert what the SQL projects rather than only what the Go
// mapping does with the result.
type recordingPool struct {
	pgxmock.PgxPoolIface
	executed *string
}

func (p recordingPool) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	*p.executed = sql
	return p.PgxPoolIface.QueryRow(ctx, sql, args...)
}
