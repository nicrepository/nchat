package storage_test

import (
	"context"
	"regexp"
	"strings"
	"testing"

	pgxmock "github.com/pashagolub/pgxmock/v2"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// Regression guard for the channel-details member preview (issue #435).
//
// The defect these tests exist for: the preview used to cut the roster to the
// first N members alphabetically and only *then* look at presence, so an online
// member who sorted past that cut never appeared. The fix is structural — the
// presence filter is part of the query and runs before ORDER BY/LIMIT — so the
// assertions below are about where the filter sits in the statement, not about
// which rows a particular fixture happens to produce.

func onlineMemberCols() []string {
	return []string{"total_count", "online_count", "user_id", "display_name", "avatar_url", "role"}
}

func TestListOnlineChannelMemberProfiles_FiltersPresenceBeforeTheLimit(t *testing.T) {
	mock := newMock(t)
	online := []string{"user-31"}
	rows := pgxmock.NewRows(onlineMemberCols()).
		AddRow(31, 1, "user-31", "Zulmira", "", "member")
	mock.ExpectQuery(`(?s)WITH active_members AS`).
		WithArgs("ws-1", "ch-1", online, domain.MaxChannelDetailsMembers).
		WillReturnRows(rows)

	var capturedSQL string
	pool := &sqlCapturingPool{Pool: mock, captured: &capturedSQL}
	page, err := storage.NewPGXMemberStore(pool).ListOnlineChannelMemberProfiles(
		context.Background(), "ws-1", "ch-1", online, domain.MaxChannelDetailsMembers,
	)
	if err != nil {
		t.Fatalf("ListOnlineChannelMemberProfiles: %v", err)
	}

	// The presence predicate must live inside online_members, which the LIMIT
	// then reads from. If ANY($3) ever moved below the ORDER BY/LIMIT, the
	// original defect would be back.
	onlineCTE := regexp.MustCompile(`(?s)online_members AS \(\s*SELECT \* FROM active_members WHERE user_id = ANY\(\$3::uuid\[\]\)\s*\)`)
	if !onlineCTE.MatchString(capturedSQL) {
		t.Fatalf("presence must be filtered in the online_members CTE:\n%s", capturedSQL)
	}
	filterAt := strings.Index(capturedSQL, "ANY($3::uuid[])")
	limitAt := strings.Index(capturedSQL, "LIMIT $4")
	if filterAt == -1 || limitAt == -1 || filterAt > limitAt {
		t.Fatalf("the presence filter must precede the limit:\n%s", capturedSQL)
	}
	// The limited page must read from the filtered set, never from active_members.
	lateral := regexp.MustCompile(`(?s)LEFT JOIN LATERAL \(\s*SELECT \* FROM online_members\s*ORDER BY lower\(display_name\), user_id\s*LIMIT \$4\s*\)`)
	if !lateral.MatchString(capturedSQL) {
		t.Fatalf("the capped page must be taken from online_members, ordered deterministically:\n%s", capturedSQL)
	}

	if len(page.Online) != 1 || page.Online[0].UserID != "user-31" {
		t.Fatalf("expected the online member back, got %+v", page.Online)
	}
	// The channel's size is reported independently of how many are online.
	if page.TotalCount != 31 || page.OnlineCount != 1 {
		t.Fatalf("expected total 31 / online 1, got total %d / online %d", page.TotalCount, page.OnlineCount)
	}
	checkExpectations(t, mock)
}

func TestListOnlineChannelMemberProfiles_ScopesByWorkspaceChannelAndActiveMembership(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`(?s)WITH active_members AS`).
		WithArgs("ws-1", "ch-1", []string{"user-1"}, 10).
		WillReturnRows(pgxmock.NewRows(onlineMemberCols()).AddRow(1, 0, nil, nil, nil, nil))

	var capturedSQL string
	pool := &sqlCapturingPool{Pool: mock, captured: &capturedSQL}
	if _, err := storage.NewPGXMemberStore(pool).ListOnlineChannelMemberProfiles(
		context.Background(), "ws-1", "ch-1", []string{"user-1"}, 10,
	); err != nil {
		t.Fatalf("ListOnlineChannelMemberProfiles: %v", err)
	}

	// Isolation and liveness predicates the Security Review approved must all
	// still be part of the statement.
	for _, fragment := range []string{
		"c.workspace_id = $1::uuid",
		"c.status = 'active'",
		"wm.status = 'active'",
		"cm.channel_id = $2::uuid",
		"u.status = 'active'",
		"u.deleted_at IS NULL",
	} {
		if !strings.Contains(capturedSQL, fragment) {
			t.Fatalf("query lost the %q predicate:\n%s", fragment, capturedSQL)
		}
	}
	// Nothing beyond what the panel renders may be selected.
	for _, forbidden := range []string{"u.email", "u.auth_source", "u.external_subject", "joined_at"} {
		if strings.Contains(capturedSQL, forbidden) {
			t.Fatalf("member listing must not select %q:\n%s", forbidden, capturedSQL)
		}
	}
	checkExpectations(t, mock)
}

func TestListOnlineChannelMemberProfiles_ReportsTotalsWhenNobodyIsOnline(t *testing.T) {
	mock := newMock(t)
	// The lateral join contributes NULL member columns; the totals still arrive.
	mock.ExpectQuery(`(?s)WITH active_members AS`).
		WithArgs("ws-1", "ch-1", []string{}, domain.MaxChannelDetailsMembers).
		WillReturnRows(pgxmock.NewRows(onlineMemberCols()).AddRow(31, 0, nil, nil, nil, nil))

	page, err := storage.NewPGXMemberStore(mock).ListOnlineChannelMemberProfiles(
		context.Background(), "ws-1", "ch-1", nil, 0,
	)
	if err != nil {
		t.Fatalf("ListOnlineChannelMemberProfiles: %v", err)
	}
	if len(page.Online) != 0 {
		t.Fatalf("expected no online members, got %+v", page.Online)
	}
	if page.TotalCount != 31 || page.OnlineCount != 0 {
		t.Fatalf("the channel's size must survive an empty preview, got total %d / online %d",
			page.TotalCount, page.OnlineCount)
	}
	checkExpectations(t, mock)
}

func TestListOnlineChannelMemberProfiles_SendsAnEmptyArrayRatherThanNull(t *testing.T) {
	mock := newMock(t)
	// `= ANY(NULL)` is NULL, not false, so a nil slice would make the predicate
	// match nothing *and* nothing — an empty array is what makes it explicit.
	mock.ExpectQuery(`(?s)WITH active_members AS`).
		WithArgs("ws-1", "ch-1", []string{}, domain.MaxChannelDetailsMembers).
		WillReturnRows(pgxmock.NewRows(onlineMemberCols()).AddRow(0, 0, nil, nil, nil, nil))

	if _, err := storage.NewPGXMemberStore(mock).ListOnlineChannelMemberProfiles(
		context.Background(), "ws-1", "ch-1", nil, domain.MaxChannelDetailsMembers,
	); err != nil {
		t.Fatalf("ListOnlineChannelMemberProfiles: %v", err)
	}
	checkExpectations(t, mock)
}

func TestListOnlineChannelMemberProfiles_ClampsTheLimit(t *testing.T) {
	for name, tt := range map[string]struct{ asked, want int }{
		"unspecified":   {asked: 0, want: domain.MaxChannelDetailsMembers},
		"negative":      {asked: -5, want: domain.MaxChannelDetailsMembers},
		"above ceiling": {asked: 5_000, want: domain.MaxChannelDetailsMembers},
		"within range":  {asked: 7, want: 7},
	} {
		t.Run(name, func(t *testing.T) {
			mock := newMock(t)
			mock.ExpectQuery(`(?s)WITH active_members AS`).
				WithArgs("ws-1", "ch-1", []string{}, tt.want).
				WillReturnRows(pgxmock.NewRows(onlineMemberCols()).AddRow(0, 0, nil, nil, nil, nil))

			if _, err := storage.NewPGXMemberStore(mock).ListOnlineChannelMemberProfiles(
				context.Background(), "ws-1", "ch-1", []string{}, tt.asked,
			); err != nil {
				t.Fatalf("ListOnlineChannelMemberProfiles: %v", err)
			}
			checkExpectations(t, mock)
		})
	}
}

func TestListOnlineChannelMemberProfiles_MapsEveryReturnedMember(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`(?s)WITH active_members AS`).
		WithArgs("ws-1", "ch-1", []string{"u-1", "u-2"}, domain.MaxChannelDetailsMembers).
		WillReturnRows(pgxmock.NewRows(onlineMemberCols()).
			AddRow(9, 2, "u-1", "Ana", "/media/ana.png", "moderator").
			AddRow(9, 2, "u-2", "Bruno", "", "member"))

	page, err := storage.NewPGXMemberStore(mock).ListOnlineChannelMemberProfiles(
		context.Background(), "ws-1", "ch-1", []string{"u-1", "u-2"}, domain.MaxChannelDetailsMembers,
	)
	if err != nil {
		t.Fatalf("ListOnlineChannelMemberProfiles: %v", err)
	}
	if len(page.Online) != 2 {
		t.Fatalf("expected two members, got %+v", page.Online)
	}
	if page.Online[0].Role != domain.ChannelRoleModerator || page.Online[0].AvatarURL != "/media/ana.png" {
		t.Fatalf("unexpected first member: %+v", page.Online[0])
	}
	if page.Online[1].AvatarURL != "" || page.Online[1].Role != domain.ChannelRoleMember {
		t.Fatalf("unexpected second member: %+v", page.Online[1])
	}
	checkExpectations(t, mock)
}
