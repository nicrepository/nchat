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
 *
 * # Scheduling and proximity (issue #675)
 *
 * The request goes through previewScheduler rather than straight to the
 * network, so a timeline that brings thirty attachments into range at once
 * starts a handful and queues the rest, visible ones first.
 *
 * Two gates, and they are not the same question:
 *
 *   eligible   may this attachment have these bytes at all — the scan verdict,
 *              the preview status, the size cap. Losing it means the bytes must
 *              go: the URL is revoked, exactly as it always was.
 *   active     may work run right now — proximity to the scrollport. Losing it
 *              stops *work*, never results: an in-flight request is aborted and
 *              a queued one is dropped, but bytes that already arrived stay on
 *              screen. Revoking those would buy a flicker and a second request
 *              for a row that is one scroll tick away.
 *
 * That split is why `active` is not in the fetch effect's dependency list: it
 * starts and stops the work inside a single subscription instead of tearing the
 * subscription down, which is what would take the finished bytes with it.
 */

import { useCallback, useEffect, useRef, useState } from "react";

import {
  promoteAttachmentFetch,
  scheduleAttachmentFetch,
  type PreviewPriority,
} from "./previewScheduler";

/** Fetches one attachment's bytes. Must be stable across renders. */
export type AttachmentBlobFetcher = (attachmentId: string, signal?: AbortSignal) => Promise<Blob>;

/**
 * A stable scheduler key per fetcher, so an attachment's preview and its
 * original are two separate pieces of work while two components asking for the
 * same one share a single request.
 *
 * Keyed on the function's identity rather than its `name`: a production build
 * may rename it, and two different fetchers colliding into one key would serve
 * one component the other's bytes.
 */
const fetcherIds = new WeakMap<AttachmentBlobFetcher, number>();
let nextFetcherId = 0;

function schedulerKey(fetchBlob: AttachmentBlobFetcher, attachmentId: string): string {
  let id = fetcherIds.get(fetchBlob);
  if (id === undefined) {
    id = ++nextFetcherId;
    fetcherIds.set(fetchBlob, id);
  }
  return `${id}:${attachmentId}`;
}

export interface AttachmentBlobUrl {
  /** The object URL to show, or null whenever the fallback belongs on screen. */
  url: string | null;
  /**
   * The bytes behind `url`, for the one caller that must outlive this component:
   * a viewer opened from a message the timeline may virtualize away can mint its
   * own object URL from the same blob instead of holding a URL this hook is
   * about to revoke (issue #675). Null whenever `url` is.
   */
  blob: Blob | null;
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
export interface AttachmentBlobOptions {
  /**
   * Where this request sits in the preview queue (issue #675). The default is
   * P0 so every caller that has not been taught about proximity — the details
   * panel's file list, the viewers — keeps behaving exactly as it did.
   */
  priority?: PreviewPriority;
  /**
   * Whether work may run right now. Defaults to true for the same reason: a
   * caller outside a lazy container is always allowed to load.
   */
  active?: boolean;
}

export function useAttachmentBlobUrl(
  attachmentId: string,
  eligible: boolean,
  fetchBlob: AttachmentBlobFetcher,
  options: AttachmentBlobOptions = {},
): AttachmentBlobUrl {
  const { priority = 0, active = true } = options;
  // One piece of state, not three. The URL, its bytes and the reason it is
  // absent always change together, and splitting them would mean a render in
  // which a stale "failed" describes a URL that has already been replaced.
  const [state, setState] = useState<{ url: string | null; blob: Blob | null; failed: boolean }>({
    url: null,
    blob: null,
    failed: false,
  });
  // The URL is kept in a ref as well as in state because revoking is a side
  // effect on the *previous* value, and cleanup functions and error callbacks
  // both need it without re-reading a rendered value that may already be stale.
  const currentUrl = useRef<string | null>(null);
  const priorityRef = useRef(priority);
  const activeRef = useRef(active);
  /**
   * Re-evaluates the current subscription against `activeRef`. Owned by the
   * fetch effect, which is the only thing that knows what "current" means.
   */
  const reconcileRef = useRef<(() => void) | null>(null);

  const replaceUrl = useCallback((next: string | null, blob: Blob | null, failed = false) => {
    if (currentUrl.current !== null) {
      URL.revokeObjectURL(currentUrl.current);
    }
    currentUrl.current = next;
    setState({ url: next, blob, failed });
  }, []);

  const key = schedulerKey(fetchBlob, attachmentId);

  // Priority promotion, not a refetch: an attachment scrolling from the
  // prefetch region into the viewport must jump the queue, and must not start
  // its request over. A task already running ignores this.
  useEffect(() => {
    priorityRef.current = priority;
    promoteAttachmentFetch(key, priority);
  }, [key, priority]);

  useEffect(() => {
    // Nothing to load, and nothing to clear either: an attachment that stops
    // being eligible went through this effect's cleanup on the way here, which
    // is both where the revoke happens and where the failure is forgotten.
    if (!eligible) {
      return;
    }
    // `live` and the abort signal cover the two different races: the request
    // still in flight is cancelled, and a response that arrives anyway — an
    // abort the browser did not honour in time — is dropped before it can
    // become the URL of an attachment nobody is looking at any more.
    let live = true;
    let settled = false;
    let controller: AbortController | null = null;

    const start = () => {
      if (!live || settled || controller) return;
      const started = new AbortController();
      controller = started;
      scheduleAttachmentFetch(key, priorityRef.current, started.signal, (signal) =>
        fetchBlob(attachmentId, signal),
      )
        .then((blob) => {
          if (!live || controller !== started) return;
          settled = true;
          replaceUrl(URL.createObjectURL(blob), blob);
        })
        .catch(() => {
          // Every failure means the same thing here: no bytes, draw the
          // fallback. Nothing is retried — a 409 or a 404 would answer the same
          // way every time, and a rerender must not turn that into a request
          // loop. `controller !== started` is how a stop is told from a real
          // failure: going far must not paint an error state.
          if (!live || controller !== started) return;
          settled = true;
          replaceUrl(null, null, true);
        });
    };

    const stop = () => {
      // Bytes that already arrived stay: they are on screen, and this row is
      // one scroll tick from being useful again. Only unfinished work is cut.
      if (settled) return;
      controller?.abort();
      controller = null;
    };

    reconcileRef.current = () => (activeRef.current ? start() : stop());
    if (activeRef.current) start();

    return () => {
      live = false;
      reconcileRef.current = null;
      controller?.abort();
      replaceUrl(null, null);
    };
    // The dependencies are the attachment's identity, its eligibility and the
    // fetcher, all stable across an ordinary rerender, so the list re-rendering
    // does not refetch anything. `priority` and `active` are deliberately
    // absent: one changes the queue order and the other starts/stops the work
    // in place — neither may tear this subscription down, because that is what
    // would revoke bytes already on screen.
  }, [attachmentId, eligible, fetchBlob, key, replaceUrl]);

  // Proximity changes start or stop the work of the subscription above without
  // replacing it. Declared after it so the subscription exists on first commit.
  useEffect(() => {
    activeRef.current = active;
    reconcileRef.current?.();
  }, [active]);

  const onLoadError = useCallback(() => {
    replaceUrl(null, null, true);
  }, [replaceUrl]);

  return { url: state.url, blob: state.blob, failed: state.failed, onLoadError };
}
