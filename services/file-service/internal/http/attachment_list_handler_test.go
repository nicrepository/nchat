package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/file-service/internal/domain"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
)

func listRequest(t *testing.T, router http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, target, nil)
	request.Header.Set("Authorization", "Bearer "+testToken)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func decodeAttachments(t *testing.T, response *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	var body struct {
		Data struct {
			Attachments []map[string]any `json:"attachments"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode listing: %v; body: %s", err, response.Body.String())
	}
	return body.Data.Attachments
}

func TestListChannelAttachmentsServesMetadataOnly(t *testing.T) {
	useCases := readyUseCases()
	useCases.listViews = []service.AttachmentView{{
		ID: "a-1", Filename: "relatório <script>.pdf", ContentType: "application/pdf",
		Size: 2048, Status: string(domain.StatusClean), DestinationKind: "channel",
		CreatedAt: time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC),
	}}
	router := newTestRouter(t, useCases, enabledConfig())

	response := listRequest(t, router, channelUploadPath(testChannelID))
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	attachments := decodeAttachments(t, response)
	if len(attachments) != 1 {
		t.Fatalf("expected one attachment, got %d", len(attachments))
	}
	item := attachments[0]
	if item["id"] != "a-1" || item["status"] != string(domain.StatusClean) {
		t.Fatalf("unexpected attachment: %v", item)
	}
	// The filename is data, not markup: it is carried verbatim in JSON and it is
	// the renderer's job to treat it as text.
	if item["filename"] != "relatório <script>.pdf" {
		t.Fatalf("filename must round-trip unchanged, got %v", item["filename"])
	}
	// Nothing about how the object is stored may appear in a listing.
	for _, forbidden := range []string{"storageObjectKey", "wrappedDek", "envelopeVersion", "workspaceId"} {
		if _, present := item[forbidden]; present {
			t.Fatalf("listing leaked %q: %v", forbidden, item)
		}
	}
	if useCases.listInput.ChannelID != testChannelID {
		t.Fatalf("expected the path channel, got %q", useCases.listInput.ChannelID)
	}
	if useCases.listInput.Limit != 0 {
		t.Fatalf("an absent limit must stay unspecified, got %d", useCases.listInput.Limit)
	}
}

func TestListChannelAttachmentsForwardsTheRequestedLimit(t *testing.T) {
	useCases := readyUseCases()
	router := newTestRouter(t, useCases, enabledConfig())

	if response := listRequest(t, router, channelUploadPath(testChannelID)+"?limit=5"); response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}
	if useCases.listInput.Limit != 5 {
		t.Fatalf("expected limit 5, got %d", useCases.listInput.Limit)
	}
}

func TestListChannelAttachmentsRejectsAMalformedLimit(t *testing.T) {
	for _, raw := range []string{"abc", "0", "-1", "1e3"} {
		useCases := readyUseCases()
		router := newTestRouter(t, useCases, enabledConfig())

		response := listRequest(t, router, channelUploadPath(testChannelID)+"?limit="+raw)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("limit=%q: expected 400, got %d", raw, response.Code)
		}
		if errorCode(t, response) != httputil.ErrCodeBadRequest {
			t.Fatalf("limit=%q: unexpected code %q", raw, errorCode(t, response))
		}
	}
}

func TestListChannelAttachmentsHidesUnreachableChannels(t *testing.T) {
	useCases := readyUseCases()
	useCases.listErr = domain.ErrNotFound
	router := newTestRouter(t, useCases, enabledConfig())

	response := listRequest(t, router, channelUploadPath(testChannelID))
	if response.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", response.Code)
	}
	if errorCode(t, response) != httputil.ErrCodeNotFound {
		t.Fatalf("unexpected code %q", errorCode(t, response))
	}
}

func TestListChannelAttachmentsRequiresAuthentication(t *testing.T) {
	router := newTestRouter(t, readyUseCases(), enabledConfig())

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, channelUploadPath(testChannelID), nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", response.Code)
	}
}

func TestListChannelAttachmentsIsUnavailableWhileUploadsAreDisabled(t *testing.T) {
	disabled := enabledConfig()
	disabled.UploadsEnabled = false
	router := newTestRouter(t, readyUseCases(), disabled)

	response := listRequest(t, router, channelUploadPath(testChannelID))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", response.Code)
	}
}
