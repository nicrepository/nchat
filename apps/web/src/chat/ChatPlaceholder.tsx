import { useParams } from "react-router-dom";

import "./ChatPlaceholder.css";

interface Props {
  type?: "channel" | "dm";
}

export default function ChatPlaceholder({ type }: Props) {
  const { id } = useParams<{ id?: string }>();

  const label =
    type === "channel"
      ? `#${id ?? ""}`
      : type === "dm"
        ? (id ?? "").replace(/-/g, " ")
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
        {type
          ? "As mensagens aparecerão aqui em breve."
          : "Escolha um canal ou uma conversa na barra lateral para começar."}
      </p>
    </div>
  );
}
