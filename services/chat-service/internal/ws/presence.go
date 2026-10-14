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

// PresenceChange reports the outcome of one presence mutation (RF-58).
//
// Status and At are always the tracker's current answer for the user, whether
// or not this call changed anything. Changed is what a caller broadcasts on: a
// second tab connecting, or the tenth message on an already-online connection,
// moves nothing and must not put an event on the wire.
//
// At is server time, taken from the tracker's clock. It is the ordering key
// clients use to discard a stale update, so it never comes from a browser.
type PresenceChange struct {
	Status  PresenceStatus
	At      time.Time
	Changed bool
	// Lingering reports that the user's last local connection just closed and
	// the tracker is holding their state through the disconnect grace instead of
	// declaring them offline (issue #798). The caller still owns the audience the
	// eventual offline will need.
	Lingering bool
	// Generation identifies the lifecycle the change belongs to: the local
	// presence of one person from its first connection to the offline its last
	// disconnect ends in (issue #798). A reconnection inside the grace is the
	// same lifecycle; one after it is a new one. It fences the person's shared
	// reach (UserPresenceStore), so a departure decided for one lifecycle can
	// never retract the next. Zero for a person this tracker never held.
	Generation uint64
}

// PresenceObserver is notified of transitions the tracker makes *on its own* —
// online → away, driven by the background inactivity check, and → offline when
// a disconnect grace runs out without a reconnect (issue #798).
//
// Transitions caused by Connect, Disconnect and RecordActivity are deliberately
// not reported here: they are returned to the caller that caused them, which is
// the only place that still knows the connection involved. A disconnect is the
// reason — addressing the resulting offline event needs the departing client's
// subscriptions, and those are already gone by the time any asynchronous
// observer could look them up.
//
// It is called without p.mu held and must not call back into the tracker.
// generation is the lifecycle the transition belongs to — for an offline, the
// one it ended.
type PresenceObserver func(workspaceID, userID string, status PresenceStatus, at time.Time, generation uint64)

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
	// grace is how long a user whose last connection closed keeps their state
	// before becoming offline (issue #798). Zero disables it: the last
	// disconnect is offline at once.
	grace time.Duration
	now   func() time.Time // injectable clock for tests

	mu     sync.RWMutex
	conns  map[presenceKey]map[string]time.Time // key → connID → lastActivity
	status map[presenceKey]PresenceStatus
	// changedAt is when status[key] last took its current value. It is the
	// ordering key that travels with every presence event, so it is written
	// under the same lock as the status it describes and never derived later.
	changedAt map[presenceKey]time.Time
	// lingering holds, for users with no connection left, when their grace
	// ends. A key is never in both conns and lingering.
	lingering map[presenceKey]time.Time
	// generations holds the lifecycle of every person with a connection or a
	// grace; generationSeq issues them, monotonic for the life of the process.
	generations   map[presenceKey]uint64
	generationSeq uint64
	observer      PresenceObserver

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
	return newPresenceTrackerWithClock(awayTimeout, 0, time.Now)
}

// NewPresenceTrackerWithGrace is NewPresenceTracker with a disconnect grace
// (issue #798): a user whose last connection closes keeps their state for grace
// and becomes offline only if no connection comes back within it.
func NewPresenceTrackerWithGrace(awayTimeout, grace time.Duration) *PresenceTracker {
	return newPresenceTrackerWithClock(awayTimeout, grace, time.Now)
}

func newPresenceTrackerWithClock(awayTimeout, grace time.Duration, now func() time.Time) *PresenceTracker {
	p := &PresenceTracker{
		awayTimeout: awayTimeout,
		grace:       grace,
		now:         now,
		conns:       make(map[presenceKey]map[string]time.Time),
		status:      make(map[presenceKey]PresenceStatus),
		changedAt:   make(map[presenceKey]time.Time),
		lingering:   make(map[presenceKey]time.Time),
		stop:        make(chan struct{}),
		done:        make(chan struct{}),
	}
	go p.run()
	return p
}

// DisconnectGrace is the configured grace. Read-only after construction.
func (p *PresenceTracker) DisconnectGrace() time.Duration { return p.grace }

// SetObserver installs the callback for tracker-driven transitions. Passing nil
// removes it. Safe to call at any time; the observer is read under p.mu and
// invoked after it is released.
func (p *PresenceTracker) SetObserver(observer PresenceObserver) {
	p.mu.Lock()
	p.observer = observer
	p.mu.Unlock()
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
//
// The returned change reports Changed only when the user was not already
// online: a second tab or device joins an existing presence rather than
// re-asserting it.
func (p *PresenceTracker) Connect(workspaceID, userID, connID string) PresenceChange {
	key := presenceKey{workspaceID: workspaceID, userID: userID}
	now := p.now()

	p.mu.Lock()
	defer p.mu.Unlock()

	// A connection inside the grace resumes the user: nothing was published
	// when the previous one left, so nothing has to be taken back — and it is
	// the same lifecycle. Any other first connection starts a new one.
	delete(p.lingering, key)
	if p.conns[key] == nil {
		p.conns[key] = make(map[string]time.Time)
	}
	p.conns[key][connID] = now
	p.beginGenerationLocked(key)
	return p.setStatusLocked(key, PresenceOnline, now)
}

// Disconnect removes a connection for (workspaceID, userID).
// If this was the last active connection, presence becomes offline.
// Safe to call from any goroutine.
//
// The remaining connections are re-read here rather than left to the next
// ticker, because the connection that left may have been the only reason the
// user was online. A tab opened next to an idle one puts the user back online;
// closing it again leaves nothing but the idle tab, and the user is away *at
// that moment* — not up to a quarter of the away timeout later, which is a
// state the client would have shown as wrong for as long as it lasted.
//
// Changed still means "the user's aggregate answer moved". Closing one of two
// active sessions moves nothing, which is the multi-device rule this tracker
// exists to hold.
func (p *PresenceTracker) Disconnect(workspaceID, userID, connID string) PresenceChange {
	key := presenceKey{workspaceID: workspaceID, userID: userID}
	now := p.now()

	p.mu.Lock()
	defer p.mu.Unlock()

	conns, ok := p.conns[key]
	if !ok {
		return p.currentLocked(key)
	}
	delete(conns, connID)
	if len(conns) > 0 {
		return p.setStatusLocked(key, p.deriveConnectedStateLocked(conns, now), now)
	}
	delete(p.conns, key)
	if p.grace > 0 {
		// The last connection is gone, but a dropped socket is not yet a user
		// who left: a Wi-Fi switch, a proxy reconnect or a deploy all look like
		// this for a few seconds. The state is kept and the decision deferred to
		// the sweep, which declares offline only if nothing reconnected.
		p.lingering[key] = now.Add(p.grace)
		change := p.currentLocked(key)
		change.Lingering = true
		return change
	}
	return p.forgetLocked(key, now)
}

// beginGenerationLocked gives a person who had no lifecycle here a new one.
func (p *PresenceTracker) beginGenerationLocked(key presenceKey) {
	if _, live := p.generations[key]; live {
		return
	}
	if p.generations == nil {
		p.generations = make(map[presenceKey]uint64)
	}
	p.generationSeq++
	p.generations[key] = p.generationSeq
}

// currentLocked is the tracker's answer for a user, with nothing changed.
func (p *PresenceTracker) currentLocked(key presenceKey) PresenceChange {
	if status, ok := p.status[key]; ok {
		return PresenceChange{Status: status, At: p.changedAt[key], Generation: p.generations[key]}
	}
	return PresenceChange{Status: PresenceOffline, At: p.changedAt[key], Generation: p.generations[key]}
}

// forgetLocked declares a user with no connection offline.
//
// The status entry is deleted rather than set to PresenceOffline, so the status
// map doesn't accumulate stale entries for users that have disconnected.
// Status() returns PresenceOffline for absent keys.
func (p *PresenceTracker) forgetLocked(key presenceKey, now time.Time) PresenceChange {
	previous, tracked := p.status[key]
	ended := p.generations[key]
	delete(p.status, key)
	delete(p.changedAt, key)
	delete(p.lingering, key)
	delete(p.generations, key)
	return PresenceChange{
		Status: PresenceOffline, At: now, Changed: tracked && previous != PresenceOffline, Generation: ended,
	}
}

// RecordActivity records user activity on a specific connection.
// Resets the inactivity timer for connID and, if the user was away,
// restores their presence to online.
// Safe to call from any goroutine.
//
// Changed is true only for the away → online transition. Activity on a user who
// is already online is the common case — every inbound frame is activity — and
// it must not put an event on the wire.
func (p *PresenceTracker) RecordActivity(workspaceID, userID, connID string) PresenceChange {
	key := presenceKey{workspaceID: workspaceID, userID: userID}
	now := p.now()

	p.mu.Lock()
	defer p.mu.Unlock()

	conns, ok := p.conns[key]
	if !ok {
		return PresenceChange{Status: PresenceOffline}
	}
	if _, hasConn := conns[connID]; !hasConn {
		return PresenceChange{Status: p.status[key], At: p.changedAt[key]}
	}
	conns[connID] = now
	if p.status[key] == PresenceAway {
		return p.setStatusLocked(key, PresenceOnline, now)
	}
	return PresenceChange{Status: p.status[key], At: p.changedAt[key]}
}

// deriveConnectedStateLocked is what a set of connections alone says about a
// user: online while any one of them is recent, away when every one has gone
// past the timeout, offline when there are none left.
//
// It is the single definition of "inactive" in this tracker — Disconnect and
// checkAway both ask it — so the two can never disagree about the boundary. The
// comparison is `<= awayTimeout` counts as active, which is the rule checkAway
// has always used. p.mu must be held.
func (p *PresenceTracker) deriveConnectedStateLocked(conns map[string]time.Time, now time.Time) PresenceStatus {
	if len(conns) == 0 {
		return PresenceOffline
	}
	for _, lastActivity := range conns {
		if now.Sub(lastActivity) <= p.awayTimeout {
			return PresenceOnline
		}
	}
	return PresenceAway
}

// setStatusLocked writes a status and stamps it. p.mu must be held.
func (p *PresenceTracker) setStatusLocked(key presenceKey, status PresenceStatus, now time.Time) PresenceChange {
	if current, ok := p.status[key]; ok && current == status {
		return PresenceChange{Status: status, At: p.changedAt[key], Generation: p.generations[key]}
	}
	p.status[key] = status
	p.changedAt[key] = now
	return PresenceChange{Status: status, At: now, Changed: true, Generation: p.generations[key]}
}

// Status returns the current presence status for (workspaceID, userID).
// Returns PresenceOffline for unknown users.
// Safe to call from any goroutine.
func (p *PresenceTracker) Status(workspaceID, userID string) PresenceStatus {
	status, _ := p.StatusAt(workspaceID, userID)
	return status
}

// StatusAt returns the current presence status for (workspaceID, userID) and
// when it last changed.
//
// An unknown user is offline as of the zero time: nothing is being claimed
// about when they left, only that this instance holds no connection for them.
// Safe to call from any goroutine.
func (p *PresenceTracker) StatusAt(workspaceID, userID string) (PresenceStatus, time.Time) {
	key := presenceKey{workspaceID: workspaceID, userID: userID}

	p.mu.RLock()
	defer p.mu.RUnlock()

	if s, ok := p.status[key]; ok {
		return s, p.changedAt[key]
	}
	return PresenceOffline, time.Time{}
}

// Lifecycle is StatusAt plus the generation the answer belongs to (zero when
// this tracker holds no lifecycle for the person).
func (p *PresenceTracker) Lifecycle(workspaceID, userID string) PresenceChange {
	key := presenceKey{workspaceID: workspaceID, userID: userID}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.currentLocked(key)
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

	ticker := time.NewTicker(p.sweepInterval())
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			p.checkAway()
			p.expireGraces()
		case <-p.stop:
			return
		}
	}
}

// sweepInterval is a quarter of the away timeout, tightened to a third of the
// grace so an expired grace is noticed within a third of its own length.
// Floored at 1 second so very short test timeouts don't produce a near-zero
// ticker interval, which would thrash the scheduler.
func (p *PresenceTracker) sweepInterval() time.Duration {
	interval := p.awayTimeout / 4
	if p.grace > 0 && p.grace/3 < interval {
		interval = p.grace / 3
	}
	return max(interval, time.Second)
}

// expireGraces declares offline every lingering user whose grace has run out,
// and reports each transition to the observer after p.mu is released. Called
// from the run goroutine and directly in tests.
func (p *PresenceTracker) expireGraces() {
	now := p.now()

	p.mu.Lock()
	expired := map[presenceKey]uint64{}
	for key, until := range p.lingering {
		if now.Before(until) {
			continue
		}
		if change := p.forgetLocked(key, now); change.Changed {
			expired[key] = change.Generation
		}
	}
	observer := p.observer
	p.mu.Unlock()

	if observer == nil {
		return
	}
	for key, generation := range expired {
		observer(key.workspaceID, key.userID, PresenceOffline, now, generation)
	}
}

// checkAway transitions online users to away when all their connections have
// been inactive for longer than awayTimeout. Called exclusively from the run
// goroutine (and directly in tests via the exported checkAway path).
//
// Invariant: only iterates p.conns, so users with no active connections (already
// offline) are never touched. The status map reflects the authoritative state;
// a missing key is equivalent to PresenceOffline (see Status).
// The transitions it makes are handed to the observer *after* p.mu is released,
// so a fan-out that reads hub state can never run while this lock is held.
func (p *PresenceTracker) checkAway() {
	now := p.now()

	p.mu.Lock()
	var transitioned []presenceKey
	generations := map[presenceKey]uint64{}
	for key, connMap := range p.conns {
		if p.status[key] != PresenceOnline {
			continue
		}
		if p.deriveConnectedStateLocked(connMap, now) == PresenceAway {
			p.status[key] = PresenceAway
			p.changedAt[key] = now
			transitioned = append(transitioned, key)
			generations[key] = p.generations[key]
		}
	}
	observer := p.observer
	p.mu.Unlock()

	if observer == nil {
		return
	}
	for _, key := range transitioned {
		observer(key.workspaceID, key.userID, PresenceAway, now, generations[key])
	}
}
