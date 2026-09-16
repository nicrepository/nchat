import { useCallback } from "react";

import { useLatestRef } from "./useLatestRef";
import type { ConversationTarget } from "./types";

/** Any socket event addressed to one conversation. */
interface TargetScopedEvent {
  target_type: "channel" | "dm";
  target_id: string;
}

/**
 * A socket event this hook does not act on itself, handed to the caller that
 * asked for it — filtered to the active target first.
 *
 * Pins, membership, attachment verdicts and typing all travel over the
 * connection this hook family already owns, so they are routed through it
 * rather than through a second socket per conversation. The filter is the point:
 * a verdict about another conversation must not make this one react, and the
 * WebSocket hook's own filter is not the only line of defence.
 *
 * `onMatch` runs before the caller's listener and only for a matching event —
 * that is where this hook's own state change belongs, when it has one.
 */
export function useForwardedTargetEvent<E extends TargetScopedEvent>(
  target: ConversationTarget,
  listener: ((event: E) => void) | undefined,
  onMatch?: (event: E) => void,
): (event: E) => void {
  const { kind, targetId } = target;
  const latestListener = useLatestRef(listener);
  const latestOnMatch = useLatestRef(onMatch);
  return useCallback(
    (event: E) => {
      if (event.target_type !== kind || event.target_id !== targetId) return;
      latestOnMatch.current?.(event);
      latestListener.current?.(event);
    },
    [kind, targetId, latestListener, latestOnMatch],
  );
}
