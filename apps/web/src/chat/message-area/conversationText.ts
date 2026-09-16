/**
 * The conversation's derived text and shortlists (moved out of
 * ChatMessageArea, issue #834).
 *
 * Pure functions of already-available values: no React, no lifecycle, no DOM.
 * That is what makes each of them answerable by a plain unit test rather than
 * by rendering a conversation.
 */

import { recentEmojis, type EmojiUsage } from "../emoji/emojiUsage";

/** How many emoji the row above a message offers before "Mais reações". */
const quickReactionCount = 3;

/**
 * "Fulano está digitando…" for one or two people, an aggregate count beyond
 * that — never one line per typist, which is what would make the area jump
 * around under a busy channel. `null` means nothing to show.
 */
export function typingIndicatorText(
  userIds: readonly string[],
  namesByUserId: ReadonlyMap<string, string>,
): string | null {
  if (userIds.length === 0) return null;
  const label = (id: string) => namesByUserId.get(id) ?? "Alguém";
  if (userIds.length === 1) return `${label(userIds[0])} está digitando…`;
  if (userIds.length === 2) return `${label(userIds[0])} e ${label(userIds[1])} estão digitando…`;
  return `${userIds.length} pessoas estão digitando…`;
}

/**
 * The quick-reaction row: what this person actually reaches for, backfilled
 * from the server's curated shortlist so a brand-new account still has one
 * (issue #496).
 *
 * The shortlist is the only server-provided part, and it is a suggestion: what
 * a reaction may be is the catalog's decision, made again on the server for
 * every toggle.
 */
export function quickReactionEmojis(usage: EmojiUsage, serverShortlist: string[]): string[] {
  const candidates = [...recentEmojis(usage, quickReactionCount), ...serverShortlist];
  return [...new Set(candidates)].slice(0, quickReactionCount);
}
