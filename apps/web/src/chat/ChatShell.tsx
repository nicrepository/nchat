import { useCallback, useEffect, useState } from "react";
import { Outlet, useLocation } from "react-router";

import { useCallSession } from "../calls/CallSessionProvider";
import "./ChatShell.css";
import ChatSidebar from "./ChatSidebar";
import SidebarDetailsPanel, { type SidebarDetailsTarget } from "./SidebarDetailsPanel";
import type { ResourceCallKind } from "./callApi";
import type { Call, CallType } from "./callState";
import type { ResourceCallTarget } from "./useResourceCallSession";
import type { Channel, DMConversation } from "./chatTypes";
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
} {
  if (state.status !== "ready") return { currentUserId: "", channels: [], dms: [] };
  return { currentUserId: state.currentUserId, channels: state.channels, dms: state.dms };
}

export interface ChatOutletContext {
  currentUserId: string;
  channels: Channel[];
  dms: DMConversation[];
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

export default function ChatShell() {
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
  const openSidebarDetails = useCallback(
    (kind: "channel" | "dm", targetId: string) => {
      const resolved = resolveDetailsTarget(
        kind,
        targetId,
        state.status === "ready" ? state.dms : [],
      );
      if (resolved) setSidebarDetails({ ...resolved, pathname });
    },
    [state, pathname],
  );

  const outletContext: ChatOutletContext = {
    currentUserId: ready.currentUserId,
    channels: ready.channels,
    dms: ready.dms,
    startCall: resourceCall.active ? undefined : calls.start,
    getResourceCall,
    isParticipatingIn,
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
    <div className="chat-app" data-testid="chat-shell">
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
      <main className="chat-app__main" aria-label="Área de mensagens">
        <Outlet context={outletContext} />
      </main>
      <SidebarDetailsPanel
        target={openDetailsTarget}
        currentUserId={ready.currentUserId}
        onClose={() => setSidebarDetails(null)}
      />
    </div>
  );
}
