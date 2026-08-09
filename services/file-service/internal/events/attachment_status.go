// Package events publishes attachment status changes to connected clients
// (RF-22).
//
// It does not own a WebSocket and does not talk to a browser. chat-service owns
// the connections, the subscriptions and the per-subscriber authorization
// re-check, and it already consumes a distributed bus for exactly this purpose:
// Valkey Pub/Sub on nchat:chat:ws:broadcast:{workspace_id}. This package is the
// smallest possible producer for that bus.
//
// # Why not a new mechanism
//
// The alternatives were a second broker, an HTTP callback from file-service into
// chat-service, or a durable outbox. All three would add infrastructure for one
// invalidation signal, and none would improve on the delivery this needs: the
// verdict is already persisted before anything is published, so the event is a
// latency optimisation over a refetch the client can always fall back on. The
// bus that already carries pin.updated and members.added carries this too.
//
// # What this producer is not trusted for
//
// Nothing. chat-service treats every bus payload as untrusted: it canonicalises
// the envelope, rejects unknown types and non-UUID identifiers, and re-derives
// each subscriber's access before delivery. Publishing an event for a channel
// therefore reaches only the people who could already read that channel — this
// package cannot address a user, cannot widen a subscription, and cannot make a
// private destination visible.
package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/nicrepository/nchat/services/file-service/internal/domain"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
	valkey "github.com/valkey-io/valkey-go"
)

// eventType is the wire type chat-service's hub accepts for this event. It is a
// constant on both sides; nothing derives it from data.
const eventType = "attachment.status"

// schemaVersion mirrors ws.CurrentEventSchemaVersion. An envelope carrying any
// other value is rejected by the consumer, which is what keeps a rolling deploy
// from delivering a shape the receiving build does not implement.
const schemaVersion = 1

// publishTimeout caps a single PUBLISH. It matches chat-service's own: long
// enough for a normal round trip, short enough that a stalled broker cannot
// hold the scan worker.
const publishTimeout = 2 * time.Second

// busEvent is the subset of ws.Event this producer fills in.
//
// It is a separate, deliberately minimal struct rather than an import of
// chat-service's type: file-service must not depend on chat-service's internal
// packages, and a producer that could set every field of the consumer's envelope
// would be a producer able to emit any event in the protocol. The fields absent
// here — message payloads, recipient_user_id, reactions, pins, call state — are
// absent because this producer has no business setting them.
type busEvent struct {
	SchemaVersion    int              `json:"schema_version"`
	Type             string           `json:"type"`
	WorkspaceID      string           `json:"workspace_id"`
	TargetType       string           `json:"target_type"`
	TargetID         string           `json:"target_id"`
	Attachment       *attachmentState `json:"attachment"`
	EventID          string           `json:"event_id"`
	SourceInstanceID string           `json:"source_instance_id"`
	CreatedAt        time.Time        `json:"created_at"`
}

// attachmentState is the payload: what changed and when, and nothing else.
type attachmentState struct {
	AttachmentID string `json:"attachment_id"`
	Status       string `json:"status"`
	UpdatedAt    string `json:"updated_at"`
}

// Publisher sends attachment status changes onto the shared broadcast bus.
type Publisher struct {
	client     valkey.Client
	instanceID string
}

var _ service.AttachmentStatusPublisher = (*Publisher)(nil)

// NewPublisher dials Valkey and returns a publisher.
//
// instanceID identifies this file-service process on the bus. chat-service uses
// it only to suppress its own echo, so a value that never collides with a
// chat-service instance id is all that is required; it is validated here to the
// same character set the consumer accepts, so a malformed one fails at start-up
// rather than by having every event silently dropped.
func NewPublisher(valkeyURL, instanceID string) (*Publisher, error) {
	if valkeyURL == "" {
		return nil, errors.New("events: valkey URL is required")
	}
	if !validInstanceID(instanceID) {
		return nil, errors.New("events: instance id must be 1-64 characters of [A-Za-z0-9._-]")
	}
	option, err := valkey.ParseURL(valkeyURL)
	if err != nil {
		// The URL carries credentials, so neither it nor the parse error text is
		// propagated.
		return nil, errors.New("events: valkey URL is invalid")
	}
	client, err := valkey.NewClient(option)
	if err != nil {
		return nil, errors.New("events: valkey client could not be created")
	}
	return &Publisher{client: client, instanceID: instanceID}, nil
}

// validInstanceID mirrors chat-service's sourceInstanceIDRe and its length
// bound. Kept as an explicit loop rather than a regexp so the accepted set is
// visible at the point it is enforced.
func validInstanceID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// PublishAttachmentStatus announces one persisted status change.
//
// Every identifier is re-parsed as a UUID before it is used. That is not
// defensive noise: the workspace id becomes part of the channel name, and a
// value carrying a glob character would publish into a pattern rather than a
// workspace. Parsing is also what canonicalises the case, so the consumer's own
// canonicalisation cannot disagree with what was published.
//
// The status is checked against the domain's closed set for the same reason a
// client's status is never trusted: this event is what a client reconciles
// against, and an unrecognised value would be one this build never decided.
func (p *Publisher) PublishAttachmentStatus(
	ctx context.Context, change service.AttachmentStatusChange,
) error {
	if p == nil || p.client == nil {
		return errors.New("events: publisher not configured")
	}
	channel, payload, err := p.encode(change)
	if err != nil {
		return err
	}

	pubCtx, cancel := context.WithTimeout(ctx, publishTimeout)
	defer cancel()

	if err := p.client.Do(pubCtx,
		p.client.B().Publish().Channel(channel).Message(string(payload)).Build(),
	).Error(); err != nil {
		// The broker's error can name its address; the caller only logs a
		// category, so nothing about the topology travels with it.
		return errors.New("events: publish failed")
	}
	return nil
}

// encode validates the change and turns it into a channel name and an envelope.
//
// It is separate from the publish so the validation — which is what stops an
// unparsed identifier from becoming part of a channel name — is reachable
// without a broker, and so a test can read the exact bytes that would go on the
// wire rather than trusting that they are what they should be.
func (p *Publisher) encode(change service.AttachmentStatusChange) (string, []byte, error) {
	targetType, err := targetTypeFor(change.DestinationKind)
	if err != nil {
		return "", nil, err
	}
	if !change.Status.Valid() {
		return "", nil, errors.New("events: attachment status is not a known state")
	}
	workspaceID, err := uuid.Parse(change.WorkspaceID)
	if err != nil {
		return "", nil, errors.New("events: workspace id is not a UUID")
	}
	targetID, err := uuid.Parse(change.DestinationID)
	if err != nil {
		return "", nil, errors.New("events: destination id is not a UUID")
	}
	attachmentID, err := uuid.Parse(change.AttachmentID)
	if err != nil {
		return "", nil, errors.New("events: attachment id is not a UUID")
	}

	payload, err := json.Marshal(busEvent{
		SchemaVersion: schemaVersion,
		Type:          eventType,
		WorkspaceID:   workspaceID.String(),
		TargetType:    targetType,
		TargetID:      targetID.String(),
		Attachment: &attachmentState{
			AttachmentID: attachmentID.String(),
			Status:       string(change.Status),
			UpdatedAt:    change.UpdatedAt.UTC().Format(time.RFC3339Nano),
		},
		EventID:          uuid.New().String(),
		SourceInstanceID: p.instanceID,
		CreatedAt:        time.Now().UTC(),
	})
	if err != nil {
		return "", nil, fmt.Errorf("events: marshal attachment status: %w", err)
	}
	// Built from the parsed UUID, never from the caller's string: that is what
	// keeps a glob character out of the channel name.
	return "nchat:chat:ws:broadcast:" + workspaceID.String(), payload, nil
}

// Close releases the connection. Safe on a nil publisher, so shutdown does not
// have to know whether the bus was configured.
func (p *Publisher) Close() {
	if p == nil || p.client == nil {
		return
	}
	p.client.Close()
}

// targetTypeFor maps the attachment's destination onto the bus target type.
//
// The two are separate vocabularies that happen to agree today, and mapping
// explicitly is what keeps an unknown kind from being published as a target the
// consumer would then resolve against the wrong table.
func targetTypeFor(kind domain.DestinationKind) (string, error) {
	switch kind {
	case domain.DestinationKindChannel:
		return "channel", nil
	case domain.DestinationKindDM:
		return "dm", nil
	default:
		return "", errors.New("events: unknown destination kind")
	}
}
