package storage_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	pgxmock "github.com/pashagolub/pgxmock/v2"

	"github.com/nicrepository/nchat/libs/go/platform/channelmembership"
	"github.com/nicrepository/nchat/libs/go/platform/uploadpolicy"
	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
	"github.com/nicrepository/nchat/services/admin-service/internal/storage"
)

const (
	userA = "11111111-1111-1111-1111-111111111111"
	userB = "22222222-2222-2222-2222-222222222222"
)

var epoch = time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)

func userColumns() []string {
	return []string{
		"id", "email", "display_name", "full_name", "avatar_url",
		"status", "auth_source", "external_provider", "last_login_at", "created_at",
		"platform_admin", "admin_roles", "memberships", "active_sessions",
	}
}

func userRow(id string, createdAt time.Time, memberships string) []any {
	return []any{
		id, "person@example.test", "Person", "Person Full", "",
		"active", "manual", "", nil, createdAt,
		false, []string{}, []byte(memberships), 0,
	}
}

// The extra row the query fetches is trimmed, and the cursor names the last row
// actually returned — not the one that was dropped. Getting that wrong skips a
// row between pages, silently.
func TestListUsers_TrimsTheProbeRowAndCursorsOnTheLastReturned(t *testing.T) {
	mock := newMock(t)
	rows := pgxmock.NewRows(userColumns()).
		AddRow(userRow(userA, epoch, "[]")...).
		AddRow(userRow(userB, epoch.Add(-time.Hour), "[]")...)
	mock.ExpectQuery(`FROM auth.users AS u`).WithArgs(anyArgs(10)...).WillReturnRows(rows)

	page, err := storage.NewPGXUserDirectoryStore(mock).
		ListUsers(context.Background(), domain.AdminUserFilter{Limit: 1})
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != userA {
		t.Fatalf("expected exactly the first row, got %+v", page.Items)
	}
	if !page.HasMore() {
		t.Fatal("a trimmed probe row means another page exists")
	}
	cursor, err := domain.DecodeCursor(page.NextCursor)
	if err != nil {
		t.Fatalf("DecodeCursor: %v", err)
	}
	if cursor.ID != userA || !cursor.At.Equal(epoch) {
		t.Fatalf("the cursor must name the last returned row, got %+v", cursor)
	}
}

func TestListUsers_LastPageHasNoCursor(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`FROM auth.users AS u`).WithArgs(anyArgs(10)...).
		WillReturnRows(pgxmock.NewRows(userColumns()).AddRow(userRow(userA, epoch, "[]")...))

	page, err := storage.NewPGXUserDirectoryStore(mock).
		ListUsers(context.Background(), domain.AdminUserFilter{Limit: 25})
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if page.HasMore() || page.NextCursor != "" {
		t.Fatalf("expected the last page, got %+v", page)
	}
}

// The pattern carries exactly ONE escape byte before each literal wildcard.
//
// This test reads bytes rather than comparing rendered strings on purpose. Go's
// %q renders a single 0x5c as two characters, so a string comparison looks like
// it proves double-escaping either way and proves nothing at all. Two backslashes
// would reach PostgreSQL as an escaped backslash followed by a live wildcard,
// which is the bug this pins the absence of. What the server then does with
// these bytes is proved separately, in directory_pg_integration_test.go.
func TestLikePattern_ProducesOneEscapeBytePerWildcard(t *testing.T) {
	cases := map[string][]byte{
		// %  1 0 0  \  %  \  _  o f f  %
		`100%_off`: {0x25, '1', '0', '0', 0x5c, 0x25, 0x5c, 0x5f, 'o', 'f', 'f', 0x25},
		`%`:        {0x25, 0x5c, 0x25, 0x25},
		`_`:        {0x25, 0x5c, 0x5f, 0x25},
		`\`:        {0x25, 0x5c, 0x5c, 0x25},
		`plain`:    {0x25, 'p', 'l', 'a', 'i', 'n', 0x25},
	}
	for term, want := range cases {
		t.Run(term, func(t *testing.T) {
			got := []byte(storage.LikePatternForTest(term))
			if string(got) != string(want) {
				t.Fatalf("likePattern(%q) = % x, want % x", term, got, want)
			}
		})
	}
	if storage.LikePatternForTest("   ") != "" {
		t.Fatal("a blank term is not a search and must produce no pattern")
	}
}

// The ESCAPE clause is stated on every ILIKE predicate rather than left to the
// server's default, so a reader of the query knows what the backslashes mean
// without knowing a default — and a server configured differently cannot change
// the answer.
func TestSearchQueries_StateTheirEscapeCharacter(t *testing.T) {
	for name, query := range map[string]string{
		"users":    storage.ListUsersQueryForTest(),
		"channels": storage.ListChannelsQueryForTest(),
	} {
		ilikes := strings.Count(query, "ILIKE")
		escapes := strings.Count(query, "ESCAPE")
		if ilikes == 0 || ilikes != escapes {
			t.Fatalf("%s: %d ILIKE predicates but %d ESCAPE clauses", name, ilikes, escapes)
		}
	}
}

// The search term reaches the query as an escaped ILIKE pattern. Without the
// escape a search for "%" matches everyone — a filter that quietly stops
// filtering on the screen an operator uses to find one person.
func TestListUsers_EscapesSearchWildcards(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`FROM auth.users AS u`).
		WithArgs(nil, nil, nil, `%100\%\_off%`, nil, false, nil, nil, nil, 26).
		WillReturnRows(pgxmock.NewRows(userColumns()))

	if _, err := storage.NewPGXUserDirectoryStore(mock).
		ListUsers(context.Background(), domain.AdminUserFilter{Query: "100%_off"}); err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

// An unset filter must be SQL NULL, which every predicate reads as "not
// applied". Passing an empty string would compare against ” and match nobody.
func TestListUsers_UnsetFiltersArriveAsNull(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`FROM auth.users AS u`).
		WithArgs(nil, nil, nil, nil, nil, false, nil, nil, nil, 26).
		WillReturnRows(pgxmock.NewRows(userColumns()))

	if _, err := storage.NewPGXUserDirectoryStore(mock).
		ListUsers(context.Background(), domain.AdminUserFilter{}); err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

// platform_admin=false is a real filter, not "unset". Collapsing them would
// hide every non-administrator instead of listing them.
func TestListUsers_PlatformAdminFalseIsAFilter(t *testing.T) {
	mock := newMock(t)
	value := false
	mock.ExpectQuery(`FROM auth.users AS u`).
		WithArgs(nil, nil, false, nil, nil, false, nil, nil, nil, 26).
		WillReturnRows(pgxmock.NewRows(userColumns()))

	if _, err := storage.NewPGXUserDirectoryStore(mock).
		ListUsers(context.Background(), domain.AdminUserFilter{PlatformAdmin: &value}); err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestListUsers_NeverSignedInIsItsOwnFilter(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`FROM auth.users AS u`).
		WithArgs(nil, nil, nil, nil, nil, true, nil, nil, nil, 26).
		WillReturnRows(pgxmock.NewRows(userColumns()))

	if _, err := storage.NewPGXUserDirectoryStore(mock).
		ListUsers(context.Background(), domain.AdminUserFilter{Inactivity: domain.ActivityFilterNever}); err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestListUsers_DecodesMemberships(t *testing.T) {
	mock := newMock(t)
	memberships := `[{"workspace_id":"33333333-3333-3333-3333-333333333333",` +
		`"workspace_name":"NChat","role":"admin","status":"active","joined_at":"2026-01-02T03:04:05Z"}]`
	mock.ExpectQuery(`FROM auth.users AS u`).WithArgs(anyArgs(10)...).
		WillReturnRows(pgxmock.NewRows(userColumns()).AddRow(userRow(userA, epoch, memberships)...))

	page, err := storage.NewPGXUserDirectoryStore(mock).
		ListUsers(context.Background(), domain.AdminUserFilter{})
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	roles := page.Items[0].WorkspaceRoles
	if len(roles) != 1 || roles[0].Role != "admin" || roles[0].WorkspaceName != "NChat" {
		t.Fatalf("unexpected memberships %+v", roles)
	}
}

func TestGetUser_NotFound(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`FROM auth.users AS u`).WithArgs(userA).WillReturnRows(pgxmock.NewRows(userColumns()))

	if _, err := storage.NewPGXUserDirectoryStore(mock).
		GetUser(context.Background(), userA); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// The transition is validated under the row lock, inside the transaction, so a
// concurrent change cannot make the decision stale between the read and the
// write.
func TestUpdateUserStatus_ValidatesUnderTheLockAndRevokesSessions(t *testing.T) {
	mock := newMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`FOR UPDATE`).WithArgs(userA).
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("active"))
	// The authorization anchor: a privileged write in flight must not commit
	// after this suspension. See mutation_authorization.go.
	mock.ExpectExec(`SELECT 1 FROM auth.admin_principals`).WithArgs(userA).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec(`UPDATE auth.users`).WithArgs(userA, "suspended").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery(`UPDATE auth.user_sessions`).WithArgs(userA, "admin_suspension").
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectExec(`UPDATE auth.oidc_exchange_codes`).WithArgs(userA).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	change, err := storage.NewPGXUserDirectoryStore(mock).
		UpdateUserStatus(context.Background(), userA, "suspended")
	if err != nil {
		t.Fatalf("UpdateUserStatus: %v", err)
	}
	if change.FromStatus != "active" || change.ToStatus != "suspended" || change.RevokedSessions != 2 {
		t.Fatalf("unexpected change %+v", change)
	}
}

// Activation restores nothing. No session is resurrected and no OIDC exchange
// code is un-consumed: the person signs in again.
func TestUpdateUserStatus_ActivationRestoresNothing(t *testing.T) {
	mock := newMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`FOR UPDATE`).WithArgs(userA).
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("suspended"))
	mock.ExpectExec(`SELECT 1 FROM auth.admin_principals`).WithArgs(userA).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec(`UPDATE auth.users`).WithArgs(userA, "active").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	change, err := storage.NewPGXUserDirectoryStore(mock).
		UpdateUserStatus(context.Background(), userA, "active")
	if err != nil {
		t.Fatalf("UpdateUserStatus: %v", err)
	}
	if change.RevokedSessions != 0 {
		t.Fatalf("activation must revoke nothing, got %d", change.RevokedSessions)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

// Two concurrent suspensions: the second observes 'suspended' under the lock
// and is refused, so only one of them claims to have made the change.
func TestUpdateUserStatus_TransitionOntoTheSameStatusConflicts(t *testing.T) {
	mock := newMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`FOR UPDATE`).WithArgs(userA).
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("suspended"))
	mock.ExpectExec(`SELECT 1 FROM auth.admin_principals`).WithArgs(userA).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectRollback()

	if _, err := storage.NewPGXUserDirectoryStore(mock).
		UpdateUserStatus(context.Background(), userA, "suspended"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
}

func TestUpdateUserStatus_UnknownUser(t *testing.T) {
	mock := newMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`FOR UPDATE`).WithArgs(userA).WillReturnError(pgx.ErrNoRows)
	mock.ExpectRollback()

	if _, err := storage.NewPGXUserDirectoryStore(mock).
		UpdateUserStatus(context.Background(), userA, "suspended"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestRevokeUserSessions_ReportsHowManyEnded(t *testing.T) {
	mock := newMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`FOR UPDATE`).WithArgs(userA).
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("active"))
	// Revoking the login behind an administrative session revokes that
	// authority too, so it takes the same anchor.
	mock.ExpectExec(`SELECT 1 FROM auth.admin_principals`).WithArgs(userA).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(`UPDATE auth.user_sessions`).WithArgs(userA, "admin_revocation").
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(3))
	mock.ExpectCommit()

	revoked, err := storage.NewPGXUserDirectoryStore(mock).
		RevokeUserSessions(context.Background(), userA)
	if err != nil {
		t.Fatalf("RevokeUserSessions: %v", err)
	}
	if revoked != 3 {
		t.Fatalf("expected 3 revoked sessions, got %d", revoked)
	}
}

// The last-administrator invariant is evaluated after the delete and inside the
// transaction, so the count describes the world the commit would create. A
// revocation that would leave nobody rolls back.
func TestRevokeAdminRole_RefusesToLeaveThePlatformWithoutAnAdministrator(t *testing.T) {
	mock := newMock(t)
	mock.ExpectBegin()
	mock.ExpectExec(`pg_advisory_xact_lock`).WithArgs(pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	// The authorization anchor, so a privileged write in flight cannot commit
	// after this role is taken away. See mutation_authorization.go.
	mock.ExpectExec(`SELECT 1 FROM auth.admin_principals`).WithArgs(userA).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec(`DELETE FROM auth.admin_principal_roles`).
		WithArgs(userA, "platform-superuser").
		WillReturnResult(pgxmock.NewResult("DELETE", 1))
	mock.ExpectQuery(`count\(DISTINCT pr.user_id\)`).WithArgs("admin.superuser").
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectRollback()

	if err := storage.NewPGXUserDirectoryStore(mock).
		RevokeAdminRole(context.Background(), userA, "platform-superuser"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestRevokeAdminRole_CommitsWhenAnotherAdministratorRemains(t *testing.T) {
	mock := newMock(t)
	mock.ExpectBegin()
	mock.ExpectExec(`pg_advisory_xact_lock`).WithArgs(pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	// The authorization anchor, so a privileged write in flight cannot commit
	// after this role is taken away. See mutation_authorization.go.
	mock.ExpectExec(`SELECT 1 FROM auth.admin_principals`).WithArgs(userA).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec(`DELETE FROM auth.admin_principal_roles`).
		WithArgs(userA, "platform-superuser").
		WillReturnResult(pgxmock.NewResult("DELETE", 1))
	mock.ExpectQuery(`count\(DISTINCT pr.user_id\)`).WithArgs("admin.superuser").
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectCommit()

	if err := storage.NewPGXUserDirectoryStore(mock).
		RevokeAdminRole(context.Background(), userA, "platform-superuser"); err != nil {
		t.Fatalf("RevokeAdminRole: %v", err)
	}
}

func TestRevokeAdminRole_UnheldRoleIsNotFound(t *testing.T) {
	mock := newMock(t)
	mock.ExpectBegin()
	mock.ExpectExec(`pg_advisory_xact_lock`).WithArgs(pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	// The authorization anchor, so a privileged write in flight cannot commit
	// after this role is taken away. See mutation_authorization.go.
	mock.ExpectExec(`SELECT 1 FROM auth.admin_principals`).WithArgs(userA).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec(`DELETE FROM auth.admin_principal_roles`).
		WithArgs(userA, "platform-auditor").
		WillReturnResult(pgxmock.NewResult("DELETE", 0))
	mock.ExpectRollback()

	if err := storage.NewPGXUserDirectoryStore(mock).
		RevokeAdminRole(context.Background(), userA, "platform-auditor"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// Platform administration is not granted to an account the platform has already
// decided should not be signing in.
func TestGrantAdminRole_RefusesASuspendedTarget(t *testing.T) {
	mock := newMock(t)
	mock.ExpectBegin()
	mock.ExpectExec(`pg_advisory_xact_lock`).WithArgs(pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(`FOR UPDATE`).WithArgs(userA).
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("suspended"))
	mock.ExpectExec(`SELECT 1 FROM auth.admin_principals`).WithArgs(userA).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectRollback()

	if err := storage.NewPGXUserDirectoryStore(mock).
		GrantAdminRole(context.Background(), userA, "platform-auditor", userB); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
}

func TestGrantAdminRole_RefusesAnUnknownRole(t *testing.T) {
	mock := newMock(t)
	mock.ExpectBegin()
	mock.ExpectExec(`pg_advisory_xact_lock`).WithArgs(pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(`FOR UPDATE`).WithArgs(userA).
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("active"))
	mock.ExpectQuery(`FROM auth.admin_roles WHERE slug`).WithArgs("does-not-exist").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectRollback()

	if err := storage.NewPGXUserDirectoryStore(mock).
		GrantAdminRole(context.Background(), userA, "does-not-exist", userB); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// A principal suspended out of band is not silently reactivated by a role
// grant.
func TestGrantAdminRole_RefusesASuspendedPrincipal(t *testing.T) {
	mock := newMock(t)
	mock.ExpectBegin()
	mock.ExpectExec(`pg_advisory_xact_lock`).WithArgs(pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(`FOR UPDATE`).WithArgs(userA).
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("active"))
	mock.ExpectQuery(`FROM auth.admin_roles WHERE slug`).WithArgs("platform-auditor").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectExec(`INSERT INTO auth.admin_principals`).WithArgs(userA).
		WillReturnResult(pgxmock.NewResult("INSERT", 0))
	mock.ExpectQuery(`SELECT status FROM auth.admin_principals`).WithArgs(userA).
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("suspended"))
	mock.ExpectRollback()

	if err := storage.NewPGXUserDirectoryStore(mock).
		GrantAdminRole(context.Background(), userA, "platform-auditor", userB); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Channels and conversations
// ---------------------------------------------------------------------------

func channelColumns() []string {
	return []string{
		"id", "workspace_id", "workspace_name", "slug", "display_name",
		"type", "status", "is_general", "member_count", "moderator_count",
		"created_by_name", "created_by_email", "created_at", "last_activity_at",
	}
}

func channelRow(id string, status string, isGeneral bool) []any {
	return []any{
		id, userB, "NChat", "eng", "Engenharia",
		"private", status, isGeneral, 12, 2,
		"Owner", "owner@example.test", epoch, nil,
	}
}

// A private channel is listed — that is what admin.channels.read authorizes —
// and the row still carries no message and no member name.
func TestListChannels_ListsPrivateChannelsWithoutContent(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`FROM chat.channels AS c`).WithArgs(anyArgs(10)...).
		WillReturnRows(pgxmock.NewRows(channelColumns()).AddRow(channelRow(userA, "active", false)...))

	page, err := storage.NewPGXChannelDirectoryStore(mock).
		ListChannels(context.Background(), domain.AdminChannelFilter{})
	if err != nil {
		t.Fatalf("ListChannels: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].Type != "private" {
		t.Fatalf("unexpected page %+v", page.Items)
	}
	if page.Items[0].LastActivityAt != nil {
		t.Fatal("a channel with no messages has no activity timestamp")
	}
}

func TestListChannels_AppliesTheAllowlistedFilters(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`FROM chat.channels AS c`).
		WithArgs(userB, "private", "archived", `%eng%`, 5, pgxmock.AnyArg(), nil, nil, nil, 26).
		WillReturnRows(pgxmock.NewRows(channelColumns()))

	if _, err := storage.NewPGXChannelDirectoryStore(mock).ListChannels(context.Background(), domain.AdminChannelFilter{
		Query: "eng", WorkspaceID: userB, Type: "private", Status: "archived",
		MinMembers: 5, ActiveWithin: "30d",
	}); err != nil {
		t.Fatalf("ListChannels: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

// The workspace's #geral channel is immutable in chat-service, and this console
// must not become a second way around that.
func TestUpdateChannelStatus_RefusesTheGeneralChannel(t *testing.T) {
	mock := newMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`FROM chat.channels WHERE id`).WithArgs(userA).
		WillReturnRows(pgxmock.NewRows([]string{"status", "is_general"}).AddRow("active", true))
	mock.ExpectRollback()

	if _, err := storage.NewPGXChannelDirectoryStore(mock).
		UpdateChannelStatus(context.Background(), userA, "archived"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
}

// Two operators archiving at once: the row lock serializes them and the second
// one loses, so there is one change and one conflict rather than two audit rows
// claiming the same thing.
func TestUpdateChannelStatus_AlreadyArchivedConflicts(t *testing.T) {
	mock := newMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`FROM chat.channels WHERE id`).WithArgs(userA).
		WillReturnRows(pgxmock.NewRows([]string{"status", "is_general"}).AddRow("archived", false))
	mock.ExpectRollback()

	if _, err := storage.NewPGXChannelDirectoryStore(mock).
		UpdateChannelStatus(context.Background(), userA, "archived"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
}

func TestUpdateChannelStatus_ArchivesAndReturnsTheNewState(t *testing.T) {
	mock := newMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`FROM chat.channels WHERE id`).WithArgs(userA).
		WillReturnRows(pgxmock.NewRows([]string{"status", "is_general"}).AddRow("active", false))
	mock.ExpectExec(`UPDATE chat.channels`).WithArgs(userA, "archived").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery(`FROM chat.channels AS c`).WithArgs(userA).
		WillReturnRows(pgxmock.NewRows(channelColumns()).AddRow(channelRow(userA, "archived", false)...))
	mock.ExpectCommit()

	channel, err := storage.NewPGXChannelDirectoryStore(mock).
		UpdateChannelStatus(context.Background(), userA, "archived")
	if err != nil {
		t.Fatalf("UpdateChannelStatus: %v", err)
	}
	if channel.Status != "archived" {
		t.Fatalf("expected the new state back, got %q", channel.Status)
	}
}

func TestUpdateChannelStatus_UnknownChannel(t *testing.T) {
	mock := newMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`FROM chat.channels WHERE id`).WithArgs(userA).WillReturnError(pgx.ErrNoRows)
	mock.ExpectRollback()

	if _, err := storage.NewPGXChannelDirectoryStore(mock).
		UpdateChannelStatus(context.Background(), userA, "archived"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestListConversations_ReturnsMetadataOnly(t *testing.T) {
	mock := newMock(t)
	columns := []string{
		"id", "workspace_id", "workspace_name", "type", "status",
		"participant_count", "message_count", "created_at", "updated_at", "last_activity_at",
	}
	mock.ExpectQuery(`FROM chat.dm_conversations AS d`).WithArgs(anyArgs(6)...).
		WillReturnRows(pgxmock.NewRows(columns).
			AddRow(userA, userB, "NChat", "group", "active", 4, int64(120), epoch, epoch, &epoch))

	page, err := storage.NewPGXChannelDirectoryStore(mock).
		ListConversations(context.Background(), domain.AdminConversationFilter{})
	if err != nil {
		t.Fatalf("ListConversations: %v", err)
	}
	item := page.Items[0]
	if item.ParticipantCount != 4 || item.MessageCount != 120 {
		t.Fatalf("unexpected metadata %+v", item)
	}
}

// The conversation query must never read a message body. This is a regression
// guard on the query text itself, because the refusal to expose DM content is a
// property of what the SQL selects, not of what the handler chooses to render.
func TestConversationQuery_NeverSelectsMessageContent(t *testing.T) {
	forbidden := []string{"body_text", "search_vector", "dm_members.user_id", "d.title"}
	query := storage.ConversationQueryForTest()
	for _, needle := range forbidden {
		if strings.Contains(query, needle) {
			t.Fatalf("the conversation query must not reference %q", needle)
		}
	}
}

// ---------------------------------------------------------------------------
// Policies
// ---------------------------------------------------------------------------

func TestUpdateAntiSpamPolicy_ReturnsTheDiff(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`message_rate_limit_per_minute`).WithArgs(userB, 30).
		WillReturnRows(pgxmock.NewRows([]string{"previous", "current", "id", "slug", "name", "status"}).
			AddRow(60, 30, userB, "default", "NChat", "active"))

	policy, change, err := storage.NewPGXPolicyStore(mock).
		UpdateAntiSpamPolicy(context.Background(), userB, 30)
	if err != nil {
		t.Fatalf("UpdateAntiSpamPolicy: %v", err)
	}
	if policy.MessageRateLimitPerMinute != 30 || change.From != 60 || change.To != 30 {
		t.Fatalf("unexpected result %+v / %+v", policy, change)
	}
}

func TestUpdateUploadPolicy_UnknownWorkspace(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`max_upload_bytes`).WithArgs(userB, uploadpolicy.DefaultMaxUploadBytes).
		WillReturnError(pgx.ErrNoRows)

	if _, _, err := storage.NewPGXPolicyStore(mock).
		UpdateUploadPolicy(context.Background(), userB, uploadpolicy.DefaultMaxUploadBytes); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestListPolicies_PagesOnTheWorkspaceOrdering(t *testing.T) {
	columns := []string{"id", "slug", "name", "status", "rate", "bytes", "created_at"}
	mock := newMock(t)
	mock.ExpectQuery(`FROM chat.workspaces AS w`).WithArgs(anyArgs(3)...).
		WillReturnRows(pgxmock.NewRows(columns).
			AddRow(userA, "a", "A", "active", 60, uploadpolicy.DefaultMaxUploadBytes, epoch).
			AddRow(userB, "b", "B", "active", 30, uploadpolicy.MinMaxUploadBytes, epoch.Add(-time.Hour)))

	page, err := storage.NewPGXPolicyStore(mock).
		ListAntiSpamPolicies(context.Background(), domain.Cursor{}, 1)
	if err != nil {
		t.Fatalf("ListAntiSpamPolicies: %v", err)
	}
	if len(page.Items) != 1 || !page.HasMore() {
		t.Fatalf("expected one item and another page, got %+v", page)
	}
	cursor, err := domain.DecodeCursor(page.NextCursor)
	if err != nil || cursor.ID != userA {
		t.Fatalf("unexpected cursor %+v (%v)", cursor, err)
	}
}

func TestListUploadPolicies_ProjectsOnlyTheUploadLimit(t *testing.T) {
	columns := []string{"id", "slug", "name", "status", "rate", "bytes", "created_at"}
	mock := newMock(t)
	mock.ExpectQuery(`FROM chat.workspaces AS w`).WithArgs(anyArgs(3)...).
		WillReturnRows(pgxmock.NewRows(columns).
			AddRow(userA, "a", "A", "active", 60, uploadpolicy.DefaultMaxUploadBytes, epoch))

	page, err := storage.NewPGXPolicyStore(mock).
		ListUploadPolicies(context.Background(), domain.Cursor{}, 25)
	if err != nil {
		t.Fatalf("ListUploadPolicies: %v", err)
	}
	if page.Items[0].MaxUploadBytes != uploadpolicy.DefaultMaxUploadBytes {
		t.Fatalf("unexpected policy %+v", page.Items[0])
	}
}

// Every store refuses rather than panicking when it was never wired.
func TestManagementStores_NilPoolIsUnavailable(t *testing.T) {
	users := storage.NewPGXUserDirectoryStore(nil)
	if _, err := users.ListUsers(context.Background(), domain.AdminUserFilter{}); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
	if _, err := users.GetUser(context.Background(), userA); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
	if _, err := users.UpdateUserStatus(context.Background(), userA, "active"); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
	if _, err := users.RevokeUserSessions(context.Background(), userA); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
	if err := users.GrantAdminRole(context.Background(), userA, "r", userB); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
	if err := users.RevokeAdminRole(context.Background(), userA, "r"); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}

	channels := storage.NewPGXChannelDirectoryStore(nil)
	if _, err := channels.ListChannels(context.Background(), domain.AdminChannelFilter{}); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
	if _, err := channels.GetChannel(context.Background(), userA); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
	if _, err := channels.UpdateChannelStatus(context.Background(), userA, "archived"); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
	if _, err := channels.ListConversations(context.Background(), domain.AdminConversationFilter{}); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}

	policies := storage.NewPGXPolicyStore(nil)
	if _, err := policies.ListAntiSpamPolicies(context.Background(), domain.Cursor{}, 25); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
	if _, err := policies.ListUploadPolicies(context.Background(), domain.Cursor{}, 25); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
	if _, _, err := policies.UpdateAntiSpamPolicy(context.Background(), userA, 30); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
	if _, _, err := policies.UpdateUploadPolicy(context.Background(), userA, uploadpolicy.MinMaxUploadBytes); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
}

// anyArgs builds a positional matcher for a query whose bindings a given spec
// is not about. The specs that *are* about the bindings name every value
// explicitly instead.
func anyArgs(n int) []any {
	args := make([]any, 0, n)
	for range n {
		args = append(args, pgxmock.AnyArg())
	}
	return args
}

func roleCatalogueColumns() []string {
	return []string{"slug", "description", "capabilities", "held", "granted_at", "granted_by"}
}

// The detail view loads the identity row, the role catalogue and the channel
// count. The catalogue answers both halves at once — what exists and what this
// person holds — so a screen offering a role does not need a second endpoint.
func TestGetUser_LoadsTheDetailAndTheRoleCatalogue(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`FROM auth.users AS u`).WithArgs(userA).
		WillReturnRows(pgxmock.NewRows(userColumns()).AddRow(userRow(userA, epoch, "[]")...))
	mock.ExpectQuery(`FROM auth.admin_roles AS r`).WithArgs(userA).
		WillReturnRows(pgxmock.NewRows(roleCatalogueColumns()).
			AddRow("platform-auditor", "Read-only.", []string{"admin.audit.read"}, true, &epoch, "root@example.test").
			AddRow("platform-superuser", "Everything.", []string{"admin.superuser"}, false, nil, ""))
	mock.ExpectQuery(`FROM chat.channel_members`).WithArgs(userA).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(7))

	detail, err := storage.NewPGXUserDirectoryStore(mock).GetUser(context.Background(), userA)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if len(detail.AvailableRoles) != 2 {
		t.Fatalf("the whole catalogue must be returned, got %+v", detail.AvailableRoles)
	}
	if len(detail.RoleGrants) != 1 || detail.RoleGrants[0].Slug != "platform-auditor" {
		t.Fatalf("only held roles are grants, got %+v", detail.RoleGrants)
	}
	if detail.RoleGrants[0].GrantedBy != "root@example.test" || detail.RoleGrants[0].GrantedAt.IsZero() {
		t.Fatalf("a grant must carry who made it and when, got %+v", detail.RoleGrants[0])
	}
	if detail.ChannelCount != 7 {
		t.Fatalf("expected 7 channel memberships, got %d", detail.ChannelCount)
	}
}

// A failure anywhere in the detail load propagates: a half-loaded record must
// not render as a person with no roles.
func TestGetUser_PropagatesEachFailure(t *testing.T) {
	t.Run("identity query", func(t *testing.T) {
		mock := newMock(t)
		mock.ExpectQuery(`FROM auth.users AS u`).WithArgs(userA).WillReturnError(errors.New("boom"))
		if _, err := storage.NewPGXUserDirectoryStore(mock).GetUser(context.Background(), userA); err == nil {
			t.Fatal("expected the failure to propagate")
		}
	})
	t.Run("role catalogue", func(t *testing.T) {
		mock := newMock(t)
		mock.ExpectQuery(`FROM auth.users AS u`).WithArgs(userA).
			WillReturnRows(pgxmock.NewRows(userColumns()).AddRow(userRow(userA, epoch, "[]")...))
		mock.ExpectQuery(`FROM auth.admin_roles AS r`).WithArgs(userA).WillReturnError(errors.New("boom"))
		if _, err := storage.NewPGXUserDirectoryStore(mock).GetUser(context.Background(), userA); err == nil {
			t.Fatal("expected the failure to propagate")
		}
	})
	t.Run("channel count", func(t *testing.T) {
		mock := newMock(t)
		mock.ExpectQuery(`FROM auth.users AS u`).WithArgs(userA).
			WillReturnRows(pgxmock.NewRows(userColumns()).AddRow(userRow(userA, epoch, "[]")...))
		mock.ExpectQuery(`FROM auth.admin_roles AS r`).WithArgs(userA).
			WillReturnRows(pgxmock.NewRows(roleCatalogueColumns()))
		mock.ExpectQuery(`FROM chat.channel_members`).WithArgs(userA).WillReturnError(errors.New("boom"))
		if _, err := storage.NewPGXUserDirectoryStore(mock).GetUser(context.Background(), userA); err == nil {
			t.Fatal("expected the failure to propagate")
		}
	})
}

// A membership payload that is not the agreed shape is an error, not an empty
// membership list: silently showing somebody as belonging to nothing is worse
// than failing.
func TestListUsers_MalformedMembershipsAreAnError(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`FROM auth.users AS u`).WithArgs(anyArgs(10)...).
		WillReturnRows(pgxmock.NewRows(userColumns()).AddRow(userRow(userA, epoch, "not json")...))

	if _, err := storage.NewPGXUserDirectoryStore(mock).
		ListUsers(context.Background(), domain.AdminUserFilter{}); err == nil {
		t.Fatal("expected the decode failure to propagate")
	}
}

func TestListUsers_QueryFailurePropagates(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`FROM auth.users AS u`).WithArgs(anyArgs(10)...).WillReturnError(errors.New("boom"))
	if _, err := storage.NewPGXUserDirectoryStore(mock).
		ListUsers(context.Background(), domain.AdminUserFilter{}); err == nil {
		t.Fatal("expected the failure to propagate")
	}
}

// The detail view resolves the two authorities in one query and keeps them
// apart by role, because a channel moderator and a workspace owner are not the
// same thing.
func TestGetChannel_ResolvesModeratorsAndWorkspaceAdmins(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`FROM chat.channels AS c`).WithArgs(userA).
		WillReturnRows(pgxmock.NewRows(channelColumns()).AddRow(channelRow(userA, "active", false)...))
	mock.ExpectQuery(`FROM chat.channels AS c\s+LEFT JOIN chat.channel_categories`).WithArgs(userA).
		WillReturnRows(pgxmock.NewRows([]string{"category", "messages"}).AddRow("Times", int64(4200)))
	mock.ExpectQuery(`FROM chat.channel_members AS cm`).WithArgs(userA, userB, 50).
		WillReturnRows(pgxmock.NewRows([]string{"user_id", "display_name", "email", "role"}).
			AddRow(userA, "Ana", "ana@example.test", "moderator").
			AddRow(userB, "Root", "root@example.test", "owner"))
	mock.ExpectQuery(`WHERE cm.channel_id = \$1::uuid`).WithArgs(userA, 50).
		WillReturnRows(pgxmock.NewRows([]string{"user_id", "display_name", "email", "role"}).
			AddRow(userA, "Ana", "ana@example.test", "moderator").
			AddRow(userB, "Root", "root@example.test", "member"))

	detail, err := storage.NewPGXChannelDirectoryStore(mock).GetChannel(context.Background(), userA)
	if err != nil {
		t.Fatalf("GetChannel: %v", err)
	}
	// The membership preview is separate from the two authority lists: it says
	// who is in the channel, not who governs it.
	if len(detail.Members) != 2 {
		t.Fatalf("expected the bounded membership preview, got %+v", detail.Members)
	}
	if len(detail.Moderators) != 1 || detail.Moderators[0].Role != "moderator" {
		t.Fatalf("unexpected moderators %+v", detail.Moderators)
	}
	if len(detail.WorkspaceAdmins) != 1 || detail.WorkspaceAdmins[0].Role != "owner" {
		t.Fatalf("unexpected workspace admins %+v", detail.WorkspaceAdmins)
	}
	if detail.CategoryName != "Times" || detail.MessageCount != 4200 {
		t.Fatalf("unexpected aggregates %+v", detail)
	}
}

func TestGetChannel_NotFound(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`FROM chat.channels AS c`).WithArgs(userA).
		WillReturnRows(pgxmock.NewRows(channelColumns()))
	if _, err := storage.NewPGXChannelDirectoryStore(mock).
		GetChannel(context.Background(), userA); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestListChannels_QueryFailurePropagates(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`FROM chat.channels AS c`).WithArgs(anyArgs(10)...).WillReturnError(errors.New("boom"))
	if _, err := storage.NewPGXChannelDirectoryStore(mock).
		ListChannels(context.Background(), domain.AdminChannelFilter{}); err == nil {
		t.Fatal("expected the failure to propagate")
	}
}

func TestListConversations_QueryFailurePropagates(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`FROM chat.dm_conversations AS d`).WithArgs(anyArgs(6)...).WillReturnError(errors.New("boom"))
	if _, err := storage.NewPGXChannelDirectoryStore(mock).
		ListConversations(context.Background(), domain.AdminConversationFilter{}); err == nil {
		t.Fatal("expected the failure to propagate")
	}
}

func TestListConversations_AppliesTheFilters(t *testing.T) {
	mock := newMock(t)
	columns := []string{
		"id", "workspace_id", "workspace_name", "type", "status",
		"participant_count", "message_count", "created_at", "updated_at", "last_activity_at",
	}
	mock.ExpectQuery(`FROM chat.dm_conversations AS d`).
		WithArgs(userB, "direct", "archived", nil, nil, 26).
		WillReturnRows(pgxmock.NewRows(columns))

	if _, err := storage.NewPGXChannelDirectoryStore(mock).ListConversations(context.Background(),
		domain.AdminConversationFilter{WorkspaceID: userB, Type: "direct", Status: "archived"}); err != nil {
		t.Fatalf("ListConversations: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

// A cursor is bound as a timestamp and a uuid, never interpolated.
func TestListUsers_CursorIsBoundAsTwoValues(t *testing.T) {
	mock := newMock(t)
	cursor := domain.Cursor{At: epoch, ID: userA}
	mock.ExpectQuery(`FROM auth.users AS u`).
		WithArgs(nil, nil, nil, nil, nil, false, nil, epoch, userA, 26).
		WillReturnRows(pgxmock.NewRows(userColumns()))

	if _, err := storage.NewPGXUserDirectoryStore(mock).
		ListUsers(context.Background(), domain.AdminUserFilter{Cursor: cursor}); err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestUpdateUploadPolicy_ReturnsTheDiff(t *testing.T) {
	mock := newMock(t)
	value := 100 * uploadpolicy.BytesPerMiB
	mock.ExpectQuery(`max_upload_bytes`).WithArgs(userB, value).
		WillReturnRows(pgxmock.NewRows([]string{"previous", "current", "id", "slug", "name", "status"}).
			AddRow(uploadpolicy.DefaultMaxUploadBytes, value, userB, "default", "NChat", "active"))

	policy, change, err := storage.NewPGXPolicyStore(mock).UpdateUploadPolicy(context.Background(), userB, value)
	if err != nil {
		t.Fatalf("UpdateUploadPolicy: %v", err)
	}
	if policy.MaxUploadBytes != value || change.From != uploadpolicy.DefaultMaxUploadBytes || change.To != value {
		t.Fatalf("unexpected result %+v / %+v", policy, change)
	}
}

func TestUpdateAntiSpamPolicy_FailurePropagates(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`message_rate_limit_per_minute`).WithArgs(userB, 30).WillReturnError(errors.New("boom"))
	if _, _, err := storage.NewPGXPolicyStore(mock).
		UpdateAntiSpamPolicy(context.Background(), userB, 30); err == nil {
		t.Fatal("expected the failure to propagate")
	}
}

func TestUpdateAntiSpamPolicy_UnknownWorkspace(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`message_rate_limit_per_minute`).WithArgs(userB, 30).WillReturnError(pgx.ErrNoRows)
	if _, _, err := storage.NewPGXPolicyStore(mock).
		UpdateAntiSpamPolicy(context.Background(), userB, 30); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestListPolicies_QueryFailurePropagates(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`FROM chat.workspaces AS w`).WithArgs(anyArgs(3)...).WillReturnError(errors.New("boom"))
	if _, err := storage.NewPGXPolicyStore(mock).
		ListAntiSpamPolicies(context.Background(), domain.Cursor{}, 25); err == nil {
		t.Fatal("expected the failure to propagate")
	}
}

func TestGrantAdminRole_Succeeds(t *testing.T) {
	mock := newMock(t)
	mock.ExpectBegin()
	mock.ExpectExec(`pg_advisory_xact_lock`).WithArgs(pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(`FOR UPDATE`).WithArgs(userA).
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("active"))
	mock.ExpectQuery(`FROM auth.admin_roles WHERE slug`).WithArgs("platform-auditor").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectExec(`INSERT INTO auth.admin_principals`).WithArgs(userA).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectQuery(`SELECT status FROM auth.admin_principals`).WithArgs(userA).
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("active"))
	mock.ExpectExec(`INSERT INTO auth.admin_principal_roles`).WithArgs(userA, "platform-auditor", userB).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	if err := storage.NewPGXUserDirectoryStore(mock).
		GrantAdminRole(context.Background(), userA, "platform-auditor", userB); err != nil {
		t.Fatalf("GrantAdminRole: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestGrantAdminRole_UnknownTarget(t *testing.T) {
	mock := newMock(t)
	mock.ExpectBegin()
	mock.ExpectExec(`pg_advisory_xact_lock`).WithArgs(pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(`FOR UPDATE`).WithArgs(userA).WillReturnError(pgx.ErrNoRows)
	mock.ExpectRollback()

	if err := storage.NewPGXUserDirectoryStore(mock).
		GrantAdminRole(context.Background(), userA, "platform-auditor", userB); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// A transaction that cannot be opened is a failure, not a silent no-op.
func TestMutations_BeginFailurePropagates(t *testing.T) {
	users := func() pgxmock.PgxPoolIface {
		mock := newMock(t)
		mock.ExpectBegin().WillReturnError(errors.New("boom"))
		return mock
	}
	store := storage.NewPGXUserDirectoryStore(users())
	if _, err := store.UpdateUserStatus(context.Background(), userA, "suspended"); err == nil {
		t.Fatal("expected the failure to propagate")
	}
	if _, err := storage.NewPGXUserDirectoryStore(users()).RevokeUserSessions(context.Background(), userA); err == nil {
		t.Fatal("expected the failure to propagate")
	}
	if err := storage.NewPGXUserDirectoryStore(users()).GrantAdminRole(context.Background(), userA, "r", userB); err == nil {
		t.Fatal("expected the failure to propagate")
	}
	if err := storage.NewPGXUserDirectoryStore(users()).RevokeAdminRole(context.Background(), userA, "r"); err == nil {
		t.Fatal("expected the failure to propagate")
	}
	if _, err := storage.NewPGXChannelDirectoryStore(users()).
		UpdateChannelStatus(context.Background(), userA, "archived"); err == nil {
		t.Fatal("expected the failure to propagate")
	}
}

// ---------------------------------------------------------------------------
// New filters
// ---------------------------------------------------------------------------

// The workspace role reaches the query as a bound value, and it is the seventh
// parameter — not folded into the search term and not concatenated.
func TestListUsers_BindsTheWorkspaceRoleFilter(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`FROM auth.users AS u`).
		WithArgs(nil, nil, nil, nil, nil, false, "owner", nil, nil, 26).
		WillReturnRows(pgxmock.NewRows(userColumns()))

	if _, err := storage.NewPGXUserDirectoryStore(mock).
		ListUsers(context.Background(), domain.AdminUserFilter{WorkspaceRole: "owner"}); err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestListChannels_BindsTheAdministeredByFilter(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`FROM chat.channels AS c`).
		WithArgs(nil, nil, nil, nil, nil, nil, userA, nil, nil, 26).
		WillReturnRows(pgxmock.NewRows(channelColumns()))

	if _, err := storage.NewPGXChannelDirectoryStore(mock).
		ListChannels(context.Background(), domain.AdminChannelFilter{AdministeredBy: userA}); err != nil {
		t.Fatalf("ListChannels: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Channel membership
// ---------------------------------------------------------------------------

func TestAddChannelMembers_AdmitsEligibleTargets(t *testing.T) {
	mock := newMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`FROM chat.channels\s+WHERE id`).WithArgs(userA).
		WillReturnRows(pgxmock.NewRows([]string{"workspace_id", "is_general"}).
			AddRow(userB, false))
	mock.ExpectQuery(`WITH eligible AS`).WithArgs(userB, userA, []string{userA}, "member").
		WillReturnRows(pgxmock.NewRows([]string{"eligible", "inserted"}).AddRow(1, 1))
	mock.ExpectQuery(`count\(\*\) FROM chat.channel_members`).WithArgs(userA).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(13))
	mock.ExpectCommit()

	change, err := storage.NewPGXChannelDirectoryStore(mock).
		AddChannelMembers(context.Background(), userA, []string{userA})
	if err != nil {
		t.Fatalf("AddChannelMembers: %v", err)
	}
	if change.Added != 1 || change.AlreadyMembers != 0 || change.MemberCount != 13 {
		t.Fatalf("unexpected change %+v", change)
	}
	// The workspace is derived from the channel, never taken from the request.
	if change.WorkspaceID != userB {
		t.Fatalf("expected the channel's own workspace, got %q", change.WorkspaceID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

// Fewer eligible rows than requested means a target does not belong here. The
// whole add rolls back rather than applying to whoever happened to qualify.
func TestAddChannelMembers_IneligibleTargetRollsTheWholeAddBack(t *testing.T) {
	mock := newMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`FROM chat.channels\s+WHERE id`).WithArgs(userA).
		WillReturnRows(pgxmock.NewRows([]string{"workspace_id", "is_general"}).
			AddRow(userB, false))
	mock.ExpectQuery(`WITH eligible AS`).WithArgs(userB, userA, []string{userA, userB}, "member").
		WillReturnRows(pgxmock.NewRows([]string{"eligible", "inserted"}).AddRow(1, 1))
	mock.ExpectRollback()

	if _, err := storage.NewPGXChannelDirectoryStore(mock).
		AddChannelMembers(context.Background(), userA, []string{userA, userB}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

// A repeat of the same add inserts nobody and reports that, rather than
// claiming an addition that did not happen.
func TestAddChannelMembers_RepeatAddsNobody(t *testing.T) {
	mock := newMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`FROM chat.channels\s+WHERE id`).WithArgs(userA).
		WillReturnRows(pgxmock.NewRows([]string{"workspace_id", "is_general"}).
			AddRow(userB, false))
	mock.ExpectQuery(`WITH eligible AS`).WithArgs(userB, userA, []string{userA}, "member").
		WillReturnRows(pgxmock.NewRows([]string{"eligible", "inserted"}).AddRow(1, 0))
	mock.ExpectQuery(`count\(\*\) FROM chat.channel_members`).WithArgs(userA).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(12))
	mock.ExpectCommit()

	change, err := storage.NewPGXChannelDirectoryStore(mock).
		AddChannelMembers(context.Background(), userA, []string{userA})
	if err != nil {
		t.Fatalf("AddChannelMembers: %v", err)
	}
	if change.Added != 0 || change.AlreadyMembers != 1 {
		t.Fatalf("unexpected change %+v", change)
	}
}

func TestAddChannelMembers_UnknownChannel(t *testing.T) {
	mock := newMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`FROM chat.channels\s+WHERE id`).WithArgs(userA).WillReturnError(pgx.ErrNoRows)
	mock.ExpectRollback()

	if _, err := storage.NewPGXChannelDirectoryStore(mock).
		AddChannelMembers(context.Background(), userA, []string{userB}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestRemoveChannelMember_RemovesAndCounts(t *testing.T) {
	mock := newMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`FROM chat.channels\s+WHERE id`).WithArgs(userA).
		WillReturnRows(pgxmock.NewRows([]string{"workspace_id", "is_general"}).
			AddRow(userB, false))
	mock.ExpectExec(`DELETE FROM chat.channel_members`).WithArgs(userA, userB).
		WillReturnResult(pgxmock.NewResult("DELETE", 1))
	mock.ExpectQuery(`count\(\*\) FROM chat.channel_members`).WithArgs(userA).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(11))
	mock.ExpectCommit()

	change, err := storage.NewPGXChannelDirectoryStore(mock).
		RemoveChannelMember(context.Background(), userA, userB)
	if err != nil {
		t.Fatalf("RemoveChannelMember: %v", err)
	}
	if !change.Removed || change.MemberCount != 11 {
		t.Fatalf("unexpected change %+v", change)
	}
}

// Removing somebody who is not a member succeeds and says nothing changed: the
// caller's intent already holds, and a retry must not look like a failure.
func TestRemoveChannelMember_IsIdempotent(t *testing.T) {
	mock := newMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`FROM chat.channels\s+WHERE id`).WithArgs(userA).
		WillReturnRows(pgxmock.NewRows([]string{"workspace_id", "is_general"}).
			AddRow(userB, false))
	mock.ExpectExec(`DELETE FROM chat.channel_members`).WithArgs(userA, userB).
		WillReturnResult(pgxmock.NewResult("DELETE", 0))
	mock.ExpectQuery(`count\(\*\) FROM chat.channel_members`).WithArgs(userA).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(12))
	mock.ExpectCommit()

	change, err := storage.NewPGXChannelDirectoryStore(mock).
		RemoveChannelMember(context.Background(), userA, userB)
	if err != nil {
		t.Fatalf("a repeat removal must succeed, got %v", err)
	}
	if change.Removed {
		t.Fatal("nothing was removed, and the result must say so")
	}
}

// #geral is refused, mirroring chat-service's ErrCannotLeaveGeneralChannel.
func TestRemoveChannelMember_RefusesTheGeneralChannel(t *testing.T) {
	mock := newMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`FROM chat.channels\s+WHERE id`).WithArgs(userA).
		WillReturnRows(pgxmock.NewRows([]string{"workspace_id", "is_general"}).
			AddRow(userB, true))
	mock.ExpectRollback()

	if _, err := storage.NewPGXChannelDirectoryStore(mock).
		RemoveChannelMember(context.Background(), userA, userB); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
}

func TestChannelMembership_NilPoolIsUnavailable(t *testing.T) {
	store := storage.NewPGXChannelDirectoryStore(nil)
	if _, err := store.AddChannelMembers(context.Background(), userA, []string{userB}); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
	if _, err := store.RemoveChannelMember(context.Background(), userA, userB); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
}

func TestChannelMembership_BeginFailurePropagates(t *testing.T) {
	for _, run := range []func(*storage.PGXChannelDirectoryStore) error{
		func(s *storage.PGXChannelDirectoryStore) error {
			_, err := s.AddChannelMembers(context.Background(), userA, []string{userB})
			return err
		},
		func(s *storage.PGXChannelDirectoryStore) error {
			_, err := s.RemoveChannelMember(context.Background(), userA, userB)
			return err
		},
	} {
		mock := newMock(t)
		mock.ExpectBegin().WillReturnError(errors.New("boom"))
		if err := run(storage.NewPGXChannelDirectoryStore(mock)); err == nil {
			t.Fatal("expected the failure to propagate")
		}
	}
}

// The eligibility half of the add is the shared rule, embedded verbatim. If the
// two ever drift, the second writer of chat.channel_members stops enforcing
// what the first one does — which is the whole reason the constant exists.
func TestAddChannelMembersQuery_EmbedsTheSharedEligibilityRule(t *testing.T) {
	if !strings.Contains(storage.AddChannelMembersQueryForTest(), channelmembership.EligibleTargetsCTE) {
		t.Fatal("the add statement must embed channelmembership.EligibleTargetsCTE verbatim")
	}
	// Every join of the shared rule is load-bearing; naming them here makes a
	// silent weakening of the constant fail in this service too.
	rule := channelmembership.EligibleTargetsCTE
	for _, clause := range []string{
		"chat.workspace_members",
		"wm.status = 'active'",
		"w.status = 'active'",
		"c.status = 'active'",
		"u.status = 'active' AND u.deleted_at IS NULL",
	} {
		if !strings.Contains(rule, clause) {
			t.Fatalf("the shared eligibility rule must still require %q", clause)
		}
	}
}

// The picker only decides what to offer; the add decides what to admit. They
// must not drift apart, so the search restates every condition the shared rule
// enforces — and this fails if either side loses one.
func TestMemberCandidateQuery_MirrorsTheSharedEligibilityRule(t *testing.T) {
	candidates := storage.MemberCandidateQueryForTest()
	rule := channelmembership.EligibleTargetsCTE
	for _, condition := range []string{
		"wm.status = 'active'",
		"w.status = 'active'",
		"c.status = 'active'",
		"u.status = 'active' AND u.deleted_at IS NULL",
	} {
		if !strings.Contains(rule, condition) {
			t.Fatalf("the shared rule no longer requires %q", condition)
		}
		if !strings.Contains(candidates, condition) {
			t.Fatalf("the candidate search must offer only what the add would admit: missing %q", condition)
		}
	}
	// Somebody already in the channel is not a candidate, and that is answered
	// by the same scan rather than one lookup per person.
	if !strings.Contains(candidates, "NOT EXISTS") {
		t.Fatal("the candidate search must exclude current members in the same statement")
	}
	// The workspace comes from the channel, never from a parameter: that is what
	// keeps the search inside one tenant.
	if strings.Contains(candidates, "$4") {
		t.Fatal("the candidate search takes exactly three parameters: channel, term, limit")
	}
}

func TestListMemberCandidates_BindsTheEscapedSearchTerm(t *testing.T) {
	mock := newMock(t)
	columns := []string{"id", "display_name", "full_name", "email", "avatar_url", "role"}
	mock.ExpectQuery(`FROM chat.workspace_members AS wm`).
		WithArgs(userA, `%100\%\_off%`, 10).
		WillReturnRows(pgxmock.NewRows(columns))

	if _, err := storage.NewPGXChannelDirectoryStore(mock).
		ListMemberCandidates(context.Background(), userA, `100%_off`, 10); err != nil {
		t.Fatalf("ListMemberCandidates: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestListMemberCandidates_EmptyTermIsNotAFilter(t *testing.T) {
	mock := newMock(t)
	columns := []string{"id", "display_name", "full_name", "email", "avatar_url", "role"}
	mock.ExpectQuery(`FROM chat.workspace_members AS wm`).
		WithArgs(userA, nil, 10).
		WillReturnRows(pgxmock.NewRows(columns).
			AddRow(userB, "Ana", "Ana Lima", "ana@example.test", "", "member"))

	candidates, err := storage.NewPGXChannelDirectoryStore(mock).
		ListMemberCandidates(context.Background(), userA, "  ", 10)
	if err != nil {
		t.Fatalf("ListMemberCandidates: %v", err)
	}
	if len(candidates) != 1 || candidates[0].DisplayName != "Ana" {
		t.Fatalf("unexpected candidates %+v", candidates)
	}
}

func TestListMemberCandidates_FailurePropagates(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`FROM chat.workspace_members AS wm`).WithArgs(userA, nil, 10).
		WillReturnError(errors.New("boom"))
	if _, err := storage.NewPGXChannelDirectoryStore(mock).
		ListMemberCandidates(context.Background(), userA, "", 10); err == nil {
		t.Fatal("expected the failure to propagate")
	}
}

func TestListMemberCandidates_NilPoolIsUnavailable(t *testing.T) {
	if _, err := storage.NewPGXChannelDirectoryStore(nil).
		ListMemberCandidates(context.Background(), userA, "", 10); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
}

// The channel lock this store takes must stay the shared protocol: the same
// object, the same mode. If one service drifts to a weaker lock, member_count
// stops being the total the operation produced and no unit test would notice.
func TestLockChannelQuery_ObeysTheSharedProtocol(t *testing.T) {
	lock := storage.LockChannelQueryForTest()
	for _, clause := range []string{"FROM chat.channels", "WHERE id = $1::uuid", "FOR UPDATE"} {
		if !strings.Contains(lock, clause) {
			t.Fatalf("the membership lock must still be %q, got:\n%s", clause, lock)
		}
	}
	if strings.Contains(lock, "FOR SHARE") {
		t.Fatal("a shared lock lets two mutations count the same total; the protocol requires FOR UPDATE")
	}
	// The shared constant chat-service embeds names the same object and mode.
	shared := channelmembership.LockChannelSQL
	for _, clause := range []string{"chat.channels", "FOR UPDATE"} {
		if !strings.Contains(shared, clause) {
			t.Fatalf("channelmembership.LockChannelSQL must still be %q", clause)
		}
	}
}
