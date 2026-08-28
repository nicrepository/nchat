package storage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	pgxmock "github.com/pashagolub/pgxmock/v2"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

func TestPGXMemberStore_SearchChannelMembers_ScopesWorkspaceChannelAndActiveMembership(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()
	mock.ExpectQuery(`(?s)FROM chat\.channel_members.*c\.workspace_id = \$1::uuid.*wm\.status = 'active'.*u\.status = 'active'.*cm\.channel_id = \$2::uuid`).
		WithArgs("ws-1", "ch-1", "an", 10).
		WillReturnRows(pgxmock.NewRows([]string{"id", "display_name"}).AddRow("user-in-channel", "Ana"))

	got, err := storage.NewPGXMemberStore(mock).SearchChannelMembers(
		context.Background(), "ws-1", "ch-1", "an", 10,
	)
	if err != nil {
		t.Fatalf("SearchChannelMembers: %v", err)
	}
	if len(got) != 1 || got[0].ID != "user-in-channel" {
		t.Fatalf("out-of-scope users must not appear: %#v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXMemberStore_ListChannelMemberProfilesByIDs_ScopesToChannelMembership(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()
	mock.ExpectQuery(`(?s)FROM chat\.channel_members cm.*c\.workspace_id = \$1::uuid.*wm\.status = 'active'.*u\.status = 'active'.*cm\.channel_id = \$2::uuid.*user_id = ANY\(\$3::uuid\[\]\)`).
		WithArgs("ws-1", "ch-1", []string{"user-a", "user-b"}).
		WillReturnRows(pgxmock.NewRows([]string{"user_id", "display_name", "avatar_url"}).
			AddRow("user-a", "Ana Souza", "https://x/a.png"))

	got, err := storage.NewPGXMemberStore(mock).ListChannelMemberProfilesByIDs(
		context.Background(), "ws-1", "ch-1", []string{"user-a", "user-b"},
	)
	if err != nil {
		t.Fatalf("ListChannelMemberProfilesByIDs: %v", err)
	}
	// user-b is not a member of the channel and must not appear — no
	// invented identity for an id the join could not resolve.
	if len(got) != 1 || got[0].UserID != "user-a" || got[0].DisplayName != "Ana Souza" || got[0].AvatarURL != "https://x/a.png" {
		t.Fatalf("unexpected profiles: %#v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXMemberStore_ListChannelMemberProfilesByIDs_EmptyIDsSelectsNoRows(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()
	mock.ExpectQuery(`(?s)FROM chat\.channel_members`).
		WithArgs("ws-1", "ch-1", []string{}).
		WillReturnRows(pgxmock.NewRows([]string{"user_id", "display_name", "avatar_url"}))

	got, err := storage.NewPGXMemberStore(mock).ListChannelMemberProfilesByIDs(
		context.Background(), "ws-1", "ch-1", nil,
	)
	if err != nil {
		t.Fatalf("ListChannelMemberProfilesByIDs: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("nil ids must select nothing, got %#v", got)
	}
}

func TestPGXMemberStore_SearchDMCandidates_ScopesEligibilityAndOrdersResults(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()
	mock.ExpectQuery(`(?s)SELECT u\.id::text, u\.display_name.*FROM chat\.workspace_members wm.*w\.status = 'active'.*u\.status = 'active'.*u\.deleted_at IS NULL.*wm\.user_id <> \$2::uuid.*caller\.status = 'active'.*ORDER BY lower\(u\.display_name\), u\.id.*LIMIT \$4`).
		WithArgs("ws-1", "user-1", "an", 20).
		WillReturnRows(pgxmock.NewRows([]string{"id", "display_name"}).
			AddRow("user-2", "Ana").
			AddRow("user-3", "André"))

	got, err := storage.NewPGXMemberStore(mock).SearchDMCandidates(
		context.Background(), "ws-1", "user-1", "an", 20,
	)
	if err != nil {
		t.Fatalf("SearchDMCandidates: %v", err)
	}
	if len(got) != 2 || got[0].DisplayName != "Ana" || got[1].DisplayName != "André" {
		t.Fatalf("unexpected candidates: %#v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXMemberStore_SearchDMCandidates_EmptyAndFailurePaths(t *testing.T) {
	for _, test := range []struct {
		name string
		rows *pgxmock.Rows
		err  error
	}{
		{name: "empty", rows: pgxmock.NewRows([]string{"id", "display_name"})},
		{name: "scan error", rows: pgxmock.NewRows([]string{"id"}).AddRow("user-2")},
		{name: "rows error", rows: pgxmock.NewRows([]string{"id", "display_name"}).AddRow("user-2", "Ana").AddRow("user-3", "André").RowError(1, errors.New("row stream failed"))},
		{name: "query error", err: errors.New("query failed")},
	} {
		t.Run(test.name, func(t *testing.T) {
			mock, err := pgxmock.NewPool()
			if err != nil {
				t.Fatalf("pgxmock: %v", err)
			}
			defer mock.Close()
			expectation := mock.ExpectQuery(`FROM chat\.workspace_members wm`).WithArgs("ws-1", "user-1", "an", 20)
			if test.err != nil {
				expectation.WillReturnError(test.err)
			} else {
				expectation.WillReturnRows(test.rows)
			}

			got, gotErr := storage.NewPGXMemberStore(mock).SearchDMCandidates(context.Background(), "ws-1", "user-1", "an", 20)
			if test.name == "empty" {
				if gotErr != nil || len(got) != 0 {
					t.Fatalf("candidates=%v error=%v", got, gotErr)
				}
			} else if gotErr == nil {
				t.Fatal("expected error")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet expectations: %v", err)
			}
		})
	}
}

func TestPGXMemberStore_GetEligibleDMMember_RequiresActiveWorkspaceMembershipAndAccount(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()
	now := time.Now()
	mock.ExpectQuery(`(?s)FROM chat\.workspace_members wm.*w\.status = 'active'.*u\.status = 'active'.*u\.deleted_at IS NULL.*wm\.status = 'active'`).
		WithArgs("ws-1", "user-1").
		WillReturnRows(pgxmock.NewRows([]string{"workspace_id", "user_id", "role", "status", "joined_at"}).
			AddRow("ws-1", "user-1", "member", "active", now))

	member, err := storage.NewPGXMemberStore(mock).GetEligibleDMMember(context.Background(), "ws-1", "user-1")
	if err != nil || member.UserID != "user-1" {
		t.Fatalf("member=%+v err=%v", member, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXMemberStore_GetEligibleDMMember_HidesIneligibleUser(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()
	mock.ExpectQuery(`FROM chat\.workspace_members wm`).
		WithArgs("ws-1", "user-1").
		WillReturnError(pgx.ErrNoRows)

	_, err = storage.NewPGXMemberStore(mock).GetEligibleDMMember(context.Background(), "ws-1", "user-1")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error=%v, want ErrNotFound", err)
	}
}

func TestPGXMemberStore_GetEligibleDMMember_PropagatesDatabaseError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()
	want := errors.New("database unavailable")
	mock.ExpectQuery(`FROM chat\.workspace_members wm`).WithArgs("ws-1", "user-1").WillReturnError(want)

	_, err = storage.NewPGXMemberStore(mock).GetEligibleDMMember(context.Background(), "ws-1", "user-1")
	if !errors.Is(err, want) {
		t.Fatalf("error=%v, want wrapped database error", err)
	}
}

func TestPGXMemberStore_AddWorkspaceMember_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	now := time.Now()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status\s+FROM chat\.workspaces`).
		WithArgs("ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("active"))
	mock.ExpectQuery(`INSERT INTO chat\.workspace_members`).
		WithArgs("ws-1", "user-1", "member").
		WillReturnRows(pgxmock.NewRows([]string{"workspace_id", "user_id", "role", "status", "joined_at"}).
			AddRow("ws-1", "user-1", "member", "active", now))
	mock.ExpectQuery(`SELECT id\s+FROM chat\.channels`).
		WithArgs("ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("ch-geral"))
	mock.ExpectExec(`(?s)INSERT INTO chat\.channel_members.*FROM chat\.workspace_members wm.*wm\.role IN \('owner', 'admin', 'moderator', 'member'\)`).
		WithArgs("ch-geral", "ws-1", "user-1", "member").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

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

	now := time.Now()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status\s+FROM chat\.workspaces`).
		WithArgs("ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("active"))
	// ON CONFLICT DO NOTHING returns 0 rows -> pgx.ErrNoRows on Scan.
	mock.ExpectQuery(`INSERT INTO chat\.workspace_members`).
		WithArgs("ws-1", "user-1", "member").
		WillReturnRows(pgxmock.NewRows([]string{"workspace_id", "user_id", "role", "status", "joined_at"}))
	mock.ExpectQuery(`SELECT wm\.workspace_id, wm\.user_id, wm\.role, wm\.status, wm\.joined_at`).
		WithArgs("ws-1", "user-1").
		WillReturnRows(pgxmock.NewRows([]string{"workspace_id", "user_id", "role", "status", "joined_at"}).
			AddRow("ws-1", "user-1", "member", "active", now))
	mock.ExpectQuery(`SELECT id\s+FROM chat\.channels`).
		WithArgs("ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("ch-geral"))
	mock.ExpectExec(`(?s)INSERT INTO chat\.channel_members.*FROM chat\.workspace_members wm.*wm\.role IN \('owner', 'admin', 'moderator', 'member'\)`).
		WithArgs("ch-geral", "ws-1", "user-1", "member").
		WillReturnResult(pgxmock.NewResult("INSERT", 0))
	mock.ExpectCommit()

	store := storage.NewPGXMemberStore(mock)
	_, err = store.AddWorkspaceMember(context.Background(), "ws-1", "user-1", domain.WorkspaceRoleMember)
	if !errors.Is(err, domain.ErrAlreadyMember) {
		t.Fatalf("expected ErrAlreadyMember, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXMemberStore_GetWorkspaceMember_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	now := time.Now()
	mock.ExpectQuery(`SELECT wm.workspace_id, wm.user_id, wm.role, wm.status, wm.joined_at`).
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

	mock.ExpectQuery(`SELECT wm.workspace_id, wm.user_id, wm.role, wm.status, wm.joined_at`).
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
	// The channel row is locked first: every writer of chat.channel_members
	// obeys the same protocol, so a concurrent add cannot land between another
	// transaction's write and its member count.
	mock.ExpectBegin()
	mock.ExpectExec(`FROM chat.channels WHERE id = \$1::uuid FOR UPDATE`).WithArgs("ch-1").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(`INSERT INTO chat.channel_members`).
		WithArgs("ch-1", "user-1", "member").
		WillReturnRows(pgxmock.NewRows([]string{"channel_id", "user_id", "role", "joined_at"}).
			AddRow("ch-1", "user-1", "member", now))
	mock.ExpectCommit()

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

	mock.ExpectBegin()
	mock.ExpectExec(`FROM chat.channels WHERE id = \$1::uuid FOR UPDATE`).WithArgs("ch-1").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(`INSERT INTO chat.channel_members`).
		WithArgs("ch-1", "user-1", "member").
		WillReturnRows(pgxmock.NewRows([]string{"channel_id", "user_id", "role", "joined_at"}))
	mock.ExpectRollback()

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

func TestPGXMemberStore_GetWorkspaceMember_DisabledWorkspace_NotFound(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	// JOIN with workspace where status='active' finds no row when workspace is disabled.
	mock.ExpectQuery(`SELECT wm.workspace_id, wm.user_id, wm.role, wm.status, wm.joined_at`).
		WithArgs("ws-disabled", "user-1").
		WillReturnRows(pgxmock.NewRows([]string{"workspace_id", "user_id", "role", "status", "joined_at"}))

	store := storage.NewPGXMemberStore(mock)
	_, err = store.GetWorkspaceMember(context.Background(), "ws-disabled", "user-1")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("disabled workspace should return ErrNotFound, got %v", err)
	}
}

func TestPGXMemberStore_GetWorkspaceMember_DBError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`SELECT wm.workspace_id, wm.user_id, wm.role, wm.status, wm.joined_at`).
		WithArgs("ws-1", "user-1").
		WillReturnError(errors.New("connection lost"))

	store := storage.NewPGXMemberStore(mock)
	_, err = store.GetWorkspaceMember(context.Background(), "ws-1", "user-1")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestPGXMemberStore_AddWorkspaceMember_DBError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	want := errors.New("db error")
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status\s+FROM chat\.workspaces`).
		WithArgs("ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("active"))
	mock.ExpectQuery(`INSERT INTO chat\.workspace_members`).
		WithArgs("ws-1", "user-1", "member").
		WillReturnError(want)
	mock.ExpectRollback()

	store := storage.NewPGXMemberStore(mock)
	_, err = store.AddWorkspaceMember(context.Background(), "ws-1", "user-1", domain.WorkspaceRoleMember)
	if !errors.Is(err, want) {
		t.Fatalf("expected db error, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXMemberStore_AddWorkspaceMember_AddsGeneralInTransaction(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	now := time.Now()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status\s+FROM chat\.workspaces`).
		WithArgs("ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("active"))
	mock.ExpectQuery(`INSERT INTO chat\.workspace_members`).
		WithArgs("ws-1", "user-1", "member").
		WillReturnRows(pgxmock.NewRows([]string{"workspace_id", "user_id", "role", "status", "joined_at"}).
			AddRow("ws-1", "user-1", "member", "active", now))
	mock.ExpectQuery(`SELECT id\s+FROM chat\.channels`).
		WithArgs("ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("ch-geral"))
	mock.ExpectExec(`(?s)INSERT INTO chat\.channel_members.*FROM chat\.workspace_members wm.*wm\.role IN \('owner', 'admin', 'moderator', 'member'\)`).
		WithArgs("ch-geral", "ws-1", "user-1", "member").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	store := storage.NewPGXMemberStore(mock)
	m, err := store.AddWorkspaceMember(context.Background(), "ws-1", "user-1", domain.WorkspaceRoleMember)
	if err != nil {
		t.Fatalf("AddWorkspaceMember: %v", err)
	}
	if m.Status != domain.MemberStatusActive {
		t.Fatalf("expected active member, got %+v", m)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXMemberStore_AddWorkspaceMember_DisabledWorkspaceDenied(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status\s+FROM chat\.workspaces`).
		WithArgs("ws-disabled").
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("disabled"))
	mock.ExpectRollback()

	store := storage.NewPGXMemberStore(mock)
	_, err = store.AddWorkspaceMember(context.Background(), "ws-disabled", "user-1", domain.WorkspaceRoleMember)
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXMemberStore_AddWorkspaceMember_MissingGeneralRollsBack(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	now := time.Now()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status\s+FROM chat\.workspaces`).
		WithArgs("ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("active"))
	mock.ExpectQuery(`INSERT INTO chat\.workspace_members`).
		WithArgs("ws-1", "user-1", "member").
		WillReturnRows(pgxmock.NewRows([]string{"workspace_id", "user_id", "role", "status", "joined_at"}).
			AddRow("ws-1", "user-1", "member", "active", now))
	mock.ExpectQuery(`SELECT id\s+FROM chat\.channels`).
		WithArgs("ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"id"}))
	mock.ExpectRollback()

	store := storage.NewPGXMemberStore(mock)
	_, err = store.AddWorkspaceMember(context.Background(), "ws-1", "user-1", domain.WorkspaceRoleMember)
	if !errors.Is(err, domain.ErrGeneralChannelMissing) {
		t.Fatalf("expected ErrGeneralChannelMissing, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXMemberStore_AddWorkspaceMember_GeneralInsertErrorRollsBack(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	now := time.Now()
	want := errors.New("channel member insert failed")
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status\s+FROM chat\.workspaces`).
		WithArgs("ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("active"))
	mock.ExpectQuery(`INSERT INTO chat\.workspace_members`).
		WithArgs("ws-1", "user-1", "member").
		WillReturnRows(pgxmock.NewRows([]string{"workspace_id", "user_id", "role", "status", "joined_at"}).
			AddRow("ws-1", "user-1", "member", "active", now))
	mock.ExpectQuery(`SELECT id\s+FROM chat\.channels`).
		WithArgs("ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("ch-geral"))
	mock.ExpectExec(`(?s)INSERT INTO chat\.channel_members.*FROM chat\.workspace_members wm.*wm\.role IN \('owner', 'admin', 'moderator', 'member'\)`).
		WithArgs("ch-geral", "ws-1", "user-1", "member").
		WillReturnError(want)
	mock.ExpectRollback()

	store := storage.NewPGXMemberStore(mock)
	_, err = store.AddWorkspaceMember(context.Background(), "ws-1", "user-1", domain.WorkspaceRoleMember)
	if !errors.Is(err, want) {
		t.Fatalf("expected insert error, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXMemberStore_AddWorkspaceMember_ExistingInactiveDoesNotSyncGeneral(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	now := time.Now()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status\s+FROM chat\.workspaces`).
		WithArgs("ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("active"))
	mock.ExpectQuery(`INSERT INTO chat\.workspace_members`).
		WithArgs("ws-1", "user-1", "member").
		WillReturnRows(pgxmock.NewRows([]string{"workspace_id", "user_id", "role", "status", "joined_at"}))
	mock.ExpectQuery(`SELECT wm\.workspace_id, wm\.user_id, wm\.role, wm\.status, wm\.joined_at`).
		WithArgs("ws-1", "user-1").
		WillReturnRows(pgxmock.NewRows([]string{"workspace_id", "user_id", "role", "status", "joined_at"}).
			AddRow("ws-1", "user-1", "member", "suspended", now))
	mock.ExpectCommit()

	store := storage.NewPGXMemberStore(mock)
	_, err = store.AddWorkspaceMember(context.Background(), "ws-1", "user-1", domain.WorkspaceRoleMember)
	if !errors.Is(err, domain.ErrAlreadyMember) {
		t.Fatalf("expected ErrAlreadyMember, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXMemberStore_ActivateWorkspaceMember_AddsGeneralInTransaction(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	now := time.Now()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status\s+FROM chat\.workspaces`).
		WithArgs("ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("active"))
	mock.ExpectQuery(`UPDATE chat\.workspace_members`).
		WithArgs("ws-1", "user-1").
		WillReturnRows(pgxmock.NewRows([]string{"workspace_id", "user_id", "role", "status", "joined_at"}).
			AddRow("ws-1", "user-1", "member", "active", now))
	mock.ExpectQuery(`SELECT id\s+FROM chat\.channels`).
		WithArgs("ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("ch-geral"))
	mock.ExpectExec(`(?s)INSERT INTO chat\.channel_members.*FROM chat\.workspace_members wm.*wm\.role IN \('owner', 'admin', 'moderator', 'member'\)`).
		WithArgs("ch-geral", "ws-1", "user-1", "member").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	store := storage.NewPGXMemberStore(mock)
	m, err := store.ActivateWorkspaceMember(context.Background(), "ws-1", "user-1")
	if err != nil {
		t.Fatalf("ActivateWorkspaceMember: %v", err)
	}
	if m.Status != domain.MemberStatusActive {
		t.Fatalf("expected active member, got %+v", m)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXMemberStore_ActivateWorkspaceMember_NotFoundRollsBack(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status\s+FROM chat\.workspaces`).
		WithArgs("ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("active"))
	mock.ExpectQuery(`UPDATE chat\.workspace_members`).
		WithArgs("ws-1", "missing").
		WillReturnRows(pgxmock.NewRows([]string{"workspace_id", "user_id", "role", "status", "joined_at"}))
	mock.ExpectRollback()

	store := storage.NewPGXMemberStore(mock)
	_, err = store.ActivateWorkspaceMember(context.Background(), "ws-1", "missing")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXMemberStore_ActivateWorkspaceMember_DisabledWorkspaceDenied(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status\s+FROM chat\.workspaces`).
		WithArgs("ws-disabled").
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("disabled"))
	mock.ExpectRollback()

	store := storage.NewPGXMemberStore(mock)
	_, err = store.ActivateWorkspaceMember(context.Background(), "ws-disabled", "user-1")
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXMemberStore_ActivateWorkspaceMember_MissingGeneralRollsBack(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	now := time.Now()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status\s+FROM chat\.workspaces`).
		WithArgs("ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("active"))
	mock.ExpectQuery(`UPDATE chat\.workspace_members`).
		WithArgs("ws-1", "user-1").
		WillReturnRows(pgxmock.NewRows([]string{"workspace_id", "user_id", "role", "status", "joined_at"}).
			AddRow("ws-1", "user-1", "member", "active", now))
	mock.ExpectQuery(`SELECT id\s+FROM chat\.channels`).
		WithArgs("ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"id"}))
	mock.ExpectRollback()

	store := storage.NewPGXMemberStore(mock)
	_, err = store.ActivateWorkspaceMember(context.Background(), "ws-1", "user-1")
	if !errors.Is(err, domain.ErrGeneralChannelMissing) {
		t.Fatalf("expected ErrGeneralChannelMissing, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXMemberStore_ActivateWorkspaceMember_UserIDScopedByWorkspace(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status\s+FROM chat\.workspaces`).
		WithArgs("ws-b").
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("active"))
	mock.ExpectQuery(`UPDATE chat\.workspace_members`).
		WithArgs("ws-b", "user-1").
		WillReturnRows(pgxmock.NewRows([]string{"workspace_id", "user_id", "role", "status", "joined_at"}))
	mock.ExpectRollback()

	store := storage.NewPGXMemberStore(mock)
	_, err = store.ActivateWorkspaceMember(context.Background(), "ws-b", "user-1")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXMemberStore_ActivateWorkspaceMember_UpdateErrorRollsBack(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	want := errors.New("update failed")
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status\s+FROM chat\.workspaces`).
		WithArgs("ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("active"))
	mock.ExpectQuery(`UPDATE chat\.workspace_members`).
		WithArgs("ws-1", "user-1").
		WillReturnError(want)
	mock.ExpectRollback()

	store := storage.NewPGXMemberStore(mock)
	_, err = store.ActivateWorkspaceMember(context.Background(), "ws-1", "user-1")
	if !errors.Is(err, want) {
		t.Fatalf("expected update error, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXMemberStore_EnsureGeneralMembership_AddsActiveMember(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	now := time.Now()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status\s+FROM chat\.workspaces`).
		WithArgs("ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("active"))
	mock.ExpectQuery(`SELECT wm\.workspace_id, wm\.user_id, wm\.role, wm\.status, wm\.joined_at`).
		WithArgs("ws-1", "user-1").
		WillReturnRows(pgxmock.NewRows([]string{"workspace_id", "user_id", "role", "status", "joined_at"}).
			AddRow("ws-1", "user-1", "member", "active", now))
	mock.ExpectQuery(`SELECT id\s+FROM chat\.channels`).
		WithArgs("ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("ch-geral"))
	mock.ExpectExec(`(?s)INSERT INTO chat\.channel_members.*FROM chat\.workspace_members wm.*wm\.role IN \('owner', 'admin', 'moderator', 'member'\)`).
		WithArgs("ch-geral", "ws-1", "user-1", "member").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	store := storage.NewPGXMemberStore(mock)
	if err := store.EnsureGeneralMembership(context.Background(), "ws-1", "user-1"); err != nil {
		t.Fatalf("EnsureGeneralMembership: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXMemberStore_EnsureGeneralMembership_InactiveMemberReturnsSentinel(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	now := time.Now()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status\s+FROM chat\.workspaces`).
		WithArgs("ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("active"))
	mock.ExpectQuery(`SELECT wm\.workspace_id, wm\.user_id, wm\.role, wm\.status, wm\.joined_at`).
		WithArgs("ws-1", "user-1").
		WillReturnRows(pgxmock.NewRows([]string{"workspace_id", "user_id", "role", "status", "joined_at"}).
			AddRow("ws-1", "user-1", "member", "left", now))
	mock.ExpectRollback()

	store := storage.NewPGXMemberStore(mock)
	err = store.EnsureGeneralMembership(context.Background(), "ws-1", "user-1")
	if !errors.Is(err, domain.ErrMemberInactive) {
		t.Fatalf("expected ErrMemberInactive, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXMemberStore_EnsureGeneralMembership_MissingMemberForbidden(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status\s+FROM chat\.workspaces`).
		WithArgs("ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("active"))
	mock.ExpectQuery(`SELECT wm\.workspace_id, wm\.user_id, wm\.role, wm\.status, wm\.joined_at`).
		WithArgs("ws-1", "missing").
		WillReturnRows(pgxmock.NewRows([]string{"workspace_id", "user_id", "role", "status", "joined_at"}))
	mock.ExpectRollback()

	store := storage.NewPGXMemberStore(mock)
	err = store.EnsureGeneralMembership(context.Background(), "ws-1", "missing")
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXMemberStore_EnsureGeneralMembership_MissingGeneralRollsBack(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	now := time.Now()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status\s+FROM chat\.workspaces`).
		WithArgs("ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("active"))
	mock.ExpectQuery(`SELECT wm\.workspace_id, wm\.user_id, wm\.role, wm\.status, wm\.joined_at`).
		WithArgs("ws-1", "user-1").
		WillReturnRows(pgxmock.NewRows([]string{"workspace_id", "user_id", "role", "status", "joined_at"}).
			AddRow("ws-1", "user-1", "member", "active", now))
	mock.ExpectQuery(`SELECT id\s+FROM chat\.channels`).
		WithArgs("ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"id"}))
	mock.ExpectRollback()

	store := storage.NewPGXMemberStore(mock)
	err = store.EnsureGeneralMembership(context.Background(), "ws-1", "user-1")
	if !errors.Is(err, domain.ErrGeneralChannelMissing) {
		t.Fatalf("expected ErrGeneralChannelMissing, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXMemberStore_EnsureGeneralMembership_CommitErrorPropagates(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	now := time.Now()
	want := errors.New("commit failed")
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status\s+FROM chat\.workspaces`).
		WithArgs("ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("active"))
	mock.ExpectQuery(`SELECT wm\.workspace_id, wm\.user_id, wm\.role, wm\.status, wm\.joined_at`).
		WithArgs("ws-1", "user-1").
		WillReturnRows(pgxmock.NewRows([]string{"workspace_id", "user_id", "role", "status", "joined_at"}).
			AddRow("ws-1", "user-1", "member", "active", now))
	mock.ExpectQuery(`SELECT id\s+FROM chat\.channels`).
		WithArgs("ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("ch-geral"))
	mock.ExpectExec(`(?s)INSERT INTO chat\.channel_members.*FROM chat\.workspace_members wm.*wm\.role IN \('owner', 'admin', 'moderator', 'member'\)`).
		WithArgs("ch-geral", "ws-1", "user-1", "member").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit().WillReturnError(want)

	store := storage.NewPGXMemberStore(mock)
	err = store.EnsureGeneralMembership(context.Background(), "ws-1", "user-1")
	if !errors.Is(err, want) {
		t.Fatalf("expected commit error, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXMemberStore_SyncGeneralMemberships_ActiveMembersOnly(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status\s+FROM chat\.workspaces`).
		WithArgs("ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("active"))
	mock.ExpectQuery(`SELECT id\s+FROM chat\.channels`).
		WithArgs("ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("ch-geral"))
	mock.ExpectExec(`(?s)INSERT INTO chat\.channel_members.*FROM chat\.workspace_members wm.*wm\.workspace_id = \$2.*wm\.status = 'active'.*ON CONFLICT \(channel_id, user_id\) DO NOTHING`).
		WithArgs("ch-geral", "ws-1", "member").
		WillReturnResult(pgxmock.NewResult("INSERT", 2))
	mock.ExpectCommit()

	store := storage.NewPGXMemberStore(mock)
	inserted, err := store.SyncGeneralMemberships(context.Background(), "ws-1")
	if err != nil {
		t.Fatalf("SyncGeneralMemberships: %v", err)
	}
	if inserted != 2 {
		t.Fatalf("expected 2 inserted rows, got %d", inserted)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXMemberStore_SyncGeneralMemberships_MissingGeneralRollsBack(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status\s+FROM chat\.workspaces`).
		WithArgs("ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("active"))
	mock.ExpectQuery(`SELECT id\s+FROM chat\.channels`).
		WithArgs("ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"id"}))
	mock.ExpectRollback()

	store := storage.NewPGXMemberStore(mock)
	_, err = store.SyncGeneralMemberships(context.Background(), "ws-1")
	if !errors.Is(err, domain.ErrGeneralChannelMissing) {
		t.Fatalf("expected ErrGeneralChannelMissing, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXMemberStore_SyncGeneralMemberships_WorkspaceNotFoundRollsBack(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status\s+FROM chat\.workspaces`).
		WithArgs("missing").
		WillReturnRows(pgxmock.NewRows([]string{"status"}))
	mock.ExpectRollback()

	store := storage.NewPGXMemberStore(mock)
	_, err = store.SyncGeneralMemberships(context.Background(), "missing")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXMemberStore_SyncGeneralMemberships_InsertErrorRollsBack(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	want := errors.New("sync insert failed")
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status\s+FROM chat\.workspaces`).
		WithArgs("ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("active"))
	mock.ExpectQuery(`SELECT id\s+FROM chat\.channels`).
		WithArgs("ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("ch-geral"))
	mock.ExpectExec(`(?s)INSERT INTO chat\.channel_members.*FROM chat\.workspace_members wm`).
		WithArgs("ch-geral", "ws-1", "member").
		WillReturnError(want)
	mock.ExpectRollback()

	store := storage.NewPGXMemberStore(mock)
	_, err = store.SyncGeneralMemberships(context.Background(), "ws-1")
	if !errors.Is(err, want) {
		t.Fatalf("expected insert error, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXMemberStore_AddChannelMember_DBError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`INSERT INTO chat.channel_members`).
		WithArgs("ch-1", "user-1", "member").
		WillReturnError(errors.New("db error"))

	store := storage.NewPGXMemberStore(mock)
	_, err = store.AddChannelMember(context.Background(), "ch-1", "user-1", domain.ChannelRoleMember)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestPGXMemberStore_GetChannelMember_DBError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`SELECT channel_id, user_id, role, joined_at`).
		WithArgs("ch-1", "user-1").
		WillReturnError(errors.New("connection lost"))

	store := storage.NewPGXMemberStore(mock)
	_, err = store.GetChannelMember(context.Background(), "ch-1", "user-1")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestPGXMemberStore_RemoveChannelMember_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT is_general FROM chat\.channels`).
		WithArgs("ch-1", "ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"is_general"}).AddRow(false))
	mock.ExpectExec(`DELETE FROM chat\.channel_members`).
		WithArgs("ch-1", "user-1").
		WillReturnResult(pgxmock.NewResult("DELETE", 1))
	mock.ExpectCommit()

	store := storage.NewPGXMemberStore(mock)
	if err := store.RemoveChannelMember(context.Background(), "ws-1", "ch-1", "user-1"); err != nil {
		t.Fatalf("RemoveChannelMember: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXMemberStore_RemoveChannelMember_NotMember_Idempotent(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT is_general FROM chat\.channels`).
		WithArgs("ch-1", "ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"is_general"}).AddRow(false))
	mock.ExpectExec(`DELETE FROM chat\.channel_members`).
		WithArgs("ch-1", "user-99").
		WillReturnResult(pgxmock.NewResult("DELETE", 0))
	mock.ExpectCommit()

	store := storage.NewPGXMemberStore(mock)
	if err := store.RemoveChannelMember(context.Background(), "ws-1", "ch-1", "user-99"); err != nil {
		t.Fatalf("non-member remove should be idempotent, got: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXMemberStore_RemoveChannelMember_ChannelNotInWorkspace_Idempotent(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT is_general FROM chat\.channels`).
		WithArgs("ch-1", "ws-other").
		WillReturnError(pgx.ErrNoRows)

	store := storage.NewPGXMemberStore(mock)
	if err := store.RemoveChannelMember(context.Background(), "ws-other", "ch-1", "user-1"); err != nil {
		t.Fatalf("channel not in workspace should be idempotent, got: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXMemberStore_RemoveChannelMember_GeneralChannel_Denied(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT is_general FROM chat\.channels`).
		WithArgs("ch-geral", "ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"is_general"}).AddRow(true))

	store := storage.NewPGXMemberStore(mock)
	err = store.RemoveChannelMember(context.Background(), "ws-1", "ch-geral", "user-1")
	if !errors.Is(err, domain.ErrCannotLeaveGeneralChannel) {
		t.Fatalf("general channel remove should return ErrCannotLeaveGeneralChannel, got: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}
