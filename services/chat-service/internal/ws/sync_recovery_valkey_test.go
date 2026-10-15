package ws

// Issue #947 across two replicas and a real Valkey.
//
// Two hubs, each with its own ValkeyBus connection, stand for two chat-service
// pods behind one Valkey: the publisher is pod A (where the HTTP request that
// persisted the message would have landed) and the recipient's connection lives
// on pod B. The bus and both hubs are real. The authorizer and the client
// connections are this package's test doubles — the transient authorization
// failure is injected — and no HTTP request, PostgreSQL or browser is involved.
//
// Owned by `Tests / Go Integration` (scripts/ci/go-integration-test.sh), whose
// job provisions Valkey. Skipped only when CHAT_TEST_VALKEY_URL is unset, which
// that script refuses; an unreachable Valkey fails it.
//
//	CHAT_TEST_VALKEY_URL=redis://127.0.0.1:6379 \
//	  go test ./internal/ws -race -run '^TestSyncRecoveryValkey_' -count=1
//
// Each case uses its own workspace, and therefore its own bus channel, so
// concurrent runs against one Valkey cannot see each other's events.

import (
	"context"
	"errors"
	"os"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

// valkeyCrossPodWait bounds how long an event may take to cross the bus.
const valkeyCrossPodWait = 3 * time.Second

// countingBus records what a hub published, so a test can prove the sync
// signal never travels between replicas.
type countingBus struct {
	*ValkeyBus
	published atomic.Int64
}

func (b *countingBus) Publish(ctx context.Context, evt Event) error {
	b.published.Add(1)
	if evt.Type == SyncRequiredType {
		return errors.New("sync_required must never be published to the bus")
	}
	return b.ValkeyBus.Publish(ctx, evt)
}

type valkeyPods struct {
	workspace string
	channel   string
	a, b      *Hub
	authB     *fakeAuthorizer
	busA      *countingBus
	busB      *countingBus
}

func newValkeyPods(t *testing.T) *valkeyPods {
	t.Helper()
	url := os.Getenv("CHAT_TEST_VALKEY_URL")
	if url == "" {
		t.Skip("CHAT_TEST_VALKEY_URL is not set")
	}
	pods := &valkeyPods{
		workspace: uuid.NewString(),
		channel:   uuid.NewString(),
		authB:     &fakeAuthorizer{},
	}
	pods.busA = connectCountingBus(t, url, "pod-a")
	pods.busB = connectCountingBus(t, url, "pod-b")
	pods.a = NewHub(&fakeAuthorizer{}, newTestLogger(), pods.busA, "pod-a")
	pods.b = NewHub(pods.authB, newTestLogger(), pods.busB, "pod-b")
	t.Cleanup(pods.a.Shutdown)
	t.Cleanup(pods.b.Shutdown)
	return pods
}

func connectCountingBus(t *testing.T, url, instance string) *countingBus {
	t.Helper()
	bus, err := NewValkeyBus(url, instance, newTestLogger())
	if err != nil {
		t.Fatalf("connect %s to Valkey: %v", instance, err)
	}
	return &countingBus{ValkeyBus: bus}
}

// connectOnB subscribes a fresh connection on pod B, then proves the bus path
// is live by delivering one message published on pod A. Pattern subscriptions
// are asynchronous, and that first delivery is the only honest signal that
// both pods are listening.
func (p *valkeyPods) connectOnB(t *testing.T, clientID, userID string) *Client {
	t.Helper()
	c := newClient(clientID, userID, p.workspace, &fakeSender{})
	registerInRunningHub(t, p.b, c)
	p.authB.setAccess(userID, p.workspace, TargetTypeChannel, p.channel, true)
	if err := p.b.Subscribe(context.Background(), c, TargetTypeChannel, p.channel); err != nil {
		t.Fatalf("subscribe %s on pod B: %v", clientID, err)
	}
	return c
}

func (p *valkeyPods) publishOnA() string {
	messageID := uuid.NewString()
	payload := testPayload()
	payload.ID = messageID
	payload.WorkspaceID = p.workspace
	payload.ChannelID = p.channel
	p.a.PublishMessageCreated(context.Background(), p.workspace, TargetTypeChannel, p.channel, payload)
	return messageID
}

// awaitCrossPodDelivery publishes on A until c receives one message, tolerating
// the window in which pod B's pattern subscription is still being set up.
func (p *valkeyPods) awaitCrossPodDelivery(t *testing.T, c *Client) {
	t.Helper()
	deadline := time.Now().Add(valkeyCrossPodWait)
	for time.Now().Before(deadline) {
		p.publishOnA()
		select {
		case <-c.outbox:
			drainOutboxFrames(t, c)
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
	t.Fatalf("no message crossed from pod A to %s through Valkey within %s", c.id, valkeyCrossPodWait)
}

func TestSyncRecoveryValkey_CrossPodDeliveryAndRecovery(t *testing.T) {
	p := newValkeyPods(t)
	affected := p.connectOnB(t, "affected-on-b", "user-1")
	bystander := p.connectOnB(t, "bystander-on-b", "user-2")
	p.awaitCrossPodDelivery(t, affected)
	drainOutboxFrames(t, bystander)

	// The database blips for one recipient on pod B.
	p.authB.setErr("user-1", p.workspace, TargetTypeChannel, p.channel, errors.New("pool exhausted"))
	p.publishOnA()

	awaitFrame(t, bystander, string(EventTypeMessageCreated), valkeyCrossPodWait)
	eventually(t, func() bool { return pendingGapCount(p.b) == 1 }, valkeyCrossPodWait,
		"pod B never recorded the gap for its own connection")
	if n := pendingGapCount(p.a); n != 0 {
		t.Fatalf("the gap belongs to the pod that owns the connection; pod A holds %d", n)
	}
	if frames := drainOutboxFrames(t, affected); len(frames) != 0 {
		t.Fatalf("fail-secure: nothing may reach the affected connection, got %d frames", len(frames))
	}

	// Access is provable again and nobody posts: recovery alone must close it.
	p.authB.setErr("user-1", p.workspace, TargetTypeChannel, p.channel, nil)
	frame, _ := awaitFrame(t, affected, syncRequiredFrameType, syncRecoveryWait)
	assertSyncRequiredFor(t, frame, TargetTypeChannel, p.channel)
	expectNoFrame(t, bystander, syncRequiredFrameType, time.Second)
	if pendingGapCount(p.b) != 0 {
		t.Fatal("a signalled gap must be cleared")
	}
	if p.busB.published.Load() != 0 {
		t.Fatalf("pod B must not publish anything to the bus, published %d", p.busB.published.Load())
	}
}

func TestSyncRecoveryValkey_UnsubscribeAndReconnectDuringRecovery(t *testing.T) {
	p := newValkeyPods(t)
	leaving := p.connectOnB(t, "leaving-on-b", "user-1")
	reconnecting := p.connectOnB(t, "reconnecting-on-b", "user-2")
	p.awaitCrossPodDelivery(t, leaving)
	drainOutboxFrames(t, reconnecting)

	for _, user := range []string{"user-1", "user-2"} {
		p.authB.setErr(user, p.workspace, TargetTypeChannel, p.channel, errors.New("pool exhausted"))
	}
	p.publishOnA()
	eventually(t, func() bool { return pendingGapCount(p.b) == 2 }, valkeyCrossPodWait,
		"pod B never recorded both gaps")

	key := targetKey{workspaceID: p.workspace, targetType: TargetTypeChannel, targetID: p.channel}.String()
	if err := p.b.RevokeSubscription(context.Background(), leaving, key); err != nil {
		t.Fatalf("unsubscribe: %v", err)
	}
	p.b.Unregister(reconnecting)
	eventually(t, func() bool { return !hubHasClient(p.b, reconnecting.id) }, time.Second, "unregister")
	for _, user := range []string{"user-1", "user-2"} {
		p.authB.setErr(user, p.workspace, TargetTypeChannel, p.channel, nil)
	}
	replacement := p.connectOnB(t, "replacement-on-b", "user-2")

	expectNoFrame(t, leaving, syncRequiredFrameType, syncQuietWindow)
	expectNoFrame(t, replacement, syncRequiredFrameType, time.Second)
	if n := pendingGapCount(p.b); n != 0 {
		t.Fatalf("released and replaced connections must not keep gaps, got %d", n)
	}
	// The new connection is an ordinary subscriber of what pod A publishes.
	p.publishOnA()
	awaitFrame(t, replacement, string(EventTypeMessageCreated), valkeyCrossPodWait)
}

// Shutting both pods down releases everything they started: the broadcast
// workers, the recovery worker and the bus subscriptions.
func TestSyncRecoveryValkey_ShutdownReleasesGoroutines(t *testing.T) {
	if os.Getenv("CHAT_TEST_VALKEY_URL") == "" {
		t.Skip("CHAT_TEST_VALKEY_URL is not set")
	}
	before := runtime.NumGoroutine()
	t.Run("pods", func(t *testing.T) {
		p := newValkeyPods(t)
		c := p.connectOnB(t, "c-on-b", "user-1")
		p.awaitCrossPodDelivery(t, c)
		p.authB.setErr("user-1", p.workspace, TargetTypeChannel, p.channel, errors.New("pool exhausted"))
		p.publishOnA()
		eventually(t, func() bool { return pendingGapCount(p.b) == 1 }, valkeyCrossPodWait, "gap")
	})
	eventually(t, func() bool { return runtime.NumGoroutine() <= before+2 }, 5*time.Second,
		"goroutines started by the two pods outlived their shutdown")
}
