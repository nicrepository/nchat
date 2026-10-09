/**
 * What a new messages array asks of the viewport (#492 item 21).
 *
 * #1082 narrowed this to the one thing that is navigation: an own send returns
 * the reader to the present. Whether an inbound message is unread is the read
 * cursor's answer now (see readCursor.test.ts), so no arrival grows a counter
 * here — the regression this file guards is that coupling coming back.
 */

import { describe, expect, it } from "vitest";

import type { LastMutation } from "../../useMessages";
import { decideTailMutation, type TailMutationInput } from "./tailMutation";

function observed(overrides: Partial<TailMutationInput> = {}): TailMutationInput {
  return { lastMutation: "ws_append" as LastMutation, resolved: true, ...overrides };
}

describe("decideTailMutation", () => {
  it("leaves an inbound message to the read cursor", () => {
    expect(decideTailMutation(observed())).toEqual({ kind: "none" });
  });

  it("returns to the bottom for the reader's own send", () => {
    expect(decideTailMutation(observed({ lastMutation: "append" as LastMutation }))).toEqual({
      kind: "return-to-bottom",
    });
  });

  it("does nothing at all before the opening position has settled", () => {
    expect(decideTailMutation(observed({ resolved: false }))).toEqual({ kind: "none" });
    const ownSend = observed({ resolved: false, lastMutation: "append" as LastMutation });
    expect(decideTailMutation(ownSend)).toEqual({ kind: "none" });
  });

  it("does nothing for a new array identity carrying no own send", () => {
    for (const lastMutation of ["prepend", "none", "initial", "ws_append"] as LastMutation[]) {
      expect(decideTailMutation(observed({ lastMutation }))).toEqual({ kind: "none" });
    }
  });
});
