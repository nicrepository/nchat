/**
 * Travelling to a specific message: a quote's "go to the original", a
 * reference, and a `?message=` deep link (moved out of ChatMessageArea, issue
 * #834).
 *
 * The trip itself belongs to the scroll authority — see the core's
 * scrollToMessage. What is left here is what surrounds it: the brief highlight
 * that says "this is the one", and the rule that a deep link is followed once
 * per link rather than on every render that mentions it.
 */

import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";

import type { Message } from "../../chatTypes";

/** How long a jumped-to message stays highlighted. */
const quoteHighlightMs = 1_200;

/** What a jump needs from the scroll authority, and nothing more. */
export interface MessageJumpCommands {
  scrollToMessage: (messageId: string) => boolean;
  hasRow: (messageId: string) => boolean;
}

export interface MessageJumpState {
  /** The message currently flashing after a jump, or null. */
  highlightedMessageId: string | null;
  jumpToMessage: (messageId: string) => void;
}

export function useMessageJump(
  { scrollToMessage, hasRow }: MessageJumpCommands,
  messages: Message[],
  focusMessageId?: string,
): MessageJumpState {
  const [highlightedMessageId, setHighlightedMessageId] = useState<string | null>(null);
  const highlightTimerRef = useRef<number | null>(null);

  const jumpToMessage = useCallback(
    (messageId: string) => {
      if (!scrollToMessage(messageId)) return;
      setHighlightedMessageId(messageId);
      if (highlightTimerRef.current !== null) window.clearTimeout(highlightTimerRef.current);
      highlightTimerRef.current = window.setTimeout(() => {
        setHighlightedMessageId(null);
        highlightTimerRef.current = null;
      }, quoteHighlightMs);
    },
    [scrollToMessage],
  );

  // The deep link follows whatever the latest jump is, without the jump being
  // one of its dependencies — the same "ref holds the latest callback" shape
  // useMessages uses for every one of its onX callbacks. Re-running this
  // because the jump was rebuilt would re-follow a link already followed.
  const jumpRef = useRef(jumpToMessage);
  useLayoutEffect(() => {
    jumpRef.current = jumpToMessage;
  });

  const focusedMessageRef = useRef("");
  useEffect(() => {
    if (!focusMessageId) {
      focusedMessageRef.current = "";
      return;
    }
    if (focusedMessageRef.current === focusMessageId) return;
    // Loaded is enough — mounted is the virtualizer's business, not this
    // effect's. A message no page has reached yet is still skipped, and the
    // bounded backward search in useOpenPosition is what brings it in.
    if (!hasRow(focusMessageId)) return;
    focusedMessageRef.current = focusMessageId;
    jumpRef.current(focusMessageId);
  }, [hasRow, focusMessageId, messages]);

  useEffect(() => {
    return () => {
      if (highlightTimerRef.current !== null) window.clearTimeout(highlightTimerRef.current);
    };
  }, []);

  return { highlightedMessageId, jumpToMessage };
}
