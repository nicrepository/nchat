package ws

// Issue #947: a broadcast skipped because the authorization re-check failed
// operationally must not leave the subscriber permanently behind the database.
//
// These tests drive the hub only through what a connection observes — its
// outbox — and through the authorizer double. Nothing here reaches into the
// recovery bookkeeping, so they describe the contract rather than the
// implementation: a missed delivery is followed, once access can be proven
// again, by exactly one content-free conversation.sync_required for that
// target and that connection, and by nothing at all when access cannot be
// proven.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

const syncRequiredFrameType = "conversation.sync_required"

// syncRecoveryWait bounds how long a test waits for the recovery signal. It is
// a ceiling for a background pass, not a sleep: every wait below returns as soon
// as the frame arrives.
const syncRecoveryWait = 5 * time.Second

// syncQuietWindow is how long a test watches for a frame that must not come.
// It covers more than one recovery pass, so "nothing arrived" means the pass
// ran and decided not to signal rather than that it had not run yet.
const syncQuietWindow = 2 * time.Second

type outboxFrame struct {
	raw    []byte
	fields map[string]any
}

func (f outboxFrame) frameType() string {
	value, _ := f.fields["type"].(string)
	return value
}

func decodeOutboxFrame(t *testing.T, data []byte) outboxFrame {
	t.Helper()
	fields := map[string]any{}
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatalf("outbox frame is not JSON: %v", err)
	}
	return outboxFrame{raw: data, fields: fields}
}

// drainOutbox returns everything queued for c right now.
func drainOutboxFrames(t *testing.T, c *Client) []outboxFrame {
	t.Helper()
	var frames []outboxFrame
	for {
		select {
		case data := <-c.outbox:
			frames = append(frames, decodeOutboxFrame(t, data))
		default:
			return frames
		}
	}
}

// awaitFrame consumes c's outbox until a frame of frameType arrives. Frames of
// other types are returned alongside it so a caller can assert on them too.
func awaitFrame(t *testing.T, c *Client, frameType string, within time.Duration) (outboxFrame, []outboxFrame) {
	t.Helper()
	deadline := time.NewTimer(within)
	defer deadline.Stop()
	var others []outboxFrame
	for {
		select {
		case data := <-c.outbox:
			frame := decodeOutboxFrame(t, data)
			if frame.frameType() == frameType {
				return frame, others
			}
			others = append(others, frame)
		case <-deadline.C:
			t.Fatalf("client %s received no %s within %s (other frames: %d)", c.id, frameType, within, len(others))
			return outboxFrame{}, nil
		}
	}
}

// expectNoFrame fails if a frame of frameType reaches c within the window.
func expectNoFrame(t *testing.T, c *Client, frameType string, within time.Duration) {
	t.Helper()
	deadline := time.NewTimer(within)
	defer deadline.Stop()
	for {
		select {
		case data := <-c.outbox:
			if frame := decodeOutboxFrame(t, data); frame.frameType() == frameType {
				t.Fatalf("client %s received an unexpected %s: %s", c.id, frameType, frame.raw)
			}
		case <-deadline.C:
			return
		}
	}
}

func countFrames(frames []outboxFrame, frameType string) int {
	n := 0
	for _, frame := range frames {
		if frame.frameType() == frameType {
			n++
		}
	}
	return n
}

// assertSyncRequiredFor checks the signal names exactly one target and carries
// nothing a subscriber could read content from.
func assertSyncRequiredFor(t *testing.T, frame outboxFrame, targetType TargetType, targetID string) {
	t.Helper()
	if frame.fields["target_type"] != string(targetType) || frame.fields["target_id"] != targetID {
		t.Fatalf("sync signal names %v/%v, want %s/%s", frame.fields["target_type"], frame.fields["target_id"], targetType, targetID)
	}
	for field := range frame.fields {
		switch field {
		case "type", "schema_version", "target_type", "target_id":
		default:
			t.Fatalf("sync signal carries %q; it must be route-only: %s", field, frame.raw)
		}
	}
}

func subscribedRunningClient(t *testing.T, h *Hub, auth *fakeAuthorizer, clientID, userID string, targets ...subscriptionTarget) *Client {
	t.Helper()
	c := newClient(clientID, userID, "ws-1", &fakeSender{})
	registerInRunningHub(t, h, c)
	for _, target := range targets {
		auth.setAccess(userID, "ws-1", target.targetType, target.targetID, true)
		if err := h.Subscribe(context.Background(), c, target.targetType, target.targetID); err != nil {
			t.Fatalf("subscribe %s: %v", target.targetID, err)
		}
	}
	return c
}

func broadcastMessage(h *Hub, tt TargetType, targetID, messageID string) {
	evt, data := makeEvent("ws-1", tt, targetID, messageID)
	h.handleBroadcast(broadcastReq{event: evt, data: data})
}

var channelOne = subscriptionTarget{targetType: TargetTypeChannel, targetID: "ch-1"}

// B03/B05: the defect itself. The skipped delivery used to be the end of the
// story — nothing told the client it had missed a message, and nothing ever
// would unless somebody else happened to post again.
func TestHub_TransientAuthErrorSignalsSyncOnceAccessIsProvenAgain(t *testing.T) {
	auth := &fakeAuthorizer{}
	h := newRunningTestHub(t, auth)
	c := subscribedRunningClient(t, h, auth, "c1", "user-1", channelOne)

	auth.setErr("user-1", "ws-1", TargetTypeChannel, "ch-1", errors.New("db unavailable"))
	broadcastMessage(h, TargetTypeChannel, "ch-1", "msg-1")
	if frames := drainOutboxFrames(t, c); len(frames) != 0 {
		t.Fatalf("nothing may be delivered while access is unproven, got %d frames", len(frames))
	}

	// Access becomes provable again, and nobody posts anything else.
	auth.setErr("user-1", "ws-1", TargetTypeChannel, "ch-1", nil)

	frame, others := awaitFrame(t, c, syncRequiredFrameType, syncRecoveryWait)
	assertSyncRequiredFor(t, frame, TargetTypeChannel, "ch-1")
	if n := countFrames(others, string(EventTypeMessageCreated)); n != 0 {
		t.Fatalf("the skipped payload must not be replayed, got %d message.created", n)
	}
}

// B04: a timeout is an operational failure, not a denial.
func TestHub_AuthTimeoutIsRecoveredLikeAnyOperationalError(t *testing.T) {
	auth := &fakeAuthorizer{}
	h := newRunningTestHub(t, auth)
	c := subscribedRunningClient(t, h, auth, "c1", "user-1", channelOne)

	auth.setErr("user-1", "ws-1", TargetTypeChannel, "ch-1", fmt.Errorf("query: %w", context.DeadlineExceeded))
	broadcastMessage(h, TargetTypeChannel, "ch-1", "msg-1")
	if frames := drainOutboxFrames(t, c); len(frames) != 0 {
		t.Fatalf("a timed-out check must not deliver, got %d frames", len(frames))
	}
	key := targetKey{workspaceID: "ws-1", targetType: TargetTypeChannel, targetID: "ch-1"}.String()
	if !hubHasSubscription(h, key, "c1") {
		t.Fatal("a timeout must not revoke the subscription")
	}

	auth.setErr("user-1", "ws-1", TargetTypeChannel, "ch-1", nil)
	frame, _ := awaitFrame(t, c, syncRequiredFrameType, syncRecoveryWait)
	assertSyncRequiredFor(t, frame, TargetTypeChannel, "ch-1")
}

// B05: when traffic resumes, the next authorized delivery is also the moment
// the client learns it has a gap — it must not wait for a background pass.
func TestHub_NextAuthorizedBroadcastAlsoSignalsTheEarlierGap(t *testing.T) {
	auth := &fakeAuthorizer{}
	h := newRunningTestHub(t, auth)
	c := subscribedRunningClient(t, h, auth, "c1", "user-1", channelOne)

	auth.setErr("user-1", "ws-1", TargetTypeChannel, "ch-1", errors.New("db unavailable"))
	broadcastMessage(h, TargetTypeChannel, "ch-1", "msg-1")
	auth.setErr("user-1", "ws-1", TargetTypeChannel, "ch-1", nil)
	broadcastMessage(h, TargetTypeChannel, "ch-1", "msg-2")

	frames := drainOutboxFrames(t, c)
	if n := countFrames(frames, string(EventTypeMessageCreated)); n != 1 {
		t.Fatalf("expected exactly the new message.created, got %d", n)
	}
	if n := countFrames(frames, syncRequiredFrameType); n != 1 {
		t.Fatalf("expected the gap to be signalled with the next delivery, got %d signals", n)
	}
}

// B08: a burst of failures is one gap, not one entry per lost event.
func TestHub_BurstOfSkippedDeliveriesCoalescesIntoOneSignal(t *testing.T) {
	auth := &fakeAuthorizer{}
	h := newRunningTestHub(t, auth)
	c := subscribedRunningClient(t, h, auth, "c1", "user-1", channelOne)

	auth.setErr("user-1", "ws-1", TargetTypeChannel, "ch-1", errors.New("db unavailable"))
	const burst = 300
	for i := range burst {
		broadcastMessage(h, TargetTypeChannel, "ch-1", fmt.Sprintf("msg-%d", i))
	}
	auth.setErr("user-1", "ws-1", TargetTypeChannel, "ch-1", nil)
	awaitFrame(t, c, syncRequiredFrameType, syncRecoveryWait)
	expectNoFrame(t, c, syncRequiredFrameType, syncQuietWindow)
}

// B09: one recipient's failure is that recipient's alone.
func TestHub_AuthErrorForOneRecipientDoesNotAffectAnother(t *testing.T) {
	auth := &fakeAuthorizer{}
	h := newRunningTestHub(t, auth)
	failing := subscribedRunningClient(t, h, auth, "c1", "user-1", channelOne)
	healthy := subscribedRunningClient(t, h, auth, "c2", "user-2", channelOne)

	auth.setErr("user-1", "ws-1", TargetTypeChannel, "ch-1", errors.New("db unavailable"))
	broadcastMessage(h, TargetTypeChannel, "ch-1", "msg-1")

	if frames := drainOutboxFrames(t, healthy); countFrames(frames, string(EventTypeMessageCreated)) != 1 {
		t.Fatalf("the authorized recipient must receive the event, got %d frames", len(frames))
	}
	if frames := drainOutboxFrames(t, failing); len(frames) != 0 {
		t.Fatalf("the failing recipient must receive nothing yet, got %d frames", len(frames))
	}

	auth.setErr("user-1", "ws-1", TargetTypeChannel, "ch-1", nil)
	awaitFrame(t, failing, syncRequiredFrameType, syncRecoveryWait)
	expectNoFrame(t, healthy, syncRequiredFrameType, syncQuietWindow)
}

// B10: a failure on one conversation says nothing about another.
func TestHub_AuthErrorOnOneTargetDoesNotMarkAnother(t *testing.T) {
	auth := &fakeAuthorizer{}
	h := newRunningTestHub(t, auth)
	dm := subscriptionTarget{targetType: TargetTypeDM, targetID: "dm-1"}
	c := subscribedRunningClient(t, h, auth, "c1", "user-1", channelOne, dm)

	auth.setErr("user-1", "ws-1", TargetTypeChannel, "ch-1", errors.New("db unavailable"))
	broadcastMessage(h, TargetTypeChannel, "ch-1", "msg-1")
	broadcastMessage(h, TargetTypeDM, "dm-1", "msg-2")
	frames := drainOutboxFrames(t, c)
	if len(frames) != 1 || frames[0].fields["target_id"] != "dm-1" {
		t.Fatalf("only the DM event may be delivered, got %d frames", len(frames))
	}

	auth.setErr("user-1", "ws-1", TargetTypeChannel, "ch-1", nil)
	frame, _ := awaitFrame(t, c, syncRequiredFrameType, syncRecoveryWait)
	assertSyncRequiredFor(t, frame, TargetTypeChannel, "ch-1")
	expectNoFrame(t, c, syncRequiredFrameType, syncQuietWindow)
}

// B11: an event that arrived over the bus from another replica follows the
// same contract as one published here — the receiving replica owns the
// connection, so it owns the gap.
func TestHub_RemoteBroadcastAuthErrorIsRecoveredOnTheReceivingReplica(t *testing.T) {
	auth := &fakeAuthorizer{}
	bus := &fakeBus{}
	h := newBusTestHub(auth, bus)
	t.Cleanup(h.Shutdown)

	c := newClient("c1", "user-1", testWorkspaceID, &fakeSender{})
	registerInRunningHub(t, h, c)
	auth.setAccess("user-1", testWorkspaceID, TargetTypeChannel, testChannelID, true)
	if err := h.Subscribe(context.Background(), c, TargetTypeChannel, testChannelID); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	auth.setErr("user-1", testWorkspaceID, TargetTypeChannel, testChannelID, errors.New("db unavailable"))
	bus.inject(remoteEvt(uuid.NewString()))
	eventually(t, func() bool { return auth.callCount() >= 2 }, testIOTimeout, "remote event never reached the re-check")

	auth.setErr("user-1", testWorkspaceID, TargetTypeChannel, testChannelID, nil)
	frame, others := awaitFrame(t, c, syncRequiredFrameType, syncRecoveryWait)
	assertSyncRequiredFor(t, frame, TargetTypeChannel, testChannelID)
	if n := countFrames(others, string(EventTypeMessageCreated)); n != 0 {
		t.Fatalf("the skipped remote payload must not be replayed, got %d", n)
	}
	if n := bus.publishCount(); n != 0 {
		t.Fatalf("the sync signal is connection-local and must never reach the bus, got %d publishes", n)
	}
}

// B06: a gap belongs to a subscription. Once the client has left the
// conversation there is nothing to resynchronise, and a later recovery must not
// speak about a target the client no longer holds.
func TestHub_UnsubscribeForgetsThePendingGap(t *testing.T) {
	auth := &fakeAuthorizer{}
	h := newRunningTestHub(t, auth)
	c := subscribedRunningClient(t, h, auth, "c1", "user-1", channelOne)

	auth.setErr("user-1", "ws-1", TargetTypeChannel, "ch-1", errors.New("db unavailable"))
	broadcastMessage(h, TargetTypeChannel, "ch-1", "msg-1")
	key := targetKey{workspaceID: "ws-1", targetType: TargetTypeChannel, targetID: "ch-1"}.String()
	if err := h.RevokeSubscription(context.Background(), c, key); err != nil {
		t.Fatalf("unsubscribe: %v", err)
	}

	auth.setErr("user-1", "ws-1", TargetTypeChannel, "ch-1", nil)
	expectNoFrame(t, c, syncRequiredFrameType, syncQuietWindow)
	if hubHasSubscription(h, key, "c1") {
		t.Fatal("recovery must not resurrect a subscription the client released")
	}
}

// B06 variant: re-subscribing is a new subscription generation. The client
// resynchronises on its own acknowledgement, so the old gap must not be
// replayed onto it.
func TestHub_ResubscribeDoesNotInheritTheOldGap(t *testing.T) {
	auth := &fakeAuthorizer{}
	h := newRunningTestHub(t, auth)
	c := subscribedRunningClient(t, h, auth, "c1", "user-1", channelOne)

	auth.setErr("user-1", "ws-1", TargetTypeChannel, "ch-1", errors.New("db unavailable"))
	broadcastMessage(h, TargetTypeChannel, "ch-1", "msg-1")
	key := targetKey{workspaceID: "ws-1", targetType: TargetTypeChannel, targetID: "ch-1"}.String()
	if err := h.RevokeSubscription(context.Background(), c, key); err != nil {
		t.Fatalf("unsubscribe: %v", err)
	}
	auth.setErr("user-1", "ws-1", TargetTypeChannel, "ch-1", nil)
	if err := h.Subscribe(context.Background(), c, TargetTypeChannel, "ch-1"); err != nil {
		t.Fatalf("resubscribe: %v", err)
	}

	expectNoFrame(t, c, syncRequiredFrameType, syncQuietWindow)
}

// B07: a connection that is gone takes its gaps with it, and the next one
// starts clean — it reconciles on open, so it needs no inherited signal.
func TestHub_DisconnectDropsThePendingGapAndTheNextConnectionStartsClean(t *testing.T) {
	auth := &fakeAuthorizer{}
	h := newRunningTestHub(t, auth)
	old := subscribedRunningClient(t, h, auth, "c1", "user-1", channelOne)

	auth.setErr("user-1", "ws-1", TargetTypeChannel, "ch-1", errors.New("db unavailable"))
	broadcastMessage(h, TargetTypeChannel, "ch-1", "msg-1")
	h.Unregister(old)
	eventually(t, func() bool { return !hubHasClient(h, "c1") }, testIOTimeout, "old connection never unregistered")

	auth.setErr("user-1", "ws-1", TargetTypeChannel, "ch-1", nil)
	replacement := subscribedRunningClient(t, h, auth, "c2", "user-1", channelOne)

	expectNoFrame(t, replacement, syncRequiredFrameType, syncQuietWindow)
}

// B02/B13: a denial discovered while recovering is a revocation, exactly as on
// the broadcast path, and the client is not told anything about the target.
func TestHub_DenialDuringRecoveryRevokesWithoutSignal(t *testing.T) {
	auth := &fakeAuthorizer{}
	h := newRunningTestHub(t, auth)
	c := subscribedRunningClient(t, h, auth, "c1", "user-1", channelOne)

	auth.setErr("user-1", "ws-1", TargetTypeChannel, "ch-1", errors.New("db unavailable"))
	broadcastMessage(h, TargetTypeChannel, "ch-1", "msg-1")
	auth.setAccess("user-1", "ws-1", TargetTypeChannel, "ch-1", false)
	auth.setErr("user-1", "ws-1", TargetTypeChannel, "ch-1", nil)

	key := targetKey{workspaceID: "ws-1", targetType: TargetTypeChannel, targetID: "ch-1"}.String()
	eventually(t, func() bool { return !hubHasSubscription(h, key, "c1") }, syncRecoveryWait,
		"a denial found by recovery must revoke the subscription")
	expectNoFrame(t, c, syncRequiredFrameType, syncQuietWindow)
}

// B13: while access cannot be proven, nothing reaches the client — neither the
// payload nor a signal that would reveal activity in the target.
func TestHub_PersistentAuthErrorNeverDeliversOrSignals(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "operational", err: errors.New("db unavailable")},
		{name: "timeout", err: context.DeadlineExceeded},
		{name: "internal", err: errors.New("unexpected row shape")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth := &fakeAuthorizer{}
			h := newRunningTestHub(t, auth)
			c := subscribedRunningClient(t, h, auth, "c1", "user-1", channelOne)

			auth.setErr("user-1", "ws-1", TargetTypeChannel, "ch-1", tc.err)
			broadcastMessage(h, TargetTypeChannel, "ch-1", "msg-1")
			broadcastMessage(h, TargetTypeChannel, "ch-1", "msg-2")

			expectNoFrame(t, c, syncRequiredFrameType, syncQuietWindow)
			if frames := drainOutboxFrames(t, c); len(frames) != 0 {
				t.Fatalf("nothing may be delivered while access is unproven, got %d frames", len(frames))
			}
		})
	}
}

// B12: the interleavings the issue names, under the race detector. Correctness
// is asserted on the final state: every connection that still holds the target
// is told about its gap exactly once access returns; nothing is ever delivered
// to a connection that released it.
func TestHub_SyncRecoveryUnderConcurrentLifecycle(t *testing.T) {
	auth := &fakeAuthorizer{}
	h := newRunningTestHub(t, auth)
	key := targetKey{workspaceID: "ws-1", targetType: TargetTypeChannel, targetID: "ch-1"}.String()

	stays := subscribedRunningClient(t, h, auth, "stays", "user-1", channelOne)
	leaves := subscribedRunningClient(t, h, auth, "leaves", "user-2", channelOne)
	drops := subscribedRunningClient(t, h, auth, "drops", "user-3", channelOne)
	for _, user := range []string{"user-1", "user-2", "user-3"} {
		auth.setErr(user, "ws-1", TargetTypeChannel, "ch-1", errors.New("db unavailable"))
	}

	var wg sync.WaitGroup
	for i := range 4 {
		wg.Go(func() {
			for j := range 25 {
				broadcastMessage(h, TargetTypeChannel, "ch-1", fmt.Sprintf("msg-%d-%d", i, j))
			}
		})
	}
	wg.Go(func() { _ = h.RevokeSubscription(context.Background(), leaves, key) })
	wg.Go(func() { h.Unregister(drops) })
	wg.Wait()
	eventually(t, func() bool { return !hubHasClient(h, "drops") }, testIOTimeout, "drops never unregistered")

	for _, user := range []string{"user-1", "user-2", "user-3"} {
		auth.setErr(user, "ws-1", TargetTypeChannel, "ch-1", nil)
	}

	awaitFrame(t, stays, syncRequiredFrameType, syncRecoveryWait)
	expectNoFrame(t, stays, syncRequiredFrameType, syncQuietWindow)
	for _, frame := range drainOutboxFrames(t, leaves) {
		t.Fatalf("a connection that released the target received %s", frame.raw)
	}
}

// ── CQ947-06: a stale denial must not revoke a newer subscription ───────────

// gatedAuthorizer holds exactly one check — the next one — until released, and
// then answers it with a denial. Every other check is the plain fake's answer.
type gatedAuthorizer struct {
	fakeAuthorizer
	gateMu  sync.Mutex
	gate    chan struct{}
	entered chan struct{}
}

// holdNextCheck arms the gate. entered closes when the held check starts.
func (a *gatedAuthorizer) holdNextCheck() (entered <-chan struct{}, release func()) {
	a.gateMu.Lock()
	defer a.gateMu.Unlock()
	a.gate = make(chan struct{})
	a.entered = make(chan struct{})
	gate := a.gate
	return a.entered, func() { close(gate) }
}

func (a *gatedAuthorizer) CanAccess(ctx context.Context, userID, workspaceID string, tt TargetType, targetID string) (bool, error) {
	a.gateMu.Lock()
	gate, entered := a.gate, a.entered
	a.gate, a.entered = nil, nil
	a.gateMu.Unlock()
	if gate == nil {
		return a.fakeAuthorizer.CanAccess(ctx, userID, workspaceID, tt, targetID)
	}
	close(entered)
	<-gate
	return false, nil
}

func awaitEntered(t *testing.T, entered <-chan struct{}) {
	t.Helper()
	select {
	case <-entered:
	case <-time.After(syncRecoveryWait):
		t.Fatal("the held authorization check never started")
	}
}

// consistently fails if condition stops holding at any point within the window.
func consistently(t *testing.T, condition func() bool, window time.Duration, msg string) {
	t.Helper()
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		if !condition() {
			t.Fatal(msg)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// resubscribe replaces c's subscription to ch-1 with a new generation.
func resubscribe(t *testing.T, h *Hub, c *Client, key string) {
	t.Helper()
	if err := h.RevokeSubscription(context.Background(), c, key); err != nil {
		t.Fatalf("unsubscribe: %v", err)
	}
	if err := h.Subscribe(context.Background(), c, TargetTypeChannel, "ch-1"); err != nil {
		t.Fatalf("resubscribe: %v", err)
	}
}

// The recovery pass asked about generation G1; while it waited the client
// left and came back as G2. The late denial is about G1 and must not touch G2.
func TestHub_StaleRecoveryDenialDoesNotRevokeANewerSubscription(t *testing.T) {
	auth := &gatedAuthorizer{}
	h := newRunningTestHub(t, auth)
	c := subscribedRunningClient(t, h, &auth.fakeAuthorizer, "c1", "user-1", channelOne)
	key := targetKey{workspaceID: "ws-1", targetType: TargetTypeChannel, targetID: "ch-1"}.String()

	auth.setErr("user-1", "ws-1", TargetTypeChannel, "ch-1", errors.New("db unavailable"))
	broadcastMessage(h, TargetTypeChannel, "ch-1", "msg-1")
	entered, release := auth.holdNextCheck()
	auth.setErr("user-1", "ws-1", TargetTypeChannel, "ch-1", nil)
	awaitEntered(t, entered)

	resubscribe(t, h, c, key)
	release()

	consistently(t, func() bool { return hubHasSubscription(h, key, "c1") }, syncQuietWindow,
		"a denial about the previous generation revoked the current subscription")
	broadcastMessage(h, TargetTypeChannel, "ch-1", "msg-2")
	if n := countFrames(drainOutboxFrames(t, c), string(EventTypeMessageCreated)); n != 1 {
		t.Fatalf("the current subscription must keep receiving events, got %d", n)
	}
}

// The same race on the broadcast path, local or relayed from another replica:
// the check started for G1, the answer arrives after G2 exists.
func TestHub_StaleBroadcastDenialDoesNotRevokeANewerSubscription(t *testing.T) {
	auth := &gatedAuthorizer{}
	h := newRunningTestHub(t, auth)
	c := subscribedRunningClient(t, h, &auth.fakeAuthorizer, "c1", "user-1", channelOne)
	key := targetKey{workspaceID: "ws-1", targetType: TargetTypeChannel, targetID: "ch-1"}.String()

	entered, release := auth.holdNextCheck()
	done := make(chan struct{})
	go func() {
		defer close(done)
		broadcastMessage(h, TargetTypeChannel, "ch-1", "msg-1")
	}()
	awaitEntered(t, entered)
	resubscribe(t, h, c, key)
	release()
	<-done

	if !hubHasSubscription(h, key, "c1") {
		t.Fatal("a denial about the previous generation revoked the current subscription")
	}
}

// Fail secure is untouched: a denial about the generation that is still current
// revokes it.
func TestHub_CurrentGenerationDenialStillRevokes(t *testing.T) {
	auth := &gatedAuthorizer{}
	h := newRunningTestHub(t, auth)
	subscribedRunningClient(t, h, &auth.fakeAuthorizer, "c1", "user-1", channelOne)
	key := targetKey{workspaceID: "ws-1", targetType: TargetTypeChannel, targetID: "ch-1"}.String()

	_, release := auth.holdNextCheck()
	release()
	broadcastMessage(h, TargetTypeChannel, "ch-1", "msg-1")

	if hubHasSubscription(h, key, "c1") {
		t.Fatal("a denial about the current generation must revoke it")
	}
}

// The broadcast path is also where events relayed from another replica are
// re-checked; the same stale denial must not cross generations there either.
func TestHub_StaleRemoteBroadcastDenialDoesNotRevokeANewerSubscription(t *testing.T) {
	auth := &gatedAuthorizer{}
	bus := &fakeBus{}
	h := newBusTestHub(auth, bus)
	t.Cleanup(h.Shutdown)
	c := newClient("c1", "user-1", testWorkspaceID, &fakeSender{})
	registerInRunningHub(t, h, c)
	auth.setAccess("user-1", testWorkspaceID, TargetTypeChannel, testChannelID, true)
	if err := h.Subscribe(context.Background(), c, TargetTypeChannel, testChannelID); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	key := targetKey{workspaceID: testWorkspaceID, targetType: TargetTypeChannel, targetID: testChannelID}.String()

	entered, release := auth.holdNextCheck()
	bus.inject(remoteEvt(uuid.NewString()))
	awaitEntered(t, entered)
	if err := h.RevokeSubscription(context.Background(), c, key); err != nil {
		t.Fatalf("unsubscribe: %v", err)
	}
	if err := h.Subscribe(context.Background(), c, TargetTypeChannel, testChannelID); err != nil {
		t.Fatalf("resubscribe: %v", err)
	}
	release()

	consistently(t, func() bool { return hubHasSubscription(h, key, "c1") }, time.Second,
		"a remote event's stale denial revoked the current subscription")
}

// A denial that lands after the connection is gone has nothing to act on.
func TestHub_DenialAfterDisconnectIsHarmless(t *testing.T) {
	auth := &gatedAuthorizer{}
	h := newRunningTestHub(t, auth)
	c := subscribedRunningClient(t, h, &auth.fakeAuthorizer, "c1", "user-1", channelOne)

	entered, release := auth.holdNextCheck()
	done := make(chan struct{})
	go func() {
		defer close(done)
		broadcastMessage(h, TargetTypeChannel, "ch-1", "msg-1")
	}()
	awaitEntered(t, entered)
	h.Unregister(c)
	eventually(t, func() bool { return !hubHasClient(h, "c1") }, testIOTimeout, "unregister")
	release()
	<-done

	if len(c.outbox) != 0 {
		t.Fatal("nothing may reach a connection that is gone")
	}
}
