package worker

import (
	"errors"
	"testing"
	"time"
)

// Issue #746: a provider's own opinion about when to try again.
//
// Retry-After is the one case where an adapter knows something RetryPolicy
// cannot work out. It is honoured as a floor and never as the schedule, and the
// two bounds either side of it are what these tests pin: a rate limiter cannot
// make this service retry sooner than its own backoff, and cannot park a
// notification past the configured ceiling.

func retryTestWorker(t *testing.T) *NotificationWorker {
	t.Helper()
	cfg := notificationTestConfig()
	cfg.RetryBaseSeconds = 30
	cfg.RetryMaxSeconds = 300
	worker := NewNotificationWorker(cfg, NotificationWorkerDeps{
		Store:  newFakeOutbox(),
		Logger: silentLogger(),
	})
	// A pinned jitter makes the backoff exact: the policy spreads across
	// [delay/2, delay), so zero jitter is the bottom of that window.
	worker.retry.Jitter = func(int64) int64 { return 0 }
	return worker
}

func TestRetryDelayIsTheBackoffWhenNothingWasRequested(t *testing.T) {
	worker := retryTestWorker(t)
	plain := errors.New("provider failed")

	// Attempt one: 30s backoff, halved by the zero jitter.
	if got := worker.retryDelay(1, plain); got != 15*time.Second {
		t.Fatalf("delay = %v, want the backoff alone", got)
	}
}

// A request shorter than the backoff is ignored. Honouring it would replace an
// exponential backoff with a tight loop against something already rate limiting
// us — which is the failure mode Retry-After is most often used to cause.
func TestAShortRetryAfterCannotShortenTheBackoff(t *testing.T) {
	worker := retryTestWorker(t)

	for _, requested := range []time.Duration{time.Second, 5 * time.Second, 14 * time.Second} {
		err := &RetryAfterError{After: requested, Err: errors.New("rate limited")}
		if got := worker.retryDelay(1, err); got != 15*time.Second {
			t.Fatalf("a %v request produced a %v delay, want the backoff", requested, got)
		}
	}
}

// A request longer than the backoff wins: a rate limiter that genuinely needs
// more time than the ladder has reached is the only thing this is for.
func TestALongerRetryAfterRaisesTheDelay(t *testing.T) {
	worker := retryTestWorker(t)
	err := &RetryAfterError{After: 90 * time.Second, Err: errors.New("rate limited")}

	if got := worker.retryDelay(1, err); got != 90*time.Second {
		t.Fatalf("delay = %v, want the requested ninety seconds", got)
	}
}

// The configured ceiling still holds. One header must not park a notification
// past its own TTL or past every remaining attempt.
func TestRetryAfterIsCappedByTheConfiguredCeiling(t *testing.T) {
	worker := retryTestWorker(t)
	err := &RetryAfterError{After: 24 * time.Hour, Err: errors.New("rate limited")}

	if got := worker.retryDelay(1, err); got != 300*time.Second {
		t.Fatalf("delay = %v, want the RetryMaxSeconds ceiling", got)
	}
}

// The wrapper changes nothing about how a failure is classified: it wraps an
// ordinary error, so it is transient, and it wraps a permanent one transparently
// when an adapter has reason to.
func TestRetryAfterDoesNotChangeClassification(t *testing.T) {
	transient := &RetryAfterError{After: time.Minute, Err: errors.New("rate limited")}
	if category, permanent := classifyDelivery(transient); permanent {
		t.Fatalf("a wrapped transient failure classified as permanent (%s)", category)
	}

	permanent := &RetryAfterError{After: time.Minute, Err: ErrPermanentDelivery}
	if _, isPermanent := classifyDelivery(permanent); !isPermanent {
		t.Fatal("the wrapper hid a permanent failure")
	}
}

// Unwrapping reaches the failure itself, so the message an operator sees is the
// adapter's and not the wrapper's.
func TestRetryAfterUnwrapsToItsCause(t *testing.T) {
	cause := errors.New("every push endpoint is rate limited")
	err := &RetryAfterError{After: time.Minute, Err: cause}

	if !errors.Is(err, cause) {
		t.Fatal("the wrapper does not unwrap to its cause")
	}
	if err.Error() != cause.Error() {
		t.Fatalf("Error() = %q, want the cause's own message", err.Error())
	}
}

// A request of zero — the adapter looked and the provider asked for nothing —
// is not a request at all.
func TestAZeroRetryAfterRequestsNothing(t *testing.T) {
	for name, err := range map[string]error{
		"no wrapper":     errors.New("failed"),
		"zero request":   &RetryAfterError{After: 0, Err: errors.New("failed")},
		"negative":       &RetryAfterError{After: -time.Minute, Err: errors.New("failed")},
		"nothing at all": nil,
	} {
		if got := requestedRetryDelay(err); got != 0 {
			t.Fatalf("%s requested %v, want nothing", name, got)
		}
	}
}

// The ceiling comes from the configuration rather than from a constant, so a
// deployment that lengthened its ladder gets the longer bound too.
func TestTheCeilingFollowsTheConfiguration(t *testing.T) {
	cfg := notificationTestConfig()
	cfg.RetryBaseSeconds = 1
	cfg.RetryMaxSeconds = 3600
	worker := NewNotificationWorker(cfg, NotificationWorkerDeps{
		Store: newFakeOutbox(), Logger: silentLogger(),
	})
	worker.retry.Jitter = func(int64) int64 { return 0 }

	err := &RetryAfterError{After: 2 * time.Hour, Err: errors.New("rate limited")}
	if got := worker.retryDelay(1, err); got != time.Hour {
		t.Fatalf("delay = %v, want the configured hour", got)
	}
}
