package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nicrepository/nchat/libs/go/platform/urlsafety"
)

// Per-target reads and convergence for issue #807.
//
// chat.link_scans is the link *target* table: one row per canonical URL, with
// the verdict pipeline's state. What this file adds is the read the message
// hydration needs — every target's current state in one query — and the sweep
// that makes "pending" a state with an end.

const (
	// LinkScanPendingDeadline is how long a target may stay pending before the
	// sweep terminalises it as unknown. Five minutes is an order of magnitude
	// past a normal scan (tens of seconds) and short enough that a provider
	// outage costs a reader five minutes of "checking" rather than a day. The
	// message itself was published at second zero; only the anchor waits.
	LinkScanPendingDeadline = 5 * time.Minute

	// Terminal reasons, mirroring the CHECK on chat.link_scans. Diagnostic
	// values, never metric labels with user content.
	TerminalReasonProvider  = "provider"
	TerminalReasonDeadline  = "deadline"
	TerminalReasonSensitive = "sensitive"
	TerminalReasonInternal  = "internal"
	TerminalReasonDisabled  = "disabled"

	// maxTerminalizeBatch bounds one sweep pass, so a provider outage that left
	// thousands of rows pending is drained in bounded statements.
	maxTerminalizeBatch = 200
)

// freshVerdictSQL is the one definition of a fresh verdict: decided within
// urlsafety.VerdictTTL, and not past whatever earlier limit the provider stated.
// alias qualifies chat.link_scans; ttlParam is the placeholder bound to
// urlsafety.VerdictTTL.Seconds(). Every reader that cares whether a clearance
// still counts — the send-path verdict load, the cost classification, the
// per-link read model, the preview claim and the preview image route — spells it
// through here so they cannot drift apart.
//
// The second clause is issue #928. A verdict is evidence, and evidence has two
// independent lifetimes: how long this deployment is willing to reuse an answer,
// and how long the provider is willing to stand behind it. Google Web Risk
// states the second with a threat match, and a condemnation may not outlive the
// evidence it rests on. So the two are a conjunction — the verdict expires at
// whichever comes first — and NULL, which is every row written before this and
// every answer from a provider that states no limit, leaves the local window
// alone in charge.
//
// It is a ceiling and never an extension: an evidence_expires_at *after*
// decided_at + VerdictTTL changes nothing, because the first clause has already
// expired the row. There is no arrangement of the two columns that reuses an
// answer for longer than VerdictTTL.
func freshVerdictSQL(alias, ttlParam string) string {
	return alias + ".decided_at IS NOT NULL" +
		" AND " + alias + ".decided_at > now() - (" + ttlParam + " * interval '1 second')" +
		" AND (" + alias + ".evidence_expires_at IS NULL OR " + alias + ".evidence_expires_at > now())"
}

// safeFreshVerdictSQL is the only clearance a preview may be fetched or served
// on: explicitly safe, and still inside its TTL.
func safeFreshVerdictSQL(alias, ttlParam string) string {
	return alias + ".status = 'safe' AND " + freshVerdictSQL(alias, ttlParam)
}

// pendingWithinDeadlineSQL is the one definition of a pending target that may
// still be worked on: pending, and not yet past its deadline. Every normal
// transition out of pending — the claim, the submission intent, the scan id,
// the verdict — is predicated on it, so once the deadline has passed the only
// transition left is the sweep's own (pending → unknown/deadline). The
// deadline is thereby a fact of the state machine, decided by the database's
// clock at the moment of each write, and not a courtesy of the sweep running
// first: a leftover the sweep's batch did not reach is not claimable, and a
// provider answer that arrives after the deadline finds nothing to write.
func pendingWithinDeadlineSQL(alias string) string {
	return alias + ".status = 'pending' AND " + alias + ".deadline_at > now()"
}

// LinkTargetState is what is durably known about one canonical URL.
type LinkTargetState struct {
	CanonicalURL string
	// Status is the raw pipeline status: pending, safe, malicious, inconclusive
	// or unknown. The service maps it onto the client-facing vocabulary.
	Status string
	// Fresh reports whether a terminal status is still inside its TTL. A stale
	// clearance is reported as-is with Fresh=false; the policy layer decides
	// whether stale-while-revalidate applies.
	Fresh     bool
	DecidedAt time.Time
	UpdatedAt time.Time
}

// LoadLinkTargets returns the current state of every canonical URL named, in
// one query. A URL with no row is absent from the map and reads as pending.
func (s *PGXMessageStore) LoadLinkTargets(ctx context.Context, canonicalURLs []string) (map[string]LinkTargetState, error) {
	if len(canonicalURLs) == 0 {
		return map[string]LinkTargetState{}, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT ls.canonical_url, ls.status,
		       `+freshVerdictSQL("ls", "$2")+`,
		       COALESCE(ls.decided_at, 'epoch'::timestamptz), ls.updated_at
		FROM chat.link_scans ls
		WHERE ls.canonical_url = ANY($1::text[])`,
		uniqueSortedURLs(canonicalURLs), urlsafety.VerdictTTL.Seconds())
	if err != nil {
		return nil, fmt.Errorf("load link targets: %w", err)
	}
	defer rows.Close()
	targets := make(map[string]LinkTargetState, len(canonicalURLs))
	for rows.Next() {
		var target LinkTargetState
		if err := rows.Scan(&target.CanonicalURL, &target.Status, &target.Fresh,
			&target.DecidedAt, &target.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan link target: %w", err)
		}
		targets[target.CanonicalURL] = target
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load link targets: %w", err)
	}
	return targets, nil
}

// TerminalizeExpiredLinkScans turns pending targets past their deadline into
// unknown, and returns the URLs it changed so the caller can announce them.
//
// This is the sentence issue #807 exists for: no non-terminal state without an
// end. It runs on every worker pass regardless of the feature flag — a flag that
// stopped the sweep would be a flag that strands rows — and it is idempotent: a
// row already terminal is not matched.
//
// An uncertain submission (attempt started, no scan id) terminalises too. Its
// scan may exist at the provider; nothing here resubmits it, and nothing ever
// will for this generation. Should the URL be named again after the TTL, the
// row is reopened and one new submission may be made — a bounded, documented
// trade against eternal waiting.
func (s *PGXMessageStore) TerminalizeExpiredLinkScans(ctx context.Context) ([]string, error) {
	return s.terminalizePending(ctx, `ls.deadline_at IS NOT NULL AND ls.deadline_at <= now()`, TerminalReasonDeadline)
}

// TerminalizePendingLinkScansDisabled converges every pending target at once
// when Link Safety is switched off: no provider will ever answer, so waiting
// for a deadline would only be five minutes of pretending. The reason says why,
// so an operator turning the flag back on can tell these from real deadline
// expiries.
func (s *PGXMessageStore) TerminalizePendingLinkScansDisabled(ctx context.Context) ([]string, error) {
	return s.terminalizePending(ctx, `TRUE`, TerminalReasonDisabled)
}

// terminalizePending is the shared statement: a bounded batch of pending rows
// matching predicate become unknown with reason. predicate is code, never
// input.
func (s *PGXMessageStore) terminalizePending(ctx context.Context, predicate, reason string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		WITH due AS (
			SELECT ls.canonical_url
			FROM chat.link_scans ls
			WHERE ls.status = 'pending'
			  AND `+predicate+`
			ORDER BY ls.created_at
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE chat.link_scans ls
		   SET status = 'unknown', decided_at = now(), terminal_reason = $2,
		       next_attempt_at = NULL, updated_at = now()
		  FROM due
		 WHERE ls.canonical_url = due.canonical_url
		RETURNING ls.canonical_url`, maxTerminalizeBatch, reason)
	if err != nil {
		return nil, fmt.Errorf("terminalize pending link scans: %w", err)
	}
	defer rows.Close()
	return scanURLs(rows)
}

// RecordLinkTargetTerminal writes a terminal state decided without the provider
// — a sensitive URL, an internal host — for a row the worker holds a claim on.
//
// Only a pending row is written, by compare-and-set: a verdict that landed from
// another worker in the meantime wins, exactly as RecordLinkVerdict behaves.
func (s *PGXMessageStore) RecordLinkTargetTerminal(ctx context.Context, canonicalURL, reason string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE chat.link_scans
		   SET status = 'unknown', decided_at = now(), terminal_reason = $2,
		       next_attempt_at = NULL, updated_at = now()
		 WHERE canonical_url = $1
		   AND status = 'pending'`, canonicalURL, reason)
	if err != nil {
		return fmt.Errorf("record link target terminal: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrLinkScanConflict
	}
	return nil
}

// LinkReference is one published message that names a target, with the
// routing the realtime announcement needs.
type LinkReference struct {
	MessageID   string
	WorkspaceID string
	TargetType  string
	TargetID    string
}

// MessagesReferencingLink lists the published messages that currently name
// canonicalURL, in a bounded page ordered by message id. workspaceID narrows
// the scan to one tenant when non-empty (previews are workspace-scoped);
// afterMessageID pages.
//
// The association is matched on the message's current fingerprint, so a body
// that was edited to drop the URL is not listed — the fan-out for a stale
// association would announce a link the message no longer has.
func (s *PGXMessageStore) MessagesReferencingLink(
	ctx context.Context, canonicalURL, workspaceID, afterMessageID string, limit int,
) ([]LinkReference, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT m.id::text, m.workspace_id::text,
		       COALESCE(m.channel_id::text, ''), COALESCE(m.dm_conversation_id::text, '')
		FROM chat.message_link_scans mls
		JOIN chat.messages m ON m.id = mls.message_id
		WHERE mls.canonical_url = $1
		  AND mls.fingerprint = COALESCE(m.link_safety_fingerprint, '')
		  AND m.status = 'active'
		  AND ($2 = '' OR m.workspace_id = $2::uuid)
		  AND ($3 = '' OR m.id > $3::uuid)
		ORDER BY m.id
		LIMIT $4`, canonicalURL, workspaceID, afterMessageID, limit)
	if err != nil {
		return nil, fmt.Errorf("messages referencing link: %w", err)
	}
	defer rows.Close()
	var refs []LinkReference
	for rows.Next() {
		var ref LinkReference
		var channelID, conversationID string
		if err := rows.Scan(&ref.MessageID, &ref.WorkspaceID, &channelID, &conversationID); err != nil {
			return nil, fmt.Errorf("scan link reference: %w", err)
		}
		ref.TargetType, ref.TargetID = TargetDM, conversationID
		if channelID != "" {
			ref.TargetType, ref.TargetID = TargetChannel, channelID
		}
		refs = append(refs, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("messages referencing link: %w", err)
	}
	return refs, nil
}

// LinkWorkspacesReferencing lists the workspaces whose published messages name
// canonicalURL — the tenants a freshly cleared target needs a preview queued
// for.
func (s *PGXMessageStore) LinkWorkspacesReferencing(ctx context.Context, canonicalURL string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT m.workspace_id::text
		FROM chat.message_link_scans mls
		JOIN chat.messages m ON m.id = mls.message_id
		WHERE mls.canonical_url = $1
		  AND mls.fingerprint = COALESCE(m.link_safety_fingerprint, '')
		  AND m.status = 'active'`, canonicalURL)
	if err != nil {
		return nil, fmt.Errorf("link workspaces referencing: %w", err)
	}
	defer rows.Close()
	return scanURLs(rows)
}

// LoadMessageBodies returns body_text for the given message ids, unwithheld.
//
// It exists for exactly one caller: the link hydration that redacts a condemned
// URL's span while keeping the rest of the text (issue #807 §3). Every
// projection still withholds a malicious body in SQL, so a read path that does
// not go through that hydration shows nothing rather than the URL — fail-closed
// by default, and this is the one deliberate exception.
func (s *PGXMessageStore) LoadMessageBodies(ctx context.Context, messageIDs []string) (map[string]string, error) {
	if len(messageIDs) == 0 {
		return map[string]string{}, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id::text, body_text FROM chat.messages WHERE id = ANY($1::uuid[])`, messageIDs)
	if err != nil {
		return nil, fmt.Errorf("load message bodies: %w", err)
	}
	defer rows.Close()
	bodies := make(map[string]string, len(messageIDs))
	for rows.Next() {
		var id, body string
		if err := rows.Scan(&id, &body); err != nil {
			return nil, fmt.Errorf("scan message body: %w", err)
		}
		bodies[id] = body
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load message bodies: %w", err)
	}
	return bodies, nil
}

// scanURLs drains a single-text-column result set.
func scanURLs(rows pgx.Rows) ([]string, error) {
	var values []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, fmt.Errorf("scan text column: %w", err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return values, nil
}
