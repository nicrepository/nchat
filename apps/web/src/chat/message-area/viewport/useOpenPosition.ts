/**
 * Positions a conversation when it opens, once, before anything else moves the
 * viewport (#492 cases A/B/C — moved out of ChatMessageArea, issue #834).
 *
 * The decision itself is pure (see resolveOpenPosition); this hook is the React
 * half of it: it applies the answer to state, asks for another page when the
 * answer was "not yet", and performs the one instant DOM scroll.
 *
 * Applied during render — not in an effect — because it is a pure function of
 * already-available props, and conditionally updating state while rendering is
 * React's documented pattern for exactly this. This codebase's lint config
 * (react-hooks/set-state-in-effect) forbids the effect version outright. The
 * two effects below perform the only genuine side effects — fetching another
 * page, and the instant DOM scroll — and neither calls a state setter.
 */

import { useEffect, useLayoutEffect, useRef, useState } from "react";

import { UNREAD_DIVIDER_KEY } from "../../timelineVirtualization";
import { scrollToBottom } from "./scrollCommands";
import {
  decideOpenPositionResolution,
  type OpenPositionInput,
  type ScrollTarget,
} from "./openPosition";
import type { ViewportCore } from "./useViewportCore";

export type { ScrollTarget } from "./openPosition";

export interface OpenPositionState {
  /** The first unread message, or null when this conversation has none. */
  firstUnreadMessageId: string | null;
  /** Whether the opening position has been decided — mutations wait for it. */
  resolved: boolean;
  /** Where the one instant positioning scroll lands, once decided. */
  scrollTarget: ScrollTarget;
}

// searchAttempts is this hook's own state, not something the caller supplies.
interface Params extends Omit<OpenPositionInput, "searchAttempts"> {
  core: ViewportCore;
  onLoadMore: () => void;
}

/**
 * Applies the opening resolution, during render (#492 A/B/C).
 *
 * Every question — is it settled, is another page needed, was one already
 * asked for, where does it land, in which phase — is answered once, in
 * decideOpenPositionResolution, which is pure. What is left here is applying
 * that answer, which is why this reads as three straight lines per outcome
 * rather than as a nest of conditions.
 *
 * Render-time on purpose, and not moved into an effect: the resolution is a
 * pure function of already-available props, and conditionally updating state
 * while rendering is React's documented pattern for exactly this (this
 * codebase's react-hooks/set-state-in-effect forbids the effect version
 * outright). The two effects below perform the only genuine side effects —
 * fetching another page, and the instant DOM scroll — and neither calls a
 * state setter.
 *
 * Separated from useInstantPositioning because the two have to sit on opposite
 * sides of the row model: the decision is made during render, while the scroll
 * that carries it out has to be registered *after* useTimelineRows, so the row
 * index it looks the target up in is already the current one.
 */
export function useOpenPositionResolution({
  core,
  onLoadMore,
  ...input
}: Params): OpenPositionState {
  const [resolved, setResolved] = useState(false);
  const [firstUnreadMessageId, setFirstUnreadMessageId] = useState<string | null>(null);
  const [scrollTarget, setScrollTarget] = useState<ScrollTarget>(undefined);
  const [searchAttempts, setSearchAttempts] = useState(0);
  const [searchedForLength, setSearchedForLength] = useState(-1);

  // Stable ref so the page-fetch effect below never depends on the caller
  // handing down the same function identity.
  const onLoadMoreRef = useRef(onLoadMore);
  useLayoutEffect(() => {
    onLoadMoreRef.current = onLoadMore;
  });

  const decision = decideOpenPositionResolution({
    ...input,
    searchAttempts,
    searchedForLength,
    resolved,
  });
  if (decision.kind === "search") {
    setSearchedForLength(decision.searchedLength);
    setSearchAttempts((n) => n + 1);
  } else if (decision.kind === "settle") {
    // Each of these is applied unconditionally because each is idempotent:
    // React bails out of the re-render when the next value is Object.is-equal
    // to the current one, so the "only if it changed" guards the previous
    // version carried were doing React's own job a second time. setPhase is
    // useState's setter (see useViewportCore), so it bails out the same way.
    setResolved(true);
    setFirstUnreadMessageId(decision.firstUnreadMessageId);
    setScrollTarget(decision.scrollTarget);
    core.setPhase(decision.phase);
  }

  // Fetches the next page for the bounded search above — a plain
  // external-system call, no setState of its own. Fires exactly once per
  // searchAttempts bump.
  useEffect(() => {
    if (searchAttempts > 0) onLoadMoreRef.current();
  }, [searchAttempts]);

  return { firstUnreadMessageId, resolved, scrollTarget };
}

/**
 * Performs the actual instant positioning once resolution picked a target — a
 * plain DOM operation, no setState of its own.
 */
export function useInstantPositioning(
  core: ViewportCore,
  scrollTarget: ScrollTarget,
  firstUnreadMessageId: string | null,
) {
  useLayoutEffect(() => {
    if (!scrollTarget) return;
    if (scrollTarget.messageId === null) {
      scrollToBottom(core.bottomRef, "auto");
      return;
    }
    // Land on the separator, not the message: the message sits right below it,
    // so scrolling to the message alone would push the separator (and its
    // "Novas mensagens" label) off-screen above the viewport.
    const targetKey =
      scrollTarget.messageId === firstUnreadMessageId ? UNREAD_DIVIDER_KEY : scrollTarget.messageId;
    // Read through the refs, not the render values: the dependency list has to
    // stay [scrollTarget, firstUnreadMessageId]. Positioning happens once, when
    // resolution picks a target — re-running it because the row model changed
    // would re-scroll on every prepended page, which is precisely the "not
    // moving the message being read" invariant #492 exists to protect.
    const activeVirtualizer = core.virtualizerRef.current;
    if (activeVirtualizer) {
      // #675: the row this resolves to is very often outside the initial
      // window, so the virtualizer places it rather than a DOM node that does
      // not exist yet.
      const index = core.rowIndexRef.current.get(targetKey);
      if (index !== undefined) activeVirtualizer.scrollToIndex(index, { align: "start" });
      return;
    }
    const element =
      targetKey === UNREAD_DIVIDER_KEY
        ? core.unreadDividerRef.current
        : core.messageRefs.current.get(targetKey);
    element?.scrollIntoView({ behavior: "auto", block: "start" });
  }, [core, scrollTarget, firstUnreadMessageId]);
}
