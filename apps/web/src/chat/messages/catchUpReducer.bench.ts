import { bench, describe } from "vitest";

import type { Message } from "../chatTypes";
import { reducer } from "./reducer";
import { initialState, type MessagesState } from "./types";

function message(n: number, updatedAt: string): Message {
  return {
    id: `m-${String(n).padStart(6, "0")}`,
    senderId: "user-2",
    senderDisplayName: "Bea",
    senderEmail: "",
    kind: "user",
    bodyText: `mensagem ${n}`,
    bodyFormat: "v3",
    isRemoved: false,
    status: "active",
    createdAt: new Date(Date.UTC(2026, 0, 1) + n * 1000).toISOString(),
    updatedAt,
    isEdited: false,
    editCount: 0,
    reactions: [],
    isFavorited: false,
    isForwarded: false,
  } as Message;
}

function workload(size: number) {
  const held = Array.from({ length: size }, (_, i) => message(i, "2026-01-01T00:00:00.000Z"));
  const read = Array.from({ length: size }, (_, i) => message(i, "2026-02-01T00:00:00.000Z"));
  const state: MessagesState = reducer(initialState, {
    type: "loaded",
    page: { messages: held, nextCursor: "" },
  });
  return { state, read };
}

for (const size of [1_000, 5_000, 10_000]) {
  describe(`catch-up stitch of ${size} held messages`, () => {
    const { state, read } = workload(size);
    bench("stitch", () => {
      reducer(state, {
        type: "caught_up",
        page: { messages: read, nextCursor: "" },
        replace: false,
        journal: [],
      });
    });
  });
}
