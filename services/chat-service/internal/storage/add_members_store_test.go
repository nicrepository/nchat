package storage_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	pgxmock "github.com/pashagolub/pgxmock/v2"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// These drive the statement sequence with pgxmock, so the ordering guarantees
// are asserted without a database: the PostgreSQL suite proves the SQL is
// correct, and these prove the store issues it in the right order and reacts to
// each outcome the right way. The two are complementary, not duplicates.

const (
	msWorkspace    = "ws-398"
	msChannel      = "ch-398"
	msConversation = "dm-398"
	msActor        = "actor-398"
	msTargetA      = "target-a"
	msTargetB      = "target-b"
)

// ── AddChannelMembers ───────────────────────────────────────────────────────

// The actor's authority is re-read before anything is written; the write only
// runs once that row comes back.
func TestPGXMemberStore_AddChannelMembers_RevalidatesActorBeforeWriting(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)FROM chat\.workspace_members wm.*wm\.role IN \('owner', 'admin'\).*FOR SHARE OF wm`).
		WithArgs(msWorkspace, msChannel, msActor).
		WillReturnRows(pgxmock.NewRows([]string{"ok"}).AddRow(true))
	mock.ExpectQuery(`(?s)WITH eligible AS.*INSERT INTO chat\.channel_members.*ON CONFLICT \(channel_id, user_id\) DO NOTHING`).
		WithArgs(msWorkspace, msChannel, []string{msTargetA, msTargetB}, "member").
		WillReturnRows(pgxmock.NewRows([]string{"eligible", "inserted", "added_ids"}).
			AddRow(2, 2, []string{msTargetA, msTargetB}))
	mock.ExpectQuery(`(?s)SELECT count\(\*\).*FROM chat\.channel_members cm`).
		WithArgs(msWorkspace, msChannel).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(7))
	mock.ExpectCommit()

	store := storage.NewPGXMemberStore(mock)
	result, err := store.AddChannelMembers(
		context.Background(), msWorkspace, msChannel, msActor, []string{msTargetA, msTargetB},
	)
	if err != nil {
		t.Fatalf("AddChannelMembers: %v", err)
	}
	if result.Added != 2 || result.AlreadyMembers != 0 || result.TotalCount != 7 {
		t.Fatalf("result = %+v, want 2/0/7", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// An actor whose authority is gone yields no row; nothing may be inserted and
// the transaction must roll back.
func TestPGXMemberStore_AddChannelMembers_RollsBackWhenActorLostAuthority(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)FOR SHARE OF wm`).
		WithArgs(msWorkspace, msChannel, msActor).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectRollback()

	store := storage.NewPGXMemberStore(mock)
	_, err = store.AddChannelMembers(
		context.Background(), msWorkspace, msChannel, msActor, []string{msTargetA},
	)

	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// Fewer eligible rows than requested means somebody is not eligible: the whole
// batch is refused rather than partially written.
func TestPGXMemberStore_AddChannelMembers_RollsBackOnIneligibleTarget(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)FOR SHARE OF wm`).
		WithArgs(msWorkspace, msChannel, msActor).
		WillReturnRows(pgxmock.NewRows([]string{"ok"}).AddRow(true))
	mock.ExpectQuery(`(?s)WITH eligible AS`).
		WithArgs(msWorkspace, msChannel, []string{msTargetA, msTargetB}, "member").
		// Two requested, one eligible.
		WillReturnRows(pgxmock.NewRows([]string{"eligible", "inserted", "added_ids"}).
			AddRow(1, 1, []string{msTargetA}))
	mock.ExpectRollback()

	store := storage.NewPGXMemberStore(mock)
	_, err = store.AddChannelMembers(
		context.Background(), msWorkspace, msChannel, msActor, []string{msTargetA, msTargetB},
	)

	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// An already-present member is reported, not raised as an error.
func TestPGXMemberStore_AddChannelMembers_ReportsExistingMembers(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)FOR SHARE OF wm`).
		WithArgs(msWorkspace, msChannel, msActor).
		WillReturnRows(pgxmock.NewRows([]string{"ok"}).AddRow(true))
	mock.ExpectQuery(`(?s)WITH eligible AS`).
		WithArgs(msWorkspace, msChannel, []string{msTargetA, msTargetB}, "member").
		WillReturnRows(pgxmock.NewRows([]string{"eligible", "inserted", "added_ids"}).
			AddRow(2, 1, []string{msTargetA}))
	mock.ExpectQuery(`(?s)SELECT count\(\*\)`).
		WithArgs(msWorkspace, msChannel).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(4))
	mock.ExpectCommit()

	store := storage.NewPGXMemberStore(mock)
	result, err := store.AddChannelMembers(
		context.Background(), msWorkspace, msChannel, msActor, []string{msTargetA, msTargetB},
	)
	if err != nil {
		t.Fatalf("AddChannelMembers: %v", err)
	}
	if result.Added != 1 || result.AlreadyMembers != 1 {
		t.Fatalf("Added/Already = %d/%d, want 1/1", result.Added, result.AlreadyMembers)
	}
}

func TestPGXMemberStore_AddChannelMembers_RejectsMissingActorWithoutQuerying(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	store := storage.NewPGXMemberStore(mock)
	_, err = store.AddChannelMembers(
		context.Background(), msWorkspace, msChannel, "  ", []string{msTargetA},
	)

	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden", err)
	}
	// No transaction was even opened for a request with no actor.
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXMemberStore_AddChannelMembers_RejectsEmptyList(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	store := storage.NewPGXMemberStore(mock)
	if _, err := store.AddChannelMembers(
		context.Background(), msWorkspace, msChannel, msActor, nil,
	); !errors.Is(err, domain.ErrNoMembersRequested) {
		t.Fatalf("err = %v, want ErrNoMembersRequested", err)
	}
}

// ── AddGroupParticipants ────────────────────────────────────────────────────

func groupAddInput(userIDs []string) storage.AddGroupParticipantsInput {
	return storage.AddGroupParticipantsInput{
		WorkspaceID: msWorkspace, ConversationID: msConversation, CallerID: msActor,
		UserIDs: userIDs,
	}
}

// Conversation lock, then actor participation, then the write, then the total —
// the order the deadlock-avoidance argument depends on.
//
// There is no read of the participant rows before the write any more, and that
// absence is the point: what this call reports comes from the write's own
// RETURNING. A pre-write read is the thing a concurrent writer invalidates.
func TestPGXDMStore_AddGroupParticipants_LocksConversationThenActor(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)FROM chat\.dm_conversations dc.*dc\.type = 'group'.*FOR SHARE OF dc`).
		WithArgs(msConversation, msWorkspace).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(msConversation))
	mock.ExpectQuery(`(?s)FROM chat\.dm_members dm.*FOR SHARE OF dm`).
		WithArgs(msConversation, msWorkspace, msActor).
		WillReturnRows(pgxmock.NewRows([]string{"ok"}).AddRow(true))
	// The upsert only touches a row that is not already active, and returns the
	// ones it made active.
	mock.ExpectQuery(`(?s)INSERT INTO chat\.dm_members AS dm.*dm\.status <> 'active'.*RETURNING user_id`).
		WithArgs(msConversation, msWorkspace, []string{msTargetA}).
		WillReturnRows(dmMemberUpsertRows(1, msTargetA))
	mock.ExpectQuery(`(?s)SELECT count\(\*\).*FROM chat\.dm_members.*status = 'active'`).
		WithArgs(msConversation).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(3))
	mock.ExpectCommit()

	store := storage.NewPGXDMStore(mock)
	result, err := store.AddGroupParticipants(context.Background(), groupAddInput([]string{msTargetA}))
	if err != nil {
		t.Fatalf("AddGroupParticipants: %v", err)
	}
	if result.Added != 1 || result.TotalCount != 3 {
		t.Fatalf("result = %+v, want 1 added / total 3", result)
	}
	if len(result.AddedUserIDs) != 1 || result.AddedUserIDs[0] != msTargetA {
		t.Fatalf("AddedUserIDs = %v, want the RETURNING of the write", result.AddedUserIDs)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// An eligible user who was already an active participant is counted by the
// eligibility CTE but returned by nothing: the ON CONFLICT branch declines to
// touch their row. So the call reports no addition and, upstream, publishes no
// event — which is what makes a retry silent.
func TestPGXDMStore_AddGroupParticipants_ReportsNothingForAnExistingParticipant(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)FROM chat\.dm_conversations dc.*FOR SHARE OF dc`).
		WithArgs(msConversation, msWorkspace).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(msConversation))
	mock.ExpectQuery(`(?s)FROM chat\.dm_members dm.*FOR SHARE OF dm`).
		WithArgs(msConversation, msWorkspace, msActor).
		WillReturnRows(pgxmock.NewRows([]string{"ok"}).AddRow(true))
	// Eligible, but the write changed nothing.
	mock.ExpectQuery(`(?s)INSERT INTO chat\.dm_members AS dm`).
		WithArgs(msConversation, msWorkspace, []string{msTargetA}).
		WillReturnRows(dmMemberUpsertRows(1))
	mock.ExpectQuery(`(?s)SELECT count\(\*\).*status = 'active'`).
		WithArgs(msConversation).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(3))
	mock.ExpectCommit()

	result, err := storage.NewPGXDMStore(mock).
		AddGroupParticipants(context.Background(), groupAddInput([]string{msTargetA}))
	if err != nil {
		t.Fatalf("AddGroupParticipants: %v", err)
	}
	if result.Added != 0 || len(result.AddedUserIDs) != 0 {
		t.Fatalf("result = %+v, want no addition reported", result)
	}
	if result.AlreadyMembers != 1 {
		t.Fatalf("AlreadyMembers = %d, want 1", result.AlreadyMembers)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// Added must never be a number the caller can read without the list agreeing:
// both come from the same RETURNING, so a mixed batch reports only the newcomer.
func TestPGXDMStore_AddGroupParticipants_AddedMatchesTheReturnedIDs(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)FROM chat\.dm_conversations dc.*FOR SHARE OF dc`).
		WithArgs(msConversation, msWorkspace).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(msConversation))
	mock.ExpectQuery(`(?s)FROM chat\.dm_members dm.*FOR SHARE OF dm`).
		WithArgs(msConversation, msWorkspace, msActor).
		WillReturnRows(pgxmock.NewRows([]string{"ok"}).AddRow(true))
	// Both eligible; only one was not already a participant.
	mock.ExpectQuery(`(?s)INSERT INTO chat\.dm_members AS dm`).
		WithArgs(msConversation, msWorkspace, []string{msTargetA, msTargetB}).
		WillReturnRows(dmMemberUpsertRows(2, msTargetB))
	mock.ExpectQuery(`(?s)SELECT count\(\*\).*status = 'active'`).
		WithArgs(msConversation).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(4))
	mock.ExpectCommit()

	result, err := storage.NewPGXDMStore(mock).
		AddGroupParticipants(context.Background(), groupAddInput([]string{msTargetA, msTargetB}))
	if err != nil {
		t.Fatalf("AddGroupParticipants: %v", err)
	}
	if result.Added != len(result.AddedUserIDs) {
		t.Fatalf("Added=%d disagrees with AddedUserIDs=%v", result.Added, result.AddedUserIDs)
	}
	if result.Added != 1 || result.AddedUserIDs[0] != msTargetB {
		t.Fatalf("result = %+v, want only %s added", result, msTargetB)
	}
	if result.AlreadyMembers != 1 {
		t.Fatalf("AlreadyMembers = %d, want 1", result.AlreadyMembers)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// The revocation case: the conversation is fine, but the actor no longer
// participates. Nothing is written and the answer is Forbidden, never the
// capacity conflict.
func TestPGXDMStore_AddGroupParticipants_RollsBackWhenActorNoLongerParticipates(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)FOR SHARE OF dc`).
		WithArgs(msConversation, msWorkspace).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(msConversation))
	mock.ExpectQuery(`(?s)FOR SHARE OF dm`).
		WithArgs(msConversation, msWorkspace, msActor).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectRollback()

	store := storage.NewPGXDMStore(mock)
	_, err = store.AddGroupParticipants(context.Background(), groupAddInput([]string{msTargetA}))

	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// A conversation that is missing, archived, 1:1 or from another tenant produces
// no lockable row, and the actor query never runs.
func TestPGXDMStore_AddGroupParticipants_RollsBackWhenConversationNotLockable(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)FOR SHARE OF dc`).
		WithArgs(msConversation, msWorkspace).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectRollback()

	store := storage.NewPGXDMStore(mock)
	_, err = store.AddGroupParticipants(context.Background(), groupAddInput([]string{msTargetA}))

	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// Groups have no fixed capacity, so a large existing membership changes nothing
// about whether a newcomer is accepted. This drives the statement sequence with
// a group that already holds far more participants than any former ceiling.
func TestPGXDMStore_AddGroupParticipants_AcceptsNewcomersRegardlessOfSize(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)FOR SHARE OF dc`).
		WithArgs(msConversation, msWorkspace).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(msConversation))
	mock.ExpectQuery(`(?s)FOR SHARE OF dm`).
		WithArgs(msConversation, msWorkspace, msActor).
		WillReturnRows(pgxmock.NewRows([]string{"ok"}).AddRow(true))
	mock.ExpectQuery(`(?s)INSERT INTO chat\.dm_members AS dm`).
		WithArgs(msConversation, msWorkspace, []string{msTargetA}).
		WillReturnRows(dmMemberUpsertRows(1, msTargetA))
	// Well past the 50 that used to be a ceiling, and nothing consults it.
	mock.ExpectQuery(`(?s)SELECT count\(\*\).*FROM chat\.dm_members.*status = 'active'`).
		WithArgs(msConversation).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(501))
	mock.ExpectCommit()

	store := storage.NewPGXDMStore(mock)
	result, err := store.AddGroupParticipants(context.Background(), groupAddInput([]string{msTargetA}))

	if err != nil {
		t.Fatalf("a large group must still accept a newcomer: %v", err)
	}
	if result.Added != 1 || result.TotalCount != 501 {
		t.Fatalf("result = %+v, want 1 added / total 501", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXDMStore_AddGroupParticipants_RejectsMissingActorAndEmptyList(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()
	store := storage.NewPGXDMStore(mock)

	if _, err := store.AddGroupParticipants(
		context.Background(), groupAddInput(nil),
	); !errors.Is(err, domain.ErrNoMembersRequested) {
		t.Fatalf("empty list err = %v, want ErrNoMembersRequested", err)
	}

	noActor := groupAddInput([]string{msTargetA})
	noActor.CallerID = "   "
	if _, err := store.AddGroupParticipants(context.Background(), noActor); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("missing actor err = %v, want ErrForbidden", err)
	}
}

// ── ListParticipantProfiles ─────────────────────────────────────────────────

func TestPGXDMStore_ListParticipantProfiles_ReturnsPageAndTotal(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`(?s)COUNT\(\*\) OVER \(\).*FROM chat\.dm_members dm.*dm\.status = 'active'`).
		WithArgs(msWorkspace, msConversation, domain.MaxDMDetailsParticipants).
		WillReturnRows(
			pgxmock.NewRows([]string{"user_id", "display_name", "avatar_url", "total_count"}).
				AddRow("u-1", "Ana", "/a.png", 12).
				AddRow("u-2", "Bruno", "", 12),
		)

	store := storage.NewPGXDMStore(mock)
	page, err := store.ListParticipantProfiles(
		context.Background(), msWorkspace, msConversation, domain.MaxDMDetailsParticipants,
	)
	if err != nil {
		t.Fatalf("ListParticipantProfiles: %v", err)
	}
	// The total describes the whole matching set, not the page.
	if page.TotalCount != 12 {
		t.Fatalf("TotalCount = %d, want 12", page.TotalCount)
	}
	if len(page.Participants) != 2 {
		t.Fatalf("Participants = %d, want 2", len(page.Participants))
	}
	if page.Participants[0].AvatarURL != "/a.png" || page.Participants[1].AvatarURL != "" {
		t.Fatalf("avatar mapping wrong: %+v", page.Participants)
	}
}

// A limit outside (0, Max] is clamped server-side; the client never picks it.
func TestPGXDMStore_ListParticipantProfiles_ClampsTheLimit(t *testing.T) {
	for name, requested := range map[string]int{
		"zero":          0,
		"negative":      -5,
		"above the cap": domain.MaxDMDetailsParticipants + 100,
	} {
		t.Run(name, func(t *testing.T) {
			mock, err := pgxmock.NewPool()
			if err != nil {
				t.Fatalf("pgxmock: %v", err)
			}
			defer mock.Close()

			mock.ExpectQuery(`(?s)FROM chat\.dm_members dm`).
				WithArgs(msWorkspace, msConversation, domain.MaxDMDetailsParticipants).
				WillReturnRows(pgxmock.NewRows([]string{"user_id", "display_name", "avatar_url", "total_count"}))

			store := storage.NewPGXDMStore(mock)
			if _, err := store.ListParticipantProfiles(
				context.Background(), msWorkspace, msConversation, requested,
			); err != nil {
				t.Fatalf("ListParticipantProfiles: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("limit was not clamped: %v", err)
			}
		})
	}
}

func TestPGXDMStore_ListParticipantProfiles_PropagatesQueryFailure(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`(?s)FROM chat\.dm_members dm`).
		WithArgs(msWorkspace, msConversation, domain.MaxDMDetailsParticipants).
		WillReturnError(errors.New("connection reset"))

	store := storage.NewPGXDMStore(mock)
	if _, err := store.ListParticipantProfiles(
		context.Background(), msWorkspace, msConversation, 0,
	); err == nil {
		t.Fatal("expected the query failure to surface")
	}
}

// ── Contextual candidate search (issue #398) ────────────────────────────────

// The NOT EXISTS is what makes the search correct; these assert it is in the
// statement the store issues and that the scope arguments reach it in order.
// The PostgreSQL suite proves the predicate itself selects the right rows.
func TestPGXMemberStore_SearchChannelMemberCandidates_ExcludesCurrentMembersInSQL(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`(?s)FROM chat\.workspace_members wm.*NOT EXISTS.*FROM chat\.channel_members cm.*cm\.channel_id = \$2::uuid`).
		WithArgs(msWorkspace, msChannel, msActor, "an", 20).
		WillReturnRows(pgxmock.NewRows([]string{"user_id", "display_name"}).AddRow("u-1", "Ana"))

	store := storage.NewPGXMemberStore(mock)
	got, err := store.SearchChannelMemberCandidates(context.Background(), msWorkspace, msChannel, msActor, "an", 20)
	if err != nil {
		t.Fatalf("SearchChannelMemberCandidates: %v", err)
	}
	if len(got) != 1 || got[0].UserID != "u-1" || got[0].DisplayName != "Ana" {
		t.Fatalf("candidates = %+v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXMemberStore_SearchChannelMemberCandidates_PropagatesQueryFailure(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`(?s)FROM chat\.workspace_members wm`).
		WithArgs(msWorkspace, msChannel, msActor, "an", 20).
		WillReturnError(errors.New("connection reset"))

	store := storage.NewPGXMemberStore(mock)
	if _, err := store.SearchChannelMemberCandidates(
		context.Background(), msWorkspace, msChannel, msActor, "an", 20,
	); err == nil {
		t.Fatal("expected the query failure to surface")
	}
}

func TestPGXDMStore_SearchGroupParticipantCandidates_ExcludesActiveParticipantsInSQL(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	// Only status='active' participation excludes: someone who left is offerable
	// again, and the upsert reactivates them.
	mock.ExpectQuery(`(?s)NOT EXISTS.*FROM chat\.dm_members dm.*dm\.conversation_id = \$2::uuid.*dm\.status = 'active'`).
		WithArgs(msWorkspace, msConversation, msActor, "br", 20).
		WillReturnRows(pgxmock.NewRows([]string{"user_id", "display_name"}).AddRow("u-9", "Bruno"))

	store := storage.NewPGXDMStore(mock)
	got, err := store.SearchGroupParticipantCandidates(
		context.Background(), msWorkspace, msConversation, msActor, "br", 20,
	)
	if err != nil {
		t.Fatalf("SearchGroupParticipantCandidates: %v", err)
	}
	if len(got) != 1 || got[0].UserID != "u-9" {
		t.Fatalf("candidates = %+v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXDMStore_SearchGroupParticipantCandidates_PropagatesQueryFailure(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`(?s)FROM chat\.workspace_members wm`).
		WithArgs(msWorkspace, msConversation, msActor, "br", 20).
		WillReturnError(errors.New("connection reset"))

	store := storage.NewPGXDMStore(mock)
	if _, err := store.SearchGroupParticipantCandidates(
		context.Background(), msWorkspace, msConversation, msActor, "br", 20,
	); err == nil {
		t.Fatal("expected the query failure to surface")
	}
}
