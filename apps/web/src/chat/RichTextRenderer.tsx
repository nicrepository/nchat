/**
 * RichTextRenderer — client-side Markdown-like renderer for chat messages.
 *
 * Security: never uses dangerouslySetInnerHTML. All output is React nodes.
 * XSS-safe by construction: strings become text nodes, never markup.
 *
 * Supported:  **bold**  *italic*  `code`  ```block```  - ul  1. ol
 *
 * ponytail: no nested formatting, no auto-links, no tables — add when spec grows.
 */

import { Fragment } from "react";
import type { ReactNode } from "react";
import {
  BOLD_MARKER,
  ITALIC_MARKER,
  CODE_MARKER,
  INLINE_RE,
  isCodeFence,
  isUlLine,
  isOlLine,
} from "./richTextMarkers";

// ── Inline tokenizer (returns data, not JSX) ──────────────────────────────────

type InlineToken = string | { type: "bold" | "italic" | "code"; text: string };

function tokenizeInline(text: string): InlineToken[] {
  return text.split(INLINE_RE).flatMap((chunk): InlineToken[] => {
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

function renderTokens(tokens: InlineToken[], keyPrefix: string): ReactNode[] {
  return tokens.map((t, i): ReactNode => {
    if (typeof t === "string") return t; // plain text → text node, XSS-safe
    const k = `${keyPrefix}-${i}`;
    if (t.type === "bold") return <strong key={k}>{t.text}</strong>;
    if (t.type === "code")
      return (
        <code key={k} className="rtr-inline-code">
          {t.text}
        </code>
      );
    return <em key={k}>{t.text}</em>;
  });
}

// ── Block types ───────────────────────────────────────────────────────────────

// ponytail: `lang` removed — unused until syntax highlighting is spec'd (YAGNI).
type Block =
  | { type: "code"; content: string }
  | { type: "ul"; items: string[] }
  | { type: "ol"; items: string[] }
  | { type: "para"; lines: string[] };

// ── Block sub-parsers (each CC <= 4) ─────────────────────────────────────────

function parseCodeFence(lines: string[], i: number): [Block, number] {
  const codeLines: string[] = [];
  i++; // skip opening fence (which may carry a lang tag — ignored)
  while (i < lines.length && !isCodeFence(lines[i])) {
    codeLines.push(lines[i]);
    i++;
  }
  if (i < lines.length) i++; // skip closing fence
  return [{ type: "code", content: codeLines.join("\n") }, i];
}

function parseUlBlock(lines: string[], i: number): [Block, number] {
  const items: string[] = [];
  while (i < lines.length && isUlLine(lines[i])) {
    items.push(lines[i].slice(2));
    i++;
  }
  return [{ type: "ul", items }, i];
}

function parseOlBlock(lines: string[], i: number): [Block, number] {
  const items: string[] = [];
  while (i < lines.length && isOlLine(lines[i])) {
    items.push(lines[i].replace(/^\d+\. /, ""));
    i++;
  }
  return [{ type: "ol", items }, i];
}

function parseParagraph(lines: string[], i: number): [{ type: "para"; lines: string[] }, number] {
  const paraLines: string[] = [];
  while (i < lines.length && !isCodeFence(lines[i]) && !isUlLine(lines[i]) && !isOlLine(lines[i])) {
    paraLines.push(lines[i]);
    i++;
  }
  return [{ type: "para", lines: paraLines }, i];
}

// ── Block dispatcher (CC = 5) ─────────────────────────────────────────────────

function parseBlocks(text: string): Block[] {
  const lines = text.split("\n");
  const blocks: Block[] = [];
  let i = 0;
  while (i < lines.length) {
    const line = lines[i];
    if (isCodeFence(line)) {
      const [block, next] = parseCodeFence(lines, i);
      blocks.push(block);
      i = next;
    } else if (isUlLine(line)) {
      const [block, next] = parseUlBlock(lines, i);
      blocks.push(block);
      i = next;
    } else if (isOlLine(line)) {
      const [block, next] = parseOlBlock(lines, i);
      blocks.push(block);
      i = next;
    } else {
      const [para, next] = parseParagraph(lines, i);
      if (para.lines.some((l) => l.length > 0)) blocks.push(para);
      i = next;
    }
  }
  return blocks;
}

// ── Component ─────────────────────────────────────────────────────────────────

interface RichTextRendererProps {
  text: string;
}

export default function RichTextRenderer({ text }: RichTextRendererProps) {
  if (!text) return null;

  return (
    <>
      {parseBlocks(text).map((block, bi) => {
        if (block.type === "code") {
          return (
            <pre key={bi} className="rtr-code-block">
              <code>{block.content}</code>
            </pre>
          );
        }

        if (block.type === "ul") {
          return (
            <ul key={bi} className="rtr-list">
              {block.items.map((item, ii) => (
                <li key={ii}>{renderTokens(tokenizeInline(item), `${bi}-${ii}`)}</li>
              ))}
            </ul>
          );
        }

        if (block.type === "ol") {
          return (
            <ol key={bi} className="rtr-list">
              {block.items.map((item, ii) => (
                <li key={ii}>{renderTokens(tokenizeInline(item), `${bi}-${ii}`)}</li>
              ))}
            </ol>
          );
        }

        return (
          <Fragment key={bi}>
            {block.lines.map((line, li, arr) => (
              <Fragment key={li}>
                {renderTokens(tokenizeInline(line), `${bi}-${li}`)}
                {li < arr.length - 1 && <br />}
              </Fragment>
            ))}
          </Fragment>
        );
      })}
    </>
  );
}
