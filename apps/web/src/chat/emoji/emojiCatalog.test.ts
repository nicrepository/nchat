import { beforeEach, describe, expect, it } from "vitest";

import {
  buildEmojiCatalog,
  emojiGroupLabels,
  emojiLabel,
  emojisInGroup,
  isCatalogedEmoji,
  loadEmojiCatalog,
  normalizeEmojiSearchText,
  populatedEmojiGroups,
  resetEmojiCatalogCache,
  searchEmojis,
  withSkinTone,
  type EmojiCatalog,
} from "./emojiCatalog";

const fixture = buildEmojiCatalog({
  version: "test",
  emojis: [
    { u: "😀", l: "rosto risonho", t: "feliz sorriso grinning", g: 0 },
    {
      u: "👍",
      l: "polegar para cima",
      t: "jóia concordo",
      g: 1,
      s: ["👍🏻", "👍🏼", "👍🏽", "👍🏾", "👍🏿"],
    },
    {
      // Two people: RGI defines the whole cartesian product of tones, of which
      // only the five below are "everyone the same tone".
      u: "🧑‍🤝‍🧑",
      l: "pessoas de mãos dadas",
      t: "amizade casal",
      g: 1,
      s: ["🧑🏻‍🤝‍🧑🏻", "🧑🏼‍🤝‍🧑🏼", "🧑🏽‍🤝‍🧑🏽", "🧑🏾‍🤝‍🧑🏾", "🧑🏿‍🤝‍🧑🏿"],
      m: ["🧑🏻‍🤝‍🧑🏼", "🧑🏿‍🤝‍🧑🏻"],
    },
    { u: "👨‍👩‍👧‍👦", l: "família", t: "casal filhos", g: 1 },
    { u: "🐱", l: "rosto de gato", t: "animal felino", g: 3 },
    { u: "🇧🇷", l: "bandeira: Brasil", t: "bandeira flag", g: 9 },
  ],
});

function entry(unicode: string) {
  const found = fixture.byUnicode.get(unicode);
  if (!found) throw new Error(`fixture is missing ${unicode}`);
  return found;
}

describe("normalizeEmojiSearchText", () => {
  it("folds accents and case so a typed term matches either spelling", () => {
    expect(normalizeEmojiSearchText("Coração")).toBe("coracao");
    expect(normalizeEmojiSearchText("  JÓIA  ")).toBe("joia");
  });
});

describe("searchEmojis", () => {
  it("finds an emoji by its name", () => {
    expect(searchEmojis(fixture, "gato").map((entry) => entry.unicode)).toEqual(["🐱"]);
  });

  it("finds an emoji by a keyword that is not in its name", () => {
    expect(searchEmojis(fixture, "felino").map((entry) => entry.unicode)).toEqual(["🐱"]);
  });

  it("matches a keyword typed without its accent", () => {
    expect(searchEmojis(fixture, "joia").map((entry) => entry.unicode)).toEqual(["👍"]);
  });

  // Every term has to match, so a second word narrows the result instead of
  // widening it the way an OR search would.
  it("narrows on each additional term", () => {
    expect(searchEmojis(fixture, "rosto")).toHaveLength(2);
    expect(searchEmojis(fixture, "rosto gato").map((entry) => entry.unicode)).toEqual(["🐱"]);
  });

  it("returns nothing for an empty query or an unmatched term", () => {
    expect(searchEmojis(fixture, "   ")).toEqual([]);
    expect(searchEmojis(fixture, "zzzz")).toEqual([]);
  });

  it("stops at the requested limit", () => {
    expect(searchEmojis(fixture, "a", 2)).toHaveLength(2);
  });
});

describe("groups", () => {
  it("lists only the groups that have emoji, in catalog order", () => {
    expect(populatedEmojiGroups(fixture)).toEqual([0, 1, 3, 9]);
  });

  it("keeps a label for every group index the data can carry", () => {
    for (const group of populatedEmojiGroups(fixture)) {
      expect(emojiGroupLabels[group]).toBeTruthy();
    }
  });

  it("returns the entries of one group", () => {
    expect(emojisInGroup(fixture, 1).map((item) => item.unicode)).toEqual(["👍", "🧑‍🤝‍🧑", "👨‍👩‍👧‍👦"]);
  });
});

describe("skin tones", () => {
  it.each([
    [1, "👍🏻"],
    [2, "👍🏼"],
    [3, "👍🏽"],
    [4, "👍🏾"],
    [5, "👍🏿"],
  ])("applies tone %i to an emoji with one person", (tone, expected) => {
    expect(withSkinTone(entry("👍"), tone)).toBe(expected);
  });

  // The bug this covers: picking by position in the cartesian product returned
  // 🧑🏻‍🤝‍🧑🏼 for tone 2. A single global selector means one tone for everyone.
  it.each([
    [1, "🧑🏻‍🤝‍🧑🏻"],
    [2, "🧑🏼‍🤝‍🧑🏼"],
    [3, "🧑🏽‍🤝‍🧑🏽"],
    [4, "🧑🏾‍🤝‍🧑🏾"],
    [5, "🧑🏿‍🤝‍🧑🏿"],
  ])("applies tone %i to every person in a multi-person sequence", (tone, expected) => {
    expect(withSkinTone(entry("🧑‍🤝‍🧑"), tone)).toBe(expected);
  });

  it("returns the untoned base for the default tone", () => {
    expect(withSkinTone(entry("👍"), 0)).toBe("👍");
    expect(withSkinTone(entry("🧑‍🤝‍🧑"), 0)).toBe("🧑‍🤝‍🧑");
  });

  // A joined sequence that Unicode gives no tones at all must come through
  // whole, not truncated or re-joined by a tone lookup.
  it("leaves an emoji without variants, or an unknown tone, untouched", () => {
    expect(withSkinTone(entry("👨‍👩‍👧‍👦"), 3)).toBe("👨‍👩‍👧‍👦");
    expect(withSkinTone(entry("👍"), 9)).toBe("👍");
    expect(withSkinTone(entry("🐱"), 2)).toBe("🐱");
  });

  // Mixed pairings are real emoji people react with, so the catalog names them —
  // it just never offers them from a global tone selector.
  it("names mixed-tone variants without ever selecting one", () => {
    expect(emojiLabel(fixture, "🧑🏻‍🤝‍🧑🏼")).toBe("pessoas de mãos dadas");
    expect(entry("🧑‍🤝‍🧑").skins).not.toContain("🧑🏻‍🤝‍🧑🏼");
  });

  // A stored recent is already toned, and still has to be nameable.
  it("names a skin-toned sequence by its base emoji", () => {
    expect(emojiLabel(fixture, "👍🏿")).toBe("polegar para cima");
    expect(emojiLabel(fixture, "🤷")).toBe("🤷");
  });
});

describe("the generated catalog", () => {
  beforeEach(() => resetEmojiCatalogCache());

  it("answers no about every sequence until it has been loaded", () => {
    expect(isCatalogedEmoji("👍")).toBe(false);
  });

  it("loads once and is shared by every later caller", async () => {
    const first = loadEmojiCatalog();
    const second = loadEmojiCatalog();
    expect(await first).toBe(await second);
  });

  it("carries a Unicode version and a comprehensive set of emoji", async () => {
    const catalog = await loadEmojiCatalog();
    expect(catalog.version).toMatch(/^\d+\.\d+$/);
    expect(catalog.entries.length).toBeGreaterThan(1500);
  });

  // The shapes a length- or prefix-based check gets wrong: a joined family, a
  // modified body part, a two-code-point flag, and a keycap.
  it("contains complete Unicode sequences, not just single code points", async () => {
    const catalog = await loadEmojiCatalog();
    for (const sequence of ["👨‍👩‍👧‍👦", "🏳️‍🌈", "🇧🇷", "1️⃣", "❤️"]) {
      expect(catalog.byUnicode.has(sequence)).toBe(true);
    }
    expect(catalog.byUnicode.has("👍🏿")).toBe(true);
  });

  it("recognises a catalogued sequence and refuses anything else once loaded", async () => {
    await loadEmojiCatalog();
    expect(isCatalogedEmoji("👍")).toBe(true);
    expect(isCatalogedEmoji("👍🏿")).toBe(true);
    expect(isCatalogedEmoji("<b>oi</b>")).toBe(false);
    expect(isCatalogedEmoji(":+1:")).toBe(false);
    expect(isCatalogedEmoji("👍👍")).toBe(false);
  });

  it("searches the real catalog in Portuguese", async () => {
    const catalog: EmojiCatalog = await loadEmojiCatalog();
    expect(searchEmojis(catalog, "foguete").map((entry) => entry.unicode)).toContain("🚀");
    expect(searchEmojis(catalog, "coracao").length).toBeGreaterThan(0);
  });

  it("offers the multi-person sequences a global tone selector needs", async () => {
    const catalog = await loadEmojiCatalog();
    expect(withSkinTone(catalog.byUnicode.get("🧑‍🤝‍🧑")!, 3)).toBe("🧑🏽‍🤝‍🧑🏽");
    expect(withSkinTone(catalog.byUnicode.get("👩‍❤️‍💋‍👨")!, 5)).toBe("👩🏿‍❤️‍💋‍👨🏿");
  });

  /**
   * The generator's job, checked over the whole dataset rather than a handful of
   * examples: every emoji the picker will tone must offer exactly five variants,
   * and each must apply one tone to everybody in the sequence. This is what makes
   * the cartesian product harmless — a regression in the generator fails here,
   * not in a user's tooltip.
   */
  it("gives every toned emoji five variants that are homogeneous", async () => {
    const catalog = await loadEmojiCatalog();
    const modifiers = ["🏻", "🏼", "🏽", "🏾", "🏿"];
    const toned = catalog.entries.filter((item) => item.skins);
    expect(toned.length).toBeGreaterThan(300);

    const wrong = toned.filter((item) =>
      modifiers.some((modifier, index) => {
        const variant = withSkinTone(item, index + 1);
        const used = new Set([...variant].filter((char) => modifiers.includes(char)));
        return item.skins?.length !== modifiers.length || used.size !== 1 || !used.has(modifier);
      }),
    );
    expect(wrong.map((item) => item.unicode)).toEqual([]);
  });
});
