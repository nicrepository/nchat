package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

func TestWorkspaceService_GetDefault_Success(t *testing.T) {
	ws := domain.Workspace{ID: "ws-1", Slug: "default", Status: domain.WorkspaceStatusActive}
	svc := service.NewWorkspaceService(&fakeWorkspaceStore{workspace: ws}, &fakeChannelStore{})
	got, err := svc.GetDefault(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Slug != "default" {
		t.Fatalf("expected default, got %q", got.Slug)
	}
}

func TestWorkspaceService_GetDefault_StoreError_Propagates(t *testing.T) {
	svc := service.NewWorkspaceService(&fakeWorkspaceStore{getErr: domain.ErrNotFound}, &fakeChannelStore{})
	_, err := svc.GetDefault(context.Background())
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestWorkspaceService_CreateCategory_EmptyName_Error(t *testing.T) {
	svc := service.NewWorkspaceService(&fakeWorkspaceStore{}, &fakeChannelStore{})
	_, err := svc.CreateCategory(context.Background(), "ws-1", "  ", 0)
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
}

func TestWorkspaceService_CreateCategory_Success(t *testing.T) {
	cat := domain.ChannelCategory{ID: "cat-1", WorkspaceID: "ws-1", Name: "General"}
	svc := service.NewWorkspaceService(&fakeWorkspaceStore{}, &fakeChannelStore{createdCategory: cat})
	got, err := svc.CreateCategory(context.Background(), "ws-1", "General", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Name != "General" {
		t.Fatalf("expected General, got %q", got.Name)
	}
}

func TestWorkspaceService_CreateChannel_InvalidSlug_Error(t *testing.T) {
	svc := service.NewWorkspaceService(&fakeWorkspaceStore{}, &fakeChannelStore{})
	_, err := svc.CreateChannel(context.Background(), storage.CreateChannelInput{
		WorkspaceID: "ws-1", Slug: "has space!", DisplayName: "Test", Type: domain.ChannelTypePublic,
	})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
}

func TestWorkspaceService_CreateChannel_EmptyDisplayName_Error(t *testing.T) {
	svc := service.NewWorkspaceService(&fakeWorkspaceStore{}, &fakeChannelStore{})
	_, err := svc.CreateChannel(context.Background(), storage.CreateChannelInput{
		WorkspaceID: "ws-1", Slug: "general", DisplayName: "", Type: domain.ChannelTypePublic,
	})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
}

func TestWorkspaceService_CreateChannel_InvalidType_Error(t *testing.T) {
	svc := service.NewWorkspaceService(&fakeWorkspaceStore{}, &fakeChannelStore{})
	_, err := svc.CreateChannel(context.Background(), storage.CreateChannelInput{
		WorkspaceID: "ws-1", Slug: "general", DisplayName: "General", Type: "secret",
	})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
}

func TestWorkspaceService_CreateChannel_DuplicateSlug_Propagated(t *testing.T) {
	svc := service.NewWorkspaceService(&fakeWorkspaceStore{}, &fakeChannelStore{createChanErr: domain.ErrDuplicateSlug})
	_, err := svc.CreateChannel(context.Background(), storage.CreateChannelInput{
		WorkspaceID: "ws-1", Slug: "geral", DisplayName: "Geral", Type: domain.ChannelTypePublic,
	})
	if !errors.Is(err, domain.ErrDuplicateSlug) {
		t.Fatalf("expected ErrDuplicateSlug, got %v", err)
	}
}

func TestWorkspaceService_CreateChannel_NormalizesSlug(t *testing.T) {
	ch := domain.Channel{ID: "ch-1", Slug: "general"}
	svc := service.NewWorkspaceService(&fakeWorkspaceStore{}, &fakeChannelStore{createdChannel: ch})
	got, err := svc.CreateChannel(context.Background(), storage.CreateChannelInput{
		WorkspaceID: "ws-1", Slug: "  GENERAL  ", DisplayName: "General", Type: domain.ChannelTypePublic,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ID != "ch-1" {
		t.Fatalf("expected ch-1, got %q", got.ID)
	}
}

func TestWorkspaceService_CreateChannel_TrailingHyphen_Error(t *testing.T) {
	svc := service.NewWorkspaceService(&fakeWorkspaceStore{}, &fakeChannelStore{})
	_, err := svc.CreateChannel(context.Background(), storage.CreateChannelInput{
		WorkspaceID: "ws-1", Slug: "abc-", DisplayName: "Test", Type: domain.ChannelTypePublic,
	})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("trailing hyphen slug should be ErrInvalidInput, got %v", err)
	}
}

func TestWorkspaceService_CreateChannel_LeadingHyphen_Error(t *testing.T) {
	svc := service.NewWorkspaceService(&fakeWorkspaceStore{}, &fakeChannelStore{})
	_, err := svc.CreateChannel(context.Background(), storage.CreateChannelInput{
		WorkspaceID: "ws-1", Slug: "-abc", DisplayName: "Test", Type: domain.ChannelTypePublic,
	})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("leading hyphen slug should be ErrInvalidInput, got %v", err)
	}
}

func TestWorkspaceService_CreateChannel_Backslash_Error(t *testing.T) {
	svc := service.NewWorkspaceService(&fakeWorkspaceStore{}, &fakeChannelStore{})
	_, err := svc.CreateChannel(context.Background(), storage.CreateChannelInput{
		WorkspaceID: "ws-1", Slug: `a\b`, DisplayName: "Test", Type: domain.ChannelTypePublic,
	})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("backslash in slug should be ErrInvalidInput, got %v", err)
	}
}

func TestWorkspaceService_CreateChannel_SingleChar_Valid(t *testing.T) {
	ch := domain.Channel{ID: "ch-1", Slug: "a"}
	svc := service.NewWorkspaceService(&fakeWorkspaceStore{}, &fakeChannelStore{createdChannel: ch})
	got, err := svc.CreateChannel(context.Background(), storage.CreateChannelInput{
		WorkspaceID: "ws-1", Slug: "a", DisplayName: "A", Type: domain.ChannelTypePublic,
	})
	if err != nil {
		t.Fatalf("single-char slug should be valid, got %v", err)
	}
	if got.ID != "ch-1" {
		t.Fatalf("expected ch-1, got %q", got.ID)
	}
}

func TestWorkspaceService_CreateChannel_InternalHyphen_Valid(t *testing.T) {
	ch := domain.Channel{ID: "ch-1", Slug: "a-b"}
	svc := service.NewWorkspaceService(&fakeWorkspaceStore{}, &fakeChannelStore{createdChannel: ch})
	got, err := svc.CreateChannel(context.Background(), storage.CreateChannelInput{
		WorkspaceID: "ws-1", Slug: "a-b", DisplayName: "A B", Type: domain.ChannelTypePublic,
	})
	if err != nil {
		t.Fatalf("internal hyphen slug should be valid, got %v", err)
	}
	if got.ID != "ch-1" {
		t.Fatalf("expected ch-1, got %q", got.ID)
	}
}

func TestWorkspaceService_CreateChannel_GeneralChannelExists_Error(t *testing.T) {
	svc := service.NewWorkspaceService(&fakeWorkspaceStore{}, &fakeChannelStore{createChanErr: domain.ErrGeneralChannelExists})
	_, err := svc.CreateChannel(context.Background(), storage.CreateChannelInput{
		WorkspaceID: "ws-1", Slug: "geral2", DisplayName: "Geral 2", Type: domain.ChannelTypePublic,
	})
	if !errors.Is(err, domain.ErrGeneralChannelExists) {
		t.Fatalf("expected ErrGeneralChannelExists, got %v", err)
	}
}
