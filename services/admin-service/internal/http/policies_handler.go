package httpapi

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/nicrepository/nchat/libs/go/platform/antispampolicy"
	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/libs/go/platform/uploadpolicy"
	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
	"github.com/nicrepository/nchat/services/admin-service/internal/service"
)

// PolicyAdmin is the operational policy surface the routes drive.
type PolicyAdmin interface {
	ListAntiSpam(ctx context.Context, cursor domain.Cursor, limit int) (domain.Page[domain.AntiSpamPolicy], error)
	ListUpload(ctx context.Context, cursor domain.Cursor, limit int) (domain.Page[domain.UploadPolicy], error)
	UpdateAntiSpam(ctx context.Context, actor service.Actor, workspaceID string, value int) (domain.AntiSpamPolicy, error)
	UpdateUpload(ctx context.Context, actor service.Actor, workspaceID string, value int64) (domain.UploadPolicy, error)
}

type workspaceRefPayload struct {
	ID     string `json:"id"`
	Slug   string `json:"slug"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

type antiSpamPolicyPayload struct {
	Workspace                 workspaceRefPayload `json:"workspace"`
	MessageRateLimitPerMinute int                 `json:"message_rate_limit_per_minute"`
}

type uploadPolicyPayload struct {
	Workspace      workspaceRefPayload `json:"workspace"`
	MaxUploadBytes int64               `json:"max_upload_bytes"`
}

// policyBoundsPayload travels with every policy response so the console renders
// and validates against the server's numbers instead of restating them.
//
// The unit is named explicitly. A limit whose unit the screen has to guess is
// how "60" becomes per-second on one side and per-minute on the other.
type policyBoundsPayload struct {
	Min     int64  `json:"min"`
	Max     int64  `json:"max"`
	Default int64  `json:"default"`
	Unit    string `json:"unit"`
	// Step, when non-zero, is the granularity every accepted value must be an
	// exact multiple of. It is the RF-32 whole-MiB rule, published rather than
	// re-derived by the client: a value off the step is refused, never rounded.
	Step int64 `json:"step,omitempty"`
}

var antiSpamBounds = policyBoundsPayload{
	Min:     antispampolicy.Min,
	Max:     antispampolicy.Max,
	Default: antispampolicy.Default,
	Unit:    "messages_per_minute",
}

var uploadBounds = policyBoundsPayload{
	Min:     uploadpolicy.MinMaxUploadBytes,
	Max:     uploadpolicy.MaxMaxUploadBytes,
	Default: uploadpolicy.DefaultMaxUploadBytes,
	Unit:    "bytes",
	Step:    uploadpolicy.BytesPerMiB,
}

// deploymentManagedUploadControls names the upload-related protections that are
// real, are enforced, and are *not* editable from here.
//
// The list is short because it is honest. These two are read from the
// environment at boot by file-service; changing either is a rollout, not a
// click, so the console shows them as not applicable rather than offering a
// field that would store a number nobody reads. Nothing that does not exist in
// the platform today appears here — there is no MIME allowlist, no configurable
// preview ceiling and no scanner-behaviour switch to advertise.
var deploymentManagedUploadControls = []string{"malware_scanning", "upload_concurrency"}

// ListAntiSpamPolicies serves the RF-19 policy of every workspace.
//
// Requires admin.security.read.
func ListAntiSpamPolicies(policies PolicyAdmin) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if policies == nil {
			writeUnavailable(w)
			return
		}
		limit, cursor, err := parsePageParams(r.URL.Query())
		if err != nil {
			writeInvalidQuery(w)
			return
		}
		page, err := policies.ListAntiSpam(r.Context(), cursor, limit)
		if err != nil {
			writeDomainError(w, err)
			return
		}
		payload := make([]antiSpamPolicyPayload, 0, len(page.Items))
		for _, policy := range page.Items {
			payload = append(payload, newAntiSpamPayload(policy))
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{
			"policies":   payload,
			"bounds":     antiSpamBounds,
			"pagination": newPagination(page.NextCursor),
		})
	})
}

type antiSpamRequest struct {
	MessageRateLimitPerMinute json.RawMessage `json:"message_rate_limit_per_minute"`
}

// UpdateAntiSpamPolicy writes one workspace's RF-19 limit.
//
// Requires admin.security.manage. The value is parsed as a JSON integer, so a
// decimal, a string, a float in exponent notation, null and a number too large
// for int64 are all refused rather than coerced into a plausible policy. The
// range check happens in the service against the shared bounds, and the column
// CHECK is the backstop; nothing on this path clamps or rounds.
//
// Propagation is chat-service's, unchanged: the workspace's cached policy is at
// most five seconds stale on instances other than the one that served a change,
// which is the same window a workspace administrator's own edit has. Nothing
// here claims a hot reload the platform does not perform.
func UpdateAntiSpamPolicy(policies PolicyAdmin) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if policies == nil {
			writeUnavailable(w)
			return
		}
		actor, ok := actorFrom(w, r)
		if !ok {
			return
		}
		var body antiSpamRequest
		if !decodeJSONBody(w, r, &body) {
			return
		}
		value, ok := integerField(body.MessageRateLimitPerMinute)
		if !ok || value < int64(antispampolicy.Min) || value > int64(antispampolicy.Max) {
			writeDomainError(w, domain.ErrInvalidInput)
			return
		}
		policy, err := policies.UpdateAntiSpam(r.Context(), actor, r.PathValue("workspaceID"), int(value))
		if err != nil {
			writeDomainError(w, err)
			return
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{
			"policy": newAntiSpamPayload(policy),
			"bounds": antiSpamBounds,
		})
	})
}

// ListUploadPolicies serves the RF-32 policy of every workspace, plus the
// upload controls this console cannot change.
//
// Requires admin.infrastructure.read.
func ListUploadPolicies(policies PolicyAdmin) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if policies == nil {
			writeUnavailable(w)
			return
		}
		limit, cursor, err := parsePageParams(r.URL.Query())
		if err != nil {
			writeInvalidQuery(w)
			return
		}
		page, err := policies.ListUpload(r.Context(), cursor, limit)
		if err != nil {
			writeDomainError(w, err)
			return
		}
		payload := make([]uploadPolicyPayload, 0, len(page.Items))
		for _, policy := range page.Items {
			payload = append(payload, newUploadPayload(policy))
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{
			"policies": payload,
			"bounds":   uploadBounds,
			// The gateway cap is a static infrastructure ceiling, not this
			// policy. It is published so the console can say why a workspace
			// limit can never exceed it, and it is derived from the same
			// constant the gateway configuration is, so the two cannot drift.
			"gateway_hard_cap_bytes": uploadpolicy.GatewayHardCapBytes,
			"deployment_managed":     deploymentManagedUploadControls,
			"pagination":             newPagination(page.NextCursor),
		})
	})
}

type uploadRequest struct {
	MaxUploadBytes json.RawMessage `json:"max_upload_bytes"`
}

// UpdateUploadPolicy writes one workspace's RF-32 attachment size limit, in
// bytes.
//
// Requires admin.infrastructure.manage. The unit is bytes on the wire and whole
// MiB in the console; the conversion happens at the presentation edge and is
// validated again here, so a client that computed it wrongly is refused rather
// than storing a limit the operator never chose. A value that is not an exact
// multiple of 1 MiB is a 400 — it is not rounded to the nearest one, because a
// stored limit that cannot be shown in the field that edits it turns the next
// ordinary save into a silent change.
//
// This limit narrows nothing that protects the platform. It cannot exceed the
// gateway's static cap, it cannot disable malware scanning, it cannot bypass
// admission control, and there is no value meaning "unlimited".
func UpdateUploadPolicy(policies PolicyAdmin) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if policies == nil {
			writeUnavailable(w)
			return
		}
		actor, ok := actorFrom(w, r)
		if !ok {
			return
		}
		var body uploadRequest
		if !decodeJSONBody(w, r, &body) {
			return
		}
		value, ok := integerField(body.MaxUploadBytes)
		if !ok || !uploadpolicy.Valid(value) {
			writeDomainError(w, domain.ErrInvalidInput)
			return
		}
		policy, err := policies.UpdateUpload(r.Context(), actor, r.PathValue("workspaceID"), value)
		if err != nil {
			writeDomainError(w, err)
			return
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{
			"policy": newUploadPayload(policy),
			"bounds": uploadBounds,
		})
	})
}

func newAntiSpamPayload(policy domain.AntiSpamPolicy) antiSpamPolicyPayload {
	return antiSpamPolicyPayload{
		Workspace:                 newWorkspaceRefPayload(policy.Workspace),
		MessageRateLimitPerMinute: policy.MessageRateLimitPerMinute,
	}
}

func newUploadPayload(policy domain.UploadPolicy) uploadPolicyPayload {
	return uploadPolicyPayload{
		Workspace:      newWorkspaceRefPayload(policy.Workspace),
		MaxUploadBytes: policy.MaxUploadBytes,
	}
}

func newWorkspaceRefPayload(workspace domain.WorkspaceRef) workspaceRefPayload {
	return workspaceRefPayload{
		ID:     workspace.ID,
		Slug:   workspace.Slug,
		Name:   workspace.Name,
		Status: workspace.Status,
	}
}
