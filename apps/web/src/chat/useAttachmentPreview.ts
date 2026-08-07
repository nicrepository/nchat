/**
 * Inline attachment preview loading (RF-31, issue #464).
 *
 * # Why an object URL at all
 *
 * The preview route requires an Authorization header, and `<img src>` cannot
 * send one. The alternatives are worse than they look: a token in the query
 * string leaks through history, logs and referrers, and a signed public URL
 * would outlive the access it was minted under. So the bytes are fetched like
 * any other authenticated request and wrapped in a blob URL that exists only in
 * this document, for as long as a component shows it.
 *
 * That URL is a resource, not a string: every one created here is revoked when
 * the attachment changes, when the component unmounts, when a newer response
 * replaces it, and when the image turns out not to be renderable. A missed
 * revoke is a leak the page keeps until it is closed.
 */

import { useCallback, useEffect, useRef, useState } from "react";

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
  const [previewUrl, setPreviewUrl] = useState<string | null>(null);
  // The URL is kept in a ref as well as in state because revoking is a side
  // effect on the *previous* value, and cleanup functions and error callbacks
  // both need it without re-reading a rendered value that may already be stale.
  const currentUrl = useRef<string | null>(null);

  const replaceUrl = useCallback((next: string | null) => {
    if (currentUrl.current !== null) {
      URL.revokeObjectURL(currentUrl.current);
    }
    currentUrl.current = next;
    setPreviewUrl(next);
  }, []);

  const { id } = attachment;
  const eligible = canShowPreview(attachment);

  useEffect(() => {
    // Nothing to load, and nothing to clear either: an attachment that stops
    // being eligible went through this effect's cleanup on the way here, which
    // is where the revoke happens.
    if (!eligible) {
      return;
    }
    // `active` and the abort signal cover the two different races: the request
    // still in flight is cancelled, and a response that arrives anyway — an
    // abort the browser did not honour in time — is dropped before it can
    // become the URL of an attachment nobody is looking at any more.
    let active = true;
    const controller = new AbortController();

    fetchAttachmentPreview(id, controller.signal)
      .then((blob) => {
        if (!active) return;
        replaceUrl(URL.createObjectURL(blob));
      })
      .catch(() => {
        // Every failure means the same thing here: no preview, show the icon.
        // Nothing is retried — a 409 or a 404 would answer the same way every
        // time, and a rerender must not turn that into a request loop.
        if (active) replaceUrl(null);
      });

    return () => {
      active = false;
      controller.abort();
      replaceUrl(null);
    };
    // The dependencies are the attachment's identity and its eligibility, both
    // stable across an ordinary rerender, so the list re-rendering does not
    // refetch anything.
  }, [id, eligible, replaceUrl]);

  const onLoadError = useCallback(() => {
    replaceUrl(null);
  }, [replaceUrl]);

  return { previewUrl, onLoadError };
}
