package ws

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"
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
// In-process only. Distributed fan-out (Valkey/pub-sub) is future scope.
//
// All internal state (clients, subs, clientSubs) is owned exclusively by the
// run goroutine; callers must use the exported methods or the channel-based API
// to ensure race safety.
type Hub struct {
	authorizer SubscriptionAuthorizer
	logger     *slog.Logger

	register   chan registerReq
	unregister chan *Client
	subReq     chan subscribeReq
	bcast      chan broadcastReq
	quit       chan struct{}
	done       chan struct{}

	// Owned exclusively by the run goroutine — do not access from other goroutines.
	clients    map[string]*Client             // clientID → client
	subs       map[string]map[string]struct{} // targetKey.String() → set of clientIDs
	clientSubs map[string]map[string]struct{} // clientID → set of targetKey strings
}

// NewHub creates a Hub and starts its background goroutine.
// Call Shutdown to stop it gracefully.
func NewHub(authorizer SubscriptionAuthorizer, logger *slog.Logger) *Hub {
	h := &Hub{
		authorizer: authorizer,
		logger:     logger,
		register:   make(chan registerReq, 64),
		unregister: make(chan *Client, 64),
		subReq:     make(chan subscribeReq, 64),
		bcast:      make(chan broadcastReq, 256),
		quit:       make(chan struct{}),
		done:       make(chan struct{}),
		clients:    make(map[string]*Client),
		subs:       make(map[string]map[string]struct{}),
		clientSubs: make(map[string]map[string]struct{}),
	}
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
// Authorization is re-checked per subscriber before delivery using a fresh
// background context (not the caller's context, which may already be cancelled
// by the time the hub goroutine processes the broadcast). If the re-check
// returns an error, delivery is skipped for that client but the subscription
// is kept — transient store errors must not unsubscribe authorized clients.
// Only a definitive allowed=false with no error causes a subscription revocation.
//
// Delivery is best-effort in-process; no persistence or durability guarantee.
// Valkey outbox fan-out for durability is future scope.
//
// Wiring: call this from MessageService after a message is persisted.
func (h *Hub) PublishMessageCreated(ctx context.Context, workspaceID string, targetType TargetType, targetID, messageID string) {
	evt := Event{
		Type:        EventTypeMessageCreated,
		WorkspaceID: workspaceID,
		TargetType:  targetType,
		TargetID:    targetID,
		MessageID:   messageID,
		CreatedAt:   time.Now().UTC(),
	}
	data, err := json.Marshal(evt)
	if err != nil {
		h.logger.ErrorContext(ctx, "ws: marshal message.created event", "error", err)
		return
	}
	select {
	case h.bcast <- broadcastReq{event: evt, data: data}:
	case <-ctx.Done():
	case <-h.quit:
	}
}

// Shutdown stops the hub goroutine and closes all client connections.
// Blocks until the goroutine exits.
func (h *Hub) Shutdown() {
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

		case <-h.quit:
			for _, c := range h.clients {
				c.close()
			}
			return
		}
	}
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
