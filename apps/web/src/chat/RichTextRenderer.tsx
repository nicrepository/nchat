/**
 * XSS-safe renderer for the canonical chat grammar in richTextMarkers.ts.
 * Strings remain React text nodes; HTML is never interpreted here.
 */

import { Fragment } from "react";
import type { ReactNode } from "react";
import {
  INLINE_MARKERS,
  findUnescapedMarker,
  isCodeFence,
  parseListLine,
  unescapeRichText,
} from "./richTextMarkers";
import type { InlineMarkerType, ListType } from "./richTextMarkers";

type InlineToken = string | { type: InlineMarkerType; text: string };

function tokenizeInline(text: string): InlineToken[] {
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

function renderTokens(tokens: InlineToken[], keyPrefix: string): ReactNode[] {
  return tokens.map((token, index): ReactNode => {
    if (typeof token === "string") return token;
    const key = `${keyPrefix}-${index}`;
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
  items: ListItemBlock[];
}

type Block = { type: "code"; content: string } | ListBlock | { type: "para"; lines: string[] };

function parseCodeFence(lines: string[], i: number): [Block, number] {
  const codeLines: string[] = [];
  i++;
  while (i < lines.length && !isCodeFence(lines[i])) codeLines.push(lines[i++]);
  if (i < lines.length) i++;
  return [{ type: "code", content: unescapeRichText(codeLines.join("\n")) }, i];
}

function parseListBlock(
  lines: string[],
  i: number,
  depth: number,
  type: ListType,
): [ListBlock, number] {
  const items: ListItemBlock[] = [];

  while (i < lines.length) {
    const line = parseListLine(lines[i]);
    if (!line || line.depth < depth) break;
    if (line.depth > depth) {
      if (!items.length) break;
      const [child, next] = parseListBlock(lines, i, line.depth, line.type);
      items[items.length - 1].children.push(child);
      i = next;
      continue;
    }
    if (line.type !== type) break;
    items.push({ text: line.text, children: [] });
    i++;
  }

  return [{ type, items }, i];
}

function parseParagraph(lines: string[], i: number): [{ type: "para"; lines: string[] }, number] {
  const paraLines: string[] = [];
  while (i < lines.length && !isCodeFence(lines[i]) && !parseListLine(lines[i])) {
    paraLines.push(lines[i++]);
  }
  return [{ type: "para", lines: paraLines }, i];
}

function parseBlocks(text: string): Block[] {
  const lines = text.split("\n");
  const blocks: Block[] = [];
  let i = 0;

  while (i < lines.length) {
    if (isCodeFence(lines[i])) {
      const [block, next] = parseCodeFence(lines, i);
      blocks.push(block);
      i = next;
      continue;
    }

    const listLine = parseListLine(lines[i]);
    if (listLine) {
      const [block, next] = parseListBlock(lines, i, listLine.depth, listLine.type);
      blocks.push(block);
      i = next;
      continue;
    }

    const [paragraph, next] = parseParagraph(lines, i);
    if (paragraph.lines.some((line) => line.length > 0)) blocks.push(paragraph);
    i = next;
  }

  return blocks;
}

function renderListItems(items: ListItemBlock[], keyPrefix: string): ReactNode[] {
  return items.map((item, index) => (
    <li key={index}>
      {renderTokens(tokenizeInline(item.text), `${keyPrefix}-${index}`)}
      {item.children.map((child, childIndex) =>
        renderList(child, `${keyPrefix}-${index}-${childIndex}`),
      )}
    </li>
  ));
}

function renderList(block: ListBlock, key: string): ReactNode {
  const items = renderListItems(block.items, key);
  return block.type === "ul" ? (
    <ul key={key} className="rtr-list">
      {items}
    </ul>
  ) : (
    <ol key={key} className="rtr-list">
      {items}
    </ol>
  );
}

export interface RichTextRendererProps {
  text: string;
}

export default function RichTextRenderer({ text }: RichTextRendererProps) {
  if (!text) return null;

  return (
    <>
      {parseBlocks(text).map((block, blockIndex) => {
        if (block.type === "code") {
          return (
            <pre key={blockIndex} className="rtr-code-block">
              <code>{block.content}</code>
            </pre>
          );
        }
        if (block.type === "ul" || block.type === "ol") {
          return renderList(block, String(blockIndex));
        }
        return (
          <Fragment key={blockIndex}>
            {block.lines.map((line, lineIndex, lines) => (
              <Fragment key={lineIndex}>
                {renderTokens(tokenizeInline(line), `${blockIndex}-${lineIndex}`)}
                {lineIndex < lines.length - 1 && <br />}
              </Fragment>
            ))}
          </Fragment>
        );
      })}
    </>
  );
}
