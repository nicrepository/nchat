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

  const load = useCallback(() => {
    let cancelled = false;
    dispatch({ type: "reload" });

    fetchSidebarData()
      .then(({ currentUserId, channels, dms }) => {
        if (!cancelled) dispatch({ type: "loaded", currentUserId, channels, dms });
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
