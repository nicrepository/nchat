package service_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
	"github.com/nicrepository/nchat/services/admin-service/internal/service"
)

// environment describes a deployment: exactly the variables a pod would have,
// and nothing else. Everything the health surface is willing to contact is
// resolved from here, so a test that wants a dependency to be unobservable
// simply leaves its variable out — which is the same thing a real overlay does
// when it scopes a Secret to another workload.
type environment map[string]string

func (e environment) lookup(name string) (string, bool) {
	value, ok := e[name]
	return value, ok
}

type stubDatabase struct {
	err   error
	delay time.Duration
	calls int
	mu    sync.Mutex
}

func (s *stubDatabase) Ping(ctx context.Context) error {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	if s.delay > 0 {
		select {
		case <-time.After(s.delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return s.err
}

func (s *stubDatabase) pings() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func newHealth(t *testing.T, database service.HealthDatabase, env environment) *service.HealthService {
	t.Helper()
	return service.NewHealthServiceWithEnv(database, service.NewHealthMetrics(), env.lookup, time.Now)
}

func snapshotOf(t *testing.T, health *service.HealthService, force bool) domain.HealthSnapshot {
	t.Helper()
	snapshot, err := health.Snapshot(context.Background(), force)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	return snapshot
}

func serviceNamed(t *testing.T, snapshot domain.HealthSnapshot, id domain.HealthServiceID) domain.ServiceHealth {
	t.Helper()
	for _, candidate := range snapshot.Services {
		if candidate.Descriptor.ID == id {
			return candidate
		}
	}
	t.Fatalf("snapshot does not contain %s", id)
	return domain.ServiceHealth{}
}

// An empty environment is the honest worst case: nothing is observable, and
// nothing may be guessed. In particular nothing may be reported as healthy.
func TestUnobservableDependenciesAreUnknownAndNeverHealthy(t *testing.T) {
	health := newHealth(t, &stubDatabase{}, environment{})
	snapshot := snapshotOf(t, health, false)

	for _, result := range snapshot.Services {
		if result.Descriptor.ID == domain.HealthServicePostgres {
			continue // the pool is wired, so this one is really checked
		}
		if result.State == domain.HealthHealthy {
			t.Errorf("%s was reported healthy without any check running", result.Descriptor.ID)
		}
		if result.CheckedAt.IsZero() {
			t.Errorf("%s carries no check timestamp", result.Descriptor.ID)
		}
	}
	valkey := serviceNamed(t, snapshot, domain.HealthServiceValkey)
	if valkey.State != domain.HealthUnknown || valkey.ErrorCategory != domain.HealthErrorNotObservable {
		t.Fatalf("expected an unobservable Valkey to be unknown/not_observable, got %s/%s", valkey.State, valkey.ErrorCategory)
	}
	if valkey.Observable {
		t.Error("Valkey must not claim to be observable when this pod has no address for it")
	}
	if valkey.LatencyMS != nil {
		t.Error("a check that never ran must not report a latency")
	}
}

// Disabled is not a failure, and it is not the same as unavailable. It is the
// one non-healthy state that must never produce an alert.
func TestDisabledIntegrationsAreNeitherFailingNorAlerting(t *testing.T) {
	health := newHealth(t, &stubDatabase{}, environment{
		"OIDC_ENABLED": "false", "LIVEKIT_ENABLED": "false", "SMTP_WORKER_ENABLED": "false",
	})
	snapshot := snapshotOf(t, health, false)

	for _, id := range []domain.HealthServiceID{domain.HealthServiceOIDC, domain.HealthServiceLiveKit, domain.HealthServiceSMTP} {
		result := serviceNamed(t, snapshot, id)
		if result.State != domain.HealthDisabled {
			t.Errorf("expected %s to be disabled, got %s", id, result.State)
		}
		if result.Enabled {
			t.Errorf("%s reports itself enabled while disabled", id)
		}
		if result.ErrorCategory != domain.HealthErrorNone {
			t.Errorf("a disabled integration is not an error: %s reported %s", id, result.ErrorCategory)
		}
	}
	for _, alert := range domain.DeriveAlerts(snapshot) {
		if alert.ServiceID == domain.HealthServiceOIDC || alert.ServiceID == domain.HealthServiceLiveKit {
			t.Errorf("a disabled integration raised an alert: %s", alert.Title)
		}
	}
}

// The central claim of the issue: having configuration is not the same as
// working. A fully described but unreachable dependency must come back
// unavailable, not healthy.
func TestConfiguredIsNotHealthy(t *testing.T) {
	// A port nothing listens on: the address is well formed and the
	// configuration is complete, so only a real connection attempt can tell
	// the difference.
	dead := closedAddress(t)
	host, port, err := net.SplitHostPort(dead)
	if err != nil {
		t.Fatalf("SplitHostPort: %v", err)
	}
	health := newHealth(t, &stubDatabase{}, environment{
		"VALKEY_HOST": host, "VALKEY_PORT": port,
	})
	valkey := serviceNamed(t, snapshotOf(t, health, false), domain.HealthServiceValkey)
	if valkey.State != domain.HealthUnavailable {
		t.Fatalf("expected a configured but unreachable Valkey to be unavailable, got %s", valkey.State)
	}
	if !valkey.Observable {
		t.Error("the address was observable; only the connection failed")
	}
	if valkey.ErrorCategory != domain.HealthErrorDependencyUnavailable && valkey.ErrorCategory != domain.HealthErrorConnectionTimeout {
		t.Errorf("unexpected category %s", valkey.ErrorCategory)
	}
}

// closedAddress returns a host:port that accepted a listener and then stopped.
func closedAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return address
}

func TestPostgresIsCheckedThroughThePoolAndReportsLatency(t *testing.T) {
	health := newHealth(t, &stubDatabase{}, environment{})
	postgres := serviceNamed(t, snapshotOf(t, health, false), domain.HealthServicePostgres)
	if postgres.State != domain.HealthHealthy {
		t.Fatalf("expected a healthy database, got %s (%s)", postgres.State, postgres.Detail)
	}
	if postgres.LatencyMS == nil {
		t.Fatal("a check that ran must report the round trip it measured")
	}
	if postgres.CheckedAt.IsZero() {
		t.Fatal("a check that ran must report when it ran")
	}
}

func TestPostgresFailureIsReportedWithoutTheDriverMessage(t *testing.T) {
	// The shape of a real pgx failure: it names the host and repeats the DSN,
	// credential included. None of it may reach a client.
	driverMessage := "dial tcp 10.0.0.5:5432: " + "pass" + "word=hunter2 database=nchat"
	health := newHealth(t, &stubDatabase{err: errors.New(driverMessage)}, environment{})
	postgres := serviceNamed(t, snapshotOf(t, health, false), domain.HealthServicePostgres)

	if postgres.State != domain.HealthUnavailable {
		t.Fatalf("expected an unavailable database, got %s", postgres.State)
	}
	if strings.Contains(postgres.Detail, "hunter2") || strings.Contains(postgres.Detail, "10.0.0.5") {
		t.Fatalf("the driver's message reached the response: %q", postgres.Detail)
	}
	if !domain.ValidHealthErrorCategory(postgres.ErrorCategory) {
		t.Fatalf("the failure was not classified: %q", postgres.ErrorCategory)
	}
}

// A pod with no database is a pod where the Admin API is not enabled at all.
// It is a blind spot, so it is unknown — not a healthy database and not a
// failing one.
func TestPostgresWithoutAPoolIsUnknown(t *testing.T) {
	health := newHealth(t, nil, environment{})
	postgres := serviceNamed(t, snapshotOf(t, health, false), domain.HealthServicePostgres)
	if postgres.State != domain.HealthUnknown || postgres.ErrorCategory != domain.HealthErrorNotObservable {
		t.Fatalf("expected unknown/not_observable, got %s/%s", postgres.State, postgres.ErrorCategory)
	}
}

// One stuck dependency must not stop the others from being reported. The
// database is deliberately slower than the per-probe timeout; every other row
// must still arrive.
func TestOneStuckDependencyDoesNotBlockTheRest(t *testing.T) {
	health := newHealth(t, &stubDatabase{delay: 5 * time.Second}, environment{
		"OIDC_ENABLED": "false", "LIVEKIT_ENABLED": "false",
	})
	started := time.Now()
	snapshot := snapshotOf(t, health, false)
	elapsed := time.Since(started)

	if len(snapshot.Services) != len(domain.HealthRegistry()) {
		t.Fatalf("expected every declared service in the snapshot, got %d", len(snapshot.Services))
	}
	// The per-probe timeout is short and is not shared, so the whole
	// collection finishes on the order of one probe rather than of the stuck
	// dependency's own delay.
	if elapsed > 4*time.Second {
		t.Fatalf("the collection waited on the stuck dependency for %s", elapsed)
	}
	postgres := serviceNamed(t, snapshot, domain.HealthServicePostgres)
	if postgres.State != domain.HealthUnavailable || postgres.ErrorCategory != domain.HealthErrorConnectionTimeout {
		t.Fatalf("expected a timed-out database, got %s/%s", postgres.State, postgres.ErrorCategory)
	}
	oidc := serviceNamed(t, snapshot, domain.HealthServiceOIDC)
	if oidc.State != domain.HealthDisabled {
		t.Fatalf("the other rows must still be produced; OIDC came back %s", oidc.State)
	}
}

// The cache is what keeps a room full of operators from multiplying into load
// on the integrations.
func TestSnapshotIsServedFromCacheWithinItsTTL(t *testing.T) {
	database := &stubDatabase{}
	health := newHealth(t, database, environment{})

	first := snapshotOf(t, health, false)
	second := snapshotOf(t, health, false)

	if database.pings() != 1 {
		t.Fatalf("expected the second read to be served from cache, got %d collections", database.pings())
	}
	if !first.CollectedAt.Equal(second.CollectedAt) {
		t.Fatal("a cached snapshot must keep the timestamp of the collection it came from")
	}
}

// And expiry is what keeps it honest. The clock is injected rather than slept
// through, so this asserts the rule instead of the machine's timing.
func TestSnapshotIsRecollectedOnceTheCacheExpires(t *testing.T) {
	database := &stubDatabase{}
	now := time.Now()
	clock := func() time.Time { return now }
	health := service.NewHealthServiceWithEnv(database, service.NewHealthMetrics(), environment{}.lookup, func() time.Time { return clock() })

	if _, err := health.Snapshot(context.Background(), false); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	now = now.Add(time.Minute)
	if _, err := health.Snapshot(context.Background(), false); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if database.pings() != 2 {
		t.Fatalf("expected an expired cache to force a new collection, got %d", database.pings())
	}
}

// The manual refresh must be usable without being an amplifier. Below the
// minimum interval it returns the snapshot it already has.
func TestForcedRefreshIsRateLimited(t *testing.T) {
	database := &stubDatabase{}
	now := time.Now()
	health := service.NewHealthServiceWithEnv(database, service.NewHealthMetrics(), environment{}.lookup, func() time.Time { return now })

	for i := 0; i < 20; i++ {
		if _, err := health.Snapshot(context.Background(), true); err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
	}
	if database.pings() != 1 {
		t.Fatalf("twenty forced refreshes produced %d collections; the button is an amplifier", database.pings())
	}

	now = now.Add(time.Minute)
	if _, err := health.Snapshot(context.Background(), true); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if database.pings() != 2 {
		t.Fatalf("a refresh after the interval must really recollect, got %d", database.pings())
	}
}

// Concurrent callers share one collection. Without this, a dashboard opened in
// ten tabs at once is ten simultaneous rounds of connections to every
// integration.
func TestConcurrentRequestsShareOneCollection(t *testing.T) {
	database := &stubDatabase{delay: 100 * time.Millisecond}
	health := newHealth(t, database, environment{})

	var group sync.WaitGroup
	results := make([]domain.HealthSnapshot, 8)
	for i := range results {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			snapshot, err := health.Snapshot(context.Background(), true)
			if err != nil {
				t.Errorf("Snapshot: %v", err)
				return
			}
			results[index] = snapshot
		}(i)
	}
	group.Wait()

	if database.pings() != 1 {
		t.Fatalf("expected eight concurrent readers to share one collection, got %d", database.pings())
	}
	for _, snapshot := range results {
		if !snapshot.CollectedAt.Equal(results[0].CollectedAt) {
			t.Fatal("concurrent readers received different snapshots")
		}
	}
}

// A caller that gives up must not take the shared collection down with it: the
// other waiters are relying on it.
func TestACancelledRequestDoesNotAbortTheSharedCollection(t *testing.T) {
	database := &stubDatabase{delay: 150 * time.Millisecond}
	health := newHealth(t, database, environment{})

	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		_, err := health.Snapshot(ctx, true)
		done <- err
	}()
	<-started
	time.Sleep(20 * time.Millisecond)
	cancel()
	<-done

	// The collection the cancelled caller started still completes, so the next
	// request is served from its result rather than starting a second one.
	snapshot, err := health.Snapshot(context.Background(), false)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snapshot.Services) != len(domain.HealthRegistry()) {
		t.Fatal("the shared collection did not complete after its initiator gave up")
	}
	if database.pings() != 1 {
		t.Fatalf("expected the cancelled collection to still be reused, got %d collections", database.pings())
	}
}

func TestSnapshotOnANilServiceIsUnavailable(t *testing.T) {
	var health *service.HealthService
	if _, err := health.Snapshot(context.Background(), false); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
}

// The registry is the only source of check targets. This asserts the property
// end to end: with an environment that names a rogue address under a variable
// no descriptor declares, nothing contacts it.
func TestOnlyDeclaredVariablesCanBecomeTargets(t *testing.T) {
	var contacted int32
	var mu sync.Mutex
	rogue := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		contacted++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer rogue.Close()

	health := newHealth(t, &stubDatabase{}, environment{
		"ATTACKER_SUPPLIED_URL": rogue.URL,
		"HEALTH_TARGET":         rogue.URL,
		"URL":                   rogue.URL,
	})
	snapshotOf(t, health, false)

	mu.Lock()
	defer mu.Unlock()
	if contacted != 0 {
		t.Fatalf("the collection contacted an address no descriptor declares (%d times)", contacted)
	}
}

// An enabled integration whose endpoint this pod cannot see is a blind spot,
// and a blind spot is unknown. Reporting it as disabled would claim a decision
// nobody made; reporting it as unavailable would claim a check that never ran.
func TestEnabledButUnobservableIsUnknownRatherThanDisabled(t *testing.T) {
	health := newHealth(t, &stubDatabase{}, environment{"LIVEKIT_ENABLED": "true"})
	livekit := serviceNamed(t, snapshotOf(t, health, false), domain.HealthServiceLiveKit)

	if livekit.State != domain.HealthUnknown {
		t.Fatalf("expected unknown, got %s", livekit.State)
	}
	if !livekit.Enabled {
		t.Error("the integration is switched on; only its endpoint is invisible from here")
	}
	if livekit.ErrorCategory != domain.HealthErrorNotObservable {
		t.Errorf("expected not_observable, got %s", livekit.ErrorCategory)
	}
	if livekit.Detail == "" {
		t.Error("a blind spot must say why it is one")
	}
}

// A malformed flag must not silently switch an integration off. Falling back
// to the declared default is what keeps a typo from hiding a real dependency.
func TestAMalformedEnabledFlagFallsBackToTheDeclaredDefault(t *testing.T) {
	health := newHealth(t, &stubDatabase{}, environment{"LIVEKIT_ENABLED": "sim"})
	livekit := serviceNamed(t, snapshotOf(t, health, false), domain.HealthServiceLiveKit)
	if livekit.State != domain.HealthDisabled {
		t.Fatalf("LiveKit defaults to off, so a malformed flag must leave it disabled, got %s", livekit.State)
	}

	health = newHealth(t, &stubDatabase{}, environment{"VALKEY_HOST": "  ", "VALKEY_PORT": "6379"})
	valkey := serviceNamed(t, snapshotOf(t, health, false), domain.HealthServiceValkey)
	if valkey.State != domain.HealthUnknown {
		t.Fatalf("a half-resolved address must not be dialled; got %s", valkey.State)
	}
}
