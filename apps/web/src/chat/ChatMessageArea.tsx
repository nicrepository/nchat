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

import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type RefObject,
} from "react";
import { createPortal } from "react-dom";
import { useOutletContext, useParams } from "react-router-dom";

import "./ChatMessageArea.css";
import type { ChatOutletContext } from "./ChatShell";
import type { Message, PinnedItem } from "./chatTypes";
import { useMessages, type LastMutation, type SendResult } from "./useMessages";
import { usePins } from "./usePins";
import RichTextRenderer from "./RichTextRenderer";
import ChatComposer from "./ChatComposer";
import { fetchAllowedReactionEmojis } from "./chatApi";

// ── Helpers ───────────────────────────────────────────────────────────────────

const defaultRecentReactions = ["👍", "❤️", "😂"];

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
  onToggleFavorite: (messageId: string, isFavorited: boolean) => void;
  /** RF-05: pin/unpin action. Only provided for channels; absent for DMs. */
  onTogglePin?: (messageId: string, pin: boolean) => void;
  /** RF-05: whether this message is currently pinned in the channel. */
  isPinned?: boolean;
  allowedReactionEmojis: string[];
  recentReactionEmojis: string[];
  reactionMenuVisible: boolean;
  onReactionMenuVisibleChange: (messageId: string, visible: boolean) => void;
  pickerOpen: boolean;
  onPickerOpenChange: (messageId: string, open: boolean) => void;
}

function MessageReactions({
  message,
  isMine = false,
  bubbleRef,
  onToggleReaction,
  onToggleFavorite,
  onTogglePin,
  isPinned = false,
  allowedReactionEmojis,
  recentReactionEmojis,
  reactionMenuVisible,
  pickerOpen,
  onPickerOpenChange,
}: Pick<
  MessageBubbleProps,
  | "message"
  | "isMine"
  | "onToggleReaction"
  | "onToggleFavorite"
  | "onTogglePin"
  | "isPinned"
  | "allowedReactionEmojis"
  | "recentReactionEmojis"
  | "reactionMenuVisible"
  | "pickerOpen"
  | "onPickerOpenChange"
> & { bubbleRef: RefObject<HTMLDivElement | null> }) {
  const menuRef = useRef<HTMLDivElement>(null);
  const anchorRef = useRef<HTMLButtonElement>(null);
  const pickerRef = useRef<HTMLDivElement>(null);

  const positionMenu = useCallback(() => {
    if (!reactionMenuVisible || !bubbleRef.current || !menuRef.current) return;
    const bubble = bubbleRef.current.getBoundingClientRect();
    if (bubble.bottom < 0 || bubble.top > window.innerHeight) return;
    const menu = menuRef.current.getBoundingClientRect();
    const gap = 6;
    const viewportPadding = 8;
    const midX = bubble.left + bubble.width / 2;
    const left = Math.min(
      Math.max(viewportPadding, isMine ? midX - menu.width : midX),
      window.innerWidth - menu.width - viewportPadding,
    );
    const above = bubble.top - menu.height - gap;
    const top =
      above >= viewportPadding
        ? above
        : Math.min(bubble.bottom + gap, window.innerHeight - menu.height - viewportPadding);
    menuRef.current.style.left = `${left}px`;
    menuRef.current.style.top = `${top}px`;
    menuRef.current.style.visibility = "visible";
  }, [bubbleRef, isMine, reactionMenuVisible]);

  useLayoutEffect(positionMenu, [positionMenu]);

  useEffect(() => {
    if (!reactionMenuVisible) return;
    document.addEventListener("scroll", positionMenu, true);
    return () => document.removeEventListener("scroll", positionMenu, true);
  }, [positionMenu, reactionMenuVisible]);

  const positionPicker = useCallback(() => {
    if (!pickerOpen || !anchorRef.current || !pickerRef.current) return;
    const anchor = anchorRef.current.getBoundingClientRect();
    if (anchor.bottom < 0 || anchor.top > window.innerHeight) {
      onPickerOpenChange(message.id, false);
      return;
    }
    const picker = pickerRef.current.getBoundingClientRect();
    const gap = 7;
    const viewportPadding = 8;
    const left = Math.min(
      Math.max(viewportPadding, anchor.right - picker.width),
      window.innerWidth - picker.width - viewportPadding,
    );
    const above = anchor.top - picker.height - gap;
    const top =
      above >= viewportPadding
        ? above
        : Math.min(anchor.bottom + gap, window.innerHeight - picker.height - viewportPadding);
    pickerRef.current.style.left = `${left}px`;
    pickerRef.current.style.top = `${top}px`;
    pickerRef.current.style.visibility = "visible";
  }, [message.id, onPickerOpenChange, pickerOpen]);

  useLayoutEffect(positionPicker, [positionPicker]);

  useEffect(() => {
    if (!pickerOpen) return;
    const closeOnOutsideClick = (event: MouseEvent) => {
      const target = event.target as Node;
      if (!menuRef.current?.contains(target) && !pickerRef.current?.contains(target)) {
        onPickerOpenChange(message.id, false);
      }
    };
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === "Escape") onPickerOpenChange(message.id, false);
    };
    document.addEventListener("mousedown", closeOnOutsideClick);
    document.addEventListener("keydown", closeOnEscape);
    document.addEventListener("scroll", positionPicker, true);
    return () => {
      document.removeEventListener("mousedown", closeOnOutsideClick);
      document.removeEventListener("keydown", closeOnEscape);
      document.removeEventListener("scroll", positionPicker, true);
    };
  }, [message.id, onPickerOpenChange, pickerOpen, positionPicker]);

  const selectReaction = (emoji: string) => {
    onToggleReaction(message.id, emoji);
    onPickerOpenChange(message.id, false);
  };

  if (message.isRemoved) return null;
  return (
    <>
      {message.reactions.length > 0 && (
        <div className="chat-msg-area__reactions" aria-label="Reações da mensagem">
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
      )}
      {reactionMenuVisible &&
        createPortal(
          <div
            ref={menuRef}
            className="chat-msg-area__reaction-menu"
            role="toolbar"
            aria-label="Reagir à mensagem"
            style={{ visibility: "hidden" }}
          >
            {recentReactionEmojis.map((emoji) => (
              <button
                key={emoji}
                type="button"
                aria-label={`Reagir rapidamente com ${emoji}`}
                onClick={() => onToggleReaction(message.id, emoji)}
              >
                {emoji}
              </button>
            ))}
            <button
              type="button"
              className={message.isFavorited ? "chat-msg-area__favorite--active" : undefined}
              aria-label={message.isFavorited ? "Remover dos favoritos" : "Favoritar mensagem"}
              aria-pressed={message.isFavorited}
              onClick={() => onToggleFavorite(message.id, !message.isFavorited)}
            >
              <span className="material-symbols-outlined" aria-hidden="true">
                star
              </span>
            </button>
            {onTogglePin && (
              <button
                type="button"
                className={isPinned ? "chat-msg-area__pin--active" : undefined}
                aria-label={isPinned ? "Desafixar mensagem" : "Fixar mensagem no canal"}
                aria-pressed={isPinned}
                onClick={() => onTogglePin(message.id, !isPinned)}
              >
                <span className="material-symbols-outlined" aria-hidden="true">
                  keep
                </span>
              </button>
            )}
            <button
              ref={anchorRef}
              type="button"
              aria-label="Mais reações"
              aria-expanded={pickerOpen}
              aria-haspopup="dialog"
              disabled={allowedReactionEmojis.length === 0}
              onClick={() => onPickerOpenChange(message.id, !pickerOpen)}
            >
              <span className="material-symbols-outlined" aria-hidden="true">
                add_reaction
              </span>
            </button>
            {pickerOpen &&
              createPortal(
                <div
                  ref={pickerRef}
                  className="chat-msg-area__reaction-grid"
                  role="dialog"
                  aria-label="Escolher reação"
                  style={{ visibility: "hidden" }}
                >
                  {allowedReactionEmojis.map((emoji) => (
                    <button
                      key={emoji}
                      type="button"
                      aria-label={`Reagir com ${emoji}`}
                      onClick={() => selectReaction(emoji)}
                    >
                      {emoji}
                    </button>
                  ))}
                </div>,
                document.body,
              )}
          </div>,
          document.body,
        )}
    </>
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
  onToggleFavorite,
  onTogglePin,
  isPinned = false,
  allowedReactionEmojis,
  recentReactionEmojis,
  reactionMenuVisible,
  onReactionMenuVisibleChange,
  pickerOpen,
  onPickerOpenChange,
}: MessageBubbleProps) {
  const bubbleRef = useRef<HTMLDivElement>(null);
  return (
    <div
      className={`chat-msg-area__msg${isMine ? " chat-msg-area__msg--mine" : ""}${isGrouped ? " chat-msg-area__msg--grouped" : ""}`}
      data-testid="chat-msg-bubble"
      tabIndex={0}
      onMouseEnter={() => onReactionMenuVisibleChange(message.id, true)}
      onMouseLeave={() => onReactionMenuVisibleChange(message.id, false)}
      onFocus={() => onReactionMenuVisibleChange(message.id, true)}
      onBlur={(event) => {
        if (!event.currentTarget.contains(event.relatedTarget)) {
          onReactionMenuVisibleChange(message.id, false);
        }
      }}
      onTouchStart={() => onReactionMenuVisibleChange(message.id, true)}
    >
      {!isMine && (
        <div className="chat-msg-area__msg-avatar" aria-hidden="true">
          {senderInitials(message)}
        </div>
      )}
      <div className="chat-msg-area__msg-body">
        <MessageMeta message={message} isMine={isMine} isGrouped={isGrouped} />
        <div
          ref={bubbleRef}
          className={`chat-msg-area__msg-bubble${message.isRemoved ? " chat-msg-area__msg-bubble--removed" : ""}`}
        >
          {message.isRemoved ? (
            "Mensagem removida."
          ) : (
            <RichTextRenderer text={message.bodyText} bodyFormat={message.bodyFormat} />
          )}
        </div>
        <MessageReactions
          message={message}
          isMine={isMine}
          bubbleRef={bubbleRef}
          onToggleReaction={onToggleReaction}
          onToggleFavorite={onToggleFavorite}
          onTogglePin={onTogglePin}
          isPinned={isPinned}
          allowedReactionEmojis={allowedReactionEmojis}
          recentReactionEmojis={recentReactionEmojis}
          reactionMenuVisible={reactionMenuVisible || pickerOpen}
          pickerOpen={pickerOpen}
          onPickerOpenChange={onPickerOpenChange}
        />
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
  onToggleFavorite: (messageId: string, isFavorited: boolean) => void;
  /** RF-05: pin/unpin action. Only provided for channels; absent for DMs. */
  onTogglePin?: (messageId: string, pin: boolean) => void;
  /** RF-05: set of currently-pinned message IDs in this channel. */
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
  onToggleFavorite,
  onTogglePin,
  pinnedIds,
  allowedReactionEmojis,
  recentReactionEmojis,
}: MessageListProps) {
  const listRef = useRef<HTMLDivElement>(null);
  const bottomRef = useRef<HTMLDivElement>(null);
  const topSentinelRef = useRef<HTMLDivElement>(null);
  const [openPickerMessageId, setOpenPickerMessageId] = useState<string | null>(null);
  const [hoveredMessageId, setHoveredMessageId] = useState<string | null>(null);
  const handlePickerOpenChange = useCallback((messageId: string, open: boolean) => {
    setOpenPickerMessageId(open ? messageId : null);
  }, []);
  const handleReactionMenuVisibleChange = useCallback(
    (messageId: string, visible: boolean) => {
      if (openPickerMessageId && openPickerMessageId !== messageId) return;
      setHoveredMessageId(visible ? messageId : null);
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
            onToggleFavorite={onToggleFavorite}
            onTogglePin={onTogglePin}
            isPinned={pinnedIds?.has(item.message.id) ?? false}
            allowedReactionEmojis={allowedReactionEmojis}
            recentReactionEmojis={recentReactionEmojis}
            reactionMenuVisible={hoveredMessageId === item.message.id}
            onReactionMenuVisibleChange={handleReactionMenuVisibleChange}
            pickerOpen={openPickerMessageId === item.message.id}
            onPickerOpenChange={handlePickerOpenChange}
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

  // RF-05: pins are per-channel; DMs pass an empty id and usePins stays idle.
  const pinChannelId = kind === "channel" ? targetId : "";
  const { pins, pinnedIds, error: pinError, togglePin, reload: reloadPins } = usePins(pinChannelId);

  const { state, sendMessage, retry, loadMore, toggleReaction, toggleFavorite } = useMessages({
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

  return (
    <div className="chat-msg-area" data-testid="chat-message-area">
      {kind === "channel" ? (
        <HeaderChannel name={resolvedName} />
      ) : (
        <HeaderDM name={resolvedName} />
      )}

      {kind === "channel" && <PinnedBar pins={pins} onUnpin={togglePin} />}

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
          onToggleFavorite={toggleFavorite}
          onTogglePin={kind === "channel" ? togglePin : undefined}
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
        onSend={handleSend}
      />
    </div>
  );
}
