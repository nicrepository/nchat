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

import type { Message } from "./chatTypes";
import InlineMessageEditor from "./InlineMessageEditor";
import MessageEditHistory from "./MessageEditHistory";
import { formatTime, senderLabel } from "./messageDisplay";
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
  onReferenceMessage: (message: Message) => void;
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
}

type MessageReactionsProps = Pick<
  MessageBubbleProps,
  | "message"
  | "isMine"
  | "onToggleReaction"
  | "onReplyMessage"
  | "onReferenceMessage"
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
};

function MessageReactions({
  message,
  isMine = false,
  bubbleRef,
  onToggleReaction,
  onReplyMessage,
  onReferenceMessage,
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
}: MessageReactionsProps) {
  const menuRef = useRef<HTMLDivElement>(null);
  const anchorRef = useRef<HTMLButtonElement>(null);
  const pickerRef = useRef<HTMLDivElement>(null);

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
          <button
            type="button"
            aria-label="Citar em outra conversa"
            onClick={() => onReferenceMessage(message)}
          >
            <span className="material-symbols-outlined" aria-hidden="true">
              format_quote
            </span>
          </button>
          {onStartEdit && (
            <button type="button" aria-label="Editar mensagem" onClick={onStartEdit}>
              <span className="material-symbols-outlined" aria-hidden="true">
                edit
              </span>
            </button>
          )}
          {onDelete && (
            <button
              type="button"
              aria-label={deleting ? "Excluindo mensagem" : "Excluir mensagem"}
              aria-busy={deleting}
              disabled={deleting}
              onClick={onDelete}
            >
              <span className="material-symbols-outlined" aria-hidden="true">
                {deleting ? "progress_activity" : "delete"}
              </span>
            </button>
          )}
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
              aria-label={isPinned ? "Desafixar mensagem" : "Fixar mensagem"}
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
        </div>
      )}
    </>
  );
}

function MessageMeta({
  message,
  isMine,
  isGrouped,
  onOpenHistory,
}: Pick<MessageBubbleProps, "message" | "isMine" | "isGrouped"> & {
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
        <RichTextRenderer text={reference.bodyText} bodyFormat={reference.bodyFormat} />
      </div>
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
  onToggleFavorite,
  onEditMessage,
  onEditForbidden,
  onDeleteMessage,
  editDisabled = false,
  channelId,
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
}: MessageBubbleProps) {
  const bubbleRef = useRef<HTMLDivElement>(null);
  const [editing, setEditing] = useState(false);
  const [historyOpen, setHistoryOpen] = useState(false);
  const [deleting, setDeleting] = useState(false);
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
          onReactionMenuVisibleChange(message.id, false);
        }
      }}
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
        <MessageMeta
          message={message}
          isMine={isMine}
          isGrouped={isGrouped}
          onOpenHistory={() => setHistoryOpen(true)}
        />
        <div
          ref={bubbleRef}
          className={`chat-msg-area__msg-bubble${message.isRemoved ? " chat-msg-area__msg-bubble--removed" : ""}`}
        >
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
          ) : editing ? (
            <InlineMessageEditor
              message={message}
              channelId={channelId}
              onSave={saveEdit}
              onCancel={() => setEditing(false)}
              onForbidden={handleForbidden}
            />
          ) : (
            <RichTextRenderer text={message.bodyText} bodyFormat={message.bodyFormat} />
          )}
        </div>
        <MessageReactions
          message={message}
          isMine={isMine}
          bubbleRef={bubbleRef}
          onToggleReaction={onToggleReaction}
          onReplyMessage={onReplyMessage}
          onReferenceMessage={onReferenceMessage}
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
        />
        {historyOpen && !message.isRemoved && (
          <MessageEditHistory messageId={message.id} onClose={() => setHistoryOpen(false)} />
        )}
      </div>
    </div>
  );
}
