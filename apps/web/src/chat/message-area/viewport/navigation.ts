/**
 * What a programmatic navigation of the timeline is, as a decision (issue
 * #880).
 *
 * A navigation has a *logical destination*, not a scroll offset: "the end of
 * the conversation", "the first message I have not read". The offset that
 * destination lives at is a function of a layout that is still being measured
 * while the trip is under way — a row replacing its estimate, an attachment
 * finishing its box, the virtualizer mounting the last screenful for the first
 * time — so the offset computed when the reader asked is already stale by the
 * time the scrollport gets there.
 *
 * That is what made "Ir para o final" stop in the middle: the command was
 * one-shot, and whatever happened to the geometry afterwards had nobody left
 * to answer for it. Here the destination stays the destination until it is
 * *confirmed*, and every pass re-derives the offset from the current layout.
 *
 * Pure on purpose, and with no React and no DOM: every ending — confirmed,
 * retargeted, placed by the row model, given up — is reachable from a plain
 * object, which is what lets the controller around it be about *when* a pass
 * happens rather than about what it decides.
 */

import { TAIL_EPSILON_PX } from "../../chatViewportState";

/**
 * The destinations a programmatic navigation can have, strongest first.
 *
 * The order is the #880 priority, written once: a deep link outranks the
 * position the reader was restored to, which outranks the unread boundary,
 * which outranks the end of the conversation. Nothing else in the viewport is
 * allowed to encode this order a second time.
 */
export const NAVIGATION_PRIORITY = [
  "MESSAGE_TARGET",
  "RESTORED_ANCHOR",
  "FIRST_UNREAD",
  "TAIL",
] as const;

export type NavigationTarget = (typeof NAVIGATION_PRIORITY)[number];

/** Why a navigation was asked for — for the tests and the ownership rules. */
export type NavigationReason = "button" | "own-send" | "open";

/** The destinations this controller drives. The other two position themselves. */
export type DrivenTarget = Extract<NavigationTarget, "FIRST_UNREAD" | "TAIL">;

/**
 * The most passes one navigation may span.
 *
 * A ceiling, not the expected exit — the same argument PREPEND_RESTORE's
 * budget carries: every pass either confirms, moves the scrollport (which is
 * what produces the next pass) or gives up, so termination comes from those
 * conditions. This only exists so that a layout correcting in circles cannot
 * hold the scrollport forever.
 */
export const MAX_NAVIGATION_PASSES = 60;

/**
 * How close the unread separator has to land to its contextual offset.
 *
 * Looser than the prepend restoration's single pixel: this is a reading
 * position being *offered*, not one being put back, and a row measured a few
 * pixels differently than the model assumed is not a defect worth another
 * correction pass.
 */
export const UNREAD_ANCHOR_TOLERANCE_PX = 8;

/**
 * How far below the top edge the "Novas mensagens" separator should sit
 * (#880 item 8).
 *
 * Not `block: "start"`: flush against the top edge the boundary reads as the
 * beginning of the conversation, with nothing above it to say what was already
 * read. A little context above it is what makes it a *boundary*.
 *
 * Proportional to the viewport, bounded at both ends, and derived here rather
 * than spelled out at each call site: on a phone a quarter of the screen is
 * barely a message, and on a tall desktop window a fixed 120px would be a
 * sliver. The bounds are what keep both from turning into half a screen of
 * already-read history.
 */
export const UNREAD_CONTEXT_MIN_PX = 56;
export const UNREAD_CONTEXT_MAX_PX = 200;
export const UNREAD_CONTEXT_RATIO = 0.22;

export function unreadContextOffsetPx(viewportPx: number): number {
  const proportional = Math.round(viewportPx * UNREAD_CONTEXT_RATIO);
  return Math.min(UNREAD_CONTEXT_MAX_PX, Math.max(UNREAD_CONTEXT_MIN_PX, proportional));
}

/**
 * Whether the viewport is really at the end (#880 item 5).
 *
 * Both halves are required, and neither is enough on its own. The bottom
 * sentinel is the authoritative confirmation — it is a node in the same layout
 * as the messages, so it moves when they do — but under virtualization it is
 * positioned by the canvas, whose height trails a row that has just been
 * measured by one commit: a DEV capture of #880 has it reported as intersecting
 * with 214px of real content still below the fold. The geometry is what closes
 * that gap, and it alone would trust a distance measured in the middle of an
 * animation.
 */
export function tailConfirmed(remainingPx: number, sentinelVisible: boolean): boolean {
  return sentinelVisible && remainingPx <= TAIL_EPSILON_PX;
}

/**
 * Whether the separator landed where the contextual offset asks for it.
 *
 * `layoutSettled` is this destination's equivalent of the tail's sentinel: the
 * boundary's only authority is its own measured box, so a reading taken while
 * rows are still replacing their estimates is a reading that the next
 * measurement will move. Confirming on one of those is how the separator ended
 * up 180px lower than asked for, with nobody left to correct it.
 */
export function unreadConfirmed(
  dividerOffsetPx: number | null,
  contextOffsetPx: number,
  atTopOfHistory: boolean,
  layoutSettled: boolean,
): boolean {
  if (dividerOffsetPx === null || !layoutSettled) return false;
  // At the very top there is no context left to show above the boundary, so
  // "as close as the conversation allows" is the destination.
  if (atTopOfHistory) return dividerOffsetPx <= contextOffsetPx + UNREAD_ANCHOR_TOLERANCE_PX;
  return Math.abs(dividerOffsetPx - contextOffsetPx) <= UNREAD_ANCHOR_TOLERANCE_PX;
}

/** What one pass of a navigation decides to do. */
export type NavigationStep =
  /** The destination is confirmed: release the scrollport. */
  | { kind: "confirm" }
  /** Move the scrollport to where the destination is *now*. */
  | { kind: "seek"; scrollTopPx: number; behavior: ScrollBehavior }
  /** Something is still travelling toward it; this pass does nothing. */
  | { kind: "wait" }
  /** Nothing further is possible — end the operation rather than hold it open. */
  | { kind: "abandon" };

export interface NavigationState {
  /** Whether the destination is confirmed (see tailConfirmed/unreadConfirmed). */
  confirmed: boolean;
  /**
   * Where the scrollport must sit, or null while neither the mounted row nor
   * the row model can say — a destination that is not in the loaded window at
   * all, which is not something a scroll can fix.
   */
  desiredScrollTopPx: number | null;
  currentScrollTopPx: number;
  /** Where the scrollport was when the previous pass looked, null on the first. */
  lastScrollTopPx: number | null;
  /**
   * The position this navigation last wrote, null when it has written none.
   * What tells its own instant write apart from an animation still in flight:
   * arriving exactly where it asked to be is not "something else is moving
   * this", it is this pass's predecessor having landed.
   */
  writtenScrollTopPx: number | null;
  /**
   * Whether another pass is already on its way.
   *
   * What separates "there is nothing more this pass can do" from "there is
   * nothing more this pass can do *yet*". Two things put a successor on the
   * way: rows still replacing estimates with real heights, and an arrival that
   * an observer has yet to report. Giving up on one of those frames is exactly
   * how a trip ends in the middle — either the destination drifts afterwards
   * with nobody left to correct it, or the scrollport is at the end and the
   * confirmation that says so arrives one frame too late.
   */
  successorExpected: boolean;
  /**
   * Where an animated scroll is still travelling to, or null when the last
   * write was instant (or there was none).
   */
  animatingTowardPx: number | null;
  /** How many passes this navigation has already taken. */
  passes: number;
  /** Animated only while the trip is short enough — see scrollToEndBehavior. */
  behavior: ScrollBehavior;
}

/**
 * One pass of a navigation.
 *
 * The termination argument, which is the whole point:
 *
 *   confirm/abandon  the operation ends;
 *   seek             the scrollport moves, and a move is what produces the
 *                    scroll event, the virtualizer's re-render and therefore
 *                    the next pass;
 *   wait             the scrollport moved on its own since the previous pass —
 *                    an animation, or a layout settling — so it already has a
 *                    successor and a write here would only cancel it.
 *
 * A pass that cannot move the scrollport (it is already exactly where the
 * destination says, and the destination is still not confirmed) has no
 * successor of its own, so it ends the operation rather than wait for a commit
 * that is never coming — the same rule PREPEND_RESTORE terminates on. It waits
 * only while something else is known to be producing the next pass.
 */
export function navigationStep(state: NavigationState): NavigationStep {
  if (state.confirmed) return { kind: "confirm" };
  if (state.passes > MAX_NAVIGATION_PASSES) return { kind: "abandon" };
  const desiredPx = state.desiredScrollTopPx;
  if (desiredPx === null) return { kind: "abandon" };
  // An animation is asked about first: while one runs the scrollport is moving
  // by definition, and the question is only whether it is still headed
  // somewhere useful. If the destination moved under it, the retarget is
  // instant on purpose (#880 item 8) — the animation dies with it.
  if (state.animatingTowardPx !== null) {
    if (animationOnCourse(state, desiredPx)) return { kind: "wait" };
    return { kind: "seek", scrollTopPx: desiredPx, behavior: state.behavior };
  }
  if (movingOnItsOwn(state)) return { kind: "wait" };
  if (Math.round(desiredPx) === Math.round(state.currentScrollTopPx)) {
    return state.successorExpected ? { kind: "wait" } : { kind: "abandon" };
  }
  return { kind: "seek", scrollTopPx: desiredPx, behavior: state.behavior };
}

/** Whether the scrollport moved since the previous pass, and not by this one. */
function movingOnItsOwn(state: NavigationState): boolean {
  const landedOnOwnWrite =
    state.writtenScrollTopPx !== null &&
    Math.round(state.writtenScrollTopPx) === Math.round(state.currentScrollTopPx);
  return (
    state.lastScrollTopPx !== null &&
    state.lastScrollTopPx !== state.currentScrollTopPx &&
    !landedOnOwnWrite
  );
}

/**
 * Whether an animated scroll is still on its way to where it was aimed.
 *
 * An animation the browser owns has not moved yet on the commit that follows
 * asking for it — the phase change alone produces one — and a plain write on
 * that commit would cancel it before it started, which is how a short trip
 * ended up teleporting. It only stops being on course when the destination
 * moves under it, and then the retarget is instant on purpose (#880 item 8).
 */
function animationOnCourse(state: NavigationState, desiredPx: number): boolean {
  return (
    state.animatingTowardPx !== null &&
    Math.round(state.animatingTowardPx) === Math.round(desiredPx)
  );
}

/**
 * What a scroll event is measured against while a navigation owns the
 * scrollport.
 */
export interface OwnMovement {
  /** Where the scrollport was when the navigation last looked. */
  lastScrollTopPx: number | null;
  /** Where the navigation last sent it. */
  writtenScrollTopPx: number | null;
  /** Whether that write was an animation still travelling between the two. */
  animating: boolean;
}

/**
 * Whether the scrollport is somewhere this navigation did not put it (#880).
 *
 * The second half of recognising a scrollbar drag, and the half that does not
 * depend on the browser's scrollbars being the classic kind: where those are
 * drawn as an overlay, the drag never reaches the page as a pointer event at
 * all, and a scroll event is the only thing the reader leaves behind.
 *
 * Not a guess from pixels: the navigation knows both positions it can be
 * responsible for — where it last looked, and where it last wrote — and a
 * settled scroll event landing on neither was somebody else's, which at this
 * point can only be the reader. An animation in flight passes through every
 * position between the two, so while one runs this says nothing and the
 * pointer signals answer instead.
 */
export function movedByReader(own: OwnMovement, scrollTopPx: number): boolean {
  if (own.animating) return false;
  const known = [own.lastScrollTopPx, own.writtenScrollTopPx];
  return !known.some((px) => px !== null && Math.abs(px - scrollTopPx) <= TAIL_EPSILON_PX);
}

/**
 * Whether an animated trip could be taken away from the navigation mid-flight
 * (#880).
 *
 * An animation is the one state where the scrollport's position says nothing:
 * it travels through every offset between where it started and where it is
 * aimed, so a reader's own scroll is indistinguishable from the next frame of
 * it — `movedByReader` is silent for exactly that reason. What rescues it is
 * the pointer, and the pointer only exists where the scrollbar is part of the
 * page: a classic scrollbar takes its width out of `clientWidth`, and one
 * drawn as an overlay (macOS, and this project's headless Chromium) takes
 * none, because it is browser chrome that the page never hears from.
 *
 * So the rule is not about taste: animate only where the reader can still take
 * over. Everywhere else the trip is instant, and #880's contract — the right
 * destination, and a reader who can always overrule it — is kept whole. The
 * distance rule (#675) still applies on top of this.
 */
export function animationIsInterruptible(offsetWidthPx: number, clientWidthPx: number): boolean {
  return offsetWidthPx > clientWidthPx;
}

/**
 * Whether a pointer went down on the scrollport's own scrollbar (#880).
 *
 * The scrollbar sits outside the padding box, and `clientWidth` is measured
 * without it — so a pointer whose offset is past that edge is on the scrollbar
 * and nowhere else. A left-hand scrollbar (a right-to-left timeline) puts it
 * before the padding box instead, which is the negative case.
 *
 * Pure, and about coordinates only: whether the event even belongs to the
 * scrollport rather than to a message inside it is the caller's question, and
 * the DOM answers it exactly (`event.target`).
 */
export function isScrollbarPointer(offsetXPx: number, clientWidthPx: number): boolean {
  return offsetXPx >= clientWidthPx || offsetXPx < 0;
}

// ── The floating control (#880 item 10) ──────────────────────────────────────

/** What the one floating control means right now. */
export type ScrollButtonMode = "tail" | "first-unread";

export interface ScrollButtonState {
  visible: boolean;
  mode: ScrollButtonMode;
  /** Real unread messages — never "how many messages are below the fold". */
  count: number;
}

export interface ScrollButtonInput {
  /** Whether the reader is away from the end (the phase decides this). */
  awayFromTail: boolean;
  /** Unread as the read cursor knows it, plus what arrived since. */
  unreadCount: number;
  /** Whether this conversation has an unread boundary row at all. */
  hasBoundary: boolean;
  /** Whether that boundary is still below the fold — not yet reached. */
  boundaryAhead: boolean;
}

/**
 * Which of the two meanings the single control carries (#880 item 10).
 *
 * There is one button, and the destination it offers is the *nearest* thing
 * the reader has not seen: the unread boundary while it is still below them,
 * the end of the conversation once it is not. Jumping straight to the end
 * while unread messages sit above it would skip exactly what the reader came
 * back for.
 */
export function scrollButtonState(input: ScrollButtonInput): ScrollButtonState {
  const offersBoundary = input.hasBoundary && input.boundaryAhead && input.unreadCount > 0;
  return {
    visible: input.awayFromTail,
    mode: offersBoundary ? "first-unread" : "tail",
    count: input.unreadCount,
  };
}
