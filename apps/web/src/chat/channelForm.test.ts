import { describe, expect, it } from "vitest";

import {
  channelDisplayNameLength,
  MAX_CHANNEL_DISPLAY_NAME_CODE_POINTS,
  MAX_CHANNEL_SLUG_LENGTH,
  slugifyChannelName,
  validateChannelDisplayName,
  validateChannelForm,
} from "./channelForm";

describe("slugifyChannelName", () => {
  it("strips accents instead of rejecting them", () => {
    expect(slugifyChannelName("Operações")).toBe("operacoes");
    expect(slugifyChannelName("Ação & Reação")).toBe("acao-reacao");
  });

  it("never produces a leading or trailing hyphen, which the server refuses", () => {
    expect(slugifyChannelName("  --Infra--  ")).toBe("infra");
    expect(slugifyChannelName("!!!")).toBe("");
  });

  it("truncates to the server limit without leaving a dangling hyphen", () => {
    const slug = slugifyChannelName("a".repeat(70) + " b");
    expect(slug).toHaveLength(MAX_CHANNEL_SLUG_LENGTH);
    expect(slug.endsWith("-")).toBe(false);
  });

  it("produces a slug the validator accepts", () => {
    expect(
      validateChannelForm({ displayName: "Operações", slug: slugifyChannelName("Operações") }),
    ).toBeNull();
  });
});

describe("validateChannelForm", () => {
  it("accepts a well-formed channel", () => {
    expect(validateChannelForm({ displayName: "Infraestrutura", slug: "infra" })).toBeNull();
    expect(validateChannelForm({ displayName: "A", slug: "a" })).toBeNull();
  });

  it("requires a name and an identifier", () => {
    expect(validateChannelForm({ displayName: "   ", slug: "infra" })).toMatch(/nome/i);
    expect(validateChannelForm({ displayName: "Infra", slug: "  " })).toMatch(/identificador/i);
  });

  // "geral" matches the slug pattern, so without its own branch the user would
  // be told the format is wrong and retype a valid-looking slug forever.
  it("reports the reserved slug specifically", () => {
    expect(validateChannelForm({ displayName: "Geral", slug: "geral" })).toMatch(/reservado/i);
    expect(validateChannelForm({ displayName: "Geral", slug: " GERAL " })).toMatch(/reservado/i);
  });

  it("rejects what the server's slug pattern rejects", () => {
    for (const slug of ["-infra", "infra-", "in fra", "Infra_1", "infra!", "á", "a".repeat(64)]) {
      expect(validateChannelForm({ displayName: "Infra", slug })).toMatch(/minúsculas/i);
    }
  });
});

describe("channel display name length", () => {
  const atLimit = (unit: string) => unit.repeat(MAX_CHANNEL_DISPLAY_NAME_CODE_POINTS);
  const overLimit = (unit: string) => unit.repeat(MAX_CHANNEL_DISPLAY_NAME_CODE_POINTS + 1);

  // The count must be code points, matching Go's utf8.RuneCountInString and
  // PostgreSQL's char_length. String.length would score an emoji as two and
  // refuse a name the server stores without complaint.
  it("counts code points, not UTF-16 units", () => {
    expect(channelDisplayNameLength(atLimit("a"))).toBe(100);
    expect(channelDisplayNameLength(atLimit("😀"))).toBe(100);
    expect(atLimit("😀").length).toBe(200);
    expect(channelDisplayNameLength("a".repeat(50) + "😀".repeat(50))).toBe(100);
  });

  it.each([
    ["ascii", "a"],
    ["emoji", "😀"],
  ])("accepts exactly the limit in %s and refuses one more", (_label, unit) => {
    expect(validateChannelDisplayName(atLimit(unit))).toBeNull();
    expect(validateChannelDisplayName(overLimit(unit))).toMatch(/100 caracteres/);
  });

  it("accepts a mixture that reaches the limit and refuses one past it", () => {
    expect(validateChannelDisplayName("a".repeat(50) + "😀".repeat(50))).toBeNull();
    expect(validateChannelDisplayName("a".repeat(50) + "😀".repeat(51))).toMatch(/100 caracteres/);
  });

  it("trims before counting, so padding never decides the outcome", () => {
    expect(validateChannelDisplayName(`  ${atLimit("a")}  `)).toBeNull();
    expect(validateChannelDisplayName(` ${overLimit("😀")} `)).toMatch(/100 caracteres/);
  });

  it("still requires a name", () => {
    expect(validateChannelDisplayName("")).toMatch(/nome/i);
    expect(validateChannelDisplayName("   ")).toMatch(/nome/i);
  });

  it("surfaces the name rule through the whole-form validator", () => {
    expect(validateChannelForm({ displayName: overLimit("😀"), slug: "infra" })).toMatch(
      /100 caracteres/,
    );
    expect(validateChannelForm({ displayName: atLimit("😀"), slug: "infra" })).toBeNull();
  });

  it("agrees with the server constant", () => {
    expect(MAX_CHANNEL_DISPLAY_NAME_CODE_POINTS).toBe(100);
  });
});
