/**
 * The conversation viewport (issue #834): one hook that owns where the timeline
 * is scrolled to and why, so the timeline itself can be about drawing messages.
 *
 * It composes the primitives in this directory over one shared scroll authority
 * (see useViewportCore) — opening position, tail following, prepend
 * restoration, pagination, jumps, focus recovery, anchor capture — and hands
 * back only what the timeline has to render: the refs to attach, and the values
 * it draws.
 *
 * The timeline never learns the phase, the observers, or the restoration: every
 * invariant those encode stays on this side of the boundary.
 */

import type { RefObject } from "react";
import type { Virtualizer } from "@tanstack/react-virtual";

import type { Message } from "../../chatTypes";
import type { ViewportAnchor } from "../../chatViewportPersistence";
import type { LastMutation } from "../../useMessages";
import type { TimelineRow } from "../../timelineVirtualization";
import { useCallback } from "react";

import { scrollButtonState, type ScrollButtonState } from "./navigation";
import { useViewportCore } from "./useViewportCore";
import { useInstantPositioning, useOpenPositionResolution } from "./useOpenPosition";
import { usePrependRestoreEffects, useRowResizeAdjustment } from "./usePrependRestore";
import { useTimelineRows } from "./useTimelineRows";
import { useNavigator, useUnreadBoundary } from "./useNavigator";
import { useTailArrival, useTailFollow } from "./useTailFollow";
import { useUnreadCount } from "./useUnreadCount";
import { useMessageJump } from "./useMessageJump";
import { useAnchorCapture, useInfiniteTop, useListFocusRecovery } from "./useListBoundaries";

export interface ConversationViewportInput {
  messages: Message[];
  currentUserId: string;
  hasMore: boolean;
  lastMutation: LastMutation;
  /** `${kind}:${targetId}` — keys the per-conversation viewport anchor. */
  conversationKey: string;
  /** The sidebar's unread_count for this target as of opening it. */
  unreadCountAtOpen: number;
  /** The anchor this conversation was left at, if any. */
  initialAnchor: ViewportAnchor | null;
  /** A `?message=` deep link. */
  focusMessageId?: string;
  onLoadMore: () => void;
  onCaptureAnchor: (key: string, anchor: ViewportAnchor) => void;
  onReachedBottom: () => void;
}

export interface ConversationViewport {
  /** Callback ref for the scroll container — also publishes it as state. */
  attachList: (element: HTMLDivElement | null) => void;
  /** #788: the ResizeObserver target — the wrapper a growing row moves. */
  contentRef: RefObject<HTMLDivElement | null>;
  /** The pagination sentinel. */
  topSentinelRef: RefObject<HTMLDivElement | null>;
  /** The tail sentinel: the scroll target and the arrival confirmation. */
  bottomRef: RefObject<HTMLDivElement | null>;
  /** #492: what AT_FIRST_UNREAD positioning actually lands on. */
  unreadDividerRef: RefObject<HTMLDivElement | null>;
  /**
   * #675: the scroll container as state, because the attachment observers need
   * it as their IntersectionObserver root and a ref alone never tells a
   * consumer it has arrived.
   */
  scrollRoot: HTMLDivElement | null;
  setMessageRef: (messageId: string, el: HTMLDivElement | null) => void;
  rows: TimelineRow[];
  virtualized: boolean;
  virtualizer: Virtualizer<HTMLDivElement, Element>;
  firstUnreadMessageId: string | null;
  highlightedMessageId: string | null;
  jumpToMessage: (messageId: string) => void;
  /** #880: the one floating control — whether it shows, and what it offers. */
  scrollButton: ScrollButtonState;
  /** What pressing that control does, which depends on what it offers. */
  onScrollButtonClick: () => void;
  /** Where focus lands when the row that held it has been unmounted. */
  focusList: () => void;
}

export function useConversationViewport(input: ConversationViewportInput): ConversationViewport {
  const { core, phase, scrollRoot } = useViewportCore();

  // Order below is load-bearing, and it is the order of the layout effects each
  // of these registers — React runs them in registration order:
  //
  //   1. the row model, so every position expressed as a row index is current
  //      before anything looks one up;
  //   2. the navigator, whose per-commit pass re-derives its destination from
  //      that model (#880);
  //   3. the opening position's one instant scroll, which looks one up — and
  //      which hands the unread boundary to the navigator above;
  //   4. the prepend restoration, whose first effect arms what its second
  //      consumes in the very same commit.
  //
  // Reordering these silently breaks #492/#675/#880: a scroll resolved against
  // a stale row index lands on the wrong message, a restoration armed after its
  // own pass effect never runs at all, and a navigation pass that runs before
  // the row model is a destination computed from the previous page.
  const resolution = useOpenPositionResolution({
    core,
    messages: input.messages,
    currentUserId: input.currentUserId,
    hasMore: input.hasMore,
    unreadCountAtOpen: input.unreadCountAtOpen,
    initialAnchor: input.initialAnchor,
    focusMessageId: input.focusMessageId,
    onLoadMore: input.onLoadMore,
  });
  const { firstUnreadMessageId, resolved } = resolution;

  const adjustForRowResize = useRowResizeAdjustment(core);

  const { rows, virtualized, virtualizer } = useTimelineRows({
    core,
    messages: input.messages,
    firstUnreadMessageId,
    adjustForRowResize,
  });

  const unread = useUnreadCount(input.unreadCountAtOpen);
  const arrival = useTailArrival(core, input.onReachedBottom, unread.clear);
  const navigator = useNavigator({
    core,
    conversationKey: input.conversationKey,
    onTailArrived: arrival.arrive,
  });

  useInstantPositioning(
    core,
    resolution.scrollTarget,
    resolution.target,
    navigator.navigateToTail,
    navigator.navigateToFirstUnread,
  );

  usePrependRestoreEffects({
    core,
    messages: input.messages,
    lastMutation: input.lastMutation,
    resolved,
  });

  useTailFollow({
    core,
    phase,
    messages: input.messages,
    currentUserId: input.currentUserId,
    lastMutation: input.lastMutation,
    resolved,
    arrival,
    navigator,
    onUnreadArrival: unread.countArrival,
  });

  const boundaryAhead = useUnreadBoundary(core, firstUnreadMessageId);
  const scrollButton = scrollButtonState({
    awayFromTail: phase !== "AT_BOTTOM" && phase !== "RESTORING_POSITION",
    unreadCount: unread.count,
    hasBoundary: firstUnreadMessageId !== null,
    boundaryAhead,
  });
  // #880 item 10: one control, two destinations. Which one it means is the
  // button state's answer, so pressing it can only ever agree with what it
  // says — there is no second source deciding where it goes.
  const { navigateToFirstUnread, navigateToTail } = navigator;
  const onScrollButtonClick = useCallback(() => {
    if (scrollButton.mode === "first-unread") navigateToFirstUnread("button");
    else navigateToTail("button");
  }, [navigateToFirstUnread, navigateToTail, scrollButton.mode]);

  const { highlightedMessageId, jumpToMessage } = useMessageJump(
    core,
    input.messages,
    input.focusMessageId,
  );

  useInfiniteTop(core, input.hasMore, input.onLoadMore);
  useAnchorCapture(core, input.conversationKey, input.onCaptureAnchor);
  const focusList = useListFocusRecovery(core);

  return {
    attachList: core.attachList,
    contentRef: core.contentRef,
    topSentinelRef: core.topSentinelRef,
    bottomRef: core.bottomRef,
    unreadDividerRef: core.unreadDividerRef,
    scrollRoot,
    setMessageRef: core.setMessageRef,
    rows,
    virtualized,
    virtualizer,
    firstUnreadMessageId,
    highlightedMessageId,
    jumpToMessage,
    scrollButton,
    onScrollButtonClick,
    focusList,
  };
}
