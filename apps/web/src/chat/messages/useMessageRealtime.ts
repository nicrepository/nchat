import { useCallback } from "react";

import { normalizeLinkSafety } from "../chatTypes";
import {
  useChatWebSocket,
  type WSAttachmentStatusEvent,
  type WSClientErrorEvent,
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

  const { reconcilePendingLinkScans, refreshAuthoritativeMessageSecurity } = reconciliation;
  const handleSubscribed = useCallback(() => {
    dispatch({ type: "ws_subscription_ready" });
    reconcilePendingLinkScans();
    refreshAuthoritativeMessageSecurity();
  }, [dispatch, reconcilePendingLinkScans, refreshAuthoritativeMessageSecurity]);

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
    onReactionError: reactions.handleReactionError,
    onSubscriptionError: handleSubscriptionError,
    onSubscribed: handleSubscribed,
  });

  return { toggleReaction, sendTyping };
}
