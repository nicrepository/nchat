package service

import (
	"context"
	"strconv"

	"github.com/nicrepository/nchat/libs/go/platform/antispampolicy"
	"github.com/nicrepository/nchat/libs/go/platform/uploadpolicy"
	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
)

// PolicyStore is the persistence the operational policy surface needs.
type PolicyStore interface {
	ListAntiSpamPolicies(ctx context.Context, cursor domain.Cursor, limit int) (domain.Page[domain.AntiSpamPolicy], error)
	ListUploadPolicies(ctx context.Context, cursor domain.Cursor, limit int) (domain.Page[domain.UploadPolicy], error)
	UpdateAntiSpamPolicy(ctx context.Context, workspaceID string, value int) (domain.AntiSpamPolicy, domain.PolicyChange, error)
	UpdateUploadPolicy(ctx context.Context, workspaceID string, value int64) (domain.UploadPolicy, domain.PolicyChange, error)
}

// PolicyService is the operational policy surface.
//
// It covers exactly the two policies that are configurable at runtime today:
// RF-19's per-user message budget and RF-32's attachment size limit. Both are
// columns on chat.workspaces with a database CHECK, read by the enforcing
// service on the path that enforces them, so an administrative change takes
// effect without a restart and without this service pretending it did.
//
// Everything else an operator might want to tune — burst windows, reaction and
// upload rate limits, link-scan budgets, malware scanner behaviour, MIME
// handling, gateway body caps — is read from the environment at boot by the
// service that enforces it. None of it is editable here, and the console says
// so rather than offering a field that would save a number nobody reads.
type PolicyService struct {
	store PolicyStore
	audit Recorder
}

func NewPolicyService(store PolicyStore, audit Recorder) *PolicyService {
	return &PolicyService{store: store, audit: audit}
}

func (s *PolicyService) ListAntiSpam(ctx context.Context, cursor domain.Cursor, limit int) (domain.Page[domain.AntiSpamPolicy], error) {
	if s == nil || s.store == nil {
		return domain.Page[domain.AntiSpamPolicy]{}, domain.ErrUnavailable
	}
	return s.store.ListAntiSpamPolicies(ctx, cursor, limit)
}

func (s *PolicyService) ListUpload(ctx context.Context, cursor domain.Cursor, limit int) (domain.Page[domain.UploadPolicy], error) {
	if s == nil || s.store == nil {
		return domain.Page[domain.UploadPolicy]{}, domain.ErrUnavailable
	}
	return s.store.ListUploadPolicies(ctx, cursor, limit)
}

// UpdateAntiSpam writes a new RF-19 limit for one workspace.
//
// The bounds come from libs/go/platform/antispampolicy, which is also where
// chat-service's own admin endpoint and the migration's CHECK get them, so the
// two writers of this column cannot disagree about what is allowed. A value
// outside them is refused: nothing here clamps, rounds or falls back, because a
// silently corrected limit is a policy the administrator never chose.
//
// Zero is not a special case and not "disabled" — it is simply below the
// minimum, which is 1 by design so an anti-spam control can never double as a
// workspace mute.
func (s *PolicyService) UpdateAntiSpam(ctx context.Context, actor Actor, workspaceID string, value int) (domain.AntiSpamPolicy, error) {
	if s == nil || s.store == nil {
		return domain.AntiSpamPolicy{}, domain.ErrUnavailable
	}
	policy, change, err := s.updateAntiSpam(ctx, workspaceID, value)
	record(ctx, s.audit, actor, domain.AuditActionPolicyAntiSpam, "admin.workspace:"+workspaceID, resultFor(err), map[string]string{
		"workspace_id":  workspaceID,
		"field":         "message_rate_limit_per_minute",
		"unit":          "messages_per_minute",
		"from":          strconv.FormatInt(change.From, 10),
		"to":            strconv.FormatInt(change.To, 10),
		"requested":     strconv.Itoa(value),
		"allowed_range": strconv.Itoa(antispampolicy.Min) + ".." + strconv.Itoa(antispampolicy.Max),
	})
	return policy, err
}

func (s *PolicyService) updateAntiSpam(ctx context.Context, workspaceID string, value int) (domain.AntiSpamPolicy, domain.PolicyChange, error) {
	if !domain.ValidUUID(workspaceID) {
		return domain.AntiSpamPolicy{}, domain.PolicyChange{}, domain.ErrInvalidInput
	}
	if !antispampolicy.Valid(value) {
		return domain.AntiSpamPolicy{}, domain.PolicyChange{}, domain.ErrInvalidInput
	}
	return s.store.UpdateAntiSpamPolicy(ctx, workspaceID, value)
}

// UpdateUpload writes a new RF-32 attachment size limit for one workspace, in
// bytes.
//
// uploadpolicy.Valid is the single definition of what is acceptable: inside the
// bounds and an exact whole number of MiB. The whole-MiB rule is not
// cosmetic — the console edits MiB, so a stored value that is not a whole MiB
// could not be shown without being changed, and an ordinary save would then
// write a limit the administrator never typed. Such a value is refused, never
// rounded.
//
// Overflow cannot arrive here as a wrapped number: the HTTP layer parses the
// field as a JSON integer into int64 and refuses anything that does not fit, so
// a value too large to represent is a 400 rather than a small positive limit.
func (s *PolicyService) UpdateUpload(ctx context.Context, actor Actor, workspaceID string, value int64) (domain.UploadPolicy, error) {
	if s == nil || s.store == nil {
		return domain.UploadPolicy{}, domain.ErrUnavailable
	}
	policy, change, err := s.updateUpload(ctx, workspaceID, value)
	record(ctx, s.audit, actor, domain.AuditActionPolicyUpload, "admin.workspace:"+workspaceID, resultFor(err), map[string]string{
		"workspace_id":  workspaceID,
		"field":         "max_upload_bytes",
		"unit":          "bytes",
		"from":          strconv.FormatInt(change.From, 10),
		"to":            strconv.FormatInt(change.To, 10),
		"requested":     strconv.FormatInt(value, 10),
		"allowed_range": strconv.FormatInt(uploadpolicy.MinMaxUploadBytes, 10) + ".." + strconv.FormatInt(uploadpolicy.MaxMaxUploadBytes, 10),
	})
	return policy, err
}

func (s *PolicyService) updateUpload(ctx context.Context, workspaceID string, value int64) (domain.UploadPolicy, domain.PolicyChange, error) {
	if !domain.ValidUUID(workspaceID) {
		return domain.UploadPolicy{}, domain.PolicyChange{}, domain.ErrInvalidInput
	}
	if !uploadpolicy.Valid(value) {
		return domain.UploadPolicy{}, domain.PolicyChange{}, domain.ErrInvalidInput
	}
	return s.store.UpdateUploadPolicy(ctx, workspaceID, value)
}
