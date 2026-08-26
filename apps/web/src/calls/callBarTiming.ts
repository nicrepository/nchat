import { useEffect, useState } from "react";

/**
 * Shared elapsed-time tick for the persistent call bars (ActiveResourceCallBar,
 * issue #642, and ActiveDirectCallBar, issue #673) — the one piece of real
 * duplication between the two: both are presentation-only bars ticking a
 * display label off the SAME kind of authoritative startedAt, never their
 * own clock or timer state.
 */

export function formatElapsed(elapsedMs: number): string {
  const totalSeconds = Math.max(0, Math.floor(elapsedMs / 1000));
  const hours = Math.floor(totalSeconds / 3600);
  const minutes = Math.floor((totalSeconds % 3600) / 60);
  const seconds = totalSeconds % 60;
  const mm = String(minutes).padStart(2, "0");
  const ss = String(seconds).padStart(2, "0");
  return hours > 0 ? `${hours}:${mm}:${ss}` : `${mm}:${ss}`;
}

/**
 * Never resets on re-render of the caller as long as startedAt itself is
 * unchanged — startedAtMs is recomputed from the prop on every call, so a
 * caller re-mounting this hook still derives the correct elapsed time from
 * the real origin instant, never 00:00.
 */
export function useElapsedLabel(startedAt: string): string {
  const startedAtMs = Date.parse(startedAt);
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, []);
  return formatElapsed(now - startedAtMs);
}
