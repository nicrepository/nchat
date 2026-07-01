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

export type InlineMarkerType = (typeof INLINE_MARKERS)[number]["type"];
export type ListType = "ul" | "ol";

const ESCAPABLE = new Set(["\\", "*", "_", "`", "-"]);

export function escapeRichText(text: string): string {
  let escaped = "";
  for (let i = 0; i < text.length; i++) {
    const char = text[i];
    if (ESCAPABLE.has(char) || (char === "." && /\d/.test(text[i - 1] ?? ""))) {
      escaped += "\\";
    }
    escaped += char;
  }
  return escaped;
}

export function unescapeRichText(text: string): string {
  let unescaped = "";
  for (let i = 0; i < text.length; i++) {
    const char = text[i];
    const next = text[i + 1];
    const escapedDot = next === "." && /\d/.test(unescaped[unescaped.length - 1] ?? "");
    if (char === "\\" && next && (ESCAPABLE.has(next) || escapedDot)) {
      unescaped += next;
      i++;
    } else {
      unescaped += char;
    }
  }
  return unescaped;
}

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

export function formatListLine(type: ListType, depth: number, index: number, text: string): string {
  const marker = type === "ul" ? "- " : `${index + 1}. `;
  return LIST_INDENT.repeat(depth) + marker + text;
}

export interface ParsedListLine {
  depth: number;
  type: ListType;
  text: string;
}

export function parseListLine(line: string): ParsedListLine | null {
  const match = /^( *)(?:([-*]) |(\d+)\. )(.*)$/.exec(line);
  if (!match || match[1].length % LIST_INDENT.length !== 0) return null;
  return {
    depth: match[1].length / LIST_INDENT.length,
    type: match[2] ? "ul" : "ol",
    text: match[4],
  };
}

export const isCodeFence = (line: string): boolean => line.startsWith(CODE_BLOCK_MARKER);
