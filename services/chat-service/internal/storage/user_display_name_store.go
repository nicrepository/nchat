package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// UserDisplayNameStore resolves a user's current display name from auth.users.
type UserDisplayNameStore interface {
	GetDisplayName(ctx context.Context, userID string) (string, error)
}

// PGXUserDisplayNameStore implements UserDisplayNameStore via a single,
// primary-key-indexed lookup against auth.users. Both schemas live in the
// same PostgreSQL database as chat.*, so no cross-service HTTP call is
// required — the same precedent PGXSessionValidator already establishes.
type PGXUserDisplayNameStore struct {
	pool Pool
}

// NewPGXUserDisplayNameStore returns a PGXUserDisplayNameStore backed by pool.
func NewPGXUserDisplayNameStore(pool Pool) *PGXUserDisplayNameStore {
	return &PGXUserDisplayNameStore{pool: pool}
}

// GetDisplayName returns userID's current display_name, or domain.ErrNotFound
// if no such user exists. display_name is TEXT NOT NULL at the schema level,
// but is defensively coalesced anyway, matching every other read of this
// column in this service (see message_store.go's listMessageColumns).
func (s *PGXUserDisplayNameStore) GetDisplayName(ctx context.Context, userID string) (string, error) {
	var name string
	err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(display_name, '') FROM auth.users WHERE id = $1`,
		userID,
	).Scan(&name)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", domain.ErrNotFound
		}
		return "", fmt.Errorf("get user display name: %w", err)
	}
	return name, nil
}
