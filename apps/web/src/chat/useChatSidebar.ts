import { invalidateConversationDetails } from "./detailsInvalidation";
import { useCallback, useEffect, useReducer, useRef, useState } from "react";
import { useLocation, useNavigate } from "react-router";

import { ApiRequestError } from "../lib/api";
import { isAuthenticated, onAuthChange } from "../lib/authSession";

import {
  fetchSidebarData,
  leaveConversation as leaveConversationRequest,
  markConversationRead,
  renameChannel as renameChannelRequest,
  renameGroup as renameGroupRequest,
  setConversationMuted,
  setConversationNotificationMode,
  setSidebarConversationPinned,
} from "./chatApi";
import type { WorkspaceAttachmentLimits } from "./chatApi";
import { normalizeChatTargetId } from "./chatTargetId";
import {
  normalizeMessagePriority,
  type Channel,
  type ChannelCategory,
  type ConversationActivity,
  type ConversationNotificationLevel,
  type ConversationNotificationMode,
  type ConversationReadState,
  type DMConversation,
} from "./chatTypes";
import { laterActivity } from "./sidebarOrder";
import type { ReadProgress } from "./readCursor";
import {
  createReadCursorWriter,
  type ReadCursorWriter,
  type ReadTarget,
  type ReadWriteOutcome,
  type ReadWriteSender,
} from "./readCursorWriter";
import {
  acceptServerRead,
  acceptSidebarAnswer,
  applyArrival,
  applyMarkAllRequested,
  applyReadProgress,
  rememberRead,
  type AnswerProvenance,
  type ReadRow,
} from "./sidebarReadState";
import {
  loadPersistedUnread,
  savePersistedUnread,
  type PersistedUnreadEntry,
} from "./sidebarUnreadPersistence";
import type { InAppAlert } from "./InAppMessageAlert";
import {
  presentLiveMessageNotification,
  type MessageNotificationEvent,
  type MessagePresentationSinks,
} from "./notificationPresentation";
import { admitRealtimeMessage } from "./realtimeMessageLedger";
import { isNamedRecipient } from "./soundRules";
import {
  useChatWebSocket,
  type WSMessageCreatedEvent,
  type WSSubscriptionTarget,
} from "./useChatWebSocket";

// ── State ────────────────────────────────────────────────────────────────────

/** #1082: a write's answer, and when — on the session's request clock — it was sent. */
type TimedReadState = ConversationReadState & { startedAt: number };

export type SidebarState =
  | { status: "loading" }
  | { status: "error"; error: string }
  | {
      status: "ready";
      currentUserId: string;
      workspaceId: string;
      attachmentLimits?: WorkspaceAttachmentLimits;
      /**
       * Whether this deployment has opened the issue #136 rollout gate, as the
       * server reported it in the same payload that hydrated this state.
       *
       * The settings page reads it to decide which control to render: the
       * binary switch every build has always had, or the three-mode select.
       * It is an affordance and never the control — the server re-derives the
       * same answer on every write.
       *
       * Optional for the same reason `attachmentLimits` is: a state assembled
       * without it — a fixture, or a build whose payload predates the field —
       * reads as "off", which is the compatible control rather than an offer
       * the server would refuse.
       */
      notificationLevelsEnabled?: boolean;
      /**
       * Issue #1082: whether this server implements the precise read cursor,
       * as the payload that hydrated this state says. Reading is persisted
       * progressively only when it does.
       */
      preciseReadCursor?: boolean;
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
      notificationLevelsEnabled: boolean;
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
      preciseReadCursor: boolean;
      /** #1082: when, on the session's request clock, this fetch started and landed. */
      provenance: AnswerProvenance;
    }
  | { type: "error"; error: string }
  | { type: "reload" }
  | {
      type: "message_created";
      target: WSSubscriptionTarget;
      senderId: string;
      messageId: string;
      /** The message's own persisted creation instant, as the server stated it. */
      messageCreatedAt: string;
      /** Whether this message names the current user (specific @mention or @all). */
      isMentioned: boolean;
      /** #1082: the request-clock tick at which this client learned of the message. */
      at: number;
    }
  | { type: "read_progress"; target: WSSubscriptionTarget; progress: ReadProgress }
  | { type: "marked_all_read"; target: WSSubscriptionTarget; at: number }
  | {
      type: "read_confirmed";
      target: WSSubscriptionTarget;
      outcome: ReadWriteOutcome<TimedReadState>;
      receivedAt: number;
    }
  | { type: "pin_changed"; target: WSSubscriptionTarget; pinnedAt: string | null }
  | {
      type: "preference_changed";
      target: WSSubscriptionTarget;
      change: ConversationPreferenceChange;
    };

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
 * Each row is what sidebarReadState makes of the server's answer and what this
 * session remembers about it (#1082). Membership still comes from `incoming`
 * only, same as mergeActivity — this never fabricates a row for a conversation
 * the server did not return.
 */
function mergeUnread<T extends ReadRow>(
  incoming: T[],
  previous: T[] | undefined,
  type: "channel" | "dm",
  action: Extract<Action, { type: "loaded" }>,
): T[] {
  const known = new Map((previous ?? []).map((item) => [item.id, item]));
  const restored = new Map(
    action.persistedUnread.filter((entry) => entry.type === type).map((entry) => [entry.id, entry]),
  );
  return incoming.map((item) =>
    acceptSidebarAnswer(
      rememberRead(item, known.get(item.id), restored.get(item.id)),
      {
        readState: item.readState,
        unreadCount: item.unreadCount,
        precise: action.preciseReadCursor,
      },
      action.provenance,
    ),
  );
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
/**
 * One optimistic change to a conversation's notification preference.
 *
 * The two fields are optional and independent, which is the invariant of issue
 * #136 expressed in the client's own state: silencing sets `muted` and says
 * nothing about the level, so turning notifications back on shows the level
 * that was already there rather than a guess. A change that sets only `muted`
 * is *how* the sidebar shortcut stays non-destructive.
 */
interface ConversationPreferenceChange {
  muted?: boolean;
  notificationLevel?: ConversationNotificationLevel;
}

function applyPreference(
  state: SidebarState,
  target: WSSubscriptionTarget,
  change: ConversationPreferenceChange | { pinnedAt: string | null },
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
 * Replaces the one row `target` names with `update(row)`, and returns the same
 * state object when the update changed nothing — so a repeated report costs no
 * render anywhere.
 */
function updateReadRow(
  state: Extract<SidebarState, { status: "ready" }>,
  target: WSSubscriptionTarget,
  update: <T extends ReadRow>(row: T) => T,
): SidebarState {
  const apply = <T extends ReadRow>(items: T[]): T[] => {
    const index = items.findIndex((item) => item.id === target.targetId);
    if (index < 0) return items;
    const updated = update(items[index]);
    if (updated === items[index]) return items;
    return items.map((item, i) => (i === index ? updated : item));
  };
  if (target.kind === "channel") {
    const channels = apply(state.channels);
    return channels === state.channels ? state : { ...state, channels };
  }
  const dms = apply(state.dms);
  return dms === state.dms ? state : { ...state, dms };
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
  // Read state belongs to the user and workspace that produced it (#1082): a
  // payload for anyone else is a first load, never merged into theirs.
  const previous =
    state.status === "ready" &&
    state.currentUserId === action.currentUserId &&
    state.workspaceId === action.workspaceId
      ? state
      : undefined;
  const channels = mergeActivity(action.channels, previous?.channels);
  const dms = mergeActivity(action.dms, previous?.dms);
  return {
    status: "ready",
    currentUserId: action.currentUserId,
    workspaceId: action.workspaceId,
    attachmentLimits: action.attachmentLimits,
    notificationLevelsEnabled: action.notificationLevelsEnabled,
    preciseReadCursor: action.preciseReadCursor,
    channels: mergeUnread(channels, previous?.channels, "channel", action),
    dms: mergeUnread(dms, previous?.dms, "dm", action),
    categories: action.categories || [],
  };
}

function applyMessageCreated(
  state: SidebarState,
  action: Extract<Action, { type: "message_created" }>,
): SidebarState {
  if (state.status !== "ready") return state;
  const eligible = action.senderId !== state.currentUserId;
  const kind = {
    eligible,
    counts: eligible,
    isMention: action.isMentioned && eligible,
  };
  const message = { id: action.messageId, createdAt: action.messageCreatedAt };
  const arrived = updateReadRow(state, action.target, (row) =>
    applyArrival(row, kind, message, action.at),
  );
  if (arrived.status !== "ready") return arrived;
  const { targetId } = action.target;
  if (action.target.kind === "channel") {
    return {
      ...arrived,
      channels: bumpActivity(arrived.channels, targetId, action.messageCreatedAt),
    };
  }
  return { ...arrived, dms: bumpActivity(arrived.dms, targetId, action.messageCreatedAt) };
}

/**
 * The read state of one row moved (#1082): the open timeline reported
 * progress, the reader marked everything read, or a write came back with the
 * server's answer. Only the named row is touched.
 */
function applyReadTransition(
  state: SidebarState,
  action: Extract<Action, { type: "read_progress" | "marked_all_read" | "read_confirmed" }>,
): SidebarState {
  if (state.status !== "ready") return state;
  return updateReadRow(state, action.target, (row) => {
    if (action.type === "read_progress") return applyReadProgress(row, action.progress);
    if (action.type === "marked_all_read") return applyMarkAllRequested(row, action.at);
    // A precise answer means nothing to a row whose server no longer has the
    // cursor (a rollback that reached this tab after the write left).
    const { outcome, receivedAt } = action;
    const answer = state.preciseReadCursor ? outcome.state : undefined;
    const provenance = answer && { startedAt: answer.startedAt, receivedAt };
    return acceptServerRead(row, answer, provenance, { endsMarkAll: outcome.request === "all" });
  });
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
    case "read_progress":
    case "marked_all_read":
    case "read_confirmed":
      return applyReadTransition(state, action);
    case "preference_changed":
      return applyPreference(state, action.target, action.change);
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
 * What the sidebar already knows about the conversation an event arrived in.
 *
 * One lookup rather than one per field: the mute preference and the
 * conversation's name are two facts about the same row, and reading it twice is
 * how they eventually come from different rows. The absent row is normalised
 * here as well, so the caller reads two plain values instead of re-deciding what
 * a missing conversation means at each use.
 */
function conversationRowFor(
  event: WSMessageCreatedEvent,
  state: SidebarState,
): { muted: boolean; name: string } {
  const items =
    state.status !== "ready" ? [] : event.target_type === "channel" ? state.channels : state.dms;
  const row = items.find(({ id }) => id === event.target_id);
  return { muted: Boolean(row?.muted), name: row?.name ?? "" };
}

/** The wire payload projected onto what the presentation layer reads. */
function notificationEventFrom(
  event: WSMessageCreatedEvent,
  payload: NonNullable<WSMessageCreatedEvent["payload"]>,
  conversationName: string,
): MessageNotificationEvent {
  return {
    eventId: payload.id,
    targetKind: event.target_type,
    targetId: event.target_id,
    senderId: payload.sender_id ?? "",
    senderDisplayName: payload.sender_display_name ?? "",
    // Typed unknown on the wire on purpose: it is a URL from another user's
    // profile and the payload does not vouch for it. A non-string is simply
    // no avatar, which the alert already renders as initials.
    senderAvatarUrl:
      typeof payload.sender_avatar_url === "string" ? payload.sender_avatar_url : undefined,
    bodyText: payload.body_text ?? "",
    conversationName,
    // Narrowed here, at the wire's edge, so the presentation layer receives one
    // of three values and never a server string it has to interpret (#826).
    priority: normalizeMessagePriority(payload.priority),
    policy: payload.notification_policy,
  };
}

/**
 * Hands one freshly received message to the presentation layer (issue #749).
 *
 * The sidebar's own part is over by the time this runs: the event has been
 * deduplicated and unread is about to be updated. Whether anything is heard or
 * shown, on which surface, and on which tab, is decided entirely over there —
 * this supplies the event plus the two facts no server can observe (the reader
 * has this conversation open here; they muted it).
 *
 * Nothing comes back. A chime that is blocked, an event another tab claimed, or
 * a browser that cannot coordinate at all changes nothing about the badge:
 * presentation and message state are separate on purpose.
 */
function presentIncomingMessage(
  event: WSMessageCreatedEvent,
  state: SidebarState,
  activeTarget: WSSubscriptionTarget | undefined,
  row: { muted: boolean; name: string },
  sinks: MessagePresentationSinks,
): void {
  const payload = event.payload;
  if (state.status !== "ready" || !payload) return;
  // Deliberately not awaited, and safe to drop: the presentation layer never
  // rejects, and its outcome changes nothing here. Whether this tab won the
  // event's claim, lost it, or found no way to coordinate at all, the unread
  // badge and the message itself are decided by the lines that follow.
  void presentLiveMessageNotification(
    notificationEventFrom(event, payload, row.name),
    {
      currentUserId: state.currentUserId,
      isMutedConversation: row.muted,
      isActiveConversation:
        activeTarget?.kind === event.target_type && activeTarget.targetId === event.target_id,
    },
    sinks,
  );
}

/**
 * #1082 (security review SR-002): how long the read cursor writer waits after
 * the server rate-limits a write — the server's own Retry-After for POST
 * …/read, a fixed 60s (UserRateLimiter). Every other failure is not a "not now".
 */
export const READ_RATE_LIMIT_BACKOFF_MS = 60_000;

function readBackoffAfter(error: unknown): number | undefined {
  const limited = error instanceof ApiRequestError && error.status === 429;
  return limited ? READ_RATE_LIMIT_BACKOFF_MS : undefined;
}

// ── Hook ─────────────────────────────────────────────────────────────────────

type SidebarData = Awaited<ReturnType<typeof fetchSidebarData>>;

/** The "loaded" action a sidebar payload makes. */
function loadedAction(
  data: SidebarData,
  persistedUnread: PersistedUnreadEntry[],
  provenance: AnswerProvenance,
): Extract<Action, { type: "loaded" }> {
  return {
    type: "loaded",
    currentUserId: data.currentUserId,
    workspaceId: data.workspaceId,
    attachmentLimits: {
      maxUploadBytes: data.maxUploadBytes ?? null,
      maxFiles: data.maxFiles ?? 1,
      maxBytes: data.maxBytes ?? Number.MAX_SAFE_INTEGER,
    },
    notificationLevelsEnabled: data.notificationLevelsEnabled ?? false,
    channels: data.channels,
    dms: data.dms,
    categories: data.categories,
    persistedUnread,
    preciseReadCursor: data.preciseReadCursor ?? false,
    provenance,
  };
}

export function useChatSidebar() {
  const [state, dispatch] = useReducer(reducer, { status: "loading" });
  // The in-app surface of the delivery plan (issue #744). One alert, the newest,
  // and nothing kept: it is a notification, not a history. Held here because
  // this hook is where an arriving message is already turned into effects.
  const [inAppAlert, setInAppAlert] = useState<InAppAlert | null>(null);
  const dismissInAppAlert = useCallback(() => setInAppAlert(null), []);
  const { pathname } = useLocation();
  const navigate = useNavigate();
  const openedTarget = targetFromPath(pathname);
  const openedTargetKind = openedTarget?.kind;
  const openedTargetId = openedTarget?.targetId;
  const mountedRef = useRef(true);
  const loadPromiseRef = useRef<Promise<void> | null>(null);
  // #1082: this session's request clock. Every sidebar fetch and read write
  // takes a tick when it starts and when it is answered, every arrival and
  // mark-all the tick at which it happened. It only answers causal questions —
  // was this request sent after that answer arrived, did this message arrive
  // before that request left — never which messages a snapshot counted. See
  // sidebarReadState.
  const clockRef = useRef(0);
  const tick = useCallback(() => ++clockRef.current, []);
  // #1082: the auth session the read state belongs to, advanced by the same
  // auth changes that restart the writer — a login, a logout, an account
  // switch, never a refresh rotation. A fetch started under another session
  // lands nowhere.
  const sessionRef = useRef(0);
  // #1082: whether the server takes precise read positions, as the newest
  // accepted payload said — synchronous, so a write about to be sent asks the
  // server that is there now, not the one the last render saw.
  const capabilityRef = useRef(false);
  // #1082 (security review SR-001): the user this session's read state
  // was loaded for, and — after a refresh rotation, until the server has said
  // whose token it is — the check every read write waits on. The refresh token
  // is a cookie every tab shares, so a rotation can hand this tab another
  // account's token; "refresh" is a claim, and this is where it is verified.
  const sessionUserRef = useRef<string | null>(null);
  const identityCheckRef = useRef<Promise<boolean> | null>(null);

  const load = useCallback(() => {
    if (loadPromiseRef.current) return loadPromiseRef.current;
    dispatch({ type: "reload" });
    const session = sessionRef.current;
    const startedAt = tick();
    const current = () => mountedRef.current && session === sessionRef.current;

    const loading = fetchSidebarData()
      .then((data) => {
        if (!current()) return;
        const { currentUserId, workspaceId } = data;
        const persistedUnread =
          currentUserId && workspaceId ? loadPersistedUnread(currentUserId, workspaceId) : [];
        capabilityRef.current = data.preciseReadCursor ?? false;
        sessionUserRef.current = currentUserId;
        dispatch(loadedAction(data, persistedUnread, { startedAt, receivedAt: tick() }));
      })
      .catch((err: unknown) => {
        if (current()) {
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
  }, [tick]);

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
      const session = sessionRef.current;
      const startedAt = tick();
      fetchSidebarData()
        .then((data) => {
          if (!mountedRef.current || session !== sessionRef.current) return;
          capabilityRef.current = data.preciseReadCursor ?? false;
          // previous is always defined at this call site (refreshSidebar only
          // ever runs once the sidebar is already "ready"), so mergeUnread
          // never consults the persisted cache — never worth a localStorage
          // read here.
          dispatch(loadedAction(data, [], { startedAt, receivedAt: tick() }));
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
  }, [tick]);

  // #1082: a row holding what it could not settle — an answer refused, or one
  // taken as a conservative upper bound — is reconciled by one refetch, whose
  // request starts after it and so settles it. Event-driven and coalesced: all
  // the marks up to the latest are covered by the same refetch, and its own
  // answer leaves a new mark only if something new happened while it was out.
  const convergedThrough = useRef(0);
  useEffect(() => {
    if (state.status !== "ready") return;
    let latest = 0;
    for (const row of [...state.channels, ...state.dms]) {
      latest = Math.max(latest, row.reconcileAfter ?? 0);
    }
    if (latest <= convergedThrough.current) return;
    convergedThrough.current = latest;
    refreshSidebar();
  }, [state, refreshSidebar]);

  /**
   * POST …/read for the read cursor writer (#1082), stamped with the tick it
   * was sent at. A position refused as a bad request means the server no
   * longer has the precise cursor — a rollback reached this tab: positions
   * stop at once, nothing is acknowledged or retried, and the sidebar asks the
   * server for its authority instead.
   */
  const sendReadCursor = useCallback<ReadWriteSender<TimedReadState>>(
    (target, lastReadMessageId, options) => {
      const session = sessionRef.current;
      const send = () => {
        const startedAt = tick();
        return markConversationRead(target.kind, target.targetId, lastReadMessageId, options).then(
          (state) => state && { ...state, startedAt },
          (error: unknown) => {
            const refused = error instanceof ApiRequestError && error.status === 400;
            if (lastReadMessageId && refused && session === sessionRef.current) {
              capabilityRef.current = false;
              refreshSidebar();
            }
            throw error;
          },
        );
      };
      // A position read under one identity is never sent under another: while
      // a rotated token is unconfirmed, the write waits, and it is dropped if
      // the token turns out to be someone else's.
      const check = identityCheckRef.current;
      if (!check) return send();
      return check.then((same) => {
        if (!same) throw new Error("read cursor dropped: the session changed");
        return send();
      });
    },
    [tick, refreshSidebar],
  );

  // #1082: the one writer of read cursors for this session. It outlives every
  // timeline that feeds it, so a conversation left mid-debounce still gets its
  // write, under its own target; and it lives exactly as long as the session —
  // the auth session itself, not the sidebar that last loaded — so a token
  // installed or removed drops everything pending at that very moment, before
  // any timer of the old session can fire. The read state goes with it: what
  // the sidebar showed belonged to the previous identity, so it is dropped and
  // loaded again for the new one. A refresh rotation keeps the writer, what it
  // has pending, and the read state — once the server confirms the rotated
  // token is still this session's user. Until then writes wait; a token of
  // another user, or no answer, is a session change like any other.
  const readWriterRef = useRef<ReadCursorWriter | null>(null);
  useEffect(() => {
    const start = () => {
      readWriterRef.current?.dispose();
      const writer = createReadCursorWriter(
        sendReadCursor,
        (target, outcome) => {
          if (readWriterRef.current !== writer) return;
          dispatch({ type: "read_confirmed", target, outcome, receivedAt: tick() });
        },
        { acceptsPositions: () => capabilityRef.current, backoffAfter: readBackoffAfter },
      );
      readWriterRef.current = writer;
    };
    start();
    const changeSession = () => {
      sessionRef.current += 1;
      capabilityRef.current = false;
      sessionUserRef.current = null;
      identityCheckRef.current = null;
      loadPromiseRef.current = null;
      start();
      dispatch({ type: "reload" });
      if (isAuthenticated()) void load();
    };
    const confirmIdentity = () => {
      const expected = sessionUserRef.current;
      // Nothing was loaded for anyone yet: the load in flight is the new token's.
      if (expected === null) return;
      const session = sessionRef.current;
      const settle = (same: boolean) => {
        if (!mountedRef.current || session !== sessionRef.current) return false;
        if (!same) changeSession();
        return same;
      };
      const check = fetchSidebarData().then(
        (data) => settle(data.currentUserId === expected),
        () => settle(false),
      );
      identityCheckRef.current = check;
      void check.finally(() => {
        if (identityCheckRef.current === check) identityCheckRef.current = null;
      });
    };
    const unsubscribe = onAuthChange((change) => {
      if (change === "refresh") confirmIdentity();
      else changeSession();
    });
    return () => {
      unsubscribe();
      readWriterRef.current?.dispose();
      readWriterRef.current = null;
    };
  }, [load, sendReadCursor, tick]);

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
    onConversationUpdated: (event) => {
      invalidateConversationDetails(event);
      refreshSidebar();
    },
    // A system message landed (a rename, someone leaving). The sidebar shows no
    // message content, but a departure changes what this user may see, so the
    // same refetch settles it — and it is coalesced, so a burst costs one.
    onConversationEvent: refreshSidebar,
    onMessageCreated: (event: WSMessageCreatedEvent) => {
      // State ingestion, once per message and before anything derives from it
      // (issue #750). `false` is a redelivery: this client already took the
      // message in, so it must neither count again nor be offered for
      // presentation. The ledger is bounded and session-scoped — it replaced an
      // unbounded Set held here, which grew for the life of the tab. See
      // realtimeMessageLedger for why bounding it is safe against the server's
      // own delivery contract.
      if (!admitRealtimeMessage(event.message_id)) return;
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
      const row = conversationRowFor(event, state);
      // The mention half of the classification the presentation layer also
      // reads, and the only part of it the sidebar needs: it decides the unread
      // badge's dot. Authoritative — the server's own mention codec decided
      // this, not a reading of the body here.
      const isMentioned =
        state.status === "ready" &&
        isNamedRecipient(event.payload?.notification_policy, state.currentUserId);
      presentIncomingMessage(event, state, openedTarget, row, {
        showInApp: setInAppAlert,
        navigate: (path) => {
          navigate(path);
          refreshSidebar();
        },
      });
      dispatch({
        type: "message_created",
        target: { kind: event.target_type, targetId: event.target_id },
        senderId: event.payload?.sender_id ?? "",
        messageId: event.message_id,
        messageCreatedAt,
        isMentioned,
        at: tick(),
      });
    },
  });

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
   * The open timeline's read progress (#1082): the cursor it reached.
   *
   * The cursor goes to the writer, which coalesces a reading session into a
   * handful of writes; the count moves when the server answers them, never on
   * the cursor alone — the browser cannot prove which messages a count
   * included (see sidebarReadState). Opening a
   * conversation reports nothing by itself — only rows actually seen move the
   * cursor, so a conversation opened and left keeps every unread it had.
   *
   * Against a server without the precise cursor (one an open tab can meet
   * during a rollback) nothing is persisted progressively: that server would
   * read any write as "everything is read until now". There are no
   * automatic read writes. Only an explicit manual mark-all is safe when the
   * loaded timeline may contain gaps.
   */
  const reportReadProgress = useCallback((target: ReadTarget, progress: ReadProgress) => {
    dispatch({ type: "read_progress", target, progress });
    if (capabilityRef.current) readWriterRef.current?.advance(target, progress.readThrough);
  }, []);

  // A cursor read moments before the tab is hidden or closed must not wait out
  // the debounce: the page may never run another timer. A page restored from
  // the back/forward cache has been frozen for an unknown time, so it asks the
  // server again rather than trusting what it showed.
  useEffect(() => {
    const onPageHide = () => readWriterRef.current?.flushOnUnload();
    const onPageShow = (event: PageTransitionEvent) => {
      if (event.persisted) refreshSidebar();
    };
    const onVisibilityChange = () => {
      if (document.visibilityState === "hidden") readWriterRef.current?.flush();
    };
    window.addEventListener("pagehide", onPageHide);
    window.addEventListener("pageshow", onPageShow);
    document.addEventListener("visibilitychange", onVisibilityChange);
    return () => {
      window.removeEventListener("pagehide", onPageHide);
      window.removeEventListener("pageshow", onPageShow);
      document.removeEventListener("visibilitychange", onVisibilityChange);
    };
  }, [refreshSidebar]);

  /**
   * Marks a conversation read without opening it (issue #527).
   *
   * The explicit "everything here is read": the row clears at once, and the
   * server resolves "everything" to the newest message it holds — the client
   * names no position, so nothing that arrives afterwards is consumed. It goes
   * through the same writer as the cursor, so it never races a read write for
   * the same conversation. Nothing here navigates, changes the selection or
   * touches the composer, and a failed write is reconciled by the next refetch
   * rather than surfaced.
   */
  const markRead = useCallback(
    (target: WSSubscriptionTarget) => {
      dispatch({ type: "marked_all_read", target, at: tick() });
      readWriterRef.current?.markAll(target);
    },
    [tick],
  );

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
   * Conversations with a notification-preference write in flight, keyed
   * `kind:targetId`.
   *
   * One set for every writer of this preference, not one per entry point. The
   * sidebar's mute shortcut and the settings page's select change the same row
   * through two different endpoints (issue #136), and two independent guards
   * would each police only their own surface — leaving the one interleaving that
   * matters, a click in each, completely unguarded.
   *
   * A ref rather than state: nothing renders from it, and it has to be read and
   * written synchronously inside one call — a state update would land a render
   * later, which is exactly the window two clicks slip through.
   */
  const preferenceInFlight = useRef<Set<string>>(new Set());

  /**
   * Writes one conversation's notification preference, optimistically and with
   * at most one write per conversation in flight.
   *
   * The one coordination primitive both public setters below go through
   * (issue #136), because both change the same row and the property has to hold
   * across them rather than within each:
   *
   *  - optimistic first, because this is a private preference the server either
   *    accepts or refuses outright, so showing it immediately and rolling back
   *    on failure is honest;
   *  - the refetch after a confirmed write reconciles with what was actually
   *    persisted, which is what makes the server — not the click — the last
   *    word. That matters most for the mute shortcut, whose restored level this
   *    client never predicted;
   *  - a second call for a conversation already being written is dropped, not
   *    queued: the endpoints behind this have no ordering guarantee in flight,
   *    and the refetch that follows the first one converges the row anyway;
   *  - the key is the target, so different conversations are independent and
   *    never wait on each other, and the entry is released in `finally` even
   *    when the error is re-thrown for the caller to render.
   *
   * `rollback` is the caller's, because only the caller knows which fields its
   * optimistic change touched — reverting a field the write never claimed would
   * undo something somebody else wrote.
   */
  const writePreference = useCallback(
    async (
      target: WSSubscriptionTarget,
      optimistic: ConversationPreferenceChange,
      rollback: ConversationPreferenceChange,
      request: () => Promise<void>,
    ) => {
      const key = `${target.kind}:${target.targetId}`;
      if (preferenceInFlight.current.has(key)) return;
      preferenceInFlight.current.add(key);
      dispatch({ type: "preference_changed", target, change: optimistic });
      try {
        await request();
        refreshSidebar();
      } catch (error) {
        dispatch({ type: "preference_changed", target, change: rollback });
        throw error;
      } finally {
        preferenceInFlight.current.delete(key);
      }
    },
    [refreshSidebar],
  );

  /**
   * Silences or restores one conversation for this user only (issue #527).
   *
   * The sidebar's quick shortcut, and it touches `muted` only — on the wire and
   * in the optimistic state alike. That is the invariant of issue #136: the
   * level the user chose in their profile is not this action's to change, so
   * silencing a conversation and turning it back on returns them to the level
   * they had, and the row shows it immediately rather than after a round trip.
   */
  const setMuted = useCallback(
    async (target: WSSubscriptionTarget, muted: boolean) => {
      if (state.status !== "ready") return;
      const items = target.kind === "channel" ? state.channels : state.dms;
      const previous = Boolean(items.find((item) => item.id === target.targetId)?.muted);
      await writePreference(target, { muted }, { muted: previous }, () =>
        setConversationMuted(target.kind, target.targetId, muted),
      );
    },
    [state, writePreference],
  );

  /**
   * Sets the whole notification preference of one conversation (issue #136).
   *
   * The settings page's control, against the canonical endpoint. It shares
   * `writePreference` with the mute shortcut above, so the two cannot be used
   * out of order against the same conversation even while both surfaces are
   * mounted together on /profile.
   *
   * The optimistic change states both dimensions, because this mode decides
   * both: `muted` names the third mode and the other two are levels that also
   * lift a mute. Choosing what to hear is choosing to hear something, which is
   * what the server does too.
   *
   * The `muted` mode is the exception, and deliberately: it leaves the level
   * alone, exactly like the shortcut, so the profile select and the row menu
   * agree about what silencing costs.
   */
  const setNotificationMode = useCallback(
    async (target: WSSubscriptionTarget, mode: ConversationNotificationMode) => {
      if (state.status !== "ready") return;
      const items = target.kind === "channel" ? state.channels : state.dms;
      const current = items.find((item) => item.id === target.targetId);
      const previous: ConversationPreferenceChange = {
        muted: Boolean(current?.muted),
        notificationLevel: current?.notificationLevel ?? "all",
      };
      const optimistic: ConversationPreferenceChange =
        mode === "muted"
          ? { muted: true }
          : { muted: false, notificationLevel: mode as ConversationNotificationLevel };
      await writePreference(target, optimistic, previous, () =>
        setConversationNotificationMode(target.kind, target.targetId, mode),
      );
    },
    [state, writePreference],
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
    reportReadProgress,
    renameChannel,
    renameGroup,
    setMuted,
    setNotificationMode,
    leaveConversation,
    inAppAlert,
    dismissInAppAlert,
  };
}
