package ws

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/wsutil"
)

// credentialParamNames is the set of query parameter names (lowercased) whose
// presence indicates an attempt to pass credentials in the URL.
var credentialParamNames = map[string]struct{}{
	"token": {}, "access_token": {}, "authorization": {},
	"auth": {}, "jwt": {}, "bearer": {},
}

// WorkspaceResolver resolves the active workspace ID for the deployment.
// The returned ID must always originate server-side; client-provided IDs
// are never accepted.
type WorkspaceResolver interface {
	GetDefaultWorkspaceID(ctx context.Context) (string, error)
}

// UserDisplayNameResolver resolves a user's current display name, looked up
// once per WebSocket connection — never once per typing event. typing.start
// can fire up to the rate-limit cadence per user (20/30s, see typing.go), so a
// per-event DB lookup would multiply load for no benefit; a new connection is
// comparatively rare. A nil resolver, or any error from it, degrades to an
// empty name rather than failing the upgrade (see resolveDisplayName) — this
// is a display nicety, not an authorization input.
type UserDisplayNameResolver interface {
	GetDisplayName(ctx context.Context, userID string) (string, error)
}

// wsSender wraps a *websocket.Conn to satisfy the sender interface.
//
// Close cancels the internal context and calls CloseNow so that any Send or
// Ping call in flight fails fast rather than blocking until the I/O deadline.
// All methods are safe for concurrent use.
type wsSender struct {
	conn   *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc
	once   sync.Once
}

func newWSSender(conn *websocket.Conn) *wsSender {
	ctx, cancel := context.WithCancel(context.Background())
	return &wsSender{conn: conn, ctx: ctx, cancel: cancel}
}

func (s *wsSender) Send(data []byte) error {
	return s.conn.Write(s.ctx, websocket.MessageText, data)
}

func (s *wsSender) Ping(ctx context.Context) error {
	return s.conn.Ping(ctx)
}

func (s *wsSender) Close() {
	s.once.Do(func() {
		s.cancel()
		_ = s.conn.CloseNow()
	})
}

// connectionLimiter tracks active WebSocket connection counts per user.
// All methods are safe for concurrent use.
type connectionLimiter struct {
	mu    sync.Mutex
	conns map[string]int
	max   int
}

func newConnectionLimiter(max int) *connectionLimiter {
	return &connectionLimiter{conns: make(map[string]int), max: max}
}

// acquire increments the active connection count for userID and returns true if
// the limit has not been reached. Returns false without incrementing if the user
// already holds max concurrent connections.
func (l *connectionLimiter) acquire(userID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.conns[userID] >= l.max {
		return false
	}
	l.conns[userID]++
	return true
}

// release decrements the connection count for userID. Safe to call after acquire.
func (l *connectionLimiter) release(userID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.conns[userID] <= 0 {
		return
	}
	l.conns[userID]--
	if l.conns[userID] == 0 {
		delete(l.conns, userID)
	}
}

// wsHandler is the stateful HTTP handler for WebSocket connections.
// The limiter is shared across all concurrent requests served by this handler.
type wsHandler struct {
	hub           *Hub
	logger        *slog.Logger
	workspaces    WorkspaceResolver
	userIDFromCtx func(*http.Request) string
	limiter       *connectionLimiter
	config        HandlerConfig
}

func newWSHandler(hub *Hub, logger *slog.Logger, workspaces WorkspaceResolver, userIDFromCtx func(*http.Request) string, cfg HandlerConfig) *wsHandler {
	cfg = cfg.withDefaults()
	return &wsHandler{
		hub:           hub,
		logger:        normalizeLogger(logger),
		workspaces:    workspaces,
		userIDFromCtx: userIDFromCtx,
		limiter:       newConnectionLimiter(cfg.MaxConnectionsPerUser),
		config:        cfg,
	}
}

// ServeWS returns an http.Handler that upgrades HTTP connections to WebSocket
// after verifying authentication.
//
// Security invariants (permanent):
//   - Access tokens must never appear in URL query strings.
//   - userID is taken exclusively from server-side request context (set by
//     BearerAuth middleware); the client never provides its own identity.
//   - workspaceID is resolved server-side via WorkspaceResolver; the client
//     never provides its own workspaceID.
//   - Unauthenticated connections are rejected with 401 before any upgrade.
//   - Users at the per-user connection limit are rejected with 429.
//
// userIDFromCtx extracts the authenticated user ID from the request context.
// It must be supplied by the caller (e.g. httpapi.GetContextUserID) to avoid
// a circular package dependency between ws and httpapi.
func ServeWS(hub *Hub, logger *slog.Logger, workspaces WorkspaceResolver, userIDFromCtx func(*http.Request) string) http.Handler {
	return ServeWSWithConfig(hub, logger, workspaces, userIDFromCtx, DefaultHandlerConfig())
}

// ServeWSWithConfig is ServeWS with explicit WebSocket resource controls.
// Invalid config values are replaced with DefaultHandlerConfig values.
func ServeWSWithConfig(hub *Hub, logger *slog.Logger, workspaces WorkspaceResolver, userIDFromCtx func(*http.Request) string, cfg HandlerConfig) http.Handler {
	return newWSHandler(hub, logger, workspaces, userIDFromCtx, cfg)
}

func (h *wsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if rejectCredentialQueryParams(w, r) {
		h.logUpgrade(r, http.StatusBadRequest, "credentials_in_query")
		return
	}
	userID, ok := requireAuthenticatedUser(w, r, h.userIDFromCtx)
	if !ok {
		h.logUpgrade(r, http.StatusUnauthorized, "unauthenticated")
		return
	}
	sessionID, ok := h.requireSessionID(w, r)
	if !ok {
		h.logUpgrade(r, http.StatusUnauthorized, "session_unresolved")
		return
	}
	if h.hub == nil || h.workspaces == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "WebSocket not available")
		h.logUpgrade(r, http.StatusServiceUnavailable, "websocket_not_wired")
		return
	}
	workspaceID, ok := resolveWorkspace(w, r, h.workspaces)
	if !ok {
		h.logUpgrade(r, http.StatusInternalServerError, "workspace_unresolved")
		return
	}
	displayName := resolveDisplayName(r.Context(), h.logger, h.config.DisplayNames, userID)
	if !h.limiter.acquire(userID) {
		httputil.WriteError(w, http.StatusTooManyRequests, "too_many_connections", "connection limit reached")
		h.logUpgrade(r, http.StatusTooManyRequests, "connection_limit_reached")
		return
	}
	defer h.limiter.release(userID)
	runConnection(w, r, h.hub, h.logger, userID, workspaceID, displayName, sessionID, h.config)
}

// requireSessionID reads the session this connection is opened under from the
// request context, where BearerAuth put the validated token's "sid".
//
// Returns ("", true) when no session authority is configured: the deployment has
// no way to re-check anything, and the upgrade is governed by the middleware
// chain alone. When one *is* configured, a missing session ID is refused rather
// than allowed through unwatched — RequireActiveSession guarantees a valid one
// upstream, so its absence means the chain is not what it is supposed to be, and
// a connection nothing can revoke is exactly what this exists to prevent.
func (h *wsHandler) requireSessionID(w http.ResponseWriter, r *http.Request) (string, bool) {
	if h.config.Sessions == nil || h.config.SessionIDFromContext == nil {
		return "", true
	}
	sessionID := h.config.SessionIDFromContext(r)
	if sessionID == "" {
		httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
		return "", false
	}
	return sessionID, true
}

// logUpgrade records the outcome of one upgrade attempt so an operator can tell
// a rejection by this service apart from a proxy-level failure, which never
// reaches this code at all (issue #449).
//
// reason is a fixed server-chosen string, never client input. The request ID is
// the correlation key httputil.RequestID already put on the request and echoed
// in the response header. Nothing here carries a token, a cookie, an Origin, a
// user ID or any message content.
func (h *wsHandler) logUpgrade(r *http.Request, status int, reason string) {
	h.logger.InfoContext(r.Context(), "ws: upgrade rejected",
		"status", status,
		"reason", reason,
		"request_id", httputil.RequestIDFromContext(r.Context()),
	)
}

// rejectCredentialQueryParams returns true and writes 400 if any query parameter
// name matches a known credential name (case-insensitive). The parameter value
// is never logged or echoed.
func rejectCredentialQueryParams(w http.ResponseWriter, r *http.Request) bool {
	for key := range r.URL.Query() {
		if _, bad := credentialParamNames[strings.ToLower(key)]; bad {
			httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest,
				"passing credentials in URL query string is not permitted")
			return true
		}
	}
	return false
}

// requireAuthenticatedUser extracts the server-asserted userID from context.
// Returns (userID, true) on success or ("", false) after writing 401.
func requireAuthenticatedUser(w http.ResponseWriter, r *http.Request, userIDFromCtx func(*http.Request) string) (string, bool) {
	userID := ""
	if userIDFromCtx != nil {
		userID = userIDFromCtx(r)
	}
	if userID == "" {
		httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
		return "", false
	}
	return userID, true
}

// resolveWorkspace obtains the workspaceID server-side.
// Returns (workspaceID, true) on success or ("", false) after writing the appropriate error.
func resolveWorkspace(w http.ResponseWriter, r *http.Request, workspaces WorkspaceResolver) (string, bool) {
	workspaceID, err := workspaces.GetDefaultWorkspaceID(r.Context())
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "workspace not found")
		} else {
			httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
		}
		return "", false
	}
	return workspaceID, true
}

// resolveDisplayName looks up userID's display name once, for the lifetime of
// this connection (see UserDisplayNameResolver). Unlike resolveWorkspace, a
// failure here never fails the upgrade: it degrades to "" and logs a warning,
// mirroring touchTypingStore's warn-and-continue pattern (typing.go).
func resolveDisplayName(ctx context.Context, logger *slog.Logger, resolver UserDisplayNameResolver, userID string) string {
	if resolver == nil {
		return ""
	}
	name, err := resolver.GetDisplayName(ctx, userID)
	if err != nil {
		logger.WarnContext(ctx, "ws: display name lookup failed", "error", err)
		return ""
	}
	return name
}

// runConnection upgrades the HTTP connection to WebSocket, registers the client
// in the hub, starts the I/O pumps, and runs the read loop until the connection
// closes. Cleanup is idempotent via stop/done and sync.Once in wsSender.
//
// The credential protocol is validated but never echoed. The fixed public
// protocol is selected so standards-compliant browsers accept the handshake.
func runConnection(w http.ResponseWriter, r *http.Request, hub *Hub, logger *slog.Logger, userID, workspaceID, displayName, sessionID string, cfg HandlerConfig) {
	logger = normalizeLogger(logger)
	cfg = cfg.withDefaults()

	if proto, ok := wsutil.SubprotocolHeader(r.Header); ok {
		if !wsutil.IsValidSubprotocolToken(proto) {
			httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid websocket subprotocol")
			return
		}
		// Token already extracted and validated by WSTokenMiddleware + BearerAuth.
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols: []string{wsutil.NegotiatedSubprotocol},
	})
	if err != nil {
		// websocket.Accept writes the error response itself. The library error
		// can name internal state, so only the fact and the correlation ID are
		// recorded.
		logger.WarnContext(r.Context(), "ws: upgrade failed",
			"request_id", httputil.RequestIDFromContext(r.Context()))
		return
	}
	conn.SetReadLimit(maxInboundMessageBytes)

	clientID := uuid.New().String()
	snd := newWSSender(conn)
	c := newClient(clientID, userID, workspaceID, snd)
	// Server-side only, and set before the client is reachable: the connection
	// carries the session the handshake proved, so the periodic re-check has an
	// identity no frame can influence.
	c.session = sessionGuard{
		id:        sessionID,
		validator: cfg.Sessions,
		interval:  cfg.SessionRevalidateInterval,
	}
	// Server-side only, resolved once in ServeHTTP (see resolveDisplayName) —
	// never re-queried per typing event, never taken from the client.
	c.displayName = displayName
	if !hub.Register(c) {
		logger.WarnContext(r.Context(), "ws: hub registration failed; closing connection", "client_id", clientID)
		c.close()
		return
	}

	done, stop := startConnectionPumps(r.Context(), c, hub, logger,
		DefaultHeartbeatInterval, DefaultHeartbeatTimeout)
	defer stop()

	readLoop(r.Context(), conn, hub, c, logger, clientID, cfg)

	stop()
	<-done
}

// readLoop reads inbound client control messages and dispatches them to the hub.
//
// Resource controls applied per connection:
//   - Rate limit: token bucket at HandlerConfig.InboundMessagesPerMinute with burst capacity.
//     Exceeding the limit closes the connection with StatusPolicyViolation.
//   - Invalid message limit: connections sending HandlerConfig.MaxInvalidMessages
//     invalid-JSON or unknown-type messages are closed with StatusPolicyViolation.
func readLoop(ctx context.Context, conn *websocket.Conn, hub *Hub, c *Client, logger *slog.Logger, clientID string, cfg HandlerConfig) {
	cfg = cfg.withDefaults()
	bucket := newInboundTokenBucket(cfg)
	invalidCount := 0

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}

		if !consumeInboundToken(&bucket, time.Now()) {
			if logger != nil {
				logger.WarnContext(ctx, "ws: inbound rate limit exceeded; closing", "client_id", clientID)
			}
			closePolicyViolation(conn, "rate limit exceeded")
			return
		}

		msg, jsonErr := decodeClientMessage(data)
		if jsonErr != nil {
			if handleInvalidInboundMessage(ctx, conn, logger, clientID, &invalidCount, cfg.MaxInvalidMessages, "ws: invalid client message") {
				return
			}
			continue
		}
		if msg.Type == ClientMessageTypeSubscribe || msg.Type == ClientMessageTypeUnsubscribe {
			target, validationErr := normalizeSubscriptionTarget(msg)
			if validationErr != nil {
				if handleInvalidInboundMessage(ctx, conn, logger, clientID, &invalidCount, cfg.MaxInvalidMessages, "ws: invalid subscribe message") {
					return
				}
				continue
			}
			msg.TargetType = target.targetType
			msg.TargetID = target.targetID
		}

		// Read before the dispatch, because the dispatch is what may create the
		// subscription. Only a change here means a subscription was actually
		// added, which is what a presence snapshot answers (RF-58).
		var subscribeGeneration uint64
		if msg.Type == ClientMessageTypeSubscribe {
			subscribeGeneration = hub.subscriptionGeneration(c, targetKey{
				workspaceID: c.workspaceID, targetType: msg.TargetType, targetID: msg.TargetID,
			}.String())
		}

		if msgErr := hub.handleClientMessage(ctx, c, msg); msgErr != nil {
			if handleReactionClientError(c, msgErr) {
				continue
			}
			if isCallClientMessage(msg.Type) && handleCallClientError(c, msg.Type, msg.CallID, msgErr) {
				continue
			}
			if isTypingClientMessage(msg.Type) && handleTypingClientError(c, msgErr) {
				continue
			}
			if msg.Type == ClientMessageTypeSubscribe {
				if c.ctx.Err() != nil || ctx.Err() != nil || errors.Is(msgErr, ErrClientNotRegistered) {
					return
				}
				if logger != nil && !errors.Is(msgErr, ErrSubscribeForbidden) {
					logger.WarnContext(ctx, "ws: subscribe authorization failed",
						"client_id", clientID,
						"target_type", string(msg.TargetType),
						"error", msgErr,
					)
				}
				handleSubscribeClientError(c, msgErr)
				continue
			}
			if handleInvalidInboundMessage(ctx, conn, logger, clientID, &invalidCount, cfg.MaxInvalidMessages, "ws: handleClientMessage error") {
				return
			}
			continue
		}
		if msg.Type == ClientMessageTypeSubscribe {
			if !handleSubscribeClientSuccess(c, msg) {
				hub.Unregister(c)
				return
			}
			// After the acknowledgement, so the client has already been told the
			// subscription exists before it is told who is in it (RF-58). The
			// generation captured before the dispatch is what distinguishes a
			// real join from a repeated subscribe to a target already held.
			hub.handleSubscribed(c, msg.TargetType, msg.TargetID, subscribeGeneration)
		}
	}
}

func decodeClientMessage(data []byte) (ClientMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var msg ClientMessage
	if err := dec.Decode(&msg); err != nil {
		return ClientMessage{}, err
	}
	// second Decode detects concatenated JSON frames.
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ClientMessage{}, errors.New("ws: trailing JSON data")
	}
	return msg, nil
}

type clientErrorResponse struct {
	Type       string `json:"type"`
	Operation  string `json:"operation,omitempty"`
	Code       string `json:"code"`
	CallID     string `json:"call_id,omitempty"`
	RetryAfter int    `json:"retry_after,omitempty"`
	Limit      int    `json:"limit,omitempty"`
}

type subscribeSuccessResponse struct {
	Type       string     `json:"type"`
	Operation  string     `json:"operation"`
	TargetType TargetType `json:"target_type"`
	TargetID   string     `json:"target_id"`
}

type subscriptionTarget struct {
	targetType TargetType
	targetID   string
}

func normalizeSubscriptionTarget(msg ClientMessage) (subscriptionTarget, error) {
	if msg.Type != ClientMessageTypeSubscribe && msg.Type != ClientMessageTypeUnsubscribe {
		return subscriptionTarget{}, fmt.Errorf("ws: subscription: invalid message type")
	}
	if msg.TargetType != TargetTypeChannel && msg.TargetType != TargetTypeDM {
		return subscriptionTarget{}, fmt.Errorf("ws: subscription: unsupported target_type")
	}
	if msg.MessageID != "" || msg.Emoji != "" {
		return subscriptionTarget{}, fmt.Errorf("ws: subscription: unexpected fields")
	}
	rawTargetID := strings.TrimSpace(msg.TargetID)
	if rawTargetID == "" {
		return subscriptionTarget{}, fmt.Errorf("ws: subscription: target_id required")
	}
	parsedID, err := uuid.Parse(rawTargetID)
	if err != nil {
		return subscriptionTarget{}, fmt.Errorf("ws: subscription: invalid target_id format")
	}
	return subscriptionTarget{targetType: msg.TargetType, targetID: parsedID.String()}, nil
}

func handleSubscribeClientSuccess(c *Client, msg ClientMessage) bool {
	data, err := json.Marshal(subscribeSuccessResponse{
		Type:       "subscribed",
		Operation:  "subscribe",
		TargetType: msg.TargetType,
		TargetID:   msg.TargetID,
	})
	return err == nil && c.enqueue(data)
}

func handleSubscribeClientError(c *Client, subscribeErr error) {
	code := "room_subscription_unavailable"
	if errors.Is(subscribeErr, ErrSubscribeForbidden) {
		code = "room_access_denied"
	}
	data, err := json.Marshal(clientErrorResponse{Type: "error", Operation: "subscribe", Code: code})
	if err == nil {
		c.enqueue(data)
	}
}

func validateSubscribe(msg ClientMessage) error {
	_, err := normalizeSubscriptionTarget(msg)
	return err
}

// handleReactionClientError classifies expected reaction outcomes so they do
// not consume the malformed-message budget or produce noisy client-error logs.
func handleReactionClientError(c *Client, err error) bool {
	response := clientErrorResponse{Type: "error"}
	switch {
	case errors.Is(err, ErrReactionRateLimited):
		response.Code = "rate_limited"
		response.RetryAfter = 60
	case errors.Is(err, ErrReactionFeatureDisabled):
		response.Code = "temporarily_unavailable"
	case errors.Is(err, domain.ErrReactionLimitReached):
		response.Code = "reaction_limit_reached"
		response.Limit = 5
	default:
		return false
	}
	data, marshalErr := json.Marshal(response)
	if marshalErr == nil {
		c.enqueue(data)
	}
	return true
}

type inboundTokenBucket struct {
	tokens            int
	lastRefill        time.Time
	messagesPerMinute int
	burst             int
}

func newInboundTokenBucket(cfg HandlerConfig) inboundTokenBucket {
	cfg = cfg.withDefaults()
	return inboundTokenBucket{
		tokens:            cfg.InboundBurst,
		lastRefill:        time.Now(),
		messagesPerMinute: cfg.InboundMessagesPerMinute,
		burst:             cfg.InboundBurst,
	}
}

// consumeInboundToken refills and consumes one token from the per-connection token bucket.
// Must be called exclusively from the readLoop goroutine — not concurrency-safe by design.
func consumeInboundToken(bucket *inboundTokenBucket, now time.Time) bool {
	if elapsed := now.Sub(bucket.lastRefill); elapsed > 0 {
		refill := int(elapsed.Nanoseconds() * int64(bucket.messagesPerMinute) / int64(time.Minute))
		if refill > 0 {
			bucket.tokens += refill
			if bucket.tokens > bucket.burst {
				bucket.tokens = bucket.burst
			}
			used := time.Duration(int64(refill) * int64(time.Minute) / int64(bucket.messagesPerMinute))
			if used <= 0 {
				bucket.lastRefill = now
			} else {
				bucket.lastRefill = bucket.lastRefill.Add(used)
			}
		}
	}
	if bucket.tokens <= 0 {
		return false
	}
	bucket.tokens--
	return true
}

func handleInvalidInboundMessage(
	ctx context.Context,
	conn *websocket.Conn,
	logger *slog.Logger,
	clientID string,
	invalidCount *int,
	maxInvalidMessages int,
	logMessage string,
) bool {
	if *invalidCount == 0 && logger != nil {
		logger.WarnContext(ctx, logMessage, "client_id", clientID)
	}
	*invalidCount++
	if shouldCloseForInvalidMessages(*invalidCount, maxInvalidMessages) {
		closePolicyViolation(conn, "too many invalid messages")
		return true
	}
	return false
}

func shouldCloseForInvalidMessages(invalidCount, maxInvalidMessages int) bool {
	return invalidCount >= maxInvalidMessages
}

func closePolicyViolation(conn *websocket.Conn, reason string) {
	_ = conn.Close(websocket.StatusPolicyViolation, reason)
}
