import { describe, expect, it } from "vitest";

import { MAX_CHANNEL_SLUG_LENGTH, slugifyChannelName, validateChannelForm } from "./channelForm";

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
