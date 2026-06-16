import { useCallback, useEffect, useReducer } from "react";

import { fetchSidebarData } from "./chatApi";
import type { Channel, DMConversation } from "./chatTypes";

// ── State ────────────────────────────────────────────────────────────────────

type SidebarState =
  | { status: "loading" }
  | { status: "error"; error: string }
  | { status: "ready"; channels: Channel[]; dms: DMConversation[] };

type Action =
  | { type: "loaded"; channels: Channel[]; dms: DMConversation[] }
  | { type: "error"; error: string }
  | { type: "reload" };

function reducer(_state: SidebarState, action: Action): SidebarState {
  switch (action.type) {
    case "loaded":
      return { status: "ready", channels: action.channels, dms: action.dms };
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
      .then(({ channels, dms }) => {
        if (!cancelled) dispatch({ type: "loaded", channels, dms });
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
