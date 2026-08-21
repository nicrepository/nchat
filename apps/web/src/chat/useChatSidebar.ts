import { useCallback, useEffect, useReducer, useRef } from "react";
import { useLocation, useNavigate } from "react-router";

import { showBrowserMessageNotification } from "./browserNotification";
import { fetchSidebarData, markConversationRead, setSidebarConversationPinned } from "./chatApi";
import { normalizeChatTargetId } from "./chatTargetId";
import type { Channel, ChannelCategory, ConversationActivity, DMConversation } from "./chatTypes";
import { playMessageSound } from "./messageSound";
import { laterActivity } from "./sidebarOrder";
import {
  loadPersistedUnread,
  savePersistedUnread,
  type PersistedUnreadEntry,
} from "./sidebarUnreadPersistence";
import { getSoundNotificationMode } from "./soundPreference";
import { classifySoundEvent, shouldPlayMessageSound } from "./soundRules";
import {
  useChatWebSocket,
  type WSMessageCreatedEvent,
  type WSSubscriptionTarget,
} from "./useChatWebSocket";

// ── State ────────────────────────────────────────────────────────────────────

export type SidebarState =
  | { status: "loading" }
  | { status: "error"; error: string }
  | {
      status: "ready";
      currentUserId: string;
      workspaceId: string;
      channels: Channel[];
      dms: DMConversation[];
      categories: ChannelCategory[];
    };

type Action =
  | {
      type: "loaded";
      currentUserId: string;
      workspaceId: string;
      channels: Channel[];
      dms: DMConversation[];
      categories: ChannelCategory[];
      /**
       * Unread/mention state restored from localStorage for this
       * (user, workspace) — only consulted when there is no in-memory
       * `previous` state yet (the very first load of the tab). Required
       * rather than optional so every dispatch site states its intent
       * explicitly; refreshSidebar() always passes [] since `previous` is
       * never undefined there.
       */
      persistedUnread: PersistedUnreadEntry[];
    }
  | { type: "error"; error: string }
  | { type: "reload" }
  | {
      type: "message_created";
      target: WSSubscriptionTarget;
      senderId: string;
      /** The message's own persisted creation instant, as the server stated it. */
      messageCreatedAt: string;
      activeTarget?: WSSubscriptionTarget;
      /** Whether this message names the current user (specific @mention or @all). */
      isMentioned: boolean;
    }
  | { type: "target_opened"; target: WSSubscriptionTarget }
  | { type: "pin_changed"; target: WSSubscriptionTarget; pinnedAt: string | null };

/**
 * Replaces the list with the server's, keeping each surviving item's activity
 * at the later of the two instants (issue #414).
 *
 * Membership comes from `incoming` and only from there. An item the server
 * stopped returning — access revoked, conversation archived — disappears, and
 * one it started returning appears; nothing is retained merely because it was
 * on screen a moment ago. What survives the swap is a single fact per surviving
 * item: how recently it was written in.
 *
 * That is what settles the refetch/event race without a request-generation
 * counter. A response computed before an event arrived reports the older
 * activity, and `laterActivity` keeps the newer one, so a slow response cannot
 * undo a conversation's promotion; a response computed after reports the same
 * or newer, and wins. Since activity never moves backwards in the database
 * (deletion is soft and leaves created_at intact), taking the maximum can only
 * ever agree with what is persisted.
 */
function mergeActivity<T extends ConversationActivity & { id: string }>(
  incoming: T[],
  previous: T[] | undefined,
): T[] {
  if (!previous?.length) return incoming;
  const known = new Map(previous.map((item) => [item.id, item.lastMessageAt]));
  return incoming.map((item) => {
    if (!known.has(item.id)) return item;
    const merged = laterActivity(item.lastMessageAt, known.get(item.id));
    return merged === item.lastMessageAt ? item : { ...item, lastMessageAt: merged };
  });
}

/**
 * Restores unread/mention state across a "loaded" dispatch — the reducer's
 * only reconciliation point, run on mount and on every refreshSidebar().
 *
 * The server's unreadCount is authoritative whenever present. In-memory and
 * persisted values only bridge the first response or a rolling deployment
 * where an older backend omits that field. Mention state remains client-only.
 * Membership still comes from `incoming` only, same as mergeActivity — this
 * never fabricates a row for a conversation the server did not return.
 */
function mergeUnread<T extends { id: string; unreadCount?: number; hasMentionUnread?: boolean }>(
  incoming: T[],
  previous: T[] | undefined,
  type: "channel" | "dm",
  persisted: PersistedUnreadEntry[],
): T[] {
  const known = new Map((previous ?? []).map((item) => [item.id, item]));
  const restored = new Map(
    persisted.filter((entry) => entry.type === type).map((entry) => [entry.id, entry]),
  );
  return incoming.map((item) => {
    const prev = known.get(item.id);
    const entry = restored.get(item.id);
    const unreadCount = item.unreadCount ?? prev?.unreadCount ?? entry?.unreadCount;
    const hasMentionUnread =
      prev?.hasMentionUnread ?? entry?.hasMentionUnread ?? item.hasMentionUnread;
    if (unreadCount === undefined && hasMentionUnread === undefined) return item;
    return { ...item, unreadCount, hasMentionUnread };
  });
}

/**
 * Moves one conversation's activity forward, and never backwards.
 *
 * Only the item the event names is touched, and only when it is already in the
 * list: an event for a conversation the sidebar does not have is not enough to
 * build a row from, because the server — not a broadcast — decides what this
 * user may see. `laterActivity` makes the update monotonic and idempotent, so
 * an event that arrives out of order cannot demote a conversation and the same
 * event applied twice is indistinguishable from once.
 */
function bumpActivity<T extends ConversationActivity & { id: string }>(
  items: T[],
  targetId: string,
  messageCreatedAt: string,
): T[] {
  return items.map((item) => {
    if (item.id !== targetId) return item;
    const merged = laterActivity(item.lastMessageAt, messageCreatedAt);
    return merged === item.lastMessageAt ? item : { ...item, lastMessageAt: merged };
  });
}

function reducer(state: SidebarState, action: Action): SidebarState {
  switch (action.type) {
    case "loaded": {
      const previous = state.status === "ready" ? state : undefined;
      const channels = mergeActivity(action.channels, previous?.channels);
      const dms = mergeActivity(action.dms, previous?.dms);
      return {
        status: "ready",
        currentUserId: action.currentUserId,
        workspaceId: action.workspaceId,
        channels: mergeUnread(channels, previous?.channels, "channel", action.persistedUnread),
        dms: mergeUnread(dms, previous?.dms, "dm", action.persistedUnread),
        categories: action.categories || [],
      };
    }
    case "error":
      return { status: "error", error: action.error };
    case "reload":
      return { status: "loading" };
    case "message_created": {
      if (state.status !== "ready") return state;
      // Activity is recorded for every persisted message, including the user's
      // own and including one in the conversation they are currently reading:
      // writing in a conversation is what makes it the most recently active one,
      // whoever wrote and wherever they are looking. Unread counting keeps its
      // own, narrower rule below — the two questions have different answers.
      const counts =
        action.senderId !== state.currentUserId &&
        !(
          action.activeTarget?.kind === action.target.kind &&
          action.activeTarget.targetId === action.target.targetId
        );
      const withUnread = <
        T extends { id: string; unreadCount?: number; hasMentionUnread?: boolean },
      >(
        items: T[],
      ): T[] =>
        counts
          ? items.map((item) =>
              item.id === action.target.targetId
                ? {
                    ...item,
                    unreadCount: (item.unreadCount ?? 0) + 1,
                    hasMentionUnread: item.hasMentionUnread || action.isMentioned,
                  }
                : item,
            )
          : items;

      if (action.target.kind === "channel") {
        return {
          ...state,
          channels: bumpActivity(
            withUnread(state.channels),
            action.target.targetId,
            action.messageCreatedAt,
          ),
        };
      }
      return {
        ...state,
        dms: bumpActivity(withUnread(state.dms), action.target.targetId, action.messageCreatedAt),
      };
    }
    case "target_opened": {
      if (state.status !== "ready") return state;
      if (action.target.kind === "channel") {
        return {
          ...state,
          channels: state.channels.map((channel) =>
            channel.id === action.target.targetId && channel.unreadCount
              ? { ...channel, unreadCount: 0, hasMentionUnread: false }
              : channel,
          ),
        };
      }
      return {
        ...state,
        dms: state.dms.map((dm) =>
          dm.id === action.target.targetId && dm.unreadCount
            ? { ...dm, unreadCount: 0, hasMentionUnread: false }
            : dm,
        ),
      };
    }
    case "pin_changed": {
      if (state.status !== "ready") return state;
      const update = <T extends { id: string; pinnedAt?: string | null }>(items: T[]): T[] =>
        items.map((item) =>
          item.id === action.target.targetId ? { ...item, pinnedAt: action.pinnedAt } : item,
        );
      return {
        ...state,
        channels: action.target.kind === "channel" ? update(state.channels) : state.channels,
        dms: action.target.kind === "dm" ? update(state.dms) : state.dms,
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
  const navigate = useNavigate();
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
      .then(({ currentUserId, workspaceId, channels, dms, categories }) => {
        if (mountedRef.current) {
          const persistedUnread =
            currentUserId && workspaceId ? loadPersistedUnread(currentUserId, workspaceId) : [];
          dispatch({
            type: "loaded",
            currentUserId,
            workspaceId,
            channels,
            dms,
            categories,
            persistedUnread,
          });
        }
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
   * Keeps the unread/mention cache in sync with every state change — new
   * message, opening a conversation, a reconciling refetch — so it is never
   * more than one render behind what's on screen. A pure side effect of
   * `state`: it never decides badge values itself, only mirrors them.
   */
  useEffect(() => {
    if (state.status !== "ready" || !state.currentUserId || !state.workspaceId) return;
    const entries: PersistedUnreadEntry[] = [
      ...state.channels.map((c) => ({
        id: c.id,
        type: "channel" as const,
        unreadCount: c.unreadCount ?? 0,
        hasMentionUnread: !!c.hasMentionUnread,
      })),
      ...state.dms.map((d) => ({
        id: d.id,
        type: "dm" as const,
        unreadCount: d.unreadCount ?? 0,
        hasMentionUnread: !!d.hasMentionUnread,
      })),
    ];
    savePersistedUnread(state.currentUserId, state.workspaceId, entries);
  }, [state]);

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
        .then(({ currentUserId, workspaceId, channels, dms, categories }) => {
          if (mountedRef.current)
            dispatch({
              type: "loaded",
              currentUserId,
              workspaceId,
              channels,
              dms,
              categories,
              // previous is always defined at this call site (refreshSidebar
              // only ever runs once the sidebar is already "ready"), so
              // mergeUnread never consults this — never worth a localStorage
              // read here.
              persistedUnread: [],
            });
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
      // The message's own created_at, assigned when the row was written — not
      // the envelope's created_at (when the event was published), not the moment
      // it arrived here, and never a browser clock. It is absent on route-only
      // events, which carry no payload: those say "something happened" without
      // saying when, so the sidebar asks the server instead of guessing. The
      // refetch is the same coalescing one membership changes use, so a burst of
      // such events costs one refetch, not one per event.
      const messageCreatedAt = event.payload?.created_at;
      if (!messageCreatedAt) {
        refreshSidebar();
        return;
      }
      // Same "is this relevant to the reader" question the reducer's `counts`
      // (message_created case above) answers for unread badges, recomputed
      // here rather than threaded out of the reducer: a reducer performs
      // state transitions, not side effects like audio playback, and this
      // callback already has senderId, currentUserId and the active target
      // in scope for the very same event. The classification is also reused
      // for the reducer's `isMentioned` flag below, so it only runs once.
      let isMentioned = false;
      if (state.status === "ready" && event.payload) {
        const payload = event.payload;
        const isOwnMessage = (payload.sender_id ?? "") === state.currentUserId;
        const isActiveConversation =
          openedTarget?.kind === event.target_type && openedTarget.targetId === event.target_id;
        const classified = classifySoundEvent(payload, event.target_type, state.currentUserId);
        isMentioned = classified.isMentioned;
        const isWindowFocused = document.visibilityState === "visible";
        const play = shouldPlayMessageSound({
          mode: getSoundNotificationMode(),
          // Already past the seenRealtimeMessageIds check above — this event
          // is guaranteed fresh by the time it reaches this decision.
          isDuplicate: false,
          isOwnMessage,
          category: classified.category,
          isMentioned: classified.isMentioned,
          isActiveConversation,
          isWindowFocused,
        });
        if (play) {
          // The native notification and the chime are alternate channels for
          // the same eligible event, never both: a native one shown
          // successfully replaces the chime; anything else (denied/default/
          // unsupported permission, background API failure, or the tab being
          // in the foreground — native notifications never fire there) falls
          // through to the existing sound path unchanged.
          let shown = false;
          if (!isWindowFocused) {
            try {
              shown = showBrowserMessageNotification({
                targetKind: event.target_type,
                targetId: event.target_id,
                senderDisplayName: payload.sender_display_name,
                bodyText: payload.body_text,
                onNavigate: (path) => {
                  navigate(path);
                  refreshSidebar();
                },
              }).shown;
            } catch {
              // The module already guards itself; this is defense in depth —
              // the WS callback must never break because of it.
            }
          }
          if (!shown) {
            // playMessageSound() already never throws, but the unread badge
            // must update even if that guarantee is ever violated — a failed
            // chime is never allowed to break message receipt.
            try {
              playMessageSound();
            } catch {
              // Swallowed on purpose: see above.
            }
          }
        }
      }
      dispatch({
        type: "message_created",
        target: { kind: event.target_type, targetId: event.target_id },
        senderId: event.payload?.sender_id ?? "",
        messageCreatedAt,
        activeTarget: openedTarget,
        isMentioned,
      });
    },
  });

  useEffect(() => {
    if (state.status === "ready" && openedTargetKind && openedTargetId) {
      dispatch({
        type: "target_opened",
        target: { kind: openedTargetKind, targetId: openedTargetId },
      });
      void Promise.resolve(markConversationRead(openedTargetKind, openedTargetId)).catch(() => {
        // Local UI remains responsive while a later refresh reconciles with
        // the server. A failed read receipt must never break chat navigation.
      });
    }
  }, [openedTargetKind, openedTargetId, state.status]);

  const setPinned = useCallback(
    async (target: WSSubscriptionTarget, pinned: boolean) => {
      if (state.status !== "ready") return;
      const items = target.kind === "channel" ? state.channels : state.dms;
      const previous = items.find((item) => item.id === target.targetId)?.pinnedAt ?? null;
      const optimistic = pinned ? "0001-01-01T00:00:00Z" : null;
      dispatch({ type: "pin_changed", target, pinnedAt: optimistic });
      try {
        await setSidebarConversationPinned(target.kind, target.targetId, pinned);
        refreshSidebar();
      } catch (error) {
        dispatch({ type: "pin_changed", target, pinnedAt: previous });
        throw error;
      }
    },
    [refreshSidebar, state],
  );

  return { state, retry: load, setPinned };
}
