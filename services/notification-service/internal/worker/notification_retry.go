package worker

import (
	cryptorand "crypto/rand"
	"math/big"
	"time"
)

// RetryPolicy decides when a failed delivery is due again.
//
// Separate from the worker because it is the one part of the retry design that
// is pure arithmetic, and arithmetic that has to be exercised at attempt one and
// at attempt twenty without waiting for either. The worker holds a policy; the
// policy holds no state.
type RetryPolicy struct {
	// Base is the first step. Max is the ceiling the doubling stops at.
	Base time.Duration
	Max  time.Duration

	// Jitter returns a value in [0, span). Injected so a test can pin the
	// randomness and assert an exact schedule; nil means crypto/rand.
	//
	// Jitter is what stops a provider outage from becoming a thundering herd.
	// Without it every event that failed in the same pass becomes due in the same
	// instant, and every replica wakes to claim them together — the retry storm
	// the whole backoff exists to prevent.
	Jitter func(span int64) int64
}

// retryMaxShift bounds the doubling.
//
// 2^20 of any base this configuration permits is already far past Max, so the
// clamp changes no reachable result — it exists so that an attempt count that
// somehow ran away cannot shift a Duration into a negative number. The overflow
// is caught a second time below, because a silent negative delay would make a
// failed notification due immediately, forever.
const retryMaxShift = 20

// Delay returns how long to wait before attempt+1, for a delivery that failed on
// the given attempt (which counts from one).
//
// Exponential, capped, then jittered: base·2^(attempt-1), bounded by Max, and
// finally spread across the upper half of that window. The result is always in
// [delay/2, delay), so it is never zero — a zero delay is a busy loop wearing a
// backoff's clothes — and never longer than the ceiling.
func (p RetryPolicy) Delay(attempt int) time.Duration {
	base, ceiling := p.bounds()

	shift := min(max(attempt-1, 0), retryMaxShift)
	delay := base << shift
	if delay <= 0 || delay > ceiling {
		delay = ceiling
	}
	return p.spread(delay)
}

// bounds returns the policy's two limits, defaulted and ordered.
//
// A zero policy is usable rather than degenerate, and a ceiling below the first
// step is raised to it: a backoff that shrinks as attempts grow is a typo, not a
// policy.
func (p RetryPolicy) bounds() (base, ceiling time.Duration) {
	base = p.Base
	if base <= 0 {
		base = time.Second
	}
	ceiling = p.Max
	if ceiling < base {
		ceiling = base
	}
	return base, ceiling
}

// spread applies the jitter, keeping the result inside [delay/2, delay).
func (p RetryPolicy) spread(delay time.Duration) time.Duration {
	half := delay / 2
	if half <= 0 {
		return delay
	}
	return half + time.Duration(p.randomBelow(int64(half)))
}

// randomBelow returns a value in [0, span), from the injected source or from
// crypto/rand.
//
// crypto/rand rather than math/rand: the value is not a secret, but it is drawn
// a handful of times per pass, the cost is irrelevant at that rate, and a
// predictable jitter shared by every replica would defeat the only thing jitter
// is for. A source that fails yields no jitter at all, which is still a bounded,
// correct delay.
func (p RetryPolicy) randomBelow(span int64) int64 {
	if span <= 0 {
		return 0
	}
	if p.Jitter != nil {
		return min(max(p.Jitter(span), 0), span-1)
	}
	value, err := cryptorand.Int(cryptorand.Reader, big.NewInt(span))
	if err != nil {
		return 0
	}
	return value.Int64()
}
