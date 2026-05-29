package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
)

// LoginAttemptsStore is the persistence interface for fetching login attempts.
type LoginAttemptsStore interface {
	GetUserFailedAttempts(ctx context.Context, userID string, limit int, cursor *domain.LoginAttemptsCursor) ([]domain.LoginAttempt, error)
}

// LoginAttemptsService handles business logic for the login attempts endpoint.
type LoginAttemptsService struct {
	store LoginAttemptsStore
}

// NewLoginAttemptsService creates a LoginAttemptsService backed by the given store.
func NewLoginAttemptsService(store LoginAttemptsStore) *LoginAttemptsService {
	return &LoginAttemptsService{store: store}
}

// GetMyAttempts returns a page of failed login attempts for userID.
// limit is clamped to [1, 100]; 0 or negative defaults to 50.
// cursorStr is a base64-encoded JSON LoginAttemptsCursor; empty string starts from the beginning.
// Returns the page rows, an opaque next-page cursor string (empty if no more pages), and any error.
func (s *LoginAttemptsService) GetMyAttempts(
	ctx context.Context,
	userID string,
	limit int,
	cursorStr string,
) ([]domain.LoginAttempt, string, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}

	var cursor *domain.LoginAttemptsCursor
	if cursorStr != "" {
		decoded, err := base64.StdEncoding.DecodeString(cursorStr)
		if err != nil {
			return nil, "", fmt.Errorf("%w: invalid cursor encoding", domain.ErrInvalidInput)
		}
		var c domain.LoginAttemptsCursor
		if err := json.Unmarshal(decoded, &c); err != nil {
			return nil, "", fmt.Errorf("%w: invalid cursor format", domain.ErrInvalidInput)
		}
		cursor = &c
	}

	rows, err := s.store.GetUserFailedAttempts(ctx, userID, limit+1, cursor)
	if err != nil {
		return nil, "", err
	}

	var nextCursor string
	if len(rows) == limit+1 {
		last := rows[limit-1]
		rows = rows[:limit]
		c := domain.LoginAttemptsCursor{CreatedAt: last.CreatedAt, ID: last.ID}
		b, err := json.Marshal(c)
		if err != nil {
			return nil, "", fmt.Errorf("encode next cursor: %w", err)
		}
		nextCursor = base64.StdEncoding.EncodeToString(b)
	}

	return rows, nextCursor, nil
}
