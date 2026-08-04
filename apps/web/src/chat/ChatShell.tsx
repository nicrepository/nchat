import { useEffect } from "react";
import { Outlet } from "react-router";

import "./ChatShell.css";
import CallPanel from "./CallPanel";
import ChatSidebar from "./ChatSidebar";
import type { CallType } from "./callState";
import { useCallMedia } from "./useCallMedia";
import { useCallSignaling } from "./useCallSignaling";
import type { Channel, DMConversation } from "./chatTypes";
import { useChatSidebar } from "./useChatSidebar";

export interface ChatOutletContext {
  currentUserId: string;
  channels: Channel[];
  dms: DMConversation[];
  startCall?: (targetUserId: string, callType: CallType) => boolean;
}

export default function ChatShell() {
  const { state, retry } = useChatSidebar();
  const media = useCallMedia();
  const prepareMedia = media.prepare;
  const calls = useCallSignaling(media);

  const outletContext: ChatOutletContext = {
    currentUserId: state.status === "ready" ? state.currentUserId : "",
    channels: state.status === "ready" ? state.channels : [],
    dms: state.status === "ready" ? state.dms : [],
    startCall: calls.start,
  };

  const currentUserId = state.status === "ready" ? state.currentUserId : "";
  const incomingRinging =
    calls.call?.status === "ringing" && calls.call.callee_id === currentUserId;

  useEffect(() => {
    if (incomingRinging) void prepareMedia();
  }, [incomingRinging, prepareMedia]);

  const participantId = calls.call
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
      <ChatSidebar state={state} retry={retry} />
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
      />
    </div>
  );
}
