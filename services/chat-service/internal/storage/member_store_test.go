package storage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	pgxmock "github.com/pashagolub/pgxmock/v2"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

func TestPGXMemberStore_AddWorkspaceMember_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	now := time.Now()
	mock.ExpectQuery(`INSERT INTO chat.workspace_members`).
		WithArgs("ws-1", "user-1", "member").
		WillReturnRows(pgxmock.NewRows([]string{"workspace_id", "user_id", "role", "status", "joined_at"}).
			AddRow("ws-1", "user-1", "member", "active", now))

	store := storage.NewPGXMemberStore(mock)
	m, err := store.AddWorkspaceMember(context.Background(), "ws-1", "user-1", domain.WorkspaceRoleMember)
	if err != nil {
		t.Fatalf("AddWorkspaceMember: %v", err)
	}
	if m.UserID != "user-1" || m.Status != domain.MemberStatusActive {
		t.Fatalf("unexpected member: %+v", m)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXMemberStore_AddWorkspaceMember_AlreadyMember(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	// ON CONFLICT DO NOTHING returns 0 rows → pgx.ErrNoRows on Scan
	mock.ExpectQuery(`INSERT INTO chat.workspace_members`).
		WithArgs("ws-1", "user-1", "member").
		WillReturnRows(pgxmock.NewRows([]string{"workspace_id", "user_id", "role", "status", "joined_at"}))

	store := storage.NewPGXMemberStore(mock)
	_, err = store.AddWorkspaceMember(context.Background(), "ws-1", "user-1", domain.WorkspaceRoleMember)
	if !errors.Is(err, domain.ErrAlreadyMember) {
		t.Fatalf("expected ErrAlreadyMember, got %v", err)
	}
}

func TestPGXMemberStore_GetWorkspaceMember_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	now := time.Now()
	mock.ExpectQuery(`SELECT workspace_id, user_id, role, status, joined_at`).
		WithArgs("ws-1", "user-1").
		WillReturnRows(pgxmock.NewRows([]string{"workspace_id", "user_id", "role", "status", "joined_at"}).
			AddRow("ws-1", "user-1", "admin", "active", now))

	store := storage.NewPGXMemberStore(mock)
	m, err := store.GetWorkspaceMember(context.Background(), "ws-1", "user-1")
	if err != nil {
		t.Fatalf("GetWorkspaceMember: %v", err)
	}
	if m.Role != domain.WorkspaceRoleAdmin {
		t.Fatalf("expected admin, got %q", m.Role)
	}
}

func TestPGXMemberStore_GetWorkspaceMember_NotFound(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`SELECT workspace_id, user_id, role, status, joined_at`).
		WithArgs("ws-1", "no-such").
		WillReturnRows(pgxmock.NewRows([]string{"workspace_id", "user_id", "role", "status", "joined_at"}))

	store := storage.NewPGXMemberStore(mock)
	_, err = store.GetWorkspaceMember(context.Background(), "ws-1", "no-such")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestPGXMemberStore_AddChannelMember_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	now := time.Now()
	mock.ExpectQuery(`INSERT INTO chat.channel_members`).
		WithArgs("ch-1", "user-1", "member").
		WillReturnRows(pgxmock.NewRows([]string{"channel_id", "user_id", "role", "joined_at"}).
			AddRow("ch-1", "user-1", "member", now))

	store := storage.NewPGXMemberStore(mock)
	m, err := store.AddChannelMember(context.Background(), "ch-1", "user-1", domain.ChannelRoleMember)
	if err != nil {
		t.Fatalf("AddChannelMember: %v", err)
	}
	if m.ChannelID != "ch-1" {
		t.Fatalf("expected ch-1, got %q", m.ChannelID)
	}
}

func TestPGXMemberStore_AddChannelMember_AlreadyMember(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`INSERT INTO chat.channel_members`).
		WithArgs("ch-1", "user-1", "member").
		WillReturnRows(pgxmock.NewRows([]string{"channel_id", "user_id", "role", "joined_at"}))

	store := storage.NewPGXMemberStore(mock)
	_, err = store.AddChannelMember(context.Background(), "ch-1", "user-1", domain.ChannelRoleMember)
	if !errors.Is(err, domain.ErrAlreadyMember) {
		t.Fatalf("expected ErrAlreadyMember, got %v", err)
	}
}

func TestPGXMemberStore_GetChannelMember_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	now := time.Now()
	mock.ExpectQuery(`SELECT channel_id, user_id, role, joined_at`).
		WithArgs("ch-1", "user-1").
		WillReturnRows(pgxmock.NewRows([]string{"channel_id", "user_id", "role", "joined_at"}).
			AddRow("ch-1", "user-1", "moderator", now))

	store := storage.NewPGXMemberStore(mock)
	m, err := store.GetChannelMember(context.Background(), "ch-1", "user-1")
	if err != nil {
		t.Fatalf("GetChannelMember: %v", err)
	}
	if m.Role != domain.ChannelRoleModerator {
		t.Fatalf("expected moderator, got %q", m.Role)
	}
}

func TestPGXMemberStore_GetChannelMember_NotFound(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`SELECT channel_id, user_id, role, joined_at`).
		WithArgs("ch-1", "no-such").
		WillReturnRows(pgxmock.NewRows([]string{"channel_id", "user_id", "role", "joined_at"}))

	store := storage.NewPGXMemberStore(mock)
	_, err = store.GetChannelMember(context.Background(), "ch-1", "no-such")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}
