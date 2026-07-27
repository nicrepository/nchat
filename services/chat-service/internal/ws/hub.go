package ws

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log/slog"
	"regexp"
	"sync"
	"time"

	"github.com/google/uuid"
)

// broadcastAuthTimeout caps the per-client authorization re-check during
// broadcast delivery. If the check exceeds this duration the event is not
// delivered to that client for this broadcast.
const broadcastAuthTimeout = 3 * time.Second

// subscriptionAuthTimeout bounds the canonical message-visibility lookup for
// a client subscribe. It matches the existing per-recipient fan-out timeout.
const subscriptionAuthTimeout = 3 * time.Second

// broadcastWorkerCount bounds concurrent fan-out authorization without letting
// one slow recipient stall every other room on this Hub instance.
const broadcastWorkerCount = 4

// broadcastWorkerQueueCapacity preserves the existing bounded capacity for
// each partition without introducing an unbounded per-target queue.
const broadcastWorkerQueueCapacity = 256

// sourceInstanceIDMaxLen is the maximum allowed length for SourceInstanceID in
// remote events. Bounded to prevent memory waste from malformed payloads.
const sourceInstanceIDMaxLen = 64

// sourceInstanceIDRe restricts SourceInstanceID to characters that are safe for
// structured logging. Raw UUIDs, hostnames, and pod names all match.
var sourceInstanceIDRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// targetKey uniquely identifies a subscription target within a workspace.
// WorkspaceID is included to prevent cross-workspace subscription collisions.
type targetKey struct {
	workspaceID string
	targetType  TargetType
	targetID    string
}

func (k targetKey) String() string {
	return fmt.Sprintf("%s\x00%s\x00%s", k.workspaceID, string(k.targetType), k.targetID)
}

// registerReq carries a register request with an acknowledgement channel.
// Using an ack channel makes Register synchronous: after Register returns,
// the client is guaranteed to be in the hub's state, so a subsequent Subscribe
// call will never fail with "client not registered".
type registerReq struct {
	client *Client
	ack    chan bool // hub sends true only when the client is tracked
}

// subscribeReq is an internal request to add a subscription.
type subscribeReq struct {
	ctx        context.Context
	client     *Client
	targetType TargetType
	targetID   string
	resp       chan error // buffered(1); hub writes result, caller reads
}

type revokeSubscriptionReq struct {
	ctx    context.Context
	client *Client
	key    string
	resp   chan error // buffered(1); hub writes result, caller reads
}

// broadcastReq is an internal request to deliver an event.
type broadcastReq struct {
	event Event
	data  []byte // pre-encoded JSON; avoids re-encoding per client
	// done is closed by the hub after the broadcast is fully processed.
	// It is non-nil only when the caller requires synchronization (e.g., tests).
	done chan struct{}
}

type broadcastSubscription struct {
	client     *Client
	generation uint64
}

// HubOption is a functional option for NewHub.
type HubOption func(*Hub)

type ReactionUpdate struct {
	MessageID  string
	TargetType TargetType
	TargetID   string
	Added      bool
	Reactions  []ReactionPayload
}

type ReactionHandler interface {
	ToggleReaction(ctx context.Context, workspaceID, userID, messageID, emoji string) (ReactionUpdate, error)
}

type ReactionLimiter interface {
	Allow(ctx context.Context, userID string) (bool, error)
}

func WithReactionHandler(handler ReactionHandler) HubOption {
	return func(h *Hub) { h.reactionHandler = handler }
}

func WithReactionLimiter(limiter ReactionLimiter) HubOption {
	return func(h *Hub) { h.reactionLimiter = limiter }
}

// WithPresence attaches a PresenceTracker to the Hub. When set, the Hub
// calls Connect on register and Disconnect on unregister so that presence
// state stays in sync with WebSocket lifecycle events.
//
// The caller retains ownership of the PresenceTracker and is responsible
// for calling its Stop method after Hub.Shutdown returns.
func WithPresence(p *PresenceTracker) HubOption {
	return func(h *Hub) { h.presence = p }
}

// Hub manages all active WebSocket connections for a single chat-service instance.
//
// Distributed fan-out is provided by the optional BroadcastBus (e.g. ValkeyBus).
// When bus is NopBus, delivery is in-process only.
//
// All internal state (clients, subs, clientSubs) is protected by mu. Helpers
// that read or mutate those maps are mutex-safe and must not hold mu while
// performing auth checks or connection I/O. The final broadcast enqueue is a
// non-blocking channel send performed under mu so it is atomic with subscription
// revocation.
type Hub struct {
	authorizer SubscriptionAuthorizer
	bus        BroadcastBus
	instanceID string
	logger     *slog.Logger
	busCancel  context.CancelFunc

	presence        *PresenceTracker // optional; nil-safe throughout
	reactionHandler ReactionHandler
	reactionLimiter ReactionLimiter

	register        chan registerReq
	unregister      chan *Client
	subReq          chan subscribeReq
	revokeReq       chan revokeSubscriptionReq
	bcast           chan broadcastReq
	remoteBcast     chan broadcastReq // events received from the distributed bus
	quit            chan struct{}
	done            chan struct{}
	broadcastDone   chan struct{}
	broadcastWG     sync.WaitGroup
	broadcastQueues []chan broadcastReq

	mu sync.RWMutex

	clients                    map[string]*Client             // clientID → client
	subs                       map[string]map[string]struct{} // targetKey.String() → set of clientIDs
	clientSubs                 map[string]map[string]struct{} // clientID → set of targetKey strings
	subscriptionGenerations    map[string]map[string]uint64   // clientID → targetKey → generation
	nextSubscriptionGeneration uint64
}

// NewHub creates a Hub and starts its background goroutine.
//
// bus is the distributed broadcast bus. Pass NopBus{} for single-instance
// deployments or when Valkey is not configured.
//
// instanceID is a stable identifier for this process instance, used for
// echo-suppression of self-published events on the bus. If empty, a UUID
// is generated automatically.
//
// opts may include WithPresence to attach a PresenceTracker.
//
// Call Shutdown to stop the hub gracefully.
func NewHub(authorizer SubscriptionAuthorizer, logger *slog.Logger, bus BroadcastBus, instanceID string, opts ...HubOption) *Hub {
	if instanceID == "" {
		instanceID = uuid.New().String()
	}
	logger = normalizeLogger(logger)
	busCtx, busCancel := context.WithCancel(context.Background())
	h := &Hub{
		authorizer:              authorizer,
		bus:                     bus,
		instanceID:              instanceID,
		logger:                  logger,
		busCancel:               busCancel,
		register:                make(chan registerReq, 64),
		unregister:              make(chan *Client, 64),
		subReq:                  make(chan subscribeReq, 64),
		revokeReq:               make(chan revokeSubscriptionReq, 64),
		bcast:                   make(chan broadcastReq, 256),
		remoteBcast:             make(chan broadcastReq, 256),
		quit:                    make(chan struct{}),
		done:                    make(chan struct{}),
		broadcastDone:           make(chan struct{}),
		clients:                 make(map[string]*Client),
		subs:                    make(map[string]map[string]struct{}),
		clientSubs:              make(map[string]map[string]struct{}),
		subscriptionGenerations: make(map[string]map[string]uint64),
	}
	for _, opt := range opts {
		opt(h)
	}
	if err := h.bus.Subscribe(busCtx, h.handleRemoteBusEvent); err != nil {
		logger.Warn("ws: bus subscribe failed; remote broadcast disabled", "error", err)
	}
	go h.run()
	h.startBroadcastWorkers()
	return h
}

// Register adds a client to the hub and blocks until the hub has acknowledged
// the registration. If Register returns true, the client is guaranteed to be
// visible to Subscribe, so callers may call Subscribe immediately afterwards
// without a race.
//
// It returns false if the hub is shutting down or if the client ID is already
// registered. On false, the caller owns connection cleanup unless the hub has
// already closed the client as part of duplicate handling.
func (h *Hub) Register(c *Client) bool {
	if h.isShuttingDown() {
		return false
	}

	ack := make(chan bool, 1)
	select {
	case h.register <- registerReq{client: c, ack: ack}:
	case <-h.quit:
		return false
	}
	select {
	case ok := <-ack:
		return ok
	case <-h.quit:
		return false
	}
}

func (h *Hub) isShuttingDown() bool {
	select {
	case <-h.quit:
		return true
	default:
		return false
	}
}

// Unregister removes a client from the hub and cleans up all its subscriptions.
// Safe to call from any goroutine.
func (h *Hub) Unregister(c *Client) {
	select {
	case h.unregister <- c:
	case <-h.quit:
	}
}

// Subscribe requests a subscription for client to targetType/targetID.
//
// Authorization is checked using the client's server-asserted userID and
// workspaceID; client-provided identity is never used.
//
// Returns ErrSubscribeForbidden if access is denied (non-enumerating).
// Blocks until the hub processes the request or ctx is cancelled.
func (h *Hub) Subscribe(ctx context.Context, c *Client, targetType TargetType, targetID string) error {
	key := targetKey{workspaceID: c.workspaceID, targetType: targetType, targetID: targetID}.String()
	authCtx, cancel := context.WithTimeout(ctx, subscriptionAuthTimeout)
	stopClientCancellation := context.AfterFunc(c.ctx, cancel)
	defer func() {
		stopClientCancellation()
		cancel()
	}()
	allowed, err := h.authorizer.CanAccess(authCtx, c.userID, c.workspaceID, targetType, targetID)
	if err != nil {
		_ = h.RevokeSubscription(ctx, c, key)
		return fmt.Errorf("authorization check: %w", err)
	}
	if !allowed {
		_ = h.RevokeSubscription(ctx, c, key)
		return ErrSubscribeForbidden
	}

	resp := make(chan error, 1)
	req := subscribeReq{
		ctx:        authCtx,
		client:     c,
		targetType: targetType,
		targetID:   targetID,
		resp:       resp,
	}
	select {
	case h.subReq <- req:
	case <-ctx.Done():
		return ctx.Err()
	case <-h.quit:
		return ErrHubShutdown
	}
	select {
	case err := <-resp:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-h.quit:
		return ErrHubShutdown
	}
}

func (h *Hub) RevokeSubscription(ctx context.Context, c *Client, key string) error {
	resp := make(chan error, 1)
	req := revokeSubscriptionReq{ctx: ctx, client: c, key: key, resp: resp}
	select {
	case h.revokeReq <- req:
	case <-ctx.Done():
		return ctx.Err()
	case <-h.quit:
		return ErrHubShutdown
	}
	select {
	case err := <-resp:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-h.quit:
		return ErrHubShutdown
	}
}

// PublishMessageCreated broadcasts a message.created event to all clients
// subscribed to the message's target (channel or DM conversation).
//
// The payload carries the full message DTO (same contract as the list endpoint)
// so that browser clients can render the message without an additional GET.
//
// Local delivery happens first and is always attempted regardless of bus state.
// Distributed delivery via bus.Publish is best-effort: failures are logged
// but do not affect local delivery or cause a panic. Bus.Publish is called
// from the caller's goroutine, not the hub run goroutine, so Valkey I/O never
// blocks the hub.
//
// Authorization is re-checked per subscriber before delivery using a fresh
// background context. Transient auth errors skip delivery but keep the
// subscription; only a definitive allowed=false revokes it.
//
// Delivery guarantee: in-process best-effort. No durability, no replay.
// Wiring: call this from MessageService after a message is persisted.
func (h *Hub) PublishMessageCreated(ctx context.Context, workspaceID string, targetType TargetType, targetID string, payload MessagePayload) {
	var eventPayload *MessagePayload
	if !payload.HasReference {
		eventPayload = &payload
	}
	evt := Event{
		SchemaVersion:    CurrentEventSchemaVersion,
		Type:             EventTypeMessageCreated,
		WorkspaceID:      workspaceID,
		TargetType:       targetType,
		TargetID:         targetID,
		MessageID:        payload.ID,
		Payload:          eventPayload,
		EventID:          uuid.New().String(),
		SourceInstanceID: h.instanceID,
		CreatedAt:        time.Now().UTC(),
	}
	data, err := json.Marshal(evt)
	if err != nil {
		h.logger.ErrorContext(ctx, "ws: marshal message.created event", "error", err)
		return
	}

	// Local delivery — independent of bus state.
	select {
	case h.bcast <- broadcastReq{event: evt, data: data}:
	case <-ctx.Done():
		return
	case <-h.quit:
		return
	}

	// Distributed publish — failure must not affect local delivery.
	busEvt := evt
	busEvt.Payload = nil
	if err := h.bus.Publish(ctx, busEvt); err != nil {
		h.logger.WarnContext(ctx, "ws: bus publish failed; local delivery unaffected",
			"workspace_id", workspaceID,
			"target_type", string(targetType),
			"error", err,
		)
	}
}

// PublishMessageUpdated broadcasts an authorized post-commit message edit.
func (h *Hub) PublishMessageUpdated(ctx context.Context, workspaceID string, targetType TargetType, targetID string, payload MessageUpdatedPayload) {
	evt := Event{
		SchemaVersion: CurrentEventSchemaVersion, Type: EventTypeMessageUpdated,
		WorkspaceID: workspaceID, TargetType: targetType, TargetID: targetID,
		MessageID: payload.MessageID, MessageUpdate: &payload,
		EventID: uuid.New().String(), SourceInstanceID: h.instanceID, CreatedAt: time.Now().UTC(),
	}
	data, err := json.Marshal(evt)
	if err != nil {
		h.logger.ErrorContext(ctx, "ws: marshal message.updated event", "error", err)
		return
	}
	select {
	case h.bcast <- broadcastReq{event: evt, data: data}:
	case <-ctx.Done():
		return
	case <-h.quit:
		return
	}
	busEvent := evt
	busEvent.MessageUpdate = nil
	if err := h.bus.Publish(ctx, busEvent); err != nil {
		h.logger.WarnContext(ctx, "ws: message update bus publish failed", "target_type", string(targetType), "error", err)
	}
}

func (h *Hub) PublishReactionUpdated(ctx context.Context, workspaceID, actorUserID, emoji string, update ReactionUpdate) {
	payload := &ReactionEventPayload{
		MessageID: update.MessageID, ActorUserID: actorUserID, Emoji: emoji,
		Added: update.Added, Reactions: update.Reactions,
	}
	evt := Event{
		SchemaVersion: CurrentEventSchemaVersion, Type: EventTypeReactionUpdated,
		WorkspaceID: workspaceID, TargetType: update.TargetType, TargetID: update.TargetID,
		MessageID: update.MessageID, Reaction: payload, EventID: uuid.New().String(),
		SourceInstanceID: h.instanceID, CreatedAt: time.Now().UTC(),
	}
	data, err := json.Marshal(evt)
	if err != nil {
		h.logger.ErrorContext(ctx, "ws: marshal reaction.updated event", "error", err)
		return
	}
	select {
	case h.bcast <- broadcastReq{event: evt, data: data}:
	case <-ctx.Done():
		return
	case <-h.quit:
		return
	}
	if err := h.bus.Publish(ctx, evt); err != nil {
		h.logger.WarnContext(ctx, "ws: reaction bus publish failed", "target_type", string(update.TargetType), "error", err)
	}
}

// PublishPinUpdated broadcasts a pin.updated event (RF-05) to all clients
// subscribed to the target. Like message.created, local delivery is attempted
// first and re-checks authorization per subscriber; distributed publish via the
// bus is best-effort. The event is route-plus-flag only — it carries no message
// body, so clients refetch the pin list on receipt.
func (h *Hub) PublishPinUpdated(ctx context.Context, workspaceID string, targetType TargetType, targetID, messageID, actorUserID string, pinned bool) {
	evt := Event{
		SchemaVersion: CurrentEventSchemaVersion, Type: EventTypePinUpdated,
		WorkspaceID: workspaceID, TargetType: targetType, TargetID: targetID,
		MessageID:        messageID,
		Pin:              &PinEventPayload{MessageID: messageID, ActorUserID: actorUserID, Pinned: pinned},
		EventID:          uuid.New().String(),
		SourceInstanceID: h.instanceID, CreatedAt: time.Now().UTC(),
	}
	data, err := json.Marshal(evt)
	if err != nil {
		h.logger.ErrorContext(ctx, "ws: marshal pin.updated event", "error", err)
		return
	}
	select {
	case h.bcast <- broadcastReq{event: evt, data: data}:
	case <-ctx.Done():
		return
	case <-h.quit:
		return
	}
	if err := h.bus.Publish(ctx, evt); err != nil {
		h.logger.WarnContext(ctx, "ws: pin bus publish failed", "target_type", string(targetType), "error", err)
	}
}

// Shutdown stops the hub goroutine, cancels bus subscriptions, closes all
// client connections, and closes the BroadcastBus. Blocks until the run
// goroutine exits.
//
// Ownership: the hub started the bus subscription (via Subscribe in NewHub),
// so it is responsible for closing it. Callers must not call bus.Close()
// separately after hub.Shutdown() — BroadcastBus.Close is idempotent and
// handles double-close safely.
func (h *Hub) Shutdown() {
	h.busCancel()
	close(h.quit)
	<-h.done
	<-h.broadcastDone
	h.bus.Close() // stop subscriber goroutines; idempotent
}

// run is the hub's main coordination goroutine.
// Map state is still protected by h.mu so helper paths and future pumps can be
// exercised safely without relying on single-goroutine ownership.
func (h *Hub) run() {
	defer close(h.done)
	for {
		select {
		case req := <-h.register:
			if h.isShuttingDown() {
				req.ack <- false
				continue
			}
			registered := h.addClient(req.client)
			if !registered {
				h.logger.WarnContext(context.Background(), "ws: duplicate client registration; dropping new client",
					"client_id", req.client.id,
				)
				req.client.close()
			} else if h.presence != nil {
				// Connect is called after addClient releases h.mu — no lock held.
				h.presence.Connect(req.client.workspaceID, req.client.userID, req.client.id)
			}
			req.ack <- registered

		case c := <-h.unregister:
			h.dropClient(c)

		case req := <-h.subReq:
			err := h.handleSubscribe(req)
			// resp is buffered(1); send never blocks.
			req.resp <- err

		case req := <-h.revokeReq:
			if err := req.ctx.Err(); err != nil {
				req.resp <- err
				continue
			}
			if !h.revokeSubscription(req.client, req.key) {
				req.resp <- ErrClientNotRegistered
				continue
			}
			req.resp <- nil

		case <-h.quit:
			// clearClients removes all clients from hub state and returns a snapshot.
			// close() and Disconnect() are called after the lock is released, so
			// neither is ever invoked while h.mu is held.
			for _, c := range h.clearClients() {
				c.close()
				if h.presence != nil {
					h.presence.Disconnect(c.workspaceID, c.userID, c.id)
				}
			}
			return
		}
	}
}

func (h *Hub) startBroadcastWorkers() {
	h.broadcastQueues = make([]chan broadcastReq, broadcastWorkerCount)
	h.broadcastWG.Add(broadcastWorkerCount + 1)
	for index := range broadcastWorkerCount {
		queue := make(chan broadcastReq, broadcastWorkerQueueCapacity)
		h.broadcastQueues[index] = queue
		go h.runBroadcastWorker(queue)
	}
	go h.runBroadcastDispatcher()
	go func() {
		h.broadcastWG.Wait()
		close(h.broadcastDone)
	}()
}

// runBroadcastDispatcher defines broadcast order as the sequence in which this
// goroutine receives local and remote events. A stable hash sends every event
// for the same complete targetKey to the same sequential worker queue.
func (h *Hub) runBroadcastDispatcher() {
	defer h.broadcastWG.Done()
	for {
		select {
		case req := <-h.bcast:
			if !h.dispatchBroadcast(req) {
				return
			}
		case req := <-h.remoteBcast:
			if !h.dispatchBroadcast(req) {
				return
			}
		case <-h.quit:
			return
		}
	}
}

func (h *Hub) dispatchBroadcast(req broadcastReq) bool {
	partition := broadcastPartition(broadcastTargetKey(req.event), len(h.broadcastQueues))
	select {
	case h.broadcastQueues[partition] <- req:
		return true
	case <-h.quit:
		return false
	}
}

// runBroadcastWorker processes one partition sequentially. Authorization stays
// outside Hub.run and Hub.mu, while different partitions execute in parallel.
func (h *Hub) runBroadcastWorker(queue <-chan broadcastReq) {
	defer h.broadcastWG.Done()
	for {
		select {
		case <-h.quit:
			return
		default:
		}

		select {
		case req := <-queue:
			h.handleBroadcast(req)
		case <-h.quit:
			return
		}
	}
}

func broadcastTargetKey(event Event) string {
	return targetKey{
		workspaceID: event.WorkspaceID,
		targetType:  event.TargetType,
		targetID:    event.TargetID,
	}.String()
}

func broadcastPartition(key string, partitionCount int) int {
	hasher := fnv.New32a()
	_, _ = hasher.Write([]byte(key))
	return int(hasher.Sum32() % uint32(partitionCount))
}

// handleRemoteBusEvent is called by the BroadcastBus Subscribe handler.
// It suppresses self-echo, strictly validates and canonicalizes the event,
// encodes to JSON, and posts to remoteBcast for the run goroutine.
// Called from a bus-owned goroutine — must not touch hub state directly.
func (h *Hub) handleRemoteBusEvent(evt Event) {
	// Suppress self-echo: events we published must not be re-delivered locally.
	if evt.SourceInstanceID == h.instanceID {
		return
	}

	// Strictly validate and canonicalize before any local delivery.
	// Remote Pub/Sub events are untrusted; auth re-check alone is not sufficient.
	canonical, ok := canonicalizeRemoteEvent(evt)
	if !ok {
		h.logger.WarnContext(context.Background(), "ws: dropped invalid remote bus event",
			"event_type", string(evt.Type),
			"target_type", string(evt.TargetType),
		)
		return
	}

	data, err := json.Marshal(canonical)
	if err != nil {
		h.logger.ErrorContext(context.Background(), "ws: failed to marshal remote bus event", "error", err)
		return
	}

	select {
	case h.remoteBcast <- broadcastReq{event: canonical, data: data}:
	default:
		// Drop if queue is full — bounded, no goroutine block.
		h.logger.WarnContext(context.Background(), "ws: remote broadcast queue full; event dropped",
			"workspace_id", canonical.WorkspaceID,
			"target_type", string(canonical.TargetType),
		)
	}
}

// canonicalizeRemoteEvent validates and canonicalizes a remote bus event.
//
// All ID fields (EventID, WorkspaceID, TargetID, MessageID for message.created)
// must be valid UUIDs and are returned in canonical lowercase form. SourceInstanceID
// is checked for length and character safety. Unknown event/target types are
// rejected. Malformed events return (Event{}, false) — fail-secure.
//
// Security note: auth re-check (SubscriptionAuthorizer.CanAccess) is necessary
// but not sufficient for untrusted Pub/Sub payloads. Canonicalization here
// prevents spoofed workspace_id / target_id values from reaching the hub.
func canonicalizeRemoteEvent(evt Event) (Event, bool) {
	var ok bool
	evt, ok = canonicalizeRemoteEnvelope(evt)
	if !ok {
		return Event{}, false
	}
	evt, ok = canonicalizeEventIDs(evt)
	if !ok {
		return Event{}, false
	}
	if evt.Type == EventTypeReactionUpdated {
		evt, ok = canonicalizeReactionEvent(evt)
		if !ok {
			return Event{}, false
		}
	}
	if evt.Type == EventTypePinUpdated {
		evt, ok = canonicalizePinEvent(evt)
		if !ok {
			return Event{}, false
		}
	}

	// Remote bus payloads may contain body_text or legacy sender_email. Strip
	// them so remote nodes route by IDs only; clients fetch by ID if needed.
	evt.Payload = nil
	evt.MessageUpdate = nil

	return evt, true
}

func canonicalizeRemoteEnvelope(evt Event) (Event, bool) {
	switch evt.SchemaVersion {
	case 0:
		// Older chat-service instances did not emit schema_version. Treat absence
		// as v1 so rolling deploys continue to deliver route-only remote events.
		evt.SchemaVersion = CurrentEventSchemaVersion
	case CurrentEventSchemaVersion:
		// OK
	default:
		return Event{}, false
	}

	// Known event type required.
	switch evt.Type {
	case EventTypeMessageCreated, EventTypeMessageUpdated, EventTypeReactionUpdated, EventTypePinUpdated:
		// OK
	default:
		return Event{}, false
	}

	// Known target type required.
	switch evt.TargetType {
	case TargetTypeChannel, TargetTypeDM:
		// OK
	default:
		return Event{}, false
	}

	// source_instance_id: required, bounded length, safe characters only.
	// Not a UUID — pod names, hostnames, or generated IDs are all acceptable.
	if evt.SourceInstanceID == "" {
		return Event{}, false
	}
	if len(evt.SourceInstanceID) > sourceInstanceIDMaxLen {
		return Event{}, false
	}
	if !sourceInstanceIDRe.MatchString(evt.SourceInstanceID) {
		return Event{}, false
	}
	return evt, true
}

func canonicalizeEventIDs(evt Event) (Event, bool) {
	// event_id: required, must be a valid UUID; canonicalize to lowercase.
	eid, err := uuid.Parse(evt.EventID)
	if err != nil {
		return Event{}, false
	}
	evt.EventID = eid.String()

	// workspace_id: required, must be a valid UUID; canonicalize to lowercase.
	wid, err := uuid.Parse(evt.WorkspaceID)
	if err != nil {
		return Event{}, false
	}
	evt.WorkspaceID = wid.String()

	// target_id: required, must be a valid UUID; canonicalize to lowercase.
	tid, err := uuid.Parse(evt.TargetID)
	if err != nil {
		return Event{}, false
	}
	evt.TargetID = tid.String()

	// All currently supported event types are message-scoped.
	mid, err := uuid.Parse(evt.MessageID)
	if err != nil {
		return Event{}, false
	}
	evt.MessageID = mid.String()
	return evt, true
}

func canonicalizeReactionEvent(evt Event) (Event, bool) {
	if evt.Reaction == nil || evt.Reaction.MessageID != evt.MessageID || evt.Reaction.Emoji == "" || len(evt.Reaction.Reactions) > 64 {
		return Event{}, false
	}
	actorID, err := uuid.Parse(evt.Reaction.ActorUserID)
	if err != nil {
		return Event{}, false
	}
	evt.Reaction.ActorUserID = actorID.String()
	for _, reaction := range evt.Reaction.Reactions {
		if reaction.Emoji == "" || reaction.Count <= 0 {
			return Event{}, false
		}
	}
	// Aggregates are not trusted from the cross-instance bus. Remote clients
	// fetch the authoritative message snapshot, trading one read for a smaller
	// trusted bus surface and consistent reacted_by_me calculation.
	// TODO: this fetch-on-remote-event can cause a thundering herd in large
	// channels. Propagate aggregates and actor_user_id through the bus before
	// computing reacted_by_me optimistically without a REST round-trip.
	evt.Reaction = nil
	return evt, true
}

// canonicalizePinEvent validates a remote pin.updated event. The actor is
// canonicalized to lowercase UUID. The tiny route-plus-flag payload is retained.
func canonicalizePinEvent(evt Event) (Event, bool) {
	if evt.Pin == nil || evt.Pin.MessageID != evt.MessageID {
		return Event{}, false
	}
	if evt.TargetType != TargetTypeChannel && evt.TargetType != TargetTypeDM {
		return Event{}, false
	}
	actorID, err := uuid.Parse(evt.Pin.ActorUserID)
	if err != nil {
		return Event{}, false
	}
	evt.Pin.ActorUserID = actorID.String()
	return evt, true
}

// dropClient removes a client and all its subscriptions from hub state,
// then closes the underlying connection.
// It does not hold h.mu while closing the underlying connection.
func (h *Hub) dropClient(c *Client) {
	if c == nil {
		return
	}

	removed := h.removeClient(c)
	if removed != nil {
		removed.close()
		if h.presence != nil {
			// Disconnect is called after removeClient releases h.mu — no lock held.
			h.presence.Disconnect(removed.workspaceID, removed.userID, removed.id)
		}
	}
}

func (h *Hub) addClient(c *Client) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	if _, exists := h.clients[c.id]; exists {
		return false
	}
	h.clients[c.id] = c
	h.clientSubs[c.id] = make(map[string]struct{})
	h.subscriptionGenerations[c.id] = make(map[string]uint64)
	return true
}

func (h *Hub) removeClient(c *Client) *Client {
	h.mu.Lock()
	defer h.mu.Unlock()

	removed, ok := h.clients[c.id]
	if !ok || removed != c {
		return nil
	}
	delete(h.clients, c.id)
	for key := range h.clientSubs[c.id] {
		if set, ok := h.subs[key]; ok {
			delete(set, c.id)
			if len(set) == 0 {
				delete(h.subs, key)
			}
		}
	}
	delete(h.clientSubs, c.id)
	delete(h.subscriptionGenerations, c.id)
	return removed
}

func (h *Hub) clearClients() []*Client {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Snapshot clients so connection close happens after releasing h.mu.
	clients := make([]*Client, 0, len(h.clients))
	for _, c := range h.clients {
		clients = append(clients, c)
	}
	h.clients = make(map[string]*Client)
	h.subs = make(map[string]map[string]struct{})
	h.clientSubs = make(map[string]map[string]struct{})
	h.subscriptionGenerations = make(map[string]map[string]uint64)
	return clients
}

// handleSubscribe applies an already-authorized subscription request.
func (h *Hub) handleSubscribe(req subscribeReq) error {
	c := req.client
	key := targetKey{workspaceID: c.workspaceID, targetType: req.targetType, targetID: req.targetID}.String()

	h.mu.Lock()
	defer h.mu.Unlock()

	if err := req.ctx.Err(); err != nil {
		return err
	}
	registeredClient, registered := h.clients[c.id]
	if !registered || registeredClient != c {
		return ErrClientNotRegistered
	}
	// The target set is created lazily on first subscription; clientSubs is
	// initialized by addClient before a client can subscribe.
	if h.subs[key] == nil {
		h.subs[key] = make(map[string]struct{})
	}
	if _, subscribed := h.clientSubs[c.id][key]; !subscribed {
		h.nextSubscriptionGeneration++
		h.subscriptionGenerations[c.id][key] = h.nextSubscriptionGeneration
	}
	h.subs[key][c.id] = struct{}{}
	h.clientSubs[c.id][key] = struct{}{}
	return nil
}

// handleBroadcast delivers an event to all authorized subscribers of its target.
//
// Authorization is re-checked per client using a fresh background context
// (not the caller's context, which may be cancelled before the hub goroutine
// processes the broadcast). This prevents a cancelled publish context from
// inadvertently revoking subscriptions.
//
// Revocation policy:
//   - allowed=true, no error  → enqueue event; drop client if outbox full.
//   - allowed=false, no error → subscription revoked silently; event not delivered.
//   - any auth error          → skip delivery for this client; subscription kept.
//     A transient DB error must not unsubscribe an authorized client.
//
// It snapshots subscribers under h.mu, then performs auth checks without the
// lock. The final state validation and non-blocking enqueue are atomic with
// unsubscribe/revocation under h.mu.
func (h *Hub) handleBroadcast(req broadcastReq) {
	if req.done != nil {
		defer close(req.done)
	}

	key := broadcastTargetKey(req.event)

	subscriptions := h.broadcastSnapshot(key)
	if len(subscriptions) == 0 {
		return
	}

	for _, subscription := range subscriptions {
		c := subscription.client
		if !h.subscriptionIsCurrent(subscription, key) {
			continue
		}

		// Re-check authorization before delivery using a fresh bounded context.
		// Using context.Background() (not the caller's context) ensures that a
		// cancelled publish context does not affect the auth check or revocation.
		authCtx, cancel := context.WithTimeout(c.ctx, broadcastAuthTimeout)
		allowed, authErr := h.authorizer.CanAccess(authCtx, c.userID, c.workspaceID, req.event.TargetType, req.event.TargetID)

		if authErr != nil {
			cancel()
			// Transient error: skip delivery but keep subscription.
			// The client remains subscribed; the next broadcast will retry.
			h.logger.WarnContext(context.Background(), "ws: auth re-check error on broadcast; skipping delivery",
				"user_id", c.userID,
				"target_type", string(req.event.TargetType),
			)
			continue
		}

		if !allowed {
			cancel()
			// Definitive revocation: access denied with no error.
			_ = h.RevokeSubscription(context.Background(), c, key)
			h.logger.DebugContext(context.Background(), "ws: subscription revoked on broadcast",
				"user_id", c.userID,
				"target_type", string(req.event.TargetType),
			)
			continue
		}

		_, outboxFull := h.enqueueAuthorizedBroadcast(authCtx, subscription, key, req.data)
		cancel()
		if outboxFull {
			// Outbox full: slow client. Drop connection and clean up.
			h.logger.WarnContext(context.Background(), "ws: dropping slow client",
				"user_id", c.userID,
			)
			h.Unregister(c)
			c.close()
		}
	}
}

// handleClientMessage processes a single inbound control message from a WebSocket
// client. This is the entry point the read pump calls for every frame received
// from the client.
//
// Package-private by design: the read pump must live inside package ws so that
// it can access unexported Client fields (workspaceID, userID, id) directly.
// Those fields are server-asserted at connection time; nothing from the msg
// payload should ever be used to identify the caller.
//
// Every message is treated as activity and refreshes the client's presence timer,
// transitioning away → online if needed.
//
// No Hub lock or Presence lock is held on entry; each subordinate call acquires
// its own lock as needed.
//
// TODO(presence-broadcast): once a presence event type is defined in event.go,
// emit a workspace-scoped transition event when status changes away → online.
func (h *Hub) handleClientMessage(ctx context.Context, c *Client, msg ClientMessage) error {
	// Record activity — any inbound frame is evidence the client is present.
	// RecordActivity uses PresenceTracker.mu; Hub.mu is not held.
	if h.presence != nil {
		h.presence.RecordActivity(c.workspaceID, c.userID, c.id)
	}

	switch msg.Type {
	case ClientMessageTypeSubscribe:
		return h.Subscribe(ctx, c, msg.TargetType, msg.TargetID)
	case ClientMessageTypeUnsubscribe:
		key := targetKey{workspaceID: c.workspaceID, targetType: msg.TargetType, targetID: msg.TargetID}.String()
		return h.RevokeSubscription(ctx, c, key)
	case ClientMessageTypePing:
		// Activity already recorded above; nothing else to do.
	case ClientMessageTypeReactionToggle:
		if err := h.handleReactionToggle(ctx, c, msg); err != nil {
			return err
		}
		return nil
	default:
		return fmt.Errorf("ws: unknown client message type %q", msg.Type)
	}
	return nil
}

func (h *Hub) handleReactionToggle(ctx context.Context, c *Client, msg ClientMessage) error {
	if h.reactionHandler == nil || h.reactionLimiter == nil {
		return ErrReactionFeatureDisabled
	}
	if err := validateReactionToggle(msg); err != nil {
		return err
	}
	allowed, err := h.reactionLimiter.Allow(ctx, c.userID)
	if err != nil {
		return fmt.Errorf("ws: reaction rate limit: %w", err)
	}
	if !allowed {
		return ErrReactionRateLimited
	}
	update, err := h.reactionHandler.ToggleReaction(ctx, c.workspaceID, c.userID, msg.MessageID, msg.Emoji)
	if err != nil {
		return fmt.Errorf("ws: toggle reaction: %w", err)
	}
	h.PublishReactionUpdated(ctx, c.workspaceID, c.userID, msg.Emoji, update)
	return nil
}

func validateReactionToggle(msg ClientMessage) error {
	if msg.MessageID == "" {
		return fmt.Errorf("ws: reaction toggle: message_id required")
	}
	if msg.Emoji == "" {
		return fmt.Errorf("ws: reaction toggle: emoji required")
	}
	if msg.TargetType != "" || msg.TargetID != "" {
		return fmt.Errorf("ws: reaction toggle: unexpected target fields")
	}
	if _, err := uuid.Parse(msg.MessageID); err != nil {
		return fmt.Errorf("ws: reaction toggle: invalid message_id format")
	}
	return nil
}

func (h *Hub) broadcastSnapshot(key string) []broadcastSubscription {
	h.mu.RLock()
	defer h.mu.RUnlock()

	subs := h.subs[key]
	if len(subs) == 0 {
		return nil
	}

	subscriptions := make([]broadcastSubscription, 0, len(subs))
	for clientID := range subs {
		c, registered := h.clients[clientID]
		generation, generated := h.subscriptionGenerations[clientID][key]
		if registered && generated {
			subscriptions = append(subscriptions, broadcastSubscription{client: c, generation: generation})
		}
	}
	return subscriptions
}

func (h *Hub) revokeSubscription(client *Client, key string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	registered, ok := h.clients[client.id]
	if !ok || registered != client {
		return false
	}

	if set, ok := h.subs[key]; ok {
		delete(set, client.id)
		if len(set) == 0 {
			delete(h.subs, key)
		}
	}
	if clientSubs, ok := h.clientSubs[client.id]; ok {
		delete(clientSubs, key)
	}
	if generations, ok := h.subscriptionGenerations[client.id]; ok {
		delete(generations, key)
	}
	return true
}

func (h *Hub) subscriptionIsCurrent(subscription broadcastSubscription, key string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()

	return h.subscriptionIsCurrentLocked(subscription, key)
}

func (h *Hub) subscriptionIsCurrentLocked(subscription broadcastSubscription, key string) bool {
	c := subscription.client
	if c == nil || h.clients[c.id] != c {
		return false
	}
	if _, ok := h.subs[key][c.id]; !ok {
		return false
	}
	if _, ok := h.clientSubs[c.id][key]; !ok {
		return false
	}
	return h.subscriptionGenerations[c.id][key] == subscription.generation
}

// enqueueAuthorizedBroadcast makes the final subscription validation and the
// non-blocking outbox enqueue atomic with unsubscribe, revocation, unregister,
// and connection replacement. It never performs storage access or network I/O.
func (h *Hub) enqueueAuthorizedBroadcast(
	ctx context.Context,
	subscription broadcastSubscription,
	key string,
	data []byte,
) (enqueued bool, outboxFull bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if ctx.Err() != nil || h.isShuttingDown() || !h.subscriptionIsCurrentLocked(subscription, key) {
		return false, false
	}
	if !subscription.client.enqueue(data) {
		return false, true
	}
	return true, false
}
