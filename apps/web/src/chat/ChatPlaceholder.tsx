import { useParams } from "react-router-dom";

import "./ChatPlaceholder.css";

export default function ChatPlaceholder() {
  const params = useParams<{ type?: string; id?: string }>();

  const label =
    params.type === "channel"
      ? `#${params.id ?? ""}`
      : params.type === "dm"
        ? params.id?.replace(/-/g, " ") ?? ""
        : "Selecione um canal ou mensagem direta";

  return (
    <div className="chat-placeholder" data-testid="chat-placeholder">
      <div className="chat-placeholder__icon" aria-hidden="true">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5"
          strokeLinecap="round" strokeLinejoin="round">
          <path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z"/>
        </svg>
      </div>
      <h2 className="chat-placeholder__title">{label}</h2>
      <p className="chat-placeholder__sub">
        {params.type
          ? "As mensagens aparecerão aqui em breve."
          : "Escolha um canal ou uma conversa na barra lateral para começar."}
      </p>
    </div>
  );
}
