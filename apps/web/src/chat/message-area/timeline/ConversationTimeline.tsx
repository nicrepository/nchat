/**
 * Which of the conversation's four states is on screen: still loading, failed,
 * empty, or a list of messages (moved out of ChatMessageArea, issue #834).
 *
 * The channel-only props are resolved here rather than by the caller, because
 * "does a DM have a channel id" is a question about this timeline and not about
 * the page around it.
 */

import { useMemo } from "react";

import type { CallParticipantProfile } from "../../chatApi";
import {
  fetchChannelCallParticipantProfiles,
  fetchGroupCallParticipantProfiles,
} from "../../chatApi";
import type { MentionTarget, MessageAcknowledgement } from "../../chatTypes";
import type { ViewportAnchor } from "../../chatViewportPersistence";
import { systemScopeFor } from "../../conversationSystemMessage";
import type { EmojiUsage } from "../../emoji/emojiUsage";
import { presenceTargetKey } from "../../presence";
import type { MessagesState } from "../../useMessages";
import type { ConversationDetailsKind } from "../../useConversationDetailsPanel";
import { EmptyState, ErrorState, LoadingSkeleton } from "../ConversationStates";
import MessageList from "./MessageList";
import type { ConversationMessageActions } from "./MessageTimelineItem";

export interface ConversationTimelineProps {
  kind: "channel" | "dm";
  targetId: string;
  name: string;
  detailsKind: ConversationDetailsKind | null;
  mentionTarget?: MentionTarget;
  state: MessagesState;
  currentUserId: string;
  /** Every callback a message offers; see MessageTimelineItem. */
  actions: ConversationMessageActions;
  onLoadMore: () => void;
  onRetry: () => void;
  editDisabledIds: Set<string>;
  pinnedIds: Set<string>;
  openingAuthorDMIds?: Set<string>;
  /** Issue #824: the server's acknowledgement summary per message, if any. */
  acknowledgements?: Record<string, MessageAcknowledgement>;
  /** Issue #824: the message whose confirmation is in flight, if any. */
  acknowledgingId?: string | null;
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
 * Resolves recipient identities for the acknowledgement details popover
 * (issue #846), scoped to whichever aggregate this conversation actually is.
 *
 * Undefined for a 1:1 DM: that view never offers the popover (see
 * MessageAcknowledgement's direct wording for a single recipient), so there is
 * nothing here for it to call.
 */
function recipientIdentityResolver(
  kind: "channel" | "dm",
  targetId: string,
  detailsKind: ConversationDetailsKind | null,
): ((userIds: string[], signal?: AbortSignal) => Promise<CallParticipantProfile[]>) | undefined {
  if (!targetId || detailsKind === "direct" || detailsKind === null) return undefined;
  return kind === "channel"
    ? (userIds, signal) => fetchChannelCallParticipantProfiles(targetId, userIds, signal)
    : (userIds, signal) => fetchGroupCallParticipantProfiles(targetId, userIds, signal);
}

export default function ConversationTimeline(props: ConversationTimelineProps) {
  const { kind, targetId, name, state, actions } = props;
  const resolveRecipientIdentities = useMemo(
    () => recipientIdentityResolver(kind, targetId, props.detailsKind),
    [kind, targetId, props.detailsKind],
  );
  if (state.status === "loading") return <LoadingSkeleton />;
  if (state.status === "error") return <ErrorState onRetry={props.onRetry} />;
  if (state.status !== "ready") return null;
  if (state.messages.length === 0) return <EmptyState kind={kind} name={name} />;
  return (
    <MessageList
      messages={state.messages}
      currentUserId={props.currentUserId}
      // "canal" / "grupo" / "conversa" for this timeline's system messages
      // (issue #527).
      systemScope={systemScopeFor(props.detailsKind, kind)}
      hasMore={state.nextCursor !== ""}
      loadingMore={state.loadingMore}
      lastMutation={state.lastMutation}
      onLoadMore={props.onLoadMore}
      actions={{
        ...actions,
        // Forwarding is a channel affordance: a DM has no channel to forward from.
        onForwardMessage: kind === "channel" && targetId ? actions.onForwardMessage : undefined,
      }}
      editDisabledIds={props.editDisabledIds}
      mentionTarget={props.mentionTarget}
      presenceTarget={targetId ? presenceTargetKey(kind, targetId) : undefined}
      pinnedIds={props.pinnedIds}
      openingAuthorDMIds={props.openingAuthorDMIds}
      acknowledgements={props.acknowledgements}
      acknowledgingId={props.acknowledgingId}
      resolveRecipientIdentities={resolveRecipientIdentities}
      recentReactionEmojis={props.recentReactionEmojis}
      emojiUsage={props.emojiUsage}
      onEmojiToneChange={props.onEmojiToneChange}
      focusMessageId={props.focusMessageId}
      conversationKey={props.conversationKey}
      unreadCountAtOpen={props.unreadCountAtOpen}
      initialAnchor={props.initialAnchor}
      onCaptureAnchor={props.onCaptureAnchor}
      onReachedBottom={props.onReachedBottom}
    />
  );
}
