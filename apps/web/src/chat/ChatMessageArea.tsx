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
 * Implemented — see useMessages and useChatWebSocket.
 * Auth uses Sec-WebSocket-Protocol to pass the Bearer token (browser WebSocket
 * upgrade cannot set custom headers; token-in-URL is rejected server-side).
 */

import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { useOutletContext, useParams } from "react-router-dom";

import "./ChatMessageArea.css";
import type { ChatOutletContext } from "./ChatShell";
import type { Message } from "./chatTypes";
import { useMessages, type LastMutation, type SendResult } from "./useMessages";
import RichTextRenderer from "./RichTextRenderer";
import ChatComposer from "./ChatComposer";
import { fetchAllowedReactionEmojis } from "./chatApi";

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
  /** True when consecutive messages from the same sender within the same minute. */
  isGrouped?: boolean;
  onToggleReaction: (messageId: string, emoji: string) => void;
  allowedReactionEmojis: string[];
  pickerOpen: boolean;
  onPickerOpenChange: (messageId: string, open: boolean) => void;
}

interface ReactionToolbarProps {
  messageId: string;
  emojis: string[];
  visible: boolean;
  open: boolean;
  onOpenChange: (messageId: string, open: boolean) => void;
  onToggleReaction: (messageId: string, emoji: string) => void;
}

function ReactionToolbar({
  messageId,
  emojis,
  visible,
  open,
  onOpenChange,
  onToggleReaction,
}: ReactionToolbarProps) {
  const pickerRef = useRef<HTMLDivElement>(null);
  const quickEmojis = emojis.slice(0, 5);

  useEffect(() => {
    if (!open) return;
    const closeOnOutsideClick = (event: MouseEvent) => {
      if (!pickerRef.current?.contains(event.target as Node)) onOpenChange(messageId, false);
    };
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === "Escape") onOpenChange(messageId, false);
    };
    document.addEventListener("mousedown", closeOnOutsideClick);
    document.addEventListener("keydown", closeOnEscape);
    return () => {
      document.removeEventListener("mousedown", closeOnOutsideClick);
      document.removeEventListener("keydown", closeOnEscape);
    };
  }, [messageId, onOpenChange, open]);

  const selectReaction = (emoji: string) => {
    onToggleReaction(messageId, emoji);
    onOpenChange(messageId, false);
  };

  return (
    <div
      ref={pickerRef}
      className="chat-msg-area__reaction-toolbar"
      role="toolbar"
      aria-label="Ações de reação"
      data-visible={visible || open}
    >
      {quickEmojis.map((emoji) => (
        <button
          key={emoji}
          type="button"
          className="chat-msg-area__quick-reaction"
          aria-label={`Reagir rapidamente com ${emoji}`}
          onClick={() => selectReaction(emoji)}
        >
          {emoji}
        </button>
      ))}
      <button
        type="button"
        className="chat-msg-area__more-reactions"
        aria-label="Mais reações"
        aria-expanded={open}
        aria-haspopup="dialog"
        onClick={() => onOpenChange(messageId, !open)}
      >
        <span aria-hidden="true">☺</span>
      </button>
      {open && (
        <div className="chat-msg-area__reaction-grid" role="dialog" aria-label="Escolher reação">
          {emojis.map((emoji) => (
            <button
              key={emoji}
              type="button"
              aria-label={`Reagir com ${emoji}`}
              onClick={() => selectReaction(emoji)}
            >
              {emoji}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

function MessageReactions({
  message,
  onToggleReaction,
}: Pick<MessageBubbleProps, "message" | "onToggleReaction">) {
  if (message.isRemoved || message.reactions.length === 0) return null;
  return (
    <div className="chat-msg-area__reactions">
      {message.reactions.map((reaction) => (
        <button
          key={reaction.emoji}
          type="button"
          className={`chat-msg-area__reaction${reaction.reactedByMe ? " chat-msg-area__reaction--mine" : ""}`}
          aria-label={`${reaction.reactedByMe ? "Remover" : "Adicionar"} reação ${reaction.emoji}`}
          aria-pressed={reaction.reactedByMe}
          onClick={() => onToggleReaction(message.id, reaction.emoji)}
        >
          <span aria-hidden="true">{reaction.emoji}</span> {reaction.count}
        </button>
      ))}
    </div>
  );
}

function MessageMeta({
  message,
  isMine,
  isGrouped,
}: Pick<MessageBubbleProps, "message" | "isMine" | "isGrouped">) {
  if (isGrouped) return null;
  return (
    <div className="chat-msg-area__msg-meta">
      {!isMine && (
        <span className="chat-msg-area__msg-sender" data-testid="chat-msg-sender">
          {senderLabel(message)}
        </span>
      )}
      <span className="chat-msg-area__msg-time">{formatTime(message.createdAt)}</span>
    </div>
  );
}

function MessageBubble({
  message,
  isMine = false,
  isGrouped = false,
  onToggleReaction,
  allowedReactionEmojis,
  pickerOpen,
  onPickerOpenChange,
}: MessageBubbleProps) {
  const [toolbarVisible, setToolbarVisible] = useState(false);

  const hideToolbarAfterBlur = (event: React.FocusEvent<HTMLDivElement>) => {
    if (!event.currentTarget.contains(event.relatedTarget)) setToolbarVisible(false);
  };

  return (
    <div
      className={`chat-msg-area__msg${isMine ? " chat-msg-area__msg--mine" : ""}${isGrouped ? " chat-msg-area__msg--grouped" : ""}`}
      data-testid="chat-msg-bubble"
      tabIndex={0}
      onMouseEnter={() => setToolbarVisible(true)}
      onMouseLeave={() => setToolbarVisible(false)}
      onFocus={() => setToolbarVisible(true)}
      onBlur={hideToolbarAfterBlur}
      onTouchStart={() => setToolbarVisible(true)}
    >
      {!isMine && (
        <div className="chat-msg-area__msg-avatar" aria-hidden="true">
          {senderInitials(message)}
        </div>
      )}
      <div className="chat-msg-area__msg-body">
        <MessageMeta message={message} isMine={isMine} isGrouped={isGrouped} />
        <div className="chat-msg-area__bubble-wrap">
          <div
            className={`chat-msg-area__msg-bubble${message.isRemoved ? " chat-msg-area__msg-bubble--removed" : ""}`}
          >
            {message.isRemoved ? (
              "Mensagem removida."
            ) : (
              <RichTextRenderer text={message.bodyText} bodyFormat={message.bodyFormat} />
            )}
          </div>
          {!message.isRemoved && allowedReactionEmojis.length > 0 && (
            <ReactionToolbar
              messageId={message.id}
              emojis={allowedReactionEmojis}
              visible={toolbarVisible}
              open={pickerOpen}
              onOpenChange={onPickerOpenChange}
              onToggleReaction={onToggleReaction}
            />
          )}
        </div>
        <MessageReactions message={message} onToggleReaction={onToggleReaction} />
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
  onToggleReaction: (messageId: string, emoji: string) => void;
  allowedReactionEmojis: string[];
}

function MessageList({
  messages,
  currentUserId,
  hasMore,
  loadingMore,
  lastMutation,
  onLoadMore,
  onToggleReaction,
  allowedReactionEmojis,
}: MessageListProps) {
  const listRef = useRef<HTMLDivElement>(null);
  const bottomRef = useRef<HTMLDivElement>(null);
  const topSentinelRef = useRef<HTMLDivElement>(null);
  const [openPickerMessageId, setOpenPickerMessageId] = useState<string | null>(null);
  const handlePickerOpenChange = useCallback((messageId: string, open: boolean) => {
    setOpenPickerMessageId(open ? messageId : null);
  }, []);

  // Stable ref for the loadMore callback so the IO observer never needs to be
  // recreated due to a new function reference.
  const onLoadMoreRef = useRef(onLoadMore);
  useLayoutEffect(() => {
    onLoadMoreRef.current = onLoadMore;
  });

  // Track previous scrollHeight for prepend scroll-delta restoration.
  const prevScrollHeightRef = useRef(0);

  // isNearBottomRef tracks whether the user is scrolled near the bottom of the
  // list. Updated on every scroll event. Used to decide whether a WS-received
  // message should auto-scroll the view or preserve the user's current position
  // (e.g. when reading history).
  //
  // Threshold: user is "near bottom" when the distance from the bottom edge of
  // the scroll container to the actual bottom is ≤ 150 px (roughly one message).
  // Starts true because the initial ready view is rendered at the latest messages.
  const isNearBottomRef = useRef(true);

  useEffect(() => {
    const el = listRef.current;
    if (!el) return;
    const onScroll = () => {
      isNearBottomRef.current = el.scrollHeight - el.scrollTop - el.clientHeight <= 150;
    };
    el.addEventListener("scroll", onScroll, { passive: true });
    return () => el.removeEventListener("scroll", onScroll);
  }, []);

  // Scroll management driven by lastMutation — explicit and race-condition-free.
  // "prepend"    → restore position via scrollHeight delta (older messages added above).
  // "initial" | "append" → scroll to bottom unconditionally.
  // "ws_append"  → scroll to bottom only when the user is already near the bottom;
  //                otherwise preserve position so reading history is not interrupted.
  // "none"       → no action (intermediate transition).
  //
  // prevScrollHeightRef is captured ONLY on stable mutations ("initial", "append",
  // "ws_append", "prepend") — never on "none". This prevents the spinner's height
  // from polluting the reference value used to compute the scroll delta on the
  // subsequent "prepend". If we captured on "none", the delta would be wrong by
  // the spinner height (~36px), causing a visible jump after every successful loadMore.
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
    } else if (lastMutation === "ws_append" && isNearBottomRef.current) {
      // Only auto-scroll on WS messages when user is already near the bottom.
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

  // Group messages by day for dividers; track same-sender/same-minute for visual grouping.
  const withDividers: Array<
    { type: "divider"; label: string } | { type: "msg"; message: Message; isGrouped: boolean }
  > = [];
  let lastDay = "";
  let lastSenderId = "";
  let lastMinute = "";
  for (const msg of messages) {
    const day = formatDate(msg.createdAt);
    if (day !== lastDay) {
      withDividers.push({ type: "divider", label: day });
      lastDay = day;
      lastSenderId = "";
      lastMinute = "";
    }
    const minute = formatTime(msg.createdAt);
    const isGrouped = msg.senderId === lastSenderId && minute === lastMinute;
    withDividers.push({ type: "msg", message: msg, isGrouped });
    lastSenderId = msg.senderId;
    lastMinute = minute;
  }

  return (
    <div
      ref={listRef}
      className="chat-msg-area__list"
      role="log"
      aria-live="polite"
      aria-label="Mensagens"
    >
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
            isGrouped={item.isGrouped}
            onToggleReaction={onToggleReaction}
            allowedReactionEmojis={allowedReactionEmojis}
            pickerOpen={openPickerMessageId === item.message.id}
            onPickerOpenChange={handlePickerOpenChange}
          />
        ),
      )}
      <div ref={bottomRef} />
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

  const { state, sendMessage, retry, loadMore, toggleReaction } = useMessages({
    kind,
    targetId,
    currentUserId: ctx.currentUserId,
  });
  const [allowedReactionEmojiState, setAllowedReactionEmojis] = useState<string[]>([]);
  const allowedReactionEmojis = useMemo(
    () => allowedReactionEmojiState,
    [allowedReactionEmojiState],
  );

  useEffect(() => {
    let active = true;
    fetchAllowedReactionEmojis().then(
      (emojis) => {
        if (active) setAllowedReactionEmojis(emojis);
      },
      () => {
        // Message rendering remains available if optional reaction config fails.
      },
    );
    return () => {
      active = false;
    };
  }, []);

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
          onToggleReaction={toggleReaction}
          allowedReactionEmojis={allowedReactionEmojis}
        />
      )}

      {state.sendError && (
        <div className="chat-msg-area__send-error" role="alert" data-testid="chat-send-error">
          <IconWarning />
          {state.sendError}
        </div>
      )}

      {state.realtimeError && (
        <div
          className="chat-msg-area__realtime-error"
          role="status"
          data-testid="chat-realtime-error"
        >
          <IconWarning />
          Conexão em tempo real instável. Tentando reconectar...
        </div>
      )}

      <ChatComposer
        channelId={kind === "channel" ? targetId : undefined}
        bodyFormat={kind === "channel" ? "v3" : "v2"}
        placeholder={
          kind === "channel" ? `Mensagem para #${resolvedName}…` : `Mensagem para ${resolvedName}…`
        }
        disabled={state.status !== "ready"}
        onSend={handleSend}
      />
    </div>
  );
}
