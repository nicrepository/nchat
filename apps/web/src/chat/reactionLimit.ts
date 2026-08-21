export const maxReactionsPerUserPerMessage = 5;

type Reaction = { emoji: string; reactedByMe: boolean };

/**
 * A selected emoji is always allowed so a user can remove it, including for
 * legacy messages that already exceed the current ceiling.
 */
export function canToggleReaction(reactions: readonly Reaction[], emoji: string): boolean {
  if (reactions.some((reaction) => reaction.emoji === emoji && reaction.reactedByMe)) return true;
  return (
    reactions.filter((reaction) => reaction.reactedByMe).length < maxReactionsPerUserPerMessage
  );
}
