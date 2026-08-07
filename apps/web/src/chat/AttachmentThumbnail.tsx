/**
 * Inline attachment preview (RF-31, issue #464).
 *
 * The whole feature on the client is one question — may this attachment show a
 * thumbnail, and if so where are the bytes — and it lives here rather than in
 * ConversationDetailsPanel so that the panel keeps rendering a list while this
 * keeps owning an object URL's lifetime. The loading itself, and the revoking
 * that goes with it, is useAttachmentPreview's.
 */

import type { ReactNode } from "react";

import { useAttachmentPreview } from "./useAttachmentPreview";
import type { ChannelAttachment } from "./chatTypes";

interface AttachmentThumbnailProps {
  attachment: ChannelAttachment;
  /** What to draw when there is no preview: the file-type icon. */
  fallback: ReactNode;
}

/**
 * Renders an attachment's preview, or the caller's fallback.
 *
 * The filename reaches the DOM as the image's alt text and nothing else — it is
 * text React escapes, never markup and never part of a URL.
 */
export default function AttachmentThumbnail({ attachment, fallback }: AttachmentThumbnailProps) {
  const { previewUrl, onLoadError } = useAttachmentPreview(attachment);

  if (previewUrl === null) {
    return <>{fallback}</>;
  }
  return (
    <img
      className="chat-details__file-thumb"
      src={previewUrl}
      alt={`Pré-visualização de ${attachment.filename}`}
      onError={onLoadError}
      loading="lazy"
      decoding="async"
      data-testid="chat-details-file-thumb"
    />
  );
}
