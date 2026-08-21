import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type KeyboardEvent as ReactKeyboardEvent,
  type RefObject,
} from "react";
import { createPortal } from "react-dom";

import { linkSafetyAllowsAnchors } from "./chatTypes";
import type { LinkSafetyRecheck, Message } from "./chatTypes";
import InlineMessageEditor from "./InlineMessageEditor";
import MessageAttachments from "./MessageAttachments";
import MessageEditHistory from "./MessageEditHistory";
import { formatTime, senderLabel } from "./messageDisplay";
import { presenceLabel, usePresence, type PresenceState } from "./presence";
import PresenceDot from "./PresenceDot";
import { canToggleReaction, maxReactionsPerUserPerMessage } from "./reactionLimit";
import RichTextRenderer from "./RichTextRenderer";
import type { CodecFormat } from "./tiptapSerializer";

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

export interface MessageBubbleProps {
  message: Message;
  /** True when this message is from the viewing user (we don't know real user ID here). */
  isMine?: boolean;
  /** True when consecutive messages from the same sender within the same minute. */
  isGrouped?: boolean;
  onToggleReaction: (messageId: string, emoji: string) => void;
  onReplyMessage: (message: Message) => void;
  onReferenceMessage: (message: Message, trigger: HTMLElement) => void;
  onForwardMessage?: (message: Message) => void;
  onToggleFavorite: (messageId: string, isFavorited: boolean) => void;
  onEditMessage: (
    messageId: string,
    body: string,
    bodyFormat: Message["bodyFormat"],
  ) => Promise<Message>;
  onEditForbidden: (messageId: string) => void;
  onDeleteMessage: (messageId: string) => Promise<void>;
  editDisabled?: boolean;
  channelId?: string;
  /** The conversation on screen; presence is resolved within it (RF-58). */
  presenceTarget?: string;
  /** RF-05: pin/unpin action for readable channels and DMs. */
  onTogglePin?: (messageId: string, pin: boolean) => void;
  /** RF-05: whether this message is currently pinned in the active target. */
  isPinned?: boolean;
  allowedReactionEmojis: string[];
  recentReactionEmojis: string[];
  reactionMenuVisible: boolean;
  onReactionMenuVisibleChange: (messageId: string, visible: boolean) => void;
  pickerOpen: boolean;
  onPickerOpenChange: (messageId: string, open: boolean) => void;
  quoteAuthorLabel?: string;
  canJumpToQuote?: boolean;
  onQuoteJump?: (messageId: string) => void;
  onReferenceJump?: (reference: NonNullable<Message["reference"]>) => void;
  isHighlighted?: boolean;
  setMessageRef?: (messageId: string, el: HTMLDivElement | null) => void;
  /**
   * RF-21 "Verificar novamente" (issue #135). Asks the server to re-read what it
   * already knows about this message's unverified links; it never starts a new
   * scan, and the notice's copy must not imply that it does.
   *
   * Optional: without it the notice is still shown, just without the action. The
   * warning is the important half — the action is a convenience.
   */
  onReconcileLinkSafety?: (messageId: string) => Promise<LinkSafetyRecheck | undefined>;
}

/**
 * The exact notice shown above a message whose links could not be verified
 * (issue #135).
 *
 * The wording is deliberate and is not a security warning. Cloudflare's real
 * production answer for this case was an operational refusal — the hostname had
 * been scanned too recently — which is evidence of nothing about the link. So
 * this says what is actually true: the check did not complete, and the automatic
 * preview was therefore not loaded. It does not call the link dangerous, and it
 * does not call it safe.
 */
const linkUnverifiedNotice =
  "Não foi possível verificar este link agora. A prévia automática não foi carregada.";

/**
 * What a reader is told about a link the provider condemned after the message
 * had already been delivered (issue #135).
 *
 * This one *is* a security statement, and it is the only one in this file, which
 * is why it reads nothing like the notice above.
 */
const linkBlockedNotice = "Este link foi bloqueado após a verificação de segurança.";

/**
 * What a quote, a cross-target reference, or an edit-history entry shows in
 * place of a body whose links this deployment condemned (issue #135, CQ-002).
 *
 * The server already sends an empty body for those, so this is presentation
 * only — it exists so the reader is told the content was withheld rather than
 * being shown an empty block that reads like a rendering bug.
 */
const withheldBodyNotice = "Conteúdo ocultado por segurança.";

type MessageReactionsProps = Pick<
  MessageBubbleProps,
  | "message"
  | "isMine"
  | "onToggleReaction"
  | "onReplyMessage"
  | "onReferenceMessage"
  | "onForwardMessage"
  | "onToggleFavorite"
  | "onTogglePin"
  | "isPinned"
  | "allowedReactionEmojis"
  | "recentReactionEmojis"
  | "reactionMenuVisible"
  | "onReactionMenuVisibleChange"
  | "pickerOpen"
  | "onPickerOpenChange"
> & {
  bubbleRef: RefObject<HTMLDivElement | null>;
  onStartEdit?: () => void;
  onDelete?: () => void;
  deleting?: boolean;
  actionsOpen: boolean;
  onActionsOpenChange: (open: boolean | ((current: boolean) => boolean)) => void;
};

function MessageReactions({
  message,
  isMine = false,
  bubbleRef,
  onToggleReaction,
  onReplyMessage,
  onReferenceMessage,
  onForwardMessage,
  onToggleFavorite,
  onTogglePin,
  isPinned = false,
  allowedReactionEmojis,
  recentReactionEmojis,
  reactionMenuVisible,
  onReactionMenuVisibleChange,
  pickerOpen,
  onPickerOpenChange,
  onStartEdit,
  onDelete,
  deleting = false,
  actionsOpen,
  onActionsOpenChange,
}: MessageReactionsProps) {
  const menuRef = useRef<HTMLDivElement>(null);
  const anchorRef = useRef<HTMLButtonElement>(null);
  const pickerRef = useRef<HTMLDivElement>(null);
  const actionsRef = useRef<HTMLDivElement>(null);
  const actionsButtonRef = useRef<HTMLButtonElement>(null);

  const positionMenu = useCallback(() => {
    if (!reactionMenuVisible || !bubbleRef.current || !menuRef.current) return;
    const bubble = bubbleRef.current.getBoundingClientRect();
    if (bubble.bottom < 0 || bubble.top > window.innerHeight) return;
    const menu = menuRef.current.getBoundingClientRect();
    // gapAbove = 0: cola a borda da toolbar exatamente no início da mensagem (feedback de UX, issue #331)
    const gapAbove = 0;
    const gapBelow = 6;
    const viewportPadding = 8;
    const midX = bubble.left + bubble.width / 2;
    const left = Math.min(
      Math.max(viewportPadding, isMine ? midX - menu.width : midX),
      window.innerWidth - menu.width - viewportPadding,
    );
    const above = bubble.top - menu.height - gapAbove;
    const top =
      above >= viewportPadding
        ? above
        : Math.min(bubble.bottom + gapBelow, window.innerHeight - menu.height - viewportPadding);
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

  useEffect(() => {
    if (!actionsOpen) return;
    const closeOnOutsideClick = (event: MouseEvent) => {
      if (!actionsRef.current?.contains(event.target as Node)) onActionsOpenChange(false);
    };
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === "Escape") onActionsOpenChange(false);
    };
    document.addEventListener("mousedown", closeOnOutsideClick);
    document.addEventListener("keydown", closeOnEscape);
    return () => {
      document.removeEventListener("mousedown", closeOnOutsideClick);
      document.removeEventListener("keydown", closeOnEscape);
    };
  }, [actionsOpen, onActionsOpenChange]);

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
    if (canToggleReaction(message.reactions, emoji)) onPickerOpenChange(message.id, false);
  };

  const reactionToggleHint = `Você pode adicionar no máximo ${maxReactionsPerUserPerMessage} reações por mensagem. Remova uma reação sua para adicionar outra.`;

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
              aria-disabled={!canToggleReaction(message.reactions, reaction.emoji)}
              title={
                canToggleReaction(message.reactions, reaction.emoji)
                  ? undefined
                  : reactionToggleHint
              }
              onClick={() => onToggleReaction(message.id, reaction.emoji)}
            >
              <span aria-hidden="true">{reaction.emoji}</span> {reaction.count}
            </button>
          ))}
        </div>
      )}
      {reactionMenuVisible && (
        <div
          ref={menuRef}
          className="chat-msg-area__reaction-menu"
          role="toolbar"
          aria-label="Reagir à mensagem"
          onMouseEnter={() => onReactionMenuVisibleChange(message.id, true)}
          style={{ visibility: "hidden" }}
        >
          {recentReactionEmojis.map((emoji) => (
            <button
              key={emoji}
              type="button"
              aria-label={`Reagir rapidamente com ${emoji}`}
              aria-disabled={!canToggleReaction(message.reactions, emoji)}
              title={canToggleReaction(message.reactions, emoji) ? undefined : reactionToggleHint}
              onClick={() => onToggleReaction(message.id, emoji)}
            >
              {emoji}
            </button>
          ))}
          <button type="button" aria-label="Responder" onClick={() => onReplyMessage(message)}>
            <span className="material-symbols-outlined" aria-hidden="true">
              reply
            </span>
          </button>
          <div ref={actionsRef} className="chat-msg-area__more-actions">
            <button
              type="button"
              ref={actionsButtonRef}
              aria-label="Mais ações"
              aria-expanded={actionsOpen}
              aria-haspopup="true"
              onClick={() => onActionsOpenChange((open) => !open)}
            >
              <span className="material-symbols-outlined" aria-hidden="true">
                more_vert
              </span>
            </button>
            {actionsOpen && (
              <div
                className="chat-msg-area__more-actions-menu"
                role="group"
                aria-label="Ações da mensagem"
              >
                <button
                  type="button"
                  onClick={(event) => {
                    event.currentTarget.focus();
                    onReferenceMessage(message, actionsButtonRef.current ?? event.currentTarget);
                  }}
                >
                  Citar em outra conversa
                </button>
                {onForwardMessage && message.kind === "user" && (
                  <button
                    type="button"
                    onClick={() => {
                      onForwardMessage(message);
                      onActionsOpenChange(false);
                    }}
                  >
                    Encaminhar
                  </button>
                )}
                {onStartEdit && (
                  <button
                    type="button"
                    onClick={() => {
                      onStartEdit();
                      onActionsOpenChange(false);
                    }}
                  >
                    Editar mensagem
                  </button>
                )}
                {onDelete && (
                  <button
                    type="button"
                    aria-busy={deleting}
                    disabled={deleting}
                    onClick={() => {
                      onDelete();
                    }}
                  >
                    {deleting ? "Excluindo mensagem" : "Excluir mensagem"}
                  </button>
                )}
                <button
                  type="button"
                  aria-pressed={message.isFavorited}
                  onClick={() => {
                    onToggleFavorite(message.id, !message.isFavorited);
                    onActionsOpenChange(false);
                  }}
                >
                  {message.isFavorited ? "Remover dos favoritos" : "Favoritar mensagem"}
                </button>
                {onTogglePin && (
                  <button
                    type="button"
                    aria-pressed={isPinned}
                    onClick={() => {
                      onTogglePin(message.id, !isPinned);
                      onActionsOpenChange(false);
                    }}
                  >
                    {isPinned ? "Desafixar mensagem" : "Fixar mensagem"}
                  </button>
                )}
              </div>
            )}
          </div>
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
                    aria-disabled={!canToggleReaction(message.reactions, emoji)}
                    title={
                      canToggleReaction(message.reactions, emoji) ? undefined : reactionToggleHint
                    }
                    onClick={() => selectReaction(emoji)}
                  >
                    {emoji}
                  </button>
                ))}
              </div>,
              document.body,
            )}
        </div>
      )}
    </>
  );
}

function MessageMeta({
  message,
  isMine,
  isGrouped,
  senderPresence,
  onOpenHistory,
}: Pick<MessageBubbleProps, "message" | "isMine" | "isGrouped"> & {
  senderPresence: PresenceState;
  onOpenHistory: () => void;
}) {
  if (isGrouped && !message.isEdited) return null;
  return (
    <div className="chat-msg-area__msg-meta">
      {!isGrouped && !isMine && (
        <span className="chat-msg-area__msg-sender" data-testid="chat-msg-sender">
          {senderLabel(message)}
        </span>
      )}
      {/* The dot next to the avatar is decorative and lives inside an
          aria-hidden wrapper, so without this the sender's state would be
          conveyed by colour alone. It sits beside the name, in the same place
          and under the same condition, so a screen reader hears the person and
          their state once per group rather than once per message — and says
          nothing at all when the server has not answered yet, since "unknown"
          is the absence of a state and not a fourth one. */}
      {!isGrouped && !isMine && senderPresence !== "unknown" && (
        <span className="sr-only" data-testid="chat-msg-sender-presence">
          {`Status: ${presenceLabel(senderPresence)}`}
        </span>
      )}
      {!isGrouped && (
        <span className="chat-msg-area__msg-time">{formatTime(message.createdAt)}</span>
      )}
      {message.isEdited && !message.isRemoved && (
        <button
          type="button"
          className="chat-msg-area__edited"
          aria-label="Ver histórico de edições"
          aria-haspopup="dialog"
          onClick={onOpenHistory}
        >
          (editada)
        </button>
      )}
    </div>
  );
}

interface QuoteBlockProps {
  quote: NonNullable<Message["quoted"]>;
  authorLabel: string;
  canJump: boolean;
  onJump?: (messageId: string) => void;
}

function QuoteBlock({ quote, authorLabel, canJump, onJump }: QuoteBlockProps) {
  const jump = () => {
    if (canJump) onJump?.(quote.id);
  };
  const handleKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    if (!canJump || (event.key !== "Enter" && event.key !== " ")) return;
    event.preventDefault();
    jump();
  };

  return (
    <div
      className={`chat-msg-area__quote${canJump ? " chat-msg-area__quote--clickable" : " chat-msg-area__quote--disabled"}`}
      role={canJump ? "button" : undefined}
      tabIndex={canJump ? 0 : undefined}
      aria-disabled={canJump ? undefined : true}
      aria-label={canJump ? `Ir para mensagem original de ${authorLabel}` : undefined}
      onClick={canJump ? jump : undefined}
      onKeyDown={handleKeyDown}
      data-testid="chat-message-quote"
    >
      <div className="chat-msg-area__quote-author">{authorLabel}</div>
      <div className="chat-msg-area__quote-excerpt">
        {quote.isRemoved ? (
          "Mensagem original indisponível."
        ) : quote.linkSafetyState === "malicious" ? (
          <span className="chat-msg-area__link-blocked-body">{withheldBodyNotice}</span>
        ) : (
          <RichTextRenderer text={quote.bodyText} bodyFormat={quote.bodyFormat} />
        )}
      </div>
    </div>
  );
}

function ReferenceBlock({
  reference,
  onJump,
}: {
  reference: NonNullable<Message["reference"]>;
  onJump?: (reference: NonNullable<Message["reference"]>) => void;
}) {
  if (!reference.available) {
    return (
      <div
        className="chat-msg-area__reference chat-msg-area__reference--unavailable"
        aria-disabled="true"
        data-testid="chat-message-reference"
      >
        citação indisponível
      </div>
    );
  }
  const targetLabel = reference.targetLabel
    ? `${reference.targetType === "channel" ? "#" : ""}${reference.targetLabel}`
    : reference.targetType === "channel"
      ? "Canal"
      : "Conversa";
  return (
    <div
      className="chat-msg-area__reference chat-msg-area__reference--clickable"
      data-testid="chat-message-reference"
      role="link"
      tabIndex={0}
      aria-label="Ir para mensagem citada"
      onClick={() => onJump?.(reference)}
      onKeyDown={(event) => {
        if (event.key === "Enter" || event.key === " ") {
          event.preventDefault();
          onJump?.(reference);
        }
      }}
    >
      <span className="chat-msg-area__reference-origin">{targetLabel}</span>
      <div className="chat-msg-area__reference-author">
        {reference.authorDisplayName || "Usuário"}
      </div>
      <div className="chat-msg-area__reference-body">
        {reference.linkSafetyState === "malicious" ? (
          <span className="chat-msg-area__link-blocked-body">{withheldBodyNotice}</span>
        ) : (
          <RichTextRenderer text={reference.bodyText} bodyFormat={reference.bodyFormat} />
        )}
      </div>
    </div>
  );
}

/**
 * The "could not verify this link" notice, and its optional re-check action
 * (issue #135).
 *
 * # Why it renders above the message and not instead of it
 *
 * The message was published. `inconclusive` means this server has no clearance
 * to fetch the URL on its own behalf — it is a statement about our authority, not
 * about the link — so the content is drawn exactly as any other message's and the
 * notice sits above it as context.
 *
 * # What this component does not do
 *
 * It does not touch the link. There is no fetch, no HEAD, no `<link rel=preload>`,
 * no prefetch and no image whose src is the message's URL anywhere in this file
 * or in the renderer it wraps. RichTextRenderer may produce an anchor that only
 * navigates after the reader clicks it; it produces no automatic fetch or
 * subresource. The reader's own browser is free to do whatever they ask of it
 * when they copy or click; that is their browser, not our server, and the
 * distinction is the whole point of the state.
 *
 * The button asks the backend to re-read a verdict it may already have. It does
 * not start a scan, and it deliberately says "Verificar novamente" rather than
 * anything that would promise one. It disables itself while in flight and for the
 * interval the server suggests, so it cannot become a poll.
 */
function LinkSafetyNotice({
  messageId,
  onReconcile,
}: {
  messageId: string;
  onReconcile?: (messageId: string) => Promise<LinkSafetyRecheck | undefined>;
}) {
  const [checking, setChecking] = useState(false);
  // When the server's cooldown lifts, as an epoch millisecond. Null means "not
  // waiting", and the timer below is what clears it. Kept in component state
  // only: the backend is the authority on the rate limit and re-applies it on
  // every request, so a reload simply asks again and is refused there. This is
  // ergonomics — it stops the button offering an action that will be rejected.
  const [blockedUntil, setBlockedUntil] = useState<number | null>(null);
  // Guards a setState after the bubble is gone — a message can be deleted, or the
  // conversation switched, while the request is in flight.
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  // One timer, armed only while a cooldown is actually running, so an idle notice
  // costs nothing. The clearing happens in the timer callback rather than in the
  // effect body: an effect that calls setState synchronously is a cascading
  // render, and a zero delay is enough to make an already-elapsed cooldown lift
  // on the next tick.
  useEffect(() => {
    if (blockedUntil === null) return;
    const remaining = Math.max(0, blockedUntil - Date.now());
    const timer = window.setTimeout(() => {
      if (mounted.current) setBlockedUntil(null);
    }, remaining);
    return () => window.clearTimeout(timer);
  }, [blockedUntil]);

  const disabled = checking || blockedUntil !== null;

  const recheck = useCallback(async () => {
    if (!onReconcile || checking || blockedUntil !== null) return;
    setChecking(true);
    try {
      const result = await onReconcile(messageId);
      // The server tells us how long its own cooldown runs for. Honouring it is
      // what keeps the button from becoming a poll: asking again sooner would be
      // refused, and offering an action that will be refused is a lie about what
      // the control does.
      if (mounted.current && result?.retryAfterSeconds) {
        setBlockedUntil(Date.now() + result.retryAfterSeconds * 1000);
      }
    } finally {
      // Re-enabled either way. A state that actually changed removes this notice
      // entirely, so the only case where the button comes back is the one where
      // nothing was learned.
      if (mounted.current) setChecking(false);
    }
  }, [blockedUntil, checking, messageId, onReconcile]);

  return (
    <div
      className="chat-msg-area__link-notice"
      data-testid="chat-message-link-unverified"
      role="note"
    >
      <span className="material-symbols-outlined" aria-hidden="true">
        info
      </span>
      <span className="chat-msg-area__link-notice-text">{linkUnverifiedNotice}</span>
      {onReconcile && (
        <button
          type="button"
          className="chat-msg-area__link-notice-action"
          data-testid="chat-message-link-recheck"
          onClick={() => void recheck()}
          disabled={disabled}
        >
          {checking ? "Verificando…" : "Verificar novamente"}
        </button>
      )}
    </div>
  );
}

export default function MessageBubble({
  message,
  isMine = false,
  isGrouped = false,
  onToggleReaction,
  onReplyMessage,
  onReferenceMessage,
  onForwardMessage,
  onToggleFavorite,
  onEditMessage,
  onEditForbidden,
  onDeleteMessage,
  editDisabled = false,
  channelId,
  presenceTarget,
  onTogglePin,
  isPinned = false,
  allowedReactionEmojis,
  recentReactionEmojis,
  reactionMenuVisible,
  onReactionMenuVisibleChange,
  pickerOpen,
  onPickerOpenChange,
  quoteAuthorLabel,
  canJumpToQuote = false,
  onQuoteJump,
  onReferenceJump,
  isHighlighted = false,
  setMessageRef,
  onReconcileLinkSafety,
}: MessageBubbleProps) {
  const bubbleRef = useRef<HTMLDivElement>(null);
  // RF-21 (issue #135). Only a condemned link withdraws content; `inconclusive`
  // deliberately does not, because there is no evidence against it and dressing
  // an operational refusal up as a block teaches readers the wrong thing about
  // both states.
  const linkBlocked = message.linkSafetyState === "malicious";
  // Whether the URLs in this body may be drawn as anchors.
  //
  // The state test is linkSafetyAllowsAnchors, which is an allowlist of exactly
  // the two states representing a *completed* check. Everything else — the legacy
  // empty state, and `unknown`, which is what the decoder produces for a server
  // value this build does not recognise — renders as literal text.
  //
  // `status === "active"` is still required, because a withheld message was never
  // published and has no public link, but it is never sufficient on its own: since
  // #135 a published message is not a verified one.
  //
  const linkSafetyState = message.linkSafetyState ?? "";
  const linksClickable =
    !message.isRemoved && message.status === "active" && linkSafetyAllowsAnchors(linkSafetyState);
  const [editing, setEditing] = useState(false);
  const [historyOpen, setHistoryOpen] = useState(false);
  const [actionsOpen, setActionsOpen] = useState(false);
  const [deleting, setDeleting] = useState(false);
  // Read for every message, including the user's own, so the hook order does
  // not depend on who wrote it. Only the other party's avatar is drawn.
  //
  // Scoped to the conversation being read: the sender wrote here, so this
  // conversation's roster is the one that would have named them.
  const senderPresence = usePresence(message.senderId, presenceTarget);
  const saveEdit = useCallback(
    async (body: string, format: CodecFormat) => {
      const updated = await onEditMessage(message.id, body, format);
      setEditing(false);
      return updated;
    },
    [message.id, onEditMessage],
  );
  const handleForbidden = useCallback(() => {
    setEditing(false);
    onEditForbidden(message.id);
  }, [message.id, onEditForbidden]);
  const handleDelete = useCallback(async () => {
    if (deleting || !window.confirm("Excluir esta mensagem?")) return;
    setDeleting(true);
    try {
      await onDeleteMessage(message.id);
    } catch {
      setDeleting(false);
    }
  }, [deleting, message.id, onDeleteMessage]);
  return (
    <div
      ref={(el) => setMessageRef?.(message.id, el)}
      className={`chat-msg-area__msg${isMine ? " chat-msg-area__msg--mine" : ""}${isGrouped ? " chat-msg-area__msg--grouped" : ""}${isHighlighted ? " chat-msg-area__msg--highlight" : ""}`}
      data-testid="chat-msg-bubble"
      data-message-id={message.id}
      tabIndex={0}
      onMouseEnter={() => onReactionMenuVisibleChange(message.id, true)}
      onMouseLeave={(event) => {
        const nextTarget = event.relatedTarget;
        if (!(nextTarget instanceof Node) || !event.currentTarget.contains(nextTarget)) {
          setActionsOpen(false);
          onReactionMenuVisibleChange(message.id, false);
        }
      }}
      onFocus={() => onReactionMenuVisibleChange(message.id, true)}
      onBlur={(event) => {
        if (!event.currentTarget.contains(event.relatedTarget)) {
          setActionsOpen(false);
          onReactionMenuVisibleChange(message.id, false);
        }
      }}
      onTouchStart={() => onReactionMenuVisibleChange(message.id, true)}
    >
      {!isMine && (
        <div className="chat-msg-area__msg-avatar" aria-hidden="true">
          {senderInitials(message)}
          {/* Decoration only, like the avatar it sits on: the sender's name is
              already in the message meta above, and repeating "Ausente" once
              per message would make a conversation unbearable to listen to. */}
          <PresenceDot state={senderPresence} size="sm" />
        </div>
      )}
      <div className="chat-msg-area__msg-body">
        <MessageMeta
          message={message}
          isMine={isMine}
          isGrouped={isGrouped}
          senderPresence={senderPresence}
          onOpenHistory={() => setHistoryOpen(true)}
        />
        <div
          ref={bubbleRef}
          className={`chat-msg-area__msg-bubble${message.isRemoved ? " chat-msg-area__msg-bubble--removed" : ""}${
            message.status === "pending_link_scan" ? " chat-msg-area__msg-bubble--pending-scan" : ""
          }`}
        >
          {/*
            RF-21. Only the sender ever receives a message in this state — the
            backend withholds it from every other reader — so this is what stops
            the composer from claiming a delivery that has not happened. The
            state is rendered, never decided: nothing here inspects a link, and
            the notice disappears only when the backend promotes the message.
          */}
          {message.status === "pending_link_scan" && (
            <div className="chat-msg-area__pending-scan" data-testid="chat-message-pending-scan">
              <span className="material-symbols-outlined" aria-hidden="true">
                shield
              </span>
              Verificando segurança dos links…
            </div>
          )}
          {/*
            RF-21 (issue #135). Above the content, so a reader sees the caveat
            before the link it is about. The two states are deliberately
            different in tone and in consequence:

            - inconclusive: the check did not complete. The message renders in
              full, unchanged, and this is context rather than a warning. It is
              not a claim about the link, and the copy must never become one;
            - malicious: the provider condemned a link after the message had
              already been delivered. The body is withheld the same way a removed
              message's is, so the URL cannot be read off the screen, copied or
              acted on — the message, its author and its timestamp stay, so the
              conversation still makes sense.
          */}
          {!message.isRemoved && message.linkSafetyState === "inconclusive" && (
            <LinkSafetyNotice messageId={message.id} onReconcile={onReconcileLinkSafety} />
          )}
          {!message.isRemoved && linkBlocked && (
            <div className="chat-msg-area__link-blocked" data-testid="chat-message-link-blocked">
              <span className="material-symbols-outlined" aria-hidden="true">
                gpp_maybe
              </span>
              {linkBlockedNotice}
            </div>
          )}
          {message.isForwarded && !message.isRemoved && (
            <div className="chat-msg-area__forwarded" data-testid="chat-message-forwarded">
              <span className="material-symbols-outlined" aria-hidden="true">
                forward
              </span>
              Mensagem encaminhada
            </div>
          )}
          {message.reference && !message.isRemoved && (
            <ReferenceBlock reference={message.reference} onJump={onReferenceJump} />
          )}
          {/* Quote ocultado em mensagens removidas para não misturar contexto residual com o placeholder de remoção. */}
          {message.quoted && !message.isRemoved && (
            <QuoteBlock
              quote={message.quoted}
              authorLabel={quoteAuthorLabel ?? "Usuário desconhecido"}
              canJump={canJumpToQuote}
              onJump={onQuoteJump}
            />
          )}
          {message.isRemoved ? (
            "Mensagem removida."
          ) : linkBlocked ? (
            // The body is withheld rather than rendered with the link struck
            // through: the body *is* the link, as far as the risk goes, and a
            // URL a reader can select and paste is a URL the block did not stop.
            // Nothing here is clickable and nothing is fetched.
            <span className="chat-msg-area__link-blocked-body">{withheldBodyNotice}</span>
          ) : editing ? (
            <InlineMessageEditor
              message={message}
              channelId={channelId}
              onSave={saveEdit}
              onCancel={() => setEditing(false)}
              onForbidden={handleForbidden}
            />
          ) : (
            <RichTextRenderer
              text={message.bodyText}
              bodyFormat={message.bodyFormat}
              linksClickable={linksClickable}
            />
          )}
          {/* RF-32. Below the body, so an attachment sent with text reads as
              part of the same message, and hidden for a removed one along with
              everything else the placeholder replaces. Editing does not touch
              attachments, so they stay visible while the body is being edited. */}
          {!message.isRemoved && <MessageAttachments attachments={message.attachments} />}
        </div>
        <MessageReactions
          message={message}
          isMine={isMine}
          bubbleRef={bubbleRef}
          onToggleReaction={onToggleReaction}
          onReplyMessage={onReplyMessage}
          onReferenceMessage={onReferenceMessage}
          onForwardMessage={onForwardMessage}
          onToggleFavorite={onToggleFavorite}
          onTogglePin={onTogglePin}
          isPinned={isPinned}
          allowedReactionEmojis={allowedReactionEmojis}
          recentReactionEmojis={recentReactionEmojis}
          reactionMenuVisible={reactionMenuVisible || pickerOpen}
          onReactionMenuVisibleChange={onReactionMenuVisibleChange}
          pickerOpen={pickerOpen}
          onPickerOpenChange={onPickerOpenChange}
          onStartEdit={
            isMine && !editDisabled && !editing
              ? () => {
                  setEditing(true);
                  onReactionMenuVisibleChange(message.id, false);
                }
              : undefined
          }
          onDelete={
            isMine && message.kind === "user" && !editing ? () => void handleDelete() : undefined
          }
          deleting={deleting}
          actionsOpen={actionsOpen}
          onActionsOpenChange={setActionsOpen}
        />
        {historyOpen && !message.isRemoved && (
          <MessageEditHistory messageId={message.id} onClose={() => setHistoryOpen(false)} />
        )}
      </div>
    </div>
  );
}
