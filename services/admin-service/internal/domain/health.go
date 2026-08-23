package domain

import (
	"fmt"
	"regexp"
	"sort"
	"sync"
	"time"
)

// Platform observability model (issue #581).
//
// Two rules shape everything in this file, and both are the reason it is a
// literal rather than anything a request can influence:
//
//   - the platform decides which services exist. A client names an id from the
//     registry below or names nothing at all; it never supplies a host, a port,
//     a URL, a DSN or a path. That is what keeps a health check from becoming
//     an SSRF primitive, and it is enforced by construction: HealthTarget is
//     resolved from this pod's own environment and there is no code path that
//     builds one from a request.
//   - "configured" is not "healthy", and "unknown" is not "healthy" either.
//     The five states below are distinct and the aggregation never collapses
//     them.

// HealthState is what the platform can honestly say about one dependency.
//
// Five values, and the distinctions between them are load bearing:
//
//   - HealthHealthy: the check ran and the dependency answered correctly.
//   - HealthDegraded: the check ran and the dependency answered, but something
//     about the answer warrants attention — latency past the budget, a pool
//     near exhaustion, a partial success.
//   - HealthUnavailable: the check ran and the dependency did not answer, or
//     answered with a failure.
//   - HealthDisabled: the deployment turned this integration off. Not a
//     failure, never an alert, and deliberately not the same as unavailable.
//   - HealthUnknown: no check ran, so the platform knows nothing. It is never
//     rendered as healthy, and it is never silently upgraded.
type HealthState string

const (
	HealthHealthy     HealthState = "healthy"
	HealthDegraded    HealthState = "degraded"
	HealthUnavailable HealthState = "unavailable"
	HealthDisabled    HealthState = "disabled"
	HealthUnknown     HealthState = "unknown"
)

// healthStateRank orders the states by how much attention they demand.
//
// Used only to compute the overall state and to sort the Health Center's
// default view. Disabled ranks below healthy because a deliberately switched
// off integration is the least interesting row on the page; unknown ranks
// above healthy because not knowing is worse than knowing it works.
var healthStateRank = map[HealthState]int{
	HealthDisabled:    0,
	HealthHealthy:     1,
	HealthUnknown:     2,
	HealthDegraded:    3,
	HealthUnavailable: 4,
}

// ValidHealthState reports whether the platform defines this state.
func ValidHealthState(state HealthState) bool {
	_, ok := healthStateRank[state]
	return ok
}

// HealthStateRank exposes the ordering so the HTTP layer can sort without
// duplicating the table.
func HealthStateRank(state HealthState) int {
	rank, ok := healthStateRank[state]
	if !ok {
		return healthStateRank[HealthUnknown]
	}
	return rank
}

// HealthErrorCategory is the sanitized reason a check did not come back
// healthy.
//
// This closed set is the entire vocabulary the Admin API uses to describe a
// failed dependency. A library's error text, a driver's message, a remote
// service's response body and a stack trace never reach a client: they are
// classified into one of these and discarded. An operator gets a category and
// a sentence written here; an attacker gets nothing that maps the cluster.
type HealthErrorCategory string

const (
	HealthErrorNone HealthErrorCategory = ""
	// HealthErrorConnectionTimeout: the dependency did not answer in time.
	HealthErrorConnectionTimeout HealthErrorCategory = "connection_timeout"
	// HealthErrorAuthenticationFailed: it answered, and refused the
	// credential this pod holds.
	HealthErrorAuthenticationFailed HealthErrorCategory = "authentication_failed"
	// HealthErrorTLSError: the transport could not be secured. Never resolved
	// by relaxing verification.
	HealthErrorTLSError HealthErrorCategory = "tls_error"
	// HealthErrorDependencyUnavailable: it refused the connection, reset it,
	// or answered with a server error.
	HealthErrorDependencyUnavailable HealthErrorCategory = "dependency_unavailable"
	// HealthErrorInvalidConfiguration: the deployment describes something this
	// service cannot use — a malformed endpoint, a discovery document that
	// contradicts its own issuer.
	HealthErrorInvalidConfiguration HealthErrorCategory = "invalid_configuration"
	// HealthErrorCapacityWarning: it answered, and reported itself close to a
	// limit.
	HealthErrorCapacityWarning HealthErrorCategory = "capacity_warning"
	// HealthErrorNotObservable: no check ran because this pod does not receive
	// the configuration that names the target. It is a statement about this
	// deployment's topology, not about the dependency, and it is the reason
	// unknown exists as a state.
	HealthErrorNotObservable HealthErrorCategory = "not_observable"
	// HealthErrorProtocolError: it answered with something this build cannot
	// interpret. Never treated as evidence of health.
	HealthErrorProtocolError HealthErrorCategory = "protocol_error"
)

var healthErrorCategories = map[HealthErrorCategory]struct{}{
	HealthErrorNone:                  {},
	HealthErrorConnectionTimeout:     {},
	HealthErrorAuthenticationFailed:  {},
	HealthErrorTLSError:              {},
	HealthErrorDependencyUnavailable: {},
	HealthErrorInvalidConfiguration:  {},
	HealthErrorCapacityWarning:       {},
	HealthErrorNotObservable:         {},
	HealthErrorProtocolError:         {},
}

// ValidHealthErrorCategory reports whether the platform defines this category.
func ValidHealthErrorCategory(category HealthErrorCategory) bool {
	_, ok := healthErrorCategories[category]
	return ok
}

// HealthServiceID is the stable identifier of one checked dependency.
//
// It is the only health-related value a client is ever allowed to name, and it
// is resolved against the registry below before it is used for anything. There
// is no identifier that means "whatever host I put here".
type HealthServiceID string

const (
	HealthServicePostgres  HealthServiceID = "postgres"
	HealthServiceValkey    HealthServiceID = "valkey"
	HealthServiceOIDC      HealthServiceID = "oidc"
	HealthServiceSMTP      HealthServiceID = "smtp"
	HealthServiceLiveKit   HealthServiceID = "livekit"
	HealthServiceTURN      HealthServiceID = "turn"
	HealthServiceClamAV    HealthServiceID = "clamav"
	HealthServiceStorage   HealthServiceID = "storage"
	HealthServiceLinkScan  HealthServiceID = "link_scan"
	HealthServiceWebSocket HealthServiceID = "websocket"
)

// HealthCategory groups the registry for display. A closed set, so it is safe
// as a Prometheus label alongside the service id.
type HealthCategory string

const (
	HealthCategoryData        HealthCategory = "data"
	HealthCategoryIdentity    HealthCategory = "identity"
	HealthCategoryMessaging   HealthCategory = "messaging"
	HealthCategoryRealtime    HealthCategory = "realtime"
	HealthCategoryContent     HealthCategory = "content"
	HealthCategoryObservation HealthCategory = "observation"
)

// HealthProbeKind names how a dependency is reached.
//
// The service layer switches on it to pick a probe. It is declared here, next
// to the target variables, because "what protocol is this" and "which variable
// names the endpoint" are one decision and splitting them is how a TCP target
// ends up in an HTTP client.
type HealthProbeKind string

const (
	// HealthProbePool uses the connection pool this service already holds. No
	// address is resolved and nothing is dialled.
	HealthProbePool HealthProbeKind = "pool"
	// HealthProbeHTTP issues one bounded GET against a server-configured URL.
	HealthProbeHTTP HealthProbeKind = "http"
	// HealthProbeValkey speaks one RESP PING on a host:port target.
	HealthProbeValkey HealthProbeKind = "valkey"
	// HealthProbeClamd speaks clamd's PING and VERSION on a host:port target.
	HealthProbeClamd HealthProbeKind = "clamd"
	// HealthProbeSMTP opens a connection and reads the greeting. It never
	// sends a message, and it never authenticates.
	HealthProbeSMTP HealthProbeKind = "smtp"
	// HealthProbeNone means the platform can state the configured/disabled
	// fact and nothing more. Resolving one of these never opens a socket.
	HealthProbeNone HealthProbeKind = "none"
)

// HealthServiceDescriptor is one row of the closed registry.
//
// Every field is a compile-time constant. In particular EnabledVar, TargetVars
// and CredentialVar are *names* of environment variables, never values and
// never anything a request can influence: resolution reads this pod's own
// environment and nothing else.
type HealthServiceDescriptor struct {
	ID          HealthServiceID
	DisplayName string
	Category    HealthCategory
	// Description says what breaks for users when this dependency is down. It
	// is the "impacto" line of an alert and it is written here so every alert
	// says something operational rather than repeating the service name.
	Description string
	Probe       HealthProbeKind
	// EnabledVar is the deployment flag that turns the integration on. Empty
	// means the integration has no switch and is always expected.
	EnabledVar string
	// EnabledDefault is what an absent EnabledVar means. Declared rather than
	// assumed, because "absent" means off for LiveKit and on for the database.
	EnabledDefault bool
	// TargetVars are the variables that, together, name the endpoint. All of
	// them must be observable for a probe to run; otherwise the result is
	// unknown/not_observable, which is a statement about this pod's topology
	// and not about the dependency.
	TargetVars []string
	// CredentialVar is an optional variable carrying a secret the probe needs
	// (Valkey's password). Its value is used to build one command and is never
	// stored, logged, wrapped in an error or marshalled.
	CredentialVar string
	// ConfigKey links the row to the configuration catalogue of issue #580. It
	// is a static key, resolved client-side into a deep link into /configuration.
	ConfigKey ConfigKey
	// RunbookPath is a static, repository-relative documentation path. It is
	// never a URL and never comes from a response.
	RunbookPath string
	// Critical marks a dependency whose loss takes the platform down rather
	// than degrading a feature. It decides whether the overall state goes to
	// unavailable or to degraded.
	Critical bool
	// LatencyBudget is the round trip past which a working dependency is
	// reported degraded. Zero means latency is measured and reported but never
	// judged.
	LatencyBudget time.Duration
}

// healthRegistry is the whole set of dependencies the Admin API checks.
//
// The order is the order the Health Center renders by default and the order the
// dashboard counts in, so it is a slice and not a map.
func healthRegistry() []HealthServiceDescriptor {
	return []HealthServiceDescriptor{
		{
			ID: HealthServicePostgres, DisplayName: "PostgreSQL",
			Category:    HealthCategoryData,
			Description: "Banco de dados da plataforma. Sem ele nenhuma sessão, mensagem ou configuração é lida ou gravada.",
			Probe:       HealthProbePool, EnabledDefault: true,
			ConfigKey: "infra.postgres.host", RunbookPath: "docs/runbooks/task-14-health-checks.md",
			Critical: true, LatencyBudget: 250 * time.Millisecond,
		},
		{
			ID: HealthServiceValkey, DisplayName: "Valkey",
			Category:    HealthCategoryData,
			Description: "Cache e barramento de broadcast do WebSocket. Degradado, a entrega em tempo real entre instâncias fica inconsistente.",
			Probe:       HealthProbeValkey, EnabledDefault: true,
			TargetVars: []string{"VALKEY_HOST", "VALKEY_PORT"}, CredentialVar: "VALKEY_PASSWORD",
			ConfigKey: "infra.valkey.host", RunbookPath: "docs/runbooks/task-16-valkey-poc.md",
			Critical: false, LatencyBudget: 150 * time.Millisecond,
		},
		{
			ID: HealthServiceOIDC, DisplayName: "Keycloak / OIDC",
			Category:    HealthCategoryIdentity,
			Description: "Provedor de single sign-on. Indisponível, resta apenas o login local.",
			Probe:       HealthProbeHTTP, EnabledVar: "OIDC_ENABLED",
			TargetVars: []string{"OIDC_ISSUER_URL"},
			ConfigKey:  "oidc.enabled", RunbookPath: "docs/runbooks/task-auth-oidc-keycloak.md",
			Critical: false, LatencyBudget: 1500 * time.Millisecond,
		},
		{
			ID: HealthServiceSMTP, DisplayName: "SMTP",
			Category:    HealthCategoryMessaging,
			Description: "Relay de e-mail. Sem ele convites e redefinições de senha ficam na fila sem sair.",
			Probe:       HealthProbeSMTP, EnabledVar: "SMTP_WORKER_ENABLED",
			TargetVars: []string{"SMTP_HOST", "SMTP_PORT"},
			ConfigKey:  "email.smtp.worker_enabled", RunbookPath: "docs/runbooks/task-smtp-bruteforce-login-audit.md",
			Critical: false, LatencyBudget: 2 * time.Second,
		},
		{
			ID: HealthServiceLiveKit, DisplayName: "LiveKit",
			Category:    HealthCategoryRealtime,
			Description: "Servidor de mídia das chamadas. Indisponível, nenhuma chamada é estabelecida.",
			Probe:       HealthProbeHTTP, EnabledVar: "LIVEKIT_ENABLED",
			TargetVars: []string{"LIVEKIT_API_URL"},
			ConfigKey:  "calls.livekit.enabled", RunbookPath: "docs/runbooks/task-livekit-coturn-dev.md",
			Critical: false, LatencyBudget: 1500 * time.Millisecond,
		},
		{
			ID: HealthServiceTURN, DisplayName: "TURN / coturn",
			Category:    HealthCategoryRealtime,
			Description: "Relay de mídia para redes restritivas. Indisponível, chamadas atrás de NAT simétrico falham.",
			Probe:       HealthProbeNone, EnabledVar: "LIVEKIT_ENABLED",
			ConfigKey: "calls.livekit.enabled", RunbookPath: "docs/runbooks/task-livekit-coturn-dev.md",
			Critical: false,
		},
		{
			ID: HealthServiceClamAV, DisplayName: "ClamAV",
			Category:    HealthCategoryContent,
			Description: "Antimalware dos anexos. Indisponível, anexos ficam retidos sem verdito e não são baixáveis.",
			Probe:       HealthProbeClamd, EnabledDefault: true,
			TargetVars: []string{"FILE_MALWARE_SCANNER_ADDRESS"},
			ConfigKey:  "", RunbookPath: "docs/runbooks/file-service-envelope-encryption.md",
			Critical: false, LatencyBudget: 500 * time.Millisecond,
		},
		{
			ID: HealthServiceStorage, DisplayName: "SeaweedFS",
			Category:    HealthCategoryContent,
			Description: "Armazenamento de anexos. Indisponível, nenhum upload ou download de arquivo conclui.",
			Probe:       HealthProbeHTTP, EnabledDefault: true,
			TargetVars: []string{"SEAWEEDFS_FILER_URL"},
			ConfigKey:  "infra.storage.filer_url", RunbookPath: "docs/runbooks/task-15-seaweedfs-poc.md",
			Critical: false, LatencyBudget: 500 * time.Millisecond,
		},
		{
			ID: HealthServiceLinkScan, DisplayName: "Link Scan",
			Category:    HealthCategoryContent,
			Description: "Verificação de links das mensagens. Desligada, links são entregues sem checagem.",
			// Deliberately never probed: the only endpoint is Cloudflare's, the
			// credential is scoped to chat-service and file-service, and a
			// periodic call would spend a third party's quota to learn nothing
			// this pod can act on.
			Probe: HealthProbeNone, EnabledVar: "CHAT_LINK_SAFETY_ENABLED",
			ConfigKey: "", RunbookPath: "docs/api/link-safety.md",
			Critical: false,
		},
		{
			ID: HealthServiceWebSocket, DisplayName: "WebSocket / tempo real",
			Category:    HealthCategoryRealtime,
			Description: "Entrega em tempo real das mensagens. Degradada, o chat só atualiza ao recarregar.",
			Probe:       HealthProbeNone, EnabledVar: "VALKEY_WS_BROADCAST_ENABLED",
			ConfigKey: "", RunbookPath: "docs/ws-hub-foundation.md",
			Critical: false,
		},
	}
}

var (
	healthOnce  sync.Once
	healthItems []HealthServiceDescriptor
	healthIndex map[HealthServiceID]HealthServiceDescriptor
)

func buildHealthRegistry() {
	healthItems = healthRegistry()
	healthIndex = make(map[HealthServiceID]HealthServiceDescriptor, len(healthItems))
	for _, descriptor := range healthItems {
		healthIndex[descriptor.ID] = descriptor
	}
}

// HealthRegistry returns every declared dependency, in a stable order.
func HealthRegistry() []HealthServiceDescriptor {
	healthOnce.Do(buildHealthRegistry)
	return healthItems
}

// LookupHealthService resolves an identifier against the registry.
//
// This is the fail-closed boundary of the whole surface: an id the platform
// does not declare is not found, and every caller treats not found as a
// refusal. There is no fallback that turns an unknown id into a target.
func LookupHealthService(id HealthServiceID) (HealthServiceDescriptor, bool) {
	healthOnce.Do(buildHealthRegistry)
	descriptor, ok := healthIndex[id]
	return descriptor, ok
}

// ServiceHealth is the result of checking one dependency.
//
// LatencyMS is a pointer because "not measured" and "measured as zero" are
// different facts, and a disabled integration must not claim a round trip of
// 0 ms. CheckedAt is always set: a result with no timestamp is a result nobody
// can judge the freshness of.
type ServiceHealth struct {
	Descriptor HealthServiceDescriptor
	State      HealthState
	// Enabled reports the deployment's switch for this integration.
	Enabled bool
	// Observable reports whether this pod receives the configuration that
	// names the target. False is what produces unknown/not_observable, and it
	// is deliberately a different fact from "not configured".
	Observable bool
	LatencyMS  *int64
	CheckedAt  time.Time
	// ErrorCategory is from the closed set. Empty when the state is healthy or
	// disabled.
	ErrorCategory HealthErrorCategory
	// Detail is a short, hand-written, actionable sentence. It never contains
	// a host, a port, a DSN, a credential or text produced by a dependency.
	Detail string
	// Version is an optional, sanitized version string. Only populated when a
	// dependency reports one that is useful and safe to show.
	Version string
}

// HealthSnapshot is one complete collection.
type HealthSnapshot struct {
	CollectedAt time.Time
	Services    []ServiceHealth
}

// Overall is the single state that summarises the platform.
//
// The rule is deliberately not "the worst state wins":
//
//   - a critical dependency that is unavailable makes the platform
//     unavailable, because nothing works without it;
//   - a non-critical dependency that is unavailable makes the platform
//     degraded, because the rest of it still works;
//   - anything degraded or unknown makes the platform degraded. Unknown is not
//     healthy, and refusing to say so is how a blind spot gets reported as a
//     green dashboard;
//   - disabled contributes nothing.
//
// An empty snapshot is unknown, not healthy.
func (s HealthSnapshot) Overall() HealthState {
	if len(s.Services) == 0 {
		return HealthUnknown
	}
	overall := HealthHealthy
	for _, service := range s.Services {
		contribution := contributionOf(service)
		if contribution == HealthUnavailable {
			return HealthUnavailable
		}
		if contribution == HealthDegraded {
			overall = HealthDegraded
		}
	}
	return overall
}

// contributionOf is what one dependency's result does to the platform's state.
//
// Three answers rather than five: a dependency either takes the platform down,
// pulls it to degraded, or contributes nothing. Which of the three a result
// produces is the whole of the rollup rule, and keeping it here is what makes
// Overall a loop rather than a nest of conditions.
func contributionOf(service ServiceHealth) HealthState {
	switch service.State {
	case HealthUnavailable:
		if service.Descriptor.Critical {
			return HealthUnavailable
		}
		return HealthDegraded
	case HealthDegraded, HealthUnknown:
		return HealthDegraded
	case HealthHealthy, HealthDisabled:
		return HealthHealthy
	}
	// A state this build does not define is not evidence that anything works.
	return HealthDegraded
}

// CountByState counts the snapshot per state, including the states with no
// members, so the dashboard renders a stable set of counters.
func (s HealthSnapshot) CountByState() map[HealthState]int {
	counts := map[HealthState]int{
		HealthHealthy: 0, HealthDegraded: 0, HealthUnavailable: 0,
		HealthDisabled: 0, HealthUnknown: 0,
	}
	for _, service := range s.Services {
		if _, ok := counts[service.State]; ok {
			counts[service.State]++
		}
	}
	return counts
}

// HealthAlertSeverity is how much attention an alert demands.
type HealthAlertSeverity string

const (
	HealthAlertCritical HealthAlertSeverity = "critical"
	HealthAlertWarning  HealthAlertSeverity = "warning"
)

// HealthAlert is one actionable condition.
//
// Derived from the current snapshot on every collection and never persisted:
// an alert that no longer describes the platform simply stops being produced,
// which is also why there is no acknowledge, no snooze and no lifecycle to get
// out of sync with reality.
type HealthAlert struct {
	ServiceID HealthServiceID
	Severity  HealthAlertSeverity
	Title     string
	// Impact is what users experience while this holds.
	Impact string
	// Action is what the operator should do next.
	Action string
	// Since is when this collection observed the condition. It is the age of
	// the observation, not of the outage: this service keeps no history, and
	// claiming otherwise would be inventing a fact.
	Since time.Time
	// RunbookPath and ConfigKey are the static destinations of the alert.
	RunbookPath string
	ConfigKey   ConfigKey
}

// DeriveAlerts produces one alert per service that needs attention.
//
// One per service, at most: a dependency that is both slow and refusing
// connections is one problem, and emitting two rows for it is how a dashboard
// teaches operators to stop reading it. Healthy and disabled produce nothing,
// and unknown produces nothing either — not knowing is shown as a state on the
// Health Center, and turning every blind spot into an alert would bury the
// real ones.
func DeriveAlerts(snapshot HealthSnapshot) []HealthAlert {
	alerts := make([]HealthAlert, 0, len(snapshot.Services))
	for _, service := range snapshot.Services {
		if alert, ok := alertFor(service, snapshot.CollectedAt); ok {
			alerts = append(alerts, alert)
		}
	}
	sort.SliceStable(alerts, func(i, j int) bool {
		return alerts[i].Severity == HealthAlertCritical && alerts[j].Severity != HealthAlertCritical
	})
	return alerts
}

func alertFor(service ServiceHealth, collectedAt time.Time) (HealthAlert, bool) {
	severity, title, ok := alertHeadline(service)
	if !ok {
		return HealthAlert{}, false
	}
	return HealthAlert{
		ServiceID:   service.Descriptor.ID,
		Severity:    severity,
		Title:       title,
		Impact:      service.Descriptor.Description,
		Action:      alertAction(service.ErrorCategory),
		Since:       collectedAt,
		RunbookPath: service.Descriptor.RunbookPath,
		ConfigKey:   service.Descriptor.ConfigKey,
	}, true
}

// alertHeadline decides whether a result is worth an alert at all, and how
// loud it should be.
func alertHeadline(service ServiceHealth) (HealthAlertSeverity, string, bool) {
	name := service.Descriptor.DisplayName
	switch service.State {
	case HealthUnavailable:
		if service.Descriptor.Critical {
			return HealthAlertCritical, name + " indisponível", true
		}
		return HealthAlertWarning, name + " indisponível", true
	case HealthDegraded:
		return HealthAlertWarning, name + " degradado", true
	case HealthHealthy, HealthDisabled, HealthUnknown:
		return "", "", false
	}
	return "", "", false
}

// alertActions is the recommended next step per sanitized category. A lookup
// table rather than a chain of conditionals, so adding a category is one line
// and forgetting one is a compile-time-visible gap rather than a silent
// fallthrough.
var alertActions = map[HealthErrorCategory]string{
	HealthErrorConnectionTimeout:     "Verifique a rede e a carga da dependência; o tempo limite do check é curto e propositalmente não configurável pelo console.",
	HealthErrorAuthenticationFailed:  "Revise a credencial do deployment para esta integração e siga a rotação em docs/runbooks/sealed-secrets-rotation.md.",
	HealthErrorTLSError:              "Verifique o certificado e a cadeia de confiança da dependência. A verificação TLS não é desligável.",
	HealthErrorDependencyUnavailable: "Verifique se a dependência está de pé e aceitando conexões no ambiente atual.",
	HealthErrorInvalidConfiguration:  "Revise a configuração desta integração; o valor observado não descreve um endpoint utilizável.",
	HealthErrorCapacityWarning:       "A dependência respondeu próxima de um limite próprio. Avalie capacidade antes que vire indisponibilidade.",
	HealthErrorProtocolError:         "A dependência respondeu algo que este build não interpreta. Confirme a versão implantada.",
}

func alertAction(category HealthErrorCategory) string {
	if action, ok := alertActions[category]; ok {
		return action
	}
	return "Abra o Health Center para o diagnóstico completo desta integração."
}

var healthServiceIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{1,31}$`)

// ValidateHealthRegistry checks the invariants the registry must hold.
//
// Exported and called from a test rather than at boot, for the same reason
// ValidateConfigCatalog is: the registry is a compile-time literal, so a
// violation is a source defect that must fail the build and not a runtime
// condition to degrade around. The checks are the ones that would otherwise
// become bugs quietly:
//
//   - a duplicate id would make one descriptor unreachable and the other
//     authoritative depending on iteration order, which is also how a lookup
//     could start resolving to a target nobody reviewed;
//   - an id that is not a plain slug would end up in a Prometheus label and in
//     a URL query, so it is constrained to the alphabet both accept;
//   - a probe kind that needs a target and declares none could only ever be
//     resolved by taking the target from somewhere else — which is the exact
//     shape of the SSRF this design refuses;
//   - a descriptor with no runbook is a row an operator cannot act on.
func ValidateHealthRegistry() error {
	return ValidateHealthDescriptors(HealthRegistry())
}

// ValidateHealthDescriptors checks any set of descriptors against the same
// rules, so the guards can be exercised against descriptors that break them.
func ValidateHealthDescriptors(descriptors []HealthServiceDescriptor) error {
	seen := make(map[HealthServiceID]struct{}, len(descriptors))
	for _, descriptor := range descriptors {
		if _, duplicate := seen[descriptor.ID]; duplicate {
			return fmt.Errorf("health registry: duplicate service %s", descriptor.ID)
		}
		seen[descriptor.ID] = struct{}{}
		if err := validateHealthIdentity(descriptor); err != nil {
			return err
		}
		if err := validateHealthProbe(descriptor); err != nil {
			return err
		}
	}
	return nil
}

func validateHealthIdentity(descriptor HealthServiceDescriptor) error {
	if !healthServiceIDPattern.MatchString(string(descriptor.ID)) {
		return fmt.Errorf("health registry: malformed service id %q", descriptor.ID)
	}
	if descriptor.DisplayName == "" || descriptor.Description == "" {
		return fmt.Errorf("health registry: %s has no display name or description", descriptor.ID)
	}
	if descriptor.Category == "" {
		return fmt.Errorf("health registry: %s has no category", descriptor.ID)
	}
	if descriptor.RunbookPath == "" {
		return fmt.Errorf("health registry: %s has no runbook", descriptor.ID)
	}
	return nil
}

// validateHealthProbe checks that the way a dependency is reached agrees with
// what the descriptor says about it.
func validateHealthProbe(descriptor HealthServiceDescriptor) error {
	switch descriptor.Probe {
	case HealthProbeNone, HealthProbePool:
		if len(descriptor.TargetVars) > 0 {
			return fmt.Errorf("health registry: %s dials nothing but declares target variables", descriptor.ID)
		}
		return nil
	case HealthProbeHTTP, HealthProbeValkey, HealthProbeClamd, HealthProbeSMTP:
		if len(descriptor.TargetVars) == 0 {
			return fmt.Errorf("health registry: %s dials but declares no target variable", descriptor.ID)
		}
		return nil
	}
	return fmt.Errorf("health registry: %s names an unknown probe kind %q", descriptor.ID, descriptor.Probe)
}
