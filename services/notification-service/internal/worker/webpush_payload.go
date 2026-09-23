package worker

import (
	"encoding/json"
	"fmt"
)

// The Web Push payload (issues #746 and #870).
//
// # What a push is allowed to say
//
// Version 1 said: references, a category, and a version. Nothing else, because
// there was nothing else to say — chat.notification_outbox stores no message
// body and no sender name, so this layer could not have included them without
// reaching back into the message store, and whether it *should* was a privacy
// decision the product had not made.
//
// Issue #870 made it. Version 2 adds exactly two strings, a title and a body
// preview, both produced on the server by webpush_preview.go from a projection
// the claim resolved against the recipient's access at that instant. Nothing
// else changed: the references, the category, the timestamp and the encryption
// are version 1's, and every field version 1 declares is still present and
// still means what it meant.
//
// # Why the deep link is not a capability
//
// SourceType and SourceID name a resource, and naming one grants nothing. The
// browser opens the application at that reference and the application asks the
// server for it exactly as it would from a click in the sidebar, with the
// session it already has.
//
// What a v2 push that reached the wrong device discloses is therefore no longer
// only "a notification exists" — it is the title and the preview as well. That
// is the deliberate cost of the feature and the reason for everything that
// bounds it: the preview is short, it is plain text, it is resolved per
// recipient, it is absent for anything the recipient may not see, and the whole
// version is off unless an operator turned it on.
//
// # What must never be here
//
// No token of any kind, no session, no capability, no VAPID key, no p256dh or
// auth, no endpoint, no e-mail address, no workspace-internal infrastructure
// detail. The struct is closed and every field is a value the recipient is
// already entitled to see, which is a stronger guarantee than a redaction step
// somebody has to remember.

// PushPayloadVersion is the schema version a preview-carrying payload declares.
//
// A consumer reads this first and ignores a payload it does not understand, so
// a Service Worker deployed before this version existed cannot misread one.
// That cuts both ways, and it is why the version is not simply raised: such a
// worker refuses a v2 payload outright and shows nothing at all. See
// PushPreviewEnabled in the configuration for the rollout order that follows
// from it.
const PushPayloadVersion = 2

// PushPayloadVersionLegacy is the contract from #746, which this service still
// emits by default and which the browser must still accept.
//
// It is not deprecated and it is not a fallback for a failed v2: it is what a
// deployment that has not enabled previews sends, and what every Service Worker
// older than #870 understands.
const PushPayloadVersionLegacy = 1

// maxPushPayloadBytes bounds the encoded payload.
//
// The Web Push encryption layer pads every message to a 4096-byte record, and
// the aes128gcm header plus the AEAD tag consume part of it, so a payload near
// that size fails inside the library at send time. This bound is well below
// where that happens and is checked before any provider is called, so an
// oversized payload is a categorised, non-retryable result rather than a
// surprise from a dependency.
//
// Version 2's own fields are bounded far lower and independently —
// titleMaxBytes plus previewMaxBytes is 520, against roughly 200 bytes of
// references — so this stays what it has always been: a guard against a future
// field, not a limit anything approaches.
const maxPushPayloadBytes = 3072

// pushPayload is the wire contract with the browser.
//
// Encoding is deterministic for the same notification and presentation snapshot.
// A new claim may change the presentation after edits or access revocation;
// dedupe uses the stable notification ID, not equality of payload bytes.
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
	// Title and BodyPreview are version 2 only, and both are omitempty.
	//
	// Omitted is a meaning and not an accident: it says "the server has nothing
	// it may show for this one", and the browser answers it with the generic
	// per-type copy version 1 always produced. That is the single fallback
	// path, so a message that was deleted, withheld or lost access to since the
	// outbox row was written produces the same banner as an unknown event type.
	//
	// A v2 payload can therefore carry a title and no body — an attachment-less
	// authorized message with no text — but never a body and no title.
	Title       string `json:"title,omitempty"`
	BodyPreview string `json:"body_preview,omitempty"`
}

// buildPushPayload encodes one notification into the bytes a push carries.
//
// withPreview selects the contract: false emits version 1 exactly as #746
// defined it, true emits version 2. It is the deployment's own switch rather
// than anything derived from the notification, because the question it answers
// is about the fleet of browsers and the operator's privacy posture, not about
// this event.
//
// A failure here is a defect in this service, never something a provider or a
// recipient did, so it is returned as an error the delivery layer classifies as
// a configuration fault: it will fail identically on every retry, and retrying
// it would burn the event's attempts against a bug.
func buildPushPayload(notification Notification, withPreview bool) ([]byte, error) {
	payload := pushPayload{
		Version:    PushPayloadVersionLegacy,
		ID:         notification.ID,
		Type:       notification.EventType,
		SourceType: notification.SourceType,
		SourceID:   notification.SourceID,
		OccurredAt: notification.OccurredAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
	}
	if withPreview {
		presentation := presentationFor(notification)
		payload.Version = PushPayloadVersion
		payload.Title = presentation.Title
		payload.BodyPreview = presentation.Body
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode push payload: %w", err)
	}
	if len(encoded) > maxPushPayloadBytes {
		return nil, fmt.Errorf("push payload is %d bytes, over the %d-byte limit",
			len(encoded), maxPushPayloadBytes)
	}
	return encoded, nil
}
