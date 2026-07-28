package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/health"
	platformlog "github.com/nicrepository/nchat/libs/go/platform/log"
	httpapi "github.com/nicrepository/nchat/services/file-service/internal/http"
)

type fakePinger struct {
	mu    sync.Mutex
	err   error
	block time.Duration
}

func (p *fakePinger) Ping(ctx context.Context) error {
	p.mu.Lock()
	err, block := p.err, p.block
	p.mu.Unlock()
	if block > 0 {
		select {
		case <-time.After(block):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}

func readinessBody(t *testing.T, handler http.Handler) (int, health.Response) {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, httpapi.RouteReadyz, nil))

	var envelope struct {
		Data health.Response `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode readiness: %v", err)
	}
	return response.Code, envelope.Data
}

func checkByName(t *testing.T, checks []health.CheckResult, name string) health.CheckResult {
	t.Helper()
	for _, check := range checks {
		if check.Name == name {
			return check
		}
	}
	t.Fatalf("expected a %q check in %+v", name, checks)
	return health.CheckResult{}
}

func hasCheck(checks []health.CheckResult, name string) bool {
	for _, check := range checks {
		if check.Name == name {
			return true
		}
	}
	return false
}

func TestReadinessHasNoDependencyChecksWhileUploadsAreDisabled(t *testing.T) {
	cfg := enabledConfig()
	cfg.UploadsEnabled = false
	router := httpapi.NewRouter(cfg, platformlog.New("file-service", "test"))

	status, body := readinessBody(t, router)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	if hasCheck(body.Checks, "postgres") || hasCheck(body.Checks, "object-storage") {
		t.Fatalf("a health-only deployment must not require dependencies: %+v", body.Checks)
	}
}

func TestReadinessChecksBothDependenciesWhenUploadsAreEnabled(t *testing.T) {
	router := httpapi.NewRouter(enabledConfig(), platformlog.New("file-service", "test"),
		httpapi.RouterDependencies{
			ReadinessPinger: &fakePinger{},
			StoragePinger:   &fakePinger{},
		})

	status, body := readinessBody(t, router)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %+v", status, body)
	}
	for _, name := range []string{"postgres", "object-storage"} {
		check := checkByName(t, body.Checks, name)
		if check.Status != health.CheckPass || !check.Critical {
			t.Fatalf("expected %q to pass and be critical, got %+v", name, check)
		}
	}
}

func TestReadinessFailsWhenADependencyIsDown(t *testing.T) {
	tests := []struct {
		name    string
		deps    httpapi.RouterDependencies
		failing string
	}{
		{
			name:    "database down",
			deps:    httpapi.RouterDependencies{ReadinessPinger: &fakePinger{err: errors.New("dial tcp db-primary.internal:5432: refused")}, StoragePinger: &fakePinger{}},
			failing: "postgres",
		},
		{
			name:    "storage down",
			deps:    httpapi.RouterDependencies{ReadinessPinger: &fakePinger{}, StoragePinger: &fakePinger{err: errors.New("seaweedfs-filer refused")}},
			failing: "object-storage",
		},
		{
			name:    "database missing",
			deps:    httpapi.RouterDependencies{StoragePinger: &fakePinger{}},
			failing: "postgres",
		},
		{
			name:    "storage missing",
			deps:    httpapi.RouterDependencies{ReadinessPinger: &fakePinger{}},
			failing: "object-storage",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := httpapi.NewRouter(enabledConfig(), platformlog.New("file-service", "test"), tt.deps)

			status, body := readinessBody(t, router)
			if status == http.StatusOK {
				t.Fatalf("expected readiness to fail, got %d", status)
			}
			check := checkByName(t, body.Checks, tt.failing)
			if check.Status == health.CheckPass {
				t.Fatalf("expected %q to fail, got %+v", tt.failing, check)
			}
			// The probe must not leak the DSN, the hostname or the driver text.
			for _, leak := range []string{"db-primary.internal", "5432", "seaweedfs-filer"} {
				if bodyContains(body, leak) {
					t.Fatalf("readiness leaked %q: %+v", leak, body)
				}
			}
		})
	}
}

func TestReadinessReportsADependencyTimeout(t *testing.T) {
	router := httpapi.NewRouter(enabledConfig(), platformlog.New("file-service", "test"),
		httpapi.RouterDependencies{
			ReadinessPinger: &fakePinger{block: 10 * time.Second},
			StoragePinger:   &fakePinger{},
		})

	status, body := readinessBody(t, router)
	if status == http.StatusOK {
		t.Fatalf("expected readiness to fail, got %d", status)
	}
	check := checkByName(t, body.Checks, "postgres")
	if check.Status == health.CheckPass {
		t.Fatalf("expected the slow dependency to fail, got %+v", check)
	}
}

func bodyContains(response health.Response, needle string) bool {
	encoded, err := json.Marshal(response)
	if err != nil {
		return false
	}
	return strings.Contains(string(encoded), needle)
}
