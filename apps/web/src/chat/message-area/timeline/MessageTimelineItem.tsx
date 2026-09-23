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

import type { CallParticipantProfile } from "../../chatApi";
import { useDirectMessagePending, type DirectMessageAccess } from "../../directMessage";
import ConversationSystemMessage from "../../ConversationSystemMessage.tsx";
import MessageBubble, { type MessageBubbleProps } from "../../MessageBubble";
import type { MentionType } from "../../richTextMarkers";
import type { MentionTarget, Message, MessageAcknowledgement } from "../../chatTypes";
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
  /** Opens a DM when a `@user` mention in a message body is clicked (issue #795). */
  onMentionClick?: (mentionType: MentionType, id: string) => void;
  onToggleFavorite: (messageId: string, isFavorited: boolean) => void;
  /** RF-21 "Verificar novamente" (issue #135); see MessageBubbleProps. */
  onReconcileLinkSafety?: MessageBubbleProps["onReconcileLinkSafety"];
  onEditMessage: MessageBubbleProps["onEditMessage"];
  onEditForbidden: MessageBubbleProps["onEditForbidden"];
  onDeleteMessage: MessageBubbleProps["onDeleteMessage"];
  /** RF-05: pin/unpin action for readable channels and DMs. */
  onTogglePin?: (messageId: string, pin: boolean) => void;
  /** Issue #824: confirms receipt of one message. See MessageBubbleProps. */
  onAcknowledge?: (messageId: string) => void;
  /**
   * Issue #846: reads one message's full per-recipient detail, for the details
   * popover a sender opens from the acknowledgement summary. See
   * MessageBubbleProps.
   */
  onOpenAcknowledgementDetails?: (messageId: string) => void;
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
  /**
   * The shared open-DM operation and this conversation's claim on it
   * (issue #895).
   *
   * Deliberately not "which recipients are pending": every row held that set,
   * so one person becoming pending invalidated every row in the list. A row
   * knows exactly one recipient — its own author — and subscribes to that one.
   */
  directMessage?: DirectMessageAccess;
  /** Every loaded message, so a quote can name its author and offer the jump. */
  messagesById: Map<string, Message>;
  /**
   * Issue #824. The server's summary for each message that asked for
   * confirmation, keyed by message id. Sparse by design: a conversation with no
   * such message carries an empty map and every row reads `undefined`.
   */
  acknowledgements?: Record<string, MessageAcknowledgement>;
  /** The message whose confirmation is in flight, if any. */
  acknowledgingId?: string | null;
  /**
   * Resolves recipient identities (display name, avatar) for the details
   * popover (issue #846), scoped to the conversation on screen. Undefined for
   * a 1:1 DM, which never offers the popover (issue #846's own rule against
   * "0 de 1"/"1 de 1" language).
   */
  resolveRecipientIdentities?: (
    userIds: string[],
    signal?: AbortSignal,
  ) => Promise<CallParticipantProfile[]>;
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
  /*
    This row's own author, and nobody else's (issue #895). Called before the
    system-message branch so the hook order never depends on what kind of row
    this is; a system message has no sender, and an empty id is never pending.

    One subscription per visible row, which the virtualiser already bounds — and
    the point of it: a request starting for somebody else answers `false` again,
    React bails out, and the row does not re-render. The list used to hold the
    whole pending set, so any request anywhere invalidated all of it.
  */
  const pendingSource = context.directMessage?.coordinator;
  const openingAuthorDM = useDirectMessagePending(pendingSource, message.senderId);
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
      acknowledgement={context.acknowledgements?.[message.id]}
      acknowledging={context.acknowledgingId === message.id}
      onAcknowledge={actions.onAcknowledge}
      onOpenAcknowledgementDetails={actions.onOpenAcknowledgementDetails}
      resolveRecipientIdentities={context.resolveRecipientIdentities}
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
      openingAuthorDM={openingAuthorDM}
      mentionInteraction={
        actions.onMentionClick
          ? {
              currentUserId: context.currentUserId,
              onMentionClick: actions.onMentionClick,
              // The narrow read-only port, so a mention can draw a busy state
              // without being able to start or cancel anything.
              pendingSource,
            }
          : undefined
      }
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
