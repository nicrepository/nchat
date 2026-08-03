/**
 * useChannelDetails — data for the channel-details panel (issue #435).
 *
 * Two independent requests behind one hook: the channel projection from
 * chat-service and the recent attachments from file-service. They are separate
 * sections of the panel and separate services, so a failure in one leaves the
 * other rendered — a single combined state would let a file-service outage
 * blank out the channel's own metadata.
 *
 * Correctness properties this hook owns:
 *  - every request is keyed by channelId, and a channel switch aborts the
 *    in-flight one *and* resets to loading, so the previous channel's data is
 *    never shown under the new channel's name;
 *  - a response that arrives after the key changed is dropped by the abort
 *    signal, so a slow reply for channel A cannot overwrite channel B;
 *  - unmount aborts too, so nothing dispatches into a dead component.
 *
 * State lives in a reducer so the load effect can dispatch synchronously
 * without tripping react-hooks/set-state-in-effect, mirroring usePins.
 *
 * Security: no tokens are handled here; the server enforces channel access and
 * answers 404 for anything the caller cannot read.
 */

import { useCallback, useEffect, useReducer, useRef } from "react";

import { fetchChannelDetails } from "./chatApi";
import { fetchChannelAttachments } from "./filesApi";
import type { ChannelAttachment, ChannelDetails } from "./chatTypes";

/** How many files the panel previews. The server clamps anything larger. */
export const channelFilesPreviewLimit = 5;

export type AsyncSection<T> =
  | { status: "loading" }
  | { status: "ready"; data: T }
  | { status: "error" };

/** The two async sections the reducer owns. Kept apart from the hook's return
 * type so `reload` — a callback, not state — never has to be threaded through
 * the reducer or restated in every action's result. */
export interface ChannelDetailsSections {
  details: AsyncSection<ChannelDetails>;
  files: AsyncSection<ChannelAttachment[]>;
}

export interface ChannelDetailsState extends ChannelDetailsSections {
  /**
   * Refetches the panel from the server (issue #398).
   *
   * The single reconciliation path after a membership change: both the HTTP
   * response and the members.added WebSocket event call this, and because it
   * replaces the section wholesale rather than appending, the two arriving
   * together cannot produce a duplicated member row or a double-counted total.
   * It reuses the same abort-keyed load the channel switch uses, so a refetch
   * racing a channel switch is dropped by the same signal.
   *
   * It does not blank the panel first: the currently rendered data stays until
   * the newer version replaces it, so nothing inside unmounts and focus is not
   * dropped from a control the user is still using.
   */
  reload: () => void;
}

type Action =
  | { type: "reset" }
  | { type: "details_ready"; details: ChannelDetails }
  | { type: "details_error" }
  | { type: "files_ready"; files: ChannelAttachment[] }
  | { type: "files_error" };

const initialState: ChannelDetailsSections = {
  details: { status: "loading" },
  files: { status: "loading" },
};

function reducer(state: ChannelDetailsSections, action: Action): ChannelDetailsSections {
  switch (action.type) {
    case "reset":
      return initialState;
    case "details_ready":
      return { ...state, details: { status: "ready", data: action.details } };
    case "details_error":
      return { ...state, details: { status: "error" } };
    case "files_ready":
      return { ...state, files: { status: "ready", data: action.files } };
    case "files_error":
      return { ...state, files: { status: "error" } };
  }
}

function isAbort(error: unknown): boolean {
  return error instanceof Error && error.name === "AbortError";
}

/**
 * Loads the panel's data for `channelId`. Pass null (panel closed, or the open
 * conversation is a DM) and the hook stays idle and issues no request.
 */
export function useChannelDetails(channelId: string | null): ChannelDetailsState {
  const [state, dispatch] = useReducer(reducer, initialState);
  const abortRef = useRef<AbortController | null>(null);

  /**
   * Fetches both sections for `id`.
   *
   * `reset` is the difference between the two callers, and it is not cosmetic.
   * A channel *switch* must reset: showing the previous channel's members under
   * the new channel's name would be wrong, and briefly so in a way the user
   * cannot detect. A *refetch* of the channel already displayed must not: the
   * data is about to be replaced by a newer version of itself, and blanking the
   * section in between unmounts everything inside it — including the control the
   * user just activated, which drops keyboard focus to <body> mid-flow.
   */
  const load = useCallback((id: string | null, reset = true) => {
    abortRef.current?.abort();
    if (reset) {
      dispatch({ type: "reset" });
    }
    if (!id) return;

    const controller = new AbortController();
    abortRef.current = controller;

    fetchChannelDetails(id, controller.signal).then(
      (details) => {
        if (controller.signal.aborted) return;
        dispatch({ type: "details_ready", details });
      },
      (error: unknown) => {
        if (controller.signal.aborted || isAbort(error)) return;
        dispatch({ type: "details_error" });
      },
    );

    fetchChannelAttachments(id, channelFilesPreviewLimit, controller.signal).then(
      (files) => {
        if (controller.signal.aborted) return;
        dispatch({ type: "files_ready", files });
      },
      (error: unknown) => {
        if (controller.signal.aborted || isAbort(error)) return;
        dispatch({ type: "files_error" });
      },
    );
  }, []);

  useEffect(() => {
    load(channelId);
    return () => abortRef.current?.abort();
  }, [channelId, load]);

  // Refetch in place: same channel, newer data, no blank frame and no unmount.
  // Bound to the current channelId rather than to a ref, so it can never refetch
  // the channel the panel has already moved away from.
  const reload = useCallback(() => load(channelId, false), [load, channelId]);

  return { ...state, reload };
}
