package worker

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/nicrepository/nchat/services/notification-service/internal/storage"
)

// Issue #870: what a notification banner is allowed to say.
//
// Two families of assertion live here and they answer different questions. The
// presentation tests answer "does the right thing appear" — a name, a
// conversation, an attachment, a reminder. The sanitisation and truncation
// tests answer "can a sender make it say something else" — which is the half
// that matters, because the input is a message somebody typed.

func previewNotification(presentation storage.MessagePresentation) Notification {
	notification := payloadNotification()
	notification.Presentation = presentation
	return notification
}

// A one-to-one DM: the sender is the whole context, so the title is the name
// and nothing is appended to it.
func TestPresentationOfADirectMessageIsTheSenderAndTheBody(t *testing.T) {
	got := presentationFor(previewNotification(storage.MessagePresentation{
		Sender: "Ana Ribeiro",
		Body:   "consegue revisar o PR hoje?",
	}))

	if got.Title != "Ana Ribeiro" {
		t.Fatalf("title = %q, want the sender alone", got.Title)
	}
	if got.Body != "consegue revisar o PR hoje?" {
		t.Fatalf("body = %q, want the message", got.Body)
	}
}

// A channel or a group: who said it, and where. Both, because either alone is
// the ambiguity #870 exists to remove.
func TestPresentationOfAConversationCarriesTheContext(t *testing.T) {
	for name, context := range map[string]string{
		"channel":  "#geral",
		"group dm": "Plantão SRE",
	} {
		t.Run(name, func(t *testing.T) {
			got := presentationFor(previewNotification(storage.MessagePresentation{
				Sender: "Ana Ribeiro", Context: context, Body: "subiu o hotfix",
			}))

			if got.Title != "Ana Ribeiro"+titleSeparator+context {
				t.Fatalf("title = %q, want the sender and %q", got.Title, context)
			}
		})
	}
}

func TestPresentationDistinguishesUnnamedGroupsFromDirectMessages(t *testing.T) {
	for _, test := range []struct {
		name, context, title string
		group                bool
	}{
		{name: "direct", title: "Ana"},
		{name: "named group", context: "Plantão", group: true, title: "Ana · Plantão"},
		{name: "unnamed group", group: true, title: "Ana · Grupo"},
		{name: "whitespace group", context: " \t\n\u00a0 ", group: true, title: "Ana · Grupo"},
		{name: "format-only group", context: string(rune(0x200B)), group: true, title: "Ana · Grupo"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := presentationFor(previewNotification(storage.MessagePresentation{
				Sender: "Ana", Context: test.context, GroupDM: test.group, Body: "mensagem",
			}))
			if got.Title != test.title || got.Body != "mensagem" {
				t.Fatalf("presentation = %+v, want title %q and unchanged body", got, test.title)
			}
		})
	}
}

// A reminder is the same message asking again (issue #825), and the title says
// so. Without this a fifth reminder reads as a fifth message.
func TestPresentationOfAnUrgentReminderIsMarkedAsOne(t *testing.T) {
	notification := previewNotification(storage.MessagePresentation{
		Sender: "Ana Ribeiro", Body: "preciso da sua confirmação",
	})
	notification.EventType = "urgent_reminder"

	got := presentationFor(notification)

	if !strings.HasPrefix(got.Title, urgentReminderPrefix) {
		t.Fatalf("title = %q, want it marked as a reminder", got.Title)
	}
	if !strings.Contains(got.Title, "Ana Ribeiro") {
		t.Fatalf("title = %q, want the sender kept", got.Title)
	}
}

// A message with files and no text says something rather than nothing. The
// filename is deliberately not it: a name can be as revealing as a body and was
// never part of what this issue authorised.
func TestPresentationOfAnAttachmentOnlyMessageSaysSo(t *testing.T) {
	got := presentationFor(previewNotification(storage.MessagePresentation{
		Sender: "Ana Ribeiro", Attachment: true,
	}))

	if got.Body != previewAttachmentOnly {
		t.Fatalf("body = %q, want the attachment stand-in", got.Body)
	}
}

// A message with neither text nor files gets a title and no body. The banner is
// then "who, and where", which is true and is all there is.
func TestPresentationWithNothingToPreviewCarriesNoBody(t *testing.T) {
	got := presentationFor(previewNotification(storage.MessagePresentation{
		Sender: "Ana Ribeiro", Context: "#geral",
	}))

	if got.Title == "" {
		t.Fatal("a message with no body still has a sender and a place")
	}
	if got.Body != "" {
		t.Fatalf("body = %q, want nothing", got.Body)
	}
}

// The zero presentation is what the projection returns for every unpublishable
// and every unauthorized case — withheld, deleted, condemned, cross-workspace,
// no longer a member. All of them arrive here identically and all of them
// produce silence, which is what the Service Worker turns into the generic
// notification version 1 always showed.
func TestPresentationOfSomethingTheRecipientMayNotSeeIsEmpty(t *testing.T) {
	got := presentationFor(previewNotification(storage.MessagePresentation{}))

	if got != (pushPresentation{}) {
		t.Fatalf("presentation = %+v, want nothing at all", got)
	}
}

// A body can never appear without a title. That pairing is the contract the
// Service Worker relies on: it has one fallback, for the title, and a body
// under a generic title would be a preview of a message from nobody.
func TestPresentationNeverCarriesABodyWithoutATitle(t *testing.T) {
	got := presentationFor(previewNotification(storage.MessagePresentation{
		Body: "texto de uma mensagem sem remetente conhecido", Attachment: true,
	}))

	if got.Body != "" {
		t.Fatalf("body = %q, want nothing without a title", got.Body)
	}
}

// Control characters and Unicode format characters never reach a screen.
//
// The bidirectional overrides are the reason this is a security test and not a
// tidiness one: U+202E reverses the rendering of everything after it, so a
// sender could compose a message that a banner displays as text they never
// wrote - including one that looks like it came from somewhere else.
func TestSanitiseStripsControlAndFormatCharacters(t *testing.T) {
	// Built from code points, never written as literals. A bidirectional
	// override in a Go source file is a Trojan Source finding in its own right
	// (gosec G116), a NUL byte cannot appear in Go source at all, and the rest
	// are invisible in an editor — which is precisely the property that makes
	// them worth a test.
	const (
		bidiOverride = 0x202E // right-to-left override
		bell         = 0x0007
		zeroWidth    = 0x200B // zero-width space, category Cf
		nul          = 0x0000
	)
	got := sanitizeLine("ok" + string(rune(bidiOverride)) + "gnitset" +
		string(rune(bell)) + " " + string(rune(zeroWidth)) + "fim" + string(rune(nul)))

	for _, forbidden := range []rune{bidiOverride, bell, zeroWidth, nul} {
		if strings.ContainsRune(got, forbidden) {
			t.Fatalf("sanitised text still carries U+%04X: %q", forbidden, got)
		}
	}
	if got != "okgnitset fim" {
		t.Fatalf("sanitised = %q", got)
	}
}

// A banner is one line. A message that is mostly newlines must not push its own
// content out of it, and a run of whitespace must not buy a sender more of the
// limit than a run of words.
func TestSanitiseCollapsesWhitespaceToOneLine(t *testing.T) {
	got := sanitizeLine("  primeira linha\n\n\tsegunda   linha  \n")

	if got != "primeira linha segunda linha" {
		t.Fatalf("sanitised = %q", got)
	}
}

// Markup is not interpreted and is not removed either. showNotification renders
// text, so a tag is the characters it is made of; stripping it would be a
// rendering decision this layer would have to keep in step with the message
// renderer for ever.
func TestSanitiseLeavesMarkupAsLiteralText(t *testing.T) {
	got := sanitizeLine("<b>oi</b> **tudo bem**")

	if got != "<b>oi</b> **tudo bem**" {
		t.Fatalf("sanitised = %q, want the characters unchanged", got)
	}
}

// Invalid UTF-8 cannot reach a payload: a JSON document carrying it is a
// malformed document, and the browser would reject the whole notification
// rather than the one bad byte.
func TestSanitiseDropsInvalidUTF8(t *testing.T) {
	got := sanitizeLine("bom dia\xff\xfe fim")

	if !utf8.ValidString(got) {
		t.Fatalf("sanitised text is not valid UTF-8: %q", got)
	}
	if got != "bom dia fim" {
		t.Fatalf("sanitised = %q", got)
	}
}

// A long message is cut, marked as cut, and cut on a character boundary.
func TestTruncationIsBoundedMarkedAndValid(t *testing.T) {
	got := presentationFor(previewNotification(storage.MessagePresentation{
		Sender: "Ana Ribeiro",
		Body:   strings.Repeat("a", previewMaxRunes*3),
	}))

	if utf8.RuneCountInString(got.Body) > previewMaxRunes {
		t.Fatalf("body is %d characters, over the %d limit",
			utf8.RuneCountInString(got.Body), previewMaxRunes)
	}
	if len(got.Body) > previewMaxBytes {
		t.Fatalf("body is %d bytes, over the %d limit", len(got.Body), previewMaxBytes)
	}
	if !strings.HasSuffix(got.Body, ellipsis) {
		t.Fatalf("a truncated body is not marked as one: %q", got.Body)
	}
}

// The byte bound binds independently of the character bound. 140 characters of
// Latin text is 140 bytes and 140 characters of an emoji is 560, so without
// this one message costs four times another and the payload budget is not a
// budget.
func TestTruncationBoundsBytesAndNotOnlyCharacters(t *testing.T) {
	got := presentationFor(previewNotification(storage.MessagePresentation{
		Sender: "Ana Ribeiro",
		Body:   strings.Repeat("\U0001F642", previewMaxRunes),
	}))

	if len(got.Body) > previewMaxBytes {
		t.Fatalf("body is %d bytes, over the %d limit", len(got.Body), previewMaxBytes)
	}
	if utf8.RuneCountInString(got.Body) >= previewMaxRunes {
		t.Fatal("the byte limit did not bind before the character limit")
	}
	if !utf8.ValidString(got.Body) {
		t.Fatalf("truncation produced invalid UTF-8: %q", got.Body)
	}
}

// Multi-byte text is never cut mid-character, at any length.
//
// A sweep rather than one case: the interesting failure is a boundary one, and
// the boundary moves with the width of the character. Accented Latin is two
// bytes, the emoji is four, so between them every offset in the budget is
// crossed by something.
func TestTruncationNeverSplitsACharacter(t *testing.T) {
	for _, unit := range []string{"ã", "\U0001F642", "日"} {
		for length := 1; length <= previewMaxRunes*4; length++ {
			body := truncate(strings.Repeat(unit, length), previewMaxRunes, previewMaxBytes)
			if !utf8.ValidString(body) {
				t.Fatalf("%q repeated %d times truncated to invalid UTF-8: %q",
					unit, length, body)
			}
		}
	}
}

// The same input always produces the same output, which is what lets a retried
// notification encode to the same payload bytes as its first attempt.
func TestTruncationIsDeterministic(t *testing.T) {
	value := strings.Repeat("mensagem longa ", 40)

	first := truncate(value, previewMaxRunes, previewMaxBytes)
	second := truncate(value, previewMaxRunes, previewMaxBytes)

	if first != second {
		t.Fatalf("truncation is not deterministic:\n%q\n%q", first, second)
	}
}

// A title long enough to be a body is cut on its own budget, which is smaller.
// Otherwise a workspace could name a channel with a paragraph and buy the whole
// banner with it.
func TestTitleHasItsOwnBound(t *testing.T) {
	got := presentationFor(previewNotification(storage.MessagePresentation{
		Sender:  strings.Repeat("A", titleMaxRunes),
		Context: strings.Repeat("B", titleMaxRunes),
	}))

	if utf8.RuneCountInString(got.Title) > titleMaxRunes {
		t.Fatalf("title is %d characters, over the %d limit",
			utf8.RuneCountInString(got.Title), titleMaxRunes)
	}
	if len(got.Title) > titleMaxBytes {
		t.Fatalf("title is %d bytes, over the %d limit", len(got.Title), titleMaxBytes)
	}
}
