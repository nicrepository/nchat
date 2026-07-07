package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// PinActionInput identifies a pin/unpin action. ActorUserID always comes from
// the authenticated context — never from the request payload.
type PinActionInput struct {
	WorkspaceID string
	ChannelID   string
	MessageID   string
	ActorUserID string
}

// ListPinsInput identifies the channel whose pins the caller wants to read.
type ListPinsInput struct {
	WorkspaceID string
	ChannelID   string
	ViewerID    string
}

// PinService implements RF-05: channel-wide pinned messages, gated to elevated
// roles for writes and to channel read access for listing.
type PinService struct {
	pins        storage.PinStore
	permissions *PermissionService
}

// NewPinService returns a PinService backed by the given store and permissions.
func NewPinService(pins storage.PinStore, permissions *PermissionService) *PinService {
	return &PinService{pins: pins, permissions: permissions}
}

func validatePinAction(input PinActionInput) (PinActionInput, error) {
	input.WorkspaceID = strings.TrimSpace(input.WorkspaceID)
	input.ChannelID = strings.TrimSpace(input.ChannelID)
	input.MessageID = strings.TrimSpace(input.MessageID)
	input.ActorUserID = strings.TrimSpace(input.ActorUserID)
	if input.WorkspaceID == "" || input.ChannelID == "" || input.MessageID == "" || input.ActorUserID == "" {
		return input, fmt.Errorf("%w: workspace, channel, message and actor are required", domain.ErrInvalidInput)
	}
	return input, nil
}

// authorizeModerate enforces read access (→ ErrNotFound) and elevated role
// (→ ErrForbidden) before a pin/unpin write.
func (s *PinService) authorizeModerate(ctx context.Context, workspaceID, channelID, userID string) error {
	readable, moderate, err := s.permissions.CanModerateChannel(ctx, workspaceID, channelID, userID)
	if err != nil {
		return err
	}
	if !readable {
		// Non-enumerating: a channel the caller cannot see is "not found".
		return domain.ErrNotFound
	}
	if !moderate {
		return domain.ErrForbidden
	}
	return nil
}

// Pin pins a message for the whole channel. Idempotent. Returns ErrNotFound
// (message missing/deleted/not in channel), ErrForbidden (role too low), or
// ErrPinLimitReached (channel at capacity).
func (s *PinService) Pin(ctx context.Context, input PinActionInput) error {
	input, err := validatePinAction(input)
	if err != nil {
		return err
	}
	if err := s.authorizeModerate(ctx, input.WorkspaceID, input.ChannelID, input.ActorUserID); err != nil {
		return err
	}
	return s.pins.AddPin(ctx, input.ChannelID, input.MessageID, input.ActorUserID)
}

// Unpin removes a channel pin. Idempotent. Same authorization as Pin.
func (s *PinService) Unpin(ctx context.Context, input PinActionInput) error {
	input, err := validatePinAction(input)
	if err != nil {
		return err
	}
	if err := s.authorizeModerate(ctx, input.WorkspaceID, input.ChannelID, input.ActorUserID); err != nil {
		return err
	}
	return s.pins.RemovePin(ctx, input.ChannelID, input.MessageID)
}

// ListPins returns the channel's pins, newest first. Requires channel read
// access — any member who can read the channel can see its pins (RF-05
// visibility mirrors reading messages). Returns ErrNotFound when the caller
// cannot read the channel.
func (s *PinService) ListPins(ctx context.Context, input ListPinsInput) ([]domain.PinnedMessage, error) {
	workspaceID := strings.TrimSpace(input.WorkspaceID)
	channelID := strings.TrimSpace(input.ChannelID)
	viewerID := strings.TrimSpace(input.ViewerID)
	if workspaceID == "" || channelID == "" || viewerID == "" {
		return nil, fmt.Errorf("%w: workspace, channel and viewer are required", domain.ErrInvalidInput)
	}
	allowed, err := s.permissions.CanRead(ctx, workspaceID, channelID, viewerID)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, domain.ErrNotFound
	}
	return s.pins.ListPins(ctx, channelID, viewerID)
}
