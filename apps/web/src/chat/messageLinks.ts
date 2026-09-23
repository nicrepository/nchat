/**
 * Per-link entities (issue #807).
 *
 * The backend is the authority for what in a message is a link, where it
 * points, what is known about it and what the reader may do with it. This
 * module decodes that contract and nothing else: there is no URL parsing here
 * that decides anything, no fetch, no navigation. A link is drawn the way the
 * server described it, and an anchor is drawn only when the server sent an
 * `href` — its presence *is* the authorisation.
 *
 * Every value is validated against a closed set on the way in. A server this
 * build does not understand — a new safety state, a new click policy — folds
 * to the value that authorises nothing, which is the only safe direction.
 */

export type LinkSafety = "pending" | "safe" | "malicious" | "unknown";
export type LinkClick = "none" | "direct" | "interstitial";
export type LinkPreviewState = "none" | "queued" | "fetching" | "ready" | "unsupported" | "failed";

export interface LinkPreview {
  state: LinkPreviewState;
  /** The real destination host, punycoded. Never the page's own claim. */
  hostname: string;
  siteName: string;
  title: string;
  description: string;
  /** Names a derived thumbnail on chat-service. Never a remote URL. */
  imageId: string;
  imageWidth: number;
  imageHeight: number;
}

export interface MessageLink {
  ordinal: number;
  /**
   * The target's stable identity, carried on every occurrence — including a
   * blocked one, whose URL and text are withheld — and on every realtime
   * update about the target. An occurrence is identified by this and its
   * ordinal, never by the visible URL, which can disappear and come back.
   */
  targetKey: string;
  /** The URL as written in the body; what a rendered span is matched against. Empty for a blocked link. */
  text: string;
  /** The canonical destination. Empty for a blocked link. */
  url: string;
  hostname: string;
  safety: LinkSafety;
  click: LinkClick;
  /** Present only when click is "direct". The server's authorisation to draw an anchor. */
  href: string;
  updatedAt: string;
  preview?: LinkPreview;
}

/**
 * The rune the server substitutes for a blocked URL in body_text (U+FFFC).
 * The renderer draws the "link blocked" chip in its place.
 */
export const LINK_BLOCKED_MARKER = "￼";

/** How many rich cards a message draws at most. Further safe links stay anchors. */
export const MAX_PREVIEW_CARDS = 2;

const safetyValues: readonly LinkSafety[] = ["pending", "safe", "malicious", "unknown"];
const clickValues: readonly LinkClick[] = ["none", "direct", "interstitial"];
const previewStates: readonly LinkPreviewState[] = [
  "none",
  "queued",
  "fetching",
  "ready",
  "unsupported",
  "failed",
];

function text(value: unknown): string {
  return typeof value === "string" ? value : "";
}

function count(value: unknown): number {
  return typeof value === "number" && Number.isFinite(value) && value > 0 ? value : 0;
}

function parsePreview(raw: unknown): LinkPreview | undefined {
  if (typeof raw !== "object" || raw === null) return undefined;
  const item = raw as Record<string, unknown>;
  const state = item.state;
  if (!(previewStates as readonly unknown[]).includes(state)) return undefined;
  return {
    state: state as LinkPreviewState,
    hostname: text(item.hostname),
    siteName: text(item.site_name),
    title: text(item.title),
    description: text(item.description),
    imageId: text(item.image_id),
    imageWidth: count(item.image_width),
    imageHeight: count(item.image_height),
  };
}

/**
 * Decodes one link entity. Returns undefined for anything outside the contract,
 * so a caller drops it and the span renders as literal text — never as an
 * anchor the server did not describe.
 */
export function parseMessageLink(raw: unknown): MessageLink | undefined {
  if (typeof raw !== "object" || raw === null) return undefined;
  const item = raw as Record<string, unknown>;
  const policy = parseLinkPolicy(item);
  // Without an identity the occurrence could never be matched to an update, and
  // an update without one could never be applied: neither is accepted.
  const targetKey = text(item.target_key);
  if (!policy || targetKey === "") return undefined;
  const preview = parsePreview(item.preview);
  return {
    ordinal: typeof item.ordinal === "number" ? item.ordinal : 0,
    targetKey,
    text: text(item.text),
    url: text(item.url),
    hostname: text(item.hostname),
    ...policy,
    updatedAt: text(item.updated_at),
    ...(preview ? { preview } : {}),
  };
}

/**
 * The closed sets that decide what a link authorises. An href on anything but
 * a direct link, or one that is not http(s), is a contract violation, and a
 * violation is refused rather than repaired: no anchor.
 */
function parseLinkPolicy(
  item: Record<string, unknown>,
): Pick<MessageLink, "safety" | "click" | "href"> | undefined {
  const safety = item.safety;
  const click = item.click;
  if (!(safetyValues as readonly unknown[]).includes(safety)) return undefined;
  if (!(clickValues as readonly unknown[]).includes(click)) return undefined;
  const href = text(item.href);
  if (href !== "" && (click !== "direct" || !/^https?:\/\//i.test(href))) return undefined;
  return { safety: safety as LinkSafety, click: click as LinkClick, href };
}

/** Decodes a `links` array; absent or malformed input yields undefined. */
export function parseMessageLinks(raw: unknown): MessageLink[] | undefined {
  if (!Array.isArray(raw)) return undefined;
  const links = raw.flatMap((item) => {
    const link = parseMessageLink(item);
    return link ? [link] : [];
  });
  return links.length > 0 ? links : undefined;
}

/**
 * Finds the link entity for a rendered span, by exact text. A span the server
 * did not describe has no entity and stays literal text.
 */
export function linkForText(links: readonly MessageLink[] | undefined, spanText: string) {
  return links?.find((link) => link.text === spanText && link.safety !== "malicious");
}

/**
 * A link version as an orderable value: milliseconds since the epoch plus the
 * sub-millisecond digits an RFC 3339 timestamp may carry. Two versions compare
 * chronologically whatever precision each was written with — `…00Z`,
 * `…00.9Z` and `…00.900000001Z` order as instants, not as strings. Undefined
 * for anything Date.parse does not accept.
 */
export function linkVersion(value: string): [ms: number, subMillis: number] | undefined {
  const match = /^(.*T\d{2}:\d{2}:\d{2})(?:\.(\d+))?(Z|[+-]\d{2}:\d{2})$/.exec(value);
  if (!match) return undefined;
  const fraction = (match[2] ?? "").padEnd(9, "0");
  const ms = Date.parse(`${match[1]}.${fraction.slice(0, 3)}${match[3]}`);
  if (!Number.isFinite(ms)) return undefined;
  return [ms, Number(fraction.slice(3, 9))];
}

/**
 * Whether an update is older than the occurrence it would replace, compared as
 * instants. Explicit and conservative about what cannot be ordered: an update
 * whose version is unreadable is treated as stale and not applied — the next
 * read converges the link — while an occurrence whose own version is
 * unreadable has nothing to defend and accepts the update.
 */
export function isStaleLinkUpdate(update: MessageLink, current: MessageLink): boolean {
  const incoming = linkVersion(update.updatedAt);
  if (!incoming) return true;
  const drawn = linkVersion(current.updatedAt);
  if (!drawn) return false;
  return incoming[0] < drawn[0] || (incoming[0] === drawn[0] && incoming[1] < drawn[1]);
}

/**
 * Applies a target-level update from the server to every occurrence of that
 * target in the message, matched by target key — never by URL, which a blocked
 * occurrence does not carry. Ordinal and text are per-occurrence and kept;
 * everything else comes from the update. A stale update — older than what is
 * drawn — is ignored, and an update for a target the message no longer names
 * changes nothing.
 */
export function applyLinkUpdate(
  links: readonly MessageLink[] | undefined,
  update: MessageLink,
): MessageLink[] | undefined {
  if (!links || update.targetKey === "") return links ? [...links] : links;
  let changed = false;
  const next = links.map((link) => {
    if (link.targetKey !== update.targetKey || isStaleLinkUpdate(update, link)) return link;
    changed = true;
    return { ...update, ordinal: link.ordinal, text: link.text };
  });
  return changed ? next : [...links];
}

/**
 * Whether applying the update would need the body the client cannot rebuild: a
 * blocked occurrence's text was withheld by the server, so its release — or a
 * fresh condemnation — can only arrive as a re-read of the message.
 */
export function linkUpdateNeedsSnapshot(
  links: readonly MessageLink[] | undefined,
  update: MessageLink,
): boolean {
  if (update.safety === "malicious") return true;
  return (
    links?.some(
      (link) =>
        link.targetKey === update.targetKey &&
        link.safety === "malicious" &&
        !isStaleLinkUpdate(update, link),
    ) ?? false
  );
}

/**
 * Merges the links of an authoritative snapshot into what is drawn, one
 * occurrence at a time, under the same version order realtime updates use.
 *
 * Two questions are answered separately. Which occurrences exist is the
 * message's own version: when the snapshot is at least as new as the drawn
 * message (`snapshotIsNewer`), its occurrence set is the truth — an occurrence
 * it no longer lists was edited away and is dropped; when the snapshot is
 * older, the drawn set stands and nothing is added or removed. Which state each
 * occurrence carries is the target's version: whichever side is newer wins, so
 * a snapshot that was read before a realtime update landed cannot regress it,
 * and a snapshot that is genuinely newer overrides an older update.
 */
export function mergeSnapshotLinks(
  current: readonly MessageLink[] | undefined,
  snapshot: readonly MessageLink[] | undefined,
  snapshotIsNewer: boolean,
): MessageLink[] | undefined {
  const base = snapshotIsNewer ? snapshot : current;
  const other = snapshotIsNewer ? current : snapshot;
  if (!base) return undefined;
  return base.map((link) => {
    const candidate = findLinkOccurrence(other, link);
    if (!candidate) return link;
    // When the snapshot is the base, `candidate` is what is drawn and `link` the
    // snapshot's state; otherwise the roles swap. Either way the newer wins.
    const [drawn, incoming] = snapshotIsNewer ? [candidate, link] : [link, candidate];
    return isStaleLinkUpdate(incoming, drawn) ? drawn : incoming;
  });
}

/** The identity of one rendered occurrence: the target it points at and its position. */
export interface LinkOccurrenceRef {
  targetKey: string;
  ordinal: number;
}

/** Resolves an occurrence in the message's current links; undefined once it is gone. */
export function findLinkOccurrence(
  links: readonly MessageLink[] | undefined,
  ref: LinkOccurrenceRef,
): MessageLink | undefined {
  return links?.find((link) => link.targetKey === ref.targetKey && link.ordinal === ref.ordinal);
}

/**
 * The cards a message draws: at most MAX_PREVIEW_CARDS, one per distinct
 * target, in occurrence order, only for links whose preview is on its way or
 * ready. A failed or unsupported preview is the link staying an ordinary
 * anchor, which is not something to draw.
 */
export function previewCards(links: readonly MessageLink[] | undefined): MessageLink[] {
  if (!links) return [];
  const seen = new Set<string>();
  const cards: MessageLink[] = [];
  for (const link of links) {
    const preview = link.preview;
    if (link.safety !== "safe" || !preview || seen.has(link.targetKey)) continue;
    if (preview.state !== "ready" && preview.state !== "queued" && preview.state !== "fetching")
      continue;
    seen.add(link.targetKey);
    cards.push(link);
    if (cards.length === MAX_PREVIEW_CARDS) break;
  }
  return cards;
}

/** Whether the message carries a link the server condemned. */
export function hasBlockedLink(links: readonly MessageLink[] | undefined): boolean {
  return links?.some((link) => link.safety === "malicious") ?? false;
}
