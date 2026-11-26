/**
 * The conversation's selection mode on touch (CQR R3 of issue #1238): while a
 * message is selected the header trades its controls for that message's — a
 * way back and its actions — and this hook decides when the mode ends.
 *
 * It ends on "Cancelar seleção", on Escape once no menu or picker is open (the
 * first Escape is theirs), and when focus goes somewhere that is neither the
 * message, its picker nor the mode's own controls. The message ending it —
 * removed, scrolled out of the timeline, its row unmounted — is the row's to
 * report (see MessageBubble). Listeners exist only while something is
 * selected: nothing here runs for a conversation at rest.
 */

import { useCallback, useEffect, useRef } from "react";

import {
  anchorIsVisible,
  intersectBounds,
  visibleBounds,
  visualViewportBounds,
} from "../../emoji/useAnchoredPicker";
import { reactionMenuId, type ReactionMenuState } from "./useReactionMenu";

/** What the header's contextual mode draws and does. */
export interface SelectionHeaderState {
  /** The id of the selected message's menu, which the actions button controls. */
  menuId: string;
  /** The selected message's touch menu, or its picker, is open. */
  menuOpen: boolean;
  onToggleMenu: (button: HTMLButtonElement) => void;
  onCancel: () => void;
}

/** The class of the header in selection mode, which owns focus like the message. */
export const selectionHeaderClass = "chat-msg-area__header--selection";

function messageRow(messageId: string): HTMLElement | undefined {
  return Array.from(document.querySelectorAll<HTMLElement>(".chat-msg-area__msg")).find(
    (row) => row.dataset.messageId === messageId,
  );
}

/**
 * The touch menu's own verdict (MessageToolbar's placement): its anchor, the
 * bubble, outside the list's visible band within the visual viewport — where
 * the menu would close as soon as it opened. The selected row's own watch reads
 * it too (MessageBubble), to reconcile a reading taken mid-scroll.
 */
export function anchorOutOfSight(row: HTMLElement): boolean {
  const bubble = row.querySelector(".chat-msg-area__msg-bubble") ?? row;
  const band = intersectBounds(visibleBounds(bubble), visualViewportBounds());
  return !anchorIsVisible(bubble.getBoundingClientRect(), band);
}

/** Whether focus at `target` still belongs to the selection of `messageId`. */
function staysSelected(target: EventTarget | null, messageId: string): boolean {
  if (!(target instanceof Element)) return false;
  if (target.closest(`.${selectionHeaderClass}, .chat-emoji-tone`)) return true;
  const owner = target.closest("[data-message-id], [data-reaction-picker-for]");
  return (
    owner?.getAttribute("data-message-id") === messageId ||
    owner?.getAttribute("data-reaction-picker-for") === messageId
  );
}

export function useSelectionMode(menu: ReactionMenuState): SelectionHeaderState | undefined {
  const { selectedMessageId: selected, onReactionMenuRelease: release } = menu;
  const surfaceOpenRef = useRef(menu.touchSurfaceOpen);
  useEffect(() => {
    surfaceOpenRef.current = menu.touchSurfaceOpen;
  });

  // Focus goes back to the message, so the reader resumes where they were.
  // Focusing it first and releasing after, in the same batch, leaves nothing
  // the focus may reveal or reselect open.
  const cancel = useCallback(() => {
    if (!selected) return;
    const row = messageRow(selected);
    if (row && !row.contains(document.activeElement)) row.focus({ preventScroll: true });
    release(selected);
  }, [release, selected]);

  useEffect(() => {
    if (!selected) return;
    // The toolbar's own Escape listener closes an open menu first; its state
    // has not changed yet when this one runs, so that Escape stays the menu's.
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== "Escape" || event.defaultPrevented || surfaceOpenRef.current) return;
      cancel();
    };
    const onFocusIn = (event: FocusEvent) => {
      if (!staysSelected(event.target, selected)) release(selected);
    };
    document.addEventListener("keydown", onKeyDown);
    document.addEventListener("focusin", onFocusIn);
    return () => {
      document.removeEventListener("keydown", onKeyDown);
      document.removeEventListener("focusin", onFocusIn);
    };
  }, [cancel, release, selected]);

  const { touchMessageId, openPickerMessageId, hoveredMessageId } = menu;
  const { onReactionMenuLongPress, onPickerOpenChange, onReactionMenuVisibleChange } = menu;
  const menuOpen =
    !!selected &&
    touchMessageId === selected &&
    (hoveredMessageId === selected || openPickerMessageId === selected);
  const onToggleMenu = useCallback(
    (button: HTMLButtonElement) => {
      if (!selected) return;
      // WebKit does not focus a pressed button; the menu takes the focused
      // element for its opener, and Escape hands focus back to it.
      button.focus({ preventScroll: true });
      if (!menuOpen) {
        // The menu belongs beside its message, and closes when the message is
        // out of view: zoomed in and panned to the header, it would close at
        // once. Its bubble partly in view, nothing moves — the timeline stays put.
        const row = messageRow(selected);
        if (row && anchorOutOfSight(row)) {
          row.scrollIntoView({ block: "nearest", inline: "nearest" });
        }
        return onReactionMenuLongPress(selected);
      }
      onPickerOpenChange(selected, false);
      onReactionMenuVisibleChange(selected, false);
    },
    [menuOpen, onPickerOpenChange, onReactionMenuLongPress, onReactionMenuVisibleChange, selected],
  );

  if (!selected) return undefined;
  return { menuId: reactionMenuId(selected), menuOpen, onToggleMenu, onCancel: cancel };
}
