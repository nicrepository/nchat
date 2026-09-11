/**
 * The timeline itself (issue #834): the rows, and the box they scroll in.
 *
 * What it is *not* is where the scrolling happens. Every #492/#675/#788
 * invariant — the state machine, the sentinels, the observers, the prepend
 * restoration, the anchor — lives behind useConversationViewport, and reaches
 * this component as refs to attach and values to draw. Reading this file should
 * tell you what a conversation looks like, not how it stays where the reader
 * left it.
 */

import { useMemo } from "react";

import AttachmentViewerHost from "../../AttachmentViewerHost";
import type { MentionTarget, Message } from "../../chatTypes";
import type { ViewportAnchor } from "../../chatViewportPersistence";
import type { SystemMessageScope } from "../../conversationSystemMessage";
import type { EmojiUsage } from "../../emoji/emojiUsage";
import { TimelineScrollRootContext } from "../../lazyAttachment";
import type { TimelineRow } from "../../timelineVirtualization";
import type { LastMutation } from "../../useMessages";
import { useConversationViewport } from "../viewport/useConversationViewport";
import MessageTimelineItem, {
  type ConversationMessageActions,
  type TimelineRowContext,
} from "./MessageTimelineItem";
import ScrollToBottomButton from "./ScrollToBottomButton";
import { useReactionMenu } from "./useReactionMenu";

export interface MessageListProps {
  messages: Message[];
  currentUserId: string;
  hasMore: boolean;
  loadingMore: boolean;
  lastMutation: LastMutation;
  /**
   * What a message offers. Without onQuoteJump: travelling to a quoted message
   * is the viewport's business, and this component is the only place that knows
   * both halves, so it joins them rather than the page above passing one down.
   */
  actions: ConversationMessageActions;
  onLoadMore: () => void;
  editDisabledIds: Set<string>;
  mentionTarget?: MentionTarget;
  systemScope: SystemMessageScope;
  presenceTarget?: string;
  pinnedIds?: Set<string>;
  openingAuthorDMIds?: Set<string>;
  recentReactionEmojis: string[];
  emojiUsage: EmojiUsage;
  onEmojiToneChange: (tone: number) => void;
  focusMessageId?: string;
  /** #492: `${kind}:${targetId}` — keys the per-conversation viewport anchor. */
  conversationKey: string;
  /** #492: the sidebar's unread_count for this target as of opening it. */
  unreadCountAtOpen: number;
  /** #492: the anchor this conversation was left at, if any. */
  initialAnchor: ViewportAnchor | null;
  /** #492: called on unmount (leaving the conversation) with the current anchor. */
  onCaptureAnchor: (key: string, anchor: ViewportAnchor) => void;
  /** #492: called once the bottom sentinel confirms the real tail was reached. */
  onReachedBottom: () => void;
}

export default function MessageList(props: MessageListProps) {
  const {
    messages,
    currentUserId,
    hasMore,
    loadingMore,
    lastMutation,
    actions,
    onLoadMore,
    conversationKey,
    unreadCountAtOpen,
    initialAnchor,
    focusMessageId,
    onCaptureAnchor,
    onReachedBottom,
  } = props;

  const {
    attachList,
    contentRef,
    topSentinelRef,
    bottomRef,
    unreadDividerRef,
    scrollRoot,
    setMessageRef,
    rows,
    virtualized,
    virtualizer,
    highlightedMessageId,
    jumpToMessage,
    pendingCount,
    scrollButtonVisible,
    scrollToBottomNow,
    focusList,
  } = useConversationViewport({
    messages,
    currentUserId,
    hasMore,
    lastMutation,
    conversationKey,
    unreadCountAtOpen,
    initialAnchor,
    focusMessageId,
    onLoadMore,
    onCaptureAnchor,
    onReachedBottom,
  });
  const reactionMenu = useReactionMenu();
  const messagesById = useMemo(
    () => new Map(messages.map((message) => [message.id, message])),
    [messages],
  );
  const context: TimelineRowContext = {
    currentUserId,
    systemScope: props.systemScope,
    presenceTarget: props.presenceTarget,
    mentionTarget: props.mentionTarget,
    recentReactionEmojis: props.recentReactionEmojis,
    emojiUsage: props.emojiUsage,
    onEmojiToneChange: props.onEmojiToneChange,
    editDisabledIds: props.editDisabledIds,
    pinnedIds: props.pinnedIds,
    openingAuthorDMIds: props.openingAuthorDMIds,
    messagesById,
  };

  const renderRow = (row: TimelineRow) => (
    <MessageTimelineItem
      key={row.key}
      row={row}
      context={context}
      actions={{ ...actions, onQuoteJump: jumpToMessage }}
      reactionMenu={reactionMenu}
      highlightedMessageId={highlightedMessageId}
      unreadDividerRef={unreadDividerRef}
      setMessageRef={setMessageRef}
    />
  );

  const timeline = (
    <div className="chat-msg-area__list-wrap">
      <div
        ref={attachList}
        className="chat-msg-area__list"
        role="log"
        aria-live="polite"
        aria-label="Mensagens"
        // #675: where focus goes when the row holding it is unmounted by the
        // virtualizer. Never in the tab order — only ever focused
        // programmatically, by the recovery effect and by a viewer closing
        // after its trigger has gone.
        tabIndex={-1}
      >
        {/* #788: the ResizeObserver target — see the tail-lock effect. */}
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
          {virtualized ? (
            <div
              className="chat-msg-area__virtual-canvas"
              data-testid="chat-virtual-canvas"
              style={{ height: virtualizer.getTotalSize() }}
            >
              {virtualizer.getVirtualItems().map((virtualRow) => (
                <div
                  key={virtualRow.key}
                  data-index={virtualRow.index}
                  // Reports its real height back, which is what makes variable
                  // height work: an attachment finishing its layout remeasures
                  // its own row, and the tail lock absorbs the shift.
                  ref={virtualizer.measureElement}
                  className="chat-msg-area__virtual-row"
                  style={{ transform: `translateY(${virtualRow.start}px)` }}
                >
                  {renderRow(rows[virtualRow.index])}
                </div>
              ))}
            </div>
          ) : (
            rows.map(renderRow)
          )}
          <div ref={bottomRef} data-testid="chat-bottom-sentinel" />
        </div>
      </div>
      <ScrollToBottomButton
        visible={scrollButtonVisible}
        pendingCount={pendingCount}
        onClick={scrollToBottomNow}
      />
    </div>
  );

  return (
    // #675: the viewers live above the list, so a lightbox stays open when the
    // message that opened it is unmounted by the virtualizer, and focus has
    // somewhere predictable to land when its trigger is gone with it.
    //
    // The scroll root goes down the same way: the timeline scrolls in its own
    // box, so "800px before the viewport" only means anything measured against
    // that box rather than against the window.
    <TimelineScrollRootContext.Provider value={scrollRoot}>
      <AttachmentViewerHost onFocusFallback={focusList}>{timeline}</AttachmentViewerHost>
    </TimelineScrollRootContext.Provider>
  );
}
