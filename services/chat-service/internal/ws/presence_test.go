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
		changedAt:   make(map[presenceKey]time.Time),
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

// ── OnlineUserIDs (issue #435) ───────────────────────────────────────────────

func TestPresence_OnlineUserIDs_ReturnsOnlyOnlineUsersSorted(t *testing.T) {
	clk := newFakeClock(time.Now())
	p := newTestPresenceTracker(5*time.Minute, clk)

	// Deliberately connected out of order: the result must be sorted, not
	// whatever order the map happens to iterate in.
	for _, userID := range []string{"user-c", "user-a", "user-b"} {
		p.Connect("ws-1", userID, "conn-"+userID)
	}

	got := p.OnlineUserIDs("ws-1")
	if len(got) != 3 || got[0] != "user-a" || got[1] != "user-b" || got[2] != "user-c" {
		t.Fatalf("expected the online users in sorted order, got %v", got)
	}
}

func TestPresence_OnlineUserIDs_ExcludesAwayAndOffline(t *testing.T) {
	const awayTimeout = 5 * time.Minute
	clk := newFakeClock(time.Now())
	p := newTestPresenceTracker(awayTimeout, clk)

	p.Connect("ws-1", "user-away", "conn-away")
	// Push the first user past the away timeout, then connect a second one so
	// only that second user is still online.
	clk.Advance(awayTimeout + time.Second)
	p.checkAway()
	p.Connect("ws-1", "user-online", "conn-online")
	// A third user connects and leaves: offline users have no entry at all.
	p.Connect("ws-1", "user-gone", "conn-gone")
	p.Disconnect("ws-1", "user-gone", "conn-gone")

	if got := p.Status("ws-1", "user-away"); got != PresenceAway {
		t.Fatalf("precondition failed: expected away, got %q", got)
	}

	got := p.OnlineUserIDs("ws-1")
	if len(got) != 1 || got[0] != "user-online" {
		t.Fatalf("away and offline users must not be reported as online, got %v", got)
	}
}

func TestPresence_OnlineUserIDs_IsWorkspaceScoped(t *testing.T) {
	clk := newFakeClock(time.Now())
	p := newTestPresenceTracker(5*time.Minute, clk)

	p.Connect("ws-1", "user-1", "conn-1")
	p.Connect("ws-2", "user-2", "conn-2")

	if got := p.OnlineUserIDs("ws-1"); len(got) != 1 || got[0] != "user-1" {
		t.Fatalf("workspace ws-1 leaked another workspace's users: %v", got)
	}
	if got := p.OnlineUserIDs("ws-2"); len(got) != 1 || got[0] != "user-2" {
		t.Fatalf("workspace ws-2 leaked another workspace's users: %v", got)
	}
	if got := p.OnlineUserIDs("ws-unknown"); len(got) != 0 {
		t.Fatalf("an unknown workspace must report nobody online, got %v", got)
	}
}

func TestPresence_OnlineUserIDs_ReflectsActivityRestoringOnline(t *testing.T) {
	const awayTimeout = 5 * time.Minute
	clk := newFakeClock(time.Now())
	p := newTestPresenceTracker(awayTimeout, clk)

	p.Connect("ws-1", "user-1", "conn-1")
	clk.Advance(awayTimeout + time.Second)
	p.checkAway()
	if got := p.OnlineUserIDs("ws-1"); len(got) != 0 {
		t.Fatalf("an away user must not be listed as online, got %v", got)
	}

	p.RecordActivity("ws-1", "user-1", "conn-1")

	if got := p.OnlineUserIDs("ws-1"); len(got) != 1 || got[0] != "user-1" {
		t.Fatalf("expected the user back online after activity, got %v", got)
	}
}

// ── change reporting (RF-58) ─────────────────────────────────────────────────
//
// The Changed flag is what decides whether an event goes on the wire, so the
// cases where it must be false matter as much as the ones where it must be true.

func TestPresence_Connect_ReportsChangeOnlyForTheFirstSession(t *testing.T) {
	clk := newFakeClock(time.Now())
	p := newTestPresenceTracker(5*time.Minute, clk)

	first := p.Connect("ws-1", "user-1", "conn-1")
	if !first.Changed || first.Status != PresenceOnline {
		t.Fatalf("first connection: expected a change to online, got %+v", first)
	}
	if !first.At.Equal(clk.Now()) {
		t.Fatalf("first connection: expected the tracker clock, got %v", first.At)
	}

	second := p.Connect("ws-1", "user-1", "conn-2")
	if second.Changed {
		t.Fatalf("a second session must not re-assert presence, got %+v", second)
	}
	if second.Status != PresenceOnline {
		t.Fatalf("second connection: expected online, got %q", second.Status)
	}
}

func TestPresence_Disconnect_ReportsChangeOnlyForTheLastSession(t *testing.T) {
	clk := newFakeClock(time.Now())
	p := newTestPresenceTracker(5*time.Minute, clk)

	p.Connect("ws-1", "user-1", "conn-1")
	p.Connect("ws-1", "user-1", "conn-2")

	if change := p.Disconnect("ws-1", "user-1", "conn-1"); change.Changed {
		t.Fatalf("closing one of two sessions must change nothing, got %+v", change)
	}
	last := p.Disconnect("ws-1", "user-1", "conn-2")
	if !last.Changed || last.Status != PresenceOffline {
		t.Fatalf("last session: expected a change to offline, got %+v", last)
	}

	// An unknown user is already offline; there is nothing to report.
	if change := p.Disconnect("ws-1", "user-unknown", "conn-x"); change.Changed {
		t.Fatalf("an unknown user must report no change, got %+v", change)
	}
}

func TestPresence_RecordActivity_ReportsOnlyTheAwayToOnlineTransition(t *testing.T) {
	const awayTimeout = 5 * time.Minute
	clk := newFakeClock(time.Now())
	p := newTestPresenceTracker(awayTimeout, clk)

	p.Connect("ws-1", "user-1", "conn-1")
	if change := p.RecordActivity("ws-1", "user-1", "conn-1"); change.Changed {
		t.Fatalf("activity on an online user must change nothing, got %+v", change)
	}

	clk.Advance(awayTimeout + time.Second)
	p.checkAway()

	back := p.RecordActivity("ws-1", "user-1", "conn-1")
	if !back.Changed || back.Status != PresenceOnline {
		t.Fatalf("expected the away → online transition, got %+v", back)
	}
	if !back.At.Equal(clk.Now()) {
		t.Fatalf("expected the transition stamped with the tracker clock, got %v", back.At)
	}

	// A connection the tracker does not hold cannot refresh anything.
	if change := p.RecordActivity("ws-1", "user-1", "conn-unknown"); change.Changed {
		t.Fatalf("an unknown connection must change nothing, got %+v", change)
	}
}

func TestPresence_StatusAt_ReportsWhenTheStatusChanged(t *testing.T) {
	const awayTimeout = 5 * time.Minute
	start := time.Now()
	clk := newFakeClock(start)
	p := newTestPresenceTracker(awayTimeout, clk)

	if status, at := p.StatusAt("ws-1", "user-1"); status != PresenceOffline || !at.IsZero() {
		t.Fatalf("an unknown user: expected offline at the zero time, got %q/%v", status, at)
	}

	p.Connect("ws-1", "user-1", "conn-1")
	if _, at := p.StatusAt("ws-1", "user-1"); !at.Equal(start) {
		t.Fatalf("expected the connect instant, got %v", at)
	}

	// Activity that changes nothing must not move the stamp: it is the instant
	// the *status* took its value, not the last time anything happened.
	clk.Advance(time.Minute)
	p.RecordActivity("ws-1", "user-1", "conn-1")
	if _, at := p.StatusAt("ws-1", "user-1"); !at.Equal(start) {
		t.Fatalf("a no-op activity moved the status stamp to %v", at)
	}

	clk.Advance(awayTimeout + time.Second)
	awayAt := clk.Now()
	p.checkAway()
	if status, at := p.StatusAt("ws-1", "user-1"); status != PresenceAway || !at.Equal(awayAt) {
		t.Fatalf("expected away stamped at the transition, got %q/%v", status, at)
	}
}

func TestPresence_Observer_ReportsAwayTransitions(t *testing.T) {
	const awayTimeout = 5 * time.Minute
	clk := newFakeClock(time.Now())
	p := newTestPresenceTracker(awayTimeout, clk)

	type report struct {
		workspaceID string
		userID      string
		status      PresenceStatus
		at          time.Time
	}
	var reports []report
	p.SetObserver(func(workspaceID, userID string, status PresenceStatus, at time.Time) {
		reports = append(reports, report{workspaceID, userID, status, at})
	})

	p.Connect("ws-1", "user-1", "conn-1")
	if len(reports) != 0 {
		t.Fatalf("Connect must report through its return value, not the observer: %+v", reports)
	}

	clk.Advance(awayTimeout + time.Second)
	transitionAt := clk.Now()
	p.checkAway()

	if len(reports) != 1 {
		t.Fatalf("expected exactly one away report, got %+v", reports)
	}
	got := reports[0]
	if got.workspaceID != "ws-1" || got.userID != "user-1" || got.status != PresenceAway {
		t.Fatalf("unexpected away report: %+v", got)
	}
	if !got.at.Equal(transitionAt) {
		t.Fatalf("expected the transition instant, got %v", got.at)
	}

	// A second sweep must not repeat a transition that already happened.
	clk.Advance(awayTimeout)
	p.checkAway()
	if len(reports) != 1 {
		t.Fatalf("away was reported twice: %+v", reports)
	}

	// Removing the observer stops the reports without stopping the tracker.
	p.SetObserver(nil)
	p.RecordActivity("ws-1", "user-1", "conn-1")
	clk.Advance(awayTimeout + time.Second)
	p.checkAway()
	if len(reports) != 1 {
		t.Fatalf("a removed observer was still called: %+v", reports)
	}
	if got := p.Status("ws-1", "user-1"); got != PresenceAway {
		t.Fatalf("the tracker stopped working without an observer: %q", got)
	}
}

// ── disconnect re-reads what is left (RF-58) ─────────────────────────────────
//
// The connections that remain are the whole answer, and Disconnect is the moment
// that answer changes. Leaving it to the periodic check meant a user could sit
// on `online` for up to a quarter of the away timeout with nothing but idle tabs
// behind it — a state the server was asserting and no longer had a reason to.

func TestPresence_Disconnect_AllRemainingIdleGoesAwayAtOnce(t *testing.T) {
	const awayTimeout = 5 * time.Minute
	clk := newFakeClock(time.Now())
	p := newTestPresenceTracker(awayTimeout, clk)

	// One tab, left alone long enough for the periodic check to call it away.
	p.Connect("ws-1", "user-1", "conn-idle")
	clk.Advance(awayTimeout + time.Second)
	p.checkAway()

	// A second tab opens: the user is online again, on the strength of that tab.
	if change := p.Connect("ws-1", "user-1", "conn-fresh"); !change.Changed || change.Status != PresenceOnline {
		t.Fatalf("a new connection must put the user online, got %+v", change)
	}

	// And closes. Nothing recent is left, so nothing holds the user online.
	change := p.Disconnect("ws-1", "user-1", "conn-fresh")
	if change.Status != PresenceAway || !change.Changed {
		t.Fatalf("expected an immediate change to away, got %+v", change)
	}
	if got := p.Status("ws-1", "user-1"); got != PresenceAway {
		t.Fatalf("expected away without waiting for the ticker, got %q", got)
	}
	// The periodic check has nothing left to do — it agrees, and reports nothing.
	p.checkAway()
	if got := p.Status("ws-1", "user-1"); got != PresenceAway {
		t.Fatalf("the ticker disagreed with the disconnect: %q", got)
	}
}

func TestPresence_Disconnect_EveryRemainingConnectionIdleGoesAway(t *testing.T) {
	const awayTimeout = 5 * time.Minute
	clk := newFakeClock(time.Now())
	p := newTestPresenceTracker(awayTimeout, clk)

	p.Connect("ws-1", "user-1", "conn-a")
	p.Connect("ws-1", "user-1", "conn-b")
	clk.Advance(awayTimeout + time.Second)
	p.Connect("ws-1", "user-1", "conn-c")

	if change := p.Disconnect("ws-1", "user-1", "conn-c"); change.Status != PresenceAway || !change.Changed {
		t.Fatalf("two idle connections must leave the user away, got %+v", change)
	}
}

func TestPresence_Disconnect_OneRecentConnectionKeepsTheUserOnline(t *testing.T) {
	const awayTimeout = 5 * time.Minute
	clk := newFakeClock(time.Now())
	p := newTestPresenceTracker(awayTimeout, clk)

	p.Connect("ws-1", "user-1", "conn-idle")
	clk.Advance(awayTimeout + time.Second)
	p.Connect("ws-1", "user-1", "conn-recent")
	p.Connect("ws-1", "user-1", "conn-extra")

	// The extra tab goes; the recent one is still recent, so nothing moved.
	if change := p.Disconnect("ws-1", "user-1", "conn-extra"); change.Changed || change.Status != PresenceOnline {
		t.Fatalf("a recent connection must hold the user online, got %+v", change)
	}
	// Even with an idle connection alongside it.
	if got := p.Status("ws-1", "user-1"); got != PresenceOnline {
		t.Fatalf("expected online, got %q", got)
	}
}

func TestPresence_Disconnect_LastConnectionIsOfflineWhateverItsAge(t *testing.T) {
	const awayTimeout = 5 * time.Minute
	clk := newFakeClock(time.Now())
	p := newTestPresenceTracker(awayTimeout, clk)

	p.Connect("ws-1", "user-1", "conn-1")
	clk.Advance(awayTimeout + time.Second)
	p.checkAway() // the user is away before the disconnect, not online

	change := p.Disconnect("ws-1", "user-1", "conn-1")
	if change.Status != PresenceOffline || !change.Changed {
		t.Fatalf("the last connection must report offline, got %+v", change)
	}
	if got := p.Status("ws-1", "user-1"); got != PresenceOffline {
		t.Fatalf("expected offline, got %q", got)
	}
}

// The boundary itself: a connection exactly at the timeout still counts as
// active, which is the rule checkAway has always applied. Disconnect must not
// draw it one nanosecond differently.
func TestPresence_Disconnect_UsesTheSameBoundaryAsTheAwayCheck(t *testing.T) {
	const awayTimeout = 5 * time.Minute

	for _, tc := range []struct {
		name string
		age  time.Duration
		want PresenceStatus
	}{
		{name: "exactly at the timeout is still active", age: awayTimeout, want: PresenceOnline},
		{name: "one nanosecond past it is not", age: awayTimeout + time.Nanosecond, want: PresenceAway},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clk := newFakeClock(time.Now())
			p := newTestPresenceTracker(awayTimeout, clk)

			p.Connect("ws-1", "user-1", "conn-old")
			clk.Advance(tc.age)
			p.Connect("ws-1", "user-1", "conn-new")

			if change := p.Disconnect("ws-1", "user-1", "conn-new"); change.Status != tc.want {
				t.Fatalf("disconnect says %q, want %q", change.Status, tc.want)
			}

			// The periodic check, given the same connection, must agree.
			clk2 := newFakeClock(time.Now())
			q := newTestPresenceTracker(awayTimeout, clk2)
			q.Connect("ws-1", "user-1", "conn-old")
			clk2.Advance(tc.age)
			q.checkAway()
			if got := q.Status("ws-1", "user-1"); got != tc.want {
				t.Fatalf("the away check says %q, want %q", got, tc.want)
			}
		})
	}
}
