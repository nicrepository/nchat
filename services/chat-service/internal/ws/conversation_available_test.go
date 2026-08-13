package ws

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// PublishConversationAvailable is the one publish path that ignores
// subscriptions, so these assert the audience directly: who receives it, who
// does not, and what it carries.
//
// They drive the hub's maps in-process and read each client's outbox, so no
// listener is opened and the sandbox's IPv6 restrictions do not apply.

// availableTestAuthorizer allows everything, so the local publish tests below
// isolate routing from authorization.
//
// The local path does not consult it: the recipients there are the ones the
// caller's own committed transaction returned, so there is nothing for the hub
// to re-derive. The remote path is the opposite case and does consult it — see
// recordingAuthorizer and the cross-instance tests further down.
type availableTestAuthorizer struct{}

func (availableTestAuthorizer) CanAccess(_ context.Context, _, _ string, _ TargetType, _ string) (bool, error) {
	return true, nil
}

// recordingAuthorizer answers a fixed verdict and remembers what it was asked,
// so the remote tests can assert both the decision and that the question was put
// with the envelope's own recipient, workspace and target.
type recordingAuthorizer struct {
	mu      sync.Mutex
	allowed bool
	err     error
	// block, when non-nil, holds CanAccess until it is closed or ctx expires —
	// how the timeout case is driven without a sleep.
	block chan struct{}
	calls []authorizerCall
}

type authorizerCall struct {
	userID      string
	workspaceID string
	targetType  TargetType
	targetID    string
}

func (a *recordingAuthorizer) CanAccess(
	ctx context.Context, userID, workspaceID string, targetType TargetType, targetID string,
) (bool, error) {
	a.mu.Lock()
	a.calls = append(a.calls, authorizerCall{userID, workspaceID, targetType, targetID})
	block := a.block
	allowed, err := a.allowed, a.err
	a.mu.Unlock()

	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	return allowed, err
}

func (a *recordingAuthorizer) recorded() []authorizerCall {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]authorizerCall(nil), a.calls...)
}

// registerForTest adds a client to the hub the way addClient would, without
// starting the hub goroutine.
func registerForTest(h *Hub, clientID, userID, workspaceID string) *Client {
	c := newClient(clientID, userID, workspaceID, &fakeSender{})
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clients[c.id] = c
	h.clientSubs[c.id] = make(map[string]struct{})
	h.subscriptionGenerations[c.id] = make(map[string]uint64)
	return c
}

// drain returns everything queued for a client without blocking.
func drain(c *Client) [][]byte {
	var out [][]byte
	for {
		select {
		case data := <-c.outbox:
			out = append(out, data)
		default:
			return out
		}
	}
}

func TestPublishConversationAvailableReachesUnsubscribedRecipient(t *testing.T) {
	hub := newTestHub(availableTestAuthorizer{})
	// Deliberately never subscribed to the channel: that is the whole point —
	// somebody just added to a private channel cannot already be a subscriber.
	added := registerForTest(hub, "c-added", "user-added", "ws-1")

	hub.PublishConversationAvailable(context.Background(), "ws-1", TargetTypeChannel, "ch-1", []string{"user-added"})

	messages := drain(added)
	if len(messages) != 1 {
		t.Fatalf("recipient got %d message(s), want 1", len(messages))
	}
	var evt Event
	if err := json.Unmarshal(messages[0], &evt); err != nil {
		t.Fatalf("unmarshal event: %v", err)
	}
	if evt.Type != EventTypeConversationAvailable {
		t.Fatalf("type = %q, want %q", evt.Type, EventTypeConversationAvailable)
	}
	if evt.TargetType != TargetTypeChannel || evt.TargetID != "ch-1" {
		t.Fatalf("event names %s/%s", evt.TargetType, evt.TargetID)
	}
}

// The signal must not tell anyone else that this conversation exists.
func TestPublishConversationAvailableDoesNotReachOtherUsers(t *testing.T) {
	hub := newTestHub(availableTestAuthorizer{})
	added := registerForTest(hub, "c-added", "user-added", "ws-1")
	bystander := registerForTest(hub, "c-other", "user-other", "ws-1")

	hub.PublishConversationAvailable(context.Background(), "ws-1", TargetTypeChannel, "ch-1", []string{"user-added"})

	if got := len(drain(added)); got != 1 {
		t.Fatalf("recipient got %d, want 1", got)
	}
	if got := len(drain(bystander)); got != 0 {
		t.Fatalf("a bystander received %d message(s) about a channel they were not added to", got)
	}
}

// A session connected under a different workspace must not learn about this one.
func TestPublishConversationAvailableIsWorkspaceScoped(t *testing.T) {
	hub := newTestHub(availableTestAuthorizer{})
	// Same user ID, different workspace context.
	foreign := registerForTest(hub, "c-foreign", "user-added", "ws-2")

	hub.PublishConversationAvailable(context.Background(), "ws-1", TargetTypeChannel, "ch-1", []string{"user-added"})

	if got := len(drain(foreign)); got != 0 {
		t.Fatalf("a session in another workspace received %d message(s)", got)
	}
}

// Every live session of the recipient shows a sidebar, so every one is told.
func TestPublishConversationAvailableReachesAllSessionsOfTheUser(t *testing.T) {
	hub := newTestHub(availableTestAuthorizer{})
	first := registerForTest(hub, "c-1", "user-added", "ws-1")
	second := registerForTest(hub, "c-2", "user-added", "ws-1")

	hub.PublishConversationAvailable(context.Background(), "ws-1", TargetTypeDM, "dm-1", []string{"user-added"})

	if got := len(drain(first)); got != 1 {
		t.Fatalf("first session got %d, want 1", got)
	}
	if got := len(drain(second)); got != 1 {
		t.Fatalf("second session got %d, want 1", got)
	}
}

func TestPublishConversationAvailableReachesEachAddedUser(t *testing.T) {
	hub := newTestHub(availableTestAuthorizer{})
	a := registerForTest(hub, "c-a", "user-a", "ws-1")
	b := registerForTest(hub, "c-b", "user-b", "ws-1")

	hub.PublishConversationAvailable(
		context.Background(), "ws-1", TargetTypeDM, "dm-1", []string{"user-a", "user-b"},
	)

	if got := len(drain(a)); got != 1 {
		t.Fatalf("user-a got %d, want 1", got)
	}
	if got := len(drain(b)); got != 1 {
		t.Fatalf("user-b got %d, want 1", got)
	}
}

// A repeated ID is a client-side accident; it must not produce two signals.
func TestPublishConversationAvailableDeduplicatesRecipients(t *testing.T) {
	hub := newTestHub(availableTestAuthorizer{})
	added := registerForTest(hub, "c-added", "user-added", "ws-1")

	hub.PublishConversationAvailable(
		context.Background(), "ws-1", TargetTypeChannel, "ch-1",
		[]string{"user-added", "user-added", "user-added"},
	)

	if got := len(drain(added)); got != 1 {
		t.Fatalf("got %d message(s) for a repeated ID, want 1", got)
	}
}

// An empty recipient list is what a rollback or a pure retry produces.
func TestPublishConversationAvailableWithNoRecipientsSendsNothing(t *testing.T) {
	hub := newTestHub(availableTestAuthorizer{})
	client := registerForTest(hub, "c-1", "user-added", "ws-1")

	hub.PublishConversationAvailable(context.Background(), "ws-1", TargetTypeChannel, "ch-1", nil)
	hub.PublishConversationAvailable(context.Background(), "ws-1", TargetTypeChannel, "ch-1", []string{})

	if got := len(drain(client)); got != 0 {
		t.Fatalf("got %d message(s) with no recipients", got)
	}
}

// A user with no live session is normal, not an error: their sidebar is correct
// on next load. The call must simply do nothing.
func TestPublishConversationAvailableWithNoSessionIsANoop(t *testing.T) {
	hub := newTestHub(availableTestAuthorizer{})
	other := registerForTest(hub, "c-other", "user-other", "ws-1")

	hub.PublishConversationAvailable(
		context.Background(), "ws-1", TargetTypeChannel, "ch-1", []string{"user-offline"},
	)

	if got := len(drain(other)); got != 0 {
		t.Fatalf("an unrelated session received %d message(s)", got)
	}
}

// The payload is an invalidation hint addressed to one person.
//
// It necessarily names its recipient — that is the routing key a remote
// instance uses to find their sessions — but naming the addressee to the
// addressee discloses nothing. What must never appear is anyone *else*: not the
// other users added in the same operation, not their names, not the
// conversation's contents.
func TestPublishConversationAvailableCarriesNoOtherIdentities(t *testing.T) {
	hub := newTestHub(availableTestAuthorizer{})
	added := registerForTest(hub, "c-added", "user-added", "ws-1")

	hub.PublishConversationAvailable(
		context.Background(), "ws-1", TargetTypeChannel, "ch-1", []string{"user-added", "user-b"},
	)

	messages := drain(added)
	if len(messages) != 1 {
		t.Fatalf("got %d message(s)", len(messages))
	}
	body := string(messages[0])
	// "user-b" was added in the same call and must not appear in the event sent
	// to "user-added": one event per recipient is what guarantees that.
	for _, leak := range []string{"user-b", "user_ids", "actor", "display_name", "email", "members"} {
		if strings.Contains(body, leak) {
			t.Fatalf("payload leaked %q: %s", leak, body)
		}
	}
	var evt Event
	if err := json.Unmarshal(messages[0], &evt); err != nil {
		t.Fatalf("unmarshal event: %v", err)
	}
	if evt.RecipientUserID != "user-added" {
		t.Fatalf("RecipientUserID = %q, want only the addressee", evt.RecipientUserID)
	}
}

// Shutdown must not deliver, and must not panic on a concurrent publish.
func TestPublishConversationAvailableDoesNothingAfterShutdown(t *testing.T) {
	hub := newTestHub(availableTestAuthorizer{})
	client := registerForTest(hub, "c-1", "user-added", "ws-1")
	// Shutdown is signalled by closing quit, which is what isShuttingDown reads.
	hub.quit = make(chan struct{})
	close(hub.quit)

	hub.PublishConversationAvailable(
		context.Background(), "ws-1", TargetTypeChannel, "ch-1", []string{"user-added"},
	)

	if got := len(drain(client)); got != 0 {
		t.Fatalf("delivered %d message(s) during shutdown", got)
	}
}

// A full outbox costs a refresh hint, never the connection: the sidebar is
// still correct after the next refetch, which is not worth a disconnect.
func TestPublishConversationAvailableSkipsAFullOutboxWithoutDropping(t *testing.T) {
	hub := newTestHub(availableTestAuthorizer{})
	client := registerForTest(hub, "c-1", "user-added", "ws-1")
	for i := 0; i < outboxSize; i++ {
		if !client.enqueue([]byte("filler")) {
			t.Fatalf("could not fill the outbox at %d", i)
		}
	}

	hub.PublishConversationAvailable(
		context.Background(), "ws-1", TargetTypeChannel, "ch-1", []string{"user-added"},
	)

	// Still registered: the publish did not drop the client.
	hub.mu.Lock()
	_, stillRegistered := hub.clients["c-1"]
	hub.mu.Unlock()
	if !stillRegistered {
		t.Fatal("a full outbox dropped the client; a refresh hint must not cost a session")
	}
}

// ── Cross-instance propagation (issue #398) ─────────────────────────────────

// A pod that did not perform the write must still tell its local sessions.
// These wire two hubs to one in-memory bus and assert the routing directly —
// deterministic, no listener, no sleeps.

const (
	caWorkspace = "3fbc9e6e-1f4a-4a2b-8b1a-0d1a2b3c4d5e"
	caChannel   = "5a1d2c3b-4e5f-4a7b-8c9d-0e1f2a3b4c5d"
	caRecipient = "9f8e7d6c-5b4a-4938-8271-6a5b4c3d2e1f"
	caOther     = "1a2b3c4d-5e6f-4718-8293-a4b5c6d7e8f9"
)

// twoHubs wires hub B's remote handler to hub A's bus, so anything A publishes
// is delivered to B exactly as a real deployment would.
func twoHubs(t *testing.T) (*Hub, *Hub, *fakeBus) {
	t.Helper()
	return twoHubsWithAuthorizer(t, availableTestAuthorizer{})
}

// twoHubsWithAuthorizer is twoHubs with the receiving hub's authorizer chosen by
// the caller — the one the remote delivery path must consult.
func twoHubsWithAuthorizer(t *testing.T, receiverAuth SubscriptionAuthorizer) (*Hub, *Hub, *fakeBus) {
	t.Helper()
	bus := &fakeBus{}
	hubA := newTestHub(availableTestAuthorizer{})
	hubA.instanceID = "pod-a"
	hubA.bus = bus

	hubB := newTestHub(receiverAuth)
	hubB.instanceID = "pod-b"
	hubB.bus = bus
	// The bus hands remote events to B's handler, the same entry point the real
	// subscriber uses.
	if err := bus.Subscribe(context.Background(), hubB.handleRemoteBusEvent); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	return hubA, hubB, bus
}

// deliverPublished replays what hub A put on the bus into hub B.
func deliverPublished(bus *fakeBus) {
	bus.mu.Lock()
	events := append([]Event(nil), bus.published...)
	handler := bus.handler
	bus.mu.Unlock()
	for _, evt := range events {
		if handler != nil {
			handler(evt)
		}
	}
}

// The headline case: the write happens on pod A, the recipient's only socket is
// on pod B, and B must still deliver.
func TestConversationAvailableCrossesInstances(t *testing.T) {
	hubA, hubB, bus := twoHubs(t)
	// Never subscribed to the channel — nobody just added to a private channel
	// could be.
	remote := registerForTest(hubB, "c-remote", caRecipient, caWorkspace)

	hubA.PublishConversationAvailable(
		context.Background(), caWorkspace, TargetTypeChannel, caChannel, []string{caRecipient},
	)
	deliverPublished(bus)

	messages := drain(remote)
	if len(messages) != 1 {
		t.Fatalf("remote session got %d message(s), want 1", len(messages))
	}
	var evt Event
	if err := json.Unmarshal(messages[0], &evt); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if evt.Type != EventTypeConversationAvailable || evt.TargetID != caChannel {
		t.Fatalf("wrong event delivered: %+v", evt)
	}
}

func TestConversationAvailableDoesNotReachOtherUsersRemotely(t *testing.T) {
	hubA, hubB, bus := twoHubs(t)
	remote := registerForTest(hubB, "c-remote", caRecipient, caWorkspace)
	bystander := registerForTest(hubB, "c-bystander", caOther, caWorkspace)

	hubA.PublishConversationAvailable(
		context.Background(), caWorkspace, TargetTypeChannel, caChannel, []string{caRecipient},
	)
	deliverPublished(bus)

	if got := len(drain(remote)); got != 1 {
		t.Fatalf("recipient got %d, want 1", got)
	}
	if got := len(drain(bystander)); got != 0 {
		t.Fatalf("a different user on the remote pod received %d message(s)", got)
	}
}

// A session in another workspace must not be routed to, even for the same user.
func TestConversationAvailableRemoteDeliveryIsWorkspaceScoped(t *testing.T) {
	hubA, hubB, bus := twoHubs(t)
	foreign := registerForTest(hubB, "c-foreign", caRecipient, "7c6b5a49-3827-4160-9152-a3b4c5d6e7f8")

	hubA.PublishConversationAvailable(
		context.Background(), caWorkspace, TargetTypeChannel, caChannel, []string{caRecipient},
	)
	deliverPublished(bus)

	if got := len(drain(foreign)); got != 0 {
		t.Fatalf("a session in another workspace received %d message(s)", got)
	}
}

// The originating pod delivers locally and must not deliver a second time when
// its own bus copy comes back: SourceInstanceID suppresses the echo.
func TestConversationAvailableIsNotDeliveredTwiceOnTheOriginatingPod(t *testing.T) {
	hubA, _, bus := twoHubs(t)
	local := registerForTest(hubA, "c-local", caRecipient, caWorkspace)
	// Route the bus back to A as well, so a missing echo check would double up.
	if err := bus.Subscribe(context.Background(), hubA.handleRemoteBusEvent); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	hubA.PublishConversationAvailable(
		context.Background(), caWorkspace, TargetTypeChannel, caChannel, []string{caRecipient},
	)
	deliverPublished(bus)

	if got := len(drain(local)); got != 1 {
		t.Fatalf("local session got %d message(s), want exactly 1", got)
	}
}

// Sessions on both pods: one message each, no more.
func TestConversationAvailableReachesSessionsOnBothInstances(t *testing.T) {
	hubA, hubB, bus := twoHubs(t)
	local := registerForTest(hubA, "c-local", caRecipient, caWorkspace)
	remote := registerForTest(hubB, "c-remote", caRecipient, caWorkspace)

	hubA.PublishConversationAvailable(
		context.Background(), caWorkspace, TargetTypeChannel, caChannel, []string{caRecipient},
	)
	deliverPublished(bus)

	if got := len(drain(local)); got != 1 {
		t.Fatalf("local session got %d, want 1", got)
	}
	if got := len(drain(remote)); got != 1 {
		t.Fatalf("remote session got %d, want 1", got)
	}
}

// One event per recipient is what keeps recipients from learning about each
// other, and it is also what makes remote routing possible at all.
func TestConversationAvailablePublishesOneEventPerRecipient(t *testing.T) {
	hubA, _, bus := twoHubs(t)

	hubA.PublishConversationAvailable(
		context.Background(), caWorkspace, TargetTypeDM, caChannel,
		[]string{caRecipient, caOther, caRecipient},
	)

	bus.mu.Lock()
	published := append([]Event(nil), bus.published...)
	bus.mu.Unlock()

	// Two distinct recipients, and the repeated ID collapsed.
	if len(published) != 2 {
		t.Fatalf("published %d event(s), want 2", len(published))
	}
	seen := map[string]int{}
	for _, evt := range published {
		if evt.RecipientUserID == "" {
			t.Fatalf("published an event with no recipient: %+v", evt)
		}
		seen[evt.RecipientUserID]++
	}
	if seen[caRecipient] != 1 || seen[caOther] != 1 {
		t.Fatalf("recipient distribution = %v, want one each", seen)
	}
}

func TestConversationAvailablePublishesNothingWithoutRecipients(t *testing.T) {
	hubA, _, bus := twoHubs(t)

	hubA.PublishConversationAvailable(context.Background(), caWorkspace, TargetTypeChannel, caChannel, nil)

	if got := bus.publishCount(); got != 0 {
		t.Fatalf("published %d event(s) with no recipients", got)
	}
}

// An event that arrived from the bus must never be put back on it.
func TestRemoteConversationAvailableIsNotRepublished(t *testing.T) {
	_, hubB, bus := twoHubs(t)
	registerForTest(hubB, "c-remote", caRecipient, caWorkspace)

	hubB.handleRemoteBusEvent(Event{
		SchemaVersion: CurrentEventSchemaVersion, Type: EventTypeConversationAvailable,
		WorkspaceID: caWorkspace, TargetType: TargetTypeChannel, TargetID: caChannel,
		RecipientUserID:  caRecipient,
		EventID:          "b2c3d4e5-f6a7-4819-8a2b-3c4d5e6f7a8b",
		SourceInstanceID: "pod-a",
	})

	if got := bus.publishCount(); got != 0 {
		t.Fatalf("a remote event was republished %d time(s)", got)
	}
}

// Malformed envelopes from the bus are untrusted input and must be dropped
// without delivery.
func TestRemoteConversationAvailableRejectsMalformedEnvelopes(t *testing.T) {
	valid := Event{
		SchemaVersion: CurrentEventSchemaVersion, Type: EventTypeConversationAvailable,
		WorkspaceID: caWorkspace, TargetType: TargetTypeChannel, TargetID: caChannel,
		RecipientUserID:  caRecipient,
		EventID:          "b2c3d4e5-f6a7-4819-8a2b-3c4d5e6f7a8b",
		SourceInstanceID: "pod-a",
	}

	tests := map[string]func(Event) Event{
		"no recipient":         func(e Event) Event { e.RecipientUserID = ""; return e },
		"recipient not a uuid": func(e Event) Event { e.RecipientUserID = "not-a-uuid"; return e },
		"workspace not a uuid": func(e Event) Event { e.WorkspaceID = "nope"; return e },
		"target not a uuid":    func(e Event) Event { e.TargetID = "nope"; return e },
		"unknown target type":  func(e Event) Event { e.TargetType = "elsewhere"; return e },
		"no source instance":   func(e Event) Event { e.SourceInstanceID = ""; return e },
		"bad event id":         func(e Event) Event { e.EventID = "nope"; return e },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			_, hubB, _ := twoHubs(t)
			client := registerForTest(hubB, "c-remote", caRecipient, caWorkspace)

			hubB.handleRemoteBusEvent(mutate(valid))

			if got := len(drain(client)); got != 0 {
				t.Fatalf("a malformed envelope delivered %d message(s)", got)
			}
		})
	}
}

// A message-scoped event carrying a recipient is not a shape this protocol
// produces; accepting it would let a sender bypass subscription routing.
func TestRemoteMessageEventWithRecipientIsRejected(t *testing.T) {
	_, hubB, _ := twoHubs(t)
	client := registerForTest(hubB, "c-remote", caRecipient, caWorkspace)

	hubB.handleRemoteBusEvent(Event{
		SchemaVersion: CurrentEventSchemaVersion, Type: EventTypePinUpdated,
		WorkspaceID: caWorkspace, TargetType: TargetTypeChannel, TargetID: caChannel,
		MessageID:        "c3d4e5f6-a7b8-4920-9b3c-4d5e6f7a8b9c",
		RecipientUserID:  caRecipient,
		EventID:          "b2c3d4e5-f6a7-4819-8a2b-3c4d5e6f7a8b",
		SourceInstanceID: "pod-a",
	})

	if got := len(drain(client)); got != 0 {
		t.Fatalf("a message event with a recipient delivered %d message(s)", got)
	}
}

// A recipient with no session on this pod is normal, not an error.
func TestRemoteConversationAvailableWithNoLocalSessionIsANoop(t *testing.T) {
	_, hubB, _ := twoHubs(t)
	unrelated := registerForTest(hubB, "c-other", caOther, caWorkspace)

	hubB.handleRemoteBusEvent(Event{
		SchemaVersion: CurrentEventSchemaVersion, Type: EventTypeConversationAvailable,
		WorkspaceID: caWorkspace, TargetType: TargetTypeChannel, TargetID: caChannel,
		RecipientUserID:  caRecipient,
		EventID:          "b2c3d4e5-f6a7-4819-8a2b-3c4d5e6f7a8b",
		SourceInstanceID: "pod-a",
	})

	if got := len(drain(unrelated)); got != 0 {
		t.Fatalf("an unrelated session received %d message(s)", got)
	}
}

// ── Remote reauthorization (issue #398) ─────────────────────────────────────

// The bus is a trust boundary. An event arriving over it names its own
// recipient, workspace and target, and none of those are things this pod
// decided — so before a frame reaches a session, the recipient's access to the
// named target is re-derived from the authoritative record.
//
// Without it, anything able to publish on the bus could aim a metadata signal
// and a forced refetch at any user whose ID it could name, and the sidebar API
// would be the only barrier left. These assert it is not the only one.

// remoteAvailable builds the envelope a sibling pod would put on the bus.
func remoteAvailable() Event {
	return Event{
		SchemaVersion: CurrentEventSchemaVersion, Type: EventTypeConversationAvailable,
		WorkspaceID: caWorkspace, TargetType: TargetTypeChannel, TargetID: caChannel,
		RecipientUserID:  caRecipient,
		EventID:          "b2c3d4e5-f6a7-4819-8a2b-3c4d5e6f7a8b",
		SourceInstanceID: "pod-a",
	}
}

func TestRemoteConversationAvailableReauthorizesTheRecipient(t *testing.T) {
	auth := &recordingAuthorizer{allowed: true}
	_, hubB, _ := twoHubsWithAuthorizer(t, auth)
	client := registerForTest(hubB, "c-remote", caRecipient, caWorkspace)

	hubB.handleRemoteBusEvent(remoteAvailable())

	calls := auth.recorded()
	if len(calls) != 1 {
		t.Fatalf("authorizer consulted %d time(s), want exactly 1", len(calls))
	}
	// The question must be put with the envelope's own scope, not a default.
	want := authorizerCall{caRecipient, caWorkspace, TargetTypeChannel, caChannel}
	if calls[0] != want {
		t.Fatalf("authorizer asked %+v, want %+v", calls[0], want)
	}
	if got := len(drain(client)); got != 1 {
		t.Fatalf("an authorized recipient got %d message(s), want 1", got)
	}
}

// The membership was removed, revoked, or never existed. One answer for all of
// them, and it is silence.
func TestRemoteConversationAvailableIsDroppedWhenAccessIsDenied(t *testing.T) {
	auth := &recordingAuthorizer{allowed: false}
	_, hubB, _ := twoHubsWithAuthorizer(t, auth)
	client := registerForTest(hubB, "c-remote", caRecipient, caWorkspace)

	hubB.handleRemoteBusEvent(remoteAvailable())

	if len(auth.recorded()) != 1 {
		t.Fatal("the authorizer was not consulted; the envelope was trusted")
	}
	if got := len(drain(client)); got != 0 {
		t.Fatalf("a denied recipient received %d frame(s)", got)
	}
}

// An error is not evidence of access. No permissive fallback.
func TestRemoteConversationAvailableIsDroppedWhenTheAuthorizerFails(t *testing.T) {
	auth := &recordingAuthorizer{allowed: true, err: errors.New("database unavailable")}
	_, hubB, _ := twoHubsWithAuthorizer(t, auth)
	client := registerForTest(hubB, "c-remote", caRecipient, caWorkspace)

	hubB.handleRemoteBusEvent(remoteAvailable())

	if got := len(drain(client)); got != 0 {
		t.Fatalf("an authorizer error delivered %d frame(s); it must fail closed", got)
	}
}

// A hanging authorizer must not deliver and must not pin the bus consumer
// forever: the bounded context is what releases it.
func TestRemoteConversationAvailableIsDroppedOnAuthorizerTimeout(t *testing.T) {
	auth := &recordingAuthorizer{allowed: true, block: make(chan struct{})}
	_, hubB, _ := twoHubsWithAuthorizer(t, auth)
	client := registerForTest(hubB, "c-remote", caRecipient, caWorkspace)
	// Never closed: CanAccess returns only when the bounded context expires.
	t.Cleanup(func() { close(auth.block) })

	done := make(chan struct{})
	go func() {
		defer close(done)
		hubB.handleRemoteBusEvent(remoteAvailable())
	}()

	select {
	case <-done:
	case <-time.After(broadcastAuthTimeout + 5*time.Second):
		t.Fatal("the bus consumer blocked past the authorization timeout")
	}
	if got := len(drain(client)); got != 0 {
		t.Fatalf("a timed-out authorization delivered %d frame(s)", got)
	}
}

// Denied delivery must also not leak through a second session of the same user.
func TestRemoteConversationAvailableDeniedForEverySessionOfTheRecipient(t *testing.T) {
	auth := &recordingAuthorizer{allowed: false}
	_, hubB, _ := twoHubsWithAuthorizer(t, auth)
	first := registerForTest(hubB, "c-1", caRecipient, caWorkspace)
	second := registerForTest(hubB, "c-2", caRecipient, caWorkspace)

	hubB.handleRemoteBusEvent(remoteAvailable())

	if got := len(drain(first)) + len(drain(second)); got != 0 {
		t.Fatalf("a denied recipient received %d frame(s) across sessions", got)
	}
}

// Allowed delivery reaches every live session, since any of them may show the
// sidebar — and it is authorized once for the user, not once per socket.
func TestRemoteConversationAvailableAllowedReachesEverySession(t *testing.T) {
	auth := &recordingAuthorizer{allowed: true}
	_, hubB, _ := twoHubsWithAuthorizer(t, auth)
	first := registerForTest(hubB, "c-1", caRecipient, caWorkspace)
	second := registerForTest(hubB, "c-2", caRecipient, caWorkspace)

	hubB.handleRemoteBusEvent(remoteAvailable())

	if got := len(drain(first)); got != 1 {
		t.Fatalf("first session got %d, want 1", got)
	}
	if got := len(drain(second)); got != 1 {
		t.Fatalf("second session got %d, want 1", got)
	}
	if got := len(auth.recorded()); got != 1 {
		t.Fatalf("authorized %d time(s) for one user, want 1", got)
	}
}

// A forged target kind must not get a group's ID checked as something else. The
// authorizer resolves each kind against its own table and denies any other, so
// the kind on the wire cannot widen what is checked.
func TestRemoteConversationAvailableRespectsTheTargetKind(t *testing.T) {
	auth := &recordingAuthorizer{allowed: true}
	_, hubB, _ := twoHubsWithAuthorizer(t, auth)
	registerForTest(hubB, "c-remote", caRecipient, caWorkspace)

	evt := remoteAvailable()
	evt.TargetType = TargetTypeDM
	hubB.handleRemoteBusEvent(evt)

	calls := auth.recorded()
	if len(calls) != 1 {
		t.Fatalf("authorizer consulted %d time(s), want 1", len(calls))
	}
	if calls[0].targetType != TargetTypeDM {
		t.Fatalf("authorized against %q, want the kind the envelope named", calls[0].targetType)
	}
}

// Authorization is not reached at all for an envelope that does not canonicalize:
// a rejected shape is dropped before any question is asked.
func TestRemoteConversationAvailableRejectsBeforeAuthorizing(t *testing.T) {
	auth := &recordingAuthorizer{allowed: true}
	_, hubB, _ := twoHubsWithAuthorizer(t, auth)
	client := registerForTest(hubB, "c-remote", caRecipient, caWorkspace)

	evt := remoteAvailable()
	evt.TargetType = "elsewhere"
	hubB.handleRemoteBusEvent(evt)

	if got := len(auth.recorded()); got != 0 {
		t.Fatalf("an unknown target kind reached the authorizer %d time(s)", got)
	}
	if got := len(drain(client)); got != 0 {
		t.Fatalf("an unknown target kind delivered %d frame(s)", got)
	}
}

// The recipient's own self-echo is dropped before authorization too: this pod
// already delivered locally, and re-authorizing would be a second lookup for a
// frame that must not be sent again either way.
func TestRemoteConversationAvailableSelfEchoIsDroppedBeforeAuthorizing(t *testing.T) {
	auth := &recordingAuthorizer{allowed: true}
	_, hubB, _ := twoHubsWithAuthorizer(t, auth)
	client := registerForTest(hubB, "c-remote", caRecipient, caWorkspace)

	evt := remoteAvailable()
	evt.SourceInstanceID = hubB.presenceInstanceID
	hubB.handleRemoteBusEvent(evt)

	if got := len(auth.recorded()); got != 0 {
		t.Fatalf("a self-echo reached the authorizer %d time(s)", got)
	}
	if got := len(drain(client)); got != 0 {
		t.Fatalf("a self-echo delivered %d frame(s)", got)
	}
}

// Authorization decides delivery; it must not add anything to what is delivered.
func TestRemoteConversationAvailablePayloadStaysMinimal(t *testing.T) {
	auth := &recordingAuthorizer{allowed: true}
	_, hubB, _ := twoHubsWithAuthorizer(t, auth)
	client := registerForTest(hubB, "c-remote", caRecipient, caWorkspace)

	// A producer that attached things it should not have.
	evt := remoteAvailable()
	evt.MessageID = "c3d4e5f6-a7b8-4920-9b3c-4d5e6f7a8b9c"
	evt.Members = &MembersAddedPayload{ActorUserID: caOther, AddedCount: 3, MemberCount: 9}
	evt.Pin = &PinEventPayload{MessageID: "x", ActorUserID: caOther, Pinned: true}
	hubB.handleRemoteBusEvent(evt)

	messages := drain(client)
	if len(messages) != 1 {
		t.Fatalf("got %d message(s), want 1", len(messages))
	}
	body := string(messages[0])
	for _, leak := range []string{caOther, "members", "pin", "message_id", "display_name", "email"} {
		if strings.Contains(body, leak) {
			t.Fatalf("remote payload carried %q: %s", leak, body)
		}
	}
}

// A bus that refuses the publish must not break the operation: the membership
// is already committed and local sessions were already told.
func TestConversationAvailableSurvivesABusPublishFailure(t *testing.T) {
	hub := newTestHub(availableTestAuthorizer{})
	hub.instanceID = "pod-a"
	hub.bus = &fakeBus{publishErr: errors.New("valkey unavailable")}
	local := registerForTest(hub, "c-local", caRecipient, caWorkspace)

	hub.PublishConversationAvailable(
		context.Background(), caWorkspace, TargetTypeChannel, caChannel, []string{caRecipient},
	)

	if got := len(drain(local)); got != 1 {
		t.Fatalf("local delivery got %d message(s), want 1 despite the bus failure", got)
	}
}
