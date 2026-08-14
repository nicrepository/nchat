package service

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/nicrepository/nchat/libs/go/platform/urlsafety"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// URLSafetyChecker answers what is already known about a set of canonical URLs
// (RF-21), without contacting anyone.
//
// It is declared at the consumer and satisfied by the link-scan store, so this
// package depends on the question rather than on Cloudflare or on pgx. Nil means
// the deployment did not enable the check.
//
// The unit is a canonical URL and not a hostname, and that is the whole point of
// this round: a verdict about "example.com" cleared every path on it, so a
// phishing page on a compromised subdirectory — or an open redirect carrying the
// payload in a query parameter — inherited the reputation of its host.
//
// Nothing here blocks on the provider. Cloudflare URL Scanner is submit-then-
// poll, so a URL nobody has scanned cannot be decided inside the send; it is
// reported as pending and the message is withheld until the worker answers.
type URLSafetyChecker interface {
	// LoadLinkVerdicts returns the fresh verdicts known for canonicalURLs. A URL
	// absent from the map has no usable verdict and must be treated as pending —
	// never as safe.
	LoadLinkVerdicts(ctx context.Context, canonicalURLs []string) (map[string]urlsafety.Verdict, error)
	// EnsureLinkScans records that these canonical URLs need a verdict, so the
	// worker will submit them. It is idempotent: ten messages naming one URL
	// produce one scan.
	EnsureLinkScans(ctx context.Context, canonicalURLs []string) error
}

// SetLinkSafety enables the RF-21 check on every path that accepts a body.
// Configure it during startup, before serving requests.
func (s *MessageService) SetLinkSafety(checker URLSafetyChecker) {
	s.linkSafety = checker
}

// HasLinkSafety reports whether the gate is installed.
//
// It exists so the bootstrap can be asserted on: "the flag was on and the gate
// is absent" is the exact state RF-21 must never reach, and without this the
// only way to observe it is to send a message through the whole service.
func (s *MessageService) HasLinkSafety() bool {
	return s != nil && s.linkSafety != nil
}

// maxCheckedURLs bounds how many scans one message may cause.
//
// The unit is *distinct canonical URLs*, because that is now what a scan costs:
// a message pointing twenty times at the same URL is one submission, but twenty
// different paths on one domain are twenty. Ten is already far outside what
// people write and well inside what an attacker would use to turn a 40,000-rune
// body into a quota-draining fan-out.
//
// Going over is a refusal rather than a truncation. Checking the first ten and
// ignoring the rest would make "add an eleventh link" a documented bypass.
const maxCheckedURLs = 10

// linkDecision is what the gate concluded about one body.
//
// Three outcomes, and the caller must handle all three: publish now, withhold
// until the worker answers, or refuse. The URLs are carried so the caller can
// record what a withheld message is waiting on, in the same statement that
// creates it.
type linkDecision struct {
	// PendingURLs are the canonical URLs with no usable verdict yet, in the
	// order they first appear. Non-empty means the message must be withheld.
	PendingURLs []string
	// URLs is every distinct canonical URL in the body. It is what gets recorded
	// against a withheld message, including the ones already safe, so a verdict
	// that expires and flips later still has an edge to be found by.
	URLs []string
}

// pending reports whether the message must be withheld.
func (d linkDecision) pending() bool { return len(d.PendingURLs) > 0 }

// messageStatus is the state a message carrying these links is created in.
//
// It returns the empty status for a cleared body rather than spelling out
// "active", so a body with no links takes exactly the same path it took before
// RF-21 existed and the storage layer's own default decides.
func (d linkDecision) messageStatus() domain.MessageStatus {
	if d.pending() {
		return domain.MessageStatusPendingLinkScan
	}
	return ""
}

// classifyBodyLinks decides what happens to a body carrying links.
//
// The policy over the outcomes is stated here rather than inside the store,
// because it is a product decision and not a property of the provider:
//
//   - malicious, or a URL with no consultable reputation at all, is refused
//     permanently. An IP literal is one of these and is deliberately not skipped:
//     leaving it unquestioned would make "host the phishing page on a bare
//     address" the obvious way past this check;
//   - a URL with no verdict yet is *not* refused and *not* allowed. It is
//     pending: the message is accepted, withheld from everyone else, and the
//     worker decides. This is the whole reason RF-21 is asynchronous — the
//     provider cannot answer about a new URL inside an interactive request, and
//     the alternatives were to publish it unchecked or to refuse every message
//     containing a link nobody had sent before;
//   - only an explicit, fresh clearance publishes immediately.
//
// Nothing here ever turns an error or an absence into a clearance.
func (s *MessageService) classifyBodyLinks(ctx context.Context, body string) (linkDecision, error) {
	if s.linkSafety == nil || body == "" {
		return linkDecision{}, nil
	}
	urls, err := extractURLs(body)
	if err != nil {
		return linkDecision{}, err
	}
	if len(urls) == 0 {
		return linkDecision{}, nil
	}
	verdicts, err := s.linkSafety.LoadLinkVerdicts(ctx, urls)
	if err != nil {
		if ctx.Err() != nil {
			return linkDecision{}, ctx.Err()
		}
		// The store is the authority; not being able to read it is not a
		// clearance. It is reported as unavailable, which is retryable.
		return linkDecision{}, domain.ErrURLCheckUnavailable
	}
	decision := linkDecision{URLs: urls}
	for _, canonical := range urls {
		switch verdicts[canonical] {
		case urlsafety.VerdictMalicious:
			// One condemned link is enough. Nothing is recorded and nothing is
			// submitted: the message is already refused.
			return linkDecision{}, domain.ErrMaliciousURL
		case urlsafety.VerdictSafe:
			// Cleared and still fresh — this URL holds nothing up.
		default:
			// Absent, expired, or any value that is not an explicit clearance.
			decision.PendingURLs = append(decision.PendingURLs, canonical)
		}
	}
	if decision.pending() {
		if err := s.linkSafety.EnsureLinkScans(ctx, decision.PendingURLs); err != nil {
			if ctx.Err() != nil {
				return linkDecision{}, ctx.Err()
			}
			return linkDecision{}, domain.ErrURLCheckUnavailable
		}
	}
	return decision, nil
}

// extractURLs returns the distinct canonical URLs a message body links to, in
// the order they first appear.
//
// It reads the body the way the renderer will, which is the part that matters.
// Bodies in the v2/v3 grammars are stored with backslash escapes, so a link
// written as `my\-site.com` renders as `my-site.com` — scanning the raw string
// would simply not see it, and "escape a hyphen" would be a bypass anyone could
// find. So the body is unescaped first, by the same rule the client uses.
//
// Deduplication is by canonical URL, which is also the unit the provider scans
// and the unit the verdict is stored under. Two links differing only by fragment
// collapse; two differing by path or query do not, and that is the finding this
// replaced — a verdict about the host used to clear every path on it.
func extractURLs(body string) ([]string, error) {
	unescaped := unescapeRichText(body)
	urls := make([]string, 0, 4)
	seen := make(map[string]struct{}, 4)
	for _, candidate := range scanURLCandidates(unescaped) {
		canonical, err := urlsafety.CanonicalizeURL(candidate)
		if err != nil {
			// Not something the provider can answer about. An IP literal is one
			// of these, and it is deliberately *not* skipped: leaving it
			// unquestioned would make "host the phishing page on a bare address"
			// the obvious way past this check. It is refused with the same
			// permanent error a condemned URL gets.
			//
			// A candidate that is not a URL at all — the scanner found "http://"
			// inside a word — has no host and is simply not a link, so it is
			// skipped rather than refused.
			if errors.Is(err, urlsafety.ErrNotCheckable) && hasHost(candidate) {
				return nil, domain.ErrMaliciousURL
			}
			continue
		}
		if _, exists := seen[canonical]; exists {
			continue
		}
		if len(urls) == maxCheckedURLs {
			return nil, fmt.Errorf("%w: at most %d distinct links are allowed per message",
				domain.ErrInvalidInput, maxCheckedURLs)
		}
		seen[canonical] = struct{}{}
		urls = append(urls, canonical)
	}
	return urls, nil
}

// hasHost reports whether a candidate names a host at all.
//
// It separates "this is a link to somewhere the provider cannot vouch for",
// which must be refused, from "this is not a link", which must be ignored.
// Without it, `https://` on its own would refuse the whole message.
func hasHost(candidate string) bool {
	parsed, err := url.Parse(candidate)
	return err == nil && parsed.Hostname() != ""
}

// scanURLCandidates finds the http and https URLs in a text.
//
// A scanner rather than a regular expression, because the interesting decisions
// are the boundaries and they are easier to read written out: a URL ends at the
// first whitespace or control character, and trailing punctuation is given back
// to the sentence — "see https://example.com." links to the site, not to a host
// with a dot on the end.
//
// It scans the original bytes rather than a lowercased copy. strings.ToLower is
// not length-preserving — "İ" (U+0130) is two bytes and lowercases to one — so
// an offset found in the copy does not address the same position in the
// original. Any such rune before a link was enough to make the slice start in
// the wrong place, or in the middle of a rune, and the link then went unchecked.
func scanURLCandidates(text string) []string {
	var candidates []string
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
		candidate := strings.TrimRight(text[start:end], ".,;:!?)]}'\"<>")
		if candidate != "" {
			candidates = append(candidates, candidate)
		}
		index = end
		if index == start {
			index++
		}
	}
	return candidates
}

// indexOfScheme returns the byte offset of the next http:// or https:// in
// text, matched case-insensitively, or -1.
//
// The comparison is done in place, on the original bytes. Both schemes are pure
// ASCII and a UTF-8 continuation byte is never in the ASCII range, so a match
// can only ever begin at a rune boundary — which is what makes scanning the
// original safe, and what makes lowercasing a copy unnecessary.
func indexOfScheme(text string) int {
	for index := 0; index < len(text); index++ {
		if text[index] != 'h' && text[index] != 'H' {
			continue
		}
		if hasASCIIPrefixFold(text[index:], "http://") ||
			hasASCIIPrefixFold(text[index:], "https://") {
			return index
		}
	}
	return -1
}

// hasASCIIPrefixFold reports whether text begins with prefix, comparing ASCII
// letters without regard to case. prefix must be lowercase ASCII.
func hasASCIIPrefixFold(text, prefix string) bool {
	if len(text) < len(prefix) {
		return false
	}
	for i := 0; i < len(prefix); i++ {
		char := text[i]
		if char >= 'A' && char <= 'Z' {
			char += 'a' - 'A'
		}
		if char != prefix[i] {
			return false
		}
	}
	return true
}

// isURLTerminator reports the bytes a URL may not contain. Whitespace and
// control characters end it; everything else is left to url.Parse, which is the
// thing that actually knows the grammar.
func isURLTerminator(char byte) bool {
	return char <= ' ' || char == 0x7f
}

// richTextEscapable mirrors V3_ESCAPABLE in apps/web/src/chat/richTextMarkers.ts.
//
// It is the superset of the v2 and v3 sets, so unescaping with it covers every
// stored grammar. The two lists are kept in sync by hand; a character added
// there and not here means a link that renders and is not checked, which is why
// the reference is named rather than left as "the client's rule".
const richTextEscapable = `\*_` + "`" + `-@[]()`

// unescapeRichText removes the backslash escapes the rich-text grammars add,
// reproducing what the reader will see.
//
// Only a backslash before a character the grammar actually escapes is removed.
// A backslash before anything else stays, so unescaping cannot manufacture a
// URL that no reader would ever be shown — `http:\/\/example.com` is not a link
// on screen and must not become one here either.
func unescapeRichText(text string) string {
	if !strings.Contains(text, `\`) {
		return text
	}
	out := make([]byte, 0, len(text))
	for index := 0; index < len(text); index++ {
		char := text[index]
		if char != '\\' || index+1 >= len(text) {
			out = append(out, char)
			continue
		}
		next := text[index+1]
		// The escaped dot is conditional in the client's rule: it is written
		// only after a digit, so "1.5" survives a round trip through the list
		// grammar. The predicate looks at what has already been emitted, which
		// is what the client's unescape does too.
		escapedDot := next == '.' && len(out) > 0 && out[len(out)-1] >= '0' && out[len(out)-1] <= '9'
		if strings.IndexByte(richTextEscapable, next) < 0 && !escapedDot {
			out = append(out, char)
			continue
		}
		out = append(out, next)
		index++
	}
	return string(out)
}
