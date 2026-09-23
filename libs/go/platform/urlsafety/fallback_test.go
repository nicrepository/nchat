package urlsafety

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// The primary/fallback composition (issue #928).
//
// These are the precedence rules stated as behaviour. The two stubs record every
// call, so each test asserts not only what came back but which provider was
// asked — "Cloudflare was never consulted" is half of what several of these
// rules actually say.

// stubProvider is a scripted reputation source. Each entry of the script answers
// one Check, so a test can make the same provider behave differently across the
// two calls a resumed check involves.
type stubProvider struct {
	name    string
	script  []stubAnswer
	calls   []string
	nextIdx int
}

type stubAnswer struct {
	result ReputationResult
	err    error
}

func newStub(name string, answers ...stubAnswer) *stubProvider {
	return &stubProvider{name: name, script: answers}
}

func (s *stubProvider) Name() string { return s.name }

func (s *stubProvider) Check(
	ctx context.Context, canonicalURL, providerRef string,
) (ReputationResult, error) {
	s.calls = append(s.calls, providerRef)
	if s.nextIdx >= len(s.script) {
		return ReputationResult{}, ErrUnavailable
	}
	answer := s.script[s.nextIdx]
	s.nextIdx++
	answer.result.Provider = s.name
	return answer.result, answer.err
}

func verdictAnswer(verdict ReputationVerdict) stubAnswer {
	return stubAnswer{result: ReputationResult{Verdict: verdict}}
}

func errorAnswer(err error) stubAnswer { return stubAnswer{err: err} }

func composed(primary, secondary *stubProvider) *PrimaryFallbackProvider {
	return NewPrimaryFallbackProvider(primary, secondary, nil)
}

// A clearance from the primary is the answer. The pipeline does not hold a link
// while a scanner double-checks a list lookup, and the secondary is not asked at
// all — which is also what makes a Cloudflare hostname refusal structurally
// unable to downgrade it.
func TestFallbackPrimarySafeDoesNotConsultSecondary(t *testing.T) {
	primary := newStub(ProviderGoogleWebRisk, verdictAnswer(ReputationSafe))
	secondary := newStub(ProviderCloudflareURLScanner)

	result, err := composed(primary, secondary).Check(context.Background(), "https://ok.test/", "")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Verdict != ReputationSafe {
		t.Fatalf("verdict = %q, want %q", result.Verdict, ReputationSafe)
	}
	if result.Provider != ProviderGoogleWebRisk {
		t.Fatalf("provider = %q, want the primary", result.Provider)
	}
	if len(secondary.calls) != 0 {
		t.Fatalf("secondary was consulted %d time(s) behind a fresh clearance", len(secondary.calls))
	}
}

// A condemnation is already the strictest answer available. Asking a second
// source could only ever produce a weaker one, so it is not asked.
func TestFallbackPrimaryMaliciousDoesNotConsultSecondary(t *testing.T) {
	primary := newStub(ProviderGoogleWebRisk, verdictAnswer(ReputationMalicious))
	secondary := newStub(ProviderCloudflareURLScanner, verdictAnswer(ReputationSafe))

	result, err := composed(primary, secondary).Check(context.Background(), "https://bad.test/", "")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Verdict != ReputationMalicious {
		t.Fatalf("verdict = %q, want %q", result.Verdict, ReputationMalicious)
	}
	if len(secondary.calls) != 0 {
		t.Fatal("a condemnation must never be re-litigated by the fallback")
	}
}

func TestFallbackUsesSecondaryWhenPrimaryIsUnavailable(t *testing.T) {
	for name, testCase := range map[string]struct {
		secondary stubAnswer
		want      ReputationVerdict
		wantErr   error
	}{
		"secondary clears":       {verdictAnswer(ReputationSafe), ReputationSafe, nil},
		"secondary condemns":     {verdictAnswer(ReputationMalicious), ReputationMalicious, nil},
		"secondary inconclusive": {verdictAnswer(ReputationUnknown), ReputationUnknown, nil},
	} {
		t.Run(name, func(t *testing.T) {
			primary := newStub(ProviderGoogleWebRisk, errorAnswer(unavailable(ReasonRateLimited)))
			secondary := newStub(ProviderCloudflareURLScanner, testCase.secondary)

			result, err := composed(primary, secondary).
				Check(context.Background(), "https://x.test/", "")

			if !errors.Is(err, testCase.wantErr) {
				t.Fatalf("err = %v, want %v", err, testCase.wantErr)
			}
			if result.Verdict != testCase.want {
				t.Fatalf("verdict = %q, want %q", result.Verdict, testCase.want)
			}
			if len(secondary.calls) != 1 {
				t.Fatalf("secondary called %d time(s), want exactly 1", len(secondary.calls))
			}
			if secondary.calls[0] != "" {
				t.Fatalf("the fallback must start a fresh check, got ref %q", secondary.calls[0])
			}
		})
	}
}

// Both sources failing is a failed exchange, never a clearance, and the reason
// reported is the primary's: it is the source that is supposed to answer.
func TestFallbackBothUnavailableFailsClosed(t *testing.T) {
	primary := newStub(ProviderGoogleWebRisk, errorAnswer(unavailable(ReasonAuthError)))
	secondary := newStub(ProviderCloudflareURLScanner, errorAnswer(unavailable(ReasonTimeout)))

	result, err := composed(primary, secondary).Check(context.Background(), "https://x.test/", "")

	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if result.Verdict == ReputationSafe {
		t.Fatal("a total outage must never report a clearance")
	}
	if FailureReason(err) != ReasonAuthError {
		t.Fatalf("reason = %q, want the primary's %q", FailureReason(err), ReasonAuthError)
	}
}

// The rule the production incident is about, stated directly: Cloudflare
// answering "finished, no verdicts" — with or without the hostname refusal that
// motivated this issue — cannot become a clearance, and cannot reach the
// pipeline as anything but the terminal non-answer it is.
func TestFallbackSecondaryNoClassificationStaysUnknown(t *testing.T) {
	for name, secondaryAnswer := range map[string]stubAnswer{
		"no classification": verdictAnswer(ReputationUnknown),
		"hostname limit": {result: ReputationResult{
			Verdict: ReputationUnknown, Reason: ReasonHostnameLimit,
		}},
	} {
		t.Run(name, func(t *testing.T) {
			primary := newStub(ProviderGoogleWebRisk, errorAnswer(ErrUnavailable))
			secondary := newStub(ProviderCloudflareURLScanner, secondaryAnswer)

			result, err := composed(primary, secondary).
				Check(context.Background(), "https://www.youtube.test/@x", "")

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.Verdict != ReputationUnknown {
				t.Fatalf("verdict = %q, want %q", result.Verdict, ReputationUnknown)
			}
		})
	}
}

// The primary answering a terminal non-answer is not an answer the pipeline can
// act on either, so the secondary gets its turn.
func TestFallbackPrimaryUnknownConsultsSecondary(t *testing.T) {
	primary := newStub(ProviderGoogleWebRisk, verdictAnswer(ReputationUnknown))
	secondary := newStub(ProviderCloudflareURLScanner, verdictAnswer(ReputationSafe))

	result, err := composed(primary, secondary).Check(context.Background(), "https://x.test/", "")

	if err != nil || result.Verdict != ReputationSafe {
		t.Fatalf("verdict = %q, err = %v; want safe from the secondary", result.Verdict, err)
	}
}

// A non-empty ref is a scan outstanding at the secondary — only an asynchronous
// provider issues one, and only the secondary is asynchronous. It is routed
// there untouched, and the primary is not asked: doing so would open a second
// line of evidence for a row already committed to one.
func TestFallbackResumesAtSecondary(t *testing.T) {
	primary := newStub(ProviderGoogleWebRisk, verdictAnswer(ReputationSafe))
	secondary := newStub(ProviderCloudflareURLScanner, verdictAnswer(ReputationMalicious))

	result, err := composed(primary, secondary).
		Check(context.Background(), "https://x.test/", "3a7f-scan-uuid")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Verdict != ReputationMalicious {
		t.Fatalf("verdict = %q, want the secondary's", result.Verdict)
	}
	if len(primary.calls) != 0 {
		t.Fatal("a resumed check must not also start one at the primary")
	}
	if secondary.calls[0] != "3a7f-scan-uuid" {
		t.Fatalf("ref = %q, want it forwarded untouched", secondary.calls[0])
	}
}

// DirectProviderRef is what a pipeline persists for a synchronous answer, so it
// names no outstanding scan anywhere. Resuming it means asking the primary
// again — handing a word that is not a scan id to the scanner would spend an
// exchange on a guaranteed refusal.
func TestFallbackDirectRefGoesBackToThePrimary(t *testing.T) {
	primary := newStub(ProviderGoogleWebRisk, verdictAnswer(ReputationSafe))
	secondary := newStub(ProviderCloudflareURLScanner)

	result, err := composed(primary, secondary).
		Check(context.Background(), "https://x.test/", DirectProviderRef)

	if err != nil || result.Verdict != ReputationSafe {
		t.Fatalf("verdict = %q, err = %v; want safe from the primary", result.Verdict, err)
	}
	if len(secondary.calls) != 0 {
		t.Fatal("a direct ref names no scan at the secondary")
	}
}

// An asynchronous primary — none exists today, and the contract permits one —
// keeps its own check. The secondary must not be consulted behind a check the
// pipeline has been told to come back to.
func TestFallbackPrimaryInProgressIsDecisive(t *testing.T) {
	primary := newStub(ProviderGoogleWebRisk, stubAnswer{
		result: ReputationResult{ProviderRef: "ref-1"}, err: ErrCheckInProgress,
	})
	secondary := newStub(ProviderCloudflareURLScanner, verdictAnswer(ReputationSafe))

	result, err := composed(primary, secondary).Check(context.Background(), "https://x.test/", "")

	if !errors.Is(err, ErrCheckInProgress) {
		t.Fatalf("err = %v, want ErrCheckInProgress", err)
	}
	if result.ProviderRef != "ref-1" {
		t.Fatalf("ref = %q, want it preserved for the pipeline to persist", result.ProviderRef)
	}
	if len(secondary.calls) != 0 {
		t.Fatal("secondary consulted behind an in-progress primary check")
	}
}

// One failed primary exchange means exactly one fallback exchange. No loop, no
// second attempt at either side inside one Check.
func TestFallbackAsksEachProviderAtMostOnce(t *testing.T) {
	primary := newStub(ProviderGoogleWebRisk, errorAnswer(unavailable(ReasonTimeout)))
	secondary := newStub(ProviderCloudflareURLScanner, errorAnswer(unavailable(ReasonTimeout)))

	if _, err := composed(primary, secondary).
		Check(context.Background(), "https://x.test/", ""); err == nil {
		t.Fatal("expected a failure")
	}
	if len(primary.calls) != 1 || len(secondary.calls) != 1 {
		t.Fatalf("calls: primary %d, secondary %d; want 1 and 1",
			len(primary.calls), len(secondary.calls))
	}
}

// A caller that goes away between the two providers buys no second exchange.
func TestFallbackCancelledContextDoesNotReachTheSecondary(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	primary := newStub(ProviderGoogleWebRisk)
	primary.script = []stubAnswer{{err: context.Canceled}}
	secondary := newStub(ProviderCloudflareURLScanner, verdictAnswer(ReputationSafe))
	cancel()

	result, err := composed(primary, secondary).Check(ctx, "https://x.test/", "")

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if result.Verdict == ReputationSafe {
		t.Fatal("a cancelled check must never report a clearance")
	}
	if len(secondary.calls) != 0 {
		t.Fatal("a cancelled caller must not spend a fallback exchange")
	}
}

// A primary outage must stop being paid for. Without a breaker of its own the
// primary would be invisible to the pipeline's circuit — which stays closed,
// correctly, because the fallback is covering — and every URL would spend a
// doomed request on it forever.
func TestFallbackBreakerStopsAskingAFailingPrimary(t *testing.T) {
	answers := make([]stubAnswer, 0, defaultBreakerThreshold)
	for i := 0; i < defaultBreakerThreshold; i++ {
		answers = append(answers, errorAnswer(unavailable(ReasonRateLimited)))
	}
	primary := newStub(ProviderGoogleWebRisk, answers...)
	secondary := newStub(ProviderCloudflareURLScanner)
	for i := 0; i < defaultBreakerThreshold+3; i++ {
		secondary.script = append(secondary.script, verdictAnswer(ReputationUnknown))
	}
	provider := composed(primary, secondary)

	for i := 0; i < defaultBreakerThreshold+3; i++ {
		if _, err := provider.Check(context.Background(), "https://x.test/", ""); err != nil {
			t.Fatalf("attempt %d: unexpected error: %v", i, err)
		}
	}

	if len(primary.calls) != defaultBreakerThreshold {
		t.Fatalf("primary called %d time(s), want it to stop at the threshold of %d",
			len(primary.calls), defaultBreakerThreshold)
	}
	// The fallback keeps working throughout: an open circuit in front of the
	// primary is not an outage of the pipeline.
	if len(secondary.calls) != defaultBreakerThreshold+3 {
		t.Fatalf("secondary called %d time(s), want every attempt", len(secondary.calls))
	}
}

// Name is diagnostic and must name both halves, so a log line says which
// composition answered.
func TestFallbackNameNamesBothProviders(t *testing.T) {
	provider := composed(
		newStub(ProviderGoogleWebRisk), newStub(ProviderCloudflareURLScanner))
	want := ProviderGoogleWebRisk + "+" + ProviderCloudflareURLScanner
	if provider.Name() != want {
		t.Fatalf("Name() = %q, want %q", provider.Name(), want)
	}
}

// FailureReason is what every metric label and structured log field on this
// path is derived from, so its closed-set guarantee is asserted directly: an
// error nobody labelled reports the honest "it failed and nothing said how",
// and an open circuit reports itself rather than being mistaken for a provider
// failure an operator should go and investigate.
func TestFailureReasonIsAlwaysAClosedValue(t *testing.T) {
	for name, testCase := range map[string]struct {
		err  error
		want string
	}{
		"labelled":         {unavailable(ReasonMalformed), ReasonMalformed},
		"circuit open":     {ErrCircuitOpen, ReasonCircuitOpen},
		"bare unavailable": {ErrUnavailable, ReasonUnavailable},
		"foreign error":    {errors.New("something else entirely"), ReasonUnavailable},
		"wrapped label": {
			fmt.Errorf("poll link scan: %w", unavailable(ReasonRateLimited)), ReasonRateLimited,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got := FailureReason(testCase.err); got != testCase.want {
				t.Fatalf("FailureReason = %q, want %q", got, testCase.want)
			}
		})
	}
}

// --- the background second opinion (issue #928) ------------------------------

// CheckSecondary asks the fallback and only the fallback. Routing it through
// Check would ask the primary again about a URL the primary already cleared,
// which is the same opinion at twice the price.
func TestCheckSecondaryAsksOnlyTheFallback(t *testing.T) {
	primary := newStub(ProviderGoogleWebRisk, verdictAnswer(ReputationSafe))
	secondary := newStub(ProviderCloudflareURLScanner, verdictAnswer(ReputationMalicious))

	result, err := composed(primary, secondary).
		CheckSecondary(context.Background(), "https://x.test/", "cf-1")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Verdict != ReputationMalicious {
		t.Fatalf("verdict = %q, want the fallback's", result.Verdict)
	}
	if len(primary.calls) != 0 {
		t.Fatal("a second opinion must not re-ask the primary")
	}
	if secondary.calls[0] != "cf-1" {
		t.Fatalf("ref = %q, want it forwarded untouched", secondary.calls[0])
	}
}

// The composition's precedence does not apply to a second opinion: there is no
// primary answer to prefer here, and a failure is simply an opinion that was not
// obtained.
func TestCheckSecondaryReportsFailuresWithoutConsultingThePrimary(t *testing.T) {
	primary := newStub(ProviderGoogleWebRisk, verdictAnswer(ReputationSafe))
	secondary := newStub(ProviderCloudflareURLScanner, errorAnswer(unavailable(ReasonHostnameLimit)))

	_, err := composed(primary, secondary).
		CheckSecondary(context.Background(), "https://x.test/", "")

	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if FailureReason(err) != ReasonHostnameLimit {
		t.Fatalf("reason = %q, want %q", FailureReason(err), ReasonHostnameLimit)
	}
	if len(primary.calls) != 0 {
		t.Fatal("a failed second opinion fell back to the primary")
	}
}

// secondOpinionService builds a Service whose provider is a composition, and
// seeds the cache with the clearance the primary produced — which is the state
// every second opinion actually runs against.
func secondOpinionService(t *testing.T, secondary *stubProvider) *Service {
	t.Helper()
	primary := newStub(ProviderGoogleWebRisk, verdictAnswer(ReputationSafe))
	service := newService(nil, nil, time.Now)
	service.provider = NewPrimaryFallbackProvider(primary, secondary, nil)
	if _, err := service.Check(context.Background(), secondOpinionURL, ""); err != nil {
		t.Fatalf("seeding the clearance: %v", err)
	}
	if verdict, ok := service.Lookup(secondOpinionURL); !ok || verdict != VerdictSafe {
		t.Fatalf("seed: verdict = %q, ok = %v", verdict, ok)
	}
	return service
}

const secondOpinionURL = "https://cleared.test/page"

// The one thing a second opinion may change: a condemnation displaces the
// clearance sitting in the cache. Without this, Lookup would keep answering
// "safe" for the rest of VerdictTTL about a URL just decided malicious.
func TestServiceCheckSecondaryCachesACondemnation(t *testing.T) {
	secondary := newStub(ProviderCloudflareURLScanner, verdictAnswer(ReputationMalicious))
	service := secondOpinionService(t, secondary)

	if _, err := service.CheckSecondary(context.Background(), secondOpinionURL, ""); err != nil {
		t.Fatalf("CheckSecondary: %v", err)
	}

	verdict, ok := service.Lookup(secondOpinionURL)
	if !ok || verdict != VerdictMalicious {
		t.Fatalf("verdict = %q, ok = %v; want the condemnation to have displaced the clearance",
			verdict, ok)
	}
}

// Everything else a second opinion can produce leaves the clearance exactly
// where it was. A failure in particular must not cache VerdictUnknown the way a
// failed Check does — that would erase a live clearance because a background
// double-check timed out.
func TestServiceCheckSecondaryLeavesAFreshClearanceAlone(t *testing.T) {
	for name, answer := range map[string]stubAnswer{
		"agrees":            verdictAnswer(ReputationSafe),
		"no classification": verdictAnswer(ReputationUnknown),
		"hostname limit": {result: ReputationResult{
			Verdict: ReputationUnknown, Reason: ReasonHostnameLimit,
		}},
		"unavailable":  errorAnswer(unavailable(ReasonTimeout)),
		"auth error":   errorAnswer(unavailable(ReasonAuthError)),
		"rate limited": errorAnswer(unavailable(ReasonRateLimited)),
	} {
		t.Run(name, func(t *testing.T) {
			secondary := newStub(ProviderCloudflareURLScanner, answer)
			service := secondOpinionService(t, secondary)

			_, _ = service.CheckSecondary(context.Background(), secondOpinionURL, "")

			verdict, ok := service.Lookup(secondOpinionURL)
			if !ok || verdict != VerdictSafe {
				t.Fatalf("verdict = %q, ok = %v; want the clearance untouched", verdict, ok)
			}
		})
	}
}

// A provider with no second source is a working deployment: there is simply no
// background verification to run, and the caller is told so rather than being
// handed a failure it would retry.
func TestServiceCheckSecondaryWithoutASecondSource(t *testing.T) {
	service := NewReputationService(newStub("solo", verdictAnswer(ReputationSafe)), nil)

	_, err := service.CheckSecondary(context.Background(), "https://x.test/", "")

	if !errors.Is(err, ErrSecondaryUnsupported) {
		t.Fatalf("err = %v, want ErrSecondaryUnsupported", err)
	}
}

// The pipeline's breaker still guards this lane: a provider that is down stops
// being asked, and an open circuit is reported as a failure rather than as an
// opinion.
func TestServiceCheckSecondaryRespectsTheCircuit(t *testing.T) {
	secondary := newStub(ProviderCloudflareURLScanner)
	service := secondOpinionService(t, secondary)
	service.SetBreaker(NewBreaker(1, time.Hour))
	secondary.script = []stubAnswer{errorAnswer(ErrUnavailable), verdictAnswer(ReputationMalicious)}

	if _, err := service.CheckSecondary(context.Background(), secondOpinionURL, ""); err == nil {
		t.Fatal("expected the first exchange to fail")
	}
	_, err := service.CheckSecondary(context.Background(), secondOpinionURL, "")

	if !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("err = %v, want ErrCircuitOpen", err)
	}
	if verdict, ok := service.Lookup(secondOpinionURL); !ok || verdict != VerdictSafe {
		t.Fatalf("an open circuit disturbed the clearance: %q ok=%v", verdict, ok)
	}
}
