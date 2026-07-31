/**
 * useConversationDetails — data for the details panel of a channel (issue #435)
 * or an ad-hoc group (issue #441).
 *
 * Two independent requests behind one hook: the conversation projection from
 * chat-service and the recent attachments from file-service. They are separate
 * sections of the panel and separate services, so a failure in one leaves the
 * other rendered — a single combined state would let a file-service outage
 * blank out the conversation's own metadata.
 *
 * Correctness properties this hook owns:
 *  - every request is keyed by the *pair* (kind, id), not by the id alone: a
 *    channel and a conversation are separate id spaces, so keying on the id
 *    could hold a stale result across a channel → group switch;
 *  - a target switch aborts the in-flight request *and* resets to loading, so
 *    the previous conversation's data is never shown under the new one's name;
 *  - a response that arrives after the key changed is dropped by the abort
 *    signal, so a slow reply for A cannot overwrite B;
 *  - unmount aborts too, so nothing dispatches into a dead component.
 *
 * State lives in a reducer so the load effect can dispatch synchronously
 * without tripping react-hooks/set-state-in-effect, mirroring usePins.
 *
 * Security: no tokens are handled here; the server enforces channel access and
 * answers 404 for anything the caller cannot read.
 */

import { useCallback, useEffect, useReducer, useRef } from "react";

import { fetchChannelDetails, fetchGroupDetails } from "./chatApi";
import { fetchConversationAttachments } from "./filesApi";
import type { ChannelAttachment, ConversationDetails } from "./chatTypes";

/**
 * What the panel is being opened for.
 *
 * `kind` is the domain discriminant — "channel" for chat.channels, "group" for
 * a chat.dm_conversations row of type 'group' — and never something derived
 * from the URL or from how many people are in the conversation.
 */
export interface ConversationDetailsTarget {
  kind: "channel" | "group";
  id: string;
}

/** How many files the panel previews. The server clamps anything larger. */
export const channelFilesPreviewLimit = 5;

export type AsyncSection<T> =
  | { status: "loading" }
  | { status: "ready"; data: T }
  | { status: "error" };

export interface ConversationDetailsState {
  details: AsyncSection<ConversationDetails>;
  files: AsyncSection<ChannelAttachment[]>;
}

type Action =
  | { type: "reset" }
  | { type: "details_ready"; details: ConversationDetails }
  | { type: "details_error" }
  | { type: "files_ready"; files: ChannelAttachment[] }
  | { type: "files_error" };

const initialState: ConversationDetailsState = {
  details: { status: "loading" },
  files: { status: "loading" },
};

function reducer(state: ConversationDetailsState, action: Action): ConversationDetailsState {
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
 * Loads the panel's data for `target`. Pass null (panel closed, or the open
 * conversation is a 1:1 DM, which has no panel in this release) and the hook
 * stays idle and issues no request.
 */
export function useConversationDetails(
  target: ConversationDetailsTarget | null,
): ConversationDetailsState {
  const [state, dispatch] = useReducer(reducer, initialState);
  const abortRef = useRef<AbortController | null>(null);

  // The effect depends on the two primitives rather than on the object, so a
  // caller that rebuilds the target literal on every render does not refetch.
  const kind = target?.kind ?? "";
  const id = target?.id ?? "";

  const load = useCallback((nextKind: string, nextID: string) => {
    abortRef.current?.abort();
    // Reset first, always: the panel must show a loading state for the new
    // conversation rather than the previous one's participants and files.
    dispatch({ type: "reset" });
    if (!nextID || (nextKind !== "channel" && nextKind !== "group")) return;

    const controller = new AbortController();
    abortRef.current = controller;

    const details =
      nextKind === "channel"
        ? fetchChannelDetails(nextID, controller.signal).then(
            (channel): ConversationDetails => ({ kind: "channel", ...channel }),
          )
        : fetchGroupDetails(nextID, controller.signal).then(
            (group): ConversationDetails => ({ kind: "group", ...group }),
          );

    details.then(
      (resolved) => {
        if (controller.signal.aborted) return;
        dispatch({ type: "details_ready", details: resolved });
      },
      (error: unknown) => {
        if (controller.signal.aborted || isAbort(error)) return;
        dispatch({ type: "details_error" });
      },
    );

    // A group's attachments live under the DM destination, never the channel
    // one: the two are separate resources with separate authorization.
    fetchConversationAttachments(
      { kind: nextKind === "channel" ? "channel" : "dm", id: nextID },
      channelFilesPreviewLimit,
      controller.signal,
    ).then(
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
    load(kind, id);
    return () => abortRef.current?.abort();
  }, [kind, id, load]);

  return state;
}
