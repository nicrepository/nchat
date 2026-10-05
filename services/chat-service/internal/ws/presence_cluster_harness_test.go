package ws

import (
	"testing"
	"time"
)

// presenceCluster is several hubs sharing one in-memory directory, one presence
// context and one fake clock — the smallest world in which a person can be
// served by two replicas at once. Every step is driven by hand.
type presenceCluster struct {
	clk       *fakeClock
	directory *fakeDirectory
	source    *fakePresenceContext
	nodes     map[string]*clusterMember
}

type clusterMember struct {
	id      string
	hub     *Hub
	tracker *PresenceTracker
	clients map[string]*Client
	// lastEvents is what the last join published.
	lastEvents []Event
}

func newPresenceCluster(t *testing.T) *presenceCluster {
	t.Helper()
	clk := newFakeClock(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	directory := newFakeDirectory()
	directory.clock = clk.Now
	return &presenceCluster{
		clk: clk, directory: directory, source: newFakePresenceContext(), nodes: map[string]*clusterMember{},
	}
}

// node returns the replica with this id, creating it on first use.
func (c *presenceCluster) node(id string) *clusterMember {
	return c.nodeWithGrace(id, 0)
}

// nodeWithGrace is node with a disconnect grace on its tracker.
func (c *presenceCluster) nodeWithGrace(id string, grace time.Duration) *clusterMember {
	if member, ok := c.nodes[id]; ok {
		return member
	}
	tracker := newTestPresenceTrackerWithGrace(5*time.Minute, grace, c.clk)
	hub := newClusterNode(id, c.directory.view(id), tracker)
	hub.presenceContext = c.source
	member := &clusterMember{id: id, hub: hub, tracker: tracker, clients: map[string]*Client{}}
	c.nodes[id] = member
	return member
}

// join connects a session of userID to this replica, subscribes it to the
// given targets and settles everything that published. The events are kept in
// m.lastEvents for a test that asserts on them.
func (m *clusterMember) join(t *testing.T, clientID, userID string, targets ...string) *Client {
	t.Helper()
	c := newClient(clientID, userID, "ws-1", &fakeSender{})
	registerInHub(t, m.hub, c)
	m.hub.connectPresence(c)
	for _, target := range targets {
		subscribeInHubState(t, m.hub, c, TargetTypeChannel, target)
		m.hub.handleSubscribed(c, TargetTypeChannel, target, 0)
	}
	_ = takeSnapshots(t, c)
	m.lastEvents = drainPresenceEvents(t, m.hub)
	m.clients[clientID] = c
	return c
}

// leave drops a session the way a closed socket does.
func (m *clusterMember) leave(t *testing.T, clientID string) {
	t.Helper()
	c, ok := m.clients[clientID]
	if !ok {
		t.Fatalf("no session %q on %s", clientID, m.id)
	}
	m.hub.dropClient(c)
	delete(m.clients, clientID)
}

// readCount is how many context reads the source has served.
func (f *fakePresenceContext) readCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads
}
