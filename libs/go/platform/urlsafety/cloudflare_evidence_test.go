package urlsafety

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Exact evidence reuse (issue #928 §10).
//
// The rule these hold, from both sides: a scan the provider already has may
// answer for *this* URL and for no other, and the answer still comes from a full
// report through the same strict checks a first-hand poll uses. A search summary
// is never a clearance, and neither is a near-miss URL.

const reusableURL = "https://docs.example.test/guia?page=2"

// evidenceServer routes the two endpoints reuse touches. search returns the
// search body; results maps a scan id to its report body.
type evidenceServer struct {
	search   string
	results  map[string]string
	searches int
	reads    int
	submits  int
}

func (e *evidenceServer) scanner(t *testing.T) *CloudflareScanner {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost:
			e.submits++
			_, _ = w.Write([]byte(`{"uuid":"fresh-scan"}`))
		case strings.Contains(r.URL.Path, "/search"):
			e.searches++
			_, _ = w.Write([]byte(e.search))
		default:
			e.reads++
			id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			body, ok := e.results[id]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte(body))
		}
	}))
	t.Cleanup(server.Close)
	scanner, err := newCloudflareScanner(server.URL, "acct", "token", server.Client())
	if err != nil {
		t.Fatalf("new cloudflare scanner: %v", err)
	}
	return scanner
}

// searchBody builds a one-result search answer.
func searchBody(uuid, url, when, visibility string) string {
	return `{"results":[{"task":{"uuid":"` + uuid + `","url":"` + url +
		`","time":"` + when + `","visibility":"` + visibility + `"}}]}`
}

// reportBody builds a finished report with an explicit verdict.
func reportBody(uuid, url string, hasVerdicts, malicious bool, timeEnd string) string {
	return `{"task":{"uuid":"` + uuid + `","url":"` + url +
		`","success":true,"status":"finished","timeEnd":"` + timeEnd + `"},` +
		`"verdicts":{"overall":{"hasVerdicts":` + boolText(hasVerdicts) +
		`,"malicious":` + boolText(malicious) + `}}}`
}

func boolText(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// A scan of exactly this URL, unlisted, recent, whose full report carries an
// explicit clearance. This is the case the whole path exists for: the answer is
// already there and no scan has to be bought.
func TestReusableEvidenceAdoptsAnExactRecentScan(t *testing.T) {
	now := time.Now()
	scanned := now.Add(-3 * time.Minute)
	server := &evidenceServer{
		search: searchBody("scan-a", reusableURL, rfc3339(scanned), "unlisted"),
		results: map[string]string{
			"scan-a": reportBody("scan-a", reusableURL, true, false, rfc3339(scanned)),
		},
	}

	evidence, err := server.scanner(t).
		findReusableEvidenceAt(context.Background(), reusableURL, VerdictTTL, now)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if evidence.Verdict != VerdictSafe {
		t.Fatalf("verdict = %q, want %q", evidence.Verdict, VerdictSafe)
	}
	if evidence.UUID != "scan-a" {
		t.Fatalf("uuid = %q", evidence.UUID)
	}
	if !evidence.ObservedAt.Equal(scanned.UTC().Truncate(time.Second)) {
		t.Fatalf("ObservedAt = %s, want the provider's own time %s",
			evidence.ObservedAt, scanned)
	}
	if server.submits != 0 {
		t.Fatal("reusing evidence must not submit a scan")
	}
}

func TestReusableEvidenceAdoptsAnExactCondemnation(t *testing.T) {
	now := time.Now()
	scanned := now.Add(-time.Minute)
	server := &evidenceServer{
		search: searchBody("scan-m", reusableURL, rfc3339(scanned), "unlisted"),
		results: map[string]string{
			"scan-m": reportBody("scan-m", reusableURL, true, true, rfc3339(scanned)),
		},
	}

	evidence, err := server.scanner(t).
		findReusableEvidenceAt(context.Background(), reusableURL, VerdictTTL, now)

	if err != nil || evidence.Verdict != VerdictMalicious {
		t.Fatalf("verdict = %q, err = %v", evidence.Verdict, err)
	}
}

// Identity, from every direction a near miss can come from. None of these is
// the URL being asked about, and none of them may answer for it.
func TestReusableEvidenceRefusesAnythingButTheExactURL(t *testing.T) {
	now := time.Now()
	scanned := rfc3339(now.Add(-time.Minute))
	for name, other := range map[string]string{
		"same host, different path":  "https://docs.example.test/outro?page=2",
		"same path, different query": "https://docs.example.test/guia?page=3",
		"no query at all":            "https://docs.example.test/guia",
		"different port":             "https://docs.example.test:8443/guia?page=2",
		"different host":             "https://docs.example.com/guia?page=2",
		"different subdomain":        "https://cdn.docs.example.test/guia?page=2",
		"different scheme":           "http://docs.example.test/guia?page=2",
		"hostname only":              "https://docs.example.test/",
	} {
		t.Run(name, func(t *testing.T) {
			server := &evidenceServer{
				search: searchBody("scan-x", other, scanned, "unlisted"),
				results: map[string]string{
					"scan-x": reportBody("scan-x", other, true, false, scanned),
				},
			}

			_, err := server.scanner(t).
				findReusableEvidenceAt(context.Background(), reusableURL, VerdictTTL, now)

			if !errors.Is(err, ErrNoReusableEvidence) {
				t.Fatalf("err = %v, want ErrNoReusableEvidence", err)
			}
		})
	}
}

// The report has to prove its own subject. A search that matched, whose report
// then describes a different URL, is exactly the misrouted or substituted
// response this second check exists for.
func TestReusableEvidenceRefusesAReportAboutAnotherURL(t *testing.T) {
	now := time.Now()
	scanned := rfc3339(now.Add(-time.Minute))
	server := &evidenceServer{
		search: searchBody("scan-a", reusableURL, scanned, "unlisted"),
		results: map[string]string{
			"scan-a": reportBody("scan-a", "https://evil.test/", true, false, scanned),
		},
	}

	_, err := server.scanner(t).
		findReusableEvidenceAt(context.Background(), reusableURL, VerdictTTL, now)

	if !errors.Is(err, ErrNoReusableEvidence) {
		t.Fatalf("err = %v, want ErrNoReusableEvidence", err)
	}
}

// Everything else that disqualifies a candidate or its report. The verdict rules
// are shared with polling, so "finished without verdicts" and "malicious=false
// alone" fail here for exactly the reason they fail there.
func TestReusableEvidenceRefusesUnusableCandidates(t *testing.T) {
	now := time.Now()
	recent := rfc3339(now.Add(-time.Minute))
	stale := rfc3339(now.Add(-2 * time.Hour))

	for name, testCase := range map[string]struct {
		search string
		report string
	}{
		"nothing found": {`{"results":[]}`, ""},
		"stale scan": {
			searchBody("scan-a", reusableURL, stale, "unlisted"),
			reportBody("scan-a", reusableURL, true, false, stale),
		},
		// Never submitted by this client, so it is not ours to read.
		"public visibility": {
			searchBody("scan-a", reusableURL, recent, "public"),
			reportBody("scan-a", reusableURL, true, false, recent),
		},
		"no uuid": {
			searchBody("", reusableURL, recent, "unlisted"), "",
		},
		// The production case, and the rule the whole issue rests on: a finished
		// scan with no verdicts is not a clearance, here or anywhere.
		"report has no verdicts": {
			searchBody("scan-a", reusableURL, recent, "unlisted"),
			reportBody("scan-a", reusableURL, false, false, recent),
		},
		"report identity mismatch": {
			searchBody("scan-a", reusableURL, recent, "unlisted"),
			reportBody("other-scan", reusableURL, true, false, recent),
		},
		"report unsuccessful": {
			searchBody("scan-a", reusableURL, recent, "unlisted"),
			`{"task":{"uuid":"scan-a","url":"` + reusableURL +
				`","success":false,"status":"finished","timeEnd":"` + recent + `"},` +
				`"verdicts":{"overall":{"hasVerdicts":false,"malicious":false}}}`,
		},
		// A report dated after the freshness window, whatever the search said.
		"report dated stale": {
			searchBody("scan-a", reusableURL, recent, "unlisted"),
			reportBody("scan-a", reusableURL, true, false, stale),
		},
	} {
		t.Run(name, func(t *testing.T) {
			server := &evidenceServer{search: testCase.search, results: map[string]string{}}
			if testCase.report != "" {
				server.results["scan-a"] = testCase.report
				server.results["other-scan"] = testCase.report
			}

			_, err := server.scanner(t).
				findReusableEvidenceAt(context.Background(), reusableURL, VerdictTTL, now)

			if !errors.Is(err, ErrNoReusableEvidence) {
				t.Fatalf("err = %v, want ErrNoReusableEvidence", err)
			}
		})
	}
}

// A search result carries a summarised verdict field. It is never read: the
// candidate yields an id, and the id is read through the full report. A summary
// claiming malicious=false over a report with no verdicts clears nothing.
func TestReusableEvidenceIgnoresTheSearchSummaryVerdict(t *testing.T) {
	now := time.Now()
	recent := rfc3339(now.Add(-time.Minute))
	server := &evidenceServer{
		search: `{"results":[{"task":{"uuid":"scan-a","url":"` + reusableURL +
			`","time":"` + recent + `","visibility":"unlisted"},` +
			`"verdicts":{"overall":{"malicious":false,"hasVerdicts":true}}}]}`,
		results: map[string]string{
			"scan-a": reportBody("scan-a", reusableURL, false, false, recent),
		},
	}

	_, err := server.scanner(t).
		findReusableEvidenceAt(context.Background(), reusableURL, VerdictTTL, now)

	if !errors.Is(err, ErrNoReusableEvidence) {
		t.Fatalf("a search summary cleared a URL its report did not: %v", err)
	}
}

// A candidate still running is not evidence yet — and it is not a failure
// either, so the caller proceeds exactly as it would have.
func TestReusableEvidenceTreatsARunningScanAsNothingToReuse(t *testing.T) {
	now := time.Now()
	server := &evidenceServer{
		search:  searchBody("scan-a", reusableURL, rfc3339(now.Add(-time.Minute)), "unlisted"),
		results: map[string]string{},
	}

	_, err := server.scanner(t).
		findReusableEvidenceAt(context.Background(), reusableURL, VerdictTTL, now)

	if !errors.Is(err, ErrNoReusableEvidence) {
		t.Fatalf("err = %v, want ErrNoReusableEvidence", err)
	}
}

// A search that could not be answered is reported as itself. It is not
// "nothing to reuse": the caller has to be able to tell the two apart.
func TestReusableEvidenceReportsASearchFailureAsAFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(server.Close)
	scanner, err := newCloudflareScanner(server.URL, "acct", "token", server.Client())
	if err != nil {
		t.Fatalf("new cloudflare scanner: %v", err)
	}

	_, err = scanner.FindReusableEvidence(context.Background(), reusableURL, VerdictTTL)

	if errors.Is(err, ErrNoReusableEvidence) {
		t.Fatal("a throttled search must not read as an absence of evidence")
	}
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}

// --- search-first, through the provider contract ----------------------------

// The order the issue requires: ask before buying. A reusable clearance answers
// the Check outright, and no scan is submitted.
func TestCloudflareCheckReusesEvidenceInsteadOfSubmitting(t *testing.T) {
	now := time.Now()
	recent := rfc3339(now.Add(-time.Minute))
	server := &evidenceServer{
		search: searchBody("scan-a", reusableURL, recent, "unlisted"),
		results: map[string]string{
			"scan-a": reportBody("scan-a", reusableURL, true, false, recent),
		},
	}

	result, err := server.scanner(t).Check(context.Background(), reusableURL, "")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Verdict != ReputationSafe {
		t.Fatalf("verdict = %q, want %q", result.Verdict, ReputationSafe)
	}
	if server.submits != 0 {
		t.Fatalf("submitted %d scan(s) despite reusable evidence", server.submits)
	}
	if result.ProviderRef != "scan-a" {
		t.Fatalf("ref = %q, want the reused scan id", result.ProviderRef)
	}
}

// The hostname refusal, end to end. Nothing reusable exists, the submission is
// declined for the budget, and the exchange fails once — no second search, no
// resubmission. The pipeline retries on its own schedule and the target
// converges to UNKNOWN at its deadline.
func TestCloudflareHostnameLimitWithNoEvidenceFailsOnceWithoutAStorm(t *testing.T) {
	var searches, submits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			submits++
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"errors":[{"message":"` + hostnameRefusalText + `"}]}`))
			return
		}
		searches++
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	t.Cleanup(server.Close)
	scanner, err := newCloudflareScanner(server.URL, "acct", "token", server.Client())
	if err != nil {
		t.Fatalf("new cloudflare scanner: %v", err)
	}

	result, err := scanner.Check(context.Background(), reusableURL, "")

	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if FailureReason(err) != ReasonHostnameLimit {
		t.Fatalf("reason = %q, want %q", FailureReason(err), ReasonHostnameLimit)
	}
	if result.Verdict == ReputationSafe {
		t.Fatal("a refused submission must never produce a clearance")
	}
	if searches != 1 || submits != 1 {
		t.Fatalf("searches = %d, submits = %d; want exactly one of each", searches, submits)
	}
}

// A search failure does not stop the submission. No submission is outstanding
// yet, so the uncertainty the failed search leaves is the uncertainty the caller
// already had — the opposite of reconciliation, where a throttled search
// mistaken for absence buys a duplicate scan.
func TestCloudflareCheckSubmitsWhenTheSearchFails(t *testing.T) {
	var submits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			submits++
			_, _ = w.Write([]byte(`{"uuid":"fresh-scan"}`))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	scanner, err := newCloudflareScanner(server.URL, "acct", "token", server.Client())
	if err != nil {
		t.Fatalf("new cloudflare scanner: %v", err)
	}

	result, err := scanner.Check(context.Background(), reusableURL, "")

	if !errors.Is(err, ErrCheckInProgress) {
		t.Fatalf("err = %v, want ErrCheckInProgress", err)
	}
	if result.ProviderRef != "fresh-scan" || submits != 1 {
		t.Fatalf("ref = %q, submits = %d", result.ProviderRef, submits)
	}
}

// A window of zero or less asks for evidence no evidence could satisfy. Refused
// before a request is made rather than after one is wasted.
func TestReusableEvidenceRefusesANonPositiveWindow(t *testing.T) {
	server := &evidenceServer{search: `{"results":[]}`, results: map[string]string{}}
	scanner := server.scanner(t)

	for _, window := range []time.Duration{0, -time.Minute} {
		_, err := scanner.findReusableEvidenceAt(
			context.Background(), reusableURL, window, time.Now())
		if !errors.Is(err, ErrNoReusableEvidence) {
			t.Fatalf("window %s: err = %v, want ErrNoReusableEvidence", window, err)
		}
	}
	if server.searches != 0 {
		t.Fatal("a non-positive window still cost a search")
	}
}

// A provider clock ahead of ours would otherwise mint evidence younger than
// now, and therefore a lifetime longer than this deployment grants. Capped.
func TestReusableEvidenceCapsAFutureProviderTimestamp(t *testing.T) {
	now := time.Now()
	ahead := rfc3339(now.Add(10 * time.Minute))
	server := &evidenceServer{
		search: searchBody("scan-a", reusableURL, ahead, "unlisted"),
		results: map[string]string{
			"scan-a": reportBody("scan-a", reusableURL, true, false, ahead),
		},
	}

	evidence, err := server.scanner(t).
		findReusableEvidenceAt(context.Background(), reusableURL, VerdictTTL, now)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if evidence.ObservedAt.After(now) {
		t.Fatalf("ObservedAt = %s, want it capped at %s", evidence.ObservedAt, now)
	}
}

// A report that dates itself not at all falls back to the search's submission
// time, which can only make the evidence look older than it is.
func TestReusableEvidenceFallsBackToTheSubmissionTime(t *testing.T) {
	now := time.Now()
	submitted := now.Add(-2 * time.Minute)
	server := &evidenceServer{
		search: searchBody("scan-a", reusableURL, rfc3339(submitted), "unlisted"),
		results: map[string]string{
			"scan-a": `{"task":{"uuid":"scan-a","url":"` + reusableURL +
				`","success":true,"status":"finished"},` +
				`"verdicts":{"overall":{"hasVerdicts":true,"malicious":false}}}`,
		},
	}

	evidence, err := server.scanner(t).
		findReusableEvidenceAt(context.Background(), reusableURL, VerdictTTL, now)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !evidence.ObservedAt.Equal(submitted.UTC().Truncate(time.Second)) {
		t.Fatalf("ObservedAt = %s, want the submission time %s", evidence.ObservedAt, submitted)
	}
}
