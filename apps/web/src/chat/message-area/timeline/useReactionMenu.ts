/**
 * The reaction affordances a message shows on hover, and the picker one of them
 * opens (moved out of ChatMessageArea, issue #834).
 *
 * At most one message shows its menu and at most one has its picker open, so
 * both live here rather than in each row — the alternative is every bubble
 * knowing about every other one.
 *
 * A long press (issue #1152) opens the same menu, for the same single message,
 * marked as touch-opened so the toolbar may fall back to a bottom sheet when
 * there is no room beside its bubble. It is one menu with two ways in, not a
 * second menu.
 */

import { useCallback, useEffect, useRef, useState } from "react";

/** How long the menu survives the pointer leaving, so a diagonal trip to it works. */
const reactionMenuLeaveDelayMs = 150;

export interface ReactionMenuState {
  /** The message whose hover menu is showing, or null. */
  hoveredMessageId: string | null;
  /** The message whose emoji picker is open, or null. */
  openPickerMessageId: string | null;
  /** The message whose menu a long press opened (issue #1152), or null. */
  touchMessageId: string | null;
  /** A touch menu or a picker is open: no swipe may reply under it. */
  touchSurfaceOpen: boolean;
  onReactionMenuVisibleChange: (messageId: string, visible: boolean) => void;
  onPickerOpenChange: (messageId: string, open: boolean) => void;
  /** Opens a message's menu from a long press, replacing any other menu. */
  onReactionMenuLongPress: (messageId: string) => void;
  /**
   * Closes whatever a message has open — menu, touch mark, picker — at once,
   * when that message is removed or its row goes away. Only that message's:
   * a late release never closes a surface another message has opened since.
   */
  onReactionMenuRelease: (messageId: string) => void;
}

/** Null when the state belongs to this message, the state unchanged otherwise. */
function ownOrKeep(current: string | null, messageId: string): string | null {
  return current === messageId ? null : current;
}

export function useReactionMenu(): ReactionMenuState {
  const [openPickerMessageId, setOpenPickerMessageId] = useState<string | null>(null);
  const [hoveredMessageId, setHoveredMessageId] = useState<string | null>(null);
  const [touchMessageId, setTouchMessageId] = useState<string | null>(null);
  const hoverCloseTimerRef = useRef<number | null>(null);

  const clearCloseTimer = useCallback(() => {
    if (hoverCloseTimerRef.current === null) return;
    window.clearTimeout(hoverCloseTimerRef.current);
    hoverCloseTimerRef.current = null;
  }, []);

  // Closing is only ever for the message asking: a message whose toolbar
  // closes late must not take another message's picker with it.
  const onPickerOpenChange = useCallback((messageId: string, open: boolean) => {
    setOpenPickerMessageId((current) => (open ? messageId : ownOrKeep(current, messageId)));
  }, []);

  const onReactionMenuVisibleChange = useCallback(
    (messageId: string, visible: boolean) => {
      // A picker open on another message keeps that message's menu: the pointer
      // is over the picker, not over this row.
      if (openPickerMessageId && openPickerMessageId !== messageId) return;
      clearCloseTimer();
      if (visible) {
        setHoveredMessageId(messageId);
        // Focus moving into a touch-opened menu re-shows the same message:
        // that keeps it touch-opened. Any other message's reveal does not.
        setTouchMessageId((current) => (current === messageId ? current : null));
        return;
      }
      hoverCloseTimerRef.current = window.setTimeout(() => {
        setHoveredMessageId((current) => ownOrKeep(current, messageId));
        setTouchMessageId((current) => ownOrKeep(current, messageId));
        hoverCloseTimerRef.current = null;
      }, reactionMenuLeaveDelayMs);
    },
    [clearCloseTimer, openPickerMessageId],
  );

  const onReactionMenuLongPress = useCallback(
    (messageId: string) => {
      clearCloseTimer();
      setHoveredMessageId(messageId);
      setTouchMessageId(messageId);
      // One contextual surface at a time: another message's picker closes.
      setOpenPickerMessageId((current) => (current === messageId ? current : null));
    },
    [clearCloseTimer],
  );

  const onReactionMenuRelease = useCallback((messageId: string) => {
    setHoveredMessageId((current) => ownOrKeep(current, messageId));
    setTouchMessageId((current) => ownOrKeep(current, messageId));
    setOpenPickerMessageId((current) => ownOrKeep(current, messageId));
  }, []);

  useEffect(() => {
    return () => {
      if (hoverCloseTimerRef.current !== null) window.clearTimeout(hoverCloseTimerRef.current);
    };
  }, []);

  return {
    hoveredMessageId,
    openPickerMessageId,
    touchMessageId,
    touchSurfaceOpen: touchMessageId !== null || openPickerMessageId !== null,
    onReactionMenuVisibleChange,
    onPickerOpenChange,
    onReactionMenuLongPress,
    onReactionMenuRelease,
  };
}
