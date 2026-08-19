package service_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/urlsafety"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
)

// The worker's recovery pass for a scan that finished without a verdict
// (issue #135).
//
// The preview gate is fail-closed and stays that way: only an explicit,
// unexpired `safe` opens it. That is exactly why this pass has to exist — without
// it, a URL whose scan came back empty would never be previewable again, because
// nothing else in the system would ever produce a clearance for it.
//
// What must remain impossible is buying a second Cloudflare scan for that URL.
// The provider was already asked, already answered, and in the production case
// this feature exists for it answered by refusing precisely because the hostname
// had been scanned too recently. So every test below asserts the submit counter
// as well as the outcome.

// reconcilingProvider is fakeLinkProvider plus the one method the recovery path
// depends on. It is a separate type so that a provider *without* Reconcile stays
// exercised too: a deployment whose client cannot search must keep working.
type reconcilingProvider struct {
	fakeLinkProvider

	reconcileMu sync.Mutex
	reconciles  int
	verdictOut  urlsafety.Verdict
	observedOut time.Time
	errOut      error
	askedURLs   []string
}

func (p *reconcilingProvider) Reconcile(
	_ context.Context, canonicalURL string,
) (urlsafety.ScanEvidence, error) {
	p.reconcileMu.Lock()
	defer p.reconcileMu.Unlock()
	p.reconciles++
	p.askedURLs = append(p.askedURLs, canonicalURL)
	observed := p.observedOut
	if observed.IsZero() && p.errOut == nil {
		observed = time.Now()
	}
	return urlsafety.ScanEvidence{
		Verdict: p.verdictOut, ObservedAt: observed, CandidateFound: p.errOut == nil,
	}, p.errOut
}

func (p *reconcilingProvider) reconcileCalls() int {
	p.reconcileMu.Lock()
	defer p.reconcileMu.Unlock()
	return p.reconciles
}

// seedInconclusive drives a URL through the real worker to the terminal state
// this pass recovers from, rather than writing the row by hand — the point is
// that the state the poll produces is the state the recovery consumes.
func seedInconclusive(t *testing.T, store *fakeLinkStore, url string) {
	t.Helper()
	provider := &fakeLinkProvider{scanID: "scan-terminal", pollErr: urlsafety.ErrScanInconclusive}
	worker := service.NewLinkScanService(store, provider, nil)
	if _, err := store.AdmitScan(context.Background(), url, service.LinkScanCapacity{}); err != nil {
		t.Fatalf("AdmitScan: %v", err)
	}
	// Two passes: one to submit, one to poll. The worker never does both for one
	// row in a single pass.
	for pass := 0; pass < 2; pass++ {
		if _, err := worker.ProcessDue(context.Background()); err != nil {
			t.Fatalf("seed pass %d: %v", pass, err)
		}
		store.releaseLeases()
	}
	if got := store.row(url).state; got != "inconclusive" {
		t.Fatalf("seed left state %q, want inconclusive", got)
	}
}

// A verdict the provider can finally produce restores the preview — and costs no
// submission.
func TestWorkerReconcilesAnInconclusiveScanToSafe(t *testing.T) {
	store := newFakeLinkStore()
	seedInconclusive(t, store, testURL)

	provider := &reconcilingProvider{verdictOut: urlsafety.VerdictSafe}
	worker := service.NewLinkScanService(store, provider, nil)

	if _, err := worker.ProcessDue(context.Background()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}

	verdict, ok, err := store.LoadVerdict(context.Background(), testURL)
	if err != nil {
		t.Fatalf("LoadVerdict: %v", err)
	}
	if !ok || verdict != urlsafety.VerdictSafe {
		t.Fatalf("verdict = %q ok=%v, want a live clearance", verdict, ok)
	}
	if submits, _ := provider.counts(); submits != 0 {
		t.Fatalf("recovery submitted %d scan(s); it must never submit", submits)
	}
	if provider.reconcileCalls() != 1 {
		t.Fatalf("reconcile calls = %d, want one", provider.reconcileCalls())
	}
}

// The other direction, which is what makes this a security mechanism rather than
// a convenience: recovery may condemn a link too.
func TestWorkerReconcilesAnInconclusiveScanToMalicious(t *testing.T) {
	store := newFakeLinkStore()
	seedInconclusive(t, store, testURL)

	provider := &reconcilingProvider{verdictOut: urlsafety.VerdictMalicious}
	worker := service.NewLinkScanService(store, provider, nil)

	if _, err := worker.ProcessDue(context.Background()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	verdict, ok, err := store.LoadVerdict(context.Background(), testURL)
	if err != nil {
		t.Fatalf("LoadVerdict: %v", err)
	}
	if !ok || verdict != urlsafety.VerdictMalicious {
		t.Fatalf("verdict = %q ok=%v, want malicious", verdict, ok)
	}
	if submits, _ := provider.counts(); submits != 0 {
		t.Fatalf("recovery submitted %d scan(s)", submits)
	}
}

// Everything that is not an explicit answer leaves the row exactly as it was.
// This is the fail-closed half: the service's permission to fetch the URL is
// only ever restored by a clearance read from a full provider report.
func TestWorkerLeavesAnInconclusiveScanAloneWithoutAnAnswer(t *testing.T) {
	for name, outcome := range map[string]error{
		"no candidate":       urlsafety.ErrNotCheckable,
		"still inconclusive": urlsafety.ErrScanInconclusive,
		"still running":      urlsafety.ErrScanPending,
		"provider failed":    urlsafety.ErrUnavailable,
		"cannot search":      urlsafety.ErrSearchUnsupported,
	} {
		t.Run(name, func(t *testing.T) {
			store := newFakeLinkStore()
			seedInconclusive(t, store, testURL)

			provider := &reconcilingProvider{errOut: outcome}
			worker := service.NewLinkScanService(store, provider, nil)

			if _, err := worker.ProcessDue(context.Background()); err != nil {
				t.Fatalf("ProcessDue: %v", err)
			}
			row := store.row(testURL)
			if row.state != "inconclusive" {
				t.Fatalf("state = %q, want the row untouched", row.state)
			}
			if row.scanUUID != "scan-terminal" {
				t.Fatalf("scan uuid = %q, want it preserved", row.scanUUID)
			}
			if _, ok, _ := store.LoadVerdict(context.Background(), testURL); ok {
				t.Fatal("a non-answer produced a usable verdict")
			}
			if submits, _ := provider.counts(); submits != 0 {
				t.Fatalf("recovery submitted %d scan(s)", submits)
			}
		})
	}
}

// The bound that makes this terminate. The claim consumes one of a fixed number
// of attempts whether or not the provider answers, and nothing refills it — so
// an unreachable Cloudflare costs a handful of searches, not an endless loop.
func TestWorkerReconciliationIsBounded(t *testing.T) {
	store := newFakeLinkStore()
	seedInconclusive(t, store, testURL)

	provider := &reconcilingProvider{errOut: urlsafety.ErrUnavailable}
	worker := service.NewLinkScanService(store, provider, nil)

	for pass := 0; pass < fakeReconcileAttemptCap+4; pass++ {
		if _, err := worker.ProcessDue(context.Background()); err != nil {
			t.Fatalf("pass %d: %v", pass, err)
		}
	}
	if provider.reconcileCalls() != fakeReconcileAttemptCap {
		t.Fatalf("reconcile calls = %d, want exactly the %d automatic attempts",
			provider.reconcileCalls(), fakeReconcileAttemptCap)
	}
	if submits, _ := provider.counts(); submits != 0 {
		t.Fatalf("recovery submitted %d scan(s)", submits)
	}
}

// A provider client that cannot reconcile is a working deployment: the pass
// simply does nothing, the row stays terminal, and — above all — nothing is
// submitted in its place.
func TestWorkerWithoutAReconcilerLeavesTheRowTerminal(t *testing.T) {
	store := newFakeLinkStore()
	seedInconclusive(t, store, testURL)

	provider := &fakeLinkProvider{}
	worker := service.NewLinkScanService(store, provider, nil)

	if _, err := worker.ProcessDue(context.Background()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	if got := store.row(testURL).state; got != "inconclusive" {
		t.Fatalf("state = %q, want inconclusive", got)
	}
	if submits, _ := provider.counts(); submits != 0 {
		t.Fatalf("a deployment without reconciliation submitted %d scan(s)", submits)
	}
}
