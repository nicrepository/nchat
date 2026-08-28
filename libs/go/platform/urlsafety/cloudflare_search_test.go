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

// The search used to recover a submission whose outcome was never written down.
//
// Two questions run through everything here. Does the request say what we meant
// to ask — the URL is a value a user chose, and it ends up inside the provider's
// filter syntax, so it must not be able to become syntax. And is the answer
// really about *our* scan of *that* URL — because adopting the wrong scan id
// means reading a verdict for a different page and clearing this one with it.

const searchTestAccount = "acct-1"

// searchServer stands in for the provider and records what it was asked.
type searchServer struct {
	query  string
	size   string
	auth   string
	path   string
	status int
	body   string
}

func (s *searchServer) start(t *testing.T) *CloudflareScanner {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.path, s.auth = r.URL.Path, r.Header.Get("Authorization")
		s.query, s.size = r.URL.Query().Get("q"), r.URL.Query().Get("size")
		status := s.status
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(s.body))
	}))
	t.Cleanup(server.Close)
	scanner, err := newCloudflareScanner(server.URL, searchTestAccount, "token-1", server.Client())
	if err != nil {
		t.Fatalf("new scanner: %v", err)
	}
	return scanner
}

// scanTasksJSON builds the envelope the provider actually answers with: a
// top-level `results`, each entry wrapping its task under `task`.
func scanTasksJSON(tasks ...string) string {
	return `{"results":[` + strings.Join(tasks, ",") + `]}`
}

func scanTaskJSON(uuid, url, when, visibility string) string {
	return `{"task":{"uuid":"` + uuid + `","url":"` + url + `","time":"` + when +
		`","visibility":"` + visibility + `","success":true}}`
}

func TestFindRecentScanUsesTheDocumentedRequest(t *testing.T) {
	when := time.Now().UTC().Format(time.RFC3339)
	server := &searchServer{body: scanTasksJSON(
		scanTaskJSON("scan-a", "https://example.com/a", when, "unlisted"),
	)}
	scanner := server.start(t)

	record, matches, err := scanner.FindRecentScan(
		context.Background(), "https://example.com/a", time.Now().Add(-time.Minute))

	if err != nil {
		t.Fatalf("FindRecentScan: %v", err)
	}
	if record.UUID != "scan-a" || matches != 1 {
		t.Fatalf("record=%+v matches=%d", record, matches)
	}
	if server.path != "/accounts/"+searchTestAccount+"/urlscanner/v2/search" {
		t.Fatalf("path = %q", server.path)
	}
	if server.auth != "Bearer token-1" {
		t.Fatalf("auth header = %q", server.auth)
	}
	if server.size != "10" {
		t.Fatalf("size = %q", server.size)
	}
	if server.query != `task.url:"https://example.com/a"` {
		t.Fatalf("query = %q", server.query)
	}
}

// The production regression. The search answers `{"results":[{...,"task":{...}}]}`,
// and a client modelling it as `{"tasks":[...]}` decodes that without error into
// an empty slice: every reconciliation then reports "no such scan", the caller
// stays uncertain with no scan_uuid, and the search repeats forever.
//
// So the payload here is the observed one, siblings and all — `_id`, `page`,
// `result`, `stats`, `verdicts` around the `task` — because the fields this
// client ignores are exactly the ones a fabricated fixture leaves out.
func TestFindRecentScanReadsTheProviderResultEnvelope(t *testing.T) {
	const wanted = "https://example.com/a"
	when := time.Now().UTC().Format(time.RFC3339)
	body := `{"results":[{` +
		`"_id":"019bc0de-0000-7000-8000-000000000001",` +
		`"page":{"url":"` + wanted + `","country":"US","asn":"AS13335"},` +
		`"result":"https://api.example/urlscanner/v2/result/scan-live",` +
		`"stats":{"dataLength":1234,"requests":7,"uniqIPs":3},` +
		`"task":{"success":true,"time":"` + when + `","url":"` + wanted + `",` +
		`"uuid":"scan-live","visibility":"unlisted"},` +
		`"verdicts":{"overall":{"malicious":false,"hasVerdicts":true}}` +
		`}]}`
	scanner := (&searchServer{body: body}).start(t)

	record, matches, err := scanner.FindRecentScan(
		context.Background(), wanted, time.Now().Add(-time.Minute))

	if err != nil {
		t.Fatalf("the real provider envelope was not understood: %v", err)
	}
	if record.UUID != "scan-live" || matches != 1 {
		t.Fatalf("record=%+v matches=%d", record, matches)
	}
	// The siblings are read past, not read from: the search never becomes a
	// verdict, whatever `verdicts` says.
	if record.URL != wanted || record.Visibility != "unlisted" {
		t.Fatalf("record = %+v", record)
	}
}

// The injection case, stated as the property that matters: whatever the URL
// contains, the query the provider receives must still be one term about one
// URL. A quote in the value may not close the term, and nothing in the value may
// become a second clause.
func TestSearchQueryCannotBeReshapedByTheURL(t *testing.T) {
	for name, raw := range map[string]string{
		"double quote":     `https://example.com/a?q="x"&next=1`,
		"backslash":        `https://example.com/a\b?q=c`,
		"quote and clause": `https://example.com/a?q=" OR task.url:"https://evil.test/`,
		"percent encoded":  `https://example.com/a?next=https%3A%2F%2Fx.test%2F`,
		"unicode":          `https://exämple.com/ünïcode?q=ç`,
		"colon in query":   `https://example.com/a?q=task.url:evil`,
	} {
		t.Run(name, func(t *testing.T) {
			canonical, err := CanonicalizeURL(raw)
			if err != nil {
				t.Skipf("not a canonicalizable URL: %v", err)
			}
			query, err := scanSearchQuery(canonical)
			if err != nil {
				t.Fatalf("scanSearchQuery: %v", err)
			}

			// One field, and the value is one quoted term: everything after the
			// opening quote up to the final quote is the value, and every quote
			// inside it is escaped.
			if !strings.HasPrefix(query, `task.url:"`) || !strings.HasSuffix(query, `"`) {
				t.Fatalf("query is not a single quoted term: %q", query)
			}
			term := query[len(`task.url:`):]
			if unescapedQuoteBefore(term, len(term)-1) {
				t.Fatalf("the value closes its own term: %q", query)
			}
			// And the round trip is lossless — escaping that mangles the URL would
			// silently search for something else.
			if got := unescapeSearchTerm(term); got != canonical {
				t.Fatalf("round trip = %q, want %q", got, canonical)
			}
		})
	}
}

// A control character cannot appear in a canonical URL, so its presence means
// the value did not come from where it is supposed to. Refused rather than
// encoded: a newline is the classic way to append a clause.
func TestSearchTermRefusesControlCharacters(t *testing.T) {
	for name, value := range map[string]string{
		"newline":         "https://example.com/a\nb",
		"carriage return": "https://example.com/a\rb",
		"null":            "https://example.com/a\x00b",
		"delete":          "https://example.com/a\x7fb",
		"empty":           "",
		"too long":        "https://example.com/" + strings.Repeat("a", maxURLLength),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := escapeSearchTerm(value); !errors.Is(err, ErrNotCheckable) {
				t.Fatalf("err = %v, want ErrNotCheckable", err)
			}
		})
	}
}

// Identity, one filter at a time. Each of these is a scan that exists and is not
// ours, and adopting any of them would mean reading a verdict about something
// else.
func TestFindRecentScanRefusesAScanThatIsNotOurs(t *testing.T) {
	const wanted = "https://example.com/a"
	now := time.Now().UTC()
	since := now.Add(-time.Minute)

	for name, task := range map[string]string{
		"a different url": scanTaskJSON(
			"scan-x", "https://example.com/b", now.Format(time.RFC3339), "unlisted"),
		"a different path on the same host": scanTaskJSON(
			"scan-x", "https://example.com/a/sub", now.Format(time.RFC3339), "unlisted"),
		"a different query": scanTaskJSON(
			"scan-x", "https://example.com/a?q=1", now.Format(time.RFC3339), "unlisted"),
		"a public scan": scanTaskJSON(
			"scan-x", wanted, now.Format(time.RFC3339), "public"),
		"older than the attempt": scanTaskJSON(
			"scan-x", wanted, now.Add(-time.Hour).Format(time.RFC3339), "unlisted"),
		"no uuid": scanTaskJSON(
			"", wanted, now.Format(time.RFC3339), "unlisted"),
		"an unparseable time": scanTaskJSON(
			"scan-x", wanted, "not-a-time", "unlisted"),
		"an unparseable url": scanTaskJSON(
			"scan-x", "javascript:alert(1)", now.Format(time.RFC3339), "unlisted"),
	} {
		t.Run(name, func(t *testing.T) {
			scanner := (&searchServer{body: scanTasksJSON(task)}).start(t)
			_, _, err := scanner.FindRecentScan(context.Background(), wanted, since)
			if !errors.Is(err, ErrNotCheckable) {
				t.Fatalf("err = %v, want ErrNotCheckable (nothing adoptable)", err)
			}
		})
	}
}

// The provider does not promise one scan per URL. Several eligible ones means
// picking deterministically — the newest — and telling the caller it was
// ambiguous so the ambiguity can be counted.
func TestFindRecentScanPicksTheNewestOfSeveralEligible(t *testing.T) {
	const wanted = "https://example.com/a"
	now := time.Now().UTC()
	server := &searchServer{body: scanTasksJSON(
		scanTaskJSON("scan-old", wanted, now.Add(-30*time.Second).Format(time.RFC3339), "unlisted"),
		scanTaskJSON("scan-new", wanted, now.Format(time.RFC3339), "unlisted"),
		// Not ours, and must not be counted as ambiguity either.
		scanTaskJSON("scan-other", "https://example.com/b", now.Format(time.RFC3339), "unlisted"),
	)}
	scanner := server.start(t)

	record, matches, err := scanner.FindRecentScan(
		context.Background(), wanted, now.Add(-time.Minute))

	if err != nil {
		t.Fatalf("FindRecentScan: %v", err)
	}
	if record.UUID != "scan-new" {
		t.Fatalf("adopted %q, want the newest", record.UUID)
	}
	if matches != 2 {
		t.Fatalf("matches = %d, want the two eligible ones", matches)
	}
}

// A scan slightly older than the recorded attempt is still ours: the two clocks
// are not the same clock. The tolerance only ever widens the window, and the URL
// equality check is what actually establishes identity.
func TestFindRecentScanToleratesClockSkew(t *testing.T) {
	const wanted = "https://example.com/a"
	now := time.Now().UTC()
	scanner := (&searchServer{body: scanTasksJSON(
		scanTaskJSON("scan-a", wanted, now.Add(-30*time.Second).Format(time.RFC3339), "unlisted"),
	)}).start(t)

	record, _, err := scanner.FindRecentScan(context.Background(), wanted, now)

	if err != nil {
		t.Fatalf("a scan inside the skew tolerance was refused: %v", err)
	}
	if record.UUID != "scan-a" {
		t.Fatalf("record = %+v", record)
	}
}

// A provider that cannot answer is not a provider saying "no such scan". The
// distinction is the whole point: absence lets the caller give up and resubmit
// eventually, a failure must not.
func TestFindRecentScanReportsFailureRatherThanAbsence(t *testing.T) {
	for name, server := range map[string]*searchServer{
		"429":            {status: http.StatusTooManyRequests, body: `{"results":[]}`},
		"500":            {status: http.StatusInternalServerError, body: `{}`},
		"403":            {status: http.StatusForbidden, body: `{}`},
		"404":            {status: http.StatusNotFound, body: `{}`},
		"malformed json": {body: `{"results":`},
		"trailing data":  {body: `{"results":[]}{"results":[]}`},
	} {
		t.Run(name, func(t *testing.T) {
			scanner := server.start(t)
			_, _, err := scanner.FindRecentScan(
				context.Background(), "https://example.com/a", time.Now().Add(-time.Minute))
			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("err = %v, want ErrUnavailable", err)
			}
		})
	}
}

// An empty result set is absence, not failure — and it must be distinguishable,
// because the two lead to different decisions upstream.
func TestFindRecentScanReportsAbsenceForAnEmptyResult(t *testing.T) {
	scanner := (&searchServer{body: `{"results":[]}`}).start(t)

	_, _, err := scanner.FindRecentScan(
		context.Background(), "https://example.com/a", time.Now().Add(-time.Minute))

	if !errors.Is(err, ErrNotCheckable) {
		t.Fatalf("err = %v, want ErrNotCheckable", err)
	}
}

// The caller going away is reported as itself rather than as a provider fact.
func TestFindRecentScanReportsCancellation(t *testing.T) {
	scanner := (&searchServer{body: `{"results":[]}`}).start(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := scanner.FindRecentScan(ctx, "https://example.com/a", time.Now())

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// unescapedQuoteBefore reports whether an unescaped quote appears before index.
func unescapedQuoteBefore(term string, index int) bool {
	for i := 1; i < index; i++ {
		if term[i] != '"' {
			continue
		}
		backslashes := 0
		for j := i - 1; j > 0 && term[j] == '\\'; j-- {
			backslashes++
		}
		if backslashes%2 == 0 {
			return true
		}
	}
	return false
}

// unescapeSearchTerm reverses escapeSearchTerm, so the round trip can be
// asserted: escaping that changes the value would search for the wrong URL.
func unescapeSearchTerm(term string) string {
	inner := strings.TrimSuffix(strings.TrimPrefix(term, `"`), `"`)
	var out strings.Builder
	for i := 0; i < len(inner); i++ {
		if inner[i] == '\\' && i+1 < len(inner) {
			i++
		}
		out.WriteByte(inner[i])
	}
	return out.String()
}
