import { useCallback } from "react";

import type { Message } from "../chatTypes";
import type { MessagesGateway } from "./messagesGateway";
import type { ConversationScope } from "./useConversationScope";
import type { ReactionTimers } from "./useReactionTimers";
import { isAbortError, type RequestRegistry } from "./useRequestRegistry";
import type { Action } from "./types";

export const realtimeFallbackErrorMessage = "Não foi possível atualizar mensagens em tempo real.";

/** The registry key of the read backing a message.created this client could not use. */
export const createdReadKey = (messageId: string) => `created:${messageId}`;
/** The registry key of the read backing a message.updated this client could not use. */
export const updatedReadKey = (messageId: string) => `updated:${messageId}`;
const reactionReadKey = (messageId: string) => `reaction:${messageId}`;

/**
 * The server's own copy of a single message, read back when realtime could not
 * be trusted or could not be used.
 *
 * Every realtime path that cannot apply what it was given ends here: a
 * message.created with no payload, a reaction.updated the relay stripped, a
 * message.updated that only carried a route. All three want the same thing —
 * one authorized GET, cancelled if the reader leaves, discarded if the answer
 * arrives for a conversation that is no longer on screen — so they share it, and
 * differ only in what they do with the message.
 */
export interface AuthoritativeReads {
  /** Recovers a message.created whose payload was absent (pre-payload server). */
  readCreatedMessage(messageId: string): void;
  /** Recovers the reactions of a message whose event carried none. */
  readReactionSnapshot(messageId: string): void;
  /** Re-reads a message an update announced, inserting it when it was never delivered. */
  readMessageSnapshot(messageId: string, insertIfMissing: boolean): void;
}

interface Options {
  scope: ConversationScope;
  gateway: MessagesGateway;
  dispatch: (action: Action) => void;
  fallbacks: RequestRegistry;
  reactionTimers: ReactionTimers;
  /** Told when a re-read turns out to be a message that is gone. */
  notifyRemoved: () => void;
}

export function useAuthoritativeReads({
  scope,
  gateway,
  dispatch,
  fallbacks,
  reactionTimers,
  notifyRemoved,
}: Options): AuthoritativeReads {
  const read = useCallback(
    (key: string, messageId: string, onMessage: (message: Message) => void) => {
      const controller = fallbacks.start(key);
      const loadKey = scope.key;
      void gateway.fetchMessage(messageId, controller.signal).then(
        (message) => {
          fallbacks.finish(key, controller);
          if (controller.signal.aborted) return;
          if (!scope.isCurrent(loadKey)) return;
          onMessage(message);
        },
        (error: unknown) => {
          fallbacks.finish(key, controller);
          if (isAbortError(error)) return;
          if (!scope.isCurrent(loadKey)) return;
          dispatch({ type: "ws_fetch_error", error: realtimeFallbackErrorMessage });
        },
      );
    },
    [dispatch, fallbacks, gateway, scope],
  );

  const readCreatedMessage = useCallback(
    (messageId: string) => {
      read(createdReadKey(messageId), messageId, (message) => {
        dispatch({ type: "ws_received", message: scope.sanitize(message) });
      });
    },
    [dispatch, read, scope],
  );

  const readReactionSnapshot = useCallback(
    (messageId: string) => {
      read(reactionReadKey(messageId), messageId, (message) => {
        reactionTimers.clear(message.id);
        dispatch({
          type: "reaction_snapshot",
          messageId: message.id,
          reactions: message.reactions,
        });
      });
    },
    [dispatch, read, reactionTimers],
  );

  const readMessageSnapshot = useCallback(
    (messageId: string, insertIfMissing: boolean) => {
      read(updatedReadKey(messageId), messageId, (message) => {
        if (message.isRemoved || message.status === "deleted") {
          scope.rememberDeleted(message.id, message.deletedAt ?? message.updatedAt);
        }
        const snapshot = scope.sanitize(message);
        dispatch({ type: "message_snapshot", message: snapshot, insertIfMissing });
        if (snapshot.isRemoved) notifyRemoved();
      });
    },
    [dispatch, notifyRemoved, read, scope],
  );

  return { readCreatedMessage, readReactionSnapshot, readMessageSnapshot };
}
