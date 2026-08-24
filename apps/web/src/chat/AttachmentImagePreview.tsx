/**
 * Large inline image/GIF preview (issue #491).
 *
 * Not a rework of AttachmentThumbnail: that component still owns the 32×32
 * thumbnail for the details panel and for every other attachment type this
 * issue leaves alone (PDF included, which already gets a server preview).
 * This one is the message list's own, and only for the four raster types
 * attachmentImageRules allows — SVG never reaches here.
 *
 * # Which bytes get fetched
 *
 * PNG and JPEG show the server's static preview when it is ready — the same
 * one AttachmentThumbnail already fetches, just drawn bigger. GIF and WebP are
 * different, and for different reasons (see attachmentImageRules): a GIF's
 * server preview is deliberately a single static frame, so animating it needs
 * the original; WebP has no server preview at all. Both are bounded by
 * MAX_INLINE_ORIGINAL_IMAGE_BYTES the same way AttachmentVideo bounds its own
 * blob fetch.
 *
 * PNG/JPEG fall back to that same original-fetch, under the same cap, when the
 * static preview is *not* ready — not only "unsupported"/"failed", but also
 * "pending". That last one matters more than it looks: a scan verdict
 * (`attachment.status`) reaching "clean" is pushed into an already-rendered
 * message over WebSocket, but a preview-worker completion is not — nothing
 * currently turns a message's `previewStatus: "pending"` into "ready" once the
 * message is on screen. Without this fallback, a PNG/JPEG sent in the current
 * session would sit on the loading skeleton until the conversation is
 * reloaded, even though the file itself is already clean and viewable. GIF and
 * WebP never had this problem because they never depend on `previewStatus` at
 * all.
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

import {
  canShowOriginalInline,
  isGifAttachment,
  withinInlineOriginalCap,
} from "./attachmentImageRules";
import { useAttachmentBlobUrl } from "./useAttachmentBlobUrl";
import { canShowPreview, isPreviewWorkPending } from "./useAttachmentPreview";
import { usePrefersReducedMotion } from "./useReducedMotion";
import { fetchAttachmentContent, fetchAttachmentPreview } from "./filesApi";
import type { ChannelAttachment } from "./chatTypes";

export interface AttachmentImageOpenPayload {
  /** The button the click/keypress came from — the lightbox returns focus here. */
  trigger: HTMLButtonElement;
  /** Whichever URL the box was already showing, so the lightbox can reuse it. */
  url: string;
  /** True when `url` is already the original (GIF playing, or WebP). */
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
  // Not reset on an attachment change: the caller (MessageAttachments) keys
  // this component's parent by attachment.id, so a different attachment is
  // already a fresh mount with this back at its initial false — there is no
  // render in which the id changes under an existing instance.
  const [userPlayedGif, setUserPlayedGif] = useState(false);

  const isWebp = attachment.contentType.toLowerCase() === "image/webp";
  const hasReadyPreview = canShowPreview(attachment);

  // GIF/WebP always may use the original, unconditionally, for their own
  // reasons (see the module comment). PNG/JPEG only fall back to it once the
  // static preview turns out not to be ready — see the module comment on why
  // that fallback is what keeps a freshly-sent image from stalling.
  const gifOrWebpOriginal = canShowOriginalInline(attachment);
  const pngJpegFallback =
    !gif && !isWebp && !hasReadyPreview && withinInlineOriginalCap(attachment);

  const animateGif = gif && gifOrWebpOriginal && (!reducedMotion || userPlayedGif);
  const showOriginal = gif ? animateGif : isWebp ? gifOrWebpOriginal : pngJpegFallback;

  const original = useAttachmentBlobUrl(attachment.id, showOriginal, fetchAttachmentContent);

  // A GIF wants the static preview exactly when it is not currently showing
  // the animated original; PNG/JPEG want it exactly when they are not falling
  // back to the original; WebP never has one to want.
  const wantsStaticPreview = gif ? !animateGif : !isWebp && !pngJpegFallback;
  const previewEligible = wantsStaticPreview && hasReadyPreview;
  const preview = useAttachmentBlobUrl(attachment.id, previewEligible, fetchAttachmentPreview);

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

  if (showOriginal) {
    if (original.failed) return <>{fallback}</>;
    if (original.url === null) {
      return <Skeleton attachmentId={attachment.id} />;
    }
    const url = original.url;
    return (
      <div className="chat-msg-area__attachment-preview">
        <button
          type="button"
          className="chat-msg-area__attachment-preview-trigger"
          aria-label={`Ampliar ${attachment.filename}`}
          data-testid={`chat-message-attachment-image-${attachment.id}`}
          onClick={(event) => onOpen({ trigger: event.currentTarget, url, isOriginal: true })}
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

  if (wantsStaticPreview) {
    if (preview.failed) return <>{fallback}</>;
    if (preview.url === null) {
      if (previewEligible || isPreviewWorkPending(attachment)) {
        return <Skeleton attachmentId={attachment.id} />;
      }
      return <>{fallback}</>;
    }
    const url = preview.url;
    return (
      <div className="chat-msg-area__attachment-preview">
        <button
          type="button"
          className="chat-msg-area__attachment-preview-trigger"
          aria-label={`Ampliar ${attachment.filename}`}
          data-testid={`chat-message-attachment-image-${attachment.id}`}
          onClick={(event) => onOpen({ trigger: event.currentTarget, url, isOriginal: false })}
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
        {gif && reducedMotion && !userPlayedGif && gifOrWebpOriginal && (
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

  return <>{fallback}</>;
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
