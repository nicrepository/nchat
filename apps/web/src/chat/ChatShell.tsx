import { useEffect } from "react";
import { Outlet } from "react-router";

import { useCallSession } from "../calls/CallSessionProvider";
import "./ChatShell.css";
import ChatSidebar from "./ChatSidebar";
import type { CallType } from "./callState";
import type { ResourceCallTarget } from "./useResourceCallSession";
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
  const {
    calls,
    resource: resourceCall,
    joinResourceParticipation,
    registerDirectory,
    registerIdentity,
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
  const outletContext: ChatOutletContext = {
    currentUserId: state.status === "ready" ? state.currentUserId : "",
    channels: state.status === "ready" ? state.channels : [],
    dms: state.status === "ready" ? state.dms : [],
    startCall: resourceCall.active ? undefined : calls.start,
    // Fresh join/rejoin gesture (issue #594 adversarial follow-up, round
    // 3): must go through joinResourceParticipation, never resourceCall.join
    // directly, so an old "left" for whatever this callId's participation
    // used to be can never abort this brand-new attempt before it registers
    // — target.callId is undefined here (the server decides/reuses it), a
    // case joinResourceParticipation is specifically built to still protect.
    joinResourceCall:
      directCallBusy || resourceCall.active
        ? undefined
        : (target) => void joinResourceParticipation(target),
  };

  return (
    <div className="chat-app" data-testid="chat-shell">
      <ChatSidebar state={state} retry={retry} setPinned={setPinned} />
      <main className="chat-app__main" aria-label="Área de mensagens">
        <Outlet context={outletContext} />
      </main>
    </div>
  );
}
