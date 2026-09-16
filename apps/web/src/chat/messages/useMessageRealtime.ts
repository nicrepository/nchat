import { useCallback } from "react";

import { normalizeLinkSafety } from "../chatTypes";
import {
  useChatWebSocket,
  type WSAttachmentStatusEvent,
  type WSClientErrorEvent,
  type WSAcknowledgementUpdatedEvent,
  type WSConversationEventMessage,
  type WSMembersAddedEvent,
  type WSMessageBlockedEvent,
  type WSMessageCreatedEvent,
  type WSMessageLinkSafetyChangedEvent,
  type WSMessageUpdatedEvent,
  type WSPinUpdatedEvent,
  type WSTypingUpdatedEvent,
} from "../useChatWebSocket";
import { blockedMessageReason } from "./composerReducer";
import { messageFromCreatedPayload } from "./realtimeMessagePayload";
import {
  createdReadKey,
  realtimeFallbackErrorMessage,
  updatedReadKey,
  type AuthoritativeReads,
} from "./useAuthoritativeReads";
import type { ConversationScope } from "./useConversationScope";
import { useForwardedTargetEvent } from "./useForwardedTargetEvent";
import type { ReactionEventHandlers } from "./useReactionEvents";
import type { RequestRegistry } from "./useRequestRegistry";
import type { SecurityReconciliation } from "./useSecurityReconciliation";
import type { Action } from "./types";

type MessageUpdate = NonNullable<WSMessageUpdatedEvent["message_update"]>;

/** Events this hook does not act on, forwarded to whoever asked for them. */
export interface RealtimeListeners {
  onPinUpdated?: (event: WSPinUpdatedEvent) => void;
  onMembersAdded?: (event: WSMembersAddedEvent) => void;
  onAttachmentStatus?: (event: WSAttachmentStatusEvent) => void;
  onTypingUpdated?: (event: WSTypingUpdatedEvent) => void;
}

export interface MessageRealtime {
  /** Sends a reaction toggle; false when the shared socket is not open. */
  toggleReaction: (messageId: string, emoji: string) => boolean;
  /** Declares this user's typing intent; false when the shared socket is not open. */
  sendTyping: (isTyping: boolean) => boolean;
}

interface Options {
  scope: ConversationScope;
  dispatch: (action: Action) => void;
  fallbacks: RequestRegistry;
  reads: AuthoritativeReads;
  reactions: ReactionEventHandlers;
  reconciliation: SecurityReconciliation;
  /**
   * Issue #824. Re-reads the acknowledgement summaries this view holds when the
   * subscription comes back ready, so a reconnect reconciles against the server
   * instead of trusting a cache that may have gone stale off-socket.
   */
  reconcileAcknowledgements?: () => void;
  /**
   * Issue #824. Re-reads one message's acknowledgement, because the server said
   * that message changed. Targeted rather than conversation-wide: the event
   * names the message, so exactly that message is asked about.
   */
  reconcileAcknowledgement?: (messageId: string) => void;
  /** Told when an event took a message out of the timeline. */
  notifyRemoved: () => void;
  listeners: RealtimeListeners;
}

/** True when an update announces the message is gone rather than edited. */
function updateWithdrawsMessage(update: MessageUpdate): boolean {
  return update.is_removed === true || update.status === "deleted";
}

/**
 * The conversation's WebSocket subscription, and everything it delivers.
 *
 * Two shapes of event arrive here. One carries what it announces — a full
 * message DTO, a link-safety verdict, a reaction list — and is applied without
 * a request. The other carries only a route, because the server could not or
 * would not put the content on the wire, and is resolved by reading the message
 * back. Both end in the same reducer transitions, so a rolling deploy that
 * downgrades one to the other changes only the number of requests.
 *
 * Every handler filters on the active target before doing anything, on top of
 * the WebSocket hook's own filter: an event about another conversation must
 * never reach this one's state.
 */
export function useMessageRealtime({
  scope,
  dispatch,
  fallbacks,
  reads,
  reactions,
  reconciliation,
  reconcileAcknowledgements,
  reconcileAcknowledgement,
  notifyRemoved,
  listeners,
}: Options): MessageRealtime {
  const { targetId, kind } = scope;
  const { readCreatedMessage, readMessageSnapshot } = reads;

  /**
   * A new message.
   *
   * Primary path: the event carries the full DTO, which is mapped and inserted
   * with no additional GET. Fallback path: the payload is absent (an old server
   * during a rolling deploy), so the message is read back rather than lost.
   */
  const handleMessageCreated = useCallback(
    (event: WSMessageCreatedEvent) => {
      const loadKey = scope.key;
      // Double-check target (the ws hook already filters, but guard here too).
      if (event.target_id !== targetId) return;
      if (!event.payload) {
        readCreatedMessage(event.message_id);
        return;
      }
      fallbacks.cancel(createdReadKey(event.message_id));
      const message = messageFromCreatedPayload(event.payload);
      if (!scope.isCurrent(loadKey)) return;
      dispatch({ type: "ws_received", message: scope.sanitize(message) });
    },
    [dispatch, fallbacks, readCreatedMessage, scope, targetId],
  );

  /**
   * RF-21: the author's message was refused.
   *
   * Without this the composer's "checking links…" bubble had no event that would
   * ever change it — the backend took the message to a terminal state and told
   * nobody — so the author was left believing a send was still in flight.
   *
   * The event is addressed to the author alone and carries no content, so there
   * is nothing to render from it: the pending bubble is removed and the send
   * error explains why. Removing rather than rewriting is deliberate — the
   * message was never published, so leaving a husk of it in the transcript would
   * suggest otherwise.
   */
  const handleMessageBlocked = useCallback(
    (event: WSMessageBlockedEvent) => {
      dispatch({
        type: "message_blocked",
        messageId: event.message_id,
        reason: blockedMessageReason(event.reason),
      });
    },
    [dispatch],
  );

  /**
   * RF-21: a published message's link-safety state changed (issue #135).
   *
   * This is the convergence path for a reconciliation that landed after the
   * message was delivered — the notice disappearing when a verdict finally
   * arrives, or the links being withdrawn when one turns out to be malicious.
   * Unlike message.blocked it never removes the message: it was published, and it
   * stays published.
   *
   * The event's own `link_safety.state` is preferred and the envelope's is not
   * consulted for the value, so a payload the server stripped simply carries the
   * conservative fallback normalizeLinkSafety produces for an unknown value.
   */
  const handleLinkSafetyChanged = useCallback(
    (event: WSMessageLinkSafetyChangedEvent) => {
      dispatch({
        type: "link_safety_changed",
        messageId: event.message_id,
        state: normalizeLinkSafety(event.link_safety?.state),
        updatedAt: event.link_safety.updated_at,
      });
    },
    [dispatch],
  );

  /**
   * An update that carries what it announces.
   *
   * The tombstone is recorded before anything else, so a read already in flight
   * cannot bring the message back. A create this client is still waiting on for
   * the same message is abandoned in favour of one authoritative read, which is
   * what keeps a delete that overtakes its own create terminal.
   */
  const applyDeliveredUpdate = useCallback(
    (event: WSMessageUpdatedEvent, update: MessageUpdate) => {
      const messageId = update.message_id;
      const withdrawn = updateWithdrawsMessage(update);
      if (withdrawn) {
        scope.rememberDeleted(messageId, update.deleted_at ?? update.updated_at ?? null);
      }
      fallbacks.cancel(updatedReadKey(messageId));
      const createdKey = createdReadKey(messageId);
      if (fallbacks.has(createdKey) && !scope.isRendered(messageId)) {
        fallbacks.cancel(createdKey);
        readMessageSnapshot(messageId, true);
      }
      dispatch({ type: "message_updated", event });
      if (withdrawn) notifyRemoved();
    },
    [dispatch, fallbacks, notifyRemoved, readMessageSnapshot, scope],
  );

  const handleMessageUpdated = useCallback(
    (event: WSMessageUpdatedEvent) => {
      if (event.target_id !== targetId) return;
      const update = event.message_update;
      if (update) {
        applyDeliveredUpdate(event, update);
        return;
      }
      const messageId = event.message_id;
      if (!messageId) return;
      readMessageSnapshot(messageId, fallbacks.has(createdReadKey(messageId)));
    },
    [applyDeliveredUpdate, fallbacks, readMessageSnapshot, targetId],
  );

  const handleSubscriptionError = useCallback(
    (event: WSClientErrorEvent) => {
      dispatch({
        type: "ws_fetch_error",
        error:
          event.code === "room_access_denied"
            ? "Não foi possível acessar as atualizações em tempo real desta conversa."
            : realtimeFallbackErrorMessage,
      });
    },
    [dispatch],
  );

  // Issue #824. One event, one message, one re-read. The handler applies nothing
  // itself: the frame carries no acknowledgement state, so the only thing to do
  // with it is ask the authorised endpoint — which is also what keeps a
  // redelivered event idempotent, since two reads of the same message return the
  // same answer.
  const handleAcknowledgementUpdated = useCallback(
    (event: WSAcknowledgementUpdatedEvent) => {
      if (!event.message_id) return;
      reconcileAcknowledgement?.(event.message_id);
    },
    [reconcileAcknowledgement],
  );

  const { reconcilePendingLinkScans, refreshAuthoritativeMessageSecurity } = reconciliation;
  const handleSubscribed = useCallback(() => {
    dispatch({ type: "ws_subscription_ready" });
    reconcilePendingLinkScans();
    refreshAuthoritativeMessageSecurity();
    // Issue #824. A subscription that comes back ready is this client's only
    // signal that it may have missed something, so acknowledgement re-reads the
    // server's own summaries here rather than polling for them. It is the
    // mechanism the two calls above already use; nothing new was invented for
    // acknowledgement, and no event was added to the protocol.
    reconcileAcknowledgements?.();
  }, [
    dispatch,
    reconcileAcknowledgements,
    reconcilePendingLinkScans,
    refreshAuthoritativeMessageSecurity,
  ]);

  // The timeline reconciles itself from the event (RF-32) while the caller
  // still gets to refresh whatever else lists this destination's files. No
  // polling is introduced: this is the mechanism that already existed.
  const reconcileAttachment = useCallback(
    (event: WSAttachmentStatusEvent) => {
      if (!event.attachment?.attachment_id) return;
      dispatch({
        type: "attachment_status",
        attachmentId: event.attachment.attachment_id,
        status: event.attachment.status,
      });
    },
    [dispatch],
  );

  const target = { kind, targetId };
  const handlePinUpdated = useForwardedTargetEvent(target, listeners.onPinUpdated);
  const handleMembersAdded = useForwardedTargetEvent(target, listeners.onMembersAdded);
  const handleTypingUpdated = useForwardedTargetEvent(target, listeners.onTypingUpdated);
  const handleAttachmentStatus = useForwardedTargetEvent(
    target,
    listeners.onAttachmentStatus,
    reconcileAttachment,
  );

  /**
   * Issue #685: a system event was persisted for this conversation — a
   * member added or removed, a rename, an archive, a call starting or
   * ending. The socket carries only the message id ("the message id
   * travels, the message does not"), so this is handled the same way an
   * update with no payload already is: one authorized read, inserted if the
   * timeline does not have it yet. Dedup by id in the reducer is what makes
   * this safe against redelivery or a reconnect replaying the same event.
   *
   * Handled internally rather than forwarded to a caller-supplied listener —
   * unlike pin/members/typing/attachment above, nothing outside the open
   * timeline needs to react to this one.
   */
  const reconcileConversationEvent = useCallback(
    (event: WSConversationEventMessage) => {
      if (!event.message_id) return;
      readMessageSnapshot(event.message_id, true);
    },
    [readMessageSnapshot],
  );
  const handleConversationEvent = useForwardedTargetEvent(
    target,
    undefined,
    reconcileConversationEvent,
  );

  const { toggleReaction, sendTyping } = useChatWebSocket({
    kind,
    targetId,
    onMessageCreated: handleMessageCreated,
    onMessageBlocked: handleMessageBlocked,
    onMessageLinkSafetyChanged: handleLinkSafetyChanged,
    onMessageUpdated: handleMessageUpdated,
    onReactionUpdated: reactions.handleReactionUpdated,
    onTypingUpdated: handleTypingUpdated,
    onPinUpdated: handlePinUpdated,
    onMembersAdded: handleMembersAdded,
    onAttachmentStatus: handleAttachmentStatus,
    onConversationEvent: handleConversationEvent,
    onAcknowledgementUpdated: handleAcknowledgementUpdated,
    onReactionError: reactions.handleReactionError,
    onSubscriptionError: handleSubscriptionError,
    onSubscribed: handleSubscribed,
  });

  return { toggleReaction, sendTyping };
}
