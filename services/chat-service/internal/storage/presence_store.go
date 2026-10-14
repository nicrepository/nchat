package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// PGXPresenceStore persists the presence facts the server owns (issue #798):
// a member's manual state with its expiry, and the instant they were last
// published as offline. It also answers, in one statement, everything about a
// set of users that presence needs and the realtime layer cannot see — their
// live manual state and whether they are in a call.
//
// Every statement is scoped by workspace_id first. Identities and the
// workspace always come from the caller's server-side context.
type PGXPresenceStore struct{ pool Pool }

func NewPGXPresenceStore(pool Pool) *PGXPresenceStore {
	return &PGXPresenceStore{pool: pool}
}

// setManualSQL writes a manual state for an *active* member only. The
// INSERT … SELECT is what makes membership part of the write: a request for a
// workspace the user is not an active member of inserts nothing and returns no
// row. The timestamp is the database's, never the caller's, and the last
// writer wins deterministically by commit order.
const setManualSQL = `
	INSERT INTO chat.user_presence (workspace_id, user_id, manual_state, manual_expires_at, manual_updated_at)
	SELECT wm.workspace_id, wm.user_id, $3, $4, clock_timestamp()
	FROM chat.workspace_members wm
	WHERE wm.workspace_id = $1::uuid AND wm.user_id = $2::uuid AND wm.status = 'active'
	ON CONFLICT (workspace_id, user_id) DO UPDATE
	SET manual_state = EXCLUDED.manual_state,
	    manual_expires_at = EXCLUDED.manual_expires_at,
	    manual_updated_at = EXCLUDED.manual_updated_at
	RETURNING manual_state, manual_expires_at, manual_updated_at`

// SetManual stores a manual state and returns what was stored. A caller that is
// not an active member gets domain.ErrForbidden.
func (s *PGXPresenceStore) SetManual(
	ctx context.Context, workspaceID, userID string, state domain.PresenceManualState, expiresAt time.Time,
) (domain.PresenceOverride, error) {
	var stored domain.PresenceOverride
	var raw string
	err := s.pool.QueryRow(ctx, setManualSQL, workspaceID, userID, string(state), expiresAt).
		Scan(&raw, &stored.ExpiresAt, &stored.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.PresenceOverride{}, domain.ErrForbidden
	}
	if err != nil {
		return domain.PresenceOverride{}, fmt.Errorf("store manual presence: %w", err)
	}
	stored.State = domain.PresenceManualState(raw)
	return stored, nil
}

// ClearManual returns the user to automatic presence. Clearing a state that
// does not exist is not an error: the outcome asked for already holds.
func (s *PGXPresenceStore) ClearManual(ctx context.Context, workspaceID, userID string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE chat.user_presence
		SET manual_state = NULL, manual_expires_at = NULL, manual_updated_at = NULL
		WHERE workspace_id = $1::uuid AND user_id = $2::uuid`, workspaceID, userID)
	if err != nil {
		return fmt.Errorf("clear manual presence: %w", err)
	}
	return nil
}

// Manual returns the user's live manual state, or the zero override when there
// is none or it has expired.
func (s *PGXPresenceStore) Manual(ctx context.Context, workspaceID, userID string) (domain.PresenceOverride, error) {
	var override domain.PresenceOverride
	var raw string
	err := s.pool.QueryRow(ctx, `
		SELECT manual_state, manual_expires_at, manual_updated_at
		FROM chat.user_presence
		WHERE workspace_id = $1::uuid AND user_id = $2::uuid
		  AND manual_state IS NOT NULL AND manual_expires_at > clock_timestamp()`,
		workspaceID, userID).Scan(&raw, &override.ExpiresAt, &override.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.PresenceOverride{}, nil
	}
	if err != nil {
		return domain.PresenceOverride{}, fmt.Errorf("read manual presence: %w", err)
	}
	override.State = domain.PresenceManualState(raw)
	return override, nil
}

// contextsSQL resolves every requested user's live manual state and call
// participation in one statement.
//
// "In a call" is the call domain's own definition of participation, narrowed to
// calls that are actually happening: a 1:1 call that was accepted, or a live
// participant lease on an active resource call. A ringing call is not one —
// nobody is talking yet. Expired overrides and expired leases are filtered by
// the database clock, so the answer holds whether or not anybody cleaned up.
//
// A lease-backed participation also reports when its latest live lease ends:
// that is how long the activity holds without a renewal, and a composition
// that used it may not be committed after it (issue #798).
//
// The leases are read FOR SHARE. Extending a live lease is not announced to
// presence — it changes no fact — and it is told apart from reviving a lapsed
// one by the lease's end against the database clock at the renewal. Reading
// under the row lock orders this read against that renewal: a read that
// overlaps an uncommitted renewal waits for it and sees the new end; a renewal
// that comes after this read is judged later than this read was. So a lease
// this read saw lapsed can only come back through a renewal judged a revival,
// which is announced.
const contextsSQL = `
	SELECT u.user_id::text, p.manual_state, p.manual_expires_at, p.manual_updated_at,
	       EXISTS (
	            SELECT 1 FROM chat.calls c
	            WHERE c.workspace_id = $1::uuid AND c.target_type = 'user' AND c.status = 'active'
	              AND (c.caller_id = u.user_id OR c.callee_id = u.user_id)) AS direct_call,
	       (SELECT max(held.expires_at) FILTER (WHERE held.expires_at > clock_timestamp())
	            FROM (SELECT l.expires_at FROM chat.call_participant_leases l
	                  JOIN chat.calls rc ON rc.id = l.call_id
	                  WHERE rc.workspace_id = $1::uuid AND rc.status = 'active' AND l.user_id = u.user_id
	                  FOR SHARE OF l) AS held) AS lease_until
	FROM unnest($2::uuid[]) AS u(user_id)
	LEFT JOIN chat.user_presence p
	  ON p.workspace_id = $1::uuid AND p.user_id = u.user_id
	 AND p.manual_state IS NOT NULL AND p.manual_expires_at > clock_timestamp()`

// Contexts returns the presence context of each requested user who has one. A
// user absent from the result has no live manual state and is not in a call.
func (s *PGXPresenceStore) Contexts(
	ctx context.Context, workspaceID string, userIDs []string,
) (map[string]domain.PresenceContext, error) {
	if len(userIDs) == 0 {
		return map[string]domain.PresenceContext{}, nil
	}
	rows, err := s.pool.Query(ctx, contextsSQL, workspaceID, userIDs)
	if err != nil {
		return nil, fmt.Errorf("read presence contexts: %w", err)
	}
	defer rows.Close()
	contexts := make(map[string]domain.PresenceContext, len(userIDs))
	for rows.Next() {
		userID, presence, err := scanPresenceContext(rows)
		if err != nil {
			return nil, err
		}
		if presence != (domain.PresenceContext{}) {
			contexts[userID] = presence
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate presence contexts: %w", err)
	}
	return contexts, nil
}

func scanPresenceContext(rows pgx.Rows) (string, domain.PresenceContext, error) {
	var (
		userID     string
		state      *string
		expiresAt  *time.Time
		updatedAt  *time.Time
		directCall bool
		leaseUntil *time.Time
	)
	if err := rows.Scan(&userID, &state, &expiresAt, &updatedAt, &directCall, &leaseUntil); err != nil {
		return "", domain.PresenceContext{}, fmt.Errorf("scan presence context: %w", err)
	}
	var presence domain.PresenceContext
	if state != nil && expiresAt != nil && updatedAt != nil {
		presence.Override = domain.PresenceOverride{
			State: domain.PresenceManualState(*state), ExpiresAt: *expiresAt, UpdatedAt: *updatedAt,
		}
	}
	if directCall || leaseUntil != nil {
		presence.Activity = domain.PresenceActivityInCall
	}
	if !directCall && leaseUntil != nil {
		presence.ActivityUntil = leaseUntil.UTC()
	}
	return userID, presence, nil
}

// DoNotDisturbUsers returns which of userIDs are in a live manual Do Not
// Disturb. It is the notification pipeline's read: lean, one statement for a
// whole subscriber list, and nothing about calls.
func (s *PGXPresenceStore) DoNotDisturbUsers(
	ctx context.Context, workspaceID string, userIDs []string,
) (map[string]bool, error) {
	if len(userIDs) == 0 {
		return map[string]bool{}, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT user_id::text FROM chat.user_presence
		WHERE workspace_id = $1::uuid AND user_id = ANY($2::uuid[])
		  AND manual_state = 'dnd' AND manual_expires_at > clock_timestamp()`, workspaceID, userIDs)
	if err != nil {
		return nil, fmt.Errorf("read do-not-disturb users: %w", err)
	}
	defer rows.Close()
	dnd := make(map[string]bool, 4)
	for rows.Next() {
		var userID string
		if err := rows.Scan(&userID); err != nil {
			return nil, fmt.Errorf("scan do-not-disturb user: %w", err)
		}
		dnd[userID] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate do-not-disturb users: %w", err)
	}
	return dnd, nil
}

// MarkLastSeen records that the server published this member as offline at
// the given server instant. GREATEST keeps it monotonic when two replicas
// report the same departure.
func (s *PGXPresenceStore) MarkLastSeen(ctx context.Context, workspaceID, userID string, at time.Time) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO chat.user_presence (workspace_id, user_id, last_seen_at)
		SELECT wm.workspace_id, wm.user_id, $3
		FROM chat.workspace_members wm
		WHERE wm.workspace_id = $1::uuid AND wm.user_id = $2::uuid
		ON CONFLICT (workspace_id, user_id) DO UPDATE
		SET last_seen_at = GREATEST(chat.user_presence.last_seen_at, EXCLUDED.last_seen_at)`,
		workspaceID, userID, at)
	if err != nil {
		return fmt.Errorf("mark last seen: %w", err)
	}
	return nil
}

// LastSeen returns when the member was last published as offline, if ever.
func (s *PGXPresenceStore) LastSeen(ctx context.Context, workspaceID, userID string) (time.Time, bool, error) {
	var at *time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT last_seen_at FROM chat.user_presence
		WHERE workspace_id = $1::uuid AND user_id = $2::uuid`, workspaceID, userID).Scan(&at)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && at == nil) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("read last seen: %w", err)
	}
	return at.UTC(), true, nil
}
