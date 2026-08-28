/**
 * Who reacted, as a sentence (issue #496).
 *
 * The server sends a bounded prefix of names with each aggregate — never the
 * whole set, and never a profile — so this renders from state the conversation
 * already loaded: no request per hover, none per badge, none per person.
 *
 * The reader is always "Você" and always first, because that is how a person
 * reads a list they are in. Their own name is not needed to say so, which is why
 * the server never has to single them out.
 */

import type { MessageReaction } from "./chatTypes";

/** How many names are spelled out before the rest becomes a count. */
const maxNamedAuthors = 2;

/**
 * Names to spell out, in the server's order, without the reader and without
 * repeats.
 *
 * Duplicates are possible in the client's copy of the list — a retried toggle,
 * a re-delivered event, an optimistic entry the confirmation has not replaced
 * yet — and a tooltip that says a name twice is a bug the reader can see.
 */
function otherAuthorNames(reaction: MessageReaction, currentUserId: string): string[] {
  const seen = new Set<string>();
  const names: string[] = [];
  for (const user of reaction.users) {
    if (user.userId === currentUserId || seen.has(user.userId)) continue;
    seen.add(user.userId);
    if (user.displayName) names.push(user.displayName);
  }
  return names;
}

function joinAuthors(entries: readonly string[], remaining: number): string {
  if (remaining > 0) return `${entries.join(", ")} e mais ${remaining}`;
  if (entries.length <= 1) return entries.join("");
  return `${entries.slice(0, -1).join(", ")} e ${entries[entries.length - 1]}`;
}

/**
 * "Você", "Você e Caio Almeida", "Álvaro Neto, Caio Almeida e mais 3".
 *
 * The trailing count is derived from the aggregate's own total, so it stays
 * correct when a reactor has no display name to show and when the server sent
 * fewer names than there are people. Returns "" for an aggregate nobody is in,
 * which is a reaction that should not be on screen at all.
 */
export function formatReactionAuthors(reaction: MessageReaction, currentUserId: string): string {
  if (reaction.count <= 0) return "";
  const entries = reaction.reactedByMe ? ["Você"] : [];
  for (const name of otherAuthorNames(reaction, currentUserId)) {
    if (entries.length === maxNamedAuthors) break;
    entries.push(name);
  }
  const remaining = Math.max(reaction.count - entries.length, 0);
  if (entries.length === 0) {
    return remaining === 1 ? "1 pessoa" : `${remaining} pessoas`;
  }
  return joinAuthors(entries, remaining);
}

/**
 * What a screen reader is given for a reaction badge: the emoji, how many
 * people, and who — the same three facts the visual badge and its tooltip carry
 * between them, in one string, so none of it depends on hovering.
 */
export function reactionAccessibleDescription(
  reaction: MessageReaction,
  currentUserId: string,
): string {
  const authors = formatReactionAuthors(reaction, currentUserId);
  return authors ? `${reaction.emoji}: ${authors}` : reaction.emoji;
}
