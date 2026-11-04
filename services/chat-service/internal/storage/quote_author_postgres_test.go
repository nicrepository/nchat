package storage_test

import (
	"context"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// Issue #1099: a quote names its author from the server's own projection, read
// live from auth.users in the same statement as the quote — so a reply to a
// message no client page holds still says who wrote it, and a rename shows on
// the next read. Opt-in like its neighbours: needs CHAT_TEST_DATABASE_URL.
func TestQuoteAuthorDisplayNameProjectionPostgreSQL(t *testing.T) {
	f := newEditFixture(t)
	f.reset(t)
	f.seedMessage(t, editMessage, "Salve", "", "")
	if _, err := f.pool.Exec(f.ctx, `
		INSERT INTO chat.messages
			(id, workspace_id, channel_id, sender_id, kind, body_text, body_format,
			 status, parent_message_id)
		VALUES ($1, $2, $3, $4, 'user', 'respondendo', 'v2', 'active', $5)`,
		quoteMessage, editWorkspace, editChannel, editAuthor, editMessage); err != nil {
		t.Fatalf("seed quoting message: %v", err)
	}
	// The fixture seeds the author with ON CONFLICT DO NOTHING, so the name is
	// set here rather than trusted, and put back once the rename below is done.
	rename := func(name string) {
		if _, err := f.pool.Exec(context.Background(),
			`UPDATE auth.users SET display_name = $2 WHERE id = $1::uuid`, editAuthor, name); err != nil {
			t.Fatalf("rename: %v", err)
		}
	}
	rename("Editor")
	t.Cleanup(func() { rename("Editor") })

	quotedAuthor := func(t *testing.T) (single, listed string) {
		t.Helper()
		got, err := f.store.GetMessageByIDInWorkspace(f.ctx, editWorkspace, quoteMessage, editAuthor)
		if err != nil || got.Quoted == nil {
			t.Fatalf("GetMessageByIDInWorkspace: %+v / %v", got.Quoted, err)
		}
		page, err := f.store.ListChannelMessages(f.ctx, storage.ListChannelMessagesInput{
			WorkspaceID: editWorkspace, ChannelID: editChannel, UserID: editAuthor,
		})
		if err != nil {
			t.Fatalf("ListChannelMessages: %v", err)
		}
		for _, msg := range page.Messages {
			if msg.ID == quoteMessage && msg.Quoted != nil {
				return got.Quoted.AuthorDisplayName, msg.Quoted.AuthorDisplayName
			}
		}
		t.Fatal("the quoting message was not listed with its quote")
		return "", ""
	}

	if single, listed := quotedAuthor(t); single != "Editor" || listed != "Editor" {
		t.Fatalf("quoted author = %q / %q, want Editor from both reads", single, listed)
	}

	rename("Editor Renomeado")
	if single, listed := quotedAuthor(t); single != "Editor Renomeado" || listed != "Editor Renomeado" {
		t.Fatalf("quoted author after rename = %q / %q, want the live name", single, listed)
	}
}
