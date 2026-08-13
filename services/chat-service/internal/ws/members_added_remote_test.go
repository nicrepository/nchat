package ws

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// members.added across instances (issue #398).
//
// The event is target-scoped: it goes to the people already subscribed to the
// conversation, telling them their view of it is stale. That makes its remote
// path the ordinary broadcast one — canonicalize, hand to the broadcast queue,
// re-check each subscriber's access at fan-out — and not the recipient-routed
// path conversation.available takes. These assert both halves: that the envelope
// survives the bus at all, and that surviving it grants nothing.

const (
	maWorkspace = "2b7d1c4e-8a3f-4d29-9e15-6c8b0a2d4f37"
	maChannel   = "8e4a2f61-3d5c-4b78-a920-1f6e3c5d7a84"
	maActor     = "4c9e7b23-6a1d-4f85-b307-2e8d5a1c9f60"
	maMember    = "6d3f8a15-9c2e-4b41-8f76-3a5e1d7c2b98"
	maOutsider  = "7a1c5e93-2b8d-4f06-9c34-5e7a2d9b1f48"
)

// membersAddedEvent is the envelope a sibling pod puts on the bus.
func membersAddedEvent() Event {
	return Event{
		SchemaVersion: CurrentEventSchemaVersion, Type: EventTypeMembersAdded,
		WorkspaceID: maWorkspace, TargetType: TargetTypeChannel, TargetID: maChannel,
		Members: &MembersAddedPayload{
			ActorUserID: maActor, AddedCount: 2, MemberCount: 7,
		},
		EventID:          "9f2e5a71-4c3b-4d18-8a06-2b7e9c1d5f43",
		SourceInstanceID: "instance-B",
		CreatedAt:        time.Now().UTC(),
	}
}

// ── Canonicalization ────────────────────────────────────────────────────────

func TestCanonicalizeRemoteMembersAddedIsAccepted(t *testing.T) {
	evt := membersAddedEvent()
	// Upper-case IDs on the wire must come back canonicalized, like every other
	// remote event's.
	evt.WorkspaceID = strings.ToUpper(maWorkspace)
	evt.TargetID = strings.ToUpper(maChannel)
	evt.Members.ActorUserID = strings.ToUpper(maActor)

	canonical, ok := canonicalizeRemoteEvent(evt)
	if !ok {
		t.Fatal("a valid members.added must canonicalize; without this the event never leaves the bus")
	}
	if canonical.Type != EventTypeMembersAdded {
		t.Fatalf("Type = %q, want %q", canonical.Type, EventTypeMembersAdded)
	}
	if canonical.WorkspaceID != maWorkspace || canonical.TargetID != maChannel {
		t.Fatalf("scope = %s/%s, want the canonicalized envelope", canonical.WorkspaceID, canonical.TargetID)
	}
	if canonical.TargetType != TargetTypeChannel {
		t.Fatalf("TargetType = %q", canonical.TargetType)
	}
	if canonical.Members == nil {
		t.Fatal("the counts payload must survive: it is what corrects a remote subscriber's counter")
	}
	if canonical.Members.ActorUserID != maActor {
		t.Fatalf("ActorUserID = %q, want lowercase canonical form", canonical.Members.ActorUserID)
	}
	if canonical.Members.AddedCount != 2 || canonical.Members.MemberCount != 7 {
		t.Fatalf("counts = %+v", canonical.Members)
	}
	// It routes by target, never by recipient.
	if canonical.RecipientUserID != "" {
		t.Fatalf("RecipientUserID = %q; members.added must stay target-scoped", canonical.RecipientUserID)
	}
}

func TestCanonicalizeRemoteMembersAddedRejectsMalformedEnvelopes(t *testing.T) {
	tests := map[string]func(*Event){
		"no payload":            func(e *Event) { e.Members = nil },
		"actor not a uuid":      func(e *Event) { e.Members.ActorUserID = "not-a-uuid" },
		"no actor":              func(e *Event) { e.Members.ActorUserID = "" },
		"workspace not a uuid":  func(e *Event) { e.WorkspaceID = "nope" },
		"no workspace":          func(e *Event) { e.WorkspaceID = "" },
		"target not a uuid":     func(e *Event) { e.TargetID = "nope" },
		"no target":             func(e *Event) { e.TargetID = "" },
		"unknown target type":   func(e *Event) { e.TargetType = "elsewhere" },
		"no target type":        func(e *Event) { e.TargetType = "" },
		"bad event id":          func(e *Event) { e.EventID = "nope" },
		"no source instance":    func(e *Event) { e.SourceInstanceID = "" },
		"unknown schema":        func(e *Event) { e.SchemaVersion = 99 },
		"zero added":            func(e *Event) { e.Members.AddedCount = 0 },
		"negative added":        func(e *Event) { e.Members.AddedCount = -1 },
		"total below added":     func(e *Event) { e.Members.MemberCount = 1 },
		"carries a recipient":   func(e *Event) { e.RecipientUserID = maMember },
		"carries a message":     func(e *Event) { e.MessageID = maChannel },
		"carries a pin payload": func(e *Event) { e.Pin = &PinEventPayload{MessageID: maChannel, ActorUserID: maActor} },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			evt := membersAddedEvent()
			mutate(&evt)
			canonical, ok := canonicalizeRemoteEvent(evt)
			switch name {
			case "carries a pin payload":
				// A stray payload from another event type is stripped rather than
				// fatal; what matters is that it cannot reach a client.
				if !ok {
					t.Fatal("a stray pin payload must be stripped, not reject the event")
				}
				if canonical.Pin != nil {
					t.Fatal("a pin payload rode along on members.added")
				}
			default:
				if ok {
					t.Fatalf("a malformed members.added was accepted: %+v", canonical)
				}
			}
		})
	}
}

// Nothing a producer attaches may add identities to what subscribers receive.
func TestCanonicalizeRemoteMembersAddedStripsForeignPayloads(t *testing.T) {
	evt := membersAddedEvent()
	evt.Payload = &MessagePayload{ID: maChannel, BodyText: "sensitive message body", SenderID: maOutsider}
	evt.MessageUpdate = &MessageUpdatedPayload{MessageID: maChannel, Body: "sensitive edit"}
	evt.Reaction = &ReactionEventPayload{MessageID: maChannel, ActorUserID: maActor, Emoji: "👍"}

	canonical, ok := canonicalizeRemoteEvent(evt)
	if !ok {
		t.Fatal("expected the event to canonicalize")
	}
	if canonical.Payload != nil || canonical.MessageUpdate != nil || canonical.Reaction != nil {
		t.Fatalf("foreign payloads survived canonicalization: %+v", canonical)
	}
	data, err := json.Marshal(canonical)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, leak := range []string{"sensitive", maOutsider, "body_text", "display_name", "email"} {
		if strings.Contains(string(data), leak) {
			t.Fatalf("canonical members.added carried %q: %s", leak, data)
		}
	}
}

// The payload names counts and an actor, never who was added — so there is no
// list of member IDs for a remote node to render or a subscriber to harvest.
func TestRemoteMembersAddedCarriesNoMemberIdentities(t *testing.T) {
	canonical, ok := canonicalizeRemoteEvent(membersAddedEvent())
	if !ok {
		t.Fatal("expected the event to canonicalize")
	}
	data, err := json.Marshal(canonical)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, leak := range []string{maMember, maOutsider, "user_ids", "added_user_ids", "participants", "email"} {
		if strings.Contains(string(data), leak) {
			t.Fatalf("members.added carried %q: %s", leak, data)
		}
	}
}

// ── Multi-instance delivery ─────────────────────────────────────────────────

// subscribeForTest wires an existing test client to a target the way a granted
// subscription would, without running the authorization round trip.
func subscribeForTest(h *Hub, c *Client, targetType TargetType, targetID string) {
	key := targetKey{workspaceID: c.workspaceID, targetType: targetType, targetID: targetID}.String()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.subs[key] == nil {
		h.subs[key] = make(map[string]struct{})
	}
	h.nextSubscriptionGeneration++
	h.subs[key][c.id] = struct{}{}
	h.clientSubs[c.id][key] = struct{}{}
	h.subscriptionGenerations[c.id][key] = h.nextSubscriptionGeneration
}

// startHubLoop gives a white-box test hub the coordination goroutine that
// handleBroadcast depends on when it revokes a subscription, and stops it at
// cleanup. Broadcasts still run synchronously on the caller's goroutine below;
// only the revoke round trip needs a live run loop.
func startHubLoop(t *testing.T, h *Hub) {
	t.Helper()
	h.register = make(chan registerReq, 8)
	h.unregister = make(chan *Client, 8)
	h.subReq = make(chan subscribeReq, 8)
	h.revokeReq = make(chan revokeSubscriptionReq, 8)
	h.quit = make(chan struct{})
	h.done = make(chan struct{})
	go h.run()
	t.Cleanup(func() {
		close(h.quit)
		<-h.done
	})
}

// deliverRemoteBroadcast runs what the dispatcher and a worker would do for
// whatever handleRemoteBusEvent queued, synchronously — so the assertions below
// need no sleeps and no polling for a worker to catch up.
func deliverRemoteBroadcast(t *testing.T, h *Hub) int {
	t.Helper()
	delivered := 0
	for {
		select {
		case req := <-h.remoteBcast:
			h.handleBroadcast(req)
			delivered++
		default:
			return delivered
		}
	}
}

// The headline case: pod A performs the write, the subscriber's socket is on
// pod B, and B must deliver. Before the fix the envelope was dropped at
// canonicalization and this subscriber never learned the roster had changed.
func TestMembersAddedCrossesInstances(t *testing.T) {
	auth := &recordingAuthorizer{allowed: true}
	_, hubB, bus := twoHubsWithAuthorizer(t, auth)
	startHubLoop(t, hubB)
	subscriber := registerForTest(hubB, "c-remote", maMember, maWorkspace)
	subscribeForTest(hubB, subscriber, TargetTypeChannel, maChannel)

	bus.inject(membersAddedEvent())
	if queued := deliverRemoteBroadcast(t, hubB); queued != 1 {
		t.Fatalf("%d remote broadcast(s) queued, want 1", queued)
	}

	messages := drain(subscriber)
	if len(messages) != 1 {
		t.Fatalf("remote subscriber got %d message(s), want 1", len(messages))
	}
	var evt Event
	if err := json.Unmarshal(messages[0], &evt); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if evt.Type != EventTypeMembersAdded || evt.TargetID != maChannel {
		t.Fatalf("wrong event delivered: %+v", evt)
	}
	if evt.Members == nil || evt.Members.MemberCount != 7 {
		t.Fatalf("counts lost across the bus: %+v", evt.Members)
	}
}

// A subscription is not a standing permission. Access is re-derived per
// subscriber at fan-out, exactly as for a local broadcast.
func TestRemoteMembersAddedReauthorizesEachSubscriber(t *testing.T) {
	auth := &recordingAuthorizer{allowed: true}
	_, hubB, bus := twoHubsWithAuthorizer(t, auth)
	startHubLoop(t, hubB)
	subscriber := registerForTest(hubB, "c-remote", maMember, maWorkspace)
	subscribeForTest(hubB, subscriber, TargetTypeChannel, maChannel)

	bus.inject(membersAddedEvent())
	deliverRemoteBroadcast(t, hubB)

	calls := auth.recorded()
	if len(calls) != 1 {
		t.Fatalf("authorizer consulted %d time(s), want 1 per subscriber", len(calls))
	}
	want := authorizerCall{maMember, maWorkspace, TargetTypeChannel, maChannel}
	if calls[0] != want {
		t.Fatalf("authorized %+v, want %+v", calls[0], want)
	}
	if got := len(drain(subscriber)); got != 1 {
		t.Fatalf("authorized subscriber got %d, want 1", got)
	}
}

func TestRemoteMembersAddedIsNotDeliveredToAnUnauthorizedSubscriber(t *testing.T) {
	auth := &recordingAuthorizer{allowed: false}
	_, hubB, bus := twoHubsWithAuthorizer(t, auth)
	startHubLoop(t, hubB)
	subscriber := registerForTest(hubB, "c-remote", maMember, maWorkspace)
	subscribeForTest(hubB, subscriber, TargetTypeChannel, maChannel)

	bus.inject(membersAddedEvent())
	deliverRemoteBroadcast(t, hubB)

	if got := len(drain(subscriber)); got != 0 {
		t.Fatalf("a subscriber whose access was revoked received %d frame(s)", got)
	}
}

// A transient failure skips delivery rather than delivering anyway.
func TestRemoteMembersAddedIsNotDeliveredWhenTheAuthorizerFails(t *testing.T) {
	auth := &recordingAuthorizer{allowed: true, err: errors.New("database unavailable")}
	_, hubB, bus := twoHubsWithAuthorizer(t, auth)
	startHubLoop(t, hubB)
	subscriber := registerForTest(hubB, "c-remote", maMember, maWorkspace)
	subscribeForTest(hubB, subscriber, TargetTypeChannel, maChannel)

	bus.inject(membersAddedEvent())
	deliverRemoteBroadcast(t, hubB)

	if got := len(drain(subscriber)); got != 0 {
		t.Fatalf("an authorizer error delivered %d frame(s); it must fail closed", got)
	}
}

// The event is scoped to the conversation, not to the workspace: someone
// connected to the same pod who does not subscribe to it learns nothing.
func TestRemoteMembersAddedDoesNotReachNonSubscribers(t *testing.T) {
	auth := &recordingAuthorizer{allowed: true}
	_, hubB, bus := twoHubsWithAuthorizer(t, auth)
	startHubLoop(t, hubB)
	subscriber := registerForTest(hubB, "c-remote", maMember, maWorkspace)
	subscribeForTest(hubB, subscriber, TargetTypeChannel, maChannel)
	// Registered on the same pod and in the same workspace, but not subscribed.
	bystander := registerForTest(hubB, "c-bystander", maOutsider, maWorkspace)

	bus.inject(membersAddedEvent())
	deliverRemoteBroadcast(t, hubB)

	if got := len(drain(subscriber)); got != 1 {
		t.Fatalf("subscriber got %d, want 1", got)
	}
	if got := len(drain(bystander)); got != 0 {
		t.Fatalf("a non-subscriber received %d frame(s); this is not a workspace broadcast", got)
	}
}

// A subscription in another workspace keys to a different target entirely.
func TestRemoteMembersAddedIsWorkspaceScoped(t *testing.T) {
	auth := &recordingAuthorizer{allowed: true}
	_, hubB, bus := twoHubsWithAuthorizer(t, auth)
	startHubLoop(t, hubB)
	foreign := registerForTest(hubB, "c-foreign", maMember, "0d5f3a82-7e1c-4b96-8d40-2c9a6f4e1b73")
	subscribeForTest(hubB, foreign, TargetTypeChannel, maChannel)

	bus.inject(membersAddedEvent())
	deliverRemoteBroadcast(t, hubB)

	if got := len(drain(foreign)); got != 0 {
		t.Fatalf("a subscriber in another workspace received %d frame(s)", got)
	}
}

// Every live session of a subscribed user is a separate subscription and each
// gets one frame — the same contract as any other target-scoped event.
func TestRemoteMembersAddedReachesEverySubscribedSession(t *testing.T) {
	auth := &recordingAuthorizer{allowed: true}
	_, hubB, bus := twoHubsWithAuthorizer(t, auth)
	startHubLoop(t, hubB)
	first := registerForTest(hubB, "c-1", maMember, maWorkspace)
	second := registerForTest(hubB, "c-2", maMember, maWorkspace)
	subscribeForTest(hubB, first, TargetTypeChannel, maChannel)
	subscribeForTest(hubB, second, TargetTypeChannel, maChannel)

	bus.inject(membersAddedEvent())
	deliverRemoteBroadcast(t, hubB)

	if got := len(drain(first)); got != 1 {
		t.Fatalf("first session got %d, want 1", got)
	}
	if got := len(drain(second)); got != 1 {
		t.Fatalf("second session got %d, want 1", got)
	}
}

// The originating pod delivered locally already; its own copy coming back off
// the bus must not queue a second broadcast.
func TestRemoteMembersAddedSelfEchoIsSuppressed(t *testing.T) {
	auth := &recordingAuthorizer{allowed: true}
	_, hubB, bus := twoHubsWithAuthorizer(t, auth)
	startHubLoop(t, hubB)
	subscriber := registerForTest(hubB, "c-remote", maMember, maWorkspace)
	subscribeForTest(hubB, subscriber, TargetTypeChannel, maChannel)

	evt := membersAddedEvent()
	evt.SourceInstanceID = hubB.presenceInstanceID
	bus.inject(evt)

	if queued := deliverRemoteBroadcast(t, hubB); queued != 0 {
		t.Fatalf("a self-echo queued %d broadcast(s)", queued)
	}
	if got := len(drain(subscriber)); got != 0 {
		t.Fatalf("a self-echo delivered %d frame(s)", got)
	}
}

// An event that came off the bus must never go back onto it.
func TestRemoteMembersAddedIsNotRepublished(t *testing.T) {
	auth := &recordingAuthorizer{allowed: true}
	_, hubB, bus := twoHubsWithAuthorizer(t, auth)
	startHubLoop(t, hubB)
	subscriber := registerForTest(hubB, "c-remote", maMember, maWorkspace)
	subscribeForTest(hubB, subscriber, TargetTypeChannel, maChannel)

	bus.inject(membersAddedEvent())
	deliverRemoteBroadcast(t, hubB)

	if got := bus.publishCount(); got != 0 {
		t.Fatalf("a remote members.added was republished %d time(s)", got)
	}
}

// A subscriber that disconnects while the event is in flight must be skipped,
// not panicked over.
func TestRemoteMembersAddedSurvivesAConcurrentDisconnect(t *testing.T) {
	auth := &recordingAuthorizer{allowed: true}
	_, hubB, bus := twoHubsWithAuthorizer(t, auth)
	startHubLoop(t, hubB)
	subscriber := registerForTest(hubB, "c-remote", maMember, maWorkspace)
	subscribeForTest(hubB, subscriber, TargetTypeChannel, maChannel)

	bus.inject(membersAddedEvent())
	// Gone between queueing and fan-out — the generation check is what makes
	// this a skip rather than a write to a dead client.
	hubB.dropClient(subscriber)

	deliverRemoteBroadcast(t, hubB)

	if got := len(drain(subscriber)); got != 0 {
		t.Fatalf("a dropped client received %d frame(s)", got)
	}
}

// PublishMembersAdded puts the event on the bus with its payload intact — that
// is what a sibling pod needs in order to have anything to canonicalize.
func TestPublishMembersAddedReachesTheBus(t *testing.T) {
	hub := newTestHub(availableTestAuthorizer{})
	hub.instanceID = "pod-a"
	bus := &fakeBus{}
	hub.bus = bus
	hub.bcast = make(chan broadcastReq, 4)
	hub.quit = make(chan struct{})

	hub.PublishMembersAdded(
		context.Background(), maWorkspace, TargetTypeChannel, maChannel, maActor, 2, 7,
	)

	published, ok := bus.lastPublished()
	if !ok {
		t.Fatal("members.added never reached the bus; remote pods would never see it")
	}
	if published.Type != EventTypeMembersAdded || published.SourceInstanceID != hub.presenceInstanceID {
		t.Fatalf("published %+v", published)
	}
	// What A publishes must be something B accepts: the two halves of the round
	// trip asserted together, so a future envelope change cannot break one side
	// silently.
	if _, accepted := canonicalizeRemoteEvent(published); !accepted {
		t.Fatal("what this hub publishes is rejected by the remote canonicalizer")
	}
}
