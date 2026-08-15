package service_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/observability"

	"github.com/nicrepository/nchat/libs/go/platform/urlsafety"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// The RF-21 worker: the half that actually talks to Cloudflare, outside every
// user's request.
//
// What these assert is the state machine, not the provider — the provider's own
// contract is pinned in libs/go/platform/urlsafety. Here the questions are: does
// one pass move each URL exactly one step, does a failure leave the row pending
// rather than releasing the message, and is a promoted message published once.

// fakeQueue is the durable half, in memory.
type fakeQueue struct {
	mu sync.Mutex

	jobs       []storage.LinkScanJob
	claimErr   error
	claims     int
	submitted  map[string]string
	verdicts   map[string]urlsafety.Verdict
	boundScan  string
	summary    storage.ResolveSummary
	resolveErr error
	resolves   int
	// submitConflict / verdictConflict make the compare-and-set lose, which is
	// how a stale worker's write is simulated without a second goroutine.
	submitConflict  bool
	verdictConflict bool
	events          []storage.PublishEvent
	claimEventsErr  error
	published       []string
	cancelled       []string
	reopened        int
	reopens         int
	markErr         error

	begun            []string
	beginConflict    bool
	beginErr         error
	persistCalls     int
	persistErr       error
	adopted          []string
	adoptConflict    bool
	resubmitsAllowed int
	resubmitCleared  []string
	providerReserved int
	reserveErr       error
	prunes           int
}

func newFakeQueue(jobs ...storage.LinkScanJob) *fakeQueue {
	return &fakeQueue{
		jobs:      jobs,
		submitted: map[string]string{},
		verdicts:  map[string]urlsafety.Verdict{},
	}
}

// ClaimDueLinkScans hands out one job per call, which is what the worker now
// asks for: leasing a batch up front is what let a slow provider have rows
// reclaimed while they were still being processed.
func (q *fakeQueue) ClaimDueLinkScans(_ context.Context, batchSize int) ([]storage.LinkScanJob, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.claims++
	if q.claimErr != nil {
		return nil, q.claimErr
	}
	if len(q.jobs) == 0 {
		return nil, nil
	}
	if batchSize > len(q.jobs) {
		batchSize = len(q.jobs)
	}
	claimed := q.jobs[:batchSize]
	q.jobs = q.jobs[batchSize:]
	return claimed, nil
}

func (q *fakeQueue) BeginLinkScanSubmit(_ context.Context, canonicalURL string, generation int) (int, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.begun = append(q.begun, canonicalURL)
	if q.beginConflict {
		return 0, storage.ErrLinkScanConflict
	}
	if q.beginErr != nil {
		return 0, q.beginErr
	}
	return generation + 1, nil
}

func (q *fakeQueue) RecordLinkScanSubmission(
	_ context.Context, canonicalURL, scanUUID string, _ int,
) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.persistCalls++
	if q.submitConflict {
		return storage.ErrLinkScanConflict
	}
	if q.persistErr != nil {
		return q.persistErr
	}
	q.submitted[canonicalURL] = scanUUID
	return nil
}

func (q *fakeQueue) AdoptScanUUID(_ context.Context, canonicalURL, scanUUID string, _ int) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.adoptConflict {
		return storage.ErrLinkScanConflict
	}
	q.adopted = append(q.adopted, scanUUID)
	q.submitted[canonicalURL] = scanUUID
	return nil
}

// ReserveProviderSubmit hands out providerCapacity submissions and then refuses,
// which is how a spent shared window is simulated without a clock.
func (q *fakeQueue) ReserveProviderSubmit(_ context.Context, limit int, _ time.Duration) (bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.reserveErr != nil {
		return false, q.reserveErr
	}
	if limit <= 0 {
		return true, nil
	}
	if q.providerReserved >= limit {
		return false, nil
	}
	q.providerReserved++
	return true, nil
}

func (q *fakeQueue) PruneLinkScanBudget(_ context.Context, _ time.Duration) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.prunes++
	return nil
}

func (q *fakeQueue) RecordLinkVerdict(_ context.Context, canonicalURL, scanUUID string, verdict urlsafety.Verdict) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.verdictConflict {
		// The row moved on to a different scan while this worker was polling, so
		// its answer describes a scan nobody is waiting on.
		return storage.ErrLinkScanConflict
	}
	q.verdicts[canonicalURL] = verdict
	q.boundScan = scanUUID
	return nil
}

func (q *fakeQueue) ResolveDecidedMessages(_ context.Context) (storage.ResolveSummary, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.resolves++
	if q.resolveErr != nil {
		return storage.ResolveSummary{}, q.resolveErr
	}
	summary := q.summary
	q.summary = storage.ResolveSummary{}
	return summary, nil
}

// ClaimPublishEvents hands out the outbox rows once. A second pass finds
// nothing, which is what "delivered" looks like from the worker's side.
func (q *fakeQueue) ClaimPublishEvents(_ context.Context, _ int) ([]storage.PublishEvent, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.claimEventsErr != nil {
		return nil, q.claimEventsErr
	}
	events := q.events
	q.events = nil
	return events, nil
}

func (q *fakeQueue) MarkPublished(_ context.Context, messageID string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.markErr != nil {
		return q.markErr
	}
	q.published = append(q.published, messageID)
	return nil
}

func (q *fakeQueue) ReopenExpiredVerdicts(_ context.Context) (int, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.reopens++
	return q.reopened, nil
}

func (q *fakeQueue) CancelPublishEvent(_ context.Context, messageID string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.cancelled = append(q.cancelled, messageID)
	return nil
}

func (q *fakeQueue) PublishOutboxBacklog(_ context.Context) (int, time.Duration, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.events), 0, nil
}

func (q *fakeQueue) LinkScanBacklog(_ context.Context) (map[string]int, time.Duration, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return map[string]int{urlsafety.StateSubmitPending: len(q.jobs)}, 0, nil
}

func (q *fakeQueue) cancelledEvents() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]string(nil), q.cancelled...)
}

func (q *fakeQueue) deliveredEvents() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]string(nil), q.published...)
}

func (q *fakeQueue) snapshot() (map[string]string, map[string]urlsafety.Verdict) {
	q.mu.Lock()
	defer q.mu.Unlock()
	submitted := make(map[string]string, len(q.submitted))
	for k, v := range q.submitted {
		submitted[k] = v
	}
	verdicts := make(map[string]urlsafety.Verdict, len(q.verdicts))
	for k, v := range q.verdicts {
		verdicts[k] = v
	}
	return submitted, verdicts
}

// fakeProvider is the Cloudflare half, in memory.
type fakeProvider struct {
	mu sync.Mutex

	scanID    string
	submitErr error
	submits   int
	verdict   urlsafety.Verdict
	pollErr   error
	polls     int
}

func (p *fakeProvider) Submit(_ context.Context, _ string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.submits++
	if p.submitErr != nil {
		return "", p.submitErr
	}
	if p.scanID == "" {
		return "scan-1", nil
	}
	return p.scanID, nil
}

func (p *fakeProvider) Poll(_ context.Context, _, _ string) (urlsafety.Verdict, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.polls++
	return p.verdict, p.pollErr
}

func (p *fakeProvider) counts() (int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.submits, p.polls
}

func worker(queue service.LinkScanQueue, provider service.LinkScanProvider, publisher service.MessageEventPublisher) *service.LinkScanService {
	return service.NewLinkScanService(queue, provider, publisher, nil)
}

// A URL that has never been submitted is submitted, and its id stored — losing
// the id would mean the scan still runs but nobody ever reads it.
func TestUnsubmittedURLIsSubmittedAndItsIDStored(t *testing.T) {
	queue := newFakeQueue(storage.LinkScanJob{CanonicalURL: "https://example.com/x"})
	provider := &fakeProvider{scanID: "scan-9"}

	moved, err := worker(queue, provider, nil).ProcessDue(context.Background())

	if err != nil || moved != 1 {
		t.Fatalf("moved=%d err=%v", moved, err)
	}
	submits, polls := provider.counts()
	// One step per pass: submitting and then immediately polling would spend an
	// exchange on an answer the provider has not had time to produce.
	if submits != 1 || polls != 0 {
		t.Fatalf("submits=%d polls=%d", submits, polls)
	}
	submitted, _ := queue.snapshot()
	if submitted["https://example.com/x"] != "scan-9" {
		t.Fatalf("stored scan id: %v", submitted)
	}
}

// An already-submitted URL is read, not submitted again.
func TestSubmittedURLIsPolled(t *testing.T) {
	queue := newFakeQueue(storage.LinkScanJob{
		CanonicalURL: "https://example.com/x", ScanUUID: "scan-9",
	})
	provider := &fakeProvider{verdict: urlsafety.VerdictSafe}

	if _, err := worker(queue, provider, nil).ProcessDue(context.Background()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	submits, polls := provider.counts()
	if submits != 0 || polls != 1 {
		t.Fatalf("submits=%d polls=%d", submits, polls)
	}
	_, verdicts := queue.snapshot()
	if verdicts["https://example.com/x"] != urlsafety.VerdictSafe {
		t.Fatalf("verdicts: %v", verdicts)
	}
}

func TestFinalVerdictsAreRecorded(t *testing.T) {
	for _, want := range []urlsafety.Verdict{urlsafety.VerdictSafe, urlsafety.VerdictMalicious} {
		t.Run(string(want), func(t *testing.T) {
			queue := newFakeQueue(storage.LinkScanJob{
				CanonicalURL: "https://example.com/x", ScanUUID: "scan-9",
			})
			provider := &fakeProvider{verdict: want}

			if _, err := worker(queue, provider, nil).ProcessDue(context.Background()); err != nil {
				t.Fatalf("ProcessDue: %v", err)
			}
			_, verdicts := queue.snapshot()
			if verdicts["https://example.com/x"] != want {
				t.Fatalf("verdicts: %v", verdicts)
			}
		})
	}
}

// The fail-closed core of the worker: nothing that is not an explicit verdict
// may be written, so the message stays withheld and the row stays pending.
func TestNothingButAFinalVerdictIsRecorded(t *testing.T) {
	for name, provider := range map[string]*fakeProvider{
		"still running":  {pollErr: urlsafety.ErrScanPending},
		"provider down":  {pollErr: urlsafety.ErrUnavailable},
		"unknown":        {verdict: urlsafety.VerdictUnknown},
		"zero value":     {verdict: ""},
		"future verdict": {verdict: urlsafety.Verdict("future")},
		"verdict with an error": {
			verdict: urlsafety.VerdictSafe, pollErr: urlsafety.ErrUnavailable,
		},
	} {
		t.Run(name, func(t *testing.T) {
			queue := newFakeQueue(storage.LinkScanJob{
				CanonicalURL: "https://example.com/x", ScanUUID: "scan-9",
			})

			if _, err := worker(queue, provider, nil).ProcessDue(context.Background()); err != nil {
				t.Fatalf("ProcessDue: %v", err)
			}
			_, verdicts := queue.snapshot()
			if len(verdicts) != 0 {
				t.Fatalf("a non-final answer was stored as a verdict: %v", verdicts)
			}
		})
	}
}

// A failed submission stores nothing, so the next claim submits again rather
// than waiting on an id that was never obtained.
func TestFailedSubmissionStoresNothing(t *testing.T) {
	queue := newFakeQueue(storage.LinkScanJob{CanonicalURL: "https://example.com/x"})
	provider := &fakeProvider{submitErr: urlsafety.ErrUnavailable}

	if _, err := worker(queue, provider, nil).ProcessDue(context.Background()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	submitted, verdicts := queue.snapshot()
	if len(submitted) != 0 || len(verdicts) != 0 {
		t.Fatalf("submitted=%v verdicts=%v", submitted, verdicts)
	}
}

// One URL failing must not stop the batch: the others still move.
func TestOneFailureDoesNotStopTheBatch(t *testing.T) {
	queue := newFakeQueue(
		storage.LinkScanJob{CanonicalURL: "https://a.example/x"},
		storage.LinkScanJob{CanonicalURL: "https://b.example/y"},
		storage.LinkScanJob{CanonicalURL: "https://c.example/z"},
	)
	provider := &fakeProvider{}

	moved, err := worker(queue, provider, nil).ProcessDue(context.Background())

	if err != nil || moved != 3 {
		t.Fatalf("moved=%d err=%v", moved, err)
	}
	submits, _ := provider.counts()
	if submits != 3 {
		t.Fatalf("submits=%d", submits)
	}
}

// --- releasing withheld messages -------------------------------------------

// A promotion is delivered from the outbox, to the right topic, exactly once —
// and the event is retired so it is not delivered again.
func TestPromotedMessageIsPublishedFromTheOutbox(t *testing.T) {
	queue := newFakeQueue()
	queue.summary = storage.ResolveSummary{Published: 1}
	queue.events = []storage.PublishEvent{{
		MessageID: "msg-1", WorkspaceID: "ws-1",
		TargetType: "channel", TargetID: "ch-1",
		Message: domain.Message{ID: "msg-1", WorkspaceID: "ws-1"},
	}}
	publisher := &fakePublisher{}

	if _, err := worker(queue, &fakeProvider{}, publisher).ProcessDue(context.Background()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	calls := publisher.snapshot()
	if len(calls) != 1 {
		t.Fatalf("published %d times", len(calls))
	}
	if calls[0].targetType != "channel" || calls[0].targetID != "ch-1" ||
		calls[0].msg.ID != "msg-1" || calls[0].workspaceID != "ws-1" {
		t.Fatalf("published to the wrong place: %+v", calls[0])
	}
	if delivered := queue.deliveredEvents(); len(delivered) != 1 || delivered[0] != "msg-1" {
		t.Fatalf("the event was not retired: %v", delivered)
	}
}

// A blocked message writes no outbox row, so there is nothing to deliver. It was
// never visible, so there is nothing to retract and nothing to announce.
func TestBlockedMessageProducesNoEvent(t *testing.T) {
	queue := newFakeQueue()
	queue.summary = storage.ResolveSummary{Blocked: 1}
	publisher := &fakePublisher{}

	if _, err := worker(queue, &fakeProvider{}, publisher).ProcessDue(context.Background()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	if len(publisher.snapshot()) != 0 {
		t.Fatal("a blocked message was broadcast")
	}
}

// The crash this whole outbox exists for: the promotion is committed, delivery
// fails, and the event is still there for the next pass. Previously the publish
// was best-effort after the commit, so a failure meant the event was simply lost
// and nobody ever learned the message existed.
func TestUndeliveredEventSurvivesAFailedPublish(t *testing.T) {
	queue := newFakeQueue()
	queue.events = []storage.PublishEvent{{
		MessageID: "msg-1", WorkspaceID: "ws-1", TargetType: "channel", TargetID: "ch-1",
		Message: domain.Message{ID: "msg-1", WorkspaceID: "ws-1"},
	}}
	queue.markErr = errors.New("hub unavailable")
	publisher := &fakePublisher{}

	if _, err := worker(queue, &fakeProvider{}, publisher).ProcessDue(context.Background()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	// Published, but not retired — so the row stays pending and the next pass
	// delivers it again. At-least-once, which the client deduplicates by id.
	if len(publisher.snapshot()) != 1 {
		t.Fatal("the event was not attempted")
	}
	if delivered := queue.deliveredEvents(); len(delivered) != 0 {
		t.Fatalf("an undelivered event was retired: %v", delivered)
	}
}

// A restart is the same shape: the events are in the database, the worker starts,
// and the next pass drains them without anything having promoted a message twice.
func TestOutboxIsDrainedAfterRestart(t *testing.T) {
	queue := newFakeQueue()
	queue.events = []storage.PublishEvent{
		{MessageID: "msg-1", WorkspaceID: "ws-1", TargetType: "channel", TargetID: "ch-1"},
		{MessageID: "msg-2", WorkspaceID: "ws-1", TargetType: "dm", TargetID: "dm-1"},
	}
	publisher := &fakePublisher{}

	// A fresh service, as after a process restart: it holds no state of its own.
	if _, err := worker(queue, &fakeProvider{}, publisher).ProcessDue(context.Background()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	if len(publisher.snapshot()) != 2 {
		t.Fatalf("published %d of 2 pending events", len(publisher.snapshot()))
	}
	if len(queue.deliveredEvents()) != 2 {
		t.Fatalf("retired %v", queue.deliveredEvents())
	}
	// And nothing was re-promoted: the resolve pass reported an empty summary.
	if queue.resolves != 1 {
		t.Fatalf("resolves=%d", queue.resolves)
	}
}

// The release pass runs even when the claim found nothing: a verdict written by
// another replica leaves messages here that nothing else would release.
func TestReleaseRunsOnAnEmptyBatch(t *testing.T) {
	queue := newFakeQueue()

	moved, err := worker(queue, &fakeProvider{}, nil).ProcessDue(context.Background())

	if err != nil || moved != 0 {
		t.Fatalf("moved=%d err=%v", moved, err)
	}
	queue.mu.Lock()
	resolves := queue.resolves
	queue.mu.Unlock()
	if resolves != 1 {
		t.Fatalf("the release pass ran %d times", resolves)
	}
}

// A claim failure is reported rather than swallowed.
func TestClaimFailureIsReported(t *testing.T) {
	queue := newFakeQueue()
	queue.claimErr = errors.New("database unavailable")

	_, err := worker(queue, &fakeProvider{}, nil).ProcessDue(context.Background())

	if err == nil {
		t.Fatal("a claim failure was swallowed")
	}
}

// --- compare-and-set --------------------------------------------------------

// The stale-worker case the security review asked for. Worker A polls a scan;
// while it waits, the row is reclaimed and rebound to a different scan. A's
// answer must not overwrite the current one — the store refuses the write and
// the worker treats that as a lost race, not as an error to retry.
func TestStaleWorkerCannotOverwriteANewerScan(t *testing.T) {
	queue := newFakeQueue(storage.LinkScanJob{
		CanonicalURL: "https://example.com/x", ScanUUID: "scan-A",
	})
	queue.verdictConflict = true
	provider := &fakeProvider{verdict: urlsafety.VerdictSafe}

	if _, err := worker(queue, provider, nil).ProcessDue(context.Background()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	_, verdicts := queue.snapshot()
	if len(verdicts) != 0 {
		t.Fatalf("a superseded worker wrote a verdict: %v", verdicts)
	}
}

// The verdict is written bound to the scan the worker actually polled, so the
// store can compare it. A write that did not carry the scan id could not be
// refused.
func TestVerdictIsWrittenBoundToItsScan(t *testing.T) {
	queue := newFakeQueue(storage.LinkScanJob{
		CanonicalURL: "https://example.com/x", ScanUUID: "scan-A",
	})
	provider := &fakeProvider{verdict: urlsafety.VerdictSafe}

	if _, err := worker(queue, provider, nil).ProcessDue(context.Background()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	queue.mu.Lock()
	bound := queue.boundScan
	queue.mu.Unlock()
	if bound != "scan-A" {
		t.Fatalf("verdict bound to %q", bound)
	}
}

// Losing the submit race is not an error to retry: another worker already bound
// a scan, so submitting again would only orphan a third one at the provider.
func TestLostSubmitRaceDoesNotResubmit(t *testing.T) {
	queue := newFakeQueue(storage.LinkScanJob{CanonicalURL: "https://example.com/x"})
	queue.submitConflict = true
	provider := &fakeProvider{}

	if _, err := worker(queue, provider, nil).ProcessDue(context.Background()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	submits, _ := provider.counts()
	if submits != 1 {
		t.Fatalf("submits=%d — a lost race must not resubmit in the same pass", submits)
	}
	submitted, _ := queue.snapshot()
	if len(submitted) != 0 {
		t.Fatalf("a lost race stored a scan id: %v", submitted)
	}
}

// One claim per item, taken immediately before it is processed. That is the
// lease fix: a batch leased up front leaves its last rows reclaimable while the
// earlier ones are still being worked.
func TestWorkerClaimsOneItemAtATime(t *testing.T) {
	queue := newFakeQueue(
		storage.LinkScanJob{CanonicalURL: "https://a.example/x"},
		storage.LinkScanJob{CanonicalURL: "https://b.example/y"},
		storage.LinkScanJob{CanonicalURL: "https://c.example/z"},
	)
	provider := &fakeProvider{}

	moved, err := worker(queue, provider, nil).ProcessDue(context.Background())

	if err != nil || moved != 3 {
		t.Fatalf("moved=%d err=%v", moved, err)
	}
	queue.mu.Lock()
	claims := queue.claims
	queue.mu.Unlock()
	// Three items, three claims, plus the one that found the queue empty.
	if claims != 4 {
		t.Fatalf("claims=%d, want one per item plus the empty one", claims)
	}
}

// --- the loop ---------------------------------------------------------------

// The loop stops when its context ends, and leaves no goroutine behind. A worker
// that outlived shutdown would publish into a hub that is closing.
func TestWorkerLoopStopsWithItsContext(t *testing.T) {
	queue := newFakeQueue()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		service.RunLinkScanWorker(ctx, worker(queue, &fakeProvider{}, nil), time.Millisecond, nil)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the worker loop did not stop with its context")
	}
}

// A nil processor is a deployment with RF-21 off; the loop is simply not run.
func TestWorkerLoopWithoutAProcessorReturns(t *testing.T) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		service.RunLinkScanWorker(context.Background(), nil, time.Millisecond, nil)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("a worker with no processor did not return")
	}
}

// --- CQ-2: a refusal is announced to its author, durably -------------------
//
// The finding: malicious took the message to `deleted` in the database and told
// nobody. The author's client had shown "checking links…" and had no event that
// would ever change it, so the bubble stayed there forever.

// fakeBlockedPublisher records refusals delivered to authors.
type fakeBlockedPublisher struct {
	mu    sync.Mutex
	calls []blockedCall
}

type blockedCall struct{ workspaceID, recipientUserID, messageID string }

func (p *fakeBlockedPublisher) PublishMessageBlocked(_ context.Context, workspaceID, recipientUserID, messageID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, blockedCall{workspaceID, recipientUserID, messageID})
}

func (p *fakeBlockedPublisher) snapshot() []blockedCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]blockedCall(nil), p.calls...)
}

func workerWithBlocked(queue service.LinkScanQueue, blocked service.MessageBlockedPublisher) *service.LinkScanService {
	svc := service.NewLinkScanService(queue, &fakeProvider{}, &fakePublisher{}, nil)
	svc.SetBlockedPublisher(blocked)
	return svc
}

// The refusal reaches its author, addressed to them alone, and is then retired.
func TestBlockedMessageIsAnnouncedToItsAuthorOnly(t *testing.T) {
	queue := newFakeQueue()
	queue.events = []storage.PublishEvent{{
		MessageID: "msg-1", WorkspaceID: "ws-1",
		EventType: storage.EventMessageBlocked,
		// The audience is the sender, not the conversation the message was never
		// shown in.
		TargetType: storage.TargetSender, TargetID: "user-author",
	}}
	blocked := &fakeBlockedPublisher{}

	if _, err := workerWithBlocked(queue, blocked).ProcessDue(context.Background()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	calls := blocked.snapshot()
	if len(calls) != 1 {
		t.Fatalf("announced %d times", len(calls))
	}
	if calls[0].recipientUserID != "user-author" || calls[0].messageID != "msg-1" {
		t.Fatalf("announced to the wrong place: %+v", calls[0])
	}
	if delivered := queue.deliveredEvents(); len(delivered) != 1 {
		t.Fatalf("the refusal was not retired: %v", delivered)
	}
}

// A refusal never goes out as a message: the conversation is not told that
// something it never saw has been removed.
func TestBlockedMessageIsNotBroadcastToTheTarget(t *testing.T) {
	queue := newFakeQueue()
	queue.events = []storage.PublishEvent{{
		MessageID: "msg-1", WorkspaceID: "ws-1",
		EventType:  storage.EventMessageBlocked,
		TargetType: storage.TargetSender, TargetID: "user-author",
	}}
	publisher := &fakePublisher{}
	svc := service.NewLinkScanService(queue, &fakeProvider{}, publisher, nil)
	svc.SetBlockedPublisher(&fakeBlockedPublisher{})

	if _, err := svc.ProcessDue(context.Background()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	if len(publisher.snapshot()) != 0 {
		t.Fatal("a refusal was broadcast to the conversation")
	}
}

// A refusal that could not be delivered stays pending, exactly like a promotion:
// the author must eventually be told, and a dropped websocket must not be the
// end of it.
func TestUndeliveredRefusalSurvivesForRetry(t *testing.T) {
	queue := newFakeQueue()
	queue.events = []storage.PublishEvent{{
		MessageID: "msg-1", WorkspaceID: "ws-1",
		EventType:  storage.EventMessageBlocked,
		TargetType: storage.TargetSender, TargetID: "user-author",
	}}
	queue.markErr = errors.New("hub unavailable")
	blocked := &fakeBlockedPublisher{}

	if _, err := workerWithBlocked(queue, blocked).ProcessDue(context.Background()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	if len(blocked.snapshot()) != 1 {
		t.Fatal("the refusal was not attempted")
	}
	if len(queue.deliveredEvents()) != 0 {
		t.Fatal("an undelivered refusal was retired")
	}
}

// --- CQ-5: an announcement whose message vanished is retired ---------------

// The finding: a message deleted between promotion and delivery left its event
// pending forever — retried on every pass, counted in the backlog gauge, and
// incapable of ever succeeding.
func TestOrphanedEventIsCancelledRatherThanRetriedForever(t *testing.T) {
	queue := newFakeQueue()
	queue.events = []storage.PublishEvent{{
		MessageID: "msg-1", WorkspaceID: "ws-1",
		EventType:  storage.EventMessageCreated,
		TargetType: "channel", TargetID: "ch-1",
		// The store reports it could not read the message back.
		Cancelled: true,
	}}
	publisher := &fakePublisher{}

	if _, err := service.NewLinkScanService(queue, &fakeProvider{}, publisher, nil).
		ProcessDue(context.Background()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	if len(publisher.snapshot()) != 0 {
		t.Fatal("a cancelled event was still broadcast")
	}
	cancelled := queue.cancelledEvents()
	if len(cancelled) != 1 || cancelled[0] != "msg-1" {
		t.Fatalf("the orphaned event was not retired: %v", cancelled)
	}
	// And not counted as delivered: the two are different operational facts.
	if len(queue.deliveredEvents()) != 0 {
		t.Fatalf("a cancelled event was recorded as delivered: %v", queue.deliveredEvents())
	}
}

// --- SEC-3: lapsed verdicts are requeued before anything is claimed --------

func TestExpiredVerdictsAreReopenedEachPass(t *testing.T) {
	queue := newFakeQueue()
	queue.reopened = 2

	if _, err := worker(queue, &fakeProvider{}, nil).ProcessDue(context.Background()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	queue.mu.Lock()
	reopens := queue.reopens
	queue.mu.Unlock()
	// Once per pass, before the claim: a URL whose clearance lapsed has to be
	// scannable again, or the message waiting on it is stranded — promotable by
	// nothing and re-scanned by nothing.
	if reopens != 1 {
		t.Fatalf("reopens=%d", reopens)
	}
}

// Observability is optional, and this is the test that says so.
//
// The code quality review read the worker as dereferencing s.metrics without
// checking it, which would make "never call SetMetrics" a crash rather than a
// supported deployment. It is not a crash — every *PipelineMetrics method
// tolerates a nil receiver — but nothing held that contract, so a method added
// later without the guard would break a deployment nobody tests. This runs the
// whole pass with no reporter attached and asserts the work still happened.
func TestLinkScanServiceRunsWithoutMetrics(t *testing.T) {
	queue := newFakeQueue(
		storage.LinkScanJob{CanonicalURL: "https://example.com/a"},
		storage.LinkScanJob{CanonicalURL: "https://example.com/b", ScanUUID: "scan-b"},
	)
	queue.summary = storage.ResolveSummary{Published: 1, Blocked: 1}
	queue.reopened = 2
	queue.events = []storage.PublishEvent{{
		MessageID: "m-1", WorkspaceID: "ws-1", EventType: storage.EventMessageCreated,
		TargetType: "channel", TargetID: "ch-1",
	}}
	provider := &fakeProvider{scanID: "scan-a", verdict: urlsafety.VerdictSafe}
	publisher := &fakePublisher{}

	// No SetMetrics anywhere in this test. That is the point.
	svc := service.NewLinkScanService(queue, provider, publisher, nil)

	moved, err := svc.ProcessDue(t.Context())
	if err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	if moved != 2 {
		t.Fatalf("moved = %d, want 2", moved)
	}
	// Functionally identical to a pass with metrics attached: submitted, polled,
	// resolved, revalidated and published all still happened.
	submits, polls := provider.counts()
	if submits != 1 || polls != 1 {
		t.Fatalf("submits=%d polls=%d, want 1 and 1", submits, polls)
	}
	if len(queue.published) != 1 {
		t.Fatalf("published = %v, want one event retired", queue.published)
	}
	if queue.reopens == 0 || queue.resolves == 0 {
		t.Fatalf("reopens=%d resolves=%d, want both exercised", queue.reopens, queue.resolves)
	}
}

// The other half of the contract: attaching a real reporter must not change what
// the worker does, and must actually produce samples. A no-op default is only
// safe if the non-default still reports.
func TestLinkScanServiceReportsWhenMetricsAreAttached(t *testing.T) {
	queue := newFakeQueue(storage.LinkScanJob{CanonicalURL: "https://example.com/a"})
	queue.reopened = 1
	provider := &fakeProvider{scanID: "scan-a"}
	svc := service.NewLinkScanService(queue, provider, &fakePublisher{}, nil)

	observability := observability.NewMetrics(observability.Config{
		ServiceName: "chat-service", MetricsEnabled: true,
	})
	reporter := urlsafety.NewPipelineMetrics(observability, "chat-service")
	if reporter == nil {
		t.Fatal("the pipeline collectors did not register")
	}
	svc.SetMetrics(reporter)

	if _, err := svc.ProcessDue(t.Context()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}

	exposition := scrapeMetrics(t, observability)
	for _, name := range []string{
		"nchat_link_scan_attempts_total",
		"nchat_link_scan_provider_duration_seconds_count",
		"nchat_link_scan_revalidations_total",
	} {
		if !strings.Contains(exposition, name) {
			t.Fatalf("%s was not exported", name)
		}
	}
}

// scrapeMetrics returns what /metrics currently serves.
func scrapeMetrics(t *testing.T, metrics *observability.Metrics) string {
	t.Helper()
	response := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("scrape returned %d", response.Code)
	}
	return response.Body.String()
}

// droppingBlockedPublisher is a bus that accepts the refusal and loses it: no
// subscriber is connected, or the frame never reaches one. It reports nothing,
// because the hub's API reports nothing — a broadcast to a target nobody is
// listening on is not an error, and it is indistinguishable here from one that
// was received.
type droppingBlockedPublisher struct {
	mu    sync.Mutex
	calls int
}

func (p *droppingBlockedPublisher) PublishMessageBlocked(_ context.Context, _, _, _ string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
}

// What a lost refusal does and does not do.
//
// It is worth stating exactly, because the guarantee is easy to overclaim. The
// dispatcher hands the event to the hub, the hub's API returns nothing, and the
// event is retired. Nobody received it and nothing recorded that fact. That is
// the honest semantic of a best-effort realtime bus, and it is precisely why the
// client's recovery cannot be built on this table:
//
//   - the outbox guarantees the event is *processed* in the backend, exactly
//     once, even across a crash;
//   - it does not guarantee any client saw it;
//   - convergence of the client is the reconciliation endpoint's job, and that
//     endpoint reads the message's own link-safety state rather than this row.
//
// The assertion below is the first half of that: a dropped refusal changes no
// business state. The second half — that the state it did not change is still
// readable afterwards — is asserted against a real database in
// TestLinkSafetyStatesPostgreSQL.
func TestALostRefusalChangesNoBusinessState(t *testing.T) {
	queue := newFakeQueue()
	queue.events = []storage.PublishEvent{{
		MessageID: "msg-blocked", WorkspaceID: "ws-1",
		EventType: storage.EventMessageBlocked, TargetType: storage.TargetSender,
		TargetID: "user-author",
	}}
	publisher := &droppingBlockedPublisher{}
	svc := workerWithBlocked(queue, publisher)

	if _, err := svc.ProcessDue(t.Context()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}

	// It was handed to the bus exactly once and retired, which is all the
	// dispatcher can observe.
	if publisher.calls != 1 {
		t.Fatalf("publish calls = %d, want 1", publisher.calls)
	}
	if len(queue.published) != 1 || queue.published[0] != "msg-blocked" {
		t.Fatalf("published = %v, want the event retired", queue.published)
	}
	// Not cancelled: cancelling means "there was nothing left to announce", and
	// something was — it simply was not received.
	if len(queue.cancelled) != 0 {
		t.Fatalf("cancelled = %v, want none", queue.cancelled)
	}
	// And nothing here re-decided the message. The refusal is a fact about the
	// message that was committed before this event ever existed; a delivery
	// failure cannot and does not revisit it.
	if queue.resolves != 1 {
		t.Fatalf("resolves = %d, want exactly the one pass", queue.resolves)
	}
}

// ── The submission window ─────────────────────────────────────────────────────
//
// The finding, stated plainly: submitting is two steps that cannot be one. The
// provider accepts and is billed, then the scan id is written down. A crash, a
// timeout, or a database blip in between leaves a URL that *has* been submitted
// and a row that cannot prove it — and the old code read that row as "never
// submitted" and sent it again.
//
// Cloudflare has no idempotency token, so the gap cannot be closed by the POST.
// What these assert is the next best thing, and the honest one: never resubmit
// on absence alone, ask the provider first, and bound the one case where asking
// cannot settle it.

// uncertainJob is a row whose submission outcome was never recorded.
func uncertainJob(url string, startedAt time.Time, generation int) storage.LinkScanJob {
	return storage.LinkScanJob{
		CanonicalURL: url, SubmitStartedAt: startedAt, SubmitGeneration: generation,
	}
}

// searchingProvider is a provider that can also be asked what it already has.
type searchingProvider struct {
	*fakeProvider
	mu sync.Mutex

	searches int
	// answers is replayed one call at a time, so a test can say "not found, then
	// found" without any timing.
	answers []searchAnswer
}

type searchAnswer struct {
	record urlsafety.ScanRecord
	match  int
	err    error
}

func (p *searchingProvider) FindRecentScan(
	_ context.Context, _ string, _ time.Time,
) (urlsafety.ScanRecord, int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.searches++
	if len(p.answers) == 0 {
		return urlsafety.ScanRecord{}, 0, urlsafety.ErrNotCheckable
	}
	answer := p.answers[0]
	if len(p.answers) > 1 {
		p.answers = p.answers[1:]
	}
	return answer.record, answer.match, answer.err
}

func (p *searchingProvider) searchCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.searches
}

func searchWorker(queue service.LinkScanQueue, provider service.LinkScanProvider) *service.LinkScanService {
	svc := service.NewLinkScanService(queue, provider, &fakePublisher{}, nil)
	svc.SetCapacity(service.LinkScanWorkerCapacity{UncertainTimeout: time.Hour})
	return svc
}

// [§22] The whole scenario, end to end: the provider accepts, the local write
// fails, the process restarts, and the scan is recovered rather than bought
// again.
func TestAnAcceptedSubmissionSurvivesAFailedLocalWrite(t *testing.T) {
	queue := newFakeQueue(storage.LinkScanJob{CanonicalURL: "https://example.com/a"})
	queue.persistErr = errors.New("connection reset")
	provider := &searchingProvider{fakeProvider: &fakeProvider{scanID: "scan-A"}}
	worker := searchWorker(queue, provider)
	worker.SetPersistRetryDelay(0)

	if _, err := worker.ProcessDue(t.Context()); err != nil {
		t.Fatalf("first pass: %v", err)
	}

	// One submission, and the intent was recorded before it.
	submits, _ := provider.counts()
	if submits != 1 {
		t.Fatalf("submits = %d, want 1", submits)
	}
	if len(queue.begun) != 1 {
		t.Fatalf("the intent was not recorded before submitting: %v", queue.begun)
	}
	// Tried more than once to write the id down, because the scan already exists
	// and losing it is the expensive outcome.
	if queue.persistCalls < 2 {
		t.Fatalf("persist attempts = %d, want a bounded retry", queue.persistCalls)
	}

	// The process restarts. The row says an attempt is outstanding, and the
	// search knows about scan-A.
	queue.persistErr = nil
	queue.jobs = []storage.LinkScanJob{uncertainJob("https://example.com/a", time.Now().Add(-time.Minute), 1)}
	provider.answers = []searchAnswer{{record: urlsafety.ScanRecord{UUID: "scan-A"}, match: 1}}
	restarted := searchWorker(queue, provider)

	if _, err := restarted.ProcessDue(t.Context()); err != nil {
		t.Fatalf("after restart: %v", err)
	}

	// The recovered id is adopted, and — the assertion the finding is about —
	// the provider was never asked to scan again.
	if len(queue.adopted) != 1 || queue.adopted[0] != "scan-A" {
		t.Fatalf("adopted = %v, want scan-A", queue.adopted)
	}
	if submits, _ := provider.counts(); submits != 1 {
		t.Fatalf("total submits = %d, want exactly 1", submits)
	}
	if len(queue.begun) != 1 {
		t.Fatalf("a second submission was begun: %v", queue.begun)
	}
}

// [§23] The provider's index is not synchronous. A search that answers "nothing"
// a moment after the submission says very little, and must not be read as "it
// never happened".
func TestReconciliationWaitsForADelayedIndex(t *testing.T) {
	url := "https://example.com/a"
	startedAt := time.Now().Add(-time.Minute)
	queue := newFakeQueue(uncertainJob(url, startedAt, 1))
	provider := &searchingProvider{
		fakeProvider: &fakeProvider{scanID: "scan-A"},
		answers: []searchAnswer{
			{err: urlsafety.ErrNotCheckable},
			{record: urlsafety.ScanRecord{UUID: "scan-A"}, match: 1},
		},
	}
	worker := searchWorker(queue, provider)

	if _, err := worker.ProcessDue(t.Context()); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	// Nothing found, nothing submitted, nothing adopted.
	if submits, _ := provider.counts(); submits != 0 {
		t.Fatalf("a not-found search caused %d submissions", submits)
	}
	if len(queue.adopted) != 0 {
		t.Fatalf("adopted = %v on a not-found search", queue.adopted)
	}

	queue.jobs = []storage.LinkScanJob{uncertainJob(url, startedAt, 1)}
	if _, err := worker.ProcessDue(t.Context()); err != nil {
		t.Fatalf("second pass: %v", err)
	}

	if len(queue.adopted) != 1 || queue.adopted[0] != "scan-A" {
		t.Fatalf("adopted = %v, want scan-A once the index caught up", queue.adopted)
	}
	if submits, _ := provider.counts(); submits != 0 {
		t.Fatalf("submits = %d, want none — the scan already existed", submits)
	}
}

// [§24] A search that fails is not a search that answered. A throttled or broken
// lookup must leave the row exactly where it is.
func TestReconciliationFailureNeverSubmits(t *testing.T) {
	for name, answer := range map[string]searchAnswer{
		"provider unavailable": {err: urlsafety.ErrUnavailable},
		"cannot search":        {err: urlsafety.ErrSearchUnsupported},
		"unexpected error":     {err: errors.New("boom")},
	} {
		t.Run(name, func(t *testing.T) {
			url := "https://example.com/a"
			queue := newFakeQueue(uncertainJob(url, time.Now().Add(-time.Minute), 1))
			provider := &searchingProvider{
				fakeProvider: &fakeProvider{scanID: "scan-A"},
				answers:      []searchAnswer{answer},
			}
			worker := searchWorker(queue, provider)

			if _, err := worker.ProcessDue(t.Context()); err != nil {
				t.Fatalf("ProcessDue: %v", err)
			}

			if submits, _ := provider.counts(); submits != 0 {
				t.Fatalf("a failed search caused %d submissions", submits)
			}
			if len(queue.adopted) != 0 {
				t.Fatalf("adopted %v on a failed search", queue.adopted)
			}
			// Inside the horizon nothing is cleared either: the row stays uncertain
			// and is asked about again.
			if len(queue.resubmitCleared) != 0 {
				t.Fatalf("a failed search released a resubmit: %v", queue.resubmitCleared)
			}
		})
	}
}

// The regression this round exists for: **no elapsed time, and no number of
// worker cycles, ever produces a second submission**.
//
// The previous implementation allowed one bounded resubmit past a 15-minute
// horizon, on the argument that a URL nobody can decide keeps its messages
// withheld forever. That argument is true and it lost anyway: Cloudflare has no
// idempotency token, so the "bounded" duplicate is a scan the account is billed
// for on a submission that probably already succeeded. The horizon is now a
// reporting threshold and nothing else.
//
// Driven by the recorded attempt time rather than by a clock this test waits on.
func TestAnUncertainAttemptIsNeverResubmitted(t *testing.T) {
	url := "https://example.com/a"
	for name, age := range map[string]time.Duration{
		"one minute old":                 time.Minute,
		"past the old 15-minute horizon": 20 * time.Minute,
		"hours old":                      6 * time.Hour,
		"days old":                       72 * time.Hour,
	} {
		t.Run(name, func(t *testing.T) {
			queue := newFakeQueue(uncertainJob(url, time.Now().Add(-age), 1))
			provider := &searchingProvider{fakeProvider: &fakeProvider{scanID: "scan-A"}}
			worker := service.NewLinkScanService(queue, provider, &fakePublisher{}, nil)
			worker.SetCapacity(service.LinkScanWorkerCapacity{UncertainTimeout: 15 * time.Minute})

			// Several cycles, because "eventually" is exactly what the removed
			// branch was: a thing that only happened after enough passes.
			for cycle := range 5 {
				queue.jobs = []storage.LinkScanJob{uncertainJob(url, time.Now().Add(-age), 1)}
				if _, err := worker.ProcessDue(t.Context()); err != nil {
					t.Fatalf("cycle %d: %v", cycle, err)
				}
			}

			if submits, _ := provider.counts(); submits != 0 {
				t.Fatalf("an uncertain attempt was resubmitted %d times", submits)
			}
			// And no intent was recorded either — the row never went back to
			// "never submitted", which is the transition that no longer exists.
			if len(queue.begun) != 0 {
				t.Fatalf("a submission was begun for an uncertain attempt: %v", queue.begun)
			}
		})
	}
}

// A worker started from an already-uncertain row — the restart case — submits
// nothing at all, however long it runs.
func TestARestartedWorkerNeverSubmitsAnUncertainAttempt(t *testing.T) {
	url := "https://example.com/a"
	startedAt := time.Now().Add(-24 * time.Hour)
	queue := newFakeQueue()
	provider := &searchingProvider{
		fakeProvider: &fakeProvider{scanID: "scan-A"},
		answers:      []searchAnswer{{err: urlsafety.ErrNotCheckable}},
	}

	// A brand-new service over the same durable state, three times over, as a
	// crash loop would produce.
	for restart := range 3 {
		queue.jobs = []storage.LinkScanJob{uncertainJob(url, startedAt, 1)}
		worker := service.NewLinkScanService(queue, provider, &fakePublisher{}, nil)
		worker.SetCapacity(service.LinkScanWorkerCapacity{UncertainTimeout: time.Minute})
		if _, err := worker.ProcessDue(t.Context()); err != nil {
			t.Fatalf("restart %d: %v", restart, err)
		}
	}

	if submits, _ := provider.counts(); submits != 0 {
		t.Fatalf("a restart caused %d submissions", submits)
	}
	if len(queue.begun) != 0 {
		t.Fatalf("a restart recorded a submission intent: %v", queue.begun)
	}
	// It did keep asking, which is the only automatic recovery there is.
	if provider.searchCount() != 3 {
		t.Fatalf("searches = %d, want one per restart", provider.searchCount())
	}
}

// Scale-out must not reintroduce the duplicate either: worker A submits and
// becomes uncertain, its lease lapses, worker B claims the row — and searches.
func TestASecondWorkerReconcilesRatherThanResubmits(t *testing.T) {
	url := "https://example.com/a"
	queue := newFakeQueue(storage.LinkScanJob{CanonicalURL: url})
	provider := &searchingProvider{
		fakeProvider: &fakeProvider{scanID: "scan-A"},
		// A submits successfully but cannot write the id down.
		answers: []searchAnswer{{record: urlsafety.ScanRecord{UUID: "scan-A"}, match: 1}},
	}
	queue.persistErr = errors.New("connection reset")
	workerA := searchWorker(queue, provider)
	workerA.SetPersistRetryDelay(0)
	if _, err := workerA.ProcessDue(t.Context()); err != nil {
		t.Fatalf("worker A: %v", err)
	}
	submitsAfterA, _ := provider.counts()
	if submitsAfterA != 1 {
		t.Fatalf("worker A submitted %d times", submitsAfterA)
	}

	// The lease lapses and a different replica picks the row up.
	queue.persistErr = nil
	queue.jobs = []storage.LinkScanJob{uncertainJob(url, time.Now().Add(-time.Hour), 1)}
	workerB := searchWorker(queue, provider)

	if _, err := workerB.ProcessDue(t.Context()); err != nil {
		t.Fatalf("worker B: %v", err)
	}

	if submits, _ := provider.counts(); submits != 1 {
		t.Fatalf("total submits = %d, want exactly the one worker A made", submits)
	}
	if len(queue.adopted) != 1 || queue.adopted[0] != "scan-A" {
		t.Fatalf("worker B adopted %v, want scan-A", queue.adopted)
	}
}

// [§56]// [§56] The shared provider allowance, which is the only kind that means
// anything: two replicas each allowing N per window is 2N at Cloudflare, and the
// number the provider counts is the one that matters.
func TestProviderSubmitCapacityIsSharedBetweenWorkers(t *testing.T) {
	// One store, two workers — the multi-replica shape, with the allowance in the
	// store where both of them see it.
	queue := newFakeQueue(
		storage.LinkScanJob{CanonicalURL: "https://example.com/a"},
		storage.LinkScanJob{CanonicalURL: "https://example.com/b"},
	)
	provider := &fakeProvider{scanID: "scan-A"}
	capacity := service.LinkScanWorkerCapacity{
		ProviderSubmitLimit: 1, ProviderSubmitWindow: time.Minute,
		UncertainTimeout: time.Hour,
	}
	first := service.NewLinkScanService(queue, provider, &fakePublisher{}, nil)
	first.SetCapacity(capacity)
	second := service.NewLinkScanService(queue, provider, &fakePublisher{}, nil)
	second.SetCapacity(capacity)

	if _, err := first.ProcessDue(t.Context()); err != nil {
		t.Fatalf("first worker: %v", err)
	}
	if _, err := second.ProcessDue(t.Context()); err != nil {
		t.Fatalf("second worker: %v", err)
	}

	// One submission across both, because the window allowed one.
	if submits, _ := provider.counts(); submits != 1 {
		t.Fatalf("submits = %d, want 1 across both workers", submits)
	}
	// And the throttled one did not even record an intent: nothing was spent, so
	// nothing has to be reconciled later.
	if len(queue.begun) != 1 {
		t.Fatalf("intents recorded = %v, want one", queue.begun)
	}
}

// Failing to read the allowance is not permission to spend it.
func TestProviderCapacityFailureIsFailClosed(t *testing.T) {
	queue := newFakeQueue(storage.LinkScanJob{CanonicalURL: "https://example.com/a"})
	queue.reserveErr = errors.New("database down")
	provider := &fakeProvider{scanID: "scan-A"}
	worker := service.NewLinkScanService(queue, provider, &fakePublisher{}, nil)
	worker.SetCapacity(service.LinkScanWorkerCapacity{
		ProviderSubmitLimit: 10, ProviderSubmitWindow: time.Minute, UncertainTimeout: time.Hour,
	})

	if _, err := worker.ProcessDue(t.Context()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}

	if submits, _ := provider.counts(); submits != 0 {
		t.Fatalf("submits = %d, want none when the allowance is unreadable", submits)
	}
}

// [§25] The ambiguous transport failure, which is the case that makes a blind
// retry wrong.
//
// The HTTP client cannot tell "Cloudflare refused" from "Cloudflare accepted and
// the response never arrived": a timeout after the bytes went out looks exactly
// like a connection that was never established. So a failed exchange leaves the
// attempt *outstanding* rather than reset — the row does not return to "never
// submitted", and the next pass asks the provider what happened.
func TestAnAmbiguousSubmitFailureLeavesTheAttemptOutstanding(t *testing.T) {
	for name, submitErr := range map[string]error{
		"timeout after the request went out": context.DeadlineExceeded,
		"provider unavailable":               urlsafety.ErrUnavailable,
		"transport error":                    errors.New("connection reset by peer"),
	} {
		t.Run(name, func(t *testing.T) {
			url := "https://example.com/a"
			queue := newFakeQueue(storage.LinkScanJob{CanonicalURL: url})
			provider := &searchingProvider{fakeProvider: &fakeProvider{submitErr: submitErr}}
			worker := searchWorker(queue, provider)

			if _, err := worker.ProcessDue(t.Context()); err != nil {
				t.Fatalf("ProcessDue: %v", err)
			}

			// The intent was recorded before the call, and nothing cleared it. That
			// recorded intent is the whole difference between this and a resubmit.
			if len(queue.begun) != 1 {
				t.Fatalf("intents = %v, want exactly the one attempt", queue.begun)
			}
			submitted, verdicts := queue.snapshot()
			if len(submitted) != 0 || len(verdicts) != 0 {
				t.Fatalf("submitted=%v verdicts=%v, want nothing recorded", submitted, verdicts)
			}

			// The next pass sees an outstanding attempt and searches instead of
			// submitting.
			queue.jobs = []storage.LinkScanJob{uncertainJob(url, time.Now().Add(-time.Minute), 1)}
			if _, err := worker.ProcessDue(t.Context()); err != nil {
				t.Fatalf("second pass: %v", err)
			}
			if provider.searchCount() != 1 {
				t.Fatalf("searches = %d, want the recovery path taken", provider.searchCount())
			}
			if len(queue.begun) != 1 {
				t.Fatalf("a second submission was begun: %v", queue.begun)
			}
		})
	}
}
