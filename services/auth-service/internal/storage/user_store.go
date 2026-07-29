package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
)

const pgCodeUniqueViolation = "23505"

// UserStore is the persistence interface for user operations.
type UserStore interface {
	CreateUser(ctx context.Context, input domain.CreateUserInput, passwordHash string) (domain.User, error)
	GetPolicySettings(ctx context.Context) (domain.PolicySettings, error)
	GetUserByID(ctx context.Context, id string) (domain.User, error)
	// GetSelfProfile returns the minimal own-profile fields for an active user
	// (id, display_name, avatar_url). ErrNotFound when the user is missing,
	// deleted, or not active.
	GetSelfProfile(ctx context.Context, id string) (domain.SelfProfile, error)
	UpdateUserStatus(ctx context.Context, id, status string) (domain.User, error)
	// SetAvatarURL points the user's avatar_url at url and returns the previous
	// value (empty when there was none) so the caller can delete the orphaned
	// file after the row is committed. Only an active, non-deleted user is
	// updated; ErrNotFound otherwise.
	SetAvatarURL(ctx context.Context, userID, url string) (previous string, err error)
	// ClearAvatarURL sets avatar_url to NULL and returns the previous value.
	// It is idempotent: clearing an already-empty avatar returns "" with no
	// error, as long as the user exists and is active.
	ClearAvatarURL(ctx context.Context, userID string) (previous string, err error)
	// GetAdminWorkspaceID returns the workspace userID administers. It is the
	// only source of workspace identity for the admin endpoints: nothing the
	// browser sends reaches it. ErrForbidden when the caller administers no
	// active workspace, which is also the answer for a plain member or guest.
	GetAdminWorkspaceID(ctx context.Context, userID string) (string, error)
	// ListWorkspaceUsers returns at most limit members of workspaceID, ordered
	// deterministically and resuming after cursor when one is given. The caller
	// must have obtained workspaceID from GetAdminWorkspaceID.
	ListWorkspaceUsers(ctx context.Context, workspaceID string, limit int, anchor *domain.WorkspaceUserAnchor) ([]domain.WorkspaceUser, error)
	// GetWorkspaceUserAnchor resolves the ordering position of userID inside
	// workspaceID, so a cursor need not carry it. Found is false when that user
	// is no longer a member.
	GetWorkspaceUserAnchor(ctx context.Context, workspaceID, userID string) (domain.WorkspaceUserAnchor, error)
}

// PGXUserStore implements UserStore using a pgx connection pool.
type PGXUserStore struct {
	pool Pool
}

func NewPGXUserStore(pool Pool) *PGXUserStore {
	return &PGXUserStore{pool: pool}
}

func (s *PGXUserStore) GetPolicySettings(ctx context.Context) (domain.PolicySettings, error) {
	var p domain.PolicySettings
	err := s.pool.QueryRow(ctx, `
		SELECT min_password_length, require_uppercase, require_lowercase,
		       require_number, require_symbol, failed_login_limit,
		       failed_login_window_minutes, failed_login_lockout_minutes,
		       session_idle_timeout_minutes, max_devices_per_user,
		       password_reset_token_ttl_minutes, invite_token_ttl_hours
		FROM auth.auth_policy_settings
		WHERE id = 1`).Scan(
		&p.MinPasswordLength, &p.RequireUppercase, &p.RequireLowercase,
		&p.RequireNumber, &p.RequireSymbol, &p.FailedLoginLimit,
		&p.FailedLoginWindowMinutes, &p.FailedLoginLockoutMinutes,
		&p.SessionIdleTimeoutMinutes, &p.MaxDevicesPerUser,
		&p.PasswordResetTokenTTLMinutes, &p.InviteTokenTTLHours,
	)
	if err != nil {
		return domain.PolicySettings{}, fmt.Errorf("get policy settings: %w", err)
	}
	return p, nil
}

func (s *PGXUserStore) CreateUser(ctx context.Context, input domain.CreateUserInput, passwordHash string) (domain.User, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.User{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var u domain.User
	err = tx.QueryRow(ctx, `
		INSERT INTO auth.users (email, display_name, full_name, status, auth_source, email_verified_at)
		VALUES ($1, $2, $3, 'active', 'manual', now())
		RETURNING id, email::text, display_name, COALESCE(full_name, ''), status, auth_source,
		          email_verified_at, created_at, updated_at`,
		input.Email, input.DisplayName, nullableString(input.FullName),
	).Scan(
		&u.ID, &u.Email, &u.DisplayName, &u.FullName, &u.Status, &u.AuthSource,
		&u.EmailVerifiedAt, &u.CreatedAt, &u.UpdatedAt,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgCodeUniqueViolation {
			return domain.User{}, domain.ErrDuplicateEmail
		}
		return domain.User{}, fmt.Errorf("insert user: %w", err)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO auth.user_password_credentials (user_id, password_hash, must_change_password)
		VALUES ($1, $2, $3)`,
		u.ID, passwordHash, input.MustChangePassword,
	)
	if err != nil {
		return domain.User{}, fmt.Errorf("insert password credential: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return domain.User{}, fmt.Errorf("commit tx: %w", err)
	}
	return u, nil
}

func (s *PGXUserStore) GetSelfProfile(ctx context.Context, id string) (domain.SelfProfile, error) {
	var p domain.SelfProfile
	var avatar *string
	err := s.pool.QueryRow(ctx, `
		SELECT id, display_name, avatar_url
		FROM auth.users
		WHERE id = $1
		  AND status = 'active'
		  AND deleted_at IS NULL`,
		id,
	).Scan(&p.ID, &p.DisplayName, &avatar)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.SelfProfile{}, domain.ErrNotFound
		}
		return domain.SelfProfile{}, fmt.Errorf("get self profile: %w", err)
	}
	if avatar != nil {
		p.AvatarURL = *avatar
	}
	return p, nil
}

func (s *PGXUserStore) GetUserByID(ctx context.Context, id string) (domain.User, error) {
	var u domain.User
	err := s.pool.QueryRow(ctx, `
		SELECT id, email::text, display_name, COALESCE(full_name, ''), status, auth_source,
		       email_verified_at, created_at, updated_at
		FROM auth.users
		WHERE id = $1
		  AND deleted_at IS NULL`,
		id,
	).Scan(&u.ID, &u.Email, &u.DisplayName, &u.FullName, &u.Status, &u.AuthSource,
		&u.EmailVerifiedAt, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.User{}, domain.ErrNotFound
		}
		return domain.User{}, fmt.Errorf("get user by id: %w", err)
	}
	return u, nil
}

func (s *PGXUserStore) UpdateUserStatus(ctx context.Context, id, newStatus string) (domain.User, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.User{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Lock the user row and read current status to validate the transition atomically.
	var currentStatus string
	err = tx.QueryRow(ctx, `
		SELECT status FROM auth.users
		WHERE id = $1 AND deleted_at IS NULL
		FOR UPDATE`,
		id,
	).Scan(&currentStatus)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.User{}, domain.ErrNotFound
		}
		return domain.User{}, fmt.Errorf("lock user for status update: %w", err)
	}

	if err := domain.ValidateStatusTransition(currentStatus, newStatus); err != nil {
		return domain.User{}, err
	}

	var u domain.User
	err = tx.QueryRow(ctx, `
		UPDATE auth.users
		SET status = $2, updated_at = now()
		WHERE id = $1
		  AND deleted_at IS NULL
		RETURNING id, email::text, display_name, COALESCE(full_name, ''), status, auth_source,
		          email_verified_at, created_at, updated_at`,
		id, newStatus,
	).Scan(&u.ID, &u.Email, &u.DisplayName, &u.FullName, &u.Status, &u.AuthSource,
		&u.EmailVerifiedAt, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return domain.User{}, fmt.Errorf("update user status: %w", err)
	}

	// Revoke all active sessions and refresh tokens in the same transaction so that
	// a suspension always produces a consistent state (status suspended + no active sessions).
	// Also invalidate pending OIDC exchange codes for the user, because a code created before
	// suspension could otherwise be consumed after reactivation to return pre-suspension tokens.
	if newStatus == "suspended" {
		if _, err := tx.Exec(ctx, `
			WITH revoked AS (
			    UPDATE auth.user_sessions
			    SET revoked_at = now(), revoked_reason = 'admin_suspension'
			    WHERE user_id = $1 AND revoked_at IS NULL
			    RETURNING id
			)
			UPDATE auth.refresh_token_history
			SET status = 'revoked', revoked_at = now()
			WHERE session_id IN (SELECT id FROM revoked)
			  AND status = 'active'`,
			id,
		); err != nil {
			return domain.User{}, fmt.Errorf("revoke sessions on suspension: %w", err)
		}

		// Invalidate any pending OIDC exchange codes for this user.
		// user_json->>'id' stores the user UUID as text; no migration required.
		// Activation intentionally does NOT reset used_at — a code invalidated by
		// suspension cannot be replayed even if the user is later reactivated.
		if _, err := tx.Exec(ctx, `
			UPDATE auth.oidc_exchange_codes
			SET used_at = now()
			WHERE used_at IS NULL
			  AND expires_at > now()
			  AND user_json->>'id' = $1`,
			id,
		); err != nil {
			return domain.User{}, fmt.Errorf("invalidate oidc exchange codes on suspension: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return domain.User{}, fmt.Errorf("commit tx: %w", err)
	}
	return u, nil
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// SetAvatarURL updates the avatar reference for an active user, returning the
// prior value so the caller can clean up the replaced file. The UPDATE is
// gated on status='active' AND deleted_at IS NULL so a suspended or scrubbed
// user can never (re)acquire an avatar through this path.
func (s *PGXUserStore) SetAvatarURL(ctx context.Context, userID, url string) (string, error) {
	return s.swapAvatarURL(ctx, userID, nullableString(url))
}

// ClearAvatarURL sets avatar_url to NULL for an active user and returns the
// prior value. Clearing an already-null avatar is not an error.
func (s *PGXUserStore) ClearAvatarURL(ctx context.Context, userID string) (string, error) {
	return s.swapAvatarURL(ctx, userID, nil)
}

func (s *PGXUserStore) swapAvatarURL(ctx context.Context, userID string, newValue any) (string, error) {
	// The CTE captures the pre-update avatar_url (RETURNING on the UPDATE would
	// otherwise expose the new value); the row lock also serialises concurrent
	// avatar swaps for the same user.
	var previous *string
	err := s.pool.QueryRow(ctx, `
		WITH target AS (
		    SELECT id, avatar_url
		    FROM auth.users
		    WHERE id = $1 AND status = 'active' AND deleted_at IS NULL
		    FOR UPDATE
		)
		UPDATE auth.users u
		SET avatar_url = $2, updated_at = now()
		FROM target
		WHERE u.id = target.id
		RETURNING target.avatar_url`,
		userID, newValue,
	).Scan(&previous)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", domain.ErrNotFound
		}
		return "", fmt.Errorf("swap avatar url: %w", err)
	}
	if previous == nil {
		return "", nil
	}
	return *previous, nil
}

// GetAdminWorkspaceID resolves the workspace userID administers.
//
// This is the workspace-scoping decision for the whole admin API, taken from
// the caller's own membership row and nothing else. The three conditions are
// each load-bearing: the role check is the authorization (a member or guest
// matches no row and gets ErrForbidden), the membership status check keeps a
// suspended or departed admin out, and the workspace status check keeps an
// archived workspace from being administered.
//
// A caller who administers several workspaces resolves to the oldest
// membership, which is stable across calls, so the list and any follow-up
// mutation always agree on one workspace.
func (s *PGXUserStore) GetAdminWorkspaceID(ctx context.Context, userID string) (string, error) {
	var workspaceID string
	err := s.pool.QueryRow(ctx, `
		SELECT wm.workspace_id::text
		FROM chat.workspace_members wm
		JOIN chat.workspaces w
		  ON w.id = wm.workspace_id AND w.status = 'active'
		WHERE wm.user_id = $1::uuid
		  AND wm.status = 'active'
		  AND wm.role IN ('owner', 'admin')
		ORDER BY wm.joined_at, wm.workspace_id
		LIMIT 1`,
		userID,
	).Scan(&workspaceID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", domain.ErrForbidden
		}
		return "", fmt.Errorf("get admin workspace: %w", err)
	}
	return workspaceID, nil
}

// workspaceUserSortKey is the ordering expression, written once and reused by
// the SELECT list, the WHERE clause and the ORDER BY. Repeating it by hand in
// three places is how a keyset paginator starts skipping or repeating rows.
const workspaceUserSortKey = `lower(coalesce(nullif(u.display_name, ''), u.email::text))`

// ListWorkspaceUsers returns one page of the members of workspaceID.
//
// The join against chat.workspace_members is the tenant boundary: rows are
// reachable only through a membership of the workspace passed in, so there is
// no result set that spans workspaces even if auth.users holds accounts from
// several — and no cursor value can widen it, because the workspace filter is
// a separate, always-present predicate.
//
// Ordering is (sort key, id). The id tiebreak is what makes the order total:
// two people with the same display name would otherwise have an undefined
// relative position, and a keyset cursor over an unstable order silently skips
// or repeats rows between pages.
//
// Resumption uses a row-value comparison rather than OFFSET, so page N costs
// the same as page 1 and concurrent inserts cannot shift the window.
//
// Members who left are excluded; suspended ones are not, because deciding
// whether to reactivate them is what the screen exists for. Soft-deleted
// accounts never appear.
func (s *PGXUserStore) ListWorkspaceUsers(ctx context.Context, workspaceID string, limit int, anchor *domain.WorkspaceUserAnchor) ([]domain.WorkspaceUser, error) {
	var cursorSortKey, cursorUserID any
	if anchor != nil {
		cursorSortKey = anchor.SortKey
		cursorUserID = anchor.UserID
	}

	rows, err := s.pool.Query(ctx, `
		SELECT u.id::text, u.email::text, u.display_name, COALESCE(u.full_name, ''),
		       u.status, u.auth_source, u.created_at
		FROM chat.workspace_members wm
		JOIN auth.users u
		  ON u.id = wm.user_id AND u.deleted_at IS NULL
		WHERE wm.workspace_id = $1::uuid
		  AND wm.status <> 'left'
		  AND (
		        $2::text IS NULL
		     OR (`+workspaceUserSortKey+`, u.id::text) > ($2::text, $3::text)
		      )
		ORDER BY `+workspaceUserSortKey+`, u.id
		LIMIT $4`,
		workspaceID, cursorSortKey, cursorUserID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list workspace users: %w", err)
	}
	defer rows.Close()

	users := make([]domain.WorkspaceUser, 0)
	for rows.Next() {
		var u domain.WorkspaceUser
		if err := rows.Scan(&u.ID, &u.Email, &u.DisplayName, &u.FullName,
			&u.Status, &u.AuthSource, &u.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan workspace user: %w", err)
		}
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate workspace users: %w", err)
	}
	return users, nil
}

// GetWorkspaceUserAnchor returns the ordering position of userID within
// workspaceID.
//
// It exists so the cursor can name a row by id alone instead of carrying the
// sort key, which is a display name or an e-mail address and therefore must not
// travel in a query string. The lookup is by primary key on both tables, so the
// cost is a constant per page.
//
// The membership join is not decoration: it is what stops a cursor naming a user
// outside the caller's workspace from resolving to a real position.
func (s *PGXUserStore) GetWorkspaceUserAnchor(ctx context.Context, workspaceID, userID string) (domain.WorkspaceUserAnchor, error) {
	var sortKey string
	err := s.pool.QueryRow(ctx, `
		SELECT `+workspaceUserSortKey+`
		FROM chat.workspace_members wm
		JOIN auth.users u
		  ON u.id = wm.user_id AND u.deleted_at IS NULL
		WHERE wm.workspace_id = $1::uuid
		  AND wm.user_id = $2::uuid
		  AND wm.status <> 'left'`,
		workspaceID, userID,
	).Scan(&sortKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.WorkspaceUserAnchor{}, nil
	}
	if err != nil {
		return domain.WorkspaceUserAnchor{}, fmt.Errorf("get workspace user anchor: %w", err)
	}
	return domain.WorkspaceUserAnchor{SortKey: sortKey, Found: true}, nil
}
