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
 * The popup is rendered through a portal into document.body, with fixed
 * coordinates measured from the trigger. It has to be: the sidebar's nav is an
 * `overflow-y: auto` scrollport, and an absolutely positioned descendant of it
 * is clipped at its edge — so a menu on the last visible row was cut off no
 * matter how the flip was computed. A portal escapes the clipping ancestor
 * entirely, which is why the position is fixed rather than absolute.
 *
 * Keyboard: Tab reaches the trigger, Enter/Space open it, ArrowUp/ArrowDown move
 * between items, Home/End jump, Escape closes, and focus returns to the trigger
 * on every close. The popup is a real `role="menu"` with `role="menuitem"`
 * children and roving focus — not a half-implemented one.
 */

import { Fragment, useCallback, useEffect, useId, useRef } from "react";
import { createPortal } from "react-dom";

import { ConversationActionGlyph } from "./ConversationActionIcons";
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

/** Viewport coordinates for the portalled popup. */
interface MenuPosition {
  top: number;
  left: number;
}

/**
 * The menu's width and the gap it keeps from the trigger and the viewport edges.
 * They live here rather than in CSS because the position is computed in JS, and
 * two sources for one number is how they drift apart.
 */
const menuWidth = 224;
const menuGap = 4;
const viewportMargin = 8;
/** A menu is a handful of rows; this is item height plus the popup's padding. */
const estimatedItemHeight = 36;
const menuPadding = 16;

/**
 * Places the popup against the trigger, in viewport coordinates.
 *
 * Vertically it opens below unless that would run past the bottom edge and there
 * is more room above — the same flip as before, but now measured against the
 * viewport, which is what the fixed-position portal is actually laid out in.
 *
 * Horizontally it is right-aligned with the trigger, then clamped so it cannot
 * leave the screen on either side. A narrow window is the case that matters: the
 * sidebar is near the left edge, so an unclamped right-aligned menu would hang
 * off it.
 */
function menuPosition(trigger: DOMRect, itemCount: number): MenuPosition {
  const height = itemCount * estimatedItemHeight + menuPadding;
  const opensBelow =
    trigger.bottom + menuGap + height <= window.innerHeight || trigger.top < height;
  const top = opensBelow ? trigger.bottom + menuGap : trigger.top - menuGap - height;
  const maxLeft = window.innerWidth - menuWidth - viewportMargin;
  const left = Math.min(
    Math.max(viewportMargin, trigger.right - menuWidth),
    Math.max(viewportMargin, maxLeft),
  );
  return { top: Math.max(viewportMargin, top), left };
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
  const restoreFocusRef = useRef(false);

  const close = useCallback(
    (restoreFocus: boolean) => {
      restoreFocusRef.current = restoreFocus;
      onOpenChange(false);
    },
    [onOpenChange],
  );

  // Placed before the first paint that shows the menu, so it never appears in the
  // wrong spot and jumps. Written straight to the node rather than held in state:
  // the position is not something the component renders differently for, it is
  // where the popup physically is, and a state round-trip would only add a frame.
  //
  // The height estimate is deliberately crude — a menu is at most a handful of
  // rows — because the real height is not known until it renders, and a second
  // measuring pass would cost exactly the jump this avoids.
  const applyPosition = useCallback(() => {
    const node = menuRef.current;
    const trigger = triggerRef.current?.getBoundingClientRect();
    if (!node || !trigger) return;
    const { top, left } = menuPosition(trigger, actions.length);
    node.style.top = `${top}px`;
    node.style.left = `${left}px`;
  }, [actions.length]);

  // A callback ref, so the popup is positioned in the same commit that mounts it.
  const attachMenu = useCallback(
    (node: HTMLDivElement | null) => {
      menuRef.current = node;
      if (node) applyPosition();
    },
    [applyPosition],
  );

  // Only an open menu listens, and only while it is open. The listeners are on
  // capture so a scroll inside the sidebar's own scrollport is seen too — that
  // container scrolls, not the window — and the menu follows its trigger instead
  // of being left behind.
  useEffect(() => {
    if (!open) return;
    window.addEventListener("scroll", applyPosition, true);
    window.addEventListener("resize", applyPosition);
    return () => {
      window.removeEventListener("scroll", applyPosition, true);
      window.removeEventListener("resize", applyPosition);
    };
  }, [open, applyPosition]);

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
    // Tab must leave the menu — it is not a focus trap. Since the popup is
    // portalled to <body> it sits last in the document, so letting the browser
    // move focus from *there* would drop the user at the end of the page rather
    // than back near the row they were on. Tab closes the menu and puts focus on
    // the trigger instead: the menu is gone, and the next Tab continues from the
    // sidebar exactly as it would have if the menu had never been opened.
    if (event.key === "Tab") {
      event.preventDefault();
      close(true);
      return;
    }
    const handler = handlers[event.key];
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
      {open &&
        createPortal(
          <div
            ref={attachMenu}
            id={menuId}
            role="menu"
            aria-label={triggerLabel}
            className="chat-sidebar__actions-menu"
            onKeyDown={handleMenuKeyDown}
            // The portal puts the popup outside the row in the DOM, so a click
            // inside it no longer bubbles through the row at all. This stays for
            // the same reason the trigger's does: nothing in this menu may reach
            // a handler that could select a conversation.
            onMouseDown={(event) => event.stopPropagation()}
          >
            {actions.map((action, index) => (
              <Fragment key={action.id}>
                {/* A separator wherever the group changes, so the destructive
                  action is visibly apart from the ordinary ones. `role="none"`
                  keeps it out of the menu's own item sequence. */}
                {index > 0 && actions[index - 1]?.group !== action.group && (
                  <span className="chat-sidebar__actions-separator" role="none" />
                )}
                <button
                  type="button"
                  role="menuitem"
                  tabIndex={-1}
                  className={`chat-sidebar__actions-item${
                    action.destructive ? " chat-sidebar__actions-item--destructive" : ""
                  }`}
                  onClick={(event) => {
                    event.preventDefault();
                    event.stopPropagation();
                    runAction(action.id);
                  }}
                >
                  <ConversationActionGlyph
                    icon={action.icon}
                    className="chat-sidebar__actions-icon"
                  />
                  <span className="chat-sidebar__actions-label">{action.label}</span>
                </button>
              </Fragment>
            ))}
          </div>,
          document.body,
        )}
    </div>
  );
}
