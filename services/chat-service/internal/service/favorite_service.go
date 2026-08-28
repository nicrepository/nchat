package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// FavoriteMessageInput identifies a favorite/unfavorite action. UserID always
// comes from the authenticated context — never from the request payload.
type FavoriteMessageInput struct {
	WorkspaceID string
	UserID      string
	MessageID   string
}

// ListFavoritesInput identifies one page of the caller's own favorites.
type ListFavoritesInput struct {
	WorkspaceID  string
	UserID       string
	BeforeCursor string // opaque encoded cursor; empty = most recent page
	Limit        int
}

// ListFavoritesOutput is one page of the caller's favorites, newest first.
// NextCursor is non-empty when an older page exists.
type ListFavoritesOutput struct {
	Favorites  []domain.FavoriteMessage
	NextCursor string
}

// FavoriteService implements RF-06: per-user private message favorites.
type FavoriteService struct{ favorites storage.FavoriteStore }

// NewFavoriteService returns a FavoriteService backed by the given store.
func NewFavoriteService(favorites storage.FavoriteStore) *FavoriteService {
	return &FavoriteService{favorites: favorites}
}

func validateFavoriteInput(input FavoriteMessageInput) (FavoriteMessageInput, error) {
	input.WorkspaceID = strings.TrimSpace(input.WorkspaceID)
	input.UserID = strings.TrimSpace(input.UserID)
	input.MessageID = strings.TrimSpace(input.MessageID)
	if input.WorkspaceID == "" || input.UserID == "" || input.MessageID == "" {
		return input, fmt.Errorf("%w: workspace, user and message are required", domain.ErrInvalidInput)
	}
	return input, nil
}

// FavoriteMessage records the favorite. Read access is re-checked in SQL;
// unauthorized and missing targets both surface ErrNotFound (non-enumerating).
func (s *FavoriteService) FavoriteMessage(ctx context.Context, input FavoriteMessageInput) error {
	input, err := validateFavoriteInput(input)
	if err != nil {
		return err
	}
	return s.favorites.AddFavorite(ctx, storage.AddFavoriteInput{
		WorkspaceID: input.WorkspaceID,
		UserID:      input.UserID,
		MessageID:   input.MessageID,
	})
}

// UnfavoriteMessage removes the caller's own favorite. Idempotent.
func (s *FavoriteService) UnfavoriteMessage(ctx context.Context, input FavoriteMessageInput) error {
	input, err := validateFavoriteInput(input)
	if err != nil {
		return err
	}
	return s.favorites.RemoveFavorite(ctx, input.UserID, input.MessageID)
}

// ListFavorites returns one page of the caller's favorites, filtered by
// current read access. Returns ErrInvalidCursor for malformed cursors.
func (s *FavoriteService) ListFavorites(ctx context.Context, input ListFavoritesInput) (ListFavoritesOutput, error) {
	workspaceID := strings.TrimSpace(input.WorkspaceID)
	userID := strings.TrimSpace(input.UserID)
	if workspaceID == "" || userID == "" {
		return ListFavoritesOutput{}, fmt.Errorf("%w: workspace and user are required", domain.ErrInvalidInput)
	}

	storageInput := storage.ListFavoritesInput{
		WorkspaceID: workspaceID,
		UserID:      userID,
		Limit:       input.Limit,
	}
	if input.BeforeCursor != "" {
		c, err := storage.DecodeCursor(input.BeforeCursor)
		if err != nil {
			return ListFavoritesOutput{}, domain.ErrInvalidCursor
		}
		storageInput.BeforeCursor = &c
	}

	result, err := s.favorites.ListFavorites(ctx, storageInput)
	if err != nil {
		return ListFavoritesOutput{}, fmt.Errorf("list favorites: %w", err)
	}

	var nextCursor string
	if result.NextCursor != nil {
		nextCursor = storage.EncodeCursor(*result.NextCursor)
	}
	return ListFavoritesOutput{Favorites: result.Favorites, NextCursor: nextCursor}, nil
}
