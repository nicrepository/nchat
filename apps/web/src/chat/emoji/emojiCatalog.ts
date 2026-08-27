/**
 * The reaction emoji catalog (issue #496).
 *
 * The data is a generated projection of Unicode's RGI list and CLDR's
 * Portuguese annotations — see scripts/emoji/generate-emoji-catalog.mjs, which
 * writes this file's JSON and the server's validation list in the same run.
 * Nothing here is hand-maintained, and nothing is fetched at runtime: the
 * catalog is a fixed property of the build, not a service.
 *
 * It is loaded on demand. A few hundred kilobytes of emoji names has no business
 * in the bundle that renders a conversation, so the import below is dynamic and
 * memoised: the first picker opened in a session pays for it, every later one
 * does not.
 */

export interface EmojiEntry {
  /** The complete Unicode sequence, ZWJ and variation selectors included. */
  readonly unicode: string;
  /** CLDR display name, Portuguese where CLDR has one. */
  readonly label: string;
  /**
   * The five variants a tone selector picks from, in tone order — the ones
   * where every person in the sequence shares the tone. Absent when the emoji
   * has no skin-tone variants at all.
   *
   * A sequence with more than one person also has mixed-tone variants (🧑‍🤝‍🧑
   * has twenty-five in total). Those are valid emoji people react with, so the
   * catalog still indexes and names them — but a *global* tone selector never
   * means one of them, which is why they are not here.
   */
  readonly skins?: readonly string[];
  /** Index into emojiGroupLabels. */
  readonly group: number;
  /** Accent-folded label and keywords, joined — the only string search reads. */
  readonly haystack: string;
}

export interface EmojiCatalog {
  readonly version: string;
  readonly entries: readonly EmojiEntry[];
  readonly byUnicode: ReadonlyMap<string, EmojiEntry>;
}

interface RawEmojiEntry {
  u: string;
  l: string;
  t: string;
  g: number;
  /** The five homogeneous skin-tone variants, in tone order. */
  s?: string[];
  /** Mixed-tone variants: indexed so they can be named, never offered by tone. */
  m?: string[];
}

interface RawEmojiCatalog {
  version: string;
  emojis: RawEmojiEntry[];
}

/**
 * Group names in the order Unicode lists them. Index 2 is Unicode's "Component"
 * group — skin tones and hair, which are parts of an emoji rather than emoji
 * anyone reacts with. The catalog contains none of them, so the tab never
 * appears; the label is kept so every other index still lines up with the data.
 */
export const emojiGroupLabels: readonly string[] = [
  "Rostos e emoções",
  "Pessoas e corpo",
  "Componentes",
  "Animais e natureza",
  "Comidas e bebidas",
  "Viagens e lugares",
  "Atividades",
  "Objetos",
  "Símbolos",
  "Bandeiras",
];

/**
 * Accent-folded, lower-cased text for matching.
 *
 * "coração" and "coracao" must find the same emoji: a Brazilian keyboard types
 * the first, a hurried search types the second. Decomposing to NFD splits a
 * letter from its accent, and dropping the combining marks leaves the letter.
 */
export function normalizeEmojiSearchText(value: string): string {
  return value
    .normalize("NFD")
    .replace(/\p{Diacritic}/gu, "")
    .toLowerCase()
    .trim();
}

function toEntry(raw: RawEmojiEntry): EmojiEntry {
  return {
    unicode: raw.u,
    label: raw.l,
    group: raw.g,
    ...(raw.s ? { skins: raw.s } : {}),
    haystack: normalizeEmojiSearchText(`${raw.l} ${raw.t}`),
  };
}

export function buildEmojiCatalog(raw: RawEmojiCatalog): EmojiCatalog {
  const entries = raw.emojis.map(toEntry);
  // Every skin-tone variant is indexed onto its base entry, mixed-tone ones
  // included, so a stored recent like "👍🏿" — or a reaction someone made with
  // "🧑🏻‍🤝‍🧑🏿" — still resolves to a name instead of being read out to a screen
  // reader as bare code points.
  const byUnicode = new Map<string, EmojiEntry>();
  raw.emojis.forEach((rawEntry, index) => {
    const entry = entries[index];
    byUnicode.set(entry.unicode, entry);
    for (const variant of rawEntry.s ?? []) byUnicode.set(variant, entry);
    for (const variant of rawEntry.m ?? []) byUnicode.set(variant, entry);
  });
  return { version: raw.version, entries, byUnicode };
}

let pending: Promise<EmojiCatalog> | null = null;
let loaded: EmojiCatalog | null = null;

/**
 * The catalog, fetched once per session and shared by every picker.
 *
 * A *successful* load is remembered; a failed one is not. Caching the rejected
 * promise would make every later call fail with the same stale error, which
 * turns a momentary network problem into a permanently broken picker — the
 * caller's retry has to be able to actually reach the network again.
 */
export function loadEmojiCatalog(): Promise<EmojiCatalog> {
  // Imported as text and parsed, not as a module: a quarter of a megabyte of
  // emoji names inferred as an object literal is a typecheck cost with nothing
  // to show for it, and JSON.parse is the faster way to materialise it anyway.
  pending ??= import("./emojiCatalog.json?raw").then(
    (module) => {
      loaded = buildEmojiCatalog(JSON.parse(module.default) as RawEmojiCatalog);
      return loaded;
    },
    (error: unknown) => {
      pending = null;
      throw error;
    },
  );
  return pending;
}

/**
 * Whether this build's catalog contains a sequence, answered synchronously.
 *
 * Used to refuse a value before it is sent, which is a convenience and not the
 * decision: the server validates every reaction against its own copy of the same
 * catalog. Before the catalog has loaded nothing has been picked from it, so an
 * unknown value here is genuinely unknown.
 */
export function isCatalogedEmoji(emoji: string): boolean {
  return loaded?.byUnicode.has(emoji) ?? false;
}

/** The display name for a sequence, base or skin-toned; the sequence itself when unknown. */
export function emojiLabel(catalog: EmojiCatalog, unicode: string): string {
  return catalog.byUnicode.get(unicode)?.label ?? unicode;
}

/** Test seam: forgets the memoised catalog so a suite can load it again. */
export function resetEmojiCatalogCache(): void {
  pending = null;
  loaded = null;
}

/**
 * The variant of an entry in the reader's chosen tone, or the entry itself when
 * it has no tones or the default is selected. Tone 0 is "default"; 1..5 are
 * Unicode's five modifiers in order.
 *
 * `skins` holds only the variants where every person in the sequence shares the
 * tone, which is what a single global selector means: tone 3 of 🧑‍🤝‍🧑 is
 * 🧑🏽‍🤝‍🧑🏽, never one of the twenty mixed pairings RGI also defines. The
 * generator decides which those are by reading the modifiers in each sequence,
 * so nothing here depends on a position in the cartesian product.
 */
export function withSkinTone(entry: EmojiEntry, tone: number): string {
  if (tone <= 0 || !entry.skins) return entry.unicode;
  return entry.skins[tone - 1] ?? entry.unicode;
}

/**
 * Entries whose name or keywords contain every term typed, capped.
 *
 * Every term must match somewhere, so "gato preto" narrows rather than widens.
 * The haystacks are folded once at load, so a keystroke is a few thousand
 * substring tests over strings already in memory — no index to rebuild, nothing
 * to schedule off the main thread.
 */
export function searchEmojis(
  catalog: EmojiCatalog,
  query: string,
  limit = 300,
): readonly EmojiEntry[] {
  const terms = normalizeEmojiSearchText(query).split(/\s+/).filter(Boolean);
  if (terms.length === 0) return [];
  const found: EmojiEntry[] = [];
  for (const entry of catalog.entries) {
    if (terms.every((term) => entry.haystack.includes(term))) found.push(entry);
    if (found.length === limit) break;
  }
  return found;
}

/**
 * The catalog entries behind a list of stored sequences, in the order given.
 *
 * A recent is stored already toned, so it resolves through the same index the
 * labels use; anything the catalog no longer knows is dropped rather than shown
 * without a name.
 */
export function entriesFor(
  catalog: EmojiCatalog,
  sequences: readonly string[],
): readonly EmojiEntry[] {
  const entries: EmojiEntry[] = [];
  for (const sequence of sequences) {
    const entry = catalog.byUnicode.get(sequence);
    if (entry) entries.push(entry);
  }
  return entries;
}

/** Entries of one Unicode group, in catalog order. */
export function emojisInGroup(catalog: EmojiCatalog, group: number): readonly EmojiEntry[] {
  return catalog.entries.filter((entry) => entry.group === group);
}

/** The group indexes that actually have emoji, in catalog order. */
export function populatedEmojiGroups(catalog: EmojiCatalog): readonly number[] {
  const groups: number[] = [];
  for (const entry of catalog.entries) {
    if (!groups.includes(entry.group)) groups.push(entry.group);
  }
  return groups;
}
