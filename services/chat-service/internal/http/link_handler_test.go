package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	httpapi "github.com/nicrepository/nchat/services/chat-service/internal/http"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// The derived-image route and the per-link JSON contract (issue #807).

type fakePreviewImages struct {
	image storage.LinkPreviewImage
	err   error
	last  [3]string
}

func (f *fakePreviewImages) LinkPreviewImage(_ context.Context, workspaceID, userID, previewID string) (storage.LinkPreviewImage, error) {
	f.last = [3]string{workspaceID, userID, previewID}
	return f.image, f.err
}

const previewID = "6f0d9c2e-1b4a-4c9d-8e7f-0123456789ab"

func imageRequest(id string, authenticated bool) *http.Request {
	request := httptest.NewRequest(http.MethodGet, "/api/chat/link-previews/"+id+"/image", nil)
	request.SetPathValue("previewID", id)
	if authenticated {
		request = request.WithContext(context.WithValue(request.Context(), httpapi.ExportCtxKeyUserID, msgTestUserID))
	}
	return request
}

func imageHandler(images *fakePreviewImages) *httpapi.MessageHandler {
	handler := httpapi.NewMessageHandler(&fakeWorkspaceResolver{workspace: activeWorkspace()}, &fakeMessageProvider{}, nil)
	if images != nil {
		handler = handler.WithLinkPreviewImages(images)
	}
	return handler
}

func TestLinkPreviewImage_ServesTheDerivedBytesToAMember(t *testing.T) {
	images := &fakePreviewImages{image: storage.LinkPreviewImage{Data: []byte{0xff, 0xd8, 0xff, 0xd9}, ContentType: "image/jpeg"}}
	recorder := httptest.NewRecorder()

	imageHandler(images).GetLinkPreviewImage(recorder, imageRequest(previewID, true))

	if recorder.Code != http.StatusOK || recorder.Body.Len() != 4 {
		t.Fatalf("status = %d body = %d", recorder.Code, recorder.Body.Len())
	}
	headers := recorder.Header()
	if headers.Get("Content-Type") != "image/jpeg" || headers.Get("X-Content-Type-Options") != "nosniff" ||
		!strings.HasPrefix(headers.Get("Cache-Control"), "private") || headers.Get("Content-Disposition") != "inline" {
		t.Fatalf("headers = %v", headers)
	}
	if images.last != [3]string{activeWorkspace().ID, msgTestUserID, previewID} {
		t.Fatalf("the store was asked with %v", images.last)
	}
}

func TestLinkPreviewImage_RefusesEverythingElseWithoutAnOracle(t *testing.T) {
	cases := map[string]struct {
		images        *fakePreviewImages
		id            string
		authenticated bool
		want          int
	}{
		"unauthenticated":  {&fakePreviewImages{}, previewID, false, http.StatusUnauthorized},
		"not a preview id": {&fakePreviewImages{}, "../secrets", true, http.StatusNotFound},
		"not found":        {&fakePreviewImages{err: domain.ErrNotFound}, previewID, true, http.StatusNotFound},
		"not wired":        {nil, previewID, true, http.StatusNotFound},
		"store failure":    {&fakePreviewImages{err: errors.New("db")}, previewID, true, http.StatusInternalServerError},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			imageHandler(tc.images).GetLinkPreviewImage(recorder, imageRequest(tc.id, tc.authenticated))
			if recorder.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, tc.want, recorder.Body.String())
			}
		})
	}
}

func TestMessageJSONCarriesThePerLinkContract(t *testing.T) {
	updated := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	message := domain.Message{
		ID: "m1", SenderID: msgTestUserID, Kind: domain.MessageKindUser, BodyText: "veja ￼ e https://ok.test/a",
		BodyFormat: domain.MessageBodyFormatV2, Status: domain.MessageStatusActive, LinkSafety: domain.MessageLinkSafetyMalicious,
		Links: []domain.MessageLink{
			{Ordinal: 0, TargetKey: domain.LinkTargetKey("https://bad.test/"), Safety: domain.LinkSafetyMalicious, Click: domain.LinkClickNone, UpdatedAt: updated},
			{Ordinal: 1, TargetKey: domain.LinkTargetKey("https://ok.test/a"), Text: "https://ok.test/a", URL: "https://ok.test/a", Hostname: "ok.test", Safety: domain.LinkSafetySafe,
				Click: domain.LinkClickDirect, Href: "https://ok.test/a", UpdatedAt: updated,
				Preview: &domain.LinkPreview{State: domain.LinkPreviewReady, Hostname: "ok.test", Title: "T", ImageID: previewID, ImageWidth: 4, ImageHeight: 2}},
		},
	}
	encoded, err := json.Marshal(httpapi.ExportMapToMessageJSON(message))
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Links []map[string]any `json:"links"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Links) != 2 {
		t.Fatalf("links = %s", encoded)
	}
	blocked, safe := decoded.Links[0], decoded.Links[1]
	for _, absent := range []string{"href", "url", "text", "hostname", "preview"} {
		if _, ok := blocked[absent]; ok {
			t.Fatalf("a blocked link carried %q: %s", absent, encoded)
		}
	}
	// The identity survives redaction, and it is not the URL.
	if blocked["target_key"] != domain.LinkTargetKey("https://bad.test/") || len(blocked["target_key"].(string)) != domain.LinkTargetKeyLength {
		t.Fatalf("blocked link identity = %v", blocked["target_key"])
	}
	if safe["target_key"] != domain.LinkTargetKey("https://ok.test/a") {
		t.Fatalf("safe link identity = %v", safe["target_key"])
	}
	if safe["href"] != "https://ok.test/a" || safe["click"] != "direct" || safe["safety"] != "safe" {
		t.Fatalf("safe link = %v", safe)
	}
	preview := safe["preview"].(map[string]any)
	if preview["image_id"] != previewID || preview["state"] != "ready" || preview["hostname"] != "ok.test" {
		t.Fatalf("preview = %v", preview)
	}
	if strings.Contains(string(encoded), "image_url") || strings.Contains(string(encoded), "http://cdn") {
		t.Fatalf("a remote image url leaked: %s", encoded)
	}
}
