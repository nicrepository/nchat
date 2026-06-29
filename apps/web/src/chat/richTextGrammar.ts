/**
 * richTextGrammar — single source of truth for the chat rich-text syntax.
 *
 * Contains only syntax (markers), predicates, and format operations.
 * Presentation concerns (labels, testIds) live in the consumers.
 *
 * Both RichTextRenderer (display) and ComposerToolbar (insertion) import
 * from here. The toolbar does NOT depend on the renderer component.
 */

// ── Inline markers ────────────────────────────────────────────────────────────

export const BOLD_MARKER = "**" as const;
export const ITALIC_MARKER = "*" as const;
export const CODE_MARKER = "`" as const;

// Order matters: ** must come before * to avoid partial matches.
// This regex corresponds 1:1 to BOLD_MARKER, ITALIC_MARKER, CODE_MARKER above.
export const INLINE_RE = /(\*\*[^*\n]+\*\*|\*[^*\n]+\*|`[^`\n]+`)/;

// ── Block predicates ──────────────────────────────────────────────────────────

export const isCodeFence = (line: string): boolean => line.startsWith("```");
export const isUlLine = (line: string): boolean => /^[-*] /.test(line);
export const isOlLine = (line: string): boolean => /^\d+\. /.test(line);

// ── Format items (syntax only — no labels or testIds) ─────────────────────────

export type FormatKind = "inline" | "block-code" | "list-ul" | "list-ol";

/** Syntax-level format descriptor. Presentation (label, testId) goes in the toolbar. */
export interface FormatItem {
  kind: FormatKind;
  marker: string;
}

export const FORMAT_ITEMS: FormatItem[] = [
  { kind: "inline", marker: BOLD_MARKER },
  { kind: "inline", marker: ITALIC_MARKER },
  { kind: "inline", marker: CODE_MARKER },
  { kind: "block-code", marker: "" },
  { kind: "list-ul", marker: "" },
  { kind: "list-ol", marker: "" },
];
