/**
 * Presentation values shared by the details panel and the header control that
 * opens it (issues #435, #441 and #443).
 *
 * They live outside ConversationDetailsPanel.tsx so that file exports components and
 * nothing else — the toggle needs the panel's id for aria-controls, and
 * importing a component module for a string would be the wrong dependency.
 */

/** aria-labelledby target: the panel's own heading. */
export const conversationDetailsTitleId = "chat-channel-details-title";

/** aria-controls target: stable, so the toggle can name the panel it opens. */
export const conversationDetailsPanelId = "chat-channel-details-panel";

/**
 * Human-readable size, binary units, pt-BR decimal separator.
 *
 * The boundaries are where a naive implementation goes wrong: 0 must not read
 * "0,0 B", exactly 1024 must roll over to "1,0 KB" rather than "1024 B", and the
 * loop must stop at the largest unit instead of dividing past it. A negative or
 * non-finite value is not a size and yields "" rather than a nonsense figure.
 */
export function formatFileSize(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return "";
  if (bytes < 1024) return `${Math.round(bytes)} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let value = bytes / 1024;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  // One decimal below 10 (2,4 MB), none above it (128 MB) — the prototype's
  // density, and it keeps the row from wrapping.
  const rounded = value < 10 ? value.toFixed(1) : Math.round(value).toString();
  return `${rounded.replace(".", ",")} ${units[unit]}`;
}

/**
 * Copy for a field the domain does not record, or that this person has not
 * filled in. One constant, so every profile row that has nothing to show says
 * the same thing and no row silently disappears instead.
 */
export const notInformedLabel = "Não informado";

/**
 * True when `timezone` is an IANA zone this runtime can actually resolve.
 *
 * Intl is the authority, not a regular expression: the set of valid zone names
 * is data, it changes with the tz database, and a pattern would both accept
 * names that do not exist ("Foo/Bar") and reject ones that do. Anything the
 * runtime refuses — a malformed string, an unknown zone, or a hostile value —
 * is not a time zone here.
 *
 * A fixed offset is refused even though Intl accepts one. ECMAScript allows
 * "-03:00" as a time-zone identifier, but an offset is not a zone: it cannot
 * say when daylight saving starts, so a profile carrying one would freeze that
 * person's clock to whichever half of the year the value was written in. Only a
 * named zone, which the tz database can resolve per instant, is usable — and a
 * resolved IANA name always contains a letter and never opens with a sign.
 *
 * This is also the injection boundary for the field: nothing downstream builds
 * markup or a URL from a time zone, and the raw string only ever reaches the
 * DOM as a text node, so a rejected value simply reads as absent.
 */
export function isValidTimeZone(timezone: string | undefined): timezone is string {
  if (typeof timezone !== "string" || timezone.trim() === "") return false;
  try {
    const resolved = new Intl.DateTimeFormat("pt-BR", { timeZone: timezone }).resolvedOptions()
      .timeZone;
    return /[A-Za-z]/.test(resolved) && !/^[+-]/.test(resolved);
  } catch {
    return false;
  }
}

/**
 * The wall-clock time in `timezone` at instant `date`, as "HH:MM".
 *
 * Pure and total: same inputs, same output, and an unusable zone yields "" so
 * the caller renders the absent state rather than a time.
 *
 * The zone does the whole job. There is deliberately no offset arithmetic
 * anywhere: Intl resolves the zone against the tz database *for that instant*,
 * so daylight saving is handled by construction and a zone whose offset changes
 * during the year cannot be frozen to whichever offset happened to apply when
 * the panel opened. It is equally deliberate that the reader's own zone is
 * never consulted — this row states what time it is where the *other* person
 * is, and falling back to the viewer's clock would state that as a fact about
 * someone else.
 */
export function formatLocalTime(date: Date, timezone: string | undefined): string {
  if (!isValidTimeZone(timezone)) return "";
  if (!(date instanceof Date) || Number.isNaN(date.getTime())) return "";
  try {
    return new Intl.DateTimeFormat("pt-BR", {
      timeZone: timezone,
      hour: "2-digit",
      minute: "2-digit",
      hour12: false,
    }).format(date);
  } catch {
    return "";
  }
}

/**
 * How often the local-time row re-reads the clock.
 *
 * The row shows hours and minutes, so a one-second tick would re-render sixty
 * times for every visible change. A minute is the display's own resolution;
 * being at most that late is invisible and costs one timer.
 */
export const localTimeRefreshMs = 60_000;
