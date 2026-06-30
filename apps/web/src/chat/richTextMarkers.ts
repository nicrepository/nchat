/**
 * richTextMarkers — inline markers and block predicates for the chat Markdown syntax.
 *
 * Used by RichTextRenderer to parse body_text on display.
 * Serialization (TipTap → Markdown) lives in tiptapSerializer.ts.
 */

// ── Inline markers ────────────────────────────────────────────────────────────

export const BOLD_MARKER = "**" as const;
export const ITALIC_MARKER = "*" as const;
export const CODE_MARKER = "`" as const;

// Order matters: ** must come before * to avoid partial matches.
export const INLINE_RE = /(\*\*[^*\n]+\*\*|\*[^*\n]+\*|`[^`\n]+`)/;

// ── Block predicates ──────────────────────────────────────────────────────────

export const isCodeFence = (line: string): boolean => line.startsWith("```");
export const isUlLine = (line: string): boolean => /^[-*] /.test(line);
export const isOlLine = (line: string): boolean => /^\d+\. /.test(line);
