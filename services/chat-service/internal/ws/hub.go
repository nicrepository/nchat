package ws

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

// broadcastAuthTimeout caps the per-client authorization re-check during
// broadcast delivery. If the check exceeds this duration the event is not
// delivered to that client for this broadcast.
const broadcastAuthTimeout = 3 * time.Second

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
	ack    chan struct{} // closed by hub when the client has been registered
}

// subscribeReq is an internal request to add a subscription.
type subscribeReq struct {
	ctx        context.Context
	client     *Client
	targetType TargetType
	targetID   string
	resp       chan error // buffered(1); hub writes result, caller reads
}

// broadcastReq is an internal request to deliver an event.
type broadcastReq struct {
	event Event
	data  []byte // pre-encoded JSON; avoids re-encoding per client
	// done is closed by the hub after the broadcast is fully processed.
	// It is non-nil only when the caller requires synchronization (e.g., tests).
	done chan struct{}
}

// Hub manages all active WebSocket connections for a single chat-service instance.
//
// Distributed fan-out is provided by the optional BroadcastBus (e.g. ValkeyBus).
// When bus is NopBus, delivery is in-process only.
//
// All internal state (clients, subs, clientSubs) is owned exclusively by the
// run goroutine; callers must use the exported methods or the channel-based API
// to ensure race safety.
type Hub struct {
	authorizer SubscriptionAuthorizer
	bus        BroadcastBus
	instanceID string
	logger     *slog.Logger
	busCancel  context.CancelFunc

	register    chan registerReq
	unregister  chan *Client
	subReq      chan subscribeReq
	bcast       chan broadcastReq
	remoteBcast chan broadcastReq // events received from the distributed bus
	quit        chan struct{}
	done        chan struct{}

	// Owned exclusively by the run goroutine — do not access from other goroutines.
	clients    map[string]*Client             // clientID → client
	subs       map[string]map[string]struct{} // targetKey.String() → set of clientIDs
	clientSubs map[string]map[string]struct{} // clientID → set of targetKey strings
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
// Call Shutdown to stop the hub gracefully.
func NewHub(authorizer SubscriptionAuthorizer, logger *slog.Logger, bus BroadcastBus, instanceID string) *Hub {
	if instanceID == "" {
		instanceID = uuid.New().String()
	}
	busCtx, busCancel := context.WithCancel(context.Background())
	h := &Hub{
		authorizer:  authorizer,
		bus:         bus,
		instanceID:  instanceID,
		logger:      logger,
		busCancel:   busCancel,
		register:    make(chan registerReq, 64),
		unregister:  make(chan *Client, 64),
		subReq:      make(chan subscribeReq, 64),
		bcast:       make(chan broadcastReq, 256),
		remoteBcast: make(chan broadcastReq, 256),
		quit:        make(chan struct{}),
		done:        make(chan struct{}),
		clients:     make(map[string]*Client),
		subs:        make(map[string]map[string]struct{}),
		clientSubs:  make(map[string]map[string]struct{}),
	}
	h.bus.Subscribe(busCtx, h.handleRemoteBusEvent)
	go h.run()
	return h
}

// Register adds a client to the hub and blocks until the hub has acknowledged
// the registration. After Register returns the client is guaranteed to be
// visible to Subscribe, so callers may call Subscribe immediately afterwards
// without a race.
func (h *Hub) Register(c *Client) {
	ack := make(chan struct{})
	select {
	case h.register <- registerReq{client: c, ack: ack}:
	case <-h.quit:
		return
	}
	select {
	case <-ack:
	case <-h.quit:
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
	resp := make(chan error, 1)
	req := subscribeReq{
		ctx:        ctx,
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
		return fmt.Errorf("hub is shutting down")
	}
	select {
	case err := <-resp:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// PublishMessageCreated broadcasts a message.created event to all clients
// subscribed to the message's target (channel or DM conversation).
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
func (h *Hub) PublishMessageCreated(ctx context.Context, workspaceID string, targetType TargetType, targetID, messageID string) {
	evt := Event{
		Type:             EventTypeMessageCreated,
		WorkspaceID:      workspaceID,
		TargetType:       targetType,
		TargetID:         targetID,
		MessageID:        messageID,
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
	if err := h.bus.Publish(ctx, evt); err != nil {
		h.logger.WarnContext(ctx, "ws: bus publish failed; local delivery unaffected",
			"workspace_id", workspaceID,
			"target_type", string(targetType),
			"error", err,
		)
	}
}

// Shutdown stops the hub goroutine, cancels bus subscriptions, and closes all
// client connections. Blocks until the run goroutine exits.
func (h *Hub) Shutdown() {
	h.busCancel()
	close(h.quit)
	<-h.done
}

// run is the hub's single background goroutine.
// All state mutations happen here, ensuring race safety without additional locking.
func (h *Hub) run() {
	defer close(h.done)
	for {
		select {
		case req := <-h.register:
			h.clients[req.client.id] = req.client
			h.clientSubs[req.client.id] = make(map[string]struct{})
			close(req.ack)

		case c := <-h.unregister:
			h.dropClient(c)

		case req := <-h.subReq:
			err := h.handleSubscribe(req)
			// resp is buffered(1); send never blocks.
			req.resp <- err

		case req := <-h.bcast:
			h.handleBroadcast(req)

		case req := <-h.remoteBcast:
			// Remote events from the bus: deliver locally only; no re-publish.
			h.handleBroadcast(req)

		case <-h.quit:
			for _, c := range h.clients {
				c.close()
			}
			return
		}
	}
}

// handleRemoteBusEvent is called by the BroadcastBus Subscribe handler.
// It validates the event, suppresses self-echo, encodes to JSON, and posts
// to remoteBcast for the run goroutine to process. This method is called from
// a bus-owned goroutine and must not touch hub state directly.
func (h *Hub) handleRemoteBusEvent(evt Event) {
	// Suppress self-echo: events we published must not be re-delivered locally.
	if evt.SourceInstanceID == h.instanceID {
		return
	}

	// Validate envelope — reject malformed or unknown events fail-secure.
	if !isValidRemoteEvent(evt) {
		h.logger.WarnContext(context.Background(), "ws: dropped invalid remote bus event",
			"event_type", string(evt.Type),
			"target_type", string(evt.TargetType),
		)
		return
	}

	data, err := json.Marshal(evt)
	if err != nil {
		h.logger.ErrorContext(context.Background(), "ws: failed to marshal remote bus event", "error", err)
		return
	}

	select {
	case h.remoteBcast <- broadcastReq{event: evt, data: data}:
	default:
		// Drop if queue is full — bounded, no goroutine block.
		h.logger.WarnContext(context.Background(), "ws: remote broadcast queue full; event dropped",
			"workspace_id", evt.WorkspaceID,
			"target_type", string(evt.TargetType),
		)
	}
}

// isValidRemoteEvent checks that an event received from the bus has the
// required fields and known types before delivery.
func isValidRemoteEvent(evt Event) bool {
	if evt.WorkspaceID == "" || evt.TargetID == "" || evt.SourceInstanceID == "" {
		return false
	}
	switch evt.Type {
	case EventTypeMessageCreated:
		// known
	default:
		return false
	}
	switch evt.TargetType {
	case TargetTypeChannel, TargetTypeDM:
		// known
	default:
		return false
	}
	return true
}

// dropClient removes a client and all its subscriptions from hub state,
// then closes the underlying connection.
// Must be called only from the run goroutine.
func (h *Hub) dropClient(c *Client) {
	if _, ok := h.clients[c.id]; !ok {
		return
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
	c.close()
}

// handleSubscribe processes a subscription request.
// Must be called only from the run goroutine.
func (h *Hub) handleSubscribe(req subscribeReq) error {
	c := req.client
	if _, ok := h.clients[c.id]; !ok {
		return fmt.Errorf("client not registered")
	}
	// Authorization uses only the server-asserted client identity.
	ok, err := h.authorizer.CanAccess(req.ctx, c.userID, c.workspaceID, req.targetType, req.targetID)
	if err != nil {
		return fmt.Errorf("authorization check: %w", err)
	}
	if !ok {
		return ErrSubscribeForbidden
	}
	key := targetKey{workspaceID: c.workspaceID, targetType: req.targetType, targetID: req.targetID}.String()
	if h.subs[key] == nil {
		h.subs[key] = make(map[string]struct{})
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
// Must be called only from the run goroutine.
func (h *Hub) handleBroadcast(req broadcastReq) {
	if req.done != nil {
		defer close(req.done)
	}

	key := targetKey{
		workspaceID: req.event.WorkspaceID,
		targetType:  req.event.TargetType,
		targetID:    req.event.TargetID,
	}.String()

	subs := h.subs[key]
	if len(subs) == 0 {
		return
	}

	for clientID := range subs {
		c, ok := h.clients[clientID]
		if !ok {
			continue
		}

		// Re-check authorization before delivery using a fresh bounded context.
		// Using context.Background() (not the caller's context) ensures that a
		// cancelled publish context does not affect the auth check or revocation.
		authCtx, cancel := context.WithTimeout(context.Background(), broadcastAuthTimeout)
		allowed, authErr := h.authorizer.CanAccess(authCtx, c.userID, c.workspaceID, req.event.TargetType, req.event.TargetID)
		cancel()

		if authErr != nil {
			// Transient error: skip delivery but keep subscription.
			// The client remains subscribed; the next broadcast will retry.
			h.logger.WarnContext(context.Background(), "ws: auth re-check error on broadcast; skipping delivery",
				"user_id", c.userID,
				"target_type", string(req.event.TargetType),
			)
			continue
		}

		if !allowed {
			// Definitive revocation: access denied with no error.
			delete(subs, clientID)
			delete(h.clientSubs[clientID], key)
			if len(subs) == 0 {
				delete(h.subs, key)
			}
			h.logger.DebugContext(context.Background(), "ws: subscription revoked on broadcast",
				"user_id", c.userID,
				"target_type", string(req.event.TargetType),
			)
			continue
		}

		if !c.enqueue(req.data) {
			// Outbox full: slow client. Drop connection and clean up.
			h.logger.WarnContext(context.Background(), "ws: dropping slow client",
				"user_id", c.userID,
			)
			h.dropClient(c)
		}
	}
}
