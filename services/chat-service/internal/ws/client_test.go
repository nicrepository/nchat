package ws

import (
	"context"
	"sync"
	"testing"
)

func TestClient_Enqueue_WithinCapacity_Succeeds(t *testing.T) {
	snd := &fakeSender{}
	c := newClient("c1", "user-1", "ws-1", snd)

	for i := 0; i < outboxSize; i++ {
		if !c.enqueue([]byte("data")) {
			t.Fatalf("enqueue should succeed within capacity, failed at %d", i)
		}
	}
}

func TestClient_Enqueue_Overflow_ReturnsFalse(t *testing.T) {
	snd := &fakeSender{}
	c := newClient("c1", "user-1", "ws-1", snd)

	for i := 0; i < outboxSize; i++ {
		c.enqueue([]byte("data"))
	}

	if c.enqueue([]byte("overflow")) {
		t.Fatal("enqueue should return false when outbox is full")
	}
}

func TestClient_Close_IdempotentUnderConcurrency(t *testing.T) {
	snd := &fakeSender{}
	c := newClient("c1", "user-1", "ws-1", snd)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.close()
		}()
	}
	wg.Wait()

	if !snd.isClosed() {
		t.Fatal("connection should be closed")
	}
}

func TestClient_Close_CalledOnce_EvenConcurrently(t *testing.T) {
	closeCount := 0
	var mu sync.Mutex
	countingSender := &countingCloseSender{onClose: func() {
		mu.Lock()
		closeCount++
		mu.Unlock()
	}}
	c := newClient("c1", "user-1", "ws-1", countingSender)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.close()
		}()
	}
	wg.Wait()

	mu.Lock()
	count := closeCount
	mu.Unlock()

	if count != 1 {
		t.Fatalf("Close should be called exactly once, was called %d times", count)
	}
}

// countingCloseSender is a sender that invokes onClose each time Close is called.
type countingCloseSender struct {
	onClose func()
}

func (s *countingCloseSender) Send(_ []byte) error          { return nil }
func (s *countingCloseSender) Ping(_ context.Context) error { return nil }
func (s *countingCloseSender) Close()                       { s.onClose() }
