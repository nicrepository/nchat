package storage_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

func maInput() service.CreateMeetingInput {
	return service.CreateMeetingInput{WorkspaceID: mtWS, ActorID: mtOrganizer, Details: mtDetails(""),
		Conversation: mtChannelConversation, IdempotencyKey: "meeting-intent",
		Participants: []storage.MeetingInvitee{{UserID: mtCohost, Role: domain.MeetingRoleCohost}, {UserID: mtAttendee, Role: domain.MeetingRoleAttendee}}}
}

func maService(pool storage.Pool) *service.MeetingService {
	return service.NewMeetingService(storage.NewPGXMeetingStore(pool))
}

func maCounts(t *testing.T, ctx context.Context, pool *pgxpool.Pool, wantMeetings, wantParticipants, wantClaims int) {
	t.Helper()
	meetings, participants := mtCount(t, ctx, pool)
	var claims int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM chat.meeting_creation_requests WHERE workspace_id=ANY($1::uuid[])`, []string{mtWS, mtOtherWS}).Scan(&claims); err != nil {
		t.Fatal(err)
	}
	if meetings != wantMeetings || participants != wantParticipants || claims != wantClaims {
		t.Fatalf("meetings/roster/claims = %d/%d/%d, want %d/%d/%d", meetings, participants, claims, wantMeetings, wantParticipants, wantClaims)
	}
}

func maCreate(t *testing.T, ctx context.Context, pool storage.Pool, input service.CreateMeetingInput) service.CreateMeetingResult {
	t.Helper()
	result, err := maService(pool).CreateMeeting(ctx, input)
	if err != nil {
		t.Fatalf("create meeting: %v", err)
	}
	return result
}

func TestMeetingAPICreateRosterAndReplayPostgreSQL(t *testing.T) {
	for _, conversation := range []*domain.MeetingConversation{nil, mtChannelConversation, {Kind: domain.MeetingConversationDM, ID: mtGroup}} {
		t.Run(fmt.Sprint(conversation), func(t *testing.T) {
			maRoundTrip(t, conversation)
		})
	}
}

func maRoundTrip(t *testing.T, conversation *domain.MeetingConversation) {
	pool, ctx := meetingsPostgres(t)
	input := maInput()
	input.Conversation = conversation
	input.Participants = []storage.MeetingInvitee{{UserID: mtAttendee}}
	original := maCreate(t, ctx, pool, input)
	store := storage.NewPGXMeetingStore(pool)
	read, roster, err := store.GetMeeting(ctx, mtScope(mtOrganizer, original.Meeting.ID))
	if err != nil || read.ID != original.Meeting.ID || len(roster) != 2 {
		t.Fatalf("read/roster = %+v/%+v, %v", read, roster, err)
	}
	maAssertRoster(t, roster)
	before := mtSnapshot(t, ctx, pool, read.ID)
	// Fresh store/service instance: no in-process state may be needed for replay.
	replay := maCreate(t, ctx, pool, input)
	if !replay.Replayed || replay.Meeting.ID != read.ID || replay.Meeting.Version != 1 || replay.Meeting.UpdatedAt != read.UpdatedAt {
		t.Fatalf("replay = %+v", replay)
	}
	if mtSnapshot(t, ctx, pool, read.ID) != before {
		t.Fatal("replay changed meeting or roster")
	}
	maCounts(t, ctx, pool, 1, 2, 1)
}

func maAssertRoster(t *testing.T, roster []domain.MeetingParticipant) {
	t.Helper()
	roles := map[string]domain.MeetingParticipant{}
	for _, p := range roster {
		roles[p.UserID] = p
	}
	if roles[mtOrganizer].Role != domain.MeetingRoleOrganizer || roles[mtOrganizer].RSVP != domain.MeetingRSVPAccepted {
		t.Fatal("organizer is not accepted exactly once")
	}
	if roles[mtAttendee].Role != domain.MeetingRoleAttendee || roles[mtAttendee].RSVP != domain.MeetingRSVPPending {
		t.Fatal("invitee is not a pending attendee")
	}
}

func TestMeetingAPICreateRefusalsAreAtomicPostgreSQL(t *testing.T) {
	cases := []struct {
		name, user   string
		conversation *domain.MeetingConversation
		want         error
	}{
		{"unknown", mtNobody, mtChannelConversation, domain.ErrMeetingParticipantNotEligible},
		{"guest excluded (#1054)", mtGuest, mtChannelConversation, domain.ErrMeetingParticipantNotEligible},
		{"foreign", mtForeign, nil, domain.ErrMeetingParticipantNotEligible},
		{"suspended", mtSuspended, nil, domain.ErrMeetingParticipantNotEligible},
		{"no channel access", mtMember, mtChannelConversation, domain.ErrMeetingParticipantNotEligible},
		{"no DM access", mtCohost, &domain.MeetingConversation{Kind: domain.MeetingConversationDM, ID: mtGroup}, domain.ErrMeetingParticipantNotEligible},
		{"cross workspace conversation", mtAttendee, &domain.MeetingConversation{Kind: domain.MeetingConversationChannel, ID: mtForeignCh}, domain.ErrNotFound},
		{"unknown conversation", mtAttendee, &domain.MeetingConversation{Kind: domain.MeetingConversationDM, ID: mtNobody}, domain.ErrNotFound},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			pool, ctx := meetingsPostgres(t)
			input := maInput()
			input.Conversation = tt.conversation
			// An eligible invitee before the invalid one must not leave a partial roster.
			input.Participants = []storage.MeetingInvitee{{UserID: mtOrganizer, Role: domain.MeetingRoleAttendee}, {UserID: tt.user}}
			input.Participants[0].UserID = mtAttendee
			if tt.user == mtAttendee {
				input.Participants = input.Participants[1:]
			}
			_, err := maService(pool).CreateMeeting(ctx, input)
			mtWant(t, tt.name, err, tt.want)
			maCounts(t, ctx, pool, 0, 0, 0)
		})
	}
}

func TestMeetingAPICreationNamespacePostgreSQL(t *testing.T) {
	pool, ctx := meetingsPostgres(t)
	input := maInput()
	input.Conversation = nil
	input.Participants = nil
	a := maCreate(t, ctx, pool, input)
	input.ActorID = mtCohost
	b := maCreate(t, ctx, pool, input)
	input.ActorID = mtForeign
	input.WorkspaceID = mtOtherWS
	c := maCreate(t, ctx, pool, input)
	if a.Meeting.ID == b.Meeting.ID || b.Meeting.ID == c.Meeting.ID || a.Meeting.ID == c.Meeting.ID {
		t.Fatal("same key crossed actor/workspace namespace")
	}
	maCounts(t, ctx, pool, 3, 3, 3)
}

func TestMeetingAPIClaimRollbackPostgreSQL(t *testing.T) {
	for _, phase := range []string{"insert", "commit"} {
		t.Run(phase, func(t *testing.T) {
			maClaimRollback(t, phase)
		})
	}
}

func maClaimRollback(t *testing.T, phase string) {
	pool, ctx := meetingsPostgres(t)
	_, err := pool.Exec(ctx, `CREATE FUNCTION chat.meeting_1053_fail() RETURNS trigger LANGUAGE plpgsql AS $$
   BEGIN IF NEW.user_id = '`+mtAttendee+`'::uuid THEN RAISE EXCEPTION 'injected failure'; END IF; RETURN NEW; END $$`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(), `DROP TRIGGER IF EXISTS meeting_1053_fail ON chat.meeting_participants; DROP FUNCTION chat.meeting_1053_fail()`)
		if err != nil {
			t.Error(err)
		}
	})
	trigger := `CREATE TRIGGER meeting_1053_fail BEFORE INSERT ON chat.meeting_participants FOR EACH ROW EXECUTE FUNCTION chat.meeting_1053_fail()`
	if phase == "commit" {
		trigger = `CREATE CONSTRAINT TRIGGER meeting_1053_fail AFTER INSERT ON chat.meeting_participants DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION chat.meeting_1053_fail()`
	}
	if _, err = pool.Exec(ctx, trigger); err != nil {
		t.Fatal(err)
	}
	_, err = maService(pool).CreateMeeting(ctx, maInput())
	mtWantSQLState(t, "injected roster failure", err, "P0001")
	maCounts(t, ctx, pool, 0, 0, 0)
	if _, err = pool.Exec(ctx, `DROP TRIGGER meeting_1053_fail ON chat.meeting_participants`); err != nil {
		t.Fatal(err)
	}
	maCreate(t, ctx, pool, maInput())
	maCounts(t, ctx, pool, 1, 3, 1)
}

func TestMeetingAPIReplayAfterUnknownCommitPostgreSQL(t *testing.T) {
	pool, ctx := meetingsPostgres(t)
	_, err := maService(maUnknownCommitPool{Pool: pool}).CreateMeeting(ctx, maInput())
	mtWant(t, "response lost after commit", err, context.DeadlineExceeded)
	maCounts(t, ctx, pool, 1, 3, 1)
	replay := maCreate(t, ctx, pool, maInput())
	if !replay.Replayed || replay.Meeting.Version != 1 {
		t.Fatalf("unknown commit recovery = %+v", replay)
	}
	before := mtSnapshot(t, ctx, pool, replay.Meeting.ID)
	conflict := maInput()
	conflict.Details.Title = "Different intent"
	_, err = maService(pool).CreateMeeting(ctx, conflict)
	mtWant(t, "same key different intention", err, domain.ErrIdempotencyKeyReused)
	if mtSnapshot(t, ctx, pool, replay.Meeting.ID) != before {
		t.Fatal("conflicting create wrote state")
	}
}

type maUnknownCommitPool struct{ storage.Pool }

func (p maUnknownCommitPool) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := p.Pool.Begin(ctx)
	return maUnknownCommitTx{tx}, err
}

type maUnknownCommitTx struct{ pgx.Tx }

func (tx maUnknownCommitTx) Commit(ctx context.Context) error {
	if err := tx.Tx.Commit(ctx); err != nil {
		return err
	}
	return context.DeadlineExceeded
}

func TestMeetingAPIReplayReauthorizesPostgreSQL(t *testing.T) {
	pool, ctx := meetingsPostgres(t)
	input := maInput()
	m := maCreate(t, ctx, pool, input).Meeting
	details := m.MeetingDetails
	details.Title = "Changed since creation"
	changed, err := storage.NewPGXMeetingStore(pool).UpdateMeeting(ctx, mtScope(mtOrganizer, m.ID), 1, details)
	if err != nil {
		t.Fatal(err)
	}
	replay := maCreate(t, ctx, pool, input)
	if replay.Meeting.Title != changed.Title || replay.Meeting.Version != 2 {
		t.Fatalf("replay did not return current committed resource: %+v", replay)
	}
	before := mtSnapshot(t, ctx, pool, m.ID)
	if _, err = pool.Exec(ctx, `UPDATE chat.workspace_members SET status='left' WHERE workspace_id=$1 AND user_id=$2`, mtWS, mtOrganizer); err != nil {
		t.Fatal(err)
	}
	_, err = maService(pool).CreateMeeting(ctx, input)
	mtWant(t, "replay after lost membership", err, domain.ErrNotFound)
	if mtSnapshot(t, ctx, pool, m.ID) != before {
		t.Fatal("denied replay wrote state")
	}
	maCounts(t, ctx, pool, 1, 3, 1)
}

func TestMeetingAPIClaimSchemaPostgreSQL(t *testing.T) {
	pool, ctx := meetingsPostgres(t)
	_, err := pool.Exec(ctx, `INSERT INTO chat.meeting_creation_requests (workspace_id,actor_user_id,idempotency_key,request_hash,meeting_id) VALUES ($1,$2,'orphan',$3,$4)`, mtWS, mtOrganizer, strings.Repeat("a", 64), mtNobody)
	mtWantSQLState(t, "orphan claim", err, "23503")
	for _, key := range []string{"", strings.Repeat("x", 129), "has space", "key\n"} {
		_, err = pool.Exec(ctx, `INSERT INTO chat.meeting_creation_requests (workspace_id,actor_user_id,idempotency_key,request_hash,meeting_id) VALUES ($1,$2,$3,$4,$5)`, mtWS, mtOrganizer, key, strings.Repeat("a", 64), mtNobody)
		mtWantSQLState(t, "invalid key", err, "23514")
	}
	maCounts(t, ctx, pool, 0, 0, 0)
}

func TestMeetingAPIClaimMigrationRoundTripPostgreSQL(t *testing.T) {
	pool, ctx := meetingsPostgres(t)
	sentinel := maCreate(t, ctx, pool, maInput()).Meeting
	before := mtSnapshot(t, ctx, pool, sentinel.ID)
	own := mtDisposableDatabase(t, connectTo)
	mtMigrate(t, own, "000071_meeting_creation_idempotency.down.sql")
	if hasTable(t, own, "chat.meeting_creation_requests") || !hasTable(t, own, "chat.meetings") {
		t.Fatal("claim down removed the wrong tables")
	}
	mtMigrate(t, own, "000071_meeting_creation_idempotency.up.sql")
	if !hasTable(t, own, "chat.meeting_creation_requests") {
		t.Fatal("claim up missing table")
	}
	if mtSnapshot(t, ctx, pool, sentinel.ID) != before {
		t.Fatal("migration reached shared state")
	}
	maCounts(t, ctx, pool, 1, 3, 1)
}

func TestMeetingAPIInvalidUpdatesLeaveHistoryPostgreSQL(t *testing.T) {
	pool, ctx := meetingsPostgres(t)
	m := maCreate(t, ctx, pool, maInput()).Meeting
	before := mtSnapshot(t, ctx, pool, m.ID)
	empty, zone, long := "", "Local", strings.Repeat("x", 5001)
	zero := m.EndsAt
	for _, patch := range []service.MeetingPatch{
		{Title: &empty}, {Timezone: &zone}, {Description: &long}, {StartsAt: &zero},
	} {
		_, err := maService(pool).UpdateMeeting(ctx, service.UpdateMeetingInput{Scope: mtScope(mtOrganizer, m.ID), ExpectedVersion: 1, Patch: patch})
		mtWant(t, "invalid patch", err, domain.ErrInvalidInput)
		if mtSnapshot(t, ctx, pool, m.ID) != before {
			t.Fatal("invalid patch changed history or version")
		}
	}
}

func TestMeetingAPIStorageRejectsInvalidInitialRolesPostgreSQL(t *testing.T) {
	pool, ctx := meetingsPostgres(t)
	m, err := domain.NewMeeting(mtWS, mtOrganizer, nil, mtDetails(""))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []storage.MeetingInvitee{
		{UserID: mtOrganizer, Role: domain.MeetingRoleAttendee},
		{UserID: mtAttendee, Role: domain.MeetingRoleOrganizer},
		{UserID: mtAttendee},
	} {
		_, err := storage.NewPGXMeetingStore(pool).CreateMeetingWithParticipants(ctx, storage.MeetingCreation{
			Meeting: m, Participants: []storage.MeetingInvitee{p}, IdempotencyKey: "storage-guard", RequestHash: strings.Repeat("a", 64),
		})
		mtWant(t, "invalid initial role", err, domain.ErrInvalidInput)
		maCounts(t, ctx, pool, 0, 0, 0)
	}
}

func TestMeetingCreationRequiresAKey(t *testing.T) {
	_, err := storage.NewPGXMeetingStore(nil).CreateMeetingWithParticipants(t.Context(), storage.MeetingCreation{})
	mtWant(t, "missing key must not open a transaction", err, domain.ErrInvalidInput)
}
