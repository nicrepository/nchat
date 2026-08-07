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

	// integrationKeyID is the active key id every row this suite writes carries.
	integrationKeyID = "kek-integration"
)

// integrationEnv holds the live dependencies plus the identifiers this run owns.
type integrationEnv struct {
	pool *pgxpool.Pool
	// store serves the request paths; lifecycle is the same store wired with
	// the attachment fence, which the invalidations require and the read and
	// upload paths do not.
	store       *storage.PGXAttachmentStore
	lifecycle   *storage.PGXAttachmentStore
	fence       storage.AttachmentFencing
	objects     *storage.SeaweedFSStore
	keys        *crypto.Keyring
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

	lockPool, ok := storage.LockConnPoolFrom(pool)
	if !ok {
		t.Fatal("the integration pool must be able to lend lock connections")
	}
	txPool, ok := storage.TransactionPoolFrom(pool)
	if !ok {
		t.Fatal("the integration pool must be able to open transactions")
	}
	fence := storage.NewPGXAttachmentFence(lockPool, txPool, discardLogger())

	env := &integrationEnv{
		pool:        pool,
		fence:       fence,
		store:       storage.NewPGXAttachmentStore(pool),
		lifecycle:   storage.NewFencedAttachmentStore(pool, fence),
		objects:     objects,
		keys:        integrationKeyring(t),
		workspaceID: uuid.NewString(),
		channelID:   uuid.NewString(),
		uploaderID:  uuid.NewString(),
	}
	// Every row this run writes carries the workspace it invented, so cleanup
	// can never touch another run's data.
	t.Cleanup(func() { env.cleanup(t) })
	return env
}

func integrationKeyring(t *testing.T) *crypto.Keyring {
	t.Helper()
	key := make([]byte, crypto.KeySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		t.Fatalf("generate key: %v", err)
	}
	ring, err := crypto.NewKeyring(
		integrationKeyID, base64.StdEncoding.EncodeToString(key), "",
	)
	if err != nil {
		t.Fatalf("build keyring: %v", err)
	}
	return ring
}

// cleanup removes this run's rows and any object they still point at.
//
// Both objects are collected: an attachment's own, and the preview a job may
// have produced from it. The preview key is derived rather than stored, so it
// is rebuilt from the id the row carries.
func (e *integrationEnv) cleanup(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	rows, err := e.pool.Query(ctx,
		`SELECT storage_object_key, preview_object_id::text
		   FROM files.attachments WHERE workspace_id = $1`, e.workspaceID)
	if err == nil {
		var keys []string
		for rows.Next() {
			var (
				key       string
				previewID *string
			)
			if scanErr := rows.Scan(&key, &previewID); scanErr == nil {
				keys = append(keys, key)
				if previewID != nil {
					if parsed, parseErr := uuid.Parse(*previewID); parseErr == nil {
						keys = append(keys, domain.PreviewObjectKey(parsed))
					}
				}
			}
		}
		rows.Close()
		for _, key := range keys {
			_ = e.objects.Delete(ctx, key)
		}
	}
	// The cleanup queue is not scoped to a workspace — a job is only an object
	// key — so this run's jobs are removed by matching the keys its own rows
	// produced. Without it, jobs accumulate across runs and a later test that
	// reads the queue sees another test's leftovers.
	if _, err := e.pool.Exec(ctx, `
		DELETE FROM files.object_cleanup_jobs
		 WHERE object_key IN (
			SELECT 'nchat/previews/' || preview_object_id::text
			  FROM files.attachments
			 WHERE workspace_id = $1 AND preview_object_id IS NOT NULL
		 )`, e.workspaceID); err != nil {
		t.Errorf("cleanup queue: %v", err)
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
		authorizer, e.store, objects, e.keys,
		domain.DefaultMaxUploadBytes, true, &countingOrphans{}, discardLogger(),
	)
}

func (e *integrationEnv) upload(
	t *testing.T, svc *service.AttachmentService, body string,
) (service.AttachmentView, error) {
	t.Helper()
	return e.uploadContent(t, svc, strings.NewReader(body), "integration.bin")
}

// uploadContent runs the production sequence: authorise the destination without
// reading anything, then stream into the authorised target.
func (e *integrationEnv) uploadContent(
	t *testing.T, svc *service.AttachmentService, content io.Reader, filename string,
) (service.AttachmentView, error) {
	t.Helper()
	target, err := svc.AuthorizeUpload(context.Background(), service.AuthorizeUploadInput{
		Destination: domain.Destination{Kind: domain.DestinationKindChannel, ID: e.channelID},
		UserID:      e.uploaderID,
		SessionID:   uuid.NewString(),
	})
	if err != nil {
		return service.AttachmentView{}, err
	}
	return svc.Upload(context.Background(), service.UploadInput{
		Target:       target,
		Filename:     filename,
		DeclaredMIME: "application/octet-stream",
		Content:      content,
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
	//
	// The conflicting state has to be 'failed' rather than 'rejected': since
	// migration 000002, attachments_dek_binding_complete_check requires the whole
	// key binding in every state an upload can finish in, and this row has none
	// yet. 'failed' is the one terminal state that legitimately never has it, so
	// it is the only flip that a row still mid-upload can actually take.
	trigger := &afterReadHook{
		Reader: strings.NewReader(strings.Repeat("finalize-conflict-payload ", 64)),
		hook: func() {
			if _, err := e.pool.Exec(context.Background(),
				`UPDATE files.attachments SET status = 'failed'
				  WHERE workspace_id = $1 AND status = 'pending_upload'`, e.workspaceID); err != nil {
				t.Errorf("stage the finalisation conflict: %v", err)
			}
		},
	}
	view, err := e.uploadContent(t, svc, trigger, "conflict.bin")
	// Put the row back so the assertions describe the compensation, not the
	// artificial conflict that triggered it.
	if _, restoreErr := e.pool.Exec(context.Background(),
		`UPDATE files.attachments SET status = 'pending_upload'
		  WHERE workspace_id = $1 AND status = 'failed'`, e.workspaceID); restoreErr != nil {
		t.Fatalf("restore the pending row: %v", restoreErr)
	}
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
		// A complete binding and a valid initial preview state, so the guard
		// being exercised is the SQL one on the previous state and not one of
		// the caller-side completeness checks.
		WrappedDEK: []byte{1, 2, 3}, KEKKeyID: integrationKeyID,
		PreviewStatus: domain.PreviewStatusUnsupported,
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
			storage_provider, storage_object_key, envelope_version, dek_wrap_version, status
		) VALUES ($1, $2, $3, 'channel', $4, $5, 'x.bin', 'application/octet-stream',
		          'seaweedfs', $6, 1, $7, 'pending_upload')`,
		attachmentID, env.workspaceID, env.uploaderID,
		env.channelID, uuid.NewString(), "nchat/attachments/"+attachmentID,
		// Supplied so the exclusivity CHECK is what fires: without it the row
		// would be rejected earlier by the dek_wrap_version fence, and this test
		// would stop covering the constraint it is about.
		crypto.KeyWrapVersion,
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

// The security regression, against real infrastructure: the size_bytes a real
// PostgreSQL row carries is the size the real wrapped key authenticates, so an
// attacker who edits that column cannot make the key open.
//
// The download use case cannot be driven here — GetAuthorized needs seeded
// auth.user_sessions and chat tables, which this suite deliberately avoids — so
// the check is made where the tampering would have to land: the persisted
// binding, read straight back out of the database and handed to the same key
// ring the service uses.
func TestIntegrationPersistedBindingAuthenticatesTheStoredSize(t *testing.T) {
	env := newIntegrationEnv(t)
	svc := env.newService(env.objects)
	payload := strings.Repeat("size-binding-payload ", 512)

	view, err := env.upload(t, svc, payload)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}

	var (
		wrappedDEK  []byte
		keyID       string
		wrapVersion int
		size        int64
	)
	if err := env.pool.QueryRow(context.Background(), `
		SELECT wrapped_dek, kek_key_id, dek_wrap_version, size_bytes
		  FROM files.attachments WHERE id = $1`, view.ID,
	).Scan(&wrappedDEK, &keyID, &wrapVersion, &size); err != nil {
		t.Fatalf("read the persisted binding: %v", err)
	}
	if len(wrappedDEK) == 0 || keyID != integrationKeyID || wrapVersion != crypto.KeyWrapVersion {
		t.Fatalf("incomplete persisted binding: key=%d bytes id=%q version=%d",
			len(wrappedDEK), keyID, wrapVersion)
	}
	if size != int64(len(payload)) {
		t.Fatalf("expected the counted size %d, got %d", len(payload), size)
	}

	binding := crypto.Binding{
		AttachmentID:           uuid.MustParse(view.ID),
		WorkspaceID:            uuid.MustParse(env.workspaceID),
		PlaintextSize:          size,
		KeyWrapVersion:         wrapVersion,
		ContentEnvelopeVersion: crypto.EnvelopeVersion,
	}
	if _, err := env.keys.Unwrap(wrappedDEK, keyID, binding); err != nil {
		t.Fatalf("the persisted binding must open with the persisted size: %v", err)
	}

	// Every edit an attacker with write access to the row could make.
	for name, tampered := range map[string]int64{
		"halved":        size / 2,
		"one byte less": size - 1,
		"one byte more": size + 1,
		"zero":          0,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := env.keys.Unwrap(wrappedDEK, keyID, withPlaintextSize(binding, tampered)); err == nil {
				t.Fatalf("size %d must not open the key", tampered)
			}
		})
	}
}

func withPlaintextSize(b crypto.Binding, size int64) crypto.Binding {
	b.PlaintextSize = size
	return b
}
