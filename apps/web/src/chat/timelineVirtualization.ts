/**
 * The timeline's row model and the numbers that bound it (issue #675).
 *
 * A conversation is not a list of messages: it is a list of *rows*, some of
 * which are day dividers and one of which may be the "Novas mensagens"
 * separator. The virtualizer indexes rows, the anchor logic names rows, and the
 * jump-to-message logic looks rows up — so building them, and giving each a
 * stable identity, is one function rather than something the view improvises
 * while it renders.
 *
 * # Why the keys matter more than they look
 *
 * The virtualizer caches a measured height per *key*. Loading a page of history
 * prepends rows and shifts every index by the number added; if the key were the
 * index, every cached measurement would silently describe a different row and
 * the reading position would drift on every prepend. Keyed by message id (and,
 * for a divider, by its day label) the cache survives it untouched.
 */

import type { Message } from "./chatTypes";

/**
 * Below this many rows the timeline renders in full.
 *
 * A conversation opens with one page (50 messages, chat-service's default), and
 * virtualizing that costs measurement work to save nothing. The window that
 * matters is the one a reader builds by pulling history in — the second page is
 * where mounting everything starts to be the wrong trade.
 */
export const VIRTUALIZE_MIN_ROWS = 60;

/**
 * Rows kept mounted on each side of the viewport.
 *
 * Enough that ordinary wheel scrolling never exposes an unmounted row, small
 * enough that the saving is real: at the estimate below this is roughly half a
 * screen in each direction.
 */
export const TIMELINE_OVERSCAN_ROWS = 6;

/**
 * The height a row is assumed to have until it has been measured.
 *
 * Only ever an initial guess — every mounted row reports its real height back,
 * which is what makes variable-height content (attachments, reactions, replies,
 * edits, players) work at all. A wrong estimate costs scrollbar accuracy for
 * rows nobody has visited yet, never layout.
 */
export const ESTIMATED_ROW_HEIGHT_PX = 92;

/**
 * The viewport height assumed before the scroll container has been measured.
 *
 * The virtualizer's own default is zero, which renders nothing until a resize
 * observation lands. A first paint of nothing is a flash of empty conversation,
 * so it starts from about a screen and is corrected on the first measurement.
 */
export const INITIAL_VIEWPORT_HEIGHT_PX = 900;

/**
 * How close the anchor row must be to where the reader left it for a prepend
 * restoration to count as finished (issue #675).
 *
 * A pixel, not a viewport. The restoration measures the anchor's real box and
 * corrects to it, so anything larger than sub-pixel rounding is drift that has
 * a cause and can be corrected — accepting more would only hide a regression.
 */
export const PREPEND_ANCHOR_TOLERANCE_PX = 1;

/**
 * The most commits one prepend restoration may span.
 *
 * A ceiling, not the expected exit. Every pass of the restoration either
 * finishes or moves the scrollport, and moving it is what produces the next
 * pass — so termination comes from those conditions, not from counting. This
 * exists only so a pathological layout that keeps correcting in circles cannot
 * hold the scroll position forever, and a real prepend never reaches it.
 */
export const MAX_PREPEND_RESTORE_PASSES = 40;

/**
 * The furthest an explicit "take me to the end" will animate (issue #675).
 *
 * A smooth scroll is an animation the browser owns for its whole duration, and
 * any write to scrollTop cancels it — leaving nobody to finish the trip. Across
 * a few screens that duration is short enough for nothing to interrupt it.
 * Across a whole conversation of loaded history it is not: rows are measured
 * for the first time on the way past, the tail-lock corrects for the growth
 * they cause, and the animation dies somewhere in the middle with the reader
 * stranded tens of thousands of pixels short.
 *
 * Beyond this distance the honest answer is to arrive at once. Nothing is lost:
 * an animation that long is a teleport with extra steps, and nobody follows the
 * content flying past.
 */
export const MAX_SMOOTH_SCROLL_DISTANCE_PX = 2000;

/**
 * Whether an explicit "take me to the end" should animate (issue #675).
 *
 * Reduced motion always wins — that contract predates this and is untouched.
 * Otherwise it is the distance that decides, and MAX_SMOOTH_SCROLL_DISTANCE_PX
 * says why: exactly at the limit still animates, past it the reader arrives at
 * once.
 */
export function scrollToEndBehavior(
  remainingPx: number,
  prefersReducedMotion: boolean,
): ScrollBehavior {
  if (prefersReducedMotion) return "auto";
  return remainingPx > MAX_SMOOTH_SCROLL_DISTANCE_PX ? "auto" : "smooth";
}

/** What one pass of a prepend restoration decides to do (issue #675). */
export type PrependRestoreStep =
  /** Nothing left to do: hand the scroll position back. */
  | { kind: "finish" }
  /** Put the scrollport where the row model says the anchor is, to mount it. */
  | { kind: "seek"; scrollTopPx: number }
  /** The anchor is measured: correct by the difference. */
  | { kind: "correct"; deltaPx: number };

export interface PrependRestoreState {
  /** Where the anchor row is in the row model, or undefined if it is gone. */
  anchorIndex: number | undefined;
  /** The offset the reader had it at. */
  anchorOffsetPx: number;
  /** Its real offset now, or null while the row is not mounted. */
  measuredOffsetPx: number | null;
  /** Where the row model says the scrollport must be to show it at that offset. */
  modelScrollTopPx: number;
  /** Where the scrollport is. */
  currentScrollTopPx: number;
  /** Whether the anchor has already been measured at least once. */
  hasBeenMeasured: boolean;
}

/**
 * One pass of PREPEND_RESTORE, as a decision.
 *
 * Pure on purpose: every way this can end is a branch here, testable with real
 * geometries, while the effect around it keeps the DOM reads and the single
 * write. The termination argument is the whole point — each pass ends in
 * exactly one of three states:
 *
 *   finish   nothing more is possible or nothing more is needed;
 *   seek     the viewport moves, so the scroll event and the re-render it
 *            causes are a guaranteed successor to this pass;
 *   correct  the same, by a measured amount.
 *
 * `seek` is only ever issued on the way in. Once the anchor has been measured,
 * the model's answer is the worse of the two and re-issuing it would undo the
 * measured correction — which is what made an earlier version oscillate between
 * them, one commit apart, until its budget ran out.
 *
 * A `seek` or `correct` that the scrollport cannot take is the caller's cue to
 * finish: it produced no movement, so it has no successor, and staying armed
 * would mean waiting on a commit that is not coming.
 */
export function prependRestoreStep(state: PrependRestoreState): PrependRestoreStep {
  // The anchor left the loaded window entirely — a deletion, or a conversation
  // reload. There is no position to restore.
  if (state.anchorIndex === undefined) return { kind: "finish" };
  if (state.measuredOffsetPx === null) {
    if (state.hasBeenMeasured) return { kind: "finish" };
    // The scrollport is already where the model says the row is and it still is
    // not mounted: nothing left for this pass to try.
    if (state.modelScrollTopPx === state.currentScrollTopPx) return { kind: "finish" };
    return { kind: "seek", scrollTopPx: state.modelScrollTopPx };
  }
  const driftPx = state.measuredOffsetPx - state.anchorOffsetPx;
  if (Math.abs(driftPx) <= PREPEND_ANCHOR_TOLERANCE_PX) return { kind: "finish" };
  return { kind: "correct", deltaPx: driftPx };
}

/** The key of the "Novas mensagens" separator — there is at most one. */
export const UNREAD_DIVIDER_KEY = "unread-divider";

export type TimelineRow =
  | { type: "divider"; key: string; label: string }
  | { type: "unread-divider"; key: string }
  | { type: "msg"; key: string; message: Message; isGrouped: boolean };

/** What the virtualizer knows about one row whose size just changed. */
export interface RowResize {
  /** The row's offset in the content, before this change is applied. */
  rowStartPx: number;
  /** The row's size as the model still holds it — the pre-change value. */
  rowSizePx: number;
  /** Where the reader is, read live from the scrollport. */
  readingOffsetPx: number;
  /**
   * Whether the model had never held a size for this row: an estimate is being
   * replaced by a real measurement, rather than a real measurement changing.
   */
  isFirstMeasurement: boolean;
  /** Whether a prepend restoration currently owns the scroll position. */
  restoring: boolean;
  /**
   * Whether an explicit "take me to the end" is in flight and owns it instead.
   */
  scrollingToEnd: boolean;
}

/**
 * Whether a row changing size must shift the scroll offset with it (#675).
 *
 * Three answers, not one, because the three cases move different things:
 *
 * # Something else owns the scroll position: never
 *
 * Two states claim scrollTop outright, and while either holds it the
 * virtualizer's adjustment is a second writer on the same value.
 *
 * PREPEND_RESTORE measures the anchor's real box and corrects to it, so an
 * adjustment landing in the same cycle is either redundant or a fight.
 *
 * SCROLLING_TO_BOTTOM is an animation the browser is running: any programmatic
 * write to scrollTop cancels it, and a cancelled animation has nobody left to
 * finish it — the reader is simply stranded wherever the write landed, which is
 * how "Ir para o final" could stop thirty thousand pixels short. The tail-lock
 * is what follows the growing content in that state, and it does not need this.
 *
 * Both hand the authority back the moment they end.
 *
 * # A first measurement: compensate whenever the row starts above the reader
 *
 * The row is replacing an estimate for a block the reader has already scrolled
 * past, so the whole estimate→actual difference belongs above them. This is the
 * virtualizer's own rule, minus its refusal to do it while scrolling backward —
 * and reaching the top sentinel is scrolling backward by definition, which is
 * why a prepended page's measurements used to push their error straight into
 * the reading position.
 *
 * # A re-measurement: only when the row is entirely above the reader
 *
 * This is the case the "starts above" rule gets wrong. A row that *crosses* the
 * top edge grows downward, below the reader's eyes: an attachment finishing its
 * layout, an edit, an expansion. Shifting the viewport by that whole delta
 * would yank the reader down by 200px because content they cannot see got
 * taller. Only a row that ends at or above them changed something already
 * scrolled past.
 */
export function shouldShiftReadingPositionForResize(resize: RowResize): boolean {
  if (resize.restoring || resize.scrollingToEnd) return false;
  if (resize.isFirstMeasurement) return resize.rowStartPx < resize.readingOffsetPx;
  return resize.rowStartPx + resize.rowSizePx <= resize.readingOffsetPx;
}

/** Whether a timeline of this many rows is worth virtualizing. */
export function shouldVirtualize(rowCount: number): boolean {
  return rowCount >= VIRTUALIZE_MIN_ROWS;
}

/**
 * Turns the loaded messages into the rows the timeline draws.
 *
 * Day dividers whenever the day changes, the unread separator immediately
 * before the first unread message, and a grouping flag for a message that
 * follows one from the same sender in the same minute — exactly what the list
 * rendered inline before, moved here so the virtualizer and the view agree on
 * what row `n` is.
 *
 * @param formatDay   day label for a timestamp (locale formatting stays in the view layer)
 * @param formatMinute minute label, used only to decide visual grouping
 */
export function buildTimelineRows(
  messages: readonly Message[],
  firstUnreadMessageId: string | null,
  formatDay: (iso: string) => string,
  formatMinute: (iso: string) => string,
): TimelineRow[] {
  const rows: TimelineRow[] = [];
  let lastDay = "";
  let lastSenderId = "";
  let lastMinute = "";
  for (const message of messages) {
    const day = formatDay(message.createdAt);
    if (day !== lastDay) {
      rows.push({ type: "divider", key: `day:${day}`, label: day });
      lastDay = day;
      lastSenderId = "";
      lastMinute = "";
    }
    if (message.id === firstUnreadMessageId) {
      rows.push({ type: "unread-divider", key: UNREAD_DIVIDER_KEY });
    }
    const minute = formatMinute(message.createdAt);
    rows.push({
      type: "msg",
      key: message.id,
      message,
      isGrouped: message.senderId === lastSenderId && minute === lastMinute,
    });
    lastSenderId = message.senderId;
    lastMinute = minute;
  }
  return rows;
}

/**
 * Where each row sits, by key.
 *
 * What jump-to-message needs: a quote, a deep link or a restored anchor names a
 * message, and the virtualizer only understands indexes. A message that is not
 * in the loaded window is simply absent here, which is the caller's cue that a
 * page has to be fetched before it can be scrolled to.
 */
export function timelineRowIndex(rows: readonly TimelineRow[]): Map<string, number> {
  const index = new Map<string, number>();
  for (let i = 0; i < rows.length; i += 1) index.set(rows[i].key, i);
  return index;
}
