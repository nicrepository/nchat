package storage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	pgxmock "github.com/pashagolub/pgxmock/v2"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

func channelCategoryCols() []string {
	return []string{"id", "workspace_id", "name", "position", "created_at", "updated_at"}
}

func newCategoryMock(t *testing.T) pgxmock.PgxPoolIface {
	t.Helper()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	t.Cleanup(mock.Close)
	return mock
}

func requireMetExpectations(t *testing.T, mock pgxmock.PgxPoolIface) {
	t.Helper()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXChannelStore_ListChannelCategories_OrdersDeterministically(t *testing.T) {
	mock := newCategoryMock(t)
	now := time.Now()
	// position is the sort key, but it is not unique: the query must also break
	// ties, so equal positions still produce one order for every reader.
	mock.ExpectQuery(`ORDER BY position, lower\(name\), id`).
		WithArgs("ws-1").
		WillReturnRows(pgxmock.NewRows(channelCategoryCols()).
			AddRow("cat-1", "ws-1", "Alfa", 0, now, now).
			AddRow("cat-2", "ws-1", "Beta", 1, now, now))

	categories, err := storage.NewPGXChannelStore(mock).ListChannelCategories(context.Background(), "ws-1")
	if err != nil {
		t.Fatalf("ListChannelCategories: %v", err)
	}
	if len(categories) != 2 || categories[0].ID != "cat-1" || categories[1].ID != "cat-2" {
		t.Fatalf("unexpected categories: %+v", categories)
	}
	requireMetExpectations(t, mock)
}

func TestPGXChannelStore_ListChannelCategories_EmptyIsNeverNil(t *testing.T) {
	mock := newCategoryMock(t)
	mock.ExpectQuery(`FROM chat.channel_categories`).
		WithArgs("ws-1").
		WillReturnRows(pgxmock.NewRows(channelCategoryCols()))

	categories, err := storage.NewPGXChannelStore(mock).ListChannelCategories(context.Background(), "ws-1")
	if err != nil {
		t.Fatalf("ListChannelCategories: %v", err)
	}
	if categories == nil || len(categories) != 0 {
		t.Fatalf("expected an empty non-nil slice, got %#v", categories)
	}
	requireMetExpectations(t, mock)
}

// A row that fails to scan, and a result set that fails mid-iteration, must both
// surface as errors instead of a silently short listing.
func TestPGXChannelStore_ListChannelCategories_RowFailures(t *testing.T) {
	t.Run("scan failure", func(t *testing.T) {
		mock := newCategoryMock(t)
		now := time.Now()
		mock.ExpectQuery(`FROM chat.channel_categories`).
			WithArgs("ws-1").
			WillReturnRows(pgxmock.NewRows(channelCategoryCols()).
				AddRow("cat-1", "ws-1", "Alfa", "not-an-int", now, now))

		if _, err := storage.NewPGXChannelStore(mock).ListChannelCategories(context.Background(), "ws-1"); err == nil {
			t.Fatal("expected a scan error")
		}
		requireMetExpectations(t, mock)
	})

	t.Run("iteration failure", func(t *testing.T) {
		mock := newCategoryMock(t)
		now := time.Now()
		mock.ExpectQuery(`FROM chat.channel_categories`).
			WithArgs("ws-1").
			WillReturnRows(pgxmock.NewRows(channelCategoryCols()).
				AddRow("cat-1", "ws-1", "Alfa", 0, now, now).
				RowError(0, errors.New("boom")))

		if _, err := storage.NewPGXChannelStore(mock).ListChannelCategories(context.Background(), "ws-1"); err == nil {
			t.Fatal("expected an iteration error")
		}
		requireMetExpectations(t, mock)
	})
}

func TestPGXChannelStore_ReorderChannelCategoriesForManager_LockRowFailures(t *testing.T) {
	for _, test := range []struct {
		name string
		rows *pgxmock.Rows
	}{
		{name: "scan failure", rows: pgxmock.NewRows([]string{"id"}).AddRow(42)},
		{name: "iteration failure", rows: pgxmock.NewRows([]string{"id"}).AddRow("cat-1").RowError(0, errors.New("boom"))},
	} {
		t.Run(test.name, func(t *testing.T) {
			mock := newCategoryMock(t)
			mock.ExpectBegin()
			mock.ExpectQuery(`FOR SHARE OF w, wm`).
				WithArgs("ws-1", "user-1").
				WillReturnRows(pgxmock.NewRows([]string{"workspace_id"}).AddRow("ws-1"))
			mock.ExpectQuery(`FOR UPDATE`).WithArgs("ws-1").WillReturnRows(test.rows)
			mock.ExpectRollback()

			_, err := storage.NewPGXChannelStore(mock).ReorderChannelCategoriesForManager(
				context.Background(),
				storage.ReorderChannelCategoriesInput{
					WorkspaceID: "ws-1", CallerID: "user-1", OrderedIDs: []string{"cat-1"},
				},
			)
			if err == nil {
				t.Fatal("expected an error")
			}
			requireMetExpectations(t, mock)
		})
	}
}

func TestPGXChannelStore_ListChannelCategories_DBError(t *testing.T) {
	mock := newCategoryMock(t)
	mock.ExpectQuery(`FROM chat.channel_categories`).
		WithArgs("ws-1").
		WillReturnError(errors.New("boom"))

	_, err := storage.NewPGXChannelStore(mock).ListChannelCategories(context.Background(), "ws-1")
	if err == nil {
		t.Fatal("expected an error")
	}
	requireMetExpectations(t, mock)
}

func TestPGXChannelStore_CountChannelCategories(t *testing.T) {
	mock := newCategoryMock(t)
	mock.ExpectQuery(`SELECT COUNT\(\*\)`).
		WithArgs("ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(7))

	total, err := storage.NewPGXChannelStore(mock).CountChannelCategories(context.Background(), "ws-1")
	if err != nil {
		t.Fatalf("CountChannelCategories: %v", err)
	}
	if total != 7 {
		t.Fatalf("total = %d, want 7", total)
	}
	requireMetExpectations(t, mock)
}

func TestPGXChannelStore_CountChannelCategories_DBError(t *testing.T) {
	mock := newCategoryMock(t)
	mock.ExpectQuery(`SELECT COUNT\(\*\)`).
		WithArgs("ws-1").
		WillReturnError(errors.New("boom"))

	if _, err := storage.NewPGXChannelStore(mock).CountChannelCategories(context.Background(), "ws-1"); err == nil {
		t.Fatal("expected an error")
	}
	requireMetExpectations(t, mock)
}

// The INSERT draws workspace_id from a row-locked authorized context and derives
// position itself, so neither can be dictated by the caller.
func TestPGXChannelStore_CreateChannelCategoryForManager_Success(t *testing.T) {
	mock := newCategoryMock(t)
	now := time.Now()
	mock.ExpectQuery(`INSERT INTO chat.channel_categories`).
		WithArgs("ws-1", "user-1", "Projetos").
		WillReturnRows(pgxmock.NewRows(channelCategoryCols()).
			AddRow("cat-1", "ws-1", "Projetos", 3, now, now))

	category, err := storage.NewPGXChannelStore(mock).CreateChannelCategoryForManager(
		context.Background(),
		storage.CreateChannelCategoryInput{WorkspaceID: "ws-1", CallerID: "user-1", Name: "Projetos"},
	)
	if err != nil {
		t.Fatalf("CreateChannelCategoryForManager: %v", err)
	}
	if category.ID != "cat-1" || category.Name != "Projetos" || category.Position != 3 {
		t.Fatalf("unexpected category: %+v", category)
	}
	requireMetExpectations(t, mock)
}

// No row from the authorized context means the workspace was not active or the
// management role was gone at the moment of the INSERT. Which one stays unsaid.
func TestPGXChannelStore_CreateChannelCategoryForManager_NoAuthorizedContext_IsForbidden(t *testing.T) {
	mock := newCategoryMock(t)
	mock.ExpectQuery(`INSERT INTO chat.channel_categories`).
		WithArgs("ws-1", "user-1", "Projetos").
		WillReturnError(pgx.ErrNoRows)

	_, err := storage.NewPGXChannelStore(mock).CreateChannelCategoryForManager(
		context.Background(),
		storage.CreateChannelCategoryInput{WorkspaceID: "ws-1", CallerID: "user-1", Name: "Projetos"},
	)
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("error = %v, want ErrForbidden", err)
	}
	requireMetExpectations(t, mock)
}

func TestPGXChannelStore_CreateChannelCategoryForManager_NoCaller_NeverQueries(t *testing.T) {
	mock := newCategoryMock(t)

	_, err := storage.NewPGXChannelStore(mock).CreateChannelCategoryForManager(
		context.Background(),
		storage.CreateChannelCategoryInput{WorkspaceID: "ws-1", Name: "Projetos"},
	)
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("error = %v, want ErrForbidden", err)
	}
	requireMetExpectations(t, mock)
}

func TestPGXChannelStore_CreateChannelCategoryForManager_DuplicateName_IsConflict(t *testing.T) {
	mock := newCategoryMock(t)
	mock.ExpectQuery(`INSERT INTO chat.channel_categories`).
		WithArgs("ws-1", "user-1", "Projetos").
		WillReturnError(&pgconn.PgError{
			Code: "23505", ConstraintName: "channel_categories_workspace_name_uidx",
		})

	_, err := storage.NewPGXChannelStore(mock).CreateChannelCategoryForManager(
		context.Background(),
		storage.CreateChannelCategoryInput{WorkspaceID: "ws-1", CallerID: "user-1", Name: "Projetos"},
	)
	if !errors.Is(err, domain.ErrDuplicateChannelCategoryName) {
		t.Fatalf("error = %v, want ErrDuplicateChannelCategoryName", err)
	}
	// A collision must reach the caller as a conflict, never be silenced.
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("error %v must wrap ErrConflict", err)
	}
	requireMetExpectations(t, mock)
}

// The service normalizes first, so a name CHECK firing means the two rules
// drifted. That is invalid input, not a 500 — and the constraint name never
// reaches the caller.
func TestPGXChannelStore_CreateChannelCategoryForManager_NameCheckViolation_IsInvalidInput(t *testing.T) {
	for _, constraint := range []string{
		"channel_categories_name_length_check",
		"channel_categories_name_trimmed_check",
		"channel_categories_name_no_control_check",
		"channel_categories_name_not_reserved_check",
		"channel_categories_position_range_check",
	} {
		t.Run(constraint, func(t *testing.T) {
			mock := newCategoryMock(t)
			mock.ExpectQuery(`INSERT INTO chat.channel_categories`).
				WithArgs("ws-1", "user-1", "Projetos").
				WillReturnError(&pgconn.PgError{Code: "23514", ConstraintName: constraint})

			_, err := storage.NewPGXChannelStore(mock).CreateChannelCategoryForManager(
				context.Background(),
				storage.CreateChannelCategoryInput{WorkspaceID: "ws-1", CallerID: "user-1", Name: "Projetos"},
			)
			if !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("error = %v, want ErrInvalidInput", err)
			}
			requireMetExpectations(t, mock)
		})
	}
}

func TestPGXChannelStore_CreateChannelCategoryForManager_UnknownDBError(t *testing.T) {
	mock := newCategoryMock(t)
	mock.ExpectQuery(`INSERT INTO chat.channel_categories`).
		WithArgs("ws-1", "user-1", "Projetos").
		WillReturnError(errors.New("boom"))

	_, err := storage.NewPGXChannelStore(mock).CreateChannelCategoryForManager(
		context.Background(),
		storage.CreateChannelCategoryInput{WorkspaceID: "ws-1", CallerID: "user-1", Name: "Projetos"},
	)
	if err == nil || errors.Is(err, domain.ErrForbidden) || errors.Is(err, domain.ErrConflict) {
		t.Fatalf("error = %v, want an opaque wrapped error", err)
	}
	requireMetExpectations(t, mock)
}

// The rename is scoped by both category_id and workspace_id and re-derives the
// management role in the same statement.
func TestPGXChannelStore_RenameChannelCategoryForManager_Success(t *testing.T) {
	mock := newCategoryMock(t)
	now := time.Now()
	mock.ExpectQuery(`UPDATE chat.channel_categories`).
		WithArgs("ws-1", "user-1", "cat-1", "Renomeada").
		WillReturnRows(pgxmock.NewRows(channelCategoryCols()).
			AddRow("cat-1", "ws-1", "Renomeada", 2, now, now))

	category, err := storage.NewPGXChannelStore(mock).RenameChannelCategoryForManager(
		context.Background(),
		storage.RenameChannelCategoryInput{
			WorkspaceID: "ws-1", CallerID: "user-1", CategoryID: "cat-1", Name: "Renomeada",
		},
	)
	if err != nil {
		t.Fatalf("RenameChannelCategoryForManager: %v", err)
	}
	if category.Name != "Renomeada" || category.Position != 2 {
		t.Fatalf("unexpected category: %+v", category)
	}
	requireMetExpectations(t, mock)
}

// A category of another workspace matches nothing, and answers exactly as a
// category that does not exist. Nothing in the response distinguishes them.
func TestPGXChannelStore_RenameChannelCategoryForManager_OtherWorkspace_IsNotFound(t *testing.T) {
	mock := newCategoryMock(t)
	mock.ExpectQuery(`UPDATE chat.channel_categories`).
		WithArgs("ws-1", "user-1", "cat-of-ws-2", "Renomeada").
		WillReturnError(pgx.ErrNoRows)

	_, err := storage.NewPGXChannelStore(mock).RenameChannelCategoryForManager(
		context.Background(),
		storage.RenameChannelCategoryInput{
			WorkspaceID: "ws-1", CallerID: "user-1", CategoryID: "cat-of-ws-2", Name: "Renomeada",
		},
	)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
	requireMetExpectations(t, mock)
}

func TestPGXChannelStore_RenameChannelCategoryForManager_DuplicateName_IsConflict(t *testing.T) {
	mock := newCategoryMock(t)
	mock.ExpectQuery(`UPDATE chat.channel_categories`).
		WithArgs("ws-1", "user-1", "cat-1", "Existente").
		WillReturnError(&pgconn.PgError{
			Code: "23505", ConstraintName: "channel_categories_workspace_name_uidx",
		})

	_, err := storage.NewPGXChannelStore(mock).RenameChannelCategoryForManager(
		context.Background(),
		storage.RenameChannelCategoryInput{
			WorkspaceID: "ws-1", CallerID: "user-1", CategoryID: "cat-1", Name: "Existente",
		},
	)
	if !errors.Is(err, domain.ErrDuplicateChannelCategoryName) {
		t.Fatalf("error = %v, want ErrDuplicateChannelCategoryName", err)
	}
	requireMetExpectations(t, mock)
}

func TestPGXChannelStore_RenameChannelCategoryForManager_NoCaller_NeverQueries(t *testing.T) {
	mock := newCategoryMock(t)

	_, err := storage.NewPGXChannelStore(mock).RenameChannelCategoryForManager(
		context.Background(),
		storage.RenameChannelCategoryInput{WorkspaceID: "ws-1", CategoryID: "cat-1", Name: "X"},
	)
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("error = %v, want ErrForbidden", err)
	}
	requireMetExpectations(t, mock)
}

func TestPGXChannelStore_RenameChannelCategoryForManager_UnknownDBError(t *testing.T) {
	mock := newCategoryMock(t)
	mock.ExpectQuery(`UPDATE chat.channel_categories`).
		WithArgs("ws-1", "user-1", "cat-1", "X").
		WillReturnError(errors.New("boom"))

	_, err := storage.NewPGXChannelStore(mock).RenameChannelCategoryForManager(
		context.Background(),
		storage.RenameChannelCategoryInput{
			WorkspaceID: "ws-1", CallerID: "user-1", CategoryID: "cat-1", Name: "X",
		},
	)
	if err == nil || errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want an opaque wrapped error", err)
	}
	requireMetExpectations(t, mock)
}

// The delete is one statement scoped by (id, workspace_id) with the RBAC backstop
// joined in. The channels are preserved by the composite FK's ON DELETE SET NULL,
// which is why no second statement appears here — the PostgreSQL test proves that
// side of it.
func TestPGXChannelStore_DeleteChannelCategoryForManager_Success(t *testing.T) {
	mock := newCategoryMock(t)
	mock.ExpectExec(`DELETE FROM chat.channel_categories`).
		WithArgs("ws-1", "user-1", "cat-1").
		WillReturnResult(pgxmock.NewResult("DELETE", 1))

	err := storage.NewPGXChannelStore(mock).DeleteChannelCategoryForManager(
		context.Background(), "ws-1", "cat-1", "user-1",
	)
	if err != nil {
		t.Fatalf("DeleteChannelCategoryForManager: %v", err)
	}
	requireMetExpectations(t, mock)
}

func TestPGXChannelStore_DeleteChannelCategoryForManager_NoRow_IsNotFound(t *testing.T) {
	mock := newCategoryMock(t)
	mock.ExpectExec(`DELETE FROM chat.channel_categories`).
		WithArgs("ws-1", "user-1", "cat-of-ws-2").
		WillReturnResult(pgxmock.NewResult("DELETE", 0))

	err := storage.NewPGXChannelStore(mock).DeleteChannelCategoryForManager(
		context.Background(), "ws-1", "cat-of-ws-2", "user-1",
	)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
	requireMetExpectations(t, mock)
}

func TestPGXChannelStore_DeleteChannelCategoryForManager_NoCaller_NeverQueries(t *testing.T) {
	mock := newCategoryMock(t)

	err := storage.NewPGXChannelStore(mock).DeleteChannelCategoryForManager(
		context.Background(), "ws-1", "cat-1", "",
	)
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("error = %v, want ErrForbidden", err)
	}
	requireMetExpectations(t, mock)
}

func TestPGXChannelStore_DeleteChannelCategoryForManager_DBError(t *testing.T) {
	mock := newCategoryMock(t)
	mock.ExpectExec(`DELETE FROM chat.channel_categories`).
		WithArgs("ws-1", "user-1", "cat-1").
		WillReturnError(errors.New("boom"))

	err := storage.NewPGXChannelStore(mock).DeleteChannelCategoryForManager(
		context.Background(), "ws-1", "cat-1", "user-1",
	)
	if err == nil || errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want an opaque wrapped error", err)
	}
	requireMetExpectations(t, mock)
}

// The reorder is one transaction: authorize and lock, lock and read the set,
// verify, write, list, commit.
func TestPGXChannelStore_ReorderChannelCategoriesForManager_Success(t *testing.T) {
	mock := newCategoryMock(t)
	now := time.Now()
	mock.ExpectBegin()
	mock.ExpectQuery(`FOR SHARE OF w, wm`).
		WithArgs("ws-1", "user-1").
		WillReturnRows(pgxmock.NewRows([]string{"workspace_id"}).AddRow("ws-1"))
	mock.ExpectQuery(`FOR UPDATE`).
		WithArgs("ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("cat-1").AddRow("cat-2"))
	mock.ExpectExec(`WITH ORDINALITY`).
		WithArgs("ws-1", []string{"cat-2", "cat-1"}).
		WillReturnResult(pgxmock.NewResult("UPDATE", 2))
	mock.ExpectQuery(`ORDER BY position, lower\(name\), id`).
		WithArgs("ws-1").
		WillReturnRows(pgxmock.NewRows(channelCategoryCols()).
			AddRow("cat-2", "ws-1", "Beta", 0, now, now).
			AddRow("cat-1", "ws-1", "Alfa", 1, now, now))
	mock.ExpectCommit()

	categories, err := storage.NewPGXChannelStore(mock).ReorderChannelCategoriesForManager(
		context.Background(),
		storage.ReorderChannelCategoriesInput{
			WorkspaceID: "ws-1", CallerID: "user-1", OrderedIDs: []string{"cat-2", "cat-1"},
		},
	)
	if err != nil {
		t.Fatalf("ReorderChannelCategoriesForManager: %v", err)
	}
	if len(categories) != 2 || categories[0].ID != "cat-2" || categories[1].ID != "cat-1" {
		t.Fatalf("unexpected order: %+v", categories)
	}
	requireMetExpectations(t, mock)
}

func TestPGXChannelStore_ReorderChannelCategoriesForManager_NotAManager_IsForbidden(t *testing.T) {
	mock := newCategoryMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`FOR SHARE OF w, wm`).
		WithArgs("ws-1", "user-1").
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectRollback()

	_, err := storage.NewPGXChannelStore(mock).ReorderChannelCategoriesForManager(
		context.Background(),
		storage.ReorderChannelCategoriesInput{
			WorkspaceID: "ws-1", CallerID: "user-1", OrderedIDs: []string{"cat-1"},
		},
	)
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("error = %v, want ErrForbidden", err)
	}
	requireMetExpectations(t, mock)
}

// A duplicate ID, a missing one, and one from another workspace are all the same
// answer, so the response cannot be used to probe another workspace's categories.
func TestPGXChannelStore_ReorderChannelCategoriesForManager_RejectsSetMismatch(t *testing.T) {
	for _, test := range []struct {
		name       string
		persisted  []string
		orderedIDs []string
	}{
		{name: "duplicate id", persisted: []string{"cat-1", "cat-2"}, orderedIDs: []string{"cat-1", "cat-1"}},
		{name: "missing id", persisted: []string{"cat-1", "cat-2"}, orderedIDs: []string{"cat-1"}},
		{name: "extra id", persisted: []string{"cat-1"}, orderedIDs: []string{"cat-1", "cat-2"}},
		{name: "id from another workspace", persisted: []string{"cat-1", "cat-2"}, orderedIDs: []string{"cat-1", "cat-of-ws-2"}},
		{name: "no categories at all", persisted: nil, orderedIDs: []string{"cat-1"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			mock := newCategoryMock(t)
			mock.ExpectBegin()
			mock.ExpectQuery(`FOR SHARE OF w, wm`).
				WithArgs("ws-1", "user-1").
				WillReturnRows(pgxmock.NewRows([]string{"workspace_id"}).AddRow("ws-1"))
			locked := pgxmock.NewRows([]string{"id"})
			for _, id := range test.persisted {
				locked = locked.AddRow(id)
			}
			mock.ExpectQuery(`FOR UPDATE`).WithArgs("ws-1").WillReturnRows(locked)
			mock.ExpectRollback()

			_, err := storage.NewPGXChannelStore(mock).ReorderChannelCategoriesForManager(
				context.Background(),
				storage.ReorderChannelCategoriesInput{
					WorkspaceID: "ws-1", CallerID: "user-1", OrderedIDs: test.orderedIDs,
				},
			)
			if !errors.Is(err, domain.ErrInvalidChannelCategoryOrder) {
				t.Fatalf("error = %v, want ErrInvalidChannelCategoryOrder", err)
			}
			// Nothing may be written when the set does not match.
			requireMetExpectations(t, mock)
		})
	}
}

// A row count below the set size means a row moved out from under the
// transaction; the reorder is refused rather than committed half-applied.
func TestPGXChannelStore_ReorderChannelCategoriesForManager_PartialUpdateIsRefused(t *testing.T) {
	mock := newCategoryMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`FOR SHARE OF w, wm`).
		WithArgs("ws-1", "user-1").
		WillReturnRows(pgxmock.NewRows([]string{"workspace_id"}).AddRow("ws-1"))
	mock.ExpectQuery(`FOR UPDATE`).
		WithArgs("ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("cat-1").AddRow("cat-2"))
	mock.ExpectExec(`WITH ORDINALITY`).
		WithArgs("ws-1", []string{"cat-1", "cat-2"}).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectRollback()

	_, err := storage.NewPGXChannelStore(mock).ReorderChannelCategoriesForManager(
		context.Background(),
		storage.ReorderChannelCategoriesInput{
			WorkspaceID: "ws-1", CallerID: "user-1", OrderedIDs: []string{"cat-1", "cat-2"},
		},
	)
	if !errors.Is(err, domain.ErrInvalidChannelCategoryOrder) {
		t.Fatalf("error = %v, want ErrInvalidChannelCategoryOrder", err)
	}
	requireMetExpectations(t, mock)
}

func TestPGXChannelStore_ReorderChannelCategoriesForManager_NoCaller_NeverBegins(t *testing.T) {
	mock := newCategoryMock(t)

	_, err := storage.NewPGXChannelStore(mock).ReorderChannelCategoriesForManager(
		context.Background(),
		storage.ReorderChannelCategoriesInput{WorkspaceID: "ws-1", OrderedIDs: []string{"cat-1"}},
	)
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("error = %v, want ErrForbidden", err)
	}
	requireMetExpectations(t, mock)
}

func TestPGXChannelStore_ReorderChannelCategoriesForManager_BeginError(t *testing.T) {
	mock := newCategoryMock(t)
	mock.ExpectBegin().WillReturnError(errors.New("boom"))

	_, err := storage.NewPGXChannelStore(mock).ReorderChannelCategoriesForManager(
		context.Background(),
		storage.ReorderChannelCategoriesInput{
			WorkspaceID: "ws-1", CallerID: "user-1", OrderedIDs: []string{"cat-1"},
		},
	)
	if err == nil || errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("error = %v, want an opaque wrapped error", err)
	}
	requireMetExpectations(t, mock)
}

func TestPGXChannelStore_ReorderChannelCategoriesForManager_FailurePathsRollBack(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(mock pgxmock.PgxPoolIface)
	}{
		{
			name: "authorize query error",
			setup: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery(`FOR SHARE OF w, wm`).WithArgs("ws-1", "user-1").
					WillReturnError(errors.New("boom"))
			},
		},
		{
			name: "lock query error",
			setup: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery(`FOR SHARE OF w, wm`).WithArgs("ws-1", "user-1").
					WillReturnRows(pgxmock.NewRows([]string{"workspace_id"}).AddRow("ws-1"))
				mock.ExpectQuery(`FOR UPDATE`).WithArgs("ws-1").WillReturnError(errors.New("boom"))
			},
		},
		{
			name: "update error",
			setup: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery(`FOR SHARE OF w, wm`).WithArgs("ws-1", "user-1").
					WillReturnRows(pgxmock.NewRows([]string{"workspace_id"}).AddRow("ws-1"))
				mock.ExpectQuery(`FOR UPDATE`).WithArgs("ws-1").
					WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("cat-1"))
				mock.ExpectExec(`WITH ORDINALITY`).WithArgs("ws-1", []string{"cat-1"}).
					WillReturnError(errors.New("boom"))
			},
		},
		{
			name: "final listing error",
			setup: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery(`FOR SHARE OF w, wm`).WithArgs("ws-1", "user-1").
					WillReturnRows(pgxmock.NewRows([]string{"workspace_id"}).AddRow("ws-1"))
				mock.ExpectQuery(`FOR UPDATE`).WithArgs("ws-1").
					WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("cat-1"))
				mock.ExpectExec(`WITH ORDINALITY`).WithArgs("ws-1", []string{"cat-1"}).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				mock.ExpectQuery(`ORDER BY position, lower\(name\), id`).WithArgs("ws-1").
					WillReturnError(errors.New("boom"))
			},
		},
		{
			name: "commit error",
			setup: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery(`FOR SHARE OF w, wm`).WithArgs("ws-1", "user-1").
					WillReturnRows(pgxmock.NewRows([]string{"workspace_id"}).AddRow("ws-1"))
				mock.ExpectQuery(`FOR UPDATE`).WithArgs("ws-1").
					WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("cat-1"))
				mock.ExpectExec(`WITH ORDINALITY`).WithArgs("ws-1", []string{"cat-1"}).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				mock.ExpectQuery(`ORDER BY position, lower\(name\), id`).WithArgs("ws-1").
					WillReturnRows(pgxmock.NewRows(channelCategoryCols()))
				mock.ExpectCommit().WillReturnError(errors.New("boom"))
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			mock := newCategoryMock(t)
			mock.ExpectBegin()
			test.setup(mock)
			mock.ExpectRollback()

			_, err := storage.NewPGXChannelStore(mock).ReorderChannelCategoriesForManager(
				context.Background(),
				storage.ReorderChannelCategoriesInput{
					WorkspaceID: "ws-1", CallerID: "user-1", OrderedIDs: []string{"cat-1"},
				},
			)
			if err == nil {
				t.Fatal("expected an error")
			}
			requireMetExpectations(t, mock)
		})
	}
}
