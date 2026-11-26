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
 *
 * The selected message (CQR R3 of issue #1238) lives here too, at the
 * conversation's level: the touch way to a message's actions without a long
 * press is a contextual mode of the conversation's header, which is no row's
 * child. One message at a time, of this conversation only, and released with
 * the rest of what that message has open — removed, unmounted or recycled.
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
  /**
   * The message selected for its actions on touch (issue #1238), or null.
   * Only ever one of this conversation's: a new conversation starts with none.
   */
  selectedMessageId: string | null;
  /** A touch menu or a picker is open: no swipe may reply under it. */
  touchSurfaceOpen: boolean;
  onReactionMenuVisibleChange: (messageId: string, visible: boolean) => void;
  onPickerOpenChange: (messageId: string, open: boolean) => void;
  /** Opens a message's menu from a long press, replacing any other menu. */
  onReactionMenuLongPress: (messageId: string) => void;
  /** Selects a message (issue #1238), replacing any other selection. */
  onMessageSelect: (messageId: string) => void;
  /**
   * Closes whatever a message has open — menu, touch mark, picker, selection —
   * at once, when that message is removed, its row goes away or its selection
   * ends. Only that message's: a late release never closes a surface another
   * message has opened since.
   */
  onReactionMenuRelease: (messageId: string) => void;
}

/** Null when the state belongs to this message, the state unchanged otherwise. */
function ownOrKeep(current: string | null, messageId: string): string | null {
  return current === messageId ? null : current;
}

/** The id of a message's menu, which the header's actions button controls. */
export function reactionMenuId(messageId: string): string {
  return `chat-msg-menu-${messageId}`;
}

/**
 * `conversationKey` scopes everything to one conversation: the page above
 * outlives a switch, and a message of the last one must not stay selected, or
 * keep a menu, in the next.
 */
export function useReactionMenu(conversationKey = ""): ReactionMenuState {
  const [openPickerMessageId, setOpenPickerMessageId] = useState<string | null>(null);
  const [hoveredMessageId, setHoveredMessageId] = useState<string | null>(null);
  const [touchMessageId, setTouchMessageId] = useState<string | null>(null);
  const [selectedMessageId, setSelectedMessageId] = useState<string | null>(null);
  const [scope, setScope] = useState(conversationKey);
  const hoverCloseTimerRef = useRef<number | null>(null);
  // React's "adjust state when a prop changes": reset during render, once.
  if (scope !== conversationKey) {
    setScope(conversationKey);
    setOpenPickerMessageId(null);
    setHoveredMessageId(null);
    setTouchMessageId(null);
    setSelectedMessageId(null);
  }

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
      // So does another message's touch menu (CQR R6 of issue #1238): a finger
      // hovers nothing, and WebKit's mouseover on whatever row a closing picker
      // uncovers took the menu and focus from the reader. That menu ends on its
      // own terms — a press outside, Escape, an action.
      if (touchMessageId && touchMessageId !== messageId) return;
      clearCloseTimer();
      if (visible) {
        setHoveredMessageId(messageId);
        return;
      }
      hoverCloseTimerRef.current = window.setTimeout(() => {
        setHoveredMessageId((current) => ownOrKeep(current, messageId));
        setTouchMessageId((current) => ownOrKeep(current, messageId));
        hoverCloseTimerRef.current = null;
      }, reactionMenuLeaveDelayMs);
    },
    [clearCloseTimer, openPickerMessageId, touchMessageId],
  );

  const onReactionMenuLongPress = useCallback(
    (messageId: string) => {
      clearCloseTimer();
      setHoveredMessageId(messageId);
      setTouchMessageId(messageId);
      // The message a long press acted on is the selected one (issue #1238).
      setSelectedMessageId(messageId);
      // One contextual surface at a time: another message's picker closes.
      setOpenPickerMessageId((current) => (current === messageId ? current : null));
    },
    [clearCloseTimer],
  );

  const onMessageSelect = useCallback((messageId: string) => {
    setSelectedMessageId(messageId);
  }, []);

  const onReactionMenuRelease = useCallback((messageId: string) => {
    setHoveredMessageId((current) => ownOrKeep(current, messageId));
    setTouchMessageId((current) => ownOrKeep(current, messageId));
    setOpenPickerMessageId((current) => ownOrKeep(current, messageId));
    setSelectedMessageId((current) => ownOrKeep(current, messageId));
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
    selectedMessageId,
    touchSurfaceOpen: touchMessageId !== null || openPickerMessageId !== null,
    onReactionMenuVisibleChange,
    onPickerOpenChange,
    onReactionMenuLongPress,
    onMessageSelect,
    onReactionMenuRelease,
  };
}
