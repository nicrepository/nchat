package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	platformlog "github.com/nicrepository/nchat/libs/go/platform/log"
	"github.com/nicrepository/nchat/services/file-service/internal/config"
	"github.com/nicrepository/nchat/services/file-service/internal/crypto"
	"github.com/nicrepository/nchat/services/file-service/internal/domain"
	httpapi "github.com/nicrepository/nchat/services/file-service/internal/http"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
)

const (
	testUserID    = "22222222-2222-4222-8222-222222222222"
	testSessionID = "11111111-1111-4111-8111-111111111111"
	testChannelID = "33333333-3333-4333-8333-333333333333"
	testDMID      = "66666666-6666-4666-8666-666666666666"
)

// --- fakes --------------------------------------------------------------

// staticValidator accepts one opaque token, so route tests exercise the
// handler rather than JWT parsing (covered separately in auth_test.go).
type staticValidator struct {
	token string
	err   error
}

func (v staticValidator) ValidateAccessToken(raw string) (httpapi.Principal, error) {
	if v.err != nil {
		return httpapi.Principal{}, v.err
	}
	if raw != v.token {
		return httpapi.Principal{}, errors.New("invalid token")
	}
	return httpapi.Principal{
		UserID: testUserID, SessionID: testSessionID,
		AccessExpiresAt: time.Now().Add(time.Hour),
	}, nil
}

type fakeUseCases struct {
	mu sync.Mutex

	ready bool

	uploadView service.AttachmentView
	uploadErr  error
	uploadCall service.UploadInput
	uploadBody []byte

	authorizeTarget service.UploadTarget
	authorizeErr    error
	authorizeCalls  int

	// uploadHook runs inside Upload, so a test can hold a request in flight and
	// observe what its admission slot blocks.
	uploadHook func()

	metadataView service.AttachmentView
	metadataErr  error

	download    service.Download
	downloadErr error
	lastInput   service.AttachmentAuthInput

	preview     service.Download
	previewErr  error
	previewCall service.AttachmentAuthInput

	listViews []service.AttachmentView
	listErr   error
	listInput service.ListDestinationAttachmentsInput

	cancelErr   error
	cancelInput service.CancelDraftInput
}

// AuthorizeUpload stands in for the destination lookup the handler now performs
// before it touches the body.
func (f *fakeUseCases) AuthorizeUpload(
	_ context.Context, input service.AuthorizeUploadInput,
) (service.UploadTarget, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.authorizeCalls++
	if f.authorizeErr != nil {
		return service.UploadTarget{}, f.authorizeErr
	}
	target := f.authorizeTarget
	if target.UploaderID == "" {
		target = service.UploadTarget{
			Destination:    input.Destination,
			WorkspaceID:    "11111111-1111-4111-8111-111111111111",
			UploaderID:     input.UserID,
			MaxUploadBytes: domain.DefaultMaxUploadBytes,
		}
	}
	return target, nil
}

func (f *fakeUseCases) authorizeCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.authorizeCalls
}

func (f *fakeUseCases) ListDestinationAttachments(
	_ context.Context, input service.ListDestinationAttachmentsInput,
) ([]service.AttachmentView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listInput = input
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.listViews, nil
}

func (f *fakeUseCases) Upload(_ context.Context, input service.UploadInput) (service.AttachmentView, error) {
	// Always drain the content: the real service streams it, so a body error
	// must surface here exactly as it would in production.
	body, readErr := io.ReadAll(input.Content)
	f.mu.Lock()
	hook := f.uploadHook
	f.mu.Unlock()
	if hook != nil {
		// Outside the lock: the point is to hold the request, not the fake.
		hook()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.uploadCall, f.uploadBody = input, body
	if readErr != nil {
		return service.AttachmentView{}, readErr
	}
	if f.uploadErr != nil {
		return service.AttachmentView{}, f.uploadErr
	}
	return f.uploadView, nil
}

func (f *fakeUseCases) CancelDraft(_ context.Context, input service.CancelDraftInput) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelInput = input
	return f.cancelErr
}

func (f *fakeUseCases) Metadata(_ context.Context, input service.AttachmentAuthInput) (service.AttachmentView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastInput = input
	if f.metadataErr != nil {
		return service.AttachmentView{}, f.metadataErr
	}
	return f.metadataView, nil
}

func (f *fakeUseCases) Download(_ context.Context, input service.AttachmentAuthInput) (service.Download, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastInput = input
	if f.downloadErr != nil {
		return service.Download{}, f.downloadErr
	}
	return f.download, nil
}

func (f *fakeUseCases) Preview(_ context.Context, input service.AttachmentAuthInput) (service.Download, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.previewCall = input
	if f.previewErr != nil {
		return service.Download{}, f.previewErr
	}
	return f.preview, nil
}

func (f *fakeUseCases) Ready() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ready
}

func (f *fakeUseCases) call() service.UploadInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.uploadCall
}

func (f *fakeUseCases) body() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]byte(nil), f.uploadBody...)
}

// --- harness ------------------------------------------------------------

const testToken = "test-access-token"

func enabledConfig() config.Config {
	return config.Config{
		ServiceName: "file-service", Env: "test", Port: 8083,
		ReadHeaderTimeoutSeconds: 5,
		UploadsEnabled:           true,
		MaxUploadBytes:           domain.DefaultMaxUploadBytes,
	}
}

// useCases is the interface rather than the fake, so the same harness can be
// pointed at the real AttachmentService — see attachment_scan_gate_test.go.
func newTestRouter(t *testing.T, useCases httpapi.AttachmentUseCases, cfg config.Config) http.Handler {
	t.Helper()
	limiter := httpapi.NewUserRateLimiter(1000, time.Minute)
	t.Cleanup(limiter.Stop)
	return httpapi.NewRouter(cfg, platformlog.New("file-service", "test"), httpapi.RouterDependencies{
		TokenValidator: staticValidator{token: testToken},
		Attachments:    useCases,
		RateLimiter:    limiter,
	})
}

func readyUseCases() *fakeUseCases {
	return &fakeUseCases{
		ready: true,
		uploadView: service.AttachmentView{
			ID: uuid.NewString(), Filename: "report.pdf",
			ContentType: "application/pdf", Size: 12,
			Status: string(domain.StatusPendingScan), DestinationKind: "channel",
			CreatedAt: time.Now().UTC(),
		},
	}
}

// multipartBody builds a body with the given file parts.
func multipartBody(t *testing.T, parts ...filePart) (io.Reader, string) {
	t.Helper()
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	for _, part := range parts {
		headers := textproto.MIMEHeader{}
		headers.Set("Content-Disposition", part.disposition())
		if part.contentType != "" {
			headers.Set("Content-Type", part.contentType)
		}
		w, err := writer.CreatePart(headers)
		if err != nil {
			t.Fatalf("create part: %v", err)
		}
		if _, err := w.Write(part.content); err != nil {
			t.Fatalf("write part: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	return &buf, writer.FormDataContentType()
}

type filePart struct {
	field       string
	filename    string
	contentType string
	content     []byte
}

func (p filePart) disposition() string {
	if p.filename == "" {
		return `form-data; name="` + p.field + `"`
	}
	return `form-data; name="` + p.field + `"; filename="` + p.filename + `"`
}

func fileOf(content string) filePart {
	return filePart{field: "file", filename: "report.pdf", contentType: "application/pdf", content: []byte(content)}
}

func uploadRequest(t *testing.T, path string, parts ...filePart) *http.Request {
	t.Helper()
	body, contentType := multipartBody(t, parts...)
	request := httptest.NewRequest(http.MethodPost, path, body)
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Authorization", "Bearer "+testToken)
	return request
}

func channelUploadPath(id string) string { return "/channels/" + id + "/attachments" }
func dmUploadPath(id string) string      { return "/dm/" + id + "/attachments" }

func errorCode(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	var envelope httputil.Envelope
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if envelope.Error == nil {
		t.Fatalf("expected an error envelope, got %s", response.Body.String())
	}
	return envelope.Error.Code
}

// --- upload -------------------------------------------------------------

func TestUploadToChannelSucceeds(t *testing.T) {
	useCases := readyUseCases()
	router := newTestRouter(t, useCases, enabledConfig())
	response := httptest.NewRecorder()

	router.ServeHTTP(response, uploadRequest(t, channelUploadPath(testChannelID), fileOf("hello world")))

	if response.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", response.Code, response.Body.String())
	}
	call := useCases.call()
	if call.Target.Destination.Kind != domain.DestinationKindChannel ||
		call.Target.Destination.ID != testChannelID {
		t.Fatalf("unexpected destination %+v", call.Target.Destination)
	}
	// The target reaches the service already authorised, and its uploader came
	// from the validated token rather than from anything in the body.
	if call.Target.UploaderID != testUserID {
		t.Fatal("the principal must come from the validated token, never from the body")
	}
	if useCases.authorizeCallCount() != 1 {
		t.Fatal("the destination must be authorised exactly once, before the body is read")
	}
	if call.Filename != "report.pdf" || call.DeclaredMIME != "application/pdf" {
		t.Fatalf("unexpected part metadata %+v", call)
	}
	if string(useCases.body()) != "hello world" {
		t.Fatalf("unexpected streamed body %q", useCases.body())
	}
}

func TestUploadAcceptsServerRecognizedMessageDraftPurpose(t *testing.T) {
	useCases := readyUseCases()
	router := newTestRouter(t, useCases, enabledConfig())
	response := httptest.NewRecorder()
	purpose := filePart{field: "purpose", content: []byte(service.UploadPurposeMessageDraft)}
	router.ServeHTTP(response, uploadRequest(
		t, channelUploadPath(testChannelID), purpose, fileOf("hello world"),
	))
	if response.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", response.Code, response.Body.String())
	}
	if got := useCases.call().Purpose; got != service.UploadPurposeMessageDraft {
		t.Fatalf("expected draft purpose, got %q", got)
	}
}

func TestDeleteAttachmentDraftIsRegistered(t *testing.T) {
	useCases := readyUseCases()
	router := newTestRouter(t, useCases, enabledConfig())
	request := httptest.NewRequest(http.MethodDelete, "/attachments/"+testChannelID, nil)
	request.Header.Set("Authorization", "Bearer "+testToken)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", response.Code, response.Body.String())
	}
	if useCases.cancelInput.AttachmentID != testChannelID ||
		useCases.cancelInput.UploaderID != testUserID {
		t.Fatalf("cancel must use the path id and authenticated uploader: %+v", useCases.cancelInput)
	}
}

func TestDeleteAttachmentDraftIsAuthenticatedAndNonEnumerating(t *testing.T) {
	useCases := readyUseCases()
	useCases.cancelErr = domain.ErrNotFound
	router := newTestRouter(t, useCases, enabledConfig())

	unauthenticated := httptest.NewRequest(http.MethodDelete, "/attachments/"+testChannelID, nil)
	unauthenticatedResponse := httptest.NewRecorder()
	router.ServeHTTP(unauthenticatedResponse, unauthenticated)
	if unauthenticatedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthenticated cancel to return 401, got %d", unauthenticatedResponse.Code)
	}

	missing := httptest.NewRequest(http.MethodDelete, "/attachments/"+testChannelID, nil)
	missing.Header.Set("Authorization", "Bearer "+testToken)
	missingResponse := httptest.NewRecorder()
	router.ServeHTTP(missingResponse, missing)
	if missingResponse.Code != http.StatusNotFound {
		t.Fatalf("foreign, associated and missing drafts must share 404, got %d", missingResponse.Code)
	}
}

func TestUploadToDMSucceeds(t *testing.T) {
	useCases := readyUseCases()
	router := newTestRouter(t, useCases, enabledConfig())
	response := httptest.NewRecorder()

	router.ServeHTTP(response, uploadRequest(t, dmUploadPath(testDMID), fileOf("dm attachment")))

	if response.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", response.Code, response.Body.String())
	}
	if useCases.call().Target.Destination.Kind != domain.DestinationKindDM {
		t.Fatalf("unexpected destination kind %q", useCases.call().Target.Destination.Kind)
	}
}

// The response must never carry the storage key, the workspace wiring or any
// crypto material.
func TestUploadResponseExposesOnlyTheClientProjection(t *testing.T) {
	useCases := readyUseCases()
	router := newTestRouter(t, useCases, enabledConfig())
	response := httptest.NewRecorder()

	router.ServeHTTP(response, uploadRequest(t, channelUploadPath(testChannelID), fileOf("hello")))

	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	allowed := map[string]bool{
		"id": true, "filename": true, "contentType": true,
		"size": true, "status": true, "destinationKind": true, "createdAt": true,
		// The preview state is a client concern and carries nothing internal:
		// it is one of four words, never an object id, a key or a URL.
		"previewStatus": true,
	}
	for field := range envelope.Data {
		if !allowed[field] {
			t.Fatalf("unexpected field %q in the upload response", field)
		}
	}
	body := response.Body.String()
	for _, leak := range []string{"nchat/attachments", "wrapped", "dek", "seaweed", "fid"} {
		if strings.Contains(strings.ToLower(body), leak) {
			t.Fatalf("response leaks %q: %s", leak, body)
		}
	}
}

func TestUploadRequiresAuthentication(t *testing.T) {
	router := newTestRouter(t, readyUseCases(), enabledConfig())

	tests := []struct {
		name   string
		header string
	}{
		{name: "no header", header: ""},
		{name: "wrong scheme", header: "Basic " + testToken},
		{name: "empty bearer", header: "Bearer "},
		{name: "invalid token", header: "Bearer nope"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := uploadRequest(t, channelUploadPath(testChannelID), fileOf("x"))
			request.Header.Del("Authorization")
			if tt.header != "" {
				request.Header.Set("Authorization", tt.header)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)

			if response.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d", response.Code)
			}
		})
	}
}

func TestUploadRejectsAnInvalidDestinationID(t *testing.T) {
	router := newTestRouter(t, readyUseCases(), enabledConfig())
	for _, path := range []string{
		channelUploadPath("not-a-uuid"),
		dmUploadPath("00000000"),
	} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, uploadRequest(t, path, fileOf("x")))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for %s, got %d", path, response.Code)
		}
	}
}

func TestUploadRejectsNonMultipartBodies(t *testing.T) {
	router := newTestRouter(t, readyUseCases(), enabledConfig())

	tests := []struct {
		name        string
		contentType string
	}{
		{name: "json", contentType: "application/json"},
		{name: "missing boundary", contentType: "multipart/form-data"},
		{name: "empty", contentType: ""},
		{name: "malformed", contentType: "multipart/form-data; boundary"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost,
				channelUploadPath(testChannelID), strings.NewReader(`{"a":1}`))
			request.Header.Set("Authorization", "Bearer "+testToken)
			if tt.contentType != "" {
				request.Header.Set("Content-Type", tt.contentType)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)

			if response.Code != http.StatusUnsupportedMediaType {
				t.Fatalf("expected 415, got %d", response.Code)
			}
		})
	}
}

func TestUploadRejectsAMissingOrWrongFilePart(t *testing.T) {
	router := newTestRouter(t, readyUseCases(), enabledConfig())

	tests := []struct {
		name  string
		parts []filePart
	}{
		{name: "no parts at all"},
		{name: "wrong field name", parts: []filePart{{field: "attachment", filename: "a.txt", content: []byte("x")}}},
		{name: "plain form field", parts: []filePart{{field: "file", content: []byte("x")}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, uploadRequest(t, channelUploadPath(testChannelID), tt.parts...))
			if response.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", response.Code, response.Body.String())
			}
		})
	}
}

// The second file is detected while the first is still streaming, so the
// service can compensate instead of leaving a stored object behind.
func TestUploadRejectsMoreThanOneFile(t *testing.T) {
	useCases := readyUseCases()
	router := newTestRouter(t, useCases, enabledConfig())
	response := httptest.NewRecorder()

	router.ServeHTTP(response, uploadRequest(t, channelUploadPath(testChannelID),
		fileOf("first"),
		filePart{field: "file", filename: "second.pdf", content: []byte("second")},
	))

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", response.Code, response.Body.String())
	}
	if errorCode(t, response) != httputil.ErrCodeBadRequest {
		t.Fatalf("unexpected error code %q", errorCode(t, response))
	}
}

func TestUploadRejectsATrailingFormField(t *testing.T) {
	router := newTestRouter(t, readyUseCases(), enabledConfig())
	response := httptest.NewRecorder()

	router.ServeHTTP(response, uploadRequest(t, channelUploadPath(testChannelID),
		fileOf("first"),
		filePart{field: "workspaceId", content: []byte("another-workspace")},
	))

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", response.Code, response.Body.String())
	}
}

// A body without Content-Length, and one that lies about it, must both be
// bounded by the byte cap rather than by the header.
func TestUploadIsBoundedRegardlessOfContentLength(t *testing.T) {
	cfg := enabledConfig()
	cfg.MaxUploadBytes = 1024
	router := newTestRouter(t, readyUseCases(), cfg)

	tests := []struct {
		name          string
		contentLength int64
	}{
		{name: "absent", contentLength: -1},
		{name: "understated", contentLength: 10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := uploadRequest(t, channelUploadPath(testChannelID),
				fileOf(strings.Repeat("a", 64*1024)))
			request.ContentLength = tt.contentLength
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)

			if response.Code == http.StatusCreated {
				t.Fatal("an over-sized body must never be accepted")
			}
		})
	}
}

// A file exactly at the cap must survive the multipart framing around it.
//
// The body carries boundaries and part headers on top of the file, so a request
// cap set to the file limit itself would refuse a legitimately sized file for
// bytes the user did not send. The handler reserves multipartOverhead for
// exactly this, and the authoritative count in the service is over the file's
// own bytes, not the envelope's.
func TestUploadAcceptsAFileExactlyAtTheCapDespiteMultipartFraming(t *testing.T) {
	cfg := enabledConfig()
	cfg.MaxUploadBytes = 4096
	router := newTestRouter(t, readyUseCases(), cfg)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, uploadRequest(t, channelUploadPath(testChannelID),
		fileOf(strings.Repeat("a", 4096))))

	if response.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", response.Code, response.Body.String())
	}
}

func TestUploadMapsServiceErrorsToSanitisedStatuses(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		want     int
		wantCode string
	}{
		{name: "empty file", err: domain.ErrEmptyFile, want: http.StatusBadRequest, wantCode: httputil.ErrCodeBadRequest},
		{name: "too many files", err: domain.ErrTooManyFiles, want: http.StatusBadRequest, wantCode: httputil.ErrCodeBadRequest},
		{name: "too large", err: domain.ErrTooLarge, want: http.StatusRequestEntityTooLarge, wantCode: "payload_too_large"},
		{name: "unauthorized", err: domain.ErrUnauthorized, want: http.StatusUnauthorized, wantCode: httputil.ErrCodeUnauthorized},
		{name: "invisible destination", err: domain.ErrNotFound, want: http.StatusNotFound, wantCode: httputil.ErrCodeNotFound},
		{name: "storage down", err: domain.ErrUnavailable, want: http.StatusServiceUnavailable, wantCode: "service_unavailable"},
		{name: "database down", err: errors.New("pq: connection refused to db-primary.internal"), want: http.StatusInternalServerError, wantCode: httputil.ErrCodeInternal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			useCases := readyUseCases()
			useCases.uploadErr = tt.err
			router := newTestRouter(t, useCases, enabledConfig())
			response := httptest.NewRecorder()

			router.ServeHTTP(response, uploadRequest(t, channelUploadPath(testChannelID), fileOf("x")))

			if response.Code != tt.want {
				t.Fatalf("expected %d, got %d: %s", tt.want, response.Code, response.Body.String())
			}
			if got := errorCode(t, response); got != tt.wantCode {
				t.Fatalf("expected code %q, got %q", tt.wantCode, got)
			}
			if strings.Contains(response.Body.String(), "db-primary.internal") {
				t.Fatal("the underlying error must never reach the client")
			}
		})
	}
}

func TestUploadIsRateLimitedPerUser(t *testing.T) {
	limiter := httpapi.NewUserRateLimiter(1, time.Minute)
	t.Cleanup(limiter.Stop)
	router := httpapi.NewRouter(enabledConfig(), platformlog.New("file-service", "test"),
		httpapi.RouterDependencies{
			TokenValidator: staticValidator{token: testToken},
			Attachments:    readyUseCases(),
			RateLimiter:    limiter,
		})

	first := httptest.NewRecorder()
	router.ServeHTTP(first, uploadRequest(t, channelUploadPath(testChannelID), fileOf("x")))
	if first.Code != http.StatusCreated {
		t.Fatalf("expected the first upload to pass, got %d", first.Code)
	}

	second := httptest.NewRecorder()
	router.ServeHTTP(second, uploadRequest(t, channelUploadPath(testChannelID), fileOf("x")))
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", second.Code)
	}
	if second.Header().Get("Retry-After") == "" {
		t.Fatal("expected a Retry-After header")
	}
}

// --- metadata -----------------------------------------------------------

func TestGetMetadataReturnsTheProjection(t *testing.T) {
	id := uuid.NewString()
	useCases := readyUseCases()
	useCases.metadataView = service.AttachmentView{
		ID: id, Filename: "report.pdf", ContentType: "application/pdf",
		Size: 42, Status: string(domain.StatusPendingScan), DestinationKind: "channel",
	}
	router := newTestRouter(t, useCases, enabledConfig())

	request := httptest.NewRequest(http.MethodGet, "/attachments/"+id, nil)
	request.Header.Set("Authorization", "Bearer "+testToken)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), string(domain.StatusPendingScan)) {
		t.Fatal("the scan state must be visible to the client")
	}
}

func TestGetMetadataRequiresAuthentication(t *testing.T) {
	router := newTestRouter(t, readyUseCases(), enabledConfig())
	request := httptest.NewRequest(http.MethodGet, "/attachments/"+uuid.NewString(), nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", response.Code)
	}
}

func TestGetMetadataHidesInvisibleAttachments(t *testing.T) {
	useCases := readyUseCases()
	useCases.metadataErr = domain.ErrNotFound
	router := newTestRouter(t, useCases, enabledConfig())

	request := httptest.NewRequest(http.MethodGet, "/attachments/"+uuid.NewString(), nil)
	request.Header.Set("Authorization", "Bearer "+testToken)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", response.Code)
	}
}

// --- download -----------------------------------------------------------

func downloadRequest(t *testing.T, id string) *http.Request {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/attachments/"+id+"/content", nil)
	request.Header.Set("Authorization", "Bearer "+testToken)
	return request
}

func TestDownloadServesTheContentWithSafeHeaders(t *testing.T) {
	id := uuid.NewString()
	payload := []byte("decrypted attachment payload")
	useCases := readyUseCases()
	useCases.download = service.Download{
		Filename: "relatório final.pdf", ContentType: "application/pdf",
		Size: int64(len(payload)), Content: seekableContent(payload),
	}
	router := newTestRouter(t, useCases, enabledConfig())
	response := httptest.NewRecorder()

	router.ServeHTTP(response, downloadRequest(t, id))

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	if !bytes.Equal(response.Body.Bytes(), payload) {
		t.Fatal("unexpected body")
	}
	if got := response.Header().Get("Content-Type"); got != "application/pdf" {
		t.Fatalf("unexpected content type %q", got)
	}
	if got := response.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("expected nosniff, got %q", got)
	}
	if got := response.Header().Get("Accept-Ranges"); got != "bytes" {
		t.Fatalf("expected Accept-Ranges bytes, got %q", got)
	}
	// Attachment content is private and re-authorised on every request, so no
	// shared cache may keep it. Asserted on the route itself rather than on the
	// global middleware: this response must carry the policy whoever else is in
	// the chain.
	if got := response.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("expected private, no-store, got %q", got)
	}
	if got := response.Header().Get("Content-Length"); got != strconv.Itoa(len(payload)) {
		t.Fatalf("unexpected content length %q", got)
	}

	disposition := response.Header().Get("Content-Disposition")
	if !strings.HasPrefix(disposition, "attachment;") {
		t.Fatalf("content must never be served inline, got %q", disposition)
	}
	if !strings.Contains(disposition, `filename="relat_rio final.pdf"`) {
		t.Fatalf("expected an ASCII fallback, got %q", disposition)
	}
	if !strings.Contains(disposition, "filename*=UTF-8''") {
		t.Fatalf("expected an RFC 5987 parameter, got %q", disposition)
	}
	if useCases.lastInput.AttachmentID != id {
		t.Fatalf("unexpected attachment id %q", useCases.lastInput.AttachmentID)
	}
}

// Active content is never rendered in the API origin: the disposition and the
// nosniff header hold whatever the detected type is.
func TestDownloadNeverServesActiveContentInline(t *testing.T) {
	for _, contentType := range []string{"text/html; charset=utf-8", "image/svg+xml", "application/javascript"} {
		t.Run(contentType, func(t *testing.T) {
			useCases := readyUseCases()
			payload := []byte("<script>alert(1)</script>")
			useCases.download = service.Download{
				Filename: "payload.html", ContentType: contentType,
				Size: int64(len(payload)), Content: seekableContent(payload),
			}
			router := newTestRouter(t, useCases, enabledConfig())
			response := httptest.NewRecorder()

			router.ServeHTTP(response, downloadRequest(t, uuid.NewString()))

			if !strings.HasPrefix(response.Header().Get("Content-Disposition"), "attachment;") {
				t.Fatal("active content must be forced to download")
			}
			if response.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatal("nosniff must be present")
			}
		})
	}
}

func TestDownloadEscapesAQuotedFilename(t *testing.T) {
	useCases := readyUseCases()
	useCases.download = service.Download{
		Filename: `evil";attachment;x=".pdf`, ContentType: "application/pdf",
		Size: 1, Content: seekableContent([]byte("x")),
	}
	router := newTestRouter(t, useCases, enabledConfig())
	response := httptest.NewRecorder()

	router.ServeHTTP(response, downloadRequest(t, uuid.NewString()))

	disposition := response.Header().Get("Content-Disposition")
	if strings.Count(disposition, `"`) != 2 {
		t.Fatalf("the quoted fallback must not be breakable: %q", disposition)
	}
}

func TestDownloadFallsBackWhenTheFilenameHasNoASCII(t *testing.T) {
	useCases := readyUseCases()
	useCases.download = service.Download{
		Filename: "relatório", ContentType: "application/pdf",
		Size: 1, Content: seekableContent([]byte("x")),
	}
	router := newTestRouter(t, useCases, enabledConfig())
	response := httptest.NewRecorder()
	router.ServeHTTP(response, downloadRequest(t, uuid.NewString()))

	if !strings.Contains(response.Header().Get("Content-Disposition"), "filename*=UTF-8''") {
		t.Fatalf("unexpected disposition %q", response.Header().Get("Content-Disposition"))
	}
}

// --- byte ranges (RF-31) ------------------------------------------------

// rangePayload is deliberately long enough that a range is a genuine subset and
// short enough to compare byte for byte.
var rangePayload = []byte("0123456789abcdefghijklmnopqrstuvwxyz")

// rangeResponse performs one download with the given Range header. An empty
// header sends none at all, which is not the same request as an empty one.
func rangeResponse(t *testing.T, header string, payload []byte) *httptest.ResponseRecorder {
	t.Helper()
	useCases := readyUseCases()
	useCases.download = service.Download{
		Filename: "clip.mp4", ContentType: "video/mp4",
		Size: int64(len(payload)), Content: seekableContent(payload),
	}
	router := newTestRouter(t, useCases, enabledConfig())
	request := downloadRequest(t, uuid.NewString())
	if header != "" {
		request.Header.Set("Range", header)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

// A request with no Range is unchanged by the feature: the whole file, 200, and
// the same safe headers it has always carried. Only Accept-Ranges moves, and it
// has to — a client learns that seeking is possible from nowhere else.
func TestDownloadWithoutRangeStillServesTheWholeFile(t *testing.T) {
	response := rangeResponse(t, "", rangePayload)

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}
	if !bytes.Equal(response.Body.Bytes(), rangePayload) {
		t.Fatal("expected the complete body")
	}
	if got := response.Header().Get("Accept-Ranges"); got != "bytes" {
		t.Fatalf("expected Accept-Ranges bytes, got %q", got)
	}
	if got := response.Header().Get("Content-Length"); got != strconv.Itoa(len(rangePayload)) {
		t.Fatalf("unexpected length %q", got)
	}
	if response.Header().Get("Content-Range") != "" {
		t.Fatal("a full response must not carry Content-Range")
	}
}

// The bytes returned are exactly the bytes asked for — the property everything
// else in the contract exists to describe.
func TestDownloadServesTheRequestedByteRange(t *testing.T) {
	last := len(rangePayload) - 1
	tests := []struct {
		name   string
		header string
		want   []byte
		rng    string
	}{
		{"first byte", "bytes=0-0", rangePayload[:1], "bytes 0-0/36"},
		{"leading run", "bytes=0-9", rangePayload[:10], "bytes 0-9/36"},
		{"interior run", "bytes=10-19", rangePayload[10:20], "bytes 10-19/36"},
		{"open ended", "bytes=30-", rangePayload[30:], "bytes 30-35/36"},
		{"suffix", "bytes=-6", rangePayload[30:], "bytes 30-35/36"},
		{"last byte", "bytes=35-35", rangePayload[last:], "bytes 35-35/36"},
		{"whole file", "bytes=0-35", rangePayload, "bytes 0-35/36"},
		// An end past the file is clamped, not refused: the client gets what
		// exists and Content-Range tells it what that was.
		{"end beyond size", "bytes=30-9999", rangePayload[30:], "bytes 30-35/36"},
		// A suffix longer than the file is the whole file.
		{"suffix beyond size", "bytes=-9999", rangePayload, "bytes 0-35/36"},
		{"spaces are tolerated", "bytes= 5-9 ", rangePayload[5:10], "bytes 5-9/36"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := rangeResponse(t, tt.header, rangePayload)

			if response.Code != http.StatusPartialContent {
				t.Fatalf("expected 206, got %d: %s", response.Code, response.Body.String())
			}
			if !bytes.Equal(response.Body.Bytes(), tt.want) {
				t.Fatalf("expected %q, got %q", tt.want, response.Body.Bytes())
			}
			if got := response.Header().Get("Content-Range"); got != tt.rng {
				t.Fatalf("expected Content-Range %q, got %q", tt.rng, got)
			}
			if got := response.Header().Get("Content-Length"); got != strconv.Itoa(len(tt.want)) {
				t.Fatalf("expected the segment's length, got %q", got)
			}
			// A partial response is still the attachment: the type and the
			// headers that keep it from being rendered do not change with it.
			if got := response.Header().Get("Content-Type"); got != "video/mp4" {
				t.Fatalf("unexpected content type %q", got)
			}
			if got := response.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Fatalf("expected nosniff, got %q", got)
			}
			// A partial response is the case that most needs the policy: a
			// proxy caching one range of one caller's video would hand it to
			// the next caller asking for the same bytes.
			if got := response.Header().Get("Cache-Control"); got != "private, no-store" {
				t.Fatalf("expected private, no-store, got %q", got)
			}
			if !strings.HasPrefix(response.Header().Get("Content-Disposition"), "attachment;") {
				t.Fatal("a range must not become an inline response")
			}
		})
	}
}

// A range that starts at or past the end of the file overlaps nothing. It is
// refused with the length, so a client that guessed wrong can correct itself
// without a second probing request.
func TestDownloadRefusesRangesPastTheEnd(t *testing.T) {
	for _, header := range []string{"bytes=36-", "bytes=36-40", "bytes=100-200"} {
		t.Run(header, func(t *testing.T) {
			response := rangeResponse(t, header, rangePayload)

			if response.Code != http.StatusRequestedRangeNotSatisfiable {
				t.Fatalf("expected 416, got %d", response.Code)
			}
			if got := response.Header().Get("Content-Range"); got != "bytes */36" {
				t.Fatalf("expected bytes */36, got %q", got)
			}
		})
	}
}

// Nothing a client can put in a Range header may produce a slice of the file.
// Reversed bounds, junk, a foreign unit and numbers far beyond int64 all end the
// same way: refused, with no part of the content in the body.
func TestDownloadRefusesMalformedRanges(t *testing.T) {
	headers := []string{
		"bytes=20-10",
		"bytes=-5-10",
		"bytes=-",
		"bytes=abc-def",
		"bytes=99999999999999999999-",
		"bytes=0-99999999999999999999",
		"items=0-9",
		"0-9",
	}
	for _, header := range headers {
		t.Run(header, func(t *testing.T) {
			response := rangeResponse(t, header, rangePayload)

			if response.Code != http.StatusRequestedRangeNotSatisfiable {
				t.Fatalf("expected 416, got %d", response.Code)
			}
			if bytes.Contains(response.Body.Bytes(), rangePayload[:4]) {
				t.Fatal("a refused range must not return content")
			}
		})
	}
}

// `bytes=-0` asks for the last zero bytes. net/http answers it as an empty 206
// rather than a 416; the body is empty either way, so nothing is disclosed and
// nothing is read. Asserted so the behaviour is a recorded decision rather than
// something a future reader has to rediscover.
func TestDownloadAnswersAZeroLengthSuffixWithNoContent(t *testing.T) {
	response := rangeResponse(t, "bytes=-0", rangePayload)

	if response.Body.Len() != 0 {
		t.Fatalf("expected an empty body, got %q", response.Body.Bytes())
	}
}

// A header naming no range at all is not a request for part of the file, so it
// is ignored and the whole file is served — the same answer as sending no
// header.
func TestDownloadIgnoresAnEmptyRangeSet(t *testing.T) {
	response := rangeResponse(t, "bytes=", rangePayload)

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}
	if !bytes.Equal(response.Body.Bytes(), rangePayload) {
		t.Fatal("expected the complete body")
	}
}

// Only one range per request. Each one costs its own storage read and its own
// chunk decryption, so a list is refused rather than turned into a fan-out.
func TestDownloadRefusesMultipleRanges(t *testing.T) {
	response := rangeResponse(t, "bytes=0-9,20-29", rangePayload)

	if response.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("expected 416, got %d", response.Code)
	}
	if got := errorCode(t, response); got != "range_not_supported" {
		t.Fatalf("unexpected code %q", got)
	}
	if got := response.Header().Get("Content-Range"); got != "bytes */36" {
		t.Fatalf("expected bytes */36, got %q", got)
	}
	if strings.Contains(response.Header().Get("Content-Type"), "multipart/byteranges") {
		t.Fatal("multipart/byteranges must never be produced")
	}
}

// An empty attachment has no satisfiable range. net/http deliberately ignores
// the header rather than refusing, because clients that attach a Range to every
// request would otherwise fail on a zero-byte file. The response is an empty
// 200, which is the right answer to "give me this file" either way.
func TestDownloadRangeOnAnEmptyAttachment(t *testing.T) {
	response := rangeResponse(t, "bytes=0-0", nil)

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}
	if response.Body.Len() != 0 {
		t.Fatal("an empty attachment has no bytes to serve")
	}
	if response.Header().Get("Content-Range") != "" {
		t.Fatal("an ignored range must not produce Content-Range")
	}
}

// The regression the whole feature has to survive: a range is not a second way
// in. Every refusal the download already makes is made before a Range header is
// so much as looked at, so not even two bytes of a file the scan has not
// cleared — or of one the caller cannot see — can be extracted.
func TestRangeCannotBypassTheScanGateOrAuthorization(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"scan pending", domain.ErrNotDownloadable, http.StatusForbidden},
		{"scan rejected", domain.ErrNotDownloadable, http.StatusForbidden},
		{"not visible", domain.ErrNotFound, http.StatusNotFound},
		{"session expired", domain.ErrUnauthorized, http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			useCases := readyUseCases()
			useCases.downloadErr = tt.err
			router := newTestRouter(t, useCases, enabledConfig())
			request := downloadRequest(t, uuid.NewString())
			request.Header.Set("Range", "bytes=0-1")
			response := httptest.NewRecorder()

			router.ServeHTTP(response, request)

			if response.Code != tt.want {
				t.Fatalf("expected %d, got %d", tt.want, response.Code)
			}
			if response.Header().Get("Content-Range") != "" {
				t.Fatal("a refused request must not describe the object")
			}
			if bytes.Contains(response.Body.Bytes(), rangePayload[:2]) {
				t.Fatal("a refused request must not leak content")
			}
		})
	}
}

// An unauthenticated caller is refused before anything else, Range or not.
func TestRangeRequiresAuthentication(t *testing.T) {
	router := newTestRouter(t, readyUseCases(), enabledConfig())
	request := httptest.NewRequest(http.MethodGet, "/attachments/"+uuid.NewString()+"/content", nil)
	request.Header.Set("Range", "bytes=0-1")
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", response.Code)
	}
}

func TestDownloadMapsServiceErrors(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		want     int
		wantCode string
	}{
		{name: "pending scan", err: domain.ErrNotDownloadable, want: http.StatusForbidden, wantCode: "file_not_scanned"},
		{name: "not visible", err: domain.ErrNotFound, want: http.StatusNotFound, wantCode: httputil.ErrCodeNotFound},
		{name: "session expired", err: domain.ErrUnauthorized, want: http.StatusUnauthorized, wantCode: httputil.ErrCodeUnauthorized},
		{name: "storage down", err: domain.ErrUnavailable, want: http.StatusServiceUnavailable, wantCode: "service_unavailable"},
		{name: "unexpected", err: errors.New("seaweedfs-filer:8888 refused the connection"), want: http.StatusInternalServerError, wantCode: httputil.ErrCodeInternal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			useCases := readyUseCases()
			useCases.downloadErr = tt.err
			router := newTestRouter(t, useCases, enabledConfig())
			response := httptest.NewRecorder()

			router.ServeHTTP(response, downloadRequest(t, uuid.NewString()))

			if response.Code != tt.want {
				t.Fatalf("expected %d, got %d", tt.want, response.Code)
			}
			if got := errorCode(t, response); got != tt.wantCode {
				t.Fatalf("expected code %q, got %q", tt.wantCode, got)
			}
			if strings.Contains(response.Body.String(), "seaweedfs-filer") {
				t.Fatal("storage topology must never reach the client")
			}
		})
	}
}

// A stream that fails integrity mid-response must not be completed as if it
// were valid: the body stays short of the declared length.
func TestDownloadAbortsWhenTheStreamFails(t *testing.T) {
	useCases := readyUseCases()
	useCases.download = service.Download{
		Filename: "report.pdf", ContentType: "application/pdf", Size: 100,
		Content: &failingReader{
			size: 100, data: []byte("partial"),
			err: errors.New("ciphertext authentication failed"),
		},
	}
	router := newTestRouter(t, useCases, enabledConfig())
	response := httptest.NewRecorder()

	router.ServeHTTP(response, downloadRequest(t, uuid.NewString()))

	if int64(response.Body.Len()) >= 100 {
		t.Fatal("a failed stream must not produce a complete body")
	}
}

func TestDownloadRequiresAuthentication(t *testing.T) {
	router := newTestRouter(t, readyUseCases(), enabledConfig())
	request := httptest.NewRequest(http.MethodGet, "/attachments/"+uuid.NewString()+"/content", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", response.Code)
	}
}

// --- feature gating -----------------------------------------------------

func TestAttachmentRoutesAreUnavailableWhileUploadsAreDisabled(t *testing.T) {
	cfg := enabledConfig()
	cfg.UploadsEnabled = false
	router := newTestRouter(t, readyUseCases(), cfg)

	requests := []*http.Request{
		uploadRequest(t, channelUploadPath(testChannelID), fileOf("x")),
		uploadRequest(t, dmUploadPath(testDMID), fileOf("x")),
		httptest.NewRequest(http.MethodGet, "/attachments/"+uuid.NewString(), nil),
		downloadRequest(t, uuid.NewString()),
	}
	for _, request := range requests {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("expected 503 for %s, got %d", request.URL.Path, response.Code)
		}
		if errorCode(t, response) != "service_unavailable" {
			t.Fatalf("unexpected code %q", errorCode(t, response))
		}
	}
}

func TestAttachmentRoutesAreUnavailableWhenDependenciesAreMissing(t *testing.T) {
	notReady := &fakeUseCases{ready: false}
	router := newTestRouter(t, notReady, enabledConfig())

	response := httptest.NewRecorder()
	router.ServeHTTP(response, uploadRequest(t, channelUploadPath(testChannelID), fileOf("x")))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", response.Code)
	}
}

func TestAttachmentRoutesAreUnavailableWithoutATokenValidator(t *testing.T) {
	router := httpapi.NewRouter(enabledConfig(), platformlog.New("file-service", "test"),
		httpapi.RouterDependencies{Attachments: readyUseCases()})

	response := httptest.NewRecorder()
	router.ServeHTTP(response, uploadRequest(t, channelUploadPath(testChannelID), fileOf("x")))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", response.Code)
	}
}

// Each attachment route is registered for exactly one method. Another method
// falls through to the service's catch-all, which answers a JSON 404 — the
// same behaviour every other route in this service has — and, crucially, never
// reaches a handler.
func TestAttachmentRoutesRejectTheWrongMethod(t *testing.T) {
	useCases := readyUseCases()
	router := newTestRouter(t, useCases, enabledConfig())

	tests := []struct{ method, path string }{
		// GET on both collections is the listing route (issues #435 and #441),
		// so the unrouted method asserted here is PUT.
		{method: http.MethodPut, path: channelUploadPath(testChannelID)},
		{method: http.MethodPut, path: dmUploadPath(testDMID)},
		{method: http.MethodPatch, path: "/attachments/" + uuid.NewString()},
		{method: http.MethodPost, path: "/attachments/" + uuid.NewString() + "/content"},
	}
	for _, tt := range tests {
		request := httptest.NewRequest(tt.method, tt.path, nil)
		request.Header.Set("Authorization", "Bearer "+testToken)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)

		if response.Code != http.StatusNotFound {
			t.Fatalf("expected 404 for %s %s, got %d", tt.method, tt.path, response.Code)
		}
		if errorCode(t, response) != httputil.ErrCodeNotFound {
			t.Fatalf("unexpected code %q", errorCode(t, response))
		}
	}
	if useCases.call().Filename != "" {
		t.Fatal("no handler may run for an unrouted method")
	}
}

func TestNewRouterToleratesANilLogger(t *testing.T) {
	router := httpapi.NewRouter(enabledConfig(), nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, httpapi.RouteHealthz, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}
}

// failingReader is a body that measures like a whole object and then fails
// part-way through, which is what an integrity failure looks like to a handler
// that has already committed its headers.
type failingReader struct {
	size int64
	data []byte
	err  error
	read int
}

// Seek reports the declared length so http.ServeContent measures this stream
// exactly as it measures a real one. Nothing is repositioned: the test only
// reads forward, and the failure under test is in Read.
func (r *failingReader) Seek(offset int64, whence int) (int64, error) {
	if whence == io.SeekEnd {
		return r.size + offset, nil
	}
	return offset, nil
}

func (r *failingReader) Close() error { return nil }

func (r *failingReader) Read(p []byte) (int, error) {
	if r.read >= len(r.data) {
		return 0, r.err
	}
	n := copy(p, r.data[r.read:])
	r.read += n
	return n, nil
}

// A filename is display metadata, but it still ends up inside a header. The
// ext-value encoding must make it impossible for one to add a parameter of its
// own or to close the ext-value early.
func TestDownloadFilenameCannotInjectHeaderParameters(t *testing.T) {
	tests := []struct {
		name     string
		filename string
	}{
		{name: "semicolon", filename: "a;filename=evil.exe"},
		{name: "apostrophe", filename: "it's a report.pdf"},
		{name: "comma", filename: "a,b.pdf"},
		{name: "equals", filename: "a=b.pdf"},
		{name: "space", filename: "two words.pdf"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			useCases := readyUseCases()
			useCases.download = service.Download{
				Filename: tt.filename, ContentType: "application/pdf",
				Size: 1, Content: seekableContent([]byte("x")),
			}
			router := newTestRouter(t, useCases, enabledConfig())
			response := httptest.NewRecorder()
			router.ServeHTTP(response, downloadRequest(t, uuid.NewString()))

			disposition := response.Header().Get("Content-Disposition")
			_, extValue, found := strings.Cut(disposition, "filename*=UTF-8''")
			if !found {
				t.Fatalf("missing ext-value in %q", disposition)
			}
			for _, forbidden := range []string{";", "'", ",", "=", " ", `"`} {
				if strings.Contains(extValue, forbidden) {
					t.Fatalf("ext-value %q must not contain %q", extValue, forbidden)
				}
			}
			// The header must still parse as exactly one disposition with two
			// parameters, whatever the filename was.
			if strings.Count(disposition, ";") != 2 {
				t.Fatalf("filename injected a parameter: %q", disposition)
			}
		})
	}
}

// A row whose recorded size was edited fails at unwrap, inside the use case, so
// the handler never gets a stream. The client must see a plain internal error:
// no 200, no Content-Length, and above all no prefix of the file that could be
// mistaken for the whole of it.
func TestDownloadWithTamperedMetadataNeverProducesAPartialBody(t *testing.T) {
	// The shapes the crypto layer reports for an edited size, an edited key id
	// and an unknown wrapping version. All three must look identical from here.
	for name, cause := range map[string]error{
		"tampered size":       fmt.Errorf("unwrap attachment key: %w: wrapped key", crypto.ErrCiphertext),
		"tampered key id":     fmt.Errorf("unwrap attachment key: %w: key id is not configured", crypto.ErrUnknownKey),
		"unknown wrap format": fmt.Errorf("unwrap attachment key: %w: key wrap version 1", crypto.ErrUnsupportedVersion),
	} {
		t.Run(name, func(t *testing.T) {
			useCases := readyUseCases()
			useCases.downloadErr = cause
			router := newTestRouter(t, useCases, enabledConfig())
			response := httptest.NewRecorder()

			router.ServeHTTP(response, downloadRequest(t, uuid.NewString()))

			if response.Code != http.StatusInternalServerError {
				t.Fatalf("expected 500, got %d", response.Code)
			}
			if got := errorCode(t, response); got != httputil.ErrCodeInternal {
				t.Fatalf("expected the generic internal code, got %q", got)
			}
			// No length is published for a response that is not the file.
			if value := response.Header().Get("Content-Length"); value != "" {
				t.Fatalf("an error response must not declare a Content-Length, got %q", value)
			}
			if disposition := response.Header().Get("Content-Disposition"); disposition != "" {
				t.Fatalf("an error response must not offer a download, got %q", disposition)
			}
			// The body is the error envelope, never file bytes.
			body := response.Body.String()
			if !strings.Contains(body, httputil.ErrCodeInternal) {
				t.Fatalf("expected the error envelope, got %q", body)
			}
			// And it names none of the causes: size, key id and format version
			// are indistinguishable to the caller.
			for _, leak := range []string{
				"size", "wrap", "key id", "kek", "dek", "nonce", "tag", "ciphertext", "version",
			} {
				if strings.Contains(strings.ToLower(body), leak) {
					t.Fatalf("the response leaks the cryptographic cause %q: %s", leak, body)
				}
			}
		})
	}
}

// The success path still publishes exactly the authenticated length, so the
// test above is not passing merely because downloads are broken.
func TestDownloadPublishesTheAuthenticatedLengthOnSuccess(t *testing.T) {
	payload := []byte("a verified attachment body")
	useCases := readyUseCases()
	useCases.download = service.Download{
		Filename: "report.pdf", ContentType: "application/pdf", Size: int64(len(payload)),
		Content: seekableContent(payload),
	}
	router := newTestRouter(t, useCases, enabledConfig())
	response := httptest.NewRecorder()

	router.ServeHTTP(response, downloadRequest(t, uuid.NewString()))

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}
	if got := response.Header().Get("Content-Length"); got != strconv.Itoa(len(payload)) {
		t.Fatalf("expected Content-Length %d, got %q", len(payload), got)
	}
	if response.Body.String() != string(payload) {
		t.Fatalf("unexpected body %q", response.Body.String())
	}
}

// contentStream is the seekable body a Download carries. Tests build one from
// bytes so http.ServeContent measures and slices it exactly as it does in
// production — a body that only reads would never exercise the range contract.
type contentStream struct{ *bytes.Reader }

func (contentStream) Close() error { return nil }

func seekableContent(payload []byte) io.ReadSeekCloser {
	return contentStream{bytes.NewReader(payload)}
}
