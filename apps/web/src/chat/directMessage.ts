/**
 * Opening a direct conversation with someone, from wherever they were named
 * (issues #795, #834, #895).
 *
 * Three things used to be one thing, and collapsing them is what produced both
 * of this feature's defects in turn:
 *
 *  1. **The operation.** One `getOrCreateDirectDM` per recipient, deduplicated,
 *     with the abort and the single navigation that follow from it. Shared by
 *     every surface, because two surfaces asking about the same person is one
 *     question.
 *  2. **The origins.** Who is waiting, and for how long they are entitled to.
 *     A timeline waits as long as its conversation is open; a details panel
 *     waits as long as it is describing the person it asked about. Those are
 *     different lifetimes, and neither is the route — a panel can close, or
 *     turn to somebody else, without the URL moving at all.
 *  3. **The presentation.** Whether *this* recipient is being resolved, and the
 *     one refusal line. Read per recipient, so a row about Ana is not
 *     re-rendered because somebody clicked Bruno.
 *
 * Fusing 1 and 2 meant a reply could outlive the panel that asked for it and
 * navigate anyway. Fusing 1 and 3 meant every pending change re-rendered the
 * whole message list. So they are three collaborating pieces here, and the
 * seams between them are the point.
 *
 * Deliberately not a task framework. There is one operation shape, one kind of
 * interest in it, and one result; anything more general would be building for a
 * second caller that does not exist.
 *
 * Security: the recipient is a user id and the destination is whatever
 * conversation id the server answers with. Nothing here builds a URL from
 * anything the caller supplied, and no surface ever holds the AbortController.
 */

import {
  useCallback,
  useEffect,
  useId,
  useLayoutEffect,
  useMemo,
  useState,
  useSyncExternalStore,
} from "react";

import { getOrCreateDirectDM } from "./chatApi";
import { ApiRequestError } from "../lib/api";

/**
 * A surface's claim on the operations it started, valid for exactly one
 * lifetime of that surface.
 *
 * Opaque: the coordinator compares tokens and never parses them. What a token
 * *is* is the caller's business — see `useDirectMessageOrigin`, which mints one
 * per component instance per lifecycle key, so "the panel closed", "the panel
 * turned to somebody else" and "the conversation changed" are all simply a new
 * token and a release of the old one.
 */
export type DirectMessageOrigin = string;

/** What the coordinator needs from its host, read at call time, never captured. */
export interface DirectMessageDeps {
  /** The viewer; a conversation with yourself is refused here. */
  currentUserId: string;
  refreshConversations?: () => void;
  navigate: (path: string) => void;
}

/**
 * Just enough of the coordinator to answer "is this person being resolved?".
 *
 * A narrow port so a renderer that only draws a busy state — the mention token
 * inside a message, for instance — cannot start an operation, cancel one, or
 * reach the network. It is also what keeps RichTextRenderer, which knows
 * nothing about conversations, from having to import this module's whole
 * vocabulary.
 */
export interface DirectMessagePendingSource {
  isPending(recipientId: string): boolean;
  subscribePending(recipientId: string, listener: () => void): () => void;
}

export interface DirectMessageCoordinator extends DirectMessagePendingSource {
  /**
   * Resolves or creates the DM with `recipientId` and navigates to it, on
   * behalf of `origin`.
   *
   * Calling it for a recipient already being resolved does not start a second
   * request: the origin simply joins the one in flight, and will be served by
   * its result. That join is the whole reason this is shared — it is only true
   * between surfaces when there is one registry, and there is one.
   */
  open(recipientId: string, origin: DirectMessageOrigin): void;
  /**
   * Drops everything this origin was waiting for.
   *
   * An operation nobody is waiting for any more is aborted; one another origin
   * still wants is left running, and that origin still gets the navigation. So
   * closing a panel cannot cancel what the timeline is waiting on, and a
   * conversation switch cannot cancel what the panel is waiting on.
   */
  releaseOrigin(origin: DirectMessageOrigin): void;
  /** The one refusal line, or null. */
  error(): string | null;
  subscribeError(listener: () => void): () => void;
  /**
   * Replaces what the coordinator reads when an operation settles.
   *
   * `navigate` and the viewer's id change identity on every render of the host;
   * handing them in this way is what lets the coordinator itself never be
   * rebuilt, which is what keeps every consumer of it from being invalidated.
   */
  setDeps(deps: DirectMessageDeps): void;
  /** Aborts everything in flight. For the owner's unmount, and for tests. */
  dispose(): void;
}

/** One shared operation, and who is still waiting for it. */
interface PendingRequest {
  controller: AbortController;
  origins: Set<DirectMessageOrigin>;
}

/**
 * The words a failure gets.
 *
 * The DM-creation endpoint answers a target that is forbidden, unknown, or
 * workspace-ineligible (a suspended or removed account included) with the same
 * 404 "user not available" — deliberately undifferentiated server-side so the
 * caller cannot enumerate why (issue #795 §10). That is the one case with copy
 * of its own; every other failure keeps the generic retry line.
 */
function refusalFor(cause: unknown): string {
  if (cause instanceof ApiRequestError && cause.status === 404) {
    return "Esta pessoa não está mais disponível para conversa direta.";
  }
  return "Não foi possível abrir a conversa. Tente novamente.";
}

/**
 * Builds the coordinator.
 *
 * The dependencies are held here and replaced through `setDeps`, rather than
 * captured at construction or read through a ref the host passes in: the
 * coordinator's identity has to survive every render of the shell — or the
 * subscriptions below would be torn down and rebuilt constantly — and it is the
 * only thing that needs to know when `navigate` has a new identity.
 */
export function createDirectMessageCoordinator(
  initialDeps: DirectMessageDeps,
): DirectMessageCoordinator {
  let deps = initialDeps;
  const requests = new Map<string, PendingRequest>();
  const pendingListeners = new Map<string, Set<() => void>>();
  const errorListeners = new Set<() => void>();
  let error: string | null = null;

  function notifyPending(recipientId: string): void {
    for (const listener of pendingListeners.get(recipientId) ?? []) listener();
  }

  function publishError(next: string | null): void {
    if (error === next) return;
    error = next;
    for (const listener of errorListeners) listener();
  }

  /**
   * Whether this reply still belongs to somebody. False once the request has
   * been replaced or every origin has let go — which is what stops a stale
   * reply from navigating and from publishing a refusal nobody is waiting for.
   */
  function isLive(recipientId: string, request: PendingRequest): boolean {
    return requests.get(recipientId) === request && request.origins.size > 0;
  }

  function settleSuccess(recipientId: string, request: PendingRequest, conversationId: string) {
    if (!isLive(recipientId, request)) return;
    // Once, here, for the whole shared operation. Letting each waiting origin
    // run its own continuation is how one result became two navigations.
    deps.refreshConversations?.();
    deps.navigate(`/chat/dm/${encodeURIComponent(conversationId)}`);
  }

  function settleFailure(recipientId: string, request: PendingRequest, cause: unknown) {
    if (!isLive(recipientId, request)) return;
    if (cause instanceof DOMException && cause.name === "AbortError") return;
    publishError(refusalFor(cause));
  }

  function forget(recipientId: string, request: PendingRequest): void {
    if (requests.get(recipientId) !== request) return;
    requests.delete(recipientId);
    notifyPending(recipientId);
  }

  function start(recipientId: string, origin: DirectMessageOrigin): void {
    const controller = new AbortController();
    const request: PendingRequest = { controller, origins: new Set([origin]) };
    requests.set(recipientId, request);
    publishError(null);
    notifyPending(recipientId);
    void getOrCreateDirectDM(recipientId, controller.signal)
      .then(({ conversationId }) => settleSuccess(recipientId, request, conversationId))
      .catch((cause: unknown) => settleFailure(recipientId, request, cause))
      .finally(() => forget(recipientId, request));
  }

  return {
    open(recipientId, origin) {
      if (!recipientId || recipientId === deps.currentUserId) return;
      const existing = requests.get(recipientId);
      // Joining, not racing: this origin is now waiting for the request that is
      // already in flight, and no second one is sent.
      if (existing) existing.origins.add(origin);
      else start(recipientId, origin);
    },

    releaseOrigin(origin) {
      for (const [recipientId, request] of requests) {
        if (!request.origins.delete(origin)) continue;
        // Somebody else is still waiting, so the operation lives on — and its
        // result is still theirs.
        if (request.origins.size > 0) continue;
        request.controller.abort();
        forget(recipientId, request);
      }
    },

    isPending(recipientId) {
      return requests.has(recipientId);
    },

    subscribePending(recipientId, listener) {
      let listeners = pendingListeners.get(recipientId);
      if (!listeners) {
        listeners = new Set();
        pendingListeners.set(recipientId, listeners);
      }
      const bucket = listeners;
      bucket.add(listener);
      return () => {
        bucket.delete(listener);
        // Evicted with its last subscriber, so a long session does not keep an
        // entry per person ever looked at.
        if (bucket.size === 0) pendingListeners.delete(recipientId);
      };
    },

    error() {
      return error;
    },

    subscribeError(listener) {
      errorListeners.add(listener);
      return () => errorListeners.delete(listener);
    },

    setDeps(next) {
      deps = next;
    },

    dispose() {
      for (const request of requests.values()) request.controller.abort();
      requests.clear();
    },
  };
}

/**
 * A coordinator that does nothing, for a surface mounted without the shell's.
 *
 * It exists so a consumer can read `ctx.directMessage ?? inertDirectMessage`
 * rather than branch on undefined: nothing is ever pending, nothing fails, and
 * activating a control does nothing at all. Never reached in production, where
 * AppShell always provides the real one — the same shape
 * `noopConversationDrafts` has for the same reason (issue #769).
 */
export const inertDirectMessage: DirectMessageCoordinator = {
  open: () => {},
  releaseOrigin: () => {},
  isPending: () => false,
  subscribePending: () => () => {},
  error: () => null,
  subscribeError: () => () => {},
  setDeps: () => {},
  dispose: () => {},
};

/**
 * The shell's one coordinator, with its dependencies kept current.
 *
 * Created once and never replaced: `useState`'s initializer runs a single time,
 * and the deps box is mutated rather than re-closed, so neither a new
 * `navigate` identity nor a request finishing can change what the shell hands
 * down. That stability is the whole reason the message list stopped
 * re-rendering — a capability that changes on every pending change invalidates
 * every consumer of it, however granular they are.
 */
export function useDirectMessageCoordinator(deps: DirectMessageDeps): DirectMessageCoordinator {
  const [coordinator] = useState(() => createDirectMessageCoordinator(deps));
  // Updated in a layout effect, so `navigate` and the viewer's id are current
  // by the time anything can be clicked — and the coordinator is never rebuilt
  // to carry them.
  useLayoutEffect(() => {
    coordinator.setDeps(deps);
  });
  useEffect(() => () => coordinator.dispose(), [coordinator]);
  return coordinator;
}

/**
 * A surface's origin token, minted per instance and renewed whenever its
 * lifecycle key changes.
 *
 * `useId` gives the instance half, so two panels on screen are two origins;
 * `key` gives the lifetime half, so a panel that closes, or turns to a
 * different conversation, becomes a *different* origin and the previous one is
 * released by the cleanup below. React runs that cleanup for the old token
 * before the effect for the new one, which is exactly the handover required.
 *
 * `key` is the caller's own discriminant — a conversation's `kind:id`, or the
 * panel's target — and never the route: a panel can close without the URL
 * moving, and that was the lifetime the route could not express.
 */
export function useDirectMessageOrigin(
  coordinator: DirectMessageCoordinator,
  key: string,
): DirectMessageOrigin {
  const instanceId = useId();
  const origin = `${instanceId}:${key}`;
  useEffect(() => () => coordinator.releaseOrigin(origin), [coordinator, origin]);
  return origin;
}

/**
 * What one surface needs to start an open-DM: the shared operation, and its own
 * claim on it.
 *
 * One value rather than two props because they are never useful apart — an
 * origin without the coordinator can do nothing, and the coordinator without an
 * origin would open operations nobody is accountable for. Memoized on two
 * stable inputs, so it is itself stable.
 */
export interface DirectMessageAccess {
  coordinator: DirectMessageCoordinator;
  origin: DirectMessageOrigin;
}

export function useDirectMessageAccess(
  coordinator: DirectMessageCoordinator,
  key: string,
): DirectMessageAccess {
  const origin = useDirectMessageOrigin(coordinator, key);
  return useMemo(() => ({ coordinator, origin }), [coordinator, origin]);
}

/**
 * Whether *this* person is being resolved.
 *
 * One subscription per interested control, keyed by the person it is about, so
 * a request starting for somebody else returns the same `false` and React bails
 * out. That is the difference from handing the whole set down: with a set, a
 * pending change anywhere invalidated every holder of it, which for the message
 * list meant every row.
 */
export function useDirectMessagePending(
  source: DirectMessagePendingSource | undefined,
  recipientId: string,
): boolean {
  const subscribe = useCallback(
    (listener: () => void) => source?.subscribePending(recipientId, listener) ?? (() => {}),
    [source, recipientId],
  );
  return useSyncExternalStore(
    subscribe,
    () => source?.isPending(recipientId) ?? false,
    () => false,
  );
}

/**
 * The one refusal line.
 *
 * Subscribed by the single component that draws it and by nothing else, so a
 * failure re-renders that line and not the conversation behind it.
 */
export function useDirectMessageError(coordinator: DirectMessageCoordinator): string | null {
  return useSyncExternalStore(coordinator.subscribeError, coordinator.error, () => null);
}
