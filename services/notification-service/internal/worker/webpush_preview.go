package worker

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nicrepository/nchat/libs/go/platform/notificationevent"
	"github.com/nicrepository/nchat/services/notification-service/internal/storage"
)

// The server-approved text of a push notification (issue #870).
//
// # Why the server decides the words
//
// A Service Worker that inferred a title from the payload would be a second
// place where presentation is reasoned about, in a build that can be months old
// and cannot be corrected without every tab of the origin closing. So it infers
// nothing: it is handed a title and a body it has already been told are safe,
// checks their shape, and shows them. Everything below is that decision, made
// once, here, where it is testable.
//
// # Why it is all plain text
//
// ServiceWorkerRegistration.showNotification() renders text and not markup —
// there is no element, no innerHTML and no parser — so the risk a preview
// carries is not injection into a page. It is what the characters *say* and
// where they end up: a lock screen, a shoulder, a screenshot. The rules below
// are written against that: strip what can lie about direction or hide itself,
// collapse what wastes the line, and bound the length deterministically.

const (
	// previewMaxRunes bounds the body preview.
	//
	// Two lines of an OS banner, roughly, on every platform that shows two.
	// Deliberately far short of a message: a preview exists to say whether
	// something is worth opening, and a longer one only puts more of a private
	// conversation on a screen the recipient is not necessarily looking at.
	previewMaxRunes = 140
	// previewMaxBytes bounds the same text in the unit the payload is measured
	// in. 140 characters of Latin text is 140 bytes and 140 of CJK is 420, so
	// the rune limit alone would let one message cost three times another. Both
	// apply, and truncation stops at whichever is reached first.
	previewMaxBytes = 320
	// titleMaxRunes and titleMaxBytes bound the title on the same terms. One
	// line: a display name, a separator and a conversation.
	titleMaxRunes = 80
	titleMaxBytes = 200
	// ellipsis marks a truncation, so a preview that stops mid-sentence reads
	// as cut rather than as the whole of what somebody said.
	ellipsis = "…"
)

// Presentation copy. Portuguese, like every other string the browser shows.
const (
	// previewAttachmentOnly is what a message with files and no text says. The
	// count is deliberately absent: "how many" is not worth a plural rule here,
	// and the recipient is about to see them.
	previewAttachmentOnly = "Enviou um anexo"
	// urgentReminderPrefix marks a repeat (issue #825). Without it the fifth
	// reminder is indistinguishable from a fifth message, which is the one
	// thing #825 gave urgent_reminder its own event type to prevent.
	urgentReminderPrefix = "Urgente: "
	// titleSeparator joins who said it to where they said it.
	titleSeparator = " · "
)

// pushPresentation is what the Service Worker is allowed to show.
//
// Two strings and nothing else. Either may be empty, and empty means "say
// nothing here": the worker falls back to the generic per-type title version 1
// always produced, and shows no body at all. That is the whole fallback
// contract, and it is why no caller has to decide what a missing preview means.
type pushPresentation struct {
	Title string
	Body  string
}

// presentationFor renders one notification for a native banner.
//
// It reads only the projection the claim resolved against this recipient's
// access at claim time (storage.presentationProjection), so there is no branch here
// that could widen what may be shown — an unauthorized event arrives as the
// zero value and leaves as two empty strings.
func presentationFor(notification Notification) pushPresentation {
	source := notification.Presentation
	title := sanitizeLine(source.Sender)
	context := sanitizeLine(source.Context)
	if context == "" && source.GroupDM {
		context = "Grupo"
	}
	if context != "" {
		if title == "" {
			title = context
		} else {
			title += titleSeparator + context
		}
	}
	if title == "" {
		// Nothing identifiable, so nothing to say. A body without a title would
		// be a preview of a message from nobody, which is worse than the
		// generic notification it falls back to.
		return pushPresentation{}
	}
	if notification.EventType == string(notificationevent.EventTypeUrgentReminder) {
		title = urgentReminderPrefix + title
	}
	return pushPresentation{
		Title: truncate(title, titleMaxRunes, titleMaxBytes),
		Body:  truncate(previewBody(source), previewMaxRunes, previewMaxBytes),
	}
}

// previewBody is the message itself, or what stands in for it.
//
// The empty string is a real answer and the common one: a message whose text
// the recipient could not see at claim time carries no attachment flag either, because both
// come from the same authorized projection.
func previewBody(source storage.MessagePresentation) string {
	if body := sanitizeLine(source.Body); body != "" {
		return body
	}
	if source.Attachment {
		return previewAttachmentOnly
	}
	return ""
}

// sanitizeLine turns message text into one line safe to put on a screen.
//
// Three things happen, in order:
//
//  1. Invalid UTF-8 is replaced. The column is UTF-8, so this is defence
//     against a byte sequence that reached it some other way; a payload is JSON
//     and invalid UTF-8 in one is a malformed payload.
//  2. Control and format characters are dropped. Control characters do things
//     to a terminal and to some notification surfaces; format characters
//     include the bidirectional overrides (U+202A..U+202E, U+2066..U+2069),
//     which let text render in an order it is not written in — a sender could
//     otherwise make a preview read as something they did not say.
//  3. Runs of whitespace become one space, and the ends are trimmed. A banner
//     is one line, and a message that starts with forty newlines must not push
//     its own content out of it.
//
// ponytail: dropping the whole Cf category also drops U+200D, so an emoji
// written as a ZWJ sequence decomposes into its parts. That is cosmetic and it
// is the cheap direction of the trade; an allowlist inside Cf is worth writing
// the day someone complains about an emoji, not before.
//
// Markdown is deliberately *not* stripped. Two asterisks reaching a banner
// render as the two characters they are, because showNotification has no
// parser, and removing them would be a rendering decision this layer would then
// have to keep in step with the message renderer for ever.
func sanitizeLine(value string) string {
	var out strings.Builder
	out.Grow(len(value))
	space := false
	for _, r := range strings.ToValidUTF8(value, "") {
		switch {
		case unicode.IsSpace(r):
			space = out.Len() > 0
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r):
			// Dropped outright, and without counting as a space: a zero-width
			// character between two letters joined a word, not two.
		default:
			if space {
				out.WriteRune(' ')
				space = false
			}
			out.WriteRune(r)
		}
	}
	return out.String()
}

// truncate bounds a string by characters and by bytes at once.
//
// Deterministic: the same input always produces the same output, which is what
// lets a retried notification encode to the same payload bytes as its first
// attempt. It cuts on a rune boundary, so it can never produce invalid UTF-8,
// and it never produces a string longer than maxRunes characters or maxBytes
// bytes *including* the ellipsis — the mark counts against both budgets, which
// is the only reading under which the limits mean what they say.
//
// ponytail: rune boundaries, not grapheme clusters. A cut can separate a
// combining accent from its letter or split a flag emoji; the result is still
// valid UTF-8 and still renders. Grapheme segmentation is a dependency, and one
// is not worth adding for the last character of a truncated preview.
func truncate(value string, maxRunes, maxBytes int) string {
	if utf8.RuneCountInString(value) <= maxRunes && len(value) <= maxBytes {
		return value
	}
	runeBudget := maxRunes - utf8.RuneCountInString(ellipsis)
	byteBudget := maxBytes - len(ellipsis)
	runes, size := 0, 0
	for _, r := range value {
		width := utf8.RuneLen(r)
		if runes >= runeBudget || size+width > byteBudget {
			break
		}
		runes, size = runes+1, size+width
	}
	return strings.TrimRight(value[:size], " ") + ellipsis
}
