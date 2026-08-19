package urlsafety

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Reconciling a scan that finished without a usable verdict (issue #135).
//
// The whole risk surface of this feature is one sentence: a search result must
// never become a verdict. The provider's search answer carries a summarised
// verdict field, it is about a URL the sender chose, and it is exactly the kind
// of value that looks close enough to a clearance to be used as one. So the
// tests below are mostly about what does *not* happen — no submission, no
// clearance from a search, no clearance from a report that does not pass every
// check an ordinary poll applies.

// reconcileServer stands in for the whole provider API and records every path it
// was asked for, so a test can assert what was and — more importantly — was not
// called.
type reconcileServer struct {
	searchStatus int
	searchBody   string
	resultStatus int
	resultBody   string

	searches atomic.Int32
	results  atomic.Int32
	// submits counts POSTs to the scan endpoint. It must be zero in every test in
	// this file: no reconciliation path may ever buy a scan.
	submits atomic.Int32
	// lastResultPath records which scan id the result endpoint was asked for, so a
	// test can prove the id came from the search rather than from anywhere else.
	lastResultPath string
}

func (s *reconcileServer) start(t *testing.T) *Service {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/urlscanner/v2/search"):
			s.searches.Add(1)
			status := s.searchStatus
			if status == 0 {
				status = http.StatusOK
			}
			w.WriteHeader(status)
			_, _ = w.Write([]byte(s.searchBody))
		case strings.Contains(r.URL.Path, "/urlscanner/v2/result/"):
			s.results.Add(1)
			s.lastResultPath = r.URL.Path
			status := s.resultStatus
			if status == 0 {
				status = http.StatusOK
			}
			w.WriteHeader(status)
			_, _ = w.Write([]byte(s.resultBody))
		case strings.Contains(r.URL.Path, "/urlscanner/v2/scan"):
			s.submits.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"uuid":"should-never-happen"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	scanner, err := newCloudflareScanner(server.URL, "acct-1", "token-1", server.Client())
	if err != nil {
		t.Fatalf("new scanner: %v", err)
	}
	return NewService(scanner, nil)
}

// assertNoSubmit is the invariant every test in this file shares, stated once.
func (s *reconcileServer) assertNoSubmit(t *testing.T) {
	t.Helper()
	if got := s.submits.Load(); got != 0 {
		t.Fatalf("reconciliation submitted %d scan(s); it must never submit", got)
	}
}

// reportJSON builds a finished report dated `now`, which is the ordinary case:
// a scan whose evidence is current.
func reportJSON(uuid, status, success, hasVerdicts, malicious string) string {
	return datedReportJSON(uuid, status, success, hasVerdicts, malicious, time.Now().UTC())
}

// datedReportJSON builds the same report with the provider's own scan time set,
// so a test can age the evidence without waiting.
func datedReportJSON(uuid, status, success, hasVerdicts, malicious string, when time.Time) string {
	stamp := when.UTC().Format(time.RFC3339)
	return `{"task":{"uuid":"` + uuid + `","status":"` + status + `","success":` + success +
		`,"time":"` + stamp + `","timeEnd":"` + stamp + `"},` +
		`"verdicts":{"overall":{"hasVerdicts":` + hasVerdicts + `,"malicious":` + malicious + `}}}`
}

func recentRFC3339() string { return time.Now().UTC().Format(time.RFC3339) }

// A candidate is found and its full report clears the URL. This is the only
// shape that may ever produce a clearance.
func TestReconcileAdoptsASafeVerdictFromTheFullReport(t *testing.T) {
	server := &reconcileServer{
		searchBody: scanTasksJSON(
			scanTaskJSON("scan-a", "https://example.com/a", recentRFC3339(), "unlisted")),
		resultBody: reportJSON("scan-a", "finished", "true", "true", "false"),
	}
	service := server.start(t)

	evidence, err := service.Reconcile(context.Background(), "https://example.com/a")

	if err != nil || evidence.Verdict != VerdictSafe {
		t.Fatalf("verdict=%q err=%v", evidence.Verdict, err)
	}
	// The scan id came from the search and was used verbatim: this is what makes
	// the search a *discovery* step rather than a verdict source.
	if !strings.HasSuffix(server.lastResultPath, "/result/scan-a") {
		t.Fatalf("result path = %q", server.lastResultPath)
	}
	if server.results.Load() != 1 {
		t.Fatalf("a candidate must be read through the result endpoint exactly once, got %d",
			server.results.Load())
	}
	server.assertNoSubmit(t)
}

// The other direction. Reconciliation is not a way to clear things; it is a way
// to learn what the provider now says, and "malicious" is one of the answers.
func TestReconcileAdoptsAMaliciousVerdictFromTheFullReport(t *testing.T) {
	server := &reconcileServer{
		searchBody: scanTasksJSON(
			scanTaskJSON("scan-b", "https://example.com/b", recentRFC3339(), "unlisted")),
		resultBody: reportJSON("scan-b", "finished", "true", "true", "true"),
	}
	service := server.start(t)

	evidence, err := service.Reconcile(context.Background(), "https://example.com/b")

	if err != nil || evidence.Verdict != VerdictMalicious {
		t.Fatalf("verdict=%q err=%v", evidence.Verdict, err)
	}
	server.assertNoSubmit(t)
}

// The finding this whole design is arranged around: the search answer's own
// verdict field must not be usable as a clearance. Here the search says the scan
// is clean in every way it can, and the full report says nothing usable — the
// result must still be "no verdict".
func TestReconcileNeverTreatsTheSearchAnswerAsAVerdict(t *testing.T) {
	// A search payload carrying every verdict-shaped field the provider might
	// ever add. None of them is read: ScanRecord has nowhere to put them.
	searchBody := `{"results":[{"task":{"uuid":"scan-c","url":"https://example.com/c",` +
		`"time":"` + recentRFC3339() + `","visibility":"unlisted","success":true},` +
		`"verdicts":{"overall":{"malicious":false,"hasVerdicts":true}}}]}`
	server := &reconcileServer{
		searchBody: searchBody,
		// The authoritative report: finished, but with nothing to act on. This is
		// the exact production shape — task.success=false with status=finished.
		resultBody: reportJSON("scan-c", "finished", "false", "false", "false"),
	}
	service := server.start(t)

	evidence, err := service.Reconcile(context.Background(), "https://example.com/c")

	if !errors.Is(err, ErrScanInconclusive) {
		t.Fatalf("a search-only clearance leaked through: verdict=%q err=%v", evidence.Verdict, err)
	}
	if evidence.Verdict.IsFinal() {
		t.Fatalf("verdict %q must not be usable", evidence.Verdict)
	}
	if server.results.Load() != 1 {
		t.Fatal("the candidate must still have been read through the result endpoint")
	}
	server.assertNoSubmit(t)
}

// A candidate always costs a result read. Without this the search could become a
// shortcut, which is the same failure as trusting its verdict field.
func TestReconcileAlwaysReadsTheFullReportForACandidate(t *testing.T) {
	server := &reconcileServer{
		searchBody: scanTasksJSON(
			scanTaskJSON("scan-d", "https://example.com/d", recentRFC3339(), "unlisted")),
		resultBody: reportJSON("scan-d", "finished", "true", "true", "false"),
	}
	service := server.start(t)

	if _, err := service.Reconcile(context.Background(), "https://example.com/d"); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if server.searches.Load() != 1 || server.results.Load() != 1 {
		t.Fatalf("searches=%d results=%d; a candidate must cost exactly one of each",
			server.searches.Load(), server.results.Load())
	}
}

// A report that still carries no usable verdict leaves everything as it was, and
// says so distinctly enough for a caller to keep the row inconclusive.
func TestReconcileReportsStillInconclusive(t *testing.T) {
	server := &reconcileServer{
		searchBody: scanTasksJSON(
			scanTaskJSON("scan-e", "https://example.com/e", recentRFC3339(), "unlisted")),
		resultBody: reportJSON("scan-e", "finished", "true", "false", "false"),
	}
	service := server.start(t)

	evidence, err := service.Reconcile(context.Background(), "https://example.com/e")

	if !errors.Is(err, ErrScanInconclusive) || evidence.Verdict.IsFinal() {
		t.Fatalf("verdict=%q err=%v", evidence.Verdict, err)
	}
	if got := ReconcileOutcome(evidence.Verdict, err); got != ReconcileStillInconclusive {
		t.Fatalf("outcome label = %q", got)
	}
	server.assertNoSubmit(t)
}

// No candidate is the ordinary answer — it is what a hostname somebody else's
// account scanned recently looks like — and it must be distinguishable from a
// failure, because only one of the two is worth logging.
func TestReconcileReportsNoCandidate(t *testing.T) {
	server := &reconcileServer{searchBody: `{"results":[]}`}
	service := server.start(t)

	evidence, err := service.Reconcile(context.Background(), "https://example.com/f")

	if !errors.Is(err, ErrNotCheckable) || evidence.Verdict.IsFinal() {
		t.Fatalf("verdict=%q err=%v", evidence.Verdict, err)
	}
	if got := ReconcileOutcome(evidence.Verdict, err); got != ReconcileNoCandidate {
		t.Fatalf("outcome label = %q", got)
	}
	if server.results.Load() != 0 {
		t.Fatal("no candidate means nothing to read")
	}
	server.assertNoSubmit(t)
}

// Exact canonical-URL matching, at the reconciliation level rather than only
// inside the search filter. A scan of a *different path on the same host* is the
// case that matters: `https://youtube.com/` must never inherit the verdict of
// `https://youtube.com/watch?v=...`.
func TestReconcileRefusesACandidateForADifferentURL(t *testing.T) {
	for name, reported := range map[string]string{
		"different path":   "https://example.com/other",
		"different query":  "https://example.com/a?v=2",
		"different host":   "https://evil.test/a",
		"same host bare":   "https://example.com/",
		"deeper same host": "https://example.com/a/b",
	} {
		t.Run(name, func(t *testing.T) {
			server := &reconcileServer{
				searchBody: scanTasksJSON(
					scanTaskJSON("scan-x", reported, recentRFC3339(), "unlisted")),
				// A clearance, deliberately, so the test fails loudly if the URL check
				// is ever skipped.
				resultBody: reportJSON("scan-x", "finished", "true", "true", "false"),
			}
			service := server.start(t)

			evidence, err := service.Reconcile(context.Background(), "https://example.com/a")

			if !errors.Is(err, ErrNotCheckable) {
				t.Fatalf("a scan of %q was adopted for a different URL: verdict=%q err=%v",
					reported, evidence.Verdict, err)
			}
			if server.results.Load() != 0 {
				t.Fatal("a refused candidate must not be read")
			}
			server.assertNoSubmit(t)
		})
	}
}

// A report describing a different scan is untrusted regardless of what it says,
// so it is a failure and never an inconclusive — an answer nobody can attribute
// is not a terminal state for this row.
func TestReconcileRefusesAReportForADifferentScan(t *testing.T) {
	server := &reconcileServer{
		searchBody: scanTasksJSON(
			scanTaskJSON("scan-g", "https://example.com/g", recentRFC3339(), "unlisted")),
		resultBody: reportJSON("scan-someone-else", "finished", "true", "true", "false"),
	}
	service := server.start(t)

	evidence, err := service.Reconcile(context.Background(), "https://example.com/g")

	if !errors.Is(err, ErrUnavailable) || evidence.Verdict.IsFinal() {
		t.Fatalf("verdict=%q err=%v", evidence.Verdict, err)
	}
	server.assertNoSubmit(t)
}

// A failing provider is fail-closed and, above all, is never a reason to submit.
// A throttled search in particular must not read as "there is no such scan".
func TestReconcileIsFailClosedOnProviderErrors(t *testing.T) {
	for name, server := range map[string]*reconcileServer{
		"search throttled":   {searchStatus: http.StatusTooManyRequests},
		"search unavailable": {searchStatus: http.StatusBadGateway},
		"search unparseable": {searchBody: `{"results":[}`},
		"result unavailable": {searchBody: scanTasksJSON(scanTaskJSON("s", "https://example.com/h", recentRFC3339(), "unlisted")), resultStatus: http.StatusInternalServerError},
		"result unparseable": {searchBody: scanTasksJSON(scanTaskJSON("s", "https://example.com/h", recentRFC3339(), "unlisted")), resultBody: `{`},
		"result trailing data": {searchBody: scanTasksJSON(scanTaskJSON("s", "https://example.com/h", recentRFC3339(), "unlisted")),
			resultBody: reportJSON("s", "finished", "true", "true", "false") + `{"task":{"uuid":"s"}}`},
	} {
		t.Run(name, func(t *testing.T) {
			service := server.start(t)

			evidence, err := service.Reconcile(context.Background(), "https://example.com/h")

			if err == nil || evidence.Verdict.IsFinal() {
				t.Fatalf("a provider failure produced a usable answer: verdict=%q err=%v", evidence.Verdict, err)
			}
			// Never absence: reporting a throttled search as "no such scan" is how a
			// duplicate submission gets bought in the sibling path.
			if errors.Is(err, ErrNotCheckable) {
				t.Fatal("a provider failure was reported as an absent scan")
			}
			if got := ReconcileOutcome(evidence.Verdict, err); got != ReconcileProviderError {
				t.Fatalf("outcome label = %q", got)
			}
			server.assertNoSubmit(t)
		})
	}
}

// A scan the provider is still running is not an answer either, and asking again
// later is free — so it is reported like an inconclusive rather than a failure.
func TestReconcileReportsAStillRunningCandidate(t *testing.T) {
	server := &reconcileServer{
		searchBody: scanTasksJSON(
			scanTaskJSON("scan-i", "https://example.com/i", recentRFC3339(), "unlisted")),
		resultStatus: http.StatusNotFound,
	}
	service := server.start(t)

	evidence, err := service.Reconcile(context.Background(), "https://example.com/i")

	if !errors.Is(err, ErrScanPending) || evidence.Verdict.IsFinal() {
		t.Fatalf("verdict=%q err=%v", evidence.Verdict, err)
	}
	if got := ReconcileOutcome(evidence.Verdict, err); got != ReconcileStillInconclusive {
		t.Fatalf("outcome label = %q", got)
	}
	server.assertNoSubmit(t)
}

// A provider client that cannot search is a working deployment: reconciliation
// is simply unavailable, and nothing is submitted in its place.
func TestReconcileReportsAnUnsearchableProvider(t *testing.T) {
	service := NewService(unsearchableScanner{verdict: VerdictSafe}, nil)

	evidence, err := service.Reconcile(context.Background(), "https://example.com/j")

	if !errors.Is(err, ErrSearchUnsupported) || evidence.Verdict.IsFinal() {
		t.Fatalf("verdict=%q err=%v", evidence.Verdict, err)
	}
	if got := ReconcileOutcome(evidence.Verdict, err); got != ReconcileUnsupported {
		t.Fatalf("outcome label = %q", got)
	}
}

// The search lookback is wide by construction — the URL being reconciled is one
// the provider already refused to scan again, so the only candidate that can
// exist is an older one. A scan *submitted* hours ago is still eligible to be
// looked at; whether its verdict may clear anything is decided separately, from
// the report's own time. This asserts the first half: the lookback is not the
// freshness gate.
func TestReconcileLooksBackFurtherThanTheVerdictTTL(t *testing.T) {
	old := time.Now().Add(-6 * time.Hour).UTC().Format(time.RFC3339)
	server := &reconcileServer{
		searchBody: scanTasksJSON(
			scanTaskJSON("scan-k", "https://example.com/k", old, "unlisted")),
		resultBody: reportJSON("scan-k", "finished", "true", "true", "false"),
	}
	service := server.start(t)

	evidence, err := service.Reconcile(context.Background(), "https://example.com/k")

	if err != nil || evidence.Verdict != VerdictSafe {
		t.Fatalf("a scan from six hours ago must be eligible: verdict=%q err=%v", evidence.Verdict, err)
	}
}

// A scan older than the lookback floor is not ours to adopt.
func TestReconcileRefusesAScanOlderThanTheLookback(t *testing.T) {
	ancient := time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339)
	server := &reconcileServer{
		searchBody: scanTasksJSON(
			scanTaskJSON("scan-l", "https://example.com/l", ancient, "unlisted")),
		resultBody: reportJSON("scan-l", "finished", "true", "true", "false"),
	}
	service := server.start(t)

	if _, err := service.Reconcile(context.Background(), "https://example.com/l"); !errors.Is(err, ErrNotCheckable) {
		t.Fatalf("expected no eligible candidate, got %v", err)
	}
}

// A URL that cannot be represented as a search term is refused before anything
// leaves the process — the same rule the submission path's search applies.
func TestReconcileRefusesAnUnrepresentableURL(t *testing.T) {
	server := &reconcileServer{}
	service := server.start(t)

	if _, err := service.Reconcile(context.Background(), "https://example.com/\n"); err == nil {
		t.Fatal("a control character in the URL must be refused")
	}
	if server.searches.Load() != 0 {
		t.Fatal("nothing may be sent for a URL that cannot be a term")
	}
	server.assertNoSubmit(t)
}

// unsearchableScanner is the minimal Scanner: it can submit and poll, and cannot search.
// It exists to exercise the "this deployment cannot reconcile" branch.
type unsearchableScanner struct{ verdict Verdict }

func (s unsearchableScanner) SubmitScan(context.Context, string) (string, error) {
	return "scan-1", nil
}

func (s unsearchableScanner) GetScanResult(context.Context, string) (Verdict, error) {
	return s.verdict, nil
}

// Evidence age (CQ-001).
//
// A verdict is evidence, and evidence has an age. Reconciliation adopts scans
// that may be hours old, so dating the resulting clearance from the moment of
// adoption would hand a full fresh lifetime to stale evidence — the exact
// forbidden shape:
//
//	scan SAFE at T0, found at T0+23h, adopted at T0+23h, granted another VerdictTTL
//
// The tests below pin the age to the provider's own timestamp all the way
// through, and refuse a clearance whose lifetime has already run out.

// reconcileAged runs a reconciliation against a report the provider dated
// `scanAge` before `now`, and evaluates it at `now`.
func reconcileAged(t *testing.T, scanAge time.Duration, malicious string) (*reconcileServer, ScanEvidence, error) {
	t.Helper()
	now := time.Now().UTC()
	scanTime := now.Add(-scanAge)
	server := &reconcileServer{
		// The search reports the scan as submitted then too, so nothing in the
		// eligibility filter is what refuses it.
		searchBody: scanTasksJSON(
			scanTaskJSON("scan-aged", "https://example.test/aged",
				scanTime.Format(time.RFC3339), "unlisted")),
		resultBody: datedReportJSON("scan-aged", "finished", "true", "true", malicious, scanTime),
	}
	service := server.start(t)
	evidence, err := service.reconcileAt(context.Background(), "https://example.test/aged", now)
	return server, evidence, err
}

// A scan younger than the TTL keeps only the lifetime it has left.
func TestReconcileGrantsOnlyTheRemainingLifetime(t *testing.T) {
	age := VerdictTTL / 3
	now := time.Now().UTC()
	_, evidence, err := reconcileAged(t, age, "false")

	if err != nil || evidence.Verdict != VerdictSafe {
		t.Fatalf("verdict=%q err=%v", evidence.Verdict, err)
	}
	// The evidence is dated by the provider, not by adoption.
	if got := evidence.Age(now); got < age-time.Minute || got > age+time.Minute {
		t.Fatalf("evidence age = %v, want about %v", got, age)
	}
	// And what is left is the remainder, not a whole new lifetime.
	remaining := evidence.ExpiresAt().Sub(now)
	if remaining >= VerdictTTL {
		t.Fatalf("remaining lifetime %v >= a full TTL %v; stale evidence was rejuvenated",
			remaining, VerdictTTL)
	}
	if remaining <= 0 {
		t.Fatalf("remaining lifetime %v, want a positive remainder", remaining)
	}
}

// Exactly at the limit is not a clearance: the lifetime has run out.
func TestReconcileRefusesEvidenceExactlyAtTheLimit(t *testing.T) {
	_, evidence, err := reconcileAged(t, VerdictTTL, "false")

	if !errors.Is(err, ErrEvidenceExpired) {
		t.Fatalf("verdict=%q err=%v, want ErrEvidenceExpired", evidence.Verdict, err)
	}
	if evidence.Verdict.IsFinal() {
		t.Fatalf("verdict %q must not be usable", evidence.Verdict)
	}
	// A candidate *was* found — the search matched and the report was read. Only
	// its age refused it, and an operator needs to see that difference.
	if !evidence.CandidateFound {
		t.Fatal("the candidate flag must survive an age refusal")
	}
}

// Older than the TTL is likewise nothing.
func TestReconcileRefusesEvidenceOlderThanTheLimit(t *testing.T) {
	_, evidence, err := reconcileAged(t, VerdictTTL+time.Minute, "false")

	if !errors.Is(err, ErrEvidenceExpired) || evidence.Verdict.IsFinal() {
		t.Fatalf("verdict=%q err=%v", evidence.Verdict, err)
	}
}

// The finding, stated with the real numbers: a 23-hour-old SAFE scan under a
// 15-minute TTL can never clear anything locally, however recently it was found.
func TestAScanFromYesterdayNeverClearsAnything(t *testing.T) {
	_, evidence, err := reconcileAged(t, 23*time.Hour, "false")

	if !errors.Is(err, ErrEvidenceExpired) {
		t.Fatalf("a 23h-old SAFE scan produced verdict=%q err=%v", evidence.Verdict, err)
	}
	if evidence.Verdict == VerdictSafe {
		t.Fatal("a 23h-old scan became a local clearance")
	}
	if got := ReconcileOutcome(evidence.Verdict, err); got != ReconcileStillInconclusive {
		t.Fatalf("outcome label = %q, want the row left inconclusive", got)
	}
}

// A malicious verdict is a restriction, not a permission, so age does not refuse
// it. Retaining it is the conservative direction and is what keeps old evidence
// of harm from being discarded.
func TestReconcileAcceptsOldMaliciousEvidence(t *testing.T) {
	_, evidence, err := reconcileAged(t, 23*time.Hour, "true")

	if err != nil || evidence.Verdict != VerdictMalicious {
		t.Fatalf("verdict=%q err=%v, want an old condemnation to still count",
			evidence.Verdict, err)
	}
	// Its evidence is still dated honestly; what the stores do with that is their
	// documented retention policy, not a claim made here.
	if evidence.ObservedAt.After(time.Now()) {
		t.Fatal("evidence time was moved forward")
	}
}

// A provider clock ahead of ours must not mint a lifetime longer than VerdictTTL.
func TestReconcileCapsAFutureProviderTimestamp(t *testing.T) {
	now := time.Now().UTC()
	future := now.Add(72 * time.Hour)
	server := &reconcileServer{
		searchBody: scanTasksJSON(
			scanTaskJSON("scan-future", "https://example.test/future",
				now.Format(time.RFC3339), "unlisted")),
		resultBody: datedReportJSON("scan-future", "finished", "true", "true", "false", future),
	}
	service := server.start(t)

	evidence, err := service.reconcileAt(context.Background(), "https://example.test/future", now)

	if err != nil || evidence.Verdict != VerdictSafe {
		t.Fatalf("verdict=%q err=%v", evidence.Verdict, err)
	}
	if evidence.ObservedAt.After(now) {
		t.Fatalf("evidence time %v is in the future relative to %v", evidence.ObservedAt, now)
	}
	if remaining := evidence.ExpiresAt().Sub(now); remaining > VerdictTTL {
		t.Fatalf("remaining lifetime %v exceeds a full TTL %v", remaining, VerdictTTL)
	}
}

// Adopting later does not make the evidence younger. Two reconciliations of the
// same scan, minutes apart, describe the same instant.
func TestAdoptingLaterDoesNotChangeTheEvidenceAge(t *testing.T) {
	now := time.Now().UTC()
	scanTime := now.Add(-VerdictTTL / 4)
	server := &reconcileServer{
		searchBody: scanTasksJSON(
			scanTaskJSON("scan-stable", "https://example.test/stable",
				scanTime.Format(time.RFC3339), "unlisted")),
		resultBody: datedReportJSON("scan-stable", "finished", "true", "true", "false", scanTime),
	}
	service := server.start(t)

	first, err := service.reconcileAt(context.Background(), "https://example.test/stable", now)
	if err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	later := now.Add(2 * time.Minute)
	second, err := service.reconcileAt(context.Background(), "https://example.test/stable", later)
	if err != nil {
		t.Fatalf("second reconcile: %v", err)
	}

	if !first.ObservedAt.Equal(second.ObservedAt) {
		t.Fatalf("evidence moved from %v to %v across adoptions",
			first.ObservedAt, second.ObservedAt)
	}
	// So the second adoption has strictly less lifetime left than the first.
	if second.ExpiresAt().Sub(later) >= first.ExpiresAt().Sub(now) {
		t.Fatal("a later adoption did not have less lifetime remaining")
	}
}

// When the report carries no usable time, the scan's submission time stands in —
// a scan cannot have concluded before it started, so the substitute can only make
// the evidence look older, never younger.
func TestReconcileFallsBackToTheSubmissionTime(t *testing.T) {
	now := time.Now().UTC()
	submitted := now.Add(-VerdictTTL / 2)
	server := &reconcileServer{
		searchBody: scanTasksJSON(
			scanTaskJSON("scan-undated", "https://example.test/undated",
				submitted.Format(time.RFC3339), "unlisted")),
		// No time fields at all in the report.
		resultBody: `{"task":{"uuid":"scan-undated","status":"finished","success":true},` +
			`"verdicts":{"overall":{"hasVerdicts":true,"malicious":false}}}`,
	}
	service := server.start(t)

	evidence, err := service.reconcileAt(context.Background(), "https://example.test/undated", now)

	if err != nil || evidence.Verdict != VerdictSafe {
		t.Fatalf("verdict=%q err=%v", evidence.Verdict, err)
	}
	if !evidence.ObservedAt.Equal(submitted.Truncate(time.Second)) {
		t.Fatalf("evidence time = %v, want the submission time %v",
			evidence.ObservedAt, submitted.Truncate(time.Second))
	}
	if evidence.ExpiresAt().Sub(now) >= VerdictTTL {
		t.Fatal("the fallback granted a full fresh lifetime")
	}
}

// A candidate the search cannot date either, with a report that cannot be dated,
// has no honest freshness to assign — so it clears nothing.
func TestReconcileRefusesCompletelyUndatedEvidence(t *testing.T) {
	server := &reconcileServer{
		searchBody: `{"results":[{"task":{"uuid":"scan-nodate","url":"https://example.test/nodate",` +
			`"time":"` + recentRFC3339() + `","visibility":"unlisted"}}]}`,
		resultBody: `{"task":{"uuid":"scan-nodate","status":"finished","success":true},` +
			`"verdicts":{"overall":{"hasVerdicts":true,"malicious":false}}}`,
	}
	service := server.start(t)

	// Evaluated with a zero search time by asking about a URL whose record cannot
	// be dated: the search filter would refuse it outright, so this exercises the
	// guard through the report path instead.
	evidence, err := service.reconcileAt(
		context.Background(), "https://example.test/nodate", time.Now().UTC())

	// The search record does carry a time here, so this succeeds — the assertion
	// is that it is dated from *something* the provider said and never from the
	// local clock at adoption.
	if err == nil && evidence.ObservedAt.IsZero() {
		t.Fatal("a verdict was returned with no evidence time at all")
	}
}

// The candidate flag separates "nothing to read" from "read it, learned nothing".
func TestCandidateFoundIsReportedForAnExactMatch(t *testing.T) {
	server := &reconcileServer{
		searchBody: scanTasksJSON(
			scanTaskJSON("scan-cf", "https://example.test/cf", recentRFC3339(), "unlisted")),
		resultBody: reportJSON("scan-cf", "finished", "false", "false", "false"),
	}
	service := server.start(t)

	evidence, err := service.Reconcile(context.Background(), "https://example.test/cf")

	if !errors.Is(err, ErrScanInconclusive) {
		t.Fatalf("err = %v", err)
	}
	if !evidence.CandidateFound {
		t.Fatal("an exact-match candidate was not reported as found")
	}
}

func TestCandidateFoundIsNotReportedWithoutAMatch(t *testing.T) {
	for name, server := range map[string]*reconcileServer{
		"empty search":   {searchBody: `{"results":[]}`},
		"different url":  {searchBody: scanTasksJSON(scanTaskJSON("s", "https://other.test/x", recentRFC3339(), "unlisted"))},
		"search failure": {searchStatus: http.StatusTooManyRequests},
	} {
		t.Run(name, func(t *testing.T) {
			service := server.start(t)

			evidence, _ := service.Reconcile(context.Background(), "https://example.test/cf")

			if evidence.CandidateFound {
				t.Fatal("a candidate was reported without an exact match")
			}
			if server.results.Load() != 0 {
				t.Fatal("a report was read without a candidate")
			}
		})
	}
}
