/**
 * tiptapSerializer — converts a TipTap/ProseMirror JSON document to the
 * Markdown-like string stored as body_text.
 *
 * This is the only serialization path. The backend contract (option A) is
 * Markdown strings; no HTML ever reaches the server.
 *
 * richTextMarkers.ts owns the grammar and symmetric escaping used here and by
 * RichTextRenderer.tsx.
 */

import {
  BOLD_ITALIC_MARKER,
  BOLD_MARKER,
  CODE_BLOCK_MARKER,
  CODE_MARKER,
  ITALIC_MARKER,
  escapeRichText,
  formatListLine,
} from "./richTextMarkers";
import type { ListType } from "./richTextMarkers";

// ── ProseMirror node shape ────────────────────────────────────────────────────

/** Minimal ProseMirror node type — no runtime dep on @tiptap/core needed. */
export interface TTNode {
  type?: string;
  text?: string;
  marks?: Array<{ type: string }>;
  attrs?: Record<string, unknown>;
  content?: TTNode[];
}

// ── Inline mark application ───────────────────────────────────────────────────

/**
 * Applies inline marks to a text string.
 *
 * Code is exclusive in the configured TipTap schema. Bold and italic combine.
 */
export function applyMarks(text: string, marks: Array<{ type: string }>): string {
  const types = new Set(marks.map((m) => m.type));
  const escaped = escapeRichText(text);
  if (types.has("code")) return CODE_MARKER + escaped + CODE_MARKER;
  if (types.has("bold") && types.has("italic"))
    return BOLD_ITALIC_MARKER + escaped + BOLD_ITALIC_MARKER;
  if (types.has("bold")) return BOLD_MARKER + escaped + BOLD_MARKER;
  if (types.has("italic")) return ITALIC_MARKER + escaped + ITALIC_MARKER;
  return escaped;
}

// ── Inline serializer ─────────────────────────────────────────────────────────

export function serializeInline(nodes: TTNode[]): string {
  return nodes
    .map((n) => {
      if (n.type === "hardBreak") return "\n";
      if (n.type === "text") return applyMarks(n.text ?? "", n.marks ?? []);
      return "";
    })
    .join("");
}

// ── List item serializer ──────────────────────────────────────────────────────

/**
 * Serializes all content blocks within a list item.
 * A listItem may contain multiple paragraphs (e.g. from paste). They are joined
 * with a space since RichTextRenderer expects single-line list items.
 * Direct content blocks are joined with a space. Child lists are serialized
 * recursively with two-space indentation, so no pasted list content is lost.
 */
function serializeListItemContent(item: TTNode): string {
  return (item.content ?? [])
    .filter((block) => block.type !== "bulletList" && block.type !== "orderedList")
    .map((block) => {
      if (block.type === "codeBlock")
        throw new Error("codeBlock inside listItem is not supported by the chat schema");
      return serializeInline(block.content ?? []);
    })
    .filter(Boolean)
    .join(" ");
}

function textContent(node: TTNode): string {
  if (node.type === "text") return node.text ?? "";
  return (node.content ?? []).map(textContent).join("");
}

function serializeList(node: TTNode, depth: number): string {
  const type: ListType = node.type === "orderedList" ? "ol" : "ul";
  const requestedStart = Number(node.attrs?.start ?? 1);
  const start = Number.isInteger(requestedStart) && requestedStart > 0 ? requestedStart : 1;
  return (node.content ?? [])
    .map((item, index) => {
      const line = formatListLine(type, depth, index, serializeListItemContent(item), start);
      const children = (item.content ?? [])
        .filter((block) => block.type === "bulletList" || block.type === "orderedList")
        .map((child) => serializeList(child, depth + 1));
      return [line, ...children].join("\n");
    })
    .join("\n");
}

// ── Block serializer ──────────────────────────────────────────────────────────

function serializeBlock(node: TTNode): string {
  switch (node.type) {
    case "codeBlock":
      return (
        CODE_BLOCK_MARKER + "\n" + escapeRichText(textContent(node)) + "\n" + CODE_BLOCK_MARKER
      );
    case "bulletList":
    case "orderedList":
      return serializeList(node, 0);
    default:
      // paragraph and any other block
      return serializeInline(node.content ?? []);
  }
}

// ── Public API ────────────────────────────────────────────────────────────────

/**
 * Serializes a TipTap/ProseMirror JSON document to the Markdown string
 * stored in body_text.
 */
export function tiptapDocToMarkdown(doc: TTNode): string {
  return (doc.content ?? []).map(serializeBlock).join("\n");
}
