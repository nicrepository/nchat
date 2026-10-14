package ws

import (
	"context"
	"sync"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// Per-user presence (issue #798).
//
// The target-scoped directory answers "who is present in this conversation".
// It cannot answer "how reachable is this person": a session asserts into the
// conversations it subscribed to, and two sessions of one person may have
// subscribed to entirely different ones. So the person's reach is kept under a
// key of their own — workspace and user — with one assertion per instance, and
// beside it the one version of their effective presence every replica stamps
// events and snapshots with.
//
//   - reach: each instance writes and removes only its own assertion, fenced by
//     the tracker lifecycle (generation) it belongs to: a write carrying an
//     older generation than the stored one is refused, so a departure decided
//     for one lifecycle can never retract the next. An assertion counts only
//     while its instance is alive.
//   - projection: the effective presence last published for the person and the
//     instant it took that value. No projection at all means offline: that is
//     what observers were told before anything was published, so publishing an
//     offline over nothing is not a change — which is what keeps a hidden
//     person's first connection from announcing itself. It changes only when
//     the projection does, so an unchanged state keeps its instant and a
//     changed one always gets a newer one than anything published before it.
//   - revision: a counter that moves with every fact the projection is
//     computed from — a reach changing state or lifecycle, a reach reaped, a
//     database fact change beginning and ending (BeginFactsChange,
//     EndFactsChange), a projection applied. A composer reads it before its
//     facts and commits only if it has not moved since.
//   - facts changes in flight: a database fact (a manual state, a call) is
//     changed between BeginFactsChange and EndFactsChange. While one is in
//     flight the person's facts are unsettled and no composition of them is
//     committed: a read between the two could pair the new revision with the
//     old fact. A change is dated by the store's own clock — its lease is a
//     duration — so the clock that judges it is the clock that started it. A
//     change nobody ended stops counting at its lease, and the next change or
//     commit about the person recovers it: removes it and moves the revision,
//     so whatever was read while it was open is read again.
//
// Project is where a composition becomes the public presence. It commits only
// if the revision is the one read, no facts change is in flight, every timed
// fact used is still valid (ValidUntil) and every other instance whose reach
// was counted is still alive — each judged by the store itself, on its own
// clock, at the moment it decides: the Valkey script reads the server's TIME,
// the in-memory store its clock under its lock. An instant the composer
// captured before the call never decides validity, however long the call took
// to arrive.

// PresenceProjection is the effective presence published for a person and the
// instant it took that value.
type PresenceProjection struct {
	Effective domain.EffectivePresence
	At        time.Time
}

// UserPresenceRecord is everything the cluster holds about one person.
type UserPresenceRecord struct {
	// Reach is every live instance's assertion, this one's included.
	Reach []DirectoryEntry
	// Projection is nil when nothing was ever published, or it lapsed.
	Projection *PresenceProjection
	// Revision is the facts revision this record was read at; Project takes
	// it back as the revision its composition expects.
	Revision uint64
}

// ProjectionCommit is one composition offered to Project.
type ProjectionCommit struct {
	Effective domain.EffectivePresence
	// Expected is the revision the composer read its facts at.
	Expected uint64
	// Now is the committer's clock: the instant a changed projection is
	// stamped with (or just after the stored one). It orders versions only;
	// it never decides whether the composition is still valid.
	Now time.Time
	// ValidUntil is when the earliest timed fact the composition used — a
	// manual state's end, a call lease's — stops holding: the store refuses
	// the commit when its own clock has reached it. Zero: none.
	ValidUntil time.Time
	// Instances are the other instances whose reach the composition counted;
	// each must still be alive at the commit.
	Instances []string
}

// FactsChange identifies one database fact change in flight.
type FactsChange struct {
	Token string
	// Lease is how long the change is honoured if it never ends — a writer
	// that crashed between the two calls — counted by the store from the
	// moment it records the change, on its own clock.
	Lease time.Duration
}

// projectionOutcome is what Project did.
type projectionOutcome uint8

const (
	// projectionUnchanged: the stored projection already is this one (or an
	// offline over nothing); the stored instant stands.
	projectionUnchanged projectionOutcome = iota
	// projectionApplied: the projection moved, to a newer instant.
	projectionApplied
	// projectionConflict: the facts moved since the composer read them, or
	// a change of them is in flight; nothing was written and the composition
	// has to be redone.
	projectionConflict
	// projectionExpired: a timed fact the composition used ended, or an
	// instance whose reach it counted died, before the commit. Nothing was
	// written; the composition has to be redone.
	projectionExpired
)

// UserPresenceStore holds per-user reach, the facts revision and the
// effective projection.
type UserPresenceStore interface {
	// AssertReach writes this instance's reach for a user in lifecycle
	// generation. false: a newer generation holds the field.
	AssertReach(ctx context.Context, workspaceID, userID string, state PresenceStatus, at time.Time, generation uint64) (bool, error)
	// WithdrawReach removes this instance's reach for a user when it belongs
	// to generation or an older one. false: a newer generation holds it.
	WithdrawReach(ctx context.Context, workspaceID, userID string, generation uint64) (bool, error)
	// ReadUsers returns the record of each user, one round trip for all.
	ReadUsers(ctx context.Context, workspaceID string, userIDs []string) (map[string]UserPresenceRecord, error)
	// Project commits a composition (see ProjectionCommit) and returns the
	// instant the projection holds: the stored one when nothing changed, a
	// newer one when it was applied, none on a conflict or an expiry.
	Project(ctx context.Context, workspaceID, userID string, commit ProjectionCommit) (time.Time, projectionOutcome, error)
	// BeginFactsChange moves each user's revision and marks a change in
	// flight; EndFactsChange moves it again and clears the mark.
	BeginFactsChange(ctx context.Context, workspaceID string, userIDs []string, change FactsChange) error
	EndFactsChange(ctx context.Context, workspaceID string, userIDs []string, change FactsChange) error
	// RefreshUsers renews the lease of these users' records.
	RefreshUsers(ctx context.Context, workspaceID string, userIDs []string) error
}

// userPresence is the store in effect: the directory's when it keeps one (see
// WithPresenceDirectory), otherwise an in-memory store — a single instance is
// the whole cluster.
func (h *Hub) userPresence() UserPresenceStore {
	h.userStoreOnce.Do(func() {
		if h.userStore == nil {
			h.userStore = newLocalUserPresence(h.presenceInstanceID, h.presenceClock)
		}
	})
	return h.userStore
}

// nextProjectionInstant is the instant a changed projection takes: now, or
// just after the previous one when a clock is behind it, so a client holding
// the previous version always accepts the new one.
func nextProjectionInstant(previous, now time.Time) time.Time {
	if now.After(previous) {
		return now
	}
	return previous.Add(time.Nanosecond)
}

// lapsedAt is whether a composition's timed facts have ended at now, the
// store's own clock at the decision.
func (c ProjectionCommit) lapsedAt(now time.Time) bool {
	return !c.ValidUntil.IsZero() && !now.Before(c.ValidUntil)
}

// localUserPresence is the single-instance store. Its memory is bounded by the
// users this process holds: a record goes when its reach is withdrawn and the
// person was published offline, and a facts change in flight is held only
// until it ends. Ordering survives the forgetting through two process-wide
// values rather than per-person tombstones: every instant it issues is later
// than every instant it issued before (issued), so a person who returns after
// a clock step backwards still gets a version their observers accept; and a
// person without a record reads at the revision of the last forgetting or
// fact change about anybody without one (forgotten), so a composition that
// started before either cannot commit as if nothing had happened. A facts
// change about somebody this process holds nothing for therefore allocates
// nothing beyond its own in-flight entry.
type localUserPresence struct {
	instanceID string
	clock      func() time.Time
	mu         sync.Mutex
	records    map[presenceKey]*localUserRecord
	changing   map[presenceKey]map[string]time.Time
	seq        uint64
	forgotten  uint64
	issued     time.Time
}

type localUserRecord struct {
	reach      *DirectoryEntry
	projection *PresenceProjection
	revision   uint64
}

func newLocalUserPresence(instanceID string, clock func() time.Time) *localUserPresence {
	return &localUserPresence{
		instanceID: instanceID,
		clock:      clock,
		records:    map[presenceKey]*localUserRecord{},
		changing:   map[presenceKey]map[string]time.Time{},
	}
}

func (s *localUserPresence) record(pk presenceKey) *localUserRecord {
	rec := s.records[pk]
	if rec == nil {
		rec = &localUserRecord{revision: s.forgotten}
		s.records[pk] = rec
	}
	return rec
}

// revisionLocked is the revision a person reads at.
func (s *localUserPresence) revisionLocked(pk presenceKey) uint64 {
	if rec := s.records[pk]; rec != nil {
		return rec.revision
	}
	return s.forgotten
}

func (s *localUserPresence) bumpLocked(rec *localUserRecord) {
	s.seq++
	rec.revision = s.seq
}

// bumpPersonLocked moves one person's revision without allocating a record:
// for somebody held nowhere, the revision every such person reads at moves.
func (s *localUserPresence) bumpPersonLocked(pk presenceKey) {
	if rec := s.records[pk]; rec != nil {
		s.bumpLocked(rec)
		return
	}
	s.seq++
	s.forgotten = s.seq
}

func (s *localUserPresence) AssertReach(
	_ context.Context, workspaceID, userID string, state PresenceStatus, at time.Time, generation uint64,
) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := s.record(presenceKey{workspaceID: workspaceID, userID: userID})
	if rec.reach != nil && rec.reach.Generation > generation {
		return false, nil
	}
	if rec.reach == nil || rec.reach.State != state || rec.reach.Generation != generation {
		s.bumpLocked(rec)
	}
	rec.reach = &DirectoryEntry{UserID: userID, State: state, At: at, InstanceID: s.instanceID, Generation: generation}
	return true, nil
}

func (s *localUserPresence) WithdrawReach(_ context.Context, workspaceID, userID string, generation uint64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := s.records[presenceKey{workspaceID: workspaceID, userID: userID}]
	if rec == nil || rec.reach == nil {
		return true, nil
	}
	if rec.reach.Generation > generation {
		return false, nil
	}
	rec.reach = nil
	s.bumpLocked(rec)
	return true, nil
}

func (s *localUserPresence) BeginFactsChange(_ context.Context, workspaceID string, userIDs []string, change FactsChange) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock()
	for _, userID := range userIDs {
		pk := presenceKey{workspaceID: workspaceID, userID: userID}
		s.settleLocked(pk, now)
		if s.changing[pk] == nil {
			s.changing[pk] = map[string]time.Time{}
		}
		s.changing[pk][change.Token] = now.Add(change.Lease)
		s.bumpPersonLocked(pk)
	}
	return nil
}

// settleLocked is settleLua on one process: it recovers the changes whose
// lease has passed at now — removes them and, if there were any, moves the
// revision once — and reports whether one is still in flight.
func (s *localUserPresence) settleLocked(pk presenceKey, now time.Time) bool {
	recovered := false
	for token, until := range s.changing[pk] {
		if !until.After(now) {
			delete(s.changing[pk], token)
			recovered = true
		}
	}
	if len(s.changing[pk]) == 0 {
		delete(s.changing, pk)
	}
	if recovered {
		s.bumpPersonLocked(pk)
	}
	return len(s.changing[pk]) > 0
}

func (s *localUserPresence) EndFactsChange(_ context.Context, workspaceID string, userIDs []string, change FactsChange) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, userID := range userIDs {
		pk := presenceKey{workspaceID: workspaceID, userID: userID}
		delete(s.changing[pk], change.Token)
		if len(s.changing[pk]) == 0 {
			delete(s.changing, pk)
		}
		s.bumpPersonLocked(pk)
	}
	return nil
}

func (s *localUserPresence) ReadUsers(_ context.Context, workspaceID string, userIDs []string) (map[string]UserPresenceRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]UserPresenceRecord, len(userIDs))
	for _, userID := range userIDs {
		pk := presenceKey{workspaceID: workspaceID, userID: userID}
		record := UserPresenceRecord{Revision: s.revisionLocked(pk)}
		if rec := s.records[pk]; rec != nil {
			record.Reach, record.Projection = rec.snapshot()
		}
		out[userID] = record
	}
	return out, nil
}

// snapshot copies what a reader may keep.
func (rec *localUserRecord) snapshot() ([]DirectoryEntry, *PresenceProjection) {
	var reach []DirectoryEntry
	if rec.reach != nil {
		reach = []DirectoryEntry{*rec.reach}
	}
	if rec.projection == nil {
		return reach, nil
	}
	projection := *rec.projection
	return reach, &projection
}

// Project is projectionScript's contract on one process, decided on the
// store's clock read under its lock. A single instance counts no other
// instance's reach, so Instances is always empty here.
func (s *localUserPresence) Project(
	_ context.Context, workspaceID, userID string, commit ProjectionCommit,
) (time.Time, projectionOutcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock()
	pk := presenceKey{workspaceID: workspaceID, userID: userID}
	inFlight := s.settleLocked(pk, now)
	if commit.lapsedAt(now) {
		return time.Time{}, projectionExpired, nil
	}
	if inFlight || s.revisionLocked(pk) != commit.Expected {
		return time.Time{}, projectionConflict, nil
	}
	stored := s.storedProjectionLocked(pk)
	if stored != nil && stored.Effective == commit.Effective {
		s.forgetDepartedLocked(pk)
		return stored.At, projectionUnchanged, nil
	}
	if stored == nil && commit.Effective.Availability == domain.PresenceOffline {
		s.forgetDepartedLocked(pk)
		return time.Time{}, projectionUnchanged, nil
	}
	return s.applyLocked(pk, commit), projectionApplied, nil
}

func (s *localUserPresence) applyLocked(pk presenceKey, commit ProjectionCommit) time.Time {
	rec := s.record(pk)
	previous := s.issued
	if rec.projection != nil && rec.projection.At.After(previous) {
		previous = rec.projection.At
	}
	at := nextProjectionInstant(previous, commit.Now)
	s.issued = at
	rec.projection = &PresenceProjection{Effective: commit.Effective, At: at}
	s.bumpLocked(rec)
	s.forgetDepartedLocked(pk)
	return at
}

func (s *localUserPresence) storedProjectionLocked(pk presenceKey) *PresenceProjection {
	if rec := s.records[pk]; rec != nil {
		return rec.projection
	}
	return nil
}

// forgetDepartedLocked drops a person who is offline with no reach left. What
// ordering needs of them outlives the record in issued and forgotten.
func (s *localUserPresence) forgetDepartedLocked(pk presenceKey) {
	rec := s.records[pk]
	if rec == nil || rec.reach != nil {
		return
	}
	if rec.projection == nil || rec.projection.Effective.Availability == domain.PresenceOffline {
		delete(s.records, pk)
		s.seq++
		s.forgotten = s.seq
	}
}

func (s *localUserPresence) RefreshUsers(context.Context, string, []string) error { return nil }

// size reports how many records and in-flight changes are held. Test seam for
// the memory bound.
func (s *localUserPresence) size() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.records) + len(s.changing)
}
