/**
 * ConversationActionsMenu — the "…" menu on a sidebar row (issue #527).
 *
 * One component, rendered once per row for its trigger, but the popup itself is
 * mounted only while that row's menu is the open one: the sidebar keeps a single
 * `{kind, id}` identity of what is open, so forty rows cost forty buttons and at
 * most one list. Nothing here registers a per-row document listener — the one
 * outside-click listener belongs to the open popup and dies with it.
 *
 * The trigger is a real <button> outside the row's `role="option"` element, so
 * activating it can never select the conversation. It still stops propagation:
 * the row is a flex container whose click would otherwise be a plausible future
 * hazard, and a menu that navigates is the defect this issue exists to prevent.
 *
 * Keyboard: Tab reaches the trigger, Enter/Space open it, ArrowUp/ArrowDown move
 * between items, Home/End jump, Escape closes, and focus returns to the trigger
 * on every close. The popup is a real `role="menu"` with `role="menuitem"`
 * children and roving focus — not a half-implemented one.
 */

import { useCallback, useEffect, useId, useRef } from "react";

import type { ConversationAction, ConversationActionId } from "./conversationActions";

interface ConversationActionsMenuProps {
  /** Accessible name of the trigger; never just "…". */
  triggerLabel: string;
  actions: ConversationAction[];
  open: boolean;
  /** Asks the sidebar to make this row's menu the open one, or to close it. */
  onOpenChange: (open: boolean) => void;
  onAction: (id: ConversationActionId) => void;
}

function IconMore() {
  return (
    <svg
      viewBox="0 0 24 24"
      className="chat-sidebar__more-icon"
      aria-hidden="true"
      focusable="false"
    >
      <circle cx="5" cy="12" r="1.75" fill="currentColor" />
      <circle cx="12" cy="12" r="1.75" fill="currentColor" />
      <circle cx="19" cy="12" r="1.75" fill="currentColor" />
    </svg>
  );
}

/** Wraps around both ends, so ArrowDown on the last item lands on the first. */
function nextIndex(current: number, delta: number, length: number): number {
  return (current + delta + length) % length;
}

export default function ConversationActionsMenu({
  triggerLabel,
  actions,
  open,
  onOpenChange,
  onAction,
}: ConversationActionsMenuProps) {
  const menuId = useId();
  const triggerRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);
  // Set while a close is the consequence of a user gesture on this menu, so
  // focus goes back to the trigger — and left alone when the menu closes because
  // the sidebar reordered, unmounted or opened another row's menu, where forcing
  // focus would steal it from wherever the user has since moved.
  const restoreFocusRef = useRef(false);

  const close = useCallback(
    (restoreFocus: boolean) => {
      restoreFocusRef.current = restoreFocus;
      onOpenChange(false);
    },
    [onOpenChange],
  );

  // Focus lands on the first item when the menu opens, and returns to the
  // trigger when a gesture closed it.
  useEffect(() => {
    if (open) {
      menuRef.current?.querySelector<HTMLButtonElement>("[role='menuitem']")?.focus();
      return;
    }
    if (restoreFocusRef.current) {
      restoreFocusRef.current = false;
      triggerRef.current?.focus();
    }
  }, [open]);

  // One listener for the one open menu. `mousedown` rather than `click` matches
  // the dialogs already in this app, and the capture-phase `focusin` closes the
  // menu when Tab moves out of it — so the tab order is never trapped.
  useEffect(() => {
    if (!open) return;
    const closeIfOutside = (event: Event) => {
      const target = event.target as Node | null;
      if (menuRef.current?.contains(target) || triggerRef.current?.contains(target)) return;
      close(false);
    };
    document.addEventListener("mousedown", closeIfOutside);
    document.addEventListener("focusin", closeIfOutside);
    return () => {
      document.removeEventListener("mousedown", closeIfOutside);
      document.removeEventListener("focusin", closeIfOutside);
    };
  }, [open, close]);

  function moveFocus(delta: number) {
    const items = Array.from(
      menuRef.current?.querySelectorAll<HTMLButtonElement>("[role='menuitem']") ?? [],
    );
    if (items.length === 0) return;
    const current = items.findIndex((item) => item === document.activeElement);
    items[nextIndex(current < 0 ? -1 : current, delta, items.length)]?.focus();
  }

  function focusEdge(last: boolean) {
    const items = Array.from(
      menuRef.current?.querySelectorAll<HTMLButtonElement>("[role='menuitem']") ?? [],
    );
    (last ? items[items.length - 1] : items[0])?.focus();
  }

  function handleMenuKeyDown(event: React.KeyboardEvent<HTMLDivElement>) {
    const handlers: Record<string, () => void> = {
      Escape: () => close(true),
      ArrowDown: () => moveFocus(1),
      ArrowUp: () => moveFocus(-1),
      Home: () => focusEdge(false),
      End: () => focusEdge(true),
    };
    const handler = handlers[event.key];
    // Tab is deliberately absent: the items are tabIndex={-1}, so Tab moves
    // focus to the next control after the menu, and the focusin listener above
    // closes it. Handling Tab here would unmount the menu before the browser
    // performed the move, and focus would fall to <body> instead — the tab order
    // must never be trapped, and it must never be dropped either.
    if (!handler) return;
    event.preventDefault();
    event.stopPropagation();
    handler();
  }

  function runAction(id: ConversationActionId) {
    close(true);
    onAction(id);
  }

  return (
    <div className="chat-sidebar__actions">
      <button
        ref={triggerRef}
        type="button"
        className="chat-sidebar__actions-trigger"
        aria-label={triggerLabel}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? menuId : undefined}
        onClick={(event) => {
          // The row is not this button's ancestor, but a stray handler on the
          // flex container must never turn "open the menu" into "open the
          // conversation".
          event.preventDefault();
          event.stopPropagation();
          // Focus is already on the trigger, so a toggle never has to restore it.
          restoreFocusRef.current = false;
          onOpenChange(!open);
        }}
      >
        <IconMore />
      </button>
      {open && (
        <div
          ref={menuRef}
          id={menuId}
          role="menu"
          aria-label={triggerLabel}
          className="chat-sidebar__actions-menu"
          onKeyDown={handleMenuKeyDown}
        >
          {actions.map((action) => (
            <button
              key={action.id}
              type="button"
              role="menuitem"
              tabIndex={-1}
              className="chat-sidebar__actions-item"
              onClick={(event) => {
                event.preventDefault();
                event.stopPropagation();
                runAction(action.id);
              }}
            >
              {action.label}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
