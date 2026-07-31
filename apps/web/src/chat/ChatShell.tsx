import { Outlet } from "react-router";

import "./ChatShell.css";
import CallPanel from "./CallPanel";
import ChatSidebar from "./ChatSidebar";
import type { CallType } from "./callState";
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
  const calls = useCallSignaling();

  const outletContext: ChatOutletContext = {
    currentUserId: state.status === "ready" ? state.currentUserId : "",
    channels: state.status === "ready" ? state.channels : [],
    dms: state.status === "ready" ? state.dms : [],
    startCall: calls.start,
  };

  const currentUserId = state.status === "ready" ? state.currentUserId : "";
  const participantId = calls.call
    ? calls.call.caller_id === currentUserId
      ? calls.call.callee_id
      : calls.call.caller_id
    : "";
  const participantName =
    state.status === "ready"
      ? (state.dms.find((dm) => dm.counterpart?.userId === participantId)?.counterpart
          ?.displayName ?? "Participante")
      : "Participante";

  return (
    <div className="chat-app" data-testid="chat-shell">
      <ChatSidebar state={state} retry={retry} />
      <main className="chat-app__main" aria-label="Área de mensagens">
        <Outlet context={outletContext} />
      </main>
      <CallPanel calls={calls} currentUserId={currentUserId} participantName={participantName} />
    </div>
  );
}
