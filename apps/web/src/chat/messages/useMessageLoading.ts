import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";

import type { MessagePage } from "../chatTypes";
import { ApiRequestError } from "../../lib/api";
import type { MessagesGateway } from "./messagesGateway";
import type { ConversationScope } from "./useConversationScope";
import type { ReactionTimers } from "./useReactionTimers";
import { isAbortError, type RequestRegistry } from "./useRequestRegistry";
import type { Action } from "./types";

/**
 * Issue #475: the backend deliberately answers a non-member with the same
 * non-enumerating 404 it uses for an unknown id (see chat-service's
 * mapServiceError), so a 404 on the conversation's own message page is this
 * client's only signal that the real cause is "not a member", not "gone
 * missing". Anything else (network, timeout, 5xx, or a 401 the auth client
 * itself did not already recover from) keeps the existing generic error path.
 */
function isAccessDenied(error: unknown): boolean {
  return error instanceof ApiRequestError && error.status === 404;
}

/**
 * The first page of a conversation, and the pages behind it.
 *
 * Both reads are guarded the same way: the request is aborted when the target
 * changes or the view unmounts, and a completion is applied only while it still
 * belongs to the conversation on screen.
 */
export interface MessageLoading {
  retry: () => void;
  loadMore: () => void;
}

interface Options {
  scope: ConversationScope;
  gateway: MessagesGateway;
  dispatch: (action: Action) => void;
  /** The realtime reads, aborted alongside the page reads on a target change. */
  fallbacks: RequestRegistry;
  reactionTimers: ReactionTimers;
  /** Opaque cursor for the next older page, from committed state. */
  nextCursor: string;
  /** Whether an older-page fetch is already in progress, from committed state. */
  loadingMore: boolean;
  /**
   * A `?message=` deep link. Only its identity matters here: a new one reopens
   * the conversation from its newest page, and the viewport pages back to the
   * message through the ordinary history (#1088). It is never fetched on its
   * own — a message spliced in outside its page is not part of the history
   * the timeline prepends to, so the reader would land beside it, not on it.
   */
  focusMessageId?: string;
  /** Which request asked for focusMessageId (issue #896); "" when unmarked. */
  focusRequest?: string;
  /** Whether focusMessageId is among the messages already loaded. */
  focusLoaded?: boolean;
}

/** The message target the current load was made for (#1088, #1099). */
export interface FocusLoad {
  messageId: string;
  request: string;
  /** Bumped whenever the target owes the conversation a fresh load. */
  generation: number;
}

/**
 * Whether a change of message target owes a fresh load (issue #1099).
 *
 * Only a target that is not loaded does: the search for it starts from the
 * newest page. A new one always reloads; asking explicitly again for the
 * *same* one is the reader's retry after a search that ended without reaching
 * it (the cap, a failed page), and an unmarked repeat ("") never reloads. A
 * loaded target — new or not — is a jump the timeline makes on its own, and
 * it replaces whatever target was still being searched for.
 */
export function nextFocusLoad(
  previous: FocusLoad,
  messageId: string,
  request: string,
  loaded: boolean,
): FocusLoad {
  if (previous.messageId === messageId && previous.request === request) return previous;
  const reload = !loaded && (previous.messageId !== messageId || request !== "");
  const generation = previous.generation + (reload ? 1 : 0);
  return { messageId, request, generation };
}

export function useMessageLoading({
  scope,
  gateway,
  dispatch,
  fallbacks,
  reactionTimers,
  nextCursor,
  loadingMore,
  focusMessageId = "",
  focusRequest = "",
  focusLoaded = false,
}: Options): MessageLoading {
  const pageAbort = useRef<AbortController | null>(null);
  const olderPageAbort = useRef<AbortController | null>(null);
  // The conversation the last load was for: reloading the same one (a new
  // message target) keeps the reply being composed, another one never does.
  const lastLoadKey = useRef("");

  // Adjusted during render, React's documented pattern for state derived from
  // a prop change (react-hooks/set-state-in-effect forbids the effect form).
  const [focusLoad, setFocusLoad] = useState<FocusLoad>({
    messageId: focusMessageId,
    request: focusRequest,
    generation: 0,
  });
  const nextLoad = nextFocusLoad(focusLoad, focusMessageId, focusRequest, focusLoaded);
  if (nextLoad !== focusLoad) setFocusLoad(nextLoad);

  /**
   * Pagination as the newest committed render left it.
   *
   * Mirrored rather than read from state because loadMore has to set the
   * in-flight flag before any async work: a second call in the same microtask
   * tick (two IO callbacks fired before the next render) must fail the guard
   * instead of issuing a duplicate fetch. Written in a layout effect with no
   * dependency list, so a completion can never read a stale cursor.
   */
  const paging = useRef({ nextCursor, loadingMore });
  useLayoutEffect(() => {
    paging.current.nextCursor = nextCursor;
    paging.current.loadingMore = loadingMore;
  });

  const sanitizePage = useCallback(
    (page: MessagePage): MessagePage => ({
      ...page,
      messages: page.messages.map((message) => scope.sanitize(message)),
    }),
    [scope],
  );

  const load = useCallback(() => {
    const loadKey = scope.key;
    pageAbort.current?.abort();
    olderPageAbort.current?.abort();
    fallbacks.abortAll();
    scope.retarget(loadKey);
    scope.markLoaded(null);
    const keepReply = lastLoadKey.current === loadKey;
    lastLoadKey.current = loadKey;

    const controller = new AbortController();
    pageAbort.current = controller;
    dispatch({ type: "loading", keepReply });

    gateway.fetchPage(undefined, controller.signal).then(
      (page) => {
        if (!scope.isCurrent(loadKey)) return;
        dispatch({ type: "loaded", page: sanitizePage(page) });
        scope.markLoaded(loadKey);
      },
      (error: unknown) => {
        if (!scope.isCurrent(loadKey)) return;
        if (isAbortError(error)) return;
        dispatch({ type: isAccessDenied(error) ? "denied" : "error" });
      },
    );

    return () => {
      controller.abort();
      olderPageAbort.current?.abort();
      fallbacks.abortAll();
      reactionTimers.clearAll();
    };
  }, [dispatch, fallbacks, gateway, reactionTimers, sanitizePage, scope]);

  useEffect(() => {
    if (!scope.targetId) return;
    return load();
  }, [load, scope.targetId, focusLoad.generation]);

  const retry = useCallback(() => {
    if (scope.targetId) load();
  }, [load, scope.targetId]);

  const loadMore = useCallback(() => {
    const cursor = paging.current.nextCursor;
    if (!cursor || paging.current.loadingMore) return;

    // Set before any async work, so the guard above refuses a second call in
    // the same tick. Cleared in both the success and the error path.
    paging.current.loadingMore = true;

    const loadKey = scope.key;
    olderPageAbort.current?.abort();
    const controller = new AbortController();
    olderPageAbort.current = controller;

    dispatch({ type: "prepending" });

    // The cursor travels with the answer: the reducer applies it only while the
    // timeline still continues from that cursor (issue #947).
    const settle = () => {
      if (olderPageAbort.current === controller) paging.current.loadingMore = false;
      return scope.isCurrent(loadKey);
    };
    gateway.fetchPage(cursor, controller.signal).then(
      (page) => {
        if (settle()) dispatch({ type: "prepended", page: sanitizePage(page), cursor });
      },
      (error: unknown) => {
        if (settle() && !isAbortError(error)) dispatch({ type: "prepend_error", cursor });
      },
    );
  }, [dispatch, gateway, sanitizePage, scope]);

  return { retry, loadMore };
}
