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
import type { InlineMarkerType, ListType, MentionType } from "./richTextMarkers";
import { findAutolinks } from "./autolink";
import { useDirectMessagePending, type DirectMessagePendingSource } from "./directMessage";
import type { MessageBodyFormat } from "./chatTypes";
import { LINK_BLOCKED_MARKER, linkForText, type MessageLink } from "./messageLinks";
import MessageLinkSpan, { BlockedLinkChip } from "./MessageLinkSpan";

type InlineToken =
  | string
  | { type: InlineMarkerType; text: string }
  | { type: "mention"; text: string; mentionType: MentionType; id: string };

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
        mentionType: mention[2] as MentionType,
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

/**
 * The link entities and the interstitial callback, threaded through the
 * renderer as one value so the recursion over lists does not grow a parameter
 * per feature.
 */
export interface LinkRendering {
  links: readonly MessageLink[];
  onOpenUnverified?: (link: MessageLink, trigger: HTMLElement) => void;
}

/**
 * Splits one plain text run into text and link spans (issue #807).
 *
 * The scanner locates URL-looking spans in the text; it decides nothing. Each
 * span is looked up in the server's link entities by its exact text, and drawn
 * the way the entity says — anchor, interstitial button, pending note. A span
 * the server did not describe stays literal text: under-linking is the accepted
 * direction, over-linking to an address nobody checked is the one that must
 * not happen. The blocked marker the server substituted for a condemned URL is
 * drawn as the blocked chip.
 *
 * Applied to every text run — plain, bold, italic, bold-italic — through
 * renderInlineText. A URL inside an inline `code` span or a fenced code block
 * is a *different* token type and never reaches here.
 *
 * Nothing is fetched. The text is a React child and the href a React attribute,
 * so both are escaped by React. There is no `dangerouslySetInnerHTML` anywhere
 * in this file.
 */
function linkifyPlain(text: string, keyPrefix: string, rendering: LinkRendering): ReactNode {
  const parts: ReactNode[] = [];
  let cursor = 0;
  const emit = (end: number) => {
    if (end > cursor)
      parts.push(...withBlockedChips(text.slice(cursor, end), `${keyPrefix}-t${cursor}`));
  };
  findAutolinks(text).forEach((span, index) => {
    const link = linkForText(rendering.links, span.href);
    if (!link) return;
    emit(span.start);
    parts.push(
      <MessageLinkSpan
        key={`${keyPrefix}-a${index}`}
        link={link}
        text={span.href}
        onOpenUnverified={rendering.onOpenUnverified}
      />,
    );
    cursor = span.end;
  });
  emit(text.length);
  return parts.length === 1 && typeof parts[0] === "string" ? (
    parts[0]
  ) : (
    <Fragment key={`${keyPrefix}-link`}>{parts}</Fragment>
  );
}

/** Text with every blocked marker replaced by the chip. */
function withBlockedChips(text: string, keyPrefix: string): ReactNode[] {
  if (!text.includes(LINK_BLOCKED_MARKER)) return [text];
  return text
    .split(LINK_BLOCKED_MARKER)
    .flatMap((piece, index) =>
      index === 0 ? [piece] : [<BlockedLinkChip key={`${keyPrefix}-b${index}`} />, piece],
    );
}

/**
 * One pipeline for every run of text a link may appear in: plain text and the
 * text inside bold, italic and bold-italic all go through the same
 * segmentation, so an emphasised URL is the same span — same entity, same
 * anchor or interstitial, same chip — as a plain one, only wrapped. Without
 * link entities the text is drawn as is. Inline code is not a text run here:
 * it keeps its literal content, deliberately.
 */
function renderInlineText(text: string, keyPrefix: string, rendering?: LinkRendering): ReactNode {
  return rendering ? linkifyPlain(text, keyPrefix, rendering) : text;
}

/**
 * Opt-in "open a DM" affordance for individual user mentions (issue #795).
 *
 * Omitted entirely for `@all`/other non-`"user"` mentions and for a mention
 * of the reader themself — those render exactly as before: a plain `<span>`,
 * inert and unfocusable, never a no-op control.
 */
export interface MentionInteraction {
  /**
   * Required, not optional: the self-mention guard below is entirely
   * `token.id !== currentUserId`, so a caller that forgot this field would
   * silently make every mention clickable, including of the reader.
   */
  currentUserId: string;
  onMentionClick: (mentionType: MentionType, id: string) => void;
  /**
   * Answers whether *one* person's DM is being resolved, for a discreet busy
   * state (issue #895).
   *
   * A read-only port rather than the set of everyone pending: this renderer
   * draws text, and handing it the set made every message holding a mention
   * re-render whenever anybody anywhere was being resolved. A mention names one
   * person and watches that one. It is also all the authority this file gets —
   * there is no way to start or cancel an operation from here.
   */
  pendingSource?: DirectMessagePendingSource;
}

/**
 * A clickable `@user` mention.
 *
 * A component rather than a branch inside `renderMentionToken`, for one reason:
 * the busy state has to be *reactive* per mention, and only a component can
 * subscribe. An inert mention stays a plain span and subscribes to nothing.
 */
function MentionButton({
  mentionToken,
  mention,
}: {
  /*
    Named `mentionToken` rather than the obvious shorter name: the repository's
    secret-marker check reads that shorter name, assigned in JSX, as a possible
    credential. What this carries is a parsed span of message text, so the prop
    is renamed rather than excused — see
    scripts/ci/governance-secret-markers-check.py.
  */
  mentionToken: Extract<InlineToken, { type: "mention" }>;
  mention: MentionInteraction;
}) {
  const pending = useDirectMessagePending(mention.pendingSource, mentionToken.id);
  const activate = () => mention.onMentionClick(mentionToken.mentionType, mentionToken.id);
  return (
    <span
      className="rtr-mention"
      data-mention-type={mentionToken.mentionType}
      data-mention-id={mentionToken.id}
      data-mention-clickable="true"
      role="button"
      tabIndex={0}
      aria-label={`Abrir conversa com ${mentionToken.text}`}
      aria-busy={pending || undefined}
      onClick={activate}
      onKeyDown={(event) => {
        if (event.key === "Enter" || event.key === " ") {
          event.preventDefault();
          activate();
        }
      }}
    >
      @{mentionToken.text}
    </span>
  );
}

/**
 * A `@user` mention token: inert text, or — when the caller opted into mention
 * navigation (issue #795) and the mention is of somebody else — a button that
 * opens the DM. Never an anchor, and never linkified: its destination is a
 * mention id, not a URL.
 */
function renderMentionToken(
  token: Extract<InlineToken, { type: "mention" }>,
  key: string,
  mention?: MentionInteraction,
): ReactNode {
  const clickable =
    mention !== undefined && token.mentionType === "user" && token.id !== mention.currentUserId;
  if (!clickable) {
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
  }
  return <MentionButton key={key} mentionToken={token} mention={mention} />;
}

function renderTokens(
  tokens: InlineToken[],
  keyPrefix: string,
  rendering?: LinkRendering,
  mention?: MentionInteraction,
): ReactNode[] {
  return tokens.map((token, index): ReactNode => {
    const key = `${keyPrefix}-${index}`;
    if (typeof token === "string") return renderInlineText(token, key, rendering);
    if (token.type === "mention") return renderMentionToken(token, key, mention);
    if (token.type === "bold")
      return <strong key={key}>{renderInlineText(token.text, key, rendering)}</strong>;
    if (token.type === "boldItalic")
      return (
        <strong key={key}>
          <em>{renderInlineText(token.text, key, rendering)}</em>
        </strong>
      );
    if (token.type === "code")
      return (
        <code key={key} className="rtr-inline-code">
          {token.text}
        </code>
      );
    return <em key={key}>{renderInlineText(token.text, key, rendering)}</em>;
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
  rendering?: LinkRendering,
  mention?: MentionInteraction,
): ReactNode[] {
  return items.map((item, index) => (
    <li key={index}>
      {renderTokens(tokenizeInline(item.text, format), `${keyPrefix}-${index}`, rendering, mention)}
      {item.children.map((child, childIndex) =>
        renderList(child, `${keyPrefix}-${index}-${childIndex}`, format, rendering, mention),
      )}
    </li>
  ));
}

function renderList(
  block: ListBlock,
  key: string,
  format: MessageBodyFormat,
  rendering?: LinkRendering,
  mention?: MentionInteraction,
): ReactNode {
  const items = renderListItems(block.items, key, format, rendering, mention);
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
  /**
   * The server's link entities for this body (issue #807), and the callback an
   * unverified link opens the interstitial with.
   *
   * **Absent by default, and that default is the point.** A URL is drawn as a
   * link only when the server described it, so a new call site — a quote
   * preview, a reference card, an edit history entry — renders URLs as plain
   * text until somebody deliberately passes the entities the server sent for
   * that surface. Nothing is ever derived from `message.status` or from the
   * text itself.
   */
  links?: LinkRendering;
  /**
   * Enables the "click a mention to open a DM" affordance (issue #795).
   * Omitted by default for the same reason as `links`: only a caller that
   * actually wants mention navigation (the primary message body, not a
   * quote/reference/edit-history preview) should opt in.
   */
  mention?: MentionInteraction;
}

export default function RichTextRenderer({
  text,
  bodyFormat = "v1",
  links,
  mention,
}: RichTextRendererProps) {
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
          return renderList(block, String(blockIndex), bodyFormat, links, mention);
        }
        return (
          <Fragment key={blockIndex}>
            {block.lines.map((line, lineIndex, lines) => (
              <Fragment key={lineIndex}>
                {renderTokens(
                  tokenizeInline(line, bodyFormat),
                  `${blockIndex}-${lineIndex}`,
                  links,
                  mention,
                )}
                {lineIndex < lines.length - 1 && <br />}
              </Fragment>
            ))}
          </Fragment>
        );
      })}
    </>
  );
}
