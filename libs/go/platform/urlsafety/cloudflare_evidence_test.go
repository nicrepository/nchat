package urlsafety

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// Walking the candidate list (issue #928 Code Quality review).
//
// The newest scan of a URL is not necessarily the usable one: it may still be
// running, may have finished without a verdict, or may be a report this client
// cannot read. Stopping there threw away an answer the provider already had and
// sent the pipeline to a submission it did not need. These fix the order —
// newest first — and the walk.

// multiSearchBody builds a search answer from several candidates, oldest listed
// first so the ordering under test is the code's and not the fixture's.
func multiSearchBody(entries ...string) string {
	return `{"results":[` + strings.Join(entries, ",") + `]}`
}

func candidateEntry(uuid, url, when string) string {
	return `{"task":{"uuid":"` + uuid + `","url":"` + url +
		`","time":"` + when + `","visibility":"unlisted"}}`
}

func TestReusableEvidenceWalksPastUnusableCandidates(t *testing.T) {
	now := time.Now()
	newer := rfc3339(now.Add(-time.Minute))
	older := rfc3339(now.Add(-5 * time.Minute))

	for name, testCase := range map[string]struct {
		newestReport string
		wantVerdict  Verdict
	}{
		// Finished, no verdicts: terminal for that scan and useless, but it
		// says nothing about the scan before it.
		"newest is inconclusive": {
			reportBody("scan-new", reusableURL, false, false, newer), VerdictSafe,
		},
		// A body this client cannot parse is not an answer; the older one is.
		"newest is malformed": {`{"task":`, VerdictSafe},
		// A report about a different URL cannot answer for this one, and must
		// not stop the search for one that can.
		"newest is about another url": {
			reportBody("scan-new", "https://elsewhere.test/", true, false, newer), VerdictSafe,
		},
		// The identity check failing is a candidate problem, not a provider one.
		"newest has a mismatched uuid": {
			reportBody("other-id", reusableURL, true, false, newer), VerdictSafe,
		},
	} {
		t.Run(name, func(t *testing.T) {
			server := &evidenceServer{
				search: multiSearchBody(
					candidateEntry("scan-old", reusableURL, older),
					candidateEntry("scan-new", reusableURL, newer),
				),
				results: map[string]string{
					"scan-new": testCase.newestReport,
					"scan-old": reportBody("scan-old", reusableURL, true, false, older),
				},
			}

			evidence, err := server.scanner(t).
				findReusableEvidenceAt(context.Background(), reusableURL, VerdictTTL, now)

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if evidence.Verdict != testCase.wantVerdict {
				t.Fatalf("verdict = %q, want %q", evidence.Verdict, testCase.wantVerdict)
			}
			if evidence.UUID != "scan-old" {
				t.Fatalf("uuid = %q, want the older usable scan", evidence.UUID)
			}
			if server.submits != 0 {
				t.Fatal("a usable older candidate must not cost a submission")
			}
		})
	}
}

// A scan still running is the case the review named directly: not evidence yet,
// and no reason at all to ignore a finished one behind it.
func TestReusableEvidenceWalksPastAPendingCandidate(t *testing.T) {
	now := time.Now()
	older := rfc3339(now.Add(-5 * time.Minute))
	server := &evidenceServer{
		search: multiSearchBody(
			candidateEntry("scan-old", reusableURL, older),
			candidateEntry("scan-running", reusableURL, rfc3339(now.Add(-time.Minute))),
		),
		// scan-running is absent from results, which is the 404 the provider
		// answers while a scan is in progress.
		results: map[string]string{
			"scan-old": reportBody("scan-old", reusableURL, true, true, older),
		},
	}

	evidence, err := server.scanner(t).
		findReusableEvidenceAt(context.Background(), reusableURL, VerdictTTL, now)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if evidence.Verdict != VerdictMalicious || evidence.UUID != "scan-old" {
		t.Fatalf("evidence = %+v, want the older condemnation", evidence)
	}
}

// Newest first, so a usable newer report wins over a usable older one: a more
// recent scan describes a more recent page.
func TestReusableEvidencePrefersTheNewestUsableCandidate(t *testing.T) {
	now := time.Now()
	newer := rfc3339(now.Add(-time.Minute))
	older := rfc3339(now.Add(-5 * time.Minute))
	server := &evidenceServer{
		search: multiSearchBody(
			candidateEntry("scan-old", reusableURL, older),
			candidateEntry("scan-new", reusableURL, newer),
		),
		results: map[string]string{
			// The newer one condemns, the older one clears. The newer must win.
			"scan-new": reportBody("scan-new", reusableURL, true, true, newer),
			"scan-old": reportBody("scan-old", reusableURL, true, false, older),
		},
	}

	evidence, err := server.scanner(t).
		findReusableEvidenceAt(context.Background(), reusableURL, VerdictTTL, now)

	if err != nil || evidence.Verdict != VerdictMalicious {
		t.Fatalf("evidence = %+v, err = %v; want the newest usable report", evidence, err)
	}
	if server.reads != 1 {
		t.Fatalf("read %d report(s), want to stop at the first usable one", server.reads)
	}
}

// Every candidate unusable is still "nothing to reuse", not a failure: the
// caller submits, exactly as it would have.
func TestReusableEvidenceGivesUpWhenNoCandidateAnswers(t *testing.T) {
	now := time.Now()
	first := rfc3339(now.Add(-time.Minute))
	second := rfc3339(now.Add(-2 * time.Minute))
	server := &evidenceServer{
		search: multiSearchBody(
			candidateEntry("scan-a", reusableURL, second),
			candidateEntry("scan-b", reusableURL, first),
		),
		results: map[string]string{
			"scan-a": reportBody("scan-a", reusableURL, false, false, second),
			"scan-b": reportBody("scan-b", reusableURL, false, false, first),
		},
	}

	_, err := server.scanner(t).
		findReusableEvidenceAt(context.Background(), reusableURL, VerdictTTL, now)

	if !errors.Is(err, ErrNoReusableEvidence) {
		t.Fatalf("err = %v, want ErrNoReusableEvidence", err)
	}
	if server.reads != 2 {
		t.Fatalf("read %d report(s), want both candidates tried", server.reads)
	}
}

// A stale candidate is filtered before any report is read — it never costs a
// request — and a fresh one behind it is still used.
func TestReusableEvidenceSkipsStaleCandidatesWithoutReadingThem(t *testing.T) {
	now := time.Now()
	fresh := rfc3339(now.Add(-2 * time.Minute))
	server := &evidenceServer{
		search: multiSearchBody(
			candidateEntry("scan-fresh", reusableURL, fresh),
			candidateEntry("scan-stale", reusableURL, rfc3339(now.Add(-3*time.Hour))),
		),
		results: map[string]string{
			"scan-fresh": reportBody("scan-fresh", reusableURL, true, false, fresh),
			"scan-stale": reportBody("scan-stale", reusableURL, true, false, fresh),
		},
	}

	evidence, err := server.scanner(t).
		findReusableEvidenceAt(context.Background(), reusableURL, VerdictTTL, now)

	if err != nil || evidence.UUID != "scan-fresh" {
		t.Fatalf("evidence = %+v, err = %v; want the fresh candidate", evidence, err)
	}
	if server.reads != 1 {
		t.Fatalf("read %d report(s), want the stale one filtered before any read", server.reads)
	}
}

// A provider-wide failure aborts the whole operation rather than walking into
// the next candidate: the next request asks the same provider the same
// question and gets the same answer.
func TestReusableEvidenceAbortsOnAProviderWideFailure(t *testing.T) {
	now := time.Now()
	when := rfc3339(now.Add(-time.Minute))
	for name, status := range map[string]int{
		"rate limited": http.StatusTooManyRequests,
		"auth":         http.StatusForbidden,
		"server error": http.StatusInternalServerError,
	} {
		t.Run(name, func(t *testing.T) {
			var reads int
			server := httptest.NewServer(http.HandlerFunc(
				func(w http.ResponseWriter, r *http.Request) {
					if strings.Contains(r.URL.Path, "/search") {
						_, _ = w.Write([]byte(multiSearchBody(
							candidateEntry("scan-a", reusableURL, when),
							candidateEntry("scan-b", reusableURL, when),
						)))
						return
					}
					reads++
					w.WriteHeader(status)
				}))
			t.Cleanup(server.Close)
			scanner, err := newCloudflareScanner(server.URL, "acct", "token", server.Client())
			if err != nil {
				t.Fatalf("new cloudflare scanner: %v", err)
			}

			_, err = scanner.findReusableEvidenceAt(
				context.Background(), reusableURL, VerdictTTL, now)

			if errors.Is(err, ErrNoReusableEvidence) {
				t.Fatal("a provider-wide failure must not read as an absence of evidence")
			}
			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("err = %v, want ErrUnavailable", err)
			}
			if reads != 1 {
				t.Fatalf("read %d report(s), want to stop at the first failure", reads)
			}
		})
	}
}

// A caller going away stops the walk immediately.
func TestReusableEvidenceAbortsOnCancellation(t *testing.T) {
	now := time.Now()
	when := rfc3339(now.Add(-time.Minute))
	ctx, cancel := context.WithCancel(context.Background())
	var reads int
	server := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, "/search") {
				_, _ = w.Write([]byte(multiSearchBody(
					candidateEntry("scan-a", reusableURL, when),
					candidateEntry("scan-b", reusableURL, when),
				)))
				return
			}
			reads++
			cancel()
			<-r.Context().Done()
		}))
	t.Cleanup(server.Close)
	t.Cleanup(cancel)
	scanner, err := newCloudflareScanner(server.URL, "acct", "token", server.Client())
	if err != nil {
		t.Fatalf("new cloudflare scanner: %v", err)
	}

	_, err = scanner.findReusableEvidenceAt(ctx, reusableURL, VerdictTTL, now)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if reads != 1 {
		t.Fatalf("read %d report(s), want the walk to stop at cancellation", reads)
	}
}

// The bound: this is evidence reuse, not a crawler. However many candidates the
// search returns, only a small fixed number of reports is ever read.
func TestReusableEvidenceReadsAtMostTheCandidateBound(t *testing.T) {
	now := time.Now()
	entries := make([]string, 0, searchLookbackLimit)
	results := map[string]string{}
	for i := 0; i < searchLookbackLimit; i++ {
		uuid := fmt.Sprintf("scan-%02d", i)
		when := rfc3339(now.Add(-time.Duration(i+1) * time.Minute))
		entries = append(entries, candidateEntry(uuid, reusableURL, when))
		// Every one of them is unusable, so the walk goes as far as it may.
		results[uuid] = reportBody(uuid, reusableURL, false, false, when)
	}
	server := &evidenceServer{search: multiSearchBody(entries...), results: results}

	_, err := server.scanner(t).
		findReusableEvidenceAt(context.Background(), reusableURL, VerdictTTL, now)

	if !errors.Is(err, ErrNoReusableEvidence) {
		t.Fatalf("err = %v, want ErrNoReusableEvidence", err)
	}
	if server.reads != maxReusableCandidates {
		t.Fatalf("read %d report(s), want the bound of %d", server.reads, maxReusableCandidates)
	}
}

// The order is total and deterministic, so two replicas reading one search
// answer read the same reports in the same order. Equal timestamps are broken
// by uuid rather than by map or slice accident.
func TestReusableCandidateOrderIsDeterministic(t *testing.T) {
	now := time.Now()
	same := rfc3339(now.Add(-time.Minute))
	response := searchResponse{}
	if err := json.Unmarshal([]byte(multiSearchBody(
		candidateEntry("scan-c", reusableURL, same),
		candidateEntry("scan-a", reusableURL, same),
		candidateEntry("scan-b", reusableURL, rfc3339(now.Add(-30*time.Second))),
	)), &response); err != nil {
		t.Fatalf("decode: %v", err)
	}

	for attempt := 0; attempt < 5; attempt++ {
		got := selectReusableCandidates(response, reusableURL, now.Add(-VerdictTTL))
		want := []string{"scan-b", "scan-a", "scan-c"}
		if len(got) != len(want) {
			t.Fatalf("candidates = %d, want %d", len(got), len(want))
		}
		for i := range want {
			if got[i].UUID != want[i] {
				t.Fatalf("order = %v, want %v", got, want)
			}
		}
	}
}

// Identity is unchanged by the walk: a near-miss URL is still never a candidate,
// however many of them the search returns.
func TestReusableEvidenceWalkKeepsExactIdentity(t *testing.T) {
	now := time.Now()
	when := rfc3339(now.Add(-time.Minute))
	server := &evidenceServer{
		search: multiSearchBody(
			candidateEntry("scan-path", "https://docs.example.test/outro?page=2", when),
			candidateEntry("scan-query", "https://docs.example.test/guia?page=3", when),
			candidateEntry("scan-port", "https://docs.example.test:8443/guia?page=2", when),
		),
		results: map[string]string{
			"scan-path":  reportBody("scan-path", reusableURL, true, false, when),
			"scan-query": reportBody("scan-query", reusableURL, true, false, when),
			"scan-port":  reportBody("scan-port", reusableURL, true, false, when),
		},
	}

	_, err := server.scanner(t).
		findReusableEvidenceAt(context.Background(), reusableURL, VerdictTTL, now)

	if !errors.Is(err, ErrNoReusableEvidence) {
		t.Fatalf("err = %v, want ErrNoReusableEvidence", err)
	}
	if server.reads != 0 {
		t.Fatalf("read %d report(s); a near-miss URL must never become a candidate", server.reads)
	}
}
