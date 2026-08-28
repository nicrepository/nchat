import { useCallback, useLayoutEffect, useRef, useState, type KeyboardEvent } from "react";
import { Outlet, useLocation } from "react-router";

import "./AppShell.css";
import ChatSidebar, { chatNavigationId } from "./ChatSidebar";
import { useNavDrawer } from "./useNavDrawer";
import SidebarDetailsPanel, { type SidebarDetailsTarget } from "./SidebarDetailsPanel";
import type { Channel, DMConversation } from "./chatTypes";
import { useChatSidebar } from "./useChatSidebar";

/**
 * Resolves a row menu's target to the details panel's own vocabulary.
 *
 * The sidebar speaks the API's two kinds (channel / dm); the panel speaks three,
 * because a group and a 1:1 render differently. The discriminant is `dm.type`,
 * the server's own value, and never the name or the participant count.
 *
 * Returns null for a conversation the sidebar does not hold — a refetch may have
 * removed it between the click and this call — so nothing opens over nothing.
 */
function resolveDetailsTarget(
  kind: "channel" | "dm",
  targetId: string,
  dms: DMConversation[],
): SidebarDetailsTarget | null {
  if (kind === "channel") return { kind: "channel", id: targetId };
  const dm = dms.find((item) => item.id === targetId);
  if (!dm) return null;
  return { kind: dm.type === "group" ? "group" : "direct", id: targetId };
}

/**
 * Whether the conversation a details panel is open for is still one this user
 * has (issue #527, code review).
 *
 * Derived from the canonical sidebar collection rather than from the route: a
 * self-leave removes the row without changing the pathname, and a panel is only
 * valid while its subject is.
 */
function detailsTargetExists(
  target: SidebarDetailsTarget,
  ready: { channels: Channel[]; dms: DMConversation[] },
): boolean {
  const collection = target.kind === "channel" ? ready.channels : ready.dms;
  return collection.some((item) => item.id === target.id);
}

/**
 * Hands focus back to whatever opened the sidebar's details panel
 * (issue #467, code quality review).
 *
 * The opener is a sidebar row's "…" trigger, and whether it can still take
 * focus depends on the composition the panel was closed in:
 *
 *  - a column sidebar: the row is right there and takes it;
 *  - a drawer: the row is still mounted and still connected, but the drawer was
 *    closed when the panel opened, and a hidden element cannot hold focus.
 *
 * Rather than predicting that from the viewport — which would duplicate the
 * responsive policy in JavaScript — this asks the DOM: focus the opener, then
 * see whether it actually took. The fallback is the navigation toggle, the one
 * control that is on screen in every drawer state, so focus never lands on
 * `<body>`. `focus()` on a detached or hidden element is a no-op, never a
 * throw, so neither step needs a guard of its own.
 */
function restoreDetailsFocus(opener: HTMLElement | null, fallback: HTMLElement | null) {
  if (opener?.isConnected) opener.focus();
  if (opener && document.activeElement === opener) return;
  fallback?.focus();
}

/**
 * A boolean as a data attribute: present and `"true"`, or absent entirely.
 *
 * Absent rather than `"false"` because that is what the CSS reads — a state
 * selector matches on the attribute existing — and because the two states of a
 * layout flag are "this mode" and "not this mode", never a third value.
 */
function dataFlag(on: boolean): "true" | undefined {
  return on ? "true" : undefined;
}

/**
 * The drawer's disclosure control (issue #467).
 *
 * A component of its own rather than three ternaries inside the shell: the
 * label, the icon and the state it reports are one decision about one control,
 * and the shell has enough to hold already.
 *
 * Only rendered as a bar below the drawer breakpoint; above it the sidebar is a
 * permanent column and this is display:none. It sits in its own grid row, above
 * the drawer rather than under it, so the control that opened the drawer stays
 * visible and focused while it is open — the disclosure pattern, with the panel
 * it controls next in DOM order. Every chat route is inside this shell, so one
 * control serves all of them instead of each surface growing its own.
 */
function ChatNavBar({
  open,
  onToggle,
  toggleRef,
}: Readonly<{
  open: boolean;
  onToggle: () => void;
  toggleRef: React.RefObject<HTMLButtonElement | null>;
}>) {
  return (
    <div className="chat-app__nav-bar">
      <button
        ref={toggleRef}
        type="button"
        className="chat-app__nav-toggle"
        aria-controls={chatNavigationId}
        aria-expanded={open}
        onClick={onToggle}
        data-testid="chat-nav-toggle"
      >
        <span className="material-symbols-outlined" aria-hidden="true">
          {open ? "close" : "menu"}
        </span>
        {open ? "Fechar conversas" : "Conversas"}
      </button>
    </div>
  );
}

/**
 * Keeps the document itself unscrollable for as long as the chat is on screen
 * (issue #467 follow-up).
 *
 * The shell is exactly one viewport tall and clips its own content, so on a
 * correct layout the document has nothing to scroll and this changes nothing.
 * It is here because "nothing to scroll" is a property of every descendant at
 * once: an out-of-flow box whose containing block is the initial one — the
 * `.sr-only` badge that produced this bug — is not clipped by any of the
 * shell's `overflow` rules, and grows the document's scroll area from wherever
 * it happens to sit. One statement about the surface costs less than trusting
 * every future descendant to stay inside a scrollport.
 *
 * Scoped to the shell's lifetime rather than declared on `html` in a
 * stylesheet: login, profile and admin are ordinary documents that scroll.
 * `useLayoutEffect` so the lock and the reset land in the same frame as the
 * first paint, and the reset is instant — a smooth scroll would animate a jump
 * the user never asked for. Both run once: this is about the surface being
 * mounted, not about anything that changes while it is.
 */
function useRootScrollLock() {
  useLayoutEffect(() => {
    const { documentElement, body } = document;
    // `overflow: hidden` freezes a scroll position rather than discarding it,
    // so arriving from a scrolled route has to be normalised, not just locked.
    window.scrollTo(0, 0);
    documentElement.classList.add(ROOT_LOCK_CLASS);
    body.classList.add(ROOT_LOCK_CLASS);
    return () => {
      documentElement.classList.remove(ROOT_LOCK_CLASS);
      body.classList.remove(ROOT_LOCK_CLASS);
    };
  }, []);
}

/** Named once, because AppShell.css and ChatShell.test.tsx both spell it. */
export const ROOT_LOCK_CLASS = "chat-root-locked";

export type AppShellOutletContext = ReturnType<typeof useChatSidebar>;

/** "/profile" or "/profile/..." gets the settings label; everything else (today, only "/chat/...") gets the chat one. */
function mainAriaLabel(pathname: string): string {
  return pathname === "/profile" || pathname.startsWith("/profile/")
    ? "Configurações da conta"
    : "Área de mensagens";
}

export default function AppShell() {
  useRootScrollLock();
  const sidebar = useChatSidebar();
  const {
    state,
    retry,
    setPinned,
    markRead,
    renameChannel,
    renameGroup,
    setMuted,
    leaveConversation,
  } = sidebar;
  // Details opened from a row menu, for that row's target (issue #527). Held
  // here rather than in ChatMessageArea because the target may be a
  // conversation other than the open one, and opening it must not navigate.
  const { pathname } = useLocation();
  // The route is part of the panel's identity, not something an effect syncs to
  // it: navigating away closes the panel because the value stored alongside it
  // stops matching, with no extra render pass. The panel is a peek at one
  // conversation, not a second persistent surface to keep in step with a route.
  const [sidebarDetails, setSidebarDetails] = useState<
    (SidebarDetailsTarget & { pathname: string }) | null
  >(null);
  const dms = state.status === "ready" ? state.dms : [];
  const channels = state.status === "ready" ? state.channels : [];
  // Two conditions, and both are about the target still being real. The route is
  // part of the panel's identity, so navigating away closes it with no extra
  // render pass; and the conversation must still be in the canonical list, so
  // leaving the conversation the panel is showing closes it too — the row is
  // gone, and a panel over a conversation this user can no longer see would keep
  // polling for details it may not have.
  const openDetailsTarget =
    sidebarDetails?.pathname === pathname && detailsTargetExists(sidebarDetails, { channels, dms })
      ? sidebarDetails
      : null;

  // The navigation is a column on wide viewports and a drawer below them
  // (issue #467). Only the open/closed boolean lives here; which of the two it
  // is at a given width is decided in AppShell.css.
  const {
    open: navOpen,
    modal: navModal,
    toggle: toggleNav,
    close: setNavClosed,
  } = useNavDrawer(pathname);
  const navToggleRef = useRef<HTMLButtonElement>(null);
  // Closing always hands focus back to the control that opened the drawer: it
  // is the one element that is on screen in every drawer state, so a keyboard
  // user never lands nowhere.
  const closeNav = useCallback(() => {
    setNavClosed();
    navToggleRef.current?.focus();
  }, [setNavClosed]);
  // The row control the panel was opened from, kept so closing can hand focus
  // back to it. A ref rather than state: nothing renders from it, and it must
  // survive the render that closes the panel. Captured here rather than read
  // from `document.activeElement`, which by this point is still the menu item
  // that is about to unmount — the menu restores its own trigger in an effect,
  // one commit later.
  const detailsOpenerRef = useRef<HTMLElement | null>(null);
  const openSidebarDetails = useCallback(
    (kind: "channel" | "dm", targetId: string, opener: HTMLElement | null) => {
      const resolved = resolveDetailsTarget(kind, targetId, dms);
      if (!resolved) return;
      detailsOpenerRef.current = opener;
      setSidebarDetails({ ...resolved, pathname });
      // The row menu that asked for this is inside the drawer, and the panel
      // covers the same area the drawer does. Closing it is what keeps the two
      // from being stacked over each other on a phone.
      closeNav();
    },
    [dms, pathname, closeNav],
  );
  // The panel's single close path — the close button and Escape both arrive
  // here through the same `onClose` — so focus is restored once, from one place,
  // whichever gesture closed it.
  const closeSidebarDetails = useCallback(() => {
    setSidebarDetails(null);
    restoreDetailsFocus(detailsOpenerRef.current, navToggleRef.current);
    detailsOpenerRef.current = null;
  }, []);

  function handleShellKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    if (!navModal || event.key !== "Escape") return;
    // React bubbles a portal's events to its React parent rather than its DOM
    // one, so the Escape that dismisses a dialog opened from inside the drawer —
    // "Nova conversa", renaming, leaving — would otherwise close the drawer
    // underneath it as well. Only a keystroke that really happened inside the
    // shell closes the drawer.
    if (!event.currentTarget.contains(event.target as Node)) return;
    closeNav();
  }

  return (
    <div
      className="chat-app"
      data-testid="chat-shell"
      data-nav-open={dataFlag(navOpen)}
      data-details-open={dataFlag(openDetailsTarget !== null)}
      onKeyDown={handleShellKeyDown}
    >
      {/* The toggle keeps focus through open and close alike, so closing from
          here needs no focus restoration of its own — unlike the backdrop, the
          Escape key and the row menu below, which all hand it back. */}
      <ChatNavBar open={navOpen} onToggle={toggleNav} toggleRef={navToggleRef} />
      <ChatSidebar
        state={state}
        retry={retry}
        setPinned={setPinned}
        markRead={markRead}
        renameChannel={renameChannel}
        renameGroup={renameGroup}
        setMuted={setMuted}
        leaveConversation={leaveConversation}
        onOpenDetails={openSidebarDetails}
      />
      {/* Pointer half of "the background is not interactive while the drawer is
          open"; `inert` below is the keyboard and assistive-technology half.
          A real named button rather than a decorative overlay: it sits between
          the drawer and the conversation in tab order, so tabbing past the end
          of the drawer reaches an explicit way out instead of nothing. */}
      {navModal && (
        <button
          type="button"
          className="chat-app__nav-backdrop"
          aria-label="Fechar a navegação"
          onClick={closeNav}
          data-testid="chat-nav-backdrop"
        />
      )}
      <main className="chat-app__main" aria-label={mainAriaLabel(pathname)} inert={navModal}>
        <Outlet context={sidebar} />
      </main>
      <SidebarDetailsPanel
        target={openDetailsTarget}
        currentUserId={state.status === "ready" ? state.currentUserId : ""}
        onClose={closeSidebarDetails}
      />
    </div>
  );
}
