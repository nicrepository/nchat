package storage_test

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
	pgxmock "github.com/pashagolub/pgxmock/v2"
)

func expectTransferSnapshot(mock pgxmock.PgxPoolIface, input storage.OwnershipMutation, actor domain.ConversationRole) {
	mock.ExpectBegin()
	mock.ExpectExec("SET TRANSACTION ISOLATION LEVEL SERIALIZABLE").WillReturnResult(pgxmock.NewResult("SET", 0))
	mock.ExpectExec("SELECT chat.lock_ownership_conversation").WithArgs(input.Scope.Kind, input.Scope.ConversationID).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery("SELECT request_hash,response").WithArgs(input.Scope.WorkspaceID, input.Scope.Kind, input.Scope.ConversationID, input.Scope.ActorID, input.IdempotencyKey).WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery("SELECT resource.workspace_id").WithArgs(input.Scope.Kind, input.Scope.ConversationID).WillReturnRows(pgxmock.NewRows([]string{"workspace", "status", "enabled"}).AddRow(ownershipWS, domain.WorkspaceStatusActive, true))
	mock.ExpectQuery("SELECT p.user_id::text,p.role,wm.role,wm.status").WithArgs(input.Scope.Kind, input.Scope.ConversationID, ownershipWS).WillReturnRows(pgxmock.NewRows([]string{"user", "role", "workspace_role", "status"}).AddRow(ownershipA, actor, domain.WorkspaceRoleMember, domain.MemberStatusActive).AddRow(ownershipB, domain.ConversationMember, domain.WorkspaceRoleMember, domain.MemberStatusActive))
}

func TestOwnershipTransferRetryReloadsAuthorization(t *testing.T) {
	for _, code := range []string{"40001", "40P01"} {
		t.Run(code, func(t *testing.T) {
			mock, err := pgxmock.NewPool()
			if err != nil {
				t.Fatal(err)
			}
			defer mock.Close()
			input := transferInput("dm")
			expectTransferSnapshot(mock, input, domain.ConversationOwner)
			mock.ExpectExec("SELECT chat.assign_ownership").WithArgs("dm", ownershipDM, ownershipB, "owner", ownershipA, "transfer").WillReturnError(&pgconn.PgError{Code: code})
			mock.ExpectRollback()
			expectTransferSnapshot(mock, input, domain.ConversationMember)
			mock.ExpectRollback()
			_, err = storage.NewPGXOwnershipStore(mock).Mutate(t.Context(), input)
			if !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("stale authorization admitted: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
