/**
 * The observability surface of the Admin API (issue #581).
 *
 * Two things this module never does, and both are the point:
 *
 *  - it never names a destination. The only value it ever sends is a service
 *    identifier that came out of a previous response, and the server resolves
 *    it against a registry before reading it. There is no field here for a
 *    URL, a host or a port, because no endpoint accepts one;
 *  - it never invents a state. The five health states are parsed against a
 *    closed set and anything else becomes `unknown`, so a build that meets a
 *    newer server reports "we do not know" rather than guessing — which is the
 *    same rule the server applies to itself.
 */

import { adminFetch } from "./client";
import { bool, contractError, num, requireArray, requireRecord, str } from "./parse";

/** The five states, exactly as the platform models them. */
export type HealthState = "healthy" | "degraded" | "unavailable" | "disabled" | "unknown";

const HEALTH_STATES: readonly HealthState[] = [
  "healthy",
  "degraded",
  "unavailable",
  "disabled",
  "unknown",
] as const;

/**
 * Reads a state from the payload.
 *
 * An unrecognised value becomes `unknown` rather than throwing: a server that
 * gained a sixth state should degrade this console into honesty, not into a
 * blank screen. It must never become `healthy`.
 */
export function parseHealthState(raw: string): HealthState {
  return HEALTH_STATES.includes(raw as HealthState) ? (raw as HealthState) : "unknown";
}

export interface ServiceHealth {
  id: string;
  displayName: string;
  category: string;
  /** What breaks for users while this dependency is not healthy. */
  impact: string;
  state: HealthState;
  /** The deployment's switch, reported next to the state and never instead of it. */
  enabled: boolean;
  /** Whether the server can see this dependency's endpoint at all. */
  observable: boolean;
  critical: boolean;
  /**
   * Absent when no round trip was measured. Deliberately `null` and not `0`:
   * a check that did not run has no latency, and rendering "0 ms" would claim
   * one.
   */
  latencyMS: number | null;
  checkedAt: string;
  /** A sanitized category from the server's closed set, or "" when healthy. */
  errorCategory: string;
  /** A short, server-written sentence. Never a library's error text. */
  detail: string;
  version: string;
  configKey: string;
  runbookPath: string;
}

export interface HealthSnapshot {
  collectedAt: string;
  overall: HealthState;
  services: ServiceHealth[];
}

export type AlertSeverity = "critical" | "warning";

export interface HealthAlert {
  serviceId: string;
  severity: AlertSeverity;
  title: string;
  impact: string;
  action: string;
  since: string;
  runbookPath: string;
  configKey: string;
}

export type MetricWindow = "instant" | "last_24h" | "cumulative";
export type MetricUnit = "count" | "bytes";

export interface PlatformMetric {
  key: string;
  label: string;
  /** What exactly is counted, in the terms an operator can verify. */
  definition: string;
  window: MetricWindow;
  unit: MetricUnit;
  /** Null when the aggregate did not run. Never conflated with zero. */
  value: number | null;
  available: boolean;
}

export interface DashboardSummary {
  collectedAt: string;
  overall: HealthState;
  stateCounts: Record<HealthState, number>;
  metrics: PlatformMetric[];
  metricsAvailable: boolean;
  alerts: HealthAlert[];
}

/** The dashboard, in one request rather than one per card. */
export async function getOverview(signal?: AbortSignal): Promise<DashboardSummary> {
  const body = await adminFetch<unknown>("/overview", { signal });
  return parseSummary(requireRecord(body, "data").summary);
}

/**
 * The Health Center's table.
 *
 * There is no filter parameter here on purpose. Filtering by state happens in
 * the browser because the payload is a dozen rows: a round trip to hide three
 * of them would be slower and would add a parameter for no gain.
 */
export async function listHealthChecks(signal?: AbortSignal): Promise<HealthSnapshot> {
  return parseSnapshot(await adminFetch<unknown>("/health/services", { signal }));
}

/**
 * Forces a fresh collection.
 *
 * A POST with no body: there is nothing about a refresh for a client to
 * parameterise, and being a POST is what puts it behind the CSRF and origin
 * guards. The server bounds how often it really recollects, so this being
 * clickable is not the same as it being expensive.
 */
export async function refreshHealthChecks(signal?: AbortSignal): Promise<HealthSnapshot> {
  return parseSnapshot(await adminFetch<unknown>("/health/refresh", { method: "POST", signal }));
}

function parseSnapshot(body: unknown): HealthSnapshot {
  const record = requireRecord(body, "data");
  return {
    collectedAt: str(record, "collected_at", "data"),
    overall: parseHealthState(str(record, "overall", "data")),
    services: requireArray(record.services, "services").map((entry, index) =>
      parseService(requireRecord(entry, `services[${index}]`), index),
    ),
  };
}

function parseService(raw: Record<string, unknown>, index: number): ServiceHealth {
  const field = `services[${index}]`;
  return {
    id: str(raw, "id", field),
    displayName: str(raw, "display_name", field),
    category: str(raw, "category", field),
    impact: str(raw, "impact", field),
    state: parseHealthState(str(raw, "state", field)),
    enabled: bool(raw, "enabled", field),
    observable: bool(raw, "observable", field),
    critical: bool(raw, "critical", field),
    latencyMS: optionalNumber(raw, "latency_ms", field),
    checkedAt: str(raw, "checked_at", field),
    errorCategory: optionalString(raw, "error_category", field),
    detail: optionalString(raw, "detail", field),
    version: optionalString(raw, "version", field),
    configKey: optionalString(raw, "config_key", field),
    runbookPath: optionalString(raw, "runbook_path", field),
  };
}

function parseSummary(body: unknown): DashboardSummary {
  const record = requireRecord(body, "summary");
  return {
    collectedAt: str(record, "collected_at", "summary"),
    overall: parseHealthState(str(record, "overall", "summary")),
    stateCounts: parseStateCounts(record.state_counts),
    metrics: requireArray(record.metrics, "metrics").map((entry, index) =>
      parseMetric(requireRecord(entry, `metrics[${index}]`), index),
    ),
    metricsAvailable: bool(record, "metrics_available", "summary"),
    alerts: requireArray(record.alerts, "alerts").map((entry, index) =>
      parseAlert(requireRecord(entry, `alerts[${index}]`), index),
    ),
  };
}

/**
 * Reads the per-state counters, filling in any state the server omitted.
 *
 * Defaulting to zero here is safe in a way it is not for a metric: this is a
 * count of rows in the same payload, so a missing key means "none of them",
 * whereas a missing metric means "we could not find out".
 */
function parseStateCounts(raw: unknown): Record<HealthState, number> {
  const record = requireRecord(raw, "summary.state_counts");
  const counts = {} as Record<HealthState, number>;
  for (const state of HEALTH_STATES) {
    counts[state] = typeof record[state] === "number" ? num(record, state, "state_counts") : 0;
  }
  return counts;
}

function parseMetric(raw: Record<string, unknown>, index: number): PlatformMetric {
  const field = `metrics[${index}]`;
  const available = bool(raw, "available", field);
  const value = raw.value;
  // A metric that claims to be available and carries no number is a contract
  // mismatch, not a zero: rendering it as zero would put an invented figure on
  // an operational dashboard.
  if (available && typeof value !== "number") {
    throw contractError(`${field}.value ausente para uma métrica disponível`);
  }
  return {
    key: str(raw, "key", field),
    label: str(raw, "label", field),
    definition: str(raw, "definition", field),
    window: parseWindow(str(raw, "window", field)),
    unit: str(raw, "unit", field) === "bytes" ? "bytes" : "count",
    value: available ? (value as number) : null,
    available,
  };
}

function parseWindow(raw: string): MetricWindow {
  if (raw === "instant" || raw === "last_24h" || raw === "cumulative") return raw;
  throw contractError(`janela desconhecida: ${raw}`);
}

function parseAlert(raw: Record<string, unknown>, index: number): HealthAlert {
  const field = `alerts[${index}]`;
  return {
    serviceId: str(raw, "service_id", field),
    severity: str(raw, "severity", field) === "critical" ? "critical" : "warning",
    title: str(raw, "title", field),
    impact: str(raw, "impact", field),
    action: str(raw, "action", field),
    since: str(raw, "since", field),
    runbookPath: optionalString(raw, "runbook_path", field),
    configKey: optionalString(raw, "config_key", field),
  };
}

/** A field the API omits entirely rather than sending as null. */
function optionalString(raw: Record<string, unknown>, key: string, field: string): string {
  if (raw[key] === undefined) return "";
  return str(raw, key, field);
}

function optionalNumber(raw: Record<string, unknown>, key: string, field: string): number | null {
  if (raw[key] === undefined || raw[key] === null) return null;
  return num(raw, key, field);
}
