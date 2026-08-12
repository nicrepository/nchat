import { useEffect, useSyncExternalStore } from "react";

import { getSessionGeneration, onAuthChange } from "../lib/authSession";
import { fetchMyProfile, type SelfProfile } from "./profileApi";

/**
 * The authenticated user's own profile, shared by every screen that shows it.
 *
 * One responsibility: hold the last profile GET /api/auth/me returned for the
 * *current* session, and tell subscribers when it changes. It decides nothing
 * about identity or authorisation — the server does, from the session token —
 * and it persists nothing: the cache is module state, gone on reload, never in
 * Web Storage.
 *
 * It is a cache and an invalidation rule, not a second auth architecture:
 *   - every entry is stamped with the session generation it was fetched under,
 *     so a reader under session B can never be handed session A's profile, not
 *     even for the render between the switch and the refetch;
 *   - a confirmed profile change (avatar upload/removal) calls
 *     `refreshSelfProfile`, so the sidebar follows without a reload, without
 *     polling and without trusting an optimistic value the server has not
 *     acknowledged.
 */

export type SelfProfileState =
  | { status: "loading" }
  | { status: "error" }
  | { status: "ready"; profile: SelfProfile };

/** A state together with the session it belongs to. */
type Entry = { generation: number; state: SelfProfileState };

const LOADING: SelfProfileState = { status: "loading" };

// Generation -1 belongs to no session, so the very first read is always a miss.
let entry: Entry = { generation: -1, state: LOADING };
let inFlight: AbortController | null = null;
const listeners = new Set<() => void>();

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

function publish(next: Entry): void {
  entry = next;
  for (const listener of listeners) listener();
}

function load(generation: number): void {
  // A request for the session we are leaving has nothing left to answer.
  inFlight?.abort();
  const controller = new AbortController();
  inFlight = controller;
  publish({ generation, state: LOADING });

  fetchMyProfile(controller.signal).then(
    (profile) => {
      if (controller.signal.aborted || getSessionGeneration() !== generation) return;
      publish({ generation, state: { status: "ready", profile } });
    },
    () => {
      // A failed load is an error, never "a user without an avatar".
      if (controller.signal.aborted || getSessionGeneration() !== generation) return;
      publish({ generation, state: { status: "error" } });
    },
  );
}

/** Loads the profile if what is cached does not belong to the current session. */
export function ensureSelfProfile(): void {
  const generation = getSessionGeneration();
  if (entry.generation !== generation) load(generation);
}

/** Refetches after the server confirmed a profile change. */
export function refreshSelfProfile(): void {
  load(getSessionGeneration());
}

/** Drops the cache. For test isolation only. */
export function _resetSelfProfile(): void {
  inFlight?.abort();
  inFlight = null;
  entry = { generation: -1, state: LOADING };
}

/**
 * The current user's profile for the session that is active *now*.
 *
 * Both the session generation and the cache are external stores, so a session
 * change re-renders immediately and the stale entry is filtered out here rather
 * than after an effect has had a chance to run.
 */
export function useSelfProfile(): SelfProfileState {
  const generation = useSyncExternalStore(onAuthChange, getSessionGeneration);
  const current = useSyncExternalStore(subscribe, () => entry);

  useEffect(() => {
    ensureSelfProfile();
  }, [generation]);

  return current.generation === generation ? current.state : LOADING;
}
