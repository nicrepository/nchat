import { useCallback, useEffect, useReducer } from "react";

import { fetchSidebarData } from "./chatApi";
import type { Channel, DMConversation } from "./chatTypes";

// ── State ────────────────────────────────────────────────────────────────────

type SidebarState =
  | { status: "loading" }
  | { status: "error"; error: string }
  | {
      status: "ready";
      currentUserId: string;
      channels: Channel[];
      dms: DMConversation[];
      /** Server-derived; the create endpoint remains authoritative. */
      canCreateChannel: boolean;
    };

type Action =
  | {
      type: "loaded";
      currentUserId: string;
      channels: Channel[];
      dms: DMConversation[];
      canCreateChannel: boolean;
    }
  | { type: "error"; error: string }
  | { type: "reload" };

function reducer(_state: SidebarState, action: Action): SidebarState {
  switch (action.type) {
    case "loaded":
      return {
        status: "ready",
        currentUserId: action.currentUserId,
        channels: action.channels,
        dms: action.dms,
        canCreateChannel: action.canCreateChannel,
      };
    case "error":
      return { status: "error", error: action.error };
    case "reload":
      return { status: "loading" };
  }
}

// ── Hook ─────────────────────────────────────────────────────────────────────

export function useChatSidebar() {
  const [state, dispatch] = useReducer(reducer, { status: "loading" });

  const load = useCallback(() => {
    let cancelled = false;
    dispatch({ type: "reload" });

    fetchSidebarData()
      .then(({ currentUserId, channels, dms, canCreateChannel }) => {
        if (!cancelled)
          dispatch({ type: "loaded", currentUserId, channels, dms, canCreateChannel });
      })
      .catch((err: unknown) => {
        if (!cancelled) {
          const message =
            err instanceof Error ? err.message : "Não foi possível carregar os dados.";
          dispatch({ type: "error", error: message });
        }
      });

    return () => {
      cancelled = true;
    };
  }, []);

  useEffect(() => {
    return load();
  }, [load]);

  return { state, retry: load };
}
