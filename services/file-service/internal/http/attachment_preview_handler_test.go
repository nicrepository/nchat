package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	platformlog "github.com/nicrepository/nchat/libs/go/platform/log"
	"github.com/nicrepository/nchat/libs/go/platform/observability"
	"github.com/nicrepository/nchat/services/file-service/internal/domain"
	httpapi "github.com/nicrepository/nchat/services/file-service/internal/http"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
)

func previewPath(attachmentID string) string {
	return "/attachments/" + attachmentID + "/preview"
}

func previewRequest(path string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.Header.Set("Authorization", "Bearer "+testToken)
	return request
}

// servablePreview wires the fake with a preview the handler can stream.
func servablePreview(useCases *fakeUseCases, body string) {
	useCases.preview = service.Download{
		Filename:    "diagram.png",
		ContentType: domain.PreviewContentType,
		Size:        int64(len(body)),
		Content:     seekableContent([]byte(body)),
	}
}

func TestPreviewServesTheImageWithSafeHeaders(t *testing.T) {
	useCases := readyUseCases()
	servablePreview(useCases, "jpeg-bytes")
	router := newTestRouter(t, useCases, enabledConfig())
	attachmentID := uuid.NewString()

	response := httptest.NewRecorder()
	router.ServeHTTP(response, previewRequest(previewPath(attachmentID)))

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	if response.Body.String() != "jpeg-bytes" {
		t.Fatalf("unexpected body %q", response.Body.String())
	}
	// The type is the server's, never the attachment's: these bytes are a
	// raster this service produced.
	if got := response.Header().Get("Content-Type"); got != domain.PreviewContentType {
		t.Fatalf("Content-Type = %q, want %q", got, domain.PreviewContentType)
	}
	// Inline is what makes it usable as an image, and it is only safe because
	// the payload cannot be the uploaded file. nosniff stops the declared type
	// from being second-guessed.
	if got := response.Header().Get("Content-Disposition"); got != "inline" {
		t.Fatalf("Content-Disposition = %q, want inline", got)
	}
	if got := response.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
	// A preview is derived from content whose visibility is re-checked on every
	// request, so no cache may keep it.
	if got := response.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("Cache-Control = %q, want private, no-store", got)
	}
	if got := response.Header().Get("Content-Length"); got != "10" {
		t.Fatalf("Content-Length = %q, want 10", got)
	}
	// The filename is not echoed anywhere: the response is not the file.
	if strings.Contains(response.Header().Get("Content-Disposition"), "diagram") {
		t.Fatal("the preview response must not name the uploaded file")
	}
	if useCases.previewCall.AttachmentID != attachmentID {
		t.Fatalf("handler asked for %q, want %q", useCases.previewCall.AttachmentID, attachmentID)
	}
	if useCases.previewCall.UserID != testUserID || useCases.previewCall.SessionID != testSessionID {
		t.Fatalf("the principal must come from the validated session, got %+v", useCases.previewCall)
	}
}

func TestPreviewRequiresAuthentication(t *testing.T) {
	useCases := readyUseCases()
	servablePreview(useCases, "jpeg-bytes")
	router := newTestRouter(t, useCases, enabledConfig())

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, previewPath(uuid.NewString()), nil))

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", response.Code)
	}
	if useCases.previewCall.AttachmentID != "" {
		t.Fatal("an unauthenticated request must not reach the use case")
	}
}

// Each refusal keeps its own meaning, and none of them describes anything the
// caller did not already know.
func TestPreviewMapsRefusalsToStableCodes(t *testing.T) {
	for name, tt := range map[string]struct {
		err        error
		wantStatus int
		wantCode   string
	}{
		"invisible attachment": {
			err: domain.ErrNotFound, wantStatus: http.StatusNotFound, wantCode: "not_found",
		},
		"not scanned": {
			err: domain.ErrNotDownloadable, wantStatus: http.StatusForbidden, wantCode: "file_not_scanned",
		},
		"no preview": {
			err: domain.ErrPreviewUnavailable, wantStatus: http.StatusConflict,
			wantCode: "preview_not_available",
		},
		"storage down": {
			err: domain.ErrUnavailable, wantStatus: http.StatusServiceUnavailable,
			wantCode: "service_unavailable",
		},
	} {
		t.Run(name, func(t *testing.T) {
			useCases := readyUseCases()
			useCases.previewErr = tt.err
			router := newTestRouter(t, useCases, enabledConfig())

			response := httptest.NewRecorder()
			router.ServeHTTP(response, previewRequest(previewPath(uuid.NewString())))

			if response.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d: %s", response.Code, tt.wantStatus, response.Body.String())
			}
			var envelope struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if envelope.Error.Code != tt.wantCode {
				t.Fatalf("code = %q, want %q", envelope.Error.Code, tt.wantCode)
			}
			// No internal detail, no storage topology, no stack trace.
			for _, leak := range []string{"nchat/previews", "seaweed", "dek", "sql", "goroutine"} {
				if strings.Contains(strings.ToLower(response.Body.String()), leak) {
					t.Fatalf("response leaks %q: %s", leak, response.Body.String())
				}
			}
		})
	}
}

// "Not approved by the scan" and "no preview" are different refusals and must
// not be confused: a client asking for a preview of a scanned file would
// otherwise be told the file is unsafe. They now differ in status as well as in
// wording, and both halves are asserted — a future refactor that collapsed them
// back onto one status would still have to keep the messages apart.
func TestPreviewMessageDistinguishesTheTwoRefusals(t *testing.T) {
	messages := map[string]string{}
	for _, tt := range []struct {
		name string
		err  error
	}{
		{name: "not scanned", err: domain.ErrNotDownloadable},
		{name: "no preview", err: domain.ErrPreviewUnavailable},
	} {
		useCases := readyUseCases()
		useCases.previewErr = tt.err
		router := newTestRouter(t, useCases, enabledConfig())
		response := httptest.NewRecorder()
		router.ServeHTTP(response, previewRequest(previewPath(uuid.NewString())))
		messages[tt.name] = response.Body.String()
	}
	if messages["not scanned"] == messages["no preview"] {
		t.Fatalf("both conflicts answered identically: %s", messages["no preview"])
	}
}

// The route must exist in every configuration, so a disabled feature is never
// mistaken for a missing endpoint.
func TestPreviewIsUnavailableRatherThanMissingWhileUploadsAreDisabled(t *testing.T) {
	cfg := enabledConfig()
	cfg.UploadsEnabled = false
	router := newTestRouter(t, readyUseCases(), cfg)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, previewRequest(previewPath(uuid.NewString())))

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", response.Code)
	}
}

func TestPreviewRejectsMethodsOtherThanGet(t *testing.T) {
	useCases := readyUseCases()
	servablePreview(useCases, "jpeg-bytes")
	router := newTestRouter(t, useCases, enabledConfig())

	for _, method := range []string{http.MethodPost, http.MethodDelete, http.MethodPut} {
		request := httptest.NewRequest(method, previewPath(uuid.NewString()), nil)
		request.Header.Set("Authorization", "Bearer "+testToken)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code == http.StatusOK {
			t.Fatalf("%s must not serve a preview", method)
		}
	}
}

// --- Delivery metrics -------------------------------------------------------
//
// The preview route logged every outcome but counted none of them, so a
// dashboard could see the download endpoint's results and nothing at all for
// previews. It counts on nchat_file_previews_total, the counter the feature
// already owns; the delivery values are disjoint from the worker's generation
// vocabulary, so either question stays answerable by selecting on the label.

// previewMetricsRouter builds a router wired to a real metric registry and
// returns a function that scrapes it, following the pattern the other metric
// tests in this package use rather than reaching into the counter.
func previewMetricsRouter(t *testing.T, useCases *fakeUseCases) (http.Handler, func() string) {
	t.Helper()
	t.Setenv("PROMETHEUS_METRICS_ENABLED", "true")
	metrics := observability.NewMetrics(observability.LoadConfig("file-service"))
	attachments := httpapi.NewAttachmentMetrics(metrics)
	limiter := httpapi.NewUserRateLimiter(1000, time.Minute)
	t.Cleanup(limiter.Stop)

	router := httpapi.NewRouter(enabledConfig(), platformlog.New("file-service", "test"),
		httpapi.RouterDependencies{
			TokenValidator: staticValidator{token: testToken},
			Attachments:    useCases,
			RateLimiter:    limiter,
			Observability:  metrics,
			Metrics:        attachments,
		})
	scrape := func() string {
		exported := httptest.NewRecorder()
		router.ServeHTTP(exported, httptest.NewRequest(http.MethodGet, httpapi.RouteMetrics, nil))
		return exported.Body.String()
	}
	return router, scrape
}

// A: a served preview is counted once.
func TestPreviewCountsAServedResponse(t *testing.T) {
	useCases := readyUseCases()
	servablePreview(useCases, "jpeg-bytes")
	router, scrape := previewMetricsRouter(t, useCases)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, previewRequest(previewPath(uuid.NewString())))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.Code)
	}

	body := scrape()
	if want := `nchat_file_previews_total{result="served"} 1`; !strings.Contains(body, want) {
		t.Fatalf("expected %q in:\n%s", want, body)
	}
}

// B, C and D: every refusal is counted exactly once, under the code the client
// was given — so the metric and the response can never tell different stories.
func TestPreviewCountsEachRefusalUnderItsOwnCode(t *testing.T) {
	for name, tt := range map[string]struct {
		err        error
		wantStatus int
		wantResult string
	}{
		"no preview": {
			err: domain.ErrPreviewUnavailable, wantStatus: http.StatusConflict,
			wantResult: "preview_not_available",
		},
		"not scanned": {
			err: domain.ErrNotDownloadable, wantStatus: http.StatusForbidden,
			wantResult: "file_not_scanned",
		},
		"invisible attachment": {
			err: domain.ErrNotFound, wantStatus: http.StatusNotFound, wantResult: "not_found",
		},
		"storage down": {
			err: domain.ErrUnavailable, wantStatus: http.StatusServiceUnavailable,
			wantResult: "service_unavailable",
		},
	} {
		t.Run(name, func(t *testing.T) {
			useCases := readyUseCases()
			useCases.previewErr = tt.err
			router, scrape := previewMetricsRouter(t, useCases)

			response := httptest.NewRecorder()
			router.ServeHTTP(response, previewRequest(previewPath(uuid.NewString())))

			// The response is unchanged by this passage.
			if response.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, tt.wantStatus)
			}
			body := scrape()
			want := `nchat_file_previews_total{result="` + tt.wantResult + `"} 1`
			if !strings.Contains(body, want) {
				t.Fatalf("expected %q in:\n%s", want, body)
			}
			// One request, one increment: no other preview result was touched.
			if strings.Contains(body, `nchat_file_previews_total{result="served"}`) {
				t.Fatalf("a refused request was also counted as served:\n%s", body)
			}
		})
	}
}

// The counter must not be double-incremented, which a scrape after two requests
// makes visible in a way a single request cannot.
func TestPreviewCountsOneResultPerRequest(t *testing.T) {
	useCases := readyUseCases()
	useCases.previewErr = domain.ErrPreviewUnavailable
	router, scrape := previewMetricsRouter(t, useCases)

	for range 3 {
		router.ServeHTTP(httptest.NewRecorder(), previewRequest(previewPath(uuid.NewString())))
	}

	body := scrape()
	if want := `nchat_file_previews_total{result="preview_not_available"} 3`; !strings.Contains(body, want) {
		t.Fatalf("expected %q in:\n%s", want, body)
	}
}

// An unauthenticated request is refused before the handler reaches the use
// case, so it must not appear as a preview outcome at all.
func TestPreviewDoesNotCountRequestsItNeverServed(t *testing.T) {
	useCases := readyUseCases()
	servablePreview(useCases, "jpeg-bytes")
	router, scrape := previewMetricsRouter(t, useCases)

	unauthenticated := httptest.NewRequest(http.MethodGet, previewPath(uuid.NewString()), nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, unauthenticated)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", response.Code)
	}

	if body := scrape(); strings.Contains(body, "nchat_file_previews_total") {
		t.Fatalf("a request rejected before the use case was counted:\n%s", body)
	}
}
