/**
 * Opening a DM with a message's author (moved out of ChatMessageArea, issue
 * #834).
 *
 * The whole of it: which authors have a request in flight, the one error line
 * a refusal shows, and the abort/generation bookkeeping that keeps a reply
 * arriving after a conversation switch from navigating somewhere nobody asked
 * for. It is one job with one lifetime, so it owns its own state rather than
 * leaving five refs and two effects lying around the page component.
 */

import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";

import { getOrCreateDirectDM } from "../../chatApi";
import type { Message } from "../../chatTypes";

interface Params {
  currentUserId: string;
  /** The conversation on screen; switching it cancels everything in flight. */
  kind: "channel" | "dm";
  targetId: string;
  refreshConversations?: () => void;
  navigate: (path: string) => void;
}

export interface AuthorDMState {
  /** Authors whose DM is currently being resolved, for the pending affordance. */
  openingAuthorDMIds: Set<string>;
  /** The one refusal line, or null. */
  openDMError: string | null;
  openAuthorDM: (message: Message) => void;
}

export function useAuthorDM({
  currentUserId,
  kind,
  targetId,
  refreshConversations,
  navigate,
}: Params): AuthorDMState {
  const [openDMError, setOpenDMError] = useState<string | null>(null);
  const [openingAuthorDMIds, setOpeningAuthorDMIds] = useState<Set<string>>(new Set());
  const openingAuthorDMRef = useRef(new Map<string, AbortController>());
  const authorDMMountedRef = useRef(true);
  const authorDMGenerationRef = useRef(0);
  // The navigate/refresh identities change every render; a ref keeps the async
  // continuations below on the current ones without restarting anything.
  const navigateRef = useRef(navigate);
  const refreshRef = useRef(refreshConversations);
  useLayoutEffect(() => {
    navigateRef.current = navigate;
    refreshRef.current = refreshConversations;
  });

  useEffect(() => {
    authorDMMountedRef.current = true;
    const pendingAuthorDMs = openingAuthorDMRef.current;
    return () => {
      authorDMMountedRef.current = false;
      for (const controller of pendingAuthorDMs.values()) controller.abort();
      pendingAuthorDMs.clear();
    };
  }, []);

  useLayoutEffect(() => {
    const generation = (authorDMGenerationRef.current += 1);
    for (const controller of openingAuthorDMRef.current.values()) controller.abort();
    openingAuthorDMRef.current.clear();
    queueMicrotask(() => {
      if (!authorDMMountedRef.current || authorDMGenerationRef.current !== generation) return;
      setOpeningAuthorDMIds(new Set());
      setOpenDMError(null);
    });
  }, [kind, targetId]);

  const openAuthorDM = useCallback(
    (message: Message) => {
      const recipientId = message.senderId;
      if (
        !recipientId ||
        recipientId === currentUserId ||
        openingAuthorDMRef.current.has(recipientId)
      ) {
        return;
      }
      const controller = new AbortController();
      const generation = authorDMGenerationRef.current;
      openingAuthorDMRef.current.set(recipientId, controller);
      setOpeningAuthorDMIds((current) => new Set(current).add(recipientId));
      setOpenDMError(null);
      /** Whether this reply still belongs to the conversation that asked for it. */
      const isCurrent = () =>
        authorDMMountedRef.current &&
        authorDMGenerationRef.current === generation &&
        openingAuthorDMRef.current.get(recipientId) === controller;
      void getOrCreateDirectDM(recipientId, controller.signal)
        .then(({ conversationId }) => {
          if (!isCurrent()) return;
          refreshRef.current?.();
          navigateRef.current(`/chat/dm/${encodeURIComponent(conversationId)}`);
        })
        .catch((error: unknown) => {
          if (!isCurrent()) return;
          if (!(error instanceof DOMException && error.name === "AbortError")) {
            setOpenDMError("NÃ£o foi possÃ­vel abrir a conversa. Tente novamente.");
          }
        })
        .finally(() => {
          if (openingAuthorDMRef.current.get(recipientId) !== controller) return;
          openingAuthorDMRef.current.delete(recipientId);
          if (!authorDMMountedRef.current) return;
          setOpeningAuthorDMIds((current) => {
            const next = new Set(current);
            next.delete(recipientId);
            return next;
          });
        });
    },
    [currentUserId],
  );

  return { openingAuthorDMIds, openDMError, openAuthorDM };
}
