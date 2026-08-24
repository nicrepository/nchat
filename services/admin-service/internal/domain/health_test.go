package domain_test

import (
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
)

// The registry is a compile-time literal, so these are the checks that turn a
// source defect into a failed build rather than into a runtime surprise.
func TestHealthRegistryHoldsItsInvariants(t *testing.T) {
	if err := domain.ValidateHealthRegistry(); err != nil {
		t.Fatalf("ValidateHealthRegistry: %v", err)
	}
	if len(domain.HealthRegistry()) == 0 {
		t.Fatal("the registry is empty; the Health Center would have nothing to list")
	}
}

// The issue names ten dependencies. A build that quietly stopped declaring one
// would still render a plausible-looking Health Center, so the set is asserted
// rather than inferred from whatever happens to be in the file.
func TestHealthRegistryDeclaresEveryRequiredDependency(t *testing.T) {
	required := []domain.HealthServiceID{
		domain.HealthServicePostgres, domain.HealthServiceValkey, domain.HealthServiceOIDC,
		domain.HealthServiceSMTP, domain.HealthServiceLiveKit, domain.HealthServiceTURN,
		domain.HealthServiceClamAV, domain.HealthServiceStorage, domain.HealthServiceLinkScan,
		domain.HealthServiceWebSocket,
	}
	for _, id := range required {
		if _, ok := domain.LookupHealthService(id); !ok {
			t.Errorf("registry does not declare %s", id)
		}
	}
}

// The lookup is the fail-closed boundary of the whole surface: it is what
// stands between a query parameter and anything that could become a dial
// target.
func TestLookupHealthServiceRefusesAnythingUndeclared(t *testing.T) {
	undeclared := []string{
		"", "unknown", "POSTGRES", "postgres ",
		// The shapes a caller would try if the id were ever treated as an
		// address. None of them may resolve.
		"http://169.254.169.254/latest/meta-data",
		"127.0.0.1:5432",
		"../postgres",
		"postgres;drop",
	}
	for _, candidate := range undeclared {
		if _, ok := domain.LookupHealthService(domain.HealthServiceID(candidate)); ok {
			t.Errorf("registry resolved %q, which it must never do", candidate)
		}
	}
}

func TestValidateHealthDescriptorsRejectsBrokenDeclarations(t *testing.T) {
	valid := domain.HealthServiceDescriptor{
		ID: "example", DisplayName: "Example", Description: "Impacto.",
		Category: domain.HealthCategoryData, Probe: domain.HealthProbeNone,
		RunbookPath: "docs/runbooks/example.md",
	}
	dialing := valid
	dialing.ID = "example_http"
	dialing.Probe = domain.HealthProbeHTTP
	dialing.TargetVars = []string{"EXAMPLE_URL"}

	noTarget := dialing
	noTarget.TargetVars = nil
	strayTarget := valid
	strayTarget.TargetVars = []string{"EXAMPLE_URL"}
	noRunbook := valid
	noRunbook.RunbookPath = ""
	badID := valid
	badID.ID = "Example Service"
	noCategory := valid
	noCategory.Category = ""
	noImpact := valid
	noImpact.Description = ""
	unknownProbe := valid
	unknownProbe.Probe = "telnet"

	cases := map[string]struct {
		descriptors []domain.HealthServiceDescriptor
		wantErr     bool
	}{
		"a valid pair passes":                 {[]domain.HealthServiceDescriptor{valid, dialing}, false},
		"a duplicate id is refused":           {[]domain.HealthServiceDescriptor{valid, valid}, true},
		"a dialling probe needs a target":     {[]domain.HealthServiceDescriptor{noTarget}, true},
		"a non-dialling probe declares none":  {[]domain.HealthServiceDescriptor{strayTarget}, true},
		"a row without a runbook is refused":  {[]domain.HealthServiceDescriptor{noRunbook}, true},
		"an id outside the alphabet is out":   {[]domain.HealthServiceDescriptor{badID}, true},
		"a row without a category is refused": {[]domain.HealthServiceDescriptor{noCategory}, true},
		"a row without an impact is refused":  {[]domain.HealthServiceDescriptor{noImpact}, true},
		"an unknown probe kind is refused":    {[]domain.HealthServiceDescriptor{unknownProbe}, true},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			err := domain.ValidateHealthDescriptors(testCase.descriptors)
			if testCase.wantErr && err == nil {
				t.Fatal("expected the validator to refuse these descriptors")
			}
			if !testCase.wantErr && err != nil {
				t.Fatalf("expected the validator to accept these descriptors: %v", err)
			}
		})
	}
}

func serviceIn(state domain.HealthState, critical bool) domain.ServiceHealth {
	return domain.ServiceHealth{
		Descriptor: domain.HealthServiceDescriptor{
			ID: "example", DisplayName: "Example", Description: "Impacto.",
			RunbookPath: "docs/runbooks/example.md", Critical: critical,
		},
		State:     state,
		CheckedAt: time.Unix(0, 0).UTC(),
	}
}

// The five states must stay distinct in the one place that could collapse
// them: the rollup the dashboard shows.
func TestOverallStateKeepsTheStatesDistinct(t *testing.T) {
	cases := map[string]struct {
		services []domain.ServiceHealth
		want     domain.HealthState
	}{
		"nothing collected is unknown, never healthy": {
			nil, domain.HealthUnknown,
		},
		"all healthy is healthy": {
			[]domain.ServiceHealth{serviceIn(domain.HealthHealthy, true), serviceIn(domain.HealthHealthy, false)},
			domain.HealthHealthy,
		},
		"a disabled integration is not a failure": {
			[]domain.ServiceHealth{serviceIn(domain.HealthHealthy, true), serviceIn(domain.HealthDisabled, false)},
			domain.HealthHealthy,
		},
		"unknown is never reported as healthy": {
			[]domain.ServiceHealth{serviceIn(domain.HealthHealthy, true), serviceIn(domain.HealthUnknown, false)},
			domain.HealthDegraded,
		},
		"a degraded dependency degrades the platform": {
			[]domain.ServiceHealth{serviceIn(domain.HealthHealthy, true), serviceIn(domain.HealthDegraded, false)},
			domain.HealthDegraded,
		},
		"a non-critical outage degrades rather than downs": {
			[]domain.ServiceHealth{serviceIn(domain.HealthHealthy, true), serviceIn(domain.HealthUnavailable, false)},
			domain.HealthDegraded,
		},
		"a critical outage takes the platform down": {
			[]domain.ServiceHealth{serviceIn(domain.HealthUnavailable, true), serviceIn(domain.HealthHealthy, false)},
			domain.HealthUnavailable,
		},
		"a critical outage outranks everything else": {
			[]domain.ServiceHealth{serviceIn(domain.HealthDegraded, false), serviceIn(domain.HealthUnavailable, true)},
			domain.HealthUnavailable,
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			got := domain.HealthSnapshot{Services: testCase.services}.Overall()
			if got != testCase.want {
				t.Fatalf("expected overall %s, got %s", testCase.want, got)
			}
		})
	}
}

func TestCountByStateReportsEveryStateIncludingTheEmptyOnes(t *testing.T) {
	snapshot := domain.HealthSnapshot{Services: []domain.ServiceHealth{
		serviceIn(domain.HealthHealthy, true),
		serviceIn(domain.HealthHealthy, false),
		serviceIn(domain.HealthDisabled, false),
	}}
	counts := snapshot.CountByState()
	if counts[domain.HealthHealthy] != 2 || counts[domain.HealthDisabled] != 1 {
		t.Fatalf("unexpected counts: %v", counts)
	}
	// A state with no members must still be present, or the dashboard's set of
	// counters would change shape between refreshes.
	for _, state := range []domain.HealthState{domain.HealthDegraded, domain.HealthUnavailable, domain.HealthUnknown} {
		if _, ok := counts[state]; !ok {
			t.Errorf("state %s is missing from the counts", state)
		}
	}
}

func TestHealthStateRankOrdersByAttentionAndFallsBackSafely(t *testing.T) {
	if domain.HealthStateRank(domain.HealthUnavailable) <= domain.HealthStateRank(domain.HealthDegraded) {
		t.Error("unavailable must demand more attention than degraded")
	}
	if domain.HealthStateRank(domain.HealthUnknown) <= domain.HealthStateRank(domain.HealthHealthy) {
		t.Error("unknown must demand more attention than healthy")
	}
	if domain.HealthStateRank(domain.HealthDisabled) >= domain.HealthStateRank(domain.HealthHealthy) {
		t.Error("a deliberately disabled integration must sort below a healthy one")
	}
	if domain.HealthStateRank("invented") != domain.HealthStateRank(domain.HealthUnknown) {
		t.Error("an undeclared state must rank as unknown rather than as anything better")
	}
	if domain.ValidHealthState("invented") {
		t.Error("an undeclared state must not validate")
	}
}

func TestAlertsAreProducedOnlyForActionableConditions(t *testing.T) {
	cases := map[string]struct {
		state     domain.HealthState
		critical  bool
		wantAlert bool
		wantLevel domain.HealthAlertSeverity
	}{
		"healthy raises nothing":                   {domain.HealthHealthy, false, false, ""},
		"disabled raises nothing":                  {domain.HealthDisabled, false, false, ""},
		"unknown raises nothing, to avoid fatigue": {domain.HealthUnknown, false, false, ""},
		"degraded warns":                           {domain.HealthDegraded, false, true, domain.HealthAlertWarning},
		"a non-critical outage warns":              {domain.HealthUnavailable, false, true, domain.HealthAlertWarning},
		"a critical outage is critical":            {domain.HealthUnavailable, true, true, domain.HealthAlertCritical},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			snapshot := domain.HealthSnapshot{
				CollectedAt: time.Unix(1700000000, 0).UTC(),
				Services:    []domain.ServiceHealth{serviceIn(testCase.state, testCase.critical)},
			}
			alerts := domain.DeriveAlerts(snapshot)
			if !testCase.wantAlert {
				if len(alerts) != 0 {
					t.Fatalf("expected no alert, got %d", len(alerts))
				}
				return
			}
			if len(alerts) != 1 {
				t.Fatalf("expected exactly one alert, got %d", len(alerts))
			}
			assertActionable(t, alerts[0], testCase.wantLevel, snapshot.CollectedAt)
		})
	}
}

// assertActionable checks what makes an alert usable: it says what happened,
// what it costs, what to do, when it was observed and where to go next.
func assertActionable(t *testing.T, alert domain.HealthAlert, severity domain.HealthAlertSeverity, collectedAt time.Time) {
	t.Helper()
	if alert.Severity != severity {
		t.Errorf("expected severity %s, got %s", severity, alert.Severity)
	}
	if alert.Title == "" || alert.Impact == "" || alert.Action == "" {
		t.Errorf("an alert without a title, an impact and an action is not actionable: %+v", alert)
	}
	if !alert.Since.Equal(collectedAt) {
		t.Errorf("expected the alert to age from the collection, got %s", alert.Since)
	}
	if alert.RunbookPath == "" {
		t.Error("an alert must point somewhere")
	}
}

// One dependency in trouble is one problem. Emitting a row per symptom is how
// an operator learns to stop reading the alert list.
func TestAlertsAreDeduplicatedToOnePerService(t *testing.T) {
	broken := serviceIn(domain.HealthUnavailable, false)
	broken.ErrorCategory = domain.HealthErrorConnectionTimeout
	snapshot := domain.HealthSnapshot{
		CollectedAt: time.Unix(1700000000, 0).UTC(),
		Services:    []domain.ServiceHealth{broken},
	}
	if alerts := domain.DeriveAlerts(snapshot); len(alerts) != 1 {
		t.Fatalf("expected one alert for one troubled service, got %d", len(alerts))
	}
}

func TestAlertsPutTheCriticalOnesFirst(t *testing.T) {
	snapshot := domain.HealthSnapshot{
		CollectedAt: time.Unix(1700000000, 0).UTC(),
		Services: []domain.ServiceHealth{
			serviceIn(domain.HealthDegraded, false),
			serviceIn(domain.HealthUnavailable, true),
		},
	}
	alerts := domain.DeriveAlerts(snapshot)
	if len(alerts) != 2 {
		t.Fatalf("expected two alerts, got %d", len(alerts))
	}
	if alerts[0].Severity != domain.HealthAlertCritical {
		t.Fatalf("expected the critical alert first, got %s", alerts[0].Severity)
	}
}

// Every category must carry a recommended action, including the ones without a
// bespoke entry: an alert whose action is empty is an alert nobody can act on.
func TestEveryErrorCategoryProducesAnAction(t *testing.T) {
	categories := []domain.HealthErrorCategory{
		domain.HealthErrorConnectionTimeout, domain.HealthErrorAuthenticationFailed,
		domain.HealthErrorTLSError, domain.HealthErrorDependencyUnavailable,
		domain.HealthErrorInvalidConfiguration, domain.HealthErrorCapacityWarning,
		domain.HealthErrorNotObservable, domain.HealthErrorProtocolError, domain.HealthErrorNone,
	}
	for _, category := range categories {
		if !domain.ValidHealthErrorCategory(category) {
			t.Errorf("category %q is not declared", category)
		}
		service := serviceIn(domain.HealthUnavailable, false)
		service.ErrorCategory = category
		alerts := domain.DeriveAlerts(domain.HealthSnapshot{Services: []domain.ServiceHealth{service}})
		if len(alerts) != 1 || alerts[0].Action == "" {
			t.Errorf("category %q produced no actionable alert", category)
		}
	}
	if domain.ValidHealthErrorCategory("made_up") {
		t.Error("an undeclared category must not validate")
	}
}
