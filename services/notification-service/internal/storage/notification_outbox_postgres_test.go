package storage_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicrepository/nchat/libs/go/platform/notificationevent"
	"github.com/nicrepository/nchat/services/notification-service/internal/storage"
)

// Issue #742: the claim protocol against a real PostgreSQL.
//
// None of this can be proved with a mock, and that is the reason the file
// exists. What is under test is behaviour the database owns and a fake would
// have to reimplement — and would then be testing itself:
//
//   - FOR UPDATE SKIP LOCKED handing two concurrent workers disjoint rows;
//   - an UPDATE that is simultaneously the claim, so there is no window between
//     choosing a row and owning it;
//   - a lease that protects a claim while it is held and releases it when it
//     expires, which is what makes a crashed worker recoverable;
//   - the transition trigger 000042 installed, which is what makes a terminal
//     state actually terminal.
//
// Opt-in like its chat-service neighbours: needs NOTIFICATION_TEST_DATABASE_URL
// against a _test database carrying the real migrations.

const (
	// The workspace and #geral channel migration 000001 seeds.
	notifyWorkerWorkspace = "00000000-0000-0000-0000-000000000001"
	notifyWorkerChannel   = "00000000-0000-0000-0000-000000000002"

	// A lease long enough that nothing expires by accident during a test.
	notifyWorkerLease = time.Hour
)

// newNotificationTestPool connects to the opt-in database and refuses anything
// that is not obviously disposable.
func newNotificationTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("NOTIFICATION_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("NOTIFICATION_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	var databaseName string
	if err := pool.QueryRow(t.Context(), `SELECT current_database()`).Scan(&databaseName); err != nil {
		t.Fatalf("read current database: %v", err)
	}
	if !strings.HasSuffix(databaseName, "_test") {
		t.Fatalf("refusing to write to non-test database %q", databaseName)
	}
	return pool
}

// outboxFixture is one message and the notification rows addressed to it.
//
// Every row names a different recipient, because the legacy unique constraint
// over (message_id, recipient_user_id, kind) is still in place through the
// expand window and a fixture that ignored it would collide rather than test
// anything.
type outboxFixture struct {
	pool      *pgxpool.Pool
	messageID string
	users     []string
	ids       []string
}

// seedOutbox writes count notification rows in the given state, oldest first.
func seedOutbox(t *testing.T, state notificationevent.State, count int) *outboxFixture {
	t.Helper()
	pool := newNotificationTestPool(t)
	fixture := &outboxFixture{pool: pool}

	// Every identifier is minted by the database, so two runs of the suite — or
	// two tests in it — can never collide on a primary key or on the unique
	// e-mail the users table enforces.
	sender := newFixtureUser(t, pool, "sender")
	fixture.users = append(fixture.users, sender)
	if err := pool.QueryRow(t.Context(), `
		INSERT INTO chat.messages
			(workspace_id, channel_id, sender_id, kind, body_text, body_format, status)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'user', 'worker fixture', 'v2', 'active')
		RETURNING id::text`,
		notifyWorkerWorkspace, notifyWorkerChannel, sender).Scan(&fixture.messageID); err != nil {
		t.Fatalf("seed message: %v", err)
	}

	occurred := time.Now().Add(-time.Duration(count) * time.Minute)
	for i := 0; i < count; i++ {
		recipient := newFixtureUser(t, pool, "recipient")
		fixture.users = append(fixture.users, recipient)

		var id string
		if err := pool.QueryRow(t.Context(), `
			INSERT INTO chat.notification_outbox
				(workspace_id, message_id, recipient_user_id, kind, status,
				 source_type, occurred_at, priority, origin, dedupe_key, suppressed_reason,
				 next_attempt_at)
			VALUES ($1::uuid, $2::uuid, $3::uuid, 'mention', $4::text,
			        'message', $5, 'high', 'live', 'message:' || $2::text || ':mention',
			        NULLIF($6::text, ''),
			        -- Claimable states carry an availability instant; pending and
			        -- the terminal states do not. Aligned with occurred_at here,
			        -- which is what a promptly evaluated event looks like.
			        CASE WHEN $4::text IN ('eligible', 'retrying', 'processing')
			             THEN $5::timestamptz END)
			RETURNING id::text`,
			notifyWorkerWorkspace, fixture.messageID, recipient, string(state),
			occurred.Add(time.Duration(i)*time.Minute), seedSuppressedReason(state)).Scan(&id); err != nil {
			t.Fatalf("seed notification %d: %v", i, err)
		}
		fixture.ids = append(fixture.ids, id)
	}

	t.Cleanup(func() {
		ctx := context.Background()
		// The outbox rows cascade from both the message and the users; removing
		// the message is enough, and the users go with their own rows.
		_, _ = pool.Exec(ctx, `DELETE FROM chat.messages WHERE id = $1::uuid`, fixture.messageID)
		_, _ = pool.Exec(ctx, `DELETE FROM auth.users WHERE id = ANY($1::uuid[])`, fixture.users)
	})
	return fixture
}

// seedSuppressedReason satisfies the contract 000042 enforces: exactly the
// suppressed state carries a reason, and the database refuses either half
// without the other.
func seedSuppressedReason(state notificationevent.State) string {
	if state == notificationevent.StateSuppressed {
		return "seeded_for_test"
	}
	return ""
}

// newFixtureUser inserts a user and returns the id the database assigned.
func newFixtureUser(t *testing.T, pool *pgxpool.Pool, role string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(t.Context(), `
		INSERT INTO auth.users (email, display_name)
		VALUES ('outbox-742-' || gen_random_uuid()::text || '@e.test', $1)
		RETURNING id::text`, "Outbox "+role).Scan(&id); err != nil {
		t.Fatalf("seed %s: %v", role, err)
	}
	return id
}

func execFixture(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), sql, args...); err != nil {
		t.Fatalf("seed fixture: %v", err)
	}
}

func (f *outboxFixture) store() *storage.PGXNotificationOutboxStore {
	return storage.NewPGXNotificationOutboxStore(f.pool, false)
}

// state reads one row's current state straight from the table.
func (f *outboxFixture) state(t *testing.T, id string) string {
	t.Helper()
	var status string
	if err := f.pool.QueryRow(t.Context(),
		`SELECT status FROM chat.notification_outbox WHERE id = $1::uuid`, id).Scan(&status); err != nil {
		t.Fatalf("read state: %v", err)
	}
	return status
}

// assertState fails unless the row is in the expected state, naming what the
// caller was proving.
func (f *outboxFixture) assertState(
	t *testing.T, id string, want notificationevent.State, why string,
) {
	t.Helper()
	if got := f.state(t, id); got != string(want) {
		t.Fatalf("state = %q, want %q (%s)", got, want, why)
	}
}

// assertAvailability fails unless the row's availability instant is present (or
// absent) as expected.
func (f *outboxFixture) assertAvailability(t *testing.T, id string, want bool, why string) {
	t.Helper()
	var present bool
	if err := f.pool.QueryRow(t.Context(),
		`SELECT next_attempt_at IS NOT NULL FROM chat.notification_outbox WHERE id = $1::uuid`,
		id).Scan(&present); err != nil {
		t.Fatalf("read availability: %v", err)
	}
	if present != want {
		t.Fatalf("availability present = %v, want %v (%s)", present, want, why)
	}
}

func (f *outboxFixture) attempts(t *testing.T, id string) int {
	t.Helper()
	var attempts int
	if err := f.pool.QueryRow(t.Context(),
		`SELECT attempts FROM chat.notification_outbox WHERE id = $1::uuid`, id).Scan(&attempts); err != nil {
		t.Fatalf("read attempts: %v", err)
	}
	return attempts
}

// becomeDueRetry puts the event into the retrying state with its next attempt
// dueAgo in the past (a negative value places it in the future).
//
// Written straight into the table rather than reached by waiting: the whole
// point of the fairness test is a schedule that has already elapsed, and
// sleeping for it would make the suite slow and flaky for no extra proof.
func (f *outboxFixture) becomeDueRetry(t *testing.T, id string, dueAgo time.Duration) {
	t.Helper()
	// eligible -> processing -> retrying is the only route the trigger allows,
	// so the row reaches the state the same way a real failure would.
	execFixture(t, f.pool, `
		UPDATE chat.notification_outbox
		SET status = 'processing', attempts = 1, next_attempt_at = now()
		WHERE id = $1::uuid`, id)
	execFixture(t, f.pool, `
		UPDATE chat.notification_outbox
		SET status = 'retrying',
		    next_attempt_at = now() - ($2 * interval '1 second'),
		    last_error = 'delivery_transient'
		WHERE id = $1::uuid`, id, dueAgo.Seconds())
}

// addPendingOccurredAt seeds count pending events whose domain event happened
// occurredAgo in the past, and returns their ids.
//
// Pending, not eligible: they become available only when a policy promotes them,
// which is the whole point of the tests that use this.
func (f *outboxFixture) addPendingOccurredAt(
	t *testing.T, count int, occurredAgo time.Duration,
) []string {
	t.Helper()
	ids := make([]string, 0, count)
	for i := 0; i < count; i++ {
		recipient := newFixtureUser(t, f.pool, "recipient")
		f.users = append(f.users, recipient)

		var id string
		if err := f.pool.QueryRow(t.Context(), `
			INSERT INTO chat.notification_outbox
				(workspace_id, message_id, recipient_user_id, kind, status,
				 source_type, occurred_at, priority, origin, dedupe_key)
			VALUES ($1::uuid, $2::uuid, $3::uuid, 'mention', 'pending',
			        'message', now() - ($4 * interval '1 second'), 'high', 'live',
			        'message:' || $2::text || ':mention')
			RETURNING id::text`,
			notifyWorkerWorkspace, f.messageID, recipient,
			occurredAgo.Seconds()).Scan(&id); err != nil {
			t.Fatalf("seed pending event: %v", err)
		}
		ids = append(ids, id)
	}
	return ids
}

// promote runs the real evaluation path over the given events, which is what
// stamps their availability instant.
func (f *outboxFixture) promote(t *testing.T, ids []string) {
	t.Helper()
	for _, id := range ids {
		if err := f.store().MarkEvaluated(
			t.Context(), id, notificationevent.StateEligible, ""); err != nil {
			t.Fatalf("promote %s: %v", id, err)
		}
	}
}

// expireLease moves a claim's deadline into the past, which is what a worker
// that died leaves behind.
func (f *outboxFixture) expireLease(t *testing.T, id string) {
	t.Helper()
	execFixture(t, f.pool, `
		UPDATE chat.notification_outbox
		SET next_attempt_at = now() - interval '1 minute'
		WHERE id = $1::uuid`, id)
}

// claimConcurrently starts workers claiming from one store at the same instant
// and returns what each of them got.
//
// The barrier is what makes it a race rather than a sequence: without it the
// first worker would usually commit before the second even issued its statement,
// and SKIP LOCKED would never be exercised. Shared by both exclusivity tests so
// neither has to restate the goroutine plumbing.
func claimConcurrently(
	t *testing.T, store *storage.PGXNotificationOutboxStore, workers, batchSize int,
) [][]storage.NotificationEvent {
	t.Helper()

	var start, done sync.WaitGroup
	start.Add(1)
	claims := make([][]storage.NotificationEvent, workers)
	for i := range claims {
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait()
			claimed, err := store.ClaimDue(context.Background(), batchSize, 5, notifyWorkerLease)
			if err != nil {
				t.Errorf("worker %d claim: %v", i, err)
				return
			}
			claims[i] = claimed
		}()
	}
	start.Done()
	done.Wait()
	return claims
}

// countClaimsPerEvent collapses the per-worker batches into how many workers
// took each event. Anything other than one is a broken claim.
func countClaimsPerEvent(claims [][]storage.NotificationEvent) map[string]int {
	seen := map[string]int{}
	for _, batch := range claims {
		for _, event := range batch {
			seen[event.ID]++
		}
	}
	return seen
}

// assertNotClaimable proves a seeded event in the given state is never handed to
// a delivery adapter.
func assertNotClaimable(t *testing.T, state notificationevent.State) {
	t.Helper()
	fixture := seedOutbox(t, state, 1)

	claimed, err := fixture.store().ClaimDue(context.Background(), 10, 5, notifyWorkerLease)
	if err != nil {
		t.Fatalf("ClaimDue: %v", err)
	}
	if _, taken := countClaimsPerEvent([][]storage.NotificationEvent{claimed})[fixture.ids[0]]; taken {
		t.Fatalf("an event in state %q was claimed for delivery", state)
	}
}

// ---------------------------------------------------------------------------
// Exclusivity
// ---------------------------------------------------------------------------

// The property the whole design rests on: two workers racing for one event, and
// only one of them getting it. FOR UPDATE SKIP LOCKED is what makes it true, and
// only a real database can demonstrate it.
func TestNotificationClaimIsExclusivePostgreSQL(t *testing.T) {
	fixture := seedOutbox(t, notificationevent.StateEligible, 1)
	store := fixture.store()

	claims := claimConcurrently(t, store, 2, 10)

	total := len(claims[0]) + len(claims[1])
	if total != 1 {
		t.Fatalf("the event was claimed %d times, want exactly once", total)
	}
	if got := fixture.attempts(t, fixture.ids[0]); got != 1 {
		t.Fatalf("attempts = %d after one claim, want 1", got)
	}
}

// With work for both, neither may see the other's rows.
func TestNotificationClaimDistributesDisjointWorkPostgreSQL(t *testing.T) {
	const events = 20
	fixture := seedOutbox(t, notificationevent.StateEligible, events)
	store := fixture.store()

	seen := countClaimsPerEvent(claimConcurrently(t, store, 4, events/2))

	for id, count := range seen {
		if count != 1 {
			t.Fatalf("event %s was claimed by %d workers at once", id, count)
		}
	}
	if len(seen) == 0 {
		t.Fatal("no work was claimed at all")
	}
}

func TestNotificationClaimRespectsTheBatchSizePostgreSQL(t *testing.T) {
	fixture := seedOutbox(t, notificationevent.StateEligible, 9)
	store := fixture.store()

	claimed, err := store.ClaimDue(context.Background(), 4, 5, notifyWorkerLease)
	if err != nil {
		t.Fatalf("ClaimDue: %v", err)
	}
	if len(claimed) != 4 {
		t.Fatalf("claimed %d events, want the batch size of 4", len(claimed))
	}
}

// Oldest occurrence first, with the id as a total tie-break, so an imported
// batch cannot overtake live events and two replicas walk the queue the same way.
func TestNotificationClaimOrderIsDeterministicPostgreSQL(t *testing.T) {
	fixture := seedOutbox(t, notificationevent.StateEligible, 5)
	store := fixture.store()

	claimed, err := store.ClaimDue(context.Background(), 3, 5, notifyWorkerLease)
	if err != nil {
		t.Fatalf("ClaimDue: %v", err)
	}
	if len(claimed) != 3 {
		t.Fatalf("claimed %d events, want 3", len(claimed))
	}
	for i := 1; i < len(claimed); i++ {
		if claimed[i].OccurredAt.Before(claimed[i-1].OccurredAt) {
			t.Fatalf("event %d occurred before its predecessor; the queue is not ordered", i)
		}
	}
}

// ---------------------------------------------------------------------------
// The lease
// ---------------------------------------------------------------------------

// A claim inside its lease belongs to the worker holding it, and no amount of
// polling by anybody else may take it.
func TestNotificationLeaseProtectsAClaimPostgreSQL(t *testing.T) {
	fixture := seedOutbox(t, notificationevent.StateEligible, 1)
	store := fixture.store()

	if claimed, err := store.ClaimDue(context.Background(), 10, 5, notifyWorkerLease); err != nil || len(claimed) != 1 {
		t.Fatalf("first claim = (%d events, %v), want one event", len(claimed), err)
	}
	for i := 0; i < 3; i++ {
		claimed, err := store.ClaimDue(context.Background(), 10, 5, notifyWorkerLease)
		if err != nil {
			t.Fatalf("second claim: %v", err)
		}
		if len(claimed) != 0 {
			t.Fatalf("a leased event was handed out again after %d retries", i)
		}
	}
	if got := fixture.state(t, fixture.ids[0]); got != string(notificationevent.StateProcessing) {
		t.Fatalf("state = %q, want the row still claimed", got)
	}
}

// A worker that died leaves a claim nobody will finalise. Once its lease has
// expired the event has to become claimable again, or a crash would lose it.
func TestNotificationExpiredLeaseIsRecoveredPostgreSQL(t *testing.T) {
	fixture := seedOutbox(t, notificationevent.StateEligible, 1)
	store := fixture.store()

	if _, err := store.ClaimDue(context.Background(), 10, 5, notifyWorkerLease); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	fixture.expireLease(t, fixture.ids[0])

	claimed, err := store.ClaimDue(context.Background(), 10, 5, notifyWorkerLease)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if len(claimed) != 1 || claimed[0].ID != fixture.ids[0] {
		t.Fatalf("reclaimed %+v, want the abandoned event", claimed)
	}
	// Attempts are counted at claim time, which is what bounds an event that
	// kills its worker every time.
	if got := fixture.attempts(t, fixture.ids[0]); got != 2 {
		t.Fatalf("attempts = %d after two claims, want 2", got)
	}
}

// The backlog is durable, so a restart resumes it rather than losing it. A new
// pool is a new process as far as the table is concerned.
func TestNotificationBacklogSurvivesAWorkerRestartPostgreSQL(t *testing.T) {
	fixture := seedOutbox(t, notificationevent.StateEligible, 3)

	if _, err := fixture.store().ClaimDue(context.Background(), 1, 5, notifyWorkerLease); err != nil {
		t.Fatalf("claim before the restart: %v", err)
	}
	fixture.expireLease(t, fixture.ids[0])

	restarted := storage.NewPGXNotificationOutboxStore(newNotificationTestPool(t), false)
	claimed, err := restarted.ClaimDue(context.Background(), 10, 5, notifyWorkerLease)
	if err != nil {
		t.Fatalf("claim after the restart: %v", err)
	}
	if len(claimed) < 3 {
		t.Fatalf("claimed %d events after the restart, want the whole backlog of 3", len(claimed))
	}
}

// ---------------------------------------------------------------------------
// What may not be claimed
// ---------------------------------------------------------------------------

// A suppressed event is a decision, not a queue entry. It must never be handed
// to a delivery adapter — nor must any other terminal state.
func TestNotificationClaimSkipsSuppressedAndTerminalEventsPostgreSQL(t *testing.T) {
	for _, state := range []notificationevent.State{
		notificationevent.StatePending,
		notificationevent.StateSuppressed,
		notificationevent.StateSent,
		notificationevent.StateFailed,
	} {
		t.Run(string(state), func(t *testing.T) { assertNotClaimable(t, state) })
	}
}

// An event whose attempts are spent is work that can no longer succeed. The
// claim must leave it alone, or the ceiling would mean nothing.
func TestNotificationClaimStopsAtTheAttemptCeilingPostgreSQL(t *testing.T) {
	fixture := seedOutbox(t, notificationevent.StateEligible, 1)
	store := fixture.store()

	const ceiling = 2
	for i := 0; i < ceiling; i++ {
		if _, err := store.ClaimDue(context.Background(), 10, ceiling, notifyWorkerLease); err != nil {
			t.Fatalf("claim %d: %v", i, err)
		}
		fixture.expireLease(t, fixture.ids[0])
	}

	claimed, err := store.ClaimDue(context.Background(), 10, ceiling, notifyWorkerLease)
	if err != nil {
		t.Fatalf("claim past the ceiling: %v", err)
	}
	for _, event := range claimed {
		if event.ID == fixture.ids[0] {
			t.Fatal("an event past its attempt ceiling was claimed again")
		}
	}
}

// The bound on a poison event: an abandoned claim whose attempts are spent
// becomes terminal instead of being reclaimed forever.
func TestNotificationExhaustedClaimsAreRetiredPostgreSQL(t *testing.T) {
	fixture := seedOutbox(t, notificationevent.StateEligible, 1)
	store := fixture.store()

	const ceiling = 2
	for i := 0; i < ceiling; i++ {
		if _, err := store.ClaimDue(context.Background(), 10, ceiling, notifyWorkerLease); err != nil {
			t.Fatalf("claim %d: %v", i, err)
		}
		fixture.expireLease(t, fixture.ids[0])
	}

	if _, err := store.FailExhausted(context.Background(), ceiling); err != nil {
		t.Fatalf("FailExhausted: %v", err)
	}
	if got := fixture.state(t, fixture.ids[0]); got != string(notificationevent.StateFailed) {
		t.Fatalf("state = %q, want the exhausted claim retired as failed", got)
	}
}

// A claim still inside its lease is work in flight. The reaper must not overtake
// it, or a slow delivery would be retired while it was still running.
func TestNotificationExhaustedReaperSparesALiveClaimPostgreSQL(t *testing.T) {
	fixture := seedOutbox(t, notificationevent.StateEligible, 1)
	store := fixture.store()

	if _, err := store.ClaimDue(context.Background(), 10, 1, notifyWorkerLease); err != nil {
		t.Fatalf("ClaimDue: %v", err)
	}

	retired, err := store.FailExhausted(context.Background(), 1)
	if err != nil {
		t.Fatalf("FailExhausted: %v", err)
	}
	if retired != 0 {
		t.Fatalf("%d live claims were retired", retired)
	}
	if got := fixture.state(t, fixture.ids[0]); got != string(notificationevent.StateProcessing) {
		t.Fatalf("state = %q, want the live claim untouched", got)
	}
}

// ---------------------------------------------------------------------------
// Outcomes
// ---------------------------------------------------------------------------

// The whole walk, against the trigger 000042 installed: a claimed event reaches
// each of its three endings, and only from a claim.
func TestNotificationOutcomesAreCompareAndSetPostgreSQL(t *testing.T) {
	fixture := seedOutbox(t, notificationevent.StateEligible, 3)
	store := fixture.store()

	if _, err := store.ClaimDue(context.Background(), 3, 5, notifyWorkerLease); err != nil {
		t.Fatalf("ClaimDue: %v", err)
	}

	// Every finalisation names the generation the claim returned, which is 1
	// here because each row has been claimed exactly once.
	if err := store.MarkDelivered(context.Background(), fixture.ids[0], 1); err != nil {
		t.Fatalf("MarkDelivered: %v", err)
	}
	if err := store.ScheduleRetry(context.Background(), fixture.ids[1], 1,
		time.Minute, "delivery_transient"); err != nil {
		t.Fatalf("ScheduleRetry: %v", err)
	}
	if err := store.MarkFailed(context.Background(), fixture.ids[2], 1, "delivery_permanent"); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}

	for id, want := range map[string]notificationevent.State{
		fixture.ids[0]: notificationevent.StateSent,
		fixture.ids[1]: notificationevent.StateRetrying,
		fixture.ids[2]: notificationevent.StateFailed,
	} {
		if got := fixture.state(t, id); got != string(want) {
			t.Fatalf("state = %q, want %q", got, want)
		}
	}

	// A second finalisation of an event that already ended must not be applied.
	// This is what a worker whose lease expired mid-delivery runs into, and it
	// has to be a conflict rather than a rewrite of a terminal state.
	if err := store.MarkDelivered(context.Background(), fixture.ids[0], 1); err == nil {
		t.Fatal("a terminal event was finalised twice")
	}
}

// A retried event is due again at the time the worker persisted, and not before.
func TestNotificationRetryScheduleIsPersistedPostgreSQL(t *testing.T) {
	fixture := seedOutbox(t, notificationevent.StateEligible, 1)
	store := fixture.store()

	if _, err := store.ClaimDue(context.Background(), 1, 5, notifyWorkerLease); err != nil {
		t.Fatalf("ClaimDue: %v", err)
	}
	if err := store.ScheduleRetry(context.Background(), fixture.ids[0], 1,
		time.Hour, "delivery_timeout"); err != nil {
		t.Fatalf("ScheduleRetry: %v", err)
	}

	claimed, err := store.ClaimDue(context.Background(), 10, 5, notifyWorkerLease)
	if err != nil {
		t.Fatalf("ClaimDue after the retry: %v", err)
	}
	for _, event := range claimed {
		if event.ID == fixture.ids[0] {
			t.Fatal("a retried event was claimed before its schedule was due")
		}
	}

	var lastError string
	if err := fixture.pool.QueryRow(t.Context(),
		`SELECT last_error FROM chat.notification_outbox WHERE id = $1::uuid`,
		fixture.ids[0]).Scan(&lastError); err != nil {
		t.Fatalf("read last_error: %v", err)
	}
	if lastError != "delivery_timeout" {
		t.Fatalf("last_error = %q, want the recorded category", lastError)
	}
}

// last_error is bounded by its type, and the bound is a security control: it is
// the one column in a table designed to carry no content where a provider's
// error body — recipient addresses, endpoints, token fragments — could be
// parked. The database refuses, not merely the worker.
func TestNotificationLastErrorCannotHoldAPayloadPostgreSQL(t *testing.T) {
	fixture := seedOutbox(t, notificationevent.StateEligible, 1)
	store := fixture.store()

	if _, err := store.ClaimDue(context.Background(), 1, 5, notifyWorkerLease); err != nil {
		t.Fatalf("ClaimDue: %v", err)
	}

	err := store.MarkFailed(context.Background(), fixture.ids[0], 1, strings.Repeat("x", 65))
	if err == nil {
		t.Fatal("a 65-character error was accepted into last_error")
	}
}

// The claim never returns a message body, an address or a subscription: the row
// carries none, and the projection must not start reading them from elsewhere.
func TestNotificationClaimReturnsReferencesOnlyPostgreSQL(t *testing.T) {
	fixture := seedOutbox(t, notificationevent.StateEligible, 1)

	claimed, err := fixture.store().ClaimDue(context.Background(), 1, 5, notifyWorkerLease)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("ClaimDue = (%d events, %v), want one", len(claimed), err)
	}
	event := claimed[0]
	if event.WorkspaceID != notifyWorkerWorkspace || event.SourceID != fixture.messageID {
		t.Fatalf("event = %+v, want the row's own references", event)
	}
	if strings.Contains(fmt.Sprintf("%+v", event), "worker fixture") {
		t.Fatal("the message body reached the worker through the outbox")
	}
}

func TestNotificationBacklogCountsOpenWorkPostgreSQL(t *testing.T) {
	fixture := seedOutbox(t, notificationevent.StateEligible, 4)
	store := fixture.store()

	before, err := store.Backlog(context.Background())
	if err != nil {
		t.Fatalf("Backlog: %v", err)
	}

	// Claiming takes work out of the backlog: it is in flight, not waiting.
	if _, err := store.ClaimDue(context.Background(), 4, 5, notifyWorkerLease); err != nil {
		t.Fatalf("ClaimDue: %v", err)
	}
	after, err := store.Backlog(context.Background())
	if err != nil {
		t.Fatalf("Backlog: %v", err)
	}
	if after != before-4 {
		t.Fatalf("backlog went from %d to %d after claiming 4", before, after)
	}
}

// The evaluation step, against the trigger: pending leaves as eligible or as
// suppressed, and the compare-and-set means only one worker's decision lands.
func TestNotificationEvaluationIsExclusivePostgreSQL(t *testing.T) {
	fixture := seedOutbox(t, notificationevent.StatePending, 1)
	store := fixture.store()

	if err := store.MarkEvaluated(context.Background(), fixture.ids[0],
		notificationevent.StateEligible, ""); err != nil {
		t.Fatalf("MarkEvaluated: %v", err)
	}
	// A second decision about the same event finds it already made.
	err := store.MarkEvaluated(context.Background(), fixture.ids[0],
		notificationevent.StateSuppressed, "quiet_hours")
	if err == nil {
		t.Fatal("an event was evaluated twice")
	}
	if got := fixture.state(t, fixture.ids[0]); got != string(notificationevent.StateEligible) {
		t.Fatalf("state = %q, want the first decision to have stood", got)
	}
}

// ---------------------------------------------------------------------------
// Stale claim finalisation
// ---------------------------------------------------------------------------

// staleFinalisation is one of the three ways a claim can end, applied by a
// worker naming the generation it believes it holds.
type staleFinalisation struct {
	name     string
	finalise func(*storage.PGXNotificationOutboxStore, string, int) error
	// terminal is the state the row would reach if the stale write were wrongly
	// applied. Asserting the row is NOT in it is how the test proves the stale
	// worker changed nothing.
	terminal notificationevent.State
}

func staleFinalisations() []staleFinalisation {
	return []staleFinalisation{
		{
			name:     "delivered",
			terminal: notificationevent.StateSent,
			finalise: func(s *storage.PGXNotificationOutboxStore, id string, attempt int) error {
				return s.MarkDelivered(context.Background(), id, attempt)
			},
		},
		{
			name:     "retry",
			terminal: notificationevent.StateRetrying,
			finalise: func(s *storage.PGXNotificationOutboxStore, id string, attempt int) error {
				return s.ScheduleRetry(context.Background(), id, attempt, time.Hour, "delivery_transient")
			},
		},
		{
			name:     "failed",
			terminal: notificationevent.StateFailed,
			finalise: func(s *storage.PGXNotificationOutboxStore, id string, attempt int) error {
				return s.MarkFailed(context.Background(), id, attempt, "delivery_permanent")
			},
		},
	}
}

// The race the claim protocol has to survive, against a real database.
//
// Worker A claims (attempts = 1) and starts delivering. Its lease expires.
// Worker B reclaims the same row (attempts = 2) — the status is 'processing' in
// both cases, so status alone cannot tell the two claims apart. Worker A then
// finishes, late, and tries to record its outcome.
//
// A's write must not land. Before the generation predicate existed it did: the
// compare-and-set matched on status only, found B's row, and finalised a claim
// that was still in flight.
func TestNotificationStaleClaimCannotFinalisePostgreSQL(t *testing.T) {
	for _, ending := range staleFinalisations() {
		t.Run(ending.name, func(t *testing.T) { assertStaleClaimIsRefused(t, ending) })
	}
}

// assertStaleClaimIsRefused drives the whole race for one kind of ending.
//
// A top-level function rather than the body of the subtest closure: the same
// steps nested inside a loop and a closure read as one deeply indented block,
// and the assertions are what this file is for.
func assertStaleClaimIsRefused(t *testing.T, ending staleFinalisation) {
	t.Helper()
	fixture := seedOutbox(t, notificationevent.StateEligible, 1)
	store := fixture.store()
	id := fixture.ids[0]

	// A claims, its lease lapses, and B reclaims the same row.
	attemptA := claimGeneration(t, store, id)
	fixture.expireLease(t, id)
	attemptB := claimGeneration(t, store, id)
	if attemptA != 1 || attemptB != 2 {
		t.Fatalf("generations were %d then %d, want 1 then 2", attemptA, attemptB)
	}
	// Both claims look identical by status; only the generation tells them apart.
	fixture.assertState(t, id, notificationevent.StateProcessing, "the row is still claimed by B")

	// A finishes late, holding a generation that has been superseded.
	err := ending.finalise(store, id, attemptA)
	if !errors.Is(err, storage.ErrNotificationStateConflict) {
		t.Fatalf("stale finalisation returned %v, want a lost-claim conflict", err)
	}
	fixture.assertState(t, id, notificationevent.StateProcessing, "B's claim is untouched")
	if got := fixture.attempts(t, id); got != attemptB {
		t.Fatalf("attempts = %d, want B's generation %d intact", got, attemptB)
	}

	// B, which actually owns the row, still finalises normally.
	if err := ending.finalise(store, id, attemptB); err != nil {
		t.Fatalf("the owning claim could not finalise: %v", err)
	}
	fixture.assertState(t, id, ending.terminal, "the owning claim reached its ending")
}

// claimGeneration claims the named event and returns the attempts value the
// claim assigned it — the identity every finalisation has to present.
func claimGeneration(t *testing.T, store *storage.PGXNotificationOutboxStore, id string) int {
	t.Helper()
	claimed, err := store.ClaimDue(context.Background(), 10, 5, notifyWorkerLease)
	if err != nil {
		t.Fatalf("ClaimDue: %v", err)
	}
	for _, event := range claimed {
		if event.ID == id {
			return event.Attempts
		}
	}
	t.Fatalf("the event was not claimed")
	return 0
}

// ---------------------------------------------------------------------------
// Fairness: a due retry must not starve behind new work
// ---------------------------------------------------------------------------

// The exact case the second review found.
//
// A retry has been waiting, due since a minute ago. Then a batch of *historical*
// events — messages from weeks back that had been sitting pending — is evaluated
// and becomes eligible right now.
//
// Those events are far older by occurred_at and far younger by availability, and
// the queue must order them by the second. Before this fix it ordered by the
// first: MarkEvaluated left next_attempt_at NULL, the claim fell back to
// occurred_at, and a three-week-old message promoted a moment ago overtook a
// retry that had genuinely been waiting.
func TestNotificationDueRetryOutranksFreshlyEvaluatedHistoryPostgreSQL(t *testing.T) {
	fixture := seedOutbox(t, notificationevent.StateEligible, 1)
	store := fixture.store()
	retryID := fixture.ids[0]

	// R is due, and has been waiting a minute.
	fixture.becomeDueRetry(t, retryID, time.Minute)

	// Only now are the historical events evaluated. Their occurred_at is three
	// weeks old; their availability is this instant.
	history := fixture.addPendingOccurredAt(t, 3, 21*24*time.Hour)
	fixture.promote(t, history)

	claimed, err := store.ClaimDue(context.Background(), 1, 5, notifyWorkerLease)
	if err != nil {
		t.Fatalf("ClaimDue: %v", err)
	}
	if len(claimed) != 1 {
		t.Fatalf("claimed %d events, want the single-event batch", len(claimed))
	}
	if claimed[0].ID != retryID {
		t.Fatalf("the batch went to a freshly evaluated historical event; " +
			"the due retry was overtaken on occurred_at rather than availability")
	}
}

// Promotion has to record availability, or the row is not claimable at all.
// This is the invariant the ordering depends on, asserted directly.
func TestNotificationEvaluationStampsAvailabilityPostgreSQL(t *testing.T) {
	fixture := seedOutbox(t, notificationevent.StatePending, 1)
	id := fixture.ids[0]

	fixture.assertAvailability(t, id, false, "a pending event is not yet available")

	fixture.promote(t, []string{id})
	fixture.assertAvailability(t, id, true, "promotion records when it became available")

	claimed, err := fixture.store().ClaimDue(context.Background(), 10, 5, notifyWorkerLease)
	if err != nil {
		t.Fatalf("ClaimDue: %v", err)
	}
	if !containsEvent(claimed, id) {
		t.Fatal("a promoted event was not claimable")
	}
}

// Suppression is terminal, so it must not become available.
func TestNotificationSuppressionStampsNoAvailabilityPostgreSQL(t *testing.T) {
	fixture := seedOutbox(t, notificationevent.StatePending, 1)
	id := fixture.ids[0]

	if err := fixture.store().MarkEvaluated(
		t.Context(), id, notificationevent.StateSuppressed, "quiet_hours"); err != nil {
		t.Fatalf("MarkEvaluated: %v", err)
	}

	fixture.assertAvailability(t, id, false, "a suppressed event never becomes available")
}

// Continuous starvation: historical pending events keep arriving and being
// evaluated, poll after poll, and the due retry still has to get through.
//
// One event per batch, so every poll is a direct contest between the waiting
// retry and whatever was just promoted.
func TestNotificationDueRetryIsNotStarvedByNewWorkPostgreSQL(t *testing.T) {
	fixture := seedOutbox(t, notificationevent.StateEligible, 1)
	store := fixture.store()
	retryID := fixture.ids[0]

	fixture.becomeDueRetry(t, retryID, time.Minute)

	for poll := 0; poll < 4; poll++ {
		// Each poll is preceded by more history being evaluated — the arrival
		// pattern that could hold the retry back for ever.
		fixture.promote(t, fixture.addPendingOccurredAt(t, 2, 21*24*time.Hour))

		claimed, err := store.ClaimDue(context.Background(), 1, 5, notifyWorkerLease)
		if err != nil {
			t.Fatalf("poll %d: %v", poll, err)
		}
		if containsEvent(claimed, retryID) {
			return
		}
	}

	t.Fatal("the due retry was never claimed across 4 polls of freshly evaluated " +
		"historical events; it is starving behind work that became available after it")
}

// A retry whose backoff has NOT elapsed still must not be claimed: fairness
// changed the ordering, and must not have widened eligibility.
func TestNotificationFutureRetryStaysUnclaimedPostgreSQL(t *testing.T) {
	fixture := seedOutbox(t, notificationevent.StateEligible, 1)
	store := fixture.store()
	retryID := fixture.ids[0]

	// Due an hour from now, so its availability instant is in the future.
	fixture.becomeDueRetry(t, retryID, -time.Hour)

	claimed, err := store.ClaimDue(context.Background(), 10, 5, notifyWorkerLease)
	if err != nil {
		t.Fatalf("ClaimDue: %v", err)
	}
	if containsEvent(claimed, retryID) {
		t.Fatal("a retry was claimed before its backoff had elapsed")
	}
}

func containsEvent(events []storage.NotificationEvent, id string) bool {
	for _, event := range events {
		if event.ID == id {
			return true
		}
	}
	return false
}

// TestMuteResolutionPostgreSQL proves the mute projection against the real
// schema (issue #744).
//
// None of this is provable with a mock. What is under test is the meaning of the
// join — which preference row reaches which outbox row — and that meaning comes
// entirely from the two tables' keys and the XOR invariant on each. A fake would
// have to reimplement all of it and would then be testing itself.
//
// The three isolations proved here are the ones a leak would look like:
// somebody else's mute, another tenant's mute, and a mute that was never
// expressed at all.
func TestMuteResolutionPostgreSQL(t *testing.T) {
	fixture := seedOutbox(t, notificationevent.StatePending, 2)
	muter, bystander := fixture.users[1], fixture.users[2]

	assertNoPreferenceIsNotMuted(t, fixture, muter, bystander)
	t.Run("one member's mute does not reach another", func(t *testing.T) {
		assertMuteIsPerRecipient(t, fixture, muter, bystander)
	})
	t.Run("a preference from another tenant does not reach this event", func(t *testing.T) {
		assertMuteDoesNotCrossTenants(t, fixture, muter, bystander)
	})
	t.Run("unmuting restores the default", func(t *testing.T) {
		assertUnmuteRestoresTheDefault(t, fixture, muter)
	})
}

// No preference row anywhere: the product default, which the table expresses as
// the absence it is. Nothing may invent a mute here.
func assertNoPreferenceIsNotMuted(t *testing.T, f *outboxFixture, muter, bystander string) {
	t.Helper()
	muted := f.mutedByRecipient(t)
	if muted[muter] || muted[bystander] {
		t.Fatalf("a recipient with no preference row read as muted: %+v", muted)
	}
}

// The table is keyed by user, and this is the assertion that the projection
// honours that key: the other member of the very same conversation is untouched.
func assertMuteIsPerRecipient(t *testing.T, f *outboxFixture, muter, bystander string) {
	t.Helper()
	f.muteChannel(t, muter, notifyWorkerWorkspace)

	muted := f.mutedByRecipient(t)
	if !muted[muter] {
		t.Fatal("the recipient who muted the conversation did not read as muted")
	}
	if muted[bystander] {
		t.Fatal("one member's mute silenced another member of the same conversation")
	}
}

// conversation_notification_prefs references workspaces and the target through
// separate foreign keys, so a row naming a workspace that does not own the
// channel is insertable — the projection's workspace predicate is the only thing
// that stops it being usable.
func assertMuteDoesNotCrossTenants(t *testing.T, f *outboxFixture, muter, bystander string) {
	t.Helper()
	f.muteChannel(t, bystander, newIsolatedWorkspace(t, f.pool))

	muted := f.mutedByRecipient(t)
	if muted[bystander] {
		t.Fatal("a preference row from another workspace reached this tenant's event")
	}
	if !muted[muter] {
		t.Fatal("the in-tenant mute stopped resolving")
	}
}

// Unmuting deletes the row rather than writing false, so the absence has to read
// as not muted again.
func assertUnmuteRestoresTheDefault(t *testing.T, f *outboxFixture, muter string) {
	t.Helper()
	execFixture(t, f.pool, `
		DELETE FROM chat.conversation_notification_prefs
		WHERE user_id = $1::uuid AND channel_id = $2::uuid`,
		muter, notifyWorkerChannel)
	if muted := f.mutedByRecipient(t); muted[muter] {
		t.Fatal("a deleted preference row still read as muted")
	}
}

// mutedByRecipient reads what the projection resolved for every pending row.
func (f *outboxFixture) mutedByRecipient(t *testing.T) map[string]bool {
	t.Helper()
	events, err := f.store().ListPending(context.Background(), 50)
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	byRecipient := make(map[string]bool, len(events))
	for _, event := range events {
		byRecipient[event.RecipientID] = event.Muted
	}
	return byRecipient
}

// muteChannel silences the fixture's channel for one user, under the workspace
// the caller names — which is what makes the cross-tenant case expressible.
func (f *outboxFixture) muteChannel(t *testing.T, userID, workspaceID string) {
	t.Helper()
	execFixture(t, f.pool, `
		INSERT INTO chat.conversation_notification_prefs (user_id, workspace_id, channel_id)
		VALUES ($1::uuid, $2::uuid, $3::uuid)`,
		userID, workspaceID, notifyWorkerChannel)
}

// newIsolatedWorkspace creates a second tenant and returns its id.
//
// The workspace and its general channel go in together: a deferred constraint
// requires every workspace to have exactly one, so the two statements have to
// share a transaction.
func newIsolatedWorkspace(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	var workspace string
	if err := tx.QueryRow(t.Context(), `
		INSERT INTO chat.workspaces (slug, name)
		VALUES ('mute-744-' || gen_random_uuid()::text, 'Mute isolation')
		RETURNING id::text`).Scan(&workspace); err != nil {
		t.Fatalf("seed second workspace: %v", err)
	}
	if _, err := tx.Exec(t.Context(), `
		INSERT INTO chat.channels (workspace_id, slug, display_name, type, is_general)
		VALUES ($1::uuid, 'geral', 'Geral', 'public', true)`, workspace); err != nil {
		t.Fatalf("seed second workspace general channel: %v", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatalf("commit second workspace: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM chat.workspaces WHERE id = $1::uuid`, workspace)
	})
	return workspace
}

// ---------------------------------------------------------------------------
// The approved presentation, against a real database (issue #870)
// ---------------------------------------------------------------------------
//
// None of this can be proved with a mock. What is under test is a WHERE clause
// spanning five tables and a SECURITY-relevant SQL function, and a fake would
// have to reimplement all of it — and would then be testing itself. The
// question every test below asks is the same one: can text belonging to one
// (workspace, recipient, message) reach a payload addressed to another, or
// reach one at all before the recipient is allowed to read it.

// presentationFixture is one channel message and one notification about it,
// addressed to a recipient who is a member of the workspace.
type presentationFixture struct {
	*outboxFixture
	recipient      string
	workspaceID    string
	channelID      string
	conversationID string
}

func seedPresentation(t *testing.T) *presentationFixture {
	t.Helper()
	fixture := seedOutbox(t, notificationevent.StatePending, 1)
	recipient := fixture.recipientOf(t, fixture.ids[0])
	workspace := newIsolatedWorkspace(t, fixture.pool)
	var channel string
	if err := fixture.pool.QueryRow(t.Context(), `
		INSERT INTO chat.channels (workspace_id, slug, display_name, type)
		VALUES ($1::uuid, 'preview', 'Preview', 'private') RETURNING id::text`, workspace).Scan(&channel); err != nil {
		t.Fatalf("seed preview channel: %v", err)
	}
	execFixture(t, fixture.pool, `
		INSERT INTO chat.workspace_members (workspace_id, user_id, role, status)
		VALUES ($1::uuid, $2::uuid, 'member', 'active')`, workspace, recipient)
	execFixture(t, fixture.pool, `INSERT INTO chat.channel_members (channel_id, user_id)
		VALUES ($1::uuid, $2::uuid)`, channel, recipient)
	execFixture(t, fixture.pool, `UPDATE chat.messages SET workspace_id = $1::uuid,
		channel_id = $2::uuid WHERE id = $3::uuid`, workspace, channel, fixture.messageID)
	execFixture(t, fixture.pool, `UPDATE chat.notification_outbox SET workspace_id = $1::uuid
		WHERE id = $2::uuid`, workspace, fixture.ids[0])
	return &presentationFixture{outboxFixture: fixture, recipient: recipient, workspaceID: workspace, channelID: channel}
}

func (f *presentationFixture) useDM(t *testing.T, kind string, title *string) {
	t.Helper()
	if err := f.pool.QueryRow(t.Context(), `
		INSERT INTO chat.dm_conversations (workspace_id, type, title, created_by, direct_pair_key)
		VALUES ($1::uuid, $2, $3, $4::uuid,
		        CASE WHEN $2 = 'direct' THEN LEAST($4::text, $5::text) || ':' || GREATEST($4::text, $5::text) END)
		RETURNING id::text`, f.workspaceID, kind, title, f.users[0], f.recipient).Scan(&f.conversationID); err != nil {
		t.Fatalf("seed preview DM: %v", err)
	}
	execFixture(t, f.pool, `INSERT INTO chat.dm_members (conversation_id, user_id)
		VALUES ($1::uuid, $2::uuid), ($1::uuid, $3::uuid)`, f.conversationID, f.users[0], f.recipient)
	execFixture(t, f.pool, `UPDATE chat.messages SET channel_id = NULL, dm_conversation_id = $1::uuid
		WHERE id = $2::uuid`, f.conversationID, f.messageID)
}

// claimFirst makes this fixture's notification the oldest claimable row.
//
// ClaimDue orders by (next_attempt_at, occurred_at, id) and takes a bounded
// batch, while both MarkEvaluated and ScheduleRetry stamp next_attempt_at with
// now() — which sorts this row *last*, behind anything another test left
// claimable, so whether the batch contained it was incidental.
//
// Backdating both scheduling columns makes it first by construction. It writes
// no status, so the transition trigger 000042 installs is not involved, and it
// changes nothing in production code to accommodate a test.
func (f *presentationFixture) claimFirst(t *testing.T) {
	t.Helper()
	execFixture(t, f.pool, `
		UPDATE chat.notification_outbox
		SET next_attempt_at = TIMESTAMPTZ '1970-01-01 00:00:00+00',
		    occurred_at     = TIMESTAMPTZ '1970-01-01 00:00:00+00'
		WHERE id = $1::uuid`, f.ids[0])
}

// claimOwn runs the real delivery claim and returns this fixture's own event,
// whatever else the batch picked up.
func (f *presentationFixture) claimOwn(
	t *testing.T, store *storage.PGXNotificationOutboxStore,
) storage.NotificationEvent {
	t.Helper()
	f.claimFirst(t)
	events, err := store.ClaimDue(t.Context(), 50, 5, notifyWorkerLease)
	if err != nil {
		t.Fatalf("ClaimDue: %v", err)
	}
	for _, event := range events {
		if event.ID == f.ids[0] {
			return event
		}
	}
	t.Fatalf("the seeded notification was not in the claimed batch of %d", len(events))
	return storage.NotificationEvent{}
}

// presentation uses the real delivery claim, not the pre-policy read.
func (f *presentationFixture) presentation(t *testing.T) storage.MessagePresentation {
	t.Helper()
	store := storage.NewPGXNotificationOutboxStore(f.pool, true)
	if err := store.MarkEvaluated(t.Context(), f.ids[0], notificationevent.StateEligible, ""); err != nil {
		t.Fatalf("MarkEvaluated: %v", err)
	}
	return f.claimOwn(t, store).Presentation
}

// recipientOf reads a notification's recipient back from the row, so a test
// never has to assume which of the fixture's users it addressed.
func (f *outboxFixture) recipientOf(t *testing.T, id string) string {
	t.Helper()
	var recipient string
	if err := f.pool.QueryRow(t.Context(), `
		SELECT recipient_user_id::text FROM chat.notification_outbox WHERE id = $1::uuid`,
		id).Scan(&recipient); err != nil {
		t.Fatalf("read recipient: %v", err)
	}
	return recipient
}

// The channel happy path.
func TestNotificationPresentationProjectsTheMessagePostgreSQL(t *testing.T) {
	fixture := seedPresentation(t)

	got := fixture.presentation(t)

	if got.Body != "worker fixture" {
		t.Fatalf("body = %q, want the message the fixture wrote", got.Body)
	}
	if got.Sender == "" {
		t.Fatal("the sender's display name is missing")
	}
	if got.Context == "" || got.Context[0] != '#' {
		t.Fatalf("context = %q, want the channel it was said in", got.Context)
	}
}

func TestNotificationPresentationDMReadAccessPostgreSQL(t *testing.T) {
	for _, kind := range []string{"direct", "group"} {
		t.Run(kind, func(t *testing.T) {
			fixture := seedPresentation(t)
			fixture.useDM(t, kind, nil)
			got := fixture.presentation(t)
			if got.Sender != "Outbox sender" || got.Body != "worker fixture" || got.GroupDM != (kind == "group") {
				t.Fatalf("active DM member received %+v", got)
			}
		})
	}
}

func TestNotificationPresentationPreservesGroupKindWithAnyTitlePostgreSQL(t *testing.T) {
	named, empty, whitespace := "Plantão", "", " \t\n\u00a0 "
	for _, test := range []struct {
		name  string
		title *string
	}{
		{"null", nil}, {"named", &named}, {"empty", &empty}, {"whitespace", &whitespace},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := seedPresentation(t)
			fixture.useDM(t, "group", test.title)
			got := fixture.presentation(t)
			wantContext := ""
			if test.title != nil {
				wantContext = *test.title
			}
			if !got.GroupDM || got.Context != wantContext {
				t.Fatalf("group kind or raw title lost: %+v", got)
			}
		})
	}
}

func TestNotificationPresentationRevokedReadAccessPostgreSQL(t *testing.T) {
	for _, kind := range []string{"channel", "direct", "group"} {
		for _, test := range []struct{ name, target, query string }{
			{"workspace disabled", "workspace", `UPDATE chat.workspaces SET status = 'disabled' WHERE id = $1::uuid`},
			{"workspace member removed", "recipient", `DELETE FROM chat.workspace_members WHERE user_id = $1::uuid`},
			{"workspace member suspended", "recipient", `UPDATE chat.workspace_members SET status = 'suspended' WHERE user_id = $1::uuid`},
			{"workspace member left", "recipient", `UPDATE chat.workspace_members SET status = 'left' WHERE user_id = $1::uuid`},
			{"channel archived", "channel", `UPDATE chat.channels SET status = 'archived' WHERE id = $1::uuid`},
			{"channel access revoked", "channel", `DELETE FROM chat.channel_members WHERE channel_id = $1::uuid`},
			{"DM archived", "dm", `UPDATE chat.dm_conversations SET status = 'archived' WHERE id = $1::uuid`},
			{"DM member left", "dm", `UPDATE chat.dm_members SET status = 'left', left_at = now() WHERE conversation_id = $1::uuid`},
		} {
			if (kind == "channel" && test.target == "dm") || (kind != "channel" && test.target == "channel") {
				continue
			}
			t.Run(kind+"/"+test.name, func(t *testing.T) {
				fixture := seedPresentation(t)
				if kind != "channel" {
					fixture.useDM(t, kind, nil)
				}
				id := map[string]string{
					"workspace": fixture.workspaceID, "recipient": fixture.recipient,
					"channel": fixture.channelID, "dm": fixture.conversationID,
				}[test.target]
				execFixture(t, fixture.pool, test.query, id)
				if got := fixture.presentation(t); got != (storage.MessagePresentation{}) {
					t.Fatalf("revoked access still projected %+v", got)
				}
			})
		}
	}
}

func TestNotificationPresentationUsesTheOutboxRecipientPostgreSQL(t *testing.T) {
	for _, kind := range []string{"channel", "direct", "group"} {
		t.Run(kind, func(t *testing.T) {
			fixture := seedPresentation(t)
			if kind != "channel" {
				fixture.useDM(t, kind, nil)
			}
			other := newFixtureUser(t, fixture.pool, "non-member")
			fixture.users = append(fixture.users, other)
			execFixture(t, fixture.pool, `INSERT INTO chat.workspace_members (workspace_id, user_id)
				VALUES ($1::uuid, $2::uuid)`, fixture.workspaceID, other)
			execFixture(t, fixture.pool, `UPDATE chat.notification_outbox SET recipient_user_id = $1::uuid
				WHERE id = $2::uuid`, other, fixture.ids[0])
			if got := fixture.presentation(t); got != (storage.MessagePresentation{}) {
				t.Fatalf("a different recipient received the member's preview: %+v", got)
			}
		})
	}
}

// A recipient who is no longer in the workspace gets no preview. The outbox row
// was written when they were, so this is the case that proves the projection
// re-derives access at claim time rather than trusting the row.
func TestNotificationPresentationStopsWhenAccessIsRevokedPostgreSQL(t *testing.T) {
	fixture := seedPresentation(t)

	execFixture(t, fixture.pool, `
		DELETE FROM chat.workspace_members
		WHERE workspace_id = $1::uuid AND user_id = $2::uuid`,
		fixture.workspaceID, fixture.recipient)

	if got := fixture.presentation(t); got != (storage.MessagePresentation{}) {
		t.Fatalf("a former member was shown %+v", got)
	}
}

// Every state in which a message is not readable produces no preview, and the
// notification itself is unaffected: it is still claimed, still delivered, and
// still says something generic.
func TestNotificationPresentationRefusesUnpublishableStatesPostgreSQL(t *testing.T) {
	for _, kind := range []string{"channel", "direct", "group"} {
		for name, mutation := range map[string]string{
			"withheld by a link scan": `UPDATE chat.messages SET status = 'pending_link_scan' WHERE id = $1::uuid`,
			"deleted":                 `UPDATE chat.messages SET status = 'deleted', deleted_at = now() WHERE id = $1::uuid`,
			"condemned":               `UPDATE chat.messages SET link_safety_state = 'malicious' WHERE id = $1::uuid`,
			// A system message carries its text in event_payload, which 000038
			// requires and which is not a person speaking. No producer writes a
			// notification for one today; this is what keeps a future one from
			// putting an event payload on a lock screen.
			"a system message": `UPDATE chat.messages
		                        SET kind = 'system', event_type = 'member_joined',
		                            event_payload = '{}'::jsonb
		                      WHERE id = $1::uuid`,
		} {
			t.Run(kind+"/"+name, func(t *testing.T) {
				fixture := seedPresentation(t)
				if kind != "channel" {
					fixture.useDM(t, kind, nil)
				}
				execFixture(t, fixture.pool, mutation, fixture.messageID)

				if got := fixture.presentation(t); got != (storage.MessagePresentation{}) {
					t.Fatalf("a %s message was previewed as %+v", name, got)
				}
			})
		}
	}
}

// The tenant scopes the lookup, so no arrangement of ids can make a message
// resolve for a notification another workspace owns.
//
// The outbox row is moved to a second workspace while still naming this
// message. That is not a state a producer can write — the workspace comes from
// the message — so it is written directly, which is exactly the point: even if
// one appeared, through an import, a repair script or a defect, the projection
// refuses it rather than putting one tenant's text in another's push.
func TestNotificationPresentationIsScopedToTheTenantPostgreSQL(t *testing.T) {
	for _, kind := range []string{"channel", "direct", "group"} {
		t.Run(kind, func(t *testing.T) {
			fixture := seedPresentation(t)
			if kind != "channel" {
				fixture.useDM(t, kind, nil)
			}
			other := newIsolatedWorkspace(t, fixture.pool)

			execFixture(t, fixture.pool, `
		UPDATE chat.notification_outbox SET workspace_id = $1::uuid WHERE id = $2::uuid`,
				other, fixture.ids[0])

			if got := fixture.presentation(t); got != (storage.MessagePresentation{}) {
				t.Fatalf("a cross-workspace notification was previewed as %+v", got)
			}
		})
	}
}

// A message with no text and no files carries a sender and a place and nothing
// else, so the banner can say who and where without claiming to quote anybody.
func TestNotificationPresentationOfAnEmptyMessagePostgreSQL(t *testing.T) {
	fixture := seedPresentation(t)
	execFixture(t, fixture.pool,
		`UPDATE chat.messages SET body_text = '' WHERE id = $1::uuid`, fixture.messageID)

	got := fixture.presentation(t)

	if got.Body != "" {
		t.Fatalf("body = %q, want nothing", got.Body)
	}
	if got.Attachment {
		t.Fatal("a message with no files reported one")
	}
	if got.Sender == "" {
		t.Fatal("the sender is still known and should still be projected")
	}
}

// The body is bounded by the statement, so a batch claim never pulls the 40000
// characters a message may carry, times the batch size, to discard almost all
// of it.
func TestNotificationPresentationBoundsTheBodyPostgreSQL(t *testing.T) {
	fixture := seedPresentation(t)
	execFixture(t, fixture.pool,
		`UPDATE chat.messages SET body_text = repeat('a', 40000) WHERE id = $1::uuid`,
		fixture.messageID)

	if got := len(fixture.presentation(t).Body); got > 1000 {
		t.Fatalf("the projection returned %d characters of message body", got)
	}
}

// The sender is bounded by the statement too, and the two names are not clipped.
//
// The three strings the projection carries are bounded differently because the
// schema bounds them differently, and the test has to follow that rather than
// pretend otherwise:
//
//   - auth.users.display_name has *no* database CHECK. It is the one column an
//     oversized value can genuinely reach, so it is the one written oversized
//     here — straight to the table, because the point is that the projection
//     bounds what it reads rather than trusting whichever writer produced it.
//   - chat.channels.display_name (1..100) and chat.dm_conversations.title
//     (<= 120) are bounded by their own CHECKs. Writing past them is impossible
//     without dropping a constraint, and a test that drops one is a test that
//     can leave the schema broken when it fails. What is worth asserting for
//     those two is the other direction: a value at the column's own ceiling
//     arrives whole, so the SQL bound can never be set low enough to clip a
//     name somebody legitimately chose.
func TestNotificationPresentationBoundsTheSenderAndContextPostgreSQL(t *testing.T) {
	t.Run("an unbounded display name is cut", func(t *testing.T) {
		fixture := seedPresentation(t)
		execFixture(t, fixture.pool, `UPDATE auth.users SET display_name = repeat('n', 5000)
			WHERE id = $1::uuid`, fixture.users[0])

		got := fixture.presentation(t)

		if len(got.Sender) == 0 || len(got.Sender) > 200 {
			t.Fatalf("the projection returned %d characters of display name", len(got.Sender))
		}
	})

	t.Run("a channel name at its own ceiling is whole", func(t *testing.T) {
		fixture := seedPresentation(t)
		name := strings.Repeat("c", 100)
		execFixture(t, fixture.pool, `UPDATE chat.channels SET display_name = $2
			WHERE id = $1::uuid`, fixture.channelID, name)

		if got := fixture.presentation(t); got.Context != "#"+name {
			t.Fatalf("a %d-character channel name arrived as %d characters",
				len(name), len(got.Context))
		}
	})

	t.Run("a group title at its own ceiling is whole", func(t *testing.T) {
		fixture := seedPresentation(t)
		title := strings.Repeat("g", 120)
		fixture.useDM(t, "group", &title)

		if got := fixture.presentation(t); got.Context != title {
			t.Fatalf("a %d-character group title arrived as %d characters",
				len(title), len(got.Context))
		}
	})
}

func TestNotificationPendingOmitsPresentationPostgreSQL(t *testing.T) {
	fixture := seedPresentation(t)
	events, err := storage.NewPGXNotificationOutboxStore(fixture.pool, true).ListPending(t.Context(), 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.ID == fixture.ids[0] {
			if event.Presentation != (storage.MessagePresentation{}) {
				t.Fatalf("pre-policy read loaded message content: %+v", event.Presentation)
			}
			return
		}
	}
	t.Fatal("pending notification missing")
}

func TestNotificationClaimWithoutPreviewPostgreSQL(t *testing.T) {
	fixture := seedPresentation(t)
	store := fixture.store()
	if err := store.MarkEvaluated(t.Context(), fixture.ids[0], notificationevent.StateEligible, ""); err != nil {
		t.Fatal(err)
	}
	events, err := store.ClaimDue(t.Context(), 50, 5, notifyWorkerLease)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Presentation != (storage.MessagePresentation{}) {
		t.Fatalf("preview-disabled claim loaded presentation: %+v", events)
	}
}

// A claim is a snapshot. A later retry must resolve access again.
func TestNotificationPresentationClaimSnapshotPostgreSQL(t *testing.T) {
	for _, mutation := range []string{
		`UPDATE chat.messages SET status = 'deleted', deleted_at = now() WHERE id = $1::uuid`,
		`DELETE FROM chat.workspace_members WHERE user_id = $2::uuid AND workspace_id = (SELECT workspace_id FROM chat.messages WHERE id = $1::uuid)`,
	} {
		t.Run(mutation, func(t *testing.T) {
			fixture := seedPresentation(t)
			claimed := fixture.presentation(t)
			if strings.HasPrefix(mutation, "UPDATE") {
				execFixture(t, fixture.pool, mutation, fixture.messageID)
			} else {
				execFixture(t, fixture.pool, mutation, fixture.messageID, fixture.recipient)
			}
			if claimed.Body != "worker fixture" {
				t.Fatal("claim did not retain its snapshot")
			}
			store := storage.NewPGXNotificationOutboxStore(fixture.pool, true)
			if err := store.ScheduleRetry(t.Context(), fixture.ids[0], 1, 0, "delivery_transient"); err != nil {
				t.Fatal(err)
			}
			retried := fixture.claimOwn(t, store)
			if retried.Attempts != 2 || retried.Presentation != (storage.MessagePresentation{}) {
				t.Fatalf("retry retained revoked preview: %+v", retried)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// SR-001: the recipient's own account, not just their membership
// ---------------------------------------------------------------------------
//
// A workspace membership and a global account are two different things, and
// only one of them an operator revokes when they suspend somebody. Sessions are
// revoked with the account — authsession.ActiveSessionCTE refuses any session
// whose user is not `status = 'active'` with `deleted_at IS NULL` — but the
// push subscription and the workspace membership survive, and the outbox row
// that was already written survives with them.
//
// So the projection has to ask the same question the session contract asks,
// about o.recipient_user_id. Without it a suspended account still had message
// text delivered to a browser it had already registered, at a moment when the
// authenticated UI would have refused the same person outright.
//
// These are deliberately *not* the workspace_members tests above. Those revoke
// `chat.workspace_members.status`, which is membership of one tenant; these
// revoke `auth.users.status` and `auth.users.deleted_at`, which is the person.

// setAccount puts the recipient's global account into one state, by writing the
// two columns ActiveSessionCTE reads and nothing else.
func (f *presentationFixture) setAccount(t *testing.T, status string, deleted bool) {
	t.Helper()
	execFixture(t, f.pool, `
		UPDATE auth.users
		SET status = $2::text,
		    deleted_at = CASE WHEN $3::boolean THEN now() END
		WHERE id = $1::uuid`, f.recipient, status, deleted)
}

// presentationForKind seeds one conversation of the given kind and returns the
// fixture, so each account-state case below reads as one line.
func presentationForKind(t *testing.T, kind string) *presentationFixture {
	t.Helper()
	fixture := seedPresentation(t)
	if kind != "channel" {
		fixture.useDM(t, kind, nil)
	}
	return fixture
}

// A globally suspended recipient gets no preview, in every kind of conversation.
//
// The `active` row of each table is the control: without it the test would pass
// just as well against a projection that never produces a preview at all.
func TestNotificationPresentationRejectsGloballySuspendedRecipientPostgreSQL(t *testing.T) {
	for _, kind := range []string{"channel", "direct", "group"} {
		for _, test := range []struct {
			name    string
			status  string
			preview bool
		}{
			{name: "active", status: "active", preview: true},
			{name: "suspended", status: "suspended"},
			// Not asked for, and worth one line: the predicate is an allowlist
			// of one value, so every other state the CHECK permits is refused
			// by the same rule rather than by a list somebody has to maintain.
			{name: "locked", status: "locked"},
			{name: "invited", status: "invited"},
		} {
			t.Run(kind+"/"+test.name, func(t *testing.T) {
				fixture := presentationForKind(t, kind)
				fixture.setAccount(t, test.status, false)

				got := fixture.presentation(t)

				if test.preview {
					if got.Body != "worker fixture" || got.Sender == "" {
						t.Fatalf("an active account was refused its preview: %+v", got)
					}
					return
				}
				if got != (storage.MessagePresentation{}) {
					t.Fatalf("a %s account was shown %+v", test.status, got)
				}
			})
		}
	}
}

// A soft-deleted recipient gets no preview either, and that is a separate
// column: a deleted account can still read `status = 'active'` — deletion is
// recorded by deleted_at — so testing only the status would leave the half of
// the contract that ActiveSessionCTE spells out second.
func TestNotificationPresentationRejectsDeletedRecipientPostgreSQL(t *testing.T) {
	for _, kind := range []string{"channel", "direct", "group"} {
		t.Run(kind, func(t *testing.T) {
			fixture := presentationForKind(t, kind)
			// status stays 'active' on purpose. Only deleted_at is set, so what
			// this proves is that deleted_at alone is disqualifying.
			fixture.setAccount(t, "active", true)

			if got := fixture.presentation(t); got != (storage.MessagePresentation{}) {
				t.Fatalf("a soft-deleted account was shown %+v", got)
			}
		})
	}
}

// The global account is re-read on every claim, not snapshotted with the first.
//
// This is the case SR-001 actually describes: the notification is produced and
// claimed while the account is fine, delivery fails transiently, and the
// suspension lands in between. The retry must resolve the account again and
// find nothing to show — the same guarantee the message state and the workspace
// membership already had, for the one predicate that was missing.
func TestNotificationPresentationRetryRechecksGlobalAccountPostgreSQL(t *testing.T) {
	for _, test := range []struct {
		name    string
		status  string
		deleted bool
	}{
		{name: "suspended", status: "suspended"},
		{name: "soft deleted", status: "active", deleted: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := seedPresentation(t)

			claimed := fixture.presentation(t)
			if claimed.Body != "worker fixture" {
				t.Fatalf("the first claim, while the account was active, saw %+v", claimed)
			}

			fixture.setAccount(t, test.status, test.deleted)

			store := storage.NewPGXNotificationOutboxStore(fixture.pool, true)
			if err := store.ScheduleRetry(t.Context(), fixture.ids[0], 1, 0, "delivery_transient"); err != nil {
				t.Fatalf("ScheduleRetry: %v", err)
			}
			retried := fixture.claimOwn(t, store)

			if retried.Attempts != 2 {
				t.Fatalf("attempts = %d, want the second claim", retried.Attempts)
			}
			if retried.Presentation != (storage.MessagePresentation{}) {
				t.Fatalf("the retry kept a preview for a %s account: %+v",
					test.name, retried.Presentation)
			}
		})
	}
}
