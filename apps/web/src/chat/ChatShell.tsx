import { useCallback, useEffect } from "react";
import { Outlet } from "react-router";

import { useCallSession } from "../calls/CallSessionProvider";
import type { ParticipantMedia } from "./useCallMedia";
import "./ChatShell.css";
import ChatSidebar from "./ChatSidebar";
import type { ResourceCallKind } from "./callApi";
import type { Call, CallType } from "./callState";
import type { ResourceCallTarget } from "./useResourceCallSession";
import type { Channel, DMConversation } from "./chatTypes";
import { useChatSidebar } from "./useChatSidebar";

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
  /** Present only while participating in a resource call with local ownership (issue #642). */
  resourceCallSession?: ActiveResourceCallSession;
}

export default function ChatShell() {
  const { state, retry, setPinned, markRead, renameChannel } = useChatSidebar();
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
  const currentUserId = state.status === "ready" ? state.currentUserId : "";
  // #642 (review fix): gated on resourcePresentationCall alone — the single
  // authority CallSessionProvider also uses to suppress its own
  // FloatingCallWindow — never a looser, independently-recomputed check.
  // Never undefined -> present the instant discovery/resource/media catch
  // up out of a connecting/reconnecting/error/leaving state, and never
  // present a frame earlier: that authority already accounts for a stale
  // discovery call_id (call.admitted/call.accepted have no ordering
  // guarantee) and for the "leaving" participation phase clearing
  // synchronously, before the leave's own server round trip resolves.
  const resourceCallSession: ActiveResourceCallSession | undefined = resourcePresentationCall
    ? {
        callId: resourcePresentationCall.call_id,
        startedAt: resourcePresentationCall.created_at,
        participants: media.participants,
        localId: currentUserId,
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
    currentUserId,
    channels: state.status === "ready" ? state.channels : [],
    dms: state.status === "ready" ? state.dms : [],
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
    <div className="chat-app" data-testid="chat-shell">
      <ChatSidebar
        state={state}
        retry={retry}
        setPinned={setPinned}
        markRead={markRead}
        renameChannel={renameChannel}
      />
      <main className="chat-app__main" aria-label="Área de mensagens">
        <Outlet context={outletContext} />
      </main>
    </div>
  );
}
