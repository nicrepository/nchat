import { useCallback, useEffect, useReducer, useRef } from "react";
import { useLocation, useNavigate } from "react-router";

import { showBrowserMessageNotification } from "./browserNotification";
import {
  fetchSidebarData,
  leaveConversation as leaveConversationRequest,
  markConversationRead,
  renameChannel as renameChannelRequest,
  renameGroup as renameGroupRequest,
  setConversationMuted,
  setSidebarConversationPinned,
} from "./chatApi";
import type { WorkspaceAttachmentLimits } from "./chatApi";
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
      attachmentLimits?: WorkspaceAttachmentLimits;
      channels: Channel[];
      dms: DMConversation[];
      categories: ChannelCategory[];
    };

type Action =
  | {
      type: "loaded";
      currentUserId: string;
      workspaceId: string;
      attachmentLimits: WorkspaceAttachmentLimits;
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
  | { type: "pin_changed"; target: WSSubscriptionTarget; pinnedAt: string | null }
  | { type: "mute_changed"; target: WSSubscriptionTarget; muted: boolean };

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

/**
 * Applies one per-conversation preference to whichever list owns the target.
 *
 * Pin and mute are the same operation on two different fields — find the row by
 * id, replace one value, leave every other row untouched — so they share this
 * rather than each carrying a copy of the branch. Both are private to the
 * viewer, which is why neither ever needs to touch anything but the named row.
 *
 * Membership is never invented here: an id the list does not hold changes
 * nothing, because what this user may see is the server's decision and not an
 * event's.
 */
function applyPreference(
  state: SidebarState,
  target: WSSubscriptionTarget,
  change: { muted?: boolean; pinnedAt?: string | null },
): SidebarState {
  if (state.status !== "ready") return state;
  const update = <T extends { id: string }>(items: T[]): T[] =>
    items.map((item) => (item.id === target.targetId ? { ...item, ...change } : item));
  return {
    ...state,
    channels: target.kind === "channel" ? update(state.channels) : state.channels,
    dms: target.kind === "dm" ? update(state.dms) : state.dms,
  };
}

/**
 * Rebuilds the ready state from a server response, carrying forward the two
 * things the response does not carry: how recently each surviving conversation
 * was written in, and the viewer's unread/mention state.
 */
function applyLoaded(
  state: SidebarState,
  action: Extract<Action, { type: "loaded" }>,
): SidebarState {
  const previous = state.status === "ready" ? state : undefined;
  const channels = mergeActivity(action.channels, previous?.channels);
  const dms = mergeActivity(action.dms, previous?.dms);
  return {
    status: "ready",
    currentUserId: action.currentUserId,
    workspaceId: action.workspaceId,
    attachmentLimits: action.attachmentLimits,
    channels: mergeUnread(channels, previous?.channels, "channel", action.persistedUnread),
    dms: mergeUnread(dms, previous?.dms, "dm", action.persistedUnread),
    categories: action.categories || [],
  };
}

/**
 * Whether this message adds to an unread count.
 *
 * Narrower than the rule for activity: the user's own message is not unread to
 * them, and neither is one arriving in the conversation they are currently
 * reading. Activity keeps its own, broader rule — writing in a conversation is
 * what makes it the most recently active one, whoever wrote and wherever they
 * are looking — because the two questions have different answers.
 */
function countsAsUnread(
  state: Extract<SidebarState, { status: "ready" }>,
  action: Extract<Action, { type: "message_created" }>,
): boolean {
  if (action.senderId === state.currentUserId) return false;
  return !(
    action.activeTarget?.kind === action.target.kind &&
    action.activeTarget.targetId === action.target.targetId
  );
}

function incrementUnread<
  T extends { id: string; unreadCount?: number; hasMentionUnread?: boolean },
>(items: T[], targetId: string, isMentioned: boolean): T[] {
  return items.map((item) =>
    item.id === targetId
      ? {
          ...item,
          unreadCount: (item.unreadCount ?? 0) + 1,
          hasMentionUnread: item.hasMentionUnread || isMentioned,
        }
      : item,
  );
}

/** Opening a conversation is what marks it read; a row with nothing unread is left alone. */
function clearUnread<T extends { id: string; unreadCount?: number; hasMentionUnread?: boolean }>(
  items: T[],
  targetId: string,
): T[] {
  return items.map((item) =>
    item.id === targetId && item.unreadCount
      ? { ...item, unreadCount: 0, hasMentionUnread: false }
      : item,
  );
}

function applyMessageCreated(
  state: SidebarState,
  action: Extract<Action, { type: "message_created" }>,
): SidebarState {
  if (state.status !== "ready") return state;
  const counts = countsAsUnread(state, action);
  const { targetId } = action.target;
  if (action.target.kind === "channel") {
    const channels = counts
      ? incrementUnread(state.channels, targetId, action.isMentioned)
      : state.channels;
    return { ...state, channels: bumpActivity(channels, targetId, action.messageCreatedAt) };
  }
  const dms = counts ? incrementUnread(state.dms, targetId, action.isMentioned) : state.dms;
  return { ...state, dms: bumpActivity(dms, targetId, action.messageCreatedAt) };
}

function applyTargetOpened(
  state: SidebarState,
  action: Extract<Action, { type: "target_opened" }>,
): SidebarState {
  if (state.status !== "ready") return state;
  const { targetId } = action.target;
  if (action.target.kind === "channel") {
    return { ...state, channels: clearUnread(state.channels, targetId) };
  }
  return { ...state, dms: clearUnread(state.dms, targetId) };
}

// Routing only: each case names the transition and hands the state to the pure
// function that performs it. Every one of those functions is exhaustive about
// its own case, so what the switch shows is the set of transitions that exist.
function reducer(state: SidebarState, action: Action): SidebarState {
  switch (action.type) {
    case "loaded":
      return applyLoaded(state, action);
    case "error":
      return { status: "error", error: action.error };
    case "reload":
      return { status: "loading" };
    case "message_created":
      return applyMessageCreated(state, action);
    case "target_opened":
      return applyTargetOpened(state, action);
    case "mute_changed":
      return applyPreference(state, action.target, { muted: action.muted });
    case "pin_changed":
      return applyPreference(state, action.target, { pinnedAt: action.pinnedAt });
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

/**
 * Announces one freshly received message to the reader and reports whether it
 * mentions them.
 *
 * Two things at once because they answer the same question from the same
 * classification: "is this relevant to the reader" decides the chime, and its
 * mention half decides the unread badge's dot. It is computed here rather than
 * threaded out of the reducer because a reducer performs state transitions, not
 * side effects like audio playback — and running the classification twice for
 * one event is how the two answers eventually disagree.
 */
function announceMessage(
  event: WSMessageCreatedEvent,
  currentUserId: string,
  activeTarget: WSSubscriptionTarget | undefined,
  onNavigate: (path: string) => void,
): boolean {
  const payload = event.payload;
  if (!payload) return false;
  const classified = classifySoundEvent(payload, event.target_type, currentUserId);
  const isWindowFocused = document.visibilityState === "visible";
  const play = shouldPlayMessageSound({
    mode: getSoundNotificationMode(),
    // Already past the seenRealtimeMessageIds check at the call site — this
    // event is guaranteed fresh by the time it reaches this decision.
    isDuplicate: false,
    isOwnMessage: (payload.sender_id ?? "") === currentUserId,
    category: classified.category,
    isMentioned: classified.isMentioned,
    isActiveConversation:
      activeTarget?.kind === event.target_type && activeTarget.targetId === event.target_id,
    isWindowFocused,
  });
  if (play) notifyOrChime(event, payload, isWindowFocused, onNavigate);
  return classified.isMentioned;
}

/**
 * The native notification and the chime are alternate channels for the same
 * eligible event, never both: a native one shown successfully replaces the
 * chime; anything else (denied/default/unsupported permission, a background API
 * failure, or the tab being in the foreground — native notifications never fire
 * there) falls through to the sound path.
 */
function notifyOrChime(
  event: WSMessageCreatedEvent,
  payload: NonNullable<WSMessageCreatedEvent["payload"]>,
  isWindowFocused: boolean,
  onNavigate: (path: string) => void,
): void {
  let shown = false;
  if (!isWindowFocused) {
    try {
      shown = showBrowserMessageNotification({
        targetKind: event.target_type,
        targetId: event.target_id,
        senderDisplayName: payload.sender_display_name,
        bodyText: payload.body_text,
        onNavigate,
      }).shown;
    } catch {
      // The module already guards itself; this is defense in depth — the WS
      // callback must never break because of it.
    }
  }
  if (shown) return;
  // playMessageSound() already never throws, but the unread badge must update
  // even if that guarantee is ever violated — a failed chime is never allowed to
  // break message receipt.
  try {
    playMessageSound();
  } catch {
    // Swallowed on purpose: see above.
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
      .then(
        ({
          currentUserId,
          workspaceId,
          maxUploadBytes,
          maxFiles,
          maxBytes,
          channels,
          dms,
          categories,
        }) => {
          if (mountedRef.current) {
            const persistedUnread =
              currentUserId && workspaceId ? loadPersistedUnread(currentUserId, workspaceId) : [];
            dispatch({
              type: "loaded",
              currentUserId,
              workspaceId,
              attachmentLimits: {
                maxUploadBytes: maxUploadBytes ?? null,
                maxFiles: maxFiles ?? 1,
                maxBytes: maxBytes ?? Number.MAX_SAFE_INTEGER,
              },
              channels,
              dms,
              categories,
              persistedUnread,
            });
          }
        },
      )
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
        .then(
          ({
            currentUserId,
            workspaceId,
            maxUploadBytes,
            maxFiles,
            maxBytes,
            channels,
            dms,
            categories,
          }) => {
            if (mountedRef.current)
              dispatch({
                type: "loaded",
                currentUserId,
                workspaceId,
                attachmentLimits: {
                  maxUploadBytes: maxUploadBytes ?? null,
                  maxFiles: maxFiles ?? 1,
                  maxBytes: maxBytes ?? Number.MAX_SAFE_INTEGER,
                },
                channels,
                dms,
                categories,
                // previous is always defined at this call site (refreshSidebar
                // only ever runs once the sidebar is already "ready"), so
                // mergeUnread never consults this — never worth a localStorage
                // read here.
                persistedUnread: [],
              });
          },
        )
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
    // A conversation was renamed somewhere else (issue #527). The event names
    // the target and nothing else, so the only correct response is the same
    // coalescing refetch membership changes use: the server re-derives what this
    // user may see, and the row keeps its identity, its pin, its mute state and
    // its unread badge because the reducer replaces items by id.
    onConversationUpdated: refreshSidebar,
    // A system message landed (a rename, someone leaving). The sidebar shows no
    // message content, but a departure changes what this user may see, so the
    // same refetch settles it — and it is coalesced, so a burst costs one.
    onConversationEvent: refreshSidebar,
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
      const isMentioned =
        state.status === "ready" &&
        announceMessage(event, state.currentUserId, openedTarget, (path) => {
          navigate(path);
          refreshSidebar();
        });
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

  /**
   * Marks a conversation read without opening it (issue #527).
   *
   * Deliberately the same pair the navigation effect above performs — the local
   * `target_opened` transition plus the server receipt — rather than a second
   * rule: "this conversation has no unread messages" has one meaning, and a
   * menu action must not invent another. Nothing here navigates, changes the
   * selection or touches the composer.
   *
   * The receipt is fire-and-forget for the same reason it is on navigation: a
   * failed write leaves the badge cleared locally until the next refetch
   * reconciles it, and must never break the sidebar.
   */
  const markRead = useCallback((target: WSSubscriptionTarget) => {
    dispatch({ type: "target_opened", target });
    void Promise.resolve(markConversationRead(target.kind, target.targetId)).catch(() => {
      // See above: a failed read receipt is not a UI failure.
    });
  }, []);

  /**
   * Renames a channel and converges every surface that renders its name.
   *
   * No optimistic write: unlike a pin — a private preference this user owns —
   * a rename is a workspace-wide change the server may refuse, and showing a
   * name that was never persisted is exactly the divergence issue #527 forbids.
   * The refetch after a confirmed 200 is what updates the sidebar, the open
   * conversation's header and the outlet context, from the one canonical
   * source. The error is re-thrown so the dialog can stay open and actionable.
   */
  const renameChannel = useCallback(
    async (channelId: string, displayName: string) => {
      await renameChannelRequest(channelId, displayName);
      refreshSidebar();
    },
    [refreshSidebar],
  );

  /**
   * Silences or restores one conversation for this user only (issue #527).
   *
   * Optimistic like the pin, and for the same reason: it is a private
   * preference the server either accepts or refuses outright, so showing the
   * new state immediately and rolling back on failure is honest. The refetch
   * after a confirmed write reconciles with what was actually persisted.
   */
  const setMuted = useCallback(
    async (target: WSSubscriptionTarget, muted: boolean) => {
      if (state.status !== "ready") return;
      const items = target.kind === "channel" ? state.channels : state.dms;
      const previous = Boolean(items.find((item) => item.id === target.targetId)?.muted);
      dispatch({ type: "mute_changed", target, muted });
      try {
        await setConversationMuted(target.kind, target.targetId, muted);
      } catch (error) {
        dispatch({ type: "mute_changed", target, muted: previous });
        throw error;
      }
    },
    [state],
  );

  /**
   * Renames a group. Same no-optimism rule the channel rename follows: a name
   * the server never accepted must not appear, so the refetch after a confirmed
   * 200 is what updates every surface.
   */
  const renameGroup = useCallback(
    async (conversationId: string, title: string) => {
      await renameGroupRequest(conversationId, title);
      refreshSidebar();
    },
    [refreshSidebar],
  );

  /**
   * Removes this user from a channel or group and drops it from the sidebar.
   *
   * The refetch is the removal: membership is the server's to decide, so the row
   * disappears because the canonical list stopped returning it, never because
   * the client deleted it locally. That also unsubscribes it from realtime —
   * the socket's target list is derived from the same state — without touching
   * the connection itself.
   *
   * Leaving the conversation you are *reading* additionally has to move you off
   * it. Staying would leave the route pointing at something this user can no
   * longer see: the message area would keep asking for its history, the details
   * panel would keep polling, and the composer would still be aimed at it. The
   * navigation happens only after the request resolved — never optimistically —
   * so a refusal leaves the reader exactly where they were.
   *
   * The fallback is the chat's own base route, which is the neutral state this
   * product already renders when nothing is selected. There is no "next
   * conversation" convention in this sidebar to follow, and inventing one here
   * would be a product decision disguised as error handling.
   */
  const leaveConversation = useCallback(
    async (target: WSSubscriptionTarget) => {
      const wasReading = openedTargetKind === target.kind && openedTargetId === target.targetId;
      await leaveConversationRequest(target.kind, target.targetId);
      refreshSidebar();
      if (wasReading) navigate("/chat");
    },
    [refreshSidebar, navigate, openedTargetKind, openedTargetId],
  );

  return {
    state,
    retry: load,
    setPinned,
    markRead,
    renameChannel,
    renameGroup,
    setMuted,
    leaveConversation,
  };
}
