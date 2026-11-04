package storage_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicrepository/nchat/libs/go/platform/conversationownership"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// Issue #1052 fixture. Workspace A holds a private channel and a group DM;
// workspace B holds a private channel of its own. Users cover every role a
// meeting decision distinguishes.
const (
	mtWS        = "5e110000-0000-4000-8000-000000000001"
	mtOtherWS   = "5e120000-0000-4000-8000-000000000001"
	mtGeneral   = "5e110000-0000-4000-8000-000000000020"
	mtGeneralB  = "5e120000-0000-4000-8000-000000000020"
	mtChannel   = "5e110000-0000-4000-8000-000000000030"
	mtForeignCh = "5e120000-0000-4000-8000-000000000030"
	mtGroup     = "5e110000-0000-4000-8000-000000000040"

	mtOrganizer = "5e110000-0000-4000-8000-0000000000a1"
	mtCohost    = "5e110000-0000-4000-8000-0000000000a2"
	mtAttendee  = "5e110000-0000-4000-8000-0000000000a3"
	mtMember    = "5e110000-0000-4000-8000-0000000000a4" // workspace member outside the channel
	mtGuest     = "5e110000-0000-4000-8000-0000000000a5" // guest inside the channel
	mtSuspended = "5e110000-0000-4000-8000-0000000000a6" // member whose account is suspended
	mtForeign   = "5e120000-0000-4000-8000-0000000000b1" // workspace B only
	mtNobody    = "5e110000-0000-4000-8000-0000000000ff" // no account at all
)

// meetingsPostgres seeds this suite's rows into a database the real migrations
// prepared (the coverage job's shared one, like the RF-21 suites) and removes
// exactly those rows afterwards, so suites sharing the database are untouched.
func meetingsPostgres(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	pool := newLinkScanTestPool(t)
	ctx := t.Context()
	users := []string{mtOrganizer, mtCohost, mtAttendee, mtMember, mtGuest, mtSuspended, mtForeign}
	cleanup := func() {
		cleanupCtx := context.Background()
		// Deleting the workspaces cascades to their channels, conversations,
		// memberships and meetings.
		if _, err := pool.Exec(cleanupCtx, `DELETE FROM chat.workspaces WHERE id = ANY($1::uuid[])`, []string{mtWS, mtOtherWS}); err != nil {
			t.Errorf("cleanup meeting workspaces: %v", err)
		}
		if _, err := pool.Exec(cleanupCtx, `DELETE FROM auth.users WHERE id = ANY($1::uuid[])`, users); err != nil {
			t.Errorf("cleanup meeting users: %v", err)
		}
	}
	cleanup() // a previous run that was killed may have left its rows behind
	t.Cleanup(cleanup)
	seeds := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO auth.users (id, email, display_name)
			SELECT id, 'meeting-1052-' || id || '@e.test', 'Meeting 1052' FROM unnest($1::uuid[]) AS u(id)`, []any{users}},
		{`UPDATE auth.users SET status = 'suspended' WHERE id = $1`, []any{mtSuspended}},
		{`INSERT INTO chat.workspaces (id, slug, name) VALUES ($1, 'meetings-a', 'A'), ($2, 'meetings-b', 'B')`,
			[]any{mtWS, mtOtherWS}},
		{`INSERT INTO chat.channels (id, workspace_id, slug, display_name, type, is_general) VALUES
			($1, $5, 'geral', 'Geral', 'public', true), ($2, $6, 'geral', 'Geral', 'public', true),
			($3, $5, 'privado', 'Privado', 'private', false), ($4, $6, 'outro', 'Outro', 'private', false)`,
			[]any{mtGeneral, mtGeneralB, mtChannel, mtForeignCh, mtWS, mtOtherWS}},
		{`INSERT INTO chat.workspace_members (workspace_id, user_id, role) VALUES
			($1, $3, 'member'), ($1, $4, 'member'), ($1, $5, 'member'), ($1, $6, 'member'),
			($1, $7, 'guest'), ($1, $8, 'member'), ($2, $9, 'member')`,
			[]any{mtWS, mtOtherWS, mtOrganizer, mtCohost, mtAttendee, mtMember, mtGuest, mtSuspended, mtForeign}},
		{`INSERT INTO chat.channel_members (channel_id, user_id) VALUES
			($1, $3), ($1, $4), ($1, $5), ($1, $6), ($1, $7), ($2, $8)`,
			[]any{mtChannel, mtForeignCh, mtOrganizer, mtCohost, mtAttendee, mtGuest, mtSuspended, mtForeign}},
		{`INSERT INTO chat.dm_conversations (id, workspace_id, type, created_by) VALUES ($1, $2, 'group', $3)`,
			[]any{mtGroup, mtWS, mtOrganizer}},
		{`INSERT INTO chat.dm_members (conversation_id, user_id) VALUES ($1, $2), ($1, $3)`,
			[]any{mtGroup, mtOrganizer, mtAttendee}},
	}
	// One transaction: a workspace must commit together with its #geral.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin seed: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	for _, seed := range seeds {
		if _, err := tx.Exec(ctx, seed.sql, seed.args...); err != nil {
			t.Fatalf("seed meetings fixture: %v", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit seed: %v", err)
	}
	return pool, ctx
}

func mtScope(actor, meetingID string) storage.MeetingScope {
	return storage.MeetingScope{WorkspaceID: mtWS, ActorID: actor, MeetingID: meetingID}
}

func mtDetails(policy domain.MeetingJoinPolicy) domain.MeetingDetails {
	start := time.Date(2026, 11, 3, 13, 0, 0, 0, time.UTC)
	return domain.MeetingDetails{Title: "Planejamento", StartsAt: start, EndsAt: start.Add(time.Hour),
		Timezone: "America/Sao_Paulo", JoinPolicy: policy}
}

var mtChannelConversation = &domain.MeetingConversation{Kind: domain.MeetingConversationChannel, ID: mtChannel}

// mtStore creates a meeting organized by mtOrganizer with only its organizer
// on the roster.
func mtStore(t *testing.T, ctx context.Context, store *storage.PGXMeetingStore, conversation *domain.MeetingConversation, details domain.MeetingDetails) domain.Meeting {
	t.Helper()
	m, err := domain.NewMeeting(mtWS, mtOrganizer, conversation, details)
	if err != nil {
		t.Fatalf("NewMeeting: %v", err)
	}
	if m, err = store.CreateMeeting(ctx, m); err != nil {
		t.Fatalf("CreateMeeting: %v", err)
	}
	return m
}

// mtCreate creates a meeting organized by mtOrganizer with a cohost and an
// attendee already on its roster.
func mtCreate(t *testing.T, ctx context.Context, store *storage.PGXMeetingStore, conversation *domain.MeetingConversation, policy domain.MeetingJoinPolicy) domain.Meeting {
	t.Helper()
	m := mtStore(t, ctx, store, conversation, mtDetails(policy))
	for user, role := range map[string]domain.MeetingRole{mtCohost: domain.MeetingRoleCohost, mtAttendee: domain.MeetingRoleAttendee} {
		if _, err := store.AddMeetingParticipant(ctx, mtScope(mtOrganizer, m.ID), user, role); err != nil {
			t.Fatalf("add %s: %v", role, err)
		}
	}
	return m
}

// mtSnapshot is the meeting's whole persisted state, for proving that a
// refused operation wrote nothing.
func mtSnapshot(t *testing.T, ctx context.Context, pool *pgxpool.Pool, meetingID string) string {
	t.Helper()
	var snapshot string
	if err := pool.QueryRow(ctx, `SELECT coalesce((SELECT row_to_json(m)::text FROM chat.meetings m WHERE id = $1), '')
		|| coalesce((SELECT json_agg(p ORDER BY user_id)::text FROM chat.meeting_participants p WHERE meeting_id = $1), '')`,
		meetingID).Scan(&snapshot); err != nil {
		t.Fatalf("snapshot meeting: %v", err)
	}
	return snapshot
}

// mtCount counts this suite's meetings and their roster rows.
func mtCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (meetings, participants int) {
	t.Helper()
	if err := pool.QueryRow(ctx, `SELECT count(DISTINCT m.id), count(p.user_id)
		FROM chat.meetings m LEFT JOIN chat.meeting_participants p ON p.meeting_id = m.id
		WHERE m.workspace_id = ANY($1::uuid[])`, []string{mtWS, mtOtherWS}).Scan(&meetings, &participants); err != nil {
		t.Fatalf("count meetings: %v", err)
	}
	return meetings, participants
}

func mtWant(t *testing.T, operation string, err, want error) {
	t.Helper()
	if !errors.Is(err, want) || (want == nil) != (err == nil) {
		t.Fatalf("%s: err = %v, want %v", operation, err, want)
	}
}

func mtWantSQLState(t *testing.T, operation string, err error, code string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != code {
		t.Fatalf("%s: err = %v, want SQLSTATE %s", operation, err, code)
	}
}

func TestMeetingCreateAndReadRoundTripPostgreSQL(t *testing.T) {
	pool, ctx := meetingsPostgres(t)
	store := storage.NewPGXMeetingStore(pool)
	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	details := mtDetails(domain.MeetingJoinConversationMembers)
	// The fall-back civil day, midnight to midnight: 25 real hours, with
	// sub-microsecond noise.
	details.StartsAt = time.Date(2026, 11, 1, 0, 0, 0, 999, newYork)
	details.EndsAt = time.Date(2026, 11, 2, 0, 0, 0, 999, newYork)
	details.Timezone = "America/New_York"
	created := mtStore(t, ctx, store, mtChannelConversation, details)

	// A session in a far-away zone must read back the same instants.
	far := storage.NewPGXMeetingStore(mtPoolInSessionZone(t, ctx, "Pacific/Kiritimati"))
	read, participants, err := far.GetMeeting(ctx, mtScope(mtOrganizer, created.ID))
	if err != nil {
		t.Fatalf("GetMeeting: %v", err)
	}
	if read.Conversation == nil || *read.Conversation != *mtChannelConversation {
		t.Fatalf("conversation = %+v", read.Conversation)
	}
	read.Conversation, created.Conversation = nil, nil
	if read != created || created.CreatedAt.IsZero() || created.CreatedAt != created.UpdatedAt {
		t.Fatalf("read %+v\nwant %+v", read, created)
	}
	got := mtPersisted{read.StartsAt, read.EndsAt.Sub(read.StartsAt), read.Timezone, read.Status, read.Version, read.RoomKey}
	want := mtPersisted{time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC), 25 * time.Hour, "America/New_York",
		domain.MeetingStatusScheduled, 1, "meeting:" + read.ID}
	if got != want {
		t.Fatalf("persisted %+v, want %+v", got, want)
	}
	roster := []domain.MeetingParticipant{{MeetingID: created.ID, UserID: mtOrganizer, Role: domain.MeetingRoleOrganizer,
		RSVP: domain.MeetingRSVPAccepted, InvitedAt: created.CreatedAt, UpdatedAt: created.CreatedAt}}
	if !slices.Equal(participants, roster) {
		t.Fatalf("participants = %+v, want %+v", participants, roster)
	}
}

type mtPersisted struct {
	start    time.Time
	duration time.Duration
	timezone string
	status   domain.MeetingStatus
	version  int64
	roomKey  string
}

// mtPoolInSessionZone connects to the test database with the given session
// TimeZone, which must not change any instant read back.
func mtPoolInSessionZone(t *testing.T, ctx context.Context, zone string) *pgxpool.Pool {
	t.Helper()
	config, err := pgxpool.ParseConfig(os.Getenv("CHAT_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["timezone"] = zone
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestMeetingCreateRefusalsPostgreSQL(t *testing.T) {
	pool, ctx := meetingsPostgres(t)
	store := storage.NewPGXMeetingStore(pool)
	dm := &domain.MeetingConversation{Kind: domain.MeetingConversationDM, ID: mtGroup}
	tests := []struct {
		name         string
		workspace    string
		organizer    string
		conversation *domain.MeetingConversation
		want         error
	}{
		{"guest organizer", mtWS, mtGuest, mtChannelConversation, domain.ErrForbidden},
		{"suspended account", mtWS, mtSuspended, nil, domain.ErrForbidden},
		{"organizer from another workspace", mtWS, mtForeign, nil, domain.ErrForbidden},
		{"unknown user", mtWS, mtNobody, nil, domain.ErrForbidden},
		{"another workspace's channel", mtWS, mtOrganizer, &domain.MeetingConversation{Kind: domain.MeetingConversationChannel, ID: mtForeignCh}, domain.ErrNotFound},
		{"channel as a dm", mtWS, mtOrganizer, &domain.MeetingConversation{Kind: domain.MeetingConversationDM, ID: mtChannel}, domain.ErrNotFound},
		{"private channel the organizer is not in", mtWS, mtMember, mtChannelConversation, domain.ErrNotFound},
		{"group the organizer is not in", mtWS, mtCohost, dm, domain.ErrNotFound},
	}
	for _, tt := range tests {
		m, err := domain.NewMeeting(tt.workspace, tt.organizer, tt.conversation, mtDetails(""))
		if err != nil {
			t.Fatal(err)
		}
		_, err = store.CreateMeeting(ctx, m)
		mtWant(t, tt.name, err, tt.want)
	}
	if meetings, participants := mtCount(t, ctx, pool); meetings+participants != 0 {
		t.Fatalf("refused creates persisted %d meetings and %d participants", meetings, participants)
	}
	m, err := domain.NewMeeting(mtWS, mtAttendee, dm, mtDetails(""))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.CreateMeeting(ctx, m); err != nil {
		t.Fatalf("group member creating in the group: %v", err)
	}
}

// The schema holds the invariants by itself, whatever a writer does.
func TestMeetingSchemaInvariantsPostgreSQL(t *testing.T) {
	pool, ctx := meetingsPostgres(t)
	store := storage.NewPGXMeetingStore(pool)
	m := mtCreate(t, ctx, store, mtChannelConversation, "")
	insertMeeting := `INSERT INTO chat.meetings (id, workspace_id, channel_id, organizer_user_id, title, starts_at, ends_at, timezone, room_key, join_policy)
		VALUES ($1::uuid, $2, $3, $4, 'x', now(), now() + interval '1 hour', 'UTC', 'meeting:' || $1::uuid, $5)`
	insertOrganizer := `INSERT INTO chat.meeting_participants (meeting_id, user_id, role, rsvp) VALUES ($1, $2, 'organizer', 'accepted')`
	inTx := func(statements func(pgx.Tx) error) error {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		if err := statements(tx); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	const newID = "5e110000-0000-4000-8000-00000000e001"

	err := inTx(func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, insertMeeting, newID, mtWS, mtForeignCh, mtOrganizer, "invited_only"); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, insertOrganizer, newID, mtOrganizer)
		return err
	})
	mtWantSQLState(t, "cross-workspace conversation", err, "23503")

	err = inTx(func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, insertMeeting, newID, mtWS, mtChannel, mtOrganizer, "invited_only")
		return err
	})
	mtWantSQLState(t, "meeting without its organizer row", err, "23503")
	if got, _ := mtCount(t, ctx, pool); got != 1 {
		t.Fatalf("a refused commit left %d meetings, want only the fixture's", got)
	}

	before := mtSnapshot(t, ctx, pool, m.ID)
	refusals := []struct {
		name, sql, code string
		args            []any
	}{
		{"delete the organizer row", `DELETE FROM chat.meeting_participants WHERE meeting_id = $1 AND user_id = $2`, "23503", []any{m.ID, mtOrganizer}},
		{"demote the organizer row", `UPDATE chat.meeting_participants SET role = 'attendee', rsvp = 'pending' WHERE meeting_id = $1 AND user_id = $2`, "23503", []any{m.ID, mtOrganizer}},
		{"second organizer", `UPDATE chat.meeting_participants SET role = 'organizer', rsvp = 'accepted' WHERE meeting_id = $1 AND user_id = $2`, "23505", []any{m.ID, mtAttendee}},
		{"duplicate participant", `INSERT INTO chat.meeting_participants (meeting_id, user_id, role) VALUES ($1, $2, 'attendee')`, "23505", []any{m.ID, mtAttendee}},
		{"organizer declining", `UPDATE chat.meeting_participants SET rsvp = 'declined' WHERE meeting_id = $1 AND user_id = $2`, "23514", []any{m.ID, mtOrganizer}},
		{"unknown role", `UPDATE chat.meeting_participants SET role = 'owner' WHERE meeting_id = $1 AND user_id = $2`, "23514", []any{m.ID, mtAttendee}},
		{"unknown rsvp", `UPDATE chat.meeting_participants SET rsvp = 'maybe' WHERE meeting_id = $1 AND user_id = $2`, "23514", []any{m.ID, mtAttendee}},
		{"unknown status", `UPDATE chat.meetings SET status = 'paused' WHERE id = $1`, "23514", []any{m.ID}},
		{"empty interval", `UPDATE chat.meetings SET ends_at = starts_at WHERE id = $1`, "23514", []any{m.ID}},
		{"conversation policy without conversation", `UPDATE chat.meetings SET channel_id = NULL, join_policy = 'conversation_members' WHERE id = $1`, "23514", []any{m.ID}},
		{"organizer moved off the roster", `UPDATE chat.meetings SET organizer_user_id = $2 WHERE id = $1`, "23503", []any{m.ID, mtMember}},
	}
	for _, r := range refusals {
		err := inTx(func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, r.sql, r.args...)
			return err
		})
		mtWantSQLState(t, r.name, err, r.code)
	}
	if after := mtSnapshot(t, ctx, pool, m.ID); after != before {
		t.Fatalf("refused writes changed the meeting:\n%s\n%s", before, after)
	}
	// Any positive interval is valid, however long.
	_, err = pool.Exec(ctx, `UPDATE chat.meetings SET ends_at = starts_at + interval '30 days' WHERE id = $1`, m.ID)
	mtWant(t, "a month-long interval", err, nil)
}

// BOLA/IDOR: a meeting id grants nothing. Every operation from outside the
// meeting's audience is the same ErrNotFound as an id that does not exist,
// and none of them writes anything.
func TestMeetingIsolationPostgreSQL(t *testing.T) {
	pool, ctx := meetingsPostgres(t)
	store := storage.NewPGXMeetingStore(pool)
	m := mtCreate(t, ctx, store, mtChannelConversation, domain.MeetingJoinInvitedOnly)
	before := mtSnapshot(t, ctx, pool, m.ID)
	const missing = "5e110000-0000-4000-8000-00000000dead"
	scopes := map[string]storage.MeetingScope{
		"other workspace's member in its own workspace":  {WorkspaceID: mtOtherWS, ActorID: mtForeign, MeetingID: m.ID},
		"other workspace's member naming this workspace": mtScope(mtForeign, m.ID),
		"uninvited workspace member":                     mtScope(mtMember, m.ID),
		"uninvited channel guest":                        mtScope(mtGuest, m.ID),
		"suspended channel member":                       mtScope(mtSuspended, m.ID),
		"user without an account":                        mtScope(mtNobody, m.ID),
		"organizer naming another workspace":             {WorkspaceID: mtOtherWS, ActorID: mtOrganizer, MeetingID: m.ID},
		"organizer naming a missing meeting":             mtScope(mtOrganizer, missing),
	}
	for name, scope := range scopes {
		_, _, err := store.GetMeeting(ctx, scope)
		mtWant(t, name+" get", err, domain.ErrNotFound)
		_, err = store.AuthorizeMeetingJoin(ctx, scope)
		mtWant(t, name+" join", err, domain.ErrNotFound)
		_, err = store.UpdateMeeting(ctx, scope, 1, mtDetails(domain.MeetingJoinWorkspaceLink))
		mtWant(t, name+" update", err, domain.ErrNotFound)
		_, err = store.TransitionMeeting(ctx, scope, 1, domain.MeetingStatusCancelled)
		mtWant(t, name+" cancel", err, domain.ErrNotFound)
		_, err = store.AddMeetingParticipant(ctx, scope, scope.ActorID, domain.MeetingRoleCohost)
		mtWant(t, name+" self-invite", err, domain.ErrNotFound)
		err = store.RemoveMeetingParticipant(ctx, scope, mtAttendee)
		mtWant(t, name+" remove", err, domain.ErrNotFound)
		_, err = store.SetMeetingRSVP(ctx, scope, domain.MeetingRSVPAccepted)
		mtWant(t, name+" rsvp", err, domain.ErrNotFound)
	}
	_, _, err := store.GetMeeting(ctx, storage.MeetingScope{WorkspaceID: mtWS, ActorID: mtOrganizer, MeetingID: "not-a-uuid"})
	mtWant(t, "malformed id", err, domain.ErrInvalidInput)
	if after := mtSnapshot(t, ctx, pool, m.ID); after != before {
		t.Fatalf("refused operations changed the meeting:\n%s\n%s", before, after)
	}
}

func TestMeetingJoinPoliciesPostgreSQL(t *testing.T) {
	pool, ctx := meetingsPostgres(t)
	store := storage.NewPGXMeetingStore(pool)
	conversation := mtCreate(t, ctx, store, mtChannelConversation, domain.MeetingJoinConversationMembers)
	link := mtCreate(t, ctx, store, nil, domain.MeetingJoinWorkspaceLink)
	tests := []struct {
		name    string
		meeting domain.Meeting
		actor   string
		want    error
	}{
		{"channel guest joins a conversation meeting", conversation, mtGuest, nil},
		{"member outside the channel", conversation, mtMember, domain.ErrNotFound},
		{"member joins with the link", link, mtMember, nil},
		{"guest is not admitted by a link", link, mtGuest, domain.ErrNotFound},
		{"other workspace with the link", link, mtForeign, domain.ErrNotFound},
	}
	for _, tt := range tests {
		_, err := store.AuthorizeMeetingJoin(ctx, mtScope(tt.actor, tt.meeting.ID))
		mtWant(t, tt.name, err, tt.want)
	}
	// Losing channel access takes the meeting's visibility with it.
	if _, err := pool.Exec(ctx, `DELETE FROM chat.channel_members WHERE channel_id = $1 AND user_id = $2`, mtChannel, mtGuest); err != nil {
		t.Fatal(err)
	}
	_, err := store.AuthorizeMeetingJoin(ctx, mtScope(mtGuest, conversation.ID))
	mtWant(t, "guest after leaving the channel", err, domain.ErrNotFound)
	// A viewer admitted by the policy cannot edit, and a closed meeting cannot be joined.
	_, err = store.UpdateMeeting(ctx, mtScope(mtMember, link.ID), 1, mtDetails(domain.MeetingJoinWorkspaceLink))
	mtWant(t, "link viewer edits", err, domain.ErrForbidden)
	if _, err = store.TransitionMeeting(ctx, mtScope(mtOrganizer, link.ID), 1, domain.MeetingStatusCancelled); err != nil {
		t.Fatal(err)
	}
	_, err = store.AuthorizeMeetingJoin(ctx, mtScope(mtMember, link.ID))
	mtWant(t, "join after cancel", err, domain.ErrMeetingInvalidState)
}

func TestMeetingLifecyclePostgreSQL(t *testing.T) {
	pool, ctx := meetingsPostgres(t)
	store := storage.NewPGXMeetingStore(pool)
	m := mtCreate(t, ctx, store, nil, "")
	refused := func(name, actor string, version int64, next domain.MeetingStatus, want error) {
		t.Helper()
		before := mtSnapshot(t, ctx, pool, m.ID)
		_, err := store.TransitionMeeting(ctx, mtScope(actor, m.ID), version, next)
		mtWant(t, name, err, want)
		if after := mtSnapshot(t, ctx, pool, m.ID); after != before {
			t.Fatalf("%s changed the meeting", name)
		}
	}
	refused("attendee starts", mtAttendee, 1, domain.MeetingStatusInProgress, domain.ErrForbidden)
	refused("cohost cancels", mtCohost, 1, domain.MeetingStatusCancelled, domain.ErrForbidden)
	refused("ending a scheduled meeting", mtOrganizer, 1, domain.MeetingStatusEnded, domain.ErrMeetingInvalidState)
	refused("unknown status", mtOrganizer, 1, "archived", domain.ErrInvalidInput)

	if started := mtMove(t, ctx, store, mtScope(mtCohost, m.ID), 1, domain.MeetingStatusInProgress); !started.UpdatedAt.After(m.UpdatedAt) {
		t.Fatalf("start kept updated_at %v", started.UpdatedAt)
	}
	refused("cancelling a meeting in progress", mtOrganizer, 2, domain.MeetingStatusCancelled, domain.ErrMeetingInvalidState)
	refused("stale end", mtOrganizer, 1, domain.MeetingStatusEnded, domain.ErrMeetingStaleVersion)

	mtMove(t, ctx, store, mtScope(mtOrganizer, m.ID), 2, domain.MeetingStatusEnded)
	refused("restarting an ended meeting", mtOrganizer, 3, domain.MeetingStatusInProgress, domain.ErrMeetingInvalidState)
	refused("rescheduling an ended meeting", mtOrganizer, 3, domain.MeetingStatusScheduled, domain.ErrMeetingInvalidState)
	_, err := store.UpdateMeeting(ctx, mtScope(mtOrganizer, m.ID), 3, mtDetails(""))
	mtWant(t, "editing an ended meeting", err, domain.ErrMeetingInvalidState)

	read, _, err := store.GetMeeting(ctx, mtScope(mtAttendee, m.ID))
	if err != nil || read.Status != domain.MeetingStatusEnded || read.Version != 3 {
		t.Fatalf("persisted state = %s v%d, %v", read.Status, read.Version, err)
	}
}

// mtMove makes a transition that must succeed, and checks it took effect and
// advanced the version by one.
func mtMove(t *testing.T, ctx context.Context, store *storage.PGXMeetingStore, scope storage.MeetingScope, version int64, next domain.MeetingStatus) domain.Meeting {
	t.Helper()
	moved, err := store.TransitionMeeting(ctx, scope, version, next)
	if err != nil || moved.Status != next || moved.Version != version+1 {
		t.Fatalf("%s from v%d = %s v%d, %v", next, version, moved.Status, moved.Version, err)
	}
	return moved
}

func TestMeetingCancelIsIdempotentPostgreSQL(t *testing.T) {
	pool, ctx := meetingsPostgres(t)
	store := storage.NewPGXMeetingStore(pool)
	m := mtCreate(t, ctx, store, nil, "")
	cancelled, err := store.TransitionMeeting(ctx, mtScope(mtOrganizer, m.ID), 1, domain.MeetingStatusCancelled)
	if err != nil || cancelled.Status != domain.MeetingStatusCancelled || cancelled.Version != 2 {
		t.Fatalf("cancel = %+v, %v", cancelled, err)
	}
	before := mtSnapshot(t, ctx, pool, m.ID)
	// The retry carries the version the first request saw.
	replayed, err := store.TransitionMeeting(ctx, mtScope(mtOrganizer, m.ID), 1, domain.MeetingStatusCancelled)
	if err != nil || replayed != cancelled {
		t.Fatalf("replay = %+v, %v; want %+v", replayed, err, cancelled)
	}
	if after := mtSnapshot(t, ctx, pool, m.ID); after != before {
		t.Fatal("a replayed cancel wrote to the meeting")
	}
	_, err = store.UpdateMeeting(ctx, mtScope(mtOrganizer, m.ID), 2, mtDetails(""))
	mtWant(t, "editing a cancelled meeting", err, domain.ErrMeetingInvalidState)
	_, err = store.TransitionMeeting(ctx, mtScope(mtOrganizer, m.ID), 2, domain.MeetingStatusInProgress)
	mtWant(t, "starting a cancelled meeting", err, domain.ErrMeetingInvalidState)
}

func TestMeetingOptimisticConcurrencyPostgreSQL(t *testing.T) {
	pool, ctx := meetingsPostgres(t)
	store := storage.NewPGXMeetingStore(pool)
	m := mtCreate(t, ctx, store, nil, "")

	const writers = 8
	var wg sync.WaitGroup
	errs := make([]error, writers)
	for i := range writers {
		wg.Go(func() {
			details := mtDetails("")
			details.Title = "Versão " + strconv.Itoa(i)
			_, errs[i] = store.UpdateMeeting(ctx, mtScope(mtOrganizer, m.ID), 1, details)
		})
	}
	wg.Wait()
	won := slices.Index(errs, nil)
	if won < 0 || countErrors(errs, domain.ErrMeetingStaleVersion) != writers-1 {
		t.Fatalf("writers = %v, want one winner and %d stale versions", errs, writers-1)
	}
	read, _, err := store.GetMeeting(ctx, mtScope(mtOrganizer, m.ID))
	if err != nil || read.Version != 2 || read.Title != "Versão "+strconv.Itoa(won) {
		t.Fatalf("after the race: version %d title %q (winner %d), %v", read.Version, read.Title, won, err)
	}

	// A cohost may edit but not widen who can join; the organizer may.
	widened := mtDetails(domain.MeetingJoinWorkspaceLink)
	_, err = store.UpdateMeeting(ctx, mtScope(mtCohost, m.ID), 2, widened)
	mtWant(t, "cohost widens the join policy", err, domain.ErrForbidden)
	updated, err := store.UpdateMeeting(ctx, mtScope(mtOrganizer, m.ID), 2, widened)
	if err != nil || updated.JoinPolicy != domain.MeetingJoinWorkspaceLink || updated.Version != 3 {
		t.Fatalf("organizer widens = %+v, %v", updated, err)
	}
	invalid := widened
	invalid.Timezone = "-03:00"
	_, err = store.UpdateMeeting(ctx, mtScope(mtCohost, m.ID), 3, invalid)
	mtWant(t, "offset as timezone", err, domain.ErrMeetingInvalidTimezone)
}

func countErrors(errs []error, target error) int {
	var n int
	for _, err := range errs {
		if errors.Is(err, target) {
			n++
		}
	}
	return n
}

func TestMeetingParticipantsPostgreSQL(t *testing.T) {
	pool, ctx := meetingsPostgres(t)
	store := storage.NewPGXMeetingStore(pool)
	m := mtCreate(t, ctx, store, mtChannelConversation, "")
	organizer, cohost, attendee := mtScope(mtOrganizer, m.ID), mtScope(mtCohost, m.ID), mtScope(mtAttendee, m.ID)

	refused := func(name string, op func() error, want error) {
		t.Helper()
		before := mtSnapshot(t, ctx, pool, m.ID)
		mtWant(t, name, op(), want)
		if after := mtSnapshot(t, ctx, pool, m.ID); after != before {
			t.Fatalf("%s changed the roster", name)
		}
	}
	add := func(scope storage.MeetingScope, user string, role domain.MeetingRole) func() error {
		return func() error { _, err := store.AddMeetingParticipant(ctx, scope, user, role); return err }
	}
	change := func(scope storage.MeetingScope, user string, role domain.MeetingRole) func() error {
		return func() error { _, err := store.ChangeMeetingParticipantRole(ctx, scope, user, role); return err }
	}
	remove := func(scope storage.MeetingScope, user string) func() error {
		return func() error { return store.RemoveMeetingParticipant(ctx, scope, user) }
	}
	refused("duplicate participant", add(organizer, mtAttendee, domain.MeetingRoleAttendee), domain.ErrMeetingParticipantExists)
	refused("other workspace's user", add(organizer, mtForeign, domain.MeetingRoleAttendee), domain.ErrMeetingParticipantNotEligible)
	refused("suspended account", add(organizer, mtSuspended, domain.MeetingRoleAttendee), domain.ErrMeetingParticipantNotEligible)
	refused("member outside the channel", add(organizer, mtMember, domain.MeetingRoleAttendee), domain.ErrMeetingParticipantNotEligible)
	refused("user without an account", add(organizer, mtNobody, domain.MeetingRoleAttendee), domain.ErrMeetingParticipantNotEligible)
	refused("malformed user id", add(organizer, "nobody", domain.MeetingRoleAttendee), domain.ErrInvalidInput)
	refused("missing role", add(organizer, mtGuest, ""), domain.ErrInvalidInput)
	refused("organizer role", add(organizer, mtGuest, domain.MeetingRoleOrganizer), domain.ErrInvalidInput)
	refused("attendee invites", add(attendee, mtGuest, domain.MeetingRoleAttendee), domain.ErrForbidden)
	refused("cohost invites a cohost", add(cohost, mtGuest, domain.MeetingRoleCohost), domain.ErrForbidden)
	refused("attendee promotes itself", change(attendee, mtAttendee, domain.MeetingRoleCohost), domain.ErrForbidden)
	refused("cohost demotes the organizer", change(cohost, mtOrganizer, domain.MeetingRoleAttendee), domain.ErrForbidden)
	refused("cohost removes the organizer", remove(cohost, mtOrganizer), domain.ErrForbidden)
	refused("organizer removes itself", remove(organizer, mtOrganizer), domain.ErrForbidden)
	refused("re-role a non-participant", change(organizer, mtGuest, domain.MeetingRoleAttendee), domain.ErrNotFound)
	refused("remove a non-participant", remove(organizer, mtGuest), domain.ErrNotFound)

	refused("guest excluded from invitations", add(cohost, mtGuest, domain.MeetingRoleAttendee), domain.ErrMeetingParticipantNotEligible)
	// Continue testing role administration with an internal member.
	if _, err := pool.Exec(ctx, `UPDATE chat.workspace_members SET role = 'member' WHERE workspace_id = $1 AND user_id = $2`, mtWS, mtGuest); err != nil {
		t.Fatal(err)
	}
	guest, err := store.AddMeetingParticipant(ctx, cohost, mtGuest, domain.MeetingRoleAttendee)
	if err != nil || guest.Role != domain.MeetingRoleAttendee || guest.RSVP != domain.MeetingRSVPPending || guest.InvitedAt.IsZero() {
		t.Fatalf("cohost invites an internal channel member = %+v, %v", guest, err)
	}
	promoted, err := store.ChangeMeetingParticipantRole(ctx, organizer, mtGuest, domain.MeetingRoleCohost)
	if err != nil || promoted.Role != domain.MeetingRoleCohost {
		t.Fatalf("organizer promotes = %+v, %v", promoted, err)
	}
	refused("cohost removes a cohost", remove(cohost, mtGuest), domain.ErrForbidden)
	mtWant(t, "organizer removes the cohost", store.RemoveMeetingParticipant(ctx, organizer, mtGuest), nil)
	mtWant(t, "cohost removes an attendee", store.RemoveMeetingParticipant(ctx, cohost, mtAttendee), nil)
	_, _, err = store.GetMeeting(ctx, attendee)
	mtWant(t, "removed attendee reads the meeting", err, domain.ErrNotFound)

	_, participants, err := store.GetMeeting(ctx, organizer)
	if err != nil || len(participants) != 2 {
		t.Fatalf("roster = %+v, %v", participants, err)
	}
	_, err = store.TransitionMeeting(ctx, organizer, 1, domain.MeetingStatusCancelled)
	mtWant(t, "cancel", err, nil)
	refused("inviting to a cancelled meeting", add(organizer, mtAttendee, domain.MeetingRoleAttendee), domain.ErrMeetingInvalidState)
}

func TestMeetingConcurrentParticipantChangesPostgreSQL(t *testing.T) {
	pool, ctx := meetingsPostgres(t)
	// #1054 excludes guests from new invitations; this race exercises internal users.
	if _, err := pool.Exec(ctx, `UPDATE chat.workspace_members SET role = 'member' WHERE workspace_id = $1 AND user_id = $2`, mtWS, mtGuest); err != nil {
		t.Fatal(err)
	}
	store := storage.NewPGXMeetingStore(pool)
	m, err := domain.NewMeeting(mtWS, mtOrganizer, nil, mtDetails(""))
	if err != nil {
		t.Fatal(err)
	}
	if m, err = store.CreateMeeting(ctx, m); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddMeetingParticipant(ctx, mtScope(mtOrganizer, m.ID), mtCohost, domain.MeetingRoleCohost); err != nil {
		t.Fatal(err)
	}
	// Two managers invite the same user at once, and two others concurrently.
	var wg sync.WaitGroup
	results := make([]error, 4)
	invites := []struct{ actor, user string }{
		{mtOrganizer, mtAttendee}, {mtCohost, mtAttendee}, {mtOrganizer, mtMember}, {mtCohost, mtGuest},
	}
	for i, invite := range invites {
		wg.Go(func() {
			_, results[i] = store.AddMeetingParticipant(ctx, mtScope(invite.actor, m.ID), invite.user, domain.MeetingRoleAttendee)
		})
	}
	wg.Wait()
	if countErrors(results[:2], domain.ErrMeetingParticipantExists) != 1 || (results[0] == nil) == (results[1] == nil) {
		t.Fatalf("same-user invites = %v, want exactly one success", results[:2])
	}
	if results[2] != nil || results[3] != nil {
		t.Fatalf("distinct invites = %v", results[2:])
	}
	if _, got := mtCount(t, ctx, pool); got != 5 {
		t.Fatalf("roster rows = %d, want organizer, cohost and three attendees", got)
	}
}

func TestMeetingRSVPPostgreSQL(t *testing.T) {
	pool, ctx := meetingsPostgres(t)
	store := storage.NewPGXMeetingStore(pool)
	m := mtCreate(t, ctx, store, nil, domain.MeetingJoinWorkspaceLink)

	before := mtSnapshot(t, ctx, pool, m.ID)
	_, err := store.SetMeetingRSVP(ctx, mtScope(mtOrganizer, m.ID), domain.MeetingRSVPDeclined)
	mtWant(t, "organizer answers", err, domain.ErrForbidden)
	_, err = store.SetMeetingRSVP(ctx, mtScope(mtMember, m.ID), domain.MeetingRSVPAccepted)
	mtWant(t, "link viewer answers", err, domain.ErrForbidden)
	_, err = store.SetMeetingRSVP(ctx, mtScope(mtAttendee, m.ID), "maybe")
	mtWant(t, "unknown answer", err, domain.ErrInvalidInput)
	if after := mtSnapshot(t, ctx, pool, m.ID); after != before {
		t.Fatal("a refused RSVP changed the meeting")
	}

	for _, answer := range []domain.MeetingRSVP{domain.MeetingRSVPTentative, domain.MeetingRSVPAccepted, domain.MeetingRSVPAccepted} {
		p, err := store.SetMeetingRSVP(ctx, mtScope(mtAttendee, m.ID), answer)
		p.InvitedAt, p.UpdatedAt = time.Time{}, time.Time{}
		if want := (domain.MeetingParticipant{MeetingID: m.ID, UserID: mtAttendee, Role: domain.MeetingRoleAttendee, RSVP: answer}); err != nil || p != want {
			t.Fatalf("attendee answers %s = %+v, %v", answer, p, err)
		}
	}
	read, participants, err := store.GetMeeting(ctx, mtScope(mtCohost, m.ID))
	if err != nil || read.Version != m.Version {
		t.Fatalf("an RSVP changed the meeting's version: %d, %v", read.Version, err)
	}
	answers := map[string]domain.MeetingRSVP{}
	for _, p := range participants {
		answers[p.UserID] = p.RSVP
	}
	want := map[string]domain.MeetingRSVP{mtOrganizer: domain.MeetingRSVPAccepted, mtCohost: domain.MeetingRSVPPending, mtAttendee: domain.MeetingRSVPAccepted}
	if !maps.Equal(answers, want) {
		t.Fatalf("answers = %v, want only the attendee's own to change: %v", answers, want)
	}
	_, err = store.TransitionMeeting(ctx, mtScope(mtOrganizer, m.ID), 1, domain.MeetingStatusCancelled)
	mtWant(t, "cancel", err, nil)
	_, err = store.SetMeetingRSVP(ctx, mtScope(mtAttendee, m.ID), domain.MeetingRSVPDeclined)
	mtWant(t, "answering a cancelled meeting", err, domain.ErrMeetingInvalidState)
}

// The down migration drops tables for a whole database, so the round trip runs
// in a database this test creates for itself, never in the shared one the other
// suites use. A meeting in the shared database must outlive it.
func TestMeetingMigrationRoundTripPostgreSQL(t *testing.T) {
	pool, ctx := meetingsPostgres(t)
	sentinel := mtStore(t, ctx, storage.NewPGXMeetingStore(pool), nil, mtDetails(""))
	shared := mtSnapshot(t, ctx, pool, sentinel.ID)
	own := mtDisposableDatabase(t, connectTo)

	mtMigrate(t, own, "000071_meeting_creation_idempotency.down.sql")
	relations, schema := mtRelations(t, own), mtMeetingSchema(t, own)
	owned := mtMeetingRelations(t, own)
	mtMigrate(t, own, "000070_meetings.down.sql")
	left := mtRelations(t, own)
	removed := slices.DeleteFunc(slices.Clone(relations), func(r string) bool { return slices.Contains(left, r) })
	if !slices.Equal(removed, owned) || len(left)+len(removed) != len(relations) {
		t.Fatalf("down removed %v, want exactly the meeting tables and their indexes %v", removed, owned)
	}
	if mtSnapshot(t, ctx, pool, sentinel.ID) != shared {
		t.Fatal("the down migration reached the shared database")
	}
	mtMigrate(t, own, "000070_meetings.up.sql")
	if !slices.Equal(mtRelations(t, own), relations) || mtMeetingSchema(t, own) != schema {
		t.Fatal("re-applying the up migration did not restore the same schema")
	}
	mtMigrate(t, own, "000071_meeting_creation_idempotency.up.sql")
	if mtSnapshot(t, ctx, pool, sentinel.ID) != shared || !hasTable(t, own, "chat.meetings") {
		t.Fatal("the round trip changed the shared meeting or left the tables missing")
	}
}

// mtDisposableDatabase creates a database for the calling test alone, under a
// random name no other run or suite can be using, connects to it with connect
// and brings it to the current schema with every up migration in the order
// scripts/db/migrate.sh applies them (find | sort). The drop is registered as
// soon as this call's own CREATE succeeded, before anything that can end the
// test, so it can only reach the database this call made and always does.
// Cleanups run last in, first out: the connection closes, then the drop runs
// on the admin connection, then that closes.
func mtDisposableDatabase(t *testing.T, connect func(*testing.T, string) *pgx.Conn) *pgx.Conn {
	t.Helper()
	dsn := os.Getenv("CHAT_TEST_DATABASE_URL")
	name := "meeting_1052_" + strings.ReplaceAll(uuid.NewString(), "-", "") + "_test"
	admin := connectTo(t, databaseDSN(t, dsn, "postgres"))
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	if _, err := admin.Exec(t.Context(), `CREATE DATABASE `+name); err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), `DROP DATABASE `+name+` WITH (FORCE)`); err != nil {
			t.Errorf("drop %s: %v", name, err)
		}
	})
	conn := connect(t, databaseDSN(t, dsn, name))
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	if !queryBool(t, conn, `SELECT current_database() = $1`, name) {
		t.Fatalf("connected to the wrong database, want %s", name)
	}
	migrations := os.DirFS(migrationsRoot(t))
	paths, err := fs.Glob(migrations, "*/*.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	for _, path := range paths {
		contents, err := fs.ReadFile(migrations, path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(t.Context(), string(contents)); err != nil {
			t.Fatalf("apply %s: %v", path, err)
		}
	}
	return conn
}

// A connection that fails right after CREATE DATABASE still drops the
// database. The failure has to end a test for real, so a child `go test` run
// of this test makes it, and this run reads the child's verdict and the
// catalog.
func TestMeetingDisposableDatabaseCleanupPostgreSQL(t *testing.T) {
	if os.Getenv("MEETING_1052_FAIL_CONNECT") == "1" {
		mtDisposableDatabase(t, func(t *testing.T, dsn string) *pgx.Conn {
			t.Fatalf("injected connection failure to %s", dsn[strings.LastIndex(dsn, "/")+1:])
			return nil
		})
		return
	}
	pool, ctx := meetingsPostgres(t)
	sentinel := mtStore(t, ctx, storage.NewPGXMeetingStore(pool), nil, mtDetails(""))
	shared := mtSnapshot(t, ctx, pool, sentinel.ID)
	child := exec.CommandContext(ctx, "go", "test", "-count=1", "-run=^TestMeetingDisposableDatabaseCleanupPostgreSQL$", ".")
	child.Env = append(os.Environ(), "MEETING_1052_FAIL_CONNECT=1")
	output, err := child.CombinedOutput()
	name := regexp.MustCompile(`injected connection failure to (meeting_1052_[0-9a-f]{32}_test)`).FindSubmatch(output)
	if err == nil || name == nil || !strings.Contains(string(output), "--- FAIL") {
		t.Fatalf("the child did not fail on the injected connection (%v):\n%s", err, output)
	}
	var left bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, string(name[1])).Scan(&left); err != nil || left {
		t.Fatalf("%s left behind = %v, %v", name[1], left, err)
	}
	if mtSnapshot(t, ctx, pool, sentinel.ID) != shared {
		t.Fatal("the failed run changed the shared database")
	}
}

func mtMigrate(t *testing.T, conn *pgx.Conn, name string) {
	t.Helper()
	if _, err := conn.Exec(t.Context(), readChatMigration(t, name)); err != nil {
		t.Fatalf("apply %s: %v", name, err)
	}
}

// mtRelations names every table, index and sequence outside the system schemas.
func mtRelations(t *testing.T, conn *pgx.Conn) []string {
	t.Helper()
	return mtNames(t, conn, `SELECT n.nspname || '.' || c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname NOT IN ('information_schema', 'pg_catalog', 'pg_toast') ORDER BY 1`)
}

// mtMeetingRelations names the two meeting tables and their indexes.
func mtMeetingRelations(t *testing.T, conn *pgx.Conn) []string {
	t.Helper()
	return mtNames(t, conn, `SELECT 'chat.' || relname FROM pg_class
		WHERE oid IN ('chat.meetings'::regclass, 'chat.meeting_participants'::regclass)
		   OR oid IN (SELECT indexrelid FROM pg_index WHERE indrelid IN ('chat.meetings'::regclass, 'chat.meeting_participants'::regclass))
		ORDER BY 1`)
}

func mtNames(t *testing.T, conn *pgx.Conn, sql string) []string {
	t.Helper()
	rows, err := conn.Query(t.Context(), sql)
	if err != nil {
		t.Fatal(err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return names
}

// mtMeetingSchema is every constraint of the meeting tables, definitions included.
func mtMeetingSchema(t *testing.T, conn *pgx.Conn) string {
	t.Helper()
	var schema string
	if err := conn.QueryRow(t.Context(), `SELECT string_agg(conname || ' ' || pg_get_constraintdef(oid), E'\n' ORDER BY conname)
		FROM pg_constraint WHERE conrelid IN ('chat.meetings'::regclass, 'chat.meeting_participants'::regclass)`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	return schema
}

// A revocation that commits while a write waits for the facts it locks is what
// the write reads: it is refused and persists nothing. Each case is a different
// path that can take a capability away.
func TestMeetingRevocationBeforeWritePostgreSQL(t *testing.T) {
	type write func(context.Context, *storage.PGXMeetingStore, domain.Meeting) error
	tests := []struct {
		name, revoke string
		args         []any
		write        write
		want         error
	}{
		{"organizer leaves the workspace", `UPDATE chat.workspace_members SET status = 'left' WHERE workspace_id = $1 AND user_id = $2`, []any{mtWS, mtOrganizer},
			func(ctx context.Context, s *storage.PGXMeetingStore, m domain.Meeting) error {
				_, err := s.UpdateMeeting(ctx, mtScope(mtOrganizer, m.ID), 1, mtDetails(""))
				return err
			}, domain.ErrNotFound},
		{"organizer's account is suspended", `UPDATE auth.users SET status = 'suspended' WHERE id = $1`, []any{mtOrganizer},
			func(ctx context.Context, s *storage.PGXMeetingStore, m domain.Meeting) error {
				_, err := s.TransitionMeeting(ctx, mtScope(mtOrganizer, m.ID), 1, domain.MeetingStatusCancelled)
				return err
			}, domain.ErrNotFound},
		{"cohost's membership is suspended", `UPDATE chat.workspace_members SET status = 'suspended' WHERE workspace_id = $1 AND user_id = $2`, []any{mtWS, mtCohost},
			func(ctx context.Context, s *storage.PGXMeetingStore, m domain.Meeting) error {
				return s.RemoveMeetingParticipant(ctx, mtScope(mtCohost, m.ID), mtAttendee)
			}, domain.ErrNotFound},
		{"the workspace is disabled", `UPDATE chat.workspaces SET status = 'disabled' WHERE id = $1`, []any{mtWS},
			func(ctx context.Context, s *storage.PGXMeetingStore, m domain.Meeting) error {
				_, err := s.SetMeetingRSVP(ctx, mtScope(mtAttendee, m.ID), domain.MeetingRSVPDeclined)
				return err
			}, domain.ErrNotFound},
		{"the invitee leaves the channel", `DELETE FROM chat.channel_members WHERE channel_id = $1 AND user_id = $2`, []any{mtChannel, mtGuest},
			func(ctx context.Context, s *storage.PGXMeetingStore, m domain.Meeting) error {
				_, err := s.AddMeetingParticipant(ctx, mtScope(mtOrganizer, m.ID), mtGuest, domain.MeetingRoleAttendee)
				return err
			}, domain.ErrMeetingParticipantNotEligible},
		{"the organizer is demoted to guest", `UPDATE chat.workspace_members SET role = 'guest' WHERE workspace_id = $1 AND user_id = $2`, []any{mtWS, mtOrganizer},
			func(ctx context.Context, s *storage.PGXMeetingStore, _ domain.Meeting) error {
				m, err := domain.NewMeeting(mtWS, mtOrganizer, mtChannelConversation, mtDetails(""))
				if err == nil {
					_, err = s.CreateMeeting(ctx, m)
				}
				return err
			}, domain.ErrForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool, ctx := meetingsPostgres(t)
			store := storage.NewPGXMeetingStore(pool)
			m := mtCreate(t, ctx, store, mtChannelConversation, "")
			before := mtState(t, ctx, pool, m.ID)
			err := mtRevokeDuring(t, ctx, pool, tt.revoke, tt.args, func() error { return tt.write(ctx, store, m) })
			mtWant(t, tt.name, err, tt.want)
			if after := mtState(t, ctx, pool, m.ID); after != before {
				t.Fatalf("a write refused after the revocation persisted:\n%s\n%s", before, after)
			}
		})
	}
}

// mtRevokeDuring holds an uncommitted revocation, starts write, waits until
// PostgreSQL shows write blocked on the revocation's locks, commits the
// revocation and returns what write returned.
func mtRevokeDuring(t *testing.T, ctx context.Context, pool *pgxpool.Pool, revoke string, args []any, write func() error) error {
	t.Helper()
	revoker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = revoker.Rollback(context.Background()) }()
	var pid int
	if err := revoker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if _, err := revoker.Exec(ctx, revoke, args...); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	written := make(chan error, 1)
	go func() { written <- write() }()
	mtWaitBlocked(t, pool, 1, pid)
	if err := revoker.Commit(ctx); err != nil {
		t.Fatalf("commit the revocation: %v", err)
	}
	return <-written
}

// mtState is the meeting's persisted state plus how many meetings this suite
// has, so a refused create is covered too.
func mtState(t *testing.T, ctx context.Context, pool *pgxpool.Pool, meetingID string) string {
	t.Helper()
	meetings, participants := mtCount(t, ctx, pool)
	return mtSnapshot(t, ctx, pool, meetingID) + strconv.Itoa(meetings) + "/" + strconv.Itoa(participants)
}

// A write that holds the actor's facts first is not undone by a revocation
// that arrives meanwhile: the revocation waits for the write to commit and
// takes effect after it, and the next write is refused.
func TestMeetingWriteBeforeRevocationPostgreSQL(t *testing.T) {
	pool, ctx := meetingsPostgres(t)
	store := storage.NewPGXMeetingStore(pool)
	m := mtCreate(t, ctx, store, mtChannelConversation, "")
	revoked := make(chan error, 1)
	// Paused right after the write has locked and read the organizer's facts.
	paused := &mtPausingPool{Pool: pool, steps: []mtPause{{"chat.workspace_members", func(writer int) {
		go func() {
			_, err := pool.Exec(ctx, `UPDATE chat.workspace_members SET status = 'left' WHERE workspace_id = $1 AND user_id = $2`, mtWS, mtOrganizer)
			revoked <- err
		}()
		mtWaitBlocked(t, pool, 1, writer)
	}}}}
	details := mtDetails("")
	details.Title = "Antes da saída"
	updated, err := storage.NewPGXMeetingStore(paused).UpdateMeeting(ctx, mtScope(mtOrganizer, m.ID), 1, details)
	if err != nil || updated.Version != 2 {
		t.Fatalf("the write that came first = v%d, %v", updated.Version, err)
	}
	mtWant(t, "the revocation after the write", <-revoked, nil)
	read, _, err := store.GetMeeting(ctx, mtScope(mtCohost, m.ID))
	if err != nil || read.Version != 2 || read.Title != details.Title {
		t.Fatalf("persisted = v%d %q, %v", read.Version, read.Title, err)
	}
	_, err = store.UpdateMeeting(ctx, mtScope(mtOrganizer, m.ID), 2, mtDetails(""))
	mtWant(t, "a write after the revocation", err, domain.ErrNotFound)
}

// A read holds the meeting row FOR SHARE until it has the roster, so an edit
// and an invitation that arrive meanwhile wait for it: it returns the meeting
// and the roster of one moment, and the writes go through once it is done.
func TestMeetingReadIsOneStatePostgreSQL(t *testing.T) {
	pool, ctx := meetingsPostgres(t)
	store := storage.NewPGXMeetingStore(pool)
	m := mtStore(t, ctx, store, nil, mtDetails(""))
	_, roster, err := store.GetMeeting(ctx, mtScope(mtOrganizer, m.ID))
	if err != nil {
		t.Fatal(err)
	}
	writes := make(chan error, 2)
	edited := mtDetails("")
	edited.Title = "Versão dois"
	// Paused right after the read has the meeting row, before the roster.
	paused := &mtPausingPool{Pool: pool, steps: []mtPause{{"FROM chat.meetings WHERE", func(reader int) {
		go func() {
			_, err := store.UpdateMeeting(ctx, mtScope(mtOrganizer, m.ID), 1, edited)
			writes <- err
		}()
		editor := mtWaitBlocked(t, pool, 1, reader)
		go func() {
			_, err := store.AddMeetingParticipant(ctx, mtScope(mtOrganizer, m.ID), mtAttendee, domain.MeetingRoleAttendee)
			writes <- err
		}()
		mtWaitBlocked(t, pool, 2, append(editor, reader)...)
	}}}}
	read, readRoster, err := storage.NewPGXMeetingStore(paused).GetMeeting(ctx, mtScope(mtOrganizer, m.ID))
	if err != nil || read != m || !slices.Equal(readRoster, roster) {
		t.Fatalf("read = v%d %q with %d participants, %v; want v1 with its own roster", read.Version, read.Title, len(readRoster), err)
	}
	for range 2 {
		mtWant(t, "a write after the read", <-writes, nil)
	}
	read, readRoster, err = store.GetMeeting(ctx, mtScope(mtOrganizer, m.ID))
	if err != nil || read.Version != 2 || read.Title != edited.Title || len(readRoster) != 2 {
		t.Fatalf("after the writes = v%d %q with %d participants, %v", read.Version, read.Title, len(readRoster), err)
	}
}

// mtSuspension is auth-service's account suspension (updateUserStatusOnce),
// statement for statement through the status write. The session and OIDC
// revocations that follow touch auth tables no meeting locks.
type mtSuspension struct {
	tx   pgx.Tx
	pid  int
	user string
}

// mtBeginSuspension opens the suspension and runs prepareOwnershipInvalidation,
// which locks every private conversation of the user FOR UPDATE. The caller
// rolls tx back.
func mtBeginSuspension(ctx context.Context, pool *pgxpool.Pool, userID string) (*mtSuspension, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	s := &mtSuspension{tx: tx, user: userID}
	var available bool
	_, err = tx.Exec(ctx, conversationownership.SerializableSQL)
	if err == nil {
		err = tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&s.pid)
	}
	if err == nil {
		err = tx.QueryRow(ctx, conversationownership.AvailabilitySQL).Scan(&available)
	}
	if err == nil && !available {
		err = errors.New("the conversation ownership protocol is not installed")
	}
	if err == nil {
		_, err = tx.Exec(ctx, conversationownership.LockUserSQL, userID)
	}
	return s, err
}

// mtSuspend runs the whole suspension.
func mtSuspend(ctx context.Context, pool *pgxpool.Pool, userID string) error {
	s, err := mtBeginSuspension(ctx, pool, userID)
	if s != nil {
		defer func() { _ = s.tx.Rollback(context.Background()) }()
	}
	if err != nil {
		return err
	}
	return s.finish(ctx)
}

// finish locks the account FOR UPDATE, hands on ownership, suspends and commits.
func (s *mtSuspension) finish(ctx context.Context) error {
	var status string
	if err := s.tx.QueryRow(ctx, `SELECT status FROM auth.users WHERE id = $1 AND deleted_at IS NULL FOR UPDATE`, s.user).Scan(&status); err != nil {
		return err
	}
	session := conversationownership.Session{
		Query: func(ctx context.Context, sql string, args ...any) (conversationownership.Rows, error) {
			return s.tx.Query(ctx, sql, args...)
		},
		Exec: func(ctx context.Context, sql string, args ...any) error {
			_, err := s.tx.Exec(ctx, sql, args...)
			return err
		},
	}
	if err := conversationownership.Invalidate(ctx, session, s.user, ""); err != nil {
		return err
	}
	if _, err := s.tx.Exec(ctx, `UPDATE auth.users SET status = 'suspended', updated_at = now() WHERE id = $1 AND deleted_at IS NULL`, s.user); err != nil {
		return err
	}
	return s.tx.Commit(ctx)
}

func mtAccountStatus(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID string) string {
	t.Helper()
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM auth.users WHERE id = $1`, userID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	return status
}

type mtWrite func(context.Context, *storage.PGXMeetingStore, domain.Meeting) error

// A suspension that has locked the user's conversations comes first: the
// meeting write waits on the conversation, before it holds the account the
// suspension locks next, so neither aborts on a deadlock; the write then reads
// the suspended account and is refused. The organizer is suspended as the
// actor, the attendee as the invitee.
func TestMeetingSuspensionBeforeWritePostgreSQL(t *testing.T) {
	t.Run("the actor", func(t *testing.T) {
		mtSuspensionBeforeWrite(t, mtOrganizer, domain.ErrNotFound, func(ctx context.Context, s *storage.PGXMeetingStore, m domain.Meeting) error {
			_, err := s.UpdateMeeting(ctx, mtScope(mtOrganizer, m.ID), 1, mtDetails(""))
			return err
		})
	})
	t.Run("the invitee", func(t *testing.T) {
		mtSuspensionBeforeWrite(t, mtAttendee, domain.ErrMeetingParticipantNotEligible, func(ctx context.Context, s *storage.PGXMeetingStore, m domain.Meeting) error {
			_, err := s.AddMeetingParticipant(ctx, mtScope(mtOrganizer, m.ID), mtAttendee, domain.MeetingRoleAttendee)
			return err
		})
	})
}

func mtSuspensionBeforeWrite(t *testing.T, suspended string, want error, write mtWrite) {
	pool, ctx := meetingsPostgres(t)
	store := storage.NewPGXMeetingStore(pool)
	m := mtStore(t, ctx, store, mtChannelConversation, mtDetails(""))
	before := mtState(t, ctx, pool, m.ID)
	suspension, err := mtBeginSuspension(ctx, pool, suspended)
	if suspension != nil {
		t.Cleanup(func() { _ = suspension.tx.Rollback(context.Background()) })
	}
	if err != nil {
		t.Fatalf("begin the suspension: %v", err)
	}
	written := make(chan error, 1)
	go func() { written <- write(ctx, store, m) }()
	mtWaitBlocked(t, pool, 1, suspension.pid)
	mtWant(t, "the suspension", suspension.finish(ctx), nil)
	mtWant(t, "the write after the suspension", <-written, want)
	if after := mtState(t, ctx, pool, m.ID); after != before {
		t.Fatalf("a refused write persisted:\n%s\n%s", before, after)
	}
	if status := mtAccountStatus(t, ctx, pool, suspended); status != "suspended" {
		t.Fatalf("account status = %q", status)
	}
}

// A meeting write that holds its facts first commits; the suspension waits on
// the meeting's conversation, which it locks among the user's others in
// (kind, id) order, and goes through after it. In the group DM case it already
// holds the private channel, which sorts first, when it reaches the DM.
func TestMeetingWriteBeforeSuspensionPostgreSQL(t *testing.T) {
	for _, conversation := range []*domain.MeetingConversation{mtChannelConversation,
		{Kind: domain.MeetingConversationDM, ID: mtGroup}} {
		t.Run(string(conversation.Kind), func(t *testing.T) { mtWriteBeforeSuspension(t, conversation) })
	}
}

func mtWriteBeforeSuspension(t *testing.T, conversation *domain.MeetingConversation) {
	pool, ctx := meetingsPostgres(t)
	store := storage.NewPGXMeetingStore(pool)
	m := mtStore(t, ctx, store, conversation, mtDetails(""))
	suspended := make(chan error, 1)
	paused := &mtPausingPool{Pool: pool, steps: []mtPause{{"chat.workspace_members", func(writer int) {
		go func() { suspended <- mtSuspend(ctx, pool, mtOrganizer) }()
		mtWaitBlocked(t, pool, 1, writer)
	}}}}
	details := mtDetails("")
	details.Title = "Antes da suspensão"
	updated, err := storage.NewPGXMeetingStore(paused).UpdateMeeting(ctx, mtScope(mtOrganizer, m.ID), 1, details)
	if err != nil || updated.Version != 2 {
		t.Fatalf("the write that came first = v%d, %v", updated.Version, err)
	}
	mtWant(t, "the suspension after the write", <-suspended, nil)
	if status := mtAccountStatus(t, ctx, pool, mtOrganizer); status != "suspended" {
		t.Fatalf("account status = %q", status)
	}
	var version int64
	var title string
	if err := pool.QueryRow(ctx, `SELECT version, title FROM chat.meetings WHERE id = $1`, m.ID).Scan(&version, &title); err != nil || version != 2 || title != details.Title {
		t.Fatalf("persisted = v%d %q, %v", version, title, err)
	}
	_, err = store.UpdateMeeting(ctx, mtScope(mtOrganizer, m.ID), 2, mtDetails(""))
	mtWant(t, "a write after the suspension", err, domain.ErrNotFound)
}

// A membership that did not exist when a write locked its facts grants
// nothing, even once it is inserted, and so cannot be revoked under the write's
// feet: the write is refused and persists nothing. A meeting without a
// conversation rests on the workspace membership alone. Inserting a
// conversation membership locks the conversation at commit (and a workspace
// member is added to its public channels), so that insert waits for a write
// on the conversation instead. A membership the write did lock keeps its
// revocation waiting until the write commits.
func TestMeetingMembershipAfterLockPostgreSQL(t *testing.T) {
	group := &domain.MeetingConversation{Kind: domain.MeetingConversationDM, ID: mtGroup}
	for _, tt := range []mtMembershipCase{
		{"the actor's workspace membership", nil, mtForeign, mtForeign, mtWS,
			`INSERT INTO chat.workspace_members (workspace_id, user_id, role) VALUES ($1, $2, 'member')`,
			`DELETE FROM chat.workspace_members WHERE workspace_id = $1 AND user_id = $2`, false, domain.ErrForbidden},
		{"the invitee's workspace membership", nil, mtOrganizer, mtForeign, mtWS,
			`INSERT INTO chat.workspace_members (workspace_id, user_id, role) VALUES ($1, $2, 'member')`,
			`DELETE FROM chat.workspace_members WHERE workspace_id = $1 AND user_id = $2`, false, domain.ErrMeetingParticipantNotEligible},
		{"the invitee's channel membership", mtChannelConversation, mtOrganizer, mtMember, mtChannel,
			`INSERT INTO chat.channel_members (channel_id, user_id) VALUES ($1, $2)`,
			`DELETE FROM chat.channel_members WHERE channel_id = $1 AND user_id = $2`, true, domain.ErrMeetingParticipantNotEligible},
		{"the invitee's DM membership", group, mtOrganizer, mtCohost, mtGroup,
			`INSERT INTO chat.dm_members (conversation_id, user_id) VALUES ($1, $2)`,
			`DELETE FROM chat.dm_members WHERE conversation_id = $1 AND user_id = $2`, true, domain.ErrMeetingParticipantNotEligible},
		{"a locked channel membership", mtChannelConversation, mtOrganizer, mtGuest, mtChannel, "",
			`DELETE FROM chat.channel_members WHERE channel_id = $1 AND user_id = $2`, false, nil},
	} {
		t.Run(tt.name, tt.run)
	}
}

type mtMembershipCase struct {
	name         string
	conversation *domain.MeetingConversation
	actor, user  string // user is the one whose membership is inserted or revoked
	on           string // the workspace or conversation of that membership
	insert       string
	revoke       string
	insertWaits  bool
	want         error
}

func (c mtMembershipCase) run(t *testing.T) {
	pool, ctx := meetingsPostgres(t)
	if c.user == mtGuest && c.want == nil {
		if _, err := pool.Exec(ctx, `UPDATE chat.workspace_members SET role = 'member' WHERE workspace_id = $1 AND user_id = $2`, mtWS, mtGuest); err != nil {
			t.Fatal(err)
		}
	}
	m := mtStore(t, ctx, storage.NewPGXMeetingStore(pool), c.conversation, mtDetails(""))
	var inserted, revoked <-chan error
	insertWaited, revokeWaited := false, false
	paused := &mtPausingPool{Pool: pool, steps: []mtPause{
		// Right after the write's locks, which name the user.
		{c.user, func(writer int) {
			if c.insert != "" {
				insertWaited, inserted = mtSettle(t, pool, writer, c.insert, c.on, c.user)
			}
		}},
		// Right after the write's own insert, before it commits.
		{"INSERT INTO chat.meeting", func(writer int) {
			revokeWaited, revoked = mtSettle(t, pool, writer, c.revoke, c.on, c.user)
		}},
	}}
	mtWant(t, c.name, c.write(ctx, storage.NewPGXMeetingStore(paused), m), c.want)
	if insertWaited != c.insertWaits {
		t.Fatalf("the insert waited for the write = %v, want %v", insertWaited, c.insertWaits)
	}
	mtDrain(t, inserted, c.insert != "")
	mtDrain(t, revoked, c.want == nil)
	mtMembershipOutcome(t, ctx, pool, c.user, c.want == nil, revokeWaited)
}

// write creates a meeting when the user is the actor, and invites the user
// otherwise.
func (c mtMembershipCase) write(ctx context.Context, store *storage.PGXMeetingStore, m domain.Meeting) error {
	if c.actor != c.user {
		_, err := store.AddMeetingParticipant(ctx, mtScope(c.actor, m.ID), c.user, domain.MeetingRoleAttendee)
		return err
	}
	next, err := domain.NewMeeting(mtWS, c.actor, c.conversation, mtDetails(""))
	if err == nil {
		_, err = store.CreateMeeting(ctx, next)
	}
	return err
}

// mtDrain waits for a concurrent membership change that ran, and requires it
// to have succeeded.
func mtDrain(t *testing.T, done <-chan error, ran bool) {
	t.Helper()
	if ran {
		mtWant(t, "the concurrent membership change", <-done, nil)
	}
}

// mtMembershipOutcome checks what the write left: a meeting or roster row for
// user only when the write was allowed, and then only with a revocation that
// waited for it.
func mtMembershipOutcome(t *testing.T, ctx context.Context, pool *pgxpool.Pool, user string, allowed, revokeWaited bool) {
	t.Helper()
	var rows int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM chat.meeting_participants WHERE user_id = $1)
		+ (SELECT count(*) FROM chat.meetings WHERE organizer_user_id = $1)`, user).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if (rows != 0) != allowed || allowed != revokeWaited {
		t.Fatalf("persisted rows for the user = %d, allowed %v, revocation waited %v", rows, allowed, revokeWaited)
	}
}

// mtSettle runs sql beside the transaction of backend holder and returns once
// it has either finished or is waiting for holder; done yields its result.
func mtSettle(t *testing.T, pool *pgxpool.Pool, holder int, sql string, args ...any) (waits bool, done <-chan error) {
	t.Helper()
	result := make(chan error, 1)
	go func() {
		_, err := pool.Exec(context.Background(), sql, args...)
		result <- err
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	for len(result) == 0 {
		var blocked bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid)))`, holder).Scan(&blocked); err != nil {
			t.Fatalf("waiting for %q to finish or wait: %v", sql, err)
		}
		if blocked {
			return true, result
		}
	}
	return false, result
}

// mtWaitBlocked waits until at least n backends wait on a lock held by one of
// holders, and returns them. It reads PostgreSQL's lock graph rather than
// guessing a delay; the timeout only ends a test that would otherwise hang.
func mtWaitBlocked(t *testing.T, pool *pgxpool.Pool, n int, holders ...int) []int {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	for {
		var blocked []int
		if err := pool.QueryRow(ctx, `SELECT coalesce(array_agg(pid), '{}') FROM pg_stat_activity
			WHERE pg_blocking_pids(pid) && $1::int[]`, holders).Scan(&blocked); err != nil {
			t.Fatalf("waiting for %d backends blocked by %v: %v", n, holders, err)
		}
		if len(blocked) >= n {
			return blocked
		}
	}
}

// mtPausingPool runs each step's pause, in order and once, with the backend pid
// of the transaction, right after the next statement whose SQL and arguments
// contain the step's match has been read; a batch is one statement made of all
// it queued. It lets a test act between two statements of one store
// operation; the store is unchanged.
type mtPausingPool struct {
	storage.Pool
	steps []mtPause
}

type mtPause struct {
	match string
	pause func(pid int)
}

func (p *mtPausingPool) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := p.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &mtPausingTx{Tx: tx, pool: p}, nil
}

// next returns the step statement reaches, or nil.
func (p *mtPausingPool) next(statement string) func(int) {
	if len(p.steps) == 0 || !strings.Contains(statement, p.steps[0].match) {
		return nil
	}
	pause := p.steps[0].pause
	p.steps = p.steps[1:]
	return pause
}

type mtPausingTx struct {
	pgx.Tx
	pool *mtPausingPool
}

func (tx *mtPausingTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return mtPausingRow{Row: tx.Tx.QueryRow(ctx, sql, args...), tx: tx, statement: fmt.Sprint(sql, args)}
}

func (tx *mtPausingTx) SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults {
	var statement strings.Builder
	for _, q := range b.QueuedQueries {
		fmt.Fprint(&statement, q.SQL, q.Arguments)
	}
	return mtPausingBatch{BatchResults: tx.Tx.SendBatch(ctx, b), tx: tx, statement: statement.String()}
}

// paused runs the pause statement reaches once it has been read.
func (tx *mtPausingTx) paused(statement string) error {
	pause := tx.pool.next(statement)
	if pause == nil {
		return nil
	}
	var pid int
	if err := tx.Tx.QueryRow(context.Background(), `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		return err
	}
	pause(pid)
	return nil
}

type mtPausingRow struct {
	pgx.Row
	tx        *mtPausingTx
	statement string
}

func (r mtPausingRow) Scan(dest ...any) error {
	if err := r.Row.Scan(dest...); err != nil {
		return err
	}
	return r.tx.paused(r.statement)
}

type mtPausingBatch struct {
	pgx.BatchResults
	tx        *mtPausingTx
	statement string
}

func (b mtPausingBatch) Close() error {
	if err := b.BatchResults.Close(); err != nil {
		return err
	}
	return b.tx.paused(b.statement)
}
