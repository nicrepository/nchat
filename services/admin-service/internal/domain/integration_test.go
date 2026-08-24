package domain_test

import (
	"strings"
	"testing"

	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
)

// The registry is a compile-time literal, so every invariant it must hold is a
// build-time fact. This is the test that makes it one.
func TestIntegrationRegistryHoldsItsInvariants(t *testing.T) {
	if err := domain.ValidateIntegrationRegistry(); err != nil {
		t.Fatalf("integration registry is inconsistent: %v", err)
	}
}

// Every setting an integration claims must be a setting the configuration
// registry of issue #580 already declares. This is what keeps the integrations
// page from becoming a second configuration model with keys of its own.
func TestIntegrationSettingsAllExistInTheConfigurationCatalog(t *testing.T) {
	for _, descriptor := range domain.IntegrationRegistry() {
		for _, key := range append(append([]domain.ConfigKey{}, descriptor.Settings...), descriptor.AdvancedSettings...) {
			if _, ok := domain.LookupConfig(key); !ok {
				t.Fatalf("%s claims setting %s, which the configuration registry does not declare", descriptor.ID, key)
			}
		}
	}
}

// No integration may declare a setting the console could write. Every
// integration key is class C or D — a ConfigMap value or a Sealed Secret — and
// the day one becomes editable, this test is what forces the write path to be
// reviewed rather than inherited.
func TestIntegrationSettingsAreNeverEditableFromTheConsole(t *testing.T) {
	for _, descriptor := range domain.IntegrationRegistry() {
		for _, key := range append(append([]domain.ConfigKey{}, descriptor.Settings...), descriptor.AdvancedSettings...) {
			definition, _ := domain.LookupConfig(key)
			if definition.Editable {
				t.Fatalf("%s claims %s as editable; issue #582 introduces no write path", descriptor.ID, key)
			}
		}
	}
}

// The console deep-links from an integration card into the configuration screen
// by searching for the integration's **id**, so an id that matches none of its
// own settings sends the operator to an empty result.
//
// The link used to carry the display name and did exactly that: "Keycloak /
// OIDC" is translated, is tokenised on the slash, and contains a word no
// configuration key has. Asserting the invariant here rather than only in the
// console is what stops the next integration from reintroducing it: the
// registry is where an id and its keys are declared together.
func TestIntegrationIDsMatchTheirOwnSettings(t *testing.T) {
	for _, descriptor := range domain.IntegrationRegistry() {
		keys := append(append([]domain.ConfigKey{}, descriptor.Settings...), descriptor.AdvancedSettings...)
		if len(keys) == 0 {
			continue
		}
		if !anySettingMatches(t, keys, string(descriptor.ID)) {
			t.Fatalf("no setting of %s carries its id, so the deep link to its configuration finds nothing", descriptor.ID)
		}
	}
}

// anySettingMatches asks the same question the console's search asks: does the
// term appear in a setting's searchable metadata? Values are deliberately not
// consulted, exactly as the search does not index them.
func anySettingMatches(t *testing.T, keys []domain.ConfigKey, term string) bool {
	t.Helper()
	for _, key := range keys {
		definition, ok := domain.LookupConfig(key)
		if !ok {
			t.Fatalf("unknown setting %s", key)
		}
		haystack := strings.ToLower(strings.Join([]string{
			definition.Label, definition.Description, string(definition.Key),
			string(definition.Category), definition.OwnerService, definition.EnvVar,
		}, " "))
		if strings.Contains(haystack, strings.ToLower(term)) {
			return true
		}
	}
	return false
}

// An unknown identifier resolves to nothing. It is the fail-closed boundary of
// the whole surface: there is no fallback that turns a name into a target.
func TestLookupIntegrationIsFailClosed(t *testing.T) {
	for _, id := range []domain.IntegrationID{"", "unknown", "OIDC", "../oidc", "oidc "} {
		if _, ok := domain.LookupIntegration(id); ok {
			t.Fatalf("%q resolved to an integration", id)
		}
	}
	if _, ok := domain.LookupIntegration(domain.IntegrationOIDC); !ok {
		t.Fatal("the registry must resolve a declared integration")
	}
}

// TURN and Link Scan are declared and deliberately not diagnosable, each with
// the reason shown to the operator. A future change that quietly adds an
// adapter to either has to delete an explanation to do it.
func TestIntegrationsWithoutADiagnosticExplainWhy(t *testing.T) {
	expected := map[domain.IntegrationID]bool{domain.IntegrationTURN: true, domain.IntegrationLinkScan: true}
	for _, descriptor := range domain.IntegrationRegistry() {
		if descriptor.Diagnosable() == expected[descriptor.ID] {
			t.Fatalf("%s: diagnosable=%v contradicts the deployment's reality", descriptor.ID, descriptor.Diagnosable())
		}
		if !descriptor.Diagnosable() && strings.TrimSpace(descriptor.DiagnosticUnsupported) == "" {
			t.Fatalf("%s offers no diagnostic and no reason", descriptor.ID)
		}
	}
}

// Only SMTP carries an action, and it is the test message. An action is the
// only thing on this surface with an effect outside the platform, so the set of
// them is asserted rather than left to grow unnoticed.
func TestOnlySMTPDeclaresAnAction(t *testing.T) {
	for _, descriptor := range domain.IntegrationRegistry() {
		if descriptor.ID == domain.IntegrationSMTP {
			continue
		}
		if len(descriptor.Actions) != 0 {
			t.Fatalf("%s declares an action; only the SMTP test message exists", descriptor.ID)
		}
	}
	smtp, _ := domain.LookupIntegration(domain.IntegrationSMTP)
	action, ok := domain.LookupIntegrationAction(smtp, domain.IntegrationActionSMTPTestEmail)
	if !ok || action.Capability != domain.CapabilityIntegrationsManage {
		t.Fatalf("the SMTP test message must exist and require the manage capability, got %+v", action)
	}
	if _, ok := domain.LookupIntegrationAction(smtp, "smtp.anything_else"); ok {
		t.Fatal("an undeclared action must not resolve")
	}
}

// Reading the surface and acting on it are separately granted. A diagnostic
// opens outbound connections and signs a credential, so it requires the manage
// capability and never the read one.
func TestIntegrationCapabilitiesSeparateReadingFromActing(t *testing.T) {
	for _, descriptor := range domain.IntegrationRegistry() {
		if descriptor.ReadCapability != domain.CapabilityIntegrationsRead {
			t.Fatalf("%s reads under %s", descriptor.ID, descriptor.ReadCapability)
		}
		if descriptor.DiagnoseCapability != domain.CapabilityIntegrationsManage {
			t.Fatalf("%s diagnoses under %s", descriptor.ID, descriptor.DiagnoseCapability)
		}
	}
}

func TestDeriveDiagnosticStatus(t *testing.T) {
	step := func(status domain.DiagnosticStatus) domain.DiagnosticStep {
		return domain.DiagnosticStep{Stage: domain.StageConnect, Status: status}
	}
	cases := map[string]struct {
		steps []domain.DiagnosticStep
		want  domain.DiagnosticStatus
	}{
		"no step ran is skipped, never passed": {
			steps: []domain.DiagnosticStep{step(domain.DiagnosticSkipped)},
			want:  domain.DiagnosticSkipped,
		},
		"an empty run is skipped": {steps: nil, want: domain.DiagnosticSkipped},
		"all passed": {
			steps: []domain.DiagnosticStep{step(domain.DiagnosticPassed), step(domain.DiagnosticPassed)},
			want:  domain.DiagnosticPassed,
		},
		"a warning survives a later pass": {
			steps: []domain.DiagnosticStep{step(domain.DiagnosticWarning), step(domain.DiagnosticPassed)},
			want:  domain.DiagnosticWarning,
		},
		"a failure wins over everything": {
			steps: []domain.DiagnosticStep{step(domain.DiagnosticPassed), step(domain.DiagnosticFailed), step(domain.DiagnosticWarning)},
			want:  domain.DiagnosticFailed,
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			if got := domain.DeriveDiagnosticStatus(testCase.steps); got != testCase.want {
				t.Fatalf("expected %q, got %q", testCase.want, got)
			}
		})
	}
}

func TestValidDiagnosticStage(t *testing.T) {
	if !domain.ValidDiagnosticStage(domain.StageDelivery) {
		t.Fatal("delivery is a declared stage")
	}
	for _, stage := range []domain.DiagnosticStage{"", "ping", "RESOLVE"} {
		if domain.ValidDiagnosticStage(stage) {
			t.Fatalf("%q must not be a declared stage", stage)
		}
	}
}

func TestIntegrationNetworkPolicySchemes(t *testing.T) {
	policy := domain.IntegrationNetworkPolicy{Schemes: []string{"http", "https"}}
	for _, scheme := range []string{"http", "https"} {
		if !policy.AllowsScheme(scheme) {
			t.Fatalf("%s must be allowed", scheme)
		}
	}
	for _, scheme := range []string{"", "file", "gopher", "ftp", "HTTPS", "ws"} {
		if policy.AllowsScheme(scheme) {
			t.Fatalf("%q must be refused", scheme)
		}
	}
	if (domain.IntegrationNetworkPolicy{}).AllowsScheme("http") {
		t.Fatal("a policy with no scheme allows none")
	}
}

func TestAuditIntegrationResourceIsNamespaced(t *testing.T) {
	resource := domain.AuditIntegrationResource(domain.IntegrationSMTP)
	if !strings.HasPrefix(resource, domain.AuditResourceIntegrationPrefix) {
		t.Fatalf("expected the integration prefix, got %q", resource)
	}
	if resource == domain.AuditIntegrationResource(domain.IntegrationOIDC) {
		t.Fatal("two integrations must not share a resource key")
	}
}

// The validator has to refuse the shapes that would become security bugs
// quietly, so each one is exercised against a descriptor that breaks it.
func TestValidateIntegrationDescriptorsRefusesBrokenRegistries(t *testing.T) {
	valid := func() domain.IntegrationDescriptor {
		return domain.IntegrationDescriptor{
			ID: "sample", DisplayName: "Sample", Summary: "Impacto.",
			Category: domain.HealthCategoryContent, HealthService: domain.HealthServiceStorage,
			RunbookPath: "docs/x.md", Diagnostic: domain.DiagnosticStorage,
			Stages:         []domain.DiagnosticStage{domain.StageReady},
			Policy:         domain.IntegrationNetworkPolicy{Schemes: []string{"https"}},
			ReadCapability: domain.CapabilityIntegrationsRead, DiagnoseCapability: domain.CapabilityIntegrationsManage,
		}
	}
	cases := map[string]func(*domain.IntegrationDescriptor){
		"malformed id":       func(d *domain.IntegrationDescriptor) { d.ID = "Sample!" },
		"no summary":         func(d *domain.IntegrationDescriptor) { d.Summary = "" },
		"no runbook":         func(d *domain.IntegrationDescriptor) { d.RunbookPath = "" },
		"unknown health row": func(d *domain.IntegrationDescriptor) { d.HealthService = "nope" },
		"unknown capability": func(d *domain.IntegrationDescriptor) { d.DiagnoseCapability = "admin.nope" },
		"unknown setting":    func(d *domain.IntegrationDescriptor) { d.Settings = []domain.ConfigKey{"nope.key"} },
		"no stage":           func(d *domain.IntegrationDescriptor) { d.Stages = nil },
		"unknown stage":      func(d *domain.IntegrationDescriptor) { d.Stages = []domain.DiagnosticStage{"ping"} },
		"silent unsupported": func(d *domain.IntegrationDescriptor) { d.Diagnostic, d.Stages = domain.DiagnosticNone, nil },
		"both and neither":   func(d *domain.IntegrationDescriptor) { d.DiagnosticUnsupported = "porque sim" },
		"unlabelled action":  func(d *domain.IntegrationDescriptor) { d.Actions = []domain.IntegrationAction{{ID: "x"}} },
		"action capability": func(d *domain.IntegrationDescriptor) {
			d.Actions = []domain.IntegrationAction{{ID: "x", Label: "l", Description: "d", Capability: "nope"}}
		},
		"duplicated setting": func(d *domain.IntegrationDescriptor) {
			d.Settings, d.AdvancedSettings = []domain.ConfigKey{"oidc.enabled"}, []domain.ConfigKey{"oidc.enabled"}
		},
		// A descriptor that says it cannot be diagnosed but still carries a
		// network policy is the shape that would let an adapter be added later
		// without anyone re-reading what it is allowed to reach.
		"policy without a diagnostic": func(d *domain.IntegrationDescriptor) {
			d.Diagnostic, d.Stages, d.DiagnosticUnsupported = domain.DiagnosticNone, nil, "sem alvo"
		},
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			descriptor := valid()
			breakIt(&descriptor)
			if err := domain.ValidateIntegrationDescriptors([]domain.IntegrationDescriptor{descriptor}); err == nil {
				t.Fatalf("%s must be refused", name)
			}
		})
	}
}

// The positive counterpart: an integration that honestly declares it cannot be
// checked validates, so the negative cases above cannot pass by accident.
func TestValidateIntegrationDescriptorsAcceptsADeclaredUnsupportedIntegration(t *testing.T) {
	descriptor := domain.IntegrationDescriptor{
		ID: "sample", DisplayName: "Sample", Summary: "Impacto.",
		Category: domain.HealthCategoryContent, HealthService: domain.HealthServiceStorage,
		RunbookPath: "docs/x.md", DiagnosticUnsupported: "Nenhuma variável nomeia o alvo.",
		ReadCapability: domain.CapabilityIntegrationsRead, DiagnoseCapability: domain.CapabilityIntegrationsManage,
	}
	if err := domain.ValidateIntegrationDescriptors([]domain.IntegrationDescriptor{descriptor}); err != nil {
		t.Fatalf("a properly declared unsupported integration must validate: %v", err)
	}
}

func TestValidateIntegrationDescriptorsRefusesDuplicateIDs(t *testing.T) {
	descriptor := domain.IntegrationDescriptor{
		ID: "sample", DisplayName: "Sample", Summary: "Impacto.",
		HealthService: domain.HealthServiceStorage, RunbookPath: "docs/x.md",
		DiagnosticUnsupported: "sem alvo",
		ReadCapability:        domain.CapabilityIntegrationsRead, DiagnoseCapability: domain.CapabilityIntegrationsManage,
	}
	err := domain.ValidateIntegrationDescriptors([]domain.IntegrationDescriptor{descriptor, descriptor})
	if err == nil {
		t.Fatal("a duplicate id must be refused")
	}
}
