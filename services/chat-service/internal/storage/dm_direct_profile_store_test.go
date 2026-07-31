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

// Counterpart-resolution tests for the 1:1 profile panel (issue #443).
//
// The question this query answers is "who is the *other* person here", and the
// answer must come from the database's own membership rows. The caller is a
// predicate (dm.user_id <> caller), never a selector, so no request can nominate
// whose profile is returned and the caller can never be handed their own.

func directProfileCols() []string {
	return []string{"user_id", "display_name", "avatar_url", "email"}
}

func TestGetDirectCounterpartProfile_ScopesByWorkspaceConversationTypeAndCaller(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`(?s)FROM chat\.dm_conversations dc`).
		WithArgs("ws-1", "conv-1", "user-me").
		WillReturnRows(pgxmock.NewRows(directProfileCols()).
			AddRow("user-other", "Juliane Lino", "/media/juliane.png", "juliane@nic.test"))

	var capturedSQL string
	pool := &sqlCapturingPool{Pool: mock, captured: &capturedSQL}
	profile, err := storage.NewPGXDMStore(pool).GetDirectCounterpartProfile(
		context.Background(), "ws-1", "conv-1", "user-me",
	)
	if err != nil {
		t.Fatalf("GetDirectCounterpartProfile: %v", err)
	}

	// Every predicate that makes this the right person, and only this person.
	// dc.type = 'direct' is what refuses a group ID; dm.user_id <> $3 is what
	// makes returning the caller's own profile impossible.
	//
	// The two caller_* EXISTS clauses are the TOCTOU fix: the caller's own
	// membership is re-established in the statement that projects the
	// counterpart, so an authorisation granted by an earlier query cannot be
	// spent here after it has been revoked.
	for _, fragment := range []string{
		"dc.workspace_id = $1::uuid",
		"dc.status = 'active'",
		"dc.type = 'direct'",
		"dc.id = $2::uuid",
		"wm.status = 'active'",
		"dm.status = 'active'",
		"dm.user_id <> $3::uuid",
		"u.status = 'active'",
		"u.deleted_at IS NULL",
		"caller_dm.user_id = $3::uuid",
		"caller_dm.status = 'active'",
		"caller_dm.conversation_id = dc.id",
		"caller_wm.user_id = $3::uuid",
		"caller_wm.status = 'active'",
		"caller_wm.workspace_id = dc.workspace_id",
		// Two rows, so a corrupt 'direct' conversation is detectable rather than
		// silently reduced to whichever participant sorted first.
		"LIMIT 2",
	} {
		if !strings.Contains(capturedSQL, fragment) {
			t.Fatalf("query is missing %q:\n%s", fragment, capturedSQL)
		}
	}
	// The projection is the profile card and nothing more. A wildcard would let
	// any future auth.users column leak into a response by accident.
	for _, forbidden := range []string{
		"SELECT *", "u.auth_source", "u.external_subject", "u.last_login_at",
		"u.email_verified_at", "u.status AS", "channel_id", "chat.channels",
	} {
		if strings.Contains(capturedSQL, forbidden) {
			t.Fatalf("direct profile query must not reference %q:\n%s", forbidden, capturedSQL)
		}
	}

	want := domain.DMDirectProfile{
		UserID:      "user-other",
		DisplayName: "Juliane Lino",
		AvatarURL:   "/media/juliane.png",
		Email:       "juliane@nic.test",
	}
	if profile != want {
		t.Fatalf("profile = %+v, want %+v", profile, want)
	}
	checkExpectations(t, mock)
}

func TestGetDirectCounterpartProfile_RevokedCallerYieldsNothing(t *testing.T) {
	mock := newMock(t)
	// The caller's membership was revoked after some earlier check said yes, so
	// the EXISTS clauses no longer hold and the statement matches nothing. The
	// store must not be able to tell this apart from "no such conversation" —
	// and, above all, must not return the counterpart's name or e-mail.
	mock.ExpectQuery(`(?s)FROM chat\.dm_conversations dc`).
		WithArgs("ws-1", "conv-1", "user-me").
		WillReturnRows(pgxmock.NewRows(directProfileCols()))

	profile, err := storage.NewPGXDMStore(mock).GetDirectCounterpartProfile(
		context.Background(), "ws-1", "conv-1", "user-me",
	)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if profile != (domain.DMDirectProfile{}) {
		t.Fatalf("a revoked caller must receive no profile, got %+v", profile)
	}
	checkExpectations(t, mock)
}

func TestGetDirectCounterpartProfile_NoCounterpartIsNotFound(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`(?s)FROM chat\.dm_conversations dc`).
		WithArgs("ws-1", "conv-1", "user-me").
		WillReturnRows(pgxmock.NewRows(directProfileCols()))

	_, err := storage.NewPGXDMStore(mock).GetDirectCounterpartProfile(
		context.Background(), "ws-1", "conv-1", "user-me",
	)
	// A conversation whose other participant left, was suspended or deleted has
	// no profile to show. That is a 404 for the caller, never an empty profile.
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	checkExpectations(t, mock)
}

func TestGetDirectCounterpartProfile_SeveralCounterpartsIsInconsistent(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`(?s)FROM chat\.dm_conversations dc`).
		WithArgs("ws-1", "conv-1", "user-me").
		WillReturnRows(pgxmock.NewRows(directProfileCols()).
			AddRow("user-a", "Ana", "", "ana@nic.test").
			AddRow("user-b", "Bruno", "", "bruno@nic.test"))

	profile, err := storage.NewPGXDMStore(mock).GetDirectCounterpartProfile(
		context.Background(), "ws-1", "conv-1", "user-me",
	)
	// The domain says a direct conversation is a pair. Picking one of the two
	// would show an arbitrary person's e-mail as "the other participant", so the
	// store refuses and lets the corruption surface.
	if !errors.Is(err, domain.ErrInconsistentDirectConversation) {
		t.Fatalf("err = %v, want ErrInconsistentDirectConversation", err)
	}
	if profile != (domain.DMDirectProfile{}) {
		t.Fatalf("no profile may be returned for a corrupt conversation, got %+v", profile)
	}
	checkExpectations(t, mock)
}

func TestGetDirectCounterpartProfile_KeepsIdentitiesApartWhenNamesMatch(t *testing.T) {
	mock := newMock(t)
	// Two people can share a display name; they are still two rows with two IDs,
	// and the query selected by ID.
	mock.ExpectQuery(`(?s)FROM chat\.dm_conversations dc`).
		WithArgs("ws-1", "conv-1", "user-me").
		WillReturnRows(pgxmock.NewRows(directProfileCols()).
			AddRow("user-other", "Ana Silva", "", "ana.silva.2@nic.test"))

	profile, err := storage.NewPGXDMStore(mock).GetDirectCounterpartProfile(
		context.Background(), "ws-1", "conv-1", "user-me",
	)
	if err != nil {
		t.Fatalf("GetDirectCounterpartProfile: %v", err)
	}
	if profile.UserID != "user-other" || profile.Email != "ana.silva.2@nic.test" {
		t.Fatalf("identity must come from the row, got %+v", profile)
	}
	checkExpectations(t, mock)
}
