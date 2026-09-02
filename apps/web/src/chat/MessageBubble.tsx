import { useCallback, useRef, useState } from "react";
import type { FocusEvent as ReactFocusEvent, MouseEvent as ReactMouseEvent } from "react";

import type { LinkSafetyRecheck, MentionTarget, Message } from "./chatTypes";
import type { EmojiUsage } from "./emoji/emojiUsage";
import MessageContent from "./MessageContent";
import MessageEditHistory from "./MessageEditHistory";
import MessageToolbar from "./MessageToolbar";
import { formatTime, senderLabel } from "./messageDisplay";
import { PersonAvatarImage } from "./PersonAvatarImage";
import { presenceLabel, usePresence, type PresenceState } from "./presence";
import PresenceDot from "./PresenceDot";
import { useMessageEditing } from "./useMessageEditing";

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
  mentionTarget?: MentionTarget;
  /** The conversation on screen; presence is resolved within it (RF-58). */
  presenceTarget?: string;
  /** RF-05: pin/unpin action for readable channels and DMs. */
  onTogglePin?: (messageId: string, pin: boolean) => void;
  /** RF-05: whether this message is currently pinned in the active target. */
  isPinned?: boolean;
  recentReactionEmojis: string[];
  /** Local emoji history and skin tone, owned by the conversation (issue #496). */
  emojiUsage: EmojiUsage;
  onEmojiToneChange: (tone: number) => void;
  /** The reader, so a reaction tooltip can say "Você" rather than their name. */
  currentUserId: string;
  onOpenAuthorDM?: (message: Message) => void;
  openingAuthorDM?: boolean;
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

function MessageMeta({
  message,
  isMine,
  isGrouped,
  senderPresence,
  onOpenHistory,
  onOpenAuthorDM,
  openingAuthorDM,
}: Pick<
  MessageBubbleProps,
  "message" | "isMine" | "isGrouped" | "onOpenAuthorDM" | "openingAuthorDM"
> & {
  senderPresence: PresenceState;
  onOpenHistory: () => void;
}) {
  if (isGrouped && !message.isEdited) return null;
  const showSender = !isGrouped && !isMine;
  const label = senderLabel(message);
  return (
    <div className="chat-msg-area__msg-meta">
      {showSender && onOpenAuthorDM && (
        <button
          type="button"
          className="chat-msg-area__msg-sender chat-msg-area__msg-sender-action"
          aria-label={`Abrir conversa com ${label}`}
          aria-busy={openingAuthorDM}
          disabled={openingAuthorDM}
          data-testid="chat-msg-sender"
          onClick={() => onOpenAuthorDM(message)}
        >
          {label}
          {openingAuthorDM && <span className="sr-only">Abrindo conversa</span>}
        </button>
      )}
      {showSender && !onOpenAuthorDM && (
        <span className="chat-msg-area__msg-sender" data-testid="chat-msg-sender">
          {label}
        </span>
      )}
      {/* The dot next to the avatar is decorative and lives inside an
          aria-hidden wrapper, so without this the sender's state would be
          conveyed by colour alone. It sits beside the name, in the same place
          and under the same condition, so a screen reader hears the person and
          their state once per group rather than once per message — and says
          nothing at all when the server has not answered yet, since "unknown"
          is the absence of a state and not a fourth one. */}
      {showSender && senderPresence !== "unknown" && (
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

/**
 * The sender's avatar column. Decorative, including the presence dot: the name
 * and the state are already in the meta row, where a screen reader reads them
 * once per group instead of once per message.
 */
function MessageAvatar({ message, presence }: { message: Message; presence: PresenceState }) {
  return (
    <div className="chat-msg-area__msg-avatar" aria-hidden="true">
      <PersonAvatarImage
        src={message.senderAvatarUrl}
        initials={senderInitials(message)}
        imgClassName="chat-msg-area__msg-avatar-img"
      />
      <PresenceDot state={presence} size="sm" />
    </div>
  );
}

function messageBodyClassName(message: Message): string {
  const removed = message.isRemoved ? " chat-msg-area__msg-bubble--removed" : "";
  const scanning =
    message.status === "pending_link_scan" ? " chat-msg-area__msg-bubble--pending-scan" : "";
  return `chat-msg-area__msg-bubble${removed}${scanning}`;
}

function messageShellClassName(flags: {
  isMine: boolean;
  isGrouped: boolean;
  isHighlighted: boolean;
}): string {
  const mine = flags.isMine ? " chat-msg-area__msg--mine" : "";
  const grouped = flags.isGrouped ? " chat-msg-area__msg--grouped" : "";
  const highlighted = flags.isHighlighted ? " chat-msg-area__msg--highlight" : "";
  return `chat-msg-area__msg${mine}${grouped}${highlighted}`;
}

/**
 * The pointer and focus handlers that reveal the hover toolbar.
 *
 * Both "leave" handlers check containment, because moving into the toolbar —
 * which is a descendant — must not read as leaving the message.
 */
function useToolbarReveal(
  messageId: string,
  onVisibleChange: (messageId: string, visible: boolean) => void,
) {
  const show = useCallback(() => onVisibleChange(messageId, true), [messageId, onVisibleChange]);
  const hideOnLeave = useCallback(
    (event: ReactMouseEvent<HTMLDivElement>) => {
      const nextTarget = event.relatedTarget;
      if (!(nextTarget instanceof Node) || !event.currentTarget.contains(nextTarget)) {
        onVisibleChange(messageId, false);
      }
    },
    [messageId, onVisibleChange],
  );
  const hideOnBlur = useCallback(
    (event: ReactFocusEvent<HTMLDivElement>) => {
      if (!event.currentTarget.contains(event.relatedTarget)) onVisibleChange(messageId, false);
    },
    [messageId, onVisibleChange],
  );
  return {
    onMouseEnter: show,
    onMouseLeave: hideOnLeave,
    onFocus: show,
    onBlur: hideOnBlur,
    onTouchStart: show,
  };
}

/**
 * Everything beside the avatar: who and when, what was said, what can be done to
 * it, and the edit history when it is open.
 *
 * This is where a message's own state lives — the editor, a delete in flight,
 * the history dialog — so the shell around it stays a layout decision and
 * nothing else.
 */
function MessageBubbleBody({
  props,
  senderPresence,
}: {
  props: MessageBubbleProps;
  senderPresence: PresenceState;
}) {
  const { message, isMine = false, isGrouped = false } = props;
  const { onReactionMenuVisibleChange } = props;
  const bubbleRef = useRef<HTMLDivElement>(null);
  const [historyOpen, setHistoryOpen] = useState(false);

  const hideToolbar = useCallback(
    () => onReactionMenuVisibleChange(message.id, false),
    [message.id, onReactionMenuVisibleChange],
  );
  const editing = useMessageEditing({
    message,
    isMine,
    editDisabled: props.editDisabled ?? false,
    onEditMessage: props.onEditMessage,
    onEditForbidden: props.onEditForbidden,
    onDeleteMessage: props.onDeleteMessage,
    onHideToolbar: hideToolbar,
  });

  return (
    <div className="chat-msg-area__msg-body">
      <MessageMeta
        message={message}
        isMine={isMine}
        isGrouped={isGrouped}
        senderPresence={senderPresence}
        onOpenHistory={() => setHistoryOpen(true)}
        onOpenAuthorDM={props.onOpenAuthorDM}
        openingAuthorDM={props.openingAuthorDM}
      />
      <div ref={bubbleRef} className={messageBodyClassName(message)}>
        <MessageContent
          message={message}
          mentionTarget={props.mentionTarget}
          editing={editing.editing}
          onSaveEdit={editing.saveEdit}
          onCancelEdit={editing.cancelEdit}
          onEditForbidden={editing.handleForbidden}
          quoteAuthorLabel={props.quoteAuthorLabel}
          canJumpToQuote={props.canJumpToQuote}
          onQuoteJump={props.onQuoteJump}
          onReferenceJump={props.onReferenceJump}
          onReconcileLinkSafety={props.onReconcileLinkSafety}
        />
      </div>
      <MessageToolbar
        message={message}
        isMine={isMine}
        bubbleRef={bubbleRef}
        currentUserId={props.currentUserId}
        recentReactionEmojis={props.recentReactionEmojis}
        emojiUsage={props.emojiUsage}
        onEmojiToneChange={props.onEmojiToneChange}
        onToggleReaction={props.onToggleReaction}
        onReplyMessage={props.onReplyMessage}
        onReferenceMessage={props.onReferenceMessage}
        onForwardMessage={props.onForwardMessage}
        onToggleFavorite={props.onToggleFavorite}
        onTogglePin={props.onTogglePin}
        isPinned={props.isPinned ?? false}
        reactionMenuVisible={props.reactionMenuVisible || props.pickerOpen}
        onReactionMenuVisibleChange={onReactionMenuVisibleChange}
        pickerOpen={props.pickerOpen}
        onPickerOpenChange={props.onPickerOpenChange}
        onStartEdit={editing.startEdit}
        onDelete={editing.requestDelete}
        deleting={editing.deleting}
      />
      {historyOpen && !message.isRemoved && (
        <MessageEditHistory messageId={message.id} onClose={() => setHistoryOpen(false)} />
      )}
    </div>
  );
}

export default function MessageBubble(props: MessageBubbleProps) {
  const { message, isMine = false, isGrouped = false, isHighlighted = false } = props;
  // Read for every message, including the user's own, so the hook order does
  // not depend on who wrote it. Only the other party's avatar is drawn.
  //
  // Scoped to the conversation being read: the sender wrote here, so this
  // conversation's roster is the one that would have named them.
  const senderPresence = usePresence(message.senderId, props.presenceTarget);
  const reveal = useToolbarReveal(message.id, props.onReactionMenuVisibleChange);

  return (
    <div
      ref={(el) => props.setMessageRef?.(message.id, el)}
      className={messageShellClassName({ isMine, isGrouped, isHighlighted })}
      data-testid="chat-msg-bubble"
      data-message-id={message.id}
      tabIndex={0}
      {...reveal}
    >
      {!isMine && <MessageAvatar message={message} presence={senderPresence} />}
      <MessageBubbleBody props={props} senderPresence={senderPresence} />
    </div>
  );
}
