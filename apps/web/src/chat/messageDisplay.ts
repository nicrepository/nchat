import type { AvatarColor, Message } from "./chatTypes";

export function formatTime(iso: string): string {
  try {
    return new Date(iso).toLocaleTimeString("pt-BR", { hour: "2-digit", minute: "2-digit" });
  } catch {
    return "";
  }
}

export function senderLabel(msg: Message): string {
  return msg.senderDisplayName || msg.senderEmail || msg.senderId.slice(0, 8);
}

/**
 * Up to two initials from a display name; "?" when nothing usable is left.
 *
 * Indexing a string with [0] yields a UTF-16 code unit, which splits any
 * character outside the BMP — an emoji or a rare CJK ideograph would render as
 * a lone surrogate (a replacement glyph). Array.from iterates code points, so
 * the first *character* is taken whatever plane it lives in.
 */
export function initialsFrom(name: string): string {
  const initials = name
    .trim()
    .split(/\s+/)
    .slice(0, 2)
    .map((word) => Array.from(word)[0]?.toUpperCase() ?? "")
    .join("");
  return initials || "?";
}

const avatarColors: AvatarColor[] = ["purple", "green", "blue", "rose", "amber", "teal"];

/**
 * Deterministic avatar colour for the initials fallback: the same seed (a user
 * ID) always yields the same swatch, across renders, reloads and viewers. A
 * random or index-based colour would flicker on every re-render and make two
 * different people look like the same one after a reorder.
 */
export function avatarColorFor(seed: string): AvatarColor {
  let hash = 0;
  for (let i = 0; i < seed.length; i += 1) {
    hash = (hash * 31 + seed.charCodeAt(i)) >>> 0;
  }
  return avatarColors[hash % avatarColors.length];
}
