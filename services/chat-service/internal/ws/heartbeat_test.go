package ws

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// ── controllableSender ────────────────────────────────────────────────────────

// controllableSender is a sender whose Ping can be configured to fail and that
// can notify a channel on each Ping call for deterministic test coordination.
// All methods are safe for concurrent use (mutex-protected), satisfying the
// sender concurrency contract.
type controllableSender struct {
	mu           sync.Mutex
	closed       bool
	pingError    error
	pingNotifyCh chan<- struct{} // if non-nil, receives one signal per Ping call
}

func (s *controllableSender) Send(_ []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("connection closed")
	}
	return nil
}

func (s *controllableSender) Ping(_ context.Context) error {
	s.mu.Lock()
	closed := s.closed
	err := s.pingError
	ch := s.pingNotifyCh
	s.mu.Unlock()

	// Signal after releasing the lock to prevent deadlock if the test waits
	// on the channel while holding its own lock.
	if ch != nil {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	if closed {
		return errors.New("connection closed")
	}
	return err
}

func (s *controllableSender) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
}

func (s *controllableSender) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *controllableSender) setNextPingError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pingError = err
}

// setPingNotifyCh sets the channel that receives a signal on each Ping call.
// Must be called before the goroutine that calls Ping is started.
func (s *controllableSender) setPingNotifyCh(ch chan<- struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pingNotifyCh = ch
}

// ── blockingSender ────────────────────────────────────────────────────────────

// blockingSender blocks on Ping until its block channel is closed, then
// returns context.Canceled. Used to test mid-Ping context cancellation.
type blockingSender struct {
	started chan struct{}
	once    sync.Once
	block   chan struct{}
}

func (s *blockingSender) Send(_ []byte) error { return nil }
func (s *blockingSender) Ping(ctx context.Context) error {
	s.once.Do(func() { close(s.started) })
	select {
	case <-s.block:
		return ctx.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (s *blockingSender) Close() {}

// ── blockingSendSender ────────────────────────────────────────────────────────

// blockingSendSender blocks on Send until Close is called. Used to verify that
// stop() interrupts an in-flight Send immediately.
type blockingSendSender struct {
	mu      sync.Mutex
	closed  bool
	block   chan struct{} // closed by Close(); unblocks any pending Send
	started chan struct{} // closed on first Send entry
	once    sync.Once
}

func newBlockingSendSender() *blockingSendSender {
	return &blockingSendSender{
		block:   make(chan struct{}),
		started: make(chan struct{}),
	}
}

func (s *blockingSendSender) Send(_ []byte) error {
	s.once.Do(func() { close(s.started) })
	s.mu.Lock()
	block := s.block
	s.mu.Unlock()
	<-block // wait until Close() is called
	return errors.New("connection closed")
}

func (s *blockingSendSender) Ping(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("connection closed")
	}
	return nil
}

func (s *blockingSendSender) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.block)
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

// awaitGoroutineExit waits for done to be closed within 2 seconds.
func awaitGoroutineExit(t *testing.T, done <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("%s goroutine did not exit within 2s", name)
	}
}

// awaitSignal waits for one signal on ch within 2 seconds.
func awaitSignal(t *testing.T, ch <-chan struct{}, desc string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s within 2s", desc)
	}
}

// ── startHeartbeat tests ──────────────────────────────────────────────────────

// TestHeartbeat_DropsClientOnPingFailure verifies that a ping error causes the
// client to be unregistered and the connection closed.
func TestHeartbeat_DropsClientOnPingFailure(t *testing.T) {
	hub := NewHub(&fakeAuthorizer{}, slog.Default(), NopBus{}, "test-inst")
	defer hub.Shutdown()

	snd := &controllableSender{}
	c := newClient("c-hb-fail", "u1", "ws1", snd)
	registerInRunningHub(t, hub, c)

	snd.setNextPingError(errors.New("ping timeout"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		startHeartbeat(ctx, c, hub, slog.Default(), time.Millisecond, 5*time.Millisecond)
	}()

	awaitGoroutineExit(t, done, "heartbeat")
	// heartbeat called c.close() synchronously before returning.
	if !snd.isClosed() {
		t.Fatal("connection should be closed after ping failure")
	}
	eventually(t, func() bool { return !hubHasClient(hub, "c-hb-fail") }, 2*time.Second, "client removed from hub after ping failure")
}

func TestHeartbeat_NilLoggerPingFailureDoesNotPanic(t *testing.T) {
	hub := NewHub(&fakeAuthorizer{}, nil, NopBus{}, "test-inst")
	defer hub.Shutdown()

	snd := &controllableSender{}
	c := newClient("c-hb-nil-logger", "u1", "ws1", snd)
	registerInRunningHub(t, hub, c)
	snd.setNextPingError(errors.New("ping timeout"))

	done := make(chan struct{})
	go func() {
		defer close(done)
		startHeartbeat(context.Background(), c, hub, nil, time.Millisecond, 5*time.Millisecond)
	}()

	awaitGoroutineExit(t, done, "heartbeat")
	if !snd.isClosed() {
		t.Fatal("connection should be closed after ping failure with nil logger")
	}
}

// TestHeartbeat_SuccessfulPingsKeepClientRegistered verifies that successful
// pings do not drop the client. Uses a ping notification channel to observe
// at least one real Ping call before asserting.
func TestHeartbeat_SuccessfulPingsKeepClientRegistered(t *testing.T) {
	hub := NewHub(&fakeAuthorizer{}, slog.Default(), NopBus{}, "test-inst")
	defer hub.Shutdown()

	pingSignal := make(chan struct{}, 1)
	snd := &controllableSender{}
	snd.setPingNotifyCh(pingSignal)
	c := newClient("c-hb-ok", "u1", "ws1", snd)
	registerInRunningHub(t, hub, c)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		startHeartbeat(ctx, c, hub, slog.Default(), time.Millisecond, 5*time.Millisecond)
	}()

	// Wait for at least one real Ping before asserting.
	awaitSignal(t, pingSignal, "first ping")

	cancel()
	awaitGoroutineExit(t, done, "heartbeat")

	// Client must still be registered: a ping succeeded; context cancel is clean shutdown.
	if !hubHasClient(hub, "c-hb-ok") {
		t.Fatal("client should remain registered after successful ping + clean context cancel")
	}
}

// TestHeartbeat_ExitsOnContextCancelWithoutDroppingClient verifies there is no
// goroutine leak and no spurious unregister when the context is cancelled
// before any ping is attempted.
func TestHeartbeat_ExitsOnContextCancelWithoutDroppingClient(t *testing.T) {
	hub := NewHub(&fakeAuthorizer{}, slog.Default(), NopBus{}, "test-inst")
	defer hub.Shutdown()

	snd := &controllableSender{}
	c := newClient("c-hb-cancel", "u1", "ws1", snd)
	registerInRunningHub(t, hub, c)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately before ticker ever fires

	done := make(chan struct{})
	go func() {
		defer close(done)
		startHeartbeat(ctx, c, hub, slog.Default(), time.Hour, 5*time.Second)
	}()

	awaitGoroutineExit(t, done, "heartbeat")

	if !hubHasClient(hub, "c-hb-cancel") {
		t.Fatal("client should remain registered: context was cancelled, not a ping failure")
	}
}

// TestHeartbeat_ContextCancelDuringPingExitsCleanly verifies that when the
// context is cancelled while a Ping is in flight, heartbeat exits without
// logging a spurious "ping failed" warning or calling hub.Unregister.
// This covers rolling-restart and clean-shutdown scenarios.
func TestHeartbeat_ContextCancelDuringPingExitsCleanly(t *testing.T) {
	hub := NewHub(&fakeAuthorizer{}, slog.Default(), NopBus{}, "test-inst")
	defer hub.Shutdown()

	// pingBlock lets the test control when Ping returns.
	pingBlock := make(chan struct{})
	snd := &blockingSender{block: pingBlock, started: make(chan struct{})}
	c := newClient("c-hb-ctxcancel", "u1", "ws1", snd)
	registerInRunningHub(t, hub, c)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		startHeartbeat(ctx, c, hub, slog.Default(), time.Millisecond, 5*time.Second)
	}()

	// Wait until Ping is blocked, then cancel the context.
	awaitSignal(t, snd.started, "ping started")
	cancel()
	close(pingBlock) // unblock Ping so it returns a context error

	awaitGoroutineExit(t, done, "heartbeat")

	// Client must still be registered: heartbeat must treat ctx.Err() != nil
	// as a clean exit, not a peer failure.
	if !hubHasClient(hub, "c-hb-ctxcancel") {
		t.Fatal("client should remain registered when ctx is cancelled mid-ping")
	}
}

// ── startConnectionPumps lifecycle tests ─────────────────────────────────────

// TestConnectionPumps_PingFailureCancelsBoth verifies that a heartbeat ping
// failure exits both the heartbeat and the writePump goroutine. This is the
// key lifecycle coordination test: writePump must not block on an empty outbox
// after heartbeat detects a dead connection.
func TestConnectionPumps_PingFailureCancelsBoth(t *testing.T) {
	hub := NewHub(&fakeAuthorizer{}, slog.Default(), NopBus{}, "test-inst")
	defer hub.Shutdown()

	snd := &controllableSender{}
	c := newClient("c-lc", "u1", "ws1", snd)
	registerInRunningHub(t, hub, c)

	snd.setNextPingError(errors.New("ping timeout"))

	done, stop := startConnectionPumps(
		context.Background(), c, hub, slog.Default(),
		time.Millisecond, 5*time.Millisecond,
	)
	defer stop()

	awaitGoroutineExit(t, done, "connection pumps")
	eventually(t, func() bool { return !hubHasClient(hub, "c-lc") }, 2*time.Second, "client removed from hub after ping failure")
}

// TestConnectionPumps_SendErrorCancelsHeartbeat verifies that a writePump send
// error exits both the writePump and the heartbeat goroutine.
func TestConnectionPumps_SendErrorCancelsHeartbeat(t *testing.T) {
	hub := NewHub(&fakeAuthorizer{}, slog.Default(), NopBus{}, "test-inst")
	defer hub.Shutdown()

	snd := &controllableSender{}
	c := newClient("c-lc-send", "u1", "ws1", snd)
	registerInRunningHub(t, hub, c)

	// Close before starting pumps so the first Send fails.
	snd.Close()

	done, stop := startConnectionPumps(
		context.Background(), c, hub, slog.Default(),
		time.Hour, 5*time.Second, // long interval so heartbeat never fires on its own
	)
	defer stop()

	// Enqueue one message to trigger a Send error.
	c.enqueue([]byte(`{"type":"test"}`))

	awaitGoroutineExit(t, done, "connection pumps")
	eventually(t, func() bool { return !hubHasClient(hub, "c-lc-send") }, 2*time.Second, "client removed from hub after send error")
}

func TestConnectionPumps_NilLoggerSendErrorDoesNotPanic(t *testing.T) {
	hub := NewHub(&fakeAuthorizer{}, nil, NopBus{}, "test-inst")
	defer hub.Shutdown()

	snd := &controllableSender{}
	c := newClient("c-lc-nil-logger", "u1", "ws1", snd)
	registerInRunningHub(t, hub, c)
	snd.Close()

	done, stop := startConnectionPumps(
		context.Background(), c, hub, nil,
		time.Hour, 5*time.Second,
	)
	defer stop()

	c.enqueue([]byte(`{"type":"test"}`))

	awaitGoroutineExit(t, done, "connection pumps")
	eventually(t, func() bool { return !hubHasClient(hub, "c-lc-nil-logger") }, 2*time.Second, "client removed from hub after send error")
}

// TestConnectionPumps_CleanShutdownOnContextCancel verifies that cancelling
// the parent context exits both pumps cleanly. writePump unconditionally defers
// hub.Unregister, so the client is removed from hub state even on clean exit.
func TestConnectionPumps_CleanShutdownOnContextCancel(t *testing.T) {
	hub := NewHub(&fakeAuthorizer{}, slog.Default(), NopBus{}, "test-inst")
	defer hub.Shutdown()

	snd := &controllableSender{}
	c := newClient("c-lc-clean", "u1", "ws1", snd)
	registerInRunningHub(t, hub, c)

	ctx, cancel := context.WithCancel(context.Background())

	done, stop := startConnectionPumps(
		ctx, c, hub, slog.Default(),
		time.Hour, 5*time.Second,
	)
	defer stop()

	cancel()
	awaitGoroutineExit(t, done, "connection pumps")
	// writePump always defers hub.Unregister regardless of exit reason.
	eventually(t, func() bool { return !hubHasClient(hub, "c-lc-clean") }, 2*time.Second, "client removed from hub on clean shutdown")
}

// TestConnectionPumps_StopCancelsBothImmediately verifies that calling stop()
// causes both pumps to exit promptly without waiting for the next heartbeat
// tick. This is a regression test for the caller-controlled lifecycle:
// a read loop that ends normally must signal pumps to stop immediately rather
// than block on an idle outbox or a distant tick.
func TestConnectionPumps_StopCancelsBothImmediately(t *testing.T) {
	hub := NewHub(&fakeAuthorizer{}, slog.Default(), NopBus{}, "test-inst")
	defer hub.Shutdown()

	// Long heartbeat interval: pumps would block until the tick fires (1 hour)
	// if stop() did not cancel the shared context immediately.
	snd := &controllableSender{}
	c := newClient("c-lc-stop", "u1", "ws1", snd)
	registerInRunningHub(t, hub, c)

	done, stop := startConnectionPumps(
		context.Background(), c, hub, slog.Default(),
		time.Hour, 5*time.Second,
	)

	// Simulate read loop ending: call stop() to signal pumps immediately.
	stop()

	// Both pumps must exit without sleeping or waiting for the next tick.
	awaitGoroutineExit(t, done, "connection pumps")
}

// TestConnectionPumps_StopInterruptsBlockedSend verifies that stop() unblocks
// a writePump that is currently inside a Send call. This covers the scenario
// where the read loop ends while the write pump is mid-transmission:
// stop() must call c.close() so Send returns an error immediately rather than
// blocking until the I/O deadline or peer response.
func TestConnectionPumps_StopInterruptsBlockedSend(t *testing.T) {
	hub := NewHub(&fakeAuthorizer{}, slog.Default(), NopBus{}, "test-inst")
	defer hub.Shutdown()

	snd := newBlockingSendSender()
	c := newClient("c-lc-blocked", "u1", "ws1", snd)
	registerInRunningHub(t, hub, c)

	done, stop := startConnectionPumps(
		context.Background(), c, hub, slog.Default(),
		time.Hour, 5*time.Second, // long interval: heartbeat must not interfere
	)

	// Enqueue a message so writePump enters Send (which will block).
	c.enqueue([]byte(`{"type":"test"}`))

	// Wait until Send is blocking so we know writePump is mid-transmission.
	awaitSignal(t, snd.started, "Send started")

	// stop() must interrupt the blocked Send by calling c.close().
	stop()

	// Both pumps must exit promptly — not waiting for the heartbeat tick.
	awaitGoroutineExit(t, done, "connection pumps")
	eventually(t, func() bool { return !hubHasClient(hub, "c-lc-blocked") }, 2*time.Second, "client removed from hub after stop interrupted Send")
}

// ── writePump tests ───────────────────────────────────────────────────────────

// TestWritePump_DropsClientOnSendError verifies that a failed Send triggers
// hub.Unregister so the client is removed from hub state.
func TestWritePump_DropsClientOnSendError(t *testing.T) {
	hub := NewHub(&fakeAuthorizer{}, slog.Default(), NopBus{}, "test-inst")
	defer hub.Shutdown()

	snd := &controllableSender{}
	c := newClient("c-wp-fail", "u1", "ws1", snd)
	registerInRunningHub(t, hub, c)

	snd.Close() // next Send returns an error

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		c.writePump(ctx, hub, slog.Default())
	}()

	if !c.enqueue([]byte(`{"type":"test"}`)) {
		t.Fatal("enqueue should succeed (outbox is empty)")
	}

	awaitGoroutineExit(t, done, "writePump")
	eventually(t, func() bool { return !hubHasClient(hub, "c-wp-fail") }, 2*time.Second, "client removed from hub after send error")
}

func TestWritePump_NilLoggerSendErrorDoesNotPanic(t *testing.T) {
	hub := NewHub(&fakeAuthorizer{}, nil, NopBus{}, "test-inst")
	defer hub.Shutdown()

	snd := &controllableSender{}
	c := newClient("c-wp-nil-logger", "u1", "ws1", snd)
	registerInRunningHub(t, hub, c)
	snd.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		c.writePump(context.Background(), hub, nil)
	}()

	if !c.enqueue([]byte(`{"type":"test"}`)) {
		t.Fatal("enqueue should succeed")
	}

	awaitGoroutineExit(t, done, "writePump")
	eventually(t, func() bool { return !hubHasClient(hub, "c-wp-nil-logger") }, 2*time.Second, "client removed from hub after send error")
}

// TestWritePump_ExitsOnContextCancel verifies no goroutine leak when the
// context is cancelled while the pump is idle.
func TestWritePump_ExitsOnContextCancel(t *testing.T) {
	hub := NewHub(&fakeAuthorizer{}, slog.Default(), NopBus{}, "test-inst")
	defer hub.Shutdown()

	snd := &controllableSender{}
	c := newClient("c-wp-cancel", "u1", "ws1", snd)
	registerInRunningHub(t, hub, c)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		c.writePump(ctx, hub, slog.Default())
	}()

	cancel()
	awaitGoroutineExit(t, done, "writePump")
}

// TestWritePump_ForwardsEnqueuedMessages verifies that messages placed on the
// outbox are forwarded to the sender in order. Uses a buffered channel to
// collect sent messages without sleeping.
func TestWritePump_ForwardsEnqueuedMessages(t *testing.T) {
	hub := NewHub(&fakeAuthorizer{}, slog.Default(), NopBus{}, "test-inst")
	defer hub.Shutdown()

	msgs := []string{`{"a":1}`, `{"b":2}`, `{"c":3}`}
	sentCh := make(chan string, len(msgs))

	trackingSender := &trackSender{
		onSend: func(data []byte) error {
			sentCh <- string(data)
			return nil
		},
	}

	c := newClient("c-wp-fwd", "u1", "ws1", trackingSender)
	registerInRunningHub(t, hub, c)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		c.writePump(ctx, hub, slog.Default())
	}()

	for _, m := range msgs {
		c.enqueue([]byte(m))
	}

	// Collect all expected messages via channel — no sleep needed.
	received := make([]string, 0, len(msgs))
	timeout := time.After(2 * time.Second)
	for len(received) < len(msgs) {
		select {
		case m := <-sentCh:
			received = append(received, m)
		case <-timeout:
			t.Fatalf("only received %d/%d messages within 2s", len(received), len(msgs))
		}
	}

	cancel()
	awaitGoroutineExit(t, done, "writePump")

	for i, want := range msgs {
		if received[i] != want {
			t.Errorf("message[%d]: got %q, want %q", i, received[i], want)
		}
	}
}

// ── sender concurrency tests ──────────────────────────────────────────────────

// TestSender_ConcurrentSendPingClose verifies that concurrent Send, Ping, and
// Close calls on controllableSender are race-safe. In production, writePump
// calls Send, startHeartbeat calls Ping, and dropClient may call Close — all
// potentially overlapping at teardown. Run with -race.
func TestSender_ConcurrentSendPingClose(t *testing.T) {
	snd := &controllableSender{}

	var wg sync.WaitGroup
	for range 20 {
		wg.Add(3)
		go func() {
			defer wg.Done()
			_ = snd.Send([]byte("data"))
		}()
		go func() {
			defer wg.Done()
			_ = snd.Ping(context.Background())
		}()
		go func() {
			defer wg.Done()
			snd.Close()
		}()
	}
	wg.Wait()
}

// trackSender records calls to Send; Ping and Close are no-ops.
type trackSender struct {
	onSend func([]byte) error
}

func (s *trackSender) Send(data []byte) error       { return s.onSend(data) }
func (s *trackSender) Ping(_ context.Context) error { return nil }
func (s *trackSender) Close()                       {}
