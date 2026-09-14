package ws

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
)

// Issue #744, review round 6: the fan-out is where a recipient exists, so it is
// where the decision is personalised. What is proved here is the delivery, not
// the rule — the rule belongs to libs/go/platform/notificationpolicy.

// fakeRecipientPolicy answers from a fixed muted set and denies every surface
// for a muted recipient, which is the shape the real engine produces. It
// restates no rule: the test asserts routing, not policy.
type fakeRecipientPolicy struct {
	muted map[string]bool
	err   error
	calls int
	// asked is the recipient list the fan-out resolved, exactly as it was
	// handed over: deduplicated and in first-seen order.
	asked []string
}

func (f *fakeRecipientPolicy) MutedUsers(
	_ context.Context, _ string, _ TargetType, _ string, userIDs []string,
) ([]string, error) {
	f.calls++
	f.asked = append([]string(nil), userIDs...)
	if f.err != nil {
		return nil, f.err
	}
	var out []string
	for _, id := range userIDs {
		if f.muted[id] {
			out = append(out, id)
		}
	}
	return out, nil
}

// The shapes the real engine produces for the three states. It restates no
// rule: the test asserts routing and encoding, not policy.
func (f *fakeRecipientPolicy) PolicyFor(
	_ MessagePayload, _ string, preference RecipientPreference,
) *NotificationPolicyPayload {
	decision := &NotificationPolicyPayload{
		PolicyVersion: 1, InApp: NotificationAllow, Sound: NotificationAllow,
		WebPush: NotificationDeny, SoundClass: SoundClassGeneral,
	}
	switch preference {
	case RecipientPreferenceMuted:
		decision.InApp, decision.Sound = NotificationDeny, NotificationDeny
		decision.Reasons = []string{"muted"}
	case RecipientPreferenceUnavailable:
		decision.InApp, decision.Sound = NotificationDeny, NotificationDeny
		decision.WebPush = NotificationDeny
		decision.Reasons = []string{"preferences_unavailable"}
	case RecipientPreferenceNone:
	}
	return decision
}

func broadcastFixture(t *testing.T, policy RecipientPolicy) (*Hub, broadcastReq) {
	t.Helper()
	hub := NewHub(NopAuthorizer{}, nil, NopBus{}, "test-instance", WithRecipientPolicy(policy))
	payload := MessagePayload{
		ID: "msg-1", WorkspaceID: "ws-1", ChannelID: "chan-1", Kind: "user",
		BodyText: "hello",
		NotificationPolicy: &NotificationPolicyPayload{
			PolicyVersion: 1, InApp: NotificationAllow, Sound: NotificationAllow,
			WebPush: NotificationDeny, SoundClass: SoundClassGeneral,
		},
	}
	event := Event{
		SchemaVersion: CurrentEventSchemaVersion, Type: EventTypeMessageCreated,
		WorkspaceID: "ws-1", TargetType: TargetTypeChannel, TargetID: "chan-1",
		MessageID: payload.ID, Payload: &payload,
	}
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return hub, broadcastReq{event: event, data: data}
}

func policyOf(t *testing.T, data []byte) *NotificationPolicyPayload {
	t.Helper()
	var decoded Event
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Payload == nil {
		t.Fatal("the delivered event carried no payload")
	}
	return decoded.Payload.NotificationPolicy
}

// Two members of one channel, one of whom silenced it: the bytes each is sent
// must carry that person's own decision.
func TestFanOutDeliversADifferentDecisionToAMutedRecipient(t *testing.T) {
	policy := &fakeRecipientPolicy{muted: map[string]bool{"user-muted": true}}
	hub, req := broadcastFixture(t, policy)

	encodings := hub.newRecipientEncodings(
		context.Background(), req, []string{"user-plain", "user-muted"})
	if encodings == nil {
		t.Fatal("no per-recipient encodings were resolved")
	}

	plain := policyOf(t, encodings.forRecipient("user-plain"))
	muted := policyOf(t, encodings.forRecipient("user-muted"))

	if plain.InApp != NotificationAllow || plain.Sound != NotificationAllow {
		t.Fatalf("the unmuted recipient lost a surface: %+v", plain)
	}
	if muted.InApp != NotificationDeny || muted.Sound != NotificationDeny {
		t.Fatalf("the muted recipient was sent an allow: %+v", muted)
	}
	// One read for the whole broadcast, not one per recipient.
	if policy.calls != 1 {
		t.Fatalf("resolved mute %d times for one broadcast, want 1", policy.calls)
	}
}

// A recipient whose decision matches the published one is sent the published
// bytes: personalising must not mean re-encoding for everybody.
func TestFanOutReusesThePublishedBytesWhenTheDecisionIsUnchanged(t *testing.T) {
	policy := &fakeRecipientPolicy{muted: map[string]bool{"user-muted": true}}
	hub, req := broadcastFixture(t, policy)

	encodings := hub.newRecipientEncodings(
		context.Background(), req, []string{"user-plain", "user-muted"})

	if got := encodings.forRecipient("user-plain"); &got[0] != &req.data[0] {
		t.Fatal("re-encoded a recipient whose decision was already the published one")
	}
	if got := encodings.forRecipient("user-muted"); &got[0] == &req.data[0] {
		t.Fatal("a diverging decision was delivered as the published bytes")
	}
}

// A preferences read that fails must not hand out the published decision.
//
// This replaces a test that asserted exactly that fallback. The behaviour it
// locked in was the defect: the published decision is the one for a recipient
// who expressed nothing, so giving it to everybody after a failed read alerts
// the person who had silenced the conversation — by a fault, and
// indistinguishably from the product ignoring them.
func TestFanOutIsFailClosedForAlertsWhenPreferencesCannotBeRead(t *testing.T) {
	policy := &fakeRecipientPolicy{err: errors.New("database unavailable")}
	hub, req := broadcastFixture(t, policy)

	encodings := hub.newRecipientEncodings(
		context.Background(), req, []string{"user-1", "user-2"})
	if encodings == nil {
		t.Fatal("a failed read must still produce a decision, not the published bytes")
	}

	// Both recipients, because the read is one statement for the whole list: if
	// it did not answer, it did not answer for anybody.
	for _, userID := range []string{"user-1", "user-2"} {
		data := encodings.bytesFor(userID, req.data)
		if &data[0] == &req.data[0] {
			t.Fatalf("%s was sent the published decision after a failed read", userID)
		}
		decision := policyOf(t, data)
		if decision.InApp != NotificationDeny || decision.Sound != NotificationDeny ||
			decision.WebPush != NotificationDeny {
			t.Fatalf("%s kept an alert surface: %+v", userID, decision)
		}
		if len(decision.Reasons) != 1 || decision.Reasons[0] != "preferences_unavailable" {
			t.Fatalf("%s reasons = %v, want the fault named explicitly", userID, decision.Reasons)
		}
	}
}

// ...and the message itself is untouched. A preferences outage costs
// notifications; it must never cost a message.
func TestFanOutStillDeliversTheMessageWhenPreferencesCannotBeRead(t *testing.T) {
	policy := &fakeRecipientPolicy{err: errors.New("database unavailable")}
	hub, req := broadcastFixture(t, policy)

	encodings := hub.newRecipientEncodings(context.Background(), req, []string{"user-1"})
	data := encodings.bytesFor("user-1", req.data)

	var delivered Event
	if err := json.Unmarshal(data, &delivered); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if delivered.Payload == nil {
		t.Fatal("the message payload was dropped")
	}
	published := *req.event.Payload
	got := *delivered.Payload
	if got.ID != published.ID || got.WorkspaceID != published.WorkspaceID ||
		got.ChannelID != published.ChannelID || got.BodyText != published.BodyText ||
		got.Kind != published.Kind {
		t.Fatalf("the message changed: %+v, want %+v", got, published)
	}
	if delivered.Type != EventTypeMessageCreated || delivered.MessageID != req.event.MessageID {
		t.Fatal("the event lost its identity")
	}
	// Exactly one read, even on the failing path: no retry, no per-recipient query.
	if policy.calls != 1 {
		t.Fatalf("queried preferences %d times, want 1", policy.calls)
	}
}

// Without a policy the hub is a build with nothing to personalise with, and
// every subscriber gets the published bytes.
func TestFanOutWithoutAPolicyChangesNothing(t *testing.T) {
	hub, req := broadcastFixture(t, nil)
	hub.recipientPolicy = nil
	if hub.newRecipientEncodings(context.Background(), req, []string{"user-1"}) != nil {
		t.Fatal("personalised a delivery with no policy to personalise it with")
	}
}

// bytesFor is what the delivery loop calls, and it has to read the same whether
// or not this build personalises anything.
func TestBytesForFallsBackToThePublishedEncoding(t *testing.T) {
	published := []byte(`{"published":true}`)

	// No policy at all: every recipient gets the published bytes.
	var none *recipientEncodings
	if got := none.bytesFor("user-1", published); &got[0] != &published[0] {
		t.Fatal("a build with no policy must deliver the published bytes")
	}

	policy := &fakeRecipientPolicy{muted: map[string]bool{"user-muted": true}}
	hub, req := broadcastFixture(t, policy)
	encodings := hub.newRecipientEncodings(
		context.Background(), req, []string{"user-plain", "user-muted"})

	if got := encodings.bytesFor("user-plain", req.data); &got[0] != &req.data[0] {
		t.Fatal("an unchanged decision must reuse the published bytes")
	}
	if got := encodings.bytesFor("user-muted", req.data); &got[0] == &req.data[0] {
		t.Fatal("a diverging decision must get its own bytes")
	}
}

// The subscriber list is what the read is asked about, and it is asked once,
// de-duplicated — one person with two tabs is one recipient.
func TestRecipientEncodingsForAsksAboutEachSubscriberOnce(t *testing.T) {
	policy := &fakeRecipientPolicy{muted: map[string]bool{}}
	hub, req := broadcastFixture(t, policy)

	subscriptions := []broadcastSubscription{
		{client: &Client{userID: "user-1"}},
		{client: &Client{userID: "user-1"}},
		{client: &Client{userID: "user-2"}},
		{client: &Client{userID: ""}},
	}
	if hub.recipientEncodingsFor(req, subscriptions) == nil {
		t.Fatal("no encodings were resolved for a broadcast with subscribers")
	}
	if policy.calls != 1 {
		t.Fatalf("asked the preference store %d times, want 1", policy.calls)
	}
	// Two tabs are one person, and a client with no identity is nobody.
	if got := policy.asked; !slices.Equal(got, []string{"user-1", "user-2"}) {
		t.Fatalf("asked about %v, want each recipient once", got)
	}
}

// The recipient list the fan-out builds: deduplicated, and in the order each
// person was first seen.
//
// Order is part of the contract rather than an accident of the data structure —
// the preference read and every decision that follows are made against this
// list, so a build that dropped the order would make a broadcast's behaviour
// depend on map iteration.
func TestRecipientListIsDeduplicatedInFirstSeenOrder(t *testing.T) {
	cases := map[string]struct {
		subscribers []string
		want        []string
	}{
		"all distinct":              {[]string{"a", "b", "c"}, []string{"a", "b", "c"}},
		"repeats collapse in place": {[]string{"a", "b", "a", "c", "b"}, []string{"a", "b", "c"}},
		"one person, many tabs":     {[]string{"a", "a", "a"}, []string{"a"}},
		"identities without a user": {[]string{"", "a", "", "b"}, []string{"a", "b"}},
		"reverse order is kept":     {[]string{"c", "b", "a"}, []string{"c", "b", "a"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			policy := &fakeRecipientPolicy{muted: map[string]bool{}}
			hub, req := broadcastFixture(t, policy)

			subscriptions := make([]broadcastSubscription, 0, len(tc.subscribers))
			for _, userID := range tc.subscribers {
				subscriptions = append(subscriptions,
					broadcastSubscription{client: &Client{userID: userID}})
			}
			hub.recipientEncodingsFor(req, subscriptions)

			if !slices.Equal(policy.asked, tc.want) {
				t.Fatalf("recipients = %v, want %v", policy.asked, tc.want)
			}
			// Still one read for the whole list, however many connections it came from.
			if policy.calls != 1 {
				t.Fatalf("read preferences %d times, want 1", policy.calls)
			}
		})
	}
}

// A person with several connections is one logical recipient: their decision is
// computed and encoded once, and every one of their tabs is sent those bytes.
func TestManyConnectionsOfOnePersonShareOneDecision(t *testing.T) {
	policy := &fakeRecipientPolicy{muted: map[string]bool{"user-muted": true}}
	hub, req := broadcastFixture(t, policy)

	subscriptions := []broadcastSubscription{
		{client: &Client{userID: "user-muted"}},
		{client: &Client{userID: "user-muted"}},
		{client: &Client{userID: "user-plain"}},
	}
	encodings := hub.recipientEncodingsFor(req, subscriptions)
	if encodings == nil {
		t.Fatal("no encodings were resolved")
	}

	first := encodings.bytesFor("user-muted", req.data)
	second := encodings.bytesFor("user-muted", req.data)
	if &first[0] != &second[0] {
		t.Fatal("the same recipient was encoded twice")
	}
	if policyOf(t, first).InApp != NotificationDeny {
		t.Fatal("the muted recipient's own decision was not applied to both connections")
	}
	if policyOf(t, encodings.bytesFor("user-plain", req.data)).InApp != NotificationAllow {
		t.Fatal("the unmuted recipient lost their surface")
	}
}

// A route-only event carries no payload — there is no decision in it to
// personalise, and nothing must be re-encoded.
func TestRouteOnlyEventsAreNotPersonalised(t *testing.T) {
	policy := &fakeRecipientPolicy{}
	hub, req := broadcastFixture(t, policy)
	req.event.Payload = nil

	if hub.recipientEncodingsFor(req, []broadcastSubscription{
		{client: &Client{userID: "user-1"}},
	}) != nil {
		t.Fatal("personalised an event that carries no payload")
	}
	if policy.calls != 0 {
		t.Fatal("queried preferences for an event with no decision in it")
	}
}

// samePolicy decides whether a recipient needs their own encoding, so it has to
// notice every field a decision is made of.
func TestSamePolicyComparesTheWholeDecision(t *testing.T) {
	base := func() *NotificationPolicyPayload {
		return &NotificationPolicyPayload{
			PolicyVersion: 1, InApp: NotificationAllow, Sound: NotificationAllow,
			WebPush: NotificationDeny, SoundClass: SoundClassGeneral,
			Reasons: []string{"muted"}, NamedUserIDs: []string{"user-1"},
		}
	}
	if !samePolicy(base(), base()) {
		t.Fatal("two identical decisions compared unequal")
	}
	if !samePolicy(nil, nil) {
		t.Fatal("two absent decisions compared unequal")
	}
	if samePolicy(base(), nil) || samePolicy(nil, base()) {
		t.Fatal("an absent decision matched a present one")
	}
	differences := []func(*NotificationPolicyPayload){
		func(p *NotificationPolicyPayload) { p.InApp = NotificationDeny },
		func(p *NotificationPolicyPayload) { p.Sound = NotificationDeny },
		func(p *NotificationPolicyPayload) { p.WebPush = NotificationAllow },
		func(p *NotificationPolicyPayload) { p.PolicyVersion = 2 },
		func(p *NotificationPolicyPayload) { p.SoundClass = SoundClassDirect },
		func(p *NotificationPolicyPayload) { p.Reasons = []string{"outside_work_hours"} },
		func(p *NotificationPolicyPayload) { p.NamedUserIDs = nil },
		func(p *NotificationPolicyPayload) { p.NamesEveryone = true },
	}
	for i, change := range differences {
		other := base()
		change(other)
		if samePolicy(base(), other) {
			t.Fatalf("difference %d was treated as the same decision", i)
		}
	}
}
