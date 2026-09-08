package worker

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/observability"
)

// Issue #742: the metrics are a privacy boundary as much as an operational one.
// Every label value has to come from the closed set in this package, because a
// label derived from a notification, a recipient or a workspace would make the
// series count grow with traffic and would publish an identifier this service
// exists to keep private.

func newTestMetrics(t *testing.T) (*NotificationMetrics, *observability.Metrics) {
	t.Helper()
	cfg := observability.LoadConfig("notification-service")
	cfg.MetricsEnabled = true
	shared := observability.NewMetrics(cfg)
	return NewNotificationMetrics(shared), shared
}

// scrape reads the shared registry the way Prometheus would.
func scrape(t *testing.T, shared *observability.Metrics) string {
	t.Helper()
	recorder := httptest.NewRecorder()
	shared.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("/metrics returned %d", recorder.Code)
	}
	return recorder.Body.String()
}

func TestNotificationMetricsAreServedOnTheSharedRegistry(t *testing.T) {
	metrics, shared := newTestMetrics(t)

	metrics.ObserveBacklog(7)
	metrics.Count(resultEligible, 2)
	metrics.ObserveDelivery(resultDelivered, 250*time.Millisecond)

	body := scrape(t, shared)
	for _, name := range []string{
		"nchat_notification_outbox_backlog",
		"nchat_notification_events_total",
		"nchat_notification_delivery_duration_seconds",
	} {
		if !strings.Contains(body, name) {
			t.Fatalf("%s was not exported:\n%s", name, body)
		}
	}
	if !strings.Contains(body, "nchat_notification_outbox_backlog 7") {
		t.Fatalf("the backlog gauge did not record its value:\n%s", body)
	}
}

// The one label is `result`, and its values are decided here. Nothing derived
// from a row may appear.
func TestNotificationMetricsCarryOnlyTheResultLabel(t *testing.T) {
	metrics, shared := newTestMetrics(t)

	metrics.Count(resultSuppressed, 1)
	metrics.Count(resultLeaseLost, 1)
	metrics.ObserveDelivery(resultFailed, time.Second)

	body := scrape(t, shared)
	for _, forbidden := range []string{
		"notification_id", "workspace", "recipient", "user_id", "message_id", "worker_id",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("an identifier leaked into a metric label (%s):\n%s", forbidden, body)
		}
	}
}

// A nil set is the metrics-disabled path, and every entry point has to survive
// it: the worker never checks before recording.
func TestNotificationMetricsAreSafeWhenAbsent(t *testing.T) {
	var metrics *NotificationMetrics

	metrics.ObserveBacklog(3)
	metrics.Count(resultClaimed, 1)
	metrics.ObserveDelivery(resultRetry, time.Second)
}

// Counting nothing must record nothing, so an empty pass does not inflate the
// series with zero-valued additions.
func TestNotificationMetricsIgnoreAnEmptyCount(t *testing.T) {
	metrics, shared := newTestMetrics(t)

	metrics.Count(resultExhausted, 0)

	if strings.Contains(scrape(t, shared), resultExhausted) {
		t.Fatal("a count of zero created a series")
	}
}

// Registration must not panic when the shared registry has metrics disabled;
// the collectors then work as no-op accumulators.
func TestNotificationMetricsToleratesADisabledRegistry(t *testing.T) {
	cfg := observability.LoadConfig("notification-service")
	cfg.MetricsEnabled = false

	metrics := NewNotificationMetrics(observability.NewMetrics(cfg))
	metrics.Count(resultError, 1)
	metrics.ObserveBacklog(1)
}

// The worker records through a real metrics set on every path, so a label that
// was never declared would surface here rather than at runtime.
func TestWorkerRecordsEveryOutcomeItProduces(t *testing.T) {
	metrics, shared := newTestMetrics(t)
	outbox := newFakeOutbox()
	outbox.seedPending("n1")
	worker := NewNotificationWorker(notificationTestConfig(), NotificationWorkerDeps{
		Store:     outbox,
		Deliverer: &recordingDeliverer{},
		Metrics:   metrics,
		Logger:    silentLogger(),
	})

	worker.runPass()

	body := scrape(t, shared)
	for _, result := range []string{resultEligible, resultClaimed, resultDelivered} {
		if !strings.Contains(body, `result="`+result+`"`) {
			t.Fatalf("outcome %q was not counted:\n%s", result, body)
		}
	}
}
