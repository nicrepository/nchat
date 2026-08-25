import { describe, expect, it } from "vitest";

import {
  actionsTriggerLabel,
  conversationActions,
  conversationTargetKind,
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
  muted: false,
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

  // A 1:1's name is the counterpart's, resolved per viewer, so there is nothing
  // to rename. A group's is its own title and any participant may change it —
  // participation is the whole authority, since chat.dm_members.role is
  // CHECK-closed to 'member'.
  it("never offers rename for a DM, and always offers it for a group", () => {
    expect(
      conversationActions(target({ kind: "dm", canRename: true })).map((a) => a.id),
    ).not.toContain("rename");
    const groupRename = conversationActions(target({ kind: "group" })).find(
      (action) => action.id === "rename",
    );
    expect(groupRename?.label).toBe("Renomear grupo");
  });

  // Archive, mute and leaving have no backend at all in chat-service. A menu
  // item for any of them would be a lie, and "Sair" on a 1:1 would be a lie the
  // product could never make true.
  // Archiving and hiding have no backend at all in chat-service. A menu item for
  // either would be a promise nothing can keep, so neither may ever appear.
  it("offers no action the domain does not implement", () => {
    const everything = (["channel", "dm", "group"] as const).flatMap((kind) =>
      [true, false].flatMap((pinned) =>
        [true, false].flatMap((hasUnread) =>
          [true, false].flatMap((muted) =>
            conversationActions(target({ kind, pinned, hasUnread, muted, canRename: true })),
          ),
        ),
      ),
    );
    const ids = new Set(everything.map((action) => action.id));
    for (const absent of ["archive", "hide", "delete"]) {
      expect([...ids]).not.toContain(absent);
    }
    expect([...ids].sort()).toEqual([
      "details",
      "leave",
      "mark-read",
      "mute",
      "pin",
      "rename",
      "unmute",
      "unpin",
    ]);
  });
});

describe("conversationTargetKind", () => {
  // A group is a chat.dm_conversations row, so it pins through the DM endpoint.
  // Sending it to the channel endpoint would be a 404 on a conversation the user
  // can plainly see.
  it("routes a group's pin through the DM endpoint, like a 1:1", () => {
    expect(conversationTargetKind("channel")).toBe("channel");
    expect(conversationTargetKind("dm")).toBe("dm");
    expect(conversationTargetKind("group")).toBe("dm");
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

// ── The full matrix (issue #527) ─────────────────────────────────────────────

describe("conversationActions — the product matrix", () => {
  const ids = (t: Partial<ConversationTarget>) =>
    conversationActions(target(t)).map((action) => action.id);

  it("offers every applicable action on an ordinary channel", () => {
    expect(ids({ kind: "channel", canRename: true, hasUnread: true })).toEqual([
      "pin",
      "mark-read",
      "mute",
      "rename",
      "details",
      "leave",
    ]);
  });

  // The general channel is structural: it is where everyone is reachable by
  // construction, so it cannot be renamed, silenced or left — by anybody. The
  // backend refuses all three in SQL; this is the UI not offering them.
  it("offers only the structural-safe actions on the general channel", () => {
    expect(ids({ kind: "channel", isGeneral: true, canRename: true, hasUnread: true })).toEqual([
      "pin",
      "mark-read",
      "details",
    ]);
  });

  it("offers every applicable action on a group", () => {
    expect(ids({ kind: "group", hasUnread: true })).toEqual([
      "pin",
      "mark-read",
      "mute",
      "rename",
      "details",
      "leave",
    ]);
  });

  // A 1:1 conversation has no membership to leave and no title to rename.
  it("offers neither rename nor leave on a 1:1 conversation", () => {
    expect(ids({ kind: "dm", hasUnread: true })).toEqual(["pin", "mark-read", "mute", "details"]);
  });

  it("toggles the mute item against the persisted preference", () => {
    expect(ids({ muted: false })).toContain("mute");
    expect(ids({ muted: true })).toContain("unmute");
    expect(ids({ muted: true })).not.toContain("mute");
  });

  it("labels details for what the panel actually shows", () => {
    const label = (kind: ConversationTarget["kind"]) =>
      conversationActions(target({ kind })).find((action) => action.id === "details")?.label;
    expect(label("channel")).toBe("Detalhes do canal");
    expect(label("group")).toBe("Detalhes do grupo");
    expect(label("dm")).toBe("Detalhes da conversa");
  });

  // Leaving is the only destructive action, and the only one allowed to be drawn
  // in destructive colour.
  it("marks only leaving destructive", () => {
    const actions = conversationActions(
      target({ kind: "channel", canRename: true, hasUnread: true }),
    );
    const destructive = actions.filter((action) => action.destructive);
    expect(destructive.map((action) => action.id)).toEqual(["leave"]);
    expect(destructive[0]?.group).toBe("destructive");
  });

  // Groups render in a fixed order so the separators land between them.
  it("keeps the three groups in order", () => {
    const groups = conversationActions(
      target({ kind: "channel", canRename: true, hasUnread: true }),
    ).map((action) => action.group);
    expect(groups).toEqual(["frequent", "frequent", "frequent", "manage", "manage", "destructive"]);
  });

  it("gives every action an icon from the shared set", () => {
    for (const kind of ["channel", "dm", "group"] as const) {
      for (const action of conversationActions(
        target({ kind, hasUnread: true, canRename: true }),
      )) {
        expect(action.icon).toBeTruthy();
      }
    }
  });
});
