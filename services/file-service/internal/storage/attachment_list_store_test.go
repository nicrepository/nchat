package storage_test

import (
	"context"
	"errors"
	"fmt"
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

			// Both kinds share the argument order, so only the SQL varies. The
			// limit is one past the page: the probe row that proves a next page.
			if pool.lastArgs[0] != testWorkspaceID || pool.lastArgs[1] != tt.destinationID ||
				pool.lastArgs[3] != 6 || len(pool.lastArgs) != 4 {
				t.Fatalf("unexpected arguments: %v", pool.lastArgs)
			}
			wantStatuses := []string{
				string(domain.StatusPendingScan), string(domain.StatusClean), string(domain.StatusRejected),
			}
			if !reflect.DeepEqual(pool.lastArgs[2], wantStatuses) {
				t.Fatalf("expected the listable status set %v, got %v", wantStatuses, pool.lastArgs[2])
			}
			if len(got.Attachments) != 1 || got.Attachments[0].ID != "a-1" || got.Next != nil {
				t.Fatalf("unexpected page: %+v", got)
			}
			// Without a cursor the statement carries no position at all.
			if strings.Contains(pool.lastSQL, "$5") {
				t.Fatalf("a first page must not bind a cursor:\n%s", pool.lastSQL)
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

	page, err := storage.NewPGXAttachmentStore(pool).
		ListDestinationAttachments(context.Background(), listQuery(5))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := page.Attachments

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
			// The clamped page plus the one probe row.
			if pool.lastArgs[3] != tt.want+1 {
				t.Fatalf("expected limit %d, got %v", tt.want+1, pool.lastArgs[3])
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

func TestListDestinationAttachmentsSurfacesQueryFailures(t *testing.T) {
	dbErr := errors.New("connection refused")
	pool := &fakePool{query: func(string, ...any) (pgx.Rows, error) {
		return nil, dbErr
	}}
	page, err := storage.NewPGXAttachmentStore(pool).
		ListDestinationAttachments(context.Background(), listQuery(5))
	if !errors.Is(err, dbErr) || len(page.Attachments) != 0 || page.Next != nil {
		t.Fatalf("page = %+v, error = %v, want the database failure without a page", page, err)
	}
}

func TestListDestinationAttachmentsRejectsAMalformedRowAndClosesRows(t *testing.T) {
	rows := &valueRows{rows: [][]any{{"only", "two"}}}
	pool := &fakePool{query: func(string, ...any) (pgx.Rows, error) {
		return rows, nil
	}}
	page, err := storage.NewPGXAttachmentStore(pool).
		ListDestinationAttachments(context.Background(), listQuery(5))
	if err == nil || len(page.Attachments) != 0 || page.Next != nil || !rows.closed {
		t.Fatalf("page = %+v, error = %v, closed = %v, want failure and cleanup", page, err, rows.closed)
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

// Issue #897: a next page is offered only when the probe row came back, and the
// cursor names the last row actually served — never the probe.
func TestListDestinationAttachmentsPagesWithTheProbeRow(t *testing.T) {
	newest := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	rowsOf := func(n int) [][]any {
		rows := make([][]any, 0, n)
		for i := range n {
			rows = append(rows, attachmentRowValues(
				fmt.Sprintf("00000000-0000-4000-8000-%012d", n-i), string(domain.StatusClean),
				fmt.Sprintf("f-%d.pdf", i), "application/pdf", 1, newest,
			))
		}
		return rows
	}

	for name, tt := range map[string]struct {
		limit    int
		returned int
		wantRows int
		wantNext bool
	}{
		"empty":           {limit: 5, returned: 0, wantRows: 0},
		"one":             {limit: 5, returned: 1, wantRows: 1},
		"below compact":   {limit: 5, returned: 4, wantRows: 4},
		"exactly compact": {limit: 5, returned: 5, wantRows: 5},
		"compact probe":   {limit: 5, returned: 6, wantRows: 5, wantNext: true},
		"exactly default": {limit: 20, returned: 20, wantRows: 20},
		"default probe":   {limit: 20, returned: 21, wantRows: 20, wantNext: true},
		"exactly maximum": {limit: 50, returned: 50, wantRows: 50},
		"maximum probe":   {limit: 50, returned: 51, wantRows: 50, wantNext: true},
	} {
		t.Run(name, func(t *testing.T) {
			rows := rowsOf(tt.returned)
			pool := &fakePool{query: func(string, ...any) (pgx.Rows, error) {
				return &valueRows{rows: rows}, nil
			}}

			page, err := storage.NewPGXAttachmentStore(pool).
				ListDestinationAttachments(context.Background(), listQuery(tt.limit))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if pool.lastArgs[3] != tt.limit+1 {
				t.Fatalf("expected limit plus probe %d, got %v", tt.limit+1, pool.lastArgs[3])
			}
			if len(page.Attachments) != tt.wantRows {
				t.Fatalf("expected %d rows, got %d", tt.wantRows, len(page.Attachments))
			}
			if (page.Next != nil) != tt.wantNext {
				t.Fatalf("next page = %+v, want present=%v", page.Next, tt.wantNext)
			}
			if !tt.wantNext {
				return
			}
			last := page.Attachments[len(page.Attachments)-1]
			if page.Next.ID != last.ID || !page.Next.CreatedAt.Equal(last.CreatedAt) {
				t.Fatalf("the cursor must name the last served row %+v, got %+v", last, page.Next)
			}
		})
	}
}

// The cursor continues strictly after the previous page in the listing's own
// order, as bind parameters, whichever destination kind is listed.
func TestListDestinationAttachmentsContinuesAfterTheCursor(t *testing.T) {
	cursor := domain.AttachmentListCursor{
		CreatedAt: time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC),
		ID:        "0f9c4a61-5d1e-4c1b-9d7e-2a4b6c8d0e1f",
	}
	for _, kind := range []domain.DestinationKind{domain.DestinationKindChannel, domain.DestinationKindDM} {
		t.Run(string(kind), func(t *testing.T) {
			pool := &fakePool{query: func(string, ...any) (pgx.Rows, error) {
				return &valueRows{}, nil
			}}
			query := destinationQuery(kind, testChannelID, 5)
			query.Before = &cursor

			if _, err := storage.NewPGXAttachmentStore(pool).
				ListDestinationAttachments(context.Background(), query); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			predicate := "AND (a.created_at, a.id) < ($5::timestamptz, $6::uuid)"
			predicateAt := strings.Index(pool.lastSQL, predicate)
			orderAt := strings.Index(pool.lastSQL, "ORDER BY a.created_at DESC, a.id DESC")
			if predicateAt < 0 || orderAt < predicateAt {
				t.Fatalf("expected the keyset predicate before the order:\n%s", pool.lastSQL)
			}
			if strings.Contains(pool.lastSQL, " OR ") {
				t.Fatalf("the keyset predicate must stay a row comparison:\n%s", pool.lastSQL)
			}
			if len(pool.lastArgs) != 6 || pool.lastArgs[4] != cursor.CreatedAt || pool.lastArgs[5] != cursor.ID {
				t.Fatalf("the cursor must be bound, never interpolated: %v", pool.lastArgs)
			}
			// The destination binding is unchanged by the cursor.
			if pool.lastArgs[0] != testWorkspaceID || pool.lastArgs[1] != testChannelID {
				t.Fatalf("a cursor must not move the destination binding: %v", pool.lastArgs)
			}
		})
	}
}
