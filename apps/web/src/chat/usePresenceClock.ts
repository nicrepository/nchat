/**
 * usePresenceClock — "now", refreshed once a minute, for the surfaces that say
 * how long ago something happened ("visto há 18 min", issue #798).
 *
 * One timer for the whole tab, shared by every subscriber and stopped when the
 * last one leaves. Only the detail surfaces subscribe — the conversation header
 * and the details panel — never an avatar or a list row, and only while the
 * line they show depends on elapsed time: an available or busy person needs no
 * clock at all.
 */

import { useSyncExternalStore } from "react";

export const PRESENCE_CLOCK_MS = 60_000;

let now = Date.now();
let timer: number | null = null;
const listeners = new Set<() => void>();

function tick(): void {
  now = Date.now();
  for (const listener of [...listeners]) listener();
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  if (listeners.size === 1) {
    // Fresh on the first subscriber, so a surface that has not needed the clock
    // for an hour does not start an hour behind.
    now = Date.now();
    timer = window.setInterval(tick, PRESENCE_CLOCK_MS);
  }
  return () => {
    listeners.delete(listener);
    if (listeners.size > 0 || timer === null) return;
    window.clearInterval(timer);
    timer = null;
  };
}

const idle = () => () => {};

function snapshot(): number {
  return now;
}

export function usePresenceClock(active: boolean): number {
  return useSyncExternalStore(active ? subscribe : idle, snapshot, snapshot);
}

/** Whether a state's description depends on elapsed time. */
export function presenceNeedsClock(state: string): boolean {
  return state === "away" || state === "brb" || state === "offline";
}
