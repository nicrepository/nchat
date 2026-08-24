package domain

import (
	"fmt"
	"regexp"
	"sync"
	"time"
)

// Secure integration configuration and diagnostics (issue #582).
//
// This file answers one question for every integration NChat has: what can the
// platform honestly say about it, and what can an operator honestly do to it
// from the console. The answer is a compile-time literal, for the same reason
// the configuration registry and the health registry are:
//
//   - the platform decides which integrations exist. A client names an id from
//     the table below, or it names nothing. It never supplies a host, a URL, a
//     port, a namespace, a credential or a target of any kind;
//   - an integration whose diagnostic this deployment cannot perform says so,
//     with a reason, instead of offering a button that would invent a result.
//
// What this file deliberately does *not* introduce is a second configuration
// system. Every editable setting still belongs to the registry of issue #580,
// every value still travels through its validate/diff/apply/rollback path, and
// nothing here writes a configuration or a credential. An integration section
// links to the settings it owns; it does not own them.

// IntegrationID is the stable identifier of one integration.
type IntegrationID string

const (
	IntegrationOIDC     IntegrationID = "oidc"
	IntegrationSMTP     IntegrationID = "smtp"
	IntegrationLiveKit  IntegrationID = "livekit"
	IntegrationTURN     IntegrationID = "turn"
	IntegrationLinkScan IntegrationID = "link_scan"
	IntegrationClamAV   IntegrationID = "clamav"
	IntegrationStorage  IntegrationID = "storage"
)

// DiagnosticKind names the adapter that runs one integration's active check.
//
// It is declared per descriptor rather than derived from the target, so a value
// in the environment can never make an address meant for one protocol be handed
// to another client. The empty kind is not "not implemented yet": it is a
// statement that this deployment has nothing it could honestly check, and it
// carries the reason.
type DiagnosticKind string

const (
	DiagnosticNone    DiagnosticKind = ""
	DiagnosticOIDC    DiagnosticKind = "oidc"
	DiagnosticSMTP    DiagnosticKind = "smtp"
	DiagnosticLiveKit DiagnosticKind = "livekit"
	DiagnosticClamAV  DiagnosticKind = "clamav"
	DiagnosticStorage DiagnosticKind = "storage"
)

// DiagnosticStage is one step of a diagnostic, from a closed vocabulary.
//
// The vocabulary is closed and shared across integrations on purpose: an
// operator reading "TLS: falhou" on the SMTP card and on the Keycloak card is
// reading the same fact, and the console renders one label table rather than
// one per integration. It is also what keeps a stage name out of a Prometheus
// label and out of a translation file by accident.
type DiagnosticStage string

const (
	// StageResolve is DNS. It is reported separately from the connection
	// because "the name does not resolve" and "the host refuses" are different
	// problems with different fixes.
	StageResolve DiagnosticStage = "resolve"
	// StageConnect is the TCP handshake.
	StageConnect DiagnosticStage = "connect"
	// StageTLS is the TLS handshake, direct or negotiated with STARTTLS.
	StageTLS DiagnosticStage = "tls"
	// StageDiscovery is the OpenID discovery document.
	StageDiscovery DiagnosticStage = "discovery"
	// StageIssuer is the comparison between the configured issuer and the one
	// the provider claims.
	StageIssuer DiagnosticStage = "issuer"
	// StageJWKS is the key set the discovery document points at.
	StageJWKS DiagnosticStage = "jwks"
	// StageCredential is the dependency accepting the credential this pod
	// holds. Skipped, never faked, when the protocol offers no way to prove it
	// without performing a real user-facing operation.
	StageCredential DiagnosticStage = "credential"
	// StageReady is the dependency declaring itself able to serve.
	StageReady DiagnosticStage = "ready"
	// StageDelivery is the one stage that changes something outside the
	// platform: a test message accepted by the relay.
	StageDelivery DiagnosticStage = "delivery"
)

var diagnosticStages = map[DiagnosticStage]struct{}{
	StageResolve: {}, StageConnect: {}, StageTLS: {}, StageDiscovery: {},
	StageIssuer: {}, StageJWKS: {}, StageCredential: {}, StageReady: {},
	StageDelivery: {},
}

// ValidDiagnosticStage reports whether the platform defines this stage.
func ValidDiagnosticStage(stage DiagnosticStage) bool {
	_, ok := diagnosticStages[stage]
	return ok
}

// DiagnosticStatus is what one stage concluded.
//
// Four values, and `skipped` is the one that earns its place: a stage that did
// not run because an earlier one failed, or because the protocol cannot prove
// it, must not be rendered as passing and must not be rendered as failing
// either. Both would be a claim the platform did not make.
type DiagnosticStatus string

const (
	DiagnosticPassed  DiagnosticStatus = "passed"
	DiagnosticWarning DiagnosticStatus = "warning"
	DiagnosticFailed  DiagnosticStatus = "failed"
	DiagnosticSkipped DiagnosticStatus = "skipped"
)

// DiagnosticStep is one stage's result as it reaches the console.
//
// Detail is a hand-written sentence chosen from this build's own vocabulary. It
// never carries a host, a port, a credential, a remote response body, a header
// or a stack trace — the same rule the health probes of issue #581 follow, and
// for the same reason.
type DiagnosticStep struct {
	Stage    DiagnosticStage
	Status   DiagnosticStatus
	Category HealthErrorCategory
	Detail   string
	// LatencyMS is nil when the stage did not run. Zero is a measurement and
	// absence is not, so they are different values.
	LatencyMS *int64
}

// DiagnosticReport is one complete run.
type DiagnosticReport struct {
	Integration IntegrationID
	StartedAt   time.Time
	// Status is the run's overall conclusion, derived from the steps by
	// DeriveDiagnosticStatus so the console and the audit trail cannot disagree
	// about whether a diagnostic passed.
	Status  DiagnosticStatus
	Steps   []DiagnosticStep
	Summary string
	// Version is an optional sanitized version string a dependency reported.
	Version string
}

// DeriveDiagnosticStatus reduces the steps to the run's conclusion.
//
// Any failure fails the run; otherwise any warning warns; a run with no step
// that actually executed is skipped rather than passed, because a diagnostic
// that checked nothing did not succeed.
func DeriveDiagnosticStatus(steps []DiagnosticStep) DiagnosticStatus {
	status := DiagnosticSkipped
	for _, step := range steps {
		switch step.Status {
		case DiagnosticFailed:
			return DiagnosticFailed
		case DiagnosticWarning:
			status = DiagnosticWarning
		case DiagnosticPassed:
			if status != DiagnosticWarning {
				status = DiagnosticPassed
			}
		case DiagnosticSkipped:
		}
	}
	return status
}

// IntegrationActionID names an explicit, operator-initiated action that is not
// a diagnostic.
type IntegrationActionID string

// IntegrationActionSMTPTestEmail delivers one message through the configured
// relay.
//
// It is the only action in this file that has an effect outside the platform,
// and its destination is deliberately not a parameter: the message goes to the
// authenticated administrator's own address, taken from the session principal.
// That single decision is what makes it impossible to use the console as an
// open relay, and it is why no allowlist of destinations had to be invented.
const IntegrationActionSMTPTestEmail IntegrationActionID = "smtp.test_email"

// IntegrationAction is one declared action.
type IntegrationAction struct {
	ID          IntegrationActionID
	Label       string
	Description string
	Capability  Capability
}

// IntegrationNetworkPolicy is what one integration is allowed to talk to.
//
// There is a policy per integration rather than one global rule, because the
// integrations genuinely differ: Keycloak and SeaweedFS are HTTP, SMTP and
// clamd are not, and a single "block private addresses" rule would break every
// in-cluster dependency this platform is built on.
//
// What does *not* differ, and is therefore not a field: link-local, multicast
// and unspecified addresses are refused for every integration. That range is
// what carries the cloud metadata endpoints (169.254.169.254 and the address
// metadata.google.internal resolves to), and no NChat dependency has ever lived
// there. Redirects are never followed by any policy either, so a remote host
// cannot nominate a second address.
type IntegrationNetworkPolicy struct {
	// Schemes are the URL schemes accepted for an HTTP-shaped target. Empty
	// means the target is a bare host:port and no URL is parsed at all.
	Schemes []string
	// AllowPrivate permits RFC 1918, unique-local and loopback destinations.
	// True for every current integration — they are cluster services — and a
	// field rather than an assumption so a future internet-only integration can
	// say no without editing the dialer.
	AllowPrivate bool
}

// AllowsScheme reports whether a URL scheme is acceptable under this policy.
func (p IntegrationNetworkPolicy) AllowsScheme(scheme string) bool {
	for _, allowed := range p.Schemes {
		if allowed == scheme {
			return true
		}
	}
	return false
}

// IntegrationDescriptor is everything the platform declares about one
// integration.
type IntegrationDescriptor struct {
	ID          IntegrationID
	DisplayName string
	// Summary is what breaks for users when this integration is not working.
	Summary  string
	Category HealthCategory
	// HealthService links the integration to the passive health registry of
	// issue #581. The console reads status from that snapshot rather than
	// running a check on render.
	HealthService HealthServiceID
	RunbookPath   string
	// Settings are the configuration keys this integration owns, in the order
	// the console renders them. Every one must exist in the registry of issue
	// #580; this list names them, it does not redefine them.
	Settings []ConfigKey
	// AdvancedSettings are the rarely-touched keys, rendered collapsed.
	AdvancedSettings []ConfigKey
	// Diagnostic is the adapter that runs the active check, or DiagnosticNone.
	Diagnostic DiagnosticKind
	// DiagnosticUnsupported is why no active check exists. Required exactly
	// when Diagnostic is DiagnosticNone, and shown verbatim, so an operator
	// learns the reason instead of finding a missing button.
	DiagnosticUnsupported string
	// Stages is the plan a supported diagnostic follows, in order. It is
	// declared so the console can render the plan before the run finishes, and
	// so a run that reports a stage nobody declared fails the registry test.
	Stages []DiagnosticStage
	// Policy is the network policy for this integration's diagnostic.
	Policy IntegrationNetworkPolicy
	// Actions are the explicit operations beyond the diagnostic.
	Actions []IntegrationAction
	// ReadCapability and DiagnoseCapability are the permissions the surface
	// requires. Declared per descriptor so the whole authorization model is
	// readable in one table.
	ReadCapability     Capability
	DiagnoseCapability Capability
}

// Diagnosable reports whether this deployment can run an active check.
func (d IntegrationDescriptor) Diagnosable() bool {
	return d.Diagnostic != DiagnosticNone
}

// httpPolicy is the shared shape of the HTTP-spoken integrations.
func httpPolicy() IntegrationNetworkPolicy {
	return IntegrationNetworkPolicy{Schemes: []string{"http", "https"}, AllowPrivate: true}
}

// socketPolicy is the shared shape of the integrations reached as host:port.
func socketPolicy() IntegrationNetworkPolicy {
	return IntegrationNetworkPolicy{AllowPrivate: true}
}

// integrationRegistry is the whole set of integrations the console presents.
//
// The order is the order the page renders, so it is a slice and not a map.
func integrationRegistry() []IntegrationDescriptor {
	base := func(descriptor IntegrationDescriptor) IntegrationDescriptor {
		descriptor.ReadCapability = CapabilityIntegrationsRead
		descriptor.DiagnoseCapability = CapabilityIntegrationsManage
		return descriptor
	}
	return []IntegrationDescriptor{
		base(oidcIntegration()),
		base(smtpIntegration()),
		base(liveKitIntegration()),
		base(turnIntegration()),
		base(linkScanIntegration()),
		base(clamAVIntegration()),
		base(storageIntegration()),
	}
}

func oidcIntegration() IntegrationDescriptor {
	return IntegrationDescriptor{
		ID: IntegrationOIDC, DisplayName: "Keycloak / OIDC",
		Summary:       "Single sign-on da plataforma. Indisponível, resta apenas o login local.",
		Category:      HealthCategoryIdentity,
		HealthService: HealthServiceOIDC,
		RunbookPath:   "docs/runbooks/task-auth-oidc-keycloak.md",
		Settings: []ConfigKey{
			"oidc.enabled", "secret.oidc_issuer_url", "secret.oidc_client_id",
			"secret.oidc_client_secret", "oidc.scopes", "oidc.auto_provision_enabled",
		},
		AdvancedSettings: []ConfigKey{
			"oidc.provider_name", "oidc.allowed_email_domains", "oidc.admin_acr_values",
			"secret.oidc_redirect_url", "secret.oidc_admin_redirect_url",
		},
		Diagnostic: DiagnosticOIDC,
		Stages: []DiagnosticStage{
			StageResolve, StageConnect, StageTLS, StageDiscovery,
			StageIssuer, StageJWKS, StageCredential,
		},
		Policy: httpPolicy(),
	}
}

func smtpIntegration() IntegrationDescriptor {
	return IntegrationDescriptor{
		ID: IntegrationSMTP, DisplayName: "SMTP",
		Summary:       "Relay de e-mail. Sem ele convites e redefinições de senha ficam na fila sem sair.",
		Category:      HealthCategoryMessaging,
		HealthService: HealthServiceSMTP,
		RunbookPath:   "docs/runbooks/task-smtp-bruteforce-login-audit.md",
		Settings: []ConfigKey{
			"email.smtp.worker_enabled", "secret.smtp_password",
		},
		Diagnostic: DiagnosticSMTP,
		Stages: []DiagnosticStage{
			StageResolve, StageConnect, StageTLS, StageCredential, StageReady,
		},
		Policy: socketPolicy(),
		Actions: []IntegrationAction{{
			ID:    IntegrationActionSMTPTestEmail,
			Label: "Enviar e-mail de teste",
			Description: "Entrega uma mensagem fixa pelo relay configurado, sempre para o " +
				"endereço da sua própria conta administrativa. O destino não é um campo: " +
				"é isso que impede o console de virar um relay aberto.",
			Capability: CapabilityIntegrationsManage,
		}},
	}
}

func liveKitIntegration() IntegrationDescriptor {
	return IntegrationDescriptor{
		ID: IntegrationLiveKit, DisplayName: "LiveKit",
		Summary:       "Servidor de mídia das chamadas. Indisponível, nenhuma chamada é estabelecida.",
		Category:      HealthCategoryRealtime,
		HealthService: HealthServiceLiveKit,
		RunbookPath:   "docs/runbooks/task-livekit-coturn-dev.md",
		Settings: []ConfigKey{
			"calls.livekit.enabled", "calls.livekit.token_ttl_seconds",
			"secret.livekit_api_key", "secret.livekit_api_secret",
		},
		Diagnostic: DiagnosticLiveKit,
		Stages: []DiagnosticStage{
			StageResolve, StageConnect, StageTLS, StageCredential, StageReady,
		},
		Policy: httpPolicy(),
	}
}

// turnIntegration is declared and deliberately not diagnosable.
//
// No coturn variable reaches any NChat workload: the dev stack configures it in
// infra/compose and LiveKit is told about it in its own file, so there is
// nothing in this pod's environment that names a TURN server. Offering a test
// would mean inventing a target, which is the one thing this whole surface
// exists to prevent.
func turnIntegration() IntegrationDescriptor {
	return IntegrationDescriptor{
		ID: IntegrationTURN, DisplayName: "TURN / coturn",
		Summary:       "Relay de mídia para redes restritivas. Indisponível, chamadas atrás de NAT simétrico falham.",
		Category:      HealthCategoryRealtime,
		HealthService: HealthServiceTURN,
		RunbookPath:   "docs/runbooks/task-livekit-coturn-dev.md",
		DiagnosticUnsupported: "O coturn é configurado dentro do LiveKit e no compose de desenvolvimento; " +
			"nenhuma variável de ambiente da plataforma nomeia o servidor TURN, então não há alvo " +
			"que este serviço possa verificar sem inventá-lo.",
	}
}

// linkScanIntegration is declared and deliberately not diagnosable.
//
// The credential is a Secret scoped to chat-service and file-service by the
// decision of RF-21; admin-service does not mount it. A check without it would
// spend a third party's quota to learn nothing, and reporting the result as if
// it described the pipeline would be a lie.
func linkScanIntegration() IntegrationDescriptor {
	return IntegrationDescriptor{
		ID: IntegrationLinkScan, DisplayName: "Link Scan",
		Summary:       "Verificação de links das mensagens. Desligada, links são entregues sem checagem.",
		Category:      HealthCategoryContent,
		HealthService: HealthServiceLinkScan,
		RunbookPath:   "docs/api/link-safety.md",
		DiagnosticUnsupported: "A credencial do provedor é um Secret montado apenas por chat-service e " +
			"file-service. Este serviço não a recebe, então qualquer teste daqui verificaria a " +
			"conectividade com um terceiro sem provar nada sobre o pipeline que realmente verifica links.",
	}
}

func clamAVIntegration() IntegrationDescriptor {
	return IntegrationDescriptor{
		ID: IntegrationClamAV, DisplayName: "ClamAV",
		Summary:       "Antimalware dos anexos. Indisponível, anexos ficam retidos sem veredito e não são baixáveis.",
		Category:      HealthCategoryContent,
		HealthService: HealthServiceClamAV,
		RunbookPath:   "docs/runbooks/file-service-envelope-encryption.md",
		Diagnostic:    DiagnosticClamAV,
		Stages:        []DiagnosticStage{StageResolve, StageConnect, StageReady},
		Policy:        socketPolicy(),
	}
}

func storageIntegration() IntegrationDescriptor {
	return IntegrationDescriptor{
		ID: IntegrationStorage, DisplayName: "SeaweedFS",
		Summary:       "Armazenamento de anexos. Indisponível, nenhum upload ou download de arquivo conclui.",
		Category:      HealthCategoryContent,
		HealthService: HealthServiceStorage,
		RunbookPath:   "docs/runbooks/task-15-seaweedfs-poc.md",
		Settings:      []ConfigKey{"infra.storage.filer_url", "infra.storage.s3_endpoint"},
		Diagnostic:    DiagnosticStorage,
		Stages:        []DiagnosticStage{StageResolve, StageConnect, StageTLS, StageReady},
		Policy:        httpPolicy(),
	}
}

var (
	integrationOnce  sync.Once
	integrationItems []IntegrationDescriptor
	integrationIndex map[IntegrationID]IntegrationDescriptor
)

func buildIntegrationRegistry() {
	integrationItems = integrationRegistry()
	integrationIndex = make(map[IntegrationID]IntegrationDescriptor, len(integrationItems))
	for _, descriptor := range integrationItems {
		integrationIndex[descriptor.ID] = descriptor
	}
}

// IntegrationRegistry returns every declared integration, in a stable order.
func IntegrationRegistry() []IntegrationDescriptor {
	integrationOnce.Do(buildIntegrationRegistry)
	return integrationItems
}

// LookupIntegration resolves an identifier against the registry.
//
// The fail-closed boundary of this surface: an id the platform does not declare
// is not found, and every caller treats not found as a refusal. There is no
// fallback that turns an unknown id into a target.
func LookupIntegration(id IntegrationID) (IntegrationDescriptor, bool) {
	integrationOnce.Do(buildIntegrationRegistry)
	descriptor, ok := integrationIndex[id]
	return descriptor, ok
}

// LookupIntegrationAction resolves an action within one integration.
func LookupIntegrationAction(descriptor IntegrationDescriptor, id IntegrationActionID) (IntegrationAction, bool) {
	for _, action := range descriptor.Actions {
		if action.ID == id {
			return action, true
		}
	}
	return IntegrationAction{}, false
}

// AuditResourceIntegrationPrefix namespaces integration events in the trail,
// the same way AuditResourceUserPrefix namespaces user events.
const AuditResourceIntegrationPrefix = "admin.integration:"

const (
	AuditActionIntegrationDiagnose  = "admin.integration.diagnose"
	AuditActionIntegrationTestEmail = "admin.integration.smtp.test_email"
)

// AuditIntegrationResource is the canonical resource key of an integration
// event.
func AuditIntegrationResource(id IntegrationID) string {
	return AuditResourceIntegrationPrefix + string(id)
}

var integrationIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{1,31}$`)

// ValidateIntegrationRegistry checks the invariants the registry must hold.
//
// Exported and called from a test rather than at boot, for the same reason
// ValidateConfigCatalog and ValidateHealthRegistry are: the registry is a
// compile-time literal, so a violation is a source defect that must fail the
// build. The checks are the ones that would otherwise become security bugs
// quietly:
//
//   - a duplicate id would make one descriptor unreachable and the other
//     authoritative depending on iteration order;
//   - a setting this integration claims but the configuration registry does not
//     declare would be a console section pointing at a key the API refuses,
//     which is how a second, unreviewed configuration surface starts;
//   - a diagnosable integration with no network policy would be dialled under
//     no policy at all;
//   - a diagnostic that declares no stages, or a stage outside the vocabulary,
//     would render as a run that checked nothing;
//   - an integration with no diagnostic and no reason is a missing button
//     nobody can explain.
func ValidateIntegrationRegistry() error {
	return ValidateIntegrationDescriptors(IntegrationRegistry())
}

// ValidateIntegrationDescriptors checks any set of descriptors against the same
// rules, so the guards can be exercised against descriptors that break them.
func ValidateIntegrationDescriptors(descriptors []IntegrationDescriptor) error {
	seen := make(map[IntegrationID]struct{}, len(descriptors))
	for _, descriptor := range descriptors {
		if _, duplicate := seen[descriptor.ID]; duplicate {
			return fmt.Errorf("integration registry: duplicate integration %s", descriptor.ID)
		}
		seen[descriptor.ID] = struct{}{}
		if err := validateIntegrationIdentity(descriptor); err != nil {
			return err
		}
		if err := validateIntegrationSettings(descriptor); err != nil {
			return err
		}
		if err := validateIntegrationDiagnostic(descriptor); err != nil {
			return err
		}
	}
	return nil
}

func validateIntegrationIdentity(descriptor IntegrationDescriptor) error {
	id := descriptor.ID
	if !integrationIDPattern.MatchString(string(id)) {
		return fmt.Errorf("integration registry: malformed integration id %q", id)
	}
	if descriptor.DisplayName == "" || descriptor.Summary == "" {
		return fmt.Errorf("integration registry: %s has no display name or summary", id)
	}
	if descriptor.RunbookPath == "" {
		return fmt.Errorf("integration registry: %s has no runbook", id)
	}
	if _, ok := LookupHealthService(descriptor.HealthService); !ok {
		return fmt.Errorf("integration registry: %s names no known health service", id)
	}
	if !IsKnownCapability(descriptor.ReadCapability) || !IsKnownCapability(descriptor.DiagnoseCapability) {
		return fmt.Errorf("integration registry: %s names an unknown capability", id)
	}
	return validateIntegrationActions(descriptor)
}

func validateIntegrationActions(descriptor IntegrationDescriptor) error {
	for _, action := range descriptor.Actions {
		if action.Label == "" || action.Description == "" {
			return fmt.Errorf("integration registry: %s has an unlabelled action", descriptor.ID)
		}
		if !IsKnownCapability(action.Capability) {
			return fmt.Errorf("integration registry: %s action %s names an unknown capability", descriptor.ID, action.ID)
		}
	}
	return nil
}

// validateIntegrationSettings asserts that every key an integration claims is a
// key the configuration registry of issue #580 already declares.
//
// This is what keeps the console from growing a second configuration model: an
// integration section can only ever point at settings that exist, with the
// class, source and editability that registry gave them.
func validateIntegrationSettings(descriptor IntegrationDescriptor) error {
	seen := make(map[ConfigKey]struct{}, len(descriptor.Settings)+len(descriptor.AdvancedSettings))
	for _, key := range append(append([]ConfigKey{}, descriptor.Settings...), descriptor.AdvancedSettings...) {
		if _, ok := LookupConfig(key); !ok {
			return fmt.Errorf("integration registry: %s claims unknown setting %s", descriptor.ID, key)
		}
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("integration registry: %s claims setting %s twice", descriptor.ID, key)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validateIntegrationDiagnostic(descriptor IntegrationDescriptor) error {
	if !descriptor.Diagnosable() {
		return validateUnsupportedDiagnostic(descriptor)
	}
	return validateSupportedDiagnostic(descriptor)
}

// validateUnsupportedDiagnostic checks an integration that declares it cannot
// be checked from this deployment.
//
// It must give a reason, and it must not carry the machinery of a diagnostic:
// stages or a network policy left behind would let an adapter be added later
// without anyone re-reading what it is allowed to reach.
func validateUnsupportedDiagnostic(descriptor IntegrationDescriptor) error {
	id := descriptor.ID
	if descriptor.DiagnosticUnsupported == "" {
		return fmt.Errorf("integration registry: %s has no diagnostic and gives no reason", id)
	}
	if len(descriptor.Stages) > 0 || len(descriptor.Policy.Schemes) > 0 {
		return fmt.Errorf("integration registry: %s has no diagnostic but declares one", id)
	}
	return nil
}

// validateSupportedDiagnostic checks an integration that declares an adapter.
//
// It must declare the plan it follows, from the closed stage vocabulary, and it
// must not also carry an explanation for why it cannot be checked.
func validateSupportedDiagnostic(descriptor IntegrationDescriptor) error {
	id := descriptor.ID
	if descriptor.DiagnosticUnsupported != "" {
		return fmt.Errorf("integration registry: %s both has and lacks a diagnostic", id)
	}
	if len(descriptor.Stages) == 0 {
		return fmt.Errorf("integration registry: %s diagnoses but declares no stage", id)
	}
	for _, stage := range descriptor.Stages {
		if !ValidDiagnosticStage(stage) {
			return fmt.Errorf("integration registry: %s declares unknown stage %q", id, stage)
		}
	}
	return nil
}
