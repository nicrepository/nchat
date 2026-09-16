/**
 * The rows the timeline draws and the virtualizer that windows them (#675 —
 * moved out of ChatMessageArea, issue #834).
 *
 * Built before anything that positions the viewport, because the row model is
 * what every position is expressed against: an index the virtualizer can reach
 * survives a row being unmounted, where a DOM node does not.
 */

import { useLayoutEffect, useMemo } from "react";
import { useVirtualizer, type Virtualizer } from "@tanstack/react-virtual";

import type { Message } from "../../chatTypes";
import { formatDayLabel, formatTime } from "../../messageDisplay";
import {
  buildTimelineRows,
  ESTIMATED_ROW_HEIGHT_PX,
  INITIAL_VIEWPORT_HEIGHT_PX,
  shouldVirtualize,
  timelineRowIndex,
  TIMELINE_OVERSCAN_ROWS,
  type TimelineRow,
} from "../../timelineVirtualization";
import { computeTopmostVisible } from "./scrollCommands";
import type { ViewportCore } from "./useViewportCore";

export interface TimelineRowsState {
  rows: TimelineRow[];
  /** Whether the windowed path renders, rather than every row at once. */
  virtualized: boolean;
  virtualizer: Virtualizer<HTMLDivElement, Element>;
}

interface Params {
  core: ViewportCore;
  messages: Message[];
  firstUnreadMessageId: string | null;
  /** See usePrependRestore — the virtualizer's resize-compensation predicate. */
  adjustForRowResize: (
    item: { key: unknown; start: number; size: number },
    delta: number,
    instance: Virtualizer<HTMLDivElement, Element>,
  ) => boolean;
}

export function useTimelineRows({
  core,
  messages,
  firstUnreadMessageId,
  adjustForRowResize,
}: Params): TimelineRowsState {
  const rows = useMemo(
    () => buildTimelineRows(messages, firstUnreadMessageId, formatDayLabel, formatTime),
    [messages, firstUnreadMessageId],
  );
  const rowIndex = useMemo(() => timelineRowIndex(rows), [rows]);
  const virtualized = shouldVirtualize(rows.length);
  // Same scroll container as before, so every #492/#788 invariant built on it —
  // the scroll handler, the tail lock, the top/bottom sentinels, the prepend
  // delta — keeps working unchanged; only which rows exist in it changes.
  const virtualizer = useVirtualizer({
    count: rows.length,
    getScrollElement: () => core.listRef.current,
    estimateSize: () => ESTIMATED_ROW_HEIGHT_PX,
    overscan: TIMELINE_OVERSCAN_ROWS,
    // Keyed by row identity, never by index: a prepended page must not
    // invalidate every measurement above it. See timelineVirtualization.ts.
    getItemKey: (index) => rows[index].key,
    initialRect: { width: 0, height: INITIAL_VIEWPORT_HEIGHT_PX },
    enabled: virtualized,
  });
  // #492 through #675: what a row's real height replacing its estimate does to
  // the reading position. An instance property in this version rather than an
  // option, and idempotent, so the layout effect below is free to reassert it
  // on every commit. See the predicate for why the default is wrong here.
  virtualizer.shouldAdjustScrollPositionOnItemSizeChange = adjustForRowResize;

  useLayoutEffect(() => {
    core.setRowModel(rowIndex, virtualized ? virtualizer : null);
    // #675: finish an anchor the scroll handler could not resolve because the
    // mounted window was still a commit behind it. At most one scan of the
    // mounted rows per scroll burst, not one per commit — the flag is cleared
    // as soon as an answer exists.
    const el = core.listRef.current;
    if (!core.anchorStaleRef.current || !el || core.prependRestoreRef.current) return;
    core.recordAnchor(computeTopmostVisible(el, core.messageRefs.current));
  });

  return { rows, virtualized, virtualizer };
}
