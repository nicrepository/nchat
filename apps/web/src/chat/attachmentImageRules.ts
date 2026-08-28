/**
 * Eligibility for the large inline image/GIF preview (issue #491).
 *
 * # Why some raster types fetch the original and others do not
 *
 * file-service's preview pipeline (services/file-service/internal/preview)
 * produces a static JPEG thumbnail for PNG, JPEG and GIF — for a GIF it decodes
 * only the first frame, so that preview can never animate. WebP is outside the
 * pipeline entirely: it is not in the server's previewable-MIME allowlist, so
 * `previewStatus` never reaches "ready" for it.
 *
 * Two cases therefore need the original bytes, the same way AttachmentVideo
 * needs them to play at all:
 *
 *   - a GIF, to animate it (the server preview is deliberately static);
 *   - a WebP, because there is no server preview to show at all — the browser
 *     decodes WebP natively, so the original is shown directly.
 *
 * Both are bounded by MAX_INLINE_ORIGINAL_IMAGE_BYTES for the same reason
 * MAX_INLINE_VIDEO_BYTES exists: the content route returns a Blob, which means
 * the whole file is loaded before anything is shown, and that is only
 * acceptable up to a size a browser tab can hold comfortably.
 */

import type { ChannelAttachment } from "./chatTypes";

const INLINE_IMAGE_CONTENT_TYPES = new Set(["image/png", "image/jpeg", "image/webp", "image/gif"]);

/**
 * Reports whether an attachment is one of the raster formats this issue gives
 * a large preview to.
 *
 * A deliberate allowlist, not `contentType.startsWith("image/")`: SVG is an
 * image MIME type this must never match, because an inline SVG preview would
 * mean rendering user-supplied active content, which the project's security
 * policy already forbids and this feature does not get to loosen.
 */
export function isImageAttachment(attachment: ChannelAttachment): boolean {
  return INLINE_IMAGE_CONTENT_TYPES.has(attachment.contentType.toLowerCase());
}

/** Reports whether an attachment is a GIF, the one format that can animate. */
export function isGifAttachment(attachment: ChannelAttachment): boolean {
  return attachment.contentType.toLowerCase() === "image/gif";
}

/**
 * The largest original image that will be loaded inline, for the two cases
 * that need it. See the module comment for why only GIF and WebP ever do.
 */
export const MAX_INLINE_ORIGINAL_IMAGE_BYTES = 15 * 1024 * 1024;

/**
 * Reports whether this attachment's inline preview requires the original
 * bytes rather than the server's static preview.
 */
export function needsOriginalForInline(attachment: ChannelAttachment): boolean {
  return isGifAttachment(attachment) || attachment.contentType.toLowerCase() === "image/webp";
}

/**
 * Reports whether the original may be fetched for the inline preview.
 *
 * `status === "clean"` mirrors the malware-scan gate the same way
 * canPlayInline does for video: the server refuses the content route for
 * anything else, so this only avoids spending a request to be told what the
 * metadata already said.
 */
export function canShowOriginalInline(attachment: ChannelAttachment): boolean {
  return (
    attachment.status === "clean" &&
    needsOriginalForInline(attachment) &&
    attachment.size > 0 &&
    attachment.size <= MAX_INLINE_ORIGINAL_IMAGE_BYTES
  );
}

/**
 * Reports whether the original could be fetched inline on size grounds alone,
 * for a clean attachment of *any* raster type — not just GIF/WebP.
 *
 * This exists for one reason: nothing currently pushes a preview-worker
 * completion event into an already-rendered message the way a scan verdict
 * is pushed (see AttachmentImagePreview's own comment). A PNG/JPEG sent in the
 * current session can sit at `previewStatus: "pending"` long after the file is
 * actually clean and its server preview has finished, because the client has
 * no way to learn that short of reloading the conversation. Falling back to
 * the original once the static preview is not ready — bounded by the same cap
 * GIF/WebP already use — avoids that stall instead of waiting on an update
 * that, for a message already on screen, never arrives.
 */
export function withinInlineOriginalCap(attachment: ChannelAttachment): boolean {
  return (
    attachment.status === "clean" &&
    attachment.size > 0 &&
    attachment.size <= MAX_INLINE_ORIGINAL_IMAGE_BYTES
  );
}
