/**
 * chatSocket — the tab's single logical connection to /api/chat/ws.
 *
 * Why this module exists (issue #449): three independent hooks used to build
 * their own WebSocket to the same endpoint — useChatSidebar and useMessages
 * (both through useChatWebSocket) and useCallSignaling. One tab therefore held
 * three connections, and chat-service caps a user at WS_MAX_CONNECTIONS_PER_USER
 * (default 5), so a second tab already exceeded the budget and its handshakes
 * were rejected. Each of those loops also retried every 250ms–2s forever, with
 * no jitter, no offline pause and no stop condition, so one transient upstream
 * failure turned into a sustained storm that kept the budget exhausted while
 * the sockets the server still held timed out.
 *
 * The connection is refcounted: the first acquire opens it, the last release
 * closes it. Everything else — one socket, one reconnect timer, generation
 * guarding, backoff, offline handling — lives here so no caller can reintroduce
 * a second loop.
 *
 * Security notes:
 * - The access token travels as a subprotocol, never in the URL query string.
 * - It is read from sessionStorage at connect time, never held in module state.
 * - Nothing here logs the token, the cookie, the URL or any message payload.
 */

import { getAccessToken, getSessionGeneration, onAuthChange } from "../lib/authSession";
import { normalizeChatTargetId } from "./chatTargetId";

const CHAT_WS_URL =
  (import.meta.env.VITE_CHAT_WS_URL as string | undefined) ??
  `${window.location.protocol === "https:" ? "wss:" : "ws:"}//${window.location.host}/api/chat/ws`;

export const CHAT_WS_SUBPROTOCOL = "nchat.v1";

/**
 * Close code the server sends when it permanently rejects the connection
 * (e.g. the inbound rate limit was exceeded — see WS_INBOUND_BURST). Unlike
 * a transient drop (1006, 1011, ...), retrying immediately would just resend
 * the same messages and get closed again, so this code alone — never the
 * reason string, which is diagnostic only — stops the reconnect loop.
 */
const WS_CLOSE_POLICY_VIOLATION = 1008;

/** First retry lands in [BASE/2, BASE]; each further attempt doubles the ceiling. */
export const RECONNECT_BASE_DELAY_MS = 1_000;
/** Ceiling for the exponential term, so a long outage settles at ~15–30s. */
export const RECONNECT_MAX_DELAY_MS = 30_000;
/** Exponent cap. Guards 2 ** attempt against overflow on a very long outage. */
export const RECONNECT_MAX_EXPONENT = 10;
/** How long a socket must stay open before the attempt counter is forgiven. */
export const STABLE_CONNECTION_MS = 10_000;
/**
 * Consecutive attempts that never reached OPEN before the client gives up.
 *
 * The browser WebSocket API does not expose the handshake status to onerror, so
 * 401, 403 and 502 are indistinguishable here. Rather than guess, the client
 * bounds the loop: a revoked session and a dead upstream both stop after this
 * many failures. An explicit signal — a session change or the browser coming
 * back online — is what resumes it.
 */
export const MAX_CONSECUTIVE_FAILURES = 8;

export type ChatSocketStatus =
  | "connecting"
  | "connected"
  | "reconnecting"
  | "disconnected"
  | "failed";

export interface ChatSocketListener {
  /**
   * Called once per generation, as soon as that socket is usable. Consumers
   * resubscribe and resynchronise here; the generation lets them discard work
   * belonging to a socket that has since been replaced.
   */
  onOpen?: (generation: number) => void;
  onMessage?: (data: Record<string, unknown>, generation: number) => void;
  onClose?: (generation: number) => void;
  onStatus?: (status: ChatSocketStatus) => void;
}

export interface ChatSocketHandle {
  send: (payload: unknown) => boolean;
  isOpen: () => boolean;
  generation: () => number;
  release: () => void;
}

/** A conversation a consumer wants events for. */
export interface ChatSubscriptionTarget {
  kind: "channel" | "dm";
  targetId: string;
}

/** The key a target is owned by. Normalised, so two spellings are one target. */
export function chatSubscriptionKey({ kind, targetId }: ChatSubscriptionTarget): string {
  return `${kind}:${normalizeChatTargetId(targetId)}`;
}

/**
 * At most one activity frame per this window, however much the user types.
 *
 * The server's away timeout is minutes; anything well inside it keeps a working
 * user online, and there is no benefit to being more precise about a person who
 * is demonstrably at the keyboard.
 */
export const PRESENCE_ACTIVITY_THROTTLE_MS = 25_000;

/**
 * The DOM events that count as a person being there.
 *
 * Deliberately not `mousemove`: a cursor resting on a trackpad, a page that
 * scrolls under an animation, a pointer nudged by a passing hand — none of those
 * are somebody using the application, and treating them as such is how an
 * abandoned tab stays online forever.
 */
const ACTIVITY_EVENTS = ["keydown", "pointerdown", "touchstart", "wheel"] as const;

/**
 * Equal-jitter exponential backoff. Pure, so the schedule is asserted directly
 * instead of through a stubbed global, and `random` is injectable so tests do
 * not depend on Math.random.
 */
export function computeBackoffDelay(attempt: number, random: () => number = Math.random): number {
  const exponent = Math.min(Math.max(attempt, 0), RECONNECT_MAX_EXPONENT);
  const ceiling = Math.min(RECONNECT_BASE_DELAY_MS * 2 ** exponent, RECONNECT_MAX_DELAY_MS);
  const half = ceiling / 2;
  return Math.round(half + random() * half);
}

const listeners = new Set<ChatSocketListener>();

let socket: WebSocket | null = null;
let generation = 0;
let refCount = 0;
let attempt = 0;
let consecutiveFailures = 0;
let reachedOpen = false;
let reconnectTimer: number | null = null;
let stabilityTimer: number | null = null;
let status: ChatSocketStatus = "disconnected";
let sessionGeneration = getSessionGeneration();
let unsubscribeAuth: (() => void) | null = null;
let onlineListener: (() => void) | null = null;
let randomSource: () => number = Math.random;
let lastActivitySentAt = 0;
let activityListenersAttached = false;

/**
 * Who wants each target, and what to send for it.
 *
 * The connection owns the remote subscription; hooks only declare interest. That
 * is the difference this map exists for: the sidebar and the open conversation
 * both watch the same channel over the same socket, and when the conversation
 * unmounts it used to send `unsubscribe` for a target the sidebar was still
 * reading — so the sidebar silently stopped receiving events for it.
 *
 * A set of consumer ids rather than a count, because the failure modes are
 * repeats and not arithmetic: React StrictMode mounts an effect twice, a cleanup
 * can run after its replacement, and a consumer can re-declare what it already
 * has. Adding a name that is already present is a no-op; decrementing a counter
 * twice for one consumer is a subscription lost for everyone.
 */
const subscriptionOwners = new Map<
  string,
  { target: ChatSubscriptionTarget; consumers: Set<string> }
>();
/** What each consumer currently declares, so its next declaration is a diff. */
const consumerTargets = new Map<string, Map<string, ChatSubscriptionTarget>>();
/**
 * Targets the server has acknowledged on the *current* generation.
 *
 * A consumer that joins a target somebody else already holds sends no frame, so
 * no acknowledgement is coming for it — this is what lets it be told the
 * subscription is already live instead of waiting forever for a reply to a
 * question nobody asked. Cleared whenever the connection is replaced, because an
 * acknowledgement belongs to the socket that gave it.
 */
const confirmedTargets = new Set<string>();

function devLog(message: string, detail?: Record<string, unknown>): void {
  // Development only, and never carries a token, a URL or a message payload —
  // production must not print the same failure once per attempt.
  if (!import.meta.env.DEV) return;
  console.info(`[chat-ws] ${message}`, detail ?? {});
}

function setStatus(next: ChatSocketStatus): void {
  if (status === next) return;
  status = next;
  devLog(`state ${next}`, { attempt });
  for (const listener of [...listeners]) listener.onStatus?.(next);
}

export function getChatSocketStatus(): ChatSocketStatus {
  return status;
}

function clearReconnectTimer(): void {
  if (reconnectTimer === null) return;
  window.clearTimeout(reconnectTimer);
  reconnectTimer = null;
}

function clearStabilityTimer(): void {
  if (stabilityTimer === null) return;
  window.clearTimeout(stabilityTimer);
  stabilityTimer = null;
}

/** Detaches the current socket without letting its late callbacks run. */
function discardSocket(): void {
  const current = socket;
  socket = null;
  if (!current) return;
  current.onopen = null;
  current.onmessage = null;
  current.onerror = null;
  current.onclose = null;
  try {
    current.close();
  } catch {
    // A socket already closing throws in some browsers; nothing to recover.
  }
}

/**
 * Reports that a person just did something, and tells the server if it is worth
 * saying (issue #444).
 *
 * Presence is *human* activity, and the server can only see what the client
 * sends. Messages are posted over HTTP, so a user could type all afternoon and
 * still be marked away five minutes after their last WebSocket frame — which is
 * precisely what happened.
 *
 * What this is not is a heartbeat. Nothing here runs on a timer: no activity
 * means no frame, which is what lets the server's away detection still work on
 * an abandoned tab. The throttle bounds the other direction — a hundred
 * keystrokes are one frame, not a hundred.
 *
 * A closed socket consumes nothing: the throttle window is only spent on a frame
 * that was actually sent, so the first real activity after a reconnect is
 * reported immediately rather than waiting out a window nobody heard.
 */
export function markPresenceActivity(): void {
  const live = socketReady() ? socket : null;
  if (!live) return;
  const now = Date.now();
  if (lastActivitySentAt !== 0 && now - lastActivitySentAt < PRESENCE_ACTIVITY_THROTTLE_MS) return;
  lastActivitySentAt = now;
  live.send(JSON.stringify({ type: "ping" }));
}

function handleActivityEvent(): void {
  markPresenceActivity();
}

function handleVisibilityChange(): void {
  // Coming back to the tab is a person returning to it. Leaving is not activity
  // and deliberately reports nothing.
  if (document.visibilityState === "visible") markPresenceActivity();
}

/**
 * Installs the activity listeners once for the whole tab.
 *
 * Once, and here, because there is one connection: a listener per component
 * would multiply with the message list, and each would have to know about
 * throttling and about the socket. The connection already knows both.
 */
function ensureActivityListeners(): void {
  if (activityListenersAttached) return;
  activityListenersAttached = true;
  for (const name of ACTIVITY_EVENTS) {
    window.addEventListener(name, handleActivityEvent, { passive: true });
  }
  document.addEventListener("visibilitychange", handleVisibilityChange);
}

function removeActivityListeners(): void {
  if (!activityListenersAttached) return;
  activityListenersAttached = false;
  for (const name of ACTIVITY_EVENTS) {
    window.removeEventListener(name, handleActivityEvent);
  }
  document.removeEventListener("visibilitychange", handleVisibilityChange);
}

/**
 * Whether a frame sent right now would reach the server on a connection whose
 * consumers have been told about it.
 *
 * `reachedOpen` and not merely `readyState`: between creating a socket and its
 * open callback there is a window where writing would emit a frame the
 * reconnect path is about to emit again, since re-establishing what the
 * connection owns is exactly what opening does.
 */
function socketReady(): boolean {
  return socket !== null && reachedOpen && socket.readyState === WebSocket.OPEN;
}

function sendSubscriptionFrame(type: "subscribe" | "unsubscribe", target: ChatSubscriptionTarget) {
  const live = socketReady() ? socket : null;
  if (!live) return;
  live.send(JSON.stringify({ type, target_type: target.kind, target_id: target.targetId }));
}

/**
 * Declares everything one consumer wants, and emits only the frames that change
 * what the *connection* wants.
 *
 * `subscribe` is sent on the 0 → 1 transition of a target's owner set and
 * `unsubscribe` on the 1 → 0 transition; everything in between is bookkeeping.
 *
 * Returns the targets that are already live on this connection because somebody
 * else holds them — no frame was sent for those, so no acknowledgement is coming
 * and the caller must treat them as confirmed rather than waiting.
 */
export function setConsumerSubscriptions(
  consumerId: string,
  targets: readonly ChatSubscriptionTarget[],
): string[] {
  const next = new Map(targets.map((target) => [chatSubscriptionKey(target), target]));
  const previous = consumerTargets.get(consumerId);
  consumerTargets.set(consumerId, next);

  for (const [key, target] of previous ?? []) {
    if (next.has(key)) continue;
    const entry = subscriptionOwners.get(key);
    if (!entry) continue;
    entry.consumers.delete(consumerId);
    if (entry.consumers.size > 0) continue;
    subscriptionOwners.delete(key);
    confirmedTargets.delete(key);
    sendSubscriptionFrame("unsubscribe", target);
  }

  const alreadyLive: string[] = [];
  for (const [key, target] of next) {
    let entry = subscriptionOwners.get(key);
    if (!entry) {
      entry = { target, consumers: new Set() };
      subscriptionOwners.set(key, entry);
    }
    const wasUnowned = entry.consumers.size === 0;
    entry.consumers.add(consumerId);
    if (wasUnowned) {
      sendSubscriptionFrame("subscribe", target);
    } else if (confirmedTargets.has(key)) {
      alreadyLive.push(key);
    }
  }
  return alreadyLive;
}

/** Drops every interest one consumer declared. */
export function releaseConsumerSubscriptions(consumerId: string): void {
  setConsumerSubscriptions(consumerId, []);
  consumerTargets.delete(consumerId);
}

/**
 * Re-sends `subscribe` for targets this consumer holds, after the server told it
 * the room was temporarily unavailable. Subscribing to a target one already has
 * is idempotent server-side, so a retry cannot disturb the other owners.
 */
export function resendConsumerSubscriptions(consumerId: string, keys: Iterable<string>): void {
  const owned = consumerTargets.get(consumerId);
  if (!owned) return;
  for (const key of keys) {
    const target = owned.get(key);
    if (target) sendSubscriptionFrame("subscribe", target);
  }
}

/**
 * Re-establishes every owned target on a connection that has just opened.
 *
 * One frame per target, not one per consumer: the ownership map is what the
 * connection wants, and a reconnect is the connection saying it again.
 */
function resubscribeOwnedTargets(): void {
  confirmedTargets.clear();
  for (const entry of subscriptionOwners.values()) {
    if (entry.consumers.size === 0) continue;
    sendSubscriptionFrame("subscribe", entry.target);
  }
}

/** Forgets every declared interest. Used when the identity behind them changes. */
function clearSubscriptionOwnership(): void {
  subscriptionOwners.clear();
  consumerTargets.clear();
  confirmedTargets.clear();
}

/** Records an acknowledgement so a later joiner can be told the target is live. */
function noteSubscriptionAcknowledgement(data: Record<string, unknown>): void {
  if (data["type"] !== "subscribed" || data["operation"] !== "subscribe") return;
  const kind = data["target_type"];
  const targetId = data["target_id"];
  if ((kind !== "channel" && kind !== "dm") || typeof targetId !== "string") return;
  confirmedTargets.add(chatSubscriptionKey({ kind, targetId }));
}

function ensureOnlineListener(): void {
  if (onlineListener) return;
  onlineListener = () => {
    // Implicit trigger only: back online must never pull the connection out
    // of a fatal "failed" state on its own — that recovery is reserved for
    // an explicit new session (handleAuthChange) or a fresh acquireChatSocket
    // call, both of which call connect() directly and are not guarded here.
    if (refCount === 0 || status === "failed") return;
    // Back online is an explicit signal, so it also clears a bounded give-up.
    consecutiveFailures = 0;
    clearReconnectTimer();
    connect();
  };
  window.addEventListener("online", onlineListener);
}

function removeOnlineListener(): void {
  if (!onlineListener) return;
  window.removeEventListener("online", onlineListener);
  onlineListener = null;
}

function scheduleReconnect(): void {
  if (refCount === 0 || reconnectTimer !== null || socket) return;
  if (consecutiveFailures >= MAX_CONSECUTIVE_FAILURES) {
    setStatus("failed");
    devLog("giving up after consecutive failures", { consecutiveFailures });
    return;
  }
  if (typeof navigator !== "undefined" && navigator.onLine === false) {
    // Offline is not a failed attempt: hold the counter and wait for `online`.
    setStatus("disconnected");
    devLog("offline; reconnect suspended");
    return;
  }
  const delay = computeBackoffDelay(attempt, randomSource);
  attempt += 1;
  setStatus("reconnecting");
  devLog("reconnect scheduled", { attempt, delayMs: delay });
  reconnectTimer = window.setTimeout(() => {
    reconnectTimer = null;
    connect();
  }, delay);
}

function connect(): void {
  if (refCount === 0 || socket || reconnectTimer !== null) return;
  if (typeof navigator !== "undefined" && navigator.onLine === false) {
    setStatus("disconnected");
    return;
  }

  const token = getAccessToken();
  if (!token) {
    // No session: stay idle. onAuthChange restarts this when one arrives.
    setStatus("disconnected");
    return;
  }

  let created: WebSocket;
  try {
    created = new WebSocket(CHAT_WS_URL, [token, CHAT_WS_SUBPROTOCOL]);
  } catch {
    consecutiveFailures += 1;
    scheduleReconnect();
    return;
  }

  socket = created;
  reachedOpen = false;
  const currentGeneration = ++generation;
  setStatus(attempt === 0 ? "connecting" : "reconnecting");

  created.onopen = () => {
    // One open per generation is an invariant consumers resynchronise on, so it
    // is enforced here rather than assumed from the transport.
    if (socket !== created || reachedOpen) return;
    reachedOpen = true;
    setStatus("connected");
    // A fresh connection is itself evidence the user is here — the server marks
    // them online on register — so the throttle starts spent, and the next real
    // activity is reported without waiting out a window.
    lastActivitySentAt = 0;
    // The connection re-establishes what it owns before any consumer is told it
    // opened, so a consumer re-declaring what it already had produces no second
    // frame for the same target.
    resubscribeOwnedTargets();
    // The attempt counter is deliberately not reset here: a socket that opens
    // and drops immediately must keep backing off, not restart at the floor.
    clearStabilityTimer();
    stabilityTimer = window.setTimeout(() => {
      stabilityTimer = null;
      if (socket !== created || created.readyState !== WebSocket.OPEN) return;
      attempt = 0;
      consecutiveFailures = 0;
      devLog("connection stable; backoff reset");
    }, STABLE_CONNECTION_MS);
    for (const listener of [...listeners]) listener.onOpen?.(currentGeneration);
  };

  created.onmessage = (event: MessageEvent<unknown>) => {
    if (socket !== created) return;
    let data: unknown;
    try {
      data = JSON.parse(event.data as string);
    } catch {
      return;
    }
    if (!data || typeof data !== "object") return;
    noteSubscriptionAcknowledgement(data as Record<string, unknown>);
    for (const listener of [...listeners]) {
      listener.onMessage?.(data as Record<string, unknown>, currentGeneration);
    }
  };

  created.onerror = () => {
    // The event carries no usable detail and onclose always follows, so there
    // is nothing to log here and nothing to schedule.
  };

  created.onclose = (event: CloseEvent) => {
    if (socket !== created) return;
    socket = null;
    clearStabilityTimer();
    // The acknowledgements belonged to this socket. Ownership does not: the
    // consumers still want those targets, and the next open re-asserts them.
    confirmedTargets.clear();
    for (const listener of [...listeners]) listener.onClose?.(currentGeneration);
    if (event.code === WS_CLOSE_POLICY_VIOLATION) {
      // Permanent rejection: reconnecting would resend the same bootstrap
      // burst and loop forever. Stop until an explicit new session or a new
      // acquire retries — see handleAuthChange and acquireChatSocket.
      clearReconnectTimer();
      setStatus("failed");
      devLog("stopped after policy violation close");
      return;
    }
    consecutiveFailures = reachedOpen ? 0 : consecutiveFailures + 1;
    scheduleReconnect();
  };
}

function handleAuthChange(): void {
  const nextSessionGeneration = getSessionGeneration();
  if (nextSessionGeneration === sessionGeneration) return;
  sessionGeneration = nextSessionGeneration;

  // A different session means the open socket carries the wrong identity.
  clearReconnectTimer();
  clearStabilityTimer();
  discardSocket();
  attempt = 0;
  consecutiveFailures = 0;
  // Subscriptions are authorized against an identity, so none of them survive it
  // changing. Consumers that are still mounted declare their targets again on
  // the next open, which is what re-derives them under the new session.
  clearSubscriptionOwnership();
  lastActivitySentAt = 0;

  if (refCount === 0) {
    setStatus("disconnected");
    return;
  }
  if (!getAccessToken()) {
    // Logout: no reconnection at all until a session exists again.
    setStatus("disconnected");
    devLog("session cleared; connection stopped");
    return;
  }
  connect();
}

/**
 * Joins the tab's shared chat connection, opening it if this is the first
 * caller. The returned handle must be released exactly once.
 */
export function acquireChatSocket(listener: ChatSocketListener): ChatSocketHandle {
  listeners.add(listener);
  refCount += 1;

  if (refCount === 1) {
    sessionGeneration = getSessionGeneration();
    unsubscribeAuth = onAuthChange(handleAuthChange);
    ensureOnlineListener();
    ensureActivityListeners();
  }

  let released = false;

  if (socket && reachedOpen && socket.readyState === WebSocket.OPEN) {
    // Joined an already-open socket: this listener still gets its own open
    // callback so it subscribes and resynchronises like any other. A socket
    // that has not fired onopen yet is left alone — every listener will be
    // called from there, so nobody sees two opens for one generation.
    //
    // Deferred by one microtask so the callback cannot run before acquire has
    // returned: a consumer that only has its handle afterwards would otherwise
    // be asked to subscribe with nothing to send on.
    const joinedGeneration = generation;
    queueMicrotask(() => {
      if (released || !listeners.has(listener)) return;
      if (generation !== joinedGeneration || !reachedOpen) return;
      listener.onOpen?.(joinedGeneration);
    });
  } else {
    connect();
  }
  return {
    send: (payload: unknown) => {
      if (released || !socket || socket.readyState !== WebSocket.OPEN) return false;
      socket.send(JSON.stringify(payload));
      return true;
    },
    isOpen: () => !released && socket !== null && socket.readyState === WebSocket.OPEN,
    generation: () => generation,
    release: () => {
      if (released) return;
      released = true;
      listeners.delete(listener);
      refCount -= 1;
      if (refCount > 0) return;
      clearReconnectTimer();
      clearStabilityTimer();
      discardSocket();
      unsubscribeAuth?.();
      unsubscribeAuth = null;
      removeOnlineListener();
      removeActivityListeners();
      clearSubscriptionOwnership();
      attempt = 0;
      consecutiveFailures = 0;
      lastActivitySentAt = 0;
      setStatus("disconnected");
    },
  };
}

/** Test-only: reset every module-level field. Mirrors authSession._resetListeners. */
export function _resetChatSocket(randomForTests: () => number = Math.random): void {
  clearReconnectTimer();
  clearStabilityTimer();
  discardSocket();
  removeOnlineListener();
  removeActivityListeners();
  clearSubscriptionOwnership();
  unsubscribeAuth?.();
  unsubscribeAuth = null;
  listeners.clear();
  refCount = 0;
  lastActivitySentAt = 0;
  attempt = 0;
  consecutiveFailures = 0;
  reachedOpen = false;
  generation = 0;
  status = "disconnected";
  sessionGeneration = getSessionGeneration();
  randomSource = randomForTests;
}
