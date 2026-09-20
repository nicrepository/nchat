import { describe, expect, it, vi } from "vitest";

import { ApiRequestError } from "../lib/api";
import type { Channel } from "./chatTypes";
import {
  conversationNameCodePointLength,
  conversationNameMaxCodePoints,
  conversationRenameAction,
  renameErrorMessage,
  renameNameRefusal,
} from "./conversationRename";

function channel(overrides: Partial<Channel> = {}): Channel {
  return {
    id: "ch-1",
    name: "Infraestrutura",
    type: "public",
    canWrite: true,
    ...overrides,
  };
}

/**
 * Who may rename what, from the server's own flags (issue #893).
 *
 * This is the whole capability story for the details panel: the editor renders
 * a control exactly when this returns something, so every "no affordance" rule
 * — the general channel, a revoked capability, a 1:1 — is proven here rather
 * than in a rendering test. None of it is a security boundary: both endpoints
 * re-derive the caller's authority from the session on every call.
 */
describe("conversationRenameAction", () => {
  const renameChannel = vi.fn().mockResolvedValue(undefined);
  const renameGroup = vi.fn().mockResolvedValue(undefined);

  function action(overrides: Partial<Parameters<typeof conversationRenameAction>[0]> = {}) {
    return conversationRenameAction({
      kind: "channel",
      targetId: "ch-1",
      channels: [channel({ canRename: true })],
      renameChannel,
      renameGroup,
      ...overrides,
    });
  }

  it("renames a channel the server said this caller may rename", async () => {
    await action()?.("Plataforma");

    expect(renameChannel).toHaveBeenCalledWith("ch-1", "Plataforma");
    expect(renameGroup).not.toHaveBeenCalled();
  });

  it("offers nothing when the server did not grant the capability", () => {
    expect(action({ channels: [channel({ canRename: false })] })).toBeUndefined();
    // Absent is read as "no", never as "unknown, so allow".
    expect(action({ channels: [channel()] })).toBeUndefined();
  });

  // The workspace's structural general channel, by the server's own is_general
  // and never by its name, its slug or its id. The backend refuses a rename of
  // it in SQL whatever this returns.
  it("offers nothing for the general channel, even with the capability set", () => {
    expect(action({ channels: [channel({ canRename: true, isGeneral: true })] })).toBeUndefined();
  });

  it("offers nothing for a channel that is not in the canonical list", () => {
    expect(action({ targetId: "ch-missing" })).toBeUndefined();
    expect(action({ channels: [] })).toBeUndefined();
  });

  // A group's authority is participation, which every row in this sidebar
  // implies; the store re-derives it inside the transaction regardless.
  it("renames a group through the group mutation", async () => {
    await action({ kind: "group", targetId: "dm-1", channels: [] })?.("Time de Infra");

    expect(renameGroup).toHaveBeenCalledWith("dm-1", "Time de Infra");
  });

  // A 1:1's name is the counterpart's, resolved per viewer: there is nothing
  // to rename, and the endpoint requires type = 'group' anyway.
  it("never offers a rename for a 1:1 conversation", () => {
    expect(action({ kind: "direct", targetId: "dm-1", channels: [] })).toBeUndefined();
  });

  it("offers nothing without a target or a kind", () => {
    expect(action({ targetId: "" })).toBeUndefined();
    expect(action({ kind: null })).toBeUndefined();
  });

  // A host that wired no mutation — a partial outlet context — shows no
  // control rather than a control that cannot do anything.
  it("offers nothing when the matching mutation is absent", () => {
    expect(action({ renameChannel: undefined })).toBeUndefined();
    expect(
      action({ kind: "group", targetId: "dm-1", channels: [], renameGroup: undefined }),
    ).toBeUndefined();
  });
});

describe("renameErrorMessage", () => {
  it("names the aggregate the caller was acting on", () => {
    const forbidden = new ApiRequestError(403, "forbidden", "forbidden");

    expect(renameErrorMessage("channel", forbidden)).toMatch(/canal/);
    expect(renameErrorMessage("group", forbidden)).toMatch(/grupo/);
  });

  it("maps each refusal the endpoints produce", () => {
    const cases: [number, RegExp][] = [
      [400, /nome válido/],
      [404, /não está mais disponível/],
      [409, /mudou enquanto você editava/],
      [429, /Muitas solicitações/],
      [0, /Sem conexão/],
    ];
    for (const [status, expected] of cases) {
      expect(renameErrorMessage("channel", new ApiRequestError(status, "e", "e"))).toMatch(
        expected,
      );
    }
  });

  // The server's own message is never surfaced: it echoes caller-controlled
  // text and can describe a resource this caller may not see.
  it("falls back to generic copy and never repeats the server's message", () => {
    const leaky = new ApiRequestError(500, "boom", "channel 9f2 of workspace acme exploded");

    expect(renameErrorMessage("channel", leaky)).toBe(
      "Não foi possível renomear o canal. Tente novamente.",
    );
    expect(renameErrorMessage("group", new Error("network"))).toBe(
      "Não foi possível renomear o grupo. Tente novamente.",
    );
  });
});

// ── The domain's caps, in the domain's unit (CQ-893-01) ────────────────────
//
// The backend counts Unicode code points with utf8.RuneCountInString. Every
// UTF-16-flavoured measure — an HTML maxLength, String.prototype.length —
// counts an emoji as two, which is how the client came to refuse a 100-emoji
// channel name the server accepts. These tests pin the unit, not just the
// numbers, so that regression cannot come back unnoticed.

describe("conversationNameCodePointLength", () => {
  it("counts code points, not UTF-16 code units", () => {
    // The distinction the bug turned on: one emoji, two units.
    expect("😀".length).toBe(2);
    expect(conversationNameCodePointLength("😀")).toBe(1);
    expect(conversationNameCodePointLength("😀".repeat(100))).toBe(100);
    expect(conversationNameCodePointLength("áé中")).toBe(3);
    expect(conversationNameCodePointLength("")).toBe(0);
  });
});

describe("conversationNameMaxCodePoints", () => {
  it("restates the server's caps", () => {
    expect(conversationNameMaxCodePoints.channel).toBe(100);
    expect(conversationNameMaxCodePoints.group).toBe(120);
  });
});

describe("renameNameRefusal", () => {
  it("refuses an empty name in the aggregate's own words", () => {
    expect(renameNameRefusal("channel", "")).toBe("Escolha um nome para este canal.");
    expect(renameNameRefusal("group", "")).toBe("Escolha um nome para este grupo.");
  });

  it("accepts a channel name of exactly 100 emoji and refuses 101", () => {
    expect(renameNameRefusal("channel", "😀".repeat(100))).toBe("");
    expect(renameNameRefusal("channel", "😀".repeat(101))).toBe(
      "O nome do canal deve ter no máximo 100 caracteres.",
    );
  });

  it("accepts a group name of exactly 120 emoji and refuses 121", () => {
    expect(renameNameRefusal("group", "😀".repeat(120))).toBe("");
    expect(renameNameRefusal("group", "😀".repeat(121))).toBe(
      "O nome do grupo deve ter no máximo 120 caracteres.",
    );
  });

  it("applies the same caps to ASCII", () => {
    expect(renameNameRefusal("channel", "a".repeat(100))).toBe("");
    expect(renameNameRefusal("channel", "a".repeat(101))).not.toBe("");
    expect(renameNameRefusal("group", "a".repeat(120))).toBe("");
    expect(renameNameRefusal("group", "a".repeat(121))).not.toBe("");
  });

  // It reports, it never rewrites: truncation would persist a name the user
  // did not choose, silently.
  it("never alters the value it was given", () => {
    const tooLong = "😀".repeat(101);
    renameNameRefusal("channel", tooLong);
    expect(tooLong).toHaveLength(202);
    expect(conversationNameCodePointLength(tooLong)).toBe(101);
  });
});
