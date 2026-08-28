/**
 * Object-URL lifetime for authenticated attachment bytes (RF-31).
 *
 * # Why an object URL at all
 *
 * Every attachment route requires an Authorization header, and neither `<img
 * src>` nor `<video src>` can send one. The alternatives are worse than they
 * look: a token in the query string leaks through history, logs and referrers,
 * and a signed public URL would outlive the access it was minted under. So the
 * bytes are fetched like any other authenticated request and wrapped in a blob
 * URL that exists only in this document, for as long as a component shows it.
 *
 * That URL is a resource, not a string: every one created here is revoked when
 * the attachment changes, when the component unmounts, when a newer response
 * replaces it, and when the bytes turn out not to be renderable. A missed revoke
 * is a leak the page keeps until it is closed.
 *
 * This hook owns that lifetime and nothing else. What is fetched, and whether an
 * attachment is eligible at all, belongs to the caller — the preview is a
 * thumbnail and the player is a video, but the resource discipline is identical
 * and must not be written twice.
 */

import { useCallback, useEffect, useRef, useState } from "react";

/** Fetches one attachment's bytes. Must be stable across renders. */
export type AttachmentBlobFetcher = (attachmentId: string, signal?: AbortSignal) => Promise<Blob>;

export interface AttachmentBlobUrl {
  /** The object URL to show, or null whenever the fallback belongs on screen. */
  url: string | null;
  /**
   * Whether the bytes could not be obtained or could not be rendered. It is
   * distinct from `url === null`, which is also what "still loading" and "not
   * eligible" look like: a caller that wants to say *why* nothing is showing
   * needs the difference.
   */
  failed: boolean;
  /** For the one failure a fetch cannot see: bytes the element will not decode. */
  onLoadError: () => void;
}

/**
 * Fetches an attachment's bytes and owns the resulting object URL.
 *
 * Returns a null URL whenever there is nothing to show — not eligible, still
 * loading, or the request failed — which is exactly when the caller draws its
 * fallback.
 */
export function useAttachmentBlobUrl(
  attachmentId: string,
  eligible: boolean,
  fetchBlob: AttachmentBlobFetcher,
): AttachmentBlobUrl {
  // One piece of state, not two. The URL and the reason it is absent always
  // change together, and splitting them would mean a render in which a stale
  // "failed" describes a URL that has already been replaced.
  const [state, setState] = useState<{ url: string | null; failed: boolean }>({
    url: null,
    failed: false,
  });
  // The URL is kept in a ref as well as in state because revoking is a side
  // effect on the *previous* value, and cleanup functions and error callbacks
  // both need it without re-reading a rendered value that may already be stale.
  const currentUrl = useRef<string | null>(null);

  const replaceUrl = useCallback((next: string | null, failed = false) => {
    if (currentUrl.current !== null) {
      URL.revokeObjectURL(currentUrl.current);
    }
    currentUrl.current = next;
    setState({ url: next, failed });
  }, []);

  useEffect(() => {
    // Nothing to load, and nothing to clear either: an attachment that stops
    // being eligible went through this effect's cleanup on the way here, which
    // is both where the revoke happens and where the failure is forgotten.
    if (!eligible) {
      return;
    }
    // `active` and the abort signal cover the two different races: the request
    // still in flight is cancelled, and a response that arrives anyway — an
    // abort the browser did not honour in time — is dropped before it can
    // become the URL of an attachment nobody is looking at any more.
    let active = true;
    const controller = new AbortController();

    fetchBlob(attachmentId, controller.signal)
      .then((blob) => {
        if (!active) return;
        replaceUrl(URL.createObjectURL(blob));
      })
      .catch(() => {
        // Every failure means the same thing here: no bytes, draw the fallback.
        // Nothing is retried — a 409 or a 404 would answer the same way every
        // time, and a rerender must not turn that into a request loop.
        if (!active) return;
        replaceUrl(null, true);
      });

    return () => {
      active = false;
      controller.abort();
      replaceUrl(null);
    };
    // The dependencies are the attachment's identity, its eligibility and the
    // fetcher, all stable across an ordinary rerender, so the list re-rendering
    // does not refetch anything.
  }, [attachmentId, eligible, fetchBlob, replaceUrl]);

  const onLoadError = useCallback(() => {
    replaceUrl(null, true);
  }, [replaceUrl]);

  return { url: state.url, failed: state.failed, onLoadError };
}
