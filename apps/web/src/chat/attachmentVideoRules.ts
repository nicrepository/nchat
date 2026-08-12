/**
 * Eligibility for inline video playback (RF-31).
 *
 * # Why there is a size cap
 *
 * The file-service serves byte ranges, so seeking a large video without
 * downloading it is possible on the server side. The browser cannot use it from
 * this application: the content route requires an Authorization header, `<video
 * src>` cannot send one, and the alternatives that would let it are all worse —
 * a token in the URL leaks through history, logs and referrers, and a public
 * route would drop the per-request authorization and the malware-scan gate that
 * make this content safe to serve at all.
 *
 * So the bytes are fetched the way every other authenticated request is and
 * played from a blob URL, which means the whole file is loaded before playback
 * starts. That is acceptable for a short clip and not for a 250 MiB upload, so
 * it is bounded: above MAX_INLINE_VIDEO_BYTES there is no player and the file
 * stays a file. The cap is what keeps this trade-off honest — nothing large is
 * ever pulled down only to be seeked locally.
 *
 * Lifting it means giving the player a transport that can authenticate, which is
 * a service worker fronting the content route. That is the upgrade path; it is
 * not needed for a clip.
 */

import type { ChannelAttachment } from "./chatTypes";

/**
 * The largest attachment that will be loaded for inline playback.
 *
 * It bounds what one row may pull into memory, and it is a client-side comfort
 * limit rather than a control: the server decides what may be read at all, and
 * refuses everything this never asks for anyway.
 */
export const MAX_INLINE_VIDEO_BYTES = 50 * 1024 * 1024;

/**
 * Reports whether an attachment is a video at all.
 *
 * The server's detected content type decides, never the filename: an extension
 * is whatever the uploader typed, while the type on the projection is what
 * file-service sniffed from the bytes it received. A `.mp4` named onto an HTML
 * file is not a video here, and never reaches a media element.
 */
export function isVideoAttachment(attachment: ChannelAttachment): boolean {
  return attachment.contentType.toLowerCase().startsWith("video/");
}

/**
 * Reports whether a player may be drawn.
 *
 * `status === "clean"` is the malware-scan gate, mirrored here so the UI does
 * not spend a request to be told what the metadata already said. It is not the
 * control: the server refuses the content route for every other state, with or
 * without a Range header, so a client that asked anyway would simply be refused.
 */
export function canPlayInline(attachment: ChannelAttachment): boolean {
  return (
    attachment.status === "clean" &&
    isVideoAttachment(attachment) &&
    attachment.size > 0 &&
    attachment.size <= MAX_INLINE_VIDEO_BYTES
  );
}
