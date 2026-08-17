import { useEffect } from "react";
import { Outlet } from "react-router";

import "./ChatShell.css";
import CallPanel from "./CallPanel";
import ChatSidebar from "./ChatSidebar";
import type { CallType } from "./callState";
import { useCallMedia } from "./useCallMedia";
import { useCallSignaling, type CallMediaBridge } from "./useCallSignaling";
import { useResourceCallSession, type ResourceCallTarget } from "./useResourceCallSession";
import type { Channel, DMConversation } from "./chatTypes";
import { useChatSidebar } from "./useChatSidebar";

export interface ChatOutletContext {
  currentUserId: string;
  channels: Channel[];
  dms: DMConversation[];
  startCall?: (targetUserId: string, callType: CallType) => boolean;
  /** RF-24: join the shared call room for a channel or a group DM. */
  joinResourceCall?: (target: ResourceCallTarget) => void;
}

export default function ChatShell() {
  const { state, retry, setPinned } = useChatSidebar();
  const media = useCallMedia();
  const prepareMedia = media.prepare;
  const identityReady = state.status === "ready";
  const resourceCall = useResourceCallSession(media);

  // Single arbiter for the one LiveKit Room the two lifecycles share.
  // RF-23's own stop() calls (decline/cancel/end/reconnect-cleanup) must
  // never tear down a Room that RF-24 currently owns — e.g. declining an
  // unrelated incoming call while a resource room is active must not kill
  // it. RF-24's hook keeps unrestricted access to the real `media`; only
  // the RF-23 signaling hook goes through this guard.
  const directCallMedia: CallMediaBridge = {
    startAudio: media.startAudio,
    connect: media.connect,
    stop: async () => {
      if (resourceCall.active) return;
      await media.stop();
    },
  };

  // Ownership of the Room transfers here — not around accept(), and not
  // inside directCallMedia.connect above. accept() returning true only means
  // a command was *sent*, not that RF-23 will ever hold media: it can still
  // lose a pending-command race, fail its own permission preflight, or never
  // have call.accept delivered at all, and handing RF-24 away in those cases
  // would abandon it for nothing. useCallSignaling calls this exactly once,
  // right when the server confirms the call is "active" and immediately
  // before it requests its own token — the one moment RF-23 is guaranteed to
  // actually need the Room. A rejection here (RF-24's cleanup failed) is
  // surfaced through the same token/connect error path RF-23 already has,
  // and never silently starts a second Room.
  const onBeforeDirectMedia = async () => {
    if (resourceCall.active) await resourceCall.leave();
  };
  const calls = useCallSignaling(directCallMedia, identityReady, onBeforeDirectMedia);

  // RF-23 and RF-24 share one LiveKit Room (`media`): a direct call already
  // ringing/active takes priority, so a resource room can't be joined while
  // one is in progress.
  const directCallBusy =
    calls.call !== null &&
    !["declined", "cancelled", "timed_out", "ended"].includes(calls.call.status);
  const outletContext: ChatOutletContext = {
    currentUserId: state.status === "ready" ? state.currentUserId : "",
    channels: state.status === "ready" ? state.channels : [],
    dms: state.status === "ready" ? state.dms : [],
    startCall: resourceCall.active ? undefined : calls.start,
    joinResourceCall:
      directCallBusy || resourceCall.active
        ? undefined
        : (target) => void resourceCall.join(target),
  };

  const currentUserId = state.status === "ready" ? state.currentUserId : "";
  const incomingRinging =
    calls.call?.status === "ringing" && calls.call.callee_id === currentUserId;

  useEffect(() => {
    if (incomingRinging) void prepareMedia();
  }, [incomingRinging, prepareMedia]);

  const participantId =
    identityReady && calls.call
      ? calls.call.caller_id === currentUserId
        ? calls.call.callee_id
        : calls.call.caller_id
      : "";
  const participant =
    state.status === "ready"
      ? state.dms.find((dm) => dm.counterpart?.userId === participantId)?.counterpart
      : undefined;
  const participantName = participant?.displayName ?? "Participante";

  return (
    <div className="chat-app" data-testid="chat-shell">
      <ChatSidebar state={state} retry={retry} setPinned={setPinned} />
      <main className="chat-app__main" aria-label="Área de mensagens">
        <Outlet context={outletContext} />
      </main>
      <CallPanel
        calls={calls}
        currentUserId={currentUserId}
        identityStatus={state.status}
        retryIdentity={retry}
        participantId={participantId}
        participantName={participantName}
        participantAvatarUrl={participant?.avatarUrl}
        media={media}
        resource={resourceCall}
      />
    </div>
  );
}
