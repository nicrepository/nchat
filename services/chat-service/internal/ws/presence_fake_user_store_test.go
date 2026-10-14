package ws

import (
	"context"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// The per-user half of fakeDirectory (issue #798): the same contract as the
// Valkey store — one reach field per instance, fenced by lifecycle generation
// and counted only while that instance is alive; one shared projection that
// moves only when it changes, only against the revision it was composed at,
// only while its timed facts and counted instances still hold; and facts
// changes in flight — so cluster tests exercise the hub against shared state
// rather than against one private store per replica.

func (d *fakeDirectory) userReachLocked(pk presenceKey) map[string]DirectoryEntry {
	if d.users == nil {
		d.users = make(map[presenceKey]map[string]DirectoryEntry)
	}
	reach := d.users[pk]
	if reach == nil {
		reach = make(map[string]DirectoryEntry)
		d.users[pk] = reach
	}
	return reach
}

// failUserStore makes every per-user operation fail until called with nil.
func (d *fakeDirectory) failUserStore(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.failUsers = err
}

// failUserOp makes one per-user operation fail until called with nil.
func (d *fakeDirectory) failUserOp(op string, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.failOps == nil {
		d.failOps = map[string]error{}
	}
	d.failOps[op] = err
}

func (d *fakeDirectory) userFailureLocked(op string) error {
	if d.failUsers != nil {
		return d.failUsers
	}
	return d.failOps[op]
}

// markLegacy makes an instance one from before issue #798.
func (d *fakeDirectory) markLegacy(instanceID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.legacy == nil {
		d.legacy = make(map[string]bool)
	}
	d.legacy[instanceID] = true
}

func (d *fakeDirectory) bumpLocked(pk presenceKey) {
	if d.revisions == nil {
		d.revisions = make(map[presenceKey]uint64)
	}
	d.revisions[pk]++
}

func (d *fakeDirectory) hook(get func(*fakeDirectory) func()) {
	d.mu.Lock()
	run := get(d)
	d.mu.Unlock()
	if run != nil {
		run()
	}
}

func (v *fakeDirectoryView) AssertReach(
	_ context.Context, workspaceID, userID string, state PresenceStatus, at time.Time, generation uint64,
) (bool, error) {
	v.shared.hook(func(d *fakeDirectory) func() { return d.beforeReach })
	v.shared.mu.Lock()
	defer v.shared.mu.Unlock()
	if err := v.shared.userFailureLocked("assert"); err != nil {
		return false, err
	}
	pk := presenceKey{workspaceID: workspaceID, userID: userID}
	reach := v.shared.userReachLocked(pk)
	old, ok := reach[v.instanceID]
	if ok && old.Generation > generation {
		return false, nil
	}
	if !ok || old.State != state || old.Generation != generation {
		v.shared.bumpLocked(pk)
	}
	reach[v.instanceID] = DirectoryEntry{UserID: userID, State: state, At: at, InstanceID: v.instanceID, Generation: generation}
	return true, nil
}

func (v *fakeDirectoryView) WithdrawReach(_ context.Context, workspaceID, userID string, generation uint64) (bool, error) {
	v.shared.hook(func(d *fakeDirectory) func() { return d.beforeReach })
	v.shared.mu.Lock()
	defer v.shared.mu.Unlock()
	if err := v.shared.userFailureLocked("withdraw"); err != nil {
		return false, err
	}
	pk := presenceKey{workspaceID: workspaceID, userID: userID}
	reach := v.shared.userReachLocked(pk)
	old, ok := reach[v.instanceID]
	if !ok {
		return true, nil
	}
	if old.Generation > generation {
		return false, nil
	}
	delete(reach, v.instanceID)
	v.shared.bumpLocked(pk)
	return true, nil
}

func (v *fakeDirectoryView) BeginFactsChange(_ context.Context, workspaceID string, userIDs []string, change FactsChange) error {
	v.shared.mu.Lock()
	defer v.shared.mu.Unlock()
	if err := v.shared.userFailureLocked("begin"); err != nil {
		return err
	}
	if v.shared.changing == nil {
		v.shared.changing = map[presenceKey]map[string]time.Time{}
	}
	now := v.shared.now()
	for _, userID := range userIDs {
		pk := presenceKey{workspaceID: workspaceID, userID: userID}
		v.shared.settleLocked(pk, now)
		if v.shared.changing[pk] == nil {
			v.shared.changing[pk] = map[string]time.Time{}
		}
		v.shared.changing[pk][change.Token] = now.Add(change.Lease)
		v.shared.bumpLocked(pk)
	}
	return nil
}

// settleLocked is settleLua's contract: lapsed changes are recovered — removed,
// and the revision moved once — and whether one is still in flight is told.
func (d *fakeDirectory) settleLocked(pk presenceKey, now time.Time) bool {
	recovered := false
	for token, until := range d.changing[pk] {
		if !until.After(now) {
			delete(d.changing[pk], token)
			recovered = true
		}
	}
	if recovered {
		d.bumpLocked(pk)
	}
	return len(d.changing[pk]) > 0
}

func (v *fakeDirectoryView) EndFactsChange(_ context.Context, workspaceID string, userIDs []string, change FactsChange) error {
	v.shared.mu.Lock()
	defer v.shared.mu.Unlock()
	if err := v.shared.userFailureLocked("end"); err != nil {
		return err
	}
	for _, userID := range userIDs {
		pk := presenceKey{workspaceID: workspaceID, userID: userID}
		delete(v.shared.changing[pk], change.Token)
		v.shared.bumpLocked(pk)
	}
	return nil
}

func (v *fakeDirectoryView) ReadUsers(_ context.Context, workspaceID string, userIDs []string) (map[string]UserPresenceRecord, error) {
	v.shared.mu.Lock()
	defer v.shared.mu.Unlock()
	if err := v.shared.userFailureLocked("read"); err != nil {
		return nil, err
	}
	out := make(map[string]UserPresenceRecord, len(userIDs))
	for _, userID := range userIDs {
		pk := presenceKey{workspaceID: workspaceID, userID: userID}
		record := UserPresenceRecord{Revision: v.shared.revisions[pk]}
		for instanceID, entry := range v.shared.users[pk] {
			if instanceID == v.instanceID || v.shared.aliveLocked(instanceID) {
				record.Reach = append(record.Reach, entry)
			}
		}
		if projection, ok := v.shared.projections[pk]; ok {
			record.Projection = &projection
		}
		out[userID] = record
	}
	return out, nil
}

// Project is projectionScript's contract, in the same order.
func (v *fakeDirectoryView) Project(
	_ context.Context, workspaceID, userID string, commit ProjectionCommit,
) (time.Time, projectionOutcome, error) {
	v.shared.hook(func(d *fakeDirectory) func() { return d.beforeProject })
	v.shared.mu.Lock()
	defer v.shared.mu.Unlock()
	if err := v.shared.userFailureLocked("project"); err != nil {
		return time.Time{}, projectionUnchanged, err
	}
	now := v.shared.now()
	pk := presenceKey{workspaceID: workspaceID, userID: userID}
	if v.shared.changing == nil {
		v.shared.changing = map[presenceKey]map[string]time.Time{}
	}
	inFlight := v.shared.settleLocked(pk, now)
	if commit.lapsedAt(now) || !v.shared.allAliveLocked(commit.Instances) {
		return time.Time{}, projectionExpired, nil
	}
	if inFlight || v.shared.revisions[pk] != commit.Expected {
		return time.Time{}, projectionConflict, nil
	}
	current, stored := v.shared.projections[pk]
	if (!stored && commit.Effective.Availability == domain.PresenceOffline) || (stored && current.Effective == commit.Effective) {
		return current.At, projectionUnchanged, nil
	}
	at := nextProjectionInstant(current.At, commit.Now)
	if v.shared.projections == nil {
		v.shared.projections = make(map[presenceKey]PresenceProjection)
	}
	v.shared.projections[pk] = PresenceProjection{Effective: commit.Effective, At: at}
	v.shared.bumpLocked(pk)
	return at, projectionApplied, nil
}

func (d *fakeDirectory) allAliveLocked(instances []string) bool {
	for _, instanceID := range instances {
		if !d.aliveLocked(instanceID) {
			return false
		}
	}
	return true
}

func (v *fakeDirectoryView) RefreshUsers(context.Context, string, []string) error { return nil }

// commitOf is a composition with no timed fact and no other instance.
func commitOf(effective domain.EffectivePresence, expected uint64, now time.Time) ProjectionCommit {
	return ProjectionCommit{Effective: effective, Expected: expected, Now: now}
}

// assertReach and withdrawReach write one lifecycle (generation 1) and report
// only failures; touchFacts is a whole facts change, begun and ended.
func assertReach(store UserPresenceStore, ctx context.Context, workspaceID, userID string, state PresenceStatus, at time.Time) error {
	_, err := store.AssertReach(ctx, workspaceID, userID, state, at, 1)
	return err
}

func withdrawReach(store UserPresenceStore, ctx context.Context, workspaceID, userID string) error {
	_, err := store.WithdrawReach(ctx, workspaceID, userID, 1)
	return err
}

func touchFacts(store UserPresenceStore, ctx context.Context, workspaceID, userID string) error {
	change := FactsChange{Token: "touch", Lease: time.Minute}
	if err := store.BeginFactsChange(ctx, workspaceID, []string{userID}, change); err != nil {
		return err
	}
	return store.EndFactsChange(ctx, workspaceID, []string{userID}, change)
}
