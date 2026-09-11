/**
 * The conversation viewport's scroll authority (issue #834).
 *
 * These refs are not a grab bag: together they *are* the state the
 * #492/#675/#788 invariants are written against — who is allowed to move the
 * scrollport right now, where the reader was, and whether the last reading was
 * trustworthy. Every primitive in this directory is one rule over this same
 * state, so it is declared once, here.
 *
 * Reading it is open; changing it is not. Every write goes through one of the
 * named transitions below, and each of them is declared in this hook because
 * that is the only place allowed to perform it: react-hooks/immutability
 * forbids a hook from modifying anything it received as an argument, refs
 * included. That constraint turns out to be the right boundary anyway — a
 * primitive decides *when* a transition happens, and never what the state then
 * looks like.
 *
 * Refs rather than state throughout: these values are touched inside effects
 * and inside async scroll/observer callbacks, never during render, which is
 * also what react-hooks/refs requires.
 */

import { useCallback, useLayoutEffect, useRef, useState, type RefObject } from "react";
import type { Virtualizer } from "@tanstack/react-virtual";

import type { ViewportPhase } from "../../chatViewportState";
import type { ViewportAnchorPoint } from "./scrollCommands";

/** An in-flight prepend restoration (#675), or null when there is none. */
export interface PrependRestore {
  messageId: string;
  offsetPx: number;
  passes: number;
  /** Whether the anchor row has been measured at least once. */
  measured: boolean;
}

/** What a scroll event said, once its geometry was found trustworthy. */
export interface ReadingPosition {
  /** Within the ~150px courtesy threshold of the end. */
  nearBottom: boolean;
  /** Within TAIL_EPSILON_PX of it — the follow-the-tail intent. */
  followTail: boolean;
}

export interface ViewportCore {
  /** The scroll container. */
  listRef: RefObject<HTMLDivElement | null>;
  /** #788: the content wrapper, whose box height is what a growing row moves. */
  contentRef: RefObject<HTMLDivElement | null>;
  /** The tail sentinel: both the scroll target and the arrival confirmation. */
  bottomRef: RefObject<HTMLDivElement | null>;
  /** The pagination sentinel. */
  topSentinelRef: RefObject<HTMLDivElement | null>;
  /** #492: AT_FIRST_UNREAD lands on the separator, not on the message below it. */
  unreadDividerRef: RefObject<HTMLDivElement | null>;
  /** The mounted message rows, by message id. */
  messageRefs: RefObject<Map<string, HTMLDivElement>>;
  /** #675: row index by key, so an unmounted row can still be reached. */
  rowIndexRef: RefObject<Map<string, number>>;
  /** The virtualizer while virtualized, null while the plain path renders. */
  virtualizerRef: RefObject<Virtualizer<HTMLDivElement, Element> | null>;
  /**
   * Which state the viewport is in, for the async callbacks (scroll handler,
   * sentinels) that would otherwise close over a stale value.
   */
  phaseRef: RefObject<ViewportPhase>;
  /**
   * #788: the INTENT to follow the tail, which is not the same thing as the
   * geometry of being at it. The phase cannot answer this on its own: the
   * scroll handler assigns AT_BOTTOM anywhere inside the 150px courtesy
   * threshold, so a deliberate small scroll up keeps the phase AT_BOTTOM while
   * the reader is no longer following the end.
   *
   * Starts true: a conversation that resolves to the bottom is positioned at
   * the tail before any scroll event can report it.
   */
  followTailRef: RefObject<boolean>;
  /**
   * Mirrors `phase === "AT_BOTTOM"` for synchronous reads inside the scroll
   * handler, which can't rely on React state having committed yet within the
   * same event.
   */
  isNearBottomRef: RefObject<boolean>;
  /**
   * #788: scrollHeight as of the previous scroll event, so the handler can tell
   * a reader's scroll from a scroll event whose geometry an async reflow has
   * already moved.
   */
  lastScrollHeightRef: RefObject<number>;
  /** scrollHeight before a prepend, for the non-virtualized delta restoration. */
  prevScrollHeightRef: RefObject<number>;
  /**
   * While this is set it is the ONLY thing allowed to write scrollTop: the
   * tail-lock stands down, the virtualizer's own resize compensation stands
   * down, and the scroll handler stops re-deriving the anchor, so a correction
   * cannot be mistaken for the reader moving.
   */
  prependRestoreRef: RefObject<PrependRestore | null>;
  /** Where the reader is, as of the last trustworthy reading. */
  currentAnchorRef: RefObject<ViewportAnchorPoint | null>;
  /** Whether the last scroll could not resolve an anchor (see the handler). */
  anchorStaleRef: RefObject<boolean>;

  // ── Transitions ───────────────────────────────────────────────────────────

  /** Callback ref for the scroll container; also publishes it as scrollRoot. */
  attachList: (element: HTMLDivElement | null) => void;
  setMessageRef: (messageId: string, el: HTMLDivElement | null) => void;
  setPhase: (phase: ViewportPhase) => void;
  /** #675: the row model the positions below are expressed against. */
  setRowModel: (
    rowIndex: Map<string, number>,
    virtualizer: Virtualizer<HTMLDivElement, Element> | null,
  ) => void;
  /**
   * Records where the reader is, and whether the reading was usable at all.
   * A null reading means no mounted row was in view, which is not a position
   * anyone had — the next commit is asked to answer instead.
   */
  recordAnchor: (topmost: ViewportAnchorPoint | null) => void;
  /** The restoration landing on target: the anchor is known exactly. */
  setAnchor: (anchor: ViewportAnchorPoint) => void;
  /** A scroll event: its size always, its geometry only when trustworthy. */
  recordScrollEvent: (scrollHeight: number, reading: ReadingPosition | null) => void;
  /** The bottom sentinel's unambiguous confirmation that the tail is on screen. */
  confirmAtTail: () => void;
  /** The last stable scrollHeight, for the plain path's prepend delta. */
  snapshotScrollHeight: (px: number) => void;
  armPrependRestore: (anchor: ViewportAnchorPoint) => void;
  /** Begins a pass and returns how many there have now been. */
  countRestorePass: () => number;
  markRestoreMeasured: () => void;
  /** Ends `restore`, unless something else already replaced it. */
  endPrependRestore: (restore: PrependRestore) => boolean;
  /**
   * Puts a message on screen, and says whether it could (#675).
   *
   * A mounted row is scrolled to directly; a row the virtualizer has not
   * mounted is identified logically and placed by the virtualizer instead —
   * `auto` rather than `smooth`, because an animated scroll cannot be corrected
   * as rows on the way are measured for the first time, which is exactly what
   * happens when travelling into unvisited history. False means the message is
   * not in the loaded window at all, and nothing moved.
   */
  scrollToMessage: (messageId: string) => boolean;
  /** Whether a message is in the loaded window, mounted or not. */
  hasRow: (messageId: string) => boolean;
}

export interface ViewportCoreState {
  core: ViewportCore;
  /** The render-time phase. Effects read phaseRef; renders read this. */
  phase: ViewportPhase;
  /**
   * #675: the scroll container as state, because the attachment observers need
   * it as their IntersectionObserver root and a ref alone never tells a
   * consumer it has arrived.
   */
  scrollRoot: HTMLDivElement | null;
}

export function useViewportCore(): ViewportCoreState {
  const [phase, setPhase] = useState<ViewportPhase>("RESTORING_POSITION");
  const [scrollRoot, setScrollRoot] = useState<HTMLDivElement | null>(null);
  const phaseRef = useRef<ViewportPhase>(phase);
  useLayoutEffect(() => {
    phaseRef.current = phase;
  }, [phase]);

  const listRef = useRef<HTMLDivElement>(null);
  const contentRef = useRef<HTMLDivElement>(null);
  const bottomRef = useRef<HTMLDivElement>(null);
  const topSentinelRef = useRef<HTMLDivElement>(null);
  const unreadDividerRef = useRef<HTMLDivElement>(null);
  const messageRefs = useRef(new Map<string, HTMLDivElement>());
  const rowIndexRef = useRef<Map<string, number>>(new Map());
  const virtualizerRef = useRef<Virtualizer<HTMLDivElement, Element> | null>(null);
  const followTailRef = useRef(true);
  const isNearBottomRef = useRef(true);
  const lastScrollHeightRef = useRef(0);
  const prevScrollHeightRef = useRef(0);
  const prependRestoreRef = useRef<PrependRestore | null>(null);
  const currentAnchorRef = useRef<ViewportAnchorPoint | null>(null);
  const anchorStaleRef = useRef(false);

  const attachList = useCallback((element: HTMLDivElement | null) => {
    listRef.current = element;
    setScrollRoot(element);
  }, []);

  const setMessageRef = useCallback((messageId: string, el: HTMLDivElement | null) => {
    if (el) messageRefs.current.set(messageId, el);
    else messageRefs.current.delete(messageId);
  }, []);

  const setRowModel = useCallback(
    (rowIndex: Map<string, number>, virtualizer: Virtualizer<HTMLDivElement, Element> | null) => {
      rowIndexRef.current = rowIndex;
      virtualizerRef.current = virtualizer;
    },
    [],
  );

  const recordAnchor = useCallback((topmost: ViewportAnchorPoint | null) => {
    if (topmost) {
      currentAnchorRef.current = topmost;
      anchorStaleRef.current = false;
    } else {
      anchorStaleRef.current = true;
    }
  }, []);

  const setAnchor = useCallback((anchor: ViewportAnchorPoint) => {
    currentAnchorRef.current = anchor;
  }, []);

  const recordScrollEvent = useCallback((scrollHeight: number, reading: ReadingPosition | null) => {
    lastScrollHeightRef.current = scrollHeight;
    if (!reading) return;
    isNearBottomRef.current = reading.nearBottom;
    followTailRef.current = reading.followTail;
  }, []);

  const confirmAtTail = useCallback(() => {
    isNearBottomRef.current = true;
    followTailRef.current = true;
  }, []);

  const snapshotScrollHeight = useCallback((px: number) => {
    prevScrollHeightRef.current = px;
  }, []);

  const armPrependRestore = useCallback((anchor: ViewportAnchorPoint) => {
    prependRestoreRef.current = {
      messageId: anchor.messageId,
      offsetPx: anchor.offsetPx,
      passes: 0,
      measured: false,
    };
  }, []);

  const countRestorePass = useCallback(() => {
    const restore = prependRestoreRef.current;
    if (!restore) return 0;
    restore.passes += 1;
    return restore.passes;
  }, []);

  const markRestoreMeasured = useCallback(() => {
    const restore = prependRestoreRef.current;
    if (restore) restore.measured = true;
  }, []);

  const endPrependRestore = useCallback((restore: PrependRestore) => {
    if (prependRestoreRef.current !== restore) return false;
    prependRestoreRef.current = null;
    return true;
  }, []);

  const scrollToMessage = useCallback((messageId: string) => {
    const el = messageRefs.current.get(messageId);
    if (el) {
      el.scrollIntoView({ behavior: "smooth", block: "center" });
      return true;
    }
    const index = rowIndexRef.current.get(messageId);
    if (index === undefined) return false;
    virtualizerRef.current?.scrollToIndex(index, { align: "center" });
    return true;
  }, []);

  const hasRow = useCallback((messageId: string) => rowIndexRef.current.has(messageId), []);

  // One object for the life of the component: the primitives below take it as a
  // dependency, and a fresh identity each render would re-run every one of
  // their effects — tearing down the observers this state exists to protect.
  //
  // useState with an initializer, never useRef: react-hooks/refs forbids
  // reading a ref's .current during render, and this value has to be returned
  // from one. Its setter is never called — the stable identity is the point.
  const [core] = useState<ViewportCore>(() => ({
    listRef,
    contentRef,
    bottomRef,
    topSentinelRef,
    unreadDividerRef,
    messageRefs,
    rowIndexRef,
    virtualizerRef,
    phaseRef,
    followTailRef,
    isNearBottomRef,
    lastScrollHeightRef,
    prevScrollHeightRef,
    prependRestoreRef,
    currentAnchorRef,
    anchorStaleRef,
    attachList,
    setMessageRef,
    setPhase,
    setRowModel,
    recordAnchor,
    setAnchor,
    recordScrollEvent,
    confirmAtTail,
    snapshotScrollHeight,
    armPrependRestore,
    countRestorePass,
    markRestoreMeasured,
    endPrependRestore,
    scrollToMessage,
    hasRow,
  }));

  return { core, phase, scrollRoot };
}
