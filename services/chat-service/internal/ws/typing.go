package ws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	valkey "github.com/valkey-io/valkey-go"
)

// typingTTL bounds how long a single typing.start assertion is honoured by the
// Valkey backstop before it self-expires.
//
// It is a package constant, not env config, for the same reason
// directoryEntryTTL and instanceLivenessTTL are: this is a protocol-level
// lease, not an operational tuning knob. It is deliberately short — seconds,
// not the hours presence uses — because typing is the most perishable state
// this hub relays: nobody reads a stale "is typing" the way an operator reads
// a stale "is online", and the client already renews well inside this window
// while the user keeps typing (see useTypingIndicator on the frontend).
//
// This TTL is not the delivery mechanism — typing.updated is a live broadcast,
// exactly like reaction.updated and pin.updated. It exists purely so a typing
// assertion cannot outlive a client that vanished without ever sending
// typing.stop (tab crash, lost network, killed process): the Valkey key
// expires on its own, and dropClient/revokeSubscription below cover the much
// more common case of an orderly disconnect promptly instead of waiting it out.
const typingTTL = 6 * time.Second

// typingStoreTimeout bounds every call to the typing store, mirroring
// directoryWriteTimeout/directoryReadTimeout: this is a cache of ephemeral
// state, so waiting on it is always worse than proceeding without it.
const typingStoreTimeout = 2 * time.Second

const (
	defaultTypingRateLimitMaxActions    = 20
	defaultTypingRateLimitWindowSeconds = 30
)

var (
	// ErrTypingRateLimited is returned when a client exceeds the typing.start
	// budget. typing.stop is never rate-limited (see handleTypingStop).
	ErrTypingRateLimited = errors.New("typing rate limited")
	// ErrTypingNotSubscribed is returned when a client asserts typing on a
	// target it does not currently hold an active subscription to.
	//
	// This is deliberately not a fresh SubscriptionAuthorizer.CanAccess call:
	// the invariant "subscribed implies authorized" already holds continuously
	// for the lifetime of a subscription (Subscribe only succeeds after
	// CanAccess, and RevokeSubscription removes the client from clientSubs the
	// moment access is lost), so checking membership in the Hub's own
	// subscription state is exactly as strict, without a DB round-trip on
	// every keystroke-driven renewal.
	ErrTypingNotSubscribed = errors.New("typing: target not subscribed")
	// ErrTypingFeatureDisabled is returned when no TypingLimiter is configured.
	// Fail-closed, like reaction and call: SECURITY.md requires rate limiting
	// on every WS action, so typing without a limiter wired is off rather than
	// unlimited.
	ErrTypingFeatureDisabled = errors.New("typing feature disabled")
)

// TypingStore is the Valkey-backed ghost-state backstop described by typingTTL.
//
// id is an opaque identifier this package constructs (see typingStoreID); the
// store's only job is to remember it for ttl and forget it early on Clear.
type TypingStore interface {
	Touch(ctx context.Context, id string, ttl time.Duration) error
	Clear(ctx context.Context, id string) error
}

// TypingLimiter applies the shared action rate limiter (see
// ValkeyReactionLimiter.AllowActionWithLimit) to typing.start.
type TypingLimiter interface {
	AllowActionWithLimit(ctx context.Context, userID, action string, maxActions, windowSeconds int) (bool, error)
}

// WithTypingStore attaches the Valkey typing-state backstop. Optional: a nil
// store (the default) skips Valkey entirely and relies on typing.stop plus
// the disconnect/revocation cleanup below — delivery still works, only the
// self-expiry backstop is absent.
func WithTypingStore(store TypingStore) HubOption {
	return func(h *Hub) { h.typingStore = store }
}

// WithTypingLimiter attaches the rate limiter for typing.start and its budget.
// Without it, typing.start is refused with ErrTypingFeatureDisabled.
func WithTypingLimiter(limiter TypingLimiter, maxActions, windowSeconds int) HubOption {
	return func(h *Hub) {
		h.typingLimiter = limiter
		h.typingRateLimitMaxActions = maxActions
		h.typingRateLimitWindowSeconds = windowSeconds
	}
}

// typingTarget is a validated typing.start/typing.stop destination.
type typingTarget struct {
	targetType TargetType
	targetID   string
}

// validateTypingMessage mirrors normalizeSubscriptionTarget (handler.go): the
// target must be a channel or DM, target_id must be a canonical UUID, and no
// field belonging to another message type may be present.
func validateTypingMessage(msg ClientMessage) (typingTarget, error) {
	if msg.Type != ClientMessageTypeTypingStart && msg.Type != ClientMessageTypeTypingStop {
		return typingTarget{}, fmt.Errorf("ws: typing: invalid message type")
	}
	if msg.TargetType != TargetTypeChannel && msg.TargetType != TargetTypeDM {
		return typingTarget{}, fmt.Errorf("ws: typing: unsupported target_type")
	}
	if msg.MessageID != "" || msg.Emoji != "" || msg.RequestID != "" || msg.CallID != "" ||
		msg.TargetUserID != "" || msg.CallType != "" {
		return typingTarget{}, fmt.Errorf("ws: typing: unexpected fields")
	}
	rawTargetID := strings.TrimSpace(msg.TargetID)
	if rawTargetID == "" {
		return typingTarget{}, fmt.Errorf("ws: typing: target_id required")
	}
	parsedID, err := uuid.Parse(rawTargetID)
	if err != nil {
		return typingTarget{}, fmt.Errorf("ws: typing: invalid target_id format")
	}
	return typingTarget{targetType: msg.TargetType, targetID: parsedID.String()}, nil
}

// clientHasSubscription reports whether c currently holds an active
// subscription to key. See ErrTypingNotSubscribed for why this stands in for
// a fresh authorization check.
func (h *Hub) clientHasSubscription(c *Client, key string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.clientSubs[c.id][key]
	return ok
}

// clientTypingKeys returns the target keys c is currently marked typing on.
func (h *Hub) clientTypingKeys(c *Client) []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	targets := h.typingActive[c.id]
	keys := make([]string, 0, len(targets))
	for key := range targets {
		keys = append(keys, key)
	}
	return keys
}

// touchTypingState records that c is typing on key, for the disconnect and
// revocation cleanup paths below. Purely in-memory; the Valkey touch is a
// separate, non-blocking-on-mu call.
func (h *Hub) touchTypingState(c *Client, key string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.typingActive[c.id] == nil {
		h.typingActive[c.id] = make(map[string]struct{})
	}
	h.typingActive[c.id][key] = struct{}{}
}

// clearTypingState removes the in-memory record unconditionally and reports
// whether c had been marked typing on key.
func (h *Hub) clearTypingState(c *Client, key string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	targets := h.typingActive[c.id]
	if targets == nil {
		return false
	}
	_, was := targets[key]
	delete(targets, key)
	if len(targets) == 0 {
		delete(h.typingActive, c.id)
	}
	return was
}

// typingStoreID builds the opaque identifier passed to TypingStore, scoped by
// target key and user so two users typing in the same conversation, or one
// user typing in two conversations, never share a lease.
func typingStoreID(key, userID string) string {
	return key + "\x00" + userID
}

func (h *Hub) touchTypingStore(ctx context.Context, key, userID string) {
	if h.typingStore == nil {
		return
	}
	storeCtx, cancel := context.WithTimeout(ctx, typingStoreTimeout)
	defer cancel()
	if err := h.typingStore.Touch(storeCtx, typingStoreID(key, userID), typingTTL); err != nil {
		h.logger.WarnContext(ctx, "ws: typing store touch failed", "error", err)
	}
}

func (h *Hub) clearTypingStore(ctx context.Context, key, userID string) {
	if h.typingStore == nil {
		return
	}
	storeCtx, cancel := context.WithTimeout(ctx, typingStoreTimeout)
	defer cancel()
	if err := h.typingStore.Clear(storeCtx, typingStoreID(key, userID)); err != nil {
		h.logger.WarnContext(ctx, "ws: typing store clear failed", "error", err)
	}
}

// publishTypingUpdated broadcasts one user's typing state, following exactly
// the PublishReactionUpdated/PublishPinUpdated shape: local delivery is
// attempted first via h.bcast, distributed delivery via h.bus.Publish is
// best-effort. No snapshot-on-subscribe exists for typing — it is inherently
// "what's happening right now", not state a newly-opened conversation needs
// to backfill.
func (h *Hub) publishTypingUpdated(ctx context.Context, workspaceID string, targetType TargetType, targetID, userID, userDisplayName string, isTyping bool) {
	evt := Event{
		SchemaVersion: CurrentEventSchemaVersion, Type: EventTypeTypingUpdated,
		WorkspaceID: workspaceID, TargetType: targetType, TargetID: targetID,
		Typing: &TypingEventPayload{
			UserID: userID, UserDisplayName: userDisplayName, IsTyping: isTyping,
			UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		},
		EventID:          uuid.New().String(),
		SourceInstanceID: h.presenceInstanceID, CreatedAt: time.Now().UTC(),
	}
	data, err := json.Marshal(evt)
	if err != nil {
		h.logger.ErrorContext(ctx, "ws: marshal typing.updated event", "error", err)
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
		h.logger.WarnContext(ctx, "ws: typing bus publish failed", "target_type", string(targetType), "error", err)
	}
}

// handleTypingStart processes a client-initiated typing.start.
func (h *Hub) handleTypingStart(ctx context.Context, c *Client, msg ClientMessage) error {
	target, err := validateTypingMessage(msg)
	if err != nil {
		return err
	}
	key := targetKey{workspaceID: c.workspaceID, targetType: target.targetType, targetID: target.targetID}.String()
	if !h.clientHasSubscription(c, key) {
		return ErrTypingNotSubscribed
	}
	if h.typingLimiter == nil {
		return ErrTypingFeatureDisabled
	}
	limit, window := h.typingRateLimitMaxActions, h.typingRateLimitWindowSeconds
	if limit <= 0 {
		limit = defaultTypingRateLimitMaxActions
	}
	if window <= 0 {
		window = defaultTypingRateLimitWindowSeconds
	}
	allowed, err := h.typingLimiter.AllowActionWithLimit(ctx, c.userID, "typing_start", limit, window)
	if err != nil {
		return fmt.Errorf("ws: typing rate limit: %w", err)
	}
	if !allowed {
		return ErrTypingRateLimited
	}
	h.touchTypingState(c, key)
	h.touchTypingStore(ctx, key, c.userID)
	h.publishTypingUpdated(ctx, c.workspaceID, target.targetType, target.targetID, c.userID, c.displayName, true)
	return nil
}

// handleTypingStop processes a client-initiated typing.stop.
//
// Unlike typing.start, this is never rate-limited: dropping a stop is worse
// than allowing extra ones, since it would leave peers thinking someone is
// still typing until their local defensive expiry kicks in. It is also
// idempotent — stopping typing that was never started (a redundant cleanup
// call, a race with an earlier stop) succeeds and broadcasts, which costs
// nothing a subscriber would notice.
func (h *Hub) handleTypingStop(ctx context.Context, c *Client, msg ClientMessage) error {
	target, err := validateTypingMessage(msg)
	if err != nil {
		return err
	}
	key := targetKey{workspaceID: c.workspaceID, targetType: target.targetType, targetID: target.targetID}.String()
	if !h.clientHasSubscription(c, key) {
		return ErrTypingNotSubscribed
	}
	h.clearTypingState(c, key)
	h.clearTypingStore(ctx, key, c.userID)
	h.publishTypingUpdated(ctx, c.workspaceID, target.targetType, target.targetID, c.userID, c.displayName, false)
	return nil
}

// stopAllTyping ends every typing session c was holding, for the disconnect
// path (dropClient): a stop is broadcast for each rather than waiting out
// typingTTL, so peers do not have to wait out the backstop for the common
// case of an orderly disconnect.
func (h *Hub) stopAllTyping(ctx context.Context, c *Client, keys []string) {
	for _, key := range keys {
		parsed, ok := parseTargetKey(key)
		if !ok {
			continue
		}
		h.clearTypingStore(ctx, key, c.userID)
		h.publishTypingUpdated(ctx, parsed.workspaceID, parsed.targetType, parsed.targetID, c.userID, c.displayName, false)
	}
}

// finishTypingStop is stopAllTyping for exactly one key, used by
// revokeSubscription once it has already removed the in-memory record under
// h.mu — called after the lock is released, so it may perform Valkey I/O and
// enqueue a broadcast.
func (h *Hub) finishTypingStop(ctx context.Context, c *Client, key string) {
	parsed, ok := parseTargetKey(key)
	if !ok {
		return
	}
	h.clearTypingStore(ctx, key, c.userID)
	h.publishTypingUpdated(ctx, parsed.workspaceID, parsed.targetType, parsed.targetID, c.userID, c.displayName, false)
}

// typingUpdatedAtMaxLen bounds the timestamp string, mirroring
// presenceUpdatedAtMaxLen: the client parses it only for ordering, so an
// unbounded value is memory a producer chose, not a client's problem to hold.
const typingUpdatedAtMaxLen = 64

// typingUserDisplayNameMaxLen bounds the display name string. Unlike
// UpdatedAt, an oversized value here is not evidence the whole event is
// malformed — it is cosmetic — so it is truncated (on a rune boundary, never
// a byte boundary, since display names carry multi-byte characters) rather
// than causing the entire typing.updated to be dropped for every subscriber.
const typingUserDisplayNameMaxLen = 128

// canonicalizeTypingEvent validates a remote typing.updated event, mirroring
// canonicalizePresenceEvent: the target must be a channel or DM (never the
// user-routed TargetTypeUser), the subject must be a UUID, canonicalized to
// lowercase, and the timestamp is bounded. Every other payload is stripped —
// a remote instance's typing event carries a typing state and nothing else,
// whatever else the producer happened to set.
func canonicalizeTypingEvent(evt Event) (Event, bool) {
	if evt.Typing == nil {
		return Event{}, false
	}
	if evt.TargetType != TargetTypeChannel && evt.TargetType != TargetTypeDM {
		return Event{}, false
	}
	userID, err := uuid.Parse(evt.Typing.UserID)
	if err != nil {
		return Event{}, false
	}
	if len(evt.Typing.UpdatedAt) > typingUpdatedAtMaxLen {
		return Event{}, false
	}
	if runes := []rune(evt.Typing.UserDisplayName); len(runes) > typingUserDisplayNameMaxLen {
		evt.Typing.UserDisplayName = string(runes[:typingUserDisplayNameMaxLen])
	}
	evt.Typing.UserID = userID.String()
	evt.Payload = nil
	evt.MessageUpdate = nil
	evt.Pin = nil
	evt.Reaction = nil
	evt.Members = nil
	evt.Attachment = nil
	evt.Presence = nil
	evt.Call = nil
	return evt, true
}

// ── Valkey-backed store ─────────────────────────────────────────────────────

const typingKeyPrefix = "nchat:chat:ws:typing:"

// ValkeyTypingStore is the Valkey-backed TypingStore: a short-TTL string key
// per (target, user), set on typing.start/renewal and deleted on typing.stop.
//
// Deliberately simpler than ValkeyPresenceDirectory: typing needs no
// per-instance field disambiguation and no instance-liveness cross-check,
// because a stale key from a crashed instance just expires within typingTTL —
// seconds, not presence's six hours — so nothing is gained by tracking which
// instance wrote it.
type ValkeyTypingStore struct {
	client valkey.Client
}

// NewValkeyTypingStore dials Valkey for the typing backstop. Returns an error
// if valkeyURL is empty; callers that want graceful degradation without
// Valkey configured should use NopTypingStore instead.
func NewValkeyTypingStore(valkeyURL string) (*ValkeyTypingStore, error) {
	if valkeyURL == "" {
		return nil, errors.New("typing store requires VALKEY_URL")
	}
	option, err := valkey.ParseURL(valkeyURL)
	if err != nil {
		return nil, fmt.Errorf("parse typing store valkey URL: %w", err)
	}
	client, err := valkey.NewClient(option)
	if err != nil {
		return nil, fmt.Errorf("create typing store client: %w", err)
	}
	return &ValkeyTypingStore{client: client}, nil
}

func (s *ValkeyTypingStore) Touch(ctx context.Context, id string, ttl time.Duration) error {
	cmd := s.client.B().Set().Key(typingKeyPrefix + id).Value("1").Ex(ttl).Build()
	return s.client.Do(ctx, cmd).Error()
}

func (s *ValkeyTypingStore) Clear(ctx context.Context, id string) error {
	cmd := s.client.B().Del().Key(typingKeyPrefix + id).Build()
	return s.client.Do(ctx, cmd).Error()
}

func (s *ValkeyTypingStore) Close() { s.client.Close() }

// NopTypingStore is the TypingStore used when Valkey is not configured.
// Delivery is unaffected — typing.updated is a live broadcast, not sourced
// from this store — only the self-expiry backstop is absent; typing.stop and
// the disconnect/revocation cleanup paths remain the way state is cleared.
type NopTypingStore struct{}

func (NopTypingStore) Touch(context.Context, string, time.Duration) error { return nil }
func (NopTypingStore) Clear(context.Context, string) error                { return nil }

// isTypingClientMessage reports whether messageType is a typing control
// message, mirroring isCallClientMessage (call_validation.go).
func isTypingClientMessage(messageType ClientMessageType) bool {
	return messageType == ClientMessageTypeTypingStart || messageType == ClientMessageTypeTypingStop
}

// handleTypingClientError classifies expected typing outcomes so they do not
// consume the malformed-message budget or produce noisy client-error logs,
// mirroring handleReactionClientError. Validation failures (bad target,
// unexpected fields) are deliberately not classified here — those fall
// through to the generic invalid-message path, same as a malformed
// reaction.toggle.
func handleTypingClientError(c *Client, err error) bool {
	response := clientErrorResponse{Type: "error", Operation: "typing"}
	switch {
	case errors.Is(err, ErrTypingRateLimited):
		response.Code = "rate_limited"
		response.RetryAfter = defaultTypingRateLimitWindowSeconds
	case errors.Is(err, ErrTypingFeatureDisabled):
		response.Code = "temporarily_unavailable"
	case errors.Is(err, ErrTypingNotSubscribed):
		response.Code = "typing_not_subscribed"
	default:
		return false
	}
	data, marshalErr := json.Marshal(response)
	if marshalErr == nil {
		c.enqueue(data)
	}
	return true
}
