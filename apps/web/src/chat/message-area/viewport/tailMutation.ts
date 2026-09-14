/**
 * What a new messages array means for a reader who may or may not be at the
 * tail (#492 items 21 and G — issue #834).
 *
 * The array's identity is the signal: the reducer produces a new one on a real
 * mutation and only then, so a change of identity is exactly one message
 * having arrived, even across repeated "ws_append"/"append" values. What that
 * arrival should do, though, is a question about the message and the reader's
 * position — not about React — so it is answered here, purely.
 *
 * The three outcomes are mutually exclusive by construction: lastMutation is a
 * single value, so an own send and an inbound message can never both apply to
 * the same array.
 */

import { isEligibleUnreadMessage, type ViewportPhase } from "../../chatViewportState";
import type { Message } from "../../chatTypes";
import type { LastMutation } from "../../useMessages";

export interface TailMutationInput {
  messages: Message[];
  currentUserId: string;
  lastMutation: LastMutation;
  /** Where the reader is right now. */
  phase: ViewportPhase;
  /** Whether the opening position has been decided; mutations wait for it. */
  resolved: boolean;
}

export type TailMutationResponse =
  /** Nothing to announce and nothing to move. */
  | { kind: "none" }
  /** Grow the pending-count badge: something arrived behind the reader. */
  | { kind: "count-unread" }
  /** An own send: animate back to the present (#492 item 21). */
  | { kind: "return-to-bottom" };

export function decideTailMutation(input: TailMutationInput): TailMutationResponse {
  // Before the opening position is settled there is no "where the reader is"
  // to measure an arrival against, so nothing counts and nothing moves.
  if (!input.resolved) return { kind: "none" };
  if (input.lastMutation === "append") return { kind: "return-to-bottom" };
  // An inbound message only ever grows the badge, and only while the reader is
  // away from the tail — at the tail they are already looking at it.
  if (input.lastMutation !== "ws_append" || input.phase === "AT_BOTTOM") return { kind: "none" };
  const latest = input.messages[input.messages.length - 1];
  const counts = Boolean(latest) && isEligibleUnreadMessage(latest, input.currentUserId);
  return counts ? { kind: "count-unread" } : { kind: "none" };
}
