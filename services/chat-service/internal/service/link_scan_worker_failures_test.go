package service_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/observability"
	"github.com/nicrepository/nchat/libs/go/platform/urlsafety"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// What the worker does when the parts around it fail.
//
// The single property every case here asserts is the same one: a failure never
// releases a withheld message and never buys a second scan. A pass that cannot
// read the backlog, cannot prune, cannot resolve or cannot deliver still
// completes, leaves the rows where they are, and lets the next pass try again —
// which is what makes "the database blipped" cost one interval instead of a
// message published unchecked or a duplicate submission.

var errBoom = errors.New("boom")

// The pass is best-effort in its housekeeping. None of these failures is a
// reason to abandon work that succeeded, and none of them decides a message.
func TestHousekeepingFailuresDoNotFailThePass(t *testing.T) {
	queue := newFakeQueue()
	queue.reopenErr = errBoom
	queue.pruneErr = errBoom

	moved, err := worker(queue, &fakeProvider{}, nil).ProcessDue(t.Context())

	if err != nil || moved != 0 {
		t.Fatalf("moved=%d err=%v, want a pass that survived its housekeeping", moved, err)
	}
	if queue.reopens != 1 || queue.prunes != 1 {
		t.Fatalf("reopens=%d prunes=%d, want both attempted once", queue.reopens, queue.prunes)
	}
}

// A backlog read is a sample, not work. Losing it must cost a gauge and nothing
// else.
func TestBacklogFailuresCostOnlyTheSample(t *testing.T) {
	reporter := urlsafety.NewPipelineMetrics(observability.NewMetrics(observability.Config{
		ServiceName: "chat-service", MetricsEnabled: true,
	}), "chat-service")

	for name, prepare := range map[string]func(*fakeQueue){
		"the scan backlog is unreadable":   func(q *fakeQueue) { q.backlogErr = errBoom },
		"the outbox backlog is unreadable": func(q *fakeQueue) { q.outboxErr = errBoom },
		"both backlogs read successfully":  func(q *fakeQueue) {},
	} {
		t.Run(name, func(t *testing.T) {
			queue := newFakeQueue()
			prepare(queue)
			svc := worker(queue, &fakeProvider{}, nil)
			svc.SetMetrics(reporter)

			if _, err := svc.ProcessDue(t.Context()); err != nil {
				t.Fatalf("ProcessDue: %v", err)
			}
		})
	}
}

// Not being able to record the intent means nothing may be submitted: a
// submission the database does not know about is the unrecoverable state the
// ordering exists to prevent.
func TestNothingIsSubmittedWhenTheIntentCannotBeRecorded(t *testing.T) {
	for name, prepare := range map[string]func(*fakeQueue){
		"the intent write fails":   func(q *fakeQueue) { q.beginErr = errBoom },
		"another worker won first": func(q *fakeQueue) { q.beginConflict = true },
	} {
		t.Run(name, func(t *testing.T) {
			queue := newFakeQueue(storage.LinkScanJob{CanonicalURL: "https://example.com/x"})
			prepare(queue)
			provider := &fakeProvider{}

			if _, err := worker(queue, provider, nil).ProcessDue(t.Context()); err != nil {
				t.Fatalf("ProcessDue: %v", err)
			}
			if submits, _ := provider.counts(); submits != 0 {
				t.Fatalf("submits=%d, want the provider never called", submits)
			}
		})
	}
}

// A verdict that cannot be written is not a verdict. The row stays pending and
// the message stays withheld.
func TestAVerdictThatCannotBeWrittenReleasesNothing(t *testing.T) {
	queue := newFakeQueue(storage.LinkScanJob{
		CanonicalURL: "https://example.com/x", ScanUUID: "scan-1",
	})
	queue.verdictErr = errBoom
	provider := &fakeProvider{verdict: urlsafety.VerdictSafe}

	if _, err := worker(queue, provider, nil).ProcessDue(t.Context()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	if _, verdicts := queue.snapshot(); len(verdicts) != 0 {
		t.Fatalf("a failed write left a verdict behind: %v", verdicts)
	}
}

// The promotion pass failing is the one failure that must be loud: it is the
// step that decides messages.
func TestAFailedResolveStillDispatchesWhatWasAlreadyQueued(t *testing.T) {
	queue := newFakeQueue()
	queue.resolveErr = errBoom
	queue.events = []storage.PublishEvent{{
		MessageID: "m-1", WorkspaceID: "ws-1", EventType: storage.EventMessageCreated,
		TargetType: "channel", TargetID: "ch-1",
	}}
	publisher := &fakePublisher{}

	if _, err := worker(queue, &fakeProvider{}, publisher).ProcessDue(t.Context()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	// Events already in the outbox are a separate, retryable step, so a failed
	// resolve must not strand them.
	if got := queue.deliveredEvents(); len(got) != 1 || got[0] != "m-1" {
		t.Fatalf("delivered=%v, want the queued event dispatched anyway", got)
	}
}

// An unreadable outbox leaves every event pending. Nothing is retired, so
// nothing is lost.
func TestAnUnreadableOutboxRetiresNothing(t *testing.T) {
	queue := newFakeQueue()
	queue.claimEventsErr = errBoom

	if _, err := worker(queue, &fakeProvider{}, &fakePublisher{}).ProcessDue(t.Context()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	if got := queue.deliveredEvents(); len(got) != 0 {
		t.Fatalf("delivered=%v, want nothing retired", got)
	}
}

// With no publisher of either kind wired there is nothing to deliver into, so
// the outbox is not even read — the events stay for a deployment that gains one.
func TestNoPublisherLeavesTheOutboxUntouched(t *testing.T) {
	queue := newFakeQueue()
	queue.events = []storage.PublishEvent{{MessageID: "m-1", EventType: storage.EventMessageCreated}}

	if _, err := worker(queue, &fakeProvider{}, nil).ProcessDue(t.Context()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	if len(queue.deliveredEvents()) != 0 || len(queue.cancelledEvents()) != 0 {
		t.Fatal("an event was retired with nothing wired to deliver it")
	}
}

// A refusal with no sender channel wired is left pending rather than retired: a
// deployment that gains the publisher later still delivers it.
func TestARefusalWithNoBlockedPublisherIsLeftPending(t *testing.T) {
	queue := newFakeQueue()
	queue.events = []storage.PublishEvent{{
		MessageID: "m-1", WorkspaceID: "ws-1", EventType: storage.EventMessageBlocked,
		TargetType: storage.TargetSender, TargetID: "u-1",
	}}

	if _, err := worker(queue, &fakeProvider{}, &fakePublisher{}).ProcessDue(t.Context()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	if got := queue.deliveredEvents(); len(got) != 0 {
		t.Fatalf("delivered=%v, want the refusal held for a later publisher", got)
	}
}

// Cancelling is a write like any other. Failing it leaves the event pending, so
// the next pass cancels it instead of losing the fact.
func TestAFailedCancellationLeavesTheEventPending(t *testing.T) {
	queue := newFakeQueue()
	queue.cancelErr = errBoom
	queue.events = []storage.PublishEvent{{
		MessageID: "m-1", WorkspaceID: "ws-1", EventType: storage.EventMessageCreated,
		TargetType: "channel", TargetID: "ch-1", Cancelled: true,
	}}

	if _, err := worker(queue, &fakeProvider{}, &fakePublisher{}).ProcessDue(t.Context()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	if got := queue.cancelledEvents(); len(got) != 0 {
		t.Fatalf("cancelled=%v, want the failed retirement to have changed nothing", got)
	}
}

// A cancelled context stops the pass where it is. Nothing is delivered after
// shutdown begins, and the failure is not logged as one.
func TestAPassStopsWhenItsContextEnds(t *testing.T) {
	queue := newFakeQueue()
	queue.events = []storage.PublishEvent{
		{MessageID: "m-1", WorkspaceID: "ws-1", EventType: storage.EventMessageCreated,
			TargetType: "channel", TargetID: "ch-1"},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := worker(queue, &fakeProvider{}, &fakePublisher{}).ProcessDue(ctx); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	if got := queue.deliveredEvents(); len(got) != 0 {
		t.Fatalf("delivered=%v after shutdown", got)
	}
}

// Reconciliation with a provider client that cannot search: the attempt can only
// be settled by the horizon, and it is never resubmitted.
func TestAnUncertainAttemptIsNotResubmittedWithoutASearcher(t *testing.T) {
	queue := newFakeQueue(storage.LinkScanJob{
		CanonicalURL:    "https://example.com/x",
		SubmitStartedAt: time.Now().Add(-2 * time.Hour),
	})
	provider := &fakeProvider{}
	svc := worker(queue, provider, nil)
	// A zero timeout is not "no horizon": it is replaced by the default, so the
	// reporting threshold can never be switched off by configuration.
	svc.SetCapacity(service.LinkScanWorkerCapacity{})

	if _, err := svc.ProcessDue(t.Context()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	if submits, _ := provider.counts(); submits != 0 {
		t.Fatalf("submits=%d, want an uncertain attempt never resubmitted", submits)
	}
}

// Several eligible scans means an earlier duplicate probably exists. The newest
// is adopted either way — what must not happen is a third submission.
func TestAnAmbiguousSearchAdoptsTheNewestScan(t *testing.T) {
	queue := newFakeQueue(storage.LinkScanJob{
		CanonicalURL: "https://example.com/x", SubmitStartedAt: time.Now().Add(-time.Minute),
	})
	provider := &searchingProvider{fakeProvider: &fakeProvider{}, answers: []searchAnswer{
		{record: urlsafety.ScanRecord{UUID: "scan-newest"}, match: 3},
	}}

	if _, err := searchWorker(queue, provider).ProcessDue(t.Context()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	submitted, _ := queue.snapshot()
	if submitted["https://example.com/x"] != "scan-newest" {
		t.Fatalf("submitted=%v, want the newest eligible scan adopted", submitted)
	}
	if submits, _ := provider.counts(); submits != 0 {
		t.Fatalf("submits=%d, want no submission during reconciliation", submits)
	}
}

// The row moved on while the search ran: somebody else resolved it, and this
// worker's recovered id is not the one that counts.
func TestAnAdoptionThatLostTheRaceChangesNothing(t *testing.T) {
	queue := newFakeQueue(storage.LinkScanJob{
		CanonicalURL: "https://example.com/x", SubmitStartedAt: time.Now().Add(-time.Minute),
	})
	queue.adoptConflict = true
	provider := &searchingProvider{fakeProvider: &fakeProvider{}, answers: []searchAnswer{
		{record: urlsafety.ScanRecord{UUID: "scan-late"}, match: 1},
	}}

	if _, err := searchWorker(queue, provider).ProcessDue(t.Context()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	submitted, _ := queue.snapshot()
	if len(submitted) != 0 {
		t.Fatalf("submitted=%v, want the lost race to have written nothing", submitted)
	}
}

// The loop's own defaults: no interval and no logger are supported deployments,
// and a pass that moves work is reported.
func TestWorkerLoopRunsWithDefaults(t *testing.T) {
	queue := newFakeQueue(storage.LinkScanJob{CanonicalURL: "https://example.com/x"})
	svc := worker(queue, &fakeProvider{scanID: "scan-1"}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		// A non-positive interval falls back to the package default rather than
		// spinning, and a nil logger falls back to the default handler.
		service.RunLinkScanWorker(ctx, svc, 0, nil)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the worker loop did not stop with its context")
	}
}

// One tick, end to end through the loop, with both outcomes a pass can report.
func TestWorkerLoopReportsEachPass(t *testing.T) {
	for name, queue := range map[string]*fakeQueue{
		"a pass that moved work": newFakeQueue(
			storage.LinkScanJob{CanonicalURL: "https://example.com/x"}),
		"a pass that failed": func() *fakeQueue {
			q := newFakeQueue()
			q.claimErr = errBoom
			return q
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			svc := worker(queue, &fakeProvider{scanID: "scan-1"}, nil)
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			service.RunLinkScanWorker(ctx, svc, 10*time.Millisecond,
				slog.New(slog.DiscardHandler))
			if queue.claims == 0 {
				t.Fatal("the loop never ran a pass")
			}
		})
	}
}
