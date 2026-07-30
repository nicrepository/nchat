package storage_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/nicrepository/nchat/services/file-service/internal/domain"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
	"github.com/nicrepository/nchat/services/file-service/internal/storage"
)

func listQuery(limit int) service.ListDestinationAttachmentsQuery {
	return service.ListDestinationAttachmentsQuery{
		WorkspaceID: testWorkspaceID,
		ChannelID:   testChannelID,
		Limit:       limit,
	}
}

func attachmentRowValues(id, status, filename, mime string, size int64, createdAt time.Time) []any {
	return []any{
		id, status, filename, mime, size,
		pgtype.Timestamptz{Time: createdAt, Valid: true},
	}
}

func TestListChannelAttachmentsScopesAndOrdersTheQuery(t *testing.T) {
	created := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	pool := &fakePool{query: func(string, ...any) (pgx.Rows, error) {
		return &valueRows{rows: [][]any{
			attachmentRowValues("a-1", string(domain.StatusClean), "novo.pdf", "application/pdf", 2048, created),
			attachmentRowValues("a-2", string(domain.StatusPendingScan), "antigo.png", "", 10, created.Add(-time.Hour)),
		}}, nil
	}}

	got, err := storage.NewPGXAttachmentStore(pool).
		ListChannelAttachments(context.Background(), listQuery(5))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Both tenancy columns must be in the predicate: the workspace alone would
	// let a channel UUID from another tenant through if authorization regressed.
	for _, fragment := range []string{
		"FROM files.attachments",
		"a.destination_kind = 'channel'",
		"a.deleted_at IS NULL",
		"a.workspace_id = $1",
		"a.channel_id = $2",
		"a.status = ANY($3)",
		"ORDER BY a.created_at DESC, a.id DESC",
		"LIMIT $4",
	} {
		if !strings.Contains(pool.lastSQL, fragment) {
			t.Fatalf("query is missing %q:\n%s", fragment, pool.lastSQL)
		}
	}
	// Nothing that must never leave the process may be selected by a listing.
	for _, forbidden := range []string{"wrapped_dek", "storage_object_key", "envelope_version"} {
		if strings.Contains(pool.lastSQL, forbidden) {
			t.Fatalf("a listing must not select %q:\n%s", forbidden, pool.lastSQL)
		}
	}
	if pool.lastArgs[0] != testWorkspaceID || pool.lastArgs[1] != testChannelID || pool.lastArgs[3] != 5 {
		t.Fatalf("unexpected arguments: %v", pool.lastArgs)
	}
	wantStatuses := []string{
		string(domain.StatusPendingScan), string(domain.StatusClean), string(domain.StatusRejected),
	}
	if !reflect.DeepEqual(pool.lastArgs[2], wantStatuses) {
		t.Fatalf("expected the listable status set %v, got %v", wantStatuses, pool.lastArgs[2])
	}

	if len(got) != 2 || got[0].ID != "a-1" || got[1].ID != "a-2" {
		t.Fatalf("unexpected rows: %+v", got)
	}
	if got[0].Status != domain.StatusClean || got[0].Filename != "novo.pdf" || got[0].Size != 2048 {
		t.Fatalf("unexpected first row: %+v", got[0])
	}
	if !got[0].CreatedAt.Equal(created) || got[0].CreatedAt.Location() != time.UTC {
		t.Fatalf("timestamps must come back as UTC, got %v", got[0].CreatedAt)
	}
}

func TestListChannelAttachmentsClampsTheLimit(t *testing.T) {
	for name, tt := range map[string]struct{ asked, want int }{
		"unspecified":   {asked: 0, want: domain.DefaultAttachmentListLimit},
		"negative":      {asked: -3, want: domain.DefaultAttachmentListLimit},
		"above ceiling": {asked: 5_000, want: domain.MaxAttachmentListLimit},
	} {
		t.Run(name, func(t *testing.T) {
			pool := &fakePool{query: func(string, ...any) (pgx.Rows, error) {
				return &valueRows{}, nil
			}}
			if _, err := storage.NewPGXAttachmentStore(pool).
				ListChannelAttachments(context.Background(), listQuery(tt.asked)); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if pool.lastArgs[3] != tt.want {
				t.Fatalf("expected limit %d, got %v", tt.want, pool.lastArgs[3])
			}
		})
	}
}

func TestListChannelAttachmentsRejectsAnUnknownStatus(t *testing.T) {
	pool := &fakePool{query: func(string, ...any) (pgx.Rows, error) {
		return &valueRows{rows: [][]any{
			attachmentRowValues("a-1", "not-a-status", "x.pdf", "application/pdf", 1, time.Now()),
		}}, nil
	}}
	if _, err := storage.NewPGXAttachmentStore(pool).
		ListChannelAttachments(context.Background(), listQuery(0)); err == nil {
		t.Fatal("a row outside the CHECK's closed set must not be served")
	}
}

func TestListChannelAttachmentsSurfacesIterationFailures(t *testing.T) {
	pool := &fakePool{query: func(string, ...any) (pgx.Rows, error) {
		return &valueRows{err: errors.New("connection lost")}, nil
	}}
	if _, err := storage.NewPGXAttachmentStore(pool).
		ListChannelAttachments(context.Background(), listQuery(0)); err == nil {
		t.Fatal("expected the iteration failure to surface")
	}
}

func TestListChannelAttachmentsRequiresAPool(t *testing.T) {
	_, err := storage.NewPGXAttachmentStore(nil).
		ListChannelAttachments(context.Background(), listQuery(0))
	if !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
}
