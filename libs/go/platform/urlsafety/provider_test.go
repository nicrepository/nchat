package urlsafety

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// The provider-agnostic contract (issue #807), asserted against the one adapter
// that exists and against a synthetic synchronous provider, so the pipeline's
// behaviour is proven independent of Cloudflare's two-step shape.

// --- Cloudflare adapter -----------------------------------------------------

// A first Check now searches before it submits (issue #928): a POST creates a
// billed scan and is what the provider's hostname budget refuses, so the cheap
// question comes first. With nothing to reuse, the submission happens exactly as
// it always did.
func TestCloudflareCheckSubmitsThenReportsInProgressWithRef(t *testing.T) {
	var methods []string
	scanner, _ := scannerAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"results":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"uuid":"scan-1"}`))
	})

	result, err := scanner.Check(context.Background(), "https://example.com/", "")

	if len(methods) != 2 || methods[0] != http.MethodGet || methods[1] != http.MethodPost {
		t.Fatalf("exchanges = %v, want a search then a submit", methods)
	}

	if !errors.Is(err, ErrCheckInProgress) {
		t.Fatalf("want ErrCheckInProgress, got %v", err)
	}
	if result.ProviderRef != "scan-1" || result.Provider != ProviderCloudflareURLScanner {
		t.Fatalf("result: %+v", result)
	}
	if result.Verdict != "" {
		t.Fatalf("an in-progress check must carry no verdict, got %q", result.Verdict)
	}
}

func TestCloudflareCheckWithRefReadsTheReport(t *testing.T) {
	cases := map[string]struct {
		status  int
		body    string
		verdict ReputationVerdict
		err     error
	}{
		"running": {http.StatusNotFound, ``, "", ErrCheckInProgress},
		"safe": {http.StatusOK, `{"task":{"uuid":"scan-1","success":true,"status":"finished","timeEnd":"2026-01-02T03:04:05Z"},` +
			`"verdicts":{"overall":{"hasVerdicts":true,"malicious":false}}}`, ReputationSafe, nil},
		"malicious": {http.StatusOK, `{"task":{"uuid":"scan-1","success":true,"status":"finished"},` +
			`"verdicts":{"overall":{"hasVerdicts":true,"malicious":true}}}`, ReputationMalicious, nil},
		// The production incident: finished, no verdicts. UNKNOWN, terminal, and
		// above all not SAFE — malicious=false is not a clearance.
		"finished without verdicts": {http.StatusOK, `{"task":{"uuid":"scan-1","success":true,"status":"finished"},` +
			`"verdicts":{"overall":{"hasVerdicts":false,"malicious":false}}}`, ReputationUnknown, nil},
		"server error":  {http.StatusInternalServerError, ``, "", ErrUnavailable},
		"rate limited":  {http.StatusTooManyRequests, ``, "", ErrUnavailable},
		"malformed":     {http.StatusOK, `{"task":`, "", ErrUnavailable},
		"wrong scan id": {http.StatusOK, `{"task":{"uuid":"other","success":true,"status":"finished"},"verdicts":{"overall":{"hasVerdicts":true,"malicious":false}}}`, "", ErrUnavailable},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			scanner, _ := scannerAgainst(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Fatalf("Check with a ref must poll, got %s", r.Method)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})

			result, err := scanner.Check(context.Background(), "https://example.com/", "scan-1")

			if !errors.Is(err, tc.err) {
				t.Fatalf("want %v, got %v", tc.err, err)
			}
			if result.Verdict != tc.verdict {
				t.Fatalf("verdict: want %q, got %q", tc.verdict, result.Verdict)
			}
			if err == nil && result.ProviderRef != "scan-1" {
				t.Fatalf("a terminal result must keep its ref, got %q", result.ProviderRef)
			}
		})
	}
}

func TestCloudflareCheckCarriesTheProviderEvidenceTime(t *testing.T) {
	scanner, _ := scannerAgainst(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"task":{"uuid":"scan-1","success":true,"status":"finished","timeEnd":"2026-01-02T03:04:05Z"},` +
			`"verdicts":{"overall":{"hasVerdicts":true,"malicious":false}}}`))
	})
	result, err := scanner.Check(context.Background(), "https://example.com/", "scan-1")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	want := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if !result.CheckedAt.Equal(want) {
		t.Fatalf("checked at: %v", result.CheckedAt)
	}
}

func TestLegacyVerdictMapping(t *testing.T) {
	if ReputationSafe.LegacyVerdict() != VerdictSafe || ReputationMalicious.LegacyVerdict() != VerdictMalicious {
		t.Fatal("safe/malicious must map onto themselves")
	}
	if ReputationUnknown.LegacyVerdict() != VerdictInconclusive || ReputationVerdict("").LegacyVerdict() != VerdictInconclusive {
		t.Fatal("anything else must map onto the fail-closed terminal value")
	}
}

// --- Service.Check over a synthetic provider ---------------------------------

// scriptedProvider answers from a queue, so a test decides each exchange.
type scriptedProvider struct {
	answers []scriptedAnswer
	calls   int
}

type scriptedAnswer struct {
	result ReputationResult
	err    error
}

func (p *scriptedProvider) Name() string { return "scripted" }

func (p *scriptedProvider) Check(_ context.Context, _, _ string) (ReputationResult, error) {
	p.calls++
	if len(p.answers) == 0 {
		return ReputationResult{}, ErrUnavailable
	}
	answer := p.answers[0]
	p.answers = p.answers[1:]
	return answer.result, answer.err
}

func TestServiceCheckPersistsTerminalAnswersInTheCache(t *testing.T) {
	provider := &scriptedProvider{answers: []scriptedAnswer{
		{result: ReputationResult{Verdict: ReputationSafe}},
	}}
	service := NewReputationService(provider, nil)

	result, err := service.Check(context.Background(), "https://example.com/", "")
	if err != nil || result.Verdict != ReputationSafe {
		t.Fatalf("Check: %+v, %v", result, err)
	}
	if verdict, ok := service.Lookup("https://example.com/"); !ok || verdict != VerdictSafe {
		t.Fatalf("a safe answer must be cached as a clearance, got %q/%v", verdict, ok)
	}
}

func TestServiceCheckNeverCachesUnknownAsAClearance(t *testing.T) {
	provider := &scriptedProvider{answers: []scriptedAnswer{
		{result: ReputationResult{Verdict: ReputationUnknown}},
	}}
	service := NewReputationService(provider, nil)

	result, err := service.Check(context.Background(), "https://example.com/", "")
	if err != nil || result.Verdict != ReputationUnknown {
		t.Fatalf("Check: %+v, %v", result, err)
	}
	if _, ok := service.Lookup("https://example.com/"); ok {
		t.Fatal("unknown must not be a cache hit")
	}
}

func TestServiceCheckRefusesAProviderThatInventsAVerdict(t *testing.T) {
	provider := &scriptedProvider{answers: []scriptedAnswer{
		{result: ReputationResult{Verdict: ReputationVerdict("trusted")}},
	}}
	service := NewReputationService(provider, nil)

	if _, err := service.Check(context.Background(), "https://example.com/", ""); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("an unknown verdict value must be unavailable, got %v", err)
	}
}

func TestServiceCheckPassesInProgressThrough(t *testing.T) {
	provider := &scriptedProvider{answers: []scriptedAnswer{
		{result: ReputationResult{ProviderRef: "ref-1"}, err: ErrCheckInProgress},
	}}
	service := NewReputationService(provider, nil)

	result, err := service.Check(context.Background(), "https://example.com/", "")
	if !errors.Is(err, ErrCheckInProgress) || result.ProviderRef != "ref-1" {
		t.Fatalf("Check: %+v, %v", result, err)
	}
}

func TestServiceCheckWithoutProviderIsUnavailable(t *testing.T) {
	service := NewService(nil, nil)
	if _, err := service.Check(context.Background(), "https://example.com/", ""); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("want ErrUnavailable, got %v", err)
	}
}

func TestServiceCheckReportsCallerCancellationAsItself(t *testing.T) {
	provider := &scriptedProvider{answers: []scriptedAnswer{{err: ErrUnavailable}}}
	service := NewReputationService(provider, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := service.Check(ctx, "https://example.com/", "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if service.CircuitState() != BreakerClosed {
		t.Fatal("a cancelled caller must not count against the breaker")
	}
}

// --- circuit breaker -----------------------------------------------------------

func TestServiceCheckOpensTheCircuitAfterConsecutiveFailures(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	provider := &scriptedProvider{}
	service := newService(nil, nil, clock)
	service.provider = provider
	service.SetBreaker(newBreaker(3, time.Minute, clock))

	for i := 0; i < 3; i++ {
		if _, err := service.Check(context.Background(), "https://example.com/", ""); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("failure %d: %v", i, err)
		}
	}
	if service.CircuitState() != BreakerOpen {
		t.Fatalf("circuit must be open after the threshold, got %s", service.CircuitState())
	}

	// Open: refused without a provider exchange, and the refusal is unavailable.
	calls := provider.calls
	_, err := service.Check(context.Background(), "https://example.com/", "")
	if !errors.Is(err, ErrCircuitOpen) || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("open circuit must report ErrCircuitOpen wrapping ErrUnavailable, got %v", err)
	}
	if provider.calls != calls {
		t.Fatal("an open circuit must not spend a provider exchange")
	}

	// After the cooldown: exactly one probe. A success closes the circuit.
	now = now.Add(time.Minute)
	provider.answers = []scriptedAnswer{{result: ReputationResult{Verdict: ReputationSafe}}}
	if _, err := service.Check(context.Background(), "https://example.com/", ""); err != nil {
		t.Fatalf("probe: %v", err)
	}
	if service.CircuitState() != BreakerClosed {
		t.Fatalf("a successful probe must close the circuit, got %s", service.CircuitState())
	}
}

func TestBreakerHalfOpenAdmitsOneProbeAndReopensOnFailure(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	breaker := newBreaker(1, time.Minute, func() time.Time { return now })

	if !breaker.Allow() {
		t.Fatal("closed must allow")
	}
	breaker.Complete(BreakerFailure)
	if breaker.State() != BreakerOpen || breaker.Allow() {
		t.Fatal("one failure at threshold 1 must open and refuse")
	}

	now = now.Add(time.Minute)
	if !breaker.Allow() {
		t.Fatal("cooldown elapsed: one probe must be admitted")
	}
	if breaker.State() != BreakerHalfOpen || breaker.Allow() {
		t.Fatal("half-open must admit exactly one probe at a time")
	}
	// C: a failed probe reopens the circuit.
	breaker.Complete(BreakerFailure)
	if breaker.State() != BreakerOpen {
		t.Fatal("a failed probe must reopen the circuit")
	}
	if breaker.Allow() {
		t.Fatal("a reopened circuit restarts its cooldown")
	}
	// D: a successful probe closes it.
	now = now.Add(time.Minute)
	if !breaker.Allow() {
		t.Fatal("second cooldown elapsed: a probe must be admitted")
	}
	breaker.Complete(BreakerSuccess)
	if breaker.State() != BreakerClosed || !breaker.Allow() {
		t.Fatal("a successful probe must close the circuit")
	}
}

// Settlement (issue #807 CQ round 2): every admitted exchange ends exactly once,
// as a success, a failure, or neutrally; neutral touches no count and frees
// the probe.
func TestBreakerNeutralSettlementKeepsTheCountAndReleasesTheProbe(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	breaker := newBreaker(3, time.Minute, func() time.Time { return now })

	// A: closed, threshold-1 failures, then a local cancellation: the count
	// does not reset, so the next failure still opens the circuit.
	for i := 0; i < 2; i++ {
		breaker.Allow()
		breaker.Complete(BreakerFailure)
	}
	breaker.Allow()
	breaker.Complete(BreakerNeutral)
	if breaker.failures != 2 || breaker.State() != BreakerClosed {
		t.Fatalf("neutral touched the count: failures=%d state=%s", breaker.failures, breaker.State())
	}
	breaker.Allow()
	breaker.Complete(BreakerFailure)
	if breaker.State() != BreakerOpen {
		t.Fatal("the third failure must open the circuit: neutral did not reset the count")
	}

	// B: half-open, the probe is cancelled locally: the circuit stays half-open
	// and admits the next probe instead of being stuck behind one that never
	// finished.
	now = now.Add(time.Minute)
	if !breaker.Allow() || breaker.Allow() {
		t.Fatal("half-open must admit exactly one probe")
	}
	breaker.Complete(BreakerNeutral)
	if breaker.State() != BreakerHalfOpen {
		t.Fatalf("neutral must not close a half-open circuit, got %s", breaker.State())
	}
	if !breaker.Allow() {
		t.Fatal("after a neutral probe the next probe must be admitted")
	}
	breaker.Complete(BreakerSuccess)
	if breaker.State() != BreakerClosed {
		t.Fatal("the real probe's success closes the circuit")
	}
}

// F/G: what each error means for the breaker under the provider contract.
func TestBreakerOutcomeClassification(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, expire := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer expire()
	live := context.Background()
	for name, tc := range map[string]struct {
		ctx  context.Context
		err  error
		want BreakerOutcome
	}{
		"verdict":                     {live, nil, BreakerSuccess},
		"check in progress":           {live, ErrCheckInProgress, BreakerSuccess},
		"scan pending":                {live, ErrScanPending, BreakerSuccess},
		"scan inconclusive":           {live, ErrScanInconclusive, BreakerSuccess},
		"unavailable":                 {live, ErrUnavailable, BreakerFailure},
		"generic provider error":      {live, errors.New("boom"), BreakerFailure},
		"deadline on the provider":    {live, context.DeadlineExceeded, BreakerFailure},
		"caller deadline elapsed":     {expired, context.DeadlineExceeded, BreakerFailure},
		"caller cancelled":            {cancelled, context.Canceled, BreakerNeutral},
		"caller cancelled, other err": {cancelled, ErrUnavailable, BreakerNeutral},
		"wrapped cancellation":        {live, fmt.Errorf("x: %w", context.Canceled), BreakerNeutral},
	} {
		t.Run(name, func(t *testing.T) {
			if got := breakerOutcome(tc.ctx, tc.err); got != tc.want {
				t.Fatalf("outcome = %d, want %d", got, tc.want)
			}
		})
	}
}

// E/H through the service: a generic provider error is a failure, a cancelled
// caller is neutral, and every admitted exchange settles exactly once — the
// probe slot is always free again afterwards.
func TestServiceExchangesSettleTheBreakerExactlyOnce(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	for name, tc := range map[string]struct {
		ctx       context.Context
		err       error
		failures  int
		state     BreakerState
		wantState BreakerState
	}{
		"generic error counts a failure":      {context.Background(), errors.New("boom"), 1, BreakerClosed, BreakerClosed},
		"unavailable counts a failure":        {context.Background(), ErrUnavailable, 1, BreakerClosed, BreakerClosed},
		"in progress is a success":            {context.Background(), ErrCheckInProgress, 0, BreakerClosed, BreakerClosed},
		"cancelled caller is neutral":         {cancelledContext(), ErrUnavailable, 0, BreakerClosed, BreakerClosed},
		"generic error ends a probe open":     {context.Background(), errors.New("boom"), 1, BreakerHalfOpen, BreakerOpen},
		"cancelled probe stays half-open":     {cancelledContext(), context.Canceled, 0, BreakerHalfOpen, BreakerHalfOpen},
		"successful probe closes the circuit": {context.Background(), nil, 0, BreakerHalfOpen, BreakerClosed},
	} {
		t.Run(name, func(t *testing.T) {
			provider := &scriptedProvider{answers: []scriptedAnswer{{result: ReputationResult{Verdict: ReputationSafe}, err: tc.err}}}
			service := newService(nil, nil, clock)
			service.provider = provider
			breaker := newBreaker(3, time.Minute, clock)
			if tc.state == BreakerHalfOpen {
				breaker.state, breaker.openedAt = BreakerOpen, now.Add(-time.Hour)
			}
			service.SetBreaker(breaker)

			_, _ = service.Check(tc.ctx, "https://example.com/", "")

			if breaker.probing {
				t.Fatal("the exchange left the probe slot taken: no settlement")
			}
			if breaker.failures != tc.failures || breaker.State() != tc.wantState {
				t.Fatalf("failures=%d state=%s, want %d %s", breaker.failures, breaker.State(), tc.failures, tc.wantState)
			}
			// Exactly once: a second settlement would be visible as the probe
			// slot or the count moving without an Allow. Nothing else may have
			// touched the breaker after the exchange.
			if breaker.Allow() != (tc.wantState != BreakerOpen) {
				t.Fatalf("admission after settlement is wrong for state %s", tc.wantState)
			}
		})
	}
}

func cancelledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func TestBreakerDefaultsApplyToNonPositiveArguments(t *testing.T) {
	breaker := NewBreaker(0, 0)
	if breaker.threshold != defaultBreakerThreshold || breaker.cooldown != defaultBreakerCooldown {
		t.Fatalf("defaults: %d %v", breaker.threshold, breaker.cooldown)
	}
	service := NewService(nil, nil)
	service.SetBreaker(nil)
	if service.breaker == nil || service.CircuitState() != BreakerClosed {
		t.Fatal("SetBreaker(nil) must restore a default closed breaker")
	}
}

func TestSubmitAndPollHonourTheCircuit(t *testing.T) {
	scanner, _ := scannerAgainst(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	service := NewService(scanner, nil)
	service.SetBreaker(NewBreaker(1, time.Hour))

	if _, err := service.Submit(context.Background(), "https://example.com/"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("first submit: %v", err)
	}
	if _, err := service.Submit(context.Background(), "https://example.com/"); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("second submit must be refused by the open circuit, got %v", err)
	}
	if _, err := service.Poll(context.Background(), "https://example.com/", "scan-1"); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("poll must be refused by the open circuit, got %v", err)
	}
}

// Submit and Poll settle the same way Check does: a submission without an id
// and a generic poll error are failures, pending is the provider working, and
// a cancelled caller is neutral — the probe slot is free afterwards either way.
func TestSubmitAndPollSettleTheBreaker(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	newSvc := func(scanner *stubScanner) (*Service, *Breaker) {
		service := newService(scanner, nil, clock)
		breaker := newBreaker(3, time.Minute, clock)
		service.SetBreaker(breaker)
		return service, breaker
	}

	service, breaker := newSvc(&stubScanner{scanID: "   "})
	if _, err := service.Submit(context.Background(), "https://example.com/"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("empty scan id: %v", err)
	}
	if breaker.failures != 1 || breaker.probing {
		t.Fatalf("an empty scan id must count as a failure and settle: failures=%d probing=%v", breaker.failures, breaker.probing)
	}

	service, breaker = newSvc(&stubScanner{blockSubmit: make(chan struct{})})
	if _, err := service.Submit(cancelledContext(), "https://example.com/"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled submit: %v", err)
	}
	if breaker.failures != 0 || breaker.probing || breaker.State() != BreakerClosed {
		t.Fatalf("a cancelled submit must settle neutrally: failures=%d probing=%v", breaker.failures, breaker.probing)
	}

	service, breaker = newSvc(&stubScanner{resultErr: errors.New("boom")})
	if _, err := service.Poll(context.Background(), "https://example.com/", "scan-1"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("generic poll error: %v", err)
	}
	if breaker.failures != 1 || breaker.probing {
		t.Fatalf("a generic poll error must count as a failure and settle: failures=%d probing=%v", breaker.failures, breaker.probing)
	}

	service, breaker = newSvc(&stubScanner{resultErr: ErrScanPending})
	if _, err := service.Poll(context.Background(), "https://example.com/", "scan-1"); !errors.Is(err, ErrScanPending) {
		t.Fatalf("pending poll: %v", err)
	}
	if breaker.failures != 0 || breaker.probing {
		t.Fatal("a pending scan is the provider working")
	}
}
