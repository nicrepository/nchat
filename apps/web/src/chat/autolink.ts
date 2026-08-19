/**
 * Finding the http(s) URLs in a message body so the renderer can draw them as
 * links (RF-21 / issue #135).
 *
 * # What this is, and firmly is not
 *
 * It turns a run of text into "this stretch is a URL" spans. That is all. It
 * never opens a connection: there is no fetch, no HEAD, no preload, no prefetch,
 * no image and no navigation here. A link is drawn because the *text* looks like
 * a URL and because the message's link-safety state permits it — never because
 * anything was asked about the address. A preview would be a server-side fetch,
 * and for an unverified link that is exactly what stays forbidden.
 *
 * # Why dangerous schemes are unreachable rather than filtered
 *
 * The scanner matches the literal prefixes `http://` and `https://` and nothing
 * else. `javascript:`, `data:`, `file:`, `blob:`, `vbscript:` and every other
 * scheme are not rejected by a denylist — they are never matched in the first
 * place, because the scheme is part of the pattern instead of being parsed out of
 * whatever the user wrote. A denylist is a list somebody has to keep complete;
 * this is a property of the match.
 *
 * `new URL()` then re-checks the parsed protocol anyway. Belt and braces: this is
 * the one place in the client that turns user text into an `href`.
 *
 * # Why it mirrors the backend scanner
 *
 * chat-service extracts the URLs it submits for scanning with the same rules —
 * see `scanURLCandidates` in services/chat-service/internal/service/link_safety.go:
 * both schemes matched case-insensitively, the URL ending at the first whitespace
 * or control character, and the same trailing punctuation given back to the
 * sentence. Drift between the two is the failure that matters: a link the
 * renderer makes clickable but the backend never extracted is a link nobody
 * checked. Where the two disagree, this side must draw *fewer* links, never more,
 * which is why anything that does not parse is left as plain text.
 */

/**
 * Trailing characters that belong to the sentence rather than to the URL, when
 * they are not part of a pair the URL itself opened.
 *
 * The closing brackets are handled separately — see trimTrailing. Stripping them
 * unconditionally breaks perfectly ordinary addresses:
 * `https://example.test/wiki/Function_(mathematics)` would lose its `)` and link
 * to the wrong page.
 */
const sentenceTrailers = `.,;:!?'"<>`;

/** Closing delimiters, paired with the opener that makes them part of the URL. */
const closingDelimiters: Record<string, string> = { ")": "(", "]": "[", "}": "{" };

/** The two schemes, lowercase. Nothing else is ever matched. */
const schemes = ["https://", "http://"] as const;

export interface LinkSpan {
  /** Byte offset of the first character of the URL in the source text. */
  start: number;
  /** Offset one past the last character of the URL. */
  end: number;
  /**
   * The URL, exactly as the reader sees it in the message.
   *
   * Deliberately the matched substring rather than `new URL(...).href`: the
   * normalised form can differ from the text on screen (an added trailing slash,
   * a re-encoded query), and an anchor whose destination is not the address next
   * to it is a phishing primitive however benign the cause.
   */
  href: string;
}

/**
 * Reports whether `text` starts with `prefix` at `index`, comparing ASCII
 * letters without regard to case.
 *
 * Both schemes are pure ASCII and a UTF-8 continuation byte is never in the
 * ASCII range, so a match can only ever begin at a character boundary. This is
 * the same argument `indexOfScheme` makes on the Go side, and it is why no
 * lowercased copy of the text is needed — building one would shift every offset,
 * because case folding is not length-preserving for every character.
 */
function hasSchemeAt(text: string, index: number, prefix: string): boolean {
  if (index + prefix.length > text.length) return false;
  for (let i = 0; i < prefix.length; i++) {
    let char = text.charCodeAt(index + i);
    if (char >= 65 && char <= 90) char += 32; // ASCII upper -> lower
    if (char !== prefix.charCodeAt(i)) return false;
  }
  return true;
}

/**
 * Characters that cannot immediately precede the start of a URL.
 *
 * Without this the scanner matches a scheme *anywhere*, including inside another
 * token, and three real cases fall out of that:
 *
 *   - `shttp://example.test` would linkify `http://example.test`, an address the
 *     sender never wrote;
 *   - `blob:https://example.test/x` would linkify the inner `https://…`, lending
 *     an anchor to a blob URL's payload;
 *   - `https://a.test/r?u=/https://b.test` would offer the nested address as a
 *     separate destination.
 *
 * So a URL may only begin at the start of the text or after a character that
 * cannot be part of a scheme or an authority: letters, digits, `+`, `-`, `.` and
 * `:` are scheme characters, and `/` and `@` are the two ways one URL embeds
 * another. Ordinary sentence characters — space, `(`, quotes, `,` — are all
 * fine, so the common case is untouched.
 *
 * chat-service's extractor does not apply this rule, and the asymmetry is
 * deliberate: there, a spurious extra candidate costs one wasted scan and is
 * conservative. Here it would cost an anchor the reader did not write, so this
 * side draws strictly fewer links. That is the direction drift is allowed to go.
 */
const schemeBoundaryExcluded = /[A-Za-z0-9+\-.:@/]/;

/** A URL ends at the first whitespace or control character. */
function isTerminator(char: string): boolean {
  const code = char.charCodeAt(0);
  return code <= 0x20 || code === 0x7f;
}

/**
 * Characters this parser refuses to see inside a candidate.
 *
 * All of them are invisible or direction-altering, and all of them exist in a URL
 * for one reason: to make the address a reader sees differ from the address the
 * browser resolves. `new URL()` keeps them; Go's `net/url` keeps them; but the
 * *rendered* string is what a reader judges the link by, so a candidate carrying
 * one is refused rather than turned into an anchor whose label lies.
 *
 * Listed by code point rather than by a category test so the set is auditable:
 * bidi overrides and isolates, the zero-width family, and the BOM.
 */
const deceptiveCharacters = /[\u061C\u200B-\u200F\u202A-\u202E\u2060-\u2064\u2066-\u2069\uFEFF]/;

/**
 * Reports whether every percent escape in the candidate is well formed.
 *
 * This is one of the places `net/url` and WHATWG genuinely disagree: Go's
 * `url.Parse` rejects an invalid escape outright, while the WHATWG parser is
 * lenient and keeps it verbatim. So a candidate containing `%zz` is a URL to the
 * browser and not a URL to the backend — the backend would refuse to canonicalise
 * it and never scan it, and an anchor would then point somewhere unchecked.
 *
 * Refused here, which keeps the client strictly the more conservative of the two.
 */
function hasOnlyValidPercentEscapes(candidate: string): boolean {
  for (let i = candidate.indexOf("%"); i >= 0; i = candidate.indexOf("%", i + 1)) {
    if (!/^%[0-9A-Fa-f]{2}/.test(candidate.slice(i, i + 3))) return false;
  }
  return true;
}

/**
 * Reports whether a candidate may be rendered as an anchor.
 *
 * The checks fall into two groups, and the second group is the important one.
 *
 * The first group is ordinary validity: it parses, its protocol really is http or
 * https, it names a host, and it carries no credentials. (The prefix match already
 * guarantees the protocol, so a disagreement means the parser saw something the
 * scanner did not — a reason to refuse rather than to trust either.)
 *
 * The second group exists because this client and chat-service parse URLs with
 * *different parsers*: WHATWG here, `net/url` there. Wherever they can read the
 * same bytes differently, an anchor could point at a host the backend never
 * scanned. So those inputs are refused outright:
 *
 *   - a literal backslash. WHATWG normalises `\` to `/` inside a special scheme,
 *     so `https://good.test\@evil.test` has host `evil.test` to a browser and
 *     host `good.test` to Go. That is the whole attack in one character;
 *   - an invalid percent escape, which Go rejects and WHATWG keeps;
 *   - an invisible or direction-altering character, which makes the rendered
 *     label differ from the resolved address.
 *
 * Under-linking is the accepted cost. Over-linking to a URL that was never
 * scanned is the thing that must not happen.
 */
function isRenderableURL(candidate: string): boolean {
  if (candidate.includes("\\")) return false;
  if (deceptiveCharacters.test(candidate)) return false;
  if (!hasOnlyValidPercentEscapes(candidate)) return false;

  let parsed: URL;
  try {
    parsed = new URL(candidate);
  } catch {
    return false;
  }
  if (parsed.protocol !== "http:" && parsed.protocol !== "https:") return false;
  if (parsed.hostname === "") return false;
  // WHATWG canonicalizes legacy IPv4 spellings (127.1, octal, hex) before
  // exposing hostname. IP literals are not checkable server-side, so never draw
  // an anchor for either the ordinary or normalized spelling.
  if (/^\d{1,3}(?:\.\d{1,3}){3}$/.test(parsed.hostname) || parsed.hostname.includes(":")) {
    return false;
  }
  if (parsed.username !== "" || parsed.password !== "") return false;
  return true;
}

/**
 * Returns the end offset of the candidate once the characters belonging to the
 * surrounding sentence have been given back.
 *
 * **Identical to `trimTrailingDelimiters` in
 * services/chat-service/internal/service/link_safety.go**, and it has to be: that
 * function decides which URLs get scanned, this one decides which get anchors, and
 * a disagreement means a clickable link to an address nobody checked. The shared
 * corpus in libs/testdata/link-safety/autolink-corpus.json holds the two together.
 *
 * Ordinary punctuation always goes back. A closing bracket goes back only when it
 * is *unbalanced* within the candidate — when the candidate holds no unmatched
 * opener for it, which means the bracket was wrapping the URL rather than living
 * inside it:
 *
 *	https://example.test/a_(b)     the ) is balanced, kept
 *	(https://example.test/foo)     the ) is unbalanced, given back
 *	https://example.test/a_[b]     the ] is balanced, kept
 *	https://example.test/foo].     both given back, right to left
 *
 * Counting is textual and deliberately shallow: a percent-encoded `%28` is not a
 * bracket to a reader and does not participate, which falls out of counting
 * literal characters only. This is a boundary heuristic, not a URL parser.
 */
function trimTrailingDelimiters(text: string, start: number, end: number): number {
  let stop = end;
  // Right to left, so "foo]." peels the dot and then the bracket.
  for (;;) {
    if (stop <= start) return stop;
    const last = text[stop - 1];
    if (sentenceTrailers.includes(last)) {
      stop--;
      continue;
    }
    const opener = closingDelimiters[last];
    if (opener !== undefined && !hasUnmatchedOpener(text, start, stop - 1, opener, last)) {
      stop--;
      continue;
    }
    return stop;
  }
}

/**
 * Reports whether the candidate up to `end` contains an opener still waiting to
 * be closed — which is what makes a trailing closer part of the URL.
 */
function hasUnmatchedOpener(
  text: string,
  start: number,
  end: number,
  opener: string,
  closer: string,
): boolean {
  let depth = 0;
  for (let i = start; i < end; i++) {
    if (text[i] === opener) depth++;
    else if (text[i] === closer && depth > 0) depth--;
  }
  return depth > 0;
}

/**
 * Returns the renderable http(s) URLs in `text`, in order, without overlaps.
 *
 * A candidate that is not renderable is skipped rather than reported, so the
 * caller renders it as ordinary text — the fail-closed direction, and the one
 * that keeps a malformed or hostile-looking address from becoming an anchor.
 */
export function findAutolinks(text: string): LinkSpan[] {
  const spans: LinkSpan[] = [];
  let index = 0;

  while (index < text.length) {
    const scheme = schemes.find((candidate) => hasSchemeAt(text, index, candidate));
    if (!scheme) {
      index++;
      continue;
    }

    let end = index;
    while (end < text.length && !isTerminator(text[end])) end++;

    // A scheme that starts in the middle of another token is not a URL the sender
    // wrote. The whole run is skipped rather than rescanned from its next
    // character, so a rejected candidate cannot have its tail matched instead.
    if (index > 0 && schemeBoundaryExcluded.test(text[index - 1])) {
      index = end > index ? end : index + 1;
      continue;
    }

    // Trailing punctuation is given back to the sentence, so "see
    // https://example.com." links to the site and not to a host with a dot on
    // the end — but a bracket the URL itself opened is kept.
    const stop = trimTrailingDelimiters(text, index, end);

    const candidate = text.slice(index, stop);
    if (stop > index && isRenderableURL(candidate)) {
      spans.push({ start: index, end: stop, href: candidate });
    }

    // Continue past the whole run either way. A candidate that was refused is not
    // re-scanned from its second character, which would otherwise let the tail of
    // a rejected address match a scheme embedded inside it.
    index = end > index ? end : index + 1;
  }

  return spans;
}
