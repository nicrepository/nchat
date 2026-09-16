package worker

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/nicrepository/nchat/services/notification-service/internal/domain"
	"github.com/nicrepository/nchat/services/notification-service/internal/storage"
)

// The fakes the Web Push delivery tests are written against (issue #746).
//
// A store that keeps the two facts the real one keeps — which endpoints exist
// and which have already been reached — and a sender that answers by endpoint
// and counts every call. Between them they make the two properties that matter
// observable without a database or a network: what the fan-out selected, and
// how many times a provider was reached.

// ---------------------------------------------------------------------------
// The store
// ---------------------------------------------------------------------------

// fakePushStore implements storage.PushDeliveryStore over maps.
//
// The ledger is modelled as the real one is — a set keyed by (notification,
// subscription) — so a test cannot accidentally prove idempotency against a
// fake that deduplicates differently from the primary key that actually does it.
type fakePushStore struct {
	mu sync.Mutex

	// active is every subscription, keyed by subscription id, in the order
	// added. status is what the real table's status column holds.
	targets  []storage.PushTarget
	statuses map[string]domain.Status

	// ledger holds "notification|subscription" for every recorded delivery.
	ledger map[string]int64

	// outcomes records every lifecycle write in order, for tests that assert
	// what a subscription was told about an attempt.
	outcomes []recordedOutcome

	listErr    error
	ledgerErr  error
	outcomeErr error
	// ledgerFailures and outcomeFailures make the next N calls fail and every
	// call after that succeed, which is what a database blip looks like from
	// here. Counted rather than timed: a test that slept would be proving
	// something about the clock instead of about the recovery.
	ledgerFailures  int
	outcomeFailures int
	// ledgerCalls and outcomeCalls count every attempt, failed ones included,
	// so a test can prove that the *write* was retried and the push was not.
	ledgerCalls  int
	outcomeCalls int
	// beforeOutcome runs inside RecordDelivery, once, holding the lock: it is
	// how a test lands a change in the window between the send and its answer
	// being recorded. That is exactly where the real race is, so the
	// interleaving is deterministic and needs no goroutine and no sleep.
	beforeOutcome func()
}

type recordedOutcome struct {
	subscriptionID string
	generation     int64
	result         domain.DeliveryResult
}

func newFakePushStore() *fakePushStore {
	return &fakePushStore{
		statuses: map[string]domain.Status{},
		ledger:   map[string]int64{},
	}
}

// withTarget adds one active subscription.
func (f *fakePushStore) withTarget(id string) *fakePushStore {
	f.targets = append(f.targets, storage.PushTarget{
		SubscriptionID: id,
		Generation:     1,
		Endpoint:       "https://push.example.com/subscription/" + id,
		P256dh:         "p256dh-" + id,
		Auth:           "auth-" + id,
	})
	f.statuses[id] = domain.StatusActive
	return f
}

func (f *fakePushStore) withTargets(ids ...string) *fakePushStore {
	for _, id := range ids {
		f.withTarget(id)
	}
	return f
}

func ledgerKey(notificationID, subscriptionID string) string {
	return notificationID + "|" + subscriptionID
}

// delivered reports whether the ledger records this pair.
func (f *fakePushStore) delivered(notificationID, subscriptionID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.ledger[ledgerKey(notificationID, subscriptionID)]
	return ok
}

func (f *fakePushStore) ledgerSize() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.ledger)
}

func (f *fakePushStore) recordedOutcomes() []recordedOutcome {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedOutcome(nil), f.outcomes...)
}

// ListDeliverable is the fan-out: active subscriptions this notification has
// not already reached, in insertion order, exactly as the real ORDER BY does.
func (f *fakePushStore) ListDeliverable(
	_ context.Context, notificationID, _, _ string,
) ([]storage.PushTarget, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}

	deliverable := make([]storage.PushTarget, 0, len(f.targets))
	for _, target := range f.targets {
		if f.statuses[target.SubscriptionID] != domain.StatusActive {
			continue
		}
		if _, done := f.ledger[ledgerKey(notificationID, target.SubscriptionID)]; done {
			continue
		}
		deliverable = append(deliverable, target)
	}
	return deliverable, nil
}

func (f *fakePushStore) MarkDelivered(
	_ context.Context, notificationID, subscriptionID string, generation int64,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ledgerCalls++
	if f.ledgerFailures > 0 {
		f.ledgerFailures--
		return errFakeStore
	}
	if f.ledgerErr != nil {
		return f.ledgerErr
	}
	// ON CONFLICT DO NOTHING: the first record wins and a repeat writes nothing.
	if _, exists := f.ledger[ledgerKey(notificationID, subscriptionID)]; !exists {
		f.ledger[ledgerKey(notificationID, subscriptionID)] = generation
	}
	return nil
}

// RecordDelivery applies the lifecycle transition the real store applies, and
// classifies it the way the real one does — including the case this fake exists
// to make reachable: the compare-and-set matches nothing because the browser
// rotated, and the subscription is active again on a new generation.
func (f *fakePushStore) RecordDelivery(
	_ context.Context, subscriptionID string, generation int64, result domain.DeliveryResult,
) (domain.DeliveryApplication, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.outcomeCalls++
	if f.beforeOutcome != nil {
		hook := f.beforeOutcome
		f.beforeOutcome = nil
		hook()
	}
	if f.outcomeFailures > 0 {
		f.outcomeFailures--
		return domain.ApplicationMissing, errFakeStore
	}
	if f.outcomeErr != nil {
		return domain.ApplicationMissing, f.outcomeErr
	}
	f.outcomes = append(f.outcomes,
		recordedOutcome{subscriptionID: subscriptionID, generation: generation, result: result})

	current, exists := f.targetByID(subscriptionID)
	switch {
	case !exists:
		return domain.ApplicationMissing, nil
	case f.statuses[subscriptionID] != domain.StatusActive:
		return domain.ApplicationInactive, nil
	case current.Generation != generation:
		// The compare-and-set names a generation the row no longer has, and the
		// row is active: the browser re-registered while the attempt was in
		// flight, so there is a live endpoint that has had nothing.
		return domain.ApplicationSuperseded, nil
	}

	if result.Outcome == domain.OutcomeInvalidated {
		f.statuses[subscriptionID] = domain.StatusInvalid
	}
	return domain.ApplicationRecorded, nil
}

// targetByID finds a subscription by id, whatever generation it is on.
func (f *fakePushStore) targetByID(subscriptionID string) (storage.PushTarget, bool) {
	for _, target := range f.targets {
		if target.SubscriptionID == subscriptionID {
			return target, true
		}
	}
	return storage.PushTarget{}, false
}

// rotatesDuring makes the browser re-register while the next attempt is in
// flight, so the answer describes an endpoint the subscription no longer has.
func (f *fakePushStore) rotatesDuring(subscriptionID string) *fakePushStore {
	return f.during(func() { f.rotateLocked(subscriptionID) })
}

// disabledDuring and deletedDuring are the other two ways a subscription stops
// matching its compare-and-set mid-attempt.
func (f *fakePushStore) disabledDuring(subscriptionID string) *fakePushStore {
	return f.during(func() { f.statuses[subscriptionID] = domain.StatusDisabled })
}

func (f *fakePushStore) deletedDuring(subscriptionID string) *fakePushStore {
	return f.during(func() { f.removeLocked(subscriptionID) })
}

// during registers a change to apply once, inside the next lifecycle write.
func (f *fakePushStore) during(change func()) *fakePushStore {
	f.beforeOutcome = change
	return f
}

func (f *fakePushStore) rotateLocked(subscriptionID string) {
	for i := range f.targets {
		if f.targets[i].SubscriptionID != subscriptionID {
			continue
		}
		f.targets[i].Generation++
		f.statuses[subscriptionID] = domain.StatusActive
	}
}

// removeLocked deletes a subscription outright, which is what a cascade from
// the user or a cleanup would do.
func (f *fakePushStore) removeLocked(subscriptionID string) {
	kept := f.targets[:0]
	for _, target := range f.targets {
		if target.SubscriptionID != subscriptionID {
			kept = append(kept, target)
		}
	}
	f.targets = kept
	delete(f.statuses, subscriptionID)
}

// ---------------------------------------------------------------------------
// The sender
// ---------------------------------------------------------------------------

// fakeSender answers per endpoint and counts every call, including the ones a
// correct implementation must never make.
type fakeSender struct {
	mu sync.Mutex
	// answers maps a subscription id to the sequence of results it returns,
	// so a test can make one endpoint fail then succeed. The last entry repeats.
	answers map[string][]PushResult
	// calls counts sends per subscription id, and total counts all of them.
	calls   map[string]int
	total   int
	latency time.Duration
}

func newFakeSender() *fakeSender {
	return &fakeSender{answers: map[string][]PushResult{}, calls: map[string]int{}}
}

// answering fixes what one endpoint returns, forever.
func (s *fakeSender) answering(subscriptionID string, results ...PushResult) *fakeSender {
	s.answers[subscriptionID] = results
	return s
}

// delivering is the common case: everything succeeds.
func delivered() PushResult {
	return PushResult{Class: PushDelivered, StatusCode: 201}
}

func gone() PushResult {
	return PushResult{Class: PushSubscriptionGone, StatusCode: 410}
}

func notFound() PushResult {
	return PushResult{Class: PushSubscriptionGone, StatusCode: 404}
}

func unavailable() PushResult {
	return PushResult{Class: PushProviderUnavailable, StatusCode: 503}
}

func rateLimited(after time.Duration) PushResult {
	return PushResult{Class: PushRateLimited, StatusCode: 429, RetryAfter: after}
}

func rejected() PushResult {
	return PushResult{Class: PushInvalidRequest, StatusCode: 400}
}

// Send looks the endpoint up by the subscription id encoded in it, which is how
// the fake store builds them.
func (s *fakeSender) Send(_ context.Context, message PushMessage) PushResult {
	s.mu.Lock()
	defer s.mu.Unlock()

	id := subscriptionIDOf(message.Endpoint)
	s.total++
	s.calls[id]++

	answers := s.answers[id]
	if len(answers) == 0 {
		return withLatency(delivered(), s.latency)
	}
	if s.calls[id] <= len(answers) {
		return withLatency(answers[s.calls[id]-1], s.latency)
	}
	return withLatency(answers[len(answers)-1], s.latency)
}

func withLatency(result PushResult, latency time.Duration) PushResult {
	result.Latency = latency
	return result
}

func subscriptionIDOf(endpoint string) string {
	const prefix = "https://push.example.com/subscription/"
	if len(endpoint) <= len(prefix) {
		return ""
	}
	return endpoint[len(prefix):]
}

func (s *fakeSender) callsTo(subscriptionID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[subscriptionID]
}

func (s *fakeSender) totalCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.total
}

// errFakeStore is the database failure the store fakes report.
var errFakeStore = errors.New("fake store failure")

// failLedgerOnce makes the next ledger write fail and every later one succeed.
func (f *fakePushStore) failLedgerOnce() *fakePushStore {
	f.ledgerFailures = 1
	return f
}

// failLifecycleOnce makes the next subscription write fail and every later one
// succeed.
func (f *fakePushStore) failLifecycleOnce() *fakePushStore {
	f.outcomeFailures = 1
	return f
}

func (f *fakePushStore) ledgerAttempts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ledgerCalls
}

func (f *fakePushStore) lifecycleAttempts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.outcomeCalls
}

// status reads a subscription's current lifecycle state.
func (f *fakePushStore) status(subscriptionID string) domain.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.statuses[subscriptionID]
}
