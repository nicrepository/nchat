package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	platformlog "github.com/nicrepository/nchat/libs/go/platform/log"
	"github.com/nicrepository/nchat/services/notification-service/internal/config"
)

func readinessBody(t *testing.T, cfg config.Config, opts ...Option) (int, string) {
	t.Helper()
	router := NewRouter(cfg, platformlog.New("notification-service", "test"), opts...)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, RouteReadyz, nil))
	return recorder.Code, recorder.Body.String()
}

func workingSMTPConfig() config.Config {
	cfg := testConfig()
	cfg.SMTPWorkerEnabled = true
	cfg.SMTPHost = "smtp.example.com"
	cfg.SMTPFrom = "no-reply@example.com"
	cfg.SMTPTLSMode = "starttls"
	cfg.SMTPTimeoutSeconds = 10
	return cfg
}

// A deliberately disabled worker is not a fault.
func TestReadinessIgnoresADisabledSMTPWorker(t *testing.T) {
	cfg := testConfig()
	cfg.SMTPWorkerEnabled = false
	code, _ := readinessBody(t, cfg)
	if code != http.StatusOK {
		t.Fatalf("readiness returned %d with SMTP deliberately disabled", code)
	}
}

func TestReadinessIsHealthyWhenTheWorkerRuns(t *testing.T) {
	code, _ := readinessBody(t, workingSMTPConfig(), WithSMTPWorkerProbe(func() bool { return true }))
	if code != http.StatusOK {
		t.Fatalf("readiness returned %d with a running worker", code)
	}
}

// The reported case: a timeout that leaves no room to record the delivery
// inside the lease. The worker refuses to run, so the pod must not be Ready.
func TestReadinessFailsWhenTheLeaseCannotCoverProcessing(t *testing.T) {
	cfg := workingSMTPConfig()
	cfg.SMTPTimeoutSeconds = 25 // 25 + 5 grace == the 30s lease, so it does not fit
	if cfg.SMTPLeaseCoversProcessing() {
		t.Fatal("25s send timeout was accepted against a 30s lease")
	}
	code, body := readinessBody(t, cfg, WithSMTPWorkerProbe(func() bool { return false }))
	if code == http.StatusOK {
		t.Fatal("readiness stayed green on a configuration the worker refuses to run")
	}
	if !strings.Contains(body, "lease") {
		t.Fatalf("readiness did not explain the lease problem: %s", body)
	}
}

// A worker that dies after start-up must take readiness with it.
func TestReadinessFailsWhenTheWorkerHasStopped(t *testing.T) {
	code, body := readinessBody(t, workingSMTPConfig(), WithSMTPWorkerProbe(func() bool { return false }))
	if code == http.StatusOK {
		t.Fatal("readiness stayed green while the enabled worker was not running")
	}
	if !strings.Contains(body, "smtp-worker-running") {
		t.Fatalf("readiness did not name the stopped worker: %s", body)
	}
}

// Callers that supply no probe keep their previous behaviour.
func TestReadinessWithoutAProbeJudgesConfigurationOnly(t *testing.T) {
	code, _ := readinessBody(t, workingSMTPConfig())
	if code != http.StatusOK {
		t.Fatalf("readiness returned %d without a worker probe", code)
	}
}
