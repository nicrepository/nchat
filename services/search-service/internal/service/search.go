package service

import (
	"context"
	"fmt"
	"time"

	"github.com/nicrepository/nchat/services/search-service/internal/domain"
)

type Store interface {
	LegacyMessages(context.Context, string, string, int, domain.LegacyMessageCursor) ([]domain.LegacyMessageResult, error)
	Messages(context.Context, string, string, int, domain.MessageCursor) ([]domain.MessageResult, error)
	Users(context.Context, string, string, int, domain.NameCursor) ([]domain.UserResult, error)
	Channels(context.Context, string, string, int, domain.NameCursor) ([]domain.ChannelResult, error)
	Groups(context.Context, string, string, int, domain.NameCursor) ([]domain.GroupResult, error)
	Files(context.Context, string, string, int, domain.TimeCursor) ([]domain.FileResult, error)
}
type Search struct {
	store Store
	now   func() time.Time
}

func New(store Store) *Search { return &Search{store: store, now: time.Now} }

// paginate runs one page: decode the caller's cursor, fetch limit+1 rows so
// has-more needs no COUNT, and encode the next cursor from the last row kept.
func paginate[C, T any](
	label, raw string, limit int,
	decode func(string) (C, error),
	fetch func(C) ([]T, error),
	encode func(T) (string, error),
) (domain.Page[T], error) {
	var cursor C
	if raw != "" {
		var err error
		if cursor, err = decode(raw); err != nil {
			return domain.Page[T]{}, domain.ErrInvalidInput
		}
	}
	items, err := fetch(cursor)
	if err != nil {
		return domain.Page[T]{}, fmt.Errorf("search %s: %w", label, err)
	}
	page := domain.Page[T]{Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		page.NextCursor, err = encode(items[limit-1])
	}
	return page, err
}

// SearchLegacyMessages serves GET /api/search/messages, the pre-#900 contract.
func (s *Search) SearchLegacyMessages(ctx context.Context, userID, query string, limit int, raw string) (domain.LegacyMessagePage, error) {
	return paginate("legacy messages", raw, limit,
		func(r string) (domain.LegacyMessageCursor, error) { return domain.DecodeLegacyMessageCursor(r, query) },
		func(c domain.LegacyMessageCursor) ([]domain.LegacyMessageResult, error) {
			return s.store.LegacyMessages(ctx, userID, query, limit+1, c)
		},
		func(last domain.LegacyMessageResult) (string, error) {
			return domain.EncodeLegacyMessageCursor(query, last.Score, last.CreatedAt, last.ID)
		})
}

// SearchMessages serves GET /api/search/v2/messages. It ranks every page against the clock of the first one, carried
// in the cursor, so scores stay comparable across pages.
func (s *Search) SearchMessages(ctx context.Context, userID, query string, limit int, raw string) (domain.MessagePage, error) {
	rankedAt := s.now().UTC()
	return paginate("messages", raw, limit,
		func(r string) (domain.MessageCursor, error) { return domain.DecodeMessageCursor(r, query) },
		func(c domain.MessageCursor) ([]domain.MessageResult, error) {
			if c.Version == 0 {
				c.RankedAt = rankedAt
			}
			rankedAt = c.RankedAt
			return s.store.Messages(ctx, userID, query, limit+1, c)
		},
		func(last domain.MessageResult) (string, error) {
			return domain.EncodeMessageCursor(query, last.Score, last.CreatedAt, last.ID, rankedAt)
		})
}

func (s *Search) SearchUsers(ctx context.Context, userID, query string, limit int, raw string) (domain.UserPage, error) {
	return paginate("users", raw, limit, nameDecoder(domain.CursorUsers, query),
		func(c domain.NameCursor) ([]domain.UserResult, error) {
			return s.store.Users(ctx, userID, query, limit+1, c)
		},
		func(last domain.UserResult) (string, error) {
			return domain.EncodeNameCursor(domain.CursorUsers, query, last.SortName, last.ID)
		})
}

func (s *Search) SearchChannels(ctx context.Context, userID, query string, limit int, raw string) (domain.ChannelPage, error) {
	return paginate("channels", raw, limit, nameDecoder(domain.CursorChannels, query),
		func(c domain.NameCursor) ([]domain.ChannelResult, error) {
			return s.store.Channels(ctx, userID, query, limit+1, c)
		},
		func(last domain.ChannelResult) (string, error) {
			return domain.EncodeNameCursor(domain.CursorChannels, query, last.SortName, last.ID)
		})
}

func (s *Search) SearchGroups(ctx context.Context, userID, query string, limit int, raw string) (domain.GroupPage, error) {
	return paginate("groups", raw, limit, nameDecoder(domain.CursorGroups, query),
		func(c domain.NameCursor) ([]domain.GroupResult, error) {
			return s.store.Groups(ctx, userID, query, limit+1, c)
		},
		func(last domain.GroupResult) (string, error) {
			return domain.EncodeNameCursor(domain.CursorGroups, query, last.SortName, last.ID)
		})
}

func (s *Search) SearchFiles(ctx context.Context, userID, query string, limit int, raw string) (domain.FilePage, error) {
	return paginate("files", raw, limit,
		func(r string) (domain.TimeCursor, error) { return domain.DecodeFileCursor(r, query) },
		func(c domain.TimeCursor) ([]domain.FileResult, error) {
			return s.store.Files(ctx, userID, query, limit+1, c)
		},
		func(last domain.FileResult) (string, error) {
			return domain.EncodeFileCursor(query, last.CreatedAt, last.ID)
		})
}

func nameDecoder(kind, query string) func(string) (domain.NameCursor, error) {
	return func(raw string) (domain.NameCursor, error) { return domain.DecodeNameCursor(raw, kind, query) }
}
