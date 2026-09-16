package worker

import (
	"encoding/json"
	"fmt"
)

// The Web Push payload (issue #746).
//
// # What a push is allowed to say
//
// References, a category, and a version. Nothing else, and the reason is not
// caution — it is that there is nothing else to say. chat.notification_outbox
// stores no message body, no sender name and no preview, deliberately (see
// notification-outbox.md, "O que nao e persistido"), so this layer could not
// include them without reaching back into the message store and defeating the
// restraint the outbox was built with. A sender or a preview is a privacy
// decision the product has not made and a schema change this issue does not
// need; when both exist, this struct gains a field and Version gains a number.
//
// # Why the deep link is not a capability
//
// SourceType and SourceID name a resource, and naming one grants nothing. The
// browser opens the application at that reference and the application asks the
// server for it exactly as it would from a click in the sidebar, with the
// session it already has. A push that reached the wrong device therefore
// discloses that *a* notification exists, and nothing about its contents.
//
// # What must never be here
//
// No token of any kind, no session, no capability, no VAPID key, no p256dh or
// auth, no endpoint, no workspace-internal infrastructure detail. The struct is
// closed and every field is a value the recipient is already entitled to see,
// which is a stronger guarantee than a redaction step somebody has to remember.

// PushPayloadVersion is the schema version carried in every payload.
//
// Version 1 is references only. A consumer reads this first and ignores a
// payload it does not understand, so a future version can add fields without a
// Service Worker deployed today misreading them.
const PushPayloadVersion = 1

// maxPushPayloadBytes bounds the encoded payload.
//
// The Web Push encryption layer pads every message to a 4096-byte record, and
// the aes128gcm header plus the AEAD tag consume part of it, so a payload near
// that size fails inside the library at send time. This bound is well below
// where that happens and is checked before any provider is called, so an
// oversized payload is a categorised, non-retryable result rather than a
// surprise from a dependency. Version 1 payloads are around 200 bytes, so this
// is a guard against a future field, not a limit anything approaches today.
const maxPushPayloadBytes = 3072

// pushPayload is the wire contract with the browser.
//
// Field order is the struct's order and encoding/json is deterministic, so the
// same notification encodes to the same bytes on every attempt. That matters
// for a retry: an endpoint that receives a repeat receives an identical
// message, which is what lets a consumer that keeps records recognise it as one.
type pushPayload struct {
	Version int    `json:"v"`
	ID      string `json:"id"`
	Type    string `json:"type"`
	// Source is the opaque reference the browser navigates to. Two fields
	// rather than a rendered URL, because a URL built here would be a routing
	// decision made in the wrong service.
	SourceType string `json:"source_type"`
	SourceID   string `json:"source_id"`
	// OccurredAt is RFC 3339. It is what lets a browser order or discard a
	// notification that arrived late, which is the one thing a push consumer
	// cannot work out for itself.
	OccurredAt string `json:"occurred_at"`
}

// buildPushPayload encodes one notification into the bytes a push carries.
//
// A failure here is a defect in this service, never something a provider or a
// recipient did, so it is returned as an error the delivery layer classifies as
// a configuration fault: it will fail identically on every retry, and retrying
// it would burn the event's attempts against a bug.
func buildPushPayload(notification Notification) ([]byte, error) {
	encoded, err := json.Marshal(pushPayload{
		Version:    PushPayloadVersion,
		ID:         notification.ID,
		Type:       notification.EventType,
		SourceType: notification.SourceType,
		SourceID:   notification.SourceID,
		OccurredAt: notification.OccurredAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
	})
	if err != nil {
		return nil, fmt.Errorf("encode push payload: %w", err)
	}
	if len(encoded) > maxPushPayloadBytes {
		return nil, fmt.Errorf("push payload is %d bytes, over the %d-byte limit",
			len(encoded), maxPushPayloadBytes)
	}
	return encoded, nil
}
