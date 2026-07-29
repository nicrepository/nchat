package service_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nicrepository/nchat/services/file-service/internal/crypto"
	"github.com/nicrepository/nchat/services/file-service/internal/domain"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
	"github.com/nicrepository/nchat/services/file-service/internal/storage"
)

// Opt-in integration coverage for the upload pipeline against real
// infrastructure: the SQL in PGXAttachmentStore runs against PostgreSQL with
// the files migration applied, and SeaweedFSStore talks to a real filer.
//
// Only the authorization decision is substituted, because it is not what these
// cases are about — the destination policy has its own tests in
// internal/storage — and seeding auth.users, auth.user_sessions and the chat
// tables would add fixture that could break for reasons unrelated to the
// upload lifecycle.
//
// Run with:
//
//	make dev-env-up
//	MIGRATIONS_DATABASE_URL='postgresql://nchat:<password>@localhost:5432/nchat_test?sslmode=disable' pnpm migrations:up
//	FILE_TEST_DATABASE_URL='postgresql://nchat:<password>@localhost:5432/nchat_test?sslmode=disable' \
//	FILE_TEST_SEAWEEDFS_URL='http://localhost:8888' \
//	  go test ./services/file-service/internal/service/ -run Integration -v
//
// Both variables are required; the suite skips otherwise, so the default test
// run never depends on external services.

const (
	testDatabaseURLEnv  = "FILE_TEST_DATABASE_URL"
	testSeaweedFSURLEnv = "FILE_TEST_SEAWEEDFS_URL"
)

// integrationEnv holds the live dependencies plus the identifiers this run owns.
type integrationEnv struct {
	pool        *pgxpool.Pool
	store       *storage.PGXAttachmentStore
	objects     *storage.SeaweedFSStore
	kek         *crypto.KeyEncryptionKey
	workspaceID string
	channelID   string
	uploaderID  string
}

// newIntegrationEnv connects to the live services and registers cleanup for
// everything this run creates.
func newIntegrationEnv(t *testing.T) *integrationEnv {
	t.Helper()

	dsn := os.Getenv(testDatabaseURLEnv)
	filerURL := os.Getenv(testSeaweedFSURLEnv)
	if dsn == "" || filerURL == "" {
		t.Skipf("%s and %s must both be set", testDatabaseURLEnv, testSeaweedFSURLEnv)
	}
	// Same guard the chat-service integration suites use: never point a
	// destructive test at a database that is not obviously disposable.
	if !strings.Contains(dsn, "_test") {
		t.Fatalf("%s must point at a *_test database", testDatabaseURLEnv)
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		t.Skipf("PostgreSQL at %s is not reachable: %v", testDatabaseURLEnv, err)
	}
	t.Cleanup(pool.Close)

	objects, err := storage.NewSeaweedFSStore(filerURL, 15*time.Second)
	if err != nil {
		t.Fatalf("build storage client: %v", err)
	}
	pingStorageCtx, cancelStorage := context.WithTimeout(ctx, 5*time.Second)
	defer cancelStorage()
	if err := objects.Ping(pingStorageCtx); err != nil {
		t.Skipf("SeaweedFS at %s is not reachable: %v", testSeaweedFSURLEnv, err)
	}

	env := &integrationEnv{
		pool:        pool,
		store:       storage.NewPGXAttachmentStore(pool),
		objects:     objects,
		kek:         integrationKEK(t),
		workspaceID: uuid.NewString(),
		channelID:   uuid.NewString(),
		uploaderID:  uuid.NewString(),
	}
	// Every row this run writes carries the workspace it invented, so cleanup
	// can never touch another run's data.
	t.Cleanup(func() { env.cleanup(t) })
	return env
}

func integrationKEK(t *testing.T) *crypto.KeyEncryptionKey {
	t.Helper()
	key := make([]byte, crypto.KeySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		t.Fatalf("generate key: %v", err)
	}
	kek, err := crypto.NewKeyEncryptionKey(base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatalf("build kek: %v", err)
	}
	return kek
}

// cleanup removes this run's rows and any object they still point at.
func (e *integrationEnv) cleanup(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	rows, err := e.pool.Query(ctx,
		`SELECT storage_object_key FROM files.attachments WHERE workspace_id = $1`, e.workspaceID)
	if err == nil {
		var keys []string
		for rows.Next() {
			var key string
			if scanErr := rows.Scan(&key); scanErr == nil {
				keys = append(keys, key)
			}
		}
		rows.Close()
		for _, key := range keys {
			_ = e.objects.Delete(ctx, key)
		}
	}
	if _, err := e.pool.Exec(ctx,
		`DELETE FROM files.attachments WHERE workspace_id = $1`, e.workspaceID); err != nil {
		t.Errorf("cleanup rows: %v", err)
	}
}

// newService wires the real store and the supplied object store.
func (e *integrationEnv) newService(objects service.ObjectStore) *service.AttachmentService {
	authorizer := &fakeAuthorizer{result: service.AuthorizedDestination{
		ID:               e.channelID,
		WorkspaceID:      e.workspaceID,
		SessionExpiresAt: time.Now().Add(time.Hour),
	}}
	return service.NewAttachmentService(
		authorizer, e.store, objects, e.kek,
		domain.DefaultMaxUploadBytes, true, &countingOrphans{}, discardLogger(),
	)
}

func (e *integrationEnv) upload(
	t *testing.T, svc *service.AttachmentService, body string,
) (service.AttachmentView, error) {
	t.Helper()
	return svc.Upload(context.Background(), service.UploadInput{
		Destination:  domain.Destination{Kind: domain.DestinationKindChannel, ID: e.channelID},
		UserID:       e.uploaderID,
		SessionID:    uuid.NewString(),
		Filename:     "integration.bin",
		DeclaredMIME: "application/octet-stream",
		Content:      strings.NewReader(body),
	})
}

// row reads the persisted state straight from PostgreSQL.
func (e *integrationEnv) row(t *testing.T, attachmentID string) (status, objectKey string) {
	t.Helper()
	err := e.pool.QueryRow(context.Background(),
		`SELECT status, storage_object_key FROM files.attachments WHERE id = $1`,
		attachmentID,
	).Scan(&status, &objectKey)
	if err != nil {
		t.Fatalf("read row %s: %v", attachmentID, err)
	}
	return status, objectKey
}

// pendingIDs replays idx_attachments_pending: the query an operator would run
// to find rows that still need reconciliation.
func (e *integrationEnv) pendingIDs(t *testing.T) []string {
	t.Helper()
	rows, err := e.pool.Query(context.Background(),
		`SELECT id::text FROM files.attachments
		  WHERE workspace_id = $1 AND status IN ('pending_upload', 'pending_scan')
		  ORDER BY created_at`, e.workspaceID)
	if err != nil {
		t.Fatalf("query pending: %v", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan pending: %v", err)
		}
		ids = append(ids, id)
	}
	return ids
}

func (e *integrationEnv) objectExists(t *testing.T, key string) bool {
	t.Helper()
	body, err := e.objects.Open(context.Background(), key)
	if errors.Is(err, domain.ErrNotFound) {
		return false
	}
	if err != nil {
		t.Fatalf("open object %s: %v", key, err)
	}
	defer func() { _ = body.Close() }()
	_, _ = io.Copy(io.Discard, body)
	return true
}

// brokenDeleteStore writes through to the real storage but always fails the
// delete, which is the exact partial failure the compensation must survive.
type brokenDeleteStore struct {
	service.ObjectStore
	deleteErr error
	deletes   int
}

func (s *brokenDeleteStore) Delete(context.Context, string) error {
	s.deletes++
	return s.deleteErr
}

func TestIntegrationUploadPersistsRowAndEncryptedObject(t *testing.T) {
	env := newIntegrationEnv(t)
	svc := env.newService(env.objects)
	payload := "integration payload " + uuid.NewString()

	view, err := env.upload(t, svc, payload)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}

	status, objectKey := env.row(t, view.ID)
	if status != string(domain.StatusPendingScan) {
		t.Fatalf("expected pending_scan, got %q", status)
	}
	if objectKey != domain.StorageObjectKey(uuid.MustParse(view.ID)) {
		t.Fatalf("unexpected storage key %q", objectKey)
	}

	// The object really is in SeaweedFS, and it is ciphertext.
	body, err := env.objects.Open(context.Background(), objectKey)
	if err != nil {
		t.Fatalf("open stored object: %v", err)
	}
	defer func() { _ = body.Close() }()
	stored, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read stored object: %v", err)
	}
	if bytes.Contains(stored, []byte(payload)) {
		t.Fatal("the stored object must not contain the plaintext")
	}
	if int64(len(stored)) != crypto.CiphertextSize(int64(len(payload))) {
		t.Fatalf("expected %d ciphertext bytes, got %d",
			crypto.CiphertextSize(int64(len(payload))), len(stored))
	}
}

// The regression case, end to end: the object is really in SeaweedFS, cleanup
// really fails, and the row must stay in the queue an operator can find.
func TestIntegrationFailedCleanupLeavesARecoverableRow(t *testing.T) {
	env := newIntegrationEnv(t)
	broken := &brokenDeleteStore{
		ObjectStore: env.objects,
		deleteErr:   errors.New("storage delete unavailable"),
	}
	svc := env.newService(broken)

	// Make the finalising update fail after the object is durable by deleting
	// the pending row's guard: a second service instance sharing the same store
	// is not needed — MarkUploaded only advances a pending_upload row, so
	// flipping the row out of that state first is enough.
	view, err := env.uploadWithFinalizeConflict(t, svc)
	if err == nil {
		t.Fatal("expected the upload to fail")
	}
	if view.ID != "" {
		t.Fatalf("no view may be returned on failure, got %+v", view)
	}

	attachmentID := env.onlyRowID(t)
	status, objectKey := env.row(t, attachmentID)
	if status != string(domain.StatusPendingUpload) {
		t.Fatalf("expected the row to stay pending_upload, got %q", status)
	}
	if pending := env.pendingIDs(t); len(pending) != 1 || pending[0] != attachmentID {
		t.Fatalf("the row must stay in the pending index, got %v", pending)
	}
	if broken.deletes != 1 {
		t.Fatalf("expected exactly one delete attempt, got %d", broken.deletes)
	}
	if !env.objectExists(t, objectKey) {
		t.Fatal("the object must still be in storage: that is what makes it an orphan")
	}
	if !errors.Is(err, broken.deleteErr) {
		t.Fatalf("the cleanup failure must be preserved, got %v", err)
	}
}

// uploadWithFinalizeConflict drives an upload whose finalising update cannot
// apply, by racing the row out of pending_upload while the object is written.
func (e *integrationEnv) uploadWithFinalizeConflict(
	t *testing.T, svc *service.AttachmentService,
) (service.AttachmentView, error) {
	t.Helper()
	// The body must be longer than the content-detection window, so it is still
	// being consumed while the object is written — that is, after the row
	// exists. Flipping the row's status there makes MarkUploaded find nothing
	// pending, which is the failure that triggers compensation.
	trigger := &afterReadHook{
		Reader: strings.NewReader(strings.Repeat("finalize-conflict-payload ", 64)),
		hook: func() {
			_, _ = e.pool.Exec(context.Background(),
				`UPDATE files.attachments SET status = 'rejected'
				  WHERE workspace_id = $1 AND status = 'pending_upload'`, e.workspaceID)
		},
	}
	view, err := svc.Upload(context.Background(), service.UploadInput{
		Destination:  domain.Destination{Kind: domain.DestinationKindChannel, ID: e.channelID},
		UserID:       e.uploaderID,
		SessionID:    uuid.NewString(),
		Filename:     "conflict.bin",
		DeclaredMIME: "application/octet-stream",
		Content:      trigger,
	})
	// Put the row back so the assertions describe the compensation, not the
	// artificial conflict that triggered it.
	_, _ = e.pool.Exec(context.Background(),
		`UPDATE files.attachments SET status = 'pending_upload'
		  WHERE workspace_id = $1 AND status = 'rejected'`, e.workspaceID)
	return view, err
}

func (e *integrationEnv) onlyRowID(t *testing.T) string {
	t.Helper()
	rows, err := e.pool.Query(context.Background(),
		`SELECT id::text FROM files.attachments WHERE workspace_id = $1`, e.workspaceID)
	if err != nil {
		t.Fatalf("query rows: %v", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		ids = append(ids, id)
	}
	if len(ids) != 1 {
		t.Fatalf("expected exactly one row, got %d", len(ids))
	}
	return ids[0]
}

// afterReadHook runs a hook once the wrapped reader is exhausted.
type afterReadHook struct {
	io.Reader
	hook func()
	done bool
}

func (r *afterReadHook) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if errors.Is(err, io.EOF) && !r.done {
		r.done = true
		r.hook()
	}
	return n, err
}

// A successful cleanup must leave nothing behind in either system.
func TestIntegrationSuccessfulCleanupRemovesTheObjectAndFailsTheRow(t *testing.T) {
	env := newIntegrationEnv(t)
	// A storage endpoint that accepts the write and then reports a failure for
	// it, so compensation runs with a working delete.
	failing := &failingPutStore{ObjectStore: env.objects, err: domain.ErrUnavailable}
	svc := env.newService(failing)

	if _, err := env.upload(t, svc, "payload"); err == nil {
		t.Fatal("expected the upload to fail")
	}

	attachmentID := env.onlyRowID(t)
	status, objectKey := env.row(t, attachmentID)
	if status != string(domain.StatusFailed) {
		t.Fatalf("expected the row to be failed, got %q", status)
	}
	if env.objectExists(t, objectKey) {
		t.Fatal("a successful cleanup must remove the object")
	}
	if pending := env.pendingIDs(t); len(pending) != 0 {
		t.Fatalf("a cleaned-up row must leave the pending index, got %v", pending)
	}
}

// failingPutStore writes through and then reports the write as failed, which is
// how a storage error is observed after bytes may already have landed.
type failingPutStore struct {
	service.ObjectStore
	err error
}

func (s *failingPutStore) Put(ctx context.Context, key string, body io.Reader) (int64, error) {
	if _, err := s.ObjectStore.Put(ctx, key, body); err != nil {
		return 0, err
	}
	return 0, s.err
}

// The store's own SQL guards are exercised here rather than against a fake.
func TestIntegrationMarkUploadedOnlyAdvancesPendingRows(t *testing.T) {
	env := newIntegrationEnv(t)
	svc := env.newService(env.objects)

	view, err := env.upload(t, svc, "payload")
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	// The row is already pending_scan, so a second finalisation must not apply.
	err = env.store.MarkUploaded(context.Background(), service.UploadedAttachment{
		ID: view.ID, Status: domain.StatusClean, DetectedMIME: "text/plain",
		Size: 1, CiphertextSize: 1,
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for a non-pending row, got %v", err)
	}
	if status, _ := env.row(t, view.ID); status != string(domain.StatusPendingScan) {
		t.Fatalf("the row must be unchanged, got %q", status)
	}
}

// A hard guarantee of the schema, asserted against the real CHECK constraints.
func TestIntegrationDestinationExclusivityIsEnforcedByTheDatabase(t *testing.T) {
	env := newIntegrationEnv(t)
	attachmentID := uuid.NewString()

	_, err := env.pool.Exec(context.Background(), `
		INSERT INTO files.attachments (
			id, workspace_id, uploader_id, destination_kind,
			channel_id, conversation_id, original_filename, declared_mime,
			storage_provider, storage_object_key, envelope_version, wrapped_dek, status
		) VALUES ($1, $2, $3, 'channel', $4, $5, 'x.bin', 'application/octet-stream',
		          'seaweedfs', $6, 1, '\x00', 'pending_upload')`,
		attachmentID, env.workspaceID, env.uploaderID,
		env.channelID, uuid.NewString(), "nchat/attachments/"+attachmentID,
	)
	if err == nil {
		t.Fatal("the database must reject a row naming both destinations")
	}
	if !strings.Contains(err.Error(), "attachments_destination_exclusive_check") {
		t.Fatalf("expected the exclusivity constraint to fire, got %v", err)
	}
}

// Guards that the suite really is opt-in: without the variables it skips
// instead of failing, which is what keeps the default run hermetic.
func TestIntegrationSuiteIsOptIn(t *testing.T) {
	if os.Getenv(testDatabaseURLEnv) == "" || os.Getenv(testSeaweedFSURLEnv) == "" {
		t.Skip("integration variables are not set, which is the opt-out path")
	}
	// When they are set, the environment must actually be usable.
	newIntegrationEnv(t)
}
