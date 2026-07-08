/**
 * usePins — RF-05 channel pinned-messages state.
 *
 * Owns the pin list for the active channel: initial fetch, a pinnedIds set for
 * O(1) per-message lookup, a togglePin action (REST round-trip, then reload),
 * and a reload() the caller wires to the pin.updated WebSocket event so pins
 * stay live for every channel member. DMs have no pins — pass an empty channelId
 * and the hook stays idle.
 *
 * Security: no tokens handled here (authenticatedFetch owns auth); the server
 * enforces role (403) and channel read access (404). A rejected toggle surfaces
 * a transient error and leaves the list untouched.
 *
 * State lives in a reducer (not useState) so the load effect can dispatch
 * synchronously without the cascading-render lint rule, mirroring useChatSidebar.
 */

import { useCallback, useEffect, useMemo, useReducer, useRef } from "react";

import { fetchChannelPins, pinMessage, unpinMessage } from "./chatApi";
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
  /** Transient error for a rejected pin/unpin (e.g. 403 for non-moderators). */
  error: string | null;
  togglePin: (messageId: string, pin: boolean) => void;
  reload: () => void;
}

export function usePins(channelId: string): UsePinsResult {
  const [state, dispatch] = useReducer(reducer, initialState);
  const abortRef = useRef<AbortController | null>(null);

  // fetch loads the channel's pins. resetFirst clears stale pins when switching
  // channels; a toggle/WS reload keeps the current list visible until the new
  // one arrives (no blink).
  const fetch = useCallback((id: string, resetFirst: boolean) => {
    abortRef.current?.abort();
    if (!id) {
      dispatch({ type: "reset" });
      return;
    }
    if (resetFirst) dispatch({ type: "reset" });
    const ctrl = new AbortController();
    abortRef.current = ctrl;
    fetchChannelPins(id, ctrl.signal).then(
      (pins) => dispatch({ type: "loaded", pins }),
      (err: unknown) => {
        if (err instanceof Error && err.name === "AbortError") return;
        // A failed pin fetch must not break message rendering; show nothing.
        dispatch({ type: "loaded", pins: [] });
      },
    );
  }, []);

  useEffect(() => {
    fetch(channelId, true);
    return () => abortRef.current?.abort();
  }, [channelId, fetch]);

  useEffect(() => {
    if (!state.error) return;
    const timer = window.setTimeout(() => dispatch({ type: "clear_error" }), 5_000);
    return () => window.clearTimeout(timer);
  }, [state.error]);

  const reload = useCallback(() => fetch(channelId, false), [channelId, fetch]);

  // ponytail: no optimistic update; the REST call is cheap and reload() reflects
  // the authoritative order/cap. A double click just repeats an idempotent call.
  const togglePin = useCallback(
    (messageId: string, pin: boolean) => {
      if (!channelId) return;
      const apply = pin ? pinMessage : unpinMessage;
      void apply(channelId, messageId)
        .then(() => fetch(channelId, false))
        .catch(() => {
          dispatch({
            type: "error",
            error: pin
              ? "Não foi possível fixar a mensagem. Você precisa de permissão de moderador."
              : "Não foi possível desafixar a mensagem.",
          });
        });
    },
    [channelId, fetch],
  );

  const pinnedIds = useMemo(() => new Set(state.pins.map((p) => p.message.id)), [state.pins]);

  return { pins: state.pins, pinnedIds, error: state.error, togglePin, reload };
}
