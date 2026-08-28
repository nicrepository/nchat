/**
 * SidebarUserMenu — the sidebar footer's account menu (issue #672 §9).
 *
 * Replaces a gear icon that was labelled "Configurações" but actually
 * navigated to /admin/users. Its real job is a three-item menu: the profile
 * link that belongs under a settings affordance, the admin link that was
 * mislabeled, and this app's first logout control.
 *
 * Mirrors ConversationActionsMenu's trigger+portal+role="menu"+keyboard-nav
 * mechanics, simplified for one static trigger with three fixed items — no
 * itemCount-based flip math — and opening above by default, since the footer
 * sits at the bottom of the sidebar.
 */

import { useCallback, useEffect, useId, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { Link } from "react-router";

import { logout } from "../auth/authApi";
import { clearTokens } from "../lib/authSession";

function IconSettings() {
  return (
    <svg
      viewBox="0 0 24 24"
      className="chat-sidebar__more-icon"
      aria-hidden="true"
      focusable="false"
    >
      <circle cx="12" cy="12" r="3" />
      <path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1Z" />
    </svg>
  );
}

const menuId = "chat-sidebar-user-menu";
const menuWidth = 200;
const menuGap = 6;
const viewportMargin = 8;

export default function SidebarUserMenu() {
  const [open, setOpen] = useState(false);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);
  const restoreFocusRef = useRef(false);
  const domId = useId();

  const close = useCallback((restoreFocus: boolean) => {
    restoreFocusRef.current = restoreFocus;
    setOpen(false);
  }, []);

  const applyPosition = useCallback(() => {
    const node = menuRef.current;
    const trigger = triggerRef.current?.getBoundingClientRect();
    if (!node || !trigger) return;
    const height = node.offsetHeight || 120;
    const opensAbove = trigger.top - menuGap - height >= viewportMargin;
    node.style.top = opensAbove
      ? `${trigger.top - menuGap - height}px`
      : `${trigger.bottom + menuGap}px`;
    const maxLeft = window.innerWidth - menuWidth - viewportMargin;
    node.style.left = `${Math.min(Math.max(viewportMargin, trigger.left), Math.max(viewportMargin, maxLeft))}px`;
  }, []);

  const attachMenu = useCallback(
    (node: HTMLDivElement | null) => {
      menuRef.current = node;
      if (node) applyPosition();
    },
    [applyPosition],
  );

  useEffect(() => {
    if (open) {
      menuRef.current
        ?.querySelector<HTMLAnchorElement | HTMLButtonElement>("[role='menuitem']")
        ?.focus();
      return;
    }
    if (restoreFocusRef.current) {
      restoreFocusRef.current = false;
      triggerRef.current?.focus();
    }
  }, [open]);

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
      menuRef.current?.querySelectorAll<HTMLElement>("[role='menuitem']") ?? [],
    );
    if (items.length === 0) return;
    const current = items.findIndex((item) => item === document.activeElement);
    const next =
      ((((current < 0 ? -1 : current) + delta) % items.length) + items.length) % items.length;
    items[next]?.focus();
  }

  function handleMenuKeyDown(event: React.KeyboardEvent<HTMLDivElement>) {
    if (event.key === "Escape") {
      event.preventDefault();
      close(true);
      return;
    }
    if (event.key === "Tab") {
      event.preventDefault();
      close(true);
      return;
    }
    if (event.key === "ArrowDown") {
      event.preventDefault();
      moveFocus(1);
    } else if (event.key === "ArrowUp") {
      event.preventDefault();
      moveFocus(-1);
    }
  }

  async function handleLogout() {
    close(true);
    try {
      await logout();
    } catch {
      // Best-effort server-side revoke. The client is the source of truth for
      // "am I logged out" — clearTokens() below fires unconditionally, and
      // RequireAuth's onAuthChange subscription redirects to /login from that.
    } finally {
      clearTokens();
    }
  }

  return (
    <div className="chat-sidebar__actions">
      <button
        ref={triggerRef}
        type="button"
        className="chat-sidebar__actions-trigger chat-sidebar__user-settings"
        aria-label="Menu da conta"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? `${menuId}-${domId}` : undefined}
        onClick={() => {
          restoreFocusRef.current = false;
          setOpen((current) => !current);
        }}
      >
        <IconSettings />
      </button>
      {open &&
        createPortal(
          <div
            ref={attachMenu}
            id={`${menuId}-${domId}`}
            role="menu"
            aria-label="Menu da conta"
            className="chat-sidebar__actions-menu"
            onKeyDown={handleMenuKeyDown}
          >
            <Link
              to="/profile"
              role="menuitem"
              tabIndex={-1}
              className="chat-sidebar__actions-item"
              onClick={() => close(false)}
            >
              <span className="chat-sidebar__actions-label">Meu perfil</span>
            </Link>
            <Link
              to="/admin/users"
              role="menuitem"
              tabIndex={-1}
              className="chat-sidebar__actions-item"
              onClick={() => close(false)}
            >
              <span className="chat-sidebar__actions-label">Administração</span>
            </Link>
            <span className="chat-sidebar__actions-separator" role="none" />
            <button
              type="button"
              role="menuitem"
              tabIndex={-1}
              className="chat-sidebar__actions-item chat-sidebar__actions-item--destructive"
              onClick={() => void handleLogout()}
            >
              <span className="chat-sidebar__actions-label">Sair</span>
            </button>
          </div>,
          document.body,
        )}
    </div>
  );
}
