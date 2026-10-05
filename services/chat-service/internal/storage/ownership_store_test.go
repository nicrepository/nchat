package storage_test

import (
	"context"
	"errors"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
	pgxmock "github.com/pashagolub/pgxmock/v2"
	"testing"
)

// Failures before admission must roll back without changing roles or leaving
// an open transaction. The original failure stays visible to the caller.
func TestOwnershipStorageAdmissionFailures(t *testing.T) {
	failure := errors.New("database unavailable")
	for _, phase := range []string{"begin", "isolation", "lock", "snapshot"} {
		t.Run(phase, func(t *testing.T) {
			mock, err := pgxmock.NewPool()
			if err != nil {
				t.Fatal(err)
			}
			defer mock.Close()
			expectOwnershipAdmissionFailure(mock, phase, failure)
			store := storage.NewPGXOwnershipStore(mock)
			_, err = store.Mutate(context.Background(), storage.OwnershipMutation{Scope: storage.OwnershipScope{Kind: "dm"}, Operation: "leave"})
			if !errors.Is(err, failure) {
				t.Fatalf("original failure lost: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func expectOwnershipAdmissionFailure(mock pgxmock.PgxPoolIface, phase string, failure error) {
	if phase == "begin" {
		mock.ExpectBegin().WillReturnError(failure)
		return
	}
	mock.ExpectBegin()
	isolation := mock.ExpectExec("SET TRANSACTION ISOLATION LEVEL SERIALIZABLE")
	if phase == "isolation" {
		isolation.WillReturnError(failure)
	} else {
		isolation.WillReturnResult(pgxmock.NewResult("SET", 0))
		lock := mock.ExpectExec("SELECT chat.lock_ownership_conversation").WithArgs("dm", "")
		if phase == "lock" {
			lock.WillReturnError(failure)
		} else {
			lock.WillReturnResult(pgxmock.NewResult("SELECT", 1))
			mock.ExpectQuery("SELECT enabled FROM chat.ownership_rollout").WillReturnError(failure)
		}
	}
	mock.ExpectRollback()
}

func TestOwnershipDetailsUnavailableDoesNotLeakProjection(t *testing.T) {
	failure := errors.New("database unavailable")
	for _, phase := range []string{"begin", "isolation", "snapshot"} {
		t.Run(phase, func(t *testing.T) {
			mock, err := pgxmock.NewPool()
			if err != nil {
				t.Fatal(err)
			}
			defer mock.Close()
			expectOwnershipDetailsFailure(mock, phase, failure)
			result, err := storage.NewPGXOwnershipStore(mock).Details(t.Context(), storage.OwnershipScope{})
			if !errors.Is(err, failure) || result.Enabled || len(result.Members) > 0 {
				t.Fatalf("projection=%+v error=%v", result, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func expectOwnershipDetailsFailure(mock pgxmock.PgxPoolIface, phase string, failure error) {
	if phase == "begin" {
		mock.ExpectBegin().WillReturnError(failure)
		return
	}
	mock.ExpectBegin()
	isolation := mock.ExpectExec("SET TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY")
	if phase == "isolation" {
		isolation.WillReturnError(failure)
	} else {
		isolation.WillReturnResult(pgxmock.NewResult("SET", 0))
		mock.ExpectQuery("SELECT enabled FROM chat.ownership_rollout").WillReturnError(failure)
	}
	mock.ExpectRollback()
}

func TestOwnershipOutboxFailureLeavesDeliveryPending(t *testing.T) {
	failure := errors.New("database unavailable")
	for _, phase := range []string{"begin", "query", "row"} {
		t.Run(phase, func(t *testing.T) {
			mock, err := pgxmock.NewPool()
			if err != nil {
				t.Fatal(err)
			}
			defer mock.Close()
			expectOwnershipOutboxFailure(mock, phase, failure)
			publishCalled := false
			err = storage.NewPGXOwnershipStore(mock).DispatchOwnershipChanges(t.Context(), func(context.Context, string, string, string) error { publishCalled = true; return nil })
			if !errors.Is(err, failure) || publishCalled {
				t.Fatalf("published=%v error=%v", publishCalled, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func expectOwnershipOutboxFailure(mock pgxmock.PgxPoolIface, phase string, failure error) {
	if phase == "begin" {
		mock.ExpectBegin().WillReturnError(failure)
		return
	}
	mock.ExpectBegin()
	query := mock.ExpectQuery("FROM chat.ownership_outbox")
	if phase == "query" {
		query.WillReturnError(failure)
	} else {
		query.WillReturnRows(pgxmock.NewRows([]string{"id", "workspace", "kind", "conversation"}).AddRow(int64(1), "ws", "dm", "id").RowError(0, failure))
	}
	mock.ExpectRollback()
}

func TestOwnershipInvalidMutationsNeverOpenTransaction(t *testing.T) {
	for _, input := range []storage.OwnershipMutation{
		{Scope: storage.OwnershipScope{Kind: "public"}, Operation: "leave"},
		{Scope: storage.OwnershipScope{Kind: "dm"}, Operation: "unknown"},
		{Scope: storage.OwnershipScope{Kind: "dm"}, Operation: "role", Role: "moderator"},
		{Scope: storage.OwnershipScope{Kind: "dm"}, Operation: "transfer", Role: domain.ConversationOwner, IdempotencyKey: "key"},
		{Scope: storage.OwnershipScope{Kind: "dm"}, Operation: "transfer", Role: domain.ConversationMember},
	} {
		if _, err := storage.NewPGXOwnershipStore(nil).Mutate(t.Context(), input); err == nil {
			t.Fatalf("invalid mutation admitted: %+v", input)
		}
	}
}
