/**
 * The reaction badges under a message, and the toolbar that appears over it.
 *
 * Split out of MessageBubble (issue #496, CQ follow-up): the bubble is about
 * showing what was said, this is about what can be done to it. The two shared a
 * file and a component, and the picker's placement rules had nowhere of their
 * own to live.
 */

import { lazy, Suspense, useCallback, useEffect, useLayoutEffect, useRef } from "react";
import { createPortal } from "react-dom";
import type { RefObject } from "react";

import type { Message } from "./chatTypes";
import type { EmojiUsage } from "./emoji/emojiUsage";
import ReactionBadge from "./ReactionBadge";
import { useReactionPresence } from "./useReactionPresence";
import { placeAgainstAnchor, useAnchoredPicker } from "./emoji/useAnchoredPicker";

/**
 * The full picker and its catalog are a chunk of their own (issue #496): a
 * conversation that never opens one never downloads a thousand emoji names.
 */
const EmojiPicker = lazy(() => import("./emoji/EmojiPicker"));

export interface MessageToolbarProps {
  message: Message;
  /** Placement mirrors for the reader's own messages, which sit on the right. */
  isMine: boolean;
  bubbleRef: RefObject<HTMLDivElement | null>;
  /** The reader, so a reaction tooltip can say "Você" rather than their name. */
  currentUserId: string;
  recentReactionEmojis: string[];
  emojiUsage: EmojiUsage;
  onEmojiToneChange: (tone: number) => void;
  onToggleReaction: (messageId: string, emoji: string) => void;
  onReplyMessage: (message: Message) => void;
  onReferenceMessage: (message: Message) => void;
  onForwardMessage?: (message: Message) => void;
  onToggleFavorite: (messageId: string, isFavorited: boolean) => void;
  onTogglePin?: (messageId: string, pin: boolean) => void;
  isPinned: boolean;
  reactionMenuVisible: boolean;
  onReactionMenuVisibleChange: (messageId: string, visible: boolean) => void;
  pickerOpen: boolean;
  onPickerOpenChange: (messageId: string, open: boolean) => void;
  onStartEdit?: () => void;
  onDelete?: () => void;
  deleting: boolean;
}

type MessageActionButtonsProps = Pick<
  MessageToolbarProps,
  | "message"
  | "onReplyMessage"
  | "onReferenceMessage"
  | "onForwardMessage"
  | "onToggleFavorite"
  | "onTogglePin"
  | "isPinned"
  | "onStartEdit"
  | "onDelete"
  | "deleting"
>;

/**
 * The two actions the author of a message has over it. They are a pair — both
 * conditional on the caller offering them, and the delete button carries an
 * in-flight state the others do not — so they are extracted together.
 */
function MessageEditDeleteButtons({
  onStartEdit,
  onDelete,
  deleting,
}: Pick<MessageToolbarProps, "onStartEdit" | "onDelete" | "deleting">) {
  return (
    <>
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
    </>
  );
}

/**
 * Everything the hover toolbar offers that is not a reaction: reply, quote,
 * forward, edit, delete, favourite and pin.
 */
function MessageActionButtons({
  message,
  onReplyMessage,
  onReferenceMessage,
  onForwardMessage,
  onToggleFavorite,
  onTogglePin,
  isPinned,
  onStartEdit,
  onDelete,
  deleting,
}: MessageActionButtonsProps) {
  return (
    <>
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
      {onForwardMessage && message.kind === "user" && (
        <button type="button" aria-label="Encaminhar" onClick={() => onForwardMessage(message)}>
          <span className="material-symbols-outlined" aria-hidden="true">
            forward
          </span>
        </button>
      )}
      <MessageEditDeleteButtons onStartEdit={onStartEdit} onDelete={onDelete} deleting={deleting} />
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
    </>
  );
}

type PlacementInput = Pick<
  MessageToolbarProps,
  | "isMine"
  | "bubbleRef"
  | "reactionMenuVisible"
  | "pickerOpen"
  | "onPickerOpenChange"
  | "onReactionMenuVisibleChange"
> & { messageId: string };

interface Placement {
  menuRef: RefObject<HTMLDivElement | null>;
  anchorRef: RefObject<HTMLButtonElement | null>;
  pickerRef: RefObject<HTMLDivElement | null>;
  closePicker: (restoreFocus: boolean) => void;
}

/**
 * Where the hover toolbar sits, and when the picker it holds closes.
 *
 * The toolbar floats outside the message's own box, so it cannot be placed by
 * CSS alone. The picker's placement is not written here: it is the same problem
 * the composer's picker has, and useAnchoredPicker owns it for both.
 */
function useReactionPickerPlacement({
  messageId,
  isMine,
  bubbleRef,
  reactionMenuVisible,
  pickerOpen,
  onPickerOpenChange,
  onReactionMenuVisibleChange,
}: PlacementInput): Placement {
  const menuRef = useRef<HTMLDivElement>(null);
  const anchorRef = useRef<HTMLButtonElement>(null);

  const positionMenu = useCallback(() => {
    if (!reactionMenuVisible || !bubbleRef.current || !menuRef.current) return;
    const bubble = bubbleRef.current.getBoundingClientRect();
    if (bubble.bottom < 0 || bubble.top > window.innerHeight) return;
    const menu = menuRef.current.getBoundingClientRect();
    const midX = bubble.left + bubble.width / 2;
    // gapAbove = 0: cola a borda da toolbar exatamente no início da mensagem
    // (feedback de UX, issue #331).
    placeAgainstAnchor(menuRef.current, bubble, menu, isMine ? midX - menu.width : midX, 6, 0);
  }, [bubbleRef, isMine, reactionMenuVisible]);

  useLayoutEffect(positionMenu, [positionMenu]);

  useEffect(() => {
    if (!reactionMenuVisible) return;
    document.addEventListener("scroll", positionMenu, true);
    return () => document.removeEventListener("scroll", positionMenu, true);
  }, [positionMenu, reactionMenuVisible]);

  /**
   * Closes the picker, returning focus to the button that opened it when the
   * user closed it deliberately — Escape, or picking an emoji. A click outside
   * passes false: focus belongs wherever the click put it.
   *
   * The toolbar is kept visible first, because it is what the anchor lives in:
   * without that, closing the picker while the pointer is elsewhere would
   * unmount the button at the same instant focus was being handed back to it.
   */
  const closePicker = useCallback(
    (restoreFocus: boolean) => {
      if (restoreFocus) onReactionMenuVisibleChange(messageId, true);
      onPickerOpenChange(messageId, false);
      if (restoreFocus) anchorRef.current?.focus();
    },
    [messageId, onPickerOpenChange, onReactionMenuVisibleChange],
  );

  const pickerRef = useAnchoredPicker({
    open: pickerOpen,
    anchorRef,
    onDismiss: closePicker,
    containerRef: menuRef,
  });

  return { menuRef, anchorRef, pickerRef, closePicker };
}

function ReactionBadges({
  message,
  currentUserId,
  onToggleReaction,
}: Pick<MessageToolbarProps, "message" | "currentUserId" | "onToggleReaction">) {
  const { rendered, onExited } = useReactionPresence(message.reactions);
  if (rendered.length === 0) return null;
  return (
    <div className="chat-msg-area__reactions" aria-label="Reações da mensagem">
      {rendered.map(({ reaction, exiting }) => (
        <ReactionBadge
          key={reaction.emoji}
          messageId={message.id}
          reaction={reaction}
          currentUserId={currentUserId}
          onToggle={onToggleReaction}
          exiting={exiting}
          onExited={onExited}
        />
      ))}
    </div>
  );
}

function QuickReactionButtons({
  messageId,
  emojis,
  onToggleReaction,
}: {
  messageId: string;
  emojis: string[];
  onToggleReaction: (messageId: string, emoji: string) => void;
}) {
  return (
    <>
      {emojis.map((emoji) => (
        <button
          key={emoji}
          type="button"
          aria-label={`Reagir rapidamente com ${emoji}`}
          onClick={() => onToggleReaction(messageId, emoji)}
        >
          {emoji}
        </button>
      ))}
    </>
  );
}

export default function MessageToolbar(props: MessageToolbarProps) {
  const { message, currentUserId, onToggleReaction, reactionMenuVisible } = props;
  const { menuRef, anchorRef, pickerRef, closePicker } = useReactionPickerPlacement({
    ...props,
    messageId: message.id,
  });

  const selectReaction = (emoji: string) => {
    onToggleReaction(message.id, emoji);
    closePicker(true);
  };

  if (message.isRemoved) return null;
  return (
    <>
      <ReactionBadges
        message={message}
        currentUserId={currentUserId}
        onToggleReaction={onToggleReaction}
      />
      {reactionMenuVisible && (
        <div
          ref={menuRef}
          className="chat-msg-area__reaction-menu"
          role="toolbar"
          aria-label="Reagir à mensagem"
          onMouseEnter={() => props.onReactionMenuVisibleChange(message.id, true)}
          style={{ visibility: "hidden" }}
        >
          <QuickReactionButtons
            messageId={message.id}
            emojis={props.recentReactionEmojis}
            onToggleReaction={onToggleReaction}
          />
          <MessageActionButtons {...props} />
          <button
            ref={anchorRef}
            type="button"
            aria-label="Mais reações"
            aria-expanded={props.pickerOpen}
            aria-haspopup="dialog"
            onClick={() => props.onPickerOpenChange(message.id, !props.pickerOpen)}
          >
            <span className="material-symbols-outlined" aria-hidden="true">
              add_reaction
            </span>
          </button>
          {props.pickerOpen &&
            createPortal(
              <div
                ref={pickerRef}
                // The portal escapes the conversation's DOM, and with it the
                // scoped palette and radius scale that .chat-app defines. The
                // class comes along so the picker is NChat-purple like the
                // surface it belongs to, instead of falling back to the root
                // theme.
                className="chat-theme chat-emoji-surface"
                role="dialog"
                aria-label="Escolher reação"
                style={{ visibility: "hidden" }}
              >
                <Suspense
                  fallback={
                    <p className="chat-emoji-picker__status" role="status">
                      Carregando emojis…
                    </p>
                  }
                >
                  <EmojiPicker
                    usage={props.emojiUsage}
                    onToneChange={props.onEmojiToneChange}
                    onSelect={selectReaction}
                  />
                </Suspense>
              </div>,
              document.body,
            )}
        </div>
      )}
    </>
  );
}
