package service_test

import (
	"context"
	"strings"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
)

// Link classification runs on the body that is persisted (issue #807 CQ round
// 4): mentions are resolved and rewritten first, and that final text is what
// is classified, fingerprinted and stored. A URL a client typed as a mention
// *label* is replaced by the canonical label and must never become a target —
// no scan, no association, no entity, no aggregate — and the fingerprint must
// bind exactly the text every later read re-scans.
//
// The inverse case cannot be built: a canonical label is a display name or a
// channel name, which the codec never lets contain a scheme, so a rewrite can
// only remove URL-looking text, never introduce it. The realistic case is the
// one tested.

const (
	mentionedID = "11111111-1111-1111-1111-111111111111"
	phantomURL  = "https://phantom.example/x"
	realURL     = "https://real.example/doc"
)

func phantomBody() string {
	return `@[` + phantomURL + `](mention:user:` + mentionedID + `) veja ` + realURL
}

func canonicalBody() string {
	return `@[Alice](mention:user:` + mentionedID + `) veja ` + realURL
}

// assertClassifiedFromPersistedBody checks what one write recorded: the body
// stored, the targets associated, the scans admitted and the fingerprint.
func assertClassifiedFromPersistedBody(t *testing.T, store *fakeMessageStore, body string, urls []string, fingerprint string) {
	t.Helper()
	if body != canonicalBody() {
		t.Fatalf("persisted body = %q, want the canonical %q", body, canonicalBody())
	}
	if len(urls) != 1 || urls[0] != realURL {
		t.Fatalf("associated targets = %v, want only %s", urls, realURL)
	}
	for _, url := range store.ensuredURLs {
		if strings.Contains(url, "phantom") {
			t.Fatalf("a scan was admitted for the phantom label: %v", store.ensuredURLs)
		}
	}
	if want := service.ExportLinkSafetyFingerprint(body, urls); fingerprint != want {
		t.Fatalf("fingerprint was not derived from the persisted body")
	}
}

func TestCreateChannelMessageClassifiesLinksAfterMentionRewrite(t *testing.T) {
	store := safetyStore(nil)
	store.mentionLabels = map[string]string{"user:" + mentionedID: "Alice"}
	svc, _ := messageServiceWith(store)

	if _, err := svc.CreateChannelMessage(context.Background(), service.CreateChannelMessageInput{
		WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1,
		BodyText: phantomBody(), BodyFormat: domain.MessageBodyFormatV3,
	}); err != nil {
		t.Fatalf("CreateChannelMessage: %v", err)
	}
	in := store.lastCreateInput
	assertClassifiedFromPersistedBody(t, store, in.BodyText, in.LinkScanURLs, in.LinkSafetyFingerprint)
	if in.LinkSafetyState != domain.MessageLinkSafetyNone {
		t.Fatalf("aggregate = %q, want the pending real link only", in.LinkSafetyState)
	}
}

func TestCreateDMMessageClassifiesLinksAfterMentionRewrite(t *testing.T) {
	store := safetyStore(nil)
	store.createdMessage = domain.Message{ID: "msg-group", WorkspaceID: "ws-1", DMConversationID: "group-1", SenderID: user1}
	store.authorizedMentionLabels = map[string]string{"user:" + mentionedID: "Alice"}
	conversation := domain.DMConversation{
		ID: "group-1", WorkspaceID: "ws-1", Type: domain.DMConversationTypeGroup,
		Status: domain.DMConversationStatusActive,
	}
	svc := service.NewMessageService(&fakeChannelStore{}, &fakeDMStore{visibleConversation: conversation}, store)
	svc.SetLinkSafety(store)

	if _, err := svc.CreateDMMessage(context.Background(), service.CreateDMMessageInput{
		WorkspaceID: "ws-1", ConversationID: "group-1", SenderID: user1,
		BodyText: phantomBody(), BodyFormat: domain.MessageBodyFormatV3,
	}); err != nil {
		t.Fatalf("CreateDMMessage: %v", err)
	}
	in := store.lastCreateInput
	assertClassifiedFromPersistedBody(t, store, in.BodyText, in.LinkScanURLs, in.LinkSafetyFingerprint)
	if got := in.MentionedUserIDs; len(got) != 1 || got[0] != mentionedID {
		t.Fatalf("mention resolution regressed: %v", got)
	}
}

func TestEditMessageClassifiesLinksAfterMentionRewrite(t *testing.T) {
	const oldURL = "https://old.example/gone"
	store := safetyStore(nil)
	store.messagesByKey = map[string]domain.Message{"ws-1:msg-1": {
		ID: "msg-1", WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1,
		Kind: domain.MessageKindUser, Status: domain.MessageStatusActive,
		BodyText: "antes " + oldURL, LinkSafety: domain.MessageLinkSafetySafe,
	}}
	store.authorizedMentionLabels = map[string]string{"user:" + mentionedID: "Alice"}
	svc, _ := messageServiceWith(store)

	if _, err := svc.EditMessage(context.Background(), service.EditMessageInput{
		WorkspaceID: "ws-1", MessageID: "msg-1", EditorID: user1,
		Body: phantomBody(), BodyFormat: domain.MessageBodyFormatV3,
	}); err != nil {
		t.Fatalf("EditMessage: %v", err)
	}
	in := store.lastEditInput
	assertClassifiedFromPersistedBody(t, store, in.Body, in.LinkScanURLs, in.LinkSafetyFingerprint)
	for _, url := range in.LinkScanURLs {
		if url == oldURL {
			t.Fatal("the edit kept an association for a URL the new body no longer names")
		}
	}
}

// Occurrences are re-derived from the persisted body at read time, so their
// spans follow the rewritten text: a label that shrinks or grows before a real
// URL moves the URL, and the entity still names it exactly once.
func TestMentionRewriteDoesNotMoveOrDuplicateLinkOccurrences(t *testing.T) {
	for name, label := range map[string]string{
		"label shrinks": "https://a-much-longer-phantom.example/path",
		"label grows":   "x",
	} {
		t.Run(name, func(t *testing.T) {
			store := safetyStore(nil)
			store.mentionLabels = map[string]string{"user:" + mentionedID: "Alice"}
			svc, _ := messageServiceWith(store)
			body := `@[` + label + `](mention:user:` + mentionedID + `) e ` + realURL + ` fim`
			if _, err := svc.CreateChannelMessage(context.Background(), service.CreateChannelMessageInput{
				WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1,
				BodyText: body, BodyFormat: domain.MessageBodyFormatV3,
			}); err != nil {
				t.Fatalf("CreateChannelMessage: %v", err)
			}
			in := store.lastCreateInput
			want := `@[Alice](mention:user:` + mentionedID + `) e ` + realURL + ` fim`
			if in.BodyText != want || len(in.LinkScanURLs) != 1 || in.LinkScanURLs[0] != realURL {
				t.Fatalf("body = %q targets = %v", in.BodyText, in.LinkScanURLs)
			}
			if strings.Count(in.BodyText, realURL) != 1 || strings.Contains(in.BodyText, "mention:user:"+mentionedID+")") == false {
				t.Fatalf("rewritten body = %q", in.BodyText)
			}
		})
	}
}
