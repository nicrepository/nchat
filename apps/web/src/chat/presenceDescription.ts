/**
 * presenceDescription — a presence as one line of context (issue #798).
 *
 * "Ocupado · Em chamada", "Ausente · há 12 min", "Offline · visto hoje às 15:42".
 * Pure: the clock is an argument, so nothing here schedules a timer and no
 * avatar re-renders on its own. A surface computes the line when it renders.
 *
 * Every instant comes from the server. For an offline person it is when the
 * server published them offline — which for somebody who chose to appear
 * offline is the moment they chose it, and nothing later.
 */

import {
  presenceActivityLabels,
  presenceLabel,
  type PresenceDetail,
  type PresenceInstant,
} from "./presence";

const MINUTE_MS = 60_000;
const HOUR_MS = 60 * MINUTE_MS;

const timeFormat = new Intl.DateTimeFormat("pt-BR", { hour: "2-digit", minute: "2-digit" });
const dateFormat = new Intl.DateTimeFormat("pt-BR", { day: "2-digit", month: "2-digit" });

function instantMs(instant: PresenceInstant | undefined): number | undefined {
  if (!instant || (instant.secondMs === 0 && instant.nanosecond === 0)) return undefined;
  return instant.secondMs + Math.floor(instant.nanosecond / 1_000_000);
}

function startOfDay(ms: number): number {
  const day = new Date(ms);
  day.setHours(0, 0, 0, 0);
  return day.getTime();
}

/** "visto há 18 min", "visto hoje às 15:42", "visto ontem às 09:10", "visto em 28/09 às 17:00". */
export function formatLastSeen(at: number, now: number): string {
  const elapsed = Math.max(0, now - at);
  if (elapsed < MINUTE_MS) return "visto agora há pouco";
  if (elapsed < HOUR_MS) return `visto há ${Math.floor(elapsed / MINUTE_MS)} min`;
  const time = timeFormat.format(at);
  const today = startOfDay(now);
  if (at >= today) return `visto hoje às ${time}`;
  if (at >= startOfDay(today - 1)) return `visto ontem às ${time}`;
  return `visto em ${dateFormat.format(at)} às ${time}`;
}

/** "há 12 min" within the hour, "desde 15:42" earlier today, nothing older. */
function formatSince(at: number, now: number): string | undefined {
  const elapsed = Math.max(0, now - at);
  if (elapsed < HOUR_MS) return `há ${Math.max(1, Math.floor(elapsed / MINUTE_MS))} min`;
  if (at >= startOfDay(now)) return `desde ${timeFormat.format(at)}`;
  return undefined;
}

function contextOf(
  detail: PresenceDetail,
  now: number | undefined,
  lastSeen?: number,
): string | undefined {
  if (detail.state === "busy" || detail.state === "dnd") {
    return detail.activity ? presenceActivityLabels[detail.activity] : undefined;
  }
  // Without a clock there is no "how long ago": a list row says what the state
  // is and why, never when.
  return now === undefined ? undefined : elapsedContextOf(detail, now, lastSeen);
}

function elapsedContextOf(
  detail: PresenceDetail,
  now: number,
  lastSeen?: number,
): string | undefined {
  switch (detail.state) {
    case "away":
    case "brb": {
      const since = instantMs(detail.updatedAt);
      return since === undefined ? undefined : formatSince(since, now);
    }
    case "offline": {
      const seen = instantMs(detail.updatedAt) ?? lastSeen;
      return seen === undefined ? undefined : formatLastSeen(seen, now);
    }
    default:
      return undefined;
  }
}

/**
 * The full line for a person: the state's word, then whatever context the
 * server gave for it. `lastSeen` is a server instant learned elsewhere (the
 * profile endpoint) for an offline person the realtime store has no entry for.
 *
 * Without `now` the line carries no elapsed time — what a list row shows, so a
 * sidebar never needs a clock and never shows last seen permanently.
 */
export function describePresence(detail: PresenceDetail, now?: number, lastSeen?: number): string {
  const label = presenceLabel(detail.state);
  const context = contextOf(detail, now, lastSeen);
  return context ? `${label} · ${context}` : label;
}
