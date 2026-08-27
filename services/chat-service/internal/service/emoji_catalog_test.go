package service_test

import (
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/service"
)

func TestEmojiCatalog_ReportsItsUnicodeVersionAndSize(t *testing.T) {
	if got := service.EmojiCatalogVersion(); got == "" {
		t.Fatal("catalog must report the Unicode emoji version it was generated from")
	}
	// A catalogue this small would mean the embedded projection was truncated or
	// replaced by a hand-written shortlist, which is the regression issue #496
	// exists to prevent.
	if got := service.EmojiCatalogSize(); got < 3000 {
		t.Fatalf("expected a comprehensive catalog, got %d sequences", got)
	}
}

// The sequences below are exactly the shapes a length- or prefix-based check
// gets wrong: a joined family, a modified body part, a two-code-point flag, a
// keycap, and a heart whose emoji presentation is a separate code point.
func TestIsAllowedReactionEmoji_AcceptsCompleteUnicodeSequences(t *testing.T) {
	for _, emoji := range []string{
		"👍",
		"❤️",
		"👨‍👩‍👧‍👦",
		"👍🏿",
		"🧑🏽‍🚒",
		"🏳️‍🌈",
		"🇧🇷",
		"1️⃣",
		"👩🏽‍❤️‍💋‍👨🏿",
	} {
		if !service.IsAllowedReactionEmoji(emoji) {
			t.Errorf("expected %q to be catalogued", emoji)
		}
	}
}

func TestIsAllowedReactionEmoji_RejectsAnythingElse(t *testing.T) {
	for name, value := range map[string]string{
		"empty":              "",
		"plain text":         "a",
		"markup":             "<img src=x onerror=alert(1)>",
		"shortcode":          ":thumbsup:",
		"two emoji":          "👍👍",
		"emoji with text":    "👍 boa",
		"emoji with padding": " 👍",
		"lone skin tone":     "🏻",
		"lone joiner":        "\u200d",
		"unqualified heart":  "❤",
		"long joiner spam":   "\U0001F44D\u200d\U0001F44D\u200d\U0001F44D\u200d\U0001F44D",
	} {
		if service.IsAllowedReactionEmoji(value) {
			t.Errorf("%s: expected %q to be refused", name, value)
		}
	}
}

// The database bound and the catalog are two halves of one decision: migration
// 000040 widened chat.message_reactions.emoji to 32 code points because that is
// where the catalog's longest sequence fits. If a future Unicode version outgrew
// it, every write of that sequence would fail at the constraint rather than at
// validation, which is the wrong place to find out.
func TestEmojiCatalog_FitsTheStoredSequenceLength(t *testing.T) {
	const storedEmojiCodePointLimit = 32
	for _, emoji := range service.AllCatalogedReactionEmojis() {
		if n := len([]rune(emoji)); n > storedEmojiCodePointLimit {
			t.Fatalf("%q needs %d code points; migration 000040 permits %d", emoji, n, storedEmojiCodePointLimit)
		}
	}
}
