package storage_test

import (
	"context"
	"strings"
	"testing"

	pgxmock "github.com/pashagolub/pgxmock/v2"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// Participant-listing tests for the group-details panel (issue #441).
//
// A group's participant list is not a channel's member list: it is scoped by
// conversation_id, it excludes people who left, and — unlike the channel
// panel — it is never filtered by presence, so an offline participant always
// appears.

func dmParticipantCols() []string {
	return []string{"user_id", "display_name", "avatar_url", "total_count"}
}

func TestListParticipantProfiles_ScopesByWorkspaceConversationAndActiveMembership(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`(?s)FROM chat\.dm_members dm`).
		WithArgs("ws-1", "conv-1", domain.MaxDMDetailsParticipants).
		WillReturnRows(pgxmock.NewRows(dmParticipantCols()).
			AddRow("u-1", "Ana", "/media/ana.png", 7).
			AddRow("u-2", "Bruno", "", 7))

	var capturedSQL string
	pool := &sqlCapturingPool{Pool: mock, captured: &capturedSQL}
	page, err := storage.NewPGXDMStore(pool).ListParticipantProfiles(
		context.Background(), "ws-1", "conv-1", domain.MaxDMDetailsParticipants,
	)
	if err != nil {
		t.Fatalf("ListParticipantProfiles: %v", err)
	}

	// Tenancy and liveness predicates. dm.status = 'active' is what removes
	// someone who left the group; dc.workspace_id is what keeps a conversation
	// UUID from another tenant from resolving here.
	for _, fragment := range []string{
		"dc.workspace_id = $1::uuid",
		"dc.status = 'active'",
		"wm.status = 'active'",
		"dm.conversation_id = $2::uuid",
		"dm.status = 'active'",
		"u.status = 'active'",
		"u.deleted_at IS NULL",
		"COUNT(*) OVER ()",
		"ORDER BY lower(u.display_name), u.id",
		"LIMIT $3",
	} {
		if !strings.Contains(capturedSQL, fragment) {
			t.Fatalf("query is missing %q:\n%s", fragment, capturedSQL)
		}
	}
	// A participant list is not a directory export, and it is never scoped by a
	// channel: chat.channel_members has no business in this query.
	for _, forbidden := range []string{"u.email", "u.auth_source", "u.external_subject", "channel_id", "chat.channels"} {
		if strings.Contains(capturedSQL, forbidden) {
			t.Fatalf("participant listing must not reference %q:\n%s", forbidden, capturedSQL)
		}
	}

	if len(page.Participants) != 2 || page.Participants[0].UserID != "u-1" {
		t.Fatalf("unexpected participants: %+v", page.Participants)
	}
	if page.Participants[0].AvatarURL != "/media/ana.png" || page.Participants[1].AvatarURL != "" {
		t.Fatalf("unexpected avatars: %+v", page.Participants)
	}
	// The total describes every active participant, not the page.
	if page.TotalCount != 7 {
		t.Fatalf("expected the full total, got %d", page.TotalCount)
	}
	checkExpectations(t, mock)
}

func TestListParticipantProfiles_ClampsTheLimit(t *testing.T) {
	for name, tt := range map[string]struct{ asked, want int }{
		"unspecified":   {asked: 0, want: domain.MaxDMDetailsParticipants},
		"negative":      {asked: -4, want: domain.MaxDMDetailsParticipants},
		"above ceiling": {asked: 900, want: domain.MaxDMDetailsParticipants},
		"within range":  {asked: 6, want: 6},
	} {
		t.Run(name, func(t *testing.T) {
			mock := newMock(t)
			mock.ExpectQuery(`(?s)FROM chat\.dm_members dm`).
				WithArgs("ws-1", "conv-1", tt.want).
				WillReturnRows(pgxmock.NewRows(dmParticipantCols()))

			if _, err := storage.NewPGXDMStore(mock).ListParticipantProfiles(
				context.Background(), "ws-1", "conv-1", tt.asked,
			); err != nil {
				t.Fatalf("ListParticipantProfiles: %v", err)
			}
			checkExpectations(t, mock)
		})
	}
}

func TestListParticipantProfiles_ReturnsAnEmptyPageWithoutRows(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`(?s)FROM chat\.dm_members dm`).
		WithArgs("ws-1", "conv-1", domain.MaxDMDetailsParticipants).
		WillReturnRows(pgxmock.NewRows(dmParticipantCols()))

	page, err := storage.NewPGXDMStore(mock).ListParticipantProfiles(
		context.Background(), "ws-1", "conv-1", 0,
	)
	if err != nil {
		t.Fatalf("ListParticipantProfiles: %v", err)
	}
	if len(page.Participants) != 0 || page.TotalCount != 0 {
		t.Fatalf("expected an empty page, got %+v", page)
	}
	checkExpectations(t, mock)
}
