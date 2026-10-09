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
 * #1082: an inbound message no longer grows a counter here. Whether it is
 * unread is the read cursor's answer — derived from the messages themselves —
 * so the only thing an arrival can ask of the viewport is the own-send return.
 */

import type { LastMutation } from "../../useMessages";

export interface TailMutationInput {
  lastMutation: LastMutation;
  /** Whether the opening position has been decided; mutations wait for it. */
  resolved: boolean;
}

export type TailMutationResponse =
  /** Nothing to move. */
  | { kind: "none" }
  /** An own send: animate back to the present (#492 item 21). */
  | { kind: "return-to-bottom" };

export function decideTailMutation(input: TailMutationInput): TailMutationResponse {
  // Before the opening position is settled there is no "where the reader is"
  // to measure an arrival against, so nothing counts and nothing moves.
  if (!input.resolved) return { kind: "none" };
  return input.lastMutation === "append" ? { kind: "return-to-bottom" } : { kind: "none" };
}
