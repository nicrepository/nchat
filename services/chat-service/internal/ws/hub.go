package ws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
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

// presenceSnapshotMaxUsers bounds how many people one presence.snapshot names.
//
// There is no membership cardinality in this domain small enough to bound it for
// us: the roster of a target's *present subscribers* is limited only by how many
// distinct users this instance holds connections for. So the bound is stated
// here, at roughly 110 bytes per entry — about 55 KiB of frame at the limit,
// which is an order of magnitude below what a browser handles comfortably and
// far above any realistic count of people simultaneously connected to one
// channel in a corporate workspace.
//
// Exceeding it does not silently shorten the answer: the snapshot is marked
// incomplete (see PresenceSnapshotResponse.Complete) and the client stops
// inferring anything from who is *missing* from it. A truncated list presented
// as complete is precisely the bug that made absence mean "offline" for people
// the server never spoke about.
const presenceSnapshotMaxUsers = 500

// directoryWriteTimeout and directoryReadTimeout bound every call to the shared
// presence directory. It is a cache of something ephemeral: waiting on it is
// always worse than answering without it, so neither a publish nor a subscribe
// may block on it for long.
const (
	directoryWriteTimeout = 2 * time.Second
	directoryReadTimeout  = 2 * time.Second
)

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

// parseTargetKey is the inverse of String. Subscriptions are stored keyed by the
// encoded form, so the presence fan-out — which addresses events at whatever a
// user happens to be subscribed to — has to read the parts back out. The NUL
// separator cannot occur inside a workspace, type or target ID, so the split is
// exact rather than a guess.
func parseTargetKey(key string) (targetKey, bool) {
	parts := strings.SplitN(key, "\x00", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return targetKey{}, false
	}
	return targetKey{workspaceID: parts[0], targetType: TargetType(parts[1]), targetID: parts[2]}, true
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

// presenceChange is one user's new presence, queued for fan-out (RF-58).
//
// keys names extra targets the event is published to, and derive asks for the
// user's live subscriptions to be resolved at publish time. Most transitions
// only need derive: the user is still connected, so their subscriptions are the
// right audience. A disconnect must name its targets explicitly — by the time
// the tracker reports the user offline, the departing client's subscriptions
// have already been removed from hub state, so deriving would address the event
// to nobody and everyone watching would keep showing a user who has left.
type presenceChange struct {
	workspaceID string
	userID      string
	status      PresenceStatus
	at          time.Time
	keys        []string
	derive      bool
}

// mergeInto folds an older pending change into this newer one.
//
// The state is the newer one's — that is the whole point of coalescing, and it
// is what makes a saturated fan-out lose intermediate states rather than the
// final one. The audiences are unioned instead, because they answer a different
// question: an older change may have named a room the newer one no longer knows
// about (a session that has since gone), and that room still has to be told
// where the user ended up.
func (c presenceChange) mergeInto(older presenceChange) presenceChange {
	merged := c
	merged.derive = c.derive || older.derive
	if len(older.keys) > 0 {
		seen := make(map[string]struct{}, len(c.keys)+len(older.keys))
		keys := make([]string, 0, len(c.keys)+len(older.keys))
		for _, key := range append(append([]string{}, c.keys...), older.keys...) {
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			keys = append(keys, key)
		}
		merged.keys = keys
	}
	return merged
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

// WithPresenceDirectory attaches shared presence state, which is what lets a
// snapshot answer for the whole cluster rather than for this process (RF-58).
//
// Without it a hub that shares a bus with other instances still delivers every
// transition correctly — those cross the bus already — but it cannot claim its
// snapshots are complete, so a client joining a target learns about the people
// on other instances only as they move. With it, a late subscriber is answered
// from state that is already there.
//
// The caller retains ownership and is responsible for closing it after
// Hub.Shutdown returns.
func WithPresenceDirectory(d PresenceDirectory) HubOption {
	return func(h *Hub) { h.directory = d }
}

// WithPresenceInstanceID hands the hub the same process-incarnation identity the
// directory was built with, so the entries it writes and the fields the
// directory owns are attributed to one writer.
//
// It exists to keep the two halves in step, not to make the identity
// configurable: an empty value is ignored, and NewHub has already generated one.
// Nothing outside process startup should call it, and no value that reached this
// process from a client may be passed to it.
func WithPresenceInstanceID(id string) HubOption {
	return func(h *Hub) {
		if id != "" {
			h.presenceInstanceID = id
		}
	}
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
	// instanceID is the operator-facing label from WS_INSTANCE_ID. It names a
	// deployment slot, not a process, so nothing that has to tell two processes
	// apart may use it — see presenceInstanceID, which is what every event this
	// hub publishes is attributed to.
	instanceID string
	// presenceInstanceID identifies *this execution of this process*, both in the
	// shared presence directory and as the origin of every event this hub puts on
	// the bus. It is deliberately not instanceID.
	//
	// instanceID is a logical identity: it comes from WS_INSTANCE_ID, it is
	// configuration, and nothing stops two pods from being handed the same value
	// — a Deployment with a fixed environment variable does exactly that. For
	// echo-suppression on the bus that is merely inefficient. For the directory
	// it is a correctness failure: the field a mutation writes is
	// user|instance, so two processes sharing a configured id would share a
	// field, overwrite each other's assertions, and each delete the other's on
	// disconnect. Every argument for single-writer fencing rests on that field
	// having one writer.
	//
	// So the directory identity is generated here, once per process start, and
	// can be neither configured nor supplied by a client. Uniqueness is a
	// property of the code rather than of a deployment manifest.
	presenceInstanceID string
	logger             *slog.Logger
	busCancel          context.CancelFunc

	presence        *PresenceTracker // optional; nil-safe throughout
	reactionHandler ReactionHandler
	reactionLimiter ReactionLimiter
	callHandler     CallHandler
	callLimiter     CallLimiter
	callStartLimit  int
	callStartWindow int

	// typingStore is the Valkey ghost-state backstop; nil-safe (typing.go).
	typingStore                  TypingStore
	typingLimiter                TypingLimiter
	typingRateLimitMaxActions    int
	typingRateLimitWindowSeconds int

	register        chan registerReq
	unregister      chan *Client
	subReq          chan subscribeReq
	revokeReq       chan revokeSubscriptionReq
	bcast           chan broadcastReq
	remoteBcast     chan broadcastReq // events received from the distributed bus
	presenceSignal  chan struct{}     // capacity 1; wakes the presence fan-out
	reconcileSignal chan struct{}     // capacity 1; wakes targeted reconciliation
	quit            chan struct{}
	done            chan struct{}
	broadcastDone   chan struct{}
	broadcastWG     sync.WaitGroup
	broadcastQueues []chan broadcastReq

	// presenceMu guards the pending presence set only. It is deliberately not
	// h.mu: reporting a presence change must never contend with, or be ordered
	// against, the subscription state a broadcast is reading.
	presenceMu      sync.Mutex
	presencePending map[presenceKey]presenceChange
	presenceOrder   []presenceKey // FIFO of dirty keys, so fan-out is deterministic

	// directory is the cluster-wide view of who is present in a target. Optional;
	// nil means this instance answers from its own connections alone.
	directory PresenceDirectory
	// reconcileMu guards the two pieces of reconciliation bookkeeping:
	// deliveredRosters, what each locally observed target was last told so a pass
	// that finds nothing new stays silent, and pendingReconcile, the set of
	// targets somebody has asked to have rebuilt.
	reconcileMu      sync.Mutex
	deliveredRosters map[string]string
	pendingReconcile map[string]struct{}

	// assertedMu guards asserted, which is this instance's ledger of the
	// assertions it has written to the shared directory.
	//
	// It is emphatically not an authorization input and not a record of where a
	// user has been: every key in it was gated by CanAccess when it was written,
	// and is gated again on every later publish. It answers one operational
	// question — what did I put in the shared store that I am responsible for
	// taking out — because an instance that forgot that would leave a person
	// standing in a room it can no longer see them in, and no other instance can
	// clean up an assertion it does not own.
	//
	// Lifetime: a key is added when an assertion is written and removed when it
	// is withdrawn; the whole entry goes when the user's last connection here
	// does. It is therefore bounded by the conversations that user is actually
	// visible in, and it cannot survive them.
	assertedMu sync.Mutex
	// asserted maps an assertion to the version this instance last confirmed for
	// it. The version is a monotonic counter, not a clock: it exists to order
	// mutations against each other, which two operations sharing a wall-clock
	// tick could not do.
	asserted map[presenceKey]map[string]uint64
	// assertionEpoch is the current version of each assertion's *intent*: what
	// this instance most recently decided should be true of that field.
	//
	// It moves when the intention moves — a subscribe that creates coverage, an
	// unsubscribe that removes it, a state that has to be published, a
	// withdrawal — and not when work happens to start running. That is the whole
	// distinction: a version handed out at execution time makes old work look
	// new simply because it was slow, which is precisely the case that has to be
	// rejected.
	assertionEpoch map[presenceKey]map[string]uint64
	// pendingAssertions are fields this instance may have written and has not
	// confirmed: in flight, or left behind by a write whose outcome is unknown.
	// They are part of what a sweep considers for removal, because a field
	// nobody believes they own is a field nobody ever cleans up.
	pendingAssertions map[presenceKey]map[string]struct{}
	// assertionSeq issues versions for both. One counter for the whole hub, so a
	// version is never reused: an epoch entry can then be dropped when its
	// assertion goes without any risk of a later intent colliding with an older
	// one that is still in flight.
	assertionSeq uint64
	// assertionLocks serialises directory mutations per user, so the writes this
	// process makes to a field it alone owns happen in a defined order.
	assertionLocks *assertionSequencer
	// intentOpened, when set, is called with the versions an intent has just
	// taken. Nil in production.
	//
	// It exists because the ordering this feature depends on is only meaningful
	// at instants a test cannot otherwise name: whether a newer intention already
	// existed while an older operation was inside its I/O is the difference
	// between correct and broken, and waiting for it to appear would make the
	// test agree with a scheduler rather than with the code. Set before the
	// goroutines that read it start.
	intentOpened func(versions map[string]uint64)
	// distributed records that events reach clients on other instances, and so
	// that this hub is not by itself the authority on who is present.
	distributed bool

	mu sync.RWMutex

	clients                    map[string]*Client             // clientID → client
	subs                       map[string]map[string]struct{} // targetKey.String() → set of clientIDs
	clientSubs                 map[string]map[string]struct{} // clientID → set of targetKey strings
	subscriptionGenerations    map[string]map[string]uint64   // clientID → targetKey → generation
	nextSubscriptionGeneration uint64
	// typingActive tracks which targets each client is currently marked typing
	// on, so a disconnect or a lost subscription can promptly broadcast
	// typing.stop instead of waiting out typingTTL. clientID → set of
	// targetKey strings.
	typingActive map[string]map[string]struct{}
}

// NewHub creates a Hub and starts its background goroutine.
//
// bus is the distributed broadcast bus. Pass NopBus{} for single-instance
// deployments or when Valkey is not configured.
//
// instanceID is the operator-facing label for this deployment slot. If empty, a
// UUID is generated automatically. Echo-suppression does not use it: that needs
// an identity two processes cannot share, which is presenceInstanceID.
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
		presenceSignal:          make(chan struct{}, 1),
		reconcileSignal:         make(chan struct{}, 1),
		presencePending:         make(map[presenceKey]presenceChange),
		deliveredRosters:        make(map[string]string),
		asserted:                make(map[presenceKey]map[string]uint64),
		assertionEpoch:          make(map[presenceKey]map[string]uint64),
		pendingAssertions:       make(map[presenceKey]map[string]struct{}),
		assertionLocks:          newAssertionSequencer(),
		presenceInstanceID:      uuid.NewString(),
		quit:                    make(chan struct{}),
		done:                    make(chan struct{}),
		broadcastDone:           make(chan struct{}),
		clients:                 make(map[string]*Client),
		subs:                    make(map[string]map[string]struct{}),
		clientSubs:              make(map[string]map[string]struct{}),
		subscriptionGenerations: make(map[string]map[string]uint64),
		typingActive:            make(map[string]map[string]struct{}),
	}
	for _, opt := range opts {
		opt(h)
	}
	// A bus that carries events off this process means other instances hold
	// connections this hub cannot see, so its own subscriber list is no longer
	// the whole answer to "who is present here" (see sendPresenceSnapshot).
	if _, isLocalOnly := bus.(NopBus); !isLocalOnly {
		h.distributed = true
	}
	h.attachPresenceObserver()
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
		SourceInstanceID: h.presenceInstanceID,
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
		EventID: uuid.New().String(), SourceInstanceID: h.presenceInstanceID, CreatedAt: time.Now().UTC(),
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
		SourceInstanceID: h.presenceInstanceID, CreatedAt: time.Now().UTC(),
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
		SourceInstanceID: h.presenceInstanceID, CreatedAt: time.Now().UTC(),
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

// PublishMembersAdded broadcasts that a channel or group gained participants
// (issue #398).
//
// Callers must invoke it only after the membership transaction has committed:
// the event tells subscribers their view is stale, and one sent for a write that
// then rolled back would make every client refetch its way back to the state it
// already had — or, worse, believe a member exists.
//
// Delivery follows the same route as pin.updated: the local broadcast queue
// re-checks each subscriber's authorization at fan-out, and the bus publish is
// best-effort for other instances. The payload carries no identities at all
// (see MembersAddedPayload), so a subscriber who may read the target learns only
// that it changed and by how much — never who was added.
func (h *Hub) PublishMembersAdded(
	ctx context.Context, workspaceID string, targetType TargetType, targetID, actorUserID string,
	addedCount, memberCount int,
) {
	evt := Event{
		SchemaVersion: CurrentEventSchemaVersion, Type: EventTypeMembersAdded,
		WorkspaceID: workspaceID, TargetType: targetType, TargetID: targetID,
		Members: &MembersAddedPayload{
			ActorUserID: actorUserID, AddedCount: addedCount, MemberCount: memberCount,
		},
		EventID:          uuid.New().String(),
		SourceInstanceID: h.presenceInstanceID, CreatedAt: time.Now().UTC(),
	}
	data, err := json.Marshal(evt)
	if err != nil {
		h.logger.ErrorContext(ctx, "ws: marshal members.added event", "error", err)
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
		h.logger.WarnContext(ctx, "ws: members bus publish failed", "target_type", string(targetType), "error", err)
	}
}

// PublishConversationAvailable delivers a sidebar-invalidation signal to the
// sessions of the given users, wherever those sessions are connected (#398).
//
// This is the one publish path in the hub that is not routed by subscription,
// and the reason is structural: the people this concerns have just been added
// to a channel or group they were not subscribed to, so the room broadcast that
// tells existing members "this changed" reaches everyone except them.
//
// One event is published per recipient rather than one carrying a list. That is
// what lets a receiving instance route without any shared subscription state —
// it reads RecipientUserID and looks up that user's local sessions — and it is
// also what keeps a recipient from learning who else was added, since no event
// ever names more than the user it is addressed to.
//
// Delivery rules, identical locally and remotely:
//   - only the userIDs passed in, which callers must take from the set the
//     transaction actually inserted — never from the request payload;
//   - only sessions in the same workspace;
//   - all live sessions of each user, since any of them may show the sidebar.
//
// The payload names the conversation and nothing else, and it grants no access:
// the client reacts by refetching the sidebar, which re-derives membership
// server-side.
//
// A user with no live session anywhere is simply skipped; their sidebar is
// correct on next load. Bus publication is best-effort, like every other
// publish here: a failure costs a stale sidebar until the next refetch, never
// the membership that was already committed.
func (h *Hub) PublishConversationAvailable(
	ctx context.Context, workspaceID string, targetType TargetType, targetID string, userIDs []string,
) {
	if len(userIDs) == 0 {
		return
	}
	seen := make(map[string]struct{}, len(userIDs))
	for _, userID := range userIDs {
		if userID == "" {
			continue
		}
		// De-duplicated so a repeated ID cannot produce two signals.
		if _, done := seen[userID]; done {
			continue
		}
		seen[userID] = struct{}{}

		evt := Event{
			SchemaVersion: CurrentEventSchemaVersion, Type: EventTypeConversationAvailable,
			WorkspaceID: workspaceID, TargetType: targetType, TargetID: targetID,
			RecipientUserID:  userID,
			EventID:          uuid.New().String(),
			SourceInstanceID: h.presenceInstanceID, CreatedAt: time.Now().UTC(),
		}
		data, err := json.Marshal(evt)
		if err != nil {
			h.logger.ErrorContext(ctx, "ws: marshal conversation.available event", "error", err)
			continue
		}

		// Local sessions first, directly. The bus copy is suppressed on this
		// instance by the SourceInstanceID echo check, so a recipient connected
		// here is told exactly once.
		h.deliverToLocalUserSessions(evt, data)

		if err := h.bus.Publish(ctx, evt); err != nil {
			// Deliberately no user ID in the log line.
			h.logger.WarnContext(ctx, "ws: conversation.available bus publish failed",
				"target_type", string(targetType), "error", err)
		}
	}
}

// PublishMessageLinkSafetyChanged corrects a published message's link-safety
// state for everyone who received it (issue #135).
//
// Routed by (workspace, target) on the ordinary broadcast path — the same routing
// message.created used — because the audience is the same: the message was
// delivered, so every subscriber holding it has to converge. That is the
// difference from PublishMessageBlocked, which is addressed to one author because
// its message was shown to nobody.
//
// Unlike message.created, the payload survives the bus. It can: a message id and
// a closed-set enum are not user content, there is nothing here for a remote node
// to have to re-fetch, and the receiving side re-validates the state against the
// same closed set before delivering it — see canonicalizeLinkSafetyEvent. A
// remote instance therefore cannot inject a state this version does not
// understand, and in particular cannot invent one a client might read as a
// clearance.
//
// The empty state is refused rather than published: "this message has nothing to
// say about links" is not a correction, and announcing it would let a client
// clear a warning on no evidence.
func (h *Hub) PublishMessageLinkSafetyChanged(
	ctx context.Context, workspaceID string, targetType TargetType, targetID, messageID, state string, updatedAt time.Time,
) {
	if workspaceID == "" || targetID == "" || messageID == "" || !isKnownLinkSafetyState(state) || updatedAt.IsZero() {
		return
	}
	evt := Event{
		SchemaVersion: CurrentEventSchemaVersion, Type: EventTypeMessageLinkSafetyChanged,
		WorkspaceID: workspaceID, TargetType: targetType, TargetID: targetID,
		MessageID:        messageID,
		LinkSafety:       &MessageLinkSafetyPayload{MessageID: messageID, State: state, UpdatedAt: updatedAt},
		EventID:          uuid.New().String(),
		SourceInstanceID: h.presenceInstanceID, CreatedAt: time.Now().UTC(),
	}
	data, err := json.Marshal(evt)
	if err != nil {
		h.logger.ErrorContext(ctx, "ws: marshal message.link_safety_changed event", "error", err)
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
		// Deliberately no message id and no state in the log line: the state is a
		// security fact about a specific message, and an operational log is not
		// where it belongs.
		h.logger.WarnContext(ctx, "ws: message.link_safety_changed bus publish failed",
			"target_type", string(targetType), "error", err)
	}
}

// isKnownLinkSafetyState reports whether a state is one of the three this version
// announces.
//
// An allowlist, and the empty state is deliberately outside it. Adding a value to
// domain.MessageLinkSafety must not make it publishable — or relayable — until
// somebody has decided what a client should do with it.
func isKnownLinkSafetyState(state string) bool {
	switch domain.MessageLinkSafety(state) {
	case domain.MessageLinkSafetySafe,
		domain.MessageLinkSafetyInconclusive,
		domain.MessageLinkSafetyMalicious:
		return true
	default:
		return false
	}
}

// canonicalizeLinkSafetyEvent validates a link-safety change arriving over the
// bus.
//
// Three things are checked, and each one closes a way a remote node could say
// something this instance would otherwise relay unexamined: the block has to be
// present, its message id has to be the envelope's own (so the routing key and the
// payload cannot disagree about which message is being corrected), and the state
// has to be one of the three known values.
//
// A failure drops the whole event rather than sanitising it. There is no safe
// partial version of "this message's links changed status".
func canonicalizeLinkSafetyEvent(evt Event) (Event, bool) {
	if evt.LinkSafety == nil || evt.MessageID == "" {
		return Event{}, false
	}
	if evt.LinkSafety.MessageID != evt.MessageID {
		return Event{}, false
	}
	if !isKnownLinkSafetyState(evt.LinkSafety.State) {
		return Event{}, false
	}
	if evt.LinkSafety.UpdatedAt.IsZero() {
		return Event{}, false
	}
	return evt, true
}

// PublishMessageBlocked tells one author their message was refused (RF-21).
//
// Addressed to a single recipient, on the same mechanism conversation.available
// already uses. That is the whole point: a blocked message was shown to nobody,
// so announcing it to the target would tell the conversation that something they
// never saw has been removed.
//
// The payload is the message id and a fixed reason. No body, no URL, no scan id,
// no provider response — the author needs to stop seeing "checking links…", and
// nothing else about the verdict is theirs to receive. reason must be one of
// the MessageBlockedReason* constants; an empty or unrecognised value is a
// caller bug, not something this method may paper over.
func (h *Hub) PublishMessageBlocked(ctx context.Context, workspaceID, recipientUserID, messageID, reason string) {
	if workspaceID == "" || recipientUserID == "" || messageID == "" || reason == "" {
		return
	}
	evt := Event{
		SchemaVersion: CurrentEventSchemaVersion, Type: EventTypeMessageBlocked,
		WorkspaceID: workspaceID, MessageID: messageID,
		RecipientUserID:  recipientUserID,
		Reason:           reason,
		EventID:          uuid.New().String(),
		SourceInstanceID: h.presenceInstanceID, CreatedAt: time.Now().UTC(),
	}
	data, err := json.Marshal(evt)
	if err != nil {
		h.logger.ErrorContext(ctx, "ws: marshal message.blocked event", "error", err)
		return
	}
	// Local sessions first, directly. The bus copy is suppressed on this instance
	// by the SourceInstanceID echo check, so a recipient connected here is told
	// exactly once.
	h.deliverToLocalUserSessions(evt, data)
	if err := h.bus.Publish(ctx, evt); err != nil {
		// Deliberately no user id and no message id in the log line.
		h.logger.WarnContext(ctx, "ws: message.blocked bus publish failed", "error", err)
	}
}

// deliverToLocalUserSessions enqueues data to every live session of
// evt.RecipientUserID in evt.WorkspaceID on this instance.
//
// The workspace match matters as much as the user match: a session connected
// under another workspace context must not be told about this one.
//
// A full outbox is skipped rather than dropping the connection — this is a
// refresh hint, and losing it costs a stale sidebar until the next refetch,
// which is not worth killing a session over.
func (h *Hub) deliverToLocalUserSessions(evt Event, data []byte) {
	if evt.RecipientUserID == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.isShuttingDown() {
		return
	}
	for _, client := range h.clients {
		if client.workspaceID != evt.WorkspaceID || client.userID != evt.RecipientUserID {
			continue
		}
		_ = client.enqueue(data)
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
	// Before anything is torn down: the assertions this instance made are keyed
	// by subscriptions that close(h.quit) is about to discard, and a clean
	// departure is worth saying while it can still be said (RF-58).
	h.withdrawLocalPresence()
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
				//
				// A new connection can change the user's aggregate state: someone
				// who was away because every session had gone idle is online again
				// the moment they open a new one. Presence is aggregated per user,
				// so the people already watching them must be told immediately —
				// waiting for this connection to subscribe would make the update
				// depend on a step that has nothing to do with the transition.
				//
				// The audience is derived from the user's *existing* subscriptions,
				// not from the new connection, which has none yet. A genuinely
				// first connection therefore resolves to an empty audience and
				// publishes nothing; the announcement for that case is what
				// handleSubscribed does when a room finally exists to announce into.
				change := h.presence.Connect(req.client.workspaceID, req.client.userID, req.client.id)
				if change.Changed {
					h.enqueuePresenceChange(presenceChange{
						workspaceID: req.client.workspaceID, userID: req.client.userID,
						status: change.Status, at: change.At, derive: true,
					})
				}
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
	h.broadcastWG.Add(broadcastWorkerCount + 4)
	for index := range broadcastWorkerCount {
		queue := make(chan broadcastReq, broadcastWorkerQueueCapacity)
		h.broadcastQueues[index] = queue
		go h.runBroadcastWorker(queue)
	}
	go h.runBroadcastDispatcher()
	go h.runPresenceFanout()
	go h.runDirectoryHeartbeat()
	go h.runPresenceReconciler()
	go func() {
		h.broadcastWG.Wait()
		close(h.broadcastDone)
	}()
}

// attachPresenceObserver routes the tracker's own transitions into the same
// queue the hub's own reports use, so there is exactly one fan-out path.
//
// The tracker reports only what it decides by itself (online → away). Everything
// the hub causes — connect, disconnect, activity — comes back as a return value
// where it happens, because only there is the connection still known.
func (h *Hub) attachPresenceObserver() {
	if h.presence == nil {
		return
	}
	h.presence.SetObserver(func(workspaceID, userID string, status PresenceStatus, at time.Time) {
		h.enqueuePresenceChange(presenceChange{
			workspaceID: workspaceID, userID: userID, status: status, at: at, derive: true,
		})
	})
}

// runPresenceFanout turns queued presence changes into published events (RF-58).
//
// It exists as its own goroutine because of who reports the changes: the hub's
// run loop (register/unregister), a client's read loop (activity) and the
// tracker's own ticker (away). None of them may block on the broadcast queue —
// the first would stall every other client, the second would stall that client's
// reads, and the third would delay away detection for everyone. Handing the work
// to one goroutine keeps all three reporting in constant time.
//
// It is joined by broadcastWG, so Shutdown waits for it like every other pump.
func (h *Hub) runPresenceFanout() {
	defer h.broadcastWG.Done()
	for {
		changes := h.takePresenceChanges()
		if len(changes) == 0 {
			select {
			case <-h.presenceSignal:
				continue
			case <-h.quit:
				return
			}
		}
		for _, change := range changes {
			select {
			case <-h.quit:
				return
			default:
			}
			h.publishPresence(change)
		}
	}
}

// enqueuePresenceChange records a change without ever blocking its reporter and
// without ever losing a user's final state.
//
// It is a coalescing set, not a queue, and that is the difference that matters:
// a bounded queue under pressure drops whatever arrives last, which is exactly
// the state that had to survive. Here a second change for the same user replaces
// the first, so a slow fan-out costs the intermediate states — online → away →
// offline collapses to offline — and never the outcome. There is nothing to
// recover later because nothing was thrown away, which is why no resynchronisation
// timer is needed.
//
// The map is bounded by the number of distinct connected users, not by traffic:
// a user who transitions a thousand times occupies one entry. The FIFO of keys
// keeps fan-out order deterministic without making the map grow.
func (h *Hub) enqueuePresenceChange(change presenceChange) {
	if change.workspaceID == "" || change.userID == "" {
		return
	}
	key := presenceKey{workspaceID: change.workspaceID, userID: change.userID}

	h.presenceMu.Lock()
	if pending, queued := h.presencePending[key]; queued {
		h.presencePending[key] = change.mergeInto(pending)
	} else {
		h.presencePending[key] = change
		h.presenceOrder = append(h.presenceOrder, key)
	}
	h.presenceMu.Unlock()

	// Capacity 1: a signal already waiting will wake a fan-out that has not yet
	// drained, and the drain takes everything pending, so a dropped signal can
	// never leave work behind.
	select {
	case h.presenceSignal <- struct{}{}:
	default:
	}
}

// takePresenceChanges drains the pending set in the order the users were first
// marked dirty. The slices are rebuilt rather than reused so a publish, which
// runs without the lock, can never read a buffer a reporter is writing to.
func (h *Hub) takePresenceChanges() []presenceChange {
	h.presenceMu.Lock()
	defer h.presenceMu.Unlock()

	if len(h.presenceOrder) == 0 {
		return nil
	}
	changes := make([]presenceChange, 0, len(h.presenceOrder))
	for _, key := range h.presenceOrder {
		changes = append(changes, h.presencePending[key])
		delete(h.presencePending, key)
	}
	h.presenceOrder = nil
	return changes
}

// publishPresence emits one presence.updated per target the change is addressed
// to, locally and over the bus.
//
// The addressing is what makes this safe to fan out at all: a presence event is
// only ever published into a channel or a conversation the user is subscribed
// to, so it reaches the people who already share that target with them — and
// each of those is re-authorized at delivery like any other broadcast. There is
// no path here that publishes to a workspace, so no reader can learn about
// someone they share nothing with, whatever their role.
//
// A user in several shared targets produces several events for the same state.
// That is deliberate: the alternative is a per-recipient computation the fan-out
// would have to do against the database, and the duplicates are free to absorb —
// the payload is idempotent and carries the instant the server decided it, so
// applying it twice is indistinguishable from applying it once.
func (h *Hub) publishPresence(change presenceChange) {
	if change.workspaceID == "" || change.userID == "" {
		return
	}
	// Copied rather than appended in place: the change's own slice must not be
	// grown here, and the loop below de-duplicates whatever overlap the two
	// sources produce.
	keys := append([]string{}, change.keys...)
	if change.derive {
		keys = append(keys, h.subscribedTargetKeys(change.workspaceID, change.userID)...)
	}
	payload := PresencePayload{
		UserID:    change.userID,
		State:     string(change.status),
		UpdatedAt: formatPresenceTime(change.at),
	}
	ctx := context.Background()

	// Every target is settled before anything is written or sent, so the shared
	// directory and the events describe exactly the same set: a room the subject
	// may no longer be seen in gets neither.
	allowed, parsedKeys := h.allowedPresenceTargets(ctx, change, keys)

	// The shared directory is written on the same keys the events go to, at the
	// same moment and from the same decision, so the two cannot describe
	// different worlds. It is best-effort: a failure costs a later snapshot its
	// completeness, never a delivery.
	h.recordInDirectory(ctx, change, allowed)

	for _, parsed := range parsedKeys {
		presence := payload
		evt := Event{
			SchemaVersion: CurrentEventSchemaVersion, Type: EventTypePresenceUpdated,
			WorkspaceID: parsed.workspaceID, TargetType: parsed.targetType, TargetID: parsed.targetID,
			Presence: &presence, EventID: uuid.New().String(),
			SourceInstanceID: h.presenceInstanceID, CreatedAt: time.Now().UTC(),
		}
		data, err := json.Marshal(evt)
		if err != nil {
			h.logger.ErrorContext(ctx, "ws: marshal presence.updated event", "error", err)
			continue
		}
		select {
		case h.bcast <- broadcastReq{event: evt, data: data}:
		case <-h.quit:
			return
		}
		if err := h.bus.Publish(ctx, evt); err != nil {
			h.logger.WarnContext(ctx, "ws: presence bus publish failed",
				"target_type", string(parsed.targetType), "error", err)
		}
	}
}

// allowedPresenceTargets narrows a change's candidate targets to the ones it may
// actually be published into: de-duplicated, in this workspace, of a kind that
// routes by subscription, and where the subject may still be seen.
//
// Extracted so publishPresence states what it does — write the directory, then
// send one event per target — without the filtering that decides which targets
// those are.
func (h *Hub) allowedPresenceTargets(
	ctx context.Context, change presenceChange, keys []string,
) (allowed []string, parsed []targetKey) {
	seen := make(map[string]struct{}, len(keys))
	allowed = make([]string, 0, len(keys))
	parsed = make([]targetKey, 0, len(keys))
	for _, key := range keys {
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		target, ok := parseTargetKey(key)
		if !ok || target.workspaceID != change.workspaceID {
			continue
		}
		if target.targetType != TargetTypeChannel && target.targetType != TargetTypeDM {
			continue
		}
		if !h.subjectBelongsTo(ctx, change, target, key) {
			continue
		}
		allowed = append(allowed, key)
		parsed = append(parsed, target)
	}
	return allowed, parsed
}

// subjectMayAppear answers, for one person and one target, the question the
// directory cannot: may this subject still be seen here at all?
//
// The directory stores presence assertions; it is not a roster and never was.
// It says "somebody asserted this state", not "this person belongs in this
// conversation" — and a person who loses access while idle produces no
// transition, so their assertion can sit there indefinitely. Anything that
// turns directory contents into something a user sees has to ask this first.
//
// It is the same SubscriptionAuthorizer every other decision uses, so Guest and
// every RBAC rule are evaluated in exactly one place. A transient error is not
// permission: it excludes the subject, which for a snapshot means the answer is
// no longer complete rather than quietly shorter.
// It answers in two parts because "no" and "could not tell" are different
// facts: a denial is a complete answer about this person, an error is not, and a
// roster built from errors must not be presented as complete.
func (h *Hub) subjectVisibility(workspaceID, userID string, target targetKey) (allowed, decided bool) {
	ctx, cancel := context.WithTimeout(context.Background(), broadcastAuthTimeout)
	permitted, err := h.authorizer.CanAccess(ctx, userID, workspaceID, target.targetType, target.targetID)
	cancel()
	if err != nil {
		h.logger.WarnContext(context.Background(), "ws: presence subject authorization error",
			"operation", "roster", "target_type", string(target.targetType),
		)
		return false, false
	}
	return permitted, true
}

// subjectMayAppear is subjectVisibility for callers that treat "could not tell"
// as "not now" — which every writer does, since neither an assertion nor a lease
// may rest on an unanswered question.
func (h *Hub) subjectMayAppear(workspaceID, userID string, target targetKey) bool {
	allowed, _ := h.subjectVisibility(workspaceID, userID, target)
	return allowed
}

// subjectBelongsTo decides whether the person a presence change is about may
// still be seen in a target at all (SR-1).
//
// Two different questions have to be asked before presence reaches a screen, and
// only one of them used to be. The fan-out has always re-checked the *recipient*
// — may this subscriber read this conversation — but the *subject* was taken from
// wherever they had subscribed, and a subscription outlives the membership that
// created it: someone removed from a private channel kept a live subscription
// until the next event revoked it, and every state change they made in the
// meantime was published into a room they no longer belonged to.
//
// So the subject is re-derived here from the same authority that decides
// everything else, SubscriptionAuthorizer.CanAccess. There is no second
// predicate and no separate notion of membership, which is what keeps Guest and
// every RBAC rule evaluated in exactly one place.
//
// A denial is acted on rather than merely skipped: the assertion is removed from
// the shared directory and an offline is delivered to that target, so the people
// still watching stop seeing someone who is no longer there instead of keeping
// the last state they were told. A transient error skips the target and changes
// nothing; the next transition, or reconciliation, will ask again.
//
// The cost is bounded by the subject's own live subscriptions — their open
// conversations, not the workspace — and presence changes are rare per person:
// connect, away, back, disconnect.
func (h *Hub) subjectBelongsTo(ctx context.Context, change presenceChange, parsed targetKey, key string) bool {
	authCtx, cancel := context.WithTimeout(ctx, broadcastAuthTimeout)
	allowed, err := h.authorizer.CanAccess(authCtx, change.userID, change.workspaceID, parsed.targetType, parsed.targetID)
	cancel()

	if err != nil {
		h.logger.WarnContext(ctx, "ws: presence subject authorization error; skipping target",
			"target_type", string(parsed.targetType),
		)
		return false
	}
	if allowed {
		return true
	}
	// Offline is already a departure; publishing it again for someone who has
	// lost access would say the same thing twice.
	if change.status != PresenceOffline {
		h.forgetSubject(ctx, change, key)
	}
	h.logger.DebugContext(ctx, "ws: presence withheld from a target the subject cannot access",
		"target_type", string(parsed.targetType),
	)
	return false
}

// forgetSubject removes a subject from a target they may no longer be seen in,
// and tells the room, so an observer's last view of them does not simply freeze.
func (h *Hub) forgetSubject(ctx context.Context, change presenceChange, key string) {
	// Through the sequencer like every other removal, so a revocation cannot
	// delete a field a concurrent write is about to own.
	h.applyAssertionForget(ctx, h.openForgetIntent(change.workspaceID, change.userID, []string{key}))
	parsed, ok := parseTargetKey(key)
	if !ok {
		return
	}
	presence := PresencePayload{
		UserID: change.userID, State: string(PresenceOffline),
		UpdatedAt: formatPresenceTime(change.at),
	}
	evt := Event{
		SchemaVersion: CurrentEventSchemaVersion, Type: EventTypePresenceUpdated,
		WorkspaceID: parsed.workspaceID, TargetType: parsed.targetType, TargetID: parsed.targetID,
		Presence: &presence, EventID: uuid.New().String(),
		SourceInstanceID: h.presenceInstanceID, CreatedAt: time.Now().UTC(),
	}
	data, err := json.Marshal(evt)
	if err != nil {
		return
	}
	select {
	case h.bcast <- broadcastReq{event: evt, data: data}:
	case <-h.quit:
		return
	}
	if err := h.bus.Publish(ctx, evt); err != nil {
		h.logger.WarnContext(ctx, "ws: presence departure bus publish failed", "error", err)
	}
}

// recordInDirectory brings the shared roster in step with what was published.
//
// It delegates to the one place that owns directory mutation, so publishing an
// event and owning an assertion stay separate concerns: this function decides
// nothing about coverage, retries or leases.
//
// Offline deletes rather than stores: the directory answers "who is present",
// and a tombstone for everyone who ever left would make it grow without bound
// while answering nothing a missing entry does not already answer.
func (h *Hub) recordInDirectory(ctx context.Context, change presenceChange, keys []string) {
	if h.directory == nil || len(keys) == 0 {
		return
	}
	if change.status == PresenceOffline {
		h.applyAssertionForget(ctx, h.openForgetIntent(change.workspaceID, change.userID, keys))
		return
	}
	intent := h.openRecordIntent(change.workspaceID, change.userID, keys, change.status, change.at)
	h.applyAssertionRecord(ctx, intent)
}

// applyAssertionRecord is the only place in production that writes an assertion
// to the shared directory.
//
// Everything a mutation needs to be correct is decided here, under the user's
// sequencer, and the order of the steps is the design:
//
//  1. Is this intent still the current one? A publication can sit in the queue
//     across an unsubscribe and a resubscribe; when it finally runs, the field
//     it would write has a newer intention behind it, and the state it carries
//     is simply out of date. It is rejected before any I/O — not repaired
//     afterwards, because a repair is a second write racing the first.
//  2. Is the target still covered here? Coverage can end without the intent
//     being superseded.
//  3. Only then, the write.
//
// The version is not issued here. It was issued when the intention was formed,
// which is what lets step 1 tell old work from new work at all: a version taken
// at execution time would make the slowest write look like the newest decision.
//
// A failed write confirms nothing (INV-1, INV-3): the target stays desired, so
// the next reconciliation retries it — no retry loop, no goroutine, no backoff.
func (h *Hub) applyAssertionRecord(ctx context.Context, intent assertionIntent) {
	if h.directory == nil || len(intent.versions) == 0 {
		return
	}
	pk := presenceKey{workspaceID: intent.workspaceID, userID: intent.userID}
	unlock := h.assertionLocks.lock(pk)
	defer unlock()

	current := h.currentIntentKeys(intent)
	if len(current) == 0 {
		return // superseded while it waited; the newer intent owns this field
	}

	// Coverage only. Whether the subject may be seen here was decided when this
	// intent was created, one authorization call per change per target, and the
	// epoch above is what makes that decision current rather than remembered.
	covered := h.coveredTargetsForUser(intent.workspaceID, intent.userID)
	wanted := make([]string, 0, len(current))
	for _, key := range current {
		if _, still := covered[key]; still {
			wanted = append(wanted, key)
		}
	}
	if len(wanted) == 0 {
		return
	}

	h.markAssertionsPending(pk, wanted)
	writeCtx, cancel := context.WithTimeout(ctx, directoryWriteTimeout)
	err := h.directory.Record(writeCtx, DirectoryEntry{
		UserID:     intent.userID,
		State:      intent.state,
		At:         intent.at,
		InstanceID: h.presenceInstanceID,
	}, wanted)
	cancel()
	if err != nil {
		// Pending stays set: the write may have landed anyway, and a field
		// nobody believes they own is a field nobody would ever remove.
		h.logger.WarnContext(ctx, "ws: presence directory record failed",
			"operation", "record", "error", err)
		return
	}

	// A newer intention can have been formed while this was in flight. Confirming
	// the old version would leave the ledger describing a decision that has been
	// replaced; the replacement is already queued on this same sequencer and will
	// write the field next, so there is nothing to compensate for and nothing to
	// undo. What must not happen is this call claiming the field as settled.
	h.confirmAssertions(intent.workspaceID, intent.userID, intent.versionsFor(h.currentIntentKeys(intent)))
}

// applyAssertionForget is the only place in production that removes an assertion
// from the shared directory.
//
// Every lifecycle that ends coverage routes here — unsubscribe, disconnect,
// membership or session revocation, a subject who lost access, shutdown,
// reconciliation — so a withdrawal is ordered against the writes for the same
// person instead of racing them. A removal that reached the directory directly
// would carry no generation of its own, and the field it deletes might already
// belong to a newer assertion.
//
// A failed withdrawal leaves the entry confirmed on purpose. It is the record
// that something of ours is still out there needing removal, and dropping it
// would strand a row in the shared store that no instance believes it owns —
// which nothing would ever clean up, because the target key's lease is kept
// alive by everybody else in the conversation.
func (h *Hub) applyAssertionForget(ctx context.Context, intent assertionIntent) {
	if h.directory == nil || len(intent.versions) == 0 {
		return
	}
	pk := presenceKey{workspaceID: intent.workspaceID, userID: intent.userID}
	unlock := h.assertionLocks.lock(pk)
	defer unlock()

	current := h.currentIntentKeys(intent)
	if len(current) == 0 {
		return // a newer intention owns this field; deleting it would take theirs
	}

	// Desired, not merely covered: a subject who lost access is still subscribed,
	// and this is the path that has to take their assertion out.
	stale := current
	if !intent.unconditional {
		desired := h.desiredAssertions(intent.workspaceID, intent.userID)
		stale = make([]string, 0, len(current))
		for _, key := range current {
			if _, wanted := desired[key]; wanted {
				continue // came back before this ran; removing it would delete live coverage
			}
			stale = append(stale, key)
		}
	}
	if len(stale) == 0 {
		return
	}

	writeCtx, cancel := context.WithTimeout(ctx, directoryWriteTimeout)
	err := h.directory.Forget(writeCtx, intent.workspaceID, intent.userID, stale)
	cancel()
	if err != nil {
		h.logger.WarnContext(ctx, "ws: presence directory forget failed",
			"operation", "forget", "error", err)
		return
	}
	// The field is gone, so the ledger says so — and the intention is retired only
	// if this removal is still the last word on it. An intent formed while the
	// HDEL was in flight is waiting on this same sequencer to write the field
	// back, and it must still be there to do it.
	removed := intent.versionsFor(stale)
	h.unconfirmAssertions(intent.workspaceID, intent.userID, removed)
	h.retireAssertionIntents(intent.workspaceID, intent.userID, removed)
}

// confirmAssertions records the version the directory accepted.
//
// A confirmation never moves backwards: a slower write finishing after a newer
// one must not leave the ledger describing the older of the two (INV-2). The
// key stops being pending in the same breath — it is now owned, not merely
// suspected.
func (h *Hub) confirmAssertions(workspaceID, userID string, versions map[string]uint64) {
	if len(versions) == 0 {
		return
	}
	pk := presenceKey{workspaceID: workspaceID, userID: userID}
	h.assertedMu.Lock()
	defer h.assertedMu.Unlock()
	owned := h.asserted[pk]
	if owned == nil {
		owned = make(map[string]uint64, 8)
		h.asserted[pk] = owned
	}
	pending := h.pendingAssertions[pk]
	for key, version := range versions {
		delete(pending, key)
		if existing, known := owned[key]; known && existing >= version {
			continue
		}
		owned[key] = version
	}
	if len(pending) == 0 {
		delete(h.pendingAssertions, pk)
	}
}

// markAssertionsPending notes fields this instance is about to write, before the
// write happens.
//
// Between the HSET leaving and its reply arriving, the field exists and nothing
// in the ledger says so. If coverage ends in that window the sweep would find
// nothing to withdraw, and the assertion would sit in the shared store until its
// lease expired — which it would not, because everyone else in the conversation
// keeps that key alive. Pending is what makes such a field visible to the sweep.
func (h *Hub) markAssertionsPending(pk presenceKey, keys []string) {
	h.assertedMu.Lock()
	defer h.assertedMu.Unlock()
	pending := h.pendingAssertions[pk]
	if pending == nil {
		pending = make(map[string]struct{}, len(keys))
		h.pendingAssertions[pk] = pending
	}
	for _, key := range keys {
		pending[key] = struct{}{}
	}
}

// unconfirmAssertions records that a removal reached the directory: the field is
// gone, so this instance no longer owns it and no longer suspects it.
//
// It touches the ledger and nothing else. The intent epoch is a different fact
// with a different lifetime — what should be true of that field next — and
// conflating the two is what let a completed withdrawal cancel the intention
// formed while it was running.
//
// The confirmation is removed only if it belongs to this removal or something
// older. A confirmation newer than the version being retired describes a write
// this call did not undo, and must survive it.
func (h *Hub) unconfirmAssertions(workspaceID, userID string, versions map[string]uint64) {
	pk := presenceKey{workspaceID: workspaceID, userID: userID}
	h.assertedMu.Lock()
	defer h.assertedMu.Unlock()
	owned := h.asserted[pk]
	pending := h.pendingAssertions[pk]
	for key, version := range versions {
		if confirmed, known := owned[key]; known && confirmed > version {
			continue
		}
		delete(owned, key)
		// The HDEL proved the field is not there, whatever was suspected.
		delete(pending, key)
	}
	if len(owned) == 0 {
		delete(h.asserted, pk)
	}
	if len(pending) == 0 {
		delete(h.pendingAssertions, pk)
	}
}

// retireAssertionIntents drops the intents that a completed operation has just
// satisfied, and only those.
//
// Compare-and-delete, under the one mutex that guards the epochs, because the
// question it asks can change the instant it is answered: an intent is opened
// without the user's sequencer, so a resubscribe can raise the epoch while a
// withdrawal is still inside its HDEL. Retiring the key unconditionally there
// deleted the newer intention, and the Record that was already queued behind the
// withdrawal then found nothing to do — leaving the conversation empty for
// somebody who had come back.
//
// Equality, not "less than or equal": a newer intention is preserved, never
// merged into the one that happens to be finishing.
//
// It is still a delete and not a never-delete: an epoch that is the last word on
// its assertion goes, so the map is bounded by live assertions rather than by
// every target anyone was ever in.
func (h *Hub) retireAssertionIntents(workspaceID, userID string, versions map[string]uint64) {
	pk := presenceKey{workspaceID: workspaceID, userID: userID}
	h.assertedMu.Lock()
	defer h.assertedMu.Unlock()

	epochs := h.assertionEpoch[pk]
	if epochs == nil {
		return
	}
	for key, version := range versions {
		current, known := epochs[key]
		if !known || current != version {
			continue // a newer intention owns this field; it is not ours to retire
		}
		delete(epochs, key)
	}
	// Safe to drop the whole entry: versions come from one counter that never
	// reissues a value, so an intent still in flight can never match a later one.
	if len(epochs) == 0 {
		delete(h.assertionEpoch, pk)
	}
}

// confirmedAssertionUsers returns every user this instance still has confirmed
// or unsettled assertions for, so a sweep can revisit them without scanning
// connections.
//
// Pending counts. A user whose last connection closed while a write was in
// flight has no coverage to be found by, and the field they may have left in the
// shared store is only reachable through this list.
func (h *Hub) confirmedAssertionUsers() []presenceKey {
	h.assertedMu.Lock()
	defer h.assertedMu.Unlock()

	users := make([]presenceKey, 0, len(h.asserted)+len(h.pendingAssertions))
	seen := make(map[presenceKey]struct{}, len(users))
	for _, source := range []map[presenceKey]struct{}{keySet(h.asserted), keySet(h.pendingAssertions)} {
		for pk := range source {
			if _, dup := seen[pk]; dup {
				continue
			}
			seen[pk] = struct{}{}
			users = append(users, pk)
		}
	}
	return users
}

// keySet is the set of keys of a map, so two ledgers can be walked as one.
func keySet[V any](m map[presenceKey]V) map[presenceKey]struct{} {
	set := make(map[presenceKey]struct{}, len(m))
	for k := range m {
		set[k] = struct{}{}
	}
	return set
}

// runDirectoryHeartbeat renews this instance's liveness so the entries it wrote
// keep counting, and stops renewing the moment it goes away — which is what
// makes a killed instance's users converge to offline elsewhere without anything
// having to notice the death.
func (h *Hub) runDirectoryHeartbeat() {
	defer h.broadcastWG.Done()
	if h.directory == nil {
		return
	}
	h.heartbeatDirectory()

	ticker := time.NewTicker(instanceHeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			h.heartbeatDirectory()
		case <-h.quit:
			return
		}
	}
}

func (h *Hub) heartbeatDirectory() {
	ctx, cancel := context.WithTimeout(context.Background(), directoryWriteTimeout)
	if err := h.directory.Heartbeat(ctx); err != nil {
		h.logger.WarnContext(ctx, "ws: presence directory heartbeat failed", "error", err)
	}
	cancel()
	// The same beat is where the assertion delta is retried and the leases are
	// renewed, in that order: a write that failed earlier gets another chance
	// before anything decides what is still worth renewing. A user can be online
	// all day without producing a transition, and nothing else would keep their
	// roster from expiring underneath them (CQ-2).
	h.reconcileAllAssertions()
	h.refreshAssertionLeases()
}

// handleSubscribed completes a subscribe with the two presence exchanges that
// make an avatar correct the moment a room is joined (RF-58).
//
// Joining is the only moment both halves are knowable: the client learns who is
// already here, and everyone here learns about the client. Neither can be done
// at connect time — a freshly registered client is in no room yet — and doing
// only one of them leaves a permanent gap, since whoever subscribed first would
// never hear about whoever subscribed second.
//
// It runs on the caller's read-loop goroutine, after the subscribe has been
// authorized and acknowledged, so a slow snapshot costs that one client and
// nothing else.
//
// previousGeneration is what the client's subscription generation for this
// target was *before* the subscribe. Subscribing to a target one already holds
// is idempotent in the hub — the generation does not move — and it has to be
// idempotent here too: a client that resends subscribe, whether by retry or by
// design, would otherwise make the server rebuild and resend a snapshot, and
// re-announce the subscriber to the whole room, on demand. Nothing has joined,
// so nothing is emitted.
func (h *Hub) handleSubscribed(c *Client, targetType TargetType, targetID string, previousGeneration uint64) {
	if h.presence == nil || c == nil {
		return
	}
	key := targetKey{workspaceID: c.workspaceID, targetType: targetType, targetID: targetID}.String()
	if h.subscriptionGeneration(c, key) == previousGeneration {
		return
	}

	if !h.mayStillRead(c, targetType, targetID, key) {
		return
	}

	h.sendPresenceSnapshot(c, targetType, targetID, key)

	// The announcement. Offline is not published: it would be the state of a
	// user who is, by construction, connected right now.
	status, at := h.presence.StatusAt(c.workspaceID, c.userID)
	if status == PresenceOffline {
		return
	}
	h.enqueuePresenceChange(presenceChange{
		workspaceID: c.workspaceID, userID: c.userID,
		status: status, at: at, keys: []string{key},
	})
}

// mayStillRead re-checks that this client may read the target, immediately
// before the snapshot is built (SR-1).
//
// The subscribe authorized the subscription; the snapshot happens afterwards,
// and a membership can be revoked in between. Every other delivery path already
// re-checks at fan-out (handleBroadcast); the snapshot is the one that did not,
// and it is the single largest disclosure in this protocol — the roster of who
// is present in a target — so it is the last place that should take an earlier
// answer on trust.
//
// It uses the same SubscriptionAuthorizer as everything else. There is no second
// predicate, so Guest and every RBAC rule are evaluated exactly once, in the
// place that already owns that decision.
//
// A definitive denial revokes the subscription that was just created and answers
// with the protocol's existing generic code, which does not distinguish "does
// not exist" from "not allowed". A transient error sends no snapshot and keeps
// the subscription, matching handleBroadcast: a database blip must not
// unsubscribe an authorized client, and the next broadcast re-checks anyway.
func (h *Hub) mayStillRead(c *Client, targetType TargetType, targetID, key string) bool {
	ctx, cancel := context.WithTimeout(c.ctx, broadcastAuthTimeout)
	allowed, err := h.authorizer.CanAccess(ctx, c.userID, c.workspaceID, targetType, targetID)
	cancel()

	if err != nil {
		h.logger.WarnContext(context.Background(), "ws: presence snapshot authorization error; skipping snapshot",
			"target_type", string(targetType),
		)
		return false
	}
	if allowed {
		return true
	}

	_ = h.RevokeSubscription(context.Background(), c, key)
	h.logger.DebugContext(context.Background(), "ws: access revoked before presence snapshot",
		"target_type", string(targetType),
	)
	handleSubscribeClientError(c, ErrSubscribeForbidden)
	return false
}

// sendPresenceSnapshot tells one client who is present in a target it has just
// been authorized for.
//
// The roster comes from this hub's own subscriber set, not from the database:
// the people who can be reported are exactly the people whose later
// presence.updated events would reach this client anyway, so the snapshot
// discloses nothing the stream does not, and it costs no query.
//
// Where the roster comes from, and what "complete" is allowed to mean, follow
// from one question: is this instance the whole cluster?
//
//   - No bus. It is. Its own connections are every connection there is, so the
//     local roster is complete and says so.
//   - A bus and a directory. The directory is the shared authority; what it
//     returns is complete for the cluster, and it is what answers a client that
//     joins a target long after everybody else did — no new transition required,
//     because the state was already written down.
//   - A bus and no directory, or a directory that failed. This instance can only
//     speak for itself, so the roster it sends is marked incomplete. Every entry
//     in it is still true, and the client will not read offline into anyone
//     missing from it.
func (h *Hub) sendPresenceSnapshot(c *Client, targetType TargetType, targetID, key string) {
	target, ok := parseTargetKey(key)
	if !ok {
		return
	}
	users, complete := h.presenceRoster(c, key, target)
	if len(users) > presenceSnapshotMaxUsers {
		// Stop, and say so. The entries kept are still true — each names one
		// person the server holds a connection for — but the client must not
		// read anything into who is missing from a list that was cut short.
		users = users[:presenceSnapshotMaxUsers]
		complete = false
		h.logger.WarnContext(context.Background(), "ws: presence snapshot truncated",
			"target_type", string(targetType), "limit", presenceSnapshotMaxUsers,
		)
	}
	data, err := json.Marshal(PresenceSnapshotResponse{
		Type: PresenceSnapshotType, TargetType: targetType, TargetID: targetID,
		Users: users, Complete: complete, TakenAt: formatPresenceTime(time.Now().UTC()),
	})
	if err != nil {
		h.logger.ErrorContext(context.Background(), "ws: marshal presence snapshot", "error", err)
		return
	}
	// A full outbox is not worth dropping the connection over: the client is
	// already behind, and the next snapshot or event repairs the gap.
	_ = c.enqueue(data)
}

// aggregateRoster collapses the directory's per-instance assertions into one
// entry per person, sorted so the same roster always serialises identically —
// which is what lets reconciliation compare two reads and stay quiet when
// nothing moved.
func aggregateRoster(entries []DirectoryEntry) []PresencePayload {
	byUser := make(map[string][]DirectoryEntry, len(entries))
	for _, entry := range entries {
		byUser[entry.UserID] = append(byUser[entry.UserID], entry)
	}
	users := make([]PresencePayload, 0, len(byUser))
	for userID, assertions := range byUser {
		state, at, ok := aggregatePresence(assertions)
		if !ok || state == PresenceOffline {
			continue
		}
		users = append(users, PresencePayload{
			UserID: userID, State: string(state), UpdatedAt: formatPresenceTime(at),
		})
	}
	sort.Slice(users, func(i, j int) bool { return users[i].UserID < users[j].UserID })
	return users
}

// presenceRoster returns who is present in a target and whether that answer is
// authoritative for the whole cluster.
func (h *Hub) presenceRoster(c *Client, key string, target targetKey) (users []PresencePayload, complete bool) {
	if h.directory != nil {
		ctx, cancel := context.WithTimeout(c.ctx, directoryReadTimeout)
		entries, err := h.directory.Present(ctx, key)
		cancel()
		if err == nil {
			return h.authorizedRoster(c.workspaceID, target, aggregateRoster(entries))
		}
		h.logger.WarnContext(context.Background(), "ws: presence directory read failed; local roster only",
			"operation", "roster", "error", err,
		)
	}

	return h.localRoster(target, key)
}

// localRoster answers "who is present in this target" from this instance's own
// connections, and reports whether that answer is authoritative.
//
// It is the single-node authority: the live subscriptions to the target, the
// tracker's state for each of those users, and the same subject authorization
// every other roster applies — a subscription can outlive the membership that
// created it, so holding one is evidence and never permission.
//
// Complete only when this instance is the whole cluster *and* every subject in
// it could be checked. With a bus there are connections it cannot see; with a
// failed authorization there are people it cannot speak for. Either way the
// client is told not to read anything into who is missing.
func (h *Hub) localRoster(target targetKey, key string) (users []PresencePayload, complete bool) {
	if h.presence == nil {
		return nil, false
	}
	users = make([]PresencePayload, 0, 8)
	for _, userID := range h.subscriberUserIDs(key) {
		status, at := h.presence.StatusAt(target.workspaceID, userID)
		if status == PresenceOffline {
			continue
		}
		users = append(users, PresencePayload{
			UserID: userID, State: string(status), UpdatedAt: formatPresenceTime(at),
		})
	}
	users, authorized := h.authorizedRoster(target.workspaceID, target, users)
	return users, authorized && !h.distributed
}

// authorizedRoster removes anybody the domain no longer places in this target,
// and reports whether the answer is complete (SR-444-01, SR-444-02).
//
// The directory is presence storage, not a roster: it answers what was
// asserted, never who belongs. Handing its contents to a client unfiltered is
// how a member removed from a private channel could keep appearing there for as
// long as they stayed idle, since nothing but a transition would have revisited
// them.
//
// The bound comes *before* the authorization and not after it. A conversation
// with a thousand people present would otherwise cost a thousand authorization
// reads to produce a payload that was never going to carry more than
// presenceSnapshotMaxUsers of them — work spent on rows that are discarded a
// moment later, repeated on every snapshot and every reconciliation sweep.
// Cutting first makes the cost of a roster proportional to what is actually
// sent, and truncating is already an incomplete answer, which the client is
// required to read as "conclude nothing about who is missing".
//
// The cut is deterministic (aggregateRoster sorts by user id) so two sweeps over
// an unchanged conversation choose the same people and stay silent, and it is
// applied after the per-user aggregation, so somebody connected through two
// instances occupies one place in the limit rather than two.
func (h *Hub) authorizedRoster(
	workspaceID string, target targetKey, candidates []PresencePayload,
) (users []PresencePayload, complete bool) {
	if len(candidates) == 0 {
		return candidates, true
	}
	complete = true
	if len(candidates) > presenceSnapshotMaxUsers {
		candidates = candidates[:presenceSnapshotMaxUsers]
		complete = false
		h.logger.WarnContext(context.Background(), "ws: presence roster truncated",
			"operation", "roster", "target_type", string(target.targetType),
			"limit", presenceSnapshotMaxUsers,
		)
	}

	allowed, decided := h.authorizeSubjects(workspaceID, target, candidates)
	users = make([]PresencePayload, 0, len(candidates))
	for _, candidate := range candidates {
		if _, ok := allowed[candidate.UserID]; ok {
			users = append(users, candidate)
		}
	}
	return users, complete && decided
}

// authorizeSubjects narrows a bounded candidate list to those the domain still
// places in the target, in one read when the authorizer can answer for a list
// and one read per subject when it cannot.
//
// decided reports whether the question could be answered at all. A failure is
// not permission and it is not a denial either: nobody is admitted, and the
// caller must not present the result as a complete roster.
func (h *Hub) authorizeSubjects(
	workspaceID string, target targetKey, candidates []PresencePayload,
) (allowed map[string]struct{}, decided bool) {
	userIDs := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		userIDs = append(userIDs, candidate.UserID)
	}

	if batch, ok := h.authorizer.(SubjectAuthorizer); ok {
		ctx, cancel := context.WithTimeout(context.Background(), broadcastAuthTimeout)
		permitted, err := batch.AuthorizeSubjects(ctx, workspaceID, target.targetType, target.targetID, userIDs)
		cancel()
		if err == nil {
			allowed = make(map[string]struct{}, len(permitted))
			for _, userID := range permitted {
				allowed[userID] = struct{}{}
			}
			return allowed, true
		}
		if !errors.Is(err, ErrSubjectBatchUnsupported) {
			h.logger.WarnContext(context.Background(), "ws: batch subject authorization failed",
				"operation", "roster", "target_type", string(target.targetType),
			)
			return nil, false
		}
		// Not supported by this authorizer; ask one at a time instead.
	}

	allowed = make(map[string]struct{}, len(userIDs))
	decided = true
	for _, userID := range userIDs {
		permitted, answered := h.subjectVisibility(workspaceID, userID, target)
		if permitted {
			allowed[userID] = struct{}{}
			continue
		}
		// Denied is a complete answer about this person; an error is not.
		if !answered {
			decided = false
		}
	}
	return allowed, decided
}

// subscribedTargetKeys returns the targets this user currently holds a
// subscription to, across every one of their live connections here.
//
// This is *active* state and not a history of where they have been. It shrinks
// when a connection closes or unsubscribes and disappears entirely when they go,
// so it cannot accumulate, and it cannot keep naming a conversation they walked
// away from. What it is not is an authorization: holding a subscription is
// evidence, never permission, which is why publishPresence re-derives the
// subject's access to each of these targets before addressing anything to it.
func (h *Hub) subscribedTargetKeys(workspaceID, userID string) []string {
	h.mu.RLock()
	defer h.mu.RUnlock()

	keys := make([]string, 0, 8)
	seen := make(map[string]struct{}, 8)
	for _, c := range h.clients {
		if c.workspaceID != workspaceID || c.userID != userID {
			continue
		}
		for key := range h.clientSubs[c.id] {
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			keys = append(keys, key)
		}
	}
	return keys
}

// clientSubscriptionKeys returns one client's subscriptions.
func (h *Hub) clientSubscriptionKeys(c *Client) []string {
	h.mu.RLock()
	defer h.mu.RUnlock()

	subs := h.clientSubs[c.id]
	keys := make([]string, 0, len(subs))
	for key := range subs {
		keys = append(keys, key)
	}
	return keys
}

// subscriptionGeneration returns the generation of this client's subscription to
// key, or 0 when it holds none.
//
// The generation is assigned only when a subscription is genuinely created (see
// handleSubscribe), so comparing it across a subscribe is the exact test for
// "did a new subscription come into existence", as opposed to "was a subscribe
// message accepted". Generations are never reused, so an unsubscribe followed by
// a subscribe produces a different one and is correctly treated as a new join.
func (h *Hub) subscriptionGeneration(c *Client, key string) uint64 {
	if c == nil {
		return 0
	}
	h.mu.RLock()
	defer h.mu.RUnlock()

	return h.subscriptionGenerations[c.id][key]
}

func snapshotKeys(set map[string]struct{}) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	return keys
}

// subscriberUserIDs returns the distinct users subscribed to a target on this
// instance, sorted so a snapshot is deterministic.
func (h *Hub) subscriberUserIDs(key string) []string {
	h.mu.RLock()
	userIDs := make([]string, 0, 8)
	seen := make(map[string]struct{}, 8)
	for clientID := range h.subs[key] {
		c, registered := h.clients[clientID]
		if !registered {
			continue
		}
		if _, dup := seen[c.userID]; dup {
			continue
		}
		seen[c.userID] = struct{}{}
		userIDs = append(userIDs, c.userID)
	}
	h.mu.RUnlock()

	sort.Strings(userIDs)
	return userIDs
}

// formatPresenceTime renders a presence instant for the wire. The zero time
// becomes an empty string rather than year 1, so a client comparing timestamps
// treats "never stated" as oldest instead of parsing a date nobody meant.
func formatPresenceTime(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return at.UTC().Format(time.RFC3339Nano)
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
	if partitionCount <= 0 || int64(partitionCount) > math.MaxUint32 {
		panic("ws: broadcast partition count must be between 1 and 4294967295")
	}

	safePartitionCount := uint32(partitionCount)
	hasher := fnv.New32a()
	_, _ = hasher.Write([]byte(key))
	return int(hasher.Sum32() % safePartitionCount)
}

// handleRemoteBusEvent is called by the BroadcastBus Subscribe handler.
// It suppresses self-echo, strictly validates and canonicalizes the event,
// encodes to JSON, and posts to remoteBcast for the run goroutine.
// Called from a bus-owned goroutine — must not touch hub state directly.
func (h *Hub) handleRemoteBusEvent(evt Event) {
	// Suppress self-echo: events we published must not be re-delivered locally.
	//
	// The comparison is against the runtime identity, not the configured one, for
	// the same reason the directory field is (see presenceInstanceID): two pods
	// handed the same WS_INSTANCE_ID would each recognise the other's events as
	// their own and drop them, so every subscriber on B would silently miss
	// everything published by A. A process-unique origin makes "did I send this"
	// answerable by construction rather than by deployment discipline.
	if evt.SourceInstanceID == h.presenceInstanceID {
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

	// User-scoped events are routed by recipient, not by subscription, so they
	// must not enter the broadcast queue — that path would look for subscribers
	// of the target, and the recipient is by definition not one.
	//
	// Delivering here also closes the republication loop: this returns without
	// ever calling bus.Publish, so an event received from the bus is never sent
	// back to it.
	if canonical.Type == EventTypeConversationAvailable {
		if !h.remoteRecipientMayAccess(canonical) {
			return
		}
		h.deliverToLocalUserSessions(canonical, data)
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

// remoteRecipientMayAccess decides whether a conversation.available that
// arrived over the bus may be handed to its addressee's local sessions.
//
// Every other remote event is routed by subscription, and a subscription is
// itself an authorization decision this hub made and re-checks at fan-out
// (handleBroadcast). conversation.available has no such anchor: it names its own
// recipient, and that name arrives from the bus. Without this check the whole
// envelope — recipient, workspace, target — would be taken on the producer's
// word, so anything able to publish could aim a metadata signal and a forced
// refetch at any user it named.
//
// So the recipient's access is re-derived here from the authoritative record,
// against the same SubscriptionAuthorizer the broadcast path uses. That is also
// what makes the target kind safe: CanAccess resolves a channel through the
// channels table and a DM through dm_members, and denies any other kind, so a
// TargetType the producer chose cannot be used to have a group's ID checked as
// something else.
//
// Fail-closed on all three failure shapes. A denial is the expected answer for a
// membership that was removed. An error or a timeout is not evidence of access,
// and a bus consumer must not block on the database, so both drop the event —
// costing at most a stale sidebar until the next refetch, which the sidebar API
// re-authorizes anyway.
func (h *Hub) remoteRecipientMayAccess(evt Event) bool {
	ctx, cancel := context.WithTimeout(context.Background(), broadcastAuthTimeout)
	defer cancel()

	allowed, err := h.authorizer.CanAccess(ctx, evt.RecipientUserID, evt.WorkspaceID, evt.TargetType, evt.TargetID)
	if err != nil {
		// Metadata only: no recipient, no target, nothing that would turn the log
		// into a record of who was added to what.
		h.logger.WarnContext(ctx, "ws: remote conversation.available authorization failed; dropping",
			"target_type", string(evt.TargetType),
			"error", err,
		)
		return false
	}
	if !allowed {
		h.logger.DebugContext(ctx, "ws: remote conversation.available denied; dropping",
			"target_type", string(evt.TargetType),
		)
		return false
	}
	return true
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
	if evt.Type == EventTypeMembersAdded {
		evt, ok = canonicalizeMembersEvent(evt)
		if !ok {
			return Event{}, false
		}
	}
	if evt.Type == EventTypeAttachmentStatus {
		evt, ok = canonicalizeAttachmentEvent(evt)
		if !ok {
			return Event{}, false
		}
	}
	if evt.Type == EventTypePresenceUpdated {
		evt, ok = canonicalizePresenceEvent(evt)
		if !ok {
			return Event{}, false
		}
	}
	if evt.Type == EventTypeMessageLinkSafetyChanged {
		evt, ok = canonicalizeLinkSafetyEvent(evt)
		if !ok {
			return Event{}, false
		}
	}
	if isCallEventType(evt.Type) {
		evt, ok = canonicalizeCallEvent(evt)
		if !ok {
			return Event{}, false
		}
	}
	if evt.Type == EventTypeTypingUpdated {
		evt, ok = canonicalizeTypingEvent(evt)
		if !ok {
			return Event{}, false
		}
	}

	// Remote bus payloads may contain body_text or legacy sender_email. Strip
	// them so remote nodes route by IDs only; clients fetch by ID if needed.
	evt.Payload = nil
	evt.MessageUpdate = nil
	// A presence/typing block belongs to exactly one event type each. Anything
	// else carrying one is relaying a state nobody asked it about, so it is
	// dropped here rather than forwarded alongside an unrelated event.
	if evt.Type != EventTypePresenceUpdated {
		evt.Presence = nil
	}
	// Same rule as the presence block: a link-safety correction belongs to exactly
	// one event type, and anything else carrying one is relaying a security state
	// nobody asked it about.
	if evt.Type != EventTypeMessageLinkSafetyChanged {
		evt.LinkSafety = nil
	}
	if evt.Type != EventTypeTypingUpdated {
		evt.Typing = nil
	}

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
	case EventTypeMessageBlocked, EventTypeMessageLinkSafetyChanged,
		EventTypeMessageCreated, EventTypeMessageUpdated, EventTypeReactionUpdated, EventTypePinUpdated,
		EventTypeMembersAdded, EventTypeConversationAvailable, EventTypeAttachmentStatus,
		EventTypePresenceUpdated, EventTypeTypingUpdated,
		EventTypeCallRinging, EventTypeCallAccepted, EventTypeCallDeclined,
		EventTypeCallCancelled, EventTypeCallTimedOut, EventTypeCallEnded:
		// OK
	default:
		return Event{}, false
	}

	// Known target type required.
	switch evt.TargetType {
	case TargetTypeChannel, TargetTypeDM, TargetTypeUser:
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

	// conversation.available is the one event that names a user rather than a
	// message: it is routed to a recipient, not to a message's subscribers.
	// Its recipient is required and must be a UUID; a message ID is not.
	if evt.Type == EventTypeConversationAvailable {
		rid, err := uuid.Parse(evt.RecipientUserID)
		if err != nil {
			return Event{}, false
		}
		evt.RecipientUserID = rid.String()
		// Nothing else may ride along: strip every payload a sender might have
		// attached, so a remote event can carry no identities or content.
		evt.MessageID = ""
		evt.Pin = nil
		evt.Reaction = nil
		evt.Members = nil
		evt.Presence = nil
		return evt, true
	}

	// No other event type may carry a recipient — that field is what makes
	// delivery bypass subscriptions, so it must not appear where it is not
	// expected.
	if evt.RecipientUserID != "" {
		return Event{}, false
	}

	// members.added is target-scoped like the message events, but it is about the
	// target itself rather than about anything in it, so it has no message to
	// name. A message_id on one is a shape this protocol does not produce.
	//
	// attachment.status is the same shape for the same reason: it is about a file
	// in the target, not about a message, and an attachment can outlive the
	// message that carried it.
	//
	// presence.updated and typing.updated are both about a person in the target
	// rather than about anything in it, so neither names a message either.
	if evt.Type == EventTypeMembersAdded || evt.Type == EventTypeAttachmentStatus ||
		evt.Type == EventTypePresenceUpdated || evt.Type == EventTypeTypingUpdated {
		if evt.MessageID != "" {
			return Event{}, false
		}
		return evt, true
	}

	// Call events (RF-23) route to a user through TargetTypeUser/TargetID rather
	// than through RecipientUserID, and name no message. canonicalizeCallEvent
	// has already asserted both, plus the whole call payload.
	if isCallEventType(evt.Type) {
		return evt, true
	}

	// Everything that remains is message-scoped.
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

// canonicalizeMembersEvent validates a remote members.added event.
//
// The payload is retained rather than stripped, like pin.updated's and unlike
// message.created's, because there is nothing in it to distrust with a body:
// MembersAddedPayload is two counts and the actor, and it names none of the
// people who were added (see its doc comment). Keeping it is what lets a remote
// subscriber correct its member counter without waiting for the refetch, and
// what makes a client's own write recognisable as an echo on any pod.
//
// The counts are still checked for a shape the publisher could actually have
// produced: an add that changed nothing is never published, and the total after
// a commit necessarily includes the people it just added. Anything else is a
// forged or corrupted envelope, and the whole event is dropped — a remote node
// must not render a count it was handed.
//
// Pin and Reaction are cleared: those belong to other event types, and an
// envelope that carries both is not one this protocol emits.
func canonicalizeMembersEvent(evt Event) (Event, bool) {
	if evt.Members == nil {
		return Event{}, false
	}
	if evt.Members.AddedCount <= 0 || evt.Members.MemberCount < evt.Members.AddedCount {
		return Event{}, false
	}
	actorID, err := uuid.Parse(evt.Members.ActorUserID)
	if err != nil {
		return Event{}, false
	}
	evt.Members.ActorUserID = actorID.String()
	evt.Pin = nil
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

// attachmentStatusValues is the closed set of states this hub will relay
// (RF-22). It mirrors file-service's downloadable-status machine, and it is
// restated here rather than trusted because the producer is another service:
// the three values are the whole external contract, and a fourth arriving over
// the bus is a shape neither this build nor its clients implement.
//
// pending_upload, failed and deleted are absent: they are internal upload
// states with no meaning to a subscriber, and an attachment in one of them is
// not something a client is showing.
var attachmentStatusValues = map[string]struct{}{
	"pending_scan": {},
	"clean":        {},
	"rejected":     {},
}

// attachmentUpdatedAtMaxLen bounds the timestamp string. It is a display and
// ordering hint the client parses, so it is length-checked rather than parsed
// here — a value this hub cannot interpret is still one the client can ignore,
// but an unbounded one is memory a producer chose.
const attachmentUpdatedAtMaxLen = 64

// canonicalizeAttachmentEvent validates a remote attachment.status event
// (RF-22).
//
// This is the only event whose producer is a different service, so the
// validation is where the trust boundary actually is. Three things are asserted
// and nothing is inferred:
//
//   - the target is a channel or a DM. TargetTypeUser is refused, because this
//     event routes by subscription and a user target would put it on the
//     recipient-addressed path it has no business reaching;
//   - the attachment id is a UUID, canonicalized to lowercase, so the id a
//     client reconciles against matches the one the API returns;
//   - the status is one of the three the contract defines. An unknown value is
//     dropped rather than forwarded, so a producer cannot invent a state the
//     client would have to guess about.
//
// Every other payload is stripped. A producer that attached a pin, a reaction, a
// members block or a message DTO gets none of them relayed: this event carries
// an attachment state and nothing else, whatever was sent.
func canonicalizeAttachmentEvent(evt Event) (Event, bool) {
	if evt.Attachment == nil {
		return Event{}, false
	}
	if evt.TargetType != TargetTypeChannel && evt.TargetType != TargetTypeDM {
		return Event{}, false
	}
	attachmentID, err := uuid.Parse(evt.Attachment.AttachmentID)
	if err != nil {
		return Event{}, false
	}
	if _, known := attachmentStatusValues[evt.Attachment.Status]; !known {
		return Event{}, false
	}
	if len(evt.Attachment.UpdatedAt) > attachmentUpdatedAtMaxLen {
		return Event{}, false
	}
	evt.Attachment.AttachmentID = attachmentID.String()
	// The message payloads are cleared here as well as by the caller, and the
	// duplication is the point: this function is what the contract for
	// attachment.status is read from, so "every other payload is stripped" has
	// to be true of the function itself rather than of the order it happens to
	// be called in. A message DTO riding on an attachment verdict is the one
	// contaminated shape that would otherwise relay a body to every subscriber
	// of the target.
	evt.Payload = nil
	evt.MessageUpdate = nil
	evt.Pin = nil
	evt.Reaction = nil
	evt.Members = nil
	evt.Call = nil
	return evt, true
}

// presenceStateValues is the closed set of states this hub will relay (RF-58).
// It mirrors the three PresenceStatus constants and is restated as wire values
// because a remote producer's string is not a PresenceStatus until it has been
// checked against them.
var presenceStateValues = map[string]struct{}{
	string(PresenceOnline):  {},
	string(PresenceAway):    {},
	string(PresenceOffline): {},
}

// presenceUpdatedAtMaxLen bounds the timestamp string, like the attachment
// event's: the client parses it for ordering, so an unparseable value costs an
// ignored update, but an unbounded one is memory a producer chose.
const presenceUpdatedAtMaxLen = 64

// canonicalizePresenceEvent validates a remote presence.updated event (RF-58).
//
// Three assertions, and nothing is inferred:
//
//   - the target is a channel or a DM. TargetTypeUser is refused: this event
//     routes by subscription, and a user target would put it on the
//     recipient-addressed path it has no business reaching;
//   - the subject is a UUID, canonicalized to lowercase, so the id a client
//     reconciles against matches the one the API returns for that person;
//   - the state is one of the three the contract defines. An unknown value is
//     dropped rather than forwarded, so a producer cannot invent a fourth state
//     that every client would then have to guess about.
//
// Every other payload is stripped. A remote instance's presence event carries a
// presence and nothing else, whatever was published.
func canonicalizePresenceEvent(evt Event) (Event, bool) {
	if evt.Presence == nil {
		return Event{}, false
	}
	if evt.TargetType != TargetTypeChannel && evt.TargetType != TargetTypeDM {
		return Event{}, false
	}
	userID, err := uuid.Parse(evt.Presence.UserID)
	if err != nil {
		return Event{}, false
	}
	if _, known := presenceStateValues[evt.Presence.State]; !known {
		return Event{}, false
	}
	if len(evt.Presence.UpdatedAt) > presenceUpdatedAtMaxLen {
		return Event{}, false
	}
	evt.Presence.UserID = userID.String()
	evt.Payload = nil
	evt.MessageUpdate = nil
	evt.Pin = nil
	evt.Reaction = nil
	evt.Members = nil
	evt.Attachment = nil
	evt.Call = nil
	return evt, true
}

// dropClient removes a client and all its subscriptions from hub state,
// then closes the underlying connection.
// It does not hold h.mu while closing the underlying connection.
func (h *Hub) dropClient(c *Client) {
	if c == nil {
		return
	}

	// The rooms this connection was in, captured before removeClient wipes them.
	// They are where the offline is announced *promptly*; any other room the user
	// was visible in converges through reconciliation, which recomputes an active
	// target's roster from the shared directory rather than trusting a connection
	// to have remembered it. That division is deliberate: a connection knows what
	// it was subscribed to and nothing more, and the per-user history that used to
	// cover the difference is what made presence outlive both the subscription and
	// the membership that justified it.
	keys := h.clientSubscriptionKeys(c)
	typingKeys := h.clientTypingKeys(c)

	removed := h.removeClient(c)
	if removed == nil {
		return
	}
	removed.close()

	// Broadcast typing.stop for anything this connection was still asserting,
	// rather than waiting out typingTTL — the common case of an orderly
	// disconnect should clear peers' indicators immediately.
	h.stopAllTyping(context.Background(), removed, typingKeys)

	if h.presence == nil {
		return
	}
	// Called after removeClient releases h.mu — no lock held. The tracker's own
	// accounting decides the user's aggregate state here; leaving a dead
	// connection in it would keep them online forever.
	change := h.presence.Disconnect(removed.workspaceID, removed.userID, removed.id)
	if !change.Changed {
		// The user is still online — another connection holds them up — but this
		// one may have been the only local cover for some of their conversations.
		// Presence and coverage are different dimensions, and a disconnect that
		// moves neither the aggregate nor this flag still moves coverage: the
		// rooms only this connection was reading must stop showing them.
		h.reconcileAssertions(removed.workspaceID, removed.userID)
		h.reconcileLostCoverage(removed.workspaceID, removed.userID, keys)
		return
	}
	// The rooms this instance actually put them in, plus the ones the departing
	// connection was reading. The first is what this instance is responsible for
	// withdrawing; the second covers a room it never got as far as asserting.
	keys = append(keys, h.assertedTargetsList(removed.workspaceID, removed.userID)...)
	h.enqueuePresenceChange(presenceChange{
		workspaceID: removed.workspaceID, userID: removed.userID,
		status: change.Status, at: change.At, keys: keys,
	})
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
	// Collected under the lock, acted on after it: a target nobody watches here
	// any more must not keep the memory of what reconciliation last sent it.
	var orphaned []string
	defer func() {
		for _, key := range orphaned {
			h.forgetReconciledTarget(key)
		}
	}()

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
				orphaned = append(orphaned, key)
			}
		}
	}
	delete(h.clientSubs, c.id)
	delete(h.subscriptionGenerations, c.id)
	delete(h.typingActive, c.id)
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
	h.typingActive = make(map[string]map[string]struct{})
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

	if req.event.TargetType == TargetTypeUser {
		h.handleUserBroadcast(req)
		return
	}
	key := broadcastTargetKey(req.event)

	if !h.presenceSubjectStillVisible(req.event) {
		return
	}

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
		// TargetTypeUser events never reach this loop: handleBroadcast routes them
		// to handleUserBroadcast and returns before this point.
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

// presenceSubjectStillVisible re-derives, at the moment of delivery, whether the
// person a presence.updated is about may still be seen in its target (SR-444-03).
//
// publishPresence already asks this — but it asks it when the event is *queued*,
// and the queue is drained by a worker. Between the two, a membership can be
// revoked: the event was authorized against a world that no longer exists, and
// the recipient checks below say nothing about the subject. That window is small
// and entirely real, and it is the only place left where presence about somebody
// could reach a room they have been removed from.
//
// Once per event, not once per recipient. The subject's access does not vary by
// who is reading, so asking N times would be N times the cost for one answer —
// and the per-recipient check that follows is a different question that still
// has to be asked N times.
//
// Fail closed: a denial and an authorization error both drop the event. An error
// is not evidence of access, and nothing is lost by staying quiet — reconciliation
// rebuilds the target's roster from an authorized read.
//
// Two events are deliberately exempt:
//
//   - anything that is not presence.updated. Messages, reactions, pins, calls and
//     attachments are about the target, not about a person in it, and their
//     authorization is the recipient's alone.
//   - a presence.snapshot, which travels as a presence.updated envelope with no
//     Presence block. Its roster was already filtered subject-by-subject by
//     authorizedRoster, so there is no single subject here to re-check.
//   - an offline state. Offline is a *withdrawal*: it is what tells a room to stop
//     showing somebody, including somebody who has just lost access (see
//     forgetSubject). Dropping it would freeze the last state an observer was
//     told — leaving a removed member displayed as online, which is the disclosure
//     this check exists to prevent, not a defence against it.
func (h *Hub) presenceSubjectStillVisible(evt Event) bool {
	if evt.Type != EventTypePresenceUpdated || evt.Presence == nil {
		return true
	}
	if evt.Presence.State == string(PresenceOffline) {
		return true
	}
	target := targetKey{
		workspaceID: evt.WorkspaceID, targetType: evt.TargetType, targetID: evt.TargetID,
	}
	if h.subjectMayAppear(evt.WorkspaceID, evt.Presence.UserID, target) {
		return true
	}
	h.logger.DebugContext(context.Background(), "ws: presence dropped at delivery; subject no longer visible",
		"target_type", string(evt.TargetType),
	)
	return false
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
func (h *Hub) handleClientMessage(ctx context.Context, c *Client, msg ClientMessage) error {
	// Record activity — any inbound frame is evidence the client is present.
	// RecordActivity uses PresenceTracker.mu; Hub.mu is not held.
	//
	// The identity credited is the connection's, asserted at the handshake. A
	// client cannot name the user whose presence its activity refreshes, because
	// no field of ClientMessage participates in this call at all.
	//
	// Only the away → online transition is published. Activity on an already
	// online user is every single frame, and broadcasting that would turn a
	// presence indicator into a traffic mirror.
	if h.presence != nil {
		if change := h.presence.RecordActivity(c.workspaceID, c.userID, c.id); change.Changed {
			h.enqueuePresenceChange(presenceChange{
				workspaceID: c.workspaceID, userID: c.userID,
				status: change.Status, at: change.At, derive: true,
			})
		}
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
	case ClientMessageTypeCallStart, ClientMessageTypeCallAccept, ClientMessageTypeCallDecline,
		ClientMessageTypeCallCancel, ClientMessageTypeCallEnd, ClientMessageTypeCallSync,
		ClientMessageTypeCallPresence:
		return h.handleCallMessage(ctx, c, msg)
	case ClientMessageTypeTypingStart:
		return h.handleTypingStart(ctx, c, msg)
	case ClientMessageTypeTypingStop:
		return h.handleTypingStop(ctx, c, msg)
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
	orphaned := false
	reconcile := false
	stoppedTyping := false
	// All four run after h.mu is released: forget what reconciliation last sent a
	// target nobody watches here any more; withdraw the directory assertion if
	// this was the last local cover for it; if the subject has stopped covering
	// a target other people here are still watching, rebuild that target's
	// roster so they stop being shown someone who left it; and if the client
	// was mid-typing on this target, broadcast the stop now rather than
	// leaving peers to wait out typingTTL.
	defer func() {
		if orphaned {
			h.forgetReconciledTarget(key)
		}
		if reconcile {
			h.reconcileAssertions(client.workspaceID, client.userID)
			h.reconcileLostCoverage(client.workspaceID, client.userID, []string{key})
		}
		if stoppedTyping {
			h.finishTypingStop(context.Background(), client, key)
		}
	}()

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
			orphaned = true
		}
	}
	if clientSubs, ok := h.clientSubs[client.id]; ok {
		delete(clientSubs, key)
	}
	if generations, ok := h.subscriptionGenerations[client.id]; ok {
		delete(generations, key)
	}
	if targets, ok := h.typingActive[client.id]; ok {
		if _, typing := targets[key]; typing {
			delete(targets, key)
			if len(targets) == 0 {
				delete(h.typingActive, client.id)
			}
			stoppedTyping = true
		}
	}
	reconcile = true
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
