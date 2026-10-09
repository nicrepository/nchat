package storage_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
	"github.com/nicrepository/nchat/services/admin-service/internal/storage"
	pgxmock "github.com/pashagolub/pgxmock/v2"
)

func TestUpdateUserStatus_InvalidationFailureReportsNoRevocation(t *testing.T) {
	failure := errors.New("invalidation transaction failed")
	cases := []struct {
		name   string
		revoke func(pgxmock.PgxPoolIface, error)
	}{
		{"sessions", expectSessionRevocationFailure},
		{"oidc", expectOIDCInvalidationFailure},
		{"commit", expectInvalidationCommitFailure},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock := newMock(t)
			expectSuspensionPromotion(mock)
			tc.revoke(mock, failure)
			mock.ExpectRollback()
			change, err := storage.NewPGXUserDirectoryStore(mock).UpdateUserStatus(context.Background(), userA, "suspended")
			if !errors.Is(err, failure) {
				t.Fatalf("lost transaction error: %v", err)
			}
			if change != (domain.UserStatusChange{}) {
				t.Fatalf("failed invalidation reported success: %+v", change)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func expectSuspensionPromotion(mock pgxmock.PgxPoolIface) {
	mock.ExpectBegin()
	mock.ExpectExec(`SET TRANSACTION ISOLATION LEVEL SERIALIZABLE`).WillReturnResult(pgxmock.NewResult("SET", 0))
	mock.ExpectQuery(`SELECT to_regprocedure`).WillReturnRows(pgxmock.NewRows([]string{"available"}).AddRow(true))
	mock.ExpectExec(`SELECT chat.lock_user_ownership_conversations`).WithArgs(userA).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(`FOR UPDATE`).WithArgs(userA).WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("active"))
	mock.ExpectExec(`SELECT 1 FROM auth.admin_principals`).WithArgs(userA).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(`WITH affected AS`).WithArgs("", "", "", userA, true).
		WillReturnRows(pgxmock.NewRows([]string{"workspace", "kind", "conversation", "members", "owners", "candidate"}).AddRow("workspace", "dm", "conversation", 1, 0, userB))
	mock.ExpectExec(`SELECT chat.assign_ownership`).WithArgs("dm", "conversation", userB, "owner", "", "invalidation").WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec(`UPDATE auth.users`).WithArgs(userA, "suspended").WillReturnResult(pgxmock.NewResult("UPDATE", 1))
}

func expectSessionRevocationFailure(mock pgxmock.PgxPoolIface, failure error) {
	mock.ExpectQuery(`UPDATE auth.user_sessions`).WithArgs(userA, "admin_suspension").WillReturnError(failure)
}
func expectRevokedSessions(mock pgxmock.PgxPoolIface) {
	mock.ExpectQuery(`UPDATE auth.user_sessions`).WithArgs(userA, "admin_suspension").WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(2))
}
func expectOIDCInvalidationFailure(mock pgxmock.PgxPoolIface, failure error) {
	expectRevokedSessions(mock)
	mock.ExpectExec(`UPDATE auth.oidc_exchange_codes`).WithArgs(userA).WillReturnError(failure)
}
func expectInvalidationCommitFailure(mock pgxmock.PgxPoolIface, failure error) {
	expectRevokedSessions(mock)
	mock.ExpectExec(`UPDATE auth.oidc_exchange_codes`).WithArgs(userA).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit().WillReturnError(failure)
}
