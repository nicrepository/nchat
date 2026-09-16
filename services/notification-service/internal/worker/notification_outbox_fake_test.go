package worker

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/notificationevent"
	"github.com/nicrepository/nchat/services/notification-service/internal/storage"
)

// fakeOutbox is an in-memory chat.notification_outbox for the worker's tests.
//
// It is not a mock: it enforces the rules the real table enforces — the state
// machine's allowed transitions, the compare-and-set that makes a claim
// exclusive, the lease that makes an abandoned one recoverable — so a test can
// assert what the worker *did* rather than which methods it called. What it
// cannot prove is that the SQL implements the same rules; that is what
// notification_outbox_postgres_test.go in the storage package is for.
type fakeOutbox struct {
	mu    sync.Mutex
	rows  map[string]*fakeRow
	order []string

	// now is the clock the lease and the retry schedule are read against, so a
	// test can expire a lease without sleeping.
	now func() time.Time

	// failures maps an operation name to the error it should return, so the
	// worker's behaviour against a database that is refusing can be exercised.
	failures map[string]error

	// claims counts calls to ClaimDue, which is how "the idle worker does not
	// poll aggressively" is measured.
	claims int

	// The scripted halves of the reminder lifecycle (issue #825) and the counters
	// that prove the worker called them. See the section at the end of this file
	// for why these are scripted rather than simulated.
	reminderResult    storage.ReminderScheduleResult
	superseded        int
	reminderPasses    int
	supersedePasses   int
	reminderBatchSize int
	// reminderInstant is the reference the worker handed the scheduler, so a
	// test can assert the worker supplies one rather than letting the store
	// invent it (issue #825).
	reminderInstant time.Time
}

type fakeRow struct {
	event         storage.NotificationEvent
	state         notificationevent.State
	nextAttemptAt time.Time
	lastError     string
	reason        string
}

func newFakeOutbox() *fakeOutbox {
	return &fakeOutbox{
		rows:     map[string]*fakeRow{},
		now:      time.Now,
		failures: map[string]error{},
	}
}

// seedPending adds an event in the state every producer writes.
func (f *fakeOutbox) seedPending(id string) *fakeRow {
	return f.seed(id, notificationevent.StatePending)
}

// seedPendingMuted adds a pending event whose recipient has muted the
// conversation it happened in — the state the outbox projection resolves from
// chat.conversation_notification_prefs (issue #744).
func (f *fakeOutbox) seedPendingMuted(id string) *fakeRow {
	row := f.seedPending(id)
	f.mu.Lock()
	defer f.mu.Unlock()
	row.event.Muted = true
	return row
}

// seedPendingWithLevel adds an event whose recipient narrowed the conversation
// to a level (issue #136), with the event kind the level is judged against.
//
// The kind is a parameter because that is the whole of what the level decides
// on: the same preference allows a mention and suppresses an ordinary message,
// and a fixture that fixed the kind could only ever prove one of the two.
func (f *fakeOutbox) seedPendingWithLevel(id, level string, kind notificationevent.EventType) *fakeRow {
	row := f.seedPending(id)
	f.mu.Lock()
	defer f.mu.Unlock()
	row.event.NotificationLevel = level
	row.event.EventType = string(kind)
	return row
}

// seedEligible adds an event a policy has already approved.
//
// It carries an availability instant, because every claimable row does: that is
// what MarkEvaluated stamps on promotion, and a fixture that skipped it would be
// modelling a row production can no longer produce.
func (f *fakeOutbox) seedEligible(id string) *fakeRow {
	row := f.seed(id, notificationevent.StateEligible)
	f.mu.Lock()
	defer f.mu.Unlock()
	row.nextAttemptAt = f.now()
	return row
}

func (f *fakeOutbox) seed(id string, state notificationevent.State) *fakeRow {
	f.mu.Lock()
	defer f.mu.Unlock()
	row := &fakeRow{
		event: storage.NotificationEvent{
			ID:          id,
			WorkspaceID: "ws-1",
			RecipientID: "user-1",
			EventType:   "mention",
			Priority:    "high",
			SourceType:  "message",
			SourceID:    "msg-" + id,
			// Every row the producers write carries an origin, and the column
			// defaults to 'live'. A fixture without one would be a row the
			// policy is right to refuse, which is not what these tests are for.
			Origin:     "live",
			DedupeKey:  "message:msg-" + id + ":mention",
			OccurredAt: f.now(),
		},
		state: state,
	}
	f.rows[id] = row
	f.order = append(f.order, id)
	return row
}

func (f *fakeOutbox) snapshot(id string) fakeRow {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[id]
	if !ok {
		return fakeRow{}
	}
	return *row
}

func (f *fakeOutbox) claimCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.claims
}

func (f *fakeOutbox) fail(operation string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures[operation] = err
}

// failure reports the configured error for an operation. Callers hold the lock.
func (f *fakeOutbox) failure(operation string) error {
	return f.failures[operation]
}

func (f *fakeOutbox) ListPending(_ context.Context, limit int) ([]storage.NotificationEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failure("list"); err != nil {
		return nil, err
	}

	var pending []storage.NotificationEvent
	for _, id := range f.order {
		row := f.rows[id]
		if row.state == notificationevent.StatePending && len(pending) < limit {
			pending = append(pending, row.event)
		}
	}
	return pending, nil
}

func (f *fakeOutbox) MarkEvaluated(
	_ context.Context, id string, state notificationevent.State, reason string,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failure("evaluate"); err != nil {
		return err
	}

	row, ok := f.rows[id]
	if !ok || row.state != notificationevent.StatePending {
		return storage.ErrNotificationStateConflict
	}
	if !notificationevent.StatePending.CanTransitionTo(state) {
		return storage.ErrInvalidNotificationTransition
	}
	if err := notificationevent.ValidateSuppressedReason(state, reason); err != nil {
		return storage.ErrInvalidNotificationTransition
	}
	row.state = state
	row.reason = reason
	if state == notificationevent.StateEligible {
		// Becoming eligible is becoming available, and the instant is recorded
		// rather than inferred from when the event occurred.
		row.nextAttemptAt = f.now()
	}
	return nil
}

// ClaimDue is the fake's most important method: it reproduces the real
// statement's exclusivity, its lease, and its refusal to hand back work whose
// attempts are spent.
func (f *fakeOutbox) ClaimDue(
	_ context.Context, batchSize, maxAttempts int, lease time.Duration,
) ([]storage.NotificationEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claims++
	if err := f.failure("claim"); err != nil {
		return nil, err
	}

	now := f.now()
	var claimed []storage.NotificationEvent
	for _, id := range f.order {
		row := f.rows[id]
		if len(claimed) >= batchSize || !row.claimable(now, maxAttempts) {
			continue
		}
		row.state = notificationevent.StateProcessing
		row.event.Attempts++
		row.nextAttemptAt = now.Add(lease)
		claimed = append(claimed, row.event)
	}
	return claimed, nil
}

// claimable mirrors the WHERE clause of claimDueQuery.
func (r *fakeRow) claimable(now time.Time, maxAttempts int) bool {
	switch r.state {
	case notificationevent.StateEligible, notificationevent.StateRetrying, notificationevent.StateProcessing:
	default:
		return false
	}
	if r.event.Attempts >= maxAttempts {
		return false
	}
	// An unset availability instant is not due. Mirrors the SQL, where a NULL
	// next_attempt_at fails the comparison rather than sorting to the front.
	return !r.nextAttemptAt.IsZero() && !r.nextAttemptAt.After(now)
}

func (f *fakeOutbox) MarkDelivered(_ context.Context, id string, attempt int) error {
	return f.endClaim(id, attempt, notificationevent.StateSent, "", 0)
}

func (f *fakeOutbox) ScheduleRetry(
	_ context.Context, id string, attempt int, delay time.Duration, category string,
) error {
	return f.endClaim(id, attempt, notificationevent.StateRetrying, category, delay)
}

func (f *fakeOutbox) MarkFailed(_ context.Context, id string, attempt int, category string) error {
	return f.endClaim(id, attempt, notificationevent.StateFailed, category, 0)
}

// endClaim is the compare-and-set every finalisation performs: it applies only
// while the row is still in the *generation* this worker claimed.
//
// The attempts check is the half that matters. Matching on 'processing' alone
// would let a worker whose lease expired finalise the claim that superseded it,
// and the state would be identical in both cases — which is precisely the race
// this models.
func (f *fakeOutbox) endClaim(
	id string, attempt int, state notificationevent.State, category string, delay time.Duration,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failure("finalise"); err != nil {
		return err
	}

	row, ok := f.rows[id]
	if !ok || row.state != notificationevent.StateProcessing || row.event.Attempts != attempt {
		return storage.ErrNotificationStateConflict
	}
	row.state = state
	row.lastError = category
	row.nextAttemptAt = time.Time{}
	if delay > 0 {
		row.nextAttemptAt = f.now().Add(delay)
	}
	return nil
}

func (f *fakeOutbox) FailExhausted(_ context.Context, maxAttempts int) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failure("exhaust"); err != nil {
		return 0, err
	}

	now := f.now()
	retired := 0
	for _, id := range f.order {
		row := f.rows[id]
		if row.state != notificationevent.StateProcessing || row.event.Attempts < maxAttempts {
			continue
		}
		if row.nextAttemptAt.IsZero() || row.nextAttemptAt.After(now) {
			continue
		}
		row.state = notificationevent.StateFailed
		row.lastError = "attempts_exhausted"
		row.nextAttemptAt = time.Time{}
		retired++
	}
	return retired, nil
}

func (f *fakeOutbox) Backlog(_ context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failure("backlog"); err != nil {
		return 0, err
	}

	backlog := 0
	for _, row := range f.rows {
		switch row.state {
		case notificationevent.StatePending, notificationevent.StateEligible, notificationevent.StateRetrying:
			backlog++
		}
	}
	return backlog, nil
}

// errStoreUnavailable stands in for a database that is refusing.
var errStoreUnavailable = errors.New("store unavailable")

// ── Persistent reminders (issue #825) ────────────────────────────────────────
//
// Scripted rather than simulated, unlike the claim protocol above, and the
// distinction is deliberate. What the worker owes this feature is narrow: call
// the two statements once per pass, count what they report, say so once rather
// than once per recipient, and keep draining when either fails. Every rule that
// actually decides a reminder — the five-minute window, only-pending
// eligibility, the unique index that makes a repeat a no-op, the ceiling that
// produces EXPIRED — is a property of the statements themselves and is proved
// against a real PostgreSQL in the storage package. Reimplementing them here
// would be a second implementation for the tests to agree with, which is how a
// fake ends up testing itself.

// reminderResult is what ScheduleDueReminders reports, set by a test.
func (f *fakeOutbox) scheduleReminderResult(result storage.ReminderScheduleResult) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reminderResult = result
}

// supersededReminders is what SuppressResolvedReminders reports, set by a test.
func (f *fakeOutbox) supersededReminders(count int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.superseded = count
}

func (f *fakeOutbox) ScheduleDueReminders(
	_ context.Context, now time.Time, batchSize int,
) (storage.ReminderScheduleResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reminderPasses++
	f.reminderBatchSize = batchSize
	f.reminderInstant = now
	if err := f.failure("schedule_reminders"); err != nil {
		return storage.ReminderScheduleResult{}, err
	}
	return f.reminderResult, nil
}

func (f *fakeOutbox) SuppressResolvedReminders(_ context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.supersedePasses++
	if err := f.failure("suppress_resolved_reminders"); err != nil {
		return 0, err
	}
	return f.superseded, nil
}
