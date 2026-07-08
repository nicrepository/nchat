package service

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

const (
	PinTargetChannel = "channel"
	PinTargetDM      = "dm"
)

// PinActionInput identifies a pin/unpin action. ActorUserID always comes from
// the authenticated context — never from the request payload.
type PinActionInput struct {
	WorkspaceID string
	TargetType  string
	TargetID    string
	MessageID   string
	ActorUserID string
}

// ListPinsInput identifies the container whose pins the caller wants to read.
type ListPinsInput struct {
	WorkspaceID string
	TargetType  string
	TargetID    string
	ViewerID    string
}

type ListPinsOutput = storage.ListPinsResult

// PinService implements RF-05: channel/DM pinned messages, gated to current
// read access by the store's shared message visibility SQL.
type PinService struct {
	pins storage.PinStore
}

// NewPinService returns a PinService backed by the given store.
func NewPinService(pins storage.PinStore) *PinService {
	return &PinService{pins: pins}
}

func validatePinAction(input PinActionInput) (PinActionInput, error) {
	input.WorkspaceID = strings.TrimSpace(input.WorkspaceID)
	input.TargetType = strings.TrimSpace(input.TargetType)
	input.TargetID = strings.TrimSpace(input.TargetID)
	input.MessageID = strings.TrimSpace(input.MessageID)
	input.ActorUserID = strings.TrimSpace(input.ActorUserID)
	if input.WorkspaceID == "" || input.TargetType == "" || input.TargetID == "" || input.MessageID == "" || input.ActorUserID == "" {
		return input, fmt.Errorf("%w: workspace, target, message and actor are required", domain.ErrInvalidInput)
	}
	if input.TargetType != PinTargetChannel && input.TargetType != PinTargetDM {
		return input, fmt.Errorf("%w: target_type must be channel or dm", domain.ErrInvalidInput)
	}
	return input, nil
}

// Pin pins a message for the whole container. Idempotent. Returns ErrNotFound
// (message missing/deleted/unreadable/not in target) or ErrPinLimitReached.
func (s *PinService) Pin(ctx context.Context, input PinActionInput) error {
	input, err := validatePinAction(input)
	if err != nil {
		return err
	}
	slog.InfoContext(ctx, "chat pin message",
		"actor_user_id", input.ActorUserID,
		"target_type", input.TargetType,
		"target_id", input.TargetID,
		"message_id", input.MessageID,
	)
	return s.pins.AddPin(ctx, input.WorkspaceID, input.TargetType, input.TargetID, input.MessageID, input.ActorUserID)
}

// Unpin removes a pin. Idempotent. Same authorization as Pin.
func (s *PinService) Unpin(ctx context.Context, input PinActionInput) error {
	input, err := validatePinAction(input)
	if err != nil {
		return err
	}
	slog.InfoContext(ctx, "chat unpin message",
		"actor_user_id", input.ActorUserID,
		"target_type", input.TargetType,
		"target_id", input.TargetID,
		"message_id", input.MessageID,
	)
	return s.pins.RemovePin(ctx, input.WorkspaceID, input.TargetType, input.TargetID, input.MessageID, input.ActorUserID)
}

// ListPins returns the target's pins, newest first. Requires current read access.
func (s *PinService) ListPins(ctx context.Context, input ListPinsInput) (ListPinsOutput, error) {
	workspaceID := strings.TrimSpace(input.WorkspaceID)
	targetType := strings.TrimSpace(input.TargetType)
	targetID := strings.TrimSpace(input.TargetID)
	viewerID := strings.TrimSpace(input.ViewerID)
	if workspaceID == "" || targetType == "" || targetID == "" || viewerID == "" {
		return ListPinsOutput{}, fmt.Errorf("%w: workspace, target and viewer are required", domain.ErrInvalidInput)
	}
	if targetType != PinTargetChannel && targetType != PinTargetDM {
		return ListPinsOutput{}, fmt.Errorf("%w: target_type must be channel or dm", domain.ErrInvalidInput)
	}
	return s.pins.ListPins(ctx, workspaceID, targetType, targetID, viewerID)
}
