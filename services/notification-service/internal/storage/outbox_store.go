package storage

import (
	"context"
	"fmt"
)

// OutboxRow is one row claimed from auth.email_outbox for SMTP delivery.
type OutboxRow struct {
	ID               string
	Kind             string
	ToEmail          string
	Subject          string
	EncryptedPayload string
	Attempts         int
}

// OutboxStore is the persistence interface for SMTP worker outbox operations.
type OutboxStore interface {
	ClaimBatch(ctx context.Context, maxAttempts, deadlineSeconds, batchSize int) ([]OutboxRow, error)
	FinaliseSuccess(ctx context.Context, id string) error
	FinaliseFailure(ctx context.Context, id string, attempts int, lastError string, backoffSec int, maxAttempts int) error
}

// PGXOutboxStore implements OutboxStore using a pgx connection pool.
type PGXOutboxStore struct {
	pool Pool
}

// NewPGXOutboxStore creates a PGXOutboxStore backed by the given pool.
func NewPGXOutboxStore(pool Pool) *PGXOutboxStore {
	return &PGXOutboxStore{pool: pool}
}

// ClaimBatch claims up to batchSize rows for delivery and marks them as processing.
func (s *PGXOutboxStore) ClaimBatch(ctx context.Context, maxAttempts, deadlineSeconds, batchSize int) ([]OutboxRow, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	rows, err := tx.Query(ctx, `
		SELECT id::text, kind, to_email, subject, payload::text, attempts
		FROM auth.email_outbox
		WHERE (
			(status = 'pending' AND (next_retry_at IS NULL OR next_retry_at <= now()))
			OR (status = 'processing' AND processing_deadline_at <= now())
		)
		AND attempts < $1
		ORDER BY next_retry_at NULLS FIRST, created_at, id
		LIMIT $2
		FOR UPDATE SKIP LOCKED`, maxAttempts, batchSize)
	if err != nil {
		return nil, fmt.Errorf("select claimable outbox rows: %w", err)
	}
	defer rows.Close()

	claimed := make([]OutboxRow, 0, batchSize)
	for rows.Next() {
		var row OutboxRow
		if err := rows.Scan(&row.ID, &row.Kind, &row.ToEmail, &row.Subject, &row.EncryptedPayload, &row.Attempts); err != nil {
			return nil, fmt.Errorf("scan outbox row: %w", err)
		}
		claimed = append(claimed, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate outbox rows: %w", err)
	}

	for _, row := range claimed {
		if _, err := tx.Exec(ctx, `
			UPDATE auth.email_outbox
			SET status = 'processing',
				processing_started_at = now(),
				processing_deadline_at = now() + ($1 * interval '1 second')
			WHERE id = $2::uuid`, deadlineSeconds, row.ID); err != nil {
			return nil, fmt.Errorf("update outbox row %s to processing: %w", row.ID, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit claim batch: %w", err)
	}
	return claimed, nil
}

// FinaliseSuccess marks a claimed outbox row as sent.
func (s *PGXOutboxStore) FinaliseSuccess(ctx context.Context, id string) error {
	if _, err := s.pool.Exec(ctx, `
		UPDATE auth.email_outbox
		SET status = 'sent',
			sent_at = now(),
			last_error = NULL,
			next_retry_at = NULL,
			processing_started_at = NULL,
			processing_deadline_at = NULL
		WHERE id = $1::uuid`, id); err != nil {
		return fmt.Errorf("finalise success %s: %w", id, err)
	}
	return nil
}

// FinaliseFailure records an SMTP failure and either retries or marks the row failed.
// The retry delay uses multiplicative backoff: backoffSec * attempts seconds.
func (s *PGXOutboxStore) FinaliseFailure(ctx context.Context, id string, attempts int, lastError string, backoffSec int, maxAttempts int) error {
	if _, err := s.pool.Exec(ctx, `
		UPDATE auth.email_outbox
		SET attempts = $1,
			last_error = $2,
			next_retry_at = CASE
				WHEN $1 >= $4 THEN NULL
				ELSE now() + (($3 * $1) * interval '1 second')
			END,
			status = CASE
				WHEN $1 >= $4 THEN 'failed'
				ELSE 'pending'
			END,
			processing_started_at = NULL,
			processing_deadline_at = NULL
		WHERE id = $5::uuid`, attempts, lastError, backoffSec, maxAttempts, id); err != nil {
		return fmt.Errorf("finalise failure %s: %w", id, err)
	}
	return nil
}
