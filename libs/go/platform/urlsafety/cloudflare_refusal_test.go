package urlsafety

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Cloudflare's refusals, told apart (issue #928 §9).
//
// The behaviour under test is deliberately *unchanged*: every one of these is
// still fail-closed, still terminal where it was terminal, and still never a
// clearance. What is new is only that an operator can see which refusal it was.
// The tests assert both halves — the verdict is what it always was, and the
// category is the closed constant — because the risk in adding a category is
// precisely that it starts deciding something.

// The production refusal that motivated putting a list lookup in front of this
// scanner. NChat asked for a scan of a perfectly ordinary URL and Cloudflare
// declined to run one, because somebody had scanned the hostname recently.
const hostnameRefusalText = "Refusing to scan: hostname was recently scanned or " +
	"too many scans to hostname in the last days."

func refusalScanner(t *testing.T, handler http.HandlerFunc) *CloudflareScanner {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	scanner, err := newCloudflareScanner(server.URL, "acct", "token", server.Client())
	if err != nil {
		t.Fatalf("new cloudflare scanner: %v", err)
	}
	return scanner
}

// A submission the provider declines because of the hostname budget. It is an
// ordinary failed exchange to the pipeline — the row stays outstanding and is
// reconciled, never resubmitted — and it is its own category to an operator.
func TestCloudflareSubmitHostnameLimitIsCategorised(t *testing.T) {
	for name, body := range map[string]string{
		"error envelope": `{"errors":[{"code":1001,"message":"` + hostnameRefusalText + `"}]}`,
		"bare message":   `{"message":"` + hostnameRefusalText + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			scanner := refusalScanner(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(body))
			})

			scanID, err := scanner.SubmitScan(context.Background(), "https://www.youtube.test/@x")

			if scanID != "" {
				t.Fatalf("a refused submission must yield no scan id, got %q", scanID)
			}
			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("err = %v, want ErrUnavailable", err)
			}
			if FailureReason(err) != ReasonHostnameLimit {
				t.Fatalf("reason = %q, want %q", FailureReason(err), ReasonHostnameLimit)
			}
			if strings.Contains(err.Error(), "Refusing to scan") {
				t.Fatalf("the provider's prose escaped the adapter: %q", err.Error())
			}
		})
	}
}

// Credentials and quota are categorised from the status alone, without reading
// a body that may not be JSON at all.
func TestCloudflareSubmitStatusCategories(t *testing.T) {
	for name, testCase := range map[string]struct {
		status int
		reason string
	}{
		"unauthorized":     {http.StatusUnauthorized, ReasonAuthError},
		"forbidden":        {http.StatusForbidden, ReasonAuthError},
		"rate limited":     {http.StatusTooManyRequests, ReasonRateLimited},
		"server error":     {http.StatusInternalServerError, ReasonUnavailable},
		"unparseable body": {http.StatusBadGateway, ReasonUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			scanner := refusalScanner(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(testCase.status)
				_, _ = w.Write([]byte(`<html>gateway</html>`))
			})
			_, err := scanner.SubmitScan(context.Background(), "https://x.test/")
			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("err = %v, want ErrUnavailable", err)
			}
			if FailureReason(err) != testCase.reason {
				t.Fatalf("reason = %q, want %q", FailureReason(err), testCase.reason)
			}
		})
	}
}

// resultBody builds the finished-report fixture the verdict rules see: a scan
// the provider confirms is the one requested, confirms is over, and which
// produced no verdict. taskExtra and topExtra are spliced into the two objects
// a refusal has been observed in.
func resultBody(scanID, taskExtra, topExtra string) string {
	return `{"task":{"uuid":"` + scanID + `","success":false,"status":"finished"` +
		taskExtra + `},"verdicts":{"overall":{"hasVerdicts":false,"malicious":false}}` +
		topExtra + `}`
}

// The report half, through the provider contract. All three of these are the
// same terminal non-answer, and the reason is the only thing that differs.
func TestCloudflareCheckCategorisesInconclusiveReports(t *testing.T) {
	const scanID = "11111111-2222-3333-4444-555555555555"
	for name, testCase := range map[string]struct {
		taskExtra string
		topExtra  string
		reason    string
	}{
		"hostname limit in task errors": {
			`,"errors":[{"message":"` + hostnameRefusalText + `"}]`, ``, ReasonHostnameLimit,
		},
		"hostname limit in top-level errors": {
			``, `,"errors":[{"code":1001,"message":"` + hostnameRefusalText + `"}]`,
			ReasonHostnameLimit,
		},
		"hostname limit in top-level message": {
			``, `,"message":"` + hostnameRefusalText + `"`, ReasonHostnameLimit,
		},
		// The other production case: the scan genuinely ran and produced
		// nothing. No refusal, so no category — and the same verdict.
		"no classification": {``, ``, ""},
	} {
		t.Run(name, func(t *testing.T) {
			scanner := refusalScanner(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(resultBody(scanID, testCase.taskExtra, testCase.topExtra)))
			})

			result, err := scanner.Check(context.Background(), "https://x.test/", scanID)

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.Verdict != ReputationUnknown {
				t.Fatalf("verdict = %q, want %q — the verdict rules are unchanged",
					result.Verdict, ReputationUnknown)
			}
			if result.Reason != testCase.reason {
				t.Fatalf("reason = %q, want %q", result.Reason, testCase.reason)
			}
		})
	}
}

// The two answers that are real verdicts still are, and carry no category:
// there is nothing refused to explain.
func TestCloudflareCheckExplicitVerdictsUnchanged(t *testing.T) {
	const scanID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	for name, testCase := range map[string]struct {
		malicious bool
		want      ReputationVerdict
	}{
		"explicit safe":      {false, ReputationSafe},
		"explicit malicious": {true, ReputationMalicious},
	} {
		t.Run(name, func(t *testing.T) {
			malicious := "false"
			if testCase.malicious {
				malicious = "true"
			}
			scanner := refusalScanner(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(
					`{"task":{"uuid":"` + scanID + `","success":true,"status":"finished"},` +
						`"verdicts":{"overall":{"hasVerdicts":true,"malicious":` + malicious + `}}}`))
			})

			result, err := scanner.Check(context.Background(), "https://x.test/", scanID)

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.Verdict != testCase.want {
				t.Fatalf("verdict = %q, want %q", result.Verdict, testCase.want)
			}
			if result.Reason != "" {
				t.Fatalf("a real verdict must carry no refusal category, got %q", result.Reason)
			}
		})
	}
}

// The rule the whole issue rests on, restated where it can regress: the report
// from the reproduction — malicious=false with no verdicts — is not a
// clearance, whatever else it carries.
func TestCloudflareMaliciousFalseAloneIsNeverSafe(t *testing.T) {
	const scanID = "99999999-8888-7777-6666-555555555555"
	scanner := refusalScanner(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(
			`{"task":{"uuid":"` + scanID + `","success":true,"status":"finished"},` +
				`"verdicts":{"overall":{"hasVerdicts":false,"malicious":false}}}`))
	})

	result, err := scanner.Check(context.Background(), "https://example.test/", scanID)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Verdict != ReputationUnknown {
		t.Fatalf("verdict = %q, want %q", result.Verdict, ReputationUnknown)
	}
}

// classifyRefusal is a string matcher over provider prose, so it is asserted
// directly: it must never answer anything but a constant, and it must not fire
// on ordinary text.
func TestClassifyRefusalReturnsOnlyClosedValues(t *testing.T) {
	if got := classifyRefusal([]string{hostnameRefusalText}); got != ReasonHostnameLimit {
		t.Fatalf("got %q, want %q", got, ReasonHostnameLimit)
	}
	if got := classifyRefusal([]string{"TOO MANY SCANS to hostname"}); got != ReasonHostnameLimit {
		t.Fatalf("case-insensitive match failed: %q", got)
	}
	for _, benign := range []string{
		"", "Scan submitted", "https://example.test/recently-scanned-photos",
	} {
		if got := classifyRefusal([]string{benign}); got != "" {
			t.Fatalf("classifyRefusal(%q) = %q, want no category", benign, got)
		}
	}
}
