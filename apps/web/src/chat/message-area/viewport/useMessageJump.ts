/**
 * Travelling to a specific message: a quote's "go to the original", a
 * reference, and a `?message=` deep link (moved out of ChatMessageArea, issue
 * #834).
 *
 * The trip itself belongs to the scroll authority — see the core's
 * scrollToMessage. What is left here is what surrounds it: the brief highlight
 * that says "this is the one", the rule that a deep link is followed once
 * per link rather than on every render that mentions it, and — since #1082 —
 * the jump's ownership of the scrollport while it travels.
 *
 * A jump owns the scrollport from the moment it is asked for until its
 * destination has landed (see navigation's jumpLanded), the browser reports
 * the scroll finished, or the reader takes over. A deep link claims it from
 * the first commit that can follow it, before the scroll even starts: the
 * frames in between are where the timeline happened to be, not where anybody
 * is reading, and the read cursor must not take them for a reading position.
 */

import { useCallback, useEffect, useLayoutEffect, useRef, useState, type RefObject } from "react";

import type { Message } from "../../chatTypes";
import { jumpLanded } from "./navigation";

/** How long a jumped-to message stays highlighted. */
const quoteHighlightMs = 1_200;

/** What a jump needs from the scroll authority, and nothing more. */
export interface MessageJumpCommands {
  scrollToMessage: (messageId: string) => boolean;
  hasRow: (messageId: string) => boolean;
  beginJump: (messageId: string) => void;
  endJump: () => void;
  jumpRef: RefObject<{ messageId: string } | null>;
  listRef: RefObject<HTMLDivElement | null>;
  messageRefs: RefObject<Map<string, HTMLElement>>;
}

/** Whether a deep link is still to be followed, given what was followed last. */
function needsFollowing(
  focusMessageId: string | undefined,
  focusRequest: string,
  followed: { messageId: string; request: string },
): focusMessageId is string {
  if (!focusMessageId) return false;
  const repeatRequested = focusRequest !== "" && focusRequest !== followed.request;
  return followed.messageId !== focusMessageId || repeatRequested;
}

/** Ends the jump in flight once its destination row sits where it was sent. */
function settleJump(commands: MessageJumpCommands) {
  const jump = commands.jumpRef.current;
  const list = commands.listRef.current;
  const row = jump ? commands.messageRefs.current.get(jump.messageId) : undefined;
  if (!list || !row) return;
  const box = row.getBoundingClientRect();
  if (jumpLanded(box, list.getBoundingClientRect().top, list)) commands.endJump();
}

export interface MessageJumpState {
  /** The message currently flashing after a jump, or null. */
  highlightedMessageId: string | null;
  jumpToMessage: (messageId: string) => void;
}

export function useMessageJump(
  commands: MessageJumpCommands,
  messages: Message[],
  focusMessageId?: string,
  focusRequest = "",
  focusMissed = false,
): MessageJumpState {
  const { scrollToMessage, hasRow, beginJump, endJump } = commands;
  const [highlightedMessageId, setHighlightedMessageId] = useState<string | null>(null);
  const highlightTimerRef = useRef<number | null>(null);

  const jumpToMessage = useCallback(
    (messageId: string) => {
      if (!scrollToMessage(messageId)) {
        endJump();
        return;
      }
      beginJump(messageId);
      setHighlightedMessageId(messageId);
      if (highlightTimerRef.current !== null) window.clearTimeout(highlightTimerRef.current);
      highlightTimerRef.current = window.setTimeout(() => {
        setHighlightedMessageId(null);
        highlightTimerRef.current = null;
      }, quoteHighlightMs);
    },
    [scrollToMessage, beginJump, endJump],
  );

  // The deep link follows whatever the latest jump is, without the jump being
  // one of its dependencies — the same "ref holds the latest callback" shape
  // useMessages uses for every one of its onX callbacks. Re-running this
  // because the jump was rebuilt would re-follow a link already followed.
  const jumpRef = useRef(jumpToMessage);
  useLayoutEffect(() => {
    jumpRef.current = jumpToMessage;
  });

  // What was last followed: the message, and the request that asked for it
  // (issue #896). A link is followed once, so a re-render never re-travels —
  // but a *new* explicit request for the same message does, which is what
  // activating "Ir para a mensagem" twice means. An unmarked request ("")
  // never counts as new: it is how a navigation that only keeps the query
  // arrives.
  const followedRef = useRef({ messageId: "", request: "" });

  // #1088: the opening could not reach this message and settled elsewhere. The
  // request that asked for it is spent, so the page that later brings the
  // message in by itself does not travel — only asking again does. Registered
  // before the effect below, which therefore always sees it spent.
  useEffect(() => {
    if (!focusMissed || !focusMessageId) return;
    if (followedRef.current.messageId === focusMessageId) return;
    followedRef.current = { messageId: focusMessageId, request: focusRequest };
  }, [focusMissed, focusMessageId, focusRequest]);

  // The deep link's claim on the scrollport, taken in the layout phase so no
  // frame between this commit and the scroll below is read as a position.
  useLayoutEffect(() => {
    if (
      needsFollowing(focusMessageId, focusRequest, followedRef.current) &&
      hasRow(focusMessageId)
    ) {
      beginJump(focusMessageId);
    }
  }, [beginJump, hasRow, focusMessageId, focusRequest, messages]);

  useEffect(() => {
    if (!focusMessageId) {
      followedRef.current = { messageId: "", request: "" };
      return;
    }
    if (!needsFollowing(focusMessageId, focusRequest, followedRef.current)) return;
    // Loaded is enough — mounted is the virtualizer's business, not this
    // effect's. A message no page has reached yet is still skipped, and the
    // bounded backward search in useOpenPosition is what brings it in.
    if (!hasRow(focusMessageId)) return;
    followedRef.current = { messageId: focusMessageId, request: focusRequest };
    jumpRef.current(focusMessageId);
  }, [hasRow, focusMessageId, focusRequest, messages]);

  useEffect(() => {
    return () => {
      if (highlightTimerRef.current !== null) window.clearTimeout(highlightTimerRef.current);
    };
  }, []);

  // Landing is checked after every commit (the row mounting, a remeasure) and
  // on every scroll; `scrollend` is the browser itself saying the trip is over.
  const commandsRef = useRef(commands);
  useLayoutEffect(() => {
    commandsRef.current = commands;
    settleJump(commands);
  });
  useEffect(() => {
    const list = commandsRef.current.listRef.current;
    if (!list) return;
    const onScroll = () => settleJump(commandsRef.current);
    const onScrollEnd = () => commandsRef.current.endJump();
    list.addEventListener("scroll", onScroll, { passive: true });
    list.addEventListener("scrollend", onScrollEnd);
    return () => {
      list.removeEventListener("scroll", onScroll);
      list.removeEventListener("scrollend", onScrollEnd);
    };
  }, []);

  return { highlightedMessageId, jumpToMessage };
}
