package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/file-service/internal/domain"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
)

type derivativeUseCases struct {
	*fakeUseCases
	derived service.Download
	err     error
}

func (f *derivativeUseCases) DownloadMP3(context.Context, service.AttachmentAuthInput) (service.Download, error) {
	return f.derived, f.err
}

func TestOriginalAndExplicitAudioDerivativeUseDistinctRoutes(t *testing.T) {
	f := &derivativeUseCases{fakeUseCases: readyUseCases(), derived: service.Download{Filename: "voice.mp3", ContentType: "audio/mpeg", Size: 3, Content: seekableContent([]byte("MP3"))}}
	f.download = service.Download{Filename: "voice.ogg", ContentType: "application/ogg", Size: 4, Content: seekableContent([]byte("OggS"))}
	router := newTestRouter(t, f, enabledConfig())
	id := uuid.NewString()
	original := httptest.NewRecorder()
	router.ServeHTTP(original, downloadRequest(t, id))
	assertDownloadRepresentation(t, original, "voice.ogg", "application/ogg", "OggS")

	r := downloadRequest(t, id)
	r.URL.Path = strings.ReplaceAll(r.URL.Path, "/content", "/audio.mp3")
	derived := httptest.NewRecorder()
	router.ServeHTTP(derived, r)
	assertDownloadRepresentation(t, derived, "voice.mp3", "audio/mpeg", "MP3")

	r.Header.Del("Authorization")
	unauthorized := httptest.NewRecorder()
	router.ServeHTTP(unauthorized, r)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated derivative: %d", unauthorized.Code)
	}
}

func TestPolicyErrorsArePublicUploadContracts(t *testing.T) {
	for _, tt := range []struct {
		err  error
		code string
	}{
		{domain.ErrFormatBlocked, "file_policy_blocked"},
		{domain.ErrUnrecognizedContent, "file_content_unrecognized"},
		{domain.ErrInspectionLimit, "file_inspection_limit"},
	} {
		t.Run(tt.code, func(t *testing.T) {
			f := readyUseCases()
			f.uploadErr = fmt.Errorf("private storage path and scanner detail: %w", tt.err)
			r := httptest.NewRecorder()
			newTestRouter(t, f, enabledConfig()).ServeHTTP(r, uploadRequest(t, channelUploadPath(testChannelID), fileOf("business bytes")))
			assertPublicHTTPError(t, r, 415, tt.code, "file type is not supported")
		})
	}
}

func assertPublicHTTPError(t *testing.T, r *httptest.ResponseRecorder, status int, code, message string) {
	t.Helper()
	if r.Code != status {
		t.Fatalf("status=%d body=%s", r.Code, r.Body.String())
	}
	var body httputil.Envelope
	if err := json.Unmarshal(r.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error == nil {
		t.Fatal("missing error envelope")
	}
	if body.Error.Code != code || body.Error.Message != message {
		t.Fatalf("public error=%+v", body.Error)
	}
	if strings.Contains(r.Body.String(), "private") {
		t.Fatal("internal detail exposed")
	}
}

func TestMP3DerivativeHTTPFailures(t *testing.T) {
	for _, tt := range []struct {
		name          string
		err           error
		status        int
		code, message string
	}{
		{"lookup", domain.ErrNotFound, 404, "not_found", "attachment not found"},
		{"open", domain.ErrUnavailable, 503, "service_unavailable", "attachment storage unavailable"},
		{"conversion", domain.ErrAudioTranscodeFailed, 502, "audio_transcode_failed", "internal error"},
		{"quarantine", domain.ErrNotDownloadable, 403, "file_not_scanned", "attachment has not been approved by the malware scan"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := &derivativeUseCases{fakeUseCases: readyUseCases(), err: fmt.Errorf("private converter detail: %w", tt.err)}
			r := httptest.NewRecorder()
			req := downloadRequest(t, uuid.NewString())
			req.URL.Path = strings.ReplaceAll(req.URL.Path, "/content", "/audio.mp3")
			newTestRouter(t, f, enabledConfig()).ServeHTTP(r, req)
			assertPublicHTTPError(t, r, tt.status, tt.code, tt.message)
		})
	}
}

func TestMP3RouteWithoutDerivativeCapabilityFailsClosed(t *testing.T) {
	r := httptest.NewRecorder()
	req := downloadRequest(t, uuid.NewString())
	req.URL.Path = strings.ReplaceAll(req.URL.Path, "/content", "/audio.mp3")
	newTestRouter(t, readyUseCases(), enabledConfig()).ServeHTTP(r, req)
	assertPublicHTTPError(t, r, 502, "audio_transcode_failed", "internal error")
}

func assertDownloadRepresentation(t *testing.T, r *httptest.ResponseRecorder, name, mime, payload string) {
	t.Helper()
	if r.Code != http.StatusOK || r.Body.String() != payload {
		t.Fatalf("download response=%d/%q", r.Code, r.Body.String())
	}
	if r.Header().Get("Content-Type") != mime {
		t.Fatalf("content type=%s", r.Header().Get("Content-Type"))
	}
	if !strings.Contains(r.Header().Get("Content-Disposition"), name) {
		t.Fatalf("filename header=%s", r.Header().Get("Content-Disposition"))
	}
}
