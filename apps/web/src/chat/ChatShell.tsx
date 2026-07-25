import { Outlet } from "react-router";

import "./ChatShell.css";
import ChatSidebar from "./ChatSidebar";
import type { Channel, DMConversation } from "./chatTypes";
import { useChatSidebar } from "./useChatSidebar";

export interface ChatOutletContext {
  currentUserId: string;
  channels: Channel[];
  dms: DMConversation[];
}

export default function ChatShell() {
  const { state, retry } = useChatSidebar();

  const outletContext: ChatOutletContext = {
    currentUserId: state.status === "ready" ? state.currentUserId : "",
    channels: state.status === "ready" ? state.channels : [],
    dms: state.status === "ready" ? state.dms : [],
  };

  return (
    <div className="chat-app" data-testid="chat-shell">
      <ChatSidebar state={state} retry={retry} />
      <main className="chat-app__main" aria-label="Área de mensagens">
        <Outlet context={outletContext} />
      </main>
    </div>
  );
}
