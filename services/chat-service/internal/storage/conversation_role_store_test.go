package storage_test

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
	pgxmock "github.com/pashagolub/pgxmock/v2"
)

func roleInput(kind string, role domain.ConversationRole) storage.OwnershipMutation {
	return storage.OwnershipMutation{Scope: storage.OwnershipScope{WorkspaceID: ownershipWS, Kind: kind, ConversationID: ownershipDM, ActorID: ownershipA}, Operation: "role", TargetUserID: ownershipB, Role: role}
}

func expectRoleSnapshot(mock pgxmock.PgxPoolIface, input storage.OwnershipMutation, actor, target domain.ConversationRole) {
	mock.ExpectBegin()
	mock.ExpectExec("SET TRANSACTION ISOLATION LEVEL SERIALIZABLE").WillReturnResult(pgxmock.NewResult("SET", 0))
	mock.ExpectExec("SELECT chat.lock_ownership_conversation").WithArgs(input.Scope.Kind, input.Scope.ConversationID).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery("SELECT resource.workspace_id").WithArgs(input.Scope.Kind, input.Scope.ConversationID).WillReturnRows(pgxmock.NewRows([]string{"workspace", "status", "enabled"}).AddRow(ownershipWS, domain.WorkspaceStatusActive, true))
	rows := pgxmock.NewRows([]string{"user", "role", "workspace_role", "status"})
	if actor != "" {
		rows.AddRow(ownershipA, actor, domain.WorkspaceRoleAdmin, domain.MemberStatusActive)
	}
	if target != "" {
		rows.AddRow(ownershipB, target, domain.WorkspaceRoleGuest, domain.MemberStatusActive)
	}
	mock.ExpectQuery("SELECT p.user_id::text,p.role,wm.role,wm.status").WithArgs(input.Scope.Kind, input.Scope.ConversationID, ownershipWS).WillReturnRows(rows)
}

func expectRoleWrite(mock pgxmock.PgxPoolIface, input storage.OwnershipMutation, failure error) {
	write := mock.ExpectExec("SELECT chat.assign_ownership").WithArgs(input.Scope.Kind, input.Scope.ConversationID, input.TargetUserID, string(input.Role), input.Scope.ActorID, "manual")
	if failure != nil {
		write.WillReturnError(failure)
	} else {
		write.WillReturnResult(pgxmock.NewResult("SELECT", 1))
	}
}

func TestConversationRoleTransitionMatrix(t *testing.T) {
	roles := []domain.ConversationRole{domain.ConversationOwner, domain.ConversationAdmin, domain.ConversationMember}
	for _, kind := range []string{"dm", "channel"} {
		for _, actor := range roles {
			for _, before := range roles {
				for _, after := range roles {
					t.Run(kind+"/"+string(actor)+"/"+string(before)+"-"+string(after), func(t *testing.T) {
						mock, err := pgxmock.NewPool()
						if err != nil {
							t.Fatal(err)
						}
						defer mock.Close()
						input := roleInput(kind, after)
						expectRoleSnapshot(mock, input, actor, before)
						var want error
						if actor != domain.ConversationOwner {
							want = domain.ErrForbidden
							mock.ExpectRollback()
						} else {
							if before != after {
								expectRoleWrite(mock, input, nil)
							}
							mock.ExpectCommit()
						}
						result, err := storage.NewPGXOwnershipStore(mock).Mutate(t.Context(), input)
						if !errors.Is(err, want) {
							t.Fatalf("error=%v want=%v", err, want)
						}
						if err == nil && (result.Role != after || result.TargetUserID != ownershipB || result.EventID != "" || result.Left) {
							t.Fatalf("result=%+v", result)
						}
						if err := mock.ExpectationsWereMet(); err != nil {
							t.Fatal(err)
						}
					})
				}
			}
		}
	}
}

func TestConversationRoleLastOwnerAndAccess(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		actor, target, after domain.ConversationRole
		want                 error
	}{
		{"last-owner", domain.ConversationMember, domain.ConversationOwner, domain.ConversationAdmin, domain.ErrOwnershipConflict},
		{"sole-owner", domain.ConversationOwner, "", domain.ConversationMember, domain.ErrOwnershipConflict},
		{"sole-owner-noop", domain.ConversationOwner, "", domain.ConversationOwner, nil},
		{"actor-unavailable", "", domain.ConversationMember, domain.ConversationAdmin, domain.ErrNotFound},
		{"target-unavailable", domain.ConversationOwner, "", domain.ConversationAdmin, domain.ErrNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mock, err := pgxmock.NewPool()
			if err != nil {
				t.Fatal(err)
			}
			defer mock.Close()
			input := roleInput("dm", tc.after)
			if tc.name == "last-owner" {
				input.Scope.ActorID = ownershipB
			}
			if tc.name == "sole-owner" || tc.name == "sole-owner-noop" {
				input.TargetUserID = ownershipA
			}
			expectRoleSnapshot(mock, input, tc.actor, tc.target)
			if tc.want == nil {
				mock.ExpectCommit()
			} else {
				mock.ExpectRollback()
			}
			_, err = storage.NewPGXOwnershipStore(mock).Mutate(t.Context(), input)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error=%v want=%v", err, tc.want)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestConversationRoleRetryReloadsAuthorization(t *testing.T) {
	for _, code := range []string{"40001", "40P01"} {
		t.Run(code, func(t *testing.T) {
			mock, err := pgxmock.NewPool()
			if err != nil {
				t.Fatal(err)
			}
			defer mock.Close()
			input := roleInput("dm", domain.ConversationAdmin)
			expectRoleSnapshot(mock, input, domain.ConversationOwner, domain.ConversationMember)
			expectRoleWrite(mock, input, nil)
			mock.ExpectCommit().WillReturnError(&pgconn.PgError{Code: code})
			mock.ExpectRollback()
			expectRoleSnapshot(mock, input, domain.ConversationMember, domain.ConversationMember)
			mock.ExpectRollback()
			_, err = storage.NewPGXOwnershipStore(mock).Mutate(t.Context(), input)
			if !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("stale actor admitted: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestConversationRolePersistenceAndCommitFailure(t *testing.T) {
	failure := errors.New("database failure")
	for _, phase := range []string{"write", "commit", "retry-exhausted"} {
		t.Run(phase, func(t *testing.T) {
			mock, err := pgxmock.NewPool()
			if err != nil {
				t.Fatal(err)
			}
			defer mock.Close()
			input := roleInput("dm", domain.ConversationAdmin)
			attempts := 1
			if phase == "retry-exhausted" {
				attempts = 3
			}
			for range attempts {
				expectRoleSnapshot(mock, input, domain.ConversationOwner, domain.ConversationMember)
				if phase == "write" {
					expectRoleWrite(mock, input, failure)
				} else {
					expectRoleWrite(mock, input, nil)
					if phase == "retry-exhausted" {
						mock.ExpectCommit().WillReturnError(&pgconn.PgError{Code: "40001"})
					} else {
						mock.ExpectCommit().WillReturnError(failure)
					}
				}
				mock.ExpectRollback()
			}
			_, err = storage.NewPGXOwnershipStore(mock).Mutate(t.Context(), input)
			if err == nil || phase != "retry-exhausted" && !errors.Is(err, failure) {
				t.Fatalf("error=%v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestConversationRoleAdmissionFailure(t *testing.T) {
	failure := errors.New("snapshot unavailable")
	for _, phase := range []string{"begin", "isolation", "lock", "resource", "roster", "scan", "rows"} {
		t.Run(phase, func(t *testing.T) {
			mock, err := pgxmock.NewPool()
			if err != nil {
				t.Fatal(err)
			}
			defer mock.Close()
			input := roleInput("dm", domain.ConversationAdmin)
			expectRoleAdmissionFailure(mock, input, phase, failure)
			if phase != "begin" {
				mock.ExpectRollback()
			}
			_, err = storage.NewPGXOwnershipStore(mock).Mutate(t.Context(), input)
			if !errors.Is(err, failure) && phase != "scan" {
				t.Fatalf("error=%v", err)
			}
			if err == nil {
				t.Fatal("failed snapshot admitted")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func expectRoleAdmissionFailure(mock pgxmock.PgxPoolIface, input storage.OwnershipMutation, phase string, failure error) {
	if phase == "begin" {
		mock.ExpectBegin().WillReturnError(failure)
		return
	}
	mock.ExpectBegin()
	isolation := mock.ExpectExec("SET TRANSACTION ISOLATION LEVEL SERIALIZABLE")
	if phase == "isolation" {
		isolation.WillReturnError(failure)
		return
	}
	isolation.WillReturnResult(pgxmock.NewResult("SET", 0))
	lock := mock.ExpectExec("SELECT chat.lock_ownership_conversation").WithArgs(input.Scope.Kind, input.Scope.ConversationID)
	if phase == "lock" {
		lock.WillReturnError(failure)
		return
	}
	lock.WillReturnResult(pgxmock.NewResult("SELECT", 1))
	resource := mock.ExpectQuery("SELECT resource.workspace_id").WithArgs(input.Scope.Kind, input.Scope.ConversationID)
	if phase == "resource" {
		resource.WillReturnError(failure)
		return
	}
	resource.WillReturnRows(pgxmock.NewRows([]string{"workspace", "status", "enabled"}).AddRow(ownershipWS, domain.WorkspaceStatusActive, true))
	roster := mock.ExpectQuery("SELECT p.user_id::text,p.role,wm.role,wm.status").WithArgs(input.Scope.Kind, input.Scope.ConversationID, ownershipWS)
	if phase == "roster" {
		roster.WillReturnError(failure)
		return
	}
	rows := pgxmock.NewRows([]string{"user", "role", "workspace_role", "status"})
	if phase == "scan" {
		rows.AddRow(ownershipA, 123, domain.WorkspaceRoleMember, domain.MemberStatusActive)
	} else {
		rows.AddRow(ownershipA, domain.ConversationOwner, domain.WorkspaceRoleMember, domain.MemberStatusActive).RowError(0, failure)
	}
	roster.WillReturnRows(rows)
}
