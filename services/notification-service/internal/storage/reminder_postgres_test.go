package storage_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicrepository/nchat/libs/go/platform/notificationevent"
	"github.com/nicrepository/nchat/services/notification-service/internal/storage"
)

// Issue #825: the reminder scheduler against a real PostgreSQL.
//
// None of this can be proved with a mock, and that is why the file exists. What
// is under test is behaviour the database owns:
//
//   - a due-reminder claim that two schedulers cannot both take, because
//     FOR UPDATE ... SKIP LOCKED hands them disjoint rows;
//   - a unique index that decides whether the nth reminder already exists, so a
//     pass repeated after a crash produces no second delivery;
//   - a schedule and an outbox row written by one statement, so a crash between
//     them is not a state that can exist;
//   - a ceiling that ends the reminders and produces the EXPIRED state
//     migration 000049 declared and left without a producer;
//   - a claim predicate that refuses a reminder whose recipient answered in the
//     meantime, evaluated against the row it locks.
//
// Time is asserted exactly, and without waiting for any of it. The scheduler
// takes its reference instant as a parameter (see ScheduleDueReminders), so
// every test here pins T0, writes the due instant directly, and asks the
// scheduler to reason about a named moment. "4m59s is not due" and "5m00s is
// due" are therefore equalities rather than tolerances, and the next window is
// asserted as an exact timestamp rather than as an approximate gap.
//
// Opt-in like its neighbours: needs NOTIFICATION_TEST_DATABASE_URL against a
// _test database carrying the real migrations.

// reminderT0 is the instant every test in this file counts from. A fixed,
// arbitrary, unambiguous UTC moment: nothing here reads a wall clock, so the
// suite behaves identically at midnight, across a DST change, and in CI.
var reminderT0 = time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)

// reminderFixture is one urgent message asking to keep reminding, and its
// recipients' rows.
type reminderFixture struct {
	pool       *pgxpool.Pool
	messageID  string
	recipients []string
}

// seedReminders writes a message and `count` recipients, each pending and each
// due at exactly `dueAt`.
func seedReminders(t *testing.T, count int, dueAt time.Time, ackRequired bool) *reminderFixture {
	t.Helper()
	pool := newNotificationTestPool(t)
	fixture := &reminderFixture{pool: pool}

	// The scheduler is global by design: it finds every due reminder in the
	// database, not this fixture's alone, because that is what a worker draining
	// a shared queue does. An earlier test's rows would therefore be counted in
	// this one's results, so the whole reminder state is cleared before each
	// fixture seeds its own. Nothing else in the schema is touched.
	for _, reset := range []string{
		`UPDATE chat.message_acknowledgements SET next_reminder_at = NULL
		 WHERE next_reminder_at IS NOT NULL`,
		`DELETE FROM chat.notification_outbox WHERE kind = 'urgent_reminder'`,
	} {
		if _, err := pool.Exec(t.Context(), reset); err != nil {
			t.Fatalf("reset reminder state: %v", err)
		}
	}

	sender := newFixtureUser(t, pool, "reminder-sender")
	if err := pool.QueryRow(t.Context(), `
		INSERT INTO chat.messages
			(workspace_id, channel_id, sender_id, kind, body_text, body_format, status,
			 priority, persistent_notifications, acknowledgement_required)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'user', 'reminder fixture', 'v2', 'active',
		        'urgent', true, $4::boolean)
		RETURNING id::text`,
		notifyWorkerWorkspace, notifyWorkerChannel, sender, ackRequired).Scan(&fixture.messageID); err != nil {
		t.Fatalf("seed message: %v", err)
	}

	for range count {
		recipient := newFixtureUser(t, pool, "reminder-recipient")
		fixture.recipients = append(fixture.recipients, recipient)
		if _, err := pool.Exec(t.Context(), `
			INSERT INTO chat.message_acknowledgements
				(message_id, recipient_id, next_reminder_at)
			VALUES ($1::uuid, $2::uuid, $3::timestamptz)`,
			fixture.messageID, recipient, dueAt.UTC()); err != nil {
			t.Fatalf("seed reminder row: %v", err)
		}
	}
	return fixture
}

func (f *reminderFixture) store() *storage.PGXNotificationOutboxStore {
	return storage.NewPGXNotificationOutboxStore(f.pool, false)
}

// reminderKeys reads the dedupe keys of every reminder written for this message,
// which is the identity the unique index decides on.
func (f *reminderFixture) reminderKeys(t *testing.T) []string {
	t.Helper()
	rows, err := f.pool.Query(t.Context(), `
		SELECT dedupe_key
		FROM chat.notification_outbox
		WHERE message_id = $1::uuid AND kind = $2
		ORDER BY dedupe_key`, f.messageID, string(notificationevent.EventTypeUrgentReminder))
	if err != nil {
		t.Fatalf("read reminder keys: %v", err)
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			t.Fatalf("scan reminder key: %v", err)
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate reminder keys: %v", err)
	}
	return keys
}

// schedule runs one scheduling pass as of `at`.
func (f *reminderFixture) schedule(
	t *testing.T, at time.Time, batchSize int,
) storage.ReminderScheduleResult {
	t.Helper()
	result, err := f.store().ScheduleDueReminders(t.Context(), at, batchSize)
	if err != nil {
		t.Fatalf("ScheduleDueReminders at %s: %v", at.Format(time.RFC3339Nano), err)
	}
	return result
}

type scheduleState struct {
	State     string
	Count     int
	Scheduled bool
	// NextReminderAt is the stored instant, so the window is asserted as an
	// equality against the reference the scheduler was given rather than as a
	// gap measured off a wall clock.
	NextReminderAt time.Time
}

func (f *reminderFixture) scheduleOf(t *testing.T, recipient string) scheduleState {
	t.Helper()
	var state scheduleState
	var nextReminderAt *time.Time
	if err := f.pool.QueryRow(t.Context(), `
		SELECT state, reminder_count, next_reminder_at
		FROM chat.message_acknowledgements
		WHERE message_id = $1::uuid AND recipient_id = $2::uuid`,
		f.messageID, recipient,
	).Scan(&state.State, &state.Count, &nextReminderAt); err != nil {
		t.Fatalf("read schedule: %v", err)
	}
	if nextReminderAt != nil {
		state.Scheduled = true
		state.NextReminderAt = nextReminderAt.UTC()
	}
	return state
}

func (f *reminderFixture) resolve(t *testing.T, recipient, state string) {
	t.Helper()
	if _, err := f.pool.Exec(t.Context(), `
		UPDATE chat.message_acknowledgements
		SET state = $3, resolved_at = now()
		WHERE message_id = $1::uuid AND recipient_id = $2::uuid`,
		f.messageID, recipient, state); err != nil {
		t.Fatalf("resolve recipient: %v", err)
	}
}

// ── the window, to the microsecond ─────────────────────────────────────────

// dueAtT0Plus5m is the fixture every boundary test uses: one recipient whose
// first reminder falls at exactly T0 + UrgentReminderInterval.
func dueAtT0Plus5m(t *testing.T, count int) *reminderFixture {
	t.Helper()
	return seedReminders(t, count, reminderT0.Add(notificationevent.UrgentReminderInterval), false)
}

// The boundary is `<=`, and it is asserted at the smallest instant the column
// can hold on either side of it.
//
// timestamptz stores microseconds, so T0+5m minus one microsecond is the
// closest a reference can get to the boundary without reaching it, and
// T0+5m plus one microsecond is the closest it can get from above. Anything
// looser than this — "about five minutes", a tolerance, a sleep — would pass
// against an off-by-one in either direction.
func TestReminderWindowBoundaryIsExactPostgreSQL(t *testing.T) {
	const microsecond = time.Microsecond
	due := reminderT0.Add(notificationevent.UrgentReminderInterval)

	for _, tc := range []struct {
		name      string
		reference time.Time
		wantDue   bool
	}{
		{"one interval minus one second", due.Add(-time.Second), false},
		{"one microsecond before the boundary", due.Add(-microsecond), false},
		{"exactly the boundary", due, true},
		{"one microsecond after the boundary", due.Add(microsecond), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := dueAtT0Plus5m(t, 1)

			result := fixture.schedule(t, tc.reference, 10)

			if scheduled := result.Scheduled == 1; scheduled != tc.wantDue {
				t.Fatalf("at %s scheduled=%d, want due=%v",
					tc.reference.Format(time.RFC3339Nano), result.Scheduled, tc.wantDue)
			}
			keys := fixture.reminderKeys(t)
			if tc.wantDue && len(keys) != 1 {
				t.Fatalf("wrote %d outbox rows for a due reminder, want 1", len(keys))
			}
			if !tc.wantDue && len(keys) != 0 {
				t.Fatalf("wrote %d outbox rows before the boundary", len(keys))
			}
		})
	}
}

// A due reminder produces exactly one outbox row per recipient, carrying the
// occurrence in its dedupe key, and the next window falls exactly one interval
// after the reference the scheduler was given — not after the instant the
// statement happened to execute.
func TestDueReminderIsScheduledAndTheWindowAdvancesPostgreSQL(t *testing.T) {
	fixture := dueAtT0Plus5m(t, 2)
	reference := reminderT0.Add(notificationevent.UrgentReminderInterval)

	result := fixture.schedule(t, reference, 10)

	if result.Scheduled != 2 || result.Deduplicated != 0 || result.Expired != 0 {
		t.Fatalf("result = %+v, want two fresh reminders", result)
	}
	keys := fixture.reminderKeys(t)
	if len(keys) != 2 {
		t.Fatalf("wrote %d outbox rows, want one per recipient", len(keys))
	}
	for _, key := range keys {
		want := "message:" + fixture.messageID + ":urgent_reminder:1"
		if key != want {
			t.Fatalf("dedupe key = %q, want %q", key, want)
		}
	}
	wantNext := reference.Add(notificationevent.UrgentReminderInterval)
	for _, recipient := range fixture.recipients {
		schedule := fixture.scheduleOf(t, recipient)
		if schedule.Count != 1 || !schedule.Scheduled || schedule.State != "pending" {
			t.Fatalf("schedule = %+v, want one reminder sent and the next one waiting", schedule)
		}
		if !schedule.NextReminderAt.Equal(wantNext) {
			t.Fatalf("next reminder at %s, want exactly %s",
				schedule.NextReminderAt.Format(time.RFC3339Nano), wantNext.Format(time.RFC3339Nano))
		}
	}
}

// The reminder row the scheduler writes is an ordinary pending notification: the
// same worker, the same policy evaluation, the same claim. A reminder that
// arrived already eligible would be one that skipped the delivery policy #825
// requires it to pass every time.
func TestScheduledReminderEntersTheOrdinaryQueuePostgreSQL(t *testing.T) {
	fixture := dueAtT0Plus5m(t, 1)
	fixture.schedule(t, reminderT0.Add(notificationevent.UrgentReminderInterval), 10)

	var status, priority, origin, sourceType string
	var sameInstant bool
	if err := fixture.pool.QueryRow(t.Context(), `
		SELECT o.status, o.priority, o.origin, o.source_type, o.occurred_at = m.created_at
		FROM chat.notification_outbox o
		JOIN chat.messages m ON m.id = o.message_id
		WHERE o.message_id = $1::uuid AND o.kind = 'urgent_reminder'`,
		fixture.messageID,
	).Scan(&status, &priority, &origin, &sourceType, &sameInstant); err != nil {
		t.Fatalf("read reminder row: %v", err)
	}
	if status != string(notificationevent.StatePending) {
		t.Fatalf("status = %q, want pending so the policy still decides it", status)
	}
	if priority != string(notificationevent.PriorityHigh) || origin != string(notificationevent.OriginLive) {
		t.Fatalf("priority/origin = %q/%q, want high/live", priority, origin)
	}
	if sourceType != string(notificationevent.SourceTypeMessage) {
		t.Fatalf("source type = %q, want message", sourceType)
	}
	if !sameInstant {
		t.Fatal("a reminder must name when the message happened, not when it was re-sent")
	}
}

// Several cycles produce several distinct reminders, each with its own
// occurrence, each exactly one interval after the last. Nothing here moves the
// window by hand: the reference advances by the interval and the scheduler's own
// arithmetic is what has to line up with it.
func TestSeveralReminderCyclesUseTheExactWindowPostgreSQL(t *testing.T) {
	fixture := dueAtT0Plus5m(t, 1)
	reference := reminderT0.Add(notificationevent.UrgentReminderInterval)

	for cycle := 1; cycle <= 3; cycle++ {
		if result := fixture.schedule(t, reference, 10); result.Scheduled != 1 {
			t.Fatalf("cycle %d scheduled %d, want 1", cycle, result.Scheduled)
		}
		schedule := fixture.scheduleOf(t, fixture.recipients[0])
		wantNext := reference.Add(notificationevent.UrgentReminderInterval)
		if !schedule.NextReminderAt.Equal(wantNext) {
			t.Fatalf("cycle %d: next reminder at %s, want %s", cycle,
				schedule.NextReminderAt.Format(time.RFC3339Nano), wantNext.Format(time.RFC3339Nano))
		}
		// One microsecond short of the next window is still not due, on every
		// cycle and not only the first.
		if result := fixture.schedule(t, wantNext.Add(-time.Microsecond), 10); result.Scheduled != 0 {
			t.Fatalf("cycle %d was reminded a microsecond early", cycle)
		}
		reference = wantNext
	}

	keys := fixture.reminderKeys(t)
	if len(keys) != 3 {
		t.Fatalf("three cycles produced %d reminders: %v", len(keys), keys)
	}
	for occurrence, key := range keys {
		want := fmt.Sprintf("message:%s:urgent_reminder:%d", fixture.messageID, occurrence+1)
		if key != want {
			t.Fatalf("key = %q, want %q", key, want)
		}
	}
}

// ── only PENDING is eligible ────────────────────────────────────────────────

// Every terminal state is ignored, and each for its own reason: they all mean
// the recipient stopped being asked. A state added later would be caught by the
// same predicate, because the predicate names pending rather than listing the
// four.
func TestOnlyAPendingRecipientProducesAReminderPostgreSQL(t *testing.T) {
	reminderDue := reminderT0.Add(notificationevent.UrgentReminderInterval)
	for _, terminal := range []string{"acknowledged", "responded", "expired", "cancelled"} {
		t.Run(terminal, func(t *testing.T) {
			fixture := dueAtT0Plus5m(t, 1)
			fixture.resolve(t, fixture.recipients[0], terminal)

			result := fixture.schedule(t, reminderDue, 10)
			if result.Scheduled != 0 {
				t.Fatalf("a %s recipient was reminded", terminal)
			}
		})
	}
}

// A recipient who resolves between two cycles stops being reminded, and the
// others do not. This is the group case #820 is explicit about.
func TestResolvingOneRecipientLeavesTheOthersRemindedPostgreSQL(t *testing.T) {
	fixture := dueAtT0Plus5m(t, 3)
	fixture.resolve(t, fixture.recipients[0], "acknowledged")

	result := fixture.schedule(t, reminderT0.Add(notificationevent.UrgentReminderInterval), 10)
	if result.Scheduled != 2 {
		t.Fatalf("scheduled %d, want the two recipients who are still waiting", result.Scheduled)
	}
}

// A deleted message reminds nobody, even if a schedule somehow survived the
// delete. The predicate is the fail-closed direction: a reminder can never be
// produced for a message its recipients are not entitled to see.
func TestADeletedMessageRemindsNobodyPostgreSQL(t *testing.T) {
	fixture := dueAtT0Plus5m(t, 2)
	if _, err := fixture.pool.Exec(t.Context(), `
		UPDATE chat.messages SET status = 'deleted', deleted_at = now() WHERE id = $1::uuid`,
		fixture.messageID); err != nil {
		t.Fatalf("delete message: %v", err)
	}

	result := fixture.schedule(t, reminderT0.Add(notificationevent.UrgentReminderInterval), 10)
	if result.Scheduled != 0 {
		t.Fatalf("a deleted message scheduled %d reminders", result.Scheduled)
	}
}

// Withdrawing the intent stops the reminders even while the schedule is still
// written: persistent_notifications is the authority, and the column is a
// consequence of it rather than a substitute for it.
func TestAMessageThatNoLongerAsksRemindsNobodyPostgreSQL(t *testing.T) {
	fixture := dueAtT0Plus5m(t, 2)
	if _, err := fixture.pool.Exec(t.Context(), `
		UPDATE chat.messages SET persistent_notifications = false WHERE id = $1::uuid`,
		fixture.messageID); err != nil {
		t.Fatalf("withdraw intent: %v", err)
	}

	result := fixture.schedule(t, reminderT0.Add(notificationevent.UrgentReminderInterval), 10)
	if result.Scheduled != 0 {
		t.Fatalf("a message that no longer asks scheduled %d reminders", result.Scheduled)
	}
}

// ── idempotency and concurrency ─────────────────────────────────────────────

// A pass repeated without the window advancing produces no second reminder. The
// unique index decides it, not a flag in memory, so the guarantee survives a
// restart between the two passes.
func TestRepeatingAReminderPassCreatesNoSecondEventPostgreSQL(t *testing.T) {
	fixture := dueAtT0Plus5m(t, 2)
	due := reminderT0.Add(notificationevent.UrgentReminderInterval)

	fixture.schedule(t, due, 10)
	// The window has advanced, so a second pass at the same reference finds
	// nothing due. Rewinding the bookkeeping is what a pass repeated after a
	// crash looks like: the same occurrence comes due again, and the unique
	// index — not a flag in memory — is what refuses to write it twice.
	if _, err := fixture.pool.Exec(t.Context(), `
		UPDATE chat.message_acknowledgements
		SET next_reminder_at = $2::timestamptz, reminder_count = 0
		WHERE message_id = $1::uuid`, fixture.messageID, due.UTC()); err != nil {
		t.Fatalf("replay the same occurrence: %v", err)
	}
	result := fixture.schedule(t, due, 10)
	if result.Scheduled != 0 || result.Deduplicated != 2 {
		t.Fatalf("result = %+v, want both rows recognised as already scheduled", result)
	}
	if keys := fixture.reminderKeys(t); len(keys) != 2 {
		t.Fatalf("replaying the same occurrence produced %d rows, want 2", len(keys))
	}
}

// Two schedulers running at once produce one reminder per recipient between
// them, not two each. FOR UPDATE ... SKIP LOCKED is what hands them disjoint
// rows; the unique index is the second line behind it.
func TestConcurrentSchedulersProduceOneReminderEachPostgreSQL(t *testing.T) {
	fixture := dueAtT0Plus5m(t, 8)
	due := reminderT0.Add(notificationevent.UrgentReminderInterval)

	var wait sync.WaitGroup
	scheduled := make([]int, 2)
	errs := make([]error, 2)
	for worker := range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := storage.NewPGXNotificationOutboxStore(fixture.pool, false).
				ScheduleDueReminders(t.Context(), due, 8)
			scheduled[worker], errs[worker] = result.Scheduled, err
		}()
	}
	wait.Wait()

	for worker, err := range errs {
		if err != nil {
			t.Fatalf("worker %d: %v", worker, err)
		}
	}
	if total := scheduled[0] + scheduled[1]; total != 8 {
		t.Fatalf("two workers scheduled %d reminders between them, want 8", total)
	}
	keys := fixture.reminderKeys(t)
	if len(keys) != 8 {
		t.Fatalf("wrote %d outbox rows, want exactly one per recipient", len(keys))
	}
	for _, recipient := range fixture.recipients {
		if got := fixture.scheduleOf(t, recipient).Count; got != 1 {
			t.Fatalf("recipient advanced %d times, want exactly 1", got)
		}
	}
}

// The batch bounds one pass. A scheduler that ignored it would be the one
// unbounded claim in a worker built entirely out of bounded ones.
func TestReminderSchedulingRespectsTheBatchSizePostgreSQL(t *testing.T) {
	fixture := dueAtT0Plus5m(t, 5)

	result := fixture.schedule(t, reminderT0.Add(notificationevent.UrgentReminderInterval), 2)
	if result.Scheduled != 2 {
		t.Fatalf("scheduled %d with a batch of 2", result.Scheduled)
	}
}

// ── the ceiling ─────────────────────────────────────────────────────────────

// Reminders stop. Driving a recipient to the ceiling ends the schedule and, on a
// message that asked for nothing else, resolves them as EXPIRED — the state
// migration 000049 declared and deliberately left without a producer.
func TestRemindersStopAtTheCeilingPostgreSQL(t *testing.T) {
	fixture := dueAtT0Plus5m(t, 1)

	last := fixture.driveToCeiling(t)

	// The deadline is arithmetic, not a guess: the last reminder falls exactly
	// MaxUrgentReminders intervals after T0, which is the hour #825 documents.
	wantLast := reminderT0.Add(
		time.Duration(notificationevent.MaxUrgentReminders) * notificationevent.UrgentReminderInterval)
	if !last.Equal(wantLast) {
		t.Fatalf("last reminder at %s, want exactly %s",
			last.Format(time.RFC3339Nano), wantLast.Format(time.RFC3339Nano))
	}

	schedule := fixture.scheduleOf(t, fixture.recipients[0])
	if schedule.Count != notificationevent.MaxUrgentReminders {
		t.Fatalf("sent %d reminders, want the ceiling of %d",
			schedule.Count, notificationevent.MaxUrgentReminders)
	}
	if schedule.Scheduled {
		t.Fatal("the schedule must be gone once the ceiling is reached")
	}
	if schedule.State != "expired" {
		t.Fatalf("state = %q, want expired", schedule.State)
	}

	// And it stays stopped, however far the clock is pushed past the deadline.
	if result := fixture.schedule(t, last.Add(24*time.Hour), 10); result.Scheduled != 0 {
		t.Fatal("a recipient past the ceiling was reminded again")
	}
}

// driveToCeiling runs one pass per interval until the reminders run out, and
// returns the reference the last one used.
//
// The clock is advanced by handing the scheduler the next instant, never by
// rewriting next_reminder_at: the window arithmetic under test is the
// scheduler's own, so a test that moved the column would be asserting against
// its own bookkeeping instead of against the implementation's.
func (f *reminderFixture) driveToCeiling(t *testing.T) time.Time {
	t.Helper()
	reference := reminderT0.Add(notificationevent.UrgentReminderInterval)
	for cycle := 1; cycle <= notificationevent.MaxUrgentReminders; cycle++ {
		if result := f.schedule(t, reference, 10); result.Scheduled != 1 {
			t.Fatalf("cycle %d scheduled %d, want 1", cycle, result.Scheduled)
		}
		if cycle == notificationevent.MaxUrgentReminders {
			break
		}
		reference = reference.Add(notificationevent.UrgentReminderInterval)
	}
	return reference
}

// On a message that also asked for confirmation, running out of reminders stops
// the reminding and leaves the question open. Marking them expired would tell
// the sender nobody is going to answer, which is not something running out of
// pushes establishes.
func TestTheCeilingDoesNotWithdrawAConfirmationRequestPostgreSQL(t *testing.T) {
	fixture := seedReminders(t, 1, reminderT0.Add(notificationevent.UrgentReminderInterval), true)

	fixture.driveToCeiling(t)

	schedule := fixture.scheduleOf(t, fixture.recipients[0])
	if schedule.State != "pending" {
		t.Fatalf("state = %q, want the confirmation request still open", schedule.State)
	}
	if schedule.Scheduled {
		t.Fatal("the reminders must have stopped even though the request stands")
	}
}

// ── the resolved recipient ──────────────────────────────────────────────────

// A reminder already in the queue is not claimable once its recipient answers.
// This is the guarantee, not the cleanup: the predicate is inside the claim, so
// an acknowledgement that committed first wins with no window between deciding
// to deliver and owning the row.
func TestAResolvedRecipientsReminderIsNotClaimablePostgreSQL(t *testing.T) {
	fixture := dueAtT0Plus5m(t, 1)
	store := fixture.store()
	fixture.schedule(t, reminderT0.Add(notificationevent.UrgentReminderInterval), 10)
	if err := store.MarkEvaluated(t.Context(), fixture.reminderID(t),
		notificationevent.StateEligible, ""); err != nil {
		t.Fatalf("MarkEvaluated: %v", err)
	}

	fixture.resolve(t, fixture.recipients[0], "acknowledged")

	claimed, err := store.ClaimDue(t.Context(), 10, 5, time.Hour)
	if err != nil {
		t.Fatalf("ClaimDue: %v", err)
	}
	for _, event := range claimed {
		if event.EventType == string(notificationevent.EventTypeUrgentReminder) {
			t.Fatal("a reminder was claimed for a recipient who had already answered")
		}
	}
}

// And the cleanup: a reminder nothing will ever claim is retired rather than
// left in the backlog for ever.
func TestAResolvedRecipientsReminderIsSuppressedPostgreSQL(t *testing.T) {
	fixture := dueAtT0Plus5m(t, 2)
	store := fixture.store()
	fixture.schedule(t, reminderT0.Add(notificationevent.UrgentReminderInterval), 10)
	fixture.resolve(t, fixture.recipients[0], "responded")

	retired, err := store.SuppressResolvedReminders(t.Context())
	if err != nil {
		t.Fatalf("SuppressResolvedReminders: %v", err)
	}
	if retired != 1 {
		t.Fatalf("retired %d reminders, want the one whose recipient answered", retired)
	}

	var status, reason string
	if err := fixture.pool.QueryRow(t.Context(), `
		SELECT status, COALESCE(suppressed_reason, '')
		FROM chat.notification_outbox
		WHERE message_id = $1::uuid AND recipient_user_id = $2::uuid AND kind = 'urgent_reminder'`,
		fixture.messageID, fixture.recipients[0],
	).Scan(&status, &reason); err != nil {
		t.Fatalf("read retired reminder: %v", err)
	}
	if status != string(notificationevent.StateSuppressed) {
		t.Fatalf("status = %q, want suppressed", status)
	}
	if reason != "recipient_resolved" {
		t.Fatalf("reason = %q, want the operational code", reason)
	}
	if strings.ContainsAny(reason, " ") {
		t.Fatalf("suppression reason %q is prose, not a code", reason)
	}
}

// A reminder whose recipient is still pending is left alone. A cleanup that
// retired live work would be worse than no cleanup at all.
func TestSuppressingResolvedRemindersSparesTheLiveOnesPostgreSQL(t *testing.T) {
	fixture := dueAtT0Plus5m(t, 2)
	store := fixture.store()
	fixture.schedule(t, reminderT0.Add(notificationevent.UrgentReminderInterval), 10)

	retired, err := store.SuppressResolvedReminders(t.Context())
	if err != nil {
		t.Fatalf("SuppressResolvedReminders: %v", err)
	}
	if retired != 0 {
		t.Fatalf("retired %d reminders whose recipients are still waiting", retired)
	}
}

// An ordinary notification is untouched by any of this: the kind test in the
// claim short-circuits, so a mention is claimed exactly as it always was.
func TestOrdinaryNotificationsAreUnaffectedByTheReminderPredicatePostgreSQL(t *testing.T) {
	fixture := seedOutbox(t, notificationevent.StateEligible, 2)

	claimed, err := storage.NewPGXNotificationOutboxStore(fixture.pool, false).
		ClaimDue(t.Context(), 10, 5, time.Hour)
	if err != nil {
		t.Fatalf("ClaimDue: %v", err)
	}
	if len(claimed) < 2 {
		t.Fatalf("claimed %d ordinary notifications, want both", len(claimed))
	}
}

// reminderID is the id of this message's single reminder row.
func (f *reminderFixture) reminderID(t *testing.T) string {
	t.Helper()
	var id string
	if err := f.pool.QueryRow(t.Context(), `
		SELECT id::text FROM chat.notification_outbox
		WHERE message_id = $1::uuid AND kind = 'urgent_reminder'
		ORDER BY created_at LIMIT 1`, f.messageID).Scan(&id); err != nil {
		t.Fatalf("read reminder id: %v", err)
	}
	return id
}

// The SQL builder and the Go builder agree when a real database evaluates the
// expression. They are written separately — the scheduler's fan-out is set-based
// — so a format that drifted would silently stop deduplicating.
func TestUrgentReminderDedupeKeyMatchesSQLPostgreSQL(t *testing.T) {
	pool := newNotificationTestPool(t)
	messageID := "74100000-0000-4000-8000-0000000008a1"

	var fromSQL string
	expression := notificationevent.UrgentReminderDedupeKeySQL("m.id", "m.occurrence")
	if err := pool.QueryRow(t.Context(),
		`SELECT `+expression+` FROM (SELECT $1::uuid AS id, 7 AS occurrence) m`,
		messageID).Scan(&fromSQL); err != nil {
		t.Fatalf("evaluate dedupe expression: %v", err)
	}

	fromGo, err := notificationevent.Identity{
		WorkspaceID: "ws", RecipientID: "user",
		EventType:  notificationevent.EventTypeUrgentReminder,
		SourceType: notificationevent.SourceTypeMessage,
		SourceID:   messageID, Discriminator: "7",
	}.DedupeKey()
	if err != nil {
		t.Fatalf("DedupeKey: %v", err)
	}
	if fromSQL != fromGo {
		t.Fatalf("SQL produced %q, Go produced %q", fromSQL, fromGo)
	}
}

// ── the claim's linearization point (issue #825, code review round 2) ────────
//
// The defect these prove is gone: ClaimDue used to read the recipient's state
// through an unlocked EXISTS, so the claim was a check-then-act across two
// tables. A transition committing between the read and the UPDATE left the
// reminder in 'processing' against a recipient who had already answered — and
// 'processing' has no path to 'suppressed', so nothing could take it back.
//
// Two real connections, two real transactions, and the interleaving is forced by
// the transactions themselves rather than by sleeping: Tx B's statement blocks
// on a lock, which is the whole point, so there is nothing to time.

// beginTx opens a transaction on its own connection and rolls it back at the end
// of the test, so a test that fails mid-interleaving cannot leave a lock behind.
func (f *reminderFixture) beginTx(t *testing.T) pgx.Tx {
	t.Helper()
	tx, err := f.pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	return tx
}

// claimIn runs the real ClaimDue inside the given transaction and reports
// whether this fixture's reminder was among what it took.
//
// The production method, not a copy of its SQL: pgx.Tx satisfies storage.Pool,
// so the store can be built over a transaction and the statement under test is
// literally the one the worker runs. A test that restated the query would prove
// the restatement correct and nothing else.
func (f *reminderFixture) claimIn(t *testing.T, tx pgx.Tx) bool {
	t.Helper()
	// A bounded deadline on purpose: a claim that ever waits on a lock it cannot
	// get fails here as a timeout instead of hanging the suite, which is also how
	// a deadlock between the two lock orders would surface.
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	claimed, err := storage.NewPGXNotificationOutboxStore(tx, false).ClaimDue(ctx, 10, 5, time.Hour)
	if err != nil {
		t.Fatalf("ClaimDue: %v", err)
	}
	for _, event := range claimed {
		if event.SourceID == f.messageID &&
			event.EventType == string(notificationevent.EventTypeUrgentReminder) {
			return true
		}
	}
	return false
}

// CASE A — the terminal transition wins.
//
// Tx B moves the recipient out of PENDING and commits; Tx A then claims. The
// reminder must not be claimed, for any of the three ways a recipient stops
// waiting. With the old unlocked EXISTS this passed only by luck of snapshot
// timing; with the locking read it is decided by the row itself.
func TestReminderClaimLosesToACommittedTerminalTransitionPostgreSQL(t *testing.T) {
	for _, terminal := range []string{"acknowledged", "responded", "cancelled"} {
		t.Run(terminal, func(t *testing.T) {
			fixture := dueAtT0Plus5m(t, 1)
			fixture.schedule(t, reminderT0.Add(notificationevent.UrgentReminderInterval), 10)
			fixture.makeReminderClaimable(t)

			// Tx B: the recipient answers, in its own transaction, and commits.
			txB := fixture.beginTx(t)
			fixture.resolveIn(t, txB, terminal)
			if err := txB.Commit(t.Context()); err != nil {
				t.Fatalf("commit terminal transition: %v", err)
			}

			// Tx A: the worker claims afterwards.
			txA := fixture.beginTx(t)
			if fixture.claimIn(t, txA) {
				t.Fatalf("a reminder was claimed for a %s recipient", terminal)
			}
			if err := txA.Commit(t.Context()); err != nil {
				t.Fatalf("commit claim: %v", err)
			}
			fixture.assertOutboxStatus(t, string(notificationevent.StateEligible))
		})
	}
}

// CASE A' — the terminal transition is in flight, uncommitted.
//
// This is the interleaving the review described and the one an unlocked read
// gets wrong: Tx B holds the recipient row mid-transition, so Tx A's snapshot
// still shows PENDING. The claim must refuse it rather than act on a state
// somebody is in the middle of changing.
func TestReminderClaimRefusesARecipientBeingResolvedConcurrentlyPostgreSQL(t *testing.T) {
	fixture := dueAtT0Plus5m(t, 1)
	fixture.schedule(t, reminderT0.Add(notificationevent.UrgentReminderInterval), 10)
	fixture.makeReminderClaimable(t)

	// Tx B holds the recipient row and does not commit.
	txB := fixture.beginTx(t)
	fixture.resolveIn(t, txB, "acknowledged")

	// Tx A claims while Tx B is still open. SKIP LOCKED on the recipient row is
	// what makes this a refusal rather than a wait, so it returns immediately.
	txA := fixture.beginTx(t)
	if fixture.claimIn(t, txA) {
		t.Fatal("a reminder was claimed while its recipient was mid-transition")
	}
	if err := txA.Commit(t.Context()); err != nil {
		t.Fatalf("commit claim: %v", err)
	}

	if err := txB.Commit(t.Context()); err != nil {
		t.Fatalf("commit terminal transition: %v", err)
	}
	fixture.assertOutboxStatus(t, string(notificationevent.StateEligible))
	// And the cleanup can still reach it, because it was never claimed — which
	// is the property the old shape destroyed by claiming it first.
	retired, err := fixture.store().SuppressResolvedReminders(t.Context())
	if err != nil {
		t.Fatalf("SuppressResolvedReminders: %v", err)
	}
	if retired != 1 {
		t.Fatalf("retired %d, want the reminder whose recipient answered", retired)
	}
}

// CASE B — the claim wins, and the transition serializes after it.
//
// Tx A claims while the recipient is PENDING, holding the recipient row. Tx B's
// transition must then wait for Tx A rather than proceeding on a state Tx A has
// already acted on. Once Tx A commits, Tx B re-evaluates and applies — the
// recipient is still PENDING, because a claim does not resolve anybody.
//
// The wait is the assertion: Tx B is launched, observed not to have finished,
// and only completes after Tx A commits. That ordering is what makes the two
// operations serializable rather than concurrent.
func TestReminderClaimHoldsTheLinearizationPointAgainstATransitionPostgreSQL(t *testing.T) {
	fixture := dueAtT0Plus5m(t, 1)
	fixture.schedule(t, reminderT0.Add(notificationevent.UrgentReminderInterval), 10)
	fixture.makeReminderClaimable(t)

	// Tx A: claim, and keep the transaction open so the lock is still held.
	txA := fixture.beginTx(t)
	if !fixture.claimIn(t, txA) {
		t.Fatal("the reminder was not claimed while its recipient was pending")
	}

	// Tx B: the recipient answers. It must block on Tx A's row lock.
	blocked := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		conn, err := fixture.pool.Acquire(ctx)
		if err != nil {
			blocked <- err
			return
		}
		defer conn.Release()
		_, err = conn.Exec(ctx, `
			UPDATE chat.message_acknowledgements
			SET state = 'acknowledged', resolved_at = now(), next_reminder_at = NULL
			WHERE message_id = $1::uuid AND state = 'pending'`, fixture.messageID)
		blocked <- err
	}()

	// It has not finished, because it cannot: Tx A holds the row.
	select {
	case err := <-blocked:
		t.Fatalf("the transition did not wait for the claim (err=%v)", err)
	case <-time.After(500 * time.Millisecond):
	}

	if err := txA.Commit(t.Context()); err != nil {
		t.Fatalf("commit claim: %v", err)
	}

	// Now it proceeds, and succeeds: a claim resolves nobody, so the recipient
	// was still PENDING when Tx B finally got the row.
	select {
	case err := <-blocked:
		if err != nil {
			t.Fatalf("the transition failed after the claim committed: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the transition never completed after the claim committed")
	}

	fixture.assertOutboxStatus(t, string(notificationevent.StateProcessing))
	if got := fixture.scheduleOf(t, fixture.recipients[0]).State; got != "acknowledged" {
		t.Fatalf("recipient state = %q, want acknowledged", got)
	}
}

// makeReminderClaimable puts the scheduled reminder into the state the worker
// claims from: evaluated, eligible, and due.
func (f *reminderFixture) makeReminderClaimable(t *testing.T) {
	t.Helper()
	if err := f.store().MarkEvaluated(t.Context(), f.reminderID(t),
		notificationevent.StateEligible, ""); err != nil {
		t.Fatalf("MarkEvaluated: %v", err)
	}
}

// resolveIn applies a PENDING -> terminal transition inside the given
// transaction, exactly as chat-service's own statements do: a conditional UPDATE
// on the recipient's row, which is the row the claim locks.
func (f *reminderFixture) resolveIn(t *testing.T, tx pgx.Tx, state string) {
	t.Helper()
	tag, err := tx.Exec(t.Context(), `
		UPDATE chat.message_acknowledgements
		SET state = $2, resolved_at = now(), next_reminder_at = NULL
		WHERE message_id = $1::uuid AND state = 'pending'`, f.messageID, state)
	if err != nil {
		t.Fatalf("resolve to %s: %v", state, err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("resolve to %s changed %d rows, want 1", state, tag.RowsAffected())
	}
}

func (f *reminderFixture) assertOutboxStatus(t *testing.T, want string) {
	t.Helper()
	var status string
	if err := f.pool.QueryRow(t.Context(), `
		SELECT status FROM chat.notification_outbox
		WHERE message_id = $1::uuid AND kind = 'urgent_reminder'`,
		f.messageID).Scan(&status); err != nil {
		t.Fatalf("read outbox status: %v", err)
	}
	if status != want {
		t.Fatalf("outbox status = %q, want %q", status, want)
	}
}
