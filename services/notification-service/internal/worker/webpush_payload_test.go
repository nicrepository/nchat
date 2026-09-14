package worker

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Issue #746: what a push is allowed to carry.
//
// The payload crosses a push service this deployment does not operate and lands
// in a browser this deployment does not control. Everything in it is therefore
// disclosed to at least one third party, so the test that matters is not what
// the payload contains but what it cannot.

func payloadNotification() Notification {
	return Notification{
		ID:          "11111111-1111-1111-1111-111111111111",
		WorkspaceID: "22222222-2222-2222-2222-222222222222",
		RecipientID: "33333333-3333-3333-3333-333333333333",
		EventType:   "mention",
		Priority:    "high",
		SourceType:  "message",
		SourceID:    "44444444-4444-4444-4444-444444444444",
		Origin:      "live",
		DedupeKey:   "message:44444444-4444-4444-4444-444444444444:mention",
		Attempt:     2,
		OccurredAt:  time.Date(2026, 9, 8, 14, 30, 0, 0, time.UTC),
		Muted:       false,
	}
}

func decodePayload(t *testing.T, encoded []byte) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("payload is not valid JSON: %v", err)
	}
	return decoded
}

// The payload is exactly six fields. A seventh appearing without this test
// being changed is the review this assertion exists to force.
func TestPushPayloadIsMinimalAndVersioned(t *testing.T) {
	encoded, err := buildPushPayload(payloadNotification())
	if err != nil {
		t.Fatalf("buildPushPayload: %v", err)
	}
	decoded := decodePayload(t, encoded)

	want := map[string]any{
		"v":           float64(PushPayloadVersion),
		"id":          "11111111-1111-1111-1111-111111111111",
		"type":        "mention",
		"source_type": "message",
		"source_id":   "44444444-4444-4444-4444-444444444444",
		"occurred_at": "2026-09-08T14:30:00Z",
	}
	if len(decoded) != len(want) {
		t.Fatalf("payload has %d fields, want %d: %s", len(decoded), len(want), encoded)
	}
	for key, expected := range want {
		if decoded[key] != expected {
			t.Fatalf("payload[%q] = %v, want %v", key, decoded[key], expected)
		}
	}
}

// The version is first and is a number, so a consumer can branch on it before
// it has understood anything else in the document.
func TestPushPayloadLeadsWithItsVersion(t *testing.T) {
	encoded, err := buildPushPayload(payloadNotification())
	if err != nil {
		t.Fatalf("buildPushPayload: %v", err)
	}
	if !strings.HasPrefix(string(encoded), `{"v":1,`) {
		t.Fatalf("payload does not lead with its version: %s", encoded)
	}
}

// Nothing the recipient is not already entitled to see, and nothing that grants
// anything. The workspace and the recipient are absent because a push arriving
// at the wrong device must disclose neither; the mute state, the origin and the
// dedupe key are absent because they are the policy's working notes.
func TestPushPayloadCarriesNoIdentityOrPolicyState(t *testing.T) {
	notification := payloadNotification()
	encoded, err := buildPushPayload(notification)
	if err != nil {
		t.Fatalf("buildPushPayload: %v", err)
	}
	body := string(encoded)

	for name, forbidden := range map[string]string{
		"workspace id": notification.WorkspaceID,
		"recipient id": notification.RecipientID,
		"dedupe key":   notification.DedupeKey,
		"origin":       notification.Origin,
		"priority":     notification.Priority,
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("the payload carries the %s: %s", name, body)
		}
	}
	for _, key := range []string{"auth", "p256dh", "token", "endpoint", "vapid", "attempt"} {
		if strings.Contains(strings.ToLower(body), key) {
			t.Fatalf("the payload mentions %q: %s", key, body)
		}
	}
}

// Two encodes of one notification produce identical bytes, so an endpoint that
// receives a repeat receives the same message. That is what lets a consumer
// keeping records recognise a duplicate as one.
func TestPushPayloadIsDeterministic(t *testing.T) {
	notification := payloadNotification()
	first, err := buildPushPayload(notification)
	if err != nil {
		t.Fatalf("buildPushPayload: %v", err)
	}
	// A later attempt of the same event differs only in the attempt counter,
	// which the payload does not carry.
	notification.Attempt = 5
	second, err := buildPushPayload(notification)
	if err != nil {
		t.Fatalf("buildPushPayload: %v", err)
	}
	if string(first) != string(second) {
		t.Fatalf("a retry encoded differently:\n%s\n%s", first, second)
	}
}

// The instant is normalised to UTC, so a payload does not leak the server's
// timezone and two servers encode the same event identically.
func TestPushPayloadNormalisesTheInstantToUTC(t *testing.T) {
	notification := payloadNotification()
	notification.OccurredAt = notification.OccurredAt.In(time.FixedZone("UTC-3", -3*60*60))

	encoded, err := buildPushPayload(notification)
	if err != nil {
		t.Fatalf("buildPushPayload: %v", err)
	}
	if got := decodePayload(t, encoded)["occurred_at"]; got != "2026-09-08T14:30:00Z" {
		t.Fatalf("occurred_at = %v, want the UTC instant", got)
	}
}

// A real payload is nowhere near the limit. This measures the headroom rather
// than asserting a number, so a field added later that consumed it would be
// noticed here instead of at a push service.
func TestPushPayloadIsFarInsideItsLimit(t *testing.T) {
	encoded, err := buildPushPayload(payloadNotification())
	if err != nil {
		t.Fatalf("buildPushPayload: %v", err)
	}
	if len(encoded) > maxPushPayloadBytes/4 {
		t.Fatalf("a version 1 payload is %d bytes, over a quarter of the %d-byte limit",
			len(encoded), maxPushPayloadBytes)
	}
}

// The limit is enforced before a provider is called, so an oversized payload is
// a categorised local failure rather than an error surfacing from inside the
// encryption library at send time.
func TestPushPayloadOverTheLimitIsRefused(t *testing.T) {
	notification := payloadNotification()
	notification.SourceID = strings.Repeat("x", maxPushPayloadBytes)

	if _, err := buildPushPayload(notification); err == nil {
		t.Fatal("an oversized payload was accepted")
	}
}
