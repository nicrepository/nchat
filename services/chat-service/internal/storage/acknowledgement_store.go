package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// AcknowledgeInput identifies the message the authenticated user is confirming.
//
// RecipientID is the authenticated principal and nothing else. There is no
// field here for a client-supplied recipient, deliberately: acknowledging on
// somebody else's behalf is not an operation this store can express, so it
// cannot be reached by forging a payload the handler forgot to ignore.
type AcknowledgeInput struct {
	WorkspaceID string
	MessageID   string
	RecipientID string
}

// ReadAcknowledgementBatchInput asks about every message one timeline page is
// showing, in one request.
//
// MessageIDs is already normalized by the service: bounded, parsed as UUIDs and
// deduplicated, so this layer binds an array it can trust to be small and
// well-formed.
type ReadAcknowledgementBatchInput struct {
	WorkspaceID string
	MessageIDs  []string
	ViewerID    string
}

// ReadAcknowledgementInput identifies the message whose acknowledgement state
// the caller wants to read, and who is asking.
type ReadAcknowledgementInput struct {
	WorkspaceID string
	MessageID   string
	ViewerID    string
}

// AcknowledgementRoute names the conversation a message lives in.
//
// It exists so a caller that has just changed an acknowledgement can announce
// it to exactly that conversation's subscribers without looking the message up
// a second time. It is routing and nothing else: no recipient, no state, no
// count — those reach a reader through the authorised read, which is the only
// place the server can decide what that particular reader may see.
type AcknowledgementRoute struct {
	// TargetType is "channel" or "dm", the vocabulary the WebSocket hub routes
	// by. Empty when the message resolved to neither, which cannot happen for a
	// message the authorization CTE admitted.
	TargetType string
	TargetID   string
}

// AcknowledgeResult is what one acknowledgement did.
type AcknowledgeResult struct {
	Summary domain.AcknowledgementSummary
	Route   AcknowledgementRoute
	// Changed reports whether this call actually moved a row from pending.
	//
	// False for a retry, a double click, and a request whose row some other
	// transition already resolved. It is what keeps an idempotent replay from
	// announcing a change that did not happen: the answer to the caller is the
	// same either way, but there is nothing to tell the conversation about.
	Changed bool
}

// AcknowledgementStore is the persistence interface for per-recipient
// acknowledgement (issue #824).
type AcknowledgementStore interface {
	// Acknowledge moves the caller's own row from pending to acknowledged and
	// returns the message's acknowledgement as that caller may see it.
	//
	// Idempotent, and idempotent under concurrency: the transition is one
	// conditional UPDATE, so a retry, a double click and two simultaneous
	// requests all leave exactly one row in exactly one state. A caller whose
	// row is already terminal — they replied, the sender withdrew the message —
	// is not an error: they are told the state that actually holds.
	//
	// Returns ErrNotFound when the message does not exist, is not readable by
	// the caller, or never asked this caller for anything. One answer for all
	// three, so the endpoint cannot be used to discover which messages exist.
	Acknowledge(ctx context.Context, input AcknowledgeInput) (AcknowledgeResult, error)

	// ReadAcknowledgement returns the counts, the viewer's own state and — for
	// the message's sender only — the per-recipient detail. Same ErrNotFound
	// contract as Acknowledge for a message the viewer may not read.
	//
	// All three come from one statement and therefore from one snapshot: a
	// response never reports counts taken before a transition alongside detail
	// taken after it.
	ReadAcknowledgement(ctx context.Context, input ReadAcknowledgementInput) (domain.AcknowledgementSummary, error)

	// ReadAcknowledgementBatch answers for a whole page at once, keyed by
	// message id.
	//
	// One statement, whatever the page holds: the earlier shape asked per
	// message, so opening a conversation with twenty urgent notices cost twenty
	// round trips and twenty aggregations. Authorization is unchanged and still
	// per message — a message the viewer may not read is simply absent from the
	// map rather than refused, which is the same non-enumerating answer the
	// single read gives and the shape every other batch read here uses.
	//
	// Deliberately carries no per-recipient detail. That is the sender's view of
	// one message, asked for by opening it; putting it in a page-sized response
	// would ship every recipient of every asking message on screen to somebody
	// who may only be entitled to the counts.
	ReadAcknowledgementBatch(
		ctx context.Context, input ReadAcknowledgementBatchInput,
	) (map[string]domain.AcknowledgementSummary, error)
}

// PGXAcknowledgementStore implements AcknowledgementStore using a pgx pool.
type PGXAcknowledgementStore struct{ pool Pool }

// NewPGXAcknowledgementStore creates a PGXAcknowledgementStore backed by the
// given pool.
func NewPGXAcknowledgementStore(pool Pool) *PGXAcknowledgementStore {
	return &PGXAcknowledgementStore{pool: pool}
}

// acknowledgementAuthorizedCTE resolves one message id to the row this viewer
// is allowed to read, or to nothing at all.
//
// Written once and shared by the read path and the write path below, because
// the two must agree on what "authorized" means: a rule that is stated twice is
// a rule that drifts, and the half that drifts is whichever one nobody is
// looking at.
//
// Authorization is the shared message-access predicate every other
// message-scoped surface uses (favorites, pins, reactions), so a guest reaches
// exactly the channels chat.channel_visible_to_user admits them to and a
// removed member reaches nothing. It is re-evaluated on every request: being a
// recipient when the message was sent does not keep a door open after the
// conversation was left.
//
// A deleted message is deliberately still readable here. It is a tombstone
// everywhere else in this service, and its recipients' rows were cancelled by
// the delete — a sender who withdrew a request is owed the answer "cancelled",
// not a 404 that suggests it never happened.
var acknowledgementAuthorizedCTE = `
	WITH authorized AS (
		SELECT m.id, m.sender_id, m.acknowledgement_required,
		       COALESCE(m.channel_id::text, '') AS channel_id,
		       COALESCE(m.dm_conversation_id::text, '') AS dm_conversation_id
		FROM chat.messages m` + messageAccessJoins("$2") + `
		WHERE m.workspace_id = $1 AND m.id = $3::uuid
		  AND ` + messageVisibilityPredicate("m", "$2") + `
		  AND ` + messageAccessPredicate("$2") + `
	)`

// acknowledgementSummaryQuery is how a write reports what it did: the counts
// and the writer's own state, with no per-recipient detail, because the person
// confirming a message is not the person entitled to the list.
var acknowledgementSummaryQuery = acknowledgementAuthorizedCTE + `
	SELECT a.acknowledgement_required,
	       a.sender_id = $2::uuid,
	       a.channel_id, a.dm_conversation_id,
	       counts.total, counts.pending, counts.acknowledged,
	       counts.responded, counts.expired, counts.cancelled,
	       COALESCE(mine.state, '')
	FROM authorized a
	-- One pass over this message's rows, aggregated in the database. The counts
	-- are FILTERed rather than grouped so the whole summary is a single row and
	-- a message that asked nobody still answers with zeros instead of no row.
	LEFT JOIN LATERAL (
		SELECT count(*)::int AS total,
		       count(*) FILTER (WHERE ma.state = 'pending')::int      AS pending,
		       count(*) FILTER (WHERE ma.state = 'acknowledged')::int AS acknowledged,
		       count(*) FILTER (WHERE ma.state = 'responded')::int    AS responded,
		       count(*) FILTER (WHERE ma.state = 'expired')::int      AS expired,
		       count(*) FILTER (WHERE ma.state = 'cancelled')::int    AS cancelled
		FROM chat.message_acknowledgements ma
		WHERE ma.message_id = a.id
	) counts ON true
	-- The viewer's own row, by full primary key. Absent for the sender of a
	-- group message and for anybody who joined after it was sent, and absent is
	-- reported as the empty string rather than as a state.
	LEFT JOIN chat.message_acknowledgements mine
	  ON mine.message_id = a.id AND mine.recipient_id = $2::uuid`

// acknowledgementBatchQuery answers for a page of messages in one statement.
//
// Set-based throughout, which is the whole point: `authorized` filters the
// requested ids down to the ones this viewer may read, and the two CTEs below
// aggregate over that set once — a GROUP BY rather than a per-message subquery,
// so the cost is three index scans for a page instead of three per message.
// Replacing N HTTP calls with N SQL round trips would have moved the problem
// rather than fixed it.
//
// Authorization is per message and identical to the single read's: the same
// shared predicate, evaluated for every id in the batch. An id the viewer may
// not read produces no row in `authorized` and therefore no entry in the answer
// — so a caller cannot mix ids from another conversation, or another tenant,
// into one request and learn anything about them. Absence is the only signal,
// and absence is what a message that does not exist produces too.
//
// No per-recipient detail: this is the page view. Who answered is the sender's
// question about one message, asked through the single read.
var acknowledgementBatchQuery = `
	WITH authorized AS (
		SELECT m.id, m.sender_id, m.acknowledgement_required
		FROM chat.messages m` + messageAccessJoins("$2") + `
		WHERE m.workspace_id = $1 AND m.id = ANY($3::uuid[])
		  AND ` + messageVisibilityPredicate("m", "$2") + `
		  AND ` + messageAccessPredicate("$2") + `
	),
	counts AS (
		SELECT ma.message_id,
		       count(*)::int                                          AS total,
		       count(*) FILTER (WHERE ma.state = 'pending')::int       AS pending,
		       count(*) FILTER (WHERE ma.state = 'acknowledged')::int  AS acknowledged,
		       count(*) FILTER (WHERE ma.state = 'responded')::int     AS responded,
		       count(*) FILTER (WHERE ma.state = 'expired')::int       AS expired,
		       count(*) FILTER (WHERE ma.state = 'cancelled')::int     AS cancelled
		FROM chat.message_acknowledgements ma
		JOIN authorized a ON a.id = ma.message_id
		GROUP BY ma.message_id
	),
	mine AS (
		SELECT ma.message_id, ma.state
		FROM chat.message_acknowledgements ma
		JOIN authorized a ON a.id = ma.message_id
		WHERE ma.recipient_id = $2::uuid
	)
	SELECT a.id::text,
	       a.acknowledgement_required,
	       COALESCE(c.total, 0), COALESCE(c.pending, 0), COALESCE(c.acknowledged, 0),
	       COALESCE(c.responded, 0), COALESCE(c.expired, 0), COALESCE(c.cancelled, 0),
	       COALESCE(mine.state, '')
	FROM authorized a
	LEFT JOIN counts c ON c.message_id = a.id
	LEFT JOIN mine ON mine.message_id = a.id`

// acknowledgementReadQuery answers one read completely: the counts, the
// viewer's own state and — for the sender — the per-recipient detail.
//
// Summary and detail must share one PostgreSQL statement snapshot. Asked as two
// statements they were two snapshots under READ COMMITTED, so an
// acknowledgement committing between them produced one response whose counts
// said pending while its detail already said acknowledged. Here `asked` is read
// once and the counts, the viewer's row and the detail are all projections of
// it, so there is a single definition of a recipient's state per statement and
// nothing to drift against.
//
// The detail hangs off the sender check in the join condition rather than being
// dropped in Go afterwards: who may see the list decides whether the rows exist
// at all, so there is no branch left that could forget to discard them. It
// carries no LIMIT, which is a property of the schema rather than an oversight
// — domain.MaxAcknowledgementRecipients bounds how many rows a send may create.
// Ordering by recipient_id makes the answer stable between two reads of the
// same unchanged message.
var acknowledgementReadQuery = acknowledgementAuthorizedCTE + `,
	asked AS (
		SELECT ma.recipient_id, ma.state, ma.resolved_at
		FROM chat.message_acknowledgements ma
		JOIN authorized a ON a.id = ma.message_id
	)
	SELECT a.acknowledgement_required,
	       a.sender_id = $2::uuid,
	       a.channel_id, a.dm_conversation_id,
	       counts.total, counts.pending, counts.acknowledged,
	       counts.responded, counts.expired, counts.cancelled,
	       COALESCE(mine.state, ''),
	       COALESCE(detail.recipient_id::text, ''),
	       COALESCE(detail.state, ''),
	       detail.resolved_at
	FROM authorized a
	LEFT JOIN LATERAL (
		SELECT count(*)::int AS total,
		       count(*) FILTER (WHERE asked.state = 'pending')::int      AS pending,
		       count(*) FILTER (WHERE asked.state = 'acknowledged')::int AS acknowledged,
		       count(*) FILTER (WHERE asked.state = 'responded')::int    AS responded,
		       count(*) FILTER (WHERE asked.state = 'expired')::int      AS expired,
		       count(*) FILTER (WHERE asked.state = 'cancelled')::int    AS cancelled
		FROM asked
	) counts ON true
	LEFT JOIN asked mine ON mine.recipient_id = $2::uuid
	LEFT JOIN asked detail ON a.sender_id = $2::uuid
	ORDER BY detail.recipient_id`

// acknowledgeStatement is the whole state machine, as one conditional UPDATE.
//
// state = 'pending' in the WHERE is what makes this safe without a lock, a
// SELECT beforehand or an idempotency key. PostgreSQL evaluates it against the
// row it actually locks, so of two concurrent acknowledgements one updates a
// row and the other updates none, and an acknowledgement racing a reply or a
// cancellation loses to whichever committed first instead of overwriting it. A
// terminal row is never rewritten and nothing can return to pending.
//
// It is also the whole of the idempotency contract. A second POST from the same
// person matches no pending row, changes nothing, and is answered from the
// state that is actually stored — never from an assumption made before the
// statement ran.
//
// Authorization is an EXISTS over the same predicate the summary uses, inside
// the same statement, so a caller who lost access between the two cannot slip a
// write in behind a read that still said yes.
var acknowledgeStatement = `
	UPDATE chat.message_acknowledgements a
	SET state = 'acknowledged', resolved_at = now()
	WHERE a.message_id = $3::uuid
	  AND a.recipient_id = $2::uuid
	  AND a.state = 'pending'
	  AND EXISTS (
		SELECT 1
		FROM chat.messages m` + messageAccessJoins("$2") + `
		WHERE m.workspace_id = $1 AND m.id = $3::uuid
		  AND ` + messageVisibilityPredicate("m", "$2") + `
		  AND ` + messageAccessPredicate("$2") + `
	  )`

func (s *PGXAcknowledgementStore) Acknowledge(
	ctx context.Context, input AcknowledgeInput,
) (AcknowledgeResult, error) {
	// One transaction, two statements, in this order on purpose: the UPDATE
	// decides, the read reports. Reading first and writing on what it said would
	// be the TOCTOU this shape exists to avoid, and folding the read into the
	// UPDATE's own statement would not work either — a CTE cannot see the rows
	// its sibling just wrote, so the summary would describe the state before the
	// acknowledgement it is meant to confirm.
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return AcknowledgeResult{}, fmt.Errorf("begin acknowledge: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, acknowledgeStatement,
		input.WorkspaceID, input.RecipientID, input.MessageID,
	)
	if err != nil {
		return AcknowledgeResult{}, fmt.Errorf("acknowledge message: %w", err)
	}

	summary, err := scanAcknowledgementSummary(tx.QueryRow(ctx, acknowledgementSummaryQuery,
		input.WorkspaceID, input.RecipientID, input.MessageID,
	))
	if err != nil {
		return AcknowledgeResult{}, err
	}
	// No row of their own means this message never asked this caller anything:
	// they are its sender, or they arrived after it was sent. Answered exactly
	// like an unreadable message, so neither can be told from the other.
	if summary.ViewerState == "" {
		return AcknowledgeResult{}, domain.ErrNotFound
	}
	if err := tx.Commit(ctx); err != nil {
		return AcknowledgeResult{}, fmt.Errorf("commit acknowledge: %w", err)
	}
	return AcknowledgeResult{
		Summary: summary.AcknowledgementSummary,
		Route:   summary.route,
		// Reported from the statement's own row count rather than by comparing
		// states: only the UPDATE knows whether it was the transition that won.
		Changed: tag.RowsAffected() > 0,
	}, nil
}

func (s *PGXAcknowledgementStore) ReadAcknowledgement(
	ctx context.Context, input ReadAcknowledgementInput,
) (domain.AcknowledgementSummary, error) {
	rows, err := s.pool.Query(ctx, acknowledgementReadQuery,
		input.WorkspaceID, input.ViewerID, input.MessageID)
	if err != nil {
		return domain.AcknowledgementSummary{}, fmt.Errorf("read acknowledgement: %w", err)
	}
	defer rows.Close()
	summary, err := scanAcknowledgementRows(rows)
	if err != nil {
		return domain.AcknowledgementSummary{}, err
	}
	return summary.AcknowledgementSummary, nil
}

func (s *PGXAcknowledgementStore) ReadAcknowledgementBatch(
	ctx context.Context, input ReadAcknowledgementBatchInput,
) (map[string]domain.AcknowledgementSummary, error) {
	summaries := make(map[string]domain.AcknowledgementSummary, len(input.MessageIDs))
	if len(input.MessageIDs) == 0 {
		return summaries, nil
	}
	rows, err := s.pool.Query(ctx, acknowledgementBatchQuery,
		input.WorkspaceID, input.ViewerID, input.MessageIDs)
	if err != nil {
		return nil, fmt.Errorf("read acknowledgement batch: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var messageID string
		var summary domain.AcknowledgementSummary
		if err := rows.Scan(
			&messageID, &summary.Required,
			&summary.Total, &summary.Pending, &summary.Acknowledged,
			&summary.Responded, &summary.Expired, &summary.Cancelled,
			(*string)(&summary.ViewerState),
		); err != nil {
			return nil, fmt.Errorf("scan acknowledgement batch: %w", err)
		}
		summaries[messageID] = summary
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate acknowledgement batch: %w", err)
	}
	return summaries, nil
}

// authorizedSummary is the summary plus the one fact only the query knows and
// the domain type deliberately does not carry: whether the viewer is the
// message's sender. It stays inside this package because "who may see the
// per-recipient detail" is a decision, and a decision belongs in one place
// rather than in a boolean travelling out to whoever reads the summary next.
type authorizedSummary struct {
	domain.AcknowledgementSummary
	senderIsViewer bool
	route          AcknowledgementRoute
}

// scanAcknowledgementRows folds one statement's rows into one answer.
//
// The message's own columns repeat on every recipient row, which is what
// carrying the aggregate and the detail in a single snapshot costs; each row
// therefore overwrites the summary with an identical value and contributes only
// its recipient. No rows at all means the message does not exist or this viewer
// may not read it — one answer for both, deliberately.
func scanAcknowledgementRows(rows pgx.Rows) (authorizedSummary, error) {
	var summary authorizedSummary
	authorized := false
	for rows.Next() {
		recipient, err := scanAcknowledgementRow(rows, &summary)
		if err != nil {
			return authorizedSummary{}, err
		}
		authorized = true
		if recipient != nil {
			summary.Recipients = append(summary.Recipients, *recipient)
		}
	}
	if err := rows.Err(); err != nil {
		return authorizedSummary{}, fmt.Errorf("read acknowledgement: %w", err)
	}
	if !authorized {
		return authorizedSummary{}, domain.ErrNotFound
	}
	return summary, nil
}

// scanAcknowledgementRow reads one row into summary and returns its recipient,
// or nil for a viewer who is not the sender and a message that asked nobody:
// both arrive as a null detail rather than as a missing row.
func scanAcknowledgementRow(
	rows pgx.Rows, summary *authorizedSummary,
) (*domain.AcknowledgementRecipient, error) {
	var channelID, conversationID, recipientID, state string
	var resolvedAt *time.Time
	err := rows.Scan(
		&summary.Required, &summary.senderIsViewer,
		&channelID, &conversationID,
		&summary.Total, &summary.Pending, &summary.Acknowledged,
		&summary.Responded, &summary.Expired, &summary.Cancelled,
		(*string)(&summary.ViewerState),
		&recipientID, &state, &resolvedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("scan acknowledgement: %w", err)
	}
	summary.route = messageRoute(channelID, conversationID)
	if recipientID == "" {
		return nil, nil
	}
	recipient := domain.AcknowledgementRecipient{
		RecipientID: recipientID,
		State:       domain.AcknowledgementState(state),
	}
	if resolvedAt != nil {
		recipient.ResolvedAt = *resolvedAt
	}
	return &recipient, nil
}

func scanAcknowledgementSummary(row pgx.Row) (authorizedSummary, error) {
	var summary authorizedSummary
	var channelID, conversationID string
	err := row.Scan(
		&summary.Required, &summary.senderIsViewer,
		&channelID, &conversationID,
		&summary.Total, &summary.Pending, &summary.Acknowledged,
		&summary.Responded, &summary.Expired, &summary.Cancelled,
		(*string)(&summary.ViewerState),
	)
	if err != nil {
		// Non-enumerating: a message that does not exist and one this caller may
		// not read produce the same zero rows and the same answer.
		if errors.Is(err, pgx.ErrNoRows) {
			return authorizedSummary{}, domain.ErrNotFound
		}
		return authorizedSummary{}, fmt.Errorf("read acknowledgement summary: %w", err)
	}
	summary.route = messageRoute(channelID, conversationID)
	return summary, nil
}

// messageRoute turns the message's two nullable destination columns into the
// hub's routing vocabulary. Exactly one is ever set, which the schema enforces.
func messageRoute(channelID, conversationID string) AcknowledgementRoute {
	if channelID != "" {
		return AcknowledgementRoute{TargetType: "channel", TargetID: channelID}
	}
	return AcknowledgementRoute{TargetType: "dm", TargetID: conversationID}
}
