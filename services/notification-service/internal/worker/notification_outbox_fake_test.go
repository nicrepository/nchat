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
			DedupeKey:   "message:msg-" + id + ":mention",
			OccurredAt:  f.now(),
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
