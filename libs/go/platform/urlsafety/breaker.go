package urlsafety

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// A circuit breaker for the reputation provider (issue #807).
//
// The failure it exists for: the provider answers 429, 5xx, malformed bodies or
// nothing at all for a while, and every worker pass on every replica keeps
// spending a request on it. The rows converge anyway — each has a deadline —
// but the requests are wasted, the provider's quota is burned on failures, and a
// 429 storm makes the outage longer. So after a run of consecutive failures the
// breaker opens, the pipeline stops asking, and the rows wait for their deadline
// or for the cooldown to elapse, whichever comes first.
//
// Deliberately small and deliberately here rather than a dependency: three
// states, one counter, one clock. What it protects is a single external
// exchange, and a library that also does bulkheads, sliding windows and
// per-endpoint statistics would be a second thing to audit for the same
// sentence of behaviour.
//
// It is never a clearance. An open circuit produces ErrUnavailable, exactly like
// a failed exchange, and the pipeline treats it exactly the same way.

// BreakerState is the circuit's position, as a closed set for the gauge.
type BreakerState string

const (
	// BreakerClosed passes every request and counts failures.
	BreakerClosed BreakerState = "closed"
	// BreakerOpen refuses every request until the cooldown elapses.
	BreakerOpen BreakerState = "open"
	// BreakerHalfOpen lets exactly one probe through; its outcome decides.
	BreakerHalfOpen BreakerState = "half_open"
)

// ErrCircuitOpen is what a refused exchange reports. It wraps ErrUnavailable so
// every caller that already handles an unavailable provider handles this without
// a new branch — and so nothing can read it as an answer about the URL.
var ErrCircuitOpen = fmt.Errorf("url safety: provider circuit open: %w", ErrUnavailable)

// Breaker defaults. Five consecutive failures is long enough that a single
// flaky exchange never opens the circuit, and short enough that a real outage
// is noticed within one worker batch. The cooldown matches FailureTTL's order
// of magnitude: an outage is re-probed once a minute, not once a pass.
const (
	defaultBreakerThreshold = 5
	defaultBreakerCooldown  = time.Minute
)

// Breaker is the circuit. The zero value is not usable; use NewBreaker.
type Breaker struct {
	mu        sync.Mutex
	state     BreakerState
	failures  int
	openedAt  time.Time
	probing   bool
	threshold int
	cooldown  time.Duration
	now       func() time.Time
}

// NewBreaker builds a closed breaker. threshold <= 0 or cooldown <= 0 select the
// defaults.
func NewBreaker(threshold int, cooldown time.Duration) *Breaker {
	return newBreaker(threshold, cooldown, time.Now)
}

func newBreaker(threshold int, cooldown time.Duration, now func() time.Time) *Breaker {
	if threshold <= 0 {
		threshold = defaultBreakerThreshold
	}
	if cooldown <= 0 {
		cooldown = defaultBreakerCooldown
	}
	if now == nil {
		now = time.Now
	}
	return &Breaker{state: BreakerClosed, threshold: threshold, cooldown: cooldown, now: now}
}

// Allow reports whether one exchange may be attempted now.
//
// Closed always allows. Open allows nothing until the cooldown has elapsed, at
// which point the circuit becomes half-open and admits exactly one probe; every
// other caller is refused until Record settles that probe.
func (b *Breaker) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case BreakerClosed:
		return true
	case BreakerOpen:
		if b.now().Sub(b.openedAt) < b.cooldown {
			return false
		}
		b.state, b.probing = BreakerHalfOpen, true
		return true
	default:
		if b.probing {
			return false
		}
		b.probing = true
		return true
	}
}

// BreakerOutcome is how an exchange Allow admitted ended. Every admitted
// exchange ends in exactly one of these, exactly once — that is the invariant
// a half-open circuit's single probe depends on.
type BreakerOutcome int

const (
	// BreakerSuccess is the provider answering: a verdict, an in-progress
	// check, a pending or inconclusive scan. It closes the circuit.
	BreakerSuccess BreakerOutcome = iota
	// BreakerFailure is the provider not answering usefully: unavailable,
	// timed out, malformed, or any error the contract does not name. It counts
	// toward the threshold and ends a probe with the circuit open.
	BreakerFailure
	// BreakerNeutral is the exchange not concluding for a local reason — the
	// caller went away — which says nothing about the provider: no count is
	// touched and the probe slot is simply released.
	BreakerNeutral
)

// Complete settles an exchange Allow admitted.
//
// A success closes the circuit and clears the count; a failure counts toward
// the threshold when closed and reopens the circuit immediately when half-open
// — a failed probe is the outage still going on; a neutral outcome releases
// the probe and changes nothing else, so a half-open circuit admits the next
// probe rather than staying stuck behind one that never finished.
func (b *Breaker) Complete(outcome BreakerOutcome) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.probing = false
	switch outcome {
	case BreakerSuccess:
		b.state, b.failures = BreakerClosed, 0
	case BreakerFailure:
		b.failures++
		if b.state == BreakerHalfOpen || b.failures >= b.threshold {
			b.state, b.openedAt = BreakerOpen, b.now()
		}
	}
}

// State reports the current position, for the gauge.
func (b *Breaker) State() BreakerState {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

// breakerOutcome classifies one exchange for the breaker.
//
// The caller going away — context.Canceled — is neutral: it is not a fact
// about the provider. A deadline elapsing while the provider was being waited
// on is a failure: the provider did not answer in the time it was given, which
// is the same fact a transport timeout reports. An in-progress, pending or
// inconclusive answer is the provider working. Everything else the contract
// does not name — ErrUnavailable and any generic error alike — is a failure,
// so a probe never ends unsettled.
func breakerOutcome(ctx context.Context, err error) BreakerOutcome {
	switch {
	case errors.Is(ctx.Err(), context.Canceled), errors.Is(err, context.Canceled):
		return BreakerNeutral
	case err == nil, errors.Is(err, ErrCheckInProgress), errors.Is(err, ErrScanPending), errors.Is(err, ErrScanInconclusive):
		return BreakerSuccess
	default:
		return BreakerFailure
	}
}
