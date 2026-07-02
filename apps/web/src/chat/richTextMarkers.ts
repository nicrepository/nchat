/** Canonical grammar shared by the TipTap serializer and message renderer. */

export const BOLD_ITALIC_MARKER = "***" as const;
export const BOLD_MARKER = "**" as const;
export const ITALIC_MARKER = "*" as const;
export const CODE_MARKER = "`" as const;
export const CODE_BLOCK_MARKER = "```" as const;
export const LIST_INDENT = "  " as const;

export const INLINE_MARKERS = [
  { type: "boldItalic", marker: BOLD_ITALIC_MARKER },
  { type: "bold", marker: BOLD_MARKER },
  { type: "italic", marker: ITALIC_MARKER },
  { type: "code", marker: CODE_MARKER },
] as const;

// Exact v1 tokenizer used before symmetric escaping was introduced.
export const LEGACY_INLINE_RE = /(\*\*[^*\n]+\*\*|\*[^*\n]+\*|`[^`\n]+`)/;

export type InlineMarkerType = (typeof INLINE_MARKERS)[number]["type"];
export type ListType = "ul" | "ol";
export type MentionType = "user" | "channel";

export const MENTION_TOKEN_RE =
  /^@\[((?:\\.|[^\]])+)]\(mention:(user|channel):([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\)/i;

const ESCAPABLE = new Set(["\\", "*", "_", "`", "-"]);
const V3_ESCAPABLE = new Set([...ESCAPABLE, "@", "[", "]", "(", ")"]);

function escapeWith(text: string, escapable: Set<string>): string {
  let escaped = "";
  for (let i = 0; i < text.length; i++) {
    const char = text[i];
    if (escapable.has(char) || (char === "." && /\d/.test(text[i - 1] ?? ""))) {
      escaped += "\\";
    }
    escaped += char;
  }
  return escaped;
}

export function escapeRichText(text: string): string {
  return escapeWith(text, ESCAPABLE);
}

export const escapeRichTextV3 = (text: string) => escapeWith(text, V3_ESCAPABLE);

export function buildMentionToken(type: MentionType, id: string, label: string): string {
  return `@[${escapeRichTextV3(label)}](mention:${type}:${id})`;
}

function unescapeWith(text: string, escapable: Set<string>): string {
  let unescaped = "";
  for (let i = 0; i < text.length; i++) {
    const char = text[i];
    const next = text[i + 1];
    const escapedDot = next === "." && /\d/.test(unescaped[unescaped.length - 1] ?? "");
    if (char === "\\" && next && (escapable.has(next) || escapedDot)) {
      unescaped += next;
      i++;
    } else {
      unescaped += char;
    }
  }
  return unescaped;
}

export const unescapeRichText = (text: string) => unescapeWith(text, ESCAPABLE);
export const unescapeRichTextV3 = (text: string) => unescapeWith(text, V3_ESCAPABLE);

function isEscaped(text: string, index: number): boolean {
  let slashes = 0;
  for (let i = index - 1; i >= 0 && text[i] === "\\"; i--) slashes++;
  return slashes % 2 === 1;
}

export function findUnescapedMarker(text: string, marker: string, from: number): number {
  for (let i = from; i <= text.length - marker.length; i++) {
    if (text.startsWith(marker, i) && !isEscaped(text, i)) return i;
  }
  return -1;
}

export function formatListLine(
  type: ListType,
  depth: number,
  index: number,
  text: string,
  start = 1,
): string {
  const marker = type === "ul" ? "- " : `${start + index}. `;
  return LIST_INDENT.repeat(depth) + marker + text;
}

export interface ParsedListLine {
  depth: number;
  type: ListType;
  index: number;
  text: string;
}

export function parseListLine(line: string): ParsedListLine | null {
  const match = /^( *)(?:([-*]) |(\d+)\. )(.*)$/.exec(line);
  if (!match || match[1].length % LIST_INDENT.length !== 0) return null;
  return {
    depth: match[1].length / LIST_INDENT.length,
    type: match[2] ? "ul" : "ol",
    index: match[3] ? Number(match[3]) : 1,
    text: match[4],
  };
}

export function parseLegacyListLine(line: string): ParsedListLine | null {
  const parsed = parseListLine(line);
  return parsed?.depth === 0 ? parsed : null;
}

export const isCodeFence = (line: string): boolean => line.startsWith(CODE_BLOCK_MARKER);
