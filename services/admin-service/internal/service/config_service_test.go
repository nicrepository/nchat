package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
	"github.com/nicrepository/nchat/services/admin-service/internal/service"
)

// configStore models the real store's one meaningful behaviour: the write is a
// compare-and-swap on the revision. Everything a spec below asserts about
// concurrency depends on that being modelled and not stubbed away.
type configStore struct {
	revision int
	values   map[domain.ConfigKey]domain.ConfigValue
	versions map[int64]domain.ConfigVersion
	nextID   int64
	applies  int
	readErr  error
	applyErr error
	// failReadsAfterApply models a database that becomes unreachable the moment
	// a change commits: the write succeeded, every read after it does not.
	failReadsAfterApply bool
}

func newConfigStore() *configStore {
	values := make(map[domain.ConfigKey]domain.ConfigValue)
	for _, definition := range domain.EditableConfigDefinitions(domain.ConfigDocumentAuthPolicy) {
		values[definition.Key] = definition.Default
	}
	return &configStore{revision: 1, values: values, versions: map[int64]domain.ConfigVersion{}, nextID: 1}
}

func (s *configStore) ReadDocument(_ context.Context, document domain.ConfigDocument) (domain.ConfigDocumentState, error) {
	if s.readErr != nil {
		return domain.ConfigDocumentState{}, s.readErr
	}
	if s.failReadsAfterApply && s.applies > 0 {
		return domain.ConfigDocumentState{}, errors.New("database unreachable")
	}
	values := make(map[domain.ConfigKey]domain.ConfigValue, len(s.values))
	for key, value := range s.values {
		values[key] = value
	}
	return domain.ConfigDocumentState{Document: document, Revision: s.revision, Values: values}, nil
}

// ApplyDocument models the real store's compare-and-swap: the revision and
// every precondition are checked against the state at the moment of the write,
// and the committed state is returned by the same call rather than read back.
func (s *configStore) ApplyDocument(_ context.Context, document domain.ConfigDocument, input domain.ConfigApplyInput) (domain.ConfigApplyOutcome, error) {
	s.applies++
	if s.applyErr != nil {
		return domain.ConfigApplyOutcome{}, s.applyErr
	}
	if input.ExpectedRevision != s.revision {
		return domain.ConfigApplyOutcome{}, domain.ErrConflict
	}
	if !domain.ConfigPreconditionsHold(input.Preconditions, s.values) {
		return domain.ConfigApplyOutcome{}, domain.ErrConflict
	}
	for _, change := range input.Changes {
		s.values[change.Key] = change.To
	}
	s.revision++
	version := domain.ConfigVersion{
		ID: s.nextID, Document: document, Revision: s.revision, AppliedAt: time.Now().UTC(),
		ActorUserID: input.ActorUserID, CorrelationID: input.CorrelationID, Reason: input.Reason,
		RevertsRevision: input.RevertsRevision, Changes: input.Changes,
	}
	s.versions[s.nextID] = version
	s.nextID++
	// Built here rather than through ReadDocument, exactly as the real store
	// builds it from the statement that performed the write: a committed change
	// must not depend on a later read succeeding.
	return domain.ConfigApplyOutcome{Version: version, State: s.snapshot(document)}, nil
}

func (s *configStore) snapshot(document domain.ConfigDocument) domain.ConfigDocumentState {
	values := make(map[domain.ConfigKey]domain.ConfigValue, len(s.values))
	for key, value := range s.values {
		values[key] = value
	}
	return domain.ConfigDocumentState{Document: document, Revision: s.revision, Values: values}
}

func (s *configStore) ListConfigVersions(_ context.Context, _ domain.ConfigDocument, limit int) ([]domain.ConfigVersion, error) {
	versions := make([]domain.ConfigVersion, 0, len(s.versions))
	for id := s.nextID - 1; id >= 1 && len(versions) < limit; id-- {
		if version, ok := s.versions[id]; ok {
			versions = append(versions, version)
		}
	}
	return versions, nil
}

func (s *configStore) GetConfigVersion(_ context.Context, _ domain.ConfigDocument, id int64) (domain.ConfigVersion, error) {
	version, ok := s.versions[id]
	if !ok {
		return domain.ConfigVersion{}, domain.ErrNotFound
	}
	return version, nil
}

func configActor(capabilities ...domain.Capability) service.Actor {
	return service.Actor{
		UserID:        actorID,
		CorrelationID: "req-config",
		Capabilities:  domain.NewCapabilitySet(capabilities),
	}
}

func changeRequest(revision int, values map[domain.ConfigKey]string) service.ConfigChangeRequest {
	raw := make(map[domain.ConfigKey]json.RawMessage, len(values))
	for key, value := range values {
		raw[key] = json.RawMessage(value)
	}
	return service.ConfigChangeRequest{
		Document:         domain.ConfigDocumentAuthPolicy,
		ExpectedRevision: revision,
		Values:           raw,
	}
}

func newConfigService(store *configStore, audit service.Recorder, environment map[string]string) *service.ConfigService {
	return service.NewConfigServiceWithEnv(store, audit, func(key string) (string, bool) {
		value, ok := environment[key]
		return value, ok
	})
}

// A credential is reported as configured or not, and never as a value. This is
// the read-path invariant asserted over the whole catalog rather than one
// field: no setting the service returns may carry a credential.
func TestConfigService_CatalogNeverCarriesACredential(t *testing.T) {
	const canary = "leak-canary-must-never-be-serialized"
	store := newConfigStore()
	configuration := newConfigService(store, &recordingAudit{}, map[string]string{
		"SMTP_PASSWORD":        canary,
		"AUTH_JWT_HMAC_SECRET": "test-jwt-secret-for-admin-config-tests",
		"OIDC_CLIENT_SECRET":   "",
		"APP_ENV":              "staging",
		"LIVEKIT_ENABLED":      "false",
	})

	view, err := configuration.Catalog(context.Background())
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}

	// The whole catalog, not one field: every setting the service returns is
	// checked, so a future definition that starts carrying its value cannot
	// slip past a spec that only looked at SMTP.
	for _, setting := range view.Settings {
		if strings.Contains(setting.Value.Text, canary) || strings.Contains(setting.Value.AuditString(), canary) {
			t.Fatalf("%s carries a credential", setting.Definition.Key)
		}
		if setting.Definition.Sensitive && setting.Value != (domain.ConfigValue{}) {
			t.Fatalf("%s carries a value: %+v", setting.Definition.Key, setting.Value)
		}
	}
	smtp := settingFor(t, view, "secret.smtp_password")
	if !smtp.Configured {
		t.Fatal("expected a non-empty credential to be reported as configured")
	}
	clientSecret := settingFor(t, view, "secret.oidc_client_secret")
	if clientSecret.Configured {
		t.Fatal("an empty credential is not configured")
	}
}

// "Not configured" and "this pod cannot see it" are different facts. Reporting
// the second as the first would send an operator to fix something that is fine.
func TestConfigService_CatalogSeparatesUnobservableFromUnconfigured(t *testing.T) {
	view, err := newConfigService(newConfigStore(), &recordingAudit{}, map[string]string{}).
		Catalog(context.Background())
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}

	scoped := settingFor(t, view, "secret.file_encryption_master_key")
	if scoped.Observable || scoped.Configured {
		t.Fatal("a Secret scoped to another workload must be reported as unobservable")
	}
	deployment := settingFor(t, view, "platform.environment")
	if deployment.Observable {
		t.Fatal("a variable this pod does not receive must not report a value")
	}
}

func TestConfigService_CatalogReportsStoredValuesForEditableSettings(t *testing.T) {
	store := newConfigStore()
	store.values[domain.ConfigKeyPasswordMinLength] = domain.IntValue(16)

	view, err := newConfigService(store, &recordingAudit{}, nil).Catalog(context.Background())
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}

	setting := settingFor(t, view, string(domain.ConfigKeyPasswordMinLength))
	if !setting.Observable || setting.Value.Int != 16 {
		t.Fatalf("expected the stored value, got %+v", setting)
	}
	if view.Documents[0].Revision != 1 {
		t.Fatalf("expected the revision to travel with the values, got %d", view.Documents[0].Revision)
	}
}

func TestConfigService_ApplyWritesAndBumpsTheRevision(t *testing.T) {
	store := newConfigStore()
	configuration := newConfigService(store, &recordingAudit{}, nil)

	result, err := configuration.Apply(context.Background(), configActor(domain.CapabilityConfigManage),
		changeRequest(1, map[domain.ConfigKey]string{domain.ConfigKeyPasswordMinLength: "16"}))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if !result.Applied {
		t.Fatalf("expected the change to be applied, got %+v", result)
	}
	if result.State.Revision != 2 {
		t.Fatalf("expected revision 2, got %d", result.State.Revision)
	}
	if store.values[domain.ConfigKeyPasswordMinLength].Int != 16 {
		t.Fatal("expected the value to be stored")
	}
}

// The trail is what an operator reads months later, so it is asserted field by
// field rather than as "an event happened".
func TestConfigService_ApplyRecordsWhatChangedInTheTrail(t *testing.T) {
	audit := &recordingAudit{}

	if _, err := newConfigService(newConfigStore(), audit, nil).
		Apply(context.Background(), configActor(domain.CapabilityConfigManage),
			changeRequest(1, map[domain.ConfigKey]string{domain.ConfigKeyPasswordMinLength: "16"})); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	event := audit.last(t)
	if event.Action != domain.AuditActionConfigUpdate {
		t.Fatalf("unexpected action: %q", event.Action)
	}
	if event.Result != domain.AuditResultSuccess {
		t.Fatalf("unexpected result: %q", event.Result)
	}
	if event.Resource != domain.AuditConfigResource(domain.ConfigDocumentAuthPolicy) {
		t.Fatalf("unexpected resource: %q", event.Resource)
	}
	if event.CorrelationID != "req-config" {
		t.Fatalf("expected the request id to be the correlation id, got %q", event.CorrelationID)
	}
	expectMetadata(t, event.Metadata, map[string]string{
		"change:" + string(domain.ConfigKeyPasswordMinLength): "12 -> 16",
		"revision_from": "1",
		"revision_to":   "2",
		"dangerous":     "false",
		"validation":    "passed",
	})
}

// expectMetadata asserts the audit fields a spec named, and names the one that
// disagreed rather than dumping the whole map.
func expectMetadata(t *testing.T, metadata, expected map[string]string) {
	t.Helper()
	for key, want := range expected {
		if metadata[key] != want {
			t.Fatalf("metadata[%q] = %q, expected %q", key, metadata[key], want)
		}
	}
}

// The reason is the operator's own prose. It belongs on the version row, which
// the audit metadata points at by id; copying it into the trail would put
// client text in a record that is otherwise entirely server-derived.
func TestConfigService_AuditRecordsThatAReasonExistsAndNotItsText(t *testing.T) {
	store := newConfigStore()
	audit := &recordingAudit{}
	request := changeRequest(1, map[domain.ConfigKey]string{domain.ConfigKeyPasswordMinLength: "8"})
	request.Reason = "aprovado no chamado SEC-77"

	if _, err := newConfigService(store, audit, nil).
		Apply(context.Background(), configActor(domain.CapabilitySuperuser), request); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	event := audit.last(t)
	for key, value := range event.Metadata {
		if strings.Contains(value, "SEC-77") {
			t.Fatalf("the stated reason reached the trail through %q", key)
		}
	}
	if event.Metadata["reason_given"] != "true" {
		t.Fatalf("expected the trail to record that a reason was given, got %+v", event.Metadata)
	}
	if event.Metadata["version_id"] == "" {
		t.Fatal("expected the trail to name the version that holds the reason")
	}
}

func TestConfigService_ApplyRefusesAStaleRevisionWithoutWriting(t *testing.T) {
	store := newConfigStore()
	audit := &recordingAudit{}
	configuration := newConfigService(store, audit, nil)
	actor := configActor(domain.CapabilityConfigManage)

	first, err := configuration.Apply(context.Background(), actor,
		changeRequest(1, map[domain.ConfigKey]string{domain.ConfigKeyPasswordMinLength: "16"}))
	if err != nil || !first.Applied {
		t.Fatalf("first apply: %+v %v", first, err)
	}

	// The second administrator loaded the form at revision 1 and is still
	// holding it.
	second, err := configuration.Apply(context.Background(), actor,
		changeRequest(1, map[domain.ConfigKey]string{domain.ConfigKeyPasswordMinLength: "20"}))

	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("expected a conflict, got %v", err)
	}
	if second.Applied {
		t.Fatal("a conflicting change must not be applied")
	}
	if !second.Plan.Stale || second.Plan.Revision != 2 {
		t.Fatalf("expected the plan to report the current revision, got %+v", second.Plan)
	}
	if store.values[domain.ConfigKeyPasswordMinLength].Int != 16 {
		t.Fatal("the losing writer overwrote the winner")
	}
	if store.applies != 1 {
		t.Fatalf("expected the store to be written once, got %d", store.applies)
	}
	if audit.last(t).Result != domain.AuditResultDenied {
		t.Fatal("expected the refused change to be recorded as denied")
	}
}

func TestConfigService_ApplyIsIdempotentForAResubmittedForm(t *testing.T) {
	store := newConfigStore()
	audit := &recordingAudit{}
	configuration := newConfigService(store, audit, nil)
	actor := configActor(domain.CapabilityConfigManage)

	if _, err := configuration.Apply(context.Background(), actor,
		changeRequest(1, map[domain.ConfigKey]string{domain.ConfigKeyPasswordMinLength: "16"})); err != nil {
		t.Fatalf("first apply: %v", err)
	}

	// Same values, current revision: the desired state is already the stored
	// state.
	result, err := configuration.Apply(context.Background(), actor,
		changeRequest(2, map[domain.ConfigKey]string{domain.ConfigKeyPasswordMinLength: "16"}))
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if result.Applied {
		t.Fatal("a no-op must not be reported as applied")
	}
	if store.revision != 2 || store.applies != 1 {
		t.Fatalf("a no-op must not write: revision %d, applies %d", store.revision, store.applies)
	}
	if len(store.versions) != 1 {
		t.Fatalf("a no-op must not record a version, got %d", len(store.versions))
	}
}

func TestConfigService_ApplyRefusesAnUnknownOrReadOnlyKey(t *testing.T) {
	cases := map[string]domain.ConfigKey{
		"unknown key":        "auth.password.min_lengthx",
		"credential":         "secret.smtp_password",
		"deployment setting": "oidc.enabled",
		"infrastructure":     "infra.postgres.host",
	}
	for name, key := range cases {
		t.Run(name, func(t *testing.T) {
			store := newConfigStore()
			_, err := newConfigService(store, &recordingAudit{}, nil).
				Apply(context.Background(), configActor(domain.CapabilitySuperuser),
					changeRequest(1, map[domain.ConfigKey]string{key: "true"}))

			if !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("expected a refusal, got %v", err)
			}
			if store.applies != 0 {
				t.Fatal("a refused key must never reach the store")
			}
		})
	}
}

func TestConfigService_ApplyReportsEveryInvalidFieldAtOnce(t *testing.T) {
	store := newConfigStore()
	result, err := newConfigService(store, &recordingAudit{}, nil).
		Apply(context.Background(), configActor(domain.CapabilityConfigManage),
			changeRequest(1, map[domain.ConfigKey]string{
				domain.ConfigKeyPasswordMinLength: "2",
				domain.ConfigKeyLoginFailedLimit:  "999",
				domain.ConfigKeyDeviceMaxPerUser:  "3",
			}))

	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected a refusal, got %v", err)
	}
	if len(result.Plan.Errors) != 2 {
		t.Fatalf("expected both invalid fields to be named, got %+v", result.Plan.Errors)
	}
	if len(result.Plan.Changes) != 0 {
		t.Fatal("an invalid request must produce no diff to confirm")
	}
	if store.applies != 0 {
		t.Fatal("nothing may be written when a field is invalid")
	}
}

// A change that weakens authentication needs the capability that confers all
// authority, and holding the ordinary manage capability is not enough.
func TestConfigService_DangerousChangeRequiresSuperuser(t *testing.T) {
	store := newConfigStore()
	audit := &recordingAudit{}
	request := changeRequest(1, map[domain.ConfigKey]string{domain.ConfigKeyPasswordRequireSymbol: "false"})
	request.Reason = "migração de política"

	result, err := newConfigService(store, audit, nil).
		Apply(context.Background(), configActor(domain.CapabilityConfigManage), request)

	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("expected a refusal, got %v", err)
	}
	if result.Plan.RequiredCapability != domain.CapabilitySuperuser || result.Plan.Authorized {
		t.Fatalf("expected the plan to name the capability it needs, got %+v", result.Plan)
	}
	if store.applies != 0 {
		t.Fatal("an unauthorized change must never reach the store")
	}
	if audit.last(t).Result != domain.AuditResultDenied {
		t.Fatal("expected the denial to be recorded")
	}

	store = newConfigStore()
	if _, err := newConfigService(store, audit, nil).
		Apply(context.Background(), configActor(domain.CapabilitySuperuser), request); err != nil {
		t.Fatalf("superuser must be allowed: %v", err)
	}
	if store.values[domain.ConfigKeyPasswordRequireSymbol].Bool {
		t.Fatal("expected the change to be stored")
	}
}

func TestConfigService_DangerousChangeRequiresAStatedReason(t *testing.T) {
	store := newConfigStore()
	_, err := newConfigService(store, &recordingAudit{}, nil).
		Apply(context.Background(), configActor(domain.CapabilitySuperuser),
			changeRequest(1, map[domain.ConfigKey]string{domain.ConfigKeyPasswordMinLength: "8"}))

	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected a dangerous change without a reason to be refused, got %v", err)
	}
	if store.applies != 0 {
		t.Fatal("nothing may be written without the required reason")
	}
}

// Preview must not write, and must tell an operator who cannot apply exactly
// what the change would do and what it would need.
func TestConfigService_PreviewWritesNothing(t *testing.T) {
	store := newConfigStore()
	audit := &recordingAudit{}

	if _, err := newConfigService(store, audit, nil).
		Preview(context.Background(), configActor(domain.CapabilityConfigRead),
			changeRequest(1, map[domain.ConfigKey]string{domain.ConfigKeyPasswordRequireNumber: "false"})); err != nil {
		t.Fatalf("Preview: %v", err)
	}

	if store.applies != 0 || store.revision != 1 {
		t.Fatal("preview must not write")
	}
	if len(audit.events) != 0 {
		t.Fatal("preview is not a mutation and must not produce an audit event")
	}
}

// An operator who cannot apply a change is still told exactly what it would do
// and what it would need, which is the whole point of a separate preview.
func TestConfigService_PreviewNamesTheDiffAndTheCapability(t *testing.T) {
	plan, err := newConfigService(newConfigStore(), &recordingAudit{}, nil).
		Preview(context.Background(), configActor(domain.CapabilityConfigRead),
			changeRequest(1, map[domain.ConfigKey]string{domain.ConfigKeyPasswordRequireNumber: "false"}))
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}

	if !plan.Dangerous {
		t.Fatal("disabling a complexity requirement is a dangerous change")
	}
	if plan.RequiredCapability != domain.CapabilitySuperuser {
		t.Fatalf("expected admin.superuser, got %q", plan.RequiredCapability)
	}
	if plan.Authorized {
		t.Fatal("a reader must not be reported as authorized for it")
	}
	if len(plan.Warnings) == 0 {
		t.Fatal("expected the plan to explain why the change is dangerous")
	}
	if plan.Apply != domain.ConfigApplyRuntime {
		t.Fatalf("expected a runtime change, got %q", plan.Apply)
	}
	if diff := plan.AffectedServices; len(diff) != 1 || diff[0] != "auth-service" {
		t.Fatalf("expected the owning service to be named, got %v", diff)
	}
	expectSingleChange(t, plan, domain.BoolValue(true), domain.BoolValue(false))
}

// expectSingleChange asserts the plan describes exactly one transition.
func expectSingleChange(t *testing.T, plan service.ConfigPlan, from, to domain.ConfigValue) {
	t.Helper()
	if len(plan.Changes) != 1 {
		t.Fatalf("expected exactly one change, got %+v", plan.Changes)
	}
	change := plan.Changes[0]
	if !change.From.Equal(from) || !change.To.Equal(to) {
		t.Fatalf("expected %s -> %s, got %s -> %s",
			from.AuditString(), to.AuditString(), change.From.AuditString(), change.To.AuditString())
	}
}

func TestConfigService_RollbackRestoresTheEarlierValueAsANewVersion(t *testing.T) {
	store := newConfigStore()
	audit := &recordingAudit{}
	configuration := newConfigService(store, audit, nil)
	actor := configActor(domain.CapabilityConfigManage)

	applied, err := configuration.Apply(context.Background(), actor,
		changeRequest(1, map[domain.ConfigKey]string{domain.ConfigKeyDeviceMaxPerUser: "20"}))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	result, err := configuration.Rollback(context.Background(), actor, applied.Version.ID, 2, "reverter teste")
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	if store.values[domain.ConfigKeyDeviceMaxPerUser].Int != 5 {
		t.Fatalf("expected the earlier value to be restored, got %d", store.values[domain.ConfigKeyDeviceMaxPerUser].Int)
	}
	// Forward-only: the rollback is a third revision, not an erasure of the
	// second.
	if result.State.Revision != 3 || len(store.versions) != 2 {
		t.Fatalf("expected the rollback to append a version, got revision %d and %d versions",
			result.State.Revision, len(store.versions))
	}
	if result.Version.RevertsRevision != applied.Version.Revision {
		t.Fatalf("expected the new version to name the one it reverts, got %d", result.Version.RevertsRevision)
	}
	event := audit.last(t)
	if event.Action != domain.AuditActionConfigRollback || event.Result != domain.AuditResultSuccess {
		t.Fatalf("unexpected audit event: %+v", event)
	}
}

func TestConfigService_RollbackRefusesAStaleRevision(t *testing.T) {
	store := newConfigStore()
	configuration := newConfigService(store, &recordingAudit{}, nil)
	actor := configActor(domain.CapabilityConfigManage)

	applied, err := configuration.Apply(context.Background(), actor,
		changeRequest(1, map[domain.ConfigKey]string{domain.ConfigKeyDeviceMaxPerUser: "20"}))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	_, err = configuration.Rollback(context.Background(), actor, applied.Version.ID, 1, "")
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("expected a conflict, got %v", err)
	}
	if store.values[domain.ConfigKeyDeviceMaxPerUser].Int != 20 {
		t.Fatal("a conflicting rollback must not write")
	}
}

// Undoing a hardening is producing a weakening, and is judged as one.
func TestConfigService_RollbackIntoADangerousValueRequiresSuperuser(t *testing.T) {
	store := newConfigStore()
	store.values[domain.ConfigKeyPasswordRequireSymbol] = domain.BoolValue(false)
	configuration := newConfigService(store, &recordingAudit{}, nil)

	hardened, err := configuration.Apply(context.Background(), configActor(domain.CapabilityConfigManage),
		changeRequest(1, map[domain.ConfigKey]string{domain.ConfigKeyPasswordRequireSymbol: "true"}))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	_, err = configuration.Rollback(context.Background(), configActor(domain.CapabilityConfigManage),
		hardened.Version.ID, 2, "voltar atrás")
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("expected the rollback to be refused, got %v", err)
	}
	if store.values[domain.ConfigKeyPasswordRequireSymbol].Bool != true {
		t.Fatal("the refused rollback wrote anyway")
	}
}

func TestConfigService_RollbackRefusesAVersionThatDoesNotExist(t *testing.T) {
	_, err := newConfigService(newConfigStore(), &recordingAudit{}, nil).
		Rollback(context.Background(), configActor(domain.CapabilitySuperuser), 999, 1, "")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected not found, got %v", err)
	}
	_, err = newConfigService(newConfigStore(), &recordingAudit{}, nil).
		Rollback(context.Background(), configActor(domain.CapabilitySuperuser), 0, 1, "")
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected a malformed version id to be refused, got %v", err)
	}
}

func TestConfigService_VersionsClampsTheRequestedPage(t *testing.T) {
	if got := service.ClampConfigVersionLimit(0); got != 25 {
		t.Fatalf("expected the default, got %d", got)
	}
	if got := service.ClampConfigVersionLimit(10_000); got != 100 {
		t.Fatalf("expected the ceiling, got %d", got)
	}
	if got := service.ClampConfigVersionLimit(7); got != 7 {
		t.Fatalf("expected the requested limit, got %d", got)
	}
	if _, err := newConfigService(newConfigStore(), &recordingAudit{}, nil).
		Versions(context.Background(), "auth.unknown", 10); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected an unknown document to be refused, got %v", err)
	}
}

func TestConfigService_RefusesAnUnknownDocumentAndAMissingRevision(t *testing.T) {
	configuration := newConfigService(newConfigStore(), &recordingAudit{}, nil)
	actor := configActor(domain.CapabilitySuperuser)

	request := changeRequest(1, map[domain.ConfigKey]string{domain.ConfigKeyDeviceMaxPerUser: "6"})
	request.Document = "auth.unknown"
	if _, err := configuration.Apply(context.Background(), actor, request); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected an unknown document to be refused, got %v", err)
	}

	request = changeRequest(0, map[domain.ConfigKey]string{domain.ConfigKeyDeviceMaxPerUser: "6"})
	if _, err := configuration.Apply(context.Background(), actor, request); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected a missing revision to be refused, got %v", err)
	}

	request = changeRequest(1, nil)
	if _, err := configuration.Apply(context.Background(), actor, request); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected an empty change set to be refused, got %v", err)
	}
}

func TestConfigService_UnwiredServiceIsUnavailable(t *testing.T) {
	var configuration *service.ConfigService
	if _, err := configuration.Catalog(context.Background()); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected unavailable, got %v", err)
	}
	if _, err := configuration.Apply(context.Background(), configActor(), service.ConfigChangeRequest{}); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected unavailable, got %v", err)
	}
}

func settingFor(t *testing.T, view service.ConfigCatalogView, key string) service.ConfigSetting {
	t.Helper()
	for _, setting := range view.Settings {
		if string(setting.Definition.Key) == key {
			return setting
		}
	}
	t.Fatalf("catalog has no setting %q", key)
	return service.ConfigSetting{}
}

// A broken store is not a refusal. The trail must show "error" and not
// "denied", because collapsing the two would make an outage look like an
// attack and an attack look like an outage.
func TestConfigService_StoreFailuresAreRecordedAsErrors(t *testing.T) {
	broken := errors.New("database unreachable")

	store := newConfigStore()
	store.readErr = broken
	if _, err := newConfigService(store, &recordingAudit{}, nil).Catalog(context.Background()); !errors.Is(err, broken) {
		t.Fatalf("expected the read failure to propagate, got %v", err)
	}

	store = newConfigStore()
	store.applyErr = broken
	audit := &recordingAudit{}
	if _, err := newConfigService(store, audit, nil).
		Apply(context.Background(), configActor(domain.CapabilityConfigManage),
			changeRequest(1, map[domain.ConfigKey]string{domain.ConfigKeyDeviceMaxPerUser: "6"})); !errors.Is(err, broken) {
		t.Fatalf("expected the write failure to propagate, got %v", err)
	}
	if audit.last(t).Result != domain.AuditResultError {
		t.Fatal("expected a broken store to be recorded as an error, not a denial")
	}
}

func TestConfigService_VersionsReturnsTheRecordedHistory(t *testing.T) {
	store := newConfigStore()
	configuration := newConfigService(store, &recordingAudit{}, nil)
	actor := configActor(domain.CapabilityConfigManage)

	if _, err := configuration.Apply(context.Background(), actor,
		changeRequest(1, map[domain.ConfigKey]string{domain.ConfigKeyDeviceMaxPerUser: "9"})); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	versions, err := configuration.Versions(context.Background(), domain.ConfigDocumentAuthPolicy, 0)
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	if len(versions) != 1 || versions[0].Revision != 2 {
		t.Fatalf("unexpected history: %+v", versions)
	}
	if len(versions[0].Changes) != 1 || versions[0].Changes[0].To.Int != 9 {
		t.Fatalf("expected the recorded change, got %+v", versions[0].Changes)
	}
}

func TestConfigService_PreviewOnAnUnwiredServiceIsUnavailable(t *testing.T) {
	var configuration *service.ConfigService

	if _, err := configuration.Preview(context.Background(), configActor(), service.ConfigChangeRequest{}); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected unavailable, got %v", err)
	}
	if _, err := configuration.Rollback(context.Background(), configActor(), 1, 1, ""); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected unavailable, got %v", err)
	}
	if _, err := configuration.Versions(context.Background(), domain.ConfigDocumentAuthPolicy, 10); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected unavailable, got %v", err)
	}
}

// A change set that names two documents cannot be checked against one revision,
// so it is refused rather than half-applied.
func TestConfigService_RefusesAnOversizedReason(t *testing.T) {
	request := changeRequest(1, map[domain.ConfigKey]string{domain.ConfigKeyDeviceMaxPerUser: "9"})
	request.Reason = strings.Repeat("a", 501)

	_, err := newConfigService(newConfigStore(), &recordingAudit{}, nil).
		Apply(context.Background(), configActor(domain.CapabilityConfigManage), request)

	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected an oversized reason to be refused, got %v", err)
	}
}

// The read path must survive a document the store cannot answer for, without
// pretending the catalog is empty.
func TestConfigService_CatalogPropagatesAReadFailure(t *testing.T) {
	store := newConfigStore()
	store.readErr = domain.ErrUnavailable

	if _, err := newConfigService(store, &recordingAudit{}, nil).
		Catalog(context.Background()); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected unavailable, got %v", err)
	}
}

// Finding 2: reverting a version that later changes have superseded would
// silently discard those changes. The rollback asserts that the version being
// reverted is still the one in force.
func TestConfigService_RollbackRefusesASupersededVersion(t *testing.T) {
	store := newConfigStore()
	audit := &recordingAudit{}
	configuration := newConfigService(store, audit, nil)
	actor := configActor(domain.CapabilityConfigManage)

	// v1 moves the limit off its default, v2 moves it again.
	first, err := configuration.Apply(context.Background(), actor,
		changeRequest(1, map[domain.ConfigKey]string{domain.ConfigKeyDeviceMaxPerUser: "20"}))
	if err != nil {
		t.Fatalf("first apply: %v", err)
	}
	if _, err := configuration.Apply(context.Background(), actor,
		changeRequest(2, map[domain.ConfigKey]string{domain.ConfigKeyDeviceMaxPerUser: "30"})); err != nil {
		t.Fatalf("second apply: %v", err)
	}

	// Reverting v1 now would put the value back to 5 and erase v2 with it.
	result, err := configuration.Rollback(context.Background(), actor, first.Version.ID, 3, "reverter v1")

	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("expected a conflict, got %v", err)
	}
	if result.Applied {
		t.Fatal("a superseded rollback must not be applied")
	}
	if !result.Plan.Superseded {
		t.Fatalf("expected the plan to report the version as superseded, got %+v", result.Plan)
	}
	if store.values[domain.ConfigKeyDeviceMaxPerUser].Int != 30 {
		t.Fatalf("the later change must survive, got %d", store.values[domain.ConfigKeyDeviceMaxPerUser].Int)
	}
	if store.revision != 3 {
		t.Fatalf("nothing may be written, revision moved to %d", store.revision)
	}
	if audit.last(t).Result != domain.AuditResultDenied {
		t.Fatal("expected the refusal to be recorded as denied")
	}
}

// The version immediately before the current state is still reversible: the
// superseded check must refuse stale rollbacks, not every rollback.
func TestConfigService_RollbackAcceptsTheVersionStillInForce(t *testing.T) {
	store := newConfigStore()
	configuration := newConfigService(store, &recordingAudit{}, nil)
	actor := configActor(domain.CapabilityConfigManage)

	applied, err := configuration.Apply(context.Background(), actor,
		changeRequest(1, map[domain.ConfigKey]string{domain.ConfigKeyDeviceMaxPerUser: "20"}))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	result, err := configuration.Rollback(context.Background(), actor, applied.Version.ID, 2, "reverter")
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if !result.Applied || store.values[domain.ConfigKeyDeviceMaxPerUser].Int != 5 {
		t.Fatalf("expected the value to be restored, got %+v", store.values[domain.ConfigKeyDeviceMaxPerUser])
	}
}

// A rollback is all or nothing. If one field of the version has moved on, the
// whole rollback is refused rather than restoring the fields that still match.
func TestConfigService_RollbackIsRefusedWhenAnyFieldWasSuperseded(t *testing.T) {
	store := newConfigStore()
	configuration := newConfigService(store, &recordingAudit{}, nil)
	actor := configActor(domain.CapabilityConfigManage)

	multi, err := configuration.Apply(context.Background(), actor,
		changeRequest(1, map[domain.ConfigKey]string{
			domain.ConfigKeyDeviceMaxPerUser:  "20",
			domain.ConfigKeyLoginFailedWindow: "30",
		}))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// Only one of the two fields moves on.
	if _, err := configuration.Apply(context.Background(), actor,
		changeRequest(2, map[domain.ConfigKey]string{domain.ConfigKeyLoginFailedWindow: "45"})); err != nil {
		t.Fatalf("second apply: %v", err)
	}

	_, err = configuration.Rollback(context.Background(), actor, multi.Version.ID, 3, "reverter")

	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("expected a conflict, got %v", err)
	}
	if store.values[domain.ConfigKeyDeviceMaxPerUser].Int != 20 {
		t.Fatal("no field may be restored when the rollback is refused")
	}
	if store.values[domain.ConfigKeyLoginFailedWindow].Int != 45 {
		t.Fatal("the superseding change must survive")
	}
}

// Rolling the same version back twice: the second attempt finds the version no
// longer in force and is refused, rather than being answered as "nothing to do".
func TestConfigService_RollbackTwiceIsRefusedRatherThanANoOp(t *testing.T) {
	store := newConfigStore()
	configuration := newConfigService(store, &recordingAudit{}, nil)
	actor := configActor(domain.CapabilityConfigManage)

	applied, err := configuration.Apply(context.Background(), actor,
		changeRequest(1, map[domain.ConfigKey]string{domain.ConfigKeyDeviceMaxPerUser: "20"}))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := configuration.Rollback(context.Background(), actor, applied.Version.ID, 2, ""); err != nil {
		t.Fatalf("first rollback: %v", err)
	}

	result, err := configuration.Rollback(context.Background(), actor, applied.Version.ID, 3, "")

	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("expected a conflict, got %v", err)
	}
	if result.Applied {
		t.Fatal("the second rollback must not be reported as applied")
	}
	if store.revision != 3 {
		t.Fatalf("nothing may be written, revision moved to %d", store.revision)
	}
}

// Finding 4: once the transaction has committed, nothing that happens
// afterwards may report the mutation as not having happened — a client told the
// write failed will send it again.
func TestConfigService_CommittedChangeSurvivesALaterReadFailure(t *testing.T) {
	store := newConfigStore()
	audit := &recordingAudit{}
	configuration := newConfigService(store, audit, nil)

	// Every read after this point fails. The write itself still committed.
	store.failReadsAfterApply = true

	result, err := configuration.Apply(context.Background(), configActor(domain.CapabilityConfigManage),
		changeRequest(1, map[domain.ConfigKey]string{domain.ConfigKeyDeviceMaxPerUser: "9"}))

	if err != nil {
		t.Fatalf("a committed change must not surface as an error: %v", err)
	}
	if !result.Applied {
		t.Fatal("a committed change must be reported as applied")
	}
	if result.Version.ID == 0 {
		t.Fatal("the version id must survive: it is how the operator finds the change")
	}
	if result.State.Revision != 2 || result.State.Values[domain.ConfigKeyDeviceMaxPerUser].Int != 9 {
		t.Fatalf("the response must describe what was committed, got %+v", result.State)
	}
	event := audit.last(t)
	if event.Result != domain.AuditResultSuccess {
		t.Fatalf("a committed change must not be audited as a failure, got %q", event.Result)
	}
	if event.Metadata["applied"] != "true" || event.Metadata["version_id"] == "" {
		t.Fatalf("the trail must record the mutation that happened, got %+v", event.Metadata)
	}
}

// The other half of the distinction: a write that never committed is applied
// false, records no version, and is audited as a refusal.
func TestConfigService_UncommittedChangeIsNotReportedAsApplied(t *testing.T) {
	store := newConfigStore()
	store.applyErr = domain.ErrConflict
	audit := &recordingAudit{}

	result, err := newConfigService(store, audit, nil).
		Apply(context.Background(), configActor(domain.CapabilityConfigManage),
			changeRequest(1, map[domain.ConfigKey]string{domain.ConfigKeyDeviceMaxPerUser: "9"}))

	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("expected the conflict to reach the caller, got %v", err)
	}
	if result.Applied || result.Version.ID != 0 {
		t.Fatalf("a failed write must invent no version, got %+v", result)
	}
	if len(store.versions) != 0 {
		t.Fatalf("no version may be recorded, got %d", len(store.versions))
	}
	if audit.last(t).Result != domain.AuditResultDenied {
		t.Fatal("expected the refusal to be recorded as denied")
	}
}

// The rollback preview and the confirmed rollback are the same derivation. A
// version still in force previews as revertible and shows the transition the
// apply would perform — the reverse of what the version did, not a diff against
// values the console happened to be rendering.
func TestConfigService_PreviewRollbackOfTheVersionInForce(t *testing.T) {
	store := newConfigStore()
	audit := &recordingAudit{}
	configuration := newConfigService(store, audit, nil)
	actor := configActor(domain.CapabilityConfigManage)

	applied, err := configuration.Apply(context.Background(), actor,
		changeRequest(1, map[domain.ConfigKey]string{domain.ConfigKeyDeviceMaxPerUser: "20"}))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	plan, err := configuration.PreviewRollback(context.Background(), actor, applied.Version.ID, 2, "")
	if err != nil {
		t.Fatalf("PreviewRollback: %v", err)
	}

	if plan.Superseded {
		t.Fatal("the version still in force must preview as revertible")
	}
	if plan.Stale {
		t.Fatalf("the revision matches; the plan must not report it stale: %+v", plan)
	}
	expectSingleChange(t, plan, domain.IntValue(20), domain.IntValue(5))
	// A preview writes nothing and audits nothing.
	if store.applies != 1 || store.revision != 2 {
		t.Fatalf("preview must not write: applies=%d revision=%d", store.applies, store.revision)
	}
	if len(audit.events) != 1 {
		t.Fatalf("preview is not a mutation and must raise no audit event, got %d", len(audit.events))
	}
}

// The finding: reverting v1 after v2 moved the value must be reported as
// impossible *at preview time*, not discovered on confirm. The plan still shows
// what that version would restore, so an operator can see why it is refused.
func TestConfigService_PreviewRollbackReportsASupersededVersion(t *testing.T) {
	store := newConfigStore()
	configuration := newConfigService(store, &recordingAudit{}, nil)
	actor := configActor(domain.CapabilityConfigManage)

	first, err := configuration.Apply(context.Background(), actor,
		changeRequest(1, map[domain.ConfigKey]string{domain.ConfigKeyDeviceMaxPerUser: "20"}))
	if err != nil {
		t.Fatalf("first apply: %v", err)
	}
	if _, err := configuration.Apply(context.Background(), actor,
		changeRequest(2, map[domain.ConfigKey]string{domain.ConfigKeyDeviceMaxPerUser: "30"})); err != nil {
		t.Fatalf("second apply: %v", err)
	}

	plan, err := configuration.PreviewRollback(context.Background(), actor, first.Version.ID, 3, "")
	if err != nil {
		t.Fatalf("PreviewRollback: %v", err)
	}

	if !plan.Superseded {
		t.Fatalf("expected the superseded version to be reported as such: %+v", plan)
	}
	// The revision is current — the console loaded after v2 — which is exactly
	// why optimistic locking alone cannot catch this.
	if plan.Stale {
		t.Fatal("superseded and stale are different facts; the revision matches here")
	}
	// The plan describes what that version would restore, so the operator can
	// see the version it names rather than a diff against the current value.
	expectSingleChange(t, plan, domain.IntValue(20), domain.IntValue(5))
}

// One diverged field is enough. A rollback is all or nothing, so the preview
// must not present a partially applicable one.
func TestConfigService_PreviewRollbackIsSupersededWhenAnyFieldMoved(t *testing.T) {
	store := newConfigStore()
	configuration := newConfigService(store, &recordingAudit{}, nil)
	actor := configActor(domain.CapabilityConfigManage)

	multi, err := configuration.Apply(context.Background(), actor,
		changeRequest(1, map[domain.ConfigKey]string{
			domain.ConfigKeyDeviceMaxPerUser:  "20",
			domain.ConfigKeyLoginFailedWindow: "30",
		}))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// Only the second field moves on; the first still holds what v1 set.
	if _, err := configuration.Apply(context.Background(), actor,
		changeRequest(2, map[domain.ConfigKey]string{domain.ConfigKeyLoginFailedWindow: "45"})); err != nil {
		t.Fatalf("second apply: %v", err)
	}

	plan, err := configuration.PreviewRollback(context.Background(), actor, multi.Version.ID, 3, "")
	if err != nil {
		t.Fatalf("PreviewRollback: %v", err)
	}

	if !plan.Superseded {
		t.Fatalf("one diverged field must make the whole rollback superseded: %+v", plan)
	}
}

// A revision the console no longer holds is a different problem from a
// superseded version, and the preview keeps them apart.
func TestConfigService_PreviewRollbackReportsAStaleRevisionSeparately(t *testing.T) {
	store := newConfigStore()
	configuration := newConfigService(store, &recordingAudit{}, nil)
	actor := configActor(domain.CapabilityConfigManage)

	applied, err := configuration.Apply(context.Background(), actor,
		changeRequest(1, map[domain.ConfigKey]string{domain.ConfigKeyDeviceMaxPerUser: "20"}))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// The version is still in force, but this caller is holding revision 1.
	plan, err := configuration.PreviewRollback(context.Background(), actor, applied.Version.ID, 1, "")
	if err != nil {
		t.Fatalf("PreviewRollback: %v", err)
	}

	if !plan.Stale {
		t.Fatalf("expected a stale revision to be reported: %+v", plan)
	}
	if plan.Superseded {
		t.Fatal("the version is still in force; only the caller's revision is behind")
	}
	if plan.Revision != 2 {
		t.Fatalf("expected the current revision in the plan, got %d", plan.Revision)
	}
}

func TestConfigService_PreviewRollbackRefusesAMissingOrMalformedVersion(t *testing.T) {
	configuration := newConfigService(newConfigStore(), &recordingAudit{}, nil)
	actor := configActor(domain.CapabilityConfigRead)

	if _, err := configuration.PreviewRollback(context.Background(), actor, 999, 1, ""); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected not found, got %v", err)
	}
	if _, err := configuration.PreviewRollback(context.Background(), actor, 0, 1, ""); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected a malformed version id to be refused, got %v", err)
	}

	var unwired *service.ConfigService
	if _, err := unwired.PreviewRollback(context.Background(), actor, 1, 1, ""); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected unavailable, got %v", err)
	}
}

// The preview and the confirmed rollback must agree about what a rollback is.
// If the two derivations ever drift, the console shows one thing and the server
// does another — which is the failure this endpoint exists to prevent.
func TestConfigService_PreviewRollbackAgreesWithTheConfirmedRollback(t *testing.T) {
	store := newConfigStore()
	configuration := newConfigService(store, &recordingAudit{}, nil)
	actor := configActor(domain.CapabilityConfigManage)

	applied, err := configuration.Apply(context.Background(), actor,
		changeRequest(1, map[domain.ConfigKey]string{domain.ConfigKeyDeviceMaxPerUser: "20"}))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	previewed, err := configuration.PreviewRollback(context.Background(), actor, applied.Version.ID, 2, "")
	if err != nil {
		t.Fatalf("PreviewRollback: %v", err)
	}
	confirmed, err := configuration.Rollback(context.Background(), actor, applied.Version.ID, 2, "")
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	if len(previewed.Changes) != len(confirmed.Plan.Changes) {
		t.Fatalf("preview and apply disagree about the change count: %d vs %d",
			len(previewed.Changes), len(confirmed.Plan.Changes))
	}
	for index, change := range previewed.Changes {
		applied := confirmed.Plan.Changes[index]
		if change.Key != applied.Key || !change.From.Equal(applied.From) || !change.To.Equal(applied.To) {
			t.Fatalf("preview and apply disagree at %d: %+v vs %+v", index, change, applied)
		}
	}
}
