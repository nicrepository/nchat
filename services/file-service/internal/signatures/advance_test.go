package signatures

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNormalPromotionCannotDowngradeAnyDatabase(t *testing.T) {
	store := Store{Root: t.TempDir()}
	a := publishFixture(t, store, "e") // signed-format fixture version 101, not real CVD crypto.
	next, m := candidateFixture(t, store, "f")
	requireSuccess(t, store.WithLock(func() error { return store.Promote(next, m, time.Now()) }))
	stale, old := candidateFixture(t, store, "e")
	if err := store.WithLock(func() error { return store.Promote(stale, old, time.Now()) }); err == nil {
		t.Fatal("102 -> 101 accepted")
	}
	state, err := store.State()
	requireSuccess(t, err)
	if state.Active != m.ID() || state.Previous != a.ID() {
		t.Fatal("downgrade changed publication")
	}
}

func TestEqualVersionRequiresIdenticalHashes(t *testing.T) {
	for _, name := range databaseNames {
		t.Run(name, func(t *testing.T) {
			store := Store{Root: t.TempDir()}
			a := publishFixture(t, store, "e")
			next, m := candidateFixture(t, store, "e")
			root, err := os.OpenRoot(next)
			requireSuccess(t, err)
			defer func() { _ = root.Close() }()
			f, err := root.OpenFile(name, os.O_WRONLY, 0600)
			requireSuccess(t, err)
			_, err = f.WriteAt([]byte("different body"), 512)
			requireSuccess(t, err)
			requireSuccess(t, f.Close())
			for i, n := range databaseNames {
				m.Hashes[i], err = databaseHash(filepath.Join(next, n))
				requireSuccess(t, err)
			}
			if err = store.Promote(next, m, time.Now()); err == nil {
				t.Fatal("same version changed bytes")
			}
			state, err := store.State()
			requireSuccess(t, err)
			if state.Active != a.ID() {
				t.Fatal("collision replaced active")
			}
		})
	}
}

func TestAdvanceRejectsMalformedAndDowngradedHeaders(t *testing.T) {
	before, after := filepath.Join(t.TempDir(), "old"), filepath.Join(t.TempDir(), "new")
	writeFixture(t, before, "ClamAV-VDB:date:102:1:216:hash:sig:builder:100")
	for _, version := range []int{101, 102, 103} {
		writeFixture(t, after, fmt.Sprintf("ClamAV-VDB:date:%d:1:216:hash:sig:builder:100", version))
		a, err := databaseHash(before)
		requireSuccess(t, err)
		b, err := databaseHash(after)
		requireSuccess(t, err)
		err = advancingDatabase(before, after, a, b)
		if (err == nil) != (version >= 102) {
			t.Fatal("incorrect monotonic decision", version, err)
		}
	}
}

func TestClockFileCannotDisappearAfterActivation(t *testing.T) {
	store := Store{Root: t.TempDir()}
	at, err := store.ClockWatermark()
	requireSuccess(t, err)
	if !at.IsZero() {
		t.Fatal("invented initial clock")
	}
	requireSuccess(t, store.CheckpointClock(time.Now()))
	requireSuccess(t, store.Acknowledge(validEvidence()))
	requireSuccess(t, os.Remove(filepath.Join(store.ackRoot(), "clock.json")))
	if _, err = store.ClockWatermark(); err == nil {
		t.Fatal("lost watermark silently bootstrapped")
	}
}

func TestClockMetadataErrorsNeverBootstrapAgain(t *testing.T) {
	store := Store{Root: t.TempDir()}
	if err := store.CheckpointClock(time.Time{}); err == nil {
		t.Fatal("zero watermark persisted")
	}
	requireSuccess(t, os.Mkdir(store.ackRoot(), 0700))
	path := filepath.Join(store.ackRoot(), "clock.json")
	for _, invalid := range []string{"broken", `{"at":"0001-01-01T00:00:00Z"}`} {
		requireSuccess(t, os.WriteFile(path, []byte(invalid), 0600))
		if _, err := store.ClockWatermark(); err == nil {
			t.Fatal("invalid persistent clock opened")
		}
	}
	blocker := filepath.Join(t.TempDir(), "file")
	requireSuccess(t, os.WriteFile(blocker, []byte("x"), 0600))
	if err := (Store{Root: t.TempDir(), AckRoot: blocker}).CheckpointClock(time.Now()); err == nil {
		t.Fatal("failed clock write approved")
	}
}

func TestMonotonicityRequiresIntactActiveMetadata(t *testing.T) {
	store := Store{Root: t.TempDir()}
	a := publishFixture(t, store, "e")
	candidate, m := candidateFixture(t, store, "f")
	manifest := filepath.Join(store.Root, a.ID(), "manifest.json")
	requireSuccess(t, os.WriteFile(manifest, []byte(`{}`), 0600))
	if err := store.Promote(candidate, m, time.Now()); err == nil {
		t.Fatal("untrusted previous version accepted")
	}
	requireSuccess(t, os.Remove(manifest))
	if err := store.Promote(candidate, m, time.Now()); err == nil {
		t.Fatal("missing previous metadata accepted")
	}
}

func TestMonotonicityRejectsChangedBytesAndMalformedVersions(t *testing.T) {
	before, after := filepath.Join(t.TempDir(), "before"), filepath.Join(t.TempDir(), "after")
	writeFixture(t, before, "invalid-header")
	writeFixture(t, after, "ClamAV-VDB:date:103:1:216:hash:sig:builder:100")
	a, err := databaseHash(before)
	requireSuccess(t, err)
	b, err := databaseHash(after)
	requireSuccess(t, err)
	if err = advancingDatabase(before, after, a, b); err == nil {
		t.Fatal("bad active header trusted")
	}
	if err = advancingDatabase(before, after, "wrong", b); err == nil {
		t.Fatal("active corruption trusted")
	}
	if err = advancingDatabase(after, before, b, "wrong"); err == nil {
		t.Fatal("candidate corruption trusted")
	}
	if err = advancingDatabase(after, before, b, a); err == nil {
		t.Fatal("bad candidate header trusted")
	}
}
