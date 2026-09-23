package service

import (
	"context"
	"errors"
	"log/slog"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// Convergence of a link target into the messages that name it (issue #807).
//
// A verdict is written once, on the target row. Everything that has to follow
// from it — the aggregate marker every message still carries for its quotes,
// the per-link state every client draws, the preview that may now be fetched or
// must now be torn down — is this type's job, and it is one type so that a
// verdict reached by the scan worker, by the deadline sweep and by
// reconciliation converges identically.

// LinkUpdatePublisher tells a conversation that one of its message's links
// changed state. The payload is the link entity, so a client patches every
// occurrence of the URL in that message and nothing else.
type LinkUpdatePublisher interface {
	PublishMessageLinkUpdated(ctx context.Context, workspaceID, targetType, targetID, messageID string, link domain.MessageLink)
}

// LinkTargetIndex is the durable half the announcer reads and writes.
type LinkTargetIndex interface {
	messageLinkSafetyRefresher
	LoadLinkTargets(ctx context.Context, canonicalURLs []string) (map[string]storage.LinkTargetState, error)
	MessagesReferencingLink(ctx context.Context, canonicalURL, workspaceID, afterMessageID string, limit int) ([]storage.LinkReference, error)
	LinkWorkspacesReferencing(ctx context.Context, canonicalURL string) ([]string, error)
	LoadLinkPreviews(ctx context.Context, workspaceID string, canonicalURLs []string) (map[string]storage.LinkPreviewRow, error)
	QueueLinkPreviews(ctx context.Context, workspaceID string, canonicalURLs []string) error
	RevokeLinkPreviews(ctx context.Context, canonicalURL string) ([]storage.LinkPreviewRow, error)
	// The fan-out continuation: one page per claim, the cursor durable.
	BeginLinkFanout(ctx context.Context, canonicalURL, workspaceID, kind string) (storage.LinkFanout, error)
	ClaimDueLinkFanouts(ctx context.Context, limit int) ([]storage.LinkFanout, error)
	// Advance and Finish settle the claim the fanout identifies; a superseded
	// claim answers storage.ErrLinkFanoutConflict and changes nothing.
	AdvanceLinkFanout(ctx context.Context, claim storage.LinkFanout, afterMessageID string) error
	FinishLinkFanout(ctx context.Context, claim storage.LinkFanout) error
}

const (
	// linkFanoutPage bounds one page of referencing messages: one query and at
	// most this many publications, then the cursor is written down.
	linkFanoutPage = 200
	// linkFanoutClaimBatch and linkFanoutPassRounds bound one Continue pass:
	// at most rounds × batch pages, re-claimed each round in waiting order so
	// several targets converging at once share the pass rather than queue
	// behind the largest.
	linkFanoutClaimBatch = 10
	linkFanoutPassRounds = 5
)

// LinkTargetAnnouncer converges one target's new state into every message that
// names it.
type LinkTargetAnnouncer struct {
	index          LinkTargetIndex
	links          LinkUpdatePublisher
	safety         LinkSafetyChangePublisher
	previewEnabled bool
	logger         *slog.Logger
}

// NewLinkTargetAnnouncer builds the announcer. Either publisher may be nil, in
// which case the corresponding event is simply not sent — the durable state is
// already written and the next read shows it.
func NewLinkTargetAnnouncer(index LinkTargetIndex, links LinkUpdatePublisher, safety LinkSafetyChangePublisher, logger *slog.Logger) *LinkTargetAnnouncer {
	if logger == nil {
		logger = slog.Default()
	}
	return &LinkTargetAnnouncer{index: index, links: links, safety: safety, logger: logger}
}

// SetPublishers attaches the realtime publishers once the hub exists.
func (a *LinkTargetAnnouncer) SetPublishers(links LinkUpdatePublisher, safety LinkSafetyChangePublisher) {
	a.links, a.safety = links, safety
}

// SetPreviewEnabled records whether a clearance should queue a preview.
func (a *LinkTargetAnnouncer) SetPreviewEnabled(enabled bool) {
	a.previewEnabled = enabled
}

// Announce converges canonicalURL after its target row changed. Every step is
// best-effort and logged without the URL: the row is the truth, and a client
// that misses an event resyncs on its next read. The realtime fan-out runs one
// page here and continues, durably, from Continue.
//
// The aggregate correction (message.link_safety_changed) is published before
// the per-link fan-out, for the clients that only understand the aggregate. A
// client that holds the per-link model treats the aggregate as a compatibility
// projection: it never withholds a body the server already redacted span by
// span, so the order of the two events cannot cost it legitimate text.
func (a *LinkTargetAnnouncer) Announce(ctx context.Context, canonicalURL string) {
	if a == nil || a.index == nil {
		return
	}
	targets, err := a.index.LoadLinkTargets(ctx, []string{canonicalURL})
	if err != nil {
		a.warn(ctx, "load link target", err)
		return
	}
	a.settlePreviews(ctx, canonicalURL, linkSafetyFor(targets[canonicalURL]))
	if err := drainMessageLinkSafety(ctx, a.index, canonicalURL, a.safety); err != nil {
		a.warn(ctx, "converge aggregate link safety", err)
	}
	a.beginFanout(ctx, canonicalURL, "", storage.LinkFanoutKindTarget)
}

// AnnouncePreview tells the messages of one workspace that a preview changed.
func (a *LinkTargetAnnouncer) AnnouncePreview(ctx context.Context, preview storage.LinkPreviewRow) {
	if a == nil || a.index == nil {
		return
	}
	a.beginFanout(ctx, preview.CanonicalURL, preview.WorkspaceID, storage.LinkFanoutKindPreview)
}

// Continue runs the fan-outs waiting for their next page, bounded per call,
// and reports how many pages it ran. Called from the scan worker's pass, so a
// continuation survives the process that began it.
func (a *LinkTargetAnnouncer) Continue(ctx context.Context) int {
	if a == nil || a.index == nil || a.links == nil {
		return 0
	}
	pages := 0
	for round := 0; round < linkFanoutPassRounds && ctx.Err() == nil; round++ {
		fanouts, err := a.index.ClaimDueLinkFanouts(ctx, linkFanoutClaimBatch)
		if err != nil {
			a.warn(ctx, "claim due link fanouts", err)
			return pages
		}
		if len(fanouts) == 0 {
			return pages
		}
		for _, fanout := range fanouts {
			a.runPage(ctx, fanout)
			pages++
		}
	}
	return pages
}

// beginFanout records the fan-out durably and runs its first page at once, so
// the common case — a URL in a handful of messages — converges in this pass.
func (a *LinkTargetAnnouncer) beginFanout(ctx context.Context, canonicalURL, workspaceID, kind string) {
	if a.links == nil {
		return
	}
	fanout, err := a.index.BeginLinkFanout(ctx, canonicalURL, workspaceID, kind)
	if err != nil {
		a.warn(ctx, "begin link fanout", err)
		return
	}
	a.runPage(ctx, fanout)
}

// runPage announces the target's current state to one page of the messages
// naming it, then advances or finishes the continuation. A failed page leaves
// the cursor where it was; the lease lapsing is the retry. Re-announcing a
// page is harmless: the payload is state, not a delta.
func (a *LinkTargetAnnouncer) runPage(ctx context.Context, fanout storage.LinkFanout) {
	refs, err := a.index.MessagesReferencingLink(ctx, fanout.CanonicalURL, fanout.WorkspaceID, fanout.AfterMessageID, linkFanoutPage)
	if err != nil {
		a.warn(ctx, "list messages referencing link", err)
		return
	}
	targets, err := a.index.LoadLinkTargets(ctx, []string{fanout.CanonicalURL})
	if err != nil {
		a.warn(ctx, "load link target for fan-out", err)
		return
	}
	target := targets[fanout.CanonicalURL]
	payloads := map[string]domain.MessageLink{}
	for _, ref := range refs {
		link := a.cachedWorkspacePayload(ctx, payloads, fanout.CanonicalURL, ref.WorkspaceID, target)
		a.links.PublishMessageLinkUpdated(ctx, ref.WorkspaceID, ref.TargetType, ref.TargetID, ref.MessageID, link)
	}
	if len(refs) < linkFanoutPage {
		err = a.index.FinishLinkFanout(ctx, fanout)
	} else {
		err = a.index.AdvanceLinkFanout(ctx, fanout, refs[len(refs)-1].MessageID)
	}
	// A lost compare-and-set is another claim's win — a reclaim after this
	// lease lapsed, or a restart by a newer announcement — not an error: the
	// current claim owns the cursor, and what this page published was the
	// target's state as re-read a moment ago.
	if err != nil && !errors.Is(err, storage.ErrLinkFanoutConflict) {
		a.warn(ctx, "record link fanout progress", err)
	}
}

// settlePreviews queues a preview in every workspace naming a freshly cleared
// URL, and revokes every preview of a URL that is no longer safe.
func (a *LinkTargetAnnouncer) settlePreviews(ctx context.Context, canonicalURL string, safety domain.LinkSafety) {
	if safety != domain.LinkSafetySafe {
		if _, err := a.index.RevokeLinkPreviews(ctx, canonicalURL); err != nil {
			a.warn(ctx, "revoke link previews", err)
		}
		return
	}
	if a.previewEnabled {
		a.queuePreviews(ctx, canonicalURL)
	}
}

// queuePreviews asks for a card in every workspace naming a cleared URL.
func (a *LinkTargetAnnouncer) queuePreviews(ctx context.Context, canonicalURL string) {
	workspaces, err := a.index.LinkWorkspacesReferencing(ctx, canonicalURL)
	if err != nil {
		a.warn(ctx, "list workspaces for preview", err)
		return
	}
	for _, workspace := range workspaces {
		if err := a.index.QueueLinkPreviews(ctx, workspace, []string{canonicalURL}); err != nil {
			a.warn(ctx, "queue link preview", err)
		}
	}
}

// cachedWorkspacePayload builds each workspace's payload once per page.
func (a *LinkTargetAnnouncer) cachedWorkspacePayload(
	ctx context.Context, payloads map[string]domain.MessageLink, canonicalURL, workspaceID string, target storage.LinkTargetState,
) domain.MessageLink {
	link, ok := payloads[workspaceID]
	if !ok {
		link = a.workspacePayload(ctx, canonicalURL, workspaceID, target)
		payloads[workspaceID] = link
	}
	return link
}

// workspacePayload builds the link entity for one workspace, with that
// workspace's preview when the target is safe and one exists.
func (a *LinkTargetAnnouncer) workspacePayload(ctx context.Context, canonicalURL, workspaceID string, target storage.LinkTargetState) domain.MessageLink {
	var preview *storage.LinkPreviewRow
	if a.previewEnabled && linkSafetyFor(target) == domain.LinkSafetySafe {
		previews, err := a.index.LoadLinkPreviews(ctx, workspaceID, []string{canonicalURL})
		if err != nil {
			a.warn(ctx, "load link preview for fan-out", err)
		} else if row, ok := previews[canonicalURL]; ok {
			preview = &row
		}
	}
	return a.linkPayload(canonicalURL, target, preview)
}

// linkPayload is the target-level entity a client patches its occurrences
// with. Text and Ordinal are per-occurrence and therefore absent; the client
// matches by TargetKey, which every occurrence carries whether or not its URL
// is visible. A condemned target carries no URL: the client re-reads the
// message and receives the redacted body.
func (a *LinkTargetAnnouncer) linkPayload(canonicalURL string, target storage.LinkTargetState, preview *storage.LinkPreviewRow) domain.MessageLink {
	link := linkFor(0, linkOccurrence{canonical: canonicalURL}, target)
	link.Text = ""
	if preview != nil && link.Safety == domain.LinkSafetySafe && a.previewEnabled {
		link.Preview = previewFor(*preview)
	}
	return link
}

func (a *LinkTargetAnnouncer) warn(ctx context.Context, step string, err error) {
	if ctx.Err() == nil {
		a.logger.WarnContext(ctx, step, slog.String("error", err.Error()))
	}
}
