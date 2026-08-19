package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/nicrepository/nchat/libs/go/platform/authsession"
	"github.com/nicrepository/nchat/services/search-service/internal/domain"
)

type PGXSessionValidator struct{ pool Queryer }

func NewPGXSessionValidator(pool Queryer) *PGXSessionValidator {
	return &PGXSessionValidator{pool: pool}
}
func (v *PGXSessionValidator) ValidateActiveSession(ctx context.Context, userID, sessionID string) error {
	var ok bool
	err := v.pool.QueryRow(ctx, authsession.ActiveSessionCTE+` SELECT true FROM active_session`, sessionID, userID).Scan(&ok)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrUnauthorized
	}
	if err != nil {
		return fmt.Errorf("validate active session: %w", err)
	}
	return nil
}
