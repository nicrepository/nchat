package ws

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// ── helpers ──────────────────────────────────────────────────────────────────

// allowAllAuthorizer permits every target. These tests are about presence; the
// authorization of a subscribe, and its re-check before the snapshot, have their
// own tests below and in hub_test.go.
type allowAllAuthorizer struct{}

func (allowAllAuthorizer) CanAccess(_ context.Context, _, _ string, _ TargetType, _ string) (bool, error) {
	return true, nil
}

// newPresenceTestHub is newTestHub with a tracker attached and a broadcast
// queue to read back. No goroutine is started: the tests drain the pending set
// themselves, so every assertion is about what the hub decided rather than
// about timing.
func newPresenceTestHub(auth SubscriptionAuthorizer, tracker *PresenceTracker) *Hub {
	h := newTestHub(auth)
	h.presence = tracker
	h.bcast = make(chan broadcastReq, 512)
	h.attachPresenceObserver()
	return h
}

// drainPresenceEvents runs every pending change through the fan-out and returns
// the presence events it published. It mirrors runPresenceFanout without the
// goroutine, so every assertion is about what the hub decided rather than about
// timing.
func drainPresenceEvents(t *testing.T, h *Hub) []Event {
	t.Helper()
	for _, change := range h.takePresenceChanges() {
		h.publishPresence(change)
	}

	events := make([]Event, 0, 4)
	for {
		select {
		case req := <-h.bcast:
			events = append(events, req.event)
		default:
			return events
		}
	}
}

// subscribeInHubState puts a subscription into hub state directly, as
// handleSubscribe would after an authorization pass.
func subscribeInHubState(t *testing.T, h *Hub, c *Client, targetType TargetType, targetID string) string {
	t.Helper()
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
	return key
}

// takeSnapshot returns the presence snapshot frames queued for a client.
func takeSnapshots(t *testing.T, c *Client) []PresenceSnapshotResponse {
	t.Helper()
	snapshots := make([]PresenceSnapshotResponse, 0, 2)
	for {
		select {
		case data := <-c.outbox:
			var frame struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(data, &frame); err != nil {
				t.Fatalf("decode outbox frame: %v", err)
			}
			if frame.Type != PresenceSnapshotType {
				continue
			}
			var snapshot PresenceSnapshotResponse
			if err := json.Unmarshal(data, &snapshot); err != nil {
				t.Fatalf("decode presence snapshot: %v", err)
			}
			snapshots = append(snapshots, snapshot)
		default:
			return snapshots
		}
	}
}

func presenceStatesFor(events []Event, userID string) []string {
	states := make([]string, 0, len(events))
	for _, evt := range events {
		if evt.Type != EventTypePresenceUpdated || evt.Presence == nil {
			continue
		}
		if evt.Presence.UserID != userID {
			continue
		}
		states = append(states, evt.Presence.State)
	}
	return states
}

// ── snapshot on subscribe ────────────────────────────────────────────────────

func TestPresence_Subscribe_SendsSnapshotOfUsersAlreadyInTarget(t *testing.T) {
	clk := newFakeClock(time.Now())
	tracker := newTestPresenceTracker(5*time.Minute, clk)
	h := newPresenceTestHub(allowAllAuthorizer{}, tracker)

	incumbent := newClient("c-incumbent", "user-incumbent", "ws-1", &fakeSender{})
	registerInHub(t, h, incumbent)
	tracker.Connect(incumbent.workspaceID, incumbent.userID, incumbent.id)
	subscribeInHubState(t, h, incumbent, TargetTypeChannel, "chan-1")

	joiner := newClient("c-joiner", "user-joiner", "ws-1", &fakeSender{})
	registerInHub(t, h, joiner)
	tracker.Connect(joiner.workspaceID, joiner.userID, joiner.id)
	subscribeInHubState(t, h, joiner, TargetTypeChannel, "chan-1")

	h.handleSubscribed(joiner, TargetTypeChannel, "chan-1", 0)

	snapshots := takeSnapshots(t, joiner)
	if len(snapshots) != 1 {
		t.Fatalf("expected exactly one snapshot, got %d", len(snapshots))
	}
	snapshot := snapshots[0]
	if snapshot.TargetType != TargetTypeChannel || snapshot.TargetID != "chan-1" {
		t.Fatalf("snapshot addressed to %s/%s", snapshot.TargetType, snapshot.TargetID)
	}
	if len(snapshot.Users) != 2 {
		t.Fatalf("expected both subscribers in the snapshot, got %+v", snapshot.Users)
	}
	for _, user := range snapshot.Users {
		if user.State != string(PresenceOnline) {
			t.Fatalf("expected online in snapshot, got %q for %q", user.State, user.UserID)
		}
		if user.UpdatedAt == "" {
			t.Fatalf("snapshot entry for %q carries no updated_at", user.UserID)
		}
	}
}

func TestPresence_Subscribe_SnapshotExcludesUsersFromOtherTargets(t *testing.T) {
	clk := newFakeClock(time.Now())
	tracker := newTestPresenceTracker(5*time.Minute, clk)
	h := newPresenceTestHub(allowAllAuthorizer{}, tracker)

	elsewhere := newClient("c-elsewhere", "user-elsewhere", "ws-1", &fakeSender{})
	registerInHub(t, h, elsewhere)
	tracker.Connect(elsewhere.workspaceID, elsewhere.userID, elsewhere.id)
	subscribeInHubState(t, h, elsewhere, TargetTypeChannel, "chan-other")

	joiner := newClient("c-joiner", "user-joiner", "ws-1", &fakeSender{})
	registerInHub(t, h, joiner)
	tracker.Connect(joiner.workspaceID, joiner.userID, joiner.id)
	subscribeInHubState(t, h, joiner, TargetTypeChannel, "chan-1")

	h.handleSubscribed(joiner, TargetTypeChannel, "chan-1", 0)

	snapshots := takeSnapshots(t, joiner)
	if len(snapshots) != 1 {
		t.Fatalf("expected exactly one snapshot, got %d", len(snapshots))
	}
	for _, user := range snapshots[0].Users {
		if user.UserID == "user-elsewhere" {
			t.Fatalf("snapshot leaked a user from an unrelated target: %+v", snapshots[0].Users)
		}
	}
}

// A workspace is a hard boundary for presence: two users in the same-named
// target but different workspaces are different targets, so neither can appear
// in the other's snapshot.
func TestPresence_Subscribe_SnapshotIsWorkspaceScoped(t *testing.T) {
	clk := newFakeClock(time.Now())
	tracker := newTestPresenceTracker(5*time.Minute, clk)
	h := newPresenceTestHub(allowAllAuthorizer{}, tracker)

	foreign := newClient("c-foreign", "user-foreign", "ws-2", &fakeSender{})
	registerInHub(t, h, foreign)
	tracker.Connect(foreign.workspaceID, foreign.userID, foreign.id)
	subscribeInHubState(t, h, foreign, TargetTypeChannel, "chan-1")

	local := newClient("c-local", "user-local", "ws-1", &fakeSender{})
	registerInHub(t, h, local)
	tracker.Connect(local.workspaceID, local.userID, local.id)
	subscribeInHubState(t, h, local, TargetTypeChannel, "chan-1")

	h.handleSubscribed(local, TargetTypeChannel, "chan-1", 0)

	snapshots := takeSnapshots(t, local)
	if len(snapshots) != 1 {
		t.Fatalf("expected exactly one snapshot, got %d", len(snapshots))
	}
	if len(snapshots[0].Users) != 1 || snapshots[0].Users[0].UserID != "user-local" {
		t.Fatalf("snapshot crossed a workspace boundary: %+v", snapshots[0].Users)
	}
}

func TestPresence_Subscribe_AnnouncesSubscriberToTarget(t *testing.T) {
	clk := newFakeClock(time.Now())
	tracker := newTestPresenceTracker(5*time.Minute, clk)
	h := newPresenceTestHub(allowAllAuthorizer{}, tracker)

	joiner := newClient("c-joiner", "user-joiner", "ws-1", &fakeSender{})
	registerInHub(t, h, joiner)
	tracker.Connect(joiner.workspaceID, joiner.userID, joiner.id)
	key := subscribeInHubState(t, h, joiner, TargetTypeDM, "dm-1")

	h.handleSubscribed(joiner, TargetTypeDM, "dm-1", 0)

	events := drainPresenceEvents(t, h)
	if got := presenceStatesFor(events, "user-joiner"); len(got) != 1 || got[0] != string(PresenceOnline) {
		t.Fatalf("expected one online announcement, got %v", got)
	}
	if events[0].TargetType != TargetTypeDM || events[0].TargetID != "dm-1" {
		t.Fatalf("announcement addressed to %s/%s, want the subscribed target", events[0].TargetType, events[0].TargetID)
	}
	if events[0].WorkspaceID != "ws-1" {
		t.Fatalf("announcement workspace %q, want ws-1", events[0].WorkspaceID)
	}
	if events[0].Presence.UpdatedAt == "" {
		t.Fatalf("announcement carries no updated_at")
	}
	if _, ok := parseTargetKey(key); !ok {
		t.Fatalf("target key %q is not parseable", key)
	}
}

// ── multi-device ─────────────────────────────────────────────────────────────

func TestPresence_ClosingOneOfTwoConnections_PublishesNothing(t *testing.T) {
	clk := newFakeClock(time.Now())
	tracker := newTestPresenceTracker(5*time.Minute, clk)
	h := newPresenceTestHub(allowAllAuthorizer{}, tracker)

	first := newClient("c-1", "user-1", "ws-1", &fakeSender{})
	second := newClient("c-2", "user-1", "ws-1", &fakeSender{})
	for _, c := range []*Client{first, second} {
		registerInHub(t, h, c)
		tracker.Connect(c.workspaceID, c.userID, c.id)
		subscribeInHubState(t, h, c, TargetTypeChannel, "chan-1")
	}

	h.dropClient(first)

	if events := drainPresenceEvents(t, h); len(events) != 0 {
		t.Fatalf("closing one of two sessions published %d event(s): %+v", len(events), events)
	}
	if got := tracker.Status("ws-1", "user-1"); got != PresenceOnline {
		t.Fatalf("expected the user to stay online, got %q", got)
	}

	h.dropClient(second)

	events := drainPresenceEvents(t, h)
	if got := presenceStatesFor(events, "user-1"); len(got) != 1 || got[0] != string(PresenceOffline) {
		t.Fatalf("expected one offline event after the last session, got %v", got)
	}
}

// An abrupt drop is the case the whole design has to survive: the client is
// already out of hub state by the time presence says "offline", so the event
// has to be addressed with the rooms captured before removal.
func TestPresence_LastConnectionDrop_PublishesOfflineToItsRooms(t *testing.T) {
	clk := newFakeClock(time.Now())
	tracker := newTestPresenceTracker(5*time.Minute, clk)
	h := newPresenceTestHub(allowAllAuthorizer{}, tracker)

	leaving := newClient("c-leaving", "user-leaving", "ws-1", &fakeSender{})
	registerInHub(t, h, leaving)
	tracker.Connect(leaving.workspaceID, leaving.userID, leaving.id)
	subscribeInHubState(t, h, leaving, TargetTypeChannel, "chan-1")
	subscribeInHubState(t, h, leaving, TargetTypeDM, "dm-1")

	h.dropClient(leaving)

	events := drainPresenceEvents(t, h)
	if len(events) != 2 {
		t.Fatalf("expected one offline event per room, got %d: %+v", len(events), events)
	}
	seen := map[string]bool{}
	for _, evt := range events {
		if evt.Presence == nil || evt.Presence.State != string(PresenceOffline) {
			t.Fatalf("expected offline, got %+v", evt.Presence)
		}
		seen[string(evt.TargetType)+":"+evt.TargetID] = true
	}
	if !seen["channel:chan-1"] || !seen["dm:dm-1"] {
		t.Fatalf("offline was not addressed to both rooms: %v", seen)
	}
}

// ── away / back ──────────────────────────────────────────────────────────────

func TestPresence_AwayTransition_PublishesToLiveSubscriptions(t *testing.T) {
	awayTimeout := 5 * time.Minute
	clk := newFakeClock(time.Now())
	tracker := newTestPresenceTracker(awayTimeout, clk)
	h := newPresenceTestHub(allowAllAuthorizer{}, tracker)

	idle := newClient("c-idle", "user-idle", "ws-1", &fakeSender{})
	registerInHub(t, h, idle)
	tracker.Connect(idle.workspaceID, idle.userID, idle.id)
	subscribeInHubState(t, h, idle, TargetTypeChannel, "chan-1")

	clk.Advance(awayTimeout + time.Second)
	tracker.checkAway()

	events := drainPresenceEvents(t, h)
	if got := presenceStatesFor(events, "user-idle"); len(got) != 1 || got[0] != string(PresenceAway) {
		t.Fatalf("expected one away event, got %v", got)
	}

	// Activity brings them back, and that transition is published too.
	if err := h.handleClientMessage(context.Background(), idle, ClientMessage{Type: ClientMessageTypePing}); err != nil {
		t.Fatalf("ping: %v", err)
	}
	events = drainPresenceEvents(t, h)
	if got := presenceStatesFor(events, "user-idle"); len(got) != 1 || got[0] != string(PresenceOnline) {
		t.Fatalf("expected one online event after activity, got %v", got)
	}
}

func TestPresence_ActivityWhileOnline_PublishesNothing(t *testing.T) {
	clk := newFakeClock(time.Now())
	tracker := newTestPresenceTracker(5*time.Minute, clk)
	h := newPresenceTestHub(allowAllAuthorizer{}, tracker)

	c := newClient("c-1", "user-1", "ws-1", &fakeSender{})
	registerInHub(t, h, c)
	tracker.Connect(c.workspaceID, c.userID, c.id)
	subscribeInHubState(t, h, c, TargetTypeChannel, "chan-1")

	for range 5 {
		if err := h.handleClientMessage(context.Background(), c, ClientMessage{Type: ClientMessageTypePing}); err != nil {
			t.Fatalf("ping: %v", err)
		}
	}

	if events := drainPresenceEvents(t, h); len(events) != 0 {
		t.Fatalf("activity on an online user published %d event(s)", len(events))
	}
}

// A second device that is still active keeps the user online: away requires
// every connection to be idle.
func TestPresence_ActiveSecondDevice_KeepsUserOnline(t *testing.T) {
	awayTimeout := 5 * time.Minute
	clk := newFakeClock(time.Now())
	tracker := newTestPresenceTracker(awayTimeout, clk)
	h := newPresenceTestHub(allowAllAuthorizer{}, tracker)

	idle := newClient("c-idle", "user-1", "ws-1", &fakeSender{})
	active := newClient("c-active", "user-1", "ws-1", &fakeSender{})
	for _, c := range []*Client{idle, active} {
		registerInHub(t, h, c)
		tracker.Connect(c.workspaceID, c.userID, c.id)
		subscribeInHubState(t, h, c, TargetTypeChannel, "chan-1")
	}

	clk.Advance(awayTimeout + time.Second)
	tracker.RecordActivity(active.workspaceID, active.userID, active.id)
	tracker.checkAway()

	if got := tracker.Status("ws-1", "user-1"); got != PresenceOnline {
		t.Fatalf("expected online while one device is active, got %q", got)
	}
	if events := drainPresenceEvents(t, h); len(events) != 0 {
		t.Fatalf("expected no transition, got %+v", events)
	}
}

// Activity is credited to the connection that produced it and to nobody else:
// there is no field a client could use to name another user.
func TestPresence_ActivityOnlyAffectsTheAuthenticatedConnection(t *testing.T) {
	awayTimeout := 5 * time.Minute
	clk := newFakeClock(time.Now())
	tracker := newTestPresenceTracker(awayTimeout, clk)
	h := newPresenceTestHub(allowAllAuthorizer{}, tracker)

	actor := newClient("c-actor", "user-actor", "ws-1", &fakeSender{})
	bystander := newClient("c-bystander", "user-bystander", "ws-1", &fakeSender{})
	for _, c := range []*Client{actor, bystander} {
		registerInHub(t, h, c)
		tracker.Connect(c.workspaceID, c.userID, c.id)
		subscribeInHubState(t, h, c, TargetTypeChannel, "chan-1")
	}

	clk.Advance(awayTimeout + time.Second)
	tracker.checkAway()
	drainPresenceEvents(t, h)

	// A message that tries to name someone else. TargetUserID is a call field;
	// nothing in the presence path reads it.
	msg := ClientMessage{Type: ClientMessageTypePing, TargetUserID: "user-bystander"}
	if err := h.handleClientMessage(context.Background(), actor, msg); err != nil {
		t.Fatalf("ping: %v", err)
	}

	if got := tracker.Status("ws-1", "user-actor"); got != PresenceOnline {
		t.Fatalf("expected the actor back online, got %q", got)
	}
	if got := tracker.Status("ws-1", "user-bystander"); got != PresenceAway {
		t.Fatalf("expected the bystander to stay away, got %q", got)
	}
	events := drainPresenceEvents(t, h)
	if got := presenceStatesFor(events, "user-bystander"); len(got) != 0 {
		t.Fatalf("a client changed another user's presence: %v", got)
	}
}

// ── fan-out addressing ───────────────────────────────────────────────────────

// One event per shared room, and never one for a room the user is not in.
func TestPresence_PublishAddressesEverySubscribedTargetOnce(t *testing.T) {
	clk := newFakeClock(time.Now())
	tracker := newTestPresenceTracker(5*time.Minute, clk)
	h := newPresenceTestHub(allowAllAuthorizer{}, tracker)

	// Two sessions of the same user, overlapping on one channel.
	first := newClient("c-1", "user-1", "ws-1", &fakeSender{})
	second := newClient("c-2", "user-1", "ws-1", &fakeSender{})
	registerInHub(t, h, first)
	registerInHub(t, h, second)
	subscribeInHubState(t, h, first, TargetTypeChannel, "chan-shared")
	subscribeInHubState(t, h, second, TargetTypeChannel, "chan-shared")
	subscribeInHubState(t, h, second, TargetTypeDM, "dm-1")

	// Somebody else's room, which must not be addressed.
	stranger := newClient("c-stranger", "user-stranger", "ws-1", &fakeSender{})
	registerInHub(t, h, stranger)
	subscribeInHubState(t, h, stranger, TargetTypeChannel, "chan-stranger")

	h.publishPresence(presenceChange{
		workspaceID: "ws-1", userID: "user-1", status: PresenceAway, at: clk.Now(), derive: true,
	})

	events := drainPresenceEvents(t, h)
	if len(events) != 2 {
		t.Fatalf("expected one event per distinct room, got %d: %+v", len(events), events)
	}
	for _, evt := range events {
		if evt.TargetID == "chan-stranger" {
			t.Fatalf("presence reached a room the user is not in")
		}
	}
}

// A key belonging to another workspace is refused even if it somehow reached
// the fan-out: workspace isolation does not depend on the caller being careful.
func TestPresence_PublishRefusesForeignWorkspaceKeys(t *testing.T) {
	clk := newFakeClock(time.Now())
	tracker := newTestPresenceTracker(5*time.Minute, clk)
	h := newPresenceTestHub(allowAllAuthorizer{}, tracker)

	foreignKey := targetKey{workspaceID: "ws-2", targetType: TargetTypeChannel, targetID: "chan-1"}.String()
	userKey := targetKey{workspaceID: "ws-1", targetType: TargetTypeUser, targetID: "user-9"}.String()

	h.publishPresence(presenceChange{
		workspaceID: "ws-1", userID: "user-1", status: PresenceOnline, at: clk.Now(),
		keys: []string{foreignKey, userKey, "not-a-key"},
	})

	if events := drainPresenceEvents(t, h); len(events) != 0 {
		t.Fatalf("expected nothing published, got %+v", events)
	}
}

// ── remote events ────────────────────────────────────────────────────────────

func TestCanonicalizeRemoteEvent_Presence(t *testing.T) {
	base := func() Event {
		return Event{
			SchemaVersion: CurrentEventSchemaVersion,
			Type:          EventTypePresenceUpdated,
			WorkspaceID:   "11111111-1111-1111-1111-111111111111",
			TargetType:    TargetTypeChannel,
			TargetID:      "22222222-2222-2222-2222-222222222222",
			Presence: &PresencePayload{
				UserID:    "33333333-3333-3333-3333-333333333333",
				State:     string(PresenceAway),
				UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
			},
			EventID:          "44444444-4444-4444-4444-444444444444",
			SourceInstanceID: "other-instance",
			CreatedAt:        time.Now().UTC(),
		}
	}

	t.Run("accepts a well-formed event", func(t *testing.T) {
		got, ok := canonicalizeRemoteEvent(base())
		if !ok {
			t.Fatal("expected a well-formed presence event to be accepted")
		}
		if got.Presence == nil || got.Presence.State != string(PresenceAway) {
			t.Fatalf("presence payload was not preserved: %+v", got.Presence)
		}
	})

	t.Run("canonicalizes the subject id to lowercase", func(t *testing.T) {
		evt := base()
		evt.Presence.UserID = "33333333-3333-3333-3333-333333333333"
		got, ok := canonicalizeRemoteEvent(evt)
		if !ok {
			t.Fatal("expected acceptance")
		}
		if got.Presence.UserID != "33333333-3333-3333-3333-333333333333" {
			t.Fatalf("user id not canonical: %q", got.Presence.UserID)
		}
	})

	t.Run("rejects an unknown state", func(t *testing.T) {
		evt := base()
		evt.Presence.State = "busy"
		if _, ok := canonicalizeRemoteEvent(evt); ok {
			t.Fatal("expected a state outside the contract to be dropped")
		}
	})

	t.Run("rejects a non-uuid subject", func(t *testing.T) {
		evt := base()
		evt.Presence.UserID = "someone"
		if _, ok := canonicalizeRemoteEvent(evt); ok {
			t.Fatal("expected a non-uuid subject to be dropped")
		}
	})

	t.Run("rejects a user target", func(t *testing.T) {
		evt := base()
		evt.TargetType = TargetTypeUser
		if _, ok := canonicalizeRemoteEvent(evt); ok {
			t.Fatal("expected a user-targeted presence event to be dropped")
		}
	})

	t.Run("rejects a missing payload", func(t *testing.T) {
		evt := base()
		evt.Presence = nil
		if _, ok := canonicalizeRemoteEvent(evt); ok {
			t.Fatal("expected a presence event with no payload to be dropped")
		}
	})

	t.Run("rejects a recipient", func(t *testing.T) {
		evt := base()
		evt.RecipientUserID = "55555555-5555-5555-5555-555555555555"
		if _, ok := canonicalizeRemoteEvent(evt); ok {
			t.Fatal("expected a recipient-addressed presence event to be dropped")
		}
	})

	t.Run("strips everything the event does not carry", func(t *testing.T) {
		evt := base()
		evt.Payload = &MessagePayload{ID: "x", BodyText: "secret"}
		evt.Pin = &PinEventPayload{MessageID: "x"}
		evt.Members = &MembersAddedPayload{ActorUserID: "x", AddedCount: 1, MemberCount: 2}
		got, ok := canonicalizeRemoteEvent(evt)
		if !ok {
			t.Fatal("expected acceptance")
		}
		if got.Payload != nil || got.Pin != nil || got.Members != nil {
			t.Fatalf("contaminated payloads survived: %+v", got)
		}
	})
}

// A presence block riding on an unrelated event type is dropped rather than
// relayed: only presence.updated carries one.
func TestCanonicalizeRemoteEvent_StripsPresenceFromOtherTypes(t *testing.T) {
	evt := Event{
		SchemaVersion: CurrentEventSchemaVersion,
		Type:          EventTypeMembersAdded,
		WorkspaceID:   "11111111-1111-1111-1111-111111111111",
		TargetType:    TargetTypeChannel,
		TargetID:      "22222222-2222-2222-2222-222222222222",
		Members: &MembersAddedPayload{
			ActorUserID: "33333333-3333-3333-3333-333333333333", AddedCount: 1, MemberCount: 3,
		},
		Presence:         &PresencePayload{UserID: "44444444-4444-4444-4444-444444444444", State: "online"},
		EventID:          "55555555-5555-5555-5555-555555555555",
		SourceInstanceID: "other-instance",
	}
	got, ok := canonicalizeRemoteEvent(evt)
	if !ok {
		t.Fatal("expected the members event to be accepted")
	}
	if got.Presence != nil {
		t.Fatalf("presence rode along on members.added: %+v", got.Presence)
	}
}

// ── lifecycle ────────────────────────────────────────────────────────────────

// The fan-out goroutine is joined by Shutdown like every other pump, so a hub
// that ran presence leaves nothing behind.
func TestPresence_Shutdown_StopsFanout(t *testing.T) {
	tracker := NewPresenceTracker(5 * time.Minute)
	defer tracker.Stop()

	hub := NewHub(&fakeAuthorizer{}, newTestLogger(), NopBus{}, "presence-shutdown", WithPresence(tracker))
	hub.enqueuePresenceChange(presenceChange{
		workspaceID: "ws-1", userID: "user-1", status: PresenceOnline, at: time.Now(),
	})
	hub.Shutdown()
}

// ── saturation and convergence (CQ-3) ────────────────────────────────────────

// Reporting a change must never block its reporter — the hub run loop, a read
// loop and the tracker's ticker all report, and none of them may wait on a
// fan-out that is behind.
func TestPresence_EnqueueNeverBlocks(t *testing.T) {
	clk := newFakeClock(time.Now())
	tracker := newTestPresenceTracker(5*time.Minute, clk)
	h := newPresenceTestHub(allowAllAuthorizer{}, tracker)

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Far more than any queue this hub ever had, and nothing is draining.
		for i := range 10_000 {
			h.enqueuePresenceChange(presenceChange{
				workspaceID: "ws-1", userID: "user-1",
				status: PresenceOnline, at: clk.Now().Add(time.Duration(i)),
			})
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("enqueuePresenceChange blocked while the fan-out was not draining")
	}
}

// The property the old bounded queue could not hold: whatever the pressure, the
// user's *final* state still reaches the observers, with no further transition
// from that user to trigger it.
func TestPresence_SaturatedFanout_ConvergesToTheFinalState(t *testing.T) {
	clk := newFakeClock(time.Now())
	tracker := newTestPresenceTracker(5*time.Minute, clk)
	h := newPresenceTestHub(allowAllAuthorizer{}, tracker)

	watcher := newClient("c-watcher", "user-watcher", "ws-1", &fakeSender{})
	registerInHub(t, h, watcher)
	subscribeInHubState(t, h, watcher, TargetTypeChannel, "chan-1")

	subject := newClient("c-subject", "user-subject", "ws-1", &fakeSender{})
	registerInHub(t, h, subject)
	subscribeInHubState(t, h, subject, TargetTypeChannel, "chan-1")

	// Nothing is draining: every one of these finds the fan-out behind.
	base := clk.Now()
	for i := range 5_000 {
		h.enqueuePresenceChange(presenceChange{
			workspaceID: "ws-1", userID: "user-subject",
			status: PresenceOnline, at: base.Add(time.Duration(i) * time.Millisecond),
			derive: true,
		})
	}
	// The last thing that happened to this user, and the only state that must
	// survive the pressure.
	finalAt := base.Add(time.Hour)
	h.enqueuePresenceChange(presenceChange{
		workspaceID: "ws-1", userID: "user-subject",
		status: PresenceOffline, at: finalAt, keys: []string{
			targetKey{workspaceID: "ws-1", targetType: TargetTypeChannel, targetID: "chan-1"}.String(),
		},
	})

	// The consumer catches up. No new transition is produced by the user.
	events := drainPresenceEvents(t, h)

	states := presenceStatesFor(events, "user-subject")
	if len(states) != 1 {
		t.Fatalf("expected the pressure to coalesce into one event, got %v", states)
	}
	if states[0] != string(PresenceOffline) {
		t.Fatalf("expected the final state to survive, got %q", states[0])
	}
	if events[0].Presence.UpdatedAt != formatPresenceTime(finalAt) {
		t.Fatalf("expected the final instant, got %q", events[0].Presence.UpdatedAt)
	}
}

// Coalescing is per user. One busy user must not swallow another's state.
func TestPresence_Coalescing_IsPerUser(t *testing.T) {
	clk := newFakeClock(time.Now())
	tracker := newTestPresenceTracker(5*time.Minute, clk)
	h := newPresenceTestHub(allowAllAuthorizer{}, tracker)

	watcher := newClient("c-watcher", "user-watcher", "ws-1", &fakeSender{})
	registerInHub(t, h, watcher)
	key := subscribeInHubState(t, h, watcher, TargetTypeChannel, "chan-1")

	for _, userID := range []string{"user-a", "user-b"} {
		h.enqueuePresenceChange(presenceChange{
			workspaceID: "ws-1", userID: userID,
			status: PresenceOnline, at: clk.Now(), keys: []string{key},
		})
	}
	// user-a moves again; user-b must be untouched.
	h.enqueuePresenceChange(presenceChange{
		workspaceID: "ws-1", userID: "user-a",
		status: PresenceAway, at: clk.Now().Add(time.Minute), keys: []string{key},
	})

	events := drainPresenceEvents(t, h)

	if got := presenceStatesFor(events, "user-a"); len(got) != 1 || got[0] != string(PresenceAway) {
		t.Fatalf("user-a: expected one away event, got %v", got)
	}
	if got := presenceStatesFor(events, "user-b"); len(got) != 1 || got[0] != string(PresenceOnline) {
		t.Fatalf("user-b: expected its own online event, got %v", got)
	}
}

// A coalesced change keeps every audience it accumulated: the older change may
// have named a room the newer one can no longer derive, and that room still has
// to learn where the user ended up.
func TestPresence_Coalescing_UnionsAudiences(t *testing.T) {
	clk := newFakeClock(time.Now())
	tracker := newTestPresenceTracker(5*time.Minute, clk)
	h := newPresenceTestHub(allowAllAuthorizer{}, tracker)

	channelKey := targetKey{workspaceID: "ws-1", targetType: TargetTypeChannel, targetID: "chan-1"}.String()
	dmKey := targetKey{workspaceID: "ws-1", targetType: TargetTypeDM, targetID: "dm-1"}.String()

	h.enqueuePresenceChange(presenceChange{
		workspaceID: "ws-1", userID: "user-1",
		status: PresenceAway, at: clk.Now(), keys: []string{channelKey},
	})
	h.enqueuePresenceChange(presenceChange{
		workspaceID: "ws-1", userID: "user-1",
		status: PresenceOffline, at: clk.Now().Add(time.Minute), keys: []string{dmKey},
	})

	events := drainPresenceEvents(t, h)
	if len(events) != 2 {
		t.Fatalf("expected the final state in both rooms, got %d event(s)", len(events))
	}
	rooms := map[string]bool{}
	for _, evt := range events {
		if evt.Presence.State != string(PresenceOffline) {
			t.Fatalf("expected only the final state, got %q", evt.Presence.State)
		}
		rooms[string(evt.TargetType)+":"+evt.TargetID] = true
	}
	if !rooms["channel:chan-1"] || !rooms["dm:dm-1"] {
		t.Fatalf("an audience was lost in the merge: %v", rooms)
	}
}

// ── connect while away (CQ-2) ────────────────────────────────────────────────

// Presence is aggregated per user, not per connection. Opening a second session
// while away makes the user online, and the people already watching them must
// hear it — waiting for that session to subscribe would make the update depend
// on a step that has nothing to do with the transition.
func TestPresence_SecondConnectionWhileAway_PublishesOnlineBeforeItSubscribes(t *testing.T) {
	const awayTimeout = 5 * time.Minute
	clk := newFakeClock(time.Now())
	tracker := newTestPresenceTracker(awayTimeout, clk)
	h := newPresenceTestHub(allowAllAuthorizer{}, tracker)

	// An existing session, subscribed to a room a watcher is also in.
	first := newClient("c-first", "user-1", "ws-1", &fakeSender{})
	registerInHub(t, h, first)
	tracker.Connect(first.workspaceID, first.userID, first.id)
	subscribeInHubState(t, h, first, TargetTypeChannel, "chan-1")

	watcher := newClient("c-watcher", "user-watcher", "ws-1", &fakeSender{})
	registerInHub(t, h, watcher)
	subscribeInHubState(t, h, watcher, TargetTypeChannel, "chan-1")

	clk.Advance(awayTimeout + time.Second)
	tracker.checkAway()
	if got := drainPresenceEvents(t, h); len(presenceStatesFor(got, "user-1")) != 1 {
		t.Fatalf("expected the away transition first, got %v", presenceStatesFor(got, "user-1"))
	}

	// The second session registers. It subscribes to nothing.
	second := newClient("c-second", "user-1", "ws-1", &fakeSender{})
	registerInHub(t, h, second)
	change := tracker.Connect(second.workspaceID, second.userID, second.id)
	if !change.Changed || change.Status != PresenceOnline {
		t.Fatalf("expected the away → online transition, got %+v", change)
	}
	h.enqueuePresenceChange(presenceChange{
		workspaceID: second.workspaceID, userID: second.userID,
		status: change.Status, at: change.At, derive: true,
	})

	events := drainPresenceEvents(t, h)
	if got := presenceStatesFor(events, "user-1"); len(got) != 1 || got[0] != string(PresenceOnline) {
		t.Fatalf("expected one online event from the new connection, got %v", got)
	}
	if events[0].TargetID != "chan-1" {
		t.Fatalf("expected the existing session's room, got %q", events[0].TargetID)
	}
	channelKey := targetKey{
		workspaceID: "ws-1", targetType: TargetTypeChannel, targetID: "chan-1",
	}.String()
	if hubHasClientSubscription(h, second.id, channelKey) {
		t.Fatal("the second connection must not have needed a subscription of its own")
	}
}

// The register path itself must do this, not just a test that mimics it.
func TestPresence_Register_PublishesTheTransitionItCauses(t *testing.T) {
	const awayTimeout = 5 * time.Minute
	clk := newFakeClock(time.Now())
	tracker := newTestPresenceTracker(awayTimeout, clk)

	auth := &fakeAuthorizer{}
	auth.setAccess("user-1", "ws-1", TargetTypeChannel, "chan-1", true)
	auth.setAccess("user-watcher", "ws-1", TargetTypeChannel, "chan-1", true)

	hub := NewHub(auth, newTestLogger(), NopBus{}, "connect-instance", WithPresence(tracker))
	t.Cleanup(hub.Shutdown)

	first := newClient("c-first", "user-1", "ws-1", &fakeSender{})
	registerInRunningHub(t, hub, first)
	if err := hub.Subscribe(context.Background(), first, TargetTypeChannel, "chan-1"); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	watcher := newClient("c-watcher", "user-watcher", "ws-1", &fakeSender{})
	registerInRunningHub(t, hub, watcher)
	if err := hub.Subscribe(context.Background(), watcher, TargetTypeChannel, "chan-1"); err != nil {
		t.Fatalf("subscribe watcher: %v", err)
	}

	clk.Advance(awayTimeout + time.Second)
	tracker.checkAway()
	eventually(t, func() bool {
		return tracker.Status("ws-1", "user-1") == PresenceAway
	}, 2*time.Second, "user went away")

	drainClientOutbox(watcher)

	second := newClient("c-second", "user-1", "ws-1", &fakeSender{})
	registerInRunningHub(t, hub, second)

	eventually(t, func() bool {
		for _, state := range presenceStatesInOutbox(watcher, "user-1") {
			if state == string(PresenceOnline) {
				return true
			}
		}
		return false
	}, 2*time.Second, "watcher was told the user is online again")
}

// A genuinely first connection has no audience: nobody is watching a user who
// was not present a moment ago, so registering must publish nothing.
func TestPresence_FirstConnection_PublishesNothing(t *testing.T) {
	clk := newFakeClock(time.Now())
	tracker := newTestPresenceTracker(5*time.Minute, clk)
	h := newPresenceTestHub(allowAllAuthorizer{}, tracker)

	lonely := newClient("c-lonely", "user-lonely", "ws-1", &fakeSender{})
	registerInHub(t, h, lonely)
	change := tracker.Connect(lonely.workspaceID, lonely.userID, lonely.id)
	if !change.Changed {
		t.Fatalf("expected the first connection to change the state, got %+v", change)
	}
	h.enqueuePresenceChange(presenceChange{
		workspaceID: lonely.workspaceID, userID: lonely.userID,
		status: change.Status, at: change.At, derive: true,
	})

	if events := drainPresenceEvents(t, h); len(events) != 0 {
		t.Fatalf("expected no audience and no event, got %+v", events)
	}
}

// ── snapshot amplification (SR-2) ────────────────────────────────────────────

func TestPresence_RepeatedSubscribe_SendsOneSnapshot(t *testing.T) {
	clk := newFakeClock(time.Now())
	tracker := newTestPresenceTracker(5*time.Minute, clk)
	h := newPresenceTestHub(allowAllAuthorizer{}, tracker)

	c := newClient("c-1", "user-1", "ws-1", &fakeSender{})
	registerInHub(t, h, c)
	tracker.Connect(c.workspaceID, c.userID, c.id)

	key := targetKey{workspaceID: "ws-1", targetType: TargetTypeChannel, targetID: "chan-1"}.String()

	// First subscribe: the generation moves from 0 to something.
	before := h.subscriptionGeneration(c, key)
	subscribeInHubState(t, h, c, TargetTypeChannel, "chan-1")
	h.handleSubscribed(c, TargetTypeChannel, "chan-1", before)
	if got := len(takeSnapshots(t, c)); got != 1 {
		t.Fatalf("first subscribe: expected 1 snapshot, got %d", got)
	}
	drainPresenceEvents(t, h)

	// A repeated subscribe for a target already held. handleSubscribe leaves the
	// generation alone, so nothing joined and nothing may be emitted.
	repeated := h.subscriptionGeneration(c, key)
	if err := h.handleSubscribe(subscribeReq{
		ctx: context.Background(), client: c, targetType: TargetTypeChannel, targetID: "chan-1",
		resp: make(chan error, 1),
	}); err != nil {
		t.Fatalf("repeated subscribe: %v", err)
	}
	h.handleSubscribed(c, TargetTypeChannel, "chan-1", repeated)

	if got := len(takeSnapshots(t, c)); got != 0 {
		t.Fatalf("repeated subscribe: expected no further snapshot, got %d", got)
	}
	if events := drainPresenceEvents(t, h); len(events) != 0 {
		t.Fatalf("repeated subscribe re-announced the subscriber: %+v", events)
	}
}

// Leaving and coming back is a real join: a new generation, so a new snapshot.
func TestPresence_ResubscribeAfterUnsubscribe_SendsANewSnapshot(t *testing.T) {
	clk := newFakeClock(time.Now())
	tracker := newTestPresenceTracker(5*time.Minute, clk)
	h := newPresenceTestHub(allowAllAuthorizer{}, tracker)

	c := newClient("c-1", "user-1", "ws-1", &fakeSender{})
	registerInHub(t, h, c)
	tracker.Connect(c.workspaceID, c.userID, c.id)
	key := targetKey{workspaceID: "ws-1", targetType: TargetTypeChannel, targetID: "chan-1"}.String()

	before := h.subscriptionGeneration(c, key)
	subscribeInHubState(t, h, c, TargetTypeChannel, "chan-1")
	h.handleSubscribed(c, TargetTypeChannel, "chan-1", before)
	drainClientOutbox(c)
	drainPresenceEvents(t, h)

	if !h.revokeSubscription(c, key) {
		t.Fatal("unsubscribe failed")
	}
	rejoin := h.subscriptionGeneration(c, key)
	subscribeInHubState(t, h, c, TargetTypeChannel, "chan-1")
	h.handleSubscribed(c, TargetTypeChannel, "chan-1", rejoin)

	if got := len(takeSnapshots(t, c)); got != 1 {
		t.Fatalf("rejoin: expected a new snapshot, got %d", got)
	}
}

// A snapshot that had to stop short says so, and the client is then not
// entitled to read anything into who is missing from it.
func TestPresence_Snapshot_MarksItselfIncompleteWhenBounded(t *testing.T) {
	clk := newFakeClock(time.Now())
	tracker := newTestPresenceTracker(5*time.Minute, clk)
	h := newPresenceTestHub(allowAllAuthorizer{}, tracker)

	viewer := newClient("c-viewer", "user-viewer", "ws-1", &fakeSender{})
	registerInHub(t, h, viewer)
	tracker.Connect(viewer.workspaceID, viewer.userID, viewer.id)
	key := subscribeInHubState(t, h, viewer, TargetTypeChannel, "chan-1")

	// One more distinct user than the bound allows.
	for i := range presenceSnapshotMaxUsers + 1 {
		id := fmt.Sprintf("user-%05d", i)
		crowd := newClient("c-"+id, id, "ws-1", &fakeSender{})
		registerInHub(t, h, crowd)
		tracker.Connect(crowd.workspaceID, crowd.userID, crowd.id)
		subscribeInHubState(t, h, crowd, TargetTypeChannel, "chan-1")
	}

	h.sendPresenceSnapshot(viewer, TargetTypeChannel, "chan-1", key)

	snapshots := takeSnapshots(t, viewer)
	if len(snapshots) != 1 {
		t.Fatalf("expected one snapshot, got %d", len(snapshots))
	}
	if snapshots[0].Complete {
		t.Fatal("a snapshot that stopped at the bound must not claim to be complete")
	}
	if len(snapshots[0].Users) != presenceSnapshotMaxUsers {
		t.Fatalf("expected the bound to be respected, got %d entries", len(snapshots[0].Users))
	}
}

func TestPresence_Snapshot_IsCompleteWithinTheBound(t *testing.T) {
	clk := newFakeClock(time.Now())
	tracker := newTestPresenceTracker(5*time.Minute, clk)
	h := newPresenceTestHub(allowAllAuthorizer{}, tracker)

	viewer := newClient("c-viewer", "user-viewer", "ws-1", &fakeSender{})
	registerInHub(t, h, viewer)
	tracker.Connect(viewer.workspaceID, viewer.userID, viewer.id)
	key := subscribeInHubState(t, h, viewer, TargetTypeChannel, "chan-1")

	h.sendPresenceSnapshot(viewer, TargetTypeChannel, "chan-1", key)

	snapshots := takeSnapshots(t, viewer)
	if len(snapshots) != 1 || !snapshots[0].Complete {
		t.Fatalf("expected one complete snapshot, got %+v", snapshots)
	}
}

// ── helpers for the running-hub tests ────────────────────────────────────────

func drainClientOutbox(c *Client) {
	for {
		select {
		case <-c.outbox:
		default:
			return
		}
	}
}

func presenceStatesInOutbox(c *Client, userID string) []string {
	states := make([]string, 0, 4)
	for {
		select {
		case data := <-c.outbox:
			var evt Event
			if err := json.Unmarshal(data, &evt); err != nil {
				continue
			}
			if evt.Type != EventTypePresenceUpdated || evt.Presence == nil {
				continue
			}
			if evt.Presence.UserID == userID {
				states = append(states, evt.Presence.State)
			}
		default:
			return states
		}
	}
}

// ── where the final offline goes ─────────────────────────────────────────────

// Two tabs on two different conversations, closed one after the other.
//
// Both rooms are told, and the reason is the ownership ledger rather than a
// history of where the user has been: this instance wrote an assertion into each
// of those rooms, so it is the one that has to withdraw both. Reconciliation is
// the backstop underneath, for the rooms an instance can no longer speak for at
// all — the ones it died holding.
func TestPresence_LastDisconnect_ConvergesInEveryObservedRoom(t *testing.T) {
	clk := newFakeClock(time.Now())
	shared := newFakeDirectory()
	tracker := newTestPresenceTracker(5*time.Minute, clk)
	h := newClusterNode("node-a", shared.view("node-a"), tracker)

	tabA := newClient("c-tab-a", "user-1", "ws-1", &fakeSender{})
	registerInHub(t, h, tabA)
	tracker.Connect(tabA.workspaceID, tabA.userID, tabA.id)
	subscribeInHubState(t, h, tabA, TargetTypeChannel, "chan-a")
	h.handleSubscribed(tabA, TargetTypeChannel, "chan-a", 0)

	tabB := newClient("c-tab-b", "user-1", "ws-1", &fakeSender{})
	registerInHub(t, h, tabB)
	tracker.Connect(tabB.workspaceID, tabB.userID, tabB.id)
	subscribeInHubState(t, h, tabB, TargetTypeChannel, "chan-b")
	h.handleSubscribed(tabB, TargetTypeChannel, "chan-b", 0)

	watcherA := newClient("c-watch-a", "user-watch-a", "ws-1", &fakeSender{})
	registerInHub(t, h, watcherA)
	subscribeInHubState(t, h, watcherA, TargetTypeChannel, "chan-a")
	watcherB := newClient("c-watch-b", "user-watch-b", "ws-1", &fakeSender{})
	registerInHub(t, h, watcherB)
	subscribeInHubState(t, h, watcherB, TargetTypeChannel, "chan-b")
	drainPresenceEvents(t, h)
	h.reconcilePresence()
	drainBroadcasts(h)

	// The first tab closes; the user is still here, so nothing is announced.
	h.dropClient(tabA)
	if got := tracker.Status("ws-1", "user-1"); got != PresenceOnline {
		t.Fatalf("expected the user to stay online after one tab closed, got %q", got)
	}
	if events := drainPresenceEvents(t, h); len(events) != 0 {
		t.Fatalf("closing one of two tabs published %d event(s)", len(events))
	}

	// The last tab closes.
	h.dropClient(tabB)
	if got := tracker.Status("ws-1", "user-1"); got != PresenceOffline {
		t.Fatalf("expected offline after the last tab, got %q", got)
	}

	events := drainPresenceEvents(t, h)
	rooms := map[string]int{}
	for _, evt := range events {
		if evt.Presence == nil || evt.Presence.UserID != "user-1" {
			continue
		}
		if evt.Presence.State != string(PresenceOffline) {
			t.Fatalf("expected offline, got %q", evt.Presence.State)
		}
		rooms[evt.TargetID]++
	}
	// The room the departing connection was reading is told at once. The one the
	// earlier tab had already stopped covering left the roster when that tab
	// closed, and its observers converge on the next sweep — the user was, after
	// all, still online at that point, so announcing them offline there would
	// have been false.
	if rooms["chan-b"] != 1 {
		t.Fatalf("the departing connection's room was not told: %v", rooms)
	}

	// And the shared roster agrees, so a sweep has nothing left to correct.
	for _, target := range []string{"chan-a", "chan-b"} {
		key := targetKey{workspaceID: "ws-1", targetType: TargetTypeChannel, targetID: target}.String()
		entries, err := shared.view("node-a").Present(context.Background(), key)
		if err != nil {
			t.Fatalf("directory read: %v", err)
		}
		if roster := aggregateRoster(entries); len(roster) != 0 {
			t.Fatalf("%s still shows the user: %+v", target, roster)
		}
	}

	// The ledger of what this instance confirmed is empty for that user.
	if got := h.assertedTargetsList("ws-1", "user-1"); len(got) != 0 {
		t.Fatalf("assertion ledger leaked after the user left: %v", got)
	}
	h.assertedMu.Lock()
	remaining := len(h.asserted)
	h.assertedMu.Unlock()
	if remaining != 0 {
		t.Fatalf("expected no ledger entries to remain, got %d", remaining)
	}
}

// Unsubscribing ends this instance's cover of that conversation, so the
// assertion goes with it — but no departure is *announced*, because the person
// has not gone anywhere: they are still online and still a member.
//
// The room's observers are corrected from the roster instead, and *immediately*:
// waiting for the periodic sweep was the bug (SR-444-04). Nothing else revisits
// that target — the subject produced no transition, so no event is coming — and
// in a single-node deployment there is no sweep at all.
func TestPresence_UnsubscribeWithdrawsTheAssertionQuietly(t *testing.T) {
	clk := newFakeClock(time.Now())
	shared := newFakeDirectory()
	tracker := newTestPresenceTracker(5*time.Minute, clk)
	h := newClusterNode("node-a", shared.view("node-a"), tracker)

	subject := newClient("c-subject", "user-1", "ws-1", &fakeSender{})
	registerInHub(t, h, subject)
	tracker.Connect(subject.workspaceID, subject.userID, subject.id)
	key := subscribeInHubState(t, h, subject, TargetTypeChannel, "chan-1")
	h.handleSubscribed(subject, TargetTypeChannel, "chan-1", 0)

	watcher := newClient("c-watcher", "user-watcher", "ws-1", &fakeSender{})
	registerInHub(t, h, watcher)
	subscribeInHubState(t, h, watcher, TargetTypeChannel, "chan-1")
	drainPresenceEvents(t, h)

	if !h.revokeSubscription(subject, key) {
		t.Fatal("unsubscribe failed")
	}

	if events := drainPresenceEvents(t, h); len(events) != 0 {
		t.Fatalf("unsubscribing announced a departure: %+v", events)
	}
	// The assertion is gone: nothing this instance holds covers that target for
	// this user any more.
	entries, err := shared.view("node-a").Present(context.Background(), key)
	if err != nil {
		t.Fatalf("directory read: %v", err)
	}
	if roster := aggregateRoster(entries); len(roster) != 0 {
		t.Fatalf("the assertion outlived the coverage that justified it: %+v", roster)
	}
	// The user is still online — the aggregate never moved.
	if got := tracker.Status("ws-1", "user-1"); got != PresenceOnline {
		t.Fatalf("an unsubscribe changed the user's presence: %q", got)
	}
	// And the watching room is corrected by the reconciliation the unsubscribe
	// itself asked for, not by a false departure and not by the next sweep.
	h.drainReconcileRequests()
	rosters := reconciledRosters(t, h)
	if roster, ok := rosters["chan-1"]; !ok || len(roster.Users) != 0 {
		t.Fatalf("the room was not corrected when the last coverage went: %+v", rosters)
	}
}

// The same room reached through two connections is still one room.
func TestPresence_OverlappingRooms_ProduceNoDuplicate(t *testing.T) {
	clk := newFakeClock(time.Now())
	tracker := newTestPresenceTracker(5*time.Minute, clk)
	h := newPresenceTestHub(allowAllAuthorizer{}, tracker)

	first := newClient("c-1", "user-1", "ws-1", &fakeSender{})
	second := newClient("c-2", "user-1", "ws-1", &fakeSender{})
	for _, c := range []*Client{first, second} {
		registerInHub(t, h, c)
		tracker.Connect(c.workspaceID, c.userID, c.id)
		subscribeInHubState(t, h, c, TargetTypeChannel, "chan-shared")
	}
	drainPresenceEvents(t, h)

	h.dropClient(first)
	h.dropClient(second)

	events := drainPresenceEvents(t, h)
	if got := presenceStatesFor(events, "user-1"); len(got) != 1 {
		t.Fatalf("expected exactly one offline for the shared room, got %v", got)
	}
}
