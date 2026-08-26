package worker

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/emailcrypto"
	"github.com/nicrepository/nchat/services/notification-service/internal/storage"
)

// blockingSender lets a test hold a delivery open across a shutdown, which is
// the only way to observe what happens to a message that has been sent but not
// yet finalised. Synchronised by channels, so nothing here depends on timing.
type blockingSender struct {
	entered chan struct{}
	release chan struct{}
	err     error
	sent    []Message
}

func newBlockingSender() *blockingSender {
	return &blockingSender{entered: make(chan struct{}, 1), release: make(chan struct{})}
}

func (b *blockingSender) Send(_ context.Context, msg Message) error {
	b.entered <- struct{}{}
	<-b.release
	if b.err != nil {
		return b.err
	}
	b.sent = append(b.sent, msg)
	return nil
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
}

func outboxRowFor(t *testing.T, decryptor *emailcrypto.Encryptor, id string) storage.OutboxRow {
	t.Helper()
	plaintext := emailcrypto.Plaintext{
		Kind:       "password_reset",
		Token:      "token-" + id,
		ActionPath: "/auth/password/reset",
		ToEmail:    "user@example.com",
		ExpiresAt:  time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC),
	}
	return storage.OutboxRow{
		ID:               id,
		Kind:             plaintext.Kind,
		ToEmail:          plaintext.ToEmail,
		EncryptedPayload: encryptPayload(t, decryptor, plaintext),
	}
}

// A worker with nothing to do must stop as soon as it is asked to.
func TestWorkerStopsPromptlyWhenIdle(t *testing.T) {
	worker := New(testWorkerConfig(), &fakeOutboxStore{}, newTestEncryptor(t), &FakeSender{}, quietLogger())
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		worker.Start(ctx)
	}()

	cancel()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("idle worker did not stop when its context was cancelled")
	}
}

// The regression this whole change exists for: a message sent but not yet
// recorded must be finalised, not abandoned to its lease.
func TestWorkerFinalisesInFlightDeliveryDuringShutdown(t *testing.T) {
	decryptor := newTestEncryptor(t)
	store := &fakeOutboxStore{rows: []storage.OutboxRow{outboxRowFor(t, decryptor, "row-1")}}
	sender := newBlockingSender()
	worker := New(testWorkerConfig(), store, decryptor, sender, quietLogger())

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		worker.Start(ctx)
	}()

	// Wait until the delivery is genuinely in flight, then ask the worker to stop.
	select {
	case <-sender.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("sender was never called")
	}
	cancel()

	// The worker must still be inside the pass, not gone.
	select {
	case <-stopped:
		t.Fatal("worker abandoned a delivery that was already in flight")
	case <-time.After(50 * time.Millisecond):
	}

	close(sender.release)
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not stop after the in-flight delivery completed")
	}

	if len(store.successCalls) != 1 || store.successCalls[0] != "row-1" {
		t.Fatalf("expected the in-flight row to be finalised, got %v", store.successCalls)
	}
}

// A send that fails during shutdown must still record the failure, so the retry
// schedule is the one the outbox decided rather than a lease expiry.
func TestWorkerFinalisesFailureDuringShutdown(t *testing.T) {
	decryptor := newTestEncryptor(t)
	store := &fakeOutboxStore{rows: []storage.OutboxRow{outboxRowFor(t, decryptor, "row-1")}}
	sender := newBlockingSender()
	sender.err = errors.New("smtp unavailable")
	worker := New(testWorkerConfig(), store, decryptor, sender, quietLogger())

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		worker.Start(ctx)
	}()

	select {
	case <-sender.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("sender was never called")
	}
	cancel()
	close(sender.release)

	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not stop")
	}
	if len(store.failureCalls) != 1 {
		t.Fatalf("expected one recorded failure, got %d", len(store.failureCalls))
	}
}

// Once stopped, the worker must not claim anything else.
func TestWorkerClaimsNothingAfterShutdown(t *testing.T) {
	decryptor := newTestEncryptor(t)
	store := &fakeOutboxStore{rows: []storage.OutboxRow{outboxRowFor(t, decryptor, "row-1")}}
	sender := newBlockingSender()
	worker := New(testWorkerConfig(), store, decryptor, sender, quietLogger())

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		worker.Start(ctx)
	}()

	select {
	case <-sender.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("sender was never called")
	}
	cancel()
	close(sender.release)
	<-stopped

	claimsAtStop := len(store.claimCalls)
	// Well past the 1s poll interval: a worker that was still running would
	// have claimed again by now.
	time.Sleep(1500 * time.Millisecond)
	if len(store.claimCalls) != claimsAtStop {
		t.Fatalf("worker claimed more work after shutdown: %d -> %d", claimsAtStop, len(store.claimCalls))
	}
}

// The lease has to outlive the work it protects. This is the invariant, checked
// against the configuration the worker actually runs with.
func TestLeaseOutlivesOneMessagesProcessing(t *testing.T) {
	worker := New(testWorkerConfig(), &fakeOutboxStore{}, newTestEncryptor(t), &FakeSender{}, quietLogger())
	if !worker.leaseCoversProcessing() {
		t.Fatalf("lease %ds does not cover a pass of %s",
			smtpWorkerDeadlineSeconds, worker.passTimeout())
	}
	// A send allowed to run longer than the lease must be rejected outright,
	// rather than silently handing rows to a second worker mid-send.
	cfg := testWorkerConfig()
	cfg.SMTPTimeoutSeconds = smtpWorkerDeadlineSeconds
	overrun := New(cfg, &fakeOutboxStore{}, newTestEncryptor(t), &FakeSender{}, quietLogger())
	if overrun.leaseCoversProcessing() {
		t.Fatal("a send timeout at the lease length was accepted")
	}
}

// A worker that cannot protect its work must not start it.
func TestWorkerRefusesToRunWhenTheLeaseIsTooShort(t *testing.T) {
	cfg := testWorkerConfig()
	cfg.SMTPTimeoutSeconds = smtpWorkerDeadlineSeconds * 2
	worker := New(cfg, &fakeOutboxStore{}, newTestEncryptor(t), &FakeSender{}, quietLogger())

	stopped := make(chan struct{})
	go func() { defer close(stopped); worker.Start(context.Background()) }()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("worker started despite a lease shorter than its own processing budget")
	}
}

// One row per claim: ten rows leased together under one 30s lease, then sent
// sequentially at up to 10s each, outlived the lease protecting them.
func TestWorkerClaimsOneRowAtATime(t *testing.T) {
	decryptor := newTestEncryptor(t)
	store := &fakeOutboxStore{rows: []storage.OutboxRow{outboxRowFor(t, decryptor, "row-1")}}
	worker := New(testWorkerConfig(), store, decryptor, &FakeSender{}, quietLogger())

	worker.pollOnce()

	if len(store.claimCalls) != 1 {
		t.Fatalf("expected one claim, got %d", len(store.claimCalls))
	}
	if store.claimCalls[0].batchSize != 1 {
		t.Fatalf("claimed a batch of %d; a lease must cover the work it protects",
			store.claimCalls[0].batchSize)
	}
	if store.claimCalls[0].deadlineSeconds != smtpWorkerDeadlineSeconds {
		t.Fatalf("unexpected lease %ds", store.claimCalls[0].deadlineSeconds)
	}
}

// A send that consumes its whole timeout must still leave the worker able to
// record the outcome. Before the send had its own deadline, the pass context was
// already cancelled by then and the row stayed unfinalised — so its lease
// expired and the message was delivered twice.
func TestFinalisationSurvivesASendThatUsesItsWholeTimeout(t *testing.T) {
	cfg := testWorkerConfig()
	cfg.SMTPTimeoutSeconds = 1
	decryptor := newTestEncryptor(t)
	store := &fakeOutboxStore{rows: []storage.OutboxRow{outboxRowFor(t, decryptor, "row-1")}}
	sender := &deadlineSender{}
	worker := New(cfg, store, decryptor, sender, quietLogger())

	worker.pollOnce()

	if !sender.sawDeadline {
		t.Fatal("the send ran without a deadline of its own")
	}
	if len(store.failureCalls) != 1 {
		t.Fatalf("expected the timed-out send to be recorded, got %d failures", len(store.failureCalls))
	}
}

// deadlineSender blocks until its context expires, the way a hung SMTP server
// looks from here, and reports whether it was given a deadline at all.
type deadlineSender struct{ sawDeadline bool }

func (d *deadlineSender) Send(ctx context.Context, _ Message) error {
	_, d.sawDeadline = ctx.Deadline()
	<-ctx.Done()
	return ctx.Err()
}

// While a row's lease is held, a second worker must not be able to take it.
// This is the property Blue/Green depends on: both slots run this worker.
func TestSecondWorkerDoesNotClaimALeasedRow(t *testing.T) {
	decryptor := newTestEncryptor(t)
	shared := &leaseAwareStore{row: outboxRowFor(t, decryptor, "row-1")}
	blue := New(testWorkerConfig(), shared, decryptor, newBlockingSender(), quietLogger())
	green := New(testWorkerConfig(), shared, decryptor, &FakeSender{}, quietLogger())

	sender := blue.sender.(*blockingSender)
	blueDone := make(chan struct{})
	go func() { defer close(blueDone); blue.pollOnce() }()

	select {
	case <-sender.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("blue never started sending")
	}

	// Green polls while blue holds the lease.
	green.pollOnce()
	if shared.claimedBy > 1 {
		t.Fatal("a second worker claimed a row that was already leased")
	}

	close(sender.release)
	<-blueDone
	if shared.finalised != 1 {
		t.Fatalf("expected the row to be finalised once, got %d", shared.finalised)
	}
}

// leaseAwareStore models the lease: a claimed row is not claimable again until
// it is released.
type leaseAwareStore struct {
	mu        sync.Mutex
	row       storage.OutboxRow
	leased    bool
	done      bool
	claimedBy int
	finalised int
}

func (l *leaseAwareStore) ClaimBatch(ctx context.Context, _, _, _ int) ([]storage.OutboxRow, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.leased || l.done {
		return nil, nil
	}
	l.leased = true
	l.claimedBy++
	return []storage.OutboxRow{l.row}, nil
}

func (l *leaseAwareStore) FinaliseSuccess(ctx context.Context, _ string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.finalised++
	l.done = true
	l.leased = false
	return nil
}

func (l *leaseAwareStore) FinaliseFailure(ctx context.Context, _ string, _ int, _ string, _ int, _ int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.leased = false
	return nil
}

// A worker that dies before finalising must leave the row recoverable: the lease
// lapses and another worker picks it up. This is the retry path, unchanged.
func TestRowIsReclaimableAfterAFailedAttempt(t *testing.T) {
	decryptor := newTestEncryptor(t)
	shared := &leaseAwareStore{row: outboxRowFor(t, decryptor, "row-1")}
	sender := &FakeSender{ErrToReturn: errors.New("smtp down")}
	first := New(testWorkerConfig(), shared, decryptor, sender, quietLogger())

	first.pollOnce()
	if shared.finalised != 0 {
		t.Fatal("a failed send was recorded as delivered")
	}
	// The lease was released on failure, so a second worker can retry.
	second := New(testWorkerConfig(), shared, decryptor, &FakeSender{}, quietLogger())
	second.pollOnce()
	if shared.finalised != 1 {
		t.Fatalf("the row was not recoverable after a failure: finalised=%d", shared.finalised)
	}
}

// A backlog must drain within one tick, not one message per polling interval.
// Claiming one row per tick at the default ten-second poll delivered six
// messages a minute however many were queued.
func TestBacklogDrainsWithoutWaitingForEachTick(t *testing.T) {
	decryptor := newTestEncryptor(t)
	rows := make([]storage.OutboxRow, 0, 5)
	for i := 0; i < 5; i++ {
		rows = append(rows, outboxRowFor(t, decryptor, "row-"+string(rune('1'+i))))
	}
	store := &queuedOutboxStore{pending: rows}
	sender := &FakeSender{}
	worker := New(testWorkerConfig(), store, decryptor, sender, quietLogger())

	start := time.Now()
	worker.drainQueue(context.Background())
	elapsed := time.Since(start)

	if len(store.successCalls) != 5 {
		t.Fatalf("drained %d of 5 messages in one pass", len(store.successCalls))
	}
	// The poll interval is 1s in the test config; five ticks would be 5s.
	if elapsed > 2*time.Second {
		t.Fatalf("draining five messages took %s; it waited for ticks", elapsed)
	}
}

// An empty queue must go back to waiting, not spin on the database.
func TestEmptyQueueDoesNotBusyLoop(t *testing.T) {
	store := &queuedOutboxStore{}
	worker := New(testWorkerConfig(), store, newTestEncryptor(t), &FakeSender{}, quietLogger())

	worker.drainQueue(context.Background())

	if store.claims != 1 {
		t.Fatalf("an empty queue produced %d claims; it should stop after one", store.claims)
	}
}

// A claim error must back off to the ticker rather than retry immediately.
func TestClaimErrorDoesNotBusyLoop(t *testing.T) {
	store := &queuedOutboxStore{claimErr: errors.New("database unavailable")}
	worker := New(testWorkerConfig(), store, newTestEncryptor(t), &FakeSender{}, quietLogger())

	worker.drainQueue(context.Background())

	if store.claims != 1 {
		t.Fatalf("a claim error produced %d claims; it should back off after one", store.claims)
	}
}

// Shutdown between items stops the drain: the message in hand finishes, the
// next one is never claimed.
func TestShutdownDuringBacklogStopsFurtherClaims(t *testing.T) {
	decryptor := newTestEncryptor(t)
	rows := []storage.OutboxRow{
		outboxRowFor(t, decryptor, "row-1"),
		outboxRowFor(t, decryptor, "row-2"),
		outboxRowFor(t, decryptor, "row-3"),
	}
	store := &queuedOutboxStore{pending: rows}
	ctx, cancel := context.WithCancel(context.Background())
	// Cancel as soon as the first message has been sent.
	store.afterSuccess = cancel
	worker := New(testWorkerConfig(), store, decryptor, &FakeSender{}, quietLogger())

	worker.drainQueue(ctx)

	if len(store.successCalls) != 1 {
		t.Fatalf("delivered %d messages after shutdown was requested, want 1", len(store.successCalls))
	}
	if store.claims != 1 {
		t.Fatalf("claimed %d times after shutdown was requested, want 1", store.claims)
	}
}

// queuedOutboxStore hands out one row per claim, the way the real store does
// with a batch size of one.
type queuedOutboxStore struct {
	mu           sync.Mutex
	pending      []storage.OutboxRow
	claims       int
	claimErr     error
	successCalls []string
	afterSuccess func()
}

func (q *queuedOutboxStore) ClaimBatch(ctx context.Context, _, _, batchSize int) ([]storage.OutboxRow, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.claims++
	if q.claimErr != nil {
		return nil, q.claimErr
	}
	if len(q.pending) == 0 {
		return nil, nil
	}
	take := q.pending[:1]
	q.pending = q.pending[1:]
	_ = batchSize
	return take, nil
}

func (q *queuedOutboxStore) FinaliseSuccess(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	q.mu.Lock()
	q.successCalls = append(q.successCalls, id)
	after := q.afterSuccess
	q.mu.Unlock()
	if after != nil {
		after()
	}
	return nil
}

func (q *queuedOutboxStore) FinaliseFailure(ctx context.Context, _ string, _ int, _ string, _ int, _ int) error {
	return ctx.Err()
}
