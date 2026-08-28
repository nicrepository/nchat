package service

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
)

// The integration surface (issue #582).
//
// It composes three things that already exist rather than adding a fourth:
//
//   - the configuration registry of issue #580 supplies every setting, with the
//     class, source and editability it already declared. Nothing here writes a
//     setting or a credential, and there is no second configuration model;
//   - the health collection of issue #581 supplies the passive status, from the
//     shared snapshot. Opening this page contacts nothing;
//   - the integration registry of this issue supplies the grouping, the network
//     policy and the diagnostic adapter.
//
// The one genuinely new capability is the active diagnostic, and every property
// that bounds it lives in this file: it is explicit, it is authorized, it is
// rate limited per administrator and integration, its concurrency is capped
// process-wide, it is cancelled with the request that asked for it, and it is
// audited whether it passes or fails.

const (
	// diagnosticBudget is the ceiling for one whole run. Each stage has its own
	// shorter timeout; this exists so a pathological case still terminates.
	diagnosticBudget = 20 * time.Second
	// maxConcurrentDiagnostics bounds how many runs this pod performs at once.
	//
	// Two, deliberately small. A diagnostic is an operator pressing a button,
	// not a background collection, and the number is what stops a room full of
	// consoles — or one script holding a stolen session — from turning the
	// Admin API into a source of outbound load.
	maxConcurrentDiagnostics = 2
)

// KeyedRateLimiter is the budget one administrator has for one action.
// Satisfied by httpapi.IPRateLimiter, which is shared rather than duplicated so
// there is one token bucket implementation in this service to review.
type KeyedRateLimiter interface {
	Allow(key string) bool
}

// IntegrationHealth is the passive collection this surface reads.
//
// `force` is deliberately never true here: opening the integrations page must
// not contact anything, and the Health Center already owns the refresh button.
type IntegrationHealth interface {
	Snapshot(ctx context.Context, force bool) (domain.HealthSnapshot, error)
}

// IntegrationAuthorizer re-proves the acting administrator against the database.
//
// Narrow on purpose: this surface performs work that leaves the platform, and
// the only question it has to ask is "does this session still hold this
// capability, right now". It knows nothing about cookies, headers, CSRF or HTTP
// status — the foundation stays the single source of truth for what authority
// means, and this is a read of it.
//
// Satisfied by storage.PGXAdminStore.ReauthorizeAction, which evaluates the
// same predicate the transactional check of issue #580 uses, without locks and
// without a transaction. See mutation_authorization.go for why a lock-free read
// is the right primitive for an external side effect.
type IntegrationAuthorizer interface {
	ReauthorizeAction(ctx context.Context, authorization domain.MutationAuthorization) error
}

// IntegrationConfiguration is the configuration catalogue this surface reads.
type IntegrationConfiguration interface {
	Catalog(ctx context.Context) (ConfigCatalogView, error)
}

// IntegrationStatus is one integration as the console renders it.
type IntegrationStatus struct {
	Descriptor domain.IntegrationDescriptor
	// Health is the passive result from the shared snapshot.
	Health domain.ServiceHealth
	// Settings are this integration's configuration keys with their effective
	// state, in declaration order, common ones first.
	//
	// Empty when the actor does not hold admin.config.read: the configuration
	// catalogue is guarded reconnaissance, and an integrations-only operator
	// sees the status and the diagnostic without the inventory.
	Settings []IntegrationSetting
	// SettingsVisible distinguishes "this integration has no settings" from
	// "you may not see them", which are different sentences on the screen.
	SettingsVisible bool
	// Diagnosable reports whether this deployment can run the active check.
	Diagnosable bool
}

// IntegrationSetting is one configuration key attached to an integration.
type IntegrationSetting struct {
	Setting ConfigSetting
	// Advanced marks a rarely-touched setting, rendered collapsed.
	Advanced bool
}

// IntegrationsView is the whole surface as one read.
type IntegrationsView struct {
	CollectedAt  time.Time
	Integrations []IntegrationStatus
}

// IntegrationService serves the integration surface.
type IntegrationService struct {
	health        IntegrationHealth
	configuration IntegrationConfiguration
	authorizer    IntegrationAuthorizer
	audit         Recorder
	// lookupEnv resolves every diagnostic target. It is this pod's own
	// environment and nothing else: no request reaches it, and there is no
	// counterpart that writes a variable.
	lookupEnv       func(string) (string, bool)
	now             func() time.Time
	diagnoseLimiter KeyedRateLimiter
	emailLimiter    KeyedRateLimiter
	slots           chan struct{}
}

// NewIntegrationService builds the service against the real environment.
func NewIntegrationService(
	health IntegrationHealth,
	configuration IntegrationConfiguration,
	authorizer IntegrationAuthorizer,
	audit Recorder,
	diagnoseLimiter KeyedRateLimiter,
	emailLimiter KeyedRateLimiter,
) *IntegrationService {
	return &IntegrationService{
		health:          health,
		configuration:   configuration,
		authorizer:      authorizer,
		audit:           audit,
		lookupEnv:       os.LookupEnv,
		now:             time.Now,
		diagnoseLimiter: diagnoseLimiter,
		emailLimiter:    emailLimiter,
		slots:           make(chan struct{}, maxConcurrentDiagnostics),
	}
}

// NewIntegrationServiceWithEnv builds the service against a described
// environment, so a test can describe a deployment without mutating the
// process.
func NewIntegrationServiceWithEnv(
	health IntegrationHealth,
	configuration IntegrationConfiguration,
	authorizer IntegrationAuthorizer,
	audit Recorder,
	diagnoseLimiter KeyedRateLimiter,
	emailLimiter KeyedRateLimiter,
	lookupEnv func(string) (string, bool),
	now func() time.Time,
) *IntegrationService {
	service := NewIntegrationService(health, configuration, authorizer, audit, diagnoseLimiter, emailLimiter)
	if lookupEnv != nil {
		service.lookupEnv = lookupEnv
	}
	if now != nil {
		service.now = now
	}
	return service
}

// List returns every declared integration with its passive status.
//
// It contacts nothing. The health snapshot is the cached one the Health Center
// already collected, and the configuration catalogue is one database read, so
// opening this page costs the platform a query and no outbound connection.
func (s *IntegrationService) List(ctx context.Context, actor Actor) (IntegrationsView, error) {
	if s == nil || s.health == nil || s.configuration == nil {
		return IntegrationsView{}, domain.ErrUnavailable
	}
	snapshot, err := s.health.Snapshot(ctx, false)
	if err != nil {
		return IntegrationsView{}, err
	}
	settings, err := s.visibleSettings(ctx, actor)
	if err != nil {
		return IntegrationsView{}, err
	}
	states := indexHealth(snapshot)
	view := IntegrationsView{
		CollectedAt:  snapshot.CollectedAt,
		Integrations: make([]IntegrationStatus, 0, len(domain.IntegrationRegistry())),
	}
	for _, descriptor := range domain.IntegrationRegistry() {
		view.Integrations = append(view.Integrations, integrationStatus(descriptor, states, settings))
	}
	return view, nil
}

// visibleSettings reads the configuration catalogue, but only for an actor
// entitled to it.
//
// The capability check is here rather than at the route because the route
// guards the integrations surface, and the configuration inventory is a
// separate, separately granted thing. An operator with admin.integrations.read
// alone gets the status and the diagnostic; the inventory of endpoints and
// credentials still requires admin.config.read.
func (s *IntegrationService) visibleSettings(ctx context.Context, actor Actor) (map[domain.ConfigKey]ConfigSetting, error) {
	if !actor.Capabilities.Has(domain.CapabilityConfigRead) {
		return nil, nil
	}
	catalog, err := s.configuration.Catalog(ctx)
	if err != nil {
		return nil, err
	}
	settings := make(map[domain.ConfigKey]ConfigSetting, len(catalog.Settings))
	for _, setting := range catalog.Settings {
		settings[setting.Definition.Key] = setting
	}
	return settings, nil
}

func indexHealth(snapshot domain.HealthSnapshot) map[domain.HealthServiceID]domain.ServiceHealth {
	states := make(map[domain.HealthServiceID]domain.ServiceHealth, len(snapshot.Services))
	for _, service := range snapshot.Services {
		states[service.Descriptor.ID] = service
	}
	return states
}

func integrationStatus(
	descriptor domain.IntegrationDescriptor,
	states map[domain.HealthServiceID]domain.ServiceHealth,
	settings map[domain.ConfigKey]ConfigSetting,
) IntegrationStatus {
	status := IntegrationStatus{
		Descriptor:      descriptor,
		Health:          states[descriptor.HealthService],
		SettingsVisible: settings != nil,
		Diagnosable:     descriptor.Diagnosable(),
	}
	if settings == nil {
		return status
	}
	status.Settings = collectSettings(descriptor, settings)
	return status
}

func collectSettings(descriptor domain.IntegrationDescriptor, known map[domain.ConfigKey]ConfigSetting) []IntegrationSetting {
	collected := make([]IntegrationSetting, 0, len(descriptor.Settings)+len(descriptor.AdvancedSettings))
	for _, key := range descriptor.Settings {
		if setting, ok := known[key]; ok {
			collected = append(collected, IntegrationSetting{Setting: setting})
		}
	}
	for _, key := range descriptor.AdvancedSettings {
		if setting, ok := known[key]; ok {
			collected = append(collected, IntegrationSetting{Setting: setting, Advanced: true})
		}
	}
	return collected
}

// Diagnose runs one integration's active check.
//
// The whole guard chain, in order: the integration must be one the registry
// declares, it must have an adapter, the actor must be inside their budget, the
// pod must have a free slot, and only then is anything dialled. A failed check
// is a *result* and not an error — the report describes what happened stage by
// stage, and the endpoint answers 200 with it, because "SMTP recusou a
// credencial" is information and not a server fault.
func (s *IntegrationService) Diagnose(ctx context.Context, actor Actor, id domain.IntegrationID) (domain.DiagnosticReport, error) {
	if s == nil {
		return domain.DiagnosticReport{}, domain.ErrUnavailable
	}
	descriptor, err := s.admitDiagnostic(actor, id)
	if err != nil {
		return s.refuse(ctx, actor, id, domain.AuditActionIntegrationDiagnose, err)
	}
	// The database decides, not the snapshot the middleware produced: a role
	// revoked while this request was in flight must stop it before anything is
	// dialled.
	if err := s.reauthorize(ctx, actor, descriptor.DiagnoseCapability); err != nil {
		return s.refuse(ctx, actor, id, domain.AuditActionIntegrationDiagnose, err)
	}
	release, err := s.acquireSlot()
	if err != nil {
		return s.refuse(ctx, actor, id, domain.AuditActionIntegrationDiagnose, err)
	}
	defer release()

	report := s.run(ctx, descriptor, nil)
	s.recordDiagnostic(ctx, actor, id, domain.AuditActionIntegrationDiagnose, report, nil)
	return report, nil
}

// admitDiagnostic resolves the integration and spends the actor's budget.
func (s *IntegrationService) admitDiagnostic(actor Actor, id domain.IntegrationID) (domain.IntegrationDescriptor, error) {
	descriptor, ok := domain.LookupIntegration(id)
	if !ok {
		return domain.IntegrationDescriptor{}, domain.ErrNotFound
	}
	if !descriptor.Diagnosable() {
		return domain.IntegrationDescriptor{}, domain.ErrConflict
	}
	if !allow(s.diagnoseLimiter, actor.UserID+"|diagnose|"+string(id)) {
		return domain.IntegrationDescriptor{}, domain.ErrTooManyRequests
	}
	return descriptor, nil
}

// acquireSlot takes one of the process-wide diagnostic slots, without waiting.
//
// Refusing rather than queueing is deliberate: a queued diagnostic would hold a
// request open behind work the operator cannot see, and the honest answer to
// "the pod is already running two of these" is to say so and let them retry.
func (s *IntegrationService) acquireSlot() (func(), error) {
	select {
	case s.slots <- struct{}{}:
		return func() { <-s.slots }, nil
	default:
		return nil, domain.ErrTooManyRequests
	}
}

func allow(limiter KeyedRateLimiter, key string) bool {
	if limiter == nil {
		return true
	}
	return limiter.Allow(key)
}

// SendTestEmail delivers one fixed message through the configured relay.
//
// The destination is the authenticated administrator's own address and is not a
// parameter of this method by accident of signature — it is read from the actor
// the session guard built. There is no code path here that can address anybody
// else, which is why the console cannot be used as an open relay.
func (s *IntegrationService) SendTestEmail(ctx context.Context, actor Actor) (domain.DiagnosticReport, error) {
	if s == nil {
		return domain.DiagnosticReport{}, domain.ErrUnavailable
	}
	descriptor, recipient, err := s.admitTestEmail(actor)
	if err != nil {
		return s.refuse(ctx, actor, domain.IntegrationSMTP, domain.AuditActionIntegrationTestEmail, err)
	}
	if err := s.reauthorize(ctx, actor, domain.CapabilityIntegrationsManage); err != nil {
		return s.refuse(ctx, actor, domain.IntegrationSMTP, domain.AuditActionIntegrationTestEmail, err)
	}
	release, err := s.acquireSlot()
	if err != nil {
		return s.refuse(ctx, actor, domain.IntegrationSMTP, domain.AuditActionIntegrationTestEmail, err)
	}
	defer release()

	// The second check runs inside the SMTP session, after AUTH and NOOP and
	// immediately before the envelope. Everything up to that point is
	// reversible; MAIL/RCPT/DATA is not, so it is the last safe place to ask.
	// The closure keeps the answer, because a revocation seen there makes this
	// an administrative refusal rather than a diagnostic result.
	var denied error
	message := smtpTestMessage{
		recipient: recipient,
		authorize: func(deliverCtx context.Context) error {
			denied = s.reauthorize(deliverCtx, actor, domain.CapabilityIntegrationsManage)
			return denied
		},
	}
	report := s.run(ctx, descriptor, &message)
	if denied != nil {
		return s.refuse(ctx, actor, domain.IntegrationSMTP, domain.AuditActionIntegrationTestEmail, denied)
	}
	s.recordDiagnostic(ctx, actor, domain.IntegrationSMTP, domain.AuditActionIntegrationTestEmail, report, nil)
	return report, nil
}

// reauthorize asks the database whether this administrator may still act.
//
// Never actor.Capabilities: that set was loaded when the request arrived and
// says nothing about what has happened since. The authorization travels as the
// same domain.MutationAuthorization the configuration surface uses, so both
// paths name identity the same way.
func (s *IntegrationService) reauthorize(ctx context.Context, actor Actor, capability domain.Capability) error {
	if s.authorizer == nil {
		return domain.ErrUnavailable
	}
	return s.authorizer.ReauthorizeAction(ctx, domain.MutationAuthorization{
		SessionID:  actor.SessionID,
		UserID:     actor.UserID,
		Capability: capability,
	})
}

// refuse records one denial and returns the empty answer.
//
// Every guard in this file ends the same way, and a refusal must never leave a
// success in the trail — resultFor classifies the error, so a revocation is
// recorded as denied and never as a completed operation.
func (s *IntegrationService) refuse(
	ctx context.Context,
	actor Actor,
	id domain.IntegrationID,
	action string,
	err error,
) (domain.DiagnosticReport, error) {
	s.recordDiagnostic(ctx, actor, id, action, domain.DiagnosticReport{}, err)
	return domain.DiagnosticReport{}, err
}

func (s *IntegrationService) admitTestEmail(actor Actor) (domain.IntegrationDescriptor, string, error) {
	descriptor, ok := domain.LookupIntegration(domain.IntegrationSMTP)
	if !ok {
		return domain.IntegrationDescriptor{}, "", domain.ErrNotFound
	}
	if _, ok := domain.LookupIntegrationAction(descriptor, domain.IntegrationActionSMTPTestEmail); !ok {
		return domain.IntegrationDescriptor{}, "", domain.ErrNotFound
	}
	recipient, err := validateTestRecipient(actor.Email)
	if err != nil {
		return domain.IntegrationDescriptor{}, "", err
	}
	if !allow(s.emailLimiter, actor.UserID+"|test-email") {
		return domain.IntegrationDescriptor{}, "", domain.ErrTooManyRequests
	}
	return descriptor, recipient, nil
}

// run performs one diagnostic under its own budget.
//
// The context is the caller's, so navigating away or closing the tab cancels
// the outbound work. That is the opposite of the health collection, which is
// shared and therefore detached: a diagnostic belongs to one operator and one
// request, and nobody else is waiting on it.
func (s *IntegrationService) run(
	ctx context.Context,
	descriptor domain.IntegrationDescriptor,
	deliver *smtpTestMessage,
) domain.DiagnosticReport {
	runCtx, cancel := context.WithTimeout(ctx, diagnosticBudget)
	defer cancel()

	started := s.now()
	recorder := newStageRecorder(s.now)
	s.dispatch(runCtx, recorder, descriptor, deliver)
	stages := declaredStages(descriptor, deliver)
	recorder.skipRemaining(stages, notExecuted)

	report := domain.DiagnosticReport{
		Integration: descriptor.ID,
		StartedAt:   started,
		Steps:       recorder.ordered(stages),
		Status:      domain.DeriveDiagnosticStatus(recorder.steps),
		Version:     recorder.version,
	}
	report.Summary = diagnosticSummary(report.Status)
	return report
}

// declaredStages is the plan the report must describe.
//
// The delivery stage is appended only when a message is actually being sent, so
// an ordinary diagnostic does not report a delivery it never intended.
func declaredStages(descriptor domain.IntegrationDescriptor, deliver *smtpTestMessage) []domain.DiagnosticStage {
	if deliver == nil {
		return descriptor.Stages
	}
	return append(append([]domain.DiagnosticStage{}, descriptor.Stages...), domain.StageDelivery)
}

// dispatch selects the adapter the registry declared.
//
// A switch on the declared kind and never on anything derived from the target:
// the descriptor decides which protocol is spoken, so a value in the
// environment cannot make an address meant for one client be handed to another.
func (s *IntegrationService) dispatch(
	ctx context.Context,
	recorder *stageRecorder,
	descriptor domain.IntegrationDescriptor,
	deliver *smtpTestMessage,
) {
	switch descriptor.Diagnostic {
	case domain.DiagnosticOIDC:
		s.diagnoseOIDC(ctx, recorder, descriptor)
	case domain.DiagnosticSMTP:
		s.diagnoseSMTP(ctx, recorder, descriptor, deliver)
	case domain.DiagnosticLiveKit:
		s.diagnoseLiveKit(ctx, recorder, descriptor)
	case domain.DiagnosticClamAV:
		s.diagnoseClamAV(ctx, recorder, descriptor)
	case domain.DiagnosticStorage:
		s.diagnoseStorage(ctx, recorder, descriptor)
	case domain.DiagnosticNone:
	}
}

func (s *IntegrationService) diagnoseOIDC(ctx context.Context, recorder *stageRecorder, descriptor domain.IntegrationDescriptor) {
	issuer, ok := s.env("OIDC_ISSUER_URL")
	if !ok {
		recorder.skip(domain.StageResolve, unobservable)
		return
	}
	runOIDCDiagnostic(ctx, recorder, descriptor.Policy, issuer)
}

func (s *IntegrationService) diagnoseStorage(ctx context.Context, recorder *stageRecorder, descriptor domain.IntegrationDescriptor) {
	endpoint, ok := s.env("SEAWEEDFS_FILER_URL")
	if !ok {
		recorder.skip(domain.StageResolve, unobservable)
		return
	}
	runStorageDiagnostic(ctx, recorder, descriptor.Policy, endpoint)
}

func (s *IntegrationService) diagnoseClamAV(ctx context.Context, recorder *stageRecorder, descriptor domain.IntegrationDescriptor) {
	address, ok := s.env("FILE_MALWARE_SCANNER_ADDRESS")
	if !ok {
		recorder.skip(domain.StageResolve, unobservable)
		return
	}
	runClamAVDiagnostic(ctx, recorder, descriptor.Policy, address)
}

func (s *IntegrationService) diagnoseLiveKit(ctx context.Context, recorder *stageRecorder, descriptor domain.IntegrationDescriptor) {
	endpoint, ok := s.env("LIVEKIT_API_URL")
	if !ok {
		recorder.skip(domain.StageResolve, unobservable)
		return
	}
	apiKey, _ := s.env("LIVEKIT_API_KEY")
	apiSecret, _ := s.env("LIVEKIT_API_SECRET")
	runLiveKitDiagnostic(ctx, recorder, descriptor.Policy, liveKitCredentials{
		endpoint: endpoint, apiKey: apiKey, apiSecret: apiSecret,
	}, s.now())
}

func (s *IntegrationService) diagnoseSMTP(
	ctx context.Context,
	recorder *stageRecorder,
	descriptor domain.IntegrationDescriptor,
	deliver *smtpTestMessage,
) {
	settings, ok := s.smtpSettings()
	if !ok {
		recorder.skip(domain.StageResolve, unobservable)
		return
	}
	runSMTPDiagnostic(ctx, recorder, descriptor.Policy, settings, deliver)
}

// smtpSettings reads the relay configuration this pod observes.
//
// The host and the sender are required: without either there is no relay to
// contact and no envelope to write, and inventing a default would produce a
// diagnostic of something the platform is not configured to do.
func (s *IntegrationService) smtpSettings() (smtpSettings, bool) {
	host, hasHost := s.env("SMTP_HOST")
	from, hasFrom := s.env("SMTP_FROM")
	if !hasHost || !hasFrom {
		return smtpSettings{}, false
	}
	port, hasPort := s.env("SMTP_PORT")
	if !hasPort {
		port = "587"
	}
	tlsMode, hasMode := s.env("SMTP_TLS_MODE")
	if !hasMode {
		tlsMode = smtpTLSStartTLS
	}
	username, _ := s.env("SMTP_USERNAME")
	password, _ := s.env("SMTP_PASSWORD")
	fromName, hasName := s.env("SMTP_FROM_NAME")
	if !hasName {
		fromName = "NChat"
	}
	return smtpSettings{
		host: host, port: port, tlsMode: strings.ToLower(tlsMode),
		username: username, password: password, from: from, fromName: fromName,
	}, true
}

// env reads one variable, treating blank as absent.
//
// A variable set to the empty string is a deployment that did not configure the
// value, and treating it as present would produce a target of "" that fails
// somewhere less legible than here.
func (s *IntegrationService) env(name string) (string, bool) {
	raw, present := s.lookupEnv(name)
	trimmed := strings.TrimSpace(raw)
	return trimmed, present && trimmed != ""
}

const unobservable = "Este serviço não recebe a configuração que nomeia o endpoint desta integração, " +
	"então nenhuma verificação foi executada."

var diagnosticSummaries = map[domain.DiagnosticStatus]string{
	domain.DiagnosticPassed:  "Todas as etapas verificadas concluíram com sucesso.",
	domain.DiagnosticWarning: "A integração respondeu, mas ao menos uma etapa merece atenção.",
	domain.DiagnosticFailed:  "Ao menos uma etapa falhou. As etapas seguintes não foram executadas.",
	domain.DiagnosticSkipped: "Nenhuma etapa pôde ser executada com a configuração que este serviço observa.",
}

func diagnosticSummary(status domain.DiagnosticStatus) string {
	return diagnosticSummaries[status]
}

// recordDiagnostic writes one audit row.
//
// The metadata is an allowlist of three server-derived values: which
// integration, what the run concluded, and — when it failed — which stage
// failed first. No target, no response, no credential and no recipient reaches
// the trail, and the recipient in particular is redundant: the actor column
// already identifies whose mailbox it was.
func (s *IntegrationService) recordDiagnostic(
	ctx context.Context,
	actor Actor,
	id domain.IntegrationID,
	action string,
	report domain.DiagnosticReport,
	err error,
) {
	metadata := map[string]string{"integration": string(id)}
	if err == nil {
		metadata["outcome"] = string(report.Status)
		if stage, failed := firstFailedStage(report); failed {
			metadata["failed_stage"] = string(stage)
		}
	}
	record(ctx, s.audit, actor, action, domain.AuditIntegrationResource(id), resultFor(err), metadata)
}

func firstFailedStage(report domain.DiagnosticReport) (domain.DiagnosticStage, bool) {
	for _, step := range report.Steps {
		if step.Status == domain.DiagnosticFailed {
			return step.Stage, true
		}
	}
	return "", false
}
