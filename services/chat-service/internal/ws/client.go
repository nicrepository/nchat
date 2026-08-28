package ws

import (
	"context"
	"log/slog"
	"sync"
)

// outboxSize is the maximum number of JSON-encoded events buffered per client.
// On overflow the client is dropped immediately; this bounds per-connection memory.
const outboxSize = 256

// sender abstracts the write side of a WebSocket connection.
//
// Concurrency: all methods MUST be safe for concurrent use from multiple
// goroutines. writePump calls Send exclusively from its goroutine;
// startHeartbeat calls Ping exclusively from its goroutine; and dropClient
// may call Close from the hub run goroutine at teardown, overlapping with
// an in-progress Send or Ping. Implementations must handle this safely.
// coder/websocket satisfies this requirement natively.
// Test implementations must use a mutex (see controllableSender, fakeSender).
type sender interface {
	// Send transmits data to the remote end. Must not block indefinitely.
	Send(data []byte) error
	// Ping sends a WebSocket ping frame and waits for the pong reply.
	// ctx limits the wait; a timeout or cancellation returns a non-nil error.
	// Used by startHeartbeat to detect dead connections.
	Ping(ctx context.Context) error
	// Close terminates the underlying connection. Safe to call concurrently
	// with Send or Ping; the connection is closed exactly once.
	Close()
}

// Client represents a single connected WebSocket client.
//
// userID and workspaceID are always extracted from the server-side authenticated
// request context; they are never accepted from client-provided input.
type Client struct {
	// id is an opaque internal identifier, distinct from userID.
	id          string
	userID      string
	workspaceID string
	// session is the server-side identity this connection was opened with, plus
	// the authority that can say whether it is still valid. Internal metadata:
	// it is never part of the WebSocket protocol in either direction, and the
	// client cannot name, change or observe it. Zero when no session authority
	// is wired, which leaves the connection with upgrade-time validation only.
	session sessionGuard
	// displayName is this connection's user's display name, resolved once at
	// connect time (see UserDisplayNameResolver, handler.go), never re-queried
	// per typing event. Empty when the lookup failed or is disabled —
	// publishTypingUpdated then sends no name and the frontend falls back to
	// its own heuristic.
	displayName string
	outbox      chan []byte
	snd         sender
	ctx         context.Context
	cancel      context.CancelFunc
	closeOnce   sync.Once
}

// newClient creates a Client with a bounded outbox.
// id must be a server-generated opaque identifier (e.g., UUID).
// userID and workspaceID must be extracted from the authenticated request context.
func newClient(id, userID, workspaceID string, snd sender) *Client {
	ctx, cancel := context.WithCancel(context.Background())
	return &Client{
		id:          id,
		userID:      userID,
		workspaceID: workspaceID,
		outbox:      make(chan []byte, outboxSize),
		snd:         snd,
		ctx:         ctx,
		cancel:      cancel,
	}
}

// enqueue attempts to place data in the client's outbound queue.
// Returns false if the queue is full (slow client); the caller must drop the client.
func (c *Client) enqueue(data []byte) bool {
	select {
	case c.outbox <- data:
		return true
	default:
		return false
	}
}

// close terminates the client's connection exactly once.
func (c *Client) close() {
	c.closeOnce.Do(func() {
		c.cancel()
		c.snd.Close()
	})
}

// writePump drains c.outbox and forwards each message to the underlying
// connection via c.snd.Send. It must run in a dedicated goroutine.
//
// When used via startConnectionPumps, the wrapper goroutine's deferred
// connCancel propagates any exit — fatal or clean — to the sibling
// startHeartbeat goroutine so it stops promptly.
//
// On a send error hub.Unregister(c) (deferred) triggers the full cleanup
// chain (removeClient → close → presence.Disconnect). Additionally, c.close()
// is called eagerly so that any in-progress Ping in the sibling startHeartbeat
// goroutine receives an immediate connection-closed error rather than blocking
// until the ping timeout. On ctx cancellation hub.Unregister is still called
// via the defer, and c.close() is not called again (idempotent via sync.Once).
//
// Lock ordering: c.snd.Send is never called while hub.mu is held; writePump
// always runs outside the hub run goroutine, so this invariant holds.
func (c *Client) writePump(ctx context.Context, hub *Hub, logger *slog.Logger) {
	logger = normalizeLogger(logger)
	defer hub.Unregister(c)
	for {
		select {
		case data, ok := <-c.outbox:
			if !ok {
				return
			}
			if err := c.snd.Send(data); err != nil {
				logger.WarnContext(ctx, "ws: send error; dropping client",
					"client_id", c.id,
				)
				// Close the underlying connection immediately so that any
				// in-progress Ping in the sibling heartbeat goroutine fails
				// fast rather than blocking until the ping timeout.
				c.close()
				return
			}
		case <-ctx.Done():
			return
		}
	}
}
