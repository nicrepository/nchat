/**
 * The read cursor controller for one open conversation (issue #1082).
 *
 *   mounted rows → rows the reader actually saw → latest position read
 *     → monotonic cursor → unread projection → published to the sidebar
 *
 * It takes facts from the viewport and never gives it orders: it reads the
 * scroll container and the mounted rows useViewportCore already keeps, and it
 * never scrolls, navigates or restores anything. Where the timeline is and how
 * far the reader has read are two different answers, and this hook only owns
 * the second one.
 *
 * "Saw" is decided by readVisibility's rule, and only while somebody can be
 * looking — the tab is visible and the window has focus — and only while the
 * reader owns the scrollport. Before the opening position is settled, and
 * while a navigation, a prepend restoration or a jump is carrying the
 * scrollport somewhere, the frames on screen are transitional: nothing is read
 * off them, and the reading starts where the trip ends.
 *
 * Two inputs move the cursor, and only one of them writes:
 *
 *  - what this reader was observed to read, published with its position so the
 *    sidebar persists it;
 *  - the server's read point (another tab or device read further), adopted
 *    as-is: it is already persisted, so it is never echoed back as a write,
 *    and the sidebar already shows the count that came with it.
 *
 * The count the conversation was opened with stays what it was at opening: it
 * placed the "Novas mensagens" boundary, and that is all it decides.
 */

import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";

import { isEligibleUnreadMessage } from "../../chatViewportState";
import type { Message } from "../../chatTypes";
import type { TimelinePosition } from "../../messages/messageOrder";
import { useLatestRef } from "../../messages/useLatestRef";
import {
  isReadThrough,
  laterReadPosition,
  openingReadCursor,
  pointInWindow,
  projectUnread,
  type ReadPosition,
  type ReadProgress,
} from "../../readCursor";
import type { ViewportCore } from "../viewport/useViewportCore";
import { latestExposedMessage } from "./readVisibility";

interface Params {
  core: ViewportCore;
  messages: Message[];
  currentUserId: string;
  /** The sidebar's unread_count for this target as of opening it. */
  unreadCountAtOpen: number;
  /** The server's current read point for this conversation, live. */
  serverReadThrough: TimelinePosition | null | undefined;
  /** Whether the opening position has been settled; nothing is read before. */
  resolved: boolean;
  /** `${kind}:${targetId}` of the conversation this timeline was mounted for. */
  conversationKey: string;
  onReadProgress: (conversationKey: string, progress: ReadProgress) => void;
}

/** Whether anybody can be looking at the page right now. */
function pageIsAttended(): boolean {
  return document.visibilityState === "visible" && document.hasFocus();
}

export function useReadCursor(params: Params): { unreadCount: number } {
  const { core, messages, currentUserId, resolved } = params;
  // Frozen when the timeline mounts — which is per conversation, since it
  // unmounts on every switch.
  const [opening] = useState(() => ({
    conversationKey: params.conversationKey,
    unreadCount: params.unreadCountAtOpen,
    cursor: openingReadCursor(messages, currentUserId, params.unreadCountAtOpen),
    tail: messages.at(-1) ?? null,
  }));
  const [observed, setObserved] = useState<ReadPosition | null>(null);
  const advance = useCallback(
    (position: ReadPosition) => setObserved((current) => laterReadPosition(current, position)),
    [],
  );

  const authority = pointInWindow(params.serverReadThrough, messages)
    ? params.serverReadThrough
    : null;
  // What was already known read before this reader did anything here.
  const known = later(opening.cursor, authority);
  const cursor = later(known, observed);
  const unreadCount = useMemo(
    () =>
      projectUnread({
        messages,
        currentUserId,
        cursor,
        unreadCountAtOpen: opening.unreadCount,
        openTail: opening.tail,
      }),
    [messages, currentUserId, cursor, opening],
  );

  useReadDetection(core, messages, currentUserId, resolved, advance);
  usePublishProgress(opening.conversationKey, observed, known, params.onReadProgress);

  return { unreadCount };
}

/** The later of two points, either of which may be missing. */
function later(
  current: TimelinePosition | null,
  incoming: TimelinePosition | null,
): TimelinePosition | null {
  return incoming ? laterReadPosition(current, incoming) : current;
}

/**
 * Reports what this reader did, and nothing else: a cursor they were observed
 * to reach past everything already known read. An authoritative point arriving
 * on its own changes the button's projection but publishes nothing — echoing
 * it would be a write of the server's own state.
 *
 * Layout-phase, like the look on commit: a message that arrives already on
 * screen is read inside the same synchronous pass that rendered it.
 */
function usePublishProgress(
  conversationKey: string,
  observed: ReadPosition | null,
  known: TimelinePosition | null,
  onReadProgress: (conversationKey: string, progress: ReadProgress) => void,
) {
  const latestOnReadProgress = useLatestRef(onReadProgress);
  const published = useRef<ReadPosition | null>(null);
  useLayoutEffect(() => {
    if (!observed || observed === published.current || isReadThrough(known, observed)) return;
    published.current = observed;
    latestOnReadProgress.current(conversationKey, { readThrough: observed });
  }, [conversationKey, observed, known, latestOnReadProgress]);
}

/**
 * Looks for newly seen rows whenever what is on screen may have changed: a
 * scroll, a commit (new rows, a remeasure, a settled opening or trip), the tab
 * becoming visible, the window regaining focus. Each look measures only the
 * mounted rows, and only a position later than the observed one re-renders.
 */
function useReadDetection(
  core: ViewportCore,
  messages: Message[],
  currentUserId: string,
  resolved: boolean,
  advance: (position: ReadPosition) => void,
) {
  // What a row accounts for when it is seen: its own message, when that can be
  // unread at all. Every message that can be unread has a row of its own —
  // conversation events included (see systemMessagePresentation) — so nothing
  // is ever read by being next to something else.
  const readByRow = useMemo(() => {
    const byId = new Map<string, ReadPosition>();
    for (const message of messages) {
      if (isEligibleUnreadMessage(message, currentUserId)) byId.set(message.id, message);
    }
    return byId;
  }, [messages, currentUserId]);

  const look = () => {
    const list = core.listRef.current;
    if (!list || !resolved || core.ownsScrollport() || !pageIsAttended()) return;
    const viewport = list.getBoundingClientRect();
    const seen = latestExposedMessage(core.messageRefs.current, viewport, (id) =>
      readByRow.get(id),
    );
    if (seen) advance(seen);
  };
  // The listeners below are registered once and always call this render's look.
  const latestLook = useLatestRef(look);

  // After every commit: new rows, a remeasure, the end of a trip. Registered
  // last in useConversationViewport, so every ownership the viewport takes in
  // this commit is already in place when it runs.
  useLayoutEffect(() => {
    latestLook.current();
  });

  useEffect(() => {
    const list = core.listRef.current;
    if (!list) return;
    const onChange = () => latestLook.current();
    list.addEventListener("scroll", onChange, { passive: true });
    document.addEventListener("visibilitychange", onChange);
    window.addEventListener("focus", onChange);
    return () => {
      list.removeEventListener("scroll", onChange);
      document.removeEventListener("visibilitychange", onChange);
      window.removeEventListener("focus", onChange);
    };
  }, [core, latestLook]);
}
