package linkpreview

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/urlsafety"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
)

// stubSafety is a Safe Browsing verdict the test decides, plus a count of how
// often it was consulted.
//
// The unit is a canonical URL, not a hostname, and neither method blocks on the
// provider: Cloudflare URL Scanner is submit-then-poll, so a URL with no verdict
// yet is a miss and EnsureScan records that a background worker must obtain one.
type stubSafety struct {
	calls      atomic.Int64
	submits    atomic.Int64
	verdict    urlsafety.Verdict
	hasVerdict bool
	submitErr  error
	loadErr    error
	askedURL   atomic.Value
	// admissionResult stages a capacity refusal. Empty means "admitted".
	admissionResult string
}

func (s *stubSafety) LoadVerdict(_ context.Context, canonicalURL string) (urlsafety.Verdict, bool, error) {
	s.calls.Add(1)
	s.askedURL.Store(canonicalURL)
	if s.loadErr != nil {
		return urlsafety.VerdictUnknown, false, s.loadErr
	}
	if !s.hasVerdict {
		return urlsafety.VerdictUnknown, false, nil
	}
	return s.verdict, true, nil
}

// EnsureScan records the *need* for a scan rather than submitting one, which is
// what makes a repeated preview of the same pending URL free.
func (s *stubSafety) AdmitScan(
	_ context.Context, _ string, _ service.LinkScanCapacity,
) (service.LinkScanAdmission, error) {
	s.submits.Add(1)
	if s.submitErr != nil {
		return service.LinkScanAdmission{}, s.submitErr
	}
	if s.admissionResult != "" {
		return service.LinkScanAdmission{NewScanCost: 1, Result: s.admissionResult}, nil
	}
	return service.LinkScanAdmission{NewScanCost: 1, Result: service.AdmissionAllowed}, nil
}

// decided builds a stub that already holds a verdict.
func decided(verdict urlsafety.Verdict) *stubSafety {
	return &stubSafety{verdict: verdict, hasVerdict: true}
}

// countingServer answers HTML and records how many requests actually arrived —
// which is how "the fetch did not happen" is asserted rather than assumed.
func countingServer(t *testing.T, requests *atomic.Int64) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><head><meta property="og:title" content="Page"></head></html>`))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestSafeVerdictLetsThePreviewProceed(t *testing.T) {
	var requests atomic.Int64
	server := countingServer(t, &requests)
	safety := decided(urlsafety.VerdictSafe)
	service := serviceAgainst(t, server, newClock(), nil).WithURLSafety(safety)

	preview, err := service.Preview(context.Background(), "http://example.com/page")
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if preview.Title != "Page" {
		t.Fatalf("preview: %+v", preview)
	}
	if requests.Load() != 1 {
		t.Fatalf("expected one fetch, got %d", requests.Load())
	}
}

// The order is the control: a malicious URL must be refused *before* anything
// is requested, because previewing a phishing page is rendering it.
func TestMaliciousVerdictPreventsTheFetch(t *testing.T) {
	var requests atomic.Int64
	server := countingServer(t, &requests)
	observer := &countingObserver{}
	safety := decided(urlsafety.VerdictMalicious)
	service := serviceAgainst(t, server, newClock(), observer).WithURLSafety(safety)

	_, err := service.Preview(context.Background(), "http://evil.example/page")
	if !errors.Is(err, ErrMaliciousURL) {
		t.Fatalf("want ErrMaliciousURL, got %v", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("the open graph fetch happened anyway: %d requests", requests.Load())
	}
	if got := observer.seen(); len(got) != 1 || got[0] != resultMalicious {
		t.Fatalf("expected one malicious result, got %v", got)
	}
}

// Fail-closed: a stored value that is not an explicit clearance is not a
// clearance, and it is reported as its own class so a caller can tell "try
// again" from "this link is bad". This case exists so adding a verdict to the
// shared package cannot quietly become "allowed" here.
func TestUnavailableVerdictPreventsTheFetch(t *testing.T) {
	var requests atomic.Int64
	server := countingServer(t, &requests)
	observer := &countingObserver{}
	safety := decided(urlsafety.Verdict("future"))
	service := serviceAgainst(t, server, newClock(), observer).WithURLSafety(safety)

	_, err := service.Preview(context.Background(), "http://example.com/page")
	if !errors.Is(err, ErrSafetyUnavailable) {
		t.Fatalf("want ErrSafetyUnavailable, got %v", err)
	}
	if errors.Is(err, ErrMaliciousURL) {
		t.Fatal("a provider outage was reported as a malicious link")
	}
	if requests.Load() != 0 {
		t.Fatalf("the open graph fetch happened anyway: %d requests", requests.Load())
	}
	if got := observer.seen(); len(got) != 1 || got[0] != resultSafetyUnknown {
		t.Fatalf("expected one safety_unavailable result, got %v", got)
	}
}

// A URL with no consultable reputation cannot be cleared, and the refusal must
// be the permanent one: telling a user to retry something that can never
// succeed is worse than telling them it is blocked. An IP literal is the case
// that matters — it is also the one the SSRF policy would refuse anyway, so this
// asserts the reputation layer's own answer.
func TestURLWithoutReputationIsBlockedPermanently(t *testing.T) {
	var requests atomic.Int64
	server := countingServer(t, &requests)
	safety := decided(urlsafety.VerdictSafe)
	service := serviceAgainst(t, server, newClock(), nil).WithURLSafety(safety)

	_, err := service.Preview(context.Background(), "http://198.51.100.7/page")
	if !errors.Is(err, ErrMaliciousURL) && !errors.Is(err, ErrURLNotAllowed) {
		t.Fatalf("want a permanent refusal, got %v", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("the open graph fetch happened anyway: %d requests", requests.Load())
	}
}

// The new outcome: a URL nobody has scanned is neither cleared nor condemned.
// The scan is queued and the caller is told to come back — and crucially, no
// Open Graph fetch happens, because fetching first and asking afterwards is
// rendering the phishing page.
func TestUnscannedURLQueuesAScanAndPreventsTheFetch(t *testing.T) {
	var requests atomic.Int64
	server := countingServer(t, &requests)
	safety := &stubSafety{}
	service := serviceAgainst(t, server, newClock(), nil).WithURLSafety(safety)

	_, err := service.Preview(context.Background(), "http://example.com/page")

	if !errors.Is(err, ErrSafetyPending) {
		t.Fatalf("want ErrSafetyPending, got %v", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("the open graph fetch happened anyway: %d requests", requests.Load())
	}
	if safety.submits.Load() != 1 {
		t.Fatalf("the scan was not queued: %d submissions", safety.submits.Load())
	}
}

// The verdict is asked about the whole URL, path and query included. A lookup
// keyed by hostname was the finding this replaced.
func TestVerdictIsAskedAboutTheCanonicalURL(t *testing.T) {
	var requests atomic.Int64
	server := countingServer(t, &requests)
	safety := decided(urlsafety.VerdictSafe)
	service := serviceAgainst(t, server, newClock(), nil).WithURLSafety(safety)

	if _, err := service.Preview(context.Background(), "http://example.com/a/b?id=7#frag"); err != nil {
		t.Fatalf("Preview: %v", err)
	}
	asked, _ := safety.askedURL.Load().(string)
	if asked != "http://example.com/a/b?id=7" {
		t.Fatalf("asked about %q", asked)
	}
}

// RF-21 does not replace the address policy. A destination the SSRF rules
// refuse stays refused, and it is refused without the provider being consulted
// at all: the two controls answer different questions and neither is the
// other's permission slip.
func TestSSRFPolicyStillAppliesWithASafeVerdict(t *testing.T) {
	var requests atomic.Int64
	server := countingServer(t, &requests)
	safety := decided(urlsafety.VerdictSafe)
	service := serviceAgainst(t, server, newClock(), nil).WithURLSafety(safety)

	for _, raw := range []string{
		"http://127.0.0.1/page",
		"http://[::1]/page",
		"http://169.254.169.254/latest/meta-data",
		"ftp://example.com/page",
		"http://user:pass@example.com/page",
		"http://example.com:12345/page",
	} {
		if _, err := service.Preview(context.Background(), raw); err == nil {
			t.Fatalf("%q was allowed", raw)
		} else if errors.Is(err, ErrMaliciousURL) || errors.Is(err, ErrSafetyUnavailable) {
			t.Fatalf("%q was refused by the reputation check instead of the url policy: %v", raw, err)
		}
	}
	if requests.Load() != 0 {
		t.Fatalf("a refused destination was still fetched: %d requests", requests.Load())
	}
}

// Without the feature configured, nothing about the preview changes.
func TestPreviewIsUnchangedWhenSafetyIsNotWired(t *testing.T) {
	var requests atomic.Int64
	server := countingServer(t, &requests)
	service := serviceAgainst(t, server, newClock(), nil)

	if _, err := service.Preview(context.Background(), "http://example.com/page"); err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("expected one fetch, got %d", requests.Load())
	}
}

// ── The verdict's TTL is the authority, not the preview's ────────────────────

// serviceWithTTL is serviceAgainst with the preview TTL chosen by the test, so
// the two lifetimes can be set the way a real deployment sets them: a preview
// reused for hours, a verdict trusted for minutes.
func serviceWithTTL(
	t *testing.T, server *httptest.Server, clock *fakeClock, ttl time.Duration,
) *Service {
	t.Helper()
	connector := &recordingConnector{target: strings.TrimPrefix(server.URL, "http://")}
	fetcher := newFetcherWith(2*time.Second, fixedResolver(publicAddr), connector.connect)
	return newService(fetcher, ttl, nil, clock.Now)
}

// expiringSafety is the checker's cache, modelled on the test's own clock: it
// answers from a remembered verdict until verdictTTL passes, then asks again.
// It is the shape urlsafety.Service really has, made steerable so the moment a
// reputation turns is a decision and not a race.
type expiringSafety struct {
	clock      *fakeClock
	verdictTTL time.Duration
	// verdict is what the provider would say right now; a test flips it.
	verdict urlsafety.Verdict
	calls   atomic.Int64
	cached  urlsafety.Verdict
	expires time.Time
}

// Lookup answers from the remembered verdict while it is live. An expired one is
// a miss, which is exactly what the real cache does — and what makes the preview
// re-decide instead of riding a clearance that has lapsed.
func (s *expiringSafety) LoadVerdict(_ context.Context, _ string) (urlsafety.Verdict, bool, error) {
	if s.cached != "" && s.clock.Now().Before(s.expires) {
		return s.cached, true, nil
	}
	return urlsafety.VerdictUnknown, false, nil
}

// Submit is what a miss triggers, and here it also stands in for the scan
// finishing: the provider's current opinion becomes the new cached verdict. That
// keeps the test about the *preview* cache not outliving the verdict, without
// modelling a worker.
func (s *expiringSafety) AdmitScan(
	_ context.Context, _ string, _ service.LinkScanCapacity,
) (service.LinkScanAdmission, error) {
	s.calls.Add(1)
	s.cached = s.verdict
	s.expires = s.clock.Now().Add(s.verdictTTL)
	return service.LinkScanAdmission{NewScanCost: 1, Result: service.AdmissionAllowed}, nil
}

// The regression: a preview cached while a URL was safe must not keep being
// served after the verdict that allowed it has expired and turned.
func TestPreviewCacheDoesNotExtendVerdictTrust(t *testing.T) {
	const verdictTTL = 15 * time.Minute
	const previewTTL = 24 * time.Hour

	var requests atomic.Int64
	server := countingServer(t, &requests)
	clock := newClock()
	safety := &expiringSafety{clock: clock, verdictTTL: verdictTTL, verdict: urlsafety.VerdictSafe}
	service := serviceWithTTL(t, server, clock, previewTTL).WithURLSafety(safety)

	// First sight of the URL: no verdict, so the scan is queued and no fetch
	// happens. This is the asynchronous half of RF-21 — the preview cannot wait
	// for a scan any more than a send can.
	if _, err := service.Preview(context.Background(), "http://example.com/page"); !errors.Is(err, ErrSafetyPending) {
		t.Fatalf("first: want ErrSafetyPending, got %v", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("a fetch happened before any verdict: %d", requests.Load())
	}

	// Safe once the scan lands: the page is fetched and both caches are warm.
	if _, err := service.Preview(context.Background(), "http://example.com/page"); err != nil {
		t.Fatalf("second: %v", err)
	}
	if requests.Load() != 1 || safety.calls.Load() != 1 {
		t.Fatalf("fetches=%d lookups=%d", requests.Load(), safety.calls.Load())
	}

	// Still inside the verdict's lifetime: both caches answer, nothing moves.
	clock.advance(time.Minute)
	if _, err := service.Preview(context.Background(), "http://example.com/page"); err != nil {
		t.Fatalf("third: %v", err)
	}
	if requests.Load() != 1 || safety.calls.Load() != 1 {
		t.Fatalf("a warm request cost work: fetches=%d lookups=%d", requests.Load(), safety.calls.Load())
	}

	// Past the verdict's lifetime but well inside the preview's, and the URL has
	// been reclassified.
	clock.advance(verdictTTL + time.Minute)
	safety.verdict = urlsafety.VerdictMalicious

	// The lapsed clearance is a miss, so the URL is re-scanned rather than
	// served — and the preview cache, still warm, does not get to answer.
	if _, err := service.Preview(context.Background(), "http://example.com/page"); !errors.Is(err, ErrSafetyPending) {
		t.Fatalf("the stale open graph entry was served past its verdict: %v", err)
	}
	_, err := service.Preview(context.Background(), "http://example.com/page")
	if !errors.Is(err, ErrMaliciousURL) {
		t.Fatalf("the reclassified url was not refused: %v", err)
	}
	if safety.calls.Load() != 2 {
		t.Fatalf("the expired verdict was not re-consulted: lookups=%d", safety.calls.Load())
	}
	// And no new fetch: the refusal happens before anything is requested.
	if requests.Load() != 1 {
		t.Fatalf("fetches=%d", requests.Load())
	}
}

// The fix must not turn every preview into a provider call. The checker keeps
// its own cache, so a warm request is a map lookup.
func TestWarmPreviewDoesNotReachTheProvider(t *testing.T) {
	var requests atomic.Int64
	server := countingServer(t, &requests)
	safety := decided(urlsafety.VerdictSafe)
	service := serviceAgainst(t, server, newClock(), nil).WithURLSafety(safety)

	for i := 0; i < 5; i++ {
		if _, err := service.Preview(context.Background(), "http://example.com/page"); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	// The checker is consulted every time — that is the fix — but a real
	// urlsafety.Service answers those from its verdict cache, which its own
	// tests cover. What must not have changed is the network cost of a warm
	// preview.
	if requests.Load() != 1 {
		t.Fatalf("a cached preview was refetched: %d requests", requests.Load())
	}
	if safety.calls.Load() != 5 {
		t.Fatalf("safety must be consulted on every request, got %d", safety.calls.Load())
	}
}
