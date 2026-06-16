import { Outlet } from "react-router-dom";

import "./ChatShell.css";
import ChatSidebar from "./ChatSidebar";

export default function ChatShell() {
  return (
    <div className="chat-app" data-testid="chat-shell">
      <ChatSidebar />
      <main className="chat-app__main" aria-label="Área de mensagens">
        <Outlet />
      </main>
    </div>
  );
}
