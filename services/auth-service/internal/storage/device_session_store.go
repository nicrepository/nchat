package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nicrepository/nchat/libs/go/platform/authsession"
	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
)

// PGXDeviceSessionStore implements device and session management persistence.
type PGXDeviceSessionStore struct {
	pool Pool
}

// NewPGXDeviceSessionStore creates a PGXDeviceSessionStore backed by the given pool.
func NewPGXDeviceSessionStore(pool Pool) *PGXDeviceSessionStore {
	return &PGXDeviceSessionStore{pool: pool}
}

// ListSessions returns sessions for userID ordered newest first.
// includeRevoked=false returns only active sessions; true returns all.
func (s *PGXDeviceSessionStore) ListSessions(ctx context.Context, userID string, includeRevoked bool, limit int) ([]domain.SessionInfo, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, device_id, created_at, last_seen_at,
		       idle_expires_at, absolute_expires_at, revoked_at,
		       ip_address::text, user_agent
		FROM auth.user_sessions
		WHERE user_id = $1
		  AND ($2 OR revoked_at IS NULL)
		ORDER BY created_at DESC, id DESC
		LIMIT $3`,
		userID, includeRevoked, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()

	var sessions []domain.SessionInfo
	for rows.Next() {
		var si domain.SessionInfo
		var ip, ua pgtype.Text
		if err := rows.Scan(
			&si.ID, &si.DeviceID, &si.CreatedAt, &si.LastSeenAt,
			&si.IdleExpiresAt, &si.AbsoluteExpiresAt, &si.RevokedAt,
			&ip, &ua,
		); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		if ip.Valid {
			si.IPAddress = ip.String
		}
		if ua.Valid {
			si.UserAgent = ua.String
		}
		sessions = append(sessions, si)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sessions: %w", err)
	}
	return sessions, nil
}

// RevokeSession revokes the session identified by sessionID for userID.
// Returns ErrNotFound if the session does not exist or belongs to a different user.
// Idempotent: already-revoked own session returns nil.
func (s *PGXDeviceSessionStore) RevokeSession(ctx context.Context, sessionID, userID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var id string
	if err := tx.QueryRow(ctx, `
		SELECT id FROM auth.user_sessions
		WHERE id = $1 AND user_id = $2
		FOR UPDATE`,
		sessionID, userID,
	).Scan(&id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		return fmt.Errorf("lock session: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE auth.user_sessions
		SET revoked_at = now(), revoked_reason = 'user_revoked'
		WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL`,
		sessionID, userID,
	); err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE auth.refresh_token_history
		SET status = 'revoked', revoked_at = now()
		WHERE session_id = $1 AND status = 'active'`,
		sessionID,
	); err != nil {
		return fmt.Errorf("revoke refresh token history: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

// RevokeAllSessionsExcept revokes all sessions for userID except exceptSessionID,
// and their active refresh token history. Uses a CTE to avoid collecting IDs separately.
func (s *PGXDeviceSessionStore) RevokeAllSessionsExcept(ctx context.Context, userID, exceptSessionID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, `
		WITH revoked AS (
		    UPDATE auth.user_sessions
		    SET revoked_at = now(), revoked_reason = 'user_revoked_all'
		    WHERE user_id = $1
		      AND id <> $2
		      AND revoked_at IS NULL
		    RETURNING id
		)
		UPDATE auth.refresh_token_history
		SET status = 'revoked', revoked_at = now()
		WHERE session_id IN (SELECT id FROM revoked)
		  AND status = 'active'`,
		userID, exceptSessionID,
	); err != nil {
		return fmt.Errorf("revoke all sessions: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

// ValidateActiveSession verifies that sessionID is the current active database session
// for userID and that the owning user can still authenticate.
func (s *PGXDeviceSessionStore) ValidateActiveSession(ctx context.Context, userID, sessionID string) error {
	var active bool
	if err := s.pool.QueryRow(ctx, authsession.ActiveSessionCTE+`
		SELECT true AS active
		FROM active_session`,
		sessionID, userID,
	).Scan(&active); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrInvalidToken
		}
		return fmt.Errorf("validate active session: %w", err)
	}
	return nil
}

// ListDevices returns devices for userID ordered by last_seen newest first.
// currentSessionID is used to mark one device as current; pass "" to skip.
// includeRevoked=false returns only active devices; true returns all.
func (s *PGXDeviceSessionStore) ListDevices(ctx context.Context, userID, currentSessionID string, includeRevoked bool, limit int) ([]domain.DeviceInfo, domain.DeviceSessionPolicy, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT d.id, d.display_name, d.platform,
		       d.last_ip::text, d.first_seen_at, d.last_seen_at, d.revoked_at,
		       COUNT(s.id) FILTER (WHERE s.revoked_at IS NULL) AS session_count,
		       CASE WHEN $2 <> '' THEN
		           COALESCE(d.id = (SELECT device_id FROM auth.user_sessions
		                            WHERE id = $2::uuid AND user_id = $1), false)
		       ELSE false END AS current
		FROM auth.user_devices AS d
		LEFT JOIN auth.user_sessions AS s ON s.device_id = d.id AND s.user_id = d.user_id
		WHERE d.user_id = $1
		  AND ($3 OR d.revoked_at IS NULL)
		GROUP BY d.id
		ORDER BY d.last_seen_at DESC, d.id DESC
		LIMIT $4`,
		userID, currentSessionID, includeRevoked, limit,
	)
	if err != nil {
		return nil, domain.DeviceSessionPolicy{}, fmt.Errorf("list devices: %w", err)
	}
	defer rows.Close()

	var devices []domain.DeviceInfo
	for rows.Next() {
		var di domain.DeviceInfo
		var lastIP pgtype.Text
		var current bool
		if err := rows.Scan(
			&di.ID, &di.DisplayName, &di.Platform,
			&lastIP, &di.FirstSeenAt, &di.LastSeenAt, &di.RevokedAt,
			&di.SessionCount, &current,
		); err != nil {
			return nil, domain.DeviceSessionPolicy{}, fmt.Errorf("scan device: %w", err)
		}
		if lastIP.Valid {
			di.LastIP = lastIP.String
		}
		di.Current = current
		devices = append(devices, di)
	}
	if err := rows.Err(); err != nil {
		return nil, domain.DeviceSessionPolicy{}, fmt.Errorf("iterate devices: %w", err)
	}

	var policy domain.DeviceSessionPolicy
	if err := s.pool.QueryRow(ctx, `
		SELECT max_devices_per_user FROM auth.auth_policy_settings WHERE id = 1`,
	).Scan(&policy.MaxDevicesPerUser); err != nil {
		return nil, domain.DeviceSessionPolicy{}, fmt.Errorf("get device policy: %w", err)
	}

	return devices, policy, nil
}

// RevokeDevice revokes the device identified by deviceID for userID, along with
// all active sessions and their refresh token history, using a single CTE.
// Returns ErrNotFound if the device does not exist or belongs to a different user.
// Idempotent: already-revoked own device returns nil.
func (s *PGXDeviceSessionStore) RevokeDevice(ctx context.Context, deviceID, userID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var id string
	if err := tx.QueryRow(ctx, `
		SELECT id FROM auth.user_devices
		WHERE id = $1 AND user_id = $2
		FOR UPDATE`,
		deviceID, userID,
	).Scan(&id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		return fmt.Errorf("lock device: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		WITH revoked_device AS (
		    UPDATE auth.user_devices
		    SET revoked_at = now()
		    WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL
		),
		revoked_sessions AS (
		    UPDATE auth.user_sessions
		    SET revoked_at = now(), revoked_reason = 'device_revoked'
		    WHERE device_id = $1 AND user_id = $2 AND revoked_at IS NULL
		    RETURNING id
		)
		UPDATE auth.refresh_token_history
		SET status = 'revoked', revoked_at = now()
		WHERE session_id IN (SELECT id FROM revoked_sessions)
		  AND status = 'active'`,
		deviceID, userID,
	); err != nil {
		return fmt.Errorf("revoke device: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

// UpdateDeviceDisplayName sets the display_name of an active device.
// Returns ErrNotFound if the device does not exist, is revoked, or belongs to a different user.
func (s *PGXDeviceSessionStore) UpdateDeviceDisplayName(ctx context.Context, deviceID, userID, name string) error {
	result, err := s.pool.Exec(ctx, `
		UPDATE auth.user_devices
		SET display_name = $1
		WHERE id = $2 AND user_id = $3 AND revoked_at IS NULL`,
		nullableString(name), deviceID, userID,
	)
	if err != nil {
		return fmt.Errorf("update device display name: %w", err)
	}
	if result.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}
