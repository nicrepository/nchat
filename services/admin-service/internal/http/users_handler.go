package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
	"github.com/nicrepository/nchat/services/admin-service/internal/service"
)

// UserAdmin is the user management surface the routes drive.
type UserAdmin interface {
	List(ctx context.Context, filter domain.AdminUserFilter) (domain.Page[domain.AdminUserSummary], error)
	Get(ctx context.Context, userID string) (domain.AdminUserDetail, error)
	SetStatus(ctx context.Context, actor service.Actor, targetUserID, status string) (domain.UserStatusChange, error)
	RevokeSessions(ctx context.Context, actor service.Actor, targetUserID string) (int, error)
	GrantRole(ctx context.Context, actor service.Actor, targetUserID, roleSlug string) error
	RevokeRole(ctx context.Context, actor service.Actor, targetUserID, roleSlug string) error
}

// adminUserPayload is the response DTO for one directory row.
//
// It is an allowlist, like the bootstrap payload: the struct names every field
// that may leave this service. auth.users carries more than this — a password
// credential lives beside it, and the row itself holds the external subject the
// identity provider knows the person by, the anonymization timestamp and the
// soft-delete timestamp. None of them is here, and none can arrive by someone
// adding a column.
type adminUserPayload struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	FullName    string `json:"full_name"`
	AvatarURL   string `json:"avatar_url"`
	Status      string `json:"status"`
	AuthSource  string `json:"auth_source"`
	// ExternalProvider names the identity provider, e.g. "keycloak". It is the
	// provider's name, never the subject identifier it knows the person by.
	ExternalProvider string `json:"external_provider"`
	// IdentityManagedExternally is derived server-side so the console does not
	// re-derive "is this a Keycloak account" from a string it happens to hold.
	// It is what makes the screen say which fields NChat is not the source of
	// truth for — and this API offers no way to write any of them.
	IdentityManagedExternally bool                   `json:"identity_managed_externally"`
	LastLoginAt               *time.Time             `json:"last_login_at"`
	CreatedAt                 time.Time              `json:"created_at"`
	PlatformAdmin             bool                   `json:"platform_admin"`
	AdminRoles                []string               `json:"admin_roles"`
	WorkspaceRoles            []workspaceRolePayload `json:"workspace_roles"`
	ActiveSessions            int                    `json:"active_sessions"`
}

type workspaceRolePayload struct {
	WorkspaceID   string    `json:"workspace_id"`
	WorkspaceName string    `json:"workspace_name"`
	Role          string    `json:"role"`
	Status        string    `json:"status"`
	JoinedAt      time.Time `json:"joined_at"`
}

type adminRolePayload struct {
	Slug         string   `json:"slug"`
	Description  string   `json:"description"`
	Capabilities []string `json:"capabilities"`
}

type adminRoleGrantPayload struct {
	adminRolePayload
	GrantedAt time.Time `json:"granted_at"`
	GrantedBy string    `json:"granted_by"`
}

type adminUserDetailPayload struct {
	adminUserPayload
	Memberships    []workspaceRolePayload  `json:"memberships"`
	ChannelCount   int                     `json:"channel_count"`
	RoleGrants     []adminRoleGrantPayload `json:"role_grants"`
	AvailableRoles []adminRolePayload      `json:"available_roles"`
}

// paginationPayload is the pagination block every listing returns.
//
// NextCursor is a pointer so the last page serializes as `"next_cursor": null`
// rather than an empty string. The field is never omitted: a client that has to
// tell "no more pages" from "this server forgot to answer" would be reading the
// absence of a key, and JSON gives it a value for exactly this.
type paginationPayload struct {
	NextCursor *string `json:"next_cursor"`
	HasMore    bool    `json:"has_more"`
}

// ListUsers serves the platform user directory.
//
// Guarded by admin.users.read, declared at the route. A directory of everyone
// on the platform, with their administrative roles and how many live sessions
// they hold, is not a "read that needs no permission".
func ListUsers(users UserAdmin) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if users == nil {
			writeUnavailable(w)
			return
		}
		filter, ok := parseUserFilter(w, r)
		if !ok {
			return
		}
		page, err := users.List(r.Context(), filter)
		if err != nil {
			writeDomainError(w, err)
			return
		}
		payload := make([]adminUserPayload, 0, len(page.Items))
		for _, user := range page.Items {
			payload = append(payload, newAdminUserPayload(user))
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{
			"users":      payload,
			"pagination": newPagination(page.NextCursor),
		})
	})
}

// parseUserFilter turns the query string into a validated filter.
//
// Every value is drawn from a closed set or is a bounded scalar, and an
// unrecognised one is a 400. There is no `sort` parameter to validate because
// the directory has one order; adding one later means adding an allowlist, not
// forwarding a column name.
func parseUserFilter(w http.ResponseWriter, r *http.Request) (domain.AdminUserFilter, bool) {
	query := r.URL.Query()
	limit, cursor, err := parsePageParams(query)
	if err != nil {
		writeInvalidQuery(w)
		return domain.AdminUserFilter{}, false
	}
	status, err := allowlisted(query, "status", domain.UserStatusFilter)
	if err != nil {
		writeInvalidQuery(w)
		return domain.AdminUserFilter{}, false
	}
	authSource, err := allowlisted(query, "auth_source", domain.UserAuthSourceFilter)
	if err != nil {
		writeInvalidQuery(w)
		return domain.AdminUserFilter{}, false
	}
	platformAdmin, err := parseTriState(query, "platform_admin")
	if err != nil {
		writeInvalidQuery(w)
		return domain.AdminUserFilter{}, false
	}
	// A workspace role is a different question from platform_admin above, and
	// they combine rather than replace each other: an operator can ask for the
	// owners of some workspace who are also platform administrators, and get
	// exactly those.
	workspaceRole, err := allowlisted(query, "workspace_role", domain.WorkspaceRoleFilter)
	if err != nil {
		writeInvalidQuery(w)
		return domain.AdminUserFilter{}, false
	}
	inactivity, err := parseUserActivity(query)
	if err != nil {
		writeInvalidQuery(w)
		return domain.AdminUserFilter{}, false
	}
	term, err := parseSearchTerm(query)
	if err != nil {
		writeInvalidQuery(w)
		return domain.AdminUserFilter{}, false
	}
	return domain.AdminUserFilter{
		Query:         term,
		Status:        status,
		AuthSource:    authSource,
		PlatformAdmin: platformAdmin,
		WorkspaceRole: workspaceRole,
		Inactivity:    inactivity,
		Limit:         limit,
		Cursor:        cursor,
	}, true
}

// GetUser serves one directory record with the aggregates the detail view
// needs. Same capability as the listing: it is the same data, for one person.
func GetUser(users UserAdmin) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if users == nil {
			writeUnavailable(w)
			return
		}
		detail, err := users.Get(r.Context(), r.PathValue("userID"))
		if err != nil {
			writeDomainError(w, err)
			return
		}
		httputil.WriteJSON(w, http.StatusOK, newAdminUserDetailPayload(detail))
	})
}

// userStatusRequest is the entire accepted body of a status change.
//
// One field, and the decoder refuses every other. A body carrying "role",
// "capabilities", "platform_admin" or "email" alongside it is a 400, not a
// partially applied update: this endpoint changes a status and cannot be talked
// into changing anything else.
type userStatusRequest struct {
	Status string `json:"status"`
}

// UpdateUserStatus activates or deactivates an account.
//
// Requires admin.users.manage, and runs behind the CSRF guard like every
// mutation. The actor is taken from the authenticated session, never from the
// body, which is what makes the self-suspension guard in the service
// enforceable at all.
func UpdateUserStatus(users UserAdmin) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if users == nil {
			writeUnavailable(w)
			return
		}
		actor, ok := actorFrom(w, r)
		if !ok {
			return
		}
		var body userStatusRequest
		if !decodeJSONBody(w, r, &body) {
			return
		}
		change, err := users.SetStatus(r.Context(), actor, r.PathValue("userID"), body.Status)
		if err != nil {
			writeDomainError(w, err)
			return
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{
			"user_id":          change.TargetUserID,
			"from_status":      change.FromStatus,
			"to_status":        change.ToStatus,
			"revoked_sessions": change.RevokedSessions,
		})
	})
}

// RevokeUserSessions signs one account out everywhere.
//
// It reports how many sessions it ended, because "0 revoked" and "3 revoked"
// are different answers to the operator's actual question — whether the person
// is still signed in somewhere.
func RevokeUserSessions(users UserAdmin) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if users == nil {
			writeUnavailable(w)
			return
		}
		actor, ok := actorFrom(w, r)
		if !ok {
			return
		}
		userID := r.PathValue("userID")
		revoked, err := users.RevokeSessions(r.Context(), actor, userID)
		if err != nil {
			writeDomainError(w, err)
			return
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{
			"user_id":          userID,
			"revoked_sessions": revoked,
		})
	})
}

// adminRoleRequest is the entire accepted body of a role grant.
type adminRoleRequest struct {
	RoleSlug string `json:"role_slug"`
}

// GrantAdminRole makes somebody a platform administrator holding one role.
//
// Guarded by admin.superuser rather than by admin.users.manage. Conferring
// authority you do not hold is escalation, so the only principal allowed to
// change who administers the platform is one that already administers all of
// it.
func GrantAdminRole(users UserAdmin) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if users == nil {
			writeUnavailable(w)
			return
		}
		actor, ok := actorFrom(w, r)
		if !ok {
			return
		}
		var body adminRoleRequest
		if !decodeJSONBody(w, r, &body) {
			return
		}
		if err := users.GrantRole(r.Context(), actor, r.PathValue("userID"), body.RoleSlug); err != nil {
			writeDomainError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// RevokeAdminRole removes one administrative role.
//
// The role is a path segment rather than a body field so the operation is
// idempotent in the shape a DELETE should be, and so a revocation cannot be
// aimed at a different role by a body the console did not send.
func RevokeAdminRole(users UserAdmin) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if users == nil {
			writeUnavailable(w)
			return
		}
		actor, ok := actorFrom(w, r)
		if !ok {
			return
		}
		if err := users.RevokeRole(r.Context(), actor, r.PathValue("userID"), r.PathValue("roleSlug")); err != nil {
			writeDomainError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// actorFrom derives who is acting from the authenticated session and the
// server-minted request id.
//
// Neither value can come from the request. The principal is the one the session
// guard loaded from the database on this request, and the correlation id is
// generated by this service's own middleware rather than accepted from a header
// — so an audit row cannot be attributed to somebody else, and cannot be
// slipped into another request's trail.
func actorFrom(w http.ResponseWriter, r *http.Request) (service.Actor, bool) {
	admin, ok := AdminFromContext(r)
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
		return service.Actor{}, false
	}
	return service.Actor{
		UserID:        admin.Principal.UserID,
		CorrelationID: httputil.RequestIDFromContext(r.Context()),
	}, true
}

func writeInvalidQuery(w http.ResponseWriter) {
	httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid query parameter")
}

func newPagination(nextCursor string) paginationPayload {
	// has_more is derived from the cursor rather than reported alongside it, so
	// the two can never disagree — a client that trusted has_more and found no
	// cursor would page forever or stop early.
	if nextCursor == "" {
		return paginationPayload{NextCursor: nil, HasMore: false}
	}
	return paginationPayload{NextCursor: &nextCursor, HasMore: true}
}

func newAdminUserPayload(user domain.AdminUserSummary) adminUserPayload {
	return adminUserPayload{
		ID:                        user.ID,
		Email:                     user.Email,
		DisplayName:               user.DisplayName,
		FullName:                  user.FullName,
		AvatarURL:                 user.AvatarURL,
		Status:                    user.Status,
		AuthSource:                user.AuthSource,
		ExternalProvider:          user.ExternalProvider,
		IdentityManagedExternally: user.AuthSource == "oidc",
		LastLoginAt:               user.LastLoginAt,
		CreatedAt:                 user.CreatedAt,
		PlatformAdmin:             user.PlatformAdmin,
		AdminRoles:                user.AdminRoles,
		WorkspaceRoles:            newWorkspaceRolePayloads(user.WorkspaceRoles),
		ActiveSessions:            user.ActiveSessions,
	}
}

func newAdminUserDetailPayload(detail domain.AdminUserDetail) adminUserDetailPayload {
	grants := make([]adminRoleGrantPayload, 0, len(detail.RoleGrants))
	for _, grant := range detail.RoleGrants {
		grants = append(grants, adminRoleGrantPayload{
			adminRolePayload: adminRolePayload{
				Slug: grant.Slug, Description: grant.Description, Capabilities: grant.Capabilities,
			},
			GrantedAt: grant.GrantedAt,
			GrantedBy: grant.GrantedBy,
		})
	}
	available := make([]adminRolePayload, 0, len(detail.AvailableRoles))
	for _, role := range detail.AvailableRoles {
		available = append(available, adminRolePayload{
			Slug: role.Slug, Description: role.Description, Capabilities: role.Capabilities,
		})
	}
	return adminUserDetailPayload{
		adminUserPayload: newAdminUserPayload(detail.AdminUserSummary),
		Memberships:      newWorkspaceRolePayloads(detail.Memberships),
		ChannelCount:     detail.ChannelCount,
		RoleGrants:       grants,
		AvailableRoles:   available,
	}
}

func newWorkspaceRolePayloads(roles []domain.WorkspaceRoleRef) []workspaceRolePayload {
	payload := make([]workspaceRolePayload, 0, len(roles))
	for _, role := range roles {
		payload = append(payload, workspaceRolePayload{
			WorkspaceID:   role.WorkspaceID,
			WorkspaceName: role.WorkspaceName,
			Role:          role.Role,
			Status:        role.Status,
			JoinedAt:      role.JoinedAt,
		})
	}
	return payload
}
