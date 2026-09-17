package service

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/nicrepository/nchat/libs/go/platform/urlsafety"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// Link entity hydration (issue #807 §4).
//
// The backend is the authority for what in a body is a link, where it points,
// what is known about it and what the reader may do. This file derives that for
// a page of messages in two batch queries — targets and previews — and never one
// per message. Occurrences are re-derived from the body with the same scanner
// that recorded the associations at write time, so the entities always describe
// the body actually being served, including after an edit.

// LinkEntityStore is what hydration reads. Declared at the consumer and
// satisfied by the message store.
type LinkEntityStore interface {
	LoadLinkTargets(ctx context.Context, canonicalURLs []string) (map[string]storage.LinkTargetState, error)
	LoadLinkPreviews(ctx context.Context, workspaceID string, canonicalURLs []string) (map[string]storage.LinkPreviewRow, error)
	LoadMessageBodies(ctx context.Context, messageIDs []string) (map[string]string, error)
	QueueLinkPreviews(ctx context.Context, workspaceID string, canonicalURLs []string) error
}

// SetLinkEntities installs the per-link read model. Configure it during
// startup, before serving requests.
func (s *MessageService) SetLinkEntities(store LinkEntityStore) {
	s.linkEntities = store
}

// SetLinkPreviewEnabled records whether rich previews are on. Off, every
// preview reads as none: stored cards are not served and nothing new is queued.
// Safety and clickability are untouched.
func (s *MessageService) SetLinkPreviewEnabled(enabled bool) {
	s.linkPreviewEnabled = enabled
}

// queueLinkPreviews asks for a preview of URLs that already hold a fresh
// clearance, in this workspace. Best-effort: a failure costs a card, never a
// message, and the worker queues again when the verdict is next confirmed.
func (s *MessageService) queueLinkPreviews(ctx context.Context, workspaceID string, safeURLs []string) {
	if s.linkEntities == nil || !s.linkPreviewEnabled || len(safeURLs) == 0 {
		return
	}
	// The error is deliberately dropped: the message service has no logger of
	// its own, the send has already succeeded, and the worker re-queues the
	// preview when it next confirms the verdict. Nothing about safety depends on
	// this write.
	_ = s.linkEntities.QueueLinkPreviews(ctx, workspaceID, safeURLs)
}

// linkOccurrence is one URL span found in a body.
type linkOccurrence struct {
	text      string
	canonical string
}

// findLinkOccurrences returns the body's URL spans, in order, with their
// canonical targets. Text that does not canonicalise is not a link.
func findLinkOccurrences(body string) []linkOccurrence {
	var occurrences []linkOccurrence
	for _, candidate := range scanURLCandidates(unescapeRichText(body)) {
		canonical, err := urlsafety.CanonicalizeURL(candidate)
		if err != nil {
			continue
		}
		occurrences = append(occurrences, linkOccurrence{text: candidate, canonical: canonical})
	}
	return occurrences
}

// hydrateMessageLinks fills Links on every message that has any, and redacts
// the span of every condemned URL from its body.
func (s *MessageService) hydrateMessageLinks(ctx context.Context, workspaceID string, messages []domain.Message) error {
	if s.linkEntities == nil {
		return nil
	}
	occurrences, urls := s.collectOccurrences(ctx, messages)
	if len(urls) == 0 {
		return nil
	}
	targets, err := s.linkEntities.LoadLinkTargets(ctx, urls)
	if err != nil {
		return fmt.Errorf("hydrate link targets: %w", err)
	}
	previews, err := s.loadPreviews(ctx, workspaceID, targets)
	if err != nil {
		return err
	}
	for i := range messages {
		occ := occurrences[messages[i].ID]
		if len(occ) == 0 {
			continue
		}
		messages[i].Links = buildMessageLinks(occ, targets, previews)
		messages[i].BodyText = redactCondemnedLinks(messages[i].BodyText, occ, targets)
	}
	return nil
}

// collectOccurrences scans every serviceable body and returns the occurrences
// per message id and the distinct URLs across the page.
//
// A message whose aggregate marker is malicious arrives with an empty body —
// every SQL projection withholds it — so its real body is re-read here, for
// this one caller, to be redacted span by span rather than wholesale.
func (s *MessageService) collectOccurrences(ctx context.Context, messages []domain.Message) (map[string][]linkOccurrence, []string) {
	bodies := s.condemnedBodies(ctx, messages)
	occurrences := make(map[string][]linkOccurrence, len(messages))
	seen := make(map[string]struct{})
	var urls []string
	for i := range messages {
		msg := &messages[i]
		if msg.Kind != domain.MessageKindUser || !msg.DeletedAt.IsZero() {
			continue
		}
		if body, ok := bodies[msg.ID]; ok {
			msg.BodyText = body
		}
		occ := findLinkOccurrences(msg.BodyText)
		if len(occ) == 0 {
			continue
		}
		occurrences[msg.ID] = occ
		urls = appendDistinctURLs(urls, seen, occ)
	}
	return occurrences, urls
}

// appendDistinctURLs adds each occurrence's canonical URL not yet seen.
func appendDistinctURLs(urls []string, seen map[string]struct{}, occ []linkOccurrence) []string {
	for _, o := range occ {
		if _, dup := seen[o.canonical]; !dup {
			seen[o.canonical] = struct{}{}
			urls = append(urls, o.canonical)
		}
	}
	return urls
}

// condemnedBodies re-reads the bodies withheld by the projection for messages
// whose aggregate marker is malicious. A read failure leaves them withheld:
// nothing is shown that the projection refused.
func (s *MessageService) condemnedBodies(ctx context.Context, messages []domain.Message) map[string]string {
	var ids []string
	for i := range messages {
		if messages[i].LinkSafety == domain.MessageLinkSafetyMalicious && messages[i].DeletedAt.IsZero() {
			ids = append(ids, messages[i].ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	bodies, err := s.linkEntities.LoadMessageBodies(ctx, ids)
	if err != nil {
		return nil
	}
	return bodies
}

// loadPreviews reads the workspace's previews for the safe targets only. A
// preview row for a target that is not currently safe is never served, whatever
// the row says — the read path is the last gate.
func (s *MessageService) loadPreviews(ctx context.Context, workspaceID string, targets map[string]storage.LinkTargetState) (map[string]storage.LinkPreviewRow, error) {
	if !s.linkPreviewEnabled {
		return nil, nil
	}
	var safe []string
	for url, target := range targets {
		if linkSafetyFor(target) == domain.LinkSafetySafe {
			safe = append(safe, url)
		}
	}
	previews, err := s.linkEntities.LoadLinkPreviews(ctx, workspaceID, safe)
	if err != nil {
		return nil, fmt.Errorf("hydrate link previews: %w", err)
	}
	return previews, nil
}

// buildMessageLinks assembles the entities for one message's occurrences.
//
// Every safe occurrence with a preview carries it: how many cards a message
// draws, and which, is presentation and belongs to the client alone (its
// MAX_PREVIEW_CARDS), so the initial load, a realtime update and a re-read all
// describe a target the same way — a third target's card is data the client
// may draw the moment an earlier one stops being eligible.
func buildMessageLinks(occurrences []linkOccurrence, targets map[string]storage.LinkTargetState, previews map[string]storage.LinkPreviewRow) []domain.MessageLink {
	links := make([]domain.MessageLink, 0, len(occurrences))
	for ordinal, occ := range occurrences {
		link := linkFor(ordinal, occ, targets[occ.canonical])
		if preview, ok := previews[occ.canonical]; ok && link.Safety == domain.LinkSafetySafe {
			link.Preview = previewFor(preview)
		}
		links = append(links, link)
	}
	return links
}

// linkFor applies the policy to one occurrence.
func linkFor(ordinal int, occ linkOccurrence, target storage.LinkTargetState) domain.MessageLink {
	safety := linkSafetyFor(target)
	click, _ := domain.LinkAccess(safety)
	link := domain.MessageLink{
		Ordinal: ordinal, TargetKey: domain.LinkTargetKey(occ.canonical),
		Safety: safety, Click: click, UpdatedAt: target.UpdatedAt,
	}
	if safety == domain.LinkSafetyMalicious {
		// Withheld: no text, no URL, no hostname. The chip says "blocked" and
		// nothing else.
		return link
	}
	link.Text, link.URL, link.Hostname = occ.text, occ.canonical, hostnameOf(occ.canonical)
	if click == domain.LinkClickDirect {
		link.Href = occ.canonical
	}
	return link
}

// linkSafetyFor maps a stored target onto the client vocabulary.
//
// A stale safe or malicious verdict is still reported as what it was: the
// durable row is the authority until a recheck replaces it, and rechecks are
// scheduled whenever the URL is named again (stale-while-revalidate, bounded by
// the next reference). A stale unknown, by contrast, reads as pending only if a
// row was reopened — which makes the status pending — so no mapping is needed.
func linkSafetyFor(target storage.LinkTargetState) domain.LinkSafety {
	switch target.Status {
	case string(urlsafety.VerdictSafe):
		return domain.LinkSafetySafe
	case string(urlsafety.VerdictMalicious):
		return domain.LinkSafetyMalicious
	case string(urlsafety.VerdictInconclusive), "unknown":
		return domain.LinkSafetyUnknown
	default:
		return domain.LinkSafetyPending
	}
}

// previewFor maps a stored preview onto the entity. The hostname comes from the
// canonical URL, never from the page: og:site_name is what the page claims,
// the hostname is where the link actually goes.
func previewFor(row storage.LinkPreviewRow) *domain.LinkPreview {
	preview := &domain.LinkPreview{
		State: domain.LinkPreviewState(row.State), Hostname: hostnameOf(row.CanonicalURL),
	}
	if row.State != string(domain.LinkPreviewReady) {
		return preview
	}
	preview.SiteName, preview.Title, preview.Description = row.SiteName, row.Title, row.Description
	if row.HasImage {
		preview.ImageID, preview.ImageWidth, preview.ImageHeight = row.ID, row.ImageWidth, row.ImageHeight
	}
	return preview
}

// hostnameOf returns the host of a canonical URL. Canonical URLs always parse.
func hostnameOf(canonical string) string {
	parsed, err := url.Parse(canonical)
	if err != nil {
		return ""
	}
	return parsed.Host
}

// redactCondemnedLinks replaces every span of a condemned URL with the blocked
// marker, so the URL cannot be read, selected or copied off the screen while
// the rest of the text stays exactly as written.
//
// The replacement runs over the escaped body the client renders, scanning it
// with the same scanner: a span is redacted if it unescapes and canonicalises
// to a condemned target. Anything the scanner sees as part of the URL goes with
// it — a trailing markup marker included — which is the fail-closed direction.
func redactCondemnedLinks(body string, occurrences []linkOccurrence, targets map[string]storage.LinkTargetState) string {
	condemned := condemnedTargets(occurrences, targets)
	if len(condemned) == 0 {
		return body
	}
	var out strings.Builder
	cursor := 0
	for _, span := range scanURLSpans(body) {
		end := trimMarkupTail(body, span.start, span.end)
		if !isCondemnedSpan(body[span.start:end], condemned) && !isCondemnedSpan(body[span.start:span.end], condemned) {
			continue
		}
		out.WriteString(body[cursor:span.start])
		out.WriteString(domain.LinkBlockedMarker)
		cursor = end
	}
	out.WriteString(body[cursor:])
	return out.String()
}

// isCondemnedSpan reports whether a raw span canonicalises, once unescaped the
// way the reader sees it, to a condemned target.
func isCondemnedSpan(raw string, condemned map[string]struct{}) bool {
	canonical, err := urlsafety.CanonicalizeURL(unescapeRichText(raw))
	if err != nil {
		return false
	}
	_, isCondemned := condemned[canonical]
	return isCondemned
}

// inlineMarkupTail are the inline-grammar markers a URL may be wrapped in
// (`**bold**`, `_italic_`, “ `code` “). The client's tokenizer strips them
// before it looks for URLs, so a condemned URL written inside them must be
// matched without them too — belt and braces, since the scanner that recorded
// the target saw the markers as part of the URL.
const inlineMarkupTail = "*_`~"

func trimMarkupTail(text string, start, end int) int {
	for end > start && strings.IndexByte(inlineMarkupTail, text[end-1]) >= 0 {
		end--
	}
	return end
}

func condemnedTargets(occurrences []linkOccurrence, targets map[string]storage.LinkTargetState) map[string]struct{} {
	condemned := make(map[string]struct{})
	for _, occ := range occurrences {
		if linkSafetyFor(targets[occ.canonical]) == domain.LinkSafetyMalicious {
			condemned[occ.canonical] = struct{}{}
		}
	}
	return condemned
}

// urlSpan is a candidate's byte range in the text it was scanned from.
type urlSpan struct{ start, end int }

// scanURLSpans is scanURLCandidates returning offsets instead of substrings,
// for the redaction that has to splice the text.
func scanURLSpans(text string) []urlSpan {
	var spans []urlSpan
	for index := 0; index < len(text); {
		start := indexOfScheme(text[index:])
		if start < 0 {
			break
		}
		start += index
		end := start
		for end < len(text) && !isURLTerminator(text[end]) {
			end++
		}
		if stop := trimTrailingDelimiters(text, start, end); stop > start {
			spans = append(spans, urlSpan{start: start, end: stop})
		}
		index = max(end, start+1)
	}
	return spans
}
