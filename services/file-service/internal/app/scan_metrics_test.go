package app

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestScanMetricsRegisteredOnceWithAndWithoutUploads(t *testing.T) {
	t.Setenv("PROMETHEUS_METRICS_ENABLED", "true")
	for _, uploads := range []bool{false, true} {
		cfg := healthOnlyConfig()
		if uploads {
			cfg = uploadsEnabledConfig(t)
		}
		application, err := newApp(cfg, appDependencies{openDB: openStub(&stubPool{}), openAdmission: admissionStub(), openFence: fenceStub(), tracingShutdown: noopShutdown})
		if err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		application.Handler.ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
		body := response.Body.String()
		value := "0"
		if uploads {
			value = "1"
		}
		if !strings.Contains(body, "nchat_file_malware_scan_required "+value+"\n") || strings.Count(body, "# TYPE nchat_file_malware_scan_required gauge") != 1 {
			t.Fatal("wrong required flag or duplicate registry")
		}
		if err := application.Shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}
