/**
 * The one ordering rule the timeline has: messages are kept in stable
 * (createdAt, id) order.
 *
 * Delivery is not ordered — a realtime event, a fallback GET and a focused-page
 * read can all produce a message that belongs before the last one drawn — so
 * every path that adds a message goes through here rather than appending.
 */

import type { Message } from "../chatTypes";

/** Stable total order: oldest first, ties broken by id so it never depends on arrival. */
function compareByCreatedAtThenId(a: Message, b: Message): number {
  if (a.createdAt !== b.createdAt) return a.createdAt < b.createdAt ? -1 : 1;
  if (a.id < b.id) return -1;
  return a.id > b.id ? 1 : 0;
}

export function insertMessageChronologically(
  messages: Message[],
  message: Message,
): { messages: Message[]; isNewer: boolean } {
  // Most messages are newer than everything drawn, so a tail check avoids a
  // full sort in the common case.
  const last = messages.at(-1);
  const isNewer =
    last === undefined ||
    message.createdAt > last.createdAt ||
    (message.createdAt === last.createdAt && message.id > last.id);
  return {
    messages: isNewer
      ? [...messages, message]
      : [...messages, message].sort(compareByCreatedAtThenId),
    isNewer,
  };
}
