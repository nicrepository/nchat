/**
 * Large inline image/GIF preview (issue #491).
 *
 * Not a rework of AttachmentThumbnail: that component still owns the 32×32
 * thumbnail for the details panel and for every other attachment type this
 * issue leaves alone (PDF included, which already gets a server preview).
 * This one is the message list's own, and only for the four raster types
 * attachmentImageRules allows — SVG never reaches here.
 *
 * # Which bytes get fetched (issue #675)
 *
 * The timeline draws derived previews and nothing else. A card is never worth
 * an original: a screen of a busy channel would otherwise be tens of megabytes
 * of full-resolution photographs to render thumbnails of them.
 *
 * So a static image — PNG, JPEG, WebP alike — shows the server's preview when
 * it is ready, and a shell when it is not. Two consequences are deliberate and
 * were accepted with this issue:
 *
 * - a WebP has no server preview at all (see attachmentImageRules), so it draws
 *   the file-type icon in the timeline. Opening it still shows the real image:
 *   the viewer is where an original belongs, and AttachmentLightbox fetches it;
 * - a PNG/JPEG whose preview is still "pending" keeps its skeleton until the
 *   preview lands. A scan verdict is pushed over WebSocket but a preview-worker
 *   completion is not, so an image sent in the current session can sit on that
 *   skeleton until the conversation is reloaded. Fetching the original to cover
 *   it is exactly the cost this issue exists to remove; the fix belongs in the
 *   preview-ready notification, not here.
 *
 * The single exception is a GIF's animation, which has no derived form — the
 * server's GIF preview is deliberately one static frame. So the animated
 * original is fetched, under MAX_INLINE_ORIGINAL_IMAGE_BYTES, and *only while
 * the row is actually on screen*. Scrolling away drops back to the static
 * frame, which is what stops a timeline of GIFs from decoding in the
 * background.
 *
 * # Reduced motion
 *
 * `prefers-reduced-motion` cannot stop a native `<img>` GIF from animating —
 * no CSS rule reaches into decoded frame timing. So when it is active, a GIF
 * shows the server's already-static preview instead of ever fetching the
 * animated original, and a "Reproduzir animação" button is the only way the
 * original gets fetched at all. That trade only exists for GIF: WebP has no
 * static preview to fall back to, so this issue does not attempt to detect an
 * animated WebP — an edge case the product brief does not call out, and one a
 * canvas-capture workaround would add real risk for.
 */

import { type ReactNode, useState } from "react";

import { canShowOriginalInline, isGifAttachment } from "./attachmentImageRules";
import { useAttachmentGate } from "./lazyAttachment";
import { useAttachmentBlobUrl } from "./useAttachmentBlobUrl";
import { canShowPreview, isPreviewWorkPending } from "./useAttachmentPreview";
import { usePrefersReducedMotion } from "./useReducedMotion";
import { fetchAttachmentContent, fetchAttachmentPreview } from "./filesApi";
import type { ChannelAttachment } from "./chatTypes";

export interface AttachmentImageOpenPayload {
  /** The button the click/keypress came from — the lightbox returns focus here. */
  trigger: HTMLButtonElement;
  /**
   * The bytes the box was already showing, so the viewer opens on them instead
   * of a blank box.
   *
   * The blob rather than the object URL (issue #675): this card can be
   * unmounted by the timeline's virtualization while the viewer is still open,
   * and its URL is revoked on the way out. The blob outlives it, and the viewer
   * mints — and revokes — an address of its own.
   */
  blob: Blob;
  /** True when `blob` is already the original (GIF playing, or WebP). */
  isOriginal: boolean;
}

interface AttachmentImagePreviewProps {
  attachment: ChannelAttachment;
  /** What to draw when there is nothing to preview — the file-type icon. */
  fallback: ReactNode;
  onOpen: (payload: AttachmentImageOpenPayload) => void;
}

/**
 * Renders the large preview box, or the reason there is none.
 *
 * Placed unconditionally by the caller for any raster attachment: a rejected
 * or still-scanning file draws no box, and the row's own name/size/status
 * already says why.
 */
export default function AttachmentImagePreview({
  attachment,
  fallback,
  onOpen,
}: AttachmentImagePreviewProps) {
  const gif = isGifAttachment(attachment);
  const reducedMotion = usePrefersReducedMotion();
  // Issue #675: far from the viewport this stays a reserved box and fetches
  // nothing. The gate never grants anything the rules below would have refused.
  const gate = useAttachmentGate();
  // Not reset on an attachment change: the caller (MessageAttachments) keys
  // this component's parent by attachment.id, so a different attachment is
  // already a fresh mount with this back at its initial false — there is no
  // render in which the id changes under an existing instance.
  const [userPlayedGif, setUserPlayedGif] = useState(false);

  const hasReadyPreview = canShowPreview(attachment);

  // The one original the timeline may still spend, and only while the row is
  // genuinely on screen: a GIF's animation has no derived form. Everything
  // else — PNG, JPEG, WebP — is preview-or-shell (issue #675).
  const mayAnimate = canShowOriginalInline(attachment) && (!reducedMotion || userPlayedGif);
  const animateGif = gif && mayAnimate && gate.proximity === "visible";

  const original = useAttachmentBlobUrl(attachment.id, animateGif, fetchAttachmentContent, {
    priority: gate.priority,
    active: gate.active,
  });

  // The static preview is what every card shows, except a GIF in the moment it
  // is animating.
  const previewEligible = !animateGif && hasReadyPreview;
  const preview = useAttachmentBlobUrl(attachment.id, previewEligible, fetchAttachmentPreview, {
    priority: gate.priority,
    active: gate.active,
  });

  if (attachment.status === "pending_scan") {
    return (
      <p
        className="chat-msg-area__attachment-note"
        data-testid={`chat-message-attachment-image-pending-${attachment.id}`}
      >
        Em análise. A pré-visualização fica disponível após a verificação.
      </p>
    );
  }
  if (attachment.status !== "clean") {
    return null;
  }

  if (animateGif) {
    if (original.failed) return <>{fallback}</>;
    if (original.url === null || original.blob === null) {
      return <Skeleton attachmentId={attachment.id} />;
    }
    const url = original.url;
    const blob = original.blob;
    return (
      <div className="chat-msg-area__attachment-preview">
        <button
          type="button"
          className="chat-msg-area__attachment-preview-trigger"
          aria-label={`Ampliar ${attachment.filename}`}
          data-testid={`chat-message-attachment-image-${attachment.id}`}
          onClick={(event) => onOpen({ trigger: event.currentTarget, blob, isOriginal: true })}
        >
          <img
            className="chat-msg-area__attachment-preview-img"
            src={url}
            alt=""
            loading="lazy"
            decoding="async"
            onError={original.onLoadError}
          />
        </button>
      </div>
    );
  }

  if (preview.failed) return <>{fallback}</>;
  if (preview.url === null || preview.blob === null) {
    // A shell, never an original: still loading, still being rendered by the
    // preview worker, or waiting for this row to come near enough to ask.
    if (previewEligible || isPreviewWorkPending(attachment)) {
      return <Skeleton attachmentId={attachment.id} />;
    }
    return <>{fallback}</>;
  }
  {
    const url = preview.url;
    const blob = preview.blob;
    return (
      <div className="chat-msg-area__attachment-preview">
        <button
          type="button"
          className="chat-msg-area__attachment-preview-trigger"
          aria-label={`Ampliar ${attachment.filename}`}
          data-testid={`chat-message-attachment-image-${attachment.id}`}
          onClick={(event) => onOpen({ trigger: event.currentTarget, blob, isOriginal: false })}
        >
          <img
            className="chat-msg-area__attachment-preview-img"
            src={url}
            alt=""
            loading="lazy"
            decoding="async"
            onError={preview.onLoadError}
          />
        </button>
        {gif && reducedMotion && !userPlayedGif && canShowOriginalInline(attachment) && (
          <button
            type="button"
            className="chat-msg-area__attachment-gif-toggle"
            data-testid={`chat-message-attachment-gif-toggle-${attachment.id}`}
            onClick={() => setUserPlayedGif(true)}
          >
            Reproduzir animação
          </button>
        )}
      </div>
    );
  }
}

function Skeleton({ attachmentId }: { attachmentId: string }) {
  return (
    <div className="chat-msg-area__attachment-preview">
      <div
        className="chat-msg-area__attachment-preview-skeleton"
        role="status"
        aria-label="Carregando pré-visualização…"
        data-testid={`chat-message-attachment-image-loading-${attachmentId}`}
      />
    </div>
  );
}
