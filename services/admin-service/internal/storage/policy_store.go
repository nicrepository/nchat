package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
)

// PGXPolicyStore reads and writes the two operational policies that are
// genuinely configurable at runtime: the RF-19 per-user message budget and the
// RF-32 attachment size limit, both columns on chat.workspaces.
//
// It writes the same columns chat-service's workspace-admin endpoints write,
// under the same database CHECK constraints, so there is one stored value and
// one set of bounds no matter which scope changed it. What differs is the
// authorization: chat-service asks "does this person administer this
// workspace", this service asks "does this principal hold the platform
// capability". Neither is the other, and neither is skipped.
//
// There is deliberately no generic "set config key" method here. Every
// deployment knob outside these two columns lives in an environment variable
// read at boot, and pretending one of those can be edited at runtime would show
// an operator a saved value that changes nothing.
type PGXPolicyStore struct {
	pool Pool
}

func NewPGXPolicyStore(pool Pool) *PGXPolicyStore {
	return &PGXPolicyStore{pool: pool}
}

const workspacePolicySelect = `
	SELECT w.id::text, w.slug, w.name, w.status,
	       w.message_rate_limit_per_minute, w.max_upload_bytes, w.created_at
	FROM chat.workspaces AS w
	WHERE ($1::timestamptz IS NULL OR (w.created_at, w.id) < ($1, $2::uuid))
	ORDER BY w.created_at DESC, w.id DESC
	LIMIT $3`

// workspacePolicyRow is both policies of one workspace, read together.
//
// One query serves both listings because the two columns live on the same row:
// reading it twice would double the work to hand each caller half of what the
// scan already produced. The *authorization* is still separate — each endpoint
// names its own capability and projects only its own field — which is where
// that decision belongs.
type workspacePolicyRow struct {
	Workspace                 domain.WorkspaceRef
	MessageRateLimitPerMinute int
	MaxUploadBytes            int64
	// CreatedAt is the ordering key. It is carried on the row rather than
	// looked up again for the last one so the cursor names the position the
	// scan actually stopped at, out of the same snapshot.
	CreatedAt time.Time
}

func (s *PGXPolicyStore) listWorkspacePolicies(ctx context.Context, cursor domain.Cursor, limit int) (domain.Page[workspacePolicyRow], error) {
	rows, err := s.pool.Query(ctx, workspacePolicySelect,
		nullableCursorTime(cursor), nullableText(cursor.ID), limit+1)
	if err != nil {
		return domain.Page[workspacePolicyRow]{}, fmt.Errorf("list workspace policies: %w", err)
	}
	defer rows.Close()

	items := make([]workspacePolicyRow, 0, limit)
	for rows.Next() {
		var row workspacePolicyRow
		if err := rows.Scan(&row.Workspace.ID, &row.Workspace.Slug, &row.Workspace.Name,
			&row.Workspace.Status, &row.MessageRateLimitPerMinute, &row.MaxUploadBytes,
			&row.CreatedAt); err != nil {
			return domain.Page[workspacePolicyRow]{}, fmt.Errorf("scan workspace policy: %w", err)
		}
		row.CreatedAt = row.CreatedAt.UTC()
		items = append(items, row)
	}
	if err := rows.Err(); err != nil {
		return domain.Page[workspacePolicyRow]{}, fmt.Errorf("read workspace policies: %w", err)
	}
	return paginate(items, limit, func(row workspacePolicyRow) domain.Cursor {
		return domain.Cursor{At: row.CreatedAt, ID: row.Workspace.ID}
	}), nil
}

// ListAntiSpamPolicies returns one page of RF-19 policies.
func (s *PGXPolicyStore) ListAntiSpamPolicies(ctx context.Context, cursor domain.Cursor, limit int) (domain.Page[domain.AntiSpamPolicy], error) {
	if s == nil || s.pool == nil {
		return domain.Page[domain.AntiSpamPolicy]{}, domain.ErrUnavailable
	}
	page, err := s.listWorkspacePolicies(ctx, cursor, domain.ClampPageSize(limit))
	if err != nil {
		return domain.Page[domain.AntiSpamPolicy]{}, err
	}
	items := make([]domain.AntiSpamPolicy, 0, len(page.Items))
	for _, row := range page.Items {
		items = append(items, domain.AntiSpamPolicy{
			Workspace:                 row.Workspace,
			MessageRateLimitPerMinute: row.MessageRateLimitPerMinute,
		})
	}
	return domain.Page[domain.AntiSpamPolicy]{Items: items, NextCursor: page.NextCursor}, nil
}

// ListUploadPolicies returns one page of RF-32 policies.
func (s *PGXPolicyStore) ListUploadPolicies(ctx context.Context, cursor domain.Cursor, limit int) (domain.Page[domain.UploadPolicy], error) {
	if s == nil || s.pool == nil {
		return domain.Page[domain.UploadPolicy]{}, domain.ErrUnavailable
	}
	page, err := s.listWorkspacePolicies(ctx, cursor, domain.ClampPageSize(limit))
	if err != nil {
		return domain.Page[domain.UploadPolicy]{}, err
	}
	items := make([]domain.UploadPolicy, 0, len(page.Items))
	for _, row := range page.Items {
		items = append(items, domain.UploadPolicy{
			Workspace:      row.Workspace,
			MaxUploadBytes: row.MaxUploadBytes,
		})
	}
	return domain.Page[domain.UploadPolicy]{Items: items, NextCursor: page.NextCursor}, nil
}

// updatePolicyStatement writes one policy column and reports the value it
// replaced, in a single statement.
//
// The CTE locks the workspace row and carries the old value out of the same
// snapshot the UPDATE writes into, so the audit diff describes a transition
// that really happened. A read-then-write across two round trips would let a
// concurrent change slip between them and be recorded as this operator's.
//
// The column name is a compile-time constant substituted here, never a value
// from a request: there are exactly two policies and each has its own endpoint,
// so no caller names a column.
func updatePolicyStatement(column string) string {
	return `
	WITH previous AS (
	    SELECT id, ` + column + ` AS value
	    FROM chat.workspaces
	    WHERE id = $1::uuid
	    FOR UPDATE
	)
	UPDATE chat.workspaces AS w
	SET ` + column + ` = $2, updated_at = now()
	FROM previous
	WHERE w.id = previous.id
	RETURNING previous.value, w.` + column + `, w.id::text, w.slug, w.name, w.status`
}

var (
	updateAntiSpamStatement = updatePolicyStatement("message_rate_limit_per_minute")
	updateUploadStatement   = updatePolicyStatement("max_upload_bytes")
)

// UpdateAntiSpamPolicy writes a validated RF-19 limit.
//
// The value arrives already validated against the shared bounds; the column's
// CHECK is the backstop, and a value that reaches it and fails is a bug in the
// caller rather than something to correct here. Nothing is clamped, rounded or
// truncated on this path.
func (s *PGXPolicyStore) UpdateAntiSpamPolicy(ctx context.Context, workspaceID string, value int) (domain.AntiSpamPolicy, domain.PolicyChange, error) {
	if s == nil || s.pool == nil {
		return domain.AntiSpamPolicy{}, domain.PolicyChange{}, domain.ErrUnavailable
	}
	var policy domain.AntiSpamPolicy
	var previous int
	err := s.pool.QueryRow(ctx, updateAntiSpamStatement, workspaceID, value).
		Scan(&previous, &policy.MessageRateLimitPerMinute,
			&policy.Workspace.ID, &policy.Workspace.Slug, &policy.Workspace.Name, &policy.Workspace.Status)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.AntiSpamPolicy{}, domain.PolicyChange{}, domain.ErrNotFound
		}
		return domain.AntiSpamPolicy{}, domain.PolicyChange{}, fmt.Errorf("update anti-spam policy: %w", err)
	}
	return policy, domain.PolicyChange{
		WorkspaceID: policy.Workspace.ID,
		From:        int64(previous),
		To:          int64(policy.MessageRateLimitPerMinute),
	}, nil
}

// UpdateUploadPolicy writes a validated RF-32 limit, in bytes.
func (s *PGXPolicyStore) UpdateUploadPolicy(ctx context.Context, workspaceID string, value int64) (domain.UploadPolicy, domain.PolicyChange, error) {
	if s == nil || s.pool == nil {
		return domain.UploadPolicy{}, domain.PolicyChange{}, domain.ErrUnavailable
	}
	var policy domain.UploadPolicy
	var previous int64
	err := s.pool.QueryRow(ctx, updateUploadStatement, workspaceID, value).
		Scan(&previous, &policy.MaxUploadBytes,
			&policy.Workspace.ID, &policy.Workspace.Slug, &policy.Workspace.Name, &policy.Workspace.Status)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.UploadPolicy{}, domain.PolicyChange{}, domain.ErrNotFound
		}
		return domain.UploadPolicy{}, domain.PolicyChange{}, fmt.Errorf("update upload policy: %w", err)
	}
	return policy, domain.PolicyChange{
		WorkspaceID: policy.Workspace.ID,
		From:        previous,
		To:          policy.MaxUploadBytes,
	}, nil
}
