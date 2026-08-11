/**
 * Inline video playback for attachments (RF-31).
 *
 * A native `<video controls>` for a video the malware scan has cleared, so play,
 * pause and seek are the browser's own and nothing is reimplemented. Anything
 * else — a video still being scanned, a rejected one, one too large to load, or
 * a file that is not a video at all — draws no player, and the row it sits in
 * keeps showing the icon, the size and the status it always did.
 *
 * Which attachments qualify, and why playback is bounded by size at all, is
 * videoAttachment.ts. The object URL's lifetime is useAttachmentBlobUrl's.
 */

import { canPlayInline, isVideoAttachment } from "./videoAttachment";
import { useAttachmentBlobUrl } from "./useAttachmentBlobUrl";
import { fetchAttachmentContent } from "./filesApi";
import type { ChannelAttachment } from "./chatTypes";

interface AttachmentVideoProps {
  attachment: ChannelAttachment;
}

/**
 * Renders a video attachment's player, or the reason there is none.
 *
 * Nothing is rendered for a file that is not a video, so the component can be
 * placed unconditionally in a list of mixed attachments and images, PDFs and
 * everything else keep behaving exactly as they did.
 */
export default function AttachmentVideo({ attachment }: AttachmentVideoProps) {
  const eligible = canPlayInline(attachment);
  const { url, failed } = useAttachmentBlobUrl(attachment.id, eligible, fetchAttachmentContent);

  if (!isVideoAttachment(attachment)) {
    return null;
  }
  // A video still being scanned says so. The row already shows the status, but
  // the absence of a player is the thing that needs explaining: without this it
  // reads as a video that failed to load.
  if (attachment.status === "pending_scan") {
    return (
      <p className="chat-details__file-video-note" data-testid="chat-details-video-pending">
        Em análise. A pré-visualização fica disponível após a verificação.
      </p>
    );
  }
  // Rejected, failed, or simply too large to pull into memory: the row's icon,
  // size and status already say everything, and a message here would repeat it.
  if (!eligible) {
    return null;
  }
  if (failed) {
    return (
      <p className="chat-details__file-video-note" data-testid="chat-details-video-error">
        Não foi possível carregar o vídeo.
      </p>
    );
  }
  if (url === null) {
    return (
      <p
        className="chat-details__file-video-note"
        role="status"
        data-testid="chat-details-video-loading"
      >
        Carregando vídeo…
      </p>
    );
  }
  return (
    // No autoplay: a list of attachments that starts playing sound on its own is
    // hostile, and the browser would block it anyway. preload is irrelevant to
    // the transfer — the blob is already here — and is set to metadata so the
    // element sizes itself without decoding frames nobody asked for.
    <video
      className="chat-details__file-video"
      src={url}
      controls
      preload="metadata"
      // The filename is text React escapes: a label, never markup and never part
      // of a URL.
      aria-label={`Vídeo: ${attachment.filename}`}
      data-testid="chat-details-file-video"
    />
  );
}
