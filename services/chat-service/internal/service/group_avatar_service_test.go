package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
)

// Group identity at the service layer (issue #1026). The service is the
// boundary that validates the emoji: exactly one sequence of the generated
// catalog (#496), compared byte for byte. The authority itself is the store's
// and is proved against PostgreSQL in the storage package.

// Values a client can send that are not one catalogued emoji. None of them may
// reach the store, on creation or on a later change.
var invalidGroupAvatarEmojis = []string{
	"abc",                          // plain text
	"<b>🎉</b>",                     // HTML around a real emoji
	"<svg onload=alert(1)></svg>",  // markup
	`<img src=x onerror=alert(1)>`, // stored-XSS probe
	":tada:",                       // shortcode
	"🎉🎉",                           // two emoji
	"🎉 ",                           // trailing whitespace — never trimmed into validity
	"\u200d",                       // a bare joiner
	"🇿",                            // half a flag: a code point the catalog has no sequence for
	strings.Repeat("🎉", 4096),      // oversized payload
}

func TestDMService_SetGroupAvatar_AcceptsCatalogedSequencesUntouched(t *testing.T) {
	for _, emoji := range []string{"🎉", "👩‍💻", "👍🏽", "🏳️‍🌈", "👨‍👩‍👧"} {
		t.Run(emoji, func(t *testing.T) {
			dms := &fakeDMStore{}
			err := service.NewDMService(dms, &fakeMemberStore{}).SetGroupAvatar(context.Background(), service.GroupAvatarInput{
				WorkspaceID: adminWSID, CallerID: strings.ToUpper(adminCaller), ConversationID: adminGroup, AvatarEmoji: emoji,
			})
			if err != nil {
				t.Fatalf("SetGroupAvatar(%q): %v", emoji, err)
			}
			if dms.lastAvatarInput.AvatarEmoji != emoji {
				t.Fatalf("stored %q, want the sequence byte for byte", dms.lastAvatarInput.AvatarEmoji)
			}
			if dms.lastAvatarInput.CallerID != adminCaller {
				t.Fatalf("caller = %q, want the canonical actor", dms.lastAvatarInput.CallerID)
			}
		})
	}
}

func TestDMService_SetGroupAvatar_RefusesAnythingButOneCatalogedEmoji(t *testing.T) {
	for _, emoji := range append([]string{""}, invalidGroupAvatarEmojis...) {
		t.Run(emoji, func(t *testing.T) {
			dms := &fakeDMStore{}
			err := service.NewDMService(dms, &fakeMemberStore{}).SetGroupAvatar(context.Background(), service.GroupAvatarInput{
				WorkspaceID: adminWSID, CallerID: adminCaller, ConversationID: adminGroup, AvatarEmoji: emoji,
			})
			if !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("err = %v, want ErrInvalidInput", err)
			}
			if dms.avatarCalls != 0 {
				t.Fatal("an invalid emoji must not reach the store")
			}
		})
	}
}

func TestDMService_ClearGroupAvatar_ForwardsAnEmptyEmojiWhateverWasSent(t *testing.T) {
	dms := &fakeDMStore{}
	err := service.NewDMService(dms, &fakeMemberStore{}).ClearGroupAvatar(context.Background(), service.GroupAvatarInput{
		WorkspaceID: adminWSID, CallerID: adminCaller, ConversationID: adminGroup, AvatarEmoji: "🎉",
	})
	if err != nil {
		t.Fatalf("ClearGroupAvatar: %v", err)
	}
	if dms.avatarCalls != 1 || dms.lastAvatarInput.AvatarEmoji != "" {
		t.Fatalf("calls = %d, forwarded %+v", dms.avatarCalls, dms.lastAvatarInput)
	}
}

func TestDMService_GroupAvatar_MalformedTargetOrActorNeverReachesTheStore(t *testing.T) {
	for _, input := range []service.GroupAvatarInput{
		{WorkspaceID: " ", CallerID: adminCaller, ConversationID: adminGroup},
		{WorkspaceID: adminWSID, CallerID: adminCaller, ConversationID: " "},
		{WorkspaceID: adminWSID, CallerID: "not-a-uuid", ConversationID: adminGroup},
	} {
		dms := &fakeDMStore{}
		err := service.NewDMService(dms, &fakeMemberStore{}).ClearGroupAvatar(context.Background(), input)
		if err == nil || dms.avatarCalls != 0 {
			t.Fatalf("input %+v: err = %v, calls = %d", input, err, dms.avatarCalls)
		}
	}
}

// The store's refusals (not a participant, not a visible group) pass through
// unchanged, so the handler can map them without describing state.
func TestDMService_SetGroupAvatar_PropagatesTheStoreDecision(t *testing.T) {
	for _, want := range []error{domain.ErrForbidden, domain.ErrNotFound} {
		dms := &fakeDMStore{avatarErr: want}
		err := service.NewDMService(dms, &fakeMemberStore{}).SetGroupAvatar(context.Background(), service.GroupAvatarInput{
			WorkspaceID: adminWSID, CallerID: adminCaller, ConversationID: adminGroup, AvatarEmoji: "🎉",
		})
		if !errors.Is(err, want) {
			t.Fatalf("err = %v, want %v", err, want)
		}
	}
}

// createGroupWithAvatar creates "Infra" with two eligible participants and the
// given emoji, returning the store so a test can see what reached it.
func createGroupWithAvatar(t *testing.T, emoji string) (*fakeDMStore, error) {
	t.Helper()
	ms := newFakeMemberStore()
	for _, uid := range []string{user1, user2, user3} {
		ms.workspaceMembers[wmKey("ws-1", uid)] = activeMembership("ws-1", uid)
	}
	dms := &fakeDMStore{createdConversation: domain.DMConversation{ID: "dm-group", Type: domain.DMConversationTypeGroup}}
	_, err := service.NewDMService(dms, ms).CreateGroupConversation(context.Background(), service.CreateGroupConversationInput{
		WorkspaceID: "ws-1", CallerID: user1, ParticipantUserIDs: []string{user2, user3}, Title: "Infra", AvatarEmoji: emoji,
	})
	return dms, err
}

// Absent is Automático; a catalogued sequence reaches the store untouched.
func TestDMService_CreateGroupConversation_StoresAnOptionalCatalogedEmoji(t *testing.T) {
	for _, emoji := range []string{"", "🚀", "🧑🏿‍🚀"} {
		t.Run(emoji, func(t *testing.T) {
			dms, err := createGroupWithAvatar(t, emoji)
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			if dms.lastGroupInput.AvatarEmoji != emoji {
				t.Fatalf("stored %q, want %q", dms.lastGroupInput.AvatarEmoji, emoji)
			}
		})
	}
}

func TestDMService_CreateGroupConversation_RefusesAnInvalidEmojiBeforeTheStore(t *testing.T) {
	for _, emoji := range invalidGroupAvatarEmojis {
		t.Run(emoji, func(t *testing.T) {
			dms, err := createGroupWithAvatar(t, emoji)
			if !errors.Is(err, domain.ErrInvalidInput) || dms.createGroupCalls != 0 {
				t.Fatalf("err = %v, store calls = %d; want ErrInvalidInput and none", err, dms.createGroupCalls)
			}
		})
	}
}
