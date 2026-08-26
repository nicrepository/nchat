package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// A worker that has been cancelled and returns lets shutdown complete at once.
func TestAwaitWaitGroupReturnsWhenWorkersFinish(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(5 * time.Millisecond)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := awaitWaitGroup(ctx, &wg); err != nil {
		t.Fatalf("awaitWaitGroup: %v", err)
	}
}

// The regression this guards: wg.Wait() has no deadline, so a worker that never
// returns held the process open until the kubelet killed it mid-cleanup.
func TestAwaitWaitGroupGivesUpAtTheDeadline(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)
	release := make(chan struct{})
	go func() {
		defer wg.Done()
		<-release
	}()
	t.Cleanup(func() { close(release) })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := awaitWaitGroup(ctx, &wg)
	elapsed := time.Since(start)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("awaitWaitGroup returned %v, want a deadline error", err)
	}
	if elapsed > time.Second {
		t.Fatalf("awaitWaitGroup waited %s past its 30ms deadline", elapsed)
	}
}

// An already-expired context must not be treated as permission to wait.
func TestAwaitWaitGroupRespectsAnExpiredContext(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)
	release := make(chan struct{})
	go func() {
		defer wg.Done()
		<-release
	}()
	t.Cleanup(func() { close(release) })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := awaitWaitGroup(ctx, &wg); err == nil {
		t.Fatal("awaitWaitGroup ignored a cancelled context")
	}
}

func TestFirstShutdownErrorReportsTheFirstFailure(t *testing.T) {
	wanted := errors.New("hub stuck")
	if got := firstShutdownError(nil, wanted, errors.New("later")); !errors.Is(got, wanted) {
		t.Fatalf("firstShutdownError returned %v, want %v", got, wanted)
	}
	if got := firstShutdownError(nil, nil); got != nil {
		t.Fatalf("firstShutdownError returned %v for a clean shutdown", got)
	}
}
