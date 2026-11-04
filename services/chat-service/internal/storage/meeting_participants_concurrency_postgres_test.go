package storage_test

import (
	"context"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

func TestMeetingSelectionBulkConcurrentPostgreSQL(t *testing.T) {
	for _, cancelFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "bulk first", true: "cancel first"}[cancelFirst], func(t *testing.T) {
			pool, ctx := meetingsPostgres(t)
			store := storage.NewPGXMeetingStore(pool)
			m := mtStore(t, ctx, store, mtChannelConversation, mtDetails(""))
			scope := mtScope(mtOrganizer, m.ID)
			done := make(chan error, 1)
			paused := &mtPausingPool{Pool: pool, steps: []mtPause{{"FOR UPDATE", func(pid int) {
				go func() {
					if cancelFirst {
						_, err := store.InviteAllMeetingParticipants(ctx, scope)
						done <- err
					} else {
						_, err := store.TransitionMeeting(ctx, scope, 1, domain.MeetingStatusCancelled)
						done <- err
					}
				}()
				mtWaitBlocked(t, pool, 1, pid)
			}}}}
			first := storage.NewPGXMeetingStore(paused)
			if cancelFirst {
				_, err := first.TransitionMeeting(ctx, scope, 1, domain.MeetingStatusCancelled)
				mtWant(t, "cancel wins", err, nil)
				mtWant(t, "bulk after cancel", <-done, domain.ErrMeetingInvalidState)
			} else {
				n, err := first.InviteAllMeetingParticipants(ctx, scope)
				if err != nil || n != 2 {
					t.Fatalf("bulk %d %v", n, err)
				}
				mtWant(t, "cancel after bulk", <-done, nil)
			}
			got, roster, err := store.GetMeeting(ctx, scope)
			want := 3
			if cancelFirst {
				want = 1
			}
			if err != nil || got.Status != domain.MeetingStatusCancelled || got.Version != 2 || len(roster) != want {
				t.Fatalf("final %+v %d %v", got, len(roster), err)
			}
		})
	}
}

func TestMeetingSelectionBulkSimultaneousPostgreSQL(t *testing.T) {
	pool, ctx := meetingsPostgres(t)
	store := storage.NewPGXMeetingStore(pool)
	m := mtStore(t, ctx, store, mtChannelConversation, mtDetails(""))
	scope := mtScope(mtOrganizer, m.ID)
	type outcome struct {
		n   int64
		err error
	}
	done := make(chan outcome, 1)
	paused := &mtPausingPool{Pool: pool, steps: []mtPause{{"FOR UPDATE", func(pid int) {
		go func() { n, err := store.InviteAllMeetingParticipants(ctx, scope); done <- outcome{n, err} }()
		mtWaitBlocked(t, pool, 1, pid)
	}}}}
	n, err := storage.NewPGXMeetingStore(paused).InviteAllMeetingParticipants(ctx, scope)
	if err != nil || n != 2 {
		t.Fatalf("first %d %v", n, err)
	}
	second := <-done
	if second.err != nil || second.n != 0 {
		t.Fatalf("second %+v", second)
	}
	before := mtSnapshot(t, ctx, pool, m.ID)
	// A committed transaction with a lost acknowledgement is safely repeatable.
	_, err = storage.NewPGXMeetingStore(maUnknownCommitPool{pool}).InviteAllMeetingParticipants(ctx, scope)
	mtWant(t, "lost acknowledgement", err, context.DeadlineExceeded)
	n, err = store.InviteAllMeetingParticipants(ctx, scope)
	if err != nil || n != 0 || before != mtSnapshot(t, ctx, pool, m.ID) {
		t.Fatal("retry changed roster", err)
	}
}

func TestMeetingSelectionBulkRevocationPostgreSQL(t *testing.T) {
	for _, tt := range []struct{ name, sql string }{
		{"workspace", `UPDATE chat.workspace_members SET status='left' WHERE user_id=$1`},
		{"account", `UPDATE auth.users SET status='suspended' WHERE id=$1`},
		{"channel", `DELETE FROM chat.channel_members WHERE user_id=$1`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			pool, ctx := meetingsPostgres(t)
			store := storage.NewPGXMeetingStore(pool)
			m := mtStore(t, ctx, store, mtChannelConversation, mtDetails(""))
			done := make(chan error, 1)
			paused := &mtPausingPool{Pool: pool, steps: []mtPause{{mtAttendee, func(pid int) {
				// The full authority batch has completed. Revocation must wait.
				go func() { _, err := pool.Exec(ctx, tt.sql, mtAttendee); done <- err }()
				mtWaitBlocked(t, pool, 1, pid)
			}}}}
			n, err := storage.NewPGXMeetingStore(paused).InviteAllMeetingParticipants(ctx, mtScope(mtOrganizer, m.ID))
			if err != nil || n != 2 {
				t.Fatalf("bulk %d %v", n, err)
			}
			mtWant(t, "revocation after bulk", <-done, nil)
		})
	}
}

func TestMeetingSelectionBulkLostConversationPostgreSQL(t *testing.T) {
	pool, ctx := meetingsPostgres(t)
	store := storage.NewPGXMeetingStore(pool)
	m := mtStore(t, ctx, store, mtChannelConversation, mtDetails(""))
	if _, err := pool.Exec(ctx, `DELETE FROM chat.channel_members WHERE channel_id=$1 AND user_id=$2`, mtChannel, mtOrganizer); err != nil {
		t.Fatal(err)
	}
	// Organizer can still see the meeting, but cannot enumerate the conversation.
	if _, _, err := store.GetMeeting(ctx, mtScope(mtOrganizer, m.ID)); err != nil {
		t.Fatal(err)
	}
	_, err := store.InviteAllMeetingParticipants(ctx, mtScope(mtOrganizer, m.ID))
	mtWant(t, "lost conversation bulk", err, domain.ErrForbidden)
	_, err = store.ListMeetingCandidates(ctx, selectionScope(domain.MeetingConversationChannel, mtChannel, mtOrganizer), storage.MeetingPageQuery{})
	mtWant(t, "lost conversation discovery", err, domain.ErrNotFound)
}

func TestMeetingSelectionRoleSerializationPostgreSQL(t *testing.T) {
	for _, tt := range []struct {
		name, first, second string
		secondErr           error
		finalRole           domain.MeetingRole
		terminal            bool
	}{
		{"remove then role", "remove", "cohost", domain.ErrNotFound, "", false},
		{"role then remove", "cohost", "remove", nil, "", false},
		{"two roles", "cohost", "attendee", nil, domain.MeetingRoleAttendee, false},
		{"role then cancel", "cohost", "cancel", nil, domain.MeetingRoleCohost, true},
		{"cancel then role", "cancel", "cohost", domain.ErrMeetingInvalidState, domain.MeetingRoleAttendee, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			pool, ctx := meetingsPostgres(t)
			store := storage.NewPGXMeetingStore(pool)
			m := mtCreate(t, ctx, store, mtChannelConversation, domain.MeetingJoinInvitedOnly)
			scope := mtScope(mtOrganizer, m.ID)
			done := make(chan error, 1)
			paused := &mtPausingPool{Pool: pool, steps: []mtPause{{"FOR UPDATE", func(pid int) {
				go func() { done <- selectionMutation(ctx, store, scope, tt.second) }()
				mtWaitBlocked(t, pool, 1, pid)
			}}}}
			mtWant(t, "first", selectionMutation(ctx, storage.NewPGXMeetingStore(paused), scope, tt.first), nil)
			mtWant(t, "second", <-done, tt.secondErr)
			got, roster, err := store.GetMeeting(ctx, scope)
			if err != nil || got.Status.Terminal() != tt.terminal {
				t.Fatal("invalid final meeting", err)
			}
			role := domain.MeetingRole("")
			for _, p := range roster {
				if p.UserID == mtAttendee {
					role = p.Role
				}
			}
			if role != tt.finalRole {
				t.Fatalf("role %s want %s", role, tt.finalRole)
			}
			wantVersion := int64(1)
			if tt.terminal {
				wantVersion = 2
			}
			if got.Version != wantVersion {
				t.Fatal("unexpected metadata version")
			}
		})
	}
}

func selectionMutation(ctx context.Context, store *storage.PGXMeetingStore, scope storage.MeetingScope, operation string) error {
	switch operation {
	case "remove":
		return store.RemoveMeetingParticipant(ctx, scope, mtAttendee)
	case "cancel":
		_, err := store.TransitionMeeting(ctx, scope, 1, domain.MeetingStatusCancelled)
		return err
	default:
		_, err := store.ChangeMeetingParticipantRole(ctx, scope, mtAttendee, domain.MeetingRole(operation))
		return err
	}
}
