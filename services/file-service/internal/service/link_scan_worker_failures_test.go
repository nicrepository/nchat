package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/observability"
	"github.com/nicrepository/nchat/libs/go/platform/urlsafety"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
)

// What the worker does when the parts around it fail.
//
// One property runs through all of them: a failure never turns into a verdict
// and never buys a second scan. A pass that cannot prune, cannot read the
// backlog or cannot write an answer still completes, leaves the row where it is,
// and lets the next pass try again.

var errLinkBoom = errors.New("boom")

// Housekeeping is best-effort. Losing it costs a gauge and a table row, not the
// work the pass actually did.
func TestHousekeepingFailuresDoNotFailThePass(t *testing.T) {
	store := newFakeLinkStore()
	store.pruneErr = errLinkBoom
	store.backlogErr = errLinkBoom
	queueScan(t, store, testURL)

	worker := service.NewLinkScanService(store, &fakeLinkProvider{scanID: "scan-1"}, nil)
	worker.SetMetrics(urlsafety.NewPipelineMetrics(observability.NewMetrics(observability.Config{
		ServiceName: "file-service", MetricsEnabled: true,
	}), "file-service"))

	moved, err := worker.ProcessDue(context.Background())

	if err != nil || moved != 1 {
		t.Fatalf("moved=%d err=%v, want the pass to have survived its housekeeping", moved, err)
	}
	if store.row(testURL).state != service.StatePolling {
		t.Fatalf("row=%+v, want the submission to have landed anyway", store.row(testURL))
	}
}

// Not being able to record the intent means nothing may be submitted: a
// submission the database does not know about is the unrecoverable state the
// ordering exists to prevent.
func TestNothingIsSubmittedWhenTheIntentCannotBeRecorded(t *testing.T) {
	store := newFakeLinkStore()
	store.beginErr = errLinkBoom
	queueScan(t, store, testURL)
	provider := &fakeLinkProvider{scanID: "scan-1"}

	if _, err := service.NewLinkScanService(store, provider, nil).
		ProcessDue(context.Background()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	if submits, _ := provider.counts(); submits != 0 {
		t.Fatalf("submits=%d, want the provider never called", submits)
	}
}

// Failing to spend the allowance is not permission to spend it. Fail closed.
func TestAnUnreadableAllowanceRefusesTheSubmission(t *testing.T) {
	store := newFakeLinkStore()
	store.reserveErr = errLinkBoom
	queueScan(t, store, testURL)
	provider := &fakeLinkProvider{scanID: "scan-1"}

	worker := service.NewLinkScanService(store, provider, nil)
	worker.SetCapacity(service.LinkScanWorkerCapacity{
		ProviderSubmitLimit: 10, ProviderSubmitWindow: time.Minute,
	})
	if _, err := worker.ProcessDue(context.Background()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	if submits, _ := provider.counts(); submits != 0 {
		t.Fatalf("submits=%d, want nothing spent against an unknown allowance", submits)
	}
}

// A provider exchange this service could not complete becomes an outstanding
// attempt rather than a row that looks unsent — that is the whole point of
// recording the intent first. A timeout after acceptance and a refusal are
// indistinguishable from here, so both take this path.
func TestAFailedExchangeLeavesTheAttemptOutstanding(t *testing.T) {
	store := newFakeLinkStore()
	queueScan(t, store, testURL)
	provider := &fakeLinkProvider{submitErr: errLinkBoom}

	worker := service.NewLinkScanService(store, provider, nil)
	if _, err := worker.ProcessDue(context.Background()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	if state := store.row(testURL).state; state != service.StateSubmitUncertain {
		t.Fatalf("state=%q, want the attempt parked as uncertain", state)
	}

	// And the next pass reconciles it instead of sending it again.
	store.releaseLeases()
	if _, err := worker.ProcessDue(context.Background()); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if submits, _ := provider.counts(); submits != 1 {
		t.Fatalf("submits=%d, want the failed exchange never repeated", submits)
	}
}

// Even the parking write can fail. The pass still completes and the intent is
// still recorded, which is what stops the row from reading as never-submitted.
func TestAFailedParkingWriteStillRecordsTheIntent(t *testing.T) {
	store := newFakeLinkStore()
	store.uncertainErr = errLinkBoom
	queueScan(t, store, testURL)
	provider := &searchingLinkProvider{
		fakeLinkProvider: &fakeLinkProvider{submitErr: errLinkBoom},
		answers:          []linkSearchAnswer{{err: urlsafety.ErrNotCheckable}},
	}

	worker := service.NewLinkScanService(store, provider, nil)
	if _, err := worker.ProcessDue(context.Background()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	if state := store.row(testURL).state; state != service.StateSubmitting {
		t.Fatalf("state=%q, want the outstanding attempt left in submitting", state)
	}

	// A lease expiry leaves the failed parking write in submitting. It is still
	// an outstanding provider attempt, so the next worker searches rather than
	// issuing a second Submit.
	store.releaseLeases()
	if _, err := worker.ProcessDue(context.Background()); err != nil {
		t.Fatalf("reconciliation pass: %v", err)
	}
	if submits, _ := provider.counts(); submits != 1 {
		t.Fatalf("submits=%d, want exactly the original failed exchange", submits)
	}
	if provider.searches != 1 {
		t.Fatalf("searches=%d, want reconciliation of the outstanding attempt", provider.searches)
	}
}

// A provider can accept the scan while the UUID write and the subsequent
// parking write both fail. The durable submitting intent is enough to make a
// later worker reconcile and adopt the recovered UUID, never submit again.
func TestAcceptedSubmissionWithFailedParkingWriteIsRecoveredWithoutResubmission(t *testing.T) {
	store := newFakeLinkStore()
	store.persistErr = errLinkBoom
	store.uncertainErr = errLinkBoom
	queueScan(t, store, testURL)
	provider := &searchingLinkProvider{
		fakeLinkProvider: &fakeLinkProvider{scanID: "scan-recovered"},
		answers: []linkSearchAnswer{{
			record: urlsafety.ScanRecord{UUID: "scan-recovered"}, match: 1,
		}},
	}
	worker := uncertainWorker(store, provider)

	if _, err := worker.ProcessDue(context.Background()); err != nil {
		t.Fatalf("submit pass: %v", err)
	}
	if state := store.row(testURL).state; state != service.StateSubmitting {
		t.Fatalf("state=%q, want the unparked attempt to remain submitting", state)
	}

	store.releaseLeases()
	if _, err := worker.ProcessDue(context.Background()); err != nil {
		t.Fatalf("reconciliation pass: %v", err)
	}
	row := store.row(testURL)
	if row.state != service.StatePolling || row.scanUUID != "scan-recovered" {
		t.Fatalf("row=%+v, want the recovered UUID adopted for polling", row)
	}
	if submits, _ := provider.counts(); submits != 1 {
		t.Fatalf("submits=%d, want exactly the accepted original submission", submits)
	}
	if provider.searches != 1 {
		t.Fatalf("searches=%d, want one reconciliation", provider.searches)
	}
}

// A verdict that cannot be written is not a verdict. The row stays undecided and
// the preview stays unresolved.
func TestAVerdictThatCannotBeWrittenDecidesNothing(t *testing.T) {
	store := newFakeLinkStore()
	queueScan(t, store, testURL)
	provider := &fakeLinkProvider{scanID: "scan-1", verdict: urlsafety.VerdictSafe}
	worker := service.NewLinkScanService(store, provider, nil)

	if _, err := worker.ProcessDue(context.Background()); err != nil {
		t.Fatalf("submit pass: %v", err)
	}
	store.releaseLeases()
	store.verdictErr = errLinkBoom
	if _, err := worker.ProcessDue(context.Background()); err != nil {
		t.Fatalf("poll pass: %v", err)
	}

	row := store.row(testURL)
	if row.state == "done" {
		t.Fatalf("row=%+v, want a failed write to have decided nothing", row)
	}
	if _, found, err := store.LoadVerdict(context.Background(), testURL); err != nil || found {
		t.Fatalf("a failed write served a verdict: found=%v (%v)", found, err)
	}
}

// A provider client that cannot search leaves the attempt where it is: the
// horizon is a reporting threshold, never a switch that permits resubmission.
func TestAnUncertainAttemptIsNotResubmittedWithoutASearcher(t *testing.T) {
	store := newFakeLinkStore()
	store.persistErr = errLinkBoom
	queueScan(t, store, testURL)
	provider := &fakeLinkProvider{scanID: "scan-1"}

	worker := service.NewLinkScanService(store, provider, nil)
	worker.SetPersistRetryDelay(0)
	// A zero timeout is not "no horizon": it is replaced by the default, so the
	// threshold cannot be switched off by configuration.
	worker.SetCapacity(service.LinkScanWorkerCapacity{})

	if _, err := worker.ProcessDue(context.Background()); err != nil {
		t.Fatalf("submit pass: %v", err)
	}
	store.releaseLeases()
	if _, err := worker.ProcessDue(context.Background()); err != nil {
		t.Fatalf("reconcile pass: %v", err)
	}
	if submits, _ := provider.counts(); submits != 1 {
		t.Fatalf("submits=%d, want an uncertain attempt never resubmitted", submits)
	}
}

// A search the provider could not answer is not an absence. Nothing is adopted
// and nothing is sent again.
func TestASearchThatCannotAnswerAdoptsNothing(t *testing.T) {
	for name, answer := range map[string]linkSearchAnswer{
		"the client cannot search": {err: urlsafety.ErrSearchUnsupported},
		"the search failed":        {err: errLinkBoom},
	} {
		t.Run(name, func(t *testing.T) {
			store := newFakeLinkStore()
			store.persistErr = errLinkBoom
			queueScan(t, store, testURL)
			provider := &searchingLinkProvider{
				fakeLinkProvider: &fakeLinkProvider{scanID: "scan-1"},
				answers:          []linkSearchAnswer{answer},
			}
			worker := uncertainWorker(store, provider)

			if _, err := worker.ProcessDue(context.Background()); err != nil {
				t.Fatalf("submit pass: %v", err)
			}
			store.releaseLeases()
			if _, err := worker.ProcessDue(context.Background()); err != nil {
				t.Fatalf("reconcile pass: %v", err)
			}
			if len(store.adopted) != 0 {
				t.Fatalf("adopted=%v, want an unanswered search to adopt nothing", store.adopted)
			}
			if submits, _ := provider.counts(); submits != 1 {
				t.Fatalf("submits=%d, want no resubmission", submits)
			}
		})
	}
}

// Several eligible scans usually means an earlier duplicate really exists. It is
// counted, the newest is adopted, and nothing is submitted again.
func TestAnAmbiguousSearchAdoptsTheNewestScan(t *testing.T) {
	store := newFakeLinkStore()
	store.persistErr = errLinkBoom
	queueScan(t, store, testURL)
	provider := &searchingLinkProvider{
		fakeLinkProvider: &fakeLinkProvider{scanID: "scan-1"},
		answers: []linkSearchAnswer{
			{record: urlsafety.ScanRecord{UUID: "scan-newest"}, match: 3},
		},
	}
	worker := uncertainWorker(store, provider)

	if _, err := worker.ProcessDue(context.Background()); err != nil {
		t.Fatalf("submit pass: %v", err)
	}
	store.releaseLeases()
	if _, err := worker.ProcessDue(context.Background()); err != nil {
		t.Fatalf("reconcile pass: %v", err)
	}
	if len(store.adopted) != 1 || store.adopted[0] != "scan-newest" {
		t.Fatalf("adopted=%v, want the newest eligible scan", store.adopted)
	}
	if submits, _ := provider.counts(); submits != 1 {
		t.Fatalf("submits=%d, want no submission during reconciliation", submits)
	}
}

// A cancelled context is a shutdown, not a provider incident: nothing is logged
// as a failure and nothing is decided on the way out.
func TestACancelledPassDecidesNothing(t *testing.T) {
	store := newFakeLinkStore()
	store.beginErr = errLinkBoom
	queueScan(t, store, testURL)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := service.NewLinkScanService(store, &fakeLinkProvider{}, nil).
		ProcessDue(ctx); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	if store.row(testURL).state == "done" {
		t.Fatal("a cancelled pass decided a row")
	}
}
