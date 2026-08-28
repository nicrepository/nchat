package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
	"github.com/nicrepository/nchat/services/admin-service/internal/service"
)

// ConfigAdmin is the configuration surface the routes drive (issue #580).
type ConfigAdmin interface {
	Catalog(ctx context.Context) (service.ConfigCatalogView, error)
	Preview(ctx context.Context, actor service.Actor, request service.ConfigChangeRequest) (service.ConfigPlan, error)
	Apply(ctx context.Context, actor service.Actor, request service.ConfigChangeRequest) (service.ConfigApplyResult, error)
	PreviewRollback(ctx context.Context, actor service.Actor, versionID int64, expectedRevision int, reason string) (service.ConfigPlan, error)
	Versions(ctx context.Context, document domain.ConfigDocument, limit int) ([]domain.ConfigVersion, error)
	Rollback(ctx context.Context, actor service.Actor, versionID int64, expectedRevision int, reason string) (service.ConfigApplyResult, error)
}

// configSettingPayload is one registry definition and its effective state.
//
// The two fields that carry a value are mutually exclusive by construction in
// newConfigSettingPayload: a sensitive definition populates `configured` and
// leaves `value` absent, and nothing in this file has a branch that can
// populate both. That is the whole of the secret-exposure defence on the read
// path, and it is one function long on purpose.
type configSettingPayload struct {
	Key              string              `json:"key"`
	Label            string              `json:"label"`
	Description      string              `json:"description"`
	Category         string              `json:"category"`
	OwnerService     string              `json:"owner_service"`
	Class            string              `json:"class"`
	Source           string              `json:"source"`
	Apply            string              `json:"apply"`
	Type             string              `json:"type"`
	Unit             string              `json:"unit,omitempty"`
	Min              *int64              `json:"min,omitempty"`
	Max              *int64              `json:"max,omitempty"`
	Nullable         bool                `json:"nullable"`
	Default          *domain.ConfigValue `json:"default,omitempty"`
	Editable         bool                `json:"editable"`
	ReadOnlyReason   string              `json:"read_only_reason,omitempty"`
	Sensitive        bool                `json:"sensitive"`
	Document         string              `json:"document,omitempty"`
	ManageCapability string              `json:"manage_capability,omitempty"`
	DangerNote       string              `json:"danger_note,omitempty"`
	Rollbackable     bool                `json:"rollbackable"`
	EnvVar           string              `json:"env_var,omitempty"`
	Observable       bool                `json:"observable"`
	Value            *domain.ConfigValue `json:"value,omitempty"`
	Configured       *bool               `json:"configured,omitempty"`
}

type configDocumentPayload struct {
	Key      string `json:"key"`
	Revision int    `json:"revision"`
}

type configDiffPayload struct {
	Key          string             `json:"key"`
	Label        string             `json:"label"`
	Category     string             `json:"category"`
	OwnerService string             `json:"owner_service"`
	Apply        string             `json:"apply"`
	Unit         string             `json:"unit,omitempty"`
	Dangerous    bool               `json:"dangerous"`
	DangerNote   string             `json:"danger_note,omitempty"`
	From         domain.ConfigValue `json:"from"`
	To           domain.ConfigValue `json:"to"`
}

type configErrorPayload struct {
	Key     string `json:"key"`
	Message string `json:"message"`
}

type configPlanPayload struct {
	Document           string               `json:"document"`
	Revision           int                  `json:"revision"`
	Stale              bool                 `json:"stale"`
	Superseded         bool                 `json:"superseded"`
	Changes            []configDiffPayload  `json:"changes"`
	Dangerous          bool                 `json:"dangerous"`
	RequiredCapability string               `json:"required_capability"`
	Authorized         bool                 `json:"authorized"`
	ReasonRequired     bool                 `json:"reason_required"`
	Warnings           []string             `json:"warnings"`
	Errors             []configErrorPayload `json:"errors"`
	AffectedServices   []string             `json:"affected_services"`
	Apply              string               `json:"apply"`
}

type configVersionPayload struct {
	ID              string              `json:"id"`
	Document        string              `json:"document"`
	Revision        int                 `json:"revision"`
	AppliedAt       time.Time           `json:"applied_at"`
	ActorUserID     string              `json:"actor_user_id"`
	ActorEmail      string              `json:"actor_email"`
	CorrelationID   string              `json:"correlation_id"`
	Reason          string              `json:"reason"`
	RevertsRevision int                 `json:"reverts_revision"`
	Rollbackable    bool                `json:"rollbackable"`
	Changes         []configDiffPayload `json:"changes"`
}

// GetConfiguration serves the registry and the effective state of every
// setting.
//
// Requires admin.config.read. It is a read and it is guarded like a write: the
// catalog names every integration, endpoint and credential the deployment has,
// which is reconnaissance for anyone who should not be holding this session.
func GetConfiguration(configuration ConfigAdmin) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if configuration == nil {
			writeUnavailable(w)
			return
		}
		view, err := configuration.Catalog(r.Context())
		if err != nil {
			writeDomainError(w, err)
			return
		}
		settings := make([]configSettingPayload, 0, len(view.Settings))
		for _, setting := range view.Settings {
			settings = append(settings, newConfigSettingPayload(setting))
		}
		documents := make([]configDocumentPayload, 0, len(view.Documents))
		for _, document := range view.Documents {
			documents = append(documents, configDocumentPayload{
				Key:      string(document.Document),
				Revision: document.Revision,
			})
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{
			"documents": documents,
			"settings":  settings,
		})
	})
}

// configChangeBody is the request shape of a preview and of an apply.
//
// `changes` is a map because the console edits a form, but it is not an open
// door: decodeJSONBody refuses any field this struct does not name, the body is
// capped, the number of entries is bounded below, and every key inside is
// resolved against the registry before anything is read from it.
type configChangeBody struct {
	Document         string                     `json:"document"`
	ExpectedRevision json.RawMessage            `json:"expected_revision"`
	Reason           string                     `json:"reason"`
	Changes          map[string]json.RawMessage `json:"changes"`
}

// PreviewConfiguration validates a change and returns the diff it would make.
//
// Requires admin.config.read, and it is a POST, so it also passes the origin
// and CSRF guards like any other non-safe method. It writes nothing: an
// operator who cannot apply a change can still see exactly what it would do and
// which capability it would need.
func PreviewConfiguration(configuration ConfigAdmin) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if configuration == nil {
			writeUnavailable(w)
			return
		}
		actor, request, ok := decodeConfigChange(w, r)
		if !ok {
			return
		}
		plan, err := configuration.Preview(r.Context(), actor, request)
		if err != nil {
			writeDomainError(w, err)
			return
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"plan": newConfigPlanPayload(plan)})
	})
}

// ApplyConfiguration writes a change set.
//
// Requires admin.config.manage, and admin.superuser as well when any resulting
// value is dangerous — that second check is in the service, because whether a
// value weakens the platform is only knowable after it has been parsed.
//
// Failures answer with the platform's error envelope and nothing else: 400 for
// a value the registry refuses, 403 for a capability the actor does not hold,
// 409 for a document that moved since the form was loaded. Field-level
// messages live on the preview response, which is a 200 carrying the plan, so
// the detailed answer stays on the endpoint that exists to produce it rather
// than turning the error envelope into a second shape clients must parse.
//
// A 409 writes nothing and merges nothing. The console reloads, previews
// again, and shows the operator the diff against the state that actually
// exists.
func ApplyConfiguration(configuration ConfigAdmin) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if configuration == nil {
			writeUnavailable(w)
			return
		}
		actor, request, ok := decodeConfigChange(w, r)
		if !ok {
			return
		}
		result, err := configuration.Apply(r.Context(), actor, request)
		if err != nil {
			writeDomainError(w, err)
			return
		}
		writeConfigResult(w, result)
	})
}

type configRollbackBody struct {
	ExpectedRevision json.RawMessage `json:"expected_revision"`
	Reason           string          `json:"reason"`
}

// RollbackConfiguration restores the values one recorded version replaced.
//
// Requires admin.config.manage, plus admin.superuser when the restored value is
// itself dangerous: a rollback that would re-weaken the platform is judged by
// what it produces, not by the fact that it is called a rollback.
func RollbackConfiguration(configuration ConfigAdmin) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if configuration == nil {
			writeUnavailable(w)
			return
		}
		actor, request, ok := decodeConfigRollback(w, r)
		if !ok {
			return
		}
		result, err := configuration.Rollback(r.Context(), actor, request.versionID, request.revision, request.reason)
		if err != nil {
			writeDomainError(w, err)
			return
		}
		writeConfigResult(w, result)
	})
}

// configRollbackRequest is a rollback as the transport understood it: a version
// this service recorded, the revision the console last read, and the operator's
// stated reason. Nothing else — the values to restore come from the version
// itself, server-side.
type configRollbackRequest struct {
	versionID int64
	revision  int
	reason    string
}

// decodeConfigRollback reads and shape-checks a rollback, answering false once
// it has already written the refusal.
//
// Same split as decodeConfigChange, for the same reason: the handler decides
// what to call and how to answer, and the shape of the request is decided in
// one place that every rollback goes through.
func decodeConfigRollback(w http.ResponseWriter, r *http.Request) (service.Actor, configRollbackRequest, bool) {
	actor, ok := actorFrom(w, r)
	if !ok {
		return service.Actor{}, configRollbackRequest{}, false
	}
	versionID, err := strconv.ParseInt(r.PathValue("versionID"), 10, 64)
	if err != nil || versionID <= 0 {
		httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid version")
		return service.Actor{}, configRollbackRequest{}, false
	}
	var body configRollbackBody
	if !decodeJSONBody(w, r, &body) {
		return service.Actor{}, configRollbackRequest{}, false
	}
	revision, valid := integerField(body.ExpectedRevision)
	if !valid || revision <= 0 || revision > maxConfigRevision {
		writeDomainError(w, domain.ErrInvalidInput)
		return service.Actor{}, configRollbackRequest{}, false
	}
	return actor, configRollbackRequest{
		versionID: versionID,
		revision:  int(revision),
		reason:    body.Reason,
	}, true
}

// PreviewConfigurationRollback computes what reverting one version would do.
//
// Requires admin.config.read, and — being a POST — passes the origin and CSRF
// guards like any other non-safe method. It writes nothing: no version is
// recorded, no revision moves, no configuration is persisted.
//
// The request carries only what belongs to the caller: which version, and the
// revision the console last read. The values to restore and the preconditions
// that decide whether the rollback is still possible are derived from the
// stored version by the same code the confirmed rollback uses.
func PreviewConfigurationRollback(configuration ConfigAdmin) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if configuration == nil {
			writeUnavailable(w)
			return
		}
		actor, request, ok := decodeConfigRollback(w, r)
		if !ok {
			return
		}
		plan, err := configuration.PreviewRollback(r.Context(), actor, request.versionID, request.revision, request.reason)
		if err != nil {
			writeDomainError(w, err)
			return
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"plan": newConfigPlanPayload(plan)})
	})
}

// ListConfigurationVersions serves the change history of one document.
//
// Requires admin.config.read. The document is a query parameter validated
// against the registry, not a free-text key: an unknown one is refused rather
// than answered with an empty list, which would make a typo look like a
// document with no history.
func ListConfigurationVersions(configuration ConfigAdmin) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if configuration == nil {
			writeUnavailable(w)
			return
		}
		document, known := parseConfigDocument(r.URL.Query().Get("document"))
		if !known {
			writeInvalidQuery(w)
			return
		}
		limit, ok := parseLimit(r.URL.Query().Get("limit"))
		if !ok {
			writeInvalidQuery(w)
			return
		}
		versions, err := configuration.Versions(r.Context(), document, limit)
		if err != nil {
			writeDomainError(w, err)
			return
		}
		payload := make([]configVersionPayload, 0, len(versions))
		for _, version := range versions {
			payload = append(payload, newConfigVersionPayload(version))
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"versions": payload})
	})
}

// parseConfigDocument resolves the optional document parameter.
//
// Absent means the one document this API can write, which is what the console
// asks for. Anything else must be a document the registry declares: an unknown
// one is refused rather than answered with an empty history, which would make a
// typo look like a document nobody has ever changed.
func parseConfigDocument(raw string) (domain.ConfigDocument, bool) {
	if raw == "" {
		return domain.ConfigDocumentAuthPolicy, true
	}
	document := domain.ConfigDocument(raw)
	return document, domain.ValidConfigDocument(document)
}

// maxConfigRevision bounds the revision a caller may echo back.
//
// The number is the platform's own counter and is compared for equality, so
// this is not a security control — it is what turns an absurd value into a 400
// instead of a database round trip.
const maxConfigRevision = 1 << 31

// decodeConfigChange reads and shape-checks a change request.
//
// The number of entries is capped at the number of settings that exist, which
// is what keeps a body full of unknown keys from being resolved one by one. The
// keys themselves are still each resolved against the registry by the service;
// this only bounds the work.
func decodeConfigChange(w http.ResponseWriter, r *http.Request) (service.Actor, service.ConfigChangeRequest, bool) {
	actor, ok := actorFrom(w, r)
	if !ok {
		return service.Actor{}, service.ConfigChangeRequest{}, false
	}
	var body configChangeBody
	if !decodeJSONBody(w, r, &body) {
		return service.Actor{}, service.ConfigChangeRequest{}, false
	}
	document := domain.ConfigDocument(body.Document)
	if !domain.ValidConfigDocument(document) {
		writeDomainError(w, domain.ErrInvalidInput)
		return service.Actor{}, service.ConfigChangeRequest{}, false
	}
	revision, valid := integerField(body.ExpectedRevision)
	if !valid || revision <= 0 || revision > maxConfigRevision {
		writeDomainError(w, domain.ErrInvalidInput)
		return service.Actor{}, service.ConfigChangeRequest{}, false
	}
	if len(body.Changes) == 0 || len(body.Changes) > len(domain.EditableConfigDefinitions(document)) {
		writeDomainError(w, domain.ErrInvalidInput)
		return service.Actor{}, service.ConfigChangeRequest{}, false
	}
	values := make(map[domain.ConfigKey]json.RawMessage, len(body.Changes))
	for key, raw := range body.Changes {
		values[domain.ConfigKey(key)] = raw
	}
	return actor, service.ConfigChangeRequest{
		Document:         document,
		ExpectedRevision: int(revision),
		Reason:           body.Reason,
		Values:           values,
	}, true
}

func writeConfigResult(w http.ResponseWriter, result service.ConfigApplyResult) {
	values := make(map[string]domain.ConfigValue, len(result.State.Values))
	for key, value := range result.State.Values {
		values[string(key)] = value
	}
	payload := map[string]any{
		"applied":  result.Applied,
		"document": string(result.State.Document),
		"revision": result.State.Revision,
		"values":   values,
		"plan":     newConfigPlanPayload(result.Plan),
	}
	if result.Applied {
		payload["version"] = newConfigVersionPayload(result.Version)
	}
	httputil.WriteJSON(w, http.StatusOK, payload)
}

// newConfigSettingPayload projects one setting for the wire.
//
// The sensitive branch returns before any value is read: there is no path
// through this function that assigns a credential to Value, and that is what
// the security suite asserts against the whole catalog rather than one field at
// a time.
func newConfigSettingPayload(setting service.ConfigSetting) configSettingPayload {
	definition := setting.Definition
	payload := configSettingPayload{
		Key:            string(definition.Key),
		Label:          definition.Label,
		Description:    definition.Description,
		Category:       string(definition.Category),
		OwnerService:   definition.OwnerService,
		Class:          string(definition.Class),
		Source:         string(definition.Source),
		Apply:          string(definition.Apply),
		Type:           string(definition.Type),
		Unit:           definition.Unit,
		Nullable:       definition.Nullable,
		Editable:       definition.Editable,
		ReadOnlyReason: definition.ReadOnlyReason,
		Sensitive:      definition.Sensitive,
		DangerNote:     definition.DangerNote,
		Rollbackable:   definition.Rollbackable(),
		EnvVar:         definition.EnvVar,
		Observable:     setting.Observable,
	}
	if definition.Editable {
		payload.Document = string(definition.Document)
		payload.ManageCapability = string(definition.ManageCapability)
		defaultValue := definition.Default
		payload.Default = &defaultValue
		if definition.Type == domain.ConfigTypeInt {
			minimum, maximum := definition.Min, definition.Max
			payload.Min, payload.Max = &minimum, &maximum
		}
	}
	if definition.Sensitive {
		configured := setting.Configured
		payload.Configured = &configured
		return payload
	}
	if setting.Observable {
		value := setting.Value
		payload.Value = &value
	}
	return payload
}

func newConfigPlanPayload(plan service.ConfigPlan) configPlanPayload {
	changes := make([]configDiffPayload, 0, len(plan.Changes))
	for _, change := range plan.Changes {
		changes = append(changes, newConfigDiffPayload(change))
	}
	failures := make([]configErrorPayload, 0, len(plan.Errors))
	for _, failure := range plan.Errors {
		failures = append(failures, configErrorPayload{Key: string(failure.Key), Message: failure.Message})
	}
	warnings := plan.Warnings
	if warnings == nil {
		warnings = []string{}
	}
	services := plan.AffectedServices
	if services == nil {
		services = []string{}
	}
	return configPlanPayload{
		Document:           string(plan.Document),
		Revision:           plan.Revision,
		Stale:              plan.Stale,
		Superseded:         plan.Superseded,
		Changes:            changes,
		Dangerous:          plan.Dangerous,
		RequiredCapability: string(plan.RequiredCapability),
		Authorized:         plan.Authorized,
		ReasonRequired:     plan.ReasonRequired,
		Warnings:           warnings,
		Errors:             failures,
		AffectedServices:   services,
		Apply:              string(plan.Apply),
	}
}

// newConfigDiffPayload renders one transition.
//
// Both values are always present because no sensitive setting can reach a
// diff: the registry refuses an editable sensitive definition, so a change set
// cannot contain one. If that ever changes, this function must grow a branch
// that reports the replacement without either value — and the catalog
// invariant test is what forces that decision to be made deliberately.
func newConfigDiffPayload(change domain.ConfigChange) configDiffPayload {
	definition, ok := domain.LookupConfig(change.Key)
	payload := configDiffPayload{
		Key:  string(change.Key),
		From: change.From,
		To:   change.To,
	}
	if !ok {
		// A key the registry no longer declares can still appear in history.
		// It is rendered as itself rather than dropped, because hiding a
		// recorded change is worse than showing one without its label.
		payload.Label = string(change.Key)
		return payload
	}
	payload.Label = definition.Label
	payload.Category = string(definition.Category)
	payload.OwnerService = definition.OwnerService
	payload.Apply = string(definition.Apply)
	payload.Unit = definition.Unit
	payload.Dangerous = definition.DangerousValue(change.To)
	if payload.Dangerous {
		payload.DangerNote = definition.DangerNote
	}
	return payload
}

func newConfigVersionPayload(version domain.ConfigVersion) configVersionPayload {
	changes := make([]configDiffPayload, 0, len(version.Changes))
	for _, change := range version.Changes {
		changes = append(changes, newConfigDiffPayload(change))
	}
	return configVersionPayload{
		ID:              strconv.FormatInt(version.ID, 10),
		Document:        string(version.Document),
		Revision:        version.Revision,
		AppliedAt:       version.AppliedAt,
		ActorUserID:     version.ActorUserID,
		ActorEmail:      version.ActorEmail,
		CorrelationID:   version.CorrelationID,
		Reason:          version.Reason,
		RevertsRevision: version.RevertsRevision,
		Rollbackable:    domain.ConfigVersionRollbackable(version),
		Changes:         changes,
	}
}
