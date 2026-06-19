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

import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { useOutletContext, useParams } from "react-router-dom";

import "./ChatMessageArea.css";
import type { ChatOutletContext } from "./ChatShell";
import type { Message } from "./chatTypes";
import { useMessages, type LastMutation, type SendResult } from "./useMessages";

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

function senderLabel(msg: Message): string {
  return msg.senderDisplayName || msg.senderEmail || msg.senderId.slice(0, 8);
}

function senderInitials(msg: Message): string {
  const label = msg.senderDisplayName || msg.senderEmail;
  if (label) {
    return label
      .split(" ")
      .slice(0, 2)
      .map((w) => w[0]?.toUpperCase() ?? "")
      .join("");
  }
  return msg.senderId.slice(0, 2).toUpperCase();
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
          {senderInitials(message)}
        </div>
      )}
      <div className="chat-msg-area__msg-body">
        <div className="chat-msg-area__msg-meta">
          {!isMine && (
            <span className="chat-msg-area__msg-sender" data-testid="chat-msg-sender">
              {senderLabel(message)}
            </span>
          )}
          <span className="chat-msg-area__msg-time">{time}</span>
        </div>
        <div
          className={`chat-msg-area__msg-bubble${message.isRemoved ? " chat-msg-area__msg-bubble--removed" : ""}`}
        >
          {message.isRemoved ? "Mensagem removida." : message.bodyText}
        </div>
      </div>
    </div>
  );
}

interface MessageListProps {
  messages: Message[];
  currentUserId: string;
  hasMore: boolean;
  loadingMore: boolean;
  lastMutation: LastMutation;
  onLoadMore: () => void;
}

function MessageList({ messages, currentUserId, hasMore, loadingMore, lastMutation, onLoadMore }: MessageListProps) {
  const listRef = useRef<HTMLDivElement>(null);
  const bottomRef = useRef<HTMLDivElement>(null);
  const topSentinelRef = useRef<HTMLDivElement>(null);

  // Stable ref for the loadMore callback so the IO observer never needs to be
  // recreated due to a new function reference.
  const onLoadMoreRef = useRef(onLoadMore);
  useLayoutEffect(() => { onLoadMoreRef.current = onLoadMore; });

  // Track previous scrollHeight for prepend scroll-delta restoration.
  const prevScrollHeightRef = useRef(0);

  // Scroll management driven by lastMutation — explicit and race-condition-free.
  // "prepend" → restore position via scrollHeight delta (older messages added above).
  // "initial" | "append" → scroll to bottom.
  // "none" → no action (intermediate transition, e.g. "prepending" sets loadingMore=true
  //           which inserts the loading spinner before the fetch resolves).
  //
  // prevScrollHeightRef is captured ONLY on stable mutations ("initial", "append",
  // "prepend") — never on "none". This prevents the spinner's height from polluting
  // the reference value used to compute the scroll delta on the subsequent "prepend".
  // If we captured on "none", the delta would be wrong by the spinner height (~36px),
  // causing a visible jump after every successful loadMore.
  useLayoutEffect(() => {
    if (!listRef.current) return;
    const el = listRef.current;

    if (lastMutation === "prepend") {
      // Shift scrollTop by the amount the container grew so the user's view is stable.
      el.scrollTop += el.scrollHeight - prevScrollHeightRef.current;
    } else if (lastMutation === "initial" || lastMutation === "append") {
      if (typeof bottomRef.current?.scrollIntoView === "function") {
        bottomRef.current.scrollIntoView({ behavior: "smooth" });
      }
    }

    // Only snapshot scrollHeight in a stable state — not during "none" transitions
    // where the loading spinner may inflate the measurement.
    if (lastMutation !== "none") {
      prevScrollHeightRef.current = el.scrollHeight;
    }
  }, [messages, lastMutation]);

  // IntersectionObserver: fire loadMore when the top sentinel enters the viewport.
  //
  // Deps are [hasMore] only — NOT [loadingMore] or [onLoadMore].
  //
  // Excluding loadingMore prevents the observer from being torn down and recreated
  // each time a fetch starts/finishes. In browsers, recreating the observer while
  // the sentinel is still visible causes an immediate re-fire, leading to a loop of
  // extra fetches. The guard against concurrent fetches lives inside loadMore() via
  // stateRef, so removing loadingMore from deps here is safe.
  //
  // onLoadMore is excluded because onLoadMoreRef keeps it stable without recreation.
  useEffect(() => {
    const sentinel = topSentinelRef.current;
    if (!sentinel || !hasMore) return;

    const observer = new IntersectionObserver(
      (entries) => {
        if (entries[0]?.isIntersecting) {
          onLoadMoreRef.current();
        }
      },
      { threshold: 0 },
    );
    observer.observe(sentinel);
    return () => observer.disconnect();
  }, [hasMore]);

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
    <div ref={listRef} className="chat-msg-area__list" role="log" aria-live="polite" aria-label="Mensagens">
      <div ref={topSentinelRef} aria-hidden="true" />
      {loadingMore && (
        <div
          className="chat-msg-area__load-more"
          role="status"
          aria-label="Carregando mensagens anteriores"
          data-testid="load-more-indicator"
        />
      )}
      {withDividers.map((item, i) =>
        item.type === "divider" ? (
          <div key={`d-${i}`} className="chat-msg-area__day-divider" aria-label={item.label}>
            {item.label}
          </div>
        ) : (
          <MessageBubble
            key={item.message.id}
            message={item.message}
            isMine={!!currentUserId && item.message.senderId === currentUserId}
          />
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
  onSend: (body: string) => Promise<SendResult>;
}

function Composer({ placeholder, disabled = false, onSend }: ComposerProps) {
  const [draft, setDraft] = useState("");
  const [sending, setSending] = useState(false);

  const canSend = draft.trim().length > 0 && !sending && !disabled;

  async function handleSend() {
    if (!canSend) return;
    const body = draft.trim();
    setSending(true);
    try {
      const result = await onSend(body);
      if (result.status === "sent") {
        setDraft(""); // clear only on confirmed success for the current target
      }
      // result.status === "stale": target changed — draft preserved, no error shown
    } catch {
      // current-target failure — draft preserved for retry, error shown by parent
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

  const ctx = useOutletContext<ChatOutletContext>() ?? { currentUserId: "", channels: [], dms: [] };

  const resolvedName =
    kind === "channel"
      ? (ctx.channels.find((ch) => ch.id === targetId)?.name ?? targetId)
      : (ctx.dms.find((dm) => dm.id === targetId)?.name ?? targetId);

  const { state, sendMessage, retry, loadMore } = useMessages({ kind, targetId });

  const handleSend = useCallback(
    (body: string): Promise<SendResult> => sendMessage(body),
    [sendMessage],
  );

  return (
    <div className="chat-msg-area" data-testid="chat-message-area">
      {kind === "channel" ? (
        <HeaderChannel name={resolvedName} />
      ) : (
        <HeaderDM name={resolvedName} />
      )}

      {state.status === "loading" && <LoadingSkeleton />}

      {state.status === "error" && <ErrorState onRetry={retry} />}

      {state.status === "ready" && state.messages.length === 0 && (
        <EmptyState kind={kind} name={resolvedName} />
      )}

      {state.status === "ready" && state.messages.length > 0 && (
        <MessageList
          messages={state.messages}
          currentUserId={ctx.currentUserId}
          hasMore={state.nextCursor !== ""}
          loadingMore={state.loadingMore}
          lastMutation={state.lastMutation}
          onLoadMore={loadMore}
        />
      )}

      {state.sendError && (
        <div className="chat-msg-area__send-error" role="alert" data-testid="chat-send-error">
          <IconWarning />
          {state.sendError}
        </div>
      )}

      <Composer
        placeholder={
          kind === "channel" ? `Mensagem para #${resolvedName}…` : `Mensagem para ${resolvedName}…`
        }
        disabled={state.status !== "ready"}
        onSend={handleSend}
      />
    </div>
  );
}
