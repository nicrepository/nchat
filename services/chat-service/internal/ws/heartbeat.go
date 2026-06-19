package ws

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// DefaultHeartbeatInterval is the recommended interval between server-initiated
// WebSocket ping frames. Callers may override it via startConnectionPumps parameters.
const DefaultHeartbeatInterval = 30 * time.Second

// DefaultHeartbeatTimeout is the maximum time the server waits for a pong
// reply before treating the connection as dead and dropping the client.
const DefaultHeartbeatTimeout = 10 * time.Second

// startConnectionPumps is the lifecycle owner for a WebSocket connection's
// I/O goroutines.
//
// It derives a connection-scoped context (pumpCtx) from ctx and launches two
// goroutines: writePump and startHeartbeat. Each goroutine is wrapped so that
// any exit — fatal or clean — calls pumpCancel, causing the sibling goroutine
// to observe ctx.Done() and exit promptly.
//
// # Returned values
//
//   - done: closed when both goroutines have exited.
//   - stop: cancels pumpCtx and closes the underlying connection immediately;
//     idempotent (safe to call multiple times or from a defer). Calling stop()
//     does not wait for goroutine exit; await <-done for that.
//     stop() also calls c.close() to interrupt any in-flight Send or Ping call,
//     ensuring the pumps exit immediately rather than blocking on I/O.
//
// # Caller contract
//
//	done, stop := startConnectionPumps(r.Context(), c, hub, logger,
//	    DefaultHeartbeatInterval, DefaultHeartbeatTimeout)
//	defer stop() // ensures pumps stop if read loop returns for any reason
//	// ... read loop ...
//	stop()  // signal pumps to exit (idempotent with defer)
//	<-done  // wait for both goroutines to exit before releasing resources
//
// # Pump-to-pump cancellation
//
// Any fatal error from writePump or startHeartbeat cancels pumpCtx through the
// deferred pumpCancel in each wrapper goroutine. This prevents writePump from
// blocking indefinitely on an empty outbox after a heartbeat failure, and
// prevents the heartbeat from ticking after a write error.
//
// # Handler integration
//
// ServeWS performs the real WebSocket upgrade and wires each accepted
// connection through startConnectionPumps, so writePump and startHeartbeat are
// active for browser clients. Browser-side reconnect/backoff remains outside
// this server lifecycle, and scroll-infinite pagination is outside this package.
//
// interval and timeout must both be positive. If either is ≤ 0 the function
// falls back to the defaults to avoid a runtime panic from time.NewTicker.
//
// Cleanup (hub.Unregister, presence.Disconnect, connection close) is handled
// by the pump goroutines themselves and is idempotent.
func startConnectionPumps(
	ctx context.Context,
	c *Client,
	hub *Hub,
	logger *slog.Logger,
	interval, timeout time.Duration,
) (done <-chan struct{}, stop func()) {
	logger = normalizeLogger(logger)
	if interval <= 0 {
		interval = DefaultHeartbeatInterval
	}
	if timeout <= 0 {
		timeout = DefaultHeartbeatTimeout
	}

	pumpCtx, pumpCancel := context.WithCancel(ctx)

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		defer pumpCancel() // any exit cancels the sibling's context
		c.writePump(pumpCtx, hub, logger)
	}()

	go func() {
		defer wg.Done()
		defer pumpCancel()
		startHeartbeat(pumpCtx, c, hub, logger, interval, timeout)
	}()

	ch := make(chan struct{})
	go func() {
		wg.Wait()
		close(ch)
	}()

	stop = func() {
		pumpCancel()
		// Close the connection so any in-flight Send or Ping returns immediately
		// rather than blocking until the peer responds or the I/O deadline fires.
		c.close()
	}
	return ch, stop
}

// startHeartbeat sends periodic WebSocket ping frames to c via c.snd.Ping to
// detect dead connections. It must be called in a dedicated goroutine and
// returns when ctx is cancelled or a ping fails.
//
// On ping failure the client is immediately unregistered via
// hub.Unregister → dropClient and the underlying connection closed via
// c.close(). Both operations are idempotent, so concurrent calls from the
// write pump or handler teardown are safe.
//
// When run via startConnectionPumps, the deferred connCancel in the wrapper
// goroutine additionally cancels the shared context on return, which causes
// writePump to exit via ctx.Done without waiting for its next outbox message.
//
// Security:
//   - No tokens, credentials, workspace content, or subscription data are logged.
//   - Only the opaque client_id (server-generated UUID) is logged on failure.
//
// Lock ordering: hub.mu is never held during the Ping or close calls; both
// run exclusively in this goroutine.
func startHeartbeat(
	ctx context.Context,
	c *Client,
	hub *Hub,
	logger *slog.Logger,
	interval, timeout time.Duration,
) {
	logger = normalizeLogger(logger)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			pingCtx, pingCancel := context.WithTimeout(ctx, timeout)
			err := c.snd.Ping(pingCtx)
			pingCancel()
			if err != nil {
				// If the parent context was cancelled (clean shutdown or handler
				// teardown), the Ping error is a side-effect of cancellation —
				// not a dead peer. Exit quietly without logging or unregistering.
				if ctx.Err() != nil {
					return
				}
				logger.WarnContext(ctx, "ws: heartbeat ping failed; dropping client",
					"client_id", c.id,
				)
				// Unregister before closing so hub state is consistent.
				hub.Unregister(c)
				// Close the connection immediately so any in-progress Send in
				// writePump fails and writePump exits without waiting for the
				// next outbox message.
				c.close()
				return
			}
		case <-ctx.Done():
			return
		}
	}
}
