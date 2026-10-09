/**
 * Instants as the chat API writes them (moved out of sidebarOrder, issue
 * #1082), so every ordering in the client — the sidebar's, the timeline's and
 * the read cursor's — reads a timestamp the same way, and none of them depends
 * on another to do it.
 */

/**
 * One instant, split so that nothing below the millisecond is lost.
 *
 * `Date` cannot hold more than milliseconds, and the activity timestamps do:
 * chat.messages.created_at is a TIMESTAMPTZ with microsecond resolution, and
 * both the sidebar payload and the WebSocket event publish it in full. Two
 * messages written 78µs apart are two different instants that the database
 * orders, so the remainder is kept alongside the epoch rather than rounded into
 * it.
 *
 * `subMillisecondNanoseconds` is what is left after the millisecond, in
 * nanoseconds — always 0…999999, so both fields stay ordinary numbers and no
 * BigInt is involved.
 */
export interface Instant {
  epochMilliseconds: number;
  subMillisecondNanoseconds: number;
}

/**
 * RFC 3339 as this API emits it: a date-time, an optional 1-9 digit fraction,
 * and either `Z` or a numeric offset. Anything else is left to `Date.parse`.
 */
const rfc3339 = /^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(?:\.(\d{1,9}))?(Z|[+-]\d{2}:\d{2})$/;

/**
 * A timestamp as a comparable instant, or `undefined` when there is none.
 *
 * The string is not compared directly. Two values written with different
 * offsets ("…T12:00:00Z" and "…T09:00:00-03:00") are the same moment and would
 * not sort as equal as text, and a fraction may be written ".9", ".900" or
 * ".900000000" for the same reason — RFC3339Nano drops trailing zeros. So the
 * value is decomposed: the millisecond part goes through `Date.parse`, which
 * settles the offset, and the digits past the millisecond are carried
 * separately. The fraction is right-padded to nine digits first, which is what
 * makes ".1" and ".100000000" land on the same pair of numbers.
 *
 * A value that is not in the shape above falls back to a whole-string
 * `Date.parse`, preserving exactly what this function accepted before the
 * fraction was tracked: anything `Date.parse` understands still yields an
 * instant, at millisecond resolution, and nothing else does.
 *
 * An unparseable string collapses to the same `undefined` a missing value
 * yields. That is the predictable reading: a row whose timestamp cannot be
 * understood is a row with no usable instant, and it is then ordered by the
 * remaining, always-available keys rather than by a number invented for it.
 */
export function parseInstant(value: string | null | undefined): Instant | undefined {
  if (typeof value !== "string") return undefined;

  const match = rfc3339.exec(value);
  if (!match) {
    const parsed = Date.parse(value);
    return Number.isFinite(parsed)
      ? { epochMilliseconds: parsed, subMillisecondNanoseconds: 0 }
      : undefined;
  }

  const [, dateTime, fraction = "", zone] = match;
  const nanoseconds = `${fraction}000000000`.slice(0, 9);
  const parsed = Date.parse(`${dateTime}.${nanoseconds.slice(0, 3)}${zone}`);
  if (!Number.isFinite(parsed)) return undefined;
  return {
    epochMilliseconds: parsed,
    subMillisecondNanoseconds: Number(nanoseconds.slice(3)),
  };
}

/** Ascending: negative when `a` is the earlier instant. */
export function compareInstants(a: Instant, b: Instant): number {
  if (a.epochMilliseconds !== b.epochMilliseconds) {
    return a.epochMilliseconds - b.epochMilliseconds;
  }
  return a.subMillisecondNanoseconds - b.subMillisecondNanoseconds;
}
