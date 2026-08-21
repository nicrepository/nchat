package storage

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
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

type CreateResourceCallInput struct {
	WorkspaceID string
	RequestID   string
	CallerID    string
	TargetType  domain.CallTargetType
	TargetID    string
	Type        domain.CallType
	ExpiresAt   time.Time
}

type RenewCallPresenceInput struct {
	WorkspaceID string
	CallID      string
	ActorID     string
	ExpiresAt   time.Time
}

// LeaveResourceCallInput identifies one participant's own departure from a
// resource (channel/group-DM) call — see PGXCallStore.LeaveResourceCall.
type LeaveResourceCallInput struct {
	WorkspaceID string
	CallID      string
	ActorID     string
}

const callSelectColumns = `id, workspace_id, request_id, caller_id, callee_id, target_type, target_id, call_type, status, version, created_at, updated_at, expires_at, accepted_at, ended_at`

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
		`SELECT `+callSelectColumns+` FROM chat.calls WHERE workspace_id = $1 AND caller_id = $2 AND request_id = $3`,
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

	// Busy means an active/ringing direct call, or a resource call this
	// user currently holds a live participant lease on — never merely
	// having once been a resource call's caller_id, which stays on that row
	// forever and would otherwise block this user's own 1:1 calls long after
	// they left it (issue #569).
	var busy bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (
			SELECT 1 FROM chat.calls
			WHERE workspace_id = $1 AND target_type = 'user' AND status IN ('ringing', 'active')
			  AND (caller_id IN ($2, $3) OR callee_id IN ($2, $3))
			UNION ALL
			SELECT 1 FROM chat.call_participant_leases leases
			JOIN chat.calls resource_calls ON resource_calls.id = leases.call_id
			WHERE resource_calls.workspace_id = $1 AND resource_calls.status = 'active'
			  AND resource_calls.target_type IN ('channel', 'dm')
			  AND leases.user_id IN ($2, $3) AND leases.expires_at > clock_timestamp()
		)`,
		input.WorkspaceID, input.CallerID, input.CalleeID,
	).Scan(&busy); err != nil {
		return domain.Call{}, false, fmt.Errorf("check active calls: %w", err)
	}
	if busy {
		return domain.Call{}, false, domain.ErrCallParticipantBusy
	}

	call, err := scanCall(tx.QueryRow(ctx,
		`INSERT INTO chat.calls (workspace_id, request_id, caller_id, callee_id, target_type, target_id, call_type, expires_at) VALUES ($1, $2, $3, $4, 'user', $4, $5, $6) RETURNING `+callSelectColumns,
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

func (s *PGXCallStore) CreateResourceCall(ctx context.Context, input CreateResourceCallInput) (domain.Call, bool, error) {
	if s == nil || s.pool == nil {
		return domain.Call{}, false, errors.New("call store unavailable")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Call{}, false, fmt.Errorf("begin create resource call: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	lockKey := input.WorkspaceID + ":" + string(input.TargetType) + ":" + input.TargetID
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lockKey); err != nil {
		return domain.Call{}, false, fmt.Errorf("lock call resource: %w", err)
	}
	existing, err := scanCall(tx.QueryRow(ctx,
		`SELECT `+callSelectColumns+` FROM chat.calls WHERE workspace_id = $1 AND caller_id = $2 AND request_id = $3`,
		input.WorkspaceID, input.CallerID, input.RequestID,
	))
	if err == nil {
		if existing.TargetType != input.TargetType || existing.TargetID != input.TargetID || existing.Type != input.Type {
			return domain.Call{}, false, domain.ErrConflict
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.Call{}, false, fmt.Errorf("commit idempotent resource call: %w", err)
		}
		return existing, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.Call{}, false, fmt.Errorf("find idempotent resource call: %w", err)
	}

	authorized, err := authorizeResourceTarget(ctx, tx, input.WorkspaceID, input.CallerID, input.TargetType, input.TargetID)
	if err != nil {
		return domain.Call{}, false, err
	}
	if !authorized {
		return domain.Call{}, false, domain.ErrForbidden
	}

	// FOR UPDATE: without it, this SELECT can return an "active" row an
	// instant before a concurrent LeaveResourceCall (or call.end) commits
	// that same row to 'ended' — the snapshot below would then be stale by
	// the time this transaction commits, and upsertCallPresence's INSERT
	// (which only waits on the row's FK-implied lock, not on this SELECT)
	// would attach a fresh lease to a call that is already over (issue
	// #569 follow-up). Taking the lock here forces this query to block
	// until any such concurrent transition finishes, then re-evaluate
	// against the committed row: WHERE status = 'active' then correctly
	// finds no match if the call just ended, and this function falls
	// through to inserting a brand-new active call instead — the joiner
	// gets a live call either way, never a dead one.
	active, err := scanCall(tx.QueryRow(ctx,
		`SELECT `+callSelectColumns+` FROM chat.calls WHERE workspace_id = $1 AND target_type = $2 AND target_id = $3 AND status = 'active' FOR UPDATE`,
		input.WorkspaceID, string(input.TargetType), input.TargetID,
	))
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return domain.Call{}, false, fmt.Errorf("find active resource call: %w", err)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		active, err = scanCall(tx.QueryRow(ctx,
			`INSERT INTO chat.calls (workspace_id, request_id, caller_id, callee_id, target_type, target_id, call_type, status, expires_at, accepted_at) VALUES ($1, $2, $3, NULL, $4, $5, $6, 'active', $7, clock_timestamp()) RETURNING `+callSelectColumns,
			input.WorkspaceID, input.RequestID, input.CallerID, string(input.TargetType), input.TargetID, string(input.Type), input.ExpiresAt,
		))
		if err != nil {
			return domain.Call{}, false, fmt.Errorf("insert resource call: %w", err)
		}
	}
	if err := upsertCallPresence(ctx, tx, active.ID, input.CallerID, input.ExpiresAt); err != nil {
		return domain.Call{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Call{}, false, fmt.Errorf("commit resource call: %w", err)
	}
	return active, active.RequestID == input.RequestID && active.CallerID == input.CallerID, nil
}

func (s *PGXCallStore) RenewCallPresence(ctx context.Context, input RenewCallPresenceInput) error {
	if s == nil || s.pool == nil {
		return errors.New("call store unavailable")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin call presence: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	call, err := scanCall(tx.QueryRow(ctx,
		`SELECT `+callSelectColumns+` FROM chat.calls WHERE workspace_id = $1 AND id = $2 FOR SHARE`,
		input.WorkspaceID, input.CallID,
	))
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (!call.IsResource() || call.Status != domain.CallStatusActive)) {
		return domain.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("read call presence target: %w", err)
	}
	authorized, err := authorizeResourceTarget(ctx, tx, input.WorkspaceID, input.ActorID, call.TargetType, call.TargetID)
	if err != nil {
		return err
	}
	if !authorized {
		return domain.ErrNotFound
	}
	if err := upsertCallPresence(ctx, tx, call.ID, input.ActorID, input.ExpiresAt); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit call presence: %w", err)
	}
	return nil
}

func authorizeResourceTarget(ctx context.Context, tx pgx.Tx, workspaceID, actorID string, targetType domain.CallTargetType, targetID string) (bool, error) {
	var query string
	switch targetType {
	case domain.CallTargetChannel:
		query = `SELECT EXISTS (
			SELECT 1 FROM chat.channels c
			JOIN chat.workspace_members wm ON wm.workspace_id = c.workspace_id AND wm.user_id = $2 AND wm.status = 'active'
			WHERE c.id = $3 AND c.workspace_id = $1 AND c.status = 'active'
			  AND chat.channel_visible_to_user(c.id, $2::uuid))`
	case domain.CallTargetDM:
		query = `SELECT EXISTS (
			SELECT 1 FROM chat.dm_conversations dc
			JOIN chat.workspace_members wm ON wm.workspace_id = dc.workspace_id AND wm.user_id = $2 AND wm.status = 'active'
			JOIN chat.dm_members dm ON dm.conversation_id = dc.id AND dm.user_id = $2 AND dm.status = 'active'
			WHERE dc.id = $3 AND dc.workspace_id = $1 AND dc.status = 'active' AND dc.type = 'group')`
	default:
		return false, domain.ErrInvalidInput
	}
	var authorized bool
	if err := tx.QueryRow(ctx, query, workspaceID, actorID, targetID).Scan(&authorized); err != nil {
		return false, fmt.Errorf("authorize call resource: %w", err)
	}
	return authorized, nil
}

func upsertCallPresence(ctx context.Context, tx pgx.Tx, callID, actorID string, expiresAt time.Time) error {
	if _, err := tx.Exec(ctx,
		`INSERT INTO chat.call_participant_leases (call_id, user_id, expires_at) VALUES ($1, $2, $3)
		 ON CONFLICT (call_id, user_id) DO UPDATE SET expires_at = EXCLUDED.expires_at`,
		callID, actorID, expiresAt,
	); err != nil {
		return fmt.Errorf("renew call presence: %w", err)
	}
	return nil
}

// LeaveResourceCall releases actorID's own participant lease on a resource
// (channel/group-DM) call immediately — the counterpart to
// upsertCallPresence, and deliberately not call.end: a resource call's
// lifecycle for its other participants must never depend on any single
// participant's leave (issue #569).
//
// Idempotent: leaving twice, or leaving after never holding an active lease
// (already left, or the call is no longer active), changes nothing and is
// not an error — a duplicated or retried leave must be safe.
//
// The call itself transitions to ended here, deterministically, only when
// actorID's lease was the last one live — never left to ExpireDueCalls'
// periodic sweep, which stays in place purely as a safety net for a
// participant who disconnects without ever sending call.leave (a crashed
// tab, a lost socket).
func (s *PGXCallStore) LeaveResourceCall(ctx context.Context, input LeaveResourceCallInput) (TransitionCallResult, error) {
	if s == nil || s.pool == nil {
		return TransitionCallResult{}, errors.New("call store unavailable")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return TransitionCallResult{}, fmt.Errorf("begin call leave: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	call, err := scanCall(tx.QueryRow(ctx,
		`SELECT `+callSelectColumns+` FROM chat.calls WHERE workspace_id = $1 AND id = $2 FOR UPDATE`,
		input.WorkspaceID, input.CallID,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return TransitionCallResult{}, domain.ErrNotFound
	}
	if err != nil {
		return TransitionCallResult{}, fmt.Errorf("lock call for leave: %w", err)
	}
	if !call.IsResource() {
		// call.leave has no meaning for a direct call: RF-23's own
		// call.decline/call.cancel/call.end already own that lifecycle.
		return TransitionCallResult{}, domain.ErrNotFound
	}

	var hadLease bool
	if err := tx.QueryRow(ctx,
		`DELETE FROM chat.call_participant_leases WHERE call_id = $1 AND user_id = $2 RETURNING TRUE`,
		call.ID, input.ActorID,
	).Scan(&hadLease); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return TransitionCallResult{}, fmt.Errorf("release call participant lease: %w", err)
	}

	if !hadLease {
		// No active lease to release: either a legitimate retry (actorID
		// already left, or never got as far as holding one) or an arbitrary
		// caller who merely knows/guessed this call_id. Only someone
		// currently authorized for the call's own target may learn its
		// state here — same authorization RenewCallPresence already
		// requires before it will touch this call at all — so an
		// unauthorized guess fails exactly like a call.leave for a call_id
		// that does not exist, carrying no call metadata.
		authorized, err := authorizeResourceTarget(ctx, tx, input.WorkspaceID, input.ActorID, call.TargetType, call.TargetID)
		if err != nil {
			return TransitionCallResult{}, err
		}
		if !authorized {
			return TransitionCallResult{}, domain.ErrNotFound
		}
		if err := tx.Commit(ctx); err != nil {
			return TransitionCallResult{}, fmt.Errorf("commit call leave: %w", err)
		}
		return TransitionCallResult{Call: call}, nil
	}

	if call.Status != domain.CallStatusActive {
		// A real lease existed, so actorID was a genuine participant at
		// some point — no further authorization check needed to hand back
		// this already-terminal call's state.
		if err := tx.Commit(ctx); err != nil {
			return TransitionCallResult{}, fmt.Errorf("commit call leave: %w", err)
		}
		return TransitionCallResult{Call: call}, nil
	}

	var remaining bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM chat.call_participant_leases WHERE call_id = $1 AND expires_at > clock_timestamp())`,
		call.ID,
	).Scan(&remaining); err != nil {
		return TransitionCallResult{}, fmt.Errorf("check remaining call participants: %w", err)
	}
	if remaining {
		if err := tx.Commit(ctx); err != nil {
			return TransitionCallResult{}, fmt.Errorf("commit call leave: %w", err)
		}
		return TransitionCallResult{Call: call}, nil
	}

	ended, err := updateCallStatus(ctx, tx, call.ID, domain.CallStatusEnded)
	if err != nil {
		return TransitionCallResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return TransitionCallResult{}, fmt.Errorf("commit call leave: %w", err)
	}
	return TransitionCallResult{Call: ended, Changed: true}, nil
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
		`SELECT `+callSelectColumns+` FROM chat.calls WHERE workspace_id = $1 AND id = $2 FOR UPDATE`,
		input.WorkspaceID, input.CallID,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return TransitionCallResult{}, domain.ErrNotFound
	}
	if err != nil {
		return TransitionCallResult{}, fmt.Errorf("lock call: %w", err)
	}
	if (call.IsResource() && call.CallerID != input.ActorID) || (!call.IsResource() && !call.IsParticipant(input.ActorID)) {
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
	if call.IsResource() {
		if action != CallActionEnd || actorID != call.CallerID {
			return "", false, domain.ErrNotFound
		}
		if call.Status == domain.CallStatusEnded {
			return domain.CallStatusEnded, true, nil
		}
		if call.Status != domain.CallStatusActive {
			return "", false, domain.ErrConflict
		}
		return domain.CallStatusEnded, false, nil
	}
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
		`UPDATE chat.calls SET status = $2, version = version + 1, updated_at = clock_timestamp(), accepted_at = CASE WHEN $2 = 'active' THEN clock_timestamp() ELSE accepted_at END, ended_at = CASE WHEN $2 IN ('declined', 'cancelled', 'timed_out', 'ended') THEN clock_timestamp() ELSE ended_at END WHERE id = $1 RETURNING `+callSelectColumns,
		callID, string(status),
	))
	if err != nil {
		return domain.Call{}, fmt.Errorf("update call status: %w", err)
	}
	return call, nil
}

func (s *PGXCallStore) CurrentCallForUser(ctx context.Context, workspaceID, userID, callID string) (domain.Call, error) {
	query := `SELECT ` + callSelectColumns + ` FROM chat.calls WHERE workspace_id = $1 AND status IN ('ringing', 'active') AND (caller_id = $2 OR (target_type = 'user' AND callee_id = $2)) ORDER BY created_at DESC LIMIT 1`
	args := []any{workspaceID, userID}
	if callID != "" {
		query = `SELECT ` + callSelectColumns + ` FROM chat.calls calls
			WHERE calls.workspace_id = $1 AND calls.id = $3 AND calls.status IN ('ringing', 'active') AND (
				(calls.target_type = 'user' AND (calls.caller_id = $2 OR calls.callee_id = $2))
				OR (calls.target_type = 'channel' AND EXISTS (
					SELECT 1 FROM chat.channels c
					JOIN chat.workspace_members wm ON wm.workspace_id = c.workspace_id AND wm.user_id = $2 AND wm.status = 'active'
					WHERE c.id = calls.target_id AND c.workspace_id = $1 AND c.status = 'active'
					  AND chat.channel_visible_to_user(c.id, $2::uuid)))
				OR (calls.target_type = 'dm' AND EXISTS (
					SELECT 1 FROM chat.dm_conversations dc
					JOIN chat.workspace_members wm ON wm.workspace_id = dc.workspace_id AND wm.user_id = $2 AND wm.status = 'active'
					JOIN chat.dm_members dm ON dm.conversation_id = dc.id AND dm.user_id = $2 AND dm.status = 'active'
					WHERE dc.id = calls.target_id AND dc.workspace_id = $1 AND dc.status = 'active' AND dc.type = 'group'))
			)`
		args = append(args, callID)
	}
	call, err := scanCall(s.pool.QueryRow(ctx, query, args...))
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
		`WITH due AS (
			SELECT id, CASE WHEN status = 'ringing' THEN 'timed_out' ELSE 'ended' END AS next_status
			FROM chat.calls
			WHERE (status = 'ringing' AND expires_at <= clock_timestamp())
			   OR (status = 'active' AND target_type IN ('channel', 'dm') AND NOT EXISTS (
				SELECT 1 FROM chat.call_participant_leases leases
				WHERE leases.call_id = chat.calls.id AND leases.expires_at > clock_timestamp()))
			ORDER BY updated_at, id LIMIT $1 FOR UPDATE SKIP LOCKED
		) UPDATE chat.calls AS calls SET status = due.next_status, version = calls.version + 1,
			updated_at = clock_timestamp(), ended_at = clock_timestamp()
		FROM due WHERE calls.id = due.id
		RETURNING calls.id, calls.workspace_id, calls.request_id, calls.caller_id, calls.callee_id,
			calls.target_type, calls.target_id, calls.call_type, calls.status, calls.version,
			calls.created_at, calls.updated_at, calls.expires_at, calls.accepted_at, calls.ended_at`,
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
	var calleeID pgtype.UUID
	var acceptedAt, endedAt pgtype.Timestamptz
	if err := row.Scan(
		&call.ID,
		&call.WorkspaceID,
		&call.RequestID,
		&call.CallerID,
		&calleeID,
		(*string)(&call.TargetType),
		&call.TargetID,
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
	if calleeID.Valid {
		call.CalleeID = uuid.UUID(calleeID.Bytes).String()
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
