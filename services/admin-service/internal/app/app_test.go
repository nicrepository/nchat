package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/nicrepository/nchat/services/admin-service/internal/config"
	httpapi "github.com/nicrepository/nchat/services/admin-service/internal/http"
	"github.com/nicrepository/nchat/services/admin-service/internal/storage"
)

type fakePool struct{ closed bool }

func (f *fakePool) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("not implemented")
}

// QueryRow returns a row that answers rather than a nil interface. A nil here
// would make any future test that reaches the store panic on Scan, which is a
// crash for a reason that has nothing to do with what the test is checking.
func (f *fakePool) QueryRow(context.Context, string, ...any) pgx.Row {
	return errRow{err: pgx.ErrNoRows}
}

// errRow is the smallest thing that satisfies pgx.Row: it reports one fixed
// error. Deliberately not a mock framework — the app tests only need the pool
// to be safe to call, not to be programmable.
type errRow struct{ err error }

func (r errRow) Scan(...any) error { return r.err }
func (f *fakePool) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

// Begin refuses rather than returning a nil transaction: the app tests never
// reach a mutation, and a nil pgx.Tx would turn a future test that does into a
// panic instead of a failure that names the missing wiring.
func (f *fakePool) Begin(context.Context) (pgx.Tx, error) {
	return nil, errors.New("not implemented")
}

func (f *fakePool) Ping(context.Context) error { return nil }
func (f *fakePool) Close()                     { f.closed = true }

func adminAPIConfig() config.Config {
	return config.Config{
		ServiceName:              "admin-service",
		Env:                      "test",
		Port:                     8085,
		ReadHeaderTimeoutSeconds: 5,
		DatabaseURL:              "postgres://localhost/nchat",
		AuthJWTHMACSecret:        "0123456789abcdef0123456789abcdef",
		AuthJWTIssuer:            "nchat-auth",
		AuthJWTAudience:          "nchat-api",
		SessionIdleTTL:           15 * time.Minute,
		SessionAbsoluteTTL:       8 * time.Hour,
	}
}

func openFake(pool *fakePool) func(context.Context, string, int) (storage.Pool, error) {
	return func(context.Context, string, int) (storage.Pool, error) { return pool, nil }
}

func noTracing(context.Context) error { return nil }

// The health-only deployment mode: no DATABASE_URL, no JWT secret. It is
// supported, so it must still start.
func TestNewCreatesApp(t *testing.T) {
	cfg := config.Config{ServiceName: "admin-service", Env: "test", Port: 8085, ReadHeaderTimeoutSeconds: 5}
	app, err := New(cfg)
	if err != nil {
		t.Fatalf("a service without the admin api must still start: %v", err)
	}
	if app == nil || app.Logger == nil || app.Handler == nil {
		t.Fatalf("expected initialized app, got %+v", app)
	}
	if app.Config.ServiceName != cfg.ServiceName || app.Config.Port != cfg.Port {
		t.Fatalf("expected config %+v, got %+v", cfg, app.Config)
	}
}

func TestNewApp_WiresTheAdminAPIWhenFullyConfigured(t *testing.T) {
	pool := &fakePool{}
	app, err := newApp(adminAPIConfig(), appDependencies{openDB: openFake(pool), tracingShutdown: noTracing})
	if err != nil {
		t.Fatalf("newApp: %v", err)
	}

	response := httptest.NewRecorder()
	app.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, httpapi.RouteAdminBootstrap, nil))
	// Wired but unauthenticated: 401, not the 503 an unwired service answers.
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 from a wired admin api, got %d", response.Code)
	}

	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if !pool.closed {
		t.Fatal("expected the pool to be closed on shutdown")
	}
	// Shutdown is idempotent: the process may call it from more than one path.
	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("second Shutdown: %v", err)
	}
}

// The Admin API is not configured at all — the health-only mode. Startup
// succeeds and the privileged routes refuse, which is the supported contract
// for a deployment that runs this service for /healthz and /version.
func TestNewApp_UnconfiguredAdminAPIStartsAndRefusesPrivilegedRoutes(t *testing.T) {
	app, err := newApp(
		config.Config{ServiceName: "admin-service", Env: "test"},
		appDependencies{openDB: openFake(&fakePool{}), tracingShutdown: noTracing},
	)
	if err != nil {
		t.Fatalf("the health-only mode must start: %v", err)
	}

	response := httptest.NewRecorder()
	app.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, httpapi.RouteAdminBootstrap, nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", response.Code)
	}
	response = httptest.NewRecorder()
	app.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, httpapi.RouteHealthz, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("expected healthz to stay 200, got %d", response.Code)
	}
}

// A configured Admin API that cannot be built must fail startup rather than
// degrade. Nothing in this process reopens the database, so a degraded service
// would serve /healthz forever with /readyz stuck at 503 — and a readiness
// probe alone never restarts a container.
func TestNewApp_ConfiguredAdminAPIThatCannotBeBuiltFailsStartup(t *testing.T) {
	tests := map[string]struct {
		cfg    config.Config
		openDB func(context.Context, string, int) (storage.Pool, error)
	}{
		"database unreachable": {
			cfg: adminAPIConfig(),
			openDB: func(context.Context, string, int) (storage.Pool, error) {
				return nil, errors.New("connection refused")
			},
		},
		"no database driver wired": {
			cfg:    adminAPIConfig(),
			openDB: nil,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			app, err := newApp(tt.cfg, appDependencies{openDB: tt.openDB, tracingShutdown: noTracing})
			if err == nil {
				t.Fatal("expected startup to fail rather than produce an unrecoverable service")
			}
			if app != nil {
				t.Fatal("a failed startup must not hand back a service to serve with")
			}
			// The DSN carries the database password and must never reach a log
			// line or an error string.
			if strings.Contains(err.Error(), tt.cfg.DatabaseURL) && tt.cfg.DatabaseURL != "" {
				t.Fatalf("startup error leaked the DSN: %v", err)
			}
		})
	}
}

// A JWT secret long enough for the session service but rejected by the token
// validator would leave the handshake unguarded. It is a configured Admin API
// that cannot be built, so it fails startup and releases the pool.
func TestNewApp_InvalidJWTConfigurationFailsStartupAndClosesThePool(t *testing.T) {
	cfg := adminAPIConfig()
	cfg.AuthJWTIssuer = "  "
	pool := &fakePool{}

	app, err := newApp(cfg, appDependencies{openDB: openFake(pool), tracingShutdown: noTracing})

	if err == nil {
		t.Fatal("expected startup to fail")
	}
	if app != nil {
		t.Fatal("a failed startup must not hand back a service")
	}
	if !pool.closed {
		t.Fatal("expected the pool to be released when startup is abandoned")
	}
}

func TestShutdown_OnNilAppIsSafe(t *testing.T) {
	var app *App
	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

// Calling into the wired store must be safe: the fake pool answers rather than
// returning a nil row, so a test that exercises a code path reaching the
// database fails on its own assertion instead of panicking.
func TestFakePool_QueryRowIsSafeToScan(t *testing.T) {
	pool := &fakePool{}

	var value string
	if err := pool.QueryRow(context.Background(), "SELECT 1").Scan(&value); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expected pgx.ErrNoRows, got %v", err)
	}
}
