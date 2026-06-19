package ws

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
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
		return
	}
	userID, ok := requireAuthenticatedUser(w, r, h.userIDFromCtx)
	if !ok {
		return
	}
	if h.hub == nil || h.workspaces == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "WebSocket not available")
		return
	}
	workspaceID, ok := resolveWorkspace(w, r, h.workspaces)
	if !ok {
		return
	}
	if !h.limiter.acquire(userID) {
		httputil.WriteError(w, http.StatusTooManyRequests, "too_many_connections", "connection limit reached")
		return
	}
	defer h.limiter.release(userID)
	runConnection(w, r, h.hub, h.logger, userID, workspaceID, h.config)
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

// runConnection upgrades the HTTP connection to WebSocket, registers the client
// in the hub, starts the I/O pumps, and runs the read loop until the connection
// closes. Cleanup is idempotent via stop/done and sync.Once in wsSender.
func runConnection(w http.ResponseWriter, r *http.Request, hub *Hub, logger *slog.Logger, userID, workspaceID string, cfg HandlerConfig) {
	logger = normalizeLogger(logger)
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		// websocket.Accept writes the error response itself.
		logger.WarnContext(r.Context(), "ws: upgrade failed")
		return
	}
	conn.SetReadLimit(maxInboundMessageBytes)

	clientID := uuid.New().String()
	snd := newWSSender(conn)
	c := newClient(clientID, userID, workspaceID, snd)
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

		var msg ClientMessage
		if jsonErr := json.Unmarshal(data, &msg); jsonErr != nil {
			if handleInvalidInboundMessage(ctx, conn, logger, clientID, &invalidCount, cfg.MaxInvalidMessages, "ws: invalid client message") {
				return
			}
			continue
		}

		if msgErr := hub.handleClientMessage(ctx, c, msg); msgErr != nil {
			if handleInvalidInboundMessage(ctx, conn, logger, clientID, &invalidCount, cfg.MaxInvalidMessages, "ws: handleClientMessage error") {
				return
			}
		}
	}
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
