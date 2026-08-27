/**
 * Keeps a reaction badge on screen while it is leaving (issue #496, CQ round 3).
 *
 * Removing the last reaction used to make the badge vanish between two frames,
 * which reads as a glitch rather than as an action. React unmounts on state
 * change and has no opinion about exit animations, so the badge has to outlive
 * its own data for as long as the animation runs.
 *
 * What ends the exit is the animation itself — `animationend` on the badge —
 * never a timer chosen to match a CSS duration. That keeps the two in step under
 * reduced motion, where the same animation is one millisecond long, and under a
 * throttled tab, where a timer would fire while the paint had not happened.
 */

import { useState } from "react";

import type { MessageReaction } from "./chatTypes";

export interface RenderedReaction {
  reaction: MessageReaction;
  /** True while the badge is animating out and must not accept a click. */
  exiting: boolean;
}

export interface ReactionPresence {
  rendered: RenderedReaction[];
  /** Called by a badge when its exit animation has finished. */
  onExited: (emoji: string) => void;
}

interface Leaving {
  reaction: MessageReaction;
  /** Where the badge sat before it left, so it does not jump to the end. */
  index: number;
}

function withoutEmoji(leaving: Leaving[], emoji: string): Leaving[] {
  return leaving.filter((item) => item.reaction.emoji !== emoji);
}

/**
 * The badges to draw: the live reactions, plus the ones still animating out at
 * the position they held.
 */
function renderList(reactions: MessageReaction[], leaving: Leaving[]): RenderedReaction[] {
  const rendered: RenderedReaction[] = reactions.map((reaction) => ({ reaction, exiting: false }));
  for (const item of leaving) {
    rendered.splice(Math.min(item.index, rendered.length), 0, {
      reaction: item.reaction,
      exiting: true,
    });
  }
  return rendered;
}

/**
 * Which badges have just left, and which have come back.
 *
 * A reaction re-added before its exit finished is simply dropped from the
 * leaving set: the live list already contains it, so it stops animating out and
 * is drawn once — no flicker, no duplicate. The same path covers an optimistic
 * removal the server refused, because a rollback is a re-add.
 */
function nextLeaving(
  previous: MessageReaction[],
  current: MessageReaction[],
  leaving: Leaving[],
): Leaving[] {
  const live = new Set(current.map((reaction) => reaction.emoji));
  const kept = leaving.filter((item) => !live.has(item.reaction.emoji));
  const departed = previous
    .map((reaction, index) => ({ reaction, index }))
    .filter(
      (item) =>
        !live.has(item.reaction.emoji) &&
        !kept.some((existing) => existing.reaction.emoji === item.reaction.emoji),
    );
  return [...kept, ...departed];
}

/**
 * An order-sensitive identity for the leaving set. Emoji are unique within one
 * message's reactions, so joining them is enough to tell two sets apart without
 * serialising the aggregates themselves.
 */
function leavingKey(leaving: Leaving[]): string {
  return leaving.map((item) => `${item.index}:${item.reaction.emoji}`).join("\u0000");
}

export function useReactionPresence(reactions: MessageReaction[]): ReactionPresence {
  const [previous, setPrevious] = useState(reactions);
  const [leaving, setLeaving] = useState<Leaving[]>([]);

  // Adjusted during render rather than in an effect: a badge that has to keep
  // being drawn must be in the very first commit after its data went away, or
  // the reader sees the gap the animation exists to fill. This is React's
  // "adjust state when props change" pattern, guarded so it runs only when the
  // list identity actually changes.
  let current = leaving;
  if (previous !== reactions) {
    const computed = nextLeaving(previous, reactions, leaving);
    setPrevious(reactions);
    // Compared by content, not by size. One badge replaced by another — 🎉
    // leaves in the same render 🚀 arrives — keeps the count at one while the
    // set is entirely different, and a length check would leave the departed
    // badge drawn alongside the live one under the same key.
    if (leavingKey(computed) !== leavingKey(leaving)) {
      current = computed;
      setLeaving(computed);
    }
  }

  return {
    rendered: renderList(reactions, current),
    onExited: (emoji: string) => setLeaving((items) => withoutEmoji(items, emoji)),
  };
}
