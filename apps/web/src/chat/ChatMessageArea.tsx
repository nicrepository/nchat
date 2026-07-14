/**
 * ChatMessageArea — central message area rendered for /chat/channel/:id and /chat/dm/:id.
 *
 * Security invariants:
 * - Message text is rendered as React text nodes (no dangerouslySetInnerHTML).
 * - Line breaks preserved via CSS white-space: pre-wrap, never via HTML injection.
 * - Route :id is decoded via safeDecodeURIComponent before use and re-encoded on navigate.
 * - localStorage stores only allowlisted recent reaction emojis, scoped by user ID.
 * - No token or message content is written to localStorage or sessionStorage.
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
import type { Message, PinnedItem } from "./chatTypes";
import { fetchAllowedReactionEmojis } from "./chatApi";
import { useMessages, type LastMutation, type SendResult } from "./useMessages";
import { usePins } from "./usePins";
import ChatComposer from "./ChatComposer";
import MessageBubble, { type MessageBubbleProps } from "./MessageBubble";
import { formatTime, senderLabel } from "./messageDisplay";

// ── Helpers ───────────────────────────────────────────────────────────────────

const defaultRecentReactions = ["👍", "❤️", "😂"];
const quoteHighlightMs = 1_200;
const reactionMenuLeaveDelayMs = 150;

function recentReactionsKey(userID: string): string {
  return `nchat_recent_reactions:${userID}`;
}

function allowedRecentReactions(
  userID: string,
  allowed: string[],
  sessionRecent: string[] = [],
): string[] {
  let stored: unknown = [];
  try {
    stored = JSON.parse(localStorage.getItem(recentReactionsKey(userID)) ?? "[]");
  } catch {
    // Ignore malformed local preferences.
  }
  const candidates = [
    ...sessionRecent,
    ...(Array.isArray(stored)
      ? stored.filter((emoji): emoji is string => typeof emoji === "string")
      : []),
    ...defaultRecentReactions,
    ...allowed,
  ];
  return [...new Set(candidates)].filter((emoji) => allowed.includes(emoji)).slice(0, 3);
}

function safeDecodeURIComponent(s: string): string {
  try {
    return decodeURIComponent(s);
  } catch {
    return s;
  }
}

function quoteAuthorLabel(
  quote: NonNullable<Message["quoted"]>,
  messagesById: Map<string, Message>,
) {
  const parent = messagesById.get(quote.id);
  return parent ? senderLabel(parent) : "Usuário desconhecido";
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

interface MessageListProps {
  messages: Message[];
  currentUserId: string;
  hasMore: boolean;
  loadingMore: boolean;
  lastMutation: LastMutation;
  onLoadMore: () => void;
  onToggleReaction: (messageId: string, emoji: string) => void;
  onReplyMessage: (message: Message) => void;
  onToggleFavorite: (messageId: string, isFavorited: boolean) => void;
  onEditMessage: MessageBubbleProps["onEditMessage"];
  onEditForbidden: MessageBubbleProps["onEditForbidden"];
  editDisabledIds: Set<string>;
  channelId?: string;
  /** RF-05: pin/unpin action for readable channels and DMs. */
  onTogglePin?: (messageId: string, pin: boolean) => void;
  /** RF-05: set of currently-pinned message IDs in this target. */
  pinnedIds?: Set<string>;
  allowedReactionEmojis: string[];
  recentReactionEmojis: string[];
}

function MessageList({
  messages,
  currentUserId,
  hasMore,
  loadingMore,
  lastMutation,
  onLoadMore,
  onToggleReaction,
  onReplyMessage,
  onToggleFavorite,
  onEditMessage,
  onEditForbidden,
  editDisabledIds,
  channelId,
  onTogglePin,
  pinnedIds,
  allowedReactionEmojis,
  recentReactionEmojis,
}: MessageListProps) {
  const listRef = useRef<HTMLDivElement>(null);
  const bottomRef = useRef<HTMLDivElement>(null);
  const topSentinelRef = useRef<HTMLDivElement>(null);
  const messageRefs = useRef(new Map<string, HTMLDivElement>());
  const highlightTimerRef = useRef<number | null>(null);
  const hoverCloseTimerRef = useRef<number | null>(null);
  const [openPickerMessageId, setOpenPickerMessageId] = useState<string | null>(null);
  const [hoveredMessageId, setHoveredMessageId] = useState<string | null>(null);
  const [highlightedMessageId, setHighlightedMessageId] = useState<string | null>(null);
  const messagesById = useMemo(
    () => new Map(messages.map((message) => [message.id, message])),
    [messages],
  );
  const setMessageRef = useCallback((messageId: string, el: HTMLDivElement | null) => {
    if (el) messageRefs.current.set(messageId, el);
    else messageRefs.current.delete(messageId);
  }, []);
  const handleQuoteJump = useCallback((messageId: string) => {
    const el = messageRefs.current.get(messageId);
    if (!el) return;
    el.scrollIntoView({ behavior: "smooth", block: "center" });
    setHighlightedMessageId(messageId);
    if (highlightTimerRef.current !== null) window.clearTimeout(highlightTimerRef.current);
    highlightTimerRef.current = window.setTimeout(() => {
      setHighlightedMessageId(null);
      highlightTimerRef.current = null;
    }, quoteHighlightMs);
  }, []);
  const handlePickerOpenChange = useCallback((messageId: string, open: boolean) => {
    setOpenPickerMessageId(open ? messageId : null);
  }, []);
  const handleReactionMenuVisibleChange = useCallback(
    (messageId: string, visible: boolean) => {
      if (openPickerMessageId && openPickerMessageId !== messageId) return;
      if (hoverCloseTimerRef.current !== null) {
        window.clearTimeout(hoverCloseTimerRef.current);
        hoverCloseTimerRef.current = null;
      }
      if (visible) {
        setHoveredMessageId(messageId);
        return;
      }
      hoverCloseTimerRef.current = window.setTimeout(() => {
        setHoveredMessageId((current) => (current === messageId ? null : current));
        hoverCloseTimerRef.current = null;
      }, reactionMenuLeaveDelayMs);
    },
    [openPickerMessageId],
  );

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

  useEffect(() => {
    return () => {
      if (highlightTimerRef.current !== null) window.clearTimeout(highlightTimerRef.current);
      if (hoverCloseTimerRef.current !== null) window.clearTimeout(hoverCloseTimerRef.current);
    };
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
            onReplyMessage={onReplyMessage}
            onToggleFavorite={onToggleFavorite}
            onEditMessage={onEditMessage}
            onEditForbidden={onEditForbidden}
            editDisabled={editDisabledIds.has(item.message.id)}
            channelId={channelId}
            onTogglePin={onTogglePin}
            isPinned={pinnedIds?.has(item.message.id) ?? false}
            allowedReactionEmojis={allowedReactionEmojis}
            recentReactionEmojis={recentReactionEmojis}
            reactionMenuVisible={hoveredMessageId === item.message.id}
            onReactionMenuVisibleChange={handleReactionMenuVisibleChange}
            pickerOpen={openPickerMessageId === item.message.id}
            onPickerOpenChange={handlePickerOpenChange}
            quoteAuthorLabel={
              item.message.quoted ? quoteAuthorLabel(item.message.quoted, messagesById) : undefined
            }
            canJumpToQuote={item.message.quoted ? messagesById.has(item.message.quoted.id) : false}
            onQuoteJump={handleQuoteJump}
            isHighlighted={highlightedMessageId === item.message.id}
            setMessageRef={setMessageRef}
          />
        ),
      )}
      <div ref={bottomRef} />
    </div>
  );
}

// ── Pinned messages bar (RF-05) ──────────────────────────────────────────────

interface PinnedBarProps {
  pins: PinnedItem[];
  onUnpin: (messageId: string, pin: boolean) => void;
}

function PinnedBar({ pins, onUnpin }: PinnedBarProps) {
  const [expanded, setExpanded] = useState(false);
  if (pins.length === 0) return null;
  return (
    <section className="chat-msg-area__pins" aria-label="Mensagens fixadas" data-testid="chat-pins">
      <button
        type="button"
        className="chat-msg-area__pins-toggle"
        aria-expanded={expanded}
        onClick={() => setExpanded((v) => !v)}
      >
        <span className="material-symbols-outlined" aria-hidden="true">
          keep
        </span>
        {pins.length === 1 ? "1 mensagem fixada" : `${pins.length} mensagens fixadas`}
      </button>
      {expanded && (
        <ul className="chat-msg-area__pins-list">
          {pins.map((pin) => (
            <li key={pin.message.id} className="chat-msg-area__pins-item">
              <span className="chat-msg-area__pins-text">
                <span className="chat-msg-area__pins-sender">{senderLabel(pin.message)}: </span>
                {pin.message.isRemoved ? "Mensagem removida." : pin.message.bodyText}
              </span>
              <button
                type="button"
                aria-label="Desafixar mensagem"
                onClick={() => onUnpin(pin.message.id, false)}
              >
                <span className="material-symbols-outlined" aria-hidden="true">
                  close
                </span>
              </button>
            </li>
          ))}
        </ul>
      )}
    </section>
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

  const [allowedReactionEmojiState, setAllowedReactionEmojis] = useState<string[]>([]);
  const [sessionRecentReactions, setSessionRecentReactions] = useState<{
    userID: string;
    emojis: string[];
  }>({ userID: "", emojis: [] });
  const [reactionInputError, setReactionInputError] = useState<string | null>(null);
  const [editDisabledIds, setEditDisabledIds] = useState<Set<string>>(new Set());
  const lastReactionToggleRef = useRef({ key: "", at: 0 });
  const allowedReactionEmojis = useMemo(
    () => allowedReactionEmojiState,
    [allowedReactionEmojiState],
  );
  const recentReactionEmojis = useMemo(
    () =>
      allowedRecentReactions(
        ctx.currentUserId,
        allowedReactionEmojis,
        sessionRecentReactions.userID === ctx.currentUserId ? sessionRecentReactions.emojis : [],
      ),
    [allowedReactionEmojis, ctx.currentUserId, sessionRecentReactions],
  );

  const rememberReaction = useCallback(
    (emoji: string) => {
      if (!ctx.currentUserId || !allowedReactionEmojis.includes(emoji)) return;
      setSessionRecentReactions((current) => {
        const existing =
          current.userID === ctx.currentUserId
            ? current.emojis
            : allowedRecentReactions(ctx.currentUserId, allowedReactionEmojis);
        const next = [emoji, ...existing.filter((item) => item !== emoji)].slice(0, 3);
        try {
          localStorage.setItem(recentReactionsKey(ctx.currentUserId), JSON.stringify(next));
        } catch {
          // Local preferences are best-effort and must never block reaction updates.
        }
        return { userID: ctx.currentUserId, emojis: next };
      });
    },
    [allowedReactionEmojis, ctx.currentUserId],
  );

  const pinTarget = useMemo(() => (targetId ? { kind, id: targetId } : null), [kind, targetId]);
  const { pins, pinnedIds, error: pinError, togglePin, reload: reloadPins } = usePins(pinTarget);

  const {
    state,
    sendMessage,
    retry,
    loadMore,
    selectReply,
    cancelReply,
    toggleReaction,
    toggleFavorite,
    editMessageLocal,
  } = useMessages({
    kind,
    targetId,
    currentUserId: ctx.currentUserId,
    onOwnReactionConfirmed: rememberReaction,
    onPinUpdated: reloadPins,
  });

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

  const replyPreview = useMemo(
    () =>
      state.replyTo
        ? {
            authorLabel: senderLabel(state.replyTo),
            bodyText: state.replyTo.bodyText,
            bodyFormat: state.replyTo.bodyFormat,
            isRemoved: state.replyTo.isRemoved,
          }
        : null,
    [state.replyTo],
  );

  const handleToggleReaction = useCallback(
    (messageId: string, emoji: string) => {
      if (!allowedReactionEmojis.includes(emoji)) {
        setReactionInputError("Emoji não permitido para reações.");
        return;
      }
      const key = `${messageId}\u0000${emoji}`;
      const now = Date.now();
      if (
        lastReactionToggleRef.current.key === key &&
        now - lastReactionToggleRef.current.at < 300
      ) {
        return;
      }
      lastReactionToggleRef.current = { key, at: now };
      setReactionInputError(null);
      toggleReaction(messageId, emoji);
    },
    [allowedReactionEmojis, toggleReaction],
  );

  const handleEditForbidden = useCallback(
    (messageId: string) => {
      setEditDisabledIds((current) => new Set(current).add(messageId));
      retry();
    },
    [retry],
  );

  return (
    <div className="chat-msg-area" data-testid="chat-message-area">
      {kind === "channel" ? (
        <HeaderChannel name={resolvedName} />
      ) : (
        <HeaderDM name={resolvedName} />
      )}

      <PinnedBar pins={pins} onUnpin={togglePin} />

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
          onToggleReaction={handleToggleReaction}
          onReplyMessage={selectReply}
          onToggleFavorite={toggleFavorite}
          onEditMessage={editMessageLocal}
          onEditForbidden={handleEditForbidden}
          editDisabledIds={editDisabledIds}
          channelId={kind === "channel" ? targetId : undefined}
          onTogglePin={togglePin}
          pinnedIds={pinnedIds}
          allowedReactionEmojis={allowedReactionEmojis}
          recentReactionEmojis={recentReactionEmojis}
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

      {(reactionInputError || state.actionError || pinError) && (
        <div className="chat-msg-area__reaction-error" role="alert">
          <IconWarning />
          {reactionInputError || state.actionError || pinError}
        </div>
      )}

      <ChatComposer
        channelId={kind === "channel" ? targetId : undefined}
        bodyFormat={kind === "channel" ? "v3" : "v2"}
        placeholder={
          kind === "channel" ? `Mensagem para #${resolvedName}…` : `Mensagem para ${resolvedName}…`
        }
        disabled={state.status !== "ready"}
        replyPreview={replyPreview}
        onCancelReply={cancelReply}
        onSend={handleSend}
      />
    </div>
  );
}
