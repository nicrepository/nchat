package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// CreateChannelCategoryInput and the other *ChannelCategory* inputs all carry
// both the workspace and the acting caller, because every statement below scopes
// itself by workspace_id and re-derives the caller's management role from
// chat.workspace_members inside the same statement. Neither is decoration: a
// mutation that trusted a bare category_id would reach across workspaces, and a
// mutation that trusted the service's earlier role check would still run for a
// caller demoted in between.
type CreateChannelCategoryInput struct {
	WorkspaceID string
	CallerID    string
	Name        string
}

// RenameChannelCategoryInput updates the only mutable field a caller controls.
// Position is never renamed into; it moves through reordering alone.
type RenameChannelCategoryInput struct {
	WorkspaceID string
	CallerID    string
	CategoryID  string
	Name        string
}

// ReorderChannelCategoriesInput carries the workspace's complete category set in
// the desired order. OrderedIDs must be exactly that set — the store verifies it
// against the rows it locked rather than trusting the caller.
type ReorderChannelCategoriesInput struct {
	WorkspaceID string
	CallerID    string
	OrderedIDs  []string
}

// ChannelCategoryStore is the persistence interface for RF-17 channel category
// operations.
//
// It is deliberately separate from ChannelStore rather than an extension of it.
// ChannelStore is implemented by test doubles across two packages, and category
// management has no business forcing them to grow methods they never call. The
// pgx implementation is nonetheless PGXChannelStore, which already owns
// CreateCategory and GetCategoryByIDInWorkspace, so there is one place that
// writes chat.channel_categories.
type ChannelCategoryStore interface {
	// ListChannelCategories returns every category of workspaceID in the
	// deterministic order the API exposes. It performs no authorization: the
	// caller must have established read access, and the listing carries no
	// channel data.
	ListChannelCategories(ctx context.Context, workspaceID string) ([]domain.ChannelCategory, error)
	// CountChannelCategories reports how many categories workspaceID holds, for
	// the per-workspace ceiling.
	CountChannelCategories(ctx context.Context, workspaceID string) (int, error)
	// CreateChannelCategoryForManager appends a category to workspaceID on behalf
	// of an active owner/admin, assigning the next position itself.
	//
	// Returns domain.ErrForbidden — without saying which condition failed — when
	// the workspace is not active or the caller holds no active management role at
	// the moment of the INSERT. Returns
	// domain.ErrDuplicateChannelCategoryName on a case-insensitive name
	// collision within the workspace.
	CreateChannelCategoryForManager(ctx context.Context, input CreateChannelCategoryInput) (domain.ChannelCategory, error)
	// RenameChannelCategoryForManager renames one category of workspaceID.
	// Returns domain.ErrNotFound when no such category exists in that workspace,
	// which is also the answer for a category that exists in another one.
	RenameChannelCategoryForManager(ctx context.Context, input RenameChannelCategoryInput) (domain.ChannelCategory, error)
	// ReorderChannelCategoriesForManager rewrites the positions of every category
	// in workspaceID from OrderedIDs, transactionally, and returns the resulting
	// listing. Returns domain.ErrInvalidChannelCategoryOrder when OrderedIDs is
	// not exactly the workspace's category set.
	ReorderChannelCategoriesForManager(ctx context.Context, input ReorderChannelCategoriesInput) ([]domain.ChannelCategory, error)
	// DeleteChannelCategoryForManager deletes one category of workspaceID. The
	// channels that referenced it are preserved and become uncategorized; see the
	// implementation for why that needs no statement of its own.
	DeleteChannelCategoryForManager(ctx context.Context, workspaceID, categoryID, callerID string) error
}

// channelCategoryColumns is the single projection every category read uses, so
// the scan order below cannot drift between statements.
const channelCategoryColumns = `id, workspace_id, name, position, created_at, updated_at`

// channelCategoryOrder is the deterministic listing order.
//
// position is the explicit sort key, but it is not unique: two categories
// created concurrently can land on the same position, and enforcing uniqueness
// would mean either retry loops on create or temporary offsets on every reorder.
// lower(name) then id break the tie instead, so equal positions still produce one
// stable order for every reader.
const channelCategoryOrder = `ORDER BY position, lower(name), id`

// managerAuthorizedWorkspace is the shared authorization fragment for category
// mutations: the workspace must be active and the caller must hold an active
// management role in it. It mirrors PGXWorkspaceStore.UpdateEditWindow — the
// service checks the same predicate first for a legible error, and this is the
// backstop that decides, serialized with the write.
//
// FOR SHARE rather than FOR UPDATE: revoking a membership or disabling a
// workspace is an UPDATE of a non-key column, which takes FOR NO KEY UPDATE and
// conflicts with FOR SHARE, so either one is serialised against a category
// mutation in flight. Two concurrent category mutations both take FOR SHARE and
// do not block each other, which FOR UPDATE would have made them do for no
// safety gained.
const managerAuthorizedWorkspace = `
	SELECT w.id AS workspace_id
	FROM chat.workspaces w
	JOIN chat.workspace_members wm
	  ON wm.workspace_id = w.id
	WHERE w.id = $1
	  AND w.status = 'active'
	  AND wm.user_id = $2
	  AND wm.status = 'active'
	  AND wm.role IN ('owner', 'admin')
	FOR SHARE OF w, wm`

func (s *PGXChannelStore) ListChannelCategories(ctx context.Context, workspaceID string) ([]domain.ChannelCategory, error) {
	return listChannelCategories(ctx, s.pool, workspaceID)
}

func (s *PGXChannelStore) CountChannelCategories(ctx context.Context, workspaceID string) (int, error) {
	var total int
	err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM chat.channel_categories
		WHERE workspace_id = $1`,
		workspaceID,
	).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("count channel categories: %w", err)
	}
	return total, nil
}

// CreateChannelCategoryForManager inserts from a row-locked authorized context,
// the way CreateChannelForActiveMember does, so the authorization decision and
// the write are one statement with nothing to interleave.
//
// workspace_id comes back out of that context rather than from the parameter, so
// the row can only ever record the workspace the database itself authorized.
//
// position is COALESCE(MAX(position) + 1, 0) over the workspace, computed inside
// the statement: no read-then-write, and an empty workspace starts at 0. Two
// concurrent creates can still compute the same MAX and tie; channelCategoryOrder
// resolves that deterministically, which is cheaper than making callers retry.
func (s *PGXChannelStore) CreateChannelCategoryForManager(ctx context.Context, input CreateChannelCategoryInput) (domain.ChannelCategory, error) {
	if input.CallerID == "" {
		return domain.ChannelCategory{}, domain.ErrForbidden
	}

	var c domain.ChannelCategory
	err := s.pool.QueryRow(ctx, `
		WITH authorized_context AS (`+managerAuthorizedWorkspace+`
		)
		INSERT INTO chat.channel_categories (workspace_id, name, position)
		SELECT
			ac.workspace_id,
			$3,
			COALESCE((
				SELECT MAX(existing.position) + 1
				FROM chat.channel_categories existing
				WHERE existing.workspace_id = ac.workspace_id
			), 0)
		FROM authorized_context ac
		RETURNING `+channelCategoryColumns,
		input.WorkspaceID, input.CallerID, input.Name,
	).Scan(&c.ID, &c.WorkspaceID, &c.Name, &c.Position, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		// No row from the authorized context means no INSERT: the workspace was
		// not active, or the management role was absent or no longer active. Which
		// of them is deliberately not distinguished — the caller must not learn a
		// workspace exists from a failure to write in it.
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ChannelCategory{}, domain.ErrForbidden
		}
		if mapped := mapChannelCategoryWriteError(err); mapped != nil {
			return domain.ChannelCategory{}, mapped
		}
		return domain.ChannelCategory{}, fmt.Errorf("create channel category: %w", err)
	}
	return c, nil
}

// RenameChannelCategoryForManager scopes the UPDATE by both category_id and
// workspace_id and re-derives the management role in the same statement, so a
// bare category ID from another workspace matches nothing.
func (s *PGXChannelStore) RenameChannelCategoryForManager(ctx context.Context, input RenameChannelCategoryInput) (domain.ChannelCategory, error) {
	if input.CallerID == "" {
		return domain.ChannelCategory{}, domain.ErrForbidden
	}

	var c domain.ChannelCategory
	err := s.pool.QueryRow(ctx, `
		UPDATE chat.channel_categories c
		SET name = $4, updated_at = now()
		FROM chat.workspaces w, chat.workspace_members wm
		WHERE c.id = $3
		  AND c.workspace_id = $1
		  AND w.id = c.workspace_id
		  AND w.status = 'active'
		  AND wm.workspace_id = c.workspace_id
		  AND wm.user_id = $2
		  AND wm.status = 'active'
		  AND wm.role IN ('owner', 'admin')
		RETURNING c.id, c.workspace_id, c.name, c.position, c.created_at, c.updated_at`,
		input.WorkspaceID, input.CallerID, input.CategoryID, input.Name,
	).Scan(&c.ID, &c.WorkspaceID, &c.Name, &c.Position, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		// The service has already established the management role, so no row here
		// means the category is not in this workspace. ErrNotFound is also the
		// answer for a category that exists in another workspace, which is what
		// keeps the two indistinguishable to a prober. A caller demoted between
		// the check and the write lands here too; reporting that as "not found"
		// discloses nothing.
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ChannelCategory{}, domain.ErrNotFound
		}
		if mapped := mapChannelCategoryWriteError(err); mapped != nil {
			return domain.ChannelCategory{}, mapped
		}
		return domain.ChannelCategory{}, fmt.Errorf("rename channel category: %w", err)
	}
	return c, nil
}

// ReorderChannelCategoriesForManager rewrites every position in one transaction.
//
// Three steps, in this order for a reason:
//
//  1. Authorize and lock the workspace and the membership. Done as its own
//     statement because a workspace with no categories yet would make an
//     authorization check folded into the category query indistinguishable from
//     "nothing to reorder".
//  2. Lock the workspace's category rows FOR UPDATE and read their IDs. This is
//     what serialises two concurrent reorders, and a concurrent delete or create,
//     against each other: the second one waits, then sees the committed set and
//     either agrees with it or is rejected.
//  3. Compare the locked set with OrderedIDs and, only if they match exactly,
//     write the new positions in a single UPDATE.
//
// The comparison is what rejects a duplicate ID, a missing one, and one from
// another workspace — all three as the same error, so the response cannot be used
// to probe another workspace's categories.
func (s *PGXChannelStore) ReorderChannelCategoriesForManager(ctx context.Context, input ReorderChannelCategoriesInput) ([]domain.ChannelCategory, error) {
	if input.CallerID == "" {
		return nil, domain.ErrForbidden
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin reorder channel categories: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	var authorizedWorkspaceID string
	if err := tx.QueryRow(ctx, managerAuthorizedWorkspace, input.WorkspaceID, input.CallerID).
		Scan(&authorizedWorkspaceID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrForbidden
		}
		return nil, fmt.Errorf("authorize reorder channel categories: %w", err)
	}

	persisted, err := lockChannelCategoryIDs(ctx, tx, authorizedWorkspaceID)
	if err != nil {
		return nil, err
	}
	if !sameChannelCategorySet(persisted, input.OrderedIDs) {
		return nil, domain.ErrInvalidChannelCategoryOrder
	}

	// WITH ORDINALITY numbers from 1; position is zero-based, matching the 0 a
	// first create assigns. Scoped by workspace_id as well as id even though the
	// set was just verified, so the statement is safe read on its own.
	tag, err := tx.Exec(ctx, `
		UPDATE chat.channel_categories c
		SET position = ordered.ordinality - 1, updated_at = now()
		FROM unnest($2::uuid[]) WITH ORDINALITY AS ordered(id, ordinality)
		WHERE c.id = ordered.id
		  AND c.workspace_id = $1`,
		authorizedWorkspaceID, input.OrderedIDs,
	)
	if err != nil {
		return nil, fmt.Errorf("reorder channel categories: %w", err)
	}
	if tag.RowsAffected() != int64(len(input.OrderedIDs)) {
		return nil, domain.ErrInvalidChannelCategoryOrder
	}

	reordered, err := listChannelCategories(ctx, tx, authorizedWorkspaceID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit reorder channel categories: %w", err)
	}
	committed = true
	return reordered, nil
}

// DeleteChannelCategoryForManager deletes the category and nothing else.
//
// The channels that referenced it keep existing and become uncategorized without
// a statement here: chat.channels carries
// channels_workspace_category_fk ... ON DELETE SET NULL (category_id) from
// migration 000002, so PostgreSQL clears the reference as part of this DELETE.
// One statement means the whole operation is atomic by construction, and the
// invariant survives writers that never go through this store.
//
// USING joins the workspace and the membership, so the delete is scoped by
// (id, workspace_id) and re-derives the management role at the same instant.
func (s *PGXChannelStore) DeleteChannelCategoryForManager(ctx context.Context, workspaceID, categoryID, callerID string) error {
	if callerID == "" {
		return domain.ErrForbidden
	}

	tag, err := s.pool.Exec(ctx, `
		DELETE FROM chat.channel_categories c
		USING chat.workspaces w, chat.workspace_members wm
		WHERE c.id = $3
		  AND c.workspace_id = $1
		  AND w.id = c.workspace_id
		  AND w.status = 'active'
		  AND wm.workspace_id = c.workspace_id
		  AND wm.user_id = $2
		  AND wm.status = 'active'
		  AND wm.role IN ('owner', 'admin')`,
		workspaceID, callerID, categoryID,
	)
	if err != nil {
		return fmt.Errorf("delete channel category: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Same reasoning as the rename: the role was already established, so this
		// is a category that is not in this workspace, and a category in another
		// workspace answers identically.
		return domain.ErrNotFound
	}
	return nil
}

// channelCategoryQuerier is the read surface shared by the pool and a
// transaction, so the listing runs identically inside and outside a reorder.
type channelCategoryQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func listChannelCategories(ctx context.Context, q channelCategoryQuerier, workspaceID string) ([]domain.ChannelCategory, error) {
	rows, err := q.Query(ctx, `
		SELECT `+channelCategoryColumns+`
		FROM chat.channel_categories
		WHERE workspace_id = $1
		`+channelCategoryOrder,
		workspaceID,
	)
	if err != nil {
		return nil, fmt.Errorf("list channel categories: %w", err)
	}
	defer rows.Close()

	categories := make([]domain.ChannelCategory, 0)
	for rows.Next() {
		var c domain.ChannelCategory
		if err := rows.Scan(&c.ID, &c.WorkspaceID, &c.Name, &c.Position, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan channel category: %w", err)
		}
		categories = append(categories, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list channel categories: %w", err)
	}
	return categories, nil
}

func lockChannelCategoryIDs(ctx context.Context, q channelCategoryQuerier, workspaceID string) ([]string, error) {
	// ORDER BY id gives every reorder the same lock acquisition order, so two of
	// them queue instead of deadlocking on interleaved rows.
	rows, err := q.Query(ctx, `
		SELECT id
		FROM chat.channel_categories
		WHERE workspace_id = $1
		ORDER BY id
		FOR UPDATE`,
		workspaceID,
	)
	if err != nil {
		return nil, fmt.Errorf("lock channel categories: %w", err)
	}
	defer rows.Close()

	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan locked channel category: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("lock channel categories: %w", err)
	}
	return ids, nil
}

// sameChannelCategorySet reports whether requested is a permutation of
// persisted: same length, no duplicates, no member outside persisted. Order is
// irrelevant here — order is the point of the caller, not of the set.
func sameChannelCategorySet(persisted, requested []string) bool {
	if len(persisted) != len(requested) {
		return false
	}
	remaining := make(map[string]struct{}, len(persisted))
	for _, id := range persisted {
		remaining[id] = struct{}{}
	}
	for _, id := range requested {
		if _, ok := remaining[id]; !ok {
			// Either not in this workspace, or already consumed by an earlier
			// occurrence of the same ID in requested.
			return false
		}
		delete(remaining, id)
	}
	return true
}

func mapChannelCategoryWriteError(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return nil
	}
	switch pgErr.ConstraintName {
	case "channel_categories_workspace_name_uidx":
		return domain.ErrDuplicateChannelCategoryName
	case "channel_categories_name_length_check",
		"channel_categories_name_trimmed_check",
		"channel_categories_name_no_control_check",
		"channel_categories_name_not_reserved_check",
		"channel_categories_position_range_check":
		// The service normalizes before writing, so reaching one of these means a
		// rule drifted between the two. Reported as invalid input rather than a
		// 500, and without echoing the value or the constraint name.
		return domain.ErrInvalidInput
	}
	return nil
}
