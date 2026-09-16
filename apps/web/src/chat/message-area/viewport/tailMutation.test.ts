/**
 * Characterization tests for what a new messages array means to a reader who
 * may or may not be at the tail (#492 items 21 and G).
 *
 * These freeze the behaviour useTailFollow had before issue #834's review
 * asked for the decision to be separated from its application — they are not a
 * new specification. The rendered-timeline tests in ChatMessageArea.test.tsx
 * still cover the badge and the scroll that follow from each answer here.
 */

import { describe, expect, it } from "vitest";

import type { Message } from "../../chatTypes";
import type { LastMutation } from "../../useMessages";
import { decideTailMutation, type TailMutationInput } from "./tailMutation";

const ME = "u-me";
const THEM = "u-them";

function message(id: string, senderId = THEM, status = "active"): Message {
  return { id, senderId, status } as unknown as Message;
}

function observed(overrides: Partial<TailMutationInput> = {}): TailMutationInput {
  return {
    messages: [message("m1"), message("m2")],
    currentUserId: ME,
    lastMutation: "ws_append" as LastMutation,
    phase: "READING_HISTORY",
    resolved: true,
    ...overrides,
  };
}

describe("decideTailMutation", () => {
  it("counts an inbound message that arrived while the reader was in history", () => {
    expect(decideTailMutation(observed())).toEqual({ kind: "count-unread" });
  });

  it("does not count an inbound message the reader is already looking at", () => {
    // At the tail the message is on screen, so there is nothing pending.
    expect(decideTailMutation(observed({ phase: "AT_BOTTOM" }))).toEqual({ kind: "none" });
  });

  it("does not count the reader's own message", () => {
    const messages = [message("m1"), message("m2", ME)];
    expect(decideTailMutation(observed({ messages }))).toEqual({ kind: "none" });
  });

  it("does not count a message that is no longer active", () => {
    const messages = [message("m1"), message("m2", THEM, "removed")];
    expect(decideTailMutation(observed({ messages }))).toEqual({ kind: "none" });
  });

  it("returns to the bottom for the reader's own send", () => {
    expect(decideTailMutation(observed({ lastMutation: "append" as LastMutation }))).toEqual({
      kind: "return-to-bottom",
    });
  });

  it("returns to the bottom for an own send even while reading history", () => {
    // #492 item 21: sending is an explicit "take me back to the present".
    const decision = decideTailMutation(
      observed({ lastMutation: "append" as LastMutation, phase: "READING_HISTORY" }),
    );
    expect(decision).toEqual({ kind: "return-to-bottom" });
  });

  it("does nothing at all before the opening position has settled", () => {
    expect(decideTailMutation(observed({ resolved: false }))).toEqual({ kind: "none" });
    const ownSend = observed({ resolved: false, lastMutation: "append" as LastMutation });
    expect(decideTailMutation(ownSend)).toEqual({ kind: "none" });
  });

  it("does nothing for a new array identity carrying no eligible mutation", () => {
    // The review's explicit case: the array changed, but neither an inbound
    // message nor an own send explains it — so the badge must not grow and
    // nothing may scroll.
    for (const lastMutation of ["prepend", "none", "initial"] as LastMutation[]) {
      expect(decideTailMutation(observed({ lastMutation }))).toEqual({ kind: "none" });
    }
  });

  it("does nothing when a prepended page arrives while reading history", () => {
    const decision = decideTailMutation(
      observed({ lastMutation: "prepend" as LastMutation, phase: "READING_HISTORY" }),
    );
    expect(decision).toEqual({ kind: "none" });
  });

  it("is a pure function of the observation, so a repeat gives the same answer", () => {
    // The hook fires this once per array identity; the guard that makes that
    // true is setCountedMessages, not this function, so the same observation
    // must never answer differently the second time.
    const input = observed();
    expect(decideTailMutation(input)).toEqual(decideTailMutation(input));
  });

  it("does not count anything when the conversation has no messages", () => {
    expect(decideTailMutation(observed({ messages: [] }))).toEqual({ kind: "none" });
  });
});
