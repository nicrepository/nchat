package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// ── fakes ─────────────────────────────────────────────────────────────────────

// fakeCategoryStore records what reached storage. It does not re-implement the
// SQL-level authorization backstop: that is proved in the storage package against
// pgxmock and PostgreSQL. What matters here is that the service refuses before
// storage is touched at all, so every test asserts the call count too.
type fakeCategoryStore struct {
	categories []domain.ChannelCategory
	total      int
	listErr    error
	countErr   error
	createErr  error
	renameErr  error
	reorderErr error
	deleteErr  error

	created       []storage.CreateChannelCategoryInput
	renamed       []storage.RenameChannelCategoryInput
	reordered     []storage.ReorderChannelCategoriesInput
	deleted       []string
	lastDeleteWS  string
	lastDeleteWho string
	listCalls     int
}

func (f *fakeCategoryStore) ListChannelCategories(_ context.Context, _ string) ([]domain.ChannelCategory, error) {
	f.listCalls++
	return f.categories, f.listErr
}

func (f *fakeCategoryStore) CountChannelCategories(_ context.Context, _ string) (int, error) {
	return f.total, f.countErr
}

func (f *fakeCategoryStore) CreateChannelCategoryForManager(_ context.Context, input storage.CreateChannelCategoryInput) (domain.ChannelCategory, error) {
	f.created = append(f.created, input)
	if f.createErr != nil {
		return domain.ChannelCategory{}, f.createErr
	}
	return domain.ChannelCategory{
		ID: "cat-new", WorkspaceID: input.WorkspaceID, Name: input.Name, Position: f.total,
	}, nil
}

func (f *fakeCategoryStore) RenameChannelCategoryForManager(_ context.Context, input storage.RenameChannelCategoryInput) (domain.ChannelCategory, error) {
	f.renamed = append(f.renamed, input)
	if f.renameErr != nil {
		return domain.ChannelCategory{}, f.renameErr
	}
	return domain.ChannelCategory{
		ID: input.CategoryID, WorkspaceID: input.WorkspaceID, Name: input.Name,
	}, nil
}

func (f *fakeCategoryStore) ReorderChannelCategoriesForManager(_ context.Context, input storage.ReorderChannelCategoriesInput) ([]domain.ChannelCategory, error) {
	f.reordered = append(f.reordered, input)
	if f.reorderErr != nil {
		return nil, f.reorderErr
	}
	out := make([]domain.ChannelCategory, 0, len(input.OrderedIDs))
	for i, id := range input.OrderedIDs {
		out = append(out, domain.ChannelCategory{ID: id, WorkspaceID: input.WorkspaceID, Position: i})
	}
	return out, nil
}

func (f *fakeCategoryStore) DeleteChannelCategoryForManager(_ context.Context, workspaceID, categoryID, callerID string) error {
	f.deleted = append(f.deleted, categoryID)
	f.lastDeleteWS = workspaceID
	f.lastDeleteWho = callerID
	return f.deleteErr
}

// fakeVisibleChannelStore stands in for the existing channel read policy. It
// returns whatever the SQL policy would already have returned, so the grouping
// under test never sees a channel the caller cannot list.
type fakeVisibleChannelStore struct {
	accesses  []storage.VisibleChannelAccess
	err       error
	callCount int
}

func (f *fakeVisibleChannelStore) ListVisibleChannelAccessByUser(_ context.Context, _, _ string) ([]storage.VisibleChannelAccess, error) {
	f.callCount++
	return f.accesses, f.err
}

// ── helpers ───────────────────────────────────────────────────────────────────

const (
	categoryWorkspaceID = "ws-cat-1"
	categoryAdminID     = "user-admin"
	categoryOwnerID     = "user-owner"
	categoryMemberID    = "user-member"
	categoryGuestID     = "user-guest"
	categorySuspendedID = "user-suspended"
	otherWorkspaceID    = "ws-cat-2"
)

func categoryWorkspaceStore() *fakeWorkspaceStore {
	return &fakeWorkspaceStore{
		workspaces: map[string]domain.Workspace{
			categoryWorkspaceID: {ID: categoryWorkspaceID, Slug: "default", Status: domain.WorkspaceStatusActive},
			otherWorkspaceID:    {ID: otherWorkspaceID, Slug: "other", Status: domain.WorkspaceStatusActive},
		},
	}
}

func categoryMemberStore() *fakeMemberStore {
	member := func(userID string, role domain.WorkspaceRole, status domain.MemberStatus) domain.WorkspaceMember {
		return domain.WorkspaceMember{WorkspaceID: categoryWorkspaceID, UserID: userID, Role: role, Status: status}
	}
	return &fakeMemberStore{
		workspaceMembers: map[string]domain.WorkspaceMember{
			wmKey(categoryWorkspaceID, categoryOwnerID):     member(categoryOwnerID, domain.WorkspaceRoleOwner, domain.MemberStatusActive),
			wmKey(categoryWorkspaceID, categoryAdminID):     member(categoryAdminID, domain.WorkspaceRoleAdmin, domain.MemberStatusActive),
			wmKey(categoryWorkspaceID, categoryMemberID):    member(categoryMemberID, domain.WorkspaceRoleMember, domain.MemberStatusActive),
			wmKey(categoryWorkspaceID, categoryGuestID):     member(categoryGuestID, domain.WorkspaceRoleGuest, domain.MemberStatusActive),
			wmKey(categoryWorkspaceID, categorySuspendedID): member(categorySuspendedID, domain.WorkspaceRoleAdmin, domain.MemberStatusSuspended),
		},
	}
}

func newCategoryService(categories *fakeCategoryStore, channels *fakeVisibleChannelStore) *service.ChannelCategoryService {
	return service.NewChannelCategoryService(categoryWorkspaceStore(), categoryMemberStore(), categories, channels)
}

func visibleChannel(id, slug string, categoryID string, channelType domain.ChannelType, member *domain.ChannelMember) storage.VisibleChannelAccess {
	return storage.VisibleChannelAccess{
		Channel: domain.Channel{
			ID: id, WorkspaceID: categoryWorkspaceID, CategoryID: categoryID, Slug: slug,
			DisplayName: slug, Type: channelType, Status: domain.ChannelStatusActive,
		},
		ChannelMember: member,
	}
}

// ── authorization ─────────────────────────────────────────────────────────────

// Every mutation takes the same gate. A plain member, a guest, a suspended admin
// and a non-member are all refused, and storage is never reached.
func TestChannelCategoryService_MutationsRequireWorkspaceManagement(t *testing.T) {
	for _, caller := range []struct {
		name   string
		userID string
	}{
		{name: "plain member", userID: categoryMemberID},
		{name: "guest", userID: categoryGuestID},
		{name: "suspended admin", userID: categorySuspendedID},
		{name: "not a member", userID: "user-stranger"},
		{name: "no caller", userID: ""},
	} {
		t.Run(caller.name, func(t *testing.T) {
			for _, operation := range []struct {
				name string
				call func(svc *service.ChannelCategoryService) error
			}{
				{name: "create", call: func(svc *service.ChannelCategoryService) error {
					_, err := svc.CreateChannelCategory(context.Background(), service.CreateChannelCategoryInput{
						WorkspaceID: categoryWorkspaceID, CallerID: caller.userID, Name: "Projetos",
					})
					return err
				}},
				{name: "rename", call: func(svc *service.ChannelCategoryService) error {
					_, err := svc.RenameChannelCategory(context.Background(), service.RenameChannelCategoryInput{
						WorkspaceID: categoryWorkspaceID, CallerID: caller.userID, CategoryID: "cat-1", Name: "Projetos",
					})
					return err
				}},
				{name: "reorder", call: func(svc *service.ChannelCategoryService) error {
					_, err := svc.ReorderChannelCategories(context.Background(), service.ReorderChannelCategoriesInput{
						WorkspaceID: categoryWorkspaceID, CallerID: caller.userID, OrderedIDs: []string{"cat-1"},
					})
					return err
				}},
				{name: "delete", call: func(svc *service.ChannelCategoryService) error {
					return svc.DeleteChannelCategory(context.Background(), categoryWorkspaceID, "cat-1", caller.userID)
				}},
			} {
				t.Run(operation.name, func(t *testing.T) {
					categories := &fakeCategoryStore{}
					err := operation.call(newCategoryService(categories, &fakeVisibleChannelStore{}))
					if !errors.Is(err, domain.ErrForbidden) {
						t.Fatalf("error = %v, want ErrForbidden", err)
					}
					if len(categories.created)+len(categories.renamed)+len(categories.reordered)+len(categories.deleted) != 0 {
						t.Fatal("a denied caller must not reach storage")
					}
				})
			}
		})
	}
}

func TestChannelCategoryService_ManagementRolesMayMutate(t *testing.T) {
	for _, userID := range []string{categoryOwnerID, categoryAdminID} {
		t.Run(userID, func(t *testing.T) {
			categories := &fakeCategoryStore{
				categories: []domain.ChannelCategory{{ID: "cat-1", WorkspaceID: categoryWorkspaceID, Name: "Alfa"}},
			}
			svc := newCategoryService(categories, &fakeVisibleChannelStore{})

			if _, err := svc.CreateChannelCategory(context.Background(), service.CreateChannelCategoryInput{
				WorkspaceID: categoryWorkspaceID, CallerID: userID, Name: "Projetos",
			}); err != nil {
				t.Fatalf("create: %v", err)
			}
			if _, err := svc.RenameChannelCategory(context.Background(), service.RenameChannelCategoryInput{
				WorkspaceID: categoryWorkspaceID, CallerID: userID, CategoryID: "cat-1", Name: "Renomeada",
			}); err != nil {
				t.Fatalf("rename: %v", err)
			}
			if _, err := svc.ReorderChannelCategories(context.Background(), service.ReorderChannelCategoriesInput{
				WorkspaceID: categoryWorkspaceID, CallerID: userID, OrderedIDs: []string{"cat-1"},
			}); err != nil {
				t.Fatalf("reorder: %v", err)
			}
			if err := svc.DeleteChannelCategory(context.Background(), categoryWorkspaceID, "cat-1", userID); err != nil {
				t.Fatalf("delete: %v", err)
			}
		})
	}
}

// A workspace an admin is not a member of denies as if it did not exist, so an
// operation cannot be aimed at another workspace's categories.
func TestChannelCategoryService_OtherWorkspaceIsForbidden(t *testing.T) {
	categories := &fakeCategoryStore{}
	svc := newCategoryService(categories, &fakeVisibleChannelStore{})

	_, err := svc.CreateChannelCategory(context.Background(), service.CreateChannelCategoryInput{
		WorkspaceID: otherWorkspaceID, CallerID: categoryAdminID, Name: "Projetos",
	})
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("error = %v, want ErrForbidden", err)
	}
	if _, err := svc.ListGroupedChannels(context.Background(), otherWorkspaceID, categoryAdminID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("list error = %v, want ErrForbidden", err)
	}
	if len(categories.created) != 0 || categories.listCalls != 0 {
		t.Fatal("another workspace must not be reached")
	}
}

// Reading is open to any active member, including a guest: the categories carry
// no channel the caller could not already list.
func TestChannelCategoryService_ListIsOpenToEveryActiveMember(t *testing.T) {
	for _, userID := range []string{categoryOwnerID, categoryAdminID, categoryMemberID, categoryGuestID} {
		t.Run(userID, func(t *testing.T) {
			if _, err := newCategoryService(&fakeCategoryStore{}, &fakeVisibleChannelStore{}).
				ListGroupedChannels(context.Background(), categoryWorkspaceID, userID); err != nil {
				t.Fatalf("ListGroupedChannels: %v", err)
			}
		})
	}
}

// CanManageCategories is the same predicate the write endpoints enforce,
// exposed as a read so a caller (e.g. the "criar canal" category picker) can
// decide what to show without duplicating the role check.
func TestChannelCategoryService_CanManageCategories(t *testing.T) {
	for _, test := range []struct {
		userID string
		want   bool
	}{
		{userID: categoryOwnerID, want: true},
		{userID: categoryAdminID, want: true},
		{userID: categoryMemberID, want: false},
		{userID: categoryGuestID, want: false},
	} {
		t.Run(test.userID, func(t *testing.T) {
			got, err := newCategoryService(&fakeCategoryStore{}, &fakeVisibleChannelStore{}).
				CanManageCategories(context.Background(), categoryWorkspaceID, test.userID)
			if err != nil {
				t.Fatalf("CanManageCategories: %v", err)
			}
			if got != test.want {
				t.Fatalf("CanManageCategories(%s) = %v, want %v", test.userID, got, test.want)
			}
		})
	}
}

func TestChannelCategoryService_CanManageCategoriesDeniesSuspendedAndNonMembers(t *testing.T) {
	for _, userID := range []string{categorySuspendedID, "user-stranger", ""} {
		if _, err := newCategoryService(&fakeCategoryStore{}, &fakeVisibleChannelStore{}).
			CanManageCategories(context.Background(), categoryWorkspaceID, userID); !errors.Is(err, domain.ErrForbidden) {
			t.Fatalf("%q: error = %v, want ErrForbidden", userID, err)
		}
	}
}

func TestChannelCategoryService_ListDeniesSuspendedAndNonMembers(t *testing.T) {
	for _, userID := range []string{categorySuspendedID, "user-stranger", ""} {
		channels := &fakeVisibleChannelStore{}
		_, err := newCategoryService(&fakeCategoryStore{}, channels).
			ListGroupedChannels(context.Background(), categoryWorkspaceID, userID)
		if !errors.Is(err, domain.ErrForbidden) {
			t.Fatalf("%q: error = %v, want ErrForbidden", userID, err)
		}
		if channels.callCount != 0 {
			t.Fatalf("%q: a denied caller must not reach the channel listing", userID)
		}
	}
}

// ── validation ────────────────────────────────────────────────────────────────

func TestChannelCategoryService_CreateNormalizesAndValidatesTheName(t *testing.T) {
	t.Run("trims before storing", func(t *testing.T) {
		categories := &fakeCategoryStore{}
		category, err := newCategoryService(categories, &fakeVisibleChannelStore{}).
			CreateChannelCategory(context.Background(), service.CreateChannelCategoryInput{
				WorkspaceID: categoryWorkspaceID, CallerID: categoryAdminID, Name: "   Projetos   ",
			})
		if err != nil {
			t.Fatalf("CreateChannelCategory: %v", err)
		}
		if category.Name != "Projetos" {
			t.Fatalf("stored name = %q, want %q", category.Name, "Projetos")
		}
		if len(categories.created) != 1 || categories.created[0].Name != "Projetos" {
			t.Fatalf("storage received %+v", categories.created)
		}
		// The workspace and the caller must be the ones the service was given, never
		// anything a payload could carry.
		if categories.created[0].WorkspaceID != categoryWorkspaceID || categories.created[0].CallerID != categoryAdminID {
			t.Fatalf("storage input = %+v", categories.created[0])
		}
	})

	for _, test := range []struct {
		name  string
		input string
		want  error
	}{
		{name: "empty", input: "", want: domain.ErrChannelCategoryNameRequired},
		{name: "whitespace only", input: "   ", want: domain.ErrChannelCategoryNameRequired},
		{name: "control character", input: "Proj\netos", want: domain.ErrChannelCategoryNameInvalid},
		{name: "reserved name", input: "Geral", want: domain.ErrChannelCategoryNameReserved},
		{name: "reserved name in another case", input: "gErAl", want: domain.ErrChannelCategoryNameReserved},
	} {
		t.Run("rejects "+test.name, func(t *testing.T) {
			categories := &fakeCategoryStore{}
			_, err := newCategoryService(categories, &fakeVisibleChannelStore{}).
				CreateChannelCategory(context.Background(), service.CreateChannelCategoryInput{
					WorkspaceID: categoryWorkspaceID, CallerID: categoryAdminID, Name: test.input,
				})
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if len(categories.created) != 0 {
				t.Fatal("an invalid name must not reach storage")
			}
		})
	}
}

// A rename must not be the way past a rule creation enforces.
func TestChannelCategoryService_RenameAppliesTheSameNameRules(t *testing.T) {
	for _, test := range []struct {
		name  string
		input string
		want  error
	}{
		{name: "empty", input: "", want: domain.ErrChannelCategoryNameRequired},
		{name: "control character", input: "Proj\tetos", want: domain.ErrChannelCategoryNameInvalid},
		{name: "reserved name", input: " geral ", want: domain.ErrChannelCategoryNameReserved},
	} {
		t.Run("rejects "+test.name, func(t *testing.T) {
			categories := &fakeCategoryStore{}
			_, err := newCategoryService(categories, &fakeVisibleChannelStore{}).
				RenameChannelCategory(context.Background(), service.RenameChannelCategoryInput{
					WorkspaceID: categoryWorkspaceID, CallerID: categoryAdminID, CategoryID: "cat-1", Name: test.input,
				})
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if len(categories.renamed) != 0 {
				t.Fatal("an invalid name must not reach storage")
			}
		})
	}

	categories := &fakeCategoryStore{}
	category, err := newCategoryService(categories, &fakeVisibleChannelStore{}).
		RenameChannelCategory(context.Background(), service.RenameChannelCategoryInput{
			WorkspaceID: categoryWorkspaceID, CallerID: categoryAdminID, CategoryID: "cat-1", Name: "  Renomeada  ",
		})
	if err != nil {
		t.Fatalf("RenameChannelCategory: %v", err)
	}
	if category.Name != "Renomeada" {
		t.Fatalf("stored name = %q, want %q", category.Name, "Renomeada")
	}
}

func TestChannelCategoryService_CreateEnforcesThePerWorkspaceCeiling(t *testing.T) {
	categories := &fakeCategoryStore{total: domain.MaxCategoriesPerWorkspace}
	_, err := newCategoryService(categories, &fakeVisibleChannelStore{}).
		CreateChannelCategory(context.Background(), service.CreateChannelCategoryInput{
			WorkspaceID: categoryWorkspaceID, CallerID: categoryAdminID, Name: "Uma Mais",
		})
	if !errors.Is(err, domain.ErrChannelCategoryLimitReached) {
		t.Fatalf("error = %v, want ErrChannelCategoryLimitReached", err)
	}
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("error %v must wrap ErrConflict", err)
	}
	if len(categories.created) != 0 {
		t.Fatal("the ceiling must be enforced before the insert")
	}

	// One below the ceiling still goes through.
	categories = &fakeCategoryStore{total: domain.MaxCategoriesPerWorkspace - 1}
	if _, err := newCategoryService(categories, &fakeVisibleChannelStore{}).
		CreateChannelCategory(context.Background(), service.CreateChannelCategoryInput{
			WorkspaceID: categoryWorkspaceID, CallerID: categoryAdminID, Name: "A Ultima",
		}); err != nil {
		t.Fatalf("one below the ceiling must be accepted: %v", err)
	}
}

func TestChannelCategoryService_CreatePropagatesStorageFailures(t *testing.T) {
	t.Run("duplicate name", func(t *testing.T) {
		categories := &fakeCategoryStore{createErr: domain.ErrDuplicateChannelCategoryName}
		_, err := newCategoryService(categories, &fakeVisibleChannelStore{}).
			CreateChannelCategory(context.Background(), service.CreateChannelCategoryInput{
				WorkspaceID: categoryWorkspaceID, CallerID: categoryAdminID, Name: "Projetos",
			})
		if !errors.Is(err, domain.ErrDuplicateChannelCategoryName) {
			t.Fatalf("error = %v, want ErrDuplicateChannelCategoryName", err)
		}
	})
	t.Run("count failure", func(t *testing.T) {
		categories := &fakeCategoryStore{countErr: errors.New("boom")}
		_, err := newCategoryService(categories, &fakeVisibleChannelStore{}).
			CreateChannelCategory(context.Background(), service.CreateChannelCategoryInput{
				WorkspaceID: categoryWorkspaceID, CallerID: categoryAdminID, Name: "Projetos",
			})
		if err == nil || errors.Is(err, domain.ErrConflict) {
			t.Fatalf("error = %v, want an opaque wrapped error", err)
		}
		if len(categories.created) != 0 {
			t.Fatal("a failed count must not be followed by an insert")
		}
	})
}

func TestChannelCategoryService_RenamePropagatesNotFound(t *testing.T) {
	categories := &fakeCategoryStore{renameErr: domain.ErrNotFound}
	_, err := newCategoryService(categories, &fakeVisibleChannelStore{}).
		RenameChannelCategory(context.Background(), service.RenameChannelCategoryInput{
			WorkspaceID: categoryWorkspaceID, CallerID: categoryAdminID, CategoryID: "cat-of-ws-2", Name: "Projetos",
		})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

// ── reordering ────────────────────────────────────────────────────────────────

func TestChannelCategoryService_ReorderBoundsThePayload(t *testing.T) {
	tooMany := make([]string, domain.MaxCategoriesPerWorkspace+1)
	for i := range tooMany {
		tooMany[i] = "cat"
	}
	for _, test := range []struct {
		name       string
		orderedIDs []string
	}{
		{name: "empty", orderedIDs: nil},
		{name: "over the ceiling", orderedIDs: tooMany},
	} {
		t.Run("rejects "+test.name, func(t *testing.T) {
			categories := &fakeCategoryStore{}
			_, err := newCategoryService(categories, &fakeVisibleChannelStore{}).
				ReorderChannelCategories(context.Background(), service.ReorderChannelCategoriesInput{
					WorkspaceID: categoryWorkspaceID, CallerID: categoryAdminID, OrderedIDs: test.orderedIDs,
				})
			if !errors.Is(err, domain.ErrInvalidChannelCategoryOrder) {
				t.Fatalf("error = %v, want ErrInvalidChannelCategoryOrder", err)
			}
			if !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("error %v must wrap ErrInvalidInput", err)
			}
			if len(categories.reordered) != 0 {
				t.Fatal("an out-of-bounds payload must not reach storage")
			}
		})
	}
}

func TestChannelCategoryService_ReorderPassesTheOrderThroughAndReturnsIt(t *testing.T) {
	categories := &fakeCategoryStore{}
	reordered, err := newCategoryService(categories, &fakeVisibleChannelStore{}).
		ReorderChannelCategories(context.Background(), service.ReorderChannelCategoriesInput{
			WorkspaceID: categoryWorkspaceID, CallerID: categoryAdminID,
			OrderedIDs: []string{"cat-3", "cat-1", "cat-2"},
		})
	if err != nil {
		t.Fatalf("ReorderChannelCategories: %v", err)
	}
	if len(categories.reordered) != 1 {
		t.Fatalf("storage received %d reorders", len(categories.reordered))
	}
	if categories.reordered[0].WorkspaceID != categoryWorkspaceID || categories.reordered[0].CallerID != categoryAdminID {
		t.Fatalf("storage input = %+v", categories.reordered[0])
	}
	for i, want := range []string{"cat-3", "cat-1", "cat-2"} {
		if reordered[i].ID != want || reordered[i].Position != i {
			t.Fatalf("position %d = %+v, want %q at %d", i, reordered[i], want, i)
		}
	}
}

// The set validation is the store's, and its verdict must reach the caller as a
// validation error rather than a 500.
func TestChannelCategoryService_ReorderPropagatesSetMismatch(t *testing.T) {
	categories := &fakeCategoryStore{reorderErr: domain.ErrInvalidChannelCategoryOrder}
	_, err := newCategoryService(categories, &fakeVisibleChannelStore{}).
		ReorderChannelCategories(context.Background(), service.ReorderChannelCategoriesInput{
			WorkspaceID: categoryWorkspaceID, CallerID: categoryAdminID, OrderedIDs: []string{"cat-1", "cat-1"},
		})
	if !errors.Is(err, domain.ErrInvalidChannelCategoryOrder) {
		t.Fatalf("error = %v, want ErrInvalidChannelCategoryOrder", err)
	}
}

// ── deletion ──────────────────────────────────────────────────────────────────

func TestChannelCategoryService_DeleteScopesByWorkspaceAndCaller(t *testing.T) {
	categories := &fakeCategoryStore{}
	if err := newCategoryService(categories, &fakeVisibleChannelStore{}).
		DeleteChannelCategory(context.Background(), categoryWorkspaceID, "cat-1", categoryAdminID); err != nil {
		t.Fatalf("DeleteChannelCategory: %v", err)
	}
	if len(categories.deleted) != 1 || categories.deleted[0] != "cat-1" {
		t.Fatalf("deleted %+v", categories.deleted)
	}
	// Never a bare category ID: the workspace and the caller travel with it.
	if categories.lastDeleteWS != categoryWorkspaceID || categories.lastDeleteWho != categoryAdminID {
		t.Fatalf("delete scope = %q/%q", categories.lastDeleteWS, categories.lastDeleteWho)
	}
}

// Deleting something that is not there is an error, not a silent success: this
// service has no idempotent deletes.
func TestChannelCategoryService_DeleteMissingCategoryIsNotFound(t *testing.T) {
	categories := &fakeCategoryStore{deleteErr: domain.ErrNotFound}
	err := newCategoryService(categories, &fakeVisibleChannelStore{}).
		DeleteChannelCategory(context.Background(), categoryWorkspaceID, "cat-gone", categoryAdminID)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

// ── grouped listing ───────────────────────────────────────────────────────────

func TestChannelCategoryService_ListGroupsChannelsUnderTheirCategory(t *testing.T) {
	categories := &fakeCategoryStore{categories: []domain.ChannelCategory{
		{ID: "cat-1", WorkspaceID: categoryWorkspaceID, Name: "Alfa", Position: 0},
		{ID: "cat-2", WorkspaceID: categoryWorkspaceID, Name: "Beta", Position: 1},
	}}
	channels := &fakeVisibleChannelStore{accesses: []storage.VisibleChannelAccess{
		visibleChannel("ch-geral", "geral", "", domain.ChannelTypePublic, nil),
		visibleChannel("ch-a", "alfa-um", "cat-1", domain.ChannelTypePublic, nil),
		visibleChannel("ch-b", "beta-um", "cat-2", domain.ChannelTypePublic, nil),
		visibleChannel("ch-a2", "alfa-dois", "cat-1", domain.ChannelTypePublic, nil),
	}}

	groups, err := newCategoryService(categories, channels).
		ListGroupedChannels(context.Background(), categoryWorkspaceID, categoryMemberID)
	if err != nil {
		t.Fatalf("ListGroupedChannels: %v", err)
	}
	if len(groups) != 3 {
		t.Fatalf("got %d groups, want 3 (virtual + 2 categories)", len(groups))
	}

	// The virtual group is first, always present, and is the one with no category.
	if groups[0].Category != nil {
		t.Fatalf("the first group must be the virtual one, got %+v", groups[0].Category)
	}
	if len(groups[0].Channels) != 1 || groups[0].Channels[0].Channel.ID != "ch-geral" {
		t.Fatalf("virtual group = %+v", groups[0].Channels)
	}
	if groups[1].Category == nil || groups[1].Category.ID != "cat-1" {
		t.Fatalf("second group = %+v", groups[1].Category)
	}
	if len(groups[1].Channels) != 2 {
		t.Fatalf("cat-1 holds %d channels, want 2", len(groups[1].Channels))
	}
	if groups[2].Category == nil || groups[2].Category.ID != "cat-2" {
		t.Fatalf("third group = %+v", groups[2].Category)
	}
	if len(groups[2].Channels) != 1 || groups[2].Channels[0].Channel.ID != "ch-b" {
		t.Fatalf("cat-2 = %+v", groups[2].Channels)
	}
}

// The categories arrive already ordered by the store; the grouping must not
// reshuffle them.
func TestChannelCategoryService_ListPreservesTheStoreOrder(t *testing.T) {
	categories := &fakeCategoryStore{categories: []domain.ChannelCategory{
		{ID: "cat-3", Name: "Zeta", Position: 0},
		{ID: "cat-1", Name: "Alfa", Position: 1},
		{ID: "cat-2", Name: "Beta", Position: 2},
	}}
	groups, err := newCategoryService(categories, &fakeVisibleChannelStore{}).
		ListGroupedChannels(context.Background(), categoryWorkspaceID, categoryMemberID)
	if err != nil {
		t.Fatalf("ListGroupedChannels: %v", err)
	}
	for i, want := range []string{"cat-3", "cat-1", "cat-2"} {
		if groups[i+1].Category.ID != want {
			t.Fatalf("group %d = %q, want %q", i+1, groups[i+1].Category.ID, want)
		}
	}
}

// The virtual group is present even with no uncategorized channel and no
// category, so the response shape never varies.
func TestChannelCategoryService_ListAlwaysReturnsTheVirtualGroup(t *testing.T) {
	groups, err := newCategoryService(&fakeCategoryStore{}, &fakeVisibleChannelStore{}).
		ListGroupedChannels(context.Background(), categoryWorkspaceID, categoryMemberID)
	if err != nil {
		t.Fatalf("ListGroupedChannels: %v", err)
	}
	if len(groups) != 1 || groups[0].Category != nil {
		t.Fatalf("got %+v, want a single virtual group", groups)
	}
	if groups[0].Channels == nil {
		t.Fatal("the virtual group must carry an empty slice, not nil")
	}
}

// Listing must never widen channel access: the grouping only ever sees what the
// existing read policy returned, so a private channel without membership is
// absent from every group — the virtual one included.
func TestChannelCategoryService_ListShowsOnlyChannelsThePolicyReturned(t *testing.T) {
	categories := &fakeCategoryStore{categories: []domain.ChannelCategory{
		{ID: "cat-1", WorkspaceID: categoryWorkspaceID, Name: "Alfa"},
	}}
	// What the SQL policy returns for this caller: a public channel, and a private
	// one they are a member of. The private channel they are not a member of is
	// simply not in the result, exactly as the query would leave it out.
	channels := &fakeVisibleChannelStore{accesses: []storage.VisibleChannelAccess{
		visibleChannel("ch-public", "publico", "cat-1", domain.ChannelTypePublic, nil),
		visibleChannel("ch-private-member", "privado-membro", "cat-1", domain.ChannelTypePrivate,
			&domain.ChannelMember{ChannelID: "ch-private-member", UserID: categoryMemberID, Role: domain.ChannelRoleMember}),
	}}

	groups, err := newCategoryService(categories, channels).
		ListGroupedChannels(context.Background(), categoryWorkspaceID, categoryMemberID)
	if err != nil {
		t.Fatalf("ListGroupedChannels: %v", err)
	}
	seen := map[string]bool{}
	for _, group := range groups {
		for _, channel := range group.Channels {
			seen[channel.Channel.ID] = true
		}
	}
	if !seen["ch-public"] || !seen["ch-private-member"] {
		t.Fatalf("expected both visible channels, got %v", seen)
	}
	if len(seen) != 2 {
		t.Fatalf("the listing must not invent channels, got %v", seen)
	}

	// can_write is derived server-side from the same policy the sidebar uses.
	for _, group := range groups {
		for _, channel := range group.Channels {
			if !channel.CanWrite {
				t.Fatalf("channel %q should be writable for an active member", channel.Channel.ID)
			}
		}
	}
}

// A private channel with a stale membership row for a different user must not
// become writable, and the derivation is the shared domain predicate.
func TestChannelCategoryService_ListDerivesCanWriteFromThePolicy(t *testing.T) {
	channels := &fakeVisibleChannelStore{accesses: []storage.VisibleChannelAccess{
		visibleChannel("ch-private-stale", "privado", "", domain.ChannelTypePrivate,
			&domain.ChannelMember{ChannelID: "ch-private-stale", UserID: "someone-else", Role: domain.ChannelRoleMember}),
	}}
	groups, err := newCategoryService(&fakeCategoryStore{}, channels).
		ListGroupedChannels(context.Background(), categoryWorkspaceID, categoryMemberID)
	if err != nil {
		t.Fatalf("ListGroupedChannels: %v", err)
	}
	if len(groups[0].Channels) != 1 {
		t.Fatalf("virtual group = %+v", groups[0].Channels)
	}
	if groups[0].Channels[0].CanWrite {
		t.Fatal("a membership row belonging to another user must not grant write access")
	}
}

// A channel pointing at a category outside the listing cannot happen while the
// composite FK holds, but if it did the channel must survive in the virtual group
// rather than disappear.
func TestChannelCategoryService_ListKeepsChannelsWithAnUnknownCategory(t *testing.T) {
	channels := &fakeVisibleChannelStore{accesses: []storage.VisibleChannelAccess{
		visibleChannel("ch-orphan", "orfao", "cat-unknown", domain.ChannelTypePublic, nil),
	}}
	groups, err := newCategoryService(&fakeCategoryStore{}, channels).
		ListGroupedChannels(context.Background(), categoryWorkspaceID, categoryMemberID)
	if err != nil {
		t.Fatalf("ListGroupedChannels: %v", err)
	}
	if len(groups[0].Channels) != 1 || groups[0].Channels[0].Channel.ID != "ch-orphan" {
		t.Fatalf("virtual group = %+v", groups[0].Channels)
	}
}

// Two queries, whatever the number of categories: no per-category fetch.
func TestChannelCategoryService_ListDoesNotQueryPerCategory(t *testing.T) {
	many := make([]domain.ChannelCategory, 0, 25)
	for i := 0; i < 25; i++ {
		many = append(many, domain.ChannelCategory{ID: string(rune('a' + i)), WorkspaceID: categoryWorkspaceID, Position: i})
	}
	categories := &fakeCategoryStore{categories: many}
	channels := &fakeVisibleChannelStore{}

	if _, err := newCategoryService(categories, channels).
		ListGroupedChannels(context.Background(), categoryWorkspaceID, categoryMemberID); err != nil {
		t.Fatalf("ListGroupedChannels: %v", err)
	}
	if categories.listCalls != 1 {
		t.Fatalf("category listing ran %d times, want 1", categories.listCalls)
	}
	if channels.callCount != 1 {
		t.Fatalf("channel listing ran %d times, want 1", channels.callCount)
	}
}

func TestChannelCategoryService_ListPropagatesStorageFailures(t *testing.T) {
	t.Run("category listing fails", func(t *testing.T) {
		channels := &fakeVisibleChannelStore{}
		_, err := newCategoryService(&fakeCategoryStore{listErr: errors.New("boom")}, channels).
			ListGroupedChannels(context.Background(), categoryWorkspaceID, categoryMemberID)
		if err == nil {
			t.Fatal("expected an error")
		}
		if channels.callCount != 0 {
			t.Fatal("a failed category listing must not be followed by a channel listing")
		}
	})
	t.Run("channel listing fails", func(t *testing.T) {
		_, err := newCategoryService(&fakeCategoryStore{}, &fakeVisibleChannelStore{err: errors.New("boom")}).
			ListGroupedChannels(context.Background(), categoryWorkspaceID, categoryMemberID)
		if err == nil {
			t.Fatal("expected an error")
		}
	})
}
