/**
 * Putting the reader back on the same message, at the same offset, once a
 * prepended page has real heights (#675 — moved out of ChatMessageArea, issue
 * #834).
 *
 * While a restoration is armed it is the ONLY writer of scrollTop: the
 * tail-lock stands down, the virtualizer's own resize compensation stands down,
 * and the scroll handler stops re-deriving the anchor. See ViewportCore for the
 * full authority table.
 */

import { useCallback, useLayoutEffect } from "react";
import type { Virtualizer } from "@tanstack/react-virtual";

import type { LastMutation } from "../../useMessages";
import type { Message } from "../../chatTypes";
import type { ViewportPhase } from "../../chatViewportState";
import {
  MAX_PREPEND_RESTORE_PASSES,
  prependRestoreStep,
  shouldShiftReadingPositionForResize,
} from "../../timelineVirtualization";
import { computeTopmostVisible, prependScrollTopFor, scrollToBottom } from "./scrollCommands";
import type { PrependRestore, ViewportCore } from "./useViewportCore";

interface Params {
  core: ViewportCore;
  phase: ViewportPhase;
  messages: Message[];
  lastMutation: LastMutation;
  resolved: boolean;
}

/**
 * What a row's size change does to the reading position, handed to the
 * virtualizer.
 *
 * Stable across renders — it reads everything it needs from the virtualizer and
 * from the core — so the virtualizer is handed the same function on every
 * commit rather than a new one to compare against. Produced separately from the
 * effects below because the virtualizer needs it before it is built, while the
 * effects have to be registered after the row model it works against.
 */
export function useRowResizeAdjustment(core: ViewportCore) {
  const { prependRestoreRef, phaseRef } = core;
  return useCallback(
    (
      item: { key: unknown; start: number; size: number },
      _delta: number,
      instance: Virtualizer<HTMLDivElement, Element>,
    ): boolean =>
      shouldShiftReadingPositionForResize({
        rowStartPx: item.start,
        rowSizePx: item.size,
        // The element's own scrollTop, not the virtualizer's cached offset:
        // that one is refreshed from a scroll event, which is asynchronous, so
        // straight after any programmatic scroll it still reports the position
        // from before the move. The reading position is a fact about the
        // element.
        readingOffsetPx:
          instance.scrollElement?.scrollTop ??
          (instance.scrollOffset ?? 0) + instance.scrollAdjustments,
        // The virtualizer's own measurement cache, read before it records this
        // change: a key it does not hold yet is an estimate being replaced.
        // No per-row state of our own, and no second cache to keep in sync.
        isFirstMeasurement: !instance.itemSizeCache.has(item.key as never),
        restoring: prependRestoreRef.current !== null,
        scrollingToEnd: phaseRef.current === "SCROLLING_TO_BOTTOM",
      }),
    [prependRestoreRef, phaseRef],
  );
}

/**
 * The two layout effects that move the scrollport after a prepend. Registered
 * in this order, and after the row model, because the mutation effect arms the
 * restoration that the pass effect consumes in the very same commit.
 */
export function usePrependRestoreEffects({
  core,
  phase,
  messages,
  lastMutation,
  resolved,
}: Params) {
  useMutationScroll(core, phase, messages, lastMutation, resolved);
  useRestorePasses(core);
}

/**
 * Scroll management for mutations that happen AFTER initial resolution.
 *
 * "prepend"    → hand the position to the restoration below (virtualized), or
 *                shift scrollTop by the scrollHeight delta (plain path).
 * "ws_append"  → follow the tail only while already AT_BOTTOM; otherwise the
 *                viewport is never pulled. The pending-count badge and an own
 *                send's animated return are handled during render by
 *                useTailFollow, for the set-state-in-effect reason documented
 *                there.
 * "none"       → no action (intermediate transition).
 *
 * prevScrollHeightRef is captured ONLY on stable mutations ("initial",
 * "append", "ws_append", "prepend") — never on "none". This prevents the
 * spinner's height from polluting the reference value used to compute the
 * scroll delta on the subsequent "prepend": capturing on "none" would leave the
 * delta wrong by the spinner height (~36px), causing a visible jump after every
 * successful loadMore.
 */
function useMutationScroll(
  core: ViewportCore,
  phase: ViewportPhase,
  messages: Message[],
  lastMutation: LastMutation,
  resolved: boolean,
) {
  useLayoutEffect(() => {
    const el = core.listRef.current;
    if (!el) return;
    if (lastMutation === "prepend") {
      restorePrependedPosition(core, el);
    } else if (resolved && lastMutation === "ws_append" && phase === "AT_BOTTOM") {
      scrollToBottom(core.bottomRef, "auto");
    }
    // Only snapshot scrollHeight in a stable state — not during "none"
    // transitions where the loading spinner may inflate the measurement.
    if (lastMutation !== "none") core.snapshotScrollHeight(el.scrollHeight);
  }, [core, messages, lastMutation, resolved, phase]);
}

/**
 * Arms the restoration for a prepended page, or falls back to the plain
 * scrollHeight delta when there is no virtualizer or no anchor to restore to.
 *
 * #675: neither one-shot compensation works on the virtualized path. The
 * scrollHeight delta is exact only while every row is mounted; and
 * scrollToIndex positions against the *estimated* heights the prepended page
 * enters with, which the virtualizer then rewrites as it measures each newly
 * mounted row — so whatever either of them computes is already stale by the
 * time the layout settles. That is where the 722px of measured drift came from.
 *
 * What survives measurement is the row's identity, so that is what is named
 * here; the restoration turns it back into pixels once the pixels are real.
 *
 * Deliberately no scrollToIndex, here or in the restoration: it arms a
 * reconcile of the virtualizer's own that keeps re-targeting that index on
 * every animation frame for seconds afterwards. That is a second scroll
 * authority outliving the restoration — it lands the row flush against the top
 * edge, discarding the offset the reader actually had, which is what made a
 * restored position slip by exactly one row. The restoration writes scrollTop
 * and nothing else.
 *
 * Nor is the viewport moved here: the restoration's first pass, in this same
 * commit, is what does it. That keeps every move on one code path, where "a
 * pass that moves nothing is a pass with no successor" can hold.
 */
function restorePrependedPosition(core: ViewportCore, el: HTMLDivElement) {
  const anchor = core.currentAnchorRef.current;
  const anchorIndex = anchor ? core.rowIndexRef.current.get(anchor.messageId) : undefined;
  if (core.virtualizerRef.current && anchor && anchorIndex !== undefined) {
    core.armPrependRestore(anchor);
    return;
  }
  // Shift scrollTop by the amount the container grew so the user's view is stable.
  el.scrollTop += el.scrollHeight - core.prevScrollHeightRef.current;
}

/**
 * PREPEND_RESTORE (#675): the passes that actually move the scrollport.
 *
 * No dependency list, so it runs again on every commit while it is armed.
 * Those commits are not a poll: the virtualizer re-renders when it measures a
 * row and when the scroll offset moves, so each pass is driven by an actual
 * layout event. Nothing here waits on a timer, a frame, or a fixed number of
 * retries.
 *
 * Every pass re-reads where the anchor row really is and corrects to where it
 * must be, absolutely rather than incrementally — so a pass never depends on
 * what the previous one managed to apply.
 *
 * It disarms as soon as the anchor is within a pixel of its target: from there
 * the virtualizer's adjustment is the right authority again, because every
 * remaining row is measured on the way into the window like any other scroll.
 * It also ends the moment a pass cannot move the viewport at all — see
 * moveOrFinish. What each pass decides is prependRestoreStep, which is pure so
 * that every one of those endings can be exercised directly.
 *
 * Cost per pass is two getBoundingClientRect calls, on the scrollport and on
 * one row — never a scan of the timeline.
 */
function useRestorePasses(core: ViewportCore) {
  const { listRef, messageRefs, prependRestoreRef } = core;
  const { endPrependRestore, recordAnchor } = core;
  /**
   * Ends the restoration, and recomputes the reading position it owned.
   *
   * This is what keeps the restoration from ever becoming a stuck owner of the
   * scrollport. Its passes are driven by commits, and the only thing guaranteed
   * to produce the next commit is the viewport actually moving — so a pass that
   * changes nothing is a pass with no successor. Staying armed in that state
   * means silently undoing every later scroll, the reader's own trip back to
   * the top sentinel included, which is exactly what it did before this rule.
   *
   * Ending early costs a few pixels of accuracy at most: from there the
   * virtualizer's reading-position adjustment is the correct authority anyway.
   * Handing the scroll position back also hands back that compensation, which
   * stood down for the duration — and the rows measured while it was off may
   * have left the reading position stale, so it is recomputed here, once.
   */
  const finishRestore = useCallback(
    (restore: PrependRestore) => {
      if (!endPrependRestore(restore)) return;
      const el = listRef.current;
      if (!el) return;
      recordAnchor(computeTopmostVisible(el, messageRefs.current));
    },
    [endPrependRestore, recordAnchor, listRef, messageRefs],
  );

  const moveOrFinish = useCallback(
    (el: HTMLDivElement, top: number) => {
      const restore = prependRestoreRef.current;
      if (!restore) return;
      const before = el.scrollTop;
      el.scrollTop = top;
      if (el.scrollTop === before) finishRestore(restore);
    },
    [prependRestoreRef, finishRestore],
  );

  useLayoutEffect(() => {
    const restore = core.prependRestoreRef.current;
    if (!restore) return;
    const el = core.listRef.current;
    const virtualizer = core.virtualizerRef.current;
    if (!el || !virtualizer) {
      core.endPrependRestore(restore);
      return;
    }
    const passes = core.countRestorePass();
    if (isRestoreOutranked(core, passes)) {
      finishRestore(restore);
      return;
    }
    const { step, node } = nextRestoreStep(core, restore, el, virtualizer);
    if (step.kind === "finish") {
      if (node) {
        // On target. The reader is where they were, so that is what the next
        // prepend — and the anchor captured on leaving the conversation — must
        // start from; the scroll handler was not allowed to record anything
        // while this ran.
        core.setAnchor({ messageId: restore.messageId, offsetPx: restore.offsetPx });
      }
      finishRestore(restore);
      return;
    }
    // Either way the viewport is asked to move, which is what makes the next
    // pass certain — and a move the scrollport cannot take is the end of the
    // road, which moveOrFinish is what notices.
    moveOrFinish(el, step.kind === "seek" ? step.scrollTopPx : el.scrollTop + step.deltaPx);
  });
}

/**
 * Whether something outranks putting this reading position back.
 *
 * The pass ceiling is never the expected exit: every branch either finishes or
 * moves the viewport, and a move is what produces the next pass. An explicit
 * "take me to the end" does outrank it — the reader has just said they do not
 * want the old position any more, and two writers pulling in opposite
 * directions would leave them at neither end.
 */
function isRestoreOutranked(core: ViewportCore, passes: number): boolean {
  return passes > MAX_PREPEND_RESTORE_PASSES || core.phaseRef.current === "SCROLLING_TO_BOTTOM";
}

/** Where this pass has to put the scrollport, measured against the real rows. */
function nextRestoreStep(
  core: ViewportCore,
  restore: PrependRestore,
  el: HTMLDivElement,
  virtualizer: Virtualizer<HTMLDivElement, Element>,
) {
  const index = core.rowIndexRef.current.get(restore.messageId);
  const node =
    index === undefined ? null : (core.messageRefs.current.get(restore.messageId) ?? null);
  if (node) core.markRestoreMeasured();
  const step = prependRestoreStep({
    anchorIndex: index,
    anchorOffsetPx: restore.offsetPx,
    measuredOffsetPx: node
      ? node.getBoundingClientRect().top - el.getBoundingClientRect().top
      : null,
    modelScrollTopPx:
      index === undefined
        ? el.scrollTop
        : prependScrollTopFor(virtualizer, index, restore.offsetPx),
    currentScrollTopPx: el.scrollTop,
    hasBeenMeasured: restore.measured,
  });
  // The node travels back with the step: "on target" is only recorded as the
  // reader's anchor when this pass actually measured the row, never on the
  // strength of an earlier pass having done so.
  return { step, node };
}
