import { useEffect, useRef } from "react";
import { useLocation, useNavigate, useOutletContext, useParams } from "react-router";

import type { ChatOutletContext } from "./ChatShell";
import { selectDefaultConversation, type DefaultSelectionCandidate } from "./sidebarOrder";
import "./ChatPlaceholder.css";

interface Props {
  type?: "channel" | "dm";
}

const emptyOutletContext: ChatOutletContext = { currentUserId: "", channels: [], dms: [] };

/**
 * Redirects the index route (`/chat`, nothing explicitly selected) to the
 * most recently active unread conversation, or the most recently active
 * conversation overall when nothing is unread — the Discord-style default
 * landing behaviour.
 *
 * Only runs for the index case (`type` is undefined): a route already naming
 * a channel/DM never renders this component at all, so a manual selection or
 * a notification-driven navigation can never be raced or overridden by this
 * effect. `hasNavigatedRef` caps it at one `navigate()` call even if the
 * outlet context reference changes again (e.g. a realtime update) before the
 * route swap unmounts this component.
 *
 * Also skipped when the navigation to `/chat` itself carries
 * `skipDefaultConversationRedirect` in its router state — set by
 * `leaveConversation` (useChatSidebar.ts): leaving a conversation is a
 * deliberate action with its own documented neutral-route contract, not "the
 * reader hasn't picked anything yet", and must never be treated as an
 * invitation to auto-navigate elsewhere.
 */
function useDefaultConversationRedirect(type: Props["type"]) {
  const navigate = useNavigate();
  const location = useLocation();
  const outlet = useOutletContext<ChatOutletContext>() ?? emptyOutletContext;
  const hasNavigatedRef = useRef(false);
  const { channels, dms } = outlet;
  const skipRedirect = Boolean(
    (location.state as { skipDefaultConversationRedirect?: boolean } | null)
      ?.skipDefaultConversationRedirect,
  );

  useEffect(() => {
    if (type || skipRedirect || hasNavigatedRef.current) return;
    const candidates: DefaultSelectionCandidate[] = [
      ...channels.map((channel) => ({ ...channel, kind: "channel" as const })),
      ...dms.map((dm) => ({ ...dm, kind: "dm" as const })),
    ];
    const target = selectDefaultConversation(candidates);
    if (!target) return;
    hasNavigatedRef.current = true;
    navigate(`/chat/${target.kind}/${encodeURIComponent(target.id)}`, { replace: true });
  }, [type, skipRedirect, channels, dms, navigate]);
}

export default function ChatPlaceholder({ type }: Props) {
  const { id } = useParams<{ id?: string }>();
  useDefaultConversationRedirect(type);

  const label =
    type === "channel"
      ? `#${id ?? ""}`
      : type === "dm"
        ? (id ?? "").replace(/-/g, " ")
        : "Selecione um canal ou mensagem direta";

  return (
    <div className="chat-placeholder" data-testid="chat-placeholder">
      <div className="chat-placeholder__icon" aria-hidden="true">
        <svg
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          strokeWidth="1.5"
          strokeLinecap="round"
          strokeLinejoin="round"
        >
          <path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z" />
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
