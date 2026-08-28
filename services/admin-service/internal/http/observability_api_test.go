package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
	"github.com/nicrepository/nchat/services/admin-service/internal/service"
)

// The observability surface's specs. What they mostly assert is what the
// endpoints refuse: no capability, no session, no destination, and no way to
// turn a refresh into an amplifier.

type stubObservability struct {
	summary  service.DashboardSummary
	snapshot domain.HealthSnapshot
	err      error
	// forced records every `force` value the handlers passed through, which is
	// how the specs assert that only the POST recollects.
	forced []bool
	calls  atomic.Int32
}

func (s *stubObservability) Summary(context.Context) (service.DashboardSummary, error) {
	if s.err != nil {
		return service.DashboardSummary{}, s.err
	}
	return s.summary, nil
}

func (s *stubObservability) Snapshot(_ context.Context, force bool) (domain.HealthSnapshot, error) {
	s.calls.Add(1)
	s.forced = append(s.forced, force)
	if s.err != nil {
		return domain.HealthSnapshot{}, s.err
	}
	return s.snapshot, nil
}

func healthFixture() *stubObservability {
	latency := int64(12)
	collectedAt := time.Unix(1700000000, 0).UTC()
	postgres, _ := domain.LookupHealthService(domain.HealthServicePostgres)
	smtp, _ := domain.LookupHealthService(domain.HealthServiceSMTP)
	livekit, _ := domain.LookupHealthService(domain.HealthServiceLiveKit)

	snapshot := domain.HealthSnapshot{
		CollectedAt: collectedAt,
		Services: []domain.ServiceHealth{
			{Descriptor: postgres, State: domain.HealthHealthy, Enabled: true, Observable: true,
				LatencyMS: &latency, CheckedAt: collectedAt},
			{Descriptor: smtp, State: domain.HealthDisabled, Enabled: false, Observable: true,
				CheckedAt: collectedAt},
			{Descriptor: livekit, State: domain.HealthUnavailable, Enabled: true, Observable: true,
				CheckedAt: collectedAt, ErrorCategory: domain.HealthErrorConnectionTimeout,
				Detail: "A dependência não respondeu dentro do tempo limite do check."},
		},
	}
	return &stubObservability{
		snapshot: snapshot,
		summary: service.DashboardSummary{
			CollectedAt:      collectedAt,
			Overall:          snapshot.Overall(),
			StateCounts:      snapshot.CountByState(),
			Metrics:          domain.PlatformMetrics(domain.PlatformCounters{UsersTotal: 12, Messages24h: 431}, true),
			MetricsAvailable: true,
			Alerts:           domain.DeriveAlerts(snapshot),
		},
	}
}

func observabilityHarness(t *testing.T, ports *stubObservability, capabilities ...domain.Capability) *testHarness {
	t.Helper()
	if len(capabilities) == 0 {
		capabilities = []domain.Capability{domain.CapabilityInfrastructureRead}
	}
	return newHarness(t, adminStore(capabilities...), withObservability(NewObservabilityPorts(ports, ports)))
}

func decodeBody(t *testing.T, response *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v (%s)", err, response.Body.String())
	}
	return envelope.Data
}

func TestOverviewServesTheWholeDashboardInOneRequest(t *testing.T) {
	ports := healthFixture()
	harness := observabilityHarness(t, ports)
	cookie, csrf := harness.establish(t)

	response := harness.do(harness.authenticated(t, http.MethodGet, RouteAdminOverview, cookie, csrf))
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", response.Code, response.Body.String())
	}
	summary, ok := decodeBody(t, response)["summary"].(map[string]any)
	if !ok {
		t.Fatal("the response carries no summary")
	}
	for _, field := range []string{"collected_at", "overall", "state_counts", "metrics", "alerts"} {
		if _, present := summary[field]; !present {
			t.Errorf("the summary is missing %s", field)
		}
	}
	if summary["overall"] != string(domain.HealthDegraded) {
		t.Errorf("expected the overall state to reflect the unavailable integration, got %v", summary["overall"])
	}
}

func TestHealthCenterListsEveryCollectedService(t *testing.T) {
	ports := healthFixture()
	harness := observabilityHarness(t, ports)
	cookie, csrf := harness.establish(t)

	response := harness.do(harness.authenticated(t, http.MethodGet, RouteAdminHealth, cookie, csrf))
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", response.Code, response.Body.String())
	}
	body := decodeBody(t, response)
	services, ok := body["services"].([]any)
	if !ok || len(services) != 3 {
		t.Fatalf("expected three services, got %v", body["services"])
	}
	// The most troubled row comes first, so an operator opening the page reads
	// the problem before the noise.
	first, _ := services[0].(map[string]any)
	if first["state"] != string(domain.HealthUnavailable) {
		t.Fatalf("expected the unavailable service first, got %v", first["state"])
	}
	assertRowsCarryTheirCheckTimestamp(t, services)
	// A read must not recollect: only the explicit refresh does.
	if len(ports.forced) != 1 || ports.forced[0] {
		t.Fatalf("the listing forced a collection: %v", ports.forced)
	}
}

// assertRowsCarryTheirCheckTimestamp checks the two rules every row obeys: a
// check that ran reports when it ran, and one that did not run omits the
// latency entirely rather than reporting a round trip of zero.
func assertRowsCarryTheirCheckTimestamp(t *testing.T, services []any) {
	t.Helper()
	for _, entry := range services {
		row, _ := entry.(map[string]any)
		if row["checked_at"] == "" || row["checked_at"] == nil {
			t.Errorf("%v carries no check timestamp", row["id"])
		}
		if row["state"] != string(domain.HealthDisabled) {
			continue
		}
		if _, present := row["latency_ms"]; present {
			t.Errorf("a disabled integration reported a latency")
		}
	}
}

func TestRefreshForcesACollectionAndRequiresCSRF(t *testing.T) {
	ports := healthFixture()
	harness := observabilityHarness(t, ports)
	cookie, csrf := harness.establish(t)

	response := harness.do(harness.authenticated(t, http.MethodPost, RouteAdminHealthRefresh, cookie, csrf))
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", response.Code, response.Body.String())
	}
	if len(ports.forced) != 1 || !ports.forced[0] {
		t.Fatalf("the refresh did not force a collection: %v", ports.forced)
	}

	// The same request without the CSRF token is a forgery and must be refused
	// before it costs anything.
	before := ports.calls.Load()
	forged := harness.authenticated(t, http.MethodPost, RouteAdminHealthRefresh, cookie, "")
	if code := harness.do(forged).Code; code != http.StatusForbidden {
		t.Fatalf("expected 403 without a CSRF token, got %d", code)
	}
	if ports.calls.Load() != before {
		t.Fatal("a forged refresh reached the collection")
	}
}

// Every route is guarded by the same capability, and a read is guarded exactly
// like a write: the dashboard reports how many people are signed in and the
// Health Center names every dependency the deployment has.
func TestObservabilityRoutesRequireTheirCapability(t *testing.T) {
	routes := []struct {
		method string
		path   string
	}{
		{http.MethodGet, RouteAdminOverview},
		{http.MethodGet, RouteAdminHealth},
		{http.MethodPost, RouteAdminHealthRefresh},
	}
	for _, route := range routes {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			ports := healthFixture()
			// A real administrator with a real session, holding a different
			// capability. This is the horizontal case, not the anonymous one.
			harness := observabilityHarness(t, ports, domain.CapabilityAuditRead)
			cookie, csrf := harness.establish(t)

			response := harness.do(harness.authenticated(t, route.method, route.path, cookie, csrf))
			if response.Code != http.StatusForbidden {
				t.Fatalf("expected 403, got %d (%s)", response.Code, response.Body.String())
			}
			if ports.calls.Load() != 0 {
				t.Fatal("a refused request reached the collection")
			}
			if !strings.Contains(strings.Join(harness.store.recordedActions(), ","), "denied") {
				t.Error("the denial was not recorded in the audit trail")
			}
		})
	}
}

func TestObservabilityRoutesRefuseAnUnauthenticatedCaller(t *testing.T) {
	ports := healthFixture()
	harness := observabilityHarness(t, ports)

	for _, path := range []string{RouteAdminOverview, RouteAdminHealth} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		if code := harness.do(request).Code; code != http.StatusUnauthorized {
			t.Errorf("%s: expected 401 without a session, got %d", path, code)
		}
	}
	if ports.calls.Load() != 0 {
		t.Fatal("an unauthenticated request reached the collection")
	}
}

// A deployment without the observability surface answers 503 rather than
// serving one of these paths unguarded — the same all-or-nothing rule the rest
// of the router applies.
func TestObservabilityRoutesAreUnavailableWhenNotWired(t *testing.T) {
	harness := newHarness(t, adminStore(domain.CapabilityInfrastructureRead))
	cookie, csrf := harness.establish(t)

	for _, route := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, RouteAdminOverview},
		{http.MethodGet, RouteAdminHealth},
		{http.MethodPost, RouteAdminHealthRefresh},
	} {
		response := harness.do(harness.authenticated(t, route.method, route.path, cookie, csrf))
		if response.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s: expected 503, got %d", route.method, route.path, response.Code)
		}
	}
}

// The single parameter the surface accepts is a registry identifier. This is
// the SSRF boundary: anything shaped like a destination must be refused before
// it reaches anything that could dial it.
func TestTheServiceFilterAcceptsOnlyRegistryIdentifiers(t *testing.T) {
	ports := healthFixture()
	harness := observabilityHarness(t, ports)
	cookie, csrf := harness.establish(t)

	refused := []string{
		"http://169.254.169.254/latest/meta-data/",
		"https://attacker.example/",
		"127.0.0.1:5432",
		"file:///etc/passwd",
		"postgres://user:pass@10.0.0.5:5432/nchat",
		"../../etc/hosts",
		"unknown_service",
	}
	for _, candidate := range refused {
		request := harness.authenticated(t, http.MethodGet, RouteAdminHealth+"?service="+candidate, cookie, csrf)
		response := harness.do(request)
		if response.Code != http.StatusBadRequest {
			t.Errorf("%q was not refused: got %d (%s)", candidate, response.Code, response.Body.String())
		}
	}

	// And the identifier that is declared narrows the listing rather than
	// naming anything the server dials.
	request := harness.authenticated(t, http.MethodGet, RouteAdminHealth+"?service=postgres", cookie, csrf)
	response := harness.do(request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200 for a declared identifier, got %d", response.Code)
	}
	services, _ := decodeBody(t, response)["services"].([]any)
	if len(services) != 1 {
		t.Fatalf("expected the listing to be narrowed to one row, got %d", len(services))
	}
}

// The refresh takes no body. Anything sent is refused rather than parsed,
// because a refresh has nothing for a caller to parameterise.
func TestTheRefreshRejectsAnyAttemptToParameteriseIt(t *testing.T) {
	ports := healthFixture()
	harness := observabilityHarness(t, ports)
	cookie, csrf := harness.establish(t)

	request := harness.authenticated(t, http.MethodPost, RouteAdminHealthRefresh+"?service=http://169.254.169.254/", cookie, csrf)
	if code := harness.do(request).Code; code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a destination-shaped parameter, got %d", code)
	}
}

// The payload is the last place a secret could escape. The fixture is
// deliberately built from the real registry, so this covers whatever the
// registry declares rather than a hand-written subset.
func TestTheHealthPayloadCarriesNoInfrastructureDetail(t *testing.T) {
	ports := healthFixture()
	harness := observabilityHarness(t, ports)
	cookie, csrf := harness.establish(t)

	body := harness.do(harness.authenticated(t, http.MethodGet, RouteAdminHealth, cookie, csrf)).Body.String()
	forbidden := []string{
		"password", "secret", "token", "Authorization", "Cookie",
		"postgres://", "DATABASE_URL", "AUTH_JWT_HMAC_SECRET",
		"VALKEY_PASSWORD", "LIVEKIT_API_SECRET",
		// Environment variable names would map the deployment even without
		// their values, and the payload has no reason to carry them.
		"SEAWEEDFS_FILER_URL", "OIDC_ISSUER_URL", "FILE_MALWARE_SCANNER_ADDRESS",
		"goroutine", // the first word of a Go stack trace
	}
	for _, needle := range forbidden {
		if strings.Contains(body, needle) {
			t.Errorf("the health payload contains %q", needle)
		}
	}
}

func TestObservabilityRoutesRefuseOtherMethods(t *testing.T) {
	ports := healthFixture()
	harness := observabilityHarness(t, ports)
	cookie, csrf := harness.establish(t)

	for _, route := range []struct {
		method string
		path   string
	}{
		{http.MethodPost, RouteAdminOverview},
		{http.MethodDelete, RouteAdminHealth},
		{http.MethodGet, RouteAdminHealthRefresh},
	} {
		response := harness.do(harness.authenticated(t, route.method, route.path, cookie, csrf))
		if response.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s: expected 405, got %d", route.method, route.path, response.Code)
		}
	}
}

// A summary the service cannot build is a 503 with the platform envelope, not
// a stack trace and not a partially rendered page.
func TestAFailedSummaryAnswersWithTheEnvelope(t *testing.T) {
	ports := healthFixture()
	ports.err = domain.ErrUnavailable
	harness := observabilityHarness(t, ports)
	cookie, csrf := harness.establish(t)

	response := harness.do(harness.authenticated(t, http.MethodGet, RouteAdminOverview, cookie, csrf))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", response.Code)
	}
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil || envelope.Error.Code == "" {
		t.Fatalf("expected the platform error envelope, got %s", response.Body.String())
	}
}

// Unavailable counters must not blank the dashboard, and they must not arrive
// as zeros either: the value is omitted and the card says so.
func TestUnavailableMetricsAreOmittedRatherThanZeroed(t *testing.T) {
	ports := healthFixture()
	ports.summary.MetricsAvailable = false
	ports.summary.Metrics = domain.PlatformMetrics(domain.PlatformCounters{}, false)
	harness := observabilityHarness(t, ports)
	cookie, csrf := harness.establish(t)

	response := harness.do(harness.authenticated(t, http.MethodGet, RouteAdminOverview, cookie, csrf))
	summary, _ := decodeBody(t, response)["summary"].(map[string]any)
	if summary["metrics_available"] != false {
		t.Fatal("the summary claims the counters are available")
	}
	metrics, _ := summary["metrics"].([]any)
	if len(metrics) == 0 {
		t.Fatal("the cards must still be listed, so the operator sees which ones are missing")
	}
	for _, entry := range metrics {
		metric, _ := entry.(map[string]any)
		if _, present := metric["value"]; present {
			t.Errorf("%v carries a value it never observed", metric["key"])
		}
	}
}
