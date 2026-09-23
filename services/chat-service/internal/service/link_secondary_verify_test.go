package service_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/urlsafety"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// The background second opinion (issue #928), through the worker.
//
// The requirement these exist for is a transition, not a provider call: a link
// Google cleared, which Cloudflare later condemns explicitly, must actually
// become malicious — and every other thing Cloudflare can say must leave the
// clearance exactly where it was.

const verifiedURL = "https://cleared.example.test/artigo"

// secondaryProvider is a LinkScanProvider that can also be asked for a second
// opinion, which is what the worker's capability check looks for. Keeping the
// two answers separate is the point: a test can clear a URL through the primary
// path and condemn it through the secondary one, which is the whole scenario.
type secondaryProvider struct {
	mu sync.Mutex

	primary    urlsafety.ReputationResult
	primaryErr error

	secondary     urlsafety.ReputationResult
	secondaryErr  error
	secondaryURLs []string
	secondaryRefs []string
}

func (p *secondaryProvider) Check(
	_ context.Context, _, _ string,
) (urlsafety.ReputationResult, error) {
	return p.primary, p.primaryErr
}

func (p *secondaryProvider) CheckSecondary(
	_ context.Context, canonicalURL, providerRef string,
) (urlsafety.ReputationResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.secondaryURLs = append(p.secondaryURLs, canonicalURL)
	p.secondaryRefs = append(p.secondaryRefs, providerRef)
	return p.secondary, p.secondaryErr
}

func (p *secondaryProvider) secondaryCalls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.secondaryURLs)
}

// googleClears is a provider whose primary answer is a synchronous clearance,
// exactly as the Web Risk adapter's is.
func googleClears() *secondaryProvider {
	return &secondaryProvider{
		primary: urlsafety.ReputationResult{
			Verdict:  urlsafety.ReputationSafe,
			Provider: urlsafety.ProviderGoogleWebRisk,
		},
	}
}

// secondaryWorker wires a worker with a publisher that records the realtime
// link-safety changes, which is how "announced" is asserted.
func secondaryWorker(
	queue service.LinkScanQueue, provider service.LinkScanProvider,
) (*service.LinkScanService, *linkAwarePublisher) {
	publisher := &linkAwarePublisher{}
	return service.NewLinkScanService(queue, provider, publisher, nil), publisher
}

// A clearance from the primary opens the lane, and one that came from the
// secondary itself does not: asking Cloudflare to check Cloudflare's own answer
// is the same opinion at twice the price.
func TestPrimaryClearanceOpensTheSecondOpinionLane(t *testing.T) {
	for name, testCase := range map[string]struct {
		verdict  urlsafety.ReputationVerdict
		provider string
		wantOpen bool
	}{
		"google clearance":     {urlsafety.ReputationSafe, urlsafety.ProviderGoogleWebRisk, true},
		"cloudflare clearance": {urlsafety.ReputationSafe, urlsafety.ProviderCloudflareURLScanner, false},
		"condemnation":         {urlsafety.ReputationMalicious, urlsafety.ProviderGoogleWebRisk, false},
		"terminal non-answer":  {urlsafety.ReputationUnknown, urlsafety.ProviderGoogleWebRisk, false},
	} {
		t.Run(name, func(t *testing.T) {
			queue := newFakeQueue(storage.LinkScanJob{CanonicalURL: verifiedURL})
			provider := &secondaryProvider{primary: urlsafety.ReputationResult{
				Verdict: testCase.verdict, Provider: testCase.provider,
			}}
			worker, _ := secondaryWorker(queue, provider)

			if _, err := worker.ProcessDue(context.Background()); err != nil {
				t.Fatalf("ProcessDue: %v", err)
			}

			opened := len(queue.secondaryOpened) == 1
			if opened != testCase.wantOpen {
				t.Fatalf("lane opened = %v, want %v (opened: %v)",
					opened, testCase.wantOpen, queue.secondaryOpened)
			}
		})
	}
}

// The transition the requirement names, end to end through the worker: the
// target is safe, the secondary condemns it explicitly, and the row becomes
// malicious and is announced — which is what removes the href and the preview
// from every client that already has the message.
func TestSecondaryCondemnationFlipsASafeTargetAndAnnouncesIt(t *testing.T) {
	queue := newFakeQueue()
	queue.secondaryJobs = []storage.LinkSecondaryJob{
		{CanonicalURL: verifiedURL, SecondaryRef: "cf-scan-1"},
	}
	// One published message names this URL; the drain reports it as blocked,
	// which is the change the client receives.
	queue.refreshBatches = [][]storage.MessageLinkSafetyChange{{{
		WorkspaceID: "ws-1", TargetType: storage.TargetChannel, TargetID: "ch-1",
		MessageID: "m-1", State: domain.MessageLinkSafetyMalicious,
		UpdatedAt: time.Now(),
	}}}
	expiry := time.Now().Add(20 * time.Minute)
	provider := googleClears()
	provider.secondary = urlsafety.ReputationResult{
		Verdict:   urlsafety.ReputationMalicious,
		Provider:  urlsafety.ProviderCloudflareURLScanner,
		ExpiresAt: expiry,
	}
	worker, publisher := secondaryWorker(queue, provider)

	if _, err := worker.ProcessDue(context.Background()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}

	// The secondary was asked about this URL, resuming the scan it had already
	// started rather than starting another.
	if provider.secondaryCalls() != 1 {
		t.Fatalf("secondary called %d time(s), want 1", provider.secondaryCalls())
	}
	if provider.secondaryRefs[0] != "cf-scan-1" {
		t.Fatalf("ref = %q, want the outstanding scan", provider.secondaryRefs[0])
	}
	// The target flipped.
	if got := queue.verdicts[verifiedURL]; got != urlsafety.VerdictMalicious {
		t.Fatalf("verdict = %q, want %q", got, urlsafety.VerdictMalicious)
	}
	if _, condemned := queue.secondaryGuilty[verifiedURL]; !condemned {
		t.Fatal("the condemnation did not go through the secondary compare-and-set")
	}
	// And every client holding the message was told, which is what revokes the
	// href and tears down the preview.
	changes := publisher.linkChangeSnapshot()
	if len(changes) != 1 {
		t.Fatalf("realtime changes = %d, want 1", len(changes))
	}
	if changes[0].MessageID != "m-1" || changes[0].State != domain.MessageLinkSafetyMalicious {
		t.Fatalf("announced %+v, want m-1 malicious", changes[0])
	}
}

// Everything the secondary can say that is not an explicit condemnation leaves
// the clearance alone. Each of these is a separate way the fallback could have
// been allowed to downgrade a verdict it did not produce, and none of them is.
func TestSecondaryNonCondemnationsLeaveTheClearanceIntact(t *testing.T) {
	for name, testCase := range map[string]struct {
		result urlsafety.ReputationResult
		err    error
		// settled reports whether the lane should close; a failure keeps it open
		// so the lease retries, and the clearance's own expiry ends it.
		settled bool
	}{
		"cloudflare clears it too": {
			result:  urlsafety.ReputationResult{Verdict: urlsafety.ReputationSafe},
			settled: true,
		},
		"no classification": {
			result:  urlsafety.ReputationResult{Verdict: urlsafety.ReputationUnknown},
			settled: true,
		},
		"hostname limit": {
			result: urlsafety.ReputationResult{
				Verdict: urlsafety.ReputationUnknown, Reason: urlsafety.ReasonHostnameLimit,
			},
			settled: true,
		},
		"unavailable":  {err: urlsafety.ErrUnavailable},
		"circuit open": {err: urlsafety.ErrCircuitOpen},
		"rate limited": {err: urlsafety.ErrUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			queue := newFakeQueue()
			queue.secondaryJobs = []storage.LinkSecondaryJob{
				{CanonicalURL: verifiedURL, SecondaryRef: "cf-scan-1"},
			}
			provider := googleClears()
			provider.secondary, provider.secondaryErr = testCase.result, testCase.err
			worker, publisher := secondaryWorker(queue, provider)

			if _, err := worker.ProcessDue(context.Background()); err != nil {
				t.Fatalf("ProcessDue: %v", err)
			}

			if got, decided := queue.verdicts[verifiedURL]; decided {
				t.Fatalf("the clearance was overwritten with %q", got)
			}
			if len(queue.secondaryGuilty) != 0 {
				t.Fatal("a non-condemnation reached the condemnation path")
			}
			if len(publisher.linkChangeSnapshot()) != 0 {
				t.Fatal("a non-condemnation announced a change")
			}
			settled := len(queue.secondarySettled) == 1
			if settled != testCase.settled {
				t.Fatalf("lane settled = %v, want %v", settled, testCase.settled)
			}
		})
	}
}

// A verification with no scan yet starts one, and the ref is written down so
// the next pass reads that scan instead of starting another.
func TestSecondaryStartsOneScanAndRemembersIt(t *testing.T) {
	queue := newFakeQueue()
	queue.secondaryJobs = []storage.LinkSecondaryJob{{CanonicalURL: verifiedURL}}
	provider := googleClears()
	provider.secondary = urlsafety.ReputationResult{ProviderRef: "cf-scan-9"}
	provider.secondaryErr = urlsafety.ErrCheckInProgress
	worker, _ := secondaryWorker(queue, provider)

	if _, err := worker.ProcessDue(context.Background()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}

	if provider.secondaryRefs[0] != "" {
		t.Fatalf("a first verification must carry no ref, got %q", provider.secondaryRefs[0])
	}
	if queue.secondaryRefs[verifiedURL] != "cf-scan-9" {
		t.Fatalf("ref recorded = %q, want cf-scan-9", queue.secondaryRefs[verifiedURL])
	}
	if len(queue.secondarySettled) != 0 {
		t.Fatal("a verification still running must not close its lane")
	}
}

// The lane never runs without both halves. A provider with no second source, or
// the feature switched off, claims nothing at all — so no row is ever left
// carrying a verification that nothing would drain.
func TestSecondaryLaneIsNotDrainedWithoutAVerifier(t *testing.T) {
	for name, build := range map[string]func() (service.LinkScanProvider, bool){
		"provider cannot verify": func() (service.LinkScanProvider, bool) {
			return &fakeProvider{}, true
		},
		"safety disabled": func() (service.LinkScanProvider, bool) {
			return googleClears(), false
		},
	} {
		t.Run(name, func(t *testing.T) {
			provider, enabled := build()
			queue := newFakeQueue()
			queue.secondaryJobs = []storage.LinkSecondaryJob{{CanonicalURL: verifiedURL}}
			worker, _ := secondaryWorker(queue, provider)
			worker.SetSafetyEnabled(enabled)

			if _, err := worker.ProcessDue(context.Background()); err != nil {
				t.Fatalf("ProcessDue: %v", err)
			}

			if queue.secondaryClaims != 0 {
				t.Fatalf("claimed the lane %d time(s) with nothing to drain it",
					queue.secondaryClaims)
			}
		})
	}
}

// One pass, one exchange per outstanding verification. The lane retries on the
// claim's lease like every other queue here, never in a loop inside a pass.
func TestSecondaryAsksOncePerPass(t *testing.T) {
	queue := newFakeQueue()
	queue.secondaryJobs = []storage.LinkSecondaryJob{
		{CanonicalURL: verifiedURL, SecondaryRef: "cf-1"},
	}
	provider := googleClears()
	provider.secondaryErr = urlsafety.ErrUnavailable
	worker, _ := secondaryWorker(queue, provider)

	if _, err := worker.ProcessDue(context.Background()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}

	if provider.secondaryCalls() != 1 {
		t.Fatalf("secondary called %d time(s) in one pass, want 1", provider.secondaryCalls())
	}
}
