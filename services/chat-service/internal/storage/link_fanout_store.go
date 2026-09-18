package storage

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Durable fan-out continuation (issue #807).
//
// Announcing a target's new state to every message naming it is bounded work
// per pass: one page of references, then the cursor is written here and the
// next pass — this process or another — continues from it. Nothing is lost to
// a failure in the middle: the row keeps its cursor until the page after it
// succeeds, and the lease expiring is the retry.

const (
	// LinkFanoutKindTarget announces a target's safety to every workspace.
	LinkFanoutKindTarget = "target"
	// LinkFanoutKindPreview announces one workspace's preview of a target.
	LinkFanoutKindPreview = "preview"

	// linkFanoutLease outlives one page: a query and one page of in-process
	// publications.
	linkFanoutLease = 60 * time.Second
	// linkFanoutMaxAttempts bounds retries of a page that keeps failing; past
	// it the fan-out is dropped and the messages converge on their next read.
	linkFanoutMaxAttempts = 10
)

// LinkFanout is one continuation: where the next page starts. ClaimID is the
// claim's identity — the store accepts an advance or a finish only from the
// claim that holds it.
type LinkFanout struct {
	ID           string
	CanonicalURL string
	// WorkspaceID is empty for a target fan-out, which spans every workspace.
	WorkspaceID string
	Kind        string
	// AfterMessageID is the last message announced; empty means from the start.
	AfterMessageID string
	Attempts       int
	ClaimID        string
}

// ErrLinkFanoutConflict reports that a compare-and-set on a continuation
// lost: the claim was superseded — its lease lapsed and another pass claimed
// the row, or a newer announcement restarted it. The caller's page is not
// recorded; the current claim owns the cursor.
var ErrLinkFanoutConflict = errors.New("link fanout: superseded")

// BeginLinkFanout records that canonicalURL has to be announced from the
// start. An existing continuation for the same key is restarted rather than
// duplicated: what it was announcing has been superseded by the newer state,
// and every page re-reads the current state anyway. The restart mints a new
// claim id, so whoever held the row before can neither advance nor finish it.
// The row is returned leased under that claim, so the caller may run its first
// page at once while no other pass claims it.
func (s *PGXMessageStore) BeginLinkFanout(ctx context.Context, canonicalURL, workspaceID, kind string) (LinkFanout, error) {
	fanout := LinkFanout{CanonicalURL: canonicalURL, WorkspaceID: workspaceID, Kind: kind}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO chat.link_fanouts (canonical_url, workspace_id, kind, lease_until, claim_id)
		VALUES ($1, NULLIF($2, '')::uuid, $3, now() + ($4 * interval '1 second'), gen_random_uuid())
		ON CONFLICT (canonical_url, workspace_key, kind) DO UPDATE
		   SET after_message_id = NULL, attempts = 0, claim_id = gen_random_uuid(),
		       lease_until = now() + ($4 * interval '1 second'), updated_at = now()
		RETURNING id::text, claim_id::text`,
		canonicalURL, workspaceID, kind, linkFanoutLease.Seconds()).Scan(&fanout.ID, &fanout.ClaimID)
	if err != nil {
		return LinkFanout{}, fmt.Errorf("begin link fanout: %w", err)
	}
	return fanout, nil
}

// ClaimDueLinkFanouts leases up to limit continuations whose lease has lapsed,
// the longest-waiting first, so one popular target cannot starve the rest. A
// continuation past its attempt ceiling is dropped instead of claimed.
func (s *PGXMessageStore) ClaimDueLinkFanouts(ctx context.Context, limit int) ([]LinkFanout, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `
		WITH expired AS (
			DELETE FROM chat.link_fanouts WHERE attempts >= $3 AND (lease_until IS NULL OR lease_until <= now())
		), due AS (
			SELECT f.id FROM chat.link_fanouts f
			WHERE (f.lease_until IS NULL OR f.lease_until <= now()) AND f.attempts < $3
			ORDER BY f.updated_at
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE chat.link_fanouts f
		   SET lease_until = now() + ($2 * interval '1 second'), attempts = f.attempts + 1,
		       claim_id = gen_random_uuid()
		  FROM due
		 WHERE f.id = due.id
		RETURNING f.id::text, f.canonical_url, COALESCE(f.workspace_id::text, ''), f.kind,
		          COALESCE(f.after_message_id::text, ''), f.attempts, f.claim_id::text`,
		limit, linkFanoutLease.Seconds(), linkFanoutMaxAttempts)
	if err != nil {
		return nil, fmt.Errorf("claim due link fanouts: %w", err)
	}
	defer rows.Close()
	var fanouts []LinkFanout
	for rows.Next() {
		var f LinkFanout
		if err := rows.Scan(&f.ID, &f.CanonicalURL, &f.WorkspaceID, &f.Kind, &f.AfterMessageID, &f.Attempts, &f.ClaimID); err != nil {
			return nil, fmt.Errorf("scan link fanout: %w", err)
		}
		fanouts = append(fanouts, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("claim due link fanouts: %w", err)
	}
	return fanouts, nil
}

// AdvanceLinkFanout records that every message up to afterMessageID has been
// announced and releases the lease so the next page can be claimed. The
// attempt counter is reset: progress was made. Compare-and-set on the claim:
// a superseded claim answers ErrLinkFanoutConflict and moves nothing.
func (s *PGXMessageStore) AdvanceLinkFanout(ctx context.Context, claim LinkFanout, afterMessageID string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE chat.link_fanouts
		   SET after_message_id = $3::uuid, attempts = 0, lease_until = NULL, claim_id = NULL, updated_at = now()
		 WHERE `+ownedFanoutSQL("$1", "$2"), claim.ID, claim.ClaimID, afterMessageID)
	if err != nil {
		return fmt.Errorf("advance link fanout: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrLinkFanoutConflict
	}
	return nil
}

// FinishLinkFanout removes a continuation whose last page was announced.
// Same compare-and-set as AdvanceLinkFanout: a superseded claim cannot end —
// or delete — the continuation that replaced it.
func (s *PGXMessageStore) FinishLinkFanout(ctx context.Context, claim LinkFanout) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM chat.link_fanouts WHERE `+ownedFanoutSQL("$1", "$2"), claim.ID, claim.ClaimID)
	if err != nil {
		return fmt.Errorf("finish link fanout: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrLinkFanoutConflict
	}
	return nil
}

// ownedFanoutSQL is the predicate every claim outcome runs under: the row still
// carries the claim id this pass was handed. A released row (claim_id NULL) or
// a restarted one (new claim id) matches nothing, so there is no ABA: a claim
// id is minted once and never reissued.
func ownedFanoutSQL(idParam, claimParam string) string {
	return "id = " + idParam + "::uuid AND claim_id = " + claimParam + "::uuid"
}
