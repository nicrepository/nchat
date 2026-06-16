/**
 * ChatMessageArea — central message area rendered for /chat/channel/:id and /chat/dm/:id.
 *
 * Security invariants:
 * - Message text is rendered as React text nodes (no dangerouslySetInnerHTML).
 * - Line breaks preserved via CSS white-space: pre-wrap, never via HTML injection.
 * - Route :id is decoded via safeDecodeURIComponent before use and re-encoded on navigate.
 * - No token, localStorage, or sessionStorage writes anywhere in this component.
 * - AbortController in useMessages cancels in-flight requests on target change or unmount.
 * - author_id is never sent; sender identity comes from the server-side JWT.
 *
 * WebSocket realtime delivery:
 * NOT IMPLEMENTED — prerequisite missing.
 * ws/handler.go returns 501 Not Implemented; no secure browser WS auth path exists.
 * A future PR must implement: (a) auth ticket or same-origin cookie WS upgrade design,
 * (b) BearerAuth equivalent for WS, (c) ServeWS implementation, (d) frontend WS client.
 */

import { useCallback, useEffect, useRef, useState } from "react";
import { useParams } from "react-router-dom";

import "./ChatMessageArea.css";
import type { Message } from "./chatTypes";
import { useMessages } from "./useMessages";

// ── Helpers ───────────────────────────────────────────────────────────────────

function safeDecodeURIComponent(s: string): string {
  try {
    return decodeURIComponent(s);
  } catch {
    return s;
  }
}

function formatTime(iso: string): string {
  try {
    return new Date(iso).toLocaleTimeString("pt-BR", { hour: "2-digit", minute: "2-digit" });
  } catch {
    return "";
  }
}

function formatDate(iso: string): string {
  try {
    const d = new Date(iso);
    const today = new Date();
    const yesterday = new Date(today);
    yesterday.setDate(yesterday.getDate() - 1);
    if (d.toDateString() === today.toDateString()) return "Hoje";
    if (d.toDateString() === yesterday.toDateString()) return "Ontem";
    return d.toLocaleDateString("pt-BR", { day: "numeric", month: "long", year: "numeric" });
  } catch {
    return "";
  }
}

// ── SVG icons ─────────────────────────────────────────────────────────────────

function IconHash({ className }: { className?: string }) {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      className={className}
      width="20"
      height="20"
    >
      <line x1="10" y1="4" x2="8" y2="20" />
      <line x1="16" y1="4" x2="14" y2="20" />
      <line x1="4" y1="9" x2="20" y2="9" />
      <line x1="3" y1="15" x2="19" y2="15" />
    </svg>
  );
}

function IconSend() {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      width="16"
      height="16"
    >
      <line x1="22" y1="2" x2="11" y2="13" />
      <polygon points="22 2 15 22 11 13 2 9 22 2" />
    </svg>
  );
}

function IconForum() {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.5"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      width="40"
      height="40"
    >
      <path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z" />
    </svg>
  );
}

function IconWarning() {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      width="28"
      height="28"
    >
      <path d="M10.29 3.86L1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z" />
      <line x1="12" y1="9" x2="12" y2="13" />
      <line x1="12" y1="17" x2="12.01" y2="17" />
    </svg>
  );
}

// ── Sub-components ────────────────────────────────────────────────────────────

interface HeaderChannelProps {
  name: string;
}

function HeaderChannel({ name }: HeaderChannelProps) {
  return (
    <header className="chat-msg-area__header" data-testid="chat-msg-header">
      <span className="chat-msg-area__header-icon" aria-hidden="true">
        <IconHash />
      </span>
      <h1 className="chat-msg-area__header-title">{name}</h1>
    </header>
  );
}

interface HeaderDMProps {
  name: string;
}

function HeaderDM({ name }: HeaderDMProps) {
  const initials = name
    .split(" ")
    .slice(0, 2)
    .map((w) => w[0]?.toUpperCase() ?? "")
    .join("");

  return (
    <header className="chat-msg-area__header" data-testid="chat-msg-header">
      <div className="chat-msg-area__header-avatar" aria-hidden="true">
        {initials || "?"}
      </div>
      <h1 className="chat-msg-area__header-title">{name}</h1>
    </header>
  );
}

function LoadingSkeleton() {
  return (
    <div
      className="chat-msg-area__loading"
      aria-busy="true"
      aria-label="Carregando mensagens"
      role="status"
    >
      {[
        { mine: false, w: "180px" },
        { mine: true, w: "220px" },
        { mine: false, w: "140px" },
        { mine: true, w: "260px" },
        { mine: false, w: "200px" },
      ].map(({ mine, w }, i) => (
        <div
          key={i}
          className={`chat-msg-area__skel-row${mine ? " chat-msg-area__skel-row--right" : ""}`}
        >
          {!mine && <div className="chat-msg-area__skel-avatar" />}
          <div className="chat-msg-area__skel-bubble" style={{ width: w }} />
        </div>
      ))}
    </div>
  );
}

interface ErrorStateProps {
  onRetry: () => void;
}

function ErrorState({ onRetry }: ErrorStateProps) {
  return (
    <div className="chat-msg-area__error" role="alert" data-testid="chat-msg-error">
      <div className="chat-msg-area__error-icon">
        <IconWarning />
      </div>
      <p className="chat-msg-area__error-msg">Não foi possível carregar as mensagens.</p>
      <button type="button" className="chat-msg-area__retry-btn" onClick={onRetry}>
        Tentar novamente
      </button>
    </div>
  );
}

interface EmptyStateProps {
  kind: "channel" | "dm";
  name: string;
}

function EmptyState({ kind, name }: EmptyStateProps) {
  return (
    <div className="chat-msg-area__empty" data-testid="chat-msg-empty">
      <div className="chat-msg-area__empty-icon">
        <IconForum />
      </div>
      <h2 className="chat-msg-area__empty-title">Nenhuma mensagem ainda</h2>
      <p className="chat-msg-area__empty-sub">
        {kind === "channel"
          ? `Este é o início do canal #${name}. Envie a primeira mensagem!`
          : `Esta é a sua conversa com ${name}. Diga olá!`}
      </p>
    </div>
  );
}

interface MessageBubbleProps {
  message: Message;
  /** True when this message is from the viewing user (we don't know real user ID here). */
  isMine?: boolean;
}

function MessageBubble({ message, isMine = false }: MessageBubbleProps) {
  const time = formatTime(message.createdAt);

  return (
    <div
      className={`chat-msg-area__msg${isMine ? " chat-msg-area__msg--mine" : ""}`}
      data-testid="chat-msg-bubble"
    >
      {!isMine && (
        <div className="chat-msg-area__msg-avatar" aria-hidden="true">
          {message.senderId.slice(0, 2).toUpperCase()}
        </div>
      )}
      <div className="chat-msg-area__msg-body">
        <div className="chat-msg-area__msg-meta">
          {!isMine && (
            <span className="chat-msg-area__msg-sender">{message.senderId.slice(0, 8)}</span>
          )}
          <span className="chat-msg-area__msg-time">{time}</span>
        </div>
        <div
          className={`chat-msg-area__msg-bubble${message.isRemoved ? " chat-msg-area__msg-bubble--removed" : ""}`}
        >
          {/* Text is rendered as a React text node — no dangerouslySetInnerHTML. */}
          {/* Line breaks preserved via CSS white-space: pre-wrap. */}
          {message.isRemoved ? "Mensagem removida." : message.bodyText}
        </div>
      </div>
    </div>
  );
}

interface MessageListProps {
  messages: Message[];
}

function MessageList({ messages }: MessageListProps) {
  const bottomRef = useRef<HTMLDivElement>(null);

  // Scroll to bottom when new messages arrive.
  // scrollIntoView may be unavailable in test environments (jsdom).
  useEffect(() => {
    if (typeof bottomRef.current?.scrollIntoView === "function") {
      bottomRef.current.scrollIntoView({ behavior: "smooth" });
    }
  }, [messages]);

  // Group messages by day for dividers.
  const withDividers: Array<
    { type: "divider"; label: string } | { type: "msg"; message: Message }
  > = [];
  let lastDay = "";
  for (const msg of messages) {
    const day = formatDate(msg.createdAt);
    if (day !== lastDay) {
      withDividers.push({ type: "divider", label: day });
      lastDay = day;
    }
    withDividers.push({ type: "msg", message: msg });
  }

  return (
    <div className="chat-msg-area__list" role="log" aria-live="polite" aria-label="Mensagens">
      {withDividers.map((item, i) =>
        item.type === "divider" ? (
          <div key={`d-${i}`} className="chat-msg-area__day-divider" aria-label={item.label}>
            {item.label}
          </div>
        ) : (
          <MessageBubble key={item.message.id} message={item.message} />
        ),
      )}
      <div ref={bottomRef} />
    </div>
  );
}

// ── Composer ──────────────────────────────────────────────────────────────────

interface ComposerProps {
  placeholder: string;
  disabled?: boolean;
  onSend: (body: string) => Promise<void>;
}

function Composer({ placeholder, disabled = false, onSend }: ComposerProps) {
  const [draft, setDraft] = useState("");
  const [sending, setSending] = useState(false);

  const canSend = draft.trim().length > 0 && !sending && !disabled;

  async function handleSend() {
    if (!canSend) return;
    const body = draft.trim();
    setDraft("");
    setSending(true);
    try {
      await onSend(body);
    } finally {
      setSending(false);
    }
  }

  function handleKeyDown(e: React.KeyboardEvent<HTMLTextAreaElement>) {
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      void handleSend();
    }
  }

  return (
    <div className="chat-msg-area__composer">
      <div
        className={`chat-msg-area__composer-box${disabled ? " chat-msg-area__composer-box--disabled" : ""}`}
      >
        <textarea
          className="chat-msg-area__composer-input"
          placeholder={placeholder}
          value={draft}
          rows={1}
          disabled={disabled || sending}
          aria-label={placeholder}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={handleKeyDown}
          data-testid="chat-composer-input"
        />
        <div className="chat-msg-area__composer-bar">
          <button
            type="button"
            className="chat-msg-area__send-btn"
            disabled={!canSend}
            aria-label="Enviar mensagem"
            onClick={() => void handleSend()}
            data-testid="chat-send-btn"
          >
            <IconSend />
          </button>
        </div>
      </div>
    </div>
  );
}

// ── Main component ────────────────────────────────────────────────────────────

interface ChatMessageAreaProps {
  kind: "channel" | "dm";
}

export default function ChatMessageArea({ kind }: ChatMessageAreaProps) {
  const params = useParams<{ id: string }>();
  const rawId = params.id ?? "";
  const targetId = safeDecodeURIComponent(rawId);

  const displayName = kind === "channel" ? `#${targetId}` : targetId;

  const { state, sendMessage, retry } = useMessages({ kind, targetId });

  const handleSend = useCallback(
    async (body: string) => {
      await sendMessage(body);
    },
    [sendMessage],
  );

  return (
    <div className="chat-msg-area" data-testid="chat-message-area">
      {kind === "channel" ? <HeaderChannel name={targetId} /> : <HeaderDM name={displayName} />}

      {state.status === "loading" && <LoadingSkeleton />}

      {state.status === "error" && <ErrorState onRetry={retry} />}

      {state.status === "ready" && state.messages.length === 0 && (
        <>
          <EmptyState kind={kind} name={targetId} />
        </>
      )}

      {state.status === "ready" && state.messages.length > 0 && (
        <MessageList messages={state.messages} />
      )}

      {state.sendError && (
        <div className="chat-msg-area__send-error" role="alert" data-testid="chat-send-error">
          <IconWarning />
          {state.sendError}
        </div>
      )}

      <Composer
        placeholder={
          kind === "channel" ? `Mensagem para #${targetId}…` : `Mensagem para ${displayName}…`
        }
        disabled={state.status !== "ready"}
        onSend={handleSend}
      />
    </div>
  );
}
