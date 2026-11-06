package signatures

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeFixture(t *testing.T, path, header string) {
	t.Helper()
	data := header + strings.Repeat(" ", 512-len(header)) + "fixture body"
	requireSuccess(t, os.WriteFile(path, []byte(data), 0600))
}
func fixtureManifest(n string) Manifest {
	return Manifest{Engine: Engine, Hashes: [3]string{n, n, n}, DailyVersion: 1, DatabaseTime: time.Unix(100, 0)}
}

func TestDatabaseHeaderBoundaries(t *testing.T) {
	for _, test := range []struct {
		header string
		valid  bool
	}{
		{"ClamAV-VDB:date:42:1:216:hash:sig:builder:100", true},
		{"ClamAV-VDB:date:42:1:217:hash:sig:builder:100", false},
		{"ClamAV-VDB:date:0:1:90:hash:sig:builder:100", false},
		{"ClamAV-VDB:date:42:1:90:hash:sig:builder:0", false},
		{"ClamAV-VDB:date:bad:1:90:hash:sig:builder:100", false},
		{"ClamAV-VDB:date:42:1:bad:hash:sig:builder:100", false},
		{"ClamAV-VDB:date:42:1:90:hash:sig:builder:bad", false},
		{"other:date:42:1:90:hash:sig:builder:100", false},
		{"broken", false},
	} {
		t.Run(test.header, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "daily.cvd")
			writeFixture(t, path, test.header)
			assertHeader(t, path, test.valid)
		})
	}
	if _, _, err := databaseHeader(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing header accepted")
	}
	path := filepath.Join(t.TempDir(), "short")
	requireSuccess(t, os.WriteFile(path, []byte("short"), 0600))
	if _, _, err := databaseHeader(path); err == nil {
		t.Fatal("short header accepted")
	}
}

func TestSnapshotAllowlistAndCorruption(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "snapshot")
	for _, name := range databaseNames {
		writeFixture(t, filepath.Join(src, name), "ClamAV-VDB:date:42:1:90:hash:sig:builder:100")
	}
	requireSuccess(t, os.WriteFile(filepath.Join(src, "malicious.ndb"), []byte("not copied"), 0600))
	requireSuccess(t, CopyDatabases(src, dst))
	for _, name := range databaseNames {
		a, _ := databaseHash(filepath.Join(src, name))
		b, _ := databaseHash(filepath.Join(dst, name))
		if a != b {
			t.Fatal("copy changed bytes")
		}
	}
	if _, err := os.Stat(filepath.Join(dst, "malicious.ndb")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unlisted signature copied")
	}
	if err := CopyDatabases(src, dst); err == nil {
		t.Fatal("snapshot overwritten")
	}
	requireSuccess(t, os.Remove(filepath.Join(src, "daily.cvd")))
	requireSuccess(t, os.Symlink(filepath.Join(src, "main.cvd"), filepath.Join(src, "daily.cvd")))
	if err := CopyDatabases(src, t.TempDir()); err == nil {
		t.Fatal("symlink accepted")
	}
	if _, err := databaseHash(src); err == nil {
		t.Fatal("directory accepted as CVD")
	}
}

func TestPublicationTransactionAndLock(t *testing.T) {
	store := Store{Root: t.TempDir()}
	now := time.Now().UTC()
	promote := func(content string) Manifest {
		t.Helper()
		dir, m := candidateFixture(t, store, content)
		requireSuccess(t, store.Promote(dir, m, now))
		return m
	}
	a, b := promote("a"), promote("b")
	state, err := store.State()
	if err != nil || state.Active != b.ID() || state.Previous != a.ID() {
		t.Fatalf("lost transaction: %#v %v", state, err)
	}
	if _, err = store.generation(a.ID()); err != nil {
		t.Fatal("previous generation lost")
	}
	if err = store.WithLock(func() error {
		return store.WithLock(func() error { t.Fatal("concurrent publisher entered"); return nil })
	}); err == nil {
		t.Fatal("concurrent lock accepted")
	}
	if err = store.WithLock(func() error { return nil }); err != nil {
		t.Fatal("lock not recovered")
	}
	if err = store.Promote("unused", b, now.Add(time.Second)); err != nil {
		t.Fatal("unchanged update rejected")
	}
	state, _ = store.State()
	if state.Previous != a.ID() {
		t.Fatal("unchanged update discarded rollback")
	}
}

func TestStoreRejectsUnknownStateAndTraversal(t *testing.T) {
	store := Store{Root: t.TempDir()}
	for _, data := range []string{`{}`, `{"active":"../etc"}`, `{"active":"` + strings.Repeat("a", 64) + `","previous":"../etc"}`, `{"active":"` + strings.Repeat("a", 64) + `","extra":true}`, `{} {}`} {
		requireSuccess(t, os.WriteFile(filepath.Join(store.Root, "state.json"), []byte(data), 0600))
		if _, err := store.State(); err == nil {
			t.Fatal("invalid state accepted")
		}
	}
	if _, err := store.generation("../etc"); err == nil {
		t.Fatal("traversal accepted")
	}
	id := strings.Repeat("b", 64)
	requireSuccess(t, os.Symlink(t.TempDir(), filepath.Join(store.Root, id)))
	if _, err := store.generation(id); err == nil {
		t.Fatal("linked generation accepted")
	}
	if _, err := store.Snapshot(context.Background(), id, t.TempDir(), time.Now(), time.Hour); err == nil {
		t.Fatal("linked snapshot accepted")
	}
	if err := store.Rollback(context.Background(), time.Now(), time.Hour); err == nil {
		t.Fatal("unknown rollback accepted")
	}
}

func requireSuccess(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func candidateFixture(t *testing.T, store Store, content string) (string, Manifest) {
	t.Helper()
	dir, err := os.MkdirTemp(store.Root, ".candidate-")
	requireSuccess(t, err)
	m := fixtureManifest(content)
	m.DailyVersion = int64(content[0])
	for i, name := range databaseNames {
		writeFixture(t, filepath.Join(dir, name), fmt.Sprintf("ClamAV-VDB:date:%d:1:216:hash:sig:builder:100", m.DailyVersion))
		m.Hashes[i], err = databaseHash(filepath.Join(dir, name))
		requireSuccess(t, err)
	}
	return dir, m
}

func publishFixture(t *testing.T, store Store, content string) Manifest {
	t.Helper()
	dir, m := candidateFixture(t, store, content)
	requireSuccess(t, store.WithLock(func() error { return store.Promote(dir, m, time.Now().UTC()) }))
	return m
}

func TestRetentionKeepsLastLoadedGeneration(t *testing.T) {
	store := Store{Root: t.TempDir()}
	a := publishFixture(t, store, "a")
	e := validEvidence()
	e.Generation = a.ID()
	requireSuccess(t, store.Acknowledge(e))
	b := publishFixture(t, store, "b")
	c := publishFixture(t, store, "c")
	requireSuccess(t, store.prune())
	state, err := store.State()
	requireSuccess(t, err)
	if state.Previous != a.ID() || state.Active != c.ID() {
		t.Fatal("unactivated candidate replaced last loaded generation")
	}
	_, err = store.generation(a.ID())
	requireSuccess(t, err)
	if _, err = store.generation(b.ID()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("obsolete generation retained")
	}
	// Lost acknowledgement must not rotate away the remembered valid previous.
	requireSuccess(t, os.Remove(filepath.Join(store.ackRoot(), "loaded.json")))
	publishFixture(t, store, "d")
	state, err = store.State()
	requireSuccess(t, err)
	if state.Previous != a.ID() {
		t.Fatal("lost acknowledgement destroyed rollback")
	}
}

func TestPublicationRecoversCrashBeforeStateCommit(t *testing.T) {
	store := Store{Root: t.TempDir()}
	a := publishFixture(t, store, "a")
	dir, b := candidateFixture(t, store, "b")
	requireSuccess(t, os.Rename(dir, filepath.Join(store.Root, b.ID())))
	publishFixture(t, store, "b")
	state, err := store.State()
	requireSuccess(t, err)
	if state.Active != b.ID() || state.Previous != a.ID() {
		t.Fatal("interrupted publication not recovered")
	}
	dir, c := candidateFixture(t, store, "c")
	requireSuccess(t, os.Mkdir(filepath.Join(store.Root, c.ID()), 0700))
	if err = store.Promote(dir, c, time.Now()); err == nil {
		t.Fatal("corrupted existing generation accepted")
	}
	state, err = store.State()
	requireSuccess(t, err)
	if state.Active != b.ID() {
		t.Fatal("failed publication changed active generation")
	}
}

func TestInterruptedStagingAndStorageErrors(t *testing.T) {
	store := Store{Root: t.TempDir()}
	interrupted := filepath.Join(store.Root, ".candidate-interrupted")
	requireSuccess(t, os.Mkdir(interrupted, 0700))
	requireSuccess(t, store.recoverCandidates())
	if _, err := os.Stat(interrupted); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("interrupted staging survived recovery")
	}
	work := t.TempDir()
	requireSuccess(t, os.WriteFile(filepath.Join(work, "daily.cld"), []byte("untracked incremental database"), 0600))
	if _, _, err := store.stage(context.Background(), work, time.Now(), time.Hour); err == nil {
		t.Fatal("untracked CLD accepted")
	}
	if _, _, err := store.stage(context.Background(), t.TempDir(), time.Now(), time.Hour); err == nil {
		t.Fatal("incomplete CVD set accepted")
	}
	entries, err := os.ReadDir(store.Root)
	requireSuccess(t, err)
	if len(entries) != 0 {
		t.Fatal("failed staging was not cleaned")
	}
	requireSuccess(t, os.WriteFile(filepath.Join(store.Root, ".publisher.lock"), nil, 0600))
	requireSuccess(t, os.Remove(filepath.Join(store.Root, ".publisher.lock")))
	requireSuccess(t, os.Symlink(t.TempDir(), filepath.Join(store.Root, ".publisher.lock")))
	if err = store.WithLock(func() error { return nil }); err == nil {
		t.Fatal("symlinked lock accepted")
	}
	if err = Run(context.Background(), "sh", "-c", "false"); err == nil {
		t.Fatal("non-allowlisted tool executed")
	}
	if err = atomicJSON(filepath.Join(store.Root, "missing"), "state.json", State{}); err == nil {
		t.Fatal("storage failure ignored")
	}
	if err = store.Acknowledge(Evidence{}); err == nil {
		t.Fatal("unknown load acknowledged")
	}
}

func TestCorruptStateAndUnavailableStorage(t *testing.T) {
	store := Store{Root: t.TempDir()}
	path := filepath.Join(store.Root, "state.json")
	requireSuccess(t, os.WriteFile(path, []byte(strings.Repeat("x", 4097)), 0600))
	if _, err := store.State(); err == nil {
		t.Fatal("oversized state accepted")
	}
	if err := store.prune(); err == nil {
		t.Fatal("GC without known active generation allowed")
	}
	if err := store.Promote("missing", fixtureManifest("a"), time.Now()); err == nil {
		t.Fatal("unknown publication succeeded")
	}
	if err := syncDir(filepath.Join(store.Root, "missing")); err == nil {
		t.Fatal("missing filesystem treated as durable")
	}
	requireSuccess(t, os.Mkdir(filepath.Join(store.Root, "ack"), 0700))
	requireSuccess(t, atomicJSON(filepath.Join(store.Root, "ack"), "loaded.json", acknowledgement{Generation: "unknown"}))
	if _, err := store.loadedGeneration(); err == nil {
		t.Fatal("unknown loaded generation trusted")
	}
	custom := Store{Root: store.Root, AckRoot: t.TempDir()}
	requireSuccess(t, custom.Acknowledge(validEvidence()))
	if id, err := custom.loadedGeneration(); err != nil || id != validEvidence().Generation {
		t.Fatal("consumer acknowledgement mount ignored")
	}
}

func TestUnavailableSnapshotDestinations(t *testing.T) {
	store := Store{Root: t.TempDir()}
	file := filepath.Join(store.Root, "file")
	requireSuccess(t, os.WriteFile(file, nil, 0600))
	if err := CopyDatabases(store.Root, file); err == nil {
		t.Fatal("unwritable snapshot destination accepted")
	}
	if err := copyDatabase(filepath.Join(store.Root, "missing"), file); err == nil {
		t.Fatal("missing source copied")
	}
	if err := copyDatabase(file, filepath.Join(store.Root, "missing", "destination")); err == nil {
		t.Fatal("missing destination mount ignored")
	}
}

func assertHeader(t *testing.T, path string, valid bool) {
	t.Helper()
	v, stamp, err := databaseHeader(path)
	if !valid {
		if err == nil {
			t.Fatal("invalid header accepted")
		}
		return
	}
	requireSuccess(t, err)
	if v != 42 || stamp.Unix() != 100 {
		t.Fatal("wrong signed header fields")
	}
}

func TestVerificationAndAcknowledgementMustHaveUsableStorage(t *testing.T) {
	if _, err := Verify(context.Background(), t.TempDir(), time.Now(), time.Hour); err == nil {
		t.Fatal("missing database set verified")
	}
	path := filepath.Join(t.TempDir(), "file")
	requireSuccess(t, os.WriteFile(path, nil, 0600))
	store := Store{Root: t.TempDir(), AckRoot: path}
	if err := store.Acknowledge(validEvidence()); err == nil {
		t.Fatal("unwritable acknowledgement ignored")
	}
}

func TestConsumerActivationExcludesPublicationAndCollection(t *testing.T) {
	store := Store{Root: t.TempDir()}
	publishFixture(t, store, "a")
	release, err := store.ConsumerLock()
	requireSuccess(t, err)
	if err = store.WithLock(func() error { t.Error("publisher entered during snapshot activation"); return nil }); err == nil {
		t.Fatal("consumer did not protect acknowledgement retention")
	}
	release()
	release()
	requireSuccess(t, store.WithLock(func() error { return nil }))
	if _, err = (Store{Root: t.TempDir()}).ConsumerLock(); err == nil {
		t.Fatal("missing bootstrap lock accepted")
	}
}

func TestPublicationExcludesConsumerActivation(t *testing.T) {
	store := Store{Root: t.TempDir()}
	requireSuccess(t, store.WithLock(func() error {
		release, err := store.ConsumerLock()
		if err == nil {
			release()
			t.Error("consumer activated during publication")
		}
		return nil
	}))
}

func TestMissingAcknowledgedGenerationCannotDestroyRollback(t *testing.T) {
	store := Store{Root: t.TempDir()}
	active := publishFixture(t, store, "active")
	proof := validEvidence()
	proof.Generation = strings.Repeat("b", 64)
	requireSuccess(t, store.Acknowledge(proof))
	candidate, m := candidateFixture(t, store, "candidate")
	if err := store.Promote(candidate, m, time.Now()); err == nil {
		t.Fatal("missing acknowledged generation accepted as rollback")
	}
	state, err := store.State()
	requireSuccess(t, err)
	if state.Active != active.ID() {
		t.Fatal("unrecoverable retention metadata changed active generation")
	}
}
