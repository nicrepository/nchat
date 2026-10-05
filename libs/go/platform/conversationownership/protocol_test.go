package conversationownership

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

type stateError string

func (e stateError) Error() string    { return string(e) }
func (e stateError) SQLState() string { return string(e) }

func TestRetry(t *testing.T) {
	for _, tc := range []struct {
		name, state        string
		failures, attempts int
	}{
		{"serialization recovers", "40001", 2, 3},
		{"deadlock recovers", "40P01", 1, 2},
		{"bounded", "40001", 4, 3},
		{"domain conflict is final", "P0953", 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			value, err := Retry(t.Context(), func() (int, error) {
				calls++
				if calls <= tc.failures {
					return 0, fmt.Errorf("wrapped: %w", stateError(tc.state))
				}
				return 42, nil
			})
			if calls != tc.attempts {
				t.Fatalf("calls=%d", calls)
			}
			if tc.failures < tc.attempts && (err != nil || value != 42) {
				t.Fatalf("value=%d err=%v", value, err)
			}
			if tc.failures >= tc.attempts && SQLState(err) != tc.state {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestRetryCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	calls := 0
	_, err := Retry(ctx, func() (bool, error) { calls++; cancel(); return false, stateError("40001") })
	if calls != 1 || !errors.Is(err, context.Canceled) {
		t.Fatalf("calls=%d error=%v", calls, err)
	}
	if SQLState(errors.New("ordinary")) != "" {
		t.Fatal("ordinary error has SQL state")
	}
}
