/**
 * Inline video playback for attachments (RF-31, issue #675).
 *
 * A native `<video controls>` for a video the malware scan has cleared, so play,
 * pause and seek are the browser's own and nothing is reimplemented. Anything
 * else — a video still being scanned, a rejected one, one too large to load, or
 * a file that is not a video at all — draws no player, and the row it sits in
 * keeps showing the icon, the size and the status it always did.
 *
 * # Nothing is downloaded before Play (issue #675)
 *
 * The content route needs an Authorization header no `<video src>` can send, so
 * playback happens from a blob URL — which means the *whole file* is fetched
 * before the first frame. Doing that on render, as this component used to, is
 * what made a history with a dozen clips pull tens of megabytes nobody asked
 * for the moment it scrolled past.
 *
 * So the card starts as a poster: a reserved box, a Play button, and zero
 * requests. Pressing Play is what arms the fetch — the same discipline
 * AttachmentAudio already used, applied to the type where the bytes are
 * largest. There is no poster image to fetch either: file-service renders
 * previews for images, PDFs and delimited text only (see domain's
 * previewableMIMEs), so a video's poster is drawn locally and costs nothing.
 *
 * # Playback stops when the card leaves the viewport (issue #675)
 *
 * Bytes that already arrived are kept when a row drifts out of the useful
 * region — re-fetching eight megabytes for a scroll tick would be worse than
 * holding them — but *playing* them is work, and work is what proximity gates.
 * A clip left decoding, and audible, behind a reader who has scrolled away is
 * the failure this pause exists for; it costs nothing to resume, because
 * `pause()` keeps `currentTime` exactly where it was.
 *
 * Coming back into view does not resume. The reader pressed Play once, several
 * screens ago; sound starting again on its own as a row scrolls past would be
 * a surprise, and the controls are right there. Nothing is re-fetched either —
 * proximity never invalidates bytes, only work.
 *
 * Which attachments qualify, and why playback is bounded by size at all, is
 * attachmentVideoRules.ts. The object URL's lifetime is useAttachmentBlobUrl's.
 */

import { useEffect, useRef, useState } from "react";

import { canPlayInline, isVideoAttachment } from "./attachmentVideoRules";
import { useAttachmentGate } from "./lazyAttachment";
import { useAttachmentBlobUrl } from "./useAttachmentBlobUrl";
import { fetchAttachmentContent } from "./filesApi";
import type { ChannelAttachment } from "./chatTypes";

interface AttachmentVideoProps {
  attachment: ChannelAttachment;
}

/**
 * Renders a video attachment's poster and, once asked, its player.
 *
 * Nothing is rendered for a file that is not a video, so the component can be
 * placed unconditionally in a list of mixed attachments and images, PDFs and
 * everything else keep behaving exactly as they did.
 */
export default function AttachmentVideo({ attachment }: AttachmentVideoProps) {
  const [armed, setArmed] = useState(false);
  const playable = canPlayInline(attachment);
  // The lazy gate cannot arm a video on its own — only a click does — but it
  // still applies once armed: a click followed immediately by a scroll far away
  // must not leave the request running for a card nobody can see.
  const gate = useAttachmentGate();
  const videoRef = useRef<HTMLVideoElement>(null);
  // Only ever pauses, never plays: the element is the authority on playback
  // once the reader has started it, and this is the one thing proximity is
  // allowed to say about it.
  const visible = gate.proximity === "visible";
  useEffect(() => {
    if (visible) return;
    const element = videoRef.current;
    if (!element || element.paused) return;
    element.pause();
  }, [visible]);
  const { url, failed } = useAttachmentBlobUrl(
    attachment.id,
    playable && armed,
    fetchAttachmentContent,
    // Explicitly asked for, so it outranks anything merely near the viewport.
    // Scrolling far away while it buffers still stops the transfer; bytes that
    // already arrived are kept, and the playback they feed is paused by the
    // effect above rather than left running out of sight.
    { priority: 0, active: gate.active },
  );

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
  if (!playable) {
    return null;
  }
  if (!armed) {
    return (
      <div className="chat-msg-area__attachment-preview">
        <button
          type="button"
          className="chat-msg-area__attachment-video-poster"
          aria-label={`Reproduzir vídeo: ${attachment.filename}`}
          data-testid={`chat-message-attachment-video-play-${attachment.id}`}
          onClick={() => setArmed(true)}
        >
          <span className="material-symbols-outlined" aria-hidden="true">
            play_circle
          </span>
        </button>
      </div>
    );
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
    // autoPlay only ever runs after the click above, which is the user gesture
    // browsers require for audible playback — a list of attachments still never
    // starts playing on its own. preload is irrelevant to the transfer (the blob
    // is already here) and stays "metadata" so the element sizes itself without
    // decoding frames nobody asked for.
    <video
      ref={videoRef}
      className="chat-details__file-video"
      src={url}
      controls
      autoPlay
      preload="metadata"
      // The filename is text React escapes: a label, never markup and never part
      // of a URL.
      aria-label={`Vídeo: ${attachment.filename}`}
      data-testid="chat-details-file-video"
    />
  );
}
