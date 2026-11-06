package signatures

import (
	"context"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// All external tools and sockets remain inside the disposable network-none image.
func officialFixture(t *testing.T) (Store, Manifest, context.Context) {
	t.Helper()
	if os.Getenv("NCHAT_CLAMAV_ISOLATED_TEST") != "true" {
		t.Skip("requires isolated official ClamAV image")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	store := Store{Root: t.TempDir()}
	candidate := filepath.Join(store.Root, ".candidate-test")
	requireSuccess(t, CopyDatabases("/var/lib/clamav", candidate))
	m, err := Verify(ctx, candidate, time.Now().UTC(), 365*24*time.Hour)
	requireSuccess(t, err)
	requireSuccess(t, store.WithLock(func() error { return store.Promote(candidate, m, time.Now().UTC()) }))
	return store, m, ctx
}

func TestIsolatedOfficialSnapshotAndExpiry(t *testing.T) {
	store, m, ctx := officialFixture(t)
	now := time.Now().UTC()
	if m.Engine != Engine || m.DailyVersion <= 0 {
		t.Fatal("unexpected pinned fixture")
	}
	snapshot := filepath.Join(t.TempDir(), "snapshot")
	loaded, err := store.Snapshot(ctx, m.ID(), snapshot, now, 365*24*time.Hour)
	requireSuccess(t, err)
	if loaded.ID() != m.ID() {
		t.Fatal("snapshot lost generation")
	}
	if _, err = Verify(ctx, snapshot, now, time.Hour); err == nil {
		t.Fatal("expired fixture approved")
	}
}

func TestIsolatedCorruptCVDRejected(t *testing.T) {
	store, m, ctx := officialFixture(t)
	published := filepath.Join(store.Root, m.ID())
	requireSuccess(t, os.Chmod(filepath.Join(published, "daily.cvd"), 0600))
	root, err := os.OpenRoot(published)
	requireSuccess(t, err)
	defer func() { _ = root.Close() }()
	f, err := root.OpenFile("daily.cvd", os.O_WRONLY, 0600)
	requireSuccess(t, err)
	_, err = f.WriteAt([]byte("corruption"), 512)
	requireSuccess(t, err)
	requireSuccess(t, f.Close())
	if _, err = store.Snapshot(ctx, m.ID(), filepath.Join(t.TempDir(), "snapshot"), time.Now().UTC(), 365*24*time.Hour); err == nil {
		t.Fatal("corrupted publication activated")
	}
}

func TestIsolatedUnknownGenerationCannotActivate(t *testing.T) {
	store, _, ctx := officialFixture(t)
	unknown := strings.Repeat("b", 64)
	requireSuccess(t, CopyDatabases("/var/lib/clamav", filepath.Join(store.Root, unknown)))
	if _, err := store.Snapshot(ctx, unknown, filepath.Join(t.TempDir(), "snapshot"), time.Now().UTC(), 365*24*time.Hour); err == nil {
		t.Fatal("valid CVDs used under false generation identity")
	}
}

func TestIsolatedRollbackRejectsExpiryAndCannotOscillate(t *testing.T) {
	store, m, ctx := officialFixture(t)
	now := time.Now().UTC()
	failed := fixtureManifest("failed").ID()
	requireSuccess(t, atomicJSON(store.Root, "state.json", State{Active: failed, Previous: m.ID()}))
	if err := store.Rollback(ctx, now, time.Hour); err == nil {
		t.Fatal("expired rollback allowed")
	}
	requireSuccess(t, store.Rollback(ctx, now, 365*24*time.Hour))
	state, err := store.State()
	requireSuccess(t, err)
	if state.Active != m.ID() || state.Previous != "" {
		t.Fatal("rollback did not converge")
	}
	_, err = os.Stat(filepath.Join(store.Root, "rejected-"+failed))
	requireSuccess(t, err)
	if err = store.Rollback(ctx, now, 365*24*time.Hour); err == nil {
		t.Fatal("rollback oscillated")
	}
}

func TestIsolatedOfflineUpdatePreservesGeneration(t *testing.T) {
	store, m, ctx := officialFixture(t)
	config := filepath.Join(t.TempDir(), "freshclam.conf")
	requireSuccess(t, os.WriteFile(config, []byte("PrivateMirror https://127.0.0.1:1\nScriptedUpdates no\nConnectTimeout 1\nReceiveTimeout 1\nMaxAttempts 1\nTestDatabases yes\n"), 0600))
	updateCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := store.Update(updateCtx, config, time.Now().UTC(), 365*24*time.Hour); err == nil {
		t.Fatal("offline update succeeded")
	}
	state, err := store.State()
	requireSuccess(t, err)
	if state.Active != m.ID() {
		t.Fatal("failed update replaced valid generation")
	}
}

func TestIsolatedFreshClamPrivateHTTPSMirror(t *testing.T) {
	if os.Getenv("NCHAT_CLAMAV_ISOLATED_TEST") != "true" {
		t.Skip("requires isolated official ClamAV image")
	}
	var limited atomic.Bool
	var advanced, cldOnly atomic.Bool
	mirror := officialMirror(&limited, &advanced, &cldOnly)
	defer mirror.Close()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	requireSuccess(t, os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: mirror.Certificate().Raw}), 0600))
	config := filepath.Join(t.TempDir(), "freshclam.conf")
	requireSuccess(t, os.WriteFile(config, []byte("DatabaseOwner clamav\nPrivateMirror "+mirror.URL+"\nScriptedUpdates no\nMaxAttempts 1\nConnectTimeout 1\nReceiveTimeout 10\nTestDatabases yes\nBytecode yes\n"), 0600))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	now := time.Now().UTC()
	age := 365 * 24 * time.Hour
	untrusted := Store{Root: t.TempDir()}
	if err := untrusted.Update(ctx, config, now, age); err == nil {
		t.Fatal("untrusted TLS mirror accepted")
	}
	t.Setenv("CURL_CA_BUNDLE", ca)
	store := untrusted // Retry the same cache after restoring TLS trust.
	requireSuccess(t, store.Update(ctx, config, now, age))
	state, err := store.State()
	requireSuccess(t, err)
	if state.Active == "" || state.LastSuccessfulUpdate.IsZero() {
		t.Fatal("verified update not published")
	}
	requireSuccess(t, store.Update(ctx, config, now.Add(time.Second), age))
	unchanged, err := store.State()
	requireSuccess(t, err)
	if unchanged.Active != state.Active || unchanged.Previous != "" {
		t.Fatal("unchanged update rotated generation")
	}
	testMirrorUpgrade(t, ctx, store, config, now, age, &advanced)
	state, err = store.State()
	requireSuccess(t, err)
	cldOnly.Store(true)
	if err = (Store{Root: t.TempDir()}).Update(ctx, config, now, age); err == nil {
		t.Fatal("CLD-only corrupt mirror bootstrapped")
	}
	cldOnly.Store(false)
	limited.Store(true)
	if err = store.Update(ctx, config, now.Add(2*time.Second), age); err == nil {
		t.Fatal("rate limit accepted as success")
	}
	after, err := store.State()
	requireSuccess(t, err)
	if after.Active != state.Active {
		t.Fatal("rate limit discarded valid base")
	}
}

func officialMirror(limited, advanced, cldOnly *atomic.Bool) *httptest.Server {
	allowed := map[string]bool{"main.cvd": true, "daily.cvd": true, "bytecode.cvd": true}
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if limited.Load() {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		if cldOnly.Load() {
			serveCorruptCLD(w, r)
			return
		}
		serveOfficialCVD(w, r, allowed, advanced.Load())
	}))
}

func TestIsolatedTimestampHeaderCannotRefreshOldDatabase(t *testing.T) {
	if os.Getenv("NCHAT_CLAMAV_ISOLATED_TEST") != "true" {
		t.Skip("requires isolated official ClamAV image")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	dir := t.TempDir()
	requireSuccess(t, CopyDatabases("/var/lib/clamav", dir))
	root, err := os.OpenRoot(dir)
	requireSuccess(t, err)
	defer func() { _ = root.Close() }()
	requireSuccess(t, root.Chmod("daily.cvd", 0600))
	file, err := root.OpenFile("daily.cvd", os.O_RDWR, 0600)
	requireSuccess(t, err)
	header := make([]byte, 512)
	_, err = io.ReadFull(file, header)
	requireSuccess(t, err)
	fields := strings.Split(strings.TrimSpace(string(header)), ":")
	now := time.Now().UTC()
	fields[8] = strconv.FormatInt(now.Add(-time.Minute).Unix(), 10)
	replacement := strings.Join(fields, ":")
	replacement += strings.Repeat(" ", 512-len(replacement))
	_, err = file.WriteAt([]byte(replacement), 0)
	requireSuccess(t, err)
	requireSuccess(t, file.Close())
	// The compressed signed payload is untouched. libclamav must reconcile the
	// outer timestamp with the .info inside that payload, not trust the header alone.
	if _, err = Verify(ctx, dir, now, 2*time.Hour); err == nil {
		t.Fatal("unsigned header refreshed an expired signed payload")
	}
}

func testMirrorUpgrade(t *testing.T, ctx context.Context, store Store, config string, now time.Time, age time.Duration, advanced *atomic.Bool) {
	t.Helper()
	if _, err := os.Stat("/test/next-databases/daily.cvd"); os.IsNotExist(err) {
		return
	}
	before, err := store.State()
	requireSuccess(t, err)
	advanced.Store(true)
	requireSuccess(t, store.Update(ctx, config, now, age))
	after, err := store.State()
	requireSuccess(t, err)
	if after.Active == before.Active {
		t.Fatal("new mirror CVD was not promoted")
	}
	oldVersion, _, err := databaseHeader(filepath.Join(store.Root, before.Active, "daily.cvd"))
	requireSuccess(t, err)
	newVersion, _, err := databaseHeader(filepath.Join(store.Root, after.Active, "daily.cvd"))
	requireSuccess(t, err)
	if newVersion <= oldVersion {
		t.Fatal("real upgrade did not advance daily")
	}
	requireSuccess(t, store.Update(ctx, config, now, age))
	// Rollback is a separate explicit operation, with crypto/engine/age verification.
	requireSuccess(t, store.Rollback(ctx, now, age))
	if err = store.Update(ctx, config, now, age); err == nil {
		t.Fatal("rejected generation silently returned")
	}
}

func serveCorruptCLD(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, ".cld") {
		_, _ = io.WriteString(w, "invalid controlled CLD")
		return
	}
	http.NotFound(w, r)
}
func serveOfficialCVD(w http.ResponseWriter, r *http.Request, allowed map[string]bool, advanced bool) {
	if !allowed[strings.TrimPrefix(r.URL.Path, "/")] {
		http.NotFound(w, r)
		return
	}
	dir := "/var/lib/clamav"
	if advanced {
		dir = "/test/next-databases"
	}
	http.FileServer(http.Dir(dir)).ServeHTTP(w, r)
}
