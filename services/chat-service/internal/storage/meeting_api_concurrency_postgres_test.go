package storage_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

type maCreated struct {
	result service.CreateMeetingResult
	err    error
}

func TestMeetingAPISimultaneousClaimsPostgreSQL(t *testing.T) {
	for _, different := range []bool{false, true} {
		t.Run(fmt.Sprint(different), func(t *testing.T) {
			pool, ctx := meetingsPostgres(t)
			second := maInput()
			if different {
				second.Details.Title = "Losing intent"
			}
			done := make(chan maCreated, 1)
			paused := &mtPausingPool{Pool: pool, steps: []mtPause{{"INSERT INTO chat.meeting_creation_requests", func(pid int) {
				go func() { result, err := maService(pool).CreateMeeting(ctx, second); done <- maCreated{result, err} }()
				mtWaitBlocked(t, pool, 1, pid)
			}}}}
			first := maCreate(t, ctx, paused, maInput())
			other := <-done
			maClaimOutcome(t, first, other, different)
			before := mtSnapshot(t, ctx, pool, first.Meeting.ID)
			again := maCreate(t, ctx, pool, maInput())
			if !again.Replayed || mtSnapshot(t, ctx, pool, first.Meeting.ID) != before {
				t.Fatal("replay changed persisted state")
			}
			maCounts(t, ctx, pool, 1, 3, 1)
		})
	}
}

func maClaimOutcome(t *testing.T, first service.CreateMeetingResult, other maCreated, different bool) {
	t.Helper()
	if first.Replayed {
		t.Fatal("claim owner reported replay")
	}
	if different {
		mtWant(t, "losing claim", other.err, domain.ErrIdempotencyKeyReused)
		return
	}
	if other.err != nil || !other.result.Replayed || !reflect.DeepEqual(other.result.Meeting, first.Meeting) {
		t.Fatalf("concurrent replay = %+v %v", other.result, other.err)
	}

}

func TestMeetingAPIClaimWaitsForRollbackPostgreSQL(t *testing.T) {
	pool, ctx := meetingsPostgres(t)
	first := maInput()
	first.Participants = []storage.MeetingInvitee{{UserID: mtNobody}}
	second := maInput()
	done := make(chan maCreated, 1)
	paused := &mtPausingPool{Pool: pool, steps: []mtPause{{"INSERT INTO chat.meeting_creation_requests", func(pid int) {
		go func() { result, err := maService(pool).CreateMeeting(ctx, second); done <- maCreated{result, err} }()
		mtWaitBlocked(t, pool, 1, pid)
	}}}}
	_, err := maService(paused).CreateMeeting(ctx, first)
	mtWant(t, "first creation rolls back", err, domain.ErrMeetingParticipantNotEligible)
	other := <-done
	if other.err != nil || other.result.Replayed {
		t.Fatalf("next claim after rollback = %+v %v", other.result, other.err)
	}
	maCounts(t, ctx, pool, 1, 3, 1)
}

type maMutation func(context.Context, *service.MeetingService, domain.Meeting) (domain.Meeting, error)

func maUpdate(ctx context.Context, svc *service.MeetingService, m domain.Meeting) (domain.Meeting, error) {
	title, description := "Winning edit", "Edited agenda"
	return svc.UpdateMeeting(ctx, service.UpdateMeetingInput{Scope: mtScope(mtOrganizer, m.ID), ExpectedVersion: 1, Patch: service.MeetingPatch{Title: &title, Description: &description}})
}
func maOtherUpdate(ctx context.Context, svc *service.MeetingService, m domain.Meeting) (domain.Meeting, error) {
	title, description := "Losing edit", "Different agenda"
	return svc.UpdateMeeting(ctx, service.UpdateMeetingInput{Scope: mtScope(mtOrganizer, m.ID), ExpectedVersion: 1, Patch: service.MeetingPatch{Title: &title, Description: &description}})
}

func maCancel(ctx context.Context, svc *service.MeetingService, m domain.Meeting) (domain.Meeting, error) {
	return svc.CancelMeeting(ctx, mtScope(mtOrganizer, m.ID), 1)
}

type maMutationResult struct {
	m   domain.Meeting
	err error
}

func TestMeetingAPIMutationsSerializePostgreSQL(t *testing.T) {
	cases := []struct {
		name          string
		first, second maMutation
		status        domain.MeetingStatus
		want          error
	}{
		{"two updates", maUpdate, maOtherUpdate, domain.MeetingStatusScheduled, domain.ErrMeetingStaleVersion},
		{"update then cancel", maUpdate, maCancel, domain.MeetingStatusScheduled, domain.ErrMeetingStaleVersion},
		{"cancel then update", maCancel, maUpdate, domain.MeetingStatusCancelled, domain.ErrMeetingStaleVersion},
		{"two cancels", maCancel, maCancel, domain.MeetingStatusCancelled, nil},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			pool, ctx := meetingsPostgres(t)
			m := maCreate(t, ctx, pool, maInput()).Meeting
			done := make(chan maMutationResult, 1)
			// Pause once the winning write owns the Meeting FOR UPDATE.
			// The competitor must wait before reading or merging its patch.
			paused := &mtPausingPool{Pool: pool, steps: []mtPause{{"FOR UPDATE", func(pid int) {
				go func() { next, err := tt.second(ctx, maService(pool), m); done <- maMutationResult{next, err} }()
				mtWaitBlocked(t, pool, 1, pid)
			}}}}
			winner, err := tt.first(ctx, maService(paused), m)
			if err != nil {
				t.Fatal(err)
			}
			loser := <-done
			mtWant(t, "second mutation", loser.err, tt.want)
			maAssertMutationState(t, ctx, pool, winner, tt.status)
			if tt.want == nil && (loser.m.Version != winner.Version || loser.m.UpdatedAt != winner.UpdatedAt) {
				t.Fatal("concurrent cancel replay wrote another revision")
			}
		})
	}
}

func maAssertMutationState(t *testing.T, ctx context.Context, pool *pgxpool.Pool, winner domain.Meeting, status domain.MeetingStatus) {
	t.Helper()
	read, roster, err := storage.NewPGXMeetingStore(pool).GetMeeting(ctx, mtScope(mtOrganizer, winner.ID))
	if err != nil {
		t.Fatal(err)
	}
	if read.MeetingDetails != winner.MeetingDetails || read.Status != status || read.Version != 2 || read.UpdatedAt != winner.UpdatedAt || len(roster) != 3 {
		t.Fatalf("persisted mutation/roster = %+v/%+v", read, roster)
	}
	maCounts(t, ctx, pool, 1, 3, 1)
}

func TestMeetingAPICreateRevocationFirstPostgreSQL(t *testing.T) {
	cases := []struct {
		name, sql string
		args      []any
		want      error
	}{
		{"actor membership", `UPDATE chat.workspace_members SET status='left' WHERE workspace_id=$1 AND user_id=$2`, []any{mtWS, mtOrganizer}, domain.ErrForbidden},
		{"invitee membership", `UPDATE chat.workspace_members SET status='left' WHERE workspace_id=$1 AND user_id=$2`, []any{mtWS, mtAttendee}, domain.ErrMeetingParticipantNotEligible},
		{"actor demotion", `UPDATE chat.workspace_members SET role='guest' WHERE workspace_id=$1 AND user_id=$2`, []any{mtWS, mtOrganizer}, domain.ErrForbidden},
		{"invitee channel access", `DELETE FROM chat.channel_members WHERE channel_id=$1 AND user_id=$2`, []any{mtChannel, mtAttendee}, domain.ErrMeetingParticipantNotEligible},
		{"actor channel access", `DELETE FROM chat.channel_members WHERE channel_id=$1 AND user_id=$2`, []any{mtChannel, mtOrganizer}, domain.ErrNotFound},
		{"invitee DM access", `UPDATE chat.dm_members SET status='left', left_at=now() WHERE conversation_id=$1 AND user_id=$2`, []any{mtGroup, mtAttendee}, domain.ErrMeetingParticipantNotEligible},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			pool, ctx := meetingsPostgres(t)
			input := maInput()
			if tt.name == "invitee DM access" {
				input.Conversation = &domain.MeetingConversation{Kind: domain.MeetingConversationDM, ID: mtGroup}
				input.Participants = []storage.MeetingInvitee{{UserID: mtAttendee}}
			}
			err := mtRevokeDuring(t, ctx, pool, tt.sql, tt.args, func() error { _, err := maService(pool).CreateMeeting(ctx, input); return err })
			mtWant(t, tt.name, err, tt.want)
			maCounts(t, ctx, pool, 0, 0, 0)
		})
	}
}

func TestMeetingAPICreateBeforeRevocationPostgreSQL(t *testing.T) {
	cases := []struct {
		name, sql string
		args      []any
	}{
		{"workspace membership", `UPDATE chat.workspace_members SET status='left' WHERE workspace_id=$1 AND user_id=$2`, []any{mtWS, mtAttendee}},
		{"channel access", `DELETE FROM chat.channel_members WHERE channel_id=$1 AND user_id=$2`, []any{mtChannel, mtAttendee}},
		{"actor role", `UPDATE chat.workspace_members SET role='guest' WHERE workspace_id=$1 AND user_id=$2`, []any{mtWS, mtOrganizer}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			pool, ctx := meetingsPostgres(t)
			done := make(chan error, 1)
			paused := &mtPausingPool{Pool: pool, steps: []mtPause{{"chat.workspace_members", func(pid int) {
				go func() { _, err := pool.Exec(ctx, tt.sql, tt.args...); done <- err }()
				mtWaitBlocked(t, pool, 1, pid)
			}}}}
			result := maCreate(t, ctx, paused, maInput())
			mtWant(t, "revocation serialized after creation", <-done, nil)
			maCounts(t, ctx, pool, 1, 3, 1)
			var role string
			if err := pool.QueryRow(ctx, `SELECT role FROM chat.meeting_participants WHERE meeting_id=$1 AND user_id=$2`, result.Meeting.ID, mtAttendee).Scan(&role); err != nil || role != "attendee" {
				t.Fatalf("historical invitee = %q %v", role, err)
			}
		})
	}
}

func TestMeetingAPICreateVersusSuspensionPostgreSQL(t *testing.T) {
	for _, user := range []string{mtOrganizer, mtAttendee} {
		t.Run(user, func(t *testing.T) {
			pool, ctx := meetingsPostgres(t)
			suspension, err := mtBeginSuspension(ctx, pool, user)
			if suspension != nil {
				t.Cleanup(func() { _ = suspension.tx.Rollback(context.Background()) })
			}
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { _, err := maService(pool).CreateMeeting(ctx, maInput()); done <- err }()
			mtWaitBlocked(t, pool, 1, suspension.pid)
			mtWant(t, "suspension commits before invite", suspension.finish(ctx), nil)
			want := domain.ErrMeetingParticipantNotEligible
			if user == mtOrganizer {
				want = domain.ErrForbidden
			}
			mtWant(t, "create sees suspension", <-done, want)
			maCounts(t, ctx, pool, 0, 0, 0)
		})
	}
}

func TestMeetingAPICreateBeforeSuspensionPostgreSQL(t *testing.T) {
	for _, conversation := range []*domain.MeetingConversation{mtChannelConversation, {Kind: domain.MeetingConversationDM, ID: mtGroup}} {
		t.Run(string(conversation.Kind)+" creation first", func(t *testing.T) {
			pool, ctx := meetingsPostgres(t)
			input := maInput()
			input.Conversation = conversation
			input.Participants = []storage.MeetingInvitee{{UserID: mtAttendee}}
			done := make(chan error, 1)
			paused := &mtPausingPool{Pool: pool, steps: []mtPause{{"chat.workspace_members", func(pid int) {
				go func() { done <- mtSuspend(ctx, pool, mtAttendee) }()
				mtWaitBlocked(t, pool, 1, pid)
			}}}}
			maCreate(t, ctx, paused, input)
			mtWant(t, "suspension after invite", <-done, nil)
			if mtAccountStatus(t, ctx, pool, mtAttendee) != "suspended" {
				t.Fatal("suspension failed")
			}
			maCounts(t, ctx, pool, 1, 2, 1)
		})
	}
}

func TestMeetingAPICohostAndCancelAuthorizationPostgreSQL(t *testing.T) {
	pool, ctx := meetingsPostgres(t)
	svc := maService(pool)
	m := maCreate(t, ctx, pool, maInput()).Meeting
	scope := mtScope(mtCohost, m.ID)
	title := "Cohost edit"
	policy := domain.MeetingJoinWorkspaceLink
	_, err := svc.UpdateMeeting(ctx, service.UpdateMeetingInput{Scope: scope, ExpectedVersion: 1, Patch: service.MeetingPatch{JoinPolicy: &policy}})
	mtWant(t, "cohost cannot widen policy", err, domain.ErrForbidden)
	updated, err := svc.UpdateMeeting(ctx, service.UpdateMeetingInput{Scope: scope, ExpectedVersion: 1, Patch: service.MeetingPatch{Title: &title}})
	if err != nil || updated.Version != 2 || updated.JoinPolicy != m.JoinPolicy {
		t.Fatalf("cohost edit = %+v %v", updated, err)
	}
	_, err = svc.CancelMeeting(ctx, scope, 2)
	mtWant(t, "cohost cannot cancel", err, domain.ErrForbidden)
	_, err = svc.CancelMeeting(ctx, mtScope(mtMember, m.ID), 2)
	mtWant(t, "invisible actor cannot cancel", err, domain.ErrNotFound)
	cancelled, err := svc.CancelMeeting(ctx, mtScope(mtOrganizer, m.ID), 2)
	if err != nil {
		t.Fatal(err)
	}
	maAssertCancelHistory(t, m, cancelled)
	before := mtSnapshot(t, ctx, pool, m.ID)
	if _, err = pool.Exec(ctx, `UPDATE chat.workspace_members SET status='left' WHERE workspace_id=$1 AND user_id=$2`, mtWS, mtOrganizer); err != nil {
		t.Fatal(err)
	}
	_, err = svc.CancelMeeting(ctx, mtScope(mtOrganizer, m.ID), 1)
	mtWant(t, "cancel replay after revocation", err, domain.ErrNotFound)
	if mtSnapshot(t, ctx, pool, m.ID) != before {
		t.Fatal("denied cancel changed history")
	}
	maCounts(t, ctx, pool, 1, 3, 1)
}

func maAssertCancelHistory(t *testing.T, before, after domain.Meeting) {
	t.Helper()
	if after.Status != domain.MeetingStatusCancelled || after.ID != before.ID || after.RoomKey != before.RoomKey || after.OrganizerUserID != before.OrganizerUserID || after.CreatedAt != before.CreatedAt {
		t.Fatal("cancel changed historical identity")
	}
	if after.StartsAt != before.StartsAt || after.EndsAt != before.EndsAt || after.Timezone != before.Timezone || after.Description != before.Description {
		t.Fatal("cancel changed historical details")
	}
}

func TestMeetingAPIRoleRevocationBeforeUpdatePostgreSQL(t *testing.T) {
	pool, ctx := meetingsPostgres(t)
	m := maCreate(t, ctx, pool, maInput()).Meeting
	done := make(chan error, 1)
	paused := &mtPausingPool{Pool: pool, steps: []mtPause{{"UPDATE chat.meeting_participants", func(pid int) {
		go func() {
			title := "Revoked cohost edit"
			_, err := maService(pool).UpdateMeeting(ctx, service.UpdateMeetingInput{Scope: mtScope(mtCohost, m.ID), ExpectedVersion: 1, Patch: service.MeetingPatch{Title: &title}})
			done <- err
		}()
		mtWaitBlocked(t, pool, 1, pid)
	}}}}
	_, err := storage.NewPGXMeetingStore(paused).ChangeMeetingParticipantRole(ctx, mtScope(mtOrganizer, m.ID), mtCohost, domain.MeetingRoleAttendee)
	mtWant(t, "cohost role revoked", err, nil)
	mtWant(t, "edit reauthorizes revoked role", <-done, domain.ErrForbidden)
	read, roster, err := storage.NewPGXMeetingStore(pool).GetMeeting(ctx, mtScope(mtOrganizer, m.ID))
	if err != nil || read.Version != 1 || read.Title != m.Title || len(roster) != 3 {
		t.Fatalf("revoked edit persisted = %+v, %v", read, err)
	}
}

func TestMeetingAPIReplayDuringRevocationPostgreSQL(t *testing.T) {
	pool, ctx := meetingsPostgres(t)
	m := maCreate(t, ctx, pool, maInput()).Meeting
	before := mtSnapshot(t, ctx, pool, m.ID)
	err := mtRevokeDuring(t, ctx, pool, `UPDATE chat.workspace_members SET status='left' WHERE workspace_id=$1 AND user_id=$2`, []any{mtWS, mtOrganizer}, func() error { _, err := maService(pool).CreateMeeting(ctx, maInput()); return err })
	mtWant(t, "replay sees committed revocation", err, domain.ErrNotFound)
	if mtSnapshot(t, ctx, pool, m.ID) != before {
		t.Fatal("replay during revocation changed history")
	}
}

func TestMeetingAPIWriteErrorsLeaveNoSuccessPostgreSQL(t *testing.T) {
	pool, ctx := meetingsPostgres(t)
	expired, cancel := context.WithDeadline(ctx, time.Now().Add(-time.Second))
	defer cancel()
	_, err := maService(pool).CreateMeeting(expired, maInput())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancelled create = %v", err)
	}
	maCounts(t, ctx, pool, 0, 0, 0)
}
