package httpapi_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	platformlog "github.com/nicrepository/nchat/libs/go/platform/log"
	"github.com/nicrepository/nchat/libs/go/platform/observability"
	httpapi "github.com/nicrepository/nchat/services/file-service/internal/http"
	"github.com/nicrepository/nchat/services/file-service/internal/service"

	"github.com/google/uuid"
)

func TestRateLimiterAllowsUpToTheLimitPerUser(t *testing.T) {
	limiter := httpapi.NewUserRateLimiter(2, time.Minute)
	t.Cleanup(limiter.Stop)

	for i := range 2 {
		if !limiter.Allow("user-a") {
			t.Fatalf("request %d must be allowed", i)
		}
	}
	if limiter.Allow("user-a") {
		t.Fatal("the third request must be refused")
	}
	// Budgets are per user, so one noisy uploader cannot block another.
	if !limiter.Allow("user-b") {
		t.Fatal("a different user must keep its own budget")
	}
}

func TestRateLimiterIgnoresUnidentifiedCallers(t *testing.T) {
	limiter := httpapi.NewUserRateLimiter(1, time.Minute)
	t.Cleanup(limiter.Stop)
	for range 5 {
		if !limiter.Allow("") {
			t.Fatal("an empty user id must not consume the budget")
		}
	}
}

func TestNilRateLimiterAllows(t *testing.T) {
	var limiter *httpapi.UserRateLimiter
	if !limiter.Allow("user") {
		t.Fatal("a nil limiter must not block")
	}
	limiter.Stop()
}

func TestRateLimiterReleasesTheBudgetAfterTheWindow(t *testing.T) {
	limiter := httpapi.NewUserRateLimiter(1, 40*time.Millisecond)
	t.Cleanup(limiter.Stop)

	if !limiter.Allow("user-a") {
		t.Fatal("the first request must be allowed")
	}
	if limiter.Allow("user-a") {
		t.Fatal("the second request must be refused inside the window")
	}
	time.Sleep(80 * time.Millisecond)
	if !limiter.Allow("user-a") {
		t.Fatal("the budget must recover after the window")
	}
}

// The garbage collector must drop idle users so a long-lived process does not
// accumulate one entry per user that ever uploaded.
func TestRateLimiterCollectsIdleUsers(t *testing.T) {
	limiter := httpapi.NewUserRateLimiter(1, 30*time.Millisecond)
	t.Cleanup(limiter.Stop)

	if !limiter.Allow("transient-user") {
		t.Fatal("the first request must be allowed")
	}
	time.Sleep(120 * time.Millisecond)
	if !limiter.Allow("transient-user") {
		t.Fatal("a collected user must start with a fresh budget")
	}
}

func TestRateLimiterStopIsIdempotent(t *testing.T) {
	limiter := httpapi.NewUserRateLimiter(1, time.Minute)
	limiter.Stop()
	limiter.Stop()
}

func TestRateLimiterRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		limit  int
		window time.Duration
	}{
		{name: "zero limit", limit: 0, window: time.Minute},
		{name: "negative limit", limit: -1, window: time.Minute},
		{name: "zero window", limit: 1, window: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("expected a panic for an unusable limiter configuration")
				}
			}()
			httpapi.NewUserRateLimiter(tt.limit, tt.window)
		})
	}
}

func TestRateLimiterMiddlewareIgnoresUnauthenticatedRequests(t *testing.T) {
	limiter := httpapi.NewUserRateLimiter(1, time.Minute)
	t.Cleanup(limiter.Stop)

	var served int
	handler := limiter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served++
		w.WriteHeader(http.StatusOK)
	}))
	for range 3 {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
		if response.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", response.Code)
		}
	}
	if served != 3 {
		t.Fatalf("expected the handler to run 3 times, got %d", served)
	}
}

// Attachment counters must be exported without any unbounded label: no
// attachment id, workspace, filename or path.
func TestAttachmentMetricsAreExportedWithBoundedLabels(t *testing.T) {
	t.Setenv("PROMETHEUS_METRICS_ENABLED", "true")
	obsCfg := observability.LoadConfig("file-service")
	metrics := observability.NewMetrics(obsCfg)
	attachmentMetrics := httpapi.NewAttachmentMetrics(metrics)
	attachmentMetrics.ObserveOrphanedObject()

	useCases := readyUseCases()
	limiter := httpapi.NewUserRateLimiter(100, time.Minute)
	t.Cleanup(limiter.Stop)
	router := httpapi.NewRouter(enabledConfig(), platformlog.New("file-service", "test"),
		httpapi.RouterDependencies{
			TokenValidator: staticValidator{token: testToken},
			Attachments:    useCases,
			RateLimiter:    limiter,
			Observability:  metrics,
			Metrics:        attachmentMetrics,
		})

	upload := httptest.NewRecorder()
	router.ServeHTTP(upload, uploadRequest(t, channelUploadPath(testChannelID), fileOf("payload")))
	if upload.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", upload.Code)
	}

	useCases.download = service.Download{
		Filename: "report.pdf", ContentType: "application/pdf",
		Size: 7, Content: io.NopCloser(strings.NewReader("payload")),
	}
	download := httptest.NewRecorder()
	router.ServeHTTP(download, downloadRequest(t, uuid.NewString()))
	if download.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", download.Code)
	}

	exported := httptest.NewRecorder()
	router.ServeHTTP(exported, httptest.NewRequest(http.MethodGet, httpapi.RouteMetrics, nil))
	body := exported.Body.String()

	for _, metric := range []string{
		"nchat_file_uploads_total",
		"nchat_file_downloads_total",
		"nchat_file_orphaned_objects_total",
	} {
		if !strings.Contains(body, metric) {
			t.Fatalf("expected %s to be exported:\n%s", metric, body)
		}
	}
	for _, forbidden := range []string{
		useCases.uploadView.ID, "filename", "report.pdf",
		"nchat/attachments", testUserID,
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("metrics must not carry %q as a label", forbidden)
		}
	}
}

func TestNilAttachmentMetricsAreSafe(t *testing.T) {
	var metrics *httpapi.AttachmentMetrics
	metrics.ObserveOrphanedObject()
}
