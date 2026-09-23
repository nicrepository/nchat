import { describe, expect, it } from "vitest";

import { adjacentSidebarConversation } from "./sidebarConversationNavigation";

describe("adjacentSidebarConversation", () => {
  it("moves through the sidebar's rendered order", () => {
    const visibleConversations = [
      { kind: "channel" as const, id: "a" },
      { kind: "dm" as const, id: "b" },
      { kind: "dm" as const, id: "c" },
    ];

    expect(
      adjacentSidebarConversation(visibleConversations, { kind: "channel", id: "a" }, 1),
    ).toEqual({
      kind: "dm",
      id: "b",
    });
    expect(adjacentSidebarConversation(visibleConversations, { kind: "dm", id: "c" }, -1)).toEqual({
      kind: "dm",
      id: "b",
    });
  });
});
