import { describe, expect, it } from "vitest";

import {
  actionsTriggerLabel,
  conversationActions,
  pinTargetKind,
  type ConversationTarget,
} from "./conversationActions";

// The menu is derived from the target, once, for all four kinds of conversation
// (issue #527). These are the rules that keep it honest: no action appears
// unless the product actually implements it for that kind of target.

const target = (overrides: Partial<ConversationTarget> = {}): ConversationTarget => ({
  kind: "channel",
  id: "ch-1",
  name: "geral",
  pinned: false,
  hasUnread: false,
  ...overrides,
});

describe("conversationActions", () => {
  it("offers pinning to every kind of conversation", () => {
    for (const kind of ["channel", "dm", "group"] as const) {
      const ids = conversationActions(target({ kind })).map((action) => action.id);
      expect(ids).toContain("pin");
    }
  });

  it("offers exactly one of pin/unpin, matching the persisted state", () => {
    expect(conversationActions(target({ pinned: false })).map((a) => a.id)).toContain("pin");
    expect(conversationActions(target({ pinned: false })).map((a) => a.id)).not.toContain("unpin");
    expect(conversationActions(target({ pinned: true })).map((a) => a.id)).toContain("unpin");
    expect(conversationActions(target({ pinned: true })).map((a) => a.id)).not.toContain("pin");
  });

  it("offers marking as read only when there is something unread", () => {
    expect(conversationActions(target({ hasUnread: false })).map((a) => a.id)).not.toContain(
      "mark-read",
    );
    expect(conversationActions(target({ hasUnread: true })).map((a) => a.id)).toContain(
      "mark-read",
    );
  });

  // Rename exists for channels only, and only when the server said so. The flag
  // is presentation: PATCH /api/chat/channels/{id} decides for itself.
  it("offers rename only for a channel the server said may be renamed", () => {
    expect(conversationActions(target({ canRename: true })).map((a) => a.id)).toContain("rename");
    expect(conversationActions(target({ canRename: false })).map((a) => a.id)).not.toContain(
      "rename",
    );
    expect(conversationActions(target({})).map((a) => a.id)).not.toContain("rename");
  });

  it("never offers rename for a DM or a group, whatever the flag says", () => {
    for (const kind of ["dm", "group"] as const) {
      const ids = conversationActions(target({ kind, canRename: true })).map((a) => a.id);
      expect(ids).not.toContain("rename");
    }
  });

  // Archive, mute and leaving have no backend at all in chat-service. A menu
  // item for any of them would be a lie, and "Sair" on a 1:1 would be a lie the
  // product could never make true.
  it("offers no action the domain does not implement", () => {
    const everything = (["channel", "dm", "group"] as const).flatMap((kind) =>
      [true, false].flatMap((pinned) =>
        [true, false].flatMap((hasUnread) =>
          conversationActions(target({ kind, pinned, hasUnread, canRename: true })),
        ),
      ),
    );
    const ids = new Set(everything.map((action) => action.id));
    for (const absent of ["leave", "archive", "mute", "details"]) {
      expect([...ids]).not.toContain(absent);
    }
    expect([...ids].sort()).toEqual(["mark-read", "pin", "rename", "unpin"]);
  });
});

describe("pinTargetKind", () => {
  // A group is a chat.dm_conversations row, so it pins through the DM endpoint.
  // Sending it to the channel endpoint would be a 404 on a conversation the user
  // can plainly see.
  it("routes a group's pin through the DM endpoint, like a 1:1", () => {
    expect(pinTargetKind("channel")).toBe("channel");
    expect(pinTargetKind("dm")).toBe("dm");
    expect(pinTargetKind("group")).toBe("dm");
  });
});

describe("actionsTriggerLabel", () => {
  it("names the conversation and its kind, never just the ellipsis", () => {
    expect(actionsTriggerLabel(target({ kind: "channel", name: "geral" }))).toBe(
      "Mais opções para canal geral",
    );
    expect(actionsTriggerLabel(target({ kind: "group", name: "Equipe Infra" }))).toBe(
      "Mais opções para grupo Equipe Infra",
    );
    expect(actionsTriggerLabel(target({ kind: "dm", name: "Juliane" }))).toBe(
      "Mais opções para conversa com Juliane",
    );
  });
});
