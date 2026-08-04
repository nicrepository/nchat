package service_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"io"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/file-service/internal/crypto"
	"github.com/nicrepository/nchat/services/file-service/internal/domain"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
)

const (
	testUserID       = "22222222-2222-4222-8222-222222222222"
	testSessionID    = "11111111-1111-4111-8111-111111111111"
	testChannelID    = "33333333-3333-4333-8333-333333333333"
	testConversation = "66666666-6666-4666-8666-666666666666"
	testWorkspaceID  = "44444444-4444-4444-8444-444444444444"
)

// testKeyID is the active key id the fixture's key ring uses; it is what the
// service must persist on every row it creates.
const testKeyID = "kek-test-active"

func testMasterKey(t *testing.T) string {
	t.Helper()
	key := make([]byte, crypto.KeySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return base64.StdEncoding.EncodeToString(key)
}

func testKeyring(t *testing.T) *crypto.Keyring {
	t.Helper()
	ring, err := crypto.NewKeyring(testKeyID, testMasterKey(t), "")
	if err != nil {
		t.Fatalf("build keyring: %v", err)
	}
	return ring
}

// fakeAuthorizer answers the destination question with a scripted result.
type fakeAuthorizer struct {
	result service.AuthorizedDestination
	err    error
	calls  []service.DestinationAuthInput
}

func (a *fakeAuthorizer) AuthorizeDestination(
	_ context.Context, input service.DestinationAuthInput,
) (service.AuthorizedDestination, error) {
	a.calls = append(a.calls, input)
	if a.err != nil {
		return service.AuthorizedDestination{}, a.err
	}
	return a.result, nil
}

// attachmentRow is the persisted state of one row. The fake keeps real state
// rather than a call log so tests can assert what an operator would find in the
// database, not merely which methods were invoked.
type attachmentRow struct {
	status      domain.Status
	failureCode string
}

// fakeStore models files.attachments closely enough to assert the invariants
// the compensation depends on, including which rows the pending index covers.
type fakeStore struct {
	mu sync.Mutex

	rows map[string]*attachmentRow

	created  []service.NewAttachment
	uploaded []service.UploadedAttachment

	createErr   error
	uploadErr   error
	markFailErr error

	markFailedCalls int

	authorized    service.StoredAttachment
	authorizedErr error

	listed     []service.ListedAttachment
	listErr    error
	listQuery  service.ListDestinationAttachmentsQuery
	listCalled int
}

func (s *fakeStore) ListDestinationAttachments(
	_ context.Context, query service.ListDestinationAttachmentsQuery,
) ([]service.ListedAttachment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listCalled++
	s.listQuery = query
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.listed, nil
}

func newFakeStore() *fakeStore {
	return &fakeStore{rows: map[string]*attachmentRow{}}
}

func (s *fakeStore) CreatePending(_ context.Context, attachment service.NewAttachment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.createErr != nil {
		return s.createErr
	}
	s.created = append(s.created, attachment)
	s.rows[attachment.ID] = &attachmentRow{status: domain.StatusPendingUpload}
	return nil
}

func (s *fakeStore) MarkUploaded(_ context.Context, update service.UploadedAttachment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.uploadErr != nil {
		return s.uploadErr
	}
	row, ok := s.rows[update.ID]
	if !ok || row.status != domain.StatusPendingUpload {
		return domain.ErrNotFound
	}
	row.status = update.Status
	s.uploaded = append(s.uploaded, update)
	return nil
}

func (s *fakeStore) MarkFailed(_ context.Context, attachmentID, failureCode string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.markFailedCalls++
	if s.markFailErr != nil {
		return s.markFailErr
	}
	// Mirrors the store: only a pending row transitions, like the SQL guard.
	if row, ok := s.rows[attachmentID]; ok && row.status == domain.StatusPendingUpload {
		row.status = domain.StatusFailed
		row.failureCode = failureCode
	}
	return nil
}

func (s *fakeStore) GetAuthorized(
	_ context.Context, _ service.AttachmentAuthInput,
) (service.StoredAttachment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.authorizedErr != nil {
		return service.StoredAttachment{}, s.authorizedErr
	}
	return s.authorized, nil
}

// statusOf returns the persisted state of the only row, failing the test when
// the row count is not exactly one.
func (s *fakeStore) statusOf(t *testing.T, attachmentID string) domain.Status {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.rows[attachmentID]
	if !ok {
		t.Fatalf("no row persisted for %s", attachmentID)
	}
	return row.status
}

// onlyRowID returns the id of the single persisted row.
func (s *fakeStore) onlyRowID(t *testing.T) string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.rows) != 1 {
		t.Fatalf("expected exactly one persisted row, got %d", len(s.rows))
	}
	for id := range s.rows {
		return id
	}
	return ""
}

// recoverable replays the predicate of idx_attachments_pending, so a test can
// assert that a row is still reachable by the operational sweep query rather
// than only that a metric moved.
func (s *fakeStore) recoverable() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []string
	for id, row := range s.rows {
		if row.status == domain.StatusPendingUpload || row.status == domain.StatusPendingScan {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

func (s *fakeStore) markFailedCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.markFailedCalls
}

func (s *fakeStore) snapshot() ([]service.NewAttachment, []service.UploadedAttachment, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]service.NewAttachment(nil), s.created...),
		append([]service.UploadedAttachment(nil), s.uploaded...),
		s.markFailedCalls
}

// fakeObjects is an in-memory object store that can be told to fail.
type fakeObjects struct {
	mu sync.Mutex

	objects map[string][]byte
	deleted []string

	putErr    error
	openErr   error
	deleteErr error

	// shortPutBy makes Put report fewer bytes than it stored, which is how a
	// store that accepted an incomplete envelope would look to the service.
	shortPutBy int64
	// opens counts Open calls, so a test can assert that a download that failed
	// on metadata never reached storage at all.
	opens int
}

func newFakeObjects() *fakeObjects {
	return &fakeObjects{objects: map[string][]byte{}}
}

func (o *fakeObjects) Put(_ context.Context, key string, body io.Reader) (int64, error) {
	// Always drain the body: the real client streams it, so a source failure
	// must surface here exactly as it would over the wire.
	content, readErr := io.ReadAll(body)
	o.mu.Lock()
	defer o.mu.Unlock()
	if readErr != nil {
		return 0, readErr
	}
	if o.putErr != nil {
		return 0, o.putErr
	}
	o.objects[key] = content
	return int64(len(content)) - o.shortPutBy, nil
}

func (o *fakeObjects) Open(_ context.Context, key string) (io.ReadCloser, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.opens++
	if o.openErr != nil {
		return nil, o.openErr
	}
	content, ok := o.objects[key]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(content)), nil
}

func (o *fakeObjects) Delete(_ context.Context, key string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.deleted = append(o.deleted, key)
	if o.deleteErr != nil {
		return o.deleteErr
	}
	delete(o.objects, key)
	return nil
}

func (o *fakeObjects) openCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.opens
}

func (o *fakeObjects) count() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.objects)
}

func (o *fakeObjects) deletedKeys() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.deleted...)
}

func (o *fakeObjects) only(t *testing.T) (string, []byte) {
	t.Helper()
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.objects) != 1 {
		t.Fatalf("expected exactly one stored object, got %d", len(o.objects))
	}
	for key, content := range o.objects {
		return key, content
	}
	return "", nil
}

func (o *fakeObjects) replace(key string, content []byte) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.objects[key] = content
}

// countingOrphans records the orphan metric the service emits.
type countingOrphans struct {
	mu    sync.Mutex
	count int
}

func (c *countingOrphans) ObserveOrphanedObject() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.count++
}

func (c *countingOrphans) value() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.count
}

// fixture wires a service with in-memory dependencies and real crypto.
type fixture struct {
	authorizer *fakeAuthorizer
	store      *fakeStore
	objects    *fakeObjects
	orphans    *countingOrphans
	keys       *crypto.Keyring
	service    *service.AttachmentService
}

type fixtureOptions struct {
	maxUploadBytes int64
	scanRequired   bool
}

func newFixture(t *testing.T, options ...fixtureOptions) *fixture {
	t.Helper()
	opts := fixtureOptions{maxUploadBytes: domain.DefaultMaxUploadBytes, scanRequired: true}
	if len(options) > 0 {
		opts = options[0]
		if opts.maxUploadBytes == 0 {
			opts.maxUploadBytes = domain.DefaultMaxUploadBytes
		}
	}

	f := &fixture{
		authorizer: &fakeAuthorizer{result: service.AuthorizedDestination{
			ID:               testChannelID,
			WorkspaceID:      testWorkspaceID,
			SessionExpiresAt: time.Now().Add(time.Hour),
		}},
		store:   newFakeStore(),
		objects: newFakeObjects(),
		orphans: &countingOrphans{},
		keys:    testKeyring(t),
	}
	f.service = service.NewAttachmentService(
		f.authorizer, f.store, f.objects, f.keys,
		opts.maxUploadBytes, opts.scanRequired, f.orphans, discardLogger(),
	)
	return f
}

// upload runs the two production steps in the production order: authorise the
// destination without touching the body, then stream into the authorised
// target. Tests exercise the same sequence the handler does.
func (f *fixture) upload(ctx context.Context, content io.Reader, filename string) (service.AttachmentView, error) {
	return f.uploadTo(ctx, domain.Destination{Kind: domain.DestinationKindChannel, ID: testChannelID},
		content, filename, "application/octet-stream")
}

func (f *fixture) uploadTo(
	ctx context.Context, destination domain.Destination,
	content io.Reader, filename, declaredMIME string,
) (service.AttachmentView, error) {
	target, err := f.service.AuthorizeUpload(ctx, service.AuthorizeUploadInput{
		Destination: destination,
		UserID:      testUserID,
		SessionID:   testSessionID,
	})
	if err != nil {
		return service.AttachmentView{}, err
	}
	return f.service.Upload(ctx, service.UploadInput{
		Target:       target,
		Filename:     filename,
		DeclaredMIME: declaredMIME,
		Content:      content,
	})
}

type failingReader struct {
	data []byte
	err  error
	read int
}

func (r *failingReader) Read(p []byte) (int, error) {
	if r.read >= len(r.data) {
		return 0, r.err
	}
	n := copy(p, r.data[r.read:])
	r.read += n
	return n, nil
}
