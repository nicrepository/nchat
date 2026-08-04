import { useCallback, useEffect, useReducer, useRef } from "react";
import { useLocation } from "react-router";

import { fetchSidebarData } from "./chatApi";
import { normalizeChatTargetId } from "./chatTargetId";
import type { Channel, DMConversation } from "./chatTypes";
import {
  useChatWebSocket,
  type WSMessageCreatedEvent,
  type WSSubscriptionTarget,
} from "./useChatWebSocket";

// ── State ────────────────────────────────────────────────────────────────────

type SidebarState =
  | { status: "loading" }
  | { status: "error"; error: string }
  | {
      status: "ready";
      currentUserId: string;
      channels: Channel[];
      dms: DMConversation[];
    };

type Action =
  | {
      type: "loaded";
      currentUserId: string;
      channels: Channel[];
      dms: DMConversation[];
    }
  | { type: "error"; error: string }
  | { type: "reload" }
  | {
      type: "message_created";
      target: WSSubscriptionTarget;
      senderId: string;
      activeTarget?: WSSubscriptionTarget;
    }
  | { type: "target_opened"; target: WSSubscriptionTarget };

function reducer(state: SidebarState, action: Action): SidebarState {
  switch (action.type) {
    case "loaded":
      return {
        status: "ready",
        currentUserId: action.currentUserId,
        channels: action.channels,
        dms: action.dms,
      };
    case "error":
      return { status: "error", error: action.error };
    case "reload":
      return { status: "loading" };
    case "message_created": {
      if (
        state.status !== "ready" ||
        action.senderId === state.currentUserId ||
        (action.activeTarget?.kind === action.target.kind &&
          action.activeTarget.targetId === action.target.targetId)
      ) {
        return state;
      }
      if (action.target.kind === "channel") {
        return {
          ...state,
          channels: state.channels.map((channel) =>
            channel.id === action.target.targetId
              ? { ...channel, unreadCount: (channel.unreadCount ?? 0) + 1 }
              : channel,
          ),
        };
      }
      return {
        ...state,
        dms: state.dms.map((dm) =>
          dm.id === action.target.targetId ? { ...dm, unreadCount: (dm.unreadCount ?? 0) + 1 } : dm,
        ),
      };
    }
    case "target_opened": {
      if (state.status !== "ready") return state;
      if (action.target.kind === "channel") {
        return {
          ...state,
          channels: state.channels.map((channel) =>
            channel.id === action.target.targetId && channel.unreadCount
              ? { ...channel, unreadCount: 0 }
              : channel,
          ),
        };
      }
      return {
        ...state,
        dms: state.dms.map((dm) =>
          dm.id === action.target.targetId && dm.unreadCount ? { ...dm, unreadCount: 0 } : dm,
        ),
      };
    }
  }
}

function targetFromPath(pathname: string): WSSubscriptionTarget | undefined {
  const match = /^\/chat\/(channel|dm)\/([^/]+)$/.exec(pathname);
  if (!match?.[1] || !match[2]) return undefined;
  try {
    const targetId = normalizeChatTargetId(decodeURIComponent(match[2]));
    return targetId ? { kind: match[1] as "channel" | "dm", targetId } : undefined;
  } catch {
    return undefined;
  }
}

// ── Hook ─────────────────────────────────────────────────────────────────────

export function useChatSidebar() {
  const [state, dispatch] = useReducer(reducer, { status: "loading" });
  const { pathname } = useLocation();
  const openedTarget = targetFromPath(pathname);
  const openedTargetKind = openedTarget?.kind;
  const openedTargetId = openedTarget?.targetId;
  const seenRealtimeMessageIds = useRef(new Set<string>());
  const mountedRef = useRef(true);
  const loadPromiseRef = useRef<Promise<void> | null>(null);

  const load = useCallback(() => {
    if (loadPromiseRef.current) return loadPromiseRef.current;
    dispatch({ type: "reload" });

    const loading = fetchSidebarData()
      .then(({ currentUserId, channels, dms }) => {
        if (mountedRef.current) dispatch({ type: "loaded", currentUserId, channels, dms });
      })
      .catch((err: unknown) => {
        if (mountedRef.current) {
          const message =
            err instanceof Error ? err.message : "Não foi possível carregar os dados.";
          dispatch({ type: "error", error: message });
        }
      })
      .finally(() => {
        if (loadPromiseRef.current === loading) loadPromiseRef.current = null;
      });
    loadPromiseRef.current = loading;
    return loading;
  }, []);

  useEffect(() => {
    mountedRef.current = true;
    void load();
    return () => {
      mountedRef.current = false;
    };
  }, [load]);

  /**
   * Refetches the sidebar in place after a membership change (issue #398).
   *
   * Distinct from `load`, which resets to the loading state: that is right for
   * a first mount and wrong here, because blanking a populated sidebar to add
   * one conversation loses the rendered list, the unread counts and the
   * selection for a frame. This replaces the data when it arrives instead.
   *
   * `fetchSidebarData` is the authoritative source and re-derives membership
   * server-side, so the event is only a hint: a signal for a conversation the
   * user cannot actually read adds nothing.
   *
   * Coalescing: a burst (two people added in quick succession, or several
   * sessions of the same user) must not start several overlapping refetches. A
   * request already in flight sets a "do it again when done" flag rather than
   * starting a second one, so no event is lost and at most two run in sequence.
   */
  const refreshInFlight = useRef(false);
  const refreshQueued = useRef(false);

  const refreshSidebar = useCallback(() => {
    if (refreshInFlight.current) {
      refreshQueued.current = true;
      return;
    }
    refreshInFlight.current = true;
    const run = () => {
      fetchSidebarData()
        .then(({ currentUserId, channels, dms }) => {
          if (mountedRef.current) dispatch({ type: "loaded", currentUserId, channels, dms });
        })
        .catch(() => {
          // The sidebar on screen stays valid; the next event or navigation
          // retries. Deliberately no error state and no retry loop — a failed
          // hint must not blank a working sidebar.
        })
        .finally(() => {
          if (refreshQueued.current && mountedRef.current) {
            refreshQueued.current = false;
            run();
            return;
          }
          refreshInFlight.current = false;
        });
    };
    run();
  }, []);

  const realtimeTargets: WSSubscriptionTarget[] =
    state.status === "ready"
      ? [
          ...state.channels.map(({ id }) => ({ kind: "channel" as const, targetId: id })),
          ...state.dms.map(({ id }) => ({ kind: "dm" as const, targetId: id })),
        ]
      : [];
  const primaryTarget = realtimeTargets[0];

  useChatWebSocket({
    kind: primaryTarget?.kind ?? "channel",
    targetId: primaryTarget?.targetId ?? "",
    additionalTargets: realtimeTargets.slice(1),
    // A conversation the user was just added to. They are not subscribed to it,
    // so this is the only way they hear about it before a reload.
    onConversationAvailable: refreshSidebar,
    onMessageCreated: (event: WSMessageCreatedEvent) => {
      if (seenRealtimeMessageIds.current.has(event.message_id)) return;
      seenRealtimeMessageIds.current.add(event.message_id);
      dispatch({
        type: "message_created",
        target: { kind: event.target_type, targetId: event.target_id },
        senderId: event.payload?.sender_id ?? "",
        activeTarget: openedTarget,
      });
    },
  });

  useEffect(() => {
    if (openedTargetKind && openedTargetId) {
      dispatch({
        type: "target_opened",
        target: { kind: openedTargetKind, targetId: openedTargetId },
      });
    }
  }, [openedTargetKind, openedTargetId, state.status]);

  return { state, retry: load };
}
