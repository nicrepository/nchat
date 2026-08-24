/**
 * The integration surface of the Admin API (issue #582).
 *
 * Two things this module never does, and both are the point:
 *
 *  - it never names a destination. There is no host, URL or port in any request
 *    below; the diagnostic identifies an integration and the server decides what
 *    that means, from its own environment;
 *  - it never asks for a credential's value, because no endpoint returns one. A
 *    sensitive setting arrives with `configured` and no `value` at all, and the
 *    parser refuses a payload that breaks that rule rather than rendering it.
 */

import { adminFetch } from "./client";
import { parseConfigSetting, type ConfigSetting } from "./configApi";
import { parseHealthState, type HealthState } from "./observabilityApi";
import { bool, num, requireArray, requireRecord, str, strList } from "./parse";

/** One step of a diagnostic, from the server's closed vocabulary. */
export type DiagnosticStage =
  | "resolve"
  | "connect"
  | "tls"
  | "discovery"
  | "issuer"
  | "jwks"
  | "credential"
  | "ready"
  | "delivery";

/**
 * What one stage concluded.
 *
 * `skipped` earns its place: a stage that did not run because an earlier one
 * failed, or because the protocol cannot prove it, is neither a pass nor a
 * failure, and rendering it as either would be a claim the server did not make.
 */
export type DiagnosticStatus = "passed" | "warning" | "failed" | "skipped";

const DIAGNOSTIC_STATUSES: readonly DiagnosticStatus[] = [
  "passed",
  "warning",
  "failed",
  "skipped",
] as const;

/**
 * Reads a stage result.
 *
 * An unrecognised status becomes `skipped` rather than throwing: a server that
 * gained a fifth one should degrade this console into honesty, not into a blank
 * screen. It must never become `passed`.
 */
export function parseDiagnosticStatus(raw: string): DiagnosticStatus {
  return DIAGNOSTIC_STATUSES.includes(raw as DiagnosticStatus)
    ? (raw as DiagnosticStatus)
    : "skipped";
}

export interface DiagnosticStep {
  stage: string;
  status: DiagnosticStatus;
  /** The sanitized failure category, from the same closed set the Health Center uses. */
  category: string;
  detail: string;
  /** Null when the stage did not run. Never zero as a stand-in. */
  latencyMS: number | null;
}

export interface DiagnosticReport {
  integration: string;
  startedAt: string;
  status: DiagnosticStatus;
  summary: string;
  version: string;
  steps: DiagnosticStep[];
}

export interface IntegrationAction {
  id: string;
  label: string;
  description: string;
  /** What the API requires. The console uses it to disable, never to authorize. */
  capability: string;
}

export interface IntegrationSetting extends ConfigSetting {
  /** Rarely-touched settings, rendered collapsed. */
  advanced: boolean;
}

export interface Integration {
  id: string;
  displayName: string;
  summary: string;
  category: string;
  runbookPath: string;
  /** The Health Center row this card links to. */
  healthServiceId: string;
  state: HealthState;
  enabled: boolean;
  observable: boolean;
  latencyMS: number | null;
  checkedAt: string;
  errorCategory: string;
  detail: string;
  version: string;
  diagnosable: boolean;
  /** Why no active check exists. Shown verbatim when `diagnosable` is false. */
  diagnosticUnsupported: string;
  /** The plan a diagnostic follows, so the console can render it before a run. */
  stages: string[];
  /**
   * Whether the inventory is visible at all.
   *
   * `false` with no settings is a permission, not an absence: the server hides
   * the configuration catalogue from an actor without `admin.config.read`, and
   * the two facts get different sentences on the screen.
   */
  settingsVisible: boolean;
  settings: IntegrationSetting[];
  actions: IntegrationAction[];
}

export interface IntegrationsView {
  collectedAt: string;
  integrations: Integration[];
}

function optionalText(raw: Record<string, unknown>, key: string): string {
  const value = raw[key];
  return typeof value === "string" ? value : "";
}

function optionalLatency(raw: Record<string, unknown>, key: string, field: string): number | null {
  return key in raw ? num(raw, key, field) : null;
}

function parseIntegrationSetting(raw: Record<string, unknown>, field: string): IntegrationSetting {
  return { ...parseConfigSetting(raw, field), advanced: bool(raw, "advanced", field) };
}

function parseAction(raw: Record<string, unknown>, field: string): IntegrationAction {
  return {
    id: str(raw, "id", field),
    label: str(raw, "label", field),
    description: str(raw, "description", field),
    capability: str(raw, "capability", field),
  };
}

function parseIntegration(raw: Record<string, unknown>, index: number): Integration {
  const field = `integrations[${index}]`;
  return {
    id: str(raw, "id", field),
    displayName: str(raw, "display_name", field),
    summary: str(raw, "summary", field),
    category: optionalText(raw, "category"),
    runbookPath: optionalText(raw, "runbook_path"),
    healthServiceId: optionalText(raw, "health_service_id"),
    state: parseHealthState(str(raw, "state", field)),
    enabled: bool(raw, "enabled", field),
    observable: bool(raw, "observable", field),
    latencyMS: optionalLatency(raw, "latency_ms", field),
    checkedAt: str(raw, "checked_at", field),
    errorCategory: optionalText(raw, "error_category"),
    detail: optionalText(raw, "detail"),
    version: optionalText(raw, "version"),
    diagnosable: bool(raw, "diagnosable", field),
    diagnosticUnsupported: optionalText(raw, "diagnostic_unsupported"),
    stages: strList(raw, "stages", field),
    settingsVisible: bool(raw, "settings_visible", field),
    settings: requireArray(raw.settings, `${field}.settings`).map((entry, position) =>
      parseIntegrationSetting(
        requireRecord(entry, `${field}.settings[${position}]`),
        `${field}.settings[${position}]`,
      ),
    ),
    actions: requireArray(raw.actions, `${field}.actions`).map((entry, position) =>
      parseAction(
        requireRecord(entry, `${field}.actions[${position}]`),
        `${field}.actions[${position}]`,
      ),
    ),
  };
}

function parseStep(raw: Record<string, unknown>, field: string): DiagnosticStep {
  return {
    stage: str(raw, "stage", field),
    status: parseDiagnosticStatus(str(raw, "status", field)),
    category: optionalText(raw, "category"),
    detail: optionalText(raw, "detail"),
    latencyMS: optionalLatency(raw, "latency_ms", field),
  };
}

function parseReport(value: unknown): DiagnosticReport {
  const raw = requireRecord(requireRecord(value, "data").report, "report");
  return {
    integration: str(raw, "integration", "report"),
    startedAt: str(raw, "started_at", "report"),
    status: parseDiagnosticStatus(str(raw, "status", "report")),
    summary: optionalText(raw, "summary"),
    version: optionalText(raw, "version"),
    steps: requireArray(raw.steps, "report.steps").map((entry, index) =>
      parseStep(requireRecord(entry, `report.steps[${index}]`), `report.steps[${index}]`),
    ),
  };
}

export async function loadIntegrations(signal?: AbortSignal): Promise<IntegrationsView> {
  const body = await adminFetch<unknown>("/integrations", { signal });
  const raw = requireRecord(body, "data");
  return {
    collectedAt: str(raw, "collected_at", "data"),
    integrations: requireArray(raw.integrations, "integrations").map((entry, index) =>
      parseIntegration(requireRecord(entry, `integrations[${index}]`), index),
    ),
  };
}

/**
 * Runs one integration's active check.
 *
 * The request names an integration and carries no body: what gets contacted is
 * the server's decision, made from its own environment. The signal is what lets
 * the page abandon a run when the operator navigates away.
 */
export async function diagnoseIntegration(
  id: string,
  signal?: AbortSignal,
): Promise<DiagnosticReport> {
  const body = await adminFetch<unknown>(`/integrations/${encodeURIComponent(id)}/diagnose`, {
    method: "POST",
    signal,
  });
  return parseReport(body);
}

/**
 * Delivers one fixed message through the configured relay.
 *
 * There is no recipient argument, and there is no recipient field in the
 * request: the server sends it to the authenticated administrator's own
 * address. That is what keeps the console from being usable as a mail relay,
 * and it is why this function takes nothing.
 */
export async function sendSMTPTestEmail(signal?: AbortSignal): Promise<DiagnosticReport> {
  const body = await adminFetch<unknown>("/integrations/smtp/test-email", {
    method: "POST",
    signal,
  });
  return parseReport(body);
}
