package ws

import (
	"sort"
	"sync"
	"time"
)

// PresenceStatus represents the online/away/offline state of a user per workspace.
type PresenceStatus string

const (
	// PresenceOnline indicates the user has at least one active connection with
	// recent activity within the away timeout window.
	PresenceOnline PresenceStatus = "online"

	// PresenceAway indicates the user has at least one active connection but
	// all connections have been inactive longer than the away timeout.
	PresenceAway PresenceStatus = "away"

	// PresenceOffline indicates the user has no active WebSocket connections
	// in this workspace.
	PresenceOffline PresenceStatus = "offline"
)

// presenceKey uniquely identifies a user within a workspace for presence tracking.
// WorkspaceID is required to prevent cross-workspace presence leakage.
type presenceKey struct {
	workspaceID string
	userID      string
}

// PresenceTracker tracks online/away/offline state per (workspaceID, userID).
//
// Concurrency safety:
//   - All exported methods are safe for concurrent use.
//   - mu is never held during I/O or outbound operations.
//   - mu is independent of the Hub's mu; callers must not hold Hub.mu while
//     calling PresenceTracker methods to avoid lock-order concerns.
//
// Multi-device: a user is offline only when their last connection in the
// workspace disconnects. Away requires all connections to be inactive.
//
// Workspace isolation: presence state is scoped per workspaceID; users in
// different workspaces are tracked independently and never share state.
type PresenceTracker struct {
	awayTimeout time.Duration
	now         func() time.Time // injectable clock for tests

	mu     sync.RWMutex
	conns  map[presenceKey]map[string]time.Time // key → connID → lastActivity
	status map[presenceKey]PresenceStatus

	stopOnce sync.Once
	stop     chan struct{}
	done     chan struct{}
}

// NewPresenceTracker creates and starts a PresenceTracker.
//
// awayTimeout is the duration of connection inactivity after which a user
// transitions from online to away. The background ticker runs at awayTimeout/4,
// with a minimum interval of 1 second regardless of the configured timeout.
//
// Call Stop to shut down the background goroutine.
func NewPresenceTracker(awayTimeout time.Duration) *PresenceTracker {
	return newPresenceTrackerWithClock(awayTimeout, time.Now)
}

func newPresenceTrackerWithClock(awayTimeout time.Duration, now func() time.Time) *PresenceTracker {
	p := &PresenceTracker{
		awayTimeout: awayTimeout,
		now:         now,
		conns:       make(map[presenceKey]map[string]time.Time),
		status:      make(map[presenceKey]PresenceStatus),
		stop:        make(chan struct{}),
		done:        make(chan struct{}),
	}
	go p.run()
	return p
}

// Stop shuts down the background away-check goroutine and blocks until it exits.
// Safe to call multiple times — subsequent calls are no-ops.
func (p *PresenceTracker) Stop() {
	p.stopOnce.Do(func() { close(p.stop) })
	<-p.done
}

// Connect records a new WebSocket connection for (workspaceID, userID) and
// sets their presence to online. Safe to call from any goroutine.
//
// connID must be a server-generated opaque identifier (e.g., Client.id).
// workspaceID and userID must be server-asserted from the auth context; they
// must never originate from client-provided input.
func (p *PresenceTracker) Connect(workspaceID, userID, connID string) {
	key := presenceKey{workspaceID: workspaceID, userID: userID}
	now := p.now()

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.conns[key] == nil {
		p.conns[key] = make(map[string]time.Time)
	}
	p.conns[key][connID] = now
	p.status[key] = PresenceOnline
}

// Disconnect removes a connection for (workspaceID, userID).
// If this was the last active connection, presence becomes offline.
// Safe to call from any goroutine.
func (p *PresenceTracker) Disconnect(workspaceID, userID, connID string) {
	key := presenceKey{workspaceID: workspaceID, userID: userID}

	p.mu.Lock()
	defer p.mu.Unlock()

	conns, ok := p.conns[key]
	if !ok {
		return
	}
	delete(conns, connID)
	if len(conns) == 0 {
		delete(p.conns, key)
		// Delete the status entry rather than setting PresenceOffline, so the
		// status map doesn't accumulate stale entries for users that have
		// disconnected. Status() returns PresenceOffline for absent keys.
		delete(p.status, key)
	}
}

// RecordActivity records user activity on a specific connection.
// Resets the inactivity timer for connID and, if the user was away,
// restores their presence to online.
// Safe to call from any goroutine.
func (p *PresenceTracker) RecordActivity(workspaceID, userID, connID string) {
	key := presenceKey{workspaceID: workspaceID, userID: userID}
	now := p.now()

	p.mu.Lock()
	defer p.mu.Unlock()

	conns, ok := p.conns[key]
	if !ok {
		return
	}
	if _, hasConn := conns[connID]; !hasConn {
		return
	}
	conns[connID] = now
	if p.status[key] == PresenceAway {
		p.status[key] = PresenceOnline
	}
}

// Status returns the current presence status for (workspaceID, userID).
// Returns PresenceOffline for unknown users.
// Safe to call from any goroutine.
func (p *PresenceTracker) Status(workspaceID, userID string) PresenceStatus {
	key := presenceKey{workspaceID: workspaceID, userID: userID}

	p.mu.RLock()
	defer p.mu.RUnlock()

	if s, ok := p.status[key]; ok {
		return s
	}
	return PresenceOffline
}

// OnlineUserIDs returns every user in workspaceID whose presence is currently
// PresenceOnline, sorted by user ID.
//
// Only PresenceOnline qualifies. PresenceAway is a distinct state by definition
// — the user still holds a connection but has been inactive past the away
// timeout — so it is not folded in here, and PresenceOffline users have no
// entry at all (Disconnect deletes the key rather than storing offline).
//
// It answers the whole question in one pass under a single read lock, so a
// caller never has to ask about members one at a time. The result is a snapshot:
// presence can change the instant the lock is released, which is inherent to
// presence and not something a longer lock would fix.
//
// workspaceID must be server-asserted; scoping by it is what keeps one
// workspace's connected users invisible to another.
func (p *PresenceTracker) OnlineUserIDs(workspaceID string) []string {
	p.mu.RLock()
	userIDs := make([]string, 0, len(p.status))
	for key, status := range p.status {
		if key.workspaceID == workspaceID && status == PresenceOnline {
			userIDs = append(userIDs, key.userID)
		}
	}
	p.mu.RUnlock()

	// Map iteration order is randomised, so the slice is sorted before it
	// leaves: callers that log, compare or paginate it must see a stable order.
	sort.Strings(userIDs)
	return userIDs
}

// run is the background goroutine that drives away transitions.
func (p *PresenceTracker) run() {
	defer close(p.done)

	interval := p.awayTimeout / 4
	// Floor at 1 second so very short test timeouts don't produce a
	// near-zero ticker interval, which would thrash the scheduler.
	if interval < time.Second {
		interval = time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			p.checkAway()
		case <-p.stop:
			return
		}
	}
}

// checkAway transitions online users to away when all their connections have
// been inactive for longer than awayTimeout. Called exclusively from the run
// goroutine (and directly in tests via the exported checkAway path).
//
// Invariant: only iterates p.conns, so users with no active connections (already
// offline) are never touched. The status map reflects the authoritative state;
// a missing key is equivalent to PresenceOffline (see Status).
func (p *PresenceTracker) checkAway() {
	now := p.now()

	p.mu.Lock()
	defer p.mu.Unlock()

	for key, connMap := range p.conns {
		if p.status[key] != PresenceOnline {
			continue
		}
		allInactive := true
		for _, lastAct := range connMap {
			if now.Sub(lastAct) <= p.awayTimeout {
				allInactive = false
				break
			}
		}
		if allInactive {
			p.status[key] = PresenceAway
		}
	}
}
