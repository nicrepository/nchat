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
  type RefObject,
} from "react";
import { createPortal } from "react-dom";
import { useLocation, useNavigate } from "react-router";

import "./ChatMessageArea.css";
import ActiveDirectCallBar, { type ActiveDirectCallBarProps } from "../calls/ActiveDirectCallBar";
import ActiveResourceCallBar from "../calls/ActiveResourceCallBar";
import type { ChatOutletContext } from "./ChatShell";
import type {
  Channel,
  DMConversation,
  DMCounterpart,
  MentionTarget,
  Message,
  PinnedItem,
} from "./chatTypes";
import { fetchAllowedReactionEmojis, getOrCreateDirectDM } from "./chatApi";
import { usePendingReference } from "./usePendingReference";
import { useConversationTarget } from "./useConversationTarget";
import { recentEmojis, type EmojiUsage } from "./emoji/emojiUsage";
import { useEmojiUsage } from "./emoji/useEmojiUsage";
import { useMessages, type LastMutation, type MessagesState, type SendResult } from "./useMessages";
import { useTypingIndicator } from "./useTypingIndicator";
import type { WSTypingUpdatedEvent } from "./useChatWebSocket";
import { usePins } from "./usePins";
import { selectLatestPin } from "./selectLatestPin";
import {
  useConversationDetailsPanel,
  type ConversationDetailsKind,
  type ConversationDetailsPanelState,
} from "./useConversationDetailsPanel";
import { useResourceCallBar } from "./useResourceCallBar";
import ConversationDetailsPanel from "./ConversationDetailsPanel";
import ConversationSystemMessage from "./ConversationSystemMessage.tsx";
import { systemScopeFor, type SystemMessageScope } from "./conversationSystemMessage";
import { conversationDetailsPanelId } from "./conversationDetailsDisplay";
import ChatComposer from "./ChatComposer";
import ForwardMessageDialog, { type ForwardSourceContext } from "./ForwardMessageDialog";
import MessageBubble, { type MessageBubbleProps } from "./MessageBubble";
import PresenceDot from "./PresenceDot";
import { presenceLabel, presenceTargetKey, usePresence, type PresenceState } from "./presence";
import {
  avatarColorFor,
  formatDayLabel,
  formatTime,
  initialsFrom,
  senderLabel,
} from "./messageDisplay";
import {
  findFirstUnreadBoundary,
  formatPendingCount,
  goToBottomAccessibleName,
  isEligibleUnreadMessage,
  isNearBottom,
  MAX_BOUNDARY_SEARCH_PAGES,
  TAIL_EPSILON_PX,
  type ViewportPhase,
} from "./chatViewportState";
import {
  loadViewportAnchor,
  saveViewportAnchor,
  type ViewportAnchor,
} from "./chatViewportPersistence";

// ── Helpers ───────────────────────────────────────────────────────────────────

const quoteHighlightMs = 1_200;
const reactionMenuLeaveDelayMs = 150;
/** How many emoji the row above a message offers before "Mais reações". */
const quickReactionCount = 3;

/**
 * "Fulano está digitando…" for one or two people, an aggregate count beyond
 * that — never one line per typist, which is what would make the area jump
 * around under a busy channel. `null` means nothing to show.
 */
function typingIndicatorText(
  userIds: readonly string[],
  namesByUserId: ReadonlyMap<string, string>,
): string | null {
  if (userIds.length === 0) return null;
  const label = (id: string) => namesByUserId.get(id) ?? "Alguém";
  if (userIds.length === 1) return `${label(userIds[0])} está digitando…`;
  if (userIds.length === 2) return `${label(userIds[0])} e ${label(userIds[1])} estão digitando…`;
  return `${userIds.length} pessoas estão digitando…`;
}

/**
 * The quick-reaction row: what this person actually reaches for, backfilled
 * from the server's curated shortlist so a brand-new account still has one
 * (issue #496).
 *
 * The shortlist is the only server-provided part, and it is a suggestion: what
 * a reaction may be is the catalog's decision, made again on the server for
 * every toggle.
 */
function quickReactionEmojis(usage: EmojiUsage, serverShortlist: string[]): string[] {
  const candidates = [...recentEmojis(usage, quickReactionCount), ...serverShortlist];
  return [...new Set(candidates)].slice(0, quickReactionCount);
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

interface DirectCallActionsProps {
  onAudio: () => void;
  onVideo: () => void;
}

/**
 * RF-23 direct 1:1 call entry: audio and video remain distinct call types,
 * each its own icon button (issue #673) — never consolidated into one
 * control or an intermediate type-picker menu.
 */
function DirectCallActions({ onAudio, onVideo }: DirectCallActionsProps) {
  return (
    <div className="chat-msg-area__call-actions">
      <button
        type="button"
        className="chat-msg-area__header-btn"
        aria-label="Iniciar chamada de áudio"
        onClick={onAudio}
      >
        <span className="material-symbols-outlined" aria-hidden="true">
          call
        </span>
      </button>
      <button
        type="button"
        className="chat-msg-area__header-btn"
        aria-label="Iniciar chamada de vídeo"
        onClick={onVideo}
      >
        <span className="material-symbols-outlined" aria-hidden="true">
          videocam
        </span>
      </button>
    </div>
  );
}

/**
 * A channel/group-DM header's resource-call state (issue #622 round 2,
 * section 14). Discovery ("is there a call") is kept strictly separate from
 * participation ("am I in it").
 *
 * #657: The header only renders the "Chamada" start button.
 * Once a call is active (or we are participating in one), the
 * ActiveResourceCallBar takes over presentation completely, and
 * the header suppresses its own call actions by receiving undefined.
 */
export type ResourceCallHeaderState = { onCall: () => void; disabled?: boolean };

/**
 * RF-24 channel/group entry (issue #540 follow-up). A resource room is one
 * multiparty call, never separate "audio" and "video" rooms, so there is a
 * single action instead of RF-23's two. An icon button (issue #673) rather
 * than the previous "Chamada" text label — the accessible name, disabled
 * state, and onCall action are unchanged.
 */
function ResourceCallAction({ state }: { state: ResourceCallHeaderState }) {
  return (
    <div className="chat-msg-area__call-actions">
      <button
        type="button"
        className="chat-msg-area__header-btn"
        aria-label="Iniciar chamada"
        onClick={state.onCall}
        disabled={Boolean(state.disabled)}
      >
        <span className="material-symbols-outlined" aria-hidden="true">
          call
        </span>
      </button>
    </div>
  );
}

interface HeaderChannelProps {
  name: string;
  detailsToggle?: React.ReactNode;
  /** RF-24/#622: absent only when this header is not showing a resource call at all (never the case for a channel). */
  resourceCall?: ResourceCallHeaderState;
}

export function HeaderChannel({ name, detailsToggle, resourceCall }: HeaderChannelProps) {
  return (
    <header className="chat-msg-area__header" data-testid="chat-msg-header">
      <span className="chat-msg-area__header-icon" aria-hidden="true">
        <IconHash />
      </span>
      <h1 className="chat-msg-area__header-title">{name}</h1>
      {resourceCall && <ResourceCallAction state={resourceCall} />}
      {detailsToggle}
    </header>
  );
}

interface HeaderDMProps {
  name: string;
  /** Same structured counterpart the sidebar uses — never a second request. */
  counterpart?: DMCounterpart;
  onStartCall?: (targetUserId: string, callType: "audio" | "video") => boolean;
  /**
   * RF-24/#622: a group's shared call room state. Always absent for a 1:1
   * (counterpart set) — issue #622 round 2 requires a direct DM keep exactly
   * Áudio/Vídeo and never show resource-call UI.
   */
  resourceCall?: ResourceCallHeaderState;
  /** A group opens its details, a 1:1 DM opens the other person's profile. */
  detailsToggle?: React.ReactNode;
  /** The conversation being read; presence is resolved within it (RF-58). */
  presenceTarget?: string;
}

/**
 * The counterpart's avatar: their picture when it loads, their initials when it
 * does not.
 *
 * A load failure is scoped to the URL that was current when it happened, so a
 * change of src must clear it — otherwise navigating A → B → A would never retry
 * A. This uses React's "adjust state when a prop changes" pattern (reset during
 * render, guarded so it runs ONLY when src actually changes, never every
 * render); an effect would trip react-hooks/set-state-in-effect. An unchanged
 * src that keeps failing stays on the initials fallback.
 */
function HeaderAvatar({
  name,
  counterpart,
  presence,
}: {
  name: string;
  counterpart: DMCounterpart | undefined;
  presence: PresenceState;
}) {
  const src = counterpart?.avatarUrl;
  const [failedSrc, setFailedSrc] = useState<string | null>(null);
  const [trackedSrc, setTrackedSrc] = useState(src);
  if (src !== trackedSrc) {
    setTrackedSrc(src);
    setFailedSrc(null);
  }
  // Same deterministic colour the sidebar uses for this person, so the initials
  // fallback matches across both surfaces. Keyed on the counterpart user id
  // (stable per person); legacy DMs without a counterpart fall back to the name.
  const color = avatarColorFor(counterpart?.userId ?? name);
  return (
    <div
      className={`chat-msg-area__header-avatar chat-msg-area__header-avatar--${color}`}
      aria-hidden="true"
    >
      {Boolean(src) && failedSrc !== src ? (
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
      <PresenceDot state={presence} size="md" />
    </div>
  );
}

export function HeaderDM({
  name,
  counterpart,
  onStartCall,
  resourceCall,
  detailsToggle,
  presenceTarget,
}: HeaderDMProps) {
  const presence = usePresence(counterpart?.userId, presenceTarget);

  return (
    <header className="chat-msg-area__header" data-testid="chat-msg-header">
      <HeaderAvatar name={name} counterpart={counterpart} presence={presence} />
      <h1 className="chat-msg-area__header-title">{name}</h1>
      {/* The header states the status in words as well: this is the surface a
          reader is looking at while writing to this person, so "Ausente" being
          discoverable without hovering a 9px dot is the point. */}
      {presence !== "unknown" && (
        <span
          className={`chat-msg-area__header-presence chat-msg-area__header-presence--${presence}`}
          data-testid="chat-msg-header-presence"
        >
          {presenceLabel(presence)}
        </span>
      )}
      {counterpart && onStartCall && (
        <DirectCallActions
          onAudio={() => onStartCall(counterpart.userId, "audio")}
          onVideo={() => onStartCall(counterpart.userId, "video")}
        />
      )}
      {!counterpart && resourceCall && <ResourceCallAction state={resourceCall} />}
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

/**
 * Scrolls the timeline to its newest message with an explicit behavior —
 * never a default, so every call site states whether this is an instant
 * positioning (open, restore, first-unread) or an animated one (explicit user
 * action / own-send). #492: smooth scrolling must never happen just because a
 * conversation opened.
 *
 * The capability check is for jsdom, which implements no layout and therefore no
 * scrollIntoView; a test asserting on message order must not fail on it.
 */
function scrollToBottom(
  bottomRef: RefObject<HTMLDivElement | null>,
  behavior: ScrollBehavior,
): void {
  if (typeof bottomRef.current?.scrollIntoView === "function") {
    bottomRef.current.scrollIntoView({ behavior });
  }
}

/** #492: reduced motion always wins over an explicit animated scroll. */
function prefersReducedMotion(): boolean {
  return (
    typeof window.matchMedia === "function" &&
    window.matchMedia("(prefers-reduced-motion: reduce)").matches
  );
}

/** The behavior an explicit user-triggered scroll (button, own-send) should use. */
function explicitScrollBehavior(): ScrollBehavior {
  return prefersReducedMotion() ? "auto" : "smooth";
}

/**
 * The floating "Ir para o final" action (#492). A real <button>, reachable
 * and Enter/Space-operable for free — no bespoke keyboard handling needed.
 * The accessible name alone carries the pending count so a screen reader
 * announces it as part of the control's name rather than as a live region
 * that would fire on every increment.
 */
function ScrollToBottomButton({
  visible,
  pendingCount,
  onClick,
}: {
  visible: boolean;
  pendingCount: number;
  onClick: () => void;
}) {
  if (!visible) return null;
  return (
    <button
      type="button"
      className="chat-msg-area__scroll-bottom-btn"
      aria-label={goToBottomAccessibleName(pendingCount)}
      title="Ir para o final"
      onClick={onClick}
    >
      <span className="material-symbols-outlined" aria-hidden="true">
        arrow_downward
      </span>
      {pendingCount > 0 && (
        <span className="chat-msg-area__scroll-bottom-badge" aria-hidden="true">
          {formatPendingCount(pendingCount)}
        </span>
      )}
    </button>
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
  onOpenAuthorDM?: MessageBubbleProps["onOpenAuthorDM"];
  openingAuthorDMIds?: Set<string>;
  onToggleFavorite: (messageId: string, isFavorited: boolean) => void;
  /** RF-21 "Verificar novamente" (issue #135); see MessageBubbleProps. */
  onReconcileLinkSafety?: MessageBubbleProps["onReconcileLinkSafety"];
  onEditMessage: MessageBubbleProps["onEditMessage"];
  onEditForbidden: MessageBubbleProps["onEditForbidden"];
  onDeleteMessage: MessageBubbleProps["onDeleteMessage"];
  editDisabledIds: Set<string>;
  mentionTarget?: MentionTarget;
  /**
   * Whether a system message in this timeline says "canal", "grupo" or
   * "conversa" (issue #527). The kind comes from the conversation record, never
   * from the route or the name.
   */
  systemScope: SystemMessageScope;
  /** The conversation on screen, so a sender's presence is resolved in it. */
  presenceTarget?: string;
  /** RF-05: pin/unpin action for readable channels and DMs. */
  onTogglePin?: (messageId: string, pin: boolean) => void;
  /** RF-05: set of currently-pinned message IDs in this target. */
  pinnedIds?: Set<string>;
  recentReactionEmojis: string[];
  /** Local emoji history and skin tone, owned by this conversation (issue #496). */
  emojiUsage: EmojiUsage;
  onEmojiToneChange: (tone: number) => void;
  focusMessageId?: string;
  /** #492: `${kind}:${targetId}` — keys the per-conversation viewport anchor. */
  conversationKey: string;
  /** #492: the sidebar's unread_count for this target as of opening it. */
  unreadCountAtOpen: number;
  /** #492: the anchor this conversation was left at, if any (own in-memory cache, then sessionStorage). */
  initialAnchor: ViewportAnchor | null;
  /** #492: called on unmount (leaving the conversation) with the current anchor. */
  onCaptureAnchor: (key: string, anchor: ViewportAnchor) => void;
  /** #492: called once the bottom sentinel confirms the real tail was reached. */
  onReachedBottom: () => void;
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
  onOpenAuthorDM,
  openingAuthorDMIds,
  onToggleFavorite,
  onReconcileLinkSafety,
  onEditMessage,
  onEditForbidden,
  onDeleteMessage,
  editDisabledIds,
  mentionTarget,
  systemScope,
  presenceTarget,
  onTogglePin,
  pinnedIds,
  recentReactionEmojis,
  emojiUsage,
  onEmojiToneChange,
  focusMessageId,
  conversationKey,
  unreadCountAtOpen,
  initialAnchor,
  onCaptureAnchor,
  onReachedBottom,
}: MessageListProps) {
  const listRef = useRef<HTMLDivElement>(null);
  // #788: the timeline content wrapper — see the ResizeObserver effect below.
  const contentRef = useRef<HTMLDivElement>(null);
  const bottomRef = useRef<HTMLDivElement>(null);
  const topSentinelRef = useRef<HTMLDivElement>(null);
  const messageRefs = useRef(new Map<string, HTMLDivElement>());
  // #492: the "Novas mensagens" separator sits just above the first unread
  // message in the DOM — scrolling the message itself to the viewport's top
  // edge would push the separator off-screen above it, so AT_FIRST_UNREAD
  // positioning targets this instead.
  const unreadDividerRef = useRef<HTMLDivElement>(null);
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

  // #492 viewport state machine. MessageList remounts on every conversation
  // switch (ConversationTimeline renders LoadingSkeleton while useMessages'
  // status cycles through "loading"), so every ref/state below starts fresh
  // per conversation — no per-target reset logic is needed here, and any
  // in-flight resolution work is torn down for free by unmount.
  //
  // Everything the resolution/badge logic below touches during render is
  // useState, never useRef: this codebase's lint config (react-hooks/refs,
  // react-hooks/set-state-in-effect) forbids both reading/writing a ref during
  // render and calling a state setter synchronously inside an effect body —
  // so "adjust state during render" (see HeaderAvatar's doc comment above for
  // the same technique) is the only compliant way to derive state from
  // already-available props like messages/hasMore/initialAnchor. Refs stay
  // reserved for values touched exclusively inside effects or the async
  // scroll/IntersectionObserver callbacks below, where touching them is fine.
  const [phase, setPhase] = useState<ViewportPhase>("RESTORING_POSITION");
  // phaseRef mirrors `phase` (kept in sync by the effect below) for the two
  // async callbacks (scroll handler, bottom sentinel) that would otherwise
  // close over a stale value — never read/written anywhere else.
  const phaseRef = useRef<ViewportPhase>(phase);
  useLayoutEffect(() => {
    phaseRef.current = phase;
  }, [phase]);
  const [pendingCount, setPendingCount] = useState(0);
  const [firstUnreadMessageId, setFirstUnreadMessageId] = useState<string | null>(null);
  const [resolved, setResolved] = useState(false);
  const [scrollTarget, setScrollTarget] = useState<{ messageId: string | null } | undefined>(
    undefined,
  );
  const [searchAttempts, setSearchAttempts] = useState(0);
  const [searchedForLength, setSearchedForLength] = useState(-1);
  const [countedMessages, setCountedMessages] = useState(messages);
  const [scrollAnimationRequest, setScrollAnimationRequest] = useState(0);
  const reachedBottomFiredRef = useRef(false);
  const currentAnchorRef = useRef<{ messageId: string; offsetPx: number } | null>(null);
  const conversationKeyRef = useRef(conversationKey);
  const onCaptureAnchorRef = useRef(onCaptureAnchor);
  const onReachedBottomRef = useRef(onReachedBottom);
  useLayoutEffect(() => {
    conversationKeyRef.current = conversationKey;
    onCaptureAnchorRef.current = onCaptureAnchor;
    onReachedBottomRef.current = onReachedBottom;
  });

  // isNearBottomRef mirrors `phase === "AT_BOTTOM"` for synchronous reads
  // inside the scroll handler, which can't rely on React state having
  // committed yet within the same event.
  //
  // Threshold: user is "near bottom" when the distance from the bottom edge of
  // the scroll container to the actual bottom is ≤ 150 px (roughly one message).
  const isNearBottomRef = useRef(true);

  // #788: whether the last real scroll left the viewport at the REAL tail,
  // not merely within isNearBottomRef's 150px courtesy threshold. The phase
  // cannot answer this: the scroll handler below assigns AT_BOTTOM anywhere
  // inside that threshold, so a deliberate small scroll up keeps the phase
  // AT_BOTTOM while the reader is no longer at the end.
  //
  // Only the scroll handler writes it, which is exactly what makes it usable
  // from the ResizeObserver: a pure content resize fires no scroll event, so
  // by the time the reflow is observed this still holds the position the
  // reader chose *before* the layout moved under them.
  //
  // Starts true: a conversation that resolves to the bottom is positioned at
  // the tail before any scroll event can report it.
  const atRealTailRef = useRef(true);

  /**
   * The topmost visible message and its offset from the container's top edge
   * — the anchor #492 restores a history position from. jsdom reports every
   * box as zero-sized, so this deterministically resolves to the first loaded
   * message there; a real browser resolves it to whatever message is actually
   * scrolled to the top of the viewport.
   */
  function computeTopmostVisible(
    container: HTMLDivElement,
    refs: Map<string, HTMLDivElement>,
  ): { messageId: string; offsetPx: number } | null {
    const containerTop = container.getBoundingClientRect().top;
    let best: { messageId: string; offsetPx: number } | null = null;
    for (const [id, el] of refs) {
      const offsetPx = el.getBoundingClientRect().top - containerTop;
      if (offsetPx >= -4 && (!best || offsetPx < best.offsetPx)) {
        best = { messageId: id, offsetPx };
      }
    }
    return best;
  }

  useEffect(() => {
    const el = listRef.current;
    if (!el) return;
    // ponytail: recomputes the anchor on every scroll event rather than
    // throttling via rAF — fine at MVP message-list sizes; add throttling if
    // profiling ever shows this loop hot.
    const onScroll = () => {
      const nearBottom = isNearBottom(el.scrollHeight, el.scrollTop, el.clientHeight);
      isNearBottomRef.current = nearBottom;
      atRealTailRef.current = isNearBottom(
        el.scrollHeight,
        el.scrollTop,
        el.clientHeight,
        TAIL_EPSILON_PX,
      );
      // Only the bottom sentinel ends SCROLLING_TO_BOTTOM — a near-bottom
      // scroll position reached mid-animation is not yet a confirmed arrival
      // (a media resize could still be in flight).
      if (phaseRef.current !== "SCROLLING_TO_BOTTOM") {
        if (nearBottom) {
          if (phaseRef.current !== "AT_BOTTOM") setPhase("AT_BOTTOM");
        } else if (phaseRef.current === "AT_BOTTOM") {
          setPhase("READING_HISTORY");
        }
      }
      const topmost = computeTopmostVisible(el, messageRefs.current);
      if (topmost) currentAnchorRef.current = topmost;
    };
    el.addEventListener("scroll", onScroll, { passive: true });
    return () => el.removeEventListener("scroll", onScroll);
  }, [setPhase]);

  // Bottom sentinel: the same node scrollToBottom() targets also tells us,
  // authoritatively, when the real tail is on screen — surviving remeasure
  // from late-loading media instead of trusting a scrollIntoView call's mere
  // return. This is also the single place mark-read is triggered from (#492
  // G): never from opening the route, only from confirmed arrival.
  useEffect(() => {
    const sentinel = bottomRef.current;
    // Capability check: some test environments (and, historically, older
    // browsers) have no IntersectionObserver — degrade to "never confirmed",
    // matching this file's existing scrollIntoView capability check.
    if (!sentinel || typeof IntersectionObserver === "undefined") return;
    const observer = new IntersectionObserver(
      (entries) => {
        const atBottom = Boolean(entries[0]?.isIntersecting);
        if (atBottom) {
          isNearBottomRef.current = true;
          setPhase("AT_BOTTOM");
          setPendingCount(0);
          if (!reachedBottomFiredRef.current) {
            reachedBottomFiredRef.current = true;
            onReachedBottomRef.current();
          }
        } else {
          reachedBottomFiredRef.current = false;
        }
      },
      { threshold: 1 },
    );
    observer.observe(sentinel);
    return () => observer.disconnect();
  }, [setPhase]);

  // #788 tail-lock: keeps the viewport pinned to the real bottom through an
  // async reflow (an attachment/media preview finishing its layout well after
  // the initial positioning) — for ANY variable-height content, never an
  // attachment-specific special case.
  //
  // Observes contentRef, not listRef: listRef's clientHeight is fixed by CSS
  // and never changes when a message grows, so only the content wrapper's own
  // box height tracks what would otherwise show up as listRef.scrollHeight.
  //
  // Two ways in, and the phase alone is not enough for either:
  //
  // - SCROLLING_TO_BOTTOM: the reader explicitly asked for the end (button or
  //   own-send), so a resize re-pins unconditionally — mid-flight is exactly
  //   when a late-loading preview would otherwise strand the animation short.
  // - AT_BOTTOM: re-pins ONLY if the last real scroll left the viewport at the
  //   true tail. #492's scroll handler assigns AT_BOTTOM anywhere within 150px
  //   of the end, so without atRealTailRef a reader who deliberately scrolled
  //   up a little — and stayed inside that threshold — would be yanked back
  //   down by an unrelated image finishing its layout.
  //
  // The correction is a direct scrollTop assignment, never scrollIntoView:
  // during SCROLLING_TO_BOTTOM there is already an in-flight scrollIntoView
  // animation, and a second scrollIntoView call races that animation instead
  // of cleanly overriding it — a plain scrollTop write always wins.
  //
  // While READING_HISTORY/AT_FIRST_UNREAD/RESTORING_POSITION this is a no-op:
  // a reading position is never overridden by a layout shift alone.
  useEffect(() => {
    const content = contentRef.current;
    if (!content || typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(() => {
      const el = listRef.current;
      if (!el) return;
      const holdsTail =
        phaseRef.current === "SCROLLING_TO_BOTTOM" ||
        (phaseRef.current === "AT_BOTTOM" && atRealTailRef.current);
      if (holdsTail) {
        el.scrollTop = el.scrollHeight - el.clientHeight;
      }
    });
    observer.observe(content);
    return () => observer.disconnect();
  }, []);

  // Captures the anchor exactly once, when this conversation is actually left
  // — the unmount that already happens on every target switch (#492 item 4:
  // "capturar posição final antes de trocar de conversa").
  useEffect(() => {
    return () => {
      const anchor = currentAnchorRef.current;
      onCaptureAnchorRef.current(conversationKeyRef.current, {
        atBottom: phaseRef.current === "AT_BOTTOM",
        anchorMessageId: phaseRef.current === "AT_BOTTOM" ? null : (anchor?.messageId ?? null),
        anchorOffsetPx: anchor?.offsetPx ?? 0,
        savedAt: Date.now(),
      });
    };
  }, []);

  useEffect(() => {
    return () => {
      if (highlightTimerRef.current !== null) window.clearTimeout(highlightTimerRef.current);
      if (hoverCloseTimerRef.current !== null) window.clearTimeout(hoverCloseTimerRef.current);
    };
  }, []);

  /**
   * Resolves where this conversation opens (#492 case A/B/C), once, before
   * anything else moves the viewport. Priority: an explicit deep link
   * (focusMessageId, owned by the quote-jump effect above) > a saved history
   * anchor > the first unread message > the bottom.
   *
   * Computed during render — not in an effect — because it is a pure
   * function of already-available props (messages/hasMore/initialAnchor/
   * unreadCountAtOpen/focusMessageId), and conditionally updating state while
   * rendering is React's documented pattern for exactly this (see
   * HeaderAvatar's doc comment above for the same technique against the same
   * set-state-in-effect lint rule this codebase enforces). The two effects
   * right after this block perform the only genuine side effects — fetching
   * another page, and the instant DOM scroll — and neither calls a state
   * setter.
   *
   * A saved anchor or an unread boundary not in the currently loaded window
   * triggers a bounded backward search (existing loadMore/beforeCursor
   * pagination, reused rather than a new endpoint — see chatViewportState.ts):
   * `searchedForLength` guarantees searchAttempts is bumped at most once per
   * distinct messages.length, so an incidental extra render never
   * double-fetches; capped at MAX_BOUNDARY_SEARCH_PAGES, exhausting it while
   * more history remains falls back to the bottom rather than guessing a
   * wrong boundary.
   */
  if (!resolved && messages.length > 0) {
    if (focusMessageId) {
      // The existing quote-jump effect owns positioning for a deep link.
      setResolved(true);
      if (phase !== "READING_HISTORY") setPhase("READING_HISTORY");
    } else {
      let handled = false;
      let needsMoreHistory = false;
      if (initialAnchor && !initialAnchor.atBottom && initialAnchor.anchorMessageId) {
        const anchorId = initialAnchor.anchorMessageId;
        if (messages.some((message) => message.id === anchorId)) {
          setResolved(true);
          setScrollTarget({ messageId: anchorId });
          if (phase !== "READING_HISTORY") setPhase("READING_HISTORY");
          handled = true;
        } else if (hasMore && searchAttempts < MAX_BOUNDARY_SEARCH_PAGES) {
          needsMoreHistory = true;
          handled = true;
        }
        // else: anchor message no longer exists and the search is exhausted
        // — fall through to the unread/bottom fallback below (#492 item 9: a
        // safe fallback, never an infinite search or a crash).
      }
      if (!handled && unreadCountAtOpen > 0) {
        const boundary = findFirstUnreadBoundary(messages, currentUserId, unreadCountAtOpen);
        if (boundary) {
          setResolved(true);
          setScrollTarget({ messageId: boundary.messageId });
          if (firstUnreadMessageId !== boundary.messageId) {
            setFirstUnreadMessageId(boundary.messageId);
          }
          if (phase !== "AT_FIRST_UNREAD") setPhase("AT_FIRST_UNREAD");
          handled = true;
        } else if (hasMore && searchAttempts < MAX_BOUNDARY_SEARCH_PAGES) {
          needsMoreHistory = true;
          handled = true;
        } else if (!hasMore) {
          // Whole history loaded and still short of unreadCountAtOpen (a
          // stale count/race) — anchor at the oldest loaded message rather
          // than guessing further.
          const oldest = messages[0];
          setResolved(true);
          setScrollTarget({ messageId: oldest.id });
          if (firstUnreadMessageId !== oldest.id) setFirstUnreadMessageId(oldest.id);
          if (phase !== "AT_FIRST_UNREAD") setPhase("AT_FIRST_UNREAD");
          handled = true;
        }
        // else: search cap reached while more history remains — never render
        // a guessed/wrong boundary; fall through to the bottom below.
      }
      if (!handled) {
        setResolved(true);
        setScrollTarget({ messageId: null });
        if (phase !== "AT_BOTTOM") setPhase("AT_BOTTOM");
      } else if (needsMoreHistory && searchedForLength !== messages.length) {
        setSearchedForLength(messages.length);
        setSearchAttempts((n) => n + 1);
      }
    }
  }

  // Fetches the next page for the bounded search above — a plain
  // external-system call, no setState of its own. Fires exactly once per
  // searchAttempts bump (searchedForLength above already guarantees at most
  // one bump per distinct messages.length).
  useEffect(() => {
    if (searchAttempts > 0) onLoadMoreRef.current();
  }, [searchAttempts]);

  // Performs the actual instant positioning once resolution picked a target
  // — a plain DOM operation, no setState of its own.
  useLayoutEffect(() => {
    if (!scrollTarget) return;
    if (scrollTarget.messageId === null) {
      scrollToBottom(bottomRef, "auto");
    } else if (scrollTarget.messageId === firstUnreadMessageId && unreadDividerRef.current) {
      // Land on the separator, not the message: the message sits right below
      // it, so scrolling to the message alone would push the separator (and
      // its "Novas mensagens" label) off-screen above the viewport.
      unreadDividerRef.current.scrollIntoView({ behavior: "auto", block: "start" });
    } else {
      messageRefs.current
        .get(scrollTarget.messageId)
        ?.scrollIntoView({ behavior: "auto", block: "start" });
    }
  }, [scrollTarget, firstUnreadMessageId]);

  // Real event handler (button onClick) — calling setState here is completely
  // ordinary, not an effect.
  const startScrollToBottom = useCallback(() => {
    setPhase("SCROLLING_TO_BOTTOM");
    scrollToBottom(bottomRef, explicitScrollBehavior());
  }, [setPhase]);

  // Consumes an own-send's animated-scroll request (set during render below)
  // — a plain DOM operation, no setState of its own, so the mutation effect
  // further down never has to call startScrollToBottom() itself.
  useEffect(() => {
    if (scrollAnimationRequest > 0) scrollToBottom(bottomRef, explicitScrollBehavior());
  }, [scrollAnimationRequest]);

  // #492: reacts to a new message array — grows the pending-count badge for
  // an eligible (active, non-own) WS message received while not AT_BOTTOM,
  // and starts the animated return-to-bottom for an own send (#492 item 21).
  // Detected during render by comparing the messages array's identity to the
  // previous render's — the reducer always produces a new array on a real
  // mutation, so this fires exactly once per message even across repeated
  // "ws_append"/"append" values. Kept out of an effect for the same
  // set-state-in-effect reason as the resolution block above.
  if (countedMessages !== messages) {
    setCountedMessages(messages);
    if (resolved && lastMutation === "ws_append" && phase !== "AT_BOTTOM") {
      const latest = messages[messages.length - 1];
      if (latest && isEligibleUnreadMessage(latest, currentUserId)) {
        setPendingCount((count) => count + 1);
      }
    }
    if (resolved && lastMutation === "append") {
      if (phase !== "SCROLLING_TO_BOTTOM") setPhase("SCROLLING_TO_BOTTOM");
      setScrollAnimationRequest((n) => n + 1);
    }
  }

  // Scroll management for mutations that happen AFTER initial resolution.
  // "prepend"    → restore position via scrollHeight delta (older messages added above).
  // "append"     → an own-sent message: explicit "return to the present" (#492
  //                item 21), routed through the same SCROLLING_TO_BOTTOM the
  //                button uses so it also only completes once the bottom
  //                sentinel confirms arrival.
  // "ws_append"  → auto-scroll only while already AT_BOTTOM; otherwise the
  //                viewport is never pulled, and the pending-count badge grows
  //                for an eligible (active, non-own) message.
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
    } else if (resolved && lastMutation === "ws_append" && phase === "AT_BOTTOM") {
      scrollToBottom(bottomRef, "auto");
      // Not-at-bottom growth of the pending-count badge, and an own-send's
      // animated return to bottom ("append"), are both handled during render
      // above (set-state-in-effect) — this branch only ever needs the DOM
      // follow for ws_append while already at the tail.
    }

    // Only snapshot scrollHeight in a stable state — not during "none" transitions
    // where the loading spinner may inflate the measurement.
    if (lastMutation !== "none") {
      prevScrollHeightRef.current = el.scrollHeight;
    }
  }, [messages, lastMutation, resolved, phase]);

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
  // #492: an "unread-divider" is inserted immediately before firstUnreadMessageId,
  // once resolution lands AT_FIRST_UNREAD — never persisted, never a message.
  const withDividers: Array<
    | { type: "divider"; label: string }
    | { type: "unread-divider" }
    | { type: "msg"; message: Message; isGrouped: boolean }
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
    if (msg.id === firstUnreadMessageId) {
      withDividers.push({ type: "unread-divider" });
    }
    const minute = formatTime(msg.createdAt);
    const isGrouped = msg.senderId === lastSenderId && minute === lastMinute;
    withDividers.push({ type: "msg", message: msg, isGrouped });
    lastSenderId = msg.senderId;
    lastMinute = minute;
  }

  const scrollButtonVisible =
    phase === "READING_HISTORY" || phase === "AT_FIRST_UNREAD" || phase === "SCROLLING_TO_BOTTOM";

  return (
    <div className="chat-msg-area__list-wrap">
      <div
        ref={listRef}
        className="chat-msg-area__list"
        role="log"
        aria-live="polite"
        aria-label="Mensagens"
      >
        {/* #788: the ResizeObserver target — see the tail-lock effect above. */}
        <div ref={contentRef} className="chat-msg-area__list-content">
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
            ) : item.type === "unread-divider" ? (
              <div
                key="unread-divider"
                ref={unreadDividerRef}
                className="chat-msg-area__new-messages-divider"
                role="separator"
                aria-label="Novas mensagens"
              >
                Novas mensagens
              </div>
            ) : item.message.kind === "system" ? (
              // A conversation event is not something a person said, so it never
              // becomes a MessageBubble: no bubble, no avatar, and none of the
              // message actions — editing "Fulano saiu do grupo" is not a thing
              // (issue #527).
              <ConversationSystemMessage
                key={item.message.id}
                message={item.message}
                scope={systemScope}
              />
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
                onReconcileLinkSafety={onReconcileLinkSafety}
                onEditMessage={onEditMessage}
                onEditForbidden={onEditForbidden}
                onDeleteMessage={onDeleteMessage}
                editDisabled={editDisabledIds.has(item.message.id)}
                mentionTarget={mentionTarget}
                presenceTarget={presenceTarget}
                onTogglePin={onTogglePin}
                isPinned={pinnedIds?.has(item.message.id) ?? false}
                recentReactionEmojis={recentReactionEmojis}
                emojiUsage={emojiUsage}
                onEmojiToneChange={onEmojiToneChange}
                currentUserId={currentUserId}
                reactionMenuVisible={hoveredMessageId === item.message.id}
                onReactionMenuVisibleChange={handleReactionMenuVisibleChange}
                pickerOpen={openPickerMessageId === item.message.id}
                onPickerOpenChange={handlePickerOpenChange}
                quoteAuthorLabel={
                  item.message.quoted
                    ? quoteAuthorLabel(item.message.quoted, messagesById)
                    : undefined
                }
                canJumpToQuote={
                  item.message.quoted ? messagesById.has(item.message.quoted.id) : false
                }
                onQuoteJump={handleQuoteJump}
                onReferenceJump={onReferenceJump}
                onOpenAuthorDM={onOpenAuthorDM}
                openingAuthorDM={openingAuthorDMIds?.has(item.message.senderId) ?? false}
                isHighlighted={highlightedMessageId === item.message.id}
                setMessageRef={setMessageRef}
              />
            ),
          )}
          <div ref={bottomRef} data-testid="chat-bottom-sentinel" />
        </div>
      </div>
      <ScrollToBottomButton
        visible={scrollButtonVisible}
        pendingCount={pendingCount}
        onClick={startScrollToBottom}
      />
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

interface ConversationHeaderProps {
  kind: "channel" | "dm";
  name: string;
  counterpart: DMCounterpart | undefined;
  presenceTarget: string | undefined;
  onStartCall: ChatOutletContext["startCall"];
  resourceCall: ResourceCallHeaderState | undefined;
  details: ConversationDetailsPanelState;
  detailsToggleRef: RefObject<HTMLButtonElement | null>;
}

/** The channel or DM header, and the details control both of them offer. */
function ConversationHeader({
  kind,
  name,
  counterpart,
  presenceTarget,
  onStartCall,
  resourceCall,
  details,
  detailsToggleRef,
}: ConversationHeaderProps) {
  const detailsToggle = details.supportsDetails ? (
    <DetailsToggle
      ref={detailsToggleRef}
      open={details.showDetails}
      label={details.toggleLabel}
      onToggle={details.toggle}
    />
  ) : undefined;
  if (kind === "channel") {
    return <HeaderChannel name={name} resourceCall={resourceCall} detailsToggle={detailsToggle} />;
  }
  return (
    <HeaderDM
      name={name}
      counterpart={counterpart}
      presenceTarget={presenceTarget}
      onStartCall={onStartCall}
      resourceCall={resourceCall}
      detailsToggle={detailsToggle}
    />
  );
}

/** Every callback the timeline hands down to a message. */
interface TimelineActions {
  onLoadMore: () => void;
  onToggleReaction: (messageId: string, emoji: string) => void;
  onReplyMessage: (message: Message) => void;
  onReferenceMessage: (message: Message) => void;
  onForwardMessage: (message: Message) => void;
  onReferenceJump: (reference: NonNullable<Message["reference"]>) => void;
  onOpenAuthorDM?: MessageBubbleProps["onOpenAuthorDM"];
  openingAuthorDMIds?: Set<string>;
  onToggleFavorite: MessageBubbleProps["onToggleFavorite"];
  onReconcileLinkSafety: MessageBubbleProps["onReconcileLinkSafety"];
  onEditMessage: MessageBubbleProps["onEditMessage"];
  onEditForbidden: MessageBubbleProps["onEditForbidden"];
  onDeleteMessage: MessageBubbleProps["onDeleteMessage"];
  onTogglePin: (messageId: string, pin: boolean) => void;
  onRetry: () => void;
}

interface ConversationTimelineProps {
  kind: "channel" | "dm";
  targetId: string;
  name: string;
  detailsKind: ConversationDetailsKind | null;
  mentionTarget?: MentionTarget;
  state: MessagesState;
  currentUserId: string;
  actions: TimelineActions;
  editDisabledIds: Set<string>;
  pinnedIds: Set<string>;
  recentReactionEmojis: string[];
  emojiUsage: EmojiUsage;
  onEmojiToneChange: (tone: number) => void;
  focusMessageId: string;
  /** #492: see MessageListProps. */
  conversationKey: string;
  unreadCountAtOpen: number;
  initialAnchor: ViewportAnchor | null;
  onCaptureAnchor: (key: string, anchor: ViewportAnchor) => void;
  onReachedBottom: () => void;
}

/**
 * Which of the conversation's four states is on screen: still loading, failed,
 * empty, or a list of messages.
 *
 * The channel-only props are resolved here rather than by the caller, because
 * "does a DM have a channel id" is a question about this timeline and not about
 * the page around it.
 */
function ConversationTimeline({
  kind,
  targetId,
  name,
  detailsKind,
  mentionTarget,
  state,
  currentUserId,
  actions,
  editDisabledIds,
  pinnedIds,
  recentReactionEmojis,
  emojiUsage,
  onEmojiToneChange,
  focusMessageId,
  conversationKey,
  unreadCountAtOpen,
  initialAnchor,
  onCaptureAnchor,
  onReachedBottom,
}: ConversationTimelineProps) {
  if (state.status === "loading") return <LoadingSkeleton />;
  if (state.status === "error") return <ErrorState onRetry={actions.onRetry} />;
  if (state.status !== "ready") return null;
  if (state.messages.length === 0) return <EmptyState kind={kind} name={name} />;
  const channelId = kind === "channel" ? targetId : undefined;
  return (
    <MessageList
      messages={state.messages}
      currentUserId={currentUserId}
      // "canal" / "grupo" / "conversa" for this timeline's system messages
      // (issue #527).
      systemScope={systemScopeFor(detailsKind, kind)}
      hasMore={state.nextCursor !== ""}
      loadingMore={state.loadingMore}
      lastMutation={state.lastMutation}
      onLoadMore={actions.onLoadMore}
      onToggleReaction={actions.onToggleReaction}
      onReplyMessage={actions.onReplyMessage}
      onReferenceMessage={actions.onReferenceMessage}
      onForwardMessage={channelId ? actions.onForwardMessage : undefined}
      onReferenceJump={actions.onReferenceJump}
      onOpenAuthorDM={actions.onOpenAuthorDM}
      openingAuthorDMIds={actions.openingAuthorDMIds}
      onToggleFavorite={actions.onToggleFavorite}
      onReconcileLinkSafety={actions.onReconcileLinkSafety}
      onEditMessage={actions.onEditMessage}
      onEditForbidden={actions.onEditForbidden}
      onDeleteMessage={actions.onDeleteMessage}
      editDisabledIds={editDisabledIds}
      mentionTarget={mentionTarget}
      presenceTarget={targetId ? presenceTargetKey(kind, targetId) : undefined}
      onTogglePin={actions.onTogglePin}
      pinnedIds={pinnedIds}
      recentReactionEmojis={recentReactionEmojis}
      emojiUsage={emojiUsage}
      onEmojiToneChange={onEmojiToneChange}
      focusMessageId={focusMessageId}
      conversationKey={conversationKey}
      unreadCountAtOpen={unreadCountAtOpen}
      initialAnchor={initialAnchor}
      onCaptureAnchor={onCaptureAnchor}
      onReachedBottom={onReachedBottom}
    />
  );
}

/**
 * The strip between the timeline and the composer: a failed send, an unstable
 * connection, a refused action, and who is typing.
 *
 * The refusals share one line because only one of them can usefully be read at
 * a time, and the order is the order they matter in.
 */
function ConversationNotices({
  sendError,
  realtimeError,
  actionError,
  openDMError,
  pinError,
  typingLabel,
}: {
  sendError: string | null;
  realtimeError: string | null;
  actionError: string | null;
  openDMError: string | null;
  pinError: string | null;
  typingLabel: string | null;
}) {
  const refusal = actionError ?? openDMError ?? pinError;
  return (
    <>
      {sendError && (
        <div className="chat-msg-area__send-error" role="alert" data-testid="chat-send-error">
          <IconWarning />
          {sendError}
        </div>
      )}
      {realtimeError && (
        <div
          className="chat-msg-area__realtime-error"
          role="status"
          data-testid="chat-realtime-error"
        >
          <IconWarning />
          Conexão em tempo real instável. Tentando reconectar...
        </div>
      )}
      {refusal && (
        <div className="chat-msg-area__reaction-error" role="alert">
          <IconWarning />
          {refusal}
        </div>
      )}
      {typingLabel && (
        <div
          className="chat-msg-area__typing-indicator"
          role="status"
          data-testid="chat-typing-indicator"
        >
          <span className="chat-msg-area__typing-dots" aria-hidden="true">
            <span />
            <span />
            <span />
          </span>
          {typingLabel}
        </div>
      )}
    </>
  );
}

/**
 * The two message dialogs. Both render through a portal, so their position in
 * the tree costs the conversation column no layout.
 */
function ConversationDialogs({
  kind,
  targetId,
  channels,
  dms,
  referenceSource,
  forwardSource,
  onCloseReference,
  onSelectReferenceDestination,
  onCloseForward,
}: {
  kind: "channel" | "dm";
  targetId: string;
  channels: Channel[];
  dms: DMConversation[];
  referenceSource: Message | null;
  forwardSource: ForwardSourceContext | null;
  onCloseReference: () => void;
  onSelectReferenceDestination: (target: { kind: "channel" | "dm"; id: string }) => void;
  onCloseForward: () => void;
}) {
  return (
    <>
      {referenceSource && (
        <ReferenceDestinationDialog
          current={{ kind, id: targetId }}
          channels={channels}
          dms={dms}
          onClose={onCloseReference}
          onSelect={onSelectReferenceDestination}
        />
      )}
      {forwardSource && kind === "channel" && (
        <ForwardMessageDialog
          source={forwardSource}
          channels={channels}
          onClose={onCloseForward}
          onSuccess={onCloseForward}
        />
      )}
    </>
  );
}

interface ChatMessageAreaProps {
  kind: "channel" | "dm";
}

/**
 * The direct 1:1 call bar for this view, or nothing (#673).
 *
 * Derived from ChatShell's own directCallSession — itself populated only once
 * CallSessionProvider's directPresentationCall authority is non-null (genuinely
 * active, media-connected, locally owned; never merely ringing — IncomingCallPopup
 * keeps owning that surface). Matched against THIS view's own server-resolved
 * counterpart (never the route or a display name) so the bar only ever appears in
 * the exact DM the call belongs to — navigating to a different conversation, or a
 * call belonging to a different DM, never shows it here.
 *
 * A module-level function rather than inline in ChatMessageArea, which the
 * resource call bar already left for the same reason: the component states what
 * it renders, and each bar decides for itself whether it applies.
 */
function directCallBar(
  kind: "channel" | "dm",
  session: ChatOutletContext["directCallSession"],
  counterpart: DMCounterpart | undefined,
): ActiveDirectCallBarProps | null {
  if (kind !== "dm" || !session || !counterpart || counterpart.userId !== session.peerUserId) {
    return null;
  }
  return {
    title: `${session.callType === "video" ? "Chamada de vídeo" : "Chamada de voz"} — ${counterpart.displayName}`,
    startedAt: session.startedAt,
    peerUserId: session.peerUserId,
    peerName: counterpart.displayName,
    peerAvatarUrl: counterpart.avatarUrl,
    microphoneEnabled: session.microphoneEnabled,
    microphonePending: session.microphonePending,
    onToggleMicrophone: session.onToggleMicrophone,
    onLeave: session.onLeave,
    onOpenFullCall: session.onOpenFullCall,
  };
}

export default function ChatMessageArea({ kind }: ChatMessageAreaProps) {
  const location = useLocation();
  const navigate = useNavigate();
  const target = useConversationTarget(kind);
  const { ctx, targetId, focusMessageId, activeDM, resolvedName } = target;
  const [referenceSource, setReferenceSource] = useState<Message | null>(null);
  const [forwardSource, setForwardSource] = useState<ForwardSourceContext | null>(null);
  const pendingReference = usePendingReference(location.state, ctx.channels, ctx.dms);
  const [allowedReactionEmojis, setAllowedReactionEmojis] = useState<string[]>([]);
  const {
    usage: emojiUsage,
    remember: rememberReaction,
    changeTone: changeEmojiTone,
  } = useEmojiUsage(ctx.currentUserId);
  const [openDMError, setOpenDMError] = useState<string | null>(null);
  const [openingAuthorDMIds, setOpeningAuthorDMIds] = useState<Set<string>>(new Set());
  const [editDisabledIds, setEditDisabledIds] = useState<Set<string>>(new Set());
  const lastReactionToggleRef = useRef({ key: "", at: 0 });
  const openingAuthorDMRef = useRef(new Map<string, AbortController>());
  const authorDMMountedRef = useRef(true);
  const authorDMGenerationRef = useRef(0);
  // #492: per-conversation viewport anchor, kept for the life of this SPA tab.
  // Owned here rather than in MessageList, which unmounts on every conversation
  // switch (ConversationTimeline swaps it for LoadingSkeleton while useMessages
  // is "loading") — this component does not, so it is where anchors survive.
  //
  // A Map held in useState (its setter never called) rather than useRef:
  // react-hooks/refs forbids reading a ref's .current during render — even a
  // ref object merely passed down as a prop and read in the receiving
  // component — and MessageList's render-time resolution genuinely needs this
  // value synchronously. A plain object identity that happens to be stable
  // across renders (never reassigned, only mutated via .set()) is not a ref
  // and carries none of that rule's tearing concerns: nothing here ever
  // depends on React noticing a mutation to it.
  const [viewportAnchors] = useState(() => new Map<string, ViewportAnchor>());
  const conversationKey = targetId ? `${kind}:${targetId}` : "";
  const unreadCountAtOpen = useMemo(() => {
    if (!targetId) return 0;
    const list = kind === "channel" ? ctx.channels : ctx.dms;
    return list.find((item) => item.id === targetId)?.unreadCount ?? 0;
  }, [kind, targetId, ctx.channels, ctx.dms]);
  // Plain function calls, safe during render.
  const initialAnchor =
    (conversationKey ? viewportAnchors.get(conversationKey) : undefined) ??
    (conversationKey && ctx.currentUserId
      ? loadViewportAnchor(ctx.currentUserId, kind, targetId)
      : null) ??
    null;
  const handleReachedBottom = useCallback(() => {
    if (!targetId) return;
    ctx.markRead?.({ kind, targetId });
  }, [ctx, kind, targetId]);
  const handleCaptureAnchor = useCallback(
    (key: string, anchor: ViewportAnchor) => {
      viewportAnchors.set(key, anchor);
      if (ctx.currentUserId) saveViewportAnchor(ctx.currentUserId, kind, targetId, anchor);
    },
    [viewportAnchors, ctx.currentUserId, kind, targetId],
  );
  const recentReactionEmojis = useMemo(
    () => quickReactionEmojis(emojiUsage, allowedReactionEmojis),
    [allowedReactionEmojis, emojiUsage],
  );
  // One object, so the composer's toolbar is not re-rendered by the identity of
  // its own props changing every frame.
  const composerEmoji = useMemo(
    () => ({ usage: emojiUsage, onToneChange: changeEmojiTone, onUsed: rememberReaction }),
    [changeEmojiTone, emojiUsage, rememberReaction],
  );

  const pinTarget = useMemo(() => (targetId ? { kind, id: targetId } : null), [kind, targetId]);
  const { pins, pinnedIds, error: pinError, togglePin, reload: reloadPins } = usePins(pinTarget);

  // One selector, one list, one result: the bar above the conversation and the
  // details panel are handed the same object, so a pin/unpin updates both at
  // once and neither can show a message the other does not.
  const latestPin = useMemo(() => selectLatestPin(pins), [pins]);

  const detailsToggleRef = useRef<HTMLButtonElement>(null);
  const details = useConversationDetailsPanel({
    kind,
    targetId,
    activeDM,
    resolvedName,
    toggleRef: detailsToggleRef,
  });
  const detailsKind = details.detailsKind;
  const reloadOpenDetails = details.reload;

  // Typing indicator: useTypingIndicator needs sendTyping, which useMessages
  // only produces once called, but useMessages needs an onTypingUpdated
  // callback to hand inbound events to useTypingIndicator. Breaking that
  // cycle is the one job of this ref — a stable callback handed to
  // useMessages that indirects to whatever useTypingIndicator currently
  // returns, kept current by the layout effect below (same "ref holds the
  // latest callback" shape as every onXRef in useMessages itself).
  const typingHandleRemoteEventRef = useRef<(event: WSTypingUpdatedEvent) => void>(() => {});
  const handleTypingUpdatedFromMessages = useCallback((event: WSTypingUpdatedEvent) => {
    typingHandleRemoteEventRef.current(event);
  }, []);

  const {
    state,
    sendMessage,
    retry,
    loadMore,
    selectReply,
    cancelReply,
    toggleReaction,
    sendTyping,
    toggleFavorite,
    reconcileLinkSafety,
    editMessageLocal,
    deleteMessageLocal,
  } = useMessages({
    kind,
    targetId,
    bodyFormat: target.bodyFormat,
    currentUserId: ctx.currentUserId,
    focusMessageId,
    onOwnReactionConfirmed: rememberReaction,
    onPinUpdated: reloadPins,
    onTypingUpdated: handleTypingUpdatedFromMessages,
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

  const typing = useTypingIndicator({
    kind,
    targetId,
    currentUserId: ctx.currentUserId,
    sendTyping,
    // Never start a typing session while the composer itself would refuse
    // one — matches the disabled prop ChatComposer already receives below.
    disabled: state.status !== "ready",
  });
  useLayoutEffect(() => {
    typingHandleRemoteEventRef.current = typing.handleRemoteEvent;
  });

  // Destructured so useCallback/useEffect below can depend on these specific,
  // stable functions rather than the whole `typing` object, whose identity
  // changes every render.
  const { notifyActivity: typingNotifyActivity, stop: typingStop } = typing;

  // A content-changing edit starts/renews typing; the composer being emptied
  // stops it immediately rather than waiting out the inactivity timer.
  const handleComposerActivity = useCallback(
    (hasContent: boolean) => {
      if (hasContent) typingNotifyActivity();
      else typingStop();
    },
    [typingNotifyActivity, typingStop],
  );

  // Second-tier fallback only: the server now resolves and sends the
  // authoritative name on the typing event itself
  // (typing.typingDisplayNameByUserId, see useTypingIndicator). This
  // heuristic — DM roster, else the name most recently seen on that user's
  // own message — only fires when the server's resolution was empty (lookup
  // failure, no display name on file), and "Alguém" (inside
  // typingIndicatorText) is the last resort after that.
  const typingNameByUserId = useMemo(() => {
    const names = new Map<string, string>();
    if (kind === "dm" && activeDM) {
      for (const participant of activeDM.participants) {
        names.set(participant.id, participant.displayName);
      }
    }
    for (const message of state.messages) {
      if (!names.has(message.senderId)) names.set(message.senderId, message.senderDisplayName);
    }
    return names;
  }, [kind, activeDM, state.messages]);

  const typingIndicatorLabel = useMemo(() => {
    const names = new Map(typingNameByUserId); // heuristic, tier 2
    for (const [userId, name] of typing.typingDisplayNameByUserId) {
      if (name) names.set(userId, name); // server-authoritative, wins
    }
    return typingIndicatorText(typing.typingUserIds, names);
  }, [typing.typingUserIds, typing.typingDisplayNameByUserId, typingNameByUserId]);

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

  useEffect(() => {
    authorDMMountedRef.current = true;
    const pendingAuthorDMs = openingAuthorDMRef.current;
    return () => {
      authorDMMountedRef.current = false;
      for (const controller of pendingAuthorDMs.values()) controller.abort();
      pendingAuthorDMs.clear();
    };
  }, []);

  useLayoutEffect(() => {
    const generation = (authorDMGenerationRef.current += 1);
    for (const controller of openingAuthorDMRef.current.values()) controller.abort();
    openingAuthorDMRef.current.clear();
    queueMicrotask(() => {
      if (!authorDMMountedRef.current || authorDMGenerationRef.current !== generation) return;
      setOpeningAuthorDMIds(new Set());
      setOpenDMError(null);
    });
  }, [kind, targetId]);

  const handleSend = useCallback(
    async (body: string, attachmentIds?: string[]): Promise<SendResult> => {
      const result = await sendMessage(
        body,
        pendingReference.messageId || undefined,
        attachmentIds,
      );
      if (result.status === "sent") {
        // Sending is itself the clearest possible "stopped typing" signal —
        // do not wait for the composer-cleared activity event or the
        // inactivity timeout to catch up.
        typingStop();
        navigate(`${location.pathname}${location.search}`, { replace: true, state: null });
      }
      return result;
    },
    [
      location.pathname,
      location.search,
      navigate,
      pendingReference.messageId,
      sendMessage,
      typingStop,
    ],
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

  /**
   * Toggle a reaction, whatever the reader touched to ask for it.
   *
   * Nothing is checked against the emoji catalog here. Every value that reaches
   * this comes from a control this UI drew — the server's quick row, an entry
   * picked out of the loaded catalog, or a reaction the server already returned
   * on the message — and the server validates the sequence again on the toggle.
   * The catalog is loaded lazily with the picker, so asking it was really asking
   * whether the picker had been opened yet: an existing reaction someone else
   * made was refused until it had, and accepted afterwards (issue #496).
   */
  const handleToggleReaction = useCallback(
    (messageId: string, emoji: string) => {
      const key = `${messageId}\u0000${emoji}`;
      const now = Date.now();
      if (
        lastReactionToggleRef.current.key === key &&
        now - lastReactionToggleRef.current.at < 300
      ) {
        return;
      }
      lastReactionToggleRef.current = { key, at: now };
      toggleReaction(messageId, emoji);
    },
    [toggleReaction],
  );

  const handleEditForbidden = useCallback(
    (messageId: string) => {
      setEditDisabledIds((current) => new Set(current).add(messageId));
      retry();
    },
    [retry],
  );
  const openAuthorDM = useCallback(
    (message: Message) => {
      const recipientId = message.senderId;
      if (
        !recipientId ||
        recipientId === ctx.currentUserId ||
        openingAuthorDMRef.current.has(recipientId)
      ) {
        return;
      }
      const controller = new AbortController();
      const generation = authorDMGenerationRef.current;
      openingAuthorDMRef.current.set(recipientId, controller);
      setOpeningAuthorDMIds((current) => new Set(current).add(recipientId));
      setOpenDMError(null);
      void getOrCreateDirectDM(recipientId, controller.signal)
        .then(({ conversationId }) => {
          if (
            !authorDMMountedRef.current ||
            authorDMGenerationRef.current !== generation ||
            openingAuthorDMRef.current.get(recipientId) !== controller
          ) {
            return;
          }
          ctx.refreshConversations?.();
          navigate(`/chat/dm/${encodeURIComponent(conversationId)}`);
        })
        .catch((error: unknown) => {
          if (
            !authorDMMountedRef.current ||
            authorDMGenerationRef.current !== generation ||
            openingAuthorDMRef.current.get(recipientId) !== controller
          ) {
            return;
          }
          if (!(error instanceof DOMException && error.name === "AbortError")) {
            setOpenDMError("NÃ£o foi possÃ­vel abrir a conversa. Tente novamente.");
          }
        })
        .finally(() => {
          if (openingAuthorDMRef.current.get(recipientId) !== controller) return;
          openingAuthorDMRef.current.delete(recipientId);
          if (!authorDMMountedRef.current) return;
          setOpeningAuthorDMIds((current) => {
            const next = new Set(current);
            next.delete(recipientId);
            return next;
          });
        });
    },
    [ctx, navigate],
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

  const resourceCall = useResourceCallBar({
    kind,
    targetId,
    resolvedName,
    detailsKind,
    ctx,
  });

  const closeReferenceDialog = useCallback(() => setReferenceSource(null), []);
  const clearPendingReference = useCallback(
    () => navigate(`${location.pathname}${location.search}`, { replace: true, state: null }),
    [location.pathname, location.search, navigate],
  );

  // One object rather than a dozen props: the timeline hands every one of these
  // straight down to a message, and none of them means anything on its own here.
  const timelineActions: TimelineActions = {
    onLoadMore: loadMore,
    onToggleReaction: handleToggleReaction,
    onReplyMessage: selectReply,
    onReferenceMessage: setReferenceSource,
    onForwardMessage: selectForwardSource,
    onReferenceJump: jumpToReference,
    onOpenAuthorDM:
      ctx.currentUserId && (kind === "channel" || activeDM?.type === "group")
        ? openAuthorDM
        : undefined,
    openingAuthorDMIds,
    onToggleFavorite: toggleFavorite,
    onReconcileLinkSafety: reconcileLinkSafety,
    onEditMessage: editMessageLocal,
    onEditForbidden: handleEditForbidden,
    onDeleteMessage: deleteMessageLocal,
    onTogglePin: togglePin,
    onRetry: retry,
  };

  const directCallBarProps = directCallBar(kind, ctx.directCallSession, activeDM?.counterpart);

  return (
    <div
      className={`chat-msg-area${details.showDetails ? " chat-msg-area--with-details" : ""}`}
      data-testid="chat-message-area"
    >
      {/*
        The conversation column is always rendered, never conditionally, so the
        panel below is a trailing sibling: adding or removing it leaves this
        entire subtree — message list, composer, scroll container — reconciled in
        place rather than remounted.
      */}
      <div className="chat-msg-area__conversation">
        <ConversationHeader
          kind={kind}
          name={resolvedName}
          counterpart={activeDM?.counterpart}
          presenceTarget={target.presenceTarget}
          // #673: once the direct call bar takes over presentation for this DM,
          // the header suppresses its own call actions — the same pattern #657
          // established for the resource call header action.
          onStartCall={directCallBarProps ? undefined : ctx.startCall}
          resourceCall={resourceCall.headerState}
          details={details}
          detailsToggleRef={detailsToggleRef}
        />

        {/*
          #657: The bar is shown based on discovery (available) or participation
          (participating-local or participating-info).
        */}
        {resourceCall.barProps && <ActiveResourceCallBar {...resourceCall.barProps} />}

        {/* #673: the direct 1:1 counterpart of the bar above — mutually
            exclusive with it by construction (resourceCallKind is null for a
            1:1 DM, so activeResourceCallBarProps is never set here). */}
        {directCallBarProps && <ActiveDirectCallBar {...directCallBarProps} />}

        <PinnedBar pin={latestPin} onUnpin={togglePin} />

        <ConversationTimeline
          kind={kind}
          targetId={targetId}
          name={resolvedName}
          detailsKind={detailsKind}
          mentionTarget={target.mentionTarget}
          state={state}
          currentUserId={ctx.currentUserId}
          actions={timelineActions}
          editDisabledIds={editDisabledIds}
          pinnedIds={pinnedIds}
          recentReactionEmojis={recentReactionEmojis}
          emojiUsage={emojiUsage}
          onEmojiToneChange={changeEmojiTone}
          focusMessageId={focusMessageId}
          conversationKey={conversationKey}
          unreadCountAtOpen={unreadCountAtOpen}
          initialAnchor={initialAnchor}
          onCaptureAnchor={handleCaptureAnchor}
          onReachedBottom={handleReachedBottom}
        />

        <ConversationNotices
          sendError={state.sendError}
          realtimeError={state.realtimeError}
          actionError={state.actionError}
          openDMError={openDMError}
          pinError={pinError}
          typingLabel={typingIndicatorLabel}
        />

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
          mentionTarget={target.mentionTarget}
          bodyFormat={target.bodyFormat}
          placeholder={target.composerPlaceholder}
          disabled={state.status !== "ready"}
          replyPreview={replyPreview}
          onCancelReply={cancelReply}
          referencePreview={pendingReference.preview}
          referenceTargetLabel={pendingReference.originLabel}
          onCancelReference={clearPendingReference}
          onSend={handleSend}
          onActivity={handleComposerActivity}
          // The composer's emoji button opens the same picker the reactions use,
          // over the same history — one emoji experience in one product (#496).
          emoji={composerEmoji}
          // RF-32 (issue #458): the same composer serves channels and DMs, so
          // one target prop covers both. It is the route's own kind and id —
          // the very pair the composer is keyed by — so an attachment can never
          // be posted to the destination the user just navigated away from.
          uploadTarget={target.uploadTarget}
          attachmentLimits={ctx.attachmentLimits}
          // An upload adds a file to the destination without creating a message
          // — the message comes later, when the user presses Enviar (RF-32) —
          // so the details panel's file list still has to reconcile here.
          onAttachmentUploaded={reloadOpenDetails}
        />
        <ConversationDialogs
          kind={kind}
          targetId={targetId}
          channels={ctx.channels}
          dms={ctx.dms}
          referenceSource={referenceSource}
          forwardSource={forwardSource}
          onCloseReference={closeReferenceDialog}
          onSelectReferenceDestination={selectReferenceDestination}
          onCloseForward={closeForwardDialog}
        />
      </div>

      {details.showDetails && (
        <ConversationDetailsPanel
          kind={detailsKind ?? "channel"}
          state={details.detailsState}
          currentUserId={ctx.currentUserId}
          latestPin={latestPin}
          onClose={details.close}
        />
      )}
    </div>
  );
}
