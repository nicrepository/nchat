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
	// ParticipationID fences this heartbeat to one specific admission (issue
	// #622 round 3). Empty means the caller claims the legacy, pre-fencing
	// identity — matched only against a lease whose own participation_id is
	// NULL, never against a fenced (non-NULL) one. A heartbeat whose
	// participation_id no longer matches the lease's current value renews
	// nothing: see PGXCallStore.RenewCallPresence.
	ParticipationID string
}

// LeaveResourceCallInput identifies one participant's own departure from a
// resource (channel/group-DM) call — see PGXCallStore.LeaveResourceCall.
type LeaveResourceCallInput struct {
	WorkspaceID string
	CallID      string
	ActorID     string
	// ParticipationID fences this leave to one specific admission (issue
	// #622 round 3), exactly like RenewCallPresenceInput's own field —
	// empty claims the legacy NULL identity, never a fenced one.
	ParticipationID string
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
	// Released is LeaveResourceCall's own outcome (issue #622 round 3): true
	// only when the caller's participation_id actually matched the lease's
	// current one and that lease was deleted. false covers every other
	// reason nothing was deleted — no lease at all, already left, or a
	// stale/superseded participation_id — collapsed into one value
	// deliberately, so a caller can never learn which case it was purely
	// from this field (see LeaveResourceCall's own doc comment). Unset
	// (false) for every other TransitionCallResult producer; only
	// LeaveResourceCall ever sets it true.
	Released bool
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

	if err := lockCallKeys(ctx, tx, input.CallerID, input.CalleeID); err != nil {
		return domain.Call{}, false, err
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

// CreateResourceCall returns, alongside the call, the ParticipationID this
// admission holds (issue #622 round 3) — a fresh, opaque fencing token
// atomically rotated in the same transaction as the lease write, never
// reconsulted afterward. An idempotent replay (the exact same request_id
// resent, e.g. a dropped response retried) returns the participation_id the
// FIRST successful call already established for this (call, actor) — it must
// never rotate again on replay, or a client that already started using the
// original response's participation_id would be silently fenced out by its
// own retry.
func (s *PGXCallStore) CreateResourceCall(ctx context.Context, input CreateResourceCallInput) (domain.Call, bool, string, error) {
	if s == nil || s.pool == nil {
		return domain.Call{}, false, "", errors.New("call store unavailable")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Call{}, false, "", fmt.Errorf("begin create resource call: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// The resource-target lock (serializes admission into this target's
	// active call) and the actor's own participant lock (issue #609: makes
	// this admission mutually exclusive with any other CreateCall/
	// CreateResourceCall for the same actor) are acquired together, through
	// the same deterministic ordering as CreateCall's participant pair — see
	// lockCallKeys.
	lockKey := input.WorkspaceID + ":" + string(input.TargetType) + ":" + input.TargetID
	if err := lockCallKeys(ctx, tx, lockKey, input.CallerID); err != nil {
		return domain.Call{}, false, "", err
	}
	existing, err := scanCall(tx.QueryRow(ctx,
		`SELECT `+callSelectColumns+` FROM chat.calls WHERE workspace_id = $1 AND caller_id = $2 AND request_id = $3`,
		input.WorkspaceID, input.CallerID, input.RequestID,
	))
	if err == nil {
		if existing.TargetType != input.TargetType || existing.TargetID != input.TargetID || existing.Type != input.Type {
			return domain.Call{}, false, "", domain.ErrConflict
		}
		// Never rotates: this is a replay of the same command, not a new
		// admission. Whatever participation_id the original call already
		// established is what this replay hands back too. If that fenced lease
		// no longer exists, fail closed: a replay is not a new admission.
		replayParticipationID, err := currentParticipationID(ctx, tx, existing.ID, input.CallerID)
		if err != nil {
			return domain.Call{}, false, "", err
		}
		if replayParticipationID == "" {
			return domain.Call{}, false, "", domain.ErrCallParticipationStale
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.Call{}, false, "", fmt.Errorf("commit idempotent resource call: %w", err)
		}
		return existing, false, replayParticipationID, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.Call{}, false, "", fmt.Errorf("find idempotent resource call: %w", err)
	}

	authorized, err := authorizeResourceTarget(ctx, tx, input.WorkspaceID, input.CallerID, input.TargetType, input.TargetID)
	if err != nil {
		return domain.Call{}, false, "", err
	}
	if !authorized {
		return domain.Call{}, false, "", domain.ErrForbidden
	}

	// FOR UPDATE: without it, this SELECT can return an "active" row an
	// instant before a concurrent LeaveResourceCall (or call.end) commits
	// that same row to 'ended' — the snapshot below would then be stale by
	// the time this transaction commits, and admitCallPresence's INSERT
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
		return domain.Call{}, false, "", fmt.Errorf("find active resource call: %w", err)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		active, err = scanCall(tx.QueryRow(ctx,
			`INSERT INTO chat.calls (workspace_id, request_id, caller_id, callee_id, target_type, target_id, call_type, status, expires_at, accepted_at) VALUES ($1, $2, $3, NULL, $4, $5, $6, 'active', $7, clock_timestamp()) RETURNING `+callSelectColumns,
			input.WorkspaceID, input.RequestID, input.CallerID, string(input.TargetType), input.TargetID, string(input.Type), input.ExpiresAt,
		))
		if err != nil {
			return domain.Call{}, false, "", fmt.Errorf("insert resource call: %w", err)
		}
	}

	// Busy means the actor already holds an active/ringing direct call, or a
	// live participant lease on a DIFFERENT active resource call — the same
	// invariant CreateCall enforces (issue #609), now actually serialized
	// against it by the shared participant advisory lock above. Considers
	// only the actor trying to join, never a second participant, since a
	// resource admission only ever seats one person at a time.
	//
	// `resource_calls.id <> $3` excludes the very call being joined/rejoined
	// here: a participant already on this call (late join, refresh, rejoin)
	// must never be told they are busy with themselves — #569 established
	// the lease, not caller_id, as the authoritative participation signal,
	// and an expired lease (filtered by expires_at) never blocks either.
	//
	// If `active` was just inserted above, this deliberately still runs
	// before that new row has any lease of its own: a genuinely busy actor
	// is still rejected, and because this call has not committed yet, the
	// deferred rollback removes the row this transaction just inserted —
	// no orphan call is left behind.
	busy, err := callParticipantBusy(ctx, tx, input.WorkspaceID, input.CallerID, active.ID)
	if err != nil {
		return domain.Call{}, false, "", err
	}
	if busy {
		return domain.Call{}, false, "", domain.ErrCallParticipantBusy
	}

	participationID, err := admitCallPresence(ctx, tx, active.ID, input.CallerID, input.ExpiresAt)
	if err != nil {
		return domain.Call{}, false, "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Call{}, false, "", fmt.Errorf("commit resource call: %w", err)
	}
	return active, active.RequestID == input.RequestID && active.CallerID == input.CallerID, participationID, nil
}

// lockCallKeys acquires a per-transaction advisory lock for each key, always
// in the same deterministic (lexicographic) order — the one thing every
// call-admission path (CreateCall's caller/callee pair, CreateResourceCall's
// resource-target/actor pair) must share to rule out a lock-ordering deadlock
// (issue #609). If every transaction that takes more than one of these locks
// sorts its own keys the same way before acquiring them, no two transactions
// can ever each hold a lock the other is waiting for: a cycle in the wait-for
// graph would require some pair of keys to be acquired in opposite orders by
// two transactions, which a single shared total order over the whole key
// space (Go's string comparison, applied uniformly here) makes impossible —
// regardless of how many distinct keys or transactions are involved, or
// whether the two callsites are locking the same *kind* of key. Locks are
// released automatically at the end of the transaction (commit or
// rollback), never held past it.
func lockCallKeys(ctx context.Context, tx pgx.Tx, keys ...string) error {
	sorted := append([]string(nil), keys...)
	sort.Strings(sorted)
	for _, key := range sorted {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, key); err != nil {
			return fmt.Errorf("lock call participant: %w", err)
		}
	}
	return nil
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
	if err := lockCallKeys(ctx, tx, input.ActorID); err != nil {
		return err
	}
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
	busy, err := callParticipantBusy(ctx, tx, input.WorkspaceID, input.ActorID, call.ID)
	if err != nil {
		return err
	}
	if busy {
		return domain.ErrCallParticipantBusy
	}
	// Fenced renewal only — never an upsert (issue #622 round 3). A
	// heartbeat whose participation_id no longer matches the lease's current
	// value (a newer admission already rotated it) must never create or
	// resurrect a row: it renews nothing, and the actor learns only that its
	// claimed participation is no longer current, never anything about
	// what — or who — actually holds the call now.
	renewed, err := renewCallPresenceFenced(ctx, tx, call.ID, input.ActorID, input.ParticipationID, input.ExpiresAt)
	if err != nil {
		return err
	}
	if !renewed {
		return domain.ErrCallParticipationStale
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit call presence: %w", err)
	}
	return nil
}

// JoinResourceCallInput identifies an explicit call.join (issue #622): the
// actor's claim of which call, and which target it believes that call
// belongs to — the target is re-validated against the call's own persisted
// TargetType/TargetID before anything else, never trusted as-is.
type JoinResourceCallInput struct {
	WorkspaceID string
	CallID      string
	ActorID     string
	TargetType  domain.CallTargetType
	TargetID    string
	ExpiresAt   time.Time
}

// JoinResourceCall admits ActorID into an already-existing, active resource
// call — issue #622's call.join. Unlike CreateResourceCall, it never creates
// a call: an unknown call_id, a call that is not a resource call, a
// target_type/target_id that does not match the call's own persisted target,
// or a call the actor is not authorized for all fail identically with
// domain.ErrNotFound and no mutation, so a guessed call_id learns nothing.
// A call that is no longer active fails with domain.ErrConflict — the join
// simply arrived too late; the caller is expected to re-sync.
//
// Lock order matches every other admission path (issue #609): the actor's
// own participant advisory lock first, then the call's row lock, then the
// busy check, then the lease upsert. JoinResourceCall never takes a
// target-level lock of its own — the call row's own FOR UPDATE lock is what
// serializes it against a concurrent LeaveResourceCall/TransitionCall(end) or
// CreateResourceCall reusing the very same row, exactly as those already
// serialize against each other.
// JoinResourceCall returns, alongside the call, the fresh ParticipationID
// this admission rotated the lease to (issue #622 round 3) — see
// CreateResourceCall's own doc comment for the identical guarantee: always a
// brand-new, unguessable value, even for a rejoin of a call_id the actor was
// already seated on (its own OLD participation_id, if any, is fenced out by
// this same admission exactly like a genuinely different actor's would be —
// there is no "this is really just me again" special case, by design: the
// server cannot distinguish a legitimate same-tab rejoin from a second tab
// racing in, and must not need to).
func (s *PGXCallStore) JoinResourceCall(ctx context.Context, input JoinResourceCallInput) (domain.Call, string, error) {
	if s == nil || s.pool == nil {
		return domain.Call{}, "", errors.New("call store unavailable")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Call{}, "", fmt.Errorf("begin join resource call: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := lockCallKeys(ctx, tx, input.ActorID); err != nil {
		return domain.Call{}, "", err
	}

	call, err := scanCall(tx.QueryRow(ctx,
		`SELECT `+callSelectColumns+` FROM chat.calls WHERE workspace_id = $1 AND id = $2 FOR UPDATE`,
		input.WorkspaceID, input.CallID,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Call{}, "", domain.ErrNotFound
	}
	if err != nil {
		return domain.Call{}, "", fmt.Errorf("lock call for join: %w", err)
	}
	if !call.IsResource() {
		return domain.Call{}, "", domain.ErrNotFound
	}
	// The client's target claim must match the call's own persisted target
	// before anything else runs: call_id X + target Y fails here, with no
	// mutation and no distinction from "call not found" — a mismatched guess
	// learns nothing about X's real target.
	if call.TargetType != input.TargetType || call.TargetID != input.TargetID {
		return domain.Call{}, "", domain.ErrNotFound
	}
	if call.Status != domain.CallStatusActive {
		return domain.Call{}, "", domain.ErrConflict
	}

	authorized, err := authorizeResourceTarget(ctx, tx, input.WorkspaceID, input.ActorID, call.TargetType, call.TargetID)
	if err != nil {
		return domain.Call{}, "", err
	}
	if !authorized {
		return domain.Call{}, "", domain.ErrNotFound
	}

	busy, err := callParticipantBusy(ctx, tx, input.WorkspaceID, input.ActorID, call.ID)
	if err != nil {
		return domain.Call{}, "", err
	}
	if busy {
		return domain.Call{}, "", domain.ErrCallParticipantBusy
	}

	participationID, err := admitCallPresence(ctx, tx, call.ID, input.ActorID, input.ExpiresAt)
	if err != nil {
		return domain.Call{}, "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Call{}, "", fmt.Errorf("commit join resource call: %w", err)
	}
	return call, participationID, nil
}

// ActiveResourceCallResult is ActiveResourceCall's answer (issue #622).
// Authorized and Found are kept apart deliberately: the storage layer can
// tell "no active call" from "not authorized for this target" even though the
// protocol layer (CallService.ResourceSync) collapses both into the same
// found=false response — an unauthorized caller must never learn a call
// exists purely by getting a different answer shape than a genuinely idle
// target would produce.
type ActiveResourceCallResult struct {
	Call       domain.Call
	Found      bool
	Authorized bool
	ObservedAt time.Time
}

// ActiveResourceCall answers call.resource.sync (issue #622): the
// authoritative active call of one channel/group-DM target, read-only —
// creates no lease, issues no token, mutates nothing.
//
// This runs as ONE statement: the authorization check, the active-call
// lookup (a LEFT JOIN, so the row is simply absent rather than fetched by a
// second query) and the observed_at snapshot are all decided from the exact
// same READ COMMITTED snapshot. Two separate round trips would let each see
// a different snapshot — "authorized" and "found" could disagree with a
// second query's own answer if the call changed in between — which is
// exactly the torn read this single-statement design rules out.
//
// ObservedAt uses statement_timestamp(), not clock_timestamp(). Postgres
// documents statement_timestamp() as the start time of the current
// statement — the same instant a READ COMMITTED snapshot for that statement
// is established — so it cannot be later than the commit of any row this
// statement's snapshot fails to see, no matter how long the statement then
// takes to finish evaluating. clock_timestamp() has no such guarantee: it is
// volatile and re-evaluated at the moment each expression actually runs, so
// a concurrent commit landing between snapshot acquisition and that
// evaluation could commit a call with a created_at earlier than the
// observed_at this query would then report for "not found" — the exact
// false-negative-freshness bug the null-sync race must not allow. See
// TestStatementTimestampIsFixedAtStatementStartNotReevaluatedPostgreSQL for
// the semantic proof, and resource_sync_postgres_test.go for the concurrent
// proof.
func (s *PGXCallStore) ActiveResourceCall(ctx context.Context, workspaceID, actorID string, targetType domain.CallTargetType, targetID string) (ActiveResourceCallResult, error) {
	if s == nil || s.pool == nil {
		return ActiveResourceCallResult{}, errors.New("call store unavailable")
	}
	var query string
	switch targetType {
	case domain.CallTargetChannel:
		query = activeResourceCallChannelQuery
	case domain.CallTargetDM:
		query = activeResourceCallDMQuery
	default:
		return ActiveResourceCallResult{}, domain.ErrInvalidInput
	}

	var authorized bool
	var observedAt time.Time
	var id, workspaceIDCol, requestID, callerID, calleeID, targetIDCol pgtype.UUID
	var targetTypeCol, callTypeCol, statusCol pgtype.Text
	var version pgtype.Int8
	var createdAt, updatedAt, expiresAt, acceptedAt, endedAt pgtype.Timestamptz

	err := s.pool.QueryRow(ctx, query, workspaceID, actorID, targetID).Scan(
		&authorized, &observedAt,
		&id, &workspaceIDCol, &requestID, &callerID, &calleeID,
		&targetTypeCol, &targetIDCol, &callTypeCol, &statusCol,
		&version, &createdAt, &updatedAt, &expiresAt, &acceptedAt, &endedAt,
	)
	if err != nil {
		return ActiveResourceCallResult{}, fmt.Errorf("check active resource call: %w", err)
	}
	result := ActiveResourceCallResult{Authorized: authorized, ObservedAt: observedAt.UTC()}
	if !authorized || !id.Valid {
		return result, nil
	}

	call := domain.Call{
		ID:          uuid.UUID(id.Bytes).String(),
		WorkspaceID: uuid.UUID(workspaceIDCol.Bytes).String(),
		RequestID:   uuid.UUID(requestID.Bytes).String(),
		CallerID:    uuid.UUID(callerID.Bytes).String(),
		TargetType:  domain.CallTargetType(targetTypeCol.String),
		TargetID:    uuid.UUID(targetIDCol.Bytes).String(),
		Type:        domain.CallType(callTypeCol.String),
		Status:      domain.CallStatus(statusCol.String),
		Version:     version.Int64,
		CreatedAt:   createdAt.Time.UTC(),
		UpdatedAt:   updatedAt.Time.UTC(),
		ExpiresAt:   expiresAt.Time.UTC(),
	}
	if calleeID.Valid {
		call.CalleeID = uuid.UUID(calleeID.Bytes).String()
	}
	if acceptedAt.Valid {
		v := acceptedAt.Time.UTC()
		call.AcceptedAt = &v
	}
	if endedAt.Valid {
		v := endedAt.Time.UTC()
		call.EndedAt = &v
	}
	result.Call = call
	result.Found = true
	return result, nil
}

const activeResourceCallChannelQuery = `
	WITH authorized AS (
		SELECT EXISTS (
			SELECT 1 FROM chat.channels c
			JOIN chat.workspace_members wm ON wm.workspace_id = c.workspace_id AND wm.user_id = $2 AND wm.status = 'active'
			WHERE c.id = $3 AND c.workspace_id = $1 AND c.status = 'active'
			  AND chat.channel_visible_to_user(c.id, $2::uuid)
		) AS ok
	),
	active_call AS (
		SELECT ` + callSelectColumns + `
		FROM chat.calls
		WHERE workspace_id = $1 AND target_type = 'channel' AND target_id = $3 AND status = 'active'
	)
	SELECT authorized.ok, statement_timestamp(), active_call.*
	FROM authorized
	LEFT JOIN active_call ON true`

const activeResourceCallDMQuery = `
	WITH authorized AS (
		SELECT EXISTS (
			SELECT 1 FROM chat.dm_conversations dc
			JOIN chat.workspace_members wm ON wm.workspace_id = dc.workspace_id AND wm.user_id = $2 AND wm.status = 'active'
			JOIN chat.dm_members dm ON dm.conversation_id = dc.id AND dm.user_id = $2 AND dm.status = 'active'
			WHERE dc.id = $3 AND dc.workspace_id = $1 AND dc.status = 'active' AND dc.type = 'group'
		) AS ok
	),
	active_call AS (
		SELECT ` + callSelectColumns + `
		FROM chat.calls
		WHERE workspace_id = $1 AND target_type = 'dm' AND target_id = $3 AND status = 'active'
	)
	SELECT authorized.ok, statement_timestamp(), active_call.*
	FROM authorized
	LEFT JOIN active_call ON true`

func callParticipantBusy(ctx context.Context, tx pgx.Tx, workspaceID, actorID, exceptCallID string) (bool, error) {
	var busy bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (
			SELECT 1 FROM chat.calls
			WHERE workspace_id = $1 AND target_type = 'user' AND status IN ('ringing', 'active')
			  AND (caller_id = $2 OR callee_id = $2)
			UNION ALL
			SELECT 1 FROM chat.call_participant_leases leases
			JOIN chat.calls resource_calls ON resource_calls.id = leases.call_id
			WHERE resource_calls.workspace_id = $1 AND resource_calls.status = 'active'
			  AND resource_calls.target_type IN ('channel', 'dm')
			  AND leases.user_id = $2 AND leases.expires_at > clock_timestamp()
			  AND resource_calls.id <> $3
		)`,
		workspaceID, actorID, exceptCallID,
	).Scan(&busy); err != nil {
		return false, fmt.Errorf("check active calls: %w", err)
	}
	return busy, nil
}

// queryRower is satisfied by both pgx.Tx and Pool — narrow enough that
// authorizeResourceTarget and callParticipantBusy can run either inside an
// existing transaction (every admission/mutation path) or directly against
// the pool for a plain read that needs no transaction of its own (see
// ActiveResourceCall).
type queryRower interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func authorizeResourceTarget(ctx context.Context, tx queryRower, workspaceID, actorID string, targetType domain.CallTargetType, targetID string) (bool, error) {
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

// admitCallPresence establishes or re-establishes actorID's participant lease
// for callID with a BRAND-NEW, opaque ParticipationID (issue #622 round 3) —
// the fencing token this admission alone now holds. Always rotates
// unconditionally: a rejoin of an already-fenced lease fences out whatever
// participation_id it previously carried, exactly like admitting a genuinely
// different actor would. Used only by CreateResourceCall/JoinResourceCall,
// never by a heartbeat — see renewCallPresenceFenced for that.
func admitCallPresence(ctx context.Context, tx pgx.Tx, callID, actorID string, expiresAt time.Time) (string, error) {
	participationID := uuid.NewString()
	if _, err := tx.Exec(ctx,
		`INSERT INTO chat.call_participant_leases (call_id, user_id, participation_id, expires_at) VALUES ($1, $2, $3, $4)
		 ON CONFLICT (call_id, user_id) DO UPDATE SET expires_at = EXCLUDED.expires_at, participation_id = EXCLUDED.participation_id`,
		callID, actorID, participationID, expiresAt,
	); err != nil {
		return "", fmt.Errorf("admit call presence: %w", err)
	}
	return participationID, nil
}

// renewCallPresenceFenced renews an EXISTING lease's expiry, gated on
// participationID still matching the lease's own current value (issue #622
// round 3) — never an upsert: a heartbeat can only ever refresh a lease that
// is already exactly this participation's own, and can never create one
// (that is admission's job alone) or resurrect one a newer admission or an
// intervening leave has since superseded or removed. participationID == ""
// claims the pre-fencing legacy identity and is matched only against a lease
// whose own participation_id is NULL (see the migration's rollout doc
// comment) — never against a fenced, non-NULL one, so an old client can never
// renew a lease a new admission already rotated. Returns whether a row was
// actually renewed; false is never an error on its own, only a fact for the
// caller to act on.
func renewCallPresenceFenced(ctx context.Context, tx pgx.Tx, callID, actorID, participationID string, expiresAt time.Time) (bool, error) {
	var query string
	var args []any
	if participationID == "" {
		query = `UPDATE chat.call_participant_leases SET expires_at = $3 WHERE call_id = $1 AND user_id = $2 AND participation_id IS NULL`
		args = []any{callID, actorID, expiresAt}
	} else {
		query = `UPDATE chat.call_participant_leases SET expires_at = $4 WHERE call_id = $1 AND user_id = $2 AND participation_id = $3`
		args = []any{callID, actorID, participationID, expiresAt}
	}
	tag, err := tx.Exec(ctx, query, args...)
	if err != nil {
		return false, fmt.Errorf("renew call presence: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// currentParticipationID reads the participation_id an actor's own lease on
// callID currently carries, or "" if no live lease exists — used only by
// CreateResourceCall's idempotent-replay branch (issue #622 round 3), which
// must hand back whatever the original, non-replayed admission already
// established rather than rotating again on retry.
func currentParticipationID(ctx context.Context, tx pgx.Tx, callID, actorID string) (string, error) {
	var participationID pgtype.Text
	err := tx.QueryRow(ctx,
		`SELECT participation_id::text FROM chat.call_participant_leases WHERE call_id = $1 AND user_id = $2`,
		callID, actorID,
	).Scan(&participationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read current participation id: %w", err)
	}
	return participationID.String, nil
}

// LeaveResourceCall releases actorID's own participant lease on a resource
// (channel/group-DM) call immediately — the counterpart to
// admitCallPresence, and deliberately not call.end: a resource call's
// lifecycle for its other participants must never depend on any single
// participant's leave (issue #569).
//
// Fenced by input.ParticipationID (issue #622 round 3): the DELETE below can
// only ever remove a lease whose own current participation_id still matches
// the caller's claimed one (or, for an empty ParticipationID, one still
// carrying the legacy NULL identity — see the migration's rollout doc
// comment). A stale value — a rejoin, a second tab, or a handoff already
// rotated the lease to a NEW participation_id — matches nothing: the DELETE
// affects zero rows, TransitionCallResult.Released comes back false, and
// nothing about the call or its other participants' leases is ever touched.
// This is what makes a stale tab's own compensating leave for an admission a
// newer one has since superseded provably safe purely by the database
// itself, independent of whether any client-side generation guard correctly
// detected the supersession.
//
// Idempotent: leaving twice, or leaving after never holding an active lease
// (already left, or the call is no longer active), changes nothing and is
// not an error — a duplicated or retried leave must be safe. Released is
// false for every reason nothing was deleted, deliberately collapsed into
// one value: a caller must never learn from this result alone whether "no
// lease at all" or "a stale participation_id" was the actual cause (see
// TransitionCallResult.Released's own doc comment).
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
	// Serialize the release with every admission/heartbeat for this actor.
	// This must precede the call-row lock: all paths that take both acquire
	// participant advisory lock(s) before any call row, so no row -> participant
	// cycle is possible. A join waiting behind this leave then sees the deleted
	// lease after commit instead of failing busy based on pre-leave state.
	if err := lockCallKeys(ctx, tx, input.ActorID); err != nil {
		return TransitionCallResult{}, err
	}

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
	var deleteQuery string
	var deleteArgs []any
	if input.ParticipationID == "" {
		deleteQuery = `DELETE FROM chat.call_participant_leases WHERE call_id = $1 AND user_id = $2 AND participation_id IS NULL RETURNING TRUE`
		deleteArgs = []any{call.ID, input.ActorID}
	} else {
		deleteQuery = `DELETE FROM chat.call_participant_leases WHERE call_id = $1 AND user_id = $2 AND participation_id = $3 RETURNING TRUE`
		deleteArgs = []any{call.ID, input.ActorID, input.ParticipationID}
	}
	if err := tx.QueryRow(ctx, deleteQuery, deleteArgs...).Scan(&hadLease); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return TransitionCallResult{}, fmt.Errorf("release call participant lease: %w", err)
	}

	if !hadLease {
		// No lease released: either no lease at all (a legitimate retry —
		// actorID already left, or never got as far as holding one), or a
		// real lease exists but under a DIFFERENT, current participation_id
		// (this exact call's own fencing protection — a stale admission's
		// compensating leave must never touch it). Both cases are handled
		// identically: only someone currently authorized for the call's own
		// target may learn its state here — same authorization
		// RenewCallPresence already requires before it will touch this call
		// at all — so an unauthorized guess fails exactly like a call.leave
		// for a call_id that does not exist, carrying no call metadata.
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
		// A real, currently-fenced lease existed and was released, so
		// actorID was a genuine, current participant — no further
		// authorization check needed to hand back this already-terminal
		// call's state.
		if err := tx.Commit(ctx); err != nil {
			return TransitionCallResult{}, fmt.Errorf("commit call leave: %w", err)
		}
		return TransitionCallResult{Call: call, Released: true}, nil
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
		return TransitionCallResult{Call: call, Released: true}, nil
	}

	ended, err := updateCallStatus(ctx, tx, call.ID, domain.CallStatusEnded)
	if err != nil {
		return TransitionCallResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return TransitionCallResult{}, fmt.Errorf("commit call leave: %w", err)
	}
	return TransitionCallResult{Call: ended, Changed: true, Released: true}, nil
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
