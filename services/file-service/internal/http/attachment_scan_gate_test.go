package httpapi_test

// Regression tests for the antimalware delivery gate (RF-22).
//
// # Why these exist next to the handler tests that already cover the statuses
//
// The other tests in this package drive a fake AttachmentUseCases: they hand the
// handler a domain error and assert the status it becomes. That proves the
// *mapping* and nothing about the gate — a build that deleted the scan check
// from the service would still pass every one of them, because the fake would
// happily return content for an attachment nobody scanned.
//
// These wire the real router to the real AttachmentService, so the whole
// delivery path is exercised end to end: authenticate, re-authorise, consult the
// persisted status, unwrap the key, open storage, stream. The attachment is
// uploaded through the service's own upload path, so the row and the encrypted
// object are the ones production would have produced, and only its status is
// moved by the test.
//
// The scenario is the bypass the requirement names: the attachment exists, the
// caller has legitimate access to it, the scan has not approved it, and the
// caller skips the UI and calls the download URL directly.

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"crypto/rand"
	"encoding/base64"

	"github.com/google/uuid"
	platformlog "github.com/nicrepository/nchat/libs/go/platform/log"
	"github.com/nicrepository/nchat/services/file-service/internal/crypto"
	"github.com/nicrepository/nchat/services/file-service/internal/domain"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
)

const (
	gateWorkspaceID = "44444444-4444-4444-8444-444444444444"
	gateKeyID       = "kek-scan-gate"
)

// gatePayload is long enough that a partial body would be obvious and that a
// byte range is a genuine slice rather than the whole file.
var gatePayload = []byte(strings.Repeat("attachment-plaintext-", 64))

// --- in-memory dependencies ---------------------------------------------

// gateAuthorizer always authorises. That is deliberate: these tests are about
// the scan gate, and the point is that a caller who *is* authorised still gets
// nothing. Authorization is refused by gateStore.authErr instead, so the two
// controls can be exercised independently.
type gateAuthorizer struct{}

func (gateAuthorizer) AuthorizeDestination(
	_ context.Context, _ service.DestinationAuthInput,
) (service.AuthorizedDestination, error) {
	return service.AuthorizedDestination{
		ID:             testChannelID,
		WorkspaceID:    gateWorkspaceID,
		MaxUploadBytes: domain.DefaultMaxUploadBytes,
	}, nil
}

// gateStore keeps the single row the upload created and hands it back to
// GetAuthorized, so the download reads exactly what the upload wrote. status is
// what the test moves; authErr models a caller who may not see the row at all.
type gateStore struct {
	mu sync.Mutex

	pending  service.NewAttachment
	uploaded service.UploadedAttachment
	status   domain.Status
	authErr  error

	// preview is the preview half of the row, empty unless the test published
	// one. The database CHECK keeps these columns empty unless previewStatus is
	// ready, and this fake keeps them together for the same reason.
	preview service.StoredAttachment
	// pages models files.attachment_preview_pages, keyed by page number.
	pages map[int]service.PreviewPage
}

func (s *gateStore) CreatePending(_ context.Context, attachment service.NewAttachment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending = attachment
	s.status = domain.StatusPendingUpload
	return nil
}

func (s *gateStore) MarkUploaded(_ context.Context, update service.UploadedAttachment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.uploaded = update
	s.status = update.Status
	return nil
}

func (s *gateStore) MarkFailed(_ context.Context, _, _ string) error { return nil }

func (s *gateStore) ListDestinationAttachments(
	_ context.Context, _ service.ListDestinationAttachmentsQuery,
) ([]service.ListedAttachment, error) {
	return nil, nil
}

// GetAuthorized answers the visibility question and returns the row as stored.
// A caller the row is invisible to gets ErrNotFound, exactly as the SQL does.
func (s *gateStore) GetAuthorized(
	_ context.Context, _ service.AttachmentAuthInput,
) (service.StoredAttachment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.authErr != nil {
		return service.StoredAttachment{}, s.authErr
	}
	return service.StoredAttachment{
		ID:               s.pending.ID,
		WorkspaceID:      s.pending.WorkspaceID,
		Kind:             s.pending.Destination.Kind,
		Status:           s.status,
		Filename:         s.pending.Filename,
		DeclaredMIME:     s.pending.DeclaredMIME,
		DetectedMIME:     s.uploaded.DetectedMIME,
		Size:             s.uploaded.Size,
		StorageObjectKey: s.pending.StorageObjectKey,
		EnvelopeVersion:  s.pending.EnvelopeVersion,
		WrappedDEK:       s.uploaded.WrappedDEK,
		KEKKeyID:         s.uploaded.KEKKeyID,
		KeyWrapVersion:   s.pending.KeyWrapVersion,

		PreviewStatus:          s.preview.PreviewStatus,
		PreviewObjectID:        s.preview.PreviewObjectID,
		PreviewSize:            s.preview.PreviewSize,
		PreviewWrappedDEK:      s.preview.PreviewWrappedDEK,
		PreviewKEKKeyID:        s.preview.PreviewKEKKeyID,
		PreviewEnvelopeVersion: s.preview.PreviewEnvelopeVersion,
		PreviewKeyWrapVersion:  s.preview.PreviewKeyWrapVersion,
		PreviewPageCount:       s.preview.PreviewPageCount,
	}, nil
}

func (s *gateStore) GetPreviewPage(
	_ context.Context, _ string, page int,
) (service.PreviewPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.pages[page]
	if !ok {
		return service.PreviewPage{}, domain.ErrNotFound
	}
	return record, nil
}

func (s *gateStore) setStatus(status domain.Status) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = status
}

func (s *gateStore) refuseVisibility(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.authErr = err
}

func (s *gateStore) attachmentID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pending.ID
}

// gateObjects is an in-memory blob store that counts reads. The count is what
// makes "zero bytes delivered" checkable at its source: a refused request must
// not merely omit the body, it must never have opened the object.
type gateObjects struct {
	mu      sync.Mutex
	objects map[string][]byte
	opens   int
}

func newGateObjects() *gateObjects {
	return &gateObjects{objects: map[string][]byte{}}
}

func (o *gateObjects) Put(_ context.Context, key string, body io.Reader) (int64, error) {
	content, err := io.ReadAll(body)
	if err != nil {
		return 0, err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.objects[key] = content
	return int64(len(content)), nil
}

func (o *gateObjects) Open(_ context.Context, key string) (io.ReadCloser, error) {
	return o.openAt(key, 0)
}

func (o *gateObjects) OpenRange(_ context.Context, key string, offset int64) (io.ReadCloser, error) {
	return o.openAt(key, offset)
}

func (o *gateObjects) openAt(key string, offset int64) (io.ReadCloser, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.opens++
	content, ok := o.objects[key]
	if !ok {
		return nil, domain.ErrNotFound
	}
	if offset < 0 || offset > int64(len(content)) {
		return nil, domain.ErrInvalidInput
	}
	return io.NopCloser(bytes.NewReader(content[offset:])), nil
}

func (o *gateObjects) Delete(_ context.Context, key string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.objects, key)
	return nil
}

func (o *gateObjects) put(key string, content []byte) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.objects[key] = content
}

func (o *gateObjects) openCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.opens
}

func (o *gateObjects) resetOpens() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.opens = 0
}

// --- fixture ------------------------------------------------------------

type gateFixture struct {
	router  http.Handler
	store   *gateStore
	objects *gateObjects
	keys    *crypto.Keyring
}

// newGateFixture uploads one attachment through the real service and returns it
// in the state a fresh upload lands in: pending_scan, with a real encrypted
// object in storage and a real wrapped key on the row.
func newGateFixture(t *testing.T) *gateFixture {
	t.Helper()

	master := make([]byte, crypto.KeySize)
	if _, err := io.ReadFull(rand.Reader, master); err != nil {
		t.Fatalf("generate key: %v", err)
	}
	keys, err := crypto.NewKeyring(gateKeyID, base64.StdEncoding.EncodeToString(master), "")
	if err != nil {
		t.Fatalf("build keyring: %v", err)
	}

	store, objects := &gateStore{}, newGateObjects()
	logger := platformlog.New("file-service", "test")
	attachments := service.NewAttachmentService(
		gateAuthorizer{}, store, objects, keys,
		domain.DefaultMaxUploadBytes, true /* scanRequired */, nil, logger,
	)

	destination := domain.Destination{Kind: domain.DestinationKindChannel, ID: testChannelID}
	target, err := attachments.AuthorizeUpload(context.Background(), service.AuthorizeUploadInput{
		Destination: destination, UserID: testUserID, SessionID: testSessionID,
	})
	if err != nil {
		t.Fatalf("authorize upload: %v", err)
	}
	view, err := attachments.Upload(context.Background(), service.UploadInput{
		Target: target, Filename: "report.bin", DeclaredMIME: "application/octet-stream",
		Content: bytes.NewReader(gatePayload),
	})
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	// The service, not the test, decides the state a finished upload lands in.
	// Asserting it here is what keeps these tests honest: if uploads ever stopped
	// starting out unapproved, every scenario below would be testing nothing.
	if view.Status != string(domain.StatusPendingScan) {
		t.Fatalf("a fresh upload must be pending_scan, got %q", view.Status)
	}
	if objects.openCount() != 0 {
		t.Fatalf("upload must not read the object back, opened %d times", objects.openCount())
	}

	return &gateFixture{
		router:  newTestRouter(t, attachments, enabledConfig()),
		store:   store,
		objects: objects,
		keys:    keys,
	}
}

// publishPreview gives the row a real, servable preview object, so the clean
// path can be checked all the way to its bytes rather than only to the point
// where the gate lets go.
func (f *gateFixture) publishPreview(t *testing.T, content []byte) {
	t.Helper()
	previewID := uuid.New()
	dataKey, err := crypto.NewDataKey()
	if err != nil {
		t.Fatalf("preview data key: %v", err)
	}
	encrypted, err := crypto.NewEncryptingReader(bytes.NewReader(content), dataKey, previewID)
	if err != nil {
		t.Fatalf("encrypt preview: %v", err)
	}
	ciphertext, err := io.ReadAll(encrypted)
	if err != nil {
		t.Fatalf("read preview envelope: %v", err)
	}
	wrapped, keyID, err := f.keys.Wrap(dataKey, crypto.Binding{
		AttachmentID:           previewID,
		WorkspaceID:            uuid.MustParse(gateWorkspaceID),
		PlaintextSize:          int64(len(content)),
		KeyWrapVersion:         crypto.KeyWrapVersion,
		ContentEnvelopeVersion: crypto.EnvelopeVersion,
	})
	if err != nil {
		t.Fatalf("wrap preview key: %v", err)
	}
	f.objects.put(domain.PreviewObjectKey(previewID), ciphertext)

	f.store.mu.Lock()
	defer f.store.mu.Unlock()
	f.store.preview = service.StoredAttachment{
		PreviewStatus:          domain.PreviewStatusReady,
		PreviewObjectID:        previewID.String(),
		PreviewSize:            int64(len(content)),
		PreviewWrappedDEK:      wrapped,
		PreviewKEKKeyID:        keyID,
		PreviewEnvelopeVersion: crypto.EnvelopeVersion,
		PreviewKeyWrapVersion:  crypto.KeyWrapVersion,
		PreviewPageCount:       1,
	}
}

// publishExtraPage adds one page beyond the first to an attachment that
// already has publishPreview's page one, exactly as MarkPreviewReady's
// unnest insert would for a multi-page PDF. It bumps PreviewPageCount to
// match, the same single write the real statement makes atomically.
func (f *gateFixture) publishExtraPage(t *testing.T, page int, content []byte) {
	t.Helper()
	objectID := uuid.New()
	dataKey, err := crypto.NewDataKey()
	if err != nil {
		t.Fatalf("page data key: %v", err)
	}
	encrypted, err := crypto.NewEncryptingReader(bytes.NewReader(content), dataKey, objectID)
	if err != nil {
		t.Fatalf("encrypt page: %v", err)
	}
	ciphertext, err := io.ReadAll(encrypted)
	if err != nil {
		t.Fatalf("read page envelope: %v", err)
	}
	wrapped, keyID, err := f.keys.Wrap(dataKey, crypto.Binding{
		AttachmentID:           objectID,
		WorkspaceID:            uuid.MustParse(gateWorkspaceID),
		PlaintextSize:          int64(len(content)),
		KeyWrapVersion:         crypto.KeyWrapVersion,
		ContentEnvelopeVersion: crypto.EnvelopeVersion,
	})
	if err != nil {
		t.Fatalf("wrap page key: %v", err)
	}
	f.objects.put(domain.PreviewObjectKey(objectID), ciphertext)

	f.store.mu.Lock()
	defer f.store.mu.Unlock()
	if f.store.pages == nil {
		f.store.pages = map[int]service.PreviewPage{}
	}
	f.store.pages[page] = service.PreviewPage{
		PageNumber: page, ObjectID: objectID.String(), Size: int64(len(content)),
		WrappedDEK: wrapped, KEKKeyID: keyID,
		EnvelopeVersion: crypto.EnvelopeVersion, KeyWrapVersion: crypto.KeyWrapVersion,
	}
	if f.store.preview.PreviewPageCount < page {
		f.store.preview.PreviewPageCount = page
	}
}

// get issues an authenticated request to a delivery route, exactly as a client
// holding a valid session would — no UI, no referer, nothing but the URL.
func (f *gateFixture) get(t *testing.T, suffix string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	f.objects.resetOpens()
	request := httptest.NewRequest(http.MethodGet, "/attachments/"+f.store.attachmentID()+suffix, nil)
	request.Header.Set("Authorization", "Bearer "+testToken)
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response := httptest.NewRecorder()
	f.router.ServeHTTP(response, request)
	return response
}

// assertRefusedWithoutContent is the whole security property in one place: the
// status is 403, the reason is the stable code, the body carries no plaintext
// and describes nothing internal, no range metadata escapes, and storage was
// never opened at all.
func assertRefusedWithoutContent(t *testing.T, response *httptest.ResponseRecorder, objects *gateObjects) {
	t.Helper()
	if response.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", response.Code)
	}
	if got := errorCode(t, response); got != "file_not_scanned" {
		t.Fatalf("expected code file_not_scanned, got %q", got)
	}
	if bytes.Contains(response.Body.Bytes(), gatePayload[:16]) {
		t.Fatal("a refused request leaked attachment content")
	}
	if response.Header().Get("Content-Range") != "" {
		t.Fatal("a refused request must not describe the object")
	}
	if response.Header().Get("Content-Disposition") != "" {
		t.Fatal("a refused request must not offer a filename")
	}
	// No stack trace, no storage host, no SQL, no daemon: the message is a
	// constant, and these are the substrings that would mean it stopped being one.
	for _, leak := range []string{"clamd", "clamav", "seaweedfs", "SELECT", "goroutine", "/nchat/"} {
		if strings.Contains(strings.ToLower(response.Body.String()), strings.ToLower(leak)) {
			t.Fatalf("response leaked internals (%q): %s", leak, response.Body.String())
		}
	}
	if objects.openCount() != 0 {
		t.Fatalf("a refused request opened storage %d times", objects.openCount())
	}
}

// --- the regression itself ----------------------------------------------

// The named bypass: upload, then ask for the bytes over the download URL
// directly, with a legitimate session and legitimate access to the attachment.
// pending_scan and rejected are both refused, and refused identically — the
// response never says which one it was.
func TestDirectDownloadURLCannotBypassTheScanGate(t *testing.T) {
	for _, status := range []domain.Status{domain.StatusPendingScan, domain.StatusRejected} {
		t.Run(string(status), func(t *testing.T) {
			fixture := newGateFixture(t)
			fixture.store.setStatus(status)

			assertRefusedWithoutContent(t, fixture.get(t, "/content", nil), fixture.objects)
		})
	}
}

// A Range header is not a second door. It is parsed by net/http *after* the
// service has already decided, so an unapproved attachment answers the same 403
// it answers without one: no 206, no partial body, no Content-Range.
func TestRangeRequestCannotBypassTheScanGate(t *testing.T) {
	for _, rangeHeader := range []string{"bytes=0-", "bytes=0-15", "bytes=-16", "bytes=32-63"} {
		t.Run(rangeHeader, func(t *testing.T) {
			fixture := newGateFixture(t)
			fixture.store.setStatus(domain.StatusPendingScan)

			response := fixture.get(t, "/content", map[string]string{"Range": rangeHeader})

			if response.Code == http.StatusPartialContent {
				t.Fatal("a range request produced 206 for an unapproved attachment")
			}
			assertRefusedWithoutContent(t, response, fixture.objects)
		})
	}
}

// The preview is a rendering of the file, so it is behind the same gate. It is
// refused before the route ever asks whether a preview exists — proved here by
// publishing a real, servable one first: a build that checked previewStatus
// before the scan would serve these bytes.
func TestPreviewCannotBypassTheScanGateEvenWhenOneIsPublished(t *testing.T) {
	for _, status := range []domain.Status{domain.StatusPendingScan, domain.StatusRejected} {
		t.Run(string(status), func(t *testing.T) {
			fixture := newGateFixture(t)
			fixture.publishPreview(t, []byte("jpeg-preview-bytes"))
			fixture.store.setStatus(status)

			response := fixture.get(t, "/preview", nil)

			assertRefusedWithoutContent(t, response, fixture.objects)
			if bytes.Contains(response.Body.Bytes(), []byte("jpeg-preview-bytes")) {
				t.Fatal("a refused preview leaked derived content")
			}
		})
	}
}

// Every state that is not clean is refused, including the ones an upload can be
// left in by a failure. Downloadable() is the single predicate, and this is the
// route-level proof that nothing else is consulted.
func TestOnlyACleanAttachmentIsEverServed(t *testing.T) {
	for _, status := range []domain.Status{
		domain.StatusPendingUpload, domain.StatusPendingScan,
		domain.StatusRejected, domain.StatusFailed, domain.StatusDeleted,
	} {
		t.Run(string(status), func(t *testing.T) {
			fixture := newGateFixture(t)
			fixture.store.setStatus(status)

			assertRefusedWithoutContent(t, fixture.get(t, "/content", nil), fixture.objects)
		})
	}
}

// The other half of the property: approving the attachment restores exactly the
// behaviour that existed before, whole file and byte range alike. Without this,
// a gate that refused everything would pass every test above.
func TestACleanAttachmentIsStillServedInFullAndByRange(t *testing.T) {
	fixture := newGateFixture(t)
	fixture.store.setStatus(domain.StatusClean)

	whole := fixture.get(t, "/content", nil)
	if whole.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", whole.Code)
	}
	if !bytes.Equal(whole.Body.Bytes(), gatePayload) {
		t.Fatal("a clean attachment did not serve its plaintext")
	}

	partial := fixture.get(t, "/content", map[string]string{"Range": "bytes=10-19"})
	if partial.Code != http.StatusPartialContent {
		t.Fatalf("expected 206, got %d", partial.Code)
	}
	if !bytes.Equal(partial.Body.Bytes(), gatePayload[10:20]) {
		t.Fatalf("range served %q", partial.Body.String())
	}
}

// A clean attachment's preview is served too, so the gate is proved to be the
// only thing the scan state changes about this route.
func TestACleanAttachmentStillServesItsPreview(t *testing.T) {
	fixture := newGateFixture(t)
	fixture.publishPreview(t, []byte("jpeg-preview-bytes"))
	fixture.store.setStatus(domain.StatusClean)

	response := fixture.get(t, "/preview", nil)

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}
	if response.Body.String() != "jpeg-preview-bytes" {
		t.Fatalf("preview served %q", response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != domain.PreviewContentType {
		t.Fatalf("preview content type = %q", got)
	}
}

func TestDocumentPreviewManifestAndFirstPageReuseTheAuthorizedPreview(t *testing.T) {
	fixture := newGateFixture(t)
	fixture.publishPreview(t, []byte("jpeg-preview-bytes"))
	fixture.publishExtraPage(t, 2, []byte("jpeg-preview-page-2"))
	fixture.publishExtraPage(t, 3, []byte("jpeg-preview-page-3"))
	fixture.store.setStatus(domain.StatusClean)

	manifest := fixture.get(t, "/document-preview", nil)
	if manifest.Code != http.StatusOK {
		t.Fatalf("manifest status = %d: %s", manifest.Code, manifest.Body.String())
	}
	if got := manifest.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("manifest cache policy = %q", got)
	}
	for _, fragment := range []string{
		`"attachmentId":"` + fixture.store.attachmentID() + `"`,
		`"pageCount":3`,
		`"labels":["Página 1","Página 2","Página 3"]`,
	} {
		if !strings.Contains(manifest.Body.String(), fragment) {
			t.Fatalf("manifest missing %q: %s", fragment, manifest.Body.String())
		}
	}

	page1 := fixture.get(t, "/document-preview/pages/1", nil)
	if page1.Code != http.StatusOK || page1.Body.String() != "jpeg-preview-bytes" {
		t.Fatalf("page 1 response = %d %q", page1.Code, page1.Body.String())
	}
	page2 := fixture.get(t, "/document-preview/pages/2", nil)
	if page2.Code != http.StatusOK || page2.Body.String() != "jpeg-preview-page-2" {
		t.Fatalf("page 2 response = %d %q", page2.Code, page2.Body.String())
	}
	page3 := fixture.get(t, "/document-preview/pages/3", nil)
	if page3.Code != http.StatusOK || page3.Body.String() != "jpeg-preview-page-3" {
		t.Fatalf("page 3 response = %d %q", page3.Code, page3.Body.String())
	}
	if got := page2.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("page 2 cache policy = %q", got)
	}
}

func TestDocumentPreviewRoutesCannotBypassScanOrPageBounds(t *testing.T) {
	fixture := newGateFixture(t)
	fixture.publishPreview(t, []byte("jpeg-preview-bytes"))
	fixture.publishExtraPage(t, 2, []byte("jpeg-preview-page-2"))
	for _, route := range []string{"/document-preview", "/document-preview/pages/1", "/document-preview/pages/2"} {
		fixture.store.setStatus(domain.StatusPendingScan)
		assertRefusedWithoutContent(t, fixture.get(t, route, nil), fixture.objects)
	}
	fixture.store.setStatus(domain.StatusClean)
	for _, route := range []string{"/document-preview/pages/3", "/document-preview/pages/0", "/document-preview/pages/-1"} {
		response := fixture.get(t, route, nil)
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404", route, response.Code)
		}
	}
}

// The scan verdict is not a substitute for authorization, in either direction.
// An attachment the caller cannot see is 404 even when it is clean, and the
// answer is the same on content and preview: approving a file grants nobody
// access to it.
func TestApprovalIsNotASubstituteForAuthorization(t *testing.T) {
	for _, route := range []string{"/content", "/preview"} {
		t.Run(route, func(t *testing.T) {
			fixture := newGateFixture(t)
			fixture.publishPreview(t, []byte("jpeg-preview-bytes"))
			fixture.store.setStatus(domain.StatusClean)
			fixture.store.refuseVisibility(domain.ErrNotFound)

			response := fixture.get(t, route, nil)

			if response.Code != http.StatusNotFound {
				t.Fatalf("expected 404, got %d", response.Code)
			}
			if fixture.objects.openCount() != 0 {
				t.Fatal("an unauthorised request reached storage")
			}
		})
	}
}
