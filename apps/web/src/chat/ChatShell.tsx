import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type KeyboardEvent,
} from "react";
import { Outlet, useLocation } from "react-router";

import { useCallSession } from "../calls/CallSessionProvider";
import type { ParticipantMedia } from "./useCallMedia";
import "./ChatShell.css";
import ChatSidebar, { chatNavigationId } from "./ChatSidebar";
import { useNavDrawer } from "./useNavDrawer";
import SidebarDetailsPanel, { type SidebarDetailsTarget } from "./SidebarDetailsPanel";
import type { ResourceCallKind } from "./callApi";
import type { Call, CallType } from "./callState";
import type { ResourceCallTarget } from "./useResourceCallSession";
import type { Channel, DMConversation } from "./chatTypes";
import type { WorkspaceAttachmentLimits } from "./chatApi";
import { useChatSidebar, type SidebarState } from "./useChatSidebar";

/**
 * The sidebar's data, or empty stand-ins while it is still loading.
 *
 * Stated once because five call sites below need the same "ready or nothing"
 * answer, and repeating the discriminant at each of them is how one of them
 * eventually forgets and reads a field off a loading state.
 */
function readySidebar(state: SidebarState): {
  currentUserId: string;
  channels: Channel[];
  dms: DMConversation[];
  attachmentLimits: WorkspaceAttachmentLimits;
} {
  if (state.status !== "ready")
    return {
      currentUserId: "",
      channels: [],
      dms: [],
      attachmentLimits: {
        maxUploadBytes: null,
        maxFiles: 1,
        maxBytes: Number.MAX_SAFE_INTEGER,
      },
    };
  return {
    currentUserId: state.currentUserId,
    channels: state.channels,
    dms: state.dms,
    attachmentLimits: state.attachmentLimits ?? {
      maxUploadBytes: null,
      maxFiles: 1,
      maxBytes: Number.MAX_SAFE_INTEGER,
    },
  };
}

/**
 * Everything ActiveResourceCallBar (issue #642) needs to represent the
 * user's OWN resource-call participation inside the channel/group-DM view
 * that owns it — populated ONLY when CallSessionProvider's own
 * resourcePresentationCall authority is non-null (issue #642 review): a
 * proven call_id match against discovery, resource+media both settled
 * (never connecting/reconnecting/error — the floating window keeps
 * presenting those), and local ownership. `callId`/`startedAt` come
 * straight from that same validated Call, never a second guess. Every
 * other field is a pass-through of state/callbacks CallSessionProvider
 * already computes — never a second source of media/lifecycle truth.
 */
export interface ActiveResourceCallSession {
  callId: string;
  startedAt: string;
  participants: ParticipantMedia[];
  localId: string;
  localName: string;
  localInitials: string;
  localAvatarUrl?: string;
  activeSpeakerId: string | null;
  microphoneEnabled: boolean;
  microphonePending: boolean;
  onToggleMicrophone: () => void;
  onLeave: () => void;
  onOpenFullCall: () => void;
}

export interface ChatOutletContext {
  currentUserId: string;
  channels: Channel[];
  dms: DMConversation[];
  attachmentLimits?: WorkspaceAttachmentLimits;
  startCall?: (targetUserId: string, callType: CallType) => boolean;
  /**
   * Discovery only (issue #622 round 2): whether a channel/group-DM has an
   * active call right now, regardless of whether the current user is in it
   * or busy elsewhere — never gated on participation or #609's join/start
   * guard, so the header can show "Chamada ativa" for a target this tab is
   * not currently able to join.
   */
  getResourceCall?: (kind: ResourceCallKind, id: string) => Call | null;
  /** True only when this exact target is the one the user is already in. */
  isParticipatingIn?: (kind: ResourceCallKind, id: string) => boolean;
  /**
   * Joins (known callId) or starts (no callId) the shared call room for a
   * channel or a group DM. undefined — never omitted from the type, but a
   * runtime undefined value — while a direct call is busy or a different
   * resource call is already active: RF-23/RF-24 share one Room, so a
   * disabled action is exactly "no function to call", the same signal the
   * header used before discovery existed. Always funnels through
   * joinResourceParticipation, never resourceCall.join directly, so an old
   * "left" for whatever this callId's participation used to be can never
   * abort this brand-new attempt before it registers.
   */
  joinResourceCall?: (target: ResourceCallTarget) => void;
  /** Present only while participating in a resource call with local ownership (issue #642/#657). */
  resourceCallSession?: ActiveResourceCallSession;
}

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

/** Named once, because ChatShell.css and ChatShell.test.tsx both spell it. */
export const ROOT_LOCK_CLASS = "chat-root-locked";

export default function ChatShell() {
  useRootScrollLock();
  const {
    state,
    retry,
    setPinned,
    markRead,
    renameChannel,
    renameGroup,
    setMuted,
    leaveConversation,
  } = useChatSidebar();
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
  const ready = readySidebar(state);
  // Two conditions, and both are about the target still being real. The route is
  // part of the panel's identity, so navigating away closes it with no extra
  // render pass; and the conversation must still be in the canonical list, so
  // leaving the conversation the panel is showing closes it too — the row is
  // gone, and a panel over a conversation this user can no longer see would keep
  // polling for details it may not have.
  const openDetailsTarget =
    sidebarDetails?.pathname === pathname && detailsTargetExists(sidebarDetails, ready)
      ? sidebarDetails
      : null;
  const {
    calls,
    resource: resourceCall,
    joinResourceParticipation,
    registerDirectory,
    registerIdentity,
    getResourceCall,
    media,
    expand,
    leaveResourceParticipation,
    localIdentity,
    resourcePresentationCall,
  } = useCallSession();
  useEffect(() => registerIdentity(state.status, retry), [registerIdentity, retry, state.status]);
  useEffect(() => {
    if (state.status === "ready") {
      registerDirectory({
        currentUserId: state.currentUserId,
        channels: state.channels,
        dms: state.dms,
      });
    }
  }, [registerDirectory, state]);

  // RF-23 and RF-24 share one LiveKit Room (`media`): a direct call already
  // ringing/active takes priority, so a resource room can't be joined while
  // one is in progress.
  const directCallBusy =
    calls.call !== null &&
    !["declined", "cancelled", "timed_out", "ended"].includes(calls.call.status);
  const activeResourceTarget = resourceCall.active;
  const isParticipatingIn = useCallback(
    (kind: ResourceCallKind, id: string) =>
      activeResourceTarget?.kind === kind && activeResourceTarget.id === id,
    [activeResourceTarget],
  );
  // The navigation is a column on wide viewports and a drawer below them
  // (issue #467). Only the open/closed boolean lives here; which of the two it
  // is at a given width is decided in ChatShell.css.
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
      const resolved = resolveDetailsTarget(
        kind,
        targetId,
        state.status === "ready" ? state.dms : [],
      );
      if (!resolved) return;
      detailsOpenerRef.current = opener;
      setSidebarDetails({ ...resolved, pathname });
      // The row menu that asked for this is inside the drawer, and the panel
      // covers the same area the drawer does. Closing it is what keeps the two
      // from being stacked over each other on a phone.
      closeNav();
    },
    [state, pathname, closeNav],
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

  // #657 fix (was #642 review fix): gated on resourcePresentationCall —
  // the authority for whether the bar's local-control mode is safe.
  // FloatingCallWindow is ALWAYS shown alongside it; this bar never
  // replaces it. Undefined -> present the instant discovery/resource/media
  // catch up, and never present a frame earlier.
  const resourceCallSession: ActiveResourceCallSession | undefined = resourcePresentationCall
    ? {
        callId: resourcePresentationCall.call_id,
        startedAt: resourcePresentationCall.created_at,
        participants: media.participants,
        localId: ready.currentUserId,
        localName: localIdentity.name,
        localInitials: localIdentity.initials,
        localAvatarUrl: localIdentity.avatarUrl,
        activeSpeakerId: media.activeSpeakerId,
        microphoneEnabled: media.microphoneEnabled,
        microphonePending: media.pendingControl === "microphone",
        onToggleMicrophone: () => void media.toggleMicrophone(),
        // Mirrors FloatingCallWindow's own resource onEnd exactly (issue
        // #642 review, blocker 5): endResourceParticipation deliberately
        // rethrows on failure — the error is already reflected through
        // resource.status/resource.error, the existing retry authority —
        // so the rejection must be swallowed here, never left unhandled.
        onLeave: () => {
          void leaveResourceParticipation().catch(() => undefined);
        },
        onOpenFullCall: () => {
          expand();
        },
      }
    : undefined;

  const outletContext: ChatOutletContext = {
    currentUserId: ready.currentUserId,
    channels: ready.channels,
    dms: ready.dms,
    attachmentLimits: ready.attachmentLimits,
    startCall: resourceCall.active ? undefined : calls.start,
    getResourceCall,
    isParticipatingIn,
    resourceCallSession,
    // Fresh join/rejoin gesture (issue #594 adversarial follow-up, round
    // 3): must go through joinResourceParticipation, never resourceCall.join
    // directly, so an old "left" for whatever this callId's participation
    // used to be can never abort this brand-new attempt before it registers
    // — target.callId is undefined for a fresh join (the server decides/
    // reuses it), a case joinResourceParticipation is specifically built to
    // still protect.
    joinResourceCall:
      directCallBusy || resourceCall.active
        ? undefined
        : (target) => void joinResourceParticipation(target),
  };

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
      <main className="chat-app__main" aria-label="Área de mensagens" inert={navModal}>
        <Outlet context={outletContext} />
      </main>
      <SidebarDetailsPanel
        target={openDetailsTarget}
        currentUserId={ready.currentUserId}
        onClose={closeSidebarDetails}
      />
    </div>
  );
}
