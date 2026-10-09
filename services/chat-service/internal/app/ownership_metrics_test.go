package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nicrepository/nchat/libs/go/platform/observability"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
	"github.com/nicrepository/nchat/services/chat-service/internal/ws"
	pgxmock "github.com/pashagolub/pgxmock/v2"
)

type ownershipMetricsBus struct {
	ws.NopBus
	err error
}

func (b *ownershipMetricsBus) Publish(context.Context, ws.Event) error { return b.err }

// Exercise the worker's actual iteration: metrics must follow failed and
// retried delivery, rather than merely accepting manually supplied values.
func TestOwnershipOutboxMetrics(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	registry := observability.NewMetrics(observability.Config{ServiceName: "chat-service", MetricsEnabled: true})
	m := newOwnershipOutboxMetrics(registry)
	bus := &ownershipMetricsBus{err: errors.New("bus unavailable")}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	b := &bootstrap{logger: logger, stores: bootstrapStores{ownership: storage.NewPGXOwnershipStore(mock)}, realtime: bootstrapRealtime{hub: ws.NewHub(nil, logger, bus, "ownership-metrics")}}
	expectOwnershipMetricIteration(mock, false)
	b.dispatchOwnershipChanges(t.Context(), m)
	assertOwnershipMetrics(t, registry, "chat_ownership_outbox_pending 1", "chat_ownership_outbox_oldest_pending_age_seconds 120", "chat_ownership_outbox_publish_failures_total 1", "chat_ownership_outbox_retries_total 0")
	bus.err = nil
	expectOwnershipMetricIteration(mock, true)
	b.dispatchOwnershipChanges(t.Context(), m)
	assertOwnershipMetrics(t, registry, "chat_ownership_outbox_pending 0", "chat_ownership_outbox_oldest_pending_age_seconds 0", "chat_ownership_outbox_publish_failures_total 1", "chat_ownership_outbox_retries_total 1")
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	if newOwnershipOutboxMetrics(nil) != nil {
		t.Fatal("disabled metrics registered collectors")
	}
	var disabled *ownershipOutboxMetrics
	disabled.attempt(true, errors.New("unavailable"))
	b.observeOwnershipBacklog(t.Context(), disabled)
}

func expectOwnershipMetricIteration(mock pgxmock.PgxPoolIface, retry bool) {
	mock.ExpectQuery("FROM chat.ownership_outbox").WillReturnRows(pgxmock.NewRows([]string{"pending", "age"}).AddRow(int64(1), float64(120)))
	attempts := 0
	if retry {
		attempts = 1
	}
	mock.ExpectBegin()
	mock.ExpectQuery("FROM chat.ownership_outbox").WillReturnRows(pgxmock.NewRows([]string{"id", "workspace", "kind", "conversation", "attempts"}).AddRow(int64(1), "workspace", "dm", "conversation", attempts))
	ack := mock.ExpectExec("UPDATE chat.ownership_outbox")
	pending, age := int64(1), float64(120)
	if retry {
		ack.WithArgs(int64(1))
		pending, age = 0, 0
	} else {
		ack.WithArgs(int64(1), 1, "publish_failed")
	}
	ack.WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()
	mock.ExpectRollback()
	mock.ExpectBegin()
	mock.ExpectQuery("FROM chat.ownership_outbox").WillReturnRows(pgxmock.NewRows([]string{"id", "workspace", "kind", "conversation", "attempts"}))
	mock.ExpectRollback()
	mock.ExpectQuery("FROM chat.ownership_outbox").WillReturnRows(pgxmock.NewRows([]string{"pending", "age"}).AddRow(pending, age))
}

func assertOwnershipMetrics(t *testing.T, registry *observability.Metrics, expected ...string) {
	t.Helper()
	recorder := httptest.NewRecorder()
	registry.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := recorder.Body.String()
	for _, want := range expected {
		if !strings.Contains(body, want) {
			t.Fatalf("missing label-free metric %q", want)
		}
	}
}
