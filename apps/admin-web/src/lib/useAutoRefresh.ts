import { useEffect, useState } from "react";

/**
 * The two time-driven behaviours the operational screens need.
 *
 * Both are here rather than inline in the pages because both are easy to get
 * wrong in the same direction: an interval that keeps firing after unmount, or
 * one that keeps polling a tab nobody is looking at.
 */

/**
 * How often the operational screens reload on their own.
 *
 * A minute, deliberately: the server caches a collection for far less than
 * that, so this is well clear of being polling, and an operator watching an
 * incident has the manual refresh for anything faster. Anything shorter would
 * turn every open tab into steady load on the integrations for no extra
 * information.
 */
export const AUTO_REFRESH_MS = 60_000;

/**
 * Reloads on an interval, and only while the tab is visible.
 *
 * The visibility check is the part that matters at scale: a console left open
 * on a second monitor overnight would otherwise keep asking, and a background
 * tab produces no information anybody reads. Coming back to the tab reloads
 * immediately, so what is on screen when an operator returns is current rather
 * than however old the tab is.
 */
export function useAutoRefresh(reload: () => void, intervalMS: number = AUTO_REFRESH_MS): void {
  useEffect(() => {
    if (intervalMS <= 0) return undefined;

    const refreshWhenVisible = () => {
      if (document.visibilityState === "hidden") return;
      reload();
    };
    const timer = window.setInterval(refreshWhenVisible, intervalMS);
    document.addEventListener("visibilitychange", refreshWhenVisible);
    return () => {
      window.clearInterval(timer);
      document.removeEventListener("visibilitychange", refreshWhenVisible);
    };
  }, [reload, intervalMS]);
}

/** How often the relative "last checked" labels are recomputed. */
export const CLOCK_TICK_MS = 10_000;

/**
 * A ticking reference instant for relative timestamps.
 *
 * Seeded from a value the caller already has — the collection timestamp —
 * rather than from the clock, so the first paint is correct and pure, and the
 * effect takes over from there. One instant is shared by every row on a
 * render, so a single collection is never described as if its checks had
 * happened at different times.
 */
export function useNow(seed: number, tickMS: number = CLOCK_TICK_MS): number {
  const [now, setNow] = useState(seed);
  useEffect(() => {
    const tick = () => setNow(Date.now());
    tick();
    if (tickMS <= 0) return undefined;
    const timer = window.setInterval(tick, tickMS);
    return () => window.clearInterval(timer);
  }, [tickMS]);
  return now;
}
