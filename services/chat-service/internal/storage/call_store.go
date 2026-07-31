package storage

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

type CreateCallInput struct {
	WorkspaceID string
	RequestID   string
	CallerID    string
	CalleeID    string
	Type        domain.CallType
	ExpiresAt   time.Time
}

type CallAction string

const (
	CallActionAccept  CallAction = "accept"
	CallActionDecline CallAction = "decline"
	CallActionCancel  CallAction = "cancel"
	CallActionEnd     CallAction = "end"
)

type TransitionCallInput struct {
	WorkspaceID string
	CallID      string
	ActorID     string
	Action      CallAction
}

type TransitionCallResult struct {
	Call     domain.Call
	Changed  bool
	TimedOut bool
}

type PGXCallStore struct {
	pool Pool
}

func NewPGXCallStore(pool Pool) *PGXCallStore {
	return &PGXCallStore{pool: pool}
}

func (s *PGXCallStore) CreateCall(ctx context.Context, input CreateCallInput) (domain.Call, bool, error) {
	if s == nil || s.pool == nil {
		return domain.Call{}, false, errors.New("call store unavailable")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Call{}, false, fmt.Errorf("begin create call: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	participantIDs := []string{input.CallerID, input.CalleeID}
	sort.Strings(participantIDs)
	for _, participantID := range participantIDs {
		if _, err := tx.Exec(ctx,
			`SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
			participantID,
		); err != nil {
			return domain.Call{}, false, fmt.Errorf("lock call participant: %w", err)
		}
	}

	existing, err := scanCall(tx.QueryRow(ctx,
		`SELECT id, workspace_id, request_id, caller_id, callee_id, call_type, status, version, created_at, updated_at, expires_at, accepted_at, ended_at FROM chat.calls WHERE workspace_id = $1 AND caller_id = $2 AND request_id = $3`,
		input.WorkspaceID, input.CallerID, input.RequestID,
	))
	if err == nil {
		if existing.CalleeID != input.CalleeID || existing.Type != input.Type {
			return domain.Call{}, false, domain.ErrConflict
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.Call{}, false, fmt.Errorf("commit idempotent call create: %w", err)
		}
		return existing, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.Call{}, false, fmt.Errorf("find idempotent call: %w", err)
	}

	var memberCount int
	if err := tx.QueryRow(ctx,
		`SELECT COUNT(*) FROM chat.workspace_members WHERE workspace_id = $1 AND user_id IN ($2, $3) AND status = 'active'`,
		input.WorkspaceID, input.CallerID, input.CalleeID,
	).Scan(&memberCount); err != nil {
		return domain.Call{}, false, fmt.Errorf("authorize call participants: %w", err)
	}
	if memberCount != 2 {
		return domain.Call{}, false, domain.ErrForbidden
	}

	var busy bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM chat.calls WHERE workspace_id = $1 AND status IN ('ringing', 'active') AND (caller_id IN ($2, $3) OR callee_id IN ($2, $3)))`,
		input.WorkspaceID, input.CallerID, input.CalleeID,
	).Scan(&busy); err != nil {
		return domain.Call{}, false, fmt.Errorf("check active calls: %w", err)
	}
	if busy {
		return domain.Call{}, false, domain.ErrConflict
	}

	call, err := scanCall(tx.QueryRow(ctx,
		`INSERT INTO chat.calls (workspace_id, request_id, caller_id, callee_id, call_type, expires_at) VALUES ($1, $2, $3, $4, $5, $6) RETURNING id, workspace_id, request_id, caller_id, callee_id, call_type, status, version, created_at, updated_at, expires_at, accepted_at, ended_at`,
		input.WorkspaceID, input.RequestID, input.CallerID, input.CalleeID, string(input.Type), input.ExpiresAt,
	))
	if err != nil {
		return domain.Call{}, false, fmt.Errorf("insert call: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Call{}, false, fmt.Errorf("commit call create: %w", err)
	}
	return call, true, nil
}

func (s *PGXCallStore) TransitionCall(ctx context.Context, input TransitionCallInput) (TransitionCallResult, error) {
	if s == nil || s.pool == nil {
		return TransitionCallResult{}, errors.New("call store unavailable")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return TransitionCallResult{}, fmt.Errorf("begin call transition: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	call, err := scanCall(tx.QueryRow(ctx,
		`SELECT id, workspace_id, request_id, caller_id, callee_id, call_type, status, version, created_at, updated_at, expires_at, accepted_at, ended_at FROM chat.calls WHERE workspace_id = $1 AND id = $2 FOR UPDATE`,
		input.WorkspaceID, input.CallID,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return TransitionCallResult{}, domain.ErrNotFound
	}
	if err != nil {
		return TransitionCallResult{}, fmt.Errorf("lock call: %w", err)
	}
	if !call.IsParticipant(input.ActorID) {
		return TransitionCallResult{}, domain.ErrNotFound
	}

	var databaseNow time.Time
	if call.Status == domain.CallStatusRinging {
		if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&databaseNow); err != nil {
			return TransitionCallResult{}, fmt.Errorf("read database clock: %w", err)
		}
	}
	if call.Status == domain.CallStatusRinging && !call.ExpiresAt.After(databaseNow.UTC()) {
		expired, updateErr := updateCallStatus(ctx, tx, call.ID, domain.CallStatusTimedOut)
		if updateErr != nil {
			return TransitionCallResult{}, updateErr
		}
		if err := tx.Commit(ctx); err != nil {
			return TransitionCallResult{}, fmt.Errorf("commit call timeout: %w", err)
		}
		return TransitionCallResult{Call: expired, Changed: true, TimedOut: true}, domain.ErrConflict
	}

	next, idempotent, err := authorizeCallTransition(call, input.ActorID, input.Action)
	if err != nil {
		return TransitionCallResult{}, err
	}
	if idempotent {
		if err := tx.Commit(ctx); err != nil {
			return TransitionCallResult{}, fmt.Errorf("commit idempotent call transition: %w", err)
		}
		return TransitionCallResult{Call: call}, nil
	}

	updated, err := updateCallStatus(ctx, tx, call.ID, next)
	if err != nil {
		return TransitionCallResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return TransitionCallResult{}, fmt.Errorf("commit call transition: %w", err)
	}
	return TransitionCallResult{Call: updated, Changed: true}, nil
}

func authorizeCallTransition(call domain.Call, actorID string, action CallAction) (domain.CallStatus, bool, error) {
	var requiredActor string
	var from, to domain.CallStatus
	switch action {
	case CallActionAccept:
		requiredActor, from, to = call.CalleeID, domain.CallStatusRinging, domain.CallStatusActive
	case CallActionDecline:
		requiredActor, from, to = call.CalleeID, domain.CallStatusRinging, domain.CallStatusDeclined
	case CallActionCancel:
		requiredActor, from, to = call.CallerID, domain.CallStatusRinging, domain.CallStatusCancelled
	case CallActionEnd:
		from, to = domain.CallStatusActive, domain.CallStatusEnded
	default:
		return "", false, domain.ErrInvalidInput
	}
	if requiredActor != "" && actorID != requiredActor {
		return "", false, domain.ErrNotFound
	}
	if call.Status == to {
		return to, true, nil
	}
	if call.Status != from {
		return "", false, domain.ErrConflict
	}
	return to, false, nil
}

func updateCallStatus(ctx context.Context, tx pgx.Tx, callID string, status domain.CallStatus) (domain.Call, error) {
	call, err := scanCall(tx.QueryRow(ctx,
		`UPDATE chat.calls SET status = $2, version = version + 1, updated_at = clock_timestamp(), accepted_at = CASE WHEN $2 = 'active' THEN clock_timestamp() ELSE accepted_at END, ended_at = CASE WHEN $2 IN ('declined', 'cancelled', 'timed_out', 'ended') THEN clock_timestamp() ELSE ended_at END WHERE id = $1 RETURNING id, workspace_id, request_id, caller_id, callee_id, call_type, status, version, created_at, updated_at, expires_at, accepted_at, ended_at`,
		callID, string(status),
	))
	if err != nil {
		return domain.Call{}, fmt.Errorf("update call status: %w", err)
	}
	return call, nil
}

func (s *PGXCallStore) CurrentCallForUser(ctx context.Context, workspaceID, userID string) (domain.Call, error) {
	call, err := scanCall(s.pool.QueryRow(ctx,
		`SELECT id, workspace_id, request_id, caller_id, callee_id, call_type, status, version, created_at, updated_at, expires_at, accepted_at, ended_at FROM chat.calls WHERE workspace_id = $1 AND status IN ('ringing', 'active') AND (caller_id = $2 OR callee_id = $2) ORDER BY created_at DESC LIMIT 1`,
		workspaceID, userID,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Call{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Call{}, fmt.Errorf("get current call: %w", err)
	}
	return call, nil
}

func (s *PGXCallStore) ExpireDueCalls(ctx context.Context, limit int) ([]domain.Call, error) {
	rows, err := s.pool.Query(ctx,
		`WITH due AS (SELECT id FROM chat.calls WHERE status = 'ringing' AND expires_at <= clock_timestamp() ORDER BY expires_at, id LIMIT $1 FOR UPDATE SKIP LOCKED) UPDATE chat.calls AS calls SET status = 'timed_out', version = calls.version + 1, updated_at = clock_timestamp(), ended_at = clock_timestamp() FROM due WHERE calls.id = due.id RETURNING calls.id, calls.workspace_id, calls.request_id, calls.caller_id, calls.callee_id, calls.call_type, calls.status, calls.version, calls.created_at, calls.updated_at, calls.expires_at, calls.accepted_at, calls.ended_at`,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("expire due calls: %w", err)
	}
	defer rows.Close()

	calls := make([]domain.Call, 0)
	for rows.Next() {
		call, scanErr := scanCall(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan expired call: %w", scanErr)
		}
		calls = append(calls, call)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate expired calls: %w", err)
	}
	return calls, nil
}

type callScanner interface {
	Scan(dest ...any) error
}

func scanCall(row callScanner) (domain.Call, error) {
	var call domain.Call
	var acceptedAt, endedAt pgtype.Timestamptz
	if err := row.Scan(
		&call.ID,
		&call.WorkspaceID,
		&call.RequestID,
		&call.CallerID,
		&call.CalleeID,
		(*string)(&call.Type),
		(*string)(&call.Status),
		&call.Version,
		&call.CreatedAt,
		&call.UpdatedAt,
		&call.ExpiresAt,
		&acceptedAt,
		&endedAt,
	); err != nil {
		return domain.Call{}, err
	}
	if acceptedAt.Valid {
		value := acceptedAt.Time.UTC()
		call.AcceptedAt = &value
	}
	if endedAt.Valid {
		value := endedAt.Time.UTC()
		call.EndedAt = &value
	}
	return call, nil
}
