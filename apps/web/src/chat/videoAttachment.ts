/**
 * Eligibility for inline video playback (RF-31).
 *
 * The file-service supports ranges, but the browser player cannot attach the
 * required Authorization header. The application therefore fetches the whole
 * file and plays it from a blob URL. Keeping a client-side size cap prevents a
 * large attachment from being pulled into memory merely for inline playback.
 */

import type { ChannelAttachment } from "./chatTypes";

/**
 * The largest attachment that will be loaded for inline playback.
 *
 * This is a client-side comfort limit, not an authorization control. The
 * server remains authoritative for access and malware-scan status.
 */
export const MAX_INLINE_VIDEO_BYTES = 50 * 1024 * 1024;

/**
 * Reports whether the server-detected content type identifies a video.
 * The user-controlled filename is intentionally ignored.
 */
export function isVideoAttachment(attachment: ChannelAttachment): boolean {
  return attachment.contentType.toLowerCase().startsWith("video/");
}

/** Reports whether the client may fetch and render an inline video player. */
export function canPlayInline(attachment: ChannelAttachment): boolean {
  return (
    attachment.status === "clean" &&
    isVideoAttachment(attachment) &&
    attachment.size > 0 &&
    attachment.size <= MAX_INLINE_VIDEO_BYTES
  );
}
