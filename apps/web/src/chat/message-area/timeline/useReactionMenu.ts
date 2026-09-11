/**
 * The reaction affordances a message shows on hover, and the picker one of them
 * opens (moved out of ChatMessageArea, issue #834).
 *
 * At most one message shows its menu and at most one has its picker open, so
 * both live here rather than in each row — the alternative is every bubble
 * knowing about every other one.
 */

import { useCallback, useEffect, useRef, useState } from "react";

/** How long the menu survives the pointer leaving, so a diagonal trip to it works. */
const reactionMenuLeaveDelayMs = 150;

export interface ReactionMenuState {
  /** The message whose hover menu is showing, or null. */
  hoveredMessageId: string | null;
  /** The message whose emoji picker is open, or null. */
  openPickerMessageId: string | null;
  onReactionMenuVisibleChange: (messageId: string, visible: boolean) => void;
  onPickerOpenChange: (messageId: string, open: boolean) => void;
}

export function useReactionMenu(): ReactionMenuState {
  const [openPickerMessageId, setOpenPickerMessageId] = useState<string | null>(null);
  const [hoveredMessageId, setHoveredMessageId] = useState<string | null>(null);
  const hoverCloseTimerRef = useRef<number | null>(null);

  const onPickerOpenChange = useCallback((messageId: string, open: boolean) => {
    setOpenPickerMessageId(open ? messageId : null);
  }, []);

  const onReactionMenuVisibleChange = useCallback(
    (messageId: string, visible: boolean) => {
      // A picker open on another message keeps that message's menu: the pointer
      // is over the picker, not over this row.
      if (openPickerMessageId && openPickerMessageId !== messageId) return;
      if (hoverCloseTimerRef.current !== null) {
        window.clearTimeout(hoverCloseTimerRef.current);
        hoverCloseTimerRef.current = null;
      }
      if (visible) {
        setHoveredMessageId(messageId);
        return;
      }
      hoverCloseTimerRef.current = window.setTimeout(() => {
        setHoveredMessageId((current) => (current === messageId ? null : current));
        hoverCloseTimerRef.current = null;
      }, reactionMenuLeaveDelayMs);
    },
    [openPickerMessageId],
  );

  useEffect(() => {
    return () => {
      if (hoverCloseTimerRef.current !== null) window.clearTimeout(hoverCloseTimerRef.current);
    };
  }, []);

  return {
    hoveredMessageId,
    openPickerMessageId,
    onReactionMenuVisibleChange,
    onPickerOpenChange,
  };
}
