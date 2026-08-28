import { describe, expect, it } from "vitest";

import { normalizeChatTargetId } from "./chatTargetId";

describe("normalizeChatTargetId", () => {
  const canonical = "550e8400-e29b-41d4-a716-446655440000";

  it.each([
    ["canonical UUID", canonical],
    ["uppercase UUID", "550E8400-E29B-41D4-A716-446655440000"],
    ["compact UUID", "550e8400e29b41d4a716446655440000"],
    ["braced UUID", "{550E8400-E29B-41D4-A716-446655440000}"],
  ])("canonicalizes a %s", (_label, value) => {
    expect(normalizeChatTargetId(value)).toBe(canonical);
  });

  it("trims but otherwise preserves non-UUID identifiers", () => {
    expect(normalizeChatTargetId("  ch-1  ")).toBe("ch-1");
    expect(normalizeChatTargetId("NOT-A-UUID")).toBe("NOT-A-UUID");
  });

  it("does not reinterpret malformed UUID-like values", () => {
    expect(normalizeChatTargetId("550e8400-e29b-41d4-a716-44665544000z")).toBe(
      "550e8400-e29b-41d4-a716-44665544000z",
    );
  });
});
