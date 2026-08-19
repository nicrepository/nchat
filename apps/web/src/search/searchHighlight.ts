/**
 * Pure text helpers for safely highlighting search matches.
 *
 * No DOM, no React — HighlightedText.tsx is the only place these results ever
 * reach JSX, and it never uses dangerouslySetInnerHTML.
 */

export function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

export interface HighlightSegment {
  value: string;
  matched: boolean;
}

/**
 * Splits `text` into segments around case-insensitive occurrences of `query`.
 *
 * An empty (or whitespace-only) query yields the whole text as one unmatched
 * segment — there is nothing to highlight yet.
 */
export function splitHighlightSegments(text: string, query: string): HighlightSegment[] {
  const trimmed = query.trim();
  if (!trimmed) return [{ value: text, matched: false }];

  const pattern = new RegExp(`(${escapeRegExp(trimmed)})`, "gi");
  const loweredTrimmed = trimmed.toLocaleLowerCase();
  return text
    .split(pattern)
    .filter((part) => part !== "")
    .map((part) => ({
      value: part,
      matched: part.toLocaleLowerCase() === loweredTrimmed,
    }));
}

/**
 * Bounds a message body around its first match so a long body_text doesn't
 * render in full inside a result row. The server sends no snippet field, so
 * this is done client-side against the raw text.
 */
export function buildMessageSnippet(text: string, query: string, contextChars = 80): string {
  const trimmed = query.trim();
  if (!trimmed) {
    return text.length > contextChars * 2 ? `${text.slice(0, contextChars * 2)}…` : text;
  }

  const matchIndex = text.toLocaleLowerCase().indexOf(trimmed.toLocaleLowerCase());
  if (matchIndex === -1) {
    return text.length > contextChars * 2 ? `${text.slice(0, contextChars * 2)}…` : text;
  }

  const start = Math.max(0, matchIndex - contextChars);
  const end = Math.min(text.length, matchIndex + trimmed.length + contextChars);
  const prefix = start > 0 ? "…" : "";
  const suffix = end < text.length ? "…" : "";
  return `${prefix}${text.slice(start, end)}${suffix}`;
}
