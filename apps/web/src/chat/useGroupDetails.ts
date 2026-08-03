/**
 * useGroupDetails — data for the group-details panel (issues #441, #398).
 *
 * The group counterpart of useChannelDetails, and deliberately its own hook
 * rather than a mode flag on that one: a group has one section (participants)
 * where a channel has three (members, pin, files), and the two fetch different
 * endpoints with different payload shapes. Parameterising one hook over that
 * would be a bigger surface than two small hooks that each do one thing.
 *
 * Correctness properties it owns, mirroring the channel hook:
 *  - every request is keyed by conversationId, and a conversation switch aborts
 *    the in-flight one *and* resets to loading, so one group's participants are
 *    never shown under another group's name;
 *  - a response that arrives after the key changed is dropped by the abort
 *    signal, so a slow reply for group A cannot overwrite group B;
 *  - unmount aborts too, so nothing dispatches into a dead component;
 *  - `reload` refetches in place without blanking the panel, so the control the
 *    user just activated is not unmounted from under their keyboard focus.
 *
 * Security: no tokens are handled here; the server enforces participation and
 * answers 404 for anything the caller cannot read, including a 1:1.
 */

import { useCallback, useEffect, useReducer, useRef } from "react";

import { fetchGroupDetails } from "./chatApi";
import type { GroupDetails } from "./chatTypes";
import type { AsyncSection } from "./useChannelDetails";

export interface GroupDetailsSections {
  details: AsyncSection<GroupDetails>;
}

export interface GroupDetailsState extends GroupDetailsSections {
  /**
   * Refetches from the server. The single reconciliation path after a
   * membership change: the HTTP response and the members.added event both call
   * it, and because it replaces the section wholesale rather than appending,
   * the two arriving together cannot duplicate a participant or double a count.
   */
  reload: () => void;
}

type Action = { type: "reset" } | { type: "ready"; details: GroupDetails } | { type: "error" };

const initialState: GroupDetailsSections = { details: { status: "loading" } };

// The prior state is deliberately unused: this hook owns a single section, so
// every action produces a complete replacement rather than a merge.
function reducer(_state: GroupDetailsSections, action: Action): GroupDetailsSections {
  switch (action.type) {
    case "reset":
      return initialState;
    case "ready":
      return { details: { status: "ready", data: action.details } };
    case "error":
      return { details: { status: "error" } };
  }
}

function isAbort(error: unknown): boolean {
  return error instanceof Error && error.name === "AbortError";
}

/**
 * Loads the panel's data for `conversationId`. Pass null (panel closed, or the
 * open conversation is a channel or a 1:1) and the hook stays idle.
 */
export function useGroupDetails(conversationId: string | null): GroupDetailsState {
  const [state, dispatch] = useReducer(reducer, initialState);
  const abortRef = useRef<AbortController | null>(null);

  const load = useCallback((id: string | null, reset = true) => {
    abortRef.current?.abort();
    if (reset) {
      dispatch({ type: "reset" });
    }
    if (!id) return;

    const controller = new AbortController();
    abortRef.current = controller;

    fetchGroupDetails(id, controller.signal).then(
      (details) => {
        if (controller.signal.aborted) return;
        dispatch({ type: "ready", details });
      },
      (error: unknown) => {
        if (controller.signal.aborted || isAbort(error)) return;
        dispatch({ type: "error" });
      },
    );
  }, []);

  useEffect(() => {
    load(conversationId);
    return () => abortRef.current?.abort();
  }, [conversationId, load]);

  const reload = useCallback(() => load(conversationId, false), [load, conversationId]);

  return { ...state, reload };
}
