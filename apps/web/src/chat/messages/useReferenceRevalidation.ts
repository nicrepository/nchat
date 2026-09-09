import { useEffect, useMemo } from "react";

import type { Message } from "../chatTypes";
import { messageBatchSize, type MessagesGateway } from "./messagesGateway";
import type { ConversationScope } from "./useConversationScope";
import type { Action } from "./types";

/** How often a mounted reference preview is read back from the server. */
const referenceRevalidationMs = 15_000;

type References = Record<string, NonNullable<Message["reference"]>>;

/**
 * One answer per preview asked about.
 *
 * A preview the server did not answer for is reported as unavailable rather
 * than left as it was: silence is how a revoked or newly inaccessible source
 * reaches this client, so failing closed is the only safe reading of it.
 */
function referenceAnswers(messageIDs: string[], resolved: References): References {
  return Object.fromEntries(
    messageIDs.map((messageID) => [messageID, resolved[messageID] ?? { available: false }]),
  );
}

interface RevalidationWindow {
  close(): void;
}

interface WindowOptions {
  gateway: MessagesGateway;
  messageIDs: string[];
  dispatch: (action: Action) => void;
  /** Whether the conversation this window was opened for is still on screen. */
  isCurrent: () => boolean;
}

/**
 * The polling window that keeps mounted reference previews honest.
 *
 * Previews go stale silently: the source can be edited, condemned or made
 * inaccessible without anything reaching this conversation's socket. So they are
 * re-read on an interval, and again whenever the tab comes back — the moment a
 * reader is most likely to be looking at a preview that has been wrong for a
 * while.
 *
 * Answers are accepted only from the newest request: an older batch that
 * resolves late must not undo a newer one, which is what the generation counter
 * refuses.
 */
function openRevalidationWindow({
  gateway,
  messageIDs,
  dispatch,
  isCurrent,
}: WindowOptions): RevalidationWindow {
  let generation = 0;
  let activeController: AbortController | null = null;
  let disposed = false;
  let scheduled = false;

  const accept = (requestGeneration: number, resolved: References) => {
    if (disposed || requestGeneration !== generation || !isCurrent()) return;
    dispatch({ type: "references_refreshed", references: referenceAnswers(messageIDs, resolved) });
  };

  const revalidate = () => {
    generation += 1;
    const requestGeneration = generation;
    activeController?.abort();
    const controller = new AbortController();
    activeController = controller;
    void gateway.resolveReferences(messageIDs, controller.signal).then(
      (references) => accept(requestGeneration, references),
      () => accept(requestGeneration, {}),
    );
  };

  const schedule = () => {
    if (scheduled) return;
    scheduled = true;
    queueMicrotask(() => {
      scheduled = false;
      if (!disposed) revalidate();
    });
  };
  const onVisibilityChange = () => {
    if (document.visibilityState === "visible") schedule();
  };

  const timer = window.setInterval(revalidate, referenceRevalidationMs);
  window.addEventListener("focus", schedule);
  document.addEventListener("visibilitychange", onVisibilityChange);

  return {
    close() {
      disposed = true;
      generation += 1;
      activeController?.abort();
      window.clearInterval(timer);
      window.removeEventListener("focus", schedule);
      document.removeEventListener("visibilitychange", onVisibilityChange);
    },
  };
}

interface Options {
  scope: ConversationScope;
  gateway: MessagesGateway;
  dispatch: (action: Action) => void;
  messages: Message[];
}

/** Keeps the cross-target reference previews on screen current. */
export function useReferenceRevalidation({ scope, gateway, dispatch, messages }: Options): void {
  const referencedMessageIDsKey = useMemo(
    () =>
      messages
        .filter((message) => message.reference)
        .map((message) => message.id)
        .join(","),
    [messages],
  );
  const { targetId, key } = scope;

  useEffect(() => {
    if (!targetId || !referencedMessageIDsKey) return;
    const allMessageIDs = referencedMessageIDsKey.split(",");
    const messageIDs = allMessageIDs.slice(-messageBatchSize);
    const overflowMessageIDs = allMessageIDs.slice(0, -messageBatchSize);
    if (overflowMessageIDs.length > 0) {
      // Beyond the one-request window there is nothing this client can honestly
      // say about a preview, so it fails closed rather than leaving a stale one.
      dispatch({
        type: "references_refreshed",
        references: referenceAnswers(overflowMessageIDs, {}),
      });
    }
    const revalidation = openRevalidationWindow({
      gateway,
      messageIDs,
      dispatch,
      isCurrent: () => scope.isCurrent(key),
    });
    return () => revalidation.close();
  }, [dispatch, gateway, key, referencedMessageIDsKey, scope, targetId]);
}
