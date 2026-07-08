/**
 * usePins — RF-05 pinned-messages state.
 *
 * Owns the pin list for the active target: initial fetch, a pinnedIds set for
 * O(1) per-message lookup, a togglePin action (REST round-trip, then reload),
 * and a reload() the caller wires to the pin.updated WebSocket event so pins
 * stay live for every readable channel/DM member. Pass null and the hook stays idle.
 *
 * Security: no tokens handled here (authenticatedFetch owns auth); the server
 * enforces target read access. A rejected toggle surfaces a transient defensive
 * error and leaves the list untouched.
 *
 * State lives in a reducer (not useState) so the load effect can dispatch
 * synchronously without the cascading-render lint rule, mirroring useChatSidebar.
 */

import { useCallback, useEffect, useMemo, useReducer, useRef } from "react";

import { fetchPins, pinMessage, unpinMessage, type PinTarget } from "./chatApi";
import type { PinnedItem } from "./chatTypes";

interface PinsState {
  pins: PinnedItem[];
  error: string | null;
}

type Action =
  | { type: "reset" }
  | { type: "loaded"; pins: PinnedItem[] }
  | { type: "error"; error: string }
  | { type: "clear_error" };

const initialState: PinsState = { pins: [], error: null };

function reducer(state: PinsState, action: Action): PinsState {
  switch (action.type) {
    case "reset":
      return initialState;
    case "loaded":
      return { pins: action.pins, error: null };
    case "error":
      return { ...state, error: action.error };
    case "clear_error":
      return { ...state, error: null };
  }
}

export interface UsePinsResult {
  pins: PinnedItem[];
  pinnedIds: Set<string>;
  /** Transient defensive error for a rejected pin/unpin. */
  error: string | null;
  togglePin: (messageId: string, pin: boolean) => void;
  reload: () => void;
}

export function usePins(target: PinTarget | null): UsePinsResult {
  const [state, dispatch] = useReducer(reducer, initialState);
  const abortRef = useRef<AbortController | null>(null);
  const targetKind = target?.kind ?? "";
  const targetId = target?.id ?? "";
  const resolvedTarget = useMemo<PinTarget | null>(() => {
    if (!targetId || (targetKind !== "channel" && targetKind !== "dm")) return null;
    return { kind: targetKind, id: targetId };
  }, [targetKind, targetId]);

  // fetch loads the target's pins. resetFirst clears stale pins when switching
  // targets; a toggle/WS reload keeps the current list visible until the new
  // one arrives (no blink).
  const fetch = useCallback((nextTarget: PinTarget | null, resetFirst: boolean) => {
    abortRef.current?.abort();
    if (!nextTarget?.id) {
      dispatch({ type: "reset" });
      return;
    }
    if (resetFirst) dispatch({ type: "reset" });
    const ctrl = new AbortController();
    abortRef.current = ctrl;
    fetchPins(nextTarget, ctrl.signal).then(
      (pins) => dispatch({ type: "loaded", pins }),
      (err: unknown) => {
        if (err instanceof Error && err.name === "AbortError") return;
        // A failed pin fetch must not break message rendering; show nothing.
        dispatch({ type: "loaded", pins: [] });
      },
    );
  }, []);

  useEffect(() => {
    fetch(resolvedTarget, true);
    return () => abortRef.current?.abort();
  }, [resolvedTarget, fetch]);

  useEffect(() => {
    if (!state.error) return;
    const timer = window.setTimeout(() => dispatch({ type: "clear_error" }), 5_000);
    return () => window.clearTimeout(timer);
  }, [state.error]);

  const reload = useCallback(() => fetch(resolvedTarget, false), [resolvedTarget, fetch]);

  // ponytail: no optimistic update; the REST call is cheap and reload() reflects
  // the authoritative order/cap. A double click just repeats an idempotent call.
  const togglePin = useCallback(
    (messageId: string, pin: boolean) => {
      if (!resolvedTarget) return;
      const currentTarget = resolvedTarget;
      const apply = pin ? pinMessage : unpinMessage;
      void apply(currentTarget, messageId)
        .then(() => fetch(currentTarget, false))
        .catch(() => {
          dispatch({
            type: "error",
            error: pin
              ? "Não foi possível fixar a mensagem."
              : "Não foi possível desafixar a mensagem.",
          });
        });
    },
    [resolvedTarget, fetch],
  );

  const pinnedIds = useMemo(() => new Set(state.pins.map((p) => p.message.id)), [state.pins]);

  return { pins: state.pins, pinnedIds, error: state.error, togglePin, reload };
}
