package storage_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	pgxmock "github.com/pashagolub/pgxmock/v2"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// The administrable channel roster (issue #469).
//
// Its whole reason to exist is that the details panel's preview is filtered by
// presence, so these tests are mostly about what the statement does *not* do:
// no presence argument, no presence predicate, and a total that describes the
// membership rather than the page.

func rosterCols() []string {
	return []string{"user_id", "display_name", "avatar_url", "role", "total_count"}
}

func TestListChannelMemberRoster_SelectsMembershipWithoutPresence(t *testing.T) {
	mock := newMock(t)
	rows := pgxmock.NewRows(rosterCols()).
		AddRow("user-1", "Ana", "/media/a.png", "moderator", 12).
		AddRow("user-2", "Bruno", "", "member", 12)
	mock.ExpectQuery(`(?s)WITH active_members AS`).
		WithArgs("ws-1", "ch-1", domain.MaxChannelDetailsMembers).
		WillReturnRows(rows)

	var capturedSQL string
	pool := &sqlCapturingPool{Pool: mock, captured: &capturedSQL}
	page, err := storage.NewPGXMemberStore(pool).ListChannelMemberRoster(
		context.Background(), "ws-1", "ch-1", domain.MaxChannelDetailsMembers,
	)
	if err != nil {
		t.Fatalf("ListChannelMemberRoster: %v", err)
	}

	if len(page.Members) != 2 || page.Members[0].UserID != "user-1" ||
		page.Members[0].Role != domain.ChannelRoleModerator || page.Members[1].AvatarURL != "" {
		t.Fatalf("unexpected page: %+v", page.Members)
	}
	// The membership's size, not the page's: a roster capped at thirty in a
	// channel of twelve hundred still has to say twelve hundred.
	if page.TotalCount != 12 {
		t.Fatalf("TotalCount = %d, want the window function's 12", page.TotalCount)
	}
	// A presence predicate here would silently turn the roster back into the
	// preview it exists to complement.
	for _, forbidden := range []string{"ANY($3", "online", "presence"} {
		if strings.Contains(capturedSQL, forbidden) {
			t.Fatalf("the roster query must not mention %q:\n%s", forbidden, capturedSQL)
		}
	}
	if !strings.Contains(capturedSQL, "COUNT(*) OVER ()") {
		t.Fatalf("the total must come from the same statement:\n%s", capturedSQL)
	}
	checkExpectations(t, mock)
}

// The isolation and liveness predicates are the same ones the online preview
// applies: one population, read two ways.
func TestListChannelMemberRoster_KeepsTheActiveMembershipPredicate(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`(?s)WITH active_members AS`).
		WithArgs("ws-1", "ch-1", 10).
		WillReturnRows(pgxmock.NewRows(rosterCols()))

	var capturedSQL string
	pool := &sqlCapturingPool{Pool: mock, captured: &capturedSQL}
	if _, err := storage.NewPGXMemberStore(pool).ListChannelMemberRoster(
		context.Background(), "ws-1", "ch-1", 10,
	); err != nil {
		t.Fatalf("ListChannelMemberRoster: %v", err)
	}

	for _, fragment := range []string{
		"c.workspace_id = $1::uuid",
		"c.status = 'active'",
		"wm.status = 'active'",
		"cm.channel_id = $2::uuid",
		"u.status = 'active'",
		"u.deleted_at IS NULL",
		"ORDER BY lower(display_name), user_id",
		"LIMIT $3",
	} {
		if !strings.Contains(capturedSQL, fragment) {
			t.Fatalf("query lost the %q predicate:\n%s", fragment, capturedSQL)
		}
	}
	// A roster is not a directory: nothing beyond what a row renders is read.
	for _, forbidden := range []string{"u.email", "u.auth_source", "u.external_subject", "joined_at"} {
		if strings.Contains(capturedSQL, forbidden) {
			t.Fatalf("the roster must not select %q:\n%s", forbidden, capturedSQL)
		}
	}
	checkExpectations(t, mock)
}

// An absent or oversized limit is clamped to the server's cap rather than
// letting a caller ask for the whole membership in one page.
func TestListChannelMemberRoster_ClampsTheLimit(t *testing.T) {
	for _, limit := range []int{0, -5, domain.MaxChannelDetailsMembers + 1} {
		mock := newMock(t)
		mock.ExpectQuery(`(?s)WITH active_members AS`).
			WithArgs("ws-1", "ch-1", domain.MaxChannelDetailsMembers).
			WillReturnRows(pgxmock.NewRows(rosterCols()))

		if _, err := storage.NewPGXMemberStore(mock).ListChannelMemberRoster(
			context.Background(), "ws-1", "ch-1", limit,
		); err != nil {
			t.Fatalf("limit %d: %v", limit, err)
		}
		checkExpectations(t, mock)
	}
}

// A failed query is a failure, never an empty roster: "nobody is in this
// channel" is a statement the panel would render.
func TestListChannelMemberRoster_PropagatesTheQueryFailure(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`(?s)WITH active_members AS`).
		WithArgs("ws-1", "ch-1", 10).
		WillReturnError(errors.New("boom"))

	if _, err := storage.NewPGXMemberStore(mock).ListChannelMemberRoster(
		context.Background(), "ws-1", "ch-1", 10,
	); err == nil {
		t.Fatal("a query failure was reported as an empty roster")
	}
	checkExpectations(t, mock)
}
