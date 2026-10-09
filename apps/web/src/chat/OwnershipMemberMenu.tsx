import { useCallback, useEffect, useId, useRef, useState, type KeyboardEvent } from "react";
import { createPortal } from "react-dom";
import { participantCopy, type ParticipantMenuAction } from "./ownershipParticipants";

// Same bottom-sheet breakpoint and placement pattern as PresenceStatusMenu.
const sheetQuery = "(max-width: 640px)";

function placePopup(node: HTMLDivElement, trigger: HTMLButtonElement | null) {
  if (!trigger) return;
  if (window.matchMedia?.(sheetQuery).matches) {
    node.style.removeProperty("top");
    node.style.removeProperty("left");
    return;
  }
  const rect = trigger.getBoundingClientRect();
  const margin = 8;
  const gap = 6;
  const below = rect.bottom + gap + node.offsetHeight <= window.innerHeight;
  const top = below ? rect.bottom + gap : rect.top - gap - node.offsetHeight;
  node.style.top = `${Math.max(margin, top)}px`;
  node.style.left = `${Math.max(margin, Math.min(rect.right - node.offsetWidth, window.innerWidth - node.offsetWidth - margin))}px`;
}

function menuItems(node: HTMLDivElement | null): HTMLButtonElement[] {
  return Array.from(node?.querySelectorAll<HTMLButtonElement>("[role='menuitem']") ?? []);
}

function moveFocus(node: HTMLDivElement | null, delta: number) {
  const items = menuItems(node);
  if (items.length === 0) return;
  const current = items.indexOf(document.activeElement as HTMLButtonElement);
  items[(current + delta + items.length) % items.length]?.focus();
}

export default function OwnershipMemberMenu({
  label,
  actions,
  onAction,
}: {
  label: string;
  actions: readonly ParticipantMenuAction[];
  onAction: (id: ParticipantMenuAction["id"], trigger: HTMLButtonElement) => void;
}) {
  const menuId = useId();
  const trigger = useRef<HTMLButtonElement>(null);
  const popup = useRef<HTMLDivElement>(null);
  const [open, setOpen] = useState(false);
  const close = useCallback((returnFocus = true) => {
    setOpen(false);
    if (returnFocus) trigger.current?.focus();
  }, []);
  const attach = useCallback((node: HTMLDivElement | null) => {
    popup.current = node;
    if (!node) return;
    placePopup(node, trigger.current);
    menuItems(node)[0]?.focus({ preventScroll: true });
  }, []);

  useEffect(() => {
    if (!open) return;
    function outside(event: Event) {
      const target = event.target as Node | null;
      if (popup.current?.contains(target) || trigger.current?.contains(target)) return;
      close(false);
    }
    function reposition(event: Event) {
      if (event.target instanceof Node && popup.current?.contains(event.target)) return;
      if (popup.current) placePopup(popup.current, trigger.current);
    }
    document.addEventListener("pointerdown", outside);
    document.addEventListener("focusin", outside);
    window.addEventListener("resize", reposition);
    window.addEventListener("scroll", reposition, true);
    return () => {
      document.removeEventListener("pointerdown", outside);
      document.removeEventListener("focusin", outside);
      window.removeEventListener("resize", reposition);
      window.removeEventListener("scroll", reposition, true);
    };
  }, [open, close]);

  function handleKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    const handlers: Record<string, (() => void) | undefined> = {
      Escape: () => close(),
      Tab: () => close(),
      ArrowDown: () => moveFocus(popup.current, 1),
      ArrowUp: () => moveFocus(popup.current, -1),
      Home: () => menuItems(popup.current)[0]?.focus(),
      End: () => menuItems(popup.current).at(-1)?.focus(),
    };
    const handler = handlers[event.key];
    if (!handler) return;
    event.preventDefault();
    event.stopPropagation();
    handler();
  }

  function runAction(id: ParticipantMenuAction["id"]) {
    close();
    if (trigger.current) onAction(id, trigger.current);
  }

  return (
    <>
      <button
        ref={trigger}
        type="button"
        className="ownership-roster__more"
        aria-label={label}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? menuId : undefined}
        onClick={() => (open ? close() : setOpen(true))}
      >
        <span className="material-symbols-outlined" aria-hidden="true">
          more_vert
        </span>
      </button>
      {open &&
        createPortal(
          <div ref={attach} className="ownership-member-menu chat-theme" onKeyDown={handleKeyDown}>
            <div className="ownership-member-menu__mobile-heading">
              <span>{label}</span>
              <button type="button" aria-label={participantCopy.closeMenu} onClick={() => close()}>
                <span className="material-symbols-outlined" aria-hidden="true">
                  close
                </span>
              </button>
            </div>
            <div role="menu" id={menuId} aria-label={label}>
              {actions.map((action, index) => (
                <button
                  key={action.id}
                  type="button"
                  role="menuitem"
                  tabIndex={-1}
                  className={[
                    action.destructive ? "ownership-member-menu__danger" : "",
                    action.destructive && index > 0 ? "ownership-member-menu__separated" : "",
                  ]
                    .filter(Boolean)
                    .join(" ")}
                  onClick={() => runAction(action.id)}
                >
                  {action.label}
                </button>
              ))}
            </div>
          </div>,
          document.body,
        )}
    </>
  );
}
