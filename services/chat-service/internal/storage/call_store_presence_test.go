package storage_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	pgxmock "github.com/pashagolub/pgxmock/v2"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// A call change announces to presence, from inside its transaction and before
// it commits, exactly the people whose participation it changes (issue #798,
// HIGH-B/HIGH-C). The real-PostgreSQL proofs of the orderings are in
// call_presence_postgres_test.go.

// recordingFacts is a storage.PresenceFacts that records what was announced.
type recordingFacts struct {
	err    error
	opened [][]string
	closed int
}

func (f *recordingFacts) OpenPresenceFacts(ctx context.Context, _ string, userIDs []string) (context.Context, func(), error) {
	if f.err != nil {
		return nil, nil, f.err
	}
	f.opened = append(f.opened, slices.Clone(userIDs))
	return ctx, func() { f.closed++ }, nil
}

func announcingStore(mock pgxmock.PgxPoolIface, facts *recordingFacts) *storage.PGXCallStore {
	store := storage.NewPGXCallStore(mock)
	store.SetPresenceFacts(facts)
	return store
}

// expectCallLeaseHolders is the participant read a resource call's end makes
// under its row lock.
func expectCallLeaseHolders(mock pgxmock.PgxPoolIface) {
	mock.ExpectQuery(`SELECT user_id::text FROM chat.call_participant_leases WHERE call_id = \$1`).
		WithArgs(callID).
		WillReturnRows(pgxmock.NewRows([]string{"user_id"}).AddRow(callCallerID).AddRow(callOutsiderID))
}

func expectRenewalUpTo(mock pgxmock.PgxPoolIface, now time.Time) {
	mock.ExpectBegin()
	mock.ExpectExec(`pg_advisory_xact_lock`).WithArgs(callCallerID).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(`FROM chat.calls.*FOR SHARE`).WithArgs(callWorkspaceID, callID).
		WillReturnRows(resourceCallRow(now, domain.CallStatusActive, 1))
	mock.ExpectQuery(`chat\.channels.*channel_visible_to_user`).WithArgs(callWorkspaceID, callCallerID, callCalleeID).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(`call_participant_leases`).WithArgs(callWorkspaceID, callCallerID, callID).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))
}

func renew(store *storage.PGXCallStore, now time.Time) error {
	return store.RenewCallPresence(context.Background(), storage.RenewCallPresenceInput{
		WorkspaceID: callWorkspaceID, CallID: callID, ActorID: callCallerID,
		ParticipationID: callParticipationID, ExpiresAt: now.Add(30 * time.Second),
	})
}

// HIGH-B: extending a live lease announces nothing; reviving a lapsed one is
// announced before the commit; a revival that cannot be announced is not made.
func TestPGXCallStoreRenewalAnnouncesOnlyARevival(t *testing.T) {
	now := time.Now().UTC()
	unavailable := errors.New("presence unavailable")
	for _, tc := range []struct {
		name     string
		lapsed   bool
		factsErr error
		wantErr  error
		opened   int
	}{
		{name: "live lease extended", lapsed: false},
		{name: "lapsed lease revived", lapsed: true, opened: 1},
		{name: "revival that cannot be announced", lapsed: true, factsErr: unavailable, wantErr: unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mock := newCategoryMock(t)
			expectRenewalUpTo(mock, now)
			mock.ExpectQuery(`(?s)WITH prior.*RETURNING prior.expires_at <= clock_timestamp\(\)`).
				WithArgs(callID, callCallerID, pgxmock.AnyArg(), now.Add(30*time.Second)).
				WillReturnRows(pgxmock.NewRows([]string{"lapsed"}).AddRow(tc.lapsed))
			if tc.wantErr == nil {
				mock.ExpectCommit()
			} else {
				mock.ExpectRollback()
			}
			facts := &recordingFacts{err: tc.factsErr}
			if err := renew(announcingStore(mock, facts), now); !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if len(facts.opened) != tc.opened || facts.closed != tc.opened {
				t.Fatalf("opened %v, closed %d; want %d", facts.opened, facts.closed, tc.opened)
			}
			if tc.opened == 1 && !slices.Equal(facts.opened[0], []string{callCallerID}) {
				t.Fatalf("announced %v, want the actor", facts.opened[0])
			}
			requireMetExpectations(t, mock)
		})
	}
}

// A renewal of a participation that is not there renews nothing and is told so.
func TestPGXCallStoreRenewalOfAMissingLeaseIsStale(t *testing.T) {
	now := time.Now().UTC()
	mock := newCategoryMock(t)
	expectRenewalUpTo(mock, now)
	mock.ExpectQuery(`WITH prior`).WithArgs(callID, callCallerID, pgxmock.AnyArg(), now.Add(30*time.Second)).
		WillReturnRows(pgxmock.NewRows([]string{"lapsed"}))
	mock.ExpectRollback()
	if err := renew(announcingStore(mock, &recordingFacts{}), now); !errors.Is(err, domain.ErrCallParticipationStale) {
		t.Fatalf("err = %v", err)
	}
	requireMetExpectations(t, mock)

	failing := newCategoryMock(t)
	expectRenewalUpTo(failing, now)
	failing.ExpectQuery(`WITH prior`).WithArgs(callID, callCallerID, pgxmock.AnyArg(), now.Add(30*time.Second)).
		WillReturnError(errors.New("down"))
	failing.ExpectRollback()
	if err := renew(announcingStore(failing, &recordingFacts{}), now); err == nil {
		t.Fatal("a failed renewal was not reported")
	}
	requireMetExpectations(t, failing)
}

// HIGH-C: a resource call's end announces every lease holder it read under the
// call's row lock, before it commits; an end that cannot be announced is
// rolled back.
func TestPGXCallStoreResourceEndAnnouncesTheHoldersReadUnderItsLock(t *testing.T) {
	now := time.Now().UTC()
	mock := newCategoryMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`FOR UPDATE`).WithArgs(callWorkspaceID, callID).WillReturnRows(resourceCallRow(now, domain.CallStatusActive, 1))
	mock.ExpectQuery(`UPDATE chat.calls.*status = \$2`).WithArgs(callID, string(domain.CallStatusEnded)).
		WillReturnRows(resourceCallEndedRow(now))
	expectCallEndedEvent(mock)
	expectCallLeaseHolders(mock)
	mock.ExpectCommit()
	facts := &recordingFacts{}
	input := storage.TransitionCallInput{WorkspaceID: callWorkspaceID, CallID: callID, ActorID: callCallerID, Action: storage.CallActionEnd}
	if _, err := announcingStore(mock, facts).TransitionCall(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if len(facts.opened) != 1 || !slices.Equal(facts.opened[0], []string{callCallerID, callOutsiderID}) || facts.closed != 1 {
		t.Fatalf("announced %v (closed %d)", facts.opened, facts.closed)
	}
	requireMetExpectations(t, mock)

	refused := newCategoryMock(t)
	refused.ExpectBegin()
	refused.ExpectQuery(`FOR UPDATE`).WithArgs(callWorkspaceID, callID).WillReturnRows(resourceCallRow(now, domain.CallStatusActive, 1))
	refused.ExpectQuery(`UPDATE chat.calls.*status = \$2`).WithArgs(callID, string(domain.CallStatusEnded)).
		WillReturnRows(resourceCallEndedRow(now))
	expectCallEndedEvent(refused)
	expectCallLeaseHolders(refused)
	refused.ExpectRollback()
	unavailable := errors.New("presence unavailable")
	if _, err := announcingStore(refused, &recordingFacts{err: unavailable}).TransitionCall(context.Background(), input); !errors.Is(err, unavailable) {
		t.Fatalf("err = %v", err)
	}
	requireMetExpectations(t, refused)

	broken := newCategoryMock(t)
	broken.ExpectBegin()
	broken.ExpectQuery(`FOR UPDATE`).WithArgs(callWorkspaceID, callID).WillReturnRows(resourceCallRow(now, domain.CallStatusActive, 1))
	broken.ExpectQuery(`UPDATE chat.calls.*status = \$2`).WithArgs(callID, string(domain.CallStatusEnded)).
		WillReturnRows(resourceCallEndedRow(now))
	expectCallEndedEvent(broken)
	broken.ExpectQuery(`SELECT user_id::text FROM chat.call_participant_leases`).WithArgs(callID).WillReturnError(errors.New("down"))
	broken.ExpectRollback()
	if _, err := announcingStore(broken, &recordingFacts{}).TransitionCall(context.Background(), input); err == nil {
		t.Fatal("an unreadable participant set was not reported")
	}
	requireMetExpectations(t, broken)
}

// A direct call moves its two parties when it becomes or stops being active,
// and nobody while it only rings.
func TestPGXCallStoreDirectTransitionsAnnounceOnlyActiveChanges(t *testing.T) {
	now := time.Now().UTC()
	for _, tc := range []struct {
		name    string
		current domain.CallStatus
		action  storage.CallAction
		actor   string
		next    domain.CallStatus
		moved   bool
	}{
		{name: "accept", current: domain.CallStatusRinging, action: storage.CallActionAccept, actor: callCalleeID, next: domain.CallStatusActive, moved: true},
		{name: "end", current: domain.CallStatusActive, action: storage.CallActionEnd, actor: callCallerID, next: domain.CallStatusEnded, moved: true},
		{name: "decline", current: domain.CallStatusRinging, action: storage.CallActionDecline, actor: callCalleeID, next: domain.CallStatusDeclined},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mock := newCategoryMock(t)
			mock.ExpectBegin()
			mock.ExpectQuery(`FOR UPDATE`).WithArgs(callWorkspaceID, callID).WillReturnRows(callRow(now, tc.current, 1))
			if tc.current == domain.CallStatusRinging {
				mock.ExpectQuery(`clock_timestamp`).WillReturnRows(pgxmock.NewRows([]string{"now"}).AddRow(now))
			}
			mock.ExpectQuery(`UPDATE chat.calls.*status = \$2`).WithArgs(callID, string(tc.next)).
				WillReturnRows(callRow(now, tc.next, 2))
			mock.ExpectCommit()
			facts := &recordingFacts{}
			if _, err := announcingStore(mock, facts).TransitionCall(context.Background(), storage.TransitionCallInput{
				WorkspaceID: callWorkspaceID, CallID: callID, ActorID: tc.actor, Action: tc.action,
			}); err != nil {
				t.Fatal(err)
			}
			want := 0
			if tc.moved {
				want = 1
				if !slices.Equal(facts.opened[0], []string{callCallerID, callCalleeID}) {
					t.Fatalf("announced %v, want both parties", facts.opened[0])
				}
			}
			if len(facts.opened) != want {
				t.Fatalf("announced %v", facts.opened)
			}
			requireMetExpectations(t, mock)
		})
	}
}
