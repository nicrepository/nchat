package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/nicrepository/nchat/libs/go/platform/authsession"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// SessionValidator validates an access token's session against the auth schema.
// The implementation queries auth.user_sessions and auth.users in the shared database.
type SessionValidator interface {
	ValidateActiveSession(ctx context.Context, userID, sessionID string) error
}

// PGXSessionValidator implements SessionValidator via a direct SQL query against
// auth.user_sessions and auth.users. Both schemas live in the same PostgreSQL
// database as chat.*, so no cross-service HTTP call is required.
type PGXSessionValidator struct {
	pool Pool
}

// NewPGXSessionValidator returns a PGXSessionValidator backed by pool.
func NewPGXSessionValidator(pool Pool) *PGXSessionValidator {
	return &PGXSessionValidator{pool: pool}
}

// ValidateActiveSession checks that:
//   - The session exists (by sessionID + userID)
//   - The session is not revoked
//   - The session has not exceeded its idle or absolute expiry
//   - The owning user is active and not deleted
//
// Returns domain.ErrInvalidToken when the session is absent or invalid.
// Returns a wrapped error on unexpected database failure.
func (s *PGXSessionValidator) ValidateActiveSession(ctx context.Context, userID, sessionID string) error {
	var active bool
	err := s.pool.QueryRow(ctx, authsession.ActiveSessionCTE+`
		SELECT true
		FROM active_session`,
		sessionID, userID,
	).Scan(&active)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrInvalidToken
		}
		return fmt.Errorf("validate active session: %w", err)
	}
	return nil
}
