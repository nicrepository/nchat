package service

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
)

// The Health Center's aggregation (issue #581).
//
// One collection serves every consumer. The dashboard's overall state, its
// service counters, its alerts and the Health Center's table all read the same
// snapshot, so two screens open side by side cannot disagree about whether the
// platform is healthy — and a dependency is asked at most once per collection
// no matter how many people are watching.
//
// Four properties keep a health check from becoming an outage of its own:
//
//   - every probe has its own short timeout. There is no single large budget
//     covering the whole collection, because one stuck dependency would then
//     consume the budget of all the others;
//   - the collection runs under a context detached from the request that
//     started it. A shared computation must not be cancellable by whichever
//     browser tab happened to trigger it and then closed;
//   - concurrency is bounded, so a deployment with more integrations does not
//     open more sockets at once;
//   - a probe that fails, panics or times out becomes that service's result.
//     Nothing propagates, and the response is still a 200 describing what was
//     learned.

const (
	// healthCacheTTL is how long a snapshot is served without recollecting.
	// Short enough that the console shows something current, long enough that
	// a room full of operators refreshing does not multiply into load on the
	// integrations.
	healthCacheTTL = 15 * time.Second
	// minForcedRefreshInterval bounds the manual refresh button. Below it, a
	// forced refresh returns the current snapshot instead of recollecting: the
	// button stays responsive and cannot be turned into an amplifier.
	minForcedRefreshInterval = 5 * time.Second
	// maxConcurrentProbes bounds how many dependencies are contacted at once.
	maxConcurrentProbes = 4
	// collectionBudget is the ceiling for one whole collection. With bounded
	// concurrency and a per-probe timeout it is never reached in practice; it
	// exists so a pathological case still terminates.
	collectionBudget = 20 * time.Second
)

// HealthDatabase is the trivial round trip the PostgreSQL check makes.
//
// Ping acquires a connection from the pool and exchanges an empty statement
// with the server, so a successful call proves connectivity, an available
// pool slot and a server that is answering queries — which is exactly the
// three things the check claims, and no more.
type HealthDatabase interface {
	Ping(ctx context.Context) error
}

// HealthService collects the state of every declared dependency.
type HealthService struct {
	database  HealthDatabase
	lookupEnv func(string) (string, bool)
	client    *http.Client
	now       func() time.Time
	metrics   *HealthMetrics

	mu          sync.Mutex
	snapshot    domain.HealthSnapshot
	hasSnapshot bool
	// inflight is the collection currently running, if any. It is what makes
	// concurrent callers share one collection rather than each starting their
	// own — the difference between a refresh button and a stampede.
	inflight *healthCollection
}

// healthCollection is one in-flight collection other callers can wait on.
type healthCollection struct {
	done     chan struct{}
	snapshot domain.HealthSnapshot
}

// NewHealthService builds the service against the real environment.
func NewHealthService(database HealthDatabase, metrics *HealthMetrics) *HealthService {
	return &HealthService{
		database:  database,
		lookupEnv: os.LookupEnv,
		client:    newProbeHTTPClient(probeTimeout),
		now:       time.Now,
		metrics:   metrics,
	}
}

// NewHealthServiceWithEnv builds the service against a described environment.
//
// Injected rather than read from the process so a test can describe a
// deployment — an observable Valkey, an unobservable LiveKit — without
// mutating global state. It is read-only by construction: there is no
// counterpart that writes an environment variable.
func NewHealthServiceWithEnv(
	database HealthDatabase,
	metrics *HealthMetrics,
	lookupEnv func(string) (string, bool),
	now func() time.Time,
) *HealthService {
	service := NewHealthService(database, metrics)
	if lookupEnv != nil {
		service.lookupEnv = lookupEnv
	}
	if now != nil {
		service.now = now
	}
	return service
}

// Snapshot returns the current health of every declared dependency.
//
// `force` is the manual refresh button. It bypasses the cache but not the
// minimum interval and not the coalescing, so holding it down costs one
// collection per interval no matter how many requests arrive.
func (s *HealthService) Snapshot(ctx context.Context, force bool) (domain.HealthSnapshot, error) {
	if s == nil {
		return domain.HealthSnapshot{}, domain.ErrUnavailable
	}
	if cached, ok := s.cached(force); ok {
		s.metrics.recordCache(true)
		return cached, nil
	}
	s.metrics.recordCache(false)
	collection, mine := s.claimCollection()
	if mine {
		s.runCollection(ctx, collection)
		return collection.snapshot, nil
	}
	return waitForCollection(ctx, collection)
}

// cached reports whether the stored snapshot may be served as is.
func (s *HealthService) cached(force bool) (domain.HealthSnapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.hasSnapshot {
		return domain.HealthSnapshot{}, false
	}
	age := s.now().Sub(s.snapshot.CollectedAt)
	if force {
		return s.snapshot, age < minForcedRefreshInterval
	}
	return s.snapshot, age < healthCacheTTL
}

// claimCollection either starts a collection or joins the running one.
func (s *HealthService) claimCollection() (*healthCollection, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inflight != nil {
		return s.inflight, false
	}
	collection := &healthCollection{done: make(chan struct{})}
	s.inflight = collection
	return collection, true
}

// runCollection performs the collection and publishes it to every waiter.
//
// The context is detached from the caller's on purpose: waiters are sharing
// this computation, so the request that happened to start it must not be able
// to cancel it out from under them. The values are kept, so tracing still
// correlates; only cancellation is dropped.
func (s *HealthService) runCollection(ctx context.Context, collection *healthCollection) {
	collectCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), collectionBudget)
	defer cancel()

	collection.snapshot = s.collect(collectCtx)

	s.mu.Lock()
	s.snapshot = collection.snapshot
	s.hasSnapshot = true
	s.inflight = nil
	s.mu.Unlock()
	close(collection.done)
}

// waitForCollection blocks until the shared collection publishes, or until the
// caller gives up.
func waitForCollection(ctx context.Context, collection *healthCollection) (domain.HealthSnapshot, error) {
	select {
	case <-collection.done:
		return collection.snapshot, nil
	case <-ctx.Done():
		return domain.HealthSnapshot{}, ctx.Err()
	}
}

// collect probes every declared dependency, with bounded concurrency.
//
// Results are written into a pre-sized slice at the descriptor's index rather
// than appended, so the output order is the registry's order regardless of
// which probe finishes first — a table whose rows move around between
// refreshes is a table nobody can read.
func (s *HealthService) collect(ctx context.Context) domain.HealthSnapshot {
	descriptors := domain.HealthRegistry()
	results := make([]domain.ServiceHealth, len(descriptors))
	slots := make(chan struct{}, maxConcurrentProbes)

	var group sync.WaitGroup
	for index, descriptor := range descriptors {
		group.Add(1)
		go func(index int, descriptor domain.HealthServiceDescriptor) {
			defer group.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			results[index] = s.check(ctx, descriptor)
		}(index, descriptor)
	}
	group.Wait()

	return domain.HealthSnapshot{CollectedAt: s.now(), Services: results}
}

// check produces one dependency's result, and never returns without one.
//
// The recover is not defensive decoration: this is the boundary that turns a
// defect in one probe into that probe's row rather than into a crashed pod
// serving nothing. A panicking probe is reported as unknown, because a build
// that panicked learned nothing.
func (s *HealthService) check(ctx context.Context, descriptor domain.HealthServiceDescriptor) (result domain.ServiceHealth) {
	started := s.now()
	defer func() {
		if recovered := recover(); recovered != nil {
			result = domain.ServiceHealth{
				Descriptor: descriptor, State: domain.HealthUnknown, Enabled: true,
				CheckedAt:     s.now(),
				ErrorCategory: domain.HealthErrorProtocolError,
				Detail:        "A verificação desta integração falhou internamente e não produziu um resultado.",
			}
		}
		s.metrics.recordCheck(descriptor.ID, result.State, s.now().Sub(started))
	}()

	resolution := s.resolve(descriptor)
	if resolution.terminal != nil {
		return s.settle(descriptor, resolution, *resolution.terminal, nil)
	}

	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	probeStarted := s.now()
	outcome := s.runProbe(probeCtx, descriptor, resolution.target)
	latency := s.now().Sub(probeStarted).Milliseconds()
	return s.settle(descriptor, resolution, outcome, &latency)
}

// settle assembles the result and applies the latency budget.
//
// The budget is applied here rather than inside each probe so every dependency
// is judged by the same rule, and so a probe cannot forget to apply it.
func (s *HealthService) settle(
	descriptor domain.HealthServiceDescriptor,
	resolution healthResolution,
	outcome probeOutcome,
	latencyMS *int64,
) domain.ServiceHealth {
	result := domain.ServiceHealth{
		Descriptor:    descriptor,
		State:         outcome.State,
		Enabled:       resolution.enabled,
		Observable:    resolution.observable,
		LatencyMS:     latencyMS,
		CheckedAt:     s.now(),
		ErrorCategory: outcome.Category,
		Detail:        outcome.Detail,
		Version:       outcome.Version,
	}
	if overBudget(descriptor, outcome, latencyMS) {
		result.State = domain.HealthDegraded
		result.ErrorCategory = domain.HealthErrorCapacityWarning
		result.Detail = "A dependência respondeu, mas acima do tempo esperado para esta integração."
	}
	return result
}

func overBudget(descriptor domain.HealthServiceDescriptor, outcome probeOutcome, latencyMS *int64) bool {
	if outcome.State != domain.HealthHealthy || descriptor.LatencyBudget <= 0 || latencyMS == nil {
		return false
	}
	return *latencyMS > descriptor.LatencyBudget.Milliseconds()
}

// runProbe dispatches to the probe the descriptor declares.
//
// A lookup on the declared kind rather than on anything derived from the
// target: the descriptor decides which protocol is spoken, so a value in the
// environment cannot make an address meant for one client be handed to
// another.
func (s *HealthService) runProbe(ctx context.Context, descriptor domain.HealthServiceDescriptor, target healthTarget) probeOutcome {
	switch descriptor.Probe {
	case domain.HealthProbePool:
		return s.probeDatabase(ctx)
	case domain.HealthProbeHTTP:
		return s.probeHTTPService(ctx, descriptor, target)
	case domain.HealthProbeValkey:
		return probeValkey(ctx, target.address, target.secret)
	case domain.HealthProbeClamd:
		return probeClamAV(ctx, target.address)
	case domain.HealthProbeSMTP:
		return probeSMTP(ctx, target.address, target.tlsMode)
	case domain.HealthProbeNone:
		return notObservable()
	}
	return notObservable()
}

// probeHTTPService covers the three dependencies reached over HTTP. OIDC needs
// the extra discovery step; the others are one bounded GET.
func (s *HealthService) probeHTTPService(ctx context.Context, descriptor domain.HealthServiceDescriptor, target healthTarget) probeOutcome {
	if descriptor.ID == domain.HealthServiceOIDC {
		return probeOIDC(ctx, s.client, target.address)
	}
	endpoint, err := parseProbeURL(normalizeWebSocketURL(target.address))
	if err != nil {
		return failed(domain.HealthErrorInvalidConfiguration, "O endpoint configurado não é uma URL http(s) utilizável.")
	}
	return probeHTTPEndpoint(ctx, s.client, endpoint.String())
}

// probeDatabase exercises the pool this service already holds.
func (s *HealthService) probeDatabase(ctx context.Context) probeOutcome {
	if s.database == nil {
		return probeOutcome{
			State:    domain.HealthUnknown,
			Category: domain.HealthErrorNotObservable,
			Detail:   "Este pod não tem o banco configurado, então a Admin API não está habilitada nele.",
		}
	}
	if err := s.database.Ping(ctx); err != nil {
		category, detail := classifyNetworkError(err)
		if category == domain.HealthErrorDependencyUnavailable {
			detail = "O banco de dados não respondeu à consulta de verificação."
		}
		return failed(category, detail)
	}
	return healthy()
}

func notObservable() probeOutcome {
	return probeOutcome{
		State:    domain.HealthUnknown,
		Category: domain.HealthErrorNotObservable,
		Detail:   "Este pod não recebe a configuração que nomeia o endpoint desta integração, então nenhuma verificação foi executada.",
	}
}

// healthTarget is one resolved probe destination.
//
// It is produced only by resolve, only from this pod's environment, and it is
// the only way any probe learns where to connect.
type healthTarget struct {
	address string
	// secret is a credential the probe writes to the socket. It is never
	// stored on a result, never logged and never marshalled.
	secret  string
	tlsMode string
}

// healthResolution is what the environment says about one dependency before
// anything is dialled.
type healthResolution struct {
	enabled    bool
	observable bool
	target     healthTarget
	// terminal, when set, is the answer: the integration is switched off, or
	// this pod cannot see where it lives. No probe runs.
	terminal *probeOutcome
}

// resolve decides whether to probe, and against what.
//
// This is the function that makes the whole surface safe. Every value it reads
// comes from s.lookupEnv — this pod's own environment, populated from the
// ConfigMap and the Secrets the deployment chose to mount. No argument reaches
// it from an HTTP request, and there is no branch that constructs a target
// from anything else.
func (s *HealthService) resolve(descriptor domain.HealthServiceDescriptor) healthResolution {
	enabled := s.enabled(descriptor)
	if !enabled {
		outcome := probeOutcome{
			State:  domain.HealthDisabled,
			Detail: "Integração desligada na configuração deste ambiente. Não é uma falha.",
		}
		return healthResolution{enabled: false, observable: true, terminal: &outcome}
	}
	values, observable := s.targetValues(descriptor)
	if !observable {
		outcome := notObservable()
		return healthResolution{enabled: true, observable: false, terminal: &outcome}
	}
	if descriptor.Probe == domain.HealthProbeNone {
		outcome := notObservable()
		return healthResolution{enabled: true, observable: false, terminal: &outcome}
	}
	return healthResolution{enabled: true, observable: true, target: s.target(descriptor, values)}
}

// enabled reads the deployment's switch for one integration.
//
// An absent variable falls back to the descriptor's declared default rather
// than to false, because "absent" means off for LiveKit and on for the
// database, and collapsing the two would report a working dependency as
// switched off.
func (s *HealthService) enabled(descriptor domain.HealthServiceDescriptor) bool {
	if descriptor.EnabledVar == "" {
		return descriptor.EnabledDefault
	}
	raw, present := s.lookupEnv(descriptor.EnabledVar)
	if !present || strings.TrimSpace(raw) == "" {
		return descriptor.EnabledDefault
	}
	value, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		return descriptor.EnabledDefault
	}
	return value
}

// targetValues reads every variable the descriptor needs.
//
// All or nothing: a target assembled from half the variables would be a
// half-invented address, so a single missing one makes the whole dependency
// unobservable. That is the state the Health Center renders as unknown, and it
// is why "configured" and "healthy" cannot be confused here.
func (s *HealthService) targetValues(descriptor domain.HealthServiceDescriptor) ([]string, bool) {
	values := make([]string, 0, len(descriptor.TargetVars))
	for _, name := range descriptor.TargetVars {
		raw, present := s.lookupEnv(name)
		trimmed := strings.TrimSpace(raw)
		if !present || trimmed == "" {
			return nil, false
		}
		values = append(values, trimmed)
	}
	return values, true
}

// target assembles the destination from the resolved values.
//
// Two variables mean host and port and are joined; one means the value is
// already the whole address; none means the probe does not dial at all, which
// is the pool check. The rule is declarative rather than per service, so
// adding a dependency is a registry entry and not a branch here.
func (s *HealthService) target(descriptor domain.HealthServiceDescriptor, values []string) healthTarget {
	var target healthTarget
	switch len(values) {
	case 0:
	case 1:
		target.address = values[0]
	default:
		target.address = values[0] + ":" + values[1]
	}
	if descriptor.CredentialVar != "" {
		secret, _ := s.lookupEnv(descriptor.CredentialVar)
		target.secret = strings.TrimSpace(secret)
	}
	if descriptor.ID == domain.HealthServiceSMTP {
		mode, _ := s.lookupEnv("SMTP_TLS_MODE")
		target.tlsMode = strings.TrimSpace(mode)
	}
	return target
}
