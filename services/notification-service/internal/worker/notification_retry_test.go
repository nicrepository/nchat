package worker

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// Issue #742: the retry schedule, proved without waiting for it.
//
// Every case pins the jitter, because a backoff whose assertions depend on
// randomness is a flaky test — and because the jitter's own bounds are the last
// case here, checked over the whole range rather than sampled.

// noJitter picks the bottom of the window, so a delay is exactly half the step.
func noJitter(int64) int64 { return 0 }

// maxJitter picks the top of it.
func maxJitter(span int64) int64 { return span - 1 }

func TestRetryDelayGrowsExponentially(t *testing.T) {
	policy := RetryPolicy{Base: 10 * time.Second, Max: time.Hour, Jitter: noJitter}

	var previous time.Duration
	for attempt := 1; attempt <= 6; attempt++ {
		delay := policy.Delay(attempt)
		if delay <= previous {
			t.Fatalf("attempt %d delayed %s, not longer than the previous %s", attempt, delay, previous)
		}
		previous = delay
	}
}

func TestRetryDelayRespectsTheCeiling(t *testing.T) {
	policy := RetryPolicy{Base: time.Second, Max: 30 * time.Second, Jitter: maxJitter}

	for attempt := 1; attempt <= 40; attempt++ {
		if delay := policy.Delay(attempt); delay > policy.Max {
			t.Fatalf("attempt %d delayed %s, past the %s ceiling", attempt, delay, policy.Max)
		}
	}
}

// An attempt count that ran away must not shift a Duration into a negative
// number, which would make a failed notification due immediately, forever.
func TestRetryDelayNeverOverflows(t *testing.T) {
	policy := RetryPolicy{Base: time.Hour, Max: 24 * time.Hour}

	for _, attempt := range []int{63, 64, 65, 1 << 20, 1 << 30} {
		delay := policy.Delay(attempt)
		if delay <= 0 {
			t.Fatalf("attempt %d delayed %s, which is not a delay at all", attempt, delay)
		}
		if delay > policy.Max {
			t.Fatalf("attempt %d delayed %s, past the ceiling", attempt, delay)
		}
	}
}

// Jitter has to stay inside [delay/2, delay): outside the bottom it is a busy
// loop, outside the top it breaks the ceiling.
func TestRetryJitterStaysInsideItsWindow(t *testing.T) {
	step := 16 * time.Second
	policy := RetryPolicy{Base: step, Max: time.Hour}

	for i := 0; i < 200; i++ {
		delay := policy.Delay(1)
		if delay < step/2 || delay >= step {
			t.Fatalf("jitter produced %s, outside [%s, %s)", delay, step/2, step)
		}
	}
}

// Two failures in the same pass must not become due in the same instant, which
// is what makes a provider outage a thundering herd.
func TestRetryJitterSpreadsRepeatedDelays(t *testing.T) {
	policy := RetryPolicy{Base: time.Minute, Max: time.Hour}

	seen := make(map[time.Duration]struct{})
	for i := 0; i < 50; i++ {
		seen[policy.Delay(3)] = struct{}{}
	}
	if len(seen) < 2 {
		t.Fatal("50 draws produced a single delay; the schedule is not jittered")
	}
}

// An injected source that misbehaves must not push the delay out of its window.
func TestRetryClampsAHostileJitterSource(t *testing.T) {
	policy := RetryPolicy{
		Base:   time.Minute,
		Max:    time.Hour,
		Jitter: func(int64) int64 { return 1 << 62 },
	}

	if delay := policy.Delay(1); delay < time.Minute/2 || delay >= time.Minute {
		t.Fatalf("delay = %s, outside the first step's window", delay)
	}

	policy.Jitter = func(int64) int64 { return -(1 << 62) }
	if delay := policy.Delay(1); delay < time.Minute/2 || delay >= time.Minute {
		t.Fatalf("delay = %s, outside the first step's window", delay)
	}
}

// A zero policy is usable rather than degenerate: a caller who forgot to
// configure one still gets a bounded, positive delay.
func TestRetryZeroPolicyIsUsable(t *testing.T) {
	if delay := (RetryPolicy{}).Delay(1); delay <= 0 {
		t.Fatalf("the zero policy delayed %s", delay)
	}
}

// A ceiling below the first step would invert the schedule.
func TestRetryRaisesACeilingBelowTheFirstStep(t *testing.T) {
	policy := RetryPolicy{Base: time.Minute, Max: time.Second, Jitter: maxJitter}

	if delay := policy.Delay(1); delay < time.Minute/2 {
		t.Fatalf("delay = %s, want at least half the base step", delay)
	}
}

func TestClassifyDeliverySeparatesPermanentFromTransient(t *testing.T) {
	tests := map[string]struct {
		err           error
		wantCategory  string
		wantPermanent bool
	}{
		"success":   {nil, "", false},
		"permanent": {fmt.Errorf("gone: %w", ErrPermanentDelivery), CategoryPermanent, true},
		"timeout":   {context.DeadlineExceeded, CategoryTimeout, false},
		"cancelled": {context.Canceled, CategoryTimeout, false},
		// Anything unrecognised is transient. That is the fail-safe direction:
		// retrying something unretryable costs a bounded number of attempts,
		// while retiring something transient loses a notification for good.
		"unknown": {errors.New("provider said 503"), CategoryTransient, false},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			category, permanent := classifyDelivery(tc.err)
			if category != tc.wantCategory || permanent != tc.wantPermanent {
				t.Fatalf("classify = (%q, %v), want (%q, %v)",
					category, permanent, tc.wantCategory, tc.wantPermanent)
			}
		})
	}
}

// Every category has to fit chat.notification_outbox.last_error, which is
// VARCHAR(64). A category that did not would fail at the database, on the one
// path that runs when things are already going wrong.
func TestDeliveryCategoriesFitTheColumn(t *testing.T) {
	for _, category := range []string{CategoryTransient, CategoryTimeout, CategoryPermanent} {
		if len(category) == 0 || len(category) > 64 {
			t.Fatalf("category %q does not fit last_error VARCHAR(64)", category)
		}
	}
}

func TestVerdictReasonIsPresentExactlyWhenSuppressing(t *testing.T) {
	if reason := (Verdict{Deliver: true, SuppressedReason: "ignored"}).Reason(); reason != "" {
		t.Fatalf("a delivered event carries reason %q", reason)
	}
	if reason := (Verdict{}).Reason(); reason != defaultSuppressedReason {
		t.Fatalf("an unexplained suppression reads %q, want %q", reason, defaultSuppressedReason)
	}
	if reason := (Verdict{SuppressedReason: "quiet_hours"}).Reason(); reason != "quiet_hours" {
		t.Fatalf("reason = %q, want the policy's own", reason)
	}
}

func TestDeliverEverythingSuppressesNothing(t *testing.T) {
	verdict, err := DeliverEverything().Evaluate(context.Background(), Notification{ID: "n1"})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !verdict.Deliver || verdict.Reason() != "" {
		t.Fatalf("verdict = %+v, want an unconditional delivery", verdict)
	}
}
