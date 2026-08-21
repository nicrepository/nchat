package service

import (
	"context"
	"regexp"
	"strconv"

	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
)

// UserDirectoryStore is the persistence the user management surface needs.
type UserDirectoryStore interface {
	ListUsers(ctx context.Context, filter domain.AdminUserFilter) (domain.Page[domain.AdminUserSummary], error)
	GetUser(ctx context.Context, userID string) (domain.AdminUserDetail, error)
	UpdateUserStatus(ctx context.Context, userID, newStatus string) (domain.UserStatusChange, error)
	RevokeUserSessions(ctx context.Context, userID string) (int, error)
	GrantAdminRole(ctx context.Context, targetUserID, roleSlug, grantedBy string) error
	RevokeAdminRole(ctx context.Context, targetUserID, roleSlug string) error
}

// UserAdminService holds the invariants of user administration.
//
// The store enforces what only the database can (row locks, referential
// integrity, the last-administrator count under a lock). This layer enforces
// what only the request knows: who is asking, and whether the operation is one
// they may perform on that target.
type UserAdminService struct {
	store UserDirectoryStore
	audit Recorder
}

func NewUserAdminService(store UserDirectoryStore, audit Recorder) *UserAdminService {
	return &UserAdminService{store: store, audit: audit}
}

// roleSlugPattern mirrors the CHECK on auth.admin_roles.slug.
//
// It is repeated here so a malformed slug is a 400 from this service rather
// than a constraint violation surfacing as a 500 — and so a slug never reaches
// the store as an unbounded string.
var roleSlugPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,63}$`)

func (s *UserAdminService) List(ctx context.Context, filter domain.AdminUserFilter) (domain.Page[domain.AdminUserSummary], error) {
	if s == nil || s.store == nil {
		return domain.Page[domain.AdminUserSummary]{}, domain.ErrUnavailable
	}
	return s.store.ListUsers(ctx, filter)
}

func (s *UserAdminService) Get(ctx context.Context, userID string) (domain.AdminUserDetail, error) {
	if s == nil || s.store == nil {
		return domain.AdminUserDetail{}, domain.ErrUnavailable
	}
	if !domain.ValidUUID(userID) {
		return domain.AdminUserDetail{}, domain.ErrInvalidInput
	}
	return s.store.GetUser(ctx, userID)
}

// SetStatus activates or deactivates an account.
//
// Two guards before the store is touched:
//
//   - the target must be somebody else. An administrator suspending their own
//     account is either a mistake that locks the console's operator out
//     mid-task, or a way to make an audit trail read as if somebody else did
//     it. auth-service's equivalent route could not enforce this because its
//     bootstrap guard carries no caller identity; this one does.
//   - the status must be one of the two an administrator may set. 'invited',
//     'locked' and 'deleted' belong to other flows and are not a switch.
//
// The transition itself is validated again inside the store's transaction,
// under a row lock, because "is this account currently active" is not something
// a check up here can still be true about by the time the write lands.
func (s *UserAdminService) SetStatus(ctx context.Context, actor Actor, targetUserID, status string) (domain.UserStatusChange, error) {
	if s == nil || s.store == nil {
		return domain.UserStatusChange{}, domain.ErrUnavailable
	}
	resource := domain.AuditUserResource(targetUserID)
	change, err := s.setStatus(ctx, actor, targetUserID, status)
	record(ctx, s.audit, actor, domain.AuditActionUserStatusUpdate, resource, resultFor(err), map[string]string{
		"target_user_id":   targetUserID,
		"requested_status": status,
		"from_status":      change.FromStatus,
		"revoked_sessions": strconv.Itoa(change.RevokedSessions),
	})
	return change, err
}

func (s *UserAdminService) setStatus(ctx context.Context, actor Actor, targetUserID, status string) (domain.UserStatusChange, error) {
	if !domain.ValidUUID(targetUserID) {
		return domain.UserStatusChange{}, domain.ErrInvalidInput
	}
	if status != domain.UserStatusActive && status != domain.UserStatusSuspended {
		return domain.UserStatusChange{}, domain.ErrInvalidInput
	}
	if targetUserID == actor.UserID {
		return domain.UserStatusChange{}, domain.ErrForbidden
	}
	return s.store.UpdateUserStatus(ctx, targetUserID, status)
}

// RevokeSessions signs an account out of every device without changing what the
// account is allowed to do.
//
// Same self guard as SetStatus, for the same reason and with one more: revoking
// the operator's own login sessions would revoke the administrative session
// riding on one of them, ending the console session from inside the request
// that asked for it. The console already has a sign-out button for that.
func (s *UserAdminService) RevokeSessions(ctx context.Context, actor Actor, targetUserID string) (int, error) {
	if s == nil || s.store == nil {
		return 0, domain.ErrUnavailable
	}
	revoked, err := s.revokeSessions(ctx, actor, targetUserID)
	record(ctx, s.audit, actor, domain.AuditActionUserSessionsRevoke, domain.AuditUserResource(targetUserID), resultFor(err), map[string]string{
		"target_user_id":   targetUserID,
		"revoked_sessions": strconv.Itoa(revoked),
	})
	return revoked, err
}

func (s *UserAdminService) revokeSessions(ctx context.Context, actor Actor, targetUserID string) (int, error) {
	if !domain.ValidUUID(targetUserID) {
		return 0, domain.ErrInvalidInput
	}
	if targetUserID == actor.UserID {
		return 0, domain.ErrForbidden
	}
	return s.store.RevokeUserSessions(ctx, targetUserID)
}

// GrantRole makes somebody a platform administrator holding one role.
//
// The capability required to reach this method is admin.superuser, declared at
// the route. That is not a convenience: a principal may only confer authority
// it holds in full, so anything less than superuser granting a role would be
// horizontal escalation — an administrator with admin.users.manage handing
// somebody admin.security.manage.
//
// The self guard closes the vertical case: an administrator cannot add a role
// to their own principal, so no chain of grants ends with the actor holding
// more than they started with.
func (s *UserAdminService) GrantRole(ctx context.Context, actor Actor, targetUserID, roleSlug string) error {
	if s == nil || s.store == nil {
		return domain.ErrUnavailable
	}
	err := s.grantRole(ctx, actor, targetUserID, roleSlug)
	record(ctx, s.audit, actor, domain.AuditActionUserRoleGrant, domain.AuditUserResource(targetUserID), resultFor(err), map[string]string{
		"target_user_id": targetUserID,
		"role_slug":      roleSlug,
	})
	return err
}

func (s *UserAdminService) grantRole(ctx context.Context, actor Actor, targetUserID, roleSlug string) error {
	if !domain.ValidUUID(targetUserID) || !roleSlugPattern.MatchString(roleSlug) {
		return domain.ErrInvalidInput
	}
	if targetUserID == actor.UserID {
		return domain.ErrForbidden
	}
	return s.store.GrantAdminRole(ctx, targetUserID, roleSlug, actor.UserID)
}

// RevokeRole removes one administrative role.
//
// The self guard here prevents an administrator revoking their own last role
// and locking themselves out; the last-administrator invariant, which the store
// evaluates inside the transaction, prevents them doing it to the platform
// through somebody else's principal.
func (s *UserAdminService) RevokeRole(ctx context.Context, actor Actor, targetUserID, roleSlug string) error {
	if s == nil || s.store == nil {
		return domain.ErrUnavailable
	}
	err := s.revokeRole(ctx, actor, targetUserID, roleSlug)
	record(ctx, s.audit, actor, domain.AuditActionUserRoleRevoke, domain.AuditUserResource(targetUserID), resultFor(err), map[string]string{
		"target_user_id": targetUserID,
		"role_slug":      roleSlug,
	})
	return err
}

func (s *UserAdminService) revokeRole(ctx context.Context, actor Actor, targetUserID, roleSlug string) error {
	if !domain.ValidUUID(targetUserID) || !roleSlugPattern.MatchString(roleSlug) {
		return domain.ErrInvalidInput
	}
	if targetUserID == actor.UserID {
		return domain.ErrForbidden
	}
	return s.store.RevokeAdminRole(ctx, targetUserID, roleSlug)
}
