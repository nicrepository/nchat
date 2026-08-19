package urlsafety

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// stubScanner stands in for the provider so the decision layer can be asserted
// without a network. Every field is what the next call answers.
type stubScanner struct {
	mu          sync.Mutex
	submitted   []string
	polled      []string
	scanID      string
	submitErr   error
	verdict     Verdict
	resultErr   error
	blockSubmit chan struct{}
}

func (s *stubScanner) SubmitScan(ctx context.Context, canonicalURL string) (string, error) {
	s.mu.Lock()
	s.submitted = append(s.submitted, canonicalURL)
	s.mu.Unlock()
	if s.blockSubmit != nil {
		select {
		case <-s.blockSubmit:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if s.submitErr != nil {
		return "", s.submitErr
	}
	if s.scanID == "" {
		return "scan-1", nil
	}
	return s.scanID, nil
}

func (s *stubScanner) GetScanResult(_ context.Context, scanID string) (Verdict, error) {
	s.mu.Lock()
	s.polled = append(s.polled, scanID)
	s.mu.Unlock()
	return s.verdict, s.resultErr
}

func (s *stubScanner) submissions() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.submitted...)
}

// --- canonicalization -----------------------------------------------------

// The finding this whole change exists for: a verdict must belong to a URL, not
// to the domain hosting it. These are the pairs that may never collapse.
func TestCanonicalURLsThatMustStayDistinct(t *testing.T) {
	distinct := []string{
		"https://example.com/",
		"https://example.com/login",
		"https://example.com/redirect?to=https://evil.example",
		"https://example.com/redirect?to=https://other.example",
		"https://example.com/download?id=1",
		"https://example.com/download?id=2",
		"http://example.com/",
		"https://example.com:8443/",
		"https://other.example/",
	}
	seen := make(map[string]string, len(distinct))
	for _, raw := range distinct {
		key, err := CanonicalizeURL(raw)
		if err != nil {
			t.Fatalf("CanonicalizeURL(%q): %v", raw, err)
		}
		if previous, clash := seen[key]; clash {
			t.Fatalf("%q and %q share the key %q", previous, raw, key)
		}
		seen[key] = raw
	}
}

// Only transformations that RFC 3986 makes equivalent may collapse.
func TestCanonicalURLsThatMayShareAKey(t *testing.T) {
	for name, pair := range map[string][2]string{
		"fragment only":       {"https://example.com/file#one", "https://example.com/file#two"},
		"fragment or none":    {"https://example.com/file", "https://example.com/file#top"},
		"scheme case":         {"HTTPS://example.com/a", "https://example.com/a"},
		"host case":           {"https://EXAMPLE.com/a", "https://example.com/a"},
		"trailing root dot":   {"https://example.com./a", "https://example.com/a"},
		"default https port":  {"https://example.com:443/a", "https://example.com/a"},
		"default http port":   {"http://example.com:80/a", "http://example.com/a"},
		"empty path or slash": {"https://example.com", "https://example.com/"},
	} {
		t.Run(name, func(t *testing.T) {
			first, err := CanonicalizeURL(pair[0])
			if err != nil {
				t.Fatalf("CanonicalizeURL(%q): %v", pair[0], err)
			}
			second, err := CanonicalizeURL(pair[1])
			if err != nil {
				t.Fatalf("CanonicalizeURL(%q): %v", pair[1], err)
			}
			if first != second {
				t.Fatalf("%q and %q must share a key: %q vs %q", pair[0], pair[1], first, second)
			}
		})
	}
}

// The query is carried through byte for byte: sorting, decoding or collapsing it
// is how two URLs the origin server treats differently end up sharing a verdict.
func TestCanonicalPreservesPathAndQueryVerbatim(t *testing.T) {
	for name, testCase := range map[string]struct{ raw, want string }{
		"query order kept": {
			raw:  "https://example.com/p?b=2&a=1",
			want: "https://example.com/p?b=2&a=1",
		},
		"encoding kept": {
			raw:  "https://example.com/p?to=https%3A%2F%2Fevil.example",
			want: "https://example.com/p?to=https%3A%2F%2Fevil.example",
		},
		"dot segments kept": {
			raw:  "https://example.com/a/../b",
			want: "https://example.com/a/../b",
		},
		"empty query kept as absent": {
			raw:  "https://example.com/p",
			want: "https://example.com/p",
		},
		"punycode applied": {
			raw:  "https://exemplo-Ç.com/p",
			want: "https://xn--exemplo--z0a.com/p",
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := CanonicalizeURL(testCase.raw)
			if err != nil {
				t.Fatalf("CanonicalizeURL: %v", err)
			}
			if got != testCase.want {
				t.Fatalf("got %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestCanonicalRefusesWhatCannotBeScanned(t *testing.T) {
	// gosec reads the "credentials" case as a hardcoded secret. It is the
	// opposite: a URL carrying a user:password@ userinfo component is exactly
	// what canonicalization must refuse, so the component has to be present for
	// the case to test anything. The value is the RFC's own example.com
	// placeholder and nothing here is a real credential.
	for name, raw := range map[string]string{ //nolint:gosec // G101: refusal fixture, not a secret
		"empty":            "",
		"blank":            "   ",
		"no scheme":        "example.com/a",
		"ftp":              "ftp://example.com/a",
		"javascript":       "javascript:alert(1)",
		"data":             "data:text/html,<h1>x</h1>",
		"file":             "file:///etc/passwd",
		"no host":          "https:///a",
		"ipv4 literal":     "https://192.0.2.1/a",
		"ipv6 literal":     "https://[2001:db8::1]/a",
		"loopback literal": "https://127.0.0.1/a",
		"short ipv4":       "http://127.1/a",
		"octal ipv4":       "http://0177.0.0.1/a",
		"hex ipv4":         "http://0x7f.0.0.1/a",
		"credentials":      "https://user:pass@example.com/a",
		"user only":        "https://user@example.com/a",
		"single label":     "https://localhost/a",
		"too long":         "https://example.com/" + strings.Repeat("a", maxURLLength),
	} {
		t.Run(name, func(t *testing.T) {
			got, err := CanonicalizeURL(raw)
			if !errors.Is(err, ErrNotCheckable) {
				t.Fatalf("want ErrNotCheckable, got %q / %v", got, err)
			}
		})
	}
}

// --- verdict strictness ---------------------------------------------------

// The finding: any Verdict value that is not an explicit clearance or an
// explicit condemnation must behave like "no answer".
func TestOnlySafeAndMaliciousAreFinal(t *testing.T) {
	for _, verdict := range []Verdict{VerdictSafe, VerdictMalicious} {
		if !verdict.IsFinal() {
			t.Fatalf("%q must be final", verdict)
		}
	}
	for _, verdict := range []Verdict{"", VerdictUnknown, "future", "SAFE", "safe ", "true"} {
		if verdict.IsFinal() {
			t.Fatalf("%q must not be final", verdict)
		}
	}
}

// Poll is where a provider answer becomes a fact this deployment acts on, so a
// non-final verdict there must be a failure with FailureTTL — not a stored
// verdict, and above all not a clearance.
func TestPollTreatsNonFinalVerdictsAsFailures(t *testing.T) {
	for name, verdict := range map[string]Verdict{
		"zero value": "",
		"unknown":    VerdictUnknown,
		"future":     Verdict("future"),
		"wrong case": Verdict("SAFE"),
	} {
		t.Run(name, func(t *testing.T) {
			clock := &testClock{now: time.Unix(0, 0)}
			scanner := &stubScanner{verdict: verdict}
			service := newService(scanner, nil, clock.Now)

			got, err := service.Poll(context.Background(), "https://example.com/", "scan-1")

			if got == VerdictSafe || !errors.Is(err, ErrUnavailable) {
				t.Fatalf("verdict=%v err=%v", got, err)
			}
			// Cached as a failure, for FailureTTL — so Lookup still misses.
			if _, ok := service.Lookup("https://example.com/"); ok {
				t.Fatal("a failure was served as a cache hit")
			}
			clock.advance(FailureTTL - time.Second)
			if entry, live := service.cache.get("https://example.com/"); !live || entry != VerdictUnknown {
				t.Fatalf("failure must be remembered for FailureTTL: %v live=%v", entry, live)
			}
			clock.advance(2 * time.Second)
			if _, live := service.cache.get("https://example.com/"); live {
				t.Fatal("the failure outlived FailureTTL")
			}
		})
	}
}

// A provider client that returned both a verdict and an error has a bug, and a
// bug must not become a green light.
func TestPollDistrustsAVerdictReturnedWithAnError(t *testing.T) {
	scanner := &stubScanner{verdict: VerdictSafe, resultErr: errors.New("boom")}
	service := newService(scanner, nil, time.Now)

	verdict, err := service.Poll(context.Background(), "https://example.com/", "scan-1")

	if verdict == VerdictSafe || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("verdict=%v err=%v", verdict, err)
	}
}

func TestPollReportsPendingWithoutCachingIt(t *testing.T) {
	clock := &testClock{now: time.Unix(0, 0)}
	scanner := &stubScanner{resultErr: ErrScanPending}
	service := newService(scanner, nil, clock.Now)

	verdict, err := service.Poll(context.Background(), "https://example.com/", "scan-1")

	if !errors.Is(err, ErrScanPending) || verdict == VerdictSafe {
		t.Fatalf("verdict=%v err=%v", verdict, err)
	}
	if _, live := service.cache.get("https://example.com/"); live {
		t.Fatal("a scan still running must not be cached as anything")
	}
}

// TestPollReportsInconclusiveWithoutCachingIt: an inconclusive scan is
// terminal for its scan id, but it is not a URL-level safety clearance, so
// nothing here may cache it — the durable per-scan terminal state belongs to
// the caller's own queue.
func TestPollReportsInconclusiveWithoutCachingIt(t *testing.T) {
	clock := &testClock{now: time.Unix(0, 0)}
	scanner := &stubScanner{resultErr: ErrScanInconclusive}
	service := newService(scanner, nil, clock.Now)

	verdict, err := service.Poll(context.Background(), "https://example.com/", "scan-1")

	if !errors.Is(err, ErrScanInconclusive) || verdict.IsFinal() {
		t.Fatalf("verdict=%v err=%v", verdict, err)
	}
	if _, live := service.cache.get("https://example.com/"); live {
		t.Fatal("an inconclusive scan must not be cached as anything")
	}
	if got, ok := service.Lookup("https://example.com/"); ok {
		t.Fatalf("Lookup must still report a miss after an inconclusive poll, got %v", got)
	}
}

func TestPollCachesAFinalVerdictForVerdictTTL(t *testing.T) {
	for _, want := range []Verdict{VerdictSafe, VerdictMalicious} {
		t.Run(string(want), func(t *testing.T) {
			clock := &testClock{now: time.Unix(0, 0)}
			service := newService(&stubScanner{verdict: want}, nil, clock.Now)

			got, err := service.Poll(context.Background(), "https://example.com/p", "scan-1")
			if err != nil || got != want {
				t.Fatalf("verdict=%v err=%v", got, err)
			}
			if cached, ok := service.Lookup("https://example.com/p"); !ok || cached != want {
				t.Fatalf("cached=%v ok=%v", cached, ok)
			}
			clock.advance(VerdictTTL + time.Second)
			if _, ok := service.Lookup("https://example.com/p"); ok {
				t.Fatal("a verdict outlived VerdictTTL")
			}
		})
	}
}

// --- lookup ---------------------------------------------------------------

// The property the whole async design rests on: the read path never calls the
// provider, so it can never take a scan's worth of time.
func TestLookupNeverReachesTheProvider(t *testing.T) {
	scanner := &stubScanner{verdict: VerdictSafe}
	service := newService(scanner, nil, time.Now)

	if _, ok := service.Lookup("https://example.com/"); ok {
		t.Fatal("an empty cache produced a hit")
	}
	if len(scanner.submissions()) != 0 || len(scanner.polled) != 0 {
		t.Fatal("Lookup contacted the provider")
	}
}

// A safe verdict for one URL may not clear a different one, which is the finding
// restated at the layer that serves the send path.
func TestLookupDoesNotLetOneURLClearAnother(t *testing.T) {
	service := newService(&stubScanner{verdict: VerdictSafe}, nil, time.Now)
	if _, err := service.Poll(context.Background(), "https://trusted.example/", "scan-1"); err != nil {
		t.Fatalf("Poll: %v", err)
	}

	for _, other := range []string{
		"https://trusted.example/phishing",
		"https://trusted.example/redirect?target=https://evil.example",
		"https://trusted.example/download?id=2",
		"http://trusted.example/",
	} {
		if _, ok := service.Lookup(other); ok {
			t.Fatalf("%q inherited a verdict from the domain root", other)
		}
	}
}

func TestRememberSeedsOnlyFinalVerdicts(t *testing.T) {
	clock := &testClock{now: time.Unix(0, 0)}
	service := newService(nil, nil, clock.Now)

	service.Remember("https://example.com/a", VerdictSafe, time.Minute)
	if verdict, ok := service.Lookup("https://example.com/a"); !ok || verdict != VerdictSafe {
		t.Fatalf("verdict=%v ok=%v", verdict, ok)
	}

	for _, verdict := range []Verdict{VerdictUnknown, "", "future"} {
		service.Remember("https://example.com/b", verdict, time.Minute)
		if _, ok := service.Lookup("https://example.com/b"); ok {
			t.Fatalf("%q was seeded into the cache", verdict)
		}
	}
	service.Remember("https://example.com/c", VerdictSafe, 0)
	if _, ok := service.Lookup("https://example.com/c"); ok {
		t.Fatal("an already-expired verdict was seeded")
	}
}

// --- submit ---------------------------------------------------------------

func TestSubmitReturnsTheScanIDAndCountsFailures(t *testing.T) {
	service := newService(&stubScanner{scanID: "scan-9"}, nil, time.Now)
	scanID, err := service.Submit(context.Background(), "https://example.com/")
	if err != nil || scanID != "scan-9" {
		t.Fatalf("scanID=%q err=%v", scanID, err)
	}

	clock := &testClock{now: time.Unix(0, 0)}
	failing := newService(&stubScanner{submitErr: ErrUnavailable}, nil, clock.Now)
	if _, err := failing.Submit(context.Background(), "https://example.com/x"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("want ErrUnavailable, got %v", err)
	}
	// A failed submission is remembered as a failure, so a provider outage costs
	// one attempt per FailureTTL rather than one per message.
	if entry, live := failing.cache.get("https://example.com/x"); !live || entry != VerdictUnknown {
		t.Fatalf("entry=%v live=%v", entry, live)
	}
}

func TestSubmitRefusesAnEmptyScanID(t *testing.T) {
	service := newService(&stubScanner{scanID: "   "}, nil, time.Now)
	if _, err := service.Submit(context.Background(), "https://example.com/"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("want ErrUnavailable, got %v", err)
	}
}

func TestSubmitAndPollWithoutAScannerFail(t *testing.T) {
	service := newService(nil, nil, time.Now)
	if _, err := service.Submit(context.Background(), "https://example.com/"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("submit: %v", err)
	}
	verdict, err := service.Poll(context.Background(), "https://example.com/", "scan-1")
	if verdict == VerdictSafe || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("poll: verdict=%v err=%v", verdict, err)
	}
}

func TestCancellationIsReportedAsItself(t *testing.T) {
	scanner := &stubScanner{blockSubmit: make(chan struct{})}
	service := newService(scanner, nil, time.Now)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := service.Submit(ctx, "https://example.com/"); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	// A cancelled attempt is the caller going away, not a fact about the URL, so
	// nothing about it is remembered.
	if _, live := service.cache.get("https://example.com/"); live {
		t.Fatal("a cancelled submission was cached")
	}
}

// --- cache ----------------------------------------------------------------

func TestCacheIsBounded(t *testing.T) {
	clock := &testClock{now: time.Unix(0, 0)}
	c := newCache(8, clock.Now)
	for i := 0; i < 200; i++ {
		c.set(fmt.Sprintf("https://example.com/%d", i), VerdictSafe, VerdictTTL)
	}
	if len(c.entries) > 8 {
		t.Fatalf("cache grew past its bound: %d", len(c.entries))
	}
}

func TestCacheIsSafeUnderConcurrentUse(t *testing.T) {
	service := newService(&stubScanner{verdict: VerdictSafe}, nil, time.Now)
	var wait sync.WaitGroup
	for i := 0; i < 16; i++ {
		wait.Add(1)
		go func(n int) {
			defer wait.Done()
			key := fmt.Sprintf("https://example.com/%d", n%4)
			for j := 0; j < 100; j++ {
				service.Remember(key, VerdictSafe, VerdictTTL)
				service.Lookup(key)
			}
		}(i)
	}
	wait.Wait()
}

// testClock drives expiry without sleeping.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}
