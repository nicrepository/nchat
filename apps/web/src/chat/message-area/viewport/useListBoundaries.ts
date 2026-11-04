/**
 * The three things that happen at the edges of the timeline (moved out of
 * ChatMessageArea, issue #834): paginating off the top, keeping focus inside
 * the list when the virtualizer unmounts the row that held it, and handing the
 * reading position back when the conversation is left.
 */

import { useCallback, useEffect, useLayoutEffect, useRef, type RefObject } from "react";

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
 * Issue #1099: a message target followed from inside the list, and the focus
 * that is to arrive there.
 *
 * Following a message no page holds reopens the conversation, which unmounts
 * the list — and the quote that had focus — so focus falls to <body>. That
 * loss is involuntary, and is undone once the trip settles. Focus the reader
 * puts anywhere meanwhile — the composer, another message of the very same
 * list — is theirs: the first `focusin` on anything but `owner` cancels the
 * return, even if that element loses focus again before the trip settles.
 */
export interface FocusReturn {
  messageId: string;
  /** The `?message=` showing when it was asked for: not a replacement of it. */
  askedFrom: string;
  /** The request (history entry) showing when it was asked for; see followFocusReturn. */
  askedIn: string;
  /** The request that took the reader to `messageId`, once a commit shows it. */
  trip: string | null;
  /** The element focus is still ours on: the quote that asked, or what we moved it to since. */
  owner: Element | null;
  /** Stops watching the reader's focus; see dropFocusReturn. */
  release: () => void;
}

/** Cancels a pending return, and the one listener it holds. */
export function dropFocusReturn(ref: RefObject<FocusReturn | null>) {
  ref.current?.release();
  ref.current = null;
}

/**
 * The pending return the current route is still travelling on, if any: the
 * commit it was armed in, before the route moves, or the trip to its message.
 * Anything else abandoned it and drops it — another target, or the way back to
 * where it was asked from, which shows the very `?message=` and request the
 * arming commit did, but only after the trip has been seen (`trip`).
 */
export function followFocusReturn(
  ref: RefObject<FocusReturn | null>,
  focusMessageId: string,
  focusRequest: string,
): FocusReturn | null {
  const pending = ref.current;
  if (!pending) return null;
  const onTrip = focusMessageId === pending.messageId;
  if (pending.trip === null) {
    if (focusMessageId === pending.askedFrom && focusRequest === pending.askedIn) return null;
    if (onTrip) pending.trip = focusRequest;
  }
  if (onTrip && focusRequest === pending.trip) return pending;
  dropFocusReturn(ref);
  return null;
}

function armFocusReturn(
  ref: RefObject<FocusReturn | null>,
  pending: Omit<FocusReturn, "release" | "trip">,
) {
  dropFocusReturn(ref);
  const onFocusIn = (event: FocusEvent) => {
    if (event.target !== ref.current?.owner) dropFocusReturn(ref);
  };
  document.addEventListener("focusin", onFocusIn, true);
  ref.current = {
    ...pending,
    trip: null,
    release: () => document.removeEventListener("focusin", onFocusIn, true),
  };
}

/** Moves a pending return's focus somewhere of ours on the way, keeping it pending. */
export function holdFocusReturn(pending: FocusReturn, element: HTMLElement) {
  pending.owner = element;
  element.focus({ preventScroll: true });
}

/**
 * Lands the focus of a followed target (see FocusReturn) once the list that
 * replaced the old one settles: on the message it travelled to, or on the
 * list itself when the message could not be reached — never left nowhere.
 * `pendingRef` outlives the list (the timeline around it holds it), and is
 * dropped when another target replaces it.
 *
 * Returns the arm: called as the target is asked for, it only arms when focus
 * is inside this list right then — a pin or a search result never does.
 */
export function useTargetFocusReturn(
  core: ViewportCore,
  pendingRef: RefObject<FocusReturn | null>,
  focusMessageId = "",
  focusRequest = "",
  targetMissed: boolean,
): (messageId: string) => void {
  const { listRef, messageRefs } = core;
  useLayoutEffect(() => {
    const pending = followFocusReturn(pendingRef, focusMessageId, focusRequest);
    const list = listRef.current;
    if (!pending || !list) return;
    const destination = messageRefs.current.get(pending.messageId) ?? (targetMissed ? list : null);
    if (!destination) return;
    dropFocusReturn(pendingRef);
    destination.focus({ preventScroll: true });
  });
  return useCallback(
    (messageId: string) => {
      const active = document.activeElement;
      if (!listRef.current?.contains(active)) {
        dropFocusReturn(pendingRef);
        return;
      }
      armFocusReturn(pendingRef, {
        messageId,
        askedFrom: focusMessageId,
        askedIn: focusRequest,
        owner: active,
      });
    },
    [listRef, pendingRef, focusMessageId, focusRequest],
  );
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
