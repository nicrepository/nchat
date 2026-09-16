package worker

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/notification-service/internal/storage"
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

// Version 1 is exactly six fields, and issue #870 did not change one of them.
// A seventh appearing here without this test being changed is the review this
// assertion exists to force.
func TestPushPayloadV1IsMinimalAndVersioned(t *testing.T) {
	encoded, err := buildPushPayload(payloadNotification(), false)
	if err != nil {
		t.Fatalf("buildPushPayload: %v", err)
	}
	decoded := decodePayload(t, encoded)

	want := map[string]any{
		"v":           float64(PushPayloadVersionLegacy),
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
	encoded, err := buildPushPayload(payloadNotification(), false)
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
	encoded, err := buildPushPayload(notification, false)
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
	first, err := buildPushPayload(notification, false)
	if err != nil {
		t.Fatalf("buildPushPayload: %v", err)
	}
	// A later attempt of the same event differs only in the attempt counter,
	// which the payload does not carry.
	notification.Attempt = 5
	second, err := buildPushPayload(notification, false)
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

	encoded, err := buildPushPayload(notification, false)
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
	encoded, err := buildPushPayload(payloadNotification(), false)
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

	if _, err := buildPushPayload(notification, false); err == nil {
		t.Fatal("an oversized payload was accepted")
	}
}

// ---------------------------------------------------------------------------
// Version 2 (issue #870)
// ---------------------------------------------------------------------------
//
// The tests above are version 1's and none of them changed, which is the first
// thing this issue had to guarantee: a deployment that has not enabled previews
// emits exactly the bytes it emitted before.

func previewedNotification() Notification {
	notification := payloadNotification()
	notification.Presentation = storage.MessagePresentation{
		Sender:  "Ana Ribeiro",
		Context: "#geral",
		Body:    "o deploy de ontem derrubou o gateway",
	}
	return notification
}

// Version 2 is version 1 plus two strings. Every field version 1 declared is
// still present, still spelled the same and still means the same thing — which
// is what lets one Service Worker read both.
func TestPushPayloadV2IsV1PlusTitleAndPreview(t *testing.T) {
	encoded, err := buildPushPayload(previewedNotification(), true)
	if err != nil {
		t.Fatalf("buildPushPayload: %v", err)
	}
	decoded := decodePayload(t, encoded)

	want := map[string]any{
		"v":            float64(PushPayloadVersion),
		"id":           "11111111-1111-1111-1111-111111111111",
		"type":         "mention",
		"source_type":  "message",
		"source_id":    "44444444-4444-4444-4444-444444444444",
		"occurred_at":  "2026-09-08T14:30:00Z",
		"title":        "Ana Ribeiro · #geral",
		"body_preview": "o deploy de ontem derrubou o gateway",
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

// The version says which contract this is, and it is not the version 1 one. A
// Service Worker that has never heard of previews reads this first and refuses
// the whole document, which is why the flag exists — see the rollout note on
// config.WebPushConfig.PushPreviewEnabled.
func TestPushPayloadVersionsAreDistinctAndDeclared(t *testing.T) {
	legacy, err := buildPushPayload(previewedNotification(), false)
	if err != nil {
		t.Fatalf("buildPushPayload: %v", err)
	}
	if !strings.HasPrefix(string(legacy), `{"v":1,`) {
		t.Fatalf("the preview-less payload is not version 1: %s", legacy)
	}
	if strings.Contains(string(legacy), "Ana Ribeiro") {
		t.Fatalf("a version 1 payload carried a preview: %s", legacy)
	}

	previewed, err := buildPushPayload(previewedNotification(), true)
	if err != nil {
		t.Fatalf("buildPushPayload: %v", err)
	}
	if !strings.HasPrefix(string(previewed), `{"v":2,`) {
		t.Fatalf("the previewed payload is not version 2: %s", previewed)
	}
}

// Nothing to show is a version 2 payload with the two fields absent, not a
// version 1 payload and not an empty string. Absent is the browser's single
// fallback path, and it is reached identically by every unpublishable and
// unauthorized case.
func TestPushPayloadV2OmitsAnAbsentPreview(t *testing.T) {
	encoded, err := buildPushPayload(payloadNotification(), true)
	if err != nil {
		t.Fatalf("buildPushPayload: %v", err)
	}
	decoded := decodePayload(t, encoded)

	if decoded["v"] != float64(PushPayloadVersion) {
		t.Fatalf("v = %v, want version 2 even with nothing to preview", decoded["v"])
	}
	for _, key := range []string{"title", "body_preview"} {
		if _, present := decoded[key]; present {
			t.Fatalf("payload carries an empty %q: %s", key, encoded)
		}
	}
}

// A message nobody may preview but everybody may be told about: title present,
// body absent. The reverse — a body under no title — is refused by
// presentationFor and asserted there.
func TestPushPayloadV2CanCarryATitleWithoutAPreview(t *testing.T) {
	notification := payloadNotification()
	notification.Presentation = storage.MessagePresentation{Sender: "Ana Ribeiro"}

	encoded, err := buildPushPayload(notification, true)
	if err != nil {
		t.Fatalf("buildPushPayload: %v", err)
	}
	decoded := decodePayload(t, encoded)

	if decoded["title"] != "Ana Ribeiro" {
		t.Fatalf("title = %v", decoded["title"])
	}
	if _, present := decoded["body_preview"]; present {
		t.Fatalf("payload carries a preview it has no text for: %s", encoded)
	}
}

// The preview is bounded long before the payload is, so no message can be
// written that pushes a version 2 payload past the encryption layer's record.
// The body here is the longest one chat.messages allows.
func TestPushPayloadV2StaysInsideItsLimitForTheLongestMessage(t *testing.T) {
	notification := payloadNotification()
	notification.Presentation = storage.MessagePresentation{
		Sender:  strings.Repeat("\U0001F642", 200),
		Context: strings.Repeat("\U0001F642", 200),
		Body:    strings.Repeat("\U0001F642", 40000),
	}

	encoded, err := buildPushPayload(notification, true)
	if err != nil {
		t.Fatalf("buildPushPayload: %v", err)
	}
	if len(encoded) > maxPushPayloadBytes {
		t.Fatalf("payload is %d bytes, over the %d-byte limit", len(encoded), maxPushPayloadBytes)
	}
	if !json.Valid(encoded) {
		t.Fatalf("truncation produced a payload that is not valid JSON: %s", encoded)
	}
}

// A version 2 payload discloses a name, a place and a sentence, and still
// discloses no identity and no policy state. Everything version 1 refused to
// carry, it still refuses to carry.
func TestPushPayloadV2StillCarriesNoIdentityOrPolicyState(t *testing.T) {
	notification := previewedNotification()
	encoded, err := buildPushPayload(notification, true)
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
	for _, key := range []string{"auth", "p256dh", "token", "endpoint", "vapid", "email", "attempt"} {
		if strings.Contains(strings.ToLower(body), key) {
			t.Fatalf("the payload mentions %q: %s", key, body)
		}
	}
}

// Two encodes of one notification produce identical bytes, previews included.
func TestPushPayloadV2IsDeterministic(t *testing.T) {
	notification := previewedNotification()
	notification.Presentation.Body = strings.Repeat("mensagem longa ", 40)

	first, err := buildPushPayload(notification, true)
	if err != nil {
		t.Fatalf("buildPushPayload: %v", err)
	}
	notification.Attempt = 5
	second, err := buildPushPayload(notification, true)
	if err != nil {
		t.Fatalf("buildPushPayload: %v", err)
	}
	if string(first) != string(second) {
		t.Fatalf("a retry encoded differently:\n%s\n%s", first, second)
	}
}

func TestPushPayloadV1BytesRemainUnchanged(t *testing.T) {
	encoded, err := buildPushPayload(previewedNotification(), false)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"v":1,"id":"11111111-1111-1111-1111-111111111111","type":"mention","source_type":"message","source_id":"44444444-4444-4444-4444-444444444444","occurred_at":"2026-09-08T14:30:00Z"}`
	if string(encoded) != want {
		t.Fatal("legacy payload bytes changed")
	}
}
