package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
	"github.com/nicrepository/nchat/services/admin-service/internal/service"
)

// The integration surface of the Admin API (issue #582).
//
// Three endpoints, and what is *not* in them is the part worth reading: no
// endpoint accepts a URL, a hostname, an IP address, a port, a credential or a
// destination of any kind. The listing takes no parameter at all; the
// diagnostic takes one path segment, resolved against the closed registry in
// domain before anything is read from it; the test message takes an empty body
// and delivers to the authenticated administrator's own address.
//
// That is what keeps "Testar configuração" from becoming a proxy: the set of
// things this pod is willing to contact comes from its own environment, and a
// caller can only choose which of them to contact, never what they are.

// IntegrationAdmin is the integration surface the routes drive.
type IntegrationAdmin interface {
	List(ctx context.Context, actor service.Actor) (service.IntegrationsView, error)
	Diagnose(ctx context.Context, actor service.Actor, id domain.IntegrationID) (domain.DiagnosticReport, error)
	SendTestEmail(ctx context.Context, actor service.Actor) (domain.DiagnosticReport, error)
}

type integrationActionPayload struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Capability  string `json:"capability"`
}

// integrationSettingPayload is one configuration key attached to an
// integration.
//
// It embeds the very same projection the configuration endpoint serves, so the
// rule that a credential arrives as `configured` and never as `value` is
// enforced by one function for both surfaces. A second projection here would be
// a second place for that rule to be got wrong.
type integrationSettingPayload struct {
	configSettingPayload
	Advanced bool `json:"advanced"`
}

type integrationPayload struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Summary     string `json:"summary"`
	Category    string `json:"category"`
	RunbookPath string `json:"runbook_path"`
	// HealthServiceID links the card to its Health Center row.
	HealthServiceID string `json:"health_service_id"`
	State           string `json:"state"`
	Enabled         bool   `json:"enabled"`
	Observable      bool   `json:"observable"`
	// LatencyMS is absent when no round trip was measured; it is never zero as
	// a stand-in for "not measured".
	LatencyMS     *int64    `json:"latency_ms,omitempty"`
	CheckedAt     time.Time `json:"checked_at"`
	ErrorCategory string    `json:"error_category,omitempty"`
	Detail        string    `json:"detail,omitempty"`
	Version       string    `json:"version,omitempty"`

	Diagnosable           bool     `json:"diagnosable"`
	DiagnosticUnsupported string   `json:"diagnostic_unsupported,omitempty"`
	Stages                []string `json:"stages"`

	SettingsVisible bool                        `json:"settings_visible"`
	Settings        []integrationSettingPayload `json:"settings"`
	Actions         []integrationActionPayload  `json:"actions"`
}

type diagnosticStepPayload struct {
	Stage     string `json:"stage"`
	Status    string `json:"status"`
	Category  string `json:"category,omitempty"`
	Detail    string `json:"detail,omitempty"`
	LatencyMS *int64 `json:"latency_ms,omitempty"`
}

type diagnosticReportPayload struct {
	Integration string                  `json:"integration"`
	StartedAt   time.Time               `json:"started_at"`
	Status      string                  `json:"status"`
	Summary     string                  `json:"summary"`
	Version     string                  `json:"version,omitempty"`
	Steps       []diagnosticStepPayload `json:"steps"`
}

// ListIntegrations serves every declared integration with its passive status.
//
// Requires admin.integrations.read. It is a read and it is guarded like a
// write, for the same reason the configuration catalogue is: the list names
// every integration the deployment has and whether each one is reachable, which
// is reconnaissance for anyone who should not be holding this session.
//
// It contacts nothing. The status comes from the shared health snapshot the
// Health Center already collected.
func ListIntegrations(integrations IntegrationAdmin) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if integrations == nil {
			writeUnavailable(w)
			return
		}
		actor, ok := actorFrom(w, r)
		if !ok {
			return
		}
		view, err := integrations.List(r.Context(), actor)
		if err != nil {
			writeDomainError(w, err)
			return
		}
		payload := make([]integrationPayload, 0, len(view.Integrations))
		for _, status := range view.Integrations {
			payload = append(payload, newIntegrationPayload(status))
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{
			"collected_at": view.CollectedAt,
			"integrations": payload,
		})
	})
}

// DiagnoseIntegration runs one integration's active check.
//
// Requires admin.integrations.manage — the manage capability and not the read
// one, even though nothing is written. A diagnostic makes this pod open
// outbound connections and, for LiveKit, sign a credential; that is an action
// with a cost, and the capability that authorizes it should be the one an
// operator grants deliberately.
//
// It is a POST with no body, so it passes the origin and CSRF guards like every
// other non-safe method. A failed check answers 200 with the report: "o relay
// recusou a credencial" is the result the operator asked for, not a server
// fault, and turning it into a 502 would lose every stage that did pass.
func DiagnoseIntegration(integrations IntegrationAdmin) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if integrations == nil {
			writeUnavailable(w)
			return
		}
		actor, ok := actorFrom(w, r)
		if !ok {
			return
		}
		// Before anything is resolved and long before anything is dialled: a
		// request that breaks the contract must not spend a diagnostic slot,
		// a rate-limit token or an outbound connection.
		if !requireEmptyBody(w, r) {
			return
		}
		id, known := parseIntegrationID(r.PathValue("integrationID"))
		if !known {
			writeDomainError(w, domain.ErrNotFound)
			return
		}
		report, err := integrations.Diagnose(r.Context(), actor, id)
		if err != nil {
			writeDomainError(w, err)
			return
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"report": newDiagnosticReportPayload(report)})
	})
}

// SendIntegrationTestEmail delivers one fixed message through the relay.
//
// Requires admin.integrations.manage, passes CSRF like every mutation, and
// accepts no body at all. The destination is the authenticated administrator's
// own address, read from the session principal: there is no recipient field to
// send, so there is nothing for a stolen session to aim.
func SendIntegrationTestEmail(integrations IntegrationAdmin) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if integrations == nil {
			writeUnavailable(w)
			return
		}
		actor, ok := actorFrom(w, r)
		if !ok {
			return
		}
		// Before the relay is contacted: a request that breaks the contract
		// must not put a message in anybody's inbox.
		if !requireEmptyBody(w, r) {
			return
		}
		report, err := integrations.SendTestEmail(r.Context(), actor)
		if err != nil {
			writeDomainError(w, err)
			return
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"report": newDiagnosticReportPayload(report)})
	})
}

// parseIntegrationID resolves the one path parameter this surface takes.
//
// Against the registry, before it is used for anything: an identifier the
// platform does not declare is a 404, never a target and never a filter that
// silently matches nothing.
func parseIntegrationID(raw string) (domain.IntegrationID, bool) {
	id := domain.IntegrationID(raw)
	if _, ok := domain.LookupIntegration(id); !ok {
		return "", false
	}
	return id, true
}

func newIntegrationPayload(status service.IntegrationStatus) integrationPayload {
	descriptor := status.Descriptor
	payload := integrationPayload{
		ID:                    string(descriptor.ID),
		DisplayName:           descriptor.DisplayName,
		Summary:               descriptor.Summary,
		Category:              string(descriptor.Category),
		RunbookPath:           descriptor.RunbookPath,
		HealthServiceID:       string(descriptor.HealthService),
		Diagnosable:           status.Diagnosable,
		DiagnosticUnsupported: descriptor.DiagnosticUnsupported,
		Stages:                stageNames(descriptor.Stages),
		SettingsVisible:       status.SettingsVisible,
		Settings:              newIntegrationSettings(status.Settings),
		Actions:               newIntegrationActions(descriptor.Actions),
	}
	applyHealth(&payload, status.Health)
	return payload
}

// applyHealth copies the passive result onto the card.
//
// A descriptor with no collected row keeps the zero state, which the registry
// forbids from ever being rendered as healthy: domain.HealthUnknown is the zero
// value's meaning here, and it is set explicitly rather than left blank so the
// console never has to interpret an empty string.
func applyHealth(payload *integrationPayload, health domain.ServiceHealth) {
	payload.State = string(domain.HealthUnknown)
	if health.State != "" {
		payload.State = string(health.State)
	}
	payload.Enabled = health.Enabled
	payload.Observable = health.Observable
	payload.LatencyMS = health.LatencyMS
	payload.CheckedAt = health.CheckedAt
	payload.ErrorCategory = string(health.ErrorCategory)
	payload.Detail = health.Detail
	payload.Version = health.Version
}

func newIntegrationSettings(settings []service.IntegrationSetting) []integrationSettingPayload {
	payload := make([]integrationSettingPayload, 0, len(settings))
	for _, setting := range settings {
		payload = append(payload, integrationSettingPayload{
			configSettingPayload: newConfigSettingPayload(setting.Setting),
			Advanced:             setting.Advanced,
		})
	}
	return payload
}

func newIntegrationActions(actions []domain.IntegrationAction) []integrationActionPayload {
	payload := make([]integrationActionPayload, 0, len(actions))
	for _, action := range actions {
		payload = append(payload, integrationActionPayload{
			ID:          string(action.ID),
			Label:       action.Label,
			Description: action.Description,
			Capability:  string(action.Capability),
		})
	}
	return payload
}

func stageNames(stages []domain.DiagnosticStage) []string {
	names := make([]string, 0, len(stages))
	for _, stage := range stages {
		names = append(names, string(stage))
	}
	return names
}

func newDiagnosticReportPayload(report domain.DiagnosticReport) diagnosticReportPayload {
	steps := make([]diagnosticStepPayload, 0, len(report.Steps))
	for _, step := range report.Steps {
		steps = append(steps, diagnosticStepPayload{
			Stage:     string(step.Stage),
			Status:    string(step.Status),
			Category:  string(step.Category),
			Detail:    step.Detail,
			LatencyMS: step.LatencyMS,
		})
	}
	return diagnosticReportPayload{
		Integration: string(report.Integration),
		StartedAt:   report.StartedAt,
		Status:      string(report.Status),
		Summary:     report.Summary,
		Version:     report.Version,
		Steps:       steps,
	}
}
