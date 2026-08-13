package ws

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// Session revalidation on a live connection (RF-58, SR-2).
//
// A WebSocket authenticates once and then lives as long as the tab does. What
// these tests hold is that a session which stops being valid stops being able to
// use the socket it opened — through the ordinary teardown, on a bounded
// cadence, with an identity the client never gets to name.

// ── fake session authority ───────────────────────────────────────────────────

// fakeSessions answers for a fixed set of sessions and counts every question,
// which is what makes "not per frame" an assertion rather than a claim.
type fakeSessions struct {
	mu      sync.Mutex
	err     error
	calls   int
	asked   []string
	answers chan<- struct{}
}

func (s *fakeSessions) ValidateActiveSession(_ context.Context, _, sessionID string) error {
	s.mu.Lock()
	s.calls++
	s.asked = append(s.asked, sessionID)
	err := s.err
	answers := s.answers
	s.mu.Unlock()

	// Signalled after the lock is released so a test can wait on a cycle without
	// deadlocking against its own assertions.
	if answers != nil {
		select {
		case answers <- struct{}{}:
		default:
		}
	}
	return err
}

func (s *fakeSessions) revoke(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

func (s *fakeSessions) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *fakeSessions) sessionsAsked() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.asked...)
}

func (s *fakeSessions) notify(ch chan<- struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.answers = ch
}

// guardedClient is a registered client carrying a session guard, which is what
// the handler builds from the authenticated handshake.
func guardedClient(t *testing.T, h *Hub, id, userID, sessionID string, sessions SessionValidator, interval time.Duration) *Client {
	t.Helper()
	c := newClient(id, userID, "ws-1", &fakeSender{})
	c.session = sessionGuard{id: sessionID, validator: sessions, interval: interval}
	registerInRunningHub(t, h, c)
	return c
}

// ── an active session is left alone ──────────────────────────────────────────

func TestSessionRevalidation_ActiveSessionKeepsTheConnection(t *testing.T) {
	tracker := NewPresenceTracker(time.Hour)
	defer tracker.Stop()

	auth := &fakeAuthorizer{}
	auth.setAccess("user-1", "ws-1", TargetTypeChannel, "chan-1", true)
	hub := NewHub(auth, newTestLogger(), NopBus{}, "session-active", WithPresence(tracker))
	defer hub.Shutdown()

	sessions := &fakeSessions{}
	cycles := make(chan struct{}, 8)
	sessions.notify(cycles)

	c := guardedClient(t, hub, "c-1", "user-1", "sess-a", sessions, time.Millisecond)
	tracker.Connect(c.workspaceID, c.userID, c.id)
	if err := hub.Subscribe(context.Background(), c, TargetTypeChannel, "chan-1"); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		// A ping interval far longer than the test, so only the session cycle runs.
		startHeartbeat(ctx, c, hub, slog.Default(), time.Hour, time.Second)
	}()

	// Several cycles, all answered "still valid".
	for i := 0; i < 3; i++ {
		<-cycles
	}

	if !hubHasClient(hub, c.id) {
		t.Fatal("a valid session lost its connection")
	}
	key := targetKey{workspaceID: "ws-1", targetType: TargetTypeChannel, targetID: "chan-1"}.String()
	if !hubHasClientSubscription(hub, c.id, key) {
		t.Fatal("a valid session lost its subscription")
	}
	if got := tracker.Status("ws-1", "user-1"); got != PresenceOnline {
		t.Fatalf("a valid session stopped holding presence: %q", got)
	}

	cancel()
	<-heartbeatDone
}

// ── a revoked session loses the socket ───────────────────────────────────────

func TestSessionRevalidation_RevokedSessionDropsTheConnection(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "revoked or expired", err: domain.ErrInvalidToken},
		{name: "session no longer exists", err: domain.ErrNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tracker := NewPresenceTracker(time.Hour)
			defer tracker.Stop()

			auth := &fakeAuthorizer{}
			auth.setAccess("user-1", "ws-1", TargetTypeChannel, "chan-1", true)
			hub := NewHub(auth, newTestLogger(), NopBus{}, "session-revoked", WithPresence(tracker))
			defer hub.Shutdown()

			sessions := &fakeSessions{}
			c := guardedClient(t, hub, "c-1", "user-1", "sess-a", sessions, time.Millisecond)
			tracker.Connect(c.workspaceID, c.userID, c.id)
			if err := hub.Subscribe(context.Background(), c, TargetTypeChannel, "chan-1"); err != nil {
				t.Fatalf("subscribe: %v", err)
			}
			key := targetKey{workspaceID: "ws-1", targetType: TargetTypeChannel, targetID: "chan-1"}.String()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			heartbeatDone := make(chan struct{})
			go func() {
				defer close(heartbeatDone)
				startHeartbeat(ctx, c, hub, slog.Default(), time.Hour, time.Second)
			}()

			sessions.revoke(tc.err)

			// The heartbeat goroutine returns on its own: the connection is gone.
			<-heartbeatDone

			eventually(t, func() bool { return !hubHasClient(hub, c.id) }, time.Second,
				"a revoked session kept its connection registered")
			if hubHasClientSubscription(hub, c.id, key) {
				t.Fatal("a revoked session kept its subscription")
			}
			// The ordinary teardown ran, so presence was recalculated rather than
			// left asserting a connection that no longer exists. It is the tail of
			// that same teardown, so it settles just after the hub state does.
			eventually(t, func() bool { return tracker.Status("ws-1", "user-1") == PresenceOffline },
				time.Second, "a revoked session still holds presence")

			// And nothing from that target reaches it afterwards.
			drainFrames(c)
			hub.PublishPinUpdated(context.Background(), "ws-1", TargetTypeChannel, "chan-1", "m-1", "user-2", true)
			eventually(t, func() bool { return true }, 50*time.Millisecond, "publish settled")
			for _, frame := range drainFrames(c) {
				if strings.Contains(frame, "pin.updated") {
					t.Fatalf("a revoked connection still received events: %s", frame)
				}
			}
		})
	}
}

// Activity is not a second authentication. Once the revalidation has dropped the
// connection, a ping on it cannot bring the session, the client or the presence
// back — the connection is no longer in the tracker to be refreshed.
func TestSessionRevalidation_ActivityCannotRestoreARevokedConnection(t *testing.T) {
	tracker := NewPresenceTracker(time.Hour)
	defer tracker.Stop()

	hub := NewHub(&fakeAuthorizer{}, newTestLogger(), NopBus{}, "session-ping", WithPresence(tracker))
	defer hub.Shutdown()

	sessions := &fakeSessions{}
	c := guardedClient(t, hub, "c-1", "user-1", "sess-a", sessions, time.Millisecond)
	tracker.Connect(c.workspaceID, c.userID, c.id)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		startHeartbeat(ctx, c, hub, slog.Default(), time.Hour, time.Second)
	}()

	sessions.revoke(domain.ErrInvalidToken)
	<-heartbeatDone
	eventually(t, func() bool {
		return !hubHasClient(hub, c.id) && tracker.Status("ws-1", "user-1") == PresenceOffline
	}, time.Second, "a revoked session kept its connection registered")

	// A late ping from the same connection.
	if change := tracker.RecordActivity("ws-1", "user-1", c.id); change.Changed || change.Status != PresenceOffline {
		t.Fatalf("activity on a revoked connection changed presence: %+v", change)
	}
	if got := tracker.Status("ws-1", "user-1"); got != PresenceOffline {
		t.Fatalf("a ping restored a revoked user's presence: %q", got)
	}
	if hubHasClient(hub, c.id) {
		t.Fatal("a ping put a revoked connection back in the hub")
	}
}

// ── one revoked session, one that is still valid ─────────────────────────────

// Revocation is per session, and presence is aggregated per user. Signing out of
// one device must close that device's socket and nothing else — the person is
// still here, on the session that is still valid.
func TestSessionRevalidation_OtherSessionsOfTheSameUserSurvive(t *testing.T) {
	tracker := NewPresenceTracker(time.Hour)
	defer tracker.Stop()

	hub := NewHub(&fakeAuthorizer{}, newTestLogger(), NopBus{}, "session-multi", WithPresence(tracker))
	defer hub.Shutdown()

	// Two authorities, because they answer for two different sessions: only the
	// first one is revoked.
	revoked := &fakeSessions{}
	valid := &fakeSessions{}
	validCycles := make(chan struct{}, 4)
	valid.notify(validCycles)

	a := guardedClient(t, hub, "c-a", "user-1", "sess-a", revoked, time.Millisecond)
	b := guardedClient(t, hub, "c-b", "user-1", "sess-b", valid, time.Millisecond)
	tracker.Connect(a.workspaceID, a.userID, a.id)
	tracker.Connect(b.workspaceID, b.userID, b.id)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	aDone, bDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(aDone)
		startHeartbeat(ctx, a, hub, slog.Default(), time.Hour, time.Second)
	}()
	go func() {
		defer close(bDone)
		startHeartbeat(ctx, b, hub, slog.Default(), time.Hour, time.Second)
	}()

	revoked.revoke(domain.ErrInvalidToken)
	<-aDone

	eventually(t, func() bool { return !hubHasClient(hub, a.id) }, time.Second,
		"the revoked session kept its connection")

	// The other session went on being asked, and went on being answered.
	<-validCycles
	if !hubHasClient(hub, b.id) {
		t.Fatal("revoking one session closed another")
	}
	if got := tracker.Status("ws-1", "user-1"); got != PresenceOnline {
		t.Fatalf("the user went offline while a valid session was still connected: %q", got)
	}

	cancel()
	<-bDone
}

// ── a failure to ask is not an answer ────────────────────────────────────────

// The session store being unreachable says nothing about whether a session
// ended. Failing closed here would turn one unavailable database into every
// connected client being disconnected at once, so the connection is kept and the
// next cycle asks again — the same split RequireActiveSession makes at the
// upgrade between a 401 and a 500.
func TestSessionRevalidation_TransientFailureKeepsTheConnection(t *testing.T) {
	hub := NewHub(&fakeAuthorizer{}, newTestLogger(), NopBus{}, "session-transient")
	defer hub.Shutdown()

	sessions := &fakeSessions{err: errors.New("dial tcp: connection refused")}
	cycles := make(chan struct{}, 8)
	sessions.notify(cycles)

	c := guardedClient(t, hub, "c-1", "user-1", "sess-a", sessions, time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		startHeartbeat(ctx, c, hub, slog.Default(), time.Hour, time.Second)
	}()

	// Several failed cycles in a row, and the connection is still there.
	for i := 0; i < 3; i++ {
		<-cycles
	}
	if !hubHasClient(hub, c.id) {
		t.Fatal("an unreachable session store disconnected a client")
	}

	cancel()
	<-heartbeatDone
}

// ── the cadence is a lifecycle one, not a per-frame one ──────────────────────

// Inbound frames are not authentication events. A hundred of them between two
// cycles must cost nothing: the check belongs to the connection's lifecycle, and
// putting it on the frame path would be a database round trip per message.
func TestSessionRevalidation_IsNotAskedPerFrame(t *testing.T) {
	tracker := NewPresenceTracker(time.Hour)
	defer tracker.Stop()

	auth := &fakeAuthorizer{}
	auth.setAccess("user-1", "ws-1", TargetTypeChannel, "chan-1", true)
	hub := NewHub(auth, newTestLogger(), NopBus{}, "session-cadence", WithPresence(tracker))
	defer hub.Shutdown()

	sessions := &fakeSessions{}
	// An interval longer than the test: no cycle is due while the frames arrive.
	c := guardedClient(t, hub, "c-1", "user-1", "sess-a", sessions, time.Hour)
	tracker.Connect(c.workspaceID, c.userID, c.id)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		startHeartbeat(ctx, c, hub, slog.Default(), time.Hour, time.Second)
	}()

	// A hundred inbound frames down the real dispatch path, plus the activity
	// each one credits.
	for i := 0; i < 100; i++ {
		if err := hub.handleClientMessage(ctx, c, ClientMessage{
			Type: ClientMessageTypeSubscribe, TargetType: TargetTypeChannel, TargetID: "chan-1",
		}); err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		tracker.RecordActivity(c.workspaceID, c.userID, c.id)
	}

	if got := sessions.callCount(); got != 0 {
		t.Fatalf("100 frames produced %d session lookups; the check is not per frame", got)
	}

	cancel()
	<-heartbeatDone
}

// ── the session identity is the server's, not the client's ───────────────────

// The only session a connection can be re-checked against is the one the
// handshake asserted. Nothing the browser sends names it: the inbound message
// contract has no session field, and a frame that invents one is rejected before
// it is dispatched anywhere.
func TestSessionRevalidation_TheClientCannotNameTheSession(t *testing.T) {
	hub := NewHub(&fakeAuthorizer{}, newTestLogger(), NopBus{}, "session-identity")
	defer hub.Shutdown()

	sessions := &fakeSessions{}
	cycles := make(chan struct{}, 4)
	sessions.notify(cycles)

	const handshakeSession = "sess-from-handshake"
	c := guardedClient(t, hub, "c-1", "user-1", handshakeSession, sessions, time.Millisecond)

	for _, frame := range []string{
		`{"type":"subscribe","target_type":"channel","target_id":"chan-1","sid":"attacker-session"}`,
		`{"type":"subscribe","target_type":"channel","target_id":"chan-1","session_id":"attacker-session"}`,
		`{"type":"subscribe","target_type":"channel","target_id":"chan-1","user_id":"user-2"}`,
		`{"type":"subscribe","target_type":"channel","target_id":"chan-1","workspace_id":"ws-2"}`,
	} {
		if _, err := decodeClientMessage([]byte(frame)); err == nil {
			t.Fatalf("the protocol accepted a frame carrying its own identity: %s", frame)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		startHeartbeat(ctx, c, hub, slog.Default(), time.Hour, time.Second)
	}()
	<-cycles
	cancel()
	<-heartbeatDone

	for _, asked := range sessions.sessionsAsked() {
		if asked != handshakeSession {
			t.Fatalf("the authority was asked about %q, not the session the handshake proved", asked)
		}
	}
	if len(sessions.sessionsAsked()) == 0 {
		t.Fatal("no revalidation happened at all")
	}
}

// A connection with no session authority wired keeps upgrade-time validation
// only, and must not spin a ticker for a question it cannot ask.
func TestSessionRevalidation_DisabledWithoutAnAuthority(t *testing.T) {
	hub := NewHub(&fakeAuthorizer{}, newTestLogger(), NopBus{}, "session-disabled")
	defer hub.Shutdown()

	c := guardedClient(t, hub, "c-1", "user-1", "sess-a", nil, time.Millisecond)
	if c.session.enabled() {
		t.Fatal("a connection with no session authority claims it can revalidate")
	}
	// Nor with an authority but no session to ask about.
	c.session = sessionGuard{validator: &fakeSessions{}, interval: time.Millisecond}
	if c.session.enabled() {
		t.Fatal("a connection with no session ID claims it can revalidate")
	}
}

// ── the session comes from the request context, or the upgrade is refused ────

// RequireActiveSession guarantees a validated session ID upstream. If one is not
// there while a session authority *is* configured, the middleware chain is not
// what it is supposed to be — and the result would be a connection nothing can
// ever revoke, which is precisely the hole this closes. So it is refused.
func TestServeWS_RefusesAnUpgradeItCouldNotRevalidate(t *testing.T) {
	hub := NewHub(&fakeAuthorizer{}, newTestLogger(), NopBus{}, "session-upgrade")
	defer hub.Shutdown()

	for _, tc := range []struct {
		name      string
		cfg       HandlerConfig
		wantOK    bool
		wantValue string
	}{
		{
			name: "no session authority: nothing to revalidate against",
			cfg: HandlerConfig{
				SessionIDFromContext: func(*http.Request) string { return "" },
			},
			wantOK: true,
		},
		{
			name: "authority configured and a session on the request",
			cfg: HandlerConfig{
				Sessions:             &fakeSessions{},
				SessionIDFromContext: func(*http.Request) string { return "sess-a" },
			},
			wantOK:    true,
			wantValue: "sess-a",
		},
		{
			name: "authority configured but no session on the request",
			cfg: HandlerConfig{
				Sessions:             &fakeSessions{},
				SessionIDFromContext: func(*http.Request) string { return "" },
			},
			wantOK: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newWSHandler(hub, slog.Default(), &fakeWorkspaceResolver{id: "ws-1"},
				userIDFromCtxFn("user-1"), tc.cfg)

			rec := httptest.NewRecorder()
			got, ok := h.requireSessionID(rec, httptest.NewRequest(http.MethodGet, "/ws", nil))

			if ok != tc.wantOK {
				t.Fatalf("accepted=%v, want %v", ok, tc.wantOK)
			}
			if got != tc.wantValue {
				t.Fatalf("session id %q, want %q", got, tc.wantValue)
			}
			if !tc.wantOK && rec.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d", rec.Code)
			}
		})
	}
}
