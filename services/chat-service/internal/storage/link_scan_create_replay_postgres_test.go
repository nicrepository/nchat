package storage_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// Creating a withheld message, and retrying that creation, against a real
// database.
//
// RF-21 made idempotency matter rather than merely nice: a withheld message is
// invisible to everyone but its author, so a client with a dropped response has
// every reason to send again — and without a key it would get a second withheld
// message and a second billed scan. The control is a partial unique index, and
// the branch that reads it is reached only by an actual unique violation, which
// is why this cannot be a fake: the store would have to invent the constraint
// name it is supposed to be recognising.
//
// Opt-in like its neighbours: needs CHAT_TEST_DATABASE_URL against a _test
// database carrying the real migrations.
func TestLinkScanCreateReplayPostgreSQL(t *testing.T) {
	ctx := t.Context()
	pool := newLinkScanTestPool(t)
	store := storage.NewPGXMessageStore(pool)

	const (
		workspace    = "00000000-0000-0000-0000-000000000001"
		channel      = "e4000000-0000-4000-8000-000000000002"
		conversation = "e4000000-0000-4000-8000-000000000003"
		author       = "e4000000-0000-4000-8000-000000000004"
		other        = "e4000000-0000-4000-8000-000000000005"
		destination  = "e4000000-0000-4000-8000-000000000006"

		scannedURL  = "https://replay.example/article"
		fingerprint = "fp-replay"
		// The identity of the whole operation, stored beside the key so a replay
		// can be told from a key reused for different content.
		requestFingerprint = "req-replay"
	)

	if _, err := pool.Exec(ctx, `
		INSERT INTO auth.users (id, email, display_name)
		VALUES ($1, 'rf21-replay-author@e.test', 'Author'),
		       ($2, 'rf21-replay-other@e.test', 'Other')
		ON CONFLICT (id) DO NOTHING`, author, other); err != nil {
		t.Fatalf("seed users: %v", err)
	}
	for _, seed := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO chat.workspace_members (workspace_id, user_id, status)
		  VALUES ($1, $2, 'active'), ($1, $3, 'active') ON CONFLICT DO NOTHING`,
			[]any{workspace, author, other}},
		{`INSERT INTO chat.channels (id, workspace_id, slug, display_name, type, status)
		  VALUES ($2, $1, 'rf21-replay', 'RF21 replay', 'public', 'active'),
		         ($3, $1, 'rf21-replay-destination', 'RF21 replay destination', 'public', 'active')
		  ON CONFLICT (id) DO NOTHING`, []any{workspace, channel, destination}},
		// A mention is only valid for somebody who is in the channel, so the
		// membership is part of the fixture rather than an assumption.
		{`INSERT INTO chat.channel_members (channel_id, user_id)
		  VALUES ($1, $2), ($1, $3) ON CONFLICT DO NOTHING`,
			[]any{channel, author, other}},
		{`INSERT INTO chat.dm_conversations
		      (id, workspace_id, type, status, created_by, direct_pair_key)
		  VALUES ($2, $1, 'direct', 'active', $3, 'rf21-replay-pair')
		  ON CONFLICT (id) DO NOTHING`, []any{workspace, conversation, author}},
		{`INSERT INTO chat.dm_members (conversation_id, user_id, status)
		  VALUES ($1, $2, 'active'), ($1, $3, 'active') ON CONFLICT DO NOTHING`,
			[]any{conversation, author, other}},
	} {
		if _, err := pool.Exec(ctx, seed.sql, seed.args...); err != nil {
			t.Fatalf("seed workspace: %v", err)
		}
	}
	t.Cleanup(func() {
		background := context.Background()
		_, _ = pool.Exec(background, `DELETE FROM chat.messages WHERE sender_id = ANY($1::uuid[])`,
			[]string{author, other})
		_, _ = pool.Exec(background,
			`DELETE FROM chat.link_scans WHERE canonical_url = $1`, scannedURL)
		_, _ = pool.Exec(background, `DELETE FROM chat.channel_members WHERE channel_id = $1`, channel)
		_, _ = pool.Exec(background, `DELETE FROM chat.dm_members WHERE conversation_id = $1`, conversation)
		_, _ = pool.Exec(background, `DELETE FROM chat.dm_conversations WHERE id = $1`, conversation)
	})

	if err := store.EnsureLinkScans(ctx, []string{scannedURL}); err != nil {
		t.Fatalf("EnsureLinkScans: %v", err)
	}

	withheld := storage.CreateMessageInput{
		WorkspaceID:           workspace,
		ChannelID:             channel,
		SenderID:              author,
		BodyText:              "veja " + scannedURL,
		BodyFormat:            domain.MessageBodyFormatV2,
		Status:                domain.MessageStatusPendingLinkScan,
		MentionedUserIDs:      []string{other},
		LinkScanURLs:          []string{scannedURL},
		LinkSafetyFingerprint: fingerprint,
		IdempotencyKey:        "key-withheld",
		RequestFingerprint:    requestFingerprint,
	}

	var created domain.Message

	// [§28] The message, the URLs it waits on and its parked mentions are one
	// atomic fact. A commit holding the message without its links would be a
	// message nothing could ever promote.
	t.Run("a withheld message is written with its links and its mentions parked", func(t *testing.T) {
		var err error
		created, err = store.CreateMessage(ctx, withheld)
		if err != nil {
			t.Fatalf("CreateMessage: %v", err)
		}
		if created.Status != domain.MessageStatusPendingLinkScan {
			t.Fatalf("status = %q, want the message withheld", created.Status)
		}

		var url, storedFingerprint string
		if err := pool.QueryRow(ctx, `
			SELECT canonical_url, fingerprint FROM chat.message_link_scans
			WHERE message_id = $1`, created.ID).Scan(&url, &storedFingerprint); err != nil {
			t.Fatalf("read association: %v", err)
		}
		if url != scannedURL || storedFingerprint != fingerprint {
			t.Fatalf("association = %s/%s, want the scanned url bound to its content",
				url, storedFingerprint)
		}

		// The rule RF-21 exists to enforce: a withheld message produces no side
		// effect aimed at anyone else. The mention is parked, not delivered.
		var parked, notified int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM chat.message_pending_mentions WHERE message_id = $1`,
			created.ID).Scan(&parked); err != nil {
			t.Fatalf("count parked mentions: %v", err)
		}
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM chat.notification_outbox WHERE message_id = $1`,
			created.ID).Scan(&notified); err != nil {
			t.Fatalf("count notifications: %v", err)
		}
		if parked != 1 || notified != 0 {
			t.Fatalf("parked=%d notified=%d, want the mention held back", parked, notified)
		}
	})

	// The collision the partial unique index produces, and the branch that has to
	// tell it apart from every other unique violation.
	t.Run("the same key does not create a second message", func(t *testing.T) {
		_, err := store.CreateMessage(ctx, withheld)
		if !errors.Is(err, storage.ErrCreateReplay) {
			t.Fatalf("CreateMessage on a reused key = %v, want ErrCreateReplay", err)
		}
		var count int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM chat.messages
			WHERE sender_id = $1 AND create_idempotency_key = $2`,
			author, withheld.IdempotencyKey).Scan(&count); err != nil {
			t.Fatalf("count messages: %v", err)
		}
		if count != 1 {
			t.Fatalf("%d messages carry the key, want the retry to have created none", count)
		}
	})

	// What the caller does with that: read back what the winner wrote, including
	// the fingerprint it compares against to tell a replay from a reuse.
	t.Run("the original is returned to the caller that retried", func(t *testing.T) {
		replay, err := store.LookupCreateReplay(ctx, storage.CreateReplayInput{
			WorkspaceID: workspace, ChannelID: channel, SenderID: author,
			IdempotencyKey: withheld.IdempotencyKey, RequestFingerprint: requestFingerprint,
		})
		if err != nil {
			t.Fatalf("LookupCreateReplay: %v", err)
		}
		if replay.ID != created.ID {
			t.Fatalf("replay id = %q, want the original %q", replay.ID, created.ID)
		}
		if replay.CreateFingerprint != requestFingerprint {
			t.Fatalf("fingerprint = %q, want %q", replay.CreateFingerprint, requestFingerprint)
		}
		if replay.Status != domain.MessageStatusPendingLinkScan {
			t.Fatalf("status = %q, want the withheld original", replay.Status)
		}
	})

	// The scoping is the security property: presenting somebody else's key, or
	// the right key against the wrong destination, matches nothing.
	t.Run("a key does not replay across senders or destinations", func(t *testing.T) {
		for name, input := range map[string]storage.CreateReplayInput{
			"another sender": {
				WorkspaceID: workspace, ChannelID: channel, SenderID: other,
				IdempotencyKey: withheld.IdempotencyKey,
			},
			"another destination": {
				WorkspaceID: workspace, DMConversationID: conversation, SenderID: author,
				IdempotencyKey: withheld.IdempotencyKey,
			},
			"no key at all": {
				WorkspaceID: workspace, ChannelID: channel, SenderID: author,
				IdempotencyKey: "   ",
			},
		} {
			if _, err := store.LookupCreateReplay(ctx, input); !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("%s: %v, want ErrNotFound", name, err)
			}
		}
	})

	// The contrast that gives the withholding its meaning: the same send without a
	// link is created active, and an active message notifies its mentions at once.
	t.Run("a message carrying no link notifies its mentions immediately", func(t *testing.T) {
		published, err := store.CreateMessage(ctx, storage.CreateMessageInput{
			WorkspaceID:      workspace,
			ChannelID:        channel,
			SenderID:         author,
			BodyText:         "sem links",
			BodyFormat:       domain.MessageBodyFormatV2,
			MentionedUserIDs: []string{other},
			IdempotencyKey:   "key-active",
		})
		if err != nil {
			t.Fatalf("CreateMessage: %v", err)
		}
		// No status supplied means active, never the zero value of a string
		// reaching the CHECK constraint.
		if published.Status != domain.MessageStatusActive {
			t.Fatalf("status = %q, want an ordinary active message", published.Status)
		}
		var notified, parked int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM chat.notification_outbox WHERE message_id = $1`,
			published.ID).Scan(&notified); err != nil {
			t.Fatalf("count notifications: %v", err)
		}
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM chat.message_pending_mentions WHERE message_id = $1`,
			published.ID).Scan(&parked); err != nil {
			t.Fatalf("count parked mentions: %v", err)
		}
		if notified != 1 || parked != 0 {
			t.Fatalf("notified=%d parked=%d, want the mention delivered at once", notified, parked)
		}
	})

	t.Run("a cached-safe message stores its authoritative marker", func(t *testing.T) {
		published, err := store.CreateMessage(ctx, storage.CreateMessageInput{
			WorkspaceID: workspace, ChannelID: channel, SenderID: author,
			BodyText: "veja " + scannedURL, BodyFormat: domain.MessageBodyFormatV2,
			LinkScanURLs: []string{scannedURL}, LinkSafetyFingerprint: fingerprint,
			LinkSafetyState: domain.MessageLinkSafetySafe,
			IdempotencyKey:  "key-cached-safe",
		})
		if err != nil {
			t.Fatalf("CreateMessage: %v", err)
		}
		if published.Status != domain.MessageStatusActive ||
			published.LinkSafety != domain.MessageLinkSafetySafe {
			t.Fatalf("message=%q/%q, want active/safe",
				published.Status, published.LinkSafety)
		}

		forwarded, err := store.ForwardChannelMessage(ctx, storage.ForwardChannelMessageInput{
			WorkspaceID: workspace, DestinationChannelID: destination, ActorID: author,
			SourceMessageID: published.ID, IdempotencyKey: "forward-cached-safe",
			BodyText: published.BodyText, BodyFormat: published.BodyFormat,
			LinkScanURLs: []string{scannedURL}, LinkSafetyFingerprint: fingerprint,
			LinkSafetyState: domain.MessageLinkSafetySafe,
		})
		if err != nil {
			t.Fatalf("ForwardChannelMessage: %v", err)
		}
		if forwarded.Message.Status != domain.MessageStatusActive ||
			forwarded.Message.LinkSafety != domain.MessageLinkSafetySafe {
			t.Fatalf("forward=%q/%q, want active/safe",
				forwarded.Message.Status, forwarded.Message.LinkSafety)
		}
	})

	// The other half of the destination identity. A DM key is keyed by the
	// conversation, through the same COALESCE the index uses.
	t.Run("a dm send is replayed against its conversation", func(t *testing.T) {
		direct, err := store.CreateMessage(ctx, storage.CreateMessageInput{
			WorkspaceID:      workspace,
			DMConversationID: conversation,
			SenderID:         author,
			BodyText:         "oi",
			BodyFormat:       domain.MessageBodyFormatV2,
			IdempotencyKey:   "key-dm",
		})
		if err != nil {
			t.Fatalf("CreateMessage (dm): %v", err)
		}

		replay, err := store.LookupCreateReplay(ctx, storage.CreateReplayInput{
			WorkspaceID: workspace, DMConversationID: conversation, SenderID: author,
			IdempotencyKey: "key-dm",
		})
		if err != nil {
			t.Fatalf("LookupCreateReplay (dm): %v", err)
		}
		if replay.ID != direct.ID {
			t.Fatalf("replay id = %q, want %q", replay.ID, direct.ID)
		}
	})

	// A key nobody has used is not a replay, and must not be reported as one.
	t.Run("an unused key finds nothing", func(t *testing.T) {
		_, err := store.LookupCreateReplay(ctx, storage.CreateReplayInput{
			WorkspaceID: workspace, ChannelID: channel, SenderID: author,
			IdempotencyKey: "key-never-used",
		})
		if !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("LookupCreateReplay = %v, want ErrNotFound", err)
		}
	})
}
