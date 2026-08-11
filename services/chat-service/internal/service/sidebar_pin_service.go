package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

type SidebarPinInput struct{ WorkspaceID, UserID, ConversationType, ConversationID string }
type SidebarPinPublisher interface {
	PublishSidebarPinUpdated(context.Context, string, string, string, string)
}
type SidebarPinService struct {
	store     storage.SidebarPinStore
	publisher SidebarPinPublisher
}

func NewSidebarPinService(store storage.SidebarPinStore) *SidebarPinService {
	return &SidebarPinService{store: store}
}
func (s *SidebarPinService) SetPublisher(p SidebarPinPublisher) { s.publisher = p }

func validateSidebarPinInput(in SidebarPinInput) (SidebarPinInput, error) {
	in.WorkspaceID = strings.TrimSpace(in.WorkspaceID)
	in.UserID = strings.TrimSpace(in.UserID)
	in.ConversationType = strings.TrimSpace(in.ConversationType)
	in.ConversationID = strings.TrimSpace(in.ConversationID)
	if in.WorkspaceID == "" || in.UserID == "" || in.ConversationID == "" || (in.ConversationType != "channel" && in.ConversationType != "dm") {
		return in, fmt.Errorf("%w: invalid sidebar pin", domain.ErrInvalidInput)
	}
	return in, nil
}
func (s *SidebarPinService) Pin(ctx context.Context, in SidebarPinInput) (storage.SidebarConversationPin, error) {
	in, err := validateSidebarPinInput(in)
	if err != nil {
		return storage.SidebarConversationPin{}, err
	}
	pin, err := s.store.Add(ctx, storage.AddSidebarConversationPinInput{WorkspaceID: in.WorkspaceID, UserID: in.UserID, ConversationType: in.ConversationType, ConversationID: in.ConversationID})
	if err == nil && s.publisher != nil {
		s.publisher.PublishSidebarPinUpdated(ctx, in.WorkspaceID, in.ConversationType, in.ConversationID, in.UserID)
	}
	return pin, err
}
func (s *SidebarPinService) Unpin(ctx context.Context, in SidebarPinInput) error {
	in, err := validateSidebarPinInput(in)
	if err != nil {
		return err
	}
	err = s.store.Remove(ctx, in.WorkspaceID, in.UserID, in.ConversationType, in.ConversationID)
	if err == nil && s.publisher != nil {
		s.publisher.PublishSidebarPinUpdated(ctx, in.WorkspaceID, in.ConversationType, in.ConversationID, in.UserID)
	}
	return err
}
