/**
 * presenceSettings — the viewer's own manual presence (issue #798).
 *
 * The server owns it: GET/PUT/DELETE /api/chat/presence/me, identity from the
 * session, workspace resolved server-side, expiry decided on the server's clock.
 * This module only asks, and keeps what the server answered.
 *
 * Durations are turned into a concrete instant here, in the browser's own time
 * zone — "Hoje" is the end of the viewer's day, "Esta semana" the end of their
 * Sunday — and that instant is what is sent and stored. No phrase like "today"
 * ever reaches the server, so the stored state means the same thing whoever
 * reads it.
 */

import { useCallback, useEffect, useRef, useState } from "react";

import { authenticatedFetch } from "../lib/authClient";
import { acquireChatSocket } from "./chatSocket";
import type { PresenceDetail, PresenceState } from "./presence";
import { describePresence } from "./presenceDescription";

const CHAT_BASE = import.meta.env.VITE_CHAT_API_BASE_URL ?? "/api/chat";
const PRESENCE_ME = `${CHAT_BASE}/presence/me`;

export type ManualPresenceState = "available" | "busy" | "dnd" | "brb" | "away" | "appear_offline";

export const manualPresenceStates: readonly ManualPresenceState[] = [
  "available",
  "busy",
  "dnd",
  "brb",
  "away",
  "appear_offline",
];

export const manualPresenceLabels: Record<ManualPresenceState, string> = {
  available: "Disponível",
  busy: "Ocupado",
  dnd: "Não perturbe",
  brb: "Volto já",
  away: "Ausente",
  appear_offline: "Aparecer offline",
};

/** The dot each manual state is drawn with. Appearing offline looks offline. */
export const manualDotState: Record<ManualPresenceState, PresenceState> = {
  available: "online",
  busy: "busy",
  dnd: "dnd",
  brb: "brb",
  away: "away",
  appear_offline: "offline",
};

/** What the server holds: a manual state and when it ends, or automatic. */
export interface PresenceSettings {
  state: ManualPresenceState | null;
  /** Epoch ms; null exactly when state is. */
  expiresAt: number | null;
  /**
   * Whether this deployment accepts a change (the server's rollout gate). A
   * stored state is in force either way; only the choice is withheld.
   */
  writable: boolean;
}

export const automaticPresence: PresenceSettings = { state: null, expiresAt: null, writable: true };

interface PresenceSettingsEnvelope {
  data?: { state?: unknown; expires_at?: unknown; writable?: unknown };
}

function isManualState(value: unknown): value is ManualPresenceState {
  return typeof value === "string" && (manualPresenceStates as readonly string[]).includes(value);
}

/**
 * Reads the server's answer. A state this client does not know is read as
 * automatic: showing "Ocupado" for something the server did not say would be
 * a claim, and the next answer replaces it anyway.
 */
export function parsePresenceSettings(body: unknown): PresenceSettings {
  const data = (body as PresenceSettingsEnvelope | null)?.data;
  const writable = data?.writable === true;
  const expiresAt = typeof data?.expires_at === "string" ? Date.parse(data.expires_at) : Number.NaN;
  if (!isManualState(data?.state) || !Number.isFinite(expiresAt)) {
    return { ...automaticPresence, writable };
  }
  return { state: data.state, expiresAt, writable };
}

export async function fetchPresenceSettings(signal?: AbortSignal): Promise<PresenceSettings> {
  return parsePresenceSettings(
    await authenticatedFetch<unknown>(PRESENCE_ME, { method: "GET", signal }),
  );
}

export async function putPresenceSettings(
  state: ManualPresenceState,
  expiresAt: number,
): Promise<PresenceSettings> {
  const body = await authenticatedFetch<unknown>(PRESENCE_ME, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ state, expires_at: new Date(expiresAt).toISOString() }),
  });
  return parsePresenceSettings(body);
}

export async function deletePresenceSettings(): Promise<PresenceSettings> {
  return parsePresenceSettings(
    await authenticatedFetch<unknown>(PRESENCE_ME, { method: "DELETE" }),
  );
}

// ── durations ────────────────────────────────────────────────────────────────

export type PresenceDuration = "1h" | "4h" | "today" | "week";

export const presenceDurationLabels: Record<PresenceDuration, string> = {
  "1h": "1 hora",
  "4h": "4 horas",
  today: "Hoje",
  week: "Esta semana",
};

const MINUTE_MS = 60_000;
const HOUR_MS = 60 * MINUTE_MS;
/**
 * The shortest expiry this client asks for. The server refuses anything under a
 * minute; a choice made at 23:59 for "Hoje" would otherwise be refused rather
 * than honoured for a few minutes.
 */
export const MIN_PRESENCE_DURATION_MS = 5 * MINUTE_MS;

function endOfDay(at: Date): number {
  const end = new Date(at);
  end.setHours(23, 59, 59, 999);
  return end.getTime();
}

/** The end of Sunday of the viewer's current week (weeks start on Monday). */
function endOfWeek(at: Date): number {
  const end = new Date(at);
  const daysToSunday = (7 - end.getDay()) % 7;
  end.setDate(end.getDate() + daysToSunday);
  return endOfDay(end);
}

/** The concrete instant a duration ends, in the viewer's time zone. */
export function presenceExpiry(duration: PresenceDuration, now: number): number {
  const at = new Date(now);
  const ends: Record<PresenceDuration, () => number> = {
    "1h": () => now + HOUR_MS,
    "4h": () => now + 4 * HOUR_MS,
    today: () => endOfDay(at),
    week: () => endOfWeek(at),
  };
  return Math.max(ends[duration](), now + MIN_PRESENCE_DURATION_MS);
}

/** "até 18:00" today, "até 03/10 18:00" later. */
export function formatPresenceUntil(expiresAt: number, now: number): string {
  const time = new Intl.DateTimeFormat("pt-BR", { hour: "2-digit", minute: "2-digit" }).format(
    expiresAt,
  );
  if (endOfDay(new Date(now)) >= expiresAt) return `até ${time}`;
  const date = new Intl.DateTimeFormat("pt-BR", { day: "2-digit", month: "2-digit" }).format(
    expiresAt,
  );
  return `até ${date} ${time}`;
}

// ── hook ─────────────────────────────────────────────────────────────────────

export interface PresenceSettingsController {
  /**
   * null until the server has answered once. A manual state whose end has
   * passed is already automatic here, before the server confirms it.
   */
  settings: PresenceSettings | null;
  pending: boolean;
  /** The last write failed; the settings shown are the server's again. */
  failed: boolean;
  set: (state: ManualPresenceState, expiresAt: number) => void;
  clear: () => void;
  retry: () => void;
}

type PresenceWrite = () => Promise<PresenceSettings>;

/** setTimeout's ceiling; a longer wait fires at once instead of late. */
const MAX_TIMER_MS = 2 ** 31 - 1;

/**
 * The viewer's settings, kept in step with the server.
 *
 * A write is shown at once and confirmed by the server's answer, which replaces
 * it; a failed write puts back what the server last said, keeps the request for
 * a retry and reads the settings again, so nothing optimistic outlives a
 * refusal.
 *
 * Reads and writes have separate lifecycles. A read is "what is stored now":
 * `presence.settings_changed` (from another tab, device, or this tab's own
 * write — the server sends it before the write answers), a reconnected socket,
 * or the end of a manual state each ask for one. While a write is in flight a
 * read could only race it, so it is deferred to when the writes settle, and a
 * read already in flight when a write starts is discarded. Among writes, only
 * the newest decides what is shown; an older one answering late only means the
 * stored state must be read again.
 */
export function usePresenceSettings(): PresenceSettingsController {
  const [settings, setSettings] = useState<PresenceSettings | null>(null);
  const [expiredAt, setExpiredAt] = useState<number | null>(null);
  const [pending, setPending] = useState(false);
  const [failed, setFailed] = useState(false);
  const confirmed = useRef<PresenceSettings | null>(null);
  const lastWrite = useRef<{ write: PresenceWrite; optimistic: PresenceSettings } | null>(null);
  const readGeneration = useRef(0);
  const latestWrite = useRef(0);
  const writesInFlight = useRef(0);
  const readOwed = useRef(false);
  const expiredFor = useRef<number | null>(null);
  const disposed = useRef(false);

  const adopt = useCallback((next: PresenceSettings) => {
    confirmed.current = next;
    setSettings(next);
  }, []);

  const reload = useCallback(() => {
    if (writesInFlight.current > 0) {
      readOwed.current = true;
      return;
    }
    const issued = ++readGeneration.current;
    fetchPresenceSettings()
      .then((next) => {
        if (!disposed.current && issued === readGeneration.current) adopt(next);
      })
      .catch(() => {
        // A failed read leaves what is known; the next hint or reconnect retries.
      });
  }, [adopt]);

  useEffect(() => {
    disposed.current = false;
    reload();
    const handle = acquireChatSocket({
      onOpen: reload,
      onMessage: (frame) => {
        if (frame["type"] === "presence.settings_changed") reload();
      },
    });
    return () => {
      disposed.current = true;
      handle.release();
    };
  }, [reload]);

  // The end of a manual state, on this controller's own clock: shown as
  // automatic when it comes, and read again from the server, which also tells
  // every session when its sweep notices. A state longer than one timeout can
  // wait is counted down in chained one-shot timeouts that depend on nothing
  // but the clock — a read failing in between cannot stop the count. One
  // timeout is armed at a time, replaced whenever the settings are, and
  // cleared on unmount. Once per end: a server whose clock is behind this one
  // answers the same end again and is not asked twice.
  useEffect(() => {
    const ends = settings?.expiresAt ?? null;
    if (ends === null || expiredFor.current === ends) return;
    const delayUntilEnd = () => Math.min(Math.max(ends - Date.now(), 0), MAX_TIMER_MS);
    const tick = () => {
      if (Date.now() < ends) {
        timer = setTimeout(tick, delayUntilEnd());
        return;
      }
      expiredFor.current = ends;
      setExpiredAt(ends);
      reload();
    };
    let timer = setTimeout(tick, delayUntilEnd());
    return () => clearTimeout(timer);
  }, [settings, reload]);

  const settle = useCallback(
    (issued: number, outcome: { next: PresenceSettings } | { error: true }) => {
      writesInFlight.current -= 1;
      if (disposed.current) return;
      if (issued !== latestWrite.current) {
        readOwed.current = true;
      } else if ("next" in outcome) {
        adopt(outcome.next);
        lastWrite.current = null;
        setPending(false);
      } else {
        setSettings(confirmed.current);
        setFailed(true);
        setPending(false);
        readOwed.current = true;
      }
      if (writesInFlight.current === 0 && readOwed.current) {
        readOwed.current = false;
        reload();
      }
    },
    [adopt, reload],
  );

  const run = useCallback(
    (write: PresenceWrite, optimistic: PresenceSettings) => {
      const issued = ++latestWrite.current;
      writesInFlight.current += 1;
      readGeneration.current += 1;
      lastWrite.current = { write, optimistic };
      setSettings(optimistic);
      setPending(true);
      setFailed(false);
      write().then(
        (next) => settle(issued, { next }),
        () => settle(issued, { error: true }),
      );
    },
    [settle],
  );

  const set = useCallback(
    (state: ManualPresenceState, expiresAt: number) =>
      run(() => putPresenceSettings(state, expiresAt), { state, expiresAt, writable: true }),
    [run],
  );
  const clear = useCallback(() => run(deletePresenceSettings, automaticPresence), [run]);
  const retry = useCallback(() => {
    const last = lastWrite.current;
    if (last) run(last.write, last.optimistic);
  }, [run]);

  const shown =
    settings !== null && settings.expiresAt !== null && settings.expiresAt === expiredAt
      ? { ...automaticPresence, writable: settings.writable }
      : settings;
  return { settings: shown, pending, failed, set, clear, retry };
}

// ── what the viewer is shown as ──────────────────────────────────────────────

export interface SelfPresenceSummary {
  dot: PresenceState;
  label: string;
}

/**
 * What the viewer is shown as. A hidden viewer is told so in words — the
 * realtime store can only say "offline", which is what everyone else sees.
 * Otherwise the effective state other people see, with its context, and the
 * manual choice only while the server has not answered yet.
 */
export function summarizeSelfPresence(
  settings: PresenceSettings | null,
  detail: PresenceDetail,
  pending: boolean,
): SelfPresenceSummary {
  const manual = settings?.state ?? null;
  if (manual === "appear_offline")
    return { dot: "offline", label: manualPresenceLabels.appear_offline };
  if (manual && (pending || detail.state === "unknown")) {
    return { dot: manualDotState[manual], label: manualPresenceLabels[manual] };
  }
  if (detail.state === "unknown") return { dot: "unknown", label: "Status" };
  return { dot: detail.state, label: describePresence(detail) };
}

/** A local date-time input value ("2026-10-01T18:00") as an instant, or NaN. */
export function parseLocalDateTime(value: string): number {
  return value ? new Date(value).getTime() : Number.NaN;
}
