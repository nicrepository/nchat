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
 * - ChatComposer is keyed by `${kind}:${targetId}`, so an unsent draft can never
 *   survive a target switch and be posted to the wrong conversation.
 *
 * WebSocket realtime delivery:
 * Implemented — see useMessages and useChatWebSocket.
 * Auth uses Sec-WebSocket-Protocol to pass the Bearer token (browser WebSocket
 * upgrade cannot set custom headers; token-in-URL is rejected server-side).
 */

import {
  forwardRef,
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { createPortal } from "react-dom";
import { useLocation, useNavigate, useOutletContext, useParams } from "react-router";

import "./ChatMessageArea.css";
import type { ChatOutletContext } from "./ChatShell";
import { normalizeChatTargetId } from "./chatTargetId";
import type { DMCounterpart, Message, PinnedItem } from "./chatTypes";
import { fetchAllowedReactionEmojis, fetchChannelMessage, fetchDMMessage } from "./chatApi";
import { useMessages, type LastMutation, type SendResult } from "./useMessages";
import { usePins } from "./usePins";
import { selectLatestPin } from "./selectLatestPin";
import { useConversationDetails } from "./useConversationDetails";
import ConversationDetailsPanel from "./ConversationDetailsPanel";
import { conversationDetailsPanelId } from "./conversationDetailsDisplay";
import ChatComposer, { type PendingReferencePreview } from "./ChatComposer";
import ForwardMessageDialog, { type ForwardSourceContext } from "./ForwardMessageDialog";
import MessageBubble, { type MessageBubbleProps } from "./MessageBubble";
import {
  avatarColorFor,
  formatDayLabel,
  formatTime,
  initialsFrom,
  senderLabel,
} from "./messageDisplay";

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

interface PendingReferenceLocation {
  messageId: string;
  targetKind: "channel" | "dm";
  targetId: string;
}

const UUID_PATTERN = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const NIL_UUID = "00000000-0000-0000-0000-000000000000";

function isValidUUID(value: unknown): value is string {
  return typeof value === "string" && UUID_PATTERN.test(value) && value.toLowerCase() !== NIL_UUID;
}

function readPendingReference(state: unknown): PendingReferenceLocation | null {
  if (typeof state !== "object" || state === null) return null;
  const value = state as Record<string, unknown>;
  if (
    !isValidUUID(value.referencedMessageId) ||
    !isValidUUID(value.referenceTargetId) ||
    (value.referenceTargetKind !== "channel" && value.referenceTargetKind !== "dm")
  ) {
    return null;
  }
  return {
    messageId: value.referencedMessageId,
    targetKind: value.referenceTargetKind,
    targetId: value.referenceTargetId,
  };
}

function quoteAuthorLabel(
  quote: NonNullable<Message["quoted"]>,
  messagesById: Map<string, Message>,
) {
  const parent = messagesById.get(quote.id);
  return parent ? senderLabel(parent) : "Usuário desconhecido";
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

interface DetailsToggleProps {
  open: boolean;
  /**
   * Names the control after what it opens: a channel's details, a group's
   * details, or a named person's profile.
   */
  label: string;
  onToggle: () => void;
}

/**
 * The details toggle.
 *
 * A real <button>, so it is reachable and operable by keyboard for free.
 * aria-expanded carries the state and aria-controls points at the panel, which
 * is why each panel needs a stable id rather than a generated one. The ref is
 * what lets the panel hand focus back here when it closes itself.
 *
 * The two variants differ only in their accessible name and the id they point
 * at — the affordance, the icon and the keyboard behaviour are identical, so
 * one component with a discriminator beats two that would drift apart.
 */
const DetailsToggle = forwardRef<HTMLButtonElement, DetailsToggleProps>(function DetailsToggle(
  { open, label, onToggle },
  ref,
) {
  return (
    <div className="chat-msg-area__header-actions">
      <button
        ref={ref}
        type="button"
        className="chat-msg-area__header-btn"
        aria-label={label}
        aria-expanded={open}
        aria-controls={conversationDetailsPanelId}
        onClick={onToggle}
        data-testid="chat-details-toggle"
      >
        <span className="material-symbols-outlined" aria-hidden="true">
          info
        </span>
      </button>
    </div>
  );
});

interface HeaderChannelProps {
  name: string;
  detailsToggle?: React.ReactNode;
}

function HeaderChannel({ name, detailsToggle }: HeaderChannelProps) {
  return (
    <header className="chat-msg-area__header" data-testid="chat-msg-header">
      <span className="chat-msg-area__header-icon" aria-hidden="true">
        <IconHash />
      </span>
      <h1 className="chat-msg-area__header-title">{name}</h1>
      {detailsToggle}
    </header>
  );
}

interface HeaderDMProps {
  name: string;
  /** Same structured counterpart the sidebar uses — never a second request. */
  counterpart?: DMCounterpart;
  onStartCall?: (targetUserId: string, callType: "audio" | "video") => boolean;
  /** A group opens its details, a 1:1 DM opens the other person's profile. */
  detailsToggle?: React.ReactNode;
}

export function HeaderDM({ name, counterpart, onStartCall, detailsToggle }: HeaderDMProps) {
  const src = counterpart?.avatarUrl;
  // A load failure is scoped to the URL that was current when it happened, so a
  // change of src must clear it — otherwise navigating A → B → A would never
  // retry A. This uses React's "adjust state when a prop changes" pattern (reset
  // during render, guarded so it runs ONLY when src actually changes, never every
  // render); an effect would trip react-hooks/set-state-in-effect. An unchanged
  // src that keeps failing stays on the initials fallback.
  const [failedSrc, setFailedSrc] = useState<string | null>(null);
  const [trackedSrc, setTrackedSrc] = useState(src);
  if (src !== trackedSrc) {
    setTrackedSrc(src);
    setFailedSrc(null);
  }
  const showImage = Boolean(src) && failedSrc !== src;
  // Same deterministic colour the sidebar uses for this person, so the initials
  // fallback matches across both surfaces. Keyed on the counterpart user id
  // (stable per person); legacy DMs without a counterpart fall back to the name.
  const color = avatarColorFor(counterpart?.userId ?? name);

  return (
    <header className="chat-msg-area__header" data-testid="chat-msg-header">
      <div
        className={`chat-msg-area__header-avatar chat-msg-area__header-avatar--${color}`}
        aria-hidden="true"
      >
        {showImage ? (
          <img
            className="chat-msg-area__header-avatar-img"
            src={src}
            alt=""
            referrerPolicy="no-referrer"
            onError={() => setFailedSrc(src ?? null)}
          />
        ) : (
          initialsFrom(counterpart?.displayName ?? name)
        )}
      </div>
      <h1 className="chat-msg-area__header-title">{name}</h1>
      {counterpart && onStartCall && (
        <div className="chat-msg-area__call-actions" aria-label="Iniciar chamada">
          <button
            type="button"
            aria-label="Iniciar chamada de áudio"
            onClick={() => onStartCall(counterpart.userId, "audio")}
          >
            Áudio
          </button>
          <button
            type="button"
            aria-label="Iniciar chamada de vídeo"
            onClick={() => onStartCall(counterpart.userId, "video")}
          >
            Vídeo
          </button>
        </div>
      )}
      {detailsToggle}
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
  onReferenceMessage: (message: Message) => void;
  onForwardMessage?: (message: Message) => void;
  onReferenceJump: (reference: NonNullable<Message["reference"]>) => void;
  onToggleFavorite: (messageId: string, isFavorited: boolean) => void;
  onEditMessage: MessageBubbleProps["onEditMessage"];
  onEditForbidden: MessageBubbleProps["onEditForbidden"];
  onDeleteMessage: MessageBubbleProps["onDeleteMessage"];
  editDisabledIds: Set<string>;
  channelId?: string;
  /** RF-05: pin/unpin action for readable channels and DMs. */
  onTogglePin?: (messageId: string, pin: boolean) => void;
  /** RF-05: set of currently-pinned message IDs in this target. */
  pinnedIds?: Set<string>;
  allowedReactionEmojis: string[];
  recentReactionEmojis: string[];
  focusMessageId?: string;
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
  onReferenceMessage,
  onForwardMessage,
  onReferenceJump,
  onToggleFavorite,
  onEditMessage,
  onEditForbidden,
  onDeleteMessage,
  editDisabledIds,
  channelId,
  onTogglePin,
  pinnedIds,
  allowedReactionEmojis,
  recentReactionEmojis,
  focusMessageId,
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
  const focusedMessageRef = useRef("");
  useEffect(() => {
    if (!focusMessageId) {
      focusedMessageRef.current = "";
      return;
    }
    if (focusedMessageRef.current === focusMessageId) return;
    const el = messageRefs.current.get(focusMessageId);
    if (!el) return;
    focusedMessageRef.current = focusMessageId;
    handleQuoteJump(focusMessageId);
  }, [focusMessageId, handleQuoteJump, messages]);
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
    const day = formatDayLabel(msg.createdAt);
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
            onReferenceMessage={onReferenceMessage}
            onForwardMessage={onForwardMessage}
            onToggleFavorite={onToggleFavorite}
            onEditMessage={onEditMessage}
            onEditForbidden={onEditForbidden}
            onDeleteMessage={onDeleteMessage}
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
            onReferenceJump={onReferenceJump}
            isHighlighted={highlightedMessageId === item.message.id}
            setMessageRef={setMessageRef}
          />
        ),
      )}
      <div ref={bottomRef} />
    </div>
  );
}

function ReferenceDestinationDialog({
  current,
  channels,
  dms,
  onClose,
  onSelect,
}: {
  current: { kind: "channel" | "dm"; id: string };
  channels: ChatOutletContext["channels"];
  dms: ChatOutletContext["dms"];
  onClose: () => void;
  onSelect: (target: { kind: "channel" | "dm"; id: string }) => void;
}) {
  const dialogRef = useRef<HTMLDivElement>(null);
  const closeButtonRef = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    const previouslyFocused = document.activeElement as HTMLElement;
    closeButtonRef.current!.focus();
    const handleDialogKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        onClose();
        return;
      }
      if (event.key !== "Tab") return;
      const focusable = dialogRef.current!.querySelectorAll<HTMLButtonElement>("button");
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };
    document.addEventListener("keydown", handleDialogKey);
    return () => {
      document.removeEventListener("keydown", handleDialogKey);
      previouslyFocused.focus();
    };
  }, [onClose]);
  const targets = [
    ...channels.map((channel) => ({
      kind: "channel" as const,
      id: channel.id,
      name: channel.name,
    })),
    ...dms.map((dm) => ({ kind: "dm" as const, id: dm.id, name: dm.name })),
  ].filter((target) => target.kind !== current.kind || target.id !== current.id);

  return createPortal(
    <div className="chat-reference-dialog__backdrop" onMouseDown={onClose}>
      <div
        ref={dialogRef}
        className="chat-reference-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="chat-reference-dialog-title"
        onMouseDown={(event) => event.stopPropagation()}
      >
        <header>
          <div>
            <h2 id="chat-reference-dialog-title">Citar em outra conversa</h2>
            <p>Escolha onde a nova mensagem será enviada.</p>
          </div>
          <button ref={closeButtonRef} type="button" aria-label="Fechar" onClick={onClose}>
            <span className="material-symbols-outlined" aria-hidden="true">
              close
            </span>
          </button>
        </header>
        {targets.length === 0 ? (
          <p className="chat-reference-dialog__empty">Nenhum outro destino disponível.</p>
        ) : (
          <ul aria-label="Destinos disponíveis">
            {targets.map((target) => (
              <li key={`${target.kind}:${target.id}`}>
                <button type="button" onClick={() => onSelect(target)}>
                  <span className="material-symbols-outlined" aria-hidden="true">
                    {target.kind === "channel" ? "tag" : "forum"}
                  </span>
                  {target.name}
                </button>
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>,
    document.body,
  );
}

// ── Pinned messages bar (RF-05) ──────────────────────────────────────────────

interface PinnedBarProps {
  /**
   * The one pin selectLatestPin chose, not the list. The details panel receives
   * the same object, so "the bar and the panel show the same message" holds by
   * construction instead of by two components agreeing on a rule.
   */
  pin: PinnedItem | null;
  onUnpin: (messageId: string, pin: boolean) => void;
}

function PinnedBar({ pin, onUnpin }: PinnedBarProps) {
  if (pin === null) return null;
  return (
    <section className="chat-msg-area__pins" aria-label="Mensagem fixada" data-testid="chat-pins">
      <div className="chat-msg-area__pins-item">
        <span className="material-symbols-outlined" aria-hidden="true">
          keep
        </span>
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
      </div>
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
  const targetId = normalizeChatTargetId(safeDecodeURIComponent(rawId));
  const location = useLocation();
  const navigate = useNavigate();
  const focusMessageId = new URLSearchParams(location.search).get("message") ?? "";
  const [referenceSource, setReferenceSource] = useState<Message | null>(null);
  const [forwardSource, setForwardSource] = useState<ForwardSourceContext | null>(null);
  const pendingReference = useMemo(() => readPendingReference(location.state), [location.state]);
  const pendingReferenceId = pendingReference?.messageId ?? "";
  const pendingReferenceKey = pendingReference
    ? `${pendingReference.targetKind}:${pendingReference.targetId}:${pendingReference.messageId}`
    : "";
  const [referenceResolution, setReferenceResolution] = useState<{
    key: string;
    preview: Extract<PendingReferencePreview, { status: "available" | "unavailable" }>;
  } | null>(null);
  const referencePreview = useMemo<PendingReferencePreview>(() => {
    if (!pendingReference) return { status: "idle" };
    return referenceResolution?.key === pendingReferenceKey
      ? referenceResolution.preview
      : { status: "loading", messageId: pendingReference.messageId };
  }, [pendingReference, pendingReferenceKey, referenceResolution]);

  const ctx = useOutletContext<ChatOutletContext>() ?? { currentUserId: "", channels: [], dms: [] };

  // The sidebar payload already carries the counterpart identity, so the header
  // reads it from the outlet context instead of issuing a per-DM request.
  const activeDM = kind === "dm" ? ctx.dms.find((dm) => dm.id === targetId) : undefined;
  const resolvedName =
    kind === "channel"
      ? (ctx.channels.find((ch) => ch.id === targetId)?.name ?? targetId)
      : (activeDM?.name ?? targetId);
  const referenceTargetLabel = useMemo(() => {
    if (pendingReference?.targetKind === "channel") {
      const name = ctx.channels.find((channel) => channel.id === pendingReference.targetId)?.name;
      return name ? `#${name}` : "Canal";
    }
    if (pendingReference?.targetKind === "dm") {
      return ctx.dms.find((dm) => dm.id === pendingReference.targetId)?.name ?? "Conversa";
    }
    return "Conversa";
  }, [ctx.channels, ctx.dms, pendingReference]);

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

  // One selector, one list, one result: the bar above the conversation and the
  // details panel are handed the same object, so a pin/unpin updates both at
  // once and neither can show a message the other does not.
  const latestPin = useMemo(() => selectLatestPin(pins), [pins]);

  // Panel state lives here, in the component that owns the conversation, and
  // not in the message list or a global store. This component is not remounted
  // when messages change or when the route parameter changes, so toggling the
  // panel cannot restart useMessages, the WebSocket subscription, the composer
  // or the scroll position — and the panel stays open across a channel switch.
  const [detailsOpen, setDetailsOpen] = useState(false);
  const detailsToggleRef = useRef<HTMLButtonElement>(null);
  // Which aggregate the panel would describe, from the domain discriminant:
  // chat.channels for a channel, and a chat.dm_conversations row of type
  // 'group' or 'direct' for a conversation. Only an unknown target resolves to
  // null.
  //
  // activeDM.type is the server's own `direct`/`group` value carried by the
  // sidebar payload, never the participant count or the conversation's name: a
  // group that happens to have two people is still a group, and a 1:1 DM whose
  // title looks like a group's is still a 1:1.
  //
  // The same discriminant decides whether the panel offers "Adicionar membros"
  // (issue #398): a channel and a group do, a 1:1 does not — adding a third
  // person would convert a direct conversation into a group, which that issue
  // deliberately does not do.
  const detailsKind: "channel" | "group" | "direct" | null =
    targetId === ""
      ? null
      : kind === "channel"
        ? "channel"
        : activeDM === undefined
          ? null
          : activeDM.type === "group"
            ? "group"
            : "direct";
  const supportsDetails = detailsKind !== null;
  const detailsTarget = useMemo(
    () => (detailsKind && detailsOpen ? { kind: detailsKind, id: targetId } : null),
    [detailsKind, detailsOpen, targetId],
  );
  const detailsState = useConversationDetails(detailsTarget);

  // members.added names nobody, so the only correct response is to refetch the
  // open panel (issue #398) — and it is the same call the local add makes, so
  // the HTTP response and the event converge on one view of the roster instead
  // of two. A closed panel has no target and so refetches nothing.
  const reloadOpenDetails = detailsState.reload;

  const closeDetails = useCallback(() => {
    setDetailsOpen(false);
    // Focus returns to the control that opened the panel, but only if it is
    // still mounted — after a channel switch to a DM it is not, and forcing
    // focus onto a detached node would drop it to <body> instead.
    detailsToggleRef.current?.focus();
  }, []);
  const toggleDetails = useCallback(() => setDetailsOpen((open) => !open), []);

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
    deleteMessageLocal,
  } = useMessages({
    kind,
    targetId,
    currentUserId: ctx.currentUserId,
    focusMessageId,
    onOwnReactionConfirmed: rememberReaction,
    onPinUpdated: reloadPins,
    // Someone added participants to the open conversation (issue #398). The
    // event names nobody, so the only correct response is to refetch — which is
    // also the same call the local add makes, so the two converge instead of
    // producing two different views of the roster.
    //
    // Passed directly: useMessages holds this callback in a ref, so a new
    // identity each render does not restart the socket or its subscriptions.
    onMembersAdded: reloadOpenDetails,
    // An attachment's malware verdict landed (RF-22). The same treatment as
    // members.added and for the same reason: the event says which row changed,
    // not what the list should now look like, so the panel refetches and the
    // server stays the single authority on every attachment's status.
    //
    // Refetching rather than patching is also what makes the event and the
    // panel's own reconciliation poll safe together. Both end in one
    // `files_ready` carrying a whole list, so two of them arriving at once
    // cannot duplicate a row, and neither can write a status the server did not
    // just report. A missed event costs nothing: the poll, a reopen, or a
    // reload all recover the current state, because the persisted status is the
    // source of truth and this is only a hint that it moved.
    onAttachmentStatus: reloadOpenDetails,
    onMessageRemoved: reloadPins,
  });

  useEffect(() => {
    if (!pendingReference) return;
    const { messageId, targetKind, targetId: sourceTargetId } = pendingReference;

    const controller = new AbortController();
    const request =
      targetKind === "channel"
        ? fetchChannelMessage(sourceTargetId, messageId, controller.signal)
        : fetchDMMessage(sourceTargetId, messageId, controller.signal);
    request.then(
      (message) => {
        if (controller.signal.aborted) return;
        setReferenceResolution({
          key: pendingReferenceKey,
          preview:
            message.id === messageId && !message.isRemoved
              ? { status: "available", messageId, message }
              : { status: "unavailable", messageId },
        });
      },
      () => {
        if (!controller.signal.aborted) {
          setReferenceResolution({
            key: pendingReferenceKey,
            preview: { status: "unavailable", messageId },
          });
        }
      },
    );
    return () => controller.abort();
  }, [pendingReference, pendingReferenceKey]);

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
    async (body: string, attachmentIds?: string[]): Promise<SendResult> => {
      const result = await sendMessage(body, pendingReferenceId || undefined, attachmentIds);
      if (result.status === "sent") {
        navigate(`${location.pathname}${location.search}`, { replace: true, state: null });
      }
      return result;
    },
    [location.pathname, location.search, navigate, pendingReferenceId, sendMessage],
  );

  const selectReferenceDestination = useCallback(
    (target: { kind: "channel" | "dm"; id: string }) => {
      if (!referenceSource) return;
      const sourceID = referenceSource.id;
      setReferenceSource(null);
      navigate(`/chat/${target.kind}/${encodeURIComponent(target.id)}`, {
        state: {
          referencedMessageId: sourceID,
          referenceTargetKind: kind,
          referenceTargetId: targetId,
        },
      });
    },
    [kind, navigate, referenceSource, targetId],
  );

  const jumpToReference = useCallback(
    (reference: NonNullable<Message["reference"]>) => {
      if (!reference.available) return;
      navigate(
        `/chat/${reference.targetType}/${encodeURIComponent(reference.targetId)}?message=${encodeURIComponent(reference.messageId)}`,
      );
    },
    [navigate],
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
  const closeForwardDialog = useCallback(() => setForwardSource(null), []);
  const selectForwardSource = useCallback(
    (message: Message) => {
      if (kind === "channel" && targetId) {
        setForwardSource({ messageID: message.id, sourceChannelID: targetId });
      }
    },
    [kind, targetId],
  );

  const showDetails = supportsDetails && detailsOpen;

  // A group's control names the panel; a 1:1's names the person it is about,
  // because "Detalhes da conversa" would be an odd thing to hear announced for
  // a panel that shows one profile. The name comes from the sidebar payload the
  // header already renders, so the label never waits on a request; when the
  // counterpart could not be resolved it degrades to the conversation itself
  // rather than to a blank or an ID.
  const detailsToggleLabel =
    detailsKind === "direct"
      ? activeDM?.counterpart?.displayName
        ? `Abrir perfil de ${activeDM.counterpart.displayName}`
        : "Abrir perfil da conversa"
      : "Detalhes do grupo";

  return (
    <div
      className={`chat-msg-area${showDetails ? " chat-msg-area--with-details" : ""}`}
      data-testid="chat-message-area"
    >
      {/*
        The conversation column is always rendered, never conditionally, so the
        panel below is a trailing sibling: adding or removing it leaves this
        entire subtree — message list, composer, scroll container — reconciled in
        place rather than remounted.
      */}
      <div className="chat-msg-area__conversation">
        {kind === "channel" ? (
          <HeaderChannel
            name={resolvedName}
            detailsToggle={
              supportsDetails ? (
                <DetailsToggle
                  ref={detailsToggleRef}
                  open={detailsOpen}
                  label="Detalhes do canal"
                  onToggle={toggleDetails}
                />
              ) : undefined
            }
          />
        ) : (
          <HeaderDM
            name={resolvedName}
            counterpart={activeDM?.counterpart}
            onStartCall={ctx.startCall}
            detailsToggle={
              supportsDetails ? (
                <DetailsToggle
                  ref={detailsToggleRef}
                  open={detailsOpen}
                  label={detailsToggleLabel}
                  onToggle={toggleDetails}
                />
              ) : undefined
            }
          />
        )}

        <PinnedBar pin={latestPin} onUnpin={togglePin} />

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
            onReferenceMessage={setReferenceSource}
            onForwardMessage={kind === "channel" ? selectForwardSource : undefined}
            onReferenceJump={jumpToReference}
            onToggleFavorite={toggleFavorite}
            onEditMessage={editMessageLocal}
            onEditForbidden={handleEditForbidden}
            onDeleteMessage={deleteMessageLocal}
            editDisabledIds={editDisabledIds}
            channelId={kind === "channel" ? targetId : undefined}
            onTogglePin={togglePin}
            pinnedIds={pinnedIds}
            allowedReactionEmojis={allowedReactionEmojis}
            recentReactionEmojis={recentReactionEmojis}
            focusMessageId={focusMessageId}
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

        {/*
        The composer is keyed by the conversation identity so switching targets
        destroys the TipTap instance and mounts an empty one. The editor body is
        the only per-target state React Router's in-place route update would
        otherwise carry over — every other piece of state here already resets
        through useMessages/usePins. Without this key, a draft typed in channel A
        stays in the composer for channel B (both are bodyFormat "v3", so
        useEditor keeps the same instance) and the send button would post it to
        the wrong conversation. Drafts are deliberately not persisted.
      */}
        <ChatComposer
          key={`${kind}:${targetId}`}
          channelId={kind === "channel" ? targetId : undefined}
          bodyFormat={kind === "channel" ? "v3" : "v2"}
          placeholder={
            kind === "channel"
              ? `Mensagem para #${resolvedName}…`
              : `Mensagem para ${resolvedName}…`
          }
          disabled={state.status !== "ready"}
          replyPreview={replyPreview}
          onCancelReply={cancelReply}
          referencePreview={referencePreview}
          referenceTargetLabel={referenceTargetLabel}
          onCancelReference={() =>
            navigate(`${location.pathname}${location.search}`, { replace: true, state: null })
          }
          onSend={handleSend}
          // RF-32 (issue #458): the same composer serves channels and DMs, so
          // one target prop covers both. It is the route's own kind and id —
          // the very pair the composer is keyed by — so an attachment can never
          // be posted to the destination the user just navigated away from.
          uploadTarget={targetId ? { kind, id: targetId } : null}
          // An upload adds a file to the destination without creating a message
          // — the message comes later, when the user presses Enviar (RF-32) —
          // so the details panel's file list still has to reconcile here.
          onAttachmentUploaded={reloadOpenDetails}
        />
        {referenceSource && (
          <ReferenceDestinationDialog
            current={{ kind, id: targetId }}
            channels={ctx.channels}
            dms={ctx.dms}
            onClose={() => setReferenceSource(null)}
            onSelect={selectReferenceDestination}
          />
        )}
        {/* Both dialogs render through a portal, so their position here costs the
          conversation column no layout. */}
        {forwardSource && kind === "channel" && (
          <ForwardMessageDialog
            source={forwardSource}
            channels={ctx.channels}
            onClose={closeForwardDialog}
            onSuccess={closeForwardDialog}
          />
        )}
      </div>

      {showDetails && (
        <ConversationDetailsPanel
          kind={detailsKind ?? "channel"}
          state={detailsState}
          currentUserId={ctx.currentUserId}
          latestPin={latestPin}
          onClose={closeDetails}
        />
      )}
    </div>
  );
}
