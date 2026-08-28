package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
)

// PGXUserDirectoryStore serves the platform user directory and the mutations
// the console performs on an account.
//
// It talks to the same database auth-service does, and to the same tables. That
// is deliberate and it is the pattern this platform already uses across service
// boundaries — file-service, media-service and search-service all read the chat
// schema directly. The alternative, a privileged service-to-service HTTP call,
// would mean inventing a second administrative credential and a second guard
// chain, which is a larger attack surface than the one it would replace.
type PGXUserDirectoryStore struct {
	pool Pool
}

func NewPGXUserDirectoryStore(pool Pool) *PGXUserDirectoryStore {
	return &PGXUserDirectoryStore{pool: pool}
}

// userSummarySelect is the identity, authorization and activity projection of
// one directory row.
//
// The lateral joins are what keeps this one query rather than one query per
// row: each is an index lookup on a table already keyed by user_id
// (admin_principal_roles' primary key, idx_workspace_members_user,
// idx_user_sessions_user_revoked), evaluated once per row of the page and never
// once per page of the platform.
//
// It carries no password hash, no refresh token, no session credential and no
// external subject: the console needs to know *that* an identity comes from an
// identity provider, not the identifier the provider knows it by.
const userSummarySelect = `
	SELECT u.id::text,
	       u.email::text,
	       u.display_name,
	       COALESCE(u.full_name, ''),
	       COALESCE(u.avatar_url, ''),
	       u.status,
	       u.auth_source,
	       COALESCE(u.external_provider, ''),
	       u.last_login_at,
	       u.created_at,
	       (p.user_id IS NOT NULL) AS platform_admin,
	       COALESCE(roles.slugs, ARRAY[]::text[]),
	       COALESCE(memberships.rows, '[]'::jsonb),
	       COALESCE(sessions.live, 0)
	FROM auth.users AS u
	LEFT JOIN auth.admin_principals AS p
	       ON p.user_id = u.id AND p.status = 'active'
	LEFT JOIN LATERAL (
	    SELECT array_agg(pr.role_slug ORDER BY pr.role_slug) AS slugs
	    FROM auth.admin_principal_roles AS pr
	    WHERE pr.user_id = u.id
	) AS roles ON true
	LEFT JOIN LATERAL (
	    SELECT jsonb_agg(jsonb_build_object(
	               'workspace_id', wm.workspace_id::text,
	               'workspace_name', w.name,
	               'role', wm.role,
	               'status', wm.status,
	               'joined_at', wm.joined_at
	           ) ORDER BY w.name, wm.workspace_id) AS rows
	    FROM chat.workspace_members AS wm
	    JOIN chat.workspaces AS w ON w.id = wm.workspace_id
	    WHERE wm.user_id = u.id AND wm.status <> 'left'
	) AS memberships ON true
	LEFT JOIN LATERAL (
	    SELECT count(*) AS live
	    FROM auth.user_sessions AS s
	    WHERE s.user_id = u.id
	      AND s.revoked_at IS NULL
	      AND s.idle_expires_at > now()
	      AND (s.absolute_expires_at IS NULL OR s.absolute_expires_at > now())
	) AS sessions ON true`

// listUsersQuery pages the directory.
//
// Every filter is a bound parameter guarded by an IS NULL escape, so one
// prepared statement serves every combination and no predicate is ever built by
// string concatenation. There is no ORDER BY expression derived from the
// request: the order is fixed, which is why no sort parameter exists to
// validate.
//
// Resumption is the row-value comparison (created_at, id) < (cursor), matching
// idx_users_directory_page exactly, so page N costs the same as page 1 and a
// concurrent insert cannot shift the window.
const listUsersQuery = userSummarySelect + `
	WHERE u.deleted_at IS NULL
	  AND ($1::text IS NULL OR u.status = $1)
	  AND ($2::text IS NULL OR u.auth_source = $2)
	  AND ($3::boolean IS NULL OR (p.user_id IS NOT NULL) = $3)
	  AND ($4::text IS NULL
	       OR u.display_name ILIKE $4 ` + likeEscapeClause + `
	       OR COALESCE(u.full_name, '') ILIKE $4 ` + likeEscapeClause + `
	       OR u.email::text ILIKE $4 ` + likeEscapeClause + `)
	  AND ($5::timestamptz IS NULL OR u.last_login_at IS NULL OR u.last_login_at < $5)
	  AND (NOT $6::boolean OR u.last_login_at IS NULL)
	  AND ($7::text IS NULL OR EXISTS (
	          SELECT 1
	          FROM chat.workspace_members AS role_wm
	          JOIN chat.workspaces AS role_w
	            ON role_w.id = role_wm.workspace_id AND role_w.status = 'active'
	          WHERE role_wm.user_id = u.id
	            AND role_wm.status = 'active'
	            AND role_wm.role = $7
	      ))
	  AND ($8::timestamptz IS NULL OR (u.created_at, u.id) < ($8, $9::uuid))
	ORDER BY u.created_at DESC, u.id DESC
	LIMIT $10`

const getUserQuery = userSummarySelect + `
	WHERE u.id = $1::uuid AND u.deleted_at IS NULL
	LIMIT 1`

// ListUsers returns one page of the platform user directory.
//
// It asks for one row more than the page size so "is there another page" is
// answered by the same scan instead of by a second COUNT over the whole
// filtered set.
func (s *PGXUserDirectoryStore) ListUsers(ctx context.Context, filter domain.AdminUserFilter) (domain.Page[domain.AdminUserSummary], error) {
	if s == nil || s.pool == nil {
		return domain.Page[domain.AdminUserSummary]{}, domain.ErrUnavailable
	}
	limit := domain.ClampPageSize(filter.Limit)

	var inactiveBefore any
	if window, ok := domain.UserActivityFilter[filter.Inactivity]; ok {
		inactiveBefore = time.Now().UTC().Add(-window)
	}
	rows, err := s.pool.Query(ctx, listUsersQuery,
		nullableText(filter.Status),
		nullableText(filter.AuthSource),
		nullableBool(filter.PlatformAdmin),
		nullableText(likePattern(filter.Query)),
		inactiveBefore,
		filter.Inactivity == domain.ActivityFilterNever,
		nullableText(filter.WorkspaceRole),
		nullableCursorTime(filter.Cursor),
		nullableText(filter.Cursor.ID),
		limit+1,
	)
	if err != nil {
		return domain.Page[domain.AdminUserSummary]{}, fmt.Errorf("list admin users: %w", err)
	}
	defer rows.Close()

	items := make([]domain.AdminUserSummary, 0, limit)
	for rows.Next() {
		user, err := scanUserSummary(rows)
		if err != nil {
			return domain.Page[domain.AdminUserSummary]{}, err
		}
		items = append(items, user)
	}
	if err := rows.Err(); err != nil {
		return domain.Page[domain.AdminUserSummary]{}, fmt.Errorf("read admin users: %w", err)
	}
	return paginate(items, limit, func(u domain.AdminUserSummary) domain.Cursor {
		return domain.Cursor{At: u.CreatedAt, ID: u.ID}
	}), nil
}

// GetUser loads one directory row plus the aggregates the detail view needs.
//
// The three queries are deliberate rather than one join: the role catalogue and
// the channel count are independent of the identity row and of each other, and
// folding them into the listing projection would make every page pay for what
// one open record uses.
func (s *PGXUserDirectoryStore) GetUser(ctx context.Context, userID string) (domain.AdminUserDetail, error) {
	if s == nil || s.pool == nil {
		return domain.AdminUserDetail{}, domain.ErrUnavailable
	}
	rows, err := s.pool.Query(ctx, getUserQuery, userID)
	if err != nil {
		return domain.AdminUserDetail{}, fmt.Errorf("get admin user: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return domain.AdminUserDetail{}, fmt.Errorf("get admin user: %w", err)
		}
		return domain.AdminUserDetail{}, domain.ErrNotFound
	}
	summary, err := scanUserSummary(rows)
	if err != nil {
		return domain.AdminUserDetail{}, err
	}
	rows.Close()

	detail := domain.AdminUserDetail{AdminUserSummary: summary, Memberships: summary.WorkspaceRoles}
	if detail.RoleGrants, detail.AvailableRoles, err = s.roleCatalogue(ctx, userID); err != nil {
		return domain.AdminUserDetail{}, err
	}
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM chat.channel_members WHERE user_id = $1::uuid`, userID,
	).Scan(&detail.ChannelCount); err != nil {
		return domain.AdminUserDetail{}, fmt.Errorf("count admin user channels: %w", err)
	}
	return detail, nil
}

// roleCatalogueQuery answers "which roles exist, what does each grant, and
// which does this person hold" in one pass.
//
// One query rather than two because the console needs both halves together and
// they share the same capability lateral; splitting them would run it twice.
const roleCatalogueQuery = `
	SELECT r.slug,
	       r.description,
	       COALESCE(caps.list, ARRAY[]::text[]),
	       (pr.user_id IS NOT NULL) AS held,
	       pr.granted_at,
	       COALESCE(granter.email::text, '')
	FROM auth.admin_roles AS r
	LEFT JOIN auth.admin_principal_roles AS pr
	       ON pr.role_slug = r.slug AND pr.user_id = $1::uuid
	LEFT JOIN auth.users AS granter ON granter.id = pr.granted_by
	LEFT JOIN LATERAL (
	    SELECT array_agg(rc.capability ORDER BY rc.capability) AS list
	    FROM auth.admin_role_capabilities AS rc
	    WHERE rc.role_slug = r.slug
	) AS caps ON true
	ORDER BY r.slug`

func (s *PGXUserDirectoryStore) roleCatalogue(ctx context.Context, userID string) ([]domain.AdminRoleGrant, []domain.AdminRoleDescriptor, error) {
	rows, err := s.pool.Query(ctx, roleCatalogueQuery, userID)
	if err != nil {
		return nil, nil, fmt.Errorf("list admin roles: %w", err)
	}
	defer rows.Close()

	grants := make([]domain.AdminRoleGrant, 0)
	available := make([]domain.AdminRoleDescriptor, 0)
	for rows.Next() {
		var slug, description, granter string
		var capabilities []string
		var held bool
		var grantedAt *time.Time
		if err := rows.Scan(&slug, &description, &capabilities, &held, &grantedAt, &granter); err != nil {
			return nil, nil, fmt.Errorf("scan admin role: %w", err)
		}
		available = append(available, domain.AdminRoleDescriptor{
			Slug: slug, Description: description, Capabilities: capabilities,
		})
		if held {
			grant := domain.AdminRoleGrant{
				Slug: slug, Description: description, GrantedBy: granter, Capabilities: capabilities,
			}
			if grantedAt != nil {
				grant.GrantedAt = grantedAt.UTC()
			}
			grants = append(grants, grant)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("read admin roles: %w", err)
	}
	return grants, available, nil
}

func scanUserSummary(rows pgx.Rows) (domain.AdminUserSummary, error) {
	var user domain.AdminUserSummary
	var memberships []byte
	if err := rows.Scan(
		&user.ID, &user.Email, &user.DisplayName, &user.FullName, &user.AvatarURL,
		&user.Status, &user.AuthSource, &user.ExternalProvider,
		&user.LastLoginAt, &user.CreatedAt,
		&user.PlatformAdmin, &user.AdminRoles, &memberships, &user.ActiveSessions,
	); err != nil {
		return domain.AdminUserSummary{}, fmt.Errorf("scan admin user: %w", err)
	}
	decoded, err := decodeMemberships(memberships)
	if err != nil {
		return domain.AdminUserSummary{}, err
	}
	user.WorkspaceRoles = decoded
	user.CreatedAt = user.CreatedAt.UTC()
	if user.LastLoginAt != nil {
		utc := user.LastLoginAt.UTC()
		user.LastLoginAt = &utc
	}
	if user.AdminRoles == nil {
		user.AdminRoles = []string{}
	}
	return user, nil
}

// membershipRow is the wire shape of one element of the membership lateral.
//
// It is a storage-local type on purpose: the JSON keys are an implementation
// detail shared between one SQL expression and one decoder, and putting struct
// tags on the domain type would publish them as if they were a contract.
type membershipRow struct {
	WorkspaceID   string    `json:"workspace_id"`
	WorkspaceName string    `json:"workspace_name"`
	Role          string    `json:"role"`
	Status        string    `json:"status"`
	JoinedAt      time.Time `json:"joined_at"`
}

func decodeMemberships(raw []byte) ([]domain.WorkspaceRoleRef, error) {
	var rows []membershipRow
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("decode admin user memberships: %w", err)
	}
	memberships := make([]domain.WorkspaceRoleRef, 0, len(rows))
	for _, row := range rows {
		memberships = append(memberships, domain.WorkspaceRoleRef{
			WorkspaceID:   row.WorkspaceID,
			WorkspaceName: row.WorkspaceName,
			Role:          row.Role,
			Status:        row.Status,
			JoinedAt:      row.JoinedAt.UTC(),
		})
	}
	return memberships, nil
}

// ---------------------------------------------------------------------------
// Mutations
// ---------------------------------------------------------------------------

// revokeSessionsCTE ends every live login of one user and marks the matching
// refresh-token history revoked, reporting how many sessions it closed.
//
// It is one statement because the two updates are one fact: a session that is
// revoked while its refresh token still reads 'active' is a session that can be
// resurrected. The data-modifying CTEs run whether or not the final SELECT
// references them, which is what lets the count come from the first one.
//
// The reason string is a bind parameter so the two callers — suspension and an
// explicit forced sign-out — are distinguishable in auth.user_sessions
// afterwards, which is where an operator looks when asking why a session ended.
const revokeSessionsCTE = `
	WITH revoked AS (
	    UPDATE auth.user_sessions
	    SET revoked_at = now(), revoked_reason = $2
	    WHERE user_id = $1::uuid AND revoked_at IS NULL
	    RETURNING id
	), history AS (
	    UPDATE auth.refresh_token_history
	    SET status = 'revoked', revoked_at = now()
	    WHERE session_id IN (SELECT id FROM revoked)
	      AND status = 'active'
	    RETURNING 1
	)
	SELECT count(*) FROM revoked`

// invalidateExchangeCodes closes the replay window a suspension would otherwise
// leave open: an OIDC exchange code minted before the suspension could
// otherwise be redeemed for tokens afterwards. Mirrors auth-service's
// UpdateUserStatus, which is the other writer of this transition.
const invalidateExchangeCodes = `
	UPDATE auth.oidc_exchange_codes
	SET used_at = now()
	WHERE used_at IS NULL
	  AND expires_at > now()
	  AND user_json->>'id' = $1`

const lockUserQuery = `
	SELECT status FROM auth.users
	WHERE id = $1::uuid AND deleted_at IS NULL
	FOR UPDATE`

// UpdateUserStatus moves an account between active and suspended.
//
// The whole transition is one transaction, and the current status is read under
// FOR UPDATE rather than in a separate round trip. That ordering is the
// invariant: two concurrent suspensions cannot both observe 'active' and both
// claim to have made the change, and a password login racing a suspension
// serializes on the same row lock auth-service's login path takes — so either
// the login is refused, or its freshly created session is revoked by the
// suspension that queued behind it.
//
// Suspension revokes every session; activation restores none. There is no path
// here that resurrects a credential.
func (s *PGXUserDirectoryStore) UpdateUserStatus(ctx context.Context, userID string, newStatus string) (domain.UserStatusChange, error) {
	if s == nil || s.pool == nil {
		return domain.UserStatusChange{}, domain.ErrUnavailable
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.UserStatusChange{}, fmt.Errorf("begin status transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	current, err := lockUserForStatusChange(ctx, tx, userID, newStatus)
	if err != nil {
		return domain.UserStatusChange{}, err
	}

	if _, err := tx.Exec(ctx,
		`UPDATE auth.users SET status = $2, updated_at = now() WHERE id = $1::uuid AND deleted_at IS NULL`,
		userID, newStatus,
	); err != nil {
		return domain.UserStatusChange{}, fmt.Errorf("update user status: %w", err)
	}

	change := domain.UserStatusChange{TargetUserID: userID, FromStatus: current, ToStatus: newStatus}
	if newStatus == domain.UserStatusSuspended {
		revoked, err := closeOutSuspendedAccess(ctx, tx, userID)
		if err != nil {
			return domain.UserStatusChange{}, err
		}
		change.RevokedSessions = revoked
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.UserStatusChange{}, fmt.Errorf("commit status transaction: %w", err)
	}
	return change, nil
}

// lockUserForStatusChange takes both locks this change needs and answers with
// the status it is replacing.
//
// The user row first, because that is what serializes two status changes and
// what a login re-validates against. Then the administrative anchor: suspending
// an administrator takes their authority away, and a privileged write already
// in flight must not be able to commit after it. The anchor is always the last
// lock this service acquires — see mutation_authorization.go for the order.
//
// The transition is checked under the user lock, so a status read here cannot
// be stale by the time it is written.
func lockUserForStatusChange(ctx context.Context, tx pgx.Tx, userID, newStatus string) (string, error) {
	var current string
	if err := tx.QueryRow(ctx, lockUserQuery, userID).Scan(&current); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", domain.ErrNotFound
		}
		return "", fmt.Errorf("lock user for status update: %w", err)
	}
	if !domain.ValidUserStatusTransition(current, newStatus) {
		return "", domain.ErrConflict
	}
	if err := lockAdminPrincipalTx(ctx, tx, userID); err != nil {
		return "", err
	}
	return current, nil
}

// closeOutSuspendedAccess takes away what a suspended account is still holding:
// its live sessions, and any OIDC exchange code that could be redeemed into a
// new one.
//
// Both belong to the suspension transaction. Leaving either for a later step
// would let a suspended account keep working until that step ran, and the count
// returned is what the audit trail reports.
func closeOutSuspendedAccess(ctx context.Context, tx pgx.Tx, userID string) (int, error) {
	var revoked int
	if err := tx.QueryRow(ctx, revokeSessionsCTE, userID, "admin_suspension").Scan(&revoked); err != nil {
		return 0, fmt.Errorf("revoke sessions on suspension: %w", err)
	}
	if _, err := tx.Exec(ctx, invalidateExchangeCodes, userID); err != nil {
		return 0, fmt.Errorf("invalidate oidc exchange codes: %w", err)
	}
	return revoked, nil
}

// RevokeUserSessions signs one account out everywhere without changing its
// status.
//
// The existence check and the revocation share a transaction so a concurrent
// soft delete cannot leave the caller with a success for an account that no
// longer exists.
func (s *PGXUserDirectoryStore) RevokeUserSessions(ctx context.Context, userID string) (int, error) {
	if s == nil || s.pool == nil {
		return 0, domain.ErrUnavailable
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin revocation transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var status string
	if err := tx.QueryRow(ctx, lockUserQuery, userID).Scan(&status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, domain.ErrNotFound
		}
		return 0, fmt.Errorf("lock user for revocation: %w", err)
	}
	// An administrative session is only valid while the login behind it is, so
	// revoking that login revokes administrative authority too, and must
	// serialize with privileged writes the same way.
	if err := lockAdminPrincipalTx(ctx, tx, userID); err != nil {
		return 0, err
	}
	var revoked int
	if err := tx.QueryRow(ctx, revokeSessionsCTE, userID, "admin_revocation").Scan(&revoked); err != nil {
		return 0, fmt.Errorf("revoke user sessions: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit revocation transaction: %w", err)
	}
	return revoked, nil
}

// adminRoleLockKey serializes every administrative role mutation.
//
// A transaction-scoped advisory lock, taken first and released by the commit or
// rollback, so two role changes never interleave. It is the cheapest correct
// answer to the last-administrator invariant, which no row lock can express:
// the rule is about a *count across rows*, and two transactions each deleting a
// different row would both see the other's row still present under READ
// COMMITTED and both commit, leaving nobody.
//
// ponytail: one global lock for all role changes. Role grants happen a handful
// of times in the life of a deployment; if that ever stops being true, the
// upgrade is a lock keyed by capability rather than a finer-grained check.
const adminRoleLockKey = 5790001

// countSuperusersQuery counts the principals who can still administer the
// platform without restriction.
//
// It requires all three of: an active administrative principal, an active
// non-deleted account, and a role reaching admin.superuser. Anything less would
// count an administrator who cannot actually sign in, and the invariant exists
// precisely to guarantee somebody can.
const countSuperusersQuery = `
	SELECT count(DISTINCT pr.user_id)
	FROM auth.admin_principal_roles AS pr
	JOIN auth.admin_role_capabilities AS rc ON rc.role_slug = pr.role_slug
	JOIN auth.admin_principals AS p ON p.user_id = pr.user_id AND p.status = 'active'
	JOIN auth.users AS u ON u.id = pr.user_id AND u.status = 'active' AND u.deleted_at IS NULL
	WHERE rc.capability = $1`

// GrantAdminRole makes a user a platform administrator holding roleSlug.
//
// Target validity is enforced here rather than trusted from the caller: the
// account must exist, must not be soft-deleted, and must be active. Granting
// platform administration to a suspended account would create an administrator
// the platform has already decided should not be signing in.
//
// A principal that exists but has been suspended is refused rather than
// silently reactivated: suspending a principal is an out-of-band decision, and
// a role grant must not undo it as a side effect.
func (s *PGXUserDirectoryStore) GrantAdminRole(ctx context.Context, targetUserID, roleSlug, grantedBy string) error {
	if s == nil || s.pool == nil {
		return domain.ErrUnavailable
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin role grant: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, adminRoleLockKey); err != nil {
		return fmt.Errorf("lock admin roles: %w", err)
	}
	var status string
	if err := tx.QueryRow(ctx, lockUserQuery, targetUserID).Scan(&status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		return fmt.Errorf("lock target user: %w", err)
	}
	if status != domain.UserStatusActive {
		return domain.ErrConflict
	}
	var exists bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM auth.admin_roles WHERE slug = $1)`, roleSlug,
	).Scan(&exists); err != nil {
		return fmt.Errorf("lookup admin role: %w", err)
	}
	if !exists {
		return domain.ErrNotFound
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO auth.admin_principals (user_id) VALUES ($1::uuid) ON CONFLICT (user_id) DO NOTHING`,
		targetUserID,
	); err != nil {
		return fmt.Errorf("ensure admin principal: %w", err)
	}
	var principalStatus string
	if err := tx.QueryRow(ctx,
		`SELECT status FROM auth.admin_principals WHERE user_id = $1::uuid`, targetUserID,
	).Scan(&principalStatus); err != nil {
		return fmt.Errorf("read admin principal: %w", err)
	}
	if principalStatus != "active" {
		return domain.ErrConflict
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO auth.admin_principal_roles (user_id, role_slug, granted_by)
		VALUES ($1::uuid, $2, NULLIF($3, '')::uuid)
		ON CONFLICT (user_id, role_slug) DO NOTHING`,
		targetUserID, roleSlug, grantedBy,
	); err != nil {
		return fmt.Errorf("grant admin role: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit role grant: %w", err)
	}
	return nil
}

// RevokeAdminRole removes one administrative role from a principal.
//
// The last-administrator invariant is checked *after* the delete and inside the
// transaction, so the count reflects the world the commit would create rather
// than the one before it. If the delete would leave nobody able to administer
// the platform, the transaction rolls back and nothing changed.
//
// The principal row is deliberately left behind when its last role goes. It
// confers nothing — the session service refuses a principal holding no
// capability on every request, so the removal takes effect immediately — and
// deleting it would cascade through auth.admin_sessions, destroying the record
// of sessions the audit trail refers to.
func (s *PGXUserDirectoryStore) RevokeAdminRole(ctx context.Context, targetUserID, roleSlug string) error {
	if s == nil || s.pool == nil {
		return domain.ErrUnavailable
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin role revoke: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, adminRoleLockKey); err != nil {
		return fmt.Errorf("lock admin roles: %w", err)
	}
	// The authorization anchor, after the advisory lock and before the change.
	// A privileged write already holding this row finishes first and this
	// revocation waits; if this commits first, that write re-reads the roles
	// under the same lock and is refused. See mutation_authorization.go.
	if err := lockAdminPrincipalTx(ctx, tx, targetUserID); err != nil {
		return err
	}
	if err := deleteRoleGrantTx(ctx, tx, targetUserID, roleSlug); err != nil {
		return err
	}
	if err := requireRemainingSuperuserTx(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit role revoke: %w", err)
	}
	return nil
}

// deleteRoleGrantTx removes one role binding, and reports a grant that was not
// there as not found rather than as a success.
func deleteRoleGrantTx(ctx context.Context, tx pgx.Tx, targetUserID, roleSlug string) error {
	tag, err := tx.Exec(ctx,
		`DELETE FROM auth.admin_principal_roles WHERE user_id = $1::uuid AND role_slug = $2`,
		targetUserID, roleSlug,
	)
	if err != nil {
		return fmt.Errorf("revoke admin role: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// requireRemainingSuperuserTx enforces the invariant that the platform is never
// left without administration.
//
// Counted after the delete and inside the same transaction, under the advisory
// lock the caller holds, so two revocations cannot each observe the other's
// administrator and both commit.
func requireRemainingSuperuserTx(ctx context.Context, tx pgx.Tx) error {
	var remaining int
	if err := tx.QueryRow(ctx, countSuperusersQuery, string(domain.CapabilitySuperuser)).Scan(&remaining); err != nil {
		return fmt.Errorf("count remaining administrators: %w", err)
	}
	if remaining == 0 {
		return domain.ErrConflict
	}
	return nil
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

// paginate trims the extra row the queries fetch and derives the next cursor
// from the last row actually returned.
//
// Deriving the cursor from the row rather than from a counter is what keeps
// "the page ends here" and "resume from here" the same position.
func paginate[T any](items []T, limit int, cursorOf func(T) domain.Cursor) domain.Page[T] {
	if len(items) <= limit {
		return domain.Page[T]{Items: items}
	}
	trimmed := items[:limit]
	return domain.Page[T]{Items: trimmed, NextCursor: cursorOf(trimmed[limit-1]).Encode()}
}

// likeEscapeChar is the character likePattern prefixes a literal wildcard with.
//
// It is also PostgreSQL's default for LIKE/ILIKE, but likeEscapeClause names it
// anyway rather than relying on that default: a search filter that quietly
// stops filtering is not the kind of behaviour to leave implicit, and a reader
// of the query should not have to know a default to know what the backslashes
// in the bound pattern mean.
const likeEscapeChar = `\`

// likeEscapeClause is appended to every ILIKE predicate in this package.
//
// E'\\' rather than '\': an escape-string constant is exactly one backslash
// whatever standard_conforming_strings is set to, so the clause cannot become a
// parse hazard on a server configured differently from the one it was written
// on.
const likeEscapeClause = `ESCAPE E'\\'`

// likePattern turns a search term into an ILIKE pattern with the wildcards
// escaped.
//
// Without the escape a search for "%" matches everyone and a search for "_"
// matches one character of anything — not an injection (the pattern is a bound
// parameter) but a filter that quietly stops filtering, which is worse on a
// screen an operator uses to find one person.
//
// The order of the three replacements is not the order of the arguments:
// strings.NewReplacer scans once and never re-scans what it wrote, so the
// backslash it inserts before a % is not itself escaped afterwards. The escape
// character is nonetheless listed first, because that is the order the rule is
// stated in and a reader should not have to know the scanner's semantics to
// believe it.
//
// The result is one backslash before each literal wildcard, not two. Go's %q
// renders that single 0x5c as \\, which is a property of the display and not
// of the value — TestLikePattern_ProducesOneEscapeBytePerWildcard pins the
// bytes, and the PostgreSQL suite pins what the server does with them.
func likePattern(query string) string {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return ""
	}
	escaped := strings.NewReplacer(
		likeEscapeChar, likeEscapeChar+likeEscapeChar,
		`%`, likeEscapeChar+`%`,
		`_`, likeEscapeChar+`_`,
	).Replace(trimmed)
	return "%" + escaped + "%"
}

// nullableText passes an unset filter as SQL NULL, which every predicate above
// reads as "this filter is not applied".
func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableBool(value *bool) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableCursorTime(cursor domain.Cursor) any {
	if cursor.IsZero() {
		return nil
	}
	return cursor.At
}
