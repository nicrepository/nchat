/**
 * The plain-text preview of a message body, for the surfaces that announce a
 * message instead of rendering it (issue #749).
 *
 * Two surfaces need exactly this and they must agree: the OS-level notification
 * and the in-app toast. A body on the wire carries the rich-text v3 grammar,
 * whose mention tokens are not text a reader should ever see — a preview that
 * shows `@[user:<uuid>|Ana]` leaks an internal identifier and reads as markup.
 *
 * It is text, and only text: the result is rendered as data by both callers
 * (JSX for the toast, the Notification `body` for the OS), never as HTML.
 */

import { MENTION_TOKEN_RE, unescapeRichTextV3 } from "./richTextMarkers";

/** How much of the body a preview shows before it is elided. */
export const MESSAGE_PREVIEW_MAX_LENGTH = 140;

// Reuse the canonical (anchored) mention grammar unanchored and globally,
// instead of re-deriving the token pattern, to swap each raw token for its
// readable label.
const MENTION_TOKEN_GLOBAL_RE = new RegExp(MENTION_TOKEN_RE.source.replace(/^\^/, ""), "gi");

export function buildMessagePreview(
  bodyText: string,
  maxLength: number = MESSAGE_PREVIEW_MAX_LENGTH,
): string {
  MENTION_TOKEN_GLOBAL_RE.lastIndex = 0;
  const withLabels = bodyText.replace(
    MENTION_TOKEN_GLOBAL_RE,
    (_match, label: string) => `@${unescapeRichTextV3(label)}`,
  );
  return withLabels.length > maxLength ? `${withLabels.slice(0, maxLength - 1)}…` : withLabels;
}
