package service_test

import (
	"context"
	"sync"
	"testing"

	"github.com/nicrepository/nchat/libs/go/platform/urlsafety"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// The scan worker driving the real primary/fallback composition (issue #928).
//
// The composition is unit-tested in libs/go/platform/urlsafety. What this file
// asserts is the join: that the worker's state machine and the composition make
// the promises together that neither makes alone —
//
//   - a URL the local policy refuses reaches *neither* provider. Not "the
//     provider was not called", which a single fake can only ever say about
//     itself, but that neither Google nor Cloudflare received the URL. That is
//     the privacy claim of issue #928 §4, and it is only true if classification
//     runs before the composition, not inside it;
//   - a synchronous clearance from the primary is recorded in the same pass,
//     with no scan outstanding anywhere and no second provider consulted;
//   - a primary outage is covered by the fallback without the row being
//     submitted twice.

// recordingProvider is one half of the composition. It records the URLs it was
// asked about, which is what makes "Cloudflare never saw this" assertable.
type recordingProvider struct {
	mu     sync.Mutex
	name   string
	result urlsafety.ReputationResult
	err    error
	urls   []string
	refs   []string
}

func (p *recordingProvider) Name() string { return p.name }

func (p *recordingProvider) Check(
	_ context.Context, canonicalURL, providerRef string,
) (urlsafety.ReputationResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.urls = append(p.urls, canonicalURL)
	p.refs = append(p.refs, providerRef)
	result := p.result
	result.Provider = p.name
	return result, p.err
}

func (p *recordingProvider) seen() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.urls...)
}

// composedWorker wires the real composition into the worker, exactly as
// app.wireReputationProvider does minus the HTTP clients.
func composedWorker(
	t *testing.T, queue service.LinkScanQueue, primary, secondary *recordingProvider,
) *service.LinkScanService {
	t.Helper()
	provider := urlsafety.NewPrimaryFallbackProvider(primary, secondary, nil)
	return service.NewLinkScanService(queue, urlsafety.NewReputationService(provider, nil), &fakePublisher{}, nil)
}

func safeProvider(name string) *recordingProvider {
	return &recordingProvider{
		name:   name,
		result: urlsafety.ReputationResult{Verdict: urlsafety.ReputationSafe},
	}
}

// The privacy rule, stated where both providers can be watched at once: a URL
// that plausibly carries a secret, or that names a private network, leaves this
// deployment for nobody. Google's Lookup API receives the full URL, so this is
// the check that keeps a password-reset link out of it — and Cloudflare, which
// fetches the URL, must not receive it either.
func TestPolicyRefusedURLsReachNeitherProvider(t *testing.T) {
	for name, testCase := range map[string]struct {
		url    string
		reason string
	}{
		"password reset": {
			"https://app.example.com/reset-password?token=s3cr3t", storage.TerminalReasonSensitive,
		},
		"magic link": {
			"https://app.example.com/magic-link?otp=123456", storage.TerminalReasonSensitive,
		},
		"oauth callback": {
			"https://app.example.com/oauth/callback?code=abc&state=xyz", storage.TerminalReasonSensitive,
		},
		"signed download": {
			"https://bucket.s3.amazonaws.com/f?X-Amz-Signature=deadbeef", storage.TerminalReasonSensitive,
		},
		"internal host": {"https://wiki.internal/runbook", storage.TerminalReasonInternal},
		"corp host":     {"https://jira.corp/browse/X-1", storage.TerminalReasonInternal},
	} {
		t.Run(name, func(t *testing.T) {
			queue := newFakeQueue(storage.LinkScanJob{CanonicalURL: testCase.url})
			primary := safeProvider(urlsafety.ProviderGoogleWebRisk)
			secondary := safeProvider(urlsafety.ProviderCloudflareURLScanner)

			if _, err := composedWorker(t, queue, primary, secondary).
				ProcessDue(context.Background()); err != nil {
				t.Fatalf("ProcessDue: %v", err)
			}

			if seen := primary.seen(); len(seen) != 0 {
				t.Fatalf("the primary received a refused URL: %v", seen)
			}
			if seen := secondary.seen(); len(seen) != 0 {
				t.Fatalf("the fallback received a refused URL: %v", seen)
			}
			if queue.policyTerminals[testCase.url] != testCase.reason {
				t.Fatalf("terminal = %v, want %s", queue.policyTerminals, testCase.reason)
			}
		})
	}
}

// A clearance from the primary decides the target in the pass that asked for
// it: no scan is left outstanding, the fallback is not consulted, and the URL
// the provider was handed is the stored canonical one rather than anything
// re-derived on the way.
func TestPrimaryClearanceIsRecordedWithoutTheFallback(t *testing.T) {
	const url = "https://docs.example.com/guide?page=2"
	queue := newFakeQueue(storage.LinkScanJob{CanonicalURL: url})
	primary := safeProvider(urlsafety.ProviderGoogleWebRisk)
	secondary := safeProvider(urlsafety.ProviderCloudflareURLScanner)

	if _, err := composedWorker(t, queue, primary, secondary).
		ProcessDue(context.Background()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}

	if got := queue.verdicts[url]; got != urlsafety.VerdictSafe {
		t.Fatalf("verdict = %q, want %q", got, urlsafety.VerdictSafe)
	}
	if seen := primary.seen(); len(seen) != 1 || seen[0] != url {
		t.Fatalf("primary saw %v, want exactly the canonical URL once", seen)
	}
	if seen := secondary.seen(); len(seen) != 0 {
		t.Fatalf("the fallback was consulted behind a fresh clearance: %v", seen)
	}
	if primary.refs[0] != "" {
		t.Fatalf("a first attempt must carry no ref, got %q", primary.refs[0])
	}
}

// A primary outage is covered by the fallback inside the same pass, and the
// fallback starts a fresh check rather than inheriting a ref the primary never
// issued.
func TestPrimaryOutageFallsBackWithinOnePass(t *testing.T) {
	const url = "https://news.example.com/article"
	queue := newFakeQueue(storage.LinkScanJob{CanonicalURL: url})
	primary := &recordingProvider{name: urlsafety.ProviderGoogleWebRisk, err: urlsafety.ErrUnavailable}
	secondary := &recordingProvider{
		name:   urlsafety.ProviderCloudflareURLScanner,
		result: urlsafety.ReputationResult{Verdict: urlsafety.ReputationMalicious},
	}

	if _, err := composedWorker(t, queue, primary, secondary).
		ProcessDue(context.Background()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}

	if got := queue.verdicts[url]; got != urlsafety.VerdictMalicious {
		t.Fatalf("verdict = %q, want %q", got, urlsafety.VerdictMalicious)
	}
	if len(primary.seen()) != 1 || len(secondary.seen()) != 1 {
		t.Fatalf("calls: primary %d, fallback %d; want one each",
			len(primary.seen()), len(secondary.seen()))
	}
	if secondary.refs[0] != "" {
		t.Fatalf("the fallback must start a fresh check, got ref %q", secondary.refs[0])
	}
}

// Both sources down is the state machine's existing fail-closed path: nothing
// is written, the attempt stays outstanding, and the target converges at its
// deadline rather than through a clearance nobody gave.
func TestTotalProviderOutageWritesNoVerdict(t *testing.T) {
	const url = "https://news.example.com/article"
	queue := newFakeQueue(storage.LinkScanJob{CanonicalURL: url})
	primary := &recordingProvider{name: urlsafety.ProviderGoogleWebRisk, err: urlsafety.ErrUnavailable}
	secondary := &recordingProvider{
		name: urlsafety.ProviderCloudflareURLScanner, err: urlsafety.ErrUnavailable,
	}

	if _, err := composedWorker(t, queue, primary, secondary).
		ProcessDue(context.Background()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}

	if got, ok := queue.verdicts[url]; ok {
		t.Fatalf("a total outage wrote verdict %q", got)
	}
	if got := queue.submitted[url]; got != "" {
		t.Fatalf("a failed exchange bound scan id %q", got)
	}
}
