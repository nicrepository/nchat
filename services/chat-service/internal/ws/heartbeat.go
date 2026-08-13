package ws

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// DefaultHeartbeatInterval is the recommended interval between server-initiated
// WebSocket ping frames. Callers may override it via startConnectionPumps parameters.
const DefaultHeartbeatInterval = 30 * time.Second

// DefaultHeartbeatTimeout is the maximum time the server waits for a pong
// reply before treating the connection as dead and dropping the client.
const DefaultHeartbeatTimeout = 10 * time.Second

// DefaultSessionRevalidateInterval is how often a live connection re-checks that
// the session that opened it is still active.
//
// A WebSocket is authenticated once, at the upgrade, and then lives for as long
// as the tab does. Without this the only thing a logout, a revoked device or a
// suspended user could not reach is the connection they were most likely to be
// using: it would keep receiving events and keep holding the person present.
//
// A minute is the whole budget: one indexed lookup per connection per minute,
// never per frame. It is deliberately not tighter — the check costs a database
// round trip, and a revocation that lands within a minute is a bound the session
// authority itself does not beat.
const DefaultSessionRevalidateInterval = time.Minute

// sessionRevalidateTimeout bounds one such check, so a stalled session store
// cannot pin the heartbeat goroutine and stop the pings with it.
const sessionRevalidateTimeout = 5 * time.Second

// SessionValidator answers whether a session is still active. It is the same
// contract the HTTP routes use through RequireActiveSession, satisfied by the
// same store: this package re-asks the existing authority rather than owning a
// second opinion about what a valid session is.
type SessionValidator interface {
	ValidateActiveSession(ctx context.Context, userID, sessionID string) error
}

// sessionGuard is what one connection needs to re-verify itself: the session
// the handshake asserted, and the authority to ask about it.
//
// The id comes from the validated token's "sid" claim, put on the request by
// the auth middleware. It is never read from a frame, never sent to the client
// and never logged. A zero guard means no revalidation is wired.
type sessionGuard struct {
	id        string
	validator SessionValidator
	interval  time.Duration
}

func (g sessionGuard) enabled() bool { return g.validator != nil && g.id != "" }

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

	// The session check rides this goroutine rather than starting one of its
	// own: it is a periodic lifecycle question about the same connection, on the
	// same cancellation, with the same teardown. A second ticker is the whole
	// cost — the cadences differ, the ownership does not.
	var sessionTick <-chan time.Time
	if c.session.enabled() {
		sessionTicker := time.NewTicker(c.session.interval)
		defer sessionTicker.Stop()
		sessionTick = sessionTicker.C
	}

	for {
		select {
		case <-sessionTick:
			if !revalidateSession(ctx, c, hub, logger) {
				return
			}
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

// revalidateSession re-asks the session authority about this connection and
// reports whether the connection may continue.
//
// A definitive "no" — revoked, expired, absent, user suspended — closes the
// connection through the ordinary path: hub.Unregister runs the same teardown a
// dead peer gets, so the subscriptions are released, the presence tracker loses
// the connection and the resulting offline is published by the code that
// already knows how. Nothing about that cleanup is repeated here.
//
// Any other error is an infrastructure failure, not evidence that a session
// ended, and it is treated as such: the connection stays and the next tick asks
// again. This is the policy RequireActiveSession already applies at the
// upgrade, where the same two error classes separate a 401 from a 500. Failing
// closed here would instead turn one unavailable database into every connected
// client being disconnected at once.
//
// Security: nothing about the session reaches the log — not the session ID, not
// the user. Only the opaque server-generated client_id.
func revalidateSession(ctx context.Context, c *Client, hub *Hub, logger *slog.Logger) bool {
	checkCtx, cancel := context.WithTimeout(ctx, sessionRevalidateTimeout)
	err := c.session.validator.ValidateActiveSession(checkCtx, c.userID, c.session.id)
	cancel()
	if err == nil {
		return true
	}
	if !errors.Is(err, domain.ErrInvalidToken) && !errors.Is(err, domain.ErrNotFound) {
		if ctx.Err() != nil {
			return false
		}
		logger.WarnContext(ctx, "ws: session revalidation failed; keeping connection",
			"client_id", c.id,
		)
		return true
	}
	if ctx.Err() != nil {
		return false
	}
	logger.InfoContext(ctx, "ws: session no longer active; dropping client",
		"client_id", c.id,
	)
	hub.Unregister(c)
	c.close()
	return false
}
