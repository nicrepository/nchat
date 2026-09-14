import { useCallback } from "react";

import type { ReactionTimers } from "./useReactionTimers";
import type { Action } from "./types";

interface Options {
  dispatch: (action: Action) => void;
  timers: ReactionTimers;
  /** Sends the toggle over the socket; false when the connection is not open. */
  sendToggle: (messageId: string, emoji: string) => boolean;
}

/**
 * What this reader asks for: an optimistic toggle, held only as long as the
 * server has a chance to confirm it.
 *
 * A toggle the socket could not even send is reverted immediately. One that was
 * sent gets a deadline, because a confirmation that never arrives would
 * otherwise leave the reader looking at a reaction the server does not have.
 */
export function useReactionToggle({
  dispatch,
  timers,
  sendToggle,
}: Options): (messageId: string, emoji: string) => void {
  return useCallback(
    (messageId: string, emoji: string) => {
      dispatch({ type: "reaction_optimistic", messageId, emoji });
      if (!sendToggle(messageId, emoji)) {
        dispatch({
          type: "reaction_revert",
          messageId,
          emoji,
          error: "Conexão em tempo real indisponível. Tente novamente.",
        });
        return;
      }
      timers.start(messageId, emoji, () => {
        dispatch({
          type: "reaction_revert",
          messageId,
          emoji,
          error: "Não foi possível confirmar a reação. Tente novamente.",
        });
      });
    },
    [dispatch, sendToggle, timers],
  );
}
