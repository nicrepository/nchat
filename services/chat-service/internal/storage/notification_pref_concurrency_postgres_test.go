package storage_test

import (
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// Mute and Unmute racing on one row, against a real PostgreSQL (issue #136).
//
// # What this exists to catch
//
// Unmuting has two outcomes depending on the stored level, and an earlier
// version ran them as two round trips. Between them a concurrent Mute could
// insert, and the pair persisted `all` + muted_at IS NULL — a state the sparse
// representation forbids and a release slot from before #136 reads as a mute.
//
// No amount of single-threaded testing finds that: each statement is correct on
// its own. So this drives the two operations from two different connections,
// released together, and asserts the property that must hold whatever order the
// database picks.
//
// # Why it is not flaky
//
// It asserts an invariant, not an interleaving. Every run is required to end in
// *some* valid serialisation, and the forbidden state is forbidden in all of
// them — so the test cannot fail for having raced one way rather than another.
// Repetition and an alternating start order only raise the chance of visiting
// the window; they are not what makes the assertion meaningful. There are no
// sleeps.
//
// The database holds the same invariant independently, so a regression shows up
// twice here: as a constraint violation returned to one of the callers, and as
// a forbidden final state.

// prefRaceIterations is enough to visit the interleaving repeatedly while
// keeping the suite's runtime in the same order as its neighbours.
const prefRaceIterations = 40

// secondPrefPool is a connection pool of its own, so the two operations under
// test genuinely run on different connections rather than being serialised onto
// one.
func secondPrefPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("CHAT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("CHAT_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("connect second pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// prefState is what the table holds for one (user, channel) afterwards.
type prefState struct {
	Exists bool
	Level  string
	Muted  bool
}

func (s prefState) String() string {
	if !s.Exists {
		return "no row"
	}
	if s.Muted {
		return s.Level + " + muted"
	}
	return s.Level + " + not muted"
}

func readPrefState(t *testing.T, pool *pgxpool.Pool, userID, channelID string) prefState {
	t.Helper()
	rows, err := pool.Query(t.Context(), `
		SELECT notification_level, (muted_at IS NOT NULL)
		FROM chat.conversation_notification_prefs
		WHERE user_id = $1::uuid AND channel_id = $2::uuid`, userID, channelID)
	if err != nil {
		t.Fatalf("read preference state: %v", err)
	}
	defer rows.Close()
	var state prefState
	for rows.Next() {
		if state.Exists {
			t.Fatal("more than one preference row for one (user, channel)")
		}
		state.Exists = true
		if err := rows.Scan(&state.Level, &state.Muted); err != nil {
			t.Fatalf("scan preference state: %v", err)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate preference state: %v", err)
	}
	return state
}

// setPrefState puts the row into exactly one starting state, bypassing the store
// so the case is about what the store does *from* there.
func setPrefState(t *testing.T, pool *pgxpool.Pool, userID, channelID string, state prefState) {
	t.Helper()
	ctx := t.Context()
	if _, err := pool.Exec(ctx, `
		DELETE FROM chat.conversation_notification_prefs
		WHERE user_id = $1::uuid AND channel_id = $2::uuid`, userID, channelID); err != nil {
		t.Fatalf("reset preference row: %v", err)
	}
	if !state.Exists {
		return
	}
	mutedAt := "NULL"
	if state.Muted {
		mutedAt = "now()"
	}
	//nolint:gosec // mutedAt is one of two literals chosen here, never caller input.
	if _, err := pool.Exec(ctx, `
		INSERT INTO chat.conversation_notification_prefs
			(user_id, workspace_id, channel_id, notification_level, muted_at)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, `+mutedAt+`)`,
		userID, notifyWorkspace, channelID, state.Level); err != nil {
		t.Fatalf("seed preference row %s: %v", state, err)
	}
}

// raceMuteAndUnmute runs one Mute and one Unmute concurrently on two
// connections, released by a shared gate, and returns both errors.
//
// `muteFirst` alternates which goroutine is started first. It does not decide
// the outcome — both are blocked on the same closed channel — it only varies
// which one tends to reach the database first, so repeated runs visit the window
// from both sides.
func raceMuteAndUnmute(
	t *testing.T, muteStore, unmuteStore *storage.PGXNotificationPrefStore,
	userID, channelID string, muteFirst bool,
) (muteErr, unmuteErr error) {
	t.Helper()
	gate := make(chan struct{})
	var wait sync.WaitGroup
	mute := func() {
		defer wait.Done()
		<-gate
		muteErr = muteStore.Mute(
			t.Context(), notifyWorkspace, userID, storage.NotificationPrefTargetChannel, channelID)
	}
	unmute := func() {
		defer wait.Done()
		<-gate
		unmuteErr = unmuteStore.Unmute(
			t.Context(), userID, storage.NotificationPrefTargetChannel, channelID)
	}
	wait.Add(2)
	if muteFirst {
		go mute()
		go unmute()
	} else {
		go unmute()
		go mute()
	}
	close(gate)
	wait.Wait()
	return muteErr, unmuteErr
}

// prefRaceCase is one starting state and the set of endings a correct
// implementation may produce from it.
type prefRaceCase struct {
	name    string
	initial prefState
	allowed []prefState
}

func (c prefRaceCase) permits(state prefState) bool {
	for _, allowed := range c.allowed {
		if allowed == state {
			return true
		}
	}
	return false
}

// The three cases the review named, each asserted over the whole set of valid
// serialisations rather than against one expected answer.
func TestPGXNotificationPrefStoreMuteUnmuteRacePostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	other := secondPrefPool(t)
	muteStore := storage.NewPGXNotificationPrefStore(pool)
	unmuteStore := storage.NewPGXNotificationPrefStore(other)
	const level = storage.NotificationLevelMentionsReplies

	for _, test := range []prefRaceCase{{
		// CASE A: nothing stored. Mute last leaves a mute; Unmute last leaves
		// the row absent, which is the default.
		name:    "no row",
		initial: prefState{},
		allowed: []prefState{
			{},
			{Exists: true, Level: storage.NotificationLevelAll, Muted: true},
		},
	}, {
		// CASE B: the default level, silenced. Same two endings: the row is
		// either silenced again or gone.
		name:    "all + muted",
		initial: prefState{Exists: true, Level: storage.NotificationLevelAll, Muted: true},
		allowed: []prefState{
			{},
			{Exists: true, Level: storage.NotificationLevelAll, Muted: true},
		},
	}, {
		// CASE C: a narrowed conversation, silenced. Either ending is fine —
		// but the level must survive both, because neither operation is allowed
		// to touch it.
		name:    "mentions_replies + muted",
		initial: prefState{Exists: true, Level: level, Muted: true},
		allowed: []prefState{
			{Exists: true, Level: level, Muted: true},
			{Exists: true, Level: level},
		},
	}} {
		t.Run(test.name, func(t *testing.T) {
			for iteration := range prefRaceIterations {
				setPrefState(t, pool, notifyAuthor, notifyChannel, test.initial)

				muteErr, unmuteErr := raceMuteAndUnmute(
					t, muteStore, unmuteStore, notifyAuthor, notifyChannel, iteration%2 == 0)

				// A constraint violation surfaces here: the database refuses
				// the forbidden state independently, so a regression fails as
				// an error before it fails as a bad reading.
				if muteErr != nil {
					t.Fatalf("iteration %d: Mute: %v", iteration, muteErr)
				}
				if unmuteErr != nil {
					t.Fatalf("iteration %d: Unmute: %v", iteration, unmuteErr)
				}

				final := readPrefState(t, pool, notifyAuthor, notifyChannel)
				// The forbidden state, named explicitly: it is the one this
				// whole test exists for, and saying so makes a failure
				// self-explanatory.
				if final.Exists && final.Level == storage.NotificationLevelAll && !final.Muted {
					t.Fatalf("iteration %d: persisted the forbidden state 'all + not muted'", iteration)
				}
				if !test.permits(final) {
					t.Fatalf("iteration %d: ended as %s, which is no valid serialisation of %s",
						iteration, final, test.initial)
				}
			}
		})
	}
}

// The level survives the race in every iteration, stated on its own because it
// is the invariant the whole feature is named for: neither operation may write
// the other's column.
func TestPGXNotificationPrefStoreRacePreservesTheLevelPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	other := secondPrefPool(t)
	muteStore := storage.NewPGXNotificationPrefStore(pool)
	unmuteStore := storage.NewPGXNotificationPrefStore(other)
	initial := prefState{Exists: true, Level: storage.NotificationLevelMentionsReplies, Muted: true}

	for iteration := range prefRaceIterations {
		setPrefState(t, pool, notifyAuthor, notifyChannel, initial)

		muteErr, unmuteErr := raceMuteAndUnmute(
			t, muteStore, unmuteStore, notifyAuthor, notifyChannel, iteration%2 == 1)
		if muteErr != nil || unmuteErr != nil {
			t.Fatalf("iteration %d: Mute=%v Unmute=%v", iteration, muteErr, unmuteErr)
		}

		final := readPrefState(t, pool, notifyAuthor, notifyChannel)
		if !final.Exists {
			t.Fatalf("iteration %d: the row vanished, losing a level nobody wrote over", iteration)
		}
		if final.Level != storage.NotificationLevelMentionsReplies {
			t.Fatalf("iteration %d: level = %q, want it preserved", iteration, final.Level)
		}
	}
}

// The database's own refusal, independent of the store.
//
// It is the second defence and it has to be asserted directly: the statement
// above is what makes the forbidden state unreachable through the application,
// and this is what makes it unreachable through a repair script, a psql session
// or a writer somebody adds later.
func TestConversationNotificationPrefsRefuseTheSparseDefaultPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	ctx := t.Context()

	if _, err := pool.Exec(ctx, `
		INSERT INTO chat.conversation_notification_prefs
			(user_id, workspace_id, channel_id, notification_level, muted_at)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'all', NULL)`,
		notifyAuthor, notifyWorkspace, notifyChannel); err != nil {
		// This is the expected path.
		return
	}
	t.Fatal("the database accepted 'all' with a NULL muted_at, which the sparse representation forbids")
}

// ...and the state the feature exists for is *not* refused: the constraint is an
// implication, not a ban on NULL.
func TestConversationNotificationPrefsAllowAnUnsilencedLevelPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	ctx := t.Context()

	if _, err := pool.Exec(ctx, `
		INSERT INTO chat.conversation_notification_prefs
			(user_id, workspace_id, channel_id, notification_level, muted_at)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'mentions_replies', NULL)`,
		notifyAuthor, notifyWorkspace, notifyChannel); err != nil {
		t.Fatalf("the database refused 'mentions_replies' with a NULL muted_at: %v", err)
	}
	// ...and a silenced level, which is the other row the model has to hold.
	if _, err := pool.Exec(ctx, `
		UPDATE chat.conversation_notification_prefs SET muted_at = now()
		WHERE user_id = $1::uuid AND channel_id = $2::uuid`,
		notifyAuthor, notifyChannel); err != nil {
		t.Fatalf("the database refused a silenced level: %v", err)
	}
}
