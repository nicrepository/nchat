import { describe, expect, it } from "vitest";

import type { Message } from "./chatTypes";
import { localParticipantDisplayName, senderLabel } from "./messageDisplay";

describe("localParticipantDisplayName", () => {
  it("appends (você) to a real display name", () => {
    expect(localParticipantDisplayName("Caio Almeida")).toBe("Caio Almeida (você)");
  });

  it("falls back to a bare Você when there is no usable name", () => {
    expect(localParticipantDisplayName("")).toBe("Você");
    expect(localParticipantDisplayName("   ")).toBe("Você");
  });
});

describe("senderLabel", () => {
  const anonymous: Message = {
    id: "m-1",
    senderId: "9f1c2d3e-4b5a-4c6d-8e7f-0a1b2c3d4e5f",
    senderDisplayName: "",
    senderEmail: "",
    kind: "user",
    bodyText: "oi",
    bodyFormat: "v3",
    isRemoved: false,
    status: "active",
    createdAt: "2026-07-15T12:00:00Z",
    updatedAt: "2026-07-15T12:00:00Z",
    isEdited: false,
    editCount: 0,
    reactions: [],
    isFavorited: false,
    isForwarded: false,
  };

  it("prefers the display name, then the e-mail", () => {
    expect(senderLabel({ ...anonymous, senderDisplayName: "Ana", senderEmail: "a@x.test" })).toBe(
      "Ana",
    );
    expect(senderLabel({ ...anonymous, senderEmail: "a@x.test" }, "Autor")).toBe("a@x.test");
  });

  it("keeps the id fragment as the last resort for existing callers", () => {
    expect(senderLabel(anonymous)).toBe("9f1c2d3e");
  });

  it("uses the caller's human fallback instead of any part of the id", () => {
    expect(senderLabel(anonymous, "Autor não identificado")).toBe("Autor não identificado");
  });
});
