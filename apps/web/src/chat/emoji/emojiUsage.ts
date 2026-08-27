/**
 * Which emoji this person reaches for (issue #496).
 *
 * Kept in the browser, per user, because the product has no server-side place
 * for a preference this small and waiting for one would hold up the feature.
 * What is stored is a short list of emoji and how often each was used — no
 * message content, no conversation, no identity beyond the key's user id, and
 * nothing that would matter if it were lost. Every read is defensive: a
 * malformed, foreign or outdated value is treated as "no history yet" rather
 * than trusted, and localStorage failures (private mode, disabled storage) are
 * never allowed to break reacting.
 */

const storagePrefix = "nchat_emoji_usage";
const schemaVersion = 1;
/** Enough to keep recents and a useful frequency ranking; small enough to stay tiny. */
const maxEntries = 40;

export interface EmojiUsageEntry {
  readonly emoji: string;
  readonly count: number;
  readonly usedAt: number;
}

export interface EmojiUsage {
  readonly entries: readonly EmojiUsageEntry[];
  /** Selected skin tone: 0 for the default, 1..5 for Unicode's modifiers. */
  readonly tone: number;
}

interface StoredUsage {
  v: number;
  tone: number;
  entries: { emoji: string; count: number; usedAt: number }[];
}

export const emptyEmojiUsage: EmojiUsage = { entries: [], tone: 0 };

function storageKey(userId: string): string {
  return `${storagePrefix}:${userId}`;
}

function isUsageEntry(value: unknown): value is EmojiUsageEntry {
  if (typeof value !== "object" || value === null) return false;
  const entry = value as Record<string, unknown>;
  return (
    typeof entry.emoji === "string" &&
    entry.emoji.length > 0 &&
    typeof entry.count === "number" &&
    Number.isFinite(entry.count) &&
    entry.count > 0 &&
    typeof entry.usedAt === "number" &&
    Number.isFinite(entry.usedAt)
  );
}

function parseTone(value: unknown): number {
  return typeof value === "number" && Number.isInteger(value) && value >= 0 && value <= 5
    ? value
    : 0;
}

/** Reads the stored preference, discarding anything that is not exactly the shape written. */
export function readEmojiUsage(userId: string): EmojiUsage {
  if (!userId) return emptyEmojiUsage;
  let raw: unknown;
  try {
    raw = JSON.parse(localStorage.getItem(storageKey(userId)) ?? "null");
  } catch {
    return emptyEmojiUsage;
  }
  if (typeof raw !== "object" || raw === null) return emptyEmojiUsage;
  const stored = raw as Partial<StoredUsage>;
  if (stored.v !== schemaVersion || !Array.isArray(stored.entries)) return emptyEmojiUsage;
  return {
    entries: stored.entries.filter(isUsageEntry).slice(0, maxEntries),
    tone: parseTone(stored.tone),
  };
}

function persist(userId: string, usage: EmojiUsage): EmojiUsage {
  try {
    localStorage.setItem(
      storageKey(userId),
      JSON.stringify({ v: schemaVersion, tone: usage.tone, entries: usage.entries }),
    );
  } catch {
    // A preference is best-effort: never let storage refuse a reaction.
  }
  return usage;
}

/** Records one use, moving the emoji to the front and bumping its count. */
export function recordEmojiUse(userId: string, emoji: string, usage: EmojiUsage): EmojiUsage {
  if (!userId || !emoji) return usage;
  const previous = usage.entries.find((entry) => entry.emoji === emoji);
  const entries = [
    { emoji, count: (previous?.count ?? 0) + 1, usedAt: Date.now() },
    ...usage.entries.filter((entry) => entry.emoji !== emoji),
  ].slice(0, maxEntries);
  return persist(userId, { entries, tone: usage.tone });
}

export function storeEmojiTone(userId: string, tone: number, usage: EmojiUsage): EmojiUsage {
  return persist(userId, { entries: usage.entries, tone: parseTone(tone) });
}

/** Most recently used first — the order entries are already kept in. */
export function recentEmojis(usage: EmojiUsage, limit: number): readonly string[] {
  return usage.entries.slice(0, limit).map((entry) => entry.emoji);
}

/**
 * Most used first, shown only once there is enough history to mean anything.
 *
 * A "most used" section built from single uses is just a second, worse copy of
 * "recent", so an emoji has to have been used more than once to appear, and the
 * section is empty — and therefore hidden — until a few have.
 */
export function frequentEmojis(usage: EmojiUsage, limit: number): readonly string[] {
  return usage.entries
    .filter((entry) => entry.count > 1)
    .sort((a, b) => b.count - a.count || b.usedAt - a.usedAt)
    .slice(0, limit)
    .map((entry) => entry.emoji);
}
