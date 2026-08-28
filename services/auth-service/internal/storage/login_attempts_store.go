package storage

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
)

// PGXLoginAttemptsStore implements retrieval of login attempts using a pgx connection pool.
type PGXLoginAttemptsStore struct {
	pool Pool
}

// NewPGXLoginAttemptsStore creates a PGXLoginAttemptsStore backed by the given pool.
func NewPGXLoginAttemptsStore(pool Pool) *PGXLoginAttemptsStore {
	return &PGXLoginAttemptsStore{pool: pool}
}

// GetUserFailedAttempts returns up to limit failed login attempts for userID, ordered by
// (created_at DESC, id DESC). When cursor is non-nil, only rows before the cursor are returned.
// Pass limit+1 to detect whether a next page exists.
func (s *PGXLoginAttemptsStore) GetUserFailedAttempts(
	ctx context.Context,
	userID string,
	limit int,
	cursor *domain.LoginAttemptsCursor,
) ([]domain.LoginAttempt, error) {
	var cursorTime any
	var cursorID any
	if cursor != nil {
		cursorTime = cursor.CreatedAt
		cursorID = cursor.ID
	}

	rows, err := s.pool.Query(ctx, `
		SELECT id, email, failure_reason,
		       ip_address::text, user_agent, created_at
		FROM auth.login_attempts
		WHERE user_id = $1
		  AND success = false
		  AND ($2::timestamptz IS NULL OR (created_at, id) < ($2, $3))
		ORDER BY created_at DESC, id DESC
		LIMIT $4`,
		userID, cursorTime, cursorID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query login attempts: %w", err)
	}
	defer rows.Close()

	var result []domain.LoginAttempt
	for rows.Next() {
		var a domain.LoginAttempt
		var ipAddr pgtype.Text
		var userAgent pgtype.Text
		if err := rows.Scan(&a.ID, &a.Email, &a.FailureReason, &ipAddr, &userAgent, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan login attempt: %w", err)
		}
		if ipAddr.Valid {
			a.IPAddress = ipAddr.String
		}
		if userAgent.Valid {
			a.UserAgent = userAgent.String
		}
		result = append(result, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate login attempts: %w", err)
	}
	return result, nil
}
