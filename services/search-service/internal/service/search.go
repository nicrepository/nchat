package service

import (
	"context"
	"fmt"

	"github.com/nicrepository/nchat/services/search-service/internal/domain"
)

type Store interface {
	Messages(context.Context, string, string, int, domain.MessageCursor) ([]domain.MessageResult, error)
	Users(context.Context, string, string, int, domain.NameCursor) ([]domain.UserResult, error)
	Channels(context.Context, string, string, int, domain.NameCursor) ([]domain.ChannelResult, error)
}
type Search struct{ store Store }

func New(store Store) *Search { return &Search{store: store} }

func (s *Search) SearchMessages(ctx context.Context, userID, query string, limit int, raw string) (domain.MessagePage, error) {
	var c domain.MessageCursor
	var err error
	if raw != "" {
		c, err = domain.DecodeMessageCursor(raw, query)
		if err != nil {
			return domain.MessagePage{}, domain.ErrInvalidInput
		}
	}
	items, err := s.store.Messages(ctx, userID, query, limit+1, c)
	if err != nil {
		return domain.MessagePage{}, fmt.Errorf("search messages: %w", err)
	}
	page := domain.MessagePage{Items: items}
	if len(items) > limit {
		last := items[limit-1]
		page.Items = items[:limit]
		page.NextCursor, err = domain.EncodeMessageCursor(query, last.Score, last.CreatedAt, last.ID)
	}
	return page, err
}
func (s *Search) SearchUsers(ctx context.Context, userID, query string, limit int, raw string) (domain.UserPage, error) {
	var c domain.NameCursor
	var err error
	if raw != "" {
		c, err = domain.DecodeNameCursor(raw, domain.CursorUsers, query)
		if err != nil {
			return domain.UserPage{}, domain.ErrInvalidInput
		}
	}
	items, err := s.store.Users(ctx, userID, query, limit+1, c)
	if err != nil {
		return domain.UserPage{}, fmt.Errorf("search users: %w", err)
	}
	p := domain.UserPage{Items: items}
	if len(items) > limit {
		last := items[limit-1]
		p.Items = items[:limit]
		p.NextCursor, err = domain.EncodeNameCursor(domain.CursorUsers, query, last.SortName, last.ID)
	}
	return p, err
}
func (s *Search) SearchChannels(ctx context.Context, userID, query string, limit int, raw string) (domain.ChannelPage, error) {
	var c domain.NameCursor
	var err error
	if raw != "" {
		c, err = domain.DecodeNameCursor(raw, domain.CursorChannels, query)
		if err != nil {
			return domain.ChannelPage{}, domain.ErrInvalidInput
		}
	}
	items, err := s.store.Channels(ctx, userID, query, limit+1, c)
	if err != nil {
		return domain.ChannelPage{}, fmt.Errorf("search channels: %w", err)
	}
	p := domain.ChannelPage{Items: items}
	if len(items) > limit {
		last := items[limit-1]
		p.Items = items[:limit]
		p.NextCursor, err = domain.EncodeNameCursor(domain.CursorChannels, query, last.SortName, last.ID)
	}
	return p, err
}
