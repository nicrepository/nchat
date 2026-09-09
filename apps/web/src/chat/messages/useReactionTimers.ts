import { useMemo, useRef } from "react";

/**
 * The rollback deadline of each reaction toggle still awaiting confirmation.
 *
 * Nested by message and then by emoji, so one confirmation clears one wait: a
 * message can carry several toggles at once and each has its own deadline.
 */
export interface ReactionTimers {
  /** Starts (or restarts) the wait for one toggle. */
  start(messageId: string, emoji: string, onTimeout: () => void): void;
  /** Stops one toggle's wait, or every wait on a message when no emoji is named. */
  clear(messageId: string, emoji?: string): void;
  /** Stops every wait — what a server-level refusal, a target change or an unmount does. */
  clearAll(): void;
}

/** How long an unconfirmed toggle is held before it is rolled back. */
const reactionConfirmTimeoutMs = 8_000;

export function useReactionTimers(): ReactionTimers {
  const timers = useRef<Map<string, Map<string, number>>>(new Map());
  return useMemo<ReactionTimers>(() => {
    const clear: ReactionTimers["clear"] = (messageId, emoji) => {
      const forMessage = timers.current.get(messageId);
      if (!forMessage) return;
      if (emoji === undefined) {
        for (const timer of forMessage.values()) window.clearTimeout(timer);
        timers.current.delete(messageId);
        return;
      }
      const timer = forMessage.get(emoji);
      if (timer === undefined) return;
      window.clearTimeout(timer);
      forMessage.delete(emoji);
      if (forMessage.size === 0) timers.current.delete(messageId);
    };
    return {
      clear,
      start(messageId, emoji, onTimeout) {
        // One window per (message, emoji): re-toggling the same emoji restarts
        // its own wait, and a toggle of a different emoji on the same message
        // starts a second one beside it.
        clear(messageId, emoji);
        const timer = window.setTimeout(() => {
          timers.current.get(messageId)?.delete(emoji);
          onTimeout();
        }, reactionConfirmTimeoutMs);
        const forMessage = timers.current.get(messageId) ?? new Map<string, number>();
        forMessage.set(emoji, timer);
        timers.current.set(messageId, forMessage);
      },
      clearAll() {
        for (const forMessage of timers.current.values()) {
          for (const timer of forMessage.values()) window.clearTimeout(timer);
        }
        timers.current.clear();
      },
    };
  }, []);
}
