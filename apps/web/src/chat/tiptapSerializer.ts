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
  BOLD_MARKER,
  BOLD_ITALIC_MARKER,
  CODE_BLOCK_MARKER,
  CODE_MARKER,
  INLINE_MARKERS,
  ITALIC_MARKER,
  LEGACY_INLINE_RE,
  MENTION_TOKEN_RE,
  buildMentionToken,
  escapeRichText,
  escapeRichTextV3,
  findUnescapedMarker,
  formatListLine,
  isCodeFence,
  parseLegacyListLine,
  parseListLine,
  unescapeRichText,
  unescapeRichTextV3,
} from "./richTextMarkers";
import type { ListType } from "./richTextMarkers";
import type { MessageBodyFormat } from "./chatTypes";

// ── ProseMirror node shape ────────────────────────────────────────────────────

/** Minimal ProseMirror node type — no runtime dep on @tiptap/core needed. */
export interface TTNode {
  type?: string;
  text?: string;
  marks?: Array<{ type: string }>;
  attrs?: Record<string, unknown>;
  content?: TTNode[];
}

export type CodecFormat = "v2" | "v3";

// ── Inline mark application ───────────────────────────────────────────────────

/**
 * Applies inline marks to a text string.
 *
 * Code is exclusive in the configured TipTap schema. Bold and italic combine.
 */
export function applyMarks(
  text: string,
  marks: Array<{ type: string }>,
  format: CodecFormat = "v2",
): string {
  const types = new Set(marks.map((m) => m.type));
  const escaped = format === "v3" ? escapeRichTextV3(text) : escapeRichText(text);
  if (types.has("code")) return CODE_MARKER + escaped + CODE_MARKER;
  if (types.has("bold") && types.has("italic"))
    return BOLD_ITALIC_MARKER + escaped + BOLD_ITALIC_MARKER;
  if (types.has("bold")) return BOLD_MARKER + escaped + BOLD_MARKER;
  if (types.has("italic")) return ITALIC_MARKER + escaped + ITALIC_MARKER;
  return escaped;
}

// ── Inline serializer ─────────────────────────────────────────────────────────

function serializeMention(node: TTNode): string {
  const mentionType = node.attrs?.mentionType;
  const id = node.attrs?.id;
  const label = node.attrs?.label;
  if (
    (mentionType !== "user" && mentionType !== "channel" && mentionType !== "all") ||
    !id ||
    !label
  ) {
    throw new Error("invalid mention node: mentionType, id, and label are required");
  }
  return buildMentionToken(mentionType, String(id), String(label));
}

export function serializeInline(nodes: TTNode[], format: CodecFormat): string {
  return nodes
    .map((n) => {
      if (n.type === "hardBreak") return "\n";
      if (n.type === "mention") {
        if (format !== "v3") throw new Error("mention nodes require body format v3");
        return serializeMention(n);
      }
      if (n.type === "text") return applyMarks(n.text ?? "", n.marks ?? [], format);
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
function serializeListItemContent(item: TTNode, format: CodecFormat): string {
  return (item.content ?? [])
    .filter((block) => block.type !== "bulletList" && block.type !== "orderedList")
    .map((block) => {
      if (block.type === "codeBlock")
        throw new Error("codeBlock inside listItem is not supported by the chat schema");
      return serializeInline(block.content ?? [], format);
    })
    .filter(Boolean)
    .join(" ");
}

function textContent(node: TTNode): string {
  if (node.type === "text") return node.text ?? "";
  return (node.content ?? []).map(textContent).join("");
}

function serializeList(node: TTNode, depth: number, format: CodecFormat): string {
  const type: ListType = node.type === "orderedList" ? "ol" : "ul";
  const requestedStart = Number(node.attrs?.start ?? 1);
  const start = Number.isInteger(requestedStart) && requestedStart > 0 ? requestedStart : 1;
  return (node.content ?? [])
    .map((item, index) => {
      const line = formatListLine(
        type,
        depth,
        index,
        serializeListItemContent(item, format),
        start,
      );
      const children = (item.content ?? [])
        .filter((block) => block.type === "bulletList" || block.type === "orderedList")
        .map((child) => serializeList(child, depth + 1, format));
      return [line, ...children].join("\n");
    })
    .join("\n");
}

// ── Block serializer ──────────────────────────────────────────────────────────

function serializeBlock(node: TTNode, format: CodecFormat): string {
  switch (node.type) {
    case "codeBlock":
      return (
        CODE_BLOCK_MARKER +
        "\n" +
        (format === "v3"
          ? escapeRichTextV3(textContent(node))
          : escapeRichText(textContent(node))) +
        "\n" +
        CODE_BLOCK_MARKER
      );
    case "bulletList":
    case "orderedList":
      return serializeList(node, 0, format);
    default:
      // paragraph and any other block
      return serializeInline(node.content ?? [], format);
  }
}

// ── Public API ────────────────────────────────────────────────────────────────

/**
 * Serializes a TipTap/ProseMirror JSON document to the Markdown string
 * stored in body_text.
 */
export function tiptapDocToMarkdown(doc: TTNode, format: CodecFormat): string {
  return (doc.content ?? []).map((node) => serializeBlock(node, format)).join("\n");
}

// ── Stored body → TipTap document ────────────────────────────────────────────

function textNode(text: string, marks: Array<{ type: string }> = []): TTNode[] {
  return text ? [{ type: "text", text, ...(marks.length ? { marks } : {}) }] : [];
}

function markerMarks(type: string): Array<{ type: string }> {
  if (type === "boldItalic") return [{ type: "bold" }, { type: "italic" }];
  return [{ type }];
}

function legacyInline(text: string): TTNode[] {
  return text.split(LEGACY_INLINE_RE).flatMap((chunk) => {
    if (!chunk) return [];
    if (chunk.startsWith(BOLD_MARKER) && chunk.endsWith(BOLD_MARKER))
      return textNode(chunk.slice(2, -2), [{ type: "bold" }]);
    if (chunk.startsWith(CODE_MARKER) && chunk.endsWith(CODE_MARKER))
      return textNode(chunk.slice(1, -1), [{ type: "code" }]);
    if (chunk.startsWith(ITALIC_MARKER) && chunk.endsWith(ITALIC_MARKER))
      return textNode(chunk.slice(1, -1), [{ type: "italic" }]);
    return textNode(chunk);
  });
}

function canonicalInline(text: string, format: "v2" | "v3"): TTNode[] {
  const nodes: TTNode[] = [];
  const unescape = format === "v3" ? unescapeRichTextV3 : unescapeRichText;
  let plain = "";
  let index = 0;
  const flush = () => {
    nodes.push(...textNode(unescape(plain)));
    plain = "";
  };

  while (index < text.length) {
    if (text[index] === "\\" && index + 1 < text.length) {
      plain += text.slice(index, index + 2);
      index += 2;
      continue;
    }
    if (format === "v3") {
      const mention = MENTION_TOKEN_RE.exec(text.slice(index));
      if (mention) {
        flush();
        nodes.push({
          type: "mention",
          attrs: {
            label: unescapeRichTextV3(mention[1]),
            mentionType: mention[2],
            id: mention[3].toLowerCase(),
          },
        });
        index += mention[0].length;
        continue;
      }
    }
    const opening = INLINE_MARKERS.find(({ marker }) => text.startsWith(marker, index));
    if (opening) {
      const start = index + opening.marker.length;
      const end = findUnescapedMarker(text, opening.marker, start);
      if (end > start) {
        flush();
        nodes.push(...textNode(unescape(text.slice(start, end)), markerMarks(opening.type)));
        index = end + opening.marker.length;
        continue;
      }
    }
    plain += text[index++];
  }
  flush();
  return nodes;
}

function parseInline(text: string, format: MessageBodyFormat): TTNode[] {
  return format === "v1" ? legacyInline(text) : canonicalInline(text, format);
}

function parseList(
  lines: string[],
  startAt: number,
  depth: number,
  format: MessageBodyFormat,
): [TTNode, number] {
  const parseLine = format === "v1" ? parseLegacyListLine : parseListLine;
  const first = parseLine(lines[startAt])!;
  const type = first.type;
  const items: TTNode[] = [];
  let index = startAt;

  while (index < lines.length) {
    const line = parseLine(lines[index]);
    if (!line || line.depth < depth) break;
    if (line.depth > depth) {
      if (!items.length) break;
      const [child, next] = parseList(lines, index, line.depth, format);
      items.at(-1)!.content!.push(child);
      index = next;
      continue;
    }
    if (line.type !== type) break;
    items.push({
      type: "listItem",
      content: [{ type: "paragraph", content: parseInline(line.text, format) }],
    });
    index++;
  }

  return [
    {
      type: type === "ol" ? "orderedList" : "bulletList",
      ...(type === "ol" ? { attrs: { start: first.index } } : {}),
      content: items,
    },
    index,
  ];
}

/** Decodes the persisted rich-text grammar into the shared TipTap schema. */
export function richTextToTiptapDoc(text: string, format: MessageBodyFormat): TTNode {
  const lines = text.split("\n");
  const content: TTNode[] = [];
  const parseLine = format === "v1" ? parseLegacyListLine : parseListLine;
  let index = 0;

  while (index < lines.length) {
    if (isCodeFence(lines[index])) {
      const code: string[] = [];
      index++;
      while (index < lines.length && !isCodeFence(lines[index])) code.push(lines[index++]);
      if (index < lines.length) index++;
      const unescape = format === "v3" ? unescapeRichTextV3 : unescapeRichText;
      content.push({
        type: "codeBlock",
        content: textNode(format === "v1" ? code.join("\n") : unescape(code.join("\n"))),
      });
      continue;
    }
    const listLine = parseLine(lines[index]);
    if (listLine) {
      const [list, next] = parseList(lines, index, listLine.depth, format);
      content.push(list);
      index = next;
      continue;
    }
    content.push({ type: "paragraph", content: parseInline(lines[index], format) });
    index++;
  }
  return { type: "doc", content: content.length ? content : [{ type: "paragraph" }] };
}
