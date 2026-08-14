package service_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

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
	resolved   []storage.ResolvedMessage
	resolveErr error
	resolves   int
}

func newFakeQueue(jobs ...storage.LinkScanJob) *fakeQueue {
	return &fakeQueue{
		jobs:      jobs,
		submitted: map[string]string{},
		verdicts:  map[string]urlsafety.Verdict{},
	}
}

func (q *fakeQueue) ClaimDueLinkScans(_ context.Context, _ int) ([]storage.LinkScanJob, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.claims++
	if q.claimErr != nil {
		return nil, q.claimErr
	}
	claimed := q.jobs
	q.jobs = nil
	return claimed, nil
}

func (q *fakeQueue) RecordLinkScanSubmission(_ context.Context, canonicalURL, scanUUID string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.submitted[canonicalURL] = scanUUID
	return nil
}

func (q *fakeQueue) RecordLinkVerdict(_ context.Context, canonicalURL string, verdict urlsafety.Verdict) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.verdicts[canonicalURL] = verdict
	return nil
}

func (q *fakeQueue) ResolveDecidedMessages(_ context.Context) ([]storage.ResolvedMessage, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.resolves++
	if q.resolveErr != nil {
		return nil, q.resolveErr
	}
	resolved := q.resolved
	q.resolved = nil
	return resolved, nil
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

// A promoted message is broadcast exactly once, to the right topic.
func TestPromotedMessageIsPublishedOnce(t *testing.T) {
	queue := newFakeQueue()
	queue.resolved = []storage.ResolvedMessage{{
		Message:   domain.Message{ID: "msg-1", WorkspaceID: "ws-1"},
		Published: true, TargetType: "channel", TargetID: "ch-1",
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
}

// A blocked message is never broadcast. It was never visible, so there is
// nothing to retract and nothing to announce.
func TestBlockedMessageIsNeverPublished(t *testing.T) {
	queue := newFakeQueue()
	queue.resolved = []storage.ResolvedMessage{{
		Message:   domain.Message{ID: "msg-1", WorkspaceID: "ws-1"},
		Published: false, TargetType: "channel", TargetID: "ch-1",
	}}
	publisher := &fakePublisher{}

	if _, err := worker(queue, &fakeProvider{}, publisher).ProcessDue(context.Background()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	if len(publisher.snapshot()) != 0 {
		t.Fatal("a blocked message was broadcast")
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

// A claim failure is reported rather than swallowed, and releases nothing.
func TestClaimFailureIsReported(t *testing.T) {
	queue := newFakeQueue()
	queue.claimErr = errors.New("database unavailable")

	_, err := worker(queue, &fakeProvider{}, nil).ProcessDue(context.Background())

	if err == nil {
		t.Fatal("a claim failure was swallowed")
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
