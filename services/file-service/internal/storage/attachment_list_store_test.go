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
	return destinationQuery(domain.DestinationKindChannel, testChannelID, limit)
}

func destinationQuery(
	kind domain.DestinationKind, id string, limit int,
) service.ListDestinationAttachmentsQuery {
	return service.ListDestinationAttachmentsQuery{
		WorkspaceID:   testWorkspaceID,
		Kind:          kind,
		DestinationID: id,
		Limit:         limit,
	}
}

func attachmentRowValues(id, status, filename, mime string, size int64, createdAt time.Time) []any {
	return []any{
		id, status, text(string(domain.PreviewStatusReady)), filename, mime, size,
		pgtype.Timestamptz{Time: createdAt, Valid: true},
		"", int32(0),
	}
}

// The listing must be indexable: each kind compares its own destination column
// directly and pins destination_kind as a literal, so the matching partial
// index (idx_attachments_channel / idx_attachments_conversation) applies. A
// COALESCE or an OR over the two columns is an expression neither index covers
// and would make this a scan-and-sort of the workspace's attachments.
func TestListDestinationAttachmentsUsesAnIndexablePredicatePerKind(t *testing.T) {
	created := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)

	for name, tt := range map[string]struct {
		kind          domain.DestinationKind
		destinationID string
		wantLiteral   string
		wantColumn    string
		// forbiddenColumn is the *other* kind's destination column: it must not
		// appear in the predicate at all, so the two kinds cannot share a plan.
		forbiddenColumn string
	}{
		"channel": {
			kind: domain.DestinationKindChannel, destinationID: testChannelID,
			wantLiteral: "a.destination_kind = 'channel'",
			wantColumn:  "a.channel_id = $2", forbiddenColumn: "conversation_id",
		},
		"dm": {
			kind: domain.DestinationKindDM, destinationID: testConversation,
			wantLiteral: "a.destination_kind = 'dm'",
			wantColumn:  "a.conversation_id = $2", forbiddenColumn: "channel_id",
		},
	} {
		t.Run(name, func(t *testing.T) {
			pool := &fakePool{query: func(string, ...any) (pgx.Rows, error) {
				return &valueRows{rows: [][]any{
					attachmentRowValues("a-1", string(domain.StatusClean), "novo.pdf", "application/pdf", 2048, created),
				}}, nil
			}}

			got, err := storage.NewPGXAttachmentStore(pool).ListDestinationAttachments(
				context.Background(), destinationQuery(tt.kind, tt.destinationID, 5),
			)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			// Everything the partial index predicate and its leading columns need.
			for _, fragment := range []string{
				"FROM files.attachments",
				tt.wantLiteral,
				"a.deleted_at IS NULL",
				"a.workspace_id = $1",
				tt.wantColumn,
				"a.status = ANY($3)",
				"m.status <> 'active'",
				"ORDER BY a.created_at DESC, a.id DESC",
				"LIMIT $4",
			} {
				if !strings.Contains(pool.lastSQL, fragment) {
					t.Fatalf("query is missing %q:\n%s", fragment, pool.lastSQL)
				}
			}
			// The regression guard: no expression over both columns, and no
			// bind parameter standing in for the destination kind.
			for _, forbidden := range []string{
				"COALESCE(a.channel_id",
				"COALESCE(a.conversation_id",
				"a.destination_kind = $",
				" OR ",
				"CASE",
			} {
				if strings.Contains(pool.lastSQL, forbidden) {
					t.Fatalf("predicate must stay directly indexable, found %q:\n%s", forbidden, pool.lastSQL)
				}
			}
			// The other kind's column must be absent entirely.
			if strings.Contains(pool.lastSQL, tt.forbiddenColumn) {
				t.Fatalf("the %s query must not reference %q:\n%s", name, tt.forbiddenColumn, pool.lastSQL)
			}
			// Nothing that must never leave the process may be selected.
			for _, forbidden := range []string{"wrapped_dek", "storage_object_key", "envelope_version"} {
				if strings.Contains(pool.lastSQL, forbidden) {
					t.Fatalf("a listing must not select %q:\n%s", forbidden, pool.lastSQL)
				}
			}

			// Both kinds share the argument order, so only the SQL varies.
			if pool.lastArgs[0] != testWorkspaceID || pool.lastArgs[1] != tt.destinationID ||
				pool.lastArgs[3] != 5 {
				t.Fatalf("unexpected arguments: %v", pool.lastArgs)
			}
			wantStatuses := []string{
				string(domain.StatusPendingScan), string(domain.StatusClean), string(domain.StatusRejected),
			}
			if !reflect.DeepEqual(pool.lastArgs[2], wantStatuses) {
				t.Fatalf("expected the listable status set %v, got %v", wantStatuses, pool.lastArgs[2])
			}
			if len(got) != 1 || got[0].ID != "a-1" {
				t.Fatalf("unexpected rows: %+v", got)
			}
		})
	}
}

// The same UUID used as a channel and as a conversation must produce two
// different queries: nothing about the identifier decides which space is read.
func TestListDestinationAttachmentsKeepsIdenticalIDsInSeparateSpaces(t *testing.T) {
	const sharedID = "55555555-5555-4555-8555-555555555555"
	seen := map[domain.DestinationKind]string{}

	for _, kind := range []domain.DestinationKind{
		domain.DestinationKindChannel, domain.DestinationKindDM,
	} {
		pool := &fakePool{query: func(string, ...any) (pgx.Rows, error) {
			return &valueRows{}, nil
		}}
		if _, err := storage.NewPGXAttachmentStore(pool).ListDestinationAttachments(
			context.Background(), destinationQuery(kind, sharedID, 5),
		); err != nil {
			t.Fatalf("%s: unexpected error: %v", kind, err)
		}
		seen[kind] = pool.lastSQL
	}

	if seen[domain.DestinationKindChannel] == seen[domain.DestinationKindDM] {
		t.Fatal("the same id in both spaces must not run the same query")
	}
	if !strings.Contains(seen[domain.DestinationKindChannel], "a.channel_id = $2") ||
		strings.Contains(seen[domain.DestinationKindChannel], "conversation_id") {
		t.Fatalf("channel query leaked into the conversation space:\n%s", seen[domain.DestinationKindChannel])
	}
	if !strings.Contains(seen[domain.DestinationKindDM], "a.conversation_id = $2") ||
		strings.Contains(seen[domain.DestinationKindDM], "channel_id") {
		t.Fatalf("dm query leaked into the channel space:\n%s", seen[domain.DestinationKindDM])
	}
}

func TestListDestinationAttachmentsMapsRowsAndOrderingFaithfully(t *testing.T) {
	created := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	pool := &fakePool{query: func(string, ...any) (pgx.Rows, error) {
		return &valueRows{rows: [][]any{
			attachmentRowValues("a-1", string(domain.StatusClean), "novo.pdf", "application/pdf", 2048, created),
			attachmentRowValues("a-2", string(domain.StatusPendingScan), "antigo.png", "", 10, created.Add(-time.Hour)),
		}}, nil
	}}

	got, err := storage.NewPGXAttachmentStore(pool).
		ListDestinationAttachments(context.Background(), listQuery(5))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
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

func TestListDestinationAttachmentsClampsTheLimit(t *testing.T) {
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
				ListDestinationAttachments(context.Background(), listQuery(tt.asked)); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if pool.lastArgs[3] != tt.want {
				t.Fatalf("expected limit %d, got %v", tt.want, pool.lastArgs[3])
			}
		})
	}
}

func TestListDestinationAttachmentsRejectsAnUnknownStatus(t *testing.T) {
	pool := &fakePool{query: func(string, ...any) (pgx.Rows, error) {
		return &valueRows{rows: [][]any{
			attachmentRowValues("a-1", "not-a-status", "x.pdf", "application/pdf", 1, time.Now()),
		}}, nil
	}}
	if _, err := storage.NewPGXAttachmentStore(pool).
		ListDestinationAttachments(context.Background(), listQuery(0)); err == nil {
		t.Fatal("a row outside the CHECK's closed set must not be served")
	}
}

func TestListDestinationAttachmentsSurfacesIterationFailures(t *testing.T) {
	pool := &fakePool{query: func(string, ...any) (pgx.Rows, error) {
		return &valueRows{err: errors.New("connection lost")}, nil
	}}
	if _, err := storage.NewPGXAttachmentStore(pool).
		ListDestinationAttachments(context.Background(), listQuery(0)); err == nil {
		t.Fatal("expected the iteration failure to surface")
	}
}

func TestListDestinationAttachmentsRequiresAPool(t *testing.T) {
	_, err := storage.NewPGXAttachmentStore(nil).
		ListDestinationAttachments(context.Background(), listQuery(0))
	if !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
}

// A conversation listing must be bound to the conversation column, and an
// unknown kind must never reach the database at all (issue #441).
func TestListDestinationAttachmentsBindsTheConversationDestination(t *testing.T) {
	pool := &fakePool{query: func(string, ...any) (pgx.Rows, error) {
		return &valueRows{}, nil
	}}
	if _, err := storage.NewPGXAttachmentStore(pool).ListDestinationAttachments(
		context.Background(), destinationQuery(domain.DestinationKindDM, testConversation, 5),
	); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pool.lastArgs[0] != testWorkspaceID || pool.lastArgs[1] != testConversation {
		t.Fatalf("query must be bound to the conversation: %v", pool.lastArgs)
	}
}

func TestListDestinationAttachmentsRejectsAnUnknownKind(t *testing.T) {
	pool := &fakePool{}
	_, err := storage.NewPGXAttachmentStore(pool).ListDestinationAttachments(
		context.Background(), destinationQuery("workspace", testChannelID, 5),
	)
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
	if pool.lastSQL != "" {
		t.Fatal("an unknown destination kind must never reach the database")
	}
}
