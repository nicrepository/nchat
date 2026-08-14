/**
 * useTypingIndicator — client-side debounce/throttle/expiry for the typing
 * indicator (channels and DMs), over the shared chat WebSocket.
 *
 * Owns two independent halves of one small state machine:
 *
 *  - outbound: `notifyActivity()` (called from the composer's real
 *    content-changed callback, never a raw keydown) sends `typing.start`
 *    immediately on the first activity, then renews at most once per
 *    ACTIVITY_RENEW_THROTTLE_MS while activity continues, and schedules an
 *    inactivity stop. `stop()` is exposed directly for the cases that must
 *    not wait out the inactivity timer: a successful send, the composer
 *    being cleared, and — via the effect below — switching target or
 *    unmounting.
 *  - inbound: `handleRemoteEvent()` (wired as useMessages' onTypingUpdated)
 *    folds `typing.updated` frames into a small per-user map, with its own
 *    local expiry (REMOTE_EXPIRY_MS) so a peer's indicator clears even if the
 *    `stop` frame or the server's own TTL-driven cleanup never reaches this
 *    tab. The local user's own echo is dropped here — self-suppression does
 *    not depend on the caller remembering to filter it out.
 *
 * No shared debounce/throttle utility is used, matching this codebase's
 * established pattern (see chatSocket.ts's presence activity throttle and
 * lib/api.ts's upload-progress throttle): a small colocated implementation
 * rather than a generic abstraction.
 *
 * Ordering of the four windows below is deliberate: a renewal happens well
 * before the server's TTL could expire, an explicit stop usually happens
 * before that TTL would anyway, and the local defensive expiry is longer
 * than the server TTL so it only ever fires when something else already
 * should have cleared the entry.
 */

import { useCallback, useEffect, useRef, useState } from "react";

import type { WSTypingUpdatedEvent } from "./useChatWebSocket";

/** Renew typing.start at most this often while the user keeps typing. */
const ACTIVITY_RENEW_THROTTLE_MS = 2_500;
/** Send typing.stop after this long without a new content-changing edit. */
const STOP_AFTER_INACTIVITY_MS = 4_000;
/**
 * Locally clear a remote user's "is typing" entry if no renewed
 * typing.updated arrives within this window. Comfortably longer than the
 * server's own Valkey TTL (see typingTTL in chat-service, 6s) plus network
 * latency, so this only fires when the stop broadcast itself was lost.
 */
const REMOTE_EXPIRY_MS = 8_000;

interface RemoteTypingEntry {
  updatedAtMs: number;
  displayName?: string;
}

export interface UseTypingIndicatorOptions {
  kind: "channel" | "dm";
  targetId: string;
  currentUserId: string;
  /** From useMessages()/useChatWebSocket()'s ChatWebSocketActions. */
  sendTyping: (isTyping: boolean) => boolean;
  /** While true, activity never starts a new typing session. */
  disabled?: boolean;
}

export interface TypingIndicatorController {
  /** Call on real, content-changing composer activity (never a raw keydown). */
  notifyActivity: () => void;
  /** Call on successful send and when the composer is cleared. */
  stop: () => void;
  /** Feed one inbound typing.updated event (wire as useMessages' onTypingUpdated). */
  handleRemoteEvent: (event: WSTypingUpdatedEvent) => void;
  /** Other users (never the local one) currently typing in this target. */
  typingUserIds: readonly string[];
  /**
   * Server-resolved display name for each currently-typing remote user, when
   * known. A user id absent here means the caller should fall back to its own
   * heuristic (DM roster / recent message sender / "Alguém").
   */
  typingDisplayNameByUserId: ReadonlyMap<string, string>;
}

export function useTypingIndicator({
  kind,
  targetId,
  currentUserId,
  sendTyping,
  disabled = false,
}: UseTypingIndicatorOptions): TypingIndicatorController {
  const [typingUserIds, setTypingUserIds] = useState<readonly string[]>([]);
  const [typingDisplayNameByUserId, setTypingDisplayNameByUserId] = useState<ReadonlyMap<string, string>>(
    new Map(),
  );

  const sendTypingRef = useRef(sendTyping);
  useEffect(() => {
    sendTypingRef.current = sendTyping;
  }, [sendTyping]);
  const disabledRef = useRef(disabled);
  useEffect(() => {
    disabledRef.current = disabled;
  }, [disabled]);

  // ── outbound ─────────────────────────────────────────────────────────────

  // Whether this tab currently considers itself "typing" — i.e. whether a
  // typing.stop is owed before going quiet. Not React state: nothing here
  // needs to re-render on this transition.
  const startedRef = useRef(false);
  const lastStartSentAtRef = useRef(0);
  const inactivityTimerRef = useRef<number | null>(null);

  const clearInactivityTimer = useCallback(() => {
    if (inactivityTimerRef.current !== null) {
      window.clearTimeout(inactivityTimerRef.current);
      inactivityTimerRef.current = null;
    }
  }, []);

  const stop = useCallback(() => {
    clearInactivityTimer();
    if (!startedRef.current) return;
    startedRef.current = false;
    sendTypingRef.current(false);
  }, [clearInactivityTimer]);

  const scheduleInactivityStop = useCallback(() => {
    clearInactivityTimer();
    inactivityTimerRef.current = window.setTimeout(() => {
      inactivityTimerRef.current = null;
      stop();
    }, STOP_AFTER_INACTIVITY_MS);
  }, [clearInactivityTimer, stop]);

  const notifyActivity = useCallback(() => {
    if (disabledRef.current) return;
    const now = Date.now();
    if (!startedRef.current || now - lastStartSentAtRef.current >= ACTIVITY_RENEW_THROTTLE_MS) {
      startedRef.current = true;
      lastStartSentAtRef.current = now;
      sendTypingRef.current(true);
    }
    scheduleInactivityStop();
  }, [scheduleInactivityStop]);

  // Target change or unmount: stop asserting typing on whatever this hook was
  // previously bound to, rather than leaving it to the server TTL. Depends on
  // kind/targetId directly — explicit about what "changed target" means here
  // — rather than on sendTyping's identity.
  useEffect(() => {
    return () => {
      clearInactivityTimer();
      if (startedRef.current) {
        startedRef.current = false;
        sendTypingRef.current(false);
      }
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps -- see comment above
  }, [kind, targetId]);

  // ── inbound ──────────────────────────────────────────────────────────────

  const remoteRef = useRef<Map<string, RemoteTypingEntry>>(new Map());
  const remoteTimersRef = useRef<Map<string, number>>(new Map());

  const clearRemoteTimer = useCallback((userId: string) => {
    const timer = remoteTimersRef.current.get(userId);
    if (timer !== undefined) {
      window.clearTimeout(timer);
      remoteTimersRef.current.delete(userId);
    }
  }, []);

  const recomputeTyping = useCallback(() => {
    const ids = Array.from(remoteRef.current.keys());
    const names = new Map<string, string>();
    for (const [userId, entry] of remoteRef.current) {
      if (entry.displayName) names.set(userId, entry.displayName);
    }
    setTypingUserIds(ids);
    setTypingDisplayNameByUserId(names);
  }, []);

  const handleRemoteEvent = useCallback(
    (event: WSTypingUpdatedEvent) => {
      const typing = event.typing;
      if (!typing || typing.user_id === currentUserId) return;
      const userId = typing.user_id;
      clearRemoteTimer(userId);

      if (!typing.is_typing) {
        if (remoteRef.current.delete(userId)) recomputeTyping();
        return;
      }

      const now = Date.now();
      const previous = remoteRef.current.get(userId);
      const displayName = typing.user_display_name || previous?.displayName;
      const changed = !previous || previous.displayName !== displayName;
      remoteRef.current.set(userId, { updatedAtMs: now, displayName });
      if (changed) recomputeTyping();

      const timer = window.setTimeout(() => {
        remoteTimersRef.current.delete(userId);
        const entry = remoteRef.current.get(userId);
        // Only clear if nothing renewed it in the meantime — a defensive
        // re-check even though the timer was already cleared and replaced on
        // every renewal, so this can only find a truly stale entry.
        if (entry && entry.updatedAtMs === now) {
          remoteRef.current.delete(userId);
          recomputeTyping();
        }
      }, REMOTE_EXPIRY_MS);
      remoteTimersRef.current.set(userId, timer);
    },
    [currentUserId, clearRemoteTimer, recomputeTyping],
  );

  // Target change: nobody is known to be typing in the new conversation yet.
  useEffect(() => {
    for (const timer of remoteTimersRef.current.values()) window.clearTimeout(timer);
    remoteTimersRef.current.clear();
    remoteRef.current.clear();
    setTypingUserIds([]);
    setTypingDisplayNameByUserId(new Map());
  }, [kind, targetId]);

  // Unmount: clear any outstanding local-expiry timers.
  useEffect(() => {
    return () => {
      for (const timer of remoteTimersRef.current.values()) window.clearTimeout(timer);
    };
  }, []);

  return { notifyActivity, stop, handleRemoteEvent, typingUserIds, typingDisplayNameByUserId };
}
