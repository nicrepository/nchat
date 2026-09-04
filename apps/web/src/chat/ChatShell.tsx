import { useCallback, useEffect } from "react";
import { Outlet, useOutletContext } from "react-router";

import { useCallSession } from "../calls/CallSessionProvider";
import type { ParticipantMedia } from "./useCallMedia";
import type { AppShellOutletContext } from "./AppShell";
import type { ResourceCallKind } from "./callApi";
import type { Call, CallType } from "./callState";
import type { ResourceCallTarget } from "./useResourceCallSession";
import type { Channel, DMConversation } from "./chatTypes";
import type { WorkspaceAttachmentLimits } from "./chatApi";
import type { SidebarState } from "./useChatSidebar";

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

/**
 * Everything ActiveDirectCallBar (issue #673) needs to represent the user's
 * OWN direct 1:1 call inside the DM view that owns it — populated ONLY when
 * CallSessionProvider's own directPresentationCall authority is non-null
 * (mirrors ActiveResourceCallSession/resourcePresentationCall exactly): a
 * genuinely active, media-connected, locally-owned direct call, never merely
 * ringing. `callId`/`startedAt`/`callType` come straight from that same
 * validated Call. `peerUserId` is the server-resolved caller/callee id —
 * never the route or a display name — so ChatMessageArea can match it
 * against the open DM's own server-resolved counterpart before rendering
 * anything, and resolve the peer's display name/avatar from there rather
 * than from a second identity source.
 */
export interface ActiveDirectCallSession {
  callId: string;
  startedAt: string;
  callType: CallType;
  peerUserId: string;
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
  /**
   * The same markRead useChatSidebar already hands the sidebar's own "Marcar
   * como lida" menu action (#527) — not a second read-state mechanism.
   * ChatMessageArea calls it once it has evidence the user reached the real
   * bottom (#492); opening the route alone is no longer sufficient. Optional
   * like every other callback here, so a partial outlet context (tests,
   * emptyOutletContext) never has to fabricate one.
   */
  markRead?: (target: { kind: "channel" | "dm"; targetId: string }) => void;
  refreshConversations?: () => void;
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
  /** Present only while a direct 1:1 call is active, media-connected, and locally owned (issue #673) — never merely ringing. */
  directCallSession?: ActiveDirectCallSession;
}

export default function ChatShell() {
  const { state, retry, markRead } = useOutletContext<AppShellOutletContext>();
  const ready = readySidebar(state);
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
    directPresentationCall,
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

  // #673's view-scoped exit/expand for a direct call — same `expand()`
  // resourceCallSession already reuses above, and `calls.end` is the exact
  // same authoritative end action FloatingCallWindow's own controls.onEnd
  // uses for a direct call. No new lifecycle: this bundles read-only
  // pass-throughs of state CallSessionProvider already computed, never a
  // second source of media/call truth.
  const directCallSession: ActiveDirectCallSession | undefined = directPresentationCall
    ? {
        callId: directPresentationCall.call_id,
        startedAt: directPresentationCall.created_at,
        callType: directPresentationCall.call_type,
        peerUserId:
          directPresentationCall.caller_id === ready.currentUserId
            ? directPresentationCall.callee_id
            : directPresentationCall.caller_id,
        microphoneEnabled: media.microphoneEnabled,
        microphonePending: media.pendingControl === "microphone",
        onToggleMicrophone: () => void media.toggleMicrophone(),
        onLeave: () => {
          calls.end();
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
    markRead,
    refreshConversations: retry,
    startCall: resourceCall.active ? undefined : calls.start,
    getResourceCall,
    isParticipatingIn,
    resourceCallSession,
    directCallSession,
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

  return <Outlet context={outletContext} />;
}
