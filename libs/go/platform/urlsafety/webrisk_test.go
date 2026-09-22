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

// The Google Web Risk adapter (issue #928).
//
// Every test here asserts one half of the same sentence: an answer is a
// clearance only when the whole exchange succeeded, and everything else is a
// failure that is never a clearance. The fixtures reproduce the API contract —
// status codes and body shapes — and never a recorded production response, so
// nothing here depends on the network or on what a particular URL happened to
// return on a particular day.

const testWebRiskKey = "test-webrisk-key-must-not-leak"

// webRiskServer stands in for the provider. The handler receives the request so
// a test can assert what was actually asked.
func webRiskServer(t *testing.T, handler http.HandlerFunc) (*WebRiskProvider, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	provider, err := newWebRiskProvider(server.URL, testWebRiskKey, server.Client())
	if err != nil {
		t.Fatalf("new web risk provider: %v", err)
	}
	return provider, server
}

// respondJSON is the 200-with-a-body fixture every happy path uses.
func respondJSON(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}
}

func respondStatus(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

func TestWebRiskRequiresAPIKey(t *testing.T) {
	if _, err := NewWebRiskProvider("   "); err == nil {
		t.Fatal("a provider with no key must be refused at construction")
	}
}

// An empty document is the provider saying no configured list names this URI.
// It is the only shape that clears a URL, and it clears it only because the
// status was 200 and the body parsed.
func TestWebRiskEmptyResponseIsSafe(t *testing.T) {
	provider, _ := webRiskServer(t, respondJSON(`{}`))
	result, err := provider.Check(context.Background(), "https://example.test/a", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Verdict != ReputationSafe {
		t.Fatalf("verdict = %q, want %q", result.Verdict, ReputationSafe)
	}
	if result.Provider != ProviderGoogleWebRisk {
		t.Fatalf("provider = %q, want %q", result.Provider, ProviderGoogleWebRisk)
	}
	if result.ProviderRef != "" {
		t.Fatalf("a synchronous provider must issue no ref, got %q", result.ProviderRef)
	}
}

// The question asked has to be the whole question: one request, both threat
// types, the URL under test and no credential in the URL.
func TestWebRiskAsksBothThreatTypesInOneRequest(t *testing.T) {
	var got *http.Request
	provider, _ := webRiskServer(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(context.Background())
		_, _ = w.Write([]byte(`{}`))
	})
	if _, err := provider.Check(context.Background(), "https://example.test/a?b=c", ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	query := got.URL.Query()
	if want := []string{"MALWARE", "SOCIAL_ENGINEERING"}; !equalStrings(query["threatTypes"], want) {
		t.Fatalf("threatTypes = %v, want %v", query["threatTypes"], want)
	}
	if query.Get("uri") != "https://example.test/a?b=c" {
		t.Fatalf("uri = %q, want the canonical URL unchanged", query.Get("uri"))
	}
	if got.Header.Get("X-Goog-Api-Key") != testWebRiskKey {
		t.Fatal("the key must travel in X-Goog-Api-Key")
	}
	if strings.Contains(got.URL.String(), testWebRiskKey) {
		t.Fatal("the key must never appear in the request URL")
	}
}

func TestWebRiskThreatVerdicts(t *testing.T) {
	for name, body := range map[string]string{
		"malware":            `{"threat":{"threatTypes":["MALWARE"]}}`,
		"social engineering": `{"threat":{"threatTypes":["SOCIAL_ENGINEERING"]}}`,
		"both":               `{"threat":{"threatTypes":["MALWARE","SOCIAL_ENGINEERING"]}}`,
		// expireTime is part of the documented shape. It is read and it does not
		// weaken the condemnation: the verdict is malicious either way, and the
		// cache lifetime is this package's VerdictTTL, which is far inside any
		// expiry Google issues.
		"with expire time": `{"threat":{"threatTypes":["MALWARE"],` +
			`"expireTime":"2099-01-01T00:00:00Z"}}`,
		// An unknown type alongside a configured one still condemns: the
		// configured one matched.
		"unknown type alongside": `{"threat":{"threatTypes":["UNWANTED_SOFTWARE","MALWARE"]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			provider, _ := webRiskServer(t, respondJSON(body))
			result, err := provider.Check(context.Background(), "https://bad.test/x", "")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.Verdict != ReputationMalicious {
				t.Fatalf("verdict = %q, want %q", result.Verdict, ReputationMalicious)
			}
			if len(result.ThreatCategories) == 0 {
				t.Fatal("a condemnation must name the list that produced it")
			}
		})
	}
}

// Every failure shape, and the closed category each one reports. The verdict is
// the same in all of them — there isn't one — and the category exists only so
// an operator can tell an exhausted quota from a rejected key.
func TestWebRiskFailuresAreNeverClearances(t *testing.T) {
	for name, testCase := range map[string]struct {
		handler http.HandlerFunc
		reason  string
	}{
		"400 bad request": {respondStatus(http.StatusBadRequest,
			`{"error":{"code":400,"status":"INVALID_ARGUMENT"}}`), ReasonUnavailable},
		"401 unauthenticated": {respondStatus(http.StatusUnauthorized, `{}`), ReasonAuthError},
		"403 permission denied": {respondStatus(http.StatusForbidden,
			`{"error":{"code":403,"status":"PERMISSION_DENIED"}}`), ReasonAuthError},
		"429 quota exhausted": {respondStatus(http.StatusTooManyRequests, `{}`), ReasonRateLimited},
		"500 internal":        {respondStatus(http.StatusInternalServerError, ``), ReasonUnavailable},
		"503 unavailable":     {respondStatus(http.StatusServiceUnavailable, ``), ReasonUnavailable},
		// A 2xx this contract does not define a body for is not an answer
		// either: understanding half a response is how a proxy becomes a verdict.
		"204 no content":  {respondStatus(http.StatusNoContent, ``), ReasonUnavailable},
		"malformed json":  {respondJSON(`{"threat":`), ReasonMalformed},
		"trailing data":   {respondJSON(`{}{"threat":{"threatTypes":["MALWARE"]}}`), ReasonMalformed},
		"not json at all": {respondJSON(`<html>proxy error</html>`), ReasonMalformed},
		// A threat object naming no type this deployment asked about is neither
		// the "no match" shape nor the "match" shape. Refused rather than read
		// in either direction.
		"threat with no types":    {respondJSON(`{"threat":{"threatTypes":[]}}`), ReasonMalformed},
		"threat with only others": {respondJSON(`{"threat":{"threatTypes":["NOPE"]}}`), ReasonMalformed},
	} {
		t.Run(name, func(t *testing.T) {
			provider, _ := webRiskServer(t, testCase.handler)
			result, err := provider.Check(context.Background(), "https://example.test/a", "")
			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("err = %v, want ErrUnavailable", err)
			}
			if result.Verdict == ReputationSafe {
				t.Fatal("a failed exchange must never report a clearance")
			}
			if reason := FailureReason(err); reason != testCase.reason {
				t.Fatalf("reason = %q, want %q", reason, testCase.reason)
			}
		})
	}
}

// A provider that never answers is a failure, not a clearance, and it is
// bounded by the caller's deadline rather than by hope.
func TestWebRiskTimeoutIsUnavailable(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	provider, _ := webRiskServer(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	provider.client.Timeout = 30 * time.Millisecond

	result, err := provider.Check(context.Background(), "https://slow.test/a", "")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if FailureReason(err) != ReasonTimeout {
		t.Fatalf("reason = %q, want %q", FailureReason(err), ReasonTimeout)
	}
	if result.Verdict == ReputationSafe {
		t.Fatal("a timeout must never report a clearance")
	}
}

// A caller going away is reported as itself. It is a fact about this process,
// not about the URL, and conflating the two is how a cancelled request would be
// counted against the provider.
func TestWebRiskContextCancellationIsNotAVerdict(t *testing.T) {
	blocked := make(chan struct{})
	t.Cleanup(func() { close(blocked) })
	provider, _ := webRiskServer(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-blocked:
		case <-r.Context().Done():
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	result, err := provider.Check(ctx, "https://example.test/a", "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if result.Verdict == ReputationSafe {
		t.Fatal("a cancelled check must never report a clearance")
	}
}

// Nothing this adapter returns may carry the key. The error text is what a log
// line and a trace both end up holding, so it is asserted directly.
func TestWebRiskNeverLeaksTheKey(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"auth error": respondStatus(http.StatusForbidden, `{"error":{"message":"denied"}}`),
		"malformed":  respondJSON(`{`),
		"server":     respondStatus(http.StatusInternalServerError, ``),
	} {
		t.Run(name, func(t *testing.T) {
			provider, _ := webRiskServer(t, handler)
			_, err := provider.Check(context.Background(), "https://example.test/a", "")
			if err == nil {
				t.Fatal("expected a failure")
			}
			if strings.Contains(err.Error(), testWebRiskKey) {
				t.Fatalf("error text carries the api key: %q", err.Error())
			}
			// The URL under test is a user's, and it is not this error's to
			// repeat either — the adapter reports a category, not an incident.
			if strings.Contains(err.Error(), "example.test") {
				t.Fatalf("error text carries the checked URL: %q", err.Error())
			}
		})
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
