/**
 * olderPagesSettled — the "an older page came back" signal (#1088).
 *
 * The opening position's bounded search waits on it, so it has to move for
 * every page that comes back, including the ones that change nothing else.
 */

import { describe, expect, it } from "vitest";

import type { Message } from "../chatTypes";
import { reducer } from "./reducer";
import { initialState, type MessagesState } from "./types";

function message(id: string): Message {
  return { id } as unknown as Message;
}

function loaded(...messages: Message[]): MessagesState {
  return reducer(initialState, { type: "loaded", page: { messages, nextCursor: "c1" } });
}

describe("olderPagesSettled", () => {
  it("starts from zero on every load", () => {
    const paged = reducer(loaded(message("m2")), {
      type: "prepended",
      page: { messages: [message("m1")], nextCursor: "c2" },
    });
    expect(paged.olderPagesSettled).toBe(1);

    expect(
      reducer(paged, { type: "loaded", page: { messages: [], nextCursor: "" } }),
    ).toMatchObject({ olderPagesSettled: 0 });
  });

  it("counts a page that only repeated loaded messages, though the array is unchanged", () => {
    const before = loaded(message("m1"), message("m2"));
    const after = reducer(before, {
      type: "prepended",
      page: { messages: [message("m1")], nextCursor: "c2" },
    });

    expect(after.messages).toBe(before.messages);
    expect(after).toMatchObject({ nextCursor: "c2", olderPagesSettled: 1 });
  });

  it("counts an empty page, and one that left the cursor where it was", () => {
    const after = reducer(loaded(message("m2")), {
      type: "prepended",
      page: { messages: [], nextCursor: "c1" },
    });
    expect(after).toMatchObject({ nextCursor: "c1", olderPagesSettled: 1 });
  });

  it("counts a page that failed, so nothing waits on it forever", () => {
    const after = reducer(loaded(message("m2")), { type: "prepend_error" });
    expect(after).toMatchObject({ loadingMore: false, olderPagesSettled: 1 });
  });

  it("does not count a page that is merely on its way", () => {
    expect(reducer(loaded(message("m2")), { type: "prepending" })).toMatchObject({
      loadingMore: true,
      olderPagesSettled: 0,
    });
  });
});
