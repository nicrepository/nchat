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
    /** Issue #824: ask this message's recipients to confirm receipt. */
    acknowledgementRequired?: boolean,
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
}

export function useMessageMutations({
  scope,
  gateway,
  dispatch,
  bodyFormat,
  notifyRemoved,
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
      acknowledgementRequired?: boolean,
    ): Promise<SendResult> => {
      if (!scope.targetId || !hasContent(body, attachmentIds)) return stale;

      // Sending a message is unambiguous proof the person is here, and it goes
      // over HTTP — so nothing about it would otherwise reach the presence
      // tracker, which only sees WebSocket frames (issue #444).
      markPresenceActivity();

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
          // Part of the draft's identity (issue #824): the same text sent once
          // plainly and once asking for confirmation are two different messages,
          // and must not share a retry key. The server draws the same line in
          // its own create fingerprint.
          acknowledgementRequired: acknowledgementRequired ?? false,
        });
        const options: PostMessageOptions = {
          parentMessageId,
          referencedMessageId,
          attachmentIds,
          idempotencyKey: idempotencyKeyFor(signature),
          // Passed unconditionally rather than spread behind a test: the request
          // builder already omits a falsy flag from the payload, so a branch here
          // would buy nothing and cost this function a decision point.
          acknowledgementRequired,
          ...(scope.kind === "dm" ? { bodyFormat } : {}),
        };

        const message = await gateway.post(body, options);

        if (!scope.isCurrent(sendKey)) return stale;
        pendingSendIdentity.current = null;
        dispatch({ type: "sent", message: scope.sanitize(message) });
        return { status: "sent" };
      } catch (error: unknown) {
        // Stale failure: silently discard — do not update state for a previous target.
        if (!scope.isCurrent(sendKey)) return stale;
        dispatch({ type: "send_error", error: sendErrorMessage(error) });
        // Re-throw for current-target failures so callers can preserve the draft.
        throw error;
      }
    },
    [bodyFormat, dispatch, gateway, idempotencyKeyFor, scope],
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
