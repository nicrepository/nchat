package ws

import (
	"sync"
	"testing"
	"time"
)

// ── helpers ──────────────────────────────────────────────────────────────────

// fakeClock is a monotonic clock for presence tests. Advance moves time forward.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock(t time.Time) *fakeClock { return &fakeClock{now: t} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// newTestPresenceTracker creates a PresenceTracker with an injected clock and
// no background goroutine. Tests call checkAway() directly for determinism.
//
// The done channel is pre-closed; Stop() is safe to call but is a no-op since
// no background goroutine was started.
func newTestPresenceTracker(awayTimeout time.Duration, clk *fakeClock) *PresenceTracker {
	p := &PresenceTracker{
		awayTimeout: awayTimeout,
		now:         clk.Now,
		conns:       make(map[presenceKey]map[string]time.Time),
		status:      make(map[presenceKey]PresenceStatus),
		stop:        make(chan struct{}),
		done:        make(chan struct{}),
	}
	// Do not start the background run goroutine; tests drive checkAway directly.
	close(p.done)
	return p
}

// ── tests ─────────────────────────────────────────────────────────────────────

func TestPresence_Connect_SetsOnline(t *testing.T) {
	clk := newFakeClock(time.Now())
	p := newTestPresenceTracker(5*time.Minute, clk)

	p.Connect("ws-1", "user-1", "conn-1")

	if got := p.Status("ws-1", "user-1"); got != PresenceOnline {
		t.Fatalf("expected online after Connect, got %q", got)
	}
}

func TestPresence_Disconnect_LastConn_SetsOffline(t *testing.T) {
	clk := newFakeClock(time.Now())
	p := newTestPresenceTracker(5*time.Minute, clk)

	p.Connect("ws-1", "user-1", "conn-1")
	p.Disconnect("ws-1", "user-1", "conn-1")

	if got := p.Status("ws-1", "user-1"); got != PresenceOffline {
		t.Fatalf("expected offline after last disconnect, got %q", got)
	}
}

func TestPresence_UnknownUser_IsOffline(t *testing.T) {
	clk := newFakeClock(time.Now())
	p := newTestPresenceTracker(5*time.Minute, clk)

	if got := p.Status("ws-1", "user-ghost"); got != PresenceOffline {
		t.Fatalf("unknown user should be offline, got %q", got)
	}
}

func TestPresence_MultipleConns_OnlyOfflineAfterLast(t *testing.T) {
	clk := newFakeClock(time.Now())
	p := newTestPresenceTracker(5*time.Minute, clk)

	p.Connect("ws-1", "user-1", "conn-1")
	p.Connect("ws-1", "user-1", "conn-2")
	p.Connect("ws-1", "user-1", "conn-3")

	p.Disconnect("ws-1", "user-1", "conn-1")
	if got := p.Status("ws-1", "user-1"); got != PresenceOnline {
		t.Fatalf("should remain online with 2 connections, got %q", got)
	}

	p.Disconnect("ws-1", "user-1", "conn-2")
	if got := p.Status("ws-1", "user-1"); got != PresenceOnline {
		t.Fatalf("should remain online with 1 connection, got %q", got)
	}

	p.Disconnect("ws-1", "user-1", "conn-3")
	if got := p.Status("ws-1", "user-1"); got != PresenceOffline {
		t.Fatalf("expected offline after last disconnect, got %q", got)
	}
}

func TestPresence_InactivityTimeout_SetsAway(t *testing.T) {
	const awayTimeout = 5 * time.Minute
	clk := newFakeClock(time.Now())
	p := newTestPresenceTracker(awayTimeout, clk)

	p.Connect("ws-1", "user-1", "conn-1")

	// Advance time past the away timeout and trigger the check.
	clk.Advance(awayTimeout + time.Second)
	p.checkAway()

	if got := p.Status("ws-1", "user-1"); got != PresenceAway {
		t.Fatalf("expected away after inactivity timeout, got %q", got)
	}
}

func TestPresence_InactivityTimeout_NotTriggeredBeforeTimeout(t *testing.T) {
	const awayTimeout = 5 * time.Minute
	clk := newFakeClock(time.Now())
	p := newTestPresenceTracker(awayTimeout, clk)

	p.Connect("ws-1", "user-1", "conn-1")

	// Advance to just before the threshold.
	clk.Advance(awayTimeout - time.Second)
	p.checkAway()

	if got := p.Status("ws-1", "user-1"); got != PresenceOnline {
		t.Fatalf("should still be online before timeout, got %q", got)
	}
}

func TestPresence_ActivityAfterAway_SetsOnline(t *testing.T) {
	const awayTimeout = 5 * time.Minute
	clk := newFakeClock(time.Now())
	p := newTestPresenceTracker(awayTimeout, clk)

	p.Connect("ws-1", "user-1", "conn-1")

	// Go away.
	clk.Advance(awayTimeout + time.Second)
	p.checkAway()
	if got := p.Status("ws-1", "user-1"); got != PresenceAway {
		t.Fatalf("expected away, got %q", got)
	}

	// Activity restores online.
	p.RecordActivity("ws-1", "user-1", "conn-1")
	if got := p.Status("ws-1", "user-1"); got != PresenceOnline {
		t.Fatalf("expected online after activity, got %q", got)
	}
}

func TestPresence_ActivityAfterAway_ResetsTimer(t *testing.T) {
	const awayTimeout = 5 * time.Minute
	clk := newFakeClock(time.Now())
	p := newTestPresenceTracker(awayTimeout, clk)

	p.Connect("ws-1", "user-1", "conn-1")

	// Go away.
	clk.Advance(awayTimeout + time.Second)
	p.checkAway()

	// Activity at t=awayTimeout+1s.
	p.RecordActivity("ws-1", "user-1", "conn-1")

	// Advance a short time — still within new timeout window; should stay online.
	clk.Advance(awayTimeout / 2)
	p.checkAway()
	if got := p.Status("ws-1", "user-1"); got != PresenceOnline {
		t.Fatalf("should still be online within new timeout window, got %q", got)
	}

	// Advance past the timeout from the activity point — should go away again.
	clk.Advance(awayTimeout)
	p.checkAway()
	if got := p.Status("ws-1", "user-1"); got != PresenceAway {
		t.Fatalf("expected away after second timeout, got %q", got)
	}
}

func TestPresence_WorkspaceIsolation(t *testing.T) {
	clk := newFakeClock(time.Now())
	p := newTestPresenceTracker(5*time.Minute, clk)

	// Same user, different workspaces.
	p.Connect("ws-A", "user-1", "conn-1")

	if got := p.Status("ws-B", "user-1"); got != PresenceOffline {
		t.Fatalf("user in ws-A must be offline in ws-B, got %q", got)
	}
	if got := p.Status("ws-A", "user-1"); got != PresenceOnline {
		t.Fatalf("user should be online in ws-A, got %q", got)
	}
}

func TestPresence_WorkspaceIsolation_DisconnectDoesNotAffectOtherWorkspace(t *testing.T) {
	clk := newFakeClock(time.Now())
	p := newTestPresenceTracker(5*time.Minute, clk)

	p.Connect("ws-A", "user-1", "conn-1")
	p.Connect("ws-B", "user-1", "conn-2")

	// Disconnect from ws-A only.
	p.Disconnect("ws-A", "user-1", "conn-1")

	if got := p.Status("ws-A", "user-1"); got != PresenceOffline {
		t.Fatalf("should be offline in ws-A after disconnect, got %q", got)
	}
	if got := p.Status("ws-B", "user-1"); got != PresenceOnline {
		t.Fatalf("should still be online in ws-B, got %q", got)
	}
}

func TestPresence_WorkspaceIsolation_AwayInOneWorkspace(t *testing.T) {
	const awayTimeout = 5 * time.Minute
	clk := newFakeClock(time.Now())
	p := newTestPresenceTracker(awayTimeout, clk)

	p.Connect("ws-A", "user-1", "conn-1")
	p.Connect("ws-B", "user-1", "conn-2")

	// ws-B has recent activity; ws-A goes idle.
	clk.Advance(awayTimeout + time.Second)
	// Refresh ws-B activity to prevent it from going away.
	p.RecordActivity("ws-B", "user-1", "conn-2")

	// Advance slightly more to ensure ws-A is past the timeout.
	clk.Advance(time.Second)
	p.checkAway()

	if got := p.Status("ws-A", "user-1"); got != PresenceAway {
		t.Fatalf("ws-A should be away, got %q", got)
	}
	if got := p.Status("ws-B", "user-1"); got != PresenceOnline {
		t.Fatalf("ws-B should still be online, got %q", got)
	}
}

func TestPresence_Disconnect_UnknownConn_IsNoop(t *testing.T) {
	clk := newFakeClock(time.Now())
	p := newTestPresenceTracker(5*time.Minute, clk)

	// Must not panic.
	p.Disconnect("ws-1", "user-1", "conn-unknown")

	if got := p.Status("ws-1", "user-1"); got != PresenceOffline {
		t.Fatalf("expected offline after noop disconnect, got %q", got)
	}
}

func TestPresence_RecordActivity_UnknownUser_IsNoop(t *testing.T) {
	clk := newFakeClock(time.Now())
	p := newTestPresenceTracker(5*time.Minute, clk)

	// Must not panic.
	p.RecordActivity("ws-1", "user-ghost", "conn-1")
}

func TestPresence_MultipleConns_AwayClearedWhenOneActive(t *testing.T) {
	const awayTimeout = 5 * time.Minute
	clk := newFakeClock(time.Now())
	p := newTestPresenceTracker(awayTimeout, clk)

	// Two connections; only one stays active.
	p.Connect("ws-1", "user-1", "conn-1")
	p.Connect("ws-1", "user-1", "conn-2")

	// Advance past timeout — both connections are now stale.
	clk.Advance(awayTimeout + time.Second)
	p.checkAway()
	if got := p.Status("ws-1", "user-1"); got != PresenceAway {
		t.Fatalf("expected away when all conns idle, got %q", got)
	}

	// conn-2 has activity — should restore online.
	p.RecordActivity("ws-1", "user-1", "conn-2")
	if got := p.Status("ws-1", "user-1"); got != PresenceOnline {
		t.Fatalf("expected online after activity on one conn, got %q", got)
	}
}

// ── race detector test ────────────────────────────────────────────────────────

func TestPresence_ConcurrentOps_NoRace(t *testing.T) {
	p := NewPresenceTracker(100 * time.Millisecond)
	defer p.Stop()

	const goroutines = 20
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func(n int) {
			defer wg.Done()
			ws := "ws-1"
			user := "user-1"
			conn := "conn-1"

			p.Connect(ws, user, conn)
			p.RecordActivity(ws, user, conn)
			_ = p.Status(ws, user)
			p.Disconnect(ws, user, conn)
		}(i)
	}

	wg.Wait()
}

func TestPresence_ConcurrentMultiUserMultiWorkspace_NoRace(t *testing.T) {
	p := NewPresenceTracker(50 * time.Millisecond)
	defer p.Stop()

	workspaces := []string{"ws-1", "ws-2", "ws-3"}
	users := []string{"user-a", "user-b", "user-c"}

	var wg sync.WaitGroup
	for _, ws := range workspaces {
		for _, user := range users {
			wg.Add(1)
			go func(ws, user string) {
				defer wg.Done()
				connID := ws + ":" + user + ":conn"
				p.Connect(ws, user, connID)
				p.RecordActivity(ws, user, connID)
				_ = p.Status(ws, user)
				p.Disconnect(ws, user, connID)
			}(ws, user)
		}
	}

	wg.Wait()
}
