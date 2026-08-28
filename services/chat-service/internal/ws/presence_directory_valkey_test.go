package ws

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The real directory against a real client (RF-58).
//
// Everything else in this package tests the hub against an in-memory double,
// which is the right level for ordering and lifecycle but says nothing about the
// commands actually sent. The claims that only exist in the Valkey layer — the
// field naming, the lease renewal, that a withdrawal is an HDEL of one field and
// not of the key, and that a read removes the assertions of processes that are
// gone — are exercised here, by speaking RESP to the same client the service
// uses.

// ── a minimal Valkey ──────────────────────────────────────────────────────────

// fakeValkeyServer implements just enough of the protocol for the directory:
// hashes, string keys with a notion of expiry, and the pipelined forms of each.
type fakeValkeyServer struct {
	mu      sync.Mutex
	hashes  map[string]map[string]string
	strings map[string]string
	// expires records the last EXPIRE seconds seen per key, so a test can assert
	// the lease was renewed without waiting for anything.
	expires  map[string]int64
	commands []string
	// failing makes one command name return an error, so the error path of each
	// operation is a tested path and not an assumption.
	failing string
}

func newFakeValkeyServer() *fakeValkeyServer {
	return &fakeValkeyServer{
		hashes:  make(map[string]map[string]string),
		strings: make(map[string]string),
		expires: make(map[string]int64),
	}
}

func (s *fakeValkeyServer) start(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go s.serve(conn)
		}
	}()
	return "valkey://" + listener.Addr().String() + "?protocol=2&client_cache=0"
}

func (s *fakeValkeyServer) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	reader := bufio.NewReader(conn)
	for {
		command, err := readReactionLimiterCommand(reader)
		if err != nil {
			return
		}
		s.mu.Lock()
		s.commands = append(s.commands, strings.ToUpper(command[0]))
		reply := s.applyLocked(command)
		s.mu.Unlock()
		if _, err := io.WriteString(conn, reply); err != nil {
			return
		}
	}
}

func (s *fakeValkeyServer) applyLocked(command []string) string {
	name := strings.ToUpper(command[0])
	if s.failing != "" && name == s.failing {
		return "-ERR presence directory unavailable\r\n"
	}
	switch name {
	case "HELLO":
		return "*2\r\n+proto\r\n:2\r\n"
	case "CLUSTER":
		return "-ERR unknown command 'cluster'\r\n"
	case "HSET":
		hash := s.hashes[command[1]]
		if hash == nil {
			hash = make(map[string]string)
			s.hashes[command[1]] = hash
		}
		hash[command[2]] = command[3]
		return ":1\r\n"
	case "HDEL":
		hash := s.hashes[command[1]]
		if _, ok := hash[command[2]]; !ok {
			return ":0\r\n"
		}
		delete(hash, command[2])
		return ":1\r\n"
	case "HGETALL":
		hash := s.hashes[command[1]]
		fields := make([]string, 0, len(hash))
		for field := range hash {
			fields = append(fields, field)
		}
		sort.Strings(fields)
		var reply strings.Builder
		fmt.Fprintf(&reply, "*%d\r\n", len(fields)*2)
		for _, field := range fields {
			fmt.Fprintf(&reply, "$%d\r\n%s\r\n$%d\r\n%s\r\n",
				len(field), field, len(hash[field]), hash[field])
		}
		return reply.String()
	case "EXPIRE":
		seconds, _ := strconv.ParseInt(command[2], 10, 64)
		if _, ok := s.hashes[command[1]]; !ok {
			if _, ok := s.strings[command[1]]; !ok {
				return ":0\r\n"
			}
		}
		s.expires[command[1]] = seconds
		return ":1\r\n"
	case "SET":
		s.strings[command[1]] = command[2]
		if len(command) >= 5 && strings.EqualFold(command[3], "EX") {
			seconds, _ := strconv.ParseInt(command[4], 10, 64)
			s.expires[command[1]] = seconds
		}
		return "+OK\r\n"
	case "DEL":
		deleted := int64(0)
		for _, key := range command[1:] {
			if _, ok := s.strings[key]; ok {
				delete(s.strings, key)
				deleted++
			}
			if _, ok := s.hashes[key]; ok {
				delete(s.hashes, key)
				deleted++
			}
			delete(s.expires, key)
		}
		return fmt.Sprintf(":%d\r\n", deleted)
	case "MGET":
		var reply strings.Builder
		fmt.Fprintf(&reply, "*%d\r\n", len(command)-1)
		for _, key := range command[1:] {
			value, ok := s.strings[key]
			if !ok {
				reply.WriteString("$-1\r\n")
				continue
			}
			fmt.Fprintf(&reply, "$%d\r\n%s\r\n", len(value), value)
		}
		return reply.String()
	default:
		return "+OK\r\n"
	}
}

func (s *fakeValkeyServer) hashFields(key string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	fields := make([]string, 0, len(s.hashes[key]))
	for field := range s.hashes[key] {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	return fields
}

func (s *fakeValkeyServer) hashExists(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.hashes[key]
	return ok
}

func (s *fakeValkeyServer) stringValue(key string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.strings[key]
	return value, ok
}

func (s *fakeValkeyServer) ttlOf(key string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.expires[key]
}

// putLiveness marks an instance alive without going through the directory, so a
// test can set up processes that are not the one under test.
func (s *fakeValkeyServer) putLiveness(instanceID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.strings[directoryLivePrefix+instanceID] = "1"
}

func (s *fakeValkeyServer) putAssertion(key, userID, instanceID string, state PresenceStatus, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	hash := s.hashes[directoryKeyPrefix+key]
	if hash == nil {
		hash = make(map[string]string)
		s.hashes[directoryKeyPrefix+key] = hash
	}
	hash[directoryField(userID, instanceID)] = encodeDirectoryValue(state, at)
}

func newTestValkeyDirectory(t *testing.T, server *fakeValkeyServer, instanceID string) *ValkeyPresenceDirectory {
	t.Helper()
	directory, err := NewValkeyPresenceDirectory(server.start(t), instanceID)
	if err != nil {
		t.Fatalf("new presence directory: %v", err)
	}
	t.Cleanup(directory.Close)
	return directory
}

// ── configuration ────────────────────────────────────────────────────────────

func TestValkeyDirectory_RejectsIncompleteConfiguration(t *testing.T) {
	for _, tt := range []struct{ url, instanceID string }{
		{url: "", instanceID: "runtime-a"},
		{url: "valkey://localhost:6379", instanceID: ""},
		{url: "://invalid", instanceID: "runtime-a"},
	} {
		if directory, err := NewValkeyPresenceDirectory(tt.url, tt.instanceID); err == nil || directory != nil {
			t.Fatalf("expected %q/%q to be rejected", tt.url, tt.instanceID)
		}
	}
}

// ── the commands a mutation actually sends ───────────────────────────────────

func TestValkeyDirectory_RecordWritesOneFieldPerProcessAndLeasesTheKey(t *testing.T) {
	server := newFakeValkeyServer()
	directory := newTestValkeyDirectory(t, server, "runtime-a")

	at := time.Now().UTC()
	keys := []string{"ws-1:channel:chan-a", "ws-1:channel:chan-b"}
	if err := directory.Record(context.Background(), DirectoryEntry{
		UserID: "user-1", State: PresenceOnline, At: at,
	}, keys); err != nil {
		t.Fatalf("record: %v", err)
	}

	for _, key := range keys {
		redisKey := directoryKeyPrefix + key
		fields := server.hashFields(redisKey)
		if len(fields) != 1 || fields[0] != "user-1|runtime-a" {
			t.Fatalf("%s holds %v, want one field user-1|runtime-a", key, fields)
		}
		// Every write renews the lease, which is what stops a busy conversation
		// from expiring under a process that never stops talking.
		if got := server.ttlOf(redisKey); got != int64(directoryEntryTTL.Seconds()) {
			t.Fatalf("%s lease = %ds, want %ds", key, got, int64(directoryEntryTTL.Seconds()))
		}
	}

	entries, err := directory.Present(context.Background(), keys[0])
	if err != nil {
		t.Fatalf("present: %v", err)
	}
	if len(entries) != 1 || entries[0].State != PresenceOnline || entries[0].InstanceID != "runtime-a" {
		t.Fatalf("read back %+v", entries)
	}
	if !entries[0].At.Equal(at.Truncate(time.Nanosecond)) {
		t.Fatalf("instant round-tripped as %v, want %v", entries[0].At, at)
	}

	// Nothing to write is not a round trip.
	if err := directory.Record(context.Background(), DirectoryEntry{UserID: "user-1"}, nil); err != nil {
		t.Fatalf("empty record: %v", err)
	}
}

func TestValkeyDirectory_ForgetDeletesOnlyItsOwnFieldAndNeverTheKey(t *testing.T) {
	server := newFakeValkeyServer()
	directory := newTestValkeyDirectory(t, server, "runtime-a")
	key := "ws-1:channel:chan-a"

	if err := directory.Record(context.Background(), DirectoryEntry{
		UserID: "user-1", State: PresenceOnline, At: time.Now().UTC(),
	}, []string{key}); err != nil {
		t.Fatalf("record: %v", err)
	}
	// Another live process asserting about the same person, and a third about
	// somebody else.
	server.putLiveness("runtime-b")
	server.putAssertion(key, "user-1", "runtime-b", PresenceAway, time.Now().UTC())
	server.putAssertion(key, "user-2", "runtime-b", PresenceOnline, time.Now().UTC())

	if err := directory.Forget(context.Background(), "ws-1", "user-1", []string{key}); err != nil {
		t.Fatalf("forget: %v", err)
	}

	remaining := server.hashFields(directoryKeyPrefix + key)
	want := []string{"user-1|runtime-b", "user-2|runtime-b"}
	if strings.Join(remaining, ",") != strings.Join(want, ",") {
		t.Fatalf("after forget the target holds %v, want %v", remaining, want)
	}
	if !server.hashExists(directoryKeyPrefix + key) {
		t.Fatal("a withdrawal deleted the whole target key")
	}
	if err := directory.Forget(context.Background(), "ws-1", "user-1", nil); err != nil {
		t.Fatalf("empty forget: %v", err)
	}
}

func TestValkeyDirectory_RefreshRenewsTheLeaseWithoutRewritingAssertions(t *testing.T) {
	server := newFakeValkeyServer()
	directory := newTestValkeyDirectory(t, server, "runtime-a")
	key := "ws-1:channel:chan-a"

	if err := directory.Record(context.Background(), DirectoryEntry{
		UserID: "user-1", State: PresenceOnline, At: time.Now().UTC(),
	}, []string{key}); err != nil {
		t.Fatalf("record: %v", err)
	}
	writesBefore := server.commandCount("HSET")

	if err := directory.Refresh(context.Background(), []string{key}); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if got := server.commandCount("HSET"); got != writesBefore {
		t.Fatalf("refresh rewrote assertions: %d HSETs, want %d", got, writesBefore)
	}
	if got := server.ttlOf(directoryKeyPrefix + key); got != int64(directoryEntryTTL.Seconds()) {
		t.Fatalf("lease = %ds, want %ds", got, int64(directoryEntryTTL.Seconds()))
	}
	if err := directory.Refresh(context.Background(), nil); err != nil {
		t.Fatalf("empty refresh: %v", err)
	}
}

func TestValkeyDirectory_HeartbeatSetsLivenessWithItsOwnTTL(t *testing.T) {
	server := newFakeValkeyServer()
	directory := newTestValkeyDirectory(t, server, "runtime-a")

	if err := directory.Heartbeat(context.Background()); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if got := server.ttlOf(directoryLivePrefix + "runtime-a"); got != int64(instanceLivenessTTL.Seconds()) {
		t.Fatalf("liveness TTL = %ds, want %ds", got, int64(instanceLivenessTTL.Seconds()))
	}
}

// ── reading ──────────────────────────────────────────────────────────────────

func TestValkeyDirectory_PresentKeepsLiveProcessesAndReapsDeadOnes(t *testing.T) {
	server := newFakeValkeyServer()
	directory := newTestValkeyDirectory(t, server, "runtime-a")
	key := "ws-1:channel:chan-a"
	at := time.Now().UTC()

	// This process, another live one, two that are gone, and a malformed field
	// that should be ignored rather than crash a read.
	if err := directory.Record(context.Background(), DirectoryEntry{
		UserID: "user-self", State: PresenceOnline, At: at,
	}, []string{key}); err != nil {
		t.Fatalf("record: %v", err)
	}
	server.putLiveness("runtime-b")
	server.putAssertion(key, "user-live", "runtime-b", PresenceAway, at)
	server.putAssertion(key, "user-dead-1", "runtime-gone-1", PresenceOnline, at)
	server.putAssertion(key, "user-dead-2", "runtime-gone-2", PresenceOnline, at)
	server.mu.Lock()
	server.hashes[directoryKeyPrefix+key]["nonsense"] = "also nonsense"
	server.mu.Unlock()

	entries, err := directory.Present(context.Background(), key)
	if err != nil {
		t.Fatalf("present: %v", err)
	}
	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		got = append(got, entry.UserID)
	}
	sort.Strings(got)
	if strings.Join(got, ",") != "user-live,user-self" {
		t.Fatalf("roster = %v, want the live processes only", got)
	}

	// The dead ones are not merely filtered: their fields are gone from the hash,
	// which is the only thing that stops the target growing forever.
	fields := server.hashFields(directoryKeyPrefix + key)
	want := []string{"nonsense", "user-live|runtime-b", "user-self|runtime-a"}
	if strings.Join(fields, ",") != strings.Join(want, ",") {
		t.Fatalf("after the read the target holds %v, want %v", fields, want)
	}

	// A target nobody is in reads as empty without a liveness round trip.
	mgets := server.commandCount("MGET")
	entries, err = directory.Present(context.Background(), "ws-1:channel:empty")
	if err != nil || len(entries) != 0 {
		t.Fatalf("empty target: %+v err=%v", entries, err)
	}
	if got := server.commandCount("MGET"); got != mgets {
		t.Fatalf("an empty target still asked about liveness: %d MGETs, want %d", got, mgets)
	}
}

func (s *fakeValkeyServer) commandCount(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, command := range s.commands {
		if command == name {
			count++
		}
	}
	return count
}

// Every operation reports a Valkey failure rather than pretending it worked. The
// callers all treat that as "confirmed nothing", which is the invariant a silent
// success would break.
func TestValkeyDirectory_SurfacesValkeyFailures(t *testing.T) {
	key := "ws-1:channel:chan-a"
	entry := DirectoryEntry{UserID: "user-1", State: PresenceOnline, At: time.Now().UTC()}

	for _, tt := range []struct {
		name    string
		failing string
		call    func(*ValkeyPresenceDirectory) error
	}{
		{name: "record", failing: "HSET", call: func(d *ValkeyPresenceDirectory) error {
			return d.Record(context.Background(), entry, []string{key})
		}},
		{name: "forget", failing: "HDEL", call: func(d *ValkeyPresenceDirectory) error {
			return d.Forget(context.Background(), "ws-1", "user-1", []string{key})
		}},
		{name: "refresh", failing: "EXPIRE", call: func(d *ValkeyPresenceDirectory) error {
			return d.Refresh(context.Background(), []string{key})
		}},
		{name: "heartbeat", failing: "SET", call: func(d *ValkeyPresenceDirectory) error {
			return d.Heartbeat(context.Background())
		}},
		{name: "read", failing: "HGETALL", call: func(d *ValkeyPresenceDirectory) error {
			_, err := d.Present(context.Background(), key)
			return err
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := newFakeValkeyServer()
			server.failing = tt.failing
			if err := tt.call(newTestValkeyDirectory(t, server, "runtime-a")); err == nil {
				t.Fatal("a Valkey failure was reported as success")
			}
		})
	}

	// Liveness is the second round trip of a read, and failing it fails the read:
	// a roster built without knowing who is alive would silently include the dead.
	server := newFakeValkeyServer()
	directory := newTestValkeyDirectory(t, server, "runtime-a")
	server.putAssertion(key, "user-other", "runtime-b", PresenceOnline, time.Now().UTC())
	server.mu.Lock()
	server.failing = "MGET"
	server.mu.Unlock()
	if _, err := directory.Present(context.Background(), key); err == nil {
		t.Fatal("a failed liveness lookup produced a roster anyway")
	}

	// A failed cleanup is not a failed read: the roster was already correct.
	server = newFakeValkeyServer()
	directory = newTestValkeyDirectory(t, server, "runtime-a")
	server.putAssertion(key, "user-dead", "runtime-gone", PresenceOnline, time.Now().UTC())
	server.mu.Lock()
	server.failing = "HDEL"
	server.mu.Unlock()
	entries, err := directory.Present(context.Background(), key)
	if err != nil {
		t.Fatalf("a failed cleanup broke the read: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("a dead process's assertion was returned: %+v", entries)
	}
}
