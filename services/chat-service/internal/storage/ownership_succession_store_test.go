package storage_test

import (
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
	pgxmock "github.com/pashagolub/pgxmock/v2"
)

func expectSuccessionSnapshot(mock pgxmock.PgxPoolIface, candidate string) {
	input := successionInput("dm", ownershipA)
	mock.ExpectBegin()
	mock.ExpectExec("SET TRANSACTION ISOLATION LEVEL SERIALIZABLE").WillReturnResult(pgxmock.NewResult("SET", 0))
	mock.ExpectExec("SELECT chat.lock_ownership_conversation").WithArgs("dm", ownershipDM).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery("SELECT enabled FROM chat.ownership_rollout").WillReturnRows(pgxmock.NewRows([]string{"enabled"}).AddRow(true))
	mock.ExpectQuery("SELECT p.user_id::text,p.role,p.joined_at,p.guest").WithArgs("dm", ownershipDM, ownershipWS, ownershipA).WillReturnRows(pgxmock.NewRows([]string{"user", "role", "joined", "guest", "name", "avatar"}).AddRow(ownershipA, domain.ConversationOwner, time.Now(), false, "A", "").AddRow(candidate, domain.ConversationMember, time.Now(), false, "Candidate", ""))
	mock.ExpectQuery("WITH affected AS").WithArgs(input.Scope.WorkspaceID, input.Scope.Kind, input.Scope.ConversationID, ownershipA, false).WillReturnRows(pgxmock.NewRows([]string{"workspace", "kind", "conversation", "members", "owners", "candidate"}).AddRow(input.Scope.WorkspaceID, input.Scope.Kind, input.Scope.ConversationID, 1, 0, candidate))
}

func expectSuccessionPromotion(mock pgxmock.PgxPoolIface, candidate string) *pgxmock.ExpectedExec {
	return mock.ExpectExec("SELECT chat.assign_ownership").WithArgs("dm", ownershipDM, candidate, "owner", ownershipA, "succession")
}

func expectSuccessionDeparture(mock pgxmock.PgxPoolIface, failure error) {
	dmID := ownershipDM
	mock.ExpectQuery("INSERT INTO chat.messages").WithArgs(ownershipWS, (*string)(nil), &dmID, ownershipA, string(domain.ConversationEventMemberLeft), pgxmock.AnyArg()).WillReturnRows(pgxmock.NewRows([]string{"id", "workspace", "channel", "dm", "sender", "kind", "event", "created"}).AddRow("event", ownershipWS, "", ownershipDM, ownershipA, "system", string(domain.ConversationEventMemberLeft), time.Now()))
	update := mock.ExpectExec("UPDATE chat.dm_members SET status='left'").WithArgs(ownershipDM, ownershipA)
	if failure != nil {
		update.WillReturnError(failure)
	} else {
		update.WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	}
}

func TestOwnershipSuccessionRetryReselectsCandidate(t *testing.T) {
	for _, code := range []string{"40001", "40P01"} {
		t.Run(code, func(t *testing.T) {
			mock, err := pgxmock.NewPool()
			if err != nil {
				t.Fatal(err)
			}
			defer mock.Close()
			expectSuccessionSnapshot(mock, ownershipB)
			expectSuccessionPromotion(mock, ownershipB).WillReturnResult(pgxmock.NewResult("SELECT", 1))
			expectSuccessionDeparture(mock, &pgconn.PgError{Code: code})
			mock.ExpectRollback()
			expectSuccessionSnapshot(mock, ownershipC)
			expectSuccessionPromotion(mock, ownershipC).WillReturnResult(pgxmock.NewResult("SELECT", 1))
			expectSuccessionDeparture(mock, nil)
			mock.ExpectCommit()
			_, err = storage.NewPGXOwnershipStore(mock).Mutate(t.Context(), successionInput("dm", ownershipA))
			if err != nil {
				t.Fatal(err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestOwnershipSuccessionDepartureFailureRollsBack(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	failure := errors.New("departure failed")
	expectSuccessionSnapshot(mock, ownershipB)
	expectSuccessionPromotion(mock, ownershipB).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	expectSuccessionDeparture(mock, failure)
	mock.ExpectRollback()
	_, err = storage.NewPGXOwnershipStore(mock).Mutate(t.Context(), successionInput("dm", ownershipA))
	if !errors.Is(err, failure) {
		t.Fatalf("err=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
