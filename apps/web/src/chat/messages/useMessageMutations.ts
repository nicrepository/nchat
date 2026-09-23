import { useCallback, useRef } from "react";

import {
  deleteMessage as deleteMessageRequest,
  editMessage as editMessageRequest,
  favoriteMessage,
  unfavoriteMessage,
  type PostMessageOptions,
} from "../chatApi";
import { markPresenceActivity } from "../chatSocket";
import type { Message } from "../chatTypes";
import {
  normalizePriorityIntent,
  standardPriorityIntent,
  type MessagePriorityIntent,
} from "../messagePriority";
import { randomId } from "../../lib/randomId";
import { sendErrorMessage } from "./composerReducer";
import type { MessagesGateway } from "./messagesGateway";
import type { ConversationScope } from "./useConversationScope";
import type { Action, SendResult } from "./types";

const stale: SendResult = { status: "stale" };

/**
 * An attachment is content: a message carrying one is sendable with an empty
 * body, and the server applies the same rule.
 */
function hasContent(body: string, attachmentIds: string[] | undefined): boolean {
  return Boolean(body.trim()) || Boolean(attachmentIds?.length);
}

/** The wire version the edit endpoint expects for a rendered body format. */
function bodyFormatVersion(bodyFormat: Message["bodyFormat"]): number {
  if (bodyFormat === "v3") return 3;
  if (bodyFormat === "v2") return 2;
  return 1;
}

/**
 * The four things a reader does to a message: send, edit, delete, favourite.
 *
 * All four share one rule, and it is the reason they live together: a result
 * that arrives after the reader has moved to another conversation is discarded
 * rather than applied. Sending reports that as `stale` so the composer keeps the
 * draft; editing and deleting simply leave the previous conversation alone.
 */
export interface MessageMutations {
  sendMessage: (
    body: string,
    referencedMessageId?: string,
    attachmentIds?: string[],
    /** Issue #822: the priority, confirmation request and reminder policy. */
    priority?: MessagePriorityIntent,
  ) => Promise<SendResult>;
  editMessageLocal: (
    messageId: string,
    body: string,
    bodyFormat: Message["bodyFormat"],
  ) => Promise<Message>;
  deleteMessageLocal: (messageId: string) => Promise<void>;
  toggleFavorite: (messageId: string, isFavorited: boolean) => void;
}

interface Options {
  scope: ConversationScope;
  gateway: MessagesGateway;
  dispatch: (action: Action) => void;
  bodyFormat: "v2" | "v3";
  /** Told when a delete this reader performed took a message out of the timeline. */
  notifyRemoved: () => void;
  /** Reconciles an event created atomically with a successful message send. */
  reconcileCreatedConversationEvent: (messageId: string) => void;
}

export function useMessageMutations({
  scope,
  gateway,
  dispatch,
  bodyFormat,
  notifyRemoved,
  reconcileCreatedConversationEvent,
}: Options): MessageMutations {
  /**
   * The idempotency key of the send currently being retried.
   *
   * The same draft resent after a failure must reach the server under the same
   * key, or a retry that only looked like it failed would post twice. A draft
   * that changed in any way is a different message and gets a new key.
   */
  const pendingSendIdentity = useRef<{ signature: string; key: string } | null>(null);
  const idempotencyKeyFor = useCallback((signature: string) => {
    if (pendingSendIdentity.current?.signature !== signature) {
      pendingSendIdentity.current = { signature, key: randomId() };
    }
    return pendingSendIdentity.current.key;
  }, []);

  const sendMessage = useCallback(
    async (
      body: string,
      referencedMessageId?: string,
      attachmentIds?: string[],
      priority?: MessagePriorityIntent,
    ): Promise<SendResult> => {
      if (!scope.targetId || !hasContent(body, attachmentIds)) return stale;

      // Sending a message is unambiguous proof the person is here, and it goes
      // over HTTP — so nothing about it would otherwise reach the presence
      // tracker, which only sees WebSocket frames (issue #444).
      markPresenceActivity();

      // The send boundary, and the one place the stated intent is made
      // coherent: normalizePriorityIntent is the single rule that says the
      // urgent-only options cannot survive a priority that is not urgent, and
      // it runs here rather than in the request builder so that the *same*
      // value feeds both the retry signature and the payload. Split them and a
      // send could be fingerprinted as one message and serialised as another —
      // or, worse, post `persistent_notifications` under `important` and be
      // refused by a server that is right to refuse it.
      //
      // Also why a caller that says nothing and a caller that says "standard"
      // are one message here rather than two with different idempotency keys.
      const intent = normalizePriorityIntent(priority ?? standardPriorityIntent);
      const sendKey = scope.key;
      const parentMessageId = scope.replyTo()?.id;
      dispatch({ type: "sending" });

      try {
        const signature = JSON.stringify({
          target: sendKey,
          bodyFormat,
          body,
          parentMessageId,
          referencedMessageId,
          attachmentIds: attachmentIds ?? [],
          // Part of the draft's identity (issues #824, #822): the same text sent
          // once plainly and once as urgent asking for confirmation are two
          // different messages, and must not share a retry key. The server draws
          // the same line in its own create fingerprint.
          priority: intent,
        });
        const options: PostMessageOptions = {
          parentMessageId,
          referencedMessageId,
          attachmentIds,
          idempotencyKey: idempotencyKeyFor(signature),
          // All three passed unconditionally rather than spread behind a test:
          // the request builder already omits each one at its default, so a
          // branch here would buy nothing and cost this function a decision
          // point.
          priority: intent.priority,
          acknowledgementRequired: intent.acknowledgementRequired,
          persistentNotifications: intent.persistentNotifications,
          ...(scope.kind === "dm" ? { bodyFormat } : {}),
        };

        const message = await gateway.post(body, options);

        pendingSendIdentity.current = null;
        // The timeline is the conversation on screen's; the acknowledgement
        // is the origin conversation's (issue #929). A reader who switched
        // away mid-request still gets their draft reconciled — by the
        // caller, against the key it captured at submit — while this state,
        // which now belongs to another conversation, is left alone.
        if (scope.isCurrent(sendKey)) {
          dispatch({ type: "sent", message: scope.sanitize(message), parentMessageId });
          if (message.createdConversationEventId) {
            reconcileCreatedConversationEvent(message.createdConversationEventId);
          }
        }
        return { status: "sent" };
      } catch (error: unknown) {
        // Stale failure: silently discard — do not update state for a previous target.
        if (!scope.isCurrent(sendKey)) return stale;
        dispatch({ type: "send_error", error: sendErrorMessage(error) });
        // Re-throw for current-target failures so callers can preserve the draft.
        throw error;
      }
    },
    [bodyFormat, dispatch, gateway, idempotencyKeyFor, reconcileCreatedConversationEvent, scope],
  );

  const editMessageLocal = useCallback(
    async (messageId: string, body: string, format: Message["bodyFormat"]) => {
      const previous = scope.messages().find((message) => message.id === messageId);
      if (!previous) throw new Error("Mensagem não encontrada.");
      const editKey = scope.key;
      const editedAt = new Date().toISOString();
      dispatch({ type: "edit_optimistic", messageId, body, bodyFormat: format, editedAt });
      try {
        const updated = await editMessageRequest(messageId, body, bodyFormatVersion(format));
        if (scope.isCurrent(editKey)) dispatch({ type: "edit_confirmed", message: updated });
        return updated;
      } catch (error) {
        if (scope.isCurrent(editKey)) {
          dispatch({ type: "edit_revert", message: previous, optimisticEditedAt: editedAt });
        }
        throw error;
      }
    },
    [dispatch, scope],
  );

  const deleteMessageLocal = useCallback(
    async (messageId: string) => {
      const deleteKey = scope.key;
      try {
        const deleted = await deleteMessageRequest(messageId);
        if (!scope.isCurrent(deleteKey)) return;
        scope.rememberDeleted(deleted.id, deleted.deletedAt ?? deleted.updatedAt);
        dispatch({
          type: "message_snapshot",
          message: scope.sanitize(deleted),
          insertIfMissing: false,
        });
        notifyRemoved();
      } catch (error) {
        if (scope.isCurrent(deleteKey)) {
          dispatch({
            type: "delete_error",
            error: "Não foi possível excluir a mensagem. Tente novamente.",
          });
        }
        throw error;
      }
    },
    [dispatch, notifyRemoved, scope],
  );

  // RF-06: REST round-trip confirms before the flag flips — no optimistic
  // update; a failure reuses the transient reaction error banner.
  // ponytail: no in-flight dedupe; a double click just repeats an idempotent call.
  const toggleFavorite = useCallback(
    (messageId: string, isFavorited: boolean) => {
      const apply = isFavorited ? favoriteMessage : unfavoriteMessage;
      void apply(messageId)
        .then(() => {
          if (scope.isRendered(messageId)) {
            dispatch({ type: "favorite_set", messageId, isFavorited });
          }
        })
        .catch(() => {
          dispatch({
            type: "favorite_error",
            error: "Não foi possível atualizar o favorito. Tente novamente.",
          });
        });
    },
    [dispatch, scope],
  );

  return { sendMessage, editMessageLocal, deleteMessageLocal, toggleFavorite };
}
