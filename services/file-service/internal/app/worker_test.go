package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/file-service/internal/config"
	"github.com/nicrepository/nchat/services/file-service/internal/storage"
)

func isolatedApp(t *testing.T, mode string) (*App, *stubPool) {
	t.Helper()
	cfg := uploadsEnabledConfig(t)
	cfg.BootMode = mode
	cfg.MalwareScannerAddress = "127.0.0.1:3310"
	cfg.MalwareSignatureMaxAgeSeconds = 86400
	cfg.DocumentConverterURL = "http://document-converter:8089"
	cfg.DocumentConverterTimeoutSeconds = 35
	if mode != "api" {
		cfg.AuthJWTHMACSecret = ""
	}
	pool := &stubPool{}
	deps := appDependencies{openDB: openStub(pool), openFence: fenceStub(), tracingShutdown: noopShutdown}
	if mode == "api" {
		deps.openAdmission = admissionStub()
	}
	application, err := newApp(cfg, deps)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := application.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	return application, pool
}

func TestIsolatedWorkloadResponsibilities(t *testing.T) {
	for _, mode := range []string{"api", "scan-worker", "preview-worker"} {
		t.Run(mode, func(t *testing.T) {
			application, _ := isolatedApp(t, mode)
			checks := map[string]bool{
				"scan":         (application.scanDone != nil) == (mode == "scan-worker"),
				"preview":      (application.previewDone != nil) == (mode == "preview-worker"),
				"cleanup":      (application.cleanupDone != nil) == (mode == "api"),
				"draft expiry": (application.draftExpiryDone != nil) == (mode == "api"),
				"URL scan":     application.linkScanDone == nil,
			}
			for name, correct := range checks {
				if !correct {
					t.Errorf("%s consumer in wrong role", name)
				}
			}
		})
	}
}

func TestWorkerHTTPHasOnlyInternalRoutes(t *testing.T) {
	for _, mode := range []string{"scan-worker", "preview-worker"} {
		application, _ := isolatedApp(t, mode)
		for route, status := range map[string]int{"/attachments/id/content": http.StatusNotFound, "/healthz": http.StatusOK, "/metrics": http.StatusOK} {
			response := httptest.NewRecorder()
			application.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, route, nil))
			if response.Code != status {
				t.Errorf("%s %s: %d", mode, route, response.Code)
			}
		}
	}
}

func TestTerminationStopsWorkerBeforeHTTPDrain(t *testing.T) {
	for _, mode := range []string{"scan-worker", "preview-worker"} {
		application, pool := isolatedApp(t, mode)
		if err := application.StopWorkers(context.Background()); err != nil {
			t.Fatal(err)
		}
		done := application.scanDone
		if mode == "preview-worker" {
			done = application.previewDone
		}
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("worker did not stop on termination")
		}
		if pool.closed {
			t.Fatal("termination hook closed pool before HTTP drain")
		}
	}
}

func TestWorkerStartupFailsClosedAndClosesPool(t *testing.T) {
	for _, failure := range []string{"fence", "missing-fence", "scanner"} {
		t.Run(failure, func(t *testing.T) {
			cfg := uploadsEnabledConfig(t)
			cfg.BootMode = "scan-worker"
			cfg.MalwareScannerAddress = "clamav:3310"
			cfg.MalwareSignatureMaxAgeSeconds = 86400
			pool := &stubPool{}
			deps := appDependencies{openDB: openStub(pool), openFence: fenceStub(), tracingShutdown: noopShutdown}
			configureWorkerFailure(failure, &cfg, &deps)
			if _, err := newApp(cfg, deps); err == nil {
				t.Fatal("accepted unusable worker")
			}
			if !pool.closed {
				t.Fatal("failed startup leaked pool")
			}
		})
	}
}

func configureWorkerFailure(failure string, cfg *config.Config, deps *appDependencies) {
	switch failure {
	case "fence":
		deps.openFence = func(storage.Pool, *slog.Logger) (storage.AttachmentFencing, error) {
			return nil, errors.New("unavailable")
		}
	case "missing-fence":
		deps.openFence = nil
	case "scanner":
		cfg.MalwareSignatureMaxAgeSeconds = 0
	}
}

func TestIsolatedRolesReportOnlyTheirLocalScanCapacity(t *testing.T) {
	t.Setenv("PROMETHEUS_METRICS_ENABLED", "true")
	for _, mode := range []string{"api", "scan-worker", "preview-worker"} {
		application, _ := isolatedApp(t, mode)
		response := httptest.NewRecorder()
		application.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		capacity := "0"
		if mode == "scan-worker" {
			capacity = "1"
		}
		body := response.Body.String()
		if !strings.Contains(body, "nchat_file_malware_scan_capacity "+capacity+"\n") {
			t.Fatalf("%s reports incorrect scan capacity", mode)
		}
		if strings.Count(body, "# TYPE nchat_file_malware_scan_capacity gauge") != 1 {
			t.Fatalf("%s duplicated scan metrics", mode)
		}
	}
}

func TestPreviewWorkerRejectsConverterBeforeOpeningDatabase(t *testing.T) {
	for _, tc := range []struct {
		name, url string
		timeout   int
	}{
		{"invalid origin", "invalid", 35},
		{"empty origin", "", 35},
		{"credentials", "http://user:pass@converter:8089", 35},
		{"zero timeout", "http://converter:8089", 0},
		{"negative timeout", "http://converter:8089", -1},
		{"timeout at ceiling", "http://converter:8089", 40},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := uploadsEnabledConfig(t)
			cfg.BootMode = "preview-worker"
			cfg.DocumentConverterURL = tc.url
			cfg.DocumentConverterTimeoutSeconds = tc.timeout
			opened := false
			deps := appDependencies{tracingShutdown: noopShutdown,
				openDB: func(context.Context, string, int, int) (storage.Pool, error) {
					opened = true
					return &stubPool{}, nil
				},
			}
			if err := cfg.Validate(); err == nil {
				t.Fatal("invalid converter accepted by configuration")
			}
			if _, err := newApp(cfg, deps); err == nil {
				t.Fatal("invalid preview worker started")
			}
			if opened {
				t.Fatal("database opened before converter configuration validation")
			}
		})
	}
}

func TestShutdownDiagnosticForUnfinishedScanWorker(t *testing.T) {
	var output bytes.Buffer
	pool := &stubPool{}
	application := &App{Logger: slog.New(slog.NewJSONHandler(&output, nil)), pool: pool,
		scanCancel: func() {}, scanDone: make(chan struct{}),
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := application.Shutdown(ctx); !errors.Is(err, errShutdownIncomplete) {
		t.Fatalf("shutdown error = %v", err)
	}
	var record struct {
		Message    string `json:"msg"`
		Reason     string `json:"reason"`
		PoolClosed bool   `json:"pool_closed"`
	}
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record.Message != "shutdown incomplete" || record.Reason != "worker_shutdown_incomplete" {
		t.Fatalf("misleading shutdown diagnostic: %+v", record)
	}
	if pool.closed || record.PoolClosed {
		t.Fatal("incomplete shutdown must retain the pool")
	}
}
