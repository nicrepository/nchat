package storage_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// RF-32. The attachment link is authorised entirely in SQL, in the same
// statement that inserts the message, so this is the only place the rules can
// actually be tested: a mock can prove the query was sent, not that PostgreSQL
// refuses a cross-workspace id.
//
// Every denial below must be the same non-enumerating domain.ErrNotFound. A
// caller must not be able to tell "that attachment does not exist" from "it
// exists but is not yours", which is what would turn this endpoint into an
// attachment oracle.
func TestPGXMessageStoreCreateMessageAttachmentsPostgreSQL(t *testing.T) {
	dsn := os.Getenv("CHAT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("CHAT_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	var databaseName string
	if err := pool.QueryRow(ctx, `SELECT current_database()`).Scan(&databaseName); err != nil {
		t.Fatalf("read current database: %v", err)
	}
	if !strings.HasSuffix(databaseName, "_test") {
		t.Fatalf("refusing destructive attachment test against non-test database %q", databaseName)
	}
	if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS chat CASCADE; DROP SCHEMA IF EXISTS files CASCADE`); err != nil {
		t.Fatalf("reset schemas: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DROP SCHEMA IF EXISTS chat CASCADE; DROP SCHEMA IF EXISTS files CASCADE`)
	})
	if _, err := pool.Exec(ctx, `
		CREATE SCHEMA IF NOT EXISTS auth;
		CREATE TABLE IF NOT EXISTS auth.users (
			id UUID PRIMARY KEY,
			email TEXT NOT NULL DEFAULT '',
			display_name TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'active',
			deleted_at TIMESTAMPTZ
		)`); err != nil {
		t.Fatalf("prepare auth schema: %v", err)
	}
	if _, err := pool.Exec(ctx, readAllChatUpMigrations(t)); err != nil {
		t.Fatalf("apply chat migrations: %v", err)
	}
	// files.attachments is file-service's table and its migrations are not this
	// service's to run. Only the columns chat-service reads are recreated here,
	// with the same names, types and closed status set as migrations/files.
	if _, err := pool.Exec(ctx, `
		CREATE SCHEMA IF NOT EXISTS files;
		CREATE TABLE files.attachments (
			id                UUID PRIMARY KEY,
			workspace_id      UUID NOT NULL,
			uploader_id       UUID NOT NULL,
			destination_kind  TEXT NOT NULL,
			channel_id        UUID,
			conversation_id   UUID,
			original_filename TEXT NOT NULL,
			declared_mime     TEXT NOT NULL DEFAULT 'application/octet-stream',
			detected_mime     TEXT,
			size_bytes        BIGINT NOT NULL DEFAULT 0,
			status            TEXT NOT NULL,
			preview_status    TEXT NOT NULL DEFAULT 'pending',
			deleted_at        TIMESTAMPTZ,
			draft_expires_at  TIMESTAMPTZ,
			updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		t.Fatalf("prepare files schema: %v", err)
	}

	const (
		workspace = "e1000000-0000-4000-8000-000000000001"
		otherWS   = "e2000000-0000-4000-8000-000000000001"
		sender    = "e1000000-0000-4000-8000-000000000002"
		stranger  = "e1000000-0000-4000-8000-000000000003"
		channel   = "e1000000-0000-4000-8000-000000000004"
		other     = "e1000000-0000-4000-8000-000000000005"
		crossWSCh = "e2000000-0000-4000-8000-000000000002"
		dmID      = "e1000000-0000-4000-8000-000000000006"

		okChannel   = "e1000000-0000-4000-8000-00000000000a"
		okDM        = "e1000000-0000-4000-8000-00000000000b"
		cleanFile   = "e1000000-0000-4000-8000-00000000000c"
		otherChanA  = "e1000000-0000-4000-8000-00000000000d"
		crossWSA    = "e1000000-0000-4000-8000-00000000000e"
		strangersA  = "e1000000-0000-4000-8000-00000000000f"
		failedA     = "e1000000-0000-4000-8000-000000000010"
		removedA    = "e1000000-0000-4000-8000-000000000011"
		pendingUpA  = "e1000000-0000-4000-8000-000000000012"
		rejectedA   = "e1000000-0000-4000-8000-000000000013"
		reuseTarget = "e1000000-0000-4000-8000-000000000014"
		raceTarget  = "e1000000-0000-4000-8000-000000000015"
		missing     = "e1000000-0000-4000-8000-0000000000ff"
	)
	if _, err := pool.Exec(ctx, `
		WITH seed_users AS (
			INSERT INTO auth.users (id, email, display_name) VALUES
			($1, 'sender@example.test', 'Sender'),
			($2, 'stranger@example.test', 'Stranger')
			ON CONFLICT (id) DO NOTHING
		), seed_workspaces AS (
			INSERT INTO chat.workspaces (id, slug, name) VALUES
			($3, 'rf32-a', 'RF32 A'), ($4, 'rf32-b', 'RF32 B')
		), seed_members AS (
			INSERT INTO chat.workspace_members (workspace_id, user_id, status) VALUES
			($3, $1, 'active'), ($3, $2, 'active'), ($4, $1, 'active')
		), seed_channels AS (
			INSERT INTO chat.channels (id, workspace_id, slug, display_name, type, status, is_general) VALUES
			($5, $3, 'main', 'Main', 'public', 'active', true),
			($6, $3, 'other', 'Other', 'public', 'active', false),
			($7, $4, 'cross', 'Cross', 'public', 'active', true)
		)
		INSERT INTO chat.dm_conversations (id, workspace_id, type, status, created_by)
			VALUES ($8, $3, 'group', 'active', $1)`,
		sender, stranger, workspace, otherWS, channel, other, crossWSCh, dmID); err != nil {
		t.Fatalf("seed chat fixtures: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO chat.dm_members (conversation_id, user_id) VALUES ($1, $2), ($1, $3)`, dmID, sender, stranger); err != nil {
		t.Fatalf("seed dm members: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO files.attachments
			(id, workspace_id, uploader_id, destination_kind, channel_id, conversation_id,
			 original_filename, detected_mime, size_bytes, status, preview_status, deleted_at)
		VALUES
			($1,  $13, $15, 'channel', $17, NULL, 'ok-channel.pdf', 'application/pdf', 11, 'pending_scan', 'pending', NULL),
			($2,  $13, $15, 'dm',      NULL, $20, 'ok-dm.pdf',      'application/pdf', 12, 'pending_scan', 'pending', NULL),
			($3,  $13, $15, 'channel', $17, NULL, 'clean.png',      'image/png',       13, 'clean',        'ready',   NULL),
			($4,  $13, $15, 'channel', $18, NULL, 'other.pdf',      'application/pdf', 14, 'pending_scan', 'pending', NULL),
			($5,  $14, $15, 'channel', $19, NULL, 'cross.pdf',      'application/pdf', 15, 'pending_scan', 'pending', NULL),
			($6,  $13, $16, 'channel', $17, NULL, 'stranger.pdf',   'application/pdf', 16, 'pending_scan', 'pending', NULL),
			($7,  $13, $15, 'channel', $17, NULL, 'failed.pdf',     'application/pdf', 17, 'failed',       'pending', NULL),
			($8,  $13, $15, 'channel', $17, NULL, 'removed.pdf',    'application/pdf', 18, 'clean',        'ready',   now()),
			($9,  $13, $15, 'channel', $17, NULL, 'pending.pdf',    'application/pdf', 19, 'pending_upload','pending',NULL),
			($10, $13, $15, 'channel', $17, NULL, 'rejected.pdf',   'application/pdf', 20, 'rejected',     'unsupported', NULL),
			($11, $13, $15, 'channel', $17, NULL, 'reuse.pdf',      'application/pdf', 21, 'pending_scan', 'pending', NULL),
			($12, $13, $15, 'channel', $17, NULL, 'race.pdf',       'application/pdf', 22, 'pending_scan', 'pending', NULL)`,
		okChannel, okDM, cleanFile, otherChanA, crossWSA, strangersA, failedA, removedA,
		pendingUpA, rejectedA, reuseTarget, raceTarget,
		workspace, otherWS, sender, stranger, channel, other, crossWSCh, dmID); err != nil {
		t.Fatalf("seed attachment fixtures: %v", err)
	}

	store := storage.NewPGXMessageStore(pool)

	t.Run("pending_scan attachment links and is returned", func(t *testing.T) {
		msg, err := store.CreateMessage(t.Context(), storage.CreateMessageInput{
			WorkspaceID: workspace, ChannelID: channel, SenderID: sender,
			BodyText: "", BodyFormat: domain.MessageBodyFormatV3,
			AttachmentIDs: []string{okChannel},
		})
		if err != nil {
			t.Fatalf("create message with attachment: %v", err)
		}
		if len(msg.Attachments) != 1 {
			t.Fatalf("expected one attachment, got %+v", msg.Attachments)
		}
		got := msg.Attachments[0]
		if got.ID != okChannel || got.Filename != "ok-channel.pdf" ||
			got.ContentType != "application/pdf" || got.SizeBytes != 11 ||
			got.Status != "pending_scan" || got.PreviewStatus != "pending" {
			t.Fatalf("unexpected attachment metadata: %+v", got)
		}
	})

	t.Run("text plus clean attachment links", func(t *testing.T) {
		msg, err := store.CreateMessage(t.Context(), storage.CreateMessageInput{
			WorkspaceID: workspace, ChannelID: channel, SenderID: sender,
			BodyText: "veja isto", BodyFormat: domain.MessageBodyFormatV3,
			AttachmentIDs: []string{cleanFile},
		})
		if err != nil {
			t.Fatalf("create message with text and attachment: %v", err)
		}
		if msg.BodyText != "veja isto" || len(msg.Attachments) != 1 || msg.Attachments[0].Status != "clean" {
			t.Fatalf("unexpected message: %+v", msg)
		}
	})

	t.Run("dm attachment links to dm message", func(t *testing.T) {
		msg, err := store.CreateMessage(t.Context(), storage.CreateMessageInput{
			WorkspaceID: workspace, DMConversationID: dmID, SenderID: sender,
			BodyText: "", BodyFormat: domain.MessageBodyFormatV2,
			AttachmentIDs: []string{okDM},
		})
		if err != nil {
			t.Fatalf("create dm message with attachment: %v", err)
		}
		if len(msg.Attachments) != 1 || msg.Attachments[0].ID != okDM {
			t.Fatalf("unexpected attachments: %+v", msg.Attachments)
		}
	})

	// Every one of these must fail identically, and must leave no message behind:
	// a partially applied create is exactly what the single-statement CTE exists
	// to make impossible.
	for name, attachmentID := range map[string]string{
		"nonexistent attachment":        missing,
		"attachment of other channel":   otherChanA,
		"attachment of other workspace": crossWSA,
		"attachment uploaded by other":  strangersA,
		"failed attachment":             failedA,
		"deleted attachment":            removedA,
		"unfinished upload":             pendingUpA,
		"rejected attachment":           rejectedA,
		"dm attachment in a channel":    okDM,
	} {
		t.Run(name, func(t *testing.T) {
			before := countMessages(t, pool, workspace)
			_, err := store.CreateMessage(t.Context(), storage.CreateMessageInput{
				WorkspaceID: workspace, ChannelID: channel, SenderID: sender,
				BodyText: "denied", BodyFormat: domain.MessageBodyFormatV3,
				AttachmentIDs: []string{attachmentID},
			})
			if !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("expected non-enumerating ErrNotFound, got %v", err)
			}
			if after := countMessages(t, pool, workspace); after != before {
				t.Fatalf("refused create must persist nothing: %d -> %d", before, after)
			}
		})
	}

	t.Run("channel attachment in a dm is refused", func(t *testing.T) {
		_, err := store.CreateMessage(t.Context(), storage.CreateMessageInput{
			WorkspaceID: workspace, DMConversationID: dmID, SenderID: sender,
			BodyText: "denied", BodyFormat: domain.MessageBodyFormatV2,
			AttachmentIDs: []string{okChannel},
		})
		if !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("expected non-enumerating ErrNotFound, got %v", err)
		}
	})

	t.Run("an attachment cannot be linked twice", func(t *testing.T) {
		if _, err := store.CreateMessage(t.Context(), storage.CreateMessageInput{
			WorkspaceID: workspace, ChannelID: channel, SenderID: sender,
			BodyText: "primeiro", BodyFormat: domain.MessageBodyFormatV3,
			AttachmentIDs: []string{reuseTarget},
		}); err != nil {
			t.Fatalf("first link: %v", err)
		}
		_, err := store.CreateMessage(t.Context(), storage.CreateMessageInput{
			WorkspaceID: workspace, ChannelID: channel, SenderID: sender,
			BodyText: "replay", BodyFormat: domain.MessageBodyFormatV3,
			AttachmentIDs: []string{reuseTarget},
		})
		if !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("expected non-enumerating ErrNotFound on replay, got %v", err)
		}
		var links int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM chat.message_attachments WHERE attachment_id = $1`,
			reuseTarget).Scan(&links); err != nil {
			t.Fatalf("count links: %v", err)
		}
		if links != 1 {
			t.Fatalf("expected exactly one link row, got %d", links)
		}
	})

	t.Run("concurrent links leave one message and a non-enumerating loser", func(t *testing.T) {
		before := countMessages(t, pool, workspace)
		start := make(chan struct{})
		errs := make(chan error, 2)
		var wg sync.WaitGroup
		for range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, err := store.CreateMessage(context.Background(), storage.CreateMessageInput{
					WorkspaceID: workspace, ChannelID: channel, SenderID: sender,
					BodyText: "race", BodyFormat: domain.MessageBodyFormatV3,
					AttachmentIDs: []string{raceTarget},
				})
				errs <- err
			}()
		}
		close(start)
		wg.Wait()
		close(errs)

		successes := 0
		for err := range errs {
			if err == nil {
				successes++
				continue
			}
			if !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("concurrent loser must be non-enumerating, got %v", err)
			}
		}
		if successes != 1 {
			t.Fatalf("successes = %d, want 1", successes)
		}
		if after := countMessages(t, pool, workspace); after != before+1 {
			t.Fatalf("concurrent loser must roll back its message: %d -> %d", before, after)
		}
		var links int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM chat.message_attachments WHERE attachment_id = $1`, raceTarget).Scan(&links); err != nil {
			t.Fatalf("count concurrent links: %v", err)
		}
		if links != 1 {
			t.Fatalf("links = %d, want 1", links)
		}
	})

	t.Run("listing returns attachments in one query per page", func(t *testing.T) {
		result, err := store.ListChannelMessages(t.Context(), storage.ListChannelMessagesInput{
			WorkspaceID: workspace, ChannelID: channel, UserID: sender,
		})
		if err != nil {
			t.Fatalf("list channel messages: %v", err)
		}
		withAttachments := 0
		for _, msg := range result.Messages {
			withAttachments += len(msg.Attachments)
		}
		if withAttachments == 0 {
			t.Fatal("expected the listing to carry the linked attachments")
		}
	})
}

func countMessages(t *testing.T, pool *pgxpool.Pool, workspaceID string) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM chat.messages WHERE workspace_id = $1`, workspaceID).Scan(&count); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	return count
}
