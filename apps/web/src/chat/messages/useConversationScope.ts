import { useCallback, useLayoutEffect, useMemo, useRef } from "react";

import type { Message } from "../chatTypes";
import { conversationKey, type ConversationTarget, type MessagesState } from "./types";

/**
 * Everything an async continuation needs to know about the conversation it
 * started in.
 *
 * Two invariants live here, and both exist because a request can outlive the
 * conversation that issued it:
 *
 *  - **staleness.** A completion is applied only while `isCurrent` still agrees
 *    the key it was issued for is the conversation on screen. The mirror behind
 *    it is written in a layout effect with no dependency list, so it is up to
 *    date synchronously in the same JS task as the render, before any microtask
 *    — a POST that resolves right after a target change is detected reliably
 *    regardless of effect scheduling;
 *  - **tombstones.** A message this client has seen deleted must not come back
 *    from a read that was already in flight when it was deleted. Every message
 *    entering the timeline is sanitised against the tombstones recorded for the
 *    current target, which are dropped when the target changes.
 */
export interface ConversationScope {
  readonly kind: "channel" | "dm";
  readonly targetId: string;
  /** The key naming the conversation this render belongs to. */
  readonly key: string;
  /** True while `key` still names the conversation on screen. */
  isCurrent(key: string): boolean;
  isRendered(messageId: string): boolean;
  /** The messages last committed, for callbacks reading them after an await. */
  messages(): Message[];
  /** The reply target last committed. */
  replyTo(): Message | null;
  /** A message rewritten as withdrawn when this client knows it was deleted. */
  sanitize(message: Message): Message;
  rememberDeleted(messageId: string, deletedAt: string | null): void;
  /** Forgets the tombstones of a previous conversation. */
  retarget(key: string): void;
}

export function useConversationScope(
  target: ConversationTarget,
  state: MessagesState,
): ConversationScope {
  const { kind, targetId } = target;
  const key = conversationKey(target);

  const latest = useRef({
    target: key,
    messages: state.messages,
    replyTo: state.replyTo,
  });
  useLayoutEffect(() => {
    latest.current.target = `${kind}:${targetId}`;
    latest.current.messages = state.messages;
    latest.current.replyTo = state.replyTo;
  });

  const tombstones = useRef<Map<string, string | null>>(new Map());
  const tombstoneTarget = useRef(key);

  const sanitize = useCallback((message: Message): Message => {
    if (tombstones.current.has(message.id)) {
      return {
        ...message,
        bodyText: "",
        quoted: undefined,
        reactions: [],
        isRemoved: true,
        status: "deleted",
        deletedAt: tombstones.current.get(message.id) ?? message.deletedAt,
      };
    }
    const quoted = message.quoted;
    if (quoted && tombstones.current.has(quoted.id)) {
      return {
        ...message,
        quoted: {
          ...quoted,
          bodyText: "",
          isRemoved: true,
          deletedAt: tombstones.current.get(quoted.id) ?? quoted.deletedAt,
        },
      };
    }
    return message;
  }, []);

  return useMemo<ConversationScope>(
    () => ({
      kind,
      targetId,
      key: `${kind}:${targetId}`,
      isCurrent: (candidate) => latest.current.target === candidate,
      isRendered: (messageId) =>
        latest.current.messages.some((message) => message.id === messageId),
      messages: () => latest.current.messages,
      replyTo: () => latest.current.replyTo,
      sanitize,
      rememberDeleted(messageId, deletedAt) {
        tombstones.current.set(messageId, deletedAt);
      },
      retarget(candidate) {
        if (tombstoneTarget.current === candidate) return;
        tombstones.current.clear();
        tombstoneTarget.current = candidate;
      },
    }),
    [kind, targetId, sanitize],
  );
}
