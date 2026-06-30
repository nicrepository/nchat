/**
 * tiptapSerializer — converts a TipTap/ProseMirror JSON document to the
 * Markdown-like string stored as body_text.
 *
 * This is the only serialization path. The backend contract (option A) is
 * Markdown strings; no HTML ever reaches the server.
 *
 * CODEC CONTRACT — must stay in sync with RichTextRenderer.tsx:
 * The grammar is declared in richTextMarkers.ts (INLINE_RE).
 * Marks are EXCLUSIVE: code > bold > italic (no combined marks).
 * bold+italic → bold wins; ***text*** is never generated (renderer can't parse it).
 */

import { BOLD_MARKER, CODE_MARKER, ITALIC_MARKER } from "./richTextMarkers";

// ── ProseMirror node shape ────────────────────────────────────────────────────

/** Minimal ProseMirror node type — no runtime dep on @tiptap/core needed. */
export interface TTNode {
  type?: string;
  text?: string;
  marks?: Array<{ type: string }>;
  content?: TTNode[];
}

// ── Inline mark application ───────────────────────────────────────────────────

/**
 * Applies inline marks to a text string.
 *
 * Priority: code > bold > italic — EXCLUSIVE, matching INLINE_RE in richTextMarkers.ts.
 * Combined marks (e.g. bold+italic) are unsupported by the renderer regex, so bold wins.
 * ponytail: combined bold+italic produces bold; extend when renderer supports ***text***.
 */
export function applyMarks(text: string, marks: Array<{ type: string }>): string {
  const types = new Set(marks.map((m) => m.type));
  if (types.has("code")) return CODE_MARKER + text + CODE_MARKER;
  if (types.has("bold")) return BOLD_MARKER + text + BOLD_MARKER;
  if (types.has("italic")) return ITALIC_MARKER + text + ITALIC_MARKER;
  return text;
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
 * Nested lists and code blocks are flattened to their inline text — chat does
 * not support nested list structure in stored Markdown.
 */
function serializeListItemContent(item: TTNode): string {
  return (item.content ?? [])
    .map((block) => {
      if (block.type === "codeBlock") return "`" + (block.content?.[0]?.text ?? "") + "`";
      // paragraph and anything else: extract inline content
      return serializeInline(block.content ?? []);
    })
    .filter(Boolean)
    .join(" ");
}

// ── Block serializer ──────────────────────────────────────────────────────────

function serializeBlock(node: TTNode): string {
  switch (node.type) {
    case "codeBlock":
      return "```\n" + (node.content?.[0]?.text ?? "") + "\n```";
    case "bulletList":
      return (node.content ?? []).map((item) => "- " + serializeListItemContent(item)).join("\n");
    case "orderedList":
      return (node.content ?? [])
        .map((item, i) => `${i + 1}. ` + serializeListItemContent(item))
        .join("\n");
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
