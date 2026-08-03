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
      attempt = 0;
      consecutiveFailures = 0;
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
  unsubscribeAuth?.();
  unsubscribeAuth = null;
  listeners.clear();
  refCount = 0;
  attempt = 0;
  consecutiveFailures = 0;
  reachedOpen = false;
  generation = 0;
  status = "disconnected";
  sessionGeneration = getSessionGeneration();
  randomSource = randomForTests;
}
