/**
 * One row of the timeline (issue #834): a day divider, the unread separator, a
 * conversation event, or a message.
 *
 * The boundary the virtualized and plain paths share. The virtualized path
 * wraps this in a positioned box and the plain path does not — nothing else
 * about a row changes, which is the point: a message must not be able to look
 * or behave differently depending on how many of them are loaded.
 *
 */

import type { RefObject } from "react";

import ConversationSystemMessage from "../../ConversationSystemMessage.tsx";
import MessageBubble, { type MessageBubbleProps } from "../../MessageBubble";
import type { MentionTarget, Message } from "../../chatTypes";
import type { SystemMessageScope } from "../../conversationSystemMessage";
import type { EmojiUsage } from "../../emoji/emojiUsage";
import { senderLabel } from "../../messageDisplay";
import type { TimelineRow } from "../../timelineVirtualization";
import type { ReactionMenuState } from "./useReactionMenu";

/** Everything a row can be asked to do, by whoever reads it. */
export interface TimelineMessageActions {
  onToggleReaction: (messageId: string, emoji: string) => void;
  onReplyMessage: (message: Message) => void;
  onReferenceMessage: (message: Message) => void;
  onForwardMessage?: (message: Message) => void;
  onReferenceJump: (reference: NonNullable<Message["reference"]>) => void;
  onQuoteJump: (messageId: string) => void;
  onOpenAuthorDM?: MessageBubbleProps["onOpenAuthorDM"];
  onToggleFavorite: (messageId: string, isFavorited: boolean) => void;
  /** RF-21 "Verificar novamente" (issue #135); see MessageBubbleProps. */
  onReconcileLinkSafety?: MessageBubbleProps["onReconcileLinkSafety"];
  onEditMessage: MessageBubbleProps["onEditMessage"];
  onEditForbidden: MessageBubbleProps["onEditForbidden"];
  onDeleteMessage: MessageBubbleProps["onDeleteMessage"];
  /** RF-05: pin/unpin action for readable channels and DMs. */
  onTogglePin?: (messageId: string, pin: boolean) => void;
}

/** The same actions, minus the one the viewport supplies (see MessageList). */
export type ConversationMessageActions = Omit<TimelineMessageActions, "onQuoteJump">;

/** What is true of the whole conversation, rather than of any one message. */
export interface TimelineRowContext {
  currentUserId: string;
  /**
   * Whether a system message in this timeline says "canal", "grupo" or
   * "conversa" (issue #527). The kind comes from the conversation record, never
   * from the route or the name.
   */
  systemScope: SystemMessageScope;
  /** The conversation on screen, so a sender's presence is resolved in it. */
  presenceTarget?: string;
  mentionTarget?: MentionTarget;
  recentReactionEmojis: string[];
  /** Local emoji history and skin tone, owned by this conversation (issue #496). */
  emojiUsage: EmojiUsage;
  onEmojiToneChange: (tone: number) => void;
  editDisabledIds: Set<string>;
  /** RF-05: set of currently-pinned message IDs in this target. */
  pinnedIds?: Set<string>;
  openingAuthorDMIds?: Set<string>;
  /** Every loaded message, so a quote can name its author and offer the jump. */
  messagesById: Map<string, Message>;
}

interface Props {
  row: TimelineRow;
  context: TimelineRowContext;
  actions: TimelineMessageActions;
  reactionMenu: ReactionMenuState;
  highlightedMessageId: string | null;
  /**
   * #492: the "Novas mensagens" separator sits just above the first unread
   * message in the DOM — scrolling the message itself to the viewport's top
   * edge would push the separator off-screen above it, so AT_FIRST_UNREAD
   * positioning targets this instead.
   */
  unreadDividerRef: RefObject<HTMLDivElement | null>;
  setMessageRef: (messageId: string, el: HTMLDivElement | null) => void;
}

export default function MessageTimelineItem({
  row,
  context,
  actions,
  reactionMenu,
  highlightedMessageId,
  unreadDividerRef,
  setMessageRef,
}: Props) {
  if (row.type === "divider") {
    return (
      <div className="chat-msg-area__day-divider" aria-label={row.label}>
        {row.label}
      </div>
    );
  }
  if (row.type === "unread-divider") {
    return (
      <div
        ref={unreadDividerRef}
        className="chat-msg-area__new-messages-divider"
        role="separator"
        aria-label="Novas mensagens"
      >
        Novas mensagens
      </div>
    );
  }
  return (
    <TimelineMessageRow
      row={row}
      context={context}
      actions={actions}
      reactionMenu={reactionMenu}
      highlightedMessageId={highlightedMessageId}
      setMessageRef={setMessageRef}
    />
  );
}

/**
 * A row that carries a message — something a person said, or something that
 * happened to the conversation.
 *
 * Where a row's own facts are derived: everything above holds conversation-wide
 * sets (which messages are pinned, which are edit-disabled, whose DM is
 * opening), and asking those about *this* message is this component's job, so a
 * bubble receives booleans and the timeline never enumerates them.
 */
function TimelineMessageRow({
  row,
  context,
  actions,
  reactionMenu,
  highlightedMessageId,
  setMessageRef,
}: Omit<Props, "unreadDividerRef"> & { row: Extract<TimelineRow, { type: "msg" }> }) {
  const message = row.message;
  if (message.kind === "system") {
    // A conversation event is not something a person said, so it never becomes
    // a MessageBubble: no bubble, no avatar, and none of the message actions —
    // editing "Fulano saiu do grupo" is not a thing (issue #527).
    return (
      <ConversationSystemMessage
        message={message}
        scope={context.systemScope}
        viewerId={context.currentUserId}
      />
    );
  }
  const quoted = message.quoted;
  return (
    <MessageBubble
      message={message}
      isMine={!!context.currentUserId && message.senderId === context.currentUserId}
      isGrouped={row.isGrouped}
      onToggleReaction={actions.onToggleReaction}
      onReplyMessage={actions.onReplyMessage}
      onReferenceMessage={actions.onReferenceMessage}
      onForwardMessage={actions.onForwardMessage}
      onToggleFavorite={actions.onToggleFavorite}
      onReconcileLinkSafety={actions.onReconcileLinkSafety}
      onEditMessage={actions.onEditMessage}
      onEditForbidden={actions.onEditForbidden}
      onDeleteMessage={actions.onDeleteMessage}
      editDisabled={context.editDisabledIds.has(message.id)}
      mentionTarget={context.mentionTarget}
      presenceTarget={context.presenceTarget}
      onTogglePin={actions.onTogglePin}
      isPinned={context.pinnedIds?.has(message.id) ?? false}
      recentReactionEmojis={context.recentReactionEmojis}
      emojiUsage={context.emojiUsage}
      onEmojiToneChange={context.onEmojiToneChange}
      currentUserId={context.currentUserId}
      reactionMenuVisible={reactionMenu.hoveredMessageId === message.id}
      onReactionMenuVisibleChange={reactionMenu.onReactionMenuVisibleChange}
      pickerOpen={reactionMenu.openPickerMessageId === message.id}
      onPickerOpenChange={reactionMenu.onPickerOpenChange}
      quoteAuthorLabel={quoted ? quoteAuthorLabel(quoted, context.messagesById) : undefined}
      canJumpToQuote={quoted ? context.messagesById.has(quoted.id) : false}
      onQuoteJump={actions.onQuoteJump}
      onReferenceJump={actions.onReferenceJump}
      onOpenAuthorDM={actions.onOpenAuthorDM}
      openingAuthorDM={context.openingAuthorDMIds?.has(message.senderId) ?? false}
      isHighlighted={highlightedMessageId === message.id}
      setMessageRef={setMessageRef}
    />
  );
}

function quoteAuthorLabel(
  quote: NonNullable<Message["quoted"]>,
  messagesById: Map<string, Message>,
) {
  const parent = messagesById.get(quote.id);
  return parent ? senderLabel(parent) : "Usuário desconhecido";
}
