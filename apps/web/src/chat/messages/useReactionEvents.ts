import { useCallback } from "react";

import type { WSClientErrorEvent, WSReactionUpdatedEvent } from "../useChatWebSocket";
import { confirmReactionIntent } from "./pendingReactions";
import type { AuthoritativeReads } from "./useAuthoritativeReads";
import type { ConversationScope } from "./useConversationScope";
import { useLatestRef } from "./useLatestRef";
import type { ReactionTimers } from "./useReactionTimers";
import type { Action, PendingReactions } from "./types";

const reactionErrorMessages: Record<string, string> = {
  rate_limited: "Muitas reações em sequência. Aguarde um minuto e tente novamente.",
  temporarily_unavailable: "Reações temporariamente indisponíveis.",
};

export interface ReactionEventHandlers {
  handleReactionUpdated: (event: WSReactionUpdatedEvent) => void;
  handleReactionError: (event: WSClientErrorEvent) => void;
}

interface Options {
  scope: ConversationScope;
  dispatch: (action: Action) => void;
  timers: ReactionTimers;
  currentUserId: string;
  /** The reducer's outstanding intents, mirrored for the decision made before dispatching. */
  pendingReactions: Map<string, PendingReactions>;
  readReactionSnapshot: AuthoritativeReads["readReactionSnapshot"];
  /** Told when this reader's own addition of an emoji is confirmed. */
  onOwnReactionConfirmed?: (emoji: string) => void;
}

/** What the server says about reactions, and what it refuses. */
export function useReactionEvents({
  scope,
  dispatch,
  timers,
  currentUserId,
  pendingReactions,
  readReactionSnapshot,
  onOwnReactionConfirmed,
}: Options): ReactionEventHandlers {
  const latestPending = useLatestRef(pendingReactions);

  const handleReactionUpdated = useCallback(
    (event: WSReactionUpdatedEvent) => {
      if (event.target_id !== scope.targetId || !scope.isRendered(event.message_id)) return;
      const { reaction } = event;
      if (!reaction) {
        readReactionSnapshot(event.message_id);
        return;
      }
      const actorIsMe = reaction.actor_user_id === currentUserId;
      // Asked once, of the same predicate the reducer uses, against a mirror of
      // the reducer's own intents. An event that settles nothing leaves the
      // rollback timer running and the emoji history untouched — it has only
      // moved this client's view of what the server holds.
      //
      // The dispatch below settles whatever this answer says was settled, so a
      // redelivered copy finds nothing outstanding: each WS frame is a separate
      // task, and React has committed the mirror by the time the next arrives.
      const confirmation = confirmReactionIntent(latestPending.current, reaction, actorIsMe);
      if (confirmation.confirmed) {
        timers.clear(reaction.message_id, reaction.emoji);
        // A removal is not a use; only reaching for an emoji is.
        if (confirmation.intent === "added") onOwnReactionConfirmed?.(reaction.emoji);
      }
      dispatch({ type: "reaction_updated", event, actorIsMe });
    },
    [
      currentUserId,
      dispatch,
      latestPending,
      onOwnReactionConfirmed,
      readReactionSnapshot,
      scope,
      timers,
    ],
  );

  const handleReactionError = useCallback(
    (event: WSClientErrorEvent) => {
      // A server-level refusal is not scoped to one toggle, so every wait ends.
      timers.clearAll();
      dispatch({
        type: "reaction_error",
        error: reactionErrorMessages[event.code] ?? "Não foi possível atualizar a reação.",
      });
    },
    [dispatch, timers],
  );

  return { handleReactionUpdated, handleReactionError };
}
