package ws

import (
	"sync"
	"testing"
	"time"
)

// Disconnect grace (issue #798): a dropped last connection is not yet a user
// who left. These drive the tracker with a fake clock and call expireGraces
// directly, so nothing here waits for the grace to pass.

const testGrace = 45 * time.Second

func TestPresenceGrace_LastDisconnectLingersInsteadOfGoingOffline(t *testing.T) {
	clk := newFakeClock(time.Now())
	p := newTestPresenceTrackerWithGrace(5*time.Minute, testGrace, clk)

	p.Connect("ws-1", "user-1", "c-1")
	change := p.Disconnect("ws-1", "user-1", "c-1")

	if change.Changed || !change.Lingering || change.Status != PresenceOnline {
		t.Fatalf("Disconnect = %+v, want a lingering online with no change", change)
	}
	if got := p.Status("ws-1", "user-1"); got != PresenceOnline {
		t.Fatalf("status during the grace = %q, want online", got)
	}
}

func TestPresenceGrace_ReconnectInsideTheGraceIsSeamless(t *testing.T) {
	clk := newFakeClock(time.Now())
	p := newTestPresenceTrackerWithGrace(5*time.Minute, testGrace, clk)
	var observed []PresenceStatus
	p.SetObserver(func(_, _ string, status PresenceStatus, _ time.Time, _ uint64) { observed = append(observed, status) })

	p.Connect("ws-1", "user-1", "c-1")
	p.Disconnect("ws-1", "user-1", "c-1")
	clk.Advance(10 * time.Second)
	if change := p.Connect("ws-1", "user-1", "c-2"); change.Changed {
		t.Fatalf("a reconnect inside the grace reported a change: %+v", change)
	}
	clk.Advance(testGrace)
	p.expireGraces()

	if len(observed) != 0 {
		t.Fatalf("a reconnected user produced transitions %v", observed)
	}
	if got := p.Status("ws-1", "user-1"); got != PresenceOnline {
		t.Fatalf("status = %q, want online", got)
	}
}

func TestPresenceGrace_ExpiredGraceIsOffline(t *testing.T) {
	clk := newFakeClock(time.Now())
	p := newTestPresenceTrackerWithGrace(5*time.Minute, testGrace, clk)
	var mu sync.Mutex
	var observed []PresenceStatus
	p.SetObserver(func(_, _ string, status PresenceStatus, _ time.Time, _ uint64) {
		mu.Lock()
		defer mu.Unlock()
		observed = append(observed, status)
	})

	p.Connect("ws-1", "user-1", "c-1")
	p.Disconnect("ws-1", "user-1", "c-1")

	clk.Advance(testGrace - time.Second)
	p.expireGraces()
	if len(observed) != 0 {
		t.Fatalf("offline before the grace ended: %v", observed)
	}

	clk.Advance(time.Second)
	p.expireGraces()
	if len(observed) != 1 || observed[0] != PresenceOffline {
		t.Fatalf("observed = %v, want one offline", observed)
	}
	if got := p.Status("ws-1", "user-1"); got != PresenceOffline {
		t.Fatalf("status after the grace = %q, want offline", got)
	}
	// Nothing is left to expire twice.
	p.expireGraces()
	if len(observed) != 1 {
		t.Fatalf("expired twice: %v", observed)
	}
}

func TestPresenceGrace_AnotherSessionMeansNoGraceAtAll(t *testing.T) {
	clk := newFakeClock(time.Now())
	p := newTestPresenceTrackerWithGrace(5*time.Minute, testGrace, clk)

	p.Connect("ws-1", "user-1", "c-1")
	p.Connect("ws-1", "user-1", "c-2")
	change := p.Disconnect("ws-1", "user-1", "c-1")

	if change.Lingering || change.Changed || change.Status != PresenceOnline {
		t.Fatalf("Disconnect = %+v, want the other session to hold the user online", change)
	}
}

func TestPresenceGrace_StaleDisconnectDuringTheGraceChangesNothing(t *testing.T) {
	clk := newFakeClock(time.Now())
	p := newTestPresenceTrackerWithGrace(5*time.Minute, testGrace, clk)

	p.Connect("ws-1", "user-1", "c-1")
	p.Disconnect("ws-1", "user-1", "c-1")
	change := p.Disconnect("ws-1", "user-1", "c-old")

	if change.Changed || change.Lingering || change.Status != PresenceOnline {
		t.Fatalf("a socket from before the drop moved the user: %+v", change)
	}
}

func TestPresenceGrace_AwayUserLingersAway(t *testing.T) {
	clk := newFakeClock(time.Now())
	p := newTestPresenceTrackerWithGrace(5*time.Minute, testGrace, clk)

	p.Connect("ws-1", "user-1", "c-1")
	clk.Advance(6 * time.Minute)
	p.checkAway()
	change := p.Disconnect("ws-1", "user-1", "c-1")

	if !change.Lingering || change.Status != PresenceAway {
		t.Fatalf("Disconnect = %+v, want a lingering away", change)
	}
	// A lingering user has no connection to go idle on.
	clk.Advance(10 * time.Second)
	p.checkAway()
	if got := p.Status("ws-1", "user-1"); got != PresenceAway {
		t.Fatalf("status = %q, want away", got)
	}
}

func TestPresenceGrace_SweepIntervalNoticesAnExpiredGraceInTime(t *testing.T) {
	cases := []struct {
		away, grace, want time.Duration
	}{
		{5 * time.Minute, 45 * time.Second, 15 * time.Second},
		{5 * time.Minute, 0, 75 * time.Second},
		{2 * time.Second, 0, time.Second},
		{5 * time.Minute, time.Second, time.Second},
	}
	for _, tc := range cases {
		p := &PresenceTracker{awayTimeout: tc.away, grace: tc.grace}
		if got := p.sweepInterval(); got != tc.want {
			t.Fatalf("sweepInterval(away=%v, grace=%v) = %v, want %v", tc.away, tc.grace, got, tc.want)
		}
	}
}

func TestPresenceGrace_ConstructorStartsAndStops(t *testing.T) {
	p := NewPresenceTrackerWithGrace(time.Minute, testGrace)
	if p.DisconnectGrace() != testGrace {
		t.Fatalf("DisconnectGrace = %v, want %v", p.DisconnectGrace(), testGrace)
	}
	p.Stop()
	p.Stop()
}
