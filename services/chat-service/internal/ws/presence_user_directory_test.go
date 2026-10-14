package ws

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// The per-user half of the Valkey directory (issue #798), against the same
// minimal RESP server the target half is tested with. scriptLocked is the
// server side of the five scripts written in Go. It never knows a script by
// its SHA: EVALSHA is answered NOSCRIPT, which makes the client send EVAL with
// the script itself, and the body is what picks the emulation. The real Lua
// runs against a real Valkey in the TestValkeyUserDirectoryReal_* tests.

var fakeScripts = map[string]func(*fakeValkeyServer, []string, []string) string{
	reachScriptBody:       (*fakeValkeyServer).reachLocked,
	reapScriptBody:        (*fakeValkeyServer).reapLocked,
	beginChangeScriptBody: (*fakeValkeyServer).beginChangeLocked,
	endChangeScriptBody:   (*fakeValkeyServer).endChangeLocked,
	projectionScriptBody:  (*fakeValkeyServer).projectLocked,
}

// scriptLocked runs EVAL body numkeys keys... args...
func (s *fakeValkeyServer) scriptLocked(command []string) string {
	run, known := fakeScripts[command[1]]
	if !strings.EqualFold(command[0], "EVAL") || !known {
		return "-NOSCRIPT No matching script.\r\n"
	}
	count, _ := strconv.Atoi(command[2])
	return run(s, command[3:3+count], command[3+count:])
}

func (s *fakeValkeyServer) hashOf(key string) map[string]string {
	hash := s.hashes[key]
	if hash == nil {
		hash = make(map[string]string)
		s.hashes[key] = hash
	}
	return hash
}

func (s *fakeValkeyServer) bumpRevisionLocked(hash map[string]string) {
	revision, _ := strconv.ParseUint(hash[userRevisionField], 10, 64)
	hash[userRevisionField] = strconv.FormatUint(revision+1, 10)
}

// reachLocked emulates reachScript: field, value (” withdraws), ttl,
// generation — fenced by the stored generation.
func (s *fakeValkeyServer) reachLocked(keys, args []string) string {
	hash := s.hashOf(keys[0])
	field, value, generation := args[0], args[1], args[3]
	old, existed := hash[field]
	var oldState, oldGeneration string
	if existed {
		parts := strings.Split(old, "|")
		if len(parts) != 3 {
			return "-ERR presence reach malformed\r\n"
		}
		oldState, oldGeneration = parts[0], parts[2]
		if atoi(oldGeneration) > atoi(generation) {
			return ":0\r\n"
		}
	}
	if value == "" {
		if existed {
			delete(hash, field)
			s.bumpRevisionLocked(hash)
		}
		return ":1\r\n"
	}
	newState, _, _ := strings.Cut(value, "|")
	hash[field] = value
	if !existed || oldState != newState || oldGeneration != generation {
		s.bumpRevisionLocked(hash)
	}
	s.expires[keys[0]] = atoi(args[2])
	return ":1\r\n"
}

func atoi(value string) int64 {
	n, _ := strconv.ParseInt(value, 10, 64)
	return n
}

func (s *fakeValkeyServer) reapLocked(keys, args []string) string {
	hash := s.hashOf(keys[0])
	removed := 0
	for _, field := range args {
		if _, ok := hash[field]; ok {
			delete(hash, field)
			removed++
		}
	}
	if removed > 0 {
		s.bumpRevisionLocked(hash)
	}
	return fmt.Sprintf(":%d\r\n", removed)
}

// nowMillisLocked is the fake authority's clock, as TIME gives it to a script.
func (s *fakeValkeyServer) nowMillisLocked() int64 {
	if s.clock == nil {
		return time.Now().UnixMilli()
	}
	return s.clock().UnixMilli()
}

// settleLocked emulates settleLua: whether a facts change is still in flight
// at now, after recovering at most limit lapsed marks — removed, and the
// revision moved once.
func (s *fakeValkeyServer) settleLocked(hash map[string]string, now, limit int64) bool {
	inFlight, removed := false, int64(0)
	for field, value := range hash {
		if !strings.HasPrefix(field, userChangeFieldPrefix) {
			continue
		}
		if until, err := strconv.ParseInt(value, 10, 64); err == nil && until > now {
			inFlight = true
		} else if removed < limit {
			delete(hash, field)
			removed++
		}
	}
	if removed > 0 {
		s.bumpRevisionLocked(hash)
	}
	return inFlight
}

func (s *fakeValkeyServer) beginChangeLocked(keys, args []string) string {
	hash := s.hashOf(keys[0])
	now := s.nowMillisLocked()
	s.settleLocked(hash, now, atoi(args[3]))
	hash[args[0]] = strconv.FormatInt(now+atoi(args[1]), 10)
	s.bumpRevisionLocked(hash)
	return ":1\r\n"
}

func (s *fakeValkeyServer) endChangeLocked(keys, args []string) string {
	hash := s.hashOf(keys[0])
	delete(hash, args[0])
	s.bumpRevisionLocked(hash)
	return ":1\r\n"
}

// projectLocked emulates projectionScript: KEYS person + liveness keys; ARGV
// projection, seconds, nanos, ttl, offline, expected, valid-until ms, reap
// limit — judged on the fake authority's own clock.
func (s *fakeValkeyServer) projectLocked(keys, args []string) string {
	projection, offline, expected := args[0], args[4], args[5]
	hash := s.hashOf(keys[0])
	now := s.nowMillisLocked()
	inFlight := s.settleLocked(hash, now, atoi(args[7]))
	if args[6] != "0" && now >= atoi(args[6]) {
		return projectionReply("0", "0", "expired")
	}
	for _, live := range keys[1:] {
		if _, ok := s.strings[live]; !ok {
			return projectionReply("0", "0", "expired")
		}
	}
	revision := hash[userRevisionField]
	if revision == "" {
		revision = "0"
	}
	if inFlight || revision != expected {
		return projectionReply("0", "0", "conflict")
	}
	current, stored := hash[userProjectionField]
	if !stored && projection == offline {
		return projectionReply("0", "0", "unchanged")
	}
	seconds, nanos := atoi(args[1]), atoi(args[2])
	if stored {
		parts := strings.Split(current, "|")
		if parts[0]+"|"+parts[1] == projection {
			return projectionReply(parts[2], parts[3], "unchanged")
		}
		next := nextProjectionInstant(time.Unix(atoi(parts[2]), atoi(parts[3])), time.Unix(seconds, nanos))
		seconds, nanos = next.Unix(), int64(next.Nanosecond())
	}
	hash[userProjectionField] = fmt.Sprintf("%s|%d|%d", projection, seconds, nanos)
	s.bumpRevisionLocked(hash)
	s.expires[keys[0]] = atoi(args[3])
	return projectionReply(strconv.FormatInt(seconds, 10), strconv.FormatInt(nanos, 10), "applied")
}

func projectionReply(values ...string) string {
	var reply strings.Builder
	fmt.Fprintf(&reply, "*%d\r\n", len(values))
	for _, value := range values {
		fmt.Fprintf(&reply, "$%d\r\n%s\r\n", len(value), value)
	}
	return reply.String()
}

func TestValkeyUserDirectory_ReachIsOneFieldPerProcessAndLeased(t *testing.T) {
	server := newFakeValkeyServer()
	a := newTestValkeyDirectory(t, server, "runtime-a")
	ctx := context.Background()
	at := time.Unix(1_790_000_000, 123).UTC()

	if err := assertReach(a, ctx, "ws-1", "user-1", PresenceAway, at); err != nil {
		t.Fatalf("assert: %v", err)
	}
	key := userPresenceKey("ws-1", "user-1")
	if fields := server.hashFields(key); strings.Join(fields, ",") != "r:runtime-a,v" {
		t.Fatalf("fields = %v", fields)
	}
	if server.ttlOf(key) != int64(userPresenceTTL.Seconds()) {
		t.Fatalf("lease = %d", server.ttlOf(key))
	}
	if err := a.RefreshUsers(ctx, "ws-1", []string{"user-1"}); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if err := withdrawReach(a, ctx, "ws-1", "user-1"); err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	if fields := server.hashFields(key); strings.Join(fields, ",") != "v" {
		t.Fatalf("withdraw left %v", fields)
	}
	if err := a.RefreshUsers(ctx, "ws-1", nil); err != nil {
		t.Fatalf("empty refresh: %v", err)
	}
}

func TestValkeyUserDirectory_ReadKeepsLiveReachAndReapsTheDead(t *testing.T) {
	server := newFakeValkeyServer()
	a := newTestValkeyDirectory(t, server, "runtime-a")
	ctx := context.Background()
	at := time.Unix(1_790_000_000, 0).UTC()
	key := userPresenceKey("ws-1", "user-1")

	_ = assertReach(a, ctx, "ws-1", "user-1", PresenceAway, at)
	server.putLiveness("runtime-b")
	server.mu.Lock()
	server.hashes[key]["r:runtime-b"] = encodeUserReach(PresenceOnline, at, 3)
	server.hashes[key]["r:runtime-gone"] = encodeUserReach(PresenceOnline, at, 1)
	server.hashes[key]["r:runtime-bad"] = "nonsense"
	server.hashes[key]["unrelated"] = "x"
	server.hashes[key][userProjectionField] = "available||1790000000|5"
	server.hashes[key][userRevisionField] = "41"
	server.mu.Unlock()

	records, err := a.ReadUsers(ctx, "ws-1", []string{"user-1", "user-none"})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	record := records["user-1"]
	status, _, _ := aggregatePresence(record.Reach)
	if len(record.Reach) != 2 || status != PresenceOnline {
		t.Fatalf("reach = %+v, want this process and the live one", record.Reach)
	}
	if record.Projection == nil || record.Projection.Effective.Availability != domain.PresenceAvailable ||
		// 41 read, and the reap of runtime-gone moved it once more.
		!record.Projection.At.Equal(time.Unix(1_790_000_000, 5)) || record.Revision != 42 || server.hashes[key][userRevisionField] != "42" {
		t.Fatalf("projection = %+v at revision %d", record.Projection, record.Revision)
	}
	for _, field := range server.hashFields(key) {
		if field == "r:runtime-gone" {
			t.Fatal("a dead process's reach survived the read")
		}
	}
	if none := records["user-none"]; len(none.Reach) != 0 || none.Projection != nil || none.Revision != 0 {
		t.Fatalf("a user with no record = %+v", none)
	}
	if empty, err := a.ReadUsers(ctx, "ws-1", nil); err != nil || len(empty) != 0 {
		t.Fatalf("empty read = %+v, %v", empty, err)
	}
}

// A roster entry is legacy evidence exactly when its live instance's liveness
// key lacks the per-user reach capability; this process's own never is.
func TestValkeyDirectory_PresentMarksLegacyInstances(t *testing.T) {
	server := newFakeValkeyServer()
	reader := newTestValkeyDirectory(t, server, "runtime-reader")
	at := time.Unix(1_790_000_000, 0).UTC()
	key := "ws-1:channel:chan-1"
	server.putLiveness("runtime-modern")
	server.putLegacyLiveness("runtime-legacy")
	for _, instance := range []string{"runtime-modern", "runtime-legacy", "runtime-reader"} {
		server.putAssertion(key, "user-"+instance, instance, PresenceOnline, at)
	}
	entries, err := reader.Present(context.Background(), key)
	if err != nil || len(entries) != 3 {
		t.Fatalf("present = %+v %v", entries, err)
	}
	for _, entry := range entries {
		if entry.Legacy != (entry.InstanceID == "runtime-legacy") {
			t.Fatalf("%s legacy = %v", entry.InstanceID, entry.Legacy)
		}
	}
}

func TestValkeyUserDirectory_ProjectionContract(t *testing.T) {
	assertProjectionContract(t, newTestValkeyDirectory(t, newFakeValkeyServer(), "runtime-a"), "ws-1")
}

func TestValkeyUserDirectory_SurfacesFailures(t *testing.T) {
	ctx := context.Background()
	for _, failing := range []string{"HGETALL", "EVALSHA", "EXPIRE"} {
		server := newFakeValkeyServer()
		a := newTestValkeyDirectory(t, server, "runtime-a")
		server.mu.Lock()
		server.failing = failing
		server.mu.Unlock()
		errs := []error{
			assertReach(a, ctx, "ws-1", "user-1", PresenceOnline, time.Now()),
			withdrawReach(a, ctx, "ws-1", "user-1"),
			a.RefreshUsers(ctx, "ws-1", []string{"user-1"}),
			touchFacts(a, ctx, "ws-1", "user-1"),
		}
		_, readErr := a.ReadUsers(ctx, "ws-1", []string{"user-1"})
		_, _, projectErr := a.Project(ctx, "ws-1", "user-1", commitOf(domain.EffectivePresence{Availability: domain.PresenceBusy}, 0, time.Now()))
		errs = append(errs, readErr, projectErr)
		if errors.Join(errs...) == nil {
			t.Fatalf("a failing %s was reported nowhere", failing)
		}
	}
}

func TestValkeyUserDirectory_DecodesOnlyWellFormedProjections(t *testing.T) {
	for _, bad := range []string{"", "available", "available||x|1", "available||1|1000000000", "available||1|-1"} {
		if _, ok := decodeProjection(bad); ok {
			t.Fatalf("%q decoded", bad)
		}
	}
	for _, reply := range [][]string{{"1"}, {"x", "1", "applied"}, {"1", "1", "maybe"}} {
		if _, _, err := parseProjectionReply(reply); err == nil {
			t.Fatalf("reply %v was accepted", reply)
		}
	}
}

// assertProjectionContract is the CAS contract, run against the fake server
// and against a real Valkey with the same expectations.
func assertProjectionContract(t *testing.T, store UserPresenceStore, workspace string) {
	t.Helper()
	ctx := context.Background()
	now := time.Unix(1_790_000_000, 999_999_999).UTC()
	available := domain.EffectivePresence{Availability: domain.PresenceAvailable}
	dnd := domain.EffectivePresence{Availability: domain.PresenceDoNotDisturb, Activity: domain.PresenceActivityInCall}
	offline := domain.EffectivePresence{Availability: domain.PresenceOffline}
	revision := func() uint64 {
		records, err := store.ReadUsers(ctx, workspace, []string{"user-1"})
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		return records["user-1"].Revision
	}

	if at, outcome, err := store.Project(ctx, workspace, "user-1", commitOf(offline, revision(), now)); err != nil || outcome != projectionUnchanged || !at.IsZero() {
		t.Fatalf("offline over nothing = %v %v %v, want no write", at, outcome, err)
	}
	first, outcome, err := store.Project(ctx, workspace, "user-1", commitOf(available, revision(), now))
	if err != nil || outcome != projectionApplied || !first.Equal(now) {
		t.Fatalf("first = %v %v %v", first, outcome, err)
	}
	// Identical is unchanged when its facts are current, and a conflict when
	// they are not: facts that moved cannot confirm anything.
	if _, outcome, _ := store.Project(ctx, workspace, "user-1", commitOf(available, 0, now.Add(time.Hour))); outcome != projectionConflict {
		t.Fatalf("identical from stale facts = %v", outcome)
	}
	if same, outcome, _ := store.Project(ctx, workspace, "user-1", commitOf(available, revision(), now.Add(time.Hour))); outcome != projectionUnchanged || !same.Equal(first) {
		t.Fatalf("identical = %v %v", same, outcome)
	}
	// A composition that read its facts before somebody else's landed loses,
	// and writes nothing.
	stale := revision()
	if err := touchFacts(store, ctx, workspace, "user-1"); err != nil {
		t.Fatalf("touch: %v", err)
	}
	if _, outcome, _ := store.Project(ctx, workspace, "user-1", commitOf(dnd, stale, now)); outcome != projectionConflict {
		t.Fatalf("stale composition = %v, want a conflict", outcome)
	}
	// Recomposed at the current revision it applies; a clock behind the stored
	// instant still moves forward, across a second.
	current := revision()
	next, outcome, _ := store.Project(ctx, workspace, "user-1", commitOf(dnd, current, now.Add(-time.Minute)))
	if outcome != projectionApplied || !next.Equal(now.Add(time.Nanosecond)) {
		t.Fatalf("recomposed = %v (%v), want one nanosecond after %v", next, outcome, now)
	}
	if revision() != current+1 {
		t.Fatal("an applied projection did not move the revision exactly once")
	}
}

// realValkeyDirectory connects to the disposable Valkey named by
// CHAT_TEST_VALKEY_URL, or skips: the real Lua is opt-in like the PostgreSQL
// suites. Every test writes under a workspace of its own.
func realValkeyDirectory(t *testing.T, instanceID string) *ValkeyPresenceDirectory {
	t.Helper()
	url := os.Getenv("CHAT_TEST_VALKEY_URL")
	if url == "" {
		t.Skip("CHAT_TEST_VALKEY_URL is not set")
	}
	directory, err := NewValkeyPresenceDirectory(url, instanceID)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(directory.Close)
	return directory
}

func realWorkspace() string { return fmt.Sprintf("ws-real-%d", time.Now().UnixNano()) }

func TestValkeyUserDirectoryReal_ProjectionContract(t *testing.T) {
	assertProjectionContract(t, realValkeyDirectory(t, "runtime-real"), realWorkspace())
}

// TEST C against the real script: A has read Available's facts, B lands DND,
// A's stale composition is refused, and A recomposed from the new facts
// agrees with B without minting another version.
func TestValkeyUserDirectoryReal_StaleCompositionIsRejectedAcrossProcesses(t *testing.T) {
	a, b := realValkeyDirectory(t, "runtime-a"), realValkeyDirectory(t, "runtime-b")
	ctx, workspace := context.Background(), realWorkspace()
	now := time.Unix(1_790_000_000, 0).UTC()
	available := domain.EffectivePresence{Availability: domain.PresenceAvailable}
	dnd := domain.EffectivePresence{Availability: domain.PresenceDoNotDisturb}
	read := func(store UserPresenceStore) UserPresenceRecord {
		records, err := store.ReadUsers(ctx, workspace, []string{"user-1"})
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		return records["user-1"]
	}
	_ = assertReach(a, ctx, workspace, "user-1", PresenceOnline, now)
	_, _, _ = a.Project(ctx, workspace, "user-1", commitOf(available, read(a).Revision, now))

	stale := read(a)
	_ = touchFacts(b, ctx, workspace, "user-1")
	bAt, outcome, err := b.Project(ctx, workspace, "user-1", commitOf(dnd, read(b).Revision, now.Add(-time.Hour)))
	if err != nil || outcome != projectionApplied {
		t.Fatalf("B = %v %v", outcome, err)
	}
	away := domain.EffectivePresence{Availability: domain.PresenceAway}
	if _, outcome, _ := a.Project(ctx, workspace, "user-1", commitOf(away, stale.Revision, now.Add(time.Hour))); outcome != projectionConflict {
		t.Fatalf("A's stale composition = %v, want a conflict", outcome)
	}
	again, outcome, _ := a.Project(ctx, workspace, "user-1", commitOf(dnd, read(a).Revision, now.Add(time.Hour)))
	if outcome != projectionUnchanged || !again.Equal(bAt) {
		t.Fatalf("recomposed = %v %v, want B's %v kept", again, outcome, bAt)
	}
	if final := read(a).Projection; final == nil || final.Effective != dnd || !final.At.Equal(bAt) || !bAt.After(now) {
		t.Fatalf("stored = %+v; B's version %v must follow A's %v despite B's slow clock", final, bAt, now)
	}
}

// TEST 6: identical projections racing produce one version, not one each.
func TestValkeyUserDirectoryReal_IdenticalProjectionsRacingApplyOnce(t *testing.T) {
	directory := realValkeyDirectory(t, "runtime-real")
	ctx, workspace := context.Background(), realWorkspace()
	busy := domain.EffectivePresence{Availability: domain.PresenceBusy}
	records, _ := directory.ReadUsers(ctx, workspace, []string{"user-1"})
	expected := records["user-1"].Revision
	outcomes := make(chan projectionOutcome, 16)
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Go(func() {
			_, outcome, err := directory.Project(ctx, workspace, "user-1", commitOf(busy, expected, time.Unix(1_790_000_000, int64(i))))
			if err != nil {
				t.Error(err)
			}
			outcomes <- outcome
		})
	}
	wg.Wait()
	close(outcomes)
	applied := 0
	for outcome := range outcomes {
		switch outcome {
		case projectionApplied:
			applied++
		case projectionConflict:
			// Its revision was taken by the racer that applied: it would
			// recompose, and find its projection already there.
		default:
			t.Fatalf("an identical racer got %v", outcome)
		}
	}
	after, _ := directory.ReadUsers(ctx, workspace, []string{"user-1"})
	if applied != 1 || after["user-1"].Revision != expected+1 {
		t.Fatalf("applied %d times, revision %d → %d", applied, expected, after["user-1"].Revision)
	}
}

func TestValkeyUserDirectoryReal_ReachMovesTheRevisionOnlyWhenItsStateDoes(t *testing.T) {
	directory := realValkeyDirectory(t, "runtime-real")
	ctx, workspace := context.Background(), realWorkspace()
	at := time.Unix(1_790_000_000, 0).UTC()
	revision := func() uint64 {
		records, err := directory.ReadUsers(ctx, workspace, []string{"user-1"})
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		return records["user-1"].Revision
	}
	steps := []struct {
		name  string
		apply func() error
		moves bool
	}{
		{"assert online", func() error { return assertReach(directory, ctx, workspace, "user-1", PresenceOnline, at) }, true},
		{"re-assert online later", func() error {
			return assertReach(directory, ctx, workspace, "user-1", PresenceOnline, at.Add(time.Minute))
		}, false},
		{"away", func() error { return assertReach(directory, ctx, workspace, "user-1", PresenceAway, at) }, true},
		{"withdraw", func() error { return withdrawReach(directory, ctx, workspace, "user-1") }, true},
		{"withdraw again", func() error { return withdrawReach(directory, ctx, workspace, "user-1") }, false},
		{"touch", func() error { return touchFacts(directory, ctx, workspace, "user-1") }, true},
	}
	for _, step := range steps {
		before := revision()
		if err := step.apply(); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		if moved := revision() != before; moved != step.moves {
			t.Fatalf("%s moved the revision = %v, want %v", step.name, moved, step.moves)
		}
	}
}

func TestValkeyUserDirectoryReal_MalformedStateFailsClearly(t *testing.T) {
	directory := realValkeyDirectory(t, "runtime-real")
	ctx, workspace := context.Background(), realWorkspace()
	busy := domain.EffectivePresence{Availability: domain.PresenceBusy}
	key := userPresenceKey(workspace, "user-1")
	for field, value := range map[string]string{userProjectionField: "garbage", userRevisionField: "not-a-number"} {
		_ = directory.client.Do(ctx, directory.client.B().Del().Key(key).Build()).Error()
		if err := directory.client.Do(ctx, directory.client.B().Hset().Key(key).FieldValue().FieldValue(field, value).Build()).Error(); err != nil {
			t.Fatal(err)
		}
		if _, _, err := directory.Project(ctx, workspace, "user-1", commitOf(busy, 0, time.Now())); err == nil {
			t.Fatalf("a malformed %s was overwritten silently", field)
		}
	}
}

// The capability marker on the liveness key is what tells legacy roster
// evidence from a modern instance's repeat of its own per-user reach.
func TestValkeyUserDirectoryReal_RostersTellLegacyInstancesApart(t *testing.T) {
	workspace := realWorkspace()
	modern := realValkeyDirectory(t, "runtime-modern-"+workspace)
	reader := realValkeyDirectory(t, "runtime-reader-"+workspace)
	ctx := context.Background()
	legacyID := "runtime-legacy-" + workspace
	at := time.Now().UTC()
	if err := modern.Heartbeat(ctx); err != nil {
		t.Fatal(err)
	}
	if err := reader.client.Do(ctx, reader.client.B().Set().Key(directoryLivePrefix+legacyID).Value("1").ExSeconds(60).Build()).Error(); err != nil {
		t.Fatal(err)
	}
	key := workspace + ":channel:chan-1"
	if err := modern.Record(ctx, DirectoryEntry{UserID: "user-1", State: PresenceOnline, At: at, InstanceID: modern.instanceID}, []string{key}); err != nil {
		t.Fatal(err)
	}
	if err := reader.client.Do(ctx, reader.client.B().Hset().Key(directoryKeyPrefix+key).FieldValue().
		FieldValue(directoryField("user-2", legacyID), encodeDirectoryValue(PresenceOnline, at)).Build()).Error(); err != nil {
		t.Fatal(err)
	}
	entries, err := reader.Present(ctx, key)
	if err != nil || len(entries) != 2 {
		t.Fatalf("present = %+v %v", entries, err)
	}
	for _, entry := range entries {
		if entry.Legacy != (entry.InstanceID == legacyID) {
			t.Fatalf("%s legacy = %v", entry.InstanceID, entry.Legacy)
		}
	}
}
