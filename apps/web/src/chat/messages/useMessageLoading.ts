import { useCallback, useEffect, useLayoutEffect, useRef } from "react";

import type { MessagePage } from "../chatTypes";
import { insertMessageChronologically } from "./messageOrder";
import type { MessagesGateway } from "./messagesGateway";
import type { ConversationScope } from "./useConversationScope";
import type { ReactionTimers } from "./useReactionTimers";
import { isAbortError, type RequestRegistry } from "./useRequestRegistry";
import type { Action } from "./types";

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
  /** Direct-navigation target resolved through the authorized single-message GET. */
  focusMessageId?: string;
}

/**
 * The initial page, with the deep-linked message guaranteed to be in it.
 *
 * A focus id that is invalid, removed or inaccessible produces the generic page
 * and no error: which of the three it was is not something this client is told,
 * and not something it should let a reader distinguish.
 */
async function fetchPageWithFocus(
  gateway: MessagesGateway,
  focusMessageId: string | undefined,
  signal: AbortSignal,
): Promise<MessagePage> {
  const page = await gateway.fetchPage(undefined, signal);
  if (!focusMessageId || page.messages.some((message) => message.id === focusMessageId)) {
    return page;
  }
  try {
    const focused = await gateway.fetchMessage(focusMessageId, signal);
    return { ...page, messages: insertMessageChronologically(page.messages, focused).messages };
  } catch {
    return page;
  }
}

export function useMessageLoading({
  scope,
  gateway,
  dispatch,
  fallbacks,
  reactionTimers,
  nextCursor,
  loadingMore,
  focusMessageId,
}: Options): MessageLoading {
  const pageAbort = useRef<AbortController | null>(null);
  const olderPageAbort = useRef<AbortController | null>(null);

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

    const controller = new AbortController();
    pageAbort.current = controller;
    dispatch({ type: "loading" });

    fetchPageWithFocus(gateway, focusMessageId, controller.signal).then(
      (page) => {
        if (!scope.isCurrent(loadKey)) return;
        dispatch({ type: "loaded", page: sanitizePage(page) });
      },
      (error: unknown) => {
        if (!scope.isCurrent(loadKey)) return;
        if (isAbortError(error)) return;
        dispatch({ type: "error" });
      },
    );

    return () => {
      controller.abort();
      olderPageAbort.current?.abort();
      fallbacks.abortAll();
      reactionTimers.clearAll();
    };
  }, [dispatch, fallbacks, focusMessageId, gateway, reactionTimers, sanitizePage, scope]);

  useEffect(() => {
    if (!scope.targetId) return;
    return load();
  }, [load, scope.targetId]);

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

    gateway.fetchPage(cursor, controller.signal).then(
      (page) => {
        paging.current.loadingMore = false;
        if (!scope.isCurrent(loadKey)) return;
        dispatch({ type: "prepended", page: sanitizePage(page) });
      },
      (error: unknown) => {
        paging.current.loadingMore = false;
        if (!scope.isCurrent(loadKey)) return;
        if (isAbortError(error)) return;
        dispatch({ type: "prepend_error" });
      },
    );
  }, [dispatch, gateway, sanitizePage, scope]);

  return { retry, loadMore };
}
