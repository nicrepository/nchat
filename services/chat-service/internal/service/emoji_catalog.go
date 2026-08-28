package service

import (
	_ "embed"
	"strings"
)

// emojiCatalogFile is the validation projection of the Unicode RGI emoji set,
// written by scripts/emoji/generate-emoji-catalog.mjs from the same run that
// writes the web picker's catalog (issue #496). Embedding it is what lets this
// service stay the authority on which reactions exist without a hand-maintained
// list that would drift from the picker on the first Unicode release.
//
//go:embed emoji_catalog.txt
var emojiCatalogFile string

const emojiCatalogVersionPrefix = "# version "

type emojiCatalog struct {
	version   string
	sequences map[string]struct{}
}

// parseEmojiCatalog reads the embedded projection: comment lines carry the
// metadata, every other non-empty line is one complete Unicode sequence.
func parseEmojiCatalog(file string) emojiCatalog {
	catalog := emojiCatalog{sequences: make(map[string]struct{}, 4096)}
	for _, line := range strings.Split(file, "\n") {
		line = strings.TrimSpace(line)
		if version, ok := strings.CutPrefix(line, emojiCatalogVersionPrefix); ok {
			catalog.version = strings.TrimSpace(version)
			continue
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		catalog.sequences[line] = struct{}{}
	}
	return catalog
}

var reactionEmojiCatalog = parseEmojiCatalog(emojiCatalogFile)

// EmojiCatalogVersion is the Unicode emoji version this build validates
// against. It is served with the allowed-emoji configuration so a client can
// tell which catalog the server is holding, and is the ETag's only input.
func EmojiCatalogVersion() string { return reactionEmojiCatalog.version }

// EmojiCatalogSize reports how many sequences the catalog admits.
func EmojiCatalogSize() int { return len(reactionEmojiCatalog.sequences) }

// AllCatalogedReactionEmojis returns every catalogued sequence. It exists for
// invariant checks over the catalog as a whole; nothing in a request path needs
// the list, which is why validation is a map lookup and not a scan.
func AllCatalogedReactionEmojis() []string {
	emojis := make([]string, 0, len(reactionEmojiCatalog.sequences))
	for emoji := range reactionEmojiCatalog.sequences {
		emojis = append(emojis, emoji)
	}
	return emojis
}

// IsAllowedReactionEmoji reports whether a value is exactly one catalogued
// Unicode emoji sequence.
//
// Membership is compared over the whole string, so a ZWJ sequence, a skin-tone
// variant and a variation selector are each matched as the single sequence they
// are — never by counting code points, and never by inspecting a prefix. Anything
// else (markup, a shortcode, two emoji concatenated, an emoji with trailing text)
// is simply not a key in the catalog and is refused.
func IsAllowedReactionEmoji(emoji string) bool {
	_, ok := reactionEmojiCatalog.sequences[emoji]
	return ok
}
