/**
 * XSS-safe renderer for the canonical chat grammar in richTextMarkers.ts.
 * Strings remain React text nodes; HTML is never interpreted here.
 */

import { Fragment } from "react";
import type { ReactNode } from "react";
import {
  BOLD_MARKER,
  CODE_MARKER,
  INLINE_MARKERS,
  ITALIC_MARKER,
  LEGACY_INLINE_RE,
  MENTION_TOKEN_RE,
  findUnescapedMarker,
  isCodeFence,
  parseLegacyListLine,
  parseListLine,
  unescapeRichText,
  unescapeRichTextV3,
} from "./richTextMarkers";
import type { InlineMarkerType, ListType } from "./richTextMarkers";
import type { MessageBodyFormat } from "./chatTypes";

type InlineToken =
  | string
  | { type: InlineMarkerType; text: string }
  | { type: "mention"; text: string; mentionType: "user" | "channel"; id: string };

function tokenizeV1Inline(text: string): InlineToken[] {
  return text.split(LEGACY_INLINE_RE).flatMap((chunk): InlineToken[] => {
    if (!chunk) return [];
    if (chunk.startsWith(BOLD_MARKER) && chunk.endsWith(BOLD_MARKER))
      return [{ type: "bold", text: chunk.slice(BOLD_MARKER.length, -BOLD_MARKER.length) }];
    if (chunk.startsWith(CODE_MARKER) && chunk.endsWith(CODE_MARKER))
      return [{ type: "code", text: chunk.slice(CODE_MARKER.length, -CODE_MARKER.length) }];
    if (chunk.startsWith(ITALIC_MARKER) && chunk.endsWith(ITALIC_MARKER))
      return [{ type: "italic", text: chunk.slice(ITALIC_MARKER.length, -ITALIC_MARKER.length) }];
    return [chunk];
  });
}

function tokenizeV2Inline(text: string): InlineToken[] {
  const tokens: InlineToken[] = [];
  let plain = "";
  let i = 0;

  const flushPlain = () => {
    if (plain) tokens.push(unescapeRichText(plain));
    plain = "";
  };

  while (i < text.length) {
    if (text[i] === "\\" && i + 1 < text.length) {
      plain += text.slice(i, i + 2);
      i += 2;
      continue;
    }

    const opening = INLINE_MARKERS.find(({ marker }) => text.startsWith(marker, i));
    if (opening) {
      const contentStart = i + opening.marker.length;
      const closing = findUnescapedMarker(text, opening.marker, contentStart);
      if (closing > contentStart) {
        flushPlain();
        tokens.push({
          type: opening.type,
          text: unescapeRichText(text.slice(contentStart, closing)),
        });
        i = closing + opening.marker.length;
        continue;
      }
    }

    plain += text[i];
    i++;
  }

  flushPlain();
  return tokens;
}

function tokenizeV3Inline(text: string): InlineToken[] {
  const tokens: InlineToken[] = [];
  let plain = "";
  let i = 0;

  const flushPlain = () => {
    if (plain) tokens.push(unescapeRichTextV3(plain));
    plain = "";
  };

  while (i < text.length) {
    if (text[i] === "\\" && i + 1 < text.length) {
      plain += text.slice(i, i + 2);
      i += 2;
      continue;
    }

    const mention = MENTION_TOKEN_RE.exec(text.slice(i));
    if (mention) {
      flushPlain();
      tokens.push({
        type: "mention",
        text: unescapeRichTextV3(mention[1]),
        mentionType: mention[2] as "user" | "channel",
        id: mention[3].toLowerCase(),
      });
      i += mention[0].length;
      continue;
    }

    const opening = INLINE_MARKERS.find(({ marker }) => text.startsWith(marker, i));
    if (opening) {
      const contentStart = i + opening.marker.length;
      const closing = findUnescapedMarker(text, opening.marker, contentStart);
      if (closing > contentStart) {
        flushPlain();
        tokens.push({
          type: opening.type,
          text: unescapeRichTextV3(text.slice(contentStart, closing)),
        });
        i = closing + opening.marker.length;
        continue;
      }
    }

    plain += text[i++];
  }

  flushPlain();
  return tokens;
}

const tokenizeInline = (text: string, format: MessageBodyFormat): InlineToken[] =>
  format === "v3"
    ? tokenizeV3Inline(text)
    : format === "v2"
      ? tokenizeV2Inline(text)
      : tokenizeV1Inline(text);

function renderTokens(tokens: InlineToken[], keyPrefix: string): ReactNode[] {
  return tokens.map((token, index): ReactNode => {
    if (typeof token === "string") return token;
    const key = `${keyPrefix}-${index}`;
    if (token.type === "mention")
      return (
        <span
          key={key}
          className="rtr-mention"
          data-mention-type={token.mentionType}
          data-mention-id={token.id}
        >
          @{token.text}
        </span>
      );
    if (token.type === "bold") return <strong key={key}>{token.text}</strong>;
    if (token.type === "boldItalic")
      return (
        <strong key={key}>
          <em>{token.text}</em>
        </strong>
      );
    if (token.type === "code")
      return (
        <code key={key} className="rtr-inline-code">
          {token.text}
        </code>
      );
    return <em key={key}>{token.text}</em>;
  });
}

interface ListItemBlock {
  text: string;
  children: ListBlock[];
}

interface ListBlock {
  type: ListType;
  start: number;
  items: ListItemBlock[];
}

type Block = { type: "code"; content: string } | ListBlock | { type: "para"; lines: string[] };

function parseCodeFence(lines: string[], i: number, format: MessageBodyFormat): [Block, number] {
  const codeLines: string[] = [];
  i++;
  while (i < lines.length && !isCodeFence(lines[i])) codeLines.push(lines[i++]);
  if (i < lines.length) i++;
  const content = codeLines.join("\n");
  return [
    {
      type: "code",
      content:
        format === "v3"
          ? unescapeRichTextV3(content)
          : format === "v2"
            ? unescapeRichText(content)
            : content,
    },
    i,
  ];
}

function parseListBlock(
  lines: string[],
  i: number,
  depth: number,
  type: ListType,
  format: MessageBodyFormat,
): [ListBlock, number] {
  const items: ListItemBlock[] = [];
  const parseLine = format === "v1" ? parseLegacyListLine : parseListLine;
  const start = parseLine(lines[i])?.index ?? 1;

  while (i < lines.length) {
    const line = parseLine(lines[i]);
    if (!line || line.depth < depth) break;
    if (line.depth > depth) {
      if (!items.length) break;
      const [child, next] = parseListBlock(lines, i, line.depth, line.type, format);
      items[items.length - 1].children.push(child);
      i = next;
      continue;
    }
    if (line.type !== type) break;
    items.push({ text: line.text, children: [] });
    i++;
  }

  return [{ type, start: format === "v1" ? 1 : start, items }, i];
}

function parseParagraph(
  lines: string[],
  i: number,
  format: MessageBodyFormat,
): [{ type: "para"; lines: string[] }, number] {
  const paraLines: string[] = [];
  const parseLine = format === "v1" ? parseLegacyListLine : parseListLine;
  while (i < lines.length && !isCodeFence(lines[i]) && !parseLine(lines[i])) {
    paraLines.push(lines[i++]);
  }
  return [{ type: "para", lines: paraLines }, i];
}

function parseBlocks(text: string, format: MessageBodyFormat): Block[] {
  const lines = text.split("\n");
  const blocks: Block[] = [];
  let i = 0;

  while (i < lines.length) {
    if (isCodeFence(lines[i])) {
      const [block, next] = parseCodeFence(lines, i, format);
      blocks.push(block);
      i = next;
      continue;
    }

    const listLine = (format === "v1" ? parseLegacyListLine : parseListLine)(lines[i]);
    if (listLine) {
      const [block, next] = parseListBlock(lines, i, listLine.depth, listLine.type, format);
      blocks.push(block);
      i = next;
      continue;
    }

    const [paragraph, next] = parseParagraph(lines, i, format);
    if (paragraph.lines.some((line) => line.length > 0)) blocks.push(paragraph);
    i = next;
  }

  return blocks;
}

function renderListItems(
  items: ListItemBlock[],
  keyPrefix: string,
  format: MessageBodyFormat,
): ReactNode[] {
  return items.map((item, index) => (
    <li key={index}>
      {renderTokens(tokenizeInline(item.text, format), `${keyPrefix}-${index}`)}
      {item.children.map((child, childIndex) =>
        renderList(child, `${keyPrefix}-${index}-${childIndex}`, format),
      )}
    </li>
  ));
}

function renderList(block: ListBlock, key: string, format: MessageBodyFormat): ReactNode {
  const items = renderListItems(block.items, key, format);
  return block.type === "ul" ? (
    <ul key={key} className="rtr-list">
      {items}
    </ul>
  ) : (
    <ol key={key} className="rtr-list" start={block.start}>
      {items}
    </ol>
  );
}

export interface RichTextRendererProps {
  text: string;
  bodyFormat?: MessageBodyFormat;
}

export default function RichTextRenderer({ text, bodyFormat = "v1" }: RichTextRendererProps) {
  if (!text) return null;

  return (
    <>
      {parseBlocks(text, bodyFormat).map((block, blockIndex) => {
        if (block.type === "code") {
          return (
            <pre key={blockIndex} className="rtr-code-block">
              <code>{block.content}</code>
            </pre>
          );
        }
        if (block.type !== "para") {
          return renderList(block, String(blockIndex), bodyFormat);
        }
        return (
          <Fragment key={blockIndex}>
            {block.lines.map((line, lineIndex, lines) => (
              <Fragment key={lineIndex}>
                {renderTokens(tokenizeInline(line, bodyFormat), `${blockIndex}-${lineIndex}`)}
                {lineIndex < lines.length - 1 && <br />}
              </Fragment>
            ))}
          </Fragment>
        );
      })}
    </>
  );
}
