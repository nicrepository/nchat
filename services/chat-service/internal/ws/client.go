package ws

import "sync"

// outboxSize is the maximum number of JSON-encoded events buffered per client.
// On overflow the client is dropped immediately; this bounds per-connection memory.
const outboxSize = 256

// sender abstracts the write side of a WebSocket connection.
// The production implementation wraps coder/websocket; tests use fakeSender.
type sender interface {
	// Send transmits data to the remote end. Must not block indefinitely.
	Send(data []byte) error
	// Close terminates the underlying connection.
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
	outbox      chan []byte
	snd         sender
	closeOnce   sync.Once
}

// newClient creates a Client with a bounded outbox.
// id must be a server-generated opaque identifier (e.g., UUID).
// userID and workspaceID must be extracted from the authenticated request context.
func newClient(id, userID, workspaceID string, snd sender) *Client {
	return &Client{
		id:          id,
		userID:      userID,
		workspaceID: workspaceID,
		outbox:      make(chan []byte, outboxSize),
		snd:         snd,
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
		c.snd.Close()
	})
}
