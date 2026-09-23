package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// Per-link safety and preview (issue #807).
//
// Four things about a link that used to be one field on the message are now
// four, and the invariant is that none of them may stand in for another:
//
//	message lifecycle   Message.Status — active or deleted; never "waiting on a scan"
//	link safety         MessageLink.Safety — what is known about this target
//	link clickability   MessageLink.Click / Href — what the policy authorises the reader to do
//	link preview        MessageLink.Preview — enrichment, only ever after an explicit safe
//
// The backend is the authority for all of them. A client draws what it is
// given; it does not parse the body to decide what is a link, does not decide
// whether a link may be followed, and never fetches anything about a URL.

// LinkSafety is the per-target verdict as a client sees it.
type LinkSafety string

const (
	// LinkSafetyPending has no verdict yet. It converges: every pending target
	// carries a deadline after which it becomes unknown.
	LinkSafetyPending LinkSafety = "pending"
	// LinkSafetySafe is an explicit, fresh clearance from the reputation
	// pipeline. It is the only value that authorises a server-side fetch.
	LinkSafetySafe LinkSafety = "safe"
	// LinkSafetyMalicious is a condemnation. No href, no preview, and the URL
	// text is withheld from the body.
	LinkSafetyMalicious LinkSafety = "malicious"
	// LinkSafetyUnknown is terminal without a clearance: the provider had no
	// usable answer, the deadline passed, or the policy refused to ask (a
	// sensitive URL, an internal host, the feature disabled). It is never a
	// clearance and never eligible for preview; the balanced policy lets the
	// reader navigate through an interstitial.
	LinkSafetyUnknown LinkSafety = "unknown"
)

// LinkClick is what the policy lets a reader do with the link.
type LinkClick string

const (
	// LinkClickNone means no navigation is offered: pending or malicious.
	LinkClickNone LinkClick = "none"
	// LinkClickDirect means an ordinary anchor to Href.
	LinkClickDirect LinkClick = "direct"
	// LinkClickInterstitial means navigation only through a confirmation that
	// shows the real destination; the client opens URL, never a server href.
	LinkClickInterstitial LinkClick = "interstitial"
)

// LinkPreviewState is the enrichment lifecycle, independent of safety.
type LinkPreviewState string

const (
	LinkPreviewNone        LinkPreviewState = "none"
	LinkPreviewQueued      LinkPreviewState = "queued"
	LinkPreviewFetching    LinkPreviewState = "fetching"
	LinkPreviewReady       LinkPreviewState = "ready"
	LinkPreviewUnsupported LinkPreviewState = "unsupported"
	LinkPreviewFailed      LinkPreviewState = "failed"
)

// LinkPreview is the rich card's content. Every string is remote text carried
// as data; the image is a derived asset served by NChat, never a remote URL.
type LinkPreview struct {
	State       LinkPreviewState
	Hostname    string
	SiteName    string
	Title       string
	Description string
	// ImageID names the derived thumbnail, served by an authenticated route
	// scoped to the workspace. Empty when the preview has no image.
	ImageID     string
	ImageWidth  int
	ImageHeight int
}

// MessageLink is one occurrence of a URL in a message body, with the state of
// the target it points at and what the policy authorises for it.
type MessageLink struct {
	// Ordinal is the occurrence's position among the message's links, in body
	// order, starting at zero.
	Ordinal int
	// TargetKey is the stable identity of the target this occurrence points at,
	// carried on every occurrence and on every realtime update about the target
	// — including a condemned one, whose URL and text are withheld. It is what a
	// client matches an update against: the identity of an occurrence is never
	// the visible URL, which can disappear and come back.
	TargetKey string
	// Text is the URL exactly as written in the body, which is what the client
	// matches its rendered spans against. Empty for a malicious link, whose text
	// is withheld from the body as well.
	Text string
	// URL is the canonical target. Empty for a malicious link.
	URL string
	// Hostname is the canonical host, punycoded, for display beside the link.
	Hostname string
	Safety   LinkSafety
	Click    LinkClick
	// Href is set only when Click is direct. Its presence *is* the
	// authorisation: a client that receives one may draw an anchor to it.
	Href string
	// UpdatedAt is the target's own version, so a realtime correction can be
	// ordered against what the client already holds.
	UpdatedAt time.Time
	Preview   *LinkPreview
}

// LinkTargetKeyLength is the length of a LinkTargetKey: 128 bits of the digest,
// hex-encoded.
const LinkTargetKeyLength = 32

// LinkTargetKey derives the stable identity of a canonical URL. One-way, so a
// condemned occurrence — whose URL is withheld — can still carry it, and equal
// for every occurrence of the same target in every message. It is an identity,
// not a secret: the URL it names was visible to every reader while pending.
func LinkTargetKey(canonicalURL string) string {
	sum := sha256.Sum256([]byte(canonicalURL))
	return hex.EncodeToString(sum[:LinkTargetKeyLength/2])
}

// LinkAccess is the balanced policy's answer for one safety state (issue #807
// §12). It is the one place clickability and preview eligibility are decided;
// nothing else derives either from a safety value.
//
//	pending    no click, no preview
//	safe       direct anchor, preview eligible
//	malicious  nothing
//	unknown    interstitial, no preview
//
// Preview eligibility is an allowlist of one value on purpose: adding a
// LinkSafety constant cannot make it fetchable until somebody writes it here.
func LinkAccess(safety LinkSafety) (click LinkClick, previewEligible bool) {
	switch safety {
	case LinkSafetySafe:
		return LinkClickDirect, true
	case LinkSafetyUnknown:
		return LinkClickInterstitial, false
	default:
		return LinkClickNone, false
	}
}

// LinkBlockedMarker is the rune the server substitutes for a malicious URL in
// body_text. It is U+FFFC OBJECT REPLACEMENT CHARACTER: it renders as nothing,
// carries no URL, and the client draws the "link blocked" chip in its place.
const LinkBlockedMarker = "￼"
