package service

import (
	"context"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// PresenceSettingsStore persists a member's manual presence (issue #798).
type PresenceSettingsStore interface {
	SetManual(ctx context.Context, workspaceID, userID string, state domain.PresenceManualState, expiresAt time.Time) (domain.PresenceOverride, error)
	ClearManual(ctx context.Context, workspaceID, userID string) error
	Manual(ctx context.Context, workspaceID, userID string) (domain.PresenceOverride, error)
}

// PresenceSettingsPublisher is the realtime layer's side of a settings change.
//
// ChangePresenceFacts is the presence facts boundary (ws.Hub): the write runs
// inside it, so a projection composed from the previous state can never be
// committed after the new one is stored. When the boundary cannot be opened it
// returns domain.ErrPresenceFactsUnavailable and the write does not happen.
// PublishPresenceSettingsChanged then tells every replica that serves the
// member to republish them and their own sessions to re-read their settings.
type PresenceSettingsPublisher interface {
	ChangePresenceFacts(ctx context.Context, workspaceID string, userIDs []string, mutate func(context.Context) error) error
	PublishPresenceSettingsChanged(ctx context.Context, workspaceID, userID string)
}

// PresenceService validates and applies a member's own manual presence.
//
// The caller's identity and workspace are arguments resolved server-side by
// the HTTP layer; nothing here accepts a user id from a request body. The
// clock is the server's: an expiry is validated against it, and the stored
// timestamp is the database's.
type PresenceService struct {
	store     PresenceSettingsStore
	publisher PresenceSettingsPublisher
	now       func() time.Time
}

func NewPresenceService(store PresenceSettingsStore, publisher PresenceSettingsPublisher) *PresenceService {
	return &PresenceService{store: store, publisher: publisher, now: time.Now}
}

// Manual returns the member's live manual state; the zero override means
// automatic presence.
func (s *PresenceService) Manual(ctx context.Context, workspaceID, userID string) (domain.PresenceOverride, error) {
	return s.store.Manual(ctx, workspaceID, userID)
}

// SetManual validates and stores a manual state, then announces it.
func (s *PresenceService) SetManual(
	ctx context.Context, workspaceID, userID, state string, expiresAt time.Time,
) (domain.PresenceOverride, error) {
	manual, err := domain.ParsePresenceManualState(state)
	if err != nil {
		return domain.PresenceOverride{}, err
	}
	if err := domain.ValidatePresenceExpiry(expiresAt, s.now()); err != nil {
		return domain.PresenceOverride{}, err
	}
	var stored domain.PresenceOverride
	err = s.change(ctx, workspaceID, userID, func(ctx context.Context) error {
		var setErr error
		stored, setErr = s.store.SetManual(ctx, workspaceID, userID, manual, expiresAt.UTC())
		return setErr
	})
	if err != nil {
		return domain.PresenceOverride{}, err
	}
	s.announce(ctx, workspaceID, userID)
	return stored, nil
}

// ClearManual returns the member to automatic presence, then announces it.
func (s *PresenceService) ClearManual(ctx context.Context, workspaceID, userID string) error {
	err := s.change(ctx, workspaceID, userID, func(ctx context.Context) error {
		return s.store.ClearManual(ctx, workspaceID, userID)
	})
	if err != nil {
		return err
	}
	s.announce(ctx, workspaceID, userID)
	return nil
}

// change runs a write through the presence facts boundary. Without a realtime
// layer there is no projection to protect and the write runs alone.
func (s *PresenceService) change(ctx context.Context, workspaceID, userID string, write func(context.Context) error) error {
	if s.publisher == nil {
		return write(ctx)
	}
	return s.publisher.ChangePresenceFacts(ctx, workspaceID, []string{userID}, write)
}

func (s *PresenceService) announce(ctx context.Context, workspaceID, userID string) {
	if s.publisher != nil {
		s.publisher.PublishPresenceSettingsChanged(ctx, workspaceID, userID)
	}
}
