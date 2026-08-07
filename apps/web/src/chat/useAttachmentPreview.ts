/**
 * Inline attachment preview loading (RF-31, issue #464).
 *
 * The preview route requires an Authorization header and `<img src>` cannot
 * send one, so the bytes are fetched like any other authenticated request and
 * shown through an object URL. That URL is a resource with a lifetime, and
 * owning it is useAttachmentBlobUrl's job — this module decides only *which*
 * attachments have a preview to load and what to load for them.
 */

import { useAttachmentBlobUrl } from "./useAttachmentBlobUrl";
import { fetchAttachmentPreview } from "./filesApi";
import type { ChannelAttachment } from "./chatTypes";

/**
 * Decides whether an attachment may show a preview at all.
 *
 * Both halves are required and neither is redundant. `previewStatus` says a
 * preview object exists; `status` says the malware scan cleared the file, which
 * the server enforces on the preview route as strictly as on the download. A
 * client that asked anyway would get a 409 — this just avoids spending a
 * request to be told what the metadata already said.
 */
export function canShowPreview(attachment: ChannelAttachment): boolean {
  return attachment.status === "clean" && attachment.previewStatus === "ready";
}

/**
 * Reports whether this attachment still has asynchronous work that could change
 * what the panel shows.
 *
 * It is the reconciliation predicate — what the panel re-reads its list for —
 * and it follows the *whole* server-side lifecycle, not just its last step:
 *
 *   pending_scan + pending   the scan has not ruled yet. It can still approve,
 *                            and the preview worker then renders. Waiting here
 *                            is the ordinary case for every upload in a
 *                            deployment with the malware scan on;
 *   clean + pending          approved, and the preview worker has not finished;
 *   anything else            nothing that can move on its own.
 *
 * The states that stop it, and why each is genuinely terminal:
 *
 *   - `ready` is the destination. `failed` and `unsupported` are the two ways a
 *     preview ends without one, and neither is retried by the server;
 *   - `rejected` is never claimed by the preview worker, and a rejection also
 *     finalises a pending preview as `unsupported` in the same statement, so a
 *     rejected attachment has nothing left to wait for;
 *   - a removed attachment stops appearing in the listing at all, so there is
 *     no state here to describe.
 *
 * Waiting for a scan is open-ended in a way waiting for a render is not — a
 * stopped scanner never rules — so the *caller* bounds it: see
 * previewReconcileMaxAttempts in useConversationDetails. This predicate answers
 * "could this still change", never "keep asking forever".
 */
export function isPreviewWorkPending(attachment: ChannelAttachment): boolean {
  if (attachment.previewStatus !== "pending") {
    return false;
  }
  return attachment.status === "pending_scan" || attachment.status === "clean";
}

export interface AttachmentPreview {
  /** The object URL to show, or null whenever the fallback belongs on screen. */
  previewUrl: string | null;
  /** For the one failure a fetch cannot see: bytes that will not decode. */
  onLoadError: () => void;
}

/**
 * Loads an attachment's preview and owns the resulting object URL.
 *
 * Returns null whenever there is nothing to show — not eligible, still loading,
 * or the request failed — which is exactly when the caller draws its fallback.
 */
export function useAttachmentPreview(attachment: ChannelAttachment): AttachmentPreview {
  const { url, onLoadError } = useAttachmentBlobUrl(
    attachment.id,
    canShowPreview(attachment),
    fetchAttachmentPreview,
  );
  return { previewUrl: url, onLoadError };
}
