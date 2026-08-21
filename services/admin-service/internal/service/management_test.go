package service_test

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"testing"

	"github.com/nicrepository/nchat/libs/go/platform/antispampolicy"
	"github.com/nicrepository/nchat/libs/go/platform/uploadpolicy"
	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
	"github.com/nicrepository/nchat/services/admin-service/internal/service"
)

const (
	actorID  = "11111111-1111-1111-1111-111111111111"
	targetID = "22222222-2222-2222-2222-222222222222"
)

func testActor() service.Actor {
	return service.Actor{UserID: actorID, CorrelationID: "req-1"}
}

// recordingAudit captures what reached the trail so a spec can assert both that
// an event was written and what it does not contain.
type recordingAudit struct{ events []domain.AuditEvent }

func (a *recordingAudit) Record(_ context.Context, event domain.AuditEvent) {
	a.events = append(a.events, event)
}

func (a *recordingAudit) last(t *testing.T) domain.AuditEvent {
	t.Helper()
	if len(a.events) == 0 {
		t.Fatal("expected an audit event, none was recorded")
	}
	return a.events[len(a.events)-1]
}

// userStore records what the service asked of it, and answers with whatever the
// spec set up. It never enforces an invariant itself: the point of these specs
// is what the service refuses *before* the store is reached.
type userStore struct {
	calls   []string
	change  domain.UserStatusChange
	revoked int
	err     error
}

func (s *userStore) ListUsers(context.Context, domain.AdminUserFilter) (domain.Page[domain.AdminUserSummary], error) {
	s.calls = append(s.calls, "list")
	return domain.Page[domain.AdminUserSummary]{}, s.err
}

func (s *userStore) GetUser(_ context.Context, userID string) (domain.AdminUserDetail, error) {
	s.calls = append(s.calls, "get:"+userID)
	return domain.AdminUserDetail{}, s.err
}

func (s *userStore) UpdateUserStatus(_ context.Context, userID, status string) (domain.UserStatusChange, error) {
	s.calls = append(s.calls, "status:"+userID+":"+status)
	if s.err != nil {
		return domain.UserStatusChange{}, s.err
	}
	change := s.change
	change.TargetUserID = userID
	change.ToStatus = status
	return change, nil
}

func (s *userStore) RevokeUserSessions(_ context.Context, userID string) (int, error) {
	s.calls = append(s.calls, "revoke:"+userID)
	return s.revoked, s.err
}

func (s *userStore) GrantAdminRole(_ context.Context, target, slug, by string) error {
	s.calls = append(s.calls, "grant:"+target+":"+slug+":"+by)
	return s.err
}

func (s *userStore) RevokeAdminRole(_ context.Context, target, slug string) error {
	s.calls = append(s.calls, "revoke-role:"+target+":"+slug)
	return s.err
}

// An administrator must not act on their own account through this console.
//
// Both halves matter: suspending yourself locks the operator out mid-task and
// leaves an audit row that reads as if somebody else did it, and revoking your
// own sessions ends the administrative session riding on one of them from
// inside the request that asked for it.
func TestUserAdminService_RefusesActingOnSelf(t *testing.T) {
	cases := map[string]func(*service.UserAdminService) error{
		"suspend self": func(s *service.UserAdminService) error {
			_, err := s.SetStatus(context.Background(), testActor(), actorID, domain.UserStatusSuspended)
			return err
		},
		"revoke own sessions": func(s *service.UserAdminService) error {
			_, err := s.RevokeSessions(context.Background(), testActor(), actorID)
			return err
		},
		"grant self a role": func(s *service.UserAdminService) error {
			return s.GrantRole(context.Background(), testActor(), actorID, "platform-superuser")
		},
		"revoke own role": func(s *service.UserAdminService) error {
			return s.RevokeRole(context.Background(), testActor(), actorID, "platform-superuser")
		},
	}
	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			store := &userStore{}
			audit := &recordingAudit{}
			if err := call(service.NewUserAdminService(store, audit)); !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("expected ErrForbidden, got %v", err)
			}
			if len(store.calls) != 0 {
				t.Fatalf("the store must not be reached, got %v", store.calls)
			}
			if event := audit.last(t); event.Result != domain.AuditResultDenied {
				t.Fatalf("a refused self-mutation is recorded as denied, got %q", event.Result)
			}
		})
	}
}

// Self-escalation is the vertical case and it is refused before any store call,
// so no chain of grants can end with the actor holding more than they started.
func TestUserAdminService_GrantRole_RecordsDeniedSelfEscalation(t *testing.T) {
	store := &userStore{}
	audit := &recordingAudit{}
	err := service.NewUserAdminService(store, audit).
		GrantRole(context.Background(), testActor(), actorID, "platform-superuser")
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
	event := audit.last(t)
	if event.Action != domain.AuditActionUserRoleGrant {
		t.Fatalf("unexpected action %q", event.Action)
	}
	if event.ActorUserID != actorID || event.CorrelationID != "req-1" {
		t.Fatalf("the actor and request must be recorded, got %+v", event)
	}
	if event.Metadata["role_slug"] != "platform-superuser" {
		t.Fatalf("the attempted role must be recorded, got %v", event.Metadata)
	}
}

func TestUserAdminService_RefusesMalformedInput(t *testing.T) {
	cases := map[string]func(*service.UserAdminService) error{
		"target is not a uuid": func(s *service.UserAdminService) error {
			_, err := s.SetStatus(context.Background(), testActor(), "not-a-uuid", domain.UserStatusSuspended)
			return err
		},
		"status is not one an administrator may set": func(s *service.UserAdminService) error {
			_, err := s.SetStatus(context.Background(), testActor(), targetID, "deleted")
			return err
		},
		"status is empty": func(s *service.UserAdminService) error {
			_, err := s.SetStatus(context.Background(), testActor(), targetID, "")
			return err
		},
		"role slug is not a slug": func(s *service.UserAdminService) error {
			return s.GrantRole(context.Background(), testActor(), targetID, "Platform Superuser")
		},
		"role slug is a wildcard": func(s *service.UserAdminService) error {
			return s.RevokeRole(context.Background(), testActor(), targetID, "%")
		},
	}
	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			store := &userStore{}
			if err := call(service.NewUserAdminService(store, &recordingAudit{})); !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("expected ErrInvalidInput, got %v", err)
			}
			if len(store.calls) != 0 {
				t.Fatalf("the store must not be reached, got %v", store.calls)
			}
		})
	}
}

func TestUserAdminService_SetStatus_RecordsTheTransitionAndItsEffect(t *testing.T) {
	store := &userStore{change: domain.UserStatusChange{FromStatus: "active", RevokedSessions: 3}}
	audit := &recordingAudit{}
	change, err := service.NewUserAdminService(store, audit).
		SetStatus(context.Background(), testActor(), targetID, domain.UserStatusSuspended)
	if err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	if change.RevokedSessions != 3 {
		t.Fatalf("the caller must learn how many sessions ended, got %d", change.RevokedSessions)
	}
	event := audit.last(t)
	if event.Result != domain.AuditResultSuccess || event.Action != domain.AuditActionUserStatusUpdate {
		t.Fatalf("unexpected event %+v", event)
	}
	// The suspension and the sessions it closed are one fact in the trail, not
	// two things an operator has to correlate by timestamp.
	if event.Metadata["from_status"] != "active" || event.Metadata["revoked_sessions"] != "3" {
		t.Fatalf("the diff and its effect must be recorded, got %v", event.Metadata)
	}
	if event.Resource != "admin.user:"+targetID {
		t.Fatalf("unexpected resource %q", event.Resource)
	}
}

// The grant carries the actor as granted_by, taken from the session and never
// from the request body.
func TestUserAdminService_GrantRole_AttributesTheGrantToTheActor(t *testing.T) {
	store := &userStore{}
	if err := service.NewUserAdminService(store, &recordingAudit{}).
		GrantRole(context.Background(), testActor(), targetID, "platform-auditor"); err != nil {
		t.Fatalf("GrantRole: %v", err)
	}
	want := "grant:" + targetID + ":platform-auditor:" + actorID
	if len(store.calls) != 1 || store.calls[0] != want {
		t.Fatalf("expected %q, got %v", want, store.calls)
	}
}

// A store refusal — the last-administrator invariant, for instance — is a
// denial in the trail, not an error: the platform said no, it did not break.
func TestUserAdminService_RevokeRole_StoreConflictIsADenial(t *testing.T) {
	store := &userStore{err: domain.ErrConflict}
	audit := &recordingAudit{}
	err := service.NewUserAdminService(store, audit).
		RevokeRole(context.Background(), testActor(), targetID, "platform-superuser")
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
	if event := audit.last(t); event.Result != domain.AuditResultDenied {
		t.Fatalf("expected a denial, got %q", event.Result)
	}
}

// A dependency failure is an error in the trail, so an outage never reads as an
// attack and an attack never reads as an outage.
func TestUserAdminService_StoreFailureIsAnError(t *testing.T) {
	store := &userStore{err: errors.New("connection refused")}
	audit := &recordingAudit{}
	_, err := service.NewUserAdminService(store, audit).
		SetStatus(context.Background(), testActor(), targetID, domain.UserStatusSuspended)
	if err == nil {
		t.Fatal("expected the failure to propagate")
	}
	if event := audit.last(t); event.Result != domain.AuditResultError {
		t.Fatalf("expected an error result, got %q", event.Result)
	}
}

func TestUserAdminService_NilStoreIsUnavailable(t *testing.T) {
	svc := service.NewUserAdminService(nil, nil)
	if _, err := svc.List(context.Background(), domain.AdminUserFilter{}); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
	if _, err := svc.Get(context.Background(), targetID); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
	if _, err := svc.SetStatus(context.Background(), testActor(), targetID, "active"); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
	if _, err := svc.RevokeSessions(context.Background(), testActor(), targetID); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
	if err := svc.GrantRole(context.Background(), testActor(), targetID, "platform-auditor"); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
	if err := svc.RevokeRole(context.Background(), testActor(), targetID, "platform-auditor"); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
}

func TestUserAdminService_GetRefusesMalformedIdentifier(t *testing.T) {
	store := &userStore{}
	if _, err := service.NewUserAdminService(store, nil).
		Get(context.Background(), "' OR 1=1 --"); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
	if len(store.calls) != 0 {
		t.Fatalf("the store must not be reached, got %v", store.calls)
	}
}

// ---------------------------------------------------------------------------
// Channels
// ---------------------------------------------------------------------------

type channelStore struct {
	calls      []string
	channel    domain.AdminChannelSummary
	candidates []domain.ChannelMemberCandidate
	membership domain.ChannelMembershipChange
	err        error
}

func (s *channelStore) ListChannels(context.Context, domain.AdminChannelFilter) (domain.Page[domain.AdminChannelSummary], error) {
	s.calls = append(s.calls, "list")
	return domain.Page[domain.AdminChannelSummary]{}, s.err
}

func (s *channelStore) GetChannel(_ context.Context, id string) (domain.AdminChannelDetail, error) {
	s.calls = append(s.calls, "get:"+id)
	return domain.AdminChannelDetail{}, s.err
}

func (s *channelStore) UpdateChannelStatus(_ context.Context, id, status string) (domain.AdminChannelSummary, error) {
	s.calls = append(s.calls, "status:"+id+":"+status)
	return s.channel, s.err
}

func (s *channelStore) ListMemberCandidates(_ context.Context, channelID, query string, limit int) ([]domain.ChannelMemberCandidate, error) {
	s.calls = append(s.calls, "candidates:"+channelID+":"+query+":"+strconv.Itoa(limit))
	return s.candidates, s.err
}

func (s *channelStore) AddChannelMembers(_ context.Context, channelID string, userIDs []string) (domain.ChannelMembershipChange, error) {
	s.calls = append(s.calls, "addMembers:"+channelID+":"+strconv.Itoa(len(userIDs)))
	if s.err != nil {
		return domain.ChannelMembershipChange{}, s.err
	}
	change := s.membership
	change.ChannelID = channelID
	return change, nil
}

func (s *channelStore) RemoveChannelMember(_ context.Context, channelID, userID string) (domain.ChannelMembershipChange, error) {
	s.calls = append(s.calls, "removeMember:"+channelID+":"+userID)
	if s.err != nil {
		return domain.ChannelMembershipChange{}, s.err
	}
	change := s.membership
	change.ChannelID = channelID
	return change, nil
}

func (s *channelStore) ListConversations(context.Context, domain.AdminConversationFilter) (domain.Page[domain.AdminConversationSummary], error) {
	s.calls = append(s.calls, "conversations")
	return domain.Page[domain.AdminConversationSummary]{}, s.err
}

func TestChannelAdminService_SetStatus_RefusesUnknownStatus(t *testing.T) {
	store := &channelStore{}
	// 'deleted' is not a channel state this platform has, and it must not
	// become one by being accepted here.
	if _, err := service.NewChannelAdminService(store, &recordingAudit{}).
		SetStatus(context.Background(), testActor(), targetID, "deleted"); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
	if len(store.calls) != 0 {
		t.Fatalf("the store must not be reached, got %v", store.calls)
	}
}

func TestChannelAdminService_SetStatus_RecordsTheChange(t *testing.T) {
	store := &channelStore{channel: domain.AdminChannelSummary{ID: targetID, WorkspaceID: actorID, Status: "archived"}}
	audit := &recordingAudit{}
	if _, err := service.NewChannelAdminService(store, audit).
		SetStatus(context.Background(), testActor(), targetID, domain.ChannelStatusArchived); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	event := audit.last(t)
	if event.Action != domain.AuditActionChannelStatus || event.Result != domain.AuditResultSuccess {
		t.Fatalf("unexpected event %+v", event)
	}
	if event.Metadata["requested_status"] != "archived" || event.Metadata["workspace_id"] != actorID {
		t.Fatalf("unexpected metadata %v", event.Metadata)
	}
}

// #geral is immutable in chat-service, and this console must not become a
// second way around that: the store's refusal is surfaced, not softened.
func TestChannelAdminService_SetStatus_PropagatesGeneralChannelRefusal(t *testing.T) {
	store := &channelStore{err: domain.ErrForbidden}
	if _, err := service.NewChannelAdminService(store, &recordingAudit{}).
		SetStatus(context.Background(), testActor(), targetID, domain.ChannelStatusArchived); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
}

func TestChannelAdminService_NilStoreIsUnavailable(t *testing.T) {
	svc := service.NewChannelAdminService(nil, nil)
	if _, err := svc.List(context.Background(), domain.AdminChannelFilter{}); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
	if _, err := svc.Get(context.Background(), targetID); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
	if _, err := svc.ListConversations(context.Background(), domain.AdminConversationFilter{}); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
	if _, err := svc.SetStatus(context.Background(), testActor(), targetID, "archived"); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
}

func TestChannelAdminService_GetRefusesMalformedIdentifier(t *testing.T) {
	store := &channelStore{}
	if _, err := service.NewChannelAdminService(store, nil).Get(context.Background(), "nope"); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
	if len(store.calls) != 0 {
		t.Fatalf("the store must not be reached, got %v", store.calls)
	}
}

// ---------------------------------------------------------------------------
// Policies
// ---------------------------------------------------------------------------

type policyStore struct {
	calls    []string
	antiSpam int
	upload   int64
	err      error
}

func (s *policyStore) ListAntiSpamPolicies(context.Context, domain.Cursor, int) (domain.Page[domain.AntiSpamPolicy], error) {
	s.calls = append(s.calls, "list-antispam")
	return domain.Page[domain.AntiSpamPolicy]{}, s.err
}

func (s *policyStore) ListUploadPolicies(context.Context, domain.Cursor, int) (domain.Page[domain.UploadPolicy], error) {
	s.calls = append(s.calls, "list-upload")
	return domain.Page[domain.UploadPolicy]{}, s.err
}

func (s *policyStore) UpdateAntiSpamPolicy(_ context.Context, workspaceID string, value int) (domain.AntiSpamPolicy, domain.PolicyChange, error) {
	s.calls = append(s.calls, "antispam:"+workspaceID)
	if s.err != nil {
		return domain.AntiSpamPolicy{}, domain.PolicyChange{}, s.err
	}
	return domain.AntiSpamPolicy{
			Workspace:                 domain.WorkspaceRef{ID: workspaceID},
			MessageRateLimitPerMinute: value,
		}, domain.PolicyChange{
			WorkspaceID: workspaceID, From: int64(s.antiSpam), To: int64(value),
		}, nil
}

func (s *policyStore) UpdateUploadPolicy(_ context.Context, workspaceID string, value int64) (domain.UploadPolicy, domain.PolicyChange, error) {
	s.calls = append(s.calls, "upload:"+workspaceID)
	if s.err != nil {
		return domain.UploadPolicy{}, domain.PolicyChange{}, s.err
	}
	return domain.UploadPolicy{
			Workspace:      domain.WorkspaceRef{ID: workspaceID},
			MaxUploadBytes: value,
		}, domain.PolicyChange{
			WorkspaceID: workspaceID, From: s.upload, To: value,
		}, nil
}

// The bounds are the guardrail, and none of these values may reach the database
// — not clamped, not rounded, not truncated.
func TestPolicyService_UpdateAntiSpam_RefusesValuesOutsideTheBounds(t *testing.T) {
	cases := map[string]int{
		"zero is not a policy":       0,
		"negative is not a policy":   -1,
		"below the minimum":          antispampolicy.Min - 1,
		"above the maximum":          antispampolicy.Max + 1,
		"absurdly large":             1 << 30,
		"absurdly negative":          -(1 << 30),
		"one past the maximum again": antispampolicy.Max + 1,
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			store := &policyStore{}
			_, err := service.NewPolicyService(store, &recordingAudit{}).
				UpdateAntiSpam(context.Background(), testActor(), targetID, value)
			if !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("expected ErrInvalidInput for %d, got %v", value, err)
			}
			if len(store.calls) != 0 {
				t.Fatalf("a refused value must not reach the database, got %v", store.calls)
			}
		})
	}
}

func TestPolicyService_UpdateAntiSpam_RecordsTheDiffAndTheUnit(t *testing.T) {
	store := &policyStore{antiSpam: 60}
	audit := &recordingAudit{}
	policy, err := service.NewPolicyService(store, audit).
		UpdateAntiSpam(context.Background(), testActor(), targetID, 30)
	if err != nil {
		t.Fatalf("UpdateAntiSpam: %v", err)
	}
	if policy.MessageRateLimitPerMinute != 30 {
		t.Fatalf("expected the stored value back, got %d", policy.MessageRateLimitPerMinute)
	}
	event := audit.last(t)
	if event.Action != domain.AuditActionPolicyAntiSpam || event.Result != domain.AuditResultSuccess {
		t.Fatalf("unexpected event %+v", event)
	}
	// A configuration diff is only useful if it names the unit: "60 to 30" is
	// ambiguous without it.
	if event.Metadata["from"] != "60" || event.Metadata["to"] != "30" || event.Metadata["unit"] != "messages_per_minute" {
		t.Fatalf("unexpected metadata %v", event.Metadata)
	}
}

func TestPolicyService_UpdateUpload_RefusesValuesOutsideTheBounds(t *testing.T) {
	cases := map[string]int64{
		"zero disables attachments through a size control": 0,
		"negative":                    -1,
		"below the minimum":           uploadpolicy.MinMaxUploadBytes - 1,
		"above the maximum":           uploadpolicy.MaxMaxUploadBytes + 1,
		"not a whole MiB":             uploadpolicy.MinMaxUploadBytes + 1,
		"one and a half MiB":          uploadpolicy.BytesPerMiB + uploadpolicy.BytesPerMiB/2,
		"absurd, near the int64 roof": 1 << 62,
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			store := &policyStore{}
			_, err := service.NewPolicyService(store, &recordingAudit{}).
				UpdateUpload(context.Background(), testActor(), targetID, value)
			if !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("expected ErrInvalidInput for %d, got %v", value, err)
			}
			if len(store.calls) != 0 {
				t.Fatalf("a refused value must not reach the database, got %v", store.calls)
			}
		})
	}
}

// A value off the MiB grid is refused, never rounded to the nearest acceptable
// one: rounding would let an ordinary save write a limit nobody typed.
func TestPolicyService_UpdateUpload_AcceptsExactMiBOnly(t *testing.T) {
	store := &policyStore{upload: uploadpolicy.DefaultMaxUploadBytes}
	audit := &recordingAudit{}
	value := 100 * uploadpolicy.BytesPerMiB
	policy, err := service.NewPolicyService(store, audit).
		UpdateUpload(context.Background(), testActor(), targetID, value)
	if err != nil {
		t.Fatalf("UpdateUpload: %v", err)
	}
	if policy.MaxUploadBytes != value {
		t.Fatalf("expected %d back unchanged, got %d", value, policy.MaxUploadBytes)
	}
	if event := audit.last(t); event.Metadata["unit"] != "bytes" || event.Metadata["to"] != "104857600" {
		t.Fatalf("unexpected metadata %v", event.Metadata)
	}
}

func TestPolicyService_RefusesMalformedWorkspace(t *testing.T) {
	store := &policyStore{}
	svc := service.NewPolicyService(store, &recordingAudit{})
	if _, err := svc.UpdateAntiSpam(context.Background(), testActor(), "not-a-uuid", 30); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
	if _, err := svc.UpdateUpload(context.Background(), testActor(), "not-a-uuid", uploadpolicy.DefaultMaxUploadBytes); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
	if len(store.calls) != 0 {
		t.Fatalf("the store must not be reached, got %v", store.calls)
	}
}

func TestPolicyService_NilStoreIsUnavailable(t *testing.T) {
	svc := service.NewPolicyService(nil, nil)
	if _, err := svc.ListAntiSpam(context.Background(), domain.Cursor{}, 0); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
	if _, err := svc.ListUpload(context.Background(), domain.Cursor{}, 0); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
	if _, err := svc.UpdateAntiSpam(context.Background(), testActor(), targetID, 30); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
	if _, err := svc.UpdateUpload(context.Background(), testActor(), targetID, uploadpolicy.DefaultMaxUploadBytes); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
}

// A nil recorder must not crash a mutation. The trail is evidence, not a
// precondition — the same rule AuditService.Record follows.
func TestManagementServices_ToleratesNoRecorder(t *testing.T) {
	if _, err := service.NewUserAdminService(&userStore{}, nil).
		SetStatus(context.Background(), testActor(), targetID, domain.UserStatusSuspended); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	if _, err := service.NewPolicyService(&policyStore{}, nil).
		UpdateAntiSpam(context.Background(), testActor(), targetID, 30); err != nil {
		t.Fatalf("UpdateAntiSpam: %v", err)
	}
}

// The read paths delegate without adding a second, divergent filter: whatever
// the handler validated is what the store receives.
func TestManagementServices_ReadPathsDelegate(t *testing.T) {
	users := &userStore{}
	if _, err := service.NewUserAdminService(users, nil).
		List(context.Background(), domain.AdminUserFilter{Query: "ana"}); err != nil {
		t.Fatalf("List: %v", err)
	}
	if _, err := service.NewUserAdminService(users, nil).Get(context.Background(), targetID); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(users.calls) != 2 || users.calls[1] != "get:"+targetID {
		t.Fatalf("unexpected calls %v", users.calls)
	}

	channels := &channelStore{}
	svc := service.NewChannelAdminService(channels, nil)
	if _, err := svc.List(context.Background(), domain.AdminChannelFilter{}); err != nil {
		t.Fatalf("List: %v", err)
	}
	if _, err := svc.Get(context.Background(), targetID); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, err := svc.ListConversations(context.Background(), domain.AdminConversationFilter{}); err != nil {
		t.Fatalf("ListConversations: %v", err)
	}
	if len(channels.calls) != 3 {
		t.Fatalf("unexpected calls %v", channels.calls)
	}

	policies := &policyStore{}
	policySvc := service.NewPolicyService(policies, nil)
	if _, err := policySvc.ListAntiSpam(context.Background(), domain.Cursor{}, 25); err != nil {
		t.Fatalf("ListAntiSpam: %v", err)
	}
	if _, err := policySvc.ListUpload(context.Background(), domain.Cursor{}, 25); err != nil {
		t.Fatalf("ListUpload: %v", err)
	}
	if len(policies.calls) != 2 {
		t.Fatalf("unexpected calls %v", policies.calls)
	}
}

// Revoking somebody else's sessions is allowed and reaches the store; only the
// operator's own account is off limits.
func TestUserAdminService_RevokeSessions_ReachesTheStoreForSomebodyElse(t *testing.T) {
	store := &userStore{revoked: 2}
	audit := &recordingAudit{}
	revoked, err := service.NewUserAdminService(store, audit).
		RevokeSessions(context.Background(), testActor(), targetID)
	if err != nil {
		t.Fatalf("RevokeSessions: %v", err)
	}
	if revoked != 2 {
		t.Fatalf("expected 2, got %d", revoked)
	}
	if event := audit.last(t); event.Action != domain.AuditActionUserSessionsRevoke ||
		event.Metadata["revoked_sessions"] != "2" {
		t.Fatalf("unexpected event %+v", event)
	}
}

func TestUserAdminService_RevokeRole_ReachesTheStoreForSomebodyElse(t *testing.T) {
	store := &userStore{}
	if err := service.NewUserAdminService(store, &recordingAudit{}).
		RevokeRole(context.Background(), testActor(), targetID, "platform-auditor"); err != nil {
		t.Fatalf("RevokeRole: %v", err)
	}
	want := "revoke-role:" + targetID + ":platform-auditor"
	if len(store.calls) != 1 || store.calls[0] != want {
		t.Fatalf("expected %q, got %v", want, store.calls)
	}
}

func TestChannelAdminService_SetStatus_RefusesAMalformedIdentifier(t *testing.T) {
	store := &channelStore{}
	if _, err := service.NewChannelAdminService(store, &recordingAudit{}).
		SetStatus(context.Background(), testActor(), "not-a-uuid", "archived"); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
	if len(store.calls) != 0 {
		t.Fatalf("the store must not be reached, got %v", store.calls)
	}
}

// ---------------------------------------------------------------------------
// Channel membership
// ---------------------------------------------------------------------------

const otherTargetID = "33333333-3333-3333-3333-333333333333"

// The target list is validated before any statement runs, so a malformed
// request is a refusal the caller can read rather than a rolled-back
// transaction.
func TestChannelAdminService_AddMembers_RefusesMalformedTargetLists(t *testing.T) {
	cases := map[string][]string{
		"empty list":             {},
		"target is not a uuid":   {"not-a-uuid"},
		"target is sql":          {"' OR 1=1 --"},
		"target is a wildcard":   {"%"},
		"one bad among the good": {targetID, "not-a-uuid"},
		"duplicate target":       {targetID, targetID},
		"oversized list":         makeIDs(domain.MaxChannelMembersPerRequest + 1),
	}
	for name, ids := range cases {
		t.Run(name, func(t *testing.T) {
			store := &channelStore{}
			_, err := service.NewChannelAdminService(store, &recordingAudit{}).
				AddMembers(context.Background(), testActor(), targetID, ids)
			if !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("expected ErrInvalidInput, got %v", err)
			}
			if len(store.calls) != 0 {
				t.Fatalf("the store must not be reached, got %v", store.calls)
			}
		})
	}
}

// The largest list the platform is willing to run in one transaction is
// accepted; one more is not. Both bounds are asserted so the limit is a real
// edge rather than an approximate one.
func TestChannelAdminService_AddMembers_AcceptsExactlyTheCap(t *testing.T) {
	store := &channelStore{}
	if _, err := service.NewChannelAdminService(store, &recordingAudit{}).
		AddMembers(context.Background(), testActor(), targetID,
			makeIDs(domain.MaxChannelMembersPerRequest)); err != nil {
		t.Fatalf("AddMembers at the cap: %v", err)
	}
	if len(store.calls) != 1 {
		t.Fatalf("expected the store to be reached once, got %v", store.calls)
	}
}

func TestChannelAdminService_AddMembers_RefusesAMalformedChannel(t *testing.T) {
	store := &channelStore{}
	if _, err := service.NewChannelAdminService(store, &recordingAudit{}).
		AddMembers(context.Background(), testActor(), "not-a-uuid", []string{targetID}); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
	if len(store.calls) != 0 {
		t.Fatalf("the store must not be reached, got %v", store.calls)
	}
}

func TestChannelAdminService_AddMembers_RecordsWhatChanged(t *testing.T) {
	store := &channelStore{membership: domain.ChannelMembershipChange{
		WorkspaceID: otherTargetID, Added: 1, AlreadyMembers: 1, MemberCount: 12,
	}}
	audit := &recordingAudit{}
	change, err := service.NewChannelAdminService(store, audit).
		AddMembers(context.Background(), testActor(), targetID, []string{targetID, otherTargetID})
	if err != nil {
		t.Fatalf("AddMembers: %v", err)
	}
	if change.MemberCount != 12 {
		t.Fatalf("unexpected change %+v", change)
	}
	event := audit.last(t)
	if event.Action != domain.AuditActionChannelMemberAdd || event.Result != domain.AuditResultSuccess {
		t.Fatalf("unexpected event %+v", event)
	}
	// "requested 2, added 1, already 1" is one fact an operator can read. "2
	// added" would have been false.
	if event.Metadata["target_count"] != "2" || event.Metadata["added"] != "1" ||
		event.Metadata["already_members"] != "1" {
		t.Fatalf("unexpected metadata %v", event.Metadata)
	}
	if event.Resource != "admin.channel:"+targetID {
		t.Fatalf("unexpected resource %q", event.Resource)
	}
}

// An ineligible target is a refusal from the store, recorded as a denial rather
// than an error: the platform said no, it did not break.
func TestChannelAdminService_AddMembers_IneligibleTargetIsADenial(t *testing.T) {
	store := &channelStore{err: domain.ErrConflict}
	audit := &recordingAudit{}
	_, err := service.NewChannelAdminService(store, audit).
		AddMembers(context.Background(), testActor(), targetID, []string{otherTargetID})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
	if event := audit.last(t); event.Result != domain.AuditResultDenied {
		t.Fatalf("expected a denial, got %q", event.Result)
	}
}

func TestChannelAdminService_RemoveMember_RecordsWhetherAnythingChanged(t *testing.T) {
	t.Run("removed", func(t *testing.T) {
		store := &channelStore{membership: domain.ChannelMembershipChange{Removed: true, MemberCount: 11}}
		audit := &recordingAudit{}
		if _, err := service.NewChannelAdminService(store, audit).
			RemoveMember(context.Background(), testActor(), targetID, otherTargetID); err != nil {
			t.Fatalf("RemoveMember: %v", err)
		}
		event := audit.last(t)
		if event.Action != domain.AuditActionChannelMemberKick || event.Metadata["removed"] != "true" {
			t.Fatalf("unexpected event %+v", event)
		}
		if event.Metadata["target_user_id"] != otherTargetID {
			t.Fatalf("the target must be recorded, got %v", event.Metadata)
		}
	})
	// A retry removes nobody and the trail says so, rather than claiming a
	// second removal that never happened.
	t.Run("nobody to remove", func(t *testing.T) {
		store := &channelStore{membership: domain.ChannelMembershipChange{Removed: false, MemberCount: 11}}
		audit := &recordingAudit{}
		change, err := service.NewChannelAdminService(store, audit).
			RemoveMember(context.Background(), testActor(), targetID, otherTargetID)
		if err != nil {
			t.Fatalf("a repeat removal must succeed, got %v", err)
		}
		if change.Removed {
			t.Fatal("nothing was removed, and the result must say so")
		}
		if audit.last(t).Metadata["removed"] != "false" {
			t.Fatalf("unexpected metadata %v", audit.last(t).Metadata)
		}
	})
}

func TestChannelAdminService_RemoveMember_RefusesMalformedIdentifiers(t *testing.T) {
	cases := map[string][2]string{
		"channel is not a uuid": {"not-a-uuid", otherTargetID},
		"target is not a uuid":  {targetID, "not-a-uuid"},
	}
	for name, ids := range cases {
		t.Run(name, func(t *testing.T) {
			store := &channelStore{}
			_, err := service.NewChannelAdminService(store, &recordingAudit{}).
				RemoveMember(context.Background(), testActor(), ids[0], ids[1])
			if !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("expected ErrInvalidInput, got %v", err)
			}
			if len(store.calls) != 0 {
				t.Fatalf("the store must not be reached, got %v", store.calls)
			}
		})
	}
}

func TestChannelAdminService_Membership_NilStoreIsUnavailable(t *testing.T) {
	svc := service.NewChannelAdminService(nil, nil)
	if _, err := svc.AddMembers(context.Background(), testActor(), targetID, []string{otherTargetID}); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
	if _, err := svc.RemoveMember(context.Background(), testActor(), targetID, otherTargetID); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
}

// makeIDs builds n distinct, well-formed uuids, so a size check is exercised by
// size alone and not by a malformed entry.
func makeIDs(n int) []string {
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		ids = append(ids, fmt.Sprintf("%08d-1111-1111-1111-111111111111", i))
	}
	return ids
}

// The four mutations that act on an account all name it the same way, so one
// filter finds every one of them.
//
// This is asserted against the real producers rather than a stub: a producer
// that spelled the resource key differently would still pass every other test
// in this file and simply be invisible in the history it belongs to.
func TestUserAdminService_NamesTheTargetUserCanonically(t *testing.T) {
	want := domain.AuditUserResource(targetID)
	cases := map[string]struct {
		action string
		call   func(*service.UserAdminService) error
	}{
		"status": {domain.AuditActionUserStatusUpdate, func(s *service.UserAdminService) error {
			_, err := s.SetStatus(context.Background(), testActor(), targetID, domain.UserStatusSuspended)
			return err
		}},
		"session revocation": {domain.AuditActionUserSessionsRevoke, func(s *service.UserAdminService) error {
			_, err := s.RevokeSessions(context.Background(), testActor(), targetID)
			return err
		}},
		"role grant": {domain.AuditActionUserRoleGrant, func(s *service.UserAdminService) error {
			return s.GrantRole(context.Background(), testActor(), targetID, "platform-auditor")
		}},
		"role revocation": {domain.AuditActionUserRoleRevoke, func(s *service.UserAdminService) error {
			return s.RevokeRole(context.Background(), testActor(), targetID, "platform-auditor")
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			audit := &recordingAudit{}
			if err := tc.call(service.NewUserAdminService(&userStore{}, audit)); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			event := audit.last(t)
			if event.Action != tc.action {
				t.Fatalf("expected %q, got %q", tc.action, event.Action)
			}
			if event.Resource != want {
				t.Fatalf("%s named the target %q, expected %q", tc.action, event.Resource, want)
			}
		})
	}
}

// A refused mutation is recorded against the same key. An operator reading
// somebody's history must see the attempts, not only the successes.
func TestUserAdminService_RefusalsAreFiledUnderTheSameUser(t *testing.T) {
	audit := &recordingAudit{}
	_, err := service.NewUserAdminService(&userStore{err: domain.ErrConflict}, audit).
		SetStatus(context.Background(), testActor(), targetID, domain.UserStatusSuspended)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
	event := audit.last(t)
	if event.Resource != domain.AuditUserResource(targetID) || event.Result != domain.AuditResultDenied {
		t.Fatalf("unexpected event %+v", event)
	}
}

// The channel membership events name the *channel*, because that is what
// changed. They are deliberately not filed under the person: re-keying them to
// the user would say a user record was modified when a channel's membership
// was. The target still travels in the metadata for the channel's own trail.
func TestChannelAdminService_MembershipIsFiledUnderTheChannel(t *testing.T) {
	audit := &recordingAudit{}
	if _, err := service.NewChannelAdminService(&channelStore{}, audit).
		RemoveMember(context.Background(), testActor(), targetID, otherTargetID); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
	event := audit.last(t)
	if event.Resource != "admin.channel:"+targetID {
		t.Fatalf("membership belongs to the channel's trail, got %q", event.Resource)
	}
	if event.Metadata["target_user_id"] != otherTargetID {
		t.Fatalf("the affected person must still be recorded, got %v", event.Metadata)
	}
}

// ---------------------------------------------------------------------------
// Audit filter
// ---------------------------------------------------------------------------

type auditListStore struct {
	filter  domain.AuditFilter
	entries []domain.AuditEntry
}

func (s *auditListStore) AppendAudit(context.Context, domain.AuditEvent) error { return nil }

func (s *auditListStore) ListAuditEvents(_ context.Context, filter domain.AuditFilter) ([]domain.AuditEntry, error) {
	s.filter = filter
	return s.entries, nil
}

// The service is what turns a user id into a resource key, so the store only
// ever receives a value this service built.
func TestAuditService_BuildsTheCanonicalResourceKey(t *testing.T) {
	store := &auditListStore{}
	if _, err := service.NewAuditService(store, nil).
		List(context.Background(), 25, targetID); err != nil {
		t.Fatalf("List: %v", err)
	}
	if want := domain.AuditUserResource(targetID); store.filter.Resource != want {
		t.Fatalf("expected %q, got %q", want, store.filter.Resource)
	}
	if store.filter.Limit != 25 {
		t.Fatalf("the limit must survive, got %d", store.filter.Limit)
	}
}

func TestAuditService_NoUserMeansTheGlobalTrail(t *testing.T) {
	store := &auditListStore{}
	if _, err := service.NewAuditService(store, nil).List(context.Background(), 0, ""); err != nil {
		t.Fatalf("List: %v", err)
	}
	if store.filter.Resource != "" {
		t.Fatalf("no narrowing expected, got %q", store.filter.Resource)
	}
}

// A malformed id never reaches the store, so no value of a caller's choosing is
// ever compared against the resource column.
func TestAuditService_RefusesAMalformedUser(t *testing.T) {
	for _, value := range []string{"abc", "admin.user:x", "%", "' OR 1=1 --"} {
		store := &auditListStore{}
		_, err := service.NewAuditService(store, nil).List(context.Background(), 25, value)
		if !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("%q: expected ErrInvalidInput, got %v", value, err)
		}
		if store.filter.Resource != "" {
			t.Fatalf("%q: nothing must reach the store, got %q", value, store.filter.Resource)
		}
	}
}
