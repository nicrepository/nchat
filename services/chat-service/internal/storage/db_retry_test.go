package storage

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// stubPool satisfies Pool without a database; its methods are never called by
// the retry loop.
type stubPool struct{}

func (stubPool) Begin(context.Context) (pgx.Tx, error) { return nil, errors.New("stub") }
func (stubPool) QueryRow(context.Context, string, ...any) pgx.Row {
	return nil
}
func (stubPool) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("stub")
}
func (stubPool) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errors.New("stub")
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeSleep records requested backoff durations and never really sleeps.
type fakeSleep struct {
	delays []time.Duration
}

func (f *fakeSleep) sleep(ctx context.Context, d time.Duration) error {
	f.delays = append(f.delays, d)
	return ctx.Err()
}

func TestOpenDBWithRetry_SucceedsOnFirstAttempt(t *testing.T) {
	sleeper := &fakeSleep{}
	attempts := 0
	connect := func(context.Context) (Pool, error) {
		attempts++
		return stubPool{}, nil
	}

	pool, err := openDBWithRetry(context.Background(), connect, discardLogger(), sleeper.sleep)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pool == nil {
		t.Fatal("expected pool")
	}
	if attempts != 1 {
		t.Fatalf("expected exactly one attempt, got %d", attempts)
	}
	if len(sleeper.delays) != 0 {
		t.Fatalf("expected no sleeps on immediate success, got %v", sleeper.delays)
	}
}

func TestOpenDBWithRetry_RecoversAfterTransientFailures(t *testing.T) {
	sleeper := &fakeSleep{}
	attempts := 0
	connect := func(context.Context) (Pool, error) {
		attempts++
		if attempts < 3 {
			return nil, errors.New("dial error with sensitive dsn detail")
		}
		return stubPool{}, nil
	}

	pool, err := openDBWithRetry(context.Background(), connect, discardLogger(), sleeper.sleep)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pool == nil {
		t.Fatal("expected pool after recovery")
	}
	if attempts != 3 {
		t.Fatalf("expected three attempts, got %d", attempts)
	}
	want := []time.Duration{500 * time.Millisecond, time.Second}
	if len(sleeper.delays) != len(want) {
		t.Fatalf("expected %d backoff sleeps, got %v", len(want), sleeper.delays)
	}
	for i := range want {
		if sleeper.delays[i] != want[i] {
			t.Fatalf("expected backoff %v at index %d, got %v", want[i], i, sleeper.delays[i])
		}
	}
}

func TestOpenDBWithRetry_BackoffIsCapped(t *testing.T) {
	sleeper := &fakeSleep{}
	attempts := 0
	ctx, cancel := context.WithCancel(context.Background())
	connect := func(context.Context) (Pool, error) {
		attempts++
		if attempts == 8 {
			cancel() // stop after enough attempts to hit the cap
		}
		return nil, errors.New("still down")
	}

	_, err := openDBWithRetry(ctx, connect, discardLogger(), sleeper.sleep)

	if !errors.Is(err, ErrDBBootstrapFailed) {
		t.Fatalf("expected ErrDBBootstrapFailed, got %v", err)
	}
	last := sleeper.delays[len(sleeper.delays)-1]
	if last != dbRetryMaxBackoff {
		t.Fatalf("expected final backoff capped at %v, got %v", dbRetryMaxBackoff, last)
	}
}

func TestOpenDBWithRetry_AllAttemptsFail_ReturnsSanitizedError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	attempts := 0
	sensitiveKey := "pass" + "word"
	sensitiveValue := "sentinel-" + "credential"
	sensitiveHost := "db." + "sentinel.invalid"
	internalFragment := "dial " + "tcp"
	rawError := internalFragment + ": " + sensitiveKey + "=" + sensitiveValue + " host=" + sensitiveHost
	connect := func(context.Context) (Pool, error) {
		attempts++
		cancel() // ctx done → the retry loop must stop after this attempt
		return nil, errors.New(rawError)
	}
	var logs strings.Builder
	logger := slog.New(slog.NewTextHandler(&logs, nil))

	pool, err := openDBWithRetry(ctx, connect, logger, (&fakeSleep{}).sleep)

	if pool != nil {
		t.Fatal("expected no pool")
	}
	if !errors.Is(err, ErrDBBootstrapFailed) {
		t.Fatalf("expected ErrDBBootstrapFailed, got %v", err)
	}
	if attempts != 1 {
		t.Fatalf("expected one attempt before ctx cancellation stopped retry, got %d", attempts)
	}
	if got := err.Error(); got != ErrDBBootstrapFailed.Error() {
		t.Fatalf("error must stay sanitized, got %q", got)
	}
	for _, sensitive := range []string{sensitiveKey, sensitiveValue, sensitiveHost, internalFragment, rawError} {
		if strings.Contains(err.Error(), sensitive) {
			t.Fatalf("public error contains sensitive fixture fragment %q", sensitive)
		}
		if strings.Contains(logs.String(), sensitive) {
			t.Fatalf("log contains sensitive fixture fragment %q", sensitive)
		}
	}
}

func TestOpenDBWithRetry_ContextCancellationStopsRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	attempts := 0
	connect := func(context.Context) (Pool, error) {
		attempts++
		return nil, errors.New("down")
	}

	_, err := openDBWithRetry(ctx, connect, discardLogger(), (&fakeSleep{}).sleep)

	if !errors.Is(err, ErrDBBootstrapFailed) {
		t.Fatalf("expected ErrDBBootstrapFailed, got %v", err)
	}
	if attempts != 1 {
		t.Fatalf("expected retry to stop immediately after cancellation, got %d attempts", attempts)
	}
}

func TestSleepContext_ReturnsErrorWhenContextDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepContext(ctx, time.Minute); err == nil {
		t.Fatal("expected context error")
	}
}

func TestSleepContext_ReturnsNilAfterDelay(t *testing.T) {
	if err := sleepContext(context.Background(), time.Millisecond); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
