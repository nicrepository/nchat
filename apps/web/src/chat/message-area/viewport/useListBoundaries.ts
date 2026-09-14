/**
 * The three things that happen at the edges of the timeline (moved out of
 * ChatMessageArea, issue #834): paginating off the top, keeping focus inside
 * the list when the virtualizer unmounts the row that held it, and handing the
 * reading position back when the conversation is left.
 */

import { useCallback, useEffect, useLayoutEffect, useRef } from "react";

import type { ViewportAnchor } from "../../chatViewportPersistence";
import type { ViewportCore } from "./useViewportCore";

/**
 * IntersectionObserver: fire loadMore when the top sentinel enters the viewport.
 *
 * Deps are [core, hasMore] only — NOT loadingMore or onLoadMore.
 *
 * Excluding loadingMore prevents the observer from being torn down and
 * recreated each time a fetch starts/finishes. In browsers, recreating the
 * observer while the sentinel is still visible causes an immediate re-fire,
 * leading to a loop of extra fetches. The guard against concurrent fetches
 * lives inside loadMore() via stateRef, so removing it from deps here is safe.
 *
 * onLoadMore is excluded because the ref below keeps it stable without
 * recreation.
 */
export function useInfiniteTop(core: ViewportCore, hasMore: boolean, onLoadMore: () => void) {
  const { topSentinelRef } = core;
  const onLoadMoreRef = useRef(onLoadMore);
  useLayoutEffect(() => {
    onLoadMoreRef.current = onLoadMore;
  });
  useEffect(() => {
    const sentinel = topSentinelRef.current;
    if (!sentinel || !hasMore) return;
    const observer = new IntersectionObserver(
      (entries) => {
        if (entries[0]?.isIntersecting) onLoadMoreRef.current();
      },
      { threshold: 0 },
    );
    observer.observe(sentinel);
    return () => observer.disconnect();
  }, [topSentinelRef, hasMore]);
}

/**
 * #675 focus recovery. Unmounting the row that holds focus sends focus to
 * <body>, where a keyboard user has lost the conversation entirely and gets no
 * announcement about it. Whenever focus was inside this list and has ended up
 * nowhere, it comes back to the list itself — a predictable place that still
 * reads as "Mensagens" and still scrolls with the arrow keys.
 */
export function useListFocusRecovery(core: ViewportCore): () => void {
  const { listRef } = core;
  const hadFocusRef = useRef(false);
  useEffect(() => {
    const el = listRef.current;
    if (!el) return;
    let pending: number | null = null;
    const onFocusIn = () => {
      hadFocusRef.current = true;
    };
    const onFocusOut = () => {
      // Deferred one task: focusout fires before the next element receives
      // focus, so reading document.activeElement now would always say <body>.
      if (pending !== null) window.clearTimeout(pending);
      pending = window.setTimeout(() => {
        pending = null;
        if (!hadFocusRef.current) return;
        if (document.activeElement !== document.body) {
          hadFocusRef.current = el.contains(document.activeElement);
          return;
        }
        hadFocusRef.current = false;
        el.focus();
      }, 0);
    };
    el.addEventListener("focusin", onFocusIn);
    el.addEventListener("focusout", onFocusOut);
    return () => {
      if (pending !== null) window.clearTimeout(pending);
      el.removeEventListener("focusin", onFocusIn);
      el.removeEventListener("focusout", onFocusOut);
    };
  }, [listRef]);

  /** Where focus lands when the thing that had it is gone (#675). */
  return useCallback(() => {
    listRef.current?.focus();
  }, [listRef]);
}

/**
 * Captures the anchor exactly once, when this conversation is actually left —
 * the unmount that already happens on every target switch (#492 item 4:
 * "capturar posição final antes de trocar de conversa").
 */
export function useAnchorCapture(
  core: ViewportCore,
  conversationKey: string,
  onCaptureAnchor: (key: string, anchor: ViewportAnchor) => void,
) {
  const { currentAnchorRef, phaseRef } = core;
  const conversationKeyRef = useRef(conversationKey);
  const onCaptureAnchorRef = useRef(onCaptureAnchor);
  useLayoutEffect(() => {
    conversationKeyRef.current = conversationKey;
    onCaptureAnchorRef.current = onCaptureAnchor;
  });
  useEffect(() => {
    return () => {
      const anchor = currentAnchorRef.current;
      const atBottom = phaseRef.current === "AT_BOTTOM";
      onCaptureAnchorRef.current(conversationKeyRef.current, {
        atBottom,
        anchorMessageId: atBottom ? null : (anchor?.messageId ?? null),
        anchorOffsetPx: anchor?.offsetPx ?? 0,
        savedAt: Date.now(),
      });
    };
  }, [currentAnchorRef, phaseRef]);
}
