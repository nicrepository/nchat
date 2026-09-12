import { useCallback, useEffect, useRef, useState } from "react";

import {
  acknowledgeMessage,
  fetchMessageAcknowledgement,
  fetchMessageAcknowledgements,
} from "../chatApi";
import type { Message, MessageAcknowledgement } from "../chatTypes";
import type { ConversationScope } from "./useConversationScope";
import type { RequestRegistry } from "./useRequestRegistry";

/**
 * Per-recipient acknowledgement for the messages on screen (issue #824).
 *
 * The server is the source of truth and this hook holds no state machine of its
 * own: it caches what the two endpoints answered, keyed by message, and replaces
 * an entry only with a newer server answer. There is deliberately no optimistic
 * transition — confirming receipt is one round trip whose whole result is the
 * authoritative summary, so showing a guess first would add a rollback path for
 * no perceptible gain.
 *
 * Nothing here is triggered by reading. #820 separates DELIVERED, READ and
 * ACKNOWLEDGED, so rendering a message, scrolling past it or marking the
 * conversation read must never produce a confirmation — only `acknowledge`,
 * called from an explicit action, posts anything.
 */
export interface Acknowledgements {
  /** What is known about each message that asked for confirmation. */
  summaries: Record<string, MessageAcknowledgement>;
  /** The message whose confirmation is currently in flight, if any. */
  pendingId: string | null;
  /** Set when the last attempt failed; cleared by the next one. */
  error: string | null;
  /** Confirms receipt of one message. Safe to call twice. */
  acknowledge(messageId: string): void;
  /**
   * Re-reads every summary this view holds. Called when a subscription comes
   * back ready, which is what makes a reconnect reconcile rather than trust a
   * cache that may have gone stale while the socket was down.
   */
  reconcile(): void;
  /**
   * Re-reads one message's summary, because realtime said it changed (issue
   * #824).
   *
   * Targeted on purpose: the event names the message, so exactly that message is
   * re-read. Reconciling the whole conversation on every acknowledgement would
   * turn one person's click into a request per asking message on everybody's
   * screen — the read amplification this feature already has to be careful about.
   *
   * The two are complementary: this keeps an open session current, and
   * `reconcile` above catches whatever a closed one missed.
   */
  reconcileOne(messageId: string): void;
}

interface Options {
  scope: ConversationScope;
  /**
   * The messages this render is showing.
   *
   * Passed explicitly rather than read from `scope.messages()`, which is a ref
   * the conversation updates in a layout effect *after* the render that
   * introduced them. Reading it here would give this hook the previous list on
   * exactly the render where a message first appears, and nothing would
   * re-render to correct it — so a freshly loaded page would sit without its
   * summaries until some unrelated state change happened to come along. Every
   * other hook that reacts to the list takes it the same way; the scope stays
   * for what it is good at, which is telling a late answer that its
   * conversation is gone.
   */
  messages: Message[];
  /** Registered here so a target change or unmount aborts every read. */
  requests: RequestRegistry;
}

const summaryKey = (messageId: string) => `acknowledgement:${messageId}`;

/**
 * The registry key of the page read.
 *
 * One key, not one per page: a second batch started while the first is in
 * flight takes it over and aborts it, which is what a rapid scroll or a
 * reconnect landing on top of a load should cost — one request, the latest one.
 */
const batchKey = "acknowledgement:batch";

export const acknowledgeErrorMessage = "Não foi possível confirmar o recebimento. Tente novamente.";

/** The messages on screen that asked anybody to confirm. */
function askingMessageIDs(messages: Message[]): string[] {
  return messages.filter((message) => message.acknowledgementRequired).map((message) => message.id);
}

/** Shared so a conversation with nothing cached returns a stable object. */
const noSummaries: Record<string, MessageAcknowledgement> = {};

/**
 * Which ids a conversation has already been asked about.
 *
 * A ref rather than state, and read only inside effects: it must not cause a
 * render of its own, and a synchronous setState from an effect body is the
 * cascading render the lint rule and React's own guidance both warn about.
 * Nothing renders from this — it only decides whether to issue a request.
 */
interface AskedPages {
  key: string;
  ids: Set<string>;
}

export function useAcknowledgements({ scope, messages, requests }: Options): Acknowledgements {
  // The cache carries the conversation it describes rather than being reset by
  // a separate effect: a summary read for channel A must never be shown against
  // channel B, and comparing the key at read time makes that impossible instead
  // of merely unlikely — there is no window in which the old entries are still
  // in state and the new target is already on screen.
  const [cache, setCache] = useState<{
    key: string;
    summaries: Record<string, MessageAcknowledgement>;
  }>({ key: scope.key, summaries: noSummaries });
  // Separate from `summaries` because the two answer different questions: a
  // message the server withheld — not readable, or gone — produces no summary,
  // and treating "no summary" as "not asked yet" would request it again on
  // every render, forever. Asking is the fact worth remembering.
  const asked = useRef<AskedPages>({ key: scope.key, ids: new Set() });
  const [pendingId, setPendingId] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  const summaries = cache.key === scope.key ? cache.summaries : noSummaries;

  const store = useCallback(
    (loadKey: string, summary: MessageAcknowledgement) => {
      if (!scope.isCurrent(loadKey)) return;
      setCache((current) => ({
        key: loadKey,
        // Entries from a previous conversation are dropped rather than merged,
        // which is the same rule the read above applies.
        summaries: {
          ...(current.key === loadKey ? current.summaries : {}),
          [summary.messageId]: summary,
        },
      }));
    },
    [scope],
  );

  /**
   * Reads one message's summary.
   *
   * `force` is what separates filling a gap from reconciling: the first skips a
   * message already known, the second asks again because the cached answer may
   * have been overtaken while this client was not listening.
   */
  const load = useCallback(
    (messageId: string, force: boolean) => {
      const key = summaryKey(messageId);
      if (!force && requests.has(key)) return;
      const controller = requests.start(key);
      const loadKey = scope.key;
      void fetchMessageAcknowledgement(messageId, controller.signal).then(
        (summary) => {
          requests.finish(key, controller);
          if (!controller.signal.aborted) store(loadKey, summary);
        },
        () => {
          // A failed read says nothing about the acknowledgement, so nothing is
          // written: leaving the entry as it was — absent, or the previous
          // answer — is what keeps this from inventing a state the server never
          // reported. An abort and a network failure are the same here, which is
          // why neither is distinguished.
          requests.finish(key, controller);
        },
      );
    },
    [requests, scope, store],
  );

  /**
   * Reads a page's summaries in one request.
   *
   * Which ids is the caller's decision, and the two callers differ on purpose: a
   * page load asks about the messages it does not yet hold, while a reconnect
   * asks about all of them, because the cached answers may have been overtaken
   * while the socket was down.
   */
  const loadPage = useCallback(
    (ids: string[]) => {
      if (ids.length === 0) return;
      const controller = requests.start(batchKey);
      const loadKey = scope.key;
      void fetchMessageAcknowledgements(ids, controller.signal).then(
        (page) => {
          requests.finish(batchKey, controller);
          if (controller.signal.aborted || !scope.isCurrent(loadKey)) return;
          // Merged in one update: a page of twenty answers is one re-render, and
          // an entry the server withheld simply does not appear, which is the
          // same absence a message nobody may read produces.
          setCache((current) => ({
            key: loadKey,
            summaries: {
              ...(current.key === loadKey ? current.summaries : {}),
              ...page,
            },
          }));
        },
        () => {
          // A failed read says nothing about any of these acknowledgements, so
          // nothing is written and whatever was already known still stands. The
          // ids stay marked asked; the next reconnect asks again.
          requests.finish(batchKey, controller);
        },
      );
    },
    [requests, scope],
  );

  // Fill the gaps: the messages on screen that ask for confirmation and have not
  // been asked about yet, requested together. A conversation of ordinary
  // messages — which is almost every conversation — issues no request at all.
  //
  // The dependency is a joined string rather than the array, so a re-render with
  // the same page does not re-run this. The filtering happens inside the effect
  // because it consults the ref above, which must not be read while rendering.
  const askingKey = askingMessageIDs(messages).join(",");
  const conversationKey = scope.key;
  useEffect(() => {
    if (asked.current.key !== conversationKey) {
      asked.current = { key: conversationKey, ids: new Set() };
    }
    const pending = (askingKey ? askingKey.split(",") : []).filter(
      (messageId) => !asked.current.ids.has(messageId),
    );
    if (pending.length === 0) return;
    for (const messageId of pending) asked.current.ids.add(messageId);
    loadPage(pending);
  }, [askingKey, conversationKey, loadPage]);

  const reconcile = useCallback(() => {
    const ids = askingMessageIDs(messages);
    // A reconnect asks again about everything on screen, so the bookkeeping is
    // reset to exactly what is being asked rather than accumulated.
    asked.current = { key: scope.key, ids: new Set(ids) };
    loadPage(ids);
  }, [loadPage, messages, scope]);

  const reconcileOne = useCallback(
    (messageId: string) => {
      // Only for a message this view is actually showing. An event for something
      // scrolled out of the loaded page has nothing to update, and reading it
      // would be a request for a summary nobody is looking at.
      if (!messages.some((message) => message.id === messageId)) return;
      // force: false, so several events for the same message in quick succession
      // share the one read already in flight. The registry keys reads by message,
      // and the answer is authoritative whenever it lands.
      load(messageId, false);
    },
    [load, messages],
  );

  const acknowledge = useCallback(
    (messageId: string) => {
      // One confirmation in flight at a time, and never a second for a message
      // whose first is still running: a double click is one logical action, and
      // the server would answer the second identically anyway.
      if (pendingId) return;
      setPendingId(messageId);
      setError(null);
      const loadKey = scope.key;
      void acknowledgeMessage(messageId).then(
        (summary) => {
          setPendingId(null);
          store(loadKey, summary);
        },
        () => {
          setPendingId(null);
          if (scope.isCurrent(loadKey)) setError(acknowledgeErrorMessage);
        },
      );
    },
    [pendingId, scope, store],
  );

  return { summaries, pendingId, error, acknowledge, reconcile, reconcileOne };
}
