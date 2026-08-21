package service_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
	"github.com/nicrepository/nchat/services/admin-service/internal/service"
)

type fakeAuditStore struct {
	appended   []domain.AuditEvent
	appendErr  error
	entries    []domain.AuditEntry
	listErr    error
	lastLimit  int
	listCalled bool
}

func (f *fakeAuditStore) AppendAudit(_ context.Context, event domain.AuditEvent) error {
	f.appended = append(f.appended, event)
	return f.appendErr
}

func (f *fakeAuditStore) ListAuditEvents(_ context.Context, limit int) ([]domain.AuditEntry, error) {
	f.listCalled = true
	f.lastLimit = limit
	return f.entries, f.listErr
}

func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func TestAuditService_RecordWritesTheEvent(t *testing.T) {
	store := &fakeAuditStore{}
	audit := service.NewAuditService(store, discardLogger())

	audit.Record(context.Background(), domain.AuditEvent{
		ActorUserID: "user-1",
		Action:      domain.AuditActionSessionCreate,
		Result:      domain.AuditResultSuccess,
	})

	if len(store.appended) != 1 || store.appended[0].Action != domain.AuditActionSessionCreate {
		t.Fatalf("expected one recorded event, got %+v", store.appended)
	}
}

// The trail is evidence, not a precondition. A failing audit write must not
// turn a database hiccup into an administrator who cannot end their session.
func TestAuditService_RecordSwallowsStoreFailures(t *testing.T) {
	store := &fakeAuditStore{appendErr: errors.New("table missing")}
	audit := service.NewAuditService(store, discardLogger())

	audit.Record(context.Background(), domain.AuditEvent{Action: "x", Result: domain.AuditResultError})
}

// A cancelled request must still leave its trail: the write runs on a detached
// context, so an aborted browser request cannot erase the record of what it did.
func TestAuditService_RecordSurvivesACancelledRequestContext(t *testing.T) {
	store := &fakeAuditStore{}
	audit := service.NewAuditService(store, discardLogger())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	audit.Record(ctx, domain.AuditEvent{Action: "x", Result: domain.AuditResultDenied})

	if len(store.appended) != 1 {
		t.Fatalf("expected the event to be written anyway, got %+v", store.appended)
	}
}

func TestAuditService_NilServiceAndStoreAreNoOps(t *testing.T) {
	var nilService *service.AuditService
	nilService.Record(context.Background(), domain.AuditEvent{})

	audit := service.NewAuditService(nil, nil)
	audit.Record(context.Background(), domain.AuditEvent{})
	if _, err := audit.List(context.Background(), 10); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
}

func TestAuditService_ListClampsTheLimit(t *testing.T) {
	store := &fakeAuditStore{}
	audit := service.NewAuditService(store, discardLogger())

	for _, tt := range []struct{ requested, want int }{
		{0, 50}, {-5, 50}, {10, 10}, {200, 200}, {5000, 200},
	} {
		if _, err := audit.List(context.Background(), tt.requested); err != nil {
			t.Fatalf("List(%d): %v", tt.requested, err)
		}
		if store.lastLimit != tt.want {
			t.Fatalf("List(%d): expected clamp to %d, got %d", tt.requested, tt.want, store.lastLimit)
		}
	}
}

func TestClampAuditLimit(t *testing.T) {
	if service.ClampAuditLimit(0) != 50 || service.ClampAuditLimit(1) != 1 || service.ClampAuditLimit(1000) != 200 {
		t.Fatal("unexpected clamping")
	}
}

func TestAuditService_ListPropagatesStoreErrors(t *testing.T) {
	store := &fakeAuditStore{listErr: errors.New("boom")}
	audit := service.NewAuditService(store, discardLogger())

	if _, err := audit.List(context.Background(), 10); err == nil {
		t.Fatal("expected the store error to surface")
	}
}
