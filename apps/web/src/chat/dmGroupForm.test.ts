import { describe, expect, it } from "vitest";

import type { DMCandidate } from "./chatTypes";
import {
  countCodePoints,
  limitCodePoints,
  limitGroupTitleInput,
  MAX_GROUP_MEMBERS,
  MAX_GROUP_TITLE_CODE_POINTS,
  MIN_GROUP_MEMBERS,
  toggleGroupMember,
} from "./dmGroupForm";

const ana: DMCandidate = { userId: "user-2", displayName: "Ana" };
const bruno: DMCandidate = { userId: "user-3", displayName: "Bruno" };

function repeat(value: string, times: number): string {
  return Array.from({ length: times }, () => value).join("");
}

describe("countCodePoints", () => {
  it("counts runes the way Go does, not UTF-16 units", () => {
    // Each of these is a single code point that JavaScript stores as a surrogate
    // pair, so String.length would report twice the server's count.
    for (const astral of ["🙂", "𝄞", "𐍈", "🧩"]) {
      expect(astral.length).toBe(2);
      expect(countCodePoints(astral)).toBe(1);
    }
    expect(countCodePoints("")).toBe(0);
    expect(countCodePoints("Infra")).toBe(5);
    expect(countCodePoints("Infra 🙂 ção")).toBe(11);
  });

  it("counts the parts of a composed emoji separately, as utf8.RuneCountInString does", () => {
    // A skin-toned emoji is a base plus a modifier: two runes on the server,
    // therefore two here. Matching the server beats matching intuition.
    expect(countCodePoints("👍🏽")).toBe(2);
  });
});

describe("limitCodePoints", () => {
  it("returns short values untouched, including the exact limit", () => {
    expect(limitCodePoints("abc", 5)).toBe("abc");
    expect(limitCodePoints("abcde", 5)).toBe("abcde");
    expect(limitCodePoints("", 5)).toBe("");
  });

  it("truncates without splitting a surrogate pair", () => {
    const truncated = limitCodePoints("🙂🙂🙂", 2);
    expect(truncated).toBe("🙂🙂");
    expect(countCodePoints(truncated)).toBe(2);
    // A naive slice(0, 2) would have produced a lone surrogate here.
    expect(truncated).not.toContain("\uD83D\uD83D");
  });
});

describe("limitGroupTitleInput", () => {
  it("accepts a title at the server limit in ASCII and in emoji alike", () => {
    for (const character of ["a", "🙂"]) {
      const atLimit = repeat(character, MAX_GROUP_TITLE_CODE_POINTS);
      expect(limitGroupTitleInput(atLimit)).toBe(atLimit);
      expect(countCodePoints(limitGroupTitleInput(atLimit))).toBe(MAX_GROUP_TITLE_CODE_POINTS);
    }
  });

  it("truncates one code point past the limit, whatever the script", () => {
    for (const character of ["a", "🙂", "𝄞"]) {
      const tooLong = repeat(character, MAX_GROUP_TITLE_CODE_POINTS + 1);
      expect(countCodePoints(limitGroupTitleInput(tooLong))).toBe(MAX_GROUP_TITLE_CODE_POINTS);
    }
  });

  it("counts a mixed string by code points rather than by UTF-16 length", () => {
    const mixed = repeat("🙂", 60) + repeat("a", 60);
    expect(mixed.length).toBe(180);
    // Under a UTF-16 limit this would already have been cut; by code points it fits.
    expect(limitGroupTitleInput(mixed)).toBe(mixed);
  });

  it("keeps whitespace while typing and leaves trimming to serialisation", () => {
    expect(limitGroupTitleInput("  Infra  ")).toBe("  Infra  ");
    expect(limitGroupTitleInput("   ")).toBe("   ");
  });
});

describe("toggleGroupMember", () => {
  it("adds, removes and never duplicates a person", () => {
    const withAna = toggleGroupMember([], ana);
    expect(withAna).toEqual([ana]);

    const withBoth = toggleGroupMember(withAna, bruno);
    expect(withBoth).toEqual([ana, bruno]);

    // Toggling an already selected person removes them instead of duplicating.
    expect(toggleGroupMember(withBoth, ana)).toEqual([bruno]);
    // Identity is the user ID, not the displayed name.
    expect(toggleGroupMember(withBoth, { userId: "user-2", displayName: "Ana Maria" })).toEqual([
      bruno,
    ]);
  });

  it("refuses an addition past the cap without disturbing the current selection", () => {
    const full = Array.from({ length: MAX_GROUP_MEMBERS }, (_, index) => ({
      userId: `filled-${index}`,
      displayName: `Pessoa ${index}`,
    }));

    const unchanged = toggleGroupMember(full, ana);
    expect(unchanged).toBe(full);
    // Removal still works at the cap, so the selection is never stuck.
    expect(toggleGroupMember(full, full[0])).toHaveLength(MAX_GROUP_MEMBERS - 1);
  });

  it("keeps the selection order stable so the payload is predictable", () => {
    const selected = [ana, bruno].reduce(toggleGroupMember, [] as DMCandidate[]);
    expect(selected.map((member) => member.userId)).toEqual(["user-2", "user-3"]);
  });
});

describe("group limits", () => {
  it("mirrors the chat-service bounds", () => {
    // minGroupDMParticipants is 3 and maxGroupDMParticipants is 50, both
    // counting the caller, who is added server-side and never selected here.
    expect(MIN_GROUP_MEMBERS).toBe(2);
    expect(MAX_GROUP_MEMBERS).toBe(49);
    expect(MAX_GROUP_TITLE_CODE_POINTS).toBe(120);
  });
});
