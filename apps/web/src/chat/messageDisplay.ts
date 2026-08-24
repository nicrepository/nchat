import type { AvatarColor, Message } from "./chatTypes";

export function formatTime(iso: string): string {
  try {
    return new Date(iso).toLocaleTimeString("pt-BR", { hour: "2-digit", minute: "2-digit" });
  } catch {
    return "";
  }
}

/** "12 de janeiro de 2024", or "" when the value is not a usable date. */
export function formatLongDate(iso: string): string {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return "";
  return date.toLocaleDateString("pt-BR", { day: "numeric", month: "long", year: "numeric" });
}

/**
 * Day label for a timestamp: "Hoje", "Ontem", or the long date.
 *
 * Shared by the conversation's day dividers and the details panel so a date
 * never reads one way above the messages and another way beside them.
 */
export function formatDayLabel(iso: string): string {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return "";
  const today = new Date();
  const yesterday = new Date(today);
  yesterday.setDate(yesterday.getDate() - 1);
  if (date.toDateString() === today.toDateString()) return "Hoje";
  if (date.toDateString() === yesterday.toDateString()) return "Ontem";
  return formatLongDate(iso);
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

/**
 * The local participant's call-presentation name (issue #612): the real
 * profile name with a "(você)" suffix, or a bare "Você" when there is no
 * usable name yet (profile loading/error/empty). The real name is always
 * primary — "Você" never *replaces* it, only stands in for it when there is
 * nothing else to show.
 */
export function localParticipantDisplayName(displayName: string): string {
  const trimmed = displayName.trim();
  return trimmed ? `${trimmed} (você)` : "Você";
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
