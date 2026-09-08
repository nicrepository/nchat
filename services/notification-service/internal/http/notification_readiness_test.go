package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nicrepository/nchat/libs/go/platform/observability"
	"github.com/nicrepository/nchat/services/notification-service/internal/config"
)

// Issue #742: readiness has to observe the outbox worker, not only the
// configuration that was supposed to start it. A pod that stays green with
// nothing draining the backlog is the failure this guards.

func workingNotificationConfig() config.Config {
	cfg := testConfig()
	cfg.DatabaseURL = "postgres://user@127.0.0.1:5432/nchat?sslmode=disable"
	cfg.NotificationWorker = config.NotificationWorkerConfig{Enabled: true}.Normalized()
	return cfg
}

// Disabled on purpose is not a fault, and it is the default everywhere.
func TestReadinessIgnoresADisabledNotificationWorker(t *testing.T) {
	code, _ := readinessBody(t, testConfig())
	if code != http.StatusOK {
		t.Fatalf("readiness returned %d with the notification worker deliberately disabled", code)
	}
}

func TestReadinessIsHealthyWhenTheNotificationWorkerRuns(t *testing.T) {
	code, _ := readinessBody(t, workingNotificationConfig(),
		WithNotificationWorkerProbe(func() bool { return true }))
	if code != http.StatusOK {
		t.Fatalf("readiness returned %d with a running notification worker", code)
	}
}

// The worker refuses to run without a database, so the pod must not claim it.
func TestReadinessFailsWhenTheNotificationWorkerHasNoDatabase(t *testing.T) {
	cfg := workingNotificationConfig()
	cfg.DatabaseURL = ""

	code, body := readinessBody(t, cfg, WithNotificationWorkerProbe(func() bool { return false }))
	if code == http.StatusOK {
		t.Fatal("readiness stayed green on a worker that cannot reach an outbox")
	}
	if !strings.Contains(body, "DATABASE_URL") {
		t.Fatalf("readiness did not explain the missing database: %s", body)
	}
}

// A lease too short to cover a batch is the setting that lets two workers
// deliver one notification. The worker refuses it, so readiness must too.
func TestReadinessFailsWhenTheNotificationLeaseCannotCoverABatch(t *testing.T) {
	cfg := workingNotificationConfig()
	cfg.NotificationWorker.BatchSize = 200
	cfg.NotificationWorker.MaxConcurrency = 1
	cfg.NotificationWorker.DeliveryTimeoutSeconds = 120
	cfg.NotificationWorker.LeaseSeconds = 60

	code, body := readinessBody(t, cfg, WithNotificationWorkerProbe(func() bool { return false }))
	if code == http.StatusOK {
		t.Fatal("readiness stayed green on a lease the worker refuses to run under")
	}
	if !strings.Contains(body, "LEASE") {
		t.Fatalf("readiness did not name the lease problem: %s", body)
	}
}

// A worker that stopped after start-up must take readiness with it.
func TestReadinessFailsWhenTheNotificationWorkerHasStopped(t *testing.T) {
	code, body := readinessBody(t, workingNotificationConfig(),
		WithNotificationWorkerProbe(func() bool { return false }))
	if code == http.StatusOK {
		t.Fatal("readiness stayed green while the enabled worker was not running")
	}
	if !strings.Contains(body, "notification-worker-running") {
		t.Fatalf("readiness did not name the stopped worker: %s", body)
	}
}

// Callers that supply no probe keep judging the configuration alone.
func TestReadinessWithoutANotificationProbeJudgesConfigurationOnly(t *testing.T) {
	code, _ := readinessBody(t, workingNotificationConfig())
	if code != http.StatusOK {
		t.Fatalf("readiness returned %d without a worker probe", code)
	}
}

// The worker registers its collectors during wiring, before the router exists,
// so the two must share one registry or /metrics would serve everything except
// the worker's own numbers.
func TestRouterServesTheRegistryItWasGiven(t *testing.T) {
	metrics := observability.NewMetrics(observability.LoadConfig("notification-service"))
	router := NewRouter(testConfig(), nil, WithMetrics(metrics))

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, RouteMetrics, nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("/metrics returned %d", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "nchat_service_info") {
		t.Fatalf("/metrics did not serve the supplied registry: %s", recorder.Body.String())
	}
}
