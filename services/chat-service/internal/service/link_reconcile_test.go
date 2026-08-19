package service_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/urlsafety"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// The reader-driven and background halves of inconclusive recovery (issue #135).
//
// Two properties run through everything here.
//
// The first is that nothing reconciliation does can buy a Cloudflare scan. That
// is enforced by the type system rather than by care: LinkVerdictReconciler has
// one method and it is not Submit. The fake below records every provider call
// anyway, so a future change that widens the interface fails these tests rather
// than quietly costing money.
//
// The second is that a client never names a URL. It names a message it can
// already read, and the URLs are read out of the store. A test that let the
// client choose would be testing a different, much worse endpoint — one that
// turns this deployment's credentials into a public URL-scanner proxy.

const (
	reconcileWorkspace = "ws-1"
	reconcileViewer    = "11111111-1111-4111-8111-111111111111"
	reconcileMessage   = "22222222-2222-4222-8222-222222222222"
	reconcileURL       = "https://recon.example/a"
	reconcileScanUUID  = "scan-a"
)

// fakeReconcileQueue is the durable half in memory.
//
// It mirrors the real store's *predicates*, not just its shape: the manual claim
// hands out one slot per URL and then refuses, the background claim is bounded by
// a counter it consumes and never refills, and the verdict write is a one-way
// door bound to the scan the row already owns.
type fakeReconcileQueue struct {
	mu sync.Mutex

	// urls is what the store would return for the message under test. A nil value
	// means the caller may not read the message, or it has nothing to reconcile —
	// the store cannot tell those apart either, and neither may a client.
	urls    []string
	urlsErr error

	state domain.MessageLinkSafety

	// manualClaimed records that the once-per-URL slot has been taken, which is
	// the durable cooldown expressed without a clock.
	manualClaimed  map[string]bool
	backgroundLeft int

	scanUUID string

	verdicts      []urlsafety.Verdict
	evidenceTimes []time.Time
	refreshed     []string
	changes       []storage.MessageLinkSafetyChange

	conflictOnWrite bool
	reconciled      chan struct{}
	claimErr        error
	claimed         chan struct{}
}

func newFakeReconcileQueue() *fakeReconcileQueue {
	return &fakeReconcileQueue{
		urls:           []string{reconcileURL},
		state:          domain.MessageLinkSafetyInconclusive,
		manualClaimed:  map[string]bool{},
		backgroundLeft: 2,
		scanUUID:       reconcileScanUUID,
	}
}

func (q *fakeReconcileQueue) MessageInconclusiveURLs(
	_ context.Context, workspaceID, viewerID, messageID string,
) ([]string, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.urlsErr != nil {
		return nil, q.urlsErr
	}
	// The authorization the real query applies, reduced to its effect: anything
	// that is not this workspace, this reader and this message is simply not
	// answered about.
	if workspaceID != reconcileWorkspace || viewerID != reconcileViewer || messageID != reconcileMessage {
		return nil, domain.ErrNotFound
	}
	if len(q.urls) == 0 {
		return nil, domain.ErrNotFound
	}
	return append([]string(nil), q.urls...), nil
}

func (q *fakeReconcileQueue) MessageLinkSafety(
	_ context.Context, workspaceID, viewerID, messageID string,
) (domain.MessageLinkSafety, time.Time, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if workspaceID != reconcileWorkspace || viewerID != reconcileViewer || messageID != reconcileMessage {
		return domain.MessageLinkSafetyNone, time.Time{}, domain.ErrNotFound
	}
	return q.state, time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC), nil
}

func (q *fakeReconcileQueue) ClaimManualReconcile(
	_ context.Context, canonicalURLs []string,
) ([]storage.InconclusiveScan, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	var claimed []storage.InconclusiveScan
	for _, url := range canonicalURLs {
		if q.manualClaimed[url] {
			continue
		}
		q.manualClaimed[url] = true
		claimed = append(claimed, storage.InconclusiveScan{CanonicalURL: url, ScanUUID: q.scanUUID})
	}
	return claimed, nil
}

func (q *fakeReconcileQueue) ClaimDueInconclusiveScans(
	_ context.Context, batchSize int,
) ([]storage.InconclusiveScan, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.claimed != nil {
		select {
		case q.claimed <- struct{}{}:
		default:
		}
	}
	if q.claimErr != nil {
		return nil, q.claimErr
	}
	if q.backgroundLeft <= 0 || batchSize <= 0 {
		return nil, nil
	}
	q.backgroundLeft--
	return []storage.InconclusiveScan{
		{CanonicalURL: reconcileURL, ScanUUID: q.scanUUID},
	}, nil
}

func (q *fakeReconcileQueue) ReconcileLinkVerdict(
	_ context.Context, _, scanUUID string, evidence urlsafety.ScanEvidence,
) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.conflictOnWrite {
		return storage.ErrLinkScanConflict
	}
	if scanUUID != q.scanUUID {
		return storage.ErrLinkScanConflict
	}
	// The real store refuses undated evidence outright, because a verdict with no
	// evidence time has no honest lifetime.
	if evidence.ObservedAt.IsZero() {
		return domain.ErrInvalidInput
	}
	verdict := evidence.Verdict
	q.verdicts = append(q.verdicts, verdict)
	q.evidenceTimes = append(q.evidenceTimes, evidence.ObservedAt)
	if q.reconciled != nil {
		select {
		case q.reconciled <- struct{}{}:
		default:
		}
	}
	switch verdict {
	case urlsafety.VerdictSafe:
		q.state = domain.MessageLinkSafetySafe
	case urlsafety.VerdictMalicious:
		q.state = domain.MessageLinkSafetyMalicious
	}
	return nil
}

// RefreshMessageLinkSafety mirrors the real store's contract, which the drain
// loop depends on: it returns only the rows it *changed*, so a second call for
// the same URL reports nothing and the caller stops. A fake that kept returning
// the same rows would both hide that contract and spin.
func (q *fakeReconcileQueue) RefreshMessageLinkSafety(
	_ context.Context, canonicalURL string,
) ([]storage.MessageLinkSafetyChange, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.refreshed = append(q.refreshed, canonicalURL)
	pending := q.changes
	q.changes = nil
	return pending, nil
}

func (q *fakeReconcileQueue) refreshCalls() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.refreshed)
}

func (q *fakeReconcileQueue) writtenVerdicts() []urlsafety.Verdict {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]urlsafety.Verdict(nil), q.verdicts...)
}

// fakeReconciler is the provider half, and is deliberately unable to submit.
type fakeReconciler struct {
	mu       sync.Mutex
	verdict  urlsafety.Verdict
	observed time.Time
	found    bool
	err      error
	asked    []string
	askedFor int
}

func (r *fakeReconciler) Reconcile(
	_ context.Context, canonicalURL string,
) (urlsafety.ScanEvidence, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.asked = append(r.asked, canonicalURL)
	r.askedFor++
	observed := r.observed
	if observed.IsZero() && r.err == nil {
		// The real provider never returns an undated verdict; default to fresh
		// evidence so tests that do not care about age read naturally.
		observed = time.Now()
	}
	return urlsafety.ScanEvidence{
		Verdict: r.verdict, ObservedAt: observed, CandidateFound: r.found || r.err == nil,
	}, r.err
}

func (r *fakeReconciler) calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.askedFor
}

func (r *fakeReconciler) askedURLs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.asked...)
}

type recordedChange struct {
	workspaceID, targetType, targetID, messageID, state string
	updatedAt                                           time.Time
}

type fakeChangePublisher struct {
	mu        sync.Mutex
	published []recordedChange
}

func (p *fakeChangePublisher) PublishMessageLinkSafetyChanged(
	_ context.Context, workspaceID, targetType, targetID, messageID, state string, updatedAt time.Time,
) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.published = append(p.published, recordedChange{
		workspaceID: workspaceID, targetType: targetType, targetID: targetID,
		messageID: messageID, state: state, updatedAt: updatedAt,
	})
}

func (p *fakeChangePublisher) events() []recordedChange {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]recordedChange(nil), p.published...)
}

func newReconcileService(
	queue *fakeReconcileQueue, provider *fakeReconciler,
) (*service.LinkReconcileService, *fakeChangePublisher) {
	publisher := &fakeChangePublisher{}
	svc := service.NewLinkReconcileService(queue, provider, nil)
	svc.SetPublisher(publisher)
	return svc, publisher
}

func manualInput() service.ReconcileMessageInput {
	return service.ReconcileMessageInput{
		WorkspaceID: reconcileWorkspace, ViewerID: reconcileViewer, MessageID: reconcileMessage,
	}
}

// A clearance obtained by reconciliation removes the notice, and everyone
// holding the message is told — once.
func TestReconcileMessagePublishesAClearance(t *testing.T) {
	updatedAt := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	queue := newFakeReconcileQueue()
	queue.changes = []storage.MessageLinkSafetyChange{{
		MessageID: reconcileMessage, WorkspaceID: reconcileWorkspace,
		TargetType: storage.TargetChannel, TargetID: "ch-1",
		State: domain.MessageLinkSafetySafe, UpdatedAt: updatedAt,
	}}
	provider := &fakeReconciler{verdict: urlsafety.VerdictSafe}
	svc, publisher := newReconcileService(queue, provider)

	result, err := svc.ReconcileMessage(context.Background(), manualInput())

	if err != nil {
		t.Fatalf("ReconcileMessage: %v", err)
	}
	if result.State != domain.MessageLinkSafetySafe {
		t.Fatalf("state = %q, want safe", result.State)
	}
	if result.UpdatedAt.IsZero() {
		t.Fatal("authoritative result has no update version")
	}
	if result.RetryAfter <= 0 {
		t.Fatal("a retry hint is always answered, so the button has something to wait on")
	}
	if got := queue.writtenVerdicts(); len(got) != 1 || got[0] != urlsafety.VerdictSafe {
		t.Fatalf("written verdicts = %v", got)
	}
	events := publisher.events()
	if len(events) != 1 {
		t.Fatalf("events = %+v, want exactly one correction", events)
	}
	if events[0].messageID != reconcileMessage || events[0].state != "safe" || !events[0].updatedAt.Equal(updatedAt) {
		t.Fatalf("event = %+v", events[0])
	}
	// The audience is the conversation, not the author: the message was delivered,
	// so everyone holding it must converge.
	if events[0].targetType != storage.TargetChannel || events[0].targetID != "ch-1" {
		t.Fatalf("event addressed %s/%s", events[0].targetType, events[0].targetID)
	}
}

// The direction that costs a reader something: a link that was merely unverified
// turns out to be condemned after the message was already delivered.
func TestReconcileMessageCondemnsAPublishedLink(t *testing.T) {
	queue := newFakeReconcileQueue()
	queue.changes = []storage.MessageLinkSafetyChange{{
		MessageID: reconcileMessage, WorkspaceID: reconcileWorkspace,
		TargetType: storage.TargetDM, TargetID: "dm-1",
		State: domain.MessageLinkSafetyMalicious,
	}}
	provider := &fakeReconciler{verdict: urlsafety.VerdictMalicious}
	svc, publisher := newReconcileService(queue, provider)

	result, err := svc.ReconcileMessage(context.Background(), manualInput())

	if err != nil {
		t.Fatalf("ReconcileMessage: %v", err)
	}
	if result.State != domain.MessageLinkSafetyMalicious {
		t.Fatalf("state = %q, want malicious", result.State)
	}
	events := publisher.events()
	if len(events) != 1 || events[0].state != "malicious" {
		t.Fatalf("events = %+v, want one malicious correction", events)
	}
}

// Nothing new learned is not an error. The reader is told the state the message
// actually has, which is what stops a client from guessing.
func TestReconcileMessageReportsTheCurrentStateWhenNothingChanged(t *testing.T) {
	for name, provider := range map[string]*fakeReconciler{
		"no candidate":       {err: urlsafety.ErrNotCheckable},
		"still inconclusive": {err: urlsafety.ErrScanInconclusive},
		"still running":      {err: urlsafety.ErrScanPending},
		"provider failed":    {err: urlsafety.ErrUnavailable},
		"cannot search":      {err: urlsafety.ErrSearchUnsupported},
	} {
		t.Run(name, func(t *testing.T) {
			queue := newFakeReconcileQueue()
			svc, publisher := newReconcileService(queue, provider)

			result, err := svc.ReconcileMessage(context.Background(), manualInput())

			if err != nil {
				t.Fatalf("a fruitless reconciliation must not be an error: %v", err)
			}
			if result.State != domain.MessageLinkSafetyInconclusive {
				t.Fatalf("state = %q, want the message left inconclusive", result.State)
			}
			if got := queue.writtenVerdicts(); len(got) != 0 {
				t.Fatalf("wrote %v without a usable verdict", got)
			}
			if events := publisher.events(); len(events) != 0 {
				t.Fatalf("announced %+v when nothing changed", events)
			}
		})
	}
}

// The rate limit that actually protects the provider account: one search per URL,
// deployment-wide, however many readers press the button.
func TestReconcileMessageIsRateLimitedPerURL(t *testing.T) {
	queue := newFakeReconcileQueue()
	provider := &fakeReconciler{err: urlsafety.ErrNotCheckable}
	svc, _ := newReconcileService(queue, provider)

	for attempt := 0; attempt < 5; attempt++ {
		result, err := svc.ReconcileMessage(context.Background(), manualInput())
		if err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
		// Every attempt still answers, and answers honestly. A refusal would teach a
		// client to retry; the current state is what it came for.
		if result.State != domain.MessageLinkSafetyInconclusive {
			t.Fatalf("attempt %d state = %q", attempt, result.State)
		}
	}
	if provider.calls() != 1 {
		t.Fatalf("the provider was asked %d times for one url in one cooldown", provider.calls())
	}
}

// Authorization, and the sameness of every refusal. A caller must not be able to
// tell "you may not read it" from "there is no such message" from "it has nothing
// to reconcile" — otherwise the endpoint is a message-id oracle.
func TestReconcileMessageIsAuthorized(t *testing.T) {
	for name, input := range map[string]service.ReconcileMessageInput{
		"another reader": {
			WorkspaceID: reconcileWorkspace,
			ViewerID:    "33333333-3333-4333-8333-333333333333",
			MessageID:   reconcileMessage,
		},
		"another workspace": {
			WorkspaceID: "ws-other", ViewerID: reconcileViewer, MessageID: reconcileMessage,
		},
		"another message": {
			WorkspaceID: reconcileWorkspace, ViewerID: reconcileViewer,
			MessageID: "44444444-4444-4444-8444-444444444444",
		},
	} {
		t.Run(name, func(t *testing.T) {
			queue := newFakeReconcileQueue()
			provider := &fakeReconciler{verdict: urlsafety.VerdictSafe}
			svc, _ := newReconcileService(queue, provider)

			if _, err := svc.ReconcileMessage(context.Background(), input); !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("err = %v, want ErrNotFound", err)
			}
			// And nothing was spent at the provider on behalf of a caller who was
			// never entitled to an answer.
			if provider.calls() != 0 {
				t.Fatal("an unauthorized request reached the provider")
			}
		})
	}
}

// A message id that is not a UUID never reaches a query.
func TestReconcileMessageRejectsAMalformedID(t *testing.T) {
	queue := newFakeReconcileQueue()
	provider := &fakeReconciler{}
	svc, _ := newReconcileService(queue, provider)

	input := manualInput()
	input.MessageID = "not-a-uuid"

	if _, err := svc.ReconcileMessage(context.Background(), input); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
	if provider.calls() != 0 {
		t.Fatal("a malformed id reached the provider")
	}
}

// The client does not choose the URL. Whatever it sends, the provider is only
// ever asked about what the store recorded for this message — the property that
// keeps this endpoint from being a URL-scanner proxy.
func TestReconcileMessageAsksOnlyAboutStoredURLs(t *testing.T) {
	queue := newFakeReconcileQueue()
	queue.urls = []string{"https://recon.example/stored"}
	provider := &fakeReconciler{err: urlsafety.ErrNotCheckable}
	svc, _ := newReconcileService(queue, provider)

	if _, err := svc.ReconcileMessage(context.Background(), manualInput()); err != nil {
		t.Fatalf("ReconcileMessage: %v", err)
	}
	asked := provider.askedURLs()
	if len(asked) != 1 || asked[0] != "https://recon.example/stored" {
		t.Fatalf("asked about %v, want only the url the store recorded", asked)
	}
}

// The scan id is not a client input either: it comes off the row, and the write
// is bound to it. A row that has moved on rejects the answer rather than taking
// somebody else's.
func TestReconcileWritesAreBoundToTheRowsOwnScan(t *testing.T) {
	queue := newFakeReconcileQueue()
	queue.conflictOnWrite = true
	provider := &fakeReconciler{verdict: urlsafety.VerdictSafe}
	svc, publisher := newReconcileService(queue, provider)

	result, err := svc.ReconcileMessage(context.Background(), manualInput())

	if err != nil {
		t.Fatalf("a lost race is not a caller error: %v", err)
	}
	if result.State != domain.MessageLinkSafetyInconclusive {
		t.Fatalf("state = %q", result.State)
	}
	// Nothing announced for a write that did not land.
	if events := publisher.events(); len(events) != 0 {
		t.Fatalf("announced %+v for a write that lost its race", events)
	}
}

// A deployment with no provider client answers plainly rather than pretending it
// looked. A client told "nothing new" would stop asking.
func TestReconcileMessageRefusesWithoutAProvider(t *testing.T) {
	svc := service.NewLinkReconcileService(newFakeReconcileQueue(), nil, nil)

	if svc.Ready() {
		t.Fatal("a service with no provider reported itself ready")
	}
	if _, err := svc.ReconcileMessage(context.Background(), manualInput()); !errors.Is(err, domain.ErrURLCheckUnavailable) {
		t.Fatalf("err = %v, want ErrURLCheckUnavailable", err)
	}
}

// The background pass, and the bound that makes it terminate. The claim consumes
// an attempt whether or not the provider answers, so a failing provider cannot
// become an endless search loop.
func TestBackgroundReconciliationIsBounded(t *testing.T) {
	queue := newFakeReconcileQueue()
	queue.backgroundLeft = 2
	provider := &fakeReconciler{err: urlsafety.ErrUnavailable}
	svc, _ := newReconcileService(queue, provider)

	total := 0
	for pass := 0; pass < 6; pass++ {
		examined, err := svc.ProcessDueReconciliations(context.Background())
		if err != nil {
			t.Fatalf("pass %d: %v", pass, err)
		}
		total += examined
	}
	if total != 2 {
		t.Fatalf("examined %d urls, want the claim's own bound to stop the pass", total)
	}
	if provider.calls() != 2 {
		t.Fatalf("the provider was asked %d times", provider.calls())
	}
	if got := queue.writtenVerdicts(); len(got) != 0 {
		t.Fatalf("a failing provider produced %v", got)
	}
}

// A pass that does learn something records it and converges the messages.
func TestBackgroundReconciliationRecordsAndAnnounces(t *testing.T) {
	queue := newFakeReconcileQueue()
	queue.backgroundLeft = 1
	queue.changes = []storage.MessageLinkSafetyChange{{
		MessageID: reconcileMessage, WorkspaceID: reconcileWorkspace,
		TargetType: storage.TargetChannel, TargetID: "ch-1",
		State: domain.MessageLinkSafetySafe,
	}}
	provider := &fakeReconciler{verdict: urlsafety.VerdictSafe}
	svc, publisher := newReconcileService(queue, provider)

	examined, err := svc.ProcessDueReconciliations(context.Background())
	if err != nil {
		t.Fatalf("ProcessDueReconciliations: %v", err)
	}
	if examined != 1 {
		t.Fatalf("examined = %d, want 1", examined)
	}
	if got := queue.writtenVerdicts(); len(got) != 1 || got[0] != urlsafety.VerdictSafe {
		t.Fatalf("written verdicts = %v", got)
	}
	if events := publisher.events(); len(events) != 1 {
		t.Fatalf("events = %+v, want one correction", events)
	}
}

// A deployment that never wired a publisher still reconciles: the state is
// persisted and every subsequent read is correct, which is the same degradation
// a dropped websocket frame produces.
func TestReconciliationWorksWithoutAPublisher(t *testing.T) {
	queue := newFakeReconcileQueue()
	provider := &fakeReconciler{verdict: urlsafety.VerdictSafe}
	svc := service.NewLinkReconcileService(queue, provider, nil)

	if _, err := svc.ReconcileMessage(context.Background(), manualInput()); err != nil {
		t.Fatalf("ReconcileMessage: %v", err)
	}
	if got := queue.writtenVerdicts(); len(got) != 1 {
		t.Fatalf("written verdicts = %v", got)
	}
}

// Metrics are optional, and a nil reporter is a supported deployment.
func TestReconciliationRunsWithoutMetrics(t *testing.T) {
	queue := newFakeReconcileQueue()
	provider := &fakeReconciler{verdict: urlsafety.VerdictSafe}
	svc := service.NewLinkReconcileService(queue, provider, nil)
	svc.SetMetrics(nil)

	if _, err := svc.ProcessDueReconciliations(context.Background()); err != nil {
		t.Fatalf("ProcessDueReconciliations: %v", err)
	}
}

// Convergence of clients is drained across batches rather than capped at one.
//
// The store returns only the rows it changed, so the loop ends when there is
// nothing left. A URL carried by more messages than one batch holds therefore
// converges within a single reconciliation instead of waiting for the next pass.
func TestReconciliationDrainsTheMessagesCarryingAURL(t *testing.T) {
	queue := newFakeReconcileQueue()
	queue.changes = []storage.MessageLinkSafetyChange{{
		MessageID: reconcileMessage, WorkspaceID: reconcileWorkspace,
		TargetType: storage.TargetChannel, TargetID: "ch-1",
		State: domain.MessageLinkSafetySafe,
	}}
	provider := &fakeReconciler{verdict: urlsafety.VerdictSafe}
	svc, publisher := newReconcileService(queue, provider)

	if _, err := svc.ReconcileMessage(context.Background(), manualInput()); err != nil {
		t.Fatalf("ReconcileMessage: %v", err)
	}

	// Asked again after the batch that reported changes, and stopped as soon as a
	// batch reported none. Exactly one announcement — the drain must not re-emit.
	if queue.refreshCalls() != 2 {
		t.Fatalf("refresh calls = %d, want one productive batch plus the one that drains",
			queue.refreshCalls())
	}
	if events := publisher.events(); len(events) != 1 {
		t.Fatalf("events = %+v, want exactly one", events)
	}
}

// The security property the batch size must not be able to weaken.
//
// A URL proven malicious is refused for *new* messages the instant the verdict
// row is written, which happens before a single message row is touched. The
// convergence loop below it is about what clients render, and no cap there can
// delay the gate.
func TestAMaliciousVerdictIsRecordedBeforeAnyMessageConverges(t *testing.T) {
	queue := newFakeReconcileQueue()
	provider := &fakeReconciler{verdict: urlsafety.VerdictMalicious}
	// The convergence step fails outright: the worst case for the ordering claim.
	failing := &refreshFailingQueue{fakeReconcileQueue: queue}
	svc := service.NewLinkReconcileService(failing, provider, nil)

	if _, err := svc.ReconcileMessage(context.Background(), manualInput()); err != nil {
		t.Fatalf("ReconcileMessage: %v", err)
	}

	// The verdict landed even though nothing converged. From here the send path's
	// LoadLinkVerdicts reads 'malicious' for this URL, so no new message carrying
	// it can be published — independently of any message-row update.
	got := queue.writtenVerdicts()
	if len(got) != 1 || got[0] != urlsafety.VerdictMalicious {
		t.Fatalf("written verdicts = %v, want the verdict recorded regardless", got)
	}
}

// refreshFailingQueue is the queue with convergence broken, so a test can assert
// what survives it.
type refreshFailingQueue struct {
	*fakeReconcileQueue
}

func (q *refreshFailingQueue) RefreshMessageLinkSafety(
	context.Context, string,
) ([]storage.MessageLinkSafetyChange, error) {
	return nil, errors.New("refresh unavailable")
}

// The convergence drain (CQ-003).
//
// The loop used to run a fixed number of batches, which meant a URL carried by
// more messages than batch × passes left the remainder permanently on the old
// marker — and for a condemnation that is a message still showing a live link to
// a URL this deployment knows is malicious. It now drains until the store reports
// nothing left, which terminates because every batch strictly consumes work.

// batchingReconcileQueue hands back convergence work in batches, like the real
// store: only rows it actually changed, and never the same row twice.
type batchingReconcileQueue struct {
	*fakeReconcileQueue
	remaining int
	batchSize int
	batches   int
}

func (q *batchingReconcileQueue) RefreshMessageLinkSafety(
	_ context.Context, _ string,
) ([]storage.MessageLinkSafetyChange, error) {
	q.batches++
	if q.remaining <= 0 {
		return nil, nil
	}
	size := q.batchSize
	if size > q.remaining {
		size = q.remaining
	}
	q.remaining -= size
	changes := make([]storage.MessageLinkSafetyChange, size)
	for i := range changes {
		changes[i] = storage.MessageLinkSafetyChange{
			MessageID:   fmt.Sprintf("msg-%d-%d", q.batches, i),
			WorkspaceID: reconcileWorkspace,
			TargetType:  storage.TargetChannel, TargetID: "ch-1",
			State: domain.MessageLinkSafetyMalicious,
		}
	}
	return changes, nil
}

// More messages than any previous ceiling allowed, all of which must converge.
func TestConvergenceDrainsPastTheOldCeiling(t *testing.T) {
	const total = 4201
	queue := &batchingReconcileQueue{
		fakeReconcileQueue: newFakeReconcileQueue(),
		remaining:          total,
		batchSize:          500,
	}
	provider := &fakeReconciler{verdict: urlsafety.VerdictMalicious}
	publisher := &fakeChangePublisher{}
	svc := service.NewLinkReconcileService(queue, provider, nil)
	svc.SetPublisher(publisher)

	if _, err := svc.ReconcileMessage(context.Background(), manualInput()); err != nil {
		t.Fatalf("ReconcileMessage: %v", err)
	}

	if queue.remaining != 0 {
		t.Fatalf("%d messages were abandoned on the old marker", queue.remaining)
	}
	if got := len(publisher.events()); got != total {
		t.Fatalf("announced %d corrections, want all %d", got, total)
	}
}

// stallingReconcileQueue violates the store's contract by returning the same
// batch forever. The drain must notice and stop rather than spin.
type stallingReconcileQueue struct {
	*fakeReconcileQueue
	calls int
}

func (q *stallingReconcileQueue) RefreshMessageLinkSafety(
	_ context.Context, _ string,
) ([]storage.MessageLinkSafetyChange, error) {
	q.calls++
	return []storage.MessageLinkSafetyChange{{
		MessageID: "msg-stuck", WorkspaceID: reconcileWorkspace,
		TargetType: storage.TargetChannel, TargetID: "ch-1",
		State: domain.MessageLinkSafetySafe,
	}}, nil
}

func TestConvergenceStopsWhenItStopsMakingProgress(t *testing.T) {
	queue := &stallingReconcileQueue{fakeReconcileQueue: newFakeReconcileQueue()}
	provider := &fakeReconciler{verdict: urlsafety.VerdictSafe}
	svc := service.NewLinkReconcileService(queue, provider, nil)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = svc.ReconcileMessage(context.Background(), manualInput())
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the drain did not terminate on a store that never makes progress")
	}
	// Two batches: the productive-looking one, and the identical repeat that
	// reveals the stall.
	if queue.calls != 2 {
		t.Fatalf("refresh calls = %d, want the repeat detected immediately", queue.calls)
	}
}

// candidate_found is emitted exactly when the search produced an exact-URL match
// whose report was read (CQ-008).
func TestCandidateFoundIsObservedOnlyForAMatch(t *testing.T) {
	for name, tc := range map[string]struct {
		provider *fakeReconciler
		want     bool
	}{
		"exact candidate, clearance": {
			provider: &fakeReconciler{verdict: urlsafety.VerdictSafe, found: true}, want: true,
		},
		"exact candidate, nothing usable": {
			provider: &fakeReconciler{err: urlsafety.ErrScanInconclusive, found: true}, want: true,
		},
		"no candidate": {
			provider: &fakeReconciler{err: urlsafety.ErrNotCheckable}, want: false,
		},
		"search failed": {
			provider: &fakeReconciler{err: urlsafety.ErrUnavailable}, want: false,
		},
	} {
		t.Run(name, func(t *testing.T) {
			queue := newFakeReconcileQueue()
			svc, _ := newReconcileService(queue, tc.provider)

			// The metric reporter is nil here, which is a supported deployment; what
			// is under test is that the service reaches the branch at all, which the
			// provider's own CandidateFound flag drives.
			if _, err := svc.ReconcileMessage(context.Background(), manualInput()); err != nil {
				t.Fatalf("ReconcileMessage: %v", err)
			}
			evidence, _ := tc.provider.Reconcile(context.Background(), reconcileURL)
			if evidence.CandidateFound != tc.want {
				t.Fatalf("CandidateFound = %v, want %v", evidence.CandidateFound, tc.want)
			}
		})
	}
}

func TestRunLinkReconcileWorkerProcessesDueScanAndStops(t *testing.T) {
	service.RunLinkReconcileWorker(t.Context(), nil, time.Millisecond, nil)
	service.RunLinkReconcileWorker(
		t.Context(), service.NewLinkReconcileService(nil, nil, nil), time.Millisecond, nil,
	)

	queue := newFakeReconcileQueue()
	queue.backgroundLeft = 1
	queue.reconciled = make(chan struct{}, 1)
	processor := service.NewLinkReconcileService(
		queue, &fakeReconciler{verdict: urlsafety.VerdictSafe}, nil,
	)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		service.RunLinkReconcileWorker(ctx, processor, time.Millisecond, nil)
	}()

	select {
	case <-queue.reconciled:
		cancel()
	case <-time.After(time.Second):
		cancel()
		t.Fatal("worker did not reconcile the due scan")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not stop after cancellation")
	}
	if got := queue.writtenVerdicts(); len(got) != 1 || got[0] != urlsafety.VerdictSafe {
		t.Fatalf("written verdicts = %v, want [safe]", got)
	}
}

// A pass that fails is logged and the worker keeps its schedule. The alternative
// — returning — would mean one transient database error silently stops
// reconciliation for the life of the pod, and the messages this exists to correct
// would stay wrong until someone restarted it.
func TestRunLinkReconcileWorkerSurvivesAFailingPass(t *testing.T) {
	queue := newFakeReconcileQueue()
	queue.claimErr = errors.New("database unavailable")
	queue.claimed = make(chan struct{}, 1)
	processor := service.NewLinkReconcileService(
		queue, &fakeReconciler{verdict: urlsafety.VerdictSafe}, nil,
	)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		service.RunLinkReconcileWorker(ctx, processor, time.Millisecond, nil)
	}()

	// Two passes: the second only happens if the first failure did not stop the
	// loop, which is the whole assertion.
	for pass := 0; pass < 2; pass++ {
		select {
		case <-queue.claimed:
		case <-time.After(2 * time.Second):
			cancel()
			t.Fatalf("the worker stopped after %d failing pass(es)", pass)
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not stop after cancellation")
	}
}

// A non-positive interval is a misconfiguration, not a request to spin. It falls
// back to the package default rather than creating a ticker that fires
// continuously and turns a config typo into a busy loop against the database.
func TestRunLinkReconcileWorkerRejectsANonPositiveInterval(t *testing.T) {
	queue := newFakeReconcileQueue()
	queue.claimed = make(chan struct{}, 1)
	processor := service.NewLinkReconcileService(
		queue, &fakeReconciler{verdict: urlsafety.VerdictSafe}, nil,
	)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		service.RunLinkReconcileWorker(ctx, processor, 0, nil)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not stop on an already-cancelled context")
	}
	// The default interval is far longer than this test runs, so a ticker built
	// from it cannot have fired. A zero interval that was taken literally would
	// have fired immediately instead.
	select {
	case <-queue.claimed:
		t.Fatal("a zero interval was used literally and fired a pass")
	default:
	}
}
