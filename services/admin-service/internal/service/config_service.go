package service

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
)

// Configuration & Secrets Management (issue #580).
//
// This service is the single place the whole edit pipeline lives, in the order
// the design requires it: resolve the key against the registry, refuse an
// unknown or read-only one, parse the value as the declared type, validate it,
// decide the capability the *resulting* value demands, check the revision the
// form was loaded at, compute the diff, write under a compare-and-swap, and
// audit. Preview runs the same pipeline and stops before the write, so what an
// operator confirms is what the server would do — not a second implementation
// that can disagree with it.
//
// What it deliberately cannot do: change anything outside the database. No
// Kubernetes object, no file, no other service's environment, no outbound
// request. domain.ValidateConfigCatalog enforces that at the registry level,
// which is also why there is no URL validator here and therefore no SSRF
// surface to police.

const (
	defaultConfigVersionLimit = 25
	maxConfigVersionLimit     = 100
)

// ConfigStore is the persistence the configuration surface needs.
type ConfigStore interface {
	ReadDocument(ctx context.Context, document domain.ConfigDocument) (domain.ConfigDocumentState, error)
	ApplyDocument(ctx context.Context, document domain.ConfigDocument, input domain.ConfigApplyInput) (domain.ConfigApplyOutcome, error)
	ListConfigVersions(ctx context.Context, document domain.ConfigDocument, limit int) ([]domain.ConfigVersion, error)
	GetConfigVersion(ctx context.Context, document domain.ConfigDocument, id int64) (domain.ConfigVersion, error)
}

// ConfigSetting is one registry definition together with what the platform
// currently reports for it.
//
// The value and the credential status are separate fields on purpose, and a
// sensitive definition only ever populates the second: the raw value of a
// credential is never assigned to anything that leaves this package.
type ConfigSetting struct {
	Definition domain.ConfigDefinition
	// Value is the effective value. Meaningless unless Observable, and never
	// populated for a sensitive definition.
	Value domain.ConfigValue
	// Observable reports whether this pod can see the value at all. False for
	// a Secret scoped to another workload, which is a different fact from "not
	// configured" and must not be reported as one.
	Observable bool
	// Configured reports whether a sensitive definition has a non-empty value.
	// It is the only thing this API ever says about a credential.
	Configured bool
}

// ConfigCatalogView is the whole configuration surface as one read.
type ConfigCatalogView struct {
	Documents []domain.ConfigDocumentState
	Settings  []ConfigSetting
}

// ConfigValidationError is one field the server refused, named so the console
// can put the message next to the input that produced it.
type ConfigValidationError struct {
	Key     domain.ConfigKey
	Message string
}

// ConfigPlan is what a change would do, computed server-side.
//
// It is what preview returns and what apply acts on, so the confirmation an
// operator reads and the decision the server makes come from the same
// computation.
type ConfigPlan struct {
	Document domain.ConfigDocument
	// Revision is the stored revision right now.
	Revision int
	// Stale reports that the document moved since the form was loaded. A stale
	// plan is never applied.
	Stale bool
	// Superseded reports that a precondition no longer holds — for a rollback,
	// that the version being reverted is no longer the one in force. It is a
	// different fact from Stale: the console may have loaded *after* the change
	// that superseded the version, so the revision would agree while the
	// rollback would still discard somebody's work.
	Superseded bool
	Changes    []domain.ConfigChange
	Dangerous  bool
	// RequiredCapability is the capability this particular change demands,
	// which is admin.superuser when any resulting value is dangerous.
	RequiredCapability domain.Capability
	Authorized         bool
	ReasonRequired     bool
	Warnings           []string
	Errors             []ConfigValidationError
	AffectedServices   []string
	Apply              domain.ConfigApply
}

// ConfigApplyResult is the outcome of a write.
//
// Applied is false with no error for the idempotent case: the requested values
// are already the stored values, so there is nothing to write, no version to
// record and no audit event to raise. A double-submitted form lands here
// instead of creating a second version that changed nothing.
type ConfigApplyResult struct {
	Applied bool
	State   domain.ConfigDocumentState
	Version domain.ConfigVersion
	Plan    ConfigPlan
}

// ConfigChangeRequest is a validated-by-shape change request from the HTTP
// layer.
//
// Values arrive as raw JSON rather than decoded, so the registry decides how
// each one is read. A caller cannot pre-decide that a field is a string and
// have that stick.
type ConfigChangeRequest struct {
	Document         domain.ConfigDocument
	ExpectedRevision int
	Reason           string
	Values           map[domain.ConfigKey]json.RawMessage
	// Preconditions are the values the document must still hold for this change
	// to mean what the caller intends. Empty for an ordinary edit, which asserts
	// only the revision; a rollback names every field of the version it reverts.
	Preconditions []domain.ConfigPrecondition
}

// ConfigService is the configuration surface.
type ConfigService struct {
	store ConfigStore
	audit Recorder
	// lookupEnv observes the settings this service does not own. It is
	// injected so a test can describe a deployment instead of mutating the
	// process, and it is read-only by construction: there is no counterpart
	// that writes an environment variable.
	lookupEnv func(string) (string, bool)
}

func NewConfigService(store ConfigStore, audit Recorder) *ConfigService {
	return &ConfigService{store: store, audit: audit, lookupEnv: os.LookupEnv}
}

// NewConfigServiceWithEnv builds the service against a described environment.
func NewConfigServiceWithEnv(store ConfigStore, audit Recorder, lookupEnv func(string) (string, bool)) *ConfigService {
	service := NewConfigService(store, audit)
	if lookupEnv != nil {
		service.lookupEnv = lookupEnv
	}
	return service
}

// Catalog returns every declared setting with its effective state.
//
// Three kinds of answer, and the difference between them is the point:
//
//   - a database-backed setting reports its stored value, which is also the
//     value auth-service enforces on the next request;
//   - a deployment setting reports what this pod observes in its own
//     environment, which is the ConfigMap Git deployed;
//   - a credential reports only whether it is configured. The value is read
//     into a local, tested for emptiness and discarded — it is never assigned
//     to a field, never logged and never marshalled.
func (s *ConfigService) Catalog(ctx context.Context) (ConfigCatalogView, error) {
	if s == nil || s.store == nil {
		return ConfigCatalogView{}, domain.ErrUnavailable
	}
	state, err := s.store.ReadDocument(ctx, domain.ConfigDocumentAuthPolicy)
	if err != nil {
		return ConfigCatalogView{}, err
	}
	settings := make([]ConfigSetting, 0, len(domain.ConfigCatalog()))
	for _, definition := range domain.ConfigCatalog() {
		settings = append(settings, s.settingFor(definition, state))
	}
	return ConfigCatalogView{
		Documents: []domain.ConfigDocumentState{state},
		Settings:  settings,
	}, nil
}

func (s *ConfigService) settingFor(definition domain.ConfigDefinition, state domain.ConfigDocumentState) ConfigSetting {
	setting := ConfigSetting{Definition: definition}
	if definition.Sensitive {
		raw, present := s.lookupEnv(definition.EnvVar)
		setting.Observable = definition.EnvVar != "" && present
		setting.Configured = setting.Observable && strings.TrimSpace(raw) != ""
		return setting
	}
	if definition.Source == domain.ConfigSourceDatabase {
		value, known := state.Values[definition.Key]
		setting.Observable = known && state.Document == definition.Document
		setting.Value = value
		return setting
	}
	raw, present := s.lookupEnv(definition.EnvVar)
	setting.Observable = definition.EnvVar != "" && present
	if setting.Observable {
		setting.Value = domain.TextValue(raw)
	}
	return setting
}

// Preview computes what a change would do, without writing anything.
//
// It requires only the read capability: it reveals nothing an operator holding
// admin.config.read cannot already see, and it is what tells them, before they
// commit to anything, that the change needs a capability they may not hold.
func (s *ConfigService) Preview(ctx context.Context, actor Actor, request ConfigChangeRequest) (ConfigPlan, error) {
	if s == nil || s.store == nil {
		return ConfigPlan{}, domain.ErrUnavailable
	}
	return s.plan(ctx, actor, request)
}

// Apply writes a change set, records a version and audits the outcome.
//
// Persisting is applying for every setting this method can touch: they are
// class A, stored in the database and read by auth-service on the request that
// enforces them. That is why there is no "applying" state and no rollout to
// follow — and why nothing here reports a change as live that is not. A
// setting that needed a rollout would not be editable at all.
func (s *ConfigService) Apply(ctx context.Context, actor Actor, request ConfigChangeRequest) (ConfigApplyResult, error) {
	if s == nil || s.store == nil {
		return ConfigApplyResult{}, domain.ErrUnavailable
	}
	result, err := s.apply(ctx, actor, request, 0)
	s.recordConfig(ctx, actor, domain.AuditActionConfigUpdate, request.Document, result, err)
	return result, err
}

// Rollback restores the values one recorded version replaced.
//
// It is a forward change, not an erasure: the previous values are validated
// against today's registry, written through the same compare-and-swap as any
// other change, and recorded as a new version that names the one it reverts.
// The history therefore never shrinks, and an apply/rollback loop leaves a
// trail of every step instead of hiding one.
//
// Rollback exists only where restoring the value is the whole operation. Every
// eligible setting is a scalar in this database with no external state to
// match, no restart to schedule and no credential to reconstruct;
// domain.ReverseConfigChanges refuses anything else, including a value today's
// registry would no longer accept.
func (s *ConfigService) Rollback(ctx context.Context, actor Actor, versionID int64, expectedRevision int, reason string) (ConfigApplyResult, error) {
	if s == nil || s.store == nil {
		return ConfigApplyResult{}, domain.ErrUnavailable
	}
	result, err := s.rollback(ctx, actor, versionID, expectedRevision, reason)
	s.recordConfig(ctx, actor, domain.AuditActionConfigRollback, domain.ConfigDocumentAuthPolicy, result, err)
	return result, err
}

func (s *ConfigService) rollback(ctx context.Context, actor Actor, versionID int64, expectedRevision int, reason string) (ConfigApplyResult, error) {
	request, reverts, err := s.rollbackRequest(ctx, versionID, expectedRevision, reason)
	if err != nil {
		return ConfigApplyResult{}, err
	}
	return s.apply(ctx, actor, request, reverts)
}

// PreviewRollback computes what reverting one version would do, without
// writing anything.
//
// It exists as its own operation because a rollback is not an edit that happens
// to carry old values: which values it restores, and whether it may be
// performed at all, are facts about the recorded version and the current state
// — both of which live here. A console that rebuilt the change set from the
// history it renders would be deriving an administrative mutation from
// presentation data, and would show a diff the apply then refuses.
//
// The plan it returns is computed by the same pipeline the confirmed rollback
// runs, from the same derivation, so what an operator reviews is what the
// server would attempt.
//
// Requires only the read capability: it reveals nothing beyond the version and
// the state the caller can already read, and it writes nothing.
func (s *ConfigService) PreviewRollback(ctx context.Context, actor Actor, versionID int64, expectedRevision int, reason string) (ConfigPlan, error) {
	if s == nil || s.store == nil {
		return ConfigPlan{}, domain.ErrUnavailable
	}
	request, _, err := s.rollbackRequest(ctx, versionID, expectedRevision, reason)
	if err != nil {
		return ConfigPlan{}, err
	}
	return s.plan(ctx, actor, request)
}

// rollbackRequest derives, from a recorded version, the change this platform
// would perform to undo it.
//
// The single derivation behind both the preview and the confirmed rollback.
// Splitting it would let the two disagree about what a rollback *is*, which is
// exactly how a console ends up showing a diff the server refuses.
//
// Everything it produces is server-side: the values to restore come from the
// stored version, and so do the preconditions — every field that version
// changed must still hold the value it set, or the version is no longer the one
// in force and undoing it would discard somebody else's change.
func (s *ConfigService) rollbackRequest(ctx context.Context, versionID int64, expectedRevision int, reason string) (ConfigChangeRequest, int, error) {
	if versionID <= 0 {
		return ConfigChangeRequest{}, 0, domain.ErrInvalidInput
	}
	version, err := s.store.GetConfigVersion(ctx, domain.ConfigDocumentAuthPolicy, versionID)
	if err != nil {
		return ConfigChangeRequest{}, 0, err
	}
	reversed, err := domain.ReverseConfigChanges(version)
	if err != nil {
		return ConfigChangeRequest{}, 0, err
	}
	values := make(map[domain.ConfigKey]json.RawMessage, len(reversed))
	for _, change := range reversed {
		encoded, err := json.Marshal(change.To)
		if err != nil {
			return ConfigChangeRequest{}, 0, domain.ErrInvalidInput
		}
		values[change.Key] = encoded
	}
	return ConfigChangeRequest{
		Document:         version.Document,
		ExpectedRevision: expectedRevision,
		Reason:           reason,
		Values:           values,
		Preconditions:    domain.ConfigRollbackPreconditions(version),
	}, version.Revision, nil
}

func (s *ConfigService) apply(ctx context.Context, actor Actor, request ConfigChangeRequest, reverts int) (ConfigApplyResult, error) {
	plan, err := s.plan(ctx, actor, request)
	if err != nil {
		return ConfigApplyResult{}, err
	}
	if err := refuseUnapplicable(plan, request.Reason); err != nil {
		return ConfigApplyResult{Plan: plan}, err
	}
	if len(plan.Changes) == 0 {
		// The desired state is already the stored state. Nothing is written and
		// no version is recorded: a resubmitted form must not produce a second
		// version that changed nothing.
		state, err := s.store.ReadDocument(ctx, request.Document)
		if err != nil {
			return ConfigApplyResult{}, err
		}
		return ConfigApplyResult{Applied: false, State: state, Plan: plan}, nil
	}
	return s.write(ctx, actor, request, plan, reverts)
}

// refuseUnapplicable answers whether a computed plan may be written at all.
//
// The refusals, each a different fact about the same plan and each mapped to
// the status a client is entitled to tell apart: a value the registry rejects,
// a document that moved or a version that has been superseded, a capability the
// actor does not hold, and a dangerous change with nothing said about why.
// Keeping them together is what makes the order reviewable — validity first,
// then concurrency, then authority.
//
// This is not the concurrency control. The store asserts the revision and the
// preconditions again inside the write, atomically; refusing here only means an
// operator is told what happened instead of watching a request fail later.
func refuseUnapplicable(plan ConfigPlan, reason string) error {
	switch {
	case len(plan.Errors) > 0:
		return domain.ErrInvalidInput
	case plan.Stale, plan.Superseded:
		return domain.ErrConflict
	case !plan.Authorized:
		return domain.ErrForbidden
	case plan.ReasonRequired && strings.TrimSpace(reason) == "":
		return domain.ErrInvalidInput
	}
	return nil
}

// write performs the change and reports what was committed.
//
// The authority it carries is identity plus the capability the plan demands;
// whether that capability is still held is decided by the store, inside the
// transaction, against the database. Nothing here is the final word on
// authorization — see storage/mutation_authorization.go.
//
// There is no read-back. The store's statement returns the row it wrote, so the
// committed revision and values come out of the same transaction that produced
// them. That is what keeps a commit from being reported as a failure: once the
// transaction has committed, nothing that happens afterwards may turn Applied
// into false or lose the version id, because a client told the mutation failed
// will send it again.
func (s *ConfigService) write(ctx context.Context, actor Actor, request ConfigChangeRequest, plan ConfigPlan, reverts int) (ConfigApplyResult, error) {
	reason, err := domain.NormalizeConfigReason(request.Reason)
	if err != nil {
		return ConfigApplyResult{Plan: plan}, err
	}
	outcome, err := s.store.ApplyDocument(ctx, request.Document, domain.ConfigApplyInput{
		ExpectedRevision: plan.Revision,
		Changes:          plan.Changes,
		ActorUserID:      actor.UserID,
		CorrelationID:    actor.CorrelationID,
		Reason:           reason,
		RevertsRevision:  reverts,
		Preconditions:    request.Preconditions,
		// The capability the *plan* demands, not the route's: a value that
		// weakens the platform requires admin.superuser, and that is the one
		// the write must still hold when it commits. plan.Authorized answered
		// the same question from the middleware's snapshot; this asks the
		// database again, in the transaction that writes.
		Authorization: domain.MutationAuthorization{
			SessionID:  actor.SessionID,
			UserID:     actor.UserID,
			Capability: plan.RequiredCapability,
		},
	})
	if err != nil {
		return ConfigApplyResult{Plan: plan}, err
	}
	return ConfigApplyResult{
		Applied: true,
		State:   outcome.State,
		Version: outcome.Version,
		Plan:    plan,
	}, nil
}

// plan is the pipeline both preview and apply run.
func (s *ConfigService) plan(ctx context.Context, actor Actor, request ConfigChangeRequest) (ConfigPlan, error) {
	document, err := resolveDocument(request)
	if err != nil {
		return ConfigPlan{}, err
	}

	desired := resolveDesired(request.Values)
	plan := ConfigPlan{
		Document:           document,
		Apply:              domain.ConfigApplyRuntime,
		Errors:             desired.failures,
		Warnings:           desired.warnings,
		Dangerous:          desired.dangerous,
		AffectedServices:   sortedSet(desired.services),
		RequiredCapability: domain.CapabilityConfigManage,
		ReasonRequired:     desired.dangerous,
	}
	if desired.dangerous {
		plan.RequiredCapability = domain.CapabilitySuperuser
	}
	plan.Authorized = actor.Capabilities.Has(plan.RequiredCapability)

	state, err := s.store.ReadDocument(ctx, document)
	if err != nil {
		return ConfigPlan{}, err
	}
	plan.Revision = state.Revision
	plan.Stale = request.ExpectedRevision != state.Revision
	plan.Superseded = !domain.ConfigPreconditionsHold(request.Preconditions, state.Values)
	if len(plan.Errors) == 0 {
		plan.Changes = domain.DiffConfig(changeBaseline(request, state), desired.values)
	}
	return plan, nil
}

// changeBaseline is the state the plan's diff is described against.
//
// An ordinary edit is described against what the document holds — that is what
// the operator is changing. A rollback is described against the version it
// undoes, which is what its preconditions name: asked to undo the version that
// set 20, the plan must say "20 -> 10" and mark itself superseded, not offer
// "30 -> 10" as though that were the operation on the table.
//
// While a version is still in force the two baselines are identical, so this
// only changes what a superseded rollback *says*, never what any change does.
func changeBaseline(request ConfigChangeRequest, state domain.ConfigDocumentState) map[domain.ConfigKey]domain.ConfigValue {
	if len(request.Preconditions) == 0 {
		return state.Values
	}
	return domain.ConfigPreconditionBaseline(request.Preconditions)
}

// resolveDocument checks the envelope of a change request and answers which
// document it addresses.
//
// Everything here is a precondition of the request itself rather than of the
// values it carries: an unknown document, an empty change set, a missing
// revision, an oversized reason, or a set of keys that does not belong to the
// document the caller named.
func resolveDocument(request ConfigChangeRequest) (domain.ConfigDocument, error) {
	if !domain.ValidConfigDocument(request.Document) {
		return "", domain.ErrInvalidInput
	}
	if len(request.Values) == 0 {
		return "", domain.ErrInvalidInput
	}
	if request.ExpectedRevision <= 0 {
		return "", domain.ErrInvalidInput
	}
	if _, err := domain.NormalizeConfigReason(request.Reason); err != nil {
		return "", err
	}
	document, err := domain.ConfigDocumentOf(sortedKeys(request.Values))
	if err != nil {
		return "", err
	}
	if document != request.Document {
		return "", domain.ErrInvalidInput
	}
	return document, nil
}

// desiredState is the requested values after the registry has read them: what
// was accepted, what was refused, and what the accepted set implies.
type desiredState struct {
	values    map[domain.ConfigKey]domain.ConfigValue
	failures  []ConfigValidationError
	warnings  []string
	services  map[string]struct{}
	dangerous bool
}

// resolveDesired parses and validates every requested value against its
// definition.
//
// It collects failures rather than stopping at the first one: an operator
// filling a form is owed every message at once, and a partially reported form
// is one they have to submit repeatedly to discover.
func resolveDesired(raw map[domain.ConfigKey]json.RawMessage) desiredState {
	desired := desiredState{
		values:   make(map[domain.ConfigKey]domain.ConfigValue, len(raw)),
		services: make(map[string]struct{}, 4),
	}
	for _, key := range sortedKeys(raw) {
		definition, _ := domain.LookupConfig(key)
		value, err := domain.ParseConfigValue(definition, raw[key])
		if err == nil {
			err = definition.Validate(value)
		}
		if err != nil {
			desired.failures = append(desired.failures, ConfigValidationError{Key: key, Message: validationMessage(err)})
			continue
		}
		desired.accept(definition, value)
	}
	return desired
}

// accept records one value the registry admitted, and what admitting it means.
func (d *desiredState) accept(definition domain.ConfigDefinition, value domain.ConfigValue) {
	d.values[definition.Key] = value
	d.services[definition.OwnerService] = struct{}{}
	if definition.DangerousValue(value) {
		d.dangerous = true
		d.warnings = append(d.warnings, definition.DangerNote)
	}
}

// Versions returns the recorded history of one document.
func (s *ConfigService) Versions(ctx context.Context, document domain.ConfigDocument, limit int) ([]domain.ConfigVersion, error) {
	if s == nil || s.store == nil {
		return nil, domain.ErrUnavailable
	}
	if !domain.ValidConfigDocument(document) {
		return nil, domain.ErrInvalidInput
	}
	return s.store.ListConfigVersions(ctx, document, ClampConfigVersionLimit(limit))
}

// ClampConfigVersionLimit normalizes a requested page size, on the same terms
// as the audit trail: unspecified gets the default, and anything above the
// ceiling is capped rather than refused so the parameter cannot be turned into
// a request for the whole table.
func ClampConfigVersionLimit(limit int) int {
	switch {
	case limit <= 0:
		return defaultConfigVersionLimit
	case limit > maxConfigVersionLimit:
		return maxConfigVersionLimit
	default:
		return limit
	}
}

// recordConfig writes the audit row for one configuration mutation.
//
// The metadata is an allowlist built field by field from values this service
// derived: the document, the revisions, the version id, the keys that changed
// and their before/after. Every one of those settings is non-sensitive by
// construction — the registry refuses an editable sensitive definition — so no
// credential can reach this map. The operator's stated reason is deliberately
// *not* copied here: it is client-supplied prose, it is already persisted on
// the version row, and the version id in this metadata is how the two are
// joined.
func (s *ConfigService) recordConfig(ctx context.Context, actor Actor, action string, document domain.ConfigDocument, result ConfigApplyResult, err error) {
	metadata := map[string]string{
		"document":         string(document),
		"applied":          strconv.FormatBool(result.Applied),
		"dangerous":        strconv.FormatBool(result.Plan.Dangerous),
		"capability":       string(result.Plan.RequiredCapability),
		"revision_from":    strconv.Itoa(result.Plan.Revision),
		"change_count":     strconv.Itoa(len(result.Plan.Changes)),
		"reason_given":     strconv.FormatBool(strings.TrimSpace(result.Version.Reason) != ""),
		"validation":       validationSummary(result.Plan),
		"reverts_revision": strconv.Itoa(result.Version.RevertsRevision),
	}
	if result.Applied {
		metadata["revision_to"] = strconv.Itoa(result.Version.Revision)
		metadata["version_id"] = strconv.FormatInt(result.Version.ID, 10)
	}
	changed := make([]string, 0, len(result.Plan.Changes))
	for _, change := range result.Plan.Changes {
		changed = append(changed, string(change.Key))
		metadata["change:"+string(change.Key)] = change.From.AuditString() + " -> " + change.To.AuditString()
	}
	metadata["changed_keys"] = strings.Join(changed, ",")
	record(ctx, s.audit, actor, action, domain.AuditConfigResource(document), resultFor(err), metadata)
}

func validationSummary(plan ConfigPlan) string {
	if len(plan.Errors) == 0 {
		return "passed"
	}
	keys := make([]string, 0, len(plan.Errors))
	for _, failure := range plan.Errors {
		keys = append(keys, string(failure.Key))
	}
	return "failed:" + strings.Join(keys, ",")
}

// validationMessage strips the wrapped sentinel so the console shows the rule
// that was broken and not the error chain that carried it.
func validationMessage(err error) string {
	message := err.Error()
	if _, after, found := strings.Cut(message, "invalid input: "); found {
		return after
	}
	return message
}

func sortedKeys(values map[domain.ConfigKey]json.RawMessage) []domain.ConfigKey {
	keys := make([]domain.ConfigKey, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}

func sortedSet(values map[string]struct{}) []string {
	items := make([]string, 0, len(values))
	for value := range values {
		items = append(items, value)
	}
	sort.Strings(items)
	return items
}
